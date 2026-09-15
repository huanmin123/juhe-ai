package modelcheckauth

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha512"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const w7bPassword = "correcthorsebatterystaple"

func w7bOpenAuth(t *testing.T, name string) (*sql.DB, *Authenticator, time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), name+".db")+"?mode=rwc")
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
	salt := "MDEyMzQ1Njc4OWFiY2RlZg"
	derived, err := pbkdf2.Key(sha512.New, w7bPassword, []byte(salt), 120000, 32)
	if err != nil {
		t.Fatal(err)
	}
	passwordHash := "pbkdf2$sha512$120000$" + salt + "$" + base64.RawURLEncoding.EncodeToString(derived)
	for _, row := range []struct {
		id, username, role, status string
		mustChange                 int
	}{
		{"acct", "admin", "admin", "active", 0},
		{"mustch", "mustchange", "admin", "active", 1},
		{"viewer", "viewer", "viewer", "active", 0},
		{"disabled", "disabled", "admin", "disabled", 0},
		{"junk", "junk", "admin", "active", 0},
	} {
		hash := passwordHash
		if row.id == "junk" {
			hash = "garbage-hash"
		}
		if _, err := db.Exec(`INSERT INTO system_accounts VALUES (?,?,?,?,?,?,?, '', '')`, row.id, row.username, row.username, row.role, row.status, hash, row.mustChange); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	auth, err := New(db, SQLite, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return db, auth, now
}

// w7b：Authenticator 直驱面（会话生命周期 + 校验分支 + Postgres 语义适配）。
func TestW7BAuthenticatorDirectSurface(t *testing.T) {
	db, auth, now := w7bOpenAuth(t, "direct")
	ctx := context.Background()

	if _, err := New(nil, SQLite, nil); err == nil {
		t.Fatal("nil db 必须报错")
	}
	if _, err := New(db, Mode(9), nil); err == nil {
		t.Fatal("非法 mode 必须报错")
	}
	if err := auth.CheckContract(ctx); err != nil {
		t.Fatalf("CheckContract = %v", err)
	}
	emptyDB, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "empty.db")+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	emptyAuth, err := New(emptyDB, SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := emptyAuth.CheckContract(ctx); err == nil {
		t.Fatal("缺表契约必须报错")
	}
	_ = emptyDB.Close()

	// Login：ttlDays 0 → 收敛 1 天；未知账户/错误密码 → 未授权。
	issued, verified, ok, err := auth.Login(ctx, "ADMIN", w7bPassword, 0)
	if err != nil || !ok || issued.Token == "" || !strings.HasPrefix(issued.SessionID, "sess-") || verified.CredentialRevision == "" {
		t.Fatalf("login issued=%+v verified=%+v ok=%v err=%v", issued, verified, ok, err)
	}
	if !issued.ExpiresAt.Equal(now.Add(24 * time.Hour)) {
		t.Fatalf("默认 ttl 到期 = %v", issued.ExpiresAt)
	}
	if _, _, ok, _ := auth.Login(ctx, "ghost", w7bPassword, 1); ok {
		t.Fatal("未知账户不得登录")
	}
	if _, _, ok, _ := auth.Login(ctx, "admin", "wrong", 1); ok {
		t.Fatal("错误密码不得登录")
	}
	if _, _, ok, _ := auth.Login(ctx, "disabled", w7bPassword, 1); ok {
		t.Fatal("非 active 账户不得登录")
	}
	if _, _, ok, _ := auth.Login(ctx, "junk", w7bPassword, 1); ok {
		t.Fatal("坏哈希账户不得登录")
	}

	// CreateSession：未知/非激活账户与非法输入。
	if _, ok, _ := auth.CreateAuthenticatedSession(ctx, "ghost", "rev", 1); ok {
		t.Fatal("未知账户不得建会话")
	}
	if _, ok, _ := auth.CreateAuthenticatedSession(ctx, "", "rev", 1); ok {
		t.Fatal("空账户不得建会话")
	}
	revision, err := auth.CurrentCredentialRevision(ctx, "acct")
	if err != nil || revision == "" {
		t.Fatalf("revision = %q err=%v", revision, err)
	}
	if _, err := auth.CurrentCredentialRevision(ctx, "ghost"); err != nil || revision == "" {
		t.Fatalf("未知账户 revision = %q err=%v", revision, err)
	}
	if _, err := auth.CurrentCredentialRevision(ctx, ""); err == nil {
		t.Fatal("空账户 revision 必须报错")
	}
	long, ok, err := auth.CreateAuthenticatedSession(ctx, "acct", revision, 30)
	if err != nil || !ok || !long.ExpiresAt.Equal(now.Add(14*24*time.Hour)) {
		t.Fatalf("ttl 收敛 = %+v ok=%v err=%v", long, ok, err)
	}
	tmp, ok, err := auth.CreateTemporaryAccessToken(ctx, "acct", revision, 0)
	if err != nil || !ok || !strings.HasPrefix(tmp.Token, "juhe_tmp_") || !strings.HasPrefix(tmp.SessionID, "tmp_sess-") {
		t.Fatalf("temp token = %+v ok=%v err=%v", tmp, ok, err)
	}
	if !temporaryToken.MatchString(tmp.Token) {
		t.Fatal("临时令牌必须匹配 Node 正则")
	}
	if _, _, err := auth.CreateTemporaryAccessToken(ctx, "", "", -5); err == nil {
		t.Fatal("非法输入必须报错")
	}

	// authenticateToken：过期/坏时间戳/touch CAS。
	expiredAt := now.Add(-time.Hour).UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
	if _, err := db.Exec(`INSERT INTO system_sessions VALUES ('expired','acct',?,?,?,?)`, hashString("expired-hash"), expiredAt, expiredAt, expiredAt); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AuthenticateToken(ctx, "expired-hash"); err == nil {
		t.Fatal("过期会话必须报错")
	}
	if _, err := db.Exec(`INSERT INTO system_sessions VALUES ('broken','acct',?,'not-a-time','x','y')`, hashString("broken-hash")); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AuthenticateTokenForSession(ctx, "broken-hash"); !strings.Contains(err.Error(), "过期") {
		t.Fatalf("坏时间戳必须按过期处理: %v", err)
	}
	// last_seen 61 秒前 → touch 写入新时间；窗口内 → 不写。
	staleSeen := now.Add(-61 * time.Second).UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
	freshSeen := now.Add(-10 * time.Second).UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
	if _, err := db.Exec(`INSERT INTO system_sessions VALUES ('stale','acct',?, ?, ?, ?)`, hashString("stale-hash"), nodeISOTime(now.Add(time.Hour)), staleSeen, staleSeen); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_sessions VALUES ('fresh','acct',?,?,?,?)`, hashString("fresh-hash"), nodeISOTime(now.Add(time.Hour)), freshSeen, freshSeen); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AuthenticateTokenForSession(ctx, "stale-hash"); err != nil {
		t.Fatal(err)
	}
	var seenText string
	if err := db.QueryRow(`SELECT last_seen_at FROM system_sessions WHERE id='stale'`).Scan(&seenText); err != nil || seenText != nodeISOTime(now) {
		t.Fatalf("touch CAS = %q err=%v", seenText, err)
	}
	if _, err := auth.AuthenticateTokenForSessionNoTouch(ctx, "fresh-hash"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT last_seen_at FROM system_sessions WHERE id='fresh'`).Scan(&seenText); err != nil || seenText != freshSeen {
		t.Fatalf("窗口内不得 touch = %q err=%v", seenText, err)
	}
	if actor, err := auth.AuthenticateTokenForSession(ctx, tmp.Token); err != nil || !actor.MustChangePassword == false {
		_ = actor
	}
	// must-change 账户会话：ForSession 放行、Authenticate 拒绝。
	mustIssued, _, ok, err := auth.Login(ctx, "mustchange", w7bPassword, 1)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if actor, err := auth.AuthenticateTokenForSession(ctx, mustIssued.Token); err != nil || !actor.MustChangePassword {
		t.Fatalf("must-change ForSession = %+v err=%v", actor, err)
	}
	if _, err := auth.AuthenticateToken(ctx, mustIssued.Token); err == nil {
		t.Fatal("must-change 必须被 Authenticate 拒绝")
	}

	// Authenticate / RequireAdmin / resolveToken 各分支。
	if _, err := auth.Authenticate(ctx, "", ""); err == nil {
		t.Fatal("缺令牌必须报错")
	}
	if _, err := auth.Authenticate(ctx, "Bearer "+issued.Token, ""); err == nil {
		t.Fatal("非临时令牌的 Bearer 必须拒绝")
	}
	if _, err := auth.Authenticate(ctx, "Basic abc", ""); err == nil {
		t.Fatal("非 Bearer 必须拒绝")
	}
	if _, err := auth.RequireAdmin(ctx, "Bearer "+tmp.Token, ""); err != nil {
		t.Fatalf("RequireAdmin admin = %v", err)
	}
	viewerIssued, _, ok, err := auth.Login(ctx, "viewer", w7bPassword, 1)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := auth.RequireAdmin(ctx, "Bearer "+viewerIssued.Token, ""); err == nil {
		t.Fatal("viewer 必须被拒绝")
	}
	cookieHeader := SessionCookieName + "=" + tmp.Token
	if _, err := auth.Authenticate(ctx, "", cookieHeader); err != nil {
		t.Fatalf("cookie 认证 = %v", err)
	}
	if _, err := resolveToken("", SessionCookieName+"=%zz"); err == nil {
		t.Fatal("坏转义 cookie 必须报错")
	}

	// 吊销族 + 清理。
	if err := auth.RevokeSession(ctx, long.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AuthenticateTokenForSession(ctx, long.Token); err == nil {
		t.Fatal("吊销后必须失效")
	}
	if err := auth.RevokeSession(ctx, " "); err == nil {
		t.Fatal("空 sessionID 必须报错")
	}
	if err := auth.RevokeToken(ctx, ""); err == nil {
		t.Fatal("空 token 必须报错")
	}
	if err := auth.RevokeOtherSessions(ctx, "", "keep"); err == nil {
		t.Fatal("空账户必须报错")
	}
	if err := auth.RevokeOtherSessions(ctx, "acct", "keep-none"); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.CleanupExpiredSessions(ctx, now, 0); err == nil {
		t.Fatal("limit 0 必须报错")
	}
	if _, err := auth.CleanupExpiredSessions(ctx, now, 10001); err == nil {
		t.Fatal("limit 超限必须报错")
	}
	// RevokeOtherSessions 已清空 acct 会话：补插过期会话供清理。
	if _, err := db.Exec(`INSERT INTO system_sessions VALUES ('w7b-expired','acct',?,?,?,?)`,
		hashString("w7b-expired-token"), expiredAt, expiredAt, expiredAt); err != nil {
		t.Fatal(err)
	}
	count, err := auth.CleanupExpiredSessions(ctx, now, 100)
	if err != nil || count < 1 {
		t.Fatalf("清理 = %d err=%v", count, err)
	}

	// ChangePassword：陈旧 CAS / 未知账户 / 非法输入 / 成功。
	if changed, err := auth.ChangePassword(ctx, "acct", "stale", "newpassword", "keep"); err != nil || changed {
		t.Fatalf("陈旧 CAS = %v err=%v", changed, err)
	}
	if changed, err := auth.ChangePassword(ctx, "ghost", "rev", "newpassword", "keep"); err != nil || changed {
		t.Fatalf("未知账户 = %v err=%v", changed, err)
	}
	if _, err := auth.ChangePassword(ctx, "", "", "", ""); err == nil {
		t.Fatal("非法输入必须报错")
	}
	changed, err := auth.ChangePassword(ctx, "acct", revision, "newpassword", issued.SessionID)
	if err != nil || !changed {
		t.Fatalf("改密 = %v err=%v", changed, err)
	}
	var mustAfter int
	if err := db.QueryRow(`SELECT must_change_password FROM system_accounts WHERE id='acct'`).Scan(&mustAfter); err != nil || mustAfter != 0 {
		t.Fatalf("改密后必须清除 must_change = %d err=%v", mustAfter, err)
	}

	// UpdateDisplayName。
	if changed, err := auth.UpdateDisplayName(ctx, "acct", "新名字"); err != nil || !changed {
		t.Fatalf("改名 = %v err=%v", changed, err)
	}
	if changed, err := auth.UpdateDisplayName(ctx, "ghost", "x"); err != nil || changed {
		t.Fatalf("未知账户改名 = %v err=%v", changed, err)
	}
	if _, err := auth.UpdateDisplayName(ctx, "", ""); err == nil {
		t.Fatal("空输入必须报错")
	}

	// Postgres 模式语义：表前缀与占位符绑定。
	pg := &Authenticator{mode: Postgres}
	if pg.table("system_sessions") != "juhe_business.system_sessions" {
		t.Fatal("Postgres 表前缀错误")
	}
	if got := pg.bind("SELECT * FROM t WHERE a=? AND b=?"); got != "SELECT * FROM t WHERE a=$1 AND b=$2" {
		t.Fatalf("bind = %q", got)
	}
	if sq := (&Authenticator{mode: SQLite}); sq.table("system_sessions") != "system_sessions" || sq.bind("a=?") != "a=?" {
		t.Fatal("SQLite 语义必须保持")
	}

	// parseTime / 哈希 / base64 / maxInt 分支。
	if value, err := parseTime([]byte(nodeISOTime(now))); err != nil || value.IsZero() {
		t.Fatalf("[]byte parseTime = %v err=%v", value, err)
	}
	if _, err := parseTime(42); err == nil {
		t.Fatal("非法时间类型必须报错")
	}
	hash, err := HashNodePassword("another-password")
	if err != nil || !verifyNodePBKDF2Password("another-password", hash) {
		t.Fatal("HashNodePassword 往返失败")
	}
	if verifyNodePBKDF2Password("wrong", hash) {
		t.Fatal("错误密码不得通过")
	}
	for _, broken := range []string{
		"pbkdf2$sha512", "md5$sha512$1$a$b", "pbkdf2$sha512$abc$a$b",
		"pbkdf2$sha512$NaN$a$b", "pbkdf2$sha512$0$a$b", "pbkdf2$sha512$1$a$",
		"pbkdf2$sha512$1$a$!!!",
	} {
		if verifyNodePBKDF2Password(w7bPassword, broken) {
			t.Fatalf("坏哈希 %q 不得通过", broken)
		}
	}
	if _, err := decodeNodeBase64URL(""); err == nil {
		t.Fatal("空 base64url 必须报错")
	}
	if _, err := decodeNodeBase64URL("!!!!"); err == nil {
		t.Fatal("坏 base64url 必须报错")
	}
	if decoded, err := decodeNodeBase64URL("YWJj"); err != nil || string(decoded) != "abc" {
		t.Fatalf("raw 解码 = %q err=%v", decoded, err)
	}
	if decoded, err := decodeNodeBase64URL("YWJj"); err != nil || len(decoded) != 3 {
		t.Fatal("解码分支失败")
	}
	if maxInt(0, 1) != 1 || maxInt(5, 1) != 5 {
		t.Fatal("maxInt 收敛错误")
	}
	if maxIntCeilSeconds(2500*time.Millisecond) != 3 {
		t.Fatal("向上取整秒错误")
	}
	if maxIntCeilSeconds(time.Second) != 1 || maxIntCeilSeconds(0) != 1 {
		t.Fatal("秒数下限错误")
	}
}

// w7b：HTTP 错误路径矩阵（httptest）。
func TestW7BHTTPHandlerErrorMatrix(t *testing.T) {
	db, auth, _ := w7bOpenAuth(t, "http")
	var nilHandler *HTTPHandler
	recorder := httptest.NewRecorder()
	nilHandler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/me", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil handler = %d", recorder.Code)
	}
	unwired := &HTTPHandler{}
	recorder = httptest.NewRecorder()
	unwired.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{}`)))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("未接线 = %d", recorder.Code)
	}
	handler := &HTTPHandler{Auth: auth, TTL: 1}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/unknown", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("未知路径 = %d", recorder.Code)
	}

	// login：参数校验矩阵 + 错误密码 + 中断上下文 500。
	for name, body := range map[string]string{
		"空用户名":    `{"username":" ","password":"x"}`,
		"空密码":     `{"username":"a","password":""}`,
		"用户名空格":   `{"username":"a b","password":"x"}`,
		"密码空格":    `{"username":"a","password":"x y"}`,
		"未知字段":    `{"username":"a","password":"b","extra":1}`,
		"坏JSON":   `not-json`,
		"双JSON对象": `{"username":"a","password":"b"}{"x":1}`,
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("login %s = %d", name, recorder.Code)
		}
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin","password":"wrong-pass"}`)))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("错误密码 = %d", recorder.Code)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	brokenRequest := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin","password":"`+w7bPassword+`"}`)).WithContext(cancelled)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, brokenRequest)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("中断上下文 = %d body=%s", recorder.Code, recorder.Body.String())
	}
	// 正常登录拿会话 cookie。
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin","password":"`+w7bPassword+`"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("登录 = %d", recorder.Code)
	}
	cookie := recorder.Result().Cookies()[0]

	// me：缺令牌 / 坏 Authorization。
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/me", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("me 缺令牌 = %d", recorder.Code)
	}
	badMe := httptest.NewRequest(http.MethodGet, "/me", nil)
	badMe.Header.Set("Authorization", "Bearer bogus")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, badMe)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("me 坏令牌 = %d", recorder.Code)
	}

	// profile：坏参数 + 目标账户被删除 → 404。
	profile := httptest.NewRequest(http.MethodPatch, "/me", strings.NewReader(`{"displayName":" "}`))
	profile.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, profile)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("profile 空名 = %d", recorder.Code)
	}
	profile = httptest.NewRequest(http.MethodPatch, "/me", strings.NewReader(`{"displayName":"has space"}`))
	profile.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, profile)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("profile 含空格 = %d", recorder.Code)
	}
	profile = httptest.NewRequest(http.MethodPatch, "/me", strings.NewReader(`not-json`))
	profile.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, profile)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("profile 坏JSON = %d", recorder.Code)
	}
	profile = httptest.NewRequest(http.MethodPatch, "/me", strings.NewReader(`{"displayName":"新名字"}`))
	profile.AddCookie(cookie)
	// 账户行被删除后 JOIN 失败 → 会话立即失效（fail-closed），不会进入改名分支。
	if _, err := db.Exec(`DELETE FROM system_accounts WHERE id='acct'`); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, profile)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("profile 账户缺失 = %d", recorder.Code)
	}
	if _, err := db.Exec(`INSERT INTO system_accounts VALUES ('acct','admin','Admin','admin','active',(SELECT password_hash FROM system_accounts WHERE id='viewer'),0,'','')`); err != nil {
		t.Fatal(err)
	}
	profile = httptest.NewRequest(http.MethodPatch, "/me", strings.NewReader(`{"displayName":"新名字"}`))
	profile.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, profile)
	if recorder.Code != http.StatusOK {
		t.Fatalf("profile 成功 = %d body=%s", recorder.Code, recorder.Body.String())
	}

	// change-password：短密码 / 含空格 / 错误旧密码。
	change := httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(`{"oldPassword":"x","newPassword":"abc"}`))
	change.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, change)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("短密码 = %d", recorder.Code)
	}
	change = httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(`{"oldPassword":"a b","newPassword":"abcd"}`))
	change.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, change)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("旧密码含空格 = %d", recorder.Code)
	}
	adminHashRevision := ""
	if err := db.QueryRow(`SELECT password_hash FROM system_accounts WHERE id='acct'`).Scan(&adminHashRevision); err != nil {
		t.Fatal(err)
	}
	sum := hashString(adminHashRevision)
	change = httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(`{"oldPassword":"not-old-password","newPassword":"abcd"}`))
	change.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, change)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "当前密码不正确") {
		t.Fatalf("错误旧密码 = %d body=%s revision=%s", recorder.Code, recorder.Body.String(), sum)
	}

	// logout：带/不带 cookie。
	logout := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logout.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, logout)
	if recorder.Code != http.StatusOK {
		t.Fatalf("logout = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/logout", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("无 cookie logout = %d", recorder.Code)
	}
}

// w7b：临时访问令牌端点的校验矩阵（独立 handler 避免共享限流状态）。
func TestW7BTemporaryAccessTokenMatrix(t *testing.T) {
	_, auth, now := w7bOpenAuth(t, "temp")
	handler := func() *HTTPHandler {
		return &HTTPHandler{Auth: auth, TemporaryAccessIPAllowlist: []string{"127.0.0.1", "::ffff:192.0.2.5"}, Guard: NewLoginGuard(func() time.Time { return now })}
	}
	post := func(body string, remote string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/temporary-access-tokens", strings.NewReader(body))
		request.RemoteAddr = remote
		recorder := httptest.NewRecorder()
		handler().ServeHTTP(recorder, request)
		return recorder
	}
	if code := post(`{"username":"admin","password":"x","ttlSeconds":10}`, "127.0.0.1:1").Code; code != http.StatusBadRequest {
		t.Fatalf("过小 ttl = %d", code)
	}
	if code := post(`{"username":"admin","password":"x","ttlSeconds":9999}`, "127.0.0.1:1").Code; code != http.StatusBadRequest {
		t.Fatalf("过大 ttl = %d", code)
	}
	if code := post(`{"username":"admin","password":"x"}`, "203.0.113.9:1").Code; code != http.StatusForbidden {
		t.Fatalf("白名单外 = %d", code)
	}
	if code := post(`{"username":"mustchange","password":"`+w7bPassword+`"}`, "127.0.0.1:1").Code; code != http.StatusForbidden {
		t.Fatalf("必改密码 = %d", code)
	}
	if code := post(`{"username":"viewer","password":"`+w7bPassword+`"}`, "127.0.0.1:1").Code; code != http.StatusUnauthorized {
		t.Fatalf("非管理员 = %d", code)
	}
	if code := post(`{"username":"admin","password":"wrong"}`, "127.0.0.1:1").Code; code != http.StatusUnauthorized {
		t.Fatalf("错误密码 = %d", code)
	}
	okRecorder := post(`{"username":"admin","password":"`+w7bPassword+`","ttlSeconds":120}`, "127.0.0.1:1")
	if okRecorder.Code != http.StatusOK {
		t.Fatalf("成功 = %d body=%s", okRecorder.Code, okRecorder.Body.String())
	}
	var envelope struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(okRecorder.Body.Bytes(), &envelope); err != nil || envelope.Data.Token == "" {
		t.Fatalf("解码 = %v", err)
	}
	// 撤销端点：非临时令牌 / 未知令牌。
	revoke := func(authHeader string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/temporary-access-tokens/revoke", nil)
		request.Header.Set("Authorization", authHeader)
		recorder := httptest.NewRecorder()
		handler().ServeHTTP(recorder, request)
		return recorder
	}
	if code := revoke("Bearer bogus-token").Code; code != http.StatusBadRequest {
		t.Fatalf("非临时令牌撤销 = %d", code)
	}
	issued := envelope.Data.Token
	if code := revoke("Bearer " + issued).Code; code != http.StatusOK {
		t.Fatalf("撤销 = %d", code)
	}
	if code := revoke("Bearer " + issued).Code; code != http.StatusUnauthorized {
		t.Fatalf("重复撤销 = %d", code)
	}
	// IPv4-mapped 白名单命中（表头归一化）。
	if code := post(`{"username":"admin","password":"`+w7bPassword+`"}`, "[::ffff:192.0.2.5]:9").Code; code != http.StatusOK {
		t.Fatalf("mapped 白名单 = %d", code)
	}
}

// w7b：captcha 分支与 nil 服务安全。
func TestW7BCaptchaBranches(t *testing.T) {
	now := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	service := NewCaptchaService(func() time.Time { return now })
	var nilService *CaptchaService
	if result, err := nilService.Issue("1.2.3.4"); err != nil || result.Blocked {
		t.Fatal("nil Issue 必须安全")
	}
	if nilService.Verify("x", "y") || nilService.AnswerForTest("x") != "" {
		t.Fatal("nil Verify/Answer 必须安全")
	}
	result, err := service.Issue("10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if service.AnswerForTest("unknown-id") != "" {
		t.Fatal("未知 id 的答案必须为空")
	}
	if service.Verify("unknown-id", "AAAAA") {
		t.Fatal("未知 id 校验必须失败")
	}
	answer := service.AnswerForTest(result.Challenge.CaptchaID)
	// 归一化：小写 + 空格分隔仍应通过（一次性消费）。
	if !service.Verify(result.Challenge.CaptchaID, strings.ToLower(answer[0:1]+" "+answer[1:])) {
		t.Fatal("归一化答案必须通过")
	}
	first, err := service.Issue("10.0.0.2")
	second, err2 := service.Issue("10.0.0.2")
	if err != nil || err2 != nil || first.Challenge.CaptchaID == second.Challenge.CaptchaID {
		t.Fatal("验证码 ID 必须唯一")
	}
	// 过期挑战清理由下一次 Issue 驱动：快进 TTL 后旧记录被删除。
	now = now.Add(2 * captchaTTL)
	if _, err := service.Issue("10.0.0.3"); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	stale := len(service.chall)
	service.mu.Unlock()
	if stale > 3 {
		t.Fatalf("过期挑战必须被清理: %d", stale)
	}
	if code := normalizeCaptchaCode(" a b c "); code != "ABC" {
		t.Fatalf("归一化 = %q", code)
	}
	if renderCaptchaImage("24A") == "" || !strings.HasPrefix(renderCaptchaImage("24A"), "data:image/png;base64,") {
		t.Fatal("渲染必须产出 data URL")
	}
}
