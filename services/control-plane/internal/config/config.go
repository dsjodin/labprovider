// Package config loads dashboard settings from the environment. All upstream
// tokens come from files (preferred) or env vars; nothing is hardcoded and
// tokens are never logged.
package config

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr string // listen address, e.g. :8443
	FQDN string // dashboard FQDN, for display; empty when not set

	TLSCert string // path to the step-ca-issued cert; empty => HTTP fallback
	TLSKey  string

	// Certificates panel (step-ca PostgreSQL backend, read-only role).
	// DSN carries no password; the password comes from a file (or env) so it
	// stays out of the DSN string, matching the repo's file-based secrets.
	StepCADSN      string
	StepCAPassword string
	CertWarnDays   int

	// DNS panel (Technitium).
	TechnitiumURL      string
	TechnitiumToken    string
	TechnitiumCABundle string

	// IPAM panel (NetBox) - dedicated read-only token.
	NetboxURL      string
	NetboxToken    string
	NetboxCABundle string

	// Services + errors panels (Docker socket, mounted read-only).
	DockerHost       string
	ContainerFilters []string
	LogTail          int

	UpstreamTimeout time.Duration

	// Deploy engine paths. ConfigPath is the managed labprovider.env the
	// wizard edits; ExamplePath is the shipped example (copied into the image
	// at build time); StatePath is the advisory deploy-state file. The engine
	// is enabled when ExamplePath exists.
	ConfigPath  string
	ExamplePath string
	StatePath   string

	// Operator login. UsersPath is the file-backed account store; an empty
	// value disables authentication entirely, which is only defensible for the
	// read-only dashboard deployment.
	UsersPath  string
	SessionTTL time.Duration

	// LogLevel is the slog level. Fixed at info, debugging a deploy meant
	// adding rc.Log calls and rebuilding the image.
	LogLevel slog.Level
}

// Load reads configuration from the environment.
func Load() Config {
	return Config{
		Addr: envOr("CONTROL_PLANE_ADDR", ":8443"),
		// No default: install.sh starts the container without this variable,
		// and a guessed hostname displayed as fact is worse than none. The
		// server prefers CONTROL_PLANE_FQDN from the managed config anyway;
		// this is only the fallback for deployments that set it in the process
		// environment (the standalone compose file).
		FQDN: os.Getenv("CONTROL_PLANE_FQDN"),

		TLSCert: os.Getenv("CONTROL_PLANE_TLS_CERT"),
		TLSKey:  os.Getenv("CONTROL_PLANE_TLS_KEY"),

		StepCADSN:      os.Getenv("CONTROL_PLANE_STEPCA_DSN"),
		StepCAPassword: readToken("CONTROL_PLANE_STEPCA_PG_PASSWORD_FILE", "CONTROL_PLANE_STEPCA_PG_PASSWORD"),
		CertWarnDays:   envInt("CONTROL_PLANE_CERT_WARN_DAYS", 30),

		TechnitiumURL:      os.Getenv("CONTROL_PLANE_TECHNITIUM_URL"),
		TechnitiumToken:    readToken("CONTROL_PLANE_TECHNITIUM_TOKEN_FILE", "CONTROL_PLANE_TECHNITIUM_TOKEN"),
		TechnitiumCABundle: os.Getenv("CONTROL_PLANE_TECHNITIUM_CA_BUNDLE"),

		NetboxURL:      os.Getenv("CONTROL_PLANE_NETBOX_URL"),
		NetboxToken:    readToken("CONTROL_PLANE_NETBOX_TOKEN_FILE", "CONTROL_PLANE_NETBOX_TOKEN"),
		NetboxCABundle: os.Getenv("CONTROL_PLANE_NETBOX_CA_BUNDLE"),

		DockerHost:       envOr("CONTROL_PLANE_DOCKER_HOST", "unix:///var/run/docker.sock"),
		ContainerFilters: splitCSV(envOr("CONTROL_PLANE_CONTAINER_FILTERS", "step-ca,technitium,netbox,dns-sync,authentik,keycloak,zitadel,depot,sftpgo,s3,traefik,chrony,rsyslog,mailpit,lldap,samba,kmip,harbor,control-plane")),
		LogTail:          envInt("CONTROL_PLANE_LOG_TAIL", 200),

		UpstreamTimeout: envDuration("CONTROL_PLANE_UPSTREAM_TIMEOUT", 5*time.Second),

		ConfigPath:  envOr("CONTROL_PLANE_CONFIG_PATH", "/opt/labprovider/control-plane/labprovider.env"),
		ExamplePath: envOr("CONTROL_PLANE_EXAMPLE_PATH", "/usr/local/share/labprovider/labprovider.env.example"),
		StatePath:   envOr("CONTROL_PLANE_STATE_PATH", "/opt/labprovider/control-plane/state.json"),

		UsersPath:  envOr("CONTROL_PLANE_USERS_PATH", "/opt/labprovider/control-plane/users.json"),
		SessionTTL: envDuration("CONTROL_PLANE_SESSION_TTL", 12*time.Hour),

		LogLevel: envLevel("CONTROL_PLANE_LOG_LEVEL", slog.LevelInfo),
	}
}

// readToken prefers a file path (SOPS/age friendly) over an inline env var.
//
// An unreadable file - wrong owner after a chown, wrong mode - falls through to
// an env var that is normally unset, and the panel then renders "not
// configured". That is the message for "you never set this up", not for
// "labprovider cannot read the file you pointed it at", so the fallback is
// logged. Keeping the fallback rather than failing: one unreadable token file
// should cost its own panel, not the whole dashboard.
func readToken(fileKey, envKey string) string {
	if p := os.Getenv(fileKey); p != "" {
		b, err := os.ReadFile(p)
		if err == nil {
			return strings.TrimSpace(string(b))
		}
		slog.Warn("token file is unreadable, falling back to the environment variable",
			"var", fileKey, "path", p, "fallback", envKey, "err", err)
	}
	return strings.TrimSpace(os.Getenv(envKey))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envInt and envDuration keep the default on a malformed value, but say so:
// CONTROL_PLANE_LOG_TAIL="200s" or CONTROL_PLANE_SESSION_TTL="12" are typos
// that otherwise take effect as silence, and a session TTL that is not the one
// you set is not something to discover from behavior.
func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		slog.Warn("not a number, using the default", "var", key, "value", v, "default", def)
		return def
	}
	return n
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		slog.Warn("not a duration, using the default", "var", key, "value", v, "default", def)
		return def
	}
	return d
}

// envLevel parses a slog level name. Unlike envInt and envDuration this one has
// no numeric form to typo, so the accepted set is spelled out in the warning.
func envLevel(key string, def slog.Level) slog.Level {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(v)); err != nil {
		slog.Warn("not a log level, using the default", "var", key, "value", v,
			"accepted", "debug, info, warn, error", "default", def)
		return def
	}
	return level
}

func splitCSV(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
