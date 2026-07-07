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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"juhe-ai/backend-go/internal/app"
	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/publicapi"
	publicapiauth "juhe-ai/backend-go/internal/modules/publicapi/auth"
	publicapiratelimit "juhe-ai/backend-go/internal/modules/publicapi/ratelimit"
	"juhe-ai/backend-go/internal/modules/publicgroups"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW1bPublicGroupsShellE2E(t *testing.T) {
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

	redisState, err := redisplatform.NewClient(redisURL, "w1b-public-groups-shell-state")
	if err != nil {
		t.Fatalf("open redis state client: %v", err)
	}
	defer closeRedisClient(t, redisState)
	limiter, err := publicapiratelimit.NewLimiter(publicapiratelimit.Options{
		Client: redisState,
		Now:    w1bPublicGroupsShellNow,
	})
	if err != nil {
		t.Fatalf("new public api limiter: %v", err)
	}
	logClient := queue.NewClient(redisOpts)
	defer closeClient(t, logClient)

	token := "juis_w1b_group_shell"
	insertW1bShellSourceAndToken(t, ctx, db, token, w1bPublicGroupsShellNow())

	var idSeq atomic.Int32
	groupService := publicgroups.NewService(publicgroups.Options{
		Store:      store,
		Transactor: store,
		Now:        w1bPublicGroupsShellNow,
		NewID: func(prefix string) string {
			return prefix + "_w1b_shell_" + strconv.Itoa(int(idSeq.Add(1)))
		},
	})
	var logSeq atomic.Int32
	router := httpapi.NewPublicAPIShell(httpapi.PublicAPIShellOptions{
		Config:           config.Config{TrustProxy: "false"},
		Authenticator:    publicapiauth.NewAuthenticator(publicapiauth.AuthenticatorOptions{Store: store, Now: w1bPublicGroupsShellNow}),
		RateLimiter:      limiter,
		LogClient:        logClient,
		EndpointHandlers: httpapi.NewPublicGroupHandlers(groupService),
		Now:              w1bPublicGroupsShellNow,
		NewLogID: func() string {
			return "publog_w1b_group_shell_" + strconv.Itoa(int(logSeq.Add(1)))
		},
	})

	addRec := serveW1bShellRequest(router, http.MethodPost, "/__aipublic__/group/add", token, "trace_group_add", `{"targetUsername":"admin","targetDisplayName":"管理员","name":"福利","providerCode":"gpt"}`)
	if addRec.Code != http.StatusCreated {
		t.Fatalf("add status = %d, body = %s", addRec.Code, addRec.Body.String())
	}
	var addBody struct {
		Data publicgroups.GroupResponse `json:"data"`
	}
	if err := json.NewDecoder(addRec.Body).Decode(&addBody); err != nil {
		t.Fatalf("decode add response: %v", err)
	}
	if addBody.Data.Action != "created" || addBody.Data.Group == nil || addBody.Data.Group.Name != "福利" {
		t.Fatalf("add response = %+v", addBody.Data)
	}
	groupID := addBody.Data.Group.ID

	listRec := serveW1bShellRequest(router, http.MethodGet, "/__aipublic__/group/list?targetUsername=admin&providerCode=gpt&page=1&pageSize=10", token, "trace_group_list", "")
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRec.Code, listRec.Body.String())
	}

	updateRec := serveW1bShellRequest(router, http.MethodPost, "/__aipublic__/group/update", token, "trace_group_update", `{"groupId":"`+groupID+`","name":"福利更新"}`)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updateRec.Code, updateRec.Body.String())
	}

	deleteRec := serveW1bShellRequest(router, http.MethodPost, "/__aipublic__/group/del", token, "trace_group_delete", `{"groupId":"`+groupID+`"}`)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}

	limitedRec := serveW1bShellRequest(router, http.MethodGet, "/__aipublic__/group/list?targetUsername=admin&providerCode=gpt&page=1&pageSize=10", token, "trace_group_limited", "")
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

	assertW1bShellGroupDeleted(t, ctx, db, groupID)
	assertW1bShellLastUsed(t, ctx, db, w1bPublicGroupsShellNow())
	assertW1bShellPublicAPILogs(t, ctx, db, token, groupID)
}

func serveW1bShellRequest(router http.Handler, method string, target string, token string, traceID string, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Request-Id", traceID)
	req.Header.Set("User-Agent", "w1b-shell-e2e")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func w1bPublicGroupsShellNow() time.Time {
	return time.Date(2026, 7, 7, 10, 30, 0, 0, time.UTC)
}

func startW1bShellIngestWorker(t *testing.T, ctx context.Context, postgresURL string, redisURL string) (<-chan struct{}, func() error) {
	t.Helper()

	workerDone := make(chan struct{})
	var workerErrMu sync.Mutex
	var workerRunErr error
	go func() {
		err := app.RunIngestWorker(ctx, config.Config{
			PostgresURL:     postgresURL,
			RedisQueueURL:   redisURL,
			RedisNamespace:  "w1b-public-groups-shell",
			LogLevel:        "error",
			ShutdownTimeout: time.Second,
		}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		workerErrMu.Lock()
		workerRunErr = err
		workerErrMu.Unlock()
		close(workerDone)
	}()

	return workerDone, func() error {
		workerErrMu.Lock()
		defer workerErrMu.Unlock()
		return workerRunErr
	}
}

func stopW1bShellIngestWorker(t *testing.T, stopWorker context.CancelFunc, workerDone <-chan struct{}, workerErr func() error) {
	t.Helper()

	stopWorker()
	select {
	case <-workerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("ingest worker shutdown timed out")
	}
	if err := workerErr(); err != nil {
		t.Fatalf("ingest worker run: %v", err)
	}
}

func insertW1bShellSourceAndToken(t *testing.T, ctx context.Context, db *sql.DB, token string, now time.Time) {
	t.Helper()

	scopes := `["` + publicapi.ScopeGroupListRead + `","` + publicapi.ScopeGroupAddWrite + `","` + publicapi.ScopeGroupUpdateWrite + `","` + publicapi.ScopeGroupDeleteWrite + `"]`
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.external_integration_sources (
			id, name, status, scopes_json, rate_limits_json, created_at, updated_at
		) VALUES ($1, $2, 'active', $3, $4, $5, $6)
	`, "extsrc_w1b_group_shell", "W1b Group Shell Source", scopes, `[{"windowSeconds":60,"maxRequests":4}]`, now, now)
	if err != nil {
		t.Fatalf("insert shell external integration source: %v", err)
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO juhe_business.external_integration_source_tokens (
			id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix, status, scopes_json, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'active', $8, $9, $10)
	`, "exttok_w1b_group_shell", "extsrc_w1b_group_shell", "W1b Group Shell Token", publicapiauth.HashExternalSourceToken(token), "encrypted", "juis_w1b_group", "shell", scopes, now, now)
	if err != nil {
		t.Fatalf("insert shell external integration token: %v", err)
	}
}

func assertW1bShellGroupDeleted(t *testing.T, ctx context.Context, db *sql.DB, groupID string) {
	t.Helper()

	var groupCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)::int
		FROM juhe_business.groups
		WHERE id = $1
	`, groupID).Scan(&groupCount); err != nil {
		t.Fatalf("count deleted shell group: %v", err)
	}
	if groupCount != 0 {
		t.Fatalf("deleted shell group count = %d, want 0", groupCount)
	}
}

func assertW1bShellLastUsed(t *testing.T, ctx context.Context, db *sql.DB, want time.Time) {
	t.Helper()

	assertTimestampEquals(t, db, "SELECT last_used_at FROM juhe_business.external_integration_sources WHERE id = $1", "extsrc_w1b_group_shell", want)
	assertTimestampEquals(t, db, "SELECT last_used_at FROM juhe_business.external_integration_source_tokens WHERE id = $1", "exttok_w1b_group_shell", want)
}

func assertW1bShellPublicAPILogs(t *testing.T, ctx context.Context, db *sql.DB, token string, groupID string) {
	t.Helper()

	rows, err := db.QueryContext(ctx, `
		SELECT id, trace_id, source_ref_id, token_id, token_prefix, is_test_token, method, path, query_string,
		       client_ip, user_agent, status_code, success, request_capture_status, response_capture_status,
		       request_data_json, response_data_json, COALESCE(error_code, ''), COALESCE(error_message, '')
		FROM juhe_dataset.public_api_logs
		WHERE id LIKE 'publog_w1b_group_shell_%'
		ORDER BY id
	`)
	if err != nil {
		t.Fatalf("query shell public api logs: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Fatalf("close shell public api log rows: %v", err)
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
			t.Fatalf("scan shell public api log: %v", err)
		}
		logs = append(logs, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate shell public api logs: %v", err)
	}
	if len(logs) != 5 {
		t.Fatalf("shell public api log count = %d, want 5", len(logs))
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
		{id: "publog_w1b_group_shell_1", traceID: "trace_group_add", method: http.MethodPost, path: "/__aipublic__/group/add", statusCode: http.StatusCreated, success: true, action: "created", bodyName: "福利"},
		{id: "publog_w1b_group_shell_2", traceID: "trace_group_list", method: http.MethodGet, path: "/__aipublic__/group/list", statusCode: http.StatusOK, success: true, queryTarget: "admin"},
		{id: "publog_w1b_group_shell_3", traceID: "trace_group_update", method: http.MethodPost, path: "/__aipublic__/group/update", statusCode: http.StatusOK, success: true, action: "updated", bodyName: "福利更新"},
		{id: "publog_w1b_group_shell_4", traceID: "trace_group_delete", method: http.MethodPost, path: "/__aipublic__/group/del", statusCode: http.StatusOK, success: true, action: "deleted"},
		{id: "publog_w1b_group_shell_5", traceID: "trace_group_limited", method: http.MethodGet, path: "/__aipublic__/group/list", statusCode: http.StatusTooManyRequests, success: false, errorCode: "external_source_rate_limited", queryTarget: "admin"},
	}

	for index, want := range expected {
		row := logs[index]
		if row.id != want.id || row.traceID != want.traceID || row.method != want.method || row.path != want.path {
			t.Fatalf("log[%d] identity = %+v, want id/trace/method/path %s/%s/%s/%s", index, row, want.id, want.traceID, want.method, want.path)
		}
		if row.sourceRefID != "extsrc_w1b_group_shell" || row.tokenID != "exttok_w1b_group_shell" || row.tokenPrefix != "juis_w1b_group" || row.isTestToken {
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
		if want.action != "" && nestedStringFromW1bShellLog(t, responseData, "body", "data", "group", "id") != groupID {
			t.Fatalf("log[%d] response group id = %#v, want %s", index, responseData["body"], groupID)
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

func decodeW1bShellLogJSON(t *testing.T, raw string) map[string]any {
	t.Helper()

	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode public api log json %q: %v", raw, err)
	}
	return out
}

func nestedStringFromW1bShellLog(t *testing.T, data map[string]any, keys ...string) string {
	t.Helper()

	value := nestedValueFromW1bShellLog(t, data, keys...)
	text, ok := value.(string)
	if !ok {
		t.Fatalf("nested value %v = %#v, want string", keys, value)
	}
	return text
}

func nestedValueFromW1bShellLog(t *testing.T, data map[string]any, keys ...string) any {
	t.Helper()

	var current any = data
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("nested value before key %s = %#v, want object", key, current)
		}
		current = object[key]
	}
	return current
}

func intFromW1bShellLog(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case string:
		parsed, _ := strconv.Atoi(typed)
		return parsed
	default:
		return 0
	}
}

func assertW1bShellLogNoSecret(t *testing.T, requestJSON string, responseJSON string, errorMessage string, token string) {
	t.Helper()

	combined := strings.ToLower(requestJSON + "\n" + responseJSON + "\n" + errorMessage)
	for _, forbidden := range []string{
		strings.ToLower(token),
		"authorization",
		"bearer ",
		"token_hash",
		"cookie",
	} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("public api log leaked forbidden text %q in %s", forbidden, combined)
		}
	}
}
