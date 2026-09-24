package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/obot-platform/providers/amazon-bedrock-model-provider/bedrockcommon"
)

func main() {
	isValidate := len(os.Args) > 1 && os.Args[1] == "validate"
	if err := mainErr(context.Background(), isValidate); err != nil {
		if isValidate {
			message := validationErrorMessage(err)
			log.Printf("ERROR Invalid Amazon Bedrock Credentials: %v", err)
			errorJSON := map[string]string{"error": message}
			if encErr := json.NewEncoder(os.Stdout).Encode(errorJSON); encErr != nil {
				log.Fatal(encErr)
			}
			os.Exit(1)
		} else {
			log.Fatal(err)
		}
	}
}

func mainErr(ctx context.Context, isValidate bool) error {
	apiKey := os.Getenv("OBOT_AMAZON_BEDROCK_API_KEY_MODEL_PROVIDER_API_KEY")
	if apiKey == "" {
		return errors.New("OBOT_AMAZON_BEDROCK_API_KEY_MODEL_PROVIDER_API_KEY not found")
	}

	region := os.Getenv("OBOT_AMAZON_BEDROCK_API_KEY_MODEL_PROVIDER_REGION")
	if region == "" {
		region = "us-east-1"
	}

	client := &http.Client{Transport: bedrockcommon.APIKeyTransport{APIKey: apiKey}}

	// Validate credentials by making the same lightweight Mantle call used for discovery.
	if _, err := bedrockcommon.ListMantleModels(ctx, client, region); err != nil {
		return fmt.Errorf("failed to validate AWS credentials: %w", err)
	}

	if isValidate {
		return nil
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "http://127.0.0.1:%s", port)
	})

	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		models, err := bedrockcommon.ListMantleModels(r.Context(), client, region)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to list models: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   models,
		}); err != nil {
			log.Printf("Failed to encode models response: %v", err)
		}
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("404 %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})

	listenHost := os.Getenv("OBOT_PROVIDER_LISTEN_HOST")
	if listenHost == "" {
		listenHost = "127.0.0.1"
	}

	addr := listenHost + ":" + port
	log.Printf("Starting server on %s", addr)
	return http.ListenAndServe(addr, mux)
}
