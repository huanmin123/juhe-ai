package modelcheckauth

// w11e modelcheckauth 错误臂：HTTP 路由参数/守卫/撤销链、会话与凭据存取、
// 验证码分支与登录守卫窗口的剩余分支。

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crypto/pbkdf2"
	_ "modernc.org/sqlite"
)

func w11ePasswordHash(password string) string {
	salt := "MDEyMzQ1Njc4OWFiY2RlZg"
	derived, err := pbkdf2.Key(sha512.New, password, []byte(salt), 120000, 32)
	if err != nil {
		panic(err)
	}
	return "pbkdf2$sha512$120000$" + salt + "$" + base64.RawURLEncoding.EncodeToString(derived)
}

func w11eAuthDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "w11e-auth.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE system_accounts (id TEXT PRIMARY KEY,username TEXT NOT NULL,display_name TEXT,role TEXT NOT NULL,status TEXT NOT NULL,password_hash TEXT NOT NULL,must_change_password INTEGER NOT NULL,last_login_at TEXT,updated_at TEXT NOT NULL)`,
		`CREATE TABLE system_sessions (id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,token_hash TEXT UNIQUE,expires_at TEXT,created_at TEXT,last_seen_at TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO system_accounts VALUES ('w11e-acct','w11e-admin','管理员','admin','active',?,0,'','')`, w11ePasswordHash("w11e-password")); err != nil {
		t.Fatal(err)
	}
	return db
}

func w11eAuth(t *testing.T, db *sql.DB, now time.Time) *Authenticator {
	t.Helper()
	auth, err := New(db, SQLite, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

func w11eLoginSession(t *testing.T, handler *HTTPHandler) *http.Cookie {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"w11e-admin","password":"w11e-password"}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("登录失败: %d %s", recorder.Code, recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("登录未返回 cookie")
	}
	return cookies[0]
}

func TestW11EHandlerWiringAndCaptchaArms(t *testing.T) {
	var nilHandler *HTTPHandler
	recorder := httptest.NewRecorder()
	nilHandler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/captcha", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil handler=%d", recorder.Code)
	}
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	db := w11eAuthDB(t)
	auth := w11eAuth(t, db, now)
	handler := &HTTPHandler{Auth: auth}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/captcha", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"required":false`) {
		t.Fatalf("未接线验证码=%d %s", recorder.Code, recorder.Body.String())
	}
	// Auth 未接线但访问受保护路由。
	handler = &HTTPHandler{}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/login", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("未接线 auth=%d", recorder.Code)
	}
	// 未知路由。
	handler = &HTTPHandler{Auth: auth}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/w11e-unknown", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("未知路由=%d", recorder.Code)
	}
	// clientIP 边臂。
	request := httptest.NewRequest(http.MethodGet, "/me", nil)
	request.RemoteAddr = "no-port-addr"
	if ip := clientIP(request); ip != "no-port-addr" {
		t.Fatalf("无端口地址=%q", ip)
	}
	request.RemoteAddr = "[::ffff:10.0.0.1]:1234"
	if ip := clientIP(request); ip != "10.0.0.1" {
		t.Fatalf("映射地址=%q", ip)
	}
	request.RemoteAddr = "::ffff:10.0.0.1:1234"
	if ip := clientIP(request); ip != "10.0.0.1:1234" {
		t.Fatalf("未加括号的映射地址按原文回退=%q", ip)
	}
	if ip := clientIP(nil); ip != "" {
		t.Fatalf("nil 请求=%q", ip)
	}
	// 临时访问白名单归一。
	if !TemporaryAccessIPAllowed("::ffff:127.0.0.1", []string{"127.0.0.1"}) {
		t.Fatal("映射 IPv4 必须允许")
	}
	if TemporaryAccessIPAllowed("", []string{"127.0.0.1"}) || TemporaryAccessIPAllowed("127.0.0.1", nil) {
		t.Fatal("空名单必须全部拒绝")
	}
}

func TestW11ETemporaryAccessTokenArms(t *testing.T) {
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	db := w11eAuthDB(t)
	auth := w11eAuth(t, db, now)
	handler := &HTTPHandler{Auth: auth, TemporaryAccessIPAllowlist: []string{"127.0.0.1", "127.0.0.2", "127.0.0.3"}, Guard: NewLoginGuard(func() time.Time { return now })}
	post := func(body string, remote string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/temporary-access-tokens", strings.NewReader(body))
		request.RemoteAddr = remote
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	// 参数无效与 TTL 边界。
	for _, body := range []string{
		`{invalid`,
		`{"username":" ","password":"p"}`,
		`{"username":"w11e admin","password":"p"}`,
		`{"username":"w11e-admin","password":"pass word"}`,
		`{"username":"w11e-admin","password":"p","ttlSeconds":30}`,
		`{"username":"w11e-admin","password":"p","ttlSeconds":7200}`,
		`{"username":"w11e-admin","password":"p","unexpected":1}`,
	} {
		if recorder := post(body, "127.0.0.1:9000"); recorder.Code != http.StatusBadRequest {
			t.Fatalf("参数 %s 必须拒绝: %d %s", body, recorder.Code, recorder.Body.String())
		}
	}
	// 来源不在白名单。
	if recorder := post(`{"username":"w11e-admin","password":"w11e-password","ttlSeconds":120}`, "10.9.9.9:9000"); recorder.Code != http.StatusForbidden {
		t.Fatalf("非白名单=%d", recorder.Code)
	}
	// 成功签发（在失败锁定前验证）。
	if recorder := post(`{"username":"w11e-admin","password":"w11e-password","ttlSeconds":120}`, "127.0.0.2:9000"); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"tokenType":"Bearer"`) {
		t.Fatalf("签发=%d %s", recorder.Code, recorder.Body.String())
	}
	// 错误密码。
	if recorder := post(`{"username":"w11e-admin","password":"wrong"}`, "127.0.0.1:9000"); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("错误密码=%d %s", recorder.Code, recorder.Body.String())
	}
	// 连续失败触发 IP 锁定（含前一次共 10 次）。
	for index := 0; index < 9; index++ {
		post(`{"username":"w11e-admin","password":"wrong"}`, "127.0.0.1:9000")
	}
	if recorder := post(`{"username":"w11e-admin","password":"w11e-password","ttlSeconds":120}`, "127.0.0.1:9000"); recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("锁定后=%d", recorder.Code)
	}
	// must_change_password 账户拒绝签发（重置换守卫绕开用户名锁）。
	handler.Guard = NewLoginGuard(func() time.Time { return now })
	if _, err := db.Exec(`UPDATE system_accounts SET must_change_password=1 WHERE id='w11e-acct'`); err != nil {
		t.Fatal(err)
	}
	if recorder := post(`{"username":"w11e-admin","password":"w11e-password","ttlSeconds":120}`, "127.0.0.3:9000"); recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "初始密码") {
		t.Fatalf("需改密=%d %s", recorder.Code, recorder.Body.String())
	}
	if _, err := db.Exec(`UPDATE system_accounts SET must_change_password=0 WHERE id='w11e-acct'`); err != nil {
		t.Fatal(err)
	}
	// 撤销：非法令牌 / 无效令牌。
	handler2 := &HTTPHandler{Auth: auth}
	request := httptest.NewRequest(http.MethodPost, "/temporary-access-tokens/revoke", nil)
	request.Header.Set("Authorization", "Bearer not-a-temporary-token")
	recorder := httptest.NewRecorder()
	handler2.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法撤销=%d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/temporary-access-tokens/revoke", nil)
	request.Header.Set("Authorization", "Bearer "+w11eTemporaryTokenShape())
	recorder = httptest.NewRecorder()
	handler2.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("无效撤销=%d", recorder.Code)
	}
}

// w11eTemporaryTokenShape 生成形状合法但未登记的临时令牌。
func w11eTemporaryTokenShape() string {
	digest := sha256.Sum256([]byte("w11e-unregistered"))
	return "juhe_tmp_" + hex.EncodeToString(digest[:])[:22] + "abcdefghijklmnopqrstu"
}

func TestW11ELoginLogoutMeProfileArms(t *testing.T) {
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	db := w11eAuthDB(t)
	auth := w11eAuth(t, db, now)
	handler := &HTTPHandler{Auth: auth, Guard: NewLoginGuard(func() time.Time { return now })}
	// 登录参数无效。
	for _, body := range []string{
		`{invalid`,
		`{"username":" ","password":"p"}`,
		`{"username":"w11e admin","password":"p"}`,
		`{"username":"w11e-admin","password":"pass word"}`,
		`{"username":"w11e-admin","unexpected":1}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("登录参数 %s 必须拒绝: %d", body, recorder.Code)
		}
	}
	// 错误密码 → 401；逐步累积失败（每次请求新建，body 不可复用）。
	for index := 0; index < 5; index++ {
		wrong := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"w11e-admin","password":"wrong"}`))
		wrong.RemoteAddr = "127.0.0.5:9000"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, wrong)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("错误密码=%d", recorder.Code)
		}
	}
	// /me 缺令牌。
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/me", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("缺令牌=%d", recorder.Code)
	}
	// 有效会话。
	session := w11eLoginSession(t, handler)
	// profile 参数无效。
	profile := httptest.NewRequest(http.MethodPatch, "/me", strings.NewReader(`{"displayName":" "}`))
	profile.AddCookie(session)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, profile)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("profile 参数=%d", recorder.Code)
	}

}

func TestW11EChangePasswordArms(t *testing.T) {
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	db := w11eAuthDB(t)
	auth := w11eAuth(t, db, now)
	handler := &HTTPHandler{Auth: auth, Guard: NewLoginGuard(func() time.Time { return now })}
	session := w11eLoginSession(t, handler)
	change := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(body))
		request.AddCookie(session)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	// 密码参数无效。
	for _, body := range []string{
		`{invalid`,
		`{"newPassword":"abc"}`,
		`{"newPassword":"pass word"}`,
		`{"newPassword":"validpassword","oldPassword":"pass word"}`,
	} {
		if recorder := change(body); recorder.Code != http.StatusBadRequest {
			t.Fatalf("密码参数 %s=%d", body, recorder.Code)
		}
	}
	// 当前密码错误。
	if recorder := change(`{"oldPassword":"wrong","newPassword":"validpassword"}`); recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "当前密码不正确") {
		t.Fatalf("错误旧密码=%d %s", recorder.Code, recorder.Body.String())
	}
	// 成功修改。
	if recorder := change(`{"oldPassword":"w11e-password","newPassword":"w11e-new-password"}`); recorder.Code != http.StatusOK {
		t.Fatalf("修改密码=%d %s", recorder.Code, recorder.Body.String())
	}
	// must_change_password 流程（新账户带初始密码）。
	if _, err := db.Exec(`UPDATE system_accounts SET must_change_password=1 WHERE id='w11e-acct'`); err != nil {
		t.Fatal(err)
	}
	login2 := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"w11e-admin","password":"w11e-new-password"}`))
	login2.RemoteAddr = "127.0.0.9:9000"
	login2Recorder := httptest.NewRecorder()
	handler.ServeHTTP(login2Recorder, login2)
	cookies := login2Recorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("第二次登录未返回 cookie")
	}
	forced := httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(`{"newPassword":"w11e-final-password"}`))
	forced.AddCookie(cookies[0])
	forcedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(forcedRecorder, forced)
	if forcedRecorder.Code != http.StatusOK {
		t.Fatalf("强制改密=%d %s", forcedRecorder.Code, forcedRecorder.Body.String())
	}
}

func TestW11ESessionStoreArms(t *testing.T) {
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	db := w11eAuthDB(t)
	auth := w11eAuth(t, db, now)
	ctx := context.Background()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	// canceled Login 失败。
	if _, _, _, err := auth.Login(canceled, "w11e-admin", "w11e-password", 10); err == nil {
		t.Fatal("canceled login 必须失败")
	}
	session, account, ok, err := auth.Login(ctx, "w11e-admin", "w11e-password", 10)
	if err != nil || !ok || account.Username != "w11e-admin" {
		t.Fatalf("登录=%+v err=%v", account, err)
	}
	// canceled 撤销。
	if err := auth.RevokeToken(canceled, session.Token); err == nil {
		t.Fatal("canceled revoke 必须失败")
	}
	if err := auth.RevokeToken(ctx, session.Token); err != nil {
		t.Fatalf("撤销=%v", err)
	}
	// 再登录 + 撤销其他会话。
	session, _, ok, err = auth.Login(ctx, "w11e-admin", "w11e-password", 10)
	if err != nil || !ok {
		t.Fatalf("再登录 err=%v", err)
	}
	other := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"w11e-admin","password":"w11e-password"}`))
	other.RemoteAddr = "127.0.0.7:9000"
	otherRecorder := httptest.NewRecorder()
	(&HTTPHandler{Auth: auth}).ServeHTTP(otherRecorder, other)
	if err := auth.RevokeOtherSessions(canceled, "w11e-acct", session.SessionID); err == nil {
		t.Fatal("canceled revoke-others 必须失败")
	}
	if err := auth.RevokeOtherSessions(ctx, "w11e-acct", session.SessionID); err != nil {
		t.Fatalf("撤销其他会话=%v", err)
	}
	// CurrentCredentialRevision / UpdateDisplayName / CleanupExpiredSessions。
	if revision, err := auth.CurrentCredentialRevision(canceled, "w11e-acct"); err == nil || revision != "" {
		t.Fatalf("canceled revision=%q err=%v", revision, err)
	}
	if revision, err := auth.CurrentCredentialRevision(ctx, "w11e-missing"); err != nil || revision != "" {
		t.Fatalf("缺失账户 revision=%q err=%v", revision, err)
	}
	if _, err := auth.UpdateDisplayName(canceled, "w11e-acct", "新名字"); err == nil {
		t.Fatal("canceled 更新资料必须失败")
	}
	if changed, err := auth.UpdateDisplayName(ctx, "w11e-missing", "新名字"); err != nil || changed {
		t.Fatalf("缺失账户资料=%t err=%v", changed, err)
	}
	// 过期会话清理（canceled + 正常）。
	if _, err := auth.CleanupExpiredSessions(canceled, now, 10); err == nil {
		t.Fatal("canceled 清理必须失败")
	}
	if _, err := db.Exec(`UPDATE system_sessions SET expires_at='2026-09-16T09:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	deleted, err := auth.CleanupExpiredSessions(ctx, now, 10)
	if err != nil || deleted == 0 {
		t.Fatalf("清理=%d err=%v", deleted, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM system_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("清理后=%d err=%v", count, err)
	}
	// ChangePassword 修订失配。
	if changed, err := auth.ChangePassword(ctx, "w11e-acct", "stale-revision", "w11e-password-2", "session-id"); err != nil || changed {
		t.Fatalf("修订失配=%t err=%v", changed, err)
	}
	// 会话表被删后 Login 失败关闭。
	if _, err := db.Exec(`DROP TABLE system_sessions`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := auth.Login(ctx, "w11e-admin", "w11e-password", 10); err == nil {
		t.Fatal("缺会话表必须失败")
	}
}

func TestW11ECaptchaAndGuardArms(t *testing.T) {
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	service := NewCaptchaService(func() time.Time { return now })
	// 未登记/过期验证码拒绝。
	if service.Verify("w11e-missing", "0000") {
		t.Fatal("未登记验证码必须失败")
	}
	result, err := service.Issue("127.0.0.1")
	if err != nil || result.Blocked {
		t.Fatalf("签发=%+v err=%v", result, err)
	}
	// Verify 单次有效：错误答案同样消耗记录。
	if service.Verify(result.Challenge.CaptchaID, "0000") {
		t.Fatal("错误答案必须失败")
	}
	secondIssue, err := service.Issue("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	answer := service.AnswerForTest(secondIssue.Challenge.CaptchaID)
	if answer == "" {
		t.Fatal("测试答案不可为空")
	}
	if !service.Verify(secondIssue.Challenge.CaptchaID, answer) {
		t.Fatal("正确答案必须通过")
	}
	// 已用验证码不可重放。
	if service.Verify(secondIssue.Challenge.CaptchaID, answer) {
		t.Fatal("验证码重放必须失败")
	}
	// 过期验证码拒绝。
	third, err := service.Issue("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now.Add(2 * time.Hour) }
	if service.Verify(third.Challenge.CaptchaID, service.AnswerForTest(third.Challenge.CaptchaID)) {
		t.Fatal("过期验证码必须失败")
	}
	// 登录守卫窗口。
	guard := NewLoginGuard(func() time.Time { return now })
	for index := 0; index < 10; index++ {
		if blocked, _, _ := guard.Failed("127.0.0.8", "user"); blocked && index < 9 {
			t.Fatalf("过早锁定 index=%d", index)
		}
	}
	blocked, retry, message := guard.Check("127.0.0.8", "user")
	if !blocked || retry <= 0 || message == "" {
		t.Fatalf("锁定=%t retry=%d message=%q", blocked, retry, message)
	}
	// 成功清零。
	guard.Success("127.0.0.8", "user")
	if blocked, _, _ := guard.Check("127.0.0.8", "user"); blocked {
		t.Fatal("成功后必须解锁")
	}
	// 窗口外失败清理。
	stale := NewLoginGuard(func() time.Time { return now })
	for index := 0; index < 10; index++ {
		_, _, _ = stale.Failed("127.0.0.9", "user")
	}
	stale.now = func() time.Time { return now.Add(2 * time.Hour) }
	if blocked, _, _ := stale.Check("127.0.0.9", "user"); blocked {
		t.Fatal("窗口外失败必须清理")
	}
}
