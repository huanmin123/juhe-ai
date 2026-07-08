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
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/modules/managementauth"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW3ManagementLoginPostgresRedisSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	postgresContainer, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("juhe_ai"),
		tcpostgres.WithUsername("juhe_ai"),
		tcpostgres.WithPassword("juhe_ai_password"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	defer terminateContainer(t, ctx, postgresContainer)

	postgresURL, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	db := openSQLDB(t, postgresURL)
	defer closeSQLDB(t, db)
	runGooseMigrations(t, db)

	redisContainer, err := tcredis.Run(ctx, redisImage)
	if err != nil {
		t.Fatalf("start redis container: %v", err)
	}
	defer terminateContainer(t, ctx, redisContainer)

	redisURL, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis connection string: %v", err)
	}
	stateRedis, err := redisplatform.NewClient(redisURL, "w3-management-login-smoke")
	if err != nil {
		t.Fatalf("open redis state client: %v", err)
	}
	defer closeRedisClient(t, stateRedis)

	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	insertW3LoginSystemAccountFixture(t, ctx, db, now)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	cfg := config.Config{
		Host:                 "127.0.0.1",
		Port:                 3000,
		ManagementAPIEnabled: true,
		TrustProxy:           "true",
		CookieSameSite:       "lax",
	}
	captchaService := managementauth.NewCaptchaServiceWithOptions(managementauth.CaptchaServiceOptions{
		Store:          stateRedis,
		Now:            func() time.Time { return now },
		NewID:          func() string { return "captcha_w3_login" },
		GenerateAnswer: func() (string, error) { return "AB234", nil },
		RenderImage:    func(string) (string, error) { return "data:image/png;base64,AA==", nil },
	})
	loginService := managementauth.NewLoginServiceWithOptions(managementauth.LoginServiceOptions{
		Store:           store,
		Captcha:         captchaService,
		Guard:           managementauth.NewLoginGuardService(stateRedis),
		Now:             func() time.Time { return now },
		NewSessionToken: func() (string, error) { return "w3-login-session-token", nil },
		NewSessionID:    func(time.Time) (string, error) { return "sess_w3_login", nil },
	})
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now.Add(time.Second) },
	})
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                       cfg,
		Logger:                       slog.Default(),
		ManagementAPIAuthMiddleware:  httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementCaptchaHandler:     httpapi.NewManagementCaptchaHandler(captchaService, cfg),
		ManagementLoginHandler:       httpapi.NewManagementLoginHandler(loginService, cfg),
		ManagementCurrentUserHandler: httpapi.NewManagementCurrentUserHandler(authenticator),
	})

	captchaID := issueW3LoginCaptcha(t, router)
	loginRec := serveW3LoginRequest(router, `{"username":"w3-login","password":"GoLogin123","captchaId":"`+captchaID+`","captchaCode":"A B 2 3 4"}`)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", loginRec.Code, loginRec.Body.String())
	}
	sessionCookie := w3LoginSessionCookie(t, loginRec)
	if sessionCookie.Value != "w3-login-session-token" || !sessionCookie.HttpOnly || sessionCookie.Path != "/" || sessionCookie.MaxAge <= 0 {
		t.Fatalf("login session cookie = %+v", sessionCookie)
	}
	var loginBody struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(loginRec.Body).Decode(&loginBody); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if loginBody.Data["id"] != "sys_w3_login" || loginBody.Data["username"] != "w3-login" {
		t.Fatalf("login response data = %+v", loginBody.Data)
	}
	if _, exists := loginBody.Data["passwordHash"]; exists {
		t.Fatalf("login response leaked passwordHash: %+v", loginBody.Data)
	}

	assertW3LoginSessionPersisted(t, ctx, db, sessionCookie.Value, now)

	meReq := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/me", nil)
	meReq.Header.Set("Cookie", managementauth.SessionCookieName+"="+sessionCookie.Value)
	meRec := httptest.NewRecorder()
	router.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("current user status = %d, body = %s", meRec.Code, meRec.Body.String())
	}

	for attempt := 1; attempt <= managementauth.LoginGuardUsernameThreshold; attempt++ {
		captchaID = issueW3LoginCaptcha(t, router)
		rec := serveW3LoginRequest(router, `{"username":"w3-login","password":"WrongPass123","captchaId":"`+captchaID+`","captchaCode":"AB234"}`)
		if attempt < managementauth.LoginGuardUsernameThreshold {
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("wrong password attempt %d status = %d, want 401, body = %s", attempt, rec.Code, rec.Body.String())
			}
			continue
		}
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("lock attempt status = %d, retry-after = %q, body = %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
		}
	}
	assertW3LoginSessionCount(t, ctx, db, 1)
}

func insertW3LoginSystemAccountFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()

	passwordHash, err := managementauth.HashPassword("GoLogin123")
	if err != nil {
		t.Fatalf("hash W3 login password: %v", err)
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES (
			'sys_w3_login', 'w3-login', 'W3 Login', NULL, 'admin', 'active', $1,
			false, false, $2, $3
		)
	`, passwordHash, now, now)
	if err != nil {
		t.Fatalf("insert W3 login system account: %v", err)
	}
}

func issueW3LoginCaptcha(t *testing.T, router http.Handler) string {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/auth/captcha", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "198.51.100.10")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("captcha status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data struct {
			CaptchaID string `json:"captchaId"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode captcha response: %v", err)
	}
	if body.Data.CaptchaID == "" {
		t.Fatal("captcha id is empty")
	}
	return body.Data.CaptchaID
}

func serveW3LoginRequest(router http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/auth/login", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "198.51.100.10")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func w3LoginSessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()

	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == managementauth.SessionCookieName {
			return cookie
		}
	}
	t.Fatalf("session cookie not found in Set-Cookie = %q", rec.Header().Values("Set-Cookie"))
	return nil
}

func assertW3LoginSessionPersisted(t *testing.T, ctx context.Context, db *sql.DB, token string, now time.Time) {
	t.Helper()

	var tokenHash string
	var expiresAt time.Time
	var createdAt time.Time
	var lastSeenAt time.Time
	var lastLoginAt time.Time
	if err := db.QueryRowContext(ctx, `
		SELECT
			ss.token_hash,
			ss.expires_at,
			ss.created_at,
			ss.last_seen_at,
			sa.last_login_at
		FROM juhe_business.system_sessions AS ss
		INNER JOIN juhe_business.system_accounts AS sa
			ON sa.id = ss.system_account_id
		WHERE ss.id = 'sess_w3_login'
	`).Scan(&tokenHash, &expiresAt, &createdAt, &lastSeenAt, &lastLoginAt); err != nil {
		t.Fatalf("read W3 login session: %v", err)
	}
	if tokenHash != managementauth.HashSessionToken(token) || tokenHash == token {
		t.Fatalf("session token hash = %q for token %q", tokenHash, token)
	}
	if !expiresAt.UTC().Equal(now.Add(managementauth.ManagementSessionTTL)) {
		t.Fatalf("session expires_at = %s, want %s", expiresAt.UTC().Format(time.RFC3339Nano), now.Add(managementauth.ManagementSessionTTL).Format(time.RFC3339Nano))
	}
	for name, got := range map[string]time.Time{
		"created_at":    createdAt,
		"last_seen_at":  lastSeenAt,
		"last_login_at": lastLoginAt,
	} {
		if !got.UTC().Equal(now) {
			t.Fatalf("%s = %s, want %s", name, got.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		}
	}
}

func assertW3LoginSessionCount(t *testing.T, ctx context.Context, db *sql.DB, want int) {
	t.Helper()

	var got int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_business.system_sessions
		WHERE system_account_id = 'sys_w3_login'
	`).Scan(&got); err != nil {
		t.Fatalf("count W3 login sessions: %v", err)
	}
	if got != want {
		t.Fatalf("W3 login session count = %d, want %d", got, want)
	}
}
