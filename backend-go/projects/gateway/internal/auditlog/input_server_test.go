package auditlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var auditInputTestNonce atomic.Uint64

func TestAuditInputHandlerPersistsSignedLoopbackInput(t *testing.T) {
	cfg := sqliteConfig(t, t.TempDir())
	store := openSQLiteStore(t, cfg)
	defer store.Close()
	lease := acquireLease(t, store)
	handler := &auditInputHandler{
		store: store,
		lease: lease,
		cfg:   InputServerConfig{SharedSecret: "test-secret", MaxBytes: defaultInputMaxBytes, RequestTimeout: time.Second},
	}
	body, err := json.Marshal(auditInputEnvelope{SchemaVersion: 1, AuditLog: fixture("input-loopback", LifecycleFinalized)})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, AuditInputPath, bytes.NewReader(body))
	request.RemoteAddr = "127.0.0.1:32100"
	request.Header.Set("Content-Type", "application/json")
	signAuditRequest(request, "test-secret", body, "input-loopback")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("signed loopback input status=%d", response.Code)
	}
	implementation := store.(*sqlStore)
	var count int
	if err := implementation.db.QueryRowContext(context.Background(), `SELECT count(*) FROM audit_logs WHERE id=?`, "input-loopback").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("persisted audit count=%d want=1", count)
	}
	search, err := store.SearchHotSearch(context.Background(), HotSearchOptions{
		Keywords: []string{"input-loopback"},
		StartAt:  time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC),
		EndAt:    time.Date(2026, 8, 9, 13, 0, 0, 0, time.UTC),
	})
	if err != nil || len(search.AuditLogIDs) != 1 || search.AuditLogIDs[0] != "input-loopback" {
		t.Fatalf("persisted input hot-search append mismatch: result=%+v err=%v", search, err)
	}

	duplicate := httptest.NewRequest(http.MethodPost, AuditInputPath, bytes.NewReader(body))
	duplicate.RemoteAddr = "127.0.0.1:32100"
	duplicate.Header.Set("Content-Type", "application/json")
	signAuditRequest(duplicate, "test-secret", body, "input-loopback-duplicate")
	duplicateResponse := httptest.NewRecorder()
	handler.ServeHTTP(duplicateResponse, duplicate)
	if duplicateResponse.Code != http.StatusNoContent {
		t.Fatalf("duplicate signed loopback input status=%d", duplicateResponse.Code)
	}
	entries, err := os.ReadDir(implementation.hotDir)
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, entry := range entries {
		content, err := os.ReadFile(implementation.hotDir + string(os.PathSeparator) + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		lines += strings.Count(string(content), `"auditLogId":"input-loopback"`)
	}
	if lines != 1 {
		t.Fatalf("idempotent input must append one hot-search line, got %d", lines)
	}
}

func TestAuditInputHandlerRejectsInvalidAbsoluteTime(t *testing.T) {
	handler := &auditInputHandler{cfg: InputServerConfig{SharedSecret: "test-secret", MaxBytes: defaultInputMaxBytes, RequestTimeout: time.Second}}
	input := fixture("input-invalid-time", LifecycleFinalized)
	input.CreatedAt = "2026-08-09T12:00:00"
	body, err := json.Marshal(auditInputEnvelope{SchemaVersion: 1, AuditLog: input})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, AuditInputPath, bytes.NewReader(body))
	request.RemoteAddr = "127.0.0.1:32100"
	request.Header.Set("Content-Type", "application/json")
	signAuditRequest(request, "test-secret", body, "input-invalid-time")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid absolute time status=%d, want 400", response.Code)
	}
}

func TestAuditInputHandlerPersistsAfterClientContextCancellation(t *testing.T) {
	cfg := sqliteConfig(t, t.TempDir())
	store := openSQLiteStore(t, cfg)
	defer store.Close()
	handler := &auditInputHandler{
		store: store,
		lease: acquireLease(t, store),
		cfg:   InputServerConfig{SharedSecret: "test-secret", MaxBytes: defaultInputMaxBytes, RequestTimeout: time.Second},
	}
	body, err := json.Marshal(auditInputEnvelope{SchemaVersion: 1, AuditLog: fixture("input-client-cancelled", LifecycleFinalized)})
	if err != nil {
		t.Fatal(err)
	}
	clientCtx, cancelClient := context.WithCancel(context.Background())
	cancelClient()
	request := httptest.NewRequest(http.MethodPost, AuditInputPath, bytes.NewReader(body)).WithContext(clientCtx)
	request.RemoteAddr = "127.0.0.1:32100"
	request.Header.Set("Content-Type", "application/json")
	signAuditRequest(request, "test-secret", body, "input-client-cancelled")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("cancelled client context input status=%d", response.Code)
	}
	var count int
	if err := store.(*sqlStore).db.QueryRowContext(context.Background(), `SELECT count(*) FROM audit_logs WHERE id=?`, "input-client-cancelled").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("persisted audit logs=%d want 1", count)
	}
}

func TestAuditInputHandlerContainsPersistPanic(t *testing.T) {
	store := newLifecycleStore()
	store.persistPanic = true
	healthy := &atomic.Bool{}
	healthy.Store(true)
	componentFatal := make(chan error, 1)
	handler := &auditInputHandler{
		store:          store,
		lease:          OwnerLease{OwnerID: "lifecycle-test-owner", FenceToken: 1},
		cfg:            InputServerConfig{SharedSecret: "test-secret", MaxBytes: defaultInputMaxBytes, RequestTimeout: time.Second},
		logger:         slog.New(recordSignalHandler{records: make(chan slog.Record, 1)}),
		healthy:        healthy,
		componentFatal: componentFatal,
	}
	body, err := json.Marshal(auditInputEnvelope{SchemaVersion: 1, AuditLog: fixture("input-panic", LifecycleFinalized)})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, AuditInputPath, bytes.NewReader(body))
	request.RemoteAddr = "127.0.0.1:32100"
	request.Header.Set("Content-Type", "application/json")
	signAuditRequest(request, "test-secret", body, "input-panic")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("persist panic status=%d want=%d", response.Code, http.StatusInternalServerError)
	}
	if !healthy.Load() {
		t.Fatal("persist panic must stay contained to the request")
	}
	select {
	case err := <-componentFatal:
		t.Fatalf("persist panic must not restart the component: %v", err)
	default:
	}
}

func TestAuditInputHandlerContainsPersistFailure(t *testing.T) {
	store := newLifecycleStore()
	store.persistErr = errors.New("persist fixture failure")
	healthy := &atomic.Bool{}
	healthy.Store(true)
	componentFatal := make(chan error, 1)
	handler := &auditInputHandler{
		store:          store,
		lease:          OwnerLease{OwnerID: "lifecycle-test-owner", FenceToken: 1},
		cfg:            InputServerConfig{SharedSecret: "test-secret", MaxBytes: defaultInputMaxBytes, RequestTimeout: time.Second},
		healthy:        healthy,
		componentFatal: componentFatal,
	}
	body, err := json.Marshal(auditInputEnvelope{SchemaVersion: 1, AuditLog: fixture("input-persist-failure", LifecycleFinalized)})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, AuditInputPath, bytes.NewReader(body))
	request.RemoteAddr = "127.0.0.1:32100"
	request.Header.Set("Content-Type", "application/json")
	signAuditRequest(request, "test-secret", body, "input-persist-failure")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("persist failure status=%d want=%d", response.Code, http.StatusInternalServerError)
	}
	if !healthy.Load() {
		t.Fatal("persist failure must stay contained to the request")
	}
	select {
	case componentErr := <-componentFatal:
		t.Fatalf("persist failure must not restart the component: %v", componentErr)
	default:
	}
}

func TestAuditInputHandlerReportsLeaseLossToSupervisor(t *testing.T) {
	store := newLifecycleStore()
	store.persistErr = ErrOwnerLeaseLost
	healthy := &atomic.Bool{}
	healthy.Store(true)
	componentFatal := make(chan error, 1)
	handler := &auditInputHandler{
		store:          store,
		lease:          OwnerLease{OwnerID: "lifecycle-test-owner", FenceToken: 1},
		cfg:            InputServerConfig{SharedSecret: "test-secret", MaxBytes: defaultInputMaxBytes, RequestTimeout: time.Second},
		healthy:        healthy,
		componentFatal: componentFatal,
	}
	body, err := json.Marshal(auditInputEnvelope{SchemaVersion: 1, AuditLog: fixture("input-lease-loss", LifecycleFinalized)})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, AuditInputPath, bytes.NewReader(body))
	request.RemoteAddr = "127.0.0.1:32100"
	request.Header.Set("Content-Type", "application/json")
	signAuditRequest(request, "test-secret", body, "input-lease-loss")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("lease loss status=%d want=%d", response.Code, http.StatusServiceUnavailable)
	}
	if healthy.Load() {
		t.Fatal("lease loss must downgrade F3 health before the supervisor restart")
	}
	select {
	case componentErr := <-componentFatal:
		if !errors.Is(componentErr, ErrOwnerLeaseLost) {
			t.Fatalf("lease loss component error=%v", componentErr)
		}
	case <-time.After(time.Second):
		t.Fatal("lease loss did not report a component failure")
	}
}

func TestAuditInputHandlerContainsHotSearchFailureAfterCommit(t *testing.T) {
	store := newLifecycleStore()
	store.hotSearchErr = errors.New("hot-search fixture failure")
	healthy := &atomic.Bool{}
	healthy.Store(true)
	componentFatal := make(chan error, 1)
	handler := &auditInputHandler{
		store:          store,
		lease:          OwnerLease{OwnerID: "lifecycle-test-owner", FenceToken: 1},
		cfg:            InputServerConfig{SharedSecret: "test-secret", MaxBytes: defaultInputMaxBytes, RequestTimeout: time.Second},
		healthy:        healthy,
		componentFatal: componentFatal,
	}
	body, err := json.Marshal(auditInputEnvelope{SchemaVersion: 1, AuditLog: fixture("input-hot-search-failure", LifecycleFinalized)})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, AuditInputPath, bytes.NewReader(body))
	request.RemoteAddr = "127.0.0.1:32100"
	request.Header.Set("Content-Type", "application/json")
	signAuditRequest(request, "test-secret", body, "input-hot-search-failure")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("durable audit commit with hot-search failure status=%d want=%d", response.Code, http.StatusNoContent)
	}
	if !healthy.Load() {
		t.Fatal("hot-search failure must stay contained to the request")
	}
	select {
	case componentErr := <-componentFatal:
		t.Fatalf("hot-search failure must not restart the component: %v", componentErr)
	default:
	}
}

func TestRunInputServerKeepsServingAfterPersistFailure(t *testing.T) {
	store := newLifecycleStore()
	store.persistErr = errors.New("input server persist fixture failure")
	inputConfig := lifecycleInputConfigAt(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunInputServer(ctx, store, lifecycleConfig(), inputConfig, nil)
	}()
	select {
	case <-store.acquired:
	case <-time.After(time.Second):
		t.Fatal("RunInputServer did not acquire the test owner lease")
	}
	body, err := json.Marshal(auditInputEnvelope{SchemaVersion: 1, AuditLog: fixture("input-server-persist-failure", LifecycleFinalized)})
	if err != nil {
		t.Fatal(err)
	}
	response := eventuallySendInput(t, inputConfig, body)
	defer response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("persist failure HTTP status=%d want=%d", response.StatusCode, http.StatusInternalServerError)
	}
	response = eventuallySendInput(t, inputConfig, body)
	defer response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("second persist failure HTTP status=%d want=%d", response.StatusCode, http.StatusInternalServerError)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunInputServer must remain alive until shutdown, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunInputServer did not stop after cancellation")
	}
}

func eventuallySendInput(t *testing.T, inputConfig InputServerConfig, body []byte) *http.Response {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	client := &http.Client{Timeout: 100 * time.Millisecond}
	for {
		request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+inputConfig.ListenAddress+AuditInputPath, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		nonce := "input-server-persist-failure-" + strconv.FormatUint(auditInputTestNonce.Add(1), 10)
		signAuditRequest(request, inputConfig.SharedSecret, body, nonce)
		response, err := client.Do(request)
		if err == nil {
			return response
		}
		if time.Now().After(deadline) {
			t.Fatalf("F3 input server did not become reachable: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAuditInputHandlerRejectsUnsignedOrNonLoopbackInput(t *testing.T) {
	cfg := sqliteConfig(t, t.TempDir())
	store := openSQLiteStore(t, cfg)
	defer store.Close()
	handler := &auditInputHandler{
		store: store,
		lease: acquireLease(t, store),
		cfg:   InputServerConfig{SharedSecret: "test-secret", MaxBytes: defaultInputMaxBytes, RequestTimeout: time.Second},
	}
	body, err := json.Marshal(auditInputEnvelope{SchemaVersion: 1, AuditLog: fixture("input-reject", LifecycleFinalized)})
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name       string
		remoteAddr string
		signature  string
		wantStatus int
	}{
		{name: "remote", remoteAddr: "192.0.2.10:32100", signature: SignAuditInput("test-secret", time.Now().UTC().Format(time.RFC3339Nano), "input-reject-remote", body), wantStatus: http.StatusForbidden},
		{name: "signature", remoteAddr: "127.0.0.1:32100", signature: "v1=00", wantStatus: http.StatusUnauthorized},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, AuditInputPath, bytes.NewReader(body))
			request.RemoteAddr = testCase.remoteAddr
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(AuditInputTimestampHeader, time.Now().UTC().Format(time.RFC3339Nano))
			request.Header.Set(AuditInputNonceHeader, "input-reject-"+testCase.name)
			request.Header.Set(AuditInputSignatureHeader, testCase.signature)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != testCase.wantStatus {
				t.Fatalf("status=%d want=%d", response.Code, testCase.wantStatus)
			}
		})
	}
}

func TestAuditInputHandlerEnforcesTimestampNonceSignatureContract(t *testing.T) {
	cfg := sqliteConfig(t, t.TempDir())
	store := openSQLiteStore(t, cfg)
	defer store.Close()
	handler := &auditInputHandler{
		store: store,
		lease: acquireLease(t, store),
		cfg: InputServerConfig{
			SharedSecret: "test-secret", MaxBytes: defaultInputMaxBytes, RequestTimeout: time.Second,
			ReplayWindow: time.Minute, ReplayCacheCapacity: 8,
		},
	}
	body, err := json.Marshal(auditInputEnvelope{SchemaVersion: 1, AuditLog: fixture("input-contract", LifecycleFinalized)})
	if err != nil {
		t.Fatal(err)
	}
	request := func(timestamp, nonce, signature string, requestBody []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, AuditInputPath, bytes.NewReader(requestBody))
		req.RemoteAddr = "127.0.0.1:32100"
		req.Header.Set("Content-Type", "application/json")
		if timestamp != "" {
			req.Header.Set(AuditInputTimestampHeader, timestamp)
		}
		if nonce != "" {
			req.Header.Set(AuditInputNonceHeader, nonce)
		}
		if signature != "" {
			req.Header.Set(AuditInputSignatureHeader, signature)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	now := time.Now().UTC()
	sign := func(timestamp time.Time, nonce string, requestBody []byte) string {
		return SignAuditInput("test-secret", timestamp.Format(time.RFC3339Nano), nonce, requestBody)
	}
	validTimestamp := now.Format(time.RFC3339Nano)
	validSignature := sign(now, "contract-valid", body)
	if response := request(validTimestamp, "contract-valid", validSignature, body); response.Code != http.StatusNoContent {
		t.Fatalf("valid request status=%d want=%d", response.Code, http.StatusNoContent)
	}
	for _, testCase := range []struct {
		name      string
		timestamp string
		nonce     string
		signature string
		body      []byte
	}{
		{name: "missing-timestamp", nonce: "missing-timestamp", signature: SignAuditInput("test-secret", "", "missing-timestamp", body), body: body},
		{name: "missing-nonce", timestamp: validTimestamp, signature: SignAuditInput("test-secret", validTimestamp, "", body), body: body},
		{name: "expired", timestamp: now.Add(-2 * time.Minute).Format(time.RFC3339Nano), nonce: "expired", signature: sign(now.Add(-2*time.Minute), "expired", body), body: body},
		{name: "future", timestamp: now.Add(2 * time.Minute).Format(time.RFC3339Nano), nonce: "future", signature: sign(now.Add(2*time.Minute), "future", body), body: body},
		{name: "tampered", timestamp: validTimestamp, nonce: "tampered", signature: validSignature, body: bytes.Replace(body, []byte("input-contract"), []byte("input-tampered"), 1)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if response := request(testCase.timestamp, testCase.nonce, testCase.signature, testCase.body); response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d want=%d", response.Code, http.StatusUnauthorized)
			}
		})
	}
	if response := request(validTimestamp, "contract-replay", sign(now, "contract-replay", body), body); response.Code != http.StatusNoContent {
		t.Fatalf("first nonce status=%d want=%d", response.Code, http.StatusNoContent)
	}
	if response := request(validTimestamp, "contract-replay", sign(now, "contract-replay", body), body); response.Code != http.StatusUnauthorized {
		t.Fatalf("replayed nonce status=%d want=%d", response.Code, http.StatusUnauthorized)
	}
	if response := request(validTimestamp, "contract-different-nonce", sign(now, "contract-different-nonce", body), body); response.Code != http.StatusNoContent {
		t.Fatalf("different nonce status=%d want=%d", response.Code, http.StatusNoContent)
	}
}

func TestAuditInputReplayCacheIsBoundedByCapacityAndWindow(t *testing.T) {
	var cache replayCache
	now := time.Now().UTC()
	if !cache.accept("one", now, time.Minute, 2) || !cache.accept("two", now, time.Minute, 2) {
		t.Fatal("initial nonces must be accepted")
	}
	if cache.accept("three", now, time.Minute, 2) {
		t.Fatal("replay cache must reject entries beyond capacity")
	}
	if len(cache.nonces) != 2 {
		t.Fatalf("replay cache size=%d want=2", len(cache.nonces))
	}
	if !cache.accept("expired", now.Add(time.Minute), time.Minute, 2) {
		t.Fatal("expired entries must be evicted before accepting a new nonce")
	}
}

func signAuditRequest(request *http.Request, secret string, body []byte, nonce string) {
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(AuditInputTimestampHeader, timestamp)
	request.Header.Set(AuditInputNonceHeader, nonce)
	request.Header.Set(AuditInputSignatureHeader, SignAuditInput(secret, timestamp, nonce, body))
}

func TestLoadInputServerConfigRequiresLoopbackAndSecret(t *testing.T) {
	valid := map[string]string{
		"JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS": "127.0.0.1:3303",
		"JUHE_AI_AUDIT_LOG_INPUT_SECRET":         "secret",
	}
	config, err := LoadInputServerConfig(func(name string) string { return valid[name] })
	if err != nil || config.ListenAddress != "127.0.0.1:3303" {
		t.Fatalf("valid input config: cfg=%+v err=%v", config, err)
	}
	for _, testCase := range []struct {
		name        string
		environment map[string]string
		wantError   string
	}{
		{name: "missing-secret", environment: map[string]string{"NODE_ENV": "production", "JUHE_AI_AUDIT_LOG_INPUT_SECRET": ""}, wantError: "JUHE_AI_AUDIT_LOG_INPUT_SECRET"},
		{name: "blank-secret", environment: map[string]string{"NODE_ENV": "production", "JUHE_AI_AUDIT_LOG_INPUT_SECRET": "   "}, wantError: "JUHE_AI_AUDIT_LOG_INPUT_SECRET"},
		{name: "does-not-fallback-to-business-secret", environment: map[string]string{"NODE_ENV": "production", "JUHE_AI_AUDIT_LOG_INPUT_SECRET": "", "JUHE_AI_SECRET": strings.Repeat("x", minimumProductionInputSecretLen)}, wantError: "JUHE_AI_AUDIT_LOG_INPUT_SECRET"},
		{name: "production-short-secret", environment: map[string]string{"NODE_ENV": "production", "JUHE_AI_AUDIT_LOG_INPUT_SECRET": "short"}, wantError: "至少 32 位"},
		{name: "public-listener", environment: map[string]string{"JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS": "0.0.0.0:3303"}, wantError: "JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS"},
		{name: "zero-port", environment: map[string]string{"JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS": "127.0.0.1:0"}, wantError: "JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := map[string]string{}
			for key, value := range valid {
				candidate[key] = value
			}
			for key, value := range testCase.environment {
				candidate[key] = value
			}
			_, err := LoadInputServerConfig(func(key string) string { return candidate[key] })
			if err == nil {
				t.Fatal("invalid input config must fail")
			}
			if !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("error=%q must contain %q", err, testCase.wantError)
			}
		})
	}

	production := map[string]string{
		"NODE_ENV":                               "production",
		"JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS": "127.0.0.1:3303",
		"JUHE_AI_AUDIT_LOG_INPUT_SECRET":         strings.Repeat("x", minimumProductionInputSecretLen),
	}
	if _, err := LoadInputServerConfig(func(key string) string { return production[key] }); err != nil {
		t.Fatalf("production input config with a 32-byte secret must succeed: %v", err)
	}
}

func TestRunInputServerRunsRetentionAndStopsWithContext(t *testing.T) {
	store := newLifecycleStore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunInputServer(ctx, store, lifecycleConfig(), lifecycleInputConfig(), nil)
	}()
	var retention RetentionConfig
	select {
	case retention = <-store.retentionCalls:
	case <-time.After(2 * time.Second):
		t.Fatal("RunInputServer did not start retention maintenance")
	}
	if retention.SuccessSampleBucketThreshold != 1000 || retention.BatchSize != 17 || retention.SuccessHotCutoff.Sub(retention.SuccessCutoff) != 71*time.Hour || !retention.FailureCutoff.Equal(retention.ErrorGroupCutoff) {
		t.Fatalf("maintenance retention policy mismatch: %+v", retention)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunInputServer cancellation failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunInputServer did not stop after context cancellation")
	}
	select {
	case <-store.released:
	case <-time.After(time.Second):
		t.Fatal("RunInputServer did not release owner lease after cancellation")
	}
}

func TestRunInputServerReturnsMaintenanceFailureAsComponentError(t *testing.T) {
	store := newLifecycleStore()
	store.cleanupErr = errors.New("retention fixture failure")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunInputServer(ctx, store, lifecycleConfig(), lifecycleInputConfig(), nil)
	}()
	select {
	case <-store.retentionCalls:
	case <-time.After(2 * time.Second):
		t.Fatal("RunInputServer did not attempt failing retention maintenance")
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "retention fixture failure") {
			t.Fatalf("maintenance failure must return a component error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunInputServer did not return after maintenance failure")
	}
}

func TestRetentionFailureDowngradesHealthBeforeReportingFatal(t *testing.T) {
	store := newLifecycleStore()
	store.cleanupErr = errors.New("retention health fixture failure")
	healthy := &atomic.Bool{}
	healthy.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fatal := make(chan error, 1)
	done := runRetentionMaintenance(ctx, store, OwnerLease{OwnerID: "lifecycle-test-owner", FenceToken: 1}, lifecycleConfig(), nil, healthy, fatal)
	select {
	case <-store.retentionCalls:
	case <-time.After(2 * time.Second):
		t.Fatal("retention maintenance did not execute")
	}
	select {
	case err := <-fatal:
		if !strings.Contains(err.Error(), "retention health fixture failure") {
			t.Fatalf("retention fatal=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("retention failure did not report component fatal")
	}
	if healthy.Load() {
		t.Fatal("retention failure must downgrade F3 health before fatal handling")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retention maintenance did not stop after a fatal failure")
	}
}

func TestRunInputServerReturnsRetentionPanicAsComponentError(t *testing.T) {
	store := newLifecycleStore()
	store.cleanupPanic = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunInputServer(ctx, store, lifecycleConfig(), lifecycleInputConfig(), nil)
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "maintenance goroutine panic") || !strings.Contains(err.Error(), "retention fixture panic") {
			t.Fatalf("retention panic must return a component error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunInputServer did not return after retention panic")
	}
}

func lifecycleConfig() Config {
	return Config{
		InstanceID:               "lifecycle-test-owner",
		OwnerLease:               5 * time.Second,
		RetentionInterval:        time.Second,
		RetentionBatchSize:       17,
		SuccessHotRetentionHours: 1,
		SuccessSampleRate:        0.1,
		SuccessRetentionDays:     3,
		ProblemRetentionDays:     7,
	}
}

func lifecycleInputConfig() InputServerConfig {
	return InputServerConfig{ListenAddress: "127.0.0.1:0", SharedSecret: "lifecycle-secret", MaxBytes: defaultInputMaxBytes, RequestTimeout: time.Second}
}

func lifecycleInputConfigAt(t *testing.T) InputServerConfig {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return InputServerConfig{ListenAddress: address, SharedSecret: "lifecycle-secret", MaxBytes: defaultInputMaxBytes, RequestTimeout: time.Second}
}

type lifecycleStore struct {
	acquired       chan struct{}
	retentionCalls chan RetentionConfig
	released       chan struct{}
	cleanupErr     error
	cleanupPanic   bool
	persistPanic   bool
	persistErr     error
	hotSearchErr   error
}

func newLifecycleStore() *lifecycleStore {
	return &lifecycleStore{acquired: make(chan struct{}, 1), retentionCalls: make(chan RetentionConfig, 1), released: make(chan struct{}, 1)}
}

func (s *lifecycleStore) EnsureSchema(context.Context) error { return nil }

func (s *lifecycleStore) AcquireOwnerLease(context.Context, string, time.Duration) (OwnerLease, bool, error) {
	select {
	case s.acquired <- struct{}{}:
	default:
	}
	return OwnerLease{OwnerID: "lifecycle-test-owner", FenceToken: 1}, true, nil
}

func (s *lifecycleStore) RenewOwnerLease(context.Context, OwnerLease, time.Duration) (bool, error) {
	return true, nil
}

func (s *lifecycleStore) ReleaseOwnerLease(context.Context, OwnerLease) error {
	select {
	case s.released <- struct{}{}:
	default:
	}
	return nil
}

func (s *lifecycleStore) CleanupOwnedBlobTemps(context.Context, OwnerLease, time.Time) error {
	return nil
}

func (s *lifecycleStore) CleanupOrphanedBlobTemps(context.Context, OwnerLease, time.Time) error {
	return nil
}

func (s *lifecycleStore) Persist(context.Context, OwnerLease, AuditLogInput) (PersistResult, error) {
	if s.persistPanic {
		panic("persist fixture panic")
	}
	return PersistResult{}, s.persistErr
}

func (s *lifecycleStore) CleanupRetention(_ context.Context, _ OwnerLease, config RetentionConfig) (RetentionResult, error) {
	select {
	case s.retentionCalls <- config:
	default:
	}
	if s.cleanupPanic {
		panic("retention fixture panic")
	}
	return RetentionResult{}, s.cleanupErr
}

func (s *lifecycleStore) AppendHotSearch(context.Context, OwnerLease, []AuditLogInput) (int, error) {
	return 0, s.hotSearchErr
}

func (s *lifecycleStore) CleanupHotSearch(context.Context, OwnerLease, time.Time, int) (int64, error) {
	return 0, nil
}

func (s *lifecycleStore) SearchHotSearch(context.Context, HotSearchOptions) (HotSearchResult, error) {
	return HotSearchResult{}, nil
}

func (s *lifecycleStore) Close() error { return nil }

type recordSignalHandler struct{ records chan<- slog.Record }

func (recordSignalHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h recordSignalHandler) Handle(_ context.Context, record slog.Record) error {
	select {
	case h.records <- record.Clone():
	default:
	}
	return nil
}

func (h recordSignalHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h recordSignalHandler) WithGroup(string) slog.Handler { return h }
