package managementgroups

import (
	"context"
	"strings"
	"time"
)

func (s *Service) loadManagementGroupCurrentConcurrency(ctx context.Context, groupIDs []string, now time.Time) (map[string]int, bool, error) {
	result := make(map[string]int, len(groupIDs))
	for _, groupID := range groupIDs {
		result[strings.TrimSpace(groupID)] = 0
	}
	if s.dynamicStore == nil || s.accountConcurrency == nil {
		return result, false, nil
	}
	rows, err := s.dynamicStore.ListManagementGroupConcurrencyAccountIDs(ctx, groupIDs)
	if err != nil {
		return nil, false, err
	}
	accountIDsByGroup := make(map[string]map[string]struct{}, len(groupIDs))
	allAccountIDs := make([]string, 0, len(rows))
	allSeen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		groupID := strings.TrimSpace(row.GroupID)
		accountID := strings.TrimSpace(row.AccountID)
		if groupID == "" || accountID == "" {
			continue
		}
		if accountIDsByGroup[groupID] == nil {
			accountIDsByGroup[groupID] = make(map[string]struct{})
		}
		accountIDsByGroup[groupID][accountID] = struct{}{}
		if _, exists := allSeen[accountID]; !exists {
			allSeen[accountID] = struct{}{}
			allAccountIDs = append(allAccountIDs, accountID)
		}
	}
	if len(allAccountIDs) == 0 {
		return result, true, nil
	}
	values, err := s.accountConcurrency.LoadAccountCurrentConcurrencyByIDs(ctx, allAccountIDs, now)
	if err != nil {
		return result, false, nil
	}
	for groupID, accountIDs := range accountIDsByGroup {
		for accountID := range accountIDs {
			result[groupID] += max(0, values[accountID])
		}
	}
	return result, true, nil
}
