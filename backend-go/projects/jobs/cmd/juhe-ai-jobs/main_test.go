package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestHealthHandlerReportsReadinessWithoutFailingLiveness(t *testing.T) {
	var runtimeRunning atomic.Bool
	runtimeRunning.Store(true)
	recorder := httptest.NewRecorder()
	healthHandler(&runtimeRunning, func() bool { return false }).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("F2 未 ready 时 /health 必须保持 liveness 200，实际为 %d", recorder.Code)
	}
	var body struct {
		Ready               bool `json:"ready"`
		RuntimeLogOwnerHeld bool `json:"runtimeLogOwnerHeld"`
		TableMonitorReady   bool `json:"tableMonitorReady"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Ready || !body.RuntimeLogOwnerHeld || body.TableMonitorReady {
		t.Fatalf("/health 必须如实报告 F1/F2 readiness，实际为 %+v", body)
	}
}
