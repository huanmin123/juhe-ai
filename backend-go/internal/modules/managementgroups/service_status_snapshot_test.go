package managementgroups

import (
	"context"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceStatusSnapshotUsesVisibleRowsAndScopeSpecificUsage(t *testing.T) {
	now := time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)
	store := &managementGroupStatusSnapshotStoreStub{
		rows: []port.ManagementGroupStatusSnapshotRow{
			{ID: "grp_owner", SystemAccountID: "sys_owner", AccessType: "owner"},
			{ID: "grp_authorized", SystemAccountID: "sys_owner", AccessType: "authorized", GroupAuthorizationID: "auth_group"},
		},
		stats: []port.ManagementGroupAccountStatsRow{
			{SystemAccountID: "sys_owner", GroupID: "grp_owner", CurrentConcurrency: 3},
			{SystemAccountID: "sys_owner", GroupID: "grp_authorized", CurrentConcurrency: 9},
		},
		usage: []port.ManagementGroupUsageRow{
			{Key: "grp_owner", Usage: port.ManagementAccountUsageSummary{RequestCount: 2, InputTokens: 8, OutputTokens: 3}},
			{Key: "grp_authorized", Usage: port.ManagementAccountUsageSummary{RequestCount: 4, InputTokens: 5, OutputTokens: 7}},
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		StatusSnapshotStore:     store,
		UsageStatsTimezoneStore: managementGroupStatusSnapshotTimezoneStub{timezone: "UTC", found: true},
		Now:                     func() time.Time { return now },
	})

	result, err := service.StatusSnapshot(context.Background(), StatusSnapshotInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		SystemAccountID:      "sys_target",
		GroupIDs:             []string{"grp_owner", "grp_authorized", "grp_owner"},
	})
	if err != nil {
		t.Fatalf("StatusSnapshot() error = %v", err)
	}
	if got, want := store.input.GroupIDs, []string{"grp_owner", "grp_authorized"}; !sameStrings(got, want) {
		t.Fatalf("snapshot IDs = %#v, want %#v", got, want)
	}
	if got, want := store.usageInputs, []port.ManagementGroupUsageLookupInput{
		{Key: "grp_owner", SystemAccountID: "sys_owner", ScopeType: "group", ScopeID: "grp_owner"},
		{Key: "grp_authorized", SystemAccountID: "sys_owner", ScopeType: "group_authorization", ScopeID: "auth_group"},
	}; !sameUsageInputs(got, want) {
		t.Fatalf("usage inputs = %#v, want %#v", got, want)
	}
	if result.GeneratedAt != now.Format(time.RFC3339) {
		t.Fatalf("generatedAt = %q", result.GeneratedAt)
	}
	if len(result.Items) != 2 || result.Items[0].CurrentConcurrency != 3 || result.Items[1].TodayUsage.TotalTokens != 12 {
		t.Fatalf("snapshot items = %#v", result.Items)
	}
}

func TestServiceStatusSnapshotEnforcesScope(t *testing.T) {
	service := NewServiceWithOptions(ServiceOptions{StatusSnapshotStore: &managementGroupStatusSnapshotStoreStub{}})
	_, err := service.StatusSnapshot(context.Background(), StatusSnapshotInput{
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
		GroupIDs:             []string{"grp_1"},
	})
	if err == nil {
		t.Fatal("StatusSnapshot() error = nil, want admin denial")
	}
}

type managementGroupStatusSnapshotStoreStub struct {
	input       port.ManagementGroupStatusSnapshotInput
	rows        []port.ManagementGroupStatusSnapshotRow
	stats       []port.ManagementGroupAccountStatsRow
	usage       []port.ManagementGroupUsageRow
	usageInputs []port.ManagementGroupUsageLookupInput
}

type managementGroupStatusSnapshotTimezoneStub struct {
	timezone string
	found    bool
}

func (s managementGroupStatusSnapshotTimezoneStub) GetManagementUsageStatsTimezone(context.Context) (string, bool, error) {
	return s.timezone, s.found, nil
}

func (s *managementGroupStatusSnapshotStoreStub) ListManagementGroupStatusSnapshotRows(_ context.Context, input port.ManagementGroupStatusSnapshotInput) ([]port.ManagementGroupStatusSnapshotRow, error) {
	s.input = input
	return append([]port.ManagementGroupStatusSnapshotRow(nil), s.rows...), nil
}

func (s *managementGroupStatusSnapshotStoreStub) ListManagementGroupAccountStats(_ context.Context, _ []string) ([]port.ManagementGroupAccountStatsRow, error) {
	return append([]port.ManagementGroupAccountStatsRow(nil), s.stats...), nil
}

func (s *managementGroupStatusSnapshotStoreStub) ListManagementGroupUsageDaily(_ context.Context, _ string, input []port.ManagementGroupUsageLookupInput) ([]port.ManagementGroupUsageRow, error) {
	s.usageInputs = append([]port.ManagementGroupUsageLookupInput(nil), input...)
	return append([]port.ManagementGroupUsageRow(nil), s.usage...), nil
}

func sameUsageInputs(got, want []port.ManagementGroupUsageLookupInput) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
