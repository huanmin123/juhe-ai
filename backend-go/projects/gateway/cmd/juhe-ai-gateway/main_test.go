package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func TestLoadSessionRetentionConfigDefaults(t *testing.T) {
	interval, limit, err := loadSessionRetentionConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if interval != 15*time.Minute || limit != 10000 {
		t.Fatalf("defaults interval=%s limit=%d", interval, limit)
	}
}

func TestLoadSessionRetentionConfigRejectsInvalidValues(t *testing.T) {
	for name, values := range map[string]string{
		"JUHE_AI_SESSION_RETENTION_INTERVAL":   "0s",
		"JUHE_AI_SESSION_RETENTION_BATCH_SIZE": "not-a-number",
	} {
		_, _, err := loadSessionRetentionConfig(func(key string) string {
			if key == name {
				return values
			}
			return ""
		})
		if err == nil {
			t.Fatalf("invalid %s must fail", name)
		}
	}
}
