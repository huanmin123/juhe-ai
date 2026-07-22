package managementgroups

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	MaxStatusSnapshotGroupIDs = 100
	MaxStatusSnapshotQuery    = 8192
)

var (
	ErrStatusSnapshotIDsMissing = errors.New("分组状态快照至少选择 1 个分组")
	ErrStatusSnapshotIDsTooMany = errors.New("分组状态快照最多查询 100 个分组")
	ErrStatusSnapshotQueryLong  = errors.New("分组状态快照查询参数过长")
)

type managementGroupStatusSnapshotStore interface {
	port.ManagementGroupStatusSnapshotReader
	port.ManagementGroupAccountStatsReader
	ListManagementGroupUsageDaily(context.Context, string, []port.ManagementGroupUsageLookupInput) ([]port.ManagementGroupUsageRow, error)
}

type StatusSnapshotInput struct {
	ActorSystemAccountID string
	ActorRole            string
	SystemAccountID      string
	SelfOnly             bool
	GroupIDs             []string
}

type StatusSnapshotItem struct {
	ID                 string       `json:"id"`
	CurrentConcurrency int          `json:"currentConcurrency"`
	TodayUsage         UsageSummary `json:"todayUsage"`
}

type StatusSnapshotResult struct {
	GeneratedAt string               `json:"generatedAt"`
	Items       []StatusSnapshotItem `json:"items"`
}

func ParseStatusSnapshotGroupIDs(raw string) ([]string, error) {
	if len(raw) > MaxStatusSnapshotQuery {
		return nil, ErrStatusSnapshotQueryLong
	}
	return normalizeStatusSnapshotGroupIDs(strings.Split(raw, ","))
}

func normalizeStatusSnapshotGroupIDs(values []string) ([]string, error) {
	seen := make(map[string]struct{}, min(len(values), MaxStatusSnapshotGroupIDs))
	ids := make([]string, 0, min(len(values), MaxStatusSnapshotGroupIDs))
	for _, value := range values {
		id := strings.TrimSpace(value)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, ErrStatusSnapshotIDsMissing
	}
	if len(ids) > MaxStatusSnapshotGroupIDs {
		return nil, ErrStatusSnapshotIDsTooMany
	}
	return ids, nil
}

func (s *Service) StatusSnapshot(ctx context.Context, input StatusSnapshotInput) (StatusSnapshotResult, error) {
	if s == nil || s.statusSnapshotStore == nil {
		return StatusSnapshotResult{}, fmt.Errorf("management group status snapshot reader is required")
	}
	groupIDs, err := normalizeStatusSnapshotGroupIDs(input.GroupIDs)
	if err != nil {
		return StatusSnapshotResult{}, err
	}
	systemAccountID, _, err := managementGroupListScope(ListInput{
		ActorSystemAccountID: input.ActorSystemAccountID,
		ActorRole:            input.ActorRole,
		SystemAccountID:      input.SystemAccountID,
		SelfOnly:             input.SelfOnly,
	})
	if err != nil {
		return StatusSnapshotResult{}, err
	}
	now := s.now()
	statDate, err := s.managementGroupListStatDate(ctx, now)
	if err != nil {
		return StatusSnapshotResult{}, err
	}
	rows, err := s.statusSnapshotStore.ListManagementGroupStatusSnapshotRows(ctx, port.ManagementGroupStatusSnapshotInput{
		SystemAccountID: systemAccountID,
		GroupIDs:        groupIDs,
	})
	if err != nil {
		return StatusSnapshotResult{}, err
	}
	if len(rows) == 0 {
		return StatusSnapshotResult{GeneratedAt: now.UTC().Format(time.RFC3339Nano), Items: []StatusSnapshotItem{}}, nil
	}
	visibleGroupIDs := make([]string, 0, len(rows))
	usageInputs := make([]port.ManagementGroupUsageLookupInput, 0, len(rows))
	for _, row := range rows {
		visibleGroupIDs = append(visibleGroupIDs, row.ID)
		scopeType := "group"
		scopeID := row.ID
		if strings.TrimSpace(row.AccessType) == "authorized" {
			scopeType = "group_authorization"
			scopeID = strings.TrimSpace(row.GroupAuthorizationID)
			if scopeID == "" {
				return StatusSnapshotResult{}, fmt.Errorf("authorized management group %q is missing authorization id", row.ID)
			}
		}
		usageInputs = append(usageInputs, port.ManagementGroupUsageLookupInput{
			Key:             row.ID,
			SystemAccountID: row.SystemAccountID,
			ScopeType:       scopeType,
			ScopeID:         scopeID,
		})
	}
	stats, err := s.statusSnapshotStore.ListManagementGroupAccountStats(ctx, visibleGroupIDs)
	if err != nil {
		return StatusSnapshotResult{}, err
	}
	usage, err := s.statusSnapshotStore.ListManagementGroupUsageDaily(ctx, statDate, usageInputs)
	if err != nil {
		return StatusSnapshotResult{}, err
	}
	statsByGroupID := make(map[string]port.ManagementGroupAccountStatsRow, len(stats))
	for _, row := range stats {
		statsByGroupID[row.GroupID] = row
	}
	usageByGroupID := make(map[string]port.ManagementAccountUsageSummary, len(usage))
	for _, row := range usage {
		usageByGroupID[row.Key] = row.Usage
	}
	items := make([]StatusSnapshotItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, StatusSnapshotItem{
			ID:                 row.ID,
			CurrentConcurrency: statsByGroupID[row.ID].CurrentConcurrency,
			TodayUsage:         managementGroupUsageSummary(usageByGroupID[row.ID]),
		})
	}
	return StatusSnapshotResult{GeneratedAt: now.UTC().Format(time.RFC3339Nano), Items: items}, nil
}
