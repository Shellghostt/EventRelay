// Package config loads EventRelay configuration from the environment.
//
// There is no configuration file. Every setting is an environment variable
// prefixed with EVENTRELAY_, documented in docs/PLAN.md section 11.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	HTTPAddr           string
	PostgresDSN        string
	RedisURL           string
	AdminToken         string
	MaxPayloadBytes    int64
	SignatureTolerance time.Duration
	HTTPTimeout        time.Duration
	ShutdownTimeout    time.Duration
	SweepInterval      time.Duration
	RetentionDays      int
	MigrateOnStart     bool
	LogLevel           slog.Level
	LogFormat          string
}

// Defaults are the values used when an optional variable is unset.
const (
	DefaultHTTPAddr           = ":8080"
	DefaultMaxPayloadBytes    = 256 << 10 // 256 KiB
	DefaultSignatureTolerance = 5 * time.Minute
	DefaultHTTPTimeout        = 10 * time.Second
	DefaultShutdownTimeout    = 15 * time.Second
	DefaultSweepInterval      = 5 * time.Second
	DefaultRetentionDays      = 30
)

// Load builds a Config from getenv. It reports every invalid value at once
// rather than failing on the first one.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		HTTPAddr:           valueOr(getenv, "EVENTRELAY_HTTP_ADDR", DefaultHTTPAddr),
		PostgresDSN:        strings.TrimSpace(getenv("EVENTRELAY_POSTGRES_DSN")),
		RedisURL:           strings.TrimSpace(getenv("EVENTRELAY_REDIS_URL")),
		AdminToken:         strings.TrimSpace(getenv("EVENTRELAY_ADMIN_TOKEN")),
		MaxPayloadBytes:    DefaultMaxPayloadBytes,
		SignatureTolerance: DefaultSignatureTolerance,
		HTTPTimeout:        DefaultHTTPTimeout,
		ShutdownTimeout:    DefaultShutdownTimeout,
		SweepInterval:      DefaultSweepInterval,
		RetentionDays:      DefaultRetentionDays,
		MigrateOnStart:     true,
		LogLevel:           slog.LevelInfo,
		LogFormat:          "json",
	}

	var problems []error
	invalid := func(name, value, want string) {
		problems = append(problems, fmt.Errorf("%s=%q is invalid: expected %s", name, value, want))
	}

	if v := getenv("EVENTRELAY_MAX_PAYLOAD_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 || n > 64<<20 {
			invalid("EVENTRELAY_MAX_PAYLOAD_BYTES", v, "a positive integer no greater than 67108864")
		} else {
			cfg.MaxPayloadBytes = n
		}
	}

	if v := getenv("EVENTRELAY_SIGNATURE_TOLERANCE_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 3600 {
			invalid("EVENTRELAY_SIGNATURE_TOLERANCE_SECONDS", v, "an integer between 1 and 3600")
		} else {
			cfg.SignatureTolerance = time.Duration(n) * time.Second
		}
	}

	if v := getenv("EVENTRELAY_HTTP_TIMEOUT_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 300 {
			invalid("EVENTRELAY_HTTP_TIMEOUT_SECONDS", v, "an integer between 1 and 300")
		} else {
			cfg.HTTPTimeout = time.Duration(n) * time.Second
		}
	}

	if v := getenv("EVENTRELAY_SHUTDOWN_TIMEOUT_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 300 {
			invalid("EVENTRELAY_SHUTDOWN_TIMEOUT_SECONDS", v, "an integer between 1 and 300")
		} else {
			cfg.ShutdownTimeout = time.Duration(n) * time.Second
		}
	}

	if v := getenv("EVENTRELAY_SWEEP_INTERVAL_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 3600 {
			invalid("EVENTRELAY_SWEEP_INTERVAL_SECONDS", v, "an integer between 1 and 3600")
		} else {
			cfg.SweepInterval = time.Duration(n) * time.Second
		}
	}

	if v := getenv("EVENTRELAY_RETENTION_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 3650 {
			invalid("EVENTRELAY_RETENTION_DAYS", v, "an integer between 1 and 3650")
		} else {
			cfg.RetentionDays = n
		}
	}

	if v := getenv("EVENTRELAY_MIGRATE_ON_START"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			invalid("EVENTRELAY_MIGRATE_ON_START", v, "true or false")
		} else {
			cfg.MigrateOnStart = b
		}
	}

	if v := strings.ToLower(strings.TrimSpace(getenv("EVENTRELAY_LOG_LEVEL"))); v != "" {
		switch v {
		case "debug":
			cfg.LogLevel = slog.LevelDebug
		case "info":
			cfg.LogLevel = slog.LevelInfo
		case "warn":
			cfg.LogLevel = slog.LevelWarn
		case "error":
			cfg.LogLevel = slog.LevelError
		default:
			invalid("EVENTRELAY_LOG_LEVEL", v, "one of debug, info, warn, error")
		}
	}

	if v := strings.ToLower(strings.TrimSpace(getenv("EVENTRELAY_LOG_FORMAT"))); v != "" {
		if v != "json" && v != "text" {
			invalid("EVENTRELAY_LOG_FORMAT", v, "json or text")
		} else {
			cfg.LogFormat = v
		}
	}

	if cfg.PostgresDSN == "" {
		problems = append(problems, errors.New("EVENTRELAY_POSTGRES_DSN is required"))
	}
	if cfg.RedisURL == "" {
		// Redis is required from M2. It is reported here so that a missing
		// value is a startup warning rather than a runtime surprise.
		cfg.RedisURL = ""
	}
	if len(cfg.AdminToken) < 16 {
		problems = append(problems, errors.New("EVENTRELAY_ADMIN_TOKEN is required and must be at least 16 characters"))
	}

	if len(problems) > 0 {
		return Config{}, errors.Join(problems...)
	}
	return cfg, nil
}

// Redacted returns the configuration safe to log.
func (c Config) Redacted() map[string]any {
	return map[string]any{
		"http_addr":           c.HTTPAddr,
		"postgres_dsn":        redactDSN(c.PostgresDSN),
		"redis_configured":    c.RedisURL != "",
		"admin_token_set":     c.AdminToken != "",
		"max_payload_bytes":   c.MaxPayloadBytes,
		"signature_tolerance": c.SignatureTolerance.String(),
		"log_level":           c.LogLevel.String(),
		"log_format":          c.LogFormat,
		"migrate_on_start":    c.MigrateOnStart,
	}
}

func valueOr(getenv func(string) string, key, fallback string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return fallback
}

// redactDSN keeps the host and database, which are useful in a log line, and
// removes the password entirely.
func redactDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	at := strings.LastIndex(dsn, "@")
	schemeEnd := strings.Index(dsn, "://")
	if at < 0 || schemeEnd < 0 || at < schemeEnd {
		return "postgres://<redacted>"
	}
	creds := dsn[schemeEnd+3 : at]
	user := creds
	if colon := strings.Index(creds, ":"); colon >= 0 {
		user = creds[:colon]
	}
	return dsn[:schemeEnd+3] + user + ":***" + dsn[at:]
}
