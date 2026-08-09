package auditlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

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
	request.Header.Set(AuditInputSignatureHeader, SignAuditInput("test-secret", body))
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
	duplicate.Header.Set(AuditInputSignatureHeader, SignAuditInput("test-secret", body))
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
		{name: "remote", remoteAddr: "192.0.2.10:32100", signature: SignAuditInput("test-secret", body), wantStatus: http.StatusForbidden},
		{name: "signature", remoteAddr: "127.0.0.1:32100", signature: "v1=00", wantStatus: http.StatusUnauthorized},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, AuditInputPath, bytes.NewReader(body))
			request.RemoteAddr = testCase.remoteAddr
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(AuditInputSignatureHeader, testCase.signature)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != testCase.wantStatus {
				t.Fatalf("status=%d want=%d", response.Code, testCase.wantStatus)
			}
		})
	}
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
	for name, override := range map[string]string{
		"missing-secret":  "",
		"public-listener": "0.0.0.0:3303",
		"zero-port":       "127.0.0.1:0",
	} {
		t.Run(name, func(t *testing.T) {
			candidate := map[string]string{}
			for key, value := range valid {
				candidate[key] = value
			}
			if name == "missing-secret" {
				candidate["JUHE_AI_AUDIT_LOG_INPUT_SECRET"] = override
			} else {
				candidate["JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS"] = override
			}
			if _, err := LoadInputServerConfig(func(key string) string { return candidate[key] }); err == nil {
				t.Fatal("invalid input config must fail")
			}
		})
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

func TestRunInputServerLogsMaintenanceFailureAndKeepsServing(t *testing.T) {
	store := newLifecycleStore()
	store.cleanupErr = errors.New("retention fixture failure")
	loggerRecords := make(chan slog.Record, 4)
	logger := slog.New(recordSignalHandler{records: loggerRecords})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunInputServer(ctx, store, lifecycleConfig(), lifecycleInputConfig(), logger)
	}()
	select {
	case <-store.retentionCalls:
	case <-time.After(2 * time.Second):
		t.Fatal("RunInputServer did not attempt failing retention maintenance")
	}
	select {
	case record := <-loggerRecords:
		if record.Message != "F3 audit retention maintenance failed" || record.Level != slog.LevelError {
			t.Fatalf("maintenance failure was not observable as error: %+v", record)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("maintenance failure did not reach logger")
	}
	select {
	case err := <-done:
		t.Fatalf("ordinary maintenance failure must not stop input server: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunInputServer cancellation after maintenance failure failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunInputServer did not stop after maintenance failure cancellation")
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

type lifecycleStore struct {
	retentionCalls chan RetentionConfig
	released       chan struct{}
	cleanupErr     error
}

func newLifecycleStore() *lifecycleStore {
	return &lifecycleStore{retentionCalls: make(chan RetentionConfig, 1), released: make(chan struct{}, 1)}
}

func (s *lifecycleStore) EnsureSchema(context.Context) error { return nil }

func (s *lifecycleStore) AcquireOwnerLease(context.Context, string, time.Duration) (OwnerLease, bool, error) {
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
	return PersistResult{}, nil
}

func (s *lifecycleStore) CleanupRetention(_ context.Context, _ OwnerLease, config RetentionConfig) (RetentionResult, error) {
	select {
	case s.retentionCalls <- config:
	default:
	}
	return RetentionResult{}, s.cleanupErr
}

func (s *lifecycleStore) AppendHotSearch(context.Context, OwnerLease, []AuditLogInput) (int, error) {
	return 0, nil
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
