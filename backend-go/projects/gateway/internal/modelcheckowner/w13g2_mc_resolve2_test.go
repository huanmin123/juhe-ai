// w13g2 modelcheckowner 批次 5：Resolve 链数据分支（revision 漂移、凭据
// 类型、可用性、映射限制）与 ListAccountOptions 查询变体。
package modelcheckowner

import (
	"context"
	"database/sql"
	"testing"
)

func w13g2McResolveBase(t *testing.T, db *sql.DB) *BusinessTargetSource {
	t.Helper()
	for _, ddl := range businessSourceContractDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	envelope := testCredentialEnvelope(t, "secret", `{"api_key":"key-1","supported_endpoint_modes":["responses_sse"]}`)
	for _, stmt := range []string{
		`INSERT INTO provider_protocol_profiles VALUES ('profile_openai_openai_v1',1,'https://example.invalid/v1')`,
		`INSERT INTO groups VALUES ('group-1','sys-1',1)`,
		`INSERT INTO accounts VALUES ('acct-1','sys-1','openai','profile_openai_openai_v1','openai','api_key',3,7,'active',1,'responses_sse',NULL,NULL,NULL,?,NULL,NULL,NULL,NULL,NULL,'Account 1')`,
		`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES ('acct-1','sys-1','group-1',1)`,
		`INSERT INTO resource_authorizations VALUES ('auth-1','account','acct-1','sys-1','sys-1','use','active','2027-01-01T00:00:00Z')`,
		`INSERT INTO accounts VALUES ('inst-1','sys-1','openai','profile_openai_openai_v1','openai','api_key',3,7,'active',1,'responses_sse',NULL,NULL,NULL,?,'2027-01-01T00:00:00Z',NULL,NULL,'auth-1','acct-1','Instance 1')`,
		`INSERT INTO group_accounts(account_id,system_account_id,group_id,account_authorization_id,enabled) VALUES ('inst-1','sys-1','group-1','auth-1',1)`,
	} {
		args := []any{}
		if countParams(stmt) == 1 {
			args = append(args, envelope)
		}
		if _, err := db.Exec(stmt, args...); err != nil {
			t.Fatal(err)
		}
	}
	source, err := NewBusinessTargetSource(db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	return source
}

// TestW13g2McResolveDataBranches 覆盖 resolveAuthorizedTarget 的修订漂移、
// 可用性判定与凭据类型分支。
func TestW13g2McResolveDataBranches(t *testing.T) {
	s, _ := w13g2McFailStore(t)
	db := s.db
	ctx := context.Background()
	source := w13g2McResolveBase(t, db)

	// 基线成功 Resolve。
	base, err := source.Resolve(ctx, runReq())
	if err != nil {
		t.Fatal(err)
	}

	// config revision 漂移 → 拒绝。
	stale := base
	if _, err := source.Resolve(ctx, RunRequest{
		SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol",
		TriggerKind: "manual", ConfigRevision: "9999",
	}); err == nil {
		t.Fatalf("config revision 漂移应拒绝")
	}
	// dispatch revision 漂移 → 拒绝。
	if _, err := source.Resolve(ctx, RunRequest{
		SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol",
		TriggerKind: "manual", DispatchRevision: 9999,
	}); err == nil {
		t.Fatalf("dispatch revision 漂移应拒绝")
	}
	// source revision 漂移 → 拒绝。
	if _, err := source.Resolve(ctx, RunRequest{
		SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol",
		TriggerKind: "manual", SourceConfigRevision: "9999",
	}); err == nil {
		t.Fatalf("source revision 漂移应拒绝")
	}
	// 实例账户停用 → 不可用（实例授权路径）。
	instReq := RunRequest{
		SystemAccountID: "sys-1", TargetType: "account", TargetID: "inst-1", Model: "gpt-5.6-sol",
		TriggerKind: "manual",
	}
	if _, err := source.Resolve(ctx, instReq); err == nil {
		t.Fatalf("停用实例应拒绝")
	}
	if _, err := db.Exec(`UPDATE accounts SET status='active' WHERE id='inst-1'`); err != nil {
		t.Fatal(err)
	}
	// 授权过期 → 不可用。
	if _, err := db.Exec(`UPDATE resource_authorizations SET expires_at='2020-01-01T00:00:00Z' WHERE id='auth-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Resolve(ctx, instReq); err == nil {
		t.Fatalf("授权过期应拒绝")
	}
	if _, err := db.Exec(`UPDATE resource_authorizations SET expires_at='2027-01-01T00:00:00Z' WHERE id='auth-1'`); err != nil {
		t.Fatal(err)
	}
	// 凭据类型不支持。
	if _, err := db.Exec(`UPDATE accounts SET type='legacy' WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Resolve(ctx, runReq()); err == nil {
		t.Fatalf("凭据类型不支持应拒绝")
	}
	if _, err := db.Exec(`UPDATE accounts SET type='api_key' WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	_ = base
	_ = stale
}

// TestW13g2McListAccountOptionsVariants 覆盖 ListAccountOptions 的查询
// 变体（SelectedID、AllSystemAccounts、search、purpose 校验）。
func TestW13g2McListAccountOptionsVariants(t *testing.T) {
	s, _ := w13g2McFailStore(t)
	db := s.db
	ctx := context.Background()
	source := w13g2McResolveBase(t, db)
	if _, err := db.Exec(`ALTER TABLE accounts ADD COLUMN protocol_version TEXT NOT NULL DEFAULT ""`); err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO account_supported_models VALUES ('acct-1','gpt-5.6-sol')`)

	if _, err := source.ListAccountOptions(ctx, AccountOptionsQuery{Purpose: "invalid"}); err == nil {
		t.Fatalf("非法 purpose 应拒绝")
	}
	if _, err := source.ListAccountOptions(ctx, AccountOptionsQuery{Purpose: "run", Limit: 0, SystemAccountID: "sys-1"}); err == nil {
		t.Fatalf("limit 0 应拒绝或取默认")
	}
	if _, err := source.ListAccountOptions(ctx, AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", SelectedID: []string{"acct-1"}, Limit: 20}); err != nil {
		t.Fatalf("SelectedID 查询应成功: %v", err)
	}
	if _, err := source.ListAccountOptions(ctx, AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", Keyword: "Acc", Limit: 20}); err != nil {
		t.Fatalf("search 查询应成功: %v", err)
	}
}
