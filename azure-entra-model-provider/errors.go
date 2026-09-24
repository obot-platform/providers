package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type managementErrorResponse struct {
	Error managementError `json:"error"`
}

type managementError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type managementRequestError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *managementRequestError) Error() string {
	switch {
	case e.Message != "" && e.Code != "":
		return fmt.Sprintf("management API returned %d (%s): %s", e.StatusCode, e.Code, e.Message)
	case e.Message != "":
		return fmt.Sprintf("management API returned %d: %s", e.StatusCode, e.Message)
	case e.Code != "":
		return fmt.Sprintf("management API returned %d (%s)", e.StatusCode, e.Code)
	default:
		return fmt.Sprintf("management API returned %d", e.StatusCode)
	}
}

func parseManagementRequestError(statusCode int, body []byte) error {
	var result managementErrorResponse
	if err := json.Unmarshal(body, &result); err == nil && result.Error != (managementError{}) {
		return &managementRequestError{
			StatusCode: statusCode,
			Code:       result.Error.Code,
			Message:    result.Error.Message,
		}
	}
	return fmt.Errorf("management API returned %d: %s", statusCode, strings.TrimSpace(string(body)))
}

type tokenRequestError struct {
	StatusCode  int
	Code        string
	Description string
	URI         string
}

func (e *tokenRequestError) Error() string {
	switch {
	case e.Description != "" && e.Code != "":
		return fmt.Sprintf("token request failed with status %d (%s): %s", e.StatusCode, e.Code, e.Description)
	case e.Description != "":
		return fmt.Sprintf("token request failed with status %d: %s", e.StatusCode, e.Description)
	case e.Code != "":
		return fmt.Sprintf("token request failed with status %d (%s)", e.StatusCode, e.Code)
	default:
		return fmt.Sprintf("token request failed with status %d", e.StatusCode)
	}
}

func parseTokenRequestError(statusCode int, body []byte) error {
	var result tokenResponse
	if err := json.Unmarshal(body, &result); err == nil && (result.Error != "" || result.ErrorDesc != "") {
		return &tokenRequestError{
			StatusCode:  statusCode,
			Code:        result.Error,
			Description: result.ErrorDesc,
			URI:         result.ErrorURI,
		}
	}
	return fmt.Errorf("token request failed with status %d: %s", statusCode, strings.TrimSpace(string(body)))
}

func validationErrorMessage(err error) string {
	if tokenErr, ok := errors.AsType[*tokenRequestError](err); ok {
		var message string
		switch {
		case tokenErr.Code == "AADSTS700016" || strings.Contains(tokenErr.Description, "AADSTS700016"):
			message = "Invalid Azure Entra ID credentials: the client ID was not found in the tenant. Check the client ID and tenant ID."
		case tokenErr.Code == "AADSTS7000215" || strings.Contains(tokenErr.Description, "AADSTS7000215"):
			message = "Invalid Azure Entra ID credentials: the client secret is invalid. Use the secret value, not the secret ID."
		case tokenErr.Code == "AADSTS900023" || strings.Contains(tokenErr.Description, "AADSTS900023"):
			message = "Invalid Azure Entra ID credentials: the tenant ID is not valid."
		case tokenErr.Description != "":
			message = "Invalid Azure Entra ID credentials: " + stripAzureRequestDetails(tokenErr.Description)
		default:
			message = "Invalid Azure Entra ID credentials."
		}
		return appendAzureErrorURI(message, tokenErr.URI)
	}

	if managementErr, ok := errors.AsType[*managementRequestError](err); ok {
		switch managementErr.Code {
		case "InvalidSubscriptionId":
			return "Invalid Azure configuration: the subscription ID is malformed or invalid."
		case "AuthorizationFailed":
			return "Invalid Azure configuration: the app does not have access to read deployments for the configured resource group and resource name. Check the resource group, resource name, and app permissions."
		}
		if managementErr.Message != "" {
			return "Invalid Azure configuration: " + managementErr.Message
		}
		return "Invalid Azure configuration."
	}
	return "Invalid Azure Entra ID credentials."
}

func appendAzureErrorURI(message, uri string) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return message
	}
	return message + " More details: " + uri
}

func stripAzureRequestDetails(message string) string {
	for _, marker := range []string{" Trace ID:", " Correlation ID:", " Timestamp:"} {
		if before, _, ok := strings.Cut(message, marker); ok {
			return strings.TrimSpace(before)
		}
	}
	return strings.TrimSpace(message)
}
