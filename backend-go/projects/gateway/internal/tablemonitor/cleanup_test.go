package tablemonitor

// 清理 POST 与参数校验契约测试（Node table-monitor.routes.ts）：
//   - cleanup 的 zod 逐字节 400 文案、未来截止 400、入队回执
//     {cutoffAt, queued, jobId, submittedAt[, blockedReason]}、mutationGuard
//     409、操作日志 entry；
//   - record_maintenance_jobs 持久通道（enqueue.go）落行与失败回执；
//   - overview/history 的 zod coerce 语义（keyword 上界、refresh 布尔、
//     空串分页、limit、时间空串）；
//   - SWR 后台刷新存活于请求取消之后（routes.go WithoutCancel 时序回归）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

type fakeDispatch struct {
	mu   sync.Mutex
	jobs []RecordMaintenanceJob
	fail bool
}

func (f *fakeDispatch) EnqueueNonBusinessDataCleanup(_ context.Context, job RecordMaintenanceJob) DispatchResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return DispatchResult{Queued: false, DroppedReason: "worker_dispatch_failed"}
	}
	f.jobs = append(f.jobs, job)
	return DispatchResult{Queued: true}
}

type fakeOperationSink struct {
	entries []authsys.OperationLogEntry
}

func (f *fakeOperationSink) Record(entry authsys.OperationLogEntry, _ *http.Request) {
	f.entries = append(f.entries, entry)
}

// fakeOverviewStore 阻塞式 LoadOverview，驱动 SWR 后台刷新时序。
type fakeOverviewStore struct {
	mu       sync.Mutex
	calls    int
	gate     chan struct{}
	response Overview
	err      error
}

func (f *fakeOverviewStore) LoadOverview(ctx context.Context, _, _ int, _ string) (Overview, error) {
	f.mu.Lock()
	f.calls++
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return Overview{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return Overview{}, f.err
	}
	return f.response, nil
}

func (f *fakeOverviewStore) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeOverviewStore) LoadTableHistory(context.Context, string, string, string, string, int) ([]TableHistoryPoint, error) {
	return nil, nil
}

func (f *fakeOverviewStore) LoadDatabaseHistory(context.Context, string, string, int) ([]DatabaseHistoryPoint, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func cleanupDeps(dispatch RecordMaintenanceDispatch, sink authsys.OperationLogSink) *Deps {
	return &Deps{Dispatch: dispatch, Sink: sink}
}

func postCleanup(t *testing.T, deps *Deps, body map[string]any, actor string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/__aisys__/api/table-monitor/non-business-data/cleanup", strings.NewReader(string(raw)))
	request.Header.Set("Content-Type", "application/json")
	if actor != "" {
		request = request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: actor, Role: "admin"}))
	}
	recorder := httptest.NewRecorder()
	deps.cleanupGuard()(http.HandlerFunc(deps.cleanupHandler)).ServeHTTP(recorder, request)
	return recorder
}

func decodeCleanupReceipt(t *testing.T, recorder *httptest.ResponseRecorder) cleanupReceipt {
	t.Helper()
	var payload struct {
		Data cleanupReceipt `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode receipt: %v body=%s", err, recorder.Body.String())
	}
	return payload.Data
}

// ---------------------------------------------------------------------------
// cleanup POST
// ---------------------------------------------------------------------------

func TestCleanupEnqueuesAndReturnsReceipt(t *testing.T) {
	dispatch := &fakeDispatch{}
	sink := &fakeOperationSink{}
	deps := cleanupDeps(dispatch, sink)
	recorder := postCleanup(t, deps, map[string]any{"cutoffAt": "2026-09-01T02:00:00+08:00"}, "sys-admin-1")
	if recorder.Code != http.StatusOK {
		t.Fatalf("cleanup not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	receipt := decodeCleanupReceipt(t, recorder)
	// offset 输入被规范化为 UTC ISO（Node toISOString transform）。
	if receipt.CutoffAt != "2026-08-31T18:00:00.000Z" {
		t.Fatalf("cutoffAt not canonicalized: %q", receipt.CutoffAt)
	}
	if !receipt.Queued || receipt.JobID == "" || receipt.SubmittedAt == "" || receipt.BlockedReason != nil {
		t.Fatalf("receipt wrong: %#v", receipt)
	}
	if !strings.HasPrefix(receipt.JobID, "recmaint_") {
		t.Fatalf("jobId shape wrong: %q", receipt.JobID)
	}
	dispatch.mu.Lock()
	jobs := append([]RecordMaintenanceJob{}, dispatch.jobs...)
	dispatch.mu.Unlock()
	if len(jobs) != 1 {
		t.Fatalf("job count wrong: %d", len(jobs))
	}
	if jobs[0].CutoffAt != receipt.CutoffAt || jobs[0].BatchSize != defaultCleanupBatchSize || jobs[0].MaxBatches != defaultCleanupMaxBatches {
		t.Fatalf("job payload wrong: %#v", jobs[0])
	}
	if len(sink.entries) != 1 {
		t.Fatalf("operation log missing: %d", len(sink.entries))
	}
	entry := sink.entries[0]
	if entry.Module != "table_monitor" || entry.Action != "cleanup_non_business_data" || entry.OperationKey != cleanupOperationKey {
		t.Fatalf("operation log identity wrong: %#v", entry)
	}
	if entry.Mode != "admin" || entry.DetailLevel != "full" || entry.VisibilityScope != "admin_only" {
		t.Fatalf("operation log facets wrong: %#v", entry)
	}
	if entry.ResourceType != "non_business_data" || entry.ResourceID != "dataset_stats_usage_shards" || entry.ResourceName != "非业务数据" {
		t.Fatalf("operation log resource wrong: %#v", entry)
	}
	if entry.Summary != "提交非业务数据硬清理任务："+receipt.JobID {
		t.Fatalf("summary wrong: %q", entry.Summary)
	}
	if entry.ActorSystemAccountID != "sys-admin-1" {
		t.Fatalf("actor wrong: %q", entry.ActorSystemAccountID)
	}
	if len(entry.Changes) != 5 {
		t.Fatalf("changes wrong: %#v", entry.Changes)
	}
	var metadata map[string]any
	if err := json.Unmarshal(entry.Metadata, &metadata); err != nil {
		t.Fatalf("metadata decode: %v", err)
	}
	if metadata["queued"] != true || metadata["batchSize"] != float64(defaultCleanupBatchSize) {
		t.Fatalf("metadata wrong: %#v", metadata)
	}
	// Node JSON.stringify 丢弃 undefined：成功回执的 metadata 不含
	// blockedReason 键。
	if _, hasBlocked := metadata["blockedReason"]; hasBlocked {
		t.Fatalf("queued receipt metadata must omit blockedReason: %#v", metadata)
	}
}

func TestCleanupValidationBodies(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		// 非 CJK 的 zod 文案（Required/invalid_type/unrecognized_keys）被
		// Node 全局本地化中间件改写为 400 默认文案；kernel.writeJSON 同语义。
		{"missing cutoffAt", map[string]any{}, "请求参数无效"},
		{"empty cutoffAt", map[string]any{"cutoffAt": "  "}, zodTimeEmpty},
		{"bad format", map[string]any{"cutoffAt": "not-a-time"}, zodTimeInvalid},
		{"non string", map[string]any{"cutoffAt": 123}, "请求参数无效"},
		{"null cutoffAt", map[string]any{"cutoffAt": nil}, "请求参数无效"},
		{"unknown key", map[string]any{"cutoffAt": "2026-09-01T00:00:00.000Z", "extra": 1}, "请求参数无效"},
		{"future cutoff", map[string]any{"cutoffAt": "2099-01-01T00:00:00.000Z"}, "清理截止时间不能晚于当前时间"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			deps := cleanupDeps(&fakeDispatch{}, &fakeOperationSink{})
			// actor 是去重 key 的组成部分：每个用例独立 actor 隔离全局
			// DefaultDeduplicationStore 中的残留条目。
			recorder := postCleanup(t, deps, testCase.body, "sys-admin-validation-"+testCase.name)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("not 400: %d %s", recorder.Code, recorder.Body.String())
			}
			var payload struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if payload.Message != testCase.want {
				t.Fatalf("message wrong: got %q want %q", payload.Message, testCase.want)
			}
		})
	}
}

func TestCleanupDispatchFailureKeepsReceiptContract(t *testing.T) {
	deps := cleanupDeps(&fakeDispatch{fail: true}, &fakeOperationSink{})
	recorder := postCleanup(t, deps, map[string]any{"cutoffAt": "2026-09-01T00:00:00.000Z"}, "sys-admin-dispatch-failure")
	if recorder.Code != http.StatusOK {
		t.Fatalf("dispatch failure must keep the 200 receipt: %d %s", recorder.Code, recorder.Body.String())
	}
	receipt := decodeCleanupReceipt(t, recorder)
	if receipt.Queued {
		t.Fatalf("queued should be false: %#v", receipt)
	}
	if receipt.BlockedReason == nil || !strings.Contains(*receipt.BlockedReason, "后台 worker 投递失败") {
		t.Fatalf("blockedReason wrong: %#v", receipt)
	}
	if len(deps.Sink.(*fakeOperationSink).entries) != 1 {
		t.Fatalf("failed dispatch should still log: %d", len(deps.Sink.(*fakeOperationSink).entries))
	}
}

func TestCleanupDuplicateSubmissionConflicts(t *testing.T) {
	deps := cleanupDeps(&fakeDispatch{}, &fakeOperationSink{})
	first := postCleanup(t, deps, map[string]any{"cutoffAt": "2026-09-01T00:00:00.000Z"}, "sys-admin-dup")
	if first.Code != http.StatusOK {
		t.Fatalf("first post failed: %d %s", first.Code, first.Body.String())
	}
	second := postCleanup(t, deps, map[string]any{"cutoffAt": "2026-09-01T00:00:00.000Z"}, "sys-admin-dup")
	if second.Code != http.StatusConflict {
		t.Fatalf("duplicate not 409: %d %s", second.Code, second.Body.String())
	}
}

// DurableDispatch：SQLite 落行；通道失败回 worker_dispatch_failed。
func TestDurableDispatchPersistsRow(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	dispatch, err := NewDurableRecordMaintenanceDispatch(db, false, nil)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	job := RecordMaintenanceJob{
		ID:         "recmaint_1_abcdef01",
		CutoffAt:   "2026-09-01T00:00:00.000Z",
		BatchSize:  defaultCleanupBatchSize,
		MaxBatches: defaultCleanupMaxBatches,
		CreatedAt:  "2026-09-04T00:00:00.000Z",
	}
	result := dispatch.EnqueueNonBusinessDataCleanup(context.Background(), job)
	if !result.Queued {
		t.Fatalf("enqueue failed: %#v", result)
	}
	var cutoffAt, jobType string
	var batchSize, maxBatches int
	if err := db.QueryRow(`SELECT type, cutoff_at, batch_size, max_batches FROM record_maintenance_jobs WHERE id = ?`,
		job.ID).Scan(&jobType, &cutoffAt, &batchSize, &maxBatches); err != nil {
		t.Fatalf("row missing: %v", err)
	}
	if jobType != RecordMaintenanceJobTypeNonBusinessDataCleanup || cutoffAt != job.CutoffAt ||
		batchSize != defaultCleanupBatchSize || maxBatches != defaultCleanupMaxBatches {
		t.Fatalf("row wrong: %s %s %d %d", jobType, cutoffAt, batchSize, maxBatches)
	}

	closed, _ := sql.Open("sqlite", ":memory:")
	_ = closed.Close()
	broken, err := NewDurableRecordMaintenanceDispatch(closed, false, nil)
	if err != nil {
		t.Fatalf("broken dispatch: %v", err)
	}
	failed := broken.EnqueueNonBusinessDataCleanup(context.Background(), job)
	if failed.Queued || failed.DroppedReason != "worker_dispatch_failed" {
		t.Fatalf("failure receipt wrong: %#v", failed)
	}
}

// ---------------------------------------------------------------------------
// overview / history 校验（zod coerce 对齐）
// ---------------------------------------------------------------------------

func TestOverviewValidationMessages(t *testing.T) {
	deps, _ := newMonitorFixture(t)
	// 全部非 CJK zod 文案最终渲染为 400 默认文案（本地化语义同 Node）。
	const badRequest = "请求参数无效"
	cases := []struct {
		query string
		want  string
	}{
		{"?keyword=" + strings.Repeat("长", 201), badRequest},
		{"?refresh=banana", badRequest},
		{"?refresh=", badRequest},
		{"?page=", badRequest},
		{"?page=abc", badRequest},
		{"?page=1.5", badRequest},
		{"?page=0", badRequest},
		{"?pageSize=0", badRequest},
		{"?pageSize=101", badRequest},
	}
	for _, testCase := range cases {
		t.Run(testCase.query, func(t *testing.T) {
			recorder := adminHandler(deps, "/__aisys__/api/table-monitor/overview"+testCase.query)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("not 400: %d %s", recorder.Code, recorder.Body.String())
			}
			var payload struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if payload.Message != testCase.want {
				t.Fatalf("message wrong: got %q want %q", payload.Message, testCase.want)
			}
		})
	}
	// 合法布尔形态：TRUE/Yes/0 走 true/true/false。
	for query, header := range map[string]string{
		"?refresh=true": "bypass",
		"?refresh=Yes":  "bypass",
		"?refresh=0":    "bounded-swr",
	} {
		recorder := adminHandler(deps, "/__aisys__/api/table-monitor/overview"+query)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s not 200: %d %s", query, recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get("X-Table-Monitor-Cache"); got != header {
			t.Fatalf("%s cache header wrong: %q want %q", query, got, header)
		}
	}
}

// historyHandler 驱动器（adminHandler 固定走 overview 面）。
func historyGet(deps *Deps, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	auth := &authsys.AuthContext{SystemAccountID: "sys-admin-1", Username: "admin", Role: "admin"}
	request = request.WithContext(authsys.WithAuthContext(request.Context(), auth))
	deps.historyHandler(recorder, request)
	return recorder
}

func TestHistoryCoerceValidation(t *testing.T) {
	deps, _ := newMonitorFixture(t)
	base := "/__aisys__/api/table-monitor/history?databaseRole=business&tableName=accounts"
	cases := []struct {
		query string
		want  string
	}{
		{base + "&limit=", "请求参数无效"},
		{base + "&limit=0", "请求参数无效"},
		{base + "&limit=abc", "请求参数无效"},
		{base + "&limit=2001", "请求参数无效"},
		{base + "&startAt=", zodTimeEmpty},
		{base + "&endAt=%20", zodTimeEmpty},
		{base + "&startAt=not-a-time", zodTimeInvalid},
	}
	for _, testCase := range cases {
		t.Run(testCase.want, func(t *testing.T) {
			recorder := historyGet(deps, testCase.query)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("not 400: %d %s", recorder.Code, recorder.Body.String())
			}
			var payload struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if payload.Message != testCase.want {
				t.Fatalf("message wrong: got %q want %q", payload.Message, testCase.want)
			}
		})
	}
	// database-history 同一 limit/时间链。
	blankLimit := historyGet(deps, "/__aisys__/api/table-monitor/database-history?limit=")
	if blankLimit.Code != http.StatusBadRequest {
		t.Fatalf("database-history blank limit not 400: %d", blankLimit.Code)
	}
	badTime := historyGet(deps, "/__aisys__/api/table-monitor/database-history?startAt=not-a-time")
	if badTime.Code != http.StatusBadRequest {
		t.Fatalf("database-history bad startAt not 400: %d", badTime.Code)
	}
}

// 时间参数规范化进查询窗口（schema transform 语义）。
func TestParseHistoryWindowCanonicalizes(t *testing.T) {
	deps := &Deps{}
	startAt, endAt, badRequest := deps.parseHistoryWindow(func() url.Values {
		values := url.Values{}
		values.Set("startAt", "2026-09-01T10:00:00+08:00")
		values.Set("endAt", "2026-09-04T12:00:00.250Z")
		return values
	}())
	if badRequest != "" || startAt != "2026-09-01T02:00:00.000Z" || endAt != "2026-09-04T12:00:00.250Z" {
		t.Fatalf("canonicalization wrong: %q %q %q", startAt, endAt, badRequest)
	}
	if _, ok := canonicalInstant("2026-09-04T12:00:00.250Z"); !ok {
		t.Fatalf("canonicalInstant rejected valid input")
	}
	if !validInstant("2026-09-04T12:00:00.250Z") {
		t.Fatalf("validInstant wrong")
	}
}

// ---------------------------------------------------------------------------
// SWR 后台刷新存活于请求取消之后（routes.go: WithoutCancel 回归）
// ---------------------------------------------------------------------------

func waitForCondition(t *testing.T, timeout time.Duration, probe func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if probe() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(message)
}

func TestSWRBackgroundRefreshSurvivesRequestCancellation(t *testing.T) {
	store := &fakeOverviewStore{}
	deps := &Deps{Store: store, Cache: NewOverviewCache()}

	// 第一轮：填充缓存（fresh）。
	firstGate := make(chan struct{})
	store.mu.Lock()
	store.gate = firstGate
	store.response = Overview{Page: 1, PageSize: 10, Total: 1, Tables: []TableSnapshot{{TableName: "v1"}}}
	store.mu.Unlock()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- adminHandlerAsync(deps, "/__aisys__/api/table-monitor/overview")
	}()
	close(firstGate)
	firstRecorder := <-done
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first overview failed: %d", firstRecorder.Code)
	}

	// 缓存老化进 stale 窗口（fresh 10min < 30min <= stale 1h）。
	deps.Cache.mu.Lock()
	deps.Cache.storedAt = time.Now().Add(-30 * time.Minute)
	deps.Cache.mu.Unlock()

	// 第二轮：stale 命中立即返回 V1，后台刷新（第二次 LoadOverview）被闸门
	// 挡住；随后取消请求上下文（等价 handler 返回后客户端断开）。
	secondGate := make(chan struct{})
	store.mu.Lock()
	store.gate = secondGate
	store.response = Overview{Page: 1, PageSize: 10, Total: 1, Tables: []TableSnapshot{{TableName: "v2"}}}
	store.mu.Unlock()

	requestCtx, cancel := context.WithCancel(context.Background())
	secondRecorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/api/table-monitor/overview", nil).WithContext(requestCtx)
	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		deps.overviewHandler(secondRecorder, request)
	}()
	waitForCondition(t, 2*time.Second, func() bool { return store.callCount() >= 2 }, "background refresh never started")
	secondDone := make(chan struct{})
	go func() {
		<-handlerDone
		close(secondDone)
	}()
	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stale hit did not return promptly")
	}
	if got := secondRecorder.Header().Get("X-Table-Monitor-Cache"); got != "bounded-swr" {
		t.Fatalf("stale hit cache header wrong: %q", got)
	}
	// handler 已返回：取消请求上下文——旧实现把 r.Context() 传进后台刷新，
	// 刷新必败并进入 30s backoff 死循环。
	cancel()
	close(secondGate)

	waitForCondition(t, 2*time.Second, func() bool {
		deps.Cache.mu.Lock()
		defer deps.Cache.mu.Unlock()
		return deps.Cache.value != nil && len(deps.Cache.value.Tables) == 1 && deps.Cache.value.Tables[0].TableName == "v2"
	}, "background refresh result not stored after request cancellation")
	deps.Cache.mu.Lock()
	refreshing := deps.Cache.refreshing
	refreshFailed := deps.Cache.refreshFailed
	deps.Cache.mu.Unlock()
	if refreshing {
		t.Fatal("refreshing flag stuck")
	}
	if !refreshFailed.IsZero() {
		t.Fatal("refresh should have succeeded, not recorded a failure backoff")
	}
	// 第三轮：fresh V2 直接命中，不再触发刷新。
	store.mu.Lock()
	store.gate = nil
	store.mu.Unlock()
	third := adminHandlerAsync(deps, "/__aisys__/api/table-monitor/overview")
	if third.Code != http.StatusOK {
		t.Fatalf("third overview failed: %d", third.Code)
	}
	if store.callCount() != 2 {
		t.Fatalf("fresh hit should not refresh: calls=%d", store.callCount())
	}
}

// adminHandlerAsync 在 goroutine 里驱动 overview handler（与 adminHandler
// 相同的直接驱动，但便于配合闸门型 fake store 使用）。
func adminHandlerAsync(deps *Deps, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	auth := &authsys.AuthContext{SystemAccountID: "sys-admin-1", Username: "admin", Role: "admin"}
	request = request.WithContext(authsys.WithAuthContext(request.Context(), auth))
	deps.overviewHandler(recorder, request)
	return recorder
}
