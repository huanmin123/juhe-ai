package studiosettle

// studiosettle 语义测试（媒体工作室设计 §5/§10 A5/A7 的 worker 侧单元）：
// 领取谓词、租约抢占、attempt 上限收口、幂等三道闸、崩溃恢复、取消迟到成功、
// mock 上游轮询终态推进。store 语义用内存 SQLite 实库验证（双方言 SQL 的
// SQLite 臂真执行；PG 臂与 SQLite 臂同串同参序列，方言差异仅在 schema 限定
// 与占位符改写，由 pg_error_stages 类集成面覆盖）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newSettleTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:studiosettle-"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	mustExecStudio(t, db,
		`CREATE TABLE studio_tasks (
			id TEXT PRIMARY KEY,
			system_account_id TEXT NOT NULL,
			client_operation_id TEXT NOT NULL,
			kind TEXT NOT NULL CHECK (kind IN ('image', 'video')),
			status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN
				('queued', 'in_progress', 'completed', 'failed', 'cancelled', 'expired')),
			settlement TEXT NOT NULL DEFAULT 'pending_settled' CHECK (settlement IN
				('pending_settled', 'settled', 'failed')),
			account_id TEXT,
			provider_code TEXT,
			upstream_job_id TEXT,
			request_snapshot_json TEXT NOT NULL DEFAULT '{}',
			error_json TEXT NOT NULL DEFAULT '{}',
			asset_id TEXT,
			poll_bookmark_json TEXT NOT NULL DEFAULT '{}',
			lease_owner TEXT,
			lease_expires_at TEXT,
			lease_attempt_count INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE (system_account_id, client_operation_id))`,
		`CREATE TABLE studio_assets (
			id TEXT PRIMARY KEY,
			system_account_id TEXT NOT NULL,
			kind TEXT NOT NULL CHECK (kind IN ('image', 'audio', 'video')),
			mime TEXT NOT NULL,
			storage_key TEXT NOT NULL UNIQUE,
			bytes INTEGER NOT NULL,
			width INTEGER,
			height INTEGER,
			duration_ms INTEGER,
			sha256 TEXT NOT NULL,
			source_task_id TEXT,
			params_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL)`,
		`CREATE UNIQUE INDEX idx_studio_assets_owner_source_task
			ON studio_assets(system_account_id, source_task_id) WHERE source_task_id IS NOT NULL`,
		`CREATE TABLE studio_user_asset_usage (
			system_account_id TEXT PRIMARY KEY,
			asset_bytes INTEGER NOT NULL DEFAULT 0,
			asset_count INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL)`,
		`CREATE TABLE media_jobs (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			api_key_id TEXT NOT NULL,
			account_id TEXT NOT NULL,
			provider_code TEXT NOT NULL,
			upstream_job_id TEXT NOT NULL,
			status TEXT NOT NULL,
			request_snapshot_json TEXT NOT NULL DEFAULT '{}',
			artifact_json TEXT NOT NULL DEFAULT '{}',
			error_json TEXT NOT NULL DEFAULT '{}',
			usage_json TEXT NOT NULL DEFAULT '{}',
			cost_usd REAL NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL)`,
		`CREATE TABLE api_keys (
			id TEXT PRIMARY KEY,
			system_account_id TEXT NOT NULL,
			purpose TEXT NOT NULL DEFAULT 'general')`)
	return db
}

func mustExecStudio(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("exec %q: %v", statement, err)
		}
	}
}

// seedStudioTask 插入一行构造态任务（time.Now 基准 RFC3339 毫秒，与生产
// layout 一致）。
func seedStudioTask(t *testing.T, db *sql.DB, id, kind, status, settlement string, mutate func(column string) any) {
	t.Helper()
	columns := map[string]any{
		"id":                    id,
		"system_account_id":     "user-a",
		"client_operation_id":   "op-" + id,
		"kind":                  kind,
		"status":                status,
		"settlement":            settlement,
		"request_snapshot_json": "{}",
		"created_at":            formatNow(time.Now().UTC().Add(-time.Hour)),
		"updated_at":            formatNow(time.Now().UTC()),
	}
	if mutate != nil {
		for _, column := range []string{
			"account_id", "provider_code", "upstream_job_id",
			"request_snapshot_json", "lease_owner", "lease_expires_at",
			"lease_attempt_count",
		} {
			if override := mutate(column); override != nil {
				columns[column] = override
			}
		}
	}
	names := make([]string, 0, len(columns))
	placeholders := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	for column, value := range columns {
		names = append(names, column)
		placeholders = append(placeholders, "?")
		args = append(args, value)
	}
	query := `INSERT INTO studio_tasks (` + strings.Join(names, ",") + `) VALUES (` + strings.Join(placeholders, ",") + `)`
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("seed task %s: %v", id, err)
	}
}

func seedMediaJob(t *testing.T, db *sql.DB, id, kind, accountID, rawUpstreamJobID, status string, createdAge time.Duration) {
	t.Helper()
	at := formatNow(time.Now().UTC().Add(-createdAge))
	_, err := db.Exec(`INSERT INTO media_jobs (id, kind, api_key_id, account_id, provider_code,
		upstream_job_id, status, created_at, updated_at)
		VALUES (?, ?, 'studiokey', ?, 'openai', ?, ?, ?, ?)`,
		id, kind, accountID, rawUpstreamJobID, status, at, at)
	if err != nil {
		t.Fatalf("seed media_jobs %s: %v", id, err)
	}
}

func seedStudioAPIKey(t *testing.T, db *sql.DB, ownerID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO api_keys (id, system_account_id, purpose) VALUES ('studiokey', ?, 'studio')`, ownerID); err != nil {
		t.Fatalf("seed api_keys: %v", err)
	}
}

func taskRow(t *testing.T, db *sql.DB, id string) map[string]any {
	t.Helper()
	row := db.QueryRow(`SELECT status, settlement, asset_id, error_json, lease_owner,
		lease_attempt_count, upstream_job_id, poll_bookmark_json FROM studio_tasks WHERE id = ?`, id)
	var status, settlement, errorJSON, bookmarkJSON string
	var assetID, leaseOwner, upstreamJobID sql.NullString
	var attempts int
	if err := row.Scan(&status, &settlement, &assetID, &errorJSON, &leaseOwner, &attempts, &upstreamJobID, &bookmarkJSON); err != nil {
		t.Fatalf("read task %s: %v", id, err)
	}
	return map[string]any{
		"status": status, "settlement": settlement, "asset_id": assetID.String,
		"error_json": errorJSON, "lease_owner": leaseOwner.String,
		"lease_attempt_count": attempts, "upstream_job_id": upstreamJobID.String,
		"poll_bookmark_json": bookmarkJSON,
	}
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", query, err)
	}
	return count
}

// ---- mock 端口 ----

// fakeAccounts 是 AccountResolver 的测试实现。
type fakeAccounts struct {
	runtime map[string]AccountRuntime
	err     map[string]error
}

func (f *fakeAccounts) Resolve(_ context.Context, accountID string) (AccountRuntime, error) {
	if err := f.err[accountID]; err != nil {
		return AccountRuntime{}, err
	}
	runtime := f.runtime[accountID]
	return runtime, nil
}

// scriptedResponse 是一次出站响应的脚本项。
type scriptedResponse struct {
	status      int
	body        string
	contentType string
}

// scriptedDoer 按前缀路由（/content 结尾 → 下载脚本；否则 → 轮询脚本），
// 记录全部请求供断言（URL / Bearer / 次数）。
type scriptedDoer struct {
	mu          sync.Mutex
	poll        []scriptedResponse
	content     []scriptedResponse
	urlRequests []scriptedResponse
	requests    []string
	auths       []string
}

func (d *scriptedDoer) next(list []scriptedResponse, fallback scriptedResponse) scriptedResponse {
	if len(list) == 0 {
		return fallback
	}
	return list[0]
}

func (d *scriptedDoer) shift(list []scriptedResponse) []scriptedResponse {
	if len(list) > 0 {
		return list[1:]
	}
	return list
}

func (d *scriptedDoer) Do(_ *http.Request) (*http.Response, error) {
	return nil, errors.New("scriptedDoer 应经 doerFunc 包装携带请求")
}

func (d *scriptedDoer) serve(request *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests = append(d.requests, request.URL.Path)
	d.auths = append(d.auths, request.Header.Get("Authorization"))
	response := scriptedResponse{status: 404, body: `{"error":{"message":"no script"}}`}
	switch {
	case strings.HasPrefix(request.URL.Host, "residue."):
		// 图像残留重下载（绝对 URL，gateway studioDownloadBounded 形态）。
		response = d.next(d.urlRequests, response)
		d.urlRequests = d.shift(d.urlRequests)
	case strings.HasSuffix(request.URL.Path, "/content"):
		response = d.next(d.content, response)
		d.content = d.shift(d.content)
	default:
		response = d.next(d.poll, response)
		d.poll = d.shift(d.poll)
	}
	header := http.Header{}
	if response.contentType != "" {
		header.Set("Content-Type", response.contentType)
	}
	return &http.Response{
		StatusCode: response.status,
		Body:       io.NopCloser(strings.NewReader(response.body)),
		Header:     header,
	}, nil
}

func (d *scriptedDoer) pollCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	count := 0
	for _, path := range d.requests {
		if !strings.HasSuffix(path, "/content") {
			count++
		}
	}
	return count
}

func (d *scriptedDoer) lastAuth() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.auths) == 0 {
		return ""
	}
	return d.auths[len(d.auths)-1]
}

type doerFunc func(request *http.Request) (*http.Response, error)

func (f doerFunc) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}

// newTestJob 构造接好 mock 端口的 SettleJob（5ms 轮询节拍，缩短单领取窗口）。
func newTestJob(t *testing.T, db *sql.DB, doer *scriptedDoer, accounts *fakeAccounts, objects ObjectWriter) *SettleJob {
	t.Helper()
	return &SettleJob{
		Store:      &TaskStore{DB: db, Postgres: false},
		Accounts:   accounts,
		Upstream:   &UpstreamClient{Doer: doerFunc(func(request *http.Request) (*http.Response, error) { return doer.serve(request) })},
		Objects:    objects,
		Clock:      func() time.Time { return time.Now() },
		LeaseOwner: "worker-test",
		PollEvery:  time.Millisecond,
	}
}

func videoPollBody(status string) string {
	return fmt.Sprintf(`{"id":"up-1","object":"video","status":%q,"progress":50,"model":"sora-2","seconds_length":8}`, status)
}

func videoCompletedBody() string {
	return `{"id":"up-1","object":"video","status":"completed","seconds_length":8,"content":[{"status":"completed","type":"video","url":"https://up.example/raw.mp4"}]}`
}

// memoryObjects 是 ObjectWriter 的内存实现（记录写删供断言）。
type memoryObjects struct {
	mu      sync.Mutex
	written map[string][]byte
	deleted []string
}

func newMemoryObjects() *memoryObjects {
	return &memoryObjects{written: map[string][]byte{}}
}

func (m *memoryObjects) Write(storageKey string, data []byte, _ int64, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.written[storageKey] = data
	return nil
}

func (m *memoryObjects) Delete(storageKeys []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = append(m.deleted, storageKeys...)
	for _, key := range storageKeys {
		delete(m.written, key)
	}
	return nil
}

func (m *memoryObjects) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.written)
}

// ---- 用例 ----

func TestClaimPredicateCoversUnsettledAndNonTerminal(t *testing.T) {
	db := newSettleTestDB(t)
	now := formatNow(time.Now().UTC())
	past := formatNow(time.Now().UTC().Add(-time.Minute))
	future := formatNow(time.Now().UTC().Add(time.Minute))
	// 可领取集：未终态、completed 未结算、failed/expired 未结算、取消未结算、
	// failed 重试臂、fresh lease 过期的 in_progress。
	seedStudioTask(t, db, "t-queued-pending", "video", "queued", "pending_settled", nil)
	seedStudioTask(t, db, "t-progress-pending", "video", "in_progress", "pending_settled", nil)
	seedStudioTask(t, db, "t-completed-pending", "video", "completed", "pending_settled", nil)
	seedStudioTask(t, db, "t-cancelled-pending", "video", "cancelled", "pending_settled", nil)
	seedStudioTask(t, db, "t-failed-retry", "image", "completed", "failed", nil)
	seedStudioTask(t, db, "t-progress-expired-lease", "video", "in_progress", "pending_settled", func(column string) any {
		if column == "lease_owner" {
			return "worker-old"
		}
		if column == "lease_expires_at" {
			return past
		}
		return nil
	})
	// 不可领取集：settled 全家、fresh lease 未过期、attempt 上限。
	seedStudioTask(t, db, "x-completed-settled", "video", "completed", "settled", nil)
	seedStudioTask(t, db, "x-cancelled-settled", "video", "cancelled", "settled", nil)
	seedStudioTask(t, db, "x-fresh-lease", "video", "in_progress", "pending_settled", func(column string) any {
		if column == "lease_owner" {
			return "worker-live"
		}
		if column == "lease_expires_at" {
			return future
		}
		return nil
	})
	seedStudioTask(t, db, "x-attempts-exhausted", "video", "completed", "pending_settled", func(column string) any {
		if column == "lease_attempt_count" {
			return MaxLeaseAttempts
		}
		return nil
	})

	store := &TaskStore{DB: db, Postgres: false}
	claimed, err := store.ClaimBatch(context.Background(), now, future, "worker-test", DefaultBatchSize, MaxLeaseAttempts)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	got := map[string]bool{}
	for _, task := range claimed {
		got[task.ID] = true
		row := taskRow(t, db, task.ID)
		if row["lease_owner"] != "worker-test" {
			t.Fatalf("task %s lease_owner = %v, want worker-test", task.ID, row["lease_owner"])
		}
	}
	for _, id := range []string{
		"t-queued-pending", "t-progress-pending", "t-completed-pending",
		"t-cancelled-pending", "t-failed-retry", "t-progress-expired-lease",
	} {
		if !got[id] {
			t.Fatalf("可领取任务 %s 未被领取（谓词覆盖缺失）", id)
		}
	}
	for _, id := range []string{"x-completed-settled", "x-cancelled-settled", "x-fresh-lease", "x-attempts-exhausted"} {
		if got[id] {
			t.Fatalf("不可领取任务 %s 被误领", id)
		}
	}
	// attempt 计数在领取时递增。
	for _, task := range claimed {
		row := taskRow(t, db, task.ID)
		if row["lease_attempt_count"].(int) != 1 {
			t.Fatalf("task %s attempts = %v, want 1", task.ID, row["lease_attempt_count"])
		}
	}
}

func TestLeasePreemptionAndCrashRecovery(t *testing.T) {
	db := newSettleTestDB(t)
	now := time.Now().UTC()
	future := formatNow(now.Add(time.Minute))
	store := &TaskStore{DB: db, Postgres: false}
	seedStudioTask(t, db, "t-live", "video", "completed", "pending_settled", nil)

	// worker-a 领取后持有有效租约：worker-b 同轮不可领取。
	if _, err := store.ClaimBatch(context.Background(), formatNow(now), future, "worker-a", DefaultBatchSize, MaxLeaseAttempts); err != nil {
		t.Fatalf("worker-a claim: %v", err)
	}
	batch, err := store.ClaimBatch(context.Background(), formatNow(now), future, "worker-b", DefaultBatchSize, MaxLeaseAttempts)
	if err != nil {
		t.Fatalf("worker-b claim: %v", err)
	}
	if len(batch) != 0 {
		t.Fatalf("worker-b 抢占了 worker-a 的有效租约: %v", batch)
	}

	// 崩溃恢复：worker-a 的租约过期后 worker-b 可再领取（领取谓词天然覆盖）。
	stale := formatNow(now.Add(-2 * time.Minute))
	if _, err := db.Exec(`UPDATE studio_tasks SET lease_expires_at = ? WHERE id = 't-live'`, stale); err != nil {
		t.Fatal(err)
	}
	batch, err = store.ClaimBatch(context.Background(), formatNow(now), future, "worker-b", DefaultBatchSize, MaxLeaseAttempts)
	if err != nil {
		t.Fatalf("worker-b reclaim: %v", err)
	}
	if len(batch) != 1 || batch[0].ID != "t-live" {
		t.Fatalf("租约过期后 worker-b 未接管: %v", batch)
	}
}

func TestExhaustAttemptsTerminalFail(t *testing.T) {
	db := newSettleTestDB(t)
	seedStudioTask(t, db, "t-exhausted", "video", "completed", "pending_settled", func(column string) any {
		if column == "lease_attempt_count" {
			return MaxLeaseAttempts
		}
		return nil
	})
	doer := &scriptedDoer{}
	job := newTestJob(t, db, doer, &fakeAccounts{}, newMemoryObjects())
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Exhausted != 1 {
		t.Fatalf("exhausted = %d, want 1", result.Exhausted)
	}
	row := taskRow(t, db, "t-exhausted")
	if row["settlement"] != "failed" {
		t.Fatalf("settlement = %v, want failed（终局不再领取）", row["settlement"])
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(row["error_json"].(string)), &payload); err != nil {
		t.Fatalf("error_json: %v", err)
	}
	if payload["code"] != ErrorCodeAttemptsExhaust {
		t.Fatalf("error code = %q, want %q", payload["code"], ErrorCodeAttemptsExhaust)
	}
	// 终局行不再被领取：第二轮零领取、上游零调用。
	second, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	if second.Claimed != 0 || doer.pollCount() != 0 {
		t.Fatalf("终局行仍被处理: claimed=%d polls=%d", second.Claimed, doer.pollCount())
	}
}

func TestVideoCompletedSettlesWithAssetAndIdempotentReplay(t *testing.T) {
	db := newSettleTestDB(t)
	seedStudioAPIKey(t, db, "user-a")
	seedMediaJob(t, db, "mj-1", "video", "acc-1", "up-1", "in_progress", time.Hour)
	seedStudioTask(t, db, "t-video", "video", "completed", "pending_settled", func(column string) any {
		if column == "upstream_job_id" {
			return "mj-1"
		}
		return nil
	})
	doer := &scriptedDoer{content: []scriptedResponse{{status: 200, body: "\x00\x00\x00\x18ftypmp42-fake-video-bytes", contentType: "video/mp4"}}}
	doer.poll = []scriptedResponse{
		{status: 200, body: videoCompletedBody(), contentType: "application/json"},
	}
	objects := newMemoryObjects()
	job := newTestJob(t, db, doer, &fakeAccounts{runtime: map[string]AccountRuntime{
		"acc-1": {Reachable: true, BaseURL: "https://up.test/v1", Bearer: "sk-upstream"},
	}}, objects)
	job.MaxPollsPerClaim = 3

	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.SettledWithAsset != 1 {
		t.Fatalf("settledWithAsset = %d, want 1: %+v", result.SettledWithAsset, result)
	}
	row := taskRow(t, db, "t-video")
	if row["status"] != "completed" || row["settlement"] != "settled" {
		t.Fatalf("task state = %v/%v, want completed/settled", row["status"], row["settlement"])
	}
	if row["asset_id"] == "" {
		t.Fatal("asset_id 未回填")
	}
	if countRows(t, db, `SELECT COUNT(*) FROM studio_assets WHERE source_task_id = 't-video'`) != 1 {
		t.Fatal("产物行未落库")
	}
	var assetKind, assetMime string
	var durationMs sql.NullInt64
	if err := db.QueryRow(`SELECT kind, mime, duration_ms FROM studio_assets WHERE source_task_id = 't-video'`).
		Scan(&assetKind, &assetMime, &durationMs); err != nil {
		t.Fatal(err)
	}
	if assetKind != "video" || assetMime != "video/mp4" {
		t.Fatalf("asset kind/mime = %s/%s, want video/video/mp4", assetKind, assetMime)
	}
	if !durationMs.Valid || durationMs.Int64 != 8000 {
		t.Fatalf("duration_ms = %v, want 8000（seconds_length 换算）", durationMs)
	}
	var usageBytes, usageCount int64
	if err := db.QueryRow(`SELECT asset_bytes, asset_count FROM studio_user_asset_usage WHERE system_account_id = 'user-a'`).Scan(&usageBytes, &usageCount); err != nil {
		t.Fatalf("配额账本未增量: %v", err)
	}
	if usageCount != 1 || usageBytes <= 0 {
		t.Fatalf("配额账本 = %d/%d, want 1 行正字节增量", usageBytes, usageCount)
	}
	// 请求形态：账户亲和三元组直连上游（base_url + /v1/videos/{raw}，账户凭据 Bearer）。
	for index, path := range doer.requests {
		if !strings.HasSuffix(path, "up-1") && !strings.HasSuffix(path, "up-1/content") {
			t.Fatalf("请求 %d 路径不符合任务面 URL 形态: %s", index, path)
		}
	}
	if auth := doer.lastAuth(); auth != "Bearer sk-upstream" {
		t.Fatalf("上游认证 = %q, want 账户凭据 Bearer", auth)
	}

	// 幂等重放：A5 崩溃窗口重演——第二轮不得再插资产行、不得重复入账、
	// 任务已 settled 不再被领取（上游零新增调用）。
	pollsBefore := doer.pollCount()
	second, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	if second.Claimed != 0 {
		t.Fatalf("settled 任务仍被领取: %+v", second)
	}
	if doer.pollCount() != pollsBefore {
		t.Fatalf("settled 任务仍触发上游轮询")
	}
	if countRows(t, db, `SELECT COUNT(*) FROM studio_assets WHERE source_task_id = 't-video'`) != 1 {
		t.Fatal("重放重复插入资产行（闸 3 失效）")
	}
	var replayBytes, replayCount int64
	if err := db.QueryRow(`SELECT asset_bytes, asset_count FROM studio_user_asset_usage WHERE system_account_id = 'user-a'`).Scan(&replayBytes, &replayCount); err != nil {
		t.Fatal(err)
	}
	if replayCount != 1 || replayBytes != usageBytes {
		t.Fatalf("重放后配额 = %d/%d, want %d/1（重放不重复计费）", replayBytes, replayCount, usageBytes)
	}
}

func TestVideoSettlementRetryArmReusesExistingAsset(t *testing.T) {
	db := newSettleTestDB(t)
	seedStudioAPIKey(t, db, "user-a")
	seedMediaJob(t, db, "mj-1", "video", "acc-1", "up-1", "completed", time.Hour)
	seedStudioTask(t, db, "t-retry", "video", "completed", "failed", func(column string) any {
		if column == "upstream_job_id" {
			return "mj-1"
		}
		return nil
	})
	// 崩溃于"已入库未标记 settled"窗口：资产行已存在，任务结算 failed。
	if _, err := db.Exec(`INSERT INTO studio_assets (id, system_account_id, kind, mime, storage_key,
		bytes, sha256, source_task_id, params_json, created_at, updated_at)
		VALUES ('stasset-existing', 'user-a', 'video', 'video/mp4', 'ab/cd/stasset-existing-x.mp4',
		10, 'aa', 't-retry', '{}', ?, ?)`, formatNow(time.Now().UTC()), formatNow(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	doer := &scriptedDoer{poll: []scriptedResponse{{status: 200, body: videoCompletedBody()}},
		content: []scriptedResponse{{status: 200, body: "video-bytes", contentType: "video/mp4"}}}
	objects := newMemoryObjects()
	job := newTestJob(t, db, doer, &fakeAccounts{runtime: map[string]AccountRuntime{
		"acc-1": {Reachable: true, BaseURL: "https://up.test", Bearer: "sk"},
	}}, objects)
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.SettledWithAsset != 1 || result.ReusedAssets != 1 {
		t.Fatalf("重放结算 = %+v, want settled+reused（复用既有资产行）", result)
	}
	row := taskRow(t, db, "t-retry")
	if row["settlement"] != "settled" || row["asset_id"] != "stasset-existing" {
		t.Fatalf("重试臂收口错误: %v", row)
	}
	if countRows(t, db, `SELECT COUNT(*) FROM studio_assets`) != 1 {
		t.Fatal("重放重复插入资产行")
	}
	if countRows(t, db, `SELECT COUNT(*) FROM studio_user_asset_usage`) != 0 {
		t.Fatal("复用既有行不得重复入账")
	}
}

func TestCancelLateSuccessSettlesWithoutPolling(t *testing.T) {
	db := newSettleTestDB(t)
	seedStudioTask(t, db, "t-cancelled", "video", "cancelled", "pending_settled", func(column string) any {
		if column == "upstream_job_id" {
			return "mj-1"
		}
		return nil
	})
	doer := &scriptedDoer{}
	job := newTestJob(t, db, doer, &fakeAccounts{}, newMemoryObjects())
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.SettledNoProduct != 1 {
		t.Fatalf("settledNoProduct = %d, want 1: %+v", result.SettledNoProduct, result)
	}
	row := taskRow(t, db, "t-cancelled")
	if row["status"] != "cancelled" || row["settlement"] != "settled" {
		t.Fatalf("取消迟到成功收口错误: %v", row)
	}
	if row["asset_id"] != "" {
		t.Fatalf("取消迟到成功不应有产物: %v", row["asset_id"])
	}
	if doer.pollCount() != 0 {
		t.Fatal("取消任务不得触发上游轮询")
	}
}

func TestImageResidueRedownloadsFromSnapshotURL(t *testing.T) {
	db := newSettleTestDB(t)
	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("rest-of-image-bytes")...)
	seedStudioTask(t, db, "t-image", "image", "completed", "pending_settled", func(column string) any {
		if column == "request_snapshot_json" {
			return `{"model":"gpt-image-1","prompt":"a cat","url":"https://residue.test/img.png"}`
		}
		return nil
	})
	doer := &scriptedDoer{urlRequests: []scriptedResponse{{status: 200, body: string(png), contentType: "application/json"}}}
	client := &UpstreamClient{Doer: doerFunc(doer.serve)}
	objects := newMemoryObjects()
	job := &SettleJob{
		Store:      &TaskStore{DB: db, Postgres: false},
		Accounts:   &fakeAccounts{},
		Upstream:   client,
		Objects:    objects,
		LeaseOwner: "worker-test",
	}
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.SettledWithAsset != 1 {
		t.Fatalf("settledWithAsset = %d, want 1: %+v", result.SettledWithAsset, result)
	}
	var kind, mime string
	var width, height sql.NullInt64
	if err := db.QueryRow(`SELECT kind, mime, width, height FROM studio_assets WHERE source_task_id = 't-image'`).Scan(&kind, &mime, &width, &height); err != nil {
		t.Fatal(err)
	}
	if kind != "image" || mime != "image/png" {
		t.Fatalf("asset kind/mime = %s/%s, want image/image/png", kind, mime)
	}
	// jobs 恢复路径不解码宽高（来源缺失保持 NULL，契约 §5.5 允许）。
	if width.Valid || height.Valid {
		t.Fatalf("width/height = %v/%v, want NULL", width, height)
	}
	if objects.count() != 1 {
		t.Fatal("对象未写入")
	}

	// 无 url 残留：结算失败收口（studio_acceptance_unknown），不重放生成。
	seedStudioTask(t, db, "t-image-nourl", "image", "completed", "pending_settled", nil)
	if _, err := job.Run(context.Background()); err != nil {
		t.Fatalf("run2: %v", err)
	}
	row := taskRow(t, db, "t-image-nourl")
	if row["settlement"] != "failed" {
		t.Fatalf("无 url 残留 settlement = %v, want failed", row["settlement"])
	}
	if !strings.Contains(row["error_json"].(string), ErrorCodeAcceptanceGone) {
		t.Fatalf("error_json 缺少 %s: %v", ErrorCodeAcceptanceGone, row["error_json"])
	}
}

func TestImageFailedRetryArmKeepsStatusAndSettles(t *testing.T) {
	db := newSettleTestDB(t)
	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("retry-bytes")...)
	// §6 重试臂：gateway 结算终局失败残留（status='failed' +
	// settlement='failed'，快照携带产物 url）重新领取重试——成功后 status
	// 保持 failed（状态机无 failed→completed 边），仅 settlement 收口。
	seedStudioTask(t, db, "t-failed-retry", "image", "failed", "failed", func(column string) any {
		if column == "request_snapshot_json" {
			return `{"model":"gpt-image-1","prompt":"a cat","url":"https://residue.test/retry.png"}`
		}
		return nil
	})
	doer := &scriptedDoer{urlRequests: []scriptedResponse{{status: 200, body: string(png), contentType: "image/png"}}}
	job := &SettleJob{
		Store:      &TaskStore{DB: db, Postgres: false},
		Accounts:   &fakeAccounts{},
		Upstream:   &UpstreamClient{Doer: doerFunc(doer.serve)},
		Objects:    newMemoryObjects(),
		LeaseOwner: "worker-test",
	}
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.SettledWithAsset != 1 {
		t.Fatalf("settledWithAsset = %d, want 1: %+v", result.SettledWithAsset, result)
	}
	row := taskRow(t, db, "t-failed-retry")
	if row["status"] != "failed" || row["settlement"] != "settled" {
		t.Fatalf("重试臂收口 = %v/%v, want failed/settled", row["status"], row["settlement"])
	}
	if countRows(t, db, `SELECT COUNT(*) FROM studio_assets WHERE source_task_id = 't-failed-retry'`) != 1 {
		t.Fatal("重试臂产物未入库")
	}
	var usageCount int64
	if err := db.QueryRow(`SELECT asset_count FROM studio_user_asset_usage WHERE system_account_id = 'user-a'`).Scan(&usageCount); err != nil {
		t.Fatalf("配额账本未增量: %v", err)
	}
}

func TestVideoPollInProgressThenCompleted(t *testing.T) {
	db := newSettleTestDB(t)
	seedStudioAPIKey(t, db, "user-a")
	seedMediaJob(t, db, "mj-1", "video", "acc-1", "up-1", "in_progress", time.Hour)
	seedStudioTask(t, db, "t-progress", "video", "queued", "pending_settled", func(column string) any {
		if column == "upstream_job_id" {
			return "mj-1"
		}
		return nil
	})
	doer := &scriptedDoer{
		poll: []scriptedResponse{
			{status: 200, body: videoPollBody("queued")},
			{status: 200, body: videoPollBody("in_progress")},
			{status: 200, body: videoCompletedBody()},
		},
		content: []scriptedResponse{{status: 200, body: "video-bytes", contentType: "video/mp4"}},
	}
	job := newTestJob(t, db, doer, &fakeAccounts{runtime: map[string]AccountRuntime{
		"acc-1": {Reachable: true, BaseURL: "https://up.test", Bearer: "sk"},
	}}, newMemoryObjects())
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.SettledWithAsset != 1 {
		t.Fatalf("settledWithAsset = %d, want 1: %+v", result.SettledWithAsset, result)
	}
	if doer.pollCount() != 3 {
		t.Fatalf("poll count = %d, want 3（终态推进由 mock 状态序列驱动）", doer.pollCount())
	}
	row := taskRow(t, db, "t-progress")
	if row["status"] != "completed" || row["settlement"] != "settled" {
		t.Fatalf("task state = %v/%v, want completed/settled", row["status"], row["settlement"])
	}
	// 中间态书签已写（§5.3 poll_bookmark_json 续轮询接续点）。
	var bookmark map[string]any
	if err := json.Unmarshal([]byte(row["poll_bookmark_json"].(string)), &bookmark); err != nil {
		t.Fatalf("bookmark: %v", err)
	}
	if bookmark["upstreamStatus"] != "completed" {
		t.Fatalf("bookmark upstreamStatus = %v, want completed", bookmark["upstreamStatus"])
	}
}

func TestVideoUpstreamFailedMirrorsTerminal(t *testing.T) {
	db := newSettleTestDB(t)
	seedStudioAPIKey(t, db, "user-a")
	seedMediaJob(t, db, "mj-1", "video", "acc-1", "up-1", "in_progress", time.Hour)
	seedStudioTask(t, db, "t-upfailed", "video", "in_progress", "pending_settled", func(column string) any {
		if column == "upstream_job_id" {
			return "mj-1"
		}
		return nil
	})
	doer := &scriptedDoer{poll: []scriptedResponse{{status: 200, body: `{"id":"up-1","status":"failed","error":{"code":"generation_failed","message":"upstream boom"}}`}}}
	job := newTestJob(t, db, doer, &fakeAccounts{runtime: map[string]AccountRuntime{
		"acc-1": {Reachable: true, BaseURL: "https://up.test", Bearer: "sk"},
	}}, newMemoryObjects())
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.SettledNoProduct != 1 {
		t.Fatalf("settledNoProduct = %d, want 1: %+v", result.SettledNoProduct, result)
	}
	row := taskRow(t, db, "t-upfailed")
	if row["status"] != "failed" || row["settlement"] != "settled" {
		t.Fatalf("上游失败收口 = %v/%v, want failed/settled（无产物可结算）", row["status"], row["settlement"])
	}
	if countRows(t, db, `SELECT COUNT(*) FROM studio_assets`) != 0 {
		t.Fatal("失败任务不得有产物")
	}
}

func TestVideoMediaJobGoneExpiresTask(t *testing.T) {
	db := newSettleTestDB(t)
	seedStudioTask(t, db, "t-gone", "video", "completed", "pending_settled", func(column string) any {
		if column == "upstream_job_id" {
			return "mj-missing"
		}
		return nil
	})
	job := newTestJob(t, db, &scriptedDoer{}, &fakeAccounts{}, newMemoryObjects())
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Expired != 1 {
		t.Fatalf("expired = %d, want 1: %+v", result.Expired, result)
	}
	row := taskRow(t, db, "t-gone")
	if row["status"] != "expired" || row["settlement"] != "settled" {
		t.Fatalf("任务面不可达收口 = %v/%v, want expired/settled（§5.3）", row["status"], row["settlement"])
	}
}

func TestVideoAccountUnreachableExpiresTask(t *testing.T) {
	db := newSettleTestDB(t)
	seedStudioAPIKey(t, db, "user-a")
	seedMediaJob(t, db, "mj-1", "video", "acc-1", "up-1", "in_progress", time.Hour)
	seedStudioTask(t, db, "t-unreachable", "video", "in_progress", "pending_settled", func(column string) any {
		if column == "upstream_job_id" {
			return "mj-1"
		}
		return nil
	})
	job := newTestJob(t, db, &scriptedDoer{}, &fakeAccounts{}, newMemoryObjects())
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Expired != 1 {
		t.Fatalf("expired = %d, want 1: %+v", result.Expired, result)
	}
	row := taskRow(t, db, "t-unreachable")
	if row["status"] != "expired" || row["settlement"] != "settled" {
		t.Fatalf("账户不可达收口 = %v/%v, want expired/settled（§5.2 不换账户）", row["status"], row["settlement"])
	}
}

func TestVideoAcceptanceRescanAdoptsMediaJob(t *testing.T) {
	db := newSettleTestDB(t)
	seedStudioAPIKey(t, db, "user-a")
	seedMediaJob(t, db, "mj-rescued", "video", "acc-1", "up-1", "completed", 30*time.Minute)
	seedStudioTask(t, db, "t-orphan", "video", "in_progress", "pending_settled", nil)
	doer := &scriptedDoer{poll: []scriptedResponse{{status: 200, body: videoCompletedBody()}},
		content: []scriptedResponse{{status: 200, body: "bytes", contentType: "video/mp4"}}}
	job := newTestJob(t, db, doer, &fakeAccounts{runtime: map[string]AccountRuntime{
		"acc-1": {Reachable: true, BaseURL: "https://up.test", Bearer: "sk"},
	}}, newMemoryObjects())
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.SettledWithAsset != 1 {
		t.Fatalf("settledWithAsset = %d, want 1: %+v", result.SettledWithAsset, result)
	}
	row := taskRow(t, db, "t-orphan")
	if row["upstream_job_id"] != "mj-rescued" {
		t.Fatalf("创建关联重扫未采纳 job id: %v", row["upstream_job_id"])
	}
	if row["settlement"] != "settled" {
		t.Fatalf("采纳后未完成结算: %v", row)
	}
}

func TestVideoAcceptanceUnknownFailsWithoutRecreate(t *testing.T) {
	db := newSettleTestDB(t)
	seedStudioTask(t, db, "t-orphan-nomatch", "video", "in_progress", "pending_settled", nil)
	doer := &scriptedDoer{}
	job := newTestJob(t, db, doer, &fakeAccounts{}, newMemoryObjects())
	if _, err := job.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	row := taskRow(t, db, "t-orphan-nomatch")
	if row["settlement"] != "failed" {
		t.Fatalf("受理未知 settlement = %v, want failed", row["settlement"])
	}
	if !strings.Contains(row["error_json"].(string), ErrorCodeAcceptanceGone) {
		t.Fatalf("error_json 缺少 %s: %v", ErrorCodeAcceptanceGone, row["error_json"])
	}
	if row["upstream_job_id"] != "" {
		t.Fatal("受理未知不得创建上游任务（直连形态不复制派发逻辑）")
	}
	if doer.pollCount() != 0 {
		t.Fatal("受理未知不得触发上游调用")
	}
}

func TestQuotaExceededFailsSettlementAndDropsObject(t *testing.T) {
	db := newSettleTestDB(t)
	seedStudioAPIKey(t, db, "user-a")
	seedMediaJob(t, db, "mj-1", "video", "acc-1", "up-1", "completed", time.Hour)
	seedStudioTask(t, db, "t-quota", "video", "completed", "pending_settled", func(column string) any {
		if column == "upstream_job_id" {
			return "mj-1"
		}
		return nil
	})
	mustExecStudio(t, db, `INSERT INTO studio_user_asset_usage (system_account_id, asset_bytes, asset_count, updated_at)
		VALUES ('user-a', 20*1024*1024*1024, 1999, '2026-01-01T00:00:00.000Z')`)
	doer := &scriptedDoer{poll: []scriptedResponse{{status: 200, body: videoCompletedBody()}},
		content: []scriptedResponse{{status: 200, body: "video-bytes", contentType: "video/mp4"}}}
	objects := newMemoryObjects()
	job := newTestJob(t, db, doer, &fakeAccounts{runtime: map[string]AccountRuntime{
		"acc-1": {Reachable: true, BaseURL: "https://up.test", Bearer: "sk"},
	}}, objects)
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.SettlementFailed != 1 {
		t.Fatalf("settlementFailed = %d, want 1: %+v", result.SettlementFailed, result)
	}
	row := taskRow(t, db, "t-quota")
	if row["settlement"] != "failed" {
		t.Fatalf("配额越限 settlement = %v, want failed（§3.6 不虚计不静默丢弃）", row["settlement"])
	}
	if !strings.Contains(row["error_json"].(string), ErrorCodeSettleQuota) {
		t.Fatalf("error_json 缺少 %s: %v", ErrorCodeSettleQuota, row["error_json"])
	}
	if countRows(t, db, `SELECT COUNT(*) FROM studio_assets`) != 0 {
		t.Fatal("配额越限不得落资产行")
	}
	if objects.count() != 0 {
		t.Fatal("结算失败后已写对象未回收")
	}
}

func TestSettleWithAssetConflictGuardsLateSuccess(t *testing.T) {
	db := newSettleTestDB(t)
	seedStudioTask(t, db, "t-conflict", "image", "cancelled", "pending_settled", nil)
	store := &TaskStore{DB: db, Postgres: false}
	_, _, err := store.SettleWithAsset(context.Background(), SettleAssetInput{
		TaskID: "t-conflict", OwnerID: "user-a",
		Asset: AssetInsert{ID: "stasset-x", Kind: "image", Mime: "image/png", StorageKey: "aa/bb/k.png",
			Bytes: 3, Sha256: "cc", SourceTaskID: "t-conflict"},
		Now: formatNow(time.Now().UTC()),
	})
	if !errors.Is(err, ErrSettleConflict) {
		t.Fatalf("cancelled 竞态收口应返回 ErrSettleConflict, got %v", err)
	}
	if countRows(t, db, `SELECT COUNT(*) FROM studio_assets`) != 0 {
		t.Fatal("冲突结算不得落资产行（迟到成功语义：对象由调用方回收）")
	}
}

func TestRunOnDatabaseWithoutStudioTablesIsZeroWork(t *testing.T) {
	db, err := sql.Open("sqlite", "file:studiosettle-empty-"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	job := &SettleJob{
		Store:      &TaskStore{DB: db, Postgres: false},
		Accounts:   &fakeAccounts{},
		Upstream:   &UpstreamClient{Doer: doerFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("不该出站") })},
		Objects:    newMemoryObjects(),
		LeaseOwner: "worker-test",
	}
	result, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("表缺席应零任务快速返回: %v", err)
	}
	if result.Claimed != 0 || result.Exhausted != 0 {
		t.Fatalf("表缺席轮次应为空: %+v", result)
	}
}

func TestNormalizeAndPathsMirrorTaskFaceShape(t *testing.T) {
	if method, path := VideoPollPath("up-1"); method != http.MethodGet || path != "/v1/videos/up-1" {
		t.Fatalf("poll path = %s %s", method, path)
	}
	if method, path := VideoContentPath("up-1"); method != http.MethodGet || path != "/v1/videos/up-1/content" {
		t.Fatalf("content path = %s %s", method, path)
	}
	if status, known := NormalizeOpenAIStatus("completed"); status != StatusCompleted || !known {
		t.Fatalf("completed 归一错误: %s %v", status, known)
	}
	if status, known := NormalizeOpenAIStatus("something_new"); status != StatusInProgress || known {
		t.Fatalf("未知状态应归 in_progress 且标记未知: %s %v", status, known)
	}
	if _, err := ParseVideoPollResponse(200, []byte(`{"id":"up-1","status":"completed"}`)); err == nil {
		t.Fatal("completed 缺少 content 下载定位应为协议违约错误")
	}
	if _, err := ParseVideoPollResponse(404, []byte("gone")); err == nil {
		t.Fatal("404 应以 UpstreamStatusError 报出")
	}
	if key := StorageKey("stasset_x", strings.Repeat("a", 64), "image/png"); !strings.HasPrefix(key, "aa/aa/stasset_x-aaaaaaaaaaaaaaaa.png") {
		t.Fatalf("storage key 形状错误: %s", key)
	}
}
