package oauthmgmt

import (
	"net/http"
	"strings"
	"testing"
)

// TestRotationInvalidationAndDispatchRevision drives the manual refresh route
// (oauth-credential-rotation.repository.ts parity) and asserts the T2 wiring:
//
//  1. post-commit double channel — the rotation invalidates the gateway
//     runtime topic AND the API-key validation topic with the Node reason
//     'oauth_credentials_rotated', plus the per-account lookup flush
//     (repository.ts:223-226);
//  2. in-transaction circuit fence — the upstream connection identity change
//     advances the dispatch revision family and lands one pending
//     dispatch_revision_changed outbox row (repository.ts:202-214).
func TestRotationInvalidationAndDispatchRevision(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL != OpenAIOAuthTokenURL {
			return http.StatusNotFound, `{"error":"wrong_endpoint"}`
		}
		return http.StatusOK, openAITokenPayload("openai-access-rotated-2")
	}

	// Seed one oauth account with a known identity.
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"openai-refresh-rot","clientId":"custom-openai-client","providerProtocolProfileId":"profile_gpt_openai_v1","name":"Rotation Probe"}`)
	if code != http.StatusCreated {
		t.Fatalf("create-from-refresh-token: %d %v", code, created)
	}
	accountID := dataMap(t, created)["id"].(string)

	before := env.dispatchRevision(t, accountID)

	// Manual refresh rotates the access token → identity change.
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL != OpenAIOAuthTokenURL {
			return http.StatusNotFound, `{"error":"wrong_endpoint"}`
		}
		return http.StatusOK, openAITokenPayload("openai-access-rotated-3")
	}
	code, refreshed := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/"+accountID+"/refresh-token",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("refresh-token: %d %v", code, refreshed)
	}

	// 1. post-commit channels.
	lookups, runtime, validation := env.rotation.channels()
	if len(lookups) != 1 || lookups[0] != accountID {
		t.Fatalf("lookup flushes = %v, want [%s]", lookups, accountID)
	}
	if len(runtime) != 1 || runtime[0] != RotationRuntimeInvalidationReason {
		t.Fatalf("runtime reasons = %v, want [%s]", runtime, RotationRuntimeInvalidationReason)
	}
	if len(validation) != 1 || validation[0] != RotationRuntimeInvalidationReason {
		t.Fatalf("validation reasons = %v, want [%s]", validation, RotationRuntimeInvalidationReason)
	}

	// 2. dispatch revision advanced + outbox row pending.
	after := env.dispatchRevision(t, accountID)
	if after != before+1 {
		t.Fatalf("dispatch revision %d → %d, want +1", before, after)
	}
	var eventType, status, transitionID string
	if err := env.db.QueryRow(`SELECT event_type, status, transition_id FROM account_circuit_outbox
		WHERE projection_key = 'account_circuit_runtime_v1' AND dedupe_key LIKE 'dispatch:%' AND account_id = ?`,
		accountID).Scan(&eventType, &status, &transitionID); err != nil {
		t.Fatalf("rotation outbox row: %v", err)
	}
	if eventType != "dispatch_revision_changed" || status != "pending" {
		t.Fatalf("outbox event = %s/%s, want dispatch_revision_changed/pending", eventType, status)
	}
	if !strings.HasPrefix(transitionID, "dispatch_") {
		t.Fatalf("transition id = %s, want newId('dispatch') shape", transitionID)
	}
	if owner := env.count(t, `SELECT COUNT(*) FROM system_accounts WHERE id = ?`, adminID); owner != 1 {
		t.Fatalf("owner rows = %d", owner)
	}
}

// dispatchRevision reads the account's dispatch_revision.
func (e *testEnv) dispatchRevision(t *testing.T, accountID string) int64 {
	t.Helper()
	var revision int64
	if err := e.db.QueryRow(`SELECT dispatch_revision FROM accounts WHERE id = ?`, accountID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	return revision
}
