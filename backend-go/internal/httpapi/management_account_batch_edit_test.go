package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/modules/managementaccountbatchedit"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

func TestNormalizeBatchUpdatesUsesExplicitSupportedFields(t *testing.T) {
	updates := normalizeBatchUpdates(map[string]any{
		"priority":             map[string]any{"enabled": true, "value": float64(9)},
		"availabilitySchedule": map[string]any{"enabled": true, "value": map[string]any{"timezone": "Asia/Shanghai"}},
		"tags":                 map[string]any{"enabled": true, "value": []any{"production"}},
	})
	if updates["priority"] != float64(9) {
		t.Fatalf("priority mapping missing: %#v", updates)
	}
	if updates["availability_schedule_json"] != `{"timezone":"Asia/Shanghai"}` {
		t.Fatalf("schedule mapping invalid: %#v", updates)
	}
	if _, ok := updates["tags"]; ok {
		t.Fatalf("unsupported tags must not become a SQL field: %#v", updates)
	}
}

func TestManagementAccountBatchEditRequiresAdmin(t *testing.T) {
	handler := NewManagementAccountBatchEditHandler(&managementaccountbatchedit.Service{})
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/accounts/batch-edit-context", strings.NewReader(`{"accountIds":["a","b"]}`))
	req = requestWithManagementAuthContext(req, managementauth.Context{SystemAccountID: "sys_user", Role: "user"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "需要管理员权限") {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}
