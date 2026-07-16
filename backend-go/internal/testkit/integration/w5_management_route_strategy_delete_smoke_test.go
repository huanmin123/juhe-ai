//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	w5RouteDeleteAdminID = "sys_w5_route_delete_admin"
	w5RouteDeleteOwnerID = "sys_w5_route_delete_owner"
	w5RouteDeleteOtherID = "sys_w5_route_delete_other"

	w5RouteDeleteAdminSession = "sess_w5_route_delete_admin"
	w5RouteDeleteOwnerSession = "sess_w5_route_delete_owner"
	w5RouteDeleteAdminToken   = "w5-route-delete-admin-session"
	w5RouteDeleteOwnerToken   = "w5-route-delete-owner-session"

	w5RouteDeleteAdminGlobalID = "route_w5_route_delete_admin_global"
	w5RouteDeleteOwnerScopeID  = "route_w5_route_delete_owner_scope"
	w5RouteDeleteSelfCrossID   = "route_w5_route_delete_self_cross"
	w5RouteDeleteSelfID        = "route_w5_route_delete_self"
	w5RouteDeleteDefaultID     = "route_w5_route_delete_default"
	w5RouteDeleteAPIKeyID      = "route_w5_route_delete_api_key"
	w5RouteDeleteRollbackID    = "route_w5_route_delete_rollback"

	w5RouteDeleteOwnerGroupID = "grp_w5_route_delete_owner"
	w5RouteDeleteOtherGroupID = "grp_w5_route_delete_other"
	w5RouteDeleteAPIKeyRefID  = "key_w5_route_delete_reference"

	w5RouteDeleteAdminDescriptionCanary = "w5-route-delete-admin-description-canary"
	w5RouteDeleteAdminConfigCanary      = "w5-route-delete-admin-config-canary"
	w5RouteDeleteSelfDescriptionCanary  = "w5-route-delete-self-description-canary"
	w5RouteDeleteSelfConfigCanary       = "w5-route-delete-self-config-canary"
	w5RouteDeleteBindingCanary          = "w5-route-delete-binding-canary"
)

func TestW5ManagementRouteStrategyDeletePostgresSmoke(t *testing.T) {
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

	now := time.Date(2026, 7, 12, 13, 30, 0, 0, time.UTC)
	createdAt := now.Add(-24 * time.Hour)
	insertW5RouteDeleteFixtures(t, ctx, db, createdAt)
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w5RouteDeleteAdminSession,
		w5RouteDeleteAdminID,
		w5RouteDeleteAdminToken,
		now.Add(-10*time.Minute),
	)
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w5RouteDeleteOwnerSession,
		w5RouteDeleteOwnerID,
		w5RouteDeleteOwnerToken,
		now.Add(-10*time.Minute),
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
	invalidator := &w5RouteDeleteInvalidator{}
	service := managementroutestrategies.NewServiceWithOptions(
		managementroutestrategies.ServiceOptions{
			CreateStore: store,
			Transactor:  store,
			Invalidator: invalidator,
			Logger:      logger,
			Now:         func() time.Time { return now },
		},
	)
	operationLogs := &w5RouteDeleteOperationLogQueue{}
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
			return fmt.Sprintf("oplog_w5_route_delete_%d", logIDCalls)
		},
	}
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                           cfg,
		Logger:                           logger,
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementRouteStrategyDeleteHandler: httpapi.NewManagementRouteStrategyDeleteHandlerWithOperationLog(
			service,
			logOptions,
		),
		ManagementMyRouteStrategyDeleteHandler: httpapi.NewManagementMyRouteStrategyDeleteHandlerWithOperationLog(
			service,
			logOptions,
		),
	})

	adminGlobal := serveW5RouteDeleteRequest(
		router,
		"/__aisys__/api/route-strategies/"+w5RouteDeleteAdminGlobalID,
		w5RouteDeleteAdminToken,
		"req_w5_route_delete_admin_global",
	)
	assertW5RouteDeleteEmpty204(t, adminGlobal)
	assertW5RouteDeleteRouteExists(t, ctx, db, w5RouteDeleteAdminGlobalID, false)
	assertW5RouteDeleteBindingExists(t, ctx, db, w5RouteDeleteAdminGlobalID, false)
	assertW5RouteDeleteSideEffectCounts(t, invalidator, operationLogs, logIDCalls, 1)

	adminWrongOwner := serveW5RouteDeleteRequest(
		router,
		"/__aisys__/api/route-strategies/"+w5RouteDeleteOwnerScopeID+
			"?systemAccountId="+w5RouteDeleteOtherID,
		w5RouteDeleteAdminToken,
		"req_w5_route_delete_admin_wrong_owner",
	)
	assertW5RouteDeleteError(
		t,
		adminWrongOwner,
		http.StatusNotFound,
		"策略路由不存在",
	)
	assertW5RouteDeleteRouteExists(t, ctx, db, w5RouteDeleteOwnerScopeID, true)
	assertW5RouteDeleteBindingExists(t, ctx, db, w5RouteDeleteOwnerScopeID, true)
	assertW5RouteDeleteSideEffectCounts(t, invalidator, operationLogs, logIDCalls, 1)

	selfCrossOwner := serveW5RouteDeleteRequest(
		router,
		"/__aisys__/api/my-route-strategies/"+w5RouteDeleteSelfCrossID,
		w5RouteDeleteOwnerToken,
		"req_w5_route_delete_self_cross_owner",
	)
	assertW5RouteDeleteError(
		t,
		selfCrossOwner,
		http.StatusNotFound,
		"策略路由不存在",
	)
	assertW5RouteDeleteRouteExists(t, ctx, db, w5RouteDeleteSelfCrossID, true)
	assertW5RouteDeleteBindingExists(t, ctx, db, w5RouteDeleteSelfCrossID, true)
	assertW5RouteDeleteSideEffectCounts(t, invalidator, operationLogs, logIDCalls, 1)

	selfSuccess := serveW5RouteDeleteRequest(
		router,
		"/__aisys__/api/my-route-strategies/"+w5RouteDeleteSelfID+
			"?systemAccountId="+w5RouteDeleteOtherID,
		w5RouteDeleteOwnerToken,
		"req_w5_route_delete_self_success",
	)
	assertW5RouteDeleteEmpty204(t, selfSuccess)
	assertW5RouteDeleteRouteExists(t, ctx, db, w5RouteDeleteSelfID, false)
	assertW5RouteDeleteBindingExists(t, ctx, db, w5RouteDeleteSelfID, false)
	assertW5RouteDeleteSideEffectCounts(t, invalidator, operationLogs, logIDCalls, 2)

	_, err = service.Delete(ctx, managementroutestrategies.DeleteInput{
		ActorSystemAccountID: w5RouteDeleteAdminID,
		ActorRole:            "admin",
		RouteStrategyID:      w5RouteDeleteDefaultID,
	})
	var defaultConflict *managementroutestrategies.DeleteConflictError
	if !errors.As(err, &defaultConflict) ||
		defaultConflict.Kind != managementroutestrategies.DeleteConflictDefault {
		t.Fatalf("default route delete error = %T %v, want typed default conflict", err, err)
	}
	defaultResponse := serveW5RouteDeleteRequest(
		router,
		"/__aisys__/api/route-strategies/"+w5RouteDeleteDefaultID+
			"?systemAccountId="+w5RouteDeleteOwnerID,
		w5RouteDeleteAdminToken,
		"req_w5_route_delete_default",
	)
	assertW5RouteDeleteError(
		t,
		defaultResponse,
		http.StatusBadRequest,
		"默认策略路由不允许删除",
	)
	assertW5RouteDeleteRouteExists(t, ctx, db, w5RouteDeleteDefaultID, true)
	assertW5RouteDeleteBindingExists(t, ctx, db, w5RouteDeleteDefaultID, true)
	assertW5RouteDeleteSideEffectCounts(t, invalidator, operationLogs, logIDCalls, 2)

	apiKeyResponse := serveW5RouteDeleteRequest(
		router,
		"/__aisys__/api/route-strategies/"+w5RouteDeleteAPIKeyID+
			"?systemAccountId="+w5RouteDeleteOwnerID,
		w5RouteDeleteAdminToken,
		"req_w5_route_delete_api_key",
	)
	assertW5RouteDeleteError(
		t,
		apiKeyResponse,
		http.StatusBadRequest,
		"策略路由已被 1 个 API Key 使用，请先解绑",
	)
	assertW5RouteDeleteRouteExists(t, ctx, db, w5RouteDeleteAPIKeyID, true)
	assertW5RouteDeleteBindingExists(t, ctx, db, w5RouteDeleteAPIKeyID, true)
	assertW5RouteDeleteAPIKeyReference(t, ctx, db)
	assertW5RouteDeleteSideEffectCounts(t, invalidator, operationLogs, logIDCalls, 2)

	installW5RouteDeleteBindingFailureTrigger(t, ctx, db)
	triggerInstalled := true
	defer func() {
		if triggerInstalled {
			dropW5RouteDeleteBindingFailureTrigger(t, ctx, db)
		}
	}()
	rollbackResponse := serveW5RouteDeleteRequest(
		router,
		"/__aisys__/api/route-strategies/"+w5RouteDeleteRollbackID+
			"?systemAccountId="+w5RouteDeleteOwnerID,
		w5RouteDeleteAdminToken,
		"req_w5_route_delete_rollback",
	)
	assertW5RouteDeleteError(
		t,
		rollbackResponse,
		http.StatusInternalServerError,
		"服务器内部错误",
	)
	assertW5RouteDeleteRouteExists(t, ctx, db, w5RouteDeleteRollbackID, true)
	assertW5RouteDeleteBindingExists(t, ctx, db, w5RouteDeleteRollbackID, true)
	assertW5RouteDeleteSideEffectCounts(t, invalidator, operationLogs, logIDCalls, 2)
	dropW5RouteDeleteBindingFailureTrigger(t, ctx, db)
	triggerInstalled = false

	assertW5RouteDeleteInvalidations(t, invalidator)
	assertW5RouteDeleteOperationLogs(t, operationLogs, now)
}

func serveW5RouteDeleteRequest(
	router http.Handler,
	target string,
	sessionToken string,
	requestID string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, target, nil)
	req.Header.Set("Cookie", managementauth.SessionCookieName+"="+sessionToken)
	req.Header.Set("User-Agent", "w5-management-route-strategy-delete-smoke")
	req.Header.Set("X-Request-Id", requestID)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func assertW5RouteDeleteEmpty204(
	t *testing.T,
	rec *httptest.ResponseRecorder,
) {
	t.Helper()
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf(
			"route strategy delete status = %d, body = %q, want empty 204",
			rec.Code,
			rec.Body.String(),
		)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("route strategy delete Cache-Control = %q, want no-store", got)
	}
}

func assertW5RouteDeleteError(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	wantStatus int,
	wantMessage string,
) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf(
			"route strategy delete status = %d, body = %s, want %d",
			rec.Code,
			rec.Body.String(),
			wantStatus,
		)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("route strategy delete Cache-Control = %q, want no-store", got)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode route strategy delete error: %v", err)
	}
	if body["message"] != wantMessage {
		t.Fatalf(
			"route strategy delete message = %q, want %q",
			body["message"],
			wantMessage,
		)
	}
}

func insertW5RouteDeleteFixtures(
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
			($1, 'w5-route-delete-admin', 'W5 Route Delete Admin', NULL, 'admin', 'active', 'hash', false, false, $4, $4),
			($2, 'w5-route-delete-owner', 'W5 Route Delete Owner', NULL, 'user', 'active', 'hash', false, false, $4, $4),
			($3, 'w5-route-delete-other', 'W5 Route Delete Other', NULL, 'user', 'active', 'hash', false, false, $4, $4)
	`, w5RouteDeleteAdminID, w5RouteDeleteOwnerID, w5RouteDeleteOtherID, now); err != nil {
		t.Fatalf("insert route strategy delete accounts: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.groups (
			id, system_account_id, name, provider_code, description, enabled, is_default,
			group_type, scheduling_policy_json, created_at, updated_at
		) VALUES
			($1, $3, $5, 'openai', NULL, true, false, 'personal', NULL, $4, $4),
			($2, $6, 'W5 Route Delete Other Group', 'openai', NULL, true, false, 'personal', NULL, $4, $4)
	`, w5RouteDeleteOwnerGroupID,
		w5RouteDeleteOtherGroupID,
		w5RouteDeleteOwnerID,
		now,
		w5RouteDeleteBindingCanary,
		w5RouteDeleteOtherID); err != nil {
		t.Fatalf("insert route strategy delete groups: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.route_strategies (
			id, system_account_id, name, description, mode, status, is_default,
			config_json, created_at, updated_at
		) VALUES
			($1, $8, 'W5 Delete Admin Global', $10, 'weighted', 'active', false, $11, $9, $9),
			($2, $8, 'W5 Delete Owner Scope', NULL, 'normal', 'active', false, NULL, $9, $9),
			($3, $12, 'W5 Delete Self Cross', NULL, 'normal', 'active', false, NULL, $9, $9),
			($4, $8, 'W5 Delete Self', $13, 'hybrid_smart', 'disabled', false, $14, $9, $9),
			($5, $8, 'W5 Delete Default', NULL, 'normal', 'active', true, NULL, $9, $9),
			($6, $8, 'W5 Delete API Key', NULL, 'normal', 'active', false, NULL, $9, $9),
			($7, $8, 'W5 Delete Rollback', 'rollback must remain', 'normal', 'active', false, NULL, $9, $9)
	`, w5RouteDeleteAdminGlobalID,
		w5RouteDeleteOwnerScopeID,
		w5RouteDeleteSelfCrossID,
		w5RouteDeleteSelfID,
		w5RouteDeleteDefaultID,
		w5RouteDeleteAPIKeyID,
		w5RouteDeleteRollbackID,
		w5RouteDeleteOwnerID,
		now,
		w5RouteDeleteAdminDescriptionCanary,
		`{"canary":"`+w5RouteDeleteAdminConfigCanary+`"}`,
		w5RouteDeleteOtherID,
		w5RouteDeleteSelfDescriptionCanary,
		`{"canary":"`+w5RouteDeleteSelfConfigCanary+`"}`); err != nil {
		t.Fatalf("insert route strategy delete routes: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.route_strategy_groups (
			id, route_strategy_id, system_account_id, group_id,
			priority, weight, status, created_at, updated_at
		) VALUES
			('rsg_w5_route_delete_admin_global', $1, $8, $10, 1, 100, 'active', $9, $9),
			('rsg_w5_route_delete_owner_scope', $2, $8, $10, 1, 1, 'active', $9, $9),
			('rsg_w5_route_delete_self_cross', $3, $11, $12, 1, 1, 'active', $9, $9),
			('rsg_w5_route_delete_self', $4, $8, $10, 1, 1, 'active', $9, $9),
			('rsg_w5_route_delete_default', $5, $8, $10, 1, 1, 'active', $9, $9),
			('rsg_w5_route_delete_api_key', $6, $8, $10, 1, 1, 'active', $9, $9),
			('rsg_w5_route_delete_rollback', $7, $8, $10, 1, 1, 'active', $9, $9)
	`, w5RouteDeleteAdminGlobalID,
		w5RouteDeleteOwnerScopeID,
		w5RouteDeleteSelfCrossID,
		w5RouteDeleteSelfID,
		w5RouteDeleteDefaultID,
		w5RouteDeleteAPIKeyID,
		w5RouteDeleteRollbackID,
		w5RouteDeleteOwnerID,
		now,
		w5RouteDeleteOwnerGroupID,
		w5RouteDeleteOtherID,
		w5RouteDeleteOtherGroupID); err != nil {
		t.Fatalf("insert route strategy delete bindings: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.api_keys (
			id, system_account_id, route_strategy_id, name, description,
			key_hash, key_prefix, key_suffix, status, is_default, created_at, updated_at
		) VALUES (
			$1, $2, $3, 'W5 Route Delete Reference', NULL,
			'hash_w5_route_delete_reference', 'jua-w5', 'delete', 'active', false, $4, $4
		)
	`, w5RouteDeleteAPIKeyRefID,
		w5RouteDeleteOwnerID,
		w5RouteDeleteAPIKeyID,
		now); err != nil {
		t.Fatalf("insert route strategy delete API Key: %v", err)
	}
}

func assertW5RouteDeleteRouteExists(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	routeStrategyID string,
	want bool,
) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_business.route_strategies
		WHERE id = $1
	`, routeStrategyID).Scan(&count); err != nil {
		t.Fatalf("count route strategy %s: %v", routeStrategyID, err)
	}
	if got := count == 1; got != want {
		t.Fatalf("route strategy %s exists = %t, want %t", routeStrategyID, got, want)
	}
}

func assertW5RouteDeleteBindingExists(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	routeStrategyID string,
	want bool,
) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_business.route_strategy_groups
		WHERE route_strategy_id = $1
	`, routeStrategyID).Scan(&count); err != nil {
		t.Fatalf("count route strategy bindings %s: %v", routeStrategyID, err)
	}
	if got := count == 1; got != want {
		t.Fatalf("route strategy %s binding exists = %t, want %t", routeStrategyID, got, want)
	}
}

func assertW5RouteDeleteAPIKeyReference(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) {
	t.Helper()
	var (
		ownerID string
		routeID string
	)
	if err := db.QueryRowContext(ctx, `
		SELECT system_account_id, route_strategy_id
		FROM juhe_business.api_keys
		WHERE id = $1
	`, w5RouteDeleteAPIKeyRefID).Scan(&ownerID, &routeID); err != nil {
		t.Fatalf("read route strategy delete API Key reference: %v", err)
	}
	if ownerID != w5RouteDeleteOwnerID || routeID != w5RouteDeleteAPIKeyID {
		t.Fatalf("route strategy delete API Key owner=%q route=%q", ownerID, routeID)
	}
}

func installW5RouteDeleteBindingFailureTrigger(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) {
	t.Helper()
	dropW5RouteDeleteBindingFailureTrigger(t, ctx, db)
	if _, err := db.ExecContext(ctx, `
		CREATE FUNCTION juhe_business.w5_route_delete_fail_binding_delete()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $function$
		BEGIN
			RAISE EXCEPTION 'w5 forced route strategy binding delete failure';
			RETURN OLD;
		END;
		$function$
	`); err != nil {
		t.Fatalf("create route strategy binding delete failure function: %v", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TRIGGER w5_route_delete_fail_binding_delete
		BEFORE DELETE ON juhe_business.route_strategy_groups
		FOR EACH ROW
		WHEN (OLD.route_strategy_id = '%s')
		EXECUTE FUNCTION juhe_business.w5_route_delete_fail_binding_delete()
	`, w5RouteDeleteRollbackID)); err != nil {
		t.Fatalf("create route strategy binding delete failure trigger: %v", err)
	}
}

func dropW5RouteDeleteBindingFailureTrigger(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		DROP TRIGGER IF EXISTS w5_route_delete_fail_binding_delete
		ON juhe_business.route_strategy_groups
	`); err != nil {
		t.Fatalf("drop route strategy binding delete failure trigger: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		DROP FUNCTION IF EXISTS juhe_business.w5_route_delete_fail_binding_delete()
	`); err != nil {
		t.Fatalf("drop route strategy binding delete failure function: %v", err)
	}
}

type w5RouteDeleteInvalidator struct {
	mu      sync.Mutex
	reasons []string
}

func (i *w5RouteDeleteInvalidator) InvalidateGatewayRuntime(
	_ context.Context,
	reason string,
) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.reasons = append(i.reasons, reason)
	return nil
}

func (i *w5RouteDeleteInvalidator) snapshot() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.reasons...)
}

type w5RouteDeleteOperationLogQueue struct {
	mu       sync.Mutex
	logs     []port.OperationLogInput
	payloads [][]byte
}

func (q *w5RouteDeleteOperationLogQueue) Enqueue(
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
	q.mu.Lock()
	defer q.mu.Unlock()
	q.logs = append(q.logs, input)
	q.payloads = append(q.payloads, append([]byte(nil), payload...))
	return queue.TaskInfo{
		ID:    fmt.Sprintf("task_w5_route_delete_%d", len(q.logs)),
		Queue: opts.Queue,
		Type:  taskType,
	}, nil
}

func (q *w5RouteDeleteOperationLogQueue) snapshot() (
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

func assertW5RouteDeleteSideEffectCounts(
	t *testing.T,
	invalidator *w5RouteDeleteInvalidator,
	operationLogs *w5RouteDeleteOperationLogQueue,
	logIDCalls int,
	want int,
) {
	t.Helper()
	reasons := invalidator.snapshot()
	logs, payloads := operationLogs.snapshot()
	if len(reasons) != want ||
		logIDCalls != want ||
		len(logs) != want ||
		len(payloads) != want {
		t.Fatalf(
			"route strategy delete invalidations=%d log ids=%d logs=%d payloads=%d, want %d",
			len(reasons),
			logIDCalls,
			len(logs),
			len(payloads),
			want,
		)
	}
}

func assertW5RouteDeleteInvalidations(
	t *testing.T,
	invalidator *w5RouteDeleteInvalidator,
) {
	t.Helper()
	reasons := invalidator.snapshot()
	if len(reasons) != 2 {
		t.Fatalf("route strategy delete invalidations = %#v, want 2", reasons)
	}
	for index, reason := range reasons {
		if reason != managementroutestrategies.RouteStrategyDeletedReason {
			t.Fatalf(
				"route strategy delete invalidation[%d] = %q, want %q",
				index,
				reason,
				managementroutestrategies.RouteStrategyDeletedReason,
			)
		}
	}
}

func assertW5RouteDeleteOperationLogs(
	t *testing.T,
	operationLogs *w5RouteDeleteOperationLogQueue,
	now time.Time,
) {
	t.Helper()
	logs, payloads := operationLogs.snapshot()
	if len(logs) != 2 || len(payloads) != 2 {
		t.Fatalf("route strategy delete logs=%d payloads=%d, want 2", len(logs), len(payloads))
	}
	assertW5RouteDeleteOperationLog(
		t,
		logs[0],
		"oplog_w5_route_delete_1",
		"req_w5_route_delete_admin_global",
		w5RouteDeleteAdminID,
		"admin",
		w5RouteDeleteOwnerID,
		w5RouteDeleteAdminGlobalID,
		"W5 Delete Admin Global",
		"/__aisys__/api/route-strategies/"+w5RouteDeleteAdminGlobalID,
		now,
	)
	assertW5RouteDeleteOperationLog(
		t,
		logs[1],
		"oplog_w5_route_delete_2",
		"req_w5_route_delete_self_success",
		w5RouteDeleteOwnerID,
		"self",
		w5RouteDeleteOwnerID,
		w5RouteDeleteSelfID,
		"W5 Delete Self",
		"/__aisys__/api/my-route-strategies/"+w5RouteDeleteSelfID,
		now,
	)
	for _, payload := range payloads {
		assertW5RouteDeleteRawOperationLogSafe(t, payload)
	}
}

func assertW5RouteDeleteOperationLog(
	t *testing.T,
	logInput port.OperationLogInput,
	wantID string,
	wantTraceID string,
	wantActorID string,
	wantMode string,
	wantOwnerID string,
	wantResourceID string,
	wantResourceName string,
	wantPath string,
	wantCreatedAt time.Time,
) {
	t.Helper()
	if logInput.ID != wantID ||
		logInput.TraceID != wantTraceID ||
		logInput.ActorSystemAccountID != wantActorID ||
		logInput.OperationScopeSystemAccountID != wantOwnerID ||
		logInput.Mode != wantMode ||
		logInput.Module != "route_strategies" ||
		logInput.Action != "delete" ||
		logInput.OperationKey != "route_strategies.delete" ||
		logInput.ResourceType != "route_strategy" ||
		logInput.ResourceID != wantResourceID ||
		logInput.ResourceName != wantResourceName ||
		logInput.Summary != "删除策略路由："+wantResourceName ||
		logInput.DetailLevel != "full" ||
		logInput.VisibilityScope != "targeted" ||
		logInput.Method != http.MethodDelete ||
		logInput.Path != wantPath ||
		logInput.StatusCode == nil ||
		*logInput.StatusCode != http.StatusNoContent ||
		logInput.ClientIP != "127.0.0.1" ||
		logInput.UserAgent != "w5-management-route-strategy-delete-smoke" ||
		!logInput.CreatedAt.UTC().Equal(wantCreatedAt.UTC()) {
		t.Fatalf("route strategy delete operation log = %+v", logInput)
	}
	if len(logInput.Viewers) != 1 ||
		logInput.Viewers[0].SystemAccountID != wantOwnerID ||
		logInput.Viewers[0].VisibilityReason != "resource_owner" ||
		logInput.Viewers[0].DetailLevel != "full" {
		t.Fatalf("route strategy delete operation log viewers = %+v", logInput.Viewers)
	}
	if len(logInput.Changes) != 1 ||
		logInput.Changes[0].Field != "deleted" ||
		logInput.Changes[0].Label != "删除状态" ||
		logInput.Changes[0].Before != false ||
		logInput.Changes[0].After != true ||
		logInput.Changes[0].Sensitive {
		t.Fatalf("route strategy delete operation log changes = %+v", logInput.Changes)
	}
}

func assertW5RouteDeleteRawOperationLogSafe(
	t *testing.T,
	payload []byte,
) {
	t.Helper()
	raw := string(payload)
	for _, forbidden := range []string{
		w5RouteDeleteAdminDescriptionCanary,
		w5RouteDeleteAdminConfigCanary,
		w5RouteDeleteSelfDescriptionCanary,
		w5RouteDeleteSelfConfigCanary,
		w5RouteDeleteBindingCanary,
		w5RouteDeleteAdminToken,
		w5RouteDeleteOwnerToken,
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("raw route strategy delete operation log leaked %q: %s", forbidden, payload)
		}
	}
	lower := strings.ToLower(raw)
	for _, forbidden := range []string{
		`"description"`,
		`"config"`,
		`"configjson"`,
		`"groupbindings"`,
		`"group_bindings"`,
		`"route_strategy_groups"`,
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("raw route strategy delete operation log contains %q: %s", forbidden, payload)
		}
	}
}
