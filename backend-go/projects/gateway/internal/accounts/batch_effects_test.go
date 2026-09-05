package accounts

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sync"
	"testing"
)

// fakeBatchInvalidator records the post-commit batch invalidation channels
// and can fail them independently (best-effort contract of batch_effects.go).
type fakeBatchInvalidator struct {
	mu             sync.Mutex
	lookups        []string
	runtimeReasons []string
	lookupErr      error
	runtimeErr     error
}

func (f *fakeBatchInvalidator) InvalidateAccountLookup(accountID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups = append(f.lookups, accountID)
	return f.lookupErr
}

func (f *fakeBatchInvalidator) InvalidateGatewayRuntime(reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runtimeReasons = append(f.runtimeReasons, reason)
	return f.runtimeErr
}

func (f *fakeBatchInvalidator) snapshot() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.lookups...), append([]string{}, f.runtimeReasons...)
}

func (f *fakeBatchInvalidator) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups = nil
	f.runtimeReasons = nil
}

// batchEffectsBody renders a two-target batch-update body.
func batchEffectsBody(idA string, revisionA int64, idB string, revisionB int64, updates string) string {
	return `{"targets":[
			{"accountId":"` + idA + `","configRevision":` + itoa64(revisionA) + `},
			{"accountId":"` + idB + `","configRevision":` + itoa64(revisionB) + `}],
		"updates":` + updates + `}`
}

// familyTransitionID mirrors Node familyDispatchTransitionId
// (account-circuit-control-plane.repository.ts:1282-1284).
func familyTransitionID(transitionID, accountID string) string {
	sum := sha256.Sum256([]byte(transitionID + "\x00" + accountID))
	return "dispatch-family:" + hex.EncodeToString(sum[:])
}

func seedBatchAccounts(t *testing.T, env *testEnv) ([]string, string) {
	t.Helper()
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	ids := []string{}
	for _, name := range []string{"alpha", "bravo"} {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload(name))
		if code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", name, code, payload)
		}
		ids = append(ids, dataMap(t, payload)["id"].(string))
	}
	seedImportProxy(t, env, "pp-1", adminID, "批量副作用代理")
	return ids, adminID
}

// TestAccountBatchUpdatePostCommitEffects pins the full committed-batch chain:
// per-account dispatch advance + outbox rows inside the transaction, then the
// best-effort lookup/runtime invalidation and the group stats dirty marker
// (Node account-batch-update.repository.ts:167-241).
func TestAccountBatchUpdatePostCommitEffects(t *testing.T) {
	env := newTestEnv(t)
	ids, adminID := seedBatchAccounts(t, env)
	fake := &fakeBatchInvalidator{}
	env.store.SetCacheInvalidator(fake)

	code, updated := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update",
		batchEffectsBody(ids[0], 1, ids[1], 1,
			`{"proxyProfileId":{"enabled":true,"value":"pp-1"},"concurrencyLimit":{"enabled":true,"value":888}}`))
	if code != http.StatusOK {
		t.Fatalf("proxy batch: %d %v", code, updated)
	}
	batchID := dataMap(t, updated)["batchId"].(string)

	// In-transaction dispatch advance: every changed account bumps its
	// dispatch revision and lands one outbox row with the Node contract.
	for _, id := range ids {
		if revision := env.queryCell(t, `SELECT dispatch_revision FROM accounts WHERE id = ?`, id); revision != "2" {
			t.Fatalf("dispatch_revision after proxy batch for %s: %v", id, revision)
		}
		if env.count(t, `SELECT COUNT(*) FROM account_circuit_outbox WHERE account_id = ?
			AND event_type = 'dispatch_revision_changed' AND projection_key = 'account_circuit_runtime_v1'
			AND dedupe_key = ? AND transition_id = ? AND account_runtime_key = ?
			AND dispatch_revision = 2 AND status = 'pending' AND attempt_count = 0
			AND circuit_scope_key IS NULL AND incident_id IS NULL AND generation IS NULL
			AND ledger_revision IS NULL AND available_at_ms > 0`, id,
			"dispatch:"+batchID+":"+id, batchID+":"+id, id) != 1 {
			t.Fatalf("outbox row contract violated for %s", id)
		}
	}

	// Post-commit invalidation: one lookup per changed account, one runtime
	// invalidation with the Node reason string.
	lookups, reasons := fake.snapshot()
	if len(lookups) != 2 ||
		!(lookups[0] == ids[0] && lookups[1] == ids[1] || lookups[0] == ids[1] && lookups[1] == ids[0]) {
		t.Fatalf("lookup invalidations: %v (want %v)", lookups, ids)
	}
	if len(reasons) != 1 || reasons[0] != "account_batch_updated" {
		t.Fatalf("runtime invalidations: %v", reasons)
	}

	// Stats dirty marking: concurrencyLimit is a group-stats field, so the
	// default group flips dirty with the Node reason.
	if reason := env.queryCell(t, `SELECT reason FROM group_account_stats_dirty WHERE group_id = ?`, "grp-default-"+adminID); reason != "account_batch_updated" {
		t.Fatalf("group stats dirty reason: %v", reason)
	}
}

// TestAccountBatchUpdateDispatchGatedByProxyChange pins the
// dispatchRevisionChanged = proxyChanged gate (Node
// account-batch-edit.service.ts:391): non-proxy changes never advance the
// dispatch revision, a re-run with an unchanged proxy value is a no-op.
func TestAccountBatchUpdateDispatchGatedByProxyChange(t *testing.T) {
	env := newTestEnv(t)
	ids, _ := seedBatchAccounts(t, env)
	fake := &fakeBatchInvalidator{}
	env.store.SetCacheInvalidator(fake)

	// notes-only batch: changed + lookup invalidations, but no dispatch
	// advance, no outbox rows, no runtime invalidation and no stats dirty
	// (notes is neither a gateway nor a group-stats field).
	code, updated := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update",
		batchEffectsBody(ids[0], 1, ids[1], 1, `{"notes":{"enabled":true,"value":"无代理变更"}}`))
	if code != http.StatusOK {
		t.Fatalf("notes batch: %d %v", code, updated)
	}
	for _, id := range ids {
		if revision := env.queryCell(t, `SELECT dispatch_revision FROM accounts WHERE id = ?`, id); revision != "1" {
			t.Fatalf("non-proxy batch must not advance %s: %v", id, revision)
		}
	}
	if env.count(t, `SELECT COUNT(*) FROM account_circuit_outbox`) != 0 {
		t.Fatal("non-proxy batch must not write outbox rows")
	}
	if env.count(t, `SELECT COUNT(*) FROM group_account_stats_dirty`) != 0 {
		t.Fatal("notes-only batch must not mark group stats dirty")
	}
	lookups, reasons := fake.snapshot()
	if len(lookups) != 2 || len(reasons) != 0 {
		t.Fatalf("notes-only invalidations: lookups=%v reasons=%v", lookups, reasons)
	}

	// First proxy batch advances; the identical re-run changes nothing.
	for round := 0; round < 2; round++ {
		fake.reset()
		code, updated := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update",
			batchEffectsBody(ids[0], 2+int64(round), ids[1], 2+int64(round),
				`{"proxyProfileId":{"enabled":true,"value":"pp-1"}}`))
		if code != http.StatusOK {
			t.Fatalf("proxy batch round %d: %d %v", round, code, updated)
		}
		changed := changedFieldSet(t, updated)
		if round == 1 {
			if len(changed) != 0 {
				t.Fatalf("unchanged proxy re-run must be a no-op: %v", dataMap(t, updated)["changedFields"])
			}
			lookups, reasons = fake.snapshot()
			if len(lookups) != 0 || len(reasons) != 0 {
				t.Fatalf("no-op re-run must not invalidate: lookups=%v reasons=%v", lookups, reasons)
			}
		}
	}
	for _, id := range ids {
		if revision := env.queryCell(t, `SELECT dispatch_revision FROM accounts WHERE id = ?`, id); revision != "2" {
			t.Fatalf("dispatch_revision after re-run for %s: %v", id, revision)
		}
	}
	if env.count(t, `SELECT COUNT(*) FROM account_circuit_outbox`) != 2 {
		t.Fatal("re-run must not write additional outbox rows")
	}
}

// TestAccountBatchUpdateAdvancesDispatchFamily pins the family semantics
// (Node advanceAccountCircuitDispatchRevisionFamilyInTransaction): an
// authorization instance advances together with its root under the hashed
// family transition id.
func TestAccountBatchUpdateAdvancesDispatchFamily(t *testing.T) {
	env := newTestEnv(t)
	ids, adminID := seedBatchAccounts(t, env)
	now := "2026-01-01T00:00:00.000Z"
	// Authorization instance of ids[0] (the batch surface itself excludes
	// instances, so only the root is targeted).
	env.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, authorization_instance_source_account_id, created_at, updated_at)
		VALUES ('acc-authz-inst', ?, 'gpt', 'prof-gpt', 'openai', 'v1', '授权实例', 'api_key', 'active',
		'sealed', 'masked', 'gpt-4o-mini', ?, ?, ?)`, adminID, ids[0], now, now)

	code, updated := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update",
		batchEffectsBody(ids[0], 1, ids[1], 1, `{"proxyProfileId":{"enabled":true,"value":"pp-1"}}`))
	if code != http.StatusOK {
		t.Fatalf("family batch: %d %v", code, updated)
	}
	batchID := dataMap(t, updated)["batchId"].(string)

	// Root, sibling root and the instance all advance to revision 2.
	for _, id := range []string{ids[0], ids[1], "acc-authz-inst"} {
		if revision := env.queryCell(t, `SELECT dispatch_revision FROM accounts WHERE id = ?`, id); revision != "2" {
			t.Fatalf("family member %s dispatch_revision: %v", id, revision)
		}
	}
	if env.count(t, `SELECT COUNT(*) FROM account_circuit_outbox WHERE account_id = ? AND transition_id = ?`,
		ids[0], batchID+":"+ids[0]) != 1 {
		t.Fatalf("root outbox row missing: %v", ids[0])
	}
	if env.count(t, `SELECT COUNT(*) FROM account_circuit_outbox WHERE account_id = ? AND transition_id = ?`,
		ids[1], batchID+":"+ids[1]) != 1 {
		t.Fatalf("sibling outbox row missing: %v", ids[1])
	}
	if env.count(t, `SELECT COUNT(*) FROM account_circuit_outbox WHERE account_id = 'acc-authz-inst'
		AND transition_id = ? AND dispatch_revision = 2`, familyTransitionID(batchID+":"+ids[0], "acc-authz-inst")) != 1 {
		t.Fatal("instance family outbox row missing")
	}
}

// TestAccountBatchUpdateInvalidatorFailureKeepsOK pins the best-effort
// contract: a fully failing invalidator must not turn a committed batch into
// an error response (Node warns and returns 200).
func TestAccountBatchUpdateInvalidatorFailureKeepsOK(t *testing.T) {
	env := newTestEnv(t)
	ids, _ := seedBatchAccounts(t, env)
	fake := &fakeBatchInvalidator{lookupErr: errFakeInvalidation, runtimeErr: errFakeInvalidation}
	env.store.SetCacheInvalidator(fake)

	code, updated := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update",
		batchEffectsBody(ids[0], 1, ids[1], 1,
			`{"proxyProfileId":{"enabled":true,"value":"pp-1"},"concurrencyLimit":{"enabled":true,"value":889}}`))
	if code != http.StatusOK {
		t.Fatalf("batch must survive invalidator failure: %d %v", code, updated)
	}
	lookups, reasons := fake.snapshot()
	if len(lookups) != 2 || len(reasons) != 1 {
		t.Fatalf("failing invalidator must still be called: lookups=%v reasons=%v", lookups, reasons)
	}
	for _, id := range ids {
		if revision := env.queryCell(t, `SELECT dispatch_revision FROM accounts WHERE id = ?`, id); revision != "2" {
			t.Fatalf("dispatch_revision for %s: %v", id, revision)
		}
		if proxy := env.queryCell(t, `SELECT proxy_profile_id FROM accounts WHERE id = ?`, id); proxy != "pp-1" {
			t.Fatalf("proxy write for %s: %v", id, proxy)
		}
	}
	if env.count(t, `SELECT COUNT(*) FROM account_circuit_outbox`) != 2 {
		t.Fatal("outbox rows must survive invalidator failure")
	}
}

// TestAccountBatchUpdateDispatchRollsBackWithBatch pins the transaction
// boundary: a batch that fails on a later target rolls back the dispatch
// advance and outbox rows of the earlier targets too.
func TestAccountBatchUpdateDispatchRollsBackWithBatch(t *testing.T) {
	env := newTestEnv(t)
	ids, _ := seedBatchAccounts(t, env)

	// Concurrent change on the second target: its request revision goes stale.
	now := "2026-01-01T00:00:00.000Z"
	env.exec(t, `UPDATE accounts SET config_revision = 2, updated_at = ? WHERE id = ?`, now, ids[1])

	code, conflict := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update",
		batchEffectsBody(ids[0], 1, ids[1], 1, `{"proxyProfileId":{"enabled":true,"value":"pp-1"}}`))
	if code != http.StatusConflict {
		t.Fatalf("stale batch: %d %v", code, conflict)
	}
	for _, id := range ids {
		if revision := env.queryCell(t, `SELECT dispatch_revision FROM accounts WHERE id = ?`, id); revision != "1" {
			t.Fatalf("rollback must restore dispatch_revision for %s: %v", id, revision)
		}
		if proxy := env.queryCell(t, `SELECT proxy_profile_id FROM accounts WHERE id = ?`, id); proxy != "" {
			t.Fatalf("rollback must restore proxy for %s: %v", id, proxy)
		}
	}
	if env.count(t, `SELECT COUNT(*) FROM account_circuit_outbox`) != 0 {
		t.Fatal("rollback must remove outbox rows")
	}
	if env.count(t, `SELECT COUNT(*) FROM group_account_stats_dirty`) != 0 {
		t.Fatal("aborted batch must not mark stats dirty")
	}
}

type fakeInvalidationError struct{}

func (fakeInvalidationError) Error() string { return "fake invalidator failure" }

var errFakeInvalidation = fakeInvalidationError{}
