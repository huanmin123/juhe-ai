package proberepo

// w7c mutation and helper arms: precheck skip-reason ordering, key mutation
// fences and insert paths, defer validation, and the passive scheduling math.

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
)

func TestW7CMarkPrecheckSkipReasonOrdering(t *testing.T) {
	h := openTestDB(t)
	h.seedPoolAccount(t, "acc-1")
	ctx := context.Background()

	// Blank fence / unparseable fence / revision < 1 all fail validation
	// before any database access.
	for name, input := range map[string]accountquality.PrecheckMutationInput{
		"blank fence":     {AccountID: "acc-1", Reason: "r"},
		"bad fence":       {AccountID: "acc-1", Reason: "r", PrecheckStartedAt: "nope", ExpectedDispatchRevision: 1},
		"bad revision":    {AccountID: "acc-1", Reason: "r", PrecheckStartedAt: nowMillisText()},
		"missing account": {AccountID: "w7c-none", Reason: "r", PrecheckStartedAt: nowMillisText(), ExpectedDispatchRevision: 1, ExpectedStatus: "active"},
	} {
		result, err := h.store.MarkPrecheckTemporaryUnavailable(ctx, input)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if result.Updated || result.SkippedReason != "invalid_precheck_fence" && result.SkippedReason != "account_missing" {
			t.Fatalf("%s: %+v", name, result)
		}
	}

	// hard_unavailable dominates stale fences.
	h.exec(t, `UPDATE accounts SET status='disabled' WHERE id='acc-1'`)
	result, err := h.store.MarkPrecheckTemporaryUnavailable(ctx, accountquality.PrecheckMutationInput{
		AccountID: "acc-1", Reason: "r", PrecheckStartedAt: nowMillisText(),
		ExpectedDispatchRevision: 99, ExpectedStatus: "disabled",
	})
	if err != nil || result.SkippedReason != "hard_unavailable" {
		t.Fatalf("disabled: %+v %v", result, err)
	}
	h.exec(t, `UPDATE accounts SET status='error' WHERE id='acc-1'`)
	result, _ = h.store.MarkPrecheckTemporaryUnavailable(ctx, accountquality.PrecheckMutationInput{
		AccountID: "acc-1", Reason: "r", PrecheckStartedAt: nowMillisText(),
		ExpectedDispatchRevision: 99, ExpectedStatus: "error",
	})
	if result.SkippedReason != "hard_unavailable" {
		t.Fatalf("error: %+v", result)
	}
	h.exec(t, `UPDATE accounts SET status='active' WHERE id='acc-1'`)

	// Garbage runtime timestamps surface invalid_runtime_state.
	h.exec(t, `UPDATE accounts SET last_health_success_at='garbage' WHERE id='acc-1'`)
	result, err = h.store.MarkPrecheckTemporaryUnavailable(ctx, accountquality.PrecheckMutationInput{
		AccountID: "acc-1", Reason: "r", PrecheckStartedAt: nowMillisText(),
		ExpectedDispatchRevision: 3, ExpectedStatus: "active",
	})
	if err != nil || result.SkippedReason != "invalid_runtime_state" {
		t.Fatalf("garbage health timestamp: %+v %v", result, err)
	}
	h.exec(t, `UPDATE accounts SET last_health_success_at=NULL, updated_at=? WHERE id='acc-1'`, plusMillis(60_000))
	result, err = h.store.MarkPrecheckTemporaryUnavailable(ctx, accountquality.PrecheckMutationInput{
		AccountID: "acc-1", Reason: "r", PrecheckStartedAt: nowMillisText(),
		ExpectedDispatchRevision: 3, ExpectedStatus: "active",
	})
	if err != nil || result.SkippedReason != "stale_account_updated" {
		t.Fatalf("newer update: %+v %v", result, err)
	}
	// updated_at newer but identical to last_used_at passes the guard.
	h.exec(t, `UPDATE accounts SET last_used_at = updated_at WHERE id='acc-1'`)
	result, err = h.store.MarkPrecheckTemporaryUnavailable(ctx, accountquality.PrecheckMutationInput{
		AccountID: "acc-1", Reason: "r", PrecheckStartedAt: nowMillisText(),
		ExpectedDispatchRevision: 3, ExpectedStatus: "active",
	})
	if err != nil || !result.Updated {
		t.Fatalf("used-at exemption: %+v %v", result, err)
	}
}

func w7cPoolFence(h *testDB, accountID, fingerprint string) accountquality.KeyMutationExpected {
	return accountquality.KeyMutationExpected{
		Status:         "temporary_unavailable",
		NextProbeAt:    plusMillis(-1000),
		StateUpdatedAt: plusMillis(0),
	}
}

func TestW7CRecordKeySuccessArms(t *testing.T) {
	h := openTestDB(t)
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "temporary_unavailable", plusMillis(-1000))
	ctx := context.Background()

	// error status is manual-restore-only.
	result, err := h.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		Expected: accountquality.KeyMutationExpected{Status: "error"},
	})
	if err != nil || result.Changed {
		t.Fatalf("manual restore: %+v %v", result, err)
	}

	// Not a pool account (single key).
	solo := openTestDB(t)
	solo.seedSchema(t)
	solo.exec(t, `INSERT INTO accounts (id, system_account_id, name, type, status, credentials_encrypted)
	    VALUES ('solo-1', 'sys-1', 's', 'api_key', 'active', ?)`,
		solo.sealCredentials(t, map[string]any{"api_key": "only-key"}))
	result, err = solo.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{
		AccountID: "solo-1", KeyFingerprint: "fp", TrafficSource: "cooldown_retest",
	})
	if err != nil || result.Changed {
		t.Fatalf("solo account: %+v %v", result, err)
	}

	// Invalid fence instants.
	result, err = h.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		Expected: accountquality.KeyMutationExpected{Status: "temporary_unavailable", NextProbeAt: "garbage"},
	})
	if err != nil || result.Changed {
		t.Fatalf("invalid probe fence: %+v %v", result, err)
	}
	result, err = h.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		Expected: accountquality.KeyMutationExpected{StateUpdatedAt: "garbage"},
	})
	if err != nil || result.Changed {
		t.Fatalf("invalid updated fence: %+v %v", result, err)
	}
	// Invalid account config revision fence.
	result, err = h.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		Expected: accountquality.KeyMutationExpected{AccountConfigRevision: -5},
	})
	if err != nil || result.Changed {
		t.Fatalf("invalid revision fence: %+v %v", result, err)
	}

	// Blank fingerprint resolves to no target at all.
	result, err = h.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{
		AccountID: "acc-1", TrafficSource: "cooldown_retest",
	})
	if err != nil || result.Changed {
		t.Fatalf("blank fingerprint: %+v %v", result, err)
	}

	// Unfenced success takes the INSERT path: a brand new fingerprint state
	// is created active.
	newKey := "sk-brand-new"
	h.exec(t, `UPDATE accounts SET credentials_encrypted = ? WHERE id = 'acc-1'`,
		h.sealCredentials(t, map[string]any{"base_url": "https://upstream.example.com/v1", "api_keys": []any{key1, newKey}}))
	result, err = h.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{
		AccountID: "acc-1", KeyFingerprint: h.store.FingerprintAPIKey(newKey), TrafficSource: "cooldown_retest",
		ObservedAt: nowMillisText(),
	})
	if err != nil || !result.Changed {
		t.Fatalf("insert path: %+v %v", result, err)
	}
	var status string
	if err := h.db.QueryRow(`SELECT status FROM account_api_key_runtime_states WHERE key_fingerprint = ?`,
		h.store.FingerprintAPIKey(newKey)).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("inserted status: %s", status)
	}
}

func TestW7CRecordKeyFailureArms(t *testing.T) {
	h := openTestDB(t)
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "disabled", plusMillis(-1000))
	ctx := context.Background()

	// Disabled keys reject failures outright.
	result, err := h.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		Status: "temporary_unavailable", StatusCode: 500, ObservedAt: nowMillisText(),
		Expected: w7cPoolFence(h, "acc-1", fp1),
	})
	if err != nil || result.Changed {
		t.Fatalf("disabled key: %+v %v", result, err)
	}

	// A provided fence without an existing row is stale.
	freshKey := "sk-fresh-failure"
	h.exec(t, `UPDATE accounts SET credentials_encrypted = ? WHERE id = 'acc-1'`,
		h.sealCredentials(t, map[string]any{"base_url": "https://upstream.example.com/v1", "api_keys": []any{key1, freshKey}}))
	result, err = h.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{
		AccountID: "acc-1", KeyFingerprint: h.store.FingerprintAPIKey(freshKey), TrafficSource: "cooldown_retest",
		Status: "temporary_unavailable", StatusCode: 500, ObservedAt: nowMillisText(),
		Expected: accountquality.KeyMutationExpected{Status: "temporary_unavailable", StateUpdatedAt: plusMillis(0)},
	})
	if err != nil || result.Changed {
		t.Fatalf("stale insert fence: %+v %v", result, err)
	}

	// Unfenced failure on a new key inserts the state with the default status
	// normalization and the generic http error code.
	result, err = h.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{
		AccountID: "acc-1", KeyFingerprint: h.store.FingerprintAPIKey(freshKey), TrafficSource: "cooldown_retest",
		StatusCode: 503, ObservedAt: plusMillis(-60_000),
	})
	if err != nil || !result.Changed {
		t.Fatalf("insert failure path: %+v %v", result, err)
	}
	var statusText, errorCodeText string
	var backoffSeconds int
	if err := h.db.QueryRow(`SELECT status, last_error_code, probe_backoff_seconds FROM account_api_key_runtime_states WHERE key_fingerprint = ?`,
		h.store.FingerprintAPIKey(freshKey)).Scan(&statusText, &errorCodeText, &backoffSeconds); err != nil {
		t.Fatal(err)
	}
	if statusText != "temporary_unavailable" || errorCodeText != "http_503" || backoffSeconds != initialProbeBackoffSeconds {
		t.Fatalf("inserted failure state: %s/%s/%d", statusText, errorCodeText, backoffSeconds)
	}

	// explicit_reset leaves the recovery window unset.
	h.exec(t, `UPDATE account_api_key_runtime_states SET recovery_started_at=NULL WHERE key_fingerprint = ?`,
		h.store.FingerprintAPIKey(freshKey))
	testNowCopy := testNow.Add(2 * time.Second)
	_ = testNowCopy
	result, err = h.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{
		AccountID: "acc-1", KeyFingerprint: h.store.FingerprintAPIKey(freshKey), TrafficSource: "cooldown_retest",
		Status: "rate_limited", QuotaRecoveryMode: "explicit_reset", StatusCode: 429,
		ObservedAt: nowMillisText(), BreakQuotaRecoveryWindow: true,
		Expected: accountquality.KeyMutationExpected{Status: "temporary_unavailable", StateUpdatedAt: plusMillis(0)},
	})
	if err != nil || !result.Changed {
		t.Fatalf("explicit reset: %+v %v", result, err)
	}
	var recovery sql.NullString
	if err := h.db.QueryRow(`SELECT recovery_started_at FROM account_api_key_runtime_states WHERE key_fingerprint = ?`,
		h.store.FingerprintAPIKey(freshKey)).Scan(&recovery); err != nil {
		t.Fatal(err)
	}
	if recovery.Valid && recovery.String != "" {
		t.Fatalf("break window must clear recovery: %v", recovery)
	}
}

func TestW7CDeferAndHelperArms(t *testing.T) {
	h := openTestDB(t)
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "temporary_unavailable", plusMillis(-1000))
	ctx := context.Background()

	// Missing expected next_probe_at is rejected before the fence.
	result, err := h.store.DeferKeyProbe(ctx, accountquality.KeyDeferInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
	})
	if err != nil || result.Changed {
		t.Fatalf("missing defer fence: %+v %v", result, err)
	}
	// A successful defer writes a jittered next probe.
	result, err = h.store.DeferKeyProbe(ctx, accountquality.KeyDeferInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		DelaySeconds: 120, ObservedAt: nowMillisText(),
		Expected: accountquality.KeyMutationExpected{
			Status: "temporary_unavailable", NextProbeAt: plusMillis(-1000), StateUpdatedAt: plusMillis(0),
		},
	})
	if err != nil || !result.Changed {
		t.Fatalf("defer: %+v %v", result, err)
	}

	// Passive scheduling math window matrix.
	for _, tc := range []struct {
		interval int64
		want     int64
	}{
		{0, 0}, {1, 0}, {59_999, 29_999}, {60_000, 30_000}, {119_999, 30_000},
		{3_600_000 - 1, 30_000}, {3_600_000, 30 * 60_000}, {86_400_000 - 1, 30 * 60_000},
		{86_400_000, 60 * 60_000}, {7*86_400_000 - 1, 60 * 60_000}, {7 * 86_400_000, 8 * 60 * 60_000},
	} {
		if got := passiveJitterWindowMS(tc.interval); got != tc.want {
			t.Fatalf("passiveJitterWindowMS(%d) = %d, want %d", tc.interval, got, tc.want)
		}
	}
	if delay := passiveScheduleDelayMS(60_000); delay < 1 {
		t.Fatalf("passiveScheduleDelayMS: %d", delay)
	}
	// passiveProbeNotBeforeAt passes through unparseable deadlines and past
	// deadlines verbatim.
	if got := passiveProbeNotBeforeAt("garbage", func() time.Time { return testNow }); got != "garbage" {
		t.Fatalf("bad deadline: %q", got)
	}
	if got := passiveProbeNotBeforeAt(plusMillis(-1000), func() time.Time { return testNow }); got != plusMillis(-1000) {
		t.Fatalf("past deadline: %q", got)
	}

	// Pure helpers.
	if got := quotaModeFromErrorCode(accountquality.QuotaRecoveryExplicitErrorCode); got != "explicit_reset" {
		t.Fatalf("quota mode explicit: %q", got)
	}
	if got := quotaModeFromErrorCode("http_500"); got != "" {
		t.Fatalf("quota mode default: %q", got)
	}
	if got := normalizeFailureStatus("weird"); got != "temporary_unavailable" || normalizeFailureStatus("error") != "error" {
		t.Fatal("normalizeFailureStatus contract")
	}
	if got := sanitizeRuntimeMessage("  a\n\nb  "); got != "a b" {
		t.Fatalf("sanitize: %q", got)
	}
	long := strings.Repeat("字", 1500)
	if got := sanitizeRuntimeMessage(long); len([]rune(got)) != 1000 {
		t.Fatalf("sanitize truncation: %d", len([]rune(got)))
	}
	if got := sanitizeRuntimeMessage("   "); got != "上游请求失败" {
		t.Fatalf("sanitize blank: %q", got)
	}
	if got := normalizeTraceID("  "); got != nil {
		t.Fatalf("blank trace: %v", got)
	}
	if got := normalizeTraceID(strings.Repeat("t", 300)); len(got.(string)) != 200 {
		t.Fatalf("long trace: %v", got)
	}
	if got := firstNonEmptyStr("", "x", "y"); got != "x" {
		t.Fatalf("firstNonEmptyStr: %q", got)
	}
	if got := normalizeObservedAt("garbage", "2026-09-04T10:00:00.000Z"); got != "2026-09-04T10:00:00.000Z" {
		t.Fatalf("normalizeObservedAt fallback: %q", got)
	}
	if _, err := canonicalInstant("nope"); err == nil {
		t.Fatal("canonicalInstant must fail on garbage")
	}
	if normalizeProbeDeferSeconds(0) != initialProbeBackoffSeconds || normalizeProbeDeferSeconds(1<<20) != maxProbeBackoffSeconds {
		t.Fatal("normalizeProbeDeferSeconds bounds")
	}
	if nextProbeBackoffSeconds(0) != initialProbeBackoffSeconds || nextProbeBackoffSeconds(1<<20) != maxProbeBackoffSeconds {
		t.Fatal("nextProbeBackoffSeconds bounds")
	}
	if nowText("x") != "x" {
		t.Fatal("nowText passthrough")
	}
	// Provider pool isolation support matrix.
	for _, provider := range []string{"openai", "GPT", "openai-compatible", "deepseek", "glm", "gemini", "hybrid", "anthropic"} {
		if !isAccountAPIKeyPoolProviderSupported(provider, "", "") {
			t.Fatalf("provider %s must be supported", provider)
		}
	}
	if !isAccountAPIKeyPoolProviderSupported("unknown", " Anthropic ", "") {
		t.Fatal("anthropic protocol must be supported")
	}
	if isAccountAPIKeyPoolProviderSupported("unknown", "openai", "") {
		t.Fatal("unknown provider/protocol must not be supported")
	}
	if got := IsolationEnabledForTest(h.store, "openai", "openai", "v1", "oauth", nil); got {
		t.Fatal("oauth accounts have no pool isolation")
	}
}

func IsolationEnabledForTest(s *Store, provider, protocol, version, accountType string, credentials map[string]any) bool {
	return s.IsAccountAPIKeyPoolIsolationEnabled(provider, protocol, version, accountType, credentials)
}
