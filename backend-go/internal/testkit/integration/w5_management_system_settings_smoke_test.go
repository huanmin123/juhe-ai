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
	"juhe-ai/backend-go/internal/modules/managementsettings"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
	"juhe-ai/backend-go/internal/systemsettings"
)

const (
	w5SystemSettingsNamespace   = "w5-management-system-settings"
	w5SystemSettingsSessionID   = "sess_w2_management_auth"
	w5SystemSettingsSession     = "w5-management-system-settings-session"
	w5SystemSettingsOperationID = "oplog_w5_management_system_settings"
)

var w5SystemSettingsUpdatedValues = map[string]string{
	"accountTestTaskConcurrency":       "8",
	"gatewayTextRawBodyLimitMegabytes": "32",
	"gptFlexPriceMultiplier":           "0.75",
}

func TestW5ManagementSystemSettingsPostgresRedisAsynqSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)
	t.Setenv("JUHE_AI_USAGE_STATS_TIMEZONE", "Asia/Shanghai")

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
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
		t.Fatalf("parse redis queue url: %v", err)
	}
	stateRedis, err := redisplatform.NewClient(redisStateURL, w5SystemSettingsNamespace+":state")
	if err != nil {
		t.Fatalf("open state redis: %v", err)
	}
	defer closeRedisClient(t, stateRedis)
	cacheRedis, err := redisplatform.NewClient(redisCacheURL, w5SystemSettingsNamespace+":cache")
	if err != nil {
		t.Fatalf("open cache redis: %v", err)
	}
	defer closeRedisClient(t, cacheRedis)

	now := time.Date(2026, 7, 10, 16, 0, 0, 0, time.UTC)
	insertW2ProxyOptionsFixture(t, ctx, db, now)
	insertW2ManagementSessionFixture(t, ctx, db, w5SystemSettingsSession, now)
	staleLastSeenAt := now.Add(-2 * time.Minute)
	setW2ManagementSessionLastSeenAt(t, ctx, db, w5SystemSettingsSessionID, staleLastSeenAt)

	originalRows := readW5SystemSettingRows(t, ctx, db)
	restored := false
	defer func() {
		if restored {
			return
		}
		restoreCtx, restoreCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer restoreCancel()
		if err := restoreW5SystemSettingRows(restoreCtx, db, originalRows); err != nil {
			t.Errorf("restore W5 system settings after failure: %v", err)
		}
	}()

	var invalidationCall int
	invalidator, err := gatewaycache.NewSystemAccountInvalidator(gatewaycache.SystemAccountInvalidatorOptions{
		Cache:     cacheRedis,
		State:     stateRedis,
		Namespace: w5SystemSettingsNamespace,
		Now:       func() time.Time { return now },
		NewVersion: func(time.Time) (string, error) {
			invalidationCall++
			return fmt.Sprintf("w5-system-settings-version-%d", invalidationCall), nil
		},
	})
	if err != nil {
		t.Fatalf("create system settings cache invalidator: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
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
		}, logger)
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
	service := managementsettings.NewSystemServiceWithOptions(managementsettings.SystemServiceOptions{
		Store:       store,
		Invalidator: invalidator,
		Logger:      logger,
		Now:         func() time.Time { return now },
	})
	cfg := config.Config{
		Host:                 "127.0.0.1",
		Port:                 3000,
		ManagementAPIEnabled: true,
		TrustProxy:           "false",
	}
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                           cfg,
		Logger:                           logger,
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementSystemSettingsHandler:  httpapi.NewManagementSystemSettingsHandler(service),
		ManagementSystemSettingsUpdateHandler: httpapi.NewManagementSystemSettingsUpdateHandlerWithOperationLog(
			service,
			httpapi.ManagementOperationLogOptions{
				Config:         cfg,
				Logger:         logger,
				Client:         logClient,
				SettingsReader: store,
				Now:            func() time.Time { return now },
				NewLogID:       func() string { return w5SystemSettingsOperationID },
			},
		),
	})

	getRec := serveW5SystemSettingsRequest(
		router,
		http.MethodGet,
		"",
		"req_w5_management_system_settings_get",
	)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", getRec.Code, getRec.Body.String())
	}
	initialSettings := decodeW5SystemSettingsResponse(t, getRec)
	assertW5SystemSettingsComplete(t, initialSettings)
	assertW5SystemSettingJSONNumber(t, initialSettings, "accountTestTaskConcurrency", "100")
	assertW5SystemSettingJSONNumber(t, initialSettings, "gatewayTextRawBodyLimitMegabytes", "16")
	assertW5SystemSettingJSONNumber(t, initialSettings, "gptPriorityPriceMultiplier", "2")
	assertW5SystemSettingJSONNumber(t, initialSettings, "gptFlexPriceMultiplier", "0.5")
	initialTimezone := append(json.RawMessage(nil), initialSettings[systemsettings.UsageStatsTimezoneKey]...)
	if string(initialTimezone) != `"Asia/Shanghai"` {
		t.Fatalf("usageStatsTimezone seed = %s, want %q", initialTimezone, "Asia/Shanghai")
	}
	assertW2ManagementSessionLastSeenAt(t, ctx, db, w5SystemSettingsSessionID, staleLastSeenAt)

	patchRec := serveW5SystemSettingsRequest(
		router,
		http.MethodPatch,
		`{"gatewayTextRawBodyLimitMegabytes":32,"accountTestTaskConcurrency":8,"gptFlexPriceMultiplier":0.75}`,
		"req_w5_management_system_settings_patch",
	)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", patchRec.Code, patchRec.Body.String())
	}
	updatedSettings := decodeW5SystemSettingsResponse(t, patchRec)
	expectedSettings := cloneW5SystemSettings(initialSettings)
	for key, value := range w5SystemSettingsUpdatedValues {
		expectedSettings[key] = json.RawMessage(value)
	}
	assertW5SystemSettingsEqual(t, updatedSettings, expectedSettings)
	assertW5SystemSettingJSONNumber(t, updatedSettings, "gptFlexPriceMultiplier", "0.75")
	if got := updatedSettings[systemsettings.UsageStatsTimezoneKey]; string(got) != string(initialTimezone) {
		t.Fatalf("usageStatsTimezone changed online from %s to %s", initialTimezone, got)
	}

	for key, value := range w5SystemSettingsUpdatedValues {
		assertW5SystemSettingRow(t, ctx, db, key, value, now)
	}
	assertW2ManagementSessionLastSeenAt(t, ctx, db, w5SystemSettingsSessionID, now)
	assertW5SystemSettingsInvalidation(t, ctx, cacheRedis, stateRedis, invalidationCall, now)

	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, func() error {
		workerErrMu.Lock()
		defer workerErrMu.Unlock()
		return workerRunErr
	}); err != nil {
		t.Fatal(err)
	}
	queueInfo, err := inspector.QueueInfo(operationlogjob.QueueName)
	if err != nil {
		t.Fatalf("read operation log queue info: %v", err)
	}
	if queueInfo.Archived != 0 || queueInfo.Completed < 1 {
		t.Fatalf("operation log queue info = %+v, want at least 1 completed and 0 archived", queueInfo)
	}
	assertW5SystemSettingsOperationLog(t, ctx, db, now)

	if err := restoreW5SystemSettingRows(ctx, db, originalRows); err != nil {
		t.Fatalf("restore W5 system setting rows: %v", err)
	}
	restored = true
	assertW5SystemSettingRowsRestored(t, ctx, db, originalRows)
}

func serveW5SystemSettingsRequest(
	router http.Handler,
	method string,
	body string,
	requestID string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/__aisys__/api/settings", strings.NewReader(body))
	req.Header.Set("Cookie", managementauth.SessionCookieName+"="+w5SystemSettingsSession)
	req.Header.Set("User-Agent", "w5-management-system-settings-smoke")
	req.Header.Set("X-Request-Id", requestID)
	req.RemoteAddr = "127.0.0.1:12345"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeW5SystemSettingsResponse(
	t *testing.T,
	rec *httptest.ResponseRecorder,
) map[string]json.RawMessage {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var envelope map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode system settings response: %v", err)
	}
	if len(envelope) != 1 {
		t.Fatalf("system settings response keys = %v, want only data", envelope)
	}
	rawData, ok := envelope["data"]
	if !ok {
		t.Fatalf("system settings response = %v, missing data", envelope)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(rawData, &data); err != nil {
		t.Fatalf("decode system settings data: %v", err)
	}
	return data
}

func assertW5SystemSettingsComplete(t *testing.T, settings map[string]json.RawMessage) {
	t.Helper()
	if len(settings) != 55 {
		t.Fatalf("system settings field count = %d, want 55", len(settings))
	}
	for _, definition := range systemsettings.Definitions() {
		if _, ok := settings[definition.Key]; !ok {
			t.Fatalf("system settings missing field %q", definition.Key)
		}
	}
}

func assertW5SystemSettingsEqual(
	t *testing.T,
	got map[string]json.RawMessage,
	want map[string]json.RawMessage,
) {
	t.Helper()
	assertW5SystemSettingsComplete(t, got)
	assertW5SystemSettingsComplete(t, want)
	for _, definition := range systemsettings.Definitions() {
		if string(got[definition.Key]) != string(want[definition.Key]) {
			t.Fatalf(
				"system setting %s = %s, want %s",
				definition.Key,
				got[definition.Key],
				want[definition.Key],
			)
		}
	}
}

func cloneW5SystemSettings(input map[string]json.RawMessage) map[string]json.RawMessage {
	output := make(map[string]json.RawMessage, len(input))
	for key, value := range input {
		output[key] = append(json.RawMessage(nil), value...)
	}
	return output
}

func assertW5SystemSettingJSONNumber(
	t *testing.T,
	settings map[string]json.RawMessage,
	key string,
	want string,
) {
	t.Helper()
	raw, ok := settings[key]
	if !ok {
		t.Fatalf("system settings missing numeric field %q", key)
	}
	var got json.Number
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode system setting %s value %s: %v", key, raw, err)
	}
	if got.String() != want || string(raw) != want {
		t.Fatalf("system setting %s = %s, want normalized JSON number %s", key, raw, want)
	}
}

type w5SystemSettingRow struct {
	ValueJSON string
	UpdatedAt time.Time
}

func readW5SystemSettingRows(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) map[string]w5SystemSettingRow {
	t.Helper()
	rows := make(map[string]w5SystemSettingRow, len(w5SystemSettingsUpdatedValues))
	for key := range w5SystemSettingsUpdatedValues {
		var row w5SystemSettingRow
		if err := db.QueryRowContext(ctx, `
			SELECT value_json, updated_at
			FROM juhe_business.system_settings
			WHERE system_account_id = 'sys_admin'
			  AND key = $1
		`, key).Scan(&row.ValueJSON, &row.UpdatedAt); err != nil {
			t.Fatalf("read original W5 system setting %s: %v", key, err)
		}
		rows[key] = row
	}
	return rows
}

func assertW5SystemSettingRow(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	key string,
	wantValueJSON string,
	wantUpdatedAt time.Time,
) {
	t.Helper()
	var valueJSON string
	var updatedAt time.Time
	if err := db.QueryRowContext(ctx, `
		SELECT value_json, updated_at
		FROM juhe_business.system_settings
		WHERE system_account_id = 'sys_admin'
		  AND key = $1
	`, key).Scan(&valueJSON, &updatedAt); err != nil {
		t.Fatalf("read updated W5 system setting %s: %v", key, err)
	}
	if valueJSON != wantValueJSON {
		t.Fatalf("system setting %s value_json = %q, want %s", key, valueJSON, wantValueJSON)
	}
	if !updatedAt.UTC().Equal(wantUpdatedAt.UTC()) {
		t.Fatalf(
			"system setting %s updated_at = %s, want %s",
			key,
			updatedAt.UTC().Format(time.RFC3339Nano),
			wantUpdatedAt.UTC().Format(time.RFC3339Nano),
		)
	}
}

func restoreW5SystemSettingRows(
	ctx context.Context,
	db *sql.DB,
	original map[string]w5SystemSettingRow,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, key := range []string{
		"accountTestTaskConcurrency",
		"gatewayTextRawBodyLimitMegabytes",
		"gptFlexPriceMultiplier",
	} {
		row, ok := original[key]
		if !ok {
			return fmt.Errorf("original system setting %s is missing", key)
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE juhe_business.system_settings
			SET value_json = $1,
			    updated_at = $2
			WHERE system_account_id = 'sys_admin'
			  AND key = $3
		`, row.ValueJSON, row.UpdatedAt, key)
		if err != nil {
			return fmt.Errorf("restore system setting %s: %w", key, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read restored system setting %s rows affected: %w", key, err)
		}
		if affected != 1 {
			return fmt.Errorf("restore system setting %s affected %d rows, want 1", key, affected)
		}
	}
	return tx.Commit()
}

func assertW5SystemSettingRowsRestored(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	original map[string]w5SystemSettingRow,
) {
	t.Helper()
	for key, want := range original {
		var got w5SystemSettingRow
		if err := db.QueryRowContext(ctx, `
			SELECT value_json, updated_at
			FROM juhe_business.system_settings
			WHERE system_account_id = 'sys_admin'
			  AND key = $1
		`, key).Scan(&got.ValueJSON, &got.UpdatedAt); err != nil {
			t.Fatalf("read restored W5 system setting %s: %v", key, err)
		}
		if got.ValueJSON != want.ValueJSON || !got.UpdatedAt.UTC().Equal(want.UpdatedAt.UTC()) {
			t.Fatalf(
				"restored system setting %s = value:%q updated:%s, want value:%q updated:%s",
				key,
				got.ValueJSON,
				got.UpdatedAt.UTC().Format(time.RFC3339Nano),
				want.ValueJSON,
				want.UpdatedAt.UTC().Format(time.RFC3339Nano),
			)
		}
	}
}

func assertW5SystemSettingsInvalidation(
	t *testing.T,
	ctx context.Context,
	cacheRedis *redisplatform.Client,
	stateRedis *redisplatform.Client,
	invalidationCalls int,
	wantPublishedAt time.Time,
) {
	t.Helper()
	if invalidationCalls != 2 {
		t.Fatalf("system settings invalidation version calls = %d, want 2", invalidationCalls)
	}
	cacheKey, err := gatewaycache.SharedCacheVersionKey(
		w5SystemSettingsNamespace,
		gatewaycache.SystemSettingsCacheName,
	)
	if err != nil {
		t.Fatalf("build system settings cache version key: %v", err)
	}
	cacheVersion, err := cacheRedis.GetRaw(ctx, cacheKey)
	if err != nil {
		t.Fatalf("read system settings cache version key %s: %v", cacheKey, err)
	}
	if string(cacheVersion) != "w5-system-settings-version-1" {
		t.Fatalf("system settings cache version = %q, want %q", cacheVersion, "w5-system-settings-version-1")
	}

	stateKey, err := gatewaycache.RuntimeStateKey(
		w5SystemSettingsNamespace,
		gatewaycache.RuntimeInvalidationStoreName,
		"topic:"+gatewaycache.GatewayRuntimeCacheTopic,
	)
	if err != nil {
		t.Fatalf("build gateway runtime cache state key: %v", err)
	}
	rawState, err := stateRedis.GetRaw(ctx, stateKey)
	if err != nil {
		t.Fatalf("read gateway runtime cache state key %s: %v", stateKey, err)
	}
	var state struct {
		Version     string `json:"version"`
		Reason      string `json:"reason"`
		PublishedAt string `json:"publishedAt"`
	}
	if err := json.Unmarshal(rawState, &state); err != nil {
		t.Fatalf("decode gateway runtime cache state %s: %v", rawState, err)
	}
	wantPublished := wantPublishedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	if state.Version != "w5-system-settings-version-2" ||
		state.Reason != managementsettings.SystemSettingsUpdatedReason ||
		state.PublishedAt != wantPublished {
		t.Fatalf(
			"gateway runtime cache state = %+v, want version %q reason %q publishedAt %q",
			state,
			"w5-system-settings-version-2",
			managementsettings.SystemSettingsUpdatedReason,
			wantPublished,
		)
	}
}

func assertW5SystemSettingsOperationLog(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	wantCreatedAt time.Time,
) {
	t.Helper()
	var row struct {
		ID                   string
		TraceID              string
		ActorSystemAccountID string
		ActorUsername        string
		ActorDisplayName     string
		ActorRole            string
		Mode                 string
		Module               string
		Action               string
		OperationKey         string
		ResourceType         string
		ResourceID           string
		ResourceName         string
		Summary              string
		DetailLevel          string
		VisibilityScope      string
		ChangesJSON          string
		MetadataJSON         string
		Method               string
		Path                 string
		StatusCode           int
		ClientIP             string
		UserAgent            string
		CreatedAt            time.Time
	}
	if err := db.QueryRowContext(ctx, `
		SELECT
			id,
			trace_id,
			actor_system_account_id,
			actor_username,
			actor_display_name,
			actor_role,
			mode,
			module,
			action,
			operation_key,
			resource_type,
			resource_id,
			resource_name,
			summary,
			detail_level,
			visibility_scope,
			changes_json,
			metadata_json,
			method,
			path,
			status_code,
			client_ip,
			user_agent,
			created_at
		FROM juhe_dataset.operation_logs
		WHERE id = $1
	`, w5SystemSettingsOperationID).Scan(
		&row.ID,
		&row.TraceID,
		&row.ActorSystemAccountID,
		&row.ActorUsername,
		&row.ActorDisplayName,
		&row.ActorRole,
		&row.Mode,
		&row.Module,
		&row.Action,
		&row.OperationKey,
		&row.ResourceType,
		&row.ResourceID,
		&row.ResourceName,
		&row.Summary,
		&row.DetailLevel,
		&row.VisibilityScope,
		&row.ChangesJSON,
		&row.MetadataJSON,
		&row.Method,
		&row.Path,
		&row.StatusCode,
		&row.ClientIP,
		&row.UserAgent,
		&row.CreatedAt,
	); err != nil {
		t.Fatalf("read system settings operation log: %v", err)
	}
	if row.ID != w5SystemSettingsOperationID ||
		row.TraceID != "req_w5_management_system_settings_patch" ||
		row.ActorSystemAccountID != "sys_w2_proxy_options" ||
		row.ActorUsername != "w2-proxy-options" ||
		row.ActorDisplayName != "W2 Proxy Options" ||
		row.ActorRole != "admin" ||
		row.Mode != "admin" ||
		row.Module != "settings" ||
		row.Action != "update_settings" ||
		row.OperationKey != "settings.update" ||
		row.ResourceType != "system_settings" ||
		row.ResourceID != "system" ||
		row.ResourceName != "系统运行设置" ||
		row.Summary != "更新系统运行设置" ||
		row.DetailLevel != "summary" ||
		row.VisibilityScope != "all_users" ||
		row.Method != http.MethodPatch ||
		row.Path != "/__aisys__/api/settings" ||
		row.StatusCode != http.StatusOK ||
		row.ClientIP != "127.0.0.1" ||
		row.UserAgent != "w5-management-system-settings-smoke" ||
		!row.CreatedAt.UTC().Equal(wantCreatedAt.UTC()) {
		t.Fatalf("system settings operation log = %+v", row)
	}
	const wantChanges = `[{"field":"accountTestTaskConcurrency","label":"accountTestTaskConcurrency","before":100,"after":8},{"field":"gatewayTextRawBodyLimitMegabytes","label":"gatewayTextRawBodyLimitMegabytes","before":16,"after":32},{"field":"gptFlexPriceMultiplier","label":"gptFlexPriceMultiplier","before":0.5,"after":0.75}]`
	if row.ChangesJSON != wantChanges {
		t.Fatalf("system settings operation log changes = %s, want %s", row.ChangesJSON, wantChanges)
	}
	if row.MetadataJSON != "{}" {
		t.Fatalf("system settings operation log metadata = %s, want {}", row.MetadataJSON)
	}

	var total int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_dataset.operation_logs
		WHERE id = $1
	`, w5SystemSettingsOperationID).Scan(&total); err != nil {
		t.Fatalf("count system settings operation logs: %v", err)
	}
	if total != 1 {
		t.Fatalf("system settings operation log count = %d, want 1", total)
	}
}
