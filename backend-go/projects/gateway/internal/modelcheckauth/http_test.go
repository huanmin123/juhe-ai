package modelcheckauth

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/sha512"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestHTTPHandlerLoginMeLogoutLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE system_accounts (id TEXT PRIMARY KEY,username TEXT NOT NULL,display_name TEXT,role TEXT NOT NULL,status TEXT NOT NULL,password_hash TEXT NOT NULL,must_change_password INTEGER NOT NULL,last_login_at TEXT,updated_at TEXT NOT NULL)`,
		`CREATE TABLE system_sessions (id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,token_hash TEXT UNIQUE,expires_at TEXT,created_at TEXT,last_seen_at TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	salt := "MDEyMzQ1Njc4OWFiY2RlZg"
	derived, err := pbkdf2.Key(sha512.New, "correcthorsebatterystaple", []byte(salt), 120000, 32)
	if err != nil {
		t.Fatal(err)
	}
	passwordHash := "pbkdf2$sha512$120000$" + salt + "$" + base64.RawURLEncoding.EncodeToString(derived)
	if _, err := db.Exec(`INSERT INTO system_accounts VALUES ('acct','Admin','管理员','admin','active',?,1,'','')`, passwordHash); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	auth, err := New(db, SQLite, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	handler := &HTTPHandler{Auth: auth}
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin","password":"correcthorsebatterystaple"}`))
	loginRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loginRecorder, login)
	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginRecorder.Code, loginRecorder.Body.String())
	}
	cookie := loginRecorder.Result().Cookies()[0]
	if cookie.Name != SessionCookieName || cookie.Value == "" {
		t.Fatalf("login cookie=%+v", cookie)
	}
	me := httptest.NewRequest(http.MethodGet, "/me", nil)
	me.AddCookie(cookie)
	meRecorder := httptest.NewRecorder()
	handler.ServeHTTP(meRecorder, me)
	if meRecorder.Code != http.StatusOK || !strings.Contains(meRecorder.Body.String(), `"mustChangePassword":true`) {
		t.Fatalf("me status=%d body=%s", meRecorder.Code, meRecorder.Body.String())
	}
	profile := httptest.NewRequest(http.MethodPatch, "/me", strings.NewReader(`{"displayName":"新管理员"}`))
	profile.AddCookie(cookie)
	profileRecorder := httptest.NewRecorder()
	handler.ServeHTTP(profileRecorder, profile)
	if profileRecorder.Code != http.StatusOK || !strings.Contains(profileRecorder.Body.String(), "新管理员") {
		t.Fatalf("profile status=%d body=%s", profileRecorder.Code, profileRecorder.Body.String())
	}
	change := httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(`{"newPassword":"newsecurepassword"}`))
	change.AddCookie(cookie)
	changeRecorder := httptest.NewRecorder()
	handler.ServeHTTP(changeRecorder, change)
	if changeRecorder.Code != http.StatusOK || strings.Contains(changeRecorder.Body.String(), `"mustChangePassword":true`) {
		t.Fatalf("change password status=%d body=%s", changeRecorder.Code, changeRecorder.Body.String())
	}
	logout := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logout.AddCookie(cookie)
	logoutRecorder := httptest.NewRecorder()
	handler.ServeHTTP(logoutRecorder, logout)
	if logoutRecorder.Code != http.StatusOK {
		t.Fatalf("logout status=%d body=%s", logoutRecorder.Code, logoutRecorder.Body.String())
	}
	var tokenHash string
	digest := sha256.Sum256([]byte(cookie.Value))
	tokenHash = hex.EncodeToString(digest[:])
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM system_sessions WHERE token_hash=?`, tokenHash).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked session count=%d err=%v", count, err)
	}
	temporary := httptest.NewRequest(http.MethodPost, "/temporary-access-tokens", strings.NewReader(`{"username":"admin","password":"newsecurepassword","ttlSeconds":120}`))
	temporary.RemoteAddr = "127.0.0.1:8080"
	temporaryRecorder := httptest.NewRecorder()
	temporaryHandler := &HTTPHandler{Auth: auth, TemporaryAccessIPAllowlist: []string{"127.0.0.1"}, Guard: NewLoginGuard(func() time.Time { return now })}
	temporaryHandler.ServeHTTP(temporaryRecorder, temporary)
	if temporaryRecorder.Code != http.StatusOK || !strings.Contains(temporaryRecorder.Body.String(), "juhe_tmp_") {
		t.Fatalf("temporary token status=%d body=%s", temporaryRecorder.Code, temporaryRecorder.Body.String())
	}
	var temporaryEnvelope struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(temporaryRecorder.Body.Bytes(), &temporaryEnvelope); err != nil || temporaryEnvelope.Data.Token == "" {
		t.Fatalf("decode temporary token response: err=%v body=%s", err, temporaryRecorder.Body.String())
	}
	revoke := httptest.NewRequest(http.MethodPost, "/temporary-access-tokens/revoke", nil)
	revoke.Header.Set("Authorization", "Bearer "+temporaryEnvelope.Data.Token)
	revokeRecorder := httptest.NewRecorder()
	temporaryHandler.ServeHTTP(revokeRecorder, revoke)
	if revokeRecorder.Code != http.StatusOK {
		t.Fatalf("temporary token revoke status=%d body=%s", revokeRecorder.Code, revokeRecorder.Body.String())
	}
	revokeAgain := httptest.NewRequest(http.MethodPost, "/temporary-access-tokens/revoke", nil)
	revokeAgain.Header.Set("Authorization", "Bearer "+temporaryEnvelope.Data.Token)
	revokeAgainRecorder := httptest.NewRecorder()
	temporaryHandler.ServeHTTP(revokeAgainRecorder, revokeAgain)
	if revokeAgainRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("revoked temporary token status=%d body=%s", revokeAgainRecorder.Code, revokeAgainRecorder.Body.String())
	}
	for attempt := 0; attempt < loginGuardLimit; attempt++ {
		failed := httptest.NewRequest(http.MethodPost, "/temporary-access-tokens", strings.NewReader(`{"username":"admin","password":"wrong-password","ttlSeconds":120}`))
		failed.RemoteAddr = "127.0.0.1:8080"
		failedRecorder := httptest.NewRecorder()
		temporaryHandler.ServeHTTP(failedRecorder, failed)
		if attempt == loginGuardLimit-1 && failedRecorder.Code != http.StatusTooManyRequests {
			t.Fatalf("temporary token login guard status=%d body=%s", failedRecorder.Code, failedRecorder.Body.String())
		}
	}
	denied := httptest.NewRequest(http.MethodPost, "/temporary-access-tokens", strings.NewReader(`{"username":"admin","password":"newsecurepassword"}`))
	denied.RemoteAddr = "192.0.2.10:8080"
	deniedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(deniedRecorder, denied)
	if deniedRecorder.Code != http.StatusForbidden {
		t.Fatalf("non-allowlisted temporary token status=%d body=%s", deniedRecorder.Code, deniedRecorder.Body.String())
	}
}

func TestHTTPHandlerRejectsUnknownLoginFields(t *testing.T) {
	// A nil database is sufficient here because strict decoding must fail
	// before authentication is attempted.
	handler := &HTTPHandler{Auth: &Authenticator{db: nil}}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"a","password":"b","extra":true}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestHTTPHandlerRejectsInvalidTemporaryTokenFieldsBeforeDefaultTTL(t *testing.T) {
	handler := &HTTPHandler{Auth: &Authenticator{db: nil}, TemporaryAccessIPAllowlist: []string{"127.0.0.1"}}
	request := httptest.NewRequest(http.MethodPost, "/temporary-access-tokens", strings.NewReader(`{"username":"","password":"secret"}`))
	request.RemoteAddr = "127.0.0.1:8080"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCaptchaLifecycleAndLoginRequirement(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	service := NewCaptchaService(func() time.Time { return now })
	result, err := service.Issue("198.51.100.10")
	if err != nil || result.Blocked || result.Challenge.CaptchaID == "" || !strings.HasPrefix(result.Challenge.Image, "data:image/png;base64,") {
		t.Fatalf("issue result=%+v err=%v", result, err)
	}
	answer := service.AnswerForTest(result.Challenge.CaptchaID)
	if answer == "" || !service.Verify(result.Challenge.CaptchaID, answer) || service.Verify(result.Challenge.CaptchaID, answer) {
		t.Fatalf("captcha answer=%q did not enforce one-time verification", answer)
	}

	for i := 0; i < captchaIssueLimit; i++ {
		if issued, issueErr := service.Issue("198.51.100.11"); issueErr != nil || issued.Blocked {
			t.Fatalf("issue %d unexpectedly blocked: %+v err=%v", i, issued, issueErr)
		}
	}
	blocked, err := service.Issue("198.51.100.11")
	if err != nil || !blocked.Blocked || blocked.RetryAfter < 1 {
		t.Fatalf("expected issue limit, result=%+v err=%v", blocked, err)
	}
	now = now.Add(captchaTTL + time.Second)
	expired, err := service.Issue("198.51.100.12")
	if err != nil || expired.Blocked {
		t.Fatalf("expired challenge should not affect issue limit: %+v err=%v", expired, err)
	}
	if service.Verify(expired.Challenge.CaptchaID, service.AnswerForTest(expired.Challenge.CaptchaID)) == false {
		t.Fatal("fresh challenge should verify before expiry")
	}
	now = now.Add(captchaTTL + time.Second)
	if service.Verify(expired.Challenge.CaptchaID, "AAAAA") {
		t.Fatal("expired challenge must fail")
	}
}

func TestHTTPHandlerCaptchaDisabledAndEnabledEndpoints(t *testing.T) {
	without := &HTTPHandler{Auth: &Authenticator{db: nil}}
	recorder := httptest.NewRecorder()
	without.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/captcha", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"required":false`) {
		t.Fatalf("disabled captcha status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	service := NewCaptchaService(nil)
	with := &HTTPHandler{Auth: &Authenticator{db: nil}, Captcha: service}
	recorder = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/captcha", nil)
	request.RemoteAddr = "127.0.0.1:8080"
	with.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"required":true`) || !strings.Contains(recorder.Body.String(), `"image":"data:image/png;base64,`) {
		t.Fatalf("enabled captcha status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	loginRecorder := httptest.NewRecorder()
	with.ServeHTTP(loginRecorder, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin","password":"secret"}`)))
	if loginRecorder.Code != http.StatusBadRequest || !strings.Contains(loginRecorder.Body.String(), "验证码") {
		t.Fatalf("captcha should guard login before credential lookup: status=%d body=%s", loginRecorder.Code, loginRecorder.Body.String())
	}
}
