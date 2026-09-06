package accounts

import (
	"net/http"
	"testing"
)

// seedCoolingRetestState plants an in-flight bounded-recovery projection
// (observation start + generation pair) on the account, the way the runtime
// and the jobs side leave it while a cooldown recovery candidate is pending.
func (e *testEnv) seedCoolingRetestState(t *testing.T, accountID, status string) {
	t.Helper()
	e.exec(t, `UPDATE accounts SET status = ?, cooldown_until = '2026-09-01T00:05:00.000Z',
		last_error_code = 'upstream_error', last_error_message = 'boom',
		cooldown_retest_failure_count = 2, cooldown_retest_observation_started_at = '2026-09-01T00:00:00.000Z',
		cooldown_retest_generation = 'cooldown:seed-generation',
		cooldown_retest_last_at = '2026-09-01T00:04:00.000Z', cooldown_retest_last_status_code = 503
		WHERE id = ?`, status, accountID)
}

// TestPatchCooldownGenerationBoundedRecoveryRestart mirrors the archived hotfix
// (account-management-patch.repository.ts boundedRecoveryActivated /
// restartBoundedRecoveryObservation): turning the temporary-unavailable
// continuous probe off while the account sits in temporary_unavailable
// restarts the observation window and must persist a NEW cooldown retest
// generation — the pre-hotfix NULL wipe orphaned the recovery candidate so the
// jobs side rejected it as "cooldown fence 无效" and recovery never resumed.
func TestPatchCooldownGenerationBoundedRecoveryRestart(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("cooling"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	env.seedCoolingRetestState(t, id, "temporary_unavailable")

	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"temporaryUnavailableContinuousProbeEnabled":false}`)
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, patched)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
		AND temporary_unavailable_continuous_probe_enabled = 0
		AND cooldown_retest_observation_started_at IS NOT NULL
		AND cooldown_retest_generation LIKE 'cooldown:%'
		AND cooldown_retest_generation <> 'cooldown:seed-generation'
		AND cooldown_retest_failure_count = 0
		AND cooldown_retest_last_at IS NULL
		AND cooldown_retest_last_status_code IS NULL
		AND cooldown_until IS NOT NULL`, id) != 1 {
		t.Fatal("bounded recovery restart must persist a fresh cooldown retest generation, not NULL")
	}
}

// TestPatchCooldownGenerationRegeneratedPerActivation mirrors the archive's
// newCooldownRetestGeneration call inside boundedRecoveryActivated: every
// probe-off activation mints a fresh generation, while a probe re-enable keeps
// the window untouched.
func TestPatchCooldownGenerationRegeneratedPerActivation(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("recycle"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	env.seedCoolingRetestState(t, id, "temporary_unavailable")

	if code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"temporaryUnavailableContinuousProbeEnabled":false}`); code != http.StatusOK {
		t.Fatalf("patch off: %d %v", code, patched)
	}
	first := env.queryCell(t, `SELECT cooldown_retest_generation FROM accounts WHERE id = ?`, id)

	// Probe re-enable: the archive only flips the flag; the retest window and
	// its generation are preserved.
	if code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":2,"temporaryUnavailableContinuousProbeEnabled":true}`); code != http.StatusOK {
		t.Fatalf("patch on: %d %v", code, patched)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
		AND temporary_unavailable_continuous_probe_enabled = 1
		AND cooldown_retest_generation = ?`, id, first) != 1 {
		t.Fatal("probe re-enable must preserve the bounded recovery generation")
	}

	// Second activation mints a different generation.
	if code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":3,"temporaryUnavailableContinuousProbeEnabled":false}`); code != http.StatusOK {
		t.Fatalf("patch off again: %d %v", code, patched)
	}
	second := env.queryCell(t, `SELECT cooldown_retest_generation FROM accounts WHERE id = ?`, id)
	if second == "" || second == first {
		t.Fatalf("second activation must mint a fresh generation: first=%q second=%q", first, second)
	}
}

// TestPatchCooldownGenerationPreservedOutsideTemporaryUnavailable mirrors the
// archive's guarded mainColumns.set: the probe-off switch only resets the
// retest window when the account is temporary_unavailable; otherwise the
// generation and observation start are left untouched (保留当代际).
func TestPatchCooldownGenerationPreservedOutsideTemporaryUnavailable(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("active-cool"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	env.seedCoolingRetestState(t, id, "active")

	if code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"temporaryUnavailableContinuousProbeEnabled":false}`); code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, patched)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
		AND temporary_unavailable_continuous_probe_enabled = 0
		AND cooldown_retest_observation_started_at = '2026-09-01T00:00:00.000Z'
		AND cooldown_retest_generation = 'cooldown:seed-generation'`, id) != 1 {
		t.Fatal("probe off outside temporary_unavailable must keep the retest generation and observation")
	}
}

// TestPatchClearFailureStateClearsCooldownGeneration mirrors the archived
// clearRetest lifecycle (nextRuntimeState / patchAccountFailureStateInTransaction):
// clearing the failure state resets the observation start AND the generation
// together. A dangling generation with a NULL observation start would make the
// jobs direct input reader reject every candidate row with
// "PG direct input 的 cooldown fence 无效".
func TestPatchClearFailureStateClearsCooldownGeneration(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("clear-me"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	env.seedCoolingRetestState(t, id, "temporary_unavailable")

	if code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"clearFailureState":true}`); code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, patched)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
		AND cooldown_retest_observation_started_at IS NULL
		AND cooldown_retest_generation IS NULL
		AND cooldown_retest_failure_count = 0
		AND cooldown_retest_last_at IS NULL
		AND cooldown_retest_last_status_code IS NULL`, id) != 1 {
		t.Fatal("clearFailureState must clear the observation start and the generation together")
	}
}
