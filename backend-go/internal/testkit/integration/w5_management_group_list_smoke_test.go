//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
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
	w5ManagementGroupListNamespace = "w5-management-group-list"

	w5ManagementGroupListAdminID = "sys_w5_management_group_list_admin"
	w5ManagementGroupListUserID  = "sys_w5_management_group_list_user"
	w5ManagementGroupListOwnerID = "sys_w5_management_group_list_owner"

	w5ManagementGroupListAdminSession = "sess_w5_management_group_list_admin"
	w5ManagementGroupListUserSession  = "sess_w5_management_group_list_user"
	w5ManagementGroupListAdminToken   = "w5-management-group-list-admin-session"
	w5ManagementGroupListUserToken    = "w5-management-group-list-user-session"

	w5ManagementGroupListOwnedZID   = "grp_w5_management_group_list_owned_z"
	w5ManagementGroupListOwnedAID   = "grp_w5_management_group_list_owned_a"
	w5ManagementGroupListActiveID   = "grp_w5_management_group_list_active"
	w5ManagementGroupListPausedID   = "grp_w5_management_group_list_paused"
	w5ManagementGroupListExpiredID  = "grp_w5_management_group_list_expired"
	w5ManagementGroupListRevokedID  = "grp_w5_management_group_list_revoked"
	w5ManagementGroupListReturnedID = "grp_w5_management_group_list_returned"

	w5ManagementGroupListActiveAuthID   = "rauth_w5_management_group_list_active"
	w5ManagementGroupListPausedAuthID   = "rauth_w5_management_group_list_paused"
	w5ManagementGroupListExpiredAuthID  = "rauth_w5_management_group_list_expired"
	w5ManagementGroupListRevokedAuthID  = "rauth_w5_management_group_list_revoked"
	w5ManagementGroupListReturnedAuthID = "rauth_w5_management_group_list_returned"

	w5ManagementGroupListOwnedAccount1 = "acct_w5_management_group_list_owned_1"
	w5ManagementGroupListOwnedAccount2 = "acct_w5_management_group_list_owned_2"
)

func TestW5ManagementGroupListPostgresSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
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
		w5ManagementGroupListNamespace+":state",
	)
	if err != nil {
		t.Fatalf("open state redis: %v", err)
	}
	defer closeRedisClient(t, stateRedis)
	accountConcurrency, err := redisplatform.NewAccountConcurrencyReader(
		stateRedis,
		w5ManagementGroupListNamespace,
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

	now := time.Date(2026, 7, 10, 18, 0, 0, 0, time.UTC)
	fixtureTimes := insertW5ManagementGroupListFixtures(t, ctx, db, now)
	seedW5ManagementGroupListConcurrency(t, ctx, rawRedis, now)
	sessionLastSeenAt := now.Add(-20 * time.Minute)
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w5ManagementGroupListAdminSession,
		w5ManagementGroupListAdminID,
		w5ManagementGroupListAdminToken,
		sessionLastSeenAt,
	)
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w5ManagementGroupListUserSession,
		w5ManagementGroupListUserID,
		w5ManagementGroupListUserToken,
		sessionLastSeenAt,
	)

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
		Logger:                       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ManagementAPIAuthMiddleware:  httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementGroupListHandler:   httpapi.NewManagementGroupListHandler(service),
		ManagementMyGroupListHandler: httpapi.NewManagementMyGroupListHandler(service),
	})

	global := requestW5ManagementGroupList(
		t,
		router,
		"/__aisys__/api/groups?page=1&pageSize=50",
		w5ManagementGroupListAdminToken,
	)
	if len(global.Result.Items) != 7 ||
		global.Result.Page != 1 ||
		global.Result.PageSize != 50 ||
		global.Result.Total != 7 ||
		global.Result.HasMore {
		t.Fatalf("admin global list = %+v", global.Result)
	}
	for _, item := range global.Result.Items {
		if item.AccessType != "owner" ||
			item.SystemAccountID == "" ||
			item.OwnerSystemAccountID == "" ||
			item.GroupAuthorizationID != "" ||
			item.AuthorizationSourceSummary != nil {
			t.Fatalf("admin global row is not owner-only: %+v", item)
		}
	}
	assertW5ManagementGroupListRuntimeAndShape(t, global)

	explicitZeroPageSize := requestW5ManagementGroupList(
		t,
		router,
		"/__aisys__/api/groups?systemAccountId="+w5ManagementGroupListUserID+"&page=1&pageSize=0",
		w5ManagementGroupListAdminToken,
	)
	assertW5ManagementGroupListIDs(
		t,
		explicitZeroPageSize.Result.Items,
		[]string{w5ManagementGroupListActiveID},
	)
	if explicitZeroPageSize.Result.Page != 1 ||
		explicitZeroPageSize.Result.PageSize != 1 ||
		explicitZeroPageSize.Result.Total != 2 ||
		!explicitZeroPageSize.Result.HasMore {
		t.Fatalf("explicit zero pageSize response = %+v", explicitZeroPageSize.Result)
	}
	assertW5ManagementGroupListRuntimeAndShape(t, explicitZeroPageSize)

	var scopedItems []managementgroups.ListItem
	var scopedRawItems []map[string]json.RawMessage
	for _, page := range []struct {
		number      int
		wantIDs     []string
		wantTotal   int
		wantHasMore bool
	}{
		{
			number:      1,
			wantIDs:     []string{w5ManagementGroupListActiveID, w5ManagementGroupListOwnedZID},
			wantTotal:   3,
			wantHasMore: true,
		},
		{
			number:      2,
			wantIDs:     []string{w5ManagementGroupListOwnedAID, w5ManagementGroupListPausedID},
			wantTotal:   5,
			wantHasMore: true,
		},
		{
			number:      3,
			wantIDs:     []string{w5ManagementGroupListExpiredID},
			wantTotal:   5,
			wantHasMore: false,
		},
	} {
		response := requestW5ManagementGroupList(
			t,
			router,
			"/__aisys__/api/groups?systemAccountId="+w5ManagementGroupListUserID+
				"&page="+w5ManagementGroupListInteger(page.number)+"&pageSize=2",
			w5ManagementGroupListAdminToken,
		)
		if response.Result.Page != page.number ||
			response.Result.PageSize != 2 ||
			response.Result.Total != page.wantTotal ||
			response.Result.HasMore != page.wantHasMore {
			t.Fatalf("admin scoped page %d = %+v", page.number, response.Result)
		}
		assertW5ManagementGroupListIDs(t, response.Result.Items, page.wantIDs)
		assertW5ManagementGroupListRuntimeAndShape(t, response)
		scopedItems = append(scopedItems, response.Result.Items...)
		scopedRawItems = append(scopedRawItems, response.RawItems...)
	}
	assertW5ManagementGroupListIDs(t, scopedItems, []string{
		w5ManagementGroupListActiveID,
		w5ManagementGroupListOwnedZID,
		w5ManagementGroupListOwnedAID,
		w5ManagementGroupListPausedID,
		w5ManagementGroupListExpiredID,
	})
	if findW5ManagementGroupListItem(scopedItems, w5ManagementGroupListRevokedID) != nil ||
		findW5ManagementGroupListItem(scopedItems, w5ManagementGroupListReturnedID) != nil {
		t.Fatalf("scoped list exposed revoked or returned authorization: %+v", scopedItems)
	}
	assertW5ManagementGroupListRawItemsDoNotExposeDetails(t, scopedRawItems)

	active := requireW5ManagementGroupListItem(t, scopedItems, w5ManagementGroupListActiveID)
	if active.SystemAccountID != w5ManagementGroupListOwnerID ||
		active.SystemAccountName != "W5 Group List Owner" ||
		active.OwnerSystemAccountID != w5ManagementGroupListOwnerID ||
		active.OwnerSystemAccountName != "W5 Group List Owner" ||
		active.Name != "W5 Active Authorized" ||
		active.ProviderCode != "openai" ||
		active.Description == nil ||
		*active.Description != "owner description must remain" ||
		active.Enabled ||
		active.IsDefault ||
		active.GroupType != "high_concurrency" ||
		active.AccessType != "authorized" ||
		active.GroupAuthorizationID != w5ManagementGroupListActiveAuthID ||
		active.AuthorizationStatus != "active" ||
		active.AuthorizationExpiresAt == nil ||
		active.AuthorizationLimits.Daily == nil ||
		!active.AuthorizationLimits.Daily.Enabled ||
		active.AuthorizationLimits.Daily.Limit != 321 ||
		active.AccountCount != 0 {
		t.Fatalf("active authorized row = %+v", active)
	}
	if active.SchedulingPolicy == nil ||
		active.SchedulingPolicy.DefaultSoftConcurrency != 17 ||
		active.SchedulingPolicy.MaxQueueWaitMs != 70000 ||
		active.SchedulingPolicy.ClientIPConcurrencyLimit != 4 ||
		active.SchedulingPolicy.ClientIPConcurrencyOverflowMode != "queue" ||
		active.SchedulingPolicy.ImageLaneMaxConcurrency != 2 {
		t.Fatalf("active local scheduling override = %+v", active.SchedulingPolicy)
	}
	if active.AccountStats.Total != 9 ||
		active.AccountStats.Available != 8 ||
		active.AccountStats.Active != 7 ||
		active.AccountStats.Disabled != 1 ||
		active.AccountStats.Error != 1 ||
		active.AccountStats.RateLimited != 2 ||
		active.AccountStats.CurrentConcurrency != 4 ||
		active.AccountStats.ConcurrencyLimit != 10 ||
		active.AccountStats.Usage.RequestCount != 7 ||
		active.AccountStats.Usage.TotalTokens != 84 ||
		active.AccountStats.TodayUsage.RequestCount != 4 ||
		active.AccountStats.TodayUsage.TotalTokens != 48 {
		t.Fatalf("active preaggregated stats = %+v", active.AccountStats)
	}
	assertW5ManagementGroupListUsageLastUsedAt(
		t,
		active.AccountStats.Usage.LastUsedAt,
		fixtureTimes.ActiveTotalLastUsedAt,
		"active total",
	)
	assertW5ManagementGroupListUsageLastUsedAt(
		t,
		active.AccountStats.TodayUsage.LastUsedAt,
		fixtureTimes.ActiveTodayLastUsedAt,
		"active today",
	)
	if active.Permissions.CanBindToAPIKey ||
		!active.Permissions.CanReturnAuthorization ||
		active.Permissions.CanDelete ||
		active.Permissions.CanAuthorize ||
		active.Permissions.CanManageAccounts {
		t.Fatalf("active authorized permissions = %+v", active.Permissions)
	}
	if active.AuthorizationSourceSummary == nil ||
		active.AuthorizationSourceSummary.ActiveSourceCount != 3 ||
		!active.AuthorizationSourceSummary.HasManual ||
		!active.AuthorizationSourceSummary.HasTeam ||
		!reflect.DeepEqual(active.AuthorizationSourceSummary.TeamNames, []string{"W5 Alpha Team", "W5 Beta Team"}) {
		t.Fatalf("active authorization source summary = %+v", active.AuthorizationSourceSummary)
	}

	owned := requireW5ManagementGroupListItem(t, scopedItems, w5ManagementGroupListOwnedZID)
	if owned.AccessType != "owner" ||
		owned.SystemAccountID != w5ManagementGroupListUserID ||
		owned.OwnerSystemAccountID != w5ManagementGroupListUserID ||
		owned.AccountCount != 4 ||
		owned.AccountStats.Total != 4 ||
		owned.AccountStats.CurrentConcurrency != 3 ||
		owned.AccountStats.CurrentConcurrencyAvailable == nil ||
		!*owned.AccountStats.CurrentConcurrencyAvailable ||
		owned.AccountStats.Usage.RequestCount != 10 ||
		owned.AccountStats.Usage.TotalTokens != 150 ||
		owned.AccountStats.TodayUsage.RequestCount != 2 ||
		owned.AccountStats.TodayUsage.TotalTokens != 25 ||
		owned.AuthorizationSourceSummary != nil {
		t.Fatalf("owned preaggregated row = %+v", owned)
	}
	assertW5ManagementGroupListUsageLastUsedAt(
		t,
		owned.AccountStats.Usage.LastUsedAt,
		fixtureTimes.OwnerTotalLastUsedAt,
		"owner total",
	)
	assertW5ManagementGroupListUsageLastUsedAt(
		t,
		owned.AccountStats.TodayUsage.LastUsedAt,
		fixtureTimes.OwnerTodayLastUsedAt,
		"owner today",
	)

	paused := requireW5ManagementGroupListItem(t, scopedItems, w5ManagementGroupListPausedID)
	if paused.AuthorizationStatus != "paused" ||
		paused.AccountCount != 0 ||
		paused.AuthorizationSourceSummary == nil ||
		paused.AuthorizationSourceSummary.ActiveSourceCount != 0 ||
		paused.AuthorizationSourceSummary.HasManual ||
		!paused.AuthorizationSourceSummary.HasTeam ||
		len(paused.AuthorizationSourceSummary.TeamNames) != 0 {
		t.Fatalf("paused authorized row = %+v", paused)
	}
	expired := requireW5ManagementGroupListItem(t, scopedItems, w5ManagementGroupListExpiredID)
	if expired.AuthorizationStatus != "expired" ||
		expired.AccountCount != 0 ||
		expired.AuthorizationSourceSummary == nil ||
		expired.AuthorizationSourceSummary.ActiveSourceCount != 0 ||
		expired.AuthorizationSourceSummary.HasManual ||
		expired.AuthorizationSourceSummary.HasTeam ||
		len(expired.AuthorizationSourceSummary.TeamNames) != 0 {
		t.Fatalf("expired authorized row = %+v", expired)
	}

	failureService := managementgroups.NewServiceWithOptions(managementgroups.ServiceOptions{
		Store: store,
		AccountConcurrency: w5ManagementGroupListConcurrencyReaderStub{
			err: errors.New("redis unavailable"),
		},
		Now: func() time.Time { return now },
	})
	failureRouter := httpapi.NewRouter(httpapi.RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
			TrustProxy:           "false",
		},
		Logger:                       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ManagementAPIAuthMiddleware:  httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementGroupListHandler:   httpapi.NewManagementGroupListHandler(failureService),
		ManagementMyGroupListHandler: httpapi.NewManagementMyGroupListHandler(failureService),
	})
	failure := requestW5ManagementGroupList(
		t,
		failureRouter,
		"/__aisys__/api/groups?systemAccountId="+w5ManagementGroupListUserID+"&page=1&pageSize=2",
		w5ManagementGroupListAdminToken,
	)
	if failure.Result.RuntimeSnapshot.AccountConcurrencyAvailable {
		t.Fatalf("failure runtime snapshot = %+v", failure.Result.RuntimeSnapshot)
	}
	failureOwned := requireW5ManagementGroupListItem(
		t,
		failure.Result.Items,
		w5ManagementGroupListOwnedZID,
	)
	if failureOwned.AccountStats.CurrentConcurrency != 31 ||
		failureOwned.AccountStats.CurrentConcurrencyAvailable == nil ||
		*failureOwned.AccountStats.CurrentConcurrencyAvailable {
		t.Fatalf("failure owner stats = %+v", failureOwned.AccountStats)
	}
	failureAuthorized := requireW5ManagementGroupListItem(
		t,
		failure.Result.Items,
		w5ManagementGroupListActiveID,
	)
	if failureAuthorized.AccountStats.CurrentConcurrency != 4 ||
		failureAuthorized.AccountStats.CurrentConcurrencyAvailable != nil {
		t.Fatalf("failure authorized stats = %+v", failureAuthorized.AccountStats)
	}
	assertW5ManagementGroupListRawItemsDoNotExposeDetails(t, failure.RawItems)

	self := requestW5ManagementGroupList(
		t,
		router,
		"/__aisys__/api/my-groups?systemAccountId="+w5ManagementGroupListOwnerID+"&page=1&pageSize=50",
		w5ManagementGroupListUserToken,
	)
	assertW5ManagementGroupListIDs(t, self.Result.Items, []string{
		w5ManagementGroupListActiveID,
		w5ManagementGroupListOwnedZID,
		w5ManagementGroupListOwnedAID,
		w5ManagementGroupListPausedID,
		w5ManagementGroupListExpiredID,
	})
	if self.Result.Total != 5 || self.Result.HasMore || self.Result.Page != 1 || self.Result.PageSize != 50 {
		t.Fatalf("self list pagination = %+v", self.Result)
	}
	for index, item := range self.Result.Items {
		if item.SystemAccountID != "" || item.SystemAccountName != "" {
			t.Fatalf("self row leaked admin owner fields: %+v", item)
		}
		if _, exists := self.RawItems[index]["systemAccountId"]; exists {
			t.Fatalf("self row exposed systemAccountId: %s", self.Body)
		}
		if _, exists := self.RawItems[index]["systemAccountName"]; exists {
			t.Fatalf("self row exposed systemAccountName: %s", self.Body)
		}
	}
	assertW5ManagementGroupListRuntimeAndShape(t, self)

	forbidden := serveW5ManagementGroupListRequest(
		router,
		"/__aisys__/api/groups",
		w5ManagementGroupListUserToken,
	)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("ordinary user global status = %d, body = %s", forbidden.Code, forbidden.Body.String())
	}
	var forbiddenBody map[string]string
	if err := json.NewDecoder(forbidden.Body).Decode(&forbiddenBody); err != nil {
		t.Fatalf("decode ordinary user global response: %v", err)
	}
	if forbiddenBody["message"] != "需要管理员权限" {
		t.Fatalf("ordinary user global response = %+v", forbiddenBody)
	}

	assertW2ManagementSessionLastSeenAt(
		t,
		ctx,
		db,
		w5ManagementGroupListAdminSession,
		sessionLastSeenAt,
	)
	assertW2ManagementSessionLastSeenAt(
		t,
		ctx,
		db,
		w5ManagementGroupListUserSession,
		sessionLastSeenAt,
	)
}

type w5ManagementGroupListResponse struct {
	Result   managementgroups.ListResult
	RawItems []map[string]json.RawMessage
	Body     string
}

func requestW5ManagementGroupList(
	t *testing.T,
	router http.Handler,
	target string,
	sessionToken string,
) w5ManagementGroupListResponse {
	t.Helper()
	rec := serveW5ManagementGroupListRequest(router, target, sessionToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, body = %s", target, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("GET %s Cache-Control = %q, want no-store", target, got)
	}
	body := rec.Body.String()
	var envelope map[string]json.RawMessage
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&envelope); err != nil {
		t.Fatalf("decode GET %s response: %v", target, err)
	}
	if len(envelope) != 1 {
		t.Fatalf("GET %s response keys = %v, want only data", target, envelope)
	}
	rawData, ok := envelope["data"]
	if !ok {
		t.Fatalf("GET %s response missing data: %s", target, body)
	}
	var result managementgroups.ListResult
	if err := json.Unmarshal(rawData, &result); err != nil {
		t.Fatalf("decode GET %s result: %v", target, err)
	}
	var shape struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rawData, &shape); err != nil {
		t.Fatalf("decode GET %s raw items: %v", target, err)
	}
	return w5ManagementGroupListResponse{
		Result:   result,
		RawItems: shape.Items,
		Body:     body,
	}
}

func serveW5ManagementGroupListRequest(
	router http.Handler,
	target string,
	sessionToken string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Cookie", managementauth.SessionCookieName+"="+sessionToken)
	req.Header.Set("User-Agent", "w5-management-group-list-smoke")
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func assertW5ManagementGroupListRuntimeAndShape(
	t *testing.T,
	response w5ManagementGroupListResponse,
) {
	t.Helper()
	if !response.Result.RuntimeSnapshot.AccountConcurrencyAvailable {
		t.Fatalf("runtime snapshot = %+v", response.Result.RuntimeSnapshot)
	}
	assertW5ManagementGroupListRawItemsDoNotExposeDetails(t, response.RawItems)
	assertW5ManagementGroupListAuthorizationLimitsShape(t, response.Result.Items, response.RawItems)
	assertW5ManagementGroupListConcurrencyShape(t, response.Result.Items, response.RawItems)
}

func assertW5ManagementGroupListConcurrencyShape(
	t *testing.T,
	items []managementgroups.ListItem,
	rawItems []map[string]json.RawMessage,
) {
	t.Helper()
	for index, item := range items {
		rawStats, exists := rawItems[index]["accountStats"]
		if !exists {
			t.Fatalf("management group %s omitted accountStats", item.ID)
		}
		var stats map[string]json.RawMessage
		if err := json.Unmarshal(rawStats, &stats); err != nil {
			t.Fatalf("decode management group %s accountStats: %v", item.ID, err)
		}
		rawAvailability, exposed := stats["currentConcurrencyAvailable"]
		if item.AccessType == "authorized" {
			if item.AccountStats.CurrentConcurrencyAvailable != nil || exposed {
				t.Fatalf("authorized group %s exposed concurrency availability: %s", item.ID, rawStats)
			}
			continue
		}
		if item.AccountStats.CurrentConcurrencyAvailable == nil ||
			!*item.AccountStats.CurrentConcurrencyAvailable ||
			!exposed ||
			string(rawAvailability) != "true" {
			t.Fatalf("owner group %s concurrency availability = %s / %+v", item.ID, rawAvailability, item.AccountStats)
		}
	}
}

func assertW5ManagementGroupListRawItemsDoNotExposeDetails(
	t *testing.T,
	items []map[string]json.RawMessage,
) {
	t.Helper()
	for _, item := range items {
		if _, exists := item["accountIds"]; exists {
			t.Fatalf("management group list exposed accountIds: %+v", item)
		}
		if _, exists := item["authorizationSources"]; exists {
			t.Fatalf("management group list exposed authorizationSources: %+v", item)
		}
	}
}

func assertW5ManagementGroupListAuthorizationLimitsShape(
	t *testing.T,
	items []managementgroups.ListItem,
	rawItems []map[string]json.RawMessage,
) {
	t.Helper()
	if len(items) != len(rawItems) {
		t.Fatalf("typed items=%d raw items=%d", len(items), len(rawItems))
	}
	for index, item := range items {
		raw, exists := rawItems[index]["authorizationLimits"]
		if !exists {
			t.Fatalf("management group %s omitted authorizationLimits", item.ID)
		}
		var limits map[string]json.RawMessage
		if err := json.Unmarshal(raw, &limits); err != nil || limits == nil {
			t.Fatalf("management group %s authorizationLimits = %s, want object", item.ID, raw)
		}
		if reflect.ValueOf(item.AuthorizationLimits).IsZero() && len(limits) != 0 {
			t.Fatalf("management group %s empty authorizationLimits = %s, want {}", item.ID, raw)
		}
	}
}

func assertW5ManagementGroupListIDs(
	t *testing.T,
	items []managementgroups.ListItem,
	want []string,
) {
	t.Helper()
	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, item.ID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("management group ids = %#v, want %#v", got, want)
	}
}

func findW5ManagementGroupListItem(
	items []managementgroups.ListItem,
	id string,
) *managementgroups.ListItem {
	for index := range items {
		if items[index].ID == id {
			return &items[index]
		}
	}
	return nil
}

func requireW5ManagementGroupListItem(
	t *testing.T,
	items []managementgroups.ListItem,
	id string,
) managementgroups.ListItem {
	t.Helper()
	item := findW5ManagementGroupListItem(items, id)
	if item == nil {
		t.Fatalf("management group %s missing from %+v", id, items)
	}
	return *item
}

func assertW5ManagementGroupListUsageLastUsedAt(
	t *testing.T,
	got *time.Time,
	want time.Time,
	label string,
) {
	t.Helper()
	if got == nil || !got.UTC().Equal(want.UTC()) {
		t.Fatalf("%s lastUsedAt = %v, want %s", label, got, want.UTC().Format(time.RFC3339Nano))
	}
}

func w5ManagementGroupListInteger(value int) string {
	return strconv.Itoa(value)
}

type w5ManagementGroupListFixtureTimes struct {
	OwnerTotalLastUsedAt  time.Time
	OwnerTodayLastUsedAt  time.Time
	ActiveTotalLastUsedAt time.Time
	ActiveTodayLastUsedAt time.Time
}

func insertW5ManagementGroupListFixtures(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
) w5ManagementGroupListFixtureTimes {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.system_settings
		SET value_json = '"Asia/Shanghai"', updated_at = $1
		WHERE system_account_id = 'sys_admin'
		  AND key = 'usageStatsTimezone'
	`, now); err != nil {
		t.Fatalf("set W5 management group usage timezone: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES
			(
				$1, 'w5-management-group-list-admin', 'W5 Group List Admin', NULL, 'admin', 'active', 'hash',
				false, false, $4, $4
			),
			(
				$2, 'w5-management-group-list-user', 'W5 Group List User', NULL, 'user', 'active', 'hash',
				false, false, $4, $4
			),
			(
				$3, 'w5-management-group-list-owner', 'W5 Group List Owner', NULL, 'user', 'active', 'hash',
				false, false, $4, $4
			)
	`, w5ManagementGroupListAdminID, w5ManagementGroupListUserID, w5ManagementGroupListOwnerID, now); err != nil {
		t.Fatalf("insert W5 management group list accounts: %v", err)
	}

	type groupFixture struct {
		id              string
		systemAccountID string
		name            string
		description     *string
		enabled         bool
		isDefault       bool
		updatedAt       time.Time
	}
	description := "owner description must remain"
	groups := []groupFixture{
		{
			id:              w5ManagementGroupListOwnedZID,
			systemAccountID: w5ManagementGroupListUserID,
			name:            "W5 Owned Z",
			enabled:         true,
			isDefault:       true,
			updatedAt:       now.Add(-2 * time.Minute),
		},
		{
			id:              w5ManagementGroupListOwnedAID,
			systemAccountID: w5ManagementGroupListUserID,
			name:            "W5 Owned A",
			enabled:         true,
			updatedAt:       now.Add(-2 * time.Minute),
		},
		{
			id:              w5ManagementGroupListActiveID,
			systemAccountID: w5ManagementGroupListOwnerID,
			name:            "W5 Active Authorized",
			description:     &description,
			enabled:         true,
			isDefault:       true,
			updatedAt:       now.Add(-10 * time.Minute),
		},
		{
			id:              w5ManagementGroupListPausedID,
			systemAccountID: w5ManagementGroupListOwnerID,
			name:            "W5 Paused Authorized",
			enabled:         true,
			updatedAt:       now.Add(-3 * time.Minute),
		},
		{
			id:              w5ManagementGroupListExpiredID,
			systemAccountID: w5ManagementGroupListOwnerID,
			name:            "W5 Expired Authorized",
			enabled:         true,
			updatedAt:       now.Add(-4 * time.Minute),
		},
		{
			id:              w5ManagementGroupListRevokedID,
			systemAccountID: w5ManagementGroupListOwnerID,
			name:            "W5 Revoked Authorized",
			enabled:         true,
			updatedAt:       now.Add(-5 * time.Minute),
		},
		{
			id:              w5ManagementGroupListReturnedID,
			systemAccountID: w5ManagementGroupListOwnerID,
			name:            "W5 Returned Authorized",
			enabled:         true,
			updatedAt:       now.Add(-6 * time.Minute),
		},
	}
	for _, group := range groups {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.groups (
				id, system_account_id, name, provider_code, description, enabled, is_default,
				group_type, scheduling_policy_json, created_at, updated_at
			) VALUES (
				$1, $2, $3, 'openai', $4, $5, $6,
				'personal', NULL, $7, $8
			)
		`,
			group.id,
			group.systemAccountID,
			group.name,
			group.description,
			group.enabled,
			group.isDefault,
			now.Add(-24*time.Hour),
			group.updatedAt,
		); err != nil {
			t.Fatalf("insert W5 management group list group %s: %v", group.id, err)
		}
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, credentials_encrypted, credential_mask, concurrency_limit, priority,
			client_compatibility, schedulable, health_check_model, health_check_endpoint_mode, created_at, updated_at
		) VALUES
			($1, $3, 'openai', 'profile_openai_openai_v1', 'openai', 'v1', $1, 'api_key', 'active',
			'v1:test:test:1', 'sk***1', 20, 0, 'openai_standard', true, 'gpt-5.6-sol', 'chat_json', $4, $4),
			($2, $3, 'openai', 'profile_openai_openai_v1', 'openai', 'v1', $2, 'api_key', 'active',
			'v1:test:test:2', 'sk***2', 20, 0, 'openai_standard', true, 'gpt-5.6-sol', 'chat_json', $5, $5)
	`,
		w5ManagementGroupListOwnedAccount1,
		w5ManagementGroupListOwnedAccount2,
		w5ManagementGroupListUserID,
		now.Add(-4*time.Minute),
		now.Add(-3*time.Minute),
	); err != nil {
		t.Fatalf("insert W5 management group list accounts: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.group_accounts (
			system_account_id, group_id, account_id, enabled, created_at, updated_at
		) VALUES
			($1, $2, $3, true, $5, $5),
			($1, $2, $4, true, $6, $6)
	`,
		w5ManagementGroupListUserID,
		w5ManagementGroupListOwnedZID,
		w5ManagementGroupListOwnedAccount1,
		w5ManagementGroupListOwnedAccount2,
		now.Add(-4*time.Minute),
		now.Add(-3*time.Minute),
	); err != nil {
		t.Fatalf("insert W5 management group list bindings: %v", err)
	}

	type authorizationFixture struct {
		id        string
		groupID   string
		status    string
		expiresAt *time.Time
		limits    *string
		updatedAt time.Time
	}
	activeExpiresAt := now.Add(24 * time.Hour)
	pausedExpiresAt := now.Add(12 * time.Hour)
	expiredAt := now.Add(-30 * time.Minute)
	limits := `{"daily":{"enabled":true,"limit":321}}`
	authorizations := []authorizationFixture{
		{
			id:        w5ManagementGroupListActiveAuthID,
			groupID:   w5ManagementGroupListActiveID,
			status:    "active",
			expiresAt: &activeExpiresAt,
			limits:    &limits,
			updatedAt: now.Add(-8 * time.Minute),
		},
		{
			id:        w5ManagementGroupListPausedAuthID,
			groupID:   w5ManagementGroupListPausedID,
			status:    "paused",
			expiresAt: &pausedExpiresAt,
			updatedAt: now.Add(-7 * time.Minute),
		},
		{
			id:        w5ManagementGroupListExpiredAuthID,
			groupID:   w5ManagementGroupListExpiredID,
			status:    "expired",
			expiresAt: &expiredAt,
			updatedAt: now.Add(-6 * time.Minute),
		},
		{
			id:        w5ManagementGroupListRevokedAuthID,
			groupID:   w5ManagementGroupListRevokedID,
			status:    "revoked",
			updatedAt: now.Add(-5 * time.Minute),
		},
		{
			id:        w5ManagementGroupListReturnedAuthID,
			groupID:   w5ManagementGroupListReturnedID,
			status:    "returned",
			updatedAt: now.Add(-4 * time.Minute),
		},
	}
	for _, authorization := range authorizations {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.resource_authorizations (
				id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
				scope, status, effective_source_type, effective_source_team_id, activated_at, last_source_changed_at,
				remark, expires_at, limits_json, created_by, created_at, revoked_by, revoked_at,
				revoked_reason, updated_at
			) VALUES (
				$1, 'group', $2, $3, $4,
				'use', $5, NULL, NULL, $6, $6,
				NULL, $7, $8, $3, $6, NULL, NULL,
				NULL, $9
			)
		`,
			authorization.id,
			authorization.groupID,
			w5ManagementGroupListOwnerID,
			w5ManagementGroupListUserID,
			authorization.status,
			now.Add(-12*time.Hour),
			authorization.expiresAt,
			authorization.limits,
			authorization.updatedAt,
		); err != nil {
			t.Fatalf("insert W5 management group authorization %s: %v", authorization.id, err)
		}
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.group_authorization_settings (
			authorization_id, system_account_id, group_id, enabled, group_type,
			scheduling_policy_json, created_at, updated_at
		) VALUES (
			$1, $2, $3, false, 'high_concurrency', $4, $5, $6
		)
	`,
		w5ManagementGroupListActiveAuthID,
		w5ManagementGroupListUserID,
		w5ManagementGroupListActiveID,
		w5ManagementGroupListPolicyJSON(),
		now.Add(-11*time.Hour),
		now.Add(-time.Minute),
	); err != nil {
		t.Fatalf("insert W5 management group local settings: %v", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_teams (
			id, name, description, status, created_by, created_at, updated_at
		) VALUES
			('team_w5_management_group_list_alpha', 'W5 Alpha Team', NULL, 'active', $1, $2, $2),
			('team_w5_management_group_list_beta', 'W5 Beta Team', NULL, 'active', $1, $2, $2),
			('team_w5_management_group_list_history', 'W5 Historical Team', NULL, 'active', $1, $2, $2)
	`, w5ManagementGroupListOwnerID, now.Add(-13*time.Hour)); err != nil {
		t.Fatalf("insert W5 management group source teams: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.resource_authorization_sources (
			id, authorization_id, source_type, source_team_id, status,
			activated_at, ended_at, ended_reason, created_by, created_at,
			revoked_by, revoked_at, updated_at
		) VALUES
			(
				'source_w5_management_group_list_manual', $1, 'manual', NULL, 'active',
				$3, NULL, NULL, $4, $3,
				NULL, NULL, $3
			),
			(
				'source_w5_management_group_list_alpha', $1, 'team', 'team_w5_management_group_list_alpha', 'active',
				$5, NULL, NULL, $4, $5,
				NULL, NULL, $5
			),
			(
				'source_w5_management_group_list_beta', $1, 'team', 'team_w5_management_group_list_beta', 'active',
				$6, NULL, NULL, $4, $6,
				NULL, NULL, $6
			),
			(
				'source_w5_management_group_list_history', $1, 'team', 'team_w5_management_group_list_history', 'revoked',
				$7, $8, 'history', $4, $7,
				$4, $8, $8
			),
			(
				'source_w5_management_group_list_paused_history', $2, 'team', 'team_w5_management_group_list_history', 'revoked',
				$7, $8, 'history', $4, $7,
				$4, $8, $8
			)
	`,
		w5ManagementGroupListActiveAuthID,
		w5ManagementGroupListPausedAuthID,
		now.Add(-10*time.Hour),
		w5ManagementGroupListOwnerID,
		now.Add(-9*time.Hour),
		now.Add(-8*time.Hour),
		now.Add(-7*time.Hour),
		now.Add(-6*time.Hour),
	); err != nil {
		t.Fatalf("insert W5 management group authorization sources: %v", err)
	}

	updatedAtText := now.UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_stats.group_account_stats (
			system_account_id, group_id, total, available, active, disabled, error,
			rate_limited, current_concurrency, concurrency_limit, updated_at
		) VALUES
			($1, $2, 4, 3, 2, 1, 1, 2, 31, 8, $6),
			($1, $3, 1, 1, 1, 0, 0, 0, 0, 2, $6),
			($4, $5, 9, 8, 7, 1, 1, 2, 4, 10, $6),
			($1, $5, 99, 99, 99, 0, 0, 0, 99, 99, $6)
	`,
		w5ManagementGroupListUserID,
		w5ManagementGroupListOwnedZID,
		w5ManagementGroupListOwnedAID,
		w5ManagementGroupListOwnerID,
		w5ManagementGroupListActiveID,
		updatedAtText,
	); err != nil {
		t.Fatalf("insert W5 management group account stats: %v", err)
	}

	times := w5ManagementGroupListFixtureTimes{
		OwnerTotalLastUsedAt:  now.Add(-105 * time.Minute).Add(123 * time.Microsecond),
		OwnerTodayLastUsedAt:  now.Add(-95 * time.Minute).Add(456 * time.Microsecond),
		ActiveTotalLastUsedAt: now.Add(-85 * time.Minute).Add(789 * time.Microsecond),
		ActiveTodayLastUsedAt: now.Add(-75 * time.Minute).Add(321 * time.Microsecond),
	}
	insertW5ManagementGroupListUsageTotals(t, ctx, db, now, times)
	insertW5ManagementGroupListUsageDaily(t, ctx, db, now, times)
	return times
}

func seedW5ManagementGroupListConcurrency(
	t *testing.T,
	ctx context.Context,
	client *redis.Client,
	now time.Time,
) {
	t.Helper()
	addSlots := func(accountID string, members ...string) {
		t.Helper()
		values := make([]redis.Z, 0, len(members))
		for _, member := range members {
			values = append(values, redis.Z{
				Score:  float64(now.Add(time.Minute).UnixMilli()),
				Member: member,
			})
		}
		if err := client.ZAdd(
			ctx,
			w5ManagementGroupListConcurrencyPrefix(accountID)+"total",
			values...,
		).Err(); err != nil {
			t.Fatalf("seed account %s concurrency: %v", accountID, err)
		}
	}
	addSlots(w5ManagementGroupListOwnedAccount1, "owned-live-1", "owned-live-2")
	addSlots(w5ManagementGroupListOwnedAccount2, "owned-live-3")
}

func w5ManagementGroupListConcurrencyPrefix(accountID string) string {
	return "juhe-ai:" + w5ManagementGroupListNamespace + ":account-concurrency-v2:" + accountID + ":"
}

type w5ManagementGroupListConcurrencyReaderStub struct {
	err error
}

func (s w5ManagementGroupListConcurrencyReaderStub) LoadAccountCurrentConcurrencyByIDs(
	context.Context,
	[]string,
	time.Time,
) (map[string]int, error) {
	return nil, s.err
}

func insertW5ManagementGroupListUsageTotals(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
	times w5ManagementGroupListFixtureTimes,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_stats.usage_stats_totals (
			system_account_id, scope_type, scope_id, request_count,
			input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
			cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
			thinking_tokens, input_image_tokens, output_image_tokens,
			total_cost_usd, last_used_at, updated_at
		) VALUES
			($1, 'group', $2, 10, 120, 30, 11, 0.11, 12, 3, 0.12, 13, 14, 15, 1.5, $6, $10),
			($3, 'group_authorization', $4, 7, 70, 14, 21, 0.21, 22, 4, 0.22, 23, 24, 25, 2.5, $7, $10),
			($3, 'group', $5, 999, 9990, 999, 0, 0, 0, 0, 0, 0, 0, 0, 99, $8, $10),
			($1, 'group_authorization', $4, 888, 8880, 888, 0, 0, 0, 0, 0, 0, 0, 0, 88, $9, $10)
	`,
		w5ManagementGroupListUserID,
		w5ManagementGroupListOwnedZID,
		w5ManagementGroupListOwnerID,
		w5ManagementGroupListActiveAuthID,
		w5ManagementGroupListActiveID,
		times.OwnerTotalLastUsedAt.UTC().Format(time.RFC3339Nano),
		times.ActiveTotalLastUsedAt.UTC().Format(time.RFC3339Nano),
		now.Add(-65*time.Minute).UTC().Format(time.RFC3339Nano),
		now.Add(-55*time.Minute).UTC().Format(time.RFC3339Nano),
		now.UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatalf("insert W5 management group usage totals: %v", err)
	}
}

func insertW5ManagementGroupListUsageDaily(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
	times w5ManagementGroupListFixtureTimes,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_stats.usage_stats_daily (
			system_account_id, scope_type, scope_id, stat_date, request_count,
			input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
			cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
			thinking_tokens, input_image_tokens, output_image_tokens,
			total_cost_usd, last_used_at, updated_at
		) VALUES
			($1, 'group', $2, '2026-07-11', 2, 20, 5, 1, 0.01, 2, 1, 0.02, 3, 4, 5, 0.5, $6, $10),
			($3, 'group_authorization', $4, '2026-07-11', 4, 40, 8, 6, 0.06, 7, 2, 0.07, 8, 9, 10, 0.8, $7, $10),
			($1, 'group', $2, '2026-07-10', 555, 5550, 555, 0, 0, 0, 0, 0, 0, 0, 0, 55, $8, $10),
			($3, 'group_authorization', $4, '2026-07-10', 444, 4440, 444, 0, 0, 0, 0, 0, 0, 0, 0, 44, $9, $10),
			($3, 'group', $5, '2026-07-11', 777, 7770, 777, 0, 0, 0, 0, 0, 0, 0, 0, 77, $8, $10),
			($1, 'group_authorization', $4, '2026-07-11', 666, 6660, 666, 0, 0, 0, 0, 0, 0, 0, 0, 66, $9, $10)
	`,
		w5ManagementGroupListUserID,
		w5ManagementGroupListOwnedZID,
		w5ManagementGroupListOwnerID,
		w5ManagementGroupListActiveAuthID,
		w5ManagementGroupListActiveID,
		times.OwnerTodayLastUsedAt.UTC().Format(time.RFC3339Nano),
		times.ActiveTodayLastUsedAt.UTC().Format(time.RFC3339Nano),
		now.Add(-45*time.Minute).UTC().Format(time.RFC3339Nano),
		now.Add(-35*time.Minute).UTC().Format(time.RFC3339Nano),
		now.UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatalf("insert W5 management group daily usage: %v", err)
	}
}

func w5ManagementGroupListPolicyJSON() string {
	return `{
		"mode":"balanced_fast",
		"defaultSoftConcurrency":17,
		"fastFirstEnabled":true,
		"fallbackOnQueueEnabled":true,
		"breakAffinityOnSoftLimit":true,
		"breakAffinityOnQueueWaitMs":0,
		"slowRequestThresholdMs":30000,
		"firstOutputSlowThresholdMs":15000,
		"recentTimeoutWindowSeconds":120,
		"recentTimeoutPenaltyThreshold":2,
		"maxQueueWaitMs":70000,
		"maxQueueSize":1000,
		"perApiKeyQueueLimit":1000,
		"clientIpConcurrencyLimit":4,
		"clientIpConcurrencyOverflowMode":"queue",
		"imageLaneMaxConcurrency":2
	}`
}
