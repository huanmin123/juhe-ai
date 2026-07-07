//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
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

func TestW1bPublicAPIKeysShellE2E(t *testing.T) {
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

	secret := "sk-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var apiKeyIDSeq atomic.Int32
	apiKeyService := publicapikeys.NewService(publicapikeys.Options{
		Store:      store,
		Transactor: store,
		Now:        w1bPublicAPIKeysShellNow,
		NewID: func(prefix string) string {
			return prefix + "_w1b_api_key_shell_" + strconv.Itoa(int(apiKeyIDSeq.Add(1)))
		},
		NewSecret: fixedIntegrationSecret(secret),
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
	addRec := serveW1bShellRequest(router, http.MethodPost, "/__aipublic__/api-key/add", token, "trace_api_key_add", addBody)
	if addRec.Code != http.StatusCreated {
		t.Fatalf("add status = %d, body = %s", addRec.Code, addRec.Body.String())
	}
	var addResponse struct {
		Data publicapikeys.APIKeyResponse `json:"data"`
	}
	if err := json.NewDecoder(addRec.Body).Decode(&addResponse); err != nil {
		t.Fatalf("decode add response: %v", err)
	}
	if addResponse.Data.Action != "created" || addResponse.Data.APIKey == nil || addResponse.Data.APIKey.Key != secret {
		t.Fatalf("add response = %+v", addResponse.Data)
	}
	apiKeyID := addResponse.Data.APIKey.ID

	listRec := serveW1bShellRequest(router, http.MethodGet, "/__aipublic__/api-key/list?targetUsername=admin&keyword="+w1bAPIKeyShellSecretLikeKeyword+"&page=1&pageSize=10", token, "trace_api_key_list", "")
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRec.Code, listRec.Body.String())
	}

	updateRec := serveW1bShellRequest(router, http.MethodPost, "/__aipublic__/api-key/update", token, "trace_api_key_update", `{"apiKeyId":"`+apiKeyID+`","name":"公开 API Key 更新"}`)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updateRec.Code, updateRec.Body.String())
	}

	deleteRec := serveW1bShellRequest(router, http.MethodPost, "/__aipublic__/api-key/del", token, "trace_api_key_delete", `{"apiKeyId":"`+apiKeyID+`"}`)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}

	limitedRec := serveW1bShellRequest(router, http.MethodGet, "/__aipublic__/api-key/list?targetUsername=admin&page=1&pageSize=10", token, "trace_api_key_limited", "")
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
	assertW1bAPIKeyShellPublicAPILogs(t, ctx, db, token, secret, apiKeyID)
}

func w1bPublicAPIKeysShellNow() time.Time {
	return time.Date(2026, 7, 7, 10, 30, 0, 0, time.UTC)
}

const w1bAPIKeyShellSecretLikeKeyword = "sk-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

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

func assertW1bAPIKeyShellPublicAPILogs(t *testing.T, ctx context.Context, db *sql.DB, token string, secret string, apiKeyID string) {
	t.Helper()

	rows, err := db.QueryContext(ctx, `
		SELECT id, trace_id, source_ref_id, token_id, token_prefix, method, path, status_code, success,
		       query_string, request_capture_status, response_capture_status, request_data_json, response_data_json,
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
		id         string
		traceID    string
		method     string
		path       string
		statusCode int
		success    bool
		errorCode  string
		action     string
	}{
		{id: "publog_w1b_api_key_shell_1", traceID: "trace_api_key_add", method: http.MethodPost, path: "/__aipublic__/api-key/add", statusCode: http.StatusCreated, success: true, action: "created"},
		{id: "publog_w1b_api_key_shell_2", traceID: "trace_api_key_list", method: http.MethodGet, path: "/__aipublic__/api-key/list", statusCode: http.StatusOK, success: true},
		{id: "publog_w1b_api_key_shell_3", traceID: "trace_api_key_update", method: http.MethodPost, path: "/__aipublic__/api-key/update", statusCode: http.StatusOK, success: true, action: "updated"},
		{id: "publog_w1b_api_key_shell_4", traceID: "trace_api_key_delete", method: http.MethodPost, path: "/__aipublic__/api-key/del", statusCode: http.StatusOK, success: true, action: "deleted"},
		{id: "publog_w1b_api_key_shell_5", traceID: "trace_api_key_limited", method: http.MethodGet, path: "/__aipublic__/api-key/list", statusCode: http.StatusTooManyRequests, success: false, errorCode: "external_source_rate_limited"},
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
		if strings.Contains(strings.ToLower(row.queryString), strings.ToLower(secret)) ||
			strings.Contains(strings.ToLower(row.queryString), strings.ToLower(token)) ||
			strings.Contains(strings.ToLower(row.queryString), strings.ToLower(w1bAPIKeyShellSecretLikeKeyword)) {
			t.Fatalf("log[%d] query_string leaked secret: %s", index, row.queryString)
		}
		if want.traceID == "trace_api_key_list" && !strings.Contains(row.queryString, "keyword=[redacted]") {
			t.Fatalf("log[%d] query_string = %s, want redacted keyword", index, row.queryString)
		}
		if row.requestCaptureStatus != "complete" || row.responseCaptureStatus != "complete" {
			t.Fatalf("log[%d] capture status = %s/%s, want complete/complete", index, row.requestCaptureStatus, row.responseCaptureStatus)
		}
		requestData := decodeW1bShellLogJSON(t, row.requestJSON)
		responseData := decodeW1bShellLogJSON(t, row.responseJSON)
		if got := intFromW1bShellLog(responseData["statusCode"]); got != want.statusCode {
			t.Fatalf("log[%d] response statusCode = %d, want %d", index, got, want.statusCode)
		}
		if want.action != "" && nestedStringFromW1bShellLog(t, responseData, "body", "data", "action") != want.action {
			t.Fatalf("log[%d] response action = %#v", index, responseData["body"])
		}
		if want.action != "" && nestedStringFromW1bShellLog(t, responseData, "body", "data", "apiKey", "id") != apiKeyID {
			t.Fatalf("log[%d] response api key id = %#v, want %s", index, responseData["body"], apiKeyID)
		}
		if want.action == "created" && nestedStringFromW1bShellLog(t, responseData, "body", "data", "apiKey", "key") != "[redacted]" {
			t.Fatalf("log[%d] response api key secret should be redacted: %#v", index, responseData["body"])
		}
		if row.method == http.MethodPost && index < 4 {
			_ = nestedValueFromW1bShellLog(t, requestData, "body")
		}
		assertW1bShellLogNoSecret(t, row.requestJSON, row.responseJSON, row.errorMessage+"\n"+row.queryString, token)
		assertW1bShellLogNoAPIKeySecret(t, row.requestJSON, row.responseJSON, row.errorMessage+"\n"+row.queryString, secret)
		assertW1bShellLogNoAPIKeySecret(t, row.requestJSON, row.responseJSON, row.errorMessage+"\n"+row.queryString, w1bAPIKeyShellSecretLikeKeyword)
	}
}

func assertW1bShellLogNoAPIKeySecret(t *testing.T, requestJSON string, responseJSON string, errorMessage string, secret string) {
	t.Helper()

	combined := strings.ToLower(requestJSON + "\n" + responseJSON + "\n" + errorMessage)
	if strings.Contains(combined, strings.ToLower(secret)) {
		t.Fatalf("public api log leaked generated api key secret in %s", combined)
	}
}
