package tablemonitor

// HTTP surface of the table-monitor route family (Node
// table-monitor.routes.ts mounted behind requireAdmin at the /table-monitor
// prefix): the three admin GETs with the X-Table-Monitor-* response headers
// and the bounded overview cache, plus the cleanup POST enqueueing the
// non_business_data_cleanup record-maintenance job (see enqueue.go for the
// Go dispatch channel).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

var validDatabaseRoles = map[string]bool{
	"business": true, "dataset": true, "usage-catalog": true,
	"stats": true, "codex-context-state": true,
}

var rfc3339Pattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$`)

// Cleanup dispatch constants (Node defaultCleanupBatchSize/MaxBatches).
const (
	defaultCleanupBatchSize = 5000
	defaultCleanupMaxBatches = 100
	cleanupOperationKey      = "table_monitor.cleanup_non_business_data"
)

// writeReadError renders the W6 typed-unavailable outcome as 503 and keeps
// every other failure opaque.
func (d *Deps) writeReadError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrSchemaUnavailable) {
		kernel.WriteError(w, http.StatusServiceUnavailable, ErrSchemaUnavailable.Error())
		return
	}
	kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
}

// Mount wires the table-monitor routes (requireAdmin family; the Node router
// mounts behind requireAdmin at the /table-monitor prefix, mutationGuard
// wrapping the cleanup POST at the router level).
func (d *Deps) Mount(k *kernel.Kernel, auth *authsys.Deps) {
	prefix := "/__aisys__/api/table-monitor"
	k.Register("GET "+prefix+"/overview", auth.RequireAdmin(http.HandlerFunc(d.overviewHandler)))
	k.Register("GET "+prefix+"/history", auth.RequireAdmin(http.HandlerFunc(d.historyHandler)))
	k.Register("GET "+prefix+"/database-history", auth.RequireAdmin(http.HandlerFunc(d.databaseHistoryHandler)))
	guard := d.cleanupGuard()
	k.Register("POST "+prefix+"/non-business-data/cleanup", auth.RequireAdmin(guard(http.HandlerFunc(d.cleanupHandler))))
}

// cleanupGuard returns the mutation-guarded cleanup handler (requireAdmin
// wraps it at Mount time, mirroring the Node app.use + router-level guard
// order).
func (d *Deps) cleanupGuard() func(http.Handler) http.Handler {
	return kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: cleanupOperationKey,
		Actor:        cleanupActor,
		Fingerprint:  cleanupFingerprint,
	})
}

// Deps bundles the route collaborators.
type Deps struct {
	// Store is the snapshot read surface (*Store implements SnapshotStore;
	// tests inject fakes to drive the SWR refresh timing contract).
	Store SnapshotStore
	// Cache bounds the default overview like Node's tableMonitorOverviewCache
	// (fresh 10min, stale-while-revalidate up to 60min, 30s failure backoff).
	Cache *OverviewCache
	// Dispatch enqueues the cleanup job (the Node record-maintenance channel
	// equivalent; nil keeps the Node "IPC unavailable" receipt shape).
	Dispatch RecordMaintenanceDispatch
	// Sink receives the cleanup operation log (Node recordOperationLogAsync;
	// nil keeps the route functional without the log, mirroring the Node
	// warn-and-continue path).
	Sink authsys.OperationLogSink
}

// SnapshotStore is the table-monitor read surface the handlers consume.
type SnapshotStore interface {
	LoadOverview(ctx context.Context, page, pageSize int, keyword string) (Overview, error)
	LoadTableHistory(ctx context.Context, databaseRole, tableName, startAt, endAt string, limit int) ([]TableHistoryPoint, error)
	LoadDatabaseHistory(ctx context.Context, startAt, endAt string, limit int) ([]DatabaseHistoryPoint, error)
}

// OverviewCache is the single-entry default SWR cache.
type OverviewCache struct {
	mu             sync.Mutex
	fresh          time.Duration
	stale          time.Duration
	failureBackoff time.Duration
	value          *Overview
	storedAt       time.Time
	lastRefresh    time.Time
	refreshFailed  time.Time
	refreshing     bool
	cond           *sync.Cond
}

// NewOverviewCache builds the default cache with the Node window constants.
func NewOverviewCache() *OverviewCache {
	cache := &OverviewCache{
		fresh:          10 * time.Minute,
		stale:          time.Hour,
		failureBackoff: 30 * time.Second,
	}
	cache.cond = sync.NewCond(&cache.mu)
	return cache
}

// Zod v3 issue messages: the 400 body is the first issue's message verbatim
// (shared/http firstIssueMessage). The message set mirrors the aipublic
// zod.go emulation.
const (
	zodRequired    = "Required"
	zodTimeEmpty   = "时间不能为空"
	zodTimeInvalid = "时间必须是带 Z 或数值 offset 的 RFC3339 时间"
)

func zodStringMax(n int) string {
	return fmt.Sprintf("String must contain at most %d character(s)", n)
}

func zodNumberMin(n int) string {
	return fmt.Sprintf("Number must be greater than or equal to %d", n)
}

func zodNumberMax(n int) string {
	return fmt.Sprintf("Number must be less than or equal to %d", n)
}

func (d *Deps) overviewHandler(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	// zod object parse: the first issue in schema field order
	// (page, pageSize, keyword, refresh) is the 400 body.
	page, hasPage, issue := coerceOptionalQueryInt(values, "page", 1, 0)
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	pageSize, hasPageSize, issue := coerceOptionalQueryInt(values, "pageSize", 1, 100)
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	keyword := ""
	if raw, exists := values["keyword"]; exists && len(raw) > 0 {
		keyword = strings.TrimSpace(raw[0])
		if runeCount(keyword) > 200 {
			kernel.WriteBadRequest(w, zodStringMax(200))
			return
		}
	}
	refresh, hasRefresh, issue := booleanQueryValue(values)
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	normalizedPage := 1
	if hasPage {
		normalizedPage = page
	}
	normalizedPageSize := 10
	if hasPageSize {
		normalizedPageSize = pageSize
	}
	cacheable := keyword == "" && normalizedPage == 1 && normalizedPageSize == 10
	if cacheable && (hasRefresh && refresh) {
		w.Header().Set("X-Table-Monitor-Cache", "bypass")
	} else if cacheable {
		w.Header().Set("X-Table-Monitor-Cache", "bounded-swr")
	} else {
		w.Header().Set("X-Table-Monitor-Cache", "none")
	}
	startedAt := time.Now()
	var overview Overview
	var err error
	if cacheable && d.Cache != nil && !(hasRefresh && refresh) {
		overview, err = d.cachedOverview(r)
	} else {
		overview, err = d.Store.LoadOverview(r.Context(), normalizedPage, normalizedPageSize, keyword)
	}
	if err != nil {
		d.writeReadError(w, err)
		return
	}
	w.Header().Set("X-Table-Monitor-Duration-Ms", strconv.FormatInt(time.Since(startedAt).Milliseconds(), 10))
	kernel.WriteOK(w, overview, "")
}

// cachedOverview serves the bounded-SWR contract: fresh hits return directly,
// stale hits return the cached value and refresh in the background, failures
// back off for 30 seconds.
func (d *Deps) cachedOverview(r *http.Request) (Overview, error) {
	cache := d.Cache
	now := time.Now()
	cache.mu.Lock()
	if cache.value != nil {
		age := now.Sub(cache.storedAt)
		if age <= cache.fresh {
			cached := *cache.value
			cache.mu.Unlock()
			return cached, nil
		}
		if age <= cache.stale {
			cached := *cache.value
			if !cache.refreshing && now.Sub(cache.refreshFailed) >= cache.failureBackoff {
				cache.refreshing = true
				// The refresh outlives the request (Node refreshes on a
				// detached timer); the request context would be canceled the
				// moment the handler returns, so detach with WithoutCancel.
				refreshCtx := context.WithoutCancel(r.Context())
				go func() {
					overview, err := d.Store.LoadOverview(refreshCtx, 1, 10, "")
					cache.mu.Lock()
					defer cache.mu.Unlock()
					cache.refreshing = false
					if err == nil {
						cache.value = &overview
						cache.storedAt = time.Now()
						cache.lastRefresh = cache.storedAt
					} else {
						cache.refreshFailed = time.Now()
					}
					cache.cond.Broadcast()
				}()
			}
			cache.mu.Unlock()
			return cached, nil
		}
		if now.Sub(cache.refreshFailed) < cache.failureBackoff {
			cache.mu.Unlock()
			return Overview{}, errCacheBackoff
		}
	}
	cache.mu.Unlock()
	overview, err := d.Store.LoadOverview(r.Context(), 1, 10, "")
	cache.mu.Lock()
	if err == nil {
		cache.value = &overview
		cache.storedAt = time.Now()
		cache.lastRefresh = cache.storedAt
	} else {
		cache.refreshFailed = time.Now()
	}
	cache.mu.Unlock()
	return overview, err
}

var errCacheBackoff = errCacheBackoffError{}

type errCacheBackoffError struct{}

func (errCacheBackoffError) Error() string {
	return "表监控概览刷新暂不可用，请稍后重试"
}

func (d *Deps) historyHandler(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	databaseRole := strings.TrimSpace(values.Get("databaseRole"))
	tableName := strings.TrimSpace(values.Get("tableName"))
	if !validDatabaseRoles[databaseRole] || tableName == "" {
		kernel.WriteBadRequest(w, "表监控历史参数无效")
		return
	}
	startAt, endAt, badRequest := d.parseHistoryWindow(values)
	if badRequest != "" {
		kernel.WriteBadRequest(w, badRequest)
		return
	}
	limit, hasLimit, issue := coerceOptionalQueryInt(values, "limit", 1, maxHistoryPointsPerSeries)
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	if !hasLimit {
		limit = defaultTableStorageHistoryLimit
	}
	points, err := d.Store.LoadTableHistory(r.Context(), databaseRole, tableName, startAt, endAt, limit)
	if err != nil {
		d.writeReadError(w, err)
		return
	}
	kernel.WriteOK(w, points, "")
}

func (d *Deps) databaseHistoryHandler(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	startAt, endAt, badRequest := d.parseHistoryWindow(values)
	if badRequest != "" {
		kernel.WriteBadRequest(w, badRequest)
		return
	}
	limit, hasLimit, issue := coerceOptionalQueryInt(values, "limit", 1, maxHistoryPointsPerSeries)
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	if !hasLimit {
		limit = defaultTableStorageHistoryLimit
	}
	points, err := d.Store.LoadDatabaseHistory(r.Context(), startAt, endAt, limit)
	if err != nil {
		d.writeReadError(w, err)
		return
	}
	kernel.WriteOK(w, points, "")
}

// parseHistoryWindow mirrors normalizeDateRange + the zod time schema:
// present-but-blank bounds fail the min(1) chain ('时间不能为空'), invalid
// instants fail the refine ('时间必须是…RFC3339 时间'), valid bounds pass
// through canonicalized (UTC ISO, the schema transform) and inverted ranges
// swap.
func (d *Deps) parseHistoryWindow(values url.Values) (string, string, string) {
	nowIso, nowMillis := d.storeNow()
	startAt, issue := optionalInstantQuery(values, "startAt")
	if issue != "" {
		return "", "", issue
	}
	endAt, issue := optionalInstantQuery(values, "endAt")
	if issue != "" {
		return "", "", issue
	}
	if endAt == "" {
		endAt = nowIso
	}
	if startAt == "" {
		startAt = time.UnixMilli(nowMillis - tableMonitorHistoryWindowDays*24*60*60*1000).UTC().Format("2006-01-02T15:04:05.000Z")
	}
	startMillis, okStart := instantMillis(startAt)
	endMillis, okEnd := instantMillis(endAt)
	if !okStart || !okEnd {
		return "", "", zodTimeInvalid
	}
	if startMillis > endMillis {
		return endAt, startAt, ""
	}
	return startAt, endAt, ""
}

// optionalInstantQuery mirrors absoluteDateTimeQuerySchema.optional(): absent
// -> ("", ""); present-but-blank -> the min(1) issue; malformed -> the refine
// issue; valid -> the canonical UTC ISO value.
func optionalInstantQuery(values url.Values, key string) (string, string) {
	raw, exists := values[key]
	if !exists || len(raw) == 0 {
		return "", ""
	}
	text := strings.TrimSpace(raw[0])
	if text == "" {
		return "", zodTimeEmpty
	}
	canonical, ok := canonicalInstant(text)
	if !ok {
		return "", zodTimeInvalid
	}
	return canonical, ""
}

// storeNow resolves the clock pair from the concrete store when available;
// the default window anchors at the process clock otherwise.
func (d *Deps) storeNow() (string, int64) {
	if store, ok := d.Store.(*Store); ok && store != nil && store.now != nil {
		return store.now()
	}
	now := time.Now()
	return now.UTC().Format("2006-01-02T15:04:05.000Z"), now.UnixMilli()
}

// validInstant mirrors canonicalizeRfc3339Instant !== undefined.
func validInstant(value string) bool {
	_, ok := instantMillis(value)
	return ok
}

// canonicalInstant mirrors canonicalizeRfc3339Instant: UTC ISO with
// millisecond precision (Node toISOString).
func canonicalInstant(value string) (string, bool) {
	millis, ok := instantMillis(value)
	if !ok {
		return "", false
	}
	return time.UnixMilli(millis).UTC().Format("2006-01-02T15:04:05.000Z"), true
}

func instantMillis(value string) (int64, bool) {
	if !rfc3339Pattern.MatchString(strings.TrimSpace(value)) {
		return 0, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// coerceOptionalQueryInt mirrors z.coerce.number().int().min(min)[.max(max)]
// .optional() over the first query value (Number('') === 0 fails the min
// bound; NaN renders the zod invalid_type nan message; non-integers the
// integer message).
func coerceOptionalQueryInt(values url.Values, key string, min, max int) (int, bool, string) {
	raw, exists := values[key]
	if !exists || len(raw) == 0 {
		return 0, false, ""
	}
	text := strings.TrimSpace(raw[0])
	if text == "" {
		if min > 0 {
			return 0, false, zodNumberMin(min)
		}
		return 0, true, ""
	}
	value, ok := coerceNumber(text)
	if !ok {
		return 0, false, "Expected number, received nan"
	}
	intValue, isInt := value.(int)
	if !isInt {
		return 0, false, "Expected integer, received float"
	}
	if intValue < min {
		return 0, false, zodNumberMin(min)
	}
	if max > 0 && intValue > max {
		return 0, false, zodNumberMax(max)
	}
	return intValue, true, ""
}

// coerceNumber mirrors Number(text) plus the integer split.
func coerceNumber(text string) (any, bool) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil {
		return nil, false
	}
	if parsed != float64(int64(parsed)) {
		return parsed, true
	}
	return int(parsed), true
}

// booleanQueryValue mirrors the refresh preprocess over the first query
// value: 1/true/yes -> true, 0/false/no -> false, anything else keeps the raw
// string so the z.boolean() check rejects it ("Expected boolean, received
// string").
func booleanQueryValue(values url.Values) (bool, bool, string) {
	raw, exists := values["refresh"]
	if !exists || len(raw) == 0 {
		return false, false, ""
	}
	normalized := strings.ToLower(strings.TrimSpace(raw[0]))
	switch normalized {
	case "1", "true", "yes":
		return true, true, ""
	case "0", "false", "no":
		return false, true, ""
	}
	return false, true, "Expected boolean, received string"
}

func runeCount(value string) int {
	return len([]rune(value))
}

// ---------------------------------------------------------------------------
// POST /non-business-data/cleanup (Node nonBusinessDataCleanupSchema +
// enqueueRecordMaintenanceJobWithResultAsync receipt).
// ---------------------------------------------------------------------------

// cleanupReceipt mirrors NonBusinessDataCleanupReceipt (field order and the
// omitempty blockedReason included).
type cleanupReceipt struct {
	CutoffAt      string  `json:"cutoffAt"`
	Queued        bool    `json:"queued"`
	JobID         string  `json:"jobId"`
	SubmittedAt   string  `json:"submittedAt"`
	BlockedReason *string `json:"blockedReason,omitempty"`
}

func (d *Deps) cleanupHandler(w http.ResponseWriter, r *http.Request) {
	body := kernel.ParsedBody(r)
	if body == nil {
		body = map[string]any{}
	}
	// zod object parse order: field chain first, then the .strict()
	// unrecognized-key pass.
	cutoffRaw, issue := requiredCutoffField(body)
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	if unknown := unrecognizedKeys(body, "cutoffAt"); len(unknown) > 0 {
		kernel.WriteBadRequest(w, "Unrecognized key(s) in object: "+strings.Join(unknown, ", "))
		return
	}
	cutoffAt, ok := canonicalInstant(cutoffRaw)
	if !ok {
		kernel.WriteBadRequest(w, zodTimeInvalid)
		return
	}
	nowIso, _ := d.storeNow()
	if cutoffAt > nowIso {
		kernel.WriteBadRequest(w, "清理截止时间不能晚于当前时间")
		return
	}

	job := RecordMaintenanceJob{
		CutoffAt:   cutoffAt,
		BatchSize:  defaultCleanupBatchSize,
		MaxBatches: defaultCleanupMaxBatches,
	}
	now := time.Now()
	job.ID = newRecordMaintenanceJobID(now)
	job.CreatedAt = now.UTC().Format("2006-01-02T15:04:05.000Z")

	result := DispatchResult{Queued: false, DroppedReason: "worker_ipc_unavailable"}
	if d.Dispatch != nil {
		result = d.Dispatch.EnqueueNonBusinessDataCleanup(r.Context(), job)
	}
	receipt := cleanupReceipt{
		CutoffAt:    job.CutoffAt,
		Queued:      result.Queued,
		JobID:       job.ID,
		SubmittedAt: job.CreatedAt,
	}
	if !result.Queued {
		blocked := cleanupBlockedReason(result.DroppedReason)
		receipt.BlockedReason = &blocked
	}

	// Node records the operation log after the enqueue and never fails the
	// response on log errors (warn-and-continue).
	if d.Sink != nil {
		d.recordCleanupOperation(r, job, &receipt)
	}
	kernel.WriteOK(w, receipt, "")
}

// cleanupBlockedReason keeps the Node droppedReason vocabulary.
func cleanupBlockedReason(droppedReason string) string {
	if droppedReason == "worker_ipc_unavailable" {
		return "后台 worker 投递通道不可用，非业务数据清理任务未提交；请确认后端主进程、DB service 和 background worker 都由同一个 supervisor 启动"
	}
	return "后台 worker 投递失败，非业务数据清理任务未提交；请稍后重试或查看后台日志"
}

// requiredCutoffField mirrors cutoffAt: absoluteDateTimeQuerySchema over the
// decoded JSON body (absent -> Required; non-string -> invalid_type; blank ->
// the min(1) issue; the format refine runs in the handler on the trimmed
// value).
func requiredCutoffField(body map[string]any) (string, string) {
	value, present := body["cutoffAt"]
	if !present {
		return "", zodRequired
	}
	text, isString := value.(string)
	if !isString {
		return "", "Expected string, received " + zodReceivedJSON(value)
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", zodTimeEmpty
	}
	return trimmed, ""
}

// zodReceivedJSON mirrors zodReceived for decoded JSON values.
func zodReceivedJSON(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64, int, int64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

// unrecognizedKeys mirrors the zod .strict() pass over the decoded body.
func unrecognizedKeys(body map[string]any, allowed ...string) []string {
	var unknown []string
	for key := range body {
		found := false
		for _, candidate := range allowed {
			if key == candidate {
				found = true
				break
			}
		}
		if !found {
			unknown = append(unknown, key)
		}
	}
	for i := 1; i < len(unknown); i++ {
		for j := i; j > 0 && unknown[j] < unknown[j-1]; j-- {
			unknown[j], unknown[j-1] = unknown[j-1], unknown[j]
		}
	}
	return unknown
}

func cleanupActor(r *http.Request) string {
	if auth := authsys.AuthContextFrom(r); auth != nil {
		return auth.SystemAccountID
	}
	return "anonymous"
}

// cleanupFingerprint mirrors the Node guard fingerprint
// { cutoffAt: bodyField(req, 'cutoffAt') }.
func cleanupFingerprint(r *http.Request) (any, error) {
	return map[string]any{"cutoffAt": kernel.BodyField(r, "cutoffAt")}, nil
}

// recordCleanupOperation mirrors recordNonBusinessDataCleanupOperation.
func (d *Deps) recordCleanupOperation(r *http.Request, job RecordMaintenanceJob, receipt *cleanupReceipt) {
	auth := authsys.AuthContextFrom(r)
	summary := "非业务数据清理未提交"
	switch {
	case receipt.Queued:
		summary = "提交非业务数据硬清理任务：" + receipt.JobID
	case receipt.BlockedReason != nil:
		summary = "非业务数据清理未提交：" + *receipt.BlockedReason
	}
	metadata := map[string]any{
		"cutoffAt":    receipt.CutoffAt,
		"batchSize":   job.BatchSize,
		"maxBatches":  job.MaxBatches,
		"queued":      receipt.Queued,
		"jobId":       receipt.JobID,
		"submittedAt": receipt.SubmittedAt,
	}
	// Node JSON.stringify drops undefined keys: queued receipts carry no
	// blockedReason field at all.
	if receipt.BlockedReason != nil {
		metadata["blockedReason"] = *receipt.BlockedReason
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return
	}
	blockedAfter := ""
	if receipt.BlockedReason != nil {
		blockedAfter = *receipt.BlockedReason
	}
	d.Sink.Record(authsys.OperationLogEntry{
		ActorSystemAccountID: authContextField(auth, func(a *authsys.AuthContext) string { return a.SystemAccountID }),
		ActorUsername:        authContextField(auth, func(a *authsys.AuthContext) string { return a.Username }),
		ActorDisplayName:     authContextField(auth, func(a *authsys.AuthContext) string { return a.DisplayName }),
		ActorRole:            authContextField(auth, func(a *authsys.AuthContext) string { return a.Role }),
		Mode:                 "admin",
		Module:               "table_monitor",
		Action:               "cleanup_non_business_data",
		OperationKey:         cleanupOperationKey,
		ResourceType:         "non_business_data",
		ResourceID:           "dataset_stats_usage_shards",
		ResourceName:         "非业务数据",
		Summary:              summary,
		DetailLevel:          "full",
		VisibilityScope:      "admin_only",
		Changes: []authsys.OperationLogChange{
			{Field: "cutoffAt", Label: "清理截止时间", After: receipt.CutoffAt},
			{Field: "batchSize", Label: "单批数量", After: strconv.Itoa(job.BatchSize)},
			{Field: "maxBatches", Label: "最大批次", After: strconv.Itoa(job.MaxBatches)},
			{Field: "jobId", Label: "后台任务", After: receipt.JobID},
			{Field: "blockedReason", Label: "未提交原因", After: blockedAfter},
		},
		Metadata: metadataJSON,
	}, r)
}

func authContextField(auth *authsys.AuthContext, pick func(*authsys.AuthContext) string) string {
	if auth == nil {
		return ""
	}
	return pick(auth)
}
