package main

// W62 方案B（2026-10-24 用户裁决）：listModelCandidateRows 候选窗口的
// 同族/跨族 rank 判定——同族改名映射（target_endpoint_family =
// source_endpoint_family，保留非恒等门）与直连同权 rank 0；跨协议转换映射
// （两族不同）rank 1。排序语义同批覆盖：同秩桶内 local_super_priority_enabled
// DESC 生效，同族改名映射账户的超级优先压过无特权的直连账户。
// 权威口径：docs/functions/AI账户模型映射选择规则.md §2.4、
// docs/functions/超级分组设计.md §4.2。

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// seedW62MappingAccount 以 hydration 测试同款形态种一个映射账户（enabled
// 连接 + account_entry_mappings 行 + 支持模型行）。
func seedW62MappingAccount(t *testing.T, fixture *chainFixture, accountID, sourceModel, sourceFamily, upstreamModel, upstreamFamily string, superPriority int) {
	t.Helper()
	db := fixture.db
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}
	credentials, err := accounts.EncryptJSON("chain-test-secret", map[string]any{"api_key": "sk-upstream"})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	connID := "conn_" + accountID
	seed(`INSERT INTO accounts (id, system_account_id, protocol_code, protocol_version, name, type, status, schedulable, credentials_encrypted, deleted_at, health_check_model) VALUES (?, ?, 'openai', 'v1', ?, 'api_key', 'active', 1, ?, NULL, ?)`,
		accountID, fixture.systemAccount, accountID, credentials, upstreamModel)
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, local_priority, local_super_priority_enabled, local_fallback_enabled, created_at)
		VALUES (?, ?, ?, 1, 0, ?, 0, ?)`, fixture.groupID, fixture.systemAccount, accountID, superPriority, now)
	seed(`INSERT INTO account_connections (id, account_id, connection_preset_code, protocol_surface_code, base_url, enabled, created_at, updated_at)
		VALUES (?, ?, 'gpt_openai_v1', 'openai_v1', ?, 1, ?, ?)`, connID, accountID, "https://"+accountID+".example.invalid/v1", now, now)
	seed(`UPDATE accounts SET health_check_connection_id = ? WHERE id = ?`, connID, accountID)
	seed(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, created_at, updated_at)
		VALUES (?, 'openai', ?, ?, 'builtin', 'active', '["chat_json","chat_sse"]', ?, ?)`,
		"mdl_"+upstreamModel, upstreamModel, upstreamModel, now, now)
	seed(`INSERT INTO account_supported_models (account_id, model, model_id, created_at) VALUES (?, ?, ?, ?)`,
		accountID, upstreamModel, "mdl_"+upstreamModel, now)
	seed(`INSERT INTO account_entry_mappings (id, account_id, source_model, source_endpoint_family, target_connection_id, target_model_id, target_endpoint_family, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		"aem_"+accountID+"_"+sourceModel, accountID, sourceModel, sourceFamily, connID, "mdl_"+upstreamModel, upstreamFamily, now, now)
}

// TestW62ModelRankFamilyArms 断言候选窗口 SQL 的三臂：直连 rank 0、同族改名
// 映射 rank 0、跨协议转换映射 rank 1。
func TestW62ModelRankFamilyArms(t *testing.T) {
	fixture := newChainFixture(t)
	// 直连账户（fixture 自带 acc_1 支持 gpt-test；补 gpt-w62 支持行）。
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}
	seed(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, created_at, updated_at)
		VALUES ('mdl_gpt-w62', 'openai', 'gpt-w62', 'gpt-w62', 'builtin', 'active', '["chat_json","chat_sse"]', ?, ?)`, now, now)
	seed(`INSERT INTO account_supported_models (account_id, model, model_id, created_at) VALUES (?, 'gpt-w62', 'mdl_gpt-w62', ?)`,
		fixture.accountID, now)
	seedW62MappingAccount(t, fixture, "acc_w62_rename", "gpt-w62", "chat_completions", "up-w62-rename", "chat_completions", 0)
	seedW62MappingAccount(t, fixture, "acc_w62_cross", "gpt-w62", "chat_completions", "up-w62-cross", "messages", 0)

	groupAccess, err := fixture.selector.resolveGroupAccess(context.Background(), fixture.groupID, fixture.systemAccount)
	if err != nil {
		t.Fatalf("resolve group access: %v", err)
	}
	_, ranks, err := fixture.selector.listModelCandidateRows(context.Background(), fixture.groupID, groupAccess,
		now, "gpt-w62", "chat_completions", false, false)
	if err != nil {
		t.Fatalf("list model candidate rows: %v", err)
	}
	if got := ranks[fixture.accountID]; got != 0 {
		t.Fatalf("direct rank = %d, want 0", got)
	}
	if got := ranks["acc_w62_rename"]; got != 0 {
		t.Fatalf("same-family rename rank = %d, want 0 (plan B: ties with direct)", got)
	}
	if got := ranks["acc_w62_cross"]; got != 1 {
		t.Fatalf("cross-protocol rank = %d, want 1", got)
	}
}

// TestW62ModelRankFamilyRenameSuperPriorityWins 排序语义：同族改名映射与
// 直连同秩（均 rank 0）后，同族改名映射账户的 local_super_priority_enabled=1
// 压过无特权的直连账户（ORDER BY model_rank → local_super_priority_enabled
// DESC），这正是 T62 生产案例的预期行为翻转。
func TestW62ModelRankFamilyRenameSuperPriorityWins(t *testing.T) {
	fixture := newChainFixture(t)
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}
	seed(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, created_at, updated_at)
		VALUES ('mdl_gpt-w62', 'openai', 'gpt-w62', 'gpt-w62', 'builtin', 'active', '["chat_json","chat_sse"]', ?, ?)`, now, now)
	seed(`INSERT INTO account_supported_models (account_id, model, model_id, created_at) VALUES (?, 'gpt-w62', 'mdl_gpt-w62', ?)`,
		fixture.accountID, now)
	// 同族改名映射账户带超级优先；直连账户无特权。
	seedW62MappingAccount(t, fixture, "acc_w62_rename", "gpt-w62", "chat_completions", "up-w62-rename", "chat_completions", 1)

	groupAccess, err := fixture.selector.resolveGroupAccess(context.Background(), fixture.groupID, fixture.systemAccount)
	if err != nil {
		t.Fatalf("resolve group access: %v", err)
	}
	rows, ranks, err := fixture.selector.listModelCandidateRows(context.Background(), fixture.groupID, groupAccess,
		now, "gpt-w62", "chat_completions", false, false)
	if err != nil {
		t.Fatalf("list model candidate rows: %v", err)
	}
	if len(rows) != 2 || rows[0].AccountID != "acc_w62_rename" || rows[1].AccountID != fixture.accountID {
		t.Fatalf("order = %s,%s; want super-priority rename first (both rank 0)", rows[0].AccountID, rows[1].AccountID)
	}
	if ranks["acc_w62_rename"] != 0 || ranks[fixture.accountID] != 0 {
		t.Fatalf("ranks = %v, want both 0", ranks)
	}
}

// TestW62AccountLevelSuperPriorityWinsWithBoundLocalZero 自有账户形态（T62
// 第二层根因）：调度标记写在 accounts.super_priority_enabled=1，绑定行
// local_super_priority_enabled 显式 0——flag 合并语义是 OR，绑定的显式 0
// 不得遮蔽账户级 1（COALESCE 取首非空会被 0 遮蔽，禁止回退到该写法）。
func TestW62AccountLevelSuperPriorityWinsWithBoundLocalZero(t *testing.T) {
	fixture := newChainFixture(t)
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed row: %v: %v: %v", query, args, err)
		}
	}
	seed(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, created_at, updated_at)
		VALUES ('mdl_gpt-w62', 'openai', 'gpt-w62', 'gpt-w62', 'builtin', 'active', '["chat_json","chat_sse"]', ?, ?)`, now, now)
	seed(`INSERT INTO account_supported_models (account_id, model, model_id, created_at) VALUES (?, 'gpt-w62', 'mdl_gpt-w62', ?)`,
		fixture.accountID, now)
	// local 显式 0 + 账户级 1（生产 tokenrhythm 形态）。
	seedW62MappingAccount(t, fixture, "acc_w62_rename", "gpt-w62", "chat_completions", "up-w62-rename", "chat_completions", 0)
	seed(`UPDATE accounts SET super_priority_enabled = 1 WHERE id = 'acc_w62_rename'`)

	groupAccess, err := fixture.selector.resolveGroupAccess(context.Background(), fixture.groupID, fixture.systemAccount)
	if err != nil {
		t.Fatalf("resolve group access: %v", err)
	}
	rows, _, err := fixture.selector.listModelCandidateRows(context.Background(), fixture.groupID, groupAccess,
		now, "gpt-w62", "chat_completions", false, false)
	if err != nil {
		t.Fatalf("list model candidate rows: %v", err)
	}
	if len(rows) != 2 || rows[0].AccountID != "acc_w62_rename" {
		t.Fatalf("order = %s,%s; want account-level super-priority rename first", rows[0].AccountID, rows[1].AccountID)
	}
}

// TestW62ChainSuperFallbackRankOrSemantics 链面比较器与 secret 同源的 flag
// OR 语义：local_* 与 accounts.* 任一为真即生效；显式 0 不遮蔽账户级 1。
func TestW62ChainSuperFallbackRankOrSemantics(t *testing.T) {
	localSuper := sql.NullInt64{Int64: 1, Valid: true}
	localZero := sql.NullInt64{Int64: 0, Valid: true}
	localNull := sql.NullInt64{Valid: false}
	cases := []struct {
		name         string
		row          chainCandidateRow
		wantSuper    int
		wantFallback int
	}{
		{"local only (authorized instance form)", chainCandidateRow{LocalSuperPriority: localSuper, LocalFallback: localSuper}, 1, 1},
		{"account only, binding zero (own-account form)", chainCandidateRow{LocalSuperPriority: localZero, LocalFallback: localZero, SuperPriorityEnabled: 1, FallbackEnabled: 1}, 1, 1},
		{"account only, binding null", chainCandidateRow{LocalSuperPriority: localNull, LocalFallback: localNull, SuperPriorityEnabled: 1}, 1, 0},
		{"neither", chainCandidateRow{LocalSuperPriority: localZero, LocalFallback: localZero}, 0, 0},
	}
	for _, tc := range cases {
		if got := chainSuperRank(&tc.row); got != tc.wantSuper {
			t.Fatalf("%s: chainSuperRank = %d, want %d", tc.name, got, tc.wantSuper)
		}
		if got := chainFallbackRank(&tc.row); got != tc.wantFallback {
			t.Fatalf("%s: chainFallbackRank = %d, want %d", tc.name, got, tc.wantFallback)
		}
	}
}

// TestW62AccountLevelSuperFullAssemblyOrder 全装配复现（T62 生产形态）：
// SQL 窗口 + merge + Go 比较器 + secret 全链之后，账户级超级优先（绑定列
// 显式 0）的同族改名映射账户必须排在无特权直连账户之前，且 secret 装配出
// SuperPriorityEnabled=true（热质量 tier / 会话亲和旁路的输入）。
func TestW62AccountLevelSuperFullAssemblyOrder(t *testing.T) {
	fixture := newChainFixture(t)
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed row: %v: %v: %v", query, args, err)
		}
	}
	seed(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, created_at, updated_at)
		VALUES ('mdl_gpt-w62', 'openai', 'gpt-w62', 'gpt-w62', 'builtin', 'active', '["chat_json","chat_sse"]', ?, ?)`, now, now)
	seed(`INSERT INTO account_supported_models (account_id, model, model_id, created_at) VALUES (?, 'gpt-w62', 'mdl_gpt-w62', ?)`,
		fixture.accountID, now)
	seedW62MappingAccount(t, fixture, "acc_w62_rename", "gpt-w62", "chat_completions", "up-w62-rename", "chat_completions", 0)
	seed(`UPDATE accounts SET super_priority_enabled = 1 WHERE id = 'acc_w62_rename'`)

	result, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(), fixture.groupID, fixture.systemAccount,
		gatewayruntimecache.OpenAIAccountsForGroupOptions{RequestedModel: "gpt-w62", RequestedEndpointFamily: "chat_completions"})
	if err != nil {
		t.Fatalf("list accounts for group: %v", err)
	}
	if len(result.Accounts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(result.Accounts))
	}
	if result.Accounts[0].ID != "acc_w62_rename" {
		t.Fatalf("assembly order = %s,%s; want account-level super first",
			result.Accounts[0].ID, result.Accounts[1].ID)
	}
	if !result.Accounts[0].SuperPriorityEnabled {
		t.Fatalf("secret SuperPriorityEnabled must be true for account-level flag")
	}
}

// TestW62OwnAccountFallbackFlagAssemblyAndOrder 自有账户降级备用双面（T62
// 排查矩阵 B+A 层）：accounts.fallback_enabled=1 + 绑定列 0（自有形态）在
// secret 装配为 FallbackEnabled=true，且排序排在非备用账户之后。同时钉死
// priority 遮蔽口径：绑定 local_priority 显式 0 遮蔽 accounts.priority（列
// NOT NULL，COALESCE 取显式值）——自有账户两处一致由写侧 patch/批量同步保
// 证，绕过同步的写面（modelcheckowner 类）会造成漂移，见 T62 排查报告。
func TestW62OwnAccountFallbackFlagAssemblyAndOrder(t *testing.T) {
	fixture := newChainFixture(t)
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed row: %v: %v: %v", query, args, err)
		}
	}
	seed(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, supported_endpoint_modes_json, created_at, updated_at)
		VALUES ('mdl_gpt-w62', 'openai', 'gpt-w62', 'gpt-w62', 'builtin', 'active', '["chat_json","chat_sse"]', ?, ?)`, now, now)
	seed(`INSERT INTO account_supported_models (account_id, model, model_id, created_at) VALUES (?, 'gpt-w62', 'mdl_gpt-w62', ?)`,
		fixture.accountID, now)
	// 自有形态降级备用：accounts.fallback_enabled=1、accounts.priority=3、
	// 绑定 local_* 全默认 0。
	seedW62MappingAccount(t, fixture, "acc_w62_fb", "gpt-w62", "chat_completions", "up-w62-fb", "chat_completions", 0)
	seed(`UPDATE accounts SET fallback_enabled = 1, priority = 3 WHERE id = 'acc_w62_fb'`)

	result, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(), fixture.groupID, fixture.systemAccount,
		gatewayruntimecache.OpenAIAccountsForGroupOptions{RequestedModel: "gpt-w62", RequestedEndpointFamily: "chat_completions"})
	if err != nil {
		t.Fatalf("list accounts for group: %v", err)
	}
	if len(result.Accounts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(result.Accounts))
	}
	if result.Accounts[0].ID != fixture.accountID || result.Accounts[1].ID != "acc_w62_fb" {
		t.Fatalf("order = %s,%s; want non-fallback first, own-form fallback last",
			result.Accounts[0].ID, result.Accounts[1].ID)
	}
	fallbackSecret := result.Accounts[1]
	if !fallbackSecret.FallbackEnabled {
		t.Fatalf("secret FallbackEnabled must be true for account-level flag (binding 0)")
	}
	if fallbackSecret.Priority != 0 {
		t.Fatalf("own-account secret priority = %d, want 0 (binding local_priority=0 shadows accounts.priority; write-side sync owns consistency)", fallbackSecret.Priority)
	}
	if result.Accounts[0].FallbackEnabled {
		t.Fatalf("non-fallback account secret FallbackEnabled must stay false")
	}
}
