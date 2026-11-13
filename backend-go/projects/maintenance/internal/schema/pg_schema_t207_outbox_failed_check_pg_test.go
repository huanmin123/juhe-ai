package schema

// pg_schema_t207_outbox_failed_check_pg_test.go 锁定 T207 收口批（revision 24）
// 契约：juhe_business.account_circuit_outbox 的 status 词表 CHECK 与状态不变式
// CHECK 放宽 'failed' 终态臂并具名（account_circuit_outbox_status_check /
// account_circuit_outbox_status_invariant），存量表旧约束由
// account-circuit-outbox-status-failed-pg-check DO 块按「表 + conkey 列 + 定义
// 文本」定位替换。文本断言 SQLite/无服务可跑；真实 PG 门禁在隔离测试库上建
// 旧形态表（旧三值词表 + 旧三臂不变式 + 相邻 CHECK），验证 DO 块只替换目标
// 两约束、保留相邻约束、幂等重放、failed 行为符合不变式。门禁模式沿用
// w-plan 系列：JUHE_AI_TEST_EXTERNAL=1 授权，DSN 取 shared.env 的
// JUHE_AI_POSTGRES_URL；PG 不可达时 t.Skip。

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func t207OutboxStatusStatements(t *testing.T) (createTable, migration string) {
	t.Helper()
	for _, statement := range postgresSchemaStatements {
		switch {
		case strings.Contains(statement.SQL, "CREATE TABLE IF NOT EXISTS account_circuit_outbox"):
			createTable = statement.SQL
		case statement.Source == "account-circuit-outbox-status-failed-pg-check":
			migration = statement.SQL
		}
	}
	if createTable == "" || migration == "" {
		t.Fatalf("schema 语句清单缺少 account_circuit_outbox 建表或 failed CHECK 迁移语句")
	}
	return createTable, migration
}

// TestT207OutboxStatusFailedCheckContract 文本契约：建表 DDL 含具名两约束与
// failed 臂；DO 块含两段旧约束定位（列级单列 + 表级不变式列组合）、具名重建
// 与表存在守卫。
func TestT207OutboxStatusFailedCheckContract(t *testing.T) {
	createTable, migration := t207OutboxStatusStatements(t)
	for _, fragment := range []string{
		"CONSTRAINT account_circuit_outbox_status_check CHECK (status IN ('pending', 'processing', 'dispatched', 'failed'))",
		"CONSTRAINT account_circuit_outbox_status_invariant CHECK ((status = 'pending'",
		"OR (status = 'failed' AND claim_token IS NULL AND claimed_by IS NULL AND claim_until_ms IS NULL AND acknowledged_at_ms IS NOT NULL))",
	} {
		if !strings.Contains(createTable, fragment) {
			t.Fatalf("account_circuit_outbox 建表 DDL 缺少片段 %q", fragment)
		}
	}
	for _, fragment := range []string{
		"to_regclass('juhe_business.account_circuit_outbox') IS NOT NULL",
		"array_length(c.conkey, 1) = 1",
		"pg_get_constraintdef(c.oid) NOT LIKE '%failed%'",
		"EXECUTE format('ALTER TABLE juhe_business.account_circuit_outbox DROP CONSTRAINT %I', legacy_status_check)",
		"EXECUTE format('ALTER TABLE juhe_business.account_circuit_outbox DROP CONSTRAINT %I', legacy_status_invariant)",
		"ADD CONSTRAINT account_circuit_outbox_status_check CHECK (status IN (''pending'', ''processing'', ''dispatched'', ''failed''))",
		"ADD CONSTRAINT account_circuit_outbox_status_invariant CHECK ((status = ''pending''",
	} {
		if !strings.Contains(migration, fragment) {
			t.Fatalf("failed CHECK 迁移 DO 块缺少片段 %q", fragment)
		}
	}
	// 列级定位必须锚定 status 列的 conkey 成员身份；表级定位必须以
	// claim_until_ms/acknowledged_at_ms 区分于 transition 校验 CHECK。
	for _, fragment := range []string{
		"AND a.attname = 'status'",
		"AND pg_get_constraintdef(c.oid) LIKE '%claim_until_ms%'",
		"AND pg_get_constraintdef(c.oid) LIKE '%acknowledged_at_ms%'",
	} {
		if !strings.Contains(migration, fragment) {
			t.Fatalf("failed CHECK 迁移 DO 块定位器缺少片段 %q", fragment)
		}
	}
}

// t207OutboxLegacyDDL 复刻存量生产表的旧约束形态（旧三值词表内联 CHECK、旧
// 三臂不变式、相邻 transition 校验与 dispatch_revision/event_type 词表），供
// 门禁测试验证 DO 块只替换目标两约束。不含 accounts 外键（隔离测试库无该表，
// DO 块不触 FK）。
const t207OutboxLegacyDDL = `CREATE TABLE juhe_business.account_circuit_outbox (
      event_id text PRIMARY KEY,
      projection_key text NOT NULL,
      dedupe_key text NOT NULL,
      event_type text NOT NULL CHECK (event_type IN ('dispatch_revision_changed', 'incident_changed')),
      account_id text NOT NULL,
      account_runtime_key text NOT NULL,
      circuit_scope_key text,
      incident_id text,
      transition_id text NOT NULL,
      dispatch_revision bigint NOT NULL CHECK (dispatch_revision >= 1),
      generation bigint,
      ledger_revision bigint,
      status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'dispatched')),
      available_at_ms bigint NOT NULL,
      claim_token text,
      claimed_by text,
      claim_until_ms bigint,
      attempt_count integer NOT NULL DEFAULT 0,
      last_error_class text,
      acknowledged_at_ms bigint,
      created_at_ms bigint NOT NULL,
      updated_at_ms bigint NOT NULL,
      UNIQUE (projection_key, dedupe_key),
      CHECK ((event_type = 'dispatch_revision_changed' AND circuit_scope_key IS NULL AND incident_id IS NULL AND generation IS NULL AND ledger_revision IS NULL)
        OR (event_type = 'incident_changed' AND circuit_scope_key IS NOT NULL AND incident_id IS NOT NULL AND generation IS NOT NULL AND ledger_revision IS NOT NULL)),
      CHECK ((status = 'pending' AND claim_token IS NULL AND claimed_by IS NULL AND claim_until_ms IS NULL AND acknowledged_at_ms IS NULL)
        OR (status = 'processing' AND claim_token IS NOT NULL AND claimed_by IS NOT NULL AND claim_until_ms IS NOT NULL AND acknowledged_at_ms IS NULL)
        OR (status = 'dispatched' AND claim_token IS NULL AND claimed_by IS NULL AND claim_until_ms IS NULL AND acknowledged_at_ms IS NOT NULL))
    )`

func t207OutboxGateDB(t *testing.T) *sql.DB {
	t.Helper()
	pgtestkit.SkipUnlessExternalAuthorized(t)
	dsn := ""
	if url := os.Getenv("JUHE_AI_T207_PG_URL"); url != "" {
		dsn = url
	} else {
		raw, err := os.ReadFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
		if err != nil {
			t.Skip("PG gated: shared.env 不可读")
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "JUHE_AI_POSTGRES_URL=") {
				dsn = strings.TrimPrefix(line, "JUHE_AI_POSTGRES_URL=")
			}
		}
	}
	if dsn == "" {
		t.Skip("PG gated: 缺少 JUHE_AI_POSTGRES_URL")
	}
	testDB := pgtestkit.NewTestDatabase(t, func(_ context.Context, db *sql.DB) error {
		if _, err := db.ExecContext(context.Background(), `CREATE SCHEMA IF NOT EXISTS juhe_business`); err != nil {
			return err
		}
		if _, err := db.ExecContext(context.Background(), t207OutboxLegacyDDL); err != nil {
			return err
		}
		return nil
	})
	return testDB.DB
}

func t207OutboxRunMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	_, migration := t207OutboxStatusStatements(t)
	if _, err := db.ExecContext(context.Background(), migration); err != nil {
		t.Fatalf("执行 failed CHECK 迁移 DO 块失败: %v", err)
	}
}

func t207OutboxStatusConstraintDefs(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
    SELECT c.conname, pg_get_constraintdef(c.oid)
      FROM pg_constraint AS c
      JOIN pg_class AS relation ON relation.oid = c.conrelid
      JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
     WHERE namespace.nspname = 'juhe_business'
       AND relation.relname = 'account_circuit_outbox'
       AND c.contype = 'c'
       AND (pg_get_constraintdef(c.oid) LIKE '%status%'
            OR pg_get_constraintdef(c.oid) LIKE '%event_type%')
       ORDER BY c.conname`)
	if err != nil {
		t.Fatalf("查询约束失败: %v", err)
	}
	defer rows.Close()
	defs := map[string]string{}
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			t.Fatalf("扫描约束失败: %v", err)
		}
		defs[name] = def
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历约束失败: %v", err)
	}
	return defs
}

// TestT207OutboxStatusFailedCheckReplacementOnPostgres 真实 PG 门禁：旧形态表
// 上执行 DO 块 → 旧三值词表与旧三臂不变式被替换为具名新约束（含 failed 臂），
// 相邻 event_type/dispatch_revision CHECK 保留；failed 行按不变式放行/拒绝；
// 重复执行幂等（约束集合不变）。
func TestT207OutboxStatusFailedCheckReplacementOnPostgres(t *testing.T) {
	db := t207OutboxGateDB(t)

	// 前置自证：旧形态表拒绝 failed 行。
	if _, err := db.ExecContext(context.Background(), `INSERT INTO juhe_business.account_circuit_outbox (event_id,projection_key,dedupe_key,event_type,account_id,account_runtime_key,circuit_scope_key,incident_id,generation,ledger_revision,transition_id,dispatch_revision,status,available_at_ms,created_at_ms,updated_at_ms) VALUES ('e0','p','d0','incident_changed','a','a','s','i',1,1,'t',1,'failed',1,1,1)`); err == nil {
		t.Fatal("旧形态表必须拒绝 failed 行（前置自证失效）")
	}

	t207OutboxRunMigration(t, db)
	defs := t207OutboxStatusConstraintDefs(t, db)
	statusCheck, ok := defs["account_circuit_outbox_status_check"]
	if !ok || !strings.Contains(statusCheck, "failed") {
		t.Fatalf("具名列级词表约束缺失或未含 failed: %v", defs)
	}
	statusInvariant, ok := defs["account_circuit_outbox_status_invariant"]
	if !ok || !strings.Contains(statusInvariant, "failed") || !strings.Contains(statusInvariant, "acknowledged_at_ms") {
		t.Fatalf("具名不变式约束缺失或未含 failed 臂: %v", defs)
	}
	// 旧无具名约束不得残留：status/event_type 相关约束中除两具名与
	// event_type 词表外不应再有单列 status 词表。
	for name, def := range defs {
		if name == "account_circuit_outbox_status_check" || name == "account_circuit_outbox_status_invariant" {
			continue
		}
		if strings.HasPrefix(def, "CHECK (status") || strings.Contains(def, "status = ") {
			t.Fatalf("旧 status 约束未被替换: %s = %s", name, def)
		}
	}
	// 相邻约束保留：event_type 词表仍在。
	if _, ok := defs["account_circuit_outbox_event_type_check"]; !ok {
		t.Fatalf("event_type 词表 CHECK 被误删: %v", defs)
	}

	// 不变式行为：合法 failed 行放行；failed 带 claim 面拒绝；非法 status 拒绝。
	legal := `INSERT INTO juhe_business.account_circuit_outbox (event_id,projection_key,dedupe_key,event_type,account_id,account_runtime_key,circuit_scope_key,incident_id,generation,ledger_revision,transition_id,dispatch_revision,status,available_at_ms,last_error_class,acknowledged_at_ms,created_at_ms,updated_at_ms) VALUES ('e1','p','d1','incident_changed','a','a','s','i',1,1,'t',1,'failed',1,'projection_failed',10,1,1)`
	if _, err := db.ExecContext(context.Background(), legal); err != nil {
		t.Fatalf("合法 failed 行必须放行: %v", err)
	}
	badFailedWithClaim := `INSERT INTO juhe_business.account_circuit_outbox (event_id,projection_key,dedupe_key,event_type,account_id,account_runtime_key,circuit_scope_key,incident_id,generation,ledger_revision,transition_id,dispatch_revision,status,available_at_ms,claim_token,claimed_by,claim_until_ms,acknowledged_at_ms,created_at_ms,updated_at_ms) VALUES ('e3','p','d3','incident_changed','a','a','s','i',1,1,'t',1,'failed',1,'tok','o',99,NULL,1,1)`
	if _, err := db.ExecContext(context.Background(), badFailedWithClaim); err == nil {
		t.Fatal("failed 行带 claim 面必须被不变式拒绝")
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO juhe_business.account_circuit_outbox (event_id,projection_key,dedupe_key,event_type,account_id,account_runtime_key,circuit_scope_key,incident_id,generation,ledger_revision,transition_id,dispatch_revision,status,available_at_ms,created_at_ms,updated_at_ms) VALUES ('e4','p','d4','incident_changed','a','a','s','i',1,1,'t',1,'canceled',1,1,1)`); err == nil {
		t.Fatal("词表外 status 必须被拒绝")
	}
	// 相邻约束仍在位：dispatch_revision=0 拒绝。
	if _, err := db.ExecContext(context.Background(), `INSERT INTO juhe_business.account_circuit_outbox (event_id,projection_key,dedupe_key,event_type,account_id,account_runtime_key,circuit_scope_key,incident_id,generation,ledger_revision,transition_id,dispatch_revision,status,available_at_ms,created_at_ms,updated_at_ms) VALUES ('e5','p','d5','incident_changed','a','a','s','i',1,1,'t',0,'failed',1,1,1)`); err == nil {
		t.Fatal("dispatch_revision >= 1 CHECK 被误删")
	}

	// 幂等：重复执行后约束集合不变。
	before := t207OutboxStatusConstraintDefs(t, db)
	t207OutboxRunMigration(t, db)
	after := t207OutboxStatusConstraintDefs(t, db)
	if len(before) != len(after) {
		t.Fatalf("重复执行产生约束变化: %d -> %d (%v -> %v)", len(before), len(after), before, after)
	}
	for name, def := range before {
		if after[name] != def {
			t.Fatalf("重复执行改变约束 %s: %q -> %q", name, def, after[name])
		}
	}
}
