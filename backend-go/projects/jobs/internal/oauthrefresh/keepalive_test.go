package oauthrefresh

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// newKeepaliveJobForTest shares the refresh fixture but wires the keepalive
// job over the same store/clock/exchanger.
func newKeepaliveJobForTest(t *testing.T) (*KeepaliveJob, *sql.DB, *fixedClock, *recordingExchanger) {
	t.Helper()
	_, store, db, clock, exchanger := newRefreshJobForTest(t)
	job := NewKeepaliveJob(store, exchanger, WithKeepaliveClock(clock))
	return job, db, clock, exchanger
}

func TestKeepaliveDueWindowPerProvider(t *testing.T) {
	job, db, clock, _ := newKeepaliveJobForTest(t)
	// anthropic: 60s lead — 59s is due, 61s is not.
	seedAccountRow(t, db, accountRowSeed{ID: "an-59", ProviderCode: "anthropic", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(59_000)}, Now: clock.Now()})
	seedAccountRow(t, db, accountRowSeed{ID: "an-61", ProviderCode: "anthropic", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(61_000)}, Now: clock.Now()})
	// grok: 300s lead — 299s is due, 301s is not.
	seedAccountRow(t, db, accountRowSeed{ID: "xai-299", ProviderCode: "xai", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-x", "refresh_token": "rt-x", "expires_at": expiresInMillis(299_000)}, Now: clock.Now()})
	seedAccountRow(t, db, accountRowSeed{ID: "xai-301", ProviderCode: "xai", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-x", "refresh_token": "rt-x", "expires_at": expiresInMillis(301_000)}, Now: clock.Now()})

	plans := KeepalivePlans()
	anthropicPlan, grokPlan := plans[0], plans[2]

	anthropicResult, err := job.RunOnce(context.Background(), anthropicPlan, 0)
	if err != nil {
		t.Fatal(err)
	}
	if anthropicResult.Scanned != 1 || anthropicResult.Due != 1 || anthropicResult.Refreshed != 1 {
		t.Fatalf("anthropic=%+v", anthropicResult)
	}
	grokResult, err := job.RunOnce(context.Background(), grokPlan, 0)
	if err != nil {
		t.Fatal(err)
	}
	if grokResult.Scanned != 1 || grokResult.Refreshed != 1 {
		t.Fatalf("grok=%+v", grokResult)
	}

	// Boundary: exactly at the lead window counts as due (<= lead).
	seedAccountRow(t, db, accountRowSeed{ID: "an-60", ProviderCode: "anthropic", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(60_000)}, Now: clock.Now()})
	result, err := job.RunOnce(context.Background(), anthropicPlan, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Refreshed != 1 {
		t.Fatalf("boundary=%+v", result)
	}
}

func TestKeepalivePreservesStoredBaseURL(t *testing.T) {
	job, db, clock, exchanger := newKeepaliveJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{ID: "an-base", ProviderCode: "anthropic", Type: "oauth",
		Credentials: map[string]any{
			"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(0),
			"base_url": "https://anthropic-proxy.example", "client_id": "client-kept",
		}, Now: clock.Now()})
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-a2","refresh_token":"rt-a2","expires_in":3600,
			"account":{"email_address":"a@b.c","uuid":"acc-uuid"}}`}, nil
	}
	result, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Refreshed != 1 {
		t.Fatalf("result=%+v", result)
	}
	credentials := readAccountCredentials(t, db, "an-base")
	// The operator-managed base_url survives the merge.
	if credentials["base_url"] != "https://anthropic-proxy.example" {
		t.Fatalf("base_url=%v", credentials["base_url"])
	}
	// The stored client id feeds the refresh request and the token builder
	// echoes the token info's client id (Node dispatch parity).
	if credentials["client_id"] != "client-kept" {
		t.Fatalf("client_id=%v", credentials["client_id"])
	}
	_, _, revision, _, _ := readAccountRow(t, db, "an-base")
	if revision != 2 {
		t.Fatalf("revision=%d", revision)
	}
}

func TestKeepaliveGeminiAccountTypeAndMerge(t *testing.T) {
	job, db, clock, exchanger := newKeepaliveJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{ID: "gm-1", ProviderCode: "gemini", Type: "google_oauth",
		Credentials: map[string]any{
			"access_token": "at-g", "refresh_token": "rt-g", "expires_at": expiresInMillis(0),
			"oauth_type": "ai_studio", "client_id": "cid", "client_secret": "sec",
			"base_url": "https://gemini-proxy.example", "project_id": "proj",
		}, Now: clock.Now()})
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-g2","expires_in":3600}`}, nil
	}
	result, err := job.RunOnce(context.Background(), KeepalivePlans()[1], 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Refreshed != 1 {
		t.Fatalf("result=%+v", result)
	}
	credentials := readAccountCredentials(t, db, "gm-1")
	if credentials["base_url"] != "https://gemini-proxy.example" || credentials["access_token"] != "at-g2" || credentials["oauth_type"] != "ai_studio" {
		t.Fatalf("credentials=%v", credentials)
	}
	// A missing rotated refresh token keeps the stored one.
	if credentials["refresh_token"] != "rt-g" {
		t.Fatalf("fallback refresh token lost: %v", credentials["refresh_token"])
	}
}

// T65-③：keepalive 本地失败必须写入 failurestate 计数（与 OpenAI 族对齐），
// 但不写账户终态——终态只在本地失败达到 3 次阈值时落库。
func TestKeepaliveMissingRefreshTokenRecordsFailureState(t *testing.T) {
	job, db, clock, _ := newKeepaliveJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{ID: "an-nort", ProviderCode: "anthropic", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	// Corrupt the row into the flag/credential inconsistency the dispatch
	// guard defends against: derived present flag stays 1, the decrypted
	// credentials lost their refresh token.
	sealed, err := EncryptJSON(cryptoTestSecret, map[string]any{"access_token": "at-a", "expires_at": expiresInMillis(0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET credentials_encrypted = $1 WHERE id = 'an-nort'`, sealed); err != nil {
		t.Fatal(err)
	}
	result, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 1 {
		t.Fatalf("result=%+v", result)
	}
	// 失败记账：local_configuration 类计数 +1，默认 300s 退避。
	state, readErr := job.failures.Read(context.Background(), "an-nort", clock.Now().UnixMilli(), 1)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if state == nil || state.Count != 1 || state.LocalConfigurationCount != 1 {
		t.Fatalf("failure state=%+v, want count=1 local=1", state)
	}
	if wantBackoff := clock.Now().Add(KeepaliveFailureBackoff).UnixMilli(); state.BackoffUntil != wantBackoff {
		t.Fatalf("backoffUntil=%d want %d (300s)", state.BackoffUntil, wantBackoff)
	}
	// 账户终态不写：首败只记账，账户保持 active 且无管理错误码。
	status, lastError, _, _, _ := readAccountRow(t, db, "an-nort")
	if status != "active" || lastError != "" {
		t.Fatalf("first local failure must not stop the account: status=%q code=%q", status, lastError)
	}
	var lastMessage string
	if err := db.QueryRow(`SELECT last_error_message FROM juhe_business.accounts WHERE id = 'an-nort'`).Scan(&lastMessage); err == nil && lastMessage != "" {
		t.Fatalf("keepalive must not write terminal state: %q", lastMessage)
	}
}

// T65-③：记账后的 300s 退避内，候选批内过滤把该账户跳过（SkippedBackoff），
// 不再打 token 端点；退避到期后恢复重试。
func TestKeepaliveBackoffFiltersAccountOutOfBatch(t *testing.T) {
	job, db, clock, exchanger := newKeepaliveJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{ID: "an-backoff", ProviderCode: "anthropic", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 401, Body: `{"error":"invalid_grant"}`}, nil
	}
	first, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Failed != 1 || exchanger.callCount() != 1 {
		t.Fatalf("first run=%+v calls=%d", first, exchanger.callCount())
	}
	// 退避未到期：账户被批内过滤，不计 Due、不打上游。
	second, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if second.Scanned != 1 || second.Due != 0 || second.SkippedBackoff != 1 || second.Failed != 0 {
		t.Fatalf("second run=%+v", second)
	}
	if exchanger.callCount() != 1 {
		t.Fatalf("backoff must keep the account off the token endpoint: calls=%d", exchanger.callCount())
	}
	// 上游失败只退避，不写终态。
	status, lastError, _, _, _ := readAccountRow(t, db, "an-backoff")
	if status != "active" || lastError != "" {
		t.Fatalf("upstream failure must not stop the account: status=%q code=%q", status, lastError)
	}
	// 退避到期（>300s）：恢复重试。
	clock.current = clock.current.Add(6 * time.Minute)
	third, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if third.Due != 1 || third.Failed != 1 || third.SkippedBackoff != 0 {
		t.Fatalf("third run=%+v", third)
	}
	if exchanger.callCount() != 2 {
		t.Fatalf("expired backoff must retry the endpoint: calls=%d", exchanger.callCount())
	}
}

// T65-③：本地失败 ×3 → 账户写入共享终态码（error ×
// oauth_token_refresh_local_configuration_invalid），失败状态清空，扫描不再
// 选中；随后操作者修复凭据 + 管理码清除（ClearAccountFailureState 原语）后，
// 成功刷新路径恢复 active 并完成轮换。
func TestKeepaliveLocalFailuresStopAccountThenRecover(t *testing.T) {
	job, db, clock, exchanger := newKeepaliveJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{ID: "an-stop", ProviderCode: "anthropic", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	sealed, err := EncryptJSON(cryptoTestSecret, map[string]any{"access_token": "at-a", "expires_at": expiresInMillis(0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET credentials_encrypted = $1 WHERE id = 'an-stop'`, sealed); err != nil {
		t.Fatal(err)
	}
	// 三轮本地失败（每轮推进时钟越过 300s 退避）。
	for run := 1; run <= 3; run++ {
		result, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
		if err != nil {
			t.Fatal(err)
		}
		if result.Failed != 1 || result.SkippedBackoff != 0 {
			t.Fatalf("run %d result=%+v", run, result)
		}
		if run < 3 {
			clock.current = clock.current.Add(6 * time.Minute)
		}
	}
	// 第 3 次命中阈值：账户 error × 共享终态码。
	status, lastError, _, _, _ := readAccountRow(t, db, "an-stop")
	if status != "error" || lastError != OpenAIOAuthTokenRefreshLocalConfigurationInvalidCode {
		t.Fatalf("status=%q code=%q", status, lastError)
	}
	var reason string
	if err := db.QueryRow(`SELECT last_error_message FROM juhe_business.accounts WHERE id = 'an-stop'`).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reason, "Anthropic OAuth 访问令牌连续 3 次因本地配置错误无法启动刷新，已停止自动刷新。") {
		t.Fatalf("reason=%q", reason)
	}
	// 终态落库后失败状态清空。
	state, readErr := job.failures.Read(context.Background(), "an-stop", clock.Now().UnixMilli(), 1)
	if readErr != nil || state != nil {
		t.Fatalf("failure state must be cleared after the terminal write: state=%v err=%v", state, readErr)
	}
	// 终态账户被既有扫描排除：下一轮全空。
	clock.current = clock.current.Add(6 * time.Minute)
	excluded, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if excluded.Scanned != 0 || excluded.Due != 0 || excluded.Failed != 0 {
		t.Fatalf("terminal account must stay out of the scan: %+v", excluded)
	}
	// 手动恢复场景：操作者重新授权修复凭据，ClearAccountFailureState 原语
	// 恢复 active（成功刷新路径的 restore 通道同一守卫），下一轮保活成功。
	repaired := map[string]any{"access_token": "at-fixed", "refresh_token": "rt-fixed", "expires_at": expiresInMillis(0)}
	repairedSealed, err := EncryptJSON(cryptoTestSecret, repaired)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET credentials_encrypted = $1, config_revision = config_revision + 1 WHERE id = 'an-stop'`,
		repairedSealed); err != nil {
		t.Fatal(err)
	}
	changed, restoredStatus, err := job.store.ClearAccountFailureState(context.Background(), "an-stop", ManagedRefreshErrorCodes)
	if err != nil || !changed || restoredStatus != "active" {
		t.Fatalf("restore primitive: changed=%v status=%q err=%v", changed, restoredStatus, err)
	}
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-a3","refresh_token":"rt-fixed","expires_in":3600,
			"account":{"email_address":"a@b.c","uuid":"acc-uuid"}}`}, nil
	}
	recovered, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Refreshed != 1 {
		t.Fatalf("recovered run=%+v", recovered)
	}
	credentials := readAccountCredentials(t, db, "an-stop")
	if credentials["access_token"] != "at-a3" {
		t.Fatalf("credentials=%v", credentials)
	}
}

// T65-③：失败状态读取失败时保守跳过该账户（不计 Due、不打上游、不报错），
// 其余账户与后续轮次不受影响（对齐 refresh.go selectBatchCandidates P0 语义）。
func TestKeepaliveConservativeSkipWhenFailureStateReadFails(t *testing.T) {
	job, db, clock, exchanger := newKeepaliveJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{ID: "an-broken-read", ProviderCode: "anthropic", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	seedAccountRow(t, db, accountRowSeed{ID: "an-readable", ProviderCode: "anthropic", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-b", "refresh_token": "rt-b", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-new","refresh_token":"rt-new","expires_in":3600,
			"account":{"email_address":"a@b.c","uuid":"acc-uuid"}}`}, nil
	}
	job.failures = &p0FailingReadStore{
		memory: NewMemoryFailureStateStore(),
		errFor: map[string]error{"an-broken-read": errors.New("redis down")},
	}
	result, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatalf("failure-state read error must not fail the cycle: %v", err)
	}
	if result.Due != 1 || result.Refreshed != 1 || result.Scanned != 2 {
		t.Fatalf("result=%+v", result)
	}
	if exchanger.callCount() != 1 {
		t.Fatalf("upstream calls=%d, want exactly the readable account", exchanger.callCount())
	}
	if credentials := readAccountCredentials(t, db, "an-broken-read"); credentials["access_token"] != "at-a" {
		t.Fatalf("unreadable-state account must stay untouched: %v", credentials)
	}
}

// T65-③：有记账的账户刷新成功 → failurestate 清空（Clear+原语）。
func TestKeepaliveSuccessClearsFailureState(t *testing.T) {
	job, db, clock, exchanger := newKeepaliveJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{ID: "an-clear", ProviderCode: "anthropic", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 401, Body: `{"error":"invalid_grant"}`}, nil
	}
	if _, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0); err != nil {
		t.Fatal(err)
	}
	state, readErr := job.failures.Read(context.Background(), "an-clear", clock.Now().UnixMilli(), 1)
	if readErr != nil || state == nil || state.Count != 1 || state.LocalConfigurationCount != 0 {
		t.Fatalf("upstream failure must record untrusted state: state=%+v err=%v", state, readErr)
	}
	// 第二轮刷新成功：清账（不残留退避，也不误写终态）。
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-a2","refresh_token":"rt-a","expires_in":3600}`}, nil
	}
	clock.current = clock.current.Add(6 * time.Minute)
	second, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if second.Refreshed != 1 {
		t.Fatalf("second run=%+v", second)
	}
	status, lastError, _, _, _ := readAccountRow(t, db, "an-clear")
	if status != "active" || lastError != "" {
		t.Fatalf("status=%q code=%q", status, lastError)
	}
	cleared, clearErr := job.failures.Read(context.Background(), "an-clear", clock.Now().UnixMilli(), 2)
	if clearErr != nil || cleared != nil {
		t.Fatalf("success must clear the failure state: state=%v err=%v", cleared, clearErr)
	}
	// 退避已清：同轮时间内不再被批内过滤。
	clock.current = clock.current.Add(6 * time.Minute)
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-a3","refresh_token":"rt-a","expires_in":3600}`}, nil
	}
	third, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if third.SkippedBackoff != 0 {
		t.Fatalf("cleared backoff must not filter the account: %+v", third)
	}
}

func TestKeepaliveConflictAdoptsFreshConcurrentRefresh(t *testing.T) {
	job, db, clock, exchanger := newKeepaliveJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{ID: "an-race", ProviderCode: "anthropic", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	// A concurrent writer completes a refresh before our persist: the first
	// CAS conflicts and the re-read finds fresh credentials, so the keepalive
	// must report "skipped fresh" instead of writing.
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-a2","refresh_token":"rt-a","expires_in":3600}`}, nil
	}
	persistCalls := 0
	job.updateCredentials = func(ctx context.Context, accountID string, credentials map[string]any, expected int64) (*RotationAccount, error) {
		persistCalls++
		// The concurrent writer rotated the refresh token too, which is the
		// Node adopt-fresh precondition.
		fresh := map[string]any{"access_token": "at-concurrent", "refresh_token": "rt-concurrent", "expires_at": expiresInMillis(400_000)}
		sealed, err := EncryptJSON(cryptoTestSecret, fresh)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE juhe_business.accounts SET config_revision = config_revision + 3,
			credentials_encrypted = $1, oauth_access_token_expires_at = $2 WHERE id = 'an-race'`,
			sealed, expiresInMillis(400_000)); err != nil {
			t.Fatal(err)
		}
		return nil, &RevisionConflictError{Message: "账户已被其他请求修改，请重试"}
	}
	result, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Refreshed != 0 || result.Failed != 0 || result.SkippedFresh != 1 {
		t.Fatalf("result=%+v", result)
	}
	credentials := readAccountCredentials(t, db, "an-race")
	if credentials["access_token"] != "at-concurrent" || credentials["refresh_token"] != "rt-concurrent" {
		t.Fatalf("concurrent credentials must stay: %v", credentials)
	}
	if persistCalls != 1 {
		t.Fatalf("persist calls=%d", persistCalls)
	}
}

func TestKeepaliveConflictExhaustionFails(t *testing.T) {
	job, db, clock, exchanger := newKeepaliveJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{ID: "an-exhaust", ProviderCode: "anthropic", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	// The concurrent writer keeps winning the CAS; after 3 attempts the
	// keepalive gives up with the conflict and the stored credentials stay.
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-a2","refresh_token":"rt-a","expires_in":3600}`}, nil
	}
	job.updateCredentials = func(ctx context.Context, accountID string, credentials map[string]any, expected int64) (*RotationAccount, error) {
		return nil, &RevisionConflictError{Message: "账户已被其他请求修改，请重试"}
	}
	result, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 1 || result.Refreshed != 0 {
		t.Fatalf("result=%+v", result)
	}
	credentials := readAccountCredentials(t, db, "an-exhaust")
	if credentials["access_token"] != "at-a" {
		t.Fatalf("stored credentials must be untouched: %v", credentials)
	}
}
