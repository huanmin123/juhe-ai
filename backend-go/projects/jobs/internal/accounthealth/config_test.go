package accounthealth

import (
	"strings"
	"testing"
)

func TestLoadConfigDirectInputSource(t *testing.T) {
	key := strings.Repeat("A", 43)
	env := map[string]string{
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

// TestLoadConfigOwnerDefaultsToGoAndRejectsOthers：OWNER 缺省视为 go（完整
// 配置无需显式 OWNER 即通过）；显式非 go 值（trim + 大小写不敏感）拒绝。
func TestLoadConfigOwnerDefaultsToGoAndRejectsOthers(t *testing.T) {
	key := strings.Repeat("A", 43)
	base := map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_STORE":             "sqlite",
		"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH":     t.TempDir() + "/state.sqlite3",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   t.TempDir(),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": key,
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "credential-secret",
	}
	for _, owner := range []string{"", "go", "GO", " go "} {
		env := make(map[string]string, len(base)+1)
		for name, value := range base {
			env[name] = value
		}
		if owner != "" {
			env["JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER"] = owner
		}
		if _, err := LoadConfig(func(name string) string { return env[name] }); err != nil {
			t.Fatalf("OWNER=%q must be accepted, got %v", owner, err)
		}
	}
	for _, owner := range []string{"node", "golang", "Go2"} {
		env := make(map[string]string, len(base)+1)
		for name, value := range base {
			env[name] = value
		}
		env["JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER"] = owner
		_, err := LoadConfig(func(name string) string { return env[name] })
		if err == nil || !strings.Contains(err.Error(), "JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER 仅支持 go") {
			t.Fatalf("OWNER=%q must be rejected with the go-only error, got %v", owner, err)
		}
	}
}

// TestLoadConfigInstanceIDDefaultsToHostname：INSTANCE_ID 缺省取
// os.Hostname()（出错或空时退化 "juhe-ai-jobs"，断言非空兜底）；显式值仍优先。
func TestLoadConfigInstanceIDDefaultsToHostname(t *testing.T) {
	key := strings.Repeat("A", 43)
	base := map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_STORE":             "sqlite",
		"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH":     t.TempDir() + "/state.sqlite3",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   t.TempDir(),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": key,
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "credential-secret",
	}
	cfg, err := LoadConfig(func(name string) string { return base[name] })
	if err != nil {
		t.Fatalf("default InstanceID path must load: %v", err)
	}
	if cfg.InstanceID == "" {
		t.Fatal("default InstanceID must fall back to hostname or juhe-ai-jobs, not stay empty")
	}
	env := make(map[string]string, len(base)+1)
	for name, value := range base {
		env[name] = value
	}
	env["JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID"] = "explicit-instance"
	cfg, err = LoadConfig(func(name string) string { return env[name] })
	if err != nil || cfg.InstanceID != "explicit-instance" {
		t.Fatalf("explicit InstanceID must win: cfg=%#v err=%v", cfg, err)
	}
}

// TestLoadConfigCredentialSecretFallsBackToSharedSecret：CREDENTIAL_SECRET
// 缺省取 JUHE_AI_SECRET；两者均空 fail-fast 且文案同时列出两个变量。
func TestLoadConfigCredentialSecretFallsBackToSharedSecret(t *testing.T) {
	key := strings.Repeat("A", 43)
	base := map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_STORE":             "sqlite",
		"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH":     t.TempDir() + "/state.sqlite3",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   t.TempDir(),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": key,
	}
	env := make(map[string]string, len(base)+1)
	for name, value := range base {
		env[name] = value
	}
	env["JUHE_AI_SECRET"] = "shared-envelope-secret"
	cfg, err := LoadConfig(func(name string) string { return env[name] })
	if err != nil || cfg.CredentialSecret != "shared-envelope-secret" {
		t.Fatalf("CREDENTIAL_SECRET must default to JUHE_AI_SECRET: cfg=%#v err=%v", cfg, err)
	}
	env["JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET"] = "explicit-envelope-secret"
	cfg, err = LoadConfig(func(name string) string { return env[name] })
	if err != nil || cfg.CredentialSecret != "explicit-envelope-secret" {
		t.Fatalf("explicit CREDENTIAL_SECRET must win: cfg=%#v err=%v", cfg, err)
	}
	_, err = LoadConfig(func(name string) string { return base[name] })
	if err == nil || !strings.Contains(err.Error(), "需配置 JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET 或 JUHE_AI_SECRET（凭据封套密钥）") {
		t.Fatalf("missing both secrets must fail with the combined message, got %v", err)
	}
}

// TestLoadConfigFailsFastOnMissingRequiredEnv：无任何 J1 env 时 LoadConfig
// 直接报错（恒开终态无静默空配置路径）；按 fail-fast 顺序逐项补齐后，错误
// 文案依次指名 STORE、INPUT_DIRECTORY 与 INPUT_SIGNING_KEY 缺项。
func TestLoadConfigFailsFastOnMissingRequiredEnv(t *testing.T) {
	_, err := LoadConfig(func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_ACCOUNT_HEALTH_STORE") {
		t.Fatalf("no-env LoadConfig must fail on the first missing required entry, got %v", err)
	}
	key := strings.Repeat("A", 43)
	base := map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_STORE":         "sqlite",
		"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH": t.TempDir() + "/state.sqlite3",
	}
	_, err = LoadConfig(func(name string) string { return base[name] })
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY") {
		t.Fatalf("missing INPUT_DIRECTORY must be named by the error, got %v", err)
	}
	base["JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY"] = t.TempDir()
	base["JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET"] = "credential-secret"
	_, err = LoadConfig(func(name string) string { return base[name] })
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY") {
		t.Fatalf("missing INPUT_SIGNING_KEY must be named by the error, got %v", err)
	}
	base["JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY"] = key
	if _, err := LoadConfig(func(name string) string { return base[name] }); err != nil {
		t.Fatalf("fully populated env must load: %v", err)
	}
}
