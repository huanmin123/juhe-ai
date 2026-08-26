package proxylatency

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestExecuteIssuedInputCommitsIsolatedSuccessNeutralAndFailure(t *testing.T) {
	var calls atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		switch request.URL.Path {
		case "/success":
			writer.WriteHeader(http.StatusOK)
		case "/neutral":
			writer.WriteHeader(http.StatusServiceUnavailable)
		case "/timeout":
			time.Sleep(100 * time.Millisecond)
			writer.WriteHeader(http.StatusOK)
		default:
			writer.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer proxyServer.Close()
	store, owner, proxy, input := executorFixture(t, proxyServer.URL, "", "")
	defer store.Close()
	input.Targets = []Target{
		{Provider: "success", ProfileID: "success", URL: "http://provider.example/success"},
		{Provider: "neutral", ProfileID: "neutral", URL: "http://provider.example/neutral"},
		{Provider: "timeout", ProfileID: "timeout", URL: "http://provider.example/timeout"},
	}
	// The first fixture input is deliberately replaced with a Store-issued
	// three-target request so the executor never consumes an ad-hoc snapshot.
	issued, err := store.IssueInput(context.Background(), InputDraft{
		ProxyID: input.ProxyID, ConfigRevision: input.ConfigRevision, Trigger: input.Trigger,
		IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(5 * time.Minute), PolicyVersion: proxyLatencyInputPolicyVersion,
		ProxyType: input.ProxyType, ProxyHost: input.ProxyHost, ProxyPort: input.ProxyPort, Targets: input.Targets,
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, committed, err := ExecuteIssuedInput(context.Background(), store, owner, proxy, issued, ExecutorOptions{Timeout: 20 * time.Millisecond})
	if err != nil || !committed {
		t.Fatalf("execute committed=%t err=%v", committed, err)
	}
	if calls.Load() != 3 || len(outcome.Items) != 3 || outcome.Items[0].Outcome != OutcomeSuccess || outcome.Items[1].Outcome != OutcomeNeutral || outcome.Items[2].Outcome != OutcomeUpstreamFailure || outcome.OverallStatus != OverallFailed {
		t.Fatalf("isolated item results=%+v calls=%d overall=%s", outcome.Items, calls.Load(), outcome.OverallStatus)
	}
}

func TestExecuteIssuedInputRecordsInvalidTargetWithoutOutboundRequest(t *testing.T) {
	var calls atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer proxyServer.Close()
	store, owner, proxy, input := executorFixture(t, proxyServer.URL, "", "")
	defer store.Close()
	issued, err := store.IssueInput(context.Background(), InputDraft{
		ProxyID: input.ProxyID, ConfigRevision: input.ConfigRevision, Trigger: input.Trigger,
		IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(5 * time.Minute), PolicyVersion: proxyLatencyInputPolicyVersion,
		ProxyType: input.ProxyType, ProxyHost: input.ProxyHost, ProxyPort: input.ProxyPort,
		Targets: []Target{
			{Provider: "reachable", ProfileID: "reachable", URL: "http://provider.example/ok"},
			{Provider: "hybrid", ProfileID: "hybrid", URL: "http://provider.example/blocked?token=query-secret"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if issued.Targets[1].URL != "" || issued.Targets[1].ProbeError != targetProbeErrorInvalidURL {
		t.Fatalf("Store must canonicalize invalid target to sanitized probe error: %+v", issued.Targets[1])
	}
	outcome, committed, err := ExecuteIssuedInput(context.Background(), store, owner, proxy, issued, ExecutorOptions{Timeout: time.Second})
	if err != nil || !committed {
		t.Fatalf("execute committed=%t err=%v", committed, err)
	}
	if calls.Load() != 1 || len(outcome.Items) != 2 || outcome.Items[1].Status != ItemUnknown || outcome.Items[1].ErrorCode != targetProbeErrorInvalidURL || outcome.OverallStatus != OverallWarning {
		t.Fatalf("invalid target must be an unknown without outbound request: calls=%d outcome=%+v", calls.Load(), outcome)
	}
}

func TestExecuteIssuedInputReplayAvoidsSecondProbe(t *testing.T) {
	var calls atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer proxyServer.Close()
	store, owner, proxy, input := executorFixture(t, proxyServer.URL, "", "")
	defer store.Close()
	startedAt := input.IssuedAt.Add(time.Second)
	first, committed, err := ExecuteIssuedInput(context.Background(), store, owner, proxy, input, ExecutorOptions{Timeout: time.Second, Now: func() time.Time { return startedAt }})
	if err != nil || !committed {
		t.Fatalf("first committed=%t err=%v", committed, err)
	}
	if !first.ObservedAt.Equal(canonicalPostgresTimestamp(startedAt)) {
		t.Fatalf("observed_at=%s want probe start %s", first.ObservedAt, startedAt)
	}
	second, committed, err := ExecuteIssuedInput(context.Background(), store, owner, proxy, input, ExecutorOptions{Timeout: time.Second})
	if err != nil || committed || second.OutcomeID != first.OutcomeID || calls.Load() != 1 {
		t.Fatalf("replay committed=%t err=%v calls=%d first=%+v second=%+v", committed, err, calls.Load(), first, second)
	}
}

func TestExecuteIssuedInputCapsUpstreamTimeoutAtIssuedInputExpiry(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	proxyServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-request.Context().Done()
		close(requestCanceled)
	}))
	defer proxyServer.Close()
	store, owner, proxy, input := executorFixture(t, proxyServer.URL, "", "")
	defer store.Close()

	// Move the test clock inside the durable validity window so a multi-second
	// executor timeout is forced down to the remaining 25ms without sleeping
	// for a minute. Production calls leave Now nil and use the real clock.
	clock := func() time.Time { return input.ExpiresAt.Add(-25 * time.Millisecond) }
	started := time.Now()
	outcome, committed, err := ExecuteIssuedInput(context.Background(), store, owner, proxy, input, ExecutorOptions{
		Timeout: time.Second,
		Now:     clock,
	})
	if err != nil || !committed {
		t.Fatalf("expiry-capped execution committed=%t err=%v outcome=%+v", committed, err, outcome)
	}
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("upstream probe did not start")
	}
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("upstream request was not canceled at issued input expiry")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("expiry-capped probe exceeded claim window: elapsed=%s", elapsed)
	}
	if len(outcome.Items) != 1 || outcome.Items[0].Outcome != OutcomeUpstreamFailure || outcome.Items[0].ErrorCode != "timeout" {
		t.Fatalf("expiry-capped item=%+v", outcome.Items)
	}
}

func TestExecuteIssuedInputSingleFlightRejectsConcurrentProbe(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	proxyServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		writer.WriteHeader(http.StatusOK)
	}))
	defer proxyServer.Close()
	store, owner, proxy, input := executorFixture(t, proxyServer.URL, "", "")
	defer store.Close()
	firstResult := make(chan struct {
		outcome   Outcome
		committed bool
		err       error
	}, 1)
	go func() {
		outcome, committed, err := ExecuteIssuedInput(context.Background(), store, owner, proxy, input, ExecutorOptions{Timeout: 2 * time.Second})
		firstResult <- struct {
			outcome   Outcome
			committed bool
			err       error
		}{outcome, committed, err}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first probe did not reach upstream")
	}
	_, committed, err := ExecuteIssuedInput(context.Background(), store, owner, proxy, input, ExecutorOptions{Timeout: time.Second})
	if !errors.Is(err, ErrRequestInFlight) || committed || calls.Load() != 1 {
		t.Fatalf("concurrent follower committed=%t err=%v calls=%d", committed, err, calls.Load())
	}
	close(release)
	first := <-firstResult
	if first.err != nil || !first.committed || first.outcome.OutcomeID != stableOutcomeID(input.RequestID) || calls.Load() != 1 {
		t.Fatalf("leader result=%+v calls=%d", first, calls.Load())
	}
	replay, committed, err := ExecuteIssuedInput(context.Background(), store, owner, proxy, input, ExecutorOptions{Timeout: time.Second})
	if err != nil || committed || replay.OutcomeID != first.outcome.OutcomeID || calls.Load() != 1 {
		t.Fatalf("post-commit replay committed=%t err=%v calls=%d", committed, err, calls.Load())
	}
}

func TestAdmitExecutionReturnsIndependentPersistedSnapshot(t *testing.T) {
	store, owner, proxy, input := executorFixture(t, "http://127.0.0.1:1", "", "")
	defer store.Close()
	resolved, claim, replay, err := store.AdmitExecution(context.Background(), owner, proxy, input)
	if err != nil || claim == "" || replay != nil {
		t.Fatalf("admit claim=%q replay=%v err=%v", claim, replay, err)
	}
	input.Targets[0].URL = "https://mutated.example/"
	if resolved.Targets[0].URL == input.Targets[0].URL {
		t.Fatal("admitted snapshot aliases caller target slice")
	}
	if input.ProxyPassword != nil {
		input.ProxyPassword.Ciphertext = "mutated"
	}
	if err := store.ReleaseExecutionClaim(context.Background(), resolved.RequestID, claim); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteIssuedInputDecryptsPasswordWithoutOutcomeLeak(t *testing.T) {
	const secret = "executor-secret-not-for-outcome"
	const password = "proxy-password-never-persist"
	proxyServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Proxy-Authorization") == "" {
			writer.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer proxyServer.Close()
	store, owner, proxy, input := executorFixture(t, proxyServer.URL, secret, password)
	defer store.Close()
	outcome, committed, err := ExecuteIssuedInput(context.Background(), store, owner, proxy, input, ExecutorOptions{CredentialSecret: secret, Timeout: time.Second})
	if err != nil || !committed {
		t.Fatalf("execute committed=%t err=%v", committed, err)
	}
	payload, err := json.Marshal(outcome)
	if err != nil || strings.Contains(string(payload), password) || strings.Contains(string(payload), secret) || strings.Contains(string(payload), input.ProxyPassword.Ciphertext) || strings.Contains(string(payload), input.Targets[0].URL) {
		t.Fatalf("outcome contains proxy secret material")
	}
}

func TestExecuteIssuedInputRejectsBadCredentialWithoutCommittedOutcome(t *testing.T) {
	const secret = "executor-good-secret"
	const password = "bad-credential-password"
	store, owner, proxy, input := executorFixture(t, "http://127.0.0.1:1", secret, password)
	defer store.Close()
	_, committed, err := ExecuteIssuedInput(context.Background(), store, owner, proxy, input, ExecutorOptions{CredentialSecret: "wrong-secret", Timeout: time.Second})
	if err == nil || committed || strings.Contains(err.Error(), password) || strings.Contains(err.Error(), input.ProxyPassword.Ciphertext) || outcomeCount(t, store) != 0 {
		t.Fatalf("bad credential committed=%t err=%v", committed, err)
	}
}

func TestExecuteIssuedInputRejectsMalformedEnvelopeWithoutCommittedOutcome(t *testing.T) {
	store, owner, proxy, input := executorFixture(t, "http://127.0.0.1:1", "fixture-secret", "fixture-password")
	defer store.Close()
	// The Store-issued snapshot is durable, so malformed envelope input is
	// rejected as an input fence before any execution or outcome write.
	tampered := input
	tampered.ProxyPassword = &CredentialEnvelope{Kind: "proxy_password", Ciphertext: "v1:not-valid"}
	_, committed, err := ExecuteIssuedInput(context.Background(), store, owner, proxy, tampered, ExecutorOptions{CredentialSecret: "fixture-secret", Timeout: time.Second})
	if !errors.Is(err, ErrInputFence) || committed || outcomeCount(t, store) != 0 {
		t.Fatalf("malformed envelope committed=%t err=%v", committed, err)
	}
}

func TestExecuteIssuedInputRejectsLegacyPasswordOnlyEnvelope(t *testing.T) {
	store, owner, proxy, input := executorFixture(t, "http://127.0.0.1:1", "fixture-secret", "fixture-password")
	defer store.Close()
	input.ProxyUsername = ""
	input.ProxyPassword = &CredentialEnvelope{Kind: "proxy_password", Ciphertext: "v1:MTIzNDU2Nzg5MDEy:MTIzNDU2Nzg5MDEyMzQ1Ng:Y2lwaGVydGV4dA"}
	_, committed, err := ExecuteIssuedInput(context.Background(), store, owner, proxy, input, ExecutorOptions{CredentialSecret: "fixture-secret", Timeout: time.Second})
	if !errors.Is(err, ErrInputFence) || committed || outcomeCount(t, store) != 0 {
		t.Fatalf("password-only input committed=%t err=%v", committed, err)
	}
}

func TestExecuteIssuedInputRejectsLostLeaseExpiredInputAndCancellation(t *testing.T) {
	t.Run("lost proxy lease", func(t *testing.T) {
		store, owner, proxy, input := executorFixture(t, "http://127.0.0.1:1", "", "")
		defer store.Close()
		if err := store.ReleaseProxyLease(context.Background(), proxy); err != nil {
			t.Fatal(err)
		}
		_, committed, err := ExecuteIssuedInput(context.Background(), store, owner, proxy, input, ExecutorOptions{Timeout: time.Second})
		if !errors.Is(err, ErrProxyLeaseLost) || committed || outcomeCount(t, store) != 0 {
			t.Fatalf("lost lease committed=%t err=%v", committed, err)
		}
	})
	t.Run("expired input", func(t *testing.T) {
		store, owner, proxy, input := executorFixture(t, "http://127.0.0.1:1", "", "")
		defer store.Close()
		if _, err := store.db.Exec(`UPDATE proxy_latency_inputs SET expires_at=? WHERE request_id=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), input.RequestID); err != nil {
			t.Fatal(err)
		}
		_, committed, err := ExecuteIssuedInput(context.Background(), store, owner, proxy, input, ExecutorOptions{Timeout: time.Second})
		if !errors.Is(err, ErrInputFence) || committed || outcomeCount(t, store) != 0 {
			t.Fatalf("expired input committed=%t err=%v", committed, err)
		}
	})
	t.Run("cancelled context", func(t *testing.T) {
		store, owner, proxy, input := executorFixture(t, "http://127.0.0.1:1", "", "")
		defer store.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, committed, err := ExecuteIssuedInput(ctx, store, owner, proxy, input, ExecutorOptions{Timeout: time.Second})
		if !errors.Is(err, context.Canceled) || committed || outcomeCount(t, store) != 0 {
			t.Fatalf("cancelled committed=%t err=%v", committed, err)
		}
	})
}

func TestExecuteIssuedInputMakesClaimReleaseFailureVisible(t *testing.T) {
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer proxyServer.Close()
	store, owner, proxy, input := executorFixture(t, proxyServer.URL, "", "")
	defer store.Close()
	releaseErr := errors.New("execution claim release failure")
	store.releaseExecutionClaim = func(context.Context, string, string) error { return releaseErr }
	outcome, committed, err := ExecuteIssuedInput(context.Background(), store, owner, proxy, input, ExecutorOptions{Timeout: time.Second})
	if !errors.Is(err, releaseErr) || committed || outcome.OutcomeID != "" {
		t.Fatalf("claim release failure was not surfaced: outcome=%+v committed=%t err=%v", outcome, committed, err)
	}
}

func TestExecuteIssuedInputRejectsTamperedIssuedSnapshotAndBusyProxy(t *testing.T) {
	store, owner, proxy, input := executorFixture(t, "http://127.0.0.1:1", "", "")
	defer store.Close()
	if _, acquired, err := store.AcquireProxyLease(context.Background(), owner, proxy.ProxyID, time.Minute); err != nil || acquired {
		t.Fatalf("active proxy must be busy: acquired=%t err=%v", acquired, err)
	}
	tampered := input
	tampered.Targets = append([]Target(nil), input.Targets...)
	tampered.Targets[0].URL = "https://other.example/changed"
	_, committed, err := ExecuteIssuedInput(context.Background(), store, owner, proxy, tampered, ExecutorOptions{Timeout: time.Second})
	if !errors.Is(err, ErrInputFence) || committed || outcomeCount(t, store) != 0 {
		t.Fatalf("tampered input committed=%t err=%v", committed, err)
	}
}

func executorFixture(t *testing.T, proxyEndpoint, secret, password string) (*Store, OwnerLease, ProxyLease, IssuedInput) {
	t.Helper()
	endpoint, err := url.Parse(proxyEndpoint)
	if err != nil || endpoint.Hostname() == "" {
		t.Fatalf("proxy endpoint invalid: %v", err)
	}
	port := endpoint.Port()
	if port == "" {
		port = "80"
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "executor.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, acquired, err := store.AcquireOwnerLease(ctx, "executor-owner", time.Minute)
	if err != nil || !acquired {
		store.Close()
		t.Fatalf("owner acquire: acquired=%t err=%v", acquired, err)
	}
	proxy, acquired, err := store.AcquireProxyLease(ctx, owner, "executor-proxy", time.Minute)
	if err != nil || !acquired {
		store.Close()
		t.Fatalf("proxy acquire: acquired=%t err=%v", acquired, err)
	}
	now := time.Now().UTC()
	draft := InputDraft{ProxyID: proxy.ProxyID, ConfigRevision: now.Add(-time.Second).Format(time.RFC3339Nano), Trigger: TriggerPeriodic, IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute), PolicyVersion: proxyLatencyInputPolicyVersion, ProxyType: endpoint.Scheme, ProxyHost: endpoint.Hostname(), ProxyPort: parsedPort, Targets: []Target{{Provider: "target", ProfileID: "target-profile", URL: "http://provider.example/success"}}}
	if password != "" {
		draft.ProxyUsername = "executor-user"
		draft.ProxyPassword = &CredentialEnvelope{Kind: "proxy_password", Ciphertext: testProxyPasswordEnvelope(t, secret, password)}
	}
	input, err := store.IssueInput(ctx, draft)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, owner, proxy, input
}

func outcomeCount(t *testing.T, store *Store) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM proxy_latency_outcomes`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func testProxyPasswordEnvelope(t *testing.T, secret, password string) string {
	t.Helper()
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		t.Fatal(err)
	}
	plaintext, err := json.Marshal(map[string]string{"password": password})
	if err != nil {
		t.Fatal(err)
	}
	sealed := gcm.Seal(nil, iv, plaintext, nil)
	cut := len(sealed) - 16
	return "v1:" + base64.RawURLEncoding.EncodeToString(iv) + ":" + base64.RawURLEncoding.EncodeToString(sealed[cut:]) + ":" + base64.RawURLEncoding.EncodeToString(sealed[:cut])
}
