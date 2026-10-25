package accounthealth

import (
	"strings"
	"testing"
)

func TestLoadConfigDirectInputSource(t *testing.T) {
	key := strings.Repeat("A", 43)
	env := map[string]string{
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
		"JUHE_AI_ACCOUNT_HEALTH_MAX_CONCURRENCY":    "19",
	}
	cfg, err := LoadConfig(func(name string) string { return env[name] })
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.InputSource != "postgres" || cfg.BusinessPostgresURL != env["JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL"] || cfg.DirectInputLimit != 17 || cfg.MaxConcurrency != 19 || cfg.IOConcurrency != cfg.MaxConcurrency || cfg.DBConcurrency != defaultDBConcurrency || cfg.DBQueueSize != defaultDBQueueSize || cfg.Store.PostgresMaxOpenConns != defaultPostgresPoolSize || cfg.Store.PostgresMaxIdleConns != defaultPostgresMaxIdleConns || cfg.DirectInputPostgresMaxOpenConns != defaultPostgresPoolSize || cfg.DirectInputPostgresMaxIdleConns != defaultPostgresMaxIdleConns {
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
	cfg, err = LoadConfig(func(key string) string { return withPool[key] })
	if err != nil || cfg.Store.PostgresMaxOpenConns != defaultPostgresPoolSize || cfg.Store.PostgresMaxIdleConns != defaultPostgresMaxIdleConns {
		t.Fatalf("external PostgreSQL pool size must be accepted: cfg=%#v err=%v", cfg, err)
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
		"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL":       "postgres://jobs-output",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL": "postgres://business-read-only",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":    t.TempDir(),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY":  key,
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET":  "credential-secret",
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
		"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL":       "postgres://jobs-output",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL": "postgres://business-read-only",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":    t.TempDir(),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY":  key,
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET":  "credential-secret",
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
		"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL":       "postgres://jobs-output",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL": "postgres://business-read-only",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":    t.TempDir(),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY":  key,
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
	// 两者均空：非生产回退开发密钥（与 gateway defaultRuntimeSecret 同值）；
	// production 仍 fail-fast。
	devFallback, err := LoadConfig(func(name string) string { return base[name] })
	if err != nil || devFallback.CredentialSecret != "juhe-ai-dev-secret-change-me" {
		t.Fatalf("missing both secrets must fall back to the dev envelope secret in non-production, got cfg=%#v err=%v", devFallback, err)
	}
	prodEnv := make(map[string]string, len(base)+1)
	for name, value := range base {
		prodEnv[name] = value
	}
	prodEnv["NODE_ENV"] = "production"
	if _, err := LoadConfig(func(name string) string { return prodEnv[name] }); err == nil || !strings.Contains(err.Error(), "凭据封套密钥") {
		t.Fatalf("production missing both secrets must fail, got %v", err)
	}
}

// TestLoadConfigZeroConfigFailsArms：J1 必填项默认化后（2026-09-19 零配置
// 决策），路径/input 目录/签名 key/凭据密钥（非生产回退开发密钥）均有缺省，
// 仅注 DATA_DIR 即可完整装载；production 下凭据封套密钥缺失仍 fail-fast，
// 显式配置始终优先。
func TestLoadConfigZeroConfigFailsArms(t *testing.T) {
	// PG 唯一形态：零配置不再可启动（POSTGRES_URL 必填 fail-fast；原「仅
	// DATA_DIR 即可完整装载」臂随 sqlite 形态退役删除）。
	if _, err := LoadConfig(func(name string) string {
		if name == "JUHE_AI_DATA_DIR" {
			return t.TempDir()
		}
		return ""
	}); err == nil || !strings.Contains(err.Error(), "JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL") {
		t.Fatalf("zero-config 必须 fail-fast 缺 POSTGRES_URL: %v", err)
	}
	loaded, err := LoadConfig(func(name string) string {
		switch name {
		case "JUHE_AI_DATA_DIR":
			return t.TempDir()
		case "JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL":
			return "postgres://jobs-output"
		case "JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL":
			return "postgres://business-read-only"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("最小 PG env LoadConfig must succeed, got %v", err)
	}
	if loaded.CredentialSecret != "juhe-ai-dev-secret-change-me" {
		t.Fatalf("credential secret must fall back to the dev envelope secret, got %q", loaded.CredentialSecret)
	}
	if _, err := LoadConfig(func(name string) string {
		switch name {
		case "JUHE_AI_DATA_DIR":
			return t.TempDir()
		case "JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL":
			return "postgres://jobs-output"
		case "JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL":
			return "postgres://business-read-only"
		case "NODE_ENV":
			return "production"
		}
		return ""
	}); err == nil || !strings.Contains(err.Error(), "JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET") {
		t.Fatalf("production zero-config must fail on the credential envelope secret, got %v", err)
	}
	if _, err := LoadConfig(func(name string) string {
		switch name {
		case "JUHE_AI_DATA_DIR":
			return t.TempDir()
		case "JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL":
			return "postgres://jobs-output"
		case "JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL":
			return "postgres://business-read-only"
		case "JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET":
			return "credential-secret"
		}
		return ""
	}); err != nil {
		t.Fatalf("minimal PG env with credential secret must load: %v", err)
	}
}

// TestLoadConfigZeroConfigDefaultsKeyFileAndSource 锁定 PG 唯一形态的零配置
// 契约：InputSource 恒 postgres、缺 INPUT_POSTGRES_URL fail-fast、显式
// SIGNING_KEY 优先；原 sqlite 缺省臂与 key 文件派生臂随 sqlite 形态退役删除
// （key 文件派生 helper 保留但仅在显式环境消费）。
func TestLoadConfigZeroConfigDefaultsKeyFileAndSource(t *testing.T) {
	base := map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "pg-direct-secret",
		"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL":      "postgres://jobs-output",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": strings.Repeat("A", 43),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   t.TempDir(),
	}
	// 缺 INPUT_POSTGRES_URL → fail-fast（D12：postgres 直读恒定）。
	if _, err := LoadConfig(func(name string) string { return base[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL") {
		t.Fatalf("缺 INPUT_POSTGRES_URL 必须 fail-fast: %v", err)
	}
	// 补齐后：InputSource 恒 postgres、Store 恒 StorePostgres。
	env := map[string]string{}
	for name, value := range base {
		env[name] = value
	}
	env["JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL"] = "postgres://business-read-only"
	cfg, err := LoadConfig(func(name string) string { return env[name] })
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.InputSource != "postgres" || cfg.Store.Mode != StorePostgres {
		t.Fatalf("InputSource/Store.Mode 恒 postgres: %#v", cfg)
	}
	if len(cfg.InputKeys) == 0 || cfg.InputKeys["runtime-v1"] == nil {
		t.Fatalf("显式 SIGNING_KEY 必须注入 InputKeys: %#v", cfg.InputKeys)
	}
	// 显式非法输入源 env 不再被读取（变量退役）：任意值不影响装载。
	env["JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE"] = "files"
	if _, err := LoadConfig(func(name string) string { return env[name] }); err != nil {
		t.Fatalf("退役 env 不得被读取: %v", err)
	}
}
