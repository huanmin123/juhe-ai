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
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"juhe-ai/backend-go/internal/app"
	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/gatewaycache"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementclientippolicies"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w6ManagementClientIPPolicyNamespace = "w6-management-client-ip-policy"
	w6ManagementClientIPPolicyClientIP  = "203.0.113.42"
	w6ManagementClientIPPolicyIPHash    = "87e060bf90104163b58dcc6d60932f0505c09322834f97a90bbd7275b646b79f"

	w6ManagementClientIPPolicyAdminID      = "sys_w6_client_ip_policy_admin"
	w6ManagementClientIPPolicyUserID       = "sys_w6_client_ip_policy_user"
	w6ManagementClientIPPolicyAdminSession = "sess_w6_client_ip_policy_admin"
	w6ManagementClientIPPolicyUserSession  = "sess_w6_client_ip_policy_user"
	w6ManagementClientIPPolicyAdminToken   = "w6-client-ip-policy-admin-session"
	w6ManagementClientIPPolicyUserToken    = "w6-client-ip-policy-user-session"
	w6ManagementClientIPPolicyInitialID    = "ip_policy_w6_initial_blacklist"

	w6ManagementClientIPPolicyForbiddenTrace   = "req_w6_client_ip_policy_forbidden"
	w6ManagementClientIPPolicyReplaceTrace     = "req_w6_client_ip_policy_replace"
	w6ManagementClientIPPolicyUnallowTrace     = "req_w6_client_ip_policy_unallow"
	w6ManagementClientIPPolicyZeroTrace        = "req_w6_client_ip_policy_unallow_zero"
	w6ManagementClientIPPolicyConcurrentATrace = "req_w6_client_ip_policy_concurrent_a"
	w6ManagementClientIPPolicyConcurrentBTrace = "req_w6_client_ip_policy_concurrent_b"

	w6ManagementClientIPPolicyReplaceReason     = "替换现有封禁策略"
	w6ManagementClientIPPolicyUnallowReason     = "管理员手动移出白名单"
	w6ManagementClientIPPolicyZeroReason        = "再次确认无活动白名单"
	w6ManagementClientIPPolicyConcurrentAReason = "并发白名单原因 A"
	w6ManagementClientIPPolicyConcurrentBReason = "并发白名单原因 B"
)

func TestW6ManagementClientIPPolicyPostgresRedisAsynqSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

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
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		terminateContainer(t, cleanupCtx, postgresContainer)
	}()

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
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		terminateContainer(t, cleanupCtx, redisContainer)
	}()

	redisURL, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis connection string: %v", err)
	}
	redisQueueURL := w3RedisURLWithDB(t, redisURL, 0)
	redisCacheURL := w3RedisURLWithDB(t, redisURL, 1)
	redisStateURL := w3RedisURLWithDB(t, redisURL, 2)
	redisOpts, err := queue.ParseRedisURL(redisQueueURL)
	if err != nil {
		t.Fatalf("parse redis queue url: %v", err)
	}
	cacheRedis, err := redisplatform.NewClient(
		redisCacheURL,
		w6ManagementClientIPPolicyNamespace+":cache",
	)
	if err != nil {
		t.Fatalf("open cache redis: %v", err)
	}
	defer closeRedisClient(t, cacheRedis)
	stateRedis, err := redisplatform.NewClient(
		redisStateURL,
		w6ManagementClientIPPolicyNamespace+":state",
	)
	if err != nil {
		t.Fatalf("open state redis: %v", err)
	}
	defer closeRedisClient(t, stateRedis)

	now := time.Date(2026, 7, 14, 9, 30, 0, 0, time.UTC)
	sessionCreatedAt := now.Add(-2 * time.Minute)
	insertW6ManagementClientIPPolicyFixtures(t, ctx, db, now, sessionCreatedAt)

	versionKey, err := gatewaycache.SharedCacheVersionKey(
		w6ManagementClientIPPolicyNamespace,
		gatewaycache.ClientIPPolicyByIPCacheName,
	)
	if err != nil {
		t.Fatalf("build client IP policy cache version key: %v", err)
	}
	const baselineVersion = "w6-client-ip-policy-version-baseline"
	if err := cacheRedis.SetRaw(
		ctx,
		versionKey,
		[]byte(baselineVersion),
		gatewaycache.SharedCacheVersionTTL,
	); err != nil {
		t.Fatalf("seed client IP policy cache version: %v", err)
	}

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var invalidationSequence atomic.Int64
	invalidator, err := gatewaycache.NewSystemAccountInvalidator(
		gatewaycache.SystemAccountInvalidatorOptions{
			Cache:     cacheRedis,
			State:     stateRedis,
			Namespace: w6ManagementClientIPPolicyNamespace,
			Now:       func() time.Time { return now },
			NewVersion: func(time.Time) (string, error) {
				sequence := invalidationSequence.Add(1)
				return fmt.Sprintf("w6-client-ip-policy-version-%d", sequence), nil
			},
		},
	)
	if err != nil {
		t.Fatalf("create client IP policy invalidator: %v", err)
	}
	allowlistVersionReader, err := httpapi.NewRedisSystemAPIClientIPAllowlistVersionReader(
		cacheRedis,
		w6ManagementClientIPPolicyNamespace,
	)
	if err != nil {
		t.Fatalf("create client IP allowlist version reader: %v", err)
	}
	rateLimitSettingsVersionReader, err := httpapi.NewRedisSystemAPIRateLimitSettingsVersionReader(
		cacheRedis,
		w6ManagementClientIPPolicyNamespace,
	)
	if err != nil {
		t.Fatalf("create system API rate limit settings version reader: %v", err)
	}
	rateLimitSettingsCache := httpapi.NewSystemAPIRateLimitSettingsCache(
		rateLimitSettingsVersionReader,
	)
	ipRateLimiter := &w6ManagementClientIPPolicyIPRateLimiter{
		delegate: httpapi.NewRedisSystemAPIIPRateLimiter(
			stateRedis,
			w6ManagementClientIPPolicyNamespace,
		),
	}
	authenticatedRateLimiter := &w6ManagementClientIPPolicyAuthenticatedRateLimiter{
		delegate: httpapi.NewRedisSystemAPIAuthenticatedRateLimiter(
			stateRedis,
			w6ManagementClientIPPolicyNamespace,
		),
	}

	var policyIDSequence atomic.Int64
	policyTransactor := &w6ManagementClientIPPolicyBarrierTransactor{delegate: store}
	service := managementclientippolicies.NewServiceWithOptions(
		managementclientippolicies.ServiceOptions{
			Transactor:  policyTransactor,
			Invalidator: invalidator,
			Logger:      logger,
			Now:         func() time.Time { return now },
			NewID: func(prefix string) string {
				return fmt.Sprintf(
					"%s_w6_management_client_ip_policy_%d",
					prefix,
					policyIDSequence.Add(1),
				)
			},
		},
	)
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})

	logClient := queue.NewClient(redisOpts)
	defer closeClient(t, logClient)
	inspector := queue.NewInspector(redisOpts)
	defer closeInspector(t, inspector)

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
	workerErr := func() error {
		workerErrMu.Lock()
		defer workerErrMu.Unlock()
		return workerRunErr
	}
	defer func() {
		stopWorker()
		select {
		case <-workerDone:
		case <-time.After(5 * time.Second):
			t.Fatal("ingest worker shutdown timed out")
		}
		if err := workerErr(); err != nil {
			t.Fatalf("ingest worker run: %v", err)
		}
	}()

	cfg := config.Config{
		Host:                 "127.0.0.1",
		Port:                 3000,
		ManagementAPIEnabled: true,
		TrustProxy:           "false",
		PostgresURL:          postgresURL,
		RedisCacheURL:        redisCacheURL,
		RedisStateURL:        redisStateURL,
		RedisQueueURL:        redisQueueURL,
		RedisNamespace:       w6ManagementClientIPPolicyNamespace,
	}
	var operationLogIDSequence atomic.Int64
	operationLogOptions := httpapi.ManagementOperationLogOptions{
		Config:         cfg,
		Logger:         logger,
		Client:         logClient,
		SettingsReader: store,
		Now:            func() time.Time { return now },
		NewLogID: func() string {
			return fmt.Sprintf(
				"oplog_w6_management_client_ip_policy_%d",
				operationLogIDSequence.Add(1),
			)
		},
	}
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                                  cfg,
		Logger:                                  logger,
		SystemAPIRateLimitReader:                store,
		SystemAPIRateLimitSettingsCache:         rateLimitSettingsCache,
		SystemAPIRateLimitSettingsVersionReader: rateLimitSettingsVersionReader,
		SystemAPIClientIPAllowlistReader:        store,
		SystemAPIClientIPAllowlistVersionReader: allowlistVersionReader,
		SystemAPIIPRateLimiter:                  ipRateLimiter,
		SystemAPIAuthenticatedRateLimiter:       authenticatedRateLimiter,
		ManagementAPIAuthMiddleware:             httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware:        httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementClientIPAllowlistHandler: httpapi.NewManagementClientIPAllowlistHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
		ManagementClientIPUnallowlistHandler: httpapi.NewManagementClientIPUnallowlistHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
	})

	forbidden := serveW6ManagementClientIPPolicyRequest(
		ctx,
		router,
		"allowlist",
		w6ManagementClientIPPolicyUserToken,
		"普通用户不可写入 IP 策略",
		w6ManagementClientIPPolicyForbiddenTrace,
	)
	assertW6ManagementClientIPPolicyMessage(
		t,
		forbidden,
		http.StatusForbidden,
		"需要管理员权限",
	)
	assertW6ManagementClientIPPolicyCounts(t, ctx, db, 1, 1, 0)
	assertW6ManagementClientIPPolicyCacheVersion(
		t,
		ctx,
		cacheRedis,
		versionKey,
		baselineVersion,
	)
	assertW2ManagementSessionLastSeenAt(
		t,
		ctx,
		db,
		w6ManagementClientIPPolicyUserSession,
		now,
	)
	assertW2ManagementSessionLastSeenAt(
		t,
		ctx,
		db,
		w6ManagementClientIPPolicyAdminSession,
		sessionCreatedAt,
	)
	if invalidationSequence.Load() != 0 || operationLogIDSequence.Load() != 0 {
		t.Fatalf(
			"forbidden request side effects: invalidations=%d operation_log_ids=%d",
			invalidationSequence.Load(),
			operationLogIDSequence.Load(),
		)
	}
	assertW6ManagementClientIPPolicyRateLimiterCalls(
		t,
		ipRateLimiter,
		authenticatedRateLimiter,
		1,
		1,
	)

	replacedRec := serveW6ManagementClientIPPolicyRequest(
		ctx,
		router,
		"allowlist",
		w6ManagementClientIPPolicyAdminToken,
		w6ManagementClientIPPolicyReplaceReason,
		w6ManagementClientIPPolicyReplaceTrace,
	)
	replaced := decodeW6ManagementClientIPPolicyAllowlistResponse(t, replacedRec)
	assertW6ManagementClientIPPolicyAllowlistSummary(
		t,
		replaced,
		w6ManagementClientIPPolicyReplaceReason,
		now,
	)
	versionAfterReplace := assertW6ManagementClientIPPolicyCacheVersionChanged(
		t,
		ctx,
		cacheRedis,
		versionKey,
		baselineVersion,
	)
	if versionAfterReplace != "w6-client-ip-policy-version-1" {
		t.Fatalf("replace cache version = %q, want sequence 1", versionAfterReplace)
	}
	assertW6ManagementClientIPPolicyInitialReplacement(t, ctx, db, replaced.ID, now)
	assertW6ManagementClientIPPolicyCounts(t, ctx, db, 2, 1, 1)
	assertW2ManagementSessionLastSeenAt(
		t,
		ctx,
		db,
		w6ManagementClientIPPolicyAdminSession,
		now,
	)
	assertW6ManagementClientIPPolicyRateLimiterCalls(
		t,
		ipRateLimiter,
		authenticatedRateLimiter,
		2,
		2,
	)
	setW2ManagementSessionLastSeenAt(
		t,
		ctx,
		db,
		w6ManagementClientIPPolicyAdminSession,
		sessionCreatedAt,
	)

	unallowRec := serveW6ManagementClientIPPolicyRequest(
		ctx,
		router,
		"unallowlist",
		w6ManagementClientIPPolicyAdminToken,
		w6ManagementClientIPPolicyUnallowReason,
		w6ManagementClientIPPolicyUnallowTrace,
	)
	unallow := decodeW6ManagementClientIPPolicyUnallowlistResponse(t, unallowRec)
	if unallow.DisabledCount != 1 {
		t.Fatalf("unallowlist disabledCount = %d, want 1", unallow.DisabledCount)
	}
	versionAfterUnallow := assertW6ManagementClientIPPolicyCacheVersionChanged(
		t,
		ctx,
		cacheRedis,
		versionKey,
		versionAfterReplace,
	)
	if versionAfterUnallow != "w6-client-ip-policy-version-2" {
		t.Fatalf("unallow cache version = %q, want sequence 2", versionAfterUnallow)
	}
	assertW6ManagementClientIPPolicyDisabled(
		t,
		ctx,
		db,
		replaced.ID,
		w6ManagementClientIPPolicyUnallowReason,
		now,
	)
	assertW6ManagementClientIPPolicyCounts(t, ctx, db, 2, 0, 0)
	assertW2ManagementSessionLastSeenAt(
		t,
		ctx,
		db,
		w6ManagementClientIPPolicyAdminSession,
		now,
	)
	assertW6ManagementClientIPPolicyRateLimiterCalls(
		t,
		ipRateLimiter,
		authenticatedRateLimiter,
		2,
		2,
	)

	zeroRec := serveW6ManagementClientIPPolicyRequest(
		ctx,
		router,
		"unallowlist",
		w6ManagementClientIPPolicyAdminToken,
		w6ManagementClientIPPolicyZeroReason,
		w6ManagementClientIPPolicyZeroTrace,
	)
	zero := decodeW6ManagementClientIPPolicyUnallowlistResponse(t, zeroRec)
	if zero.DisabledCount != 0 {
		t.Fatalf("zero-row unallowlist disabledCount = %d, want 0", zero.DisabledCount)
	}
	versionAfterZero := assertW6ManagementClientIPPolicyCacheVersionChanged(
		t,
		ctx,
		cacheRedis,
		versionKey,
		versionAfterUnallow,
	)
	if versionAfterZero != "w6-client-ip-policy-version-3" {
		t.Fatalf("zero-row cache version = %q, want sequence 3", versionAfterZero)
	}
	assertW6ManagementClientIPPolicyCounts(t, ctx, db, 2, 0, 0)
	assertW6ManagementClientIPPolicyRateLimiterCalls(
		t,
		ipRateLimiter,
		authenticatedRateLimiter,
		3,
		3,
	)

	concurrentReasons := []string{
		w6ManagementClientIPPolicyConcurrentAReason,
		w6ManagementClientIPPolicyConcurrentBReason,
	}
	concurrentTraces := []string{
		w6ManagementClientIPPolicyConcurrentATrace,
		w6ManagementClientIPPolicyConcurrentBTrace,
	}
	concurrentRecorders := make([]*httptest.ResponseRecorder, len(concurrentReasons))
	concurrentResults := make(chan w6ManagementClientIPPolicyConcurrentResult, len(concurrentReasons))
	barrier := policyTransactor.enableBarrier()
	concurrentCtx, cancelConcurrent := context.WithTimeout(ctx, 30*time.Second)
	var concurrentWG sync.WaitGroup
	var cleanupConcurrentOnce sync.Once
	cleanupConcurrent := func() {
		cleanupConcurrentOnce.Do(func() {
			barrier.release()
			cancelConcurrent()
			done := make(chan struct{})
			go func() {
				concurrentWG.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Errorf("concurrent client IP policy requests did not stop after cancellation")
			}
		})
	}
	defer cleanupConcurrent()
	startConcurrentRequest := func(index int) {
		concurrentWG.Add(1)
		go func() {
			defer concurrentWG.Done()
			concurrentResults <- w6ManagementClientIPPolicyConcurrentResult{
				index: index,
				recorder: serveW6ManagementClientIPPolicyRequest(
					concurrentCtx,
					router,
					"allowlist",
					w6ManagementClientIPPolicyAdminToken,
					concurrentReasons[index],
					concurrentTraces[index],
				),
			}
		}()
	}
	startConcurrentRequest(0)
	waitW6ManagementClientIPPolicyBarrier(
		t,
		concurrentCtx,
		barrier.firstLockAcquired,
		concurrentResults,
		"first registry row lock",
	)
	startConcurrentRequest(1)
	waitW6ManagementClientIPPolicyBarrier(
		t,
		concurrentCtx,
		barrier.secondLockAttempted,
		concurrentResults,
		"second registry row lock attempt",
	)
	waitForW6ManagementClientIPPolicyRegistryLockWait(t, concurrentCtx, db)
	barrier.release()
	for range concurrentReasons {
		select {
		case result := <-concurrentResults:
			concurrentRecorders[result.index] = result.recorder
		case <-concurrentCtx.Done():
			t.Fatalf("wait for concurrent client IP policy responses: %v", concurrentCtx.Err())
		}
	}
	cleanupConcurrent()
	policyTransactor.disableBarrier(barrier)

	concurrentPolicies := make(map[string]managementclientippolicies.PolicySummary, 2)
	for index, recorder := range concurrentRecorders {
		policy := decodeW6ManagementClientIPPolicyAllowlistResponse(t, recorder)
		assertW6ManagementClientIPPolicyAllowlistSummary(
			t,
			policy,
			concurrentReasons[index],
			now,
		)
		concurrentPolicies[concurrentTraces[index]] = policy
	}
	if concurrentPolicies[w6ManagementClientIPPolicyConcurrentATrace].ID ==
		concurrentPolicies[w6ManagementClientIPPolicyConcurrentBTrace].ID {
		t.Fatalf("concurrent allowlist responses reused policy ID: %+v", concurrentPolicies)
	}
	assertW6ManagementClientIPPolicyConcurrentRows(t, ctx, db, concurrentPolicies, now)
	assertW6ManagementClientIPPolicyCounts(t, ctx, db, 4, 1, 1)
	versionAfterConcurrent := assertW6ManagementClientIPPolicyCacheVersionChanged(
		t,
		ctx,
		cacheRedis,
		versionKey,
		versionAfterZero,
	)
	if versionAfterConcurrent != "w6-client-ip-policy-version-4" &&
		versionAfterConcurrent != "w6-client-ip-policy-version-5" {
		t.Fatalf(
			"concurrent cache version = %q, want one of concurrent versions 4 or 5",
			versionAfterConcurrent,
		)
	}
	if invalidationSequence.Load() != 5 {
		t.Fatalf("client IP policy invalidations = %d, want 5", invalidationSequence.Load())
	}
	assertW6ManagementClientIPPolicyRateLimiterCalls(
		t,
		ipRateLimiter,
		authenticatedRateLimiter,
		5,
		5,
	)

	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, workerErr); err != nil {
		t.Fatalf("wait for client IP policy operation logs: %v", err)
	}
	assertW6ManagementClientIPPolicyOperationLogs(
		t,
		ctx,
		db,
		now,
		map[string]w6ManagementClientIPPolicyOperationLogExpectation{
			w6ManagementClientIPPolicyReplaceTrace: {
				action:   "allowlist",
				reason:   w6ManagementClientIPPolicyReplaceReason,
				policyID: replaced.ID,
			},
			w6ManagementClientIPPolicyUnallowTrace: {
				action:           "unallowlist",
				reason:           w6ManagementClientIPPolicyUnallowReason,
				disabledCount:    1,
				hasDisabledCount: true,
			},
			w6ManagementClientIPPolicyZeroTrace: {
				action:           "unallowlist",
				reason:           w6ManagementClientIPPolicyZeroReason,
				disabledCount:    0,
				hasDisabledCount: true,
			},
			w6ManagementClientIPPolicyConcurrentATrace: {
				action:   "allowlist",
				reason:   w6ManagementClientIPPolicyConcurrentAReason,
				policyID: concurrentPolicies[w6ManagementClientIPPolicyConcurrentATrace].ID,
			},
			w6ManagementClientIPPolicyConcurrentBTrace: {
				action:   "allowlist",
				reason:   w6ManagementClientIPPolicyConcurrentBReason,
				policyID: concurrentPolicies[w6ManagementClientIPPolicyConcurrentBTrace].ID,
			},
		},
	)
	if operationLogIDSequence.Load() != 5 {
		t.Fatalf("operation log IDs generated = %d, want 5", operationLogIDSequence.Load())
	}
}

type w6ManagementClientIPPolicyIPRateLimiter struct {
	delegate httpapi.SystemAPIIPRateLimiter
	calls    atomic.Int64
}

func (l *w6ManagementClientIPPolicyIPRateLimiter) AllowSystemAPIIP(
	ctx context.Context,
	key string,
	settings httpapi.SystemAPIIPRateLimitSettings,
) (httpapi.SystemAPIRateLimitDecision, error) {
	l.calls.Add(1)
	return l.delegate.AllowSystemAPIIP(ctx, key, settings)
}

type w6ManagementClientIPPolicyAuthenticatedRateLimiter struct {
	delegate httpapi.SystemAPIAuthenticatedRateLimiter
	calls    atomic.Int64
}

func (l *w6ManagementClientIPPolicyAuthenticatedRateLimiter) AllowSystemAPIAuthenticated(
	ctx context.Context,
	key string,
	limit int,
) (httpapi.SystemAPIRateLimitDecision, error) {
	l.calls.Add(1)
	return l.delegate.AllowSystemAPIAuthenticated(ctx, key, limit)
}

type w6ManagementClientIPPolicyBarrierTransactor struct {
	delegate port.ManagementClientIPPolicyTransactor
	mu       sync.Mutex
	barrier  *w6ManagementClientIPPolicyLockBarrier
}

func (t *w6ManagementClientIPPolicyBarrierTransactor) ManagementClientIPPolicyInTx(
	ctx context.Context,
	fn func(context.Context, port.ManagementClientIPPolicyStore) error,
) error {
	t.mu.Lock()
	barrier := t.barrier
	t.mu.Unlock()
	return t.delegate.ManagementClientIPPolicyInTx(
		ctx,
		func(txCtx context.Context, store port.ManagementClientIPPolicyStore) error {
			if barrier != nil {
				store = &w6ManagementClientIPPolicyBarrierStore{
					ManagementClientIPPolicyStore: store,
					barrier:                       barrier,
				}
			}
			return fn(txCtx, store)
		},
	)
}

func (t *w6ManagementClientIPPolicyBarrierTransactor) enableBarrier() *w6ManagementClientIPPolicyLockBarrier {
	barrier := &w6ManagementClientIPPolicyLockBarrier{
		firstLockAcquired:   make(chan struct{}),
		secondLockAttempted: make(chan struct{}),
		releaseFirst:        make(chan struct{}),
	}
	t.mu.Lock()
	t.barrier = barrier
	t.mu.Unlock()
	return barrier
}

func (t *w6ManagementClientIPPolicyBarrierTransactor) disableBarrier(
	barrier *w6ManagementClientIPPolicyLockBarrier,
) {
	t.mu.Lock()
	if t.barrier == barrier {
		t.barrier = nil
	}
	t.mu.Unlock()
}

type w6ManagementClientIPPolicyLockBarrier struct {
	attempts            atomic.Int64
	firstLockAcquired   chan struct{}
	secondLockAttempted chan struct{}
	releaseFirst        chan struct{}
	releaseOnce         sync.Once
}

func (b *w6ManagementClientIPPolicyLockBarrier) release() {
	b.releaseOnce.Do(func() { close(b.releaseFirst) })
}

type w6ManagementClientIPPolicyBarrierStore struct {
	port.ManagementClientIPPolicyStore
	barrier *w6ManagementClientIPPolicyLockBarrier
}

func (s *w6ManagementClientIPPolicyBarrierStore) LockManagementClientIPRegistry(
	ctx context.Context,
	ipHash string,
) (port.ManagementClientIPRegistryRow, bool, error) {
	attempt := s.barrier.attempts.Add(1)
	if attempt == 2 {
		close(s.barrier.secondLockAttempted)
	}
	row, found, err := s.ManagementClientIPPolicyStore.LockManagementClientIPRegistry(ctx, ipHash)
	if err != nil || attempt != 1 {
		return row, found, err
	}
	close(s.barrier.firstLockAcquired)
	select {
	case <-s.barrier.releaseFirst:
		return row, found, nil
	case <-ctx.Done():
		return port.ManagementClientIPRegistryRow{}, false, ctx.Err()
	}
}

type w6ManagementClientIPPolicyConcurrentResult struct {
	index    int
	recorder *httptest.ResponseRecorder
}

func waitW6ManagementClientIPPolicyBarrier(
	t *testing.T,
	ctx context.Context,
	signal <-chan struct{},
	results <-chan w6ManagementClientIPPolicyConcurrentResult,
	label string,
) {
	t.Helper()
	select {
	case <-signal:
	case result := <-results:
		status := 0
		body := ""
		if result.recorder != nil {
			status = result.recorder.Code
			body = result.recorder.Body.String()
		}
		t.Fatalf(
			"client IP policy request %d completed before %s: status=%d body=%s",
			result.index,
			label,
			status,
			body,
		)
	case <-ctx.Done():
		t.Fatalf("wait for %s: %v", label, ctx.Err())
	}
}

func waitForW6ManagementClientIPPolicyRegistryLockWait(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiters int
		err := db.QueryRowContext(waitCtx, `
			SELECT count(*)
			FROM pg_stat_activity
			WHERE datname = current_database()
			  AND pid <> pg_backend_pid()
			  AND wait_event_type = 'Lock'
			  AND query LIKE '%client_ip_registry%'
			  AND query LIKE '%FOR UPDATE%'
		`).Scan(&waiters)
		if err != nil {
			t.Fatalf("inspect concurrent client IP registry lock wait: %v", err)
		}
		if waiters >= 1 {
			return
		}
		select {
		case <-ticker.C:
		case <-waitCtx.Done():
			t.Fatalf("second client IP policy transaction did not wait for registry row lock: %v", waitCtx.Err())
		}
	}
}

func assertW6ManagementClientIPPolicyRateLimiterCalls(
	t *testing.T,
	ipLimiter *w6ManagementClientIPPolicyIPRateLimiter,
	authenticatedLimiter *w6ManagementClientIPPolicyAuthenticatedRateLimiter,
	wantIP int64,
	wantAuthenticated int64,
) {
	t.Helper()
	if got := ipLimiter.calls.Load(); got != wantIP {
		t.Fatalf("system API IP limiter calls = %d, want %d", got, wantIP)
	}
	if got := authenticatedLimiter.calls.Load(); got != wantAuthenticated {
		t.Fatalf("system API authenticated limiter calls = %d, want %d", got, wantAuthenticated)
	}
}

func insertW6ManagementClientIPPolicyFixtures(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
	sessionCreatedAt time.Time,
) {
	t.Helper()
	accounts := []struct {
		id          string
		username    string
		displayName string
		role        string
	}{
		{
			id:          w6ManagementClientIPPolicyAdminID,
			username:    "w6-client-ip-policy-admin",
			displayName: "W6 Client IP Policy Admin",
			role:        "admin",
		},
		{
			id:          w6ManagementClientIPPolicyUserID,
			username:    "w6-client-ip-policy-user",
			displayName: "W6 Client IP Policy User",
			role:        "user",
		},
	}
	for _, account := range accounts {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.system_accounts (
				id, username, display_name, description, role, status, password_hash,
				must_change_password, image_generation_enabled, created_at, updated_at
			) VALUES (
				$1, $2, $3, NULL, $4, 'active', 'hash',
				false, false, $5, $6
			)
		`, account.id, account.username, account.displayName, account.role, now, now); err != nil {
			t.Fatalf("insert client IP policy account %s: %v", account.id, err)
		}
	}

	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w6ManagementClientIPPolicyAdminSession,
		w6ManagementClientIPPolicyAdminID,
		w6ManagementClientIPPolicyAdminToken,
		sessionCreatedAt,
	)
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w6ManagementClientIPPolicyUserSession,
		w6ManagementClientIPPolicyUserID,
		w6ManagementClientIPPolicyUserToken,
		sessionCreatedAt,
	)

	timestamp := now.Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_stats.client_ip_registry (
			ip_hash, bucket_no, aggregate_ip_key, client_ip, ip_version,
			first_seen_at, last_seen_at, created_at, updated_at
		) VALUES (
			$1, 191, $2, $2, 4,
			$3, $3, $3, $3
		)
	`, w6ManagementClientIPPolicyIPHash, w6ManagementClientIPPolicyClientIP, timestamp); err != nil {
		t.Fatalf("insert client IP registry fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_stats.client_ip_policies (
			id, ip_hash, policy_type, status, reason, expires_at,
			created_by_system_account_id, created_at, updated_at,
			disabled_at, disabled_by_system_account_id, disabled_reason
		) VALUES (
			$1, $2, 'blacklist', 'active', '初始封禁策略', NULL,
			$3, $4, $4,
			NULL, NULL, NULL
		)
	`,
		w6ManagementClientIPPolicyInitialID,
		w6ManagementClientIPPolicyIPHash,
		w6ManagementClientIPPolicyAdminID,
		timestamp,
	); err != nil {
		t.Fatalf("insert active client IP blacklist fixture: %v", err)
	}
}

func serveW6ManagementClientIPPolicyRequest(
	ctx context.Context,
	router http.Handler,
	action string,
	sessionToken string,
	reason string,
	requestID string,
) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"reason": reason})
	req := httptest.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"/__aisys__/api/ip-stats/"+w6ManagementClientIPPolicyIPHash+"/"+action,
		strings.NewReader(string(body)),
	)
	req.Header.Set("Cookie", managementauth.SessionCookieName+"="+sessionToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "w6-management-client-ip-policy-smoke")
	req.Header.Set("X-Request-Id", requestID)
	req.RemoteAddr = w6ManagementClientIPPolicyClientIP + ":12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func assertW6ManagementClientIPPolicyMessage(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	wantStatus int,
	wantMessage string,
) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("response status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertW6ManagementClientIPPolicyNoStore(t, rec)
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body["message"] != wantMessage {
		t.Fatalf("response message = %q, want %q", body["message"], wantMessage)
	}
}

func decodeW6ManagementClientIPPolicyAllowlistResponse(
	t *testing.T,
	rec *httptest.ResponseRecorder,
) managementclientippolicies.PolicySummary {
	t.Helper()
	if rec == nil {
		t.Fatal("allowlist recorder is nil")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("allowlist status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertW6ManagementClientIPPolicyNoStore(t, rec)
	var response struct {
		Data managementclientippolicies.PolicySummary `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode allowlist response: %v", err)
	}
	return response.Data
}

func decodeW6ManagementClientIPPolicyUnallowlistResponse(
	t *testing.T,
	rec *httptest.ResponseRecorder,
) managementclientippolicies.UnallowlistResult {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("unallowlist status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertW6ManagementClientIPPolicyNoStore(t, rec)
	var response struct {
		Data managementclientippolicies.UnallowlistResult `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode unallowlist response: %v", err)
	}
	return response.Data
}

func assertW6ManagementClientIPPolicyNoStore(
	t *testing.T,
	rec *httptest.ResponseRecorder,
) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func assertW6ManagementClientIPPolicyAllowlistSummary(
	t *testing.T,
	policy managementclientippolicies.PolicySummary,
	wantReason string,
	now time.Time,
) {
	t.Helper()
	if policy.ID == "" ||
		policy.IPHash != w6ManagementClientIPPolicyIPHash ||
		policy.PolicyType != "allowlist" ||
		policy.Status != "active" ||
		policy.Reason == nil ||
		*policy.Reason != wantReason ||
		policy.ExpiresAt != nil ||
		policy.CreatedBySystemAccountID != w6ManagementClientIPPolicyAdminID ||
		policy.CreatedAt != now.UTC().Format(time.RFC3339Nano) ||
		policy.UpdatedAt != now.UTC().Format(time.RFC3339Nano) ||
		policy.DisabledAt != nil ||
		policy.DisabledBySystemAccountID != nil ||
		policy.DisabledReason != nil {
		t.Fatalf("allowlist policy = %+v, want active reason %q", policy, wantReason)
	}
}

func assertW6ManagementClientIPPolicyCounts(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	wantTotal int,
	wantActive int,
	wantActiveAllowlist int,
) {
	t.Helper()
	var total int
	var active int
	var activeAllowlist int
	if err := db.QueryRowContext(ctx, `
		SELECT
			count(*),
			count(*) FILTER (WHERE status = 'active'),
			count(*) FILTER (WHERE status = 'active' AND policy_type = 'allowlist')
		FROM juhe_stats.client_ip_policies
		WHERE ip_hash = $1
	`, w6ManagementClientIPPolicyIPHash).Scan(
		&total,
		&active,
		&activeAllowlist,
	); err != nil {
		t.Fatalf("count client IP policies: %v", err)
	}
	if total != wantTotal || active != wantActive || activeAllowlist != wantActiveAllowlist {
		t.Fatalf(
			"client IP policy counts = total:%d active:%d active_allowlist:%d, want %d/%d/%d",
			total,
			active,
			activeAllowlist,
			wantTotal,
			wantActive,
			wantActiveAllowlist,
		)
	}
}

func assertW6ManagementClientIPPolicyInitialReplacement(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	createdPolicyID string,
	now time.Time,
) {
	t.Helper()
	var initialStatus string
	var initialDisabledAt sql.NullString
	var initialDisabledBy sql.NullString
	var initialDisabledReason sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT status, disabled_at, disabled_by_system_account_id, disabled_reason
		FROM juhe_stats.client_ip_policies
		WHERE id = $1
	`, w6ManagementClientIPPolicyInitialID).Scan(
		&initialStatus,
		&initialDisabledAt,
		&initialDisabledBy,
		&initialDisabledReason,
	); err != nil {
		t.Fatalf("read replaced initial client IP policy: %v", err)
	}
	if initialStatus != "disabled" ||
		!initialDisabledAt.Valid ||
		initialDisabledAt.String != now.UTC().Format(time.RFC3339Nano) ||
		!initialDisabledBy.Valid ||
		initialDisabledBy.String != w6ManagementClientIPPolicyAdminID ||
		!initialDisabledReason.Valid ||
		initialDisabledReason.String != "被新的白名单策略替换" {
		t.Fatalf(
			"initial policy replacement = status:%q at:%+v by:%+v reason:%+v",
			initialStatus,
			initialDisabledAt,
			initialDisabledBy,
			initialDisabledReason,
		)
	}

	var activeID string
	var activeReason sql.NullString
	var activeCreator string
	if err := db.QueryRowContext(ctx, `
		SELECT id, reason, created_by_system_account_id
		FROM juhe_stats.client_ip_policies
		WHERE ip_hash = $1
		  AND policy_type = 'allowlist'
		  AND status = 'active'
	`, w6ManagementClientIPPolicyIPHash).Scan(
		&activeID,
		&activeReason,
		&activeCreator,
	); err != nil {
		t.Fatalf("read replacement allowlist policy: %v", err)
	}
	if activeID != createdPolicyID ||
		!activeReason.Valid ||
		activeReason.String != w6ManagementClientIPPolicyReplaceReason ||
		activeCreator != w6ManagementClientIPPolicyAdminID {
		t.Fatalf(
			"replacement allowlist = id:%q reason:%+v creator:%q",
			activeID,
			activeReason,
			activeCreator,
		)
	}
}

func assertW6ManagementClientIPPolicyDisabled(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	policyID string,
	wantReason string,
	now time.Time,
) {
	t.Helper()
	var status string
	var disabledAt sql.NullString
	var disabledBy sql.NullString
	var disabledReason sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT status, disabled_at, disabled_by_system_account_id, disabled_reason
		FROM juhe_stats.client_ip_policies
		WHERE id = $1
	`, policyID).Scan(&status, &disabledAt, &disabledBy, &disabledReason); err != nil {
		t.Fatalf("read disabled client IP policy %s: %v", policyID, err)
	}
	if status != "disabled" ||
		!disabledAt.Valid ||
		disabledAt.String != now.UTC().Format(time.RFC3339Nano) ||
		!disabledBy.Valid ||
		disabledBy.String != w6ManagementClientIPPolicyAdminID ||
		!disabledReason.Valid ||
		disabledReason.String != wantReason {
		t.Fatalf(
			"disabled policy %s = status:%q at:%+v by:%+v reason:%+v",
			policyID,
			status,
			disabledAt,
			disabledBy,
			disabledReason,
		)
	}
}

func assertW6ManagementClientIPPolicyConcurrentRows(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	policies map[string]managementclientippolicies.PolicySummary,
	now time.Time,
) {
	t.Helper()
	wantIDs := map[string]struct{}{
		policies[w6ManagementClientIPPolicyConcurrentATrace].ID: {},
		policies[w6ManagementClientIPPolicyConcurrentBTrace].ID: {},
	}
	rows, err := db.QueryContext(ctx, `
		SELECT
			id,
			reason,
			status,
			created_by_system_account_id,
			disabled_at,
			disabled_by_system_account_id,
			disabled_reason
		FROM juhe_stats.client_ip_policies
		WHERE ip_hash = $1
		  AND reason IN ($2, $3)
		ORDER BY reason
	`,
		w6ManagementClientIPPolicyIPHash,
		w6ManagementClientIPPolicyConcurrentAReason,
		w6ManagementClientIPPolicyConcurrentBReason,
	)
	if err != nil {
		t.Fatalf("query concurrent client IP policies: %v", err)
	}
	defer rows.Close()

	activeCount := 0
	disabledCount := 0
	rowCount := 0
	for rows.Next() {
		rowCount++
		var id string
		var reason sql.NullString
		var status string
		var createdBy string
		var disabledAt sql.NullString
		var disabledBy sql.NullString
		var disabledReason sql.NullString
		if err := rows.Scan(
			&id,
			&reason,
			&status,
			&createdBy,
			&disabledAt,
			&disabledBy,
			&disabledReason,
		); err != nil {
			t.Fatalf("scan concurrent client IP policy: %v", err)
		}
		if _, ok := wantIDs[id]; !ok {
			t.Fatalf("unexpected concurrent client IP policy ID %q", id)
		}
		delete(wantIDs, id)
		if !reason.Valid ||
			(reason.String != w6ManagementClientIPPolicyConcurrentAReason &&
				reason.String != w6ManagementClientIPPolicyConcurrentBReason) ||
			createdBy != w6ManagementClientIPPolicyAdminID {
			t.Fatalf(
				"concurrent client IP policy %s = reason:%+v creator:%q",
				id,
				reason,
				createdBy,
			)
		}
		switch status {
		case "active":
			activeCount++
			if disabledAt.Valid || disabledBy.Valid || disabledReason.Valid {
				t.Fatalf(
					"active concurrent policy %s has disabled fields: at:%+v by:%+v reason:%+v",
					id,
					disabledAt,
					disabledBy,
					disabledReason,
				)
			}
		case "disabled":
			disabledCount++
			if !disabledAt.Valid ||
				disabledAt.String != now.UTC().Format(time.RFC3339Nano) ||
				!disabledBy.Valid ||
				disabledBy.String != w6ManagementClientIPPolicyAdminID ||
				!disabledReason.Valid ||
				disabledReason.String != "被新的白名单策略替换" {
				t.Fatalf(
					"disabled concurrent policy %s = at:%+v by:%+v reason:%+v",
					id,
					disabledAt,
					disabledBy,
					disabledReason,
				)
			}
		default:
			t.Fatalf("concurrent policy %s status = %q", id, status)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate concurrent client IP policies: %v", err)
	}
	if rowCount != 2 || activeCount != 1 || disabledCount != 1 || len(wantIDs) != 0 {
		t.Fatalf(
			"concurrent policies = rows:%d active:%d disabled:%d missing_ids:%v",
			rowCount,
			activeCount,
			disabledCount,
			wantIDs,
		)
	}
}

func assertW6ManagementClientIPPolicyCacheVersion(
	t *testing.T,
	ctx context.Context,
	cacheRedis *redisplatform.Client,
	versionKey string,
	want string,
) {
	t.Helper()
	got := readW6ManagementClientIPPolicyCacheVersion(t, ctx, cacheRedis, versionKey)
	if got != want {
		t.Fatalf("client IP policy cache version = %q, want %q", got, want)
	}
}

func assertW6ManagementClientIPPolicyCacheVersionChanged(
	t *testing.T,
	ctx context.Context,
	cacheRedis *redisplatform.Client,
	versionKey string,
	previous string,
) string {
	t.Helper()
	current := readW6ManagementClientIPPolicyCacheVersion(t, ctx, cacheRedis, versionKey)
	if current == "" || current == previous {
		t.Fatalf(
			"client IP policy cache version did not change: previous=%q current=%q",
			previous,
			current,
		)
	}
	return current
}

func readW6ManagementClientIPPolicyCacheVersion(
	t *testing.T,
	ctx context.Context,
	cacheRedis *redisplatform.Client,
	versionKey string,
) string {
	t.Helper()
	value, err := cacheRedis.GetRaw(ctx, versionKey)
	if err != nil {
		t.Fatalf("read client IP policy cache version %s: %v", versionKey, err)
	}
	return strings.TrimSpace(string(value))
}

type w6ManagementClientIPPolicyOperationLogExpectation struct {
	action           string
	reason           string
	policyID         string
	disabledCount    int64
	hasDisabledCount bool
}

type w6ManagementClientIPPolicyOperationLogRow struct {
	id                            string
	traceID                       string
	actorSystemAccountID          string
	actorUsername                 string
	actorDisplayName              string
	actorRole                     string
	operationScopeSystemAccountID sql.NullString
	mode                          string
	module                        string
	action                        string
	operationKey                  string
	resourceType                  string
	resourceID                    string
	resourceName                  string
	summary                       string
	detailLevel                   string
	visibilityScope               string
	changesJSON                   string
	metadataJSON                  string
	method                        string
	path                          string
	statusCode                    int
	clientIP                      string
	userAgent                     string
	createdAt                     time.Time
}

func assertW6ManagementClientIPPolicyOperationLogs(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
	expected map[string]w6ManagementClientIPPolicyOperationLogExpectation,
) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT
			id,
			trace_id,
			actor_system_account_id,
			actor_username,
			actor_display_name,
			actor_role,
			operation_scope_system_account_id,
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
		WHERE resource_type = 'client_ip'
		  AND resource_id = $1
		ORDER BY id
	`, w6ManagementClientIPPolicyIPHash)
	if err != nil {
		t.Fatalf("query client IP policy operation logs: %v", err)
	}
	defer rows.Close()

	remaining := make(map[string]w6ManagementClientIPPolicyOperationLogExpectation, len(expected))
	for traceID, expectation := range expected {
		remaining[traceID] = expectation
	}
	rowCount := 0
	for rows.Next() {
		rowCount++
		var row w6ManagementClientIPPolicyOperationLogRow
		if err := rows.Scan(
			&row.id,
			&row.traceID,
			&row.actorSystemAccountID,
			&row.actorUsername,
			&row.actorDisplayName,
			&row.actorRole,
			&row.operationScopeSystemAccountID,
			&row.mode,
			&row.module,
			&row.action,
			&row.operationKey,
			&row.resourceType,
			&row.resourceID,
			&row.resourceName,
			&row.summary,
			&row.detailLevel,
			&row.visibilityScope,
			&row.changesJSON,
			&row.metadataJSON,
			&row.method,
			&row.path,
			&row.statusCode,
			&row.clientIP,
			&row.userAgent,
			&row.createdAt,
		); err != nil {
			t.Fatalf("scan client IP policy operation log: %v", err)
		}
		expectation, ok := remaining[row.traceID]
		if !ok {
			t.Fatalf("unexpected client IP policy operation log trace %q: %+v", row.traceID, row)
		}
		delete(remaining, row.traceID)
		assertW6ManagementClientIPPolicyOperationLogRow(t, row, expectation, now)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate client IP policy operation logs: %v", err)
	}
	if rowCount != len(expected) || len(remaining) != 0 {
		t.Fatalf(
			"client IP policy operation logs = %d, want %d, missing=%v",
			rowCount,
			len(expected),
			remaining,
		)
	}

	var forbiddenCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_dataset.operation_logs
		WHERE trace_id = $1
	`, w6ManagementClientIPPolicyForbiddenTrace).Scan(&forbiddenCount); err != nil {
		t.Fatalf("count forbidden client IP policy operation logs: %v", err)
	}
	if forbiddenCount != 0 {
		t.Fatalf("forbidden request wrote %d operation log(s)", forbiddenCount)
	}

	assertW6ManagementClientIPPolicyOperationLogTargets(t, ctx, db, expected)

	var viewerCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_dataset.operation_log_viewers AS viewers
		INNER JOIN juhe_dataset.operation_logs AS logs
		  ON logs.id = viewers.operation_log_id
		WHERE logs.resource_type = 'client_ip'
		  AND logs.resource_id = $1
	`, w6ManagementClientIPPolicyIPHash).Scan(&viewerCount); err != nil {
		t.Fatalf("count client IP policy operation log viewers: %v", err)
	}
	if viewerCount != 0 {
		t.Fatalf("admin-only client IP policy operation log viewers = %d, want 0", viewerCount)
	}
}

func assertW6ManagementClientIPPolicyOperationLogTargets(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	expected map[string]w6ManagementClientIPPolicyOperationLogExpectation,
) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT
			logs.trace_id,
			targets.target_type,
			targets.target_id,
			targets.target_name,
			targets.target_owner_system_account_id,
			targets.relation
		FROM juhe_dataset.operation_log_targets AS targets
		INNER JOIN juhe_dataset.operation_logs AS logs
		  ON logs.id = targets.operation_log_id
		WHERE logs.resource_type = 'client_ip'
		  AND logs.resource_id = $1
		ORDER BY logs.trace_id, targets.id
	`, w6ManagementClientIPPolicyIPHash)
	if err != nil {
		t.Fatalf("query client IP policy operation log targets: %v", err)
	}
	defer rows.Close()

	remaining := make(map[string]struct{}, len(expected))
	for traceID := range expected {
		remaining[traceID] = struct{}{}
	}
	rowCount := 0
	for rows.Next() {
		rowCount++
		var traceID string
		var targetType string
		var targetID sql.NullString
		var targetName sql.NullString
		var targetOwner sql.NullString
		var relation string
		if err := rows.Scan(
			&traceID,
			&targetType,
			&targetID,
			&targetName,
			&targetOwner,
			&relation,
		); err != nil {
			t.Fatalf("scan client IP policy operation log target: %v", err)
		}
		if _, ok := remaining[traceID]; !ok {
			t.Fatalf("unexpected or duplicate client IP policy target trace %q", traceID)
		}
		delete(remaining, traceID)
		if targetType != "client_ip" ||
			!targetID.Valid || targetID.String != w6ManagementClientIPPolicyIPHash ||
			!targetName.Valid || targetName.String != w6ManagementClientIPPolicyIPHash[:12] ||
			targetOwner.Valid ||
			relation != "primary" {
			t.Fatalf(
				"client IP policy operation log target = trace:%q type:%q id:%+v name:%+v owner:%+v relation:%q",
				traceID,
				targetType,
				targetID,
				targetName,
				targetOwner,
				relation,
			)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate client IP policy operation log targets: %v", err)
	}
	if rowCount != len(expected) || len(remaining) != 0 {
		t.Fatalf(
			"client IP policy operation log targets = %d, want %d, missing=%v",
			rowCount,
			len(expected),
			remaining,
		)
	}
}

func assertW6ManagementClientIPPolicyOperationLogRow(
	t *testing.T,
	row w6ManagementClientIPPolicyOperationLogRow,
	expected w6ManagementClientIPPolicyOperationLogExpectation,
	now time.Time,
) {
	t.Helper()
	wantResourceName := w6ManagementClientIPPolicyIPHash[:12]
	wantSummary := "加入 IP 白名单：" + wantResourceName
	if expected.action == "unallowlist" {
		wantSummary = "移出 IP 白名单：" + wantResourceName
	}
	if !strings.HasPrefix(row.id, "oplog_w6_management_client_ip_policy_") ||
		row.actorSystemAccountID != w6ManagementClientIPPolicyAdminID ||
		row.actorUsername != "w6-client-ip-policy-admin" ||
		row.actorDisplayName != "W6 Client IP Policy Admin" ||
		row.actorRole != "admin" ||
		row.operationScopeSystemAccountID.Valid ||
		row.mode != "admin" ||
		row.module != "client_ip_stats" ||
		row.action != expected.action ||
		row.operationKey != "client_ip_stats."+expected.action ||
		row.resourceType != "client_ip" ||
		row.resourceID != w6ManagementClientIPPolicyIPHash ||
		row.resourceName != wantResourceName ||
		row.summary != wantSummary ||
		row.detailLevel != "full" ||
		row.visibilityScope != "admin_only" ||
		row.method != http.MethodPost ||
		row.path != "/__aisys__/api/ip-stats/"+w6ManagementClientIPPolicyIPHash+"/"+expected.action ||
		row.statusCode != http.StatusOK ||
		row.clientIP != w6ManagementClientIPPolicyClientIP ||
		row.userAgent != "w6-management-client-ip-policy-smoke" ||
		!row.createdAt.UTC().Equal(now.UTC()) {
		t.Fatalf("client IP policy operation log = %+v, expected = %+v", row, expected)
	}

	var metadata map[string]any
	if err := json.Unmarshal([]byte(row.metadataJSON), &metadata); err != nil {
		t.Fatalf("decode client IP policy operation log metadata %s: %v", row.metadataJSON, err)
	}
	if metadata["ipHash"] != w6ManagementClientIPPolicyIPHash ||
		metadata["policyType"] != "allowlist" ||
		metadata["reason"] != expected.reason {
		t.Fatalf("client IP policy operation log metadata = %+v", metadata)
	}

	var changes []port.OperationLogChange
	if err := json.Unmarshal([]byte(row.changesJSON), &changes); err != nil {
		t.Fatalf("decode client IP policy operation log changes %s: %v", row.changesJSON, err)
	}
	changeByField := make(map[string]port.OperationLogChange, len(changes))
	for _, change := range changes {
		changeByField[change.Field] = change
	}
	if expected.action == "allowlist" {
		if len(changes) != 4 ||
			metadata["policyId"] != expected.policyID ||
			metadata["durationLabel"] != "永久" ||
			changeByField["reason"].After != expected.reason ||
			changeByField["policyType"].After != "allowlist" ||
			changeByField["duration"].After != "永久" ||
			changeByField["expiresAt"].After != nil {
			t.Fatalf(
				"allowlist operation log metadata=%+v changes=%+v",
				metadata,
				changes,
			)
		}
		return
	}
	if !expected.hasDisabledCount {
		t.Fatalf("unallowlist expectation lacks disabled count: %+v", expected)
	}
	wantDisabledCount := float64(expected.disabledCount)
	if len(changes) != 3 ||
		metadata["disabledCount"] != wantDisabledCount ||
		changeByField["disabledCount"].After != wantDisabledCount ||
		changeByField["policyType"].Before != "allowlist" ||
		changeByField["policyType"].After != nil ||
		changeByField["reason"].After != expected.reason {
		t.Fatalf(
			"unallowlist operation log metadata=%+v changes=%+v",
			metadata,
			changes,
		)
	}
}
