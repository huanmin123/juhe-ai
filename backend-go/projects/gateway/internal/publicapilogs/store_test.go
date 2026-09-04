package publicapilogs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// manualClock is the injected clock shared by the store/pipeline/retention
// tests.
type manualClock struct {
	current time.Time
}

func (c *manualClock) Now() time.Time { return c.current }

func (c *manualClock) Advance(d time.Duration) { c.current = c.current.Add(d) }

const publicAPILogsDDL = `CREATE TABLE IF NOT EXISTS public_api_logs (
	id TEXT PRIMARY KEY,
	trace_id TEXT,
	source_ref_id TEXT,
	source_name TEXT,
	token_id TEXT,
	token_name TEXT,
	token_prefix TEXT,
	is_test_token INTEGER NOT NULL DEFAULT 0,
	method TEXT NOT NULL,
	path TEXT NOT NULL,
	query_string TEXT,
	client_ip TEXT,
	user_agent TEXT,
	status_code INTEGER,
	success INTEGER NOT NULL DEFAULT 0,
	duration_ms INTEGER,
	request_size_bytes INTEGER NOT NULL DEFAULT 0,
	response_size_bytes INTEGER NOT NULL DEFAULT 0,
	request_capture_status TEXT NOT NULL DEFAULT 'empty',
	response_capture_status TEXT NOT NULL DEFAULT 'empty',
	request_data_json TEXT NOT NULL DEFAULT '{}',
	response_data_json TEXT NOT NULL DEFAULT '{}',
	error_code TEXT,
	error_message TEXT,
	started_at TEXT NOT NULL,
	ended_at TEXT NOT NULL,
	created_at TEXT NOT NULL
)`

func newTestStore(t *testing.T) (*Store, *sql.DB, *manualClock) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:publicapilogs-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(publicAPILogsDDL); err != nil {
		t.Fatal(err)
	}
	clock := &manualClock{current: fixedTime(t, "2026-09-04T08:00:00Z")}
	store, err := NewStore(db, false, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store, db, clock
}

// countRows returns the number of rows matching an optional trace prefix.
func countRows(t *testing.T, db *sql.DB, tracePrefix string) int {
	t.Helper()
	var count int
	query := "SELECT COUNT(*) FROM public_api_logs"
	args := []any{}
	if tracePrefix != "" {
		query += " WHERE trace_id >= ? AND trace_id < ?"
		args = append(args, tracePrefix, textPrefixUpperBoundForTest(tracePrefix))
	}
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func textPrefixUpperBoundForTest(value string) string {
	runes := []rune(value)
	for index := len(runes) - 1; index >= 0; index-- {
		if runes[index] < 0x10ffff {
			runes[index]++
			return string(runes[:index+1])
		}
	}
	return value + "\uffff"
}

// TestStoreInsertBatchNormalization verifies row defaults and normalization
// against the Node repository.
func TestStoreInsertBatchNormalization(t *testing.T) {
	store, db, _ := newTestStore(t)
	ctx := context.Background()

	full := Input{
		ID: "publog_full", TraceID: "tr-full", SourceRefID: "ref-1", SourceName: "src",
		TokenID: "tok", TokenName: "name", TokenPrefix: "pre", IsTestToken: true,
		Method: "post", Path: "/p", QueryString: "a=1", ClientIP: "10.0.0.1", UserAgent: "ua",
		StatusCode: 201, Success: true, DurationMS: 12.7,
		RequestSizeBytes: 10, ResponseSizeBytes: -5,
		RequestCaptureStatus:  CaptureStatusComplete,
		ResponseCaptureStatus: CaptureStatus("bogus"),
		RequestData:           map[string]any{"a": 1.0},
		ResponseData:          map[string]any{"b": true},
		ErrorCode:             "e", ErrorMessage: "m",
		StartedAt: "2026-09-01T10:00:00.000Z", EndedAt: "2026-09-01T10:00:00.010Z",
		CreatedAt: "2026-09-01T10:00:00.010Z",
	}
	minimal := Input{Method: "GET", Path: "/x", StartedAt: "s", EndedAt: "e"}

	if err := store.InsertBatch(ctx, []Input{full, minimal}); err != nil {
		t.Fatal(err)
	}

	var traceID, sourceRefID, sourceName, method, queryString, userAgent, errorCode sql.NullString
	var isTestToken, success, statusCode, durationMS, requestSize, responseSize int64
	var requestStatus, responseDataJSON string
	if err := db.QueryRow(`SELECT trace_id, source_ref_id, source_name, method, is_test_token, success,
		status_code, duration_ms, request_size_bytes, response_size_bytes, request_capture_status,
		query_string, user_agent, error_code, response_data_json
		FROM public_api_logs WHERE id = 'publog_full'`).Scan(
		&traceID, &sourceRefID, &sourceName, &method, &isTestToken, &success, &statusCode, &durationMS,
		&requestSize, &responseSize, &requestStatus, &queryString, &userAgent, &errorCode, &responseDataJSON); err != nil {
		t.Fatal(err)
	}
	if !traceID.Valid || traceID.String != "tr-full" || !sourceRefID.Valid || sourceName.String != "src" {
		t.Fatalf("text columns: %v %v %v", traceID, sourceRefID, sourceName)
	}
	if method.String != "post" || isTestToken != 1 || success != 1 || statusCode != 201 {
		t.Fatalf("flags: %s %d %d %d", method.String, isTestToken, success, statusCode)
	}
	if durationMS != 12 { // integerOrNull truncates
		t.Fatalf("duration_ms: %d", durationMS)
	}
	if requestSize != 10 || responseSize != 0 { // negative normalizes to 0
		t.Fatalf("sizes: %d/%d", requestSize, responseSize)
	}
	if requestStatus != "complete" {
		t.Fatalf("request status: %q", requestStatus)
	}
	if responseDataJSON != `{"b":true}` {
		t.Fatalf("response_data_json: %s", responseDataJSON)
	}

	var generatedID, createdAt, requestJSON string
	var defaultStatus string
	if err := db.QueryRow(`SELECT id, created_at, request_data_json, response_capture_status
		FROM public_api_logs WHERE trace_id IS NULL`).Scan(&generatedID, &createdAt, &requestJSON, &defaultStatus); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(generatedID, "publog_") {
		t.Fatalf("generated id: %q", generatedID)
	}
	if createdAt != "2026-09-04T08:00:00.000Z" {
		t.Fatalf("created_at default must come from the injected clock: %q", createdAt)
	}
	if requestJSON != "{}" {
		t.Fatalf("nil request data must store {}: %q", requestJSON)
	}
	if defaultStatus != "empty" {
		t.Fatalf("unknown capture status normalizes to empty: %q", defaultStatus)
	}
}

// TestStoreInsertBatchEmptyAndRollback covers the batch contract: empty is a
// no-op and a failing row leaves no partial rows.
func TestStoreInsertBatchEmptyAndRollback(t *testing.T) {
	store, db, _ := newTestStore(t)
	ctx := context.Background()
	if err := store.InsertBatch(ctx, nil); err != nil {
		t.Fatalf("empty batch: %v", err)
	}

	// Duplicate primary key fails the whole transaction.
	batch := []Input{
		{ID: "dup", Method: "GET", Path: "/a", StartedAt: "s", EndedAt: "e"},
		{ID: "dup", Method: "GET", Path: "/b", StartedAt: "s", EndedAt: "e"},
	}
	if err := store.InsertBatch(ctx, batch); err == nil {
		t.Fatal("duplicate id must fail the batch")
	}
	if count := countRows(t, db, ""); count != 0 {
		t.Fatalf("failed batch must not leave partial rows, got %d", count)
	}
}

// TestStoreCleanupBeforeConditions covers the retention repository contract:
// strictly created_at < cutoff, limit honored, stable oldest-first order.
func TestStoreCleanupBeforeConditions(t *testing.T) {
	store, db, _ := newTestStore(t)
	ctx := context.Background()

	insert := func(id, createdAt string) {
		if err := store.InsertBatch(ctx, []Input{{ID: id, TraceID: id, Method: "GET", Path: "/x", StartedAt: createdAt, EndedAt: createdAt, CreatedAt: createdAt}}); err != nil {
			t.Fatal(err)
		}
	}
	insert("old-1", "2026-01-01T00:00:00.000Z")
	insert("old-2", "2026-02-01T00:00:00.000Z")
	insert("boundary", "2026-03-01T00:00:00.000Z") // equal to cutoff: survives
	insert("new", "2026-03-01T00:00:00.001Z")
	deleted, err := store.CleanupBefore(ctx, "2026-03-01T00:00:00.000Z", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Fatalf("strictly-older cutoff must delete exactly the 2 old rows, got %d", deleted)
	}
	if count := countRows(t, db, ""); count != 2 {
		t.Fatalf("remaining rows: %d", count)
	}
	var survived string
	if err := db.QueryRow(`SELECT id FROM public_api_logs WHERE created_at = '2026-03-01T00:00:00.000Z'`).Scan(&survived); err != nil {
		t.Fatalf("cutoff-equal row must survive: %v", err)
	}

	// Limit is honored and picks the oldest first.
	for i := 0; i < 12; i++ {
		insert(fmt.Sprintf("bounded-%02d", i), "2025-01-01T00:00:00.000Z")
	}
	deleted, err = store.CleanupBefore(ctx, "2026-09-04T00:00:00.000Z", 5)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 5 {
		t.Fatalf("limit must cap the batch, got %d", deleted)
	}
	if count := countRows(t, db, "bounded-"); count != 7 {
		t.Fatalf("bounded rows remaining: %d", count)
	}
	var oldest string
	if err := db.QueryRow(`SELECT id FROM public_api_logs WHERE trace_id LIKE 'bounded-%' ORDER BY id ASC LIMIT 1`).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if oldest != "bounded-05" {
		t.Fatalf("oldest five must go first, remaining oldest: %q", oldest)
	}

	// limit < 1 clamps to one row.
	deleted, err = store.CleanupBefore(ctx, "2026-09-04T00:00:00.000Z", 0)
	if err != nil || deleted != 1 {
		t.Fatalf("limit 0 clamps to 1: %d %v", deleted, err)
	}
	// Nothing to delete.
	deleted, err = store.CleanupBefore(ctx, "2000-01-01T00:00:00.000Z", 100)
	if err != nil || deleted != 0 {
		t.Fatalf("empty cleanup: %d %v", deleted, err)
	}
}

// TestStorePostgresSQLShape pins the dual-mode SQL generation without a live
// PostgreSQL: juhe_dataset qualification, $n ordinals and the idempotency
// clause.
func TestStorePostgresSQLShape(t *testing.T) {
	clock := &manualClock{current: fixedTime(t, "2026-09-04T08:00:00Z")}
	store, err := NewStore(nil, true, clock.Now, nil)
	if err == nil {
		t.Fatal("nil db must be rejected")
	}
	_ = store

	s := &Store{pg: true}
	if got := s.table("public_api_logs"); got != "juhe_dataset.public_api_logs" {
		t.Fatalf("pg table: %q", got)
	}
	bound := s.bind("INSERT INTO t (a, b) VALUES (?, ?) ON CONFLICT(id) DO NOTHING")
	if bound != "INSERT INTO t (a, b) VALUES ($1, $2) ON CONFLICT(id) DO NOTHING" {
		t.Fatalf("bind: %q", bound)
	}
	placeholders := multiRowPlaceholders(2, 3)
	if placeholders != "(?, ?, ?), (?, ?, ?)" {
		t.Fatalf("placeholders: %q", placeholders)
	}
}

// TestStoreNewIDFormat mirrors newId('publog').
func TestStoreNewIDFormat(t *testing.T) {
	clock := &manualClock{current: fixedTime(t, "2026-09-04T08:00:00Z")}
	store, _, _ := newTestStore(t)
	got := store.newID("publog")
	wantPrefix := "publog_" + strconv.FormatInt(clock.current.UnixMilli(), 10) + "_"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("id %q must mirror newId prefix+millis (%q...)", got, wantPrefix)
	}
	if len(got) != len(wantPrefix)+8 {
		t.Fatalf("id suffix must be 8 hex chars: %q", got)
	}
}

// --- retention ---

type mockCleanupStore struct {
	mu        sync.Mutex
	calls     []string
	deleted   []int
	limit     []int
	failAt    int // 1-based call to fail with errFailed
	errFailed error
}

func (m *mockCleanupStore) CleanupBefore(_ context.Context, cutoff string, limit int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, cutoff)
	m.limit = append(m.limit, limit)
	index := len(m.calls)
	if m.failAt == index {
		return 0, m.errFailed
	}
	deleted := m.deleted[min(index, len(m.deleted))-1]
	return deleted, nil
}

// TestRetentionRunOnceConditions is table-driven over the cleanup loop.
func TestRetentionRunOnceConditions(t *testing.T) {
	tests := []struct {
		name              string
		deletedPerBatch   []int
		maxBatchesReached int
		wantTotal         int
		wantCalls         int
		wantSleeps        int
	}{
		{"non-full batch ends the run", []int{1000, 1000, 5}, RetentionMaxBatchesPerRun, 2005, 3, 2},
		{"max batches caps the run", []int{1000}, RetentionMaxBatchesPerRun, 20000, 20, 20},
		{"empty first batch ends immediately", []int{0}, RetentionMaxBatchesPerRun, 0, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &manualClock{current: fixedTime(t, "2026-09-04T08:00:00Z")}
			store := &mockCleanupStore{deleted: tt.deletedPerBatch}
			sleeps := 0
			retention := NewRetention(store, func(context.Context) (map[string]any, error) {
				return map[string]any{"publicApiLogRetentionDays": 30}, nil
			}, clock.Now, func(context.Context) error {
				sleeps++
				return nil
			})
			total, err := retention.RunOnce(context.Background())
			if err != nil {
				t.Fatalf("RunOnce: %v", err)
			}
			if total != tt.wantTotal {
				t.Fatalf("total: %d", total)
			}
			if len(store.calls) != tt.wantCalls {
				t.Fatalf("calls: %d", len(store.calls))
			}
			if sleeps != tt.wantSleeps {
				t.Fatalf("sleeps: %d", sleeps)
			}
			for _, cutoff := range store.calls {
				wantCutoff := "2026-08-05T08:00:00.000Z" // now - 30d
				if cutoff != wantCutoff {
					t.Fatalf("cutoff %q, want %q", cutoff, wantCutoff)
				}
			}
			for _, limit := range store.limit {
				if limit != RetentionBatchSize {
					t.Fatalf("limit: %d", limit)
				}
			}
		})
	}
}

// TestRetentionSettingValidation mirrors settingNumber's fail-closed errors.
func TestRetentionSettingValidation(t *testing.T) {
	clock := &manualClock{current: fixedTime(t, "2026-09-04T08:00:00Z")}
	tests := []struct {
		name     string
		settings map[string]any
		wantErr  string
	}{
		{"missing key", map[string]any{}, "系统设置 publicApiLogRetentionDays 必须是整数"},
		{"non integer", map[string]any{"publicApiLogRetentionDays": "30"}, "系统设置 publicApiLogRetentionDays 必须是整数"},
		{"below range", map[string]any{"publicApiLogRetentionDays": 0}, "系统设置 publicApiLogRetentionDays 必须在 1 到 365 之间"},
		{"above range", map[string]any{"publicApiLogRetentionDays": 366}, "系统设置 publicApiLogRetentionDays 必须在 1 到 365 之间"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockCleanupStore{deleted: []int{0}}
			retention := NewRetention(store, func(context.Context) (map[string]any, error) {
				return tt.settings, nil
			}, clock.Now, nil)
			_, err := retention.RunOnce(context.Background())
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("error %v, want %q", err, tt.wantErr)
			}
			if len(store.calls) != 0 {
				t.Fatal("invalid settings must not touch the store")
			}
		})
	}
	// Integer-valued floats are accepted (Number.isInteger parity).
	if days, err := SettingNumber(map[string]any{"k": float64(30)}, "k", 1, 365); err != nil || days != 30 {
		t.Fatalf("float64 integer: %d %v", days, err)
	}
}

// TestRetentionErrorPropagation covers the abnormal paths.
func TestRetentionErrorPropagation(t *testing.T) {
	clock := &manualClock{current: fixedTime(t, "2026-09-04T08:00:00Z")}
	failure := errors.New("cleanup boom")
	store := &mockCleanupStore{deleted: []int{0}, failAt: 1, errFailed: failure}
	retention := NewRetention(store, func(context.Context) (map[string]any, error) {
		return map[string]any{"publicApiLogRetentionDays": 30}, nil
	}, clock.Now, nil)
	if _, err := retention.RunOnce(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("store failure must propagate: %v", err)
	}

	settingsFailure := errors.New("settings boom")
	retention = NewRetention(store, func(context.Context) (map[string]any, error) {
		return nil, settingsFailure
	}, clock.Now, nil)
	if _, err := retention.RunOnce(context.Background()); !errors.Is(err, settingsFailure) {
		t.Fatalf("settings failure must propagate: %v", err)
	}

	// Canceled context stops the loop before touching the store.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	retention = NewRetention(store, func(context.Context) (map[string]any, error) {
		return map[string]any{"publicApiLogRetentionDays": 30}, nil
	}, clock.Now, nil)
	if _, err := retention.RunOnce(ctx); err == nil {
		t.Fatal("canceled context must abort the run")
	}
}

// TestRetentionEndToEndSQLite runs the retention against the real store.
func TestRetentionEndToEndSQLite(t *testing.T) {
	store, db, clock := newTestStore(t)
	ctx := context.Background()
	insert := func(id, createdAt string) {
		if err := store.InsertBatch(ctx, []Input{{ID: id, Method: "GET", Path: "/x", StartedAt: createdAt, EndedAt: createdAt, CreatedAt: createdAt}}); err != nil {
			t.Fatal(err)
		}
	}
	insert("expired", "2026-08-01T00:00:00.000Z")
	insert("fresh", "2026-09-01T00:00:00.000Z")

	retention := NewRetention(store, func(context.Context) (map[string]any, error) {
		return map[string]any{"publicApiLogRetentionDays": 30}, nil
	}, clock.Now, nil)
	total, err := retention.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("only the expired record goes: %d", total)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM public_api_logs WHERE id = 'fresh'`).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("fresh record must survive: %d %v", remaining, err)
	}
}
