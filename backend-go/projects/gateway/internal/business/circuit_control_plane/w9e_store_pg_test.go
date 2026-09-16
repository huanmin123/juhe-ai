package circuitcontrolplane

// w9e 真实 dev PG 门禁化覆盖测试（TestW9ECPPPostgres 入口）。
//
// 隔离铁律（与 cmd 包 w1_pg_wave_test.go 同约定）：
//   - 只允许触碰临时子库 juhe_ai_sub2api_dev_w9ecpp；主库一个字节不动。
//   - 连接串只从 .local/project-resources/dev/env/shared.env 读取（w9eCPPEnv），
//     失败信息不携带 URL/密码。
//   - env 缺失或 PG 不可达一律 t.Skip，保持无 dev 环境时测试可重放。
//   - 覆盖目标：checkKeyModelCapabilityIndex 的 Postgres 分支（pg_index 查询、
//     ErrNoRows/兼容性判定）与 PG 模式 bind/forUpdate 事务路径。

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const w9eCPPDB = "juhe_ai_sub2api_dev_w9ecpp"

func w9eCPPEnv(t *testing.T) map[string]string {
	t.Helper()
	path := "../../../../../../.local/project-resources/dev/env/shared.env"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("dev env 不可达（跳过 PG 门禁测试）: %v", err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if found {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return values
}

const w9eCPPIncidentsDDL = `CREATE TABLE IF NOT EXISTS ` + defaultBusinessSchema + `.account_circuit_incidents (
 circuit_scope_key TEXT PRIMARY KEY, account_id TEXT NOT NULL, account_runtime_key TEXT NOT NULL, scope_kind TEXT NOT NULL,
 key_fingerprint TEXT, protocol_code TEXT, request_lane TEXT, model_family TEXT, client_model TEXT, capability_hash TEXT,
 credential_source_account_id TEXT, client_endpoint_family TEXT, final_upstream_model TEXT, upstream_endpoint_mode TEXT, incident_id TEXT NOT NULL,
 parent_incident_id TEXT, child_incident_ids_json TEXT NOT NULL, caused_by_terminal_outcome_id TEXT, state TEXT NOT NULL,
 failure_scope TEXT, generation BIGINT NOT NULL, dispatch_revision BIGINT NOT NULL, ledger_revision BIGINT NOT NULL,
 projected_ledger_revision BIGINT NOT NULL, transition_id TEXT NOT NULL, cooldown_observation_generation BIGINT NOT NULL,
 open_until_ms BIGINT, next_transition_at_ms BIGINT, lease_id TEXT, lease_purpose TEXT, lease_owner_run_id TEXT,
 lease_until_ms BIGINT, attempt_started_at_ms BIGINT, attempt_hard_deadline_ms BIGINT, upstream_attempt_observed INTEGER NOT NULL,
 backoff_level BIGINT NOT NULL, consecutive_failures BIGINT NOT NULL, confirmation_failures_required BIGINT NOT NULL,
 confirmation_failure_evidence_keys_json TEXT NOT NULL, recovering_successes BIGINT NOT NULL, last_failure_class TEXT,
 retained_until_ms BIGINT, created_at_ms BIGINT NOT NULL, updated_at_ms BIGINT NOT NULL)`

const w9eCPPOutboxDDL = `CREATE TABLE IF NOT EXISTS ` + defaultBusinessSchema + `.account_circuit_outbox (
 event_id TEXT PRIMARY KEY, projection_key TEXT NOT NULL, dedupe_key TEXT NOT NULL UNIQUE, event_type TEXT NOT NULL,
 account_id TEXT NOT NULL, account_runtime_key TEXT NOT NULL, circuit_scope_key TEXT, incident_id TEXT, transition_id TEXT NOT NULL,
 dispatch_revision BIGINT NOT NULL, generation BIGINT, ledger_revision BIGINT, status TEXT NOT NULL, available_at_ms BIGINT NOT NULL,
 claim_token TEXT, claimed_by TEXT, claim_until_ms BIGINT, attempt_count BIGINT NOT NULL, last_error_class TEXT,
 acknowledged_at_ms BIGINT, created_at_ms BIGINT NOT NULL, updated_at_ms BIGINT NOT NULL)`

const w9eCPPCapabilityIndex = `CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON ` +
	defaultBusinessSchema + `.account_circuit_incidents(scope_kind, capability_hash) ` +
	`WHERE scope_kind = 'key_model' AND capability_hash IS NOT NULL`

func w9eCPPPostgres(t *testing.T) *sql.DB {
	t.Helper()
	if testing.Short() {
		t.Skip("short 模式跳过真实 dev PG 测试")
	}
	env := w9eCPPEnv(t)
	host := env["DEV_POSTGRES_HOST"]
	directPort := env["DEV_POSTGRES_DIRECT_PORT"]
	adminUser := env["DEV_POSTGRES_ADMIN_USERNAME"]
	adminPass := env["DEV_POSTGRES_ADMIN_PASSWORD"]
	appURL := env["JUHE_AI_POSTGRES_URL"]
	if host == "" || directPort == "" || adminUser == "" || adminPass == "" || appURL == "" {
		t.Skipf("dev env 缺少 PG 键（跳过真实 PG 门禁测试）")
	}
	adminDSN := fmt.Sprintf("postgres://%s:%s@%s:%s/postgres?sslmode=disable", adminUser, adminPass, host, directPort)
	adminDB, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatalf("打开 dev PG 管理连接失败: %v", err)
	}
	defer adminDB.Close()
	if err := adminDB.Ping(); err != nil {
		t.Skipf("dev PG 管理口不可达（跳过）: %v", err)
	}
	// 每轮重灌临时子库，保证测试只反映本轮状态。
	if _, err := adminDB.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, w9eCPPDB)); err != nil {
		t.Skipf("清理临时子库失败（跳过）: %v", err)
	}
	if _, err := adminDB.Exec(fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, w9eCPPDB, env["DEV_POSTGRES_APP_USERNAME"])); err != nil {
		t.Fatalf("创建临时子库失败: %v", err)
	}
	sep := strings.LastIndex(appURL, "/")
	if sep < 0 {
		t.Skipf("dev env 的 JUHE_AI_POSTGRES_URL 形态异常（跳过）")
	}
	tempAppURL := appURL[:sep+1] + w9eCPPDB
	appDB, err := sql.Open("pgx", tempAppURL)
	if err != nil {
		t.Fatalf("打开临时子库连接失败: %v", err)
	}
	appDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = appDB.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := appDB.PingContext(ctx); err != nil {
		t.Fatalf("临时子库 ping 失败: %v", err)
	}
	for _, ddl := range []string{
		`CREATE SCHEMA IF NOT EXISTS ` + defaultBusinessSchema,
		`CREATE TABLE IF NOT EXISTS ` + defaultBusinessSchema + `.accounts (id TEXT PRIMARY KEY, dispatch_revision BIGINT NOT NULL DEFAULT 1, circuit_projection_revision BIGINT NOT NULL DEFAULT 0, deleted_at TEXT)`,
		w9eCPPIncidentsDDL,
		w9eCPPOutboxDDL,
		w9eCPPCapabilityIndex,
		`INSERT INTO ` + defaultBusinessSchema + `.accounts(id,dispatch_revision,circuit_projection_revision) VALUES ('a1',1,0)`,
	} {
		if _, err := appDB.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("临时子库 DDL 失败: %v", err)
		}
	}
	return appDB
}

func TestW9ECPPPostgresContractAndFlows(t *testing.T) {
	appDB := w9eCPPPostgres(t)
	ctx := context.Background()
	gate := OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}

	s, err := New(appDB, Postgres, defaultBusinessSchema, gate)
	if err != nil {
		t.Fatal(err)
	}
	// 索引契约：PG 分支 unique/columns/predicate 全部匹配 → nil。
	if err := s.CheckContract(ctx); err != nil {
		t.Fatalf("PG CheckContract 应成功: %v", err)
	}
	// PG 事务路径：dispatch 推进 + CAS 事故写入（forUpdate/bind PG 分支）。
	res, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "tr-pg-1", NowMS: 1})
	if err != nil || res.Status != "applied" || res.DispatchRevision != 2 {
		t.Fatalf("PG advance = %+v err=%v", res, err)
	}
	mut := wkBaseIncident("w9e-pg-scope", "a1", 2)
	cas, err := s.CompareAndSetIncident(ctx, mut)
	if err != nil || cas.Status != "applied" {
		t.Fatalf("PG CAS = %+v err=%v", cas, err)
	}
	claimed, err := s.ClaimOutbox(ctx, "w9e-owner", 200, 60_000, 10)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("PG claim = %d 个 err=%v", len(claimed), err)
	}
	// 缺索引 → missing。
	if _, err := appDB.Exec(`DROP INDEX ` + defaultBusinessSchema + `.idx_account_circuit_incidents_key_model_capability`); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("缺索引应报 missing, got %v", err)
	}
	// 唯一性不符 → incompatible。
	if _, err := appDB.Exec(`CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON ` + defaultBusinessSchema + `.account_circuit_incidents(scope_kind, capability_hash)`); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("非部分索引应报 incompatible, got %v", err)
	}
	if _, err := appDB.Exec(`DROP INDEX ` + defaultBusinessSchema + `.idx_account_circuit_incidents_key_model_capability`); err != nil {
		t.Fatal(err)
	}
	// 列数不符 → indnkeyatts 不匹配 → missing（同名校验要求列数一致）。
	if _, err := appDB.Exec(`CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON ` + defaultBusinessSchema + `.account_circuit_incidents(scope_kind) WHERE scope_kind = 'key_model' AND capability_hash IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckContract(ctx); err == nil {
		t.Fatal("列数不符应失败")
	}
}
