//go:build windows

package main

// w1_boot_chain_test.go —— juhe-ai-gateway owner 模式二进制级全链路请求覆盖
// 测试（TestW1LBootChainFullRequestCoverage）：复用 w1_boot_cover_test.go 的
// 插桩二进制与覆盖目录登记帮手（w1bBuildCoverBinary / w1bCoverageDir /
// w1bScenarioEnv / w1bAppendCoverageManifest / w1bFreePort /
// w1bSetNewProcessGroup / w1bSendCtrlBreak / w1bWriteCutoverEvidence）和
// w1_boot_owner_test.go 的 owner 全栈 fixture（w1v2PrepareBusinessSQLite /
// w1v2PrepareJ3bSQLite / w1v2SeedCircuitRuntimeIndex / w1v2OwnerFailMarkers），
// 在 owner 启动 env 之上追加 JUHE_AI_GATEWAY_CHAIN_ENABLED=true 与
// JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS=true（NODE_ENV 未设置即非
// production，允许 httptest loopback 上游），把真实的
// POST /v1/chat/completions 请求穿过 preauth → 候选 → dispatch → 上游 →
// 响应/用量全链。
//
// 业务库补种子（w1lSeedChainBusinessRows）在 maintenance 真实 DDL
// ensure+seed（w1v2PrepareBusinessSQLite）之后执行，INSERT 列名全部按
// bootstrap SQLiteSchemaBusiness 的真实列适配：
//
//   - 账户 credentials_encrypted 指向进程内 httptest 上游
//     （http://127.0.0.1:<port>，二进制子进程跨进程可达），加密 secret 与
//     JUHE_AI_SECRET 一致（selector 用 cfg.Secret 解密）；
//   - accounts.provider_protocol_profile_id 使用 seed 写入的
//     profile_openai_openai_v1（外键 → provider_protocol_profiles.id）；
//   - api_keys.key_hash 用 gatewayruntimecache.HashSecret(明文)；
//   - provider_model_catalog 补 (openai, gpt-test) active 行。
//
// 场景（同一 owner 进程内按顺序驱动，失败确认次序保证 client-IP 账户回避
// 不污染后续断言）：
//
//	a  key(w1l_key_main  → w1l_rs_main  → w1l_group_main)  model=gpt-test
//	   上游 200 → 断言 200 且 body 含上游内容（dispatch 成功主链、首字节、
//	   用量完成路径）。
//	b2 key(w1l_key_failover → w1l_rs_failover → [w1l_group_fb_bad(priority 1,
//	   acc_bad 502) → w1l_group_fb_ok(priority 2, acc_ok)])  model=gpt-test
//	   断言 200 且 body 含上游内容（失败派发 → 组耗尽 → switchToFallbackGroup
//	   跨组回退成功）。
//	b1 key(w1l_key_solo → w1l_rs_solo → w1l_group_solo_bad 仅 acc_bad)
//	   model=gpt-test，上游恒 502 → 断言 503 耗尽契约「上游暂时不可用，请
//	   重试」，上游诊断文案不外泄（失败派发/失败用量/重试预算臂）。
//	c  key(w1l_key_main) model=w1l-unknown-model → 断言 503 模型不匹配契约
//	   （capability gate 返回 code=model_unsupported + 固定文案「当前分组
//	   无账户支持请求路径或客户端协议」，候选窗口被清空）。
//
// 每个请求后不强求异步用量落盘断言（spool 面由 GOCOVERDIR 计数收割覆盖）。
// 关闭：CTRL_BREAK_EVENT 优雅退出，断言 exit 0、无 fail 输出。全程有界：
// 30s 就绪轮询 + 30s 退出等待 + 85s 进程总预算，单请求 30s 客户端超时。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"

	_ "modernc.org/sqlite"
)

const (
	w1lOwnerEpoch      = "epoch-w1l"
	w1lRedisNS         = "w1l"
	w1lRuntimeSecret   = "w1l-owner-chain-boot-secret"
	w1lUpstreamGoodKey = "sk-w1l-upstream-good"
	w1lUpstreamBadKey  = "sk-w1l-upstream-bad"
	w1lUpstreamContent = "W1L 全链路上游内容"
	w1lUpstreamLeak    = "w1l 上游 502 故障"
	// w1lProfileOpenAIV1 是 seed 写入 provider_protocol_profiles 的 openai v1
	// 档案 id（pgSeedProfiles），accounts 外键必须指向它。
	w1lProfileOpenAIV1 = "profile_openai_openai_v1"
	// w1lSeedTimestamp 是补种子的固定 created_at/updated_at（真实 DDL 的
	// NOT NULL 时间列；RFC3339 毫秒 UTC，与 seedTimestamp 输出同形）。
	w1lSeedTimestamp = "2026-09-14T00:00:00.000Z"
)

// w1lGatewayKeySecrets 是三个网关 API Key 的明文（请求 Authorization 用）。
var w1lGatewayKeySecrets = map[string]string{
	"w1l_key_main":     "sk-w1l-key-main-0001",
	"w1l_key_solo":     "sk-w1l-key-solo-0002",
	"w1l_key_failover": "sk-w1l-key-fail-0003",
}

// w1lUpstreamBody 构造上游 200 的 chat.completion JSON 响应体。
func w1lUpstreamBody() string {
	return fmt.Sprintf(`{"id":"chatcmpl-w1l","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`, w1lUpstreamContent)
}

// w1lStartUpstream 启动进程内 httptest 上游：携带 w1lUpstreamGoodKey 的请求
// 返回 200 chat.completion；其余 Authorization 返回 502 故障体（用请求头里的
// 账户 api_key 区分 w1l_acc_ok 与 w1l_acc_bad 两条派发路径）。返回上游
// loopback 基地址与两条路径的原子计数。
func w1lStartUpstream(t *testing.T) (string, *int32, *int32) {
	t.Helper()
	var bad, good int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+w1lUpstreamGoodKey {
			atomic.AddInt32(&bad, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":{"message":%q}}`, w1lUpstreamLeak)))
			return
		}
		atomic.AddInt32(&good, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(w1lUpstreamBody()))
	}))
	t.Cleanup(upstream.Close)
	return upstream.URL, &bad, &good
}

// w1lSeedChainBusinessRows 在 maintenance ensure+seed 之后的业务库上补
// chain 链路运行行（真实 DDL 列名）：4 组、2 账户、3 策略、3 API Key、
// 1 模型目录行，并回读行数校验落库结果。
func w1lSeedChainBusinessRows(t *testing.T, path, upstreamBaseURL string) {
	t.Helper()
	db, err := bootstrap.OpenSQLiteFile(path)
	if err != nil {
		t.Fatalf("打开业务库补种子失败: %v", err)
	}
	defer db.Close()
	now := w1lSeedTimestamp
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("补种子失败: %v, statement=%s", err, query)
		}
	}

	// ---- 分组：主组（场景 a/c）、solo 故障组（场景 b1）、回退两组（场景 b2） ----
	for _, group := range []struct{ id, name string }{
		{"w1l_group_main", "W1L 主分组"},
		{"w1l_group_solo_bad", "W1L 单故障分组"},
		{"w1l_group_fb_bad", "W1L 回退故障分组"},
		{"w1l_group_fb_ok", "W1L 回退正常分组"},
	} {
		seed(`INSERT INTO groups (id, system_account_id, name, provider_code, enabled, group_type, created_at, updated_at)
			VALUES (?, 'sys_admin', ?, 'openai', 1, 'personal', ?, ?)`, group.id, group.name, now, now)
	}

	// ---- 账户：acc_ok（上游 200）与 acc_bad（上游 502） ----
	credOK, err := accounts.EncryptJSON(w1lRuntimeSecret, map[string]any{
		"api_key":  w1lUpstreamGoodKey,
		"base_url": upstreamBaseURL,
	})
	if err != nil {
		t.Fatalf("加密 acc_ok 凭据失败: %v", err)
	}
	credBad, err := accounts.EncryptJSON(w1lRuntimeSecret, map[string]any{
		"api_key":  w1lUpstreamBadKey,
		"base_url": upstreamBaseURL,
	})
	if err != nil {
		t.Fatalf("加密 acc_bad 凭据失败: %v", err)
	}
	for _, account := range []struct{ id, name, credentials string }{
		{"w1l_acc_ok", "W1L 正常账户", credOK},
		{"w1l_acc_bad", "W1L 故障账户", credBad},
	} {
		seed(`INSERT INTO accounts (
				id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
				name, type, status, schedulable, credentials_encrypted, health_check_model, health_check_endpoint_mode,
				created_at, updated_at
			) VALUES (?, 'sys_admin', 'openai', ?, 'openai', 'v1', ?, 'api_key', 'active', 1, ?, 'gpt-test', 'chat_json', ?, ?)`,
			account.id, w1lProfileOpenAIV1, account.name, account.credentials, now, now)
	}

	// ---- 组成员 + 支持模型（PK (account_id, model)，每账户一行） ----
	for _, member := range [][2]string{
		{"w1l_group_main", "w1l_acc_ok"},
		{"w1l_group_solo_bad", "w1l_acc_bad"},
		{"w1l_group_fb_bad", "w1l_acc_bad"},
		{"w1l_group_fb_ok", "w1l_acc_ok"},
	} {
		seed(`INSERT INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at)
			VALUES ('sys_admin', ?, ?, 1, ?, ?)`, member[0], member[1], now, now)
	}
	for _, accountID := range []string{"w1l_acc_ok", "w1l_acc_bad"} {
		seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at)
			VALUES (?, 'openai', 'gpt-test', ?)`, accountID, now)
	}

	// ---- 路由策略 + 组绑定 + API Key ----
	type w1lGroupBinding struct {
		bindingID, groupID string
		priority           int
	}
	for _, strategy := range []struct {
		id, name, mode string
		keyID          string
		bindings       []w1lGroupBinding
	}{
		{"w1l_rs_main", "W1L 主路由", "normal", "w1l_key_main",
			[]w1lGroupBinding{{"w1l_rsg_main", "w1l_group_main", 1}}},
		{"w1l_rs_solo", "W1L 单故障路由", "normal", "w1l_key_solo",
			[]w1lGroupBinding{{"w1l_rsg_solo", "w1l_group_solo_bad", 1}}},
		{"w1l_rs_failover", "W1L 回退路由", "failover", "w1l_key_failover",
			[]w1lGroupBinding{
				{"w1l_rsg_fb_bad", "w1l_group_fb_bad", 1},
				{"w1l_rsg_fb_ok", "w1l_group_fb_ok", 2},
			}},
	} {
		seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, status, created_at, updated_at)
			VALUES (?, 'sys_admin', ?, ?, 'active', ?, ?)`, strategy.id, strategy.name, strategy.mode, now, now)
		for _, binding := range strategy.bindings {
			seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
				VALUES (?, ?, 'sys_admin', ?, ?, 1, 'active', ?, ?)`,
				binding.bindingID, strategy.id, binding.groupID, binding.priority, now, now)
		}
		secret := w1lGatewayKeySecrets[strategy.keyID]
		keySecretEncrypted, encryptErr := accounts.EncryptJSON(w1lRuntimeSecret, map[string]any{"key": secret})
		if encryptErr != nil {
			t.Fatalf("加密 API Key %s 密文失败: %v", strategy.keyID, encryptErr)
		}
		seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix, key_secret_encrypted, status, created_at, updated_at)
			VALUES (?, 'sys_admin', ?, ?, ?, ?, ?, ?, 'active', ?, ?)`,
			strategy.keyID, strategy.id, strategy.name+" Key",
			gatewayruntimecache.HashSecret(secret), secret[:8], secret[len(secret)-8:],
			keySecretEncrypted, now, now)
	}

	// ---- 模型目录：请求模型 gpt-test 的 active 目录行 ----
	seed(`INSERT INTO provider_model_catalog (id, provider_code, model, status, catalog_order, supported_api_protocols_json, source, catalog_visible, created_at, updated_at)
		VALUES ('w1l_cat_gpt_test', 'openai', 'gpt-test', 'active', 0, '["chat_completions"]', 'builtin', 1, ?, ?)`, now, now)

	// ---- 回读校验：关键行确实落库 ----
	checks := []struct {
		table, where string
		want         int
	}{
		{"groups", "id LIKE 'w1l_%'", 4},
		{"accounts", "id LIKE 'w1l_%'", 2},
		{"group_accounts", "group_id LIKE 'w1l_%'", 4},
		{"api_keys", "id LIKE 'w1l_%'", 3},
		{"route_strategies", "id LIKE 'w1l_%'", 3},
		{"provider_model_catalog", "id = 'w1l_cat_gpt_test'", 1},
	}
	for _, check := range checks {
		var count int
		if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", check.table, check.where)).Scan(&count); err != nil {
			t.Fatalf("回读 %s 失败: %v", check.table, err)
		}
		if count != check.want {
			t.Fatalf("回读 %s（%s）行数=%d，期望 %d", check.table, check.where, count, check.want)
		}
	}
}

// w1lOwnerChainEnv 组装 owner 全栈 + chain 的子进程 env：在 TestW1BOwnerFullBoot
// 的 env 契约之上追加 JUHE_AI_GATEWAY_CHAIN_ENABLED=true（挂载 /v1 编排器）
// 与 JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS=true（httptest loopback 上游，
// 非 production 信号下允许）。
func w1lOwnerChainEnv(t *testing.T, coverageDir, root, businessPath string, cacheRedis, stateRedis *miniredis.Miniredis, mainPort int) []string {
	t.Helper()
	for _, dir := range []string{
		filepath.Join(root, "codex-shards"),
		filepath.Join(root, "usage-shards"),
		filepath.Join(root, "audit-blobs"),
		filepath.Join(root, "audit-hot"),
		filepath.Join(root, "usage-spool"),
		filepath.Join(root, "logs"),
	} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("创建运行时目录 %s 失败: %v", dir, err)
		}
	}
	runtimeLogPath := filepath.Join(root, "runtime-log.sqlite")
	if err := os.WriteFile(runtimeLogPath, nil, 0o644); err != nil {
		t.Fatalf("预建 runtime-log 空文件失败: %v", err)
	}
	j3bPath := filepath.Join(root, "j3b-dedicated.sqlite")
	w1v2PrepareJ3bSQLite(t, j3bPath)
	for _, evidenceDir := range []string{
		filepath.Join(root, "business-evidence"),
		filepath.Join(root, "j3b-evidence"),
	} {
		if err := os.MkdirAll(evidenceDir, 0o750); err != nil {
			t.Fatalf("创建 cutover evidence 目录 %s 失败: %v", evidenceDir, err)
		}
	}
	businessEvidence := w1bWriteCutoverEvidence(t, filepath.Join(root, "business-evidence"), w1lOwnerEpoch)
	j3bEvidence := w1bWriteCutoverEvidence(t, filepath.Join(root, "j3b-evidence"), w1lOwnerEpoch)
	w1v2SeedCircuitRuntimeIndex(t, stateRedis.Addr(), w1lRedisNS)

	healthPort := w1bFreePort(t)
	j3bPort := w1bFreePort(t)
	return w1bScenarioEnv(t, coverageDir,
		// runtime 组合根（loadRuntimeConfig）
		"JUHE_AI_RUNTIME_MODE=performance",
		"JUHE_AI_DATABASE_DRIVER=sqlite",
		"JUHE_AI_CACHE_DRIVER=redis",
		"JUHE_AI_RUNTIME_STATE_DRIVER=redis",
		"JUHE_AI_REDIS_CACHE_URL=redis://"+cacheRedis.Addr(),
		"JUHE_AI_REDIS_STATE_URL=redis://"+stateRedis.Addr(),
		"JUHE_AI_REDIS_NAMESPACE=juhe-ai:"+w1lRedisNS,
		"JUHE_AI_SECRET="+w1lRuntimeSecret,
		"JUHE_AI_HOST=127.0.0.1",
		fmt.Sprintf("JUHE_AI_PORT=%d", mainPort),
		fmt.Sprintf("JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=127.0.0.1:%d", healthPort),
		"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true",
		"JUHE_AI_GATEWAY_CHAIN_ENABLED=true",
		"JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS=true",
		"JUHE_AI_AUTH_CAPTCHA_DISABLED=true",
		// 六库角色路径（业务库复用补种子的 business.sqlite）
		"JUHE_AI_DATABASE_PATH="+businessPath,
		"JUHE_AI_CHAT_DATABASE_PATH="+filepath.Join(root, "chat.sqlite"),
		"JUHE_AI_DATASET_DATABASE_PATH="+filepath.Join(root, "dataset.sqlite"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH="+filepath.Join(root, "usage-catalog.sqlite"),
		"JUHE_AI_STATS_DATABASE_PATH="+filepath.Join(root, "stats.sqlite"),
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH="+filepath.Join(root, "table-monitor.sqlite"),
		"JUHE_AI_RUNTIME_LOG_DATABASE_PATH="+runtimeLogPath,
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT="+filepath.Join(root, "codex-shards"),
		// business owner handoff（businessOwnerGate + 证据）
		"JUHE_AI_BUSINESS_OWNER=gateway",
		"JUHE_AI_BUSINESS_DATABASE_PATH="+businessPath,
		"JUHE_AI_BUSINESS_HANDOFF_CONFIRMED=true",
		"JUHE_AI_BUSINESS_NODE_WRITER_STOPPED=true",
		"JUHE_AI_BUSINESS_SCHEMA_READY=true",
		"JUHE_AI_BUSINESS_OWNER_EPOCH="+w1lOwnerEpoch,
		"JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH="+businessEvidence,
		// J3b owner
		"JUHE_AI_J3B_ENABLED=true",
		"JUHE_AI_J3B_OWNER=gateway",
		"JUHE_AI_J3B_INSTANCE_ID=w1l-owner-chain",
		"JUHE_AI_J3B_STORE=sqlite",
		"JUHE_AI_J3B_DATABASE_PATH="+j3bPath,
		"JUHE_AI_J3B_BUSINESS_DATABASE_PATH="+businessPath,
		"JUHE_AI_J3B_CREDENTIAL_SECRET=w1l-credential-secret-value",
		"JUHE_AI_J3B_IDENTITY_SECRET=w1l-identity-secret-value",
		"JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED=true",
		"JUHE_AI_J3B_NODE_WRITER_STOPPED=true",
		"JUHE_AI_J3B_OWNER_EPOCH="+w1lOwnerEpoch,
		"JUHE_AI_J3B_CUTOVER_EVIDENCE_PATH="+j3bEvidence,
		"JUHE_AI_J3B_SCHEMA_READY=true",
		"JUHE_AI_J3B_HEALTH_BOUNDARY_READY=true",
		"JUHE_AI_J3B_RUNTIME_READY=true",
		"JUHE_AI_J3B_CIRCUIT_REDIS_URL=redis://"+stateRedis.Addr(),
		"JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE="+w1lRedisNS,
		fmt.Sprintf("JUHE_AI_J3B_MANAGEMENT_LISTEN_ADDRESS=127.0.0.1:%d", j3bPort),
		// F3 审计 + F4 操作日志（sqlite 模式）
		"JUHE_AI_AUDIT_LOG_STORE=sqlite",
		"JUHE_AI_AUDIT_LOG_DATABASE_PATH="+filepath.Join(root, "audit.sqlite"),
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY="+filepath.Join(root, "audit-blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY="+filepath.Join(root, "audit-hot"),
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH="+businessPath,
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID=w1l-owner-chain",
		"JUHE_AI_OPERATION_LOG_STORE=sqlite",
		"JUHE_AI_OPERATION_LOG_DATABASE_PATH="+filepath.Join(root, "operation.sqlite"),
		"JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH="+businessPath,
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID=w1l-owner-chain",
		"JUHE_AI_USAGE_SHARD_ROOT="+filepath.Join(root, "usage-shards"),
		// chain 协作面（spool / 日志 grep 目录）
		"JUHE_AI_USAGE_SPOOL_DIRECTORY="+filepath.Join(root, "usage-spool"),
		"JUHE_AI_LOG_DIR="+filepath.Join(root, "logs"),
	)
}

// w1lOwnerGatewayProcess 携带运行中的 owner 网关子进程句柄、有界等待通道
// 与进程总预算 ctx（超时判定必须在 cancel 之前读 ctx.Err，同 W1V2）。
type w1lOwnerGatewayProcess struct {
	cmd           *exec.Cmd
	done          chan error
	cancel        context.CancelFunc
	ctx           context.Context
	stdout        *bytes.Buffer
	stderr        *bytes.Buffer
	healthAddress string
	mainAddress   string
}

// w1lStartOwnerGateway 启动插桩二进制的 owner 分支并轮询 /health
// ready=true（30s 有界，超时即杀并 Fatal），随后断言管理面 200。
func w1lStartOwnerGateway(t *testing.T, exe string, env []string, mainPort int) *w1lOwnerGatewayProcess {
	t.Helper()
	healthAddress := ""
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS="); ok {
			healthAddress = value
		}
	}
	if healthAddress == "" {
		t.Fatalf("场景 W1L env 缺少 JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS")
	}
	mainAddress := fmt.Sprintf("127.0.0.1:%d", mainPort)
	ctx, cancel := context.WithTimeout(context.Background(), w1v2BootTotalTimeout)
	cmd := exec.CommandContext(ctx, exe)
	cmd.Env = env
	w1bSetNewProcessGroup(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if startErr := cmd.Start(); startErr != nil {
		cancel()
		t.Fatalf("场景 W1L 启动 owner 网关失败: %v", startErr)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	client := &http.Client{Timeout: 2 * time.Second}
	healthReady := false
	pollDeadline := time.Now().Add(w1v2BootPollTimeout)
	for time.Now().Before(pollDeadline) {
		response, err := client.Get("http://" + healthAddress + "/health")
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err == nil && readErr == nil && response.StatusCode == http.StatusOK {
				var payload map[string]any
				if json.Unmarshal(body, &payload) == nil && payload["ready"] == true {
					healthReady = true
					break
				}
			}
		}
		select {
		case waitErr := <-done:
			cancel()
			t.Fatalf("场景 W1L owner 网关在健康就绪前退出: %v\nstdout=%s\nstderr=%s", waitErr, stdout.String(), stderr.String())
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !healthReady {
		_ = cmd.Process.Kill()
		<-done
		cancel()
		t.Fatalf("场景 W1L 健康端点 %s 在 %s 内未就绪（ready=true）\nstdout=%s\nstderr=%s", healthAddress, w1v2BootPollTimeout, stdout.String(), stderr.String())
	}
	managementResponse, managementErr := client.Get("http://" + mainAddress + "/__aisys__/api/health")
	if managementErr != nil {
		_ = cmd.Process.Kill()
		<-done
		cancel()
		t.Fatalf("场景 W1L 管理面 /__aisys__/api/health 探活失败: %v", managementErr)
	}
	_, _ = io.Copy(io.Discard, managementResponse.Body)
	_ = managementResponse.Body.Close()
	if managementResponse.StatusCode != http.StatusOK {
		_ = cmd.Process.Kill()
		<-done
		cancel()
		t.Fatalf("场景 W1L 管理面 /__aisys__/api/health 期望 200，实际 %d", managementResponse.StatusCode)
	}
	return &w1lOwnerGatewayProcess{
		cmd:           cmd,
		done:          done,
		cancel:        cancel,
		ctx:           ctx,
		stdout:        &stdout,
		stderr:        &stderr,
		healthAddress: healthAddress,
		mainAddress:   mainAddress,
	}
}

// w1lPostChatCompletions 向 owner 主监听发送一次真实
// POST /v1/chat/completions（Bearer 网关 Key + JSON body），返回状态码与响应体。
func w1lPostChatCompletions(t *testing.T, mainAddress, apiKeySecret, model string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "http://"+mainAddress+"/v1/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"W1L 链路请求"}]}`, model)))
	if err != nil {
		t.Fatalf("构建 /v1/chat/completions 请求失败: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+apiKeySecret)
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions（model=%s）失败: %v", model, err)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		t.Fatalf("读取 /v1/chat/completions 响应体失败: %v", readErr)
	}
	return response.StatusCode, string(body)
}

// w1lStopOwnerGateway 以 CTRL_BREAK_EVENT 触发优雅关闭并断言 exit 0、无
// fail 输出（30s 有界，超时强杀）。
func w1lStopOwnerGateway(t *testing.T, proc *w1lOwnerGatewayProcess) {
	t.Helper()
	if err := w1bSendCtrlBreak(proc.cmd.Process.Pid); err != nil {
		_ = proc.cmd.Process.Kill()
		<-proc.done
		proc.cancel()
		t.Fatalf("场景 W1L GenerateConsoleCtrlEvent(CTRL_BREAK) 失败: %v", err)
	}
	select {
	case waitErr := <-proc.done:
		// 超时判定必须在 cancel 之前取值（同 W1V2 场景注释）。
		ctxErr := proc.ctx.Err()
		proc.cancel()
		if ctxErr != nil {
			t.Fatalf("场景 W1L 超过 %s 有界等待，进程已被终止", w1v2BootTotalTimeout)
		}
		exitCode := 0
		if waitErr != nil {
			exitErr, ok := waitErr.(*exec.ExitError)
			if !ok {
				t.Fatalf("场景 W1L 等待进程退出失败: %v", waitErr)
			}
			exitCode = exitErr.ExitCode()
		}
		if exitCode != 0 {
			t.Fatalf("场景 W1L 优雅关闭期望 exit 0，实际 %d\nstdout=%s\nstderr=%s", exitCode, proc.stdout.String(), proc.stderr.String())
		}
	case <-time.After(w1v2BootStopTimeout):
		_ = proc.cmd.Process.Kill()
		<-proc.done
		proc.cancel()
		t.Fatalf("场景 W1L CTRL_BREAK 后 %s 内未退出，已强杀\nstdout=%s\nstderr=%s", w1v2BootStopTimeout, proc.stdout.String(), proc.stderr.String())
	}

	// ---- 输出契约：无 fail 文案（复用 W1V2 的 owner fail 标记采样） ----
	combined := proc.stdout.String() + proc.stderr.String()
	for _, marker := range w1v2OwnerFailMarkers {
		if bytes.Contains([]byte(combined), []byte(marker)) {
			t.Fatalf("场景 W1L owner 启动路径出现 fail 输出 %q\nstdout=%s\nstderr=%s", marker, proc.stdout.String(), proc.stderr.String())
		}
	}
}

// TestW1LBootChainFullRequestCoverage 场景 W1L：owner 全栈 + chain 启动 →
// 四个真实 /v1/chat/completions 请求（成功主链 / 跨组回退 / 单组耗尽 /
// 未知模型契约）→ CTRL_BREAK 优雅关闭 exit 0。
func TestW1LBootChainFullRequestCoverage(t *testing.T) {
	exe := w1bBuildCoverBinary(t)
	root := t.TempDir()

	// ---- 进程内 httptest 上游（bad/good 两条路径原子计数） ----
	upstreamBaseURL, badHits, goodHits := w1lStartUpstream(t)

	// ---- fixture：maintenance ensure+seed 业务库 + chain 补种子 ----
	businessPath := filepath.Join(root, "business.sqlite")
	w1v2PrepareBusinessSQLite(t, businessPath)
	w1lSeedChainBusinessRows(t, businessPath, upstreamBaseURL)

	// ---- fixture：miniredis cache/state 两实例 ----
	cacheRedis := miniredis.RunT(t)
	stateRedis := miniredis.RunT(t)

	// ---- 启动 owner 全栈 + chain（GOCOVERDIR 走 w1b 机制） ----
	mainPort := w1bFreePort(t)
	coverageDir := w1bCoverageDir(t, "W1L-owner-chain-full")
	env := w1lOwnerChainEnv(t, coverageDir, root, businessPath, cacheRedis, stateRedis, mainPort)
	proc := w1lStartOwnerGateway(t, exe, env, mainPort)

	// ---- 场景 a：成功主链（200 + 上游内容） ----
	status, body := w1lPostChatCompletions(t, proc.mainAddress, w1lGatewayKeySecrets["w1l_key_main"], "gpt-test")
	if status != http.StatusOK {
		t.Fatalf("场景 W1L-a 期望 200，实际 %d，body=%s", status, body)
	}
	if !strings.Contains(body, w1lUpstreamContent) {
		t.Fatalf("场景 W1L-a 响应缺少上游内容 %q，body=%s", w1lUpstreamContent, body)
	}
	if !strings.Contains(body, `"object":"chat.completion"`) {
		t.Fatalf("场景 W1L-a 响应不是 chat.completion 契约，body=%s", body)
	}
	if atomic.LoadInt32(goodHits) < 1 {
		t.Fatalf("场景 W1L-a 上游 good 命中数=%d，期望至少 1", atomic.LoadInt32(goodHits))
	}

	// ---- 场景 b2：跨组回退（failover 策略，acc_bad 502 → fallback acc_ok 200） ----
	status, body = w1lPostChatCompletions(t, proc.mainAddress, w1lGatewayKeySecrets["w1l_key_failover"], "gpt-test")
	if status != http.StatusOK {
		t.Fatalf("场景 W1L-b2 期望回退后 200，实际 %d，body=%s", status, body)
	}
	if !strings.Contains(body, w1lUpstreamContent) {
		t.Fatalf("场景 W1L-b2 回退响应缺少上游内容 %q，body=%s", w1lUpstreamContent, body)
	}
	if atomic.LoadInt32(badHits) < 1 {
		t.Fatalf("场景 W1L-b2 上游 bad 命中数=%d，期望至少 1（首组 acc_bad 真实派发）", atomic.LoadInt32(badHits))
	}

	// ---- 场景 b1：单组耗尽（上游恒 502 → 503 固定契约，诊断不外泄） ----
	status, body = w1lPostChatCompletions(t, proc.mainAddress, w1lGatewayKeySecrets["w1l_key_solo"], "gpt-test")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("场景 W1L-b1 期望 503，实际 %d，body=%s", status, body)
	}
	if !strings.Contains(body, "上游暂时不可用，请重试") {
		t.Fatalf("场景 W1L-b1 缺少耗尽契约文案，body=%s", body)
	}
	if strings.Contains(body, w1lUpstreamLeak) || strings.Contains(body, "最后一次尝试") {
		t.Fatalf("场景 W1L-b1 上游诊断外泄，body=%s", body)
	}

	// ---- 场景 c：未知模型（capability gate 按模型过滤清空候选 → 503 固定契约） ----
	status, body = w1lPostChatCompletions(t, proc.mainAddress, w1lGatewayKeySecrets["w1l_key_main"], "w1l-unknown-model")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("场景 W1L-c 期望 503，实际 %d，body=%s", status, body)
	}
	if !strings.Contains(body, "当前分组无账户支持请求路径或客户端协议") || !strings.Contains(body, `"code":"model_unsupported"`) {
		t.Fatalf("场景 W1L-c 缺少模型不匹配契约（model_unsupported + 固定文案），body=%s", body)
	}

	// ---- 优雅关闭（CTRL_BREAK → exit 0、无 fail 输出） ----
	w1lStopOwnerGateway(t, proc)

	w1bAppendCoverageManifest(t, coverageDir)
	t.Logf("场景 W1L 全链路请求覆盖通过: main=%s health=%s upstream=%s goodHits=%d badHits=%d",
		proc.mainAddress, proc.healthAddress, upstreamBaseURL, atomic.LoadInt32(goodHits), atomic.LoadInt32(badHits))
}
