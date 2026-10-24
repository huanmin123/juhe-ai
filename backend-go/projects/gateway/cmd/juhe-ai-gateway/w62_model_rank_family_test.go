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
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
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
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, local_super_priority_enabled, created_at)
		VALUES (?, ?, ?, 1, ?, ?)`, fixture.groupID, fixture.systemAccount, accountID, superPriority, now)
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
