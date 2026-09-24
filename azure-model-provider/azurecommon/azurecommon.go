package azurecommon

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	DialectOpenAIResponses   = "OpenAIResponses"
	DialectAnthropicMessages = "AnthropicMessages"
)

type Deployment struct {
	Usage   string
	Dialect string
}

type Model struct {
	ID       string            `json:"id"`
	Object   string            `json:"object"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// azureEndpointSuffixes is an allowlist of recognized Azure OpenAI endpoint host suffixes.
var azureEndpointSuffixes = []string{
	".openai.azure.com",
	".cognitiveservices.azure.com",
	".services.ai.azure.com",
	".models.ai.azure.com",
}

// ValidateEndpoint enforces HTTPS, an empty query/fragment, and an allowlisted Azure OpenAI host.
func ValidateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "https" {
		return errors.New("endpoint must use HTTPS")
	}
	if u.User != nil {
		return errors.New("endpoint must not contain user info")
	}
	if u.RawQuery != "" {
		return errors.New("endpoint must not contain a query string")
	}
	if u.Fragment != "" {
		return errors.New("endpoint must not contain a fragment")
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range azureEndpointSuffixes {
		if strings.HasSuffix(host, suffix) {
			return nil
		}
	}
	return fmt.Errorf("host %q is not a recognized Azure OpenAI endpoint (must end in .openai.azure.com, .cognitiveservices.azure.com, .services.ai.azure.com, or .models.ai.azure.com)", host)
}

// DeploymentUsageType infers usage type from a deployment name.
// Used by the Entra provider where deployments are auto-discovered and usage type cannot be specified explicitly.
func DeploymentUsageType(name string) string {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "embed") {
		return "text-embedding"
	}
	if strings.Contains(lower, "dall-e") || strings.Contains(lower, "image") {
		return "image-generation"
	}
	return "llm"
}

// DialectForModelFormat maps the ARM deployment model format to a nanobot dialect.
func DialectForModelFormat(format string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "openai":
		return DialectOpenAIResponses, true
	case "anthropic":
		return DialectAnthropicMessages, true
	default:
		return "", false
	}
}

// BuildModelsFromDeployments converts deployment metadata into model objects.
func BuildModelsFromDeployments(deployments map[string]Deployment) []Model {
	models := make([]Model, 0, len(deployments))
	for deploymentName, deployment := range deployments {
		models = append(models, Model{
			ID:     deploymentName,
			Object: "model",
			Metadata: map[string]string{
				"usage":   deployment.Usage,
				"dialect": deployment.Dialect,
			},
		})
	}
	return models
}
