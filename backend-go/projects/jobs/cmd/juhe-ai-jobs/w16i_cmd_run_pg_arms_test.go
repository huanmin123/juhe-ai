// 波次 w16i：PG 门禁的进程内 run() 臂覆盖。复用 w14q 的 w1cover seed 与
// env builder（w14qPGMainEnv），把 spawn 子进程换成进程内 run() + 注入停机
// ctx：J1(postgres store + postgres 直连 input) + model-recovery + J2 + J3a
// (+management) 全链装配、组件注册与优雅停机直接记入父进程覆盖 profile。
// 连接串只从 shared.env 读取改写；连接失败 t.Skip；凭据不落日志。
package main

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestW16HIPGFullChainOwnerShutdown 进程内驱动 run() 完成 J1/J2/J3a PG 装配
// 成功链、management API 装配与优雅停机（覆盖 w14q 同一生产路径的进程内
// 形态）。
func TestW16HIPGFullChainOwnerShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 全链 skipped in -short mode")
	}
	pgURL := w12cPGOverrideURL(t)
	if pgURL == "" {
		t.Skip("w16i: 覆盖库连接串不可用")
	}
	redisURL := w14jSharedEnvValue(t, "JUHE_AI_REDIS_STATE_URL")
	if redisURL == "" {
		t.Skip("w16i: shared.env 无 Redis state URL")
	}
	db, err := sql.Open("pgx", pgURL)
	if err != nil {
		t.Skipf("w16i: 打开覆盖库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pingCancel()
	if err := db.PingContext(pingCtx); err != nil {
		t.Skipf("w16i: 覆盖库不可达: %v", err)
	}
	if _, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS juhe_jobs`); err != nil {
		t.Skipf("w16i: 建立 juhe_jobs schema 失败: %v", err)
	}
	for _, statement := range w14jPGSeedStatements() {
		if _, err := db.Exec(statement); err != nil {
			if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "multiple primary keys") {
				continue
			}
			t.Logf("w16i pg seed 跳过: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.system_accounts
		(id, username, display_name, role, status, password_hash, created_at, updated_at)
		VALUES ('sys_admin', 'sys_admin', 'sys_admin', 'admin', 'active', 'w16i-test-seed-not-a-login-hash', $1, $1)
		ON CONFLICT (id) DO NOTHING`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Skipf("w16i: sys_admin seed 失败（共享库形状漂移，跳过）: %v", err)
	}
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
	// J2 预置表：accountbalance EnsureSchema 是幂等 DDL（CheckSchema 只读，
	// 须先 bootstrap；w14q 同款）。
	balanceStore, err := accountbalance.OpenStore(accountbalance.StoreConfig{Mode: accountbalance.StorePostgres, PostgresURL: pgURL})
	if err != nil {
		t.Fatalf("打开 J2 store 做 schema bootstrap: %v", err)
	}
	if err := balanceStore.EnsureSchema(context.Background()); err != nil {
		_ = balanceStore.Close()
		t.Fatalf("J2 schema bootstrap: %v", err)
	}
	if err := balanceStore.Close(); err != nil {
		t.Logf("w16i: J2 bootstrap store 关闭: %v", err)
	}
	// F4 operation_logs 审计面：J3a management CheckContract 依赖
	// gateway-owned 审计表（加法幂等 DDL，登记；w14q 同款）。
	for _, statement := range strings.Split(w14qOperationLogsSchema, ";") {
		if statement = strings.TrimSpace(statement); statement == "" {
			continue
		}
		if _, err := db.Exec(statement); err != nil {
			t.Logf("w16i F4 seed 跳过: %v", err)
		}
	}
	w14qCleanupPGMainRows(t, db)
	t.Cleanup(func() { w14qCleanupPGMainRows(t, db) })
	w14qDueCandidateGuard(t, db)

	env := w14qPGMainEnv(t, t.TempDir(), pgURL, redisURL, wgFreePort(t))
	w16hRunUntilReadyAndCancel(t, env, func(payload map[string]any) bool {
		worker, _ := payload["worker"].(map[string]any)
		workerReady, _ := payload["workerReady"].(bool)
		return payload["accountHealthEnabled"] == true &&
			payload["accountBalanceEnabled"] == true &&
			payload["proxyLatencyEnabled"] == true &&
			payload["runtimeLogOwnerHeld"] == true &&
			payload["tableMonitorReady"] == true &&
			workerReady && worker != nil
	}, "w16i PG 全链", func(port int) {
		w16iProbeManualBridge(t, port)
	})
}

// w16iProbeManualBridge 就绪后探测 J2 manual 桥错误臂：错误 secret → 401、
// 坏 JSON 体 → 400、尾随 JSON → 400（handler 装配自进程内 run()，锁定
// jobsHTTPHandler 的 manual 桥校验链）。
func w16iProbeManualBridge(t *testing.T, port int) {
	t.Helper()
	base := "http://127.0.0.1:" + itoa(port) + "/account-balance/manual"
	post := func(body string, authorization string) int {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, base, strings.NewReader(body))
		if err != nil {
			t.Fatalf("构造 manual 桥请求失败: %v", err)
		}
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("manual 桥请求失败: %v", err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	secret := "Bearer w14q-manual-secret-0123456789abcdef"
	if code := post(`{"input":{}}`, "Bearer wrong-secret-value-0123456789abcdef"); code != http.StatusUnauthorized {
		t.Fatalf("错误 secret 必须 401，得到 %d", code)
	}
	if code := post(`{not-json`, secret); code != http.StatusBadRequest {
		t.Fatalf("坏 JSON 必须 400，得到 %d", code)
	}
	if code := post(`{"input":{}} {"trailing":true}`, secret); code != http.StatusBadRequest {
		t.Fatalf("尾随 JSON 必须 400，得到 %d", code)
	}
	// 合法形状 + 不存在账户：进入 RunManual 真实执行路径（对缺失账户返回
	// 错误 → handler 的 5xx/4xx 错误出口）；只断言请求被受理（非 404/401/400
	// 前置校验拒绝），具体状态码由上游语义决定不锁定。字段名须匹配
	// accountbalance.Input 的 JSON 标签（DisallowUnknownFields）。
	if code := post(`{"input":{"account_id":"w16i-no-such-account","system_account_id":"sys_w16i_missing","config_revision":1,"trigger":"manual","issued_at":"2026-09-19T00:00:00Z"}}`, secret); code == http.StatusNotFound || code == http.StatusUnauthorized || code == http.StatusBadRequest {
		t.Fatalf("合法 envelope 必须进入 RunManual 执行路径，得到前置拒绝 %d", code)
	}
}

// TestW16HIPGModelRecoveryPingFailArm 覆盖 model-recovery Redis ping 失败臂
// （J1 PG 直连成功链前置 + Redis 指向不可达地址）。J1 store EnsureSchema
// 依赖外部 bootstrap 的 juhe_jobs schema，先自愈预置。
func TestW16HIPGModelRecoveryPingFailArm(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门禁臂 skipped in -short mode")
	}
	pgURL := w12cPGOverrideURL(t)
	if pgURL == "" {
		t.Skip("w16i: 覆盖库连接串不可用")
	}
	w16jEnsurePGJobsFixture(t, pgURL)
	env := w16hBaseEnv(t)
	w16hApplyEnv(t, env)
	w16hApplyEnv(t, map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":         "go",
		"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":        "w16i-mr-ping",
		"JUHE_AI_ACCOUNT_HEALTH_STORE":              "postgres",
		"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL":       pgURL,
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE":       "postgres",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL": pgURL,
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":    t.TempDir(),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY":  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA0",
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET":  "0123456789abcdef0123456789abcdef",
		"JUHE_AI_REDIS_STATE_URL":                   "redis://127.0.0.1:1",
		"JUHE_AI_REDIS_NAMESPACE":                   "juhe-ai:w16i",
	})
	w16hRunArms(t, nil, 1, "ping model-recovery Redis store")
}

// TestW16HIPGJ3aInputPingFailArm 覆盖 J3a postgres 直连 input 的 ping 失败臂
// （jobs store=w1cover 成功链前置 + input 指向不可达地址）。store CheckSchema
// 依赖 juhe_jobs 预 provision 表，先自愈预置。
func TestW16HIPGJ3aInputPingFailArm(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门禁臂 skipped in -short mode")
	}
	pgURL := w12cPGOverrideURL(t)
	if pgURL == "" {
		t.Skip("w16i: 覆盖库连接串不可用")
	}
	w16jEnsurePGJobsFixture(t, pgURL)
	env := w16hBaseEnv(t)
	w16hApplyEnv(t, env)
	w16hApplyEnv(t, map[string]string{
		"JUHE_AI_PROXY_LATENCY_ENABLED":             "true",
		"JUHE_AI_PROXY_LATENCY_JOBS_OWNER":          "go",
		"JUHE_AI_PROXY_LATENCY_INSTANCE_ID":         "w16i-j3a-ping",
		"JUHE_AI_PROXY_LATENCY_STORE":               "postgres",
		"JUHE_AI_PROXY_LATENCY_POSTGRES_URL":        pgURL,
		"JUHE_AI_PROXY_LATENCY_CREDENTIAL_SECRET":   "0123456789abcdef0123456789abcdef",
		"JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_URL":  "postgres://w16i:w16i@127.0.0.1:1/w16i",
		"JUHE_AI_PROXY_LATENCY_RESULT_POSTGRES_URL": pgURL,
	})
	w16hRunArms(t, nil, 1, "ping J3a proxy-latency direct-input database")
}

// TestW16HIPGJ1InputPingFailArm 覆盖 J1 postgres 直连 input 的 ping 失败臂
// （store=w1cover 成功链前置 + input 指向不可达地址）。store EnsureSchema
// 依赖外部 bootstrap 的 juhe_jobs schema，先自愈预置。
func TestW16HIPGJ1InputPingFailArm(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门禁臂 skipped in -short mode")
	}
	pgURL := w12cPGOverrideURL(t)
	if pgURL == "" {
		t.Skip("w16i: 覆盖库连接串不可用")
	}
	w16jEnsurePGJobsFixture(t, pgURL)
	env := w16hBaseEnv(t)
	w16hApplyEnv(t, env)
	w16hApplyEnv(t, map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":         "go",
		"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":        "w16i-j1-ping",
		"JUHE_AI_ACCOUNT_HEALTH_STORE":              "postgres",
		"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL":       pgURL,
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE":       "postgres",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL": "postgres://w16i:w16i@127.0.0.1:1/w16i",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":    t.TempDir(),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY":  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA0",
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET":  "0123456789abcdef0123456789abcdef",
	})
	w16hRunArms(t, nil, 1, "ping J1 account-health direct-input database")
}

// TestW16HIPGJ3aManagementFailArms 覆盖 J3a management 装配失败臂：store 三
// 库均用 w1cover 成功链前置，management PostgreSQL 指向不可达地址 → source
// 初始化或契约校验失败（两条错误出口之一，均收敛为退出码 1）。store 侧依赖
// juhe_jobs 预 provision 表，先自愈预置。
func TestW16HIPGJ3aManagementFailArms(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门禁臂 skipped in -short mode")
	}
	pgURL := w12cPGOverrideURL(t)
	if pgURL == "" {
		t.Skip("w16i: 覆盖库连接串不可用")
	}
	w16jEnsurePGJobsFixture(t, pgURL)
	managementPort := wgFreePort(t)
	env := w16hBaseEnv(t)
	w16hApplyEnv(t, env)
	w16hApplyEnv(t, map[string]string{
		"JUHE_AI_PROXY_LATENCY_ENABLED":                   "true",
		"JUHE_AI_PROXY_LATENCY_JOBS_OWNER":                "go",
		"JUHE_AI_PROXY_LATENCY_INSTANCE_ID":               "w16i-j3a-mgmt",
		"JUHE_AI_PROXY_LATENCY_STORE":                     "postgres",
		"JUHE_AI_PROXY_LATENCY_POSTGRES_URL":              pgURL,
		"JUHE_AI_PROXY_LATENCY_CREDENTIAL_SECRET":         "0123456789abcdef0123456789abcdef",
		"JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_URL":        pgURL,
		"JUHE_AI_PROXY_LATENCY_RESULT_POSTGRES_URL":       pgURL,
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED":        "true",
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_LISTEN_ADDRESS": "127.0.0.1:" + itoa(managementPort),
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_URL":   "postgres://w16i:w16i@127.0.0.1:1/w16i",
	})
	w16hRunArms(t, nil, 1, "J3a management")
}
