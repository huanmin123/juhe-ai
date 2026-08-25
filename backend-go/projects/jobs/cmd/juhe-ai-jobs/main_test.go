package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
	_ "modernc.org/sqlite"
)

func TestValidateLoopbackListenAddress(t *testing.T) {
	tests := []struct {
		name    string
		address string
		valid   bool
	}{
		{name: "ipv4 loopback", address: "127.0.0.1:3305", valid: true},
		{name: "ipv4 loopback range", address: "127.255.255.254:65535", valid: true},
		{name: "localhost", address: "localhost:3305", valid: true},
		{name: "localhost case insensitive", address: "LOCALHOST:3305", valid: true},
		{name: "ipv6 loopback", address: "[::1]:3305", valid: true},
		{name: "wildcard ipv4", address: "0.0.0.0:3305", valid: false},
		{name: "wildcard ipv6", address: "[::]:3305", valid: false},
		{name: "remote ipv4", address: "192.168.1.10:3305", valid: false},
		{name: "missing port ipv4", address: "127.0.0.1", valid: false},
		{name: "missing port localhost", address: "localhost", valid: false},
		{name: "missing host", address: ":3305", valid: false},
		{name: "invalid port", address: "localhost:not-a-port", valid: false},
		{name: "zero port", address: "localhost:0", valid: false},
		{name: "port out of range", address: "localhost:65536", valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateLoopbackListenAddress(test.address)
			if test.valid && err != nil {
				t.Fatalf("validateLoopbackListenAddress(%q) returned error: %v", test.address, err)
			}
			if !test.valid && err == nil {
				t.Fatalf("validateLoopbackListenAddress(%q) accepted invalid address", test.address)
			}
		})
	}
}

func TestListenLoopbackUsesValidatedAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err = listenLoopback(address)
	if err != nil {
		t.Fatalf("listenLoopback(%q) returned error: %v", address, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := listenLoopback("0.0.0.0:3305"); err == nil {
		t.Fatal("listenLoopback accepted wildcard address")
	}
}

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
	var providerRequests atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Host == "provider.invalid" {
			providerRequests.Add(1)
		}
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
	business, err := sql.Open("sqlite", "file:j3a-manual-business?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer business.Close()
	for _, statement := range []string{
		`CREATE TABLE proxy_profiles (id TEXT PRIMARY KEY, test_status TEXT NOT NULL, latency_ms INTEGER, outbound_ip TEXT, outbound_region TEXT, last_test_message TEXT, last_tested_at TEXT, updated_at TEXT NOT NULL)`,
		`CREATE TABLE proxy_latency_projection_receipts (outcome_id TEXT PRIMARY KEY, proxy_id TEXT NOT NULL, input_version INTEGER NOT NULL, disposition TEXT NOT NULL, reason TEXT, applied_at TEXT NOT NULL)`,
		`CREATE TABLE proxy_latency_projection_cursors (consumer_key TEXT PRIMARY KEY, stored_at TEXT, outcome_id TEXT, updated_at TEXT NOT NULL)`,
		`INSERT INTO proxy_profiles(id,test_status,updated_at) VALUES ('proxy-manual','unknown','2026-08-23T00:00:00.123Z'), ('proxy-no-provider','unknown','2026-08-23T00:00:00.123Z')`,
	} {
		if _, err := business.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	projector, err := proxylatency.NewResultProjector(store, business, proxylatency.ResultProjectorConfig{Now: time.Now}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := proxylatency.NewRunner(cfg, store, nil, nil)
	runner.SetResultProjector(projector)
	var running atomic.Bool
	running.Store(true)
	handler := jobsHTTPHandler(ownermode.Active, &running, func() bool { return true }, false, func() bool { return true }, false, func() bool { return true }, nil, "", true, func() bool { return false }, func() proxylatency.RunnerStatus { return runner.Status() }, func() (proxylatency.RunnerStatus, bool) { return runner.Snapshot() }, true, cfg.ManualHTTPSecret, runner)
	body := `{"input":{"schema_version":1,"proxy_id":"proxy-manual","proxy_name":"Manual proxy","config_revision":"2026-08-23T00:00:00.123Z","proxy_type":"http","proxy_host":"` + proxyURL.Hostname() + `","proxy_port":` + fmt.Sprint(port) + `,"targets":[{"provider":"openai","profile_id":"profile-openai","name":"OpenAI","url":"http://provider.invalid/"},{"provider":"hybrid","profile_id":"profile-hybrid","name":"Hybrid","url":""},{"provider":"unsupported","profile_id":"profile-unsupported","name":"Unsupported","url":"ftp://provider.invalid/v1"}],"deadline_ms":1000}}`
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
	items, ok := report["items"].([]any)
	if !ok || len(items) != 4 || providerRequests.Load() != 1 {
		t.Fatalf("manual bridge must keep each invalid provider as one non-outbound item: providerRequests=%d report=%#v", providerRequests.Load(), report)
	}
	hybrid, ok := items[2].(map[string]any)
	if !ok || hybrid["targetUrl"] != "" || hybrid["message"] != "未形成真实代理检测请求：Invalid URL" || hybrid["status"] != "unknown" {
		t.Fatalf("manual bridge invalid provider compatibility mismatch: %#v", hybrid)
	}
	unsupported, ok := items[3].(map[string]any)
	if !ok || unsupported["targetUrl"] != "ftp://provider.invalid/v1" || unsupported["message"] != "未形成真实代理检测请求：不支持的目标协议：ftp:" || unsupported["status"] != "unknown" {
		t.Fatalf("manual bridge unsupported URL compatibility mismatch: %#v", unsupported)
	}
	runNodeGoProxyLatencyManualInterop(t, handler, proxyURL, cfg.ManualHTTPSecret)
	if _, err := business.Exec(`UPDATE proxy_profiles SET updated_at='2026-08-24T00:00:00.123Z' WHERE id='proxy-manual'`); err != nil {
		t.Fatal(err)
	}
	staleRequest := httptest.NewRequest(http.MethodPost, "/proxy-latency/manual", strings.NewReader(body))
	staleRequest.Header.Set("Authorization", "Bearer "+cfg.ManualHTTPSecret)
	staleRequest.Header.Set("Content-Type", "application/json")
	staleRecord := httptest.NewRecorder()
	handler.ServeHTTP(staleRecord, staleRequest)
	if staleRecord.Code != http.StatusOK {
		t.Fatalf("stale manual bridge must keep report status=%d body=%s", staleRecord.Code, staleRecord.Body.String())
	}
	if _, err := business.Exec(`DELETE FROM proxy_profiles WHERE id='proxy-manual'`); err != nil {
		t.Fatal(err)
	}
	deletedRequest := httptest.NewRequest(http.MethodPost, "/proxy-latency/manual", strings.NewReader(body))
	deletedRequest.Header.Set("Authorization", "Bearer "+cfg.ManualHTTPSecret)
	deletedRequest.Header.Set("Content-Type", "application/json")
	deletedRecord := httptest.NewRecorder()
	handler.ServeHTTP(deletedRecord, deletedRequest)
	if deletedRecord.Code != http.StatusNotFound {
		t.Fatalf("deleted proxy manual bridge status=%d body=%s", deletedRecord.Code, deletedRecord.Body.String())
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
	if _, err := business.Exec(`DELETE FROM proxy_profiles WHERE id='proxy-no-provider'`); err != nil {
		t.Fatal(err)
	}
	deletedNoProviderRequest := httptest.NewRequest(http.MethodPost, "/proxy-latency/manual", strings.NewReader(`{"input":{"schema_version":1,"proxy_id":"proxy-no-provider","proxy_name":"No provider","config_revision":"2026-08-23T00:00:00.123Z","proxy_type":"http","proxy_host":"127.0.0.1","proxy_port":8080,"targets":[]}}`))
	deletedNoProviderRequest.Header.Set("Authorization", "Bearer "+cfg.ManualHTTPSecret)
	deletedNoProviderRequest.Header.Set("Content-Type", "application/json")
	deletedNoProviderRecord := httptest.NewRecorder()
	handler.ServeHTTP(deletedNoProviderRecord, deletedNoProviderRequest)
	if deletedNoProviderRecord.Code != http.StatusNotFound {
		t.Fatalf("deleted no-provider manual bridge status=%d body=%s", deletedNoProviderRecord.Code, deletedNoProviderRecord.Body.String())
	}
}

// runNodeGoProxyLatencyManualInterop exercises the real Node handover adapter
// against the Go jobs HTTP handler. It is opt-in because the normal Go test
// image deliberately does not require a Node/pnpm runtime.
func runNodeGoProxyLatencyManualInterop(t *testing.T, handler http.Handler, proxyURL *url.URL, secret string) {
	t.Helper()
	if os.Getenv("J3A_NODE_GO_MANUAL_INTEROP") != "1" {
		return
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	pnpm := "pnpm"
	if runtime.GOOS == "windows" {
		pnpm = "pnpm.cmd"
	}
	command := exec.Command(pnpm, "--filter", "juhe-ai-backend", "run", "test:proxy-latency-node-go-manual-interop")
	command.Dir = repoRoot
	command.Env = append(os.Environ(),
		"J3A_NODE_GO_MANUAL_INTEROP_JOBS_URL="+server.URL,
		"J3A_NODE_GO_MANUAL_INTEROP_PROXY_HOST="+proxyURL.Hostname(),
		"J3A_NODE_GO_MANUAL_INTEROP_PROXY_PORT="+proxyURL.Port(),
		"J3A_NODE_GO_MANUAL_INTEROP_SECRET="+secret,
		"JUHE_AI_PROXY_LATENCY_JOBS_OWNER=go",
		"JUHE_AI_PROXY_LATENCY_JOBS_HTTP_URL="+server.URL,
		"JUHE_AI_PROXY_LATENCY_MANUAL_HTTP_SECRET="+secret,
		"JUHE_AI_PROXY_LATENCY_CREDENTIAL_SECRET=node-go-manual-interop-credential-secret",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Node -> Go J3a manual interop failed: %v\n%s", err, output)
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
		{name: "missing runner returns service unavailable", request: httptest.NewRequest(http.MethodPost, "/proxy-latency/manual", strings.NewReader(`{"input":{}}`)), want: http.StatusServiceUnavailable},
		{name: "wrong method returns method not allowed", request: httptest.NewRequest(http.MethodGet, "/proxy-latency/manual", nil), want: http.StatusMethodNotAllowed},
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
