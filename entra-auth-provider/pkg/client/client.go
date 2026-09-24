package client

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	msgraphsdkgo "github.com/microsoftgraph/msgraph-sdk-go"
)

type StaticTokenCredential struct {
	token string
}

func (s StaticTokenCredential) GetToken(_ context.Context, options policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: s.token}, nil
}

func NewStaticClient(token string) (*msgraphsdkgo.GraphServiceClient, error) {
	return msgraphsdkgo.NewGraphServiceClientWithCredentials(StaticTokenCredential{
		token: token,
	}, nil)
}

func NewClient(tenantID, clientID, clientSecret string) (*msgraphsdkgo.GraphServiceClient, error) {
	cred, err := azidentity.NewClientSecretCredential(tenantID, clientID, clientSecret, nil)
	if err != nil {
		return nil, err
	}

	client, err := msgraphsdkgo.NewGraphServiceClientWithCredentials(cred, []string{"https://graph.microsoft.com/.default"})
	if err != nil {
		return nil, err
	}

	return client, nil
}
