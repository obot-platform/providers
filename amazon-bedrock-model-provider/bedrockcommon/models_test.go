package bedrockcommon

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFilterMantleModels(t *testing.T) {
	models := filterMantleModels([]Model{
		{ID: "anthropic.claude", Object: "model", Metadata: map[string]string{"source": "mantle", "dialect": "wrong", "usage": "wrong"}},
		{ID: "openai.gpt", Object: "model"},
		{ID: "google.gemini", Object: "model"},
		{ID: "amazon.titan", Object: "model"},
		{ID: "meta.llama", Object: "model"},
	})

	if len(models) != 3 {
		t.Fatalf("len(models) = %d, want 3", len(models))
	}
	assertModel(t, models[0], "anthropic.claude", dialectAnthropicMessages)
	if models[0].Metadata["source"] != "mantle" {
		t.Fatalf("source metadata = %q, want preserved", models[0].Metadata["source"])
	}
	assertModel(t, models[1], "openai.gpt", dialectOpenAIResponses)
	assertModel(t, models[2], "google.gemini", dialectOpenAIResponses)
}

func TestMantleModelsURLDefaultRegion(t *testing.T) {
	if got, want := mantleModelsURL(""), "https://bedrock-mantle.us-east-1.api.aws/v1/models"; got != want {
		t.Fatalf("mantleModelsURL() = %q, want %q", got, want)
	}
}

func TestListMantleModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"anthropic.claude","object":"model"},{"id":"amazon.titan","object":"model"}]}`))
	}))
	defer server.Close()

	client := server.Client()
	client.Transport = rewriteHostTransport{target: server.URL, next: client.Transport}
	models, err := ListMantleModels(t.Context(), client, "us-west-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	assertModel(t, models[0], "anthropic.claude", dialectAnthropicMessages)
}

func TestListMantleModelsReturnsHTTPErrorForNon2xx(t *testing.T) {
	for _, status := range []int{http.StatusContinue, http.StatusMultipleChoices, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := &http.Client{Transport: responseRoundTripper{
				statusCode: status,
				status:     http.StatusText(status),
				body:       "mantle error",
			}}

			_, err := ListMantleModels(t.Context(), client, "us-east-1")
			if err == nil {
				t.Fatal("expected error")
			}
			var httpErr HTTPError
			if !errors.As(err, &httpErr) {
				t.Fatalf("error = %T, want HTTPError", err)
			}
			if httpErr.StatusCode != status {
				t.Fatalf("StatusCode = %d, want %d", httpErr.StatusCode, status)
			}
			if httpErr.Body != "mantle error" {
				t.Fatalf("Body = %q, want mantle error", httpErr.Body)
			}
		})
	}
}

func TestListMantleModelsReturnsReadErrorForNon2xxBody(t *testing.T) {
	client := &http.Client{Transport: responseRoundTripper{
		statusCode: http.StatusInternalServerError,
		status:     http.StatusText(http.StatusInternalServerError),
		readErr:    errors.New("read failed"),
	}}

	_, err := ListMantleModels(t.Context(), client, "us-east-1")
	if err == nil {
		t.Fatal("expected error")
	}
	var httpErr HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %T, want HTTPError", err)
	}
	if got, want := httpErr.Body, "failed to read error response body: read failed"; got != want {
		t.Fatalf("Body = %q, want %q", got, want)
	}
}

func TestWithDefaultTimeout(t *testing.T) {
	t.Run("nil client", func(t *testing.T) {
		client := withDefaultTimeout(nil)
		if client == nil {
			t.Fatal("client is nil")
		}
		if client.Timeout != defaultTimeout {
			t.Fatalf("Timeout = %s, want %s", client.Timeout, defaultTimeout)
		}
	})

	t.Run("zero timeout client is copied", func(t *testing.T) {
		transport := captureRoundTripper{}
		original := &http.Client{Transport: &transport}
		client := withDefaultTimeout(original)
		if client == original {
			t.Fatal("expected copied client")
		}
		if original.Timeout != 0 {
			t.Fatalf("original Timeout = %s, want 0", original.Timeout)
		}
		if client.Timeout != defaultTimeout {
			t.Fatalf("Timeout = %s, want %s", client.Timeout, defaultTimeout)
		}
		if client.Transport != original.Transport {
			t.Fatal("transport was not preserved")
		}
	})

	t.Run("custom timeout is preserved", func(t *testing.T) {
		original := &http.Client{Timeout: time.Second}
		client := withDefaultTimeout(original)
		if client != original {
			t.Fatal("expected original client")
		}
		if client.Timeout != time.Second {
			t.Fatalf("Timeout = %s, want %s", client.Timeout, time.Second)
		}
	})
}

func TestAPIKeyTransportSetsBearer(t *testing.T) {
	capture := &captureRoundTripper{}
	transport := APIKeyTransport{APIKey: "bedrock-key", Next: capture}
	req := httptest.NewRequest(http.MethodGet, "https://bedrock-mantle.us-east-1.api.aws/v1/models", nil)
	req.Header.Set("Authorization", "Bearer client-token")
	req.Header.Set("X-Api-Key", "client-key")

	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got := capture.req.Header.Get("Authorization"); got != "Bearer bedrock-key" {
		t.Fatalf("Authorization = %q, want Bedrock API key bearer", got)
	}
	if got := capture.req.Header.Get("X-Api-Key"); got != "" {
		t.Fatalf("X-Api-Key = %q, want empty", got)
	}
}

func TestSignRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://bedrock-mantle.us-east-1.api.aws/v1/models", nil)
	err := signRequest(req, StaticAuth{
		AccessKeyID:     "AKIDEXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
	}, time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); !strings.Contains(got, "AWS4-HMAC-SHA256") || !strings.Contains(got, "/us-east-1/bedrock/aws4_request") {
		t.Fatalf("Authorization = %q, want SigV4 default region/service", got)
	}
	if got := req.Header.Get("X-Amz-Content-Sha256"); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("X-Amz-Content-Sha256 = %q, want empty payload hash", got)
	}
}

func assertModel(t *testing.T, model Model, id, dialect string) {
	t.Helper()
	if model.ID != id {
		t.Fatalf("id = %q, want %q", model.ID, id)
	}
	if got := model.Metadata["dialect"]; got != dialect {
		t.Fatalf("dialect = %q, want %q", got, dialect)
	}
	if got := model.Metadata["usage"]; got != usageLLM {
		t.Fatalf("usage = %q, want %q", got, usageLLM)
	}
}

type captureRoundTripper struct {
	req *http.Request
}

func (c *captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.req = req
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

type responseRoundTripper struct {
	statusCode int
	status     string
	body       string
	readErr    error
}

func (r responseRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	body := io.ReadCloser(io.NopCloser(strings.NewReader(r.body)))
	if r.readErr != nil {
		body = errorReadCloser{err: r.readErr}
	}
	return &http.Response{
		StatusCode: r.statusCode,
		Status:     r.status,
		Body:       body,
	}, nil
}

type errorReadCloser struct {
	err error
}

func (e errorReadCloser) Read([]byte) (int, error) {
	return 0, e.err
}

func (e errorReadCloser) Close() error {
	return nil
}

type rewriteHostTransport struct {
	target string
	next   http.RoundTripper
}

func (r rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = strings.Split(r.target, "://")[0]
	req.URL.Host = strings.TrimPrefix(r.target, req.URL.Scheme+"://")
	return r.next.RoundTrip(req)
}
