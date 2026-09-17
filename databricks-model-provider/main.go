package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

func userFacingErrorMessage(err error) string {
	if upstreamErr, ok := errors.AsType[*upstreamResponseError](err); ok {
		return upstreamErr.userMessage()
	}
	return err.Error()
}

func reportProviderError(stdout, stderr io.Writer, action string, err error) error {
	fmt.Fprintf(stderr, "failed to %s databricks-model-provider: %v\n", action, err)
	return json.NewEncoder(stdout).Encode(map[string]string{"error": userFacingErrorMessage(err)})
}

func validateProvider(ctx context.Context, cfg *config, stdout, stderr io.Writer) error {
	if _, err := cfg.listModels(ctx); err != nil {
		if reportErr := reportProviderError(stdout, stderr, "validate", err); reportErr != nil {
			fmt.Fprintf(stderr, "failed to report databricks-model-provider error: %v\n", reportErr)
		}
		return err
	}
	return nil
}

func main() {
	cfg, err := configFromEnv()
	if err != nil {
		if reportErr := reportProviderError(os.Stdout, os.Stderr, "configure", err); reportErr != nil {
			fmt.Fprintf(os.Stderr, "failed to report databricks-model-provider error: %v\n", reportErr)
		}
		os.Exit(1)
	}

	if len(os.Args) > 1 && os.Args[1] == "validate" {
		if err := validateProvider(context.Background(), cfg, os.Stdout, os.Stderr); err != nil {
			os.Exit(1)
		}
		return
	}

	if err := cfg.run(); err != nil {
		fmt.Printf("failed to run databricks-model-provider: %v\n", err)
		os.Exit(1)
	}
}
