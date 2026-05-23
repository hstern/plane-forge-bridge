# internal/mapping

Configuration loader for plane-forge-bridge.

## Purpose

Reads the YAML config file, applies a small set of `PFB_*` environment
overrides, validates the result, and resolves secrets from the
environment. The output is a `*Resolved` value ready for the rest of the
program to use.

The YAML file is the source of truth for layout. Secrets are *not* stored
in the file — the file names the environment variable that holds each
secret, and the loader reads it at startup. See
[token_env indirection](#token_env-indirection) below.

## Config shape

| Field                          | Type              | Env override                       |
|--------------------------------|-------------------|------------------------------------|
| `listen`                       | string `host:port`| `PFB_LISTEN`                       |
| `log_level`                    | enum              | `PFB_LOG_LEVEL`                    |
| `forge.base_url`               | absolute URL      | `PFB_FORGE_BASE_URL`               |
| `forge.token_env`              | env var name      | `PFB_FORGE_TOKEN_ENV`              |
| `forge.webhook_secret_env`     | env var name      | `PFB_FORGE_WEBHOOK_SECRET_ENV`     |
| `plane.base_url`               | absolute URL      | `PFB_PLANE_BASE_URL`               |
| `plane.workspace_slug`         | string            | `PFB_PLANE_WORKSPACE_SLUG`         |
| `plane.api_key_env`            | env var name      | `PFB_PLANE_API_KEY_ENV`            |
| `plane.webhook_secret_env`     | env var name      | `PFB_PLANE_WEBHOOK_SECRET_ENV`     |
| `bridge_bot.forge_username`    | string            | (none — YAML only)                 |
| `bridge_bot.plane_member_id`   | UUID              | (none — YAML only)                 |
| `links[].forge_repo`           | `owner/repo`      | (none — YAML only)                 |
| `links[].plane_project_id`     | UUID              | (none — YAML only)                 |
| `links[].project_identifier`   | short uppercase ID (optional) | (none — YAML only)     |
| `links[].state_map`            | map[string]string | (none — YAML only)                 |
| `links[].pr_state_map`         | map[string]string (optional) | (none — YAML only)      |
| `users`                        | map[string]UUID   | (none — YAML only)                 |
| `idemp.lru_capacity`           | positive int      | (none — YAML only)                 |

`enum` log levels are `debug`, `info`, `warn`, `error`. `log_level`
defaults to `info` and `idemp.lru_capacity` defaults to `4096`.

Non-secret, structural fields (`users`, `links`, `bridge_bot`) live only
in YAML — there is no env override for them. They tend to change with
code review, not deployment.

## `token_env` indirection

The YAML doesn't hold tokens; it holds the *name* of the environment
variable that does. So `forge.token_env: PFB_FORGE_TOKEN` tells the
loader "read `$PFB_FORGE_TOKEN` at startup and use that as the API
token". The `PFB_FORGE_TOKEN_ENV` override replaces the *name* — it does
not replace the token value.

Two reasons:

1. Container orchestrators (Kubernetes Secrets, Docker secrets, systemd
   credentials) inject secrets as environment variables under names of
   their choosing. The indirection lets ops point the bridge at whatever
   name the secret store happens to use without re-rolling the YAML.
2. Inspecting the running process environment with `printenv` doesn't
   reveal which value is the live token — the YAML names it explicitly.
   This is a small operational nicety, not a security boundary.

## Validation rules

The loader returns an error wrapping one of the package sentinels:

- `ErrConfigMissing` — file does not exist or cannot be opened.
- `ErrConfigMalformed` — YAML decode failure, including unknown fields.
- `ErrConfigInvalid` — validation failure. Specifically:
  - `listen` must be a valid `host:port` with a non-zero port.
  - `log_level`, if set, must be one of `debug|info|warn|error`.
  - `forge.base_url` and `plane.base_url` must parse and be absolute
    (scheme + host).
  - `forge.token_env`, `forge.webhook_secret_env`, `plane.api_key_env`,
    and `plane.webhook_secret_env` must be non-empty.
  - `plane.workspace_slug`, `bridge_bot.forge_username`,
    `bridge_bot.plane_member_id` must be non-empty; the member ID must
    parse as a UUID.
  - Each `links[].forge_repo` matches `owner/repo` (one slash, no
    whitespace).
  - Each `links[].plane_project_id` and each `users[*]` value parses as
    a UUID.
  - `links[].project_identifier`, when set, matches
    `^[A-Z][A-Z0-9]{0,9}$` — uppercase letters and digits only, starts
    with a letter, at most 10 characters. When unset, PR ref parsing
    bypasses the link.
  - `links[].pr_state_map` keys, when set, must each be one of
    `opened|merged|closed|reviewed`. The values are plane state names
    resolved at runtime through the same lookup used by `state_map`, so
    typos in values surface as runtime warnings rather than load
    failures. Enables the PR → work-item state automation (build step
    9).
  - `idemp.lru_capacity` defaults to 4096 if zero; must be positive.
- `ErrSecretMissing` — one of the env vars named in
  `*_env` fields resolves to the empty string.

## Public API

```go
package mapping

type Config struct { /* YAML shape, decoded as-is */ }
type Resolved struct { /* validated config with secrets pulled in */ }
type Link struct {
    ForgeRepo         string
    PlaneProjectID    string
    ProjectIdentifier string            // optional, e.g. "PFB"
    StateMap          map[string]string
    PRStateMap        map[string]string // optional, keys: opened|merged|closed|reviewed
}

// Load reads path, applies PFB_* overrides, validates, and resolves
// secrets from os.Getenv.
func Load(path string) (*Resolved, error)

// LoadFromReader is the testable variant. envLookup defaults to a
// function that returns "" for every key if nil.
func LoadFromReader(r io.Reader, envLookup func(string) string) (*Resolved, error)

var (
    ErrConfigMissing   error // config file not found
    ErrConfigMalformed error // YAML decode failure
    ErrConfigInvalid   error // validation failure
    ErrSecretMissing   error // named secret env var unset/empty
)
```

The YAML library used is `gopkg.in/yaml.v3`. Decoding runs with
`KnownFields(true)` so typos in field names surface as
`ErrConfigMalformed`.
