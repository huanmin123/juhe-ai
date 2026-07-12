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

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementroutestrategies"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w5ManagementRouteStrategyUpdateAdminID = "sys_w5_route_update_admin"
	w5ManagementRouteStrategyUpdateOwnerID = "sys_w5_route_update_owner"
	w5ManagementRouteStrategyUpdateOtherID = "sys_w5_route_update_other"

	w5ManagementRouteStrategyUpdateAdminSession = "sess_w5_route_update_admin"
	w5ManagementRouteStrategyUpdateOwnerSession = "sess_w5_route_update_owner"
	w5ManagementRouteStrategyUpdateOtherSession = "sess_w5_route_update_other"
	w5ManagementRouteStrategyUpdateAdminToken   = "w5-route-update-admin-session"
	w5ManagementRouteStrategyUpdateOwnerToken   = "w5-route-update-owner-session"
	w5ManagementRouteStrategyUpdateOtherToken   = "w5-route-update-other-session"

	w5ManagementRouteStrategyUpdateTargetID    = "route_w5_route_update_target"
	w5ManagementRouteStrategyUpdateDuplicateID = "route_w5_route_update_duplicate"
	w5ManagementRouteStrategyUpdateRollbackID  = "route_w5_route_update_rollback"
	w5ManagementRouteStrategyUpdateSelfID      = "route_w5_route_update_self"
	w5ManagementRouteStrategyUpdateOldGroupID  = "grp_w5_route_update_old"
	w5ManagementRouteStrategyUpdatePrimaryID   = "grp_w5_route_update_primary"
	w5ManagementRouteStrategyUpdateBackupID    = "grp_w5_route_update_backup"
	w5ManagementRouteStrategyUpdateOldBinding  = "rsg_w5_route_update_old"
	w5ManagementRouteStrategyRollbackBinding   = "rsg_w5_route_update_rollback_old"
	w5ManagementRouteStrategySelfBinding       = "rsg_w5_route_update_self_old"
	w5ManagementRouteStrategyUpdateAPIKeyID    = "key_w5_route_update"
)

func TestW5ManagementRouteStrategyUpdatePostgresSmoke(t *testing.T) {
	// Test-first RED: handler and router wiring already existed in the shared worktree,
	// so the initial compile failure was the absent PostgreSQL fixture/route contract.
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("juhe_ai"),
		tcpostgres.WithUsername("juhe_ai"),
		tcpostgres.WithPassword("juhe_ai_password"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	defer terminateContainer(t, ctx, container)

	postgresURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	db := openSQLDB(t, postgresURL)
	defer closeSQLDB(t, db)
	runGooseMigrations(t, db)

	now := time.Date(2026, 7, 12, 11, 45, 0, 0, time.UTC)
	createdAt := now.Add(-48 * time.Hour)
	insertW5ManagementRouteStrategyUpdateFixtures(t, ctx, db, createdAt)
	sessionCreatedAt := now.Add(-5 * time.Minute)
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w5ManagementRouteStrategyUpdateAdminSession,
		w5ManagementRouteStrategyUpdateAdminID,
		w5ManagementRouteStrategyUpdateAdminToken,
		sessionCreatedAt,
	)
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w5ManagementRouteStrategyUpdateOwnerSession,
		w5ManagementRouteStrategyUpdateOwnerID,
		w5ManagementRouteStrategyUpdateOwnerToken,
		sessionCreatedAt,
	)
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w5ManagementRouteStrategyUpdateOtherSession,
		w5ManagementRouteStrategyUpdateOtherID,
		w5ManagementRouteStrategyUpdateOtherToken,
		sessionCreatedAt,
	)

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
	invalidator := &w5ManagementRouteStrategyUpdateInvalidator{}
	idCalls := 0
	service := managementroutestrategies.NewServiceWithOptions(
		managementroutestrategies.ServiceOptions{
			CreateStore: store,
			Transactor:  store,
			Invalidator: invalidator,
			Logger:      logger,
			Now:         func() time.Time { return now },
			NewID: func(prefix string) string {
				idCalls++
				return fmt.Sprintf("%s_w5_route_update_%d", prefix, idCalls)
			},
		},
	)
	operationLogs := &w5ManagementRouteStrategyUpdateOperationLogQueue{}
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
			return fmt.Sprintf("oplog_w5_route_update_%d", logIDCalls)
		},
	}
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                           cfg,
		Logger:                           logger,
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementRouteStrategyUpdateHandler: httpapi.NewManagementRouteStrategyUpdateHandlerWithOperationLog(
			service,
			logOptions,
		),
		ManagementMyRouteStrategyUpdateHandler: httpapi.NewManagementMyRouteStrategyUpdateHandlerWithOperationLog(
			service,
			logOptions,
		),
	})
	assertW5ManagementRouteStrategySessionLastSeenAt(
		t,
		ctx,
		db,
		w5ManagementRouteStrategyUpdateAdminSession,
		sessionCreatedAt,
	)
	assertW5ManagementRouteStrategySessionLastSeenAt(
		t,
		ctx,
		db,
		w5ManagementRouteStrategyUpdateOwnerSession,
		sessionCreatedAt,
	)

	successRec := serveW5ManagementRouteStrategyUpdateRequest(
		router,
		"/__aisys__/api/route-strategies/"+w5ManagementRouteStrategyUpdateTargetID+
			"?systemAccountId="+w5ManagementRouteStrategyUpdateOwnerID,
		w5ManagementRouteStrategyUpdateAdminToken,
		`{
			"name":" W5 Route Updated ",
			"description":null,
			"mode":"weighted",
			"status":"disabled",
			"groupBindings":[
				{"groupId":"`+w5ManagementRouteStrategyUpdateBackupID+`","priority":2,"weight":30,"status":"active"},
				{"groupId":"`+w5ManagementRouteStrategyUpdatePrimaryID+`","priority":1,"weight":70,"status":"active"}
			]
		}`,
		"req_w5_route_update_success",
	)
	updated := decodeW5ManagementRouteStrategyUpdateDetail(t, successRec, http.StatusOK)
	assertW5ManagementRouteStrategyUpdateResponse(t, updated, createdAt, now)
	assertW5ManagementRouteStrategyUpdateDatabase(t, ctx, db, createdAt, now)
	assertW5ManagementRouteStrategySessionLastSeenAt(
		t,
		ctx,
		db,
		w5ManagementRouteStrategyUpdateAdminSession,
		now,
	)
	assertW5ManagementRouteStrategyUpdateSideEffects(t, invalidator, operationLogs, logIDCalls, 1)

	duplicateRec := serveW5ManagementRouteStrategyUpdateRequest(
		router,
		"/__aisys__/api/route-strategies/"+w5ManagementRouteStrategyUpdateTargetID+
			"?systemAccountId="+w5ManagementRouteStrategyUpdateOwnerID,
		w5ManagementRouteStrategyUpdateAdminToken,
		`{"name":" W5 Route Update Duplicate "}`,
		"req_w5_route_update_duplicate",
	)
	assertW5ManagementRouteStrategyUpdateError(
		t,
		duplicateRec,
		http.StatusConflict,
		"策略路由名称已存在：W5 Route Update Duplicate",
	)
	assertW5ManagementRouteStrategyUpdateDatabase(t, ctx, db, createdAt, now)
	assertW5ManagementRouteStrategyUpdateSideEffects(t, invalidator, operationLogs, logIDCalls, 1)

	selfOtherRec := serveW5ManagementRouteStrategyUpdateRequest(
		router,
		"/__aisys__/api/my-route-strategies/"+w5ManagementRouteStrategyUpdateTargetID,
		w5ManagementRouteStrategyUpdateOtherToken,
		`{"status":"active"}`,
		"req_w5_route_update_self_other",
	)
	assertW5ManagementRouteStrategyUpdateError(
		t,
		selfOtherRec,
		http.StatusNotFound,
		"策略路由不存在",
	)
	assertW5ManagementRouteStrategyUpdateDatabase(t, ctx, db, createdAt, now)
	assertW5ManagementRouteStrategyUpdateSideEffects(t, invalidator, operationLogs, logIDCalls, 1)

	runW5ManagementRouteStrategyUpdateReviewScenarios(
		t,
		ctx,
		db,
		router,
		invalidator,
		operationLogs,
		&logIDCalls,
		createdAt,
		now,
	)
}

func serveW5ManagementRouteStrategyUpdateRequest(
	router http.Handler,
	target string,
	sessionToken string,
	body string,
	requestID string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPatch, target, strings.NewReader(body))
	req.Header.Set("Cookie", managementauth.SessionCookieName+"="+sessionToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "w5-management-route-strategy-update-smoke")
	req.Header.Set("X-Request-Id", requestID)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeW5ManagementRouteStrategyUpdateDetail(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	wantStatus int,
) managementroutestrategies.DetailResult {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("route strategy update status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("route strategy update Cache-Control = %q, want no-store", got)
	}
	var envelope struct {
		Data managementroutestrategies.DetailResult `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode route strategy update response: %v", err)
	}
	return envelope.Data
}

func assertW5ManagementRouteStrategyUpdateError(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	wantStatus int,
	wantMessage string,
) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("route strategy update error status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("route strategy update error Cache-Control = %q, want no-store", got)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode route strategy update error: %v", err)
	}
	if body["message"] != wantMessage {
		t.Fatalf("route strategy update error message = %q, want %q", body["message"], wantMessage)
	}
}

func runW5ManagementRouteStrategyUpdateReviewScenarios(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	router http.Handler,
	invalidator *w5ManagementRouteStrategyUpdateInvalidator,
	operationLogs *w5ManagementRouteStrategyUpdateOperationLogQueue,
	logIDCalls *int,
	createdAt time.Time,
	now time.Time,
) {
	t.Helper()
	installW5ManagementRouteStrategyUpdateBindingFailureTrigger(t, ctx, db)
	triggerInstalled := true
	defer func() {
		if triggerInstalled {
			dropW5ManagementRouteStrategyUpdateBindingFailureTrigger(t, ctx, db)
		}
	}()

	rollbackRec := serveW5ManagementRouteStrategyUpdateRequest(
		router,
		"/__aisys__/api/route-strategies/"+w5ManagementRouteStrategyUpdateRollbackID+
			"?systemAccountId="+w5ManagementRouteStrategyUpdateOwnerID,
		w5ManagementRouteStrategyUpdateAdminToken,
		`{
			"name":"W5 Rollback Attempted",
			"description":"must rollback",
			"mode":"weighted",
			"status":"disabled",
			"groupBindings":[
				{"groupId":"`+w5ManagementRouteStrategyUpdatePrimaryID+`","priority":1,"weight":60,"status":"active"},
				{"groupId":"`+w5ManagementRouteStrategyUpdateBackupID+`","priority":2,"weight":40,"status":"active"}
			]
		}`,
		"req_w5_route_update_binding_failure",
	)
	assertW5ManagementRouteStrategyUpdateError(
		t,
		rollbackRec,
		http.StatusInternalServerError,
		"服务器内部错误",
	)
	assertW5ManagementRouteStrategyUpdateRollbackDatabase(t, ctx, db, createdAt)
	assertW5ManagementRouteStrategyUpdateSideEffects(
		t,
		invalidator,
		operationLogs,
		*logIDCalls,
		1,
	)

	dropW5ManagementRouteStrategyUpdateBindingFailureTrigger(t, ctx, db)
	triggerInstalled = false

	assertW5ManagementRouteStrategySessionLastSeenAt(
		t,
		ctx,
		db,
		w5ManagementRouteStrategyUpdateOwnerSession,
		now.Add(-5*time.Minute),
	)
	selfRec := serveW5ManagementRouteStrategyUpdateRequest(
		router,
		"/__aisys__/api/my-route-strategies/"+w5ManagementRouteStrategyUpdateSelfID,
		w5ManagementRouteStrategyUpdateOwnerToken,
		`{"name":" W5 Self Updated ","description":null,"status":"disabled"}`,
		"req_w5_route_update_self_success",
	)
	selfRaw := append([]byte(nil), selfRec.Body.Bytes()...)
	selfUpdated := decodeW5ManagementRouteStrategyUpdateDetail(t, selfRec, http.StatusOK)
	assertW5ManagementRouteStrategyUpdateSelfResponse(t, selfUpdated, selfRaw, createdAt, now)
	assertW5ManagementRouteStrategyUpdateSelfDatabase(t, ctx, db, createdAt, now)
	assertW5ManagementRouteStrategySessionLastSeenAt(
		t,
		ctx,
		db,
		w5ManagementRouteStrategyUpdateOwnerSession,
		now,
	)
	assertW5ManagementRouteStrategyUpdateSideEffects(
		t,
		invalidator,
		operationLogs,
		*logIDCalls,
		2,
	)
}

func installW5ManagementRouteStrategyUpdateBindingFailureTrigger(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) {
	t.Helper()
	dropW5ManagementRouteStrategyUpdateBindingFailureTrigger(t, ctx, db)
	if _, err := db.ExecContext(ctx, `
		CREATE FUNCTION juhe_business.w5_route_update_fail_binding_insert()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $function$
		BEGIN
			RAISE EXCEPTION 'w5 forced route strategy binding insert failure';
			RETURN NEW;
		END;
		$function$
	`); err != nil {
		t.Fatalf("create route strategy binding failure function: %v", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TRIGGER w5_route_update_fail_binding_insert
		BEFORE INSERT ON juhe_business.route_strategy_groups
		FOR EACH ROW
		WHEN (NEW.route_strategy_id = '%s')
		EXECUTE FUNCTION juhe_business.w5_route_update_fail_binding_insert()
	`, w5ManagementRouteStrategyUpdateRollbackID)); err != nil {
		t.Fatalf("create route strategy binding failure trigger: %v", err)
	}
}

func dropW5ManagementRouteStrategyUpdateBindingFailureTrigger(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		DROP TRIGGER IF EXISTS w5_route_update_fail_binding_insert
		ON juhe_business.route_strategy_groups
	`); err != nil {
		t.Fatalf("drop route strategy binding failure trigger: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		DROP FUNCTION IF EXISTS juhe_business.w5_route_update_fail_binding_insert()
	`); err != nil {
		t.Fatalf("drop route strategy binding failure function: %v", err)
	}
}

func assertW5ManagementRouteStrategySessionLastSeenAt(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	sessionID string,
	want time.Time,
) {
	t.Helper()
	var lastSeenAt time.Time
	if err := db.QueryRowContext(ctx, `
		SELECT last_seen_at
		FROM juhe_business.system_sessions
		WHERE id = $1
	`, sessionID).Scan(&lastSeenAt); err != nil {
		t.Fatalf("read route strategy update session %s last_seen_at: %v", sessionID, err)
	}
	if !lastSeenAt.UTC().Equal(want.UTC()) {
		t.Fatalf(
			"route strategy update session %s last_seen_at = %s, want %s",
			sessionID,
			lastSeenAt.UTC().Format(time.RFC3339Nano),
			want.UTC().Format(time.RFC3339Nano),
		)
	}
}

func assertW5ManagementRouteStrategyUpdateResponse(
	t *testing.T,
	got managementroutestrategies.DetailResult,
	wantCreatedAt time.Time,
	wantUpdatedAt time.Time,
) {
	t.Helper()
	if got.ID != w5ManagementRouteStrategyUpdateTargetID ||
		got.SystemAccountID != w5ManagementRouteStrategyUpdateOwnerID ||
		got.SystemAccountName != "W5 Route Update Owner" ||
		got.Name != "W5 Route Updated" ||
		got.Description != nil ||
		got.Mode != "weighted" ||
		got.Status != "disabled" ||
		!got.IsDefault ||
		got.NormalRoutingConfig != nil ||
		got.HybridRoutingConfig != nil ||
		got.APIKeyCount != 1 ||
		got.CreatedAt != wantCreatedAt.UTC().Format(time.RFC3339Nano) ||
		got.UpdatedAt != wantUpdatedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("route strategy update response = %+v", got)
	}
	if len(got.GroupBindings) != 2 {
		t.Fatalf("route strategy update bindings = %+v, want 2", got.GroupBindings)
	}
	assertW5ManagementRouteStrategyUpdateBinding(
		t,
		got.GroupBindings[0],
		"rsg_w5_route_update_1",
		w5ManagementRouteStrategyUpdatePrimaryID,
		"W5 Route Update Primary",
		1,
		70,
	)
	assertW5ManagementRouteStrategyUpdateBinding(
		t,
		got.GroupBindings[1],
		"rsg_w5_route_update_2",
		w5ManagementRouteStrategyUpdateBackupID,
		"W5 Route Update Backup",
		2,
		30,
	)
}

func assertW5ManagementRouteStrategyUpdateBinding(
	t *testing.T,
	got managementroutestrategies.GroupBindingSummary,
	wantID string,
	wantGroupID string,
	wantGroupName string,
	wantPriority int,
	wantWeight int,
) {
	t.Helper()
	if got.ID != wantID ||
		got.GroupID != wantGroupID ||
		got.GroupName != wantGroupName ||
		got.ProviderCode != "openai" ||
		got.Priority != wantPriority ||
		got.Weight != wantWeight ||
		got.Status != "active" ||
		!got.GroupEnabled {
		t.Fatalf("route strategy update binding = %+v", got)
	}
}

func assertW5ManagementRouteStrategyUpdateSelfResponse(
	t *testing.T,
	got managementroutestrategies.DetailResult,
	raw []byte,
	wantCreatedAt time.Time,
	wantUpdatedAt time.Time,
) {
	t.Helper()
	for _, ownerField := range []string{`"systemAccountId"`, `"systemAccountName"`} {
		if strings.Contains(string(raw), ownerField) {
			t.Fatalf("self route strategy response leaked %s: %s", ownerField, raw)
		}
	}
	if got.ID != w5ManagementRouteStrategyUpdateSelfID ||
		got.SystemAccountID != "" ||
		got.SystemAccountName != "" ||
		got.Name != "W5 Self Updated" ||
		got.Description != nil ||
		got.Mode != "normal" ||
		got.Status != "disabled" ||
		got.IsDefault ||
		got.NormalRoutingConfig == nil ||
		got.NormalRoutingConfig.SchedulingPreference != "cost_first" ||
		got.NormalRoutingConfig.SpeedFirstConfig != nil ||
		got.HybridRoutingConfig != nil ||
		got.APIKeyCount != 0 ||
		got.CreatedAt != wantCreatedAt.UTC().Format(time.RFC3339Nano) ||
		got.UpdatedAt != wantUpdatedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("self route strategy update response = %+v", got)
	}
	if len(got.GroupBindings) != 1 {
		t.Fatalf("self route strategy bindings = %+v, want 1", got.GroupBindings)
	}
	binding := got.GroupBindings[0]
	if binding.ID == w5ManagementRouteStrategySelfBinding ||
		binding.GroupID != w5ManagementRouteStrategyUpdateOldGroupID ||
		binding.GroupName != "W5 Route Update Old" ||
		binding.ProviderCode != "openai" ||
		binding.Priority != 1 ||
		binding.Weight != 1 ||
		binding.Status != "active" ||
		!binding.GroupEnabled {
		t.Fatalf("self route strategy binding = %+v", binding)
	}
}

func insertW5ManagementRouteStrategyUpdateFixtures(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	createdAt time.Time,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES
			($1, 'w5-route-update-admin', 'W5 Route Update Admin', NULL, 'admin', 'active', 'hash', false, false, $4, $4),
			($2, 'w5-route-update-owner', 'W5 Route Update Owner', NULL, 'user', 'active', 'hash', false, false, $4, $4),
			($3, 'w5-route-update-other', 'W5 Route Update Other', NULL, 'user', 'active', 'hash', false, false, $4, $4)
	`, w5ManagementRouteStrategyUpdateAdminID,
		w5ManagementRouteStrategyUpdateOwnerID,
		w5ManagementRouteStrategyUpdateOtherID,
		createdAt); err != nil {
		t.Fatalf("insert route strategy update accounts: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.groups (
			id, system_account_id, name, provider_code, description, enabled, is_default,
			group_type, scheduling_policy_json, created_at, updated_at
		) VALUES
			($1, $4, 'W5 Route Update Old', 'openai', NULL, true, false, 'personal', NULL, $5, $5),
			($2, $4, 'W5 Route Update Primary', 'openai', NULL, true, false, 'personal', NULL, $5, $5),
			($3, $4, 'W5 Route Update Backup', 'openai', NULL, true, false, 'personal', NULL, $5, $5)
	`, w5ManagementRouteStrategyUpdateOldGroupID,
		w5ManagementRouteStrategyUpdatePrimaryID,
		w5ManagementRouteStrategyUpdateBackupID,
		w5ManagementRouteStrategyUpdateOwnerID,
		createdAt); err != nil {
		t.Fatalf("insert route strategy update groups: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.route_strategies (
			id, system_account_id, name, description, mode, status, is_default,
			config_json, created_at, updated_at
		) VALUES
			(
				$1, $5, 'W5 Route Original', 'W5 original description', 'normal', 'active', true,
				'{"normalRoutingConfig":{"schedulingPreference":"speed_first","speedFirstConfig":{"firstByteThresholdMs":20000}}}',
				$6, $6
			),
			($2, $5, 'W5 Route Update Duplicate', NULL, 'normal', 'active', false, NULL, $6, $6),
			($3, $5, 'W5 Rollback Original', 'rollback original', 'normal', 'active', false, NULL, $6, $6),
			($4, $5, 'W5 Self Original', 'self original', 'normal', 'active', false, NULL, $6, $6)
	`, w5ManagementRouteStrategyUpdateTargetID,
		w5ManagementRouteStrategyUpdateDuplicateID,
		w5ManagementRouteStrategyUpdateRollbackID,
		w5ManagementRouteStrategyUpdateSelfID,
		w5ManagementRouteStrategyUpdateOwnerID,
		createdAt); err != nil {
		t.Fatalf("insert route strategy update routes: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.route_strategy_groups (
			id, route_strategy_id, system_account_id, group_id,
			priority, weight, status, created_at, updated_at
		) VALUES
			($1, $4, $7, $8, 1, 1, 'active', $9, $9),
			($2, $5, $7, $8, 1, 1, 'active', $9, $9),
			($3, $6, $7, $8, 1, 1, 'active', $9, $9)
	`, w5ManagementRouteStrategyUpdateOldBinding,
		w5ManagementRouteStrategyRollbackBinding,
		w5ManagementRouteStrategySelfBinding,
		w5ManagementRouteStrategyUpdateTargetID,
		w5ManagementRouteStrategyUpdateRollbackID,
		w5ManagementRouteStrategyUpdateSelfID,
		w5ManagementRouteStrategyUpdateOwnerID,
		w5ManagementRouteStrategyUpdateOldGroupID,
		createdAt); err != nil {
		t.Fatalf("insert route strategy update initial bindings: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.api_keys (
			id, system_account_id, route_strategy_id, name, description,
			key_hash, key_prefix, key_suffix, status, is_default, created_at, updated_at
		) VALUES (
			$1, $2, $3, 'W5 Route Update Key', NULL,
			'hash_w5_route_update', 'jua-w5', 'update', 'active', true, $4, $4
		)
	`, w5ManagementRouteStrategyUpdateAPIKeyID,
		w5ManagementRouteStrategyUpdateOwnerID,
		w5ManagementRouteStrategyUpdateTargetID,
		createdAt); err != nil {
		t.Fatalf("insert route strategy update api key: %v", err)
	}
}

func assertW5ManagementRouteStrategyUpdateDatabase(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	wantCreatedAt time.Time,
	wantUpdatedAt time.Time,
) {
	t.Helper()
	var (
		ownerID     string
		name        string
		description sql.NullString
		mode        string
		status      string
		isDefault   bool
		configJSON  sql.NullString
		createdAt   time.Time
		updatedAt   time.Time
	)
	if err := db.QueryRowContext(ctx, `
		SELECT
			system_account_id, name, description, mode, status, is_default,
			config_json, created_at, updated_at
		FROM juhe_business.route_strategies
		WHERE id = $1
	`, w5ManagementRouteStrategyUpdateTargetID).Scan(
		&ownerID,
		&name,
		&description,
		&mode,
		&status,
		&isDefault,
		&configJSON,
		&createdAt,
		&updatedAt,
	); err != nil {
		t.Fatalf("read route strategy update row: %v", err)
	}
	if ownerID != w5ManagementRouteStrategyUpdateOwnerID ||
		name != "W5 Route Updated" ||
		description.Valid ||
		mode != "weighted" ||
		status != "disabled" ||
		!isDefault ||
		configJSON.Valid ||
		!createdAt.UTC().Equal(wantCreatedAt.UTC()) ||
		!updatedAt.UTC().Equal(wantUpdatedAt.UTC()) {
		t.Fatalf(
			"route strategy row owner=%q name=%q description=%+v mode=%q status=%q default=%t config=%+v createdAt=%s updatedAt=%s",
			ownerID,
			name,
			description,
			mode,
			status,
			isDefault,
			configJSON,
			createdAt.UTC().Format(time.RFC3339Nano),
			updatedAt.UTC().Format(time.RFC3339Nano),
		)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, group_id, priority, weight, status, created_at, updated_at
		FROM juhe_business.route_strategy_groups
		WHERE route_strategy_id = $1
		ORDER BY priority ASC, id ASC
	`, w5ManagementRouteStrategyUpdateTargetID)
	if err != nil {
		t.Fatalf("read route strategy update bindings: %v", err)
	}
	defer rows.Close()
	type bindingRow struct {
		id        string
		groupID   string
		priority  int
		weight    int
		status    string
		createdAt time.Time
		updatedAt time.Time
	}
	var bindings []bindingRow
	for rows.Next() {
		var row bindingRow
		if err := rows.Scan(
			&row.id,
			&row.groupID,
			&row.priority,
			&row.weight,
			&row.status,
			&row.createdAt,
			&row.updatedAt,
		); err != nil {
			t.Fatalf("scan route strategy update binding: %v", err)
		}
		bindings = append(bindings, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate route strategy update bindings: %v", err)
	}
	if len(bindings) != 2 {
		t.Fatalf("persisted route strategy update bindings = %+v, want 2", bindings)
	}
	wantBindings := []bindingRow{
		{
			id:        "rsg_w5_route_update_1",
			groupID:   w5ManagementRouteStrategyUpdatePrimaryID,
			priority:  1,
			weight:    70,
			status:    "active",
			createdAt: wantUpdatedAt,
			updatedAt: wantUpdatedAt,
		},
		{
			id:        "rsg_w5_route_update_2",
			groupID:   w5ManagementRouteStrategyUpdateBackupID,
			priority:  2,
			weight:    30,
			status:    "active",
			createdAt: wantUpdatedAt,
			updatedAt: wantUpdatedAt,
		},
	}
	for index, want := range wantBindings {
		got := bindings[index]
		if got.id != want.id ||
			got.groupID != want.groupID ||
			got.priority != want.priority ||
			got.weight != want.weight ||
			got.status != want.status ||
			!got.createdAt.UTC().Equal(want.createdAt.UTC()) ||
			!got.updatedAt.UTC().Equal(want.updatedAt.UTC()) {
			t.Fatalf("persisted route strategy update binding[%d] = %+v, want %+v", index, got, want)
		}
	}
	var oldBindingCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_business.route_strategy_groups
		WHERE id = $1
	`, w5ManagementRouteStrategyUpdateOldBinding).Scan(&oldBindingCount); err != nil {
		t.Fatalf("count old route strategy binding: %v", err)
	}
	if oldBindingCount != 0 {
		t.Fatalf("old route strategy binding count = %d, want 0", oldBindingCount)
	}

	var (
		apiKeyOwnerID string
		apiKeyRouteID string
		apiKeyCount   int
	)
	if err := db.QueryRowContext(ctx, `
		SELECT system_account_id, route_strategy_id
		FROM juhe_business.api_keys
		WHERE id = $1
	`, w5ManagementRouteStrategyUpdateAPIKeyID).Scan(
		&apiKeyOwnerID,
		&apiKeyRouteID,
	); err != nil {
		t.Fatalf("read route strategy update api key reference: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_business.api_keys
		WHERE system_account_id = $1
		  AND route_strategy_id = $2
	`, w5ManagementRouteStrategyUpdateOwnerID,
		w5ManagementRouteStrategyUpdateTargetID).Scan(&apiKeyCount); err != nil {
		t.Fatalf("count route strategy update api key references: %v", err)
	}
	if apiKeyOwnerID != w5ManagementRouteStrategyUpdateOwnerID ||
		apiKeyRouteID != w5ManagementRouteStrategyUpdateTargetID ||
		apiKeyCount != 1 {
		t.Fatalf(
			"api key reference owner=%q route=%q count=%d",
			apiKeyOwnerID,
			apiKeyRouteID,
			apiKeyCount,
		)
	}
}

func assertW5ManagementRouteStrategyUpdateRollbackDatabase(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	wantTimestamp time.Time,
) {
	t.Helper()
	var (
		name        string
		description sql.NullString
		mode        string
		status      string
		configJSON  sql.NullString
		createdAt   time.Time
		updatedAt   time.Time
	)
	if err := db.QueryRowContext(ctx, `
		SELECT name, description, mode, status, config_json, created_at, updated_at
		FROM juhe_business.route_strategies
		WHERE id = $1
	`, w5ManagementRouteStrategyUpdateRollbackID).Scan(
		&name,
		&description,
		&mode,
		&status,
		&configJSON,
		&createdAt,
		&updatedAt,
	); err != nil {
		t.Fatalf("read rollback route strategy row: %v", err)
	}
	if name != "W5 Rollback Original" ||
		description != (sql.NullString{String: "rollback original", Valid: true}) ||
		mode != "normal" ||
		status != "active" ||
		configJSON.Valid ||
		!createdAt.UTC().Equal(wantTimestamp.UTC()) ||
		!updatedAt.UTC().Equal(wantTimestamp.UTC()) {
		t.Fatalf(
			"rollback route row name=%q description=%+v mode=%q status=%q config=%+v createdAt=%s updatedAt=%s",
			name,
			description,
			mode,
			status,
			configJSON,
			createdAt.UTC().Format(time.RFC3339Nano),
			updatedAt.UTC().Format(time.RFC3339Nano),
		)
	}
	var (
		bindingID        string
		groupID          string
		priority         int
		weight           int
		bindingStatus    string
		bindingCreatedAt time.Time
		bindingUpdatedAt time.Time
		bindingCount     int
	)
	if err := db.QueryRowContext(ctx, `
		SELECT id, group_id, priority, weight, status, created_at, updated_at
		FROM juhe_business.route_strategy_groups
		WHERE route_strategy_id = $1
	`, w5ManagementRouteStrategyUpdateRollbackID).Scan(
		&bindingID,
		&groupID,
		&priority,
		&weight,
		&bindingStatus,
		&bindingCreatedAt,
		&bindingUpdatedAt,
	); err != nil {
		t.Fatalf("read rollback route strategy binding: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_business.route_strategy_groups
		WHERE route_strategy_id = $1
	`, w5ManagementRouteStrategyUpdateRollbackID).Scan(&bindingCount); err != nil {
		t.Fatalf("count rollback route strategy bindings: %v", err)
	}
	if bindingCount != 1 ||
		bindingID != w5ManagementRouteStrategyRollbackBinding ||
		groupID != w5ManagementRouteStrategyUpdateOldGroupID ||
		priority != 1 ||
		weight != 1 ||
		bindingStatus != "active" ||
		!bindingCreatedAt.UTC().Equal(wantTimestamp.UTC()) ||
		!bindingUpdatedAt.UTC().Equal(wantTimestamp.UTC()) {
		t.Fatalf(
			"rollback binding count=%d id=%q group=%q priority=%d weight=%d status=%q createdAt=%s updatedAt=%s",
			bindingCount,
			bindingID,
			groupID,
			priority,
			weight,
			bindingStatus,
			bindingCreatedAt.UTC().Format(time.RFC3339Nano),
			bindingUpdatedAt.UTC().Format(time.RFC3339Nano),
		)
	}
}

func assertW5ManagementRouteStrategyUpdateSelfDatabase(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	wantCreatedAt time.Time,
	wantUpdatedAt time.Time,
) {
	t.Helper()
	var (
		name        string
		description sql.NullString
		mode        string
		status      string
		isDefault   bool
		configJSON  sql.NullString
		createdAt   time.Time
		updatedAt   time.Time
	)
	if err := db.QueryRowContext(ctx, `
		SELECT name, description, mode, status, is_default, config_json, created_at, updated_at
		FROM juhe_business.route_strategies
		WHERE id = $1
	`, w5ManagementRouteStrategyUpdateSelfID).Scan(
		&name,
		&description,
		&mode,
		&status,
		&isDefault,
		&configJSON,
		&createdAt,
		&updatedAt,
	); err != nil {
		t.Fatalf("read self route strategy row: %v", err)
	}
	if name != "W5 Self Updated" ||
		description.Valid ||
		mode != "normal" ||
		status != "disabled" ||
		isDefault ||
		configJSON.Valid ||
		!createdAt.UTC().Equal(wantCreatedAt.UTC()) ||
		!updatedAt.UTC().Equal(wantUpdatedAt.UTC()) {
		t.Fatalf(
			"self route row name=%q description=%+v mode=%q status=%q default=%t config=%+v createdAt=%s updatedAt=%s",
			name,
			description,
			mode,
			status,
			isDefault,
			configJSON,
			createdAt.UTC().Format(time.RFC3339Nano),
			updatedAt.UTC().Format(time.RFC3339Nano),
		)
	}
	var (
		bindingID        string
		groupID          string
		priority         int
		weight           int
		bindingStatus    string
		bindingCreatedAt time.Time
		bindingUpdatedAt time.Time
		bindingCount     int
	)
	if err := db.QueryRowContext(ctx, `
		SELECT id, group_id, priority, weight, status, created_at, updated_at
		FROM juhe_business.route_strategy_groups
		WHERE route_strategy_id = $1
	`, w5ManagementRouteStrategyUpdateSelfID).Scan(
		&bindingID,
		&groupID,
		&priority,
		&weight,
		&bindingStatus,
		&bindingCreatedAt,
		&bindingUpdatedAt,
	); err != nil {
		t.Fatalf("read self route strategy binding: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_business.route_strategy_groups
		WHERE route_strategy_id = $1
	`, w5ManagementRouteStrategyUpdateSelfID).Scan(&bindingCount); err != nil {
		t.Fatalf("count self route strategy bindings: %v", err)
	}
	if bindingCount != 1 ||
		bindingID == w5ManagementRouteStrategySelfBinding ||
		groupID != w5ManagementRouteStrategyUpdateOldGroupID ||
		priority != 1 ||
		weight != 1 ||
		bindingStatus != "active" ||
		!bindingCreatedAt.UTC().Equal(wantUpdatedAt.UTC()) ||
		!bindingUpdatedAt.UTC().Equal(wantUpdatedAt.UTC()) {
		t.Fatalf(
			"self binding count=%d id=%q group=%q priority=%d weight=%d status=%q createdAt=%s updatedAt=%s",
			bindingCount,
			bindingID,
			groupID,
			priority,
			weight,
			bindingStatus,
			bindingCreatedAt.UTC().Format(time.RFC3339Nano),
			bindingUpdatedAt.UTC().Format(time.RFC3339Nano),
		)
	}
}

type w5ManagementRouteStrategyUpdateInvalidator struct {
	mu      sync.Mutex
	reasons []string
}

func (i *w5ManagementRouteStrategyUpdateInvalidator) InvalidateGatewayRuntime(
	_ context.Context,
	reason string,
) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.reasons = append(i.reasons, reason)
	return nil
}

func (i *w5ManagementRouteStrategyUpdateInvalidator) snapshot() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.reasons...)
}

type w5ManagementRouteStrategyUpdateOperationLogQueue struct {
	mu       sync.Mutex
	logs     []port.OperationLogInput
	payloads [][]byte
}

func (q *w5ManagementRouteStrategyUpdateOperationLogQueue) Enqueue(
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
	rawPayload := append([]byte(nil), payload...)
	input, err := operationlogjob.DecodeWriteTaskPayload(payload)
	if err != nil {
		return queue.TaskInfo{}, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.logs = append(q.logs, input)
	q.payloads = append(q.payloads, rawPayload)
	return queue.TaskInfo{
		ID:    fmt.Sprintf("task_w5_route_update_%d", len(q.logs)),
		Queue: opts.Queue,
		Type:  taskType,
	}, nil
}

func (q *w5ManagementRouteStrategyUpdateOperationLogQueue) snapshot() (
	[]port.OperationLogInput,
	[][]byte,
) {
	q.mu.Lock()
	defer q.mu.Unlock()
	logs := append([]port.OperationLogInput(nil), q.logs...)
	payloads := make([][]byte, 0, len(q.payloads))
	for _, payload := range q.payloads {
		payloads = append(payloads, append([]byte(nil), payload...))
	}
	return logs, payloads
}

func assertW5ManagementRouteStrategyUpdateSideEffects(
	t *testing.T,
	invalidator *w5ManagementRouteStrategyUpdateInvalidator,
	operationLogs *w5ManagementRouteStrategyUpdateOperationLogQueue,
	logIDCalls int,
	wantCount int,
) {
	t.Helper()
	reasons := invalidator.snapshot()
	if len(reasons) != wantCount {
		t.Fatalf("route strategy update invalidations = %#v, want %d", reasons, wantCount)
	}
	for index, reason := range reasons {
		if reason != managementroutestrategies.RouteStrategyUpdatedReason {
			t.Fatalf(
				"route strategy update invalidation[%d] = %q, want %q",
				index,
				reason,
				managementroutestrategies.RouteStrategyUpdatedReason,
			)
		}
	}
	logs, payloads := operationLogs.snapshot()
	if logIDCalls != wantCount ||
		len(logs) != wantCount ||
		len(payloads) != wantCount {
		t.Fatalf(
			"route strategy update log ids=%d logs=%d payloads=%d, want %d",
			logIDCalls,
			len(logs),
			len(payloads),
			wantCount,
		)
	}
	assertW5ManagementRouteStrategyUpdateOperationLog(t, logs[0])
	assertW5ManagementRouteStrategyUpdateRawOperationLogSafe(t, payloads[0])
	if wantCount == 2 {
		assertW5ManagementRouteStrategyUpdateSelfOperationLog(t, logs[1])
		assertW5ManagementRouteStrategyUpdateRawOperationLogSafe(t, payloads[1])
	}
}

func assertW5ManagementRouteStrategyUpdateOperationLog(
	t *testing.T,
	logInput port.OperationLogInput,
) {
	t.Helper()
	if logInput.ID != "oplog_w5_route_update_1" ||
		logInput.TraceID != "req_w5_route_update_success" ||
		logInput.ActorSystemAccountID != w5ManagementRouteStrategyUpdateAdminID ||
		logInput.ActorUsername != "w5-route-update-admin" ||
		logInput.ActorDisplayName != "W5 Route Update Admin" ||
		logInput.ActorRole != "admin" ||
		logInput.OperationScopeSystemAccountID != w5ManagementRouteStrategyUpdateOwnerID ||
		logInput.Mode != "admin" ||
		logInput.Module != "route_strategies" ||
		logInput.Action != "update" ||
		logInput.OperationKey != "route_strategies.update" ||
		logInput.ResourceType != "route_strategy" ||
		logInput.ResourceID != w5ManagementRouteStrategyUpdateTargetID ||
		logInput.ResourceName != "W5 Route Updated" ||
		logInput.Summary != "更新策略路由：W5 Route Updated" ||
		logInput.DetailLevel != "full" ||
		logInput.VisibilityScope != "targeted" ||
		logInput.Method != http.MethodPatch ||
		logInput.Path != "/__aisys__/api/route-strategies/"+w5ManagementRouteStrategyUpdateTargetID ||
		logInput.ClientIP != "127.0.0.1" ||
		logInput.UserAgent != "w5-management-route-strategy-update-smoke" ||
		!logInput.CreatedAt.UTC().Equal(time.Date(2026, 7, 12, 11, 45, 0, 0, time.UTC)) {
		t.Fatalf("route strategy update operation log = %+v", logInput)
	}
	if logInput.StatusCode == nil || *logInput.StatusCode != http.StatusOK {
		t.Fatalf("route strategy update operation log status = %+v, want 200", logInput.StatusCode)
	}
	if len(logInput.Viewers) != 1 ||
		logInput.Viewers[0].SystemAccountID != w5ManagementRouteStrategyUpdateOwnerID ||
		logInput.Viewers[0].VisibilityReason != "resource_owner" ||
		logInput.Viewers[0].DetailLevel != "full" {
		t.Fatalf("route strategy update operation log viewers = %+v", logInput.Viewers)
	}
	wantFields := []string{
		"name",
		"description",
		"mode",
		"status",
		"groupBindings",
		"normalRoutingConfig",
	}
	if len(logInput.Changes) != len(wantFields) {
		t.Fatalf("route strategy update operation log changes = %+v", logInput.Changes)
	}
	for index, wantField := range wantFields {
		if logInput.Changes[index].Field != wantField ||
			logInput.Changes[index].Sensitive {
			t.Fatalf("route strategy update operation log change[%d] = %+v, want safe %q", index, logInput.Changes[index], wantField)
		}
	}
	if logInput.Changes[0].Before != "W5 Route Original" ||
		logInput.Changes[0].After != "W5 Route Updated" ||
		logInput.Changes[1].Before != "W5 original description" ||
		logInput.Changes[1].After != nil ||
		logInput.Changes[2].Before != "normal" ||
		logInput.Changes[2].After != "weighted" ||
		logInput.Changes[3].Before != "active" ||
		logInput.Changes[3].After != "disabled" {
		t.Fatalf("route strategy update scalar operation log changes = %+v", logInput.Changes[:4])
	}
}

func assertW5ManagementRouteStrategyUpdateSelfOperationLog(
	t *testing.T,
	logInput port.OperationLogInput,
) {
	t.Helper()
	if logInput.ID != "oplog_w5_route_update_2" ||
		logInput.TraceID != "req_w5_route_update_self_success" ||
		logInput.ActorSystemAccountID != w5ManagementRouteStrategyUpdateOwnerID ||
		logInput.ActorUsername != "w5-route-update-owner" ||
		logInput.ActorDisplayName != "W5 Route Update Owner" ||
		logInput.ActorRole != "user" ||
		logInput.OperationScopeSystemAccountID != w5ManagementRouteStrategyUpdateOwnerID ||
		logInput.Mode != "self" ||
		logInput.Module != "route_strategies" ||
		logInput.Action != "update" ||
		logInput.OperationKey != "route_strategies.update" ||
		logInput.ResourceType != "route_strategy" ||
		logInput.ResourceID != w5ManagementRouteStrategyUpdateSelfID ||
		logInput.ResourceName != "W5 Self Updated" ||
		logInput.Summary != "更新策略路由：W5 Self Updated" ||
		logInput.DetailLevel != "full" ||
		logInput.VisibilityScope != "targeted" ||
		logInput.Method != http.MethodPatch ||
		logInput.Path != "/__aisys__/api/my-route-strategies/"+w5ManagementRouteStrategyUpdateSelfID ||
		logInput.ClientIP != "127.0.0.1" ||
		logInput.UserAgent != "w5-management-route-strategy-update-smoke" ||
		!logInput.CreatedAt.UTC().Equal(time.Date(2026, 7, 12, 11, 45, 0, 0, time.UTC)) {
		t.Fatalf("self route strategy update operation log = %+v", logInput)
	}
	if logInput.StatusCode == nil || *logInput.StatusCode != http.StatusOK {
		t.Fatalf("self route strategy update operation log status = %+v, want 200", logInput.StatusCode)
	}
	if len(logInput.Viewers) != 1 ||
		logInput.Viewers[0].SystemAccountID != w5ManagementRouteStrategyUpdateOwnerID ||
		logInput.Viewers[0].VisibilityReason != "resource_owner" ||
		logInput.Viewers[0].DetailLevel != "full" {
		t.Fatalf("self route strategy update operation log viewers = %+v", logInput.Viewers)
	}
	wantFields := []string{"name", "description", "status"}
	if len(logInput.Changes) != len(wantFields) {
		t.Fatalf("self route strategy update operation log changes = %+v", logInput.Changes)
	}
	for index, wantField := range wantFields {
		if logInput.Changes[index].Field != wantField ||
			logInput.Changes[index].Sensitive {
			t.Fatalf(
				"self route strategy update operation log change[%d] = %+v, want safe %q",
				index,
				logInput.Changes[index],
				wantField,
			)
		}
	}
	if logInput.Changes[0].Before != "W5 Self Original" ||
		logInput.Changes[0].After != "W5 Self Updated" ||
		logInput.Changes[1].Before != "self original" ||
		logInput.Changes[1].After != nil ||
		logInput.Changes[2].Before != "active" ||
		logInput.Changes[2].After != "disabled" {
		t.Fatalf("self route strategy scalar operation log changes = %+v", logInput.Changes[:3])
	}
}

func assertW5ManagementRouteStrategyUpdateRawOperationLogSafe(
	t *testing.T,
	payload []byte,
) {
	t.Helper()
	raw := string(payload)
	for _, forbidden := range []string{
		w5ManagementRouteStrategyUpdateAdminToken,
		w5ManagementRouteStrategyUpdateOwnerToken,
		w5ManagementRouteStrategyUpdateOtherToken,
		"hash_w5_route_update",
		managementauth.SessionCookieName,
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("raw route strategy operation log leaked %q: %s", forbidden, payload)
		}
	}
	lower := strings.ToLower(raw)
	for _, forbidden := range []string{
		`"authorization"`,
		`"cookie"`,
		`"secret"`,
		`"token"`,
		"bearer ",
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("raw route strategy operation log leaked %q: %s", forbidden, payload)
		}
	}
}
