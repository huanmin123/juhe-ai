package gometrics

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Config tests migrated from the former jobs gometricsstore suite; the shared
// LoadConfig only adds the caller's default role parameter.

// 2026-09-27 起出厂默认开启：STORE 与 DRIVER 都未配置时跟随 sqlite（零配置
// standalone 默认），路径派生 <数据根>/go-runtime-metrics.sqlite3，其余部署
// 参数保留默认值。
func TestLoadConfigDefaultsToSQLiteDerivedPath(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" }, "jobs")
	if err != nil || !cfg.Enabled || cfg.Store != DialectSQLite {
		t.Fatalf("zero-config must default to enabled sqlite store: cfg=%+v err=%v", cfg, err)
	}
	if cfg.DatabasePath != filepath.Join("./data", "go-runtime-metrics.sqlite3") {
		t.Fatalf("derived path must be <data root>/go-runtime-metrics.sqlite3: %q", cfg.DatabasePath)
	}
	if cfg.Interval != defaultInterval || cfg.RetentionDays != defaultRetentionDays || cfg.Service != "juhe-ai" || cfg.Role != "jobs" {
		t.Fatalf("deployment defaults must be kept: cfg=%+v err=%v", cfg, err)
	}
}

func TestLoadConfigAppliesCallerDefaultRole(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" }, "gateway")
	if err != nil || cfg.Role != "gateway" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

// STORE=disabled 是唯一关闭路径：即使 DRIVER=postgres 且主库 URL 可用也不采样。
func TestLoadConfigExplicitDisabledTurnsOff(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_DATABASE_DRIVER":          "postgres",
		"JUHE_AI_POSTGRES_URL":             "postgres://main.invalid/db",
		"JUHE_AI_GO_RUNTIME_METRICS_STORE": "disabled",
	}
	cfg, err := LoadConfig(func(key string) string { return values[key] }, "gateway")
	if err != nil || cfg.Enabled {
		t.Fatalf("explicit disabled must turn sampling off: cfg=%+v err=%v", cfg, err)
	}
}

func TestLoadConfigSQLite(t *testing.T) {
	// 2026-09-27 起 ROLE env 移除：设置 JUHE_AI_GO_RUNTIME_METRICS_ROLE 也
	// 不生效，role 恒为调用方 defaultRole。
	values := map[string]string{"JUHE_AI_GO_RUNTIME_METRICS_STORE": "sqlite", "JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH": "metrics.sqlite3", "JUHE_AI_GO_RUNTIME_METRICS_INTERVAL": "20s", "JUHE_AI_GO_RUNTIME_METRICS_RETENTION_DAYS": "45", "JUHE_AI_GO_RUNTIME_METRICS_ROLE": "replica-1"}
	cfg, err := LoadConfig(func(key string) string { return values[key] }, "gateway")
	if err != nil || !cfg.Enabled || cfg.Interval != 20*time.Second || cfg.RetentionDays != 45 || cfg.Store != DialectSQLite || cfg.DatabasePath != "metrics.sqlite3" || cfg.Role != "gateway" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestLoadConfigRoleEnvRemoved(t *testing.T) {
	// ROLE env 残留值必须被忽略（该 env 被两进程共享转发，覆盖会导致
	// gateway 冒用 jobs 角色采样、读侧固定双角色查询失真）。
	values := map[string]string{
		"JUHE_AI_GO_RUNTIME_METRICS_STORE":        "postgres",
		"JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL": "postgres://roles.invalid/db",
		"JUHE_AI_GO_RUNTIME_METRICS_ROLE":         "jobs",
	}
	cfg, err := LoadConfig(func(key string) string { return values[key] }, "gateway")
	if err != nil || cfg.Role != "gateway" {
		t.Fatalf("ROLE env must be ignored (role stays defaultRole): cfg=%+v err=%v", cfg, err)
	}
}

func TestLoadConfigPostgresRequiresURL(t *testing.T) {
	// 专用 URL 与共享 POSTGRES_URL 都为空才报错，错误消息点名两个变量名。
	_, err := LoadConfig(func(key string) string {
		if key == "JUHE_AI_GO_RUNTIME_METRICS_STORE" {
			return "postgres"
		}
		return ""
	}, "jobs")
	if err == nil {
		t.Fatal("expected missing postgres URL error")
	}
	for _, name := range []string{"JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL", "JUHE_AI_POSTGRES_URL"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error must name %s: %v", name, err)
		}
	}
}

func TestLoadConfigPostgresURLFallbackToSharedURL(t *testing.T) {
	// 专用 URL 为空时回退共享 JUHE_AI_POSTGRES_URL。
	values := map[string]string{
		"JUHE_AI_GO_RUNTIME_METRICS_STORE": "postgres",
		"JUHE_AI_POSTGRES_URL":             "postgres://shared.invalid/db",
	}
	cfg, err := LoadConfig(func(key string) string { return values[key] }, "gateway")
	if err != nil || !cfg.Enabled || cfg.Store != DialectPostgres || cfg.PostgresURL != "postgres://shared.invalid/db" {
		t.Fatalf("shared POSTGRES_URL fallback must apply: cfg=%+v err=%v", cfg, err)
	}
	// 专用 URL 优先于共享 URL。
	values["JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL"] = "postgres://dedicated.invalid/db"
	cfg, err = LoadConfig(func(key string) string { return values[key] }, "gateway")
	if err != nil || cfg.PostgresURL != "postgres://dedicated.invalid/db" {
		t.Fatalf("dedicated URL must win over shared fallback: cfg=%+v err=%v", cfg, err)
	}
}

// STORE 未配置时跟随 JUHE_AI_DATABASE_DRIVER：DRIVER=postgres → postgres，
// 专用 URL 为空时回退共享 JUHE_AI_POSTGRES_URL；两个 URL 都为空才报错。
func TestLoadConfigPostgresDriverFollowsMainStore(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_DATABASE_DRIVER": "postgres",
		"JUHE_AI_POSTGRES_URL":    "postgres://main.invalid/db",
	}
	cfg, err := LoadConfig(func(key string) string { return values[key] }, "jobs")
	if err != nil || !cfg.Enabled || cfg.Store != DialectPostgres || cfg.PostgresURL != "postgres://main.invalid/db" {
		t.Fatalf("driver=postgres must follow main store with shared URL fallback: cfg=%+v err=%v", cfg, err)
	}
	_, err = LoadConfig(func(key string) string {
		if key == "JUHE_AI_DATABASE_DRIVER" {
			return "postgres"
		}
		return ""
	}, "jobs")
	if err == nil {
		t.Fatal("expected missing postgres URL error for driver-derived postgres store")
	}
	for _, name := range []string{"JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL", "JUHE_AI_POSTGRES_URL"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error must name %s: %v", name, err)
		}
	}
}

// DRIVER=sqlite 且无任何路径 env：派生 <DATA_DIR 或 ./data>/go-runtime-metrics.sqlite3
// （与 jobs datadir.Root 语义一致）；未知 DRIVER 值同样回落 sqlite。
func TestLoadConfigSQLiteDriverDerivedPath(t *testing.T) {
	values := map[string]string{"JUHE_AI_DATABASE_DRIVER": "sqlite", "JUHE_AI_DATA_DIR": "F:/tmp/metrics-data-root"}
	cfg, err := LoadConfig(func(key string) string { return values[key] }, "gateway")
	if err != nil || !cfg.Enabled || cfg.Store != DialectSQLite || cfg.DatabasePath != filepath.Join("F:/tmp/metrics-data-root", "go-runtime-metrics.sqlite3") {
		t.Fatalf("DATA_DIR must seed the derived sqlite path: cfg=%+v err=%v", cfg, err)
	}
	cfg, err = LoadConfig(func(key string) string {
		if key == "JUHE_AI_DATABASE_DRIVER" {
			return "sqlite"
		}
		return ""
	}, "gateway")
	if err != nil || !cfg.Enabled || cfg.DatabasePath != filepath.Join("./data", "go-runtime-metrics.sqlite3") {
		t.Fatalf("unset DATA_DIR must fall back to ./data: cfg=%+v err=%v", cfg, err)
	}
	values = map[string]string{"JUHE_AI_DATABASE_DRIVER": "banana"}
	cfg, err = LoadConfig(func(key string) string { return values[key] }, "gateway")
	if err != nil || !cfg.Enabled || cfg.Store != DialectSQLite {
		t.Fatalf("unknown driver value must fall back to sqlite: cfg=%+v err=%v", cfg, err)
	}
}

// 显式 STORE 优先于 driver 跟随：显式 postgres 覆盖 DRIVER=sqlite，显式
// sqlite 覆盖 DRIVER=postgres 且显式路径仍优先于派生。
func TestLoadConfigExplicitStoreOverridesDriver(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_DATABASE_DRIVER":                 "sqlite",
		"JUHE_AI_GO_RUNTIME_METRICS_STORE":        "postgres",
		"JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL": "postgres://explicit.invalid/db",
	}
	cfg, err := LoadConfig(func(key string) string { return values[key] }, "gateway")
	if err != nil || !cfg.Enabled || cfg.Store != DialectPostgres || cfg.PostgresURL != "postgres://explicit.invalid/db" {
		t.Fatalf("explicit STORE=postgres must win over DRIVER=sqlite: cfg=%+v err=%v", cfg, err)
	}
	values = map[string]string{
		"JUHE_AI_DATABASE_DRIVER":                  "postgres",
		"JUHE_AI_GO_RUNTIME_METRICS_STORE":         "sqlite",
		"JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH": "explicit.sqlite3",
	}
	cfg, err = LoadConfig(func(key string) string { return values[key] }, "gateway")
	if err != nil || !cfg.Enabled || cfg.Store != DialectSQLite || cfg.DatabasePath != "explicit.sqlite3" {
		t.Fatalf("explicit STORE=sqlite must win over DRIVER=postgres and keep the explicit path: cfg=%+v err=%v", cfg, err)
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

// 2026-09-21 起 JUHE_AI_GO_RUNTIME_METRICS_ENABLED 开关移除：采样与否由
// 存储参数决定，残留开关值必须被忽略。
func TestLoadConfigEnabledEnvRemoved(t *testing.T) {
	values := map[string]string{"JUHE_AI_GO_RUNTIME_METRICS_STORE": "sqlite", "JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH": "m.sqlite3", "JUHE_AI_GO_RUNTIME_METRICS_ENABLED": "false"}
	cfg, err := LoadConfig(func(key string) string { return values[key] }, "jobs")
	if err != nil || !cfg.Enabled {
		t.Fatalf("ENABLED env must be ignored (always-on by store): cfg=%+v err=%v", cfg, err)
	}
}

func TestOpenStoreDisabledReturnsNil(t *testing.T) {
	// 2026-09-27 起默认开启（跟随主存储），关闭必须显式 STORE=disabled。
	cfg, err := LoadConfig(func(key string) string {
		if key == "JUHE_AI_GO_RUNTIME_METRICS_STORE" {
			return "disabled"
		}
		return ""
	}, "jobs")
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
