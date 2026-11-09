package main

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestProfileDisplaySettingsPreserveIdentity(t *testing.T) {
	o := validOptions()
	o.NameClaim = "display_name"
	o.EmailDisplayDomain = "@Example.COM"
	cfg, err := o.resolve()
	if err != nil {
		t.Fatal(err)
	}
	s := &server{cfg: cfg}
	claims := map[string]any{"sub": "stable-subject", "email": "JDOE", "name": "Fallback", "display_name": "Jane Doe"}
	p := s.profile(claims)
	if p["id"] != "stable-subject" || p["email"] != "JDOE" || claims["email"] != "JDOE" {
		t.Fatalf("identity changed: %v", p)
	}
	if p["name"] != "Jane Doe" || p["email_display_domain"] != "example.com" {
		t.Fatalf("display mapping: %v", p)
	}
	delete(claims, "display_name")
	if got := s.profile(claims)["name"]; got != "Fallback" {
		t.Fatalf("fallback = %v", got)
	}
	o.EmailDisplayDomain = ""
	cfg, err = o.resolve()
	if err != nil {
		t.Fatal(err)
	}
	s.cfg = cfg
	if got := s.profile(claims)["email_display_domain"]; got != "" {
		t.Fatalf("domain did not clear: %v", got)
	}
}

func TestDisplaySettingsValidation(t *testing.T) {
	o := validOptions()
	o.NameClaim = " "
	cfg, err := o.resolve()
	if err != nil || cfg.NameClaim != "name" {
		t.Fatalf("default name: %v, %v", cfg, err)
	}
	for _, domain := range []string{"a@b.com", "https://example.com", "bad domain", ".example.com", "-bad.com"} {
		o.EmailDisplayDomain = domain
		if _, err := o.resolve(); err == nil {
			t.Errorf("accepted domain %q", domain)
		}
	}
}

func TestNameClaimFromVerifiedSessionToken(t *testing.T) {
	var calls atomic.Int32
	idp := userinfoServer(t, &calls, map[string]any{"sub": "stable-subject", "email": "JDOE"})
	defer idp.Close()
	s := newTestServer(t, idp.URL, time.Minute)
	s.cfg.NameClaim = "display_name"
	s.idTokenClaims.Add("tok", map[string]any{"sub": "stable-subject", "display_name": "Jane Doe", "email": "different"})
	_, p := getUserInfo(t, s, "Bearer tok")
	if p["name"] != "Jane Doe" || p["email"] != "JDOE" || p["id"] != "stable-subject" {
		t.Fatalf("profile = %v", p)
	}
	s.idTokenClaims.Add("tok", map[string]any{"sub": "different-subject", "display_name": "Wrong Person"})
	_, p = getUserInfo(t, s, "Bearer tok")
	if p["name"] == "Wrong Person" {
		t.Fatal("accepted name from a different subject")
	}
}
