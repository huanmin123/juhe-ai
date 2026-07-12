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
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementroutestrategies"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w5ManagementRouteStrategyAdminID = "sys_w5_management_route_strategy_admin"
	w5ManagementRouteStrategyOwnerID = "sys_w5_management_route_strategy_owner"
	w5ManagementRouteStrategyOtherID = "sys_w5_management_route_strategy_other"

	w5ManagementRouteStrategyAdminSession = "sess_w5_management_route_strategy_admin"
	w5ManagementRouteStrategyOwnerSession = "sess_w5_management_route_strategy_owner"
	w5ManagementRouteStrategyAdminToken   = "w5-management-route-strategy-admin-session"
	w5ManagementRouteStrategyOwnerToken   = "w5-management-route-strategy-owner-session"

	w5ManagementRouteStrategyOtherRouteID   = "route_w5_management_route_strategy_other"
	w5ManagementRouteStrategyNormalID       = "route_w5_management_route_strategy_normal"
	w5ManagementRouteStrategyWeightedID     = "route_w5_management_route_strategy_weighted"
	w5ManagementRouteStrategyPercentDecoyID = "route_w5_management_route_strategy_percent_decoy"
	w5ManagementRouteStrategyLowercaseID    = "route_w5_management_route_strategy_lowercase"
	w5ManagementRouteStrategyHybridID       = "route_w5_management_route_strategy_hybrid"
	w5ManagementRouteStrategySecondNormalID = "route_w5_management_route_strategy_second_normal"
	w5ManagementRouteStrategyNormalBinding1 = "rsg_w5_management_route_strategy_normal_1"
	w5ManagementRouteStrategyNormalBinding2 = "rsg_w5_management_route_strategy_normal_2"
	w5ManagementRouteStrategyNormalBinding3 = "rsg_w5_management_route_strategy_normal_3"
	w5ManagementRouteStrategyNormalBinding4 = "rsg_w5_management_route_strategy_normal_4"
	w5ManagementRouteStrategyNormalGroup1   = "grp_w5_management_route_strategy_normal_1"
	w5ManagementRouteStrategyNormalGroup2   = "grp_w5_management_route_strategy_normal_2"
	w5ManagementRouteStrategyNormalGroup3   = "grp_w5_management_route_strategy_normal_3"
	w5ManagementRouteStrategyNormalGroup4   = "grp_w5_management_route_strategy_normal_4"
	w5ManagementRouteStrategyNormalAPIKey1  = "key_w5_management_route_strategy_normal_1"
	w5ManagementRouteStrategyNormalAPIKey2  = "key_w5_management_route_strategy_normal_2"
)

func TestW5ManagementRouteStrategyListDetailPostgresSmoke(t *testing.T) {
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

	now := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	insertW5ManagementRouteStrategyFixtures(t, ctx, db, now)
	sessionLastSeenAt := now.Add(-20 * time.Minute)
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w5ManagementRouteStrategyAdminSession,
		w5ManagementRouteStrategyAdminID,
		w5ManagementRouteStrategyAdminToken,
		sessionLastSeenAt,
	)
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w5ManagementRouteStrategyOwnerSession,
		w5ManagementRouteStrategyOwnerID,
		w5ManagementRouteStrategyOwnerToken,
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
	service := managementroutestrategies.NewService(store)
	enabledRouter := newW5ManagementRouteStrategyRouter(true, authenticator, service)
	disabledRouter := newW5ManagementRouteStrategyRouter(false, authenticator, service)

	for _, request := range []struct {
		target string
		token  string
	}{
		{target: "/__aisys__/api/route-strategies", token: w5ManagementRouteStrategyAdminToken},
		{target: "/__aisys__/api/my-route-strategies", token: w5ManagementRouteStrategyOwnerToken},
		{target: "/__aisys__/api/route-strategies/" + w5ManagementRouteStrategyNormalID, token: w5ManagementRouteStrategyAdminToken},
		{target: "/__aisys__/api/my-route-strategies/" + w5ManagementRouteStrategyNormalID, token: w5ManagementRouteStrategyOwnerToken},
	} {
		rec := serveW5ManagementRouteStrategyRequest(disabledRouter, request.target, request.token)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("disabled GET %s status = %d, body = %s", request.target, rec.Code, rec.Body.String())
		}
	}

	missingSession := serveW5ManagementRouteStrategyRequest(
		enabledRouter,
		"/__aisys__/api/my-route-strategies",
		"",
	)
	if missingSession.Code != http.StatusUnauthorized {
		t.Fatalf("missing session status = %d, body = %s", missingSession.Code, missingSession.Body.String())
	}
	assertW5ManagementRouteStrategyNoStore(t, missingSession, "missing session")

	forbidden := serveW5ManagementRouteStrategyRequest(
		enabledRouter,
		"/__aisys__/api/route-strategies",
		w5ManagementRouteStrategyOwnerToken,
	)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("ordinary user global status = %d, body = %s", forbidden.Code, forbidden.Body.String())
	}
	assertW5ManagementRouteStrategyNoStore(t, forbidden, "ordinary user global")

	global := requestW5ManagementRouteStrategyList(
		t,
		enabledRouter,
		"/__aisys__/api/route-strategies",
		w5ManagementRouteStrategyAdminToken,
	)
	assertW5ManagementRouteStrategyListIDs(t, global.Result.Items, []string{
		w5ManagementRouteStrategyOtherRouteID,
		w5ManagementRouteStrategyNormalID,
		w5ManagementRouteStrategyWeightedID,
		w5ManagementRouteStrategyPercentDecoyID,
		w5ManagementRouteStrategyLowercaseID,
		w5ManagementRouteStrategyHybridID,
		w5ManagementRouteStrategySecondNormalID,
	})
	if global.Result.Page != 1 ||
		global.Result.PageSize != 50 ||
		global.Result.Total != 7 ||
		global.Result.HasMore {
		t.Fatalf("admin global list = %+v", global.Result)
	}
	assertW5ManagementRouteStrategyOwnerFields(t, global.RawItems, true, "admin global")

	owner := requestW5ManagementRouteStrategyList(
		t,
		enabledRouter,
		"/__aisys__/api/route-strategies?systemAccountId="+w5ManagementRouteStrategyOwnerID,
		w5ManagementRouteStrategyAdminToken,
	)
	assertW5ManagementRouteStrategyListIDs(t, owner.Result.Items, w5ManagementRouteStrategyOwnerRouteIDs())
	if owner.Result.Total != 6 || owner.Result.HasMore {
		t.Fatalf("admin owner list = %+v", owner.Result)
	}
	assertW5ManagementRouteStrategyOwnerFields(t, owner.RawItems, true, "admin owner")

	var pagedItems []managementroutestrategies.ListItem
	for _, page := range []struct {
		number      int
		wantIDs     []string
		wantTotal   int
		wantHasMore bool
	}{
		{
			number:      1,
			wantIDs:     w5ManagementRouteStrategyOwnerRouteIDs()[0:2],
			wantTotal:   3,
			wantHasMore: true,
		},
		{
			number:      2,
			wantIDs:     w5ManagementRouteStrategyOwnerRouteIDs()[2:4],
			wantTotal:   5,
			wantHasMore: true,
		},
		{
			number:      3,
			wantIDs:     w5ManagementRouteStrategyOwnerRouteIDs()[4:6],
			wantTotal:   6,
			wantHasMore: false,
		},
	} {
		response := requestW5ManagementRouteStrategyList(
			t,
			enabledRouter,
			"/__aisys__/api/route-strategies?systemAccountId="+w5ManagementRouteStrategyOwnerID+
				"&page="+strconv.Itoa(page.number)+"&pageSize=2",
			w5ManagementRouteStrategyAdminToken,
		)
		if response.Result.Page != page.number ||
			response.Result.PageSize != 2 ||
			response.Result.Total != page.wantTotal ||
			response.Result.HasMore != page.wantHasMore {
			t.Fatalf("admin owner page %d = %+v", page.number, response.Result)
		}
		assertW5ManagementRouteStrategyListIDs(t, response.Result.Items, page.wantIDs)
		pagedItems = append(pagedItems, response.Result.Items...)
	}
	assertW5ManagementRouteStrategyListIDs(t, pagedItems, w5ManagementRouteStrategyOwnerRouteIDs())

	explicitZeroPageSize := requestW5ManagementRouteStrategyList(
		t,
		enabledRouter,
		"/__aisys__/api/route-strategies?systemAccountId="+w5ManagementRouteStrategyOwnerID+"&pageSize=0",
		w5ManagementRouteStrategyAdminToken,
	)
	assertW5ManagementRouteStrategyListIDs(
		t,
		explicitZeroPageSize.Result.Items,
		w5ManagementRouteStrategyOwnerRouteIDs()[0:1],
	)
	if explicitZeroPageSize.Result.PageSize != 1 ||
		explicitZeroPageSize.Result.Total != 2 ||
		!explicitZeroPageSize.Result.HasMore {
		t.Fatalf("explicit zero pageSize response = %+v", explicitZeroPageSize.Result)
	}

	literalPrefix := requestW5ManagementRouteStrategyList(
		t,
		enabledRouter,
		"/__aisys__/api/route-strategies?systemAccountId="+w5ManagementRouteStrategyOwnerID+
			"&keyword="+url.QueryEscape("Alpha%_"),
		w5ManagementRouteStrategyAdminToken,
	)
	assertW5ManagementRouteStrategyListIDs(t, literalPrefix.Result.Items, []string{
		w5ManagementRouteStrategyNormalID,
	})

	lowercasePrefix := requestW5ManagementRouteStrategyList(
		t,
		enabledRouter,
		"/__aisys__/api/route-strategies?systemAccountId="+w5ManagementRouteStrategyOwnerID+
			"&keyword="+url.QueryEscape("alpha%_"),
		w5ManagementRouteStrategyAdminToken,
	)
	assertW5ManagementRouteStrategyListIDs(t, lowercasePrefix.Result.Items, []string{
		w5ManagementRouteStrategyLowercaseID,
	})

	normalListItem := requireW5ManagementRouteStrategyListItem(
		t,
		owner.Result.Items,
		w5ManagementRouteStrategyNormalID,
	)
	if normalListItem.BindingCount != 4 ||
		normalListItem.APIKeyCount != 2 ||
		len(normalListItem.GroupBindingPreview) != 3 {
		t.Fatalf("normal list enrichment = %+v", normalListItem)
	}
	assertW5ManagementRouteStrategyPreviewIDs(t, normalListItem.GroupBindingPreview, []string{
		w5ManagementRouteStrategyNormalBinding1,
		w5ManagementRouteStrategyNormalBinding2,
		w5ManagementRouteStrategyNormalBinding3,
	})
	if normalListItem.GroupBindingPreview[0].GroupName != "W5 Route Strategy Group 1" ||
		normalListItem.GroupBindingPreview[0].ProviderCode != "openai" ||
		!normalListItem.GroupBindingPreview[0].GroupEnabled ||
		normalListItem.GroupBindingPreview[2].GroupEnabled {
		t.Fatalf("normal binding preview = %+v", normalListItem.GroupBindingPreview)
	}

	self := requestW5ManagementRouteStrategyList(
		t,
		enabledRouter,
		"/__aisys__/api/my-route-strategies?systemAccountId="+w5ManagementRouteStrategyOtherID,
		w5ManagementRouteStrategyOwnerToken,
	)
	assertW5ManagementRouteStrategyListIDs(t, self.Result.Items, w5ManagementRouteStrategyOwnerRouteIDs())
	assertW5ManagementRouteStrategyOwnerFields(t, self.RawItems, false, "self list")

	adminDetail := requestW5ManagementRouteStrategyDetail(
		t,
		enabledRouter,
		"/__aisys__/api/route-strategies/"+w5ManagementRouteStrategyNormalID+
			"?systemAccountId="+w5ManagementRouteStrategyOwnerID,
		w5ManagementRouteStrategyAdminToken,
	)
	if adminDetail.Result.ID != w5ManagementRouteStrategyNormalID ||
		adminDetail.Result.SystemAccountID != w5ManagementRouteStrategyOwnerID ||
		adminDetail.Result.SystemAccountName != "W5 Route Strategy Owner" ||
		adminDetail.Result.APIKeyCount != 2 ||
		len(adminDetail.Result.GroupBindings) != 4 {
		t.Fatalf("admin detail = %+v", adminDetail.Result)
	}
	assertW5ManagementRouteStrategyDetailBindingIDs(t, adminDetail.Result.GroupBindings, []string{
		w5ManagementRouteStrategyNormalBinding1,
		w5ManagementRouteStrategyNormalBinding2,
		w5ManagementRouteStrategyNormalBinding3,
		w5ManagementRouteStrategyNormalBinding4,
	})
	if adminDetail.Result.GroupBindings[0].Priority != 1 ||
		adminDetail.Result.GroupBindings[0].Weight != 70 ||
		adminDetail.Result.GroupBindings[3].Status != "disabled" {
		t.Fatalf("admin detail bindings = %+v", adminDetail.Result.GroupBindings)
	}
	assertW5ManagementRouteStrategyNormalConfig(t, adminDetail.Result.NormalRoutingConfig)
	assertW5ManagementRouteStrategyDetailOwnerFields(t, adminDetail.RawData, true, "admin detail")

	selfDetail := requestW5ManagementRouteStrategyDetail(
		t,
		enabledRouter,
		"/__aisys__/api/my-route-strategies/"+w5ManagementRouteStrategyNormalID+
			"?systemAccountId="+w5ManagementRouteStrategyOtherID,
		w5ManagementRouteStrategyOwnerToken,
	)
	if selfDetail.Result.ID != w5ManagementRouteStrategyNormalID ||
		selfDetail.Result.SystemAccountID != "" ||
		selfDetail.Result.SystemAccountName != "" ||
		len(selfDetail.Result.GroupBindings) != 4 {
		t.Fatalf("self detail = %+v", selfDetail.Result)
	}
	assertW5ManagementRouteStrategyNormalConfig(t, selfDetail.Result.NormalRoutingConfig)
	assertW5ManagementRouteStrategyDetailOwnerFields(t, selfDetail.RawData, false, "self detail")

	adminOwnerMismatch := serveW5ManagementRouteStrategyRequest(
		enabledRouter,
		"/__aisys__/api/route-strategies/"+w5ManagementRouteStrategyNormalID+
			"?systemAccountId="+w5ManagementRouteStrategyOtherID,
		w5ManagementRouteStrategyAdminToken,
	)
	assertW5ManagementRouteStrategyNotFound(t, adminOwnerMismatch, "admin owner mismatch")

	selfOwnerMismatch := serveW5ManagementRouteStrategyRequest(
		enabledRouter,
		"/__aisys__/api/my-route-strategies/"+w5ManagementRouteStrategyOtherRouteID+
			"?systemAccountId="+w5ManagementRouteStrategyOtherID,
		w5ManagementRouteStrategyOwnerToken,
	)
	assertW5ManagementRouteStrategyNotFound(t, selfOwnerMismatch, "self forged owner")

	assertW2ManagementSessionLastSeenAt(
		t,
		ctx,
		db,
		w5ManagementRouteStrategyAdminSession,
		sessionLastSeenAt,
	)
	assertW2ManagementSessionLastSeenAt(
		t,
		ctx,
		db,
		w5ManagementRouteStrategyOwnerSession,
		sessionLastSeenAt,
	)
}

func newW5ManagementRouteStrategyRouter(
	enabled bool,
	authenticator *managementauth.Authenticator,
	service *managementroutestrategies.Service,
) http.Handler {
	return httpapi.NewRouter(httpapi.RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: enabled,
			TrustProxy:           "false",
		},
		Logger:                                 slog.New(slog.NewTextHandler(io.Discard, nil)),
		ManagementAPIAuthMiddleware:            httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementRouteStrategyListHandler:     httpapi.NewManagementRouteStrategyListHandler(service),
		ManagementMyRouteStrategyListHandler:   httpapi.NewManagementMyRouteStrategyListHandler(service),
		ManagementRouteStrategyDetailHandler:   httpapi.NewManagementRouteStrategyDetailHandler(service),
		ManagementMyRouteStrategyDetailHandler: httpapi.NewManagementMyRouteStrategyDetailHandler(service),
	})
}

type w5ManagementRouteStrategyListResponse struct {
	Result   managementroutestrategies.ListResult
	RawItems []map[string]json.RawMessage
	Body     string
}

func requestW5ManagementRouteStrategyList(
	t *testing.T,
	router http.Handler,
	target string,
	sessionToken string,
) w5ManagementRouteStrategyListResponse {
	t.Helper()
	rec := serveW5ManagementRouteStrategyRequest(router, target, sessionToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, body = %s", target, rec.Code, rec.Body.String())
	}
	assertW5ManagementRouteStrategyNoStore(t, rec, target)
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
	var result managementroutestrategies.ListResult
	if err := json.Unmarshal(rawData, &result); err != nil {
		t.Fatalf("decode GET %s result: %v", target, err)
	}
	var shape struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rawData, &shape); err != nil {
		t.Fatalf("decode GET %s raw items: %v", target, err)
	}
	return w5ManagementRouteStrategyListResponse{
		Result:   result,
		RawItems: shape.Items,
		Body:     body,
	}
}

type w5ManagementRouteStrategyDetailResponse struct {
	Result  managementroutestrategies.DetailResult
	RawData map[string]json.RawMessage
	Body    string
}

func requestW5ManagementRouteStrategyDetail(
	t *testing.T,
	router http.Handler,
	target string,
	sessionToken string,
) w5ManagementRouteStrategyDetailResponse {
	t.Helper()
	rec := serveW5ManagementRouteStrategyRequest(router, target, sessionToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, body = %s", target, rec.Code, rec.Body.String())
	}
	assertW5ManagementRouteStrategyNoStore(t, rec, target)
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
	var result managementroutestrategies.DetailResult
	if err := json.Unmarshal(rawData, &result); err != nil {
		t.Fatalf("decode GET %s result: %v", target, err)
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(rawData, &shape); err != nil {
		t.Fatalf("decode GET %s raw detail: %v", target, err)
	}
	return w5ManagementRouteStrategyDetailResponse{
		Result:  result,
		RawData: shape,
		Body:    body,
	}
}

func serveW5ManagementRouteStrategyRequest(
	router http.Handler,
	target string,
	sessionToken string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if sessionToken != "" {
		req.Header.Set("Cookie", managementauth.SessionCookieName+"="+sessionToken)
	}
	req.Header.Set("User-Agent", "w5-management-route-strategy-list-detail-smoke")
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func assertW5ManagementRouteStrategyNoStore(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	label string,
) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("%s Cache-Control = %q, want no-store", label, got)
	}
}

func assertW5ManagementRouteStrategyNotFound(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	label string,
) {
	t.Helper()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("%s status = %d, body = %s", label, rec.Code, rec.Body.String())
	}
	assertW5ManagementRouteStrategyNoStore(t, rec, label)
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode %s response: %v", label, err)
	}
	if body["message"] != "策略路由不存在" {
		t.Fatalf("%s response = %+v", label, body)
	}
}

func assertW5ManagementRouteStrategyListIDs(
	t *testing.T,
	items []managementroutestrategies.ListItem,
	want []string,
) {
	t.Helper()
	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, item.ID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("management route strategy ids = %#v, want %#v", got, want)
	}
}

func w5ManagementRouteStrategyOwnerRouteIDs() []string {
	return []string{
		w5ManagementRouteStrategyNormalID,
		w5ManagementRouteStrategyWeightedID,
		w5ManagementRouteStrategyPercentDecoyID,
		w5ManagementRouteStrategyLowercaseID,
		w5ManagementRouteStrategyHybridID,
		w5ManagementRouteStrategySecondNormalID,
	}
}

func requireW5ManagementRouteStrategyListItem(
	t *testing.T,
	items []managementroutestrategies.ListItem,
	id string,
) managementroutestrategies.ListItem {
	t.Helper()
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("management route strategy %s missing from %+v", id, items)
	return managementroutestrategies.ListItem{}
}

func assertW5ManagementRouteStrategyOwnerFields(
	t *testing.T,
	items []map[string]json.RawMessage,
	wantVisible bool,
	label string,
) {
	t.Helper()
	for _, item := range items {
		_, hasID := item["systemAccountId"]
		_, hasName := item["systemAccountName"]
		if hasID != wantVisible || hasName != wantVisible {
			t.Fatalf("%s owner field visibility = id:%t name:%t item:%+v", label, hasID, hasName, item)
		}
	}
}

func assertW5ManagementRouteStrategyDetailOwnerFields(
	t *testing.T,
	data map[string]json.RawMessage,
	wantVisible bool,
	label string,
) {
	t.Helper()
	_, hasID := data["systemAccountId"]
	_, hasName := data["systemAccountName"]
	if hasID != wantVisible || hasName != wantVisible {
		t.Fatalf("%s owner field visibility = id:%t name:%t data:%+v", label, hasID, hasName, data)
	}
}

func assertW5ManagementRouteStrategyPreviewIDs(
	t *testing.T,
	items []managementroutestrategies.GroupBindingPreview,
	want []string,
) {
	t.Helper()
	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, item.ID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("management route strategy preview ids = %#v, want %#v", got, want)
	}
}

func assertW5ManagementRouteStrategyDetailBindingIDs(
	t *testing.T,
	items []managementroutestrategies.GroupBindingSummary,
	want []string,
) {
	t.Helper()
	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, item.ID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("management route strategy detail binding ids = %#v, want %#v", got, want)
	}
}

func assertW5ManagementRouteStrategyNormalConfig(
	t *testing.T,
	config *managementroutestrategies.NormalRoutingConfig,
) {
	t.Helper()
	if config == nil ||
		config.SchedulingPreference != "speed_first" ||
		config.SpeedFirstConfig == nil ||
		config.SpeedFirstConfig.FirstByteThresholdMs != 25000 ||
		config.SpeedFirstConfig.SlowTriggerCount != 4 ||
		config.SpeedFirstConfig.SlowWindowSeconds != 120 ||
		config.SpeedFirstConfig.RecoverySuccessCount != 3 ||
		config.SpeedFirstConfig.ProbeIntervalSeconds != 30 ||
		config.SpeedFirstConfig.DegradedTTLSeconds != 300 ||
		config.SpeedFirstConfig.MaxFirstByteRetriesPerRequest != 2 {
		t.Fatalf("normal route strategy config = %+v", config)
	}
}

func insertW5ManagementRouteStrategyFixtures(
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
			(
				$1, 'w5-management-route-strategy-admin', 'W5 Route Strategy Admin', NULL,
				'admin', 'active', 'hash', false, false, $4, $4
			),
			(
				$2, 'w5-management-route-strategy-owner', 'W5 Route Strategy Owner', NULL,
				'user', 'active', 'hash', false, false, $4, $4
			),
			(
				$3, 'w5-management-route-strategy-other', 'W5 Route Strategy Other', NULL,
				'user', 'active', 'hash', false, false, $4, $4
			)
	`, w5ManagementRouteStrategyAdminID, w5ManagementRouteStrategyOwnerID, w5ManagementRouteStrategyOtherID, now); err != nil {
		t.Fatalf("insert W5 management route strategy system accounts: %v", err)
	}

	normalConfig := `{
		"normalRoutingConfig": {
			"schedulingPreference": "speed_first",
			"speedFirstConfig": {
				"firstByteThresholdMs": "25000",
				"slowTriggerCount": 4
			}
		}
	}`
	type routeFixture struct {
		id              string
		systemAccountID string
		name            string
		description     *string
		mode            string
		status          string
		isDefault       bool
		configJSON      *string
		updatedAt       time.Time
	}
	normalDescription := "W5 route strategy detail"
	routes := []routeFixture{
		{
			id:              w5ManagementRouteStrategyOtherRouteID,
			systemAccountID: w5ManagementRouteStrategyOtherID,
			name:            "Other Global",
			mode:            "weighted",
			status:          "active",
			isDefault:       true,
			updatedAt:       now.Add(-30 * time.Second),
		},
		{
			id:              w5ManagementRouteStrategyNormalID,
			systemAccountID: w5ManagementRouteStrategyOwnerID,
			name:            "Alpha%_ Literal",
			description:     &normalDescription,
			mode:            "normal",
			status:          "active",
			isDefault:       true,
			configJSON:      &normalConfig,
			updatedAt:       now.Add(-time.Minute),
		},
		{
			id:              w5ManagementRouteStrategyWeightedID,
			systemAccountID: w5ManagementRouteStrategyOwnerID,
			name:            "Alpha Wildcard Decoy",
			mode:            "weighted",
			status:          "active",
			updatedAt:       now.Add(-2 * time.Minute),
		},
		{
			id:              w5ManagementRouteStrategyPercentDecoyID,
			systemAccountID: w5ManagementRouteStrategyOwnerID,
			name:            "Alpha%X Decoy",
			mode:            "failover",
			status:          "disabled",
			updatedAt:       now.Add(-3 * time.Minute),
		},
		{
			id:              w5ManagementRouteStrategyLowercaseID,
			systemAccountID: w5ManagementRouteStrategyOwnerID,
			name:            "alpha%_ lower",
			mode:            "round_robin",
			status:          "active",
			updatedAt:       now.Add(-4 * time.Minute),
		},
		{
			id:              w5ManagementRouteStrategyHybridID,
			systemAccountID: w5ManagementRouteStrategyOwnerID,
			name:            "Beta Hybrid",
			mode:            "hybrid_smart",
			status:          "active",
			configJSON: w5ManagementRouteStrategyStringPointer(`{
				"hybridRoutingConfig": {
					"scoringModel": "gpt-5",
					"levelRoutes": [
						{"minLevel": 1, "maxLevel": 5, "targetModel": "gpt-5-mini"},
						{"minLevel": 6, "maxLevel": 10, "targetModel": "gpt-5"}
					]
				}
			}`),
			updatedAt: now.Add(-5 * time.Minute),
		},
		{
			id:              w5ManagementRouteStrategySecondNormalID,
			systemAccountID: w5ManagementRouteStrategyOwnerID,
			name:            "Gamma Normal",
			mode:            "normal",
			status:          "active",
			updatedAt:       now.Add(-6 * time.Minute),
		},
	}
	for index, fixture := range routes {
		createdAt := now.Add(-24*time.Hour - time.Duration(index)*time.Minute)
		if _, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.route_strategies (
				id, system_account_id, name, description, mode, status,
				is_default, config_json, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, $5, $6,
				$7, $8, $9, $10
			)
		`,
			fixture.id,
			fixture.systemAccountID,
			fixture.name,
			fixture.description,
			fixture.mode,
			fixture.status,
			fixture.isDefault,
			fixture.configJSON,
			createdAt,
			fixture.updatedAt,
		); err != nil {
			t.Fatalf("insert W5 management route strategy fixture %s: %v", fixture.id, err)
		}
	}

	for index, group := range []struct {
		id      string
		name    string
		enabled bool
	}{
		{id: w5ManagementRouteStrategyNormalGroup1, name: "W5 Route Strategy Group 1", enabled: true},
		{id: w5ManagementRouteStrategyNormalGroup2, name: "W5 Route Strategy Group 2", enabled: true},
		{id: w5ManagementRouteStrategyNormalGroup3, name: "W5 Route Strategy Group 3", enabled: false},
		{id: w5ManagementRouteStrategyNormalGroup4, name: "W5 Route Strategy Group 4", enabled: true},
	} {
		fixtureTime := now.Add(-12*time.Hour + time.Duration(index)*time.Minute)
		if _, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.groups (
				id, system_account_id, name, provider_code, description,
				enabled, is_default, group_type, scheduling_policy_json,
				created_at, updated_at
			) VALUES (
				$1, $2, $3, 'openai', NULL,
				$4, false, 'personal', NULL,
				$5, $5
			)
		`, group.id, w5ManagementRouteStrategyOwnerID, group.name, group.enabled, fixtureTime); err != nil {
			t.Fatalf("insert W5 management route strategy group %s: %v", group.id, err)
		}
	}

	for index, binding := range []struct {
		id       string
		groupID  string
		priority int
		weight   int
		status   string
	}{
		{
			id:       w5ManagementRouteStrategyNormalBinding1,
			groupID:  w5ManagementRouteStrategyNormalGroup1,
			priority: 1,
			weight:   70,
			status:   "active",
		},
		{
			id:       w5ManagementRouteStrategyNormalBinding2,
			groupID:  w5ManagementRouteStrategyNormalGroup2,
			priority: 2,
			weight:   20,
			status:   "active",
		},
		{
			id:       w5ManagementRouteStrategyNormalBinding3,
			groupID:  w5ManagementRouteStrategyNormalGroup3,
			priority: 3,
			weight:   10,
			status:   "active",
		},
		{
			id:       w5ManagementRouteStrategyNormalBinding4,
			groupID:  w5ManagementRouteStrategyNormalGroup4,
			priority: 4,
			weight:   5,
			status:   "disabled",
		},
	} {
		fixtureTime := now.Add(-10*time.Hour + time.Duration(index)*time.Minute)
		if _, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.route_strategy_groups (
				id, route_strategy_id, system_account_id, group_id,
				priority, weight, status, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4,
				$5, $6, $7, $8, $8
			)
		`,
			binding.id,
			w5ManagementRouteStrategyNormalID,
			w5ManagementRouteStrategyOwnerID,
			binding.groupID,
			binding.priority,
			binding.weight,
			binding.status,
			fixtureTime,
		); err != nil {
			t.Fatalf("insert W5 management route strategy binding %s: %v", binding.id, err)
		}
	}

	for index, apiKeyID := range []string{
		w5ManagementRouteStrategyNormalAPIKey1,
		w5ManagementRouteStrategyNormalAPIKey2,
	} {
		fixtureTime := now.Add(-8*time.Hour + time.Duration(index)*time.Minute)
		if _, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.api_keys (
				id, system_account_id, route_strategy_id, name, description,
				key_hash, key_prefix, key_suffix, key_secret_encrypted,
				status, is_default, expires_at, quota_limits_json,
				availability_schedule_json, availability_schedule_next_check_at,
				last_used_at, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, NULL,
				$5, $6, $7, NULL,
				'active', $8, NULL, NULL,
				NULL, NULL,
				NULL, $9, $9
			)
		`,
			apiKeyID,
			w5ManagementRouteStrategyOwnerID,
			w5ManagementRouteStrategyNormalID,
			"W5 Route Strategy Key "+strconv.Itoa(index+1),
			"hash-"+apiKeyID,
			"sk-w5-route-"+strconv.Itoa(index+1),
			"route"+strconv.Itoa(index+1),
			index == 0,
			fixtureTime,
		); err != nil {
			t.Fatalf("insert W5 management route strategy API Key %s: %v", apiKeyID, err)
		}
	}
}

func w5ManagementRouteStrategyStringPointer(value string) *string {
	return &value
}
