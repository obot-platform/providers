package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

type azureAPIErrorResponse struct {
	Error azureAPIErrorDetail `json:"error"`
}

type azureAPIErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type azureAPIError struct {
	StatusCode int
	Code       string
	Message    string
}

type configurationError struct {
	err error
}

func (e *configurationError) Error() string {
	return e.err.Error()
}

func (e *configurationError) Unwrap() error {
	return e.err
}

func (e *azureAPIError) Error() string {
	switch {
	case e.Message != "" && e.Code != "":
		return fmt.Sprintf("Azure API returned %d (%s): %s", e.StatusCode, e.Code, e.Message)
	case e.Message != "":
		return fmt.Sprintf("Azure API returned %d: %s", e.StatusCode, e.Message)
	case e.Code != "":
		return fmt.Sprintf("Azure API returned %d (%s)", e.StatusCode, e.Code)
	default:
		return fmt.Sprintf("Azure API returned %d", e.StatusCode)
	}
}

func validationErrorMessage(err error) string {
	if configErr, ok := errors.AsType[*configurationError](err); ok {
		return configErr.Error()
	}

	if apiErr, ok := errors.AsType[*azureAPIError](err); ok {
		switch {
		case apiErr.StatusCode == http.StatusUnauthorized || apiErr.Code == "401":
			return "Invalid Azure credentials: the API key is invalid or does not belong to the configured endpoint. Check the API key and endpoint."
		}
		if apiErr.Message != "" {
			return "Invalid Azure credentials: " + apiErr.Message
		}
		return "Invalid Azure credentials."
	}

	if dnsErr, ok := errors.AsType[*net.DNSError](err); ok {
		if dnsErr.Name != "" {
			return "Invalid Azure endpoint: host " + dnsErr.Name + " was not found. Check the endpoint URL."
		}
		return "Invalid Azure endpoint: host was not found. Check the endpoint URL."
	}

	return "Invalid Azure credentials."
}

func parseAzureAPIError(statusCode int, body []byte) error {
	var result azureAPIErrorResponse
	if err := json.Unmarshal(body, &result); err == nil && result.Error != (azureAPIErrorDetail{}) {
		return &azureAPIError{
			StatusCode: statusCode,
			Code:       result.Error.Code,
			Message:    result.Error.Message,
		}
	}
	return fmt.Errorf("Azure API returned %d: %s", statusCode, strings.TrimSpace(string(body)))
}
