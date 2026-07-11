//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/apikeysecret"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/gatewaycache"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/secretcrypto"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w5ManagementAPIKeyCreateAdminLogID = "oplog_w5_management_api_key_create_admin"
	w5ManagementAPIKeyCreateSelfLogID  = "oplog_w5_management_api_key_create_self"
)

type w5ManagementAPIKeyCreateResponse struct {
	Data struct {
		ID                   string         `json:"id"`
		SystemAccountID      string         `json:"systemAccountId"`
		Name                 string         `json:"name"`
		Key                  string         `json:"key"`
		KeyPrefix            string         `json:"keyPrefix"`
		KeySuffix            string         `json:"keySuffix"`
		Status               string         `json:"status"`
		RouteStrategyID      string         `json:"routeStrategyId"`
		QuotaLimits          map[string]any `json:"quotaLimits"`
		AvailabilitySchedule map[string]any `json:"availabilitySchedule"`
	} `json:"data"`
	Message string `json:"message"`
}

func exerciseW5ManagementAPIKeyCreateSmoke(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	store *postgresstore.Store,
	router http.Handler,
	cacheRedis *redisplatform.Client,
	stateRedis *redisplatform.Client,
	inspector *queue.Inspector,
	workerDone <-chan struct{},
	workerErr func() error,
	now time.Time,
) {
	t.Helper()

	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.system_accounts
		SET status = 'disabled'
		WHERE id = $1
	`, w5ManagementAPIKeyListOtherID); err != nil {
		t.Fatalf("disable W5 API Key create target account: %v", err)
	}

	adminBody := `{
		"name":" W5 Admin Created ",
		"description":" admin create secret-free description ",
		"routeStrategyId":"` + w5ManagementAPIKeyListOtherRouteID + `",
		"status":"disabled",
		"quotaLimits":{"hourly":{"enabled":true,"hours":6,"limit":1.000001}},
		"availabilitySchedule":{
			"enabled":true,
			"timezone":"UTC",
			"mode":"allow_windows",
			"windows":[{"daysOfWeek":[6],"start":"11:00","end":"13:00"}]
		}
	}`
	admin := requestW5ManagementAPIKeyCreate(
		t,
		router,
		"/__aisys__/api/api-keys?systemAccountId="+w5ManagementAPIKeyListOtherID,
		w5ManagementAPIKeyListAdminToken,
		adminBody,
		"req_w5_management_api_key_create_admin",
	)
	if admin.Data.SystemAccountID != w5ManagementAPIKeyListOtherID ||
		admin.Data.Name != "W5 Admin Created" ||
		admin.Data.Status != "active" ||
		admin.Data.RouteStrategyID != w5ManagementAPIKeyListOtherRouteID {
		t.Fatalf("admin create response = %+v", admin)
	}
	if admin.Data.AvailabilitySchedule["timezone"] != "UTC" {
		t.Fatalf("admin schedule = %+v", admin.Data.AvailabilitySchedule)
	}
	assertW5ManagementAPIKeyCreateStored(t, ctx, db, admin, w5ManagementAPIKeyListOtherID, true)

	self := requestW5ManagementAPIKeyCreate(
		t,
		router,
		"/__aisys__/api/my-api-keys?systemAccountId="+w5ManagementAPIKeyListOtherID,
		w5ManagementAPIKeyListOwnerToken,
		`{"name":"W5 Self Created","routeStrategyId":"`+
			w5ManagementAPIKeyListOwnerPrimaryRouteID+`","status":"disabled"}`,
		"req_w5_management_api_key_create_self",
	)
	if self.Data.SystemAccountID != "" ||
		self.Data.Name != "W5 Self Created" ||
		self.Data.Status != "disabled" ||
		self.Data.RouteStrategyID != w5ManagementAPIKeyListOwnerPrimaryRouteID {
		t.Fatalf("self create response = %+v", self)
	}
	assertW5ManagementAPIKeyCreateStored(t, ctx, db, self, w5ManagementAPIKeyListOwnerID, false)

	assertW5ManagementAPIKeyCreateFailure(
		t,
		router,
		"/__aisys__/api/my-api-keys",
		w5ManagementAPIKeyListOwnerToken,
		`{"name":"Wrong Owner Route","routeStrategyId":"`+w5ManagementAPIKeyListOtherRouteID+`"}`,
		http.StatusBadRequest,
		"API Key 绑定的策略路由不存在或不属于当前用户",
	)
	assertW5ManagementAPIKeyCreateFailure(
		t,
		router,
		"/__aisys__/api/my-api-keys",
		w5ManagementAPIKeyListOwnerToken,
		`{"name":"Missing Route","routeStrategyId":"route_missing"}`,
		http.StatusBadRequest,
		"API Key 绑定的策略路由不存在或不属于当前用户",
	)
	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.route_strategies
		SET status = 'disabled'
		WHERE id = $1
	`, w5ManagementAPIKeyListOwnerSecondaryRouteID); err != nil {
		t.Fatalf("disable W5 API Key create route: %v", err)
	}
	assertW5ManagementAPIKeyCreateFailure(
		t,
		router,
		"/__aisys__/api/api-keys?systemAccountId="+w5ManagementAPIKeyListOwnerID,
		w5ManagementAPIKeyListAdminToken,
		`{"name":"Disabled Route","routeStrategyId":"`+w5ManagementAPIKeyListOwnerSecondaryRouteID+`"}`,
		http.StatusBadRequest,
		"API Key 只能绑定启用状态的策略路由",
	)
	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.route_strategies
		SET status = 'active'
		WHERE id = $1
	`, w5ManagementAPIKeyListOwnerSecondaryRouteID); err != nil {
		t.Fatalf("restore W5 API Key create route: %v", err)
	}
	assertW5ManagementAPIKeyCreateFailure(
		t,
		router,
		"/__aisys__/api/api-keys?systemAccountId="+w5ManagementAPIKeyListOwnerID,
		w5ManagementAPIKeyListAdminToken,
		`{"name":"Owner Default","routeStrategyId":"`+w5ManagementAPIKeyListOwnerPrimaryRouteID+`"}`,
		http.StatusConflict,
		"API Key 名称已存在：Owner Default",
	)

	assertW5ManagementAPIKeyCreateDuplicateHashConstraint(t, ctx, db, store, now)
	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, workerErr); err != nil {
		t.Fatalf("wait for API Key create operation logs: %v", err)
	}
	assertW5ManagementAPIKeyCreateOperationLog(t, ctx, db, w5ManagementAPIKeyCreateAdminLogID, admin, w5ManagementAPIKeyListOtherID, "admin")
	assertW5ManagementAPIKeyCreateOperationLog(t, ctx, db, w5ManagementAPIKeyCreateSelfLogID, self, w5ManagementAPIKeyListOwnerID, "self")
	assertW5ManagementAPIKeyCreateInvalidations(t, ctx, cacheRedis, stateRedis, self.Data.ID, now)
}

func requestW5ManagementAPIKeyCreate(
	t *testing.T,
	router http.Handler,
	path string,
	sessionToken string,
	body string,
	requestID string,
) w5ManagementAPIKeyCreateResponse {
	t.Helper()
	rec := serveW5ManagementAPIKeySecretRequest(
		router,
		http.MethodPost,
		path,
		sessionToken,
		body,
		requestID,
	)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s status = %d, body = %s", path, rec.Code, rec.Body.String())
	}
	assertW5ManagementAPIKeySecretNoStore(t, rec)
	var response w5ManagementAPIKeyCreateResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if response.Message != "API Key 已创建，请立即复制完整密钥" ||
		response.Data.ID == "" ||
		response.Data.Key == "" ||
		response.Data.KeyPrefix != apikeysecret.Prefix(response.Data.Key) ||
		response.Data.KeySuffix != apikeysecret.Suffix(response.Data.Key) {
		t.Fatalf("create response = %+v", response)
	}
	return response
}

func assertW5ManagementAPIKeyCreateFailure(
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
		http.MethodPost,
		path,
		sessionToken,
		body,
		"req_w5_management_api_key_create_failure_"+strings.ReplaceAll(wantMessage, " ", "_"),
	)
	if rec.Code != wantStatus {
		t.Fatalf("failure %s status = %d, want %d; body = %s", path, rec.Code, wantStatus, rec.Body.String())
	}
	var response map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode failure response: %v", err)
	}
	if response["message"] != wantMessage {
		t.Fatalf("failure message = %q, want %q", response["message"], wantMessage)
	}
}

func assertW5ManagementAPIKeyCreateStored(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	response w5ManagementAPIKeyCreateResponse,
	wantOwner string,
	wantSchedule bool,
) {
	t.Helper()
	var owner string
	var keyHash string
	var encrypted string
	var status string
	var quotaJSON sql.NullString
	var scheduleJSON sql.NullString
	var nextCheckAt sql.NullTime
	if err := db.QueryRowContext(ctx, `
		SELECT
			system_account_id,
			key_hash,
			key_secret_encrypted,
			status,
			quota_limits_json,
			availability_schedule_json,
			availability_schedule_next_check_at
		FROM juhe_business.api_keys
		WHERE id = $1
	`, response.Data.ID).Scan(
		&owner,
		&keyHash,
		&encrypted,
		&status,
		&quotaJSON,
		&scheduleJSON,
		&nextCheckAt,
	); err != nil {
		t.Fatalf("read created API Key %s: %v", response.Data.ID, err)
	}
	if owner != wantOwner ||
		keyHash != apikeysecret.Hash(response.Data.Key) ||
		status != response.Data.Status {
		t.Fatalf("created API Key owner=%q hash=%q status=%q response=%+v", owner, keyHash, status, response)
	}
	payload, err := secretcrypto.NewJSONCodec(w5ManagementAPIKeySecretRuntimeSecret).DecryptJSON(encrypted)
	if err != nil || payload["key"] != response.Data.Key {
		t.Fatalf("decrypt created API Key payload=%#v err=%v", payload, err)
	}
	if wantSchedule {
		if !quotaJSON.Valid || !strings.Contains(quotaJSON.String, `"hours":6`) ||
			!scheduleJSON.Valid ||
			!nextCheckAt.Valid ||
			!nextCheckAt.Time.UTC().Equal(time.Date(2026, 7, 11, 13, 0, 0, 0, time.UTC)) {
			t.Fatalf("created schedule quota=%+v schedule=%+v next=%+v", quotaJSON, scheduleJSON, nextCheckAt)
		}
		var hourlyCount int
		if err := db.QueryRowContext(ctx, `
			SELECT count(*)
			FROM juhe_business.request_quota_hourly_window_configs
			WHERE window_hours = 6
		`).Scan(&hourlyCount); err != nil {
			t.Fatalf("count hourly window config: %v", err)
		}
		if hourlyCount != 1 {
			t.Fatalf("hourly window config count = %d, want 1", hourlyCount)
		}
		return
	}
	if quotaJSON.Valid || scheduleJSON.Valid || nextCheckAt.Valid {
		t.Fatalf("self create optional storage quota=%+v schedule=%+v next=%+v", quotaJSON, scheduleJSON, nextCheckAt)
	}
}

func assertW5ManagementAPIKeyCreateDuplicateHashConstraint(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	store *postgresstore.Store,
	now time.Time,
) {
	t.Helper()
	var duplicateHash string
	if err := db.QueryRowContext(ctx, `
		SELECT key_hash
		FROM juhe_business.api_keys
		WHERE id = $1
	`, w5ManagementAPIKeyListOwnerDefaultID).Scan(&duplicateHash); err != nil {
		t.Fatalf("read duplicate hash fixture: %v", err)
	}
	_, err := store.CreateManagementAPIKey(ctx, port.ManagementAPIKeyCreateInput{
		ID:                 "key_w5_management_api_key_create_duplicate_hash",
		SystemAccountID:    w5ManagementAPIKeyListOwnerID,
		RouteStrategyID:    w5ManagementAPIKeyListOwnerPrimaryRouteID,
		Name:               "W5 Duplicate Hash",
		KeyHash:            duplicateHash,
		KeyPrefix:          "sk-dup",
		KeySuffix:          "dup",
		KeySecretEncrypted: "encrypted-duplicate",
		Status:             "active",
		CreatedAt:          now,
		UpdatedAt:          now,
	})
	if !errors.Is(err, port.ErrManagementAPIKeyHashExists) {
		t.Fatalf("duplicate hash error = %v, want %v", err, port.ErrManagementAPIKeyHashExists)
	}
}

func assertW5ManagementAPIKeyCreateOperationLog(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	logID string,
	response w5ManagementAPIKeyCreateResponse,
	wantOwner string,
	wantMode string,
) {
	t.Helper()
	var mode string
	var operationScope string
	var operationKey string
	var summary string
	var changesJSON string
	if err := db.QueryRowContext(ctx, `
		SELECT mode, operation_scope_system_account_id, operation_key, summary, changes_json
		FROM juhe_dataset.operation_logs
		WHERE id = $1
	`, logID).Scan(&mode, &operationScope, &operationKey, &summary, &changesJSON); err != nil {
		t.Fatalf("read API Key create operation log %s: %v", logID, err)
	}
	if mode != wantMode ||
		operationScope != wantOwner ||
		operationKey != "api_keys.create" ||
		summary != "创建 API Key："+response.Data.Name {
		t.Fatalf("API Key create operation log mode=%q scope=%q key=%q summary=%q", mode, operationScope, operationKey, summary)
	}
	for _, forbidden := range []string{
		response.Data.Key,
		apikeysecret.Hash(response.Data.Key),
		"admin create secret-free description",
		"quotaLimits",
		"expiresAt",
		"ciphertext",
	} {
		if strings.Contains(changesJSON, forbidden) {
			t.Fatalf("API Key create operation log leaked %q: %s", forbidden, changesJSON)
		}
	}
	if !strings.Contains(changesJSON, response.Data.KeyPrefix+"..."+response.Data.KeySuffix) {
		t.Fatalf("API Key create operation log missing marker: %s", changesJSON)
	}
}

func assertW5ManagementAPIKeyCreateInvalidations(
	t *testing.T,
	ctx context.Context,
	cacheRedis *redisplatform.Client,
	stateRedis *redisplatform.Client,
	wantAPIKeyID string,
	now time.Time,
) {
	t.Helper()
	assertW5ManagementAPIKeySecretInvalidationTopicWithReason(
		t,
		ctx,
		stateRedis,
		gatewaycache.GatewayRuntimeCacheTopic,
		"w5-api-key-secret-version-6",
		"",
		"api_key_created",
		now,
	)
	assertW5ManagementAPIKeySecretInvalidationTopicWithReason(
		t,
		ctx,
		stateRedis,
		gatewaycache.APIKeyQuotaCacheTopic,
		"w5-api-key-secret-version-7",
		wantAPIKeyID,
		"api_key_created",
		now,
	)
	versionKey, err := gatewaycache.SharedCacheVersionKey(
		w5ManagementAPIKeySecretRedisNamespace,
		gatewaycache.APIKeyValidationCacheName,
	)
	if err != nil {
		t.Fatalf("build API Key validation version key: %v", err)
	}
	raw, err := cacheRedis.GetRaw(ctx, versionKey)
	if err != nil {
		t.Fatalf("read API Key validation version: %v", err)
	}
	if string(raw) != "w5-api-key-secret-version-1" {
		t.Fatalf("create unexpectedly invalidated validation cache: %q", raw)
	}
}

func assertW5ManagementAPIKeySecretInvalidationTopicWithReason(
	t *testing.T,
	ctx context.Context,
	stateRedis *redisplatform.Client,
	topic string,
	wantVersion string,
	wantAPIKeyID string,
	wantReason string,
	wantPublishedAt time.Time,
) {
	t.Helper()
	key, err := gatewaycache.RuntimeStateKey(
		w5ManagementAPIKeySecretRedisNamespace,
		gatewaycache.RuntimeInvalidationStoreName,
		"topic:"+topic,
	)
	if err != nil {
		t.Fatalf("build invalidation topic %s: %v", topic, err)
	}
	raw, err := stateRedis.GetRaw(ctx, key)
	if err != nil {
		t.Fatalf("read invalidation topic %s: %v", topic, err)
	}
	var state struct {
		Version     string `json:"version"`
		Reason      string `json:"reason"`
		APIKeyID    string `json:"apiKeyId,omitempty"`
		PublishedAt string `json:"publishedAt"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode invalidation topic %s: %v", topic, err)
	}
	if state.Version != wantVersion ||
		state.Reason != wantReason ||
		state.APIKeyID != wantAPIKeyID ||
		state.PublishedAt != wantPublishedAt.UTC().Format("2006-01-02T15:04:05.000Z") {
		t.Fatalf("invalidation topic %s = %+v", topic, state)
	}
}
