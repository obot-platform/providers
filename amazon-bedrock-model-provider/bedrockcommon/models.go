package bedrockcommon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4signer "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const (
	defaultRegion  = "us-east-1"
	signingService = "bedrock"
	defaultTimeout = 30 * time.Second

	dialectAnthropicMessages = "AnthropicMessages"
	dialectOpenAIResponses   = "OpenAIResponses"
	usageLLM                 = "llm"
)

type Model struct {
	ID       string            `json:"id"`
	Object   string            `json:"object,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type modelsResponse struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

type StaticAuth struct {
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

type HTTPError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e HTTPError) Error() string {
	if e.Body == "" {
		return e.Status
	}
	return fmt.Sprintf("%s: %s", e.Status, e.Body)
}

func mantleModelsURL(region string) string {
	if region == "" {
		region = defaultRegion
	}
	return fmt.Sprintf("https://bedrock-mantle.%s.api.aws/v1/models", region)
}

func ListMantleModels(ctx context.Context, client *http.Client, region string) ([]Model, error) {
	client = withDefaultTimeout(client)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mantleModelsURL(region), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= 300 {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if err != nil {
			return nil, HTTPError{StatusCode: resp.StatusCode, Status: resp.Status, Body: fmt.Sprintf("failed to read error response body: %v", err)}
		}
		return nil, HTTPError{StatusCode: resp.StatusCode, Status: resp.Status, Body: strings.TrimSpace(string(body))}
	}

	var models modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
		return nil, fmt.Errorf("failed to decode Mantle models response: %w", err)
	}
	return filterMantleModels(models.Data), nil
}

func withDefaultTimeout(client *http.Client) *http.Client {
	if client == nil {
		return &http.Client{Timeout: defaultTimeout}
	}
	if client.Timeout != 0 {
		return client
	}
	clientCopy := *client
	clientCopy.Timeout = defaultTimeout
	return &clientCopy
}

func filterMantleModels(models []Model) []Model {
	filtered := make([]Model, 0, len(models))
	for _, model := range models {
		dialect := dialectForModel(model.ID)
		if dialect == "" {
			continue
		}
		if model.Object == "" {
			model.Object = "model"
		}
		if model.Metadata == nil {
			model.Metadata = map[string]string{}
		}
		model.Metadata["dialect"] = dialect
		model.Metadata["usage"] = usageLLM
		filtered = append(filtered, model)
	}
	return filtered
}

func dialectForModel(id string) string {
	switch {
	case strings.HasPrefix(id, "anthropic."):
		return dialectAnthropicMessages
	case strings.HasPrefix(id, "openai."), strings.HasPrefix(id, "google."):
		return dialectOpenAIResponses
	default:
		return ""
	}
}

type StaticAuthTransport struct {
	Auth StaticAuth
	Next http.RoundTripper
}

func (t StaticAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := signRequest(req, t.Auth, time.Now()); err != nil {
		return nil, err
	}
	next := t.Next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(req)
}

type APIKeyTransport struct {
	APIKey string
	Next   http.RoundTripper
}

func (t APIKeyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Del("X-Api-Key")
	req.Header.Set("Authorization", "Bearer "+t.APIKey)
	next := t.Next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(req)
}

func signRequest(req *http.Request, auth StaticAuth, signingTime time.Time) error {
	if auth.Region == "" {
		auth.Region = defaultRegion
	}
	if req.Body == nil {
		req.Body = http.NoBody
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return fmt.Errorf("failed to read request body for AWS signing: %w", err)
	}
	if err := req.Body.Close(); err != nil {
		return fmt.Errorf("failed to close request body after AWS signing read: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(body))

	sum := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(sum[:])
	req.Header.Del("Authorization")
	req.Header.Del("X-Api-Key")
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	return v4signer.NewSigner().SignHTTP(req.Context(), aws.Credentials{
		AccessKeyID:     auth.AccessKeyID,
		SecretAccessKey: auth.SecretAccessKey,
		SessionToken:    auth.SessionToken,
	}, req, payloadHash, signingService, auth.Region, signingTime)
}
