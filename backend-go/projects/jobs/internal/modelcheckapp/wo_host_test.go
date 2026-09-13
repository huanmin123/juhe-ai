package modelcheckapp

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckdurable"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckruntime"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckstore"
	_ "modernc.org/sqlite"
)

// prepareSQLiteBusinessDB 用给定 schema 初始化业务库文件；空 schema 产生一个空库，
// 用于验证装配链路对缺表的 fail-closed 行为。
func prepareSQLiteBusinessDB(t *testing.T, path, schema string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("打开业务库失败: %v", err)
	}
	defer db.Close()
	if schema == "" {
		if err := db.Ping(); err != nil {
			t.Fatalf("初始化空业务库失败: %v", err)
		}
		return
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("初始化业务库 schema 失败: %v", err)
	}
}

// sqliteBusinessFixtureSchema 覆盖 OpenHost 装配链路上的三类只读契约：
// 业务候选读取（modelchecksource）、质量策略读取（modelcheckpolicy）和管理会话鉴权（modelcheckauth）。
// 契约查询都是结构性校验（WHERE 0 / LIMIT 0），因此只需要表和列存在，不需要业务数据。
const sqliteBusinessFixtureSchema = `
CREATE TABLE accounts(
  id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, config_revision TEXT NOT NULL,
  status TEXT NOT NULL, schedulable INTEGER NOT NULL, health_check_endpoint_mode TEXT NOT NULL,
  authorization_instance_authorization_id TEXT, authorization_instance_source_account_id TEXT,
  authorization_instance_owner_system_account_id TEXT, name TEXT NOT NULL DEFAULT '',
  provider_code TEXT NOT NULL DEFAULT '', provider_protocol_profile_id TEXT NOT NULL DEFAULT '',
  protocol_code TEXT NOT NULL DEFAULT '', protocol_version TEXT NOT NULL DEFAULT '',
  type TEXT NOT NULL DEFAULT '', credentials_encrypted TEXT NOT NULL DEFAULT '',
  proxy_profile_id TEXT, last_error_code TEXT, account_expires_at TEXT, deleted_at TEXT
);
CREATE TABLE provider_protocol_profiles(id TEXT PRIMARY KEY, enabled INTEGER NOT NULL, base_url TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE group_accounts(system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, account_id TEXT NOT NULL, account_authorization_id TEXT, enabled INTEGER NOT NULL, updated_at TEXT);
CREATE TABLE groups(id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, enabled INTEGER NOT NULL);
CREATE TABLE resource_authorizations(id TEXT PRIMARY KEY, resource_type TEXT, resource_id TEXT, resource_owner_system_account_id TEXT, grantee_system_account_id TEXT, scope TEXT, status TEXT, expires_at TEXT);
CREATE TABLE proxy_profiles(id TEXT PRIMARY KEY, enabled INTEGER, type TEXT, host TEXT, port INTEGER, username TEXT, password_encrypted TEXT);
CREATE TABLE account_supported_models(account_id TEXT NOT NULL, model TEXT NOT NULL);
CREATE TABLE account_model_mappings(account_id TEXT NOT NULL, source_model TEXT NOT NULL);
CREATE TABLE model_quality_policies(system_account_id TEXT PRIMARY KEY, revision TEXT NOT NULL, profile TEXT NOT NULL, manual_enforcement_enabled INTEGER NOT NULL, penalty_threshold INTEGER NOT NULL, penalty_action TEXT NOT NULL, recovery_interval_minutes INTEGER NOT NULL);
CREATE TABLE system_sessions(id TEXT PRIMARY KEY);
CREATE TABLE system_accounts(id TEXT PRIMARY KEY);
`

// validSQLiteConfig 返回一个可成功装配的 SQLite 模式配置：三个库文件各自独立，
// Deadline/ProbeSetVersion 满足 modelcheckcommand.New 的最低校验要求。
func validSQLiteConfig(t *testing.T) modelcheckruntime.RuntimeConfig {
	t.Helper()
	dir := t.TempDir()
	return modelcheckruntime.RuntimeConfig{
		Enabled:              true,
		StoreMode:            "sqlite",
		JobsDatabasePath:     filepath.Join(dir, "jobs.sqlite3"),
		DatasetDatabasePath:  filepath.Join(dir, "dataset.sqlite3"),
		BusinessDatabasePath: filepath.Join(dir, "business.sqlite3"),
		CredentialSecret:     "wo-credential-secret",
		IdentitySecret:       "wo-identity-secret",
		ProbeSetVersion:      "wo-probe-v1",
		Deadline:             time.Minute,
		Heartbeat:            time.Second,
	}
}

func TestOpenHostDisabledReturnsInactiveHost(t *testing.T) {
	// 契约：J3b 未启用时 jobs 侧必须返回未就绪的空壳 Host，不打开任何数据库连接。
	host, err := OpenHost(context.Background(), modelcheckruntime.RuntimeConfig{Enabled: false})
	if err != nil {
		t.Fatalf("禁用配置下 OpenHost 不应报错: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	if host.Ready() {
		t.Fatalf("禁用配置下 Host 不应处于就绪状态")
	}
	if host.Service != nil || host.Handler != nil || host.Business != nil || host.Durable != nil || host.Dataset != nil {
		t.Fatalf("禁用配置下 Host 不应装配任何组件: %#v", host)
	}
	if host.Config.Enabled {
		t.Fatalf("Config 应原样保留入参的禁用状态")
	}
	if err := host.Close(); err != nil {
		t.Fatalf("未装配组件的 Close 应返回 nil: %v", err)
	}
}

func TestNilHostReadyAndClose(t *testing.T) {
	// 契约：nil 接收者必须安全，Ready 返回 false，Close 返回 nil。
	var host *Host
	if host.Ready() {
		t.Fatalf("nil Host 不应处于就绪状态")
	}
	if err := host.Close(); err != nil {
		t.Fatalf("nil Host 的 Close 应返回 nil: %v", err)
	}
}

func TestOpenHostSQLiteStopsAtTargetResolutionAssertion(t *testing.T) {
	// 行为存疑：modelcheckexecutor.TargetResolver 是函数类型（不是接口），
	// OpenHost 用它对 *SQLiteReader 结构体指针做类型断言必然失败，因此
	// 即使全部 schema 就绪，装配也会在该断言处 fail-closed，后续的
	// Service/Handler 组装代码当前不可达（gateway 侧同名装配需单独核对）。
	// 此处按当前实际行为断言；若上游把断言目标改为接口，本用例需同步更新。
	cfg := validSQLiteConfig(t)
	prepareSQLiteBusinessDB(t, cfg.BusinessDatabasePath, sqliteBusinessFixtureSchema)
	host, err := OpenHost(context.Background(), cfg)
	if err == nil {
		_ = host.Close()
		t.Fatalf("按当前实现，类型断言必然失败，OpenHost 应报错")
	}
	if !strings.Contains(err.Error(), "J3b business reader does not implement target resolution") {
		t.Fatalf("错误应指向目标解析断言失败，实际: %v", err)
	}
	if host != nil {
		t.Fatalf("失败路径应返回 nil Host")
	}
}

func TestHostCloseClosesAllComponents(t *testing.T) {
	// 契约：Close 按业务库 -> dataset -> durable 的顺序关闭全部组件，
	// 且对 nil 组件和重复关闭保持幂等。
	dir := t.TempDir()
	business, err := sql.Open("sqlite", filepath.Join(dir, "business.sqlite3"))
	if err != nil {
		t.Fatalf("打开业务库失败: %v", err)
	}
	durable, err := modelcheckdurable.OpenSQLite(filepath.Join(dir, "durable.sqlite3"))
	if err != nil {
		t.Fatalf("打开 durable 库失败: %v", err)
	}
	dataset, err := modelcheckstore.OpenSQLite(filepath.Join(dir, "dataset.sqlite3"))
	if err != nil {
		t.Fatalf("打开 dataset 库失败: %v", err)
	}
	host := &Host{Business: business, Dataset: dataset, Durable: durable}
	if err := host.Close(); err != nil {
		t.Fatalf("组件齐全时 Close 不应报错: %v", err)
	}
	// 全部组件已关闭后再次 Close 仍应返回 nil（database/sql 的 Close 幂等）。
	if err := host.Close(); err != nil {
		t.Fatalf("重复 Close 不应报错: %v", err)
	}
	// 只有部分组件时也应安全关闭。
	partial := &Host{Durable: durable}
	if err := partial.Close(); err != nil {
		t.Fatalf("部分组件 Close 不应报错: %v", err)
	}
}

func TestOpenHostSQLiteRequiresJobsDatabasePath(t *testing.T) {
	// 契约：durable 库路径为空时 fail-closed，不产生半装配的 Host。
	cfg := validSQLiteConfig(t)
	cfg.JobsDatabasePath = ""
	host, err := OpenHost(context.Background(), cfg)
	if err == nil {
		_ = host.Close()
		t.Fatalf("durable 路径为空时 OpenHost 应报错")
	}
	if !strings.Contains(err.Error(), "path is required") {
		t.Fatalf("错误应指向 durable 路径缺失，实际: %v", err)
	}
	if host != nil {
		t.Fatalf("失败路径应返回 nil Host")
	}
}

func TestOpenHostSQLiteRequiresDatasetDatabasePath(t *testing.T) {
	// 契约：dataset 库路径为空时报错，且必须回滚已打开的 durable 连接。
	cfg := validSQLiteConfig(t)
	cfg.DatasetDatabasePath = ""
	host, err := OpenHost(context.Background(), cfg)
	if err == nil {
		_ = host.Close()
		t.Fatalf("dataset 路径为空时 OpenHost 应报错")
	}
	if !strings.Contains(err.Error(), "path is required") {
		t.Fatalf("错误应指向 dataset 路径缺失，实际: %v", err)
	}
	if host != nil {
		t.Fatalf("失败路径应返回 nil Host")
	}
}

func TestOpenHostSQLiteFailsWhenBusinessSchemaMissing(t *testing.T) {
	// 契约：业务库缺表时装配必须失败，错误应指向业务读取器契约校验。
	cfg := validSQLiteConfig(t)
	prepareSQLiteBusinessDB(t, cfg.BusinessDatabasePath, ``)
	host, err := OpenHost(context.Background(), cfg)
	if err == nil {
		_ = host.Close()
		t.Fatalf("业务库缺表时 OpenHost 应报错")
	}
	if !strings.Contains(err.Error(), "verify J3b business reader contract") {
		t.Fatalf("错误应指向业务读取器契约，实际: %v", err)
	}
	if host != nil {
		t.Fatalf("失败路径应返回 nil Host")
	}
}

// readerOnlyFixtureSchema 满足 modelchecksource 契约查询的列需求，但不包含
// 策略表和会话表，用于逐级验证策略/鉴权契约的 fail-closed 行为。
const readerOnlyFixtureSchema = `
CREATE TABLE accounts(
  id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, config_revision TEXT NOT NULL,
  status TEXT NOT NULL, schedulable INTEGER NOT NULL, health_check_endpoint_mode TEXT NOT NULL,
  authorization_instance_authorization_id TEXT, authorization_instance_source_account_id TEXT,
  authorization_instance_owner_system_account_id TEXT, name TEXT NOT NULL DEFAULT '',
  provider_code TEXT NOT NULL DEFAULT '', provider_protocol_profile_id TEXT NOT NULL DEFAULT '',
  protocol_code TEXT NOT NULL DEFAULT '', protocol_version TEXT NOT NULL DEFAULT '',
  type TEXT NOT NULL DEFAULT '', credentials_encrypted TEXT NOT NULL DEFAULT '',
  proxy_profile_id TEXT, last_error_code TEXT, account_expires_at TEXT, deleted_at TEXT
);
CREATE TABLE provider_protocol_profiles(id TEXT PRIMARY KEY, enabled INTEGER NOT NULL, base_url TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE group_accounts(system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, account_id TEXT NOT NULL, account_authorization_id TEXT, enabled INTEGER NOT NULL, updated_at TEXT);
CREATE TABLE groups(id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, enabled INTEGER NOT NULL);
CREATE TABLE resource_authorizations(id TEXT PRIMARY KEY, resource_type TEXT, resource_id TEXT, resource_owner_system_account_id TEXT, grantee_system_account_id TEXT, scope TEXT, status TEXT, expires_at TEXT);
CREATE TABLE proxy_profiles(id TEXT PRIMARY KEY, enabled INTEGER, type TEXT, host TEXT, port INTEGER, username TEXT, password_encrypted TEXT);
CREATE TABLE account_supported_models(account_id TEXT NOT NULL, model TEXT NOT NULL);
CREATE TABLE account_model_mappings(account_id TEXT NOT NULL, source_model TEXT NOT NULL);
`

// policyOnlyFixtureSchema 在 reader 契约之上补充策略表，用于验证鉴权契约失败路径。
const policyOnlyFixtureSchema = readerOnlyFixtureSchema + `
CREATE TABLE model_quality_policies(system_account_id TEXT PRIMARY KEY, revision TEXT NOT NULL, profile TEXT NOT NULL, manual_enforcement_enabled INTEGER NOT NULL, penalty_threshold INTEGER NOT NULL, penalty_action TEXT NOT NULL, recovery_interval_minutes INTEGER NOT NULL);
`

func TestOpenHostSQLiteFailsWhenPolicySchemaMissing(t *testing.T) {
	// 契约：model_quality_policies 缺失时策略契约校验失败。
	cfg := validSQLiteConfig(t)
	prepareSQLiteBusinessDB(t, cfg.BusinessDatabasePath, readerOnlyFixtureSchema)
	host, err := OpenHost(context.Background(), cfg)
	if err == nil {
		_ = host.Close()
		t.Fatalf("策略表缺失时 OpenHost 应报错")
	}
	if !strings.Contains(err.Error(), "verify J3b policy contract") {
		t.Fatalf("错误应指向策略契约，实际: %v", err)
	}
	if host != nil {
		t.Fatalf("失败路径应返回 nil Host")
	}
}

func TestOpenHostSQLiteFailsWhenAuthSchemaMissing(t *testing.T) {
	// 契约：管理会话表缺失时鉴权契约校验失败，监听器不允许绑定。
	cfg := validSQLiteConfig(t)
	prepareSQLiteBusinessDB(t, cfg.BusinessDatabasePath, policyOnlyFixtureSchema)
	host, err := OpenHost(context.Background(), cfg)
	if err == nil {
		_ = host.Close()
		t.Fatalf("会话表缺失时 OpenHost 应报错")
	}
	if !strings.Contains(err.Error(), "verify J3b authentication contract") {
		t.Fatalf("错误应指向鉴权契约，实际: %v", err)
	}
	if host != nil {
		t.Fatalf("失败路径应返回 nil Host")
	}
}

func TestOpenHostPostgresFailsWhenSchemaUnreachable(t *testing.T) {
	// 契约：PostgreSQL 模式在 EnsureSchema 阶段做连通性校验，连不上必须 fail-closed。
	// 使用本机必然拒绝连接的端口，避免依赖真实数据库。
	cfg := validSQLiteConfig(t)
	cfg.StoreMode = "postgres"
	cfg.JobsPostgresURL = "postgres://juhe:secret@127.0.0.1:1/juhe_jobs?connect_timeout=1"
	cfg.BusinessPostgresURL = "postgres://juhe:secret@127.0.0.1:1/juhe_business?connect_timeout=1"
	host, err := OpenHost(context.Background(), cfg)
	if err == nil {
		_ = host.Close()
		t.Fatalf("PostgreSQL 不可达时 OpenHost 应报错")
	}
	if !strings.Contains(err.Error(), "verify J3b durable schema") {
		t.Fatalf("错误应指向 durable schema 校验，实际: %v", err)
	}
	if host != nil {
		t.Fatalf("失败路径应返回 nil Host")
	}
}
