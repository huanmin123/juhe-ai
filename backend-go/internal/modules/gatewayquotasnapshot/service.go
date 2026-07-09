package gatewayquotasnapshot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	GatewayQuotaSnapshotCostPageSize          = 5000
	GatewayQuotaSnapshotAuthorizationPageSize = 5000
	defaultUsageStatsTimezone                 = "UTC"
)

type Service struct {
	store         port.GatewayQuotaSnapshotReader
	timezoneStore port.ManagementUsageStatsTimezoneReader
	now           func() time.Time
}

type ServiceOptions struct {
	Store         port.GatewayQuotaSnapshotReader
	TimezoneStore port.ManagementUsageStatsTimezoneReader
	Now           func() time.Time
}

type BuildInput struct {
	Now      time.Time
	Timezone string
}

type Snapshot struct {
	GeneratedAt                  string                       `json:"generatedAt"`
	CostEntries                  []CostSnapshotEntry          `json:"costEntries"`
	AuthorizationEntries         []AuthorizationSnapshotEntry `json:"authorizationEntries"`
	CostEntriesComplete          bool                         `json:"costEntriesComplete"`
	AuthorizationEntriesComplete bool                         `json:"authorizationEntriesComplete"`
	Timezone                     string                       `json:"timezone"`
	StatDate                     string                       `json:"statDate"`
	StatWeek                     string                       `json:"statWeek"`
	StatMonth                    string                       `json:"statMonth"`
}

type CostSnapshotEntry struct {
	SystemAccountID   string                 `json:"systemAccountId"`
	ScopeType         string                 `json:"scopeType"`
	ScopeID           string                 `json:"scopeId"`
	HourlyWindowHours int                    `json:"hourlyWindowHours,omitempty"`
	Costs             port.GatewayQuotaCosts `json:"costs"`
}

type AuthorizationSnapshotEntry struct {
	ScopeType       string               `json:"scopeType"`
	AuthorizationID string               `json:"authorizationId"`
	Decision        GatewayQuotaDecision `json:"decision"`
}

type GatewayQuotaDecision struct {
	Allowed bool   `json:"allowed"`
	Message string `json:"message,omitempty"`
}

type quotaCostCheck struct {
	key       string
	limits    port.ManagementRequestQuotaLimits
	costInput port.GatewayQuotaCostLookupInput
}

func NewService(store port.GatewayQuotaSnapshotReader) *Service {
	return NewServiceWithOptions(ServiceOptions{Store: store})
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	timezoneStore := opts.TimezoneStore
	if timezoneStore == nil {
		if candidate, ok := opts.Store.(port.ManagementUsageStatsTimezoneReader); ok {
			timezoneStore = candidate
		}
	}
	return &Service{
		store:         opts.Store,
		timezoneStore: timezoneStore,
		now:           now,
	}
}

func (s *Service) Build(ctx context.Context, input BuildInput) (Snapshot, error) {
	if s.store == nil {
		return Snapshot{}, fmt.Errorf("gateway quota snapshot store is required")
	}
	now := input.Now
	if now.IsZero() {
		now = s.now()
	}
	timezone, location, err := s.usageStatsTimezone(ctx, input.Timezone)
	if err != nil {
		return Snapshot{}, err
	}
	statDate := dateKey(now, location)
	statWeek := weekKey(now, location)
	statMonth := monthKey(now, location)

	apiKeyWindow, err := s.store.ListGatewayQuotaSnapshotAPIKeys(ctx, GatewayQuotaSnapshotCostPageSize)
	if err != nil {
		return Snapshot{}, err
	}
	authorizationWindow, err := s.store.ListGatewayQuotaSnapshotAuthorizations(ctx, GatewayQuotaSnapshotAuthorizationPageSize)
	if err != nil {
		return Snapshot{}, err
	}
	teamAuthorizationWindow, err := s.store.ListGatewayQuotaSnapshotTeamAuthorizations(ctx, GatewayQuotaSnapshotAuthorizationPageSize)
	if err != nil {
		return Snapshot{}, err
	}

	apiKeyChecks := make([]quotaCostCheck, 0, len(apiKeyWindow.Rows))
	for _, row := range apiKeyWindow.Rows {
		check, ok := apiKeyQuotaCostCheck(row, statDate, statWeek, statMonth)
		if ok {
			apiKeyChecks = append(apiKeyChecks, check)
		}
	}

	authorizationChecksByID := map[string][]quotaCostCheck{}
	authorizationChecks := []quotaCostCheck{}
	for _, row := range authorizationWindow.Rows {
		scopeType := authorizationScopeType(row.ResourceType)
		check, ok := authorizationQuotaCostCheck(row, scopeType, statDate, statWeek, statMonth)
		if ok {
			authorizationChecksByID[row.ID] = append(authorizationChecksByID[row.ID], check)
			authorizationChecks = append(authorizationChecks, check)
		}
	}
	for _, row := range teamAuthorizationWindow.Rows {
		scopeType := authorizationScopeType(row.ResourceType)
		check, ok := teamAuthorizationQuotaCostCheck(row, scopeType, statDate, statWeek, statMonth)
		if ok {
			authorizationChecksByID[row.AuthorizationID] = append(authorizationChecksByID[row.AuthorizationID], check)
			authorizationChecks = append(authorizationChecks, check)
		}
	}

	allChecks := uniqueQuotaCostChecks(append(apiKeyChecks, authorizationChecks...))
	costInputs := make([]port.GatewayQuotaCostLookupInput, 0, len(allChecks))
	for _, check := range allChecks {
		costInputs = append(costInputs, check.costInput)
	}
	costsByKey, err := s.store.LoadGatewayQuotaSnapshotCosts(ctx, costInputs)
	if err != nil {
		return Snapshot{}, err
	}

	costEntries := make([]CostSnapshotEntry, 0, len(apiKeyChecks))
	for _, check := range apiKeyChecks {
		costEntries = append(costEntries, CostSnapshotEntry{
			SystemAccountID:   check.costInput.SystemAccountID,
			ScopeType:         check.costInput.ScopeType,
			ScopeID:           check.costInput.ScopeID,
			HourlyWindowHours: check.costInput.HourlyWindowHours,
			Costs:             costsByKey[check.costInput.Key],
		})
	}

	authorizationEntries := make([]AuthorizationSnapshotEntry, 0, len(authorizationWindow.Rows))
	for _, row := range authorizationWindow.Rows {
		checks := authorizationChecksByID[row.ID]
		if len(checks) == 0 {
			continue
		}
		allowed := true
		for _, check := range checks {
			if isRequestQuotaExceeded(check.limits, costsByKey[check.costInput.Key]) {
				allowed = false
				break
			}
		}
		decision := GatewayQuotaDecision{Allowed: allowed}
		if !allowed {
			decision.Message = "额度已用完，请联系管理员提升额度"
		}
		authorizationEntries = append(authorizationEntries, AuthorizationSnapshotEntry{
			ScopeType:       authorizationScopeType(row.ResourceType),
			AuthorizationID: row.ID,
			Decision:        decision,
		})
	}

	return Snapshot{
		GeneratedAt:                  formatGeneratedAt(now),
		CostEntries:                  costEntries,
		AuthorizationEntries:         authorizationEntries,
		CostEntriesComplete:          apiKeyWindow.Complete,
		AuthorizationEntriesComplete: authorizationWindow.Complete && teamAuthorizationWindow.Complete,
		Timezone:                     timezone,
		StatDate:                     statDate,
		StatWeek:                     statWeek,
		StatMonth:                    statMonth,
	}, nil
}

func (s *Service) usageStatsTimezone(ctx context.Context, input string) (string, *time.Location, error) {
	timezone := strings.TrimSpace(input)
	if timezone == "" && s.timezoneStore != nil {
		value, found, err := s.timezoneStore.GetManagementUsageStatsTimezone(ctx)
		if err != nil {
			return "", nil, err
		}
		if found {
			timezone = strings.TrimSpace(value)
		}
	}
	if timezone == "" {
		timezone = defaultUsageStatsTimezone
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return "", nil, fmt.Errorf("系统设置 usageStatsTimezone 无效: %w", err)
	}
	return timezone, location, nil
}

func apiKeyQuotaCostCheck(row port.GatewayQuotaSnapshotAPIKeyRow, statDate string, statWeek string, statMonth string) (quotaCostCheck, bool) {
	if !hasEnabledRequestQuotaLimit(row.Limits) {
		return quotaCostCheck{}, false
	}
	hours := 0
	if row.Limits.Hourly != nil {
		hours = normalizeHourlyWindowHours(row.Limits.Hourly.Hours)
	}
	input := port.GatewayQuotaCostLookupInput{
		SystemAccountID:   strings.TrimSpace(row.SystemAccountID),
		ScopeType:         "api_key",
		ScopeID:           strings.TrimSpace(row.ID),
		StatDate:          statDate,
		StatWeek:          statWeek,
		StatMonth:         statMonth,
		HourlyWindowHours: hours,
	}
	input.Key = requestQuotaCostKey(input)
	return quotaCostCheck{key: input.Key, limits: row.Limits, costInput: input}, true
}

func authorizationQuotaCostCheck(row port.GatewayQuotaSnapshotAuthorizationRow, scopeType string, statDate string, statWeek string, statMonth string) (quotaCostCheck, bool) {
	if !hasEnabledRequestQuotaLimit(row.Limits) {
		return quotaCostCheck{}, false
	}
	hours := 0
	if row.Limits.Hourly != nil {
		hours = normalizeHourlyWindowHours(row.Limits.Hourly.Hours)
	}
	systemAccountID := authorizationQuotaStatsSystemAccountID(row, scopeType)
	input := port.GatewayQuotaCostLookupInput{
		SystemAccountID:   systemAccountID,
		ScopeType:         scopeType,
		ScopeID:           strings.TrimSpace(row.ID),
		StatDate:          statDate,
		StatWeek:          statWeek,
		StatMonth:         statMonth,
		HourlyWindowHours: hours,
	}
	input.Key = requestQuotaCostKey(input)
	return quotaCostCheck{key: input.Key, limits: row.Limits, costInput: input}, true
}

func teamAuthorizationQuotaCostCheck(row port.GatewayQuotaSnapshotTeamAuthorizationRow, scopeType string, statDate string, statWeek string, statMonth string) (quotaCostCheck, bool) {
	if !hasEnabledRequestQuotaLimit(row.Limits) {
		return quotaCostCheck{}, false
	}
	resourceID := teamAuthorizationResourceID(row, scopeType)
	if resourceID == "" || strings.TrimSpace(row.EffectiveSourceTeamID) == "" {
		return quotaCostCheck{}, false
	}
	hours := 0
	if row.Limits.Hourly != nil {
		hours = normalizeHourlyWindowHours(row.Limits.Hourly.Hours)
	}
	snapshotScopeType := "account_authorization_team"
	if scopeType == "group_authorization" {
		snapshotScopeType = "group_authorization_team"
	}
	input := port.GatewayQuotaCostLookupInput{
		SystemAccountID:   teamAuthorizationQuotaStatsSystemAccountID(row, scopeType),
		ScopeType:         snapshotScopeType,
		ScopeID:           resourceID + ":" + strings.TrimSpace(row.EffectiveSourceTeamID),
		StatDate:          statDate,
		StatWeek:          statWeek,
		StatMonth:         statMonth,
		HourlyWindowHours: hours,
	}
	input.Key = requestQuotaCostKey(input)
	return quotaCostCheck{key: input.Key, limits: row.Limits, costInput: input}, true
}

func authorizationScopeType(resourceType string) string {
	if strings.TrimSpace(resourceType) == "account" {
		return "account_authorization"
	}
	return "group_authorization"
}

func authorizationQuotaStatsSystemAccountID(row port.GatewayQuotaSnapshotAuthorizationRow, scopeType string) string {
	if scopeType == "account_authorization" {
		return strings.TrimSpace(row.GranteeSystemAccountID)
	}
	return strings.TrimSpace(row.ResourceOwnerSystemAccountID)
}

func teamAuthorizationQuotaStatsSystemAccountID(row port.GatewayQuotaSnapshotTeamAuthorizationRow, scopeType string) string {
	if scopeType == "account_authorization" {
		if granteeID := strings.TrimSpace(row.AuthorizationGranteeSystemAccountID); granteeID != "" {
			return granteeID
		}
	}
	return strings.TrimSpace(row.ResourceOwnerSystemAccountID)
}

func teamAuthorizationResourceID(row port.GatewayQuotaSnapshotTeamAuthorizationRow, scopeType string) string {
	if scopeType == "account_authorization" {
		return strings.TrimSpace(row.AuthorizationInstanceAccountID)
	}
	return strings.TrimSpace(row.ResourceID)
}

func hasEnabledRequestQuotaLimit(value port.ManagementRequestQuotaLimits) bool {
	return (value.Hourly != nil && value.Hourly.Enabled) ||
		(value.Daily != nil && value.Daily.Enabled) ||
		(value.Weekly != nil && value.Weekly.Enabled) ||
		(value.Monthly != nil && value.Monthly.Enabled) ||
		(value.Total != nil && value.Total.Enabled)
}

func isRequestQuotaExceeded(limits port.ManagementRequestQuotaLimits, costs port.GatewayQuotaCosts) bool {
	return (limits.Hourly != nil && limits.Hourly.Enabled && costs.Hourly >= limits.Hourly.Limit) ||
		(limits.Daily != nil && limits.Daily.Enabled && costs.Daily >= limits.Daily.Limit) ||
		(limits.Weekly != nil && limits.Weekly.Enabled && costs.Weekly >= limits.Weekly.Limit) ||
		(limits.Monthly != nil && limits.Monthly.Enabled && costs.Monthly >= limits.Monthly.Limit) ||
		(limits.Total != nil && limits.Total.Enabled && costs.Total >= limits.Total.Limit)
}

func uniqueQuotaCostChecks(checks []quotaCostCheck) []quotaCostCheck {
	seen := map[string]bool{}
	out := make([]quotaCostCheck, 0, len(checks))
	for _, check := range checks {
		if check.key == "" || seen[check.key] {
			continue
		}
		seen[check.key] = true
		out = append(out, check)
	}
	return out
}

func requestQuotaCostKey(input port.GatewayQuotaCostLookupInput) string {
	return strings.Join([]string{
		input.SystemAccountID,
		input.ScopeType,
		input.ScopeID,
		input.StatDate,
		input.StatWeek,
		input.StatMonth,
		hourlyWindowKey(input.HourlyWindowHours),
	}, "\x00")
}

func hourlyWindowKey(value int) string {
	if value <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", normalizeHourlyWindowHours(value))
}

func normalizeHourlyWindowHours(value int) int {
	if value <= 0 {
		return 0
	}
	return max(1, value)
}

func dateKey(value time.Time, location *time.Location) string {
	local := value.In(location)
	return fmt.Sprintf("%04d-%02d-%02d", local.Year(), int(local.Month()), local.Day())
}

func weekKey(value time.Time, location *time.Location) string {
	local := value.In(location)
	year, month, day := local.Date()
	localDate := time.Date(year, month, day, 0, 0, 0, 0, location)
	offset := (int(localDate.Weekday()) + 6) % 7
	start := localDate.AddDate(0, 0, -offset)
	return fmt.Sprintf("%04d-%02d-%02d", start.Year(), int(start.Month()), start.Day())
}

func monthKey(value time.Time, location *time.Location) string {
	local := value.In(location)
	return fmt.Sprintf("%04d-%02d", local.Year(), int(local.Month()))
}

func formatGeneratedAt(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}
