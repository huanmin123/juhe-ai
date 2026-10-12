// accounts.capability_domain 启动只读形状校验（AI 账户能力域拆分 M1）jobs
// 侧测试：SQLite 方言走形状矩阵（通过/缺列/缺表/类型错/可空/默认值错/缺
// CHECK）与 AtStartup 包装；PostgreSQL 方言复用 w12c 覆盖库连接串（不可用时
// 跳过），在独立 scratch schema 上验证真实 PG 判定（含默认值 `'chat'::text`
// 渲染），不触碰 juhe_business 数据。
package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestCapabilityDomainSchemaAtStartupSQLite 走 AtStartup 包装：临时目录业务
// 库（正确形状 + 已封存阶段记录）必须通过；错误形状必须 fail-fast。
func TestCapabilityDomainSchemaAtStartupSQLite(t *testing.T) {
	dir := t.TempDir()
	businessPath := filepath.Join(dir, "business.sqlite3")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(businessPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE accounts (
		id TEXT PRIMARY KEY,
		` + capabilityDomainCorrectColumn + `
	)`); err != nil {
		t.Fatal(err)
	}
	// M2 阶段门禁夹具契约：AtStartup 校验现要求已封存的阶段记录。
	w16hSeedCapabilityDomainStageRecord(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checkAccountsCapabilityDomainSchemaAtStartup(context.Background(), workerConfig{Driver: "sqlite", BusinessSQLitePath: businessPath}); err != nil {
		t.Fatalf("标准形状必须通过: %v", err)
	}

	brokenPath := filepath.Join(dir, "broken.sqlite3")
	db, err = sql.Open("sqlite", "file:"+filepath.ToSlash(brokenPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE accounts (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	err = checkAccountsCapabilityDomainSchemaAtStartup(context.Background(), workerConfig{Driver: "sqlite", BusinessSQLitePath: brokenPath})
	if err == nil || !strings.Contains(err.Error(), "列缺失") {
		t.Fatalf("缺列必须 fail-fast，got: %v", err)
	}

	// 业务库文件缺失（F1 store 先于本校验打开，生产不可达；包装层仍须显式报
	// 错而不是 panic）。
	err = checkAccountsCapabilityDomainSchemaAtStartup(context.Background(), workerConfig{Driver: "sqlite", BusinessSQLitePath: filepath.Join(dir, "missing.sqlite3")})
	if err == nil {
		t.Fatalf("业务库缺失必须 fail-fast")
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

// TestCapabilityDomainSchemaPostgresArms 在 w12c 覆盖库上以独立 scratch schema
// 验证 PG 方言判定；覆盖库不可用时跳过。
func TestCapabilityDomainSchemaPostgresArms(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过真实 PG")
	}
	pgURL := w12cPGOverrideURL(t)
	if pgURL == "" {
		t.Skip("w12c: 覆盖库连接串不可用")
	}
	db, err := sql.Open("pgx", pgURL)
	if err != nil {
		t.Skipf("打开覆盖库失败（跳过）: %v", err)
	}
	defer db.Close()
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pingCancel()
	if err := db.PingContext(pingCtx); err != nil {
		t.Skipf("覆盖库不可达（跳过）: %v", err)
	}

	schema := "w12c_capdom_probe"
	// defer db.Close() 先于 t.Cleanup 执行，清理必须走测试体内的显式 DROP。
	defer func() {
		if _, err := db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, schema)); err != nil {
			t.Errorf("清理 scratch schema 失败: %v", err)
		}
	}()

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
