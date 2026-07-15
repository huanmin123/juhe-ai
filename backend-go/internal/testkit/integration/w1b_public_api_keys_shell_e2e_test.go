//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
	"juhe-ai/backend-go/internal/modules/publicapikeys"
	"juhe-ai/backend-go/internal/modules/publicgroups"
	"juhe-ai/backend-go/internal/modules/publicroutestrategies"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW1bPublicAPIKeysShellE2EPreservesRawPublicAPILogCapture(t *testing.T) {
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

	redisState, err := redisplatform.NewClient(redisURL, "w1b-public-api-key-shell-state")
	if err != nil {
		t.Fatalf("open redis state client: %v", err)
	}
	defer closeRedisClient(t, redisState)
	limiter, err := publicapiratelimit.NewLimiter(publicapiratelimit.Options{
		Client: redisState,
		Now:    w1bPublicAPIKeysShellNow,
	})
	if err != nil {
		t.Fatalf("new public api limiter: %v", err)
	}
	logClient := queue.NewClient(redisOpts)
	defer closeClient(t, logClient)

	token := "juis_w1b_api_key_shell"
	insertW1bAPIKeyShellSourceAndToken(t, ctx, db, token, w1bPublicAPIKeysShellNow())

	var groupIDSeq atomic.Int32
	groupService := publicgroups.NewService(publicgroups.Options{
		Store:      store,
		Transactor: store,
		Now:        w1bPublicAPIKeysShellNow,
		NewID: func(prefix string) string {
			return prefix + "_w1b_api_key_shell_" + strconv.Itoa(int(groupIDSeq.Add(1)))
		},
	})
	group, err := groupService.Add(ctx, publicgroups.AddInput{
		TargetUsername:    "admin",
		TargetDisplayName: "管理员",
		Name:              "API Key Shell 分组",
		ProviderCode:      "gpt",
	})
	if err != nil {
		t.Fatalf("seed group: %v", err)
	}

	var routeIDSeq atomic.Int32
	routeService := publicroutestrategies.NewService(publicroutestrategies.Options{
		Store:      store,
		Transactor: store,
		Now:        w1bPublicAPIKeysShellNow,
		NewID: func(prefix string) string {
			return prefix + "_w1b_api_key_shell_" + strconv.Itoa(int(routeIDSeq.Add(1)))
		},
	})
	route, err := routeService.Add(ctx, publicroutestrategies.AddInput{
		TargetUsername: "admin",
		Name:           "API Key Shell 策略",
		GroupBindings:  []publicroutestrategies.GroupBindingInput{{GroupID: group.Group.ID}},
	})
	if err != nil {
		t.Fatalf("seed route: %v", err)
	}

	generatedAPIKey := "sk-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var apiKeyIDSeq atomic.Int32
	apiKeyService := publicapikeys.NewService(publicapikeys.Options{
		Store:      store,
		Transactor: store,
		Now:        w1bPublicAPIKeysShellNow,
		NewID: func(prefix string) string {
			return prefix + "_w1b_api_key_shell_" + strconv.Itoa(int(apiKeyIDSeq.Add(1)))
		},
		NewSecret: fixedIntegrationSecret(generatedAPIKey),
	})
	var logSeq atomic.Int32
	router := httpapi.NewPublicAPIShell(httpapi.PublicAPIShellOptions{
		Config:           config.Config{TrustProxy: "false"},
		Authenticator:    publicapiauth.NewAuthenticator(publicapiauth.AuthenticatorOptions{Store: store, Now: w1bPublicAPIKeysShellNow}),
		RateLimiter:      limiter,
		LogClient:        logClient,
		EndpointHandlers: httpapi.NewPublicAPIKeyHandlers(apiKeyService),
		Now:              w1bPublicAPIKeysShellNow,
		NewLogID: func() string {
			return "publog_w1b_api_key_shell_" + strconv.Itoa(int(logSeq.Add(1)))
		},
	})

	addBody := `{"targetUsername":"admin","name":"公开 API Key","routeStrategyId":"` + route.RouteStrategy.ID + `","quotaLimits":{"daily":{"enabled":true,"limit":100}}}`
	addRec := serveW1bShellRawCaptureRequest(router, http.MethodPost, "/__aipublic__/api-key/add", token, "trace_api_key_add", addBody)
	if addRec.Code != http.StatusCreated {
		t.Fatalf("add status = %d, body = %s", addRec.Code, addRec.Body.String())
	}
	var addResponse struct {
		Data publicapikeys.APIKeyResponse `json:"data"`
	}
	if err := json.NewDecoder(addRec.Body).Decode(&addResponse); err != nil {
		t.Fatalf("decode add response: %v", err)
	}
	if addResponse.Data.Action != "created" || addResponse.Data.APIKey == nil || addResponse.Data.APIKey.Key != generatedAPIKey {
		t.Fatalf("add response = %+v", addResponse.Data)
	}
	apiKeyID := addResponse.Data.APIKey.ID

	listRec := serveW1bShellRawCaptureRequest(router, http.MethodGet, "/__aipublic__/api-key/list?targetUsername=admin&keyword="+w1bAPIKeyShellRawKeyword+"&page=1&pageSize=10", token, "trace_api_key_list", "")
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRec.Code, listRec.Body.String())
	}

	updateRec := serveW1bShellRawCaptureRequest(router, http.MethodPost, "/__aipublic__/api-key/update", token, "trace_api_key_update", `{"apiKeyId":"`+apiKeyID+`","name":"公开 API Key 更新"}`)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updateRec.Code, updateRec.Body.String())
	}

	deleteRec := serveW1bShellRawCaptureRequest(router, http.MethodPost, "/__aipublic__/api-key/del", token, "trace_api_key_delete", `{"apiKeyId":"`+apiKeyID+`"}`)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}

	limitedRec := serveW1bShellRawCaptureRequest(router, http.MethodGet, "/__aipublic__/api-key/list?targetUsername=admin&page=1&pageSize=10", token, "trace_api_key_limited", "")
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

	assertW1bShellAPIKeyDeleted(t, ctx, db, apiKeyID)
	assertW1bAPIKeyShellLastUsed(t, ctx, db, w1bPublicAPIKeysShellNow())
	assertW1bAPIKeyShellPublicAPILogsPreserveRawValues(t, ctx, db, token, generatedAPIKey, apiKeyID, route.RouteStrategy.ID)
}

func w1bPublicAPIKeysShellNow() time.Time {
	return time.Date(2026, 7, 7, 10, 30, 0, 0, time.UTC)
}

const (
	w1bAPIKeyShellRawKeyword    = "sk-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	w1bShellCaptureCookieSecret = "w1b-shell-cookie-secret"
	w1bShellCaptureCookie       = "juhe_ai_session=" + w1bShellCaptureCookieSecret
)

func serveW1bShellRawCaptureRequest(router http.Handler, method string, target string, token string, traceID string, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Cookie", w1bShellCaptureCookie)
	req.Header.Set("X-Request-Id", traceID)
	req.Header.Set("User-Agent", "w1b-shell-e2e")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func insertW1bAPIKeyShellSourceAndToken(t *testing.T, ctx context.Context, db *sql.DB, token string, now time.Time) {
	t.Helper()

	scopes := `["` + publicapi.ScopeAPIKeyListRead + `","` + publicapi.ScopeAPIKeyAddWrite + `","` + publicapi.ScopeAPIKeyUpdateWrite + `","` + publicapi.ScopeAPIKeyDeleteWrite + `"]`
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.external_integration_sources (
			id, name, status, scopes_json, rate_limits_json, created_at, updated_at
		) VALUES ($1, $2, 'active', $3, $4, $5, $6)
	`, "extsrc_w1b_api_key_shell", "W1b API Key Shell Source", scopes, `[{"windowSeconds":60,"maxRequests":4}]`, now, now)
	if err != nil {
		t.Fatalf("insert api key shell external integration source: %v", err)
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO juhe_business.external_integration_source_tokens (
			id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix, status, scopes_json, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'active', $8, $9, $10)
	`, "exttok_w1b_api_key_shell", "extsrc_w1b_api_key_shell", "W1b API Key Shell Token", publicapiauth.HashExternalSourceToken(token), "encrypted", "juis_w1b_api_key", "shell", scopes, now, now)
	if err != nil {
		t.Fatalf("insert api key shell external integration token: %v", err)
	}
}

func assertW1bShellAPIKeyDeleted(t *testing.T, ctx context.Context, db *sql.DB, apiKeyID string) {
	t.Helper()

	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)::int
		FROM juhe_business.api_keys
		WHERE id = $1
	`, apiKeyID).Scan(&count); err != nil {
		t.Fatalf("count deleted shell api key: %v", err)
	}
	if count != 0 {
		t.Fatalf("deleted shell api key count = %d, want 0", count)
	}
}

func assertW1bAPIKeyShellLastUsed(t *testing.T, ctx context.Context, db *sql.DB, want time.Time) {
	t.Helper()

	assertTimestampEquals(t, db, "SELECT last_used_at FROM juhe_business.external_integration_sources WHERE id = $1", "extsrc_w1b_api_key_shell", want)
	assertTimestampEquals(t, db, "SELECT last_used_at FROM juhe_business.external_integration_source_tokens WHERE id = $1", "exttok_w1b_api_key_shell", want)
}

func assertW1bAPIKeyShellPublicAPILogsPreserveRawValues(t *testing.T, ctx context.Context, db *sql.DB, token string, generatedAPIKey string, apiKeyID string, routeStrategyID string) {
	t.Helper()

	rows, err := db.QueryContext(ctx, `
		SELECT id, COALESCE(trace_id, ''), COALESCE(source_ref_id, ''), COALESCE(token_id, ''),
		       COALESCE(token_prefix, ''), method, path, COALESCE(status_code, 0), success,
		       COALESCE(query_string, ''), request_capture_status, response_capture_status, request_data_json, response_data_json,
		       COALESCE(error_code, ''), COALESCE(error_message, '')
		FROM juhe_dataset.public_api_logs
		WHERE id LIKE 'publog_w1b_api_key_shell_%'
		ORDER BY id
	`)
	if err != nil {
		t.Fatalf("query api key shell public api logs: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Fatalf("close api key shell public api log rows: %v", err)
		}
	}()

	type logRow struct {
		id                    string
		traceID               string
		sourceRefID           string
		tokenID               string
		tokenPrefix           string
		method                string
		path                  string
		statusCode            int
		success               bool
		queryString           string
		requestCaptureStatus  string
		responseCaptureStatus string
		requestJSON           string
		responseJSON          string
		errorCode             string
		errorMessage          string
	}
	var logs []logRow
	for rows.Next() {
		var row logRow
		if err := rows.Scan(
			&row.id,
			&row.traceID,
			&row.sourceRefID,
			&row.tokenID,
			&row.tokenPrefix,
			&row.method,
			&row.path,
			&row.statusCode,
			&row.success,
			&row.queryString,
			&row.requestCaptureStatus,
			&row.responseCaptureStatus,
			&row.requestJSON,
			&row.responseJSON,
			&row.errorCode,
			&row.errorMessage,
		); err != nil {
			t.Fatalf("scan api key shell public api log: %v", err)
		}
		logs = append(logs, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate api key shell public api logs: %v", err)
	}
	if len(logs) != 5 {
		t.Fatalf("api key shell public api log count = %d, want 5", len(logs))
	}

	expected := []struct {
		id                     string
		traceID                string
		method                 string
		path                   string
		queryString            string
		statusCode             int
		success                bool
		errorCode              string
		action                 string
		requestAPIKeyID        string
		requestName            string
		requestRouteStrategyID string
	}{
		{id: "publog_w1b_api_key_shell_1", traceID: "trace_api_key_add", method: http.MethodPost, path: "/__aipublic__/api-key/add", statusCode: http.StatusCreated, success: true, action: "created", requestName: "公开 API Key", requestRouteStrategyID: routeStrategyID},
		{id: "publog_w1b_api_key_shell_2", traceID: "trace_api_key_list", method: http.MethodGet, path: "/__aipublic__/api-key/list", queryString: "targetUsername=admin&keyword=" + w1bAPIKeyShellRawKeyword + "&page=1&pageSize=10", statusCode: http.StatusOK, success: true},
		{id: "publog_w1b_api_key_shell_3", traceID: "trace_api_key_update", method: http.MethodPost, path: "/__aipublic__/api-key/update", statusCode: http.StatusOK, success: true, action: "updated", requestAPIKeyID: apiKeyID, requestName: "公开 API Key 更新"},
		{id: "publog_w1b_api_key_shell_4", traceID: "trace_api_key_delete", method: http.MethodPost, path: "/__aipublic__/api-key/del", statusCode: http.StatusOK, success: true, action: "deleted", requestAPIKeyID: apiKeyID},
		{id: "publog_w1b_api_key_shell_5", traceID: "trace_api_key_limited", method: http.MethodGet, path: "/__aipublic__/api-key/list", queryString: "targetUsername=admin&page=1&pageSize=10", statusCode: http.StatusTooManyRequests, success: false, errorCode: "external_source_rate_limited"},
	}

	for index, want := range expected {
		row := logs[index]
		if row.id != want.id || row.traceID != want.traceID || row.method != want.method || row.path != want.path {
			t.Fatalf("log[%d] identity = %+v, want %s/%s/%s/%s", index, row, want.id, want.traceID, want.method, want.path)
		}
		if row.sourceRefID != "extsrc_w1b_api_key_shell" || row.tokenID != "exttok_w1b_api_key_shell" || row.tokenPrefix != "juis_w1b_api_key" {
			t.Fatalf("log[%d] source = %s/%s/%s", index, row.sourceRefID, row.tokenID, row.tokenPrefix)
		}
		if row.statusCode != want.statusCode || row.success != want.success || row.errorCode != want.errorCode {
			t.Fatalf("log[%d] status/success/error = %d/%v/%q, want %d/%v/%q", index, row.statusCode, row.success, row.errorCode, want.statusCode, want.success, want.errorCode)
		}
		if row.queryString != want.queryString {
			t.Fatalf("log[%d] query_string = %q, want original %q", index, row.queryString, want.queryString)
		}
		if row.requestCaptureStatus != "complete" || row.responseCaptureStatus != "complete" {
			t.Fatalf("log[%d] capture status = %s/%s, want complete/complete", index, row.requestCaptureStatus, row.responseCaptureStatus)
		}
		requestData := decodeW1bShellLogJSON(t, row.requestJSON)
		responseData := decodeW1bShellLogJSON(t, row.responseJSON)
		assertW1bShellSnapshotsExcludeUncapturedCredentials(t, index, requestData, row.requestJSON, row.responseJSON, token)
		if want.traceID == "trace_api_key_list" && nestedStringFromW1bShellLog(t, requestData, "query", "keyword") != w1bAPIKeyShellRawKeyword {
			t.Fatalf("log[%d] query keyword = %#v, want original %q", index, requestData["query"], w1bAPIKeyShellRawKeyword)
		}
		if want.requestAPIKeyID != "" && nestedStringFromW1bShellLog(t, requestData, "body", "apiKeyId") != want.requestAPIKeyID {
			t.Fatalf("log[%d] request apiKeyId = %#v, want original %q", index, requestData["body"], want.requestAPIKeyID)
		}
		if want.requestName != "" && nestedStringFromW1bShellLog(t, requestData, "body", "name") != want.requestName {
			t.Fatalf("log[%d] request name = %#v, want original %q", index, requestData["body"], want.requestName)
		}
		if want.requestRouteStrategyID != "" && nestedStringFromW1bShellLog(t, requestData, "body", "routeStrategyId") != want.requestRouteStrategyID {
			t.Fatalf("log[%d] request routeStrategyId = %#v, want original %q", index, requestData["body"], want.requestRouteStrategyID)
		}
		if got := intFromW1bShellLog(responseData["statusCode"]); got != want.statusCode {
			t.Fatalf("log[%d] response statusCode = %d, want %d", index, got, want.statusCode)
		}
		if want.action != "" && nestedStringFromW1bShellLog(t, responseData, "body", "data", "action") != want.action {
			t.Fatalf("log[%d] response action = %#v", index, responseData["body"])
		}
		if want.action != "" && nestedStringFromW1bShellLog(t, responseData, "body", "data", "apiKey", "id") != apiKeyID {
			t.Fatalf("log[%d] response api key id = %#v, want %s", index, responseData["body"], apiKeyID)
		}
		if want.action == "created" && nestedStringFromW1bShellLog(t, responseData, "body", "data", "apiKey", "key") != generatedAPIKey {
			t.Fatalf("log[%d] response api key = %#v, want original generated key", index, responseData["body"])
		}
	}
}

func assertW1bShellSnapshotsExcludeUncapturedCredentials(t *testing.T, index int, requestData map[string]any, requestJSON string, responseJSON string, sourceToken string) {
	t.Helper()

	headersValue := nestedValueFromW1bShellLog(t, requestData, "headers")
	headers, ok := headersValue.(map[string]any)
	if !ok {
		t.Fatalf("log[%d] request headers = %#v, want captured header map", index, headersValue)
	}
	for key := range headers {
		if strings.EqualFold(key, "authorization") || strings.EqualFold(key, "cookie") {
			t.Fatalf("log[%d] request snapshot captured excluded credential header %q: %#v", index, key, headers)
		}
	}
	combinedSnapshots := strings.ToLower(requestJSON + "\n" + responseJSON)
	for _, credential := range []struct {
		label string
		value string
	}{
		{label: "source bearer token", value: sourceToken},
		{label: "cookie value", value: w1bShellCaptureCookieSecret},
	} {
		if strings.Contains(combinedSnapshots, strings.ToLower(credential.value)) {
			t.Fatalf("log[%d] snapshots included uncaptured %s", index, credential.label)
		}
	}
}
