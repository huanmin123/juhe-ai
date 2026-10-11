// accounts.capability_domain 列 schema 测试（AI 账户能力域拆分
// PLAN-20261011T141004000Z §数据变化），沿 expedited_recovery_enabled 测试形态：
//   - PG：建表 DDL 行与存量库 ALTER 语句的文本契约钉死（live PostgreSQL 走
//     opt-in smoke：JUHE_AI_CAPABILITY_DOMAIN_PG_SMOKE_URL 指向一次性隔离库）；
//   - SQLite：fresh DDL 含列且 CHECK 生效；既有库缺列 → 同形加列（存量行落
//     'chat'）；已有列 → 幂等早退；错误形状 → 原样报错不静默修补。

package schema

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// capabilityDomainLegacyDDL 变换当前 business 主 DDL：kind 为 "missing" 时
// 删除整行列声明（pre-column 库），为 "broken" 时替换为无约束形状。
func capabilityDomainLegacyDDL(t *testing.T, kind string) string {
	t.Helper()
	const fullLine = "      capability_domain TEXT NOT NULL DEFAULT 'chat' CHECK (capability_domain IN ('chat','video','audio','image')),\n"
	const decl = "capability_domain TEXT NOT NULL DEFAULT 'chat' CHECK (capability_domain IN ('chat','video','audio','image'))"
	switch kind {
	case "missing":
		ddl := strings.Replace(SQLiteBusinessMainDDLForTest(), fullLine, "", 1)
		if strings.Contains(ddl, "capability_domain") {
			t.Fatal("legacy missing fixture 仍声明 capability_domain")
		}
		return ddl
	case "broken":
		ddl := strings.Replace(SQLiteBusinessMainDDLForTest(), decl, "capability_domain TEXT", 1)
		if !strings.Contains(ddl, "capability_domain TEXT\n") && !strings.Contains(ddl, "capability_domain TEXT,") {
			t.Fatal("legacy broken fixture 替换未生效")
		}
		return ddl
	}
	t.Fatalf("unknown fixture kind %q", kind)
	return ""
}

func capabilityDomainPrelude(t *testing.T, db *sql.DB, kind string) {
	t.Helper()
	if _, err := db.Exec(capabilityDomainLegacyDDL(t, kind)); err != nil {
		t.Fatalf("seed legacy business DDL: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO accounts
      (id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
       name, type, credentials_encrypted, health_check_model, health_check_endpoint_mode, created_at, updated_at)
      VALUES ('acct-legacy', 'sys-1', 'gpt', 'profile-1', 'openai', 'v1', 'legacy', 'api_key', 'enc', 'm', 'chat_json',
       '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed legacy account: %v", err)
	}
}

// TestSQLiteCapabilityDomainFreshDDL covers the fresh-database path: the
// CREATE TABLE declares the column with a working CHECK, and repeated ensure
// runs exit the guard early.
func TestSQLiteCapabilityDomainFreshDDL(t *testing.T) {
	db := openSharedMemorySQLite(t, "capability-domain-fresh")
	ctx := context.Background()
	if _, err := EnsureSQLiteBusiness(ctx, db); err != nil {
		t.Fatalf("EnsureSQLiteBusiness: %v", err)
	}
	if !sqliteTableHasColumn(t, db, "accounts", "capability_domain") {
		t.Fatal("fresh accounts lacks capability_domain")
	}
	notNull, dflt, found := sqliteColumnMeta(t, db, "accounts", "capability_domain")
	if !found || notNull != 1 || !dflt.Valid || dflt.String != "'chat'" {
		t.Fatalf("capability_domain meta: found=%v notnull=%d dflt=%+v, want notnull=1 dflt='chat'", found, notNull, dflt)
	}
	// CHECK 生效：合法值接受、词表外值拒绝；存量默认行先断言 'chat'。
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatalf("disable foreign_keys: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO accounts
		(id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
		 name, type, credentials_encrypted, health_check_model, health_check_endpoint_mode, created_at, updated_at)
		VALUES ('acct-probe', 'sys-1', 'gpt', 'profile-1', 'openai', 'v1', 'probe', 'api_key', 'enc', 'm', 'chat_json',
		 '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed probe account: %v", err)
	}
	var domain string
	if err := db.QueryRow(`SELECT capability_domain FROM accounts WHERE id = 'acct-probe'`).Scan(&domain); err != nil {
		t.Fatalf("read probe domain: %v", err)
	}
	if domain != "chat" {
		t.Fatalf("probe domain = %q, want default chat", domain)
	}
	for _, value := range []string{"chat", "video", "audio", "image"} {
		if _, err := db.Exec(`UPDATE accounts SET capability_domain = ? WHERE id = 'acct-probe'`, value); err != nil {
			t.Fatalf("capability_domain=%s must be accepted: %v", value, err)
		}
	}
	if _, err := db.Exec(`UPDATE accounts SET capability_domain = 'media' WHERE id = 'acct-probe'`); err == nil {
		t.Fatal("capability_domain='media' must be rejected by the column CHECK")
	}
	// 重复 ensure 早退（二次 ADD COLUMN 会报 duplicate column）。
	if _, err := EnsureSQLiteBusiness(ctx, db); err != nil {
		t.Fatalf("second EnsureSQLiteBusiness must exit the guard early: %v", err)
	}
}

// TestSQLiteCapabilityDomainLegacyAdd covers the existing-database path:
// missing column → guarded ADD COLUMN with existing rows landing on 'chat'.
func TestSQLiteCapabilityDomainLegacyAdd(t *testing.T) {
	db := openSharedMemorySQLite(t, "capability-domain-legacy")
	capabilityDomainPrelude(t, db, "missing")
	if sqliteTableHasColumn(t, db, "accounts", "capability_domain") {
		t.Fatal("legacy precondition violated: capability_domain already present")
	}
	if _, err := EnsureSQLiteBusiness(context.Background(), db); err != nil {
		t.Fatalf("EnsureSQLiteBusiness over legacy tables: %v", err)
	}
	if !sqliteTableHasColumn(t, db, "accounts", "capability_domain") {
		t.Fatal("legacy accounts lacks capability_domain after ensure")
	}
	var domain string
	if err := db.QueryRow(`SELECT capability_domain FROM accounts WHERE id = 'acct-legacy'`).Scan(&domain); err != nil {
		t.Fatalf("read legacy row: %v", err)
	}
	if domain != "chat" {
		t.Fatalf("legacy row capability_domain = %q, want default chat", domain)
	}
	// 加列后的形状必须通过四值校验（幂等重入）。
	if err := VerifySQLiteCapabilityDomainShape(context.Background(), db); err != nil {
		t.Fatalf("shape verify after add: %v", err)
	}
}

// TestSQLiteCapabilityDomainWrongShapeRejected covers the fail-closed path:
// a column that exists with the wrong shape stops with the original error
// instead of being silently repaired.
func TestSQLiteCapabilityDomainWrongShapeRejected(t *testing.T) {
	cases := []struct {
		name string
		decl string
	}{
		{"可空无默认无 CHECK", "capability_domain TEXT"},
		{"缺 CHECK", "capability_domain TEXT NOT NULL DEFAULT 'chat'"},
		{"错误词表", "capability_domain TEXT NOT NULL DEFAULT 'chat' CHECK (capability_domain IN ('chat','video'))"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db2 := openSharedMemorySQLite(t, "capability-domain-broken")
			ddl := strings.Replace(SQLiteBusinessMainDDLForTest(),
				"capability_domain TEXT NOT NULL DEFAULT 'chat' CHECK (capability_domain IN ('chat','video','audio','image'))",
				testCase.decl, 1)
			if _, err := db2.Exec(ddl); err != nil {
				t.Fatalf("seed broken ddl: %v", err)
			}
			if err := VerifySQLiteCapabilityDomainShape(context.Background(), db2); err == nil {
				t.Fatal("错误形状必须原样报错")
			} else if !strings.Contains(err.Error(), "形状不符") {
				t.Fatalf("错误应指明形状不符：%v", err)
			}
			// ensure 路径同样拒绝（不静默修补）。
			if _, err := EnsureSQLiteBusiness(context.Background(), db2); err == nil {
				t.Fatal("ensure 必须拒绝错误形状")
			}
		})
	}
}

// TestPostgresCapabilityDomainSchemaContract pins the PG text contract along
// the expedited-recovery precedent: the CREATE TABLE declaration line and the
// legacy ALTER guard statement.
func TestPostgresCapabilityDomainSchemaContract(t *testing.T) {
	accountsDDL := findPostgresStatementContaining(t, "CREATE TABLE IF NOT EXISTS accounts (")
	domainLine := pgColumnDeclarationLine(t, accountsDDL, "capability_domain")
	want := "      capability_domain text NOT NULL DEFAULT 'chat' CHECK (capability_domain IN ('chat','video','audio','image')),"
	if domainLine != want {
		t.Fatalf("accounts.capability_domain declaration drifted:\n got %q\nwant %q", domainLine, want)
	}
	probeIndex := strings.Index(accountsDDL, "expedited_recovery_enabled integer NOT NULL")
	domainIndex := strings.Index(accountsDDL, "capability_domain text NOT NULL")
	if probeIndex < 0 || domainIndex < probeIndex {
		t.Fatalf("capability_domain must sit next to expedited_recovery_enabled (expedited at %d, domain at %d)", probeIndex, domainIndex)
	}

	alterFound := false
	for _, statement := range postgresSchemaStatements {
		if statement.Source != "account-capability-domain-pg-column" {
			continue
		}
		alterFound = true
		wantAlter := `ALTER TABLE "juhe_business"."accounts" ADD COLUMN IF NOT EXISTS capability_domain text NOT NULL DEFAULT 'chat' CHECK (capability_domain IN ('chat','video','audio','image'))`
		if statement.SQL != wantAlter {
			t.Fatalf("capability domain legacy ALTER drifted:\n got %q\nwant %q", statement.SQL, wantAlter)
		}
	}
	if !alterFound {
		t.Fatal("account-capability-domain-pg-column ALTER guard statement missing")
	}
}

// TestPostgresCapabilityDomainLiveSmoke is the opt-in live PostgreSQL test:
// set JUHE_AI_CAPABILITY_DOMAIN_PG_SMOKE_URL to a disposable database to run
// the real ADD COLUMN / shape-verify path; plain `go test` skips it.
func TestPostgresCapabilityDomainLiveSmoke(t *testing.T) {
	databaseURL := os.Getenv("JUHE_AI_CAPABILITY_DOMAIN_PG_SMOKE_URL")
	if databaseURL == "" {
		t.Skip("JUHE_AI_CAPABILITY_DOMAIN_PG_SMOKE_URL not set; PostgreSQL capability-domain smoke test skipped")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open pgx: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}
	// 新库：EnsurePostgres 建表 + 加列，形状校验通过。
	if _, err := EnsurePostgres(ctx, db); err != nil {
		t.Fatalf("first EnsurePostgres: %v", err)
	}
	if err := VerifyPostgresBusinessCapabilityDomainShape(ctx, db); err != nil {
		t.Fatalf("shape verify after ensure: %v", err)
	}
	// 幂等重跑。
	if _, err := EnsurePostgres(ctx, db); err != nil {
		t.Fatalf("second EnsurePostgres: %v", err)
	}
	// 独立入口（迁移命令路径）：缺列库上加列。
	if _, err := db.Exec(`ALTER TABLE "juhe_business"."accounts" DROP COLUMN capability_domain`); err != nil {
		t.Fatalf("drop column for legacy drill: %v", err)
	}
	if err := EnsurePostgresBusinessCapabilityDomainColumn(ctx, db); err != nil {
		t.Fatalf("EnsurePostgresBusinessCapabilityDomainColumn: %v", err)
	}
	// 错误形状（手工去掉 NOT NULL 语义：新建一列错误形状不可行，改为直接
	// 篡改默认值）→ 校验拒绝。
	if _, err := db.Exec(`ALTER TABLE "juhe_business"."accounts" ALTER COLUMN capability_domain DROP DEFAULT`); err != nil {
		t.Fatalf("break default: %v", err)
	}
	if err := VerifyPostgresBusinessCapabilityDomainShape(ctx, db); err == nil {
		t.Fatal("错误形状必须被拒绝")
	}
}
