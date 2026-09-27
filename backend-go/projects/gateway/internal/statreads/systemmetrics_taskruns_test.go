package statreads

// runtime/jobs 读面契约测试：jobs worker 落库的 background_task_runs 历史
// 经 runtime/jobs 暴露（排序 updated_at DESC, run_id DESC + 既有分页包络），
// 缺表/空表按空 items 200 降级。

import (
	"net/http"
	"testing"

	_ "modernc.org/sqlite"
)

const taskRunsSchema = `
	CREATE TABLE background_task_runs (
		run_id TEXT PRIMARY KEY, job_name TEXT NOT NULL, job_type TEXT NOT NULL,
		worker_role TEXT NOT NULL, status TEXT NOT NULL, lease_key TEXT NOT NULL,
		owner_id TEXT, params_json TEXT NOT NULL DEFAULT '{}', result_json TEXT NOT NULL DEFAULT '{}',
		error_message TEXT, submitted_at TEXT NOT NULL, started_at TEXT, heartbeat_at TEXT,
		finished_at TEXT, duration_ms INTEGER, exit_code INTEGER,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL
	)
`

func newTaskRunsFixture(t *testing.T, withTable bool) *testFixture {
	t.Helper()
	fixture := newFixture(t)
	if withTable {
		if _, err := fixture.db.Exec(taskRunsSchema); err != nil {
			t.Fatalf("apply task runs schema: %v", err)
		}
	}
	return fixture
}

func seedTaskRunRow(t *testing.T, fixture *testFixture, statement string) {
	t.Helper()
	if _, err := fixture.db.Exec(statement); err != nil {
		t.Fatalf("seed task run: %v", err)
	}
}

func TestRuntimeJobsEmptyTableAnswersEmptyItems(t *testing.T) {
	fixture := newTaskRunsFixture(t, true)
	recorder := invoke(t, fixture.deps.runtimeJobsHandler, http.MethodGet, "/?page=1&pageSize=10", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("empty table must answer 200: %d %s", recorder.Code, recorder.Body.String())
	}
	payload := dataMap(t, decodeBody(t, recorder))
	if payload["total"] != float64(0) || payload["page"] != float64(1) || payload["pageSize"] != float64(10) || payload["hasMore"] != false {
		t.Fatalf("empty table paging envelope wrong: %#v", payload)
	}
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 0 {
		t.Fatalf("empty table must answer empty items: %#v", payload["items"])
	}
}

func TestRuntimeJobsMissingTableDegradesToEmptyItems(t *testing.T) {
	fixture := newTaskRunsFixture(t, false)
	recorder := invoke(t, fixture.deps.runtimeJobsHandler, http.MethodGet, "/?page=1&pageSize=10", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("missing table must degrade to 200: %d %s", recorder.Code, recorder.Body.String())
	}
	items, ok := dataMap(t, decodeBody(t, recorder))["items"].([]any)
	if !ok || len(items) != 0 {
		t.Fatalf("missing table must answer empty items: %#v", items)
	}
}

func TestRuntimeJobsMapsRowsAndPaginates(t *testing.T) {
	fixture := newTaskRunsFixture(t, true)
	seedTaskRunRow(t, fixture, `INSERT INTO background_task_runs
		(run_id, job_name, job_type, worker_role, status, lease_key, submitted_at, started_at, finished_at, duration_ms, created_at, updated_at)
		VALUES ('run-old', 'usage-stats-aggregation', 'usage-stats-aggregation', 'worker', 'completed', 'scheduled:usage-stats-aggregation:',
		'2026-09-04T10:00:00.000Z', '2026-09-04T10:00:00.000Z', '2026-09-04T10:00:02.500Z', 2500,
		'2026-09-04T10:00:00.000Z', '2026-09-04T10:00:02.500Z')`)
	seedTaskRunRow(t, fixture, `INSERT INTO background_task_runs
		(run_id, job_name, job_type, worker_role, status, lease_key, error_message, submitted_at, started_at, finished_at, duration_ms, created_at, updated_at)
		VALUES ('run-new', 'group-account-stats-refresh', 'group-account-stats-refresh', 'worker', 'failed', 'scheduled:group-account-stats-refresh:',
		'聚合批次失败', '2026-09-04T11:00:00.000Z', '2026-09-04T11:00:00.000Z', '2026-09-04T11:00:01.000Z', 1000,
		'2026-09-04T11:00:00.000Z', '2026-09-04T11:00:01.000Z')`)
	seedTaskRunRow(t, fixture, `INSERT INTO background_task_runs
		(run_id, job_name, job_type, worker_role, status, lease_key, submitted_at, created_at, updated_at)
		VALUES ('run-a', 'usage-overview-windows-refresh', 'usage-overview-windows-refresh', 'worker', 'queued', 'scheduled:usage-overview-windows-refresh:',
		'2026-09-04T09:00:00.000Z', '2026-09-04T09:00:00.000Z', '2026-09-04T09:00:00.000Z')`)

	recorder := invoke(t, fixture.deps.runtimeJobsHandler, http.MethodGet, "/?page=1&pageSize=10", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("jobs page 1 not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	payload := dataMap(t, decodeBody(t, recorder))
	if payload["total"] != float64(3) || payload["page"] != float64(1) || payload["pageSize"] != float64(10) || payload["hasMore"] != false {
		t.Fatalf("page 1 envelope wrong: %#v", payload)
	}
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("page 1 items wrong: %#v", payload["items"])
	}
	// updated_at DESC：run-new（11:00）在前，run-old（10:00:02）次之，run-a（09:00）最后。
	first := items[0].(map[string]any)
	if first["runId"] != "run-new" || first["status"] != "failed" || first["errorMessage"] != "聚合批次失败" {
		t.Fatalf("newest failed run mapping wrong: %#v", first)
	}
	if first["jobName"] != "group-account-stats-refresh" || first["jobType"] != "group-account-stats-refresh" || first["workerRole"] != "worker" {
		t.Fatalf("newest run identity wrong: %#v", first)
	}
	if first["startedAt"] != "2026-09-04T11:00:00.000Z" || first["finishedAt"] != "2026-09-04T11:00:01.000Z" || first["durationMs"] != float64(1000) {
		t.Fatalf("newest run timing wrong: %#v", first)
	}
	second := items[1].(map[string]any)
	if second["runId"] != "run-old" || second["status"] != "completed" {
		t.Fatalf("second run wrong: %#v", second)
	}
	if second["errorMessage"] != nil {
		t.Fatalf("completed run must carry null errorMessage: %#v", second)
	}
	// queued 行 started_at/finished_at/duration_ms 保持 null。
	queued := items[2].(map[string]any)
	if queued["runId"] != "run-a" || queued["status"] != "queued" {
		t.Fatalf("queued run wrong: %#v", queued)
	}
	if queued["startedAt"] != nil || queued["finishedAt"] != nil || queued["durationMs"] != nil {
		t.Fatalf("queued run timing must stay null: %#v", queued)
	}
}

func TestRuntimeJobsPageBoundaryFollowsExistingEnvelope(t *testing.T) {
	fixture := newTaskRunsFixture(t, true)
	// 12 行递增 updated_at，pageSize=10 时第一页满页 hasMore=true，第二页余 2 行。
	for index := 1; index <= 12; index++ {
		seedTaskRunRow(t, fixture, `INSERT INTO background_task_runs
			(run_id, job_name, job_type, worker_role, status, lease_key, submitted_at, created_at, updated_at)
			VALUES ('run-`+string(rune('0'+index/10))+string(rune('0'+index%10))+`', 'job', 'job', 'worker', 'completed', 'scheduled:job:',
			'2026-09-04T00:`+padTaskRunMinute(index)+`:00.000Z', '2026-09-04T00:`+padTaskRunMinute(index)+`:00.000Z', '2026-09-04T00:`+padTaskRunMinute(index)+`:00.000Z')`)
	}
	recorder := invoke(t, fixture.deps.runtimeJobsHandler, http.MethodGet, "/?page=1&pageSize=10", adminAuth(""))
	payload := dataMap(t, decodeBody(t, recorder))
	if payload["total"] != float64(12) || payload["hasMore"] != true || len(payload["items"].([]any)) != 10 {
		t.Fatalf("page 1 boundary wrong: %#v", payload)
	}
	recorder = invoke(t, fixture.deps.runtimeJobsHandler, http.MethodGet, "/?page=2&pageSize=10", adminAuth(""))
	payload = dataMap(t, decodeBody(t, recorder))
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 2 || payload["hasMore"] != false {
		t.Fatalf("page 2 boundary wrong: %#v", payload)
	}
	if items[0].(map[string]any)["runId"] != "run-02" || items[1].(map[string]any)["runId"] != "run-01" {
		t.Fatalf("page 2 order wrong: %#v", items)
	}
}

func padTaskRunMinute(index int) string {
	return string(rune('0'+index/10)) + string(rune('0'+index%10))
}

// seedRuntimeJobsStatusRows 落入 completed/failed/queued 三种状态各一行
// （updated_at 递增：run-old < run-new < run-a），供 status 筛选 arm 复用。
func seedRuntimeJobsStatusRows(t *testing.T, fixture *testFixture) {
	t.Helper()
	seedTaskRunRow(t, fixture, `INSERT INTO background_task_runs
		(run_id, job_name, job_type, worker_role, status, lease_key, error_message, submitted_at, started_at, finished_at, duration_ms, created_at, updated_at)
		VALUES ('run-old', 'usage-stats-aggregation', 'usage-stats-aggregation', 'worker', 'completed', 'scheduled:usage-stats-aggregation:',
		NULL, '2026-09-04T10:00:00.000Z', '2026-09-04T10:00:00.000Z', '2026-09-04T10:00:02.500Z', 2500,
		'2026-09-04T10:00:00.000Z', '2026-09-04T10:00:02.500Z')`)
	seedTaskRunRow(t, fixture, `INSERT INTO background_task_runs
		(run_id, job_name, job_type, worker_role, status, lease_key, error_message, submitted_at, started_at, finished_at, duration_ms, created_at, updated_at)
		VALUES ('run-new', 'group-account-stats-refresh', 'group-account-stats-refresh', 'worker', 'failed', 'scheduled:group-account-stats-refresh:',
		'聚合批次失败', '2026-09-04T11:00:00.000Z', '2026-09-04T11:00:00.000Z', '2026-09-04T11:00:01.000Z', 1000,
		'2026-09-04T11:00:00.000Z', '2026-09-04T11:00:01.000Z')`)
	seedTaskRunRow(t, fixture, `INSERT INTO background_task_runs
		(run_id, job_name, job_type, worker_role, status, lease_key, submitted_at, created_at, updated_at)
		VALUES ('run-a', 'usage-overview-windows-refresh', 'usage-overview-windows-refresh', 'worker', 'queued', 'scheduled:usage-overview-windows-refresh:',
		'2026-09-04T09:00:00.000Z', '2026-09-04T09:00:00.000Z', '2026-09-04T09:00:00.000Z')`)
}

func TestRuntimeJobsStatusFilterSelectsMatchingRows(t *testing.T) {
	fixture := newTaskRunsFixture(t, true)
	seedRuntimeJobsStatusRows(t, fixture)
	// status=failed 只返回 failed 行，total 反映筛选后集合。
	recorder := invoke(t, fixture.deps.runtimeJobsHandler, http.MethodGet, "/?page=1&pageSize=10&status=failed", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status filter not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	payload := dataMap(t, decodeBody(t, recorder))
	if payload["total"] != float64(1) || payload["hasMore"] != false {
		t.Fatalf("failed filter envelope wrong: %#v", payload)
	}
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("failed filter items wrong: %#v", payload["items"])
	}
	if items[0].(map[string]any)["runId"] != "run-new" {
		t.Fatalf("failed filter must keep run-new: %#v", items)
	}
	// 状态词表覆盖：completed / queued / running / skipped（后两者空集）。
	for _, status := range []string{"completed", "queued"} {
		recorder = invoke(t, fixture.deps.runtimeJobsHandler, http.MethodGet, "/?page=1&pageSize=10&status="+status, adminAuth(""))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s filter not 200: %d %s", status, recorder.Code, recorder.Body.String())
		}
		payload = dataMap(t, decodeBody(t, recorder))
		if payload["total"] != float64(1) {
			t.Fatalf("%s filter total wrong: %#v", status, payload)
		}
	}
	for _, status := range []string{"running", "skipped"} {
		recorder = invoke(t, fixture.deps.runtimeJobsHandler, http.MethodGet, "/?page=1&pageSize=10&status="+status, adminAuth(""))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s filter not 200: %d %s", status, recorder.Code, recorder.Body.String())
		}
		payload = dataMap(t, decodeBody(t, recorder))
		if payload["total"] != float64(0) {
			t.Fatalf("%s filter must answer empty set: %#v", status, payload)
		}
	}
}

func TestRuntimeJobsStatusFilterPaginationReflectsFilteredSet(t *testing.T) {
	fixture := newTaskRunsFixture(t, true)
	// 12 行中 5 行 failed：pageSize=10 下 status=failed 的 total=5、hasMore=false。
	for index := 1; index <= 12; index++ {
		status := "completed"
		if index <= 5 {
			status = "failed"
		}
		seedTaskRunRow(t, fixture, `INSERT INTO background_task_runs
			(run_id, job_name, job_type, worker_role, status, lease_key, submitted_at, created_at, updated_at)
			VALUES ('run-`+string(rune('0'+index/10))+string(rune('0'+index%10))+`', 'job', 'job', 'worker', '`+status+`', 'scheduled:job:',
			'2026-09-04T00:`+padTaskRunMinute(index)+`:00.000Z', '2026-09-04T00:`+padTaskRunMinute(index)+`:00.000Z', '2026-09-04T00:`+padTaskRunMinute(index)+`:00.000Z')`)
	}
	recorder := invoke(t, fixture.deps.runtimeJobsHandler, http.MethodGet, "/?page=1&pageSize=10&status=failed", adminAuth(""))
	payload := dataMap(t, decodeBody(t, recorder))
	if payload["total"] != float64(5) || payload["hasMore"] != false || len(payload["items"].([]any)) != 5 {
		t.Fatalf("filtered set must drive total/hasMore: %#v", payload)
	}
	// 无筛选对照：total=12、hasMore=true。
	recorder = invoke(t, fixture.deps.runtimeJobsHandler, http.MethodGet, "/?page=1&pageSize=10", adminAuth(""))
	payload = dataMap(t, decodeBody(t, recorder))
	if payload["total"] != float64(12) || payload["hasMore"] != true {
		t.Fatalf("unfiltered baseline wrong: %#v", payload)
	}
}

func TestRuntimeJobsStatusFilterInvalidAnswer400(t *testing.T) {
	fixture := newTaskRunsFixture(t, true)
	seedRuntimeJobsStatusRows(t, fixture)
	// 非法值 400；空白值视同缺省（全部）。
	recorder := invoke(t, fixture.deps.runtimeJobsHandler, http.MethodGet, "/?page=1&pageSize=10&status=purged", adminAuth(""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid status must answer 400: %d %s", recorder.Code, recorder.Body.String())
	}
	if got := decodeBody(t, recorder)["message"]; got != "后台任务状态筛选不合法" {
		t.Fatalf("400 message wrong: %#v", got)
	}
	recorder = invoke(t, fixture.deps.runtimeJobsHandler, http.MethodGet, "/?page=1&pageSize=10&status=", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("blank status must degrade to unfiltered 200: %d %s", recorder.Code, recorder.Body.String())
	}
	payload := dataMap(t, decodeBody(t, recorder))
	if payload["total"] != float64(3) {
		t.Fatalf("blank status must keep all rows: %#v", payload)
	}
}
