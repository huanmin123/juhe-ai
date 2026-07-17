//go:build integration

package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

	"github.com/hibiken/asynq"
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
	w4AuthorizationRevokeHeader    = "X-Test-Canary"
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
		queueRedis        *goredis.Client
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
		if queueRedis != nil {
			if err := queueRedis.Close(); err != nil {
				t.Errorf("close authorization revoke queue redis: %v", err)
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
	queueKeyspaceOpts, err := goredis.ParseURL(redisQueueURL)
	if err != nil {
		t.Fatalf("parse authorization revoke queue redis URL for keyspace scan: %v", err)
	}
	queueRedis = goredis.NewClient(queueKeyspaceOpts)

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

	var logBuffer w4AuthorizationRevokeLockedBuffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, nil))
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

	assertW4AuthorizationRevokePreRequestState(t, ctx, db)
	stateBeforeSuccess := readW4AuthorizationRevokeRedisDB(t, ctx, keyspaceRedis)
	if len(stateBeforeSuccess) != 0 {
		t.Fatalf("authorization revoke state Redis before request = %+v, want empty DB", stateBeforeSuccess)
	}
	queueBeforeSuccess := readW4AuthorizationRevokeQueueInfo(t, inspector, true)
	assertW4AuthorizationRevokeSecretFree(t, "logger before request", logBuffer.String(), w4AuthorizationRevokeToken, w4AuthorizationRevokeCanary)

	response := doW4AuthorizationRevokeRequest(t, ctx, httpServer.URL, "req_w4_authorization_revoke")
	assertW4AuthorizationRevokeHTTPResponseSecretFree(t, "success", response, w4AuthorizationRevokeToken, w4AuthorizationRevokeCanary)
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
	businessBeforeRepeat := readW4AuthorizationRevokeBusinessSnapshot(t, ctx, db)
	assertW4AuthorizationRevokeSecretFree(t, "PostgreSQL business after success", businessBeforeRepeat, w4AuthorizationRevokeToken, w4AuthorizationRevokeCanary)
	sessionAfterSuccess := readW4AuthorizationRevokeSessionSnapshot(t, ctx, db)
	assertW4AuthorizationRevokeSecretFree(t, "PostgreSQL session after success touch", sessionAfterSuccess, w4AuthorizationRevokeToken, w4AuthorizationRevokeCanary)
	stateBeforeRepeat := readW4AuthorizationRevokeRedisDB(t, ctx, keyspaceRedis)
	assertW4AuthorizationRevokeRedisDBSecretFree(t, "state Redis after success", stateBeforeRepeat, w4AuthorizationRevokeToken, w4AuthorizationRevokeCanary)
	assertW4AuthorizationRevokeSecretFree(t, "logger after business commit", logBuffer.String(), w4AuthorizationRevokeToken, w4AuthorizationRevokeCanary)
	assertW4AuthorizationRevokeBusinessRows(t, ctx, db, now)
	assertW4AuthorizationRevokeInvalidations(t, ctx, stateRedis, now)
	assertW4AuthorizationRevokeExactStateKeys(t, stateBeforeRepeat)
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
	operationLogsBeforeRepeat := readW4AuthorizationRevokeOperationLogSnapshot(t, ctx, db)
	assertW4AuthorizationRevokeSecretFree(t, "PostgreSQL operation logs after ingest", operationLogsBeforeRepeat, w4AuthorizationRevokeToken, w4AuthorizationRevokeCanary)
	queueTasksBeforeRepeat := readW4AuthorizationRevokeAsynqTaskSnapshot(t, ctx, queueRedis)
	assertW4AuthorizationRevokeAsynqPayloadsSecretFree(t, queueTasksBeforeRepeat, w4AuthorizationRevokeToken, w4AuthorizationRevokeCanary)
	assertW4AuthorizationRevokeSecretFree(t, "logger after operation-log ingest", logBuffer.String(), w4AuthorizationRevokeToken, w4AuthorizationRevokeCanary)
	assertW4AuthorizationRevokeAsynqTaskSnapshotPresent(t, queueTasksBeforeRepeat)
	queueBeforeRepeat := readW4AuthorizationRevokeQueueInfo(t, inspector, false)
	assertW4AuthorizationRevokeQueueSuccessTransition(t, queueBeforeSuccess, queueBeforeRepeat)
	countsBeforeRepeat := readW4AuthorizationRevokeCounts(t, ctx, db)
	if countsBeforeRepeat != (w4AuthorizationRevokeCounts{Grants: 1, Runtime: 1, Sources: 1, Dirty: 1, Logs: 1, Targets: 3, Viewers: 3, Terms: countsBeforeRepeat.Terms}) || countsBeforeRepeat.Terms == 0 {
		t.Fatalf("authorization revoke counts = %+v", countsBeforeRepeat)
	}
	assertW4AuthorizationRevokeOperationLog(t, ctx, db, now)

	repeat := doW4AuthorizationRevokeRequest(t, ctx, httpServer.URL, "req_w4_authorization_revoke_repeat")
	assertW4AuthorizationRevokeHTTPResponseSecretFree(t, "repeat", repeat, w4AuthorizationRevokeToken, w4AuthorizationRevokeCanary)
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
	businessAfterRepeat := readW4AuthorizationRevokeBusinessSnapshot(t, ctx, db)
	assertW4AuthorizationRevokeSecretFree(t, "PostgreSQL business after repeat", businessAfterRepeat, w4AuthorizationRevokeToken, w4AuthorizationRevokeCanary)
	// Authentication touch may update session timestamps, so scan each snapshot without requiring row equality.
	sessionAfterRepeat := readW4AuthorizationRevokeSessionSnapshot(t, ctx, db)
	assertW4AuthorizationRevokeSecretFree(t, "PostgreSQL session after repeat touch", sessionAfterRepeat, w4AuthorizationRevokeToken, w4AuthorizationRevokeCanary)
	operationLogsAfterRepeat := readW4AuthorizationRevokeOperationLogSnapshot(t, ctx, db)
	assertW4AuthorizationRevokeSecretFree(t, "PostgreSQL operation logs after repeat", operationLogsAfterRepeat, w4AuthorizationRevokeToken, w4AuthorizationRevokeCanary)
	stateAfterRepeat := readW4AuthorizationRevokeRedisDB(t, ctx, keyspaceRedis)
	assertW4AuthorizationRevokeRedisDBSecretFree(t, "state Redis after repeat", stateAfterRepeat, w4AuthorizationRevokeToken, w4AuthorizationRevokeCanary)
	queueTasksAfterRepeat := readW4AuthorizationRevokeAsynqTaskSnapshot(t, ctx, queueRedis)
	assertW4AuthorizationRevokeAsynqPayloadsSecretFree(t, queueTasksAfterRepeat, w4AuthorizationRevokeToken, w4AuthorizationRevokeCanary)
	assertW4AuthorizationRevokeSecretFree(t, "logger after repeat", logBuffer.String(), w4AuthorizationRevokeToken, w4AuthorizationRevokeCanary)
	if got := readW4AuthorizationRevokeQueueInfo(t, inspector, false); got != queueBeforeRepeat {
		t.Fatalf("authorization revoke queue changed after repeat: before=%+v after=%+v", queueBeforeRepeat, got)
	}
	if got := readW4AuthorizationRevokeCounts(t, ctx, db); got != countsBeforeRepeat {
		t.Fatalf("authorization revoke counts changed after repeat: before=%+v after=%+v", countsBeforeRepeat, got)
	}
	if businessAfterRepeat != businessBeforeRepeat {
		t.Fatalf("authorization revoke business rows changed after repeat")
	}
	if operationLogsAfterRepeat != operationLogsBeforeRepeat {
		t.Fatalf("authorization revoke operation log JSON changed after repeat")
	}
	if !reflect.DeepEqual(stateAfterRepeat, stateBeforeRepeat) {
		t.Fatal("authorization revoke state Redis key/value snapshot changed after repeat")
	}
	if !reflect.DeepEqual(queueTasksAfterRepeat, queueTasksBeforeRepeat) {
		t.Fatal("authorization revoke Asynq task/payload snapshot changed after repeat")
	}
	if invalidationCalls != 2 || logIDCalls != 1 {
		t.Fatalf("authorization revoke repeat side effects: invalidations=%d logIDs=%d, want 2 and 1", invalidationCalls, logIDCalls)
	}
}

type w4AuthorizationRevokeHTTPResponse struct {
	StatusCode int
	Body       string
	Header     http.Header
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
	req.Header.Set(w4AuthorizationRevokeHeader, w4AuthorizationRevokeCanary)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("execute authorization revoke request: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read authorization revoke response: %v", err)
	}
	return w4AuthorizationRevokeHTTPResponse{StatusCode: resp.StatusCode, Body: string(raw), Header: resp.Header.Clone()}
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

type w4AuthorizationRevokeRedisEntry struct {
	Key   string
	Type  string
	Value string
}

func readW4AuthorizationRevokeRedisDB(t *testing.T, ctx context.Context, client *goredis.Client) []w4AuthorizationRevokeRedisEntry {
	t.Helper()
	var cursor uint64
	var keys []string
	for {
		page, next, err := client.Scan(ctx, cursor, "*", 100).Result()
		if err != nil {
			t.Fatalf("scan authorization revoke Redis DB: %v", err)
		}
		keys = append(keys, page...)
		cursor = next
		if cursor == 0 {
			break
		}
	}
	sort.Strings(keys)
	entries := make([]w4AuthorizationRevokeRedisEntry, 0, len(keys))
	for _, key := range keys {
		kind, err := client.Type(ctx, key).Result()
		if err != nil {
			t.Fatalf("read authorization revoke Redis key type %q: %v", key, err)
		}
		entries = append(entries, w4AuthorizationRevokeRedisEntry{Key: key, Type: kind, Value: readW4AuthorizationRevokeRedisValue(t, ctx, client, key, kind)})
	}
	return entries
}

func readW4AuthorizationRevokeRedisValue(t *testing.T, ctx context.Context, client *goredis.Client, key, kind string) string {
	t.Helper()
	switch kind {
	case "string":
		value, err := client.Get(ctx, key).Result()
		if err != nil {
			t.Fatalf("read authorization revoke Redis string %q: %v", key, err)
		}
		return value
	case "hash":
		values, err := client.HGetAll(ctx, key).Result()
		if err != nil {
			t.Fatalf("read authorization revoke Redis hash %q: %v", key, err)
		}
		fields := make([]string, 0, len(values))
		for field := range values {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		parts := make([]string, 0, len(fields))
		for _, field := range fields {
			parts = append(parts, field+"="+values[field])
		}
		return strings.Join(parts, "\n")
	case "list":
		values, err := client.LRange(ctx, key, 0, -1).Result()
		if err != nil {
			t.Fatalf("read authorization revoke Redis list %q: %v", key, err)
		}
		return strings.Join(values, "\n")
	case "set":
		values, err := client.SMembers(ctx, key).Result()
		if err != nil {
			t.Fatalf("read authorization revoke Redis set %q: %v", key, err)
		}
		sort.Strings(values)
		return strings.Join(values, "\n")
	case "zset":
		values, err := client.ZRangeWithScores(ctx, key, 0, -1).Result()
		if err != nil {
			t.Fatalf("read authorization revoke Redis zset %q: %v", key, err)
		}
		parts := make([]string, 0, len(values))
		for _, value := range values {
			parts = append(parts, fmt.Sprintf("%v=%g", value.Member, value.Score))
		}
		return strings.Join(parts, "\n")
	default:
		raw, err := client.Dump(ctx, key).Result()
		if err != nil {
			t.Fatalf("dump authorization revoke Redis %s key %q: %v", kind, key, err)
		}
		return string(raw)
	}
}

func readW4AuthorizationRevokeAsynqTaskSnapshot(t *testing.T, ctx context.Context, client *goredis.Client) []w4AuthorizationRevokeRedisEntry {
	t.Helper()
	queuePrefix := "asynq:{" + operationlogjob.QueueName + "}:"
	taskPrefix := queuePrefix + "t:"
	stableStateKeys := map[string]bool{
		queuePrefix + "pending":   true,
		queuePrefix + "active":    true,
		queuePrefix + "scheduled": true,
		queuePrefix + "retry":     true,
		queuePrefix + "archived":  true,
		queuePrefix + "completed": true,
	}
	all := readW4AuthorizationRevokeRedisDB(t, ctx, client)
	result := make([]w4AuthorizationRevokeRedisEntry, 0, len(all))
	for _, entry := range all {
		if strings.HasPrefix(entry.Key, taskPrefix) || stableStateKeys[entry.Key] {
			result = append(result, entry)
		}
	}
	return result
}

func assertW4AuthorizationRevokeAsynqTaskSnapshotPresent(t *testing.T, entries []w4AuthorizationRevokeRedisEntry) {
	t.Helper()
	queuePrefix := "asynq:{" + operationlogjob.QueueName + "}:"
	hasTaskPayload := false
	hasCompletedState := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Key, queuePrefix+"t:") && entry.Type == "hash" && strings.Contains(entry.Value, "msg=") {
			hasTaskPayload = true
		}
		if entry.Key == queuePrefix+"completed" {
			hasCompletedState = true
		}
	}
	if !hasTaskPayload || !hasCompletedState {
		t.Fatalf("authorization revoke stable Asynq task snapshot missing task payload or completed state")
	}
}

func readW4AuthorizationRevokeQueueInfo(t *testing.T, inspector *queue.Inspector, allowMissing bool) queue.QueueInfo {
	t.Helper()
	info, err := inspector.QueueInfo(operationlogjob.QueueName)
	if err != nil {
		if allowMissing && isW4AuthorizationRevokeQueueNotFound(err, operationlogjob.QueueName) {
			return queue.QueueInfo{Queue: operationlogjob.QueueName}
		}
		t.Fatalf("read authorization revoke queue: %v", err)
	}
	if info.Pending != 0 || info.Active != 0 || info.Retry != 0 || info.Archived != 0 {
		t.Fatalf("authorization revoke queue not drained: %+v", info)
	}
	return info
}

func isW4AuthorizationRevokeQueueNotFound(err error, queueName string) bool {
	if err == nil || strings.TrimSpace(queueName) == "" {
		return false
	}
	if errors.Is(err, asynq.ErrQueueNotFound) {
		return true
	}
	want := fmt.Sprintf("NOT_FOUND: queue %q does not exist", queueName)
	for current := err; current != nil; current = errors.Unwrap(current) {
		if current.Error() == want {
			return true
		}
	}
	return false
}

func TestIsW4AuthorizationRevokeQueueNotFound(t *testing.T) {
	actual := fmt.Errorf(`NOT_FOUND: queue %q does not exist`, operationlogjob.QueueName)
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "actual inspector error", err: actual, want: true},
		{name: "wrapped actual inspector error", err: fmt.Errorf("inspect queue: %w", actual), want: true},
		{name: "public sentinel", err: fmt.Errorf("inspect queue: %w", asynq.ErrQueueNotFound), want: true},
		{name: "different queue", err: fmt.Errorf(`NOT_FOUND: queue %q does not exist`, "other-queue")},
		{name: "different not found error", err: errors.New("NOT_FOUND: redis key does not exist")},
		{name: "connection error", err: errors.New("dial tcp: connection refused")},
		{name: "nil", err: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isW4AuthorizationRevokeQueueNotFound(tc.err, operationlogjob.QueueName); got != tc.want {
				t.Fatalf("isW4AuthorizationRevokeQueueNotFound() = %t, want %t", got, tc.want)
			}
		})
	}
}

func assertW4AuthorizationRevokeQueueSuccessTransition(t *testing.T, before, after queue.QueueInfo) {
	t.Helper()
	if err := validateW4AuthorizationRevokeQueueSuccessTransition(before, after); err != nil {
		t.Fatalf("authorization revoke queue success transition: %v", err)
	}
}

func validateW4AuthorizationRevokeQueueSuccessTransition(before, after queue.QueueInfo) error {
	if before.Size != 0 || before.Pending != 0 || before.Active != 0 || before.Retry != 0 || before.Archived != 0 || before.Completed != 0 {
		return fmt.Errorf("baseline counters are not zero: %+v", before)
	}
	if after.Pending != 0 || after.Active != 0 || after.Retry != 0 || after.Archived != 0 {
		return fmt.Errorf("non-terminal counters are not zero: %+v", after)
	}
	if after.Size != before.Size+1 {
		return fmt.Errorf("size = %d, want %d", after.Size, before.Size+1)
	}
	if after.Completed != before.Completed+1 {
		return fmt.Errorf("completed = %d, want %d", after.Completed, before.Completed+1)
	}
	return nil
}

func TestValidateW4AuthorizationRevokeQueueSuccessTransition(t *testing.T) {
	baseline := queue.QueueInfo{Queue: operationlogjob.QueueName}
	success := queue.QueueInfo{Queue: operationlogjob.QueueName, Size: 1, Completed: 1}
	tests := []struct {
		name    string
		after   queue.QueueInfo
		wantErr bool
	}{
		{name: "valid transition", after: success},
		{name: "size did not increment", after: queue.QueueInfo{Queue: operationlogjob.QueueName, Completed: 1}, wantErr: true},
		{name: "completed did not increment", after: queue.QueueInfo{Queue: operationlogjob.QueueName, Size: 1}, wantErr: true},
		{name: "pending remains", after: queue.QueueInfo{Queue: operationlogjob.QueueName, Size: 1, Pending: 1, Completed: 1}, wantErr: true},
		{name: "archived remains", after: queue.QueueInfo{Queue: operationlogjob.QueueName, Size: 1, Archived: 1, Completed: 1}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateW4AuthorizationRevokeQueueSuccessTransition(baseline, tc.after)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateW4AuthorizationRevokeQueueSuccessTransition() error = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
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

func readW4AuthorizationRevokeSessionSnapshot(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var raw string
	if err := db.QueryRowContext(ctx, `
SELECT row_to_json(s)::text
FROM juhe_business.system_sessions s
WHERE s.id = $1
`, w4AuthorizationRevokeSessionID).Scan(&raw); err != nil {
		t.Fatalf("read authorization revoke session snapshot: %v", err)
	}
	return raw
}

func assertW4AuthorizationRevokePreRequestState(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, row := range []struct {
		name  string
		table string
		id    string
	}{
		{"grant", "resource_authorization_grants", w4AuthorizationRevokeGrantID},
		{"runtime", "resource_authorizations", w4AuthorizationRevokeRuntimeID},
		{"source", "resource_authorization_sources", w4AuthorizationRevokeSourceID},
	} {
		var status string
		query := fmt.Sprintf("SELECT status FROM juhe_business.%s WHERE id = $1", row.table)
		if err := db.QueryRowContext(ctx, query, row.id).Scan(&status); err != nil {
			t.Fatalf("read authorization revoke pre-request %s: %v", row.name, err)
		}
		if status != "active" {
			t.Fatalf("authorization revoke pre-request %s status = %q, want active", row.name, status)
		}
	}
	if counts := readW4AuthorizationRevokeCounts(t, ctx, db); counts != (w4AuthorizationRevokeCounts{Grants: 1, Runtime: 1, Sources: 1}) {
		t.Fatalf("authorization revoke pre-request counts = %+v, want one active business row and no side effects", counts)
	}
}

func readW4AuthorizationRevokeOperationLogSnapshot(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var raw string
	if err := db.QueryRowContext(ctx, `
SELECT jsonb_build_object(
  'operationLogs', (SELECT COALESCE(jsonb_agg(to_jsonb(l) ORDER BY l.id), '[]'::jsonb) FROM juhe_dataset.operation_logs l),
  'targets', (SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.id), '[]'::jsonb) FROM juhe_dataset.operation_log_targets x),
  'viewers', (SELECT COALESCE(jsonb_agg(to_jsonb(v) ORDER BY v.operation_log_id, v.system_account_id, v.visibility_reason), '[]'::jsonb) FROM juhe_dataset.operation_log_viewers v),
  'terms', (SELECT COALESCE(jsonb_agg(to_jsonb(s) ORDER BY s.operation_log_id, s.term), '[]'::jsonb) FROM juhe_dataset.operation_log_summary_search_terms s)
)::text
`).Scan(&raw); err != nil {
		t.Fatalf("read authorization revoke operation-log snapshot: %v", err)
	}
	return raw
}

func assertW4AuthorizationRevokeExactStateKeys(t *testing.T, entries []w4AuthorizationRevokeRedisEntry) {
	t.Helper()
	wantKeys := make([]string, 0, 2)
	for _, topic := range []string{gatewaycache.GatewayRuntimeCacheTopic, gatewaycache.AuthorizationQuotaCacheTopic} {
		key, err := gatewaycache.RuntimeStateKey(w4AuthorizationRevokeNamespace, gatewaycache.RuntimeInvalidationStoreName, "topic:"+topic)
		if err != nil {
			t.Fatalf("build authorization revoke state Redis key: %v", err)
		}
		wantKeys = append(wantKeys, key)
	}
	sort.Strings(wantKeys)
	gotKeys := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type != "string" {
			t.Fatalf("authorization revoke state Redis key %q type = %q, want string", entry.Key, entry.Type)
		}
		gotKeys = append(gotKeys, entry.Key)
	}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("authorization revoke state Redis keys = %v, want %v", gotKeys, wantKeys)
	}
}

func assertW4AuthorizationRevokeHTTPResponseSecretFree(t *testing.T, label string, response w4AuthorizationRevokeHTTPResponse, values ...string) {
	t.Helper()
	assertW4AuthorizationRevokeSecretFree(t, label+" HTTP body", response.Body, values...)
	for name, headers := range response.Header {
		assertW4AuthorizationRevokeSecretFree(t, label+" HTTP header "+name, strings.Join(headers, "\n"), values...)
	}
}

func assertW4AuthorizationRevokeRedisDBSecretFree(t *testing.T, label string, entries []w4AuthorizationRevokeRedisEntry, values ...string) {
	t.Helper()
	for _, entry := range entries {
		assertW4AuthorizationRevokeSecretFree(t, label+" key "+entry.Key, entry.Key+"\n"+entry.Value, values...)
	}
}

// The queue Redis DB contains each Asynq task state plus its serialized payload.
func assertW4AuthorizationRevokeAsynqPayloadsSecretFree(t *testing.T, entries []w4AuthorizationRevokeRedisEntry, values ...string) {
	t.Helper()
	assertW4AuthorizationRevokeRedisDBSecretFree(t, "Asynq task payloads across all queue states", entries, values...)
}

func assertW4AuthorizationRevokeSecretFree(t *testing.T, label, raw string, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(raw, value) {
			t.Fatalf("authorization revoke sensitive value leaked in %s", label)
		}
	}
}

type w4AuthorizationRevokeLockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *w4AuthorizationRevokeLockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *w4AuthorizationRevokeLockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
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
