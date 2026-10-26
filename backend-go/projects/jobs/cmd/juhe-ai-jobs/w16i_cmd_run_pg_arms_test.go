// 波次 w16i：PG 门禁的进程内 run() 臂覆盖。复用 w14q 的 env builder
// （w14qPGMainEnv）与 wgCmdKitDB 隔离测试库（原 w1cover 共享覆盖库 seed 已
// 退役），把 spawn 子进程换成进程内 run() + 注入停机 ctx：J1(postgres store
// + postgres 直连 input) + model-recovery + J2 + J3a (+management) 全链装配、
// 组件注册与优雅停机直接记入父进程覆盖 profile。凭据不落日志。
package main

import (
	"context"
	"database/sql"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	_ "github.com/jackc/pgx/v5/stdlib"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// w16iKitDBWithContractSurface 打开 wgCmdKitDB 隔离测试库并补齐 run() 装配链
// 在各测试目标失败臂之前必须通过的外部 bootstrap 面（J2 accountbalance
// EnsureSchema 与 F4 operation_logs 审计面），使失败收敛到目标臂而非更早的
// J2/管理面契约缺失（w14q/w16i 全链同款前置）。
func w16iKitDBWithContractSurface(t *testing.T) (*sql.DB, string) {
	t.Helper()
	db, pgURL := wgCmdKitDB(t)
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
	for _, statement := range strings.Split(w14qOperationLogsSchema, ";") {
		if statement = strings.TrimSpace(statement); statement == "" {
			continue
		}
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("w16i F4 seed 失败: %v", err)
		}
	}
	return db, pgURL
}

// TestW16HIPGFullChainOwnerShutdown 进程内驱动 run() 完成 J1/J2/J3a PG 装配
// 成功链、management API 装配与优雅停机（覆盖 w14q 同一生产路径的进程内
// 形态）。
// w16iBaseEnv 构造 PG 唯一形态的 owner env 基底：F1 store 与 J1 store/input
// 全部走 wgCmdKitDB 隔离测试库（原 w16hBaseEnv 的 sqlite 基底随 sqlite 形态
// 退役删除；连接串不落日志）。
func w16iBaseEnv(t *testing.T, pgURL string) map[string]string {
	t.Helper()
	return map[string]string{
		"JUHE_AI_POSTGRES_URL":                      pgURL,
		"JUHE_AI_RUNTIME_LOG_POSTGRES_URL":          pgURL,
		"JUHE_AI_RUNTIME_LOG_INSTANCE_ID":           "w16i-run",
		"JUHE_AI_LOG_DIR":                           t.TempDir(),
		"JUHE_AI_CHAT_ASSETS_ROOT":                  filepath.Join(t.TempDir(), "chat-assets"),
		"JUHE_AI_CODEX_CONTEXT_ROOT":                filepath.Join(t.TempDir(), "codex-context"),
		"JUHE_AI_SECRET":                            "0123456789abcdef0123456789abcdef",
		"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":        "w16i-j1",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":    t.TempDir(),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY":  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA0",
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET":  "0123456789abcdef0123456789abcdef",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL": pgURL,
	}
}

func TestW16HIPGFullChainOwnerShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 全链 skipped in -short mode")
	}
	db, pgURL := wgCmdKitDB(t)
	redisURL := w14jSharedEnvValue(t, "JUHE_AI_REDIS_STATE_URL")
	if redisURL == "" {
		t.Skip("w16i: shared.env 无 Redis state URL")
	}
	// 子进程装配链的外部 bootstrap 前置（w14q 同款）：J2 accountbalance
	// EnsureSchema（CheckSchema 只读，须先 bootstrap）与 F4 operation_logs
	// 审计面（J3a management CheckContract 依赖 gateway-owned 审计表）。
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
	for _, statement := range strings.Split(w14qOperationLogsSchema, ";") {
		if statement = strings.TrimSpace(statement); statement == "" {
			continue
		}
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("w16i F4 seed 失败: %v", err)
		}
	}
	w14qDueCandidateGuard(t, db)

	env := w14qPGMainEnv(t, t.TempDir(), pgURL, redisURL, wgFreePort(t))
	w16hRunUntilReadyAndCancel(t, env, func(payload map[string]any) bool {
		worker, _ := payload["worker"].(map[string]any)
		workerReady, _ := payload["workerReady"].(bool)
		return payload["accountHealthEnabled"] == true &&
			payload["accountBalanceEnabled"] == true &&
			payload["proxyLatencyEnabled"] == true &&
			payload["runtimeLogOwnerHeld"] == true &&
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
	_, pgURL := wgCmdKitDB(t)
	env := w16iBaseEnv(t, pgURL)
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
// （jobs store=隔离测试库成功链前置 + input 指向不可达地址）。store CheckSchema
// 依赖 juhe_jobs 预 provision 表，先自愈预置。
func TestW16HIPGJ3aInputPingFailArm(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门禁臂 skipped in -short mode")
	}
	_, pgURL := w16iKitDBWithContractSurface(t)
	env := w16iBaseEnv(t, pgURL)
	w16hApplyEnv(t, env)
	w16hApplyEnv(t, map[string]string{
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
// （store=隔离测试库成功链前置 + input 指向不可达地址）。store EnsureSchema
// 依赖外部 bootstrap 的 juhe_jobs schema，先自愈预置。
func TestW16HIPGJ1InputPingFailArm(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门禁臂 skipped in -short mode")
	}
	_, pgURL := wgCmdKitDB(t)
	env := w16iBaseEnv(t, pgURL)
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
// 库均用隔离测试库成功链前置，management PostgreSQL 指向不可达地址 → source
// 初始化或契约校验失败（两条错误出口之一，均收敛为退出码 1）。store 侧依赖
// juhe_jobs 预 provision 表，先自愈预置。
func TestW16HIPGJ3aManagementFailArms(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门禁臂 skipped in -short mode")
	}
	_, pgURL := w16iKitDBWithContractSurface(t)
	managementPort := wgFreePort(t)
	env := w16iBaseEnv(t, pgURL)
	w16hApplyEnv(t, env)
	w16hApplyEnv(t, map[string]string{
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
