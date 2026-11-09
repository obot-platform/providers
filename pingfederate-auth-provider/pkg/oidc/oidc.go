// Package oidc holds the small amount of OpenID Connect plumbing the
// PingFederate auth provider needs beyond what oauth2-proxy already does:
// discovery, userinfo lookups, and reading claims out of tokens that
// oauth2-proxy has already verified.
package oidc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Discovery is the subset of the OpenID Provider Metadata document we use.
type Discovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
	JwksURI               string `json:"jwks_uri"`
}

// NewHTTPClient returns a client that trusts the system roots plus the PEM
// bundles at the given paths. It builds its own transport rather than
// deriving one from http.DefaultTransport, because oauth2-proxy installs its
// configured CAs into the default transport process-wide and this client's
// trust must not depend on that.
func NewHTTPClient(caFiles []string) (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if len(caFiles) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		for _, caFile := range caFiles {
			pem, err := os.ReadFile(caFile)
			if err != nil {
				return nil, fmt.Errorf("read CA file %s: %w", caFile, err)
			}
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("CA file %s contains no PEM certificates", caFile)
			}
		}
		tlsConfig.RootCAs = pool
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       tlsConfig,
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}, nil
}

// Discover fetches <issuer>/.well-known/openid-configuration.
func Discover(ctx context.Context, client *http.Client, issuer string) (*Discovery, error) {
	url := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("fetch %s: unexpected status %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var d Discovery
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("decode discovery document: %w", err)
	}
	return &d, nil
}

// Userinfo calls the userinfo endpoint with the given bearer token and returns
// the raw claims.
func Userinfo(ctx context.Context, client *http.Client, userinfoURL, authorization string) (map[string]any, error) {
	if userinfoURL == "" {
		return nil, fmt.Errorf("no userinfo endpoint configured")
	}
	token := strings.TrimSpace(authorization)
	if len(token) >= 7 && strings.EqualFold(token[:7], "bearer ") {
		token = strings.TrimSpace(token[7:])
	}
	if token == "" {
		return nil, fmt.Errorf("missing access token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userinfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("userinfo returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var claims map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&claims); err != nil {
		return nil, fmt.Errorf("decode userinfo: %w", err)
	}
	return claims, nil
}

// UnverifiedClaims decodes the payload of a JWT without checking its
// signature. Only call this on tokens oauth2-proxy has already verified.
func UnverifiedClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode JWT payload: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("parse JWT payload: %w", err)
	}
	return claims, nil
}

// StringClaim returns claim as a string, or "" when absent or not a string.
func StringClaim(claims map[string]any, name string) string {
	s, _ := claims[name].(string)
	return s
}

// StringsClaim returns a multi-valued claim as a string slice. A JSON array
// is used as-is. A plain string is one value, unless delimiter is set, in
// which case it is split on the delimiter: PingFederate emits a single
// delimited string when a multi-valued attribute is mapped as text.
func StringsClaim(claims map[string]any, name, delimiter string) []string {
	switch v := claims[name].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, SplitValues(s, delimiter)...)
			}
		}
		return out
	case []string:
		var out []string
		for _, s := range v {
			out = append(out, SplitValues(s, delimiter)...)
		}
		return out
	case string:
		return SplitValues(v, delimiter)
	default:
		return nil
	}
}

// SplitValues splits value on delimiter when one is set, trimming whitespace
// and dropping empty entries. Without a delimiter the whole trimmed value is
// returned as the only entry.
func SplitValues(value, delimiter string) []string {
	parts := []string{value}
	if delimiter != "" {
		parts = strings.Split(value, delimiter)
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// DisplayName picks the best human-readable name from standard OIDC claims,
// ending with the configured email claim when nothing better is present.
func DisplayName(claims map[string]any, emailClaim string) string {
	if name := StringClaim(claims, "name"); name != "" {
		return name
	}
	given, family := StringClaim(claims, "given_name"), StringClaim(claims, "family_name")
	if full := strings.TrimSpace(given + " " + family); full != "" {
		return full
	}
	if name := StringClaim(claims, "preferred_username"); name != "" {
		return name
	}
	return StringClaim(claims, emailClaim)
}
