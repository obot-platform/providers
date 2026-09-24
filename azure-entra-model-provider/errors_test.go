package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseTokenRequestError(t *testing.T) {
	err := parseTokenRequestError(400, []byte(`{"error":"unauthorized_client","error_description":"AADSTS700016: Application was not found. Trace ID: abc Correlation ID: def Timestamp: 2026-04-27 18:48:53Z","error_uri":"https://login.microsoftonline.com/error?code=700016"}`))
	if got, want := err.Error(), "token request failed with status 400 (unauthorized_client): AADSTS700016: Application was not found. Trace ID: abc Correlation ID: def Timestamp: 2026-04-27 18:48:53Z"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}

	message := validationErrorMessage(fmt.Errorf("failed to fetch deployments from Azure: %w", err))
	if got, want := message, "Invalid Azure Entra ID credentials: the client ID was not found in the tenant. Check the client ID and tenant ID. More details: https://login.microsoftonline.com/error?code=700016"; got != want {
		t.Fatalf("validation error = %q, want %q", got, want)
	}
}

func TestValidationErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "incorrect client ID",
			err:  &tokenRequestError{Code: "unauthorized_client", Description: "AADSTS700016: Application with identifier 'asdf' was not found in the directory 'Acorn Labs'. Trace ID: b9af10e8 Correlation ID: 901f72ec Timestamp: 2026-04-27 18:48:53Z", URI: "https://login.microsoftonline.com/error?code=700016"},
			want: "Invalid Azure Entra ID credentials: the client ID was not found in the tenant. Check the client ID and tenant ID. More details: https://login.microsoftonline.com/error?code=700016",
		},
		{
			name: "incorrect secret",
			err:  &tokenRequestError{Code: "invalid_client", Description: "AADSTS7000215: Invalid client secret provided. Ensure the secret being sent in the request is the client secret value, not the client secret ID. Trace ID: e4bfa1c6 Correlation ID: 4f25ae0f Timestamp: 2026-04-27 18:50:29Z"},
			want: "Invalid Azure Entra ID credentials: the client secret is invalid. Use the secret value, not the secret ID.",
		},
		{
			name: "tenant ID",
			err:  &tokenRequestError{Code: "invalid_request", Description: "AADSTS900023: Specified tenant identifier 'asdf' is neither a valid DNS name, nor a valid external domain. Trace ID: ff41e5b9 Correlation ID: e0026983 Timestamp: 2026-04-27 18:51:05Z"},
			want: "Invalid Azure Entra ID credentials: the tenant ID is not valid.",
		},
		{
			name: "subscription ID",
			err:  &managementRequestError{StatusCode: 400, Code: "InvalidSubscriptionId", Message: "The provided subscription identifier 'asdf' is malformed or invalid."},
			want: "Invalid Azure configuration: the subscription ID is malformed or invalid.",
		},
		{
			name: "group or resource name",
			err:  &managementRequestError{StatusCode: 403, Code: "AuthorizationFailed", Message: "The client does not have authorization to perform action 'Microsoft.CognitiveServices/accounts/deployments/read' over scope '/subscriptions/id/resourceGroups/asdf/providers/Microsoft.CognitiveServices/accounts/name' or the scope is invalid."},
			want: "Invalid Azure configuration: the app does not have access to read deployments for the configured resource group and resource name. Check the resource group, resource name, and app permissions.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validationErrorMessage(fmt.Errorf("failed to fetch deployments from Azure: %w", tt.err))
			if got != tt.want {
				t.Fatalf("validationErrorMessage() = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, `{"error"`) || strings.Contains(got, "Trace ID:") {
				t.Fatalf("validationErrorMessage() returned raw Azure details: %q", got)
			}
		})
	}
}

func TestParseManagementRequestError(t *testing.T) {
	err := parseManagementRequestError(400, []byte(`{"error":{"code":"InvalidSubscriptionId","message":"The provided subscription identifier 'asdf' is malformed or invalid."}}`))
	if got, want := err.Error(), "management API returned 400 (InvalidSubscriptionId): The provided subscription identifier 'asdf' is malformed or invalid."; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}

	message := validationErrorMessage(fmt.Errorf("failed to fetch deployments from Azure: %w", err))
	if got, want := message, "Invalid Azure configuration: the subscription ID is malformed or invalid."; got != want {
		t.Fatalf("validation error = %q, want %q", got, want)
	}
}
