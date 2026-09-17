package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestParseWorkspaceURL(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		value   string
		wantErr bool
	}{
		{
			name:  "valid",
			value: "https://example.cloud.databricks.com/",
		},
		{
			name:  "custom domain",
			value: "https://models.example.com",
		},
		{
			name:    "empty",
			wantErr: true,
		},
		{
			name:    "http",
			value:   "http://example.com",
			wantErr: true,
		},
		{
			name:    "path",
			value:   "https://example.com/workspace",
			wantErr: true,
		},
		{
			name:    "query",
			value:   "https://example.com?x=y",
			wantErr: true,
		},
		{
			name:    "fragment",
			value:   "https://example.com#fragment",
			wantErr: true,
		},
		{
			name:    "userinfo",
			value:   "https://user@example.com",
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseWorkspaceURL(test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseWorkspaceURL() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestParseWorkspaceURLDefaultsToHTTPS(t *testing.T) {
	t.Parallel()

	u, err := parseWorkspaceURL("example.cloud.databricks.com")
	if err != nil {
		t.Fatalf("parseWorkspaceURL() error = %v", err)
	}
	if got, want := u.String(), "https://example.cloud.databricks.com"; got != want {
		t.Fatalf("parseWorkspaceURL() = %q, want %q", got, want)
	}
}

func TestListModels(t *testing.T) {
	t.Parallel()

	var requests int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests++
		if got := req.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		if req.URL.Path != "/api/2.0/serving-endpoints" {
			t.Errorf("path = %q", req.URL.Path)
		}
		if req.URL.RawQuery != "" {
			t.Errorf("query = %q, want empty", req.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"endpoints":[
				{"name":"databricks-qwen3-next-80b-a3b-instruct","creation_timestamp":1699610000000,"task":"llm/v1/chat","state":{"ready":"READY","config_update":"NOT_UPDATING"},"config":{"served_entities":[{"name":"databricks-qwen3-next-80b-a3b-instruct","entity_name":"system.ai.qwen3-next-80b-a3b-instruct","type":"FOUNDATION_MODEL","foundation_model":{"name":"system.ai.qwen3-next-80b-a3b-instruct","display_name":"Qwen3 Next Instruct","api_types":["mlflow/v1/chat/completions","mlflow/v1/responses"]}}]}},
				{"name":"databricks-gpt-5","creator":null,"creation_timestamp":1000,"task":"llm/v1/chat","state":{"ready":"READY"},"config":{"served_entities":[{"foundation_model":{"api_types":["mlflow/v1/responses"]}}]}},
				{"name":"databricks-gpt-oss-120b","creation_timestamp":1754179200000,"task":"llm/v1/chat","state":{"ready":"READY","config_update":"NOT_UPDATING"},"config":{"served_entities":[{"name":"databricks-gpt-oss-120b","entity_name":"system.ai.gpt-oss-120b","type":"FOUNDATION_MODEL","foundation_model":{"name":"system.ai.gpt-oss-120b","display_name":"GPT OSS 120B","api_types":["mlflow/v1/chat/completions","mlflow/v1/responses"]}}]}},
				{"name":"databricks-openai-native","creator":null,"creation_timestamp":4000,"task":"llm/v1/chat","state":{"ready":"READY"},"config":{"served_entities":[{"foundation_model":{"api_types":["openai/v1/responses"]}}]}},
				{"name":"databricks-z-both-responses","creator":null,"creation_timestamp":5000,"task":"llm/v1/chat","state":{"ready":"READY"},"config":{"served_entities":[{"foundation_model":{"api_types":["mlflow/v1/responses","openai/v1/responses"]}}]}},
				{"name":"databricks-gte-large-en","creation_timestamp":1699610000000,"task":"llm/v1/embeddings","state":{"ready":"READY"},"config":{"served_entities":[{"foundation_model":{"api_types":["mlflow/v1/embeddings"]}}]}},
				{"name":"databricks-meta-llama-3-1-8b-instruct","creation_timestamp":1699610000000,"task":"llm/v1/chat","state":{"ready":"READY"},"config":{"served_entities":[{"foundation_model":{"api_types":["mlflow/v1/chat/completions"]}}]}},
				{"name":"databricks-custom","creator":"user@example.com","task":"llm/v1/chat","state":{"ready":"READY"},"config":{"served_entities":[{"foundation_model":{"api_types":["mlflow/v1/responses"]}}]}},
				{"name":"databricks-not-ready","creator":null,"task":"llm/v1/chat","state":{"ready":"NOT_READY"}},
				{"name":"other","creator":null,"task":"llm/v1/chat","state":{"ready":"READY"},"config":{"served_entities":[{"foundation_model":{"api_types":["openai/v1/responses"]}}]}}
			]
		}`))
	}))
	t.Cleanup(upstream.Close)

	baseURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config{workspaceURL: baseURL, token: "secret", client: upstream.Client()}
	models, err := cfg.listModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if len(models) != 7 {
		t.Fatalf("models = %#v", models)
	}
	if models[0].ID != "databricks-qwen3-next-80b-a3b-instruct" || models[0].Metadata["dialect"] != "OpenResponses" || models[0].Metadata["displayName"] != "Qwen3 Next Instruct" {
		t.Errorf("Qwen model = %#v", models[0])
	}
	if models[1].ID != "databricks-gpt-5" || models[1].Metadata["dialect"] != "OpenResponses" {
		t.Errorf("GPT model = %#v", models[1])
	}
	if models[2].ID != "databricks-gpt-oss-120b" || models[2].Metadata["dialect"] != "OpenResponses" || models[2].Metadata["displayName"] != "GPT OSS 120B" {
		t.Errorf("GPT OSS model = %#v", models[2])
	}
	if models[3].ID != "databricks-openai-native" || models[3].Metadata["dialect"] != "OpenAIResponses" {
		t.Errorf("OpenAI-native model = %#v", models[3])
	}
	if models[4].ID != "databricks-z-both-responses" || models[4].Metadata["dialect"] != "OpenAIResponses" {
		t.Errorf("dual-dialect model = %#v", models[4])
	}
	if models[5].ID != "databricks-custom" || models[5].Metadata["dialect"] != "OpenResponses" {
		t.Errorf("creator-owned model = %#v", models[5])
	}
	if models[6].ID != "other" || models[6].Metadata["dialect"] != "OpenAIResponses" {
		t.Errorf("non-prefixed model = %#v", models[6])
	}
}

func TestIsFoundationChatEndpointRequiresResponsesForAllServedEntities(t *testing.T) {
	t.Parallel()

	responsesAPI := []string{"mlflow/v1/responses"}
	openAIResponsesAPITypes := []string{"openai/v1/responses"}
	chatCompletionsAPI := []string{"mlflow/v1/chat/completions"}

	for _, test := range []struct {
		name   string
		config servingEndpointConfig
		want   bool
	}{
		{
			name: "responses only",
			config: servingEndpointConfig{ServedEntities: []servedEntity{
				{
					Name:            "responses",
					FoundationModel: &foundationModel{APITypes: responsesAPI},
				},
			}},
			want: true,
		},
		{
			name: "responses only at entity level",
			config: servingEndpointConfig{ServedEntities: []servedEntity{
				{
					Name:     "responses",
					APITypes: responsesAPI,
				},
			}},
			want: true,
		},
		{
			name: "OpenAI responses only",
			config: servingEndpointConfig{ServedEntities: []servedEntity{
				{
					Name:            "responses",
					FoundationModel: &foundationModel{APITypes: openAIResponsesAPITypes},
				},
			}},
			want: true,
		},
		{
			name: "chat completions only",
			config: servingEndpointConfig{ServedEntities: []servedEntity{
				{
					Name:            "chat",
					FoundationModel: &foundationModel{APITypes: chatCompletionsAPI},
				},
			}},
		},
		{
			name: "entity without responses support",
			config: servingEndpointConfig{ServedEntities: []servedEntity{
				{
					Name:            "responses",
					FoundationModel: &foundationModel{APITypes: responsesAPI},
				},
				{
					Name:            "chat",
					FoundationModel: &foundationModel{APITypes: chatCompletionsAPI},
				},
			}},
		},
		{
			name: "mixed APIs on one entity",
			config: servingEndpointConfig{ServedEntities: []servedEntity{
				{
					Name:            "both",
					FoundationModel: &foundationModel{APITypes: []string{"mlflow/v1/chat/completions", "mlflow/v1/responses"}},
				},
			}},
			want: true,
		},
		{
			name: "responses shared by all entities",
			config: servingEndpointConfig{ServedEntities: []servedEntity{
				{
					Name:            "both",
					FoundationModel: &foundationModel{APITypes: []string{"openai/v1/responses", "mlflow/v1/responses"}},
				},
				{
					Name:            "openresponses",
					FoundationModel: &foundationModel{APITypes: responsesAPI},
				},
			}},
			want: true,
		},
		{
			name: "incompatible response dialects",
			config: servingEndpointConfig{ServedEntities: []servedEntity{
				{
					Name:            "openresponses",
					FoundationModel: &foundationModel{APITypes: responsesAPI},
				},
				{
					Name:            "openai",
					FoundationModel: &foundationModel{APITypes: openAIResponsesAPITypes},
				},
			}},
		},
		{
			name: "missing API metadata",
			config: servingEndpointConfig{ServedEntities: []servedEntity{
				{
					Name:            "missing",
					FoundationModel: &foundationModel{},
				},
			}},
		},
		{
			name: "empty API metadata",
			config: servingEndpointConfig{ServedEntities: []servedEntity{
				{
					Name:            "empty",
					FoundationModel: &foundationModel{APITypes: []string{}},
				},
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			endpoint := servingEndpoint{
				Name: "databricks-test", Task: "llm/v1/chat",
				State: servingEndpointState{Ready: "READY"}, Config: test.config,
			}
			if got := isFoundationChatEndpoint(endpoint); got != test.want {
				t.Errorf("isFoundationChatEndpoint() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestListModelsErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		statusCode int
		body       string
		want       string
		wantExact  string
	}{
		{
			name:       "upstream",
			statusCode: http.StatusUnauthorized,
			body:       `{"error_code":401,"message":"Credential was not sent or was of an unsupported type for this API."}`,
			wantExact:  "list serving endpoints: status 401: Credential was not sent or was of an unsupported type for this API.",
		},
		{
			name:       "invalid json",
			statusCode: http.StatusOK,
			body:       `{`,
			want:       "decode serving endpoints",
		},
		{
			name:       "trailing json",
			statusCode: http.StatusOK,
			body:       `{"endpoints":[]} {}`,
			want:       "decode serving endpoints",
		},
		{
			name:       "oversized",
			statusCode: http.StatusOK,
			body:       `{"padding":"` + strings.Repeat("x", 8<<20) + `","endpoints":[]}`,
			want:       "response exceeds 8388608 bytes",
		},
		{
			name:       "empty",
			statusCode: http.StatusOK,
			body:       `{"endpoints":[]}`,
			want:       "no ready Databricks",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.statusCode)
				_, _ = w.Write([]byte(test.body))
			}))
			t.Cleanup(upstream.Close)
			baseURL, _ := url.Parse(upstream.URL)
			cfg := &config{workspaceURL: baseURL, token: "secret", client: upstream.Client()}
			_, err := cfg.listModels(context.Background())
			if err == nil {
				t.Fatalf("expected error")
			}
			if test.wantExact != "" && err.Error() != test.wantExact {
				t.Fatalf("error = %q, want %q", err, test.wantExact)
			}
			if test.want != "" && !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestHandler(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"endpoints":[{"name":"databricks-gpt-oss-120b","task":"llm/v1/chat","state":{"ready":"READY"},"config":{"served_entities":[{"foundation_model":{"name":"system.ai.gpt-oss-120b","display_name":"GPT OSS 120B","api_types":["mlflow/v1/chat/completions","mlflow/v1/responses"]}}]}}]}`))
	}))
	t.Cleanup(upstream.Close)
	baseURL, _ := url.Parse(upstream.URL)
	cfg := &config{workspaceURL: baseURL, token: "secret", listenPort: "1234", client: upstream.Client()}

	health := httptest.NewRecorder()
	cfg.handler().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/", nil))
	if health.Code != http.StatusOK || health.Body.String() != "http://127.0.0.1:1234" {
		t.Fatalf("health = %d %q", health.Code, health.Body.String())
	}

	modelsRecorder := httptest.NewRecorder()
	cfg.handler().ServeHTTP(modelsRecorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	var response modelsResponse
	if err := json.NewDecoder(modelsRecorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if modelsRecorder.Code != http.StatusOK || len(response.Data) != 1 || response.Data[0].Metadata["dialect"] != "OpenResponses" {
		t.Fatalf("models response = %d %#v", modelsRecorder.Code, response)
	}
}
