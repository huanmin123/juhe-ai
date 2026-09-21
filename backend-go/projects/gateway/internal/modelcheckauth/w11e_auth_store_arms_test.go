package modelcheckauth

// w11e modelcheckauth 第二批错误臂：CheckContract、token 认证时间语义、
// 会话存取错误路径、验证码限流与守卫 nil/default 分支。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestW11ECheckContractArms(t *testing.T) {
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	var nilAuth *Authenticator
	if err := nilAuth.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("nil authenticator 必须拒绝: %v", err)
	}
	db := w11eAuthDB(t)
	auth := w11eAuth(t, db, now)
	if err := auth.CheckContract(context.Background()); err != nil {
		t.Fatalf("契约必须通过: %v", err)
	}
	// 缺列必须拒绝。
	if _, err := db.Exec(`ALTER TABLE system_sessions DROP COLUMN last_seen_at`); err != nil {
		t.Fatal(err)
	}
	if err := auth.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "system_sessions") {
		t.Fatalf("缺列必须拒绝: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.Exec(`ALTER TABLE system_sessions ADD COLUMN last_seen_at TEXT`); err != nil {
		t.Fatal(err)
	}
	if err := auth.CheckContract(canceled); err == nil {
		t.Fatal("canceled 契约必须失败")
	}
}

func TestW11EAuthenticateTokenArms(t *testing.T) {
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	db := w11eAuthDB(t)
	auth := w11eAuth(t, db, now)
	ctx := context.Background()
	// 空令牌。
	if _, err := auth.authenticateToken(ctx, "  ", false, false); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("空令牌=%v", err)
	}
	// 无效令牌。
	if _, err := auth.AuthenticateTokenForSession(ctx, w11eTemporaryTokenShape()); err == nil {
		t.Fatal("未登记令牌必须过期")
	}
	// 建立真实会话。
	handler := &HTTPHandler{Auth: auth}
	cookie := w11eLoginSession(t, handler)
	// 过期会话。
	if _, err := db.Exec(`UPDATE system_sessions SET expires_at='2026-09-16T09:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/me", nil)
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("过期会话=%d", recorder.Code)
	}
	// 损坏时间格式失败关闭。
	if _, err := db.Exec(`UPDATE system_sessions SET expires_at='not-a-time'`); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AuthenticateTokenForSession(ctx, cookie.Value); err == nil {
		t.Fatal("损坏过期时间必须失败关闭")
	}
	if _, err := db.Exec(`UPDATE system_sessions SET expires_at=?`, now.Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE system_sessions SET last_seen_at='not-a-time'`); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AuthenticateTokenForSession(ctx, cookie.Value); err == nil {
		t.Fatal("损坏 last_seen 必须失败关闭")
	}
	// touch 路径（last_seen 距今超过一分钟）。
	if _, err := db.Exec(`UPDATE system_sessions SET last_seen_at=?`, now.Add(-2*time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AuthenticateTokenForSession(ctx, cookie.Value); err != nil {
		t.Fatalf("touch 会话=%v", err)
	}
	var seen string
	if err := db.QueryRow(`SELECT last_seen_at FROM system_sessions`).Scan(&seen); err != nil || seen == "" {
		t.Fatalf("touch 后 last_seen=%q err=%v", seen, err)
	}
	// canceled touch 失败。
	if _, err := db.Exec(`UPDATE system_sessions SET last_seen_at=?`, now.Add(-2*time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := auth.AuthenticateTokenForSession(canceled, cookie.Value); err == nil {
		t.Fatal("canceled 认证必须失败")
	}
	// Authenticate nil authenticator。
	var nilAuth *Authenticator
	if _, err := nilAuth.Authenticate(ctx, "", ""); err == nil {
		t.Fatal("nil Authenticate 必须失败")
	}
	if _, err := nilAuth.RequireAdmin(ctx, "", ""); err == nil {
		t.Fatal("nil RequireAdmin 必须失败")
	}
	// 非管理员角色。
	if _, err := db.Exec(`UPDATE system_accounts SET role='user'`); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.RequireAdmin(ctx, "Bearer "+cookie.Value, ""); err == nil {
		t.Fatal("非管理员必须拒绝")
	}
}

func TestW11ELogoutAndJSONArms(t *testing.T) {
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	db := w11eAuthDB(t)
	auth := w11eAuth(t, db, now)
	handler := &HTTPHandler{Auth: auth}
	cookie := w11eLoginSession(t, handler)
	// logout 撤销失败（会话表被删）。
	if _, err := db.Exec(`DROP TABLE system_sessions`); err != nil {
		t.Fatal(err)
	}
	logout := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logout.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, logout)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("logout 撤销失败=%d", recorder.Code)
	}
}

func TestW11ECaptchaRateLimitArms(t *testing.T) {
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	service := NewCaptchaService(func() time.Time { return now })
	// 同一 IP 连续签发触发限流。
	blocked := false
	for index := 0; index < 61 && !blocked; index++ {
		result, err := service.Issue("127.0.0.4")
		if err != nil {
			t.Fatal(err)
		}
		if result.Blocked {
			blocked = true
			if result.RetryAfter <= 0 || result.Message == "" {
				t.Fatalf("限流载荷=%+v", result)
			}
		}
	}
	if !blocked {
		t.Fatal("连续签发必须触发限流")
	}
	// 其他 IP 不受限流影响。
	if result, err := service.Issue("127.0.0.5"); err != nil || result.Blocked {
		t.Fatalf("其他 IP=%+v err=%v", result, err)
	}
	// 验证码大小写归一。
	normal, err := service.Issue("127.0.0.6")
	if err != nil {
		t.Fatal(err)
	}
	answer := service.AnswerForTest(normal.Challenge.CaptchaID)
	if answer == "" {
		t.Fatal("答案不可为空")
	}
	if !service.Verify(normal.Challenge.CaptchaID, strings.ToLower(answer)) {
		t.Fatal("验证码必须大小写不敏感")
	}
}

func TestW11EGuardNilAndDefaultArms(t *testing.T) {
	var nilGuard *LoginGuard
	if blocked, retry, message, err := nilGuard.Check("127.0.0.1", "user"); blocked || retry != 0 || message != "" || err != nil {
		t.Fatal("nil Check 必须放行")
	}
	if blocked, _, _, err := nilGuard.Failed("127.0.0.1", "user"); blocked || err != nil {
		t.Fatal("nil Failed 必须放行")
	}
	nilGuard.Success("127.0.0.1", "user")
	// 默认时钟守卫。
	guard := NewLoginGuard(nil)
	if blocked, _, _, err := guard.Check("127.0.0.1", "user"); blocked || err != nil {
		t.Fatal("默认时钟必须可用")
	}
	// 用户名锁定跨 IP 生效。
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	userLock := NewLoginGuard(func() time.Time { return now })
	var message string
	for index := 0; index < 10; index++ {
		_, _, message, _ = userLock.Failed("10.1.0."+strconv.Itoa(index), "SharedUser")
	}
	if !strings.Contains(message, "账号暂时锁定") {
		t.Fatalf("用户名锁定=%q", message)
	}
	if blocked, _, _, _ := userLock.Check("10.2.0.1", "shareduser"); !blocked {
		t.Fatal("用户名锁必须跨 IP 且大小写不敏感")
	}
}

func TestW11EHTTPJSONDecodeArms(t *testing.T) {
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	db := w11eAuthDB(t)
	auth := w11eAuth(t, db, now)
	handler := &HTTPHandler{Auth: auth, Guard: NewLoginGuard(func() time.Time { return now })}
	cookie := w11eLoginSession(t, handler)
	// profile 非法 JSON。
	profile := httptest.NewRequest(http.MethodPatch, "/me", strings.NewReader(`{invalid`))
	profile.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, profile)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("profile 非法 JSON=%d", recorder.Code)
	}
	// change-password 非法 JSON。
	change := httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(`{invalid`))
	change.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, change)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("change 非法 JSON=%d", recorder.Code)
	}
	// cookie 转义失败。
	badCookie := httptest.NewRequest(http.MethodGet, "/me", nil)
	badCookie.Header.Set("Cookie", SessionCookieName+"=%zz")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, badCookie)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("坏 cookie=%d", recorder.Code)
	}
	// 尾随 JSON 对象拒绝。
	trailing := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"w11e-admin","password":"x"} {}`))
	trailing.RemoteAddr = "127.0.77.1:9000"
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, trailing)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("尾随对象=%d", recorder.Code)
	}
}
