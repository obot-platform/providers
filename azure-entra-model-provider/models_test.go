package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/obot-platform/providers/azure-model-provider/azurecommon"
)

func TestFetchDeploymentsFromManagement(t *testing.T) {
	tests := []struct {
		name       string
		response   managementDeploymentsResponse
		statusCode int
		wantErr    bool
		want       map[string]azurecommon.Deployment
	}{
		{
			name: "single deployment",
			response: managementDeploymentsResponse{Value: []managementDeployment{
				{DeploymentName: "my-gpt4", Properties: managementDeploymentProperties{Model: managementDeploymentModel{ProviderFormat: "OpenAI", ModelName: "gpt-4"}}},
			}},
			statusCode: 200,
			want: map[string]azurecommon.Deployment{
				"my-gpt4": {Usage: "llm", Dialect: azurecommon.DialectOpenAIResponses},
			},
		},
		{
			name: "usage type inferred from model name",
			response: managementDeploymentsResponse{Value: []managementDeployment{
				{DeploymentName: "custom-deployment", Properties: managementDeploymentProperties{Model: managementDeploymentModel{ProviderFormat: "OpenAI", ModelName: "text-embedding-ada-002"}}},
			}},
			statusCode: 200,
			want: map[string]azurecommon.Deployment{
				"custom-deployment": {Usage: "text-embedding", Dialect: azurecommon.DialectOpenAIResponses},
			},
		},
		{
			name: "Anthropic model format",
			response: managementDeploymentsResponse{Value: []managementDeployment{
				{DeploymentName: "claude-deployment", Properties: managementDeploymentProperties{Model: managementDeploymentModel{ProviderFormat: "Anthropic", ModelName: "claude-sonnet-4"}}},
			}},
			statusCode: 200,
			want: map[string]azurecommon.Deployment{
				"claude-deployment": {Usage: "llm", Dialect: azurecommon.DialectAnthropicMessages},
			},
		},
		{
			name: "two deployments of the same model",
			response: managementDeploymentsResponse{Value: []managementDeployment{
				{DeploymentName: "deploy-a", Properties: managementDeploymentProperties{Model: managementDeploymentModel{ProviderFormat: "OpenAI"}}},
				{DeploymentName: "deploy-b", Properties: managementDeploymentProperties{Model: managementDeploymentModel{ProviderFormat: "OpenAI"}}},
			}},
			statusCode: 200,
			want: map[string]azurecommon.Deployment{
				"deploy-a": {Usage: "llm", Dialect: azurecommon.DialectOpenAIResponses},
				"deploy-b": {Usage: "llm", Dialect: azurecommon.DialectOpenAIResponses},
			},
		},
		{
			name: "unknown model format is omitted",
			response: managementDeploymentsResponse{Value: []managementDeployment{
				{DeploymentName: "known", Properties: managementDeploymentProperties{Model: managementDeploymentModel{ProviderFormat: "OpenAI"}}},
				{DeploymentName: "unknown", Properties: managementDeploymentProperties{Model: managementDeploymentModel{ProviderFormat: "Meta"}}},
			}},
			statusCode: 200,
			want: map[string]azurecommon.Deployment{
				"known": {Usage: "llm", Dialect: azurecommon.DialectOpenAIResponses},
			},
		},
		{
			name:       "non-200 response returns error",
			statusCode: 403,
			wantErr:    true,
		},
		{
			name:       "empty deployment list",
			statusCode: 200,
			want:       map[string]azurecommon.Deployment{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgmtServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer mgmtServer.Close()

			got, err := fetchDeploymentsFromManagementURL(context.Background(), mgmtServer.URL, "fake-token")
			if (err != nil) != tt.wantErr {
				t.Fatalf("fetchDeploymentsFromManagementURL() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d deployments, want %d: %v", len(got), len(tt.want), got)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("deployments[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}
