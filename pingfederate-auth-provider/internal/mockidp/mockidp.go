// Package mockidp is a small OpenID Connect identity provider for tests. It
// speaks the subset of the protocol PingFederate exposes to Obot: discovery,
// the authorization code flow (with optional PKCE), token refresh, a JWKS
// endpoint, and userinfo. Logins are automatic: the authorization endpoint
// immediately redirects back with a code for the configured user.
package mockidp

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Endpoint paths mirror PingFederate's defaults so URLs in tests look like
// the real thing.
const (
	DiscoveryPath = "/.well-known/openid-configuration"
	AuthorizePath = "/as/authorization.oauth2"
	TokenPath     = "/as/token.oauth2"
	UserinfoPath  = "/idp/userinfo.openid"
	JWKSPath      = "/pf/JWKS"
)

// User is the identity the provider asserts.
type User struct {
	Subject           string
	Email             string
	EmailVerified     *bool
	Name              string
	GivenName         string
	FamilyName        string
	PreferredUsername string
	Picture           string
	Groups            []string
}

// Config controls the provider's behavior.
type Config struct {
	ClientID     string
	ClientSecret string
	// RequirePKCE rejects authorization requests without a code challenge.
	RequirePKCE bool
	// GroupsInIDToken and GroupsInUserinfo choose where the groups claim is
	// emitted, mirroring PingFederate's per-endpoint attribute contracts.
	GroupsInIDToken  bool
	GroupsInUserinfo bool
	// GroupsAsDelimitedString emits the groups claim as one comma-separated
	// string instead of a JSON array, a common PingFederate attribute mapping.
	GroupsAsDelimitedString bool
	// GroupsClaimName is the name of the groups claim; it defaults to "groups".
	GroupsClaimName string
	// EmailClaimName is the name of the email claim; it defaults to "email".
	EmailClaimName string
	// EmailInIDToken when false leaves email out of the ID token so that the
	// relying party has to look it up at the userinfo endpoint.
	EmailInIDToken bool
	// AccessTokenLifetime defaults to one hour.
	AccessTokenLifetime time.Duration
}

// AuthorizationRequest records what a client sent to the authorization
// endpoint, for assertions.
type AuthorizationRequest struct {
	ClientID            string
	ResponseType        string
	RedirectURI         string
	State               string
	Nonce               string
	Scope               string
	CodeChallenge       string
	CodeChallengeMethod string
}

// Counts reports how often each endpoint was called.
type Counts struct {
	Discovery, Authorize, Token, Refresh, Userinfo, JWKS int
}

type authCode struct {
	user                User
	nonce               string
	redirectURI         string
	codeChallenge       string
	codeChallengeMethod string
}

type tokenRecord struct {
	user    User
	expires time.Time
}

// IDP is the provider. Set its issuer to the base URL it is served on before
// handling requests, because the discovery document and ID tokens carry it.
type IDP struct {
	cfg Config
	key *rsa.PrivateKey
	kid string

	mu             sync.Mutex
	issuer         string
	user           User
	codes          map[string]authCode
	tokens         map[string]tokenRecord
	refreshTokens  map[string]User
	authorizations []AuthorizationRequest
	counts         Counts
}

// New creates a provider with a fresh signing key.
func New(cfg Config) *IDP {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(fmt.Sprintf("mockidp: generate key: %v", err))
	}
	if cfg.AccessTokenLifetime == 0 {
		cfg.AccessTokenLifetime = time.Hour
	}
	if cfg.GroupsClaimName == "" {
		cfg.GroupsClaimName = "groups"
	}
	if cfg.EmailClaimName == "" {
		cfg.EmailClaimName = "email"
	}
	return &IDP{
		cfg:           cfg,
		key:           key,
		kid:           "mock-" + randomID(4),
		codes:         map[string]authCode{},
		tokens:        map[string]tokenRecord{},
		refreshTokens: map[string]User{},
	}
}

// SetIssuer sets the issuer identifier, which is also the base URL of every
// endpoint.
func (i *IDP) SetIssuer(issuer string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.issuer = strings.TrimSuffix(issuer, "/")
}

// Issuer returns the issuer identifier.
func (i *IDP) Issuer() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.issuer
}

// SetUser sets the identity that subsequent logins assert.
func (i *IDP) SetUser(u User) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.user = u
}

// Authorizations returns the recorded authorization requests, oldest first.
func (i *IDP) Authorizations() []AuthorizationRequest {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]AuthorizationRequest(nil), i.authorizations...)
}

// Counts returns the endpoint call counts.
func (i *IDP) Counts() Counts {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.counts
}

// AccessTokenFor returns an access token for the user as if they had logged
// in, for calling userinfo directly in tests.
func (i *IDP) AccessTokenFor(u User) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	token := randomID(16)
	i.tokens[token] = tokenRecord{user: u, expires: time.Now().Add(i.cfg.AccessTokenLifetime)}
	return token
}

// ExpireAccessToken makes a token stop working at the userinfo endpoint.
func (i *IDP) ExpireAccessToken(token string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.tokens, token)
}

func (i *IDP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case DiscoveryPath:
		i.discovery(w, r)
	case AuthorizePath:
		i.authorize(w, r)
	case TokenPath:
		i.token(w, r)
	case UserinfoPath:
		i.userinfo(w, r)
	case JWKSPath:
		i.jwks(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (i *IDP) discovery(w http.ResponseWriter, _ *http.Request) {
	i.mu.Lock()
	i.counts.Discovery++
	issuer := i.issuer
	i.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + AuthorizePath,
		"token_endpoint":                        issuer + TokenPath,
		"userinfo_endpoint":                     issuer + UserinfoPath,
		"jwks_uri":                              issuer + JWKSPath,
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"scopes_supported":                      []string{"openid", "profile", "email", "groups"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"code_challenge_methods_supported":      []string{"S256", "plain"},
		"claims_supported":                      []string{"sub", "email", "email_verified", "name", "given_name", "family_name", "preferred_username", "picture", "groups"},
	})
}

func (i *IDP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := AuthorizationRequest{
		ClientID:            q.Get("client_id"),
		ResponseType:        q.Get("response_type"),
		RedirectURI:         q.Get("redirect_uri"),
		State:               q.Get("state"),
		Nonce:               q.Get("nonce"),
		Scope:               q.Get("scope"),
		CodeChallenge:       q.Get("code_challenge"),
		CodeChallengeMethod: q.Get("code_challenge_method"),
	}

	i.mu.Lock()
	i.counts.Authorize++
	i.authorizations = append(i.authorizations, req)
	user := i.user
	i.mu.Unlock()

	switch {
	case req.ClientID != i.cfg.ClientID:
		http.Error(w, "unknown client_id", http.StatusBadRequest)
		return
	case req.ResponseType != "code":
		http.Error(w, "unsupported response_type", http.StatusBadRequest)
		return
	case req.RedirectURI == "":
		http.Error(w, "missing redirect_uri", http.StatusBadRequest)
		return
	case i.cfg.RequirePKCE && req.CodeChallenge == "":
		http.Error(w, "PKCE code_challenge is required", http.StatusBadRequest)
		return
	}
	if req.CodeChallenge != "" && req.CodeChallengeMethod != "S256" && req.CodeChallengeMethod != "plain" {
		http.Error(w, "unsupported code_challenge_method", http.StatusBadRequest)
		return
	}

	redirect, err := url.Parse(req.RedirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}

	code := randomID(16)
	i.mu.Lock()
	i.codes[code] = authCode{
		user:                user,
		nonce:               req.Nonce,
		redirectURI:         req.RedirectURI,
		codeChallenge:       req.CodeChallenge,
		codeChallengeMethod: req.CodeChallengeMethod,
	}
	i.mu.Unlock()

	values := redirect.Query()
	values.Set("code", code)
	if req.State != "" {
		values.Set("state", req.State)
	}
	redirect.RawQuery = values.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (i *IDP) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "invalid_request"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}

	clientID, clientSecret, ok := r.BasicAuth()
	if ok {
		// RFC 6749 section 2.3.1 form-encodes the credentials before Basic auth.
		if id, err := url.QueryUnescape(clientID); err == nil {
			clientID = id
		}
		if secret, err := url.QueryUnescape(clientSecret); err == nil {
			clientSecret = secret
		}
	} else {
		clientID, clientSecret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if clientID != i.cfg.ClientID || subtle.ConstantTimeCompare([]byte(clientSecret), []byte(i.cfg.ClientSecret)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	i.counts.Token++

	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		code, ok := i.codes[r.PostForm.Get("code")]
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "unknown or reused code"})
			return
		}
		delete(i.codes, r.PostForm.Get("code"))
		if r.PostForm.Get("redirect_uri") != code.redirectURI {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "redirect_uri mismatch"})
			return
		}
		if code.codeChallenge != "" && !verifyPKCE(code.codeChallenge, code.codeChallengeMethod, r.PostForm.Get("code_verifier")) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "PKCE verification failed"})
			return
		}
		i.issueTokensLocked(w, code.user, code.nonce)
	case "refresh_token":
		i.counts.Refresh++
		user, ok := i.refreshTokens[r.PostForm.Get("refresh_token")]
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "unknown refresh token"})
			return
		}
		delete(i.refreshTokens, r.PostForm.Get("refresh_token"))
		i.issueTokensLocked(w, user, "")
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
	}
}

// issueTokensLocked writes a token response. The caller holds the lock.
func (i *IDP) issueTokensLocked(w http.ResponseWriter, user User, nonce string) {
	now := time.Now()
	accessToken := randomID(16)
	refreshToken := randomID(16)
	i.tokens[accessToken] = tokenRecord{user: user, expires: now.Add(i.cfg.AccessTokenLifetime)}
	i.refreshTokens[refreshToken] = user

	claims := map[string]any{
		"iss":       i.issuer,
		"sub":       user.Subject,
		"aud":       i.cfg.ClientID,
		"exp":       now.Add(i.cfg.AccessTokenLifetime).Unix(),
		"iat":       now.Unix(),
		"auth_time": now.Unix(),
	}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	i.addProfileClaims(claims, user, i.cfg.EmailInIDToken, i.cfg.GroupsInIDToken)

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  accessToken,
		"token_type":    "Bearer",
		"expires_in":    int(i.cfg.AccessTokenLifetime.Seconds()),
		"refresh_token": refreshToken,
		"id_token":      i.sign(claims),
		"scope":         "openid email profile",
	})
}

func (i *IDP) userinfo(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.counts.Userinfo++

	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_request"`)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_request"})
		return
	}
	record, ok := i.tokens[strings.TrimPrefix(auth, "Bearer ")]
	if !ok || time.Now().After(record.expires) {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
		return
	}

	claims := map[string]any{"sub": record.user.Subject}
	i.addProfileClaims(claims, record.user, true, i.cfg.GroupsInUserinfo)
	writeJSON(w, http.StatusOK, claims)
}

func (i *IDP) addProfileClaims(claims map[string]any, user User, includeEmail, includeGroups bool) {
	if includeEmail && user.Email != "" {
		claims[i.cfg.EmailClaimName] = user.Email
		if user.EmailVerified != nil {
			claims["email_verified"] = *user.EmailVerified
		}
	}
	for name, value := range map[string]string{
		"name":               user.Name,
		"given_name":         user.GivenName,
		"family_name":        user.FamilyName,
		"preferred_username": user.PreferredUsername,
		"picture":            user.Picture,
	} {
		if value != "" {
			claims[name] = value
		}
	}
	if includeGroups && len(user.Groups) > 0 {
		if i.cfg.GroupsAsDelimitedString {
			claims[i.cfg.GroupsClaimName] = strings.Join(user.Groups, ",")
		} else {
			claims[i.cfg.GroupsClaimName] = user.Groups
		}
	}
}

func (i *IDP) jwks(w http.ResponseWriter, _ *http.Request) {
	i.mu.Lock()
	i.counts.JWKS++
	i.mu.Unlock()

	pub := i.key.PublicKey
	writeJSON(w, http.StatusOK, map[string]any{
		"keys": []map[string]string{{
			"kty": "RSA",
			"use": "sig",
			"alg": "RS256",
			"kid": i.kid,
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}},
	})
}

// sign produces an RS256 JWT.
func (i *IDP) sign(claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": i.kid})
	payload, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, digest[:])
	if err != nil {
		panic(fmt.Sprintf("mockidp: sign: %v", err))
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func verifyPKCE(challenge, method, verifier string) bool {
	if verifier == "" {
		return false
	}
	switch method {
	case "S256":
		sum := sha256.Sum256([]byte(verifier))
		return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(challenge)) == 1
	case "plain":
		return subtle.ConstantTimeCompare([]byte(verifier), []byte(challenge)) == 1
	default:
		return false
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("mockidp: random: %v", err))
	}
	return hex.EncodeToString(b)
}
