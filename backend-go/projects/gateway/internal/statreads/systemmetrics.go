package statreads

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"
)

// System-metrics read family (Node system-metrics.repository.ts
// getSystemMetricsTrend + stats.routes.ts runtime routes). The runtime
// summary/queues routes keep the unavailable degradation the Node code
// produces when requestServerSystemMetricsRuntime Snapshot fails
// (runtimeSnapshotAvailable:false, empty rows); the runtime jobs route reads
// the background_task_runs history the Go jobs worker persists per scheduled
// execution.
const (
	processEventLoopPeakWindowMS    = int64(24 * 60 * 60 * 1000)
	processEventLoopLatestFreshness = int64(2 * 60 * 1000)
)

// systemMetricsTrendHandler mirrors GET /system-metrics/trend.
func (d *Deps) systemMetricsTrendHandler(w http.ResponseWriter, r *http.Request) {
	startDate, endDate, badRequest := parseUsageOverviewQuery(r.URL.Query())
	if badRequest != "" {
		kernel.WriteBadRequest(w, "监控日期范围不合法")
		return
	}
	rng, err := d.normalizeSystemMetricsDateRange(r.Context(), startDate, endDate)
	if err != nil {
		d.writeReadError(w, err)
		return
	}
	rows, err := d.queryStats(r, `
		SELECT bucket_key AS stat_hour, sample_count, cpu_percent_sum, memory_used_percent_sum,
			network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_count,
			network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_count
		FROM `+d.statsTable("system_metrics_trend_windows")+`
		WHERE window_key = ? AND start_date = ? AND end_date = ?
		ORDER BY bucket_key ASC
	`, rangeWindowKey(rng), rng.StartDate, rng.EndDate)
	if err != nil {
		d.writeReadError(w, err)
		return
	}
	hourlyTrend := make([]systemMetricsTrendPoint, 0, len(rows))
	for _, row := range rows {
		sampleCount := row.number("sample_count")
		hourlyTrend = append(hourlyTrend, systemMetricsTrendPoint{
			StatHour:                   row.text("stat_hour"),
			CPUPercentAvg:              averageFromSum(row.value("cpu_percent_sum"), sampleCount),
			MemoryUsedPercentAvg:       averageFromSum(row.value("memory_used_percent_sum"), sampleCount),
			NetworkRxBytesPerSecondAvg: averageFromSum(row.value("network_rx_bytes_per_sec_sum"), row.value("network_rx_bytes_per_sec_count")),
			NetworkTxBytesPerSecondAvg: averageFromSum(row.value("network_tx_bytes_per_sec_sum"), row.value("network_tx_bytes_per_sec_count")),
		})
	}
	peakStartedAt := rfc3339Millis(time.UnixMilli(d.Now().UnixMilli() - processEventLoopPeakWindowMS))
	latestStartedAt := rfc3339Millis(time.UnixMilli(d.Now().UnixMilli() - processEventLoopLatestFreshness))
	latestRows, err := d.processEventLoopTrendLatestRows(r, latestStartedAt)
	if err != nil {
		d.writeReadError(w, err)
		return
	}
	peakRows, err := d.processEventLoopTrendPeakRows(r, peakStartedAt)
	if err != nil {
		d.writeReadError(w, err)
		return
	}
	trendRows, err := d.processEventLoopTrendRows(r, rng)
	if err != nil {
		d.writeReadError(w, err)
		return
	}
	kernel.WriteOK(w, systemMetricsTrendOverview{
		HourlyTrend:                  hourlyTrend,
		ProcessEventLoopLatestStatus: d.buildProcessEventLoopTrendLatestStatus(latestRows),
		ProcessEventLoopPeakStatus:   d.buildProcessEventLoopTrendPeakStatus(peakRows),
		ProcessEventLoopTrend:        trendRows,
	}, "")
}

func (d *Deps) processEventLoopTrendLatestRows(r *http.Request, startedAt string) ([]Row, error) {
	if d.PGDialect {
		rows, err := d.queryStats(r, `
			SELECT DISTINCT ON (process_role) process_role, process_pid, sampled_at, event_loop_lag_ms,
				process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes
			FROM `+d.statsTable("process_event_loop_samples")+`
			WHERE sampled_at >= ?
			ORDER BY process_role, sampled_at DESC, id DESC
			LIMIT 256
		`, startedAt)
		return filterValidRoles(rows), err
	}
	rows, err := d.queryStats(r, `
		SELECT process_role, process_pid, sampled_at, event_loop_lag_ms,
			process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes
		FROM (
			SELECT process_role, process_pid, sampled_at, event_loop_lag_ms,
				process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes,
				ROW_NUMBER() OVER (
					PARTITION BY process_role
					ORDER BY sampled_at DESC, id DESC
				) AS role_rank
			FROM process_event_loop_samples
			WHERE sampled_at >= ?
		)
		WHERE role_rank = 1
		LIMIT 256
	`, startedAt)
	return filterValidRoles(rows), err
}

func (d *Deps) processEventLoopTrendPeakRows(r *http.Request, startedAt string) ([]Row, error) {
	if d.PGDialect {
		rows, err := d.queryStats(r, `
			SELECT DISTINCT ON (process_role) process_role, process_pid, sampled_at, event_loop_lag_ms
			FROM `+d.statsTable("process_event_loop_samples")+`
			WHERE sampled_at >= ? AND event_loop_lag_ms IS NOT NULL
			ORDER BY process_role, event_loop_lag_ms DESC, sampled_at DESC, id DESC
			LIMIT 256
		`, startedAt)
		return filterValidRoles(rows), err
	}
	rows, err := d.queryStats(r, `
		SELECT process_role, process_pid, sampled_at, event_loop_lag_ms
		FROM (
			SELECT process_role, process_pid, sampled_at, event_loop_lag_ms,
				ROW_NUMBER() OVER (
					PARTITION BY process_role
					ORDER BY event_loop_lag_ms DESC, sampled_at DESC, id DESC
				) AS role_rank
			FROM process_event_loop_samples
			WHERE sampled_at >= ? AND event_loop_lag_ms IS NOT NULL
		)
		WHERE role_rank = 1
		LIMIT 256
	`, startedAt)
	return filterValidRoles(rows), err
}

func (d *Deps) processEventLoopTrendRows(r *http.Request, rng Range) ([]processEventLoopTrendPoint, error) {
	rows, err := d.queryStats(r, `
		SELECT bucket_key AS stat_hour, process_role, sample_count, event_loop_lag_ms_sum,
			event_loop_lag_ms_count, event_loop_lag_ms_max, process_rss_bytes_sum, process_rss_bytes_max
		FROM `+d.statsTable("process_event_loop_trend_windows")+`
		WHERE window_key = ? AND start_date = ? AND end_date = ?
		ORDER BY bucket_key ASC, process_role ASC
	`, rangeWindowKey(rng), rng.StartDate, rng.EndDate)
	if err != nil {
		return nil, err
	}
	points := []processEventLoopTrendPoint{}
	for _, row := range rows {
		if !isValidProcessRole(row.text("process_role")) {
			continue
		}
		sampleCount := row.number("sample_count")
		lagSampleCount := row.number("event_loop_lag_ms_count")
		if lagSampleCount == 0 {
			lagSampleCount = sampleCount
		}
		points = append(points, processEventLoopTrendPoint{
			StatMinute:         row.text("stat_hour"),
			ProcessRole:        row.text("process_role"),
			EventLoopLagMsAvg:  averageFromSum(row.value("event_loop_lag_ms_sum"), lagSampleCount),
			EventLoopLagMsMax:  row.nullNumber("event_loop_lag_ms_max"),
			ProcessRssBytesAvg: averageFromSum(row.value("process_rss_bytes_sum"), sampleCount),
			ProcessRssBytesMax: row.nullNumber("process_rss_bytes_max"),
		})
	}
	return points, nil
}

func filterValidRoles(rows []Row) []Row {
	valid := make([]Row, 0, len(rows))
	for _, row := range rows {
		if isValidProcessRole(row.text("process_role")) {
			valid = append(valid, row)
		}
	}
	return valid
}

// validProcessRoles mirror the Node process role vocabulary (ProcessEventLoopRole).
var validProcessRoles = map[string]bool{
	"server": true, "ingest-worker": true, "stats-worker": true, "ops-worker": true,
	"db-service": true,
}

// workerReplicaRolePattern mirrors the worker replica family of
// processEventLoopRoleFromUnknown (shared/process-event-loop-monitor.ts):
// <worker-role>:<replica index 1..64>, digits only, no leading zeros.
var workerReplicaRolePattern = regexp.MustCompile(`^(?:ingest-worker|usage-worker|log-worker|stats-worker|ops-worker):(?:[1-9]|[1-5][0-9]|6[0-4])$`)

func isValidProcessRole(value string) bool {
	if validProcessRoles[value] {
		return true
	}
	// gateway:* / control:* / control-replica:* / db-service:* families
	//（D-102：db-service: 前缀族此前缺位，db-service 副本采样被读侧过滤）。
	for _, prefix := range []string{"gateway:", "control:", "control-replica:", "db-service:"} {
		if strings.HasPrefix(value, prefix) && len(value) > len(prefix) {
			return true
		}
	}
	return workerReplicaRolePattern.MatchString(value)
}

// standaloneProcessRoles mirrors PROCESS_EVENT_LOOP_ROLES
// (system-metrics.repository.ts:27-33).
var standaloneProcessRoles = []string{"server", "ingest-worker", "stats-worker", "ops-worker", "db-service"}

// trendStatusRoles mirrors trendStatusRoles
// (system-metrics.repository.ts:1355-1358): standalone mode keeps the fixed
// process family order; performance mode exports the roles present in the
// data rows sorted by role text (compareText, plain string compare).
func (d *Deps) trendStatusRoles(rows []Row) []string {
	if d.runtimeStandalone() {
		return standaloneProcessRoles
	}
	seen := map[string]bool{}
	roles := []string{}
	for _, row := range rows {
		role := row.text("process_role")
		if !isValidProcessRole(role) || seen[role] {
			continue
		}
		seen[role] = true
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}

type trendStatusRow struct {
	ProcessRole      string
	SampleAvailable  bool
	ProcessPid       *int64
	SampledAt        *string
	EventLoopLagMs   *int64
	ProcessRssBytes  *int64
	ProcessHeapUsed  *int64
	ProcessHeapTotal *int64
}

func (d *Deps) buildProcessEventLoopTrendLatestStatus(rows []Row) []processEventLoopStatus {
	mapped := map[string]trendStatusRow{}
	for _, row := range rows {
		role := row.text("process_role")
		if !isValidProcessRole(role) {
			continue
		}
		mapped[role] = trendStatusRow{
			ProcessRole:      role,
			SampleAvailable:  true,
			ProcessPid:       row.nullNumber("process_pid"),
			SampledAt:        row.nullText("sampled_at"),
			EventLoopLagMs:   row.nullNumber("event_loop_lag_ms"),
			ProcessRssBytes:  row.nullNumber("process_rss_bytes"),
			ProcessHeapUsed:  row.nullNumber("process_heap_used_bytes"),
			ProcessHeapTotal: row.nullNumber("process_heap_total_bytes"),
		}
	}
	statuses := make([]processEventLoopStatus, 0, len(standaloneProcessRoles))
	for _, role := range d.trendStatusRoles(rows) {
		if entry, ok := mapped[role]; ok {
			statuses = append(statuses, processEventLoopStatus{
				ProcessRole:      role,
				SampleAvailable:  true,
				ProcessPid:       entry.ProcessPid,
				SampledAt:        entry.SampledAt,
				EventLoopLagMs:   entry.EventLoopLagMs,
				ProcessRssBytes:  entry.ProcessRssBytes,
				ProcessHeapUsed:  entry.ProcessHeapUsed,
				ProcessHeapTotal: entry.ProcessHeapTotal,
			})
			continue
		}
		statuses = append(statuses, processEventLoopStatus{ProcessRole: role})
	}
	return statuses
}

func (d *Deps) buildProcessEventLoopTrendPeakStatus(rows []Row) []processEventLoopPeakStatus {
	mapped := map[string]Row{}
	for _, row := range rows {
		role := row.text("process_role")
		if !isValidProcessRole(role) {
			continue
		}
		mapped[role] = row
	}
	statuses := make([]processEventLoopPeakStatus, 0, len(standaloneProcessRoles))
	for _, role := range d.trendStatusRoles(rows) {
		if row, ok := mapped[role]; ok {
			statuses = append(statuses, processEventLoopPeakStatus{
				ProcessRole:     role,
				SampleAvailable: true,
				ProcessPid:      row.nullNumber("process_pid"),
				SampledAt:       row.nullText("sampled_at"),
				EventLoopLagMs:  row.nullNumber("event_loop_lag_ms"),
			})
			continue
		}
		statuses = append(statuses, processEventLoopPeakStatus{ProcessRole: role})
	}
	return statuses
}

// goRuntimeTrendRoles is the role family the trend exports. Every Go process
// self-samples into the shared store (去跨进程战役第三刀), so the read side
// merges the fixed gateway+jobs role list; each item keeps its own
// service/role fields and the outer envelope carries the aggregate marker.
var goRuntimeTrendRoles = []string{"gateway", "jobs"}

// goRuntimeTrendAggregateRole marks the merged two-role envelope. The
// frontend never consumes the outer role field (it renders items directly),
// so the aggregate keeps the shape honest without a per-role split.
const goRuntimeTrendAggregateRole = "gateway+jobs"

// goRuntimeTrendMaxRange clamps the query window to the store's 90-day
// hourly-trend bound instead of failing the request.
const goRuntimeTrendMaxRange = 90 * 24 * time.Hour

// goRuntimeTrendHandler mirrors GET /system-metrics/go-runtime-trend: an
// in-process Store.QueryTrend over the shared go_runtime_metrics_hourly
// windows (gateway + jobs roles). Store disabled or no data answers 200 with
// empty items; only a real query failure surfaces as a read error.
func (d *Deps) goRuntimeTrendHandler(w http.ResponseWriter, r *http.Request) {
	startDate, endDate, badRequest := parseUsageOverviewQuery(r.URL.Query())
	if badRequest != "" {
		kernel.WriteBadRequest(w, "Go 运行时指标日期范围不合法")
		return
	}
	rng, err := d.normalizeSystemMetricsDateRange(r.Context(), startDate, endDate)
	if err != nil {
		d.writeReadError(w, err)
		return
	}
	location, err := d.timezoneLocation(r.Context())
	if err != nil {
		d.writeReadError(w, err)
		return
	}
	from, fromErr := time.Parse(time.RFC3339, startOfZonedDateKeyIso(rng.StartDate, location))
	to, toErr := time.Parse(time.RFC3339, startOfZonedDateKeyIso(nextCalendarDateKey(rng.EndDate), location))
	if fromErr != nil || toErr != nil || !from.Before(to) {
		kernel.WriteBadRequest(w, "Go 运行时指标日期范围无法解析")
		return
	}
	if to.Sub(from) > goRuntimeTrendMaxRange {
		from = to.Add(-goRuntimeTrendMaxRange)
	}
	items := []gometrics.WindowAggregate{}
	if d.GoRuntimeMetrics != nil {
		for _, role := range goRuntimeTrendRoles {
			rows, queryErr := d.GoRuntimeMetrics.QueryTrend(r.Context(), d.GoRuntimeMetricsService, role, from, to)
			if queryErr != nil {
				d.writeReadError(w, queryErr)
				return
			}
			items = append(items, rows...)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	kernel.WriteOK(w, map[string]any{
		"runtimeKind": "go",
		"service":     d.GoRuntimeMetricsService,
		"role":        goRuntimeTrendAggregateRole,
		"timezone":    location.String(),
		"range":       rng,
		"items":       items,
	}, "")
}

// ---------------------------------------------------------------------------
// Runtime snapshot routes: summary and queues keep the documented unavailable
// degradation (Node's loadSystemMetricsRuntimeSnapshot catch(() => undefined)
// path — the Node db-service IPC peer is gone). The jobs route reads the
// background_task_runs history the Go jobs worker persists per scheduled
// execution.
// ---------------------------------------------------------------------------

func (d *Deps) runtimeSummaryHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	kernel.WriteOK(w, map[string]any{
		"runtimeSnapshotAvailable":      false,
		"runtimeSnapshotStale":          nil,
		"ingestWorkerSnapshotAvailable": false,
		"statsWorkerSnapshotAvailable":  false,
		"opsWorkerSnapshotAvailable":    false,
		"jobsAvailable":                 false,
		"queuesAvailable":               false,
	}, "")
}

// backgroundTaskRunItem is the runtime/jobs row contract: one scheduled job
// execution persisted by the Go jobs worker (RunWithTaskRun) into
// background_task_runs.
type backgroundTaskRunItem struct {
	RunID        string  `json:"runId"`
	JobName      string  `json:"jobName"`
	JobType      string  `json:"jobType"`
	WorkerRole   string  `json:"workerRole"`
	Status       string  `json:"status"`
	StartedAt    *string `json:"startedAt"`
	FinishedAt   *string `json:"finishedAt"`
	DurationMs   *int64  `json:"durationMs"`
	ErrorMessage *string `json:"errorMessage"`
}

// isTaskRunsSchemaMissing maps the driver shapes of a missing
// background_task_runs table/columns onto the empty-items degradation
// (SQLite "no such table", PostgreSQL SQLSTATE 42P01/42703).
func isTaskRunsSchemaMissing(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "no such table") ||
		strings.Contains(message, "no such column") ||
		strings.Contains(message, "42P01") ||
		strings.Contains(message, "42703") ||
		strings.Contains(message, "does not exist")
}

// runtimeJobsHandler mirrors GET /system-metrics/runtime/jobs: rows come
// from the worker-persisted background_task_runs history (newest first by
// updated_at then run_id); a missing table (schema not migrated yet) or a
// nil stats handle answers the empty-items 200 degradation, only a real
// query failure surfaces as a read error.
func (d *Deps) runtimeJobsHandler(w http.ResponseWriter, r *http.Request) {
	page, pageSize, ok := parseRuntimePageQuery(r.URL.Query(), w)
	if !ok {
		return
	}
	items := []any{}
	if d.Stats != nil {
		rows, err := d.queryStats(r, `
			SELECT run_id, job_name, job_type, worker_role, status, started_at, finished_at,
				duration_ms, error_message
			FROM `+d.statsTable("background_task_runs")+`
			ORDER BY updated_at DESC, run_id DESC
		`)
		if err != nil && !isTaskRunsSchemaMissing(err) {
			d.writeReadError(w, err)
			return
		}
		for _, row := range rows {
			items = append(items, backgroundTaskRunItem{
				RunID:        row.text("run_id"),
				JobName:      row.text("job_name"),
				JobType:      row.text("job_type"),
				WorkerRole:   row.text("worker_role"),
				Status:       row.text("status"),
				StartedAt:    row.nullText("started_at"),
				FinishedAt:   row.nullText("finished_at"),
				DurationMs:   row.nullNumber("duration_ms"),
				ErrorMessage: row.nullText("error_message"),
			})
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	kernel.WriteOK(w, paginateSystemMetricsRows(items, page, pageSize), "")
}

func (d *Deps) runtimeQueuesHandler(w http.ResponseWriter, r *http.Request) {
	page, pageSize, ok := parseRuntimePageQuery(r.URL.Query(), w)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	kernel.WriteOK(w, paginateSystemMetricsRows([]any{}, page, pageSize), "")
}

// healthSnapshotJobsTimeout bounds the per-request jobs /health fetch; the
// jobs health listener is a loopback side channel, so a slow jobs process
// must not hold the snapshot route open.
const healthSnapshotJobsTimeout = 2 * time.Second

// healthSnapshotJobsBodyLimit caps the jobs /health body read before JSON
// decoding; the worker payload is a small readiness map, well below this.
const healthSnapshotJobsBodyLimit = 1 << 20

// healthSnapshotJobsReasonLimit keeps a non-JSON or error body excerpt inside
// the reason field readable without echoing unbounded content.
const healthSnapshotJobsReasonLimit = 200

// healthSnapshotHandler mirrors GET /system-metrics/health-snapshot: it
// aggregates the gateway owner readiness (in-process probe, payload passed
// through verbatim) and the jobs process /health (loopback fetch, payload
// passed through verbatim). Every degradation is available:false + reason —
// the route never fails on a missing or unhealthy peer.
func (d *Deps) healthSnapshotHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	kernel.WriteOK(w, map[string]any{
		"checkedAt": d.Now().UTC().Format(time.RFC3339),
		"gateway":   d.gatewayHealthSection(),
		"jobs":      d.jobsHealthSection(r),
	}, "")
}

// gatewayHealthSection embeds the owner readiness payload verbatim; an
// unwired probe degrades instead of failing the snapshot.
func (d *Deps) gatewayHealthSection() map[string]any {
	if d.GatewayReadiness == nil {
		return map[string]any{"available": false, "reason": "gateway readiness 探针未接线"}
	}
	_, payload := d.GatewayReadiness()
	if payload == nil {
		return map[string]any{"available": false, "reason": "gateway readiness 载荷为空"}
	}
	return payload
}

// jobsHealthSection fetches the jobs /health payload over the loopback side
// channel. Missing address (compose never injected it), transport errors,
// non-200 answers and non-JSON bodies all degrade to available:false +
// reason; a 200 JSON object is passed through verbatim.
func (d *Deps) jobsHealthSection(r *http.Request) map[string]any {
	if d.JobsHealthURL == "" {
		return map[string]any{"available": false, "reason": "JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS 未配置"}
	}
	fetchCtx, cancel := context.WithTimeout(r.Context(), healthSnapshotJobsTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, d.JobsHealthURL, nil)
	if err != nil {
		return map[string]any{"available": false, "reason": err.Error()}
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return map[string]any{"available": false, "reason": err.Error()}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, healthSnapshotJobsBodyLimit))
	if err != nil {
		return map[string]any{"available": false, "reason": err.Error()}
	}
	if response.StatusCode != http.StatusOK {
		return map[string]any{"available": false, "reason": fmt.Sprintf("jobs /health 状态码 %d：%s", response.StatusCode, truncateHealthReason(body))}
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return map[string]any{"available": false, "reason": fmt.Sprintf("jobs /health 响应不是 JSON 对象：%s", truncateHealthReason(body))}
	}
	return map[string]any{"available": true, "payload": payload}
}

// truncateHealthReason renders a bounded excerpt of a raw response body for
// the reason field.
func truncateHealthReason(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > healthSnapshotJobsReasonLimit {
		return text[:healthSnapshotJobsReasonLimit]
	}
	return text
}

// parseRuntimePageQuery mirrors systemMetricsRuntimePageQuerySchema; writes
// the 400 and returns ok=false when the bounds fail.
func parseRuntimePageQuery(values url.Values, w http.ResponseWriter) (page, pageSize int, ok bool) {
	page = 1
	pageSize = 10
	if raw := strings.TrimSpace(values.Get("page")); raw != "" {
		parsed, parseErr := parseAlphaInt(raw)
		if parseErr != nil || parsed < 1 {
			kernel.WriteBadRequest(w, "后台任务分页参数不合法")
			return 0, 0, false
		}
		page = parsed
	}
	if raw := strings.TrimSpace(values.Get("pageSize")); raw != "" {
		parsed, parseErr := parseAlphaInt(raw)
		if parseErr != nil || parsed < 10 || parsed > 50 {
			kernel.WriteBadRequest(w, "后台任务分页参数不合法")
			return 0, 0, false
		}
		pageSize = parsed
	}
	return page, pageSize, true
}

// paginateSystemMetricsRows mirrors paginateSystemMetricsRuntimeRows.
func paginateSystemMetricsRows(rows []any, page, pageSize int) map[string]any {
	offset := (page - 1) * pageSize
	total := len(rows)
	slice := []any{}
	if offset < total {
		end := offset + pageSize
		if end > total {
			end = total
		}
		slice = rows[offset:end]
	}
	return map[string]any{
		"items":    slice,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
		"hasMore":  offset+pageSize < total,
	}
}

type systemMetricsTrendOverview struct {
	HourlyTrend                  []systemMetricsTrendPoint    `json:"hourlyTrend"`
	ProcessEventLoopLatestStatus []processEventLoopStatus     `json:"processEventLoopLatestStatus"`
	ProcessEventLoopPeakStatus   []processEventLoopPeakStatus `json:"processEventLoopPeakStatus"`
	ProcessEventLoopTrend        []processEventLoopTrendPoint `json:"processEventLoopTrend"`
}

type systemMetricsTrendPoint struct {
	StatHour                   string `json:"statHour"`
	CPUPercentAvg              *int64 `json:"cpuPercentAvg"`
	MemoryUsedPercentAvg       *int64 `json:"memoryUsedPercentAvg"`
	NetworkRxBytesPerSecondAvg *int64 `json:"networkRxBytesPerSecondAvg"`
	NetworkTxBytesPerSecondAvg *int64 `json:"networkTxBytesPerSecondAvg"`
}

type processEventLoopStatus struct {
	ProcessRole      string  `json:"processRole"`
	SampleAvailable  bool    `json:"sampleAvailable"`
	ProcessPid       *int64  `json:"processPid"`
	SampledAt        *string `json:"sampledAt"`
	EventLoopLagMs   *int64  `json:"eventLoopLagMs"`
	ProcessRssBytes  *int64  `json:"processRssBytes"`
	ProcessHeapUsed  *int64  `json:"processHeapUsedBytes"`
	ProcessHeapTotal *int64  `json:"processHeapTotalBytes"`
}

type processEventLoopPeakStatus struct {
	ProcessRole     string  `json:"processRole"`
	SampleAvailable bool    `json:"sampleAvailable"`
	ProcessPid      *int64  `json:"processPid"`
	SampledAt       *string `json:"sampledAt"`
	EventLoopLagMs  *int64  `json:"eventLoopLagMs"`
}

type processEventLoopTrendPoint struct {
	StatMinute         string `json:"statMinute"`
	ProcessRole        string `json:"processRole"`
	EventLoopLagMsAvg  *int64 `json:"eventLoopLagMsAvg"`
	EventLoopLagMsMax  *int64 `json:"eventLoopLagMsMax"`
	ProcessRssBytesAvg *int64 `json:"processRssBytesAvg"`
	ProcessRssBytesMax *int64 `json:"processRssBytesMax"`
}

// mathRoundMax avoids importing math for one clamp.
func roundNonNegative(value float64) int64 {
	if value < 0 || math.IsNaN(value) {
		return 0
	}
	return int64(value + 0.5)
}
