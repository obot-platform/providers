# syntax=docker/dockerfile:1
FROM cgr.dev/chainguard/wolfi-base AS base

RUN apk upgrade --no-cache && apk add --no-cache go-1.26 make git curl

FROM base AS providers-builder
WORKDIR /obot-providers/providers
COPY . /obot-providers/providers
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/root/go/pkg/mod \
    BIN_DIR=/bin make package-providers && \
    mkdir -p /providers-runtime/obot-providers/providers && \
    cp -a /obot-providers/.envrc.providers.providers /providers-runtime/obot-providers/ && \
    cp -a /obot-providers/providers/auth-providers /providers-runtime/obot-providers/providers/ && \
    cp -a /obot-providers/providers/model-providers /providers-runtime/obot-providers/providers/ && \
    for bin_dir in /obot-providers/providers/*-provider/bin; do \
        provider_dir="$(dirname "${bin_dir}")"; \
        dest="/providers-runtime/obot-providers/providers/$(basename "${provider_dir}")"; \
        mkdir -p "${dest}"; \
        cp -a "${bin_dir}" "${dest}/"; \
    done

FROM base AS providers
WORKDIR /obot-providers/providers
COPY --from=providers-builder /providers-runtime/obot-providers/ /obot-providers/

FROM base AS encryption-bins-builder
WORKDIR /obot-providers
COPY ./Makefile /obot-providers/
COPY ./scripts/package-encryption-bins.sh /obot-providers/scripts/

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/root/go/pkg/mod \
    BIN_DIR=/obot-providers/bin make package-encryption-bins && \
    mkdir -p /encryption-bins-runtime/bin /encryption-bins-runtime/obot-providers && \
    cp -a /obot-providers/.envrc.providers.encryption-bins /encryption-bins-runtime/obot-providers/ && \
    cp -a /obot-providers/bin/aws-encryption-provider /encryption-bins-runtime/bin/ && \
    cp -a /obot-providers/bin/azure-encryption-provider /encryption-bins-runtime/bin/ && \
    cp -a /obot-providers/bin/gcp-encryption-provider /encryption-bins-runtime/bin/

FROM base AS encryption-bins
WORKDIR /obot-providers
COPY --from=encryption-bins-builder /encryption-bins-runtime/bin/ /bin/
COPY --from=encryption-bins-builder /encryption-bins-runtime/obot-providers/ /obot-providers/
