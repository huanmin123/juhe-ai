package main

// 时间计划闸门与账户状态解耦（docs/functions/时间计划闸门与账户状态解耦设计.md
// §5.1）：网关候选 SQL 必须挂闸门——直连账户与授权实例来源账户 gate=0 时
// 都不进入「可调度候选」，视频准入臂（video_create）同样受闸门约束。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func candidateIDs(rows []chainCandidateRow) map[string]bool {
	ids := map[string]bool{}
	for index := range rows {
		ids[rows[index].ID] = true
	}
	return ids
}

func TestChainCandidateScheduleGateDirectAccount(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	groupAccess := &gatewayruntimecache.GroupUsageAccessMetadata{
		GroupOwnerSystemAccountID: fixture.systemAccount,
	}
	ctx := context.Background()
	now := chainNowISO(fixture.selector.now)

	candidates := func(mediaAdmission bool) map[string]bool {
		t.Helper()
		rows, err := fixture.selector.listCandidateRows(ctx, fixture.groupID, groupAccess, now, false, mediaAdmission)
		if err != nil {
			t.Fatalf("list candidate rows: %v", err)
		}
		return candidateIDs(rows)
	}

	// 基线：active 账户进入候选。
	if !candidates(false)[fixture.accountID] {
		t.Fatal("基线 active 账户必须在候选中")
	}
	// 闸门关闭：status 仍 active，但 gate=0 不得进入候选。
	if _, err := db.Exec(`UPDATE accounts SET availability_schedule_gate = 0 WHERE id = ?`, fixture.accountID); err != nil {
		t.Fatal(err)
	}
	if candidates(false)[fixture.accountID] {
		t.Fatal("gate=0 的 active 账户不得进入可调度候选")
	}
	// 闸门打开：恢复候选。
	if _, err := db.Exec(`UPDATE accounts SET availability_schedule_gate = 1 WHERE id = ?`, fixture.accountID); err != nil {
		t.Fatal(err)
	}
	if !candidates(false)[fixture.accountID] {
		t.Fatal("gate=1 必须恢复候选")
	}

	// 视频准入臂（video_create）：pending_test + 冷却已过可被准入，但闸门
	// 关闭时必须同样被排除（status 臂与 cooldown 臂都挂闸门）。
	if _, err := db.Exec(`UPDATE accounts SET status = 'pending_test', health_check_endpoint_mode = 'video_create',
		cooldown_until = '2020-01-01T00:00:00.000Z', availability_schedule_gate = 1 WHERE id = ?`, fixture.accountID); err != nil {
		t.Fatal(err)
	}
	if candidates(false)[fixture.accountID] {
		t.Fatal("默认窗口不得准入 pending_test 账户")
	}
	if !candidates(true)[fixture.accountID] {
		t.Fatal("video_create 准入臂必须放行冷却已过的 pending_test 账户")
	}
	if _, err := db.Exec(`UPDATE accounts SET availability_schedule_gate = 0 WHERE id = ?`, fixture.accountID); err != nil {
		t.Fatal(err)
	}
	if candidates(true)[fixture.accountID] {
		t.Fatal("gate=0 时视频准入臂同样不得放行")
	}
}

func TestChainCandidateScheduleGateAuthorizedSource(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	credentials, err := accounts.EncryptJSON("chain-test-secret", map[string]any{"api_key": "sk-gate-source"})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(fixtureBind(query), args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}
	// 来源账户（gate 由本测试翻转）+ 以它为来源的授权实例。
	seed(`INSERT INTO accounts (id, system_account_id, protocol_code, protocol_version, name, type, status, schedulable,
			credentials_encrypted, availability_schedule_gate, deleted_at, health_check_model)
		VALUES ('acc_gate_src', ?, 'openai', 'v1', '闸门来源', 'api_key', 'active', 1, ?, 1, NULL, 'gpt-test')`,
		fixture.systemAccount, credentials)
	seed(`INSERT INTO accounts (id, system_account_id, protocol_code, protocol_version, name, type, status, schedulable,
			credentials_encrypted, authorization_instance_source_account_id, authorization_instance_authorization_id, deleted_at)
		VALUES ('acc_gate_inst', ?, 'openai', 'v1', '闸门实例', 'api_key', 'active', 1, ?, 'acc_gate_src', 'authz_gate', NULL)`,
		fixture.systemAccount, credentials)
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, account_authorization_id, created_at)
		VALUES (?, ?, 'acc_gate_inst', 1, 'authz_gate', '2026-09-04T00:00:00.000Z')`,
		fixture.groupID, fixture.systemAccount)

	groupAccess := &gatewayruntimecache.GroupUsageAccessMetadata{
		GroupOwnerSystemAccountID: fixture.systemAccount,
	}
	candidates := func() map[string]bool {
		t.Helper()
		rows, err := fixture.selector.listCandidateRows(context.Background(), fixture.groupID, groupAccess,
			chainNowISO(fixture.selector.now), false, false)
		if err != nil {
			t.Fatalf("list candidate rows: %v", err)
		}
		return candidateIDs(rows)
	}

	// 来源账户闸门打开：实例进入候选。
	if !candidateIDsContains(candidates(), "acc_gate_inst") {
		t.Fatal("来源账户 gate=1 时授权实例必须在候选中")
	}
	// 来源账户闸门关闭：实例自身 active/schedulable 也不得进入候选。
	if _, err := db.Exec(`UPDATE accounts SET availability_schedule_gate = 0 WHERE id = 'acc_gate_src'`); err != nil {
		t.Fatal(err)
	}
	if candidateIDsContains(candidates(), "acc_gate_inst") {
		t.Fatal("来源账户 gate=0 时授权实例不得进入可调度候选")
	}
}

func candidateIDsContains(ids map[string]bool, accountID string) bool {
	return ids[accountID]
}
