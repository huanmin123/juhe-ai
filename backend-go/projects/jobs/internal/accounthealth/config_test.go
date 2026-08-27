package accounthealth

import (
	"strings"
	"testing"
)

func TestLoadConfigDirectInputSource(t *testing.T) {
	key := strings.Repeat("A", 43)
	env := map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_ENABLED":                       "true",
		"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":                    "go",
		"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":                   "jobs-test",
		"JUHE_AI_ACCOUNT_HEALTH_STORE":                         "postgres",
		"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL":                  "postgres://jobs-output",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":               t.TempDir(),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY":             key,
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET":             "credential-secret",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE":                  "postgres",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL":            "postgres://business-read-only",
		"JUHE_AI_ACCOUNT_HEALTH_DIRECT_INPUT_LIMIT":            "17",
		"JUHE_AI_ACCOUNT_HEALTH_MAX_CONCURRENCY":               "19",
		"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_MAX_OPEN_CONNS":       "23",
		"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_MAX_IDLE_CONNS":       "10",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_MAX_OPEN_CONNS": "29",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_MAX_IDLE_CONNS": "10",
	}
	cfg, err := LoadConfig(func(name string) string { return env[name] })
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.InputSource != "postgres" || cfg.BusinessPostgresURL != env["JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL"] || cfg.DirectInputLimit != 17 || cfg.MaxConcurrency != 19 || cfg.IOConcurrency != 19 || cfg.DBConcurrency != defaultDBConcurrency || cfg.DBQueueSize != defaultDBQueueSize || cfg.Store.PostgresMaxOpenConns != 23 || cfg.Store.PostgresMaxIdleConns != 10 || cfg.DirectInputPostgresMaxOpenConns != 29 || cfg.DirectInputPostgresMaxIdleConns != 10 {
		t.Fatalf("unexpected direct input config: %#v", cfg)
	}
}

func TestLoadConfigJ1CapacityDefaultsAndUpperBound(t *testing.T) {
	key := strings.Repeat("A", 43)
	base := map[string]string{
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
	}
	cfg, err := LoadConfig(func(name string) string { return base[name] })
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.DirectInputLimit != defaultDirectInputLimit || cfg.MaxConcurrency != defaultPostgresConcurrency || cfg.IOConcurrency != defaultPostgresConcurrency || cfg.DBConcurrency != defaultDBConcurrency || cfg.DBQueueSize != defaultDBQueueSize || cfg.Store.PostgresMaxOpenConns != defaultPostgresPoolSize || cfg.Store.PostgresMaxIdleConns != defaultPostgresMaxIdleConns {
		t.Fatalf("defaults changed: %#v", cfg)
	}
	for _, name := range []string{"JUHE_AI_ACCOUNT_HEALTH_MAX_CONCURRENCY", "JUHE_AI_ACCOUNT_HEALTH_DIRECT_INPUT_LIMIT"} {
		withValue := make(map[string]string, len(base)+1)
		for key, value := range base {
			withValue[key] = value
		}
		withValue[name] = "5097"
		if _, err := LoadConfig(func(key string) string { return withValue[key] }); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s must reject values above its configured range, got %v", name, err)
		}
	}
	withPool := make(map[string]string, len(base)+2)
	for key, value := range base {
		withPool[key] = value
	}
	withPool["JUHE_AI_ACCOUNT_HEALTH_POSTGRES_MAX_OPEN_CONNS"] = "1200"
	withPool["JUHE_AI_ACCOUNT_HEALTH_POSTGRES_MAX_IDLE_CONNS"] = "10"
	cfg, err = LoadConfig(func(key string) string { return withPool[key] })
	if err != nil || cfg.Store.PostgresMaxOpenConns != 1200 || cfg.Store.PostgresMaxIdleConns != 10 {
		t.Fatalf("external PostgreSQL pool size must be accepted: cfg=%#v err=%v", cfg, err)
	}
	withPool["JUHE_AI_ACCOUNT_HEALTH_POSTGRES_MAX_IDLE_CONNS"] = "11"
	if _, err := LoadConfig(func(key string) string { return withPool[key] }); err == nil {
		t.Fatal("idle connection configuration above the platform limit must be rejected")
	}
}

func TestLoadConfigSQLiteUsesFourWorkers(t *testing.T) {
	key := strings.Repeat("A", 43)
	storePath := t.TempDir() + "/state.sqlite3"
	base := map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_ENABLED":           "true",
		"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":        "go",
		"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":       "jobs-test",
		"JUHE_AI_ACCOUNT_HEALTH_STORE":             "sqlite",
		"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH":     storePath,
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   t.TempDir(),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": key,
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "credential-secret",
	}
	cfg, err := LoadConfig(func(name string) string { return base[name] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConcurrency != defaultSQLiteConcurrency {
		t.Fatalf("SQLite default concurrency = %d, want %d", cfg.MaxConcurrency, defaultSQLiteConcurrency)
	}
}

func TestLoadConfigSQLitePerformanceModeUsesSixtyFourWorkers(t *testing.T) {
	key := strings.Repeat("A", 43)
	base := map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_ENABLED":           "true",
		"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":        "go",
		"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":       "jobs-test",
		"JUHE_AI_ACCOUNT_HEALTH_STORE":             "sqlite",
		"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH":     t.TempDir() + "/state.sqlite3",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   t.TempDir(),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": key,
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "credential-secret",
		"JUHE_AI_RUNTIME_MODE":                     "performance",
	}
	cfg, err := LoadConfig(func(name string) string { return base[name] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConcurrency != defaultPerformanceConcurrency {
		t.Fatalf("SQLite performance concurrency = %d, want %d", cfg.MaxConcurrency, defaultPerformanceConcurrency)
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
