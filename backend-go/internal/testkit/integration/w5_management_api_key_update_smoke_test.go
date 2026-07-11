//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/gatewaycache"
	"juhe-ai/backend-go/internal/modules/managementapikeys"
	"juhe-ai/backend-go/internal/modules/managementauth"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w5ManagementAPIKeyUpdateAdminGlobalLogID       = "oplog_w5_management_api_key_update_admin_global"
	w5ManagementAPIKeyUpdateAdminExplicitLogID     = "oplog_w5_management_api_key_update_admin_explicit"
	w5ManagementAPIKeyUpdateSameDisabledRouteLogID = "oplog_w5_management_api_key_update_same_disabled_route"
	w5ManagementAPIKeyUpdateSelfScheduleLogID      = "oplog_w5_management_api_key_update_self_schedule"
	w5ManagementAPIKeyUpdateSelfClearLogID         = "oplog_w5_management_api_key_update_self_clear"
	w5ManagementAPIKeyUpdateCommittedFailureLogID  = "oplog_w5_management_api_key_update_committed_failure"
)

func exerciseW5ManagementAPIKeyUpdateSmoke(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	store *postgresstore.Store,
	router http.Handler,
	redisCacheURL string,
	stateRedis *redisplatform.Client,
	operationLogOptions httpapi.ManagementOperationLogOptions,
	inspector *queue.Inspector,
	workerDone <-chan struct{},
	workerErr func() error,
	cfg config.Config,
	logger *slog.Logger,
	now time.Time,
) {
	t.Helper()

	adminGlobal := requestW5ManagementAPIKeyUpdate(
		t,
		router,
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyListOwnerDefaultID,
		w5ManagementAPIKeyListAdminToken,
		`{"name":"Owner Default Updated"}`,
		"req_w5_management_api_key_update_admin_global",
	)
	if adminGlobal.Name != "Owner Default Updated" ||
		adminGlobal.SystemAccountID != w5ManagementAPIKeyListOwnerID ||
		adminGlobal.Usage.RequestCount != 7 ||
		adminGlobal.Usage.TotalCost != 2.5 {
		t.Fatalf("admin global update = %+v", adminGlobal)
	}

	adminExplicit := requestW5ManagementAPIKeyUpdate(
		t,
		router,
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyListStableZID+
			"?systemAccountId="+w5ManagementAPIKeyListOwnerID,
		w5ManagementAPIKeyListAdminToken,
		`{"description":"updated by explicit owner"}`,
		"req_w5_management_api_key_update_admin_explicit",
	)
	if adminExplicit.Description == nil ||
		*adminExplicit.Description != "updated by explicit owner" ||
		adminExplicit.SystemAccountID != w5ManagementAPIKeyListOwnerID {
		t.Fatalf("admin explicit update = %+v", adminExplicit)
	}
	assertW5ManagementAPIKeyUpdateFailure(
		t,
		router,
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyListStableZID+
			"?systemAccountId="+w5ManagementAPIKeyListOtherID,
		w5ManagementAPIKeyListAdminToken,
		`{"description":"wrong owner"}`,
		http.StatusNotFound,
		"API Key 不存在",
	)

	setW5ManagementAPIKeyRouteStatus(
		t,
		ctx,
		db,
		w5ManagementAPIKeyListOwnerSecondaryRouteID,
		"disabled",
	)
	sameDisabledRoute := requestW5ManagementAPIKeyUpdate(
		t,
		router,
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyListLiteralID+
			"?systemAccountId="+w5ManagementAPIKeyListOwnerID,
		w5ManagementAPIKeyListAdminToken,
		`{"routeStrategyId":"`+w5ManagementAPIKeyListOwnerSecondaryRouteID+`"}`,
		"req_w5_management_api_key_update_same_disabled_route",
	)
	if sameDisabledRoute.RouteStrategyID != w5ManagementAPIKeyListOwnerSecondaryRouteID {
		t.Fatalf("same disabled route update = %+v", sameDisabledRoute)
	}

	assertW5ManagementAPIKeyUpdateFailureKeepsRoute(
		t,
		ctx,
		db,
		router,
		w5ManagementAPIKeyListStableZID,
		`{"routeStrategyId":"`+w5ManagementAPIKeyListOwnerSecondaryRouteID+`"}`,
		"API Key 只能绑定启用状态的策略路由",
	)
	assertW5ManagementAPIKeyUpdateFailureKeepsRoute(
		t,
		ctx,
		db,
		router,
		w5ManagementAPIKeyListStableZID,
		`{"routeStrategyId":"route_w5_management_api_key_update_missing"}`,
		"API Key 绑定的策略路由不存在或不属于当前用户",
	)
	setW5ManagementAPIKeyRouteStatus(
		t,
		ctx,
		db,
		w5ManagementAPIKeyListOwnerSecondaryRouteID,
		"active",
	)
	assertW5ManagementAPIKeyUpdateFailure(
		t,
		router,
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyListOwnerDefaultID+
			"?systemAccountId="+w5ManagementAPIKeyListOwnerID,
		w5ManagementAPIKeyListAdminToken,
		`{"routeStrategyId":"`+w5ManagementAPIKeyListOwnerSecondaryRouteID+`"}`,
		http.StatusBadRequest,
		"默认 API Key 不允许更换策略路由",
	)

	selfSchedule := requestW5ManagementAPIKeyUpdate(
		t,
		router,
		"/__aisys__/api/my-api-keys/"+w5ManagementAPIKeyListEmptyUsageID+
			"?systemAccountId="+w5ManagementAPIKeyListOtherID,
		w5ManagementAPIKeyListOwnerToken,
		`{
			"quotaLimits":{"hourly":{"enabled":true,"hours":8,"limit":33}},
			"availabilitySchedule":{
				"enabled":true,
				"timezone":"UTC",
				"mode":"allow_windows",
				"windows":[{"daysOfWeek":[6],"start":"13:00","end":"14:00"}]
			}
		}`,
		"req_w5_management_api_key_update_self_schedule",
	)
	if selfSchedule.SystemAccountID != "" ||
		selfSchedule.Status != "disabled" ||
		selfSchedule.QuotaLimits.Hourly == nil ||
		selfSchedule.QuotaLimits.Hourly.Hours != 8 ||
		selfSchedule.AvailabilitySchedule["timezone"] != "UTC" {
		t.Fatalf("self schedule update = %+v", selfSchedule)
	}
	assertW5ManagementAPIKeyUpdateHourlyQuotaStored(t, ctx, db, w5ManagementAPIKeyListEmptyUsageID)

	selfClear := requestW5ManagementAPIKeyUpdate(
		t,
		router,
		"/__aisys__/api/my-api-keys/"+w5ManagementAPIKeyListEmptyUsageID,
		w5ManagementAPIKeyListOwnerToken,
		`{"status":"active","quotaLimits":null,"availabilitySchedule":null}`,
		"req_w5_management_api_key_update_self_clear",
	)
	if selfClear.Status != "active" ||
		selfClear.QuotaLimits.Hourly != nil ||
		selfClear.AvailabilitySchedule != nil {
		t.Fatalf("self clear update = %+v", selfClear)
	}
	assertW5ManagementAPIKeyUpdateOptionalColumnsCleared(t, ctx, db, w5ManagementAPIKeyListEmptyUsageID)

	assertW5ManagementAPIKeyUpdateFailure(
		t,
		router,
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyListStableZID,
		w5ManagementAPIKeyListAdminToken,
		`{"name":"Alpha Apple"}`,
		http.StatusConflict,
		"API Key 名称已存在：Alpha Apple",
	)

	failingRouter := newW5ManagementAPIKeyUpdateFailingRedisRouter(
		t,
		store,
		redisCacheURL,
		stateRedis,
		operationLogOptions,
		cfg,
		logger,
		now,
	)
	committedFailure := serveW5ManagementAPIKeySecretRequest(
		failingRouter,
		http.MethodPatch,
		"/__aisys__/api/my-api-keys/"+w5ManagementAPIKeyListStableAID,
		w5ManagementAPIKeyListOwnerToken,
		`{"description":"committed despite validation Redis failure"}`,
		"req_w5_management_api_key_update_committed_failure",
	)
	if committedFailure.Code != http.StatusInternalServerError {
		t.Fatalf(
			"committed validation Redis failure status = %d, body = %s",
			committedFailure.Code,
			committedFailure.Body.String(),
		)
	}
	var committedDescription sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT description
		FROM juhe_business.api_keys
		WHERE id = $1
	`, w5ManagementAPIKeyListStableAID).Scan(&committedDescription); err != nil {
		t.Fatalf("read committed validation Redis failure row: %v", err)
	}
	if !committedDescription.Valid ||
		committedDescription.String != "committed despite validation Redis failure" {
		t.Fatalf("committed validation Redis failure description = %+v", committedDescription)
	}

	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, workerErr); err != nil {
		t.Fatalf("wait for API Key update operation logs: %v", err)
	}
	assertW5ManagementAPIKeyUpdateOperationLogs(t, ctx, db)
}

func requestW5ManagementAPIKeyUpdate(
	t *testing.T,
	router http.Handler,
	path string,
	sessionToken string,
	body string,
	requestID string,
) managementapikeys.ListItem {
	t.Helper()
	rec := serveW5ManagementAPIKeySecretRequest(
		router,
		http.MethodPatch,
		path,
		sessionToken,
		body,
		requestID,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("update %s status = %d, body = %s", path, rec.Code, rec.Body.String())
	}
	assertW5ManagementAPIKeySecretNoStore(t, rec)
	var envelope map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if len(envelope) != 1 {
		t.Fatalf("update response keys = %v, want only data", envelope)
	}
	rawData, ok := envelope["data"]
	if !ok {
		t.Fatal("update response missing data")
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(rawData, &rawFields); err != nil {
		t.Fatalf("decode update raw data: %v", err)
	}
	for _, forbidden := range []string{"key", "keyHash", "keySecretEncrypted", "message"} {
		if _, exists := rawFields[forbidden]; exists {
			t.Fatalf("update response exposed %s", forbidden)
		}
	}
	var item managementapikeys.ListItem
	if err := json.Unmarshal(rawData, &item); err != nil {
		t.Fatalf("decode update data: %v", err)
	}
	return item
}

func assertW5ManagementAPIKeyUpdateFailure(
	t *testing.T,
	router http.Handler,
	path string,
	sessionToken string,
	body string,
	wantStatus int,
	wantMessage string,
) {
	t.Helper()
	rec := serveW5ManagementAPIKeySecretRequest(
		router,
		http.MethodPatch,
		path,
		sessionToken,
		body,
		"req_w5_management_api_key_update_failure",
	)
	if rec.Code != wantStatus {
		t.Fatalf("update failure %s status = %d, body = %s", path, rec.Code, rec.Body.String())
	}
	assertW5ManagementAPIKeySecretNoStore(t, rec)
	var response map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode update failure response: %v", err)
	}
	if response["message"] != wantMessage {
		t.Fatalf("update failure message = %q, want %q", response["message"], wantMessage)
	}
}

func assertW5ManagementAPIKeyUpdateFailureKeepsRoute(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	router http.Handler,
	apiKeyID string,
	body string,
	wantMessage string,
) {
	t.Helper()
	assertW5ManagementAPIKeyUpdateFailure(
		t,
		router,
		"/__aisys__/api/api-keys/"+apiKeyID+
			"?systemAccountId="+w5ManagementAPIKeyListOwnerID,
		w5ManagementAPIKeyListAdminToken,
		body,
		http.StatusBadRequest,
		wantMessage,
	)
	var routeStrategyID string
	if err := db.QueryRowContext(ctx, `
		SELECT route_strategy_id
		FROM juhe_business.api_keys
		WHERE id = $1
	`, apiKeyID).Scan(&routeStrategyID); err != nil {
		t.Fatalf("read rejected API Key route: %v", err)
	}
	if routeStrategyID != w5ManagementAPIKeyListOwnerPrimaryRouteID {
		t.Fatalf("rejected API Key route = %q", routeStrategyID)
	}
}

func setW5ManagementAPIKeyRouteStatus(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	routeStrategyID string,
	status string,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.route_strategies
		SET status = $2
		WHERE id = $1
	`, routeStrategyID, status); err != nil {
		t.Fatalf("set route %s status %s: %v", routeStrategyID, status, err)
	}
}

func assertW5ManagementAPIKeyUpdateHourlyQuotaStored(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	apiKeyID string,
) {
	t.Helper()
	var quotaJSON string
	if err := db.QueryRowContext(ctx, `
		SELECT quota_limits_json
		FROM juhe_business.api_keys
		WHERE id = $1
	`, apiKeyID).Scan(&quotaJSON); err != nil {
		t.Fatalf("read updated API Key quota: %v", err)
	}
	if !strings.Contains(quotaJSON, `"hours":8`) {
		t.Fatalf("updated API Key quota = %s", quotaJSON)
	}
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_business.request_quota_hourly_window_configs
		WHERE window_hours = 8
	`).Scan(&count); err != nil {
		t.Fatalf("count updated hourly quota config: %v", err)
	}
	if count != 1 {
		t.Fatalf("updated hourly quota config count = %d, want 1", count)
	}
}

func assertW5ManagementAPIKeyUpdateOptionalColumnsCleared(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	apiKeyID string,
) {
	t.Helper()
	var status string
	var quotaJSON sql.NullString
	var scheduleJSON sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT status, quota_limits_json, availability_schedule_json
		FROM juhe_business.api_keys
		WHERE id = $1
	`, apiKeyID).Scan(&status, &quotaJSON, &scheduleJSON); err != nil {
		t.Fatalf("read cleared API Key optional columns: %v", err)
	}
	if status != "active" || quotaJSON.Valid || scheduleJSON.Valid {
		t.Fatalf(
			"cleared API Key status=%q quota=%+v schedule=%+v",
			status,
			quotaJSON,
			scheduleJSON,
		)
	}
}

func newW5ManagementAPIKeyUpdateFailingRedisRouter(
	t *testing.T,
	store *postgresstore.Store,
	redisCacheURL string,
	stateRedis *redisplatform.Client,
	operationLogOptions httpapi.ManagementOperationLogOptions,
	cfg config.Config,
	logger *slog.Logger,
	now time.Time,
) http.Handler {
	t.Helper()
	failingCache, err := redisplatform.NewClient(
		redisCacheURL,
		w5ManagementAPIKeySecretRedisNamespace+":update-failure",
	)
	if err != nil {
		t.Fatalf("open failing validation cache Redis client: %v", err)
	}
	invalidator, err := gatewaycache.NewSystemAccountInvalidator(
		gatewaycache.SystemAccountInvalidatorOptions{
			Cache:     failingCache,
			State:     stateRedis,
			Namespace: w5ManagementAPIKeySecretRedisNamespace,
			Now:       func() time.Time { return now },
			NewVersion: func(time.Time) (string, error) {
				return "w5-api-key-update-failing-version", nil
			},
		},
	)
	if err != nil {
		_ = failingCache.Close()
		t.Fatalf("create failing validation cache invalidator: %v", err)
	}
	if err := failingCache.Close(); err != nil {
		t.Fatalf("close validation cache Redis client before update: %v", err)
	}

	service := managementapikeys.NewServiceWithOptions(managementapikeys.ServiceOptions{
		ListReader:               store,
		Updater:                  store,
		UsageStatsTimezoneReader: store,
		Invalidator:              invalidator,
	})
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return httpapi.NewRouter(httpapi.RouterOptions{
		Config:                           cfg,
		Logger:                           logger,
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementAPIKeyUpdateHandler: httpapi.NewManagementAPIKeyUpdateHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
		ManagementMyAPIKeyUpdateHandler: httpapi.NewManagementMyAPIKeyUpdateHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
	})
}

func assertW5ManagementAPIKeyUpdateOperationLogs(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) {
	t.Helper()
	ids := []string{
		w5ManagementAPIKeyUpdateAdminGlobalLogID,
		w5ManagementAPIKeyUpdateAdminExplicitLogID,
		w5ManagementAPIKeyUpdateSameDisabledRouteLogID,
		w5ManagementAPIKeyUpdateSelfScheduleLogID,
		w5ManagementAPIKeyUpdateSelfClearLogID,
		w5ManagementAPIKeyUpdateCommittedFailureLogID,
	}
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_dataset.operation_logs
		WHERE id IN ($1, $2, $3, $4, $5, $6)
	`, ids[0], ids[1], ids[2], ids[3], ids[4], ids[5]).Scan(&count); err != nil {
		t.Fatalf("count API Key update operation logs: %v", err)
	}
	if count != len(ids) {
		t.Fatalf("API Key update operation log count = %d, want %d", count, len(ids))
	}

	var mode string
	var scope string
	var operationKey string
	var resourceName string
	var changesJSON string
	if err := db.QueryRowContext(ctx, `
		SELECT mode, operation_scope_system_account_id, operation_key, resource_name, changes_json
		FROM juhe_dataset.operation_logs
		WHERE id = $1
	`, w5ManagementAPIKeyUpdateAdminGlobalLogID).Scan(
		&mode,
		&scope,
		&operationKey,
		&resourceName,
		&changesJSON,
	); err != nil {
		t.Fatalf("read admin global API Key update operation log: %v", err)
	}
	if mode != "admin" ||
		scope != w5ManagementAPIKeyListOwnerID ||
		operationKey != "api_keys.update" ||
		resourceName != "Owner Default Updated" ||
		!strings.Contains(changesJSON, `"before":"Owner Default"`) ||
		!strings.Contains(changesJSON, `"after":"Owner Default Updated"`) {
		t.Fatalf(
			"admin global API Key update log mode=%q scope=%q key=%q resource=%q changes=%s",
			mode,
			scope,
			operationKey,
			resourceName,
			changesJSON,
		)
	}

	var noOpChanges string
	if err := db.QueryRowContext(ctx, `
		SELECT changes_json
		FROM juhe_dataset.operation_logs
		WHERE id = $1
	`, w5ManagementAPIKeyUpdateSameDisabledRouteLogID).Scan(&noOpChanges); err != nil {
		t.Fatalf("read same-disabled-route API Key update operation log: %v", err)
	}
	if noOpChanges != "[]" {
		t.Fatalf("same-disabled-route changes = %s, want []", noOpChanges)
	}

	var committedChanges string
	if err := db.QueryRowContext(ctx, `
		SELECT changes_json
		FROM juhe_dataset.operation_logs
		WHERE id = $1
	`, w5ManagementAPIKeyUpdateCommittedFailureLogID).Scan(&committedChanges); err != nil {
		t.Fatalf("read committed failure API Key update operation log: %v", err)
	}
	if !strings.Contains(committedChanges, "committed despite validation Redis failure") {
		t.Fatalf("committed failure changes = %s", committedChanges)
	}
	for _, forbidden := range []string{"key_hash", "keyHash", "key_secret", "keySecret", "encrypted-", "hash-"} {
		if strings.Contains(changesJSON, forbidden) || strings.Contains(committedChanges, forbidden) {
			t.Fatalf("API Key update operation log leaked forbidden marker %q", forbidden)
		}
	}

	var failedLogs int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_dataset.operation_logs
		WHERE trace_id = 'req_w5_management_api_key_update_failure'
	`).Scan(&failedLogs); err != nil {
		t.Fatalf("count rejected API Key update operation logs: %v", err)
	}
	if failedLogs != 0 {
		t.Fatalf("rejected API Key updates wrote %d operation logs", failedLogs)
	}
}
