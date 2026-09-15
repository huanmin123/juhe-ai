package proberepo

// w7c second wave: pure credential/scheduling helpers, canceled-context
// error arms, and ListDueForProbe candidate filtering arms.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
)

func TestW7CCredentialHelperMatrix(t *testing.T) {
	if got := mapIndex([]any{"a", "b"}, 1); got != "b" {
		t.Fatalf("mapIndex hit: %v", got)
	}
	if got := mapIndex([]any{"a"}, 5); got != nil {
		t.Fatalf("mapIndex out of range: %v", got)
	}
	if got := mapIndex([]any{"a"}, -1); got != nil {
		t.Fatalf("mapIndex negative: %v", got)
	}

	if got := normalizeAPIKeyWeight(float64(50)); got != 50 {
		t.Fatalf("weight float64: %d", got)
	}
	if got := normalizeAPIKeyWeight(float32(7)); got != 7 {
		t.Fatalf("weight float32: %d", got)
	}
	if got := normalizeAPIKeyWeight(int(9)); got != 9 {
		t.Fatalf("weight int: %d", got)
	}
	if got := normalizeAPIKeyWeight(int64(11)); got != 11 {
		t.Fatalf("weight int64: %d", got)
	}
	if got := normalizeAPIKeyWeight(json.Number("23")); got != 23 {
		t.Fatalf("weight json.Number: %d", got)
	}
	if got := normalizeAPIKeyWeight("50"); got != 1 {
		t.Fatalf("weight string: %d", got)
	}
	if got := normalizeAPIKeyWeight(0.5); got != 1 {
		t.Fatalf("weight below range: %d", got)
	}
	if got := normalizeAPIKeyWeight(101.0); got != 1 {
		t.Fatalf("weight above range: %d", got)
	}
	if _, ok := asFloat(json.Number("nope")); ok {
		t.Fatal("bad json.Number must fail")
	}
	if _, ok := asFloat(true); ok {
		t.Fatal("bool is not a float")
	}

	h := openTestDB(t)
	pool := map[string]any{"api_keys": []any{"a", "b"}}
	if h.store.IsAccountAPIKeyPoolIsolationEnabled("openai", "openai", "v1", "oauth", pool) {
		t.Fatal("oauth accounts are excluded")
	}
	if h.store.IsAccountAPIKeyPoolIsolationEnabled("openai", "openai", "v1", "api_key", map[string]any{"api_key": "only"}) {
		t.Fatal("single-key accounts are excluded")
	}
	if !h.store.IsAccountAPIKeyPoolIsolationEnabled("openai", "openai", "v1", "api_key", pool) {
		t.Fatal("openai pool accounts qualify")
	}
	if h.store.IsAccountAPIKeyPoolIsolationEnabled("unknown", "openai", "v1", "api_key", pool) {
		t.Fatal("unsupported providers are excluded")
	}
}

func TestW7CMutationPureHelpers(t *testing.T) {
	h := openTestDB(t)
	if sql, params := h.store.configFenceSQL(0), h.store.configFenceParams(0); sql != "" || params != nil {
		t.Fatal("config fence below 1 must be empty")
	}
	if !strings.Contains(h.store.configFenceSQL(3), "config_revision = ?") || len(h.store.configFenceParams(3)) != 1 {
		t.Fatal("config fence above 0 must be present")
	}
	if got := quotaModeFromErrorCode(accountquality.QuotaRecoveryGenericErrorCode); got != "generic" {
		t.Fatalf("generic mode: %q", got)
	}
	if got := firstNonEmptyStr("", ""); got != "" {
		t.Fatalf("all empty: %q", got)
	}
	// observed earlier than now keeps the caller's observation timestamp.
	if got := normalizeObservedAt(plusMillis(-5000), nowMillisText()); got != plusMillis(-5000) {
		t.Fatalf("observed kept: %q", got)
	}
	observed := nowMillisText()
	if _, err := canonicalInstant(observed); err != nil {
		t.Fatalf("canonical instant: %v", err)
	}
	if ok, err := isFutureInstant("garbage", 0); ok || err == nil {
		t.Fatalf("isFuture garbage: %t %v", ok, err)
	}

	// quotaRecoveryStartedAt decision matrix.
	existing := &runtimeRow{lastErrorCode: accountquality.QuotaRecoveryGenericErrorCode, status: "rate_limited", recoveryStartedAt: "kept"}
	if got := quotaRecoveryStartedAt("generic", existing, observed, false); got != "kept" {
		t.Fatalf("generic continuation: %v", got)
	}
	existingBlank := &runtimeRow{lastErrorCode: accountquality.QuotaRecoveryGenericErrorCode, status: "rate_limited"}
	if got := quotaRecoveryStartedAt("generic", existingBlank, observed, false); got != observed {
		t.Fatalf("generic restart: %v", got)
	}
	existingOther := &runtimeRow{lastErrorCode: "http_500", status: "temporary_unavailable", recoveryStartedAt: "kept"}
	if got := quotaRecoveryStartedAt("generic", existingOther, observed, false); got != observed {
		t.Fatalf("generic restart on other mode: %v", got)
	}
	if got := quotaRecoveryStartedAt("", existingOther, observed, false); got != "kept" {
		t.Fatalf("default keeps window: %v", got)
	}
	if got := quotaRecoveryStartedAt("", nil, observed, false); got != observed {
		t.Fatalf("default without row: %v", got)
	}
	if got := quotaRecoveryStartedAt("explicit_reset", existing, observed, false); got != nil {
		t.Fatalf("explicit reset clears: %v", got)
	}
	if got := quotaRecoveryStartedAt("generic", existing, observed, true); got != nil {
		t.Fatalf("break window clears: %v", got)
	}
}

func TestW7CCanceledContextMutationArms(t *testing.T) {
	h := openTestDB(t)
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "temporary_unavailable", plusMillis(-1000))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := h.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{AccountID: "acc-1", KeyFingerprint: fp1}); err == nil {
		t.Fatal("canceled success must fail")
	}
	if _, err := h.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{AccountID: "acc-1", KeyFingerprint: fp1, StatusCode: 500}); err == nil {
		t.Fatal("canceled failure must fail")
	}
	if _, err := h.store.DeferKeyProbe(ctx, accountquality.KeyDeferInput{AccountID: "acc-1", KeyFingerprint: fp1, Expected: accountquality.KeyMutationExpected{NextProbeAt: plusMillis(-1000)}}); err == nil {
		t.Fatal("canceled defer must fail")
	}
	if _, err := h.store.MarkPrecheckTemporaryUnavailable(ctx, accountquality.PrecheckMutationInput{
		AccountID: "acc-1", Reason: "r", PrecheckStartedAt: nowMillisText(), ExpectedDispatchRevision: 3, ExpectedStatus: "active",
	}); err == nil {
		t.Fatal("canceled precheck must fail")
	}
	if _, err := h.store.ListDueForProbe(ctx, 1); err == nil {
		t.Fatal("canceled list due must fail")
	}
	if _, err := h.store.loadHybridProbeMappings(ctx, "acc-1", "openai"); err == nil {
		t.Fatal("canceled hybrid mappings must fail")
	}
	if _, err := h.store.LoadAccountMetadataByIds(ctx, []string{"acc-1"}); err == nil {
		t.Fatal("canceled metadata must fail")
	}
	if _, err := h.store.LoadAccountForGroupFull(ctx, "acc-1"); err == nil {
		t.Fatal("canceled full reload must fail")
	}
	if _, err := h.store.LoadProbeView(ctx, accountquality.ProbeRequest{AccountID: "acc-1"}); err == nil {
		t.Fatal("canceled probe view must fail")
	}
	if err := h.store.EnsureSchema(ctx); err == nil {
		t.Fatal("canceled EnsureSchema must fail")
	}
}

func TestW7CListDueForProbeFiltersBrokenCandidates(t *testing.T) {
	h := openTestDB(t)
	h.seedSchema(t)
	credentials := h.sealCredentials(t, map[string]any{"base_url": "https://x", "api_keys": []any{"a", "b"}})
	single := h.sealCredentials(t, map[string]any{"api_key": "solo"})
	h.exec(t, `INSERT INTO accounts (id, system_account_id, name, type, status, credentials_encrypted, config_revision)
	    VALUES
	      ('bad-cred', 'sys-1', 'n', 'api_key', 'active', 'garbage', 1),
	      ('solo-acct', 'sys-1', 'n', 'api_key', 'active', ?, 1),
	      ('rotated', 'sys-1', 'n', 'api_key', 'active', ?, 1),
	      ('rev-zero', 'sys-1', 'n', 'api_key', 'active', ?, 0)`, single, credentials, credentials)
	h.exec(t, `INSERT INTO account_api_key_runtime_states (id, system_account_id, account_id, key_fingerprint, key_index, status, next_probe_at, updated_at)
	    VALUES
	      ('s-bad', 'sys-1', 'bad-cred', 'fp-bad', 0, 'temporary_unavailable', ?, ?),
	      ('s-solo', 'sys-1', 'solo-acct', 'fp-solo', 0, 'temporary_unavailable', ?, ?),
	      ('s-rot', 'sys-1', 'rotated', 'fp-rot', 0, 'temporary_unavailable', ?, ?),
	      ('s-rev', 'sys-1', 'rev-zero', 'fp-rev', 0, 'temporary_unavailable', ?, ?)`,
		plusMillis(-1000), nowMillisText(), plusMillis(-1000), nowMillisText(),
		plusMillis(-1000), nowMillisText(), plusMillis(-1000), nowMillisText())

	candidates, err := h.store.ListDueForProbe(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("all broken candidates must be filtered: %+v", candidates)
	}
}

func TestW7CLoadProbeViewMissingArms(t *testing.T) {
	h := openTestDB(t)
	key1, _ := h.seedPoolAccount(t, "acc-1")
	_ = key1

	// Missing account view and missing group candidate both yield nil.
	if view, err := h.store.LoadProbeView(context.Background(), accountquality.ProbeRequest{AccountID: "w7c-missing"}); err != nil || view != nil {
		t.Fatalf("missing account: %v %v", view, err)
	}
	if view, err := h.store.LoadProbeView(context.Background(), accountquality.ProbeRequest{
		AccountID: "acc-1", GroupID: "w7c-none", SystemAccountID: "sys-1",
	}); err != nil || view != nil {
		t.Fatalf("missing candidate: %v %v", view, err)
	}
	// loadSupportedModels skips blank and duplicate rows.
	h.exec(t, `INSERT INTO account_supported_models (account_id, model) VALUES ('acc-1', ''), ('acc-1', 'gpt-test')`)
	account, err := h.store.LoadAccountForTest(context.Background(), "acc-1")
	if err != nil || account == nil {
		t.Fatalf("account: %v %v", account, err)
	}
	if len(account.SupportedModels) != 1 || account.SupportedModels[0] != "gpt-test" {
		t.Fatalf("models: %v", account.SupportedModels)
	}
	// loadAPIKeyRuntimeStatuses skips blank fingerprints.
	h.exec(t, `INSERT INTO account_api_key_runtime_states (id, system_account_id, account_id, key_fingerprint, key_index, status, updated_at)
	    VALUES ('blank-fp', 'sys-1', 'acc-1', '', 9, 'active', ?)`, nowMillisText())
	// blank fingerprints are invisible to the runtime map; the account stays
	// available because one active pool key remains.
	if !account.EffectiveAvailable {
		t.Fatalf("availability: %+v", account.EffectiveAvailabilityStatus)
	}
	if _, ok := account.APIKeyRuntime[""]; ok {
		t.Fatal("blank fingerprints must be skipped")
	}
	// Unfenced success against a state whose last_attempt_at lies in the
	// future changes nothing (plain false, no stale reason).
	h.seedRuntimeState(t, "acc-1", h.store.FingerprintAPIKey("a"), 0, "temporary_unavailable", plusMillis(600_000))
	h.exec(t, `UPDATE account_api_key_runtime_states SET last_attempt_at = ? WHERE key_fingerprint = ?`,
		plusMillis(300_000), h.store.FingerprintAPIKey("a"))
	result, err := h.store.RecordKeySuccess(context.Background(), accountquality.KeySuccessInput{
		AccountID: "acc-1", KeyFingerprint: h.store.FingerprintAPIKey("a"), TrafficSource: "cooldown_retest",
		ObservedAt: nowMillisText(),
	})
	if err != nil || result.Changed {
		t.Fatalf("future attempt fence: %+v %v", result, err)
	}
}
