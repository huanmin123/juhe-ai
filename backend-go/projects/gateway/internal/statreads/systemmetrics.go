package statreads

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// System-metrics read family (Node system-metrics.repository.ts
// getSystemMetricsTrend + stats.routes.ts runtime routes). The runtime
// summary/jobs/queues routes read the Node db-service IPC runtime snapshot;
// the Go gateway has no such IPC peer, so they serve the exact unavailable
// degradation the Node code produces when requestServerSystemMetricsRuntime
// Snapshot fails (runtimeSnapshotAvailable:false, empty rows).
const (
	processEventLoopPeakWindowMS    = int64(24 * 60 * 60 * 1000)
	processEventLoopLatestFreshness = int64(2 * 60 * 1000)
	goRuntimeTrendRequestTimeout    = 2500 * time.Millisecond
	goRuntimeTrendPayloadMaxItems   = 24*90 + 1
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
	// gateway:* / control:* / control-replica:* families.
	for _, prefix := range []string{"gateway:", "control:", "control-replica:"} {
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

// goRuntimeTrendHandler mirrors GET /system-metrics/go-runtime-trend: proxy to
// the Go jobs metrics server loopback origin with the same 503 degradation
// and payload validation as Node.
func (d *Deps) goRuntimeTrendHandler(w http.ResponseWriter, r *http.Request) {
	startDate, endDate, badRequest := parseUsageOverviewQuery(r.URL.Query())
	if badRequest != "" {
		kernel.WriteBadRequest(w, "Go 运行时指标日期范围不合法")
		return
	}
	rng, err := d.normalizeSystemMetricsDateRange(r.Context(), startDate, endDate)
	if err != nil {
		writeGoRuntimeUnavailable(w)
		return
	}
	location, err := d.timezoneLocation(r.Context())
	if err != nil {
		writeGoRuntimeUnavailable(w)
		return
	}
	from := startOfZonedDateKeyIso(rng.StartDate, location)
	to := startOfZonedDateKeyIso(nextCalendarDateKey(rng.EndDate), location)
	if from == "" || to == "" {
		kernel.WriteBadRequest(w, "Go 运行时指标日期范围无法解析")
		return
	}
	if d.GoRuntimeMetricsURL == "" {
		writeGoRuntimeUnavailable(w)
		return
	}
	endpoint, err := url.Parse(strings.TrimRight(d.GoRuntimeMetricsURL, "/") + "/__aisys__/api/stats/go-runtime-trend")
	if err != nil {
		writeGoRuntimeUnavailable(w)
		return
	}
	query := endpoint.Query()
	query.Set("from", from)
	query.Set("to", to)
	endpoint.RawQuery = query.Encode()
	ctx, cancel := context.WithTimeout(r.Context(), goRuntimeTrendRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		writeGoRuntimeUnavailable(w)
		return
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		if isTimeout(err) {
			kernel.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "Go 运行时指标请求超时"})
			return
		}
		writeGoRuntimeUnavailable(w)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writeGoRuntimeUnavailable(w)
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		writeGoRuntimeUnavailable(w)
		return
	}
	var payload goRuntimeTrendPayload
	if err := json.Unmarshal(body, &payload); err != nil || !validGoRuntimePayload(&payload) {
		kernel.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "Go 运行时指标响应无效"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	kernel.WriteOK(w, map[string]any{
		"runtimeKind": "go",
		"service":     payload.Service,
		"role":        payload.Role,
		"timezone":    location.String(),
		"range":       rng,
		"items":       payload.Items,
	}, "")
}

func writeGoRuntimeUnavailable(w http.ResponseWriter) {
	kernel.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "Go 运行时指标暂不可用"})
}

// isTimeout mirrors the Node AbortError/TimeoutError branch.
func isTimeout(err error) bool {
	type timeouter interface{ Timeout() bool }
	if typed, ok := err.(timeouter); ok && typed.Timeout() {
		return true
	}
	return strings.Contains(err.Error(), "context deadline exceeded") ||
		strings.Contains(err.Error(), "Client.Timeout")
}

func validGoRuntimePayload(payload *goRuntimeTrendPayload) bool {
	if payload.RuntimeKind != "go" || payload.Service == "" || payload.Role == "" {
		return false
	}
	return len(payload.Items) <= goRuntimeTrendPayloadMaxItems
}

// goRuntimeTrendPayload mirrors goRuntimeTrendPayloadSchema (items validated
// structurally; unknown item fields are tolerated by json.Unmarshal).
type goRuntimeTrendPayload struct {
	RuntimeKind string               `json:"runtimeKind"`
	Service     string               `json:"service"`
	Role        string               `json:"role"`
	Items       []goRuntimeTrendItem `json:"items"`
}

type goRuntimeTrendItem struct {
	WindowStart       string  `json:"windowStart"`
	WindowEnd         string  `json:"windowEnd"`
	Service           string  `json:"service"`
	Role              string  `json:"role"`
	RuntimeKind       string  `json:"runtimeKind"`
	SampleCount       int64   `json:"sampleCount"`
	GoroutinesAvg     float64 `json:"goroutinesAvg"`
	GoroutinesMax     float64 `json:"goroutinesMax"`
	HeapAllocBytesAvg float64 `json:"heapAllocBytesAvg"`
	HeapAllocBytesMax float64 `json:"heapAllocBytesMax"`
	HeapLiveBytesAvg  float64 `json:"heapLiveBytesAvg"`
	HeapLiveBytesMax  float64 `json:"heapLiveBytesMax"`
	HeapObjectsAvg    float64 `json:"heapObjectsAvg"`
	HeapObjectsMax    float64 `json:"heapObjectsMax"`
	ThreadsAvg        float64 `json:"threadsAvg"`
	ThreadsMax        float64 `json:"threadsMax"`
}

// ---------------------------------------------------------------------------
// Runtime snapshot routes: the Go gateway has no Node db-service IPC peer, so
// every route serves the documented unavailable degradation (Node's
// loadSystemMetricsRuntimeSnapshot catch(() => undefined) path).
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

func (d *Deps) runtimeJobsHandler(w http.ResponseWriter, r *http.Request) {
	page, pageSize, ok := parseRuntimePageQuery(r.URL.Query(), w)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	kernel.WriteOK(w, paginateSystemMetricsRows([]any{}, page, pageSize), "")
}

func (d *Deps) runtimeQueuesHandler(w http.ResponseWriter, r *http.Request) {
	page, pageSize, ok := parseRuntimePageQuery(r.URL.Query(), w)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	kernel.WriteOK(w, paginateSystemMetricsRows([]any{}, page, pageSize), "")
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
