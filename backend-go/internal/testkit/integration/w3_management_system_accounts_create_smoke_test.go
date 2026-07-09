//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"juhe-ai/backend-go/internal/app"
	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementsystemaccounts"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW3ManagementSystemAccountCreatePostgresRedisSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
	redisQueueURL := w3RedisURLWithDB(t, redisURL, 0)
	redisOpts, err := queue.ParseRedisURL(redisQueueURL)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}

	now := time.Date(2026, 7, 9, 14, 0, 0, 0, time.UTC)
	insertW3SystemAccountCreateAdminFixture(t, ctx, db, now)
	adminSessionToken := "w3-system-account-create-admin-session"
	insertW2ManagementSessionForAccountFixture(t, ctx, db, "sess_w3_create_admin", "sys_w3_create_admin", adminSessionToken, now)

	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	var workerErrMu sync.Mutex
	var workerRunErr error
	go func() {
		err := app.RunIngestWorker(workerCtx, config.Config{
			PostgresURL:     postgresURL,
			RedisQueueURL:   redisQueueURL,
			RedisNamespace:  "juhe-ai",
			LogLevel:        "error",
			ShutdownTimeout: time.Second,
		}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		workerErrMu.Lock()
		workerRunErr = err
		workerErrMu.Unlock()
		close(workerDone)
	}()
	defer func() {
		stopWorker()
		select {
		case <-workerDone:
		case <-time.After(5 * time.Second):
			t.Fatal("ingest worker shutdown timed out")
		}
		workerErrMu.Lock()
		err := workerRunErr
		workerErrMu.Unlock()
		if err != nil {
			t.Fatalf("ingest worker run: %v", err)
		}
	}()

	logClient := queue.NewClient(redisOpts)
	defer closeClient(t, logClient)
	inspector := queue.NewInspector(redisOpts)
	defer closeInspector(t, inspector)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	cfg := config.Config{
		Host:                 "127.0.0.1",
		Port:                 3000,
		ManagementAPIEnabled: true,
		TrustProxy:           "false",
	}
	service := managementsystemaccounts.NewServiceWithOptions(managementsystemaccounts.ServiceOptions{
		Store:  store,
		Now:    func() time.Time { return now },
		Secret: "w3-create-secret",
	})
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                           cfg,
		Logger:                           slog.Default(),
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementSystemAccountCreateHandler: httpapi.NewManagementSystemAccountCreateHandlerWithOperationLog(
			service,
			httpapi.ManagementOperationLogOptions{
				Config: cfg,
				Logger: slog.Default(),
				Client: logClient,
				Now:    func() time.Time { return now },
				NewLogID: func() string {
					return "oplog_w3_system_account_create_1"
				},
			},
		),
	})

	createBody := `{"username":"w3-create-user","displayName":"W3CreateUser","description":"创建说明","password":"CreatePass123","role":"user","status":"active","mustChangePassword":true,"imageGenerationEnabled":true}`
	req := httptest.NewRequest(http.MethodPost, "/__aisys__/api/system-accounts", strings.NewReader(createBody))
	req.Header.Set("Cookie", managementauth.SessionCookieName+"="+adminSessionToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "w3-system-account-create-smoke")
	req.Header.Set("X-Request-Id", "req_w3_create_system_account")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	bodyText := rec.Body.String()
	for _, forbidden := range []string{"CreatePass123", "passwordHash", "DefaultGroupIDs", "DefaultAPIKeyIDs", "key_secret_encrypted", "sk-"} {
		if strings.Contains(bodyText, forbidden) {
			t.Fatalf("create response leaked %q: %s", forbidden, bodyText)
		}
	}
	var createResponse struct {
		Data managementsystemaccounts.Summary `json:"data"`
	}
	if err := json.NewDecoder(strings.NewReader(bodyText)).Decode(&createResponse); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	created := createResponse.Data
	if created.ID == "" ||
		created.Username != "w3-create-user" ||
		created.DisplayName != "W3CreateUser" ||
		created.Description != "创建说明" ||
		created.Role != "user" ||
		created.Status != "active" ||
		!created.MustChangePassword ||
		!created.ImageGenerationEnabled ||
		created.LastLoginAt != "" ||
		created.CreatedAt != now.Format(time.RFC3339Nano) ||
		created.UpdatedAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("create response data = %+v", created)
	}

	assertW3SystemAccountCreateAccount(t, ctx, db, created.ID)
	assertW3SystemAccountCreateDefaultGroups(t, ctx, db, created.ID)
	assertW3SystemAccountCreateDefaultRoutes(t, ctx, db, created.ID)
	assertW3SystemAccountCreateDefaultAPIKeys(t, ctx, db, created.ID)

	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, func() error {
		workerErrMu.Lock()
		defer workerErrMu.Unlock()
		return workerRunErr
	}); err != nil {
		t.Fatal(err)
	}
	assertW3SystemAccountCreateOperationLog(t, ctx, db, created.ID)
}

func insertW3SystemAccountCreateAdminFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES (
			'sys_w3_create_admin', 'w3-create-admin', 'W3 Create Admin', NULL, 'super_admin', 'active', 'hash',
			false, false, $1, $2
		)
	`, now, now)
	if err != nil {
		t.Fatalf("insert W3 system account create admin fixture: %v", err)
	}
}

func assertW3SystemAccountCreateAccount(t *testing.T, ctx context.Context, db *sql.DB, accountID string) {
	t.Helper()
	var username string
	var displayName string
	var description sql.NullString
	var role string
	var status string
	var passwordHash string
	var mustChangePassword bool
	var imageGenerationEnabled bool
	if err := db.QueryRowContext(ctx, `
		SELECT username, display_name, description, role, status, password_hash, must_change_password, image_generation_enabled
		FROM juhe_business.system_accounts
		WHERE id = $1
	`, accountID).Scan(&username, &displayName, &description, &role, &status, &passwordHash, &mustChangePassword, &imageGenerationEnabled); err != nil {
		t.Fatalf("read W3 create account: %v", err)
	}
	if username != "w3-create-user" ||
		displayName != "W3CreateUser" ||
		!description.Valid ||
		description.String != "创建说明" ||
		role != "user" ||
		status != "active" ||
		!mustChangePassword ||
		!imageGenerationEnabled {
		t.Fatalf("created account = username:%s display:%s desc:%+v role:%s status:%s mustChange:%v image:%v",
			username, displayName, description, role, status, mustChangePassword, imageGenerationEnabled)
	}
	if passwordHash == "" || passwordHash == "CreatePass123" || !managementauth.VerifyPassword("CreatePass123", passwordHash) {
		t.Fatalf("unexpected created password hash %q", passwordHash)
	}
}

func assertW3SystemAccountCreateDefaultGroups(t *testing.T, ctx context.Context, db *sql.DB, accountID string) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT provider_code, enabled, is_default
		FROM juhe_business.groups
		WHERE system_account_id = $1
		ORDER BY provider_code
	`, accountID)
	if err != nil {
		t.Fatalf("query W3 create default groups: %v", err)
	}
	defer rows.Close()

	gotProviders := map[string]bool{}
	for rows.Next() {
		var providerCode string
		var enabled bool
		var isDefault bool
		if err := rows.Scan(&providerCode, &enabled, &isDefault); err != nil {
			t.Fatalf("scan W3 create default group: %v", err)
		}
		if !enabled || !isDefault {
			t.Fatalf("default group provider=%s enabled=%v isDefault=%v", providerCode, enabled, isDefault)
		}
		gotProviders[providerCode] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate W3 create default groups: %v", err)
	}
	wantProviders := []string{"anthropic", "deepseek", "gemini", "glm", "gpt", "hybrid", "openai"}
	if len(gotProviders) != len(wantProviders) {
		t.Fatalf("default group providers = %+v", gotProviders)
	}
	for _, provider := range wantProviders {
		if !gotProviders[provider] {
			t.Fatalf("missing default group provider %s in %+v", provider, gotProviders)
		}
	}
}

func assertW3SystemAccountCreateDefaultRoutes(t *testing.T, ctx context.Context, db *sql.DB, accountID string) {
	t.Helper()
	var routeCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_business.route_strategies
		WHERE system_account_id = $1
		  AND mode = 'normal'
		  AND status = 'active'
		  AND is_default = true
		  AND config_json IS NULL
	`, accountID).Scan(&routeCount); err != nil {
		t.Fatalf("count W3 create default routes: %v", err)
	}
	if routeCount != 6 {
		t.Fatalf("default route count = %d, want 6", routeCount)
	}

	var routeGroupCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_business.route_strategy_groups rsg
		JOIN juhe_business.route_strategies rs
		  ON rs.id = rsg.route_strategy_id
		 AND rs.system_account_id = rsg.system_account_id
		JOIN juhe_business.groups g
		  ON g.id = rsg.group_id
		 AND g.system_account_id = rsg.system_account_id
		WHERE rsg.system_account_id = $1
		  AND rsg.priority = 1
		  AND rsg.weight = 1
		  AND rsg.status = 'active'
		  AND rs.mode = 'normal'
		  AND rs.is_default = true
		  AND g.is_default = true
		  AND g.provider_code <> 'hybrid'
	`, accountID).Scan(&routeGroupCount); err != nil {
		t.Fatalf("count W3 create default route groups: %v", err)
	}
	if routeGroupCount != 6 {
		t.Fatalf("default route group count = %d, want 6", routeGroupCount)
	}
}

func assertW3SystemAccountCreateDefaultAPIKeys(t *testing.T, ctx context.Context, db *sql.DB, accountID string) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT key_hash, key_prefix, key_suffix, key_secret_encrypted, status, is_default
		FROM juhe_business.api_keys
		WHERE system_account_id = $1
		ORDER BY id
	`, accountID)
	if err != nil {
		t.Fatalf("query W3 create default api keys: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var keyHash string
		var keyPrefix string
		var keySuffix string
		var keySecretEncrypted sql.NullString
		var status string
		var isDefault bool
		if err := rows.Scan(&keyHash, &keyPrefix, &keySuffix, &keySecretEncrypted, &status, &isDefault); err != nil {
			t.Fatalf("scan W3 create default api key: %v", err)
		}
		count++
		if len(keyHash) != 64 ||
			len(keyPrefix) != 8 ||
			len(keySuffix) != 8 ||
			!strings.HasPrefix(keyPrefix, "sk-") ||
			!keySecretEncrypted.Valid ||
			!strings.HasPrefix(keySecretEncrypted.String, "v1:") ||
			strings.Contains(keySecretEncrypted.String, "sk-") ||
			status != "active" ||
			!isDefault {
			t.Fatalf("default api key row = hash:%q prefix:%q suffix:%q encrypted:%q status:%s default:%v",
				keyHash, keyPrefix, keySuffix, keySecretEncrypted.String, status, isDefault)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate W3 create default api keys: %v", err)
	}
	if count != 6 {
		t.Fatalf("default api key count = %d, want 6", count)
	}
}

func assertW3SystemAccountCreateOperationLog(t *testing.T, ctx context.Context, db *sql.DB, accountID string) {
	t.Helper()
	logRow := readW3SystemAccountPatchOperationLog(t, ctx, db, "oplog_w3_system_account_create_1")
	if logRow.OperationKey != "system_accounts.create" ||
		logRow.Action != "create" ||
		logRow.ResourceID != accountID ||
		logRow.ResourceName != "W3CreateUser" ||
		logRow.TraceID != "req_w3_create_system_account" {
		t.Fatalf("create operation log = %+v", logRow)
	}
	for _, forbidden := range []string{"CreatePass123", "sk-", "key_secret", "DefaultAPIKey"} {
		if strings.Contains(logRow.ChangesJSON, forbidden) || strings.Contains(logRow.MetadataJSON, forbidden) {
			t.Fatalf("create operation log leaked %q: changes=%s metadata=%s", forbidden, logRow.ChangesJSON, logRow.MetadataJSON)
		}
	}
	changes := decodeW3SystemAccountPatchChanges(t, logRow.ChangesJSON)
	if !w3SystemAccountCreateHasAfter(changes, "username", "w3-create-user") ||
		!w3SystemAccountCreateHasAfter(changes, "displayName", "W3CreateUser") ||
		!w3SystemAccountCreateHasAfter(changes, "role", "user") ||
		!w3SystemAccountCreateHasAfter(changes, "status", "active") ||
		!w3SystemAccountCreateHasAfter(changes, "imageGenerationEnabled", true) {
		t.Fatalf("create operation log changes = %+v", changes)
	}
	foundPassword := false
	for _, change := range changes {
		if change.Field == "password" {
			foundPassword = true
			if !change.Sensitive || change.After != "已设置" {
				t.Fatalf("create password change = %+v", change)
			}
		}
	}
	if !foundPassword {
		t.Fatalf("create operation log missing password change: %+v", changes)
	}
}

func w3SystemAccountCreateHasAfter(changes []port.OperationLogChange, field string, after any) bool {
	for _, change := range changes {
		if change.Field == field && change.After == after {
			return true
		}
	}
	return false
}
