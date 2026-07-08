//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementproxies"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW2ManagementAuthPostgresSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("juhe_ai"),
		tcpostgres.WithUsername("juhe_ai"),
		tcpostgres.WithPassword("juhe_ai_password"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	defer terminateContainer(t, ctx, container)

	postgresURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}

	db := openSQLDB(t, postgresURL)
	defer closeSQLDB(t, db)
	runGooseMigrations(t, db)

	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	insertW2ProxyOptionsFixture(t, ctx, db, now)
	adminOldPasswordHash, err := managementauth.HashPassword("OldPass123")
	if err != nil {
		t.Fatalf("hash admin old password: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.system_accounts
		SET password_hash = $1
		WHERE id = 'sys_w2_proxy_options'
	`, adminOldPasswordHash); err != nil {
		t.Fatalf("set W2 admin password hash: %v", err)
	}
	sessionToken := "w2-management-session-token"
	insertW2ManagementSessionFixture(t, ctx, db, sessionToken, now)
	adminOtherSessionToken := "w2-management-other-session-token"
	insertW2ManagementSessionForAccountFixture(t, ctx, db, "sess_w2_management_auth_other", "sys_w2_proxy_options", adminOtherSessionToken, now)
	mustChangeSessionToken := "w2-management-must-change-session-token"
	insertW2MustChangeSystemAccountFixture(t, ctx, db, now)
	insertW2ManagementSessionForAccountFixture(t, ctx, db, "sess_w2_management_auth_must_change", "sys_w2_must_change", mustChangeSessionToken, now)
	mustChangeOtherSessionToken := "w2-management-must-change-other-session-token"
	insertW2ManagementSessionForAccountFixture(t, ctx, db, "sess_w2_management_auth_must_change_other", "sys_w2_must_change", mustChangeOtherSessionToken, now)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	authContext, err := authenticator.AuthenticateCookie(ctx, "juhe_ai_session="+sessionToken)
	if err != nil {
		t.Fatalf("authenticate management cookie: %v", err)
	}
	if authContext.SystemAccountID != "sys_w2_proxy_options" || authContext.Role != "admin" {
		t.Fatalf("auth context = %+v", authContext)
	}

	cfg := config.Config{
		Host:                 "127.0.0.1",
		Port:                 3000,
		ManagementAPIEnabled: true,
		TrustProxy:           "false",
	}
	operationLogQueue := &w2OperationLogQueueStub{}
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                       cfg,
		Logger:                       slog.Default(),
		ManagementAPIAuthMiddleware:  httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementCurrentUserHandler: httpapi.NewManagementCurrentUserHandler(authenticator),
		ManagementProfileUpdateHandler: httpapi.NewManagementProfileUpdateHandlerWithOperationLog(
			managementauth.NewProfileService(store),
			httpapi.ManagementOperationLogOptions{
				Config:   cfg,
				Client:   operationLogQueue,
				Now:      func() time.Time { return now },
				NewLogID: func() string { return "oplog_w2_profile" },
			},
		),
		ManagementPasswordChangeHandler: httpapi.NewManagementPasswordChangeHandler(authenticator, managementauth.NewPasswordService(store)),
		ManagementLogoutHandler:         httpapi.NewManagementLogoutHandler(authenticator),
		ManagementProxyOptionsHandler:   httpapi.NewManagementProxyOptionsHandler(managementproxies.NewService(store)),
	})

	currentUserReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
	currentUserReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	currentUserRec := httptest.NewRecorder()
	router.ServeHTTP(currentUserRec, currentUserReq)
	if currentUserRec.Code != http.StatusOK {
		t.Fatalf("current user status = %d, body = %s", currentUserRec.Code, currentUserRec.Body.String())
	}
	var currentUserBody struct {
		Data struct {
			ID                 string `json:"id"`
			Username           string `json:"username"`
			DisplayName        string `json:"displayName"`
			Role               string `json:"role"`
			MustChangePassword bool   `json:"mustChangePassword"`
		} `json:"data"`
	}
	if err := json.NewDecoder(currentUserRec.Body).Decode(&currentUserBody); err != nil {
		t.Fatalf("decode current user response: %v", err)
	}
	if currentUserBody.Data.ID != "sys_w2_proxy_options" ||
		currentUserBody.Data.Username != "w2-proxy-options" ||
		currentUserBody.Data.DisplayName != "W2 Proxy Options" ||
		currentUserBody.Data.Role != "admin" ||
		currentUserBody.Data.MustChangePassword {
		t.Fatalf("current user = %+v", currentUserBody.Data)
	}

	profileReq := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/auth/me", strings.NewReader(`{"displayName":"W2ProfileRenamed"}`))
	profileReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	profileRec := httptest.NewRecorder()
	router.ServeHTTP(profileRec, profileReq)
	if profileRec.Code != http.StatusOK {
		t.Fatalf("profile update status = %d, body = %s", profileRec.Code, profileRec.Body.String())
	}
	var profileBody struct {
		Data struct {
			ID          string `json:"id"`
			Username    string `json:"username"`
			DisplayName string `json:"displayName"`
			Role        string `json:"role"`
		} `json:"data"`
	}
	if err := json.NewDecoder(profileRec.Body).Decode(&profileBody); err != nil {
		t.Fatalf("decode profile response: %v", err)
	}
	if profileBody.Data.ID != "sys_w2_proxy_options" || profileBody.Data.DisplayName != "W2ProfileRenamed" || profileBody.Data.Role != "admin" {
		t.Fatalf("profile response = %+v", profileBody.Data)
	}
	var savedDisplayName string
	if err := db.QueryRowContext(ctx, `
		SELECT display_name
		FROM juhe_business.system_accounts
		WHERE id = 'sys_w2_proxy_options'
	`).Scan(&savedDisplayName); err != nil {
		t.Fatalf("read updated profile display name: %v", err)
	}
	if savedDisplayName != "W2ProfileRenamed" {
		t.Fatalf("saved display name = %q, want W2ProfileRenamed", savedDisplayName)
	}
	if operationLogQueue.decodeErr != nil {
		t.Fatalf("decode profile operation log: %v", operationLogQueue.decodeErr)
	}
	if len(operationLogQueue.logs) != 1 {
		t.Fatalf("operation logs = %d, want 1", len(operationLogQueue.logs))
	}
	profileLog := operationLogQueue.logs[0]
	if profileLog.OperationKey != "auth.update_profile" ||
		profileLog.Module != "system_accounts" ||
		profileLog.Action != "update" ||
		profileLog.ResourceType != "system_account" ||
		profileLog.ResourceID != "sys_w2_proxy_options" ||
		profileLog.ResourceName != "W2ProfileRenamed" ||
		profileLog.Summary != "修改显示名称：W2ProfileRenamed" {
		t.Fatalf("profile operation log = %+v", profileLog)
	}
	if len(profileLog.Changes) != 1 || profileLog.Changes[0].Field != "displayName" || profileLog.Changes[0].Label != "显示名称" {
		t.Fatalf("profile operation log changes = %+v", profileLog.Changes)
	}

	renamedUserReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
	renamedUserReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	renamedUserRec := httptest.NewRecorder()
	router.ServeHTTP(renamedUserRec, renamedUserReq)
	if renamedUserRec.Code != http.StatusOK {
		t.Fatalf("renamed current user status = %d, body = %s", renamedUserRec.Code, renamedUserRec.Body.String())
	}
	var renamedUserBody struct {
		Data struct {
			DisplayName string `json:"displayName"`
		} `json:"data"`
	}
	if err := json.NewDecoder(renamedUserRec.Body).Decode(&renamedUserBody); err != nil {
		t.Fatalf("decode renamed current user response: %v", err)
	}
	if renamedUserBody.Data.DisplayName != "W2ProfileRenamed" {
		t.Fatalf("renamed current user = %+v", renamedUserBody.Data)
	}

	mustChangeReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
	mustChangeReq.Header.Set("Cookie", "juhe_ai_session="+mustChangeSessionToken)
	mustChangeRec := httptest.NewRecorder()
	router.ServeHTTP(mustChangeRec, mustChangeReq)
	if mustChangeRec.Code != http.StatusOK {
		t.Fatalf("must change current user status = %d, body = %s", mustChangeRec.Code, mustChangeRec.Body.String())
	}
	var mustChangeBody struct {
		Data struct {
			ID                 string `json:"id"`
			Role               string `json:"role"`
			MustChangePassword bool   `json:"mustChangePassword"`
		} `json:"data"`
	}
	if err := json.NewDecoder(mustChangeRec.Body).Decode(&mustChangeBody); err != nil {
		t.Fatalf("decode must change current user response: %v", err)
	}
	if mustChangeBody.Data.ID != "sys_w2_must_change" || mustChangeBody.Data.Role != "user" || !mustChangeBody.Data.MustChangePassword {
		t.Fatalf("must change current user = %+v", mustChangeBody.Data)
	}

	mustChangeProtectedReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options", nil)
	mustChangeProtectedReq.Header.Set("Cookie", "juhe_ai_session="+mustChangeSessionToken)
	mustChangeProtectedRec := httptest.NewRecorder()
	router.ServeHTTP(mustChangeProtectedRec, mustChangeProtectedReq)
	if mustChangeProtectedRec.Code != http.StatusForbidden {
		t.Fatalf("must change protected status = %d, want 403, body = %s", mustChangeProtectedRec.Code, mustChangeProtectedRec.Body.String())
	}
	var mustChangeProtectedBody map[string]string
	if err := json.NewDecoder(mustChangeProtectedRec.Body).Decode(&mustChangeProtectedBody); err != nil {
		t.Fatalf("decode must change protected response: %v", err)
	}
	if mustChangeProtectedBody["code"] != managementauth.ErrorCodeMustChangePassword {
		t.Fatalf("must change protected body = %+v", mustChangeProtectedBody)
	}

	mustChangeProfileReq := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/auth/me", strings.NewReader(`{"displayName":"BlockedProfile"}`))
	mustChangeProfileReq.Header.Set("Cookie", "juhe_ai_session="+mustChangeSessionToken)
	mustChangeProfileRec := httptest.NewRecorder()
	router.ServeHTTP(mustChangeProfileRec, mustChangeProfileReq)
	if mustChangeProfileRec.Code != http.StatusForbidden {
		t.Fatalf("must change profile status = %d, want 403, body = %s", mustChangeProfileRec.Code, mustChangeProfileRec.Body.String())
	}
	var mustChangeProfileBody map[string]string
	if err := json.NewDecoder(mustChangeProfileRec.Body).Decode(&mustChangeProfileBody); err != nil {
		t.Fatalf("decode must change profile response: %v", err)
	}
	if mustChangeProfileBody["code"] != managementauth.ErrorCodeMustChangePassword {
		t.Fatalf("must change profile body = %+v", mustChangeProfileBody)
	}

	mustChangePasswordReq := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/change-password", strings.NewReader(`{"newPassword":"MustNew123"}`))
	mustChangePasswordReq.Header.Set("Cookie", "juhe_ai_session="+mustChangeSessionToken)
	mustChangePasswordRec := httptest.NewRecorder()
	router.ServeHTTP(mustChangePasswordRec, mustChangePasswordReq)
	if mustChangePasswordRec.Code != http.StatusOK {
		t.Fatalf("must change password status = %d, body = %s", mustChangePasswordRec.Code, mustChangePasswordRec.Body.String())
	}
	var mustChangePasswordBody struct {
		Data struct {
			ID                 string `json:"id"`
			Status             string `json:"status"`
			MustChangePassword bool   `json:"mustChangePassword"`
		} `json:"data"`
	}
	if err := json.NewDecoder(mustChangePasswordRec.Body).Decode(&mustChangePasswordBody); err != nil {
		t.Fatalf("decode must change password response: %v", err)
	}
	if mustChangePasswordBody.Data.ID != "sys_w2_must_change" ||
		mustChangePasswordBody.Data.Status != "active" ||
		mustChangePasswordBody.Data.MustChangePassword {
		t.Fatalf("must change password response = %+v", mustChangePasswordBody.Data)
	}
	var mustChangeSavedHash string
	var mustChangeSavedFlag bool
	if err := db.QueryRowContext(ctx, `
		SELECT password_hash, must_change_password
		FROM juhe_business.system_accounts
		WHERE id = 'sys_w2_must_change'
	`).Scan(&mustChangeSavedHash, &mustChangeSavedFlag); err != nil {
		t.Fatalf("read must change password state: %v", err)
	}
	if mustChangeSavedFlag || mustChangeSavedHash == "MustNew123" || !managementauth.VerifyPassword("MustNew123", mustChangeSavedHash) {
		t.Fatalf("must change saved password state flag=%v hash=%q", mustChangeSavedFlag, mustChangeSavedHash)
	}
	mustChangeOtherReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
	mustChangeOtherReq.Header.Set("Cookie", "juhe_ai_session="+mustChangeOtherSessionToken)
	mustChangeOtherRec := httptest.NewRecorder()
	router.ServeHTTP(mustChangeOtherRec, mustChangeOtherReq)
	if mustChangeOtherRec.Code != http.StatusUnauthorized {
		t.Fatalf("must change other session status = %d, want 401, body = %s", mustChangeOtherRec.Code, mustChangeOtherRec.Body.String())
	}
	mustChangeAfterReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
	mustChangeAfterReq.Header.Set("Cookie", "juhe_ai_session="+mustChangeSessionToken)
	mustChangeAfterRec := httptest.NewRecorder()
	router.ServeHTTP(mustChangeAfterRec, mustChangeAfterReq)
	if mustChangeAfterRec.Code != http.StatusOK {
		t.Fatalf("must change current session after password status = %d, body = %s", mustChangeAfterRec.Code, mustChangeAfterRec.Body.String())
	}
	var mustChangeAfterBody struct {
		Data struct {
			MustChangePassword bool `json:"mustChangePassword"`
		} `json:"data"`
	}
	if err := json.NewDecoder(mustChangeAfterRec.Body).Decode(&mustChangeAfterBody); err != nil {
		t.Fatalf("decode must change after response: %v", err)
	}
	if mustChangeAfterBody.Data.MustChangePassword {
		t.Fatalf("must change current user after password = %+v", mustChangeAfterBody.Data)
	}

	adminMissingOldReq := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/change-password", strings.NewReader(`{"newPassword":"AdminNew123"}`))
	adminMissingOldReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	adminMissingOldRec := httptest.NewRecorder()
	router.ServeHTTP(adminMissingOldRec, adminMissingOldReq)
	if adminMissingOldRec.Code != http.StatusBadRequest {
		t.Fatalf("admin missing old password status = %d, want 400, body = %s", adminMissingOldRec.Code, adminMissingOldRec.Body.String())
	}
	var adminMissingOldBody map[string]string
	if err := json.NewDecoder(adminMissingOldRec.Body).Decode(&adminMissingOldBody); err != nil {
		t.Fatalf("decode admin missing old response: %v", err)
	}
	if adminMissingOldBody["message"] != "请填写当前密码" {
		t.Fatalf("admin missing old body = %+v", adminMissingOldBody)
	}
	adminWrongOldReq := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/change-password", strings.NewReader(`{"oldPassword":"WrongPass","newPassword":"AdminNew123"}`))
	adminWrongOldReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	adminWrongOldRec := httptest.NewRecorder()
	router.ServeHTTP(adminWrongOldRec, adminWrongOldReq)
	if adminWrongOldRec.Code != http.StatusBadRequest {
		t.Fatalf("admin wrong old password status = %d, want 400, body = %s", adminWrongOldRec.Code, adminWrongOldRec.Body.String())
	}
	var adminWrongOldBody map[string]string
	if err := json.NewDecoder(adminWrongOldRec.Body).Decode(&adminWrongOldBody); err != nil {
		t.Fatalf("decode admin wrong old response: %v", err)
	}
	if adminWrongOldBody["message"] != "当前密码不正确" {
		t.Fatalf("admin wrong old body = %+v", adminWrongOldBody)
	}
	adminPasswordReq := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/change-password", strings.NewReader(`{"oldPassword":"OldPass123","newPassword":"AdminNew123"}`))
	adminPasswordReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	adminPasswordRec := httptest.NewRecorder()
	router.ServeHTTP(adminPasswordRec, adminPasswordReq)
	if adminPasswordRec.Code != http.StatusOK {
		t.Fatalf("admin password status = %d, body = %s", adminPasswordRec.Code, adminPasswordRec.Body.String())
	}
	var adminPasswordBody struct {
		Data struct {
			ID                     string `json:"id"`
			DisplayName            string `json:"displayName"`
			Status                 string `json:"status"`
			MustChangePassword     bool   `json:"mustChangePassword"`
			ImageGenerationEnabled bool   `json:"imageGenerationEnabled"`
			UpdatedAt              string `json:"updatedAt"`
		} `json:"data"`
	}
	if err := json.NewDecoder(adminPasswordRec.Body).Decode(&adminPasswordBody); err != nil {
		t.Fatalf("decode admin password response: %v", err)
	}
	if adminPasswordBody.Data.ID != "sys_w2_proxy_options" ||
		adminPasswordBody.Data.DisplayName != "W2ProfileRenamed" ||
		adminPasswordBody.Data.Status != "active" ||
		adminPasswordBody.Data.MustChangePassword ||
		adminPasswordBody.Data.UpdatedAt == "" {
		t.Fatalf("admin password response = %+v", adminPasswordBody.Data)
	}
	var adminSavedHash string
	var adminSavedFlag bool
	if err := db.QueryRowContext(ctx, `
		SELECT password_hash, must_change_password
		FROM juhe_business.system_accounts
		WHERE id = 'sys_w2_proxy_options'
	`).Scan(&adminSavedHash, &adminSavedFlag); err != nil {
		t.Fatalf("read admin password state: %v", err)
	}
	if adminSavedFlag || adminSavedHash == "AdminNew123" || !managementauth.VerifyPassword("AdminNew123", adminSavedHash) {
		t.Fatalf("admin saved password state flag=%v hash=%q", adminSavedFlag, adminSavedHash)
	}
	adminOtherReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
	adminOtherReq.Header.Set("Cookie", "juhe_ai_session="+adminOtherSessionToken)
	adminOtherRec := httptest.NewRecorder()
	router.ServeHTTP(adminOtherRec, adminOtherReq)
	if adminOtherRec.Code != http.StatusUnauthorized {
		t.Fatalf("admin other session status = %d, want 401, body = %s", adminOtherRec.Code, adminOtherRec.Body.String())
	}
	adminCurrentAfterPasswordReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
	adminCurrentAfterPasswordReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	adminCurrentAfterPasswordRec := httptest.NewRecorder()
	router.ServeHTTP(adminCurrentAfterPasswordRec, adminCurrentAfterPasswordReq)
	if adminCurrentAfterPasswordRec.Code != http.StatusOK {
		t.Fatalf("admin current session after password status = %d, body = %s", adminCurrentAfterPasswordRec.Code, adminCurrentAfterPasswordRec.Body.String())
	}
	if len(operationLogQueue.logs) != 1 {
		t.Fatalf("operation logs after password change = %d, want only profile log", len(operationLogQueue.logs))
	}

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options?keyword=Al&limit=2", nil)
	req.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []managementproxies.Option `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data) != 2 || body.Data[0].ID != "proxy_w2_alpha" || body.Data[1].ID != "proxy_w2_alpine" {
		t.Fatalf("proxy options = %+v", body.Data)
	}

	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401", unauthorized.Code)
	}

	logoutReq := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/logout", nil)
	logoutReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	logoutRec := httptest.NewRecorder()
	router.ServeHTTP(logoutRec, logoutReq)
	if logoutRec.Code != http.StatusOK {
		t.Fatalf("logout status = %d, body = %s", logoutRec.Code, logoutRec.Body.String())
	}
	var logoutBody struct {
		Data struct {
			LoggedOut bool `json:"loggedOut"`
		} `json:"data"`
	}
	if err := json.NewDecoder(logoutRec.Body).Decode(&logoutBody); err != nil {
		t.Fatalf("decode logout response: %v", err)
	}
	if !logoutBody.Data.LoggedOut {
		t.Fatalf("logout response = %+v", logoutBody.Data)
	}
	setCookie := logoutRec.Header().Get("Set-Cookie")
	if !strings.Contains(setCookie, "juhe_ai_session=") || !strings.Contains(setCookie, "Max-Age=0") || !strings.Contains(setCookie, "Path=/") {
		t.Fatalf("logout Set-Cookie = %q", setCookie)
	}

	revokedReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options", nil)
	revokedReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	revokedRec := httptest.NewRecorder()
	router.ServeHTTP(revokedRec, revokedReq)
	if revokedRec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status = %d, want 401, body = %s", revokedRec.Code, revokedRec.Body.String())
	}
}

func insertW2ManagementSessionFixture(t *testing.T, ctx context.Context, db *sql.DB, token string, now time.Time) {
	t.Helper()
	insertW2ManagementSessionForAccountFixture(t, ctx, db, "sess_w2_management_auth", "sys_w2_proxy_options", token, now)
}

func insertW2MustChangeSystemAccountFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES (
			'sys_w2_must_change', 'w2-must-change', 'W2 Must Change', NULL, 'user', 'active', 'hash',
			true, false, $1, $2
		)
	`, now, now)
	if err != nil {
		t.Fatalf("insert W2 must change system account: %v", err)
	}
}
