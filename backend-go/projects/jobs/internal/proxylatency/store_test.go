package proxylatency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteCheckSchemaFailsClosedWhenOwnedObjectIsMissing(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "proxy-latency-schema-check.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`DROP INDEX idx_proxy_latency_outcomes_cursor`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(context.Background()); err == nil || !strings.Contains(err.Error(), "idx_proxy_latency_outcomes_cursor") {
		t.Fatalf("缺少 SQLite schema 对象时必须 fail-closed，实际错误=%v", err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("显式 schema maintenance 应能恢复私有 SQLite schema: %v", err)
	}
	if err := store.CheckSchema(context.Background()); err != nil {
		t.Fatalf("显式 schema maintenance 后 CheckSchema 仍失败: %v", err)
	}
}

func TestSQLiteStoreOwnerProxyLeasesAndOutcomes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy-latency.sqlite3")
	first, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	ctx := context.Background()
	ownerA, acquired, err := first.AcquireOwnerLease(ctx, "owner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("owner A acquire: acquired=%t err=%v", acquired, err)
	}
	if _, acquired, err := second.AcquireOwnerLease(ctx, "owner-b", time.Minute); err != nil || acquired {
		t.Fatalf("owner B contention: acquired=%t err=%v", acquired, err)
	}
	proxyA, acquired, err := first.AcquireProxyLease(ctx, ownerA, "proxy-1", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("proxy acquire: acquired=%t err=%v", acquired, err)
	}
	if _, acquired, err := first.AcquireProxyLease(ctx, ownerA, "proxy-1", time.Minute); err != nil || acquired {
		t.Fatalf("proxy contention: acquired=%t err=%v", acquired, err)
	}

	issued, err := first.IssueInput(ctx, testInputDraft("proxy-1", TriggerPeriodic))
	if err != nil {
		t.Fatal(err)
	}
	outcome := testOutcomeForIssued("outcome-1", issued, ownerA, proxyA)
	inserted, err := first.AppendOutcome(ctx, ownerA, proxyA, outcome)
	if err != nil || !inserted {
		t.Fatalf("first outcome: inserted=%t err=%v", inserted, err)
	}
	inserted, err = first.AppendOutcome(ctx, ownerA, proxyA, outcome)
	if err != nil || inserted {
		t.Fatalf("idempotent replay: inserted=%t err=%v", inserted, err)
	}
	conflict := outcome
	conflict.OutcomeID = "outcome-conflict"
	if _, err := first.AppendOutcome(ctx, ownerA, proxyA, conflict); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("request conflict error=%v", err)
	}
	wrongFenceInput, err := first.IssueInput(ctx, testInputDraft("proxy-1", TriggerPeriodic))
	if err != nil {
		t.Fatal(err)
	}
	wrongFence := testOutcomeForIssued("outcome-wrong-fence", wrongFenceInput, ownerA, proxyA)
	wrongFence.ProxyFenceToken++
	if _, err := first.AppendOutcome(ctx, ownerA, proxyA, wrongFence); err == nil {
		t.Fatal("outcome with mismatched proxy fence must be rejected")
	}

	if err := first.ReleaseProxyLease(ctx, proxyA); err != nil {
		t.Fatal(err)
	}
	if err := first.ReleaseProxyLease(ctx, proxyA); !errors.Is(err, ErrProxyLeaseLost) {
		t.Fatalf("second proxy release must be stale, got %v", err)
	}
	proxyB, acquired, err := first.AcquireProxyLease(ctx, ownerA, "proxy-1", time.Minute)
	if err != nil || !acquired || proxyB.FenceToken <= proxyA.FenceToken {
		t.Fatalf("proxy reacquire=%+v acquired=%t err=%v", proxyB, acquired, err)
	}
	staleProxyInput, err := first.IssueInput(ctx, testInputDraft("proxy-1", TriggerPeriodic))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.AppendOutcome(ctx, ownerA, proxyA, testOutcomeForIssued("outcome-stale", staleProxyInput, ownerA, proxyA)); !errors.Is(err, ErrProxyLeaseLost) {
		t.Fatalf("stale proxy lease error=%v", err)
	}

	if err := first.ReleaseOwnerLease(ctx, ownerA); err != nil {
		t.Fatal(err)
	}
	ownerB, acquired, err := second.AcquireOwnerLease(ctx, "owner-b", time.Minute)
	if err != nil || !acquired || ownerB.FenceToken <= ownerA.FenceToken {
		t.Fatalf("owner B takeover=%+v acquired=%t err=%v", ownerB, acquired, err)
	}
	staleOwnerInput, err := first.IssueInput(ctx, testInputDraft("proxy-1", TriggerPeriodic))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.AppendOutcome(ctx, ownerA, proxyB, testOutcomeForIssued("outcome-owner-stale", staleOwnerInput, ownerA, proxyB)); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("stale owner lease error=%v", err)
	}
}

func TestSQLiteStoreIssuesImmutableInputVersions(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "proxy-latency-inputs.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 21, 5, 0, 0, 0, time.UTC)
	draft := InputDraft{
		ProxyID: "proxy-1", ConfigRevision: "2026-08-21T04:59:00Z", Trigger: TriggerPeriodic,
		IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute), PolicyVersion: proxyLatencyInputPolicyVersion,
		ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 8080,
		Targets: []Target{{Provider: "gpt", ProfileID: "profile-gpt", URL: "https://api.openai.com/v1"}},
	}
	first, err := store.IssueInput(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.IssueInput(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	if first.RequestID == "" || second.RequestID == "" || first.RequestID == second.RequestID || first.InputVersion != 1 || second.InputVersion != 2 {
		t.Fatalf("issued input identity invalid: first=%+v second=%+v", first, second)
	}
	var rowCount int
	if err := store.db.QueryRow(`SELECT count(*) FROM proxy_latency_inputs WHERE proxy_id=?`, "proxy-1").Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 2 {
		t.Fatalf("issued input rows=%d, want 2", rowCount)
	}
}

func TestSQLiteStoreAppendOutcomeRequiresIssuedInputAndMatchesFence(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "proxy-latency-input-fence.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	owner, acquired, err := store.AcquireOwnerLease(ctx, "owner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("owner acquire: acquired=%t err=%v", acquired, err)
	}
	proxy, acquired, err := store.AcquireProxyLease(ctx, owner, "proxy-1", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("proxy acquire: acquired=%t err=%v", acquired, err)
	}
	unissued := testOutcome("unissued", "missing-request", "proxy-1", owner, proxy)
	if _, err := store.AppendOutcome(ctx, owner, proxy, unissued); !errors.Is(err, ErrInputFence) {
		t.Fatalf("unissued input err=%v, want ErrInputFence", err)
	}
	issued, err := store.IssueInput(ctx, testInputDraft("proxy-1", TriggerPeriodic))
	if err != nil {
		t.Fatal(err)
	}
	outcome := testOutcomeForIssued("outcome-1", issued, owner, proxy)
	if _, err := store.AppendOutcome(ctx, owner, proxy, outcome); err != nil {
		t.Fatal(err)
	}
	wrong := outcome
	wrong.InputVersion++
	wrong.OutcomeID = "outcome-wrong-version"
	if _, err := store.AppendOutcome(ctx, owner, proxy, wrong); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("wrong input version err=%v, want ErrRequestConflict", err)
	}
	replay := outcome
	replay.OutcomeID = outcome.OutcomeID
	inserted, err := store.AppendOutcome(ctx, owner, proxy, replay)
	if err != nil || inserted {
		t.Fatalf("same identity replay: inserted=%t err=%v", inserted, err)
	}
}

func TestSQLiteStoreAppendOutcomeRejectsExpiredIssuedInput(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "proxy-latency-expired-input.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	owner, acquired, err := store.AcquireOwnerLease(ctx, "owner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("owner acquire: acquired=%t err=%v", acquired, err)
	}
	proxy, acquired, err := store.AcquireProxyLease(ctx, owner, "proxy-1", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("proxy acquire: acquired=%t err=%v", acquired, err)
	}
	draft := testInputDraft("proxy-1", TriggerPeriodic)
	draft.IssuedAt = time.Now().UTC().Add(-2 * time.Minute)
	draft.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	issued, err := store.IssueInput(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	outcome := testOutcomeForIssued("expired", issued, owner, proxy)
	if _, err := store.AppendOutcome(ctx, owner, proxy, outcome); !errors.Is(err, ErrInputFence) {
		t.Fatalf("expired input err=%v, want ErrInputFence", err)
	}
}

func TestSQLiteStoreReplayIgnoresCurrentLeaseAndInputExpiry(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "proxy-latency-replay-expiry.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	owner, acquired, err := store.AcquireOwnerLease(ctx, "owner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("owner acquire: acquired=%t err=%v", acquired, err)
	}
	proxy, acquired, err := store.AcquireProxyLease(ctx, owner, "proxy-1", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("proxy acquire: acquired=%t err=%v", acquired, err)
	}
	issued, err := store.IssueInput(ctx, testInputDraft("proxy-1", TriggerPeriodic))
	if err != nil {
		t.Fatal(err)
	}
	outcome := testOutcomeForIssued("replay-expiry", issued, owner, proxy)
	if inserted, err := store.AppendOutcome(ctx, owner, proxy, outcome); err != nil || !inserted {
		t.Fatalf("initial outcome: inserted=%t err=%v", inserted, err)
	}
	if _, err := store.db.Exec(`UPDATE proxy_latency_inputs SET expires_at=? WHERE request_id=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), issued.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE proxy_latency_owner_leases SET lease_until=? WHERE lease_key=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), "proxy-latency-owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE proxy_latency_proxy_leases SET lease_until=? WHERE proxy_id=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), proxy.ProxyID); err != nil {
		t.Fatal(err)
	}
	if inserted, err := store.AppendOutcome(ctx, owner, proxy, outcome); err != nil || inserted {
		t.Fatalf("expired replay: inserted=%t err=%v", inserted, err)
	}
}

func TestSQLiteStoreRejectsPoisonedCommittedOutcomeDigest(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "proxy-latency-poisoned-outcome.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	owner, acquired, err := store.AcquireOwnerLease(ctx, "owner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("owner acquire: acquired=%t err=%v", acquired, err)
	}
	proxy, acquired, err := store.AcquireProxyLease(ctx, owner, "proxy-1", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("proxy acquire: acquired=%t err=%v", acquired, err)
	}
	issued, err := store.IssueInput(ctx, testInputDraft("proxy-1", TriggerPeriodic))
	if err != nil {
		t.Fatal(err)
	}
	outcome := testOutcomeForIssued("poisoned-outcome", issued, owner, proxy)
	if inserted, err := store.AppendOutcome(ctx, owner, proxy, outcome); err != nil || !inserted {
		t.Fatalf("initial outcome: inserted=%t err=%v", inserted, err)
	}
	var payload []byte
	if err := store.db.QueryRow(`SELECT payload FROM proxy_latency_outcomes WHERE request_id=?`, issued.RequestID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	// Keep the original payload_digest while changing the bytes. Both direct
	// replay and AdmitExecution must fail closed instead of trusting poisoned
	// committed JSON.
	payload = append(payload, byte(' '))
	if _, err := store.db.Exec(`UPDATE proxy_latency_outcomes SET payload=? WHERE request_id=?`, payload, issued.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LoadCommittedOutcome(ctx, issued); !errors.Is(err, ErrRequestConflict) || found {
		t.Fatalf("poisoned direct replay found=%t err=%v", found, err)
	}
	if _, committed, err := ExecuteIssuedInput(ctx, store, owner, proxy, issued, ExecutorOptions{Timeout: time.Second}); !errors.Is(err, ErrRequestConflict) || committed {
		t.Fatalf("poisoned admitted replay committed=%t err=%v", committed, err)
	}
}

func TestSQLiteStoreReplayAfterOwnerAndProxyLeaseTakeover(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "proxy-latency-replay-takeover.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	ownerA, acquired, err := store.AcquireOwnerLease(ctx, "owner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("owner A acquire: acquired=%t err=%v", acquired, err)
	}
	proxyA, acquired, err := store.AcquireProxyLease(ctx, ownerA, "proxy-1", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("proxy A acquire: acquired=%t err=%v", acquired, err)
	}
	issued, err := store.IssueInput(ctx, testInputDraft("proxy-1", TriggerPeriodic))
	if err != nil {
		t.Fatal(err)
	}
	outcome := testOutcomeForIssued("takeover-replay", issued, ownerA, proxyA)
	if inserted, err := store.AppendOutcome(ctx, ownerA, proxyA, outcome); err != nil || !inserted {
		t.Fatalf("initial outcome: inserted=%t err=%v", inserted, err)
	}
	// Expire both leases, then let a successor acquire new fence tokens. The
	// original immutable outcome still carries owner A/proxy A tokens; replay
	// must be found before validating the successor caller shape.
	expired := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`UPDATE proxy_latency_owner_leases SET lease_until=? WHERE lease_key=?`, expired, "proxy-latency-owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE proxy_latency_proxy_leases SET lease_until=? WHERE proxy_id=?`, expired, proxyA.ProxyID); err != nil {
		t.Fatal(err)
	}
	ownerB, acquired, err := store.AcquireOwnerLease(ctx, "owner-b", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("owner B acquire: acquired=%t err=%v", acquired, err)
	}
	proxyB, acquired, err := store.AcquireProxyLease(ctx, ownerB, proxyA.ProxyID, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("proxy B acquire: acquired=%t err=%v", acquired, err)
	}
	if proxyB.FenceToken <= proxyA.FenceToken || ownerB.FenceToken <= ownerA.FenceToken {
		t.Fatalf("successor fences did not advance: ownerA=%+v ownerB=%+v proxyA=%+v proxyB=%+v", ownerA, ownerB, proxyA, proxyB)
	}
	if inserted, err := store.AppendOutcome(ctx, ownerB, proxyB, outcome); err != nil || inserted {
		t.Fatalf("successor replay: inserted=%t err=%v", inserted, err)
	}
}

func TestSQLiteStoreRejectsObservedAtExpiryBoundary(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "proxy-latency-observed-boundary.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	owner, acquired, err := store.AcquireOwnerLease(ctx, "owner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("owner acquire: acquired=%t err=%v", acquired, err)
	}
	proxy, acquired, err := store.AcquireProxyLease(ctx, owner, "proxy-1", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("proxy acquire: acquired=%t err=%v", acquired, err)
	}
	issued, err := store.IssueInput(ctx, testInputDraft("proxy-1", TriggerPeriodic))
	if err != nil {
		t.Fatal(err)
	}
	outcome := testOutcomeForIssued("expiry-boundary", issued, owner, proxy)
	outcome.ObservedAt = issued.ExpiresAt
	if _, err := store.AppendOutcome(ctx, owner, proxy, outcome); !errors.Is(err, ErrInputFence) {
		t.Fatalf("observed_at == expires_at err=%v, want ErrInputFence", err)
	}
}

func TestSQLiteStoreRejectsChangedInputAfterExecutionAdmission(t *testing.T) {
	store, owner, proxy, input := executorFixture(t, "http://127.0.0.1:1", "", "")
	defer store.Close()
	ctx := context.Background()
	resolved, claim, replay, err := store.AdmitExecution(ctx, owner, proxy, input)
	if err != nil || claim == "" || replay != nil {
		t.Fatalf("admit claim=%q replay=%v err=%v", claim, replay, err)
	}
	changed := resolved
	changed.Targets = append([]Target(nil), resolved.Targets...)
	changed.Targets[0].URL = "https://changed.example/"
	payload, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	if _, err := store.db.Exec(`UPDATE proxy_latency_inputs SET payload=?, payload_digest=? WHERE request_id=?`, payload, hex.EncodeToString(digest[:]), resolved.RequestID); err != nil {
		t.Fatal(err)
	}
	outcome := testOutcomeForIssued(stableOutcomeID(resolved.RequestID), resolved, owner, proxy)
	outcome.executionClaimToken = claim
	if _, err := store.AppendOutcome(ctx, owner, proxy, outcome); !errors.Is(err, ErrInputFence) {
		t.Fatalf("changed durable input err=%v, want ErrInputFence", err)
	}
}

func TestSQLiteStoreRejectsNonUTCIssuedAtAndRevisionWhitespace(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "proxy-latency-input-utc.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	valid := testInputDraft("proxy-1", TriggerPeriodic)
	for name, mutate := range map[string]func(*InputDraft){
		"issued at non utc":   func(draft *InputDraft) { draft.IssuedAt = draft.IssuedAt.In(time.FixedZone("CST", 8*60*60)) },
		"expires at non utc":  func(draft *InputDraft) { draft.ExpiresAt = draft.ExpiresAt.In(time.FixedZone("CST", 8*60*60)) },
		"revision whitespace": func(draft *InputDraft) { draft.ConfigRevision = " " + draft.ConfigRevision + " " },
	} {
		t.Run(name, func(t *testing.T) {
			draft := valid
			mutate(&draft)
			if _, err := store.IssueInput(context.Background(), draft); err == nil {
				t.Fatal("invalid UTC/revision input must fail closed")
			}
		})
	}
}

func TestSQLiteStoreCanonicalizesIssuedInputTargetsAndRevision(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "proxy-latency-input-canonical.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	draft := testInputDraft("proxy-1", TriggerPeriodic)
	draft.ConfigRevision = "2026-08-21T05:00:00.123456789Z"
	draft.Targets = []Target{{Provider: " OpenAI ", ProfileID: " profile-openai ", URL: " https://api.openai.com/v1 "}}
	issued, err := store.IssueInput(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	if issued.ConfigRevision != draft.ConfigRevision || issued.Targets[0].Provider != "openai" || issued.Targets[0].ProfileID != "profile-openai" || issued.Targets[0].URL != "https://api.openai.com/v1" {
		t.Fatalf("issued input was not canonicalized: %+v", issued)
	}
}

func TestSQLiteStoreDoesNotPersistInvalidTargetURL(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "proxy-latency-invalid-target.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	draft := testInputDraft("proxy-1", TriggerPeriodic)
	draft.Targets = []Target{
		{Provider: "valid", ProfileID: "profile-valid", URL: "https://example.test/v1"},
		{Provider: "query", ProfileID: "profile-query", URL: "https://example.test/v1?token=query-secret"},
		{Provider: "userinfo", ProfileID: "profile-userinfo", URL: "https://user:userinfo-secret@example.test/v1"},
		{Provider: "fragment", ProfileID: "profile-fragment", URL: "https://example.test/v1#fragment-secret"},
	}
	issued, err := store.IssueInput(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if err := store.db.QueryRow(`SELECT payload FROM proxy_latency_inputs WHERE request_id=?`, issued.RequestID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range []string{"query-secret", "userinfo-secret", "fragment-secret"} {
		if strings.Contains(string(payload), sentinel) {
			t.Fatalf("durable issued payload leaked invalid target sentinel %q: %s", sentinel, payload)
		}
	}
	var persisted IssuedInput
	if err := json.Unmarshal(payload, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Targets) != 4 || persisted.Targets[0].URL != "https://example.test/v1" {
		t.Fatalf("persisted target set=%+v", persisted.Targets)
	}
	for _, target := range persisted.Targets[1:] {
		if target.URL != "" || target.ProbeError != targetProbeErrorInvalidURL || target.Provider == "" || target.ProfileID == "" {
			t.Fatalf("invalid target durable form=%+v", target)
		}
	}
}

func TestSQLiteStoreDropsPasswordOnlyCredentialLikeNode(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "proxy-latency-password-only.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	draft := testInputDraft("proxy-1", TriggerPeriodic)
	draft.ProxyPassword = &CredentialEnvelope{Kind: "proxy_password", Ciphertext: "v1:MTIzNDU2Nzg5MDEy:MTIzNDU2Nzg5MDEyMzQ1Ng:Y2lwaGVydGV4dA"}
	issued, err := store.IssueInput(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	if issued.ProxyPassword != nil {
		t.Fatal("password-only credential must not change Node-compatible no-auth semantics")
	}
}

func TestSQLiteStoreRejectsInvalidInputDraft(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "proxy-latency-invalid-input.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	valid := testInputDraft("proxy-1", TriggerPeriodic)
	for name, mutate := range map[string]func(*InputDraft){
		"short ttl":          func(draft *InputDraft) { draft.ExpiresAt = draft.IssuedAt.Add(30 * time.Second) },
		"long ttl":           func(draft *InputDraft) { draft.ExpiresAt = draft.IssuedAt.Add(16 * time.Minute) },
		"non utc revision":   func(draft *InputDraft) { draft.ConfigRevision = "2026-08-21T05:00:00+08:00" },
		"wrong policy":       func(draft *InputDraft) { draft.PolicyVersion = "other-policy" },
		"duplicate provider": func(draft *InputDraft) { draft.Targets = append(draft.Targets, draft.Targets[0]) },
		"duplicate provider with whitespace": func(draft *InputDraft) {
			draft.Targets = append(draft.Targets, Target{Provider: " gpt ", ProfileID: "profile-other", URL: "https://api.example.com/v1"})
		},
		"wrong password kind": func(draft *InputDraft) {
			draft.ProxyPassword = &CredentialEnvelope{Kind: "wrong", Ciphertext: "v1:MTIzNDU2Nzg5MDEy:MTIzNDU2Nzg5MDEyMzQ1Ng:Y2lwaGVydGV4dA"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			draft := valid
			draft.Targets = append([]Target(nil), valid.Targets...)
			mutate(&draft)
			if _, err := store.IssueInput(context.Background(), draft); err == nil {
				t.Fatal("invalid input draft must fail closed")
			}
		})
	}
}

func TestPostgresSchemaIsJobsOnlyAndUsesLocalTimeouts(t *testing.T) {
	if containsForbiddenPostgresDDL(postgresSchema) {
		t.Fatal("PostgreSQL schema must contain only CREATE TABLE IF NOT EXISTS for jobs tables")
	}
	if postgresSchema == "" || postgresSetLocalSQL == "" {
		t.Fatal("PostgreSQL jobs store must define schema and transaction-local timeouts")
	}
}

func TestCanonicalJSONDigestSurvivesJSONBKeyReordering(t *testing.T) {
	issued := IssuedInput{
		RequestID: "j3a-request", ProxyID: "proxy-1", InputVersion: 1,
		ConfigRevision: "2026-08-21T04:59:00Z", Trigger: TriggerPeriodic,
		IssuedAt:      time.Date(2026, 8, 21, 5, 0, 0, 0, time.UTC),
		ExpiresAt:     time.Date(2026, 8, 21, 5, 5, 0, 0, time.UTC),
		PolicyVersion: proxyLatencyInputPolicyVersion, ProxyType: "http",
		ProxyHost: "127.0.0.1", ProxyPort: 8080,
		Targets: []Target{{Provider: "gpt", ProfileID: "profile-gpt", URL: "https://api.openai.com/v1"}},
	}
	issuedDigest, err := canonicalJSONDigest(issued)
	if err != nil {
		t.Fatal(err)
	}
	var issuedFromJSONB IssuedInput
	if err := json.Unmarshal([]byte(`{"targets":[{"url":"https://api.openai.com/v1","profile_id":"profile-gpt","provider":"gpt"}],"proxy_port":8080,"proxy_host":"127.0.0.1","proxy_type":"http","policy_version":"j3a-proxy-latency-v1","expires_at":"2026-08-21T05:05:00Z","issued_at":"2026-08-21T05:00:00Z","trigger":"periodic","config_revision":"2026-08-21T04:59:00Z","input_version":1,"proxy_id":"proxy-1","request_id":"j3a-request"}`), &issuedFromJSONB); err != nil {
		t.Fatal(err)
	}
	gotIssuedDigest, err := canonicalJSONDigest(issuedFromJSONB)
	if err != nil || gotIssuedDigest != issuedDigest {
		t.Fatalf("issued digest changed after JSONB key reordering: want=%s got=%s err=%v", issuedDigest, gotIssuedDigest, err)
	}

	outcome := Outcome{
		OutcomeID: "outcome-1", RequestID: "j3a-request", ProxyID: "proxy-1",
		ObservedAt: time.Date(2026, 8, 21, 5, 1, 0, 0, time.UTC), InputVersion: 1,
		ConfigRevision: "2026-08-21T04:59:00Z", Trigger: TriggerPeriodic,
		OwnerFenceToken: 1, ProxyFenceToken: 2, OverallStatus: OverallPassed,
		Items: []ItemResult{},
	}
	outcomeDigest, err := canonicalJSONDigest(outcome)
	if err != nil {
		t.Fatal(err)
	}
	var outcomeFromJSONB Outcome
	if err := json.Unmarshal([]byte(`{"items":[],"overall_status":"passed","proxy_fence_token":2,"owner_fence_token":1,"trigger":"periodic","config_revision":"2026-08-21T04:59:00Z","input_version":1,"observed_at":"2026-08-21T05:01:00Z","proxy_id":"proxy-1","request_id":"j3a-request","outcome_id":"outcome-1"}`), &outcomeFromJSONB); err != nil {
		t.Fatal(err)
	}
	gotOutcomeDigest, err := canonicalJSONDigest(outcomeFromJSONB)
	if err != nil || gotOutcomeDigest != outcomeDigest {
		t.Fatalf("outcome digest changed after JSONB key reordering: want=%s got=%s err=%v", outcomeDigest, gotOutcomeDigest, err)
	}
}

func testOutcome(outcomeID, requestID, proxyID string, owner OwnerLease, proxy ProxyLease) Outcome {
	return Outcome{
		OutcomeID:       outcomeID,
		RequestID:       requestID,
		ProxyID:         proxyID,
		ObservedAt:      time.Now().UTC(),
		InputVersion:    1,
		ConfigRevision:  time.Now().UTC().Format(time.RFC3339Nano),
		Trigger:         TriggerPeriodic,
		OwnerFenceToken: owner.FenceToken,
		ProxyFenceToken: proxy.FenceToken,
		OverallStatus:   OverallPassed,
		Items:           []ItemResult{{Status: ItemPassed, HTTPStatus: 200, LatencyMS: 12, Outcome: OutcomeSuccess}},
	}
}

func testOutcomeForIssued(outcomeID string, issued IssuedInput, owner OwnerLease, proxy ProxyLease) Outcome {
	outcome := testOutcome(outcomeID, issued.RequestID, issued.ProxyID, owner, proxy)
	outcome.InputVersion = issued.InputVersion
	outcome.ConfigRevision = issued.ConfigRevision
	outcome.Trigger = issued.Trigger
	outcome.ObservedAt = issued.IssuedAt.Add(time.Second)
	return outcome
}

func testInputDraft(proxyID string, trigger Trigger) InputDraft {
	now := time.Now().UTC()
	return InputDraft{
		ProxyID: proxyID, ConfigRevision: now.Add(-time.Second).Format(time.RFC3339Nano), Trigger: trigger,
		IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute), PolicyVersion: proxyLatencyInputPolicyVersion,
		ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 8080,
		Targets: []Target{{Provider: "gpt", ProfileID: "profile-gpt", URL: "https://api.openai.com/v1"}},
	}
}
