package mapping

import "errors"

// Config is the YAML shape, decoded as-is.
type Config struct {
	Listen   string `yaml:"listen"`
	LogLevel string `yaml:"log_level"`

	Forge struct {
		BaseURL          string `yaml:"base_url"`
		TokenEnv         string `yaml:"token_env"`
		WebhookSecretEnv string `yaml:"webhook_secret_env"`
	} `yaml:"forge"`

	Plane struct {
		BaseURL          string `yaml:"base_url"`
		WorkspaceSlug    string `yaml:"workspace_slug"`
		APIKeyEnv        string `yaml:"api_key_env"`
		WebhookSecretEnv string `yaml:"webhook_secret_env"`
	} `yaml:"plane"`

	BridgeBot struct {
		ForgeUsername string `yaml:"forge_username"`
		PlaneMemberID string `yaml:"plane_member_id"`
	} `yaml:"bridge_bot"`

	Links []Link            `yaml:"links"`
	Users map[string]string `yaml:"users"`

	Idemp struct {
		LRUCapacity int `yaml:"lru_capacity"`
	} `yaml:"idemp"`
}

// Link couples a forge repository with a Plane project and supplies the
// per-link state name mapping.
type Link struct {
	ForgeRepo      string            `yaml:"forge_repo"`
	PlaneProjectID string            `yaml:"plane_project_id"`
	StateMap       map[string]string `yaml:"state_map"`
}

// Resolved is the fully-validated configuration with secrets pulled in
// from the environment. This is the value the rest of the program uses.
type Resolved struct {
	Listen   string
	LogLevel string

	Forge struct {
		BaseURL       string
		Token         string
		WebhookSecret string
	}

	Plane struct {
		BaseURL       string
		WorkspaceSlug string
		APIKey        string
		WebhookSecret string
	}

	BridgeBot struct {
		ForgeUsername string
		PlaneMemberID string
	}

	Links []Link
	Users map[string]string

	Idemp struct {
		LRUCapacity int
	}
}

// Sentinel errors. All loader errors wrap one of these so callers can
// branch on errors.Is.
var (
	ErrConfigMissing   = errors.New("mapping: config file not found")
	ErrConfigMalformed = errors.New("mapping: malformed config")
	ErrConfigInvalid   = errors.New("mapping: invalid config")
	ErrSecretMissing   = errors.New("mapping: required secret env var is unset or empty")
)

// defaultLRUCapacity is the loop-break LRU size used when the config
// leaves idemp.lru_capacity at zero.
const defaultLRUCapacity = 4096
