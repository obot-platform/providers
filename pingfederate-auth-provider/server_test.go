package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
)

// newTestServer builds a server without oauth2-proxy, for the endpoints that
// do not need a session.
func newTestServer(t *testing.T, userinfoURL string, cacheTTL time.Duration) *server {
	t.Helper()
	o := validOptions()
	o.UserinfoCacheTTL = cacheTTL.String()
	cfg, err := o.resolve()
	if err != nil {
		t.Fatal(err)
	}
	s := &server{
		cfg:           cfg,
		httpClient:    http.DefaultClient,
		userinfoURL:   userinfoURL,
		listenURL:     "http://127.0.0.1:9999",
		groups:        expirable.NewLRU[string, []string](cacheSize, nil, cacheTTL+time.Hour),
		idTokenClaims: expirable.NewLRU[string, map[string]any](cacheSize, nil, time.Hour),
		directory:     newGroupDirectory(cfg.knownGroups),
	}
	if cacheTTL > 0 {
		s.userinfo = expirable.NewLRU[string, map[string]any](cacheSize, nil, cacheTTL)
	}
	return s
}

func userinfoServer(t *testing.T, calls *atomic.Int32, claims map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(claims)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func getUserInfo(t *testing.T, s *server, authorization string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/obot-get-user-info", nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	s.getUserInfo(rec, req)
	var body map[string]any
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode user info: %v", err)
		}
	}
	return rec, body
}

func listGroups(t *testing.T, s *server, id string) (*httptest.ResponseRecorder, state.GroupInfoList) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/obot-list-user-auth-groups", strings.NewReader(id))
	rec := httptest.NewRecorder()
	s.listUserAuthGroups(rec, req)
	var groups state.GroupInfoList
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &groups); err != nil {
			t.Fatalf("decode groups: %v", err)
		}
	}
	return rec, groups
}

func TestGetUserInfoShapesProfileAndRecordsGroups(t *testing.T) {
	var calls atomic.Int32
	upstream := userinfoServer(t, &calls, map[string]any{
		"sub":     "sub-1",
		"email":   "ada@example.com",
		"name":    "Ada Lovelace",
		"picture": "https://img.example.com/ada.png",
		"groups":  []string{"admins", "devs"},
		"custom":  "kept",
	})
	s := newTestServer(t, upstream.URL, 5*time.Minute)

	rec, profile := getUserInfo(t, s, "Bearer tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	for key, want := range map[string]string{
		"id": "sub-1", "name": "Ada Lovelace", "email": "ada@example.com",
		"picture": "https://img.example.com/ada.png", "custom": "kept", "sub": "sub-1",
	} {
		if profile[key] != want {
			t.Errorf("profile[%s] = %v, want %q", key, profile[key], want)
		}
	}

	for _, id := range []string{"sub-1", "ada@example.com"} {
		rec, groups := listGroups(t, s, id)
		if rec.Code != http.StatusOK {
			t.Fatalf("groups(%s) status = %d: %s", id, rec.Code, rec.Body)
		}
		if len(groups) != 2 || groups[0].ID != "pingfederate/admins" || groups[0].Name != "admins" || groups[1].ID != "pingfederate/devs" {
			t.Errorf("groups(%s) = %+v", id, groups)
		}
	}
}

func TestGetUserInfoCachesUpstreamCalls(t *testing.T) {
	var calls atomic.Int32
	upstream := userinfoServer(t, &calls, map[string]any{"sub": "sub-1", "email": "ada@example.com"})

	cached := newTestServer(t, upstream.URL, 5*time.Minute)
	for range 3 {
		if rec, _ := getUserInfo(t, cached, "Bearer tok"); rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("upstream calls with cache = %d, want 1", calls.Load())
	}

	calls.Store(0)
	uncached := newTestServer(t, upstream.URL, 0)
	for range 3 {
		if rec, _ := getUserInfo(t, uncached, "Bearer tok"); rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
	}
	if calls.Load() != 3 {
		t.Errorf("upstream calls without cache = %d, want 3", calls.Load())
	}
}

func TestGetUserInfoFallsBackToIDTokenClaims(t *testing.T) {
	var calls atomic.Int32
	upstream := userinfoServer(t, &calls, nil) // rejects everything but "tok"
	s := newTestServer(t, upstream.URL, time.Minute)

	rec, _ := getUserInfo(t, s, "Bearer unknown")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("without cached claims: status = %d, want 400", rec.Code)
	}

	s.idTokenClaims.Add("unknown", map[string]any{"sub": "sub-2", "email": "bob@example.com", "given_name": "Bob", "family_name": "Builder"})
	rec, profile := getUserInfo(t, s, "Bearer unknown")
	if rec.Code != http.StatusOK {
		t.Fatalf("with cached claims: status = %d: %s", rec.Code, rec.Body)
	}
	if profile["id"] != "sub-2" || profile["name"] != "Bob Builder" || profile["email"] != "bob@example.com" {
		t.Errorf("profile = %v", profile)
	}

	noEndpoint := newTestServer(t, "", time.Minute)
	noEndpoint.idTokenClaims.Add("unknown", map[string]any{"sub": "sub-3"})
	if rec, profile := getUserInfo(t, noEndpoint, "Bearer unknown"); rec.Code != http.StatusOK || profile["id"] != "sub-3" {
		t.Errorf("without a userinfo endpoint: status = %d, profile = %v", rec.Code, profile)
	}
}

func TestGetUserInfoRequiresToken(t *testing.T) {
	s := newTestServer(t, "http://127.0.0.1:1", time.Minute)
	if rec, _ := getUserInfo(t, s, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if rec, _ := getUserInfo(t, s, "Bearer "); rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestGetUserInfoKeepsIDTokenGroupsWhenUserinfoHasNoGroupsClaim(t *testing.T) {
	var calls atomic.Int32
	upstream := userinfoServer(t, &calls, map[string]any{"sub": "sub-1", "email": "ada@example.com"})
	s := newTestServer(t, upstream.URL, time.Minute)
	s.rememberGroups([]string{"admins"}, "sub-1")

	if rec, _ := getUserInfo(t, s, "Bearer tok"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if _, groups := listGroups(t, s, "sub-1"); len(groups) != 1 || groups[0].ID != "pingfederate/admins" {
		t.Errorf("groups = %+v, want the ID token groups to survive", groups)
	}
}

func TestListUserAuthGroups(t *testing.T) {
	s := newTestServer(t, "", time.Minute)

	if rec, _ := listGroups(t, s, "  "); rec.Code != http.StatusBadRequest {
		t.Errorf("empty id: status = %d, want 400", rec.Code)
	}
	if rec, _ := listGroups(t, s, "nobody"); rec.Code != http.StatusInternalServerError {
		t.Errorf("unknown id: status = %d, want 500 so Obot keeps its cached memberships", rec.Code)
	}

	s.rememberGroups(nil, "loner")
	rec, groups := listGroups(t, s, "loner")
	if rec.Code != http.StatusOK || groups == nil || len(groups) != 0 {
		t.Errorf("known user without groups: status = %d, groups = %#v, want an empty list", rec.Code, groups)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("body = %q, want []", body)
	}

	s.rememberGroups([]string{" ops ", "", "sec"}, "trimmed")
	if _, groups := listGroups(t, s, "trimmed\n"); len(groups) != 2 || groups[0].ID != "pingfederate/ops" || groups[1].Name != "sec" {
		t.Errorf("groups = %+v", groups)
	}
}

func TestRecordSession(t *testing.T) {
	s := newTestServer(t, "", time.Minute)
	idToken := "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"sub-9","email":"z@example.com","groups":["admins","devs"]}`)) + ".s"

	ss := state.SerializableState{AccessToken: "tok", IDToken: idToken, User: "z@example.com", Email: "z@example.com"}
	s.recordSession(&ss)
	if len(ss.GroupInfos) != 2 || ss.GroupInfos[0].ID != "pingfederate/admins" || ss.GroupInfos[1].Name != "devs" {
		t.Errorf("GroupInfos from ID token claims = %+v", ss.GroupInfos)
	}
	for _, id := range []string{"sub-9", "z@example.com"} {
		if rec, groups := listGroups(t, s, id); rec.Code != http.StatusOK || len(groups) != 2 {
			t.Errorf("groups(%s): status = %d, groups = %+v", id, rec.Code, groups)
		}
	}
	if claims, ok := s.idTokenClaims.Get("tok"); !ok || claims["sub"] != "sub-9" {
		t.Errorf("ID token claims not cached by access token: %v %v", claims, ok)
	}

	// Groups oauth2-proxy already extracted take precedence over the token.
	ss = state.SerializableState{AccessToken: "tok2", IDToken: idToken, User: "z@example.com", Groups: []string{"from-session"}}
	s.recordSession(&ss)
	if len(ss.GroupInfos) != 1 || ss.GroupInfos[0].ID != "pingfederate/from-session" {
		t.Errorf("GroupInfos = %+v, want the session groups", ss.GroupInfos)
	}

	// A session without an ID token (refreshed without one) still records groups.
	ss = state.SerializableState{AccessToken: "tok3", User: "u", Groups: []string{"g"}}
	s.recordSession(&ss)
	if _, groups := listGroups(t, s, "u"); len(groups) != 1 {
		t.Errorf("groups without ID token = %+v", groups)
	}
	if _, ok := s.idTokenClaims.Get("tok3"); ok {
		t.Error("no claims should be cached without an ID token")
	}

	// A garbage ID token is tolerated.
	ss = state.SerializableState{AccessToken: "tok4", IDToken: "garbage", User: "v"}
	s.recordSession(&ss)
	if rec, groups := listGroups(t, s, "v"); rec.Code != http.StatusOK || len(groups) != 0 {
		t.Errorf("garbage token: status = %d, groups = %+v", rec.Code, groups)
	}
}

func TestRecordSessionSplitsDelimitedGroups(t *testing.T) {
	s := newTestServer(t, "", time.Minute)
	s.cfg.GroupsDelimiter = ","
	idToken := "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"sub-d","groups":"Domain Admins, devs"}`)) + ".s"

	ss := state.SerializableState{AccessToken: "tok", IDToken: idToken, User: "d"}
	s.recordSession(&ss)
	if len(ss.GroupInfos) != 2 || ss.GroupInfos[0].Name != "Domain Admins" || ss.GroupInfos[1].Name != "devs" {
		t.Errorf("GroupInfos from delimited claim = %+v", ss.GroupInfos)
	}

	// oauth2-proxy hands a delimited string claim over as a single group.
	ss = state.SerializableState{AccessToken: "tok2", User: "d2", Groups: []string{"ops,sec"}}
	s.recordSession(&ss)
	if len(ss.GroupInfos) != 2 || ss.GroupInfos[0].ID != "pingfederate/ops" || ss.GroupInfos[1].ID != "pingfederate/sec" {
		t.Errorf("GroupInfos from delimited session groups = %+v", ss.GroupInfos)
	}

	s.cfg.GroupsDelimiter = ""
	ss = state.SerializableState{AccessToken: "tok3", User: "d3", Groups: []string{"CN=Admins,OU=Groups"}}
	s.recordSession(&ss)
	if len(ss.GroupInfos) != 1 || ss.GroupInfos[0].Name != "CN=Admins,OU=Groups" {
		t.Errorf("GroupInfos without delimiter = %+v, want the value kept whole", ss.GroupInfos)
	}
}

func TestProfileIDFallbacks(t *testing.T) {
	s := newTestServer(t, "", time.Minute)
	if p := s.profile(map[string]any{"sub": "s", "email": "e"}); p["id"] != "s" {
		t.Errorf("id = %v, want sub", p["id"])
	}
	if p := s.profile(map[string]any{"email": "e"}); p["id"] != "e" {
		t.Errorf("id = %v, want the email claim", p["id"])
	}
	s.cfg.EmailClaim = "mail"
	if p := s.profile(map[string]any{"mail": "m", "email": "e"}); p["id"] != "m" || p["email"] != "m" || p["name"] != "m" {
		t.Errorf("custom email claim: %v", p)
	}
	if p := s.profile(map[string]any{}); p["id"] != "" || p["name"] != "" {
		t.Errorf("empty claims: %v", p)
	}
	if _, ok := s.profile(map[string]any{"sub": "s"})["email"]; ok {
		t.Error("email should be absent when the claim is missing")
	}
}

func TestBearerToken(t *testing.T) {
	cases := map[string]string{
		"Bearer abc":   "abc",
		"bearer abc":   "abc",
		"BEARER  abc ": "abc",
		"abc":          "abc",
		"":             "",
		"Bearer ":      "",
	}
	for in, want := range cases {
		if got := bearerToken(in); got != want {
			t.Errorf("bearerToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRootReportsListenURL(t *testing.T) {
	s := newTestServer(t, "", time.Minute)
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(s.listenURL)) })
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Body.String() != "http://127.0.0.1:9999" {
		t.Errorf("body = %q", rec.Body.String())
	}
}
