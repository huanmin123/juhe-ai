package managementstats

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	usageWindowDays       = 31
	usageStatsTimezoneTTL = time.Minute
)

type Service struct {
	store              port.ManagementUsageStatsTimezoneReader
	systemMetricsStore port.ManagementSystemMetricsReader
	statsReader        port.ManagementStatsReader
	now                func() time.Time
	timezoneMu         sync.Mutex
	timezoneCache      cachedUsageStatsTimezone
	timezoneRefresh    *usageStatsTimezoneRefresh
}

type ServiceOptions struct {
	Store              port.ManagementUsageStatsTimezoneReader
	SystemMetricsStore port.ManagementSystemMetricsReader
	StatsReader        port.ManagementStatsReader
	Now                func() time.Time
}

type UsageWindow struct {
	Timezone  string `json:"timezone"`
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	Days      int    `json:"days"`
	MaxDays   int    `json:"maxDays"`
}

type cachedUsageStatsTimezone struct {
	name      string
	location  *time.Location
	expiresAt time.Time
}

type usageStatsTimezoneRefresh struct {
	done chan struct{}
}

func NewService(store port.ManagementUsageStatsTimezoneReader) *Service {
	options := ServiceOptions{Store: store}
	if systemMetricsStore, ok := store.(port.ManagementSystemMetricsReader); ok {
		options.SystemMetricsStore = systemMetricsStore
	}
	return NewServiceWithOptions(options)
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	statsReader := opts.StatsReader
	if statsReader == nil {
		statsReader, _ = opts.Store.(port.ManagementStatsReader)
	}
	return &Service{
		store:              opts.Store,
		systemMetricsStore: opts.SystemMetricsStore,
		statsReader:        statsReader,
		now:                now,
	}
}

type ReadScope struct {
	ActorSystemAccountID  string
	Admin                 bool
	TargetSystemAccountID string
}

type StatsRange struct {
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	Days      int    `json:"days"`
	MaxDays   int    `json:"maxDays"`
}

type UsageSummary struct {
	RequestCount       int64   `json:"requestCount"`
	InputTokens        int64   `json:"inputTokens"`
	OutputTokens       int64   `json:"outputTokens"`
	CacheReadTokens    int64   `json:"cacheReadTokens"`
	CacheReadCost      float64 `json:"cacheReadCost"`
	CacheWriteTokens   int64   `json:"cacheWriteTokens"`
	CacheWrite1hTokens int64   `json:"cacheWrite1hTokens"`
	CacheWriteCost     float64 `json:"cacheWriteCost"`
	ThinkingTokens     int64   `json:"thinkingTokens"`
	InputImageTokens   int64   `json:"inputImageTokens"`
	OutputImageTokens  int64   `json:"outputImageTokens"`
	TotalTokens        int64   `json:"totalTokens"`
	TotalCost          float64 `json:"totalCost"`
	LastUsedAt         *string `json:"lastUsedAt,omitempty"`
}

type AccountUsageInput struct {
	Page       int
	PageSize   int
	Keyword    string
	StartDate  string
	EndDate    string
	AccountIDs []string
}

type AccountUsageRow struct {
	ID                          string            `json:"id"`
	SystemAccountID             *string           `json:"systemAccountId,omitempty"`
	SystemAccountName           *string           `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID        string            `json:"ownerSystemAccountId"`
	OwnerSystemAccountName      *string           `json:"ownerSystemAccountName,omitempty"`
	ProviderCode                string            `json:"providerCode"`
	Name                        string            `json:"name"`
	Type                        string            `json:"type"`
	Status                      string            `json:"status"`
	AccessType                  string            `json:"accessType,omitempty"`
	RangeUsage                  UsageSummary      `json:"rangeUsage"`
	DailyUsage                  []DailyUsagePoint `json:"dailyUsage"`
	AuthorizationUsageAvailable bool              `json:"authorizationUsageAvailable"`
	AuthorizationCount          int               `json:"authorizationCount"`
	AuthorizationTeamCount      int               `json:"authorizationTeamCount"`
}

type AccountUsageOverview struct {
	Range                  StatsRange        `json:"range"`
	Summary                UsageSummary      `json:"summary"`
	Rows                   []AccountUsageRow `json:"rows"`
	DefaultTrendAccountIDs []string          `json:"defaultTrendAccountIds"`
	Total                  int               `json:"total"`
	HasMore                bool              `json:"hasMore"`
	Page                   int               `json:"page"`
	PageSize               int               `json:"pageSize"`
}

type DailyUsagePoint struct {
	StatDate string `json:"statDate"`
	UsageSummary
}

type AccountUsageTrendInput struct {
	StartDate  string
	EndDate    string
	AccountIDs []string
}

type AccountUsageTrendRow struct {
	ID                     string            `json:"id"`
	Name                   string            `json:"name"`
	ProviderCode           string            `json:"providerCode"`
	SystemAccountID        *string           `json:"systemAccountId,omitempty"`
	SystemAccountName      *string           `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID   string            `json:"ownerSystemAccountId"`
	OwnerSystemAccountName *string           `json:"ownerSystemAccountName,omitempty"`
	AccessType             string            `json:"accessType,omitempty"`
	DailyUsage             []DailyUsagePoint `json:"dailyUsage"`
}

type AccountUsageTrendOverview struct {
	Range StatsRange             `json:"range"`
	Rows  []AccountUsageTrendRow `json:"rows"`
}

type AIPerformanceInput struct {
	StartDate  string
	EndDate    string
	AccountIDs []string
}

type AIPerformanceAccountsInput struct {
	Keyword    string
	AccountIDs []string
	Limit      int
}

type AIPerformanceAccount struct {
	ID                     string  `json:"id"`
	Name                   string  `json:"name"`
	Status                 string  `json:"status"`
	ProviderCode           string  `json:"providerCode"`
	SystemAccountID        string  `json:"systemAccountId"`
	SystemAccountName      *string `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID   *string `json:"ownerSystemAccountId,omitempty"`
	OwnerSystemAccountName *string `json:"ownerSystemAccountName,omitempty"`
	AccessType             string  `json:"accessType,omitempty"`
	RequestCountLast7d     int64   `json:"requestCountLast7d"`
	Selected               bool    `json:"selected"`
	DefaultVisible         bool    `json:"defaultVisible"`
}

type AIPerformanceAccountOption struct {
	ID                     string  `json:"id"`
	Name                   string  `json:"name"`
	Status                 string  `json:"status"`
	ProviderCode           string  `json:"providerCode"`
	SystemAccountID        string  `json:"systemAccountId"`
	SystemAccountName      *string `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID   *string `json:"ownerSystemAccountId,omitempty"`
	OwnerSystemAccountName *string `json:"ownerSystemAccountName,omitempty"`
	AccessType             string  `json:"accessType,omitempty"`
	RequestCountLast7d     int64   `json:"requestCountLast7d"`
}

type AIPerformancePoint struct {
	StatHour            string `json:"statHour"`
	RequestCount        int64  `json:"requestCount"`
	AverageFirstTokenMS *int64 `json:"averageFirstTokenMs,omitempty"`
	MaxFirstTokenMS     *int64 `json:"maxFirstTokenMs,omitempty"`
	AverageDurationMS   *int64 `json:"averageDurationMs,omitempty"`
	MaxDurationMS       *int64 `json:"maxDurationMs,omitempty"`
}

type AIPerformanceAccountSeries struct {
	AccountID       string               `json:"accountId"`
	AccountName     string               `json:"accountName"`
	ProviderCode    string               `json:"providerCode"`
	SystemAccountID string               `json:"systemAccountId"`
	Points          []AIPerformancePoint `json:"points"`
}

type AIPerformanceSummary struct {
	RequestCount        int64  `json:"requestCount"`
	AverageFirstTokenMS *int64 `json:"averageFirstTokenMs,omitempty"`
	MaxFirstTokenMS     *int64 `json:"maxFirstTokenMs,omitempty"`
	AverageDurationMS   *int64 `json:"averageDurationMs,omitempty"`
	MaxDurationMS       *int64 `json:"maxDurationMs,omitempty"`
}

type AIPerformanceOverview struct {
	Range            StatsRange                   `json:"range"`
	DefaultAccounts  []AIPerformanceAccount       `json:"defaultAccounts"`
	SelectedAccounts []AIPerformanceAccount       `json:"selectedAccounts"`
	Accounts         []AIPerformanceAccount       `json:"accounts"`
	HourlySeries     []AIPerformanceAccountSeries `json:"hourlySeries"`
	Summary          AIPerformanceSummary         `json:"summary"`
}

func (s *Service) AccountUsage(ctx context.Context, readScope ReadScope, input AccountUsageInput) (AccountUsageOverview, error) {
	if s.statsReader == nil {
		return AccountUsageOverview{}, fmt.Errorf("management stats reader is required")
	}
	defaultRange := rangeDefaultToday
	if strings.TrimSpace(input.StartDate) == "" && strings.TrimSpace(input.EndDate) == "" {
		defaultRange = rangeDefaultLast31Days
	}
	rangeValue, err := s.normalizeRange(ctx, input.StartDate, input.EndDate, defaultRange)
	if err != nil {
		return AccountUsageOverview{}, err
	}
	pageSize := clampDefault(input.PageSize, 10, 1, 200)
	maxPage := max(1, 1000/pageSize)
	page := clampDefault(input.Page, 1, 1, maxPage)
	scope, err := managementStatsScope(readScope)
	if err != nil {
		return AccountUsageOverview{}, err
	}
	result, err := s.statsReader.ReadManagementAccountUsage(ctx, port.ManagementAccountUsageReadInput{
		Scope: scope, Range: port.ManagementStatsRange{StartDate: rangeValue.StartDate, EndDate: rangeValue.EndDate},
		Page: page, PageSize: pageSize, Keyword: strings.TrimSpace(input.Keyword), AccountIDs: uniqueStrings(input.AccountIDs, 50),
	})
	if err != nil {
		return AccountUsageOverview{}, err
	}
	rows := make([]AccountUsageRow, 0, len(result.Rows))
	for _, row := range result.Rows {
		rows = append(rows, accountUsageRow(row, scope.IncludeSystemAccountFields))
	}
	defaultIDs := uniqueStrings(result.DefaultTrendAccountIDs, 10)
	if len(defaultIDs) == 0 && scope.ScopeType == "account" {
		for _, row := range rows {
			if row.RangeUsage.RequestCount > 0 || row.RangeUsage.TotalTokens > 0 || row.RangeUsage.TotalCost > 0 {
				defaultIDs = append(defaultIDs, row.ID)
				if len(defaultIDs) == 10 {
					break
				}
			}
		}
	}
	total := (page-1)*pageSize + result.PageRowCount
	if result.HasMore {
		total++
	}
	if shown := (page-1)*pageSize + len(rows); shown > total {
		total = shown
	}
	return AccountUsageOverview{Range: rangeValue, Summary: usageSummary(result.Summary), Rows: rows, DefaultTrendAccountIDs: defaultIDs, Total: total, HasMore: result.HasMore, Page: page, PageSize: pageSize}, nil
}

func (s *Service) AccountUsageTrend(ctx context.Context, readScope ReadScope, input AccountUsageTrendInput) (AccountUsageTrendOverview, error) {
	if s.statsReader == nil {
		return AccountUsageTrendOverview{}, fmt.Errorf("management stats reader is required")
	}
	rangeValue, err := s.normalizeRange(ctx, input.StartDate, input.EndDate, rangeDefaultToday)
	if err != nil {
		return AccountUsageTrendOverview{}, err
	}
	scope, err := managementStatsScope(readScope)
	if err != nil {
		return AccountUsageTrendOverview{}, err
	}
	result, err := s.statsReader.ReadManagementAccountUsageTrend(ctx, port.ManagementAccountUsageTrendReadInput{Scope: scope, Range: port.ManagementStatsRange{StartDate: rangeValue.StartDate, EndDate: rangeValue.EndDate}, AccountIDs: uniqueStrings(input.AccountIDs, 10)})
	if err != nil {
		return AccountUsageTrendOverview{}, err
	}
	daily := make(map[string]map[string]UsageSummary)
	for _, row := range result.DailyRows {
		if daily[row.AccountID] == nil {
			daily[row.AccountID] = make(map[string]UsageSummary)
		}
		daily[row.AccountID][row.StatDate] = usageSummary(row.Usage)
	}
	dates := dateBuckets(rangeValue)
	rows := make([]AccountUsageTrendRow, 0, len(result.Accounts))
	for _, account := range result.Accounts {
		points := make([]DailyUsagePoint, 0, len(dates))
		for _, date := range dates {
			points = append(points, DailyUsagePoint{StatDate: date, UsageSummary: daily[account.ID][date]})
		}
		row := AccountUsageTrendRow{ID: account.ID, Name: account.Name, ProviderCode: account.ProviderCode, OwnerSystemAccountID: account.OwnerSystemAccountID, OwnerSystemAccountName: optionalString(account.OwnerSystemAccountName), AccessType: account.AccessType, DailyUsage: points}
		if scope.IncludeSystemAccountFields {
			row.SystemAccountID = optionalString(account.SystemAccountID)
			row.SystemAccountName = optionalString(account.SystemAccountName)
		}
		rows = append(rows, row)
	}
	return AccountUsageTrendOverview{Range: rangeValue, Rows: rows}, nil
}

func (s *Service) AIPerformance(ctx context.Context, readScope ReadScope, input AIPerformanceInput) (AIPerformanceOverview, error) {
	if s.statsReader == nil {
		return AIPerformanceOverview{}, fmt.Errorf("management stats reader is required")
	}
	rangeValue, err := s.normalizeAIPerformanceRange(ctx, input.StartDate, input.EndDate)
	if err != nil {
		return AIPerformanceOverview{}, err
	}
	scope, err := managementStatsScope(readScope)
	if err != nil {
		return AIPerformanceOverview{}, err
	}
	result, err := s.statsReader.ReadManagementAIPerformance(ctx, port.ManagementAIPerformanceReadInput{Scope: scope, Range: port.ManagementStatsRange{StartDate: rangeValue.StartDate, EndDate: rangeValue.EndDate}, AccountIDs: uniqueStrings(input.AccountIDs, 20)})
	if err != nil {
		return AIPerformanceOverview{}, err
	}
	defaultIDs := stringSetFromAccounts(result.DefaultAccounts)
	selectedIDs := stringSetFromAccounts(result.SelectedAccounts)
	accounts := mergePerformanceAccounts(result.DefaultAccounts, result.SelectedAccounts, defaultIDs, selectedIDs)
	defaultAccounts := filterPerformanceAccounts(accounts, func(account AIPerformanceAccount) bool { return account.DefaultVisible })
	selectedAccounts := filterPerformanceAccounts(accounts, func(account AIPerformanceAccount) bool { return account.Selected })
	hourly := make(map[string]port.ManagementAIPerformanceHourlyRow, len(result.HourlyRows))
	for _, row := range result.HourlyRows {
		hourly[row.AccountID+"\n"+row.StatHour] = row
	}
	hours := hourBuckets(rangeValue)
	series := make([]AIPerformanceAccountSeries, 0, len(accounts))
	for _, account := range accounts {
		points := make([]AIPerformancePoint, 0, len(hours))
		for _, hour := range hours {
			row := hourly[account.ID+"\n"+hour]
			points = append(points, performancePoint(hour, row))
		}
		series = append(series, AIPerformanceAccountSeries{AccountID: account.ID, AccountName: account.Name, ProviderCode: account.ProviderCode, SystemAccountID: account.SystemAccountID, Points: points})
	}
	return AIPerformanceOverview{Range: rangeValue, DefaultAccounts: defaultAccounts, SelectedAccounts: selectedAccounts, Accounts: accounts, HourlySeries: series, Summary: performanceSummary(result.Summary)}, nil
}

func (s *Service) AIPerformanceAccounts(ctx context.Context, readScope ReadScope, input AIPerformanceAccountsInput) ([]AIPerformanceAccountOption, error) {
	if s.statsReader == nil {
		return nil, fmt.Errorf("management stats reader is required")
	}
	scope, err := managementStatsScope(readScope)
	if err != nil {
		return nil, err
	}
	limit := clampDefault(input.Limit, 50, 1, 50)
	rows, err := s.statsReader.ReadManagementAIPerformanceAccounts(ctx, port.ManagementAIPerformanceAccountsReadInput{Scope: scope, Keyword: strings.TrimSpace(input.Keyword), AccountIDs: uniqueStrings(input.AccountIDs, 20), Limit: limit})
	if err != nil {
		return nil, err
	}
	result := make([]AIPerformanceAccountOption, 0, len(rows))
	for _, row := range rows {
		result = append(result, AIPerformanceAccountOption{ID: row.ID, Name: row.Name, Status: row.Status, ProviderCode: row.ProviderCode, SystemAccountID: row.SystemAccountID, SystemAccountName: optionalString(row.SystemAccountName), OwnerSystemAccountID: optionalString(row.OwnerSystemAccountID), OwnerSystemAccountName: optionalString(row.OwnerSystemAccountName), AccessType: row.AccessType, RequestCountLast7d: row.RequestCountLast7d})
	}
	return result, nil
}

type rangeDefault int

const (
	rangeDefaultToday rangeDefault = iota
	rangeDefaultLast31Days
)

func (s *Service) normalizeAIPerformanceRange(ctx context.Context, startDate, endDate string) (StatsRange, error) {
	if strings.TrimSpace(startDate) == "" && strings.TrimSpace(endDate) == "" {
		return s.normalizeRange(ctx, "", "", rangeDefaultLast31Days)
	}
	if strings.TrimSpace(startDate) == "" {
		startDate = endDate
	}
	if strings.TrimSpace(endDate) == "" {
		endDate = startDate
	}
	return s.normalizeRange(ctx, startDate, endDate, rangeDefaultToday)
}

func (s *Service) normalizeRange(ctx context.Context, startText, endText string, defaultValue rangeDefault) (StatsRange, error) {
	if s.store == nil {
		return StatsRange{}, fmt.Errorf("management usage stats timezone store is required")
	}
	now := s.now()
	_, location, err := s.usageStatsTimezone(ctx, now)
	if err != nil {
		return StatsRange{}, err
	}
	now = s.now()
	year, month, day := now.In(location).Date()
	today := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	earliest := today.AddDate(0, 0, -(usageWindowDays - 1))
	defaultStart := today
	if defaultValue == rangeDefaultLast31Days {
		defaultStart = earliest
	}
	end := parseDateOrDefault(endText, today)
	end = clampDate(end, earliest, today)
	start := parseDateOrDefault(startText, defaultStart)
	start = clampDate(start, earliest, today)
	if start.After(end) {
		start = end
	}
	earliestStart := end.AddDate(0, 0, -(usageWindowDays - 1))
	if start.Before(earliestStart) {
		start = earliestStart
	}
	days := int(end.Sub(start).Hours()/24) + 1
	return StatsRange{StartDate: start.Format(time.DateOnly), EndDate: end.Format(time.DateOnly), Days: days, MaxDays: usageWindowDays}, nil
}

func managementStatsScope(input ReadScope) (port.ManagementStatsScope, error) {
	actor := strings.TrimSpace(input.ActorSystemAccountID)
	if actor == "" {
		return port.ManagementStatsScope{}, fmt.Errorf("missing management stats actor")
	}
	target := strings.TrimSpace(input.TargetSystemAccountID)
	if input.Admin && target == "all" {
		target = ""
	}
	if input.Admin && target == "" {
		return port.ManagementStatsScope{SystemAccountID: "global", ScopeType: "account", ViewerSystemAccountID: actor, IncludeSystemAccountFields: true}, nil
	}
	if target == "" || !input.Admin {
		target = actor
	}
	return port.ManagementStatsScope{SystemAccountID: target, ScopeType: "caller_account", ViewerSystemAccountID: target, IncludeSystemAccountFields: input.Admin}, nil
}

func accountUsageRow(row port.ManagementAccountUsageRow, includeSystemAccount bool) AccountUsageRow {
	result := AccountUsageRow{ID: row.Account.ID, OwnerSystemAccountID: row.Account.OwnerSystemAccountID, OwnerSystemAccountName: optionalString(row.Account.OwnerSystemAccountName), ProviderCode: row.Account.ProviderCode, Name: row.Account.Name, Type: row.Account.Type, Status: row.Account.Status, AccessType: row.Account.AccessType, RangeUsage: usageSummary(row.Usage), DailyUsage: []DailyUsagePoint{}}
	if includeSystemAccount {
		result.SystemAccountID = optionalString(row.Account.SystemAccountID)
		result.SystemAccountName = optionalString(row.Account.SystemAccountName)
	}
	return result
}

func usageSummary(row port.ManagementUsageAggregate) UsageSummary {
	return UsageSummary{RequestCount: row.RequestCount, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, CacheReadTokens: row.CacheReadTokens, CacheReadCost: row.CacheReadCostUSD, CacheWriteTokens: row.CacheWriteTokens, CacheWrite1hTokens: row.CacheWrite1hTokens, CacheWriteCost: row.CacheWriteCostUSD, ThinkingTokens: row.ThinkingTokens, InputImageTokens: row.InputImageTokens, OutputImageTokens: row.OutputImageTokens, TotalTokens: row.InputTokens + row.OutputTokens, TotalCost: row.TotalCostUSD, LastUsedAt: row.LastUsedAt}
}

func mergePerformanceAccounts(defaultRows, selectedRows []port.ManagementStatsAccount, defaultIDs, selectedIDs map[string]struct{}) []AIPerformanceAccount {
	seen := map[string]struct{}{}
	result := make([]AIPerformanceAccount, 0, len(defaultRows)+len(selectedRows))
	for _, row := range append(append([]port.ManagementStatsAccount{}, defaultRows...), selectedRows...) {
		if _, ok := seen[row.ID]; ok {
			continue
		}
		seen[row.ID] = struct{}{}
		_, isDefault := defaultIDs[row.ID]
		_, isSelected := selectedIDs[row.ID]
		result = append(result, performanceAccount(row, isDefault, isSelected))
	}
	return result
}

func performanceAccount(row port.ManagementStatsAccount, defaultVisible, selected bool) AIPerformanceAccount {
	return AIPerformanceAccount{ID: row.ID, Name: row.Name, Status: row.Status, ProviderCode: row.ProviderCode, SystemAccountID: row.SystemAccountID, SystemAccountName: optionalString(row.SystemAccountName), OwnerSystemAccountID: optionalString(row.OwnerSystemAccountID), OwnerSystemAccountName: optionalString(row.OwnerSystemAccountName), AccessType: row.AccessType, RequestCountLast7d: row.RequestCountLast7d, Selected: selected, DefaultVisible: defaultVisible}
}

func performancePoint(hour string, row port.ManagementAIPerformanceHourlyRow) AIPerformancePoint {
	return AIPerformancePoint{StatHour: hour, RequestCount: row.RequestCount, AverageFirstTokenMS: roundedAverage(row.FirstTokenMSSum, row.FirstTokenMSCount), MaxFirstTokenMS: countedMax(row.FirstTokenMSMax, row.FirstTokenMSCount), AverageDurationMS: roundedAverage(row.DurationMSSum, row.DurationMSCount), MaxDurationMS: countedMax(row.DurationMSMax, row.DurationMSCount)}
}

func performanceSummary(row port.ManagementAIPerformanceAggregate) AIPerformanceSummary {
	return AIPerformanceSummary{RequestCount: row.RequestCount, AverageFirstTokenMS: roundedAverage(row.FirstTokenMSSum, row.FirstTokenMSCount), MaxFirstTokenMS: countedMax(row.FirstTokenMSMax, row.FirstTokenMSCount), AverageDurationMS: roundedAverage(row.DurationMSSum, row.DurationMSCount), MaxDurationMS: countedMax(row.DurationMSMax, row.DurationMSCount)}
}

func roundedAverage(sum, count int64) *int64 {
	if count <= 0 {
		return nil
	}
	value := int64(math.Round(float64(sum) / float64(count)))
	return &value
}

func countedMax(value, count int64) *int64 {
	if count <= 0 {
		return nil
	}
	value = max(0, value)
	return &value
}

func hourBuckets(statsRange StatsRange) []string {
	dates := dateBuckets(statsRange)
	result := make([]string, 0, statsRange.Days*24)
	for _, date := range dates {
		for hour := 0; hour < 24; hour++ {
			result = append(result, fmt.Sprintf("%sT%02d", date, hour))
		}
	}
	return result
}

func dateBuckets(statsRange StatsRange) []string {
	start, _ := time.Parse(time.DateOnly, statsRange.StartDate)
	end, _ := time.Parse(time.DateOnly, statsRange.EndDate)
	result := make([]string, 0, statsRange.Days)
	for date := start; !date.After(end); date = date.AddDate(0, 0, 1) {
		result = append(result, date.Format(time.DateOnly))
	}
	return result
}

func stringSetFromAccounts(rows []port.ManagementStatsAccount) map[string]struct{} {
	result := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		result[row.ID] = struct{}{}
	}
	return result
}

func filterPerformanceAccounts(rows []AIPerformanceAccount, keep func(AIPerformanceAccount) bool) []AIPerformanceAccount {
	result := make([]AIPerformanceAccount, 0, len(rows))
	for _, row := range rows {
		if keep(row) {
			result = append(result, row)
		}
	}
	return result
}

func uniqueStrings(values []string, limit int) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, min(len(values), limit))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result
}

func parseDateOrDefault(value string, fallback time.Time) time.Time {
	parsed, err := time.Parse(time.DateOnly, strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

func clampDate(value, low, high time.Time) time.Time {
	if value.Before(low) {
		return low
	}
	if value.After(high) {
		return high
	}
	return value
}

func clampDefault(value, defaultValue, low, high int) int {
	if value == 0 {
		value = defaultValue
	}
	return min(high, max(low, value))
}

func optionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func (s *Service) UsageWindow(ctx context.Context) (UsageWindow, error) {
	if s.store == nil {
		return UsageWindow{}, fmt.Errorf("management usage stats timezone store is required")
	}
	cacheNow := s.now()
	timezone, location, err := s.usageStatsTimezone(ctx, cacheNow)
	if err != nil {
		return UsageWindow{}, err
	}
	now := s.now()
	year, month, day := now.In(location).Date()
	endDate := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	startDate := endDate.AddDate(0, 0, -(usageWindowDays - 1))
	return UsageWindow{
		Timezone:  timezone,
		StartDate: startDate.Format(time.DateOnly),
		EndDate:   endDate.Format(time.DateOnly),
		Days:      usageWindowDays,
		MaxDays:   usageWindowDays,
	}, nil
}

func (s *Service) usageStatsTimezone(ctx context.Context, now time.Time) (string, *time.Location, error) {
	for {
		s.timezoneMu.Lock()
		if s.timezoneCache.location != nil && now.Before(s.timezoneCache.expiresAt) {
			timezone := s.timezoneCache.name
			location := s.timezoneCache.location
			s.timezoneMu.Unlock()
			return timezone, location, nil
		}
		if refresh := s.timezoneRefresh; refresh != nil {
			done := refresh.done
			s.timezoneMu.Unlock()
			select {
			case <-ctx.Done():
				return "", nil, ctx.Err()
			case <-done:
				continue
			}
		}
		refresh := &usageStatsTimezoneRefresh{done: make(chan struct{})}
		s.timezoneRefresh = refresh
		s.timezoneMu.Unlock()

		timezone, location, err := s.readUsageStatsTimezone(ctx, now)

		s.timezoneMu.Lock()
		if s.timezoneRefresh == refresh {
			s.timezoneRefresh = nil
		}
		close(refresh.done)
		s.timezoneMu.Unlock()
		return timezone, location, err
	}
}

func (s *Service) readUsageStatsTimezone(ctx context.Context, now time.Time) (string, *time.Location, error) {
	timezone, found, err := s.store.GetManagementUsageStatsTimezone(ctx)
	if err != nil {
		return "", nil, err
	}
	timezone = strings.TrimSpace(timezone)
	if !found || timezone == "" {
		return "", nil, fmt.Errorf("系统设置缺少 usageStatsTimezone")
	}
	location, err := loadUsageStatsLocation(timezone)
	if err != nil {
		return "", nil, fmt.Errorf("系统设置 usageStatsTimezone 无效: %w", err)
	}
	s.timezoneMu.Lock()
	s.timezoneCache = cachedUsageStatsTimezone{
		name:      timezone,
		location:  location,
		expiresAt: now.Add(usageStatsTimezoneTTL),
	}
	s.timezoneMu.Unlock()
	return timezone, location, nil
}
