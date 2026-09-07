package gometrics

import (
	"context"
	"testing"
	"time"
)

// Config tests migrated from the former jobs gometricsstore suite; the shared
// LoadConfig only adds the caller's default role parameter.

func TestLoadConfigDisabledByDefault(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" }, "jobs")
	if err != nil || cfg.Enabled || cfg.Interval != defaultInterval || cfg.RetentionDays != defaultRetentionDays || cfg.Service != "juhe-ai" || cfg.Role != "jobs" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestLoadConfigAppliesCallerDefaultRole(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" }, "gateway")
	if err != nil || cfg.Role != "gateway" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestLoadConfigSQLite(t *testing.T) {
	values := map[string]string{"JUHE_AI_GO_RUNTIME_METRICS_STORE": "sqlite", "JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH": "metrics.sqlite3", "JUHE_AI_GO_RUNTIME_METRICS_INTERVAL": "20s", "JUHE_AI_GO_RUNTIME_METRICS_RETENTION_DAYS": "45", "JUHE_AI_GO_RUNTIME_METRICS_ROLE": "replica-1"}
	cfg, err := LoadConfig(func(key string) string { return values[key] }, "gateway")
	if err != nil || !cfg.Enabled || cfg.Interval != 20*time.Second || cfg.RetentionDays != 45 || cfg.Store != DialectSQLite || cfg.DatabasePath != "metrics.sqlite3" || cfg.Role != "replica-1" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestLoadConfigPostgresRequiresURL(t *testing.T) {
	_, err := LoadConfig(func(key string) string {
		if key == "JUHE_AI_GO_RUNTIME_METRICS_STORE" {
			return "postgres"
		}
		return ""
	}, "jobs")
	if err == nil {
		t.Fatal("expected missing postgres URL error")
	}
}

func TestLoadConfigRejectsInvalidIntervalAndRetention(t *testing.T) {
	for _, values := range []map[string]string{
		{"JUHE_AI_GO_RUNTIME_METRICS_STORE": "sqlite", "JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH": "m.sqlite3", "JUHE_AI_GO_RUNTIME_METRICS_INTERVAL": "500ms"},
		{"JUHE_AI_GO_RUNTIME_METRICS_STORE": "sqlite", "JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH": "m.sqlite3", "JUHE_AI_GO_RUNTIME_METRICS_RETENTION_DAYS": "0"},
		{"JUHE_AI_GO_RUNTIME_METRICS_STORE": "sqlite", "JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH": "m.sqlite3", "JUHE_AI_GO_RUNTIME_METRICS_RETENTION_DAYS": "3651"},
		{"JUHE_AI_GO_RUNTIME_METRICS_STORE": "oracle"},
	} {
		if _, err := LoadConfig(func(key string) string { return values[key] }, "jobs"); err == nil {
			t.Fatalf("expected config error for %+v", values)
		}
	}
}

func TestLoadConfigEnabledFalseDisables(t *testing.T) {
	values := map[string]string{"JUHE_AI_GO_RUNTIME_METRICS_STORE": "sqlite", "JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH": "m.sqlite3", "JUHE_AI_GO_RUNTIME_METRICS_ENABLED": "false"}
	cfg, err := LoadConfig(func(key string) string { return values[key] }, "jobs")
	if err != nil || cfg.Enabled {
		t.Fatalf("enabled=false must disable the store: cfg=%+v err=%v", cfg, err)
	}
}

func TestOpenStoreDisabledReturnsNil(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" }, "jobs")
	if err != nil {
		t.Fatal(err)
	}
	store, db, err := OpenStore(cfg)
	if err != nil || store != nil || db != nil {
		t.Fatalf("disabled config must return nil handles: %v %v %v", store, db, err)
	}
	if err := EnsureReady(context.Background(), store); err == nil {
		t.Fatal("EnsureReady must reject a nil (disabled) store")
	}
}
