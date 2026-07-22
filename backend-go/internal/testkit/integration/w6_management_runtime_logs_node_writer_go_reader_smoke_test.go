//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
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
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementruntimelogs"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w6RuntimeLogsGooseTargetVersion = int64(45)
	w6RuntimeLogsNodeOutputPrefix   = "JUHE_AI_NODE_GO_RUNTIME_LOGS "
	w6RuntimeLogsOutputLimit        = 64 * 1024
	w6RuntimeLogsBodyLimit          = 256 * 1024
	w6RuntimeLogsNodeTimeout        = 90 * time.Second
	w6RuntimeLogsProbeTimeout       = 10 * time.Second
	w6RuntimeLogsPostgresPassword   = "node_go_runtime_logs_password"
	w6RuntimeLogsAdminToken         = "w6-runtime-logs-admin-token"
	w6RuntimeLogsUserToken          = "w6-runtime-logs-user-token"
)

type w6RuntimeLogFixture struct {
	ID           string `json:"id"`
	LogFile      string `json:"logFile"`
	LogOffset    int64  `json:"logOffset"`
	LineNumber   int    `json:"lineNumber"`
	Time         string `json:"time"`
	Level        string `json:"level"`
	TraceID      string `json:"traceId"`
	Event        string `json:"event"`
	Message      string `json:"message"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	RawJSON      string `json:"rawJson"`
	CreatedAt    string `json:"createdAt"`
}

func TestW6ManagementRuntimeLogsNodeWriterGoReaderIntegrationSmoke(t *testing.T) {
	nodePath, backendDir := w6RuntimeLogsNodePrerequisites(t)
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	postgresContainer, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("juhe_ai"),
		tcpostgres.WithUsername("juhe_ai"),
		tcpostgres.WithPassword(w6RuntimeLogsPostgresPassword),
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

	w6RuntimeLogsRunFreshMigrations(t, db)

	now := time.Now().UTC().Truncate(time.Millisecond)
	fixtures := w6RuntimeLogsFixtures(now)
	w6RuntimeLogsRunNodeWriter(t, ctx, nodePath, backendDir, postgresURL, fixtures)
	w6RuntimeLogsAssertNodeWriterFacets(t, ctx, db, len(fixtures))
	w6RuntimeLogsInsertSessions(t, ctx, db, now)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open production PostgreSQL store: %s", w6ManagementClientIPStatsRedactSensitiveText(err.Error(), postgresURL))
	}
	defer store.Close()

	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	service := managementruntimelogs.NewServiceWithOptions(managementruntimelogs.ServiceOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
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
		ManagementRuntimeLogsHandler:      httpapi.NewManagementRuntimeLogsHandler(service, true),
	})
	server := httptest.NewServer(router)
	defer server.Close()

	w6RuntimeLogsAssertPermissions(t, ctx, server)
	w6RuntimeLogsAssertDefaultList(t, ctx, server, fixtures)
	w6RuntimeLogsAssertFilters(t, ctx, server, now)
	w6RuntimeLogsAssertDetailAndNotFound(t, ctx, server, fixtures[0], fixtures[3])
	w6RuntimeLogsAssertSessionsUntouched(t, ctx, db, now.Add(-2*time.Hour))
	w6RuntimeLogsAssertExplainGates(t, ctx, db)
}

func w6RuntimeLogsFixtures(now time.Time) []w6RuntimeLogFixture {
	fixture := func(
		id string,
		offset time.Duration,
		level string,
		traceID string,
		event string,
		message string,
		errorMessage string,
	) w6RuntimeLogFixture {
		logTime := now.Add(offset).Format("2006-01-02T15:04:05.000Z")
		rawJSON := fmt.Sprintf(
			`{"id":%q,"time":%q,"level":%q,"traceId":%q,"event":%q,"message":%q,"rawOnly":"raw-only-%s"}`,
			id,
			logTime,
			level,
			traceID,
			event,
			message,
			id,
		)
		return w6RuntimeLogFixture{
			ID:           id,
			LogFile:      "runtime-w6-integration.log",
			LogOffset:    int64(len(id) * 100),
			LineNumber:   len(id),
			Time:         logTime,
			Level:        level,
			TraceID:      traceID,
			Event:        event,
			Message:      message,
			ErrorMessage: errorMessage,
			RawJSON:      rawJSON,
			CreatedAt:    logTime,
		}
	}

	return []w6RuntimeLogFixture{
		fixture("rtlog_w6_runtime_target", -time.Hour, "error", "trace-runtime-alpha-001", "gateway.failed", "runtime-needle target", "upstream failed"),
		fixture("rtlog_w6_runtime_trace_sibling", -2*time.Hour, "debug", "trace-runtime-alpha-002", "gateway.completed", "trace sibling", ""),
		fixture("rtlog_w6_runtime_trace_near_miss", -90*time.Minute, "warn", "trace-runtime-alphb-001", "gateway.retry", "trace near miss", ""),
		fixture("mock_w6_runtime_level", -3*time.Hour, "info", "trace-runtime-level-001", "worker.tick", "level row", ""),
		fixture("rtlog_w6_runtime_event", -4*time.Hour, "info", "trace-runtime-event-001", "gateway.failed", "event row", ""),
		fixture("rtlog_w6_runtime_keyword_recent", -5*time.Hour, "info", "trace-runtime-keyword-001", "worker.flush", "runtime-needle recent", ""),
		fixture("rtlog_w6_runtime_keyword_old", -7*time.Hour, "info", "trace-runtime-keyword-002", "worker.flush", "runtime-needle old", ""),
		fixture("rtlog_w6_runtime_newest", -30*time.Minute, "fatal", "trace-runtime-newest-001", "process.fatal", "newest unrelated", "fatal fixture"),
	}
}

func w6RuntimeLogsNodePrerequisites(t *testing.T) (string, string) {
	t.Helper()

	nodePath, err := exec.LookPath("node")
	if err != nil {
		w6RuntimeLogsDependencyUnavailable(t, "node executable is unavailable: %v", err)
		return "", ""
	}
	nodePath, err = filepath.Abs(nodePath)
	if err != nil {
		t.Fatalf("resolve absolute node executable path: %v", err)
	}
	backendDir := filepath.Join(filepath.Dir(repoRoot(t)), "backend")
	if info, statErr := os.Stat(filepath.Join(backendDir, "src", "storage", "runtime-logs.repository.ts")); statErr != nil {
		t.Fatalf("stat production Node runtime logs repository: %v", statErr)
	} else if info.IsDir() {
		t.Fatal("production Node runtime logs repository path is a directory")
	}

	probeCtx, cancel := context.WithTimeout(t.Context(), w6RuntimeLogsProbeTimeout)
	defer cancel()
	stdout := newW6ManagementClientIPStatsBoundedOutput(w6RuntimeLogsOutputLimit)
	stderr := newW6ManagementClientIPStatsBoundedOutput(w6RuntimeLogsOutputLimit)
	baseEnvironment := w6ManagementClientIPStatsNodeWriterBaseEnvironment()
	w6ManagementClientIPStatsAssertIsolatedBaseEnvironment(t, baseEnvironment)
	probe := exec.CommandContext(probeCtx, nodePath, "--import", "tsx", "--eval", "")
	probe.Dir = backendDir
	probe.Env = baseEnvironment
	probe.Stdout = stdout
	probe.Stderr = stderr
	probe.WaitDelay = 3 * time.Second
	if err := probe.Run(); err != nil {
		w6RuntimeLogsDependencyUnavailable(
			t,
			"tsx loader probe failed: %v; stdout=%q stderr=%q",
			err,
			w6ManagementClientIPStatsBoundedRedactedOutput(stdout, ""),
			w6ManagementClientIPStatsBoundedRedactedOutput(stderr, ""),
		)
		return "", ""
	}
	if stdout.Truncated() || stderr.Truncated() {
		w6RuntimeLogsDependencyUnavailable(t, "tsx loader probe output exceeded bounded capture")
		return "", ""
	}
	return nodePath, backendDir
}

func w6RuntimeLogsDependencyUnavailable(t *testing.T, format string, args ...any) {
	t.Helper()

	message := fmt.Sprintf(format, args...)
	required, err := parseIntegrationRequirement(os.Getenv(requireIntegrationEnv))
	if err != nil {
		t.Fatalf("%s configuration is invalid: %v", requireIntegrationEnv, err)
	}
	if required {
		t.Fatalf("%s=1 but runtime logs integration dependency is unavailable: %s", requireIntegrationEnv, message)
	}
	t.Skipf("skip runtime logs Node writer to Go reader smoke because dependency is unavailable: %s", message)
}

func w6RuntimeLogsRunFreshMigrations(t *testing.T, db *sql.DB) {
	t.Helper()

	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	migrationDir := filepath.Join(repoRoot(t), "db", "migrations")
	if err := goose.UpTo(db, migrationDir, w6RuntimeLogsGooseTargetVersion); err != nil {
		t.Fatalf("goose up to %06d on fresh database: %v", w6RuntimeLogsGooseTargetVersion, err)
	}
	version, err := goose.GetDBVersion(db)
	if err != nil {
		t.Fatalf("read goose version: %v", err)
	}
	if version != w6RuntimeLogsGooseTargetVersion {
		t.Fatalf("goose version = %d, want exactly %d", version, w6RuntimeLogsGooseTargetVersion)
	}
}

func w6RuntimeLogsRunNodeWriter(
	t *testing.T,
	ctx context.Context,
	nodePath string,
	backendDir string,
	postgresURL string,
	fixtures []w6RuntimeLogFixture,
) {
	t.Helper()

	fixtureJSON, err := json.Marshal(fixtures)
	if err != nil {
		t.Fatalf("encode runtime log fixtures: %v", err)
	}
	script := `
(async () => {
  const fixtures = JSON.parse(process.env.JUHE_AI_NODE_GO_RUNTIME_LOGS_FIXTURE_JSON || '[]')
  const [{ createRuntimeLogsBatchAsync }, { closePostgresPool }] = await Promise.all([
    import('./src/storage/runtime-logs.repository.ts'),
    import('./src/storage/postgres-client.ts')
  ])
  try {
    await createRuntimeLogsBatchAsync(fixtures)
    console.log('` + w6RuntimeLogsNodeOutputPrefix + `' + JSON.stringify({ ids: fixtures.map((item) => item.id) }))
  } finally {
    await closePostgresPool()
  }
})().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
`
	nodeCtx, cancel := context.WithTimeout(ctx, w6RuntimeLogsNodeTimeout)
	defer cancel()
	stdout := newW6ManagementClientIPStatsBoundedOutput(w6RuntimeLogsOutputLimit)
	stderr := newW6ManagementClientIPStatsBoundedOutput(w6RuntimeLogsOutputLimit)
	command := exec.CommandContext(nodeCtx, nodePath, "--import", "tsx", "--eval", script)
	command.Dir = backendDir
	command.Env = append(w6ManagementClientIPStatsNodeWriterBaseEnvironment(),
		"NODE_ENV=test",
		"JUHE_AI_RUNTIME_MODE=performance",
		"JUHE_AI_DATABASE_DRIVER=postgres",
		"JUHE_AI_CACHE_DRIVER=redis",
		"JUHE_AI_RUNTIME_STATE_DRIVER=redis",
		"JUHE_AI_QUEUE_DRIVER=redis_stream",
		"JUHE_AI_POSTGRES_URL="+postgresURL,
		"JUHE_AI_REDIS_CACHE_URL=redis://127.0.0.1:1/0",
		"JUHE_AI_REDIS_STATE_URL=redis://127.0.0.1:1/1",
		"JUHE_AI_REDIS_QUEUE_URL=redis://127.0.0.1:1/2",
		"JUHE_AI_PROCESS_ROLE=worker",
		"JUHE_AI_WORKER_ROLE=ingest-worker",
		"JUHE_AI_LOG_FILE_ENABLED=false",
		"JUHE_AI_LOG_CONSOLE_ENABLED=false",
		"JUHE_AI_NODE_GO_RUNTIME_LOGS_FIXTURE_JSON="+string(fixtureJSON),
	)
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = 5 * time.Second
	if err := command.Run(); err != nil {
		t.Fatalf(
			"production Node runtime log writer failed: %v; stdout=%q stderr=%q",
			err,
			w6ManagementClientIPStatsBoundedRedactedOutput(stdout, postgresURL),
			w6ManagementClientIPStatsBoundedRedactedOutput(stderr, postgresURL),
		)
	}
	if stdout.Truncated() || stderr.Truncated() {
		t.Fatal("production Node runtime log writer output exceeded bounded capture")
	}
	var protocolRecords int
	for _, line := range strings.Split(stdout.String(), "\n") {
		if strings.HasPrefix(strings.TrimSuffix(line, "\r"), w6RuntimeLogsNodeOutputPrefix) {
			protocolRecords++
		}
	}
	if protocolRecords != 1 {
		t.Fatalf(
			"production Node runtime log writer protocol records = %d; stdout=%q stderr=%q",
			protocolRecords,
			w6ManagementClientIPStatsBoundedRedactedOutput(stdout, postgresURL),
			w6ManagementClientIPStatsBoundedRedactedOutput(stderr, postgresURL),
		)
	}
}

func w6RuntimeLogsAssertNodeWriterFacets(t *testing.T, ctx context.Context, db *sql.DB, want int) {
	t.Helper()

	var logs int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.runtime_logs`).Scan(&logs); err != nil {
		t.Fatalf("count Node-written runtime logs: %v", err)
	}
	if logs != want {
		t.Fatalf("Node-written runtime logs = %d, want %d", logs, want)
	}
	var facetCount int
	if err := db.QueryRowContext(ctx, `
SELECT total_count
FROM juhe_dataset.runtime_log_facet_summary
WHERE bucket_key = 'current'
`).Scan(&facetCount); err != nil {
		t.Fatalf("read Node-written runtime log facet summary: %v", err)
	}
	if facetCount != want {
		t.Fatalf("Node-written runtime log facet total = %d, want %d", facetCount, want)
	}
}

func w6RuntimeLogsInsertSessions(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()

	lastSeenAt := now.Add(-2 * time.Hour)
	_, err := db.ExecContext(ctx, `
INSERT INTO juhe_business.system_accounts (
  id, username, display_name, description, role, status, password_hash,
  must_change_password, image_generation_enabled, created_at, updated_at
) VALUES
  ('sys_w6_runtime_logs_admin', 'w6-runtime-logs-admin', 'W6 Runtime Logs Admin', NULL, 'admin', 'active', 'hash', false, false, $1, $1),
  ('sys_w6_runtime_logs_user', 'w6-runtime-logs-user', 'W6 Runtime Logs User', NULL, 'user', 'active', 'hash', false, false, $1, $1);
INSERT INTO juhe_business.system_sessions (
  id, system_account_id, token_hash, expires_at, created_at, last_seen_at
) VALUES
  ('sess_w6_runtime_logs_admin', 'sys_w6_runtime_logs_admin', $2, $3, $1, $4),
  ('sess_w6_runtime_logs_user', 'sys_w6_runtime_logs_user', $5, $3, $1, $4);
`, now, managementauth.HashSessionToken(w6RuntimeLogsAdminToken), now.Add(24*time.Hour), lastSeenAt, managementauth.HashSessionToken(w6RuntimeLogsUserToken))
	if err != nil {
		t.Fatalf("insert runtime logs accounts and sessions: %v", err)
	}
}

func w6RuntimeLogsAssertPermissions(t *testing.T, ctx context.Context, server *httptest.Server) {
	t.Helper()

	status, body := w6RuntimeLogsGET(t, ctx, server, "/__aisys__/api/runtime-logs", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("runtime logs anonymous status = %d, want 401; body=%s", status, body)
	}
	status, body = w6RuntimeLogsGET(t, ctx, server, "/__aisys__/api/runtime-logs", w6RuntimeLogsUserToken)
	if status != http.StatusForbidden || !strings.Contains(string(body), "需要管理员权限") {
		t.Fatalf("runtime logs non-admin status = %d, want 403; body=%s", status, body)
	}
}

func w6RuntimeLogsAssertDefaultList(
	t *testing.T,
	ctx context.Context,
	server *httptest.Server,
	fixtures []w6RuntimeLogFixture,
) {
	t.Helper()

	data, body := w6RuntimeLogsAdminList(t, ctx, server, url.Values{"pageSize": {"100"}})
	items := w6RuntimeLogsItems(t, data)
	if len(items) != len(fixtures) {
		t.Fatalf("default runtime logs items = %d, want %d; body=%s", len(items), len(fixtures), body)
	}
	if items[0]["id"] != "rtlog_w6_runtime_newest" || items[len(items)-1]["id"] != "rtlog_w6_runtime_keyword_old" {
		t.Fatalf("default runtime logs ordering = first %#v last %#v", items[0]["id"], items[len(items)-1]["id"])
	}
	for _, item := range items {
		if _, found := item["rawJson"]; found {
			t.Fatalf("runtime log list leaked rawJson: %#v", item)
		}
	}
	if strings.Contains(string(body), "raw-only-") || strings.Contains(string(body), `"rawJson"`) {
		t.Fatalf("runtime log list leaked raw payload: %s", body)
	}
	w6RuntimeLogsAssertListMetadata(t, data)
}

func w6RuntimeLogsAssertListMetadata(t *testing.T, data map[string]any) {
	t.Helper()

	for _, key := range []string{"elapsedMs", "retentionDays", "retentionDaysSource", "runtimeAvailable", "workerSnapshotAvailable", "runtimeLogIndexQueueAvailable"} {
		if _, found := data[key]; found {
			t.Fatalf("runtime log list must not expose progressive runtime field %s: %#v", key, data[key])
		}
	}
	if data["page"] != float64(1) || data["pageSize"] != float64(100) || data["hasMore"] != false || data["total"] != float64(8) {
		t.Fatalf("runtime log pagination metadata = %#v", data)
	}
}

func w6RuntimeLogsAssertFilters(t *testing.T, ctx context.Context, server *httptest.Server, now time.Time) {
	t.Helper()

	w6RuntimeLogsAssertListIDs(t, ctx, server, url.Values{"traceId": {"trace-runtime-alpha-"}}, []string{
		"rtlog_w6_runtime_target",
		"rtlog_w6_runtime_trace_sibling",
	})
	w6RuntimeLogsAssertListIDs(t, ctx, server, url.Values{"level": {"error"}}, []string{
		"rtlog_w6_runtime_target",
	})
	w6RuntimeLogsAssertListIDs(t, ctx, server, url.Values{"level": {"info"}}, []string{
		"mock_w6_runtime_level",
		"rtlog_w6_runtime_event",
		"rtlog_w6_runtime_keyword_recent",
		"rtlog_w6_runtime_keyword_old",
	})
	w6RuntimeLogsAssertListIDs(t, ctx, server, url.Values{"event": {"gateway.failed"}}, []string{
		"rtlog_w6_runtime_target",
		"rtlog_w6_runtime_event",
	})
	w6RuntimeLogsAssertListIDs(t, ctx, server, url.Values{"keyword": {"runtime-needle"}}, []string{
		"rtlog_w6_runtime_target",
		"rtlog_w6_runtime_keyword_recent",
	})
	w6RuntimeLogsAssertListIDs(t, ctx, server, url.Values{
		"traceId": {"trace-runtime-alpha-"},
		"level":   {"error"},
		"event":   {"gateway.failed"},
		"keyword": {"runtime-needle"},
		"startAt": {now.Add(-6 * time.Hour).Format(time.RFC3339Nano)},
		"endAt":   {now.Format(time.RFC3339Nano)},
	}, []string{"rtlog_w6_runtime_target"})
}

func w6RuntimeLogsAssertListIDs(
	t *testing.T,
	ctx context.Context,
	server *httptest.Server,
	query url.Values,
	want []string,
) {
	t.Helper()

	data, body := w6RuntimeLogsAdminList(t, ctx, server, query)
	items := w6RuntimeLogsItems(t, data)
	got := make([]string, 0, len(items))
	for _, item := range items {
		id, ok := item["id"].(string)
		if !ok {
			t.Fatalf("runtime log filtered item id = %#v; body=%s", item["id"], body)
		}
		got = append(got, id)
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("runtime log filter ids = %v, want %v; body=%s", got, want, body)
	}
}

func w6RuntimeLogsAssertDetailAndNotFound(
	t *testing.T,
	ctx context.Context,
	server *httptest.Server,
	wants ...w6RuntimeLogFixture,
) {
	t.Helper()

	for _, want := range wants {
		status, body := w6RuntimeLogsGET(t, ctx, server, "/__aisys__/api/runtime-logs/"+url.PathEscape(want.ID), w6RuntimeLogsAdminToken)
		if status != http.StatusOK {
			t.Fatalf("runtime log detail %q status = %d; body=%s", want.ID, status, body)
		}
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode runtime log detail %q: %v; body=%s", want.ID, err, body)
		}
		if envelope.Data["id"] != want.ID || envelope.Data["rawJson"] != want.RawJSON {
			t.Fatalf("runtime log detail = %#v, want id %q rawJson %q", envelope.Data, want.ID, want.RawJSON)
		}
	}

	status, body := w6RuntimeLogsGET(t, ctx, server, "/__aisys__/api/runtime-logs/rtlog_w6_missing", w6RuntimeLogsAdminToken)
	if status != http.StatusNotFound || !strings.Contains(string(body), "运行日志不存在") {
		t.Fatalf("runtime log missing detail status = %d, want 404; body=%s", status, body)
	}
}

func w6RuntimeLogsAssertSessionsUntouched(t *testing.T, ctx context.Context, db *sql.DB, want time.Time) {
	t.Helper()

	rows, err := db.QueryContext(ctx, `
SELECT id, last_seen_at
FROM juhe_business.system_sessions
WHERE id IN ('sess_w6_runtime_logs_admin', 'sess_w6_runtime_logs_user')
ORDER BY id
`)
	if err != nil {
		t.Fatalf("read runtime logs sessions after read requests: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id string
		var lastSeenAt time.Time
		if err := rows.Scan(&id, &lastSeenAt); err != nil {
			t.Fatalf("scan runtime logs session: %v", err)
		}
		count++
		if !lastSeenAt.UTC().Equal(want) {
			t.Fatalf("read-only runtime logs request touched session %s: last_seen_at=%s want=%s", id, lastSeenAt.UTC().Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate runtime logs sessions: %v", err)
	}
	if count != 2 {
		t.Fatalf("runtime logs sessions read = %d, want 2", count)
	}
}

func w6RuntimeLogsAdminList(
	t *testing.T,
	ctx context.Context,
	server *httptest.Server,
	query url.Values,
) (map[string]any, []byte) {
	t.Helper()

	path := "/__aisys__/api/runtime-logs"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	status, body := w6RuntimeLogsGET(t, ctx, server, path, w6RuntimeLogsAdminToken)
	if status != http.StatusOK {
		t.Fatalf("runtime log list status = %d; body=%s", status, body)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode runtime log list: %v; body=%s", err, body)
	}
	return envelope.Data, body
}

func w6RuntimeLogsItems(t *testing.T, data map[string]any) []map[string]any {
	t.Helper()

	rawItems, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("runtime log items = %#v", data["items"])
	}
	items := make([]map[string]any, 0, len(rawItems))
	for _, rawItem := range rawItems {
		item, ok := rawItem.(map[string]any)
		if !ok {
			t.Fatalf("runtime log item = %#v", rawItem)
		}
		items = append(items, item)
	}
	return items
}

func w6RuntimeLogsGET(
	t *testing.T,
	ctx context.Context,
	server *httptest.Server,
	path string,
	token string,
) (int, []byte) {
	t.Helper()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatalf("build runtime logs request: %v", err)
	}
	if token != "" {
		request.Header.Set("Cookie", managementauth.SessionCookieName+"="+token)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("request production Go runtime logs route: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, w6RuntimeLogsBodyLimit+1))
	if err != nil {
		t.Fatalf("read production Go runtime logs response: %v", err)
	}
	if len(body) > w6RuntimeLogsBodyLimit {
		t.Fatalf("production Go runtime logs response exceeded %d bytes", w6RuntimeLogsBodyLimit)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("runtime logs Cache-Control = %q, want no-store", response.Header.Get("Cache-Control"))
	}
	return response.StatusCode, body
}

func w6RuntimeLogsAssertExplainGates(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin runtime log EXPLAIN transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `ANALYZE juhe_dataset.runtime_logs`); err != nil {
		t.Fatalf("analyze runtime logs before EXPLAIN: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatalf("disable sequential scan for runtime log index availability proof: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL plan_cache_mode = force_generic_plan`); err != nil {
		t.Fatalf("force generic plans for runtime log index proof: %v", err)
	}

	for _, testCase := range []struct {
		name          string
		statementName string
		wantIndex     string
		input         port.ManagementRuntimeLogListInput
	}{
		{name: "default list", statementName: "w6_runtime_logs_default", wantIndex: "idx_runtime_logs_time"},
		{
			name:          "trace prefix",
			statementName: "w6_runtime_logs_trace",
			wantIndex:     "idx_runtime_logs_trace_c_time",
			input:         port.ManagementRuntimeLogListInput{TraceID: "trace-runtime-alpha-"},
		},
		{
			name:          "level",
			statementName: "w6_runtime_logs_level",
			wantIndex:     "idx_runtime_logs_level_time",
			input:         port.ManagementRuntimeLogListInput{Level: "error"},
		},
		{
			name:          "event",
			statementName: "w6_runtime_logs_event",
			wantIndex:     "idx_runtime_logs_event_time",
			input:         port.ManagementRuntimeLogListInput{Event: "gateway.failed"},
		},
		{
			name:          "keyword with time window",
			statementName: "w6_runtime_logs_keyword",
			wantIndex:     "idx_runtime_logs_time",
			input: port.ManagementRuntimeLogListInput{
				Keyword: "runtime-needle",
				StartAt: "2000-01-01T00:00:00.000Z",
				EndAt:   "2100-01-01T00:00:00.000Z",
			},
		},
	} {
		query, args := postgresstore.BuildManagementRuntimeLogListQueryForIntegration(testCase.input, 101, 0)
		w6RuntimeLogsAssertPreparedExplainIndex(
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

	var traceIndexDefinition string
	if err := tx.QueryRowContext(ctx, `
SELECT pg_get_indexdef(indexrelid)
FROM pg_index
WHERE indexrelid = 'juhe_dataset.idx_runtime_logs_trace_c_time'::regclass
`).Scan(&traceIndexDefinition); err != nil {
		t.Fatalf("read runtime log trace index definition: %v", err)
	}
	if !strings.Contains(traceIndexDefinition, `COLLATE "C"`) {
		t.Fatalf("runtime log trace index is missing C collation: %s", traceIndexDefinition)
	}
}

func w6RuntimeLogsAssertPreparedExplainIndex(
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

	prepareSQL := "PREPARE " + statementName + " AS\n" + query
	if _, err := tx.ExecContext(ctx, prepareSQL); err != nil {
		t.Fatalf("prepare runtime log %s: %v", name, err)
	}
	deallocateSQL := "DEALLOCATE " + statementName
	defer func() { _, _ = tx.ExecContext(ctx, deallocateSQL) }()

	explainSQL := "EXPLAIN (FORMAT JSON, COSTS false) EXECUTE " + statementName + "(" + w6RuntimeLogsSQLLiterals(t, args) + ")"
	var rawPlan []byte
	if err := tx.QueryRowContext(ctx, explainSQL).Scan(&rawPlan); err != nil {
		t.Fatalf("EXPLAIN runtime log %s: %v", name, err)
	}
	var plan any
	if err := json.Unmarshal(rawPlan, &plan); err != nil {
		t.Fatalf("decode runtime log %s EXPLAIN: %v; plan=%s", name, err, rawPlan)
	}
	indexes := map[string]bool{}
	w6RuntimeLogsCollectPlanIndexes(plan, indexes)
	if !indexes[wantIndex] {
		t.Fatalf("runtime log %s EXPLAIN indexes = %v, want %s; plan=%s", name, indexes, wantIndex, rawPlan)
	}
}

func w6RuntimeLogsSQLLiterals(t *testing.T, args []any) string {
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
		default:
			t.Fatalf("unsupported runtime log EXPLAIN argument type %T", arg)
		}
	}
	return strings.Join(literals, ", ")
}

func w6RuntimeLogsCollectPlanIndexes(value any, indexes map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		if indexName, ok := typed["Index Name"].(string); ok {
			indexes[indexName] = true
		}
		for _, nested := range typed {
			w6RuntimeLogsCollectPlanIndexes(nested, indexes)
		}
	case []any:
		for _, nested := range typed {
			w6RuntimeLogsCollectPlanIndexes(nested, indexes)
		}
	}
}
