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
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	goredis "github.com/redis/go-redis/v9"
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
	"juhe-ai/backend-go/internal/modules/managementauthorizations"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w4AuthorizationRevokeNamespace = "w4-management-authorization-revoke"
	w4AuthorizationRevokeAdminID   = "sys_w4_authorization_revoke_admin"
	w4AuthorizationRevokeOwnerID   = "sys_w4_authorization_revoke_owner"
	w4AuthorizationRevokeGranteeID = "sys_w4_authorization_revoke_grantee"
	w4AuthorizationRevokeSessionID = "sess_w4_authorization_revoke_admin"
	w4AuthorizationRevokeToken     = "w4-authorization-revoke-admin-session-token"
	w4AuthorizationRevokeGroupID   = "grp_w4_authorization_revoke_owner"
	w4AuthorizationRevokeGrantID   = "rauthgrant_w4_authorization_revoke"
	w4AuthorizationRevokeRuntimeID = "rauth_w4_authorization_revoke"
	w4AuthorizationRevokeSourceID  = "rauthsrc_w4_authorization_revoke"
	w4AuthorizationRevokeLogID     = "oplog_w4_authorization_revoke"
	w4AuthorizationRevokeCanary    = "w4-authorization-revoke-sensitive-canary"
)

func TestW4ManagementAuthorizationRevokePostgresRedisAsynqSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var (
		postgresContainer *tcpostgres.PostgresContainer
		redisContainer    *tcredis.RedisContainer
		db                *sql.DB
		store             *postgresstore.Store
		stateRedis        *redisplatform.Client
		keyspaceRedis     *goredis.Client
		logClient         *queue.Client
		inspector         *queue.Inspector
		httpServer        *httptest.Server
		stopWorker        context.CancelFunc
		workerDone        chan struct{}
		workerErrMu       sync.Mutex
		workerRunErr      error
	)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if stopWorker != nil {
			stopWorker()
			select {
			case <-workerDone:
			case <-cleanupCtx.Done():
				t.Errorf("authorization revoke worker shutdown: %v", cleanupCtx.Err())
			}
			workerErrMu.Lock()
			err := workerRunErr
			workerErrMu.Unlock()
			if err != nil {
				t.Errorf("authorization revoke worker run: %v", err)
			}
		}
		if httpServer != nil {
			httpServer.Close()
		}
		if inspector != nil {
			if err := inspector.Close(); err != nil {
				t.Errorf("close authorization revoke queue inspector: %v", err)
			}
		}
		if logClient != nil {
			if err := logClient.Close(); err != nil {
				t.Errorf("close authorization revoke queue client: %v", err)
			}
		}
		if stateRedis != nil {
			if err := stateRedis.Close(); err != nil {
				t.Errorf("close authorization revoke state redis: %v", err)
			}
		}
		if keyspaceRedis != nil {
			if err := keyspaceRedis.Close(); err != nil {
				t.Errorf("close authorization revoke keyspace redis: %v", err)
			}
		}
		if store != nil {
			store.Close()
		}
		if db != nil {
			if err := db.Close(); err != nil {
				t.Errorf("close authorization revoke postgres: %v", err)
			}
		}
		if redisContainer != nil {
			if err := redisContainer.Terminate(cleanupCtx); err != nil {
				t.Errorf("terminate authorization revoke redis: %v", err)
			}
		}
		if postgresContainer != nil {
			if err := postgresContainer.Terminate(cleanupCtx); err != nil {
				t.Errorf("terminate authorization revoke postgres: %v", err)
			}
		}
	})

	var err error
	postgresContainer, err = tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("juhe_ai"),
		tcpostgres.WithUsername("juhe_ai"),
		tcpostgres.WithPassword("juhe_ai_password"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start authorization revoke postgres: %v", err)
	}
	postgresURL, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("authorization revoke postgres connection string: %v", err)
	}
	db = openSQLDB(t, postgresURL)
	runGooseMigrations(t, db)
	version, err := goose.GetDBVersion(db)
	if err != nil {
		t.Fatalf("read authorization revoke Goose version: %v", err)
	}
	if version != 55 {
		t.Fatalf("authorization revoke Goose version = %d, want 55", version)
	}

	redisContainer, err = tcredis.Run(ctx, redisImage)
	if err != nil {
		t.Fatalf("start authorization revoke redis: %v", err)
	}
	redisURL, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("authorization revoke redis connection string: %v", err)
	}
	redisQueueURL := w3RedisURLWithDB(t, redisURL, 0)
	redisStateURL := w3RedisURLWithDB(t, redisURL, 1)
	redisOpts, err := queue.ParseRedisURL(redisQueueURL)
	if err != nil {
		t.Fatalf("parse authorization revoke queue redis URL: %v", err)
	}
	stateRedis, err = redisplatform.NewClient(redisStateURL, w4AuthorizationRevokeNamespace+":state")
	if err != nil {
		t.Fatalf("open authorization revoke state redis: %v", err)
	}
	keyspaceOpts, err := goredis.ParseURL(redisStateURL)
	if err != nil {
		t.Fatalf("parse authorization revoke state redis URL: %v", err)
	}
	keyspaceRedis = goredis.NewClient(keyspaceOpts)

	now := time.Date(2026, 7, 17, 14, 0, 0, 0, time.UTC)
	insertW4AuthorizationRevokeFixtures(t, ctx, db, now)
	insertW2ManagementSessionForAccountFixture(t, ctx, db, w4AuthorizationRevokeSessionID, w4AuthorizationRevokeAdminID, w4AuthorizationRevokeToken, now.Add(-time.Minute))

	store, err = postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open authorization revoke postgres store: %v", err)
	}
	var invalidationCalls int
	invalidator, err := gatewaycache.NewSystemAccountInvalidator(gatewaycache.SystemAccountInvalidatorOptions{
		State: stateRedis, Namespace: w4AuthorizationRevokeNamespace, Now: func() time.Time { return now },
		NewVersion: func(time.Time) (string, error) {
			invalidationCalls++
			return fmt.Sprintf("w4-authorization-revoke-version-%d", invalidationCalls), nil
		},
	})
	if err != nil {
		t.Fatalf("create authorization revoke invalidator: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	workerCtx, workerCancel := context.WithCancel(ctx)
	stopWorker = workerCancel
	workerDone = make(chan struct{})
	go func() {
		err := app.RunIngestWorker(workerCtx, config.Config{PostgresURL: postgresURL, RedisQueueURL: redisQueueURL, RedisNamespace: "juhe-ai", LogLevel: "error", ShutdownTimeout: time.Second}, logger)
		workerErrMu.Lock()
		workerRunErr = err
		workerErrMu.Unlock()
		close(workerDone)
	}()
	logClient = queue.NewClient(redisOpts)
	inspector = queue.NewInspector(redisOpts)
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{Store: store, Now: func() time.Time { return now }})
	service := managementauthorizations.NewServiceWithOptions(managementauthorizations.ServiceOptions{Store: store, Now: func() time.Time { return now }, Secret: "w4-authorization-revoke-test-secret", AuthorizationInvalidator: invalidator})
	cfg := config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true, TrustProxy: "false"}
	logIDCalls := 0
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config: cfg, Logger: logger,
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementAuthorizationRevokeHandler: httpapi.NewManagementAuthorizationRevokeHandlerWithOperationLog(service, httpapi.ManagementOperationLogOptions{
			Config: cfg, Logger: logger, Client: logClient, SettingsReader: store, Now: func() time.Time { return now },
			NewLogID: func() string { logIDCalls++; return w4AuthorizationRevokeLogID },
		}),
	})
	httpServer = httptest.NewServer(router)

	response := doW4AuthorizationRevokeRequest(t, ctx, httpServer.URL, "req_w4_authorization_revoke")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authorization revoke status = %d, want 200; body=%s", response.StatusCode, response.Body)
	}
	var envelope struct {
		Data managementauthorizations.Summary `json:"data"`
	}
	if err := json.Unmarshal([]byte(response.Body), &envelope); err != nil {
		t.Fatalf("decode authorization revoke response: %v", err)
	}
	if envelope.Data.ID != w4AuthorizationRevokeGrantID || envelope.Data.Status != "revoked" || envelope.Data.RevokedBy != w4AuthorizationRevokeAdminID || envelope.Data.RevokedAt == nil || !envelope.Data.RevokedAt.UTC().Equal(now) {
		t.Fatalf("authorization revoke response = %+v", envelope.Data)
	}
	assertW4AuthorizationRevokeBusinessRows(t, ctx, db, now)
	assertW4AuthorizationRevokeInvalidations(t, ctx, stateRedis, now)
	if invalidationCalls != 2 {
		t.Fatalf("authorization revoke invalidation calls = %d, want 2", invalidationCalls)
	}
	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, func() error {
		workerErrMu.Lock()
		defer workerErrMu.Unlock()
		return workerRunErr
	}); err != nil {
		t.Fatal(err)
	}
	queueBeforeRepeat := readW4AuthorizationRevokeQueueInfo(t, inspector)
	if queueBeforeRepeat.Completed != 1 || queueBeforeRepeat.Archived != 0 {
		t.Fatalf("authorization revoke queue = %+v, want completed=1 archived=0", queueBeforeRepeat)
	}
	assertW4AuthorizationRevokeOperationLog(t, ctx, db, now)
	countsBeforeRepeat := readW4AuthorizationRevokeCounts(t, ctx, db)
	if countsBeforeRepeat != (w4AuthorizationRevokeCounts{Grants: 1, Runtime: 1, Sources: 1, Dirty: 1, Logs: 1, Targets: 3, Viewers: 3, Terms: countsBeforeRepeat.Terms}) || countsBeforeRepeat.Terms == 0 {
		t.Fatalf("authorization revoke counts = %+v", countsBeforeRepeat)
	}
	businessBeforeRepeat := readW4AuthorizationRevokeBusinessSnapshot(t, ctx, db)
	redisBeforeRepeat := readW4AuthorizationRevokeRedisKeyspace(t, ctx, keyspaceRedis)
	if len(redisBeforeRepeat) != 2 {
		t.Fatalf("authorization revoke Redis keyspace = %+v, want exactly 2 invalidation keys", redisBeforeRepeat)
	}

	repeat := doW4AuthorizationRevokeRequest(t, ctx, httpServer.URL, "req_w4_authorization_revoke_repeat")
	if repeat.StatusCode != http.StatusNotFound {
		t.Fatalf("repeated authorization revoke status = %d, want 404; body=%s", repeat.StatusCode, repeat.Body)
	}
	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, func() error {
		workerErrMu.Lock()
		defer workerErrMu.Unlock()
		return workerRunErr
	}); err != nil {
		t.Fatal(err)
	}
	if got := readW4AuthorizationRevokeQueueInfo(t, inspector); got != queueBeforeRepeat {
		t.Fatalf("authorization revoke queue changed after repeat: before=%+v after=%+v", queueBeforeRepeat, got)
	}
	if got := readW4AuthorizationRevokeCounts(t, ctx, db); got != countsBeforeRepeat {
		t.Fatalf("authorization revoke counts changed after repeat: before=%+v after=%+v", countsBeforeRepeat, got)
	}
	if got := readW4AuthorizationRevokeBusinessSnapshot(t, ctx, db); got != businessBeforeRepeat {
		t.Fatalf("authorization revoke business rows changed after repeat")
	}
	if got := readW4AuthorizationRevokeRedisKeyspace(t, ctx, keyspaceRedis); !reflect.DeepEqual(got, redisBeforeRepeat) {
		t.Fatalf("authorization revoke Redis changed after repeat: before=%+v after=%+v", redisBeforeRepeat, got)
	}
	if invalidationCalls != 2 || logIDCalls != 1 {
		t.Fatalf("authorization revoke repeat side effects: invalidations=%d logIDs=%d, want 2 and 1", invalidationCalls, logIDCalls)
	}
	assertW4AuthorizationRevokeSensitiveValuesAbsent(t, ctx, db, w4AuthorizationRevokeToken, w4AuthorizationRevokeCanary)
}

type w4AuthorizationRevokeHTTPResponse struct {
	StatusCode int
	Body       string
}

func doW4AuthorizationRevokeRequest(t *testing.T, ctx context.Context, serverURL, requestID string) w4AuthorizationRevokeHTTPResponse {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, serverURL+"/__aisys__/api/authorizations/"+w4AuthorizationRevokeGrantID+"?systemAccountId="+w4AuthorizationRevokeOwnerID, nil)
	if err != nil {
		t.Fatalf("create authorization revoke request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: managementauth.SessionCookieName, Value: w4AuthorizationRevokeToken})
	req.Header.Set("User-Agent", "w4-management-authorization-revoke-smoke")
	req.Header.Set("X-Request-Id", requestID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("execute authorization revoke request: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read authorization revoke response: %v", err)
	}
	return w4AuthorizationRevokeHTTPResponse{StatusCode: resp.StatusCode, Body: string(raw)}
}

func insertW4AuthorizationRevokeFixtures(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	createdAt := now.Add(-time.Hour)
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.system_accounts (id, username, display_name, role, status, password_hash, must_change_password, image_generation_enabled, created_at, updated_at) VALUES ($1, 'w4-revoke-admin', 'W4 Revoke Admin', 'admin', 'active', 'hash', false, false, $4, $4), ($2, 'w4-revoke-owner', 'W4 Revoke Owner', 'user', 'active', 'hash', false, false, $4, $4), ($3, 'w4-revoke-grantee', 'W4 Revoke Grantee', 'user', 'active', 'hash', false, false, $4, $4)`, w4AuthorizationRevokeAdminID, w4AuthorizationRevokeOwnerID, w4AuthorizationRevokeGranteeID, createdAt); err != nil {
		t.Fatalf("insert authorization revoke accounts: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at) VALUES ($1, $2, 'W4 Revoke Owner Group', 'openai', true, false, 'personal', $3, $3)`, w4AuthorizationRevokeGroupID, w4AuthorizationRevokeOwnerID, createdAt); err != nil {
		t.Fatalf("insert authorization revoke group: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.resource_authorization_grants (id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_system_account_id, scope, status, remark, created_by, created_at, updated_at) VALUES ($1, 'group', $2, $3, 'system_account', $4, 'use', 'active', 'revoke fixture', $3, $5, $5)`, w4AuthorizationRevokeGrantID, w4AuthorizationRevokeGroupID, w4AuthorizationRevokeOwnerID, w4AuthorizationRevokeGranteeID, createdAt); err != nil {
		t.Fatalf("insert authorization revoke grant: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status, effective_source_type, activated_at, last_source_changed_at, remark, created_by, created_at, updated_at) VALUES ($1, 'group', $2, $3, $4, 'use', 'active', 'manual', $5, $5, 'revoke fixture', $3, $5, $5)`, w4AuthorizationRevokeRuntimeID, w4AuthorizationRevokeGroupID, w4AuthorizationRevokeOwnerID, w4AuthorizationRevokeGranteeID, createdAt); err != nil {
		t.Fatalf("insert authorization revoke runtime: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.resource_authorization_sources (id, authorization_id, source_type, status, activated_at, created_by, created_at, updated_at) VALUES ($1, $2, 'manual', 'active', $3, $4, $3, $3)`, w4AuthorizationRevokeSourceID, w4AuthorizationRevokeRuntimeID, createdAt, w4AuthorizationRevokeOwnerID); err != nil {
		t.Fatalf("insert authorization revoke source: %v", err)
	}
}

func assertW4AuthorizationRevokeBusinessRows(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	for _, table := range []struct{ name, id string }{{"resource_authorization_grants", w4AuthorizationRevokeGrantID}, {"resource_authorizations", w4AuthorizationRevokeRuntimeID}, {"resource_authorization_sources", w4AuthorizationRevokeSourceID}} {
		var status, revokedBy string
		var revokedAt, updatedAt time.Time
		query := fmt.Sprintf("SELECT status, revoked_by, revoked_at, updated_at FROM juhe_business.%s WHERE id = $1", table.name)
		if err := db.QueryRowContext(ctx, query, table.id).Scan(&status, &revokedBy, &revokedAt, &updatedAt); err != nil {
			t.Fatalf("read revoked %s: %v", table.name, err)
		}
		if status != "revoked" || revokedBy != w4AuthorizationRevokeAdminID || !revokedAt.UTC().Equal(now) || !updatedAt.UTC().Equal(now) {
			t.Fatalf("revoked %s = status=%q actor=%q revokedAt=%s updatedAt=%s", table.name, status, revokedBy, revokedAt, updatedAt)
		}
	}
	var groupID, reason string
	var updatedAt time.Time
	if err := db.QueryRowContext(ctx, `SELECT group_id, reason, updated_at FROM juhe_business.group_account_stats_dirty WHERE group_id = '__all__'`).Scan(&groupID, &reason, &updatedAt); err != nil {
		t.Fatalf("read authorization revoke dirty marker: %v", err)
	}
	if groupID != "__all__" || reason != managementauthorizations.ResourceAuthorizationRevokedReason || !updatedAt.UTC().Equal(now) {
		t.Fatalf("authorization revoke dirty marker = %q %q %s", groupID, reason, updatedAt)
	}
}

func assertW4AuthorizationRevokeInvalidations(t *testing.T, ctx context.Context, client *redisplatform.Client, now time.Time) {
	t.Helper()
	for index, topic := range []string{gatewaycache.GatewayRuntimeCacheTopic, gatewaycache.AuthorizationQuotaCacheTopic} {
		key, err := gatewaycache.RuntimeStateKey(w4AuthorizationRevokeNamespace, gatewaycache.RuntimeInvalidationStoreName, "topic:"+topic)
		if err != nil {
			t.Fatalf("build authorization revoke invalidation key: %v", err)
		}
		raw, err := client.GetRaw(ctx, key)
		if err != nil {
			t.Fatalf("read authorization revoke invalidation %s: %v", topic, err)
		}
		var state struct{ Version, Reason, PublishedAt string }
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatalf("decode authorization revoke invalidation %s: %v", topic, err)
		}
		wantVersion := fmt.Sprintf("w4-authorization-revoke-version-%d", index+1)
		if state.Version != wantVersion || state.Reason != managementauthorizations.ResourceAuthorizationRevokedReason || state.PublishedAt != now.UTC().Format("2006-01-02T15:04:05.000Z") {
			t.Fatalf("authorization revoke invalidation %s = %+v", topic, state)
		}
	}
}

type w4AuthorizationRevokeRedisEntry struct{ Key, Value string }

func readW4AuthorizationRevokeRedisKeyspace(t *testing.T, ctx context.Context, client *goredis.Client) []w4AuthorizationRevokeRedisEntry {
	t.Helper()
	var cursor uint64
	var keys []string
	for {
		page, next, err := client.Scan(ctx, cursor, "juhe-ai:"+w4AuthorizationRevokeNamespace+":*", 100).Result()
		if err != nil {
			t.Fatalf("scan authorization revoke Redis: %v", err)
		}
		keys = append(keys, page...)
		cursor = next
		if cursor == 0 {
			break
		}
	}
	sort.Strings(keys)
	result := make([]w4AuthorizationRevokeRedisEntry, 0, len(keys))
	for _, key := range keys {
		value, err := client.Get(ctx, key).Result()
		if err != nil {
			t.Fatalf("read authorization revoke Redis key: %v", err)
		}
		result = append(result, w4AuthorizationRevokeRedisEntry{key, value})
	}
	return result
}

func readW4AuthorizationRevokeQueueInfo(t *testing.T, inspector *queue.Inspector) queue.QueueInfo {
	t.Helper()
	info, err := inspector.QueueInfo(operationlogjob.QueueName)
	if err != nil {
		t.Fatalf("read authorization revoke queue: %v", err)
	}
	if info.Pending != 0 || info.Active != 0 || info.Retry != 0 || info.Archived != 0 {
		t.Fatalf("authorization revoke queue not drained: %+v", info)
	}
	return info
}

type w4AuthorizationRevokeCounts struct {
	Grants  int
	Runtime int
	Sources int
	Dirty   int
	Logs    int
	Targets int
	Viewers int
	Terms   int
}

func readW4AuthorizationRevokeCounts(t *testing.T, ctx context.Context, db *sql.DB) w4AuthorizationRevokeCounts {
	t.Helper()
	var result w4AuthorizationRevokeCounts
	queries := []struct {
		target *int
		query  string
		args   []any
	}{
		{&result.Grants, `SELECT count(*) FROM juhe_business.resource_authorization_grants WHERE id = $1`, []any{w4AuthorizationRevokeGrantID}}, {&result.Runtime, `SELECT count(*) FROM juhe_business.resource_authorizations WHERE id = $1`, []any{w4AuthorizationRevokeRuntimeID}}, {&result.Sources, `SELECT count(*) FROM juhe_business.resource_authorization_sources WHERE id = $1`, []any{w4AuthorizationRevokeSourceID}}, {&result.Dirty, `SELECT count(*) FROM juhe_business.group_account_stats_dirty WHERE group_id = '__all__'`, nil}, {&result.Logs, `SELECT count(*) FROM juhe_dataset.operation_logs WHERE id = $1`, []any{w4AuthorizationRevokeLogID}}, {&result.Targets, `SELECT count(*) FROM juhe_dataset.operation_log_targets WHERE operation_log_id = $1`, []any{w4AuthorizationRevokeLogID}}, {&result.Viewers, `SELECT count(*) FROM juhe_dataset.operation_log_viewers WHERE operation_log_id = $1`, []any{w4AuthorizationRevokeLogID}}, {&result.Terms, `SELECT count(*) FROM juhe_dataset.operation_log_summary_search_terms WHERE operation_log_id = $1`, []any{w4AuthorizationRevokeLogID}},
	}
	for index, query := range queries {
		if err := db.QueryRowContext(ctx, query.query, query.args...).Scan(query.target); err != nil {
			t.Fatalf("read authorization revoke count %d: %v", index, err)
		}
	}
	return result
}

func readW4AuthorizationRevokeBusinessSnapshot(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT jsonb_build_object('grants', (SELECT COALESCE(jsonb_agg(to_jsonb(g) ORDER BY g.id), '[]'::jsonb) FROM juhe_business.resource_authorization_grants g WHERE g.id = $1), 'runtime', (SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id), '[]'::jsonb) FROM juhe_business.resource_authorizations r WHERE r.id = $2), 'sources', (SELECT COALESCE(jsonb_agg(to_jsonb(s) ORDER BY s.id), '[]'::jsonb) FROM juhe_business.resource_authorization_sources s WHERE s.id = $3), 'dirty', (SELECT COALESCE(jsonb_agg(to_jsonb(d) ORDER BY d.group_id), '[]'::jsonb) FROM juhe_business.group_account_stats_dirty d WHERE d.group_id = '__all__'))::text`, w4AuthorizationRevokeGrantID, w4AuthorizationRevokeRuntimeID, w4AuthorizationRevokeSourceID).Scan(&raw); err != nil {
		t.Fatalf("read authorization revoke business snapshot: %v", err)
	}
	return raw
}

func assertW4AuthorizationRevokeOperationLog(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	var actor, key, resourceID, changes, method, path string
	var status int
	var createdAt time.Time
	if err := db.QueryRowContext(ctx, `SELECT actor_system_account_id, operation_key, resource_id, changes_json, method, path, status_code, created_at FROM juhe_dataset.operation_logs WHERE id = $1`, w4AuthorizationRevokeLogID).Scan(&actor, &key, &resourceID, &changes, &method, &path, &status, &createdAt); err != nil {
		t.Fatalf("read authorization revoke operation log: %v", err)
	}
	if actor != w4AuthorizationRevokeAdminID || key != "authorizations.revoke" || resourceID != w4AuthorizationRevokeGrantID || method != http.MethodDelete || path != "/__aisys__/api/authorizations/"+w4AuthorizationRevokeGrantID || status != http.StatusOK || !createdAt.UTC().Equal(now) {
		t.Fatalf("authorization revoke operation log = actor=%q key=%q resource=%q method=%q path=%q status=%d created=%s", actor, key, resourceID, method, path, status, createdAt)
	}
	var decoded []struct {
		Field         string
		Before, After bool
		Sensitive     bool
	}
	if err := json.Unmarshal([]byte(changes), &decoded); err != nil {
		t.Fatalf("decode authorization revoke log changes: %v", err)
	}
	if len(decoded) != 1 || decoded[0].Field != "revoked" || decoded[0].Before || !decoded[0].After || decoded[0].Sensitive {
		t.Fatalf("authorization revoke log changes = %+v", decoded)
	}
	assertW4AuthorizationRevokeTargets(t, ctx, db)
	assertW4AuthorizationRevokeViewers(t, ctx, db)
	assertW4AuthorizationRevokeTerms(t, ctx, db)
}

func assertW4AuthorizationRevokeTargets(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT target_type, target_id, target_name, target_owner_system_account_id, relation FROM juhe_dataset.operation_log_targets WHERE operation_log_id = $1 ORDER BY relation`, w4AuthorizationRevokeLogID)
	if err != nil {
		t.Fatalf("query authorization revoke targets: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var kind string
		var id, name, owner sql.NullString
		var relation string
		if err := rows.Scan(&kind, &id, &name, &owner, &relation); err != nil {
			t.Fatalf("scan authorization revoke target: %v", err)
		}
		got[relation] = strings.Join([]string{kind, id.String, name.String, owner.String}, "|")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate authorization revoke targets: %v", err)
	}
	want := map[string]string{
		"owner":   "group|" + w4AuthorizationRevokeGroupID + "|W4 Revoke Owner Group|" + w4AuthorizationRevokeOwnerID,
		"grantee": "system_account|" + w4AuthorizationRevokeGranteeID + "|W4 Revoke Grantee|" + w4AuthorizationRevokeGranteeID,
		"primary": "authorization|" + w4AuthorizationRevokeGrantID + "|W4 Revoke Owner Group|" + w4AuthorizationRevokeOwnerID,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("authorization revoke targets = %+v, want %+v", got, want)
	}
}

func assertW4AuthorizationRevokeViewers(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT system_account_id, visibility_reason, detail_level FROM juhe_dataset.operation_log_viewers WHERE operation_log_id = $1`, w4AuthorizationRevokeLogID)
	if err != nil {
		t.Fatalf("query authorization revoke viewers: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var account, reason, detail string
		if err := rows.Scan(&account, &reason, &detail); err != nil {
			t.Fatalf("scan authorization revoke viewer: %v", err)
		}
		got[account+"|"+reason] = detail
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate authorization revoke viewers: %v", err)
	}
	want := map[string]string{
		w4AuthorizationRevokeAdminID + "|actor_self":              "full",
		w4AuthorizationRevokeOwnerID + "|authorization_owner":     "full",
		w4AuthorizationRevokeGranteeID + "|authorization_grantee": "full",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("authorization revoke viewers = %+v, want %+v", got, want)
	}
}

func assertW4AuthorizationRevokeTerms(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT term FROM juhe_dataset.operation_log_summary_search_terms WHERE operation_log_id = $1`, w4AuthorizationRevokeLogID)
	if err != nil {
		t.Fatalf("query authorization revoke search terms: %v", err)
	}
	defer rows.Close()
	terms := map[string]bool{}
	for rows.Next() {
		var term string
		if err := rows.Scan(&term); err != nil {
			t.Fatalf("scan authorization revoke search term: %v", err)
		}
		terms[term] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate authorization revoke search terms: %v", err)
	}
	for _, term := range []string{"w4", "revoke", "owner", "group", "grantee"} {
		if !terms[term] {
			t.Fatalf("authorization revoke search terms missing %q: %+v", term, terms)
		}
	}
}

func assertW4AuthorizationRevokeSensitiveValuesAbsent(t *testing.T, ctx context.Context, db *sql.DB, values ...string) {
	t.Helper()
	queries := []struct {
		name, query string
		args        []any
	}{
		{"grant", `SELECT COALESCE(row_to_json(x)::text, '') FROM juhe_business.resource_authorization_grants x WHERE id = $1`, []any{w4AuthorizationRevokeGrantID}},
		{"runtime", `SELECT COALESCE(row_to_json(x)::text, '') FROM juhe_business.resource_authorizations x WHERE id = $1`, []any{w4AuthorizationRevokeRuntimeID}},
		{"source", `SELECT COALESCE(row_to_json(x)::text, '') FROM juhe_business.resource_authorization_sources x WHERE id = $1`, []any{w4AuthorizationRevokeSourceID}},
		{"operation log", `SELECT COALESCE(string_agg(row_to_json(x)::text, ''), '') FROM juhe_dataset.operation_logs x WHERE id = $1`, []any{w4AuthorizationRevokeLogID}},
		{"targets", `SELECT COALESCE(string_agg(row_to_json(x)::text, ''), '') FROM juhe_dataset.operation_log_targets x WHERE operation_log_id = $1`, []any{w4AuthorizationRevokeLogID}},
		{"viewers", `SELECT COALESCE(string_agg(row_to_json(x)::text, ''), '') FROM juhe_dataset.operation_log_viewers x WHERE operation_log_id = $1`, []any{w4AuthorizationRevokeLogID}},
		{"search terms", `SELECT COALESCE(string_agg(row_to_json(x)::text, ''), '') FROM juhe_dataset.operation_log_summary_search_terms x WHERE operation_log_id = $1`, []any{w4AuthorizationRevokeLogID}},
		{"session", `SELECT COALESCE(row_to_json(x)::text, '') FROM juhe_business.system_sessions x WHERE id = $1`, []any{w4AuthorizationRevokeSessionID}},
	}
	for _, value := range values {
		for _, query := range queries {
			var raw string
			if err := db.QueryRowContext(ctx, query.query, query.args...).Scan(&raw); err != nil {
				t.Fatalf("scan authorization revoke %s: %v", query.name, err)
			}
			if strings.Contains(raw, value) {
				t.Fatalf("authorization revoke sensitive value leaked in %s", query.name)
			}
		}
	}
}
