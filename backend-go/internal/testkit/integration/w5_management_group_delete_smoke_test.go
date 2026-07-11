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
	w5ManagementGroupDeleteNamespace = "w5-management-group-delete"

	w5ManagementGroupDeleteAdminID   = "sys_w5_group_delete_admin"
	w5ManagementGroupDeleteOwnerID   = "sys_w5_group_delete_owner"
	w5ManagementGroupDeleteGranteeID = "sys_w5_group_delete_grantee"

	w5ManagementGroupDeleteAdminSession   = "sess_w5_group_delete_admin"
	w5ManagementGroupDeleteOwnerSession   = "sess_w5_group_delete_owner"
	w5ManagementGroupDeleteGranteeSession = "sess_w5_group_delete_grantee"
	w5ManagementGroupDeleteAdminToken     = "w5-group-delete-admin-session"
	w5ManagementGroupDeleteOwnerToken     = "w5-group-delete-owner-session"
	w5ManagementGroupDeleteGranteeToken   = "w5-group-delete-grantee-session"

	w5ManagementGroupDeleteCascadeID         = "grp_w5_group_delete_cascade"
	w5ManagementGroupDeleteCascadeAlternate  = "grp_w5_group_delete_cascade_alternate"
	w5ManagementGroupDeleteSelfID            = "grp_w5_group_delete_self"
	w5ManagementGroupDeleteAuthorizedID      = "grp_w5_group_delete_authorized"
	w5ManagementGroupDeleteDefaultID         = "grp_w5_group_delete_default"
	w5ManagementGroupDeleteGuardID           = "grp_w5_group_delete_guard"
	w5ManagementGroupDeleteGuardOwnerAltID   = "grp_w5_group_delete_guard_owner_alt"
	w5ManagementGroupDeleteGuardGranteeAltID = "grp_w5_group_delete_guard_grantee_alt"

	w5ManagementGroupDeleteCascadeAuthID    = "rauth_w5_group_delete_cascade"
	w5ManagementGroupDeleteAuthorizedAuthID = "rauth_w5_group_delete_authorized"
	w5ManagementGroupDeleteGuardAuthID      = "rauth_w5_group_delete_guard"
	w5ManagementGroupDeleteCascadeGrantID   = "rgrant_w5_group_delete_cascade"
	w5ManagementGroupDeleteCascadeSourceID  = "rsource_w5_group_delete_cascade"

	w5ManagementGroupDeleteCascadeRouteID  = "route_w5_group_delete_cascade"
	w5ManagementGroupDeleteGuardOwnerRoute = "route_w5_group_delete_guard_owner"
	w5ManagementGroupDeleteGuardUserRoute  = "route_w5_group_delete_guard_grantee"
)

func TestW5ManagementGroupDeletePostgresRedisSmoke(t *testing.T) {
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
		w5ManagementGroupDeleteNamespace+":state",
	)
	if err != nil {
		t.Fatalf("open state redis: %v", err)
	}
	defer closeRedisClient(t, stateRedis)
	cacheRedis, err := redisplatform.NewClient(
		w3RedisURLWithDB(t, redisURL, 2),
		w5ManagementGroupDeleteNamespace+":cache",
	)
	if err != nil {
		t.Fatalf("open cache redis: %v", err)
	}
	defer closeRedisClient(t, cacheRedis)

	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	insertW5ManagementGroupDeleteFixtures(t, ctx, db, now)
	sessionCreatedAt := now.Add(-10 * time.Minute)
	insertW2ManagementSessionForAccountFixture(
		t, ctx, db,
		w5ManagementGroupDeleteAdminSession,
		w5ManagementGroupDeleteAdminID,
		w5ManagementGroupDeleteAdminToken,
		sessionCreatedAt,
	)
	insertW2ManagementSessionForAccountFixture(
		t, ctx, db,
		w5ManagementGroupDeleteOwnerSession,
		w5ManagementGroupDeleteOwnerID,
		w5ManagementGroupDeleteOwnerToken,
		sessionCreatedAt,
	)
	insertW2ManagementSessionForAccountFixture(
		t, ctx, db,
		w5ManagementGroupDeleteGranteeSession,
		w5ManagementGroupDeleteGranteeID,
		w5ManagementGroupDeleteGranteeToken,
		sessionCreatedAt,
	)

	lookupKey := w5ManagementGroupDeleteSharedCacheKey(
		t,
		gatewaycache.GroupLookupCacheName,
	)
	accountIDsKey := w5ManagementGroupDeleteSharedCacheKey(
		t,
		gatewaycache.GroupAccountIDsCacheName,
	)
	if err := cacheRedis.SetRaw(ctx, lookupKey, []byte("delete-seed-lookup"), time.Hour); err != nil {
		t.Fatalf("seed group lookup cache version: %v", err)
	}
	if err := cacheRedis.SetRaw(ctx, accountIDsKey, []byte("delete-seed-account-ids"), time.Hour); err != nil {
		t.Fatalf("seed group account IDs cache version: %v", err)
	}

	versionCalls := 0
	invalidator, err := gatewaycache.NewSystemAccountInvalidator(
		gatewaycache.SystemAccountInvalidatorOptions{
			Cache:     cacheRedis,
			State:     stateRedis,
			Namespace: w5ManagementGroupDeleteNamespace,
			Now:       func() time.Time { return now },
			NewVersion: func(time.Time) (string, error) {
				versionCalls++
				return fmt.Sprintf("w5-management-group-delete-version-%d", versionCalls), nil
			},
		},
	)
	if err != nil {
		t.Fatalf("create group delete invalidator: %v", err)
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
		Store:       store,
		Invalidator: invalidator,
		Logger:      logger,
		Now:         func() time.Time { return now },
	})
	operationLogs := &w5ManagementGroupDeleteOperationLogQueue{}
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
			return fmt.Sprintf("oplog_w5_management_group_delete_%d", logIDCalls)
		},
	}
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                           cfg,
		Logger:                           logger,
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementGroupDeleteHandler: httpapi.NewManagementGroupDeleteHandlerWithOperationLog(
			service,
			logOptions,
		),
		ManagementMyGroupDeleteHandler: httpapi.NewManagementMyGroupDeleteHandlerWithOperationLog(
			service,
			logOptions,
		),
	})
	server := httptest.NewServer(router)
	defer server.Close()

	adminDelete := requestW5ManagementGroupDelete(
		t,
		server,
		"/__aisys__/api/groups/"+w5ManagementGroupDeleteCascadeID+
			"?systemAccountId="+w5ManagementGroupDeleteOwnerID,
		w5ManagementGroupDeleteAdminToken,
		"req_w5_group_delete_admin",
	)
	assertW5ManagementGroupDeleteEmpty204(t, adminDelete)
	assertW5ManagementGroupDeleteCascade(t, ctx, db, now)
	assertW5ManagementGroupDeleteInvalidation(
		t,
		ctx,
		cacheRedis,
		stateRedis,
		"w5-management-group-delete-version-1",
		"w5-management-group-delete-version-2",
		"w5-management-group-delete-version-3",
		now,
	)
	if versionCalls != 3 {
		t.Fatalf("group delete invalidation version calls after admin delete = %d, want 3", versionCalls)
	}
	assertW5ManagementGroupDeleteLogCount(t, operationLogs, 1)

	authorizedDelete := requestW5ManagementGroupDelete(
		t,
		server,
		"/__aisys__/api/my-groups/"+w5ManagementGroupDeleteAuthorizedID,
		w5ManagementGroupDeleteGranteeToken,
		"req_w5_group_delete_authorized",
	)
	assertW5ManagementGroupDeleteError(
		t,
		authorizedDelete,
		http.StatusNotFound,
		"分组不存在",
	)
	assertW5ManagementGroupDeleteRowCount(
		t,
		ctx,
		db,
		"juhe_business.groups",
		"id",
		w5ManagementGroupDeleteAuthorizedID,
		1,
	)
	assertW5ManagementGroupDeleteFailureSideEffects(t, operationLogs, 1, versionCalls, 3)

	defaultDelete := requestW5ManagementGroupDelete(
		t,
		server,
		"/__aisys__/api/my-groups/"+w5ManagementGroupDeleteDefaultID,
		w5ManagementGroupDeleteOwnerToken,
		"req_w5_group_delete_default",
	)
	assertW5ManagementGroupDeleteError(
		t,
		defaultDelete,
		http.StatusBadRequest,
		"默认分组不能删除",
	)
	assertW5ManagementGroupDeleteRowCount(
		t,
		ctx,
		db,
		"juhe_business.groups",
		"id",
		w5ManagementGroupDeleteDefaultID,
		1,
	)
	assertW5ManagementGroupDeleteFailureSideEffects(t, operationLogs, 1, versionCalls, 3)

	selfDelete := requestW5ManagementGroupDelete(
		t,
		server,
		"/__aisys__/api/my-groups/"+w5ManagementGroupDeleteSelfID,
		w5ManagementGroupDeleteOwnerToken,
		"req_w5_group_delete_self",
	)
	assertW5ManagementGroupDeleteEmpty204(t, selfDelete)
	assertW5ManagementGroupDeleteRowCount(
		t,
		ctx,
		db,
		"juhe_business.groups",
		"id",
		w5ManagementGroupDeleteSelfID,
		0,
	)
	assertW5ManagementGroupDeleteLogCount(t, operationLogs, 2)
	if versionCalls != 6 {
		t.Fatalf("group delete invalidation version calls after self delete = %d, want 6", versionCalls)
	}

	repeatDelete := requestW5ManagementGroupDelete(
		t,
		server,
		"/__aisys__/api/my-groups/"+w5ManagementGroupDeleteSelfID,
		w5ManagementGroupDeleteOwnerToken,
		"req_w5_group_delete_repeat",
	)
	assertW5ManagementGroupDeleteError(
		t,
		repeatDelete,
		http.StatusNotFound,
		"分组不存在",
	)
	assertW5ManagementGroupDeleteFailureSideEffects(t, operationLogs, 2, versionCalls, 6)

	ownerGuard := requestW5ManagementGroupDelete(
		t,
		server,
		"/__aisys__/api/my-groups/"+w5ManagementGroupDeleteGuardID,
		w5ManagementGroupDeleteOwnerToken,
		"req_w5_group_delete_owner_guard",
	)
	assertW5ManagementGroupDeleteError(
		t,
		ownerGuard,
		http.StatusBadRequest,
		"唯一可用的启用分组",
	)
	assertW5ManagementGroupDeleteFailureSideEffects(t, operationLogs, 2, versionCalls, 6)

	insertW5ManagementGroupDeleteGuardOwnerAlternate(t, ctx, db, now)
	insertW5ManagementGroupDeleteGuardGranteeTarget(t, ctx, db, now)
	granteeGuard := requestW5ManagementGroupDelete(
		t,
		server,
		"/__aisys__/api/my-groups/"+w5ManagementGroupDeleteGuardID,
		w5ManagementGroupDeleteOwnerToken,
		"req_w5_group_delete_grantee_guard",
	)
	assertW5ManagementGroupDeleteError(
		t,
		granteeGuard,
		http.StatusBadRequest,
		"唯一可用的启用分组",
	)
	assertW5ManagementGroupDeleteFailureSideEffects(t, operationLogs, 2, versionCalls, 6)

	insertW5ManagementGroupDeleteGuardGranteeAlternate(t, ctx, db, now)
	guardDelete := requestW5ManagementGroupDelete(
		t,
		server,
		"/__aisys__/api/my-groups/"+w5ManagementGroupDeleteGuardID,
		w5ManagementGroupDeleteOwnerToken,
		"req_w5_group_delete_guard_success",
	)
	assertW5ManagementGroupDeleteEmpty204(t, guardDelete)
	assertW5ManagementGroupDeleteRowCount(
		t,
		ctx,
		db,
		"juhe_business.groups",
		"id",
		w5ManagementGroupDeleteGuardID,
		0,
	)
	assertW5ManagementGroupDeleteRowCount(
		t,
		ctx,
		db,
		"juhe_business.route_strategy_groups",
		"group_id",
		w5ManagementGroupDeleteGuardID,
		0,
	)
	if versionCalls != 9 {
		t.Fatalf("group delete invalidation version calls after guard delete = %d, want 9", versionCalls)
	}
	assertW5ManagementGroupDeleteRuntimeState(
		t,
		ctx,
		stateRedis,
		"w5-management-group-delete-version-9",
		now,
	)

	if logIDCalls != 3 {
		t.Fatalf("group delete operation log id calls = %d, want 3", logIDCalls)
	}
	assertW5ManagementGroupDeleteOperationLogs(t, operationLogs, now)
}

type w5ManagementGroupDeleteHTTPResponse struct {
	Status int
	Header http.Header
	Body   string
}

func requestW5ManagementGroupDelete(
	t *testing.T,
	server *httptest.Server,
	path string,
	sessionToken string,
	requestID string,
) w5ManagementGroupDeleteHTTPResponse {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, server.URL+path, nil)
	if err != nil {
		t.Fatalf("create DELETE %s request: %v", path, err)
	}
	req.Header.Set("Cookie", managementauth.SessionCookieName+"="+sessionToken)
	req.Header.Set("User-Agent", "w5-management-group-delete-smoke")
	req.Header.Set("X-Request-Id", requestID)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read DELETE %s response: %v", path, err)
	}
	return w5ManagementGroupDeleteHTTPResponse{
		Status: resp.StatusCode,
		Header: resp.Header.Clone(),
		Body:   string(body),
	}
}

func assertW5ManagementGroupDeleteEmpty204(
	t *testing.T,
	resp w5ManagementGroupDeleteHTTPResponse,
) {
	t.Helper()
	if resp.Status != http.StatusNoContent || resp.Body != "" {
		t.Fatalf("group delete status = %d, body = %q, want empty 204", resp.Status, resp.Body)
	}
}

func assertW5ManagementGroupDeleteError(
	t *testing.T,
	resp w5ManagementGroupDeleteHTTPResponse,
	wantStatus int,
	wantMessagePart string,
) {
	t.Helper()
	if resp.Status != wantStatus {
		t.Fatalf("group delete status = %d, body = %s, want %d", resp.Status, resp.Body, wantStatus)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(resp.Body), &body); err != nil {
		t.Fatalf("decode group delete error response %q: %v", resp.Body, err)
	}
	if !strings.Contains(body["message"], wantMessagePart) {
		t.Fatalf("group delete message = %q, want containing %q", body["message"], wantMessagePart)
	}
}

func insertW5ManagementGroupDeleteFixtures(
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
			($1, 'w5-group-delete-admin', 'W5 Group Delete Admin', NULL, 'admin', 'active', 'hash', false, false, $4, $4),
			($2, 'w5-group-delete-owner', 'W5 Group Delete Owner', NULL, 'user', 'active', 'hash', false, false, $4, $4),
			($3, 'w5-group-delete-grantee', 'W5 Group Delete Grantee', NULL, 'user', 'active', 'hash', false, false, $4, $4)
	`, w5ManagementGroupDeleteAdminID, w5ManagementGroupDeleteOwnerID,
		w5ManagementGroupDeleteGranteeID, now); err != nil {
		t.Fatalf("insert W5 management group delete accounts: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.groups (
			id, system_account_id, name, provider_code, description, enabled, is_default,
			group_type, scheduling_policy_json, created_at, updated_at
		) VALUES
			($1, $9, 'W5 Delete Cascade', 'openai', 'cascade target', true, false, 'personal', NULL, $11, $11),
			($2, $9, 'W5 Delete Cascade Alternate', 'openai', NULL, true, false, 'personal', NULL, $11, $11),
			($3, $9, 'W5 Delete Self', 'openai', NULL, true, false, 'personal', NULL, $11, $11),
			($4, $9, 'W5 Delete Authorized', 'openai', NULL, true, false, 'personal', NULL, $11, $11),
			($5, $9, 'W5 Delete Default', 'openai', NULL, true, true, 'personal', NULL, $11, $11),
			($6, $9, 'W5 Delete Guard', 'openai', 'guard target', true, false, 'personal', NULL, $11, $11),
			($7, $9, 'W5 Delete Guard Owner Alternate', 'openai', NULL, true, false, 'personal', NULL, $11, $11),
			($8, $10, 'W5 Delete Guard Grantee Alternate', 'openai', NULL, true, false, 'personal', NULL, $11, $11)
	`,
		w5ManagementGroupDeleteCascadeID,
		w5ManagementGroupDeleteCascadeAlternate,
		w5ManagementGroupDeleteSelfID,
		w5ManagementGroupDeleteAuthorizedID,
		w5ManagementGroupDeleteDefaultID,
		w5ManagementGroupDeleteGuardID,
		w5ManagementGroupDeleteGuardOwnerAltID,
		w5ManagementGroupDeleteGuardGranteeAltID,
		w5ManagementGroupDeleteOwnerID,
		w5ManagementGroupDeleteGranteeID,
		now,
	); err != nil {
		t.Fatalf("insert W5 management group delete groups: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, credentials_encrypted, credential_mask, concurrency_limit, priority,
			client_compatibility, schedulable, health_check_model, created_at, updated_at
		) VALUES (
			'acct_w5_group_delete_cascade', $1, 'openai', 'profile_openai_openai_v1', 'openai', 'v1',
			'W5 Delete Cascade Account', 'api_key', 'active', 'v1:test:test:delete', 'sk***delete', 20, 0,
			'openai_standard', true, 'gpt-5.6-sol', $2, $2
		)
	`, w5ManagementGroupDeleteOwnerID, now); err != nil {
		t.Fatalf("insert W5 management group delete account: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.group_accounts (
			system_account_id, group_id, account_id, enabled, created_at, updated_at
		) VALUES (
			$1, $2, 'acct_w5_group_delete_cascade', true, $3, $3
		)
	`, w5ManagementGroupDeleteOwnerID, w5ManagementGroupDeleteCascadeID, now); err != nil {
		t.Fatalf("insert W5 management group delete account binding: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.resource_authorizations (
			id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
			scope, status, effective_source_type, effective_source_team_id, activated_at, last_source_changed_at,
			remark, expires_at, limits_json, created_by, created_at, revoked_by, revoked_at,
			revoked_reason, updated_at
		) VALUES
			($1, 'group', $4, $7, $8, 'use', 'active', 'manual', NULL, $9, $9, 'cascade history', NULL, NULL, $7, $9, NULL, NULL, NULL, $9),
			($2, 'group', $5, $7, $8, 'use', 'active', 'manual', NULL, $9, $9, 'authorized delete check', NULL, NULL, $7, $9, NULL, NULL, NULL, $9),
			($3, 'group', $6, $7, $8, 'use', 'active', 'manual', NULL, $9, $9, 'guard route use', NULL, NULL, $7, $9, NULL, NULL, NULL, $9)
	`,
		w5ManagementGroupDeleteCascadeAuthID,
		w5ManagementGroupDeleteAuthorizedAuthID,
		w5ManagementGroupDeleteGuardAuthID,
		w5ManagementGroupDeleteCascadeID,
		w5ManagementGroupDeleteAuthorizedID,
		w5ManagementGroupDeleteGuardID,
		w5ManagementGroupDeleteOwnerID,
		w5ManagementGroupDeleteGranteeID,
		now.Add(-time.Hour),
	); err != nil {
		t.Fatalf("insert W5 management group delete authorizations: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.group_authorization_settings (
			authorization_id, system_account_id, group_id, enabled, group_type,
			scheduling_policy_json, created_at, updated_at
		) VALUES (
			$1, $2, $3, true, 'personal', NULL, $4, $4
		)
	`, w5ManagementGroupDeleteCascadeAuthID, w5ManagementGroupDeleteGranteeID,
		w5ManagementGroupDeleteCascadeID, now); err != nil {
		t.Fatalf("insert W5 management group delete authorization settings: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.resource_authorization_sources (
			id, authorization_id, source_type, source_team_id, status,
			activated_at, ended_at, ended_reason, created_by, created_at,
			revoked_by, revoked_at, updated_at
		) VALUES (
			$1, $2, 'manual', NULL, 'active',
			$4, NULL, NULL, $3, $4,
			NULL, NULL, $4
		)
	`, w5ManagementGroupDeleteCascadeSourceID, w5ManagementGroupDeleteCascadeAuthID,
		w5ManagementGroupDeleteOwnerID, now.Add(-time.Hour)); err != nil {
		t.Fatalf("insert W5 management group delete authorization source: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.resource_authorization_grants (
			id, resource_type, resource_id, resource_owner_system_account_id,
			grantee_type, grantee_system_account_id, grantee_team_id,
			scope, status, remark, expires_at, limits_json,
			created_by, created_at, revoked_by, revoked_at, updated_at
		) VALUES (
			$1, 'group', $2, $3,
			'system_account', $4, NULL,
			'use', 'active', 'cascade grant history', NULL, NULL,
			$3, $5, NULL, NULL, $5
		)
	`, w5ManagementGroupDeleteCascadeGrantID, w5ManagementGroupDeleteCascadeID,
		w5ManagementGroupDeleteOwnerID, w5ManagementGroupDeleteGranteeID,
		now.Add(-time.Hour)); err != nil {
		t.Fatalf("insert W5 management group delete authorization grant: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.route_strategies (
			id, system_account_id, name, description, mode, status, is_default,
			config_json, created_at, updated_at
		) VALUES
			($1, $4, 'W5 Delete Cascade Route', NULL, 'weighted', 'active', false, NULL, $6, $6),
			($2, $4, 'W5 Delete Guard Owner Route', NULL, 'weighted', 'active', false, NULL, $6, $6),
			($3, $5, 'W5 Delete Guard Grantee Route', NULL, 'weighted', 'active', false, NULL, $6, $6)
	`, w5ManagementGroupDeleteCascadeRouteID, w5ManagementGroupDeleteGuardOwnerRoute,
		w5ManagementGroupDeleteGuardUserRoute, w5ManagementGroupDeleteOwnerID,
		w5ManagementGroupDeleteGranteeID, now); err != nil {
		t.Fatalf("insert W5 management group delete route strategies: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.route_strategy_groups (
			id, route_strategy_id, system_account_id, group_id,
			priority, weight, status, created_at, updated_at
		) VALUES
			('rsg_w5_group_delete_cascade_target', $1, $4, $5, 1, 50, 'active', $7, $7),
			('rsg_w5_group_delete_cascade_alt', $1, $4, $6, 2, 50, 'active', $7, $7),
			('rsg_w5_group_delete_guard_owner_target', $2, $4, $3, 1, 100, 'active', $7, $7)
	`, w5ManagementGroupDeleteCascadeRouteID, w5ManagementGroupDeleteGuardOwnerRoute,
		w5ManagementGroupDeleteGuardID, w5ManagementGroupDeleteOwnerID,
		w5ManagementGroupDeleteCascadeID, w5ManagementGroupDeleteCascadeAlternate, now); err != nil {
		t.Fatalf("insert W5 management group delete route bindings: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_stats.group_account_stats (
			system_account_id, group_id, total, available, active, disabled, error,
			rate_limited, current_concurrency, concurrency_limit, updated_at
		) VALUES (
			$1, $2, 7, 6, 5, 1, 1,
			0, 3, 20, $3
		)
	`, w5ManagementGroupDeleteOwnerID, w5ManagementGroupDeleteCascadeID,
		now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert W5 management group delete stats: %v", err)
	}
}

func insertW5ManagementGroupDeleteGuardOwnerAlternate(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.route_strategy_groups (
			id, route_strategy_id, system_account_id, group_id,
			priority, weight, status, created_at, updated_at
		) VALUES (
			'rsg_w5_group_delete_guard_owner_alt', $1, $2, $3,
			2, 100, 'active', $4, $4
		)
	`, w5ManagementGroupDeleteGuardOwnerRoute, w5ManagementGroupDeleteOwnerID,
		w5ManagementGroupDeleteGuardOwnerAltID, now); err != nil {
		t.Fatalf("insert W5 management group delete owner guard alternate: %v", err)
	}
}

func insertW5ManagementGroupDeleteGuardGranteeTarget(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.route_strategy_groups (
			id, route_strategy_id, system_account_id, group_id,
			priority, weight, status, created_at, updated_at
		) VALUES (
			'rsg_w5_group_delete_guard_grantee_target', $1, $2, $3,
			1, 100, 'active', $4, $4
		)
	`, w5ManagementGroupDeleteGuardUserRoute, w5ManagementGroupDeleteGranteeID,
		w5ManagementGroupDeleteGuardID, now); err != nil {
		t.Fatalf("insert W5 management group delete grantee guard target: %v", err)
	}
}

func insertW5ManagementGroupDeleteGuardGranteeAlternate(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.route_strategy_groups (
			id, route_strategy_id, system_account_id, group_id,
			priority, weight, status, created_at, updated_at
		) VALUES (
			'rsg_w5_group_delete_guard_grantee_alt', $1, $2, $3,
			2, 100, 'active', $4, $4
		)
	`, w5ManagementGroupDeleteGuardUserRoute, w5ManagementGroupDeleteGranteeID,
		w5ManagementGroupDeleteGuardGranteeAltID, now); err != nil {
		t.Fatalf("insert W5 management group delete grantee guard alternate: %v", err)
	}
}

func assertW5ManagementGroupDeleteCascade(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
) {
	t.Helper()
	for _, check := range []struct {
		table  string
		column string
		value  string
		want   int
	}{
		{"juhe_business.groups", "id", w5ManagementGroupDeleteCascadeID, 0},
		{"juhe_business.group_accounts", "group_id", w5ManagementGroupDeleteCascadeID, 0},
		{"juhe_business.route_strategy_groups", "group_id", w5ManagementGroupDeleteCascadeID, 0},
		{"juhe_business.group_authorization_settings", "group_id", w5ManagementGroupDeleteCascadeID, 0},
		{"juhe_business.resource_authorizations", "id", w5ManagementGroupDeleteCascadeAuthID, 1},
		{"juhe_business.resource_authorization_grants", "id", w5ManagementGroupDeleteCascadeGrantID, 1},
		{"juhe_business.resource_authorization_sources", "id", w5ManagementGroupDeleteCascadeSourceID, 1},
		{"juhe_stats.group_account_stats", "group_id", w5ManagementGroupDeleteCascadeID, 1},
	} {
		assertW5ManagementGroupDeleteRowCount(
			t,
			ctx,
			db,
			check.table,
			check.column,
			check.value,
			check.want,
		)
	}

	var authorizationResourceType string
	var authorizationResourceID string
	if err := db.QueryRowContext(ctx, `
		SELECT resource_type, resource_id
		FROM juhe_business.resource_authorizations
		WHERE id = $1
	`, w5ManagementGroupDeleteCascadeAuthID).Scan(
		&authorizationResourceType,
		&authorizationResourceID,
	); err != nil {
		t.Fatalf("read retained group authorization history: %v", err)
	}
	if authorizationResourceType != "group" ||
		authorizationResourceID != w5ManagementGroupDeleteCascadeID {
		t.Fatalf(
			"retained group authorization resource = %q/%q",
			authorizationResourceType,
			authorizationResourceID,
		)
	}

	var grantResourceType string
	var grantResourceID string
	if err := db.QueryRowContext(ctx, `
		SELECT resource_type, resource_id
		FROM juhe_business.resource_authorization_grants
		WHERE id = $1
	`, w5ManagementGroupDeleteCascadeGrantID).Scan(
		&grantResourceType,
		&grantResourceID,
	); err != nil {
		t.Fatalf("read retained group authorization grant history: %v", err)
	}
	if grantResourceType != "group" || grantResourceID != w5ManagementGroupDeleteCascadeID {
		t.Fatalf("retained group authorization grant resource = %q/%q", grantResourceType, grantResourceID)
	}

	var sourceAuthorizationID string
	if err := db.QueryRowContext(ctx, `
		SELECT authorization_id
		FROM juhe_business.resource_authorization_sources
		WHERE id = $1
	`, w5ManagementGroupDeleteCascadeSourceID).Scan(&sourceAuthorizationID); err != nil {
		t.Fatalf("read retained group authorization source history: %v", err)
	}
	if sourceAuthorizationID != w5ManagementGroupDeleteCascadeAuthID {
		t.Fatalf("retained group authorization source authorization = %q", sourceAuthorizationID)
	}

	var dirtyReason string
	var dirtyUpdatedAt time.Time
	if err := db.QueryRowContext(ctx, `
		SELECT reason, updated_at
		FROM juhe_business.group_account_stats_dirty
		WHERE group_id = $1
	`, w5ManagementGroupDeleteCascadeID).Scan(&dirtyReason, &dirtyUpdatedAt); err != nil {
		t.Fatalf("read group delete dirty marker: %v", err)
	}
	if dirtyReason != managementgroups.GroupDeletedReason ||
		!dirtyUpdatedAt.UTC().Equal(now.UTC()) {
		t.Fatalf(
			"group delete dirty marker reason=%q updatedAt=%s",
			dirtyReason,
			dirtyUpdatedAt.UTC().Format(time.RFC3339Nano),
		)
	}

	var statsTotal int
	var statsUpdatedAt string
	if err := db.QueryRowContext(ctx, `
		SELECT total, updated_at
		FROM juhe_stats.group_account_stats
		WHERE system_account_id = $1
		  AND group_id = $2
	`, w5ManagementGroupDeleteOwnerID, w5ManagementGroupDeleteCascadeID).Scan(
		&statsTotal,
		&statsUpdatedAt,
	); err != nil {
		t.Fatalf("read retained group account stats: %v", err)
	}
	wantStatsUpdatedAt := now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if statsTotal != 7 || statsUpdatedAt != wantStatsUpdatedAt {
		t.Fatalf(
			"retained group account stats total=%d updatedAt=%q, want 7/%q",
			statsTotal,
			statsUpdatedAt,
			wantStatsUpdatedAt,
		)
	}
}

func assertW5ManagementGroupDeleteRowCount(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	table string,
	column string,
	value string,
	want int,
) {
	t.Helper()
	allowed := map[string]map[string]bool{
		"juhe_business.groups":                         {"id": true},
		"juhe_business.group_accounts":                 {"group_id": true},
		"juhe_business.route_strategy_groups":          {"group_id": true},
		"juhe_business.group_authorization_settings":   {"group_id": true},
		"juhe_business.resource_authorizations":        {"id": true},
		"juhe_business.resource_authorization_grants":  {"id": true},
		"juhe_business.resource_authorization_sources": {"id": true},
		"juhe_stats.group_account_stats":               {"group_id": true},
	}
	if !allowed[table][column] {
		t.Fatalf("unsupported count target %s.%s", table, column)
	}
	var count int
	query := fmt.Sprintf("SELECT count(*) FROM %s WHERE %s = $1", table, column)
	if err := db.QueryRowContext(ctx, query, value).Scan(&count); err != nil {
		t.Fatalf("count %s where %s=%q: %v", table, column, value, err)
	}
	if count != want {
		t.Fatalf("%s where %s=%q count = %d, want %d", table, column, value, count, want)
	}
}

func w5ManagementGroupDeleteSharedCacheKey(t *testing.T, cacheName string) string {
	t.Helper()
	key, err := gatewaycache.SharedCacheVersionKey(
		w5ManagementGroupDeleteNamespace,
		cacheName,
	)
	if err != nil {
		t.Fatalf("build group delete shared cache key %s: %v", cacheName, err)
	}
	return key
}

func assertW5ManagementGroupDeleteInvalidation(
	t *testing.T,
	ctx context.Context,
	cacheRedis *redisplatform.Client,
	stateRedis *redisplatform.Client,
	wantLookupVersion string,
	wantAccountIDsVersion string,
	wantRuntimeVersion string,
	wantPublishedAt time.Time,
) {
	t.Helper()
	lookupVersion, err := cacheRedis.GetRaw(
		ctx,
		w5ManagementGroupDeleteSharedCacheKey(t, gatewaycache.GroupLookupCacheName),
	)
	if err != nil {
		t.Fatalf("read group delete lookup cache version: %v", err)
	}
	accountIDsVersion, err := cacheRedis.GetRaw(
		ctx,
		w5ManagementGroupDeleteSharedCacheKey(t, gatewaycache.GroupAccountIDsCacheName),
	)
	if err != nil {
		t.Fatalf("read group delete account IDs cache version: %v", err)
	}
	if string(lookupVersion) != wantLookupVersion ||
		string(accountIDsVersion) != wantAccountIDsVersion {
		t.Fatalf(
			"group delete shared cache versions lookup=%q accountIDs=%q, want %q/%q",
			lookupVersion,
			accountIDsVersion,
			wantLookupVersion,
			wantAccountIDsVersion,
		)
	}
	assertW5ManagementGroupDeleteRuntimeState(
		t,
		ctx,
		stateRedis,
		wantRuntimeVersion,
		wantPublishedAt,
	)
}

func assertW5ManagementGroupDeleteRuntimeState(
	t *testing.T,
	ctx context.Context,
	stateRedis *redisplatform.Client,
	wantVersion string,
	wantPublishedAt time.Time,
) {
	t.Helper()
	key, err := gatewaycache.RuntimeStateKey(
		w5ManagementGroupDeleteNamespace,
		gatewaycache.RuntimeInvalidationStoreName,
		"topic:"+gatewaycache.GatewayRuntimeCacheTopic,
	)
	if err != nil {
		t.Fatalf("build group delete runtime state key: %v", err)
	}
	raw, err := stateRedis.GetRaw(ctx, key)
	if err != nil {
		t.Fatalf("read group delete runtime state: %v", err)
	}
	var state struct {
		Version     string `json:"version"`
		Reason      string `json:"reason"`
		PublishedAt string `json:"publishedAt"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode group delete runtime state %s: %v", raw, err)
	}
	wantPublished := wantPublishedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	if state.Version != wantVersion ||
		state.Reason != managementgroups.GroupDeletedReason ||
		state.PublishedAt != wantPublished {
		t.Fatalf(
			"group delete runtime state = %+v, want version=%q reason=%q publishedAt=%q",
			state,
			wantVersion,
			managementgroups.GroupDeletedReason,
			wantPublished,
		)
	}
}

func assertW5ManagementGroupDeleteFailureSideEffects(
	t *testing.T,
	operationLogs *w5ManagementGroupDeleteOperationLogQueue,
	wantLogs int,
	gotVersions int,
	wantVersions int,
) {
	t.Helper()
	assertW5ManagementGroupDeleteLogCount(t, operationLogs, wantLogs)
	if gotVersions != wantVersions {
		t.Fatalf(
			"group delete invalidation version calls after failed delete = %d, want %d",
			gotVersions,
			wantVersions,
		)
	}
}

type w5ManagementGroupDeleteOperationLogQueue struct {
	logs []port.OperationLogInput
}

func (q *w5ManagementGroupDeleteOperationLogQueue) Enqueue(
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
		ID:    fmt.Sprintf("task_w5_management_group_delete_%d", len(q.logs)),
		Queue: opts.Queue,
		Type:  taskType,
	}, nil
}

func assertW5ManagementGroupDeleteLogCount(
	t *testing.T,
	queueStub *w5ManagementGroupDeleteOperationLogQueue,
	want int,
) {
	t.Helper()
	if len(queueStub.logs) != want {
		t.Fatalf("group delete operation logs = %d, want %d: %+v", len(queueStub.logs), want, queueStub.logs)
	}
}

func assertW5ManagementGroupDeleteOperationLogs(
	t *testing.T,
	queueStub *w5ManagementGroupDeleteOperationLogQueue,
	now time.Time,
) {
	t.Helper()
	assertW5ManagementGroupDeleteLogCount(t, queueStub, 3)

	adminLog := queueStub.logs[0]
	assertW5ManagementGroupDeleteOperationLogCommon(
		t,
		adminLog,
		"oplog_w5_management_group_delete_1",
		"req_w5_group_delete_admin",
		w5ManagementGroupDeleteAdminID,
		"admin",
		w5ManagementGroupDeleteCascadeID,
		"W5 Delete Cascade",
		"/__aisys__/api/groups/"+w5ManagementGroupDeleteCascadeID,
		now,
	)
	assertW5ManagementGroupDeleteOperationLogStrategies(
		t,
		adminLog,
		[]w5ManagementGroupDeleteExpectedStrategy{{
			ID:   w5ManagementGroupDeleteCascadeRouteID,
			Name: "W5 Delete Cascade Route",
		}},
	)

	selfLog := queueStub.logs[1]
	assertW5ManagementGroupDeleteOperationLogCommon(
		t,
		selfLog,
		"oplog_w5_management_group_delete_2",
		"req_w5_group_delete_self",
		w5ManagementGroupDeleteOwnerID,
		"self",
		w5ManagementGroupDeleteSelfID,
		"W5 Delete Self",
		"/__aisys__/api/my-groups/"+w5ManagementGroupDeleteSelfID,
		now,
	)
	if len(selfLog.Targets) != 0 || selfLog.Metadata != nil || len(selfLog.Changes) != 1 {
		t.Fatalf("self group delete operation log side effects = %+v", selfLog)
	}

	guardLog := queueStub.logs[2]
	assertW5ManagementGroupDeleteOperationLogCommon(
		t,
		guardLog,
		"oplog_w5_management_group_delete_3",
		"req_w5_group_delete_guard_success",
		w5ManagementGroupDeleteOwnerID,
		"self",
		w5ManagementGroupDeleteGuardID,
		"W5 Delete Guard",
		"/__aisys__/api/my-groups/"+w5ManagementGroupDeleteGuardID,
		now,
	)
	assertW5ManagementGroupDeleteOperationLogStrategies(
		t,
		guardLog,
		[]w5ManagementGroupDeleteExpectedStrategy{
			{ID: w5ManagementGroupDeleteGuardUserRoute, Name: "W5 Delete Guard Grantee Route"},
			{ID: w5ManagementGroupDeleteGuardOwnerRoute, Name: "W5 Delete Guard Owner Route"},
		},
	)
}

func assertW5ManagementGroupDeleteOperationLogCommon(
	t *testing.T,
	logInput port.OperationLogInput,
	wantID string,
	wantTraceID string,
	wantActorID string,
	wantMode string,
	wantResourceID string,
	wantResourceName string,
	wantPath string,
	wantCreatedAt time.Time,
) {
	t.Helper()
	if logInput.ID != wantID ||
		logInput.TraceID != wantTraceID ||
		logInput.ActorSystemAccountID != wantActorID ||
		logInput.OperationScopeSystemAccountID != w5ManagementGroupDeleteOwnerID ||
		logInput.Mode != wantMode ||
		logInput.Module != "groups" ||
		logInput.Action != "delete" ||
		logInput.OperationKey != "groups.delete" ||
		logInput.ResourceType != "group" ||
		logInput.ResourceID != wantResourceID ||
		logInput.ResourceName != wantResourceName ||
		logInput.Summary != "删除分组："+wantResourceName ||
		logInput.DetailLevel != "full" ||
		logInput.VisibilityScope != "targeted" ||
		logInput.Method != http.MethodDelete ||
		logInput.Path != wantPath ||
		logInput.StatusCode == nil ||
		*logInput.StatusCode != http.StatusNoContent ||
		logInput.ClientIP != "127.0.0.1" ||
		logInput.UserAgent != "w5-management-group-delete-smoke" ||
		!logInput.CreatedAt.UTC().Equal(wantCreatedAt.UTC()) {
		t.Fatalf("group delete operation log = %+v", logInput)
	}
	if len(logInput.Viewers) != 1 ||
		logInput.Viewers[0].SystemAccountID != w5ManagementGroupDeleteOwnerID ||
		logInput.Viewers[0].VisibilityReason != "resource_owner" ||
		logInput.Viewers[0].DetailLevel != "full" {
		t.Fatalf("group delete operation log viewers = %+v", logInput.Viewers)
	}
	if len(logInput.Changes) == 0 ||
		logInput.Changes[0].Field != "deleted" ||
		logInput.Changes[0].Before != false ||
		logInput.Changes[0].After != true {
		t.Fatalf("group delete operation log changes = %+v", logInput.Changes)
	}
}

type w5ManagementGroupDeleteExpectedStrategy struct {
	ID   string
	Name string
}

func assertW5ManagementGroupDeleteOperationLogStrategies(
	t *testing.T,
	logInput port.OperationLogInput,
	want []w5ManagementGroupDeleteExpectedStrategy,
) {
	t.Helper()
	if len(logInput.Targets) != len(want) {
		t.Fatalf("group delete operation log targets = %+v, want %d", logInput.Targets, len(want))
	}
	for index, expected := range want {
		target := logInput.Targets[index]
		if target.TargetType != "route_strategy" ||
			target.TargetID != expected.ID ||
			target.TargetName != expected.Name ||
			target.TargetOwnerSystemAccountID != w5ManagementGroupDeleteOwnerID ||
			target.Relation != "affected" {
			t.Fatalf("group delete operation log target[%d] = %+v", index, target)
		}
	}

	rawMetadata, err := json.Marshal(logInput.Metadata)
	if err != nil {
		t.Fatalf("marshal group delete operation log metadata: %v", err)
	}
	var metadata struct {
		AffectedRouteStrategyCount int `json:"affectedRouteStrategyCount"`
		AffectedRouteStrategies    []struct {
			RouteStrategyID   string `json:"routeStrategyId"`
			RouteStrategyName string `json:"routeStrategyName"`
			RemovedGroupID    string `json:"removedGroupId"`
			RemovedGroupName  string `json:"removedGroupName"`
		} `json:"affectedRouteStrategies"`
	}
	if err := json.Unmarshal(rawMetadata, &metadata); err != nil {
		t.Fatalf("decode group delete operation log metadata %s: %v", rawMetadata, err)
	}
	if metadata.AffectedRouteStrategyCount != len(want) ||
		len(metadata.AffectedRouteStrategies) != len(want) {
		t.Fatalf("group delete operation log metadata = %+v", metadata)
	}
	for index, expected := range want {
		entry := metadata.AffectedRouteStrategies[index]
		if entry.RouteStrategyID != expected.ID ||
			entry.RouteStrategyName != expected.Name ||
			entry.RemovedGroupID != logInput.ResourceID ||
			entry.RemovedGroupName != logInput.ResourceName {
			t.Fatalf("group delete operation log metadata strategy[%d] = %+v", index, entry)
		}
	}
	if len(logInput.Changes) != 2 ||
		logInput.Changes[1].Field != "affectedRouteStrategies" {
		t.Fatalf("group delete affected route strategy changes = %+v", logInput.Changes)
	}
}
