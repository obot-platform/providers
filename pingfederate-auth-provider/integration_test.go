package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
	"github.com/obot-platform/providers/pingfederate-auth-provider/internal/mockidp"
)

// These tests run the whole provider against a mock OpenID Connect identity
// provider: the browser-side login through oauth2-proxy, then the endpoints
// Obot calls afterwards with the resulting cookie and access token.

var adaUser = mockidp.User{
	Subject:           "sub-ada",
	Email:             "ada@example.com",
	Name:              "Ada Lovelace",
	GivenName:         "Ada",
	FamilyName:        "Lovelace",
	PreferredUsername: "ada",
	Picture:           "https://img.example.com/ada.png",
	Groups:            []string{"admins", "devs"},
}

func defaultIDPConfig() mockidp.Config {
	return mockidp.Config{
		ClientID:        "obot",
		ClientSecret:    "obot-secret",
		RequirePKCE:     true,
		GroupsInIDToken: true,
		EmailInIDToken:  true,
	}
}

type testIDP struct {
	*mockidp.IDP
	srv *httptest.Server
}

func startIDP(t *testing.T, cfg mockidp.Config, useTLS bool) *testIDP {
	t.Helper()
	idp := mockidp.New(cfg)
	idp.SetUser(adaUser)
	srv := httptest.NewUnstartedServer(idp)
	if useTLS {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	idp.SetIssuer(srv.URL)
	return &testIDP{IDP: idp, srv: srv}
}

type testProvider struct {
	url    string
	host   string
	srv    *server
	client *http.Client
	jar    *cookiejar.Jar
}

// providerEnv is every variable the provider reads, so each test starts from
// a clean environment regardless of what the process inherited.
var providerEnv = []string{
	"OBOT_PINGFEDERATE_AUTH_PROVIDER_CLIENT_ID", "OBOT_PINGFEDERATE_AUTH_PROVIDER_CLIENT_SECRET",
	"OBOT_PINGFEDERATE_AUTH_PROVIDER_ISSUER_URL", "OBOT_PINGFEDERATE_AUTH_PROVIDER_SCOPES",
	"OBOT_PINGFEDERATE_AUTH_PROVIDER_EMAIL_CLAIM", "OBOT_PINGFEDERATE_AUTH_PROVIDER_SKIP_NONCE",
	"OBOT_PINGFEDERATE_AUTH_PROVIDER_NAME_CLAIM", "OBOT_PINGFEDERATE_AUTH_PROVIDER_EMAIL_DISPLAY_DOMAIN",
	"OBOT_PINGFEDERATE_AUTH_PROVIDER_GROUPS_CLAIM", "OBOT_PINGFEDERATE_AUTH_PROVIDER_GROUPS_DELIMITER",
	"OBOT_PINGFEDERATE_AUTH_PROVIDER_KNOWN_GROUPS",
	"OBOT_PINGFEDERATE_AUTH_PROVIDER_ALLOW_UNVERIFIED_EMAIL", "OBOT_PINGFEDERATE_AUTH_PROVIDER_CODE_CHALLENGE_METHOD",
	"OBOT_PINGFEDERATE_AUTH_PROVIDER_EXTRA_AUDIENCES", "OBOT_PINGFEDERATE_AUTH_PROVIDER_SKIP_ISSUER_VERIFICATION",
	"OBOT_PINGFEDERATE_AUTH_PROVIDER_CA_CERTIFICATE", "OBOT_PINGFEDERATE_AUTH_PROVIDER_CA_FILE",
	"OBOT_PINGFEDERATE_AUTH_PROVIDER_SKIP_DISCOVERY", "OBOT_PINGFEDERATE_AUTH_PROVIDER_AUTHORIZATION_URL",
	"OBOT_PINGFEDERATE_AUTH_PROVIDER_TOKEN_URL", "OBOT_PINGFEDERATE_AUTH_PROVIDER_USERINFO_URL",
	"OBOT_PINGFEDERATE_AUTH_PROVIDER_JWKS_URL", "OBOT_PINGFEDERATE_AUTH_PROVIDER_USERINFO_CACHE_TTL",
	"OBOT_SERVER_PUBLIC_URL", "OBOT_SERVER_URL", "OBOT_AUTH_PROVIDER_POSTGRES_CONNECTION_DSN",
	"OBOT_AUTH_PROVIDER_POSTGRES_MAX_CONNECTIONS", "OBOT_AUTH_PROVIDER_POSTGRES_MAX_IDLE_CONNECTIONS",
	"OBOT_AUTH_PROVIDER_POSTGRES_CONNECTION_LIFETIME_SECONDS", "OBOT_AUTH_PROVIDER_COOKIE_SECRET",
	"OBOT_AUTH_PROVIDER_EMAIL_DOMAINS", "OBOT_AUTH_PROVIDER_TOKEN_REFRESH_DURATION", "OBOT_AUTH_PROVIDER_ENABLE_LOGGING",
}

// startProvider configures the provider through the environment, exactly as
// Obot does, and serves it on a loopback port that doubles as the Obot URL so
// that the identity provider's redirects land on it directly.
func startProvider(t *testing.T, issuer string, env map[string]string) (*testProvider, error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "http://" + ln.Addr().String()

	secret := cookieSecret(t)
	for _, name := range providerEnv {
		t.Setenv(name, "")
	}
	for name, value := range map[string]string{
		"OBOT_PINGFEDERATE_AUTH_PROVIDER_CLIENT_ID":     "obot",
		"OBOT_PINGFEDERATE_AUTH_PROVIDER_CLIENT_SECRET": "obot-secret",
		"OBOT_PINGFEDERATE_AUTH_PROVIDER_ISSUER_URL":    issuer,
		"OBOT_SERVER_URL":                  baseURL,
		"OBOT_AUTH_PROVIDER_COOKIE_SECRET": base64.StdEncoding.EncodeToString(secret),
		"OBOT_AUTH_PROVIDER_EMAIL_DOMAINS": "*",
	} {
		t.Setenv(name, value)
	}
	if os.Getenv("PINGFEDERATE_TEST_LOGGING") != "" {
		// Surfaces oauth2-proxy's own logs when debugging a failing flow.
		t.Setenv("OBOT_AUTH_PROVIDER_ENABLE_LOGGING", "true")
	}
	for name, value := range env {
		t.Setenv(name, value)
	}

	cfg, err := loadOptions()
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	srv, err := newServer(context.Background(), cfg, baseURL)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}

	httpServer := &http.Server{Handler: obotRootCallback(srv.handler())}
	go func() { _ = httpServer.Serve(ln) }()
	t.Cleanup(func() { _ = httpServer.Close() })

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	host := ln.Addr().String()
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Stop once the login flow hands the browser back to Obot.
			if req.URL.Host == host && (req.URL.Path == "/after-login" || req.URL.Path == "/bye") {
				return http.ErrUseLastResponse
			}
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
	return &testProvider{url: baseURL, host: host, srv: srv, client: client, jar: jar}, nil
}

// cookieSecret generates a secret the way Obot does: 32 random bytes that
// are unchanged by whitespace trimming.
func cookieSecret(t *testing.T) []byte {
	t.Helper()
	secret := make([]byte, 32)
	for {
		if _, err := rand.Read(secret); err != nil {
			t.Fatal(err)
		}
		if len(bytes.TrimSpace(secret)) == len(secret) {
			return secret
		}
	}
}

// obotRootCallback emulates the Obot server, which registers its own root
// URL as the OAuth redirect URI and bounces the identity provider's callback
// to the provider's /oauth2/callback (see pkg/oauth/redirect.go in Obot).
func obotRootCallback(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path == "/" && (q.Get("code") != "" || q.Get("error") != "" || q.Get("state") != "") {
			target := *r.URL
			target.Path = "/oauth2/callback"
			http.Redirect(w, r, target.String(), http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func mustStartProvider(t *testing.T, issuer string, env map[string]string) *testProvider {
	t.Helper()
	p, err := startProvider(t, issuer, env)
	if err != nil {
		t.Fatalf("start provider: %v", err)
	}
	return p
}

func (p *testProvider) login(t *testing.T) *http.Response {
	t.Helper()
	resp, err := p.client.Get(p.url + "/oauth2/start?rd=" + url.QueryEscape("/after-login"))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (p *testProvider) mustLogin(t *testing.T) {
	t.Helper()
	resp := p.login(t)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/after-login" {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("login did not end at the requested redirect: status=%d location=%q body=%s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	if p.sessionCookie() == nil {
		t.Fatal("login succeeded but no session cookie was set")
	}
}

func (p *testProvider) sessionCookie() *http.Cookie {
	u, _ := url.Parse(p.url)
	for _, c := range p.jar.Cookies(u) {
		if c.Name == "obot_access_token" {
			return c
		}
	}
	return nil
}

func (p *testProvider) cookieHeader() string {
	u, _ := url.Parse(p.url)
	var parts []string
	for _, c := range p.jar.Cookies(u) {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// getState calls /obot-get-state the way Obot does: with a serialized copy of
// the browser's request, of which only the Cookie header matters.
func (p *testProvider) getState(t *testing.T) (state.SerializableState, *http.Response) {
	t.Helper()
	body, err := json.Marshal(state.SerializableRequest{
		Method: http.MethodGet,
		URL:    p.url + "/api/me",
		Header: map[string][]string{"Cookie": {p.cookieHeader()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(p.url+"/obot-get-state", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	defer resp.Body.Close()
	var ss state.SerializableState
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&ss); err != nil {
			t.Fatalf("decode state: %v", err)
		}
	} else {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body = io.NopCloser(bytes.NewReader(raw))
	}
	return ss, resp
}

func (p *testProvider) mustGetState(t *testing.T) state.SerializableState {
	t.Helper()
	ss, resp := p.getState(t)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("get state: status=%d body=%s", resp.StatusCode, body)
	}
	return ss
}

func (p *testProvider) userInfo(t *testing.T, accessToken string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, p.url+"/obot-get-user-info", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("user info: %v", err)
	}
	defer resp.Body.Close()
	var profile map[string]any
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
			t.Fatalf("decode user info: %v", err)
		}
	}
	return resp.StatusCode, profile
}

func (p *testProvider) groups(t *testing.T, id string) (int, state.GroupInfoList) {
	t.Helper()
	resp, err := http.Post(p.url+"/obot-list-user-auth-groups", "text/plain", strings.NewReader(id))
	if err != nil {
		t.Fatalf("groups: %v", err)
	}
	defer resp.Body.Close()
	var groups state.GroupInfoList
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&groups); err != nil {
			t.Fatalf("decode groups: %v", err)
		}
	}
	return resp.StatusCode, groups
}

func groupNames(groups state.GroupInfoList) string {
	var parts []string
	for _, g := range groups {
		parts = append(parts, g.ID+"="+g.Name)
	}
	return strings.Join(parts, ",")
}

func TestLoginFlowEndToEnd(t *testing.T) {
	idp := startIDP(t, defaultIDPConfig(), false)
	p := mustStartProvider(t, idp.Issuer(), nil)

	p.mustLogin(t)

	auths := idp.Authorizations()
	if len(auths) != 1 {
		t.Fatalf("authorization requests = %d, want 1", len(auths))
	}
	a := auths[0]
	if a.CodeChallengeMethod != "S256" || a.CodeChallenge == "" {
		t.Errorf("PKCE not used: %+v", a)
	}
	if a.Scope != "openid email profile" {
		t.Errorf("scope = %q", a.Scope)
	}
	// Obot registers its root URL with the identity provider and forwards the
	// callback itself, so that is what the provider must send.
	if a.RedirectURI != p.url+"/" {
		t.Errorf("redirect_uri = %q, want %s/", a.RedirectURI, p.url)
	}
	if a.Nonce == "" || a.State == "" {
		t.Errorf("nonce or state missing: %+v", a)
	}

	ss := p.mustGetState(t)
	// Obot keys the identity on User, which is the stable OIDC subject.
	if ss.User != "sub-ada" || ss.Email != "ada@example.com" || ss.PreferredUsername != "ada" {
		t.Errorf("identity in state = user:%q email:%q preferred:%q", ss.User, ss.Email, ss.PreferredUsername)
	}
	if ss.AccessToken == "" || ss.IDToken == "" {
		t.Error("state is missing tokens")
	}
	if ss.ExpiresOn == nil || ss.ExpiresOn.Before(time.Now()) {
		t.Errorf("expiresOn = %v", ss.ExpiresOn)
	}
	if got := groupNames(ss.GroupInfos); got != "pingfederate/admins=admins,pingfederate/devs=devs" {
		t.Errorf("groupInfos = %q", got)
	}
	if len(ss.SetCookies) != 0 {
		t.Errorf("unexpected cookie refresh right after login: %v", ss.SetCookies)
	}

	before := idp.Counts().Userinfo
	status, profile := p.userInfo(t, ss.AccessToken)
	if status != http.StatusOK {
		t.Fatalf("user info status = %d", status)
	}
	for key, want := range map[string]string{"id": "sub-ada", "name": "Ada Lovelace", "email": "ada@example.com", "picture": "https://img.example.com/ada.png"} {
		if profile[key] != want {
			t.Errorf("profile[%s] = %v, want %q", key, profile[key], want)
		}
	}
	if idp.Counts().Userinfo != before+1 {
		t.Errorf("userinfo calls = %d, want %d", idp.Counts().Userinfo, before+1)
	}
	if status, _ := p.userInfo(t, ss.AccessToken); status != http.StatusOK || idp.Counts().Userinfo != before+1 {
		t.Errorf("second user info call was not served from cache (status %d, calls %d)", status, idp.Counts().Userinfo)
	}

	for _, id := range []string{"sub-ada", "ada@example.com"} {
		status, groups := p.groups(t, id)
		if status != http.StatusOK || groupNames(groups) != "pingfederate/admins=admins,pingfederate/devs=devs" {
			t.Errorf("groups(%s) = %d %q", id, status, groupNames(groups))
		}
	}
	if status, _ := p.groups(t, "someone-else"); status != http.StatusInternalServerError {
		t.Errorf("groups for unknown user: status = %d, want 500", status)
	}

	if status, _ := p.userInfo(t, "not-a-token"); status != http.StatusBadRequest {
		t.Errorf("user info with a bad token: status = %d, want 400", status)
	}
}

func TestLoginDisplaySettingsPreserveRawIdentity(t *testing.T) {
	cfg := defaultIDPConfig()
	cfg.EmailClaimName = "uid"
	idp := startIDP(t, cfg, false)
	user := adaUser
	user.Email = "ADA"
	idp.SetUser(user)
	p := mustStartProvider(t, idp.Issuer(), map[string]string{
		"OBOT_PINGFEDERATE_AUTH_PROVIDER_EMAIL_CLAIM":          "uid",
		"OBOT_PINGFEDERATE_AUTH_PROVIDER_NAME_CLAIM":           "preferred_username",
		"OBOT_PINGFEDERATE_AUTH_PROVIDER_EMAIL_DISPLAY_DOMAIN": "@Example.COM",
	})
	p.mustLogin(t)
	ss := p.mustGetState(t)
	if ss.User != "sub-ada" || ss.Email != "ADA" || ss.PreferredUsername != "ada" {
		t.Fatalf("display settings changed session identity: %+v", ss)
	}
	status, profile := p.userInfo(t, ss.AccessToken)
	if status != http.StatusOK || profile["id"] != "sub-ada" || profile["email"] != "ADA" || profile["name"] != "ada" || profile["email_display_domain"] != "example.com" {
		t.Fatalf("status = %d, profile = %v", status, profile)
	}
	for _, id := range []string{"sub-ada", "ADA"} {
		status, groups := p.groups(t, id)
		if status != http.StatusOK || groupNames(groups) != "pingfederate/admins=admins,pingfederate/devs=devs" {
			t.Errorf("groups(%s) = %d %q", id, status, groupNames(groups))
		}
	}
}

func TestKnownGroupsBackObotGroupSearch(t *testing.T) {
	idp := startIDP(t, defaultIDPConfig(), false)
	p := mustStartProvider(t, idp.Issuer(), map[string]string{
		"OBOT_PINGFEDERATE_AUTH_PROVIDER_KNOWN_GROUPS": "LDAP-Obot-Admins\nLDAP-Obot-Auditors",
	})

	listGroups := func(query string) (state.GroupInfoList, string) {
		resp, err := http.Get(p.url + "/obot-list-auth-groups?" + query)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list groups: status = %d", resp.StatusCode)
		}
		var page struct {
			Items      state.GroupInfoList `json:"items"`
			NextCursor string              `json:"nextCursor"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			t.Fatal(err)
		}
		return page.Items, page.NextCursor
	}

	// Declared groups are listed before anyone has signed in, so they can be
	// given roles in Obot up front.
	items, _ := listGroups("limit=50")
	if got := groupNames(items); got != "pingfederate/LDAP-Obot-Admins=LDAP-Obot-Admins,pingfederate/LDAP-Obot-Auditors=LDAP-Obot-Auditors" {
		t.Errorf("declared groups = %q", got)
	}

	p.mustLogin(t)
	p.mustGetState(t)
	items, _ = listGroups("limit=50")
	if got := groupNames(items); got != "pingfederate/admins=admins,pingfederate/devs=devs,pingfederate/LDAP-Obot-Admins=LDAP-Obot-Admins,pingfederate/LDAP-Obot-Auditors=LDAP-Obot-Auditors" {
		t.Errorf("groups after sign-in = %q", got)
	}
	items, _ = listGroups("name=ldap&limit=1")
	if len(items) != 1 || items[0].Name != "LDAP-Obot-Admins" {
		t.Errorf("filtered first page = %q", groupNames(items))
	}

	resp, err := http.Get(p.url + "/obot-get-auth-groups?ids=pingfederate%2FLDAP-Obot-Auditors")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var resolved struct {
		Items state.GroupInfoList `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&resolved); err != nil {
		t.Fatal(err)
	}
	if groupNames(resolved.Items) != "pingfederate/LDAP-Obot-Auditors=LDAP-Obot-Auditors" {
		t.Errorf("resolved = %q", groupNames(resolved.Items))
	}
}

func TestUnsupportedObotEndpointsAre404(t *testing.T) {
	idp := startIDP(t, defaultIDPConfig(), false)
	p := mustStartProvider(t, idp.Issuer(), nil)

	for _, path := range []string{"/obot-something-new", "/obot-get-icon-url"} {
		resp, err := http.Get(p.url + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 so Obot treats it as unsupported; body: %.80s", path, resp.StatusCode, body)
		}
		if strings.Contains(string(body), "<html") {
			t.Errorf("%s: answered by oauth2-proxy's HTML page instead of the provider", path)
		}
	}

	// Paths that are not Obot's still belong to oauth2-proxy.
	resp, err := http.Get(p.url + "/oauth2/sign_in")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/oauth2/sign_in: status = %d, want oauth2-proxy's sign-in page", resp.StatusCode)
	}
}

func TestGetStateWithoutSession(t *testing.T) {
	idp := startIDP(t, defaultIDPConfig(), false)
	p := mustStartProvider(t, idp.Issuer(), nil)

	_, resp := p.getState(t)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 for a request without a session cookie", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), "failed to get state: ") {
		t.Errorf("body = %q, want the error Obot inspects", body)
	}
}

func TestGroupsOnlyInUserinfo(t *testing.T) {
	cfg := defaultIDPConfig()
	cfg.GroupsInIDToken = false
	cfg.GroupsInUserinfo = true
	idp := startIDP(t, cfg, false)
	p := mustStartProvider(t, idp.Issuer(), nil)

	p.mustLogin(t)
	ss := p.mustGetState(t)
	if got := groupNames(ss.GroupInfos); got != "pingfederate/admins=admins,pingfederate/devs=devs" {
		t.Errorf("groupInfos = %q, want groups fetched from userinfo", got)
	}
	if status, groups := p.groups(t, "sub-ada"); status != http.StatusOK || len(groups) != 2 {
		t.Errorf("groups = %d %q", status, groupNames(groups))
	}
}

func TestEmailOnlyInUserinfo(t *testing.T) {
	cfg := defaultIDPConfig()
	cfg.EmailInIDToken = false
	idp := startIDP(t, cfg, false)
	p := mustStartProvider(t, idp.Issuer(), nil)

	p.mustLogin(t)
	if ss := p.mustGetState(t); ss.Email != "ada@example.com" {
		t.Errorf("email = %q, want it fetched from userinfo", ss.Email)
	}
}

func TestDelimitedGroupsClaim(t *testing.T) {
	cfg := defaultIDPConfig()
	cfg.GroupsAsDelimitedString = true
	cfg.GroupsClaimName = "memberOf"
	idp := startIDP(t, cfg, false)
	user := adaUser
	user.Groups = []string{"Domain Admins", "devs"}
	idp.SetUser(user)
	p := mustStartProvider(t, idp.Issuer(), map[string]string{
		"OBOT_PINGFEDERATE_AUTH_PROVIDER_GROUPS_CLAIM":     "memberOf",
		"OBOT_PINGFEDERATE_AUTH_PROVIDER_GROUPS_DELIMITER": ",",
	})

	p.mustLogin(t)
	ss := p.mustGetState(t)
	if got := groupNames(ss.GroupInfos); got != "pingfederate/Domain Admins=Domain Admins,pingfederate/devs=devs" {
		t.Errorf("groupInfos = %q", got)
	}
}

func TestUnverifiedEmail(t *testing.T) {
	cfg := defaultIDPConfig()
	idp := startIDP(t, cfg, false)
	user := adaUser
	unverified := false
	user.EmailVerified = &unverified
	idp.SetUser(user)

	t.Run("allowed by default", func(t *testing.T) {
		p := mustStartProvider(t, idp.Issuer(), nil)
		p.mustLogin(t)
	})

	t.Run("rejected when required", func(t *testing.T) {
		p := mustStartProvider(t, idp.Issuer(), map[string]string{"OBOT_PINGFEDERATE_AUTH_PROVIDER_ALLOW_UNVERIFIED_EMAIL": "false"})
		resp := p.login(t)
		if resp.StatusCode == http.StatusFound && resp.Header.Get("Location") == "/after-login" {
			t.Fatal("login with an unverified email should have failed")
		}
		if p.sessionCookie() != nil {
			t.Error("a session cookie was set despite the failed login")
		}
	})
}

func TestEmailDomainRestriction(t *testing.T) {
	idp := startIDP(t, defaultIDPConfig(), false)
	p := mustStartProvider(t, idp.Issuer(), map[string]string{"OBOT_AUTH_PROVIDER_EMAIL_DOMAINS": "other.example.org"})

	resp := p.login(t)
	if resp.StatusCode == http.StatusFound && resp.Header.Get("Location") == "/after-login" {
		t.Fatal("login from a disallowed email domain should have failed")
	}
	if p.sessionCookie() != nil {
		t.Error("a session cookie was set despite the failed login")
	}
}

func TestPKCEDisabled(t *testing.T) {
	cfg := defaultIDPConfig()
	cfg.RequirePKCE = false
	idp := startIDP(t, cfg, false)
	p := mustStartProvider(t, idp.Issuer(), map[string]string{"OBOT_PINGFEDERATE_AUTH_PROVIDER_CODE_CHALLENGE_METHOD": "none"})

	p.mustLogin(t)
	if a := idp.Authorizations()[0]; a.CodeChallenge != "" || a.CodeChallengeMethod != "" {
		t.Errorf("PKCE parameters sent although disabled: %+v", a)
	}
}

func TestCustomScopesAndClaims(t *testing.T) {
	cfg := defaultIDPConfig()
	cfg.EmailClaimName = "mail"
	idp := startIDP(t, cfg, false)
	p := mustStartProvider(t, idp.Issuer(), map[string]string{
		"OBOT_PINGFEDERATE_AUTH_PROVIDER_SCOPES":      "email, profile, groups",
		"OBOT_PINGFEDERATE_AUTH_PROVIDER_EMAIL_CLAIM": "mail",
	})

	p.mustLogin(t)
	if a := idp.Authorizations()[0]; a.Scope != "openid email profile groups" {
		t.Errorf("scope = %q", a.Scope)
	}
	ss := p.mustGetState(t)
	if ss.User != "sub-ada" || ss.Email != "ada@example.com" {
		t.Errorf("user = %q email = %q, want the subject and the mail claim", ss.User, ss.Email)
	}
	if status, profile := p.userInfo(t, ss.AccessToken); status != http.StatusOK || profile["email"] != "ada@example.com" || profile["id"] != "sub-ada" {
		t.Errorf("user info = %d %v", status, profile)
	}
	if status, groups := p.groups(t, "ada@example.com"); status != http.StatusOK || len(groups) != 2 {
		t.Errorf("groups by email = %d %q", status, groupNames(groups))
	}
}

func TestSkipNonce(t *testing.T) {
	idp := startIDP(t, defaultIDPConfig(), false)
	p := mustStartProvider(t, idp.Issuer(), map[string]string{"OBOT_PINGFEDERATE_AUTH_PROVIDER_SKIP_NONCE": "true"})

	p.mustLogin(t)
	if a := idp.Authorizations()[0]; a.Nonce != "" {
		t.Errorf("nonce sent although verification is skipped: %+v", a)
	}
	p.mustGetState(t)
}

func TestSkipDiscovery(t *testing.T) {
	idp := startIDP(t, defaultIDPConfig(), false)
	p := mustStartProvider(t, idp.Issuer(), map[string]string{
		"OBOT_PINGFEDERATE_AUTH_PROVIDER_SKIP_DISCOVERY":    "true",
		"OBOT_PINGFEDERATE_AUTH_PROVIDER_AUTHORIZATION_URL": idp.Issuer() + mockidp.AuthorizePath,
		"OBOT_PINGFEDERATE_AUTH_PROVIDER_TOKEN_URL":         idp.Issuer() + mockidp.TokenPath,
		"OBOT_PINGFEDERATE_AUTH_PROVIDER_JWKS_URL":          idp.Issuer() + mockidp.JWKSPath,
		"OBOT_PINGFEDERATE_AUTH_PROVIDER_USERINFO_URL":      idp.Issuer() + mockidp.UserinfoPath,
	})

	p.mustLogin(t)
	if idp.Counts().Discovery != 0 {
		t.Errorf("discovery document fetched %d times, want 0", idp.Counts().Discovery)
	}
	ss := p.mustGetState(t)
	if status, profile := p.userInfo(t, ss.AccessToken); status != http.StatusOK || profile["id"] != "sub-ada" {
		t.Errorf("user info = %d %v", status, profile)
	}
}

func TestIssuerMismatchIsReported(t *testing.T) {
	idp := startIDP(t, defaultIDPConfig(), false)
	// A trailing slash is a common misconfiguration: the discovery document
	// reports the issuer without it.
	if _, err := startProvider(t, idp.Issuer()+"/", nil); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("expected an issuer mismatch error, got %v", err)
	}

	p := mustStartProvider(t, idp.Issuer()+"/", map[string]string{"OBOT_PINGFEDERATE_AUTH_PROVIDER_SKIP_ISSUER_VERIFICATION": "true"})
	p.mustLogin(t)
}

func TestPrivateCA(t *testing.T) {
	idp := startIDP(t, defaultIDPConfig(), true)
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: idp.srv.Certificate().Raw}))

	// Without the CA the provider must refuse to start rather than talk to
	// an identity provider it cannot verify. This case runs first because
	// oauth2-proxy installs trusted CAs process-wide once configured.
	if _, err := startProvider(t, idp.Issuer(), nil); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("expected a certificate error without the CA, got %v", err)
	}

	t.Run("CA as PEM text", func(t *testing.T) {
		p := mustStartProvider(t, idp.Issuer(), map[string]string{"OBOT_PINGFEDERATE_AUTH_PROVIDER_CA_CERTIFICATE": caPEM})
		p.mustLogin(t)
		ss := p.mustGetState(t)
		if status, profile := p.userInfo(t, ss.AccessToken); status != http.StatusOK || profile["id"] != "sub-ada" {
			t.Errorf("user info = %d %v", status, profile)
		}
		for _, f := range p.srv.cfg.caFiles {
			if info, err := os.Stat(f); err != nil || info.Mode().Perm() != 0o600 {
				t.Errorf("temporary CA file %s: %v %v", f, info, err)
			}
			_ = os.Remove(f)
		}
	})

	t.Run("CA as file", func(t *testing.T) {
		caFile := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(caFile, []byte(caPEM), 0o600); err != nil {
			t.Fatal(err)
		}
		p := mustStartProvider(t, idp.Issuer(), map[string]string{"OBOT_PINGFEDERATE_AUTH_PROVIDER_CA_FILE": caFile})
		p.mustLogin(t)
	})
}

func TestTokenRefresh(t *testing.T) {
	idp := startIDP(t, defaultIDPConfig(), false)
	p := mustStartProvider(t, idp.Issuer(), map[string]string{"OBOT_AUTH_PROVIDER_TOKEN_REFRESH_DURATION": "1s"})

	p.mustLogin(t)
	if ss := p.mustGetState(t); len(ss.SetCookies) != 0 {
		t.Fatalf("unexpected refresh immediately after login: %v", ss.SetCookies)
	}

	// Session age is measured in whole seconds, so wait past the next tick.
	time.Sleep(2100 * time.Millisecond)
	ss := p.mustGetState(t)
	if len(ss.SetCookies) == 0 {
		t.Fatal("expected a refreshed session cookie after the refresh interval")
	}
	if idp.Counts().Refresh != 1 {
		t.Errorf("refresh token grants = %d, want 1", idp.Counts().Refresh)
	}
	if ss.Email != "ada@example.com" {
		t.Errorf("email after refresh = %q", ss.Email)
	}

	// Apply the refreshed cookies as a browser would and check the session is still good.
	header := http.Header{}
	for _, c := range ss.SetCookies {
		header.Add("Set-Cookie", c)
	}
	u, _ := url.Parse(p.url)
	p.jar.SetCookies(u, (&http.Response{Header: header}).Cookies())
	if ss := p.mustGetState(t); len(ss.SetCookies) != 0 || ss.Email != "ada@example.com" {
		t.Errorf("state after applying refreshed cookies: setCookies=%v email=%q", ss.SetCookies, ss.Email)
	}
}

func TestSignOut(t *testing.T) {
	idp := startIDP(t, defaultIDPConfig(), false)
	p := mustStartProvider(t, idp.Issuer(), nil)
	p.mustLogin(t)

	resp, err := p.client.Get(p.url + "/oauth2/sign_out?rd=" + url.QueryEscape("/bye"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/bye" {
		t.Errorf("sign out: status=%d location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if p.sessionCookie() != nil {
		t.Error("session cookie survived sign out")
	}
	if _, resp := p.getState(t); resp.StatusCode == http.StatusOK {
		t.Error("state still available after sign out")
	}
}
