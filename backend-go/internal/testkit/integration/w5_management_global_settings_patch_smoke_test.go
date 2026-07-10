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
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/gatewaycache"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementsettings"
	"juhe-ai/backend-go/internal/modules/publicsettings"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW5ManagementGlobalSettingsPatchPostgresRedisSmoke(t *testing.T) {
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
	stateRedis, err := redisplatform.NewClient(
		w3RedisURLWithDB(t, redisURL, 1),
		"w5-management-global-settings-patch:state",
	)
	if err != nil {
		t.Fatalf("open state redis: %v", err)
	}
	defer func() { _ = stateRedis.Close() }()
	cacheRedis, err := redisplatform.NewClient(
		w3RedisURLWithDB(t, redisURL, 2),
		"w5-management-global-settings-patch:cache",
	)
	if err != nil {
		t.Fatalf("open cache redis: %v", err)
	}
	defer func() { _ = cacheRedis.Close() }()

	now := time.Date(2026, 7, 10, 13, 0, 0, 0, time.UTC)
	const (
		cacheNamespace = "w5-management-global-settings-patch"
		cacheVersion   = "w5-management-global-settings-patch-version"
		sessionToken   = "w5-management-global-settings-patch-session"
	)
	invalidator, err := gatewaycache.NewSystemAccountInvalidator(gatewaycache.SystemAccountInvalidatorOptions{
		Cache:     cacheRedis,
		State:     stateRedis,
		Namespace: cacheNamespace,
		Now:       func() time.Time { return now },
		NewVersion: func(time.Time) (string, error) {
			return cacheVersion, nil
		},
	})
	if err != nil {
		t.Fatalf("create global settings cache invalidator: %v", err)
	}

	insertW2ProxyOptionsFixture(t, ctx, db, now)
	insertW2ManagementSessionFixture(t, ctx, db, sessionToken, now)
	staleLastSeenAt := now.Add(-2 * time.Minute)
	setW2ManagementSessionLastSeenAt(t, ctx, db, "sess_w2_management_auth", staleLastSeenAt)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	updateService := managementsettings.NewServiceWithOptions(managementsettings.ServiceOptions{
		Store:                          store,
		GlobalSettingsCacheInvalidator: invalidator,
		Now:                            func() time.Time { return now },
	})
	readService := publicsettings.NewService(store)
	operationLogs := &w5GlobalSettingsOperationLogQueue{}
	cfg := config.Config{
		Host:                 "127.0.0.1",
		Port:                 3000,
		ManagementAPIEnabled: true,
		TrustProxy:           "false",
	}
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                           cfg,
		Logger:                           slog.Default(),
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementGlobalSettingsHandler:  httpapi.NewManagementGlobalSettingsHandler(&readService),
		ManagementGlobalSettingsUpdateHandler: httpapi.NewManagementGlobalSettingsUpdateHandlerWithOperationLog(
			updateService,
			httpapi.ManagementOperationLogOptions{
				Config:   cfg,
				Logger:   slog.Default(),
				Client:   operationLogs,
				Now:      func() time.Time { return now },
				NewLogID: func() string { return "oplog_w5_management_global_settings_patch" },
			},
		),
	})

	patchRec := serveW5GlobalSettingsRequest(
		router,
		http.MethodPatch,
		sessionToken,
		`{"appName":" W5 品牌名称 ","appIcon":" /w5-brand.svg "}`,
		"req_w5_management_global_settings_patch",
	)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", patchRec.Code, patchRec.Body.String())
	}
	const wantBody = "{\"data\":{\"appName\":\"W5 品牌名称\",\"appIcon\":\"/w5-brand.svg\"}}\n"
	if got := patchRec.Body.String(); got != wantBody {
		t.Fatalf("PATCH body = %q, want %q", got, wantBody)
	}
	if got := patchRec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("PATCH Cache-Control = %q, want no-store", got)
	}

	assertW5GlobalSettingRow(t, ctx, db, "appName", "W5 品牌名称", now)
	assertW5GlobalSettingRow(t, ctx, db, "appIcon", "/w5-brand.svg", now)
	assertW2ManagementSessionLastSeenAt(t, ctx, db, "sess_w2_management_auth", now)
	assertW5GlobalSettingsCacheVersion(t, ctx, cacheRedis, cacheNamespace, cacheVersion)
	assertW5GlobalSettingsOperationLog(t, operationLogs)

	setW2ManagementSessionLastSeenAt(t, ctx, db, "sess_w2_management_auth", staleLastSeenAt)
	getRec := serveW5GlobalSettingsRequest(
		router,
		http.MethodGet,
		sessionToken,
		"",
		"req_w5_management_global_settings_get",
	)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", getRec.Code, getRec.Body.String())
	}
	if got := getRec.Body.String(); got != wantBody {
		t.Fatalf("GET body = %q, want %q", got, wantBody)
	}
	if got := getRec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("GET Cache-Control = %q, want no-store", got)
	}
	assertW2ManagementSessionLastSeenAt(t, ctx, db, "sess_w2_management_auth", staleLastSeenAt)
}

func serveW5GlobalSettingsRequest(
	router http.Handler,
	method string,
	sessionToken string,
	body string,
	requestID string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/__aisys__/api/settings/global", strings.NewReader(body))
	req.Header.Set("Cookie", managementauth.SessionCookieName+"="+sessionToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "w5-management-global-settings-patch-smoke")
	req.Header.Set("X-Request-Id", requestID)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func assertW5GlobalSettingRow(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	key string,
	wantValue string,
	wantUpdatedAt time.Time,
) {
	t.Helper()
	var valueJSON string
	var updatedAt time.Time
	if err := db.QueryRowContext(ctx, `
		SELECT value_json, updated_at
		FROM juhe_business.global_settings
		WHERE key = $1
	`, key).Scan(&valueJSON, &updatedAt); err != nil {
		t.Fatalf("read global setting %s: %v", key, err)
	}
	var value string
	if err := json.Unmarshal([]byte(valueJSON), &value); err != nil {
		t.Fatalf("decode global setting %s value %q: %v", key, valueJSON, err)
	}
	if value != wantValue {
		t.Fatalf("global setting %s = %q, want %q", key, value, wantValue)
	}
	if !updatedAt.UTC().Equal(wantUpdatedAt.UTC()) {
		t.Fatalf("global setting %s updated_at = %s, want %s",
			key,
			updatedAt.UTC().Format(time.RFC3339Nano),
			wantUpdatedAt.UTC().Format(time.RFC3339Nano),
		)
	}
}

func assertW5GlobalSettingsCacheVersion(
	t *testing.T,
	ctx context.Context,
	cacheRedis *redisplatform.Client,
	namespace string,
	wantVersion string,
) {
	t.Helper()
	key, err := gatewaycache.SharedCacheVersionKey(namespace, gatewaycache.GlobalSettingsCacheName)
	if err != nil {
		t.Fatalf("build global settings cache version key: %v", err)
	}
	value, err := cacheRedis.GetRaw(ctx, key)
	if err != nil {
		t.Fatalf("read global settings cache version key %s: %v", key, err)
	}
	if string(value) != wantVersion {
		t.Fatalf("global settings cache version = %q, want %q", value, wantVersion)
	}
}

type w5GlobalSettingsOperationLogQueue struct {
	taskType string
	options  queue.EnqueueOptions
	log      port.OperationLogInput
	err      error
}

func (q *w5GlobalSettingsOperationLogQueue) Enqueue(
	_ context.Context,
	taskType string,
	payload []byte,
	opts queue.EnqueueOptions,
) (queue.TaskInfo, error) {
	q.taskType = taskType
	q.options = opts
	q.log, q.err = operationlogjob.DecodeWriteTaskPayload(payload)
	if q.err != nil {
		return queue.TaskInfo{}, q.err
	}
	return queue.TaskInfo{ID: "task_w5_management_global_settings_patch", Queue: opts.Queue, Type: taskType}, nil
}

func assertW5GlobalSettingsOperationLog(t *testing.T, queueStub *w5GlobalSettingsOperationLogQueue) {
	t.Helper()
	if queueStub.err != nil {
		t.Fatalf("decode global settings operation log: %v", queueStub.err)
	}
	if queueStub.taskType != operationlogjob.TaskTypeWrite ||
		queueStub.options.Queue != operationlogjob.QueueName {
		t.Fatalf("operation log task=%q options=%+v", queueStub.taskType, queueStub.options)
	}
	logInput := queueStub.log
	if logInput.ID != "oplog_w5_management_global_settings_patch" ||
		logInput.TraceID != "req_w5_management_global_settings_patch" ||
		logInput.ActorSystemAccountID != "sys_w2_proxy_options" ||
		logInput.OperationKey != "settings.update_global" ||
		logInput.Module != "settings" ||
		logInput.Action != "update_global" ||
		logInput.ResourceType != "global_settings" ||
		logInput.ResourceID != "global" ||
		logInput.DetailLevel != "summary" ||
		logInput.VisibilityScope != "all_users" ||
		logInput.Method != http.MethodPatch ||
		logInput.Path != "/__aisys__/api/settings/global" ||
		logInput.ClientIP != "127.0.0.1" {
		t.Fatalf("operation log = %+v", logInput)
	}
	if logInput.StatusCode == nil || *logInput.StatusCode != http.StatusOK {
		t.Fatalf("operation log status = %+v, want 200", logInput.StatusCode)
	}
	if len(logInput.Changes) != 2 ||
		logInput.Changes[0].Field != "appName" ||
		logInput.Changes[0].Before != "聚合 AI" ||
		logInput.Changes[0].After != "W5 品牌名称" ||
		logInput.Changes[1].Field != "appIcon" ||
		logInput.Changes[1].Before != "/__aisys__/brand-icon.svg" ||
		logInput.Changes[1].After != "/w5-brand.svg" {
		t.Fatalf("operation log changes = %+v", logInput.Changes)
	}
}
