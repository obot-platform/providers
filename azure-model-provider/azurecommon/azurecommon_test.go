package azurecommon

import "testing"

func TestBuildModelsFromDeployments(t *testing.T) {
	models := BuildModelsFromDeployments(map[string]Deployment{
		"custom-claude-deployment": {Usage: "llm", Dialect: DialectAnthropicMessages},
	})
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	if models[0].ID != "custom-claude-deployment" {
		t.Fatalf("model ID = %q, want deployment name", models[0].ID)
	}
	if got := models[0].Metadata["usage"]; got != "llm" {
		t.Fatalf("usage = %q, want llm", got)
	}
	if got := models[0].Metadata["dialect"]; got != DialectAnthropicMessages {
		t.Fatalf("dialect = %q, want %q", got, DialectAnthropicMessages)
	}
}

func TestDialectForModelFormat(t *testing.T) {
	tests := []struct {
		format string
		want   string
		wantOK bool
	}{
		{format: "OpenAI", want: DialectOpenAIResponses, wantOK: true},
		{format: "anthropic", want: DialectAnthropicMessages, wantOK: true},
		{format: " Meta ", wantOK: false},
		{format: "", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			got, ok := DialectForModelFormat(tt.format)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("DialectForModelFormat(%q) = (%q, %v), want (%q, %v)", tt.format, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
