package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

func TestPassiveGatewayHealthNeverClaimsOwnerReadiness(t *testing.T) {
	record := httptest.NewRecorder()
	passiveGatewayHealthHandler(ownermode.Drain).ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", record.Code, record.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(record.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ready"] != false || payload["ownerReady"] != false || payload["ownerMode"] != "drain" || payload["auditLogReady"] != false {
		t.Fatalf("passive health claimed owner readiness: %#v", payload)
	}
}
