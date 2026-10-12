// accounts.capability_domain 启动只读形状校验（AI 账户能力域拆分 M1）测试：
// SQLite 方言走形状矩阵（通过/缺列/缺表/类型错/可空/默认值错/缺 CHECK/收窄
// CHECK）并与 maintenance bootstrap ensure 产出锁同形；PostgreSQL 方言复用
// w1g2 覆盖子库（dev env 缺失或不可达时跳过），在独立 scratch schema 上做同
// 矩阵验证，不触碰 juhe_business 数据。
package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	capdomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
	_ "modernc.org/sqlite"
)

// capabilityDomainCorrectColumn 是与 maintenance DDL 同构的正确列声明。
const capabilityDomainCorrectColumn = `capability_domain TEXT NOT NULL DEFAULT 'chat' CHECK (capability_domain IN ('chat','video','audio','image'))`

func openCapabilityDomainSQLiteFixture(t *testing.T, createTable string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:capdom-check-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if createTable != "" {
		if _, err := db.Exec(createTable); err != nil {
			t.Fatalf("fixture DDL: %v: %v", createTable, err)
		}
	}
	return db
}

func TestCapabilityDomainSchemaSQLitePasses(t *testing.T) {
	db := openCapabilityDomainSQLiteFixture(t, `CREATE TABLE accounts (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		deleted_at TEXT,
		`+capabilityDomainCorrectColumn+`
	)`)
	if err := checkAccountsCapabilityDomainSchema(context.Background(), db, false); err != nil {
		t.Fatalf("标准形状必须通过: %v", err)
	}
}

func TestCapabilityDomainSchemaSQLiteFailureArms(t *testing.T) {
	scenarios := []struct {
		name    string
		create  string
		wantErr string
	}{
		{"missing-table", "", "列缺失"},
		{"missing-column", `CREATE TABLE accounts (id TEXT PRIMARY KEY, name TEXT NOT NULL)`, "列缺失"},
		{"wrong-type", `CREATE TABLE accounts (id TEXT PRIMARY KEY, capability_domain BIGINT NOT NULL DEFAULT 0)`, "期望 TEXT"},
		{"nullable", `CREATE TABLE accounts (id TEXT PRIMARY KEY, capability_domain TEXT)`, "期望 NOT NULL"},
		{"wrong-default", `CREATE TABLE accounts (id TEXT PRIMARY KEY, capability_domain TEXT NOT NULL DEFAULT 'video')`, "期望 'chat'"},
		{"missing-check", `CREATE TABLE accounts (id TEXT PRIMARY KEY, capability_domain TEXT NOT NULL DEFAULT 'chat')`, "四值 CHECK"},
		{"narrowed-check", `CREATE TABLE accounts (id TEXT PRIMARY KEY, capability_domain TEXT NOT NULL DEFAULT 'chat' CHECK (capability_domain IN ('chat','video')))`, "四值 CHECK"},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			db := openCapabilityDomainSQLiteFixture(t, scenario.create)
			err := checkAccountsCapabilityDomainSchema(context.Background(), db, false)
			if err == nil {
				t.Fatalf("形状不符必须 fail-fast")
			}
			if !strings.Contains(err.Error(), scenario.wantErr) || !strings.Contains(err.Error(), capabilityDomainExpectedShape) {
				t.Fatalf("错误应包含 %q 与期望形状，got: %v", scenario.wantErr, err)
			}
		})
	}
}

// TestCapabilityDomainSchemaSQLiteMatchesMaintenanceBootstrap 锁定启动校验与
// maintenance bootstrap ensure 的建表形状一致：同一次 ensure 产出的库必须通过
// 校验（防两侧 DDL 漂移）。
func TestCapabilityDomainSchemaSQLiteMatchesMaintenanceBootstrap(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "business.sqlite3")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := bootstrap.EnsureSQLiteSchema(context.Background(), bootstrap.SQLiteSchemaBusiness, db); err != nil {
		t.Fatalf("bootstrap ensure: %v", err)
	}
	if err := checkAccountsCapabilityDomainSchema(context.Background(), db, false); err != nil {
		t.Fatalf("bootstrap ensure 产出的业务库必须通过启动校验: %v", err)
	}
}

// TestCapabilityDomainSchemaPostgresArms 在 dev PG 上开一次性子库，于独立
// scratch schema 验证 PG 方言四值形状判定（含真实 PG 的默认值渲染
// `'chat'::text`）；dev env 缺失或 PG 管理口不可达时跳过。不依赖也不触碰
// w1g2 覆盖子库与 juhe_business 数据。
func TestCapabilityDomainSchemaPostgresArms(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过真实 PG")
	}
	env := w1pgEnvFile(t)
	host := env["DEV_POSTGRES_HOST"]
	directPort := env["DEV_POSTGRES_DIRECT_PORT"]
	adminUser := env["DEV_POSTGRES_ADMIN_USERNAME"]
	adminPass := env["DEV_POSTGRES_ADMIN_PASSWORD"]
	appUser := env["DEV_POSTGRES_APP_USERNAME"]
	appURL := env["JUHE_AI_POSTGRES_URL"]
	if host == "" || directPort == "" || adminUser == "" || appURL == "" {
		t.Skipf("dev env 缺少 PG 键（跳过 PG 形状校验测试）")
	}
	adminDSN := fmt.Sprintf("postgres://%s:%s@%s:%s/postgres?sslmode=disable", adminUser, adminPass, host, directPort)
	adminDB, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatalf("打开 dev PG 管理口失败: %v", err)
	}
	defer adminDB.Close()
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pingCancel()
	if err := adminDB.PingContext(pingCtx); err != nil {
		t.Skipf("dev PG 管理口不可达（跳过）: %v", err)
	}

	database := "juhe_ai_sub2api_dev_capdomcheck"
	// dropDatabase 自管短连接：测试体内的 defer 关闭顺序先于 t.Cleanup，
	// 复用外层句柄会在清理时已是关闭态。
	dropDatabase := func() {
		admin, err := sql.Open("pgx", adminDSN)
		if err != nil {
			t.Logf("清理一次性子库打开管理连接失败: %v", err)
			return
		}
		defer admin.Close()
		if _, err := admin.Exec(fmt.Sprintf(
			`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()`, database)); err != nil {
			t.Logf("终止残留会话失败: %v", err)
		}
		if _, err := admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, database)); err != nil {
			t.Logf("清理一次性子库失败: %v", err)
		}
	}
	dropDatabase()
	owner := appUser
	if owner == "" {
		owner = adminUser
	}
	if _, err := adminDB.Exec(fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, database, owner)); err != nil {
		t.Fatalf("创建一次性子库失败: %v", err)
	}
	t.Cleanup(dropDatabase)

	sep := strings.LastIndex(appURL, "/")
	db, err := sql.Open("pgx", appURL[:sep+1]+database)
	if err != nil {
		t.Fatalf("打开一次性子库失败: %v", err)
	}
	defer db.Close()

	schema := "capdom_probe"

	scenarios := []struct {
		name    string
		column  string
		wantErr string
	}{
		{"pass", capabilityDomainCorrectColumn, ""},
		{"missing-column", "", "缺列"},
		{"wrong-type", `capability_domain bigint NOT NULL DEFAULT 0`, "期望 text"},
		{"nullable", `capability_domain text`, "期望 NOT NULL"},
		{"wrong-default", `capability_domain text NOT NULL DEFAULT 'video'`, "期望前缀 'chat'"},
		{"missing-check", `capability_domain text NOT NULL DEFAULT 'chat'`, "CHECK"},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			if _, err := db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, schema)); err != nil {
				t.Fatalf("重置 scratch schema: %v", err)
			}
			columnSQL := ""
			if scenario.column != "" {
				columnSQL = ",\n\t" + scenario.column
			}
			if _, err := db.Exec(fmt.Sprintf(`CREATE SCHEMA %s; CREATE TABLE %s.accounts (id text PRIMARY KEY%s)`, schema, schema, columnSQL)); err != nil {
				t.Fatalf("建 scratch accounts: %v", err)
			}
			err := checkAccountsCapabilityDomainSchemaPostgres(context.Background(), db, schema)
			if scenario.wantErr == "" {
				if err != nil {
					t.Fatalf("标准形状必须通过: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("形状不符必须 fail-fast")
			}
			if !strings.Contains(err.Error(), scenario.wantErr) {
				t.Fatalf("错误应包含 %q，got: %v", scenario.wantErr, err)
			}
		})
	}
}

// TestCapabilityDomainStageRecordGateArms 覆盖 M2 启动阶段门禁三失败臂
// （AI 账户能力域拆分 v7）：记录缺失 / 仅 m1_complete / ruleVersion 不符
// （含 global_settings 表缺失的读取失败臂）都必须 fail-fast，且错误信息携带
// 迁移命令指引；已封存 m2_finalized 放行。
func TestCapabilityDomainStageRecordGateArms(t *testing.T) {
	const migrationCommandNeedle = "--migrate-account-capability-domain"
	openGateFixture := func(t *testing.T, record string) *sql.DB {
		t.Helper()
		db := openCapabilityDomainSQLiteFixture(t, `CREATE TABLE accounts (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			deleted_at TEXT,
			`+capabilityDomainCorrectColumn+`
		)`)
		if _, err := db.Exec(`CREATE TABLE global_settings (
			key TEXT PRIMARY KEY,
			value_json TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`); err != nil {
			t.Fatalf("seed global_settings ddl: %v", err)
		}
		if record != "" {
			if _, err := db.Exec(`INSERT INTO global_settings (key, value_json, updated_at) VALUES (?, ?, '2026-10-12T08:00:00Z')`,
				capdomain.MigrationSettingKey, record); err != nil {
				t.Fatalf("seed stage record: %v", err)
			}
		}
		return db
	}
	scenarios := []struct {
		name        string
		record      string
		wantErrText string
	}{
		{"record-missing", "", "不存在"},
		{"m1-complete", `{"stage":"m1_complete","ruleVersion":1,"updatedAt":"2026-10-12T08:00:00Z"}`, "m1_complete"},
		{"rule-version-mismatch", `{"stage":"m2_finalized","ruleVersion":2,"updatedAt":"2026-10-12T08:00:00Z"}`, "ruleVersion"},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			db := openGateFixture(t, scenario.record)
			err := checkAccountCapabilityDomainStageRecord(context.Background(), db, false)
			if err == nil {
				t.Fatalf("阶段门禁失败臂必须 fail-fast")
			}
			if !strings.Contains(err.Error(), scenario.wantErrText) || !strings.Contains(err.Error(), migrationCommandNeedle) {
				t.Fatalf("错误应包含 %q 与迁移命令指引 %q，got: %v", scenario.wantErrText, migrationCommandNeedle, err)
			}
		})
	}
	t.Run("global-settings-table-missing", func(t *testing.T) {
		db := openCapabilityDomainSQLiteFixture(t, `CREATE TABLE accounts (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			deleted_at TEXT,
			`+capabilityDomainCorrectColumn+`
		)`)
		err := checkAccountCapabilityDomainStageRecord(context.Background(), db, false)
		if err == nil || !strings.Contains(err.Error(), "读取失败") || !strings.Contains(err.Error(), migrationCommandNeedle) {
			t.Fatalf("global_settings 缺失必须报读取失败且携带迁移命令指引，got: %v", err)
		}
	})
	t.Run("m2-finalized-passes", func(t *testing.T) {
		db := openGateFixture(t, `{"stage":"m2_finalized","ruleVersion":1,"updatedAt":"2026-10-12T08:00:00Z"}`)
		if err := checkAccountCapabilityDomainStageRecord(context.Background(), db, false); err != nil {
			t.Fatalf("已封存记录必须放行: %v", err)
		}
	})
}

// TestComposeSystemAPISealsCapabilityDomainStageRecord 验证 compose 夹具路径
// 确实经过全新空库自动封存（AI 账户能力域拆分 v7）：SQLite storage preflight
// 走 maintenance bootstrap.SeedSQLiteBusiness，fresh 业务库在组合根内获得
// m2_finalized 阶段记录，启动阶段门禁因此放行（gateway 启动本身不是迁移
// 执行者，封存发生在 bootstrap 种子路径）。
func TestComposeSystemAPISealsCapabilityDomainStageRecord(t *testing.T) {
	cfg := composeTestConfig(t)
	store := openComposeOperationStore(t)
	createRuntimeLogDataset(t, cfg.RuntimeLogDatabasePath)
	auditConfig, auditProducer, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	defer closeAudit()
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), auditProducer, auditConfig, composeTestOwnerHealth())
	if err != nil {
		t.Fatalf("compose system api: %v", err)
	}
	defer composed.Shutdown()
	recordJSON, err := capabilityDomainStageRecordJSON(context.Background(), composed.DB)
	if err != nil {
		t.Fatalf("read stage record: %v", err)
	}
	record, err := capdomain.ParseRecord(recordJSON)
	if err != nil {
		t.Fatalf("parse stage record: %v", err)
	}
	if record.Stage != capdomain.StageM2Finalized || record.RuleVersion != capdomain.RuleVersion {
		t.Fatalf("compose 夹具库阶段记录应已封存 m2_finalized，got %+v", record)
	}
}

// capabilityDomainStageRecordJSON 读业务库阶段记录原始 JSON（无记录时报错）。
func capabilityDomainStageRecordJSON(ctx context.Context, db *sql.DB) (string, error) {
	var valueJSON string
	err := db.QueryRowContext(ctx, `SELECT value_json FROM global_settings WHERE key = ?`, capdomain.MigrationSettingKey).Scan(&valueJSON)
	if err != nil {
		return "", fmt.Errorf("read capability domain stage record: %w", err)
	}
	return valueJSON, nil
}
