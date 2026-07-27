package cooldownaccountretest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

type QuotaEligibility struct {
	Subjects  port.CooldownAccountRetestQuotaSubjectReader
	Costs     port.GatewayQuotaCostReader
	Timezones port.ManagementUsageStatsTimezoneReader
}

type cooldownAccountRetestQuotaCheck struct {
	accountID string
	limits    port.ManagementRequestQuotaLimits
	input     port.GatewayQuotaCostLookupInput
}

// EligibleByAccountID resolves one page as a batch. Every requested account is
// initialized to false so absent or malformed authorization evidence fails closed.
func (q QuotaEligibility) EligibleByAccountID(
	ctx context.Context,
	candidates []port.CooldownAccountRetestCandidate,
	now time.Time,
) (map[string]bool, error) {
	accountIDs := uniqueCooldownAccountRetestCandidateIDs(candidates)
	eligible := make(map[string]bool, len(accountIDs))
	for _, accountID := range accountIDs {
		eligible[accountID] = false
	}
	if len(accountIDs) == 0 {
		return eligible, nil
	}
	if q.Subjects == nil || q.Costs == nil || q.Timezones == nil {
		return eligible, fmt.Errorf("cooldown account retest quota dependencies are required")
	}
	if now.IsZero() {
		now = time.Now()
	}
	location, err := q.usageStatsLocation(ctx)
	if err != nil {
		return eligible, err
	}
	subjects, err := q.Subjects.LoadCooldownAccountRetestQuotaSubjects(ctx, accountIDs, now)
	if err != nil {
		return eligible, fmt.Errorf("load cooldown account retest quota subjects: %w", err)
	}

	statDate := cooldownAccountRetestQuotaDateKey(now, location)
	statWeek := cooldownAccountRetestQuotaWeekKey(now, location)
	statMonth := cooldownAccountRetestQuotaMonthKey(now, location)
	checks := make([]cooldownAccountRetestQuotaCheck, 0, len(subjects)*2)
	seenSubjects := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		accountID := strings.TrimSpace(subject.AccountID)
		if _, requested := eligible[accountID]; !requested {
			continue
		}
		if _, duplicate := seenSubjects[accountID]; duplicate {
			return failClosedCooldownAccountRetestQuota(eligible), fmt.Errorf("duplicate cooldown account retest quota subject %q", accountID)
		}
		seenSubjects[accountID] = struct{}{}
		if !subject.AuthorizationValid {
			continue
		}
		switch subject.AccessType {
		case port.CooldownAccountRetestQuotaAccessOwner:
			if strings.TrimSpace(subject.AuthorizationID) == "" && validCooldownAccountRetestQuotaLimits(subject.DirectLimits) && validCooldownAccountRetestQuotaLimits(subject.TeamLimits) {
				eligible[accountID] = true
			}
		case port.CooldownAccountRetestQuotaAccessAuthorized:
			authorizationID := strings.TrimSpace(subject.AuthorizationID)
			systemAccountID := strings.TrimSpace(subject.SystemAccountID)
			if authorizationID == "" || systemAccountID == "" ||
				!validCooldownAccountRetestQuotaLimits(subject.DirectLimits) ||
				!validCooldownAccountRetestQuotaLimits(subject.TeamLimits) {
				continue
			}
			eligible[accountID] = true
			if hasCooldownAccountRetestQuotaLimit(subject.DirectLimits) {
				checks = append(checks, newCooldownAccountRetestQuotaCheck(
					accountID, systemAccountID, "account_authorization", authorizationID,
					subject.DirectLimits, statDate, statWeek, statMonth,
				))
			}
			teamID := strings.TrimSpace(subject.EffectiveSourceTeamID)
			if teamID != "" && hasCooldownAccountRetestQuotaLimit(subject.TeamLimits) {
				checks = append(checks, newCooldownAccountRetestQuotaCheck(
					accountID, systemAccountID, "account_authorization_team", accountID+":"+teamID,
					subject.TeamLimits, statDate, statWeek, statMonth,
				))
			}
		}
	}

	checks = uniqueCooldownAccountRetestQuotaChecks(checks)
	if len(checks) == 0 {
		return eligible, nil
	}
	inputs := make([]port.GatewayQuotaCostLookupInput, 0, len(checks))
	for _, check := range checks {
		inputs = append(inputs, check.input)
	}
	costsByKey, err := q.Costs.LoadGatewayQuotaSnapshotCosts(ctx, inputs)
	if err != nil {
		return failClosedCooldownAccountRetestQuota(eligible), fmt.Errorf("load cooldown account retest quota costs: %w", err)
	}
	for _, check := range checks {
		costs, found := costsByKey[check.input.Key]
		if !found || cooldownAccountRetestQuotaExceeded(check.limits, costs) {
			eligible[check.accountID] = false
		}
	}
	return eligible, nil
}

func failClosedCooldownAccountRetestQuota(eligible map[string]bool) map[string]bool {
	for accountID := range eligible {
		eligible[accountID] = false
	}
	return eligible
}

func (q QuotaEligibility) usageStatsLocation(ctx context.Context) (*time.Location, error) {
	timezone, found, err := q.Timezones.GetManagementUsageStatsTimezone(ctx)
	if err != nil {
		return nil, fmt.Errorf("read cooldown account retest quota timezone: %w", err)
	}
	if !found || strings.TrimSpace(timezone) == "" {
		return nil, fmt.Errorf("系统设置缺少 usageStatsTimezone")
	}
	location, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil {
		return nil, fmt.Errorf("系统设置 usageStatsTimezone 无效: %w", err)
	}
	return location, nil
}

func newCooldownAccountRetestQuotaCheck(
	accountID string,
	systemAccountID string,
	scopeType string,
	scopeID string,
	limits port.ManagementRequestQuotaLimits,
	statDate string,
	statWeek string,
	statMonth string,
) cooldownAccountRetestQuotaCheck {
	hourlyWindowHours := 0
	if limits.Hourly != nil && limits.Hourly.Enabled {
		hourlyWindowHours = limits.Hourly.Hours
	}
	input := port.GatewayQuotaCostLookupInput{
		SystemAccountID:   systemAccountID,
		ScopeType:         scopeType,
		ScopeID:           scopeID,
		StatDate:          statDate,
		StatWeek:          statWeek,
		StatMonth:         statMonth,
		HourlyWindowHours: hourlyWindowHours,
	}
	input.Key = cooldownAccountRetestQuotaCostKey(input)
	return cooldownAccountRetestQuotaCheck{accountID: accountID, limits: limits, input: input}
}

func uniqueCooldownAccountRetestCandidateIDs(candidates []port.CooldownAccountRetestCandidate) []string {
	seen := make(map[string]struct{}, len(candidates))
	output := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		accountID := strings.TrimSpace(candidate.ID)
		if accountID == "" {
			continue
		}
		if _, exists := seen[accountID]; exists {
			continue
		}
		seen[accountID] = struct{}{}
		output = append(output, accountID)
	}
	return output
}

func uniqueCooldownAccountRetestQuotaChecks(checks []cooldownAccountRetestQuotaCheck) []cooldownAccountRetestQuotaCheck {
	seen := make(map[string]struct{}, len(checks))
	output := make([]cooldownAccountRetestQuotaCheck, 0, len(checks))
	for _, check := range checks {
		key := check.accountID + "\x00" + check.input.Key
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		output = append(output, check)
	}
	return output
}

func hasCooldownAccountRetestQuotaLimit(limits port.ManagementRequestQuotaLimits) bool {
	return (limits.Hourly != nil && limits.Hourly.Enabled) ||
		(limits.Daily != nil && limits.Daily.Enabled) ||
		(limits.Weekly != nil && limits.Weekly.Enabled) ||
		(limits.Monthly != nil && limits.Monthly.Enabled) ||
		(limits.Total != nil && limits.Total.Enabled)
}

func validCooldownAccountRetestQuotaLimits(limits port.ManagementRequestQuotaLimits) bool {
	if limits.Hourly != nil && limits.Hourly.Enabled && (limits.Hourly.Hours <= 0 || limits.Hourly.Limit <= 0) {
		return false
	}
	for _, limit := range []*port.ManagementRequestQuotaLimit{limits.Daily, limits.Weekly, limits.Monthly, limits.Total} {
		if limit != nil && limit.Enabled && limit.Limit <= 0 {
			return false
		}
	}
	return true
}

func cooldownAccountRetestQuotaExceeded(limits port.ManagementRequestQuotaLimits, costs port.GatewayQuotaCosts) bool {
	return (limits.Hourly != nil && limits.Hourly.Enabled && costs.Hourly >= limits.Hourly.Limit) ||
		(limits.Daily != nil && limits.Daily.Enabled && costs.Daily >= limits.Daily.Limit) ||
		(limits.Weekly != nil && limits.Weekly.Enabled && costs.Weekly >= limits.Weekly.Limit) ||
		(limits.Monthly != nil && limits.Monthly.Enabled && costs.Monthly >= limits.Monthly.Limit) ||
		(limits.Total != nil && limits.Total.Enabled && costs.Total >= limits.Total.Limit)
}

func cooldownAccountRetestQuotaCostKey(input port.GatewayQuotaCostLookupInput) string {
	hourlyWindow := ""
	if input.HourlyWindowHours > 0 {
		hourlyWindow = fmt.Sprintf("%d", input.HourlyWindowHours)
	}
	return strings.Join([]string{
		input.SystemAccountID,
		input.ScopeType,
		input.ScopeID,
		input.StatDate,
		input.StatWeek,
		input.StatMonth,
		hourlyWindow,
	}, "\x00")
}

func cooldownAccountRetestQuotaDateKey(value time.Time, location *time.Location) string {
	local := value.In(location)
	return fmt.Sprintf("%04d-%02d-%02d", local.Year(), int(local.Month()), local.Day())
}

func cooldownAccountRetestQuotaWeekKey(value time.Time, location *time.Location) string {
	local := value.In(location)
	year, month, day := local.Date()
	localDate := time.Date(year, month, day, 0, 0, 0, 0, location)
	offset := (int(localDate.Weekday()) + 6) % 7
	start := localDate.AddDate(0, 0, -offset)
	return fmt.Sprintf("%04d-%02d-%02d", start.Year(), int(start.Month()), start.Day())
}

func cooldownAccountRetestQuotaMonthKey(value time.Time, location *time.Location) string {
	local := value.In(location)
	return fmt.Sprintf("%04d-%02d", local.Year(), int(local.Month()))
}
