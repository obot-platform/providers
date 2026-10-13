# Providers
The home of official Obot providers

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

## Provider display text

Provider YAML references backend catalog messages in `name`, `description`, and
configuration parameters' `friendlyName` and `description`:

```yaml
name: "${google_auth_provider_name}"
description: "${google_auth_provider_description}"
```

Messages live in Obot's `pkg/i18n/messages/auth-providers/{locale}.json` or
`pkg/i18n/messages/model-providers/{locale}.json`. Use the YAML filename with
hyphens replaced by underscores as the key prefix. Parameter keys append the
lowercase configuration suffix and `friendly_name` or `description`.

Obot resolves references using `Accept-Language`, falling back to English for
missing or empty translations. Unknown keys retain their `${key}` reference.
Literal text remains valid; inline YAML `locales` maps are no longer supported.
Ship catalog additions in Obot together with updated provider registry images;
catalog changes require rebuilding Obot.

## Issues
Want to open an issue? Head over to the [Obot repo](https://github.com/obot-platform/obot/issues). Provider related issues will have the `providers` label.
