package accounts

// BUG-0162 revalidate slice regression: the POST
// /{id}/api-key-runtime/revalidate route contract (Node
// account-detail.routes.ts:156-233) and the import-source base_url SSRF
// negative matrix (safeSourceBaseURL → assertSafeUpstreamBaseURL, mirroring
// the Node safeBaseUrl adapter hook).

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// fakeRevalidateEffects records the revalidate handover and satisfies the
// whole RuntimeResetEffects port.
type fakeRevalidateEffects struct {
	mu        sync.Mutex
	accountID string
	revision  int64
	result    AccountAPIKeyRuntimeRevalidation
	err       error
}

func (f *fakeRevalidateEffects) RevalidateAccountAPIKeyRuntimePool(_ context.Context, accountID string, revision int64) (AccountAPIKeyRuntimeRevalidation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accountID = accountID
	f.revision = revision
	if f.err != nil {
		return AccountAPIKeyRuntimeRevalidation{}, f.err
	}
	return f.result, nil
}

func (f *fakeRevalidateEffects) calls() (string, int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accountID, f.revision
}

func (f *fakeRevalidateEffects) ClearAccountRuntimeAvailability(context.Context, RuntimeAvailabilityClearInput) (RuntimeAvailabilityClearResult, error) {
	return RuntimeAvailabilityClearResult{}, nil
}

func (f *fakeRevalidateEffects) ClearNormalRouteLatencyDegradation(context.Context, string, string) (int64, error) {
	return 0, nil
}

func (f *fakeRevalidateEffects) LoadAPIKeyTransientStates(context.Context, string, []string) ([]AccountAPIKeyTransientSelectionState, error) {
	return nil, nil
}

func (f *fakeRevalidateEffects) ClearAPIKeyFailureGuard(string, string, *int64) bool { return false }

func (f *fakeRevalidateEffects) ClearAPIKeyTransientFailure(context.Context, string, string, *int64) (bool, error) {
	return false, nil
}

func (f *fakeRevalidateEffects) DispatchAccountHealthCheck(string, string) {}

func (f *fakeRevalidateEffects) AuthorizationQuotaExceeded(context.Context, AuthorizationQuotaCheckInput) (bool, error) {
	return false, nil
}

func (f *fakeRevalidateEffects) APIKeyPoolAllUnavailable(context.Context, string) (bool, error) {
	return false, nil
}

func TestBug0162RevalidateRouteContract(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	effects := &fakeRevalidateEffects{}
	env.store.SetRuntimeResetEffects(effects)
	env.seedM11Account(t, "acc-rev-1", adminID, "pool-owner", "api_key", "active", Credentials{
		"api_keys": []any{"sk-a", "sk-b"}, "base_url": "https://api.openai.com/v1",
	})

	// Valid revision: 200 with the { id, configRevision, changed } projection,
	// the port receives the route-fenced revision and the operation log lands.
	effects.result = AccountAPIKeyRuntimeRevalidation{Eligible: true, Changed: 2}
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-rev-1/api-key-runtime/revalidate",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("revalidate: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["id"] != "acc-rev-1" || data["configRevision"] != float64(1) || data["changed"] != float64(2) {
		t.Fatalf("revalidate payload: %v", data)
	}
	if accountID, revision := effects.calls(); accountID != "acc-rev-1" || revision != 1 {
		t.Fatalf("port calls: %s %d", accountID, revision)
	}
	seen := false
	for _, entry := range env.sink.entries {
		if entry.OperationKey == "accounts.api_key_runtime_revalidate" {
			seen = true
			if entry.Action != "api_key_runtime_revalidate" || entry.ResourceID != "acc-rev-1" ||
				entry.OperationScopeSystemAccountID != adminID || entry.Summary != "重新验证账户 API Key 池：acc-rev-1" {
				t.Fatalf("operation log entry: %+v", entry)
			}
		}
	}
	if !seen {
		t.Fatalf("operation log actions: %v", env.sink.actions())
	}

	// Revision conflict: 409 with the refresh copy before the port is called.
	effects.result = AccountAPIKeyRuntimeRevalidation{Eligible: true, Changed: 2}
	code, conflict := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-rev-1/api-key-runtime/revalidate",
		`{"expectedConfigRevision":2}`)
	if code != http.StatusConflict || conflict["message"] != RevisionConflictMessage {
		t.Fatalf("revision conflict: %d %v", code, conflict)
	}
	if _, revision := effects.calls(); revision != 1 {
		t.Fatalf("port must not observe the stale request: %d", revision)
	}

	// Ineligible outcome: 409 with the Node code + reason + copy. A separate
	// account keeps the assertion clear of the mutation guard's 60s success
	// dedup entry left by the first request (same fingerprint would collide).
	env.seedM11Account(t, "acc-rev-1b", adminID, "pool-owner-b", "api_key", "active", Credentials{
		"api_keys": []any{"sk-a", "sk-b"}, "base_url": "https://api.openai.com/v1",
	})
	effects.result = AccountAPIKeyRuntimeRevalidation{Reason: "no_revalidatable_key"}
	code, ineligible := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-rev-1b/api-key-runtime/revalidate",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusConflict {
		t.Fatalf("ineligible: %d %v", code, ineligible)
	}
	if ineligible["code"] != "ACCOUNT_API_KEY_RUNTIME_REVALIDATE_NOT_EXECUTABLE" ||
		ineligible["reason"] != "no_revalidatable_key" ||
		ineligible["message"] != "当前账户没有可重新验证的不可用 Key" {
		t.Fatalf("ineligible payload: %v", ineligible)
	}

	// Body schema: unknown field / missing / zero / non-integer revisions →
	// 400 重新验证 API Key 池参数无效. A dedicated account; every case
	// fingerprints a distinct expectedConfigRevision, so the guard never
	// collapses two cases into the dedup key.
	env.seedM11Account(t, "acc-rev-1c", adminID, "pool-owner-c", "api_key", "active", Credentials{
		"api_keys": []any{"sk-a", "sk-b"}, "base_url": "https://api.openai.com/v1",
	})
	for name, body := range map[string]string{
		"unknown field": `{"expectedConfigRevision":1,"bogus":1}`,
		"missing":       `{}`,
		"zero":          `{"expectedConfigRevision":0}`,
		"string":        `{"expectedConfigRevision":"1"}`,
	} {
		code, bad := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-rev-1c/api-key-runtime/revalidate", body)
		if code != http.StatusBadRequest || bad["message"] != "重新验证 API Key 池参数无效" {
			t.Fatalf("%s: %d %v", name, code, bad)
		}
	}
}

func TestBug0162RevalidateRouteAccessGates(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	effects := &fakeRevalidateEffects{result: AccountAPIKeyRuntimeRevalidation{Eligible: true, Changed: 1}}
	env.store.SetRuntimeResetEffects(effects)
	env.seedM11Account(t, "acc-rev-2", adminID, "admin-pool", "api_key", "active", Credentials{
		"api_keys": []any{"sk-a", "sk-b"}, "base_url": "https://api.openai.com/v1",
	})
	env.seedM11Account(t, "acc-rev-3", adminID, "authorized-instance", "api_key", "active", Credentials{
		"api_keys": []any{"sk-a", "sk-b"}, "base_url": "https://api.openai.com/v1",
	})
	env.exec(t, `UPDATE accounts SET authorization_instance_source_account_id = 'acc-rev-2' WHERE id = 'acc-rev-3'`)

	// Missing account → 404.
	code, missing := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-missing/api-key-runtime/revalidate",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusNotFound || missing["message"] != "账户不存在" {
		t.Fatalf("missing account: %d %v", code, missing)
	}

	// Authorized instance → 403 with the Node copy, port untouched.
	code, forbidden := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-rev-3/api-key-runtime/revalidate",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusForbidden || forbidden["message"] != "授权实例不能重新验证来源账户 API Key 池" {
		t.Fatalf("authorized instance: %d %v", code, forbidden)
	}

	aliceID := env.login(t, "alice", "alice-pass", "user")
	env.seedM11Account(t, "acc-rev-4", aliceID, "alice-pool", "api_key", "active", Credentials{
		"api_keys": []any{"sk-a", "sk-b"}, "base_url": "https://api.openai.com/v1",
	})

	// Anonymous and user-role callers cannot reach the admin surface.
	env.do(t, http.MethodPost, "/__aisys__/api/auth/logout", "")
	code, anonymous := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-rev-2/api-key-runtime/revalidate",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous admin revalidate: %d %v", code, anonymous)
	}
	env.login(t, "alice", "alice-pass", "user")
	code, denied := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-rev-2/api-key-runtime/revalidate",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusForbidden {
		t.Fatalf("user admin revalidate: %d %v", code, denied)
	}

	// Self surface: the owner revalidates through /my-accounts; a foreign
	// account stays 404.
	code, mine := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-rev-4/api-key-runtime/revalidate",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusOK || dataMap(t, mine)["id"] != "acc-rev-4" {
		t.Fatalf("self surface: %d %v", code, mine)
	}
	if accountID, _ := effects.calls(); accountID != "acc-rev-4" {
		t.Fatalf("port calls after self surface: %s", accountID)
	}
	env.login(t, "bob", "bob-pass", "user")
	code, foreign := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-rev-4/api-key-runtime/revalidate",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusNotFound {
		t.Fatalf("foreign self revalidate: %d %v", code, foreign)
	}
}

// TestBug0162ImportSourceBaseURLSSRFMatrix pins the safeSourceBaseURL →
// assertSafeUpstreamBaseURL handover: loopback, private, link-local, reserved
// and wrong-scheme source base URLs are refused (the Node safeBaseUrl adapter
// hook counts one ignored field and the caller skips the record); the public
// upstream passes.
func TestBug0162ImportSourceBaseURLSSRFMatrix(t *testing.T) {
	unsafe := []string{
		"http://127.0.0.1:9/v1",
		"http://10.1.2.3/v1",
		"http://172.16.0.9/v1",
		"http://172.31.255.255/v1",
		"http://192.168.1.1/v1",
		"http://169.254.169.254/latest/meta-data",
		"http://0.0.0.0/v1",
		"http://[::1]/v1",
		"http://[::ffff:127.0.0.1]/v1",
		"ftp://api.openai.com/v1",
	}
	for _, value := range unsafe {
		state := &adapterState{source: emptySourceSummary(importSourceNewAPI)}
		if safeSourceBaseURL(value, state) {
			t.Fatalf("unsafe source base url accepted: %s", value)
		}
		if state.source.IgnoredFields != 1 {
			t.Fatalf("%s: ignoredFields = %d, want 1", value, state.source.IgnoredFields)
		}
	}
	state := &adapterState{source: emptySourceSummary(importSourceNewAPI)}
	if !safeSourceBaseURL(defaultOpenAIBaseURL, state) {
		t.Fatal("public upstream base url must pass")
	}
	if state.source.IgnoredFields != 0 {
		t.Fatalf("ignoredFields = %d, want 0", state.source.IgnoredFields)
	}

	// Adapter-level wiring: a NewAPI channel record with a loopback base_url
	// is skipped with the upstream policy message instead of being accepted.
	state = &adapterState{source: emptySourceSummary(importSourceNewAPI)}
	adaptChannelSource([]any{map[string]any{
		"type":     "openai",
		"key":      "sk-channel-1",
		"base_url": "http://127.0.0.1:9/v1",
		"name":     "loopback",
	}}, importSourceNewAPI, state)
	if state.source.Accepted != 0 || state.source.Skipped != 1 {
		t.Fatalf("adapter outcome: accepted=%d skipped=%d", state.source.Accepted, state.source.Skipped)
	}
	joined := strings.Join(state.source.Messages, "\n")
	if !strings.Contains(joined, "不符合上游地址策略") {
		t.Fatalf("skip messages: %v", state.source.Messages)
	}
}
