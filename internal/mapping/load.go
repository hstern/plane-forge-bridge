package mapping

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// uuidRE matches a lower- or upper-case canonical UUID (8-4-4-4-12).
// We intentionally avoid an external UUID library; the bridge never
// generates UUIDs, it only echoes them between systems.
var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// repoRE matches "owner/repo" with no leading/trailing slashes and exactly
// one separator. Forge repo paths use this canonical form.
var repoRE = regexp.MustCompile(`^[^/\s]+/[^/\s]+$`)

// validLogLevels is the set of log levels accepted in the YAML and in
// the PFB_LOG_LEVEL override.
var validLogLevels = map[string]struct{}{
	"debug": {},
	"info":  {},
	"warn":  {},
	"error": {},
}

// Load reads path, applies PFB_* environment overrides, validates the
// result, and resolves secrets from the environment. It returns a
// Resolved ready for use by the rest of the program.
//
// Errors wrap one of the package sentinels (ErrConfigMissing,
// ErrConfigMalformed, ErrConfigInvalid, ErrSecretMissing) so callers can
// branch with errors.Is.
func Load(path string) (*Resolved, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrConfigMissing, path)
		}
		return nil, fmt.Errorf("%w: %s: %v", ErrConfigMissing, path, err)
	}
	defer f.Close()
	return LoadFromReader(f, os.Getenv)
}

// LoadFromReader is the testable variant. It takes the YAML bytes
// directly and an env lookup function (use os.Getenv in production).
// Returning a non-nil *Resolved together with an error is never done —
// callers can rely on the (value, nil) / (nil, err) convention.
func LoadFromReader(r io.Reader, envLookup func(string) string) (*Resolved, error) {
	if envLookup == nil {
		envLookup = func(string) string { return "" }
	}

	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("%w: read: %v", ErrConfigMalformed, err)
	}

	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: %v", ErrConfigMalformed, err)
	}

	applyEnvOverrides(&cfg, envLookup)

	if err := validate(&cfg); err != nil {
		return nil, err
	}

	return resolve(&cfg, envLookup)
}

// applyEnvOverrides mutates cfg in place, replacing fields whose
// corresponding PFB_* env var is set. Empty env values are ignored — to
// clear a field, leave it out of the YAML.
func applyEnvOverrides(cfg *Config, env func(string) string) {
	set := func(dst *string, key string) {
		if v := env(key); v != "" {
			*dst = v
		}
	}
	set(&cfg.Listen, "PFB_LISTEN")
	set(&cfg.LogLevel, "PFB_LOG_LEVEL")
	set(&cfg.Forge.BaseURL, "PFB_FORGE_BASE_URL")
	set(&cfg.Forge.TokenEnv, "PFB_FORGE_TOKEN_ENV")
	set(&cfg.Forge.WebhookSecretEnv, "PFB_FORGE_WEBHOOK_SECRET_ENV")
	set(&cfg.Plane.BaseURL, "PFB_PLANE_BASE_URL")
	set(&cfg.Plane.WorkspaceSlug, "PFB_PLANE_WORKSPACE_SLUG")
	set(&cfg.Plane.APIKeyEnv, "PFB_PLANE_API_KEY_ENV")
	set(&cfg.Plane.WebhookSecretEnv, "PFB_PLANE_WEBHOOK_SECRET_ENV")
}

func validate(cfg *Config) error {
	if cfg.Listen == "" {
		return fmt.Errorf("%w: listen is required", ErrConfigInvalid)
	}
	if err := validateListen(cfg.Listen); err != nil {
		return fmt.Errorf("%w: listen %q: %v", ErrConfigInvalid, cfg.Listen, err)
	}

	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	if _, ok := validLogLevels[cfg.LogLevel]; !ok {
		return fmt.Errorf("%w: log_level %q: must be one of debug|info|warn|error", ErrConfigInvalid, cfg.LogLevel)
	}

	if err := validateAbsURL(cfg.Forge.BaseURL); err != nil {
		return fmt.Errorf("%w: forge.base_url %q: %v", ErrConfigInvalid, cfg.Forge.BaseURL, err)
	}
	if cfg.Forge.TokenEnv == "" {
		return fmt.Errorf("%w: forge.token_env is required", ErrConfigInvalid)
	}
	if cfg.Forge.WebhookSecretEnv == "" {
		return fmt.Errorf("%w: forge.webhook_secret_env is required", ErrConfigInvalid)
	}

	if err := validateAbsURL(cfg.Plane.BaseURL); err != nil {
		return fmt.Errorf("%w: plane.base_url %q: %v", ErrConfigInvalid, cfg.Plane.BaseURL, err)
	}
	if cfg.Plane.WorkspaceSlug == "" {
		return fmt.Errorf("%w: plane.workspace_slug is required", ErrConfigInvalid)
	}
	if cfg.Plane.APIKeyEnv == "" {
		return fmt.Errorf("%w: plane.api_key_env is required", ErrConfigInvalid)
	}
	if cfg.Plane.WebhookSecretEnv == "" {
		return fmt.Errorf("%w: plane.webhook_secret_env is required", ErrConfigInvalid)
	}

	if cfg.BridgeBot.ForgeUsername == "" {
		return fmt.Errorf("%w: bridge_bot.forge_username is required", ErrConfigInvalid)
	}
	if cfg.BridgeBot.PlaneMemberID == "" {
		return fmt.Errorf("%w: bridge_bot.plane_member_id is required", ErrConfigInvalid)
	}
	if !uuidRE.MatchString(cfg.BridgeBot.PlaneMemberID) {
		return fmt.Errorf("%w: bridge_bot.plane_member_id %q: not a UUID", ErrConfigInvalid, cfg.BridgeBot.PlaneMemberID)
	}

	for i, l := range cfg.Links {
		if !repoRE.MatchString(l.ForgeRepo) {
			return fmt.Errorf("%w: links[%d].forge_repo %q: must be owner/repo", ErrConfigInvalid, i, l.ForgeRepo)
		}
		if !uuidRE.MatchString(l.PlaneProjectID) {
			return fmt.Errorf("%w: links[%d].plane_project_id %q: not a UUID", ErrConfigInvalid, i, l.PlaneProjectID)
		}
	}

	for name, id := range cfg.Users {
		if !uuidRE.MatchString(id) {
			return fmt.Errorf("%w: users[%q] %q: not a UUID", ErrConfigInvalid, name, id)
		}
	}

	if cfg.Idemp.LRUCapacity == 0 {
		cfg.Idemp.LRUCapacity = defaultLRUCapacity
	}
	if cfg.Idemp.LRUCapacity < 0 {
		return fmt.Errorf("%w: idemp.lru_capacity %d: must be positive", ErrConfigInvalid, cfg.Idemp.LRUCapacity)
	}

	return nil
}

func validateListen(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	_ = host // empty host means "all interfaces", which is fine
	if port == "" {
		return errors.New("missing port")
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return fmt.Errorf("port %q: %v", port, err)
	}
	if n == 0 {
		return errors.New("port 0 is not allowed")
	}
	return nil
}

func validateAbsURL(s string) error {
	if s == "" {
		return errors.New("required")
	}
	u, err := url.Parse(s)
	if err != nil {
		return err
	}
	if u.Scheme == "" || u.Host == "" {
		return errors.New("must be absolute (scheme + host)")
	}
	return nil
}

func resolve(cfg *Config, env func(string) string) (*Resolved, error) {
	r := &Resolved{
		Listen:   cfg.Listen,
		LogLevel: cfg.LogLevel,
		Links:    cfg.Links,
		Users:    cfg.Users,
	}
	r.Forge.BaseURL = cfg.Forge.BaseURL
	r.Plane.BaseURL = cfg.Plane.BaseURL
	r.Plane.WorkspaceSlug = cfg.Plane.WorkspaceSlug
	r.BridgeBot.ForgeUsername = cfg.BridgeBot.ForgeUsername
	r.BridgeBot.PlaneMemberID = cfg.BridgeBot.PlaneMemberID
	r.Idemp.LRUCapacity = cfg.Idemp.LRUCapacity

	var err error
	if r.Forge.Token, err = mustEnv(env, cfg.Forge.TokenEnv); err != nil {
		return nil, err
	}
	if r.Forge.WebhookSecret, err = mustEnv(env, cfg.Forge.WebhookSecretEnv); err != nil {
		return nil, err
	}
	if r.Plane.APIKey, err = mustEnv(env, cfg.Plane.APIKeyEnv); err != nil {
		return nil, err
	}
	if r.Plane.WebhookSecret, err = mustEnv(env, cfg.Plane.WebhookSecretEnv); err != nil {
		return nil, err
	}

	return r, nil
}

func mustEnv(env func(string) string, name string) (string, error) {
	v := env(name)
	if v == "" {
		return "", fmt.Errorf("%w: %s", ErrSecretMissing, name)
	}
	return v, nil
}
