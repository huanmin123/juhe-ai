package tablemonitor

// HTTP surface of the table-monitor read family (Node
// table-monitor.routes.ts): three admin GETs with the X-Table-Monitor-*
// response headers and the bounded overview cache. The cleanup POST stays
// Node-owned (see the package doc).

import (
	"errors"
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

// writeReadError renders the W6 typed-unavailable outcome as 503 and keeps
// every other failure opaque.
func (d *Deps) writeReadError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrSchemaUnavailable) {
		kernel.WriteError(w, http.StatusServiceUnavailable, ErrSchemaUnavailable.Error())
		return
	}
	kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
}

// Mount wires the table-monitor read routes (requireAdmin family; the Node
// router mounts behind requireAdmin at the /table-monitor prefix).
func (d *Deps) Mount(k *kernel.Kernel, auth *authsys.Deps) {
	prefix := "/__aisys__/api/table-monitor"
	k.Register("GET "+prefix+"/overview", auth.RequireAdmin(http.HandlerFunc(d.overviewHandler)))
	k.Register("GET "+prefix+"/history", auth.RequireAdmin(http.HandlerFunc(d.historyHandler)))
	k.Register("GET "+prefix+"/database-history", auth.RequireAdmin(http.HandlerFunc(d.databaseHistoryHandler)))
}

// Deps bundles the route collaborators.
type Deps struct {
	Store *Store
	// Cache bounds the default overview like Node's tableMonitorOverviewCache
	// (fresh 10min, stale-while-revalidate up to 60min, 30s failure backoff).
	Cache *OverviewCache
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

func (d *Deps) overviewHandler(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	page, hasPage := integerQuery(values.Get("page"))
	pageSize, hasPageSize := integerQuery(values.Get("pageSize"))
	keyword := strings.TrimSpace(values.Get("keyword"))
	refresh := booleanQueryValue(values.Get("refresh"))
	if hasPage && page < 1 {
		kernel.WriteBadRequest(w, "表监控参数无效")
		return
	}
	if hasPageSize && (pageSize < 1 || pageSize > 100) {
		kernel.WriteBadRequest(w, "表监控参数无效")
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
	if cacheable && refresh {
		w.Header().Set("X-Table-Monitor-Cache", "bypass")
	} else if cacheable {
		w.Header().Set("X-Table-Monitor-Cache", "bounded-swr")
	} else {
		w.Header().Set("X-Table-Monitor-Cache", "none")
	}
	startedAt := time.Now()
	var overview Overview
	var err error
	if cacheable && d.Cache != nil && !refresh {
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
				go func() {
					overview, err := d.Store.LoadOverview(r.Context(), 1, 10, "")
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
	limit := defaultTableStorageHistoryLimit
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxHistoryPointsPerSeries {
			kernel.WriteBadRequest(w, "表监控历史参数无效")
			return
		}
		limit = parsed
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
		kernel.WriteBadRequest(w, "数据库增长历史参数无效")
		return
	}
	limit := defaultTableStorageHistoryLimit
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxHistoryPointsPerSeries {
			kernel.WriteBadRequest(w, "数据库增长历史参数无效")
			return
		}
		limit = parsed
	}
	points, err := d.Store.LoadDatabaseHistory(r.Context(), startAt, endAt, limit)
	if err != nil {
		d.writeReadError(w, err)
		return
	}
	kernel.WriteOK(w, points, "")
}

// parseHistoryWindow mirrors normalizeDateRange: RFC3339 bounds, 30-day
// default window ending now, inverted ranges swapped.
func (d *Deps) parseHistoryWindow(values url.Values) (string, string, string) {
	nowIso, nowMillis := d.Store.now()
	startRaw := strings.TrimSpace(values.Get("startAt"))
	endRaw := strings.TrimSpace(values.Get("endAt"))
	if startRaw != "" && !validInstant(startRaw) {
		return "", "", "时间必须是带 Z 或数值 offset 的 RFC3339 时间"
	}
	if endRaw != "" && !validInstant(endRaw) {
		return "", "", "时间必须是带 Z 或数值 offset 的 RFC3339 时间"
	}
	endAt := endRaw
	if endAt == "" {
		endAt = nowIso
	}
	startAt := startRaw
	if startAt == "" {
		startAt = time.UnixMilli(nowMillis - tableMonitorHistoryWindowDays*24*60*60*1000).UTC().Format("2006-01-02T15:04:05.000Z")
	}
	startMillis, okStart := instantMillis(startAt)
	endMillis, okEnd := instantMillis(endAt)
	if !okStart || !okEnd {
		return "", "", "时间必须是带 Z 或数值 offset 的 RFC3339 时间"
	}
	if startMillis > endMillis {
		return endAt, startAt, ""
	}
	return startAt, endAt, ""
}

// validInstant mirrors canonicalizeRfc3339Instant !== undefined.
func validInstant(value string) bool {
	_, ok := instantMillis(value)
	return ok
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

// integerQuery / booleanQueryValue mirror the zod coercions.
func integerQuery(raw string) (int, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0, false
	}
	parsed, err := strconv.Atoi(text)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func booleanQueryValue(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
