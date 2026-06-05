#!/bin/bash
set -e -x -o pipefail

REPO=github.com/obot-platform/providers
REPO_DIR=/obot-providers/providers
REPO_NAME=$(basename $REPO_DIR)

if [[ -x "${REPO_DIR}/scripts/build.sh" ]]; then
    (
        echo "Running build script for ${REPO}..."
        cd "${REPO_DIR}"
        ./scripts/build.sh
        echo "Build script for ${REPO} complete!"
    )
else
    echo "No build script found in ${REPO}"
fi

OBOT_SERVER_VERSIONS="$(
    cat <<VERSIONS
${REPO}=$(cd /obot-providers/providers && git rev-parse --short HEAD),${OBOT_SERVER_VERSIONS}
VERSIONS
)"
OBOT_SERVER_VERSIONS="${OBOT_SERVER_VERSIONS%,}"

cd /obot-providers
cat <<EOF >.envrc.providers.${REPO_NAME}
export OBOT_SERVER_PROVIDER_REGISTRIES="/obot-providers/providers"
export OBOT_SERVER_VERSIONS="${OBOT_SERVER_VERSIONS}"
EOF
