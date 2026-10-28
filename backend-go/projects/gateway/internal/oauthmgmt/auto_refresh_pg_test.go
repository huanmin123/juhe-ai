package oauthmgmt

// T65-①（网关上游 401 自动刷新）：RotateAccountTokensFromStored 的 PG 面测试。
// 覆盖：成功轮换（token 端点恰一次 + 凭据落库已更新）、代理守卫、CAS 冲突
// 视同成功、上游换发失败的类型化错误（不写账户状态）、非 OAuth 类型门。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// createAutoRefreshGPTAccount 经既有 OAuth 建号路由创建 gpt OAuth 账户，返回 ID。
func createAutoRefreshGPTAccount(t *testing.T, env *testEnv, name string) string {
	t.Helper()
	env.login(t, "root", "root-pass", "super_admin")
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL != OpenAIOAuthTokenURL {
			return http.StatusNotFound, `{"error":"wrong_endpoint"}`
		}
		return http.StatusOK, openAITokenPayload("create-token")
	}
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"rt","name":"`+name+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("建号: %d %v", code, created)
	}
	return dataMap(t, created)["id"].(string)
}

// attachAutoRefreshProxyProfile 插入可用代理 profile 并绑到账户上。
func attachAutoRefreshProxyProfile(t *testing.T, env *testEnv, accountID string) {
	t.Helper()
	profileID := "proxy-" + accountID
	_, err := env.db.Exec(fixtureBind(`INSERT INTO proxy_profiles
		(id, system_account_id, name, type, host, port, enabled, created_at, updated_at)
		VALUES (?, 'root-id', '刷新代理', 'http', '127.0.0.1', 7890, TRUE, ?, ?)`),
		profileID, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("seed proxy profile: %v", err)
	}
	if _, err := env.db.Exec(fixtureBind(`UPDATE accounts SET proxy_profile_id = ? WHERE id = ?`), profileID, accountID); err != nil {
		t.Fatalf("bind proxy profile: %v", err)
	}
}

// TestAutoRefreshRotateAccountTokensFromStoredSuccess：库内现读 revision 做
// CAS、token 端点恰一次（本调用增量）、轮换后凭据落库已更新（access_token /
// revision 前进）。
func TestAutoRefreshRotateAccountTokensFromStoredSuccess(t *testing.T) {
	env := newTestEnv(t)
	accountID := createAutoRefreshGPTAccount(t, env, "自动刷新成功")
	attachAutoRefreshProxyProfile(t, env, accountID)
	baseline := len(env.exchanger.recorded())
	env.exchanger.respond = staticToken(openAITokenPayload("auto-refreshed-token"))

	result, err := env.store.RotateAccountTokensFromStored(context.Background(), accountID)
	if err != nil {
		t.Fatalf("auto refresh: %v", err)
	}
	if result == nil || !result.Rotated || result.AlreadyCurrent {
		t.Fatalf("result = %+v, want rotated", result)
	}
	if got := len(env.exchanger.recorded()) - baseline; got != 1 {
		t.Fatalf("token endpoint calls = %d, want exactly 1", got)
	}
	// 库内现读：凭据已更新、revision 前进且与方法返回一致。
	current, err := env.store.findRotationAccount(context.Background(), accountID, AccessScope{IsAdmin: true})
	if err != nil || current == nil {
		t.Fatalf("re-read account: %v", err)
	}
	if token := stringCredential(current.Credentials, "access_token"); token != "auto-refreshed-token" {
		t.Fatalf("stored access_token = %q, want auto-refreshed-token（落库已更新）", token)
	}
	if current.ConfigRevision <= 1 || current.ConfigRevision != result.ConfigRevision {
		t.Fatalf("stored revision = %d, result revision = %d", current.ConfigRevision, result.ConfigRevision)
	}
}

// TestAutoRefreshProxyGuardArm：未绑定代理 → 本地错误（非上游类型），token
// 端点零调用，账户保持现状。夹具默认 WithProxyRequired(false)（proxy_required_
// test.go 同款收口），本测例重开强制拦截。
func TestAutoRefreshProxyGuardArm(t *testing.T) {
	env := newTestEnv(t)
	accountID := createAutoRefreshGPTAccount(t, env, "自动刷新缺代理")
	env.store.proxyRequired = true
	baseline := len(env.exchanger.recorded())
	env.exchanger.respond = staticToken(openAITokenPayload("should-not-happen"))

	_, err := env.store.RotateAccountTokensFromStored(context.Background(), accountID)
	if err == nil {
		t.Fatal("missing proxy must fail the refresh")
	}
	var upstream *AutoRefreshUpstreamError
	if errors.As(err, &upstream) {
		t.Fatalf("proxy guard failure is local, not upstream: %v", err)
	}
	if !strings.Contains(err.Error(), "未绑定代理") {
		t.Fatalf("proxy guard message = %v", err)
	}
	if got := len(env.exchanger.recorded()) - baseline; got != 0 {
		t.Fatalf("token endpoint calls = %d, want 0", got)
	}
}

// TestAutoRefreshConcurrentRefreshSingleWinner：并发 401 竞态臂 —— 同一账户多
// 个调用并发刷新，CAS 胜者 Rotated、败者（revision 冲突）一律按 already-current
// 视同成功（不报错），最终落库凭据一致。原「外部预 bump revision」脚本不能构
// 成冲突：本方法内部 DB 现读 config_revision（findRotationAccount），只有真实
// 并发才能踩到 RotateCredentials 的 CAS 冲突臂。
func TestAutoRefreshConcurrentRefreshSingleWinner(t *testing.T) {
	env := newTestEnv(t)
	accountID := createAutoRefreshGPTAccount(t, env, "自动刷新并发")
	attachAutoRefreshProxyProfile(t, env, accountID)
	baseline := len(env.exchanger.recorded())
	env.exchanger.respond = staticToken(openAITokenPayload("concurrent-token"))

	const racers = 4
	results := make([]*AutoRefreshResult, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for index := 0; index < racers; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = env.store.RotateAccountTokensFromStored(context.Background(), accountID)
		}(index)
	}
	wg.Wait()

	rotated, already := 0, 0
	for index := range results {
		if errs[index] != nil {
			t.Fatalf("racer %d: %v（CAS 败者必须按 already-current 视同成功）", index, errs[index])
		}
		if results[index] == nil {
			t.Fatalf("racer %d: nil result", index)
		}
		if results[index].Rotated {
			rotated++
		} else if results[index].AlreadyCurrent {
			already++
		}
	}
	if rotated < 1 || rotated+already != racers {
		t.Fatalf("rotated=%d already=%d, want rotated>=1 and total=%d", rotated, already, racers)
	}
	if got := len(env.exchanger.recorded()) - baseline; got < 1 || got > racers {
		t.Fatalf("token endpoint calls = %d, want 1..%d", got, racers)
	}
	current, err := env.store.findRotationAccount(context.Background(), accountID, AccessScope{IsAdmin: true})
	if err != nil || current == nil {
		t.Fatalf("re-read account: %v", err)
	}
	if token := stringCredential(current.Credentials, "access_token"); token != "concurrent-token" {
		t.Fatalf("stored access_token = %q, want concurrent-token", token)
	}
}

// TestAutoRefreshUpstreamFailureClassification：上游换发错误带类型化标记，
// 账户状态 / revision 不变（上游响应永不终态）。
func TestAutoRefreshUpstreamFailureClassification(t *testing.T) {
	env := newTestEnv(t)
	accountID := createAutoRefreshGPTAccount(t, env, "自动刷新上游失败")
	attachAutoRefreshProxyProfile(t, env, accountID)
	env.exchanger.respond = func(_ int, _ exchangeCall) (int, string) {
		return http.StatusBadRequest, `{"error":"invalid_grant"}`
	}

	before, err := env.store.findRotationAccount(context.Background(), accountID, AccessScope{IsAdmin: true})
	if err != nil || before == nil {
		t.Fatalf("read before: %v", err)
	}
	_, refreshErr := env.store.RotateAccountTokensFromStored(context.Background(), accountID)
	if refreshErr == nil {
		t.Fatal("upstream failure must surface")
	}
	var upstream *AutoRefreshUpstreamError
	if !errors.As(refreshErr, &upstream) {
		t.Fatalf("error = %T (%v), want AutoRefreshUpstreamError", refreshErr, refreshErr)
	}
	after, err := env.store.findRotationAccount(context.Background(), accountID, AccessScope{IsAdmin: true})
	if err != nil || after == nil {
		t.Fatalf("read after: %v", err)
	}
	if after.Status != before.Status || after.ConfigRevision != before.ConfigRevision {
		t.Fatalf("account state changed on upstream failure: before=%+v after=%+v", before, after)
	}
}

// TestAutoRefreshBlockedAccountGuardArm：OpenAI blocked 错误账户拒绝自动刷新
// （ValidationError 文案与手动刷新 handler 同口径），token 端点零调用；豁免
// 错误码（oauth_token_refresh_failed）的 error 账户不被拦——守卫放行后按
// requiresProxy 强制在代理守卫处报错，证明拒绝来自 blocked 守卫而非一刀切。
func TestAutoRefreshBlockedAccountGuardArm(t *testing.T) {
	env := newTestEnv(t)
	accountID := createAutoRefreshGPTAccount(t, env, "自动刷新blocked")
	env.exec(t, `UPDATE accounts SET status = 'error', last_error_code = 'upstream_persistent_401' WHERE id = ?`, accountID)
	baseline := len(env.exchanger.recorded())
	env.exchanger.respond = staticToken(openAITokenPayload("should-not-happen"))

	_, err := env.store.RotateAccountTokensFromStored(context.Background(), accountID)
	if err == nil {
		t.Fatal("blocked account must fail the auto refresh")
	}
	var upstream *AutoRefreshUpstreamError
	if errors.As(err, &upstream) {
		t.Fatalf("blocked guard failure is local, not upstream: %v", err)
	}
	if err.Error() != "异常账户请先执行异常恢复后再操作" {
		t.Fatalf("blocked guard message = %v（须与手动刷新同口径）", err)
	}
	if got := len(env.exchanger.recorded()) - baseline; got != 0 {
		t.Fatalf("token endpoint calls = %d, want 0", got)
	}

	// 豁免错误码不被 blocked 守卫拦截：守卫放行后在代理守卫处报错。
	if _, err := env.db.Exec(fixtureBind(`UPDATE accounts SET last_error_code = 'oauth_token_refresh_failed' WHERE id = ?`), accountID); err != nil {
		t.Fatalf("update last_error_code: %v", err)
	}
	env.store.proxyRequired = true
	if _, err := env.store.RotateAccountTokensFromStored(context.Background(), accountID); err == nil || !strings.Contains(err.Error(), "未绑定代理") {
		t.Fatalf("exempt error account must pass the blocked guard: %v", err)
	}
}

// TestAutoRefreshRotatableGateArm：命中 vendor_code + account type 但协议族
// 不可轮换（plan.rotatable 不过）→ 拒绝且错误面与手动刷新 handler 同口径
// （plan.label + " OAuth 账户不存在或无权操作"），token 端点零调用。
func TestAutoRefreshRotatableGateArm(t *testing.T) {
	env := newTestEnv(t)
	accountID := createAutoRefreshGPTAccount(t, env, "自动刷新协议族不符")
	if _, err := env.db.Exec(fixtureBind(`UPDATE accounts SET protocol_code = 'unknown_proto', protocol_version = 'v9' WHERE id = ?`), accountID); err != nil {
		t.Fatalf("corrupt protocol family: %v", err)
	}
	baseline := len(env.exchanger.recorded())
	env.exchanger.respond = staticToken(openAITokenPayload("should-not-happen"))

	_, err := env.store.RotateAccountTokensFromStored(context.Background(), accountID)
	if err == nil || err.Error() != "OpenAI OAuth 账户不存在或无权操作" {
		t.Fatalf("rotatable gate = %v, want OpenAI OAuth 账户不存在或无权操作", err)
	}
	if got := len(env.exchanger.recorded()) - baseline; got != 0 {
		t.Fatalf("token endpoint calls = %d, want 0", got)
	}
}

// TestAutoRefreshTypeGateAndMissingAccount：api_key 账户与非存在 ID 走类型门 /
// 存在性门（本地错误，token 端点零调用）。
func TestAutoRefreshTypeGateAndMissingAccount(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	sealed, err := encryptJSON(testSecret, map[string]any{"api_key": "sk-..."})
	if err != nil {
		t.Fatalf("seal credentials: %v", err)
	}
	if _, err := env.db.Exec(fixtureBind(`INSERT INTO accounts
		(id, system_account_id, vendor_code, protocol_code, protocol_version, name, type, status, credentials_encrypted, created_at, updated_at)
		VALUES ('acc-apikey', 'root-id', 'gpt', 'openai', 'v1', 'APIKey 账户', 'api_key', 'active', ?, ?, ?)`),
		sealed, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed api_key account: %v", err)
	}

	if _, err := env.store.RotateAccountTokensFromStored(context.Background(), "acc-apikey"); err == nil {
		t.Fatal("api_key account must fail the type gate")
	} else if strings.Contains(err.Error(), "不存在") {
		t.Fatalf("api_key gate message = %v（应为类型不支持而非不存在）", err)
	}
	if _, err := env.store.RotateAccountTokensFromStored(context.Background(), "acc-not-exists"); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("missing account = %v", err)
	}
	if got := len(env.exchanger.recorded()); got != 0 {
		t.Fatalf("token endpoint calls = %d, want 0", got)
	}
}
