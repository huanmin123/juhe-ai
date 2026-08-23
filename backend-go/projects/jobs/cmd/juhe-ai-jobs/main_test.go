package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

func TestMatchesAccountBalanceManualSecret(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	request := httptest.NewRequest(http.MethodPost, "/account-balance/manual", nil)
	if matchesAccountBalanceManualSecret(request, secret) {
		t.Fatal("manual bridge must reject a missing bearer secret")
	}
	request.Header.Set("Authorization", "Bearer wrong")
	if matchesAccountBalanceManualSecret(request, secret) {
		t.Fatal("manual bridge must reject a wrong bearer secret")
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	if !matchesAccountBalanceManualSecret(request, secret) {
		t.Fatal("manual bridge must accept its configured bearer secret")
	}
}

func TestPassiveJobsHealthNeverClaimsOwnerReadiness(t *testing.T) {
	handler := passiveJobsHealthHandler(ownermode.Standby)
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", record.Code, record.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(record.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ready"] != false || payload["ownerReady"] != false || payload["ownerMode"] != "standby" || payload["accountBalanceReady"] != false {
		t.Fatalf("passive health claimed owner readiness: %#v", payload)
	}
}

func TestHealthWiresProxyLatencyStatus(t *testing.T) {
	var running atomic.Bool
	running.Store(true)
	now := time.Date(2026, 8, 21, 15, 0, 0, 0, time.UTC)
	status := func() proxylatency.RunnerStatus {
		return proxylatency.RunnerStatus{OwnerHeld: true, LastCycleAt: now, LastSuccess: now, Inputs: 2, Executed: 2}
	}
	handler := healthHandler(ownermode.Active, &running, func() bool { return true }, false, func() bool { return true }, false, func() bool { return true }, true, func() bool { return true }, status)
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", record.Code, record.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(record.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["proxyLatencyEnabled"] != true || payload["proxyLatencyOwnerHeld"] != true || payload["proxyLatencyExecuted"] != float64(2) || payload["proxyLatencyLastError"] != "" {
		t.Fatalf("missing proxy latency health fields: %#v", payload)
	}
}

func TestHealthKeepsProxyLatencyFailureVisible(t *testing.T) {
	running := atomic.Bool{}
	running.Store(true)
	now := time.Date(2026, 8, 21, 15, 0, 0, 0, time.UTC)
	status := func() proxylatency.RunnerStatus {
		return proxylatency.RunnerStatus{OwnerHeld: false, LastCycleAt: now, LastError: "owner lease lost"}
	}
	handler := healthHandler(ownermode.Active, &running, func() bool { return true }, false, func() bool { return true }, false, func() bool { return true }, true, func() bool { return false }, status)
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	var payload map[string]any
	if err := json.Unmarshal(record.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ready"] != false || payload["proxyLatencyReady"] != false || payload["proxyLatencyOwnerHeld"] != false || payload["proxyLatencyLastError"] != "owner lease lost" {
		t.Fatalf("failed proxy latency state was not exposed: %#v", payload)
	}
}

func TestHealthDisabledProxyLatencyDoesNotBlock(t *testing.T) {
	running := atomic.Bool{}
	running.Store(true)
	handler := healthHandler(ownermode.Active, &running, func() bool { return true }, false, func() bool { return true }, false, func() bool { return true }, false, func() bool { return false }, func() proxylatency.RunnerStatus {
		return proxylatency.RunnerStatus{}
	})
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	var payload map[string]any
	if err := json.Unmarshal(record.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ready"] != true || payload["proxyLatencyEnabled"] != false || payload["proxyLatencyReady"] != true {
		t.Fatalf("disabled proxy latency unexpectedly blocked health: %#v", payload)
	}
}

func TestHealthUsesAtomicProxyLatencySnapshot(t *testing.T) {
	running := atomic.Bool{}
	running.Store(true)
	now := time.Date(2026, 8, 21, 15, 0, 0, 0, time.UTC)
	status := func() proxylatency.RunnerStatus {
		return proxylatency.RunnerStatus{OwnerHeld: false, LastCycleAt: now, LastError: "owner lease lost"}
	}
	// The legacy callbacks intentionally disagree. The snapshot callback is the
	// authoritative, atomic pair used by the production Runner wiring.
	snapshot := func() (proxylatency.RunnerStatus, bool) {
		return status(), false
	}
	handler := healthHandler(ownermode.Active, &running, func() bool { return true }, false, func() bool { return true }, false, func() bool { return true }, true, func() bool { return true }, status, snapshot)
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	var payload map[string]any
	if err := json.Unmarshal(record.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ready"] != false || payload["proxyLatencyReady"] != false || payload["proxyLatencyOwnerHeld"] != false || payload["proxyLatencyLastError"] != "owner lease lost" {
		t.Fatalf("health did not use atomic proxy snapshot: %#v", payload)
	}
}

func TestJobsHTTPHandlerWiresProxyLatencyManualBridge(t *testing.T) {
	proxyServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	defer proxyServer.Close()
	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	port := 0
	if _, err := fmt.Sscanf(proxyURL.Port(), "%d", &port); err != nil {
		t.Fatal(err)
	}
	cfg := proxylatency.RuntimeConfig{
		Enabled:          true,
		InstanceID:       "j3a-manual-test",
		Store:            proxylatency.StoreConfig{Mode: proxylatency.StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "jobs.sqlite")},
		OwnerLease:       10 * time.Second,
		ProxyLease:       5 * time.Second,
		ProbeTimeout:     2 * time.Second,
		ManualEnabled:    true,
		ManualHTTPSecret: "0123456789abcdef0123456789abcdef",
		ManualDeadline:   2 * time.Second,
		CredentialSecret: "test-secret",
		Now:              time.Now,
	}
	store, err := proxylatency.OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := proxylatency.NewRunner(cfg, store, nil, nil)
	var running atomic.Bool
	running.Store(true)
	handler := jobsHTTPHandler(ownermode.Active, &running, func() bool { return true }, false, func() bool { return true }, false, func() bool { return true }, nil, "", true, func() bool { return false }, func() proxylatency.RunnerStatus { return runner.Status() }, func() (proxylatency.RunnerStatus, bool) { return runner.Snapshot() }, true, cfg.ManualHTTPSecret, runner)
	body := `{"input":{"schema_version":1,"proxy_id":"proxy-manual","proxy_name":"Manual proxy","config_revision":"2026-08-23T00:00:00.123Z","proxy_type":"http","proxy_host":"` + proxyURL.Hostname() + `","proxy_port":` + fmt.Sprint(port) + `,"targets":[{"provider":"openai","profile_id":"profile-openai","name":"OpenAI","url":"http://provider.invalid/"}],"deadline_ms":1000}}`
	request := httptest.NewRequest(http.MethodPost, "/proxy-latency/manual", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+cfg.ManualHTTPSecret)
	request.Header.Set("Content-Type", "application/json")
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("manual bridge status=%d body=%s", record.Code, record.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(record.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["schemaVersion"] != float64(1) || payload["job"] != "proxy-latency" {
		t.Fatalf("manual bridge envelope mismatch: %#v", payload)
	}
	report, ok := payload["report"].(map[string]any)
	if !ok || report["proxyId"] != "proxy-manual" {
		t.Fatalf("manual bridge report mismatch: %#v", payload)
	}
	noProviderRequest := httptest.NewRequest(http.MethodPost, "/proxy-latency/manual", strings.NewReader(`{"input":{"schema_version":1,"proxy_id":"proxy-no-provider","proxy_name":"No provider","config_revision":"2026-08-23T00:00:00.123Z","proxy_type":"http","proxy_host":"127.0.0.1","proxy_port":8080,"targets":[]}}`))
	noProviderRequest.Header.Set("Authorization", "Bearer "+cfg.ManualHTTPSecret)
	noProviderRequest.Header.Set("Content-Type", "application/json")
	noProviderRecord := httptest.NewRecorder()
	handler.ServeHTTP(noProviderRecord, noProviderRequest)
	if noProviderRecord.Code != http.StatusOK {
		t.Fatalf("no-provider manual bridge status=%d body=%s", noProviderRecord.Code, noProviderRecord.Body.String())
	}
	var noProviderPayload map[string]any
	if err := json.Unmarshal(noProviderRecord.Body.Bytes(), &noProviderPayload); err != nil {
		t.Fatal(err)
	}
	noProviderReport, ok := noProviderPayload["report"].(map[string]any)
	if !ok || noProviderReport["status"] != "unknown" || noProviderReport["message"] != "代理检测未形成有效传输尝试" {
		t.Fatalf("no-provider manual report mismatch: %#v", noProviderPayload)
	}
}

func TestJobsHTTPHandlerRejectsProxyLatencyManualInvalidBoundary(t *testing.T) {
	var running atomic.Bool
	running.Store(true)
	handler := jobsHTTPHandler(ownermode.Active, &running, func() bool { return true }, false, func() bool { return true }, false, func() bool { return true }, nil, "", true, func() bool { return false }, func() proxylatency.RunnerStatus { return proxylatency.RunnerStatus{} }, func() (proxylatency.RunnerStatus, bool) { return proxylatency.RunnerStatus{}, false }, true, "0123456789abcdef0123456789abcdef", nil)
	for _, test := range []struct {
		name    string
		request *http.Request
		want    int
	}{
		{name: "missing runner returns 404", request: httptest.NewRequest(http.MethodPost, "/proxy-latency/manual", strings.NewReader(`{"input":{}}`)), want: http.StatusNotFound},
		{name: "wrong method returns 404", request: httptest.NewRequest(http.MethodGet, "/proxy-latency/manual", nil), want: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := httptest.NewRecorder()
			handler.ServeHTTP(record, test.request)
			if record.Code != test.want {
				t.Fatalf("status=%d body=%s want=%d", record.Code, record.Body.String(), test.want)
			}
		})
	}
}
