package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementmodelcheckoptions"
)

func TestManagementModelCheckOptionsHandlers(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.Handler
		auth       *managementauth.Context
		wantStatus int
		wantText   string
	}{
		{
			name:       "admin route accepts admin and ignores static scope query",
			handler:    NewManagementModelCheckOptionsHandler(managementmodelcheckoptions.NewService()),
			auth:       &managementauth.Context{SystemAccountID: "sys_admin", Role: "admin"},
			wantStatus: http.StatusOK,
			wantText:   `"defaultModel":"gpt-5.6-sol"`,
		},
		{
			name:       "self route accepts ordinary user",
			handler:    NewManagementMyModelCheckOptionsHandler(managementmodelcheckoptions.NewService()),
			auth:       &managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
			wantStatus: http.StatusOK,
			wantText:   `"enabledByDefault":false`,
		},
		{
			name:       "admin route rejects ordinary user",
			handler:    NewManagementModelCheckOptionsHandler(managementmodelcheckoptions.NewService()),
			auth:       &managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
			wantStatus: http.StatusForbidden,
			wantText:   "需要管理员权限",
		},
		{
			name:       "missing auth context is internal wiring error",
			handler:    NewManagementMyModelCheckOptionsHandler(managementmodelcheckoptions.NewService()),
			wantStatus: http.StatusInternalServerError,
			wantText:   "服务器内部错误",
		},
		{
			name:       "nil service is internal wiring error",
			handler:    NewManagementMyModelCheckOptionsHandler(nil),
			auth:       &managementauth.Context{SystemAccountID: "sys_user", Role: "user"},
			wantStatus: http.StatusInternalServerError,
			wantText:   "服务器内部错误",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/model-checks/options?systemAccountId=all&systemAccountId=ignored", nil)
			if tt.auth != nil {
				req = req.WithContext(context.WithValue(req.Context(), managementAuthContextKey, *tt.auth))
			}
			rec := httptest.NewRecorder()

			tt.handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantText) {
				t.Fatalf("body = %s, want text %q", rec.Body.String(), tt.wantText)
			}
			if rec.Code == http.StatusOK {
				var body struct {
					Data managementmodelcheckoptions.Result `json:"data"`
				}
				if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if len(body.Data.SupportedModels) != 13 || len(body.Data.SupportedProfiles) != 1 {
					t.Fatalf("data = %+v", body.Data)
				}
			}
		})
	}
}
