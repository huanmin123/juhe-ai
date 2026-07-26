package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementProfileDetailHandlerReturnsForcedPasswordProfile(t *testing.T) {
	createdAt := time.Date(2026, 7, 1, 2, 3, 4, 0, time.UTC)
	lastLoginAt := createdAt.Add(time.Hour)
	authenticator := &managementCurrentUserAuthenticatorStub{
		context: managementauth.Context{SystemAccountID: "sys_user", MustChangePassword: true},
	}
	service := managementauth.NewProfileDetailService(&managementProfileDetailReaderStub{
		account: port.ManagementSystemAccountSummary{
			ID: "sys_user", Username: "user", DisplayName: "测试用户", Description: "普通账户",
			Role: "user", Status: "active", MustChangePassword: true, ImageGenerationEnabled: true,
			LastLoginAt: &lastLoginAt, CreatedAt: createdAt, UpdatedAt: createdAt,
		},
		found: true,
	})
	handler := NewManagementProfileDetailHandler(authenticator, service)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/profile", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if authenticator.cookieHeader != "juhe_ai_session=session-token" {
		t.Fatalf("cookie header = %q", authenticator.cookieHeader)
	}
	var body struct {
		Data managementPasswordChangeResponse `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.ID != "sys_user" || body.Data.Status != "active" || !body.Data.MustChangePassword || !body.Data.ImageGenerationEnabled {
		t.Fatalf("body = %+v", body.Data)
	}
	if body.Data.LastLoginAt != lastLoginAt.Format(time.RFC3339Nano) || body.Data.CreatedAt != createdAt.Format(time.RFC3339Nano) {
		t.Fatalf("timestamps = %+v", body.Data)
	}
	for _, sensitive := range []string{"password", "passwordHash", "tokenHash"} {
		if strings.Contains(rec.Body.String(), sensitive) {
			t.Fatalf("response contains sensitive field %q: %s", sensitive, rec.Body.String())
		}
	}
}

type managementProfileDetailReaderStub struct {
	account port.ManagementSystemAccountSummary
	found   bool
	err     error
}

func (s *managementProfileDetailReaderStub) FindManagementCurrentUserProfile(_ context.Context, _ string) (port.ManagementSystemAccountSummary, bool, error) {
	return s.account, s.found, s.err
}
