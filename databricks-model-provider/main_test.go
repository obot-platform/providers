package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestValidateProviderReportsDatabricksMessage(t *testing.T) {
	t.Parallel()

	const message = "Credential was not sent or was of an unsupported type for this API."
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error_code":401,"message":"` + message + `"}`))
	}))
	t.Cleanup(upstream.Close)
	workspaceURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config{workspaceURL: workspaceURL, token: "invalid", client: upstream.Client()}

	var stdout, stderr bytes.Buffer
	err = validateProvider(context.Background(), cfg, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected validation error")
	}

	var output map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("stdout is not JSON: %v: %q", err, stdout.String())
	}
	if got := output["error"]; got != message {
		t.Fatalf("error = %q, want %q", got, message)
	}
	if got := stderr.String(); !strings.Contains(got, "list serving endpoints: status 401: "+message) {
		t.Fatalf("stderr = %q, want detailed upstream error", got)
	}
}

func TestReportProviderErrorFallsBackToError(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if err := reportProviderError(&stdout, &stderr, "configure", errors.New("workspace URL is required")); err != nil {
		t.Fatal(err)
	}

	if got, want := stdout.String(), "{\"error\":\"workspace URL is required\"}\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}
