package main

// w1: accountsRuntimeResetBridge 的纯投影与 DB 读取面直测（fixture SQLite）。

import (
	"context"
	"testing"
	"time"
)

func newW1ResetBridge(t *testing.T) *accountsRuntimeResetBridge {
	t.Helper()
	fixture := newChainFixture(t)
	// 补 team grant 读取面的最小表。
	if _, err := fixture.db.Exec(`CREATE TABLE resource_authorization_grants (
		id TEXT PRIMARY KEY, resource_type TEXT, resource_id TEXT, grantee_type TEXT,
		grantee_team_id TEXT, status TEXT, expires_at TEXT, limits_json TEXT)`); err != nil {
		t.Fatalf("create grants table: %v", err)
	}
	return &accountsRuntimeResetBridge{db: fixture.db, pg: false, now: time.Now}
}

func TestW1ResetBridgeGenerationsAndGuards(t *testing.T) {
	bridge := newW1ResetBridge(t)
	// 未记住：空串。
	if got := bridge.rememberedTransientGeneration("acc_1", "fp_1"); got != "" {
		t.Fatalf("未记住 = %q", got)
	}
	bridge.rememberTransientGeneration("acc_1", "fp_1", "gen-1")
	if got := bridge.rememberedTransientGeneration("acc_1", "fp_1"); got != "gen-1" {
		t.Fatalf("记住 = %q", got)
	}
	// guardAccount：指纹 + 代际指针。
	account := bridge.guardAccount("acc_2", "fp_2", "")
	if account.ID != "acc_2" || account.SelectedAPIKeyFingerprint == nil || *account.SelectedAPIKeyFingerprint != "fp_2" {
		t.Fatalf("guard account = %+v", account)
	}
	if account.SelectedAPIKeyTransientGeneration != nil {
		t.Fatal("无代际不得有指针")
	}
	withGen := bridge.guardAccount("acc_3", "fp_3", "gen-9")
	if withGen.SelectedAPIKeyTransientGeneration == nil || *withGen.SelectedAPIKeyTransientGeneration != "gen-9" {
		t.Fatalf("代际 = %v", withGen.SelectedAPIKeyTransientGeneration)
	}
	// 表限定与占位符。
	if bridge.table("accounts") != "accounts" {
		t.Fatal("sqlite 表无前缀")
	}
	if got := bridge.bind("a = ? AND b = ?"); got != "a = ? AND b = ?" {
		t.Fatalf("sqlite bind = %q", got)
	}
	pgBridge := &accountsRuntimeResetBridge{pg: true}
	if pgBridge.table("accounts") != "juhe_business.accounts" {
		t.Fatal("pg 前缀缺失")
	}
	if got := pgBridge.bind("a = ? AND b = ?"); got != "a = $1 AND b = $2" {
		t.Fatalf("pg bind = %q", got)
	}
}

func TestW1ResetBridgeAuthorizationReads(t *testing.T) {
	bridge := newW1ResetBridge(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	// 授权存在：limits 投影。
	seed := `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_system_account_id, status, expires_at, effective_source_type, effective_source_team_id, limits_json)
		VALUES ('authz_r1', 'account', 'acc_1', 'sys_owner', 'sys_caller', 'active', NULL, 'team', 'team_1', '{"total":{"enabled":true}}')`
	if _, err := bridge.db.Exec(seed); err != nil {
		t.Fatalf("seed authz: %v", err)
	}
	limits, err := bridge.authorizationLimitsJSON(ctx, "authz_r1")
	if err != nil || limits == "" {
		t.Fatalf("limits = %q, %v", limits, err)
	}
	// 未知授权：空串。
	missing, err := bridge.authorizationLimitsJSON(ctx, "authz_none")
	if err != nil || missing != "" {
		t.Fatalf("缺失 = %q, %v", missing, err)
	}
	// teamGrantLimitsJSON：无 grants 表数据 → 空串不报错。
	grant, err := bridge.teamGrantLimitsJSON(ctx, "authz_r1", now)
	if err != nil || grant != "" {
		t.Fatalf("无 grant = %q, %v", grant, err)
	}
	// 插入 team grant 后命中。
	grantSeed := `INSERT INTO resource_authorization_grants (id, resource_type, resource_id, grantee_type,
		grantee_team_id, status, expires_at, limits_json)
		VALUES ('grant_1', 'account', 'acc_1', 'team', 'team_1', 'active', NULL, '{"hourly":{"enabled":true}}')`
	if _, err := bridge.db.Exec(grantSeed); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	grant, err = bridge.teamGrantLimitsJSON(ctx, "authz_r1", now)
	if err != nil || grant == "" {
		t.Fatalf("grant = %q, %v", grant, err)
	}
	// authorizationInstanceIDs：无实例账户 → 空。
	ids, err := bridge.authorizationInstanceIDs(ctx, "authz_r1")
	if err != nil || len(ids) != 0 {
		t.Fatalf("无实例 = %v, %v", ids, err)
	}
	// 授权实例账户命中。
	instanceSeed := `INSERT INTO accounts (id, system_account_id, provider_code, name, type, status, schedulable,
		authorization_instance_authorization_id, deleted_at)
		VALUES ('acc_instance', 'sys_caller', 'openai', '实例', 'api_key', 'active', 1, 'authz_r1', NULL)`
	if _, err := bridge.db.Exec(instanceSeed); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	ids, err = bridge.authorizationInstanceIDs(ctx, "authz_r1")
	if err != nil || len(ids) != 1 || ids[0] != "acc_instance" {
		t.Fatalf("实例 = %v, %v", ids, err)
	}
	// ClearAPIKeyTransientFailure：nil 代际 false；未记住 false。
	ok, err := bridge.ClearAPIKeyTransientFailure(ctx, "acc_x", "fp_x", nil)
	if err != nil || ok {
		t.Fatalf("nil 代际 = %v, %v", ok, err)
	}
	generation := int64(7)
	if ok, err := bridge.ClearAPIKeyTransientFailure(ctx, "acc_y", "fp_y", &generation); err != nil || ok {
		t.Fatalf("未记住 = %v, %v", ok, err)
	}
}
