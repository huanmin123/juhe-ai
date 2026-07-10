package managementgroups

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

var ErrGroupNotFound = errors.New("分组不存在")

type AccountConcurrencyReader interface {
	LoadAccountCurrentConcurrencyByIDs(
		ctx context.Context,
		accountIDs []string,
		now time.Time,
	) (map[string]int, error)
}

type DetailInput struct {
	ActorSystemAccountID string
	ActorRole            string
	SystemAccountID      string
	SelfOnly             bool
	GroupID              string
}

type DetailAuthorizationSource struct {
	ID              string     `json:"id"`
	AuthorizationID string     `json:"authorizationId"`
	SourceType      string     `json:"sourceType"`
	SourceTeamName  string     `json:"sourceTeamName,omitempty"`
	Status          string     `json:"status"`
	ActivatedAt     *time.Time `json:"activatedAt,omitempty"`
	EndedReason     string     `json:"endedReason,omitempty"`
	CreatedBy       string     `json:"createdBy"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type DetailResult struct {
	ID                     string                            `json:"id"`
	SystemAccountID        string                            `json:"systemAccountId,omitempty"`
	SystemAccountName      string                            `json:"systemAccountName,omitempty"`
	OwnerSystemAccountID   string                            `json:"ownerSystemAccountId"`
	OwnerSystemAccountName string                            `json:"ownerSystemAccountName,omitempty"`
	Name                   string                            `json:"name"`
	ProviderCode           string                            `json:"providerCode"`
	Description            *string                           `json:"description,omitempty"`
	Enabled                bool                              `json:"enabled"`
	IsDefault              bool                              `json:"isDefault"`
	GroupType              string                            `json:"groupType"`
	SchedulingPolicy       *SchedulingPolicy                 `json:"schedulingPolicy,omitempty"`
	AccountIDs             []string                          `json:"accountIds"`
	AccountStats           GroupAccountStats                 `json:"accountStats"`
	AccessType             string                            `json:"accessType"`
	GroupAuthorizationID   string                            `json:"groupAuthorizationId,omitempty"`
	AuthorizationStatus    string                            `json:"authorizationStatus,omitempty"`
	AuthorizationExpiresAt *time.Time                        `json:"authorizationExpiresAt,omitempty"`
	AuthorizationLimits    port.ManagementRequestQuotaLimits `json:"authorizationLimits"`
	AuthorizationSources   *[]DetailAuthorizationSource      `json:"authorizationSources,omitempty"`
	Permissions            ResourcePermissions               `json:"permissions"`
}

func (s *Service) Detail(ctx context.Context, input DetailInput) (DetailResult, error) {
	if s.detailStore == nil {
		return DetailResult{}, fmt.Errorf("management group detail reader is required")
	}
	if s.listStore == nil {
		return DetailResult{}, fmt.Errorf("management group list reader is required")
	}
	if s.accountConcurrency == nil {
		return DetailResult{}, fmt.Errorf("account concurrency reader is required")
	}
	groupID := input.GroupID
	systemAccountID, includeSystemAccountFields, err := managementGroupDetailScope(input)
	if err != nil {
		return DetailResult{}, err
	}
	storeInput := port.ManagementGroupDetailInput{
		GroupID:         groupID,
		SystemAccountID: systemAccountID,
	}
	row, found, err := s.detailStore.FindManagementGroupDetail(ctx, storeInput)
	if err != nil {
		return DetailResult{}, err
	}
	if !found {
		return DetailResult{}, ErrGroupNotFound
	}

	accountIDs, err := s.detailStore.ListManagementGroupDetailAccountIDs(ctx, storeInput)
	if err != nil {
		return DetailResult{}, err
	}
	now := s.now()
	currentConcurrency, err := s.accountConcurrency.LoadAccountCurrentConcurrencyByIDs(ctx, accountIDs, now)
	if err != nil {
		return DetailResult{}, err
	}
	statsRows, err := s.listStore.ListManagementGroupAccountStats(ctx, []string{row.ID})
	if err != nil {
		return DetailResult{}, err
	}
	statsRow := port.ManagementGroupAccountStatsRow{}
	for _, candidate := range statsRows {
		if candidate.GroupID == row.ID && candidate.SystemAccountID == row.SystemAccountID {
			statsRow = candidate
			break
		}
	}

	accessType := groupAccessType(row.AccessType)
	usageInput, err := managementGroupDetailUsageInput(row, accessType)
	if err != nil {
		return DetailResult{}, err
	}
	totalUsageRows, err := s.listStore.ListManagementGroupUsageTotals(ctx, []port.ManagementGroupUsageLookupInput{usageInput})
	if err != nil {
		return DetailResult{}, err
	}
	statDate, err := s.managementGroupListStatDate(ctx, now)
	if err != nil {
		return DetailResult{}, err
	}
	todayUsageRows, err := s.listStore.ListManagementGroupUsageDaily(
		ctx,
		statDate,
		[]port.ManagementGroupUsageLookupInput{usageInput},
	)
	if err != nil {
		return DetailResult{}, err
	}
	totalUsage := managementGroupDetailUsage(totalUsageRows, usageInput.Key)
	todayUsage := managementGroupDetailUsage(todayUsageRows, usageInput.Key)

	groupType, err := normalizeGroupType(row.GroupType)
	if err != nil {
		return DetailResult{}, fmt.Errorf("normalize management group %q type: %w", row.ID, err)
	}
	schedulingPolicy, err := parseManagementGroupListSchedulingPolicy(row.SchedulingPolicyJSON, groupType)
	if err != nil {
		return DetailResult{}, fmt.Errorf("parse management group %q scheduling policy: %w", row.ID, err)
	}
	authorizationLimits, err := parseManagementGroupAuthorizationLimits(row.AuthorizationLimitsJSON)
	if err != nil {
		return DetailResult{}, fmt.Errorf("parse management group %q authorization limits: %w", row.ID, err)
	}

	stats := managementGroupAccountStats(statsRow, todayUsage, totalUsage)
	permissions := ownerPermissions()
	isDefault := row.IsDefault
	responseAccountIDs := append([]string{}, accountIDs...)
	var authorizationSources *[]DetailAuthorizationSource
	if accessType == "authorized" {
		if strings.TrimSpace(row.GroupAuthorizationID) == "" {
			return DetailResult{}, fmt.Errorf("authorized management group %q is missing authorization id", row.ID)
		}
		sourceRows, err := s.detailStore.ListManagementGroupDetailAuthorizationSources(ctx, storeInput)
		if err != nil {
			return DetailResult{}, err
		}
		sources := managementGroupDetailAuthorizationSources(sourceRows)
		authorizationSources = &sources
		permissions = authorizedGroupPermissions(
			canBindAuthorizedGroupAt(row.Enabled, row.AuthorizationStatus, row.AuthorizationExpiresAt, now),
			hasActiveManualManagementGroupDetailSource(sourceRows),
		)
		responseAccountIDs = []string{}
		isDefault = false
	} else {
		stats.Total = len(accountIDs)
		stats.CurrentConcurrency = sumManagementGroupAccountConcurrency(accountIDs, currentConcurrency)
	}

	result := DetailResult{
		ID:                     row.ID,
		OwnerSystemAccountID:   row.SystemAccountID,
		OwnerSystemAccountName: row.SystemAccountName,
		Name:                   row.Name,
		ProviderCode:           row.ProviderCode,
		Description:            row.Description,
		Enabled:                row.Enabled,
		IsDefault:              isDefault,
		GroupType:              groupType,
		SchedulingPolicy:       schedulingPolicy,
		AccountIDs:             responseAccountIDs,
		AccountStats:           stats,
		AccessType:             accessType,
		GroupAuthorizationID:   row.GroupAuthorizationID,
		AuthorizationStatus:    row.AuthorizationStatus,
		AuthorizationExpiresAt: row.AuthorizationExpiresAt,
		AuthorizationLimits:    authorizationLimits,
		AuthorizationSources:   authorizationSources,
		Permissions:            permissions,
	}
	if includeSystemAccountFields {
		result.SystemAccountID = row.SystemAccountID
		result.SystemAccountName = row.SystemAccountName
	}
	if accessType != "authorized" {
		result.GroupAuthorizationID = ""
		result.AuthorizationStatus = ""
		result.AuthorizationExpiresAt = nil
	}
	return result, nil
}

func managementGroupDetailScope(input DetailInput) (string, bool, error) {
	actorSystemAccountID := strings.TrimSpace(input.ActorSystemAccountID)
	if actorSystemAccountID == "" || strings.TrimSpace(input.GroupID) == "" {
		return "", false, ErrGroupListInvalid
	}
	if input.SelfOnly || !managementGroupListAdminRole(input.ActorRole) {
		return actorSystemAccountID, false, nil
	}
	systemAccountID := strings.TrimSpace(input.SystemAccountID)
	if systemAccountID == "all" {
		systemAccountID = ""
	}
	return systemAccountID, true, nil
}

func managementGroupDetailUsageInput(
	row port.ManagementGroupListRow,
	accessType string,
) (port.ManagementGroupUsageLookupInput, error) {
	if accessType == "authorized" {
		authorizationID := strings.TrimSpace(row.GroupAuthorizationID)
		if authorizationID == "" {
			return port.ManagementGroupUsageLookupInput{}, fmt.Errorf(
				"authorized management group %q is missing authorization id",
				row.ID,
			)
		}
		return port.ManagementGroupUsageLookupInput{
			Key:             authorizationID,
			SystemAccountID: row.SystemAccountID,
			ScopeType:       "group_authorization",
			ScopeID:         authorizationID,
		}, nil
	}
	return port.ManagementGroupUsageLookupInput{
		Key:             row.ID,
		SystemAccountID: row.SystemAccountID,
		ScopeType:       "group",
		ScopeID:         row.ID,
	}, nil
}

func managementGroupDetailUsage(
	rows []port.ManagementGroupUsageRow,
	key string,
) port.ManagementAccountUsageSummary {
	for _, row := range rows {
		if row.Key == key {
			return row.Usage
		}
	}
	return port.ManagementAccountUsageSummary{}
}

func managementGroupDetailAuthorizationSources(
	rows []port.ManagementResourceAuthorizationSourceSummary,
) []DetailAuthorizationSource {
	result := make([]DetailAuthorizationSource, 0, len(rows))
	for _, row := range rows {
		result = append(result, DetailAuthorizationSource{
			ID:              row.ID,
			AuthorizationID: row.AuthorizationID,
			SourceType:      row.SourceType,
			SourceTeamName:  row.SourceTeamName,
			Status:          row.Status,
			ActivatedAt:     row.ActivatedAt,
			EndedReason:     row.EndedReason,
			CreatedBy:       "",
			CreatedAt:       row.CreatedAt,
			UpdatedAt:       row.UpdatedAt,
		})
	}
	return result
}

func hasActiveManualManagementGroupDetailSource(
	rows []port.ManagementResourceAuthorizationSourceSummary,
) bool {
	for _, row := range rows {
		if row.SourceType == "manual" && row.Status == "active" {
			return true
		}
	}
	return false
}

func sumManagementGroupAccountConcurrency(accountIDs []string, values map[string]int) int {
	total := 0
	seen := make(map[string]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		accountID = strings.TrimSpace(accountID)
		if accountID == "" {
			continue
		}
		if _, exists := seen[accountID]; exists {
			continue
		}
		seen[accountID] = struct{}{}
		total += max(0, values[accountID])
	}
	return total
}
