package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// APIClient wraps JumpCloud API calls using either x-api-key or service-account bearer-token authentication.
type APIClient struct {
	baseURL                    string
	apiKey                     string
	orgID                      string
	serviceAccountClientID     string
	serviceAccountClientSecret string
	serviceAccountTokenURL     string
	httpClient                 *http.Client

	mu                sync.Mutex
	cachedAccessToken string
	cachedTokenExpiry time.Time
}

type ServiceAccountOptions struct {
	ClientID     string
	ClientSecret string
	TokenURL     string
}

func New(baseURL, apiKey, orgID string, serviceAccount ServiceAccountOptions) *APIClient {
	return &APIClient{
		baseURL:                    strings.TrimSuffix(baseURL, "/"),
		apiKey:                     strings.TrimSpace(apiKey),
		orgID:                      strings.TrimSpace(orgID),
		serviceAccountClientID:     strings.TrimSpace(serviceAccount.ClientID),
		serviceAccountClientSecret: strings.TrimSpace(serviceAccount.ClientSecret),
		serviceAccountTokenURL:     strings.TrimSpace(serviceAccount.TokenURL),
		httpClient:                 http.DefaultClient,
	}
}

// DoRequest performs an authenticated request to the JumpCloud API.
func (c *APIClient) DoRequest(ctx context.Context, method, path string, query url.Values, body []byte) (*http.Response, error) {
	reqURL := c.baseURL + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}

	buildRequest := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader(body))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("Accept", "application/json")
		if err := c.applyAuth(ctx, req); err != nil {
			return nil, err
		}
		if c.orgID != "" {
			req.Header.Set("x-org-id", c.orgID)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		return req, nil
	}

	req, err := buildRequest()
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}

	if resp.StatusCode != http.StatusUnauthorized || c.apiKey != "" {
		return resp, nil
	}

	resp.Body.Close()
	c.clearCachedAccessToken()

	retryReq, err := buildRequest()
	if err != nil {
		return nil, err
	}

	retryResp, err := c.httpClient.Do(retryReq)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}

	return retryResp, nil
}

func (c *APIClient) clearCachedAccessToken() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cachedAccessToken = ""
	c.cachedTokenExpiry = time.Time{}
}

func (c *APIClient) applyAuth(ctx context.Context, req *http.Request) error {
	if c.apiKey != "" {
		req.Header.Set("x-api-key", c.apiKey)
		return nil
	}

	accessToken, err := c.getServiceAccountAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("failed to obtain JumpCloud service account access token: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	return nil
}

func (c *APIClient) getServiceAccountAccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cachedAccessToken != "" && time.Now().Before(c.cachedTokenExpiry) {
		return c.cachedAccessToken, nil
	}

	if c.serviceAccountClientID == "" || c.serviceAccountClientSecret == "" || c.serviceAccountTokenURL == "" {
		return "", fmt.Errorf("service account auth is not configured")
	}

	form := url.Values{}
	form.Set("scope", "api")
	form.Set("grant_type", "client_credentials")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serviceAccountTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create token request: %w", err)
	}

	basic := base64.StdEncoding.EncodeToString([]byte(c.serviceAccountClientID + ":" + c.serviceAccountClientSecret))
	req.Header.Set("Authorization", "Basic "+basic)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("failed to decode token response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("token response missing access_token")
	}

	expiresIn := time.Duration(tokenResp.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = 55 * time.Minute
	}
	refreshSkew := 30 * time.Second
	if expiresIn > refreshSkew {
		expiresIn -= refreshSkew
	}

	c.cachedAccessToken = tokenResp.AccessToken
	c.cachedTokenExpiry = time.Now().Add(expiresIn)
	return c.cachedAccessToken, nil
}

func bodyReader(body []byte) io.Reader {
	if body == nil {
		return nil
	}
	return bytes.NewReader(body)
}
