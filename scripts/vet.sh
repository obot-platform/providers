#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")/.."

# Iterate modules rather than binaries so shared modules are covered too.
while IFS= read -r -d '' gomod; do
    (
        cd "$(dirname "$gomod")"
        echo "Vetting $PWD"
        go vet ./...
    )
done < <(find -L . -name go.mod -not -path '*/vendor/*' -not -path '*/node_modules/*' -print0)
