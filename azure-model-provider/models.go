package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/obot-platform/providers/azure-model-provider/azurecommon"
)

// validUsageTypes is the set of recognized usage type values.
var validUsageTypes = map[string]struct{}{
	"llm":              {},
	"reasoning-llm":    {},
	"text-embedding":   {},
	"image-generation": {},
}

// parseDeployments parses a comma-separated list of deployment specs.
// Each spec is deploymentName[:usageType[:dialect]]. Usage defaults to "llm"
// and dialect defaults to OpenAIResponses.
func parseDeployments(s string) (map[string]azurecommon.Deployment, error) {
	deployments := make(map[string]azurecommon.Deployment)
	for spec := range strings.SplitSeq(s, ",") {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}

		parts := strings.Split(spec, ":")
		if len(parts) > 3 {
			return nil, fmt.Errorf("invalid deployment spec %q: expected deployment[:usage[:dialect]]", spec)
		}
		deployment := strings.TrimSpace(parts[0])
		usageType := "llm"
		dialect := azurecommon.DialectOpenAIResponses
		if len(parts) >= 2 {
			usageType = strings.TrimSpace(parts[1])
			if deployment == "" || usageType == "" {
				return nil, fmt.Errorf("invalid deployment spec %q: deployment name and usage type must be non-empty", spec)
			}
		}
		if err := validateName(deployment); err != nil {
			return nil, fmt.Errorf("invalid deployment spec %q: %w", spec, err)
		}
		if _, ok := validUsageTypes[usageType]; !ok {
			return nil, fmt.Errorf("invalid deployment spec %q: usage type %q must be one of: llm, reasoning-llm, text-embedding, image-generation", spec, usageType)
		}
		if len(parts) == 3 {
			dialectName := strings.TrimSpace(parts[2])
			var ok bool
			dialect, ok = azurecommon.DialectForModelFormat(dialectName)
			if !ok {
				return nil, fmt.Errorf("invalid deployment spec %q: dialect %q must be one of: openai, anthropic", spec, dialectName)
			}
		}
		deployments[deployment] = azurecommon.Deployment{Usage: usageType, Dialect: dialect}
	}
	if len(deployments) == 0 {
		return nil, fmt.Errorf("no valid deployments found in %q", s)
	}
	return deployments, nil
}

// validNameRe is an allowlist for Azure deployment names: alphanumeric, hyphens,
// underscores, and dots, starting with an alphanumeric character, max 64 characters.
var validNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9\-_.]{0,63}$`)

// validateName rejects deployment names that don't match the Azure naming allowlist.
func validateName(name string) error {
	if !validNameRe.MatchString(name) {
		return fmt.Errorf("name %q is invalid (must match [a-zA-Z0-9][a-zA-Z0-9\\-_.]{0,63})", name)
	}
	return nil
}
