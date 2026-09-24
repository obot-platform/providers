package main

import (
	"errors"
	"net/http"
	"strings"

	"github.com/obot-platform/providers/amazon-bedrock-model-provider/bedrockcommon"
)

func validationErrorMessage(err error) string {
	if httpErr, ok := errors.AsType[bedrockcommon.HTTPError](err); ok {
		messageLower := strings.ToLower(httpErr.Body)
		switch {
		case httpErr.StatusCode == http.StatusForbidden:
			return "Invalid Amazon Bedrock credentials: access was denied. Check that the credentials have Bedrock access and are configured for the selected region."
		case httpErr.StatusCode == http.StatusUnauthorized:
			fallthrough
		case strings.Contains(messageLower, "security token"):
			fallthrough
		case strings.Contains(messageLower, "invalid token"):
			return "Invalid Amazon Bedrock credentials: the access key ID or session token is invalid. If these are temporary credentials, include a valid session token."
		case strings.Contains(messageLower, "access denied"):
			fallthrough
		case strings.Contains(messageLower, "not authorized"):
			return "Invalid Amazon Bedrock credentials: access was denied. Check that the credentials have Bedrock access and are configured for the selected region."
		case httpErr.Body != "":
			return "Invalid Amazon Bedrock credentials: " + httpErr.Body
		}
		return "Invalid Amazon Bedrock credentials: " + httpErr.Status
	}
	return "Invalid Amazon Bedrock credentials."
}
