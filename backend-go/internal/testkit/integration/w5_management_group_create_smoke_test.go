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
	"juhe-ai/backend-go/internal/modules/managementgroups"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w5ManagementGroupCreateNamespace = "w5-management-group-create"

	w5ManagementGroupAdminID      = "sys_w5_group_create_admin"
	w5ManagementGroupUserID       = "sys_w5_group_create_user"
	w5ManagementGroupAdminSession = "sess_w5_group_create_admin"
	w5ManagementGroupUserSession  = "sess_w5_group_create_user"
	w5ManagementGroupAdminToken   = "w5-management-group-create-admin-session"
	w5ManagementGroupUserToken    = "w5-management-group-create-user-session"

	w5ManagementGroupPersonalID   = "grp_w5_management_group_create_1"
	w5ManagementGroupHighID       = "grp_w5_management_group_create_2"
	w5ManagementGroupPersonalName = "W5 Admin Personal"
	w5ManagementGroupHighName     = "W5 User High Concurrency"
)

func TestW5ManagementGroupCreatePostgresRedisAsynqSmoke(t *testing.T) {
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
	redisOpts, err := queue.ParseRedisURL(redisQueueURL)
	if err != nil {
		t.Fatalf("parse redis queue url: %v", err)
	}
	stateRedis, err := redisplatform.NewClient(redisStateURL, w5ManagementGroupCreateNamespace+":state")
	if err != nil {
		t.Fatalf("open state redis: %v", err)
	}
	defer closeRedisClient(t, stateRedis)

	now := time.Date(2026, 7, 10, 17, 0, 0, 0, time.UTC)
	insertW5ManagementGroupCreateAccountFixtures(t, ctx, db, now)
	sessionCreatedAt := now.Add(-2 * time.Minute)
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w5ManagementGroupAdminSession,
		w5ManagementGroupAdminID,
		w5ManagementGroupAdminToken,
		sessionCreatedAt,
	)
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w5ManagementGroupUserSession,
		w5ManagementGroupUserID,
		w5ManagementGroupUserToken,
		sessionCreatedAt,
	)
	assertW5ManagementGroupCreateProviderSeeds(t, ctx, db)
	assertW5ManagementGroupCreateCounts(t, ctx, db, w5ManagementGroupSideEffectCounts{})

	invalidationCalls := 0
	invalidator, err := gatewaycache.NewSystemAccountInvalidator(gatewaycache.SystemAccountInvalidatorOptions{
		State:     stateRedis,
		Namespace: w5ManagementGroupCreateNamespace,
		Now:       func() time.Time { return now },
		NewVersion: func(time.Time) (string, error) {
			invalidationCalls++
			return fmt.Sprintf("w5-management-group-version-%d", invalidationCalls), nil
		},
	})
	if err != nil {
		t.Fatalf("create management group runtime invalidator: %v", err)
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
	groupIDCalls := 0
	service := managementgroups.NewServiceWithOptions(managementgroups.ServiceOptions{
		Store:       store,
		Invalidator: invalidator,
		Logger:      logger,
		Now:         func() time.Time { return now },
		NewID: func(prefix string) string {
			groupIDCalls++
			return fmt.Sprintf("%s_w5_management_group_create_%d", prefix, groupIDCalls)
		},
	})
	cfg := config.Config{
		Host:                 "127.0.0.1",
		Port:                 3000,
		ManagementAPIEnabled: true,
		TrustProxy:           "false",
	}
	logIDCalls := 0
	operationLogOptions := httpapi.ManagementOperationLogOptions{
		Config:         cfg,
		Logger:         logger,
		Client:         logClient,
		SettingsReader: store,
		Now:            func() time.Time { return now },
		NewLogID: func() string {
			logIDCalls++
			return fmt.Sprintf("oplog_w5_management_group_create_%d", logIDCalls)
		},
	}
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                           cfg,
		Logger:                           logger,
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementGroupCreateHandler: httpapi.NewManagementGroupCreateHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
		ManagementMyGroupCreateHandler: httpapi.NewManagementMyGroupCreateHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
	})

	adminRec := serveW5ManagementGroupCreateRequest(
		router,
		"/__aisys__/api/groups?systemAccountId="+w5ManagementGroupUserID,
		w5ManagementGroupAdminToken,
		`{
			"name":" W5 Admin Personal ",
			"providerCode":" openai ",
			"description":" admin-created personal group ",
			"groupType":"personal"
		}`,
		"req_w5_management_group_admin_create",
	)
	if adminRec.Code != http.StatusCreated {
		t.Fatalf("admin create status = %d, body = %s", adminRec.Code, adminRec.Body.String())
	}
	adminGroup, adminBody := decodeW5ManagementGroupCreateResponse(t, adminRec)
	assertW5ManagementGroupPersonalResponse(t, adminGroup, adminBody)
	assertW5ManagementGroupRow(t, ctx, db, w5ManagementGroupPersonalID, w5ManagementGroupExpectedPersonalRow(now))
	assertW2ManagementSessionLastSeenAt(t, ctx, db, w5ManagementGroupAdminSession, now)
	assertW2ManagementSessionLastSeenAt(t, ctx, db, w5ManagementGroupUserSession, sessionCreatedAt)
	assertW5ManagementGroupRuntimeInvalidation(
		t,
		ctx,
		stateRedis,
		invalidationCalls,
		"w5-management-group-version-1",
		now,
	)

	userRec := serveW5ManagementGroupCreateRequest(
		router,
		"/__aisys__/api/my-groups",
		w5ManagementGroupUserToken,
		`{
			"name":"W5 User High Concurrency",
			"providerCode":"gpt",
			"description":"user high concurrency group",
			"enabled":false,
			"groupType":"high_concurrency",
			"schedulingPolicy":{
				"defaultSoftConcurrency":25,
				"maxQueueWaitMs":90000,
				"clientIpConcurrencyLimit":8,
				"clientIpConcurrencyOverflowMode":"queue",
				"imageLaneMaxConcurrency":3
			}
		}`,
		"req_w5_management_group_user_create",
	)
	if userRec.Code != http.StatusCreated {
		t.Fatalf("user create status = %d, body = %s", userRec.Code, userRec.Body.String())
	}
	userGroup, userBody := decodeW5ManagementGroupCreateResponse(t, userRec)
	assertW5ManagementGroupHighConcurrencyResponse(t, userGroup, userBody)
	assertW5ManagementGroupRow(t, ctx, db, w5ManagementGroupHighID, w5ManagementGroupExpectedHighRow(now))
	assertW2ManagementSessionLastSeenAt(t, ctx, db, w5ManagementGroupAdminSession, now)
	assertW2ManagementSessionLastSeenAt(t, ctx, db, w5ManagementGroupUserSession, now)
	assertW5ManagementGroupRuntimeInvalidation(
		t,
		ctx,
		stateRedis,
		invalidationCalls,
		"w5-management-group-version-2",
		now,
	)

	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, func() error {
		workerErrMu.Lock()
		defer workerErrMu.Unlock()
		return workerRunErr
	}); err != nil {
		t.Fatal(err)
	}
	queueInfoBeforeDuplicate := readW5ManagementGroupOperationLogQueueInfo(t, inspector)
	if queueInfoBeforeDuplicate.Completed != 2 {
		t.Fatalf("operation log queue completed = %d, want 2", queueInfoBeforeDuplicate.Completed)
	}
	assertW5ManagementGroupOperationLogs(t, ctx, db, now)
	sideEffectsBeforeDuplicate := readW5ManagementGroupSideEffectCounts(t, ctx, db)
	wantSideEffects := w5ManagementGroupSideEffectCounts{
		Groups:                  2,
		OperationLogs:           2,
		OperationLogTargets:     2,
		OperationLogViewers:     4,
		OperationLogSearchTerms: sideEffectsBeforeDuplicate.OperationLogSearchTerms,
	}
	if sideEffectsBeforeDuplicate.OperationLogSearchTerms == 0 {
		t.Fatal("operation log search term count = 0, want generated search rows")
	}
	assertW5ManagementGroupCreateCounts(t, ctx, db, wantSideEffects)

	duplicateRec := serveW5ManagementGroupCreateRequest(
		router,
		"/__aisys__/api/my-groups",
		w5ManagementGroupUserToken,
		`{
			"name":"W5 Admin Personal",
			"providerCode":"openai",
			"description":"duplicate should fail in postgres",
			"enabled":false,
			"groupType":"personal"
		}`,
		"req_w5_management_group_duplicate",
	)
	if duplicateRec.Code != http.StatusConflict {
		t.Fatalf("duplicate create status = %d, body = %s", duplicateRec.Code, duplicateRec.Body.String())
	}
	var duplicateBody map[string]string
	if err := json.NewDecoder(duplicateRec.Body).Decode(&duplicateBody); err != nil {
		t.Fatalf("decode duplicate create response: %v", err)
	}
	const wantDuplicateMessage = "同一供应商下分组名称已存在：" + w5ManagementGroupPersonalName
	if duplicateBody["message"] != wantDuplicateMessage {
		t.Fatalf("duplicate create message = %q, want %q", duplicateBody["message"], wantDuplicateMessage)
	}

	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, func() error {
		workerErrMu.Lock()
		defer workerErrMu.Unlock()
		return workerRunErr
	}); err != nil {
		t.Fatal(err)
	}
	queueInfoAfterDuplicate := readW5ManagementGroupOperationLogQueueInfo(t, inspector)
	if queueInfoAfterDuplicate.Completed != queueInfoBeforeDuplicate.Completed {
		t.Fatalf(
			"operation log queue completed changed after duplicate: before=%d after=%d",
			queueInfoBeforeDuplicate.Completed,
			queueInfoAfterDuplicate.Completed,
		)
	}
	assertW5ManagementGroupCreateCounts(t, ctx, db, sideEffectsBeforeDuplicate)
	assertW5ManagementGroupMissing(t, ctx, db, "grp_w5_management_group_create_3")
	assertW5ManagementGroupRuntimeInvalidation(
		t,
		ctx,
		stateRedis,
		invalidationCalls,
		"w5-management-group-version-2",
		now,
	)
	assertW2ManagementSessionLastSeenAt(t, ctx, db, w5ManagementGroupAdminSession, now)
	assertW2ManagementSessionLastSeenAt(t, ctx, db, w5ManagementGroupUserSession, now)
	if logIDCalls != 2 {
		t.Fatalf("operation log id calls = %d, want 2", logIDCalls)
	}
}

func serveW5ManagementGroupCreateRequest(
	router http.Handler,
	target string,
	sessionToken string,
	body string,
	requestID string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Cookie", managementauth.SessionCookieName+"="+sessionToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "w5-management-group-create-smoke")
	req.Header.Set("X-Request-Id", requestID)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeW5ManagementGroupCreateResponse(
	t *testing.T,
	rec *httptest.ResponseRecorder,
) (managementgroups.Summary, string) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	bodyText := rec.Body.String()
	var envelope map[string]json.RawMessage
	if err := json.NewDecoder(strings.NewReader(bodyText)).Decode(&envelope); err != nil {
		t.Fatalf("decode management group create response: %v", err)
	}
	if len(envelope) != 1 {
		t.Fatalf("management group create response keys = %v, want only data", envelope)
	}
	rawData, ok := envelope["data"]
	if !ok {
		t.Fatalf("management group create response = %s, missing data", bodyText)
	}
	var group managementgroups.Summary
	if err := json.Unmarshal(rawData, &group); err != nil {
		t.Fatalf("decode management group create data: %v", err)
	}
	return group, bodyText
}

func assertW5ManagementGroupPersonalResponse(
	t *testing.T,
	group managementgroups.Summary,
	body string,
) {
	t.Helper()
	if group.ID != w5ManagementGroupPersonalID ||
		group.SystemAccountID != w5ManagementGroupUserID ||
		group.Name != w5ManagementGroupPersonalName ||
		group.ProviderCode != "openai" ||
		group.Description == nil ||
		*group.Description != "admin-created personal group" ||
		!group.Enabled ||
		group.IsDefault ||
		group.GroupType != "personal" ||
		group.SchedulingPolicy != nil ||
		group.AccountIDs == nil ||
		len(group.AccountIDs) != 0 ||
		group.AccountStats != (managementgroups.GroupAccountStats{}) {
		t.Fatalf("admin personal group response = %+v", group)
	}
	if !strings.Contains(body, `"systemAccountId":"`+w5ManagementGroupUserID+`"`) {
		t.Fatalf("admin personal group response missing owner field: %s", body)
	}
}

func assertW5ManagementGroupHighConcurrencyResponse(
	t *testing.T,
	group managementgroups.Summary,
	body string,
) {
	t.Helper()
	if group.ID != w5ManagementGroupHighID ||
		group.SystemAccountID != "" ||
		group.Name != w5ManagementGroupHighName ||
		group.ProviderCode != "gpt" ||
		group.Description == nil ||
		*group.Description != "user high concurrency group" ||
		group.Enabled ||
		group.IsDefault ||
		group.GroupType != "high_concurrency" ||
		group.AccountIDs == nil ||
		len(group.AccountIDs) != 0 ||
		group.AccountStats != (managementgroups.GroupAccountStats{}) {
		t.Fatalf("user high concurrency group response = %+v", group)
	}
	if strings.Contains(body, `"systemAccountId"`) {
		t.Fatalf("self group response leaked systemAccountId: %s", body)
	}
	assertW5ManagementGroupCompletePolicy(t, group.SchedulingPolicy, "response")
}

func insertW5ManagementGroupCreateAccountFixtures(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES
			(
				$1, 'w5-group-create-admin', 'W5 Group Create Admin', NULL, 'admin', 'active', 'hash',
				false, false, $3, $3
			),
			(
				$2, 'w5-group-create-user', 'W5 Group Create User', NULL, 'user', 'active', 'hash',
				false, false, $3, $3
			)
	`, w5ManagementGroupAdminID, w5ManagementGroupUserID, now)
	if err != nil {
		t.Fatalf("insert W5 management group create accounts: %v", err)
	}
}

func assertW5ManagementGroupCreateProviderSeeds(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_business.providers
		WHERE code IN ('openai', 'gpt')
		  AND enabled = true
	`).Scan(&count); err != nil {
		t.Fatalf("count W5 management group provider seeds: %v", err)
	}
	if count != 2 {
		t.Fatalf("enabled W5 management group provider seed count = %d, want 2", count)
	}
}

type w5ManagementGroupRow struct {
	ID                   string
	SystemAccountID      string
	Name                 string
	ProviderCode         string
	Description          sql.NullString
	Enabled              bool
	IsDefault            bool
	GroupType            string
	SchedulingPolicyJSON sql.NullString
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func w5ManagementGroupExpectedPersonalRow(now time.Time) w5ManagementGroupRow {
	return w5ManagementGroupRow{
		ID:              w5ManagementGroupPersonalID,
		SystemAccountID: w5ManagementGroupUserID,
		Name:            w5ManagementGroupPersonalName,
		ProviderCode:    "openai",
		Description:     sql.NullString{String: "admin-created personal group", Valid: true},
		Enabled:         true,
		GroupType:       "personal",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func w5ManagementGroupExpectedHighRow(now time.Time) w5ManagementGroupRow {
	return w5ManagementGroupRow{
		ID:              w5ManagementGroupHighID,
		SystemAccountID: w5ManagementGroupUserID,
		Name:            w5ManagementGroupHighName,
		ProviderCode:    "gpt",
		Description:     sql.NullString{String: "user high concurrency group", Valid: true},
		Enabled:         false,
		GroupType:       "high_concurrency",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func assertW5ManagementGroupRow(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	groupID string,
	want w5ManagementGroupRow,
) {
	t.Helper()
	var got w5ManagementGroupRow
	if err := db.QueryRowContext(ctx, `
		SELECT
			id,
			system_account_id,
			name,
			provider_code,
			description,
			enabled,
			is_default,
			group_type,
			scheduling_policy_json,
			created_at,
			updated_at
		FROM juhe_business.groups
		WHERE id = $1
	`, groupID).Scan(
		&got.ID,
		&got.SystemAccountID,
		&got.Name,
		&got.ProviderCode,
		&got.Description,
		&got.Enabled,
		&got.IsDefault,
		&got.GroupType,
		&got.SchedulingPolicyJSON,
		&got.CreatedAt,
		&got.UpdatedAt,
	); err != nil {
		t.Fatalf("read W5 management group row %s: %v", groupID, err)
	}
	if got.ID != want.ID ||
		got.SystemAccountID != want.SystemAccountID ||
		got.Name != want.Name ||
		got.ProviderCode != want.ProviderCode ||
		got.Description != want.Description ||
		got.Enabled != want.Enabled ||
		got.IsDefault != want.IsDefault ||
		got.GroupType != want.GroupType ||
		!got.CreatedAt.UTC().Equal(want.CreatedAt.UTC()) ||
		!got.UpdatedAt.UTC().Equal(want.UpdatedAt.UTC()) {
		t.Fatalf("W5 management group row = %+v, want %+v", got, want)
	}
	if want.GroupType == "personal" {
		if got.SchedulingPolicyJSON.Valid {
			t.Fatalf("personal group scheduling policy = %q, want NULL", got.SchedulingPolicyJSON.String)
		}
		return
	}
	if !got.SchedulingPolicyJSON.Valid {
		t.Fatal("high concurrency group scheduling policy is NULL")
	}
	var policy managementgroups.SchedulingPolicy
	if err := json.Unmarshal([]byte(got.SchedulingPolicyJSON.String), &policy); err != nil {
		t.Fatalf("decode high concurrency DB policy %s: %v", got.SchedulingPolicyJSON.String, err)
	}
	assertW5ManagementGroupCompletePolicy(t, &policy, "database")
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got.SchedulingPolicyJSON.String), &fields); err != nil {
		t.Fatalf("decode high concurrency DB policy fields: %v", err)
	}
	if len(fields) != 16 {
		t.Fatalf("high concurrency DB policy field count = %d, want 16: %v", len(fields), fields)
	}
}

func assertW5ManagementGroupCompletePolicy(
	t *testing.T,
	got *managementgroups.SchedulingPolicy,
	source string,
) {
	t.Helper()
	want := managementgroups.SchedulingPolicy{
		Mode:                            "balanced_fast",
		DefaultSoftConcurrency:          25,
		FastFirstEnabled:                true,
		FallbackOnQueueEnabled:          true,
		BreakAffinityOnSoftLimit:        true,
		BreakAffinityOnQueueWaitMs:      0,
		SlowRequestThresholdMs:          30000,
		FirstOutputSlowThresholdMs:      15000,
		RecentTimeoutWindowSeconds:      120,
		RecentTimeoutPenaltyThreshold:   2,
		MaxQueueWaitMs:                  90000,
		MaxQueueSize:                    1000,
		PerAPIKeyQueueLimit:             1000,
		ClientIPConcurrencyLimit:        8,
		ClientIPConcurrencyOverflowMode: "queue",
		ImageLaneMaxConcurrency:         3,
	}
	if got == nil || *got != want {
		t.Fatalf("high concurrency %s policy = %+v, want %+v", source, got, want)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal high concurrency %s policy: %v", source, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode high concurrency %s policy fields: %v", source, err)
	}
	if len(fields) != 16 {
		t.Fatalf("high concurrency %s policy field count = %d, want 16: %v", source, len(fields), fields)
	}
}

func assertW5ManagementGroupRuntimeInvalidation(
	t *testing.T,
	ctx context.Context,
	stateRedis *redisplatform.Client,
	invalidationCalls int,
	wantVersion string,
	wantPublishedAt time.Time,
) {
	t.Helper()
	wantCalls := 1
	if wantVersion == "w5-management-group-version-2" {
		wantCalls = 2
	}
	if invalidationCalls != wantCalls {
		t.Fatalf("management group invalidation calls = %d, want %d", invalidationCalls, wantCalls)
	}
	stateKey, err := gatewaycache.RuntimeStateKey(
		w5ManagementGroupCreateNamespace,
		gatewaycache.RuntimeInvalidationStoreName,
		"topic:"+gatewaycache.GatewayRuntimeCacheTopic,
	)
	if err != nil {
		t.Fatalf("build management group runtime invalidation key: %v", err)
	}
	rawState, err := stateRedis.GetRaw(ctx, stateKey)
	if err != nil {
		t.Fatalf("read management group runtime invalidation key %s: %v", stateKey, err)
	}
	var state struct {
		Version     string `json:"version"`
		Reason      string `json:"reason"`
		PublishedAt string `json:"publishedAt"`
	}
	if err := json.Unmarshal(rawState, &state); err != nil {
		t.Fatalf("decode management group runtime invalidation %s: %v", rawState, err)
	}
	wantPublished := wantPublishedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	if state.Version != wantVersion ||
		state.Reason != managementgroups.GroupCreatedReason ||
		state.PublishedAt != wantPublished {
		t.Fatalf(
			"management group runtime invalidation = %+v, want version %q reason %q publishedAt %q",
			state,
			wantVersion,
			managementgroups.GroupCreatedReason,
			wantPublished,
		)
	}
}

func readW5ManagementGroupOperationLogQueueInfo(
	t *testing.T,
	inspector *queue.Inspector,
) queue.QueueInfo {
	t.Helper()
	info, err := inspector.QueueInfo(operationlogjob.QueueName)
	if err != nil {
		t.Fatalf("read management group operation log queue info: %v", err)
	}
	if info.Pending != 0 ||
		info.Active != 0 ||
		info.Retry != 0 ||
		info.Archived != 0 {
		t.Fatalf("management group operation log queue is not drained: %+v", info)
	}
	return info
}

type w5ManagementGroupOperationLogRow struct {
	ID                            string
	TraceID                       string
	ActorSystemAccountID          string
	ActorUsername                 string
	ActorDisplayName              string
	ActorRole                     string
	OperationScopeSystemAccountID string
	Mode                          string
	Module                        string
	Action                        string
	OperationKey                  string
	ResourceType                  string
	ResourceID                    string
	ResourceName                  string
	Summary                       string
	DetailLevel                   string
	VisibilityScope               string
	ChangesJSON                   string
	MetadataJSON                  string
	Method                        string
	Path                          string
	StatusCode                    int
	ClientIP                      string
	UserAgent                     string
	CreatedAt                     time.Time
}

func assertW5ManagementGroupOperationLogs(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
) {
	t.Helper()
	admin := readW5ManagementGroupOperationLog(t, ctx, db, "oplog_w5_management_group_create_1")
	assertW5ManagementGroupOperationLog(
		t,
		admin,
		"req_w5_management_group_admin_create",
		w5ManagementGroupAdminID,
		"w5-group-create-admin",
		"W5 Group Create Admin",
		"admin",
		"admin",
		w5ManagementGroupPersonalID,
		w5ManagementGroupPersonalName,
		"openai",
		"personal",
		true,
		"/__aisys__/api/groups",
		now,
	)
	assertW5ManagementGroupOperationLogTarget(
		t,
		ctx,
		db,
		admin.ID,
		w5ManagementGroupPersonalID,
		w5ManagementGroupPersonalName,
		now,
	)
	assertW5ManagementGroupOperationLogViewers(
		t,
		ctx,
		db,
		admin.ID,
		map[string]string{
			w5ManagementGroupAdminID + "\x00actor_self":    "full",
			w5ManagementGroupUserID + "\x00resource_owner": "full",
		},
	)

	user := readW5ManagementGroupOperationLog(t, ctx, db, "oplog_w5_management_group_create_2")
	assertW5ManagementGroupOperationLog(
		t,
		user,
		"req_w5_management_group_user_create",
		w5ManagementGroupUserID,
		"w5-group-create-user",
		"W5 Group Create User",
		"user",
		"self",
		w5ManagementGroupHighID,
		w5ManagementGroupHighName,
		"gpt",
		"high_concurrency",
		false,
		"/__aisys__/api/my-groups",
		now,
	)
	assertW5ManagementGroupOperationLogTarget(
		t,
		ctx,
		db,
		user.ID,
		w5ManagementGroupHighID,
		w5ManagementGroupHighName,
		now,
	)
	assertW5ManagementGroupOperationLogViewers(
		t,
		ctx,
		db,
		user.ID,
		map[string]string{
			w5ManagementGroupUserID + "\x00actor_self":     "full",
			w5ManagementGroupUserID + "\x00resource_owner": "full",
		},
	)
}

func readW5ManagementGroupOperationLog(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	id string,
) w5ManagementGroupOperationLogRow {
	t.Helper()
	var row w5ManagementGroupOperationLogRow
	if err := db.QueryRowContext(ctx, `
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
		WHERE id = $1
	`, id).Scan(
		&row.ID,
		&row.TraceID,
		&row.ActorSystemAccountID,
		&row.ActorUsername,
		&row.ActorDisplayName,
		&row.ActorRole,
		&row.OperationScopeSystemAccountID,
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
		t.Fatalf("read management group operation log %s: %v", id, err)
	}
	return row
}

func assertW5ManagementGroupOperationLog(
	t *testing.T,
	row w5ManagementGroupOperationLogRow,
	wantTraceID string,
	wantActorID string,
	wantActorUsername string,
	wantActorDisplayName string,
	wantActorRole string,
	wantMode string,
	wantResourceID string,
	wantResourceName string,
	wantProviderCode string,
	wantGroupType string,
	wantEnabled bool,
	wantPath string,
	wantCreatedAt time.Time,
) {
	t.Helper()
	if row.TraceID != wantTraceID ||
		row.ActorSystemAccountID != wantActorID ||
		row.ActorUsername != wantActorUsername ||
		row.ActorDisplayName != wantActorDisplayName ||
		row.ActorRole != wantActorRole ||
		row.OperationScopeSystemAccountID != w5ManagementGroupUserID ||
		row.Mode != wantMode ||
		row.Module != "groups" ||
		row.Action != "create" ||
		row.OperationKey != "groups.create" ||
		row.ResourceType != "group" ||
		row.ResourceID != wantResourceID ||
		row.ResourceName != wantResourceName ||
		row.Summary != "创建分组："+wantResourceName ||
		row.DetailLevel != "full" ||
		row.VisibilityScope != "targeted" ||
		row.MetadataJSON != "{}" ||
		row.Method != http.MethodPost ||
		row.Path != wantPath ||
		row.StatusCode != http.StatusCreated ||
		row.ClientIP != "127.0.0.1" ||
		row.UserAgent != "w5-management-group-create-smoke" ||
		!row.CreatedAt.UTC().Equal(wantCreatedAt.UTC()) {
		t.Fatalf("management group operation log = %+v", row)
	}
	assertW5ManagementGroupOperationLogChanges(
		t,
		row.ChangesJSON,
		wantResourceName,
		wantProviderCode,
		wantGroupType,
		wantEnabled,
	)
}

func assertW5ManagementGroupOperationLogChanges(
	t *testing.T,
	raw string,
	wantName string,
	wantProviderCode string,
	wantGroupType string,
	wantEnabled bool,
) {
	t.Helper()
	var changes []port.OperationLogChange
	if err := json.Unmarshal([]byte(raw), &changes); err != nil {
		t.Fatalf("decode management group operation log changes %s: %v", raw, err)
	}
	want := []struct {
		field string
		after any
	}{
		{field: "name", after: wantName},
		{field: "providerCode", after: wantProviderCode},
		{field: "groupType", after: wantGroupType},
		{field: "enabled", after: wantEnabled},
	}
	if len(changes) != len(want) {
		t.Fatalf("management group operation log changes = %+v, want %d entries", changes, len(want))
	}
	for index, expected := range want {
		change := changes[index]
		if change.Field != expected.field ||
			change.Before != nil ||
			change.After != expected.after ||
			change.Sensitive {
			t.Fatalf("management group operation log change[%d] = %+v, want field %q after %#v", index, change, expected.field, expected.after)
		}
	}
}

func assertW5ManagementGroupOperationLogTarget(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	operationLogID string,
	wantGroupID string,
	wantGroupName string,
	wantCreatedAt time.Time,
) {
	t.Helper()
	var targetType string
	var targetID string
	var targetName string
	var ownerSystemAccountID string
	var relation string
	var createdAt time.Time
	if err := db.QueryRowContext(ctx, `
		SELECT
			target_type,
			target_id,
			target_name,
			target_owner_system_account_id,
			relation,
			created_at
		FROM juhe_dataset.operation_log_targets
		WHERE operation_log_id = $1
	`, operationLogID).Scan(
		&targetType,
		&targetID,
		&targetName,
		&ownerSystemAccountID,
		&relation,
		&createdAt,
	); err != nil {
		t.Fatalf("read management group operation log target %s: %v", operationLogID, err)
	}
	if targetType != "group" ||
		targetID != wantGroupID ||
		targetName != wantGroupName ||
		ownerSystemAccountID != w5ManagementGroupUserID ||
		relation != "primary" ||
		!createdAt.UTC().Equal(wantCreatedAt.UTC()) {
		t.Fatalf(
			"management group operation log target = type:%q id:%q name:%q owner:%q relation:%q created:%s",
			targetType,
			targetID,
			targetName,
			ownerSystemAccountID,
			relation,
			createdAt.UTC().Format(time.RFC3339Nano),
		)
	}
}

func assertW5ManagementGroupOperationLogViewers(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	operationLogID string,
	want map[string]string,
) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT system_account_id, visibility_reason, detail_level
		FROM juhe_dataset.operation_log_viewers
		WHERE operation_log_id = $1
		ORDER BY system_account_id, visibility_reason
	`, operationLogID)
	if err != nil {
		t.Fatalf("query management group operation log viewers %s: %v", operationLogID, err)
	}
	defer rows.Close()

	got := make(map[string]string, len(want))
	for rows.Next() {
		var systemAccountID string
		var visibilityReason string
		var detailLevel string
		if err := rows.Scan(&systemAccountID, &visibilityReason, &detailLevel); err != nil {
			t.Fatalf("scan management group operation log viewer %s: %v", operationLogID, err)
		}
		got[systemAccountID+"\x00"+visibilityReason] = detailLevel
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate management group operation log viewers %s: %v", operationLogID, err)
	}
	if len(got) != len(want) {
		t.Fatalf("management group operation log viewers %s = %+v, want %+v", operationLogID, got, want)
	}
	for key, wantDetailLevel := range want {
		if got[key] != wantDetailLevel {
			t.Fatalf("management group operation log viewer %s %q = %q, want %q", operationLogID, key, got[key], wantDetailLevel)
		}
	}
}

type w5ManagementGroupSideEffectCounts struct {
	Groups                  int
	OperationLogs           int
	OperationLogTargets     int
	OperationLogViewers     int
	OperationLogSearchTerms int
}

func readW5ManagementGroupSideEffectCounts(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) w5ManagementGroupSideEffectCounts {
	t.Helper()
	var counts w5ManagementGroupSideEffectCounts
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_business.groups
		WHERE system_account_id = $1
		  AND id LIKE 'grp_w5_management_group_create_%'
	`, w5ManagementGroupUserID).Scan(&counts.Groups); err != nil {
		t.Fatalf("count W5 management groups: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_dataset.operation_logs
		WHERE id LIKE 'oplog_w5_management_group_create_%'
	`).Scan(&counts.OperationLogs); err != nil {
		t.Fatalf("count W5 management group operation logs: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_dataset.operation_log_targets
		WHERE operation_log_id LIKE 'oplog_w5_management_group_create_%'
	`).Scan(&counts.OperationLogTargets); err != nil {
		t.Fatalf("count W5 management group operation log targets: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_dataset.operation_log_viewers
		WHERE operation_log_id LIKE 'oplog_w5_management_group_create_%'
	`).Scan(&counts.OperationLogViewers); err != nil {
		t.Fatalf("count W5 management group operation log viewers: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_dataset.operation_log_summary_search_terms
		WHERE operation_log_id LIKE 'oplog_w5_management_group_create_%'
	`).Scan(&counts.OperationLogSearchTerms); err != nil {
		t.Fatalf("count W5 management group operation log search terms: %v", err)
	}
	return counts
}

func assertW5ManagementGroupCreateCounts(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	want w5ManagementGroupSideEffectCounts,
) {
	t.Helper()
	if got := readW5ManagementGroupSideEffectCounts(t, ctx, db); got != want {
		t.Fatalf("W5 management group side effect counts = %+v, want %+v", got, want)
	}
}

func assertW5ManagementGroupMissing(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	groupID string,
) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_business.groups
		WHERE id = $1
	`, groupID).Scan(&count); err != nil {
		t.Fatalf("count missing W5 management group %s: %v", groupID, err)
	}
	if count != 0 {
		t.Fatalf("W5 management group %s count = %d, want 0", groupID, count)
	}
}
