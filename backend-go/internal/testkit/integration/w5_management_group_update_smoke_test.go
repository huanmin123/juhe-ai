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
	"juhe-ai/backend-go/internal/modules/managementgroups"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w5ManagementGroupUpdateNamespace = "w5-management-group-update"

	w5ManagementGroupUpdateAdminID    = "sys_w5_group_update_admin"
	w5ManagementGroupUpdateOwnerID    = "sys_w5_group_update_owner"
	w5ManagementGroupUpdateGranteeAID = "sys_w5_group_update_grantee_a"
	w5ManagementGroupUpdateGranteeBID = "sys_w5_group_update_grantee_b"

	w5ManagementGroupUpdateAdminSession    = "sess_w5_group_update_admin"
	w5ManagementGroupUpdateGranteeASession = "sess_w5_group_update_grantee_a"
	w5ManagementGroupUpdateGranteeBSession = "sess_w5_group_update_grantee_b"
	w5ManagementGroupUpdateAdminToken      = "w5-group-update-admin-session"
	w5ManagementGroupUpdateGranteeAToken   = "w5-group-update-grantee-a-session"
	w5ManagementGroupUpdateGranteeBToken   = "w5-group-update-grantee-b-session"

	w5ManagementGroupUpdateTargetID    = "grp_w5_group_update_target"
	w5ManagementGroupUpdateAlternateID = "grp_w5_group_update_alternate"
	w5ManagementGroupUpdateAuthAID     = "rauth_w5_group_update_a"
	w5ManagementGroupUpdateAuthBID     = "rauth_w5_group_update_b"
)

func TestW5ManagementGroupUpdatePostgresRedisSmoke(t *testing.T) {
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
	stateRedis, err := redisplatform.NewClient(
		w3RedisURLWithDB(t, redisURL, 1),
		w5ManagementGroupUpdateNamespace+":state",
	)
	if err != nil {
		t.Fatalf("open state redis: %v", err)
	}
	defer closeRedisClient(t, stateRedis)
	cacheRedis, err := redisplatform.NewClient(
		w3RedisURLWithDB(t, redisURL, 2),
		w5ManagementGroupUpdateNamespace+":cache",
	)
	if err != nil {
		t.Fatalf("open cache redis: %v", err)
	}
	defer closeRedisClient(t, cacheRedis)
	accountConcurrency, err := redisplatform.NewAccountConcurrencyReader(
		stateRedis,
		w5ManagementGroupUpdateNamespace,
	)
	if err != nil {
		t.Fatalf("create account concurrency reader: %v", err)
	}

	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	insertW5ManagementGroupUpdateFixtures(t, ctx, db, now)
	sessionCreatedAt := now.Add(-5 * time.Minute)
	insertW2ManagementSessionForAccountFixture(
		t, ctx, db,
		w5ManagementGroupUpdateAdminSession,
		w5ManagementGroupUpdateAdminID,
		w5ManagementGroupUpdateAdminToken,
		sessionCreatedAt,
	)
	insertW2ManagementSessionForAccountFixture(
		t, ctx, db,
		w5ManagementGroupUpdateGranteeASession,
		w5ManagementGroupUpdateGranteeAID,
		w5ManagementGroupUpdateGranteeAToken,
		sessionCreatedAt,
	)
	insertW2ManagementSessionForAccountFixture(
		t, ctx, db,
		w5ManagementGroupUpdateGranteeBSession,
		w5ManagementGroupUpdateGranteeBID,
		w5ManagementGroupUpdateGranteeBToken,
		sessionCreatedAt,
	)

	versionCalls := 0
	invalidator, err := gatewaycache.NewSystemAccountInvalidator(gatewaycache.SystemAccountInvalidatorOptions{
		Cache:     cacheRedis,
		State:     stateRedis,
		Namespace: w5ManagementGroupUpdateNamespace,
		Now:       func() time.Time { return now },
		NewVersion: func(time.Time) (string, error) {
			versionCalls++
			return fmt.Sprintf("w5-management-group-update-version-%d", versionCalls), nil
		},
	})
	if err != nil {
		t.Fatalf("create group update invalidator: %v", err)
	}

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	service := managementgroups.NewServiceWithOptions(managementgroups.ServiceOptions{
		Store:              store,
		Invalidator:        invalidator,
		AccountConcurrency: accountConcurrency,
		Logger:             logger,
		Now:                func() time.Time { return now },
	})
	operationLogs := &w5ManagementGroupUpdateOperationLogQueue{}
	logIDCalls := 0
	cfg := config.Config{
		Host:                 "127.0.0.1",
		Port:                 3000,
		ManagementAPIEnabled: true,
		TrustProxy:           "false",
	}
	logOptions := httpapi.ManagementOperationLogOptions{
		Config:         cfg,
		Logger:         logger,
		Client:         operationLogs,
		SettingsReader: store,
		Now:            func() time.Time { return now },
		NewLogID: func() string {
			logIDCalls++
			return fmt.Sprintf("oplog_w5_management_group_update_%d", logIDCalls)
		},
	}
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                           cfg,
		Logger:                           logger,
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementGroupUpdateHandler: httpapi.NewManagementGroupUpdateHandlerWithOperationLog(
			service,
			logOptions,
		),
		ManagementMyGroupUpdateHandler: httpapi.NewManagementMyGroupUpdateHandlerWithOperationLog(
			service,
			logOptions,
		),
		ManagementGroupDetailHandler:   httpapi.NewManagementGroupDetailHandler(service),
		ManagementMyGroupDetailHandler: httpapi.NewManagementMyGroupDetailHandler(service),
	})

	ownerRec := serveW5ManagementGroupUpdateRequest(
		router,
		http.MethodPatch,
		"/__aisys__/api/groups/"+w5ManagementGroupUpdateTargetID,
		w5ManagementGroupUpdateAdminToken,
		`{"name":" W5 Owner Updated ","providerCode":" gpt ","description":" owner persisted "}`,
		"req_w5_group_update_owner",
	)
	owner := decodeW5ManagementGroupUpdateDetail(t, ownerRec, http.StatusOK)
	assertW5ManagementGroupUpdateOwnerDetail(t, owner, true)
	assertW5ManagementGroupUpdateBaseRow(t, ctx, db, now)
	assertW5ManagementGroupUpdateInvalidation(
		t, ctx, cacheRedis, stateRedis,
		"w5-management-group-update-version-1",
		"w5-management-group-update-version-2",
		managementgroups.GroupUpdatedReason,
		now,
	)

	forbiddenRec := serveW5ManagementGroupUpdateRequest(
		router,
		http.MethodPatch,
		"/__aisys__/api/my-groups/"+w5ManagementGroupUpdateTargetID,
		w5ManagementGroupUpdateGranteeAToken,
		`{"name":"grantee must not rename owner group"}`,
		"req_w5_group_update_grantee_forbidden",
	)
	assertW5ManagementGroupUpdateError(
		t,
		forbiddenRec,
		http.StatusBadRequest,
		"授权分组使用配置包含未知字段：name",
	)
	assertW5ManagementGroupUpdateAuthorizationSettingsMissing(
		t,
		ctx,
		db,
		w5ManagementGroupUpdateAuthAID,
	)

	granteeConfigRec := serveW5ManagementGroupUpdateRequest(
		router,
		http.MethodPatch,
		"/__aisys__/api/my-groups/"+w5ManagementGroupUpdateTargetID,
		w5ManagementGroupUpdateGranteeAToken,
		`{
			"enabled":true,
			"groupType":"high_concurrency",
			"schedulingPolicy":{
				"defaultSoftConcurrency":17,
				"maxQueueWaitMs":45000,
				"clientIpConcurrencyLimit":6,
				"clientIpConcurrencyOverflowMode":"reject",
				"imageLaneMaxConcurrency":4
			}
		}`,
		"req_w5_group_update_grantee_config",
	)
	granteeConfig := decodeW5ManagementGroupUpdateDetail(t, granteeConfigRec, http.StatusOK)
	assertW5ManagementGroupUpdateAuthorizedDetail(t, granteeConfig, w5ManagementGroupUpdateAuthAID, true)
	assertW5ManagementGroupUpdateAuthorizationSettings(
		t,
		ctx,
		db,
		w5ManagementGroupUpdateAuthAID,
		w5ManagementGroupUpdateGranteeAID,
		true,
		now,
	)
	assertW5ManagementGroupUpdateBaseRow(t, ctx, db, now)
	assertW5ManagementGroupUpdateRuntimeInvalidation(
		t,
		ctx,
		stateRedis,
		"w5-management-group-update-version-3",
		managementgroups.GroupAuthorizationSettingsUpdatedReason,
		now,
	)

	granteeDisableRec := serveW5ManagementGroupUpdateRequest(
		router,
		http.MethodPatch,
		"/__aisys__/api/my-groups/"+w5ManagementGroupUpdateTargetID,
		w5ManagementGroupUpdateGranteeAToken,
		`{"enabled":false}`,
		"req_w5_group_update_grantee_disable",
	)
	granteeDisabled := decodeW5ManagementGroupUpdateDetail(t, granteeDisableRec, http.StatusOK)
	assertW5ManagementGroupUpdateAuthorizedDetail(t, granteeDisabled, w5ManagementGroupUpdateAuthAID, false)
	assertW5ManagementGroupUpdateAuthorizationSettings(
		t,
		ctx,
		db,
		w5ManagementGroupUpdateAuthAID,
		w5ManagementGroupUpdateGranteeAID,
		false,
		now,
	)
	assertW5ManagementGroupUpdateRuntimeInvalidation(
		t,
		ctx,
		stateRedis,
		"w5-management-group-update-version-4",
		managementgroups.GroupAuthorizationSettingsUpdatedReason,
		now,
	)

	granteeBDisableRec := serveW5ManagementGroupUpdateRequest(
		router,
		http.MethodPatch,
		"/__aisys__/api/my-groups/"+w5ManagementGroupUpdateTargetID,
		w5ManagementGroupUpdateGranteeBToken,
		`{"enabled":false}`,
		"req_w5_group_update_grantee_b_disable",
	)
	assertW5ManagementGroupUpdateError(
		t,
		granteeBDisableRec,
		http.StatusBadRequest,
		"无法停用授权分组“W5 Owner Updated”：该分组仍是当前范围内活跃策略路由的唯一可用启用分组",
	)
	assertW5ManagementGroupUpdateAuthorizationSettingsMissing(
		t,
		ctx,
		db,
		w5ManagementGroupUpdateAuthBID,
	)

	ownerDisableRec := serveW5ManagementGroupUpdateRequest(
		router,
		http.MethodPatch,
		"/__aisys__/api/groups/"+w5ManagementGroupUpdateTargetID,
		w5ManagementGroupUpdateAdminToken,
		`{"enabled":false}`,
		"req_w5_group_update_owner_disable",
	)
	assertW5ManagementGroupUpdateError(
		t,
		ownerDisableRec,
		http.StatusBadRequest,
		"无法停用分组“W5 Owner Updated”：该分组仍是当前范围内活跃策略路由的唯一可用启用分组",
	)

	ownerAfterGuard := requestW5ManagementGroupUpdateDetail(
		t,
		router,
		"/__aisys__/api/groups/"+w5ManagementGroupUpdateTargetID,
		w5ManagementGroupUpdateAdminToken,
	)
	assertW5ManagementGroupUpdateOwnerDetail(t, ownerAfterGuard, true)
	granteeAAfterGuard := requestW5ManagementGroupUpdateDetail(
		t,
		router,
		"/__aisys__/api/my-groups/"+w5ManagementGroupUpdateTargetID,
		w5ManagementGroupUpdateGranteeAToken,
	)
	assertW5ManagementGroupUpdateAuthorizedDetail(
		t,
		granteeAAfterGuard,
		w5ManagementGroupUpdateAuthAID,
		false,
	)
	granteeBAfterGuard := requestW5ManagementGroupUpdateDetail(
		t,
		router,
		"/__aisys__/api/my-groups/"+w5ManagementGroupUpdateTargetID,
		w5ManagementGroupUpdateGranteeBToken,
	)
	assertW5ManagementGroupUpdateAuthorizedPersonalDetail(
		t,
		granteeBAfterGuard,
		w5ManagementGroupUpdateAuthBID,
	)
	assertW5ManagementGroupUpdateBaseRow(t, ctx, db, now)
	assertW5ManagementGroupUpdateRuntimeInvalidation(
		t,
		ctx,
		stateRedis,
		"w5-management-group-update-version-4",
		managementgroups.GroupAuthorizationSettingsUpdatedReason,
		now,
	)
	if versionCalls != 4 {
		t.Fatalf("group update invalidation version calls = %d, want 4", versionCalls)
	}
	if logIDCalls != 3 {
		t.Fatalf("group update operation log id calls = %d, want 3", logIDCalls)
	}
	assertW5ManagementGroupUpdateOperationLogs(t, operationLogs)
}

func serveW5ManagementGroupUpdateRequest(
	router http.Handler,
	method string,
	target string,
	sessionToken string,
	body string,
	requestID string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Cookie", managementauth.SessionCookieName+"="+sessionToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "w5-management-group-update-smoke")
	req.Header.Set("X-Request-Id", requestID)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeW5ManagementGroupUpdateDetail(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	wantStatus int,
) managementgroups.DetailResult {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("group update status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("group update Cache-Control = %q, want no-store", got)
	}
	var envelope struct {
		Data managementgroups.DetailResult `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode group update response: %v", err)
	}
	return envelope.Data
}

func requestW5ManagementGroupUpdateDetail(
	t *testing.T,
	router http.Handler,
	target string,
	sessionToken string,
) managementgroups.DetailResult {
	t.Helper()
	rec := serveW5ManagementGroupUpdateRequest(
		router,
		http.MethodGet,
		target,
		sessionToken,
		"",
		"",
	)
	return decodeW5ManagementGroupUpdateDetail(t, rec, http.StatusOK)
}

func assertW5ManagementGroupUpdateError(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	wantStatus int,
	wantMessage string,
) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("group update error status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode group update error: %v", err)
	}
	if body["message"] != wantMessage {
		t.Fatalf("group update error message = %q, want %q", body["message"], wantMessage)
	}
}

func assertW5ManagementGroupUpdateOwnerDetail(
	t *testing.T,
	got managementgroups.DetailResult,
	wantEnabled bool,
) {
	t.Helper()
	if got.ID != w5ManagementGroupUpdateTargetID ||
		got.SystemAccountID != w5ManagementGroupUpdateOwnerID ||
		got.OwnerSystemAccountID != w5ManagementGroupUpdateOwnerID ||
		got.Name != "W5 Owner Updated" ||
		got.ProviderCode != "gpt" ||
		got.Description == nil ||
		*got.Description != "owner persisted" ||
		got.Enabled != wantEnabled ||
		got.IsDefault ||
		got.GroupType != "personal" ||
		got.SchedulingPolicy != nil ||
		got.AccessType != "owner" ||
		got.GroupAuthorizationID != "" ||
		len(got.AccountIDs) != 0 {
		t.Fatalf("owner group detail = %+v", got)
	}
}

func assertW5ManagementGroupUpdateAuthorizedDetail(
	t *testing.T,
	got managementgroups.DetailResult,
	wantAuthorizationID string,
	wantEnabled bool,
) {
	t.Helper()
	if got.ID != w5ManagementGroupUpdateTargetID ||
		got.SystemAccountID != "" ||
		got.OwnerSystemAccountID != w5ManagementGroupUpdateOwnerID ||
		got.Name != "W5 Owner Updated" ||
		got.ProviderCode != "gpt" ||
		got.Description == nil ||
		*got.Description != "owner persisted" ||
		got.Enabled != wantEnabled ||
		got.IsDefault ||
		got.GroupType != "high_concurrency" ||
		got.AccessType != "authorized" ||
		got.GroupAuthorizationID != wantAuthorizationID ||
		got.AuthorizationStatus != "active" ||
		len(got.AccountIDs) != 0 {
		t.Fatalf("authorized group detail = %+v", got)
	}
	assertW5ManagementGroupUpdatePolicy(t, got.SchedulingPolicy)
}

func assertW5ManagementGroupUpdateAuthorizedPersonalDetail(
	t *testing.T,
	got managementgroups.DetailResult,
	wantAuthorizationID string,
) {
	t.Helper()
	if got.ID != w5ManagementGroupUpdateTargetID ||
		got.SystemAccountID != "" ||
		got.OwnerSystemAccountID != w5ManagementGroupUpdateOwnerID ||
		got.Name != "W5 Owner Updated" ||
		got.ProviderCode != "gpt" ||
		got.Description == nil ||
		*got.Description != "owner persisted" ||
		!got.Enabled ||
		got.IsDefault ||
		got.GroupType != "personal" ||
		got.SchedulingPolicy != nil ||
		got.AccessType != "authorized" ||
		got.GroupAuthorizationID != wantAuthorizationID ||
		got.AuthorizationStatus != "active" ||
		len(got.AccountIDs) != 0 {
		t.Fatalf("authorized personal group detail = %+v", got)
	}
}

func assertW5ManagementGroupUpdatePolicy(
	t *testing.T,
	got *managementgroups.SchedulingPolicy,
) {
	t.Helper()
	want := managementgroups.SchedulingPolicy{
		Mode:                            "balanced_fast",
		DefaultSoftConcurrency:          17,
		FastFirstEnabled:                true,
		FallbackOnQueueEnabled:          true,
		BreakAffinityOnSoftLimit:        true,
		BreakAffinityOnQueueWaitMs:      0,
		SlowRequestThresholdMs:          30000,
		FirstOutputSlowThresholdMs:      15000,
		RecentTimeoutWindowSeconds:      120,
		RecentTimeoutPenaltyThreshold:   2,
		MaxQueueWaitMs:                  45000,
		MaxQueueSize:                    1000,
		PerAPIKeyQueueLimit:             1000,
		ClientIPConcurrencyLimit:        6,
		ClientIPConcurrencyOverflowMode: "reject",
		ImageLaneMaxConcurrency:         4,
	}
	if got == nil || *got != want {
		t.Fatalf("authorized scheduling policy = %+v, want %+v", got, want)
	}
}

func insertW5ManagementGroupUpdateFixtures(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES
			($1, 'w5-group-update-admin', 'W5 Group Update Admin', NULL, 'admin', 'active', 'hash', false, false, $5, $5),
			($2, 'w5-group-update-owner', 'W5 Group Update Owner', NULL, 'user', 'active', 'hash', false, false, $5, $5),
			($3, 'w5-group-update-grantee-a', 'W5 Group Update Grantee A', NULL, 'user', 'active', 'hash', false, false, $5, $5),
			($4, 'w5-group-update-grantee-b', 'W5 Group Update Grantee B', NULL, 'user', 'active', 'hash', false, false, $5, $5)
	`, w5ManagementGroupUpdateAdminID, w5ManagementGroupUpdateOwnerID,
		w5ManagementGroupUpdateGranteeAID, w5ManagementGroupUpdateGranteeBID, now); err != nil {
		t.Fatalf("insert W5 management group update accounts: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.groups (
			id, system_account_id, name, provider_code, description, enabled, is_default,
			group_type, scheduling_policy_json, created_at, updated_at
		) VALUES
			($1, $3, 'W5 Owner Original', 'openai', 'owner original', true, false, 'personal', NULL, $5, $5),
			($2, $4, 'W5 Grantee A Alternate', 'openai', 'guard alternate', true, false, 'personal', NULL, $5, $5)
	`, w5ManagementGroupUpdateTargetID, w5ManagementGroupUpdateAlternateID,
		w5ManagementGroupUpdateOwnerID, w5ManagementGroupUpdateGranteeAID, now); err != nil {
		t.Fatalf("insert W5 management group update groups: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.resource_authorizations (
			id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
			scope, status, effective_source_type, effective_source_team_id, activated_at, last_source_changed_at,
			remark, expires_at, limits_json, created_by, created_at, revoked_by, revoked_at,
			revoked_reason, updated_at
		) VALUES
			($1, 'group', $3, $4, $5, 'use', 'active', 'manual', NULL, $7, $7, NULL, $8, NULL, $4, $7, NULL, NULL, NULL, $7),
			($2, 'group', $3, $4, $6, 'use', 'active', 'manual', NULL, $7, $7, NULL, $8, NULL, $4, $7, NULL, NULL, NULL, $7)
	`, w5ManagementGroupUpdateAuthAID, w5ManagementGroupUpdateAuthBID,
		w5ManagementGroupUpdateTargetID, w5ManagementGroupUpdateOwnerID,
		w5ManagementGroupUpdateGranteeAID, w5ManagementGroupUpdateGranteeBID,
		now.Add(-time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatalf("insert W5 management group update authorizations: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.route_strategies (
			id, system_account_id, name, description, mode, status, is_default,
			config_json, created_at, updated_at
		) VALUES
			('route_w5_group_update_a', $1, 'W5 Group Update Route A', NULL, 'weighted', 'active', false, NULL, $3, $3),
			('route_w5_group_update_b', $2, 'W5 Group Update Route B', NULL, 'normal', 'active', false, NULL, $3, $3)
	`, w5ManagementGroupUpdateGranteeAID, w5ManagementGroupUpdateGranteeBID, now); err != nil {
		t.Fatalf("insert W5 management group update route strategies: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.route_strategy_groups (
			id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at
		) VALUES
			('rsg_w5_group_update_a_target', 'route_w5_group_update_a', $1, $3, 1, 50, 'active', $5, $5),
			('rsg_w5_group_update_a_alternate', 'route_w5_group_update_a', $1, $4, 2, 50, 'active', $5, $5),
			('rsg_w5_group_update_b_target', 'route_w5_group_update_b', $2, $3, 1, 1, 'active', $5, $5)
	`, w5ManagementGroupUpdateGranteeAID, w5ManagementGroupUpdateGranteeBID,
		w5ManagementGroupUpdateTargetID, w5ManagementGroupUpdateAlternateID, now); err != nil {
		t.Fatalf("insert W5 management group update route bindings: %v", err)
	}
}

func assertW5ManagementGroupUpdateBaseRow(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	wantUpdatedAt time.Time,
) {
	t.Helper()
	var (
		name             string
		providerCode     string
		description      sql.NullString
		enabled          bool
		groupType        string
		schedulingPolicy sql.NullString
		updatedAt        time.Time
	)
	if err := db.QueryRowContext(ctx, `
		SELECT name, provider_code, description, enabled, group_type, scheduling_policy_json, updated_at
		FROM juhe_business.groups
		WHERE id = $1
	`, w5ManagementGroupUpdateTargetID).Scan(
		&name,
		&providerCode,
		&description,
		&enabled,
		&groupType,
		&schedulingPolicy,
		&updatedAt,
	); err != nil {
		t.Fatalf("read W5 management group update base row: %v", err)
	}
	if name != "W5 Owner Updated" ||
		providerCode != "gpt" ||
		description != (sql.NullString{String: "owner persisted", Valid: true}) ||
		!enabled ||
		groupType != "personal" ||
		schedulingPolicy.Valid ||
		!updatedAt.UTC().Equal(wantUpdatedAt.UTC()) {
		t.Fatalf(
			"group update base row name=%q provider=%q description=%+v enabled=%t type=%q policy=%+v updatedAt=%s",
			name,
			providerCode,
			description,
			enabled,
			groupType,
			schedulingPolicy,
			updatedAt.UTC().Format(time.RFC3339Nano),
		)
	}
}

func assertW5ManagementGroupUpdateAuthorizationSettings(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	authorizationID string,
	systemAccountID string,
	wantEnabled bool,
	wantUpdatedAt time.Time,
) {
	t.Helper()
	var (
		gotSystemAccountID string
		groupID            string
		enabled            bool
		groupType          string
		policyJSON         string
		createdAt          time.Time
		updatedAt          time.Time
	)
	if err := db.QueryRowContext(ctx, `
		SELECT system_account_id, group_id, enabled, group_type, scheduling_policy_json, created_at, updated_at
		FROM juhe_business.group_authorization_settings
		WHERE authorization_id = $1
	`, authorizationID).Scan(
		&gotSystemAccountID,
		&groupID,
		&enabled,
		&groupType,
		&policyJSON,
		&createdAt,
		&updatedAt,
	); err != nil {
		t.Fatalf("read group authorization settings %s: %v", authorizationID, err)
	}
	var policy managementgroups.SchedulingPolicy
	if err := json.Unmarshal([]byte(policyJSON), &policy); err != nil {
		t.Fatalf("decode group authorization settings policy %s: %v", policyJSON, err)
	}
	if gotSystemAccountID != systemAccountID ||
		groupID != w5ManagementGroupUpdateTargetID ||
		enabled != wantEnabled ||
		groupType != "high_concurrency" ||
		!createdAt.UTC().Equal(wantUpdatedAt.UTC()) ||
		!updatedAt.UTC().Equal(wantUpdatedAt.UTC()) {
		t.Fatalf(
			"group authorization settings auth=%s account=%s group=%s enabled=%t type=%s createdAt=%s updatedAt=%s",
			authorizationID,
			gotSystemAccountID,
			groupID,
			enabled,
			groupType,
			createdAt.UTC().Format(time.RFC3339Nano),
			updatedAt.UTC().Format(time.RFC3339Nano),
		)
	}
	assertW5ManagementGroupUpdatePolicy(t, &policy)
}

func assertW5ManagementGroupUpdateAuthorizationSettingsMissing(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	authorizationID string,
) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_business.group_authorization_settings
		WHERE authorization_id = $1
	`, authorizationID).Scan(&count); err != nil {
		t.Fatalf("count group authorization settings %s: %v", authorizationID, err)
	}
	if count != 0 {
		t.Fatalf("group authorization settings %s count = %d, want 0", authorizationID, count)
	}
}

func assertW5ManagementGroupUpdateInvalidation(
	t *testing.T,
	ctx context.Context,
	cacheRedis *redisplatform.Client,
	stateRedis *redisplatform.Client,
	wantLookupVersion string,
	wantRuntimeVersion string,
	wantReason string,
	wantPublishedAt time.Time,
) {
	t.Helper()
	key, err := gatewaycache.SharedCacheVersionKey(
		w5ManagementGroupUpdateNamespace,
		gatewaycache.GroupLookupCacheName,
	)
	if err != nil {
		t.Fatalf("build group lookup cache version key: %v", err)
	}
	value, err := cacheRedis.GetRaw(ctx, key)
	if err != nil {
		t.Fatalf("read group lookup cache version: %v", err)
	}
	if string(value) != wantLookupVersion {
		t.Fatalf("group lookup cache version = %q, want %q", value, wantLookupVersion)
	}
	assertW5ManagementGroupUpdateRuntimeInvalidation(
		t,
		ctx,
		stateRedis,
		wantRuntimeVersion,
		wantReason,
		wantPublishedAt,
	)
}

func assertW5ManagementGroupUpdateRuntimeInvalidation(
	t *testing.T,
	ctx context.Context,
	stateRedis *redisplatform.Client,
	wantVersion string,
	wantReason string,
	wantPublishedAt time.Time,
) {
	t.Helper()
	key, err := gatewaycache.RuntimeStateKey(
		w5ManagementGroupUpdateNamespace,
		gatewaycache.RuntimeInvalidationStoreName,
		"topic:"+gatewaycache.GatewayRuntimeCacheTopic,
	)
	if err != nil {
		t.Fatalf("build group update runtime invalidation key: %v", err)
	}
	raw, err := stateRedis.GetRaw(ctx, key)
	if err != nil {
		t.Fatalf("read group update runtime invalidation: %v", err)
	}
	var state struct {
		Version     string `json:"version"`
		Reason      string `json:"reason"`
		PublishedAt string `json:"publishedAt"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode group update runtime invalidation %s: %v", raw, err)
	}
	if state.Version != wantVersion ||
		state.Reason != wantReason ||
		state.PublishedAt != wantPublishedAt.UTC().Format("2006-01-02T15:04:05.000Z") {
		t.Fatalf(
			"group update runtime invalidation = %+v, want version=%q reason=%q publishedAt=%q",
			state,
			wantVersion,
			wantReason,
			wantPublishedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		)
	}
}

type w5ManagementGroupUpdateOperationLogQueue struct {
	logs []port.OperationLogInput
}

func (q *w5ManagementGroupUpdateOperationLogQueue) Enqueue(
	_ context.Context,
	taskType string,
	payload []byte,
	opts queue.EnqueueOptions,
) (queue.TaskInfo, error) {
	if taskType != operationlogjob.TaskTypeWrite {
		return queue.TaskInfo{}, fmt.Errorf("unexpected operation log task type %q", taskType)
	}
	if opts.Queue != operationlogjob.QueueName {
		return queue.TaskInfo{}, fmt.Errorf("unexpected operation log queue %q", opts.Queue)
	}
	input, err := operationlogjob.DecodeWriteTaskPayload(payload)
	if err != nil {
		return queue.TaskInfo{}, err
	}
	q.logs = append(q.logs, input)
	return queue.TaskInfo{
		ID:    fmt.Sprintf("task_w5_management_group_update_%d", len(q.logs)),
		Queue: opts.Queue,
		Type:  taskType,
	}, nil
}

func assertW5ManagementGroupUpdateOperationLogs(
	t *testing.T,
	queueStub *w5ManagementGroupUpdateOperationLogQueue,
) {
	t.Helper()
	if len(queueStub.logs) != 3 {
		t.Fatalf("group update operation logs = %d, want 3: %+v", len(queueStub.logs), queueStub.logs)
	}
	owner := queueStub.logs[0]
	if owner.ID != "oplog_w5_management_group_update_1" ||
		owner.TraceID != "req_w5_group_update_owner" ||
		owner.ActorSystemAccountID != w5ManagementGroupUpdateAdminID ||
		owner.OperationScopeSystemAccountID != w5ManagementGroupUpdateOwnerID ||
		owner.Mode != "admin" ||
		owner.OperationKey != "groups.update" ||
		owner.ResourceID != w5ManagementGroupUpdateTargetID ||
		owner.ResourceName != "W5 Owner Updated" ||
		owner.Path != "/__aisys__/api/groups/"+w5ManagementGroupUpdateTargetID ||
		len(owner.Changes) != 3 {
		t.Fatalf("owner group update operation log = %+v", owner)
	}
	configLog := queueStub.logs[1]
	if configLog.ID != "oplog_w5_management_group_update_2" ||
		configLog.TraceID != "req_w5_group_update_grantee_config" ||
		configLog.ActorSystemAccountID != w5ManagementGroupUpdateGranteeAID ||
		configLog.OperationScopeSystemAccountID != w5ManagementGroupUpdateGranteeAID ||
		configLog.Mode != "self" ||
		configLog.Summary != "更新授权分组使用配置：W5 Owner Updated" ||
		configLog.Path != "/__aisys__/api/my-groups/"+w5ManagementGroupUpdateTargetID ||
		len(configLog.Changes) != 2 ||
		configLog.Changes[0].Field != "groupType" ||
		configLog.Changes[1].Field != "schedulingPolicy" {
		t.Fatalf("grantee config operation log = %+v", configLog)
	}
	disableLog := queueStub.logs[2]
	if disableLog.ID != "oplog_w5_management_group_update_3" ||
		disableLog.TraceID != "req_w5_group_update_grantee_disable" ||
		disableLog.ActorSystemAccountID != w5ManagementGroupUpdateGranteeAID ||
		len(disableLog.Changes) != 1 ||
		disableLog.Changes[0].Field != "enabled" ||
		disableLog.Changes[0].Before != true ||
		disableLog.Changes[0].After != false {
		t.Fatalf("grantee disable operation log = %+v", disableLog)
	}
	for index, logInput := range queueStub.logs {
		if logInput.StatusCode == nil || *logInput.StatusCode != http.StatusOK {
			t.Fatalf("operation log %d status = %+v, want 200", index, logInput.StatusCode)
		}
		if logInput.Module != "groups" ||
			logInput.Action != "update" ||
			logInput.ResourceType != "group" ||
			logInput.ClientIP != "127.0.0.1" {
			t.Fatalf("operation log %d common fields = %+v", index, logInput)
		}
	}
}
