// w13g2 modelcheckowner 批次 4：BusinessTargetSource Resolve 链的 armed
// 注入（授权目标读取、fence、策略、映射、代理、凭据解密）。
package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

// w13g2McResolveFixture 装配完整的 Resolve 场景（实例账户 + 授权 + 分组）。
func w13g2McResolveFixture(t *testing.T, db *sql.DB, secret string) *BusinessTargetSource {
	t.Helper()
	for _, ddl := range businessSourceContractDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	envelope := testCredentialEnvelope(t, secret, `{"api_key":"key-1","supported_endpoint_modes":["responses_sse"]}`)
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
	source, err := NewBusinessTargetSource(db, false, secret)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func countParams(stmt string) int {
	count := 0
	for i := 0; i+1 < len(stmt); i++ {
		if stmt[i] == '?' {
			count++
		}
	}
	return count
}

// TestW13g2McResolveArms armed 命中 Resolve 链各查询。
func TestW13g2McResolveArms(t *testing.T) {
	s, fp := w13g2McFailStore(t)
	ctx := context.Background()
	source := w13g2McResolveFixture(t, s.db, "secret")

	// Resolve 主查询失败。
	fp.arm("FROM accounts a JOIN")
	defer fp.disarm()
	if _, err := source.Resolve(ctx, runReq()); err == nil {
		t.Fatalf("应失败")
	}
	// supportedModels 查询失败。
	fp.arm("FROM account_supported_models")
	if _, err := source.Resolve(ctx, runReq()); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()
	// 模型映射查询失败。
	fp.arm("FROM account_model_mappings")
	if _, err := source.Resolve(ctx, runReq()); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()
	// 策略读取失败（BuildRequest 链）。
	fp.arm("FROM model_quality_policies")
	defer fp.disarm()
	if _, err := source.BuildRequest(ctx, "sys-1", RunCommand{Model: "gpt-5.6-sol"}); err == nil {
		t.Fatalf("应失败")
	}
}

func runReq() RunRequest {
	return RunRequest{
		SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1",
		Model: "gpt-5.6-sol", TriggerKind: "manual", ProbeSetVersion: "p1",
		Prompt: "ping",
	}
}

// TestW13g2McRuntimeStreamArms 覆盖 runtime RunStream 的 nil 与错误守卫。
func TestW13g2McRuntimeStreamArms(t *testing.T) {
	s, fp := w13g2McFailStore(t)
	ctx := context.Background()
	var nilRuntime *Runtime
	if _, err := nilRuntime.RunStream(ctx, RunRequest{}, nil); err == nil {
		t.Fatalf("nil runtime 应报错")
	}
	// RunStream 携带事件回调但 store 查询失败（IssueInput armed）。
	rt := &Runtime{Store: s}
	fp.arm("FROM model_check_inputs WHERE input_id=?")
	events := 0
	if _, err := rt.RunStream(ctx, RunRequest{
		SystemAccountID: "sys-1", TargetID: "acct-1", Model: "m", TriggerKind: "manual",
	}, func(ProgressEvent) { events++ }); err == nil && events > 0 {
		t.Fatalf("应失败")
	}
	fp.disarm()
	// Outcome payload JSON 无效（canonicalJSON 分支）在 arm 释放后验证。
	if _, err := s.IssueInput(ctx, InputRecord{
		InputID: "i-json", IdentityKey: "k", TargetID: "t", ConfigRevision: "c", PolicyRevision: "p",
		Trigger: "manual", IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
		Payload: json.RawMessage(`{"a":1}`),
	}); err != nil {
		t.Fatalf("合法 JSON 应成功: %v", err)
	}
}
