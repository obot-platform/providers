module github.com/obot-platform/providers/amazon-bedrock-api-key-model-provider

go 1.26.4

require github.com/obot-platform/providers/amazon-bedrock-model-provider v0.0.0-00010101000000-000000000000

require (
	github.com/aws/aws-sdk-go-v2 v1.41.6 // indirect
	github.com/aws/smithy-go v1.25.0 // indirect
)

replace github.com/obot-platform/providers/amazon-bedrock-model-provider => ../amazon-bedrock-model-provider
