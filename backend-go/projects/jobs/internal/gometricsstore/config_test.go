package gometricsstore

import (
	"testing"
	"time"
)

func TestLoadConfigDisabledByDefault(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" })
	if err != nil || cfg.Enabled || cfg.Interval != defaultInterval || cfg.RetentionDays != defaultRetentionDays {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestLoadConfigSQLite(t *testing.T) {
	values := map[string]string{"JUHE_AI_GO_RUNTIME_METRICS_STORE": "sqlite", "JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH": "metrics.sqlite3", "JUHE_AI_GO_RUNTIME_METRICS_INTERVAL": "20s", "JUHE_AI_GO_RUNTIME_METRICS_RETENTION_DAYS": "45"}
	cfg, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil || !cfg.Enabled || cfg.Interval != 20*time.Second || cfg.RetentionDays != 45 {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestLoadConfigPostgresRequiresURL(t *testing.T) {
	_, err := LoadConfig(func(key string) string {
		if key == "JUHE_AI_GO_RUNTIME_METRICS_STORE" {
			return "postgres"
		}
		return ""
	})
	if err == nil {
		t.Fatal("expected missing postgres URL error")
	}
}
