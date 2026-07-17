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
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/gatewaycache"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementauthorizations"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w4AuthorizationUpdateNamespace        = "w4-management-authorization-update"
	w4AuthorizationUpdateAdminID          = "sys_w4_authorization_update_admin"
	w4AuthorizationUpdateOwnerID          = "sys_w4_authorization_update_owner"
	w4AuthorizationUpdateGranteeID        = "sys_w4_authorization_update_grantee"
	w4AuthorizationUpdateSessionID        = "sess_w4_authorization_update_admin"
	w4AuthorizationUpdateToken            = "w4-authorization-update-admin-session-token"
	w4AuthorizationUpdateGranteeSessionID = "sess_w4_authorization_update_grantee"
	w4AuthorizationUpdateGranteeToken     = "w4-authorization-update-grantee-session-token"
	w4AuthorizationUpdateCanary           = "w4-authorization-update-sensitive-canary"
	w4AuthorizationUpdateGrantID          = "rauthgrant_w4_authorization_update"
	w4AuthorizationUpdateRuntimeID        = "rauth_w4_authorization_update"
	w4AuthorizationUpdateSourceID         = "rauthsrc_w4_authorization_update"
	w4AuthorizationUpdateGroupID          = "grp_w4_authorization_update_owner"
	w4AuthorizationUpdateLogID            = "oplog_w4_authorization_update"
)

func TestW4ManagementAuthorizationUpdatePostgresRedisAsynqSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var postgresContainer *tcpostgres.PostgresContainer
	var redisContainer *tcredis.RedisContainer
	var db *sql.DB
	var store *postgresstore.Store
	var stateRedis *redisplatform.Client
	var stateKeyspace *goredis.Client
	var queueRedis *goredis.Client
	var logClient *queue.Client
	var inspector *queue.Inspector
	var server *httptest.Server
	var stopWorker context.CancelFunc
	var workerDone chan struct{}
	var workerMu sync.Mutex
	var workerErr error
	t.Cleanup(func() {
		if stopWorker != nil {
			stopWorker()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
			select {
			case <-workerDone:
			case <-shutdownCtx.Done():
				t.Errorf("authorization update worker shutdown: %v", shutdownCtx.Err())
			}
			shutdownCancel()
			workerMu.Lock()
			err := workerErr
			workerMu.Unlock()
			if err != nil {
				t.Errorf("authorization update worker run: %v", err)
			}
		}
		if server != nil {
			server.Close()
		}
		if inspector != nil {
			_ = inspector.Close()
		}
		if logClient != nil {
			_ = logClient.Close()
		}
		if stateRedis != nil {
			_ = stateRedis.Close()
		}
		if stateKeyspace != nil {
			_ = stateKeyspace.Close()
		}
		if queueRedis != nil {
			_ = queueRedis.Close()
		}
		if store != nil {
			store.Close()
		}
		if db != nil {
			_ = db.Close()
		}
		if redisContainer != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
			if err := redisContainer.Terminate(cleanupCtx); err != nil {
				t.Errorf("terminate authorization update redis: %v", err)
			}
			cleanupCancel()
		}
		if postgresContainer != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
			if err := postgresContainer.Terminate(cleanupCtx); err != nil {
				t.Errorf("terminate authorization update postgres: %v", err)
			}
			cleanupCancel()
		}
	})

	var err error
	postgresContainer, err = tcpostgres.Run(ctx, postgresImage, tcpostgres.WithDatabase("juhe_ai"), tcpostgres.WithUsername("juhe_ai"), tcpostgres.WithPassword("juhe_ai_password"), tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("start authorization update postgres: %v", err)
	}
	postgresURL, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("authorization update postgres connection string: %v", err)
	}
	db = openSQLDB(t, postgresURL)
	runGooseMigrations(t, db)
	version, err := goose.GetDBVersion(db)
	if err != nil {
		t.Fatalf("read authorization update Goose version: %v", err)
	}
	if want := latestW4AuthorizationRevokeMigrationVersion(t); version != want {
		t.Fatalf("authorization update Goose version = %d, want latest catalog version %d", version, want)
	}

	redisContainer, err = tcredis.Run(ctx, redisImage)
	if err != nil {
		t.Fatalf("start authorization update redis: %v", err)
	}
	redisURL, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("authorization update redis connection string: %v", err)
	}
	queueURL := w3RedisURLWithDB(t, redisURL, 0)
	stateURL := w3RedisURLWithDB(t, redisURL, 1)
	queueOpts, err := queue.ParseRedisURL(queueURL)
	if err != nil {
		t.Fatalf("parse authorization update queue redis URL: %v", err)
	}
	stateRedis, err = redisplatform.NewClient(stateURL, w4AuthorizationUpdateNamespace+":state")
	if err != nil {
		t.Fatalf("open authorization update state redis: %v", err)
	}
	stateOpts, err := goredis.ParseURL(stateURL)
	if err != nil {
		t.Fatalf("parse authorization update state redis URL: %v", err)
	}
	stateKeyspace = goredis.NewClient(stateOpts)
	queueScanOpts, err := goredis.ParseURL(queueURL)
	if err != nil {
		t.Fatalf("parse authorization update queue keyspace URL: %v", err)
	}
	queueRedis = goredis.NewClient(queueScanOpts)

	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	insertW4AuthorizationUpdateFixture(t, ctx, db, now)
	insertW2ManagementSessionForAccountFixture(t, ctx, db, w4AuthorizationUpdateSessionID, w4AuthorizationUpdateAdminID, w4AuthorizationUpdateToken, now.Add(-time.Minute))
	insertW2ManagementSessionForAccountFixture(t, ctx, db, w4AuthorizationUpdateGranteeSessionID, w4AuthorizationUpdateGranteeID, w4AuthorizationUpdateGranteeToken, now.Add(-time.Minute))
	store, err = postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open authorization update postgres store: %v", err)
	}
	var invalidationCalls int
	invalidator, err := gatewaycache.NewSystemAccountInvalidator(gatewaycache.SystemAccountInvalidatorOptions{
		State: stateRedis, Namespace: w4AuthorizationUpdateNamespace, Now: func() time.Time { return now },
		NewVersion: func(time.Time) (string, error) {
			invalidationCalls++
			return fmt.Sprintf("w4-authorization-update-version-%d", invalidationCalls), nil
		},
	})
	if err != nil {
		t.Fatalf("create authorization update invalidator: %v", err)
	}
	var logs w4AuthorizationRevokeLockedBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	workerCtx, workerCancel := context.WithCancel(ctx)
	stopWorker = workerCancel
	workerDone = make(chan struct{})
	go func() {
		err := app.RunIngestWorker(workerCtx, config.Config{PostgresURL: postgresURL, RedisQueueURL: queueURL, RedisNamespace: "juhe-ai", LogLevel: "error", ShutdownTimeout: time.Second}, logger)
		workerMu.Lock()
		workerErr = err
		workerMu.Unlock()
		close(workerDone)
	}()
	logClient = queue.NewClient(queueOpts)
	inspector = queue.NewInspector(queueOpts)
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{Store: store, Now: func() time.Time { return now }})
	service := managementauthorizations.NewServiceWithOptions(managementauthorizations.ServiceOptions{Store: store, Now: func() time.Time { return now }, Secret: "w4-authorization-update-test-secret", AuthorizationInvalidator: invalidator})
	logIDCalls := 0
	cfg := config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true, TrustProxy: "false"}
	server = httptest.NewServer(httpapi.NewRouter(httpapi.RouterOptions{
		Config: cfg, Logger: logger, ManagementAPIAuthMiddleware: httpapi.NewManagementAPIAuthMiddleware(authenticator), ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementAuthorizationUpdateHandler: httpapi.NewManagementAuthorizationUpdateHandlerWithOperationLog(service, httpapi.ManagementOperationLogOptions{Config: cfg, Logger: logger, Client: logClient, SettingsReader: store, Now: func() time.Time { return now }, NewLogID: func() string { logIDCalls++; return w4AuthorizationUpdateLogID }}),
	}))

	assertW4AuthorizationUpdateBaseline(t, ctx, db)
	if state := readW4AuthorizationRevokeRedisDB(t, ctx, stateKeyspace); len(state) != 0 {
		t.Fatal("authorization update state Redis before request is not empty")
	}
	queueBefore := readW4AuthorizationRevokeQueueInfo(t, inspector, true)
	assertW4AuthorizationRevokeSecretFree(t, "authorization update logger before request", logs.String(), w4AuthorizationUpdateToken, w4AuthorizationUpdateCanary)
	response := doW4AuthorizationUpdateRequest(t, ctx, server.URL, w4AuthorizationUpdateAdminID, `{"status":"paused","limits":{"daily":{"enabled":true,"limit":17}}}`, "req_w4_authorization_update")
	assertW4AuthorizationRevokeHTTPResponseSecretFree(t, "update success", w4AuthorizationRevokeHTTPResponse{StatusCode: response.StatusCode, Body: response.Body, Header: response.Header}, w4AuthorizationUpdateToken, w4AuthorizationUpdateCanary)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authorization update status = %d, body=%s", response.StatusCode, response.Body)
	}
	var envelope struct {
		Data managementauthorizations.Summary `json:"data"`
	}
	if err := json.Unmarshal([]byte(response.Body), &envelope); err != nil {
		t.Fatalf("decode authorization update response: %v", err)
	}
	if envelope.Data.ID != w4AuthorizationUpdateGrantID || envelope.Data.Status != "paused" || !envelope.Data.UpdatedAt.UTC().Equal(now) {
		t.Fatalf("authorization update response = %+v", envelope.Data)
	}
	assertW4AuthorizationUpdateBusinessRows(t, ctx, db, now)
	assertW4AuthorizationUpdateInvalidations(t, ctx, stateRedis, now)
	if invalidationCalls != 2 {
		t.Fatalf("authorization update invalidations = %d, want 2", invalidationCalls)
	}
	stateBeforeFailure := readW4AuthorizationRevokeRedisDB(t, ctx, stateKeyspace)
	assertW4AuthorizationRevokeRedisDBSecretFree(t, "authorization update state Redis", stateBeforeFailure, w4AuthorizationUpdateToken, w4AuthorizationUpdateCanary)
	assertW4AuthorizationRevokeExactStateKeysForNamespace(t, w4AuthorizationUpdateNamespace, stateBeforeFailure)
	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, func() error { workerMu.Lock(); defer workerMu.Unlock(); return workerErr }); err != nil {
		t.Fatal(err)
	}
	queueAfter := readW4AuthorizationRevokeQueueInfo(t, inspector, false)
	assertW4AuthorizationRevokeQueueSuccessTransition(t, queueBefore, queueAfter)
	operationsBeforeFailure := readW4AuthorizationRevokeOperationLogSnapshot(t, ctx, db)
	queueAfterSuccess := readW4AuthorizationRevokeRedisDB(t, ctx, queueRedis)
	assertW4AuthorizationRevokeRedisDBSecretFree(t, "authorization update Asynq Redis", queueAfterSuccess, w4AuthorizationUpdateToken, w4AuthorizationUpdateCanary)
	queueStableBeforeFailure := stableW4AuthorizationRevokeQueueRedisSnapshot(queueAfterSuccess)
	assertW4AuthorizationRevokeAsynqTaskSnapshotPresent(t, queueStableBeforeFailure)
	assertW4AuthorizationUpdateOperationLog(t, ctx, db, now)
	businessBeforeFailure := readW4AuthorizationUpdateBusinessSnapshot(t, ctx, db)

	// Terminal rows and a non-owner cannot mutate the committed update or emit another invalidation/log task.
	if _, err := db.ExecContext(ctx, `UPDATE juhe_business.resource_authorization_grants SET status = 'revoked' WHERE id = $1`, w4AuthorizationUpdateGrantID); err != nil {
		t.Fatalf("make update fixture terminal: %v", err)
	}
	businessTerminal := readW4AuthorizationUpdateBusinessSnapshot(t, ctx, db)
	terminal := doW4AuthorizationUpdateRequest(t, ctx, server.URL, w4AuthorizationUpdateAdminID, `{"status":"active"}`, "req_w4_authorization_update_terminal")
	assertW4AuthorizationRevokeHTTPResponseSecretFree(t, "terminal update", w4AuthorizationRevokeHTTPResponse{StatusCode: terminal.StatusCode, Body: terminal.Body, Header: terminal.Header}, w4AuthorizationUpdateToken, w4AuthorizationUpdateCanary)
	if terminal.StatusCode != http.StatusNotFound {
		t.Fatalf("terminal authorization update status = %d, body=%s", terminal.StatusCode, terminal.Body)
	}
	assertW4AuthorizationUpdateNoSideEffects(t, ctx, db, stateKeyspace, queueRedis, inspector, businessTerminal, operationsBeforeFailure, stateBeforeFailure, queueStableBeforeFailure, queueAfter, invalidationCalls, logIDCalls)
	denied := doW4AuthorizationUpdateRequest(t, ctx, server.URL, w4AuthorizationUpdateGranteeID, `{"status":"active"}`, "req_w4_authorization_update_denied")
	assertW4AuthorizationRevokeHTTPResponseSecretFree(t, "denied update", w4AuthorizationRevokeHTTPResponse{StatusCode: denied.StatusCode, Body: denied.Body, Header: denied.Header}, w4AuthorizationUpdateToken, w4AuthorizationUpdateCanary)
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin authorization update status = %d, body=%s", denied.StatusCode, denied.Body)
	}
	assertW4AuthorizationUpdateNoSideEffects(t, ctx, db, stateKeyspace, queueRedis, inspector, businessTerminal, operationsBeforeFailure, stateBeforeFailure, queueStableBeforeFailure, queueAfter, invalidationCalls, logIDCalls)
	assertW4AuthorizationRevokeSecretFree(t, "authorization update PostgreSQL and logger", businessBeforeFailure+businessTerminal+operationsBeforeFailure+readW4AuthorizationUpdateSessionSnapshot(t, ctx, db)+logs.String(), w4AuthorizationUpdateToken, w4AuthorizationUpdateCanary)
}

type w4AuthorizationUpdateResponse struct {
	StatusCode int
	Body       string
	Header     http.Header
}

func doW4AuthorizationUpdateRequest(t *testing.T, ctx context.Context, serverURL, actorID, body, requestID string) w4AuthorizationUpdateResponse {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, serverURL+"/__aisys__/api/authorizations/"+w4AuthorizationUpdateGrantID+"?systemAccountId="+w4AuthorizationUpdateOwnerID, strings.NewReader(body))
	if err != nil {
		t.Fatalf("create authorization update request: %v", err)
	}
	// Only the seeded administrator session is valid. A forged non-admin context must fail before service side effects.
	token := w4AuthorizationUpdateToken
	if actorID != w4AuthorizationUpdateAdminID {
		token = w4AuthorizationUpdateGranteeToken
	}
	req.AddCookie(&http.Cookie{Name: managementauth.SessionCookieName, Value: token})
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", requestID)
	req.Header.Set("X-Test-Canary", w4AuthorizationUpdateCanary)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("execute authorization update request: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read authorization update response: %v", err)
	}
	return w4AuthorizationUpdateResponse{StatusCode: resp.StatusCode, Body: string(raw), Header: resp.Header.Clone()}
}

func insertW4AuthorizationUpdateFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	created := now.Add(-time.Hour)
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.system_accounts (id, username, display_name, role, status, password_hash, must_change_password, image_generation_enabled, created_at, updated_at) VALUES ($1, 'w4-update-admin', 'W4 Update Admin', 'admin', 'active', 'hash', false, false, $4, $4), ($2, 'w4-update-owner', 'W4 Update Owner', 'user', 'active', 'hash', false, false, $4, $4), ($3, 'w4-update-grantee', 'W4 Update Grantee', 'user', 'active', 'hash', false, false, $4, $4)`, w4AuthorizationUpdateAdminID, w4AuthorizationUpdateOwnerID, w4AuthorizationUpdateGranteeID, created); err != nil {
		t.Fatalf("insert authorization update accounts: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at) VALUES ($1, $2, 'W4 Update Owner Group', 'openai', true, false, 'personal', $3, $3)`, w4AuthorizationUpdateGroupID, w4AuthorizationUpdateOwnerID, created); err != nil {
		t.Fatalf("insert authorization update group: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.resource_authorization_grants (id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_system_account_id, scope, status, remark, created_by, created_at, updated_at) VALUES ($1, 'group', $2, $3, 'system_account', $4, 'use', 'active', 'update fixture', $3, $5, $5)`, w4AuthorizationUpdateGrantID, w4AuthorizationUpdateGroupID, w4AuthorizationUpdateOwnerID, w4AuthorizationUpdateGranteeID, created); err != nil {
		t.Fatalf("insert authorization update grant: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status, effective_source_type, activated_at, last_source_changed_at, remark, created_by, created_at, updated_at) VALUES ($1, 'group', $2, $3, $4, 'use', 'active', 'manual', $5, $5, 'update fixture', $3, $5, $5)`, w4AuthorizationUpdateRuntimeID, w4AuthorizationUpdateGroupID, w4AuthorizationUpdateOwnerID, w4AuthorizationUpdateGranteeID, created); err != nil {
		t.Fatalf("insert authorization update runtime: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.resource_authorization_sources (id, authorization_id, source_type, status, activated_at, created_by, created_at, updated_at) VALUES ($1, $2, 'manual', 'active', $3, $4, $3, $3)`, w4AuthorizationUpdateSourceID, w4AuthorizationUpdateRuntimeID, created, w4AuthorizationUpdateOwnerID); err != nil {
		t.Fatalf("insert authorization update source: %v", err)
	}
}

func assertW4AuthorizationUpdateBaseline(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.resource_authorization_grants WHERE id=$1 AND status='active'`, w4AuthorizationUpdateGrantID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("authorization update active grant baseline: count=%d err=%v", count, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.group_account_stats_dirty WHERE group_id='__all__'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("authorization update dirty baseline: count=%d err=%v", count, err)
	}
}

func assertW4AuthorizationUpdateBusinessRows(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	for _, row := range []struct {
		table, id, wantStatus string
		wantUpdated           bool
	}{{"resource_authorization_grants", w4AuthorizationUpdateGrantID, "paused", true}, {"resource_authorizations", w4AuthorizationUpdateRuntimeID, "paused", true}, {"resource_authorization_sources", w4AuthorizationUpdateSourceID, "active", false}} {
		var status string
		var updated time.Time
		if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT status, updated_at FROM juhe_business.%s WHERE id=$1", row.table), row.id).Scan(&status, &updated); err != nil {
			t.Fatalf("read updated %s: %v", row.table, err)
		}
		if status != row.wantStatus || (row.wantUpdated && !updated.UTC().Equal(now)) {
			t.Fatalf("updated %s = status=%q updated=%s", row.table, status, updated)
		}
	}
	var reason string
	if err := db.QueryRowContext(ctx, `SELECT reason FROM juhe_business.group_account_stats_dirty WHERE group_id='__all__'`).Scan(&reason); err != nil || reason != managementauthorizations.ResourceAuthorizationUpdatedReason {
		t.Fatalf("authorization update dirty reason=%q err=%v", reason, err)
	}
}

func assertW4AuthorizationUpdateInvalidations(t *testing.T, ctx context.Context, client *redisplatform.Client, now time.Time) {
	t.Helper()
	for index, topic := range []string{gatewaycache.GatewayRuntimeCacheTopic, gatewaycache.AuthorizationQuotaCacheTopic} {
		key, err := gatewaycache.RuntimeStateKey(w4AuthorizationUpdateNamespace, gatewaycache.RuntimeInvalidationStoreName, "topic:"+topic)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := client.GetRaw(ctx, key)
		if err != nil {
			t.Fatalf("read authorization update invalidation: %v", err)
		}
		var state struct{ Version, Reason, PublishedAt string }
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		if state.Version != fmt.Sprintf("w4-authorization-update-version-%d", index+1) || state.Reason != managementauthorizations.ResourceAuthorizationUpdatedReason || state.PublishedAt != now.Format("2006-01-02T15:04:05.000Z") {
			t.Fatalf("authorization update invalidation = %+v", state)
		}
	}
}

func assertW4AuthorizationRevokeExactStateKeysForNamespace(t *testing.T, namespace string, entries []w4AuthorizationRevokeRedisEntry) {
	t.Helper()
	want := []string{}
	for _, topic := range []string{gatewaycache.GatewayRuntimeCacheTopic, gatewaycache.AuthorizationQuotaCacheTopic} {
		key, err := gatewaycache.RuntimeStateKey(namespace, gatewaycache.RuntimeInvalidationStoreName, "topic:"+topic)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, key)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type != "string" {
			t.Fatalf("unexpected state Redis type %q", entry.Type)
		}
		got = append(got, entry.Key)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("authorization update state keys = %v, want %v", got, want)
	}
}

func readW4AuthorizationUpdateBusinessSnapshot(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT jsonb_build_object('grant',(SELECT to_jsonb(x) FROM juhe_business.resource_authorization_grants x WHERE id=$1),'runtime',(SELECT to_jsonb(x) FROM juhe_business.resource_authorizations x WHERE id=$2),'source',(SELECT to_jsonb(x) FROM juhe_business.resource_authorization_sources x WHERE id=$3),'dirty',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY group_id),'[]'::jsonb) FROM juhe_business.group_account_stats_dirty x))::text`, w4AuthorizationUpdateGrantID, w4AuthorizationUpdateRuntimeID, w4AuthorizationUpdateSourceID).Scan(&raw); err != nil {
		t.Fatalf("read authorization update business snapshot: %v", err)
	}
	return raw
}

func readW4AuthorizationUpdateSessionSnapshot(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT row_to_json(s)::text FROM juhe_business.system_sessions s WHERE id = $1`, w4AuthorizationUpdateSessionID).Scan(&raw); err != nil {
		t.Fatalf("read authorization update session snapshot: %v", err)
	}
	return raw
}

func assertW4AuthorizationUpdateOperationLog(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	var key, action, method, path string
	var status int
	if err := db.QueryRowContext(ctx, `SELECT operation_key, action, method, path, status_code FROM juhe_dataset.operation_logs WHERE id=$1`, w4AuthorizationUpdateLogID).Scan(&key, &action, &method, &path, &status); err != nil {
		t.Fatalf("read authorization update operation log: %v", err)
	}
	if key != "authorizations.update" || action != "update" || method != http.MethodPatch || path != "/__aisys__/api/authorizations/"+w4AuthorizationUpdateGrantID || status != http.StatusOK {
		t.Fatalf("authorization update operation log = key=%q action=%q method=%q path=%q status=%d", key, action, method, path, status)
	}
	var targets, viewers, terms int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM juhe_dataset.operation_log_targets WHERE operation_log_id=$1), (SELECT count(*) FROM juhe_dataset.operation_log_viewers WHERE operation_log_id=$1), (SELECT count(*) FROM juhe_dataset.operation_log_summary_search_terms WHERE operation_log_id=$1)`, w4AuthorizationUpdateLogID).Scan(&targets, &viewers, &terms); err != nil {
		t.Fatal(err)
	}
	if targets != 3 || viewers != 3 || terms == 0 {
		t.Fatalf("authorization update operation log relations = targets=%d viewers=%d terms=%d", targets, viewers, terms)
	}
	_ = now
}

func assertW4AuthorizationUpdateNoSideEffects(t *testing.T, ctx context.Context, db *sql.DB, state *goredis.Client, queueDB *goredis.Client, inspector *queue.Inspector, wantBusiness, wantOperations string, wantState, wantQueue []w4AuthorizationRevokeRedisEntry, wantQueueInfo queue.QueueInfo, invalidations, logIDs int) {
	t.Helper()
	if got := readW4AuthorizationUpdateBusinessSnapshot(t, ctx, db); got != wantBusiness {
		t.Fatal("authorization update business changed after failed request")
	}
	if got := readW4AuthorizationRevokeOperationLogSnapshot(t, ctx, db); got != wantOperations {
		t.Fatal("authorization update operation logs changed after failed request")
	}
	if got := readW4AuthorizationRevokeRedisDB(t, ctx, state); !reflect.DeepEqual(got, wantState) {
		t.Fatal("authorization update state Redis changed after failed request")
	}
	if got := stableW4AuthorizationRevokeQueueRedisSnapshot(readW4AuthorizationRevokeRedisDB(t, ctx, queueDB)); !reflect.DeepEqual(got, wantQueue) {
		t.Fatal("authorization update queue snapshot changed after failed request")
	}
	if got := readW4AuthorizationRevokeQueueInfo(t, inspector, false); got != wantQueueInfo {
		t.Fatal("authorization update queue counters changed after failed request")
	}
	if invalidations != 2 || logIDs != 1 {
		t.Fatalf("authorization update failed side effects = invalidations=%d logIDs=%d", invalidations, logIDs)
	}
}
