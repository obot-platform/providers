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
			name: "security token body",
			err: bedrockcommon.HTTPError{
				StatusCode: http.StatusUnauthorized,
				Status:     http.StatusText(http.StatusUnauthorized),
				Body:       "The security token included in the request is invalid.",
			},
			want: "Invalid Amazon Bedrock credentials: the access key ID or session token is invalid. If these are temporary credentials, include a valid session token.",
		},
		{
			name: "invalid token body",
			err: bedrockcommon.HTTPError{
				StatusCode: http.StatusBadRequest,
				Status:     http.StatusText(http.StatusBadRequest),
				Body:       "invalid token",
			},
			want: "Invalid Amazon Bedrock credentials: the access key ID or session token is invalid. If these are temporary credentials, include a valid session token.",
		},
		{
			name: "access denied",
			err: bedrockcommon.HTTPError{
				StatusCode: http.StatusForbidden,
				Status:     http.StatusText(http.StatusForbidden),
				Body:       "Access denied",
			},
			want: "Invalid Amazon Bedrock credentials: access was denied. Check that the credentials have Bedrock access and are configured for the selected region.",
		},
		{
			name: "forbidden status takes precedence over invalid token body",
			err: bedrockcommon.HTTPError{
				StatusCode: http.StatusForbidden,
				Status:     http.StatusText(http.StatusForbidden),
				Body:       "invalid token",
			},
			want: "Invalid Amazon Bedrock credentials: access was denied. Check that the credentials have Bedrock access and are configured for the selected region.",
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
