package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	workspaceURLEnv = "OBOT_DATABRICKS_MODEL_PROVIDER_WORKSPACE_URL"
	tokenEnv        = "OBOT_DATABRICKS_MODEL_PROVIDER_TOKEN"

	openAIResponsesAPI = "openai/v1/responses"
	openResponsesAPI   = "mlflow/v1/responses"
	openAIResponses    = "OpenAIResponses"
	openResponses      = "OpenResponses"

	maxServingEndpointsResponseSize = 8 << 20
)

type config struct {
	workspaceURL *url.URL
	token        string
	listenPort   string
	client       *http.Client
}

type servingEndpointsResponse struct {
	Endpoints []servingEndpoint `json:"endpoints"`
}

type databricksErrorResponse struct {
	Message string `json:"message"`
}

type upstreamResponseError struct {
	Operation  string
	StatusCode int
	Message    string
	Body       string
}

func (e *upstreamResponseError) Error() string {
	detail := e.Message
	if detail == "" {
		detail = e.Body
	}
	return fmt.Sprintf("%s: status %d: %s", e.Operation, e.StatusCode, detail)
}

func (e *upstreamResponseError) userMessage() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("Databricks request failed with status %d", e.StatusCode)
}

type servingEndpoint struct {
	Name              string                `json:"name"`
	CreationTimestamp int64                 `json:"creation_timestamp"`
	Task              string                `json:"task"`
	State             servingEndpointState  `json:"state"`
	Config            servingEndpointConfig `json:"config"`
}

type servingEndpointState struct {
	Ready string `json:"ready"`
}

type servingEndpointConfig struct {
	ServedEntities []servedEntity `json:"served_entities"`
}

type servedEntity struct {
	Name            string           `json:"name"`
	APITypes        []string         `json:"api_types"`
	FoundationModel *foundationModel `json:"foundation_model"`
}

type foundationModel struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	APITypes    []string `json:"api_types"`
}

type modelsResponse struct {
	Object string  `json:"object"`
	Data   []model `json:"data"`
}

type model struct {
	ID       string            `json:"id"`
	Object   string            `json:"object"`
	Created  int64             `json:"created"`
	OwnedBy  string            `json:"owned_by"`
	Metadata map[string]string `json:"metadata"`
}

func configFromEnv() (*config, error) {
	workspaceURL, err := parseWorkspaceURL(os.Getenv(workspaceURLEnv))
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(os.Getenv(tokenEnv))
	if token == "" {
		return nil, fmt.Errorf("%s is required", tokenEnv)
	}

	return &config{
		workspaceURL: workspaceURL,
		token:        token,
		listenPort:   os.Getenv("PORT"),
		client:       &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func parseWorkspaceURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%s is required", workspaceURLEnv)
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", workspaceURLEnv, err)
	}
	if u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("%s must be an absolute HTTPS URL", workspaceURLEnv)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("%s must contain only the workspace scheme and host", workspaceURLEnv)
	}
	u.Path = ""
	return u, nil
}

func (c *config) listModels(ctx context.Context) ([]model, error) {
	endpointURL := c.workspaceURL.JoinPath("api/2.0/serving-endpoints")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpointURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create serving endpoints request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list serving endpoints: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		var errorResponse databricksErrorResponse
		_ = json.Unmarshal(body, &errorResponse)
		return nil, &upstreamResponseError{
			Operation:  "list serving endpoints",
			StatusCode: resp.StatusCode,
			Message:    strings.TrimSpace(errorResponse.Message),
			Body:       strings.TrimSpace(string(body)),
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxServingEndpointsResponseSize+1))
	closeErr := resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read serving endpoints: %w", err)
	}
	if len(body) > maxServingEndpointsResponseSize {
		return nil, fmt.Errorf("serving endpoints response exceeds %d bytes", maxServingEndpointsResponseSize)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close serving endpoints response: %w", closeErr)
	}

	var response servingEndpointsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode serving endpoints: %w", err)
	}

	var result []model
	for _, endpoint := range response.Endpoints {
		if !isFoundationChatEndpoint(endpoint) {
			continue
		}
		result = append(result, modelFromEndpoint(endpoint))
	}

	if len(result) == 0 {
		return nil, errors.New("no ready Databricks foundation chat models with Responses API support are available")
	}
	return result, nil
}

func isFoundationChatEndpoint(endpoint servingEndpoint) bool {
	return endpoint.Task == "llm/v1/chat" &&
		endpoint.State.Ready == "READY" &&
		responsesDialect(endpoint.Config) != ""
}

// responsesDialect finds a Responses dialect shared by every served entity in an endpoint.
// It prefers OpenAIResponses when both dialects are supported and returns an empty string
// when no shared dialect exists.
func responsesDialect(config servingEndpointConfig) string {
	if len(config.ServedEntities) == 0 {
		return ""
	}

	common := map[string]bool{
		openAIResponses: true,
		openResponses:   true,
	}
	for _, entity := range config.ServedEntities {
		supported := entity.responseDialects()
		for dialect := range common {
			if !supported[dialect] {
				delete(common, dialect)
			}
		}
		if len(common) == 0 {
			return ""
		}
	}
	if common[openAIResponses] {
		return openAIResponses
	}
	return openResponses
}

func (e servedEntity) responseDialects() map[string]bool {
	result := map[string]bool{}
	addAPITypes := func(apiTypes []string) {
		for _, apiType := range apiTypes {
			switch apiType {
			case openAIResponsesAPI:
				result[openAIResponses] = true
			case openResponsesAPI:
				result[openResponses] = true
			}
		}
	}

	addAPITypes(e.APITypes)
	if e.FoundationModel != nil {
		addAPITypes(e.FoundationModel.APITypes)
	}
	return result
}

func modelFromEndpoint(endpoint servingEndpoint) model {
	dialect := responsesDialect(endpoint.Config)
	displayName := ""
	for _, entity := range endpoint.Config.ServedEntities {
		if entity.FoundationModel == nil {
			continue
		}
		displayName = strings.TrimSpace(entity.FoundationModel.DisplayName)
		if displayName == "" {
			displayName = strings.TrimSpace(entity.FoundationModel.Name)
		}
		if displayName != "" {
			break
		}
	}
	if displayName == "" {
		displayName = endpoint.Name
	}

	return model{
		ID:      endpoint.Name,
		Object:  "model",
		Created: time.UnixMilli(endpoint.CreationTimestamp).Unix(),
		OwnedBy: "databricks",
		Metadata: map[string]string{
			"displayName": displayName,
			"usage":       "llm",
			"dialect":     dialect,
		},
	}
}

func (c *config) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("http://127.0.0.1:" + c.listenPort))
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, req *http.Request) {
		models, err := c.listModels(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(modelsResponse{Object: "list", Data: models}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	return mux
}

func (c *config) run() error {
	listenHost := os.Getenv("OBOT_PROVIDER_LISTEN_HOST")
	if listenHost == "" {
		listenHost = "127.0.0.1"
	}
	server := &http.Server{
		Addr:              net.JoinHostPort(listenHost, c.listenPort),
		Handler:           c.handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Printf("[model-provider: Databricks] Starting discovery API on %s\n", server.Addr)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
