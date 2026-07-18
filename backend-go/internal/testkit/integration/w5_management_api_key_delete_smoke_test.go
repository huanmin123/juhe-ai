//go:build integration

package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	w5ManagementAPIKeyDeleteAdminGlobalID      = "key_w5_management_api_key_delete_admin_global"
	w5ManagementAPIKeyDeleteWrongOwnerID       = "key_w5_management_api_key_delete_wrong_owner"
	w5ManagementAPIKeyDeleteSelfID             = "key_w5_management_api_key_delete_self"
	w5ManagementAPIKeyDeleteCleanupFailureID   = "key_w5_management_api_key_delete_cleanup_failure"
	w5ManagementAPIKeyDeleteCommittedFailureID = "key_w5_management_api_key_delete_committed_failure"

	w5ManagementAPIKeyDeleteAdminGlobalLogID      = "oplog_w5_management_api_key_delete_admin_global"
	w5ManagementAPIKeyDeleteSelfLogID             = "oplog_w5_management_api_key_delete_self"
	w5ManagementAPIKeyDeleteCommittedFailureLogID = "oplog_w5_management_api_key_delete_" +
		"committed_failure"

	w5ManagementAPIKeyDeleteWrongOwnerTraceID       = "req_w5_management_api_key_delete_wrong_owner"
	w5ManagementAPIKeyDeleteDefaultTraceID          = "req_w5_management_api_key_delete_default"
	w5ManagementAPIKeyDeleteAdminGlobalTraceID      = "req_w5_management_api_key_delete_admin_global"
	w5ManagementAPIKeyDeleteRepeatTraceID           = "req_w5_management_api_key_delete_repeat"
	w5ManagementAPIKeyDeleteSelfTraceID             = "req_w5_management_api_key_delete_self"
	w5ManagementAPIKeyDeleteCleanupFailureTraceID   = "req_w5_management_api_key_delete_cleanup_failure"
	w5ManagementAPIKeyDeleteCommittedFailureTraceID = "req_w5_management_api_key_delete_committed_failure"

	w5ManagementAPIKeyDeleteHashCanary       = "w5-delete-hash-canary"
	w5ManagementAPIKeyDeletePrefixCanary     = "w5-delete-prefix-canary"
	w5ManagementAPIKeyDeleteSuffixCanary     = "w5-delete-suffix-canary"
	w5ManagementAPIKeyDeleteCiphertextCanary = "w5-delete-ciphertext-canary"
)

type w5ManagementAPIKeySmokeInvalidator struct {
	validation *gatewaycache.SystemAccountInvalidator
	lookup     *gatewaycache.SystemAccountInvalidator
	runtime    *gatewaycache.SystemAccountInvalidator
	quota      *gatewaycache.SystemAccountInvalidator
}

func newW5ManagementAPIKeySmokeInvalidator(
	cacheRedis *redisplatform.Client,
	stateRedis *redisplatform.Client,
	now time.Time,
	invalidationVersion *int,
	lookupVersion *int,
) (*w5ManagementAPIKeySmokeInvalidator, error) {
	numberedVersion := func(time.Time) (string, error) {
		(*invalidationVersion)++
		return fmt.Sprintf(
			"w5-api-key-secret-version-%d",
			*invalidationVersion,
		), nil
	}
	lookupOnlyVersion := func(time.Time) (string, error) {
		(*lookupVersion)++
		return fmt.Sprintf(
			"w5-api-key-secret-lookup-version-%d",
			*lookupVersion,
		), nil
	}
	newInvalidator := func(
		newVersion gatewaycache.VersionGenerator,
	) (*gatewaycache.SystemAccountInvalidator, error) {
		return gatewaycache.NewSystemAccountInvalidator(
			gatewaycache.SystemAccountInvalidatorOptions{
				Cache:      cacheRedis,
				State:      stateRedis,
				Namespace:  w5ManagementAPIKeySecretRedisNamespace,
				Now:        func() time.Time { return now },
				NewVersion: newVersion,
			},
		)
	}

	validation, err := newInvalidator(numberedVersion)
	if err != nil {
		return nil, err
	}
	lookup, err := newInvalidator(lookupOnlyVersion)
	if err != nil {
		return nil, err
	}
	runtimeInvalidator, err := newInvalidator(numberedVersion)
	if err != nil {
		return nil, err
	}
	quota, err := newInvalidator(numberedVersion)
	if err != nil {
		return nil, err
	}
	return &w5ManagementAPIKeySmokeInvalidator{
		validation: validation,
		lookup:     lookup,
		runtime:    runtimeInvalidator,
		quota:      quota,
	}, nil
}

func (i *w5ManagementAPIKeySmokeInvalidator) InvalidateAPIKeyValidationCache(
	ctx context.Context,
) error {
	return i.validation.InvalidateAPIKeyValidationCache(ctx)
}

func (i *w5ManagementAPIKeySmokeInvalidator) InvalidateAPIKeyLookupCache(
	ctx context.Context,
	apiKeyID string,
	reason string,
) error {
	return i.lookup.InvalidateAPIKeyLookupCache(ctx, apiKeyID, reason)
}

func (i *w5ManagementAPIKeySmokeInvalidator) InvalidateGatewayRuntime(
	ctx context.Context,
	reason string,
) error {
	return i.runtime.InvalidateGatewayRuntime(ctx, reason)
}

func (i *w5ManagementAPIKeySmokeInvalidator) InvalidateAPIKeyQuotaChanged(
	ctx context.Context,
	apiKeyID string,
	reason string,
) error {
	return i.quota.InvalidateAPIKeyQuotaChanged(ctx, apiKeyID, reason)
}

func exerciseW5ManagementAPIKeyDeleteSmoke(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	store *postgresstore.Store,
	router http.Handler,
	cacheRedis *redisplatform.Client,
	redisCacheURL string,
	stateRedis *redisplatform.Client,
	operationLogOptions httpapi.ManagementOperationLogOptions,
	inspector *queue.Inspector,
	workerDone <-chan struct{},
	workerErr func() error,
	cfg config.Config,
	logger *slog.Logger,
	now time.Time,
	invalidationVersion *int,
	lookupVersion *int,
) {
	t.Helper()

	insertW5ManagementAPIKeyDeleteFixtures(t, ctx, db, now)
	preseed := preseedW5ManagementAPIKeyDeleteCleanupTarget(t, ctx, db, now)

	wrongOwnerInvalidationBefore := readW5ManagementAPIKeyDeleteInvalidationSnapshot(
		t,
		ctx,
		cacheRedis,
		stateRedis,
		invalidationVersion,
		lookupVersion,
	)
	wrongOwnerBefore := readW5ManagementAPIKeyDeleteRowSnapshot(
		t,
		ctx,
		db,
		w5ManagementAPIKeyDeleteWrongOwnerID,
	)
	wrongOwner := serveW5ManagementAPIKeyDeleteRequest(
		router,
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyDeleteWrongOwnerID+
			"?systemAccountId="+w5ManagementAPIKeyListOtherID,
		w5ManagementAPIKeyListAdminToken,
		w5ManagementAPIKeyDeleteWrongOwnerTraceID,
	)
	assertW5ManagementAPIKeyDeleteError(
		t,
		wrongOwner,
		http.StatusNotFound,
		"API Key 不存在",
	)
	assertW5ManagementAPIKeyDeleteRowExists(
		t,
		ctx,
		db,
		w5ManagementAPIKeyDeleteWrongOwnerID,
		true,
	)
	assertW5ManagementAPIKeyDeleteRowSnapshotUnchanged(
		t,
		ctx,
		db,
		w5ManagementAPIKeyDeleteWrongOwnerID,
		wrongOwnerBefore,
	)
	assertW5ManagementAPIKeyDeleteCleanupMissing(
		t,
		ctx,
		db,
		w5ManagementAPIKeyDeleteWrongOwnerID,
	)
	assertW5ManagementAPIKeyDeleteInvalidationSnapshotUnchanged(
		t,
		ctx,
		cacheRedis,
		stateRedis,
		invalidationVersion,
		lookupVersion,
		wrongOwnerInvalidationBefore,
		"wrong-owner DELETE",
	)

	defaultInvalidationBefore := readW5ManagementAPIKeyDeleteInvalidationSnapshot(
		t,
		ctx,
		cacheRedis,
		stateRedis,
		invalidationVersion,
		lookupVersion,
	)
	defaultBefore := readW5ManagementAPIKeyDeleteRowSnapshot(
		t,
		ctx,
		db,
		w5ManagementAPIKeyListOwnerDefaultID,
	)
	defaultDelete := serveW5ManagementAPIKeyDeleteRequest(
		router,
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyListOwnerDefaultID,
		w5ManagementAPIKeyListAdminToken,
		w5ManagementAPIKeyDeleteDefaultTraceID,
	)
	assertW5ManagementAPIKeyDeleteError(
		t,
		defaultDelete,
		http.StatusConflict,
		"默认 API Key 不允许删除",
	)
	assertW5ManagementAPIKeyDeleteRowExists(
		t,
		ctx,
		db,
		w5ManagementAPIKeyListOwnerDefaultID,
		true,
	)
	assertW5ManagementAPIKeyDeleteRowSnapshotUnchanged(
		t,
		ctx,
		db,
		w5ManagementAPIKeyListOwnerDefaultID,
		defaultBefore,
	)
	assertW5ManagementAPIKeyDeleteCleanupMissing(
		t,
		ctx,
		db,
		w5ManagementAPIKeyListOwnerDefaultID,
	)
	assertW5ManagementAPIKeyDeleteInvalidationSnapshotUnchanged(
		t,
		ctx,
		cacheRedis,
		stateRedis,
		invalidationVersion,
		lookupVersion,
		defaultInvalidationBefore,
		"default DELETE",
	)

	validationKey := w5ManagementAPIKeyDeleteSharedCacheKey(
		t,
		gatewaycache.APIKeyValidationCacheName,
	)
	lookupKey := w5ManagementAPIKeyDeleteSharedCacheKey(
		t,
		gatewaycache.APIKeyLookupCacheName,
	)
	if err := cacheRedis.SetRaw(ctx, validationKey, []byte("delete-seed-validation"), time.Hour); err != nil {
		t.Fatalf("seed API Key delete validation version: %v", err)
	}
	if err := cacheRedis.SetRaw(ctx, lookupKey, []byte("delete-seed-lookup"), time.Hour); err != nil {
		t.Fatalf("seed API Key delete lookup version: %v", err)
	}

	versionBeforeAdmin := *invalidationVersion
	lookupVersionBeforeAdmin := *lookupVersion
	adminDelete := serveW5ManagementAPIKeyDeleteRequest(
		router,
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyDeleteAdminGlobalID,
		w5ManagementAPIKeyListAdminToken,
		w5ManagementAPIKeyDeleteAdminGlobalTraceID,
	)
	assertW5ManagementAPIKeyDeleteEmpty204(t, adminDelete)
	assertW5ManagementAPIKeyDeleteRowExists(
		t,
		ctx,
		db,
		w5ManagementAPIKeyDeleteAdminGlobalID,
		false,
	)
	adminCleanup := requireW5ManagementAPIKeyDeleteCleanupTarget(
		t,
		ctx,
		db,
		w5ManagementAPIKeyDeleteAdminGlobalID,
	)
	assertW5ManagementAPIKeyDeleteFreshCleanupTarget(
		t,
		adminCleanup,
		w5ManagementAPIKeyListOwnerID,
		now,
	)
	assertW5ManagementAPIKeyDeleteInvalidations(
		t,
		ctx,
		cacheRedis,
		stateRedis,
		w5ManagementAPIKeyDeleteAdminGlobalID,
		versionBeforeAdmin,
		lookupVersionBeforeAdmin,
		now,
	)
	if *invalidationVersion != versionBeforeAdmin+3 ||
		*lookupVersion != lookupVersionBeforeAdmin+1 {
		t.Fatalf(
			"API Key delete invalidation versions = %d/%d, want %d/%d",
			*invalidationVersion,
			*lookupVersion,
			versionBeforeAdmin+3,
			lookupVersionBeforeAdmin+1,
		)
	}

	repeatDelete := serveW5ManagementAPIKeyDeleteRequest(
		router,
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyDeleteAdminGlobalID,
		w5ManagementAPIKeyListAdminToken,
		w5ManagementAPIKeyDeleteRepeatTraceID,
	)
	assertW5ManagementAPIKeyDeleteError(
		t,
		repeatDelete,
		http.StatusNotFound,
		"API Key 不存在",
	)
	repeatCleanup := requireW5ManagementAPIKeyDeleteCleanupTarget(
		t,
		ctx,
		db,
		w5ManagementAPIKeyDeleteAdminGlobalID,
	)
	assertW5ManagementAPIKeyDeleteCleanupTargetEqual(
		t,
		repeatCleanup,
		adminCleanup,
		"second DELETE",
	)
	if *invalidationVersion != versionBeforeAdmin+3 ||
		*lookupVersion != lookupVersionBeforeAdmin+1 {
		t.Fatalf("second DELETE unexpectedly invalidated API Key caches")
	}

	versionBeforeSelf := *invalidationVersion
	lookupVersionBeforeSelf := *lookupVersion
	selfDelete := serveW5ManagementAPIKeyDeleteRequest(
		router,
		"/__aisys__/api/my-api-keys/"+w5ManagementAPIKeyDeleteSelfID+
			"?systemAccountId="+w5ManagementAPIKeyListOtherID,
		w5ManagementAPIKeyListOwnerToken,
		w5ManagementAPIKeyDeleteSelfTraceID,
	)
	assertW5ManagementAPIKeyDeleteEmpty204(t, selfDelete)
	assertW5ManagementAPIKeyDeleteRowExists(
		t,
		ctx,
		db,
		w5ManagementAPIKeyDeleteSelfID,
		false,
	)
	selfCleanup := requireW5ManagementAPIKeyDeleteCleanupTarget(
		t,
		ctx,
		db,
		w5ManagementAPIKeyDeleteSelfID,
	)
	assertW5ManagementAPIKeyDeleteReusedCleanupTarget(t, selfCleanup, preseed, now)
	if *invalidationVersion != versionBeforeSelf+3 ||
		*lookupVersion != lookupVersionBeforeSelf+1 {
		t.Fatalf(
			"self API Key delete invalidation versions = %d/%d, want %d/%d",
			*invalidationVersion,
			*lookupVersion,
			versionBeforeSelf+3,
			lookupVersionBeforeSelf+1,
		)
	}

	removeFailureTrigger := installW5ManagementAPIKeyDeleteCleanupFailureTrigger(t, ctx, db)
	defer removeFailureTrigger()
	cleanupFailureBefore := readW5ManagementAPIKeyDeleteRowSnapshot(
		t,
		ctx,
		db,
		w5ManagementAPIKeyDeleteCleanupFailureID,
	)
	cleanupFailure := serveW5ManagementAPIKeyDeleteRequest(
		router,
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyDeleteCleanupFailureID,
		w5ManagementAPIKeyListAdminToken,
		w5ManagementAPIKeyDeleteCleanupFailureTraceID,
	)
	assertW5ManagementAPIKeyDeleteError(
		t,
		cleanupFailure,
		http.StatusInternalServerError,
		"服务器内部错误",
	)
	assertW5ManagementAPIKeyDeleteRowExists(
		t,
		ctx,
		db,
		w5ManagementAPIKeyDeleteCleanupFailureID,
		true,
	)
	assertW5ManagementAPIKeyDeleteRowSnapshotUnchanged(
		t,
		ctx,
		db,
		w5ManagementAPIKeyDeleteCleanupFailureID,
		cleanupFailureBefore,
	)
	assertW5ManagementAPIKeyDeleteCleanupMissing(
		t,
		ctx,
		db,
		w5ManagementAPIKeyDeleteCleanupFailureID,
	)
	if *invalidationVersion != versionBeforeSelf+3 ||
		*lookupVersion != lookupVersionBeforeSelf+1 {
		t.Fatalf("rolled-back API Key delete unexpectedly invalidated caches")
	}

	beforeCommittedFailure := readW5ManagementAPIKeyDeleteInvalidationState(
		t,
		ctx,
		cacheRedis,
		stateRedis,
	)
	failingRouter := newW5ManagementAPIKeyDeleteFailingRedisRouter(
		t,
		store,
		redisCacheURL,
		stateRedis,
		operationLogOptions,
		cfg,
		logger,
		now,
	)
	committedFailure := serveW5ManagementAPIKeyDeleteRequest(
		failingRouter,
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyDeleteCommittedFailureID,
		w5ManagementAPIKeyListAdminToken,
		w5ManagementAPIKeyDeleteCommittedFailureTraceID,
	)
	assertW5ManagementAPIKeyDeleteError(
		t,
		committedFailure,
		http.StatusInternalServerError,
		"服务器内部错误",
	)
	assertW5ManagementAPIKeyDeleteRowExists(
		t,
		ctx,
		db,
		w5ManagementAPIKeyDeleteCommittedFailureID,
		false,
	)
	committedCleanup := requireW5ManagementAPIKeyDeleteCleanupTarget(
		t,
		ctx,
		db,
		w5ManagementAPIKeyDeleteCommittedFailureID,
	)
	assertW5ManagementAPIKeyDeleteFreshCleanupTarget(
		t,
		committedCleanup,
		w5ManagementAPIKeyListOwnerID,
		now,
	)
	afterCommittedFailure := readW5ManagementAPIKeyDeleteInvalidationState(
		t,
		ctx,
		cacheRedis,
		stateRedis,
	)
	if !beforeCommittedFailure.equal(afterCommittedFailure) {
		t.Fatalf("committed validation failure advanced lookup/runtime/quota invalidations")
	}

	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, workerErr); err != nil {
		t.Fatalf("wait for API Key delete operation logs: %v", err)
	}
	assertW5ManagementAPIKeyDeleteOperationLogs(t, ctx, db, now)
}

func insertW5ManagementAPIKeyDeleteFixtures(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
) {
	t.Helper()
	type fixture struct {
		id    string
		name  string
		owner string
		route string
	}
	fixtures := []fixture{
		{
			id:    w5ManagementAPIKeyDeleteAdminGlobalID,
			name:  "W5 Delete Admin Global",
			owner: w5ManagementAPIKeyListOwnerID,
			route: w5ManagementAPIKeyListOwnerPrimaryRouteID,
		},
		{
			id:    w5ManagementAPIKeyDeleteWrongOwnerID,
			name:  "W5 Delete Wrong Owner",
			owner: w5ManagementAPIKeyListOwnerID,
			route: w5ManagementAPIKeyListOwnerPrimaryRouteID,
		},
		{
			id:    w5ManagementAPIKeyDeleteSelfID,
			name:  "W5 Delete Self",
			owner: w5ManagementAPIKeyListOwnerID,
			route: w5ManagementAPIKeyListOwnerPrimaryRouteID,
		},
		{
			id:    w5ManagementAPIKeyDeleteCleanupFailureID,
			name:  "W5 Delete Cleanup Failure",
			owner: w5ManagementAPIKeyListOwnerID,
			route: w5ManagementAPIKeyListOwnerPrimaryRouteID,
		},
		{
			id:    w5ManagementAPIKeyDeleteCommittedFailureID,
			name:  "W5 Delete Committed Failure",
			owner: w5ManagementAPIKeyListOwnerID,
			route: w5ManagementAPIKeyListOwnerPrimaryRouteID,
		},
	}
	for index, item := range fixtures {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.api_keys (
				id, system_account_id, route_strategy_id, name, description,
				key_hash, key_prefix, key_suffix, key_secret_encrypted,
				status, is_default, expires_at, quota_limits_json,
				availability_schedule_json, availability_schedule_next_check_at,
				last_used_at, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, NULL,
				$5, $6, $7, $8,
				'active', false, NULL, NULL,
				NULL, NULL,
				NULL, $9, $9
			)
		`,
			item.id,
			item.owner,
			item.route,
			item.name,
			w5ManagementAPIKeyDeleteHashCanary+"-"+item.id,
			fmt.Sprintf("%s-%d", w5ManagementAPIKeyDeletePrefixCanary, index+1),
			fmt.Sprintf("%s-%d", w5ManagementAPIKeyDeleteSuffixCanary, index+1),
			w5ManagementAPIKeyDeleteCiphertextCanary+"-"+item.id,
			now.Add(-time.Duration(index+1)*time.Hour),
		); err != nil {
			t.Fatalf("insert API Key delete fixture %s: %v", item.id, err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.api_keys
		SET key_hash = $2,
		    key_prefix = $3,
		    key_suffix = $4,
		    key_secret_encrypted = $5
		WHERE id = $1
	`,
		w5ManagementAPIKeyListOwnerDefaultID,
		w5ManagementAPIKeyDeleteHashCanary+"-default",
		w5ManagementAPIKeyDeletePrefixCanary+"-default",
		w5ManagementAPIKeyDeleteSuffixCanary+"-default",
		w5ManagementAPIKeyDeleteCiphertextCanary+"-default",
	); err != nil {
		t.Fatalf("prepare default API Key delete canaries: %v", err)
	}
}

type w5ManagementAPIKeyDeleteCleanupTarget struct {
	APIKeyID          string
	SystemAccountID   string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	AttemptCount      int
	LastAttemptAt     sql.NullTime
	LastBlockedReason sql.NullString
	LastErrorMessage  sql.NullString
}

func preseedW5ManagementAPIKeyDeleteCleanupTarget(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
) w5ManagementAPIKeyDeleteCleanupTarget {
	t.Helper()
	createdAt := now.Add(-3 * time.Hour)
	updatedAt := now.Add(-2 * time.Hour)
	lastAttemptAt := now.Add(-90 * time.Minute)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_dataset.api_key_record_cleanup_targets (
			api_key_id,
			system_account_id,
			created_at,
			updated_at,
			attempt_count,
			last_attempt_at,
			last_blocked_reason,
			last_error_message
		) VALUES ($1, $2, $3, $4, 4, $5, $6, $7)
	`,
		w5ManagementAPIKeyDeleteSelfID,
		w5ManagementAPIKeyListOtherID,
		createdAt,
		updatedAt,
		lastAttemptAt,
		"retry_after_fixture",
		"fixture last error",
	); err != nil {
		t.Fatalf("preseed API Key delete cleanup target: %v", err)
	}
	return requireW5ManagementAPIKeyDeleteCleanupTarget(
		t,
		ctx,
		db,
		w5ManagementAPIKeyDeleteSelfID,
	)
}

func serveW5ManagementAPIKeyDeleteRequest(
	router http.Handler,
	path string,
	sessionToken string,
	requestID string,
) *httptest.ResponseRecorder {
	return serveW5ManagementAPIKeySecretRequest(
		router,
		http.MethodDelete,
		path,
		sessionToken,
		"",
		requestID,
	)
}

func assertW5ManagementAPIKeyDeleteEmpty204(
	t *testing.T,
	rec *httptest.ResponseRecorder,
) {
	t.Helper()
	assertW5ManagementAPIKeyDeleteNoSecretLeakage(t, rec)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("API Key DELETE status = %d, bodyBytes = %d", rec.Code, rec.Body.Len())
	}
	assertW5ManagementAPIKeyDeleteNoStore(t, rec)
	if rec.Body.Len() != 0 {
		t.Fatalf("API Key DELETE 204 body bytes = %d, want 0", rec.Body.Len())
	}
}

func assertW5ManagementAPIKeyDeleteError(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	wantStatus int,
	wantMessage string,
) {
	t.Helper()
	assertW5ManagementAPIKeyDeleteNoSecretLeakage(t, rec)
	if rec.Code != wantStatus {
		t.Fatalf(
			"API Key DELETE error status = %d, want %d, bodyBytes = %d",
			rec.Code,
			wantStatus,
			rec.Body.Len(),
		)
	}
	assertW5ManagementAPIKeyDeleteNoStore(t, rec)
	var response map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode API Key DELETE error: %v", err)
	}
	if len(response) != 1 {
		t.Fatalf("API Key DELETE error fields = %d, want only message", len(response))
	}
	rawMessage, ok := response["message"]
	if !ok {
		t.Fatal("API Key DELETE error omitted message")
	}
	var message string
	if err := json.Unmarshal(rawMessage, &message); err != nil {
		t.Fatalf("decode API Key DELETE error message: %v", err)
	}
	if message != wantMessage {
		t.Fatalf("API Key DELETE message = %q, want %q", message, wantMessage)
	}
}

func assertW5ManagementAPIKeyDeleteNoStore(
	t *testing.T,
	rec *httptest.ResponseRecorder,
) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("API Key DELETE Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("API Key DELETE Pragma = %q, want no-cache", got)
	}
}

func assertW5ManagementAPIKeyDeleteNoSecretLeakage(
	t *testing.T,
	rec *httptest.ResponseRecorder,
) {
	t.Helper()
	assertW5ManagementAPIKeyDeleteTextSecretFree(t, "response body", rec.Body.String())
	for _, header := range []string{
		"Authentication-Info",
		"Authorization",
		"Cookie",
		"Proxy-Authorization",
		"Proxy-Authentication-Info",
		"Set-Cookie",
		"X-Api-Key",
		"X-Auth-Token",
		"X-Openai-Api-Key",
	} {
		if _, exists := rec.Header()[http.CanonicalHeaderKey(header)]; exists {
			t.Fatalf("API Key DELETE response exposed sensitive header %s", header)
		}
	}
	for name, values := range rec.Header() {
		assertW5ManagementAPIKeyDeleteTextSecretFree(t, "response header name", name)
		for _, value := range values {
			assertW5ManagementAPIKeyDeleteTextSecretFree(
				t,
				"response header "+name,
				value,
			)
		}
	}
}

func assertW5ManagementAPIKeyDeleteTextSecretFree(
	t *testing.T,
	label string,
	value string,
) {
	t.Helper()
	for index, canary := range w5ManagementAPIKeyDeleteSecretCanaries() {
		if strings.Contains(value, canary) {
			t.Fatalf("API Key DELETE %s leaked secret canary index %d", label, index)
		}
	}
}

func w5ManagementAPIKeyDeleteSecretCanaries() []string {
	return []string{
		w5ManagementAPIKeyDeleteHashCanary,
		w5ManagementAPIKeyDeletePrefixCanary,
		w5ManagementAPIKeyDeleteSuffixCanary,
		w5ManagementAPIKeyDeleteCiphertextCanary,
	}
}

func assertW5ManagementAPIKeyDeleteRowExists(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	apiKeyID string,
	want bool,
) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_business.api_keys
		WHERE id = $1
	`, apiKeyID).Scan(&count); err != nil {
		t.Fatalf("count API Key delete row %s: %v", apiKeyID, err)
	}
	if got := count == 1; got != want {
		t.Fatalf("API Key delete row %s exists = %t, want %t", apiKeyID, got, want)
	}
}

func readW5ManagementAPIKeyDeleteRowSnapshot(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	apiKeyID string,
) []byte {
	t.Helper()
	var snapshot []byte
	if err := db.QueryRowContext(ctx, `
		SELECT row_to_json(api_keys)::text
		FROM juhe_business.api_keys AS api_keys
		WHERE id = $1
	`, apiKeyID).Scan(&snapshot); err != nil {
		t.Fatalf("read API Key delete row snapshot %s: %v", apiKeyID, err)
	}
	return bytes.Clone(snapshot)
}

func assertW5ManagementAPIKeyDeleteRowSnapshotUnchanged(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	apiKeyID string,
	before []byte,
) {
	t.Helper()
	after := readW5ManagementAPIKeyDeleteRowSnapshot(t, ctx, db, apiKeyID)
	if !bytes.Equal(after, before) {
		t.Fatalf("API Key delete row %s changed unexpectedly", apiKeyID)
	}
}

func requireW5ManagementAPIKeyDeleteCleanupTarget(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	apiKeyID string,
) w5ManagementAPIKeyDeleteCleanupTarget {
	t.Helper()
	var target w5ManagementAPIKeyDeleteCleanupTarget
	if err := db.QueryRowContext(ctx, `
		SELECT
			api_key_id,
			system_account_id,
			created_at,
			updated_at,
			attempt_count,
			last_attempt_at,
			last_blocked_reason,
			last_error_message
		FROM juhe_dataset.api_key_record_cleanup_targets
		WHERE api_key_id = $1
	`, apiKeyID).Scan(
		&target.APIKeyID,
		&target.SystemAccountID,
		&target.CreatedAt,
		&target.UpdatedAt,
		&target.AttemptCount,
		&target.LastAttemptAt,
		&target.LastBlockedReason,
		&target.LastErrorMessage,
	); err != nil {
		t.Fatalf("read API Key delete cleanup target %s: %v", apiKeyID, err)
	}
	return target
}

func assertW5ManagementAPIKeyDeleteCleanupMissing(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	apiKeyID string,
) {
	t.Helper()
	var value string
	err := db.QueryRowContext(ctx, `
		SELECT api_key_id
		FROM juhe_dataset.api_key_record_cleanup_targets
		WHERE api_key_id = $1
	`, apiKeyID).Scan(&value)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("API Key delete cleanup target %s error = %v, want no rows", apiKeyID, err)
	}
}

func assertW5ManagementAPIKeyDeleteFreshCleanupTarget(
	t *testing.T,
	target w5ManagementAPIKeyDeleteCleanupTarget,
	wantOwner string,
	wantTime time.Time,
) {
	t.Helper()
	if target.SystemAccountID != wantOwner ||
		!target.CreatedAt.UTC().Equal(wantTime.UTC()) ||
		!target.UpdatedAt.UTC().Equal(wantTime.UTC()) ||
		target.AttemptCount != 0 ||
		target.LastAttemptAt.Valid ||
		target.LastBlockedReason.Valid ||
		target.LastErrorMessage.Valid {
		t.Fatalf("fresh API Key delete cleanup target = %+v", target)
	}
}

func assertW5ManagementAPIKeyDeleteCleanupTargetEqual(
	t *testing.T,
	got w5ManagementAPIKeyDeleteCleanupTarget,
	want w5ManagementAPIKeyDeleteCleanupTarget,
	label string,
) {
	t.Helper()
	if got.APIKeyID != want.APIKeyID ||
		got.SystemAccountID != want.SystemAccountID ||
		!got.CreatedAt.UTC().Equal(want.CreatedAt.UTC()) ||
		!got.UpdatedAt.UTC().Equal(want.UpdatedAt.UTC()) ||
		got.AttemptCount != want.AttemptCount ||
		got.LastAttemptAt.Valid != want.LastAttemptAt.Valid ||
		(got.LastAttemptAt.Valid &&
			!got.LastAttemptAt.Time.UTC().Equal(want.LastAttemptAt.Time.UTC())) ||
		got.LastBlockedReason != want.LastBlockedReason ||
		got.LastErrorMessage != want.LastErrorMessage {
		t.Fatalf("%s cleanup target = %+v, want %+v", label, got, want)
	}
}

func assertW5ManagementAPIKeyDeleteReusedCleanupTarget(
	t *testing.T,
	got w5ManagementAPIKeyDeleteCleanupTarget,
	before w5ManagementAPIKeyDeleteCleanupTarget,
	now time.Time,
) {
	t.Helper()
	if got.SystemAccountID != w5ManagementAPIKeyListOwnerID ||
		!got.CreatedAt.UTC().Equal(before.CreatedAt.UTC()) ||
		!got.UpdatedAt.UTC().Equal(now.UTC()) ||
		got.UpdatedAt.UTC().Equal(before.UpdatedAt.UTC()) ||
		got.AttemptCount != before.AttemptCount ||
		got.LastAttemptAt.Valid != before.LastAttemptAt.Valid ||
		(got.LastAttemptAt.Valid &&
			!got.LastAttemptAt.Time.UTC().Equal(before.LastAttemptAt.Time.UTC())) ||
		got.LastBlockedReason != before.LastBlockedReason ||
		got.LastErrorMessage != before.LastErrorMessage {
		t.Fatalf("reused API Key delete cleanup target = %+v, before %+v", got, before)
	}
}

func w5ManagementAPIKeyDeleteSharedCacheKey(
	t *testing.T,
	cacheName string,
) string {
	t.Helper()
	key, err := gatewaycache.SharedCacheVersionKey(
		w5ManagementAPIKeySecretRedisNamespace,
		cacheName,
	)
	if err != nil {
		t.Fatalf("build API Key delete shared cache key %s: %v", cacheName, err)
	}
	return key
}

func assertW5ManagementAPIKeyDeleteInvalidations(
	t *testing.T,
	ctx context.Context,
	cacheRedis *redisplatform.Client,
	stateRedis *redisplatform.Client,
	apiKeyID string,
	versionBefore int,
	lookupVersionBefore int,
	now time.Time,
) {
	t.Helper()
	validationVersion, err := cacheRedis.GetRaw(
		ctx,
		w5ManagementAPIKeyDeleteSharedCacheKey(
			t,
			gatewaycache.APIKeyValidationCacheName,
		),
	)
	if err != nil {
		t.Fatalf("read API Key delete validation version: %v", err)
	}
	lookupVersion, err := cacheRedis.GetRaw(
		ctx,
		w5ManagementAPIKeyDeleteSharedCacheKey(
			t,
			gatewaycache.APIKeyLookupCacheName,
		),
	)
	if err != nil {
		t.Fatalf("read API Key delete lookup version: %v", err)
	}
	wantValidationVersion := fmt.Sprintf(
		"w5-api-key-secret-version-%d",
		versionBefore+1,
	)
	wantLookupVersion := fmt.Sprintf(
		"w5-api-key-secret-lookup-version-%d",
		lookupVersionBefore+1,
	)
	if string(validationVersion) != wantValidationVersion ||
		string(lookupVersion) != wantLookupVersion {
		t.Fatalf(
			"API Key delete cache versions validation=%q lookup=%q, want %q/%q",
			validationVersion,
			lookupVersion,
			wantValidationVersion,
			wantLookupVersion,
		)
	}
	assertW5ManagementAPIKeySecretInvalidationTopicWithReason(
		t,
		ctx,
		stateRedis,
		gatewaycache.GatewayRuntimeCacheTopic,
		fmt.Sprintf("w5-api-key-secret-version-%d", versionBefore+2),
		"",
		"api_key_deleted",
		now,
	)
	assertW5ManagementAPIKeySecretInvalidationTopicWithReason(
		t,
		ctx,
		stateRedis,
		gatewaycache.APIKeyQuotaCacheTopic,
		fmt.Sprintf("w5-api-key-secret-version-%d", versionBefore+3),
		apiKeyID,
		"api_key_deleted",
		now,
	)
}

type w5ManagementAPIKeyDeleteInvalidationState struct {
	Validation []byte
	Lookup     []byte
	Runtime    []byte
	Quota      []byte
}

type w5ManagementAPIKeyDeleteInvalidationSnapshot struct {
	InvalidationVersion int
	LookupVersion       int
	State               w5ManagementAPIKeyDeleteInvalidationState
}

func readW5ManagementAPIKeyDeleteInvalidationSnapshot(
	t *testing.T,
	ctx context.Context,
	cacheRedis *redisplatform.Client,
	stateRedis *redisplatform.Client,
	invalidationVersion *int,
	lookupVersion *int,
) w5ManagementAPIKeyDeleteInvalidationSnapshot {
	t.Helper()
	return w5ManagementAPIKeyDeleteInvalidationSnapshot{
		InvalidationVersion: *invalidationVersion,
		LookupVersion:       *lookupVersion,
		State: readW5ManagementAPIKeyDeleteInvalidationState(
			t,
			ctx,
			cacheRedis,
			stateRedis,
		),
	}
}

func assertW5ManagementAPIKeyDeleteInvalidationSnapshotUnchanged(
	t *testing.T,
	ctx context.Context,
	cacheRedis *redisplatform.Client,
	stateRedis *redisplatform.Client,
	invalidationVersion *int,
	lookupVersion *int,
	before w5ManagementAPIKeyDeleteInvalidationSnapshot,
	label string,
) {
	t.Helper()
	after := readW5ManagementAPIKeyDeleteInvalidationSnapshot(
		t,
		ctx,
		cacheRedis,
		stateRedis,
		invalidationVersion,
		lookupVersion,
	)
	if after.InvalidationVersion != before.InvalidationVersion ||
		after.LookupVersion != before.LookupVersion ||
		!after.State.equal(before.State) {
		t.Fatalf("%s changed API Key invalidation counters or Redis state", label)
	}
}

func (s w5ManagementAPIKeyDeleteInvalidationState) equal(
	other w5ManagementAPIKeyDeleteInvalidationState,
) bool {
	return bytes.Equal(s.Validation, other.Validation) &&
		bytes.Equal(s.Lookup, other.Lookup) &&
		bytes.Equal(s.Runtime, other.Runtime) &&
		bytes.Equal(s.Quota, other.Quota)
}

func readW5ManagementAPIKeyDeleteInvalidationState(
	t *testing.T,
	ctx context.Context,
	cacheRedis *redisplatform.Client,
	stateRedis *redisplatform.Client,
) w5ManagementAPIKeyDeleteInvalidationState {
	t.Helper()
	return w5ManagementAPIKeyDeleteInvalidationState{
		Validation: readW5ManagementAPIKeyDeleteRedisValue(
			t,
			ctx,
			cacheRedis,
			w5ManagementAPIKeyDeleteSharedCacheKey(
				t,
				gatewaycache.APIKeyValidationCacheName,
			),
		),
		Lookup: readW5ManagementAPIKeyDeleteRedisValue(
			t,
			ctx,
			cacheRedis,
			w5ManagementAPIKeyDeleteSharedCacheKey(
				t,
				gatewaycache.APIKeyLookupCacheName,
			),
		),
		Runtime: readW5ManagementAPIKeyDeleteRedisValue(
			t,
			ctx,
			stateRedis,
			w5ManagementAPIKeyDeleteRuntimeStateKey(
				t,
				gatewaycache.GatewayRuntimeCacheTopic,
			),
		),
		Quota: readW5ManagementAPIKeyDeleteRedisValue(
			t,
			ctx,
			stateRedis,
			w5ManagementAPIKeyDeleteRuntimeStateKey(
				t,
				gatewaycache.APIKeyQuotaCacheTopic,
			),
		),
	}
}

func readW5ManagementAPIKeyDeleteRedisValue(
	t *testing.T,
	ctx context.Context,
	client *redisplatform.Client,
	key string,
) []byte {
	t.Helper()
	value, err := client.GetRaw(ctx, key)
	if err != nil {
		t.Fatalf("read API Key delete Redis state: %v", err)
	}
	return bytes.Clone(value)
}

func w5ManagementAPIKeyDeleteRuntimeStateKey(
	t *testing.T,
	topic string,
) string {
	t.Helper()
	key, err := gatewaycache.RuntimeStateKey(
		w5ManagementAPIKeySecretRedisNamespace,
		gatewaycache.RuntimeInvalidationStoreName,
		"topic:"+topic,
	)
	if err != nil {
		t.Fatalf("build API Key delete runtime state key %s: %v", topic, err)
	}
	return key
}

func installW5ManagementAPIKeyDeleteCleanupFailureTrigger(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) func() {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		CREATE OR REPLACE FUNCTION juhe_dataset.w5_management_api_key_delete_fail_cleanup()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $$
		BEGIN
			IF NEW.api_key_id = 'key_w5_management_api_key_delete_cleanup_failure' THEN
				RAISE EXCEPTION 'forced API Key cleanup target failure';
			END IF;
			RETURN NEW;
		END;
		$$
	`); err != nil {
		t.Fatalf("create API Key delete cleanup failure function: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		DROP TRIGGER IF EXISTS w5_management_api_key_delete_fail_cleanup
		ON juhe_dataset.api_key_record_cleanup_targets
	`); err != nil {
		t.Fatalf("drop stale API Key delete cleanup failure trigger: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TRIGGER w5_management_api_key_delete_fail_cleanup
		BEFORE INSERT OR UPDATE
		ON juhe_dataset.api_key_record_cleanup_targets
		FOR EACH ROW
		EXECUTE FUNCTION juhe_dataset.w5_management_api_key_delete_fail_cleanup()
	`); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, cleanupErr := db.ExecContext(cleanupCtx, `
			DROP FUNCTION IF EXISTS juhe_dataset.w5_management_api_key_delete_fail_cleanup()
		`); cleanupErr != nil {
			t.Fatalf(
				"create API Key delete cleanup failure trigger: %v; cleanup function: %v",
				err,
				cleanupErr,
			)
		}
		t.Fatalf("create API Key delete cleanup failure trigger: %v", err)
	}
	return func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := db.ExecContext(cleanupCtx, `
			DROP TRIGGER IF EXISTS w5_management_api_key_delete_fail_cleanup
			ON juhe_dataset.api_key_record_cleanup_targets
		`); err != nil {
			t.Errorf("drop API Key delete cleanup failure trigger: %v", err)
		}
		if _, err := db.ExecContext(cleanupCtx, `
			DROP FUNCTION IF EXISTS juhe_dataset.w5_management_api_key_delete_fail_cleanup()
		`); err != nil {
			t.Errorf("drop API Key delete cleanup failure function: %v", err)
		}
	}
}

func newW5ManagementAPIKeyDeleteFailingRedisRouter(
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
		w5ManagementAPIKeySecretRedisNamespace+":delete-failure",
	)
	if err != nil {
		t.Fatalf("open failing API Key delete cache Redis client: %v", err)
	}
	invalidator, err := gatewaycache.NewSystemAccountInvalidator(
		gatewaycache.SystemAccountInvalidatorOptions{
			Cache:     failingCache,
			State:     stateRedis,
			Namespace: w5ManagementAPIKeySecretRedisNamespace,
			Now:       func() time.Time { return now },
			NewVersion: func(time.Time) (string, error) {
				return "w5-api-key-delete-failing-version", nil
			},
		},
	)
	if err != nil {
		_ = failingCache.Close()
		t.Fatalf("create failing API Key delete invalidator: %v", err)
	}
	if err := failingCache.Close(); err != nil {
		t.Fatalf("close API Key delete validation cache client: %v", err)
	}

	service := managementapikeys.NewServiceWithOptions(managementapikeys.ServiceOptions{
		Deleter:     store,
		Invalidator: invalidator,
		Now:         func() time.Time { return now },
	})
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	return httpapi.NewRouter(httpapi.RouterOptions{
		Config:                           cfg,
		Logger:                           logger,
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementAPIKeyDeleteHandler: httpapi.NewManagementAPIKeyDeleteHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
		ManagementMyAPIKeyDeleteHandler: httpapi.NewManagementMyAPIKeyDeleteHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
	})
}

func assertW5ManagementAPIKeyDeleteOperationLogs(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
) {
	t.Helper()
	ids := []string{
		w5ManagementAPIKeyDeleteAdminGlobalLogID,
		w5ManagementAPIKeyDeleteSelfLogID,
		w5ManagementAPIKeyDeleteCommittedFailureLogID,
	}
	var total int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_dataset.operation_logs
		WHERE id IN ($1, $2, $3)
	`, ids[0], ids[1], ids[2]).Scan(&total); err != nil {
		t.Fatalf("count API Key delete operation logs: %v", err)
	}
	if total != len(ids) {
		t.Fatalf("API Key delete operation log count = %d, want %d", total, len(ids))
	}
	assertW5ManagementAPIKeyDeleteOperationLogsSecretFree(t, ctx, db, ids)

	assertW5ManagementAPIKeyDeleteOperationLog(
		t,
		ctx,
		db,
		w5ManagementAPIKeyDeleteAdminGlobalLogID,
		w5ManagementAPIKeyDeleteAdminGlobalTraceID,
		w5ManagementAPIKeyListAdminID,
		"admin",
		w5ManagementAPIKeyDeleteAdminGlobalID,
		"W5 Delete Admin Global",
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyDeleteAdminGlobalID,
		http.StatusNoContent,
		now,
	)
	assertW5ManagementAPIKeyDeleteOperationLog(
		t,
		ctx,
		db,
		w5ManagementAPIKeyDeleteSelfLogID,
		w5ManagementAPIKeyDeleteSelfTraceID,
		w5ManagementAPIKeyListOwnerID,
		"self",
		w5ManagementAPIKeyDeleteSelfID,
		"W5 Delete Self",
		"/__aisys__/api/my-api-keys/"+w5ManagementAPIKeyDeleteSelfID,
		http.StatusNoContent,
		now,
	)
	assertW5ManagementAPIKeyDeleteOperationLog(
		t,
		ctx,
		db,
		w5ManagementAPIKeyDeleteCommittedFailureLogID,
		w5ManagementAPIKeyDeleteCommittedFailureTraceID,
		w5ManagementAPIKeyListAdminID,
		"admin",
		w5ManagementAPIKeyDeleteCommittedFailureID,
		"W5 Delete Committed Failure",
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyDeleteCommittedFailureID,
		http.StatusInternalServerError,
		now,
	)

	var adminResourceLogs int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_dataset.operation_logs
		WHERE operation_key = 'api_keys.delete'
		  AND resource_id = $1
	`, w5ManagementAPIKeyDeleteAdminGlobalID).Scan(&adminResourceLogs); err != nil {
		t.Fatalf("count repeated API Key delete logs: %v", err)
	}
	if adminResourceLogs != 1 {
		t.Fatalf("repeated API Key DELETE committed logs = %d, want 1", adminResourceLogs)
	}

	var uncommittedLogs int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_dataset.operation_logs
		WHERE trace_id IN ($1, $2, $3, $4)
	`,
		w5ManagementAPIKeyDeleteWrongOwnerTraceID,
		w5ManagementAPIKeyDeleteDefaultTraceID,
		w5ManagementAPIKeyDeleteRepeatTraceID,
		w5ManagementAPIKeyDeleteCleanupFailureTraceID,
	).Scan(&uncommittedLogs); err != nil {
		t.Fatalf("count uncommitted API Key delete operation logs: %v", err)
	}
	if uncommittedLogs != 0 {
		t.Fatalf("uncommitted API Key deletes wrote %d operation logs", uncommittedLogs)
	}
}

func assertW5ManagementAPIKeyDeleteOperationLog(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	logID string,
	traceID string,
	actorID string,
	mode string,
	resourceID string,
	resourceName string,
	path string,
	statusCode int,
	now time.Time,
) {
	t.Helper()
	var row struct {
		TraceID      string
		ActorID      string
		ScopeID      string
		Mode         string
		Module       string
		Action       string
		OperationKey string
		ResourceType string
		ResourceID   string
		ResourceName string
		Summary      string
		DetailLevel  string
		Visibility   string
		ChangesJSON  string
		MetadataJSON string
		Method       string
		Path         string
		StatusCode   int
		ClientIP     string
		UserAgent    string
		CreatedAt    time.Time
	}
	if err := db.QueryRowContext(ctx, `
		SELECT
			trace_id,
			actor_system_account_id,
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
	`, logID).Scan(
		&row.TraceID,
		&row.ActorID,
		&row.ScopeID,
		&row.Mode,
		&row.Module,
		&row.Action,
		&row.OperationKey,
		&row.ResourceType,
		&row.ResourceID,
		&row.ResourceName,
		&row.Summary,
		&row.DetailLevel,
		&row.Visibility,
		&row.ChangesJSON,
		&row.MetadataJSON,
		&row.Method,
		&row.Path,
		&row.StatusCode,
		&row.ClientIP,
		&row.UserAgent,
		&row.CreatedAt,
	); err != nil {
		t.Fatalf("read API Key delete operation log %s: %v", logID, err)
	}
	if row.TraceID != traceID ||
		row.ActorID != actorID ||
		row.ScopeID != w5ManagementAPIKeyListOwnerID ||
		row.Mode != mode ||
		row.Module != "api_keys" ||
		row.Action != "delete" ||
		row.OperationKey != "api_keys.delete" ||
		row.ResourceType != "api_key" ||
		row.ResourceID != resourceID ||
		row.ResourceName != resourceName ||
		row.Summary != "删除 API Key："+resourceName ||
		row.DetailLevel != "full" ||
		row.Visibility != "targeted" ||
		row.Method != http.MethodDelete ||
		row.Path != path ||
		row.StatusCode != statusCode ||
		row.ClientIP != "127.0.0.1" ||
		row.UserAgent != "w5-management-api-key-secret-smoke" ||
		!row.CreatedAt.UTC().Equal(now.UTC()) {
		t.Fatalf("API Key delete operation log %s = %+v", logID, row)
	}
	if row.MetadataJSON != "{}" {
		t.Fatalf("API Key delete operation log %s metadata = %s", logID, row.MetadataJSON)
	}
	var changes []struct {
		Field  string `json:"field"`
		Label  string `json:"label"`
		Before bool   `json:"before"`
		After  bool   `json:"after"`
	}
	if err := json.Unmarshal([]byte(row.ChangesJSON), &changes); err != nil {
		t.Fatalf("decode API Key delete operation log changes %s: %v", logID, err)
	}
	if len(changes) != 1 ||
		changes[0].Field != "deleted" ||
		changes[0].Label != "删除状态" ||
		changes[0].Before ||
		!changes[0].After {
		t.Fatalf("API Key delete operation log changes %s = %+v", logID, changes)
	}
	assertW5ManagementAPIKeyDeleteOperationLogViewer(t, ctx, db, logID)
}

func assertW5ManagementAPIKeyDeleteOperationLogViewer(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	logID string,
) {
	t.Helper()
	var actorSystemAccountID string
	if err := db.QueryRowContext(ctx, `
		SELECT actor_system_account_id
		FROM juhe_dataset.operation_logs
		WHERE id = $1
	`, logID).Scan(&actorSystemAccountID); err != nil {
		t.Fatalf("query API Key delete operation log actor %s: %v", logID, err)
	}
	rows, err := db.QueryContext(ctx, `
		SELECT system_account_id, visibility_reason, detail_level
		FROM juhe_dataset.operation_log_viewers
		WHERE operation_log_id = $1
	`, logID)
	if err != nil {
		t.Fatalf("query API Key delete operation log viewers %s: %v", logID, err)
	}
	defer rows.Close()

	want := map[string]string{
		actorSystemAccountID + "\x00actor_self":              "full",
		w5ManagementAPIKeyListOwnerID + "\x00resource_owner": "full",
	}
	got := make(map[string]string, len(want))
	for rows.Next() {
		var systemAccountID string
		var visibilityReason string
		var detailLevel string
		if err := rows.Scan(&systemAccountID, &visibilityReason, &detailLevel); err != nil {
			t.Fatalf("scan API Key delete operation log viewer %s: %v", logID, err)
		}
		got[systemAccountID+"\x00"+visibilityReason] = detailLevel
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate API Key delete operation log viewers %s: %v", logID, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("API Key delete operation log viewers %s = %+v, want %+v", logID, got, want)
	}
}

func assertW5ManagementAPIKeyDeleteOperationLogsSecretFree(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	ids []string,
) {
	t.Helper()
	if len(ids) != 3 {
		t.Fatalf("API Key delete operation log secret scan IDs = %d, want 3", len(ids))
	}
	queries := []struct {
		label string
		sql   string
	}{
		{
			label: "operation_logs",
			sql: `
				SELECT row_to_json(operation_logs)::text
				FROM juhe_dataset.operation_logs
				WHERE id IN ($1, $2, $3)
			`,
		},
		{
			label: "operation_log_targets",
			sql: `
				SELECT row_to_json(operation_log_targets)::text
				FROM juhe_dataset.operation_log_targets
				WHERE operation_log_id IN ($1, $2, $3)
			`,
		},
		{
			label: "operation_log_viewers",
			sql: `
				SELECT row_to_json(operation_log_viewers)::text
				FROM juhe_dataset.operation_log_viewers
				WHERE operation_log_id IN ($1, $2, $3)
			`,
		},
		{
			label: "operation_log_summary_search_terms",
			sql: `
				SELECT row_to_json(operation_log_summary_search_terms)::text
				FROM juhe_dataset.operation_log_summary_search_terms
				WHERE operation_log_id IN ($1, $2, $3)
			`,
		},
	}
	for _, query := range queries {
		rows, err := db.QueryContext(ctx, query.sql, ids[0], ids[1], ids[2])
		if err != nil {
			t.Fatalf("query API Key delete %s secret scan: %v", query.label, err)
		}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				t.Fatalf("scan API Key delete %s secret row: %v", query.label, err)
			}
			assertW5ManagementAPIKeyDeleteTextSecretFree(t, query.label, raw)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("iterate API Key delete %s secret rows: %v", query.label, err)
		}
		rows.Close()
	}
}
