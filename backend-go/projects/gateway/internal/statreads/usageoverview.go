package statreads

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Usage-overview read family (Node usage-stats.repository.ts
// getUsageStatsOverview* + stats.routes.ts /usage-window). All five sections
// share the usageOverviewQuerySchema date contract and the fixed 31-day
// default window.

// parseUsageOverviewQuery mirrors usageOverviewQuerySchema: both bounds are
// optional YYYY-MM-DD strings (zod trims before the regex).
func parseUsageOverviewQuery(values url.Values) (startDate, endDate string, badRequest string) {
	startDate = strings.TrimSpace(values.Get("startDate"))
	endDate = strings.TrimSpace(values.Get("endDate"))
	if startDate != "" && !dateKeyPattern.MatchString(startDate) {
		return "", "", "开始日期格式应为 YYYY-MM-DD"
	}
	if endDate != "" && !dateKeyPattern.MatchString(endDate) {
		return "", "", "结束日期格式应为 YYYY-MM-DD"
	}
	return startDate, endDate, ""
}

// normalizeUsageOverviewDateRange mirrors normalizeUsageOverviewDateRangeAsync.
func (d *Deps) normalizeUsageOverviewDateRange(ctx context.Context, startDate, endDate string) (Range, error) {
	location, err := d.timezoneLocation(ctx)
	if err != nil {
		return Range{}, err
	}
	todayKey := dateKeyIn(d.Now(), location)
	if startDate == "" && endDate == "" {
		return fixedUsageStatsDefaultRange(todayKey), nil
	}
	if startDate == "" {
		startDate = endDate
	}
	if startDate == "" {
		startDate = todayKey
	}
	if endDate == "" {
		endDate = startDate
	}
	if endDate == "" {
		endDate = todayKey
	}
	return normalizeRange(startDate, endDate, todayKey), nil
}

// normalizeStatsDateRange mirrors normalizeStatsDateRangeAsync (ai-performance
// default window: yesterday..today).
func (d *Deps) normalizeStatsDateRange(ctx context.Context, startDate, endDate string) (Range, error) {
	location, err := d.timezoneLocation(ctx)
	if err != nil {
		return Range{}, err
	}
	now := d.Now()
	todayKey := dateKeyIn(now, location)
	defaultStart := dateKeyIn(now.Add(-time.Duration(2*dayMS)*time.Millisecond), location)
	defaultEnd := todayKey
	if startDate == "" {
		startDate = endDate
	}
	if startDate == "" {
		startDate = defaultStart
	}
	if endDate == "" {
		endDate = startDate
	}
	if endDate == "" {
		endDate = defaultEnd
	}
	return normalizeRange(startDate, endDate, todayKey), nil
}

// normalizeSystemMetricsDateRange mirrors normalizeSystemMetricsDateRangeAsync
// (default: today..today).
func (d *Deps) normalizeSystemMetricsDateRange(ctx context.Context, startDate, endDate string) (Range, error) {
	location, err := d.timezoneLocation(ctx)
	if err != nil {
		return Range{}, err
	}
	todayKey := dateKeyIn(d.Now(), location)
	if startDate == "" {
		startDate = endDate
	}
	if startDate == "" {
		startDate = todayKey
	}
	if endDate == "" {
		endDate = startDate
	}
	if endDate == "" {
		endDate = todayKey
	}
	return normalizeRange(startDate, endDate, todayKey), nil
}

// overviewSectionHandler routes the five usage-overview sections by the
// request path suffix (one Node handler family, one Go registration helper).
func (d *Deps) overviewSectionHandler(selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope := requestScope(r)
		if selfOnly {
			scope = selfScope(r)
		}
		section := sectionFromPath(r.URL.Path)
		startDate, endDate, badRequest := parseUsageOverviewQuery(r.URL.Query())
		if badRequest != "" {
			kernel.WriteBadRequest(w, badRequest)
			return
		}
		rng, err := d.normalizeUsageOverviewDateRange(r.Context(), startDate, endDate)
		if err != nil {
			d.writeReadError(w, err)
			return
		}
		switch section {
		case "summary":
			payload, err := d.usageOverviewSummary(r, scope, rng)
			d.writeSection(w, payload, err)
		case "daily-trend":
			payload, err := d.usageOverviewDailyTrend(r, scope, rng)
			d.writeSection(w, payload, err)
		case "hourly-trend":
			payload, err := d.usageOverviewHourlyTrend(r, scope, rng)
			d.writeSection(w, payload, err)
		case "model-distribution":
			payload, err := d.usageOverviewModelDistribution(r, scope, rng)
			d.writeSection(w, payload, err)
		case "errors":
			payload, err := d.usageOverviewErrors(r, scope, rng)
			d.writeSection(w, payload, err)
		default:
			kernel.WriteAPINotFound(w)
		}
	}
}

func sectionFromPath(path string) string {
	const marker = "/usage-overview/"
	index := strings.LastIndex(path, marker)
	if index < 0 {
		return ""
	}
	return strings.TrimSuffix(path[index+len(marker):], "/")
}

// queryIsPresent reports whether the query carries any value for the key.
func queryIsPresent(values url.Values, key string) bool {
	_, ok := values[key]
	return ok
}

func (d *Deps) writeSection(w http.ResponseWriter, payload any, err error) {
	if err != nil {
		d.writeReadError(w, err)
		return
	}
	kernel.WriteOK(w, payload, "")
}

// usageWindowHandler mirrors GET /usage-window.
func (d *Deps) usageWindowHandler(w http.ResponseWriter, r *http.Request) {
	location, err := d.timezoneLocation(r.Context())
	if err != nil {
		d.writeReadError(w, err)
		return
	}
	rng := fixedUsageStatsDefaultRange(dateKeyIn(d.Now(), location))
	kernel.WriteOK(w, map[string]any{
		"timezone":  location.String(),
		"startDate": rng.StartDate,
		"endDate":   rng.EndDate,
		"days":      rng.Days,
		"maxDays":   rng.MaxDays,
	}, "")
}

// usageOverviewStatsScope mirrors usageOverviewStatsScope.
func usageOverviewStatsScope(scope AccessScope) (systemAccountID, scopeID string) {
	if scopedID := scope.scopedID(); scopedID != "" {
		return scopedID, scopedID
	}
	if scope.canAccessAll() {
		return globalStatsSystemAccountID, globalStatsScopeID
	}
	id := scope.currentID()
	return id, id
}
