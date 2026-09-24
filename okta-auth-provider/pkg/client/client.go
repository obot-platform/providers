package client

import (
	"strings"

	"github.com/okta/okta-sdk-golang/v5/okta"
)

// Retries for a rate-limited request, and the cap on the wait between them. The SDK leaves both at
// Go's zero value when unset, and neither zero value works: MaxRetries 0 stops the backoff before
// its first retry, so a single 429 is fatal, and MaxBackoff 0 clamps the wait computed from Okta's
// X-Rate-Limit-Reset header down to nothing.
const (
	oktaRateLimitMaxRetries        = 2
	oktaRateLimitMaxBackoffSeconds = 30
)

// NewServiceClient creates an Okta API client using private key JWT authentication.
// The SDK handles all token management automatically (JWT signing, token acquisition, caching, refresh).
//
// Parameters:
// - clientID: OAuth client ID for the API Services app
// - privateKeyPEM: PEM-encoded RSA private key (must be PKCS#1 format: "-----BEGIN RSA PRIVATE KEY-----")
// - orgURL: Full Okta domain (e.g., "https://dev-123456.okta.com")
// - scopes: OAuth scopes to request (e.g., []string{"okta.users.read", "okta.groups.read"})
//
// The Okta SDK automatically:
// 1. Creates and signs JWT assertions using the private key
// 2. Requests access tokens from Okta's /oauth2/v1/token endpoint
// 3. Caches tokens for their lifetime (typically 1 hour)
// 4. Automatically refreshes expired tokens
// 5. Includes tokens in all Management API requests
//
// Important: The private key must be in PKCS#1 format. If you have a PKCS#8 key
// (starts with "-----BEGIN PRIVATE KEY-----"), convert it using:
//
//	openssl rsa -in pkcs8_key.pem -out pkcs1_key.pem
func NewServiceClient(clientID, privateKeyPEM, orgURL string, scopes []string) (*okta.APIClient, error) {
	config, err := okta.NewConfiguration(
		okta.WithOrgUrl(orgURL),
		okta.WithAuthorizationMode("PrivateKey"),
		okta.WithClientId(clientID),
		okta.WithScopes(scopes),
		okta.WithPrivateKey(fixPEMFormat(privateKeyPEM)),
		okta.WithRateLimitMaxRetries(oktaRateLimitMaxRetries),
		okta.WithRateLimitMaxBackOff(oktaRateLimitMaxBackoffSeconds),
		// WithPrivateKeyId is optional - only needed if you have multiple JWKs registered
	)
	if err != nil {
		return nil, err
	}

	return okta.NewAPIClient(config), nil
}

func fixPEMFormat(pem string) string {
	return strings.TrimSpace(strings.Replace(pem, "\\n", "\n", -1))
}
