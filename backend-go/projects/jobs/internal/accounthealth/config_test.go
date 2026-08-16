package accounthealth

import (
	"strings"
	"testing"
)

func TestLoadConfigDirectInputSource(t *testing.T) {
	key := strings.Repeat("A", 43)
	env := map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_ENABLED":            "true",
		"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":         "go",
		"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":        "jobs-test",
		"JUHE_AI_ACCOUNT_HEALTH_STORE":              "postgres",
		"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL":       "postgres://jobs-output",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":    t.TempDir(),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY":  key,
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET":  "credential-secret",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE":       "postgres",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL": "postgres://business-read-only",
		"JUHE_AI_ACCOUNT_HEALTH_DIRECT_INPUT_LIMIT": "17",
	}
	cfg, err := LoadConfig(func(name string) string { return env[name] })
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.InputSource != "postgres" || cfg.BusinessPostgresURL != env["JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL"] || cfg.DirectInputLimit != 17 {
		t.Fatalf("unexpected direct input config: %#v", cfg)
	}
}

func TestLoadConfigDirectInputRequiresSeparateBusinessURL(t *testing.T) {
	key := strings.Repeat("A", 43)
	env := map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_ENABLED":           "true",
		"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":        "go",
		"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":       "jobs-test",
		"JUHE_AI_ACCOUNT_HEALTH_STORE":             "postgres",
		"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL":      "postgres://jobs-output",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   t.TempDir(),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": key,
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "credential-secret",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE":      "postgres",
	}
	if _, err := LoadConfig(func(name string) string { return env[name] }); err == nil || !strings.Contains(err.Error(), "INPUT_POSTGRES_URL") {
		t.Fatalf("expected separate business URL validation, got %v", err)
	}
}

func TestLoadConfigRequiresExplicitGoOwner(t *testing.T) {
	env := map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_ENABLED": "true",
	}
	if _, err := LoadConfig(func(name string) string { return env[name] }); err == nil || !strings.Contains(err.Error(), "JOBS_OWNER") {
		t.Fatalf("expected explicit Go owner validation, got %v", err)
	}
}
