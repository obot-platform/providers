# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM cgr.dev/chainguard/wolfi-base AS base

RUN apk upgrade --no-cache && apk add --no-cache bash go-1.27 make git ca-certificates upx

FROM base AS runtime-files
RUN mkdir -p /runtime/etc/ssl/certs /runtime/tmp && \
    chmod 1777 /runtime/tmp && \
    cp /etc/ssl/certs/ca-certificates.crt /runtime/etc/ssl/certs/

FROM scratch AS runtime
ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
COPY --from=runtime-files /runtime/ /

FROM base AS providers-builder
WORKDIR /obot-providers/providers
COPY . /obot-providers/providers
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/root/go/pkg/mod \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} make package-providers && \
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

# Compress only the packaged copies so the builder retains ordinary ELF binaries.
FROM providers-builder AS providers-package
ARG COMPRESS_BINARIES=true
RUN case "$COMPRESS_BINARIES" in \
        true) upx --best --lzma /providers-runtime/obot-providers/providers/*-provider/bin/obot-provider && \
            upx --test /providers-runtime/obot-providers/providers/*-provider/bin/obot-provider ;; \
        false) ;; \
        *) echo 'COMPRESS_BINARIES must be true or false' >&2; exit 1 ;; \
    esac

FROM runtime AS providers
WORKDIR /obot-providers/providers
COPY --from=providers-package /providers-runtime/obot-providers/ /obot-providers/

FROM base AS encryption-bins-builder
WORKDIR /obot-providers
COPY ./Makefile /obot-providers/
COPY ./scripts/package-encryption-bins.sh /obot-providers/scripts/

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/root/go/pkg/mod \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} BIN_DIR=/obot-providers/bin make package-encryption-bins && \
    mkdir -p /encryption-bins-runtime/bin /encryption-bins-runtime/obot-providers && \
    cp -a /obot-providers/.envrc.providers.encryption-bins /encryption-bins-runtime/obot-providers/ && \
    cp -a /obot-providers/bin/aws-encryption-provider /encryption-bins-runtime/bin/ && \
    cp -a /obot-providers/bin/azure-encryption-provider /encryption-bins-runtime/bin/ && \
    cp -a /obot-providers/bin/gcp-encryption-provider /encryption-bins-runtime/bin/

FROM encryption-bins-builder AS encryption-bins-package
ARG COMPRESS_BINARIES=true
RUN case "$COMPRESS_BINARIES" in \
        true) upx --best --lzma /encryption-bins-runtime/bin/*-encryption-provider && \
            upx --test /encryption-bins-runtime/bin/*-encryption-provider ;; \
        false) ;; \
        *) echo 'COMPRESS_BINARIES must be true or false' >&2; exit 1 ;; \
    esac

FROM runtime AS encryption-bins
WORKDIR /obot-providers
COPY --from=encryption-bins-package /encryption-bins-runtime/bin/ /bin/
COPY --from=encryption-bins-package /encryption-bins-runtime/obot-providers/ /obot-providers/
