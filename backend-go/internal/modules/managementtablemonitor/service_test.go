package managementtablemonitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceOverviewMapsNodeDTOAndUsesBoundedLimit(t *testing.T) {
	store := &tableMonitorReaderStub{overview: port.ManagementTableStorageOverview{
		SampledAt: "2026-07-22T10:00:00.000Z",
		Databases: []port.ManagementDatabaseStorageSnapshot{{
			DatabaseRole: port.MonitoredDatabaseRoleBusiness,
			DatabasePath: "postgres:juhe_business",
			SampledAt:    "2026-07-22T10:00:00.000Z",
			FileBytes:    int64Pointer(4096),
		}},
		Tables: []port.ManagementTableStorageSnapshot{{
			DatabaseRole: port.MonitoredDatabaseRoleBusiness,
			TableName:    "system_accounts",
			SampledAt:    "2026-07-22T10:00:00.000Z",
			TableBytes:   int64Pointer(100),
			IndexBytes:   int64Pointer(25),
			TotalBytes:   int64Pointer(125),
		}},
	}}
	service := NewService(store)

	result, err := service.Overview(context.Background(), OverviewInput{Limit: 20})
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if store.overviewLimit != 20 {
		t.Fatalf("overview limit = %d, want 20", store.overviewLimit)
	}
	if result.SampledAt != "2026-07-22T10:00:00.000Z" || len(result.Databases) != 1 || len(result.Tables) != 1 {
		t.Fatalf("overview = %+v", result)
	}
	if result.Databases[0].DatabasePath != "postgres:juhe_business" {
		t.Fatalf("database path = %q", result.Databases[0].DatabasePath)
	}
	if result.Tables[0].IndexToTableRatio == nil || *result.Tables[0].IndexToTableRatio != 0.25 {
		t.Fatalf("index ratio = %v", result.Tables[0].IndexToTableRatio)
	}
}

func TestServiceHistoryDefaultsToThirtyDaysAndKeepsStableOldestFirstRows(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 34, 56, 987_654_321, time.UTC)
	store := &tableMonitorReaderStub{
		tableHistory:    []port.ManagementTableStorageSnapshot{},
		databaseHistory: []port.ManagementDatabaseStorageSnapshot{},
	}
	service := NewServiceWithOptions(ServiceOptions{Store: store, Now: func() time.Time { return now }})

	tableRows, err := service.TableHistory(context.Background(), TableHistoryInput{
		DatabaseRole: port.MonitoredDatabaseRoleDataset,
		TableName:    " usage_records ",
	})
	if err != nil {
		t.Fatalf("TableHistory: %v", err)
	}
	if tableRows == nil {
		t.Fatal("table history must return an array")
	}
	wantStart := "2026-06-22T12:34:56.987Z"
	wantEnd := "2026-07-22T12:34:56.987Z"
	if store.tableHistoryInput.DatabaseRole != port.MonitoredDatabaseRoleDataset ||
		store.tableHistoryInput.TableName != "usage_records" ||
		store.tableHistoryInput.StartAt != wantStart || store.tableHistoryInput.EndAt != wantEnd ||
		store.tableHistoryInput.Limit != 720 {
		t.Fatalf("table history input = %+v", store.tableHistoryInput)
	}

	databaseRows, err := service.DatabaseHistory(context.Background(), DatabaseHistoryInput{
		StartAt: time.Date(2026, 7, 22, 15, 0, 0, 0, time.UTC),
		EndAt:   time.Date(2026, 7, 20, 15, 0, 0, 0, time.UTC),
		Limit:   10_001,
	})
	if err != nil {
		t.Fatalf("DatabaseHistory: %v", err)
	}
	if databaseRows == nil {
		t.Fatal("database history must return an array")
	}
	if store.databaseHistoryInput.StartAt != "2026-07-20T15:00:00.000Z" ||
		store.databaseHistoryInput.EndAt != "2026-07-22T15:00:00.000Z" ||
		store.databaseHistoryInput.Limit != 10_000 {
		t.Fatalf("database history input = %+v", store.databaseHistoryInput)
	}
}

func TestServiceHistoryClampsExplicitRangeToThirtyDaysEndingAtRequestedEnd(t *testing.T) {
	store := &tableMonitorReaderStub{tableHistory: []port.ManagementTableStorageSnapshot{}}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Now: func() time.Time {
			t.Fatal("explicit ranges must not read the current clock")
			return time.Time{}
		},
	})

	_, err := service.TableHistory(context.Background(), TableHistoryInput{
		DatabaseRole: port.MonitoredDatabaseRoleStats,
		TableName:    "table_storage_snapshots",
		StartAt:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		EndAt:        time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("TableHistory: %v", err)
	}
	if store.tableHistoryInput.StartAt != "2026-06-22T00:00:00.000Z" || store.tableHistoryInput.EndAt != "2026-07-22T00:00:00.000Z" {
		t.Fatalf("clamped range = %s - %s", store.tableHistoryInput.StartAt, store.tableHistoryInput.EndAt)
	}
}

func TestServicePropagatesStoreErrorsWithoutFallback(t *testing.T) {
	want := errors.New("postgres unavailable")
	service := NewService(&tableMonitorReaderStub{overviewErr: want, tableHistoryErr: want, databaseHistoryErr: want})
	if _, err := service.Overview(context.Background(), OverviewInput{}); !errors.Is(err, want) {
		t.Fatalf("overview error = %v", err)
	}
	if _, err := service.TableHistory(context.Background(), TableHistoryInput{}); !errors.Is(err, want) {
		t.Fatalf("table history error = %v", err)
	}
	if _, err := service.DatabaseHistory(context.Background(), DatabaseHistoryInput{}); !errors.Is(err, want) {
		t.Fatalf("database history error = %v", err)
	}
}

type tableMonitorReaderStub struct {
	overview             port.ManagementTableStorageOverview
	overviewLimit        int
	overviewErr          error
	tableHistoryInput    port.ManagementTableStorageHistoryInput
	tableHistory         []port.ManagementTableStorageSnapshot
	tableHistoryErr      error
	databaseHistoryInput port.ManagementDatabaseStorageHistoryInput
	databaseHistory      []port.ManagementDatabaseStorageSnapshot
	databaseHistoryErr   error
}

func (s *tableMonitorReaderStub) GetManagementTableStorageOverview(_ context.Context, limit int) (port.ManagementTableStorageOverview, error) {
	s.overviewLimit = limit
	return s.overview, s.overviewErr
}

func (s *tableMonitorReaderStub) ListManagementTableStorageHistory(_ context.Context, input port.ManagementTableStorageHistoryInput) ([]port.ManagementTableStorageSnapshot, error) {
	s.tableHistoryInput = input
	return s.tableHistory, s.tableHistoryErr
}

func (s *tableMonitorReaderStub) ListManagementDatabaseStorageHistory(_ context.Context, input port.ManagementDatabaseStorageHistoryInput) ([]port.ManagementDatabaseStorageSnapshot, error) {
	s.databaseHistoryInput = input
	return s.databaseHistory, s.databaseHistoryErr
}

func int64Pointer(value int64) *int64 { return &value }
