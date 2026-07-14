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
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	publicapilogjob "juhe-ai/backend-go/internal/jobs/publicapilog"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementpublicapilogs"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w6ManagementPublicAPILogsPostgresPassword = "go_public_api_logs_password"
	w6ManagementPublicAPILogsAdminToken       = "w6-public-api-logs-admin-token"
	w6ManagementPublicAPILogsUserToken        = "w6-public-api-logs-user-token"
	w6ManagementPublicAPILogsResponseLimit    = 256 * 1024
	w6ManagementPublicAPILogsPayloadMarker    = "w6-public-api-payload-marker-"
)

type w6ManagementPublicAPILogListData struct {
	Items    []map[string]any `json:"items"`
	Total    int              `json:"total"`
	HasMore  bool             `json:"hasMore"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
}

func TestW6ManagementPublicAPILogsGoWriterReaderPostgresSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()

	postgresContainer, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("juhe_ai"),
		tcpostgres.WithUsername("juhe_ai"),
		tcpostgres.WithPassword(w6ManagementPublicAPILogsPostgresPassword),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start PostgreSQL container: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		terminateContainer(t, cleanupCtx, postgresContainer)
	}()

	postgresURL, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("build PostgreSQL connection string: %v", err)
	}
	db := openSQLDB(t, postgresURL)
	defer closeSQLDB(t, db)

	w6ManagementPublicAPILogsRunFreshMigrations(t, db)
	w6ManagementPublicAPILogsAssertSchemaTypes(t, ctx, db)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf(
			"open production PostgreSQL store: %s",
			w6ManagementPublicAPILogsRedactSensitiveText(err.Error(), postgresURL),
		)
	}
	defer store.Close()

	now := time.Date(2026, 7, 14, 12, 0, 0, 123_000_000, time.UTC)
	fixtures := w6ManagementPublicAPILogFixtures(now)
	w6ManagementPublicAPILogsWriteWithProductionGoJob(t, ctx, store, fixtures)
	w6ManagementPublicAPILogsAssertWrittenCount(t, ctx, db, len(fixtures))
	sessionLastSeenAt := now.Add(-2 * time.Hour)
	w6ManagementPublicAPILogsInsertSessions(t, ctx, db, now, sessionLastSeenAt)

	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	service := managementpublicapilogs.NewService(store)
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
			TrustProxy:           "false",
		},
		Logger:                            slog.New(slog.NewTextHandler(io.Discard, nil)),
		SystemAPIRateLimitReader:          store,
		SystemAPIIPRateLimiter:            httpapi.NewInMemorySystemAPIIPRateLimiter(),
		SystemAPIAuthenticatedRateLimiter: httpapi.NewInMemorySystemAPIAuthenticatedRateLimiter(),
		ManagementAPIAuthMiddleware:       httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementPublicAPILogsHandler:    httpapi.NewManagementPublicAPILogsHandler(service),
	})
	server := httptest.NewServer(router)
	defer server.Close()

	w6ManagementPublicAPILogsAssertPermissions(t, ctx, server)
	w6ManagementPublicAPILogsAssertDefaultStableList(t, ctx, server)
	w6ManagementPublicAPILogsAssertProgressivePagination(t, ctx, server)
	w6ManagementPublicAPILogsAssertFilters(t, ctx, server, now)
	w6ManagementPublicAPILogsAssertDetailAndNotFound(t, ctx, server, fixtures)
	w6ManagementPublicAPILogsAssertSessionsUntouched(t, ctx, db, sessionLastSeenAt)
	w6ManagementPublicAPILogsAssertExplainGates(t, ctx, db)
}

func w6ManagementPublicAPILogFixtures(now time.Time) []port.PublicAPILogInput {
	fixture := func(
		id string,
		createdOffset time.Duration,
		sourceRefID string,
		method string,
		path string,
		queryString string,
		success bool,
		statusCode int,
		traceID string,
		clientIP string,
	) port.PublicAPILogInput {
		createdAt := now.Add(createdOffset)
		durationMs := int64(100 + len(id))
		marker := w6ManagementPublicAPILogsPayloadMarker + id
		errorCode := ""
		errorMessage := ""
		if !success {
			errorCode = "fixture_failed"
			errorMessage = "W6 public API fixture failed"
		}
		return port.PublicAPILogInput{
			ID:                    id,
			TraceID:               traceID,
			SourceRefID:           sourceRefID,
			SourceName:            "W6 source " + sourceRefID,
			TokenID:               "token_" + sourceRefID,
			TokenName:             "W6 token " + sourceRefID,
			TokenPrefix:           "jpa_w6_" + sourceRefID,
			IsTestToken:           strings.Contains(id, "filter_target"),
			Method:                method,
			Path:                  path,
			QueryString:           queryString,
			ClientIP:              clientIP,
			UserAgent:             "w6-go-public-api-log-smoke/1.0",
			StatusCode:            &statusCode,
			Success:               success,
			DurationMs:            &durationMs,
			RequestSizeBytes:      int64(1000 + len(id)),
			ResponseSizeBytes:     int64(2000 + len(id)),
			RequestCaptureStatus:  port.PublicAPILogCaptureComplete,
			ResponseCaptureStatus: port.PublicAPILogCaptureComplete,
			RequestData: map[string]any{
				"marker": marker + "-request",
				"nested": map[string]any{"fixture": id, "kind": "request"},
			},
			ResponseData: map[string]any{
				"marker": marker + "-response",
				"nested": map[string]any{"fixture": id, "kind": "response"},
			},
			ErrorCode:    errorCode,
			ErrorMessage: errorMessage,
			StartedAt:    createdAt.Add(-time.Duration(durationMs) * time.Millisecond),
			EndedAt:      createdAt,
			CreatedAt:    createdAt,
		}
	}

	return []port.PublicAPILogInput{
		fixture("publog_w6_newest", -10*time.Minute, "extsrc_w6_gamma", http.MethodGet, "/__aipublic__/health", "full=1", true, 204, "trace-w6-newest-001", "198.51.100.10"),
		fixture("publog_w6_same_a", -20*time.Minute, "extsrc_w6_gamma", http.MethodGet, "/__aipublic__/same/a", "slot=a", true, 200, "trace-w6-same-a", "198.51.100.11"),
		fixture("publog_w6_same_z", -20*time.Minute, "extsrc_w6_delta", http.MethodGet, "/__aipublic__/same/z", "slot=z", true, 200, "trace-w6-same-z", "198.51.100.12"),
		fixture("publog_w6_path_sibling", -40*time.Minute, "extsrc_w6_beta", http.MethodPost, "/__aipublic__/account/add", "mode=preview", true, 201, "trace-w6-path-001", "192.0.2.20"),
		fixture("publog_w6_filter_target", -60*time.Minute, "extsrc_w6_alpha", http.MethodPatch, "/__aipublic__/account/add", "dryRun=true&mode=full", false, 503, "trace-w6-alpha-001", "203.0.113.41"),
		fixture("publog_w6_trace_sibling", -80*time.Minute, "extsrc_w6_beta", http.MethodGet, "/__aipublic__/group/list", "page=1", true, 200, "trace-w6-alpha-002", "203.0.113.42"),
		fixture("publog_w6_source_sibling", -100*time.Minute, "extsrc_w6_alpha", http.MethodPost, "/v1/messages", "stream=true", true, 202, "trace-w6-source-001", "198.51.100.30"),
		fixture("publog_w6_failed_sibling", -120*time.Minute, "extsrc_w6_beta", http.MethodDelete, "/__aipublic__/token/revoke", "force=1", false, 429, "trace-w6-failed-001", "203.0.113.99"),
		fixture("publog_w6_oldest", -180*time.Minute, "extsrc_w6_delta", http.MethodOptions, "/__aipublic__/catalog", "full=false", true, 200, "trace-w6-oldest-001", "192.0.2.99"),
	}
}

func w6ManagementPublicAPILogsRunFreshMigrations(t *testing.T, db *sql.DB) {
	t.Helper()

	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set Goose PostgreSQL dialect: %v", err)
	}
	migrationDir := filepath.Join(repoRoot(t), "db", "migrations")
	if err := goose.Up(db, migrationDir); err != nil {
		t.Fatalf("run all Goose migrations on fresh PostgreSQL: %v", err)
	}
	version, err := goose.GetDBVersion(db)
	if err != nil {
		t.Fatalf("read Goose version after full migration: %v", err)
	}
	if version <= 0 {
		t.Fatalf("Goose version after full migration = %d, want positive", version)
	}
}

func w6ManagementPublicAPILogsAssertSchemaTypes(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	want := map[string]string{
		"is_test_token":       "boolean",
		"success":             "boolean",
		"duration_ms":         "bigint",
		"request_size_bytes":  "bigint",
		"response_size_bytes": "bigint",
		"started_at":          "timestamp with time zone",
		"ended_at":            "timestamp with time zone",
		"created_at":          "timestamp with time zone",
	}
	rows, err := db.QueryContext(ctx, `
SELECT column_name, data_type
FROM information_schema.columns
WHERE table_schema = 'juhe_dataset'
  AND table_name = 'public_api_logs'
  AND column_name IN (
    'is_test_token', 'success', 'duration_ms', 'request_size_bytes',
    'response_size_bytes', 'started_at', 'ended_at', 'created_at'
  )
`)
	if err != nil {
		t.Fatalf("query public API log schema types: %v", err)
	}
	defer rows.Close()

	got := make(map[string]string, len(want))
	for rows.Next() {
		var columnName string
		var dataType string
		if err := rows.Scan(&columnName, &dataType); err != nil {
			t.Fatalf("scan public API log schema type: %v", err)
		}
		got[columnName] = dataType
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate public API log schema types: %v", err)
	}
	for columnName, wantType := range want {
		if got[columnName] != wantType {
			t.Fatalf("public API log column %s data_type = %q, want %q; all=%v", columnName, got[columnName], wantType, got)
		}
	}
}

func w6ManagementPublicAPILogsWriteWithProductionGoJob(
	t *testing.T,
	ctx context.Context,
	store *postgresstore.Store,
	fixtures []port.PublicAPILogInput,
) {
	t.Helper()

	// The Node writer still targets legacy integer/text columns, so this smoke crosses the production Go Encode/Handle job boundary.
	for _, fixture := range fixtures {
		payload, err := publicapilogjob.EncodeWriteTaskPayload(fixture)
		if err != nil {
			t.Fatalf("encode production Go public API log job %s: %v", fixture.ID, err)
		}
		if err := publicapilogjob.HandleWriteTask(ctx, store, payload); err != nil {
			t.Fatalf("handle production Go public API log job %s: %v", fixture.ID, err)
		}
	}
}

func w6ManagementPublicAPILogsAssertWrittenCount(t *testing.T, ctx context.Context, db *sql.DB, want int) {
	t.Helper()

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.public_api_logs`).Scan(&count); err != nil {
		t.Fatalf("count Go-written public API logs: %v", err)
	}
	if count != want {
		t.Fatalf("Go-written public API logs = %d, want %d", count, want)
	}
}

func w6ManagementPublicAPILogsInsertSessions(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
	lastSeenAt time.Time,
) {
	t.Helper()

	_, err := db.ExecContext(ctx, `
INSERT INTO juhe_business.system_accounts (
  id, username, display_name, description, role, status, password_hash,
  must_change_password, image_generation_enabled, created_at, updated_at
) VALUES
  ('sys_w6_public_api_logs_admin', 'w6-public-api-logs-admin', 'W6 Public API Logs Admin', NULL, 'admin', 'active', 'hash', false, false, $1, $1),
  ('sys_w6_public_api_logs_user', 'w6-public-api-logs-user', 'W6 Public API Logs User', NULL, 'user', 'active', 'hash', false, false, $1, $1);
INSERT INTO juhe_business.system_sessions (
  id, system_account_id, token_hash, expires_at, created_at, last_seen_at
) VALUES
  ('sess_w6_public_api_logs_admin', 'sys_w6_public_api_logs_admin', $2, $3, $1, $4),
  ('sess_w6_public_api_logs_user', 'sys_w6_public_api_logs_user', $5, $3, $1, $4);
`, now, managementauth.HashSessionToken(w6ManagementPublicAPILogsAdminToken), now.Add(24*time.Hour), lastSeenAt, managementauth.HashSessionToken(w6ManagementPublicAPILogsUserToken))
	if err != nil {
		t.Fatalf("insert public API logs accounts and sessions: %v", err)
	}
}

func w6ManagementPublicAPILogsAssertPermissions(t *testing.T, ctx context.Context, server *httptest.Server) {
	t.Helper()

	status, body := w6ManagementPublicAPILogsGET(t, ctx, server, "/__aisys__/api/public-api-logs", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("public API logs anonymous status = %d, want 401; body=%s", status, body)
	}
	status, body = w6ManagementPublicAPILogsGET(t, ctx, server, "/__aisys__/api/public-api-logs", w6ManagementPublicAPILogsUserToken)
	if status != http.StatusForbidden || !strings.Contains(string(body), "需要管理员权限") {
		t.Fatalf("public API logs user status = %d, want 403; body=%s", status, body)
	}
}

func w6ManagementPublicAPILogsAssertDefaultStableList(t *testing.T, ctx context.Context, server *httptest.Server) {
	t.Helper()

	data, body := w6ManagementPublicAPILogsAdminList(t, ctx, server, nil)
	wantIDs := []string{
		"publog_w6_newest",
		"publog_w6_same_z",
		"publog_w6_same_a",
		"publog_w6_path_sibling",
		"publog_w6_filter_target",
		"publog_w6_trace_sibling",
		"publog_w6_source_sibling",
		"publog_w6_failed_sibling",
		"publog_w6_oldest",
	}
	w6ManagementPublicAPILogsAssertListData(t, data, body, wantIDs, 9, false, 1, 100)
}

func w6ManagementPublicAPILogsAssertProgressivePagination(t *testing.T, ctx context.Context, server *httptest.Server) {
	t.Helper()

	tests := []struct {
		name      string
		query     url.Values
		wantIDs   []string
		wantPage  int
		wantTotal int
		wantMore  bool
	}{
		{
			name:      "first lookahead page",
			query:     url.Values{"page": {"1"}, "pageSize": {"2"}},
			wantIDs:   []string{"publog_w6_newest", "publog_w6_same_z"},
			wantPage:  1,
			wantTotal: 3,
			wantMore:  true,
		},
		{
			name:      "second progressive page",
			query:     url.Values{"page": {"2"}, "pageSize": {"2"}},
			wantIDs:   []string{"publog_w6_same_a", "publog_w6_path_sibling"},
			wantPage:  2,
			wantTotal: 5,
			wantMore:  true,
		},
		{
			name:      "final progressive page",
			query:     url.Values{"page": {"5"}, "pageSize": {"2"}},
			wantIDs:   []string{"publog_w6_oldest"},
			wantPage:  5,
			wantTotal: 9,
			wantMore:  false,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			data, body := w6ManagementPublicAPILogsAdminList(t, ctx, server, testCase.query)
			w6ManagementPublicAPILogsAssertListData(
				t,
				data,
				body,
				testCase.wantIDs,
				testCase.wantTotal,
				testCase.wantMore,
				testCase.wantPage,
				2,
			)
		})
	}
}

func w6ManagementPublicAPILogsAssertFilters(t *testing.T, ctx context.Context, server *httptest.Server, now time.Time) {
	t.Helper()

	targetCreatedAt := now.Add(-60 * time.Minute)
	targetTimestamp := targetCreatedAt.Format("2006-01-02T15:04:05.000Z")
	copiedPath := "PATCH /__aipublic__/account/add?dryRun=true&mode=full"
	tests := []struct {
		name    string
		query   url.Values
		wantIDs []string
	}{
		{
			name:    "trace prefix",
			query:   url.Values{"traceId": {"trace-w6-alpha-"}},
			wantIDs: []string{"publog_w6_filter_target", "publog_w6_trace_sibling"},
		},
		{
			name:    "source exact",
			query:   url.Values{"sourceRefId": {"extsrc_w6_alpha"}},
			wantIDs: []string{"publog_w6_filter_target", "publog_w6_source_sibling"},
		},
		{
			name:    "copied method path and query",
			query:   url.Values{"path": {copiedPath}},
			wantIDs: []string{"publog_w6_path_sibling", "publog_w6_filter_target"},
		},
		{
			name:    "failed result",
			query:   url.Values{"result": {"failed"}},
			wantIDs: []string{"publog_w6_filter_target", "publog_w6_failed_sibling"},
		},
		{
			name:    "success result",
			query:   url.Values{"result": {"success"}, "pageSize": {"100"}},
			wantIDs: []string{"publog_w6_newest", "publog_w6_same_z", "publog_w6_same_a", "publog_w6_path_sibling", "publog_w6_trace_sibling", "publog_w6_source_sibling", "publog_w6_oldest"},
		},
		{
			name:    "status exact",
			query:   url.Values{"statusCode": {"503"}},
			wantIDs: []string{"publog_w6_filter_target"},
		},
		{
			name:    "client IP prefix",
			query:   url.Values{"clientIp": {"203.0.113.4"}},
			wantIDs: []string{"publog_w6_filter_target", "publog_w6_trace_sibling"},
		},
		{
			name: "inclusive date endpoints",
			query: url.Values{
				"startAt": {targetTimestamp},
				"endAt":   {targetTimestamp},
			},
			wantIDs: []string{"publog_w6_filter_target"},
		},
		{
			name: "all filters are ANDed",
			query: url.Values{
				"traceId":     {"trace-w6-alpha-001"},
				"sourceRefId": {"extsrc_w6_alpha"},
				"path":        {copiedPath},
				"result":      {"failed"},
				"statusCode":  {"503"},
				"clientIp":    {"203.0.113.41"},
				"startAt":     {targetTimestamp},
				"endAt":       {targetTimestamp},
			},
			wantIDs: []string{"publog_w6_filter_target"},
		},
		{
			name: "reverse date range is empty",
			query: url.Values{
				"startAt": {targetCreatedAt.Add(time.Millisecond).Format("2006-01-02T15:04:05.000Z")},
				"endAt":   {targetCreatedAt.Add(-time.Millisecond).Format("2006-01-02T15:04:05.000Z")},
			},
			wantIDs: []string{},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			data, body := w6ManagementPublicAPILogsAdminList(t, ctx, server, testCase.query)
			w6ManagementPublicAPILogsAssertIDs(t, data.Items, body, testCase.wantIDs)
		})
	}
}

func w6ManagementPublicAPILogsAssertDetailAndNotFound(
	t *testing.T,
	ctx context.Context,
	server *httptest.Server,
	fixtures []port.PublicAPILogInput,
) {
	t.Helper()

	var target port.PublicAPILogInput
	for _, fixture := range fixtures {
		if fixture.ID == "publog_w6_filter_target" {
			target = fixture
			break
		}
	}
	if target.ID == "" {
		t.Fatal("public API log detail target fixture is missing")
	}

	status, body := w6ManagementPublicAPILogsGET(
		t,
		ctx,
		server,
		"/__aisys__/api/public-api-logs/"+url.PathEscape(target.ID),
		w6ManagementPublicAPILogsAdminToken,
	)
	if status != http.StatusOK {
		t.Fatalf("public API log detail status = %d, want 200; body=%s", status, body)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode public API log detail: %v; body=%s", err, body)
	}
	if envelope.Data["id"] != target.ID || envelope.Data["method"] != target.Method || envelope.Data["path"] != target.Path {
		t.Fatalf("public API log detail summary = %#v", envelope.Data)
	}
	requestData, requestIsObject := envelope.Data["requestData"].(map[string]any)
	responseData, responseIsObject := envelope.Data["responseData"].(map[string]any)
	wantMarker := w6ManagementPublicAPILogsPayloadMarker + target.ID
	if !requestIsObject || requestData["marker"] != wantMarker+"-request" {
		t.Fatalf("public API log requestData = %#v, want object marker", envelope.Data["requestData"])
	}
	if !responseIsObject || responseData["marker"] != wantMarker+"-response" {
		t.Fatalf("public API log responseData = %#v, want object marker", envelope.Data["responseData"])
	}

	status, body = w6ManagementPublicAPILogsGET(
		t,
		ctx,
		server,
		"/__aisys__/api/public-api-logs/publog_w6_missing",
		w6ManagementPublicAPILogsAdminToken,
	)
	if status != http.StatusNotFound || !strings.Contains(string(body), "公开接口日志不存在") {
		t.Fatalf("missing public API log detail status = %d, want 404; body=%s", status, body)
	}
}

func w6ManagementPublicAPILogsAssertSessionsUntouched(t *testing.T, ctx context.Context, db *sql.DB, want time.Time) {
	t.Helper()

	rows, err := db.QueryContext(ctx, `
SELECT id, last_seen_at
FROM juhe_business.system_sessions
WHERE id IN ('sess_w6_public_api_logs_admin', 'sess_w6_public_api_logs_user')
ORDER BY id
`)
	if err != nil {
		t.Fatalf("read public API logs sessions after read requests: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var id string
		var lastSeenAt time.Time
		if err := rows.Scan(&id, &lastSeenAt); err != nil {
			t.Fatalf("scan public API logs session: %v", err)
		}
		count++
		if !lastSeenAt.UTC().Equal(want.UTC()) {
			t.Fatalf(
				"read-only public API logs request touched session %s: last_seen_at=%s want=%s",
				id,
				lastSeenAt.UTC().Format(time.RFC3339Nano),
				want.UTC().Format(time.RFC3339Nano),
			)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate public API logs sessions: %v", err)
	}
	if count != 2 {
		t.Fatalf("public API logs sessions read = %d, want 2", count)
	}
}

func w6ManagementPublicAPILogsAdminList(
	t *testing.T,
	ctx context.Context,
	server *httptest.Server,
	query url.Values,
) (w6ManagementPublicAPILogListData, []byte) {
	t.Helper()

	path := "/__aisys__/api/public-api-logs"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	status, body := w6ManagementPublicAPILogsGET(t, ctx, server, path, w6ManagementPublicAPILogsAdminToken)
	if status != http.StatusOK {
		t.Fatalf("public API log list status = %d, want 200; body=%s", status, body)
	}
	var envelope struct {
		Data w6ManagementPublicAPILogListData `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode public API log list: %v; body=%s", err, body)
	}
	w6ManagementPublicAPILogsAssertListHasNoPayload(t, envelope.Data.Items, body)
	return envelope.Data, body
}

func w6ManagementPublicAPILogsAssertListHasNoPayload(t *testing.T, items []map[string]any, body []byte) {
	t.Helper()

	for _, item := range items {
		if _, found := item["requestData"]; found {
			t.Fatalf("public API log list leaked requestData: %#v", item)
		}
		if _, found := item["responseData"]; found {
			t.Fatalf("public API log list leaked responseData: %#v", item)
		}
	}
	for _, forbidden := range []string{`"requestData"`, `"responseData"`, w6ManagementPublicAPILogsPayloadMarker} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("public API log list leaked %q: %s", forbidden, body)
		}
	}
}

func w6ManagementPublicAPILogsAssertListData(
	t *testing.T,
	data w6ManagementPublicAPILogListData,
	body []byte,
	wantIDs []string,
	wantTotal int,
	wantMore bool,
	wantPage int,
	wantPageSize int,
) {
	t.Helper()

	w6ManagementPublicAPILogsAssertIDs(t, data.Items, body, wantIDs)
	if data.Total != wantTotal || data.HasMore != wantMore || data.Page != wantPage || data.PageSize != wantPageSize {
		t.Fatalf(
			"public API log pagination = total %d hasMore %v page %d pageSize %d, want %d/%v/%d/%d; body=%s",
			data.Total,
			data.HasMore,
			data.Page,
			data.PageSize,
			wantTotal,
			wantMore,
			wantPage,
			wantPageSize,
			body,
		)
	}
}

func w6ManagementPublicAPILogsAssertIDs(t *testing.T, items []map[string]any, body []byte, want []string) {
	t.Helper()

	got := make([]string, 0, len(items))
	for _, item := range items {
		id, ok := item["id"].(string)
		if !ok {
			t.Fatalf("public API log list item id = %#v; body=%s", item["id"], body)
		}
		got = append(got, id)
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("public API log list ids = %v, want %v; body=%s", got, want, body)
	}
}

func w6ManagementPublicAPILogsGET(
	t *testing.T,
	ctx context.Context,
	server *httptest.Server,
	path string,
	token string,
) (int, []byte) {
	t.Helper()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatalf("build public API logs request: %v", err)
	}
	if token != "" {
		request.AddCookie(&http.Cookie{Name: managementauth.SessionCookieName, Value: token})
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("request production Go public API logs route: %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, w6ManagementPublicAPILogsResponseLimit+1))
	if err != nil {
		t.Fatalf("read production Go public API logs response: %v", err)
	}
	if len(body) > w6ManagementPublicAPILogsResponseLimit {
		t.Fatalf("production Go public API logs response exceeded %d bytes", w6ManagementPublicAPILogsResponseLimit)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("public API logs Cache-Control = %q, want no-store", got)
	}
	return response.StatusCode, body
}

func w6ManagementPublicAPILogsAssertExplainGates(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin public API log EXPLAIN transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `ANALYZE juhe_dataset.public_api_logs`); err != nil {
		t.Fatalf("analyze public API logs before EXPLAIN: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatalf("disable sequential scan for public API log index proof: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL plan_cache_mode = force_generic_plan`); err != nil {
		t.Fatalf("force generic plans for public API log index proof: %v", err)
	}
	var enableSeqscan string
	var planCacheMode string
	if err := tx.QueryRowContext(ctx, `SHOW enable_seqscan`).Scan(&enableSeqscan); err != nil {
		t.Fatalf("read enable_seqscan for public API log index proof: %v", err)
	}
	if err := tx.QueryRowContext(ctx, `SHOW plan_cache_mode`).Scan(&planCacheMode); err != nil {
		t.Fatalf("read plan_cache_mode for public API log index proof: %v", err)
	}
	if enableSeqscan != "off" || planCacheMode != "force_generic_plan" {
		t.Fatalf("public API log plan settings = enable_seqscan %q plan_cache_mode %q", enableSeqscan, planCacheMode)
	}

	for _, testCase := range []struct {
		name          string
		statementName string
		wantIndex     string
		input         port.ManagementPublicAPILogListInput
	}{
		{
			name:          "default list",
			statementName: "w6_public_api_logs_default",
			wantIndex:     "idx_public_api_logs_created",
		},
		{
			name:          "source exact",
			statementName: "w6_public_api_logs_source",
			wantIndex:     "idx_public_api_logs_source_created",
			input:         port.ManagementPublicAPILogListInput{SourceRefID: "extsrc_w6_alpha"},
		},
	} {
		query, args := postgresstore.BuildManagementPublicAPILogListQueryForIntegration(testCase.input, 101, 0)
		w6ManagementPublicAPILogsAssertPreparedExplainIndex(
			t,
			ctx,
			tx,
			testCase.name,
			testCase.statementName,
			testCase.wantIndex,
			query,
			args,
		)
	}
}

func w6ManagementPublicAPILogsAssertPreparedExplainIndex(
	t *testing.T,
	ctx context.Context,
	tx *sql.Tx,
	name string,
	statementName string,
	wantIndex string,
	query string,
	args []any,
) {
	t.Helper()

	if _, err := tx.ExecContext(ctx, "PREPARE "+statementName+" AS\n"+query); err != nil {
		t.Fatalf("prepare public API log %s query: %v", name, err)
	}
	defer func() { _, _ = tx.ExecContext(ctx, "DEALLOCATE "+statementName) }()

	explainSQL := "EXPLAIN (FORMAT JSON, COSTS false) EXECUTE " + statementName + "(" + w6ManagementPublicAPILogsSQLLiterals(t, args) + ")"
	var rawPlan []byte
	if err := tx.QueryRowContext(ctx, explainSQL).Scan(&rawPlan); err != nil {
		t.Fatalf("EXPLAIN public API log %s query: %v", name, err)
	}
	var plan any
	if err := json.Unmarshal(rawPlan, &plan); err != nil {
		t.Fatalf("decode public API log %s EXPLAIN: %v; plan=%s", name, err, rawPlan)
	}
	indexes := map[string]bool{}
	w6ManagementPublicAPILogsCollectPlanIndexes(plan, indexes)
	if !indexes[wantIndex] {
		t.Fatalf("public API log %s EXPLAIN indexes = %v, want %s; plan=%s", name, indexes, wantIndex, rawPlan)
	}
}

func w6ManagementPublicAPILogsSQLLiterals(t *testing.T, args []any) string {
	t.Helper()

	literals := make([]string, 0, len(args))
	for _, arg := range args {
		switch value := arg.(type) {
		case string:
			literals = append(literals, "'"+strings.ReplaceAll(value, "'", "''")+"'")
		case int32:
			literals = append(literals, strconv.FormatInt(int64(value), 10))
		case int:
			literals = append(literals, strconv.Itoa(value))
		case bool:
			literals = append(literals, strconv.FormatBool(value))
		case time.Time:
			literals = append(literals, "'"+value.UTC().Format(time.RFC3339Nano)+"'")
		default:
			t.Fatalf("unsupported public API log EXPLAIN argument type %T", arg)
		}
	}
	return strings.Join(literals, ", ")
}

func w6ManagementPublicAPILogsCollectPlanIndexes(value any, indexes map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		if indexName, ok := typed["Index Name"].(string); ok {
			indexes[indexName] = true
		}
		for _, nested := range typed {
			w6ManagementPublicAPILogsCollectPlanIndexes(nested, indexes)
		}
	case []any:
		for _, nested := range typed {
			w6ManagementPublicAPILogsCollectPlanIndexes(nested, indexes)
		}
	}
}

func w6ManagementPublicAPILogsRedactSensitiveText(value string, secrets ...string) string {
	redacted := value
	for _, secret := range secrets {
		if secret != "" {
			redacted = strings.ReplaceAll(redacted, secret, "<redacted>")
		}
	}
	redacted = strings.ReplaceAll(redacted, w6ManagementPublicAPILogsPostgresPassword, "<redacted>")
	return redacted
}
