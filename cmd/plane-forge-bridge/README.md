# `cmd/plane-forge-bridge`

The bridge binary. Composition root — parses flags, loads config, builds the logger, instantiates `internal/server.Server`, and runs it under a signal-cancelled context.

## Usage

```sh
plane-forge-bridge --config /etc/pfb/config.yaml
plane-forge-bridge --version
```

Flags:

| Flag        | Env          | Default        | Description                                     |
| ----------- | ------------ | -------------- | ----------------------------------------------- |
| `--config`  | `PFB_CONFIG` | `config.yaml`  | Path to the YAML config (see `../../config.example.yaml`). |
| `--version` | —            | —              | Print build version and exit.                   |

## Build

```sh
make build
./bin/plane-forge-bridge --version

# or directly:
go build -trimpath -ldflags="-s -w -X main.version=$(git describe --tags --always)" \
  -o bin/plane-forge-bridge ./cmd/plane-forge-bridge
```

## Signals

SIGINT or SIGTERM triggers a graceful shutdown — the HTTP server stops accepting new connections and waits up to 15 s for in-flight handlers to finish before exiting.

## Logging

JSON via `log/slog`. Level is taken from `log_level` in the config (one of `debug`, `info`, `warn`, `error`). Unknown values fall back to `info`.
