#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")/.."

# Pure Go binaries can run without libc in the provider image or its consumer.
export CGO_ENABLED=${CGO_ENABLED:-0}

while IFS= read -r -d '' maingo; do
    if [ "$(basename "$(dirname "$maingo")")" = common ]; then
        continue
    fi
    (
        cd "$(dirname "$maingo")"
        echo "Building $PWD"
        go build -trimpath -ldflags="-s -w -buildid=" -o bin/obot-provider .
    )
done < <(find -L . -name main.go -not -path '*/vendor/*' -not -path '*/node_modules/*' -print0)
