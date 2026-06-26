// Package config defines the on-disk configuration for mysql-mcp and the logic
// to locate, load, expand and validate it. A single set of Go structs drives
// JSON unmarshalling, runtime validation, the generated JSON Schema and the
// generated configuration docs.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
	"github.com/go-playground/validator/v10"
)

// AppName is the XDG application directory name (e.g. $XDG_CONFIG_HOME/mysql-mcp).
const AppName = "mysql-mcp"

// Config is the root configuration object.
type Config struct {
	// Schema is an optional pointer to the JSON Schema for this file. It is
	// ignored at runtime and exists so editors can offer completion.
	Schema string `json:"$schema,omitempty" jsonschema:"-"`

	// HTTP holds settings for the streamable HTTP transport.
	HTTP HTTPConfig `json:"http,omitempty"`

	// QueryTimeoutSeconds caps how long a single query or statement may run
	// before it is cancelled. Defaults to 30 when unset.
	QueryTimeoutSeconds int `json:"query_timeout_seconds,omitempty" validate:"omitempty,min=1"`

	// Sources maps a logical source name to its database connection settings.
	// At least one source is required. The source name is what MCP clients pass
	// in the "source" argument of each tool call.
	Sources map[string]*SourceConfig `json:"sources" validate:"required,min=1,dive"`
}

// HTTPConfig configures the streamable HTTP transport.
type HTTPConfig struct {
	// Addr is the listen address for the HTTP transport, host:port. Because the
	// server is meant to run on the same machine as its consumer, this defaults
	// to a loopback address.
	Addr string `json:"addr,omitempty"`

	// Path is the URL path the MCP endpoint is mounted on.
	Path string `json:"path,omitempty"`

	// Stateless serves each request without server-side session affinity. Set
	// this when running behind an MCP proxy or load balancer that may not pin a
	// client to one backend.
	Stateless bool `json:"stateless,omitempty"`

	// JSONResponse returns application/json instead of text/event-stream. Useful
	// behind proxies that buffer or do not support Server-Sent Events.
	JSONResponse bool `json:"json_response,omitempty"`

	// DisableDNSRebindProtection turns off the default localhost DNS-rebinding
	// guard. Enable it only when a trusted MCP proxy forwards requests with a
	// non-localhost Host header; do not expose the server to untrusted networks.
	DisableDNSRebindProtection bool `json:"disable_dns_rebind_protection,omitempty"`
}

// SourceConfig describes a single database a client can query. Connection
// details may be supplied either as a complete DSN or as discrete fields.
type SourceConfig struct {
	// Engine selects the database dialect. Supported in this release: "mysql"
	// and "mariadb" (handled identically).
	Engine string `json:"engine" validate:"required,oneof=mysql mariadb"`

	// DSN is a complete go-sql-driver/mysql data source name. When set, the
	// discrete host/port/user/password/database fields are ignored. The DSN
	// must not specify a custom net when SSH is configured.
	DSN string `json:"dsn,omitempty"`

	// Host is the database host. Defaults to 127.0.0.1.
	Host string `json:"host,omitempty"`
	// Port is the database port. Defaults to 3306.
	Port int `json:"port,omitempty" validate:"omitempty,min=1,max=65535"`
	// User is the database user.
	User string `json:"user,omitempty"`
	// Password is the database password. Supports ${ENV_VAR} expansion so the
	// secret can live in the environment rather than on disk.
	Password string `json:"password,omitempty"`
	// Database is the default schema to connect to. May be empty.
	Database string `json:"database,omitempty"`

	// Readonly forbids any statement that is not a pure read on this source.
	// Read-only sources reject the write_query tool and any non-SELECT/SHOW/
	// DESCRIBE/EXPLAIN statement.
	Readonly bool `json:"readonly,omitempty"`

	// SSH, when set, tunnels the database connection through an SSH host. The
	// database host/port are resolved from the SSH server's perspective.
	SSH *SSHConfig `json:"ssh,omitempty" validate:"omitempty"`

	// name is the map key, filled in during Load for convenient access.
	name string
}

// SSHConfig describes an SSH tunnel used to reach a remote database.
type SSHConfig struct {
	// Host is the SSH server hostname.
	Host string `json:"host" validate:"required"`
	// Port is the SSH server port. Defaults to 22.
	Port int `json:"port,omitempty" validate:"omitempty,min=1,max=65535"`
	// User is the SSH login user.
	User string `json:"user" validate:"required"`
	// Password is an optional SSH password. Supports ${ENV_VAR} expansion.
	// Prefer key-based auth via PrivateKeyPath.
	Password string `json:"password,omitempty"`
	// PrivateKeyPath is the path to a private key for public-key auth. Supports
	// ~ and ${ENV_VAR} expansion.
	PrivateKeyPath string `json:"private_key_path,omitempty"`
	// PrivateKeyPassphrase decrypts an encrypted private key. Supports
	// ${ENV_VAR} expansion.
	PrivateKeyPassphrase string `json:"private_key_passphrase,omitempty"`
	// KnownHostsPath is the known_hosts file used to verify the SSH host key.
	// Defaults to ~/.ssh/known_hosts. Host-key verification is always enforced.
	KnownHostsPath string `json:"known_hosts_path,omitempty"`
}

// Name returns the source's logical name (its key in the sources map).
func (s *SourceConfig) Name() string { return s.name }

// IsRemote reports whether the source is reached over an SSH tunnel.
func (s *SourceConfig) IsRemote() bool { return s.SSH != nil }

// DefaultConfigPath returns the XDG config path mysql-mcp loads by default,
// whether or not the file exists.
func DefaultConfigPath() string {
	return filepath.Join(xdg.ConfigHome, AppName, "config.json")
}

// Locate resolves the configuration file path. An explicit path is returned
// as-is. Otherwise the XDG config directories are searched.
func Locate(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if p, err := xdg.SearchConfigFile(filepath.Join(AppName, "config.json")); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("no config file found; create %s or pass --config", DefaultConfigPath())
}

// Load reads, expands and validates the configuration at the given path. When
// path is empty the default XDG location is used.
func Load(path string) (*Config, error) {
	resolved, err := Locate(path)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", resolved, err)
	}
	cfg, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse config %s: %w", resolved, err)
	}
	return cfg, nil
}

// Parse decodes, expands and validates configuration from raw JSON bytes. It is
// the testable core of Load.
func Parse(raw []byte) (*Config, error) {
	cfg, err := decodeStrict(raw)
	if err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	cfg.expandEnv()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate runs struct validation plus cross-field checks.
func (c *Config) Validate() error {
	v := validator.New(validator.WithRequiredStructEnabled())
	if err := v.Struct(c); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	for name, src := range c.Sources {
		if src.DSN == "" && src.Host == "" {
			return fmt.Errorf("source %q: either dsn or host must be set", name)
		}
		if src.DSN != "" && src.SSH != nil {
			return fmt.Errorf("source %q: dsn cannot be combined with ssh; use discrete host/port fields", name)
		}
	}
	return nil
}

func (c *Config) applyDefaults() {
	if c.HTTP.Addr == "" {
		c.HTTP.Addr = "127.0.0.1:7000"
	}
	if c.HTTP.Path == "" {
		c.HTTP.Path = "/mcp"
	}
	if c.QueryTimeoutSeconds == 0 {
		c.QueryTimeoutSeconds = 30
	}
	for name, src := range c.Sources {
		src.name = name
		if src.Port == 0 {
			src.Port = 3306
		}
		if src.Host == "" && src.DSN == "" {
			src.Host = "127.0.0.1"
		}
		if src.SSH != nil {
			if src.SSH.Port == 0 {
				src.SSH.Port = 22
			}
			if src.SSH.KnownHostsPath == "" {
				if home, err := os.UserHomeDir(); err == nil {
					src.SSH.KnownHostsPath = filepath.Join(home, ".ssh", "known_hosts")
				}
			}
		}
	}
}

// expandEnv expands ${VAR} references in secret-bearing string fields.
func (c *Config) expandEnv() {
	for _, src := range c.Sources {
		src.Password = expand(src.Password)
		src.DSN = expand(src.DSN)
		if src.SSH != nil {
			src.SSH.Password = expand(src.SSH.Password)
			src.SSH.PrivateKeyPassphrase = expand(src.SSH.PrivateKeyPassphrase)
			src.SSH.PrivateKeyPath = expandPath(src.SSH.PrivateKeyPath)
			src.SSH.KnownHostsPath = expandPath(src.SSH.KnownHostsPath)
		}
	}
}

func expand(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return os.Expand(s, func(k string) string { return os.Getenv(k) })
}

// expandPath expands environment variables and a leading ~ in a filesystem path.
func expandPath(p string) string {
	p = expand(p)
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
