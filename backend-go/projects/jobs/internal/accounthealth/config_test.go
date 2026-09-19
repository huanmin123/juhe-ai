package accounthealth

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
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
	loaded, err := LoadConfig(func(name string) string {
		if name == "JUHE_AI_DATA_DIR" {
			return t.TempDir()
		}
		return ""
	})
	if err != nil {
		t.Fatalf("zero-config LoadConfig must succeed (dev secret fallback), got %v", err)
	}
	if loaded.CredentialSecret != "juhe-ai-dev-secret-change-me" {
		t.Fatalf("zero-config credential secret must fall back to the dev envelope secret, got %q", loaded.CredentialSecret)
	}
	if _, err := LoadConfig(func(name string) string {
		switch name {
		case "JUHE_AI_DATA_DIR":
			return t.TempDir()
		case "NODE_ENV":
			return "production"
		}
		return ""
	}); err == nil || !strings.Contains(err.Error(), "JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET") {
		t.Fatalf("production zero-config must fail on the credential envelope secret, got %v", err)
	}
	if _, err := LoadConfig(func(name string) string {
		if name == "JUHE_AI_DATA_DIR" {
			return t.TempDir()
		}
		if name == "JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET" {
			return "credential-secret"
		}
		return ""
	}); err != nil {
		t.Fatalf("zero-config env with only DATA_DIR + credential secret must load: %v", err)
	}
}

// TestLoadConfigZeroConfigDefaultsKeyFileAndSource 覆盖 2026-09-19 零配置
// 决策：仅注 DATA_DIR + 凭据封套密钥（唯一无法派生的共享密钥）即可完整装载。
// 校验四件事：store/source 缺省推导两臂、派生路径落 <DATA_DIR> 固定名、
// 签名 key 文件首次装载生成（48 字节 base64rawurl、0600）、二次装载复用
// 同一 key（进程重启后签名密钥稳定）。
func TestLoadConfigZeroConfigDefaultsKeyFileAndSource(t *testing.T) {
	dataDir := t.TempDir()
	base := map[string]string{
		"JUHE_AI_DATA_DIR":                         dataDir,
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "zero-config-secret",
	}
	getenv := func(name string) string { return base[name] }

	// 臂一：sqlite 缺省（DATABASE_DRIVER 缺省非 postgres）→ source=files。
	cfg, err := LoadConfig(getenv)
	if err != nil {
		t.Fatalf("DATA_DIR + credential secret must load: %v", err)
	}
	if cfg.Store.Mode != StoreSQLite || cfg.InputSource != "files" {
		t.Fatalf("sqlite 缺省臂: mode=%v source=%q", cfg.Store.Mode, cfg.InputSource)
	}
	if expected := filepath.Join(dataDir, "account-health.sqlite3"); cfg.Store.DatabasePath != expected {
		t.Fatalf("store path = %q, want %q", cfg.Store.DatabasePath, expected)
	}
	if expected := filepath.Join(dataDir, "account-health-input"); cfg.InputDirectory != expected {
		t.Fatalf("input dir = %q, want %q", cfg.InputDirectory, expected)
	}
	if _, statErr := os.Stat(cfg.InputDirectory); statErr != nil {
		t.Fatalf("派生 input 目录必须被创建: %v", statErr)
	}
	firstKey, keyOK := cfg.InputKeys["runtime-v1"]
	if !keyOK || len(firstKey) != 48 {
		t.Fatalf("缺省签名 key 必须是 48 字节: len=%d ok=%v", len(firstKey), keyOK)
	}
	keyFile := filepath.Join(dataDir, "account-health-input.key")
	raw, readErr := os.ReadFile(keyFile)
	if readErr != nil {
		t.Fatalf("签名 key 文件必须被创建: %v", readErr)
	}
	decoded, decodeErr := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if decodeErr != nil || len(decoded) != 48 {
		t.Fatalf("key 文件内容必须是 48 字节 base64rawurl: %v len=%d", decodeErr, len(decoded))
	}
	if info, statErr := os.Stat(keyFile); statErr == nil && runtime.GOOS != "windows" {
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("key 文件权限必须为 0600: %v", info.Mode().Perm())
		}
	}

	// 臂二（重启一致性）：二次 LoadConfig 读取同一 key 文件。
	reloaded, err := LoadConfig(getenv)
	if err != nil {
		t.Fatalf("second LoadConfig: %v", err)
	}
	secondKey := reloaded.InputKeys["runtime-v1"]
	if string(secondKey) != string(firstKey) {
		t.Fatal("二次装载必须复用同一签名 key（可重启一致）")
	}

	// 臂三：显式 env 始终优先于 key 文件。
	explicit := map[string]string{
		"JUHE_AI_DATA_DIR":                         dataDir,
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "zero-config-secret",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": strings.Repeat("A", 43),
	}
	withExplicit, err := LoadConfig(func(name string) string { return explicit[name] })
	if err != nil {
		t.Fatalf("explicit signing key must load: %v", err)
	}
	decodedExplicit, decodeErr := base64.RawURLEncoding.DecodeString(strings.Repeat("A", 43))
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if got := string(withExplicit.InputKeys["runtime-v1"]); got != string(decodedExplicit) {
		t.Fatalf("显式签名 key 必须优先: %q", got)
	}

	// 臂四：postgres 缺省臂——DATABASE_DRIVER=postgres → store/source 均
	// postgres，store URL 回退 JUHE_AI_POSTGRES_URL。
	pg := map[string]string{
		"JUHE_AI_DATA_DIR":                          dataDir,
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET":  "zero-config-secret",
		"JUHE_AI_DATABASE_DRIVER":                   "postgres",
		"JUHE_AI_POSTGRES_URL":                      "postgres://shared/juhe",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL": "postgres://shared/business",
	}
	pgCfg, err := LoadConfig(func(name string) string { return pg[name] })
	if err != nil {
		t.Fatalf("postgres 缺省臂必须装载: %v", err)
	}
	if pgCfg.Store.Mode != StorePostgres || pgCfg.InputSource != "postgres" {
		t.Fatalf("postgres 缺省臂: mode=%v source=%q", pgCfg.Store.Mode, pgCfg.InputSource)
	}
	if pgCfg.Store.PostgresURL != "postgres://shared/juhe" {
		t.Fatalf("store URL 必须回退 JUHE_AI_POSTGRES_URL: %q", pgCfg.Store.PostgresURL)
	}
}
