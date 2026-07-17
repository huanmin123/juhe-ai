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
	"sync/atomic"
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
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w4AuthorizationRevokeNamespace = "w4-management-authorization-revoke"
	w4AuthorizationRevokeGrantID   = "rauthgrant_w4_revoke_active"
	w4AuthorizationRevokeRuntimeID = "rauth_w4_revoke_active"
	w4AuthorizationRevokeSourceID  = "rauthsrc_w4_revoke_active"
	w4AuthorizationRevokeGroupID   = "grp_w4_revoke_active"
	w4AuthorizationRevokeOwnerID   = "sys_w4_revoke_owner"
	w4AuthorizationRevokeWrongID   = "sys_w4_revoke_wrong_owner"
	w4AuthorizationRevokeGranteeID = "sys_w4_revoke_grantee"
	w4AuthorizationRevokeAdminAID  = "sys_w4_revoke_admin_a"
	w4AuthorizationRevokeAdminBID  = "sys_w4_revoke_admin_b"
	w4AuthorizationRevokeTokenA    = "w4-revoke-admin-a-sensitive-token"
	w4AuthorizationRevokeTokenB    = "w4-revoke-admin-b-sensitive-token"
	w4AuthorizationRevokeCanary    = "w4-revoke-wrong-owner-sensitive-canary"
	w4AuthorizationRevokeLogID     = "oplog_w4_authorization_revoke"
)

type w4AuthorizationRevokeSmokeResult struct {
	ConcurrentStatuses []int
}

func TestW4ManagementAuthorizationRevokePostgresRedisAsynqSmoke(t *testing.T) {
	result := exerciseW4ManagementAuthorizationRevokeSmoke(t)
	assertW4ManagementAuthorizationRevokeConcurrentStatuses(t, result.ConcurrentStatuses)
}

func exerciseW4ManagementAuthorizationRevokeSmoke(t *testing.T) w4AuthorizationRevokeSmokeResult {
	t.Helper()
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
	keyspaceOptions, err := goredis.ParseURL(redisStateURL)
	if err != nil {
		t.Fatalf("parse authorization revoke keyspace redis URL: %v", err)
	}
	keyspaceRedis = goredis.NewClient(keyspaceOptions)

	now := time.Date(2026, 7, 17, 12, 45, 0, 123000000, time.UTC)
	insertW4AuthorizationRevokeFixtures(t, ctx, db, now)
	insertW2ManagementSessionForAccountFixture(t, ctx, db, "sess_w4_revoke_admin_a", w4AuthorizationRevokeAdminAID, w4AuthorizationRevokeTokenA, now.Add(-time.Minute))
	insertW2ManagementSessionForAccountFixture(t, ctx, db, "sess_w4_revoke_admin_b", w4AuthorizationRevokeAdminBID, w4AuthorizationRevokeTokenB, now.Add(-time.Minute))

	store, err = postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open authorization revoke postgres store: %v", err)
	}
	assertW4AuthorizationRevokeTerminalStoreCases(t, ctx, store, db, now)

	var versionCalls atomic.Int32
	invalidator, err := gatewaycache.NewSystemAccountInvalidator(gatewaycache.SystemAccountInvalidatorOptions{
		State:     stateRedis,
		Namespace: w4AuthorizationRevokeNamespace,
		Now:       func() time.Time { return now },
		NewVersion: func(time.Time) (string, error) {
			return fmt.Sprintf("w4-authorization-revoke-version-%d", versionCalls.Add(1)), nil
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
	logClient = queue.NewClient(redisOpts)
	inspector = queue.NewInspector(redisOpts)

	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{Store: store, Now: func() time.Time { return now }})
	service := managementauthorizations.NewServiceWithOptions(managementauthorizations.ServiceOptions{
		Store:                    store,
		Now:                      func() time.Time { return now },
		Secret:                   "w4-authorization-revoke-test-secret",
		AuthorizationInvalidator: invalidator,
	})
	cfg := config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true, TrustProxy: "false"}
	var logIDCalls atomic.Int32
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                           cfg,
		Logger:                           logger,
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementAuthorizationRevokeHandler: httpapi.NewManagementAuthorizationRevokeHandlerWithOperationLog(
			service,
			httpapi.ManagementOperationLogOptions{
				Config: cfg, Logger: logger, Client: logClient, SettingsReader: store,
				Now: func() time.Time { return now },
				NewLogID: func() string {
					logIDCalls.Add(1)
					return w4AuthorizationRevokeLogID
				},
			},
		),
	})
	httpServer = httptest.NewServer(router)

	beforeWrongOwner := readW4AuthorizationRevokeDeepSnapshot(t, ctx, db)
	redisBeforeWrongOwner := readW4AuthorizationRevokeRedisSnapshot(t, ctx, keyspaceRedis)
	queueBeforeWrongOwner := readW4AuthorizationRevokeQueueInfo(t, inspector)
	wrongOwner := doW4AuthorizationRevokeRequest(t, ctx, httpServer.URL, w4AuthorizationRevokeTokenA, w4AuthorizationRevokeWrongID, w4AuthorizationRevokeCanary)
	if wrongOwner.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong-owner authorization revoke status = %d, want 404; body=%s", wrongOwner.StatusCode, wrongOwner.Body)
	}
	assertW4AuthorizationRevokeSnapshotUnchanged(t, "wrong owner", beforeWrongOwner, readW4AuthorizationRevokeDeepSnapshot(t, ctx, db))
	if got := readW4AuthorizationRevokeRedisSnapshot(t, ctx, keyspaceRedis); !reflect.DeepEqual(got, redisBeforeWrongOwner) {
		t.Fatalf("authorization revoke Redis changed after wrong owner: before=%+v after=%+v", redisBeforeWrongOwner, got)
	}
	if got := readW4AuthorizationRevokeQueueInfo(t, inspector); got != queueBeforeWrongOwner {
		t.Fatalf("authorization revoke queue changed after wrong owner: before=%+v after=%+v", queueBeforeWrongOwner, got)
	}

	responses := runW4AuthorizationRevokeConcurrentRequests(t, ctx, httpServer.URL)
	statuses := []int{responses[0].StatusCode, responses[1].StatusCode}
	assertW4ManagementAuthorizationRevokeConcurrentStatuses(t, statuses)
	success := responses[0]
	if success.StatusCode != http.StatusOK {
		success = responses[1]
	}
	successActor := success.ActorID
	var envelope struct {
		Data managementauthorizations.Summary `json:"data"`
	}
	if err := json.Unmarshal([]byte(success.Body), &envelope); err != nil {
		t.Fatalf("decode successful authorization revoke response: %v", err)
	}
	if envelope.Data.ID != w4AuthorizationRevokeGrantID || envelope.Data.Status != "revoked" || envelope.Data.RevokedBy != successActor || envelope.Data.RevokedAt == nil || !envelope.Data.RevokedAt.UTC().Equal(now) || !envelope.Data.UpdatedAt.UTC().Equal(now) {
		t.Fatalf("successful authorization revoke response = %+v, actor=%q", envelope.Data, successActor)
	}

	assertW4AuthorizationRevokeFinalRows(t, ctx, db, successActor, now)
	assertW4AuthorizationRevokeInvalidations(t, ctx, stateRedis, now)
	if got := versionCalls.Load(); got != 2 {
		t.Fatalf("authorization revoke invalidation versions = %d, want 2", got)
	}
	redisAfterSuccess := readW4AuthorizationRevokeRedisSnapshot(t, ctx, keyspaceRedis)
	if len(redisAfterSuccess) != 2 {
		t.Fatalf("authorization revoke Redis namespace = %+v, want exactly 2 keys", redisAfterSuccess)
	}
	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, func() error {
		workerErrMu.Lock()
		defer workerErrMu.Unlock()
		return workerRunErr
	}); err != nil {
		t.Fatal(err)
	}
	queueAfterSuccess := readW4AuthorizationRevokeQueueInfo(t, inspector)
	if queueAfterSuccess.Completed != 1 || queueAfterSuccess.Archived != 0 {
		t.Fatalf("authorization revoke queue = %+v, want completed=1 archived=0", queueAfterSuccess)
	}
	assertW4AuthorizationRevokeOperationLog(t, ctx, db, successActor, now)
	if got := logIDCalls.Load(); got != 1 {
		t.Fatalf("authorization revoke operation-log IDs = %d, want 1", got)
	}

	stableBusiness := readW4AuthorizationRevokeDeepSnapshot(t, ctx, db)
	stableRedis := readW4AuthorizationRevokeRedisSnapshot(t, ctx, keyspaceRedis)
	stableQueue := readW4AuthorizationRevokeQueueInfo(t, inspector)
	repeat := doW4AuthorizationRevokeRequest(t, ctx, httpServer.URL, w4AuthorizationRevokeTokenA, w4AuthorizationRevokeOwnerID, "req_w4_authorization_revoke_repeat")
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
	assertW4AuthorizationRevokeSnapshotUnchanged(t, "repeated revoke", stableBusiness, readW4AuthorizationRevokeDeepSnapshot(t, ctx, db))
	if got := readW4AuthorizationRevokeRedisSnapshot(t, ctx, keyspaceRedis); !reflect.DeepEqual(got, stableRedis) {
		t.Fatalf("authorization revoke Redis changed after repeat: before=%+v after=%+v", stableRedis, got)
	}
	if got := readW4AuthorizationRevokeQueueInfo(t, inspector); got != stableQueue {
		t.Fatalf("authorization revoke queue changed after repeat: before=%+v after=%+v", stableQueue, got)
	}
	if versionCalls.Load() != 2 || logIDCalls.Load() != 1 {
		t.Fatalf("authorization revoke repeat side effects: versions=%d logIDs=%d", versionCalls.Load(), logIDCalls.Load())
	}
	assertW4AuthorizationRevokeSensitiveValuesAbsent(t, ctx, db)
	return w4AuthorizationRevokeSmokeResult{ConcurrentStatuses: statuses}
}

func assertW4ManagementAuthorizationRevokeConcurrentStatuses(t *testing.T, statuses []int) {
	t.Helper()
	got := append([]int(nil), statuses...)
	sort.Ints(got)
	want := []int{http.StatusOK, http.StatusNotFound}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("concurrent authorization revoke statuses = %v, want exactly one 200 and one 404", statuses)
	}
}

type w4AuthorizationRevokeHTTPResponse struct {
	StatusCode int
	Body       string
	ActorID    string
}

func runW4AuthorizationRevokeConcurrentRequests(t *testing.T, ctx context.Context, serverURL string) [2]w4AuthorizationRevokeHTTPResponse {
	t.Helper()
	inputs := []struct {
		token   string
		actorID string
		reqID   string
	}{
		{w4AuthorizationRevokeTokenA, w4AuthorizationRevokeAdminAID, "req_w4_authorization_revoke_admin_a"},
		{w4AuthorizationRevokeTokenB, w4AuthorizationRevokeAdminBID, "req_w4_authorization_revoke_admin_b"},
	}
	start := make(chan struct{})
	results := make(chan w4AuthorizationRevokeHTTPResponse, len(inputs))
	var wg sync.WaitGroup
	for _, input := range inputs {
		input := input
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			response := doW4AuthorizationRevokeRequest(t, ctx, serverURL, input.token, w4AuthorizationRevokeOwnerID, input.reqID)
			response.ActorID = input.actorID
			results <- response
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var responses [2]w4AuthorizationRevokeHTTPResponse
	index := 0
	for response := range results {
		responses[index] = response
		index++
	}
	return responses
}

func doW4AuthorizationRevokeRequest(t *testing.T, ctx context.Context, serverURL, token, ownerID, requestID string) w4AuthorizationRevokeHTTPResponse {
	t.Helper()
	url := serverURL + "/__aisys__/api/authorizations/" + w4AuthorizationRevokeGrantID + "?systemAccountId=" + ownerID
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		t.Fatalf("create authorization revoke request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: managementauth.SessionCookieName, Value: token})
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
	createdAt := now.Add(-2 * time.Hour)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES
			($1, 'w4-revoke-admin-a', 'W4 Revoke Admin A', NULL, 'admin', 'active', 'hash', false, false, $6, $6),
			($2, 'w4-revoke-admin-b', 'W4 Revoke Admin B', NULL, 'admin', 'active', 'hash', false, false, $6, $6),
			($3, 'w4-revoke-owner', 'W4 Revoke Owner', NULL, 'user', 'active', 'hash', false, false, $6, $6),
			($4, 'w4-revoke-wrong-owner', 'W4 Revoke Wrong Owner', NULL, 'user', 'active', 'hash', false, false, $6, $6),
			($5, 'w4-revoke-grantee', 'W4 Revoke Grantee', NULL, 'user', 'active', 'hash', false, false, $6, $6)
	`, w4AuthorizationRevokeAdminAID, w4AuthorizationRevokeAdminBID, w4AuthorizationRevokeOwnerID, w4AuthorizationRevokeWrongID, w4AuthorizationRevokeGranteeID, createdAt); err != nil {
		t.Fatalf("insert authorization revoke accounts: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.groups (
			id, system_account_id, name, provider_code, description, enabled, is_default,
			group_type, scheduling_policy_json, created_at, updated_at
		) VALUES
			($1, $5, 'W4 Revoke Active Group', 'openai', NULL, true, false, 'personal', NULL, $9, $9),
			($2, $5, 'W4 Revoke Expired Group', 'openai', NULL, true, false, 'personal', NULL, $9, $9),
			($3, $5, 'W4 Revoke Revoked Group', 'openai', NULL, true, false, 'personal', NULL, $9, $9),
			($4, $5, 'W4 Revoke Returned Group', 'openai', NULL, true, false, 'personal', NULL, $9, $9)
	`, w4AuthorizationRevokeGroupID, "grp_w4_revoke_expired", "grp_w4_revoke_revoked", "grp_w4_revoke_returned", w4AuthorizationRevokeOwnerID, w4AuthorizationRevokeGranteeID, w4AuthorizationRevokeAdminAID, w4AuthorizationRevokeAdminBID, createdAt); err != nil {
		t.Fatalf("insert authorization revoke groups: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.resource_authorizations (
			id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
			scope, status, effective_source_type, effective_source_team_id, activated_at,
			last_source_changed_at, remark, expires_at, limits_json, created_by, created_at,
			revoked_by, revoked_at, revoked_reason, updated_at
		) VALUES
			($1, 'group', $3, $5, $6, 'use', 'active', 'manual', NULL, $7, $7, 'active fixture', NULL, NULL, $5, $7, NULL, NULL, NULL, $7),
			($2, 'group', $4, $5, $6, 'use', 'expired', 'manual', NULL, $7, $7, 'expired fixture', $8, NULL, $5, $7, NULL, NULL, NULL, $7)
	`, w4AuthorizationRevokeRuntimeID, "rauth_w4_revoke_expired", w4AuthorizationRevokeGroupID, "grp_w4_revoke_expired", w4AuthorizationRevokeOwnerID, w4AuthorizationRevokeGranteeID, createdAt, now.Add(-time.Hour)); err != nil {
		t.Fatalf("insert authorization revoke runtime rows: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.resource_authorization_sources (
			id, authorization_id, source_type, source_team_id, status, activated_at,
			ended_at, ended_reason, created_by, created_at, revoked_by, revoked_at, updated_at
		) VALUES
			($1, $3, 'manual', NULL, 'active', $5, NULL, NULL, $6, $5, NULL, NULL, $5),
			($2, $4, 'manual', NULL, 'active', $5, NULL, NULL, $6, $5, NULL, NULL, $5)
	`, w4AuthorizationRevokeSourceID, "rauthsrc_w4_revoke_expired", w4AuthorizationRevokeRuntimeID, "rauth_w4_revoke_expired", createdAt, w4AuthorizationRevokeOwnerID); err != nil {
		t.Fatalf("insert authorization revoke source rows: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.resource_authorization_grants (
			id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
			grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at,
			limits_json, created_by, created_at, revoked_by, revoked_at, updated_at
		) VALUES
			($1, 'group', $5, $9, 'system_account', $10, NULL, 'use', 'active', 'active fixture', NULL, NULL, $9, $11, NULL, NULL, $11),
			($2, 'group', $6, $9, 'system_account', $10, NULL, 'use', 'expired', 'expired fixture', $12, NULL, $9, $11, NULL, NULL, $11),
			($3, 'group', $7, $9, 'system_account', $10, NULL, 'use', 'revoked', 'revoked fixture', NULL, NULL, $9, $11, $9, $11, $11),
			($4, 'group', $8, $9, 'system_account', $10, NULL, 'use', 'returned', 'returned fixture', NULL, NULL, $9, $11, $10, $11, $11)
	`, w4AuthorizationRevokeGrantID, "rauthgrant_w4_revoke_expired", "rauthgrant_w4_revoke_revoked", "rauthgrant_w4_revoke_returned", w4AuthorizationRevokeGroupID, "grp_w4_revoke_expired", "grp_w4_revoke_revoked", "grp_w4_revoke_returned", w4AuthorizationRevokeOwnerID, w4AuthorizationRevokeGranteeID, createdAt, now.Add(-time.Hour)); err != nil {
		t.Fatalf("insert authorization revoke grant rows: %v", err)
	}
}

func assertW4AuthorizationRevokeTerminalStoreCases(t *testing.T, ctx context.Context, store *postgresstore.Store, db *sql.DB, now time.Time) {
	t.Helper()
	for _, id := range []string{"rauthgrant_w4_revoke_revoked", "rauthgrant_w4_revoke_returned"} {
		before := readW4AuthorizationRevokeGrantJSON(t, ctx, db, id)
		_, found, err := store.RevokeManagementResourceAuthorization(ctx, port.ManagementResourceAuthorizationRevokeInput{
			AuthorizationID: id, ActorSystemAccountID: w4AuthorizationRevokeAdminAID, CanAccessAll: true, RevokedAt: now,
		})
		if err != nil || found {
			t.Fatalf("terminal authorization revoke id=%s found=%t err=%v, want false nil", id, found, err)
		}
		if after := readW4AuthorizationRevokeGrantJSON(t, ctx, db, id); after != before {
			t.Fatalf("terminal authorization %s changed: before=%s after=%s", id, before, after)
		}
	}
	summary, found, err := store.RevokeManagementResourceAuthorization(ctx, port.ManagementResourceAuthorizationRevokeInput{
		AuthorizationID: "rauthgrant_w4_revoke_expired", ActorSystemAccountID: w4AuthorizationRevokeAdminAID, CanAccessAll: true, RevokedAt: now,
	})
	if err != nil || !found || summary.Status != "revoked" || summary.RevokedBy != w4AuthorizationRevokeAdminAID {
		t.Fatalf("expired authorization revoke summary=%+v found=%t err=%v", summary, found, err)
	}
	var statuses struct{ Grant, Runtime, Source string }
	if err := db.QueryRowContext(ctx, `
		SELECT g.status, r.status, s.status
		FROM juhe_business.resource_authorization_grants g
		JOIN juhe_business.resource_authorizations r ON r.id = 'rauth_w4_revoke_expired'
		JOIN juhe_business.resource_authorization_sources s ON s.authorization_id = r.id
		WHERE g.id = 'rauthgrant_w4_revoke_expired'
	`).Scan(&statuses.Grant, &statuses.Runtime, &statuses.Source); err != nil {
		t.Fatalf("read expired authorization reclaim statuses: %v", err)
	}
	if statuses != (struct{ Grant, Runtime, Source string }{"revoked", "revoked", "revoked"}) {
		t.Fatalf("expired authorization reclaim statuses = %+v", statuses)
	}
}

func readW4AuthorizationRevokeGrantJSON(t *testing.T, ctx context.Context, db *sql.DB, id string) string {
	t.Helper()
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT row_to_json(g)::text FROM juhe_business.resource_authorization_grants g WHERE id = $1`, id).Scan(&raw); err != nil {
		t.Fatalf("read authorization revoke grant %s: %v", id, err)
	}
	return raw
}

type w4AuthorizationRevokeSnapshot struct {
	Grants, Runtime, Sources, Dirty, Logs, Targets, Viewers, SearchTerms string
}

func readW4AuthorizationRevokeDeepSnapshot(t *testing.T, ctx context.Context, db *sql.DB) w4AuthorizationRevokeSnapshot {
	t.Helper()
	queries := []struct {
		destination *string
		query       string
	}{
		{query: `SELECT COALESCE(jsonb_agg(to_jsonb(g) ORDER BY g.id), '[]'::jsonb)::text FROM juhe_business.resource_authorization_grants g WHERE g.id LIKE 'rauthgrant_w4_revoke_%'`},
		{query: `SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id), '[]'::jsonb)::text FROM juhe_business.resource_authorizations r WHERE r.id LIKE 'rauth_w4_revoke_%'`},
		{query: `SELECT COALESCE(jsonb_agg(to_jsonb(s) ORDER BY s.id), '[]'::jsonb)::text FROM juhe_business.resource_authorization_sources s WHERE s.id LIKE 'rauthsrc_w4_revoke_%'`},
		{query: `SELECT COALESCE(jsonb_agg(to_jsonb(d) ORDER BY d.group_id), '[]'::jsonb)::text FROM juhe_business.group_account_stats_dirty d WHERE d.group_id = '__all__'`},
		{query: `SELECT COALESCE(jsonb_agg(to_jsonb(l) ORDER BY l.id), '[]'::jsonb)::text FROM juhe_dataset.operation_logs l WHERE l.id = '` + w4AuthorizationRevokeLogID + `'`},
		{query: `SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.id), '[]'::jsonb)::text FROM juhe_dataset.operation_log_targets x WHERE x.operation_log_id = '` + w4AuthorizationRevokeLogID + `'`},
		{query: `SELECT COALESCE(jsonb_agg(to_jsonb(v) ORDER BY v.system_account_id, v.visibility_reason), '[]'::jsonb)::text FROM juhe_dataset.operation_log_viewers v WHERE v.operation_log_id = '` + w4AuthorizationRevokeLogID + `'`},
		{query: `SELECT COALESCE(jsonb_agg(to_jsonb(s) ORDER BY s.term), '[]'::jsonb)::text FROM juhe_dataset.operation_log_summary_search_terms s WHERE s.operation_log_id = '` + w4AuthorizationRevokeLogID + `'`},
	}
	var snapshot w4AuthorizationRevokeSnapshot
	destinations := []*string{&snapshot.Grants, &snapshot.Runtime, &snapshot.Sources, &snapshot.Dirty, &snapshot.Logs, &snapshot.Targets, &snapshot.Viewers, &snapshot.SearchTerms}
	for i := range queries {
		queries[i].destination = destinations[i]
		if err := db.QueryRowContext(ctx, queries[i].query).Scan(queries[i].destination); err != nil {
			t.Fatalf("read authorization revoke deep snapshot %d: %v", i, err)
		}
	}
	return snapshot
}

func assertW4AuthorizationRevokeSnapshotUnchanged(t *testing.T, label string, before, after w4AuthorizationRevokeSnapshot) {
	t.Helper()
	if before != after {
		t.Fatalf("authorization revoke %s changed deep snapshot:\nbefore=%+v\nafter=%+v", label, before, after)
	}
}

type w4AuthorizationRevokeRedisEntry struct{ Key, Value string }

func readW4AuthorizationRevokeRedisSnapshot(t *testing.T, ctx context.Context, client *goredis.Client) []w4AuthorizationRevokeRedisEntry {
	t.Helper()
	pattern := "juhe-ai:" + w4AuthorizationRevokeNamespace + ":*"
	var cursor uint64
	var keys []string
	for {
		page, next, err := client.Scan(ctx, cursor, pattern, 100).Result()
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
	entries := make([]w4AuthorizationRevokeRedisEntry, 0, len(keys))
	for _, key := range keys {
		value, err := client.Get(ctx, key).Result()
		if err != nil {
			t.Fatalf("read authorization revoke Redis key %s: %v", key, err)
		}
		entries = append(entries, w4AuthorizationRevokeRedisEntry{key, value})
	}
	return entries
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
			t.Fatalf("authorization revoke invalidation %s = %+v, want version=%s", topic, state, wantVersion)
		}
	}
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

func assertW4AuthorizationRevokeFinalRows(t *testing.T, ctx context.Context, db *sql.DB, actor string, now time.Time) {
	t.Helper()
	var grant, runtime, source struct {
		Status, RevokedBy    string
		RevokedAt, UpdatedAt time.Time
	}
	if err := db.QueryRowContext(ctx, `SELECT status, revoked_by, revoked_at, updated_at FROM juhe_business.resource_authorization_grants WHERE id = $1`, w4AuthorizationRevokeGrantID).Scan(&grant.Status, &grant.RevokedBy, &grant.RevokedAt, &grant.UpdatedAt); err != nil {
		t.Fatalf("read revoked authorization grant: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status, revoked_by, revoked_at, updated_at FROM juhe_business.resource_authorizations WHERE id = $1`, w4AuthorizationRevokeRuntimeID).Scan(&runtime.Status, &runtime.RevokedBy, &runtime.RevokedAt, &runtime.UpdatedAt); err != nil {
		t.Fatalf("read revoked runtime authorization: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status, revoked_by, revoked_at, updated_at FROM juhe_business.resource_authorization_sources WHERE id = $1`, w4AuthorizationRevokeSourceID).Scan(&source.Status, &source.RevokedBy, &source.RevokedAt, &source.UpdatedAt); err != nil {
		t.Fatalf("read revoked authorization source: %v", err)
	}
	for name, row := range map[string]struct {
		Status, RevokedBy    string
		RevokedAt, UpdatedAt time.Time
	}{"grant": grant, "runtime": runtime, "source": source} {
		if row.Status != "revoked" || row.RevokedBy != actor || !row.RevokedAt.UTC().Equal(now) || !row.UpdatedAt.UTC().Equal(now) {
			t.Fatalf("authorization revoke %s final row = %+v, actor=%q now=%s", name, row, actor, now)
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

func assertW4AuthorizationRevokeOperationLog(t *testing.T, ctx context.Context, db *sql.DB, actor string, now time.Time) {
	t.Helper()
	var actorID, operationKey, resourceID, changesJSON, method, path string
	var createdAt time.Time
	var statusCode int
	if err := db.QueryRowContext(ctx, `
		SELECT actor_system_account_id, operation_key, resource_id, changes_json, method, path, status_code, created_at
		FROM juhe_dataset.operation_logs WHERE id = $1
	`, w4AuthorizationRevokeLogID).Scan(&actorID, &operationKey, &resourceID, &changesJSON, &method, &path, &statusCode, &createdAt); err != nil {
		t.Fatalf("read authorization revoke operation log: %v", err)
	}
	if actorID != actor || operationKey != "authorizations.revoke" || resourceID != w4AuthorizationRevokeGrantID || method != http.MethodDelete || path != "/__aisys__/api/authorizations/"+w4AuthorizationRevokeGrantID || statusCode != http.StatusOK || !createdAt.UTC().Equal(now) {
		t.Fatalf("authorization revoke operation log actor=%q key=%q resource=%q method=%q path=%q status=%d created=%s", actorID, operationKey, resourceID, method, path, statusCode, createdAt)
	}
	var changes []struct {
		Field, Label  string
		Before, After bool
		Sensitive     bool
	}
	if err := json.Unmarshal([]byte(changesJSON), &changes); err != nil {
		t.Fatalf("decode authorization revoke changes: %v", err)
	}
	if len(changes) != 1 || changes[0].Field != "revoked" || changes[0].Before || !changes[0].After || changes[0].Sensitive {
		t.Fatalf("authorization revoke changes = %+v", changes)
	}
	var targetCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.operation_log_targets WHERE operation_log_id = $1`, w4AuthorizationRevokeLogID).Scan(&targetCount); err != nil || targetCount != 3 {
		t.Fatalf("authorization revoke targets count=%d err=%v, want 3", targetCount, err)
	}
	viewers := map[string]bool{}
	rows, err := db.QueryContext(ctx, `SELECT system_account_id FROM juhe_dataset.operation_log_viewers WHERE operation_log_id = $1`, w4AuthorizationRevokeLogID)
	if err != nil {
		t.Fatalf("query authorization revoke viewers: %v", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatalf("scan authorization revoke viewer: %v", err)
		}
		viewers[id] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("iterate authorization revoke viewers: %v", err)
	}
	rows.Close()
	if len(viewers) != 3 || !viewers[actor] || !viewers[w4AuthorizationRevokeOwnerID] || !viewers[w4AuthorizationRevokeGranteeID] {
		t.Fatalf("authorization revoke viewers = %+v", viewers)
	}
	var searchCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.operation_log_summary_search_terms WHERE operation_log_id = $1`, w4AuthorizationRevokeLogID).Scan(&searchCount); err != nil || searchCount == 0 {
		t.Fatalf("authorization revoke search terms count=%d err=%v", searchCount, err)
	}
}

func assertW4AuthorizationRevokeSensitiveValuesAbsent(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	queries := []string{
		`SELECT COALESCE(string_agg(row_to_json(x)::text, ''), '') FROM juhe_business.resource_authorization_grants x WHERE x.id LIKE 'rauthgrant_w4_revoke_%'`,
		`SELECT COALESCE(string_agg(row_to_json(x)::text, ''), '') FROM juhe_business.resource_authorizations x WHERE x.id LIKE 'rauth_w4_revoke_%'`,
		`SELECT COALESCE(string_agg(row_to_json(x)::text, ''), '') FROM juhe_business.resource_authorization_sources x WHERE x.id LIKE 'rauthsrc_w4_revoke_%'`,
		`SELECT COALESCE(string_agg(row_to_json(x)::text, ''), '') FROM juhe_dataset.operation_logs x WHERE x.id = '` + w4AuthorizationRevokeLogID + `'`,
		`SELECT COALESCE(string_agg(row_to_json(x)::text, ''), '') FROM juhe_dataset.operation_log_targets x WHERE x.operation_log_id = '` + w4AuthorizationRevokeLogID + `'`,
		`SELECT COALESCE(string_agg(row_to_json(x)::text, ''), '') FROM juhe_dataset.operation_log_viewers x WHERE x.operation_log_id = '` + w4AuthorizationRevokeLogID + `'`,
		`SELECT COALESCE(string_agg(row_to_json(x)::text, ''), '') FROM juhe_dataset.operation_log_summary_search_terms x WHERE x.operation_log_id = '` + w4AuthorizationRevokeLogID + `'`,
		`SELECT COALESCE(string_agg(row_to_json(x)::text, ''), '') FROM juhe_business.system_sessions x WHERE x.id IN ('sess_w4_revoke_admin_a', 'sess_w4_revoke_admin_b')`,
	}
	for index, query := range queries {
		var raw string
		if err := db.QueryRowContext(ctx, query).Scan(&raw); err != nil {
			t.Fatalf("scan authorization revoke sensitive table %d: %v", index, err)
		}
		for _, forbidden := range []string{w4AuthorizationRevokeTokenA, w4AuthorizationRevokeTokenB, w4AuthorizationRevokeCanary} {
			if strings.Contains(raw, forbidden) {
				t.Fatalf("authorization revoke sensitive value leaked in table %d", index)
			}
		}
	}
}
