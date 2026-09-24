# Providers
The home of official Obot providers, including enterprise-gated authentication
and model providers.

Each provider is built as a standalone binary and packaged into the unified
`ghcr.io/obot-platform/providers` image.

`make build` produces static Go binaries with source paths, debug symbols, and
linker build IDs removed. Set `CGO_ENABLED=1` if a local build requires cgo.

The `providers` and `encryption-bins` Docker targets package UPX-compressed
binaries in `scratch` images. Obot consumes the existing `/obot-providers` and
`/bin/*-encryption-provider` paths with `COPY --from`; the images have no shell or
default command. A CA certificate bundle and writable `/tmp` are included for
running a binary directly with an explicit `--entrypoint`.

Compression minimizes executable size at the cost of build time and startup
decompression. To retain ordinary ELF binaries for debugging, binary inspection,
or environments that restrict executable unpacking, disable compression:

```sh
docker build --target providers --build-arg COMPRESS_BINARIES=false -t providers:uncompressed .
docker build --target encryption-bins --build-arg COMPRESS_BINARIES=false -t encryption-bins:uncompressed .
```

Docker builds cross-compile for the target platform, including `linux/amd64` and
`linux/arm64`, without running target binaries during the build. Local builds
remain uncompressed.

## Issues
Want to open an issue? Head over to the [Obot repo](https://github.com/obot-platform/obot/issues). Provider related issues will have the `providers` label.
