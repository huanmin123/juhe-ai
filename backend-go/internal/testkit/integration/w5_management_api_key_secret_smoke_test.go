//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/apikeysecret"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/gatewaycache"
	"juhe-ai/backend-go/internal/modules/managementauth"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/secretcrypto"
)

const (
	w5ManagementAPIKeySecretRuntimeSecret       = "w5-management-api-key-secret-runtime-secret"
	w5ManagementAPIKeySecretRedisNamespace      = "w5-management-api-key-secret"
	w5ManagementAPIKeySecretAdminRevealLogID    = "oplog_w5_management_api_key_secret_admin_reveal"
	w5ManagementAPIKeySecretSelfRevealLogID     = "oplog_w5_management_api_key_secret_self_reveal"
	w5ManagementAPIKeySecretAdminRefreshLogID   = "oplog_w5_management_api_key_secret_admin_refresh"
	w5ManagementAPIKeySecretRepairedRevealLogID = "oplog_w5_management_api_key_secret_repaired_reveal"
)

func exerciseW5ManagementAPIKeySecretSmoke(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	router http.Handler,
	cacheRedis *redisplatform.Client,
	stateRedis *redisplatform.Client,
	inspector *queue.Inspector,
	workerDone <-chan struct{},
	workerErr func() error,
	sessionLastSeenAt time.Time,
	now time.Time,
) {
	t.Helper()

	const existingKey = "sk-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	codec := secretcrypto.NewJSONCodec(w5ManagementAPIKeySecretRuntimeSecret)
	encrypted, err := codec.EncryptJSON(map[string]any{"key": existingKey})
	if err != nil {
		t.Fatalf("encrypt W5 management API Key secret fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.api_keys
		SET key_hash = $1,
		    key_prefix = $2,
		    key_suffix = $3,
		    key_secret_encrypted = $4
		WHERE id = $5
	`, apikeysecret.Hash(existingKey), apikeysecret.Prefix(existingKey), apikeysecret.Suffix(existingKey),
		encrypted, w5ManagementAPIKeyListOwnerDefaultID); err != nil {
		t.Fatalf("prepare W5 management API Key secret fixture: %v", err)
	}

	wrongOwner := serveW5ManagementAPIKeySecretRequest(
		router,
		http.MethodGet,
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyListOwnerDefaultID+
			"/secret?systemAccountId="+w5ManagementAPIKeyListOtherID,
		w5ManagementAPIKeyListAdminToken,
		"",
		"req_w5_management_api_key_secret_wrong_owner",
	)
	if wrongOwner.Code != http.StatusNotFound {
		t.Fatalf("wrong-owner reveal status = %d, want %d", wrongOwner.Code, http.StatusNotFound)
	}

	adminReveal := serveW5ManagementAPIKeySecretRequest(
		router,
		http.MethodGet,
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyListOwnerDefaultID+"/secret",
		w5ManagementAPIKeyListAdminToken,
		"",
		"req_w5_management_api_key_secret_admin_reveal",
	)
	assertW5ManagementAPIKeySecretReveal(t, adminReveal, existingKey)
	assertW2ManagementSessionLastSeenAt(t, ctx, db, w5ManagementAPIKeyListAdminSID, sessionLastSeenAt)
	assertW2ManagementSessionLastSeenAt(t, ctx, db, w5ManagementAPIKeyListOwnerSID, sessionLastSeenAt)

	selfReveal := serveW5ManagementAPIKeySecretRequest(
		router,
		http.MethodGet,
		"/__aisys__/api/my-api-keys/"+w5ManagementAPIKeyListOwnerDefaultID+
			"/secret?systemAccountId="+w5ManagementAPIKeyListOtherID,
		w5ManagementAPIKeyListOwnerToken,
		"",
		"req_w5_management_api_key_secret_self_reveal",
	)
	assertW5ManagementAPIKeySecretReveal(t, selfReveal, existingKey)
	assertW2ManagementSessionLastSeenAt(t, ctx, db, w5ManagementAPIKeyListAdminSID, sessionLastSeenAt)
	assertW2ManagementSessionLastSeenAt(t, ctx, db, w5ManagementAPIKeyListOwnerSID, sessionLastSeenAt)

	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.api_keys
		SET key_secret_encrypted = NULL
		WHERE id = $1
	`, w5ManagementAPIKeyListEmptyUsageID); err != nil {
		t.Fatalf("set W5 management API Key NULL ciphertext fixture: %v", err)
	}
	nullReveal := serveW5ManagementAPIKeySecretRequest(
		router,
		http.MethodGet,
		"/__aisys__/api/my-api-keys/"+w5ManagementAPIKeyListEmptyUsageID+"/secret",
		w5ManagementAPIKeyListOwnerToken,
		"",
		"req_w5_management_api_key_secret_null_reveal",
	)
	if nullReveal.Code != http.StatusInternalServerError {
		t.Fatalf(
			"NULL ciphertext reveal status = %d, want %d",
			nullReveal.Code,
			http.StatusInternalServerError,
		)
	}

	wrongOwnerRefresh := serveW5ManagementAPIKeySecretRequest(
		router,
		http.MethodPost,
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyListEmptyUsageID+
			"/refresh-key?systemAccountId="+w5ManagementAPIKeyListOtherID,
		w5ManagementAPIKeyListAdminToken,
		"{}",
		"req_w5_management_api_key_secret_wrong_owner_refresh",
	)
	if wrongOwnerRefresh.Code != http.StatusNotFound {
		t.Fatalf(
			"wrong-owner refresh status = %d, want %d",
			wrongOwnerRefresh.Code,
			http.StatusNotFound,
		)
	}

	refresh := serveW5ManagementAPIKeySecretRequest(
		router,
		http.MethodPost,
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyListEmptyUsageID+
			"/refresh-key?systemAccountId="+w5ManagementAPIKeyListOwnerID,
		w5ManagementAPIKeyListAdminToken,
		`{"ignored":true}`,
		"req_w5_management_api_key_secret_admin_refresh",
	)
	if refresh.Code != http.StatusOK {
		t.Fatalf("admin refresh status = %d, want %d", refresh.Code, http.StatusOK)
	}
	assertW5ManagementAPIKeySecretNoStore(t, refresh)
	var refreshEnvelope struct {
		Data    map[string]json.RawMessage `json:"data"`
		Message string                     `json:"message"`
	}
	if err := json.NewDecoder(refresh.Body).Decode(&refreshEnvelope); err != nil {
		t.Fatalf("decode self refresh response: %v", err)
	}
	if refreshEnvelope.Message != "API Key 密钥已刷新，请立即复制完整密钥" {
		t.Fatalf("admin refresh message = %q", refreshEnvelope.Message)
	}
	for _, forbidden := range []string{"keyHash", "keySecretEncrypted"} {
		if _, exists := refreshEnvelope.Data[forbidden]; exists {
			t.Fatalf("admin refresh exposed forbidden field %s", forbidden)
		}
	}
	assertW5ManagementAPIKeySecretStringField(
		t,
		refreshEnvelope.Data,
		"systemAccountId",
		w5ManagementAPIKeyListOwnerID,
	)
	assertW5ManagementAPIKeySecretStringField(
		t,
		refreshEnvelope.Data,
		"systemAccountName",
		"W5 API Key List Owner",
	)
	var refreshedKey string
	if err := json.Unmarshal(refreshEnvelope.Data["key"], &refreshedKey); err != nil || refreshedKey == "" {
		t.Fatalf("decode refreshed key: err=%v empty=%t", err, refreshedKey == "")
	}
	assertW5ManagementAPIKeySecretRedisInvalidations(t, ctx, cacheRedis, stateRedis, now)
	assertW2ManagementSessionLastSeenAt(t, ctx, db, w5ManagementAPIKeyListAdminSID, now)
	assertW2ManagementSessionLastSeenAt(t, ctx, db, w5ManagementAPIKeyListOwnerSID, sessionLastSeenAt)

	var storedHash string
	var storedPrefix string
	var storedSuffix string
	var storedEncrypted string
	if err := db.QueryRowContext(ctx, `
		SELECT key_hash, key_prefix, key_suffix, key_secret_encrypted
		FROM juhe_business.api_keys
		WHERE id = $1
	`, w5ManagementAPIKeyListEmptyUsageID).Scan(
		&storedHash,
		&storedPrefix,
		&storedSuffix,
		&storedEncrypted,
	); err != nil {
		t.Fatalf("query refreshed W5 management API Key secret: %v", err)
	}
	if storedHash != apikeysecret.Hash(refreshedKey) ||
		storedPrefix != apikeysecret.Prefix(refreshedKey) ||
		storedSuffix != apikeysecret.Suffix(refreshedKey) {
		t.Fatalf(
			"stored refreshed secret markers hash=%q prefix=%q suffix=%q",
			storedHash,
			storedPrefix,
			storedSuffix,
		)
	}
	storedPayload, err := codec.DecryptJSON(storedEncrypted)
	if err != nil || storedPayload["key"] != refreshedKey {
		t.Fatalf("stored refreshed ciphertext does not match response: err=%v", err)
	}

	repairedReveal := serveW5ManagementAPIKeySecretRequest(
		router,
		http.MethodGet,
		"/__aisys__/api/my-api-keys/"+w5ManagementAPIKeyListEmptyUsageID+"/secret",
		w5ManagementAPIKeyListOwnerToken,
		"",
		"req_w5_management_api_key_secret_repaired_reveal",
	)
	assertW5ManagementAPIKeySecretReveal(t, repairedReveal, refreshedKey)
	assertW2ManagementSessionLastSeenAt(t, ctx, db, w5ManagementAPIKeyListAdminSID, now)
	assertW2ManagementSessionLastSeenAt(t, ctx, db, w5ManagementAPIKeyListOwnerSID, sessionLastSeenAt)

	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, workerErr); err != nil {
		t.Fatal(err)
	}
	queueInfo, err := inspector.QueueInfo(operationlogjob.QueueName)
	if err != nil {
		t.Fatalf("read API Key secret operation log queue info: %v", err)
	}
	if queueInfo.Archived != 0 || queueInfo.Completed != 4 {
		t.Fatalf("API Key secret operation log queue info = %+v, want exactly 4 completed and 0 archived", queueInfo)
	}
	assertW5ManagementAPIKeySecretOperationLogs(t, ctx, db, existingKey, refreshedKey, now)
}

func serveW5ManagementAPIKeySecretRequest(
	router http.Handler,
	method string,
	target string,
	sessionToken string,
	body string,
	requestID string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if sessionToken != "" {
		req.Header.Set("Cookie", managementauth.SessionCookieName+"="+sessionToken)
	}
	req.Header.Set("User-Agent", "w5-management-api-key-secret-smoke")
	req.Header.Set("X-Request-Id", requestID)
	req.RemoteAddr = "127.0.0.1:12345"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func assertW5ManagementAPIKeySecretReveal(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	wantKey string,
) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("secret reveal status = %d, want %d", rec.Code, http.StatusOK)
	}
	assertW5ManagementAPIKeySecretNoStore(t, rec)
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode secret reveal response: %v", err)
	}
	if len(envelope.Data) != 1 {
		t.Fatalf("secret reveal field count = %d, want 1", len(envelope.Data))
	}
	var key string
	if err := json.Unmarshal(envelope.Data["key"], &key); err != nil || key != wantKey {
		t.Fatalf("secret reveal key mismatch: err=%v", err)
	}
}

func assertW5ManagementAPIKeySecretNoStore(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Header().Get("Cache-Control") != "no-store" ||
		rec.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("secret response cache headers = %#v", rec.Header())
	}
}

func assertW5ManagementAPIKeySecretStringField(
	t *testing.T,
	fields map[string]json.RawMessage,
	field string,
	want string,
) {
	t.Helper()
	var got string
	if err := json.Unmarshal(fields[field], &got); err != nil {
		t.Fatalf("decode refresh %s: %v", field, err)
	}
	if got != want {
		t.Fatalf("refresh %s = %q, want %q", field, got, want)
	}
}

func assertW5ManagementAPIKeySecretRedisInvalidations(
	t *testing.T,
	ctx context.Context,
	cacheRedis *redisplatform.Client,
	stateRedis *redisplatform.Client,
	wantPublishedAt time.Time,
) {
	t.Helper()
	versionKey, err := gatewaycache.SharedCacheVersionKey(
		w5ManagementAPIKeySecretRedisNamespace,
		gatewaycache.APIKeyValidationCacheName,
	)
	if err != nil {
		t.Fatalf("build API Key validation cache key: %v", err)
	}
	rawVersion, err := cacheRedis.GetRaw(ctx, versionKey)
	if err != nil {
		t.Fatalf("read API Key validation cache key %s: %v", versionKey, err)
	}
	if got := string(rawVersion); got != "w5-api-key-secret-version-1" {
		t.Fatalf("API Key validation cache version = %q, want version 1", got)
	}
	assertW5ManagementAPIKeySecretInvalidationTopic(
		t,
		ctx,
		stateRedis,
		gatewaycache.GatewayRuntimeCacheTopic,
		"w5-api-key-secret-version-2",
		"",
		wantPublishedAt,
	)
	assertW5ManagementAPIKeySecretInvalidationTopic(
		t,
		ctx,
		stateRedis,
		gatewaycache.APIKeyQuotaCacheTopic,
		"w5-api-key-secret-version-3",
		w5ManagementAPIKeyListEmptyUsageID,
		wantPublishedAt,
	)
}

func assertW5ManagementAPIKeySecretInvalidationTopic(
	t *testing.T,
	ctx context.Context,
	stateRedis *redisplatform.Client,
	topic string,
	wantVersion string,
	wantAPIKeyID string,
	wantPublishedAt time.Time,
) {
	t.Helper()
	key, err := gatewaycache.RuntimeStateKey(
		w5ManagementAPIKeySecretRedisNamespace,
		gatewaycache.RuntimeInvalidationStoreName,
		"topic:"+topic,
	)
	if err != nil {
		t.Fatalf("build API Key secret invalidation topic %s key: %v", topic, err)
	}
	raw, err := stateRedis.GetRaw(ctx, key)
	if err != nil {
		t.Fatalf("read API Key secret invalidation topic %s key %s: %v", topic, key, err)
	}
	var state struct {
		Version     string `json:"version"`
		Reason      string `json:"reason"`
		APIKeyID    string `json:"apiKeyId,omitempty"`
		PublishedAt string `json:"publishedAt"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode API Key secret invalidation topic %s payload %s: %v", topic, raw, err)
	}
	wantPublished := wantPublishedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	if state.Version != wantVersion ||
		state.Reason != "api_key_secret_refreshed" ||
		state.APIKeyID != wantAPIKeyID ||
		state.PublishedAt != wantPublished {
		t.Fatalf(
			"API Key secret invalidation topic %s state = %+v, want version %q reason %q apiKeyId %q publishedAt %q",
			topic,
			state,
			wantVersion,
			"api_key_secret_refreshed",
			wantAPIKeyID,
			wantPublished,
		)
	}
}

func assertW5ManagementAPIKeySecretOperationLogs(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	existingKey string,
	refreshedKey string,
	wantCreatedAt time.Time,
) {
	t.Helper()
	var total int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_dataset.operation_logs
		WHERE id IN ($1, $2, $3, $4)
	`,
		w5ManagementAPIKeySecretAdminRevealLogID,
		w5ManagementAPIKeySecretSelfRevealLogID,
		w5ManagementAPIKeySecretAdminRefreshLogID,
		w5ManagementAPIKeySecretRepairedRevealLogID,
	).Scan(&total); err != nil {
		t.Fatalf("count API Key secret operation logs: %v", err)
	}
	if total != 4 {
		t.Fatalf("API Key secret operation log count = %d, want 4", total)
	}
	var failedTotal int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_dataset.operation_logs
		WHERE trace_id IN ($1, $2, $3)
	`,
		"req_w5_management_api_key_secret_wrong_owner",
		"req_w5_management_api_key_secret_null_reveal",
		"req_w5_management_api_key_secret_wrong_owner_refresh",
	).Scan(&failedTotal); err != nil {
		t.Fatalf("count failed API Key secret operation logs: %v", err)
	}
	if failedTotal != 0 {
		t.Fatalf("failed API Key secret requests wrote %d operation logs", failedTotal)
	}

	existingMarker := apikeysecret.Prefix(existingKey) + "..." + apikeysecret.Suffix(existingKey)
	assertW5ManagementAPIKeySecretRevealOperationLog(
		t,
		ctx,
		db,
		w5ManagementAPIKeySecretAdminRevealLogID,
		"req_w5_management_api_key_secret_admin_reveal",
		w5ManagementAPIKeyListAdminID,
		"w5-management-api-key-list-admin",
		"W5 API Key List Admin",
		"admin",
		"admin",
		w5ManagementAPIKeyListOwnerDefaultID,
		"Owner Default",
		existingMarker,
		existingKey,
		refreshedKey,
		wantCreatedAt,
	)
	assertW5ManagementAPIKeySecretRevealOperationLog(
		t,
		ctx,
		db,
		w5ManagementAPIKeySecretSelfRevealLogID,
		"req_w5_management_api_key_secret_self_reveal",
		w5ManagementAPIKeyListOwnerID,
		"w5-management-api-key-list-owner",
		"W5 API Key List Owner",
		"user",
		"self",
		w5ManagementAPIKeyListOwnerDefaultID,
		"Owner Default",
		existingMarker,
		existingKey,
		refreshedKey,
		wantCreatedAt,
	)
	refreshedMarker := apikeysecret.Prefix(refreshedKey) + "..." + apikeysecret.Suffix(refreshedKey)
	assertW5ManagementAPIKeySecretRevealOperationLog(
		t,
		ctx,
		db,
		w5ManagementAPIKeySecretRepairedRevealLogID,
		"req_w5_management_api_key_secret_repaired_reveal",
		w5ManagementAPIKeyListOwnerID,
		"w5-management-api-key-list-owner",
		"W5 API Key List Owner",
		"user",
		"self",
		w5ManagementAPIKeyListEmptyUsageID,
		"Beta Empty",
		refreshedMarker,
		existingKey,
		refreshedKey,
		wantCreatedAt,
	)

	var row struct {
		ID                    string
		TraceID               string
		ActorSystemAccountID  string
		ActorUsername         string
		ActorDisplayName      string
		ActorRole             string
		OperationScopeAccount string
		Mode                  string
		Module                string
		Action                string
		OperationKey          string
		ResourceType          string
		ResourceID            string
		ResourceName          string
		Summary               string
		DetailLevel           string
		VisibilityScope       string
		ChangesJSON           string
		MetadataJSON          string
		Method                string
		Path                  string
		StatusCode            int
		ClientIP              string
		UserAgent             string
		CreatedAt             time.Time
	}
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
	`, w5ManagementAPIKeySecretAdminRefreshLogID).Scan(
		&row.ID,
		&row.TraceID,
		&row.ActorSystemAccountID,
		&row.ActorUsername,
		&row.ActorDisplayName,
		&row.ActorRole,
		&row.OperationScopeAccount,
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
		t.Fatalf("read API Key refresh operation log: %v", err)
	}
	refreshPath := "/__aisys__/api/api-keys/" + w5ManagementAPIKeyListEmptyUsageID + "/refresh-key"
	if row.ID != w5ManagementAPIKeySecretAdminRefreshLogID ||
		row.TraceID != "req_w5_management_api_key_secret_admin_refresh" ||
		row.ActorSystemAccountID != w5ManagementAPIKeyListAdminID ||
		row.ActorUsername != "w5-management-api-key-list-admin" ||
		row.ActorDisplayName != "W5 API Key List Admin" ||
		row.ActorRole != "admin" ||
		row.OperationScopeAccount != w5ManagementAPIKeyListOwnerID ||
		row.Mode != "admin" ||
		row.Module != "api_keys" ||
		row.Action != "refresh_key" ||
		row.OperationKey != "api_keys.refresh_key" ||
		row.ResourceType != "api_key" ||
		row.ResourceID != w5ManagementAPIKeyListEmptyUsageID ||
		row.ResourceName != "Beta Empty" ||
		row.Summary != "刷新 API Key 密钥：Beta Empty" ||
		row.DetailLevel != "full" ||
		row.VisibilityScope != "targeted" ||
		row.Method != http.MethodPost ||
		row.Path != refreshPath ||
		row.StatusCode != http.StatusOK ||
		row.ClientIP != "127.0.0.1" ||
		row.UserAgent != "w5-management-api-key-secret-smoke" ||
		!row.CreatedAt.UTC().Equal(wantCreatedAt.UTC()) {
		t.Fatal("API Key refresh operation log does not match the expected contract")
	}
	var changes []struct {
		Field  string `json:"field"`
		Label  string `json:"label"`
		Before string `json:"before"`
		After  string `json:"after"`
	}
	if err := json.Unmarshal([]byte(row.ChangesJSON), &changes); err != nil {
		t.Fatalf("decode API Key refresh operation log changes: %v", err)
	}
	wantAfter := refreshedMarker
	if len(changes) != 1 ||
		changes[0].Field != "key" ||
		changes[0].Label != "密钥标识" ||
		changes[0].Before != "sk-w5-empty...emp123" ||
		changes[0].After != wantAfter {
		t.Fatal("API Key refresh operation log has unexpected marker changes")
	}
	if row.MetadataJSON != "{}" {
		t.Fatal("API Key refresh operation log metadata is not empty")
	}
	for _, value := range []string{row.ChangesJSON, row.MetadataJSON, row.Summary, row.ResourceName} {
		if strings.Contains(value, existingKey) || strings.Contains(value, refreshedKey) {
			t.Fatal("API Key refresh operation log leaked a full secret")
		}
	}
}

func assertW5ManagementAPIKeySecretRevealOperationLog(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	id string,
	traceID string,
	actorSystemAccountID string,
	actorUsername string,
	actorDisplayName string,
	actorRole string,
	mode string,
	resourceID string,
	resourceName string,
	keyMarker string,
	existingKey string,
	refreshedKey string,
	wantCreatedAt time.Time,
) {
	t.Helper()
	var row struct {
		ID                    string
		TraceID               string
		ActorSystemAccountID  string
		ActorUsername         string
		ActorDisplayName      string
		ActorRole             string
		OperationScopeAccount string
		Mode                  string
		Module                string
		Action                string
		OperationKey          string
		ResourceType          string
		ResourceID            string
		ResourceName          string
		Summary               string
		DetailLevel           string
		VisibilityScope       string
		ChangesJSON           string
		MetadataJSON          string
		Method                string
		Path                  string
		StatusCode            int
		ClientIP              string
		UserAgent             string
		CreatedAt             time.Time
	}
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
		&row.OperationScopeAccount,
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
		t.Fatalf("read API Key reveal operation log %s: %v", id, err)
	}
	wantPath := "/__aisys__/api/"
	if mode == "self" {
		wantPath += "my-api-keys/"
	} else {
		wantPath += "api-keys/"
	}
	wantPath += resourceID + "/secret"
	wantSummary := "查看 API Key 完整密钥：" + resourceName
	if row.ID != id ||
		row.TraceID != traceID ||
		row.ActorSystemAccountID != actorSystemAccountID ||
		row.ActorUsername != actorUsername ||
		row.ActorDisplayName != actorDisplayName ||
		row.ActorRole != actorRole ||
		row.OperationScopeAccount != w5ManagementAPIKeyListOwnerID ||
		row.Mode != mode ||
		row.Module != "api_keys" ||
		row.Action != "reveal_secret" ||
		row.OperationKey != "api_keys.reveal_secret" ||
		row.ResourceType != "api_key" ||
		row.ResourceID != resourceID ||
		row.ResourceName != resourceName ||
		row.Summary != wantSummary ||
		row.DetailLevel != "full" ||
		row.VisibilityScope != "targeted" ||
		row.Method != http.MethodGet ||
		row.Path != wantPath ||
		row.StatusCode != http.StatusOK ||
		row.ClientIP != "127.0.0.1" ||
		row.UserAgent != "w5-management-api-key-secret-smoke" ||
		!row.CreatedAt.UTC().Equal(wantCreatedAt.UTC()) {
		t.Fatalf("API Key reveal operation log %s does not match the expected contract", id)
	}
	var changes []struct {
		Field  string  `json:"field"`
		Label  string  `json:"label"`
		Before *string `json:"before"`
		After  string  `json:"after"`
	}
	if err := json.Unmarshal([]byte(row.ChangesJSON), &changes); err != nil {
		t.Fatalf("decode API Key reveal operation log changes %s: %v", id, err)
	}
	if len(changes) != 1 ||
		changes[0].Field != "key" ||
		changes[0].Label != "密钥标识" ||
		changes[0].Before != nil ||
		changes[0].After != keyMarker {
		t.Fatalf("API Key reveal operation log %s has unexpected marker changes", id)
	}
	if row.MetadataJSON != "{}" {
		t.Fatalf("API Key reveal operation log %s metadata is not empty", id)
	}
	for _, value := range []string{row.ChangesJSON, row.MetadataJSON, row.Summary, row.ResourceName} {
		if strings.Contains(value, existingKey) || strings.Contains(value, refreshedKey) {
			t.Fatalf("API Key reveal operation log %s leaked a full secret", id)
		}
	}
}
