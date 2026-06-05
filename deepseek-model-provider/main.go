package main

import (
	"fmt"
	"os"

	"github.com/obot-platform/providers/openai-model-provider/proxy"
)

func main() {
	apiKey := os.Getenv("OBOT_DEEPSEEK_MODEL_PROVIDER_API_KEY")
	if apiKey == "" {
		fmt.Println("OBOT_DEEPSEEK_MODEL_PROVIDER_API_KEY is not set, credential must be provided on a per-request basis")
	}

	cfg := &proxy.Config{
		APIKey:               apiKey,
		PersonalAPIKeyHeader: "X-Obot-OBOT_DEEPSEEK_MODEL_PROVIDER_API_KEY",
		ListenPort:           os.Getenv("PORT"),
		BaseURL:              "https://api.deepseek.com/v1",
		RewriteModelsFn:      proxy.RewriteAllModelsWithUsage("llm"),
		Name:                 "DeepSeek",
	}

	if err := cfg.Validate(); err != nil {
		os.Exit(1)
	}

	if len(os.Args) > 1 && os.Args[1] == "validate" {
		return
	}

	if err := proxy.Run(cfg); err != nil {
		panic(err)
	}
}
