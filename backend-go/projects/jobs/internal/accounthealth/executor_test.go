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
	if err != nil || outcome.Outcome != OutcomeSuccess || outcome.WinnerIndex == nil || *outcome.WinnerIndex != 1 {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
	next, found, err := store.LoadKeyCursor(ctx, input.AccountID, healthKeyCursorPurpose, input.KeySetFingerprint)
	if err != nil || !found || next != 0 {
		t.Fatalf("cursor next=%d found=%t err=%v", next, found, err)
	}
}
