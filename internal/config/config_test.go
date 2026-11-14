package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/shellghostt/eventrelay/internal/config"
)

func env(pairs map[string]string) func(string) string {
	return func(key string) string { return pairs[key] }
}

func validEnv() map[string]string {
	return map[string]string{
		"EVENTRELAY_REDIS_URL":    "redis://localhost:6379/0",
		"EVENTRELAY_POSTGRES_DSN": "postgres://user:secret@localhost:5432/eventrelay",
		"EVENTRELAY_ADMIN_TOKEN":  "0123456789abcdef0123456789abcdef",
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := config.Load(env(validEnv()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.HTTPAddr != config.DefaultHTTPAddr {
		t.Errorf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.MaxPayloadBytes != config.DefaultMaxPayloadBytes {
		t.Errorf("MaxPayloadBytes = %d", cfg.MaxPayloadBytes)
	}
	if cfg.SignatureTolerance != config.DefaultSignatureTolerance {
		t.Errorf("SignatureTolerance = %v", cfg.SignatureTolerance)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v", cfg.LogLevel)
	}
	if cfg.LogFormat != "json" {
		t.Errorf("LogFormat = %q", cfg.LogFormat)
	}
	if !cfg.MigrateOnStart {
		t.Error("MigrateOnStart should default to true")
	}
}

func TestLoadOverrides(t *testing.T) {
	pairs := validEnv()
	pairs["EVENTRELAY_HTTP_ADDR"] = ":9090"
	pairs["EVENTRELAY_MAX_PAYLOAD_BYTES"] = "1024"
	pairs["EVENTRELAY_SIGNATURE_TOLERANCE_SECONDS"] = "60"
	pairs["EVENTRELAY_LOG_LEVEL"] = "debug"
	pairs["EVENTRELAY_LOG_FORMAT"] = "text"
	pairs["EVENTRELAY_MIGRATE_ON_START"] = "false"

	cfg, err := config.Load(env(pairs))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.HTTPAddr != ":9090" || cfg.MaxPayloadBytes != 1024 {
		t.Fatalf("unexpected values: %+v", cfg)
	}
	if cfg.SignatureTolerance != 60*time.Second {
		t.Fatalf("tolerance = %v", cfg.SignatureTolerance)
	}
	if cfg.LogLevel != slog.LevelDebug || cfg.LogFormat != "text" {
		t.Fatalf("logging = %v/%s", cfg.LogLevel, cfg.LogFormat)
	}
	if cfg.MigrateOnStart {
		t.Fatal("MigrateOnStart should be false")
	}
}

func TestLoadRequiresCoreSettings(t *testing.T) {
	_, err := config.Load(env(map[string]string{}))
	if err == nil {
		t.Fatal("expected an error")
	}

	msg := err.Error()
	for _, want := range []string{"EVENTRELAY_POSTGRES_DSN", "EVENTRELAY_ADMIN_TOKEN"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %s: %s", want, msg)
		}
	}
}

func TestLoadRejectsShortAdminToken(t *testing.T) {
	pairs := validEnv()
	pairs["EVENTRELAY_ADMIN_TOKEN"] = "short"

	if _, err := config.Load(env(pairs)); err == nil {
		t.Fatal("expected an error for a short admin token")
	}
}

func TestLoadReportsEveryInvalidValue(t *testing.T) {
	pairs := validEnv()
	pairs["EVENTRELAY_MAX_PAYLOAD_BYTES"] = "nope"
	pairs["EVENTRELAY_LOG_LEVEL"] = "verbose"

	_, err := config.Load(env(pairs))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "EVENTRELAY_MAX_PAYLOAD_BYTES") {
		t.Error("missing first problem")
	}
	if !strings.Contains(err.Error(), "EVENTRELAY_LOG_LEVEL") {
		t.Error("missing second problem")
	}
}

func TestRedactedHidesPassword(t *testing.T) {
	cfg, err := config.Load(env(validEnv()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	redacted := cfg.Redacted()
	dsn, _ := redacted["postgres_dsn"].(string)
	if strings.Contains(dsn, "secret") {
		t.Fatalf("password leaked: %s", dsn)
	}
	if !strings.Contains(dsn, "localhost:5432") {
		t.Fatalf("host was removed: %s", dsn)
	}
	if redacted["admin_token_set"] != true {
		t.Fatal("admin_token_set should be true")
	}
}

func TestRedactedHidesQueryAndUsernameCredentials(t *testing.T) {
	for _, dsn := range []string{
		"postgres://user:secret@localhost/db?password=querysecret",
		"postgres://user@localhost/db?sslpassword=querysecret#secret",
		"postgres://secret@localhost/db",
		"host=localhost password=secret",
	} {
		cfg := config.Config{PostgresDSN: dsn}
		if strings.Contains(cfg.Redacted()["postgres_dsn"].(string), "secret") {
			t.Fatal("configuration redaction leaked a credential")
		}
	}
}
