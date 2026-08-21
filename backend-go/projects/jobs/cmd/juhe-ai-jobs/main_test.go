package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proxylatency"
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

func TestHealthWiresProxyLatencyStatus(t *testing.T) {
	var running atomic.Bool
	running.Store(true)
	now := time.Date(2026, 8, 21, 15, 0, 0, 0, time.UTC)
	status := func() proxylatency.RunnerStatus {
		return proxylatency.RunnerStatus{OwnerHeld: true, LastCycleAt: now, LastSuccess: now, Inputs: 2, Executed: 2}
	}
	handler := healthHandler(&running, func() bool { return true }, false, func() bool { return true }, false, func() bool { return true }, true, func() bool { return true }, status)
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
	handler := healthHandler(&running, func() bool { return true }, false, func() bool { return true }, false, func() bool { return true }, true, func() bool { return false }, status)
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
	handler := healthHandler(&running, func() bool { return true }, false, func() bool { return true }, false, func() bool { return true }, false, func() bool { return false }, func() proxylatency.RunnerStatus {
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
	handler := healthHandler(&running, func() bool { return true }, false, func() bool { return true }, false, func() bool { return true }, true, func() bool { return true }, status, snapshot)
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
