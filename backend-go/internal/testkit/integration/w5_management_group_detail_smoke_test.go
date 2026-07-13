//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementgroups"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w5ManagementGroupDetailNamespace = "w5-management-group-detail"

	w5ManagementGroupDetailAdminID   = "sys_w5_management_group_detail_admin"
	w5ManagementGroupDetailOwnerID   = "sys_w5_management_group_detail_owner"
	w5ManagementGroupDetailGranteeID = "sys_w5_management_group_detail_grantee"

	w5ManagementGroupDetailAdminSession   = "sess_w5_management_group_detail_admin"
	w5ManagementGroupDetailGranteeSession = "sess_w5_management_group_detail_grantee"
	w5ManagementGroupDetailAdminToken     = "w5-management-group-detail-admin-session"
	w5ManagementGroupDetailGranteeToken   = "w5-management-group-detail-grantee-session"

	w5ManagementGroupDetailOwnedID      = "grp_w5_management_group_detail_owned"
	w5ManagementGroupDetailAuthorizedID = "grp_w5_management_group_detail_authorized"
	w5ManagementGroupDetailAuthID       = "rauth_w5_management_group_detail_authorized"

	w5ManagementGroupDetailOwnedAccount1 = "acct_w5_management_group_detail_owned_1"
	w5ManagementGroupDetailOwnedAccount2 = "acct_w5_management_group_detail_owned_2"
	w5ManagementGroupDetailAuthAccount1  = "acct_w5_management_group_detail_auth_1"
	w5ManagementGroupDetailAuthAccount2  = "acct_w5_management_group_detail_auth_2"
)

func TestW5ManagementGroupDetailPostgresRedisSmoke(t *testing.T) {
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
	redisStateURL := w3RedisURLWithDB(t, redisURL, 1)
	stateRedis, err := redisplatform.NewClient(
		redisStateURL,
		w5ManagementGroupDetailNamespace+":state",
	)
	if err != nil {
		t.Fatalf("open state redis: %v", err)
	}
	defer closeRedisClient(t, stateRedis)
	accountConcurrency, err := redisplatform.NewAccountConcurrencyReader(
		stateRedis,
		w5ManagementGroupDetailNamespace,
	)
	if err != nil {
		t.Fatalf("create account concurrency reader: %v", err)
	}
	redisOptions, err := redis.ParseURL(redisStateURL)
	if err != nil {
		t.Fatalf("parse redis state URL: %v", err)
	}
	rawRedis := redis.NewClient(redisOptions)
	defer func() { _ = rawRedis.Close() }()

	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	sessionLastSeenAt := now.Add(-20 * time.Minute)
	insertW5ManagementGroupDetailFixtures(t, ctx, db, now)
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w5ManagementGroupDetailAdminSession,
		w5ManagementGroupDetailAdminID,
		w5ManagementGroupDetailAdminToken,
		sessionLastSeenAt,
	)
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w5ManagementGroupDetailGranteeSession,
		w5ManagementGroupDetailGranteeID,
		w5ManagementGroupDetailGranteeToken,
		sessionLastSeenAt,
	)
	seedW5ManagementGroupDetailConcurrency(t, ctx, rawRedis, now)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	service := managementgroups.NewServiceWithOptions(managementgroups.ServiceOptions{
		Store:              store,
		AccountConcurrency: accountConcurrency,
		Now:                func() time.Time { return now },
	})
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
			TrustProxy:           "false",
		},
		Logger:                         slog.New(slog.NewTextHandler(io.Discard, nil)),
		ManagementAPIAuthMiddleware:    httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementGroupDetailHandler:   httpapi.NewManagementGroupDetailHandler(service),
		ManagementMyGroupDetailHandler: httpapi.NewManagementMyGroupDetailHandler(service),
	})

	owned := requestW5ManagementGroupDetail(
		t,
		router,
		"/__aisys__/api/groups/"+w5ManagementGroupDetailOwnedID,
		w5ManagementGroupDetailAdminToken,
	)
	if owned.Result.ID != w5ManagementGroupDetailOwnedID ||
		owned.Result.SystemAccountID != w5ManagementGroupDetailOwnerID ||
		owned.Result.OwnerSystemAccountID != w5ManagementGroupDetailOwnerID ||
		owned.Result.AccessType != "owner" ||
		!slices.Equal(owned.Result.AccountIDs, []string{
			w5ManagementGroupDetailOwnedAccount1,
			w5ManagementGroupDetailOwnedAccount2,
		}) ||
		owned.Result.AccountStats.Total != 2 ||
		owned.Result.AccountStats.CurrentConcurrency != 3 {
		t.Fatalf("owner detail = %+v", owned.Result)
	}

	authorized := requestW5ManagementGroupDetail(
		t,
		router,
		"/__aisys__/api/my-groups/"+w5ManagementGroupDetailAuthorizedID,
		w5ManagementGroupDetailGranteeToken,
	)
	if authorized.Result.ID != w5ManagementGroupDetailAuthorizedID ||
		authorized.Result.SystemAccountID != "" ||
		authorized.Result.AccessType != "authorized" ||
		authorized.Result.GroupAuthorizationID != w5ManagementGroupDetailAuthID ||
		len(authorized.Result.AccountIDs) != 0 ||
		authorized.Result.AccountStats.Total != 4 ||
		authorized.Result.AccountStats.CurrentConcurrency != 17 {
		t.Fatalf("authorized detail = %+v", authorized.Result)
	}
	if _, exists := authorized.RawData["systemAccountId"]; exists {
		t.Fatalf("my-groups detail exposed systemAccountId: %s", authorized.Body)
	}
	if rawAccountIDs, exists := authorized.RawData["accountIds"]; !exists || string(rawAccountIDs) != "[]" {
		t.Fatalf("authorized accountIds = %s, body = %s", rawAccountIDs, authorized.Body)
	}
	assertW5ManagementGroupDetailExpiredSlotCleaned(t, ctx, rawRedis)

	invisible := serveW5ManagementGroupDetailRequest(
		router,
		"/__aisys__/api/my-groups/"+w5ManagementGroupDetailOwnedID,
		w5ManagementGroupDetailGranteeToken,
	)
	if invisible.Code != http.StatusNotFound {
		t.Fatalf("invisible detail status = %d, body = %s", invisible.Code, invisible.Body.String())
	}
	var invisibleBody map[string]string
	if err := json.NewDecoder(invisible.Body).Decode(&invisibleBody); err != nil {
		t.Fatalf("decode invisible detail response: %v", err)
	}
	if invisibleBody["message"] != "分组不存在" {
		t.Fatalf("invisible detail response = %+v", invisibleBody)
	}

	assertW2ManagementSessionLastSeenAt(
		t,
		ctx,
		db,
		w5ManagementGroupDetailAdminSession,
		sessionLastSeenAt,
	)
	assertW2ManagementSessionLastSeenAt(
		t,
		ctx,
		db,
		w5ManagementGroupDetailGranteeSession,
		sessionLastSeenAt,
	)
}

type w5ManagementGroupDetailResponse struct {
	Result  managementgroups.DetailResult
	RawData map[string]json.RawMessage
	Body    string
}

func requestW5ManagementGroupDetail(
	t *testing.T,
	router http.Handler,
	target string,
	sessionToken string,
) w5ManagementGroupDetailResponse {
	t.Helper()
	rec := serveW5ManagementGroupDetailRequest(router, target, sessionToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, body = %s", target, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("GET %s Cache-Control = %q, want no-store", target, got)
	}
	body := rec.Body.String()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode GET %s envelope: %v", target, err)
	}
	var result managementgroups.DetailResult
	if err := json.Unmarshal(envelope.Data, &result); err != nil {
		t.Fatalf("decode GET %s detail: %v", target, err)
	}
	var rawData map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Data, &rawData); err != nil {
		t.Fatalf("decode GET %s raw detail: %v", target, err)
	}
	return w5ManagementGroupDetailResponse{Result: result, RawData: rawData, Body: body}
}

func serveW5ManagementGroupDetailRequest(
	router http.Handler,
	target string,
	sessionToken string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func insertW5ManagementGroupDetailFixtures(
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
			($1, 'w5-group-detail-admin', 'W5 Group Detail Admin', NULL, 'admin', 'active', 'hash', false, false, $4, $4),
			($2, 'w5-group-detail-owner', 'W5 Group Detail Owner', NULL, 'user', 'active', 'hash', false, false, $4, $4),
			($3, 'w5-group-detail-grantee', 'W5 Group Detail Grantee', NULL, 'user', 'active', 'hash', false, false, $4, $4)
	`, w5ManagementGroupDetailAdminID, w5ManagementGroupDetailOwnerID, w5ManagementGroupDetailGranteeID, now); err != nil {
		t.Fatalf("insert W5 management group detail system accounts: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.groups (
			id, system_account_id, name, provider_code, description, enabled, is_default,
			group_type, scheduling_policy_json, created_at, updated_at
		) VALUES
			($1, $3, 'W5 Owned Detail', 'openai', 'owner detail', true, true, 'personal', NULL, $4, $4),
			($2, $3, 'W5 Authorized Detail', 'openai', 'authorized detail', true, false, 'personal', NULL, $4, $4)
	`, w5ManagementGroupDetailOwnedID, w5ManagementGroupDetailAuthorizedID, w5ManagementGroupDetailOwnerID, now); err != nil {
		t.Fatalf("insert W5 management group detail groups: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, credentials_encrypted, credential_mask, concurrency_limit, priority,
			client_compatibility, schedulable, health_check_model, health_check_endpoint_family, created_at, updated_at
		) VALUES
			($1, $5, 'openai', 'profile_openai_openai_v1', 'openai', 'v1', $1, 'api_key', 'active', 'v1:test:test:1', 'sk***1', 20, 0, 'openai_standard', true, 'gpt-5.6-sol', 'chat_completions', $6, $6),
			($2, $5, 'openai', 'profile_openai_openai_v1', 'openai', 'v1', $2, 'api_key', 'active', 'v1:test:test:2', 'sk***2', 20, 0, 'openai_standard', true, 'gpt-5.6-sol', 'chat_completions', $7, $7),
			($3, $5, 'openai', 'profile_openai_openai_v1', 'openai', 'v1', $3, 'api_key', 'active', 'v1:test:test:3', 'sk***3', 20, 0, 'openai_standard', true, 'gpt-5.6-sol', 'chat_completions', $8, $8),
			($4, $5, 'openai', 'profile_openai_openai_v1', 'openai', 'v1', $4, 'api_key', 'active', 'v1:test:test:4', 'sk***4', 20, 0, 'openai_standard', true, 'gpt-5.6-sol', 'chat_completions', $9, $9)
	`,
		w5ManagementGroupDetailOwnedAccount1,
		w5ManagementGroupDetailOwnedAccount2,
		w5ManagementGroupDetailAuthAccount1,
		w5ManagementGroupDetailAuthAccount2,
		w5ManagementGroupDetailOwnerID,
		now.Add(-4*time.Minute),
		now.Add(-3*time.Minute),
		now.Add(-2*time.Minute),
		now.Add(-time.Minute),
	); err != nil {
		t.Fatalf("insert W5 management group detail accounts: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.group_accounts (
			system_account_id, group_id, account_id, enabled, created_at, updated_at
		) VALUES
			($1, $2, $4, true, $8, $8),
			($1, $2, $5, true, $9, $9),
			($1, $3, $6, true, $10, $10),
			($1, $3, $7, true, $11, $11)
	`,
		w5ManagementGroupDetailOwnerID,
		w5ManagementGroupDetailOwnedID,
		w5ManagementGroupDetailAuthorizedID,
		w5ManagementGroupDetailOwnedAccount1,
		w5ManagementGroupDetailOwnedAccount2,
		w5ManagementGroupDetailAuthAccount1,
		w5ManagementGroupDetailAuthAccount2,
		now.Add(-4*time.Minute),
		now.Add(-3*time.Minute),
		now.Add(-2*time.Minute),
		now.Add(-time.Minute),
	); err != nil {
		t.Fatalf("insert W5 management group detail bindings: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.resource_authorizations (
			id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
			scope, status, effective_source_type, effective_source_team_id, activated_at, last_source_changed_at,
			remark, expires_at, limits_json, created_by, created_at, revoked_by, revoked_at,
			revoked_reason, updated_at
		) VALUES (
			$1, 'group', $2, $3, $4,
			'use', 'active', 'manual', NULL, $5, $5,
			NULL, $6, '{"daily":{"enabled":true,"limit":50}}', $3, $5, NULL, NULL,
			NULL, $5
		)
	`,
		w5ManagementGroupDetailAuthID,
		w5ManagementGroupDetailAuthorizedID,
		w5ManagementGroupDetailOwnerID,
		w5ManagementGroupDetailGranteeID,
		now.Add(-time.Hour),
		now.Add(time.Hour),
	); err != nil {
		t.Fatalf("insert W5 management group detail authorization: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.resource_authorization_sources (
			id, authorization_id, source_type, source_team_id, status,
			activated_at, ended_at, ended_reason, created_by, created_at,
			revoked_by, revoked_at, updated_at
		) VALUES (
			'source_w5_management_group_detail_manual', $1, 'manual', NULL, 'active',
			$2, NULL, NULL, $3, $2,
			NULL, NULL, $2
		)
	`, w5ManagementGroupDetailAuthID, now.Add(-time.Hour), w5ManagementGroupDetailOwnerID); err != nil {
		t.Fatalf("insert W5 management group detail authorization source: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_stats.group_account_stats (
			system_account_id, group_id, total, available, active, disabled, error,
			rate_limited, current_concurrency, concurrency_limit, updated_at
		) VALUES
			($1, $2, 99, 2, 2, 0, 0, 0, 99, 40, $4),
			($1, $3, 4, 3, 3, 1, 0, 0, 17, 40, $4)
	`, w5ManagementGroupDetailOwnerID, w5ManagementGroupDetailOwnedID, w5ManagementGroupDetailAuthorizedID, now.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert W5 management group detail stats: %v", err)
	}
}

func seedW5ManagementGroupDetailConcurrency(
	t *testing.T,
	ctx context.Context,
	client *redis.Client,
	now time.Time,
) {
	t.Helper()
	addSlots := func(accountID string, scores map[string]float64) {
		t.Helper()
		prefix := w5ManagementGroupDetailConcurrencyPrefix(accountID)
		members := make([]redis.Z, 0, len(scores))
		for member, score := range scores {
			members = append(members, redis.Z{Score: score, Member: member})
		}
		if err := client.ZAdd(ctx, prefix+"total", members...).Err(); err != nil {
			t.Fatalf("seed account %s total concurrency: %v", accountID, err)
		}
	}
	addSlots(w5ManagementGroupDetailOwnedAccount1, map[string]float64{
		"owned-live-1": float64(now.Add(time.Minute).UnixMilli()),
		"owned-live-2": float64(now.Add(2 * time.Minute).UnixMilli()),
	})
	addSlots(w5ManagementGroupDetailOwnedAccount2, map[string]float64{
		"owned-live-3": float64(now.Add(time.Minute).UnixMilli()),
	})

	prefix := w5ManagementGroupDetailConcurrencyPrefix(w5ManagementGroupDetailAuthAccount1)
	expiredScore := float64(now.Add(-time.Second).UnixMilli())
	liveScore := float64(now.Add(time.Minute).UnixMilli())
	totalMembers := []redis.Z{{Score: expiredScore, Member: "expired-slot"}}
	for index := 1; index <= 5; index++ {
		totalMembers = append(totalMembers, redis.Z{
			Score:  liveScore,
			Member: "live-slot-" + strconv.Itoa(index),
		})
	}
	if err := client.ZAdd(ctx, prefix+"total", totalMembers...).Err(); err != nil {
		t.Fatalf("seed authorized total concurrency: %v", err)
	}
	for _, lane := range []string{"text", "image"} {
		if err := client.ZAdd(ctx, prefix+lane,
			redis.Z{Score: expiredScore, Member: "expired-slot"},
			redis.Z{Score: liveScore, Member: "live-slot-1"},
		).Err(); err != nil {
			t.Fatalf("seed authorized %s concurrency: %v", lane, err)
		}
	}
	if err := client.HSet(ctx, prefix+"metadata",
		"expired-slot", `{"lane":"text"}`,
		"live-slot-1", `{"lane":"text"}`,
	).Err(); err != nil {
		t.Fatalf("seed authorized concurrency metadata: %v", err)
	}
}

func assertW5ManagementGroupDetailExpiredSlotCleaned(
	t *testing.T,
	ctx context.Context,
	client *redis.Client,
) {
	t.Helper()
	prefix := w5ManagementGroupDetailConcurrencyPrefix(w5ManagementGroupDetailAuthAccount1)
	for _, lane := range []string{"total", "text", "image"} {
		score, err := client.ZScore(ctx, prefix+lane, "expired-slot").Result()
		if err != redis.Nil {
			t.Fatalf("expired %s slot score = %v, error = %v; want redis.Nil", lane, score, err)
		}
	}
	if exists, err := client.HExists(ctx, prefix+"metadata", "expired-slot").Result(); err != nil || exists {
		t.Fatalf("expired metadata exists = %t, error = %v", exists, err)
	}
	if count, err := client.ZCard(ctx, prefix+"total").Result(); err != nil || count != 5 {
		t.Fatalf("authorized live total slots = %d, error = %v; want 5", count, err)
	}
	if exists, err := client.HExists(ctx, prefix+"metadata", "live-slot-1").Result(); err != nil || !exists {
		t.Fatalf("live metadata exists = %t, error = %v", exists, err)
	}
}

func w5ManagementGroupDetailConcurrencyPrefix(accountID string) string {
	return "juhe-ai:" + w5ManagementGroupDetailNamespace + ":account-concurrency-v2:" + accountID + ":"
}
