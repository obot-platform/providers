package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/obot-platform/providers/amazon-bedrock-model-provider/bedrockcommon"
)

func TestValidationErrorMessageForHTTPError(t *testing.T) {
	tests := []struct {
		name string
		err  bedrockcommon.HTTPError
		want string
	}{
		{
			name: "expired token body",
			err: bedrockcommon.HTTPError{
				StatusCode: http.StatusForbidden,
				Status:     http.StatusText(http.StatusForbidden),
				Body:       "Bearer token has expired",
			},
			want: "Invalid Amazon Bedrock credentials: the bearer token has expired. Generate a new API key and try again.",
		},
		{
			name: "unauthorized",
			err: bedrockcommon.HTTPError{
				StatusCode: http.StatusUnauthorized,
				Status:     http.StatusText(http.StatusUnauthorized),
			},
			want: "Invalid Amazon Bedrock credentials: access was denied. Check that the API key is valid, not expired, and configured for the selected region.",
		},
		{
			name: "access denied body",
			err: bedrockcommon.HTTPError{
				StatusCode: http.StatusForbidden,
				Status:     http.StatusText(http.StatusForbidden),
				Body:       "Access denied",
			},
			want: "Invalid Amazon Bedrock credentials: access was denied. Check that the API key is valid, not expired, and configured for the selected region.",
		},
		{
			name: "body passthrough",
			err: bedrockcommon.HTTPError{
				StatusCode: http.StatusBadRequest,
				Status:     http.StatusText(http.StatusBadRequest),
				Body:       "custom mantle error",
			},
			want: "Invalid Amazon Bedrock credentials: custom mantle error",
		},
		{
			name: "status passthrough",
			err: bedrockcommon.HTTPError{
				StatusCode: http.StatusBadRequest,
				Status:     http.StatusText(http.StatusBadRequest),
			},
			want: "Invalid Amazon Bedrock credentials: " + http.StatusText(http.StatusBadRequest),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := fmt.Errorf("failed to validate AWS credentials: %w", tt.err)
			if got := validationErrorMessage(err); got != tt.want {
				t.Fatalf("validationErrorMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidationErrorMessageForGenericError(t *testing.T) {
	const want = "Invalid Amazon Bedrock credentials."
	if got := validationErrorMessage(fmt.Errorf("network unavailable")); got != want {
		t.Fatalf("validationErrorMessage() = %q, want %q", got, want)
	}
}
