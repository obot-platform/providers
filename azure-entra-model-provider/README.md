# Azure OpenAI (Entra ID) Model Provider

Model provider for Azure OpenAI using Entra ID service principal authentication. Deployments are discovered automatically from the Azure Management API.

## Setup

### 1. Create a service principal

```bash
az ad sp create-for-rbac --name "<sp-name>" \
  --role "Cognitive Services OpenAI User" \
  --scopes /subscriptions/<subscription-id>/resourceGroups/<resource-group>/providers/Microsoft.CognitiveServices/accounts/<account-name>
```

This outputs the `appId` (client ID), `password` (client secret), and `tenant` (tenant ID) needed below.

### 2. Find your resource details

```bash
az cognitiveservices account show \
  --name <account-name> \
  --resource-group <resource-group> \
  --query "{endpoint:properties.endpoint, id:id}"
```

## Configuration

### Required

| Variable | Description | Example |
|---|---|---|
| `OBOT_AZURE_ENTRA_MODEL_PROVIDER_ENDPOINT` | Azure OpenAI endpoint URL | `https://my-resource.openai.azure.com` |
| `OBOT_AZURE_ENTRA_MODEL_PROVIDER_CLIENT_ID` | Entra ID application (client) ID | `xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx` |
| `OBOT_AZURE_ENTRA_MODEL_PROVIDER_CLIENT_SECRET` | Entra ID client secret | |
| `OBOT_AZURE_ENTRA_MODEL_PROVIDER_TENANT_ID` | Entra ID tenant ID | `xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx` |
| `OBOT_AZURE_ENTRA_MODEL_PROVIDER_SUBSCRIPTION_ID` | Azure subscription ID | `xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx` |
| `OBOT_AZURE_ENTRA_MODEL_PROVIDER_RESOURCE_GROUP` | Resource group containing the account | `my-resource-group` |
| `OBOT_AZURE_ENTRA_MODEL_PROVIDER_RESOURCE_NAME` | Cognitive Services account name | `my-openai-account` |

## How deployment discovery works

On startup, the provider calls the Azure Management API to list deployments in the configured Cognitive Services account:

```
GET https://management.azure.com/subscriptions/{subscriptionId}/resourceGroups/{resourceGroup}/providers/Microsoft.CognitiveServices/accounts/{accountName}/deployments?api-version=2024-10-01
```

The service principal requires at minimum the `Cognitive Services OpenAI User` role on the account to read deployments. Each deployment's deployment name becomes the model ID exposed to Obot.
