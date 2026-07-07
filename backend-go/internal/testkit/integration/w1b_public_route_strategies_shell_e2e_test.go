//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/publicapi"
	publicapiauth "juhe-ai/backend-go/internal/modules/publicapi/auth"
	publicapiratelimit "juhe-ai/backend-go/internal/modules/publicapi/ratelimit"
	"juhe-ai/backend-go/internal/modules/publicgroups"
	"juhe-ai/backend-go/internal/modules/publicroutestrategies"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW1bPublicRouteStrategiesShellE2E(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
	redisOpts, err := queue.ParseRedisURL(redisURL)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}

	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone, workerErr := startW1bShellIngestWorker(t, workerCtx, postgresURL, redisURL)
	defer stopW1bShellIngestWorker(t, stopWorker, workerDone, workerErr)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	redisState, err := redisplatform.NewClient(redisURL, "w1b-public-route-shell-state")
	if err != nil {
		t.Fatalf("open redis state client: %v", err)
	}
	defer closeRedisClient(t, redisState)
	limiter, err := publicapiratelimit.NewLimiter(publicapiratelimit.Options{
		Client: redisState,
		Now:    w1bPublicRouteStrategiesShellNow,
	})
	if err != nil {
		t.Fatalf("new public api limiter: %v", err)
	}
	logClient := queue.NewClient(redisOpts)
	defer closeClient(t, logClient)

	token := "juis_w1b_route_shell"
	insertW1bRouteShellSourceAndToken(t, ctx, db, token, w1bPublicRouteStrategiesShellNow())

	var groupIDSeq atomic.Int32
	groupService := publicgroups.NewService(publicgroups.Options{
		Store:      store,
		Transactor: store,
		Now:        w1bPublicRouteStrategiesShellNow,
		NewID: func(prefix string) string {
			return prefix + "_w1b_route_shell_" + strconv.Itoa(int(groupIDSeq.Add(1)))
		},
	})
	primary, err := groupService.Add(ctx, publicgroups.AddInput{
		TargetUsername:    "admin",
		TargetDisplayName: "管理员",
		Name:              "主分组",
		ProviderCode:      "gpt",
	})
	if err != nil {
		t.Fatalf("seed primary group: %v", err)
	}
	backup, err := groupService.Add(ctx, publicgroups.AddInput{
		TargetUsername: "admin",
		Name:           "备用分组",
		ProviderCode:   "gpt",
	})
	if err != nil {
		t.Fatalf("seed backup group: %v", err)
	}

	var routeIDSeq atomic.Int32
	routeService := publicroutestrategies.NewService(publicroutestrategies.Options{
		Store:      store,
		Transactor: store,
		Now:        w1bPublicRouteStrategiesShellNow,
		NewID: func(prefix string) string {
			return prefix + "_w1b_route_shell_" + strconv.Itoa(int(routeIDSeq.Add(1)))
		},
	})
	var logSeq atomic.Int32
	router := httpapi.NewPublicAPIShell(httpapi.PublicAPIShellOptions{
		Config:           config.Config{TrustProxy: "false"},
		Authenticator:    publicapiauth.NewAuthenticator(publicapiauth.AuthenticatorOptions{Store: store, Now: w1bPublicRouteStrategiesShellNow}),
		RateLimiter:      limiter,
		LogClient:        logClient,
		EndpointHandlers: httpapi.NewPublicRouteStrategyHandlers(routeService),
		Now:              w1bPublicRouteStrategiesShellNow,
		NewLogID: func() string {
			return "publog_w1b_route_shell_" + strconv.Itoa(int(logSeq.Add(1)))
		},
	})

	addBody := `{"targetUsername":"admin","name":"公开策略","groupBindings":[{"groupId":"` + primary.Group.ID + `"}]}`
	addRec := serveW1bShellRequest(router, http.MethodPost, "/__aipublic__/route-strategy/add", token, "trace_route_add", addBody)
	if addRec.Code != http.StatusCreated {
		t.Fatalf("add status = %d, body = %s", addRec.Code, addRec.Body.String())
	}
	var addResponse struct {
		Data publicroutestrategies.RouteStrategyResponse `json:"data"`
	}
	if err := json.NewDecoder(addRec.Body).Decode(&addResponse); err != nil {
		t.Fatalf("decode add response: %v", err)
	}
	if addResponse.Data.Action != "created" || addResponse.Data.RouteStrategy == nil || addResponse.Data.RouteStrategy.Name != "公开策略" {
		t.Fatalf("add response = %+v", addResponse.Data)
	}
	routeID := addResponse.Data.RouteStrategy.ID

	listRec := serveW1bShellRequest(router, http.MethodGet, "/__aipublic__/route-strategy/list?targetUsername=admin&keyword=%E5%85%AC%E5%BC%80&page=1&pageSize=10", token, "trace_route_list", "")
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRec.Code, listRec.Body.String())
	}

	updateBody := `{"routeStrategyId":"` + routeID + `","mode":"failover","groupBindings":[{"groupId":"` + primary.Group.ID + `","priority":1,"weight":100},{"groupId":"` + backup.Group.ID + `","priority":2,"weight":50}]}`
	updateRec := serveW1bShellRequest(router, http.MethodPost, "/__aipublic__/route-strategy/update", token, "trace_route_update", updateBody)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updateRec.Code, updateRec.Body.String())
	}

	deleteRec := serveW1bShellRequest(router, http.MethodPost, "/__aipublic__/route-strategy/del", token, "trace_route_delete", `{"routeStrategyId":"`+routeID+`"}`)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}

	limitedRec := serveW1bShellRequest(router, http.MethodGet, "/__aipublic__/route-strategy/list?targetUsername=admin&page=1&pageSize=10", token, "trace_route_limited", "")
	if limitedRec.Code != http.StatusTooManyRequests {
		t.Fatalf("limited status = %d, body = %s", limitedRec.Code, limitedRec.Body.String())
	}
	if limitedRec.Header().Get("Retry-After") == "" {
		t.Fatalf("limited Retry-After is empty")
	}

	inspector := queue.NewInspector(redisOpts)
	defer closeInspector(t, inspector)
	if err := waitForPublicAPILogQueueDrained(ctx, inspector, workerDone, workerErr); err != nil {
		t.Fatal(err)
	}

	assertW1bShellRouteStrategyDeleted(t, ctx, db, routeID)
	assertW1bRouteShellLastUsed(t, ctx, db, w1bPublicRouteStrategiesShellNow())
	assertW1bRouteShellPublicAPILogs(t, ctx, db, token, routeID)
}

func w1bPublicRouteStrategiesShellNow() time.Time {
	return time.Date(2026, 7, 7, 11, 45, 0, 0, time.UTC)
}

func insertW1bRouteShellSourceAndToken(t *testing.T, ctx context.Context, db *sql.DB, token string, now time.Time) {
	t.Helper()

	scopes := `["` + publicapi.ScopeRouteStrategyListRead + `","` + publicapi.ScopeRouteStrategyAddWrite + `","` + publicapi.ScopeRouteStrategyUpdateWrite + `","` + publicapi.ScopeRouteStrategyDeleteWrite + `"]`
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.external_integration_sources (
			id, name, status, scopes_json, rate_limits_json, created_at, updated_at
		) VALUES ($1, $2, 'active', $3, $4, $5, $6)
	`, "extsrc_w1b_route_shell", "W1b Route Shell Source", scopes, `[{"windowSeconds":60,"maxRequests":4}]`, now, now)
	if err != nil {
		t.Fatalf("insert route shell external integration source: %v", err)
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO juhe_business.external_integration_source_tokens (
			id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix, status, scopes_json, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'active', $8, $9, $10)
	`, "exttok_w1b_route_shell", "extsrc_w1b_route_shell", "W1b Route Shell Token", publicapiauth.HashExternalSourceToken(token), "encrypted", "juis_w1b_route", "shell", scopes, now, now)
	if err != nil {
		t.Fatalf("insert route shell external integration token: %v", err)
	}
}

func assertW1bShellRouteStrategyDeleted(t *testing.T, ctx context.Context, db *sql.DB, routeID string) {
	t.Helper()

	var routeCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)::int
		FROM juhe_business.route_strategies
		WHERE id = $1
	`, routeID).Scan(&routeCount); err != nil {
		t.Fatalf("count deleted shell route strategy: %v", err)
	}
	if routeCount != 0 {
		t.Fatalf("deleted shell route strategy count = %d, want 0", routeCount)
	}
}

func assertW1bRouteShellLastUsed(t *testing.T, ctx context.Context, db *sql.DB, want time.Time) {
	t.Helper()

	assertTimestampEquals(t, db, "SELECT last_used_at FROM juhe_business.external_integration_sources WHERE id = $1", "extsrc_w1b_route_shell", want)
	assertTimestampEquals(t, db, "SELECT last_used_at FROM juhe_business.external_integration_source_tokens WHERE id = $1", "exttok_w1b_route_shell", want)
}

func assertW1bRouteShellPublicAPILogs(t *testing.T, ctx context.Context, db *sql.DB, token string, routeID string) {
	t.Helper()

	rows, err := db.QueryContext(ctx, `
		SELECT id, trace_id, source_ref_id, token_id, token_prefix, is_test_token, method, path, query_string,
		       client_ip, user_agent, status_code, success, request_capture_status, response_capture_status,
		       request_data_json, response_data_json, COALESCE(error_code, ''), COALESCE(error_message, '')
		FROM juhe_dataset.public_api_logs
		WHERE id LIKE 'publog_w1b_route_shell_%'
		ORDER BY id
	`)
	if err != nil {
		t.Fatalf("query route shell public api logs: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Fatalf("close route shell public api log rows: %v", err)
		}
	}()

	type publicAPILogRow struct {
		id                    string
		traceID               string
		sourceRefID           string
		tokenID               string
		tokenPrefix           string
		isTestToken           bool
		method                string
		path                  string
		queryString           string
		clientIP              string
		userAgent             string
		statusCode            int
		success               bool
		requestCaptureStatus  string
		responseCaptureStatus string
		requestJSON           string
		responseJSON          string
		errorCode             string
		errorMessage          string
	}
	var logs []publicAPILogRow
	for rows.Next() {
		var row publicAPILogRow
		if err := rows.Scan(
			&row.id,
			&row.traceID,
			&row.sourceRefID,
			&row.tokenID,
			&row.tokenPrefix,
			&row.isTestToken,
			&row.method,
			&row.path,
			&row.queryString,
			&row.clientIP,
			&row.userAgent,
			&row.statusCode,
			&row.success,
			&row.requestCaptureStatus,
			&row.responseCaptureStatus,
			&row.requestJSON,
			&row.responseJSON,
			&row.errorCode,
			&row.errorMessage,
		); err != nil {
			t.Fatalf("scan route shell public api log: %v", err)
		}
		logs = append(logs, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate route shell public api logs: %v", err)
	}
	if len(logs) != 5 {
		t.Fatalf("route shell public api log count = %d, want 5", len(logs))
	}

	expected := []struct {
		id          string
		traceID     string
		method      string
		path        string
		statusCode  int
		success     bool
		errorCode   string
		action      string
		bodyName    string
		queryTarget string
	}{
		{id: "publog_w1b_route_shell_1", traceID: "trace_route_add", method: http.MethodPost, path: "/__aipublic__/route-strategy/add", statusCode: http.StatusCreated, success: true, action: "created", bodyName: "公开策略"},
		{id: "publog_w1b_route_shell_2", traceID: "trace_route_list", method: http.MethodGet, path: "/__aipublic__/route-strategy/list", statusCode: http.StatusOK, success: true, queryTarget: "admin"},
		{id: "publog_w1b_route_shell_3", traceID: "trace_route_update", method: http.MethodPost, path: "/__aipublic__/route-strategy/update", statusCode: http.StatusOK, success: true, action: "updated"},
		{id: "publog_w1b_route_shell_4", traceID: "trace_route_delete", method: http.MethodPost, path: "/__aipublic__/route-strategy/del", statusCode: http.StatusOK, success: true, action: "deleted"},
		{id: "publog_w1b_route_shell_5", traceID: "trace_route_limited", method: http.MethodGet, path: "/__aipublic__/route-strategy/list", statusCode: http.StatusTooManyRequests, success: false, errorCode: "external_source_rate_limited", queryTarget: "admin"},
	}

	for index, want := range expected {
		row := logs[index]
		if row.id != want.id || row.traceID != want.traceID || row.method != want.method || row.path != want.path {
			t.Fatalf("log[%d] identity = %+v, want id/trace/method/path %s/%s/%s/%s", index, row, want.id, want.traceID, want.method, want.path)
		}
		if row.sourceRefID != "extsrc_w1b_route_shell" || row.tokenID != "exttok_w1b_route_shell" || row.tokenPrefix != "juis_w1b_route" || row.isTestToken {
			t.Fatalf("log[%d] source = %s/%s/%s test=%v", index, row.sourceRefID, row.tokenID, row.tokenPrefix, row.isTestToken)
		}
		if row.clientIP == "" || row.userAgent != "w1b-shell-e2e" {
			t.Fatalf("log[%d] client/user-agent = %q/%q", index, row.clientIP, row.userAgent)
		}
		if row.statusCode != want.statusCode || row.success != want.success || row.errorCode != want.errorCode {
			t.Fatalf("log[%d] status/success/error = %d/%v/%q, want %d/%v/%q", index, row.statusCode, row.success, row.errorCode, want.statusCode, want.success, want.errorCode)
		}
		if row.requestCaptureStatus != "complete" || row.responseCaptureStatus != "complete" {
			t.Fatalf("log[%d] capture status = %s/%s, want complete/complete", index, row.requestCaptureStatus, row.responseCaptureStatus)
		}
		requestData := decodeW1bShellLogJSON(t, row.requestJSON)
		responseData := decodeW1bShellLogJSON(t, row.responseJSON)
		if got := intFromW1bShellLog(responseData["statusCode"]); got != want.statusCode {
			t.Fatalf("log[%d] response statusCode = %d, want %d", index, got, want.statusCode)
		}
		if want.queryTarget != "" && nestedStringFromW1bShellLog(t, requestData, "query", "targetUsername") != want.queryTarget {
			t.Fatalf("log[%d] query targetUsername = %#v", index, requestData["query"])
		}
		if want.bodyName != "" && nestedStringFromW1bShellLog(t, requestData, "body", "name") != want.bodyName {
			t.Fatalf("log[%d] body name = %#v", index, requestData["body"])
		}
		if want.action != "" && nestedStringFromW1bShellLog(t, responseData, "body", "data", "action") != want.action {
			t.Fatalf("log[%d] response action = %#v", index, responseData["body"])
		}
		if want.action != "" && nestedStringFromW1bShellLog(t, responseData, "body", "data", "routeStrategy", "id") != routeID {
			t.Fatalf("log[%d] response route strategy id = %#v, want %s", index, responseData["body"], routeID)
		}
		if want.errorCode != "" {
			if nestedStringFromW1bShellLog(t, responseData, "body", "code") != want.errorCode {
				t.Fatalf("log[%d] response code = %#v, want %s", index, responseData["body"], want.errorCode)
			}
			if got := intFromW1bShellLog(nestedValueFromW1bShellLog(t, responseData, "body", "details", "maxRequests")); got != 4 {
				t.Fatalf("log[%d] maxRequests = %d, want 4", index, got)
			}
		}
		assertW1bShellLogNoSecret(t, row.requestJSON, row.responseJSON, row.errorMessage, token)
	}
}
