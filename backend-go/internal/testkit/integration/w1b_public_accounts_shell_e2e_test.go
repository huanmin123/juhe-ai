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
	"juhe-ai/backend-go/internal/modules/publicaccounts"
	"juhe-ai/backend-go/internal/modules/publicapi"
	publicapiauth "juhe-ai/backend-go/internal/modules/publicapi/auth"
	publicapiratelimit "juhe-ai/backend-go/internal/modules/publicapi/ratelimit"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW1bPublicAccountsShellE2E(t *testing.T) {
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

	redisState, err := redisplatform.NewClient(redisURL, "w1b-public-account-shell-state")
	if err != nil {
		t.Fatalf("open redis state client: %v", err)
	}
	defer closeRedisClient(t, redisState)
	limiter, err := publicapiratelimit.NewLimiter(publicapiratelimit.Options{
		Client: redisState,
		Now:    w1bPublicAccountsShellNow,
	})
	if err != nil {
		t.Fatalf("new public api limiter: %v", err)
	}
	logClient := queue.NewClient(redisOpts)
	defer closeClient(t, logClient)

	token := "juis_w1b_account_shell"
	insertW1bAccountShellSourceAndToken(t, ctx, db, token, w1bPublicAccountsShellNow())

	accountCredentialSecret := "w1b-public-account-shell-credential-secret"
	var accountIDSeq atomic.Int32
	accountService := publicaccounts.NewService(publicaccounts.Options{
		Store:      store,
		Transactor: store,
		Now:        w1bPublicAccountsShellNow,
		NewID: func(prefix string) string {
			return prefix + "_w1b_account_shell_" + strconv.Itoa(int(accountIDSeq.Add(1)))
		},
		Secret: accountCredentialSecret,
	})
	var logSeq atomic.Int32
	router := httpapi.NewPublicAPIShell(httpapi.PublicAPIShellOptions{
		Config:           config.Config{TrustProxy: "false"},
		Authenticator:    publicapiauth.NewAuthenticator(publicapiauth.AuthenticatorOptions{Store: store, Now: w1bPublicAccountsShellNow}),
		RateLimiter:      limiter,
		LogClient:        logClient,
		EndpointHandlers: httpapi.NewPublicAccountHandlers(accountService),
		Now:              w1bPublicAccountsShellNow,
		NewLogID: func() string {
			return "publog_w1b_account_shell_" + strconv.Itoa(int(logSeq.Add(1)))
		},
	})

	initialSecret := "sk-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	addBody := `{"targetUsername":"admin","targetDisplayName":"管理员","targetGroupName":"账号分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"` + initialSecret + `","status":"active"}`
	addRec := serveW1bShellRequest(router, http.MethodPost, "/__aipublic__/account/add", token, "trace_account_add", addBody)
	if addRec.Code != http.StatusCreated {
		t.Fatalf("add status = %d, body = %s", addRec.Code, addRec.Body.String())
	}
	assertW1bPublicAccountResponseNoSecret(t, addRec.Body.String(), initialSecret)
	var addResponse struct {
		Data publicaccounts.AccountResponse `json:"data"`
	}
	if err := json.Unmarshal(addRec.Body.Bytes(), &addResponse); err != nil {
		t.Fatalf("decode add response: %v", err)
	}
	if addResponse.Data.Action != "created" || addResponse.Data.Account == nil || addResponse.Data.Account.Status != publicaccounts.StatusPendingTest || addResponse.Data.Account.Schedulable {
		t.Fatalf("add response = %+v", addResponse.Data)
	}
	assertW1bPublicAccountModelList(t, addResponse.Data.Account.SupportedModels, w1bGPTDefaultSupportedModels)
	accountID := addResponse.Data.Account.ID
	assertW1bPublicAccountStored(t, ctx, db, accountID, initialSecret, publicaccounts.StatusPendingTest, false)
	assertW1bPublicAccountModels(t, ctx, db, accountID, w1bGPTDefaultSupportedModels)

	emptyModelsAccountName := "空模型账号"
	emptyModelsBody := `{"targetUsername":"admin","targetGroupName":"账号分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"` + emptyModelsAccountName + `","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"` + initialSecret + `","supportedModels":[]}`
	emptyModelsRec := serveW1bShellRequest(router, http.MethodPost, "/__aipublic__/account/add", token, "trace_account_add_empty_models", emptyModelsBody)
	if emptyModelsRec.Code != http.StatusBadRequest {
		t.Fatalf("explicit empty supportedModels status = %d, body = %s", emptyModelsRec.Code, emptyModelsRec.Body.String())
	}
	assertW1bPublicAccountResponseNoSecret(t, emptyModelsRec.Body.String(), initialSecret)
	var emptyModelsResponse struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(emptyModelsRec.Body.Bytes(), &emptyModelsResponse); err != nil {
		t.Fatalf("decode explicit empty supportedModels response: %v", err)
	}
	if emptyModelsResponse.Message != w1bInvalidSupportedModelsMessage {
		t.Fatalf("explicit empty supportedModels message = %q, want %q", emptyModelsResponse.Message, w1bInvalidSupportedModelsMessage)
	}
	assertW1bPublicAccountNameCount(t, ctx, db, emptyModelsAccountName, 0)

	duplicateEmptyModelsBody := `{"targetUsername":"admin","targetGroupName":"账号分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号","type":"api_key","baseUrl":"https://api.openai.com/v1","apiKey":"` + initialSecret + `","supportedModels":[]}`
	duplicateEmptyModelsRec := serveW1bShellRequest(router, http.MethodPost, "/__aipublic__/account/add", token, "trace_account_add_duplicate_empty_models", duplicateEmptyModelsBody)
	if duplicateEmptyModelsRec.Code != http.StatusConflict {
		t.Fatalf("duplicate add with explicit empty supportedModels status = %d, body = %s", duplicateEmptyModelsRec.Code, duplicateEmptyModelsRec.Body.String())
	}
	assertW1bPublicAccountResponseNoSecret(t, duplicateEmptyModelsRec.Body.String(), initialSecret)
	var duplicateEmptyModelsResponse struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(duplicateEmptyModelsRec.Body.Bytes(), &duplicateEmptyModelsResponse); err != nil {
		t.Fatalf("decode duplicate add with explicit empty supportedModels response: %v", err)
	}
	if duplicateEmptyModelsResponse.Message != w1bDuplicateAccountNameMessage {
		t.Fatalf("duplicate add with explicit empty supportedModels message = %q, want %q", duplicateEmptyModelsResponse.Message, w1bDuplicateAccountNameMessage)
	}
	assertW1bPublicAccountNameCount(t, ctx, db, "公开账号", 1)

	listRec := serveW1bShellRequest(router, http.MethodGet, "/__aipublic__/account/list?targetUsername=admin&targetGroupName=%E8%B4%A6%E5%8F%B7%E5%88%86%E7%BB%84&providerCode=gpt&providerProtocolProfileId=profile_gpt_openai_v1&keyword="+w1bAccountShellSecretLikeKeyword+"&page=1&pageSize=10", token, "trace_account_list", "")
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	assertW1bPublicAccountResponseNoSecret(t, listRec.Body.String(), initialSecret)

	updatedSecret := "sk-fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	userInfoURL := "https://user:pass@example.com/private?token=notes-secret-value"
	updateBody := `{"accountId":"` + accountID + `","targetUsername":"admin","targetGroupName":"账号分组","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"公开账号更新","status":"disabled","baseUrl":"https://api.openai.com/v2","apiKey":"` + updatedSecret + `","supportedModels":["gpt-5.5-codex"],"concurrencyLimit":7,"priority":3,"notes":"` + userInfoURL + `"}`
	updateRec := serveW1bShellRequest(router, http.MethodPost, "/__aipublic__/account/update", token, "trace_account_update", updateBody)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updateRec.Code, updateRec.Body.String())
	}
	assertW1bPublicAccountResponseNoSecret(t, updateRec.Body.String(), updatedSecret)
	var updateResponse struct {
		Data publicaccounts.AccountResponse `json:"data"`
	}
	if err := json.Unmarshal(updateRec.Body.Bytes(), &updateResponse); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updateResponse.Data.Action != "updated" || updateResponse.Data.Account == nil || updateResponse.Data.Account.Name != "公开账号更新" || updateResponse.Data.Account.Status != publicaccounts.StatusDisabled || updateResponse.Data.Account.Schedulable {
		t.Fatalf("update response = %+v", updateResponse.Data)
	}
	assertW1bPublicAccountStored(t, ctx, db, accountID, updatedSecret, publicaccounts.StatusDisabled, false)
	assertW1bPublicAccountModels(t, ctx, db, accountID, []string{"gpt-5.5-codex"})

	deleteRec := serveW1bShellRequest(router, http.MethodPost, "/__aipublic__/account/del", token, "trace_account_delete", `{"accountId":"`+accountID+`","targetUsername":"admin","providerCode":"gpt","providerProtocolProfileId":"profile_gpt_openai_v1"}`)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}
	assertW1bPublicAccountResponseNoSecret(t, deleteRec.Body.String(), updatedSecret)

	limitedRec := serveW1bShellRequest(router, http.MethodGet, "/__aipublic__/account/list?targetUsername=admin&page=1&pageSize=10", token, "trace_account_limited", "")
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

	assertW1bPublicAccountSoftDeleted(t, ctx, db, accountID)
	assertW1bAccountShellLastUsed(t, ctx, db, w1bPublicAccountsShellNow())
	assertW1bAccountShellPublicAPILogs(t, ctx, db, token, initialSecret, updatedSecret, userInfoURL, accountID)
}

func w1bPublicAccountsShellNow() time.Time {
	return time.Date(2026, 7, 7, 12, 30, 0, 0, time.UTC)
}

const w1bAccountShellSecretLikeKeyword = "sk-cccccccccccccccccccccccccccccccc"

func insertW1bAccountShellSourceAndToken(t *testing.T, ctx context.Context, db *sql.DB, token string, now time.Time) {
	t.Helper()

	scopes := `["` + publicapi.ScopeAccountListRead + `","` + publicapi.ScopeAccountAddWrite + `","` + publicapi.ScopeAccountUpdateWrite + `","` + publicapi.ScopeAccountDeleteWrite + `"]`
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.external_integration_sources (
			id, name, status, scopes_json, rate_limits_json, created_at, updated_at
		) VALUES ($1, $2, 'active', $3, $4, $5, $6)
	`, "extsrc_w1b_account_shell", "W1b Account Shell Source", scopes, `[{"windowSeconds":60,"maxRequests":6}]`, now, now)
	if err != nil {
		t.Fatalf("insert account shell external integration source: %v", err)
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO juhe_business.external_integration_source_tokens (
			id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix, status, scopes_json, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'active', $8, $9, $10)
	`, "exttok_w1b_account_shell", "extsrc_w1b_account_shell", "W1b Account Shell Token", publicapiauth.HashExternalSourceToken(token), "encrypted", "juis_w1b_account", "shell", scopes, now, now)
	if err != nil {
		t.Fatalf("insert account shell external integration token: %v", err)
	}
}

func assertW1bAccountShellLastUsed(t *testing.T, ctx context.Context, db *sql.DB, want time.Time) {
	t.Helper()

	assertTimestampEquals(t, db, "SELECT last_used_at FROM juhe_business.external_integration_sources WHERE id = $1", "extsrc_w1b_account_shell", want)
	assertTimestampEquals(t, db, "SELECT last_used_at FROM juhe_business.external_integration_source_tokens WHERE id = $1", "exttok_w1b_account_shell", want)
}

func assertW1bAccountShellPublicAPILogs(t *testing.T, ctx context.Context, db *sql.DB, token string, initialSecret string, updatedSecret string, userInfoURL string, accountID string) {
	t.Helper()

	rows, err := db.QueryContext(ctx, `
		SELECT id, trace_id, source_ref_id, token_id, token_prefix, is_test_token,
		       method, path, COALESCE(query_string, ''), client_ip, user_agent, status_code, success,
		       request_capture_status, response_capture_status, request_data_json, response_data_json,
		       COALESCE(error_code, ''), COALESCE(error_message, '')
		FROM juhe_dataset.public_api_logs
		WHERE id LIKE 'publog_w1b_account_shell_%'
		ORDER BY id
	`)
	if err != nil {
		t.Fatalf("query account shell public api logs: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Fatalf("close account shell public api log rows: %v", err)
		}
	}()

	type logRow struct {
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
	var logs []logRow
	for rows.Next() {
		var row logRow
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
			t.Fatalf("scan account shell public api log: %v", err)
		}
		logs = append(logs, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate account shell public api logs: %v", err)
	}
	if len(logs) != 7 {
		t.Fatalf("account shell public api log count = %d, want 7", len(logs))
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
		message    string
	}{
		{id: "publog_w1b_account_shell_1", traceID: "trace_account_add", method: http.MethodPost, path: "/__aipublic__/account/add", statusCode: http.StatusCreated, success: true, action: "created"},
		{id: "publog_w1b_account_shell_2", traceID: "trace_account_add_empty_models", method: http.MethodPost, path: "/__aipublic__/account/add", statusCode: http.StatusBadRequest, success: false, message: w1bInvalidSupportedModelsMessage},
		{id: "publog_w1b_account_shell_3", traceID: "trace_account_add_duplicate_empty_models", method: http.MethodPost, path: "/__aipublic__/account/add", statusCode: http.StatusConflict, success: false, message: w1bDuplicateAccountNameMessage},
		{id: "publog_w1b_account_shell_4", traceID: "trace_account_list", method: http.MethodGet, path: "/__aipublic__/account/list", statusCode: http.StatusOK, success: true},
		{id: "publog_w1b_account_shell_5", traceID: "trace_account_update", method: http.MethodPost, path: "/__aipublic__/account/update", statusCode: http.StatusOK, success: true, action: "updated"},
		{id: "publog_w1b_account_shell_6", traceID: "trace_account_delete", method: http.MethodPost, path: "/__aipublic__/account/del", statusCode: http.StatusOK, success: true, action: "deleted"},
		{id: "publog_w1b_account_shell_7", traceID: "trace_account_limited", method: http.MethodGet, path: "/__aipublic__/account/list", statusCode: http.StatusTooManyRequests, success: false, errorCode: "external_source_rate_limited"},
	}

	for index, want := range expected {
		row := logs[index]
		if row.id != want.id || row.traceID != want.traceID || row.method != want.method || row.path != want.path {
			t.Fatalf("log[%d] identity = %+v, want %s/%s/%s/%s", index, row, want.id, want.traceID, want.method, want.path)
		}
		if row.sourceRefID != "extsrc_w1b_account_shell" || row.tokenID != "exttok_w1b_account_shell" || row.tokenPrefix != "juis_w1b_account" || row.isTestToken {
			t.Fatalf("log[%d] source = %s/%s/%s test=%v", index, row.sourceRefID, row.tokenID, row.tokenPrefix, row.isTestToken)
		}
		if row.clientIP == "" || row.userAgent != "w1b-shell-e2e" {
			t.Fatalf("log[%d] client/user-agent = %q/%q", index, row.clientIP, row.userAgent)
		}
		if row.statusCode != want.statusCode || row.success != want.success || row.errorCode != want.errorCode {
			t.Fatalf("log[%d] status/success/error = %d/%v/%q, want %d/%v/%q", index, row.statusCode, row.success, row.errorCode, want.statusCode, want.success, want.errorCode)
		}
		for _, secret := range []string{token, initialSecret, updatedSecret, w1bAccountShellSecretLikeKeyword, userInfoURL, "user:pass", "notes-secret-value"} {
			if strings.Contains(strings.ToLower(row.queryString), strings.ToLower(secret)) {
				t.Fatalf("log[%d] query_string leaked secret %q: %s", index, secret, row.queryString)
			}
		}
		if row.requestCaptureStatus != "complete" || row.responseCaptureStatus != "complete" {
			t.Fatalf("log[%d] capture status = %s/%s, want complete/complete", index, row.requestCaptureStatus, row.responseCaptureStatus)
		}
		requestData := decodeW1bShellLogJSON(t, row.requestJSON)
		responseData := decodeW1bShellLogJSON(t, row.responseJSON)
		if got := intFromW1bShellLog(responseData["statusCode"]); got != want.statusCode {
			t.Fatalf("log[%d] response statusCode = %d, want %d", index, got, want.statusCode)
		}
		if want.traceID == "trace_account_list" {
			if !strings.Contains(row.queryString, "keyword=[redacted]") {
				t.Fatalf("log[%d] query_string = %s, want redacted keyword", index, row.queryString)
			}
			if nestedStringFromW1bShellLog(t, requestData, "query", "keyword") != "[redacted]" {
				t.Fatalf("log[%d] query keyword = %#v, want redacted", index, requestData["query"])
			}
		}
		if want.traceID == "trace_account_add" || want.traceID == "trace_account_add_empty_models" || want.traceID == "trace_account_add_duplicate_empty_models" {
			if nestedStringFromW1bShellLog(t, requestData, "body", "apiKey") != "[redacted]" {
				t.Fatalf("log[%d] add apiKey should be redacted: %#v", index, requestData["body"])
			}
			if nestedStringFromW1bShellLog(t, requestData, "body", "baseUrl") != "[redacted]" {
				t.Fatalf("log[%d] add baseUrl should be redacted: %#v", index, requestData["body"])
			}
		}
		if want.traceID == "trace_account_update" {
			if nestedStringFromW1bShellLog(t, requestData, "body", "apiKey") != "[redacted]" {
				t.Fatalf("log[%d] update apiKey should be redacted: %#v", index, requestData["body"])
			}
			if nestedStringFromW1bShellLog(t, requestData, "body", "baseUrl") != "[redacted]" {
				t.Fatalf("log[%d] update baseUrl should be redacted: %#v", index, requestData["body"])
			}
			if nestedStringFromW1bShellLog(t, requestData, "body", "notes") != "[redacted]" {
				t.Fatalf("log[%d] URL userinfo note should be redacted: %#v", index, requestData["body"])
			}
		}
		if want.action != "" && nestedStringFromW1bShellLog(t, responseData, "body", "data", "action") != want.action {
			t.Fatalf("log[%d] response action = %#v", index, responseData["body"])
		}
		if want.action != "" && nestedStringFromW1bShellLog(t, responseData, "body", "data", "account", "id") != accountID {
			t.Fatalf("log[%d] response account id = %#v, want %s", index, responseData["body"], accountID)
		}
		if want.message != "" && nestedStringFromW1bShellLog(t, responseData, "body", "message") != want.message {
			t.Fatalf("log[%d] response message = %#v, want %q", index, responseData["body"], want.message)
		}
		if want.errorCode != "" {
			if nestedStringFromW1bShellLog(t, responseData, "body", "code") != want.errorCode {
				t.Fatalf("log[%d] response code = %#v, want %s", index, responseData["body"], want.errorCode)
			}
			if got := intFromW1bShellLog(nestedValueFromW1bShellLog(t, responseData, "body", "details", "maxRequests")); got != 6 {
				t.Fatalf("log[%d] maxRequests = %d, want 6", index, got)
			}
		}
		assertW1bShellLogNoSecret(t, row.requestJSON, row.responseJSON, row.errorMessage+"\n"+row.queryString, token)
		assertW1bShellLogNoAPIKeySecret(t, row.requestJSON, row.responseJSON, row.errorMessage+"\n"+row.queryString, initialSecret)
		assertW1bShellLogNoAPIKeySecret(t, row.requestJSON, row.responseJSON, row.errorMessage+"\n"+row.queryString, updatedSecret)
		assertW1bShellLogNoAPIKeySecret(t, row.requestJSON, row.responseJSON, row.errorMessage+"\n"+row.queryString, w1bAccountShellSecretLikeKeyword)
		assertW1bShellLogNoAPIKeySecret(t, row.requestJSON, row.responseJSON, row.errorMessage+"\n"+row.queryString, userInfoURL)
		assertW1bShellLogNoAPIKeySecret(t, row.requestJSON, row.responseJSON, row.errorMessage+"\n"+row.queryString, "user:pass")
		assertW1bShellLogNoAPIKeySecret(t, row.requestJSON, row.responseJSON, row.errorMessage+"\n"+row.queryString, "notes-secret-value")
		assertW1bPublicAccountResponseNoSecret(t, row.responseJSON, initialSecret)
		assertW1bPublicAccountResponseNoSecret(t, row.responseJSON, updatedSecret)
	}
}

func assertW1bPublicAccountResponseNoSecret(t *testing.T, response string, secrets ...string) {
	t.Helper()

	normalized := strings.ToLower(response)
	for _, forbidden := range []string{"\"apikey\"", "\"baseurl\"", "\"credentials\""} {
		if strings.Contains(normalized, forbidden) {
			t.Fatalf("public account response leaked forbidden field %s in %s", forbidden, response)
		}
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(normalized, strings.ToLower(secret)) {
			t.Fatalf("public account response leaked secret %q in %s", secret, response)
		}
	}
}
