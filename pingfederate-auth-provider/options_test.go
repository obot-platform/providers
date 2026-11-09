package main

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

func validOptions() Options {
	return Options{
		ClientID:                 "obot",
		ClientSecret:             "secret",
		IssuerURL:                "https://sso.example.com",
		Scopes:                   "openid email profile",
		EmailClaim:               "email",
		GroupsClaim:              "groups",
		AllowUnverifiedEmail:     "true",
		CodeChallengeMethod:      "S256",
		SkipIssuerVerification:   "false",
		SkipDiscovery:            "false",
		UserinfoCacheTTL:         "5m",
		ObotServerURL:            "https://obot.example.com",
		AuthCookieSecret:         base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32))),
		AuthEmailDomains:         "*",
		AuthTokenRefreshDuration: "1h",
	}
}

func TestResolveDefaults(t *testing.T) {
	s, err := validOptions().resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !s.allowUnverifiedEmail || s.skipIssuerVerification || s.skipDiscovery || s.skipNonce || s.loggingEnabled {
		t.Errorf("unexpected booleans: %+v", s)
	}
	if s.codeChallengeMethod != "S256" {
		t.Errorf("codeChallengeMethod = %q", s.codeChallengeMethod)
	}
	if s.scopes != "openid email profile" {
		t.Errorf("scopes = %q", s.scopes)
	}
	if len(s.emailDomains) != 1 || s.emailDomains[0] != "*" {
		t.Errorf("emailDomains = %v", s.emailDomains)
	}
	if s.refreshDuration.Hours() != 1 || s.userinfoCacheTTL.Minutes() != 5 {
		t.Errorf("durations: refresh=%s cache=%s", s.refreshDuration, s.userinfoCacheTTL)
	}
	if len(s.cookieSecret) != 32 {
		t.Errorf("cookie secret length = %d", len(s.cookieSecret))
	}
	if len(s.caFiles) != 0 {
		t.Errorf("caFiles = %v, want none", s.caFiles)
	}
}

func TestResolveNormalizesScopes(t *testing.T) {
	cases := map[string]string{
		"":                             "openid",
		"email profile":                "openid email profile",
		"openid,email, profile,groups": "openid email profile groups",
		"profile openid profile":       "openid profile",
	}
	for in, want := range cases {
		o := validOptions()
		o.Scopes = in
		s, err := o.resolve()
		if err != nil {
			t.Fatalf("resolve(%q): %v", in, err)
		}
		if s.scopes != want {
			t.Errorf("scopes(%q) = %q, want %q", in, s.scopes, want)
		}
	}
}

func TestResolveCodeChallengeMethod(t *testing.T) {
	cases := map[string]string{"S256": "S256", "s256": "S256", "plain": "plain", "none": "", "NONE": "", "disabled": ""}
	for in, want := range cases {
		o := validOptions()
		o.CodeChallengeMethod = in
		s, err := o.resolve()
		if err != nil {
			t.Fatalf("resolve(%q): %v", in, err)
		}
		if s.codeChallengeMethod != want {
			t.Errorf("codeChallengeMethod(%q) = %q, want %q", in, s.codeChallengeMethod, want)
		}
	}
	o := validOptions()
	o.CodeChallengeMethod = "sha1"
	if _, err := o.resolve(); err == nil {
		t.Error("expected an error for an unsupported code challenge method")
	}
}

func TestResolveRejectsBadValues(t *testing.T) {
	cases := map[string]func(*Options){
		"empty issuer":       func(o *Options) { o.IssuerURL = "" },
		"relative issuer":    func(o *Options) { o.IssuerURL = "sso.example.com" },
		"ftp issuer":         func(o *Options) { o.IssuerURL = "ftp://sso.example.com" },
		"empty client id":    func(o *Options) { o.ClientID = " " },
		"empty server url":   func(o *Options) { o.ObotServerURL = "" },
		"bad bool":           func(o *Options) { o.AllowUnverifiedEmail = "maybe" },
		"bad skip discovery": func(o *Options) { o.SkipDiscovery = "sometimes" },
		"bad logging":        func(o *Options) { o.LoggingEnabled = "verbose" },
		"skip discovery without urls": func(o *Options) {
			o.SkipDiscovery = "true"
			o.AuthorizationURL = "https://sso.example.com/as/authorization.oauth2"
			o.TokenURL = "https://sso.example.com/as/token.oauth2"
		},
		"bad token url":             func(o *Options) { o.TokenURL = "nope" },
		"bad userinfo url":          func(o *Options) { o.UserinfoURL = "/idp/userinfo.openid" },
		"empty email domains":       func(o *Options) { o.AuthEmailDomains = " , " },
		"cookie secret not base64":  func(o *Options) { o.AuthCookieSecret = "***" },
		"cookie secret wrong size":  func(o *Options) { o.AuthCookieSecret = base64.StdEncoding.EncodeToString([]byte("short")) },
		"bad refresh duration":      func(o *Options) { o.AuthTokenRefreshDuration = "soon" },
		"negative refresh duration": func(o *Options) { o.AuthTokenRefreshDuration = "-1h" },
		"bad cache ttl":             func(o *Options) { o.UserinfoCacheTTL = "later" },
		"negative cache ttl":        func(o *Options) { o.UserinfoCacheTTL = "-5m" },
		"missing ca file":           func(o *Options) { o.CAFile = "/nonexistent/ca.pem" },
		"ca certificate not pem":    func(o *Options) { o.CACertificate = "MIIB..." },
	}
	for name, mutate := range cases {
		o := validOptions()
		mutate(&o)
		if _, err := o.resolve(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestResolveSkipDiscoveryWithEndpoints(t *testing.T) {
	o := validOptions()
	o.SkipDiscovery = "true"
	o.AuthorizationURL = "https://sso.example.com/as/authorization.oauth2"
	o.TokenURL = "https://sso.example.com/as/token.oauth2"
	o.JWKSURL = "https://sso.example.com/pf/JWKS"
	s, err := o.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !s.skipDiscovery {
		t.Error("skipDiscovery should be set")
	}
}

func TestResolveCACertificateWritesProtectedFile(t *testing.T) {
	pemText := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"
	o := validOptions()
	o.CACertificate = "  " + pemText + "  "
	s, err := o.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(s.caFiles) != 1 {
		t.Fatalf("caFiles = %v, want one temporary file", s.caFiles)
	}
	t.Cleanup(func() { _ = os.Remove(s.caFiles[0]) })

	info, err := os.Stat(s.caFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("CA file mode = %o, want 600", info.Mode().Perm())
	}
	content, err := os.ReadFile(s.caFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(content)) != pemText {
		t.Errorf("CA file content = %q", content)
	}
}

func TestResolveCAFileAndCertificateTogether(t *testing.T) {
	caFile := t.TempDir() + "/ca.pem"
	if err := os.WriteFile(caFile, []byte("-----BEGIN CERTIFICATE-----\nAAA\n-----END CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := validOptions()
	o.CAFile = caFile
	o.CACertificate = "-----BEGIN CERTIFICATE-----\nBBB\n-----END CERTIFICATE-----"
	s, err := o.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(s.caFiles) != 2 || s.caFiles[0] != caFile {
		t.Errorf("caFiles = %v", s.caFiles)
	}
	for _, f := range s.caFiles[1:] {
		_ = os.Remove(f)
	}
}

func TestResolveExtraAudiencesAndEmailDomains(t *testing.T) {
	o := validOptions()
	o.ExtraAudiences = "api://obot, https://other "
	o.AuthEmailDomains = "example.com , example.org"
	s, err := o.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if strings.Join(s.extraAudiences, "|") != "api://obot|https://other" {
		t.Errorf("extraAudiences = %v", s.extraAudiences)
	}
	if strings.Join(s.emailDomains, "|") != "example.com|example.org" {
		t.Errorf("emailDomains = %v", s.emailDomains)
	}
}

func TestLoadOptionsFromEnvironment(t *testing.T) {
	for _, name := range []string{
		"OBOT_PINGFEDERATE_AUTH_PROVIDER_CLIENT_ID", "OBOT_PINGFEDERATE_AUTH_PROVIDER_CLIENT_SECRET",
		"OBOT_PINGFEDERATE_AUTH_PROVIDER_ISSUER_URL", "OBOT_SERVER_URL", "OBOT_AUTH_PROVIDER_COOKIE_SECRET",
	} {
		t.Setenv(name, "")
	}
	if _, err := loadOptions(); err == nil {
		t.Fatal("expected an error with no configuration")
	}

	t.Setenv("OBOT_PINGFEDERATE_AUTH_PROVIDER_CLIENT_ID", "obot")
	t.Setenv("OBOT_PINGFEDERATE_AUTH_PROVIDER_CLIENT_SECRET", "secret")
	t.Setenv("OBOT_PINGFEDERATE_AUTH_PROVIDER_ISSUER_URL", "https://sso.example.com ")
	t.Setenv("OBOT_SERVER_URL", "https://obot.example.com")
	t.Setenv("OBOT_AUTH_PROVIDER_COOKIE_SECRET", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("y", 24))))
	t.Setenv("OBOT_PINGFEDERATE_AUTH_PROVIDER_GROUPS_CLAIM", "memberOf")
	t.Setenv("OBOT_AUTH_PROVIDER_ENABLE_LOGGING", "true")

	s, err := loadOptions()
	if err != nil {
		t.Fatalf("loadOptions: %v", err)
	}
	if s.issuerURL != "https://sso.example.com" || s.GroupsClaim != "memberOf" || !s.loggingEnabled {
		t.Errorf("unexpected settings: issuer=%q groups=%q logging=%v", s.issuerURL, s.GroupsClaim, s.loggingEnabled)
	}
	if s.EmailClaim != "email" || s.scopes != "openid email profile" || s.codeChallengeMethod != "S256" || s.skipNonce {
		t.Errorf("defaults not applied: %+v", s.Options)
	}
}
