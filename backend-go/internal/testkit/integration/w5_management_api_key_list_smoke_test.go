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
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"juhe-ai/backend-go/internal/app"
	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/managementapikeys"
	"juhe-ai/backend-go/internal/modules/managementauth"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w5ManagementAPIKeyListAdminID  = "sys_w5_management_api_key_list_admin"
	w5ManagementAPIKeyListOwnerID  = "sys_w5_management_api_key_list_owner"
	w5ManagementAPIKeyListOtherID  = "sys_w5_management_api_key_list_other"
	w5ManagementAPIKeyListAdminSID = "sess_w5_management_api_key_list_admin"
	w5ManagementAPIKeyListOwnerSID = "sess_w5_management_api_key_list_owner"

	w5ManagementAPIKeyListAdminToken = "w5-management-api-key-list-admin-session"
	w5ManagementAPIKeyListOwnerToken = "w5-management-api-key-list-owner-session"

	w5ManagementAPIKeyListOwnerPrimaryRouteID   = "route_w5_management_api_key_list_owner_primary"
	w5ManagementAPIKeyListOwnerSecondaryRouteID = "route_w5_management_api_key_list_owner_secondary"
	w5ManagementAPIKeyListOtherRouteID          = "route_w5_management_api_key_list_other"

	w5ManagementAPIKeyListOtherDefaultID = "key_w5_management_api_key_list_other_default"
	w5ManagementAPIKeyListOwnerDefaultID = "key_w5_management_api_key_list_owner_default"
	w5ManagementAPIKeyListStableZID      = "key_w5_management_api_key_list_z"
	w5ManagementAPIKeyListStableAID      = "key_w5_management_api_key_list_a"
	w5ManagementAPIKeyListLiteralID      = "key_w5_management_api_key_list_literal"
	w5ManagementAPIKeyListEmptyUsageID   = "key_w5_management_api_key_list_empty"
	w5ManagementAPIKeyListLowercaseID    = "key_w5_management_api_key_list_lowercase"
)

func TestW5ManagementAPIKeyOperationLogQueueCountMatchesConfiguredIDs(t *testing.T) {
	if w5ManagementAPIKeyOperationLogQueueCountMatches(4, 0, 6) {
		t.Fatal("four completed jobs must not satisfy six configured operation log IDs")
	}
	if !w5ManagementAPIKeyOperationLogQueueCountMatches(6, 0, 6) {
		t.Fatal("six completed jobs should satisfy six configured operation log IDs")
	}
	if w5ManagementAPIKeyOperationLogQueueCountMatches(6, 1, 6) {
		t.Fatal("archived jobs must fail the operation log queue assertion")
	}
}

func TestW5ManagementAPIKeyUpdatePostgresRedisSmoke(t *testing.T) {
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
	redisQueueURL := w3RedisURLWithDB(t, redisURL, 0)
	redisStateURL := w3RedisURLWithDB(t, redisURL, 1)
	redisCacheURL := w3RedisURLWithDB(t, redisURL, 2)
	redisOpts, err := queue.ParseRedisURL(redisQueueURL)
	if err != nil {
		t.Fatalf("parse redis queue url: %v", err)
	}
	stateRedis, err := redisplatform.NewClient(redisStateURL, w5ManagementAPIKeySecretRedisNamespace+":state")
	if err != nil {
		t.Fatalf("open state redis: %v", err)
	}
	defer closeRedisClient(t, stateRedis)
	cacheRedis, err := redisplatform.NewClient(redisCacheURL, w5ManagementAPIKeySecretRedisNamespace+":cache")
	if err != nil {
		t.Fatalf("open cache redis: %v", err)
	}
	defer closeRedisClient(t, cacheRedis)

	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	fixtureTimes := insertW5ManagementAPIKeyListFixtures(t, ctx, db, now)
	sessionLastSeenAt := now.Add(-20 * time.Minute)
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w5ManagementAPIKeyListAdminSID,
		w5ManagementAPIKeyListAdminID,
		w5ManagementAPIKeyListAdminToken,
		sessionLastSeenAt,
	)
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w5ManagementAPIKeyListOwnerSID,
		w5ManagementAPIKeyListOwnerID,
		w5ManagementAPIKeyListOwnerToken,
		sessionLastSeenAt,
	)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var invalidationVersion int
	var lookupVersion int
	apiKeySecretInvalidator, err := newW5ManagementAPIKeySmokeInvalidator(
		cacheRedis,
		stateRedis,
		now,
		&invalidationVersion,
		&lookupVersion,
	)
	if err != nil {
		t.Fatalf("create API Key secret invalidator: %v", err)
	}

	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	var workerErrMu sync.Mutex
	var workerRunErr error
	go func() {
		err := app.RunIngestWorker(workerCtx, config.Config{
			PostgresURL:     postgresURL,
			RedisQueueURL:   redisQueueURL,
			RedisNamespace:  "juhe-ai",
			LogLevel:        "error",
			ShutdownTimeout: time.Second,
		}, logger)
		workerErrMu.Lock()
		workerRunErr = err
		workerErrMu.Unlock()
		close(workerDone)
	}()
	defer func() {
		stopWorker()
		select {
		case <-workerDone:
		case <-time.After(5 * time.Second):
			t.Fatal("ingest worker shutdown timed out")
		}
		workerErrMu.Lock()
		err := workerRunErr
		workerErrMu.Unlock()
		if err != nil {
			t.Fatalf("ingest worker run: %v", err)
		}
	}()

	logClient := queue.NewClient(redisOpts)
	defer closeClient(t, logClient)
	inspector := queue.NewInspector(redisOpts)
	defer closeInspector(t, inspector)

	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	service := managementapikeys.NewServiceWithOptions(managementapikeys.ServiceOptions{
		ListReader:               store,
		Creator:                  store,
		Updater:                  store,
		Deleter:                  store,
		UsageStatsTimezoneReader: store,
		SecretStore:              store,
		SecretTransactor:         store,
		Invalidator:              apiKeySecretInvalidator,
		Secret:                   w5ManagementAPIKeySecretRuntimeSecret,
		Now:                      func() time.Time { return now },
	})
	cfg := config.Config{
		Host:                 "127.0.0.1",
		Port:                 3000,
		ManagementAPIEnabled: true,
		TrustProxy:           "false",
	}
	logIDs := []string{
		w5ManagementAPIKeySecretAdminRevealLogID,
		w5ManagementAPIKeySecretSelfRevealLogID,
		w5ManagementAPIKeySecretAdminRefreshLogID,
		w5ManagementAPIKeySecretRepairedRevealLogID,
		w5ManagementAPIKeyCreateAdminLogID,
		w5ManagementAPIKeyCreateSelfLogID,
		w5ManagementAPIKeyUpdateAdminGlobalLogID,
		w5ManagementAPIKeyUpdateAdminExplicitLogID,
		w5ManagementAPIKeyUpdateSameDisabledRouteLogID,
		w5ManagementAPIKeyUpdateSelfScheduleLogID,
		w5ManagementAPIKeyUpdateSelfClearLogID,
		w5ManagementAPIKeyUpdateCommittedFailureLogID,
		w5ManagementAPIKeyDeleteAdminGlobalLogID,
		w5ManagementAPIKeyDeleteSelfLogID,
		w5ManagementAPIKeyDeleteCommittedFailureLogID,
	}
	nextLogID := 0
	operationLogOptions := httpapi.ManagementOperationLogOptions{
		Config:         cfg,
		Logger:         logger,
		Client:         logClient,
		SettingsReader: store,
		Now:            func() time.Time { return now },
		NewLogID: func() string {
			if nextLogID >= len(logIDs) {
				t.Fatalf("unexpected extra API Key secret operation log %d", nextLogID+1)
				return ""
			}
			id := logIDs[nextLogID]
			nextLogID++
			return id
		},
	}
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                           cfg,
		Logger:                           logger,
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementAPIKeyListHandler:      httpapi.NewManagementAPIKeyListHandler(service),
		ManagementMyAPIKeyListHandler: httpapi.NewManagementMyAPIKeyListHandler(
			service,
		),
		ManagementAPIKeySecretHandler: httpapi.NewManagementAPIKeySecretHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
		ManagementMyAPIKeySecretHandler: httpapi.NewManagementMyAPIKeySecretHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
		ManagementAPIKeyRefreshHandler: httpapi.NewManagementAPIKeyRefreshHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
		ManagementMyAPIKeyRefreshHandler: httpapi.NewManagementMyAPIKeyRefreshHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
		ManagementAPIKeyCreateHandler: httpapi.NewManagementAPIKeyCreateHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
		ManagementMyAPIKeyCreateHandler: httpapi.NewManagementMyAPIKeyCreateHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
		ManagementAPIKeyUpdateHandler: httpapi.NewManagementAPIKeyUpdateHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
		ManagementMyAPIKeyUpdateHandler: httpapi.NewManagementMyAPIKeyUpdateHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
		ManagementAPIKeyDeleteHandler: httpapi.NewManagementAPIKeyDeleteHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
		ManagementMyAPIKeyDeleteHandler: httpapi.NewManagementMyAPIKeyDeleteHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
	})

	disabledRouter := httpapi.NewRouter(httpapi.RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: false,
			TrustProxy:           "false",
		},
		Logger:                        slog.New(slog.NewTextHandler(io.Discard, nil)),
		ManagementAPIAuthMiddleware:   httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIKeyListHandler:   httpapi.NewManagementAPIKeyListHandler(service),
		ManagementMyAPIKeyListHandler: httpapi.NewManagementMyAPIKeyListHandler(service),
	})
	disabled := serveW5ManagementAPIKeyListRequest(
		disabledRouter,
		"/__aisys__/api/api-keys",
		w5ManagementAPIKeyListAdminToken,
	)
	if disabled.Code != http.StatusNotFound {
		t.Fatalf("management opt-in disabled status = %d, body = %s", disabled.Code, disabled.Body.String())
	}

	unauthorized := serveW5ManagementAPIKeyListRequest(
		router,
		"/__aisys__/api/my-api-keys",
		"",
	)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("missing session status = %d, body = %s", unauthorized.Code, unauthorized.Body.String())
	}
	assertW5ManagementAPIKeyListNoStore(t, unauthorized, "missing session")

	forbidden := serveW5ManagementAPIKeyListRequest(
		router,
		"/__aisys__/api/api-keys",
		w5ManagementAPIKeyListOwnerToken,
	)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("ordinary user global status = %d, body = %s", forbidden.Code, forbidden.Body.String())
	}
	assertW5ManagementAPIKeyListNoStore(t, forbidden, "ordinary user global")
	var forbiddenBody map[string]string
	if err := json.NewDecoder(forbidden.Body).Decode(&forbiddenBody); err != nil {
		t.Fatalf("decode ordinary user global response: %v", err)
	}
	if forbiddenBody["message"] != "需要管理员权限" {
		t.Fatalf("ordinary user global response = %+v", forbiddenBody)
	}

	global := requestW5ManagementAPIKeyList(
		t,
		router,
		"/__aisys__/api/api-keys",
		w5ManagementAPIKeyListAdminToken,
	)
	assertW5ManagementAPIKeyListIDs(t, global.Result.Items, []string{
		w5ManagementAPIKeyListOtherDefaultID,
		w5ManagementAPIKeyListOwnerDefaultID,
		w5ManagementAPIKeyListStableZID,
		w5ManagementAPIKeyListStableAID,
		w5ManagementAPIKeyListLiteralID,
		w5ManagementAPIKeyListEmptyUsageID,
		w5ManagementAPIKeyListLowercaseID,
	})
	if global.Result.Page != 1 ||
		global.Result.PageSize != 50 ||
		global.Result.Total != 7 ||
		global.Result.HasMore {
		t.Fatalf("admin global list = %+v", global.Result)
	}
	for index, item := range global.Result.Items {
		if item.SystemAccountID == "" || item.SystemAccountName == "" {
			t.Fatalf("admin global item omitted owner fields: %+v", item)
		}
		if _, exists := global.RawItems[index]["systemAccountId"]; !exists {
			t.Fatalf("admin global item omitted raw systemAccountId: %s", global.Body)
		}
		if _, exists := global.RawItems[index]["systemAccountName"]; !exists {
			t.Fatalf("admin global item omitted raw systemAccountName: %s", global.Body)
		}
	}
	assertW5ManagementAPIKeyListDTOIsSecretFree(t, global)

	owner := requestW5ManagementAPIKeyList(
		t,
		router,
		"/__aisys__/api/api-keys?systemAccountId="+w5ManagementAPIKeyListOwnerID,
		w5ManagementAPIKeyListAdminToken,
	)
	assertW5ManagementAPIKeyListIDs(t, owner.Result.Items, []string{
		w5ManagementAPIKeyListOwnerDefaultID,
		w5ManagementAPIKeyListStableZID,
		w5ManagementAPIKeyListStableAID,
		w5ManagementAPIKeyListLiteralID,
		w5ManagementAPIKeyListEmptyUsageID,
		w5ManagementAPIKeyListLowercaseID,
	})
	if owner.Result.Total != 6 || owner.Result.HasMore {
		t.Fatalf("admin owner list = %+v", owner.Result)
	}

	var pagedItems []managementapikeys.ListItem
	for _, page := range []struct {
		number      int
		wantIDs     []string
		wantTotal   int
		wantHasMore bool
	}{
		{
			number: 1,
			wantIDs: []string{
				w5ManagementAPIKeyListOwnerDefaultID,
				w5ManagementAPIKeyListStableZID,
			},
			wantTotal:   3,
			wantHasMore: true,
		},
		{
			number: 2,
			wantIDs: []string{
				w5ManagementAPIKeyListStableAID,
				w5ManagementAPIKeyListLiteralID,
			},
			wantTotal:   5,
			wantHasMore: true,
		},
		{
			number: 3,
			wantIDs: []string{
				w5ManagementAPIKeyListEmptyUsageID,
				w5ManagementAPIKeyListLowercaseID,
			},
			wantTotal:   6,
			wantHasMore: false,
		},
	} {
		response := requestW5ManagementAPIKeyList(
			t,
			router,
			"/__aisys__/api/api-keys?systemAccountId="+w5ManagementAPIKeyListOwnerID+
				"&page="+strconv.Itoa(page.number)+"&pageSize=2",
			w5ManagementAPIKeyListAdminToken,
		)
		if response.Result.Page != page.number ||
			response.Result.PageSize != 2 ||
			response.Result.Total != page.wantTotal ||
			response.Result.HasMore != page.wantHasMore {
			t.Fatalf("admin owner page %d = %+v", page.number, response.Result)
		}
		assertW5ManagementAPIKeyListIDs(t, response.Result.Items, page.wantIDs)
		assertW5ManagementAPIKeyListDTOIsSecretFree(t, response)
		pagedItems = append(pagedItems, response.Result.Items...)
	}
	assertW5ManagementAPIKeyListIDs(t, pagedItems, []string{
		w5ManagementAPIKeyListOwnerDefaultID,
		w5ManagementAPIKeyListStableZID,
		w5ManagementAPIKeyListStableAID,
		w5ManagementAPIKeyListLiteralID,
		w5ManagementAPIKeyListEmptyUsageID,
		w5ManagementAPIKeyListLowercaseID,
	})

	disabledStatus := requestW5ManagementAPIKeyList(
		t,
		router,
		"/__aisys__/api/api-keys?systemAccountId="+w5ManagementAPIKeyListOwnerID+"&status=disabled",
		w5ManagementAPIKeyListAdminToken,
	)
	assertW5ManagementAPIKeyListIDs(t, disabledStatus.Result.Items, []string{
		w5ManagementAPIKeyListLiteralID,
	})

	secondaryRoute := requestW5ManagementAPIKeyList(
		t,
		router,
		"/__aisys__/api/api-keys?systemAccountId="+w5ManagementAPIKeyListOwnerID+
			"&routeStrategyId="+w5ManagementAPIKeyListOwnerSecondaryRouteID,
		w5ManagementAPIKeyListAdminToken,
	)
	assertW5ManagementAPIKeyListIDs(t, secondaryRoute.Result.Items, []string{
		w5ManagementAPIKeyListLiteralID,
		w5ManagementAPIKeyListEmptyUsageID,
		w5ManagementAPIKeyListLowercaseID,
	})

	uppercasePrefix := requestW5ManagementAPIKeyList(
		t,
		router,
		"/__aisys__/api/api-keys?systemAccountId="+w5ManagementAPIKeyListOwnerID+"&keyword=Alpha",
		w5ManagementAPIKeyListAdminToken,
	)
	assertW5ManagementAPIKeyListIDs(t, uppercasePrefix.Result.Items, []string{
		w5ManagementAPIKeyListStableZID,
		w5ManagementAPIKeyListStableAID,
		w5ManagementAPIKeyListLiteralID,
	})
	if findW5ManagementAPIKeyListItem(
		uppercasePrefix.Result.Items,
		w5ManagementAPIKeyListLowercaseID,
	) != nil {
		t.Fatalf("case-sensitive prefix matched lowercase name: %+v", uppercasePrefix.Result.Items)
	}

	literalPrefix := requestW5ManagementAPIKeyList(
		t,
		router,
		"/__aisys__/api/api-keys?systemAccountId="+w5ManagementAPIKeyListOwnerID+"&keyword=Alpha%25",
		w5ManagementAPIKeyListAdminToken,
	)
	assertW5ManagementAPIKeyListIDs(t, literalPrefix.Result.Items, []string{
		w5ManagementAPIKeyListLiteralID,
	})

	self := requestW5ManagementAPIKeyList(
		t,
		router,
		"/__aisys__/api/my-api-keys?systemAccountId="+w5ManagementAPIKeyListOtherID+
			"&page=1&pageSize=50",
		w5ManagementAPIKeyListOwnerToken,
	)
	assertW5ManagementAPIKeyListIDs(t, self.Result.Items, []string{
		w5ManagementAPIKeyListOwnerDefaultID,
		w5ManagementAPIKeyListStableZID,
		w5ManagementAPIKeyListStableAID,
		w5ManagementAPIKeyListLiteralID,
		w5ManagementAPIKeyListEmptyUsageID,
		w5ManagementAPIKeyListLowercaseID,
	})
	if self.Result.Total != 6 || self.Result.HasMore {
		t.Fatalf("self list pagination = %+v", self.Result)
	}
	for index, item := range self.Result.Items {
		if item.SystemAccountID != "" || item.SystemAccountName != "" {
			t.Fatalf("self item leaked owner fields: %+v", item)
		}
		if _, exists := self.RawItems[index]["systemAccountId"]; exists {
			t.Fatalf("self item exposed systemAccountId: %s", self.Body)
		}
		if _, exists := self.RawItems[index]["systemAccountName"]; exists {
			t.Fatalf("self item exposed systemAccountName: %s", self.Body)
		}
	}
	assertW5ManagementAPIKeyListDTOIsSecretFree(t, self)

	defaultItem := requireW5ManagementAPIKeyListItem(
		t,
		owner.Result.Items,
		w5ManagementAPIKeyListOwnerDefaultID,
	)
	if defaultItem.SystemAccountID != w5ManagementAPIKeyListOwnerID ||
		defaultItem.SystemAccountName != "W5 API Key List Owner" ||
		defaultItem.Name != "Owner Default" ||
		defaultItem.Description == nil ||
		*defaultItem.Description != "owner default description" ||
		defaultItem.KeyPrefix != "sk-w5-owner-default" ||
		defaultItem.KeySuffix != "def123" ||
		defaultItem.Status != "active" ||
		!defaultItem.IsDefault ||
		defaultItem.RouteStrategyID != w5ManagementAPIKeyListOwnerPrimaryRouteID ||
		defaultItem.RouteStrategyName != "W5 Owner Primary" ||
		defaultItem.RouteStrategyMode != "normal" ||
		defaultItem.RouteStrategyStatus != "active" ||
		defaultItem.ExpiresAt == nil ||
		!defaultItem.ExpiresAt.UTC().Equal(fixtureTimes.ExpiresAt.UTC()) {
		t.Fatalf("owner default DTO = %+v", defaultItem)
	}
	if defaultItem.QuotaLimits.Hourly == nil ||
		!defaultItem.QuotaLimits.Hourly.Enabled ||
		defaultItem.QuotaLimits.Hourly.Hours != 6 ||
		defaultItem.QuotaLimits.Hourly.Limit != 12.5 ||
		defaultItem.QuotaLimits.Daily == nil ||
		defaultItem.QuotaLimits.Daily.Limit != 100 ||
		defaultItem.QuotaLimits.Weekly == nil ||
		defaultItem.QuotaLimits.Weekly.Limit != 500 ||
		defaultItem.QuotaLimits.Monthly == nil ||
		defaultItem.QuotaLimits.Monthly.Limit != 2000 ||
		defaultItem.QuotaLimits.Total == nil ||
		defaultItem.QuotaLimits.Total.Limit != 9000 {
		t.Fatalf("owner default quota limits = %+v", defaultItem.QuotaLimits)
	}
	assertW5ManagementAPIKeyListSchedule(t, defaultItem.AvailabilitySchedule)
	if defaultItem.Usage.RequestCount != 7 ||
		defaultItem.Usage.InputTokens != 70 ||
		defaultItem.Usage.OutputTokens != 14 ||
		defaultItem.Usage.CacheReadTokens != 21 ||
		defaultItem.Usage.CacheReadCost != 0.21 ||
		defaultItem.Usage.CacheWriteTokens != 22 ||
		defaultItem.Usage.CacheWrite1hTokens != 4 ||
		defaultItem.Usage.CacheWriteCost != 0.22 ||
		defaultItem.Usage.ThinkingTokens != 23 ||
		defaultItem.Usage.InputImageTokens != 24 ||
		defaultItem.Usage.OutputImageTokens != 25 ||
		defaultItem.Usage.TotalTokens != 84 ||
		defaultItem.Usage.TotalCost != 2.5 ||
		defaultItem.Usage.LastUsedAt == nil ||
		!defaultItem.Usage.LastUsedAt.UTC().Equal(fixtureTimes.LastUsedAt.UTC()) {
		t.Fatalf("owner-scoped usage = %+v", defaultItem.Usage)
	}

	emptyUsage := requireW5ManagementAPIKeyListItem(
		t,
		owner.Result.Items,
		w5ManagementAPIKeyListEmptyUsageID,
	)
	if !reflect.DeepEqual(emptyUsage.Usage, port.ManagementAccountUsageSummary{}) {
		t.Fatalf("empty usage = %+v, want all zero", emptyUsage.Usage)
	}
	emptyUsageRaw := requireW5ManagementAPIKeyListRawItem(
		t,
		owner,
		w5ManagementAPIKeyListEmptyUsageID,
	)
	var rawUsage map[string]json.RawMessage
	if err := json.Unmarshal(emptyUsageRaw["usage"], &rawUsage); err != nil {
		t.Fatalf("decode empty usage: %v", err)
	}
	for _, field := range []string{
		"requestCount",
		"inputTokens",
		"outputTokens",
		"cacheReadTokens",
		"cacheReadCost",
		"cacheWriteTokens",
		"cacheWrite1hTokens",
		"cacheWriteCost",
		"thinkingTokens",
		"inputImageTokens",
		"outputImageTokens",
		"totalTokens",
		"totalCost",
	} {
		if got := string(rawUsage[field]); got != "0" {
			t.Fatalf("empty usage %s = %s, want 0", field, got)
		}
	}
	if _, exists := rawUsage["lastUsedAt"]; exists {
		t.Fatalf("empty usage exposed lastUsedAt: %s", emptyUsageRaw["usage"])
	}

	assertW2ManagementSessionLastSeenAt(
		t,
		ctx,
		db,
		w5ManagementAPIKeyListAdminSID,
		sessionLastSeenAt,
	)
	assertW2ManagementSessionLastSeenAt(
		t,
		ctx,
		db,
		w5ManagementAPIKeyListOwnerSID,
		sessionLastSeenAt,
	)
	exerciseW5ManagementAPIKeySecretSmoke(
		t,
		ctx,
		db,
		router,
		cacheRedis,
		stateRedis,
		inspector,
		workerDone,
		func() error {
			workerErrMu.Lock()
			defer workerErrMu.Unlock()
			return workerRunErr
		},
		sessionLastSeenAt,
		now,
	)
	exerciseW5ManagementAPIKeyCreateSmoke(
		t,
		ctx,
		db,
		store,
		router,
		cacheRedis,
		stateRedis,
		inspector,
		workerDone,
		func() error {
			workerErrMu.Lock()
			defer workerErrMu.Unlock()
			return workerRunErr
		},
		now,
	)
	exerciseW5ManagementAPIKeyUpdateSmoke(
		t,
		ctx,
		db,
		store,
		router,
		redisCacheURL,
		stateRedis,
		operationLogOptions,
		inspector,
		workerDone,
		func() error {
			workerErrMu.Lock()
			defer workerErrMu.Unlock()
			return workerRunErr
		},
		cfg,
		logger,
		now,
	)
	exerciseW5ManagementAPIKeyDeleteSmoke(
		t,
		ctx,
		db,
		store,
		router,
		cacheRedis,
		redisCacheURL,
		stateRedis,
		operationLogOptions,
		inspector,
		workerDone,
		func() error {
			workerErrMu.Lock()
			defer workerErrMu.Unlock()
			return workerRunErr
		},
		cfg,
		logger,
		now,
		&invalidationVersion,
		&lookupVersion,
	)
	queueInfo, err := inspector.QueueInfo(operationlogjob.QueueName)
	if err != nil {
		t.Fatalf("read operation log queue info: %v", err)
	}
	if !w5ManagementAPIKeyOperationLogQueueCountMatches(
		queueInfo.Completed,
		queueInfo.Archived,
		len(logIDs),
	) {
		t.Fatalf(
			"operation log queue completed=%d archived=%d, want completed=%d archived=0",
			queueInfo.Completed,
			queueInfo.Archived,
			len(logIDs),
		)
	}
	if nextLogID != len(logIDs) {
		t.Fatalf("operation log ids consumed = %d, want %d", nextLogID, len(logIDs))
	}
}

func w5ManagementAPIKeyOperationLogQueueCountMatches(
	completed int,
	archived int,
	expectedCompleted int,
) bool {
	return completed == expectedCompleted && archived == 0
}

type w5ManagementAPIKeyListResponse struct {
	Result   managementapikeys.ListResult
	RawItems []map[string]json.RawMessage
	Body     string
}

type w5ManagementAPIKeyListFixtureTimes struct {
	ExpiresAt  time.Time
	LastUsedAt time.Time
}

func requestW5ManagementAPIKeyList(
	t *testing.T,
	router http.Handler,
	target string,
	sessionToken string,
) w5ManagementAPIKeyListResponse {
	t.Helper()
	rec := serveW5ManagementAPIKeyListRequest(router, target, sessionToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, body = %s", target, rec.Code, rec.Body.String())
	}
	assertW5ManagementAPIKeyListNoStore(t, rec, target)
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
	var result managementapikeys.ListResult
	if err := json.Unmarshal(rawData, &result); err != nil {
		t.Fatalf("decode GET %s result: %v", target, err)
	}
	var shape struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rawData, &shape); err != nil {
		t.Fatalf("decode GET %s raw items: %v", target, err)
	}
	return w5ManagementAPIKeyListResponse{
		Result:   result,
		RawItems: shape.Items,
		Body:     body,
	}
}

func serveW5ManagementAPIKeyListRequest(
	router http.Handler,
	target string,
	sessionToken string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if sessionToken != "" {
		req.Header.Set("Cookie", managementauth.SessionCookieName+"="+sessionToken)
	}
	req.Header.Set("User-Agent", "w5-management-api-key-list-smoke")
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func assertW5ManagementAPIKeyListNoStore(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	label string,
) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("%s Cache-Control = %q, want no-store", label, got)
	}
}

func assertW5ManagementAPIKeyListIDs(
	t *testing.T,
	items []managementapikeys.ListItem,
	want []string,
) {
	t.Helper()
	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, item.ID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("management API Key ids = %#v, want %#v", got, want)
	}
}

func findW5ManagementAPIKeyListItem(
	items []managementapikeys.ListItem,
	id string,
) *managementapikeys.ListItem {
	for index := range items {
		if items[index].ID == id {
			return &items[index]
		}
	}
	return nil
}

func requireW5ManagementAPIKeyListItem(
	t *testing.T,
	items []managementapikeys.ListItem,
	id string,
) managementapikeys.ListItem {
	t.Helper()
	item := findW5ManagementAPIKeyListItem(items, id)
	if item == nil {
		t.Fatalf("management API Key %s missing from %+v", id, items)
	}
	return *item
}

func requireW5ManagementAPIKeyListRawItem(
	t *testing.T,
	response w5ManagementAPIKeyListResponse,
	id string,
) map[string]json.RawMessage {
	t.Helper()
	for _, item := range response.RawItems {
		var itemID string
		if err := json.Unmarshal(item["id"], &itemID); err != nil {
			t.Fatalf("decode management API Key raw id: %v", err)
		}
		if itemID == id {
			return item
		}
	}
	t.Fatalf("management API Key raw item %s missing from %s", id, response.Body)
	return nil
}

func assertW5ManagementAPIKeyListDTOIsSecretFree(
	t *testing.T,
	response w5ManagementAPIKeyListResponse,
) {
	t.Helper()
	for _, item := range response.RawItems {
		for _, forbidden := range []string{
			"key",
			"keyHash",
			"key_hash",
			"keySecretEncrypted",
			"key_secret_encrypted",
			"createdAt",
			"updatedAt",
		} {
			if _, exists := item[forbidden]; exists {
				t.Fatalf("management API Key list exposed %s: %s", forbidden, response.Body)
			}
		}
	}
	for _, forbidden := range []string{
		`"key":`,
		`"keyHash":`,
		`"key_hash":`,
		`"keySecretEncrypted":`,
		`"key_secret_encrypted":`,
		`"createdAt":`,
		`"updatedAt":`,
	} {
		if strings.Contains(response.Body, forbidden) {
			t.Fatalf("management API Key list body exposed %s: %s", forbidden, response.Body)
		}
	}
}

func assertW5ManagementAPIKeyListSchedule(
	t *testing.T,
	schedule map[string]any,
) {
	t.Helper()
	if schedule["enabled"] != true ||
		schedule["timezone"] != "UTC" ||
		schedule["mode"] != "allow_windows" {
		t.Fatalf("availability schedule = %#v", schedule)
	}
	windows, ok := schedule["windows"].([]any)
	if !ok || len(windows) != 1 {
		t.Fatalf("availability schedule windows = %#v", schedule["windows"])
	}
	window, ok := windows[0].(map[string]any)
	if !ok ||
		window["start"] != "00:00" ||
		window["end"] != "23:59" {
		t.Fatalf("availability schedule window = %#v", windows[0])
	}
	days, ok := window["daysOfWeek"].([]any)
	if !ok || !reflect.DeepEqual(days, []any{
		float64(1),
		float64(2),
		float64(3),
		float64(4),
		float64(5),
		float64(6),
		float64(7),
	}) {
		t.Fatalf("availability schedule days = %#v", window["daysOfWeek"])
	}
}

func insertW5ManagementAPIKeyListFixtures(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
) w5ManagementAPIKeyListFixtureTimes {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES
			(
				$1, 'w5-management-api-key-list-admin', 'W5 API Key List Admin', NULL, 'admin', 'active', 'hash',
				false, false, $4, $4
			),
			(
				$2, 'w5-management-api-key-list-owner', 'W5 API Key List Owner', NULL, 'user', 'active', 'hash',
				false, false, $4, $4
			),
			(
				$3, 'w5-management-api-key-list-other', 'W5 API Key List Other', NULL, 'user', 'active', 'hash',
				false, false, $4, $4
			)
	`, w5ManagementAPIKeyListAdminID, w5ManagementAPIKeyListOwnerID, w5ManagementAPIKeyListOtherID, now); err != nil {
		t.Fatalf("insert W5 management API Key list system accounts: %v", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.route_strategies (
			id, system_account_id, name, description, mode, status, is_default,
			config_json, created_at, updated_at
		) VALUES
			($1, $4, 'W5 Owner Primary', NULL, 'normal', 'active', true, NULL, $6, $6),
			($2, $4, 'W5 Owner Secondary', NULL, 'failover', 'disabled', false, NULL, $6, $6),
			($3, $5, 'W5 Other Weighted', NULL, 'weighted', 'active', true, NULL, $6, $6)
	`,
		w5ManagementAPIKeyListOwnerPrimaryRouteID,
		w5ManagementAPIKeyListOwnerSecondaryRouteID,
		w5ManagementAPIKeyListOtherRouteID,
		w5ManagementAPIKeyListOwnerID,
		w5ManagementAPIKeyListOtherID,
		now.Add(-24*time.Hour),
	); err != nil {
		t.Fatalf("insert W5 management API Key list route strategies: %v", err)
	}

	expiresAt := now.Add(30 * 24 * time.Hour)
	quotaLimits := `{
		"hourly":{"enabled":true,"hours":6,"limit":12.5},
		"daily":{"enabled":true,"limit":100},
		"weekly":{"enabled":true,"limit":500},
		"monthly":{"enabled":true,"limit":2000},
		"total":{"enabled":true,"limit":9000}
	}`
	availabilitySchedule := `{
		"enabled":true,
		"timezone":"UTC",
		"mode":"allow_windows",
		"windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"00:00","end":"23:59"}]
	}`
	type apiKeyFixture struct {
		id             string
		systemAccount  string
		routeStrategy  string
		name           string
		description    *string
		prefix         string
		suffix         string
		status         string
		isDefault      bool
		expiresAt      *time.Time
		quotaLimits    *string
		schedule       *string
		createdAt      time.Time
		updatedAt      time.Time
		encryptedValue string
	}
	description := "owner default description"
	fixtures := []apiKeyFixture{
		{
			id:             w5ManagementAPIKeyListOtherDefaultID,
			systemAccount:  w5ManagementAPIKeyListOtherID,
			routeStrategy:  w5ManagementAPIKeyListOtherRouteID,
			name:           "Other Default",
			prefix:         "sk-w5-other-default",
			suffix:         "oth123",
			status:         "active",
			isDefault:      true,
			createdAt:      now.Add(-6 * time.Hour),
			updatedAt:      now.Add(-30 * time.Second),
			encryptedValue: "encrypted-other-default",
		},
		{
			id:             w5ManagementAPIKeyListOwnerDefaultID,
			systemAccount:  w5ManagementAPIKeyListOwnerID,
			routeStrategy:  w5ManagementAPIKeyListOwnerPrimaryRouteID,
			name:           "Owner Default",
			description:    &description,
			prefix:         "sk-w5-owner-default",
			suffix:         "def123",
			status:         "active",
			isDefault:      true,
			expiresAt:      &expiresAt,
			quotaLimits:    &quotaLimits,
			schedule:       &availabilitySchedule,
			createdAt:      now.Add(-5 * time.Hour),
			updatedAt:      now.Add(-time.Minute),
			encryptedValue: "encrypted-owner-default",
		},
		{
			id:             w5ManagementAPIKeyListStableZID,
			systemAccount:  w5ManagementAPIKeyListOwnerID,
			routeStrategy:  w5ManagementAPIKeyListOwnerPrimaryRouteID,
			name:           "Alpha Zebra",
			prefix:         "sk-w5-z",
			suffix:         "zzz123",
			status:         "active",
			createdAt:      now.Add(-4 * time.Hour),
			updatedAt:      now.Add(-2 * time.Minute),
			encryptedValue: "encrypted-z",
		},
		{
			id:             w5ManagementAPIKeyListStableAID,
			systemAccount:  w5ManagementAPIKeyListOwnerID,
			routeStrategy:  w5ManagementAPIKeyListOwnerPrimaryRouteID,
			name:           "Alpha Apple",
			prefix:         "sk-w5-a",
			suffix:         "aaa123",
			status:         "active",
			createdAt:      now.Add(-4 * time.Hour),
			updatedAt:      now.Add(-2 * time.Minute),
			encryptedValue: "encrypted-a",
		},
		{
			id:             w5ManagementAPIKeyListLiteralID,
			systemAccount:  w5ManagementAPIKeyListOwnerID,
			routeStrategy:  w5ManagementAPIKeyListOwnerSecondaryRouteID,
			name:           "Alpha% Literal",
			prefix:         "sk-w5-literal",
			suffix:         "lit123",
			status:         "disabled",
			createdAt:      now.Add(-3 * time.Hour),
			updatedAt:      now.Add(-3 * time.Minute),
			encryptedValue: "encrypted-literal",
		},
		{
			id:             w5ManagementAPIKeyListEmptyUsageID,
			systemAccount:  w5ManagementAPIKeyListOwnerID,
			routeStrategy:  w5ManagementAPIKeyListOwnerSecondaryRouteID,
			name:           "Beta Empty",
			prefix:         "sk-w5-empty",
			suffix:         "emp123",
			status:         "active",
			createdAt:      now.Add(-2 * time.Hour),
			updatedAt:      now.Add(-4 * time.Minute),
			encryptedValue: "encrypted-empty",
		},
		{
			id:             w5ManagementAPIKeyListLowercaseID,
			systemAccount:  w5ManagementAPIKeyListOwnerID,
			routeStrategy:  w5ManagementAPIKeyListOwnerSecondaryRouteID,
			name:           "alpha lower",
			prefix:         "sk-w5-lower",
			suffix:         "low123",
			status:         "active",
			createdAt:      now.Add(-time.Hour),
			updatedAt:      now.Add(-5 * time.Minute),
			encryptedValue: "encrypted-lower",
		},
	}
	for _, fixture := range fixtures {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.api_keys (
				id, system_account_id, route_strategy_id, name, description,
				key_hash, key_prefix, key_suffix, key_secret_encrypted,
				status, is_default, expires_at, quota_limits_json,
				availability_schedule_json, availability_schedule_next_check_at,
				last_used_at, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, $8, $9,
				$10, $11, $12, $13,
				$14, NULL,
				NULL, $15, $16
			)
		`,
			fixture.id,
			fixture.systemAccount,
			fixture.routeStrategy,
			fixture.name,
			fixture.description,
			"hash-"+fixture.id,
			fixture.prefix,
			fixture.suffix,
			fixture.encryptedValue,
			fixture.status,
			fixture.isDefault,
			fixture.expiresAt,
			fixture.quotaLimits,
			fixture.schedule,
			fixture.createdAt,
			fixture.updatedAt,
		); err != nil {
			t.Fatalf("insert W5 management API Key list fixture %s: %v", fixture.id, err)
		}
	}

	lastUsedAt := now.Add(-45 * time.Minute).Add(123 * time.Microsecond)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_stats.usage_stats_totals (
			system_account_id, scope_type, scope_id, request_count,
			input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
			cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
			thinking_tokens, input_image_tokens, output_image_tokens,
			total_cost_usd, last_used_at, updated_at
		) VALUES
			($1, 'api_key', $3, 7, 70, 14, 21, 0.21, 22, 4, 0.22, 23, 24, 25, 2.5, $5, $6),
			($2, 'api_key', $3, 999, 9990, 999, 998, 9.98, 997, 996, 9.96, 995, 994, 993, 99.9, $4, $6),
			($2, 'api_key', $7, 3, 30, 6, 2, 0.02, 4, 1, 0.04, 5, 6, 7, 0.3, $4, $6)
	`,
		w5ManagementAPIKeyListOwnerID,
		w5ManagementAPIKeyListOtherID,
		w5ManagementAPIKeyListOwnerDefaultID,
		now.Add(-30*time.Minute).UTC().Format(time.RFC3339Nano),
		lastUsedAt.UTC().Format(time.RFC3339Nano),
		now.UTC().Format(time.RFC3339Nano),
		w5ManagementAPIKeyListOtherDefaultID,
	); err != nil {
		t.Fatalf("insert W5 management API Key usage totals: %v", err)
	}

	return w5ManagementAPIKeyListFixtureTimes{
		ExpiresAt:  expiresAt,
		LastUsedAt: lastUsedAt,
	}
}
