// 波次 w14q：main() PG 直连装配成功链子进程 e2e。w1cover 共享库已重建为
// 生产形状（accounts.schedulable 为 integer），此前被 42883 text 列死结阻塞
// 的 J1 PG 直连输入 / model-recovery / J2 / J3a 装配链现在可测。
//
// 场景：owner 模式子进程以 F1/F2 SQLite + J1(postgres store + postgres 直连
// input) + model-recovery(Redis) + J2(postgres) + J3a(postgres + management)
// 全组件装配；父测试等 /health 报告各组件 enabled 且 worker 就绪后立即
// CTRL_BREAK 干净停机。覆盖 main() 的 PG 装配成功分支、组件注册与停机排空。
//
// 安全边界：
//   - 连接串只从 shared.env 读取改写，绝不写入日志与断言；
//   - 共享库只做幂等 DDL 与 w14q- 前缀数据清理，不动他人行；
//   - J1 初始扫描会读取共享库中 due 的测试 fixture 账户（w9h- 等波次
//     seed 的无效凭据账户，探针为无效凭据的公开上游请求，量级个位数）；
//     父测试先做只读 due 候选统计，超过护栏值时跳过。
package main

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// w14qDueCandidateGuard 统计共享库当前 J1 直连 due 候选数量（只读）。仅输出
// 数量；候选身份不落日志。超过护栏说明共享库被填充了大量真实账户，跳过 e2e。
func w14qDueCandidateGuard(t *testing.T, db *sql.DB) int {
	t.Helper()
	reader, err := accounthealth.NewPostgresDirectInputReader(db, "0123456789abcdef0123456789abcdef", time.Hour, time.Now)
	if err != nil {
		t.Fatalf("构造护栏 reader: %v", err)
	}
	result, err := reader.LoadDueWithFailures(context.Background(), 512)
	if err != nil {
		// 契约/形状失败让主测试路径去报错；护栏只关心可达时的数量。
		t.Logf("w14q due 护栏统计失败（继续，由 e2e 验证契约）: %v", err)
		return 0
	}
	if count := len(result.Inputs); count > 20 {
		t.Skipf("w14q: 共享库 due 候选 %d 超过护栏 20，跳过探针型 e2e", count)
	}
	return len(result.Inputs)
}

// w14qCleanupPGMainRows 清理本测试在共享库写入的 w14q- 前缀行。
func w14qCleanupPGMainRows(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`DELETE FROM juhe_jobs.account_health_owner_leases WHERE owner_id LIKE 'w14q-%'`,
		`DELETE FROM juhe_jobs.account_balance_owner_leases WHERE owner_id LIKE 'w14q-%'`,
		`DELETE FROM juhe_jobs.proxy_latency_owner_leases WHERE owner_id LIKE 'w14q-%'`,
		`DELETE FROM juhe_jobs.proxy_latency_proxy_leases WHERE owner_id LIKE 'w14q-%'`,
		`DELETE FROM juhe_business.account_health_projection_cursors WHERE consumer_key LIKE 'w14q-%'`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Logf("w14q pg main cleanup: %v", err)
		}
	}
}

// w14qPGMainEnv 在 owner 模式 SQLite F1/F2 环境上叠加 PG 装配链 env。
func w14qPGMainEnv(t *testing.T, root string, pgURL, redisURL string, managementPort int) map[string]string {
	t.Helper()
	env := wgOwnerModeMainEnv(t, root)
	// J1：postgres store + postgres 直连输入（替换 wgOwnerModeMainEnv 的 sqlite 形状）。
	delete(env, "JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH")
	env["JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID"] = "w14q-pg-main-e2e"
	env["JUHE_AI_ACCOUNT_HEALTH_STORE"] = "postgres"
	env["JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL"] = pgURL
	env["JUHE_AI_ACCOUNT_HEALTH_POSTGRES_MAX_OPEN_CONNS"] = "8"
	env["JUHE_AI_ACCOUNT_HEALTH_POSTGRES_MAX_IDLE_CONNS"] = "4"
	env["JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE"] = "postgres"
	env["JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL"] = pgURL
	env["JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_MAX_OPEN_CONNS"] = "8"
	env["JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_MAX_IDLE_CONNS"] = "4"
	// model-recovery：独立 namespace，只允许 w14q 形状字符。
	env["JUHE_AI_REDIS_STATE_URL"] = redisURL
	env["JUHE_AI_REDIS_NAMESPACE"] = "juhe-ai:w14q"
	// J2 account-balance：postgres store + postgres 直连输入。
	env["JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER"] = "go"
	env["JUHE_AI_ACCOUNT_BALANCE_OWNER_ID"] = "w14q-balance-owner"
	env["JUHE_AI_ACCOUNT_BALANCE_STORE"] = "postgres"
	env["JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL"] = pgURL
	env["JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL"] = pgURL
	env["JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET"] = "0123456789abcdef0123456789abcdef"
	env["JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET"] = "w14q-manual-secret-0123456789abcdef"
	// J3a proxy-latency：postgres 三库同址 + management API。
	env["JUHE_AI_PROXY_LATENCY_JOBS_OWNER"] = "go"
	env["JUHE_AI_PROXY_LATENCY_INSTANCE_ID"] = "w14q-j3a-main-e2e"
	env["JUHE_AI_PROXY_LATENCY_STORE"] = "postgres"
	env["JUHE_AI_PROXY_LATENCY_POSTGRES_URL"] = pgURL
	env["JUHE_AI_PROXY_LATENCY_CREDENTIAL_SECRET"] = "0123456789abcdef0123456789abcdef"
	env["JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_URL"] = pgURL
	env["JUHE_AI_PROXY_LATENCY_RESULT_POSTGRES_URL"] = pgURL
	env["JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED"] = "true"
	env["JUHE_AI_PROXY_LATENCY_MANAGEMENT_LISTEN_ADDRESS"] = "127.0.0.1:" + strconv.Itoa(managementPort)
	env["JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_URL"] = pgURL
	return env
}

// w14qOperationLogsSchema 摘自 gateway/internal/operationlog/schema.go 的
// postgresSchema（F4 权威 DDL，加法幂等）。
const w14qOperationLogsSchema = `CREATE SCHEMA IF NOT EXISTS juhe_dataset; CREATE TABLE IF NOT EXISTS juhe_dataset.operation_log_owner_leases (lease_key text PRIMARY KEY, owner_id text NOT NULL, fence_token bigint NOT NULL, lease_until timestamptz NOT NULL, updated_at timestamptz NOT NULL); CREATE TABLE IF NOT EXISTS juhe_dataset.operation_logs (id text PRIMARY KEY, trace_id text, actor_system_account_id text NOT NULL, actor_username text, actor_display_name text, actor_role text NOT NULL, operation_scope_system_account_id text, mode text NOT NULL, module text NOT NULL, action text NOT NULL, operation_key text NOT NULL, resource_type text NOT NULL, resource_id text, resource_name text, summary text NOT NULL, detail_level text NOT NULL, visibility_scope text NOT NULL, changes_json jsonb NOT NULL, metadata_json jsonb NOT NULL, method text, path text, status_code integer, client_ip text, user_agent text, created_at timestamptz NOT NULL); CREATE TABLE IF NOT EXISTS juhe_dataset.operation_log_targets (id text PRIMARY KEY, operation_log_id text NOT NULL REFERENCES juhe_dataset.operation_logs(id) ON DELETE CASCADE, target_type text NOT NULL,target_id text,target_name text,target_owner_system_account_id text,relation text NOT NULL,created_at timestamptz NOT NULL); CREATE TABLE IF NOT EXISTS juhe_dataset.operation_log_viewers (operation_log_id text NOT NULL REFERENCES juhe_dataset.operation_logs(id) ON DELETE CASCADE,system_account_id text NOT NULL,visibility_reason text NOT NULL,detail_level text NOT NULL,created_at timestamptz NOT NULL,PRIMARY KEY(operation_log_id,system_account_id,visibility_reason,detail_level)); CREATE TABLE IF NOT EXISTS juhe_dataset.operation_log_summary_search_terms (operation_log_id text NOT NULL REFERENCES juhe_dataset.operation_logs(id) ON DELETE CASCADE,term text NOT NULL,created_at timestamptz NOT NULL,PRIMARY KEY(term,operation_log_id))`

// TestW14QPGMainFullAssemblySmoke 驱动生产 main() 完成 J1/J2/J3a PG 装配
// 成功链并干净停机。
func TestW14QPGMainFullAssemblySmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("PG main e2e skipped in -short mode")
	}
	pgURL := w12cPGOverrideURL(t)
	if pgURL == "" {
		t.Skip("w14q: 覆盖库连接串不可用")
	}
	redisURL := w14jSharedEnvValue(t, "JUHE_AI_REDIS_STATE_URL")
	if redisURL == "" {
		t.Skip("w14q: shared.env 无 Redis state URL")
	}
	db, err := sql.Open("pgx", pgURL)
	if err != nil {
		t.Skipf("w14q: 打开覆盖库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pingCancel()
	if err := db.PingContext(pingCtx); err != nil {
		t.Skipf("w14q: 覆盖库不可达: %v", err)
	}
	// 自愈共享库形状（加法幂等 DDL）+ 清理本波次残留。
	// 外部重建只建立六个业务 schema；juhe_jobs 由 app 角色所有、表由各测试
	// EnsureSchema 自建，这里补 bootstrap 前置的 CREATE SCHEMA IF NOT EXISTS
	//（gateway w1_compose_final_test.go 同款先例，登记：加法幂等 DDL）。
	if _, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS juhe_jobs`); err != nil {
		t.Skipf("w14q: 建立 juhe_jobs schema 失败: %v", err)
	}
	// w14jPGSeedStatements 对生产形状 system_accounts 缺 created_at 列；
	// w14q 用补齐 created_at 的 seed（ON CONFLICT DO NOTHING，不改既有行）。
	for _, statement := range w14jPGSeedStatements() {
		if _, err := db.Exec(statement); err != nil {
			if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "multiple primary keys") {
				continue
			}
			t.Logf("w14q pg seed 跳过: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.system_accounts
		(id, username, display_name, role, status, password_hash, created_at, updated_at)
		VALUES ('sys_admin', 'sys_admin', 'sys_admin', 'admin', 'active', 'w14q-test-seed-not-a-login-hash', $1, $1)
		ON CONFLICT (id) DO NOTHING`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Skipf("w14q: sys_admin seed 失败（共享库形状漂移，跳过）: %v", err)
	}
	// direct schedule canonical 设置行（w12d/w9h 同惯例，幂等补齐）。
	for key, value := range map[string]string{
		"accountHealthCheckIntervalHours":      "24",
		"accountHealthCheckJitterMinutes":      "10",
		"accountHealthCheckFailureThreshold":   "3",
		"defaultTemporaryUnschedulableMinutes": "60",
		"cooldownAccountRetestMaxBackoffHours": "24",
		"usageStatsTimezone":                   "\"Asia/Shanghai\"",
	} {
		if _, err := db.Exec(`INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
			VALUES ('sys_admin', $1, $2, $3) ON CONFLICT (system_account_id, key) DO NOTHING`, key, value, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("seed direct schedule settings: %v", err)
		}
	}
	// J2 预置表：accountbalance EnsureSchema 是幂等 DDL（Go-owned 交接表，
	// record_maintenance_jobs 先例；CheckSchema 只读不建表，须先 bootstrap）。
	balanceStore, err := accountbalance.OpenStore(accountbalance.StoreConfig{Mode: accountbalance.StorePostgres, PostgresURL: pgURL})
	if err != nil {
		t.Fatalf("打开 J2 store 做 schema bootstrap: %v", err)
	}
	if err := balanceStore.EnsureSchema(context.Background()); err != nil {
		_ = balanceStore.Close()
		t.Fatalf("J2 schema bootstrap: %v", err)
	}
	if err := balanceStore.Close(); err != nil {
		t.Logf("w14q: J2 bootstrap store 关闭: %v", err)
	}
	// F4 operation_logs 审计面：J3a management CheckContract 以 EXPLAIN INSERT
	// 验证 gateway-owned 审计表写契约（不写数据）。表 DDL 逐字摘自
	// gateway/internal/operationlog/schema.go postgresSchema（加法幂等，登记）。
	for _, statement := range strings.Split(w14qOperationLogsSchema, ";") {
		if statement = strings.TrimSpace(statement); statement == "" {
			continue
		}
		if _, err := db.Exec(statement); err != nil {
			t.Logf("w14q F4 seed 跳过: %v", err)
		}
	}
	w14qCleanupPGMainRows(t, db)
	t.Cleanup(func() { w14qCleanupPGMainRows(t, db) })
	// 探针型 e2e 护栏：due 候选必须在个位数量级。
	w14qDueCandidateGuard(t, db)

	root := t.TempDir()
	managementPort := wgFreePort(t)
	env := w14qPGMainEnv(t, root, pgURL, redisURL, managementPort)
	port := wgFreePort(t)
	cmd := wgSpawnMainChild(t, env, wgScenarioOwner, port)
	payload := wgPollHealthPayload(t, port, wgChildReadyTimeout, func(current map[string]any) bool {
		worker, _ := current["worker"].(map[string]any)
		workerReady, _ := current["workerReady"].(bool)
		return current["accountHealthEnabled"] == true &&
			current["accountBalanceEnabled"] == true &&
			current["proxyLatencyEnabled"] == true &&
			current["runtimeLogOwnerHeld"] == true &&
			current["tableMonitorReady"] == true &&
			workerReady && worker != nil
	})
	// 装配证据断言：PG 直连 J1/J2/J3a 全部注册进健康载荷（runner Ready 依赖
	// 首轮扫描完成，属运行时行为，不作为装配分支的验收条件）。
	if payload["accountHealthEnabled"] != true || payload["accountBalanceEnabled"] != true || payload["proxyLatencyEnabled"] != true {
		t.Fatalf("PG 装配组件健康载荷缺失: %v", payload)
	}
	// management API 必须已在独立端口服务（装配成功证据；handler 对未知路径
	// 返回 4xx 也证明 listener 已服务）。
	managementResponse, err := http.Get("http://127.0.0.1:" + strconv.Itoa(managementPort) + "/__w14q_probe__")
	if err != nil {
		t.Fatalf("J3a management 端口未服务: %v", err)
	}
	_ = managementResponse.Body.Close()
	wgInterruptAndWait(t, cmd)
}
