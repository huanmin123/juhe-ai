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
	w4AuthorizationUpdateRepeatLogID      = "oplog_w4_authorization_update_repeat"
)

type w4AuthorizationUpdateClock struct {
	mu  sync.RWMutex
	now time.Time
}

func newW4AuthorizationUpdateClock(now time.Time) *w4AuthorizationUpdateClock {
	return &w4AuthorizationUpdateClock{now: now.UTC()}
}

func (c *w4AuthorizationUpdateClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *w4AuthorizationUpdateClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now.UTC()
	c.mu.Unlock()
}

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

	firstNow := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	secondNow := firstNow.Add(2 * time.Minute)
	clock := newW4AuthorizationUpdateClock(firstNow)
	insertW4AuthorizationUpdateFixture(t, ctx, db, firstNow)
	insertW2ManagementSessionForAccountFixture(t, ctx, db, w4AuthorizationUpdateSessionID, w4AuthorizationUpdateAdminID, w4AuthorizationUpdateToken, firstNow.Add(-time.Minute))
	insertW2ManagementSessionForAccountFixture(t, ctx, db, w4AuthorizationUpdateGranteeSessionID, w4AuthorizationUpdateGranteeID, w4AuthorizationUpdateGranteeToken, firstNow.Add(-time.Minute))
	store, err = postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open authorization update postgres store: %v", err)
	}
	var invalidationCalls int
	invalidator, err := gatewaycache.NewSystemAccountInvalidator(gatewaycache.SystemAccountInvalidatorOptions{
		State: stateRedis, Namespace: w4AuthorizationUpdateNamespace, Now: clock.Now,
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
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{Store: store, Now: clock.Now})
	service := managementauthorizations.NewServiceWithOptions(managementauthorizations.ServiceOptions{Store: store, Now: clock.Now, Secret: "w4-authorization-update-test-secret", AuthorizationInvalidator: invalidator})
	logIDCalls := 0
	cfg := config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true, TrustProxy: "false"}
	server = httptest.NewServer(httpapi.NewRouter(httpapi.RouterOptions{
		Config: cfg, Logger: logger, ManagementAPIAuthMiddleware: httpapi.NewManagementAPIAuthMiddleware(authenticator), ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementAuthorizationUpdateHandler: httpapi.NewManagementAuthorizationUpdateHandlerWithOperationLog(service, httpapi.ManagementOperationLogOptions{Config: cfg, Logger: logger, Client: logClient, SettingsReader: store, Now: clock.Now, NewLogID: func() string {
			logIDCalls++
			if logIDCalls == 1 {
				return w4AuthorizationUpdateLogID
			}
			return w4AuthorizationUpdateRepeatLogID
		}}),
	}))

	assertW4AuthorizationUpdateBaseline(t, ctx, db)
	baselineSessions := readW4AuthorizationUpdateSessionSnapshot(t, ctx, db)
	assertW4AuthorizationUpdateSecretFree(t, "PostgreSQL sessions before request", baselineSessions)
	stateBaseline := readW4AuthorizationRevokeRedisDB(t, ctx, stateKeyspace)
	assertW4AuthorizationUpdateRedisSecretFree(t, "state Redis before request", stateBaseline)
	if len(stateBaseline) != 0 {
		t.Fatal("authorization update state Redis before request is not empty")
	}
	queueBaselineRaw := readW4AuthorizationRevokeRedisDB(t, ctx, queueRedis)
	assertW4AuthorizationUpdateRedisSecretFree(t, "queue Redis before request", queueBaselineRaw)
	if stable := stableW4AuthorizationRevokeQueueRedisSnapshot(queueBaselineRaw); len(stable) != 0 {
		t.Fatalf("authorization update stable Asynq baseline count = %d, want 0", len(stable))
	}
	queueBefore := readW4AuthorizationRevokeQueueInfo(t, inspector, true)
	assertW4AuthorizationUpdateSecretFree(t, "logger before request", logs.String())
	sourceBaseline := readW4AuthorizationUpdateSourceSnapshot(t, ctx, db)
	assertW4AuthorizationUpdateSecretFree(t, "PostgreSQL source before request", sourceBaseline)
	payload := `{"status":"paused","limits":{"daily":{"enabled":true,"limit":17}}}`
	response := doW4AuthorizationUpdateRequest(t, ctx, server.URL, w4AuthorizationUpdateAdminID, payload, "req_w4_authorization_update")
	assertW4AuthorizationUpdateHTTPSecretFree(t, "update success", response)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authorization update status = %d, body=%s", response.StatusCode, response.Body)
	}
	var envelope struct {
		Data managementauthorizations.Summary `json:"data"`
	}
	if err := json.Unmarshal([]byte(response.Body), &envelope); err != nil {
		t.Fatalf("decode authorization update response: %v", err)
	}
	if envelope.Data.ID != w4AuthorizationUpdateGrantID || envelope.Data.Status != "paused" || !envelope.Data.UpdatedAt.UTC().Equal(firstNow) || envelope.Data.Limits.Daily == nil || !envelope.Data.Limits.Daily.Enabled || envelope.Data.Limits.Daily.Limit != 17 {
		t.Fatalf("authorization update response = %+v", envelope.Data)
	}
	assertW4AuthorizationUpdateBusinessRows(t, ctx, db, firstNow, sourceBaseline)
	assertW4AuthorizationUpdateInvalidations(t, ctx, stateRedis, firstNow, 1)
	if invalidationCalls != 2 {
		t.Fatalf("authorization update invalidations = %d, want 2", invalidationCalls)
	}
	stateBeforeRepeat := readW4AuthorizationRevokeRedisDB(t, ctx, stateKeyspace)
	assertW4AuthorizationUpdateRedisSecretFree(t, "state Redis after success", stateBeforeRepeat)
	assertW4AuthorizationRevokeExactStateKeysForNamespace(t, w4AuthorizationUpdateNamespace, stateBeforeRepeat)
	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, func() error { workerMu.Lock(); defer workerMu.Unlock(); return workerErr }); err != nil {
		t.Fatal(err)
	}
	queueAfterFirst := readW4AuthorizationRevokeQueueInfo(t, inspector, false)
	assertW4AuthorizationRevokeQueueSuccessTransition(t, queueBefore, queueAfterFirst)
	operationsBeforeRepeat := readW4AuthorizationRevokeOperationLogSnapshot(t, ctx, db)
	assertW4AuthorizationUpdateSecretFree(t, "PostgreSQL audit after success", operationsBeforeRepeat)
	queueAfterSuccess := readW4AuthorizationRevokeRedisDB(t, ctx, queueRedis)
	assertW4AuthorizationUpdateRedisSecretFree(t, "Asynq Redis after success", queueAfterSuccess)
	queueStableBeforeRepeat := stableW4AuthorizationRevokeQueueRedisSnapshot(queueAfterSuccess)
	assertW4AuthorizationRevokeAsynqTaskSnapshotPresent(t, queueStableBeforeRepeat)
	assertW4AuthorizationUpdateOperationLog(t, ctx, db, w4AuthorizationUpdateLogID, "req_w4_authorization_update", firstNow)
	auditCountsAfterFirst := readW4AuthorizationUpdateAuditCounts(t, ctx, db)
	if auditCountsAfterFirst.Logs != 1 || auditCountsAfterFirst.Targets != 3 || auditCountsAfterFirst.Viewers != 3 || auditCountsAfterFirst.Terms == 0 {
		t.Fatalf("authorization update first audit counts = %+v", auditCountsAfterFirst)
	}
	businessBeforeRepeat := readW4AuthorizationUpdateBusinessSnapshot(t, ctx, db)
	assertW4AuthorizationUpdateSecretFree(t, "PostgreSQL business after success", businessBeforeRepeat)

	// The production contract records every successful PATCH, even when the normalized values are unchanged.
	clock.Set(secondNow)
	repeat := doW4AuthorizationUpdateRequest(t, ctx, server.URL, w4AuthorizationUpdateAdminID, payload, "req_w4_authorization_update_repeat")
	assertW4AuthorizationUpdateHTTPSecretFree(t, "repeated update", repeat)
	if repeat.StatusCode != http.StatusOK {
		t.Fatalf("repeated authorization update status = %d", repeat.StatusCode)
	}
	var repeatedEnvelope struct {
		Data managementauthorizations.Summary `json:"data"`
	}
	if err := json.Unmarshal([]byte(repeat.Body), &repeatedEnvelope); err != nil {
		t.Fatalf("decode repeated authorization update response: %v", err)
	}
	if repeatedEnvelope.Data.ID != w4AuthorizationUpdateGrantID || repeatedEnvelope.Data.Status != "paused" || !repeatedEnvelope.Data.UpdatedAt.UTC().Equal(secondNow) || repeatedEnvelope.Data.Limits.Daily == nil || !repeatedEnvelope.Data.Limits.Daily.Enabled || repeatedEnvelope.Data.Limits.Daily.Limit != 17 {
		t.Fatalf("repeated authorization update response = %+v", repeatedEnvelope.Data)
	}
	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, func() error { workerMu.Lock(); defer workerMu.Unlock(); return workerErr }); err != nil {
		t.Fatal(err)
	}
	assertW4AuthorizationUpdateBusinessRows(t, ctx, db, secondNow, sourceBaseline)
	businessAfterRepeat := readW4AuthorizationUpdateBusinessSnapshot(t, ctx, db)
	assertW4AuthorizationUpdateSecretFree(t, "PostgreSQL business after repeat", businessAfterRepeat)
	if businessAfterRepeat == businessBeforeRepeat {
		t.Fatal("authorization update business timestamps did not advance after equal-value repeat")
	}
	assertW4AuthorizationUpdateInvalidations(t, ctx, stateRedis, secondNow, 3)
	stateBeforeFailure := readW4AuthorizationRevokeRedisDB(t, ctx, stateKeyspace)
	assertW4AuthorizationUpdateRedisSecretFree(t, "state Redis after repeat", stateBeforeFailure)
	queueAfterRepeat := readW4AuthorizationRevokeQueueInfo(t, inspector, false)
	assertW4AuthorizationUpdateQueueIncrement(t, queueAfterFirst, queueAfterRepeat)
	operationsBeforeFailure := readW4AuthorizationRevokeOperationLogSnapshot(t, ctx, db)
	assertW4AuthorizationUpdateSecretFree(t, "PostgreSQL audit after repeat", operationsBeforeFailure)
	queueAfterRepeatRaw := readW4AuthorizationRevokeRedisDB(t, ctx, queueRedis)
	assertW4AuthorizationUpdateRedisSecretFree(t, "Asynq Redis after repeat", queueAfterRepeatRaw)
	queueStableBeforeFailure := stableW4AuthorizationRevokeQueueRedisSnapshot(queueAfterRepeatRaw)
	if reflect.DeepEqual(queueStableBeforeFailure, queueStableBeforeRepeat) {
		t.Fatal("authorization update stable Asynq snapshot did not add the repeated operation task")
	}
	assertW4AuthorizationRevokeAsynqTaskSnapshotPresent(t, queueStableBeforeFailure)
	assertW4AuthorizationUpdateOperationLog(t, ctx, db, w4AuthorizationUpdateRepeatLogID, "req_w4_authorization_update_repeat", secondNow)
	auditCountsAfterRepeat := readW4AuthorizationUpdateAuditCounts(t, ctx, db)
	if auditCountsAfterRepeat.Logs != 2 || auditCountsAfterRepeat.Targets != 6 || auditCountsAfterRepeat.Viewers != 6 || auditCountsAfterRepeat.Terms != auditCountsAfterFirst.Terms*2 {
		t.Fatalf("authorization update repeated audit counts = %+v, first=%+v", auditCountsAfterRepeat, auditCountsAfterFirst)
	}
	if invalidationCalls != 4 || logIDCalls != 2 {
		t.Fatalf("authorization update repeat side effects = invalidations=%d logIDs=%d, want 4 and 2", invalidationCalls, logIDCalls)
	}

	// Terminal rows and a non-owner cannot mutate the committed update or emit another invalidation/log task.
	if _, err := db.ExecContext(ctx, `UPDATE juhe_business.resource_authorization_grants SET status = 'revoked' WHERE id = $1`, w4AuthorizationUpdateGrantID); err != nil {
		t.Fatalf("make update fixture terminal: %v", err)
	}
	businessTerminal := readW4AuthorizationUpdateBusinessSnapshot(t, ctx, db)
	assertW4AuthorizationUpdateSecretFree(t, "PostgreSQL terminal baseline", businessTerminal)
	terminal := doW4AuthorizationUpdateRequest(t, ctx, server.URL, w4AuthorizationUpdateAdminID, `{"status":"active"}`, "req_w4_authorization_update_terminal")
	assertW4AuthorizationUpdateHTTPSecretFree(t, "terminal update", terminal)
	if terminal.StatusCode != http.StatusNotFound {
		t.Fatalf("terminal authorization update status = %d, body=%s", terminal.StatusCode, terminal.Body)
	}
	assertW4AuthorizationUpdateNoSideEffects(t, ctx, db, stateKeyspace, queueRedis, inspector, businessTerminal, operationsBeforeFailure, stateBeforeFailure, queueStableBeforeFailure, queueAfterRepeat, invalidationCalls, logIDCalls)
	denied := doW4AuthorizationUpdateRequest(t, ctx, server.URL, w4AuthorizationUpdateGranteeID, `{"status":"active"}`, "req_w4_authorization_update_denied")
	assertW4AuthorizationUpdateHTTPSecretFree(t, "denied update", denied)
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin authorization update status = %d, body=%s", denied.StatusCode, denied.Body)
	}
	assertW4AuthorizationUpdateNoSideEffects(t, ctx, db, stateKeyspace, queueRedis, inspector, businessTerminal, operationsBeforeFailure, stateBeforeFailure, queueStableBeforeFailure, queueAfterRepeat, invalidationCalls, logIDCalls)
	assertW4AuthorizationUpdateSecretFree(t, "final PostgreSQL and logger", businessTerminal+operationsBeforeFailure+readW4AuthorizationUpdateSessionSnapshot(t, ctx, db)+logs.String())
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
	for _, row := range []struct{ table, id string }{{"resource_authorization_grants", w4AuthorizationUpdateGrantID}, {"resource_authorizations", w4AuthorizationUpdateRuntimeID}, {"resource_authorization_sources", w4AuthorizationUpdateSourceID}} {
		var count int
		if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM juhe_business.%s WHERE id=$1 AND status='active'`, row.table), row.id).Scan(&count); err != nil || count != 1 {
			t.Fatalf("authorization update active %s baseline: count=%d err=%v", row.table, count, err)
		}
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.group_account_stats_dirty WHERE group_id='__all__'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("authorization update dirty baseline: count=%d err=%v", count, err)
	}
	var logs, targets, viewers, terms int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM juhe_dataset.operation_logs), (SELECT count(*) FROM juhe_dataset.operation_log_targets), (SELECT count(*) FROM juhe_dataset.operation_log_viewers), (SELECT count(*) FROM juhe_dataset.operation_log_summary_search_terms)`).Scan(&logs, &targets, &viewers, &terms); err != nil {
		t.Fatalf("read authorization update audit baseline counts: %v", err)
	}
	if logs != 0 || targets != 0 || viewers != 0 || terms != 0 {
		t.Fatalf("authorization update audit baseline counts = logs=%d targets=%d viewers=%d terms=%d", logs, targets, viewers, terms)
	}
	var sessions, hashOnly int
	if err := db.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE token_hash <> '' AND token_hash <> $1 AND token_hash <> $2) FROM juhe_business.system_sessions WHERE id IN ($3,$4)`, w4AuthorizationUpdateToken, w4AuthorizationUpdateGranteeToken, w4AuthorizationUpdateSessionID, w4AuthorizationUpdateGranteeSessionID).Scan(&sessions, &hashOnly); err != nil {
		t.Fatalf("read authorization update session baseline: %v", err)
	}
	if sessions != 2 || hashOnly != 2 {
		t.Fatalf("authorization update session baseline = sessions=%d hashOnly=%d, want 2 and 2", sessions, hashOnly)
	}
}

func assertW4AuthorizationUpdateBusinessRows(t *testing.T, ctx context.Context, db *sql.DB, now time.Time, sourceBaseline string) {
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
	for _, row := range []struct{ table, id string }{{"resource_authorization_grants", w4AuthorizationUpdateGrantID}, {"resource_authorizations", w4AuthorizationUpdateRuntimeID}} {
		var matches bool
		if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT limits_json::jsonb = '{"daily":{"enabled":true,"limit":17}}'::jsonb FROM juhe_business.%s WHERE id=$1`, row.table), row.id).Scan(&matches); err != nil {
			t.Fatalf("read authorization update %s limits: %v", row.table, err)
		}
		if !matches {
			t.Fatalf("authorization update %s limits_json does not equal expected daily limit", row.table)
		}
	}
	if got := readW4AuthorizationUpdateSourceSnapshot(t, ctx, db); got != sourceBaseline {
		t.Fatal("authorization update source row changed")
	}
	var reason string
	var dirtyUpdatedAt time.Time
	if err := db.QueryRowContext(ctx, `SELECT reason, updated_at FROM juhe_business.group_account_stats_dirty WHERE group_id='__all__'`).Scan(&reason, &dirtyUpdatedAt); err != nil || reason != managementauthorizations.ResourceAuthorizationUpdatedReason || !dirtyUpdatedAt.UTC().Equal(now) {
		t.Fatalf("authorization update dirty metadata mismatch: err=%v", err)
	}
}

func assertW4AuthorizationUpdateInvalidations(t *testing.T, ctx context.Context, client *redisplatform.Client, now time.Time, firstVersion int) {
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
		if state.Version != fmt.Sprintf("w4-authorization-update-version-%d", firstVersion+index) || state.Reason != managementauthorizations.ResourceAuthorizationUpdatedReason || state.PublishedAt != now.Format("2006-01-02T15:04:05.000Z") {
			t.Fatalf("authorization update invalidation fields do not match expected topic index %d", index)
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
	sort.Strings(want)
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type != "string" {
			t.Fatalf("unexpected state Redis type %q", entry.Type)
		}
		got = append(got, entry.Key)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("authorization update state key count/digest mismatch: got=%d want=%d", len(got), len(want))
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
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(s) ORDER BY s.id),'[]'::jsonb)::text FROM juhe_business.system_sessions s WHERE s.id IN ($1,$2)`, w4AuthorizationUpdateSessionID, w4AuthorizationUpdateGranteeSessionID).Scan(&raw); err != nil {
		t.Fatalf("read authorization update session snapshot: %v", err)
	}
	return raw
}

func readW4AuthorizationUpdateSourceSnapshot(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT to_jsonb(s)::text FROM juhe_business.resource_authorization_sources s WHERE id=$1`, w4AuthorizationUpdateSourceID).Scan(&raw); err != nil {
		t.Fatalf("read authorization update source snapshot: %v", err)
	}
	return raw
}

type w4AuthorizationUpdateAuditCounts struct {
	Logs    int
	Targets int
	Viewers int
	Terms   int
}

func readW4AuthorizationUpdateAuditCounts(t *testing.T, ctx context.Context, db *sql.DB) w4AuthorizationUpdateAuditCounts {
	t.Helper()
	var counts w4AuthorizationUpdateAuditCounts
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM juhe_dataset.operation_logs), (SELECT count(*) FROM juhe_dataset.operation_log_targets), (SELECT count(*) FROM juhe_dataset.operation_log_viewers), (SELECT count(*) FROM juhe_dataset.operation_log_summary_search_terms)`).Scan(&counts.Logs, &counts.Targets, &counts.Viewers, &counts.Terms); err != nil {
		t.Fatalf("read authorization update audit counts: %v", err)
	}
	return counts
}

func assertW4AuthorizationUpdateOperationLog(t *testing.T, ctx context.Context, db *sql.DB, logID, traceID string, now time.Time) {
	t.Helper()
	var trace, actor, scope, mode, module, action, key, resourceType, resourceID, resourceName, summary, changes, method, path string
	var status int
	var createdAt time.Time
	if err := db.QueryRowContext(ctx, `SELECT trace_id, actor_system_account_id, operation_scope_system_account_id, mode, module, action, operation_key, resource_type, resource_id, resource_name, summary, changes_json, method, path, status_code, created_at FROM juhe_dataset.operation_logs WHERE id=$1`, logID).Scan(&trace, &actor, &scope, &mode, &module, &action, &key, &resourceType, &resourceID, &resourceName, &summary, &changes, &method, &path, &status, &createdAt); err != nil {
		t.Fatalf("read authorization update operation log: %v", err)
	}
	if trace != traceID || actor != w4AuthorizationUpdateAdminID || scope != w4AuthorizationUpdateOwnerID || mode != "admin" || module != "authorizations" || action != "update" || key != "authorizations.update" || resourceType != "authorization" || resourceID != w4AuthorizationUpdateGrantID || resourceName != "W4 Update Owner Group" || summary != "更新资源授权：W4 Update Owner Group -> W4 Update Grantee" || method != http.MethodPatch || path != "/__aisys__/api/authorizations/"+w4AuthorizationUpdateGrantID || status != http.StatusOK || !createdAt.UTC().Equal(now) {
		t.Fatal("authorization update operation log core fields do not match the HTTP contract")
	}
	var decoded []struct {
		Field string          `json:"field"`
		After json.RawMessage `json:"after"`
	}
	if err := json.Unmarshal([]byte(changes), &decoded); err != nil {
		t.Fatalf("decode authorization update changes: %v", err)
	}
	if len(decoded) != 2 || decoded[0].Field != "status" || string(decoded[0].After) != `"paused"` || decoded[1].Field != "limits" {
		t.Fatal("authorization update changes do not contain exact status and limits fields")
	}
	var limitsJSON string
	if err := json.Unmarshal(decoded[1].After, &limitsJSON); err != nil {
		t.Fatalf("decode authorization update audit limits string: %v", err)
	}
	if strings.TrimSpace(limitsJSON) == "" {
		t.Fatal("authorization update audit limits string is empty")
	}
	var limits struct {
		Daily struct {
			Enabled bool `json:"enabled"`
			Limit   int  `json:"limit"`
		} `json:"daily"`
	}
	if err := json.Unmarshal([]byte(limitsJSON), &limits); err != nil || !limits.Daily.Enabled || limits.Daily.Limit != 17 {
		t.Fatalf("decode authorization update audit limits: %v", err)
	}
	assertW4AuthorizationUpdateTargets(t, ctx, db, logID)
	assertW4AuthorizationUpdateViewers(t, ctx, db, logID)
	assertW4AuthorizationUpdateTerms(t, ctx, db, logID)
}

func assertW4AuthorizationUpdateTargets(t *testing.T, ctx context.Context, db *sql.DB, logID string) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT target_type, COALESCE(target_id,''), COALESCE(target_name,''), COALESCE(target_owner_system_account_id,''), relation FROM juhe_dataset.operation_log_targets WHERE operation_log_id=$1`, logID)
	if err != nil {
		t.Fatalf("query authorization update targets: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var kind, id, name, owner, relation string
		if err := rows.Scan(&kind, &id, &name, &owner, &relation); err != nil {
			t.Fatalf("scan authorization update target: %v", err)
		}
		got[relation] = strings.Join([]string{kind, id, name, owner}, "|")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate authorization update targets: %v", err)
	}
	want := map[string]string{"owner": "group|" + w4AuthorizationUpdateGroupID + "|W4 Update Owner Group|" + w4AuthorizationUpdateOwnerID, "grantee": "system_account|" + w4AuthorizationUpdateGranteeID + "|W4 Update Grantee|" + w4AuthorizationUpdateGranteeID, "primary": "authorization|" + w4AuthorizationUpdateGrantID + "|W4 Update Owner Group|" + w4AuthorizationUpdateOwnerID}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("authorization update target semantics do not match owner/grantee/primary contract")
	}
}

func assertW4AuthorizationUpdateViewers(t *testing.T, ctx context.Context, db *sql.DB, logID string) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT system_account_id, visibility_reason, detail_level FROM juhe_dataset.operation_log_viewers WHERE operation_log_id=$1`, logID)
	if err != nil {
		t.Fatalf("query authorization update viewers: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var account, reason, detail string
		if err := rows.Scan(&account, &reason, &detail); err != nil {
			t.Fatalf("scan authorization update viewer: %v", err)
		}
		got[account+"|"+reason] = detail
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate authorization update viewers: %v", err)
	}
	want := map[string]string{w4AuthorizationUpdateAdminID + "|actor_self": "full", w4AuthorizationUpdateOwnerID + "|authorization_owner": "full", w4AuthorizationUpdateGranteeID + "|authorization_grantee": "full"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("authorization update viewer identity/reason/detail semantics do not match")
	}
}

func assertW4AuthorizationUpdateTerms(t *testing.T, ctx context.Context, db *sql.DB, logID string) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT term FROM juhe_dataset.operation_log_summary_search_terms WHERE operation_log_id=$1`, logID)
	if err != nil {
		t.Fatalf("query authorization update search terms: %v", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var term string
		if err := rows.Scan(&term); err != nil {
			t.Fatalf("scan authorization update search term: %v", err)
		}
		got[term] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate authorization update search terms: %v", err)
	}
	for _, want := range []string{"w4", "update", "owner", "group", "grantee"} {
		if !got[want] {
			t.Fatalf("authorization update search terms missing expected token %q", want)
		}
	}
}

func assertW4AuthorizationUpdateQueueIncrement(t *testing.T, before, after queue.QueueInfo) {
	t.Helper()
	if before.Pending != 0 || before.Active != 0 || before.Retry != 0 || before.Archived != 0 || after.Pending != 0 || after.Active != 0 || after.Retry != 0 || after.Archived != 0 || after.Size != before.Size+1 || after.Completed != before.Completed+1 {
		t.Fatalf("authorization update repeat queue transition invalid: before=%+v after=%+v", before, after)
	}
}

func assertW4AuthorizationUpdateSecretFree(t *testing.T, label, raw string) {
	t.Helper()
	assertW4AuthorizationRevokeSecretFree(t, label, raw, w4AuthorizationUpdateToken, w4AuthorizationUpdateGranteeToken, w4AuthorizationUpdateCanary)
}

func assertW4AuthorizationUpdateHTTPSecretFree(t *testing.T, label string, response w4AuthorizationUpdateResponse) {
	t.Helper()
	assertW4AuthorizationUpdateSecretFree(t, label+" HTTP body", response.Body)
	index := 0
	for name, values := range response.Header {
		assertW4AuthorizationUpdateSecretFree(t, fmt.Sprintf("%s HTTP header entry %d", label, index), name+"\n"+strings.Join(values, "\n"))
		index++
	}
}

func assertW4AuthorizationUpdateRedisSecretFree(t *testing.T, label string, entries []w4AuthorizationRevokeRedisEntry) {
	t.Helper()
	for index, entry := range entries {
		assertW4AuthorizationUpdateSecretFree(t, fmt.Sprintf("%s entry %d key_sha256=%s", label, index, w4AuthorizationRevokeShortDigest(entry.Key)), entry.Key+"\n"+entry.Value)
	}
}

func assertW4AuthorizationUpdateNoSideEffects(t *testing.T, ctx context.Context, db *sql.DB, state *goredis.Client, queueDB *goredis.Client, inspector *queue.Inspector, wantBusiness, wantOperations string, wantState, wantQueue []w4AuthorizationRevokeRedisEntry, wantQueueInfo queue.QueueInfo, invalidations, logIDs int) {
	t.Helper()
	gotBusiness := readW4AuthorizationUpdateBusinessSnapshot(t, ctx, db)
	assertW4AuthorizationUpdateSecretFree(t, "PostgreSQL business after rejected update", gotBusiness)
	if gotBusiness != wantBusiness {
		t.Fatal("authorization update business changed after failed request")
	}
	gotOperations := readW4AuthorizationRevokeOperationLogSnapshot(t, ctx, db)
	assertW4AuthorizationUpdateSecretFree(t, "PostgreSQL audit after rejected update", gotOperations)
	if gotOperations != wantOperations {
		t.Fatal("authorization update operation logs changed after failed request")
	}
	gotState := readW4AuthorizationRevokeRedisDB(t, ctx, state)
	assertW4AuthorizationUpdateRedisSecretFree(t, "state Redis after rejected update", gotState)
	if !reflect.DeepEqual(gotState, wantState) {
		t.Fatal("authorization update state Redis changed after failed request")
	}
	queueRaw := readW4AuthorizationRevokeRedisDB(t, ctx, queueDB)
	assertW4AuthorizationUpdateRedisSecretFree(t, "Asynq Redis after rejected update", queueRaw)
	if got := stableW4AuthorizationRevokeQueueRedisSnapshot(queueRaw); !reflect.DeepEqual(got, wantQueue) {
		t.Fatal("authorization update queue snapshot changed after failed request")
	}
	if got := readW4AuthorizationRevokeQueueInfo(t, inspector, false); got != wantQueueInfo {
		t.Fatal("authorization update queue counters changed after failed request")
	}
	if invalidations != 4 || logIDs != 2 {
		t.Fatalf("authorization update failed side effects = invalidations=%d logIDs=%d", invalidations, logIDs)
	}
}
