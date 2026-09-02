package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

func TestValidateLoopbackListenAddress(t *testing.T) {
	for _, test := range []struct {
		address string
		valid   bool
	}{
		{"127.0.0.1:3305", true}, {"127.255.255.254:65535", true}, {"localhost:3305", true}, {"LOCALHOST:3305", true}, {"[::1]:3305", true},
		{"0.0.0.0:3305", false}, {"[::]:3305", false}, {"192.168.1.10:3305", false}, {"127.0.0.1", false}, {"localhost", false}, {":3305", false}, {"localhost:not-a-port", false}, {"localhost:0", false}, {"localhost:65536", false},
	} {
		err := validateLoopbackListenAddress(test.address)
		if (err == nil) != test.valid {
			t.Fatalf("validateLoopbackListenAddress(%q) err=%v valid=%v", test.address, err, test.valid)
		}
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
		t.Fatalf("listenLoopback(%q): %v", address, err)
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
		t.Fatal("manual bridge accepted missing bearer secret")
	}
	request.Header.Set("Authorization", "Bearer wrong")
	if matchesAccountBalanceManualSecret(request, secret) {
		t.Fatal("manual bridge accepted wrong bearer secret")
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	if !matchesAccountBalanceManualSecret(request, secret) {
		t.Fatal("manual bridge rejected configured bearer secret")
	}
}

func TestPassiveJobsHealthNeverClaimsOwnerReadiness(t *testing.T) {
	handler := passiveJobsHealthHandler(ownermode.Standby)
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	var payload map[string]any
	if record.Code != http.StatusOK || json.Unmarshal(record.Body.Bytes(), &payload) != nil {
		t.Fatalf("health status=%d body=%s", record.Code, record.Body.String())
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
	var payload map[string]any
	if record.Code != http.StatusOK || json.Unmarshal(record.Body.Bytes(), &payload) != nil {
		t.Fatalf("health status=%d body=%s", record.Code, record.Body.String())
	}
	if payload["proxyLatencyEnabled"] != true || payload["proxyLatencyOwnerHeld"] != true || payload["proxyLatencyExecuted"] != float64(2) || payload["proxyLatencyLastError"] != "" {
		t.Fatalf("missing proxy latency health fields: %#v", payload)
	}
}

func TestHealthKeepsProxyLatencyFailureVisible(t *testing.T) {
	var running atomic.Bool
	running.Store(true)
	status := func() proxylatency.RunnerStatus { return proxylatency.RunnerStatus{LastError: "owner lease lost"} }
	handler := healthHandler(ownermode.Active, &running, func() bool { return true }, false, func() bool { return true }, false, func() bool { return true }, true, func() bool { return false }, status)
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	var payload map[string]any
	if err := json.Unmarshal(record.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ready"] != false || payload["proxyLatencyReady"] != false || payload["proxyLatencyLastError"] != "owner lease lost" {
		t.Fatalf("failed proxy latency state was not exposed: %#v", payload)
	}
}

func TestHealthDisabledProxyLatencyDoesNotBlock(t *testing.T) {
	var running atomic.Bool
	running.Store(true)
	handler := healthHandler(ownermode.Active, &running, func() bool { return true }, false, func() bool { return true }, false, func() bool { return true }, false, func() bool { return false }, func() proxylatency.RunnerStatus { return proxylatency.RunnerStatus{} })
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
	var running atomic.Bool
	running.Store(true)
	status := func() proxylatency.RunnerStatus { return proxylatency.RunnerStatus{LastError: "owner lease lost"} }
	snapshot := func() (proxylatency.RunnerStatus, bool) { return status(), false }
	handler := healthHandler(ownermode.Active, &running, func() bool { return true }, false, func() bool { return true }, false, func() bool { return true }, true, func() bool { return true }, status, snapshot)
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	var payload map[string]any
	if err := json.Unmarshal(record.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ready"] != false || payload["proxyLatencyReady"] != false || payload["proxyLatencyLastError"] != "owner lease lost" {
		t.Fatalf("health did not use atomic proxy snapshot: %#v", payload)
	}
}

func TestJobsHTTPHandlerDoesNotExposeRetiredProxyLatencyBridge(t *testing.T) {
	var running atomic.Bool
	running.Store(true)
	handler := jobsHTTPHandler(ownermode.Active, &running, func() bool { return true }, false, func() bool { return true }, false, func() bool { return true }, nil, "")
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodPost, "/proxy-latency/manual", nil))
	if record.Code != http.StatusNotFound {
		t.Fatalf("retired J3a bridge status=%d body=%s", record.Code, record.Body.String())
	}
}

func TestJobsHTTPHandlerExposesGoRuntimeMetrics(t *testing.T) {
	var running atomic.Bool
	handler := jobsHTTPHandler(ownermode.Active, &running, func() bool { return true }, false, func() bool { return true }, false, func() bool { return true }, nil, "")
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/__aisys__/metrics", nil))
	if record.Code != http.StatusOK || !strings.Contains(record.Body.String(), `runtimeKind="go"`) {
		t.Fatalf("metrics status=%d body=%s", record.Code, record.Body.String())
	}
}
