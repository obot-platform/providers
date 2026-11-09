package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	oauth2proxy "github.com/oauth2-proxy/oauth2-proxy/v7"
	"github.com/oauth2-proxy/oauth2-proxy/v7/pkg/apis/options"
	"github.com/oauth2-proxy/oauth2-proxy/v7/pkg/validation"
	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
	"github.com/obot-platform/providers/pingfederate-auth-provider/pkg/oidc"
)

const (
	providerName = "pingfederate-auth-provider"

	// groupIDPrefix namespaces the group IDs this provider reports. Obot
	// rejects groups outside the prefix declared in the manifest, so this
	// constant and the manifest's groupIDPrefix must agree.
	groupIDPrefix = "pingfederate/"

	cacheSize    = 10000
	cacheTTL     = 24 * time.Hour
	maxBodyBytes = 64 << 10
)

// server wires oauth2-proxy (which owns the OAuth2/OIDC login flow and the
// session cookie) to the extra endpoints Obot calls on an auth provider.
type server struct {
	cfg         *settings
	proxy       *oauth2proxy.OAuthProxy
	httpClient  *http.Client
	userinfoURL string
	listenURL   string

	// groups maps every identifier Obot might use for a user (subject, user
	// claim, email) to the group names last seen for that user. Obot asks for
	// groups by the ID it learned from /obot-get-user-info, without a token,
	// so the answer has to come from what earlier calls recorded.
	groups *expirable.LRU[string, []string]
	// idTokenClaims maps an access token to the claims of the ID token issued
	// with it, as a fallback profile when the userinfo endpoint is unavailable.
	idTokenClaims *expirable.LRU[string, map[string]any]
	// userinfo caches userinfo responses by access token. Obot asks for the
	// profile on nearly every request, and PingFederate should not see that.
	userinfo *expirable.LRU[string, map[string]any]
	// directory answers Obot's group search and lookups.
	directory *groupDirectory
}

func newServer(ctx context.Context, cfg *settings, listenURL string) (*server, error) {
	httpClient, err := oidc.NewHTTPClient(cfg.caFiles)
	if err != nil {
		return nil, err
	}

	userinfoURL := strings.TrimSpace(cfg.UserinfoURL)
	if !cfg.skipDiscovery {
		discovery, err := oidc.Discover(ctx, httpClient, cfg.issuerURL)
		if err != nil {
			return nil, fmt.Errorf("OIDC discovery failed for issuer %s: %w", cfg.issuerURL, err)
		}
		if !cfg.skipIssuerVerification && discovery.Issuer != cfg.issuerURL {
			return nil, fmt.Errorf("the discovery document reports issuer %q, which does not match the configured issuer URL %q; use the reported value, or enable skipping issuer verification", discovery.Issuer, cfg.issuerURL)
		}
		if userinfoURL == "" {
			userinfoURL = discovery.UserinfoEndpoint
		}
	}
	if userinfoURL == "" {
		slog.Warn("no userinfo endpoint is configured or discovered; user profiles will be read from ID token claims only")
	}

	proxy, err := newOAuthProxy(cfg, userinfoURL)
	if err != nil {
		return nil, err
	}

	s := &server{
		cfg:           cfg,
		proxy:         proxy,
		httpClient:    httpClient,
		userinfoURL:   userinfoURL,
		listenURL:     listenURL,
		groups:        expirable.NewLRU[string, []string](cacheSize, nil, cacheTTL),
		idTokenClaims: expirable.NewLRU[string, map[string]any](cacheSize, nil, cacheTTL),
		directory:     newGroupDirectory(cfg.knownGroups),
	}
	if cfg.userinfoCacheTTL > 0 {
		s.userinfo = expirable.NewLRU[string, map[string]any](cacheSize, nil, cfg.userinfoCacheTTL)
	}
	return s, nil
}

// newOAuthProxy configures oauth2-proxy's generic OpenID Connect provider for
// PingFederate. Everything Obot-specific (cookie name, redirect URL, session
// store) mirrors the other Obot auth providers so the gateway treats this one
// the same way.
func newOAuthProxy(cfg *settings, userinfoURL string) (*oauth2proxy.OAuthProxy, error) {
	legacyOpts := options.NewLegacyOptions()
	provider := &legacyOpts.LegacyProvider
	provider.ProviderType = "oidc"
	provider.ProviderName = "PingFederate"
	provider.ClientID = cfg.ClientID
	provider.ClientSecret = cfg.ClientSecret
	provider.OIDCIssuerURL = cfg.issuerURL
	provider.Scope = cfg.scopes
	provider.OIDCEmailClaim = cfg.EmailClaim
	// The session's User field, which Obot stores as the provider user ID,
	// is the OIDC subject: oauth2-proxy's default for this provider type and
	// what Obot's other OIDC providers report. Its legacy "user ID claim"
	// option is deliberately left alone, because setting it to anything but
	// "email" silently redefines the email claim as well.
	provider.OIDCGroupsClaim = cfg.GroupsClaim
	provider.InsecureOIDCAllowUnverifiedEmail = cfg.allowUnverifiedEmail
	provider.InsecureOIDCSkipIssuerVerification = cfg.skipIssuerVerification
	provider.InsecureOIDCSkipNonce = cfg.skipNonce
	provider.OIDCExtraAudiences = cfg.extraAudiences
	provider.CodeChallengeMethod = cfg.codeChallengeMethod
	if len(cfg.caFiles) > 0 {
		provider.ProviderCAFiles = cfg.caFiles
		provider.UseSystemTrustStore = true
	}
	if cfg.skipDiscovery {
		provider.SkipOIDCDiscovery = true
		provider.LoginURL = strings.TrimSpace(cfg.AuthorizationURL)
		provider.RedeemURL = strings.TrimSpace(cfg.TokenURL)
		provider.OIDCJwksURL = strings.TrimSpace(cfg.JWKSURL)
		provider.ProfileURL = userinfoURL
	}

	opts, err := legacyOpts.ToOptions()
	if err != nil {
		return nil, fmt.Errorf("failed to convert legacy options: %w", err)
	}

	opts.Server.BindAddress = ""
	opts.MetricsServer.BindAddress = ""
	if cfg.PostgresConnectionDSN != "" {
		opts.Session.Type = options.PostgresSessionStoreType
		opts.Session.Postgres.ConnectionDSN = cfg.PostgresConnectionDSN
		opts.Session.Postgres.MaxOpenConns = cfg.PostgresMaxConnections
		opts.Session.Postgres.MaxIdleConns = cfg.PostgresMaxIdleConnections
		opts.Session.Postgres.ConnMaxLifetime = cfg.PostgresConnectionLifetimeSeconds
		opts.Session.Postgres.TableNamePrefix = "pingfederate_"
	}
	opts.Cookie.Refresh = cfg.refreshDuration
	opts.Cookie.Name = "obot_access_token"
	opts.Cookie.Secret = cfg.cookieSecret
	opts.Cookie.Secure = strings.HasPrefix(cfg.ObotServerURL, "https://")
	opts.Cookie.CSRFExpire = 30 * time.Minute
	opts.RawRedirectURL = cfg.ObotServerURL + "/"
	opts.EmailDomains = cfg.emailDomains
	opts.Logging.RequestEnabled = cfg.loggingEnabled
	opts.Logging.AuthEnabled = cfg.loggingEnabled
	opts.Logging.StandardEnabled = cfg.loggingEnabled

	if err := validation.Validate(opts); err != nil {
		return nil, fmt.Errorf("invalid oauth2-proxy configuration: %w", err)
	}

	proxy, err := oauth2proxy.NewOAuthProxy(opts, oauth2proxy.NewValidator(opts.EmailDomains, opts.AuthenticatedEmailsFile))
	if err != nil {
		return nil, fmt.Errorf("failed to create oauth2 proxy: %w", err)
	}
	return proxy, nil
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(s.listenURL))
	})
	mux.HandleFunc("/obot-get-state", s.getState)
	mux.HandleFunc("/obot-get-user-info", s.getUserInfo)
	mux.HandleFunc("/obot-list-user-auth-groups", s.listUserAuthGroups)
	mux.HandleFunc("/obot-list-auth-groups", s.listAuthGroups)
	mux.HandleFunc("/obot-get-auth-groups", s.getAuthGroups)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/obot-") {
			// An Obot-internal endpoint this provider does not know must not
			// fall through to oauth2-proxy, whose sign-in page would be
			// mistaken for a provider failure.
			notSupported(w, r)
			return
		}
		s.proxy.ServeHTTP(w, r)
	})
	return mux
}

func notSupported(w http.ResponseWriter, r *http.Request) {
	http.Error(w, fmt.Sprintf("%s is not supported by %s", r.URL.Path, providerName), http.StatusNotFound)
}

// getState answers Obot's question "who sent this request?" from the session
// cookie, refreshing the upstream tokens when due, and records what it learned
// about the user's groups for later lookups.
func (s *server) getState(w http.ResponseWriter, r *http.Request) {
	var sr state.SerializableRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&sr); err != nil {
		http.Error(w, fmt.Sprintf("failed to decode request body: %v", err), http.StatusBadRequest)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), sr.Method, sr.URL, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create request object: %v", err), http.StatusBadRequest)
		return
	}
	req.Header = sr.Header

	ss, err := state.GetSerializableState(s.proxy, req)
	if err != nil {
		// An invalid session answers 401 so Obot clears the cookie instead of
		// failing the request; anything else is a provider failure.
		state.WriteStateError(w, err)
		return
	}

	s.recordSession(&ss)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(ss); err != nil {
		http.Error(w, fmt.Sprintf("failed to encode state: %v", err), http.StatusInternalServerError)
	}
}

// recordSession fills in the group information for a session and remembers
// it under every identifier Obot may later use to ask for the user's groups.
func (s *server) recordSession(ss *state.SerializableState) {
	claims := map[string]any{}
	if ss.IDToken != "" {
		// oauth2-proxy verified this token when it created or refreshed the
		// session, so reading its claims without re-verifying is safe.
		parsed, err := oidc.UnverifiedClaims(ss.IDToken)
		if err != nil {
			slog.Warn("could not read ID token claims", "error", err)
		} else {
			claims = parsed
		}
	}

	groups := s.splitGroups(ss.Groups)
	if len(groups) == 0 {
		groups = oidc.StringsClaim(claims, s.cfg.GroupsClaim, s.cfg.GroupsDelimiter)
	}
	ss.GroupInfos = groupInfos(groups)

	s.rememberGroups(groups, oidc.StringClaim(claims, "sub"), oidc.StringClaim(claims, s.cfg.EmailClaim), ss.User, ss.Email)
	if ss.AccessToken != "" && len(claims) > 0 {
		s.idTokenClaims.Add(ss.AccessToken, claims)
	}
}

// getUserInfo returns the user's profile for the access token in the
// Authorization header. Obot reads "id" (the subject, used for group lookups),
// and would read "name" and "picture" if it knew this provider by name.
func (s *server) getUserInfo(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		http.Error(w, "missing access token", http.StatusBadRequest)
		return
	}

	claims, err := s.userinfoClaims(r.Context(), token)
	if err != nil {
		cached, ok := s.idTokenClaims.Get(token)
		if !ok {
			http.Error(w, fmt.Sprintf("failed to fetch user info: %v", err), http.StatusBadRequest)
			return
		}
		slog.Warn("userinfo request failed; answering from ID token claims", "error", err)
		claims = cached
	}

	// PingFederate can release a display-name attribute only in the ID token.
	// Fill missing name fields from the already-verified session token, while
	// preserving the userinfo email and every identity/group lookup field.
	profileClaims := maps.Clone(claims)
	subject := oidc.StringClaim(claims, "sub")
	if cached, ok := s.idTokenClaims.Get(token); ok && subject != "" && oidc.StringClaim(cached, "sub") == subject {
		for _, name := range []string{s.cfg.NameClaim, "name", "given_name", "family_name", "preferred_username"} {
			if strings.TrimSpace(oidc.StringClaim(profileClaims, name)) == "" {
				if value := oidc.StringClaim(cached, name); value != "" {
					profileClaims[name] = value
				}
			}
		}
	}
	profile := s.profile(profileClaims)
	if _, ok := claims[s.cfg.GroupsClaim]; ok {
		groups := oidc.StringsClaim(claims, s.cfg.GroupsClaim, s.cfg.GroupsDelimiter)
		s.rememberGroups(groups, oidc.StringClaim(claims, "sub"), oidc.StringClaim(claims, s.cfg.EmailClaim))
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(profile); err != nil {
		http.Error(w, fmt.Sprintf("failed to encode user info: %v", err), http.StatusInternalServerError)
	}
}

// userinfoClaims fetches the userinfo document for an access token, through
// the cache when one is configured.
func (s *server) userinfoClaims(ctx context.Context, token string) (map[string]any, error) {
	if s.userinfoURL == "" {
		return nil, fmt.Errorf("no userinfo endpoint is configured")
	}
	if s.userinfo != nil {
		if claims, ok := s.userinfo.Get(token); ok {
			return claims, nil
		}
	}
	claims, err := oidc.Userinfo(ctx, s.httpClient, s.userinfoURL, token)
	if err != nil {
		return nil, err
	}
	if s.userinfo != nil {
		s.userinfo.Add(token, claims)
	}
	return claims, nil
}

// profile shapes claims into the document Obot expects from
// /obot-get-user-info. The raw claims are kept alongside the derived fields
// so nothing an administrator might want to inspect is lost.
func (s *server) profile(claims map[string]any) map[string]any {
	out := make(map[string]any, len(claims)+3)
	maps.Copy(out, claims)

	id := oidc.StringClaim(claims, "sub")
	if id == "" {
		id = oidc.StringClaim(claims, s.cfg.EmailClaim)
	}
	out["id"] = id
	out["name"] = oidc.DisplayName(claims, s.cfg.EmailClaim)
	if name := oidc.StringClaim(claims, s.cfg.NameClaim); strings.TrimSpace(name) != "" {
		out["name"] = name
	}
	// This is presentation metadata. Never modify the email claim, subject,
	// session email, or the identifiers used for group lookups.
	out["email_display_domain"] = s.cfg.EmailDisplayDomain
	if email := oidc.StringClaim(claims, s.cfg.EmailClaim); email != "" {
		out["email"] = email
	}
	return out
}

// listUserAuthGroups returns the groups recorded for a provider user ID. The
// request body is the bare ID. An unknown ID is reported as an error rather
// than as "no groups": Obot would persist an empty answer and drop the user's
// memberships, whereas an error keeps what it already has and retries later.
func (s *server) listUserAuthGroups(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read request body: %v", err), http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(string(body))
	if id == "" {
		http.Error(w, "missing user ID", http.StatusBadRequest)
		return
	}

	groups, ok := s.groups.Get(id)
	if !ok {
		http.Error(w, fmt.Sprintf("no group information is recorded for user %q; it is recorded when the user signs in or is looked up", id), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(groupInfos(groups)); err != nil {
		http.Error(w, fmt.Sprintf("failed to encode groups: %v", err), http.StatusInternalServerError)
	}
}

// splitGroups applies the configured delimiter to groups oauth2-proxy
// extracted itself, which keeps a delimited string claim as one value.
func (s *server) splitGroups(groups []string) []string {
	var out []string
	for _, g := range groups {
		out = append(out, oidc.SplitValues(g, s.cfg.GroupsDelimiter)...)
	}
	return out
}

// rememberGroups stores the group list under each non-empty key.
func (s *server) rememberGroups(groups []string, keys ...string) {
	stored := make([]string, 0, len(groups))
	for _, g := range groups {
		if g = strings.TrimSpace(g); g != "" {
			stored = append(stored, g)
		}
	}
	for _, key := range keys {
		if key != "" {
			s.groups.Add(key, stored)
		}
	}
	s.directory.remember(stored)
}

// groupInfos converts raw group names into Obot's group documents, with the
// namespacing prefix Obot requires on the ID and the plain name for display.
func groupInfos(groups []string) state.GroupInfoList {
	infos := make(state.GroupInfoList, 0, len(groups))
	for _, g := range groups {
		if g = strings.TrimSpace(g); g != "" {
			infos = append(infos, state.GroupInfo{ID: groupIDPrefix + g, Name: g})
		}
	}
	return infos
}

func bearerToken(authorization string) string {
	authorization = strings.TrimLeft(authorization, " \t")
	if len(authorization) >= 7 && strings.EqualFold(authorization[:7], "bearer ") {
		authorization = authorization[7:]
	}
	return strings.TrimSpace(authorization)
}
