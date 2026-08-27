package accounthealth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestExecuteInputProbeUsesKeyPoolCursorAndReportsWinner(t *testing.T) {
	secret := "test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer sk-good" {
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(`{"error":{"message":"bad key"}}`))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"juhe"}}]}`))
	}))
	defer server.Close()
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "account-health.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	lease, acquired, err := store.AcquireOwnerLease(ctx, "owner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire lease=%#v acquired=%t err=%v", lease, acquired, err)
	}
	input := testInput(server.URL, "chat_json")
	input.KeySetFingerprint = "set-v1"
	input.APIKeys = []APIKeyInput{
		{Index: 0, Fingerprint: "bad", Credential: CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, "sk-bad")}},
		{Index: 1, Fingerprint: "good", Credential: CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, "sk-good")}},
	}
	outcome, err := ExecuteInputProbe(ctx, store, lease, input, ProbeRequest{RequestID: "request-1", AccountID: input.AccountID, InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, Deadline: time.Now().Add(time.Minute)}, ProbeOptions{Secret: secret, Timeout: time.Second})
	if err != nil || outcome.Outcome != OutcomeSuccess || outcome.WinnerIndex == nil || *outcome.WinnerIndex != 1 || outcome.WinnerKeyFingerprint != "good" {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
	next, found, err := store.LoadKeyCursor(ctx, input.AccountID, healthKeyCursorPurpose, input.KeySetFingerprint)
	if err != nil || !found || next != 0 {
		t.Fatalf("cursor next=%d found=%t err=%v", next, found, err)
	}
}

func TestExecuteInputProbeUsesInjectedClockForOutcome(t *testing.T) {
	fixed := time.Date(2026, 8, 17, 3, 4, 5, 678000000, time.FixedZone("UTC+8", 8*60*60))
	input := Input{AccountID: "account", InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1}
	outcome, err := ExecuteInputProbe(context.Background(), nil, OwnerLease{}, input, ProbeRequest{RequestID: "request", AccountID: "other"}, ProbeOptions{Now: func() time.Time { return fixed }})
	if err != nil {
		t.Fatal(err)
	}
	want := fixed.UTC()
	if !outcome.ObservedAt.Equal(want) || outcome.ObservedAt.Location() != time.UTC {
		t.Fatalf("outcome observedAt=%s, want injected UTC clock %s", outcome.ObservedAt, want)
	}
}

func TestExecuteInputProbePersistsCursorAfterProbeContextExpires(t *testing.T) {
	secret := "test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// Keep the upstream busy past the short probe deadline, then let the
		// handler return so httptest.Server.Close cannot wait on a leaked request.
		time.Sleep(200 * time.Millisecond)
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "account-health.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "owner-timeout", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire lease=%#v acquired=%t err=%v", lease, acquired, err)
	}
	input := testInput(server.URL, "chat_json")
	input.KeySetFingerprint = "set-timeout"
	input.APIKeys = []APIKeyInput{
		{Index: 0, Fingerprint: "first", Credential: CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, "sk-first")}},
		{Index: 1, Fingerprint: "second", Credential: CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, "sk-second")}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	outcome, err := ExecuteInputProbe(ctx, store, lease, input, ProbeRequest{RequestID: "request-timeout", AccountID: input.AccountID, InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, Deadline: time.Now().Add(time.Second)}, ProbeOptions{Secret: secret, Timeout: time.Second})
	if err != nil {
		t.Fatalf("expired probe context must still produce an outcome after cursor persistence: %v", err)
	}
	if outcome.Outcome != OutcomeUpstreamFailed && outcome.Outcome != OutcomeTaskFailed {
		t.Fatalf("outcome=%#v, want a failed probe outcome", outcome)
	}
	next, found, err := store.LoadKeyCursor(context.Background(), input.AccountID, healthKeyCursorPurpose, input.KeySetFingerprint)
	if err != nil || !found || next != 1 {
		t.Fatalf("cursor next=%d found=%t err=%v, want persisted next index 1", next, found, err)
	}
}
