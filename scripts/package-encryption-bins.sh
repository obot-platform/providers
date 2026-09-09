#!/bin/bash
set -e -x -o pipefail

BIN_DIR=${BIN_DIR:-./bin}
export CGO_ENABLED=${CGO_ENABLED:-0}

cd /obot-providers

if [ ! -e aws-encryption-provider ]; then
    git clone --depth=1 https://github.com/obot-platform/aws-encryption-provider
fi
cd /obot-providers/aws-encryption-provider
go build -trimpath -ldflags="-s -w -buildid=" -o "${BIN_DIR}/aws-encryption-provider" cmd/server/main.go
OBOT_SERVER_VERSIONS="$(
    cat <<VERSIONS
github.com/obot-platform/aws-encryption-provider=$(git rev-parse --short HEAD),${OBOT_SERVER_VERSIONS}
VERSIONS
)"

cd /obot-providers

if [ ! -e kubernetes-kms ]; then
    git clone --depth=1 https://github.com/obot-platform/kubernetes-kms
fi
cd /obot-providers/kubernetes-kms
go build -trimpath -ldflags="-s -w -buildid=" -o "${BIN_DIR}/azure-encryption-provider" cmd/server/main.go
OBOT_SERVER_VERSIONS="$(
    cat <<VERSIONS
github.com/obot-platform/kubernetes-kms=$(git rev-parse --short HEAD),${OBOT_SERVER_VERSIONS}
VERSIONS
)"
OBOT_SERVER_VERSIONS="${OBOT_SERVER_VERSIONS%,}"

cd /obot-providers

if [ ! -e k8s-cloudkms-plugin ]; then
    git clone --depth=1 https://github.com/obot-platform/k8s-cloudkms-plugin
fi
cd /obot-providers/k8s-cloudkms-plugin
go build -trimpath -ldflags="-s -w -buildid=" -o "${BIN_DIR}/gcp-encryption-provider" cmd/k8s-cloudkms-plugin/main.go
OBOT_SERVER_VERSIONS="$(
    cat <<VERSIONS
github.com/obot-platform/k8s-cloudkms-plugin=$(git rev-parse --short HEAD),${OBOT_SERVER_VERSIONS}
VERSIONS
)"

cd /obot-providers
cat <<EOF >.envrc.providers.encryption-bins
export OBOT_SERVER_VERSIONS="${OBOT_SERVER_VERSIONS}"
EOF
