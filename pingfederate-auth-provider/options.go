package main

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/obot-platform/providers/auth-providers-common/pkg/env"
)

// Options is every setting the provider reads from its environment. Obot
// passes the parameters an administrator entered in the admin UI as the
// environment variables named in the provider manifest, plus PORT and the
// server URLs. Nothing else from the Obot process environment reaches the
// provider, which is why trust for a private CA is a provider setting rather
// than something inherited from the pod.
type Options struct {
	ClientID     string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_CLIENT_ID"`
	ClientSecret string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_CLIENT_SECRET"`
	IssuerURL    string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_ISSUER_URL"`

	Scopes                 string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_SCOPES" default:"openid email profile"`
	NameClaim              string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_NAME_CLAIM" default:"name"`
	EmailDisplayDomain     string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_EMAIL_DISPLAY_DOMAIN" optional:"true"`
	EmailClaim             string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_EMAIL_CLAIM" default:"email"`
	GroupsClaim            string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_GROUPS_CLAIM" default:"groups"`
	GroupsDelimiter        string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_GROUPS_DELIMITER" optional:"true"`
	KnownGroups            string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_KNOWN_GROUPS" optional:"true"`
	AllowUnverifiedEmail   string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_ALLOW_UNVERIFIED_EMAIL" default:"true"`
	CodeChallengeMethod    string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_CODE_CHALLENGE_METHOD" default:"S256"`
	SkipNonce              string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_SKIP_NONCE" default:"false"`
	ExtraAudiences         string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_EXTRA_AUDIENCES" optional:"true"`
	SkipIssuerVerification string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_SKIP_ISSUER_VERIFICATION" default:"false"`
	CACertificate          string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_CA_CERTIFICATE" optional:"true"`
	CAFile                 string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_CA_FILE" optional:"true"`
	SkipDiscovery          string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_SKIP_DISCOVERY" default:"false"`
	AuthorizationURL       string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_AUTHORIZATION_URL" optional:"true"`
	TokenURL               string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_TOKEN_URL" optional:"true"`
	UserinfoURL            string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_USERINFO_URL" optional:"true"`
	JWKSURL                string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_JWKS_URL" optional:"true"`
	UserinfoCacheTTL       string `env:"OBOT_PINGFEDERATE_AUTH_PROVIDER_USERINFO_CACHE_TTL" default:"5m"`

	ObotServerURL                     string `env:"OBOT_SERVER_PUBLIC_URL,OBOT_SERVER_URL"`
	PostgresConnectionDSN             string `env:"OBOT_AUTH_PROVIDER_POSTGRES_CONNECTION_DSN" optional:"true"`
	PostgresMaxConnections            int    `env:"OBOT_AUTH_PROVIDER_POSTGRES_MAX_CONNECTIONS" optional:"true"`
	PostgresMaxIdleConnections        int    `env:"OBOT_AUTH_PROVIDER_POSTGRES_MAX_IDLE_CONNECTIONS" optional:"true"`
	PostgresConnectionLifetimeSeconds int    `env:"OBOT_AUTH_PROVIDER_POSTGRES_CONNECTION_LIFETIME_SECONDS" optional:"true"`
	AuthCookieSecret                  string `env:"OBOT_AUTH_PROVIDER_COOKIE_SECRET"`
	AuthEmailDomains                  string `env:"OBOT_AUTH_PROVIDER_EMAIL_DOMAINS" default:"*"`
	AuthTokenRefreshDuration          string `env:"OBOT_AUTH_PROVIDER_TOKEN_REFRESH_DURATION" default:"1h"`
	LoggingEnabled                    string `env:"OBOT_AUTH_PROVIDER_ENABLE_LOGGING" optional:"true"`
}

// settings is Options after parsing and validation: typed values, normalized
// URLs, and the CA bundle materialized as files for oauth2-proxy.
type settings struct {
	Options

	issuerURL              string
	scopes                 string
	allowUnverifiedEmail   bool
	skipIssuerVerification bool
	skipNonce              bool
	skipDiscovery          bool
	codeChallengeMethod    string // "" disables PKCE
	extraAudiences         []string
	knownGroups            []string
	caFiles                []string
	cookieSecret           string
	emailDomains           []string
	refreshDuration        time.Duration
	userinfoCacheTTL       time.Duration
	loggingEnabled         bool
}

func loadOptions() (*settings, error) {
	var opts Options
	if err := env.LoadEnvForStruct(&opts); err != nil {
		return nil, err
	}
	return opts.resolve()
}

// resolve validates the raw options and derives the typed settings. It is
// separate from loadOptions so tests can build Options directly.
func (o Options) resolve() (*settings, error) {
	s := &settings{Options: o}
	var err error
	s.NameClaim = strings.TrimSpace(o.NameClaim)
	if s.NameClaim == "" {
		s.NameClaim = "name"
	}
	s.EmailDisplayDomain = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(o.EmailDisplayDomain), "@"))
	if s.EmailDisplayDomain != "" {
		if len(s.EmailDisplayDomain) > 253 {
			return nil, fmt.Errorf("email display domain is too long")
		}
		for _, label := range strings.Split(s.EmailDisplayDomain, ".") {
			if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return nil, fmt.Errorf("invalid email display domain")
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
					return nil, fmt.Errorf("invalid email display domain")
				}
			}
		}
	}

	s.issuerURL = strings.TrimSpace(o.IssuerURL)
	if err := validateHTTPURL("issuer URL", s.issuerURL); err != nil {
		return nil, err
	}
	if strings.TrimSpace(o.ClientID) == "" {
		return nil, fmt.Errorf("client ID must not be empty")
	}
	if strings.TrimSpace(o.EmailClaim) == "" || strings.TrimSpace(o.GroupsClaim) == "" {
		return nil, fmt.Errorf("email claim and groups claim must not be empty")
	}
	if strings.TrimSpace(o.ObotServerURL) == "" {
		return nil, fmt.Errorf("obot server URL must not be empty")
	}

	if s.allowUnverifiedEmail, err = parseBool("allow unverified email", o.AllowUnverifiedEmail); err != nil {
		return nil, err
	}
	if s.skipIssuerVerification, err = parseBool("skip issuer verification", o.SkipIssuerVerification); err != nil {
		return nil, err
	}
	if s.skipNonce, err = parseBool("skip nonce verification", o.SkipNonce); err != nil {
		return nil, err
	}
	if s.skipDiscovery, err = parseBool("skip discovery", o.SkipDiscovery); err != nil {
		return nil, err
	}
	if s.loggingEnabled, err = parseBool("enable logging", o.LoggingEnabled); err != nil {
		return nil, err
	}

	if s.codeChallengeMethod, err = normalizeCodeChallengeMethod(o.CodeChallengeMethod); err != nil {
		return nil, err
	}
	s.scopes = normalizeScopes(o.Scopes)

	if s.skipDiscovery {
		for name, value := range map[string]string{
			"authorization URL": o.AuthorizationURL,
			"token URL":         o.TokenURL,
			"JWKS URL":          o.JWKSURL,
		} {
			if strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("%s is required when OIDC discovery is skipped", name)
			}
		}
	}
	for name, value := range map[string]string{
		"authorization URL": o.AuthorizationURL,
		"token URL":         o.TokenURL,
		"userinfo URL":      o.UserinfoURL,
		"JWKS URL":          o.JWKSURL,
	} {
		if strings.TrimSpace(value) != "" {
			if err := validateHTTPURL(name, strings.TrimSpace(value)); err != nil {
				return nil, err
			}
		}
	}

	s.extraAudiences = splitList(o.ExtraAudiences)
	s.knownGroups = splitLines(o.KnownGroups)
	s.emailDomains = splitList(o.AuthEmailDomains)
	if len(s.emailDomains) == 0 {
		return nil, fmt.Errorf("allowed email domains must not be empty (use * to allow any domain)")
	}

	if s.caFiles, err = resolveCAFiles(o.CAFile, o.CACertificate); err != nil {
		return nil, err
	}

	cookieSecret, err := base64.StdEncoding.DecodeString(o.AuthCookieSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to decode cookie secret: %w", err)
	}
	// Obot generates secrets that survive trimming, and older installations
	// stored them with a trailing newline, so whitespace is trimmed only when
	// the raw value is not already a usable key.
	s.cookieSecret = string(cookieSecret)
	if !validCookieSecretLength(len(s.cookieSecret)) {
		s.cookieSecret = strings.TrimSpace(s.cookieSecret)
	}
	if !validCookieSecretLength(len(s.cookieSecret)) {
		return nil, fmt.Errorf("cookie secret must decode to 16, 24, or 32 bytes, got %d", len(s.cookieSecret))
	}

	if s.refreshDuration, err = time.ParseDuration(o.AuthTokenRefreshDuration); err != nil {
		return nil, fmt.Errorf("failed to parse token refresh duration: %w", err)
	}
	if s.refreshDuration < 0 {
		return nil, fmt.Errorf("token refresh duration must not be negative")
	}
	if s.userinfoCacheTTL, err = time.ParseDuration(o.UserinfoCacheTTL); err != nil {
		return nil, fmt.Errorf("failed to parse userinfo cache TTL: %w", err)
	}
	if s.userinfoCacheTTL < 0 {
		return nil, fmt.Errorf("userinfo cache TTL must not be negative")
	}

	return s, nil
}

func validCookieSecretLength(n int) bool {
	return n == 16 || n == 24 || n == 32
}

func validateHTTPURL(name, value string) error {
	u, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("%s %q is not a valid URL: %w", name, value, err)
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("%s %q must be an absolute http(s) URL", name, value)
	}
	return nil
}

func parseBool(name, value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "false", "0", "no", "off":
		return false, nil
	case "true", "1", "yes", "on":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be true or false, got %q", name, value)
	}
}

// normalizeCodeChallengeMethod maps the configured PKCE method to what
// oauth2-proxy expects. "none" (or empty) turns PKCE off for an authorization
// server that rejects unexpected code_challenge parameters.
func normalizeCodeChallengeMethod(value string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "", "NONE", "OFF", "DISABLED":
		return "", nil
	case "S256":
		return "S256", nil
	case "PLAIN":
		return "plain", nil
	default:
		return "", fmt.Errorf("code challenge method must be S256, plain, or none, got %q", value)
	}
}

// normalizeScopes accepts scopes separated by spaces or commas and makes sure
// "openid" is requested, without which the token response carries no ID token.
func normalizeScopes(value string) string {
	seen := map[string]bool{"openid": true}
	scopes := []string{"openid"}
	for _, scope := range strings.FieldsFunc(value, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' || r == '\n' }) {
		if !seen[scope] {
			seen[scope] = true
			scopes = append(scopes, scope)
		}
	}
	return strings.Join(scopes, " ")
}

// splitLines accepts one entry per line or comma-separated entries, so a
// list pasted into a multi-line form field and a one-line value both work.
func splitLines(value string) []string {
	var out []string
	seen := map[string]bool{}
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		if item = strings.TrimSpace(item); item != "" && !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	return out
}

func splitList(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// resolveCAFiles returns the PEM files that should be trusted in addition to
// the system roots. A CA given as PEM text (the form that fits an admin UI
// field) is written to a private temporary file, because oauth2-proxy only
// accepts certificate authorities as file paths.
func resolveCAFiles(caFile, caCertificate string) ([]string, error) {
	var files []string
	if caFile = strings.TrimSpace(caFile); caFile != "" {
		if _, err := os.Stat(caFile); err != nil {
			return nil, fmt.Errorf("CA file is not readable: %w", err)
		}
		files = append(files, caFile)
	}
	if pem := strings.TrimSpace(caCertificate); pem != "" {
		if !strings.Contains(pem, "-----BEGIN CERTIFICATE-----") {
			return nil, fmt.Errorf("CA certificate must be one or more PEM-encoded certificates")
		}
		f, err := os.CreateTemp("", "pingfederate-auth-provider-ca-*.pem")
		if err != nil {
			return nil, fmt.Errorf("failed to create temporary CA file: %w", err)
		}
		if _, err := f.WriteString(pem + "\n"); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("failed to write temporary CA file: %w", err)
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("failed to close temporary CA file: %w", err)
		}
		if err := os.Chmod(f.Name(), 0o600); err != nil {
			return nil, fmt.Errorf("failed to protect temporary CA file: %w", err)
		}
		files = append(files, f.Name())
	}
	return files, nil
}
