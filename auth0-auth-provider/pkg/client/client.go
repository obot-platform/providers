package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ManagementClient wraps Auth0 Management API calls with automatic token management.
type ManagementClient struct {
	baseURL      string
	clientID     string
	clientSecret string

	mu          sync.Mutex
	accessToken string
	expiry      time.Time
}

// NewManagementClient creates a client for the Auth0 Management API.
// Uses client credentials grant to obtain and refresh tokens automatically.
func NewManagementClient(domain, clientID, clientSecret string) *ManagementClient {
	return &ManagementClient{
		baseURL:      "https://" + strings.TrimSuffix(domain, "/"),
		clientID:     clientID,
		clientSecret: clientSecret,
	}
}

// tokenResponse represents the Auth0 token endpoint response.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

// getToken returns a valid access token, refreshing if necessary.
func (c *ManagementClient) getToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Return cached token if still valid (with 60s buffer)
	if c.accessToken != "" && time.Now().Add(60*time.Second).Before(c.expiry) {
		return c.accessToken, nil
	}

	// Request new token via client credentials grant
	data := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"audience":      {c.baseURL + "/api/v2/"},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/oauth/token", strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to request token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("failed to decode token response: %w", err)
	}

	c.accessToken = tokenResp.AccessToken
	c.expiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	return c.accessToken, nil
}

// clearToken invalidates the cached access token so the next call to getToken fetches a fresh one.
func (c *ManagementClient) clearToken() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accessToken = ""
	c.expiry = time.Time{}
}

// DoRequest performs an authenticated request to the Auth0 Management API.
// If the server responds with 401 Unauthorized, the cached token is cleared and the request is retried once.
// The body parameter is a []byte so that it can be safely replayed on retry.
func (c *ManagementClient) DoRequest(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	token, err := c.getToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get management token: %w", err)
	}

	reqURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		c.clearToken()

		token, err = c.getToken(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to refresh management token: %w", err)
		}

		req, err = http.NewRequestWithContext(ctx, method, reqURL, bodyReader(body))
		if err != nil {
			return nil, fmt.Errorf("failed to create retry request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
	}

	return resp, nil
}

// bodyReader returns a fresh *bytes.Reader for the given body, or nil if body is nil.
func bodyReader(body []byte) io.Reader {
	if body == nil {
		return nil
	}
	return bytes.NewReader(body)
}
