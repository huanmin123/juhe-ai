package managementclientipstats

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/timezonecompat"
)

const (
	defaultListPageSize   = 20
	maxListWindowRowCount = 1000
	maxUsageRangeDays     = 31
)

type Service struct {
	listReader               port.ManagementClientIPStatsListReader
	registryReader           port.ManagementClientIPStatsRegistryReader
	detailReader             port.ManagementClientIPStatsDetailReader
	usageStatsTimezoneReader port.ManagementUsageStatsTimezoneReader
	now                      func() time.Time
}

type ServiceOptions struct {
	ListReader               port.ManagementClientIPStatsListReader
	RegistryReader           port.ManagementClientIPStatsRegistryReader
	DetailReader             port.ManagementClientIPStatsDetailReader
	UsageStatsTimezoneReader port.ManagementUsageStatsTimezoneReader
	Now                      func() time.Time
}

type ListInput struct {
	Page              int
	PageSize          int
	Keyword           string
	Status            string
	StartDate         string
	EndDate           string
	LastUsedStartDate string
	LastUsedEndDate   string
	SortField         string
	SortOrder         string
}

type UsageRange struct {
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	Days      int    `json:"days"`
	MaxDays   int    `json:"maxDays"`
}

type UsageSummary struct {
	RequestCount        int64    `json:"requestCount"`
	SuccessCount        int64    `json:"successCount"`
	ErrorCount          int64    `json:"errorCount"`
	ErrorRate           float64  `json:"errorRate"`
	InputTokens         int64    `json:"inputTokens"`
	OutputTokens        int64    `json:"outputTokens"`
	CacheReadTokens     int64    `json:"cacheReadTokens"`
	CacheReadCost       float64  `json:"cacheReadCost"`
	CacheWriteTokens    int64    `json:"cacheWriteTokens"`
	CacheWrite1hTokens  int64    `json:"cacheWrite1hTokens"`
	CacheWriteCost      float64  `json:"cacheWriteCost"`
	ThinkingTokens      int64    `json:"thinkingTokens"`
	InputImageTokens    int64    `json:"inputImageTokens"`
	OutputImageTokens   int64    `json:"outputImageTokens"`
	TotalTokens         int64    `json:"totalTokens"`
	TotalCost           float64  `json:"totalCost"`
	ActiveDays          int      `json:"activeDays"`
	AverageDurationMs   *float64 `json:"averageDurationMs,omitempty"`
	AverageFirstTokenMs *float64 `json:"averageFirstTokenMs,omitempty"`
	MaxDurationMs       *int64   `json:"maxDurationMs,omitempty"`
	LastUsedAt          *string  `json:"lastUsedAt,omitempty"`
	LastErrorAt         *string  `json:"lastErrorAt,omitempty"`
}

type ListItem struct {
	IPHash         string       `json:"ipHash"`
	AggregateIPKey string       `json:"aggregateIpKey"`
	LastSeenAt     *string      `json:"lastSeenAt,omitempty"`
	Status         string       `json:"status"`
	RangeUsage     UsageSummary `json:"rangeUsage"`
}

type ListResult struct {
	Items          []ListItem `json:"items"`
	PageUpperBound int        `json:"pageUpperBound"`
	HasMore        bool       `json:"hasMore"`
	Page           int        `json:"page"`
	PageSize       int        `json:"pageSize"`
	Range          UsageRange `json:"range"`
	RangeReady     bool       `json:"rangeReady"`
}

func NewService(reader port.ManagementClientIPStatsListReader) *Service {
	options := ServiceOptions{ListReader: reader}
	if registryReader, ok := any(reader).(port.ManagementClientIPStatsRegistryReader); ok {
		options.RegistryReader = registryReader
	}
	if detailReader, ok := any(reader).(port.ManagementClientIPStatsDetailReader); ok {
		options.DetailReader = detailReader
	}
	if timezoneReader, ok := any(reader).(port.ManagementUsageStatsTimezoneReader); ok {
		options.UsageStatsTimezoneReader = timezoneReader
	}
	return NewServiceWithOptions(options)
}

func NewServiceWithOptions(options ServiceOptions) *Service {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		listReader:               options.ListReader,
		registryReader:           options.RegistryReader,
		detailReader:             options.DetailReader,
		usageStatsTimezoneReader: options.UsageStatsTimezoneReader,
		now:                      now,
	}
}

func (s *Service) List(ctx context.Context, input ListInput) (ListResult, error) {
	if s.listReader == nil {
		return ListResult{}, fmt.Errorf("management client IP stats list reader is required")
	}
	location, err := s.usageStatsLocation(ctx)
	if err != nil {
		return ListResult{}, err
	}
	now := s.now()
	rangeValue := normalizeUsageRange(input.StartDate, input.EndDate, now, location)
	lastUsedStartAt, lastUsedEndExclusiveAt := normalizeLastUsedWindow(
		input.LastUsedStartDate,
		input.LastUsedEndDate,
		now,
		location,
	)
	pageSize := normalizePageSize(input.PageSize)
	page := normalizePage(input.Page, pageSize)
	sortField, sortOrder := normalizeSort(input.SortField, input.SortOrder)

	pageValue, err := s.listReader.ListManagementClientIPStats(ctx, port.ManagementClientIPStatsListInput{
		StartDate:              rangeValue.StartDate,
		EndDate:                rangeValue.EndDate,
		LastUsedStartAt:        lastUsedStartAt,
		LastUsedEndExclusiveAt: lastUsedEndExclusiveAt,
		Keyword:                trimECMAScriptWhitespace(input.Keyword),
		Status:                 normalizeStatus(input.Status),
		SortField:              sortField,
		SortOrder:              sortOrder,
		Now:                    now,
		Limit:                  pageSize + 1,
		Offset:                 (page - 1) * pageSize,
	})
	if err != nil {
		return ListResult{}, err
	}
	items := make([]ListItem, 0, len(pageValue.Rows))
	for _, row := range pageValue.Rows {
		items = append(items, listItem(row))
	}
	upperBound := (page-1)*pageSize + len(items)
	if pageValue.HasMore {
		upperBound++
	}
	return ListResult{
		Items:          items,
		PageUpperBound: upperBound,
		HasMore:        pageValue.HasMore,
		Page:           page,
		PageSize:       pageSize,
		Range:          rangeValue,
		RangeReady:     pageValue.RangeReady,
	}, nil
}

func (s *Service) usageStatsLocation(ctx context.Context) (*time.Location, error) {
	if s.usageStatsTimezoneReader == nil {
		return nil, fmt.Errorf("management usage stats timezone reader is required")
	}
	timezone, found, err := s.usageStatsTimezoneReader.GetManagementUsageStatsTimezone(ctx)
	if err != nil {
		return nil, err
	}
	timezone = trimECMAScriptWhitespace(timezone)
	if !found || timezone == "" {
		return nil, fmt.Errorf("系统设置缺少 usageStatsTimezone")
	}
	location, err := timezonecompat.LoadNodeLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("系统设置 usageStatsTimezone 无效: %w", err)
	}
	return location, nil
}

func normalizeUsageRange(startDate string, endDate string, now time.Time, location *time.Location) UsageRange {
	todayText := now.In(location).Format(time.DateOnly)
	today, _ := time.Parse(time.DateOnly, todayText)
	earliestSupported := today.AddDate(0, 0, -(maxUsageRangeDays - 1))

	end, ok := parseDateKey(trimECMAScriptWhitespace(endDate))
	if !ok {
		end = today
	}
	end = clampDate(end, earliestSupported, today)

	start, ok := parseDateKey(trimECMAScriptWhitespace(startDate))
	if !ok {
		start = today
	}
	start = clampDate(start, earliestSupported, today)
	if start.After(end) {
		start = end
	}
	earliestStart := end.AddDate(0, 0, -(maxUsageRangeDays - 1))
	if start.Before(earliestStart) {
		start = earliestStart
	}

	return UsageRange{
		StartDate: start.Format(time.DateOnly),
		EndDate:   end.Format(time.DateOnly),
		Days:      calendarDaysInclusive(start, end),
		MaxDays:   maxUsageRangeDays,
	}
}

func normalizeLastUsedWindow(
	startDate string,
	endDate string,
	now time.Time,
	location *time.Location,
) (*time.Time, *time.Time) {
	if trimECMAScriptWhitespace(startDate) == "" && trimECMAScriptWhitespace(endDate) == "" {
		return nil, nil
	}
	rangeValue := normalizeUsageRange(startDate, endDate, now, location)
	start, _ := parseDateKey(rangeValue.StartDate)
	end, _ := parseDateKey(rangeValue.EndDate)
	startAt := zonedDateStart(start, location)
	endExclusiveAt := zonedDateStart(end.AddDate(0, 0, 1), location)
	return &startAt, &endExclusiveAt
}

func zonedDateStart(date time.Time, location *time.Location) time.Time {
	return time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, location)
}

func parseDateKey(value string) (time.Time, bool) {
	if len(value) != len(time.DateOnly) || value[4] != '-' || value[7] != '-' {
		return time.Time{}, false
	}
	for index, char := range value {
		if index == 4 || index == 7 {
			continue
		}
		if char < '0' || char > '9' {
			return time.Time{}, false
		}
	}
	parsed, err := time.Parse(time.DateOnly, value)
	return parsed, err == nil
}

func clampDate(value time.Time, earliest time.Time, latest time.Time) time.Time {
	if value.Before(earliest) {
		return earliest
	}
	if value.After(latest) {
		return latest
	}
	return value
}

func calendarDaysInclusive(start time.Time, end time.Time) int {
	return int(end.Sub(start).Hours()/24) + 1
}

func normalizePageSize(value int) int {
	if value <= 0 {
		return defaultListPageSize
	}
	return value
}

func normalizePage(value int, pageSize int) int {
	if value <= 0 {
		return 1
	}
	maxPage := max(1, maxListWindowRowCount/max(1, pageSize))
	return min(value, maxPage)
}

func normalizeStatus(value string) port.ManagementClientIPStatsStatus {
	if value == "" {
		return port.ManagementClientIPStatsStatusAll
	}
	return port.ManagementClientIPStatsStatus(value)
}

func normalizeSort(
	field string,
	order string,
) (port.ManagementClientIPStatsSortField, port.ManagementClientIPStatsSortOrder) {
	if field == "" {
		return port.ManagementClientIPStatsSortRequestCount, port.ManagementClientIPStatsSortDescending
	}
	if order == "asc" {
		return port.ManagementClientIPStatsSortField(field), port.ManagementClientIPStatsSortAscending
	}
	return port.ManagementClientIPStatsSortField(field), port.ManagementClientIPStatsSortDescending
}

func listItem(row port.ManagementClientIPStatsListRow) ListItem {
	return ListItem{
		IPHash:         row.IPHash,
		AggregateIPKey: row.AggregateIPKey,
		LastSeenAt:     stringPointer(row.LastSeenAt),
		Status:         listItemStatus(row.Status),
		RangeUsage:     usageSummary(row.RangeUsage),
	}
}

func usageSummary(row port.ManagementClientIPUsageSummary) UsageSummary {
	var errorRate float64
	if row.RequestCount > 0 {
		errorRate = float64(row.ErrorCount) / float64(row.RequestCount)
	}
	averageDurationMs := finiteFloat(row.AverageDurationMs)
	averageFirstTokenMs := finiteFloat(row.AverageFirstTokenMs)
	return UsageSummary{
		RequestCount:        row.RequestCount,
		SuccessCount:        row.SuccessCount,
		ErrorCount:          row.ErrorCount,
		ErrorRate:           errorRate,
		InputTokens:         row.InputTokens,
		OutputTokens:        row.OutputTokens,
		CacheReadTokens:     row.CacheReadTokens,
		CacheReadCost:       row.CacheReadCost,
		CacheWriteTokens:    row.CacheWriteTokens,
		CacheWrite1hTokens:  row.CacheWrite1hTokens,
		CacheWriteCost:      row.CacheWriteCost,
		ThinkingTokens:      row.ThinkingTokens,
		InputImageTokens:    row.InputImageTokens,
		OutputImageTokens:   row.OutputImageTokens,
		TotalTokens:         row.InputTokens + row.OutputTokens,
		TotalCost:           row.TotalCost,
		ActiveDays:          int(row.ActiveDays),
		AverageDurationMs:   averageDurationMs,
		AverageFirstTokenMs: averageFirstTokenMs,
		MaxDurationMs:       positiveInt64(row.MaxDurationMs),
		LastUsedAt:          cloneString(row.LastUsedAt),
		LastErrorAt:         cloneString(row.LastErrorAt),
	}
}

func listItemStatus(value port.ManagementClientIPStatsStatus) string {
	switch value {
	case port.ManagementClientIPStatsStatusBlacklisted:
		return "blacklisted"
	case port.ManagementClientIPStatsStatusAllowlisted:
		return "allowlisted"
	default:
		return "normal"
	}
}

func finiteFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	return finiteFloatValue(*value)
}

func finiteFloatValue(value float64) *float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	result := value
	return &result
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func stringPointer(value string) *string {
	result := value
	return &result
}

func positiveInt64(value *int64) *int64 {
	if value == nil || *value <= 0 {
		return nil
	}
	result := *value
	return &result
}

func trimECMAScriptWhitespace(value string) string {
	return strings.TrimFunc(value, func(character rune) bool {
		switch character {
		case '\u0009', '\u000B', '\u000C', '\u0020', '\u00A0', '\u1680',
			'\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005',
			'\u2006', '\u2007', '\u2008', '\u2009', '\u200A', '\u202F',
			'\u205F', '\u3000', '\uFEFF', '\u000A', '\u000D', '\u2028',
			'\u2029':
			return true
		default:
			return false
		}
	})
}
