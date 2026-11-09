package oidc

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func jwt(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestUnverifiedClaims(t *testing.T) {
	claims, err := UnverifiedClaims(jwt(t, map[string]any{"sub": "abc", "groups": []string{"a", "b"}}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := StringClaim(claims, "sub"); got != "abc" {
		t.Errorf("sub = %q, want abc", got)
	}
	if got := StringsClaim(claims, "groups", ""); strings.Join(got, ",") != "a,b" {
		t.Errorf("groups = %v, want [a b]", got)
	}

	for name, token := range map[string]string{
		"not a jwt":    "abc",
		"bad base64":   "a.%%%.c",
		"bad json":     "a." + base64.RawURLEncoding.EncodeToString([]byte("nope")) + ".c",
		"empty string": "",
	} {
		if _, err := UnverifiedClaims(token); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestStringsClaim(t *testing.T) {
	cases := []struct {
		name      string
		value     any
		delimiter string
		want      []string
	}{
		{"json array", []any{"admins", " devs ", "", 7}, "", []string{"admins", "devs"}},
		{"json array with delimiter", []any{"admins,devs", "ops"}, ",", []string{"admins", "devs", "ops"}},
		{"string slice", []string{"x", " "}, "", []string{"x"}},
		{"comma separated", "admins, devs ,ops,", ",", []string{"admins", "devs", "ops"}},
		{"comma kept without delimiter", "admins,devs", "", []string{"admins,devs"}},
		{"name with spaces kept", "Domain Admins", ",", []string{"Domain Admins"}},
		{"single", "admins", "", []string{"admins"}},
		{"empty string", "", "", nil},
		{"missing", nil, "", nil},
		{"wrong type", 42, "", nil},
	}
	for _, tc := range cases {
		claims := map[string]any{}
		if tc.value != nil {
			claims["groups"] = tc.value
		}
		got := StringsClaim(claims, "groups", tc.delimiter)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDisplayName(t *testing.T) {
	cases := []struct {
		name   string
		claims map[string]any
		want   string
	}{
		{"name wins", map[string]any{"name": "Ada Lovelace", "given_name": "Ada", "email": "ada@example.com"}, "Ada Lovelace"},
		{"given and family", map[string]any{"given_name": "Ada", "family_name": "Lovelace"}, "Ada Lovelace"},
		{"given only", map[string]any{"given_name": "Ada"}, "Ada"},
		{"preferred username", map[string]any{"preferred_username": "ada"}, "ada"},
		{"email claim fallback", map[string]any{"mail": "ada@example.com"}, "ada@example.com"},
		{"nothing", map[string]any{}, ""},
	}
	for _, tc := range cases {
		if got := DisplayName(tc.claims, "mail"); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestDiscover(t *testing.T) {
	var issuer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":            issuer,
			"userinfo_endpoint": issuer + "/userinfo",
			"jwks_uri":          issuer + "/jwks",
		})
	}))
	defer srv.Close()
	issuer = srv.URL

	d, err := Discover(context.Background(), srv.Client(), issuer+"/")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if d.Issuer != issuer || d.UserinfoEndpoint != issuer+"/userinfo" || d.JwksURI != issuer+"/jwks" {
		t.Errorf("unexpected discovery document: %+v", d)
	}

	if _, err := Discover(context.Background(), srv.Client(), issuer+"/missing"); err == nil {
		t.Error("expected an error for a 404 discovery document")
	}
}

func TestUserinfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_token"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sub": "u1", "email": "u1@example.com"})
	}))
	defer srv.Close()

	for _, authorization := range []string{"good", "Bearer good", "bearer good"} {
		claims, err := Userinfo(context.Background(), srv.Client(), srv.URL, authorization)
		if err != nil {
			t.Fatalf("Userinfo(%q): %v", authorization, err)
		}
		if StringClaim(claims, "sub") != "u1" {
			t.Errorf("Userinfo(%q): sub = %v", authorization, claims["sub"])
		}
	}

	if _, err := Userinfo(context.Background(), srv.Client(), srv.URL, "bad"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("expected a 401 error, got %v", err)
	}
	if _, err := Userinfo(context.Background(), srv.Client(), "", "good"); err == nil {
		t.Error("expected an error without a userinfo URL")
	}
}

func TestNewHTTPClientTrustsCAFile(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}

	untrusting, err := NewHTTPClient(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := untrusting.Get(srv.URL); err == nil {
		t.Fatal("expected the default client to reject the test certificate")
	} else {
		var unknownAuthority x509.UnknownAuthorityError
		if !errors.As(err, &unknownAuthority) {
			t.Fatalf("expected an unknown authority error, got %v", err)
		}
	}

	trusting, err := NewHTTPClient([]string{caFile})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := trusting.Get(srv.URL)
	if err != nil {
		t.Fatalf("client with CA file: %v", err)
	}
	resp.Body.Close()

	if _, err := NewHTTPClient([]string{filepath.Join(t.TempDir(), "missing.pem")}); err == nil {
		t.Error("expected an error for a missing CA file")
	}
	empty := filepath.Join(t.TempDir(), "empty.pem")
	if err := os.WriteFile(empty, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHTTPClient([]string{empty}); err == nil {
		t.Error("expected an error for a CA file without certificates")
	}
}
