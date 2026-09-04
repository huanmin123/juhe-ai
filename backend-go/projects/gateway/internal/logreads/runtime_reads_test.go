package logreads

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

var runtimeReadsDDL = []string{
	`CREATE TABLE IF NOT EXISTS runtime_logs (
	  id TEXT PRIMARY KEY, log_file TEXT, log_offset INTEGER, line_number INTEGER,
	  time TEXT NOT NULL, level TEXT NOT NULL, trace_id TEXT, event TEXT, message TEXT,
	  error_message TEXT, raw_json TEXT NOT NULL, created_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS runtime_log_facet_summary (
	  bucket_key TEXT PRIMARY KEY, total_count INTEGER NOT NULL DEFAULT 0,
	  earliest_time TEXT, latest_time TEXT, updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS runtime_log_level_facets (
	  bucket_key TEXT NOT NULL, level TEXT NOT NULL, count INTEGER NOT NULL DEFAULT 0,
	  updated_at TEXT NOT NULL, PRIMARY KEY (bucket_key, level)
	)`,
	`CREATE TABLE IF NOT EXISTS runtime_log_event_facets (
	  bucket_key TEXT NOT NULL, event TEXT NOT NULL, count INTEGER NOT NULL DEFAULT 0,
	  latest_time TEXT, updated_at TEXT NOT NULL, PRIMARY KEY (bucket_key, event)
	)`,
}

const runtimeReadsPinnedNow = "2026-06-03T13:00:00.000Z"

func seedRuntimeReads(t *testing.T, env *readsTestEnv) {
	t.Helper()
	env.exec(t, `INSERT INTO runtime_logs (id, log_file, log_offset, line_number, time, level, trace_id, event,
		message, error_message, raw_json, created_at) VALUES ('r-3', 'gateway.log', 300, 3,
		'2026-06-03T12:00:00.000Z', 'error', 'tr-3', 'gateway.request', 'boom failed', 'explode',
		'{"level":"error","message":"boom"}', '2026-06-03T12:00:01.000Z')`)
	env.exec(t, `INSERT INTO runtime_logs (id, log_file, log_offset, line_number, time, level, trace_id, event,
		message, raw_json, created_at) VALUES ('r-2', 'gateway.log', 200, 2, '2026-06-03T09:30:00.000Z', 'info',
		'tr-2', 'gateway.request', 'ok warnXlike', '{"level":"info","message":"ok"}', '2026-06-03T09:30:01.000Z')`)
	env.exec(t, `INSERT INTO runtime_logs (id, log_file, log_offset, line_number, time, level, trace_id, event,
		message, raw_json, created_at) VALUES ('r-1', 'scheduler.log', 100, 1, '2026-06-01T08:00:00.000Z', 'warn',
		'tr-1', 'scheduler.tick', 'warn%like_thing', '{"level":"warn"}', '2026-06-01T08:00:01.000Z')`)
	env.exec(t, `INSERT INTO runtime_log_facet_summary (bucket_key, total_count, earliest_time, latest_time, updated_at)
		VALUES ('current', 3, '2026-06-01T08:00:00.000Z', '2026-06-03T12:00:00.000Z', '2026-06-03T12:00:01.000Z')`)
	for _, level := range []string{"error", "info", "warn"} {
		env.exec(t, `INSERT INTO runtime_log_level_facets (bucket_key, level, count, updated_at)
			VALUES ('current', ?, 1, '2026-06-03T12:00:01.000Z')`, level)
	}
	env.exec(t, `INSERT INTO runtime_log_event_facets (bucket_key, event, count, latest_time, updated_at)
		VALUES ('current', 'gateway.request', 2, '2026-06-03T12:00:00.000Z', '2026-06-03T12:00:01.000Z')`)
	env.exec(t, `INSERT INTO runtime_log_event_facets (bucket_key, event, count, latest_time, updated_at)
		VALUES ('current', 'scheduler.tick', 1, '2026-06-01T08:00:00.000Z', '2026-06-01T08:00:01.000Z')`)
}

// pinRuntimeReadsNow pins the keyword window clock on the concrete reader.
func pinRuntimeReadsNow(audit AuditLogReader, runtime RuntimeLogReader, public PublicApiLogReader) {
	_ = audit
	_ = public
	concrete, ok := runtime.(*runtimeLogSQLReader)
	if !ok {
		return
	}
	pinned, err := time.Parse(time.RFC3339Nano, runtimeReadsPinnedNow)
	if err != nil {
		panic(err)
	}
	concrete.Now = func() time.Time { return pinned }
}

func TestRuntimeLogReadsList(t *testing.T) {
	env := newReadsTestEnv(t, runtimeReadsDDL, pinRuntimeReadsNow, true)
	seedRuntimeReads(t, env)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs", "")
	if code != http.StatusOK {
		t.Fatalf("list status: %d %v", code, payload)
	}
	data := wantData(t, payload)
	items := wantItems(t, data)
	if len(items) != 3 {
		t.Fatalf("items: %v", items)
	}
	if id := wantString(t, items[0], "id"); id != "r-3" {
		t.Fatalf("expected time DESC order (r-3 first), got %s", id)
	}
	if id := wantString(t, items[2], "id"); id != "r-1" {
		t.Fatalf("expected r-1 last, got %s", id)
	}
	first := items[0]
	if wantString(t, first, "time") != "2026-06-03T12:00:00.000Z" || wantString(t, first, "level") != "error" ||
		wantString(t, first, "traceId") != "tr-3" || wantString(t, first, "event") != "gateway.request" ||
		wantString(t, first, "message") != "boom failed" || wantString(t, first, "errorMessage") != "explode" {
		t.Fatalf("item mapping: %v", first)
	}
	if _, exists := items[1]["errorMessage"]; exists {
		t.Fatalf("absent error_message must be omitted: %v", items[1])
	}
	if wantFloat(t, data, "total") != 3 || wantFloat(t, data, "pageSize") != 100 || wantBool(t, data, "hasMore") {
		t.Fatalf("pagination envelope: %v", data)
	}

	// Level filter, case-insensitive with lowercase storage, 'all' = no filter.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs?level=ERROR", "")
	if code != http.StatusOK {
		t.Fatalf("level filter status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 1 || wantString(t, items[0], "id") != "r-3" {
		t.Fatalf("level filter items: %v", items)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs?level=all", "")
	if code != http.StatusOK {
		t.Fatalf("level=all status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 3 {
		t.Fatalf("level=all items: %v", items)
	}

	// Prefix and exact filters.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs?traceId=tr-3", "")
	if code != http.StatusOK {
		t.Fatalf("traceId filter status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 1 || wantString(t, items[0], "id") != "r-3" {
		t.Fatalf("traceId filter items: %v", items)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs?event=scheduler.tick", "")
	if code != http.StatusOK {
		t.Fatalf("event filter status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 1 || wantString(t, items[0], "id") != "r-1" {
		t.Fatalf("event filter items: %v", items)
	}

	// Keyword: default 6h window from the pinned now (2026-06-03T13:00Z).
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs?keyword=boom", "")
	if code != http.StatusOK {
		t.Fatalf("keyword status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 1 || wantString(t, items[0], "id") != "r-3" {
		t.Fatalf("keyword items: %v", items)
	}
	// r-1 is older than the 6h window so the keyword search hides it even
	// though its message contains the term; r-2 stays inside the window.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs?keyword=warn", "")
	if code != http.StatusOK {
		t.Fatalf("windowed keyword status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 1 || wantString(t, items[0], "id") != "r-2" {
		t.Fatalf("keyword window should hide r-1 only: %v", items)
	}
	// An explicit range bypasses the window; LIKE escaping keeps % literal, so
	// only r-1 (warn%like_thing) matches, not r-2 (ok warnXlike).
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs?keyword=warn%25like&startAt=2026-06-01T00:00:00Z", "")
	if code != http.StatusOK {
		t.Fatalf("escaped keyword status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 1 || wantString(t, items[0], "id") != "r-1" {
		t.Fatalf("LIKE escaping items: %v", items)
	}

	// Strict time bounds.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs?endAt=06/01/2026", "")
	if code != http.StatusBadRequest {
		t.Fatalf("invalid endAt status: %d %v", code, payload)
	}
	if message := wantString(t, payload, "message"); !strings.Contains(message, "结束时间") {
		t.Fatalf("invalid endAt message: %q", message)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs?startAt=2026-06-02T00:00:00Z", "")
	if code != http.StatusOK {
		t.Fatalf("range status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 2 {
		t.Fatalf("range items: %v", items)
	}

	// Page size clamps at 100.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs?pageSize=500&page=99", "")
	if code != http.StatusOK {
		t.Fatalf("clamp status: %d", code)
	}
	data = wantData(t, payload)
	if wantFloat(t, data, "pageSize") != 100 || wantFloat(t, data, "page") != 10 {
		t.Fatalf("page/pageSize clamps: %v", data)
	}
}

func TestRuntimeLogReadsFacetsAndDetail(t *testing.T) {
	env := newReadsTestEnv(t, runtimeReadsDDL, pinRuntimeReadsNow, true)
	seedRuntimeReads(t, env)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/facets", "")
	if code != http.StatusOK {
		t.Fatalf("facets status: %d %v", code, payload)
	}
	facets := wantData(t, payload)
	if wantFloat(t, facets, "retentionDays") != 14 || wantFloat(t, facets, "totalIndexed") != 3 {
		t.Fatalf("facets header: %v", facets)
	}
	if wantString(t, facets, "earliestIndexedAt") != "2026-06-01T08:00:00.000Z" ||
		wantString(t, facets, "latestIndexedAt") != "2026-06-03T12:00:00.000Z" {
		t.Fatalf("facets range: %v", facets)
	}
	levels, ok := facets["levels"].([]any)
	if !ok || len(levels) != 3 {
		t.Fatalf("levels: %v", facets["levels"])
	}
	// count DESC, level ASC → all counts equal so alphabetical.
	if first := levels[0].(map[string]any); wantString(t, first, "value") != "error" || wantFloat(t, first, "count") != 1 {
		t.Fatalf("levels order: %v", levels)
	}
	events, ok := facets["events"].([]any)
	if !ok || len(events) != 2 {
		t.Fatalf("events: %v", facets["events"])
	}
	if events[0].(string) != "gateway.request" || events[1].(string) != "scheduler.tick" {
		t.Fatalf("events order: %v", events)
	}

	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/r-3", "")
	if code != http.StatusOK {
		t.Fatalf("detail status: %d %v", code, payload)
	}
	detail := wantData(t, payload)
	if wantString(t, detail, "id") != "r-3" || wantString(t, detail, "rawJson") != `{"level":"error","message":"boom"}` {
		t.Fatalf("detail delta mapping: %v", detail)
	}
	if _, exists := detail["level"]; exists {
		t.Fatalf("detail delta must not include summary columns: %v", detail)
	}

	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/nope", "")
	if code != http.StatusNotFound {
		t.Fatalf("missing detail status: %d", code)
	}
	if message := wantString(t, payload, "message"); message != "运行日志不存在" {
		t.Fatalf("missing detail message: %q", message)
	}
}

func TestRuntimeLogReadsRetentionClamp(t *testing.T) {
	env := newReadsTestEnv(t, runtimeReadsDDL, func(_ AuditLogReader, runtime RuntimeLogReader, _ PublicApiLogReader) {
		if concrete, ok := runtime.(*runtimeLogSQLReader); ok {
			concrete.RetentionDays = func() int { return 500 }
		}
	}, true)
	seedRuntimeReads(t, env)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/runtime-logs/facets", "")
	if code != http.StatusOK {
		t.Fatalf("facets status: %d", code)
	}
	if days := wantFloat(t, wantData(t, payload), "retentionDays"); days != 90 {
		t.Fatalf("retentionDays should clamp to 90: %v", payload)
	}
}
