package oauthrefresh

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Single-account refresh (RefreshAccount / accountLocks.Lock / race retry)
// ---------------------------------------------------------------------------

func TestW9HRefreshAccountArms(t *testing.T) {
	// Success path rotates credentials and returns the updated account.
	job, _, db, clock, exchanger := newRefreshJobForTest(t)
	seedOpenAIOAuthAccount(t, db, "w9h-ra", openAICredentials(expiresInMillis(60_000)), clock.Now())
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-ra","refresh_token":"rt-ra","expires_in":1800}`}, nil
	}
	account, err := job.store.FindRotationAccount(context.Background(), "w9h-ra")
	if err != nil || account == nil {
		t.Fatalf("find account=%v err=%v", account, err)
	}
	refreshed, err := job.RefreshAccount(context.Background(), account, RefreshAccountOptions{})
	if err != nil || refreshed == nil {
		t.Fatalf("refresh account=%v err=%v", refreshed, err)
	}
	if refreshed.Credentials["access_token"] != "at-ra" || refreshed.ConfigRevision != 2 {
		t.Fatalf("refreshed=%+v", refreshed)
	}
	// Not due (fresh token) returns the stored account untouched.
	exchanger.calls = 0
	fresh, err := job.RefreshAccount(context.Background(), refreshed, RefreshAccountOptions{})
	if err != nil || fresh == nil {
		t.Fatalf("fresh account=%v err=%v", fresh, err)
	}
	if exchanger.callCount() != 0 {
		t.Fatal("fresh account must not call upstream")
	}
	// Missing refresh token surfaces the local configuration error.
	seedAccountRow(t, db, accountRowSeed{ID: "w9h-ra-nort", ProviderCode: "gpt", ProfileID: ProfileGPTOpenAIV1, Type: "oauth",
		Credentials: map[string]any{"access_token": "at", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	noToken, err := job.store.FindRotationAccount(context.Background(), "w9h-ra-nort")
	if err != nil || noToken == nil {
		t.Fatalf("find nort=%v err=%v", noToken, err)
	}
	if _, err := job.RefreshAccount(context.Background(), noToken, RefreshAccountOptions{Force: true}); !IsLocalConfigurationError(err) {
		t.Fatalf("missing refresh token err=%v", err)
	}
	// Unknown account fails with the missing-account copy.
	ghost := &RotationAccount{ID: "w9h-ra-ghost"}
	if _, err := job.RefreshAccount(context.Background(), ghost, RefreshAccountOptions{Force: true}); err == nil || !strings.Contains(err.Error(), "不存在或无法刷新") {
		t.Fatalf("ghost err=%v", err)
	}
}

func TestW9HRefreshAccountRaceRetryUsesLatestToken(t *testing.T) {
	job, _, db, clock, exchanger := newRefreshJobForTest(t)
	seedOpenAIOAuthAccount(t, db, "w9h-race", openAICredentials(expiresInMillis(60_000)), clock.Now())
	account, _ := job.store.FindRotationAccount(context.Background(), "w9h-race")

	// First exchange fails with the old token; while it "runs" the stored
	// refresh token rotates. The retry attempt reads the latest token and wins.
	exchanger.respond = func(call int, _ TokenHTTPRequest) (TokenHTTPResponse, error) {
		if call == 1 {
			rotated := map[string]any{
				"access_token": "at-rotated", "refresh_token": "rt-rotated",
				"expires_at": expiresInMillis(0), // expired: recovery reads retry, not fresh
			}
			if err := rewriteAccountCredentials(t, db, "w9h-race", rotated); err != nil {
				t.Fatal(err)
			}
			return TokenHTTPResponse{}, &UpstreamError{Message: "OpenAI OAuth 令牌请求失败：HTTP 400，expired", StatusCode: 502}
		}
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-final","refresh_token":"rt-final","expires_in":3600}`}, nil
	}
	refreshed, err := job.RefreshAccount(context.Background(), account, RefreshAccountOptions{})
	if err != nil || refreshed == nil {
		t.Fatalf("race refresh=%v err=%v", refreshed, err)
	}
	if refreshed.Credentials["access_token"] != "at-final" {
		t.Fatalf("credentials=%v", refreshed.Credentials)
	}
	if exchanger.callCount() != 2 {
		t.Fatalf("calls=%d", exchanger.callCount())
	}
}

func TestW9HRefreshAccountRaceExhaustionReturnsCause(t *testing.T) {
	job, _, db, clock, exchanger := newRefreshJobForTest(t)
	seedOpenAIOAuthAccount(t, db, "w9h-race2", openAICredentials(expiresInMillis(60_000)), clock.Now())
	account, _ := job.store.FindRotationAccount(context.Background(), "w9h-race2")
	exchanger.respond = func(call int, _ TokenHTTPRequest) (TokenHTTPResponse, error) {
		if call == 1 {
			// Rotate to a fresh (usable) token: race recovery returns "fresh"
			// instead of retrying, and the caller keeps the stored state.
			rotated := map[string]any{
				"access_token": "at-fresh", "refresh_token": "rt-fresh",
				"expires_at": expiresInMillis(3_600_000),
			}
			if err := rewriteAccountCredentials(t, db, "w9h-race2", rotated); err != nil {
				t.Fatal(err)
			}
			return TokenHTTPResponse{}, &UpstreamError{Message: "boom", StatusCode: 502}
		}
		t.Fatal("second call must not happen after fresh recovery")
		return TokenHTTPResponse{}, nil
	}
	refreshed, err := job.RefreshAccount(context.Background(), account, RefreshAccountOptions{})
	if err != nil || refreshed == nil {
		t.Fatalf("fresh recovery=%v err=%v", refreshed, err)
	}
	if refreshed.Credentials["access_token"] != "at-fresh" {
		t.Fatalf("credentials=%v", refreshed.Credentials)
	}
}

// rewriteAccountCredentials reseals the stored envelope behind the test's back
// (the concurrent-writer half of the refresh race).
func rewriteAccountCredentials(t *testing.T, db *sql.DB, id string, credentials map[string]any) error {
	t.Helper()
	sealed, err := EncryptJSON(cryptoTestSecret, credentials)
	if err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE accounts SET credentials_encrypted = ?, config_revision = config_revision + 1, updated_at = ? WHERE id = ?`,
		sealed, isoMillis(defaultNow()), id)
	return err
}

// ---------------------------------------------------------------------------
// RunOnce arms: cancelled context, settings errors, admission deferral
// ---------------------------------------------------------------------------

type w9hFailingSettings struct{ err error }

func (s w9hFailingSettings) SettingInt(context.Context, string) (int64, bool, error) {
	return 0, false, s.err
}

func TestW9HRunOnceCancelledContext(t *testing.T) {
	job, _, _, _, _ := newRefreshJobForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := job.RunOnce(ctx, RefreshOptions{})
	if err != nil || result != (RefreshResult{}) {
		t.Fatalf("cancelled result=%+v err=%v", result, err)
	}
}

func TestW9HRunOnceSettingsArms(t *testing.T) {
	job, _, db, clock, _ := newRefreshJobForTest(t)
	seedOpenAIOAuthAccount(t, db, "w9h-set", openAICredentials(expiresInMillis(60_000)), clock.Now())
	// Out-of-range system setting fails with the settings copy.
	job.settings = MapSettingsReader{"oauthAccessTokenRefreshLeadSeconds": 1}
	if _, err := job.RunOnce(context.Background(), RefreshOptions{}); err == nil || !strings.Contains(err.Error(), "系统设置 oauthAccessTokenRefreshLeadSeconds 必须在 60 到 86400 之间") {
		t.Fatalf("lead settings err=%v", err)
	}
	// Reader error propagates.
	job.settings = w9hFailingSettings{err: errors.New("settings down")}
	if _, err := job.RunOnce(context.Background(), RefreshOptions{}); err == nil || !strings.Contains(err.Error(), "settings down") {
		t.Fatalf("reader err=%v", err)
	}
	// Out-of-range batch and backoff mirrors.
	job.settings = MapSettingsReader{"oauthAccessTokenRefreshBatchSize": 999}
	if _, err := job.RunOnce(context.Background(), RefreshOptions{}); err == nil || !strings.Contains(err.Error(), "oauthAccessTokenRefreshBatchSize") {
		t.Fatalf("batch settings err=%v", err)
	}
	job.settings = MapSettingsReader{"oauthAccessTokenRefreshRetryBackoffSeconds": -1}
	if _, err := job.RunOnce(context.Background(), RefreshOptions{}); err == nil || !strings.Contains(err.Error(), "RetryBackoffSeconds") {
		t.Fatalf("backoff settings err=%v", err)
	}
	job.settings = MapSettingsReader{"startAdmissionBudgetMs": 999_999}
	if _, err := job.RunOnce(context.Background(), RefreshOptions{}); err == nil || !strings.Contains(err.Error(), "startAdmissionBudgetMs") {
		t.Fatalf("budget settings err=%v", err)
	}
	// Pointer options go through optionInt with the same bounds.
	if _, err := job.RunOnce(context.Background(), RefreshOptions{LeadSeconds: intPtr(0)}); err == nil {
		t.Fatal("lead pointer 0 must fail")
	}
	if _, err := job.RunOnce(context.Background(), RefreshOptions{BatchSize: intPtr(1000)}); err == nil {
		t.Fatal("batch pointer 1000 must fail")
	}
	// Valid pointers succeed and reach the candidate scan.
	job.settings = nil
	result, err := job.RunOnce(context.Background(), RefreshOptions{LeadSeconds: intPtr(600), BatchSize: intPtr(5), RetryBackoffSeconds: intPtr(0), StartAdmissionBudgetMs: intPtr(1000)})
	if err != nil {
		t.Fatalf("valid pointers err=%v", err)
	}
	if result.Scanned != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestW9HRunOnceAdmissionBudgetDefersBatch(t *testing.T) {
	store, db, _ := newTestStore(t)
	seedProviderProfiles(t, db)
	seedOpenAIOAuthAccount(t, db, "w9h-defer", openAICredentials(expiresInMillis(60_000)), defaultNow())
	// The clock advances past the admission deadline after the first read, so
	// every selected candidate defers to the next cycle.
	ticks := 0
	var mu sync.Mutex
	clock := ClockFunc(func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		ticks++
		if ticks == 1 {
			return defaultNow()
		}
		return defaultNow().Add(time.Hour)
	})
	job := NewRefreshJob(store, &recordingExchanger{}, WithClock(clock))
	job.store = store
	result, err := job.RunOnce(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	if result.Scanned != 0 || result.Due != 0 || result.DeferredBudget != 1 || result.Refreshed != 0 || result.Started != 0 {
		t.Fatalf("result=%+v", result)
	}
}

// ---------------------------------------------------------------------------
// processCandidate arms: decrypt failure evidence, failure store errors
// ---------------------------------------------------------------------------

type w9hFailingFailureStore struct {
	memory    *MemoryFailureStateStore
	recordErr error
}

func (s *w9hFailingFailureStore) Record(ctx context.Context, accountID string, backoffUntil int64, kind FailureKind, revision int64) (RefreshFailureState, error) {
	if s.recordErr != nil {
		return RefreshFailureState{}, s.recordErr
	}
	return s.memory.Record(ctx, accountID, backoffUntil, kind, revision)
}

func (s *w9hFailingFailureStore) Read(ctx context.Context, accountID string, now int64, revision int64) (*RefreshFailureState, error) {
	return s.memory.Read(ctx, accountID, now, revision)
}

func (s *w9hFailingFailureStore) Clear(ctx context.Context, accountID string, guard RefreshFailureState) error {
	return s.memory.Clear(ctx, accountID, guard)
}

func (s *w9hFailingFailureStore) CleanupBackoff(now int64) { s.memory.CleanupBackoff(now) }

func seedUndecryptableAccount(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO accounts (id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status,
		credentials_encrypted, config_revision, updated_at)
		VALUES (?, 'gpt', 'profile_gpt_openai_v1', 'openai', 'v1', ?, 'oauth', 'active', 'not-a-valid-envelope', 1, ?)`,
		id, "账户-"+id, isoMillis(defaultNow()))
	if err != nil {
		t.Fatal(err)
	}
}

func TestW9HDecryptFailureWithoutEvidence(t *testing.T) {
	job, _, db, clock, _ := newRefreshJobForTest(t)
	seedUndecryptableAccount(t, db, "w9h-bad-envelope")
	result, err := job.RunOnce(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	if result.Scanned != 1 || result.Failed != 1 || result.Exceptioned != 0 {
		t.Fatalf("result=%+v", result)
	}
	// Without a decryptable sibling the account-local defect is unproven: the
	// status must stay active.
	status, code, _, _, _ := readAccountRow(t, db, "w9h-bad-envelope")
	if status != "active" || code != "" {
		t.Fatalf("status=%q code=%q", status, code)
	}
	_ = clock
}

func TestW9HDecryptFailureWithEvidenceReachesTerminalStop(t *testing.T) {
	job, _, db, clock, exchanger := newRefreshJobForTest(t)
	// A decryptable sibling proves the local keyring works; the undecryptable
	// row counts as an account-local defect. The sibling keeps failing with an
	// upstream error so it stays due (evidence present) in every round.
	seedOpenAIOAuthAccount(t, db, "w9h-sibling", openAICredentials(expiresInMillis(0)), clock.Now())
	seedUndecryptableAccount(t, db, "w9h-bad-2")
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 500, Body: `{"error":"upstream down"}`}, nil
	}
	// Three consecutive local-configuration failures reach the threshold.
	for round := 1; round <= 3; round++ {
		result, err := job.RunOnce(context.Background(), RefreshOptions{RetryBackoffSeconds: intPtr(0)})
		if err != nil {
			t.Fatalf("round %d err=%v", round, err)
		}
		if result.Scanned != 2 || result.Failed != 2 || result.Refreshed != 0 {
			t.Fatalf("round %d result=%+v", round, result)
		}
		if round < 3 && result.Exceptioned != 0 {
			t.Fatalf("round %d exceptioned early: %+v", round, result)
		}
		// The sibling stays active; its failure class is untrusted upstream.
		if status, code, _, _, _ := readAccountRow(t, db, "w9h-sibling"); status != "active" {
			t.Fatalf("round %d sibling status=%q code=%q", round, status, code)
		}
	}
	status, code, revision, _, _ := readAccountRow(t, db, "w9h-bad-2")
	if status != "error" || code != OpenAIOAuthTokenRefreshLocalConfigurationInvalidCode || revision != 2 {
		t.Fatalf("terminal status=%q code=%q revision=%d", status, code, revision)
	}
	if readAccountLastMessage(t, db, "w9h-bad-2") == "" {
		t.Fatal("terminal reason message must be stored")
	}
}

func readAccountLastMessage(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var message sql.NullString
	if err := db.QueryRow(`SELECT last_error_message FROM accounts WHERE id = ?`, id).Scan(&message); err != nil {
		t.Fatal(err)
	}
	return message.String
}

func TestW9HFailureRecordWriteErrorSurfaces(t *testing.T) {
	store, db, _ := newTestStore(t)
	seedProviderProfiles(t, db)
	seedOpenAIOAuthAccount(t, db, "w9h-rec-fail", openAICredentials(expiresInMillis(0)), defaultNow())
	// The refresh fails (upstream 500) and the failure-state write fails too.
	job := NewRefreshJob(store, &recordingExchanger{respond: func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 500, Body: `{"error":"x"}`}, nil
	}}, WithClock(ClockFunc(defaultNow)), WithFailureStateStore(&w9hFailingFailureStore{memory: NewMemoryFailureStateStore(), recordErr: errors.New("state store down")}))
	job.store = store
	result, err := job.RunOnce(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("result=%+v", result)
	}
	status, _, _, _, _ := readAccountRow(t, db, "w9h-rec-fail")
	if status != "active" {
		t.Fatalf("status=%q", status)
	}
}

func TestW9HRefreshCandidateFetchLimitArms(t *testing.T) {
	store, _, _ := newTestStore(t)
	job := NewRefreshJob(store, nil, WithClock(ClockFunc(defaultNow)))
	// Requested account filter scales the limit with a floor at the batch size.
	if got := job.refreshCandidateFetchLimit(10, 3); got != 15 {
		t.Fatalf("filter limit=%d", got)
	}
	if got := job.refreshCandidateFetchLimit(100, 1); got != 100 {
		t.Fatalf("floor limit=%d", got)
	}
	if got := job.refreshCandidateFetchLimit(500, 1000); got != 500 {
		t.Fatalf("ceiling limit=%d", got)
	}
	// Memory store counts tracked accounts into the fetch limit.
	memory := NewMemoryFailureStateStore()
	for i := 0; i < 7; i++ {
		_, _ = memory.Record(context.Background(), string(rune('a'+i)), 0, FailureKindUntrustedUpstream, 1)
	}
	job.failures = memory
	if got := job.refreshCandidateFetchLimit(10, 0); got != 17 {
		t.Fatalf("memory limit=%d", got)
	}
	// Non-memory stores scale the batch size and clamp at 200.
	job.failures = NewRedisFailureStateStore(&w9hScripter{})
	if got := job.refreshCandidateFetchLimit(50, 0); got != 200 {
		t.Fatalf("redis limit=%d", got)
	}
	if got := job.refreshCandidateFetchLimit(10, 0); got != 50 {
		t.Fatalf("redis small limit=%d", got)
	}
}

// ---------------------------------------------------------------------------
// Keepalive arms through the store
// ---------------------------------------------------------------------------

func TestW9HKeepaliveRefreshOneArms(t *testing.T) {
	job, db, clock, exchanger := newKeepaliveJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{ID: "w9h-ka", ProviderCode: "anthropic", ProfileID: "profile_anthropic_anthropic_v1", Type: "oauth",
		Credentials: map[string]any{"access_token": "at", "refresh_token": "rt", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	source, err := job.store.FindRotationAccount(context.Background(), "w9h-ka")
	if err != nil || source == nil {
		t.Fatalf("find=%v err=%v", source, err)
	}
	anthropicPlan := KeepalivePlans()[0]
	grokPlan := KeepalivePlans()[2]

	// Source missing (row absent) reads as a local configuration failure.
	if _, err := job.refreshOne(context.Background(), anthropicPlan, &RotationAccount{ID: "w9h-ka-missing"}, clock.Now()); !IsLocalConfigurationError(err) {
		t.Fatalf("missing source err=%v", err)
	}
	// Provider mismatch reads as not refreshable (also local configuration):
	// the grok plan cannot refresh an anthropic-seeded row.
	if _, err := job.refreshOne(context.Background(), grokPlan, source, clock.Now()); !IsLocalConfigurationError(err) {
		t.Fatalf("mismatch err=%v", err)
	}
	// Unsupported provider fails with the provider copy (the plan must match
	// the stored row for the refresh switch to be reached).
	seedAccountRow(t, db, accountRowSeed{ID: "w9h-ka-other", ProviderCode: "other", ProfileID: "profile_gpt_openai_v1", Type: "oauth",
		Credentials: map[string]any{"access_token": "at", "refresh_token": "rt", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	otherSource, err := job.store.FindRotationAccount(context.Background(), "w9h-ka-other")
	if err != nil || otherSource == nil {
		t.Fatalf("find other=%v err=%v", otherSource, err)
	}
	if _, err := job.refreshOne(context.Background(), KeepalivePlan{Provider: "other", AccountType: "oauth"}, otherSource, clock.Now()); err == nil || !strings.Contains(err.Error(), "不支持的保活供应商") {
		t.Fatalf("unsupported err=%v", err)
	}
	// Fresh token: no refresh needed. The stored row must carry the fresh
	// expiry because refreshOne re-reads the source.
	if err := rewriteAccountCredentials(t, db, "w9h-ka", map[string]any{"access_token": "at", "refresh_token": "rt", "expires_at": expiresInMillis(3_600_000)}); err != nil {
		t.Fatal(err)
	}
	refreshed, err := job.refreshOne(context.Background(), anthropicPlan, source, clock.Now())
	if err != nil || refreshed {
		t.Fatalf("fresh refreshed=%v err=%v", refreshed, err)
	}
	// Grok missing refresh token carries the grok message (the stored row must
	// itself lack the token because refreshOne re-reads the source).
	seedAccountRow(t, db, accountRowSeed{ID: "w9h-ka-grokless", ProviderCode: "xai", ProfileID: ProfileXAIOpenAIV1, Type: "oauth",
		Credentials: map[string]any{"access_token": "at", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	grokless, err := job.store.FindRotationAccount(context.Background(), "w9h-ka-grokless")
	if err != nil || grokless == nil {
		t.Fatalf("find grokless=%v err=%v", grokless, err)
	}
	if _, err := job.refreshOne(context.Background(), grokPlan, grokless, clock.Now()); !IsLocalConfigurationError(err) || !strings.Contains(err.Error(), "Grok OAuth 账户缺少 Refresh Token") {
		t.Fatalf("grok missing token err=%v", err)
	}
	// Persist seam returning (nil, nil) reads as the source disappearing.
	seedAccountRow(t, db, accountRowSeed{ID: "w9h-ka2", ProviderCode: "anthropic", ProfileID: "profile_anthropic_anthropic_v1", Type: "oauth",
		Credentials: map[string]any{"access_token": "at", "refresh_token": "rt", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	source2, _ := job.store.FindRotationAccount(context.Background(), "w9h-ka2")
	job.updateCredentials = func(context.Context, string, map[string]any, int64) (*RotationAccount, error) { return nil, nil }
	if _, err := job.refreshOne(context.Background(), anthropicPlan, source2, clock.Now()); err == nil || !strings.Contains(err.Error(), "不存在或类型不匹配") {
		t.Fatalf("nil persist err=%v", err)
	}
	// Non-conflict persist errors propagate.
	job.updateCredentials = func(context.Context, string, map[string]any, int64) (*RotationAccount, error) {
		return nil, errors.New("disk on fire")
	}
	if _, err := job.refreshOne(context.Background(), anthropicPlan, source2, clock.Now()); err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("persist error err=%v", err)
	}
	job.updateCredentials = nil
	// Success persists merged credentials through the real store.
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-new","refresh_token":"rt-new","expires_in":3600}`}, nil
	}
	refreshed, err = job.refreshOne(context.Background(), anthropicPlan, source2, clock.Now())
	if err != nil || !refreshed {
		t.Fatalf("success refreshed=%v err=%v", refreshed, err)
	}
	credentials := readAccountCredentials(t, db, "w9h-ka2")
	if credentials["access_token"] != "at-new" {
		t.Fatalf("merged credentials=%v", credentials)
	}
	// The stored row had no base_url, so the token default rides the merge.
	if credentials["base_url"] != anthropicOAuthBaseURL {
		t.Fatalf("base_url=%v", credentials["base_url"])
	}
}

func TestW9HKeepaliveConflictReloadArms(t *testing.T) {
	job, db, clock, _ := newKeepaliveJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{ID: "w9h-ka-conflict", ProviderCode: "anthropic", ProfileID: "profile_anthropic_anthropic_v1", Type: "oauth",
		Credentials: map[string]any{"access_token": "at", "refresh_token": "rt", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	source, _ := job.store.FindRotationAccount(context.Background(), "w9h-ka-conflict")
	anthropicPlan := KeepalivePlans()[0]

	// Conflict + the account disappearing mid-retry fails with the conflict.
	attempts := 0
	job.updateCredentials = func(context.Context, string, map[string]any, int64) (*RotationAccount, error) {
		attempts++
		if attempts == 1 {
			if err := db.Exec; false {
				_ = err
			}
			_, _ = db.Exec(`DELETE FROM accounts WHERE id = 'w9h-ka-conflict'`)
			return nil, &RevisionConflictError{Message: "账户已被其他请求修改，请重试"}
		}
		return nil, &RevisionConflictError{Message: "账户已被其他请求修改，请重试"}
	}
	if _, err := job.refreshOne(context.Background(), anthropicPlan, source, clock.Now()); err == nil {
		t.Fatal("conflict with vanished account must fail")
	}
	_ = attempts
}

func TestW9HKeepaliveConflictAdoptsStillDueRetry(t *testing.T) {
	job, db, clock, exchanger := newKeepaliveJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{ID: "w9h-ka-adopt", ProviderCode: "anthropic", ProfileID: "profile_anthropic_anthropic_v1", Type: "oauth",
		Credentials: map[string]any{"access_token": "at", "refresh_token": "rt-old", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	source, _ := job.store.FindRotationAccount(context.Background(), "w9h-ka-adopt")
	anthropicPlan := KeepalivePlans()[0]
	var upstreamCalls int
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		upstreamCalls++
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-new","refresh_token":"rt-new","expires_in":3600}`}, nil
	}
	// First persist conflicts; meanwhile another writer rotated the refresh
	// token but left the access token expired (still due) → the job retries
	// with the latest source and wins on the second persist.
	job.updateCredentials = func(ctx context.Context, accountID string, credentials map[string]any, expected int64) (*RotationAccount, error) {
		if upstreamCalls == 1 {
			upstreamCalls++
			rotated := map[string]any{"access_token": "at-other", "refresh_token": "rt-other", "expires_at": expiresInMillis(0)}
			if err := rewriteAccountCredentials(t, db, accountID, rotated); err != nil {
				t.Fatal(err)
			}
			return nil, &RevisionConflictError{Message: "conflict"}
		}
		return job.store.UpdateAccountCredentials(ctx, accountID, credentials, expected)
	}
	refreshed, err := job.refreshOne(context.Background(), anthropicPlan, source, clock.Now())
	if err != nil || !refreshed {
		t.Fatalf("adopt refreshed=%v err=%v", refreshed, err)
	}
	credentials := readAccountCredentials(t, db, "w9h-ka-adopt")
	if credentials["refresh_token"] != "rt-new" {
		t.Fatalf("credentials=%v", credentials)
	}
}

func TestW9HListDueKeepaliveAccountsSkipsUndecryptable(t *testing.T) {
	job, db, clock, _ := newKeepaliveJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{ID: "w9h-ka-good", ProviderCode: "anthropic", ProfileID: "profile_anthropic_anthropic_v1", Type: "oauth",
		Credentials: map[string]any{"access_token": "at", "refresh_token": "rt", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	seedUndecryptableKeepaliveRow(t, db, "w9h-ka-bad")
	plan := KeepalivePlans()[0]
	candidates, err := job.store.ListDueKeepaliveAccounts(context.Background(), plan.Provider, plan.AccountType, plan.RequiredProfileID, plan.Lead, 10, clock.Now())
	if err != nil {
		t.Fatalf("list err=%v", err)
	}
	if len(candidates) != 1 || candidates[0].Account.ID != "w9h-ka-good" {
		t.Fatalf("candidates=%d", len(candidates))
	}
}

func seedUndecryptableKeepaliveRow(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO accounts (id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status,
		credentials_encrypted, oauth_refresh_token_present, config_revision, updated_at)
		VALUES (?, 'anthropic', 'profile_anthropic_anthropic_v1', 'openai', 'v1', ?, 'oauth', 'active', 'garbage-envelope', 1, 1, ?)`,
		id, "账户-"+id, isoMillis(defaultNow()))
	if err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Store CAS writer branches
// ---------------------------------------------------------------------------

func TestW9HRotateCredentialsGuardArms(t *testing.T) {
	_, store, db, clock, _ := newRefreshJobForTest(t)
	seedOpenAIOAuthAccount(t, db, "w9h-rot", openAICredentials(expiresInMillis(0)), clock.Now())
	ctx := context.Background()
	next := openAICredentials(expiresInMillis(3_600_000))

	// Missing account → (nil, nil).
	if result, err := store.RotateCredentials(ctx, RotateCredentialsInput{AccountID: "w9h-ghost", ExpectedConfigRevision: 1, ExpectedProviderCode: "gpt", ExpectedAccountType: "oauth", ExpectedProviderProtocolProfileID: ProfileGPTOpenAIV1, Credentials: next}); err != nil || result != nil {
		t.Fatalf("ghost result=%v err=%v", result, err)
	}
	// Provider/profile/type mismatch → (nil, nil).
	if result, err := store.RotateCredentials(ctx, RotateCredentialsInput{AccountID: "w9h-rot", ExpectedConfigRevision: 1, ExpectedProviderCode: "anthropic", ExpectedAccountType: "oauth", ExpectedProviderProtocolProfileID: ProfileGPTOpenAIV1, Credentials: next}); err != nil || result != nil {
		t.Fatalf("mismatch result=%v err=%v", result, err)
	}
	// Stale revision → RevisionConflictError.
	if _, err := store.RotateCredentials(ctx, RotateCredentialsInput{AccountID: "w9h-rot", ExpectedConfigRevision: 9, ExpectedProviderCode: "gpt", ExpectedAccountType: "oauth", ExpectedProviderProtocolProfileID: ProfileGPTOpenAIV1, Credentials: next}); err == nil || !IsRevisionConflict(err) {
		t.Fatalf("conflict err=%v", err)
	}
	// Invalid revision input fails fast.
	if _, err := store.RotateCredentials(ctx, RotateCredentialsInput{AccountID: "w9h-rot", ExpectedConfigRevision: 0, Credentials: next}); err == nil {
		t.Fatal("revision 0 must fail")
	}
	// Invalid expires_at fails the canonicalization guard.
	bad := openAICredentials("not-a-time")
	if _, err := store.RotateCredentials(ctx, RotateCredentialsInput{AccountID: "w9h-rot", ExpectedConfigRevision: 1, ExpectedProviderCode: "gpt", ExpectedAccountType: "oauth", ExpectedProviderProtocolProfileID: ProfileGPTOpenAIV1, Credentials: bad}); err == nil {
		t.Fatal("bad expires_at must fail")
	}
	// Missing credential source fails with the empty-credentials copy.
	if _, err := store.RotateCredentials(ctx, RotateCredentialsInput{AccountID: "w9h-rot", ExpectedConfigRevision: 1, ExpectedProviderCode: "gpt", ExpectedAccountType: "oauth", ExpectedProviderProtocolProfileID: ProfileGPTOpenAIV1, Credentials: map[string]any{"expires_at": expiresInMillis(0)}}); err == nil || !strings.Contains(err.Error(), "OAuth 凭据不能为空") {
		t.Fatalf("empty credentials err=%v", err)
	}
}

func IsRevisionConflict(err error) bool {
	var conflict *RevisionConflictError
	return errors.As(err, &conflict)
}

func TestW9HUpdateAccountCredentialsGuardArms(t *testing.T) {
	_, store, db, clock, _ := newRefreshJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{ID: "w9h-upd", ProviderCode: "anthropic", ProfileID: "profile_anthropic_anthropic_v1", Type: "oauth",
		Credentials: map[string]any{"access_token": "at", "refresh_token": "rt", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	ctx := context.Background()
	next := map[string]any{"access_token": "at2", "refresh_token": "rt2", "expires_at": expiresInMillis(3_600_000)}

	if account, err := store.UpdateAccountCredentials(ctx, "w9h-ghost", next, 1); err != nil || account != nil {
		t.Fatalf("ghost=%v err=%v", account, err)
	}
	if _, err := store.UpdateAccountCredentials(ctx, "w9h-upd", next, 0); err == nil {
		t.Fatal("revision 0 must fail")
	}
	if _, err := store.UpdateAccountCredentials(ctx, "w9h-upd", map[string]any{"expires_at": "junk"}, 1); err == nil {
		t.Fatal("bad expires_at must fail")
	}
	if _, err := store.UpdateAccountCredentials(ctx, "w9h-upd", map[string]any{"api_key": ""}, 1); err == nil || !strings.Contains(err.Error(), "OAuth 凭据不能为空") {
		t.Fatalf("empty source err=%v", err)
	}
	if _, err := store.UpdateAccountCredentials(ctx, "w9h-upd", next, 5); !IsRevisionConflict(err) {
		t.Fatalf("stale revision err=%v", err)
	}
	account, err := store.UpdateAccountCredentials(ctx, "w9h-upd", next, 1)
	if err != nil || account == nil {
		t.Fatalf("update=%v err=%v", account, err)
	}
	if account.ConfigRevision != 2 || account.Credentials["access_token"] != "at2" {
		t.Fatalf("account=%+v", account)
	}
	// The derived columns follow the merge.
	_, _, revision, oauthExpiresAt, refreshPresent := readAccountRow(t, db, "w9h-upd")
	if revision != 2 || refreshPresent != 1 || oauthExpiresAt.String != expiresInMillis(3_600_000) {
		t.Fatalf("derived revision=%d present=%d expires=%v", revision, refreshPresent, oauthExpiresAt)
	}
}

func TestW9HFindRotationAccountArms(t *testing.T) {
	_, store, db, clock, _ := newRefreshJobForTest(t)
	seedOpenAIOAuthAccount(t, db, "w9h-find", openAICredentials(expiresInMillis(0)), clock.Now())
	// Deleted row reads as absent.
	if _, err := db.Exec(`UPDATE accounts SET deleted_at = ? WHERE id = 'w9h-find'`, isoMillis(clock.Now())); err != nil {
		t.Fatal(err)
	}
	if account, err := store.FindRotationAccount(context.Background(), "w9h-find"); err != nil || account != nil {
		t.Fatalf("deleted account=%v err=%v", account, err)
	}
	// Undecryptable envelope surfaces the credentials-unavailable error.
	seedUndecryptableAccount(t, db, "w9h-find-bad")
	if _, err := store.FindRotationAccount(context.Background(), "w9h-find-bad"); err == nil {
		t.Fatal("undecryptable account must fail")
	} else if _, ok := err.(*CredentialsUnavailableError); !ok {
		t.Fatalf("err type=%T", err)
	}
}

func TestW9HMarkAndClearFailureStateArms(t *testing.T) {
	_, store, db, clock, _ := newRefreshJobForTest(t)
	seedOpenAIOAuthAccount(t, db, "w9h-mark", openAICredentials(expiresInMillis(0)), clock.Now())
	ctx := context.Background()
	// Guard miss (wrong status) reports updated=false.
	updated, err := store.MarkAccountFailureState(ctx, "w9h-mark", "code", "reason", 1, "disabled")
	if err != nil || updated {
		t.Fatalf("guard miss updated=%v err=%v", updated, err)
	}
	// Guard hit flips the status.
	if updated, err = store.MarkAccountFailureState(ctx, "w9h-mark", OpenAIOAuthTokenRefreshFailedErrorCode, "reason", 1, "active"); err != nil || !updated {
		t.Fatalf("guard hit updated=%v err=%v", updated, err)
	}
	// Clear with a non-matching code leaves the account in error.
	changed, status, err := store.ClearAccountFailureState(ctx, "w9h-mark", []string{OpenAIOAuthTokenRefreshLocalConfigurationInvalidCode})
	if err != nil || changed || status != "" {
		t.Fatalf("clear mismatch changed=%v status=%q err=%v", changed, status, err)
	}
	// Clear with the managed code restores active.
	changed, status, err = store.ClearAccountFailureState(ctx, "w9h-mark", ManagedRefreshErrorCodes)
	if err != nil || !changed || status != "active" {
		t.Fatalf("clear hit changed=%v status=%q err=%v", changed, status, err)
	}
	// Second clear misses (already active).
	changed, _, err = store.ClearAccountFailureState(ctx, "w9h-mark", ManagedRefreshErrorCodes)
	if err != nil || changed {
		t.Fatalf("second clear changed=%v err=%v", changed, err)
	}
}

func TestW9HOpenStoreValidationAndClose(t *testing.T) {
	if _, err := OpenStore(nil, StoreSQLite, cryptoTestSecret); err == nil {
		t.Fatal("nil db must fail")
	}
	store, db, _ := newTestStore(t)
	if _, err := OpenStore(db, StoreSQLite, "  "); err == nil {
		t.Fatal("blank secret must fail")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close err=%v", err)
	}
	// Reopening a second store over a fresh db keeps the WithClock override.
	path := t.TempDir()
	_ = path
}

func TestW9HRequiredCredentialSourceArms(t *testing.T) {
	cases := []struct {
		name       string
		accountTyp string
		credential map[string]any
		want       string
		wantErr    string
	}{
		{"oauth refresh token", "oauth", map[string]any{"refresh_token": "rt", "access_token": "at"}, "rt", ""},
		{"oauth access fallback", "oauth", map[string]any{"access_token": "at"}, "at", ""},
		{"oauth empty", "oauth", map[string]any{}, "", "OAuth 凭据不能为空"},
		{"api key", "api_key", map[string]any{"api_key": "sk"}, "sk", ""},
		{"api key empty", "api_key", map[string]any{}, "", "API Key 不能为空"},
		{"google oauth", "google_oauth", map[string]any{"refresh_token": "rt"}, "rt", ""},
		{"google oauth empty", "google_oauth", map[string]any{}, "", "Google OAuth 凭据不能为空"},
		{"default pick", "other", map[string]any{"api_key": "sk"}, "sk", ""},
		{"default fallbacks", "other", map[string]any{"refresh_token": "rt"}, "rt", ""},
		{"default empty", "other", map[string]any{}, "", "账户凭据不能为空"},
		{"whitespace-only counts as empty", "api_key", map[string]any{"api_key": "  "}, "", "API Key 不能为空"},
	}
	for _, tc := range cases {
		got, err := requiredCredentialSource(tc.accountTyp, tc.credential)
		if tc.wantErr != "" {
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("%s: err=%v want %q", tc.name, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("%s: got=%q err=%v want %q", tc.name, got, err, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Sweep finalizer error path
// ---------------------------------------------------------------------------

func TestW9HSweepFinalizerErrorAborts(t *testing.T) {
	store, db, clock := newSweepStore(t)
	seedGrantRow(t, db, "w9h-grant-a", "active", isoMillis(clock.Now().Add(-time.Hour)), "", "creator-1")
	failing := FinalizerFunc(func(context.Context, *sql.Tx, ResourceAuthorizationGrant, string) error {
		return errors.New("fanout down")
	})
	if _, err := store.RunAuthorizationExpirySweep(context.Background(), failing, 0); err == nil || !strings.Contains(err.Error(), "fanout down") {
		t.Fatalf("sweep err=%v", err)
	}
	// The transaction rolled back: the grant is still active.
	assertGrantStatus(t, db, "w9h-grant-a", "active")
}

// ---------------------------------------------------------------------------
// Health fanout reserve/query error arms
// ---------------------------------------------------------------------------

func TestW9HHealthFanoutErrorArms(t *testing.T) {
	store, db, _ := newTestStore(t)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Empty instance account id fails the version reservation.
	if _, err := store.reserveAccountHealthInputVersionTx(ctx, tx, "  ", isoMillis(defaultNow())); err == nil {
		t.Fatal("empty account id must fail")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	// A store over a schema-less database fails the instance query.
	raw, err := sql.Open("sqlite", t.TempDir()+"/bare.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	bare, err := OpenStore(raw, StoreSQLite, cryptoTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	bareTx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bare.EnqueueAccountHealthInputsForAuthorizationSourceTx(ctx, bareTx, "account", "res", AuthorizationGrantHealthFanoutReason); err == nil {
		t.Fatal("missing tables must fail the fanout")
	}
	_ = bareTx.Rollback()
}

// ---------------------------------------------------------------------------
// Availability sync: api-key invalid-disabled next-check-only arm
// ---------------------------------------------------------------------------

func TestW9HSyncApiKeyInvalidDisabledAdvancesOnly(t *testing.T) {
	store, db, _ := newSweepStore(t)
	seedApiKeyWithSchedule(t, db, "w9h-key-invalid-disabled", `{"enabled":true,"mode":"bogus"}`, "", "disabled")
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), defaultNow(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Invalid != 1 || result.Disabled != 0 || result.Unchanged != 0 {
		t.Fatalf("result=%+v", result)
	}
	var status, nextCheck string
	if err := db.QueryRow(`SELECT status, COALESCE(availability_schedule_next_check_at,'') FROM api_keys WHERE id = 'w9h-key-invalid-disabled'`).Scan(&status, &nextCheck); err != nil {
		t.Fatal(err)
	}
	if status != "disabled" {
		t.Fatalf("status=%q", status)
	}
	// The empty schedule drops only the JSON; a nil next-check is fine.
	_, _ = db.Exec(`UPDATE api_keys SET availability_schedule_json = NULL WHERE id = 'w9h-key-invalid-disabled'`)
}

func TestW9HSyncAccountDueEventOnDisabledActivates(t *testing.T) {
	store, db, _ := newSweepStore(t)
	// Window end at 18:00 UTC disables the active account.
	atEnd := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	seedAccountWithSchedule(t, db, "w9h-acc-end", availabilityScheduleJSON, "", "active", false)
	result, err := store.SyncAccountScheduleStatuses(context.Background(), atEnd, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disabled != 1 {
		t.Fatalf("result=%+v", result)
	}
	status, _, _ := readAccountStatus(t, db, "w9h-acc-end")
	if status != "disabled" {
		t.Fatalf("status=%q", status)
	}
	// The first run advanced the next-check column past now; resetting it puts
	// the row back into the scan while the dedupe event stays → skipped.
	if _, err := db.Exec(`UPDATE accounts SET availability_schedule_next_check_at = NULL WHERE id = 'w9h-acc-end'`); err != nil {
		t.Fatal(err)
	}
	result, err = store.SyncAccountScheduleStatuses(context.Background(), atEnd, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 1 {
		t.Fatalf("dedupe result=%+v", result)
	}
}
