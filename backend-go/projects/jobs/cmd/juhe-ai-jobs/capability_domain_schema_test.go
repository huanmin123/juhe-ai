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
// 库（正确形状）必须通过；错误形状必须 fail-fast。
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
