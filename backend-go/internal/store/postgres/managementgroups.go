package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	defaultManagementGroupOptionLimit = 50
	maxManagementGroupOptionLimit     = 50
)

var requiredHighConcurrencySchedulingPolicyKeys = []string{
	"mode",
	"defaultSoftConcurrency",
	"fastFirstEnabled",
	"fallbackOnQueueEnabled",
	"breakAffinityOnSoftLimit",
	"breakAffinityOnQueueWaitMs",
	"slowRequestThresholdMs",
	"firstOutputSlowThresholdMs",
	"recentTimeoutWindowSeconds",
	"recentTimeoutPenaltyThreshold",
	"maxQueueWaitMs",
	"maxQueueSize",
	"perApiKeyQueueLimit",
	"clientIpConcurrencyLimit",
	"clientIpConcurrencyOverflowMode",
	"imageLaneMaxConcurrency",
}

func (s *Store) ListManagementGroupOptions(ctx context.Context, input port.ManagementGroupOptionListInput) ([]port.ManagementGroupOption, error) {
	return listManagementGroupOptions(ctx, s.queries(), input)
}

func (s *Store) ListManagementGroupAccountOptions(ctx context.Context, input port.ManagementGroupOptionListInput) ([]port.ManagementGroupAccountOption, error) {
	return listManagementGroupAccountOptions(ctx, s.queries(), input)
}

func listManagementGroupOptions(ctx context.Context, q *postgresqueries.Queries, input port.ManagementGroupOptionListInput) ([]port.ManagementGroupOption, error) {
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	rows, err := q.ListManagementGroupOptions(ctx, postgresqueries.ListManagementGroupOptionsParams{
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
		Ids:             uniqueStrings(input.IDs, 50),
		ProviderCode:    strings.TrimSpace(input.ProviderCode),
		HasKeyword:      keyword != "",
		Keyword:         keyword,
		KeywordUpper:    keywordUpper,
		PreferDefault:   input.PreferDefault,
		RowLimit:        int32(managementGroupOptionLimit(input.Limit)),
		ManageableOnly:  input.ManageableOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("list management group options: %w", err)
	}
	options := make([]port.ManagementGroupOption, 0, len(rows))
	for _, row := range rows {
		schedulingPolicy, err := managementGroupSchedulingPolicy(row.ID, row.GroupType, row.SchedulingPolicyJson)
		if err != nil {
			return nil, err
		}
		authorizationLimits, err := managementGroupAuthorizationLimits(row.ID, row.AuthorizationLimitsJson)
		if err != nil {
			return nil, err
		}
		option := port.ManagementGroupOption{
			ID:                                 row.ID,
			OwnerSystemAccountID:               row.SystemAccountID,
			OwnerSystemAccountName:             textValue(row.SystemAccountName),
			Name:                               row.Name,
			ProviderCode:                       row.ProviderCode,
			Enabled:                            row.Enabled,
			IsDefault:                          row.IsDefault,
			GroupType:                          managementGroupType(row.GroupType),
			SchedulingPolicy:                   schedulingPolicy,
			AccessType:                         managementGroupAccessType(row.AccessType),
			GroupAuthorizationID:               textValue(row.GroupAuthorizationID),
			AuthorizationStatus:                textValue(row.AuthorizationStatus),
			AuthorizationExpiresAt:             timestamptzPtr(row.AuthorizationExpiresAt),
			AuthorizationLimits:                authorizationLimits,
			HasActiveManualAuthorizationSource: row.HasActiveManualAuthorizationSource,
		}
		if input.IncludeSystemAccountFields {
			option.SystemAccountID = row.SystemAccountID
			option.SystemAccountName = textValue(row.SystemAccountName)
		}
		if option.AccessType != "authorized" && !input.IncludeSystemAccountFields {
			option.OwnerSystemAccountName = ""
		}
		options = append(options, option)
	}
	return options, nil
}

func listManagementGroupAccountOptions(ctx context.Context, q *postgresqueries.Queries, input port.ManagementGroupOptionListInput) ([]port.ManagementGroupAccountOption, error) {
	keyword := strings.TrimSpace(input.Keyword)
	keywordUpper := ""
	if keyword != "" {
		keywordUpper = textPrefixUpperBound(keyword)
	}
	rows, err := q.ListManagementGroupAccountOptions(ctx, postgresqueries.ListManagementGroupAccountOptionsParams{
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
		Ids:             uniqueStrings(input.IDs, 50),
		ProviderCode:    strings.TrimSpace(input.ProviderCode),
		HasKeyword:      keyword != "",
		Keyword:         keyword,
		KeywordUpper:    keywordUpper,
		PreferDefault:   input.PreferDefault,
		RowLimit:        int32(managementGroupOptionLimit(input.Limit)),
		ManageableOnly:  input.ManageableOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("list management account group options: %w", err)
	}
	groupIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		groupIDs = append(groupIDs, row.ID)
	}
	accountIDsByGroupID := map[string][]string{}
	if len(groupIDs) > 0 {
		accountIDRows, err := q.ListManagementGroupAccountOptionIDs(ctx, postgresqueries.ListManagementGroupAccountOptionIDsParams{
			GroupIds:        groupIDs,
			SystemAccountID: strings.TrimSpace(input.SystemAccountID),
		})
		if err != nil {
			return nil, fmt.Errorf("list management group account option ids: %w", err)
		}
		for _, row := range accountIDRows {
			accountIDsByGroupID[row.GroupID] = append(accountIDsByGroupID[row.GroupID], row.AccountID)
		}
	}
	options := make([]port.ManagementGroupAccountOption, 0, len(rows))
	for _, row := range rows {
		schedulingPolicy, err := managementGroupSchedulingPolicy(row.ID, row.GroupType, row.SchedulingPolicyJson)
		if err != nil {
			return nil, err
		}
		authorizationLimits, err := managementGroupAuthorizationLimits(row.ID, row.AuthorizationLimitsJson)
		if err != nil {
			return nil, err
		}
		accessType := managementGroupAccessType(row.AccessType)
		accountIDs := append([]string{}, accountIDsByGroupID[row.ID]...)
		if accessType == "authorized" {
			accountIDs = []string{}
		}
		option := port.ManagementGroupAccountOption{
			ID:                                 row.ID,
			OwnerSystemAccountID:               row.SystemAccountID,
			OwnerSystemAccountName:             textValue(row.SystemAccountName),
			Name:                               row.Name,
			ProviderCode:                       row.ProviderCode,
			Enabled:                            row.Enabled,
			IsDefault:                          row.IsDefault,
			GroupType:                          managementGroupType(row.GroupType),
			SchedulingPolicy:                   schedulingPolicy,
			AccessType:                         accessType,
			GroupAuthorizationID:               textValue(row.GroupAuthorizationID),
			AuthorizationStatus:                textValue(row.AuthorizationStatus),
			AuthorizationExpiresAt:             timestamptzPtr(row.AuthorizationExpiresAt),
			AuthorizationLimits:                authorizationLimits,
			HasActiveManualAuthorizationSource: row.HasActiveManualAuthorizationSource,
			AccountIDs:                         accountIDs,
		}
		if input.IncludeSystemAccountFields {
			option.SystemAccountID = row.SystemAccountID
			option.SystemAccountName = textValue(row.SystemAccountName)
		}
		if option.AccessType != "authorized" && !input.IncludeSystemAccountFields {
			option.OwnerSystemAccountName = ""
		}
		options = append(options, option)
	}
	return options, nil
}

func managementGroupOptionLimit(limit int) int {
	if limit <= 0 {
		return defaultManagementGroupOptionLimit
	}
	return min(limit, maxManagementGroupOptionLimit)
}

func managementGroupType(value string) string {
	if strings.TrimSpace(value) == "" {
		return "personal"
	}
	return value
}

func managementGroupAccessType(value string) string {
	if strings.TrimSpace(value) == "authorized" {
		return "authorized"
	}
	return "owner"
}

func managementGroupAuthorizationLimits(groupID string, value pgtype.Text) (map[string]any, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, nil
	}
	var limits map[string]any
	if err := json.Unmarshal([]byte(value.String), &limits); err != nil {
		return nil, fmt.Errorf("group %s authorization limits json is invalid: %w", groupID, err)
	}
	return limits, nil
}

func managementGroupSchedulingPolicy(groupID string, groupType string, value pgtype.Text) (map[string]any, error) {
	if managementGroupType(groupType) != "high_concurrency" {
		return nil, nil
	}
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, fmt.Errorf("group %s high concurrency scheduling policy is missing", groupID)
	}
	var policy map[string]any
	if err := json.Unmarshal([]byte(value.String), &policy); err != nil {
		return nil, fmt.Errorf("group %s high concurrency scheduling policy is invalid: %w", groupID, err)
	}
	for _, key := range requiredHighConcurrencySchedulingPolicyKeys {
		if policy[key] == nil {
			return nil, fmt.Errorf("group %s high concurrency scheduling policy missing %s", groupID, key)
		}
	}
	return policy, nil
}

var _ port.ManagementGroupOptionReader = (*Store)(nil)
