package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/obot-platform/providers/openai-model-provider/proxy"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

var log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})).With("tool", "gemini-vertex-model-provider")

func configure(ctx context.Context) (*google.Credentials, string, error) {
	// Ensure that we have some valid credentials JSON data
	credsJSON := os.Getenv("OBOT_GEMINI_VERTEX_MODEL_PROVIDER_GOOGLE_CREDENTIALS_JSON")
	if credsJSON == "" {
		return nil, "", fmt.Errorf("google application credentials content is required")
	}

	var creds map[string]any
	if err := json.Unmarshal([]byte(credsJSON), &creds); err != nil {
		return nil, "", fmt.Errorf("failed to parse google application credentials json: %w", err)
	}

	gcreds, err := google.CredentialsFromJSON(ctx, []byte(credsJSON), "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, "", fmt.Errorf("failed to parse google credentials JSON: %w", err)
	}

	// Ensure that we have a Project ID set
	var pid string
	if p, ok := creds["project_id"]; ok {
		pid = p.(string)
	} else {
		pid = os.Getenv("OBOT_GEMINI_VERTEX_MODEL_PROVIDER_GOOGLE_CLOUD_PROJECT")
	}
	if pid == "" {
		return nil, "", fmt.Errorf("google cloud project id is required")
	}
	gcreds.ProjectID = pid

	// Ensure that we have a Location set
	var loc string
	if l, ok := creds["location"]; ok {
		loc = l.(string)
	} else {
		loc = os.Getenv("OBOT_GEMINI_VERTEX_MODEL_PROVIDER_GOOGLE_CLOUD_LOCATION")
	}
	if loc == "" {
		return nil, "", fmt.Errorf("google cloud location is required")
	}

	return gcreds, loc, nil
}

func handleErr(err error) {
	if err != nil {
		fmt.Printf("{\"error\": \"%v\"}\n", err)
		log.Error("gemini-vertex-model-provider error", "error", err)
		os.Exit(1)
	}
}

func addUsageMetadata(models []map[string]any) []map[string]any {
	for _, m := range models {
		usage := "llm"
		if strings.Contains(m["id"].(string), "embedding") {
			usage = "text-embedding"
		}
		m["metadata"] = map[string]any{"usage": usage}
	}
	return models
}

func listModels(w http.ResponseWriter, r *http.Request) {
	content := map[string]any{
		"data": addUsageMetadata(
			[]map[string]any{
				// LLMs: https://cloud.google.com/vertex-ai/generative-ai/docs/model-reference/inference#supported-models
				{
					"id":   "google/gemini-2.0-flash",
					"name": "Gemini 2.0 Flash",
				},
				{
					"id":   "google/gemini-2.0-flash-lite-001",
					"name": "Gemini 2.0 Flash-Lite",
				},
				{
					"id":   "google/gemini-2.5-pro",
					"name": "Gemini 2.5 Pro",
				},
				{
					"id":   "google/gemini-2.5-flash",
					"name": "Gemini 2.5 Flash",
				},
				{
					"id":   "google/gemini-2.5-flash-preview-09-2025",
					"name": "Gemini 2.5 Flash Preview (09/2025)",
				},
				// Embedding Models: https://cloud.google.com/vertex-ai/generative-ai/docs/embeddings/get-text-embeddings#supported-models
				{
					"id":   "google/textembedding-gecko@001",
					"name": "Text Embedding Gecko (001) [EN]",
				},
				{
					"id":   "google/textembedding-gecko@003",
					"name": "Text Embedding Gecko (003) [EN]",
				},
				{
					"id":   "google/text-embedding-004",
					"name": "Text Embedding 004 [EN]",
				},
				{
					"id":   "google/text-embedding-005",
					"name": "Text Embedding 005 [EN]",
				},
				{
					"id":   "google/textembedding-gecko-multilingual@001",
					"name": "Text Embedding Gecko Multilingual (001)",
				},
				{
					"id":   "google/text-multilingual-embedding-002",
					"name": "Text Multilingual Embedding 002",
				},
			},
		),
	}
	if err := json.NewEncoder(w).Encode(content); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func main() {
	creds, location, err := configure(context.Background())
	handleErr(err)

	tokenSource := oauth2.ReuseTokenSourceWithExpiry(nil, creds.TokenSource, 5*time.Minute)

	token, err := tokenSource.Token()
	handleErr(err)

	if len(os.Args) > 1 && os.Args[1] == "validate" {
		log.Info("validate OK")
		os.Exit(0)
	}

	httpClient := &http.Client{
		Transport: &oauth2.Transport{
			Source: tokenSource, // takes care of refreshing the token
		},
	}

	// required, because the OpenAI API compatibility does not provide e.g. embeddings yet
	s := &server{
		httpClient: httpClient,
		location:   location,
		projectID:  creds.ProjectID,
	}

	// https://{LOCATION}-aiplatform.googleapis.com/v1beta1/projects/{PROJECT_ID}/locations/{LOCATION}/endpoints/openapi
	// see https://cloud.google.com/vertex-ai/generative-ai/docs/multimodal/call-vertex-using-openai-library#supported-gemini-models
	cfg := &proxy.Config{
		APIKey:     token.AccessToken,
		ListenPort: os.Getenv("PORT"),
		BaseURL:    fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/%s/endpoints/openapi", creds.ProjectID, location),
		Name:       "gemini-vertex",
		CustomPathHandleFuncs: map[string]http.HandlerFunc{
			"/v1/models":     listModels,
			"/v1/embeddings": s.embeddings,
		},
	}

	go func() {
		for {
			token, err := tokenSource.Token()
			handleErr(err)
			cfg.APIKey = token.AccessToken
			// sleep until 4 minutes before the token expires
			time.Sleep(time.Until(token.Expiry) - 5*time.Minute)
		}
	}()

	if err := proxy.Run(cfg); err != nil {
		panic(err)
	}
}
