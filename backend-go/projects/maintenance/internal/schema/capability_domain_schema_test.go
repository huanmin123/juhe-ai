// accounts.capability_domain 列 schema 测试（AI 账户能力域拆分
// PLAN-20261011T141004000Z §数据变化）：PG 建表 DDL 行与存量库 ALTER 语句的
// 文本契约钉死（live PostgreSQL 走 opt-in smoke：
// JUHE_AI_CAPABILITY_DOMAIN_PG_SMOKE_URL 指向一次性隔离库）。SQLite fresh
// DDL/存量加列臂随 SQLite 存储形态退役删除（PLAN-20261024T051925945Z B4）。

package schema

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
)

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
