package main

// accounts RuntimeResetEffects 端到端测试：composeSystemAPI 提供真实业务库
// schema（SQLite），compose_accounts_reset.go 的桥接以与生产装配相同的构造
// 接线到 accounts.Store，然后走一次完整 runtime-reset，断言
// account_api_key_runtime_states 域的真实副作用（accountkeystates 包）。
//
// 说明：生产装配点在 JUHE_AI_GATEWAY_CHAIN_ENABLED 分支内
// （compose.go），这里用同款构造手动装配，避免整条 /v1 链的启动依赖。

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accountkeystates"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
)

func TestAccountsRuntimeResetBridgeAPIKeyRuntimePool(t *testing.T) {
	cfg := composeTestConfig(t)
	store := openComposeOperationStore(t)
	createRuntimeLogDataset(t, cfg.RuntimeLogDatabasePath)
	auditConfig, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	defer closeAudit()
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), auditConfig)
	if err != nil {
		t.Fatalf("compose system api: %v", err)
	}
	defer composed.Shutdown()
	seedSystemSettings(t, composed.DB)

	// 生产同款桥接：guard 为 memory 驱动（与 chain_runtime.go 的非 redis 路径一致）；
	// 派发桥目标为空（本测试未接 jobs internalapi），健康检查派发按 inert 契约跳过。
	guard := gatewayaccounteffects.NewAccountAPIKeyFailureGuard(
		gatewayaccounteffects.SideEffectsConfig{RuntimeStateDriver: cfg.RuntimeStateDriver},
		gatewayaccounteffects.SystemClock{}, nil, nil)
	resetEffects, err := newAccountsRuntimeResetBridge(composed, settingsValueReader(composed.settingsStore), &chainRuntimeServices{AccountAPIKeyGuard: guard}, cfg.Secret, newChainJobsHealthDispatchBridge("", "", nil))
	if err != nil {
		t.Fatalf("assemble runtime reset bridge: %v", err)
	}
	accountStore, err := accounts.NewStore(composed.DB, false, cfg.Secret, time.Now, newCompositionID)
	if err != nil {
		t.Fatalf("accounts store: %v", err)
	}
	accountStore.SetRuntimeResetEffects(resetEffects)
	keyStates, err := accountkeystates.NewStore(accountkeystates.Config{
		DB: composed.DB, Postgres: false, Secret: cfg.Secret, Now: time.Now,
	})
	if err != nil {
		t.Fatalf("accountkeystates store: %v", err)
	}

	const accountID = "acc-e2e"
	// 真实 schema 带外键（system_accounts / accounts），复用引导种子的系统账户。
	var ownerID string
	if err := composed.DB.QueryRow(`SELECT id FROM system_accounts ORDER BY created_at ASC LIMIT 1`).Scan(&ownerID); err != nil {
		t.Fatalf("seed system account lookup: %v", err)
	}
	// 三把 Key 的 openai api_key 池账户（active + schedulable，无持久失败态）。
	keys := []any{"sk-e2e-a", "sk-e2e-b", "sk-e2e-c"}
	fingerprints := make([]string, 0, len(keys))
	for _, key := range keys {
		fingerprints = append(fingerprints, keyStates.FingerprintAPIKey(key.(string)))
	}
	sealed, err := accounts.EncryptJSON(cfg.Secret, map[string]any{"api_keys": keys})
	if err != nil {
		t.Fatal(err)
	}
	nowISO := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := composed.DB.Exec(`INSERT INTO accounts (id, system_account_id, name, type, status, schedulable,
		provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
		client_compatibility, config_revision, dispatch_revision, credentials_encrypted,
		health_check_model, health_check_endpoint_mode, created_at, updated_at)
		VALUES (?, ?, 'reset-e2e', 'api_key', 'active', 1,
		'openai', 'profile_openai_openai_v1', 'openai', 'v1',
		'openai_standard', 1, 1, ?,
		'', 'chat_json', ?, ?)`,
		accountID, ownerID, sealed, nowISO, nowISO); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	// 运行态：k1 rate_limited（到期时间在未来）、k2 error、k3 disabled。
	seedState := func(fingerprint, status string) {
		t.Helper()
		if _, err := composed.DB.Exec(`INSERT INTO account_api_key_runtime_states
			(id, system_account_id, account_id, key_fingerprint, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"state-"+fingerprint[:12], ownerID, accountID, fingerprint, status, nowISO, nowISO); err != nil {
			t.Fatalf("seed state %s: %v", fingerprint[:8], err)
		}
	}
	seedState(fingerprints[0], "rate_limited")
	seedState(fingerprints[1], "error")
	seedState(fingerprints[2], "disabled")
	// group_accounts.group_id 外键 groups(id)，groups.provider_code 外键 providers(code)。
	if _, err := composed.DB.Exec(`INSERT INTO groups (id, system_account_id, name, provider_code, created_at, updated_at)
		VALUES ('grp-e2e', ?, 'reset-e2e-group', 'openai', ?, ?)`, ownerID, nowISO, nowISO); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if _, err := composed.DB.Exec(`INSERT INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at)
		VALUES (?, 'grp-e2e', ?, 1, ?, ?)`, ownerID, accountID, nowISO, nowISO); err != nil {
		t.Fatalf("seed group binding: %v", err)
	}

	// reset → 真实端口调用链。
	outcome, err := accountStore.ResetAccountRuntimeState(context.Background(), accountID, 1, accounts.AccessScope{IsAdmin: true})
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	result := outcome.Result
	// 重校验只触碰 rate_limited + error 两行（disabled 与池外 Key 不动）。
	if result.APIKeyRuntimeRevalidated != 2 {
		t.Fatalf("apiKeyRuntimeRevalidated: %d", result.APIKeyRuntimeRevalidated)
	}
	cleared := map[string]bool{}
	for _, item := range result.Cleared {
		cleared[item] = true
	}
	if !cleared["api_key_runtime"] {
		t.Fatalf("cleared set: %v", result.Cleared)
	}
	if result.GatewayRuntime != "cleared" {
		t.Fatalf("gatewayRuntime: %s", result.GatewayRuntime)
	}
	// 重校验后三把 Key 全部不可用（rate_limited/unverified/disabled）→
	// APIKeyPoolAllUnavailable 判定生效，dispatchEligible=false。
	if result.DispatchEligible {
		t.Fatalf("dispatchEligible must be false while the whole pool is unavailable: %+v", result)
	}

	// 新包副作用断言：error → unverified、cooldown 清空、next_probe_at 到期。
	var status string
	var cooldownUntil, nextProbeAt sql.NullString
	if err := composed.DB.QueryRow(`SELECT status, cooldown_until, next_probe_at FROM account_api_key_runtime_states
		WHERE account_id = ? AND key_fingerprint = ?`, accountID, fingerprints[1]).
		Scan(&status, &cooldownUntil, &nextProbeAt); err != nil {
		t.Fatal(err)
	}
	if status != "unverified" || cooldownUntil.Valid || !nextProbeAt.Valid {
		t.Fatalf("error key row: %s %v %v", status, cooldownUntil.Valid, nextProbeAt.Valid)
	}
	if err := composed.DB.QueryRow(`SELECT status FROM account_api_key_runtime_states
		WHERE account_id = ? AND key_fingerprint = ?`, accountID, fingerprints[2]).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "disabled" {
		t.Fatalf("disabled key must stay untouched: %s", status)
	}
	// runtime-state-changed 标记：group_account_stats_dirty 脏行 + reason。
	var reason string
	if err := composed.DB.QueryRow(`SELECT reason FROM group_account_stats_dirty WHERE group_id = 'grp-e2e'`).Scan(&reason); err != nil {
		t.Fatalf("stats dirty row: %v", err)
	}
	if reason != "account_api_key_runtime" {
		t.Fatalf("dirty reason: %s", reason)
	}

	// 派发桥目标为空（未接 jobs internalapi）→ 派发按 input_unavailable 拒绝：
	// 不 panic、不阻塞 reset（生产装配见 compose_accounts_reset_dispatch_test.go）。
	resetEffects.DispatchAccountHealthCheck(accountID, "e2e-skip")

	// 直连端口语义：非池账户 / 缺账户回落。
	revalidation, err := resetEffects.RevalidateAccountAPIKeyRuntimePool(context.Background(), "acc-missing", 1)
	if err != nil || revalidation.Eligible || revalidation.Reason != "account_not_found" {
		t.Fatalf("missing account revalidation: %+v %v", revalidation, err)
	}
	if allUnavailable, err := resetEffects.APIKeyPoolAllUnavailable(context.Background(), "acc-missing"); err != nil || allUnavailable {
		t.Fatalf("missing account allUnavailable: %v %v", allUnavailable, err)
	}
	// 池内账户经由 summaries 判定仍为全不可用。
	if allUnavailable, err := resetEffects.APIKeyPoolAllUnavailable(context.Background(), accountID); err != nil || !allUnavailable {
		t.Fatalf("pool account allUnavailable: %v %v", allUnavailable, err)
	}
}
