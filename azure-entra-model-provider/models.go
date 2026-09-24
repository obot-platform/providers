package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/obot-platform/providers/azure-model-provider/azurecommon"
)

type managementDeploymentsResponse struct {
	Value []managementDeployment `json:"value"`
}

type managementDeployment struct {
	DeploymentName string                         `json:"name"`
	Properties     managementDeploymentProperties `json:"properties"`
}

type managementDeploymentProperties struct {
	Model managementDeploymentModel `json:"model"`
}

type managementDeploymentModel struct {
	ProviderFormat string `json:"format"`
	ModelName      string `json:"name"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
	ErrorURI    string `json:"error_uri"`
}

// fetchDeploymentsFromManagement lists actual deployments using the Azure Management API.
func fetchDeploymentsFromManagement(ctx context.Context, subscriptionID, resourceGroup, resourceName, tenantID, clientID, clientSecret string) (map[string]azurecommon.Deployment, error) {
	token, err := getEntraTokenForScope(ctx, tenantID, clientID, clientSecret, "https://management.azure.com/.default")
	if err != nil {
		return nil, fmt.Errorf("failed to get management token: %w", err)
	}

	mgmtURL := fmt.Sprintf(
		"https://management.azure.com/subscriptions/%s/resourceGroups/%s/providers/Microsoft.CognitiveServices/accounts/%s/deployments?api-version=2024-10-01",
		url.PathEscape(subscriptionID),
		url.PathEscape(resourceGroup),
		url.PathEscape(resourceName),
	)

	return fetchDeploymentsFromManagementURL(ctx, mgmtURL, token)
}

// fetchDeploymentsFromManagementURL fetches and parses deployments from a management API URL.
func fetchDeploymentsFromManagementURL(ctx context.Context, mgmtURL, token string) (map[string]azurecommon.Deployment, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mgmtURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build management request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("management request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read management response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, parseManagementRequestError(resp.StatusCode, body)
	}

	var result managementDeploymentsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to decode management response: %w", err)
	}

	deployments := make(map[string]azurecommon.Deployment, len(result.Value))
	for _, d := range result.Value {
		dialect, ok := azurecommon.DialectForModelFormat(d.Properties.Model.ProviderFormat)
		if !ok {
			continue
		}
		usageName := d.Properties.Model.ModelName
		if usageName == "" {
			usageName = d.DeploymentName
		}
		deployments[d.DeploymentName] = azurecommon.Deployment{
			Usage:   azurecommon.DeploymentUsageType(usageName),
			Dialect: dialect,
		}
	}
	return deployments, nil
}

// getEntraTokenForScope acquires an OAuth2 access token via the client-credentials flow.
func getEntraTokenForScope(ctx context.Context, tenantID, clientID, clientSecret, scope string) (string, error) {
	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenantID)

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("scope", scope)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode/100 != 2 {
		return "", parseTokenRequestError(resp.StatusCode, body)
	}

	var result tokenResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to decode token response: %w", err)
	}
	if result.Error != "" {
		return "", &tokenRequestError{StatusCode: resp.StatusCode, Code: result.Error, Description: result.ErrorDesc, URI: result.ErrorURI}
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("token response missing access_token")
	}
	return result.AccessToken, nil
}
