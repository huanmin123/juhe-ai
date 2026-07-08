//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/gatewaycache"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementsystemaccounts"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW3ManagementSystemAccountsPatchPostgresRedisSmoke(t *testing.T) {
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
	redisStateURL := w3RedisURLWithDB(t, redisURL, 1)
	redisCacheURL := w3RedisURLWithDB(t, redisURL, 2)
	redisOpts, err := queue.ParseRedisURL(redisQueueURL)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	stateRedis, err := redisplatform.NewClient(redisStateURL, "w3-system-account-patch:state")
	if err != nil {
		t.Fatalf("open state redis: %v", err)
	}
	defer func() { _ = stateRedis.Close() }()
	cacheRedis, err := redisplatform.NewClient(redisCacheURL, "w3-system-account-patch:cache")
	if err != nil {
		t.Fatalf("open cache redis: %v", err)
	}
	defer func() { _ = cacheRedis.Close() }()
	invalidator, err := gatewaycache.NewSystemAccountInvalidator(gatewaycache.SystemAccountInvalidatorOptions{
		Cache:     cacheRedis,
		State:     stateRedis,
		Namespace: "w3-system-account-patch",
	})
	if err != nil {
		t.Fatalf("create gateway cache invalidator: %v", err)
	}

	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	insertW3SystemAccountPatchFixtures(t, ctx, db, now)
	adminSessionToken := "w3-system-account-patch-admin-session"
	insertW2ManagementSessionForAccountFixture(t, ctx, db, "sess_w3_patch_admin", "sys_w3_patch_admin", adminSessionToken, now)
	insertW3SystemAccountPatchTargetSessions(t, ctx, db, now, "reset")

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
	logID := 0
	service := managementsystemaccounts.NewServiceWithOptions(managementsystemaccounts.ServiceOptions{
		Store:                    store,
		Now:                      func() time.Time { return now },
		SystemAccountInvalidator: invalidator,
	})
	router := httpapiNewW3SystemAccountPatchRouter(t, cfg, authenticator, service, logClient, func() time.Time {
		return now
	}, func() string {
		logID++
		return fmt.Sprintf("oplog_w3_system_account_patch_%d", logID)
	})

	resetRec := serveW3SystemAccountPatchRequest(router, adminSessionToken, "sys_w3_patch_target", `{"password":"NewPass123","mustChangePassword":false}`, "req_w3_patch_reset")
	if resetRec.Code != http.StatusOK {
		t.Fatalf("reset status = %d, body = %s", resetRec.Code, resetRec.Body.String())
	}
	if strings.Contains(resetRec.Body.String(), "passwordHash") || strings.Contains(resetRec.Body.String(), "NewPass123") {
		t.Fatalf("reset response leaked password data: %s", resetRec.Body.String())
	}
	var resetBody struct {
		Data managementsystemaccounts.Summary `json:"data"`
	}
	if err := json.NewDecoder(resetRec.Body).Decode(&resetBody); err != nil {
		t.Fatalf("decode reset response: %v", err)
	}
	if resetBody.Data.ID != "sys_w3_patch_target" || resetBody.Data.MustChangePassword {
		t.Fatalf("reset response = %+v", resetBody.Data)
	}
	assertW3SystemAccountPatchPassword(t, ctx, db, "NewPass123")
	assertW3SystemAccountPatchSessionCount(t, ctx, db, "sys_w3_patch_target", 0)

	insertW3SystemAccountPatchTargetSessions(t, ctx, db, now.Add(time.Second), "status")
	statusRec := serveW3SystemAccountPatchRequest(router, adminSessionToken, "sys_w3_patch_target", `{"status":"disabled"}`, "req_w3_patch_status")
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status update status = %d, body = %s", statusRec.Code, statusRec.Body.String())
	}
	var statusBody struct {
		Data managementsystemaccounts.Summary `json:"data"`
	}
	if err := json.NewDecoder(statusRec.Body).Decode(&statusBody); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if statusBody.Data.Status != "disabled" {
		t.Fatalf("status response = %+v", statusBody.Data)
	}
	assertW3SystemAccountPatchSessionCount(t, ctx, db, "sys_w3_patch_target", 0)
	statusCacheVersion := assertW3GatewayCacheInvalidationRedisFacts(t, ctx, cacheRedis, stateRedis, gatewaycache.SystemAccountStatusChangedReason)

	insertW3SystemAccountPatchTargetSessions(t, ctx, db, now.Add(2*time.Second), "image")
	imageRec := serveW3SystemAccountPatchRequest(router, adminSessionToken, "sys_w3_patch_target", `{"imageGenerationEnabled":true}`, "req_w3_patch_image")
	if imageRec.Code != http.StatusOK {
		t.Fatalf("image generation update status = %d, body = %s", imageRec.Code, imageRec.Body.String())
	}
	var imageBody struct {
		Data managementsystemaccounts.Summary `json:"data"`
	}
	if err := json.NewDecoder(imageRec.Body).Decode(&imageBody); err != nil {
		t.Fatalf("decode image generation response: %v", err)
	}
	if !imageBody.Data.ImageGenerationEnabled {
		t.Fatalf("image generation response = %+v", imageBody.Data)
	}
	assertW3SystemAccountPatchImageGeneration(t, ctx, db, true)
	assertW3SystemAccountPatchSessionCount(t, ctx, db, "sys_w3_patch_target", 2)
	imageCacheVersion := assertW3GatewayCacheInvalidationRedisFacts(t, ctx, cacheRedis, stateRedis, gatewaycache.SystemAccountImageGenerationChangedReason)
	if imageCacheVersion == statusCacheVersion {
		t.Fatalf("gateway API key validation cache version did not change after image update: %q", imageCacheVersion)
	}

	profileRec := serveW3SystemAccountPatchRequest(router, adminSessionToken, "sys_w3_patch_target", `{"displayName":"W3PatchRenamed","description":"更新说明"}`, "req_w3_patch_profile")
	if profileRec.Code != http.StatusOK {
		t.Fatalf("profile update status = %d, body = %s", profileRec.Code, profileRec.Body.String())
	}
	var profileBody struct {
		Data managementsystemaccounts.Summary `json:"data"`
	}
	if err := json.NewDecoder(profileRec.Body).Decode(&profileBody); err != nil {
		t.Fatalf("decode profile response: %v", err)
	}
	if profileBody.Data.DisplayName != "W3PatchRenamed" || profileBody.Data.Description != "更新说明" || profileBody.Data.Status != "disabled" || !profileBody.Data.ImageGenerationEnabled {
		t.Fatalf("profile response = %+v", profileBody.Data)
	}

	demoteRec := serveW3SystemAccountPatchRequest(router, adminSessionToken, "sys_w3_patch_admin", `{"role":"user"}`, "req_w3_patch_demote_last_super")
	if demoteRec.Code != http.StatusConflict || !strings.Contains(demoteRec.Body.String(), "至少保留一个启用的超级管理员") {
		t.Fatalf("last active super admin demote status = %d, body = %s", demoteRec.Code, demoteRec.Body.String())
	}
	assertW3SystemAccountPatchAdminRole(t, ctx, db)

	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, func() error {
		workerErrMu.Lock()
		defer workerErrMu.Unlock()
		return workerRunErr
	}); err != nil {
		t.Fatal(err)
	}
	assertW3SystemAccountPatchOperationLogs(t, ctx, db)
}

func httpapiNewW3SystemAccountPatchRouter(
	t *testing.T,
	cfg config.Config,
	authenticator *managementauth.Authenticator,
	service *managementsystemaccounts.Service,
	logClient *queue.Client,
	now func() time.Time,
	newLogID func() string,
) http.Handler {
	t.Helper()
	return httpapi.NewRouter(httpapi.RouterOptions{
		Config:                           cfg,
		Logger:                           slog.Default(),
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementSystemAccountPatchHandler: httpapi.NewManagementSystemAccountPatchHandlerWithOperationLog(
			service,
			httpapi.ManagementOperationLogOptions{
				Config:   cfg,
				Logger:   slog.Default(),
				Client:   logClient,
				Now:      now,
				NewLogID: newLogID,
			},
		),
	})
}

func insertW3SystemAccountPatchFixtures(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES
			('sys_w3_patch_admin', 'w3-patch-admin', 'W3 Patch Admin', NULL, 'super_admin', 'active', 'hash',
			 false, false, $1, $2),
			('sys_w3_patch_target', 'w3-patch-target', 'W3 Patch Target', '原始说明', 'user', 'active', 'old-hash',
			 true, false, $1, $2)
	`, now, now)
	if err != nil {
		t.Fatalf("insert W3 system account patch fixtures: %v", err)
	}
}

func insertW3SystemAccountPatchTargetSessions(t *testing.T, ctx context.Context, db *sql.DB, now time.Time, suffix string) {
	t.Helper()
	insertW2ManagementSessionForAccountFixture(t, ctx, db, "sess_w3_patch_target_"+suffix+"_1", "sys_w3_patch_target", "w3-patch-target-session-"+suffix+"-1", now)
	insertW2ManagementSessionForAccountFixture(t, ctx, db, "sess_w3_patch_target_"+suffix+"_2", "sys_w3_patch_target", "w3-patch-target-session-"+suffix+"-2", now)
}

func serveW3SystemAccountPatchRequest(router http.Handler, token string, id string, body string, requestID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPatch, "/__aisys__/api/system-accounts/"+id, strings.NewReader(body))
	req.Header.Set("Cookie", managementauth.SessionCookieName+"="+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "w3-system-account-patch-smoke")
	req.Header.Set("X-Request-Id", requestID)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func w3RedisURLWithDB(t *testing.T, rawURL string, db int) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse redis url for db rewrite: %v", err)
	}
	parsed.Path = "/" + strconv.Itoa(db)
	return parsed.String()
}

func assertW3SystemAccountPatchPassword(t *testing.T, ctx context.Context, db *sql.DB, password string) {
	t.Helper()
	var hash string
	if err := db.QueryRowContext(ctx, `
		SELECT password_hash
		FROM juhe_business.system_accounts
		WHERE id = 'sys_w3_patch_target'
	`).Scan(&hash); err != nil {
		t.Fatalf("read W3 patch target password hash: %v", err)
	}
	if hash == "" || hash == password || !managementauth.VerifyPassword(password, hash) {
		t.Fatalf("unexpected W3 patch target password hash %q", hash)
	}
}

func assertW3SystemAccountPatchImageGeneration(t *testing.T, ctx context.Context, db *sql.DB, want bool) {
	t.Helper()
	var got bool
	if err := db.QueryRowContext(ctx, `
		SELECT image_generation_enabled
		FROM juhe_business.system_accounts
		WHERE id = 'sys_w3_patch_target'
	`).Scan(&got); err != nil {
		t.Fatalf("read W3 patch target image generation flag: %v", err)
	}
	if got != want {
		t.Fatalf("W3 patch image generation enabled = %v, want %v", got, want)
	}
}

func assertW3GatewayCacheInvalidationRedisFacts(
	t *testing.T,
	ctx context.Context,
	cacheRedis *redisplatform.Client,
	stateRedis *redisplatform.Client,
	wantReason string,
) string {
	t.Helper()
	versionKey, err := gatewaycache.SharedCacheVersionKey("w3-system-account-patch", gatewaycache.APIKeyValidationCacheName)
	if err != nil {
		t.Fatalf("build W3 gateway cache version key: %v", err)
	}
	versionValue, err := cacheRedis.GetRaw(ctx, versionKey)
	if err != nil {
		t.Fatalf("read W3 gateway API key validation cache version key %s: %v", versionKey, err)
	}
	if strings.TrimSpace(string(versionValue)) == "" {
		t.Fatalf("W3 gateway API key validation cache version key %s is empty", versionKey)
	}

	stateKey, err := gatewaycache.RuntimeStateKey("w3-system-account-patch", gatewaycache.RuntimeInvalidationStoreName, "topic:"+gatewaycache.GatewayRuntimeCacheTopic)
	if err != nil {
		t.Fatalf("build W3 gateway runtime invalidation key: %v", err)
	}
	rawState, err := stateRedis.GetRaw(ctx, stateKey)
	if err != nil {
		t.Fatalf("read W3 gateway runtime invalidation key %s: %v", stateKey, err)
	}
	var state struct {
		Version     string `json:"version"`
		Reason      string `json:"reason"`
		PublishedAt string `json:"publishedAt"`
	}
	if err := json.Unmarshal(rawState, &state); err != nil {
		t.Fatalf("decode W3 gateway runtime invalidation state %s: %v", rawState, err)
	}
	if state.Version == "" || state.Reason != wantReason || state.PublishedAt == "" {
		t.Fatalf("W3 gateway runtime invalidation state = %+v, want reason %q", state, wantReason)
	}
	return string(versionValue)
}

func assertW3SystemAccountPatchSessionCount(t *testing.T, ctx context.Context, db *sql.DB, accountID string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_business.system_sessions
		WHERE system_account_id = $1
	`, accountID).Scan(&got); err != nil {
		t.Fatalf("count W3 patch sessions for %s: %v", accountID, err)
	}
	if got != want {
		t.Fatalf("W3 patch session count for %s = %d, want %d", accountID, got, want)
	}
}

func assertW3SystemAccountPatchAdminRole(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var role string
	var status string
	if err := db.QueryRowContext(ctx, `
		SELECT role, status
		FROM juhe_business.system_accounts
		WHERE id = 'sys_w3_patch_admin'
	`).Scan(&role, &status); err != nil {
		t.Fatalf("read W3 patch admin role: %v", err)
	}
	if role != "super_admin" || status != "active" {
		t.Fatalf("W3 patch admin role/status = %s/%s, want super_admin/active", role, status)
	}
}

func waitForOperationLogQueueDrained(ctx context.Context, inspector *queue.Inspector, workerDone <-chan struct{}, workerErr func() error) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		info, err := inspector.QueueInfo(operationlogjob.QueueName)
		if err != nil {
			return err
		}
		if info.Archived > 0 {
			return fmt.Errorf("operation log queue archived %d task(s)", info.Archived)
		}
		if info.Pending == 0 && info.Active == 0 && info.Retry == 0 {
			return nil
		}

		select {
		case <-workerDone:
			if err := workerErr(); err != nil {
				return err
			}
			return fmt.Errorf("ingest worker stopped before operation log queue drained")
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func assertW3SystemAccountPatchOperationLogs(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	reset := readW3SystemAccountPatchOperationLog(t, ctx, db, "oplog_w3_system_account_patch_1")
	if reset.OperationKey != "system_accounts.reset_password" ||
		reset.Action != "reset_password" ||
		reset.ResourceID != "sys_w3_patch_target" ||
		reset.TraceID != "req_w3_patch_reset" {
		t.Fatalf("reset operation log = %+v", reset)
	}
	if strings.Contains(reset.ChangesJSON, "NewPass123") {
		t.Fatalf("reset operation log leaked password: %s", reset.ChangesJSON)
	}
	resetChanges := decodeW3SystemAccountPatchChanges(t, reset.ChangesJSON)
	if len(resetChanges) == 0 || resetChanges[0].Field != "password" || !resetChanges[0].Sensitive || resetChanges[0].After != "已重置" {
		t.Fatalf("reset operation log changes = %+v", resetChanges)
	}
	resetMetadata := decodeW3SystemAccountPatchMetadata(t, reset.MetadataJSON)
	if resetMetadata["revokedSessionCount"] != float64(2) {
		t.Fatalf("reset operation log metadata = %+v", resetMetadata)
	}

	status := readW3SystemAccountPatchOperationLog(t, ctx, db, "oplog_w3_system_account_patch_2")
	if status.OperationKey != "system_accounts.update" ||
		status.Action != "update" ||
		status.ResourceID != "sys_w3_patch_target" ||
		status.TraceID != "req_w3_patch_status" {
		t.Fatalf("status operation log = %+v", status)
	}
	statusChanges := decodeW3SystemAccountPatchChanges(t, status.ChangesJSON)
	if len(statusChanges) != 1 || statusChanges[0].Field != "status" || statusChanges[0].Before != "active" || statusChanges[0].After != "disabled" {
		t.Fatalf("status operation log changes = %+v", statusChanges)
	}
	statusMetadata := decodeW3SystemAccountPatchMetadata(t, status.MetadataJSON)
	if statusMetadata["revokedSessionCount"] != float64(2) {
		t.Fatalf("status operation log metadata = %+v", statusMetadata)
	}

	image := readW3SystemAccountPatchOperationLog(t, ctx, db, "oplog_w3_system_account_patch_3")
	if image.OperationKey != "system_accounts.update" ||
		image.Action != "update" ||
		image.ResourceID != "sys_w3_patch_target" ||
		image.TraceID != "req_w3_patch_image" {
		t.Fatalf("image operation log = %+v", image)
	}
	imageChanges := decodeW3SystemAccountPatchChanges(t, image.ChangesJSON)
	if len(imageChanges) != 1 || imageChanges[0].Field != "imageGenerationEnabled" || imageChanges[0].Before != false || imageChanges[0].After != true {
		t.Fatalf("image operation log changes = %+v", imageChanges)
	}

	profile := readW3SystemAccountPatchOperationLog(t, ctx, db, "oplog_w3_system_account_patch_4")
	if profile.OperationKey != "system_accounts.update" ||
		profile.Action != "update" ||
		profile.ResourceID != "sys_w3_patch_target" ||
		profile.ResourceName != "W3PatchRenamed" ||
		profile.TraceID != "req_w3_patch_profile" {
		t.Fatalf("profile operation log = %+v", profile)
	}
	profileChanges := decodeW3SystemAccountPatchChanges(t, profile.ChangesJSON)
	if !w3SystemAccountPatchHasChange(profileChanges, "displayName", "W3 Patch Target", "W3PatchRenamed") ||
		!w3SystemAccountPatchHasChange(profileChanges, "description", "原始说明", "更新说明") {
		t.Fatalf("profile operation log changes = %+v", profileChanges)
	}
	for _, change := range profileChanges {
		if change.Field == "password" || change.Field == "status" || change.Field == "imageGenerationEnabled" || change.Sensitive {
			t.Fatalf("profile operation log contains unexpected change: %+v", change)
		}
	}

	var total int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_dataset.operation_logs
		WHERE id LIKE 'oplog_w3_system_account_patch_%'
	`).Scan(&total); err != nil {
		t.Fatalf("count W3 patch operation logs: %v", err)
	}
	if total != 4 {
		t.Fatalf("W3 patch operation log count = %d, want 4", total)
	}
}

type w3SystemAccountPatchOperationLogRow struct {
	ID           string
	TraceID      string
	OperationKey string
	Action       string
	ResourceID   string
	ResourceName string
	ChangesJSON  string
	MetadataJSON string
}

func readW3SystemAccountPatchOperationLog(t *testing.T, ctx context.Context, db *sql.DB, id string) w3SystemAccountPatchOperationLogRow {
	t.Helper()
	var row w3SystemAccountPatchOperationLogRow
	if err := db.QueryRowContext(ctx, `
		SELECT id, trace_id, operation_key, action, resource_id, resource_name, changes_json, metadata_json
		FROM juhe_dataset.operation_logs
		WHERE id = $1
	`, id).Scan(&row.ID, &row.TraceID, &row.OperationKey, &row.Action, &row.ResourceID, &row.ResourceName, &row.ChangesJSON, &row.MetadataJSON); err != nil {
		t.Fatalf("read W3 patch operation log %s: %v", id, err)
	}
	return row
}

func decodeW3SystemAccountPatchChanges(t *testing.T, raw string) []port.OperationLogChange {
	t.Helper()
	var changes []port.OperationLogChange
	if err := json.Unmarshal([]byte(raw), &changes); err != nil {
		t.Fatalf("decode W3 patch operation log changes %s: %v", raw, err)
	}
	return changes
}

func decodeW3SystemAccountPatchMetadata(t *testing.T, raw string) map[string]any {
	t.Helper()
	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		t.Fatalf("decode W3 patch operation log metadata %s: %v", raw, err)
	}
	return metadata
}

func w3SystemAccountPatchHasChange(changes []port.OperationLogChange, field string, before any, after any) bool {
	for _, change := range changes {
		if change.Field == field && change.Before == before && change.After == after {
			return true
		}
	}
	return false
}
