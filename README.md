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

Provider YAML keeps English in `name`, `description`, and each configuration
parameter's `friendlyName` and `description`. Add `locales` beside those
fields for `ja`, `ko`, and `zh-CN`:

```yaml
locales:
  ja:
    name: "表示名"
    description: "説明"
```

For a configuration parameter, use `friendlyName` and `description` under each
locale instead of `name` and `description`. Obot selects the display text from
`Accept-Language` and uses the English field when a translation is absent.

## Issues
Want to open an issue? Head over to the [Obot repo](https://github.com/obot-platform/obot/issues). Provider related issues will have the `providers` label.
