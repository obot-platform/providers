package main

import (
	"context"
	"fmt"
	"net"
	"testing"
)

func TestParseAzureAPIError(t *testing.T) {
	err := parseAzureAPIError(401, []byte(`{"error":{"code":"401","message":"Access denied due to invalid subscription key or wrong API endpoint. Make sure to provide a valid key for an active subscription and use a correct regional API endpoint for your resource."}}`))
	if got, want := err.Error(), "Azure API returned 401 (401): Access denied due to invalid subscription key or wrong API endpoint. Make sure to provide a valid key for an active subscription and use a correct regional API endpoint for your resource."; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}

	message := validationErrorMessage(fmt.Errorf("failed to validate Azure credentials: %w", err))
	if got, want := message, "Invalid Azure credentials: the API key is invalid or does not belong to the configured endpoint. Check the API key and endpoint."; got != want {
		t.Fatalf("validation error = %q, want %q", got, want)
	}
}

func TestValidationErrorMessageForInvalidHost(t *testing.T) {
	err := fmt.Errorf("failed to validate Azure credentials: request failed: %w", &net.DNSError{
		Err:  "no such host",
		Name: "calvin-testing-tempz.cognitiveservices.azure.com",
	})
	message := validationErrorMessage(err)
	if got, want := message, "Invalid Azure endpoint: host calvin-testing-tempz.cognitiveservices.azure.com was not found. Check the endpoint URL."; got != want {
		t.Fatalf("validation error = %q, want %q", got, want)
	}
}

func TestValidationErrorMessageForInvalidDeployments(t *testing.T) {
	t.Setenv("OBOT_AZURE_MODEL_PROVIDER_ENDPOINT", "https://example.openai.azure.com")
	t.Setenv("OBOT_AZURE_MODEL_PROVIDER_API_KEY", "unused")
	t.Setenv("OBOT_AZURE_MODEL_PROVIDER_DEPLOYMENTS", "claude-haiku-4-5:lflm:anthropic")

	err := mainErr(context.Background(), true)
	message := validationErrorMessage(err)
	if got, want := message, `invalid deployment spec "claude-haiku-4-5:lflm:anthropic": usage type "lflm" must be one of: llm, reasoning-llm, text-embedding, image-generation`; got != want {
		t.Fatalf("validation error = %q, want %q", got, want)
	}
}

func TestValidationErrorMessageForUnknownError(t *testing.T) {
	message := validationErrorMessage(fmt.Errorf("unexpected internal detail"))
	if got, want := message, "Invalid Azure credentials."; got != want {
		t.Fatalf("validation error = %q, want %q", got, want)
	}
}
