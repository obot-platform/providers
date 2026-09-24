package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/obot-platform/providers/azure-model-provider/azurecommon"
)

func main() {
	isValidate := len(os.Args) > 1 && os.Args[1] == "validate"
	if err := mainErr(context.Background(), isValidate); err != nil {
		if isValidate {
			message := validationErrorMessage(err)
			log.Printf("ERROR Invalid Azure Entra ID Credentials: %v", err)
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
	endpoint := os.Getenv("OBOT_AZURE_ENTRA_MODEL_PROVIDER_ENDPOINT")
	if endpoint == "" {
		return errors.New("OBOT_AZURE_ENTRA_MODEL_PROVIDER_ENDPOINT not found")
	}
	endpoint = strings.TrimRight(endpoint, "/")
	if err := azurecommon.ValidateEndpoint(endpoint); err != nil {
		return fmt.Errorf("invalid OBOT_AZURE_ENTRA_MODEL_PROVIDER_ENDPOINT: %w", err)
	}

	clientID := os.Getenv("OBOT_AZURE_ENTRA_MODEL_PROVIDER_CLIENT_ID")
	if clientID == "" {
		return errors.New("OBOT_AZURE_ENTRA_MODEL_PROVIDER_CLIENT_ID not found")
	}

	clientSecret := os.Getenv("OBOT_AZURE_ENTRA_MODEL_PROVIDER_CLIENT_SECRET")
	if clientSecret == "" {
		return errors.New("OBOT_AZURE_ENTRA_MODEL_PROVIDER_CLIENT_SECRET not found")
	}

	tenantID := os.Getenv("OBOT_AZURE_ENTRA_MODEL_PROVIDER_TENANT_ID")
	if tenantID == "" {
		return errors.New("OBOT_AZURE_ENTRA_MODEL_PROVIDER_TENANT_ID not found")
	}

	subscriptionID := os.Getenv("OBOT_AZURE_ENTRA_MODEL_PROVIDER_SUBSCRIPTION_ID")
	if subscriptionID == "" {
		return errors.New("OBOT_AZURE_ENTRA_MODEL_PROVIDER_SUBSCRIPTION_ID not found")
	}

	resourceGroup := os.Getenv("OBOT_AZURE_ENTRA_MODEL_PROVIDER_RESOURCE_GROUP")
	if resourceGroup == "" {
		return errors.New("OBOT_AZURE_ENTRA_MODEL_PROVIDER_RESOURCE_GROUP not found")
	}

	resourceName := os.Getenv("OBOT_AZURE_ENTRA_MODEL_PROVIDER_RESOURCE_NAME")
	if resourceName == "" {
		return errors.New("OBOT_AZURE_ENTRA_MODEL_PROVIDER_RESOURCE_NAME not found")
	}

	if _, err := fetchDeploymentsFromManagement(ctx, subscriptionID, resourceGroup, resourceName, tenantID, clientID, clientSecret); err != nil {
		return fmt.Errorf("failed to fetch deployments from Azure: %w", err)
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
		deployments, err := fetchDeploymentsFromManagement(r.Context(), subscriptionID, resourceGroup, resourceName, tenantID, clientID, clientSecret)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   azurecommon.BuildModelsFromDeployments(deployments),
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
