package modelcheckdurable

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
)

func TestSQLiteIssueClaimFenceAndOutcomeReplay(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "j3b.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	draft := validDraft(now)
	first, err := store.Issue(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if first.Input.InputVersion != 1 || first.IdentityKey == "" {
		t.Fatalf("issued=%#v", first)
	}
	replay, err := store.Issue(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Input.InputID != first.Input.InputID || replay.Input.InputDigest != first.Input.InputDigest || replay.Input.InputVersion != 1 {
		t.Fatalf("replay=%#v first=%#v", replay, first)
	}
	loaded, err := store.LoadInput(ctx, first.Input.InputID, now)
	if err != nil || loaded.Input.InputDigest != first.Input.InputDigest {
		t.Fatalf("load=%#v err=%v", loaded, err)
	}

	changed := draft
	changed.InputID = "input-2"
	changed.Target.ConfigRevision = "config-revision-2"
	second, err := store.Issue(ctx, changed)
	if err != nil || second.Input.InputVersion != 2 {
		t.Fatalf("different snapshot should allocate version, second=%#v err=%v", second, err)
	}

	claim, err := store.Claim(ctx, first.Input.InputID, "owner-a", "claim-a", "outcome-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim.FenceToken != 1 {
		t.Fatalf("claim=%#v", claim)
	}
	if _, err := store.Claim(ctx, first.Input.InputID, "owner-b", "claim-b", "outcome-b", now.Add(10*time.Second), time.Minute); !errors.Is(err, ErrBusy) {
		t.Fatalf("busy err=%v", err)
	}
	if _, err := store.Claim(ctx, first.Input.InputID, "owner-b", "claim-b", "outcome-b", now.Add(2*time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	newClaim, err := store.Claim(ctx, first.Input.InputID, "owner-b", "claim-b", "outcome-b", now.Add(2*time.Minute), time.Minute)
	if err != nil || newClaim.FenceToken != 2 {
		t.Fatalf("takeover=%#v err=%v", newClaim, err)
	}

	if err := store.CommitOutcome(ctx, Outcome{OutcomeID: "outcome-a", InputID: first.Input.InputID, InputDigest: first.Input.InputDigest, Payload: []byte(`{"status":"passed"}`)}, claim, now.Add(2*time.Minute)); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale commit err=%v", err)
	}
	payload := []byte(`{"status":"passed"}`)
	if err := store.CommitOutcome(ctx, Outcome{OutcomeID: "outcome-b", InputID: first.Input.InputID, InputDigest: first.Input.InputDigest, Payload: payload}, newClaim, now.Add(2*time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitOutcome(ctx, Outcome{OutcomeID: "outcome-b", InputID: first.Input.InputID, InputDigest: first.Input.InputDigest, Payload: payload}, newClaim, now.Add(2*time.Minute+2*time.Second)); err != nil {
		t.Fatalf("idempotent outcome err=%v", err)
	}
	if err := store.CommitOutcome(ctx, Outcome{OutcomeID: "outcome-b", InputID: first.Input.InputID, InputDigest: first.Input.InputDigest, Payload: payload}, newClaim, now.Add(5*time.Minute)); err != nil {
		t.Fatalf("expired idempotent outcome err=%v", err)
	}
	if err := store.CommitOutcome(ctx, Outcome{OutcomeID: "outcome-b", InputID: first.Input.InputID, InputDigest: first.Input.InputDigest, Payload: []byte(`{"status":"failed"}`)}, newClaim, now.Add(2*time.Minute+3*time.Second)); !errors.Is(err, ErrOutcomeConflict) {
		t.Fatalf("conflicting replay err=%v", err)
	}
}

func TestSQLiteIssueRejectsInputIDReuseWithDifferentImmutableInput(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "versions.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	if _, err := store.Issue(ctx, validDraft(now)); err != nil {
		t.Fatal(err)
	}
	reused := validDraft(now)
	reused.Target.ConfigRevision = "config-revision-2"
	if _, err := store.Issue(ctx, reused); err == nil || !strings.Contains(err.Error(), "already bound") {
		t.Fatalf("input ID reuse err=%v", err)
	}
}

func TestPostgresModelCheckDurableSmoke(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("J3B_MODEL_CHECK_DURABLE_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("J3B_MODEL_CHECK_DURABLE_POSTGRES_URL is not set")
	}
	store, err := OpenPostgres(dsn, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	draft := validDraft(now)
	draft.InputID = "pg-smoke-input-" + strings.ReplaceAll(now.Format("150405.000000"), ".", "")
	issued, err := store.Issue(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadInput(ctx, issued.Input.InputID, now); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, issued.Input.InputID, "pg-smoke-owner", "pg-smoke-claim", "pg-smoke-outcome", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"status":"passed","source":"postgres-smoke"}`)
	commit := Outcome{OutcomeID: claim.OutcomeID, InputID: issued.Input.InputID, InputDigest: issued.Input.InputDigest, Payload: payload}
	if err := store.CommitOutcome(ctx, commit, claim, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitOutcome(ctx, commit, claim, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("idempotent postgres replay: %v", err)
	}
}

func validDraft(issuedAt time.Time) modelcheckinput.Draft {
	return modelcheckinput.Draft{
		InputID: "input-1", SystemAccountID: "system-account", ActorSystemAccountID: "actor-account",
		Target: modelcheckinput.AccountSnapshot{ID: "target-account", ConfigRevision: "config-revision-1", ProviderCode: "openai", ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", EndpointFingerprint: "endpoint-hmac-1", MappedUpstreamModel: "gpt-5.6-sol", CredentialEnvelopeRef: "credential-alias-1", ProxyConfigurationVersion: "proxy-revision-1"},
		Model:  "gpt-5.6-sol", Profile: "quick", Trigger: modelcheckinput.TriggerManual, ProbeSetVersion: "probe-v1", Policy: modelcheckinput.PolicySnapshot{Revision: "policy-revision-1", Digest: "policy-digest-1"}, IssuedAt: issuedAt, DeadlineAt: issuedAt.Add(5 * time.Minute),
	}
}
