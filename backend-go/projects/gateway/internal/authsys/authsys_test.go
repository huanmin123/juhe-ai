package authsys

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"

	_ "modernc.org/sqlite"
)

type revokeFailingPort struct {
	businessauth.Port
	err error
}

func (p revokeFailingPort) RevokeToken(context.Context, string) error {
	return p.err
}

type authenticateFailingPort struct {
	businessauth.Port
	err error
}

func (p authenticateFailingPort) Authenticate(context.Context, string, bool, bool) (modelcheckauth.Actor, error) {
	return modelcheckauth.Actor{}, p.err
}

func newTestEnv(t *testing.T) (*Deps, *kernel.Kernel, *httptest.Server) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:authsys-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	// modernc sqlite serializes writers; a single pooled connection avoids
	// cross-connection table-lock deadlocks (same recipe as businessauth tests).
	db.SetMaxOpenConns(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for i, statement := range []string{
		`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER CHECK (ai_account_limit BETWEEN 0 AND 1000000), request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL, FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_system_accounts_username_unique_lower ON system_accounts(lower(username))`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
		t.Logf("exec %d done", i)
	}
	now := time.Now
	service, err := businessauth.New(db, modelcheckauth.SQLite, now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("constructors done")
	accounts, err := NewAccountStore(db, modelcheckauth.SQLite, now)
	if err != nil {
		t.Fatal(err)
	}
	deps := &Deps{
		Port: service, Accounts: accounts, Captcha: modelcheckauth.NewCaptchaService(now),
		LoginGuard: modelcheckauth.NewLoginGuard(now), Now: now, CaptchaDisabled: true,
		TemporaryAccessIPAllowlist: []string{"127.0.0.1"},
	}
	k := kernel.New(kernel.Options{})
	deps.MountAuth(k, "lax", false)
	deps.MountSystemAccounts(k, "lax", false)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return deps, k, server
}

func seedAccount(t *testing.T, deps *Deps, username, password, role string) AccountListItem {
	t.Helper()
	item, err := deps.Accounts.Create(nil, CreateInput{
		Username: username, DisplayName: strings.ToUpper(username) + "_Name",
		Password: password, Role: role, MustChangePassword: boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func postJSON(t *testing.T, server *httptest.Server, path, body, cookie string) (*http.Response, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	return response, payload
}

func getJSON(t *testing.T, server *httptest.Server, path, cookie string) (*http.Response, map[string]any) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	return response, payload
}

func login(t *testing.T, server *httptest.Server, username, password string) string {
	t.Helper()
	response, payload := postJSON(t, server, "/__aisys__/api/auth/login", `{"username":"`+username+`","password":"`+password+`"}`, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login failed: %d %v", response.StatusCode, payload)
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == SessionCookieName {
			return cookie.Name + "=" + cookie.Value
		}
	}
	t.Fatal("session cookie missing")
	return ""
}

func TestLoginMeChangePasswordFlow(t *testing.T) {
	deps, _, server := newTestEnv(t)
	seedAccount(t, deps, "admin1", "super-secret", "super_admin")

	wrongResponse, payload := postJSON(t, server, "/__aisys__/api/auth/login", `{"username":"admin1","password":"wrong"}`, "")
	if wrongResponse.StatusCode != http.StatusUnauthorized || payload["message"] != "账号或密码错误" {
		t.Fatalf("wrong password: %d %v", wrongResponse.StatusCode, payload)
	}

	cookie := login(t, server, "admin1", "super-secret")

	meResponse, mePayload := getJSON(t, server, "/__aisys__/api/auth/me", cookie)
	if meResponse.StatusCode != http.StatusOK || mePayload["data"].(map[string]any)["username"] != "admin1" {
		t.Fatalf("me: %d %v", meResponse.StatusCode, mePayload)
	}

	updatedProfile, updatedProfilePayload := patchJSON(t, server, "/__aisys__/api/auth/me", `{"displayName":"AdminRenamed"}`, cookie)
	if updatedProfile.StatusCode != http.StatusOK {
		t.Fatalf("update profile: %d %v", updatedProfile.StatusCode, updatedProfilePayload)
	}
	updatedProfileData, ok := updatedProfilePayload["data"].(map[string]any)
	if !ok || updatedProfileData["displayName"] != "AdminRenamed" {
		t.Fatalf("update profile response must contain the persisted display name: %v", updatedProfilePayload)
	}

	// Anonymous request is rejected with the exact Node message.
	anonymous, anonymousPayload := getJSON(t, server, "/__aisys__/api/auth/me", "")
	if anonymous.StatusCode != http.StatusUnauthorized || anonymousPayload["message"] != "请先登录" {
		t.Fatalf("anonymous me: %d %v", anonymous.StatusCode, anonymousPayload)
	}

	wrongOld, wrongOldPayload := postJSON(t, server, "/__aisys__/api/auth/change-password", `{"oldPassword":"nope","newPassword":"new-pass-123"}`, cookie)
	if wrongOld.StatusCode != http.StatusBadRequest || wrongOldPayload["message"] != "当前密码不正确" {
		t.Fatalf("wrong old password: %d %v", wrongOld.StatusCode, wrongOldPayload)
	}

	changed, changedPayload := postJSON(t, server, "/__aisys__/api/auth/change-password", `{"oldPassword":"super-secret","newPassword":"new-pass-123"}`, cookie)
	if changed.StatusCode != http.StatusOK || changedPayload["data"].(map[string]any)["mustChangePassword"] != false {
		t.Fatalf("change password: %d %v", changed.StatusCode, changedPayload)
	}

	// The current session still authenticates after change-password.
	if _, err := deps.Port.Authenticate(context.Background(), cookieSessionToken(cookie), false, false); err != nil {
		t.Fatalf("current session must survive: %v", err)
	}
	reLogin := login(t, server, "admin1", "new-pass-123")
	if reLogin == "" {
		t.Fatal("re-login with new password failed")
	}
}

func TestLoginGuardLocksAfterTenFailures(t *testing.T) {
	deps, _, server := newTestEnv(t)
	seedAccount(t, deps, "user1", "password-1", "user")
	for i := 0; i < 10; i++ {
		response, _ := postJSON(t, server, "/__aisys__/api/auth/login", `{"username":"user1","password":"bad"}`, "")
		if i < 9 && response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d expected 401, got %d", i, response.StatusCode)
		}
	}
	blocked, payload := postJSON(t, server, "/__aisys__/api/auth/login", `{"username":"user1","password":"password-1"}`, "")
	// Same-IP attempts hit the IP dimension first (Node checks IP before
	// username); a different IP would receive the username message.
	if blocked.StatusCode != http.StatusTooManyRequests || payload["message"] != "尝试过于频繁，请稍后再试" {
		t.Fatalf("locked login: %d %v", blocked.StatusCode, payload)
	}
	if retryAfter := blocked.Header.Get("Retry-After"); retryAfter == "" {
		t.Fatal("Retry-After header missing on lock")
	}
}

func TestCaptchaContract(t *testing.T) {
	deps, _, server := newTestEnv(t)
	response, payload := getJSON(t, server, "/__aisys__/api/auth/captcha", "")
	if response.StatusCode != http.StatusOK || payload["data"].(map[string]any)["required"] != false {
		t.Fatalf("captcha disabled contract: %d %v", response.StatusCode, payload)
	}

	deps.CaptchaDisabled = false
	enabled, enabledPayload := getJSON(t, server, "/__aisys__/api/auth/captcha", "")
	data := enabledPayload["data"].(map[string]any)
	if enabled.StatusCode != http.StatusOK || data["required"] != true || data["captchaId"] == "" {
		t.Fatalf("captcha enabled contract: %d %v", enabled.StatusCode, enabledPayload)
	}
	if !strings.HasPrefix(data["image"].(string), "data:image/png;base64,") {
		t.Fatal("captcha image must be a PNG data URL")
	}

	missing, missingPayload := postJSON(t, server, "/__aisys__/api/auth/login", `{"username":"x","password":"y"}`, "")
	if missing.StatusCode != http.StatusBadRequest || missingPayload["message"] != "登录参数无效" {
		t.Fatalf("captcha params required: %d %v", missing.StatusCode, missingPayload)
	}

	wrong, wrongPayload := postJSON(t, server, "/__aisys__/api/auth/login",
		`{"username":"x","password":"y","captchaId":"`+data["captchaId"].(string)+`","captchaCode":"XXXXX"}`, "")
	if wrong.StatusCode != http.StatusBadRequest || wrongPayload["message"] != "验证码错误或已过期" {
		t.Fatalf("wrong captcha: %d %v", wrong.StatusCode, wrongPayload)
	}
}

func TestSystemAccountsCRUDAndAuthorization(t *testing.T) {
	deps, _, server := newTestEnv(t)
	seedAccount(t, deps, "root", "root-password", "super_admin")
	seedAccount(t, deps, "plain", "plain-password", "user")
	adminCookie := login(t, server, "root", "root-password")
	userCookie := login(t, server, "plain", "plain-password")

	// User role cannot list accounts.
	forbidden, forbiddenPayload := getJSON(t, server, "/__aisys__/api/system-accounts", userCookie)
	if forbidden.StatusCode != http.StatusForbidden || forbiddenPayload["message"] != "需要管理员权限" {
		t.Fatalf("user listing accounts: %d %v", forbidden.StatusCode, forbiddenPayload)
	}

	created, createdPayload := postJSON(t, server, "/__aisys__/api/system-accounts",
		`{"username":"second","displayName":"Second_Name","password":"second-pass","role":"admin"}`, adminCookie)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create account: %d %v", created.StatusCode, createdPayload)
	}

	duplicate, duplicatePayload := postJSON(t, server, "/__aisys__/api/system-accounts",
		`{"username":"SECOND","displayName":"Other_Name","password":"second-pass"}`, adminCookie)
	if duplicate.StatusCode != http.StatusConflict || duplicatePayload["message"] != "用户账户已存在" {
		t.Fatalf("duplicate username: %d %v", duplicate.StatusCode, duplicatePayload)
	}

	list, listPayload := getJSON(t, server, "/__aisys__/api/system-accounts?page=1&pageSize=20", adminCookie)
	if list.StatusCode != http.StatusOK {
		t.Fatalf("list accounts: %d %v", list.StatusCode, listPayload)
	}
	rawItems, present := listPayload["data"].(map[string]any)["items"]
	if !present {
		t.Fatalf("items key missing: %d %v", list.StatusCode, listPayload)
	}
	items := rawItems.([]any)
	if list.StatusCode != http.StatusOK || len(items) != 3 {
		t.Fatalf("list accounts: %d %d items", list.StatusCode, len(items))
	}

	// Super-admin invariant: disabling the only active super admin is 409.
	rootID := accountIDByUsername(t, deps, "root")
	patchBody := `{"expectedUpdatedAt":"` + rfc3339NowPlus(t, deps, 0) + `","status":"disabled"}`
	_ = patchBody
	// (fetch current edit version through the list payload)
	item := findItemByUsername(t, listPayload, "root")
	patch, patchPayload := patchJSON(t, server, "/__aisys__/api/system-accounts/"+rootID,
		`{"expectedUpdatedAt":"`+item["editVersion"].(string)+`","status":"disabled"}`, adminCookie)
	if patch.StatusCode != http.StatusConflict || patchPayload["message"] != "至少保留一个启用的超级管理员" {
		t.Fatalf("super admin invariant: %d %v", patch.StatusCode, patchPayload)
	}

	// Optimistic concurrency: stale expectedUpdatedAt conflicts.
	stale, stalePayload := patchJSON(t, server, "/__aisys__/api/system-accounts/"+accountIDByUsername(t, deps, "plain"),
		`{"expectedUpdatedAt":"2001-01-01T00:00:00Z","displayName":"Renamed Name"}`, adminCookie)
	if stale.StatusCode != http.StatusConflict || stalePayload["message"] != "系统账户已被其他操作修改，请刷新后重试" {
		t.Fatalf("stale patch: %d %v", stale.StatusCode, stalePayload)
	}

	// Username changes are rejected.
	rename, renamePayload := patchJSON(t, server, "/__aisys__/api/system-accounts/"+accountIDByUsername(t, deps, "plain"),
		`{"expectedUpdatedAt":"`+item["editVersion"].(string)+`","username":"newname"}`, adminCookie)
	if rename.StatusCode != http.StatusBadRequest || renamePayload["message"] != "用户账户创建后不能修改" {
		t.Fatalf("username immutable: %d %v", rename.StatusCode, renamePayload)
	}

	// Password reset via management PATCH forces logout of that account.
	plainItem := findItemByUsername(t, listPayload, "plain")
	plainPatch, _ := patchJSON(t, server, "/__aisys__/api/system-accounts/"+accountIDByUsername(t, deps, "plain"),
		`{"expectedUpdatedAt":"`+plainItem["editVersion"].(string)+`","password":"rotated-pass"}`, adminCookie)
	if plainPatch.StatusCode != http.StatusOK {
		t.Fatalf("password reset patch: %d %v", plainPatch.StatusCode, plainPatch)
	}
	oldSession, oldPayload := getJSON(t, server, "/__aisys__/api/auth/me", userCookie)
	if oldSession.StatusCode != http.StatusUnauthorized {
		t.Fatalf("password reset must revoke sessions: %d %v", oldSession.StatusCode, oldPayload)
	}
}

func TestTemporaryAccessTokenLifecycle(t *testing.T) {
	deps, _, server := newTestEnv(t)
	seedAccount(t, deps, "admin1", "super-secret", "super_admin")

	issued, issuedPayload := postJSON(t, server, "/__aisys__/api/auth/temporary-access-tokens",
		`{"username":"admin1","password":"super-secret","ttlSeconds":120}`, "")
	if issued.StatusCode != http.StatusOK || !strings.HasPrefix(issuedPayload["data"].(map[string]any)["token"].(string), "juhe_tmp_") {
		t.Fatalf("temporary token issue: %d %v", issued.StatusCode, issuedPayload)
	}
	token := issuedPayload["data"].(map[string]any)["token"].(string)
	lowercaseBearerRequest, err := http.NewRequest(http.MethodGet, server.URL+"/__aisys__/api/auth/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	lowercaseBearerRequest.Header.Set("Authorization", "bearer "+token)
	lowercaseBearerResponse, err := http.DefaultClient.Do(lowercaseBearerRequest)
	if err != nil {
		t.Fatal(err)
	}
	lowercaseBearerResponse.Body.Close()
	if lowercaseBearerResponse.StatusCode != http.StatusOK {
		t.Fatalf("lowercase bearer scheme must authenticate the temporary token: %d", lowercaseBearerResponse.StatusCode)
	}

	// Non-admin accounts are rejected.
	seedAccount(t, deps, "regular", "regular-pass", "user")
	denied, _ := postJSON(t, server, "/__aisys__/api/auth/temporary-access-tokens",
		`{"username":"regular","password":"regular-pass"}`, "")
	if denied.StatusCode != http.StatusUnauthorized {
		t.Fatalf("non-admin temporary token: %d", denied.StatusCode)
	}

	revoke, revokePayload := postJSON(t, server, "/__aisys__/api/auth/temporary-access-tokens/revoke", `{}`, "Authorization: Bearer "+token)
	_ = revokePayload
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/__aisys__/api/auth/temporary-access-tokens/revoke", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer "+token)
	direct, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	direct.Body.Close()
	if revoke.StatusCode != http.StatusOK && direct.StatusCode != http.StatusOK {
		t.Fatal("temporary token revoke failed")
	}
}

func TestTemporaryAccessTokenRequiresAllowlistedSource(t *testing.T) {
	deps, _, server := newTestEnv(t)
	seedAccount(t, deps, "admin1", "super-secret", "super_admin")

	deps.TemporaryAccessIPAllowlist = nil
	denied, deniedPayload := postJSON(t, server, "/__aisys__/api/auth/temporary-access-tokens",
		`{"username":"admin1","password":"super-secret","ttlSeconds":120}`, "")
	if denied.StatusCode != http.StatusForbidden || deniedPayload["message"] != "当前来源不在临时访问令牌白名单中" {
		t.Fatalf("empty allowlist must deny: %d %v", denied.StatusCode, deniedPayload)
	}

	// The Node configuration normalizes IPv4-mapped IPv6 entries to IPv4.
	// Keep the comparison tolerant of the same listener/proxy representation.
	deps.TemporaryAccessIPAllowlist = []string{"::ffff:127.0.0.1"}
	allowed, allowedPayload := postJSON(t, server, "/__aisys__/api/auth/temporary-access-tokens",
		`{"username":"admin1","password":"super-secret","ttlSeconds":120}`, "")
	if allowed.StatusCode != http.StatusOK || !strings.HasPrefix(allowedPayload["data"].(map[string]any)["token"].(string), "juhe_tmp_") {
		t.Fatalf("normalized allowlist must issue: %d %v", allowed.StatusCode, allowedPayload)
	}
}

func TestLogoutRevokeFailureDoesNotReportSuccess(t *testing.T) {
	deps, _, server := newTestEnv(t)
	seedAccount(t, deps, "admin1", "super-secret", "super_admin")
	cookie := login(t, server, "admin1", "super-secret")

	originalPort := deps.Port
	deps.Port = revokeFailingPort{Port: originalPort, err: errors.New("revoke storage failed")}
	logout, logoutPayload := postJSON(t, server, "/__aisys__/api/auth/logout", `{}`, cookie)
	if logout.StatusCode != http.StatusInternalServerError || logoutPayload["message"] != "服务器内部错误" {
		t.Fatalf("logout revoke failure must surface as server failure: %d %v", logout.StatusCode, logoutPayload)
	}
	if cookies := logout.Header.Values("Set-Cookie"); len(cookies) != 0 {
		t.Fatalf("failed logout must not clear a still-valid session cookie: %v", cookies)
	}

	deps.Port = originalPort
	stillAuthenticated, stillAuthenticatedPayload := getJSON(t, server, "/__aisys__/api/auth/me", cookie)
	if stillAuthenticated.StatusCode != http.StatusOK {
		t.Fatalf("failed revoke must leave the existing session valid: %d %v", stillAuthenticated.StatusCode, stillAuthenticatedPayload)
	}
}

func TestLogoutCookieUsesConfiguredSecurityAttributes(t *testing.T) {
	deps, _, server := newTestEnv(t)
	seedAccount(t, deps, "admin1", "super-secret", "super_admin")
	cookie := login(t, server, "admin1", "super-secret")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/logout", nil)
	request.Header.Set("Cookie", cookie)
	deps.postLogout("none", true)(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("configured logout should succeed: %d %s", recorder.Code, recorder.Body.String())
	}
	setCookie := recorder.Header().Get("Set-Cookie")
	if !strings.Contains(setCookie, "SameSite=None") || !strings.Contains(setCookie, "Secure") {
		t.Fatalf("logout cookie must preserve configured SameSite/Secure attributes: %q", setCookie)
	}
}

func TestSessionStorageFailureIsNotReportedAsInvalidToken(t *testing.T) {
	deps, _, server := newTestEnv(t)
	seedAccount(t, deps, "admin1", "super-secret", "super_admin")
	cookie := login(t, server, "admin1", "super-secret")

	deps.Port = authenticateFailingPort{Port: deps.Port, err: errors.New("session storage unavailable")}
	response, payload := getJSON(t, server, "/__aisys__/api/auth/me", cookie)
	if response.StatusCode != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("session storage failure must remain a server failure: %d %v", response.StatusCode, payload)
	}
}

func TestDevelopmentAutoLogin(t *testing.T) {
	deps, _, server := newTestEnv(t)
	seedAccount(t, deps, "devadmin", "dev-password", "super_admin")
	deps.DevAutoLoginUsername = "devadmin"

	response, payload := getJSON(t, server, "/__aisys__/api/auth/me", "")
	if response.StatusCode != http.StatusOK || payload["data"].(map[string]any)["username"] != "devadmin" {
		t.Fatalf("dev auto login: %d %v", response.StatusCode, payload)
	}
}

func cookieSessionToken(cookie string) string {
	parts := strings.SplitN(cookie, "=", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return cookie
}

func accountIDByUsername(t *testing.T, deps *Deps, username string) string {
	t.Helper()
	summary, err := deps.Accounts.FindByUsername(nil, username)
	if err != nil || summary.ID == "" {
		t.Fatalf("account %s missing: %v", username, err)
	}
	return summary.ID
}

func rfc3339NowPlus(t *testing.T, deps *Deps, _ int) string {
	t.Helper()
	return deps.Now().UTC().Format(time.RFC3339Nano)
}

func findItemByUsername(t *testing.T, payload map[string]any, username string) map[string]any {
	t.Helper()
	items := payload["data"].(map[string]any)["items"].([]any)
	for _, raw := range items {
		item := raw.(map[string]any)
		if item["username"] == username {
			return item
		}
	}
	t.Fatalf("account %s not in list", username)
	return nil
}

func patchJSON(t *testing.T, server *httptest.Server, path, body, cookie string) (*http.Response, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPatch, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Cookie", cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	return response, payload
}
