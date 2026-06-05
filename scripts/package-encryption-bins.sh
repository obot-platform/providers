#!/bin/bash
set -e -x -o pipefail

BIN_DIR=${BIN_DIR:-./bin}

cd /obot-providers

if [ ! -e aws-encryption-provider ]; then
    git clone --depth=1 https://github.com/kubernetes-sigs/aws-encryption-provider
fi
cd /obot-providers/aws-encryption-provider
go build -o "${BIN_DIR}/aws-encryption-provider" cmd/server/main.go
OBOT_SERVER_VERSIONS="$(
    cat <<VERSIONS
github.com/kubernetes-sigs/aws-encryption-provider=$(git rev-parse --short HEAD),${OBOT_SERVER_VERSIONS}
VERSIONS
)"

cd /obot-providers

if [ ! -e kubernetes-kms ]; then
    git clone --depth=1 https://github.com/Azure/kubernetes-kms
fi
cd /obot-providers/kubernetes-kms
go build -ldflags="-s -w" -o "${BIN_DIR}/azure-encryption-provider" cmd/server/main.go
OBOT_SERVER_VERSIONS="$(
    cat <<VERSIONS
github.com/Azure/kubernetes-kms=$(git rev-parse --short HEAD),${OBOT_SERVER_VERSIONS}
VERSIONS
)"
OBOT_SERVER_VERSIONS="${OBOT_SERVER_VERSIONS%,}"

cd /obot-providers

if [ ! -e k8s-cloudkms-plugin ]; then
    git clone --depth=1 https://github.com/obot-platform/k8s-cloudkms-plugin
fi
cd /obot-providers/k8s-cloudkms-plugin
go build -ldflags "-s -w -extldflags 'static'" -installsuffix cgo -tags netgo -o "${BIN_DIR}/gcp-encryption-provider" cmd/k8s-cloudkms-plugin/main.go
OBOT_SERVER_VERSIONS="$(
    cat <<VERSIONS
github.com/obot-platform/k8s-cloudkms-plugin=$(git rev-parse --short HEAD),${OBOT_SERVER_VERSIONS}
VERSIONS
)"

cd /obot-providers
cat <<EOF >.envrc.providers.encryption-bins
export OBOT_SERVER_VERSIONS="${OBOT_SERVER_VERSIONS}"
EOF
