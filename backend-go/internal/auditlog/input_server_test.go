package auditlog

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
