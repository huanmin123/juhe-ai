package managementtablemonitor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultOverviewLimit = 200
	maxOverviewLimit     = 1000
	defaultHistoryLimit  = 720
	maxHistoryLimit      = 10000
	historyWindow        = 30 * 24 * time.Hour
	isoMilliseconds      = "2006-01-02T15:04:05.000Z"
)

type Service struct {
	store port.ManagementTableMonitorReader
	now   func() time.Time
}

type ServiceOptions struct {
	Store port.ManagementTableMonitorReader
	Now   func() time.Time
}

type OverviewInput struct {
	Limit int
}

type TableHistoryInput struct {
	DatabaseRole port.MonitoredDatabaseRole
	TableName    string
	StartAt      time.Time
	EndAt        time.Time
	Limit        int
}

type DatabaseHistoryInput struct {
	StartAt time.Time
	EndAt   time.Time
	Limit   int
}

type Overview struct {
	SampledAt string                         `json:"sampledAt,omitempty"`
	Databases []DatabaseStorageSnapshot      `json:"databases"`
	Tables    []TableStorageOverviewSnapshot `json:"tables"`
}

type DatabaseStorageSnapshot struct {
	DatabaseRole  port.MonitoredDatabaseRole `json:"databaseRole"`
	DatabasePath  string                     `json:"databasePath"`
	SampledAt     string                     `json:"sampledAt"`
	FileBytes     *int64                     `json:"fileBytes,omitempty"`
	WALBytes      *int64                     `json:"walBytes,omitempty"`
	SHMBytes      *int64                     `json:"shmBytes,omitempty"`
	PageSize      *int64                     `json:"pageSize,omitempty"`
	PageCount     *int64                     `json:"pageCount,omitempty"`
	FreelistCount *int64                     `json:"freelistCount,omitempty"`
	UsedBytes     *int64                     `json:"usedBytes,omitempty"`
	FreeBytes     *int64                     `json:"freeBytes,omitempty"`
	TableCount    *int64                     `json:"tableCount,omitempty"`
	IndexCount    *int64                     `json:"indexCount,omitempty"`
}

type TableStorageOverviewSnapshot struct {
	DatabaseRole      port.MonitoredDatabaseRole `json:"databaseRole"`
	TableName         string                     `json:"tableName"`
	SampledAt         string                     `json:"sampledAt"`
	TableKind         string                     `json:"tableKind,omitempty"`
	ParentTableName   string                     `json:"parentTableName,omitempty"`
	IsPartition       bool                       `json:"isPartition"`
	IsArchive         bool                       `json:"isArchive"`
	RowCount          *int64                     `json:"rowCount,omitempty"`
	TableBytes        *int64                     `json:"tableBytes,omitempty"`
	IndexBytes        *int64                     `json:"indexBytes,omitempty"`
	IndexToTableRatio *float64                   `json:"indexToTableRatio,omitempty"`
	TotalBytes        *int64                     `json:"totalBytes,omitempty"`
	GrowthBytes1H     *int64                     `json:"growthBytes1h,omitempty"`
	GrowthRows1H      *int64                     `json:"growthRows1h,omitempty"`
	GrowthBytes24H    *int64                     `json:"growthBytes24h,omitempty"`
	GrowthRows24H     *int64                     `json:"growthRows24h,omitempty"`
}

type TableStorageSnapshot struct {
	DatabaseRole      port.MonitoredDatabaseRole `json:"databaseRole"`
	TableName         string                     `json:"tableName"`
	SampledAt         string                     `json:"sampledAt"`
	TableKind         string                     `json:"tableKind,omitempty"`
	ParentTableName   string                     `json:"parentTableName,omitempty"`
	IsPartition       bool                       `json:"isPartition"`
	IsArchive         bool                       `json:"isArchive"`
	RowCount          *int64                     `json:"rowCount,omitempty"`
	TableBytes        *int64                     `json:"tableBytes,omitempty"`
	IndexBytes        *int64                     `json:"indexBytes,omitempty"`
	IndexToTableRatio *float64                   `json:"indexToTableRatio,omitempty"`
	IndexToTotalRatio *float64                   `json:"indexToTotalRatio,omitempty"`
	TotalBytes        *int64                     `json:"totalBytes,omitempty"`
	PageCount         *int64                     `json:"pageCount,omitempty"`
	IndexCount        int64                      `json:"indexCount"`
	GrowthBytes1H     *int64                     `json:"growthBytes1h,omitempty"`
	GrowthRows1H      *int64                     `json:"growthRows1h,omitempty"`
	GrowthBytes24H    *int64                     `json:"growthBytes24h,omitempty"`
	GrowthRows24H     *int64                     `json:"growthRows24h,omitempty"`
}

func NewService(store port.ManagementTableMonitorReader) *Service {
	return NewServiceWithOptions(ServiceOptions{Store: store})
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{store: opts.Store, now: now}
}

func (s *Service) Overview(ctx context.Context, input OverviewInput) (Overview, error) {
	if s.store == nil {
		return Overview{}, fmt.Errorf("management table monitor reader is required")
	}
	result, err := s.store.GetManagementTableStorageOverview(ctx, normalizedLimit(input.Limit, defaultOverviewLimit, maxOverviewLimit))
	if err != nil {
		return Overview{}, err
	}
	databases := make([]DatabaseStorageSnapshot, 0, len(result.Databases))
	for _, row := range result.Databases {
		databases = append(databases, databaseSnapshot(row))
	}
	tables := make([]TableStorageOverviewSnapshot, 0, len(result.Tables))
	for _, row := range result.Tables {
		tables = append(tables, tableOverviewSnapshot(row))
	}
	return Overview{SampledAt: result.SampledAt, Databases: databases, Tables: tables}, nil
}

func (s *Service) TableHistory(ctx context.Context, input TableHistoryInput) ([]TableStorageSnapshot, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management table monitor reader is required")
	}
	startAt, endAt := s.normalizedRange(input.StartAt, input.EndAt)
	rows, err := s.store.ListManagementTableStorageHistory(ctx, port.ManagementTableStorageHistoryInput{
		DatabaseRole: input.DatabaseRole,
		TableName:    trimECMAScriptWhitespace(input.TableName),
		StartAt:      startAt,
		EndAt:        endAt,
		Limit:        normalizedLimit(input.Limit, defaultHistoryLimit, maxHistoryLimit),
	})
	if err != nil {
		return nil, err
	}
	result := make([]TableStorageSnapshot, 0, len(rows))
	for _, row := range rows {
		result = append(result, tableSnapshot(row))
	}
	return result, nil
}

func (s *Service) DatabaseHistory(ctx context.Context, input DatabaseHistoryInput) ([]DatabaseStorageSnapshot, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management table monitor reader is required")
	}
	startAt, endAt := s.normalizedRange(input.StartAt, input.EndAt)
	rows, err := s.store.ListManagementDatabaseStorageHistory(ctx, port.ManagementDatabaseStorageHistoryInput{
		StartAt: startAt,
		EndAt:   endAt,
		Limit:   normalizedLimit(input.Limit, defaultHistoryLimit, maxHistoryLimit),
	})
	if err != nil {
		return nil, err
	}
	result := make([]DatabaseStorageSnapshot, 0, len(rows))
	for _, row := range rows {
		result = append(result, databaseSnapshot(row))
	}
	return result, nil
}

func (s *Service) normalizedRange(startAt time.Time, endAt time.Time) (string, string) {
	if startAt.IsZero() || endAt.IsZero() {
		defaultEnd := s.now().UTC().Truncate(time.Millisecond)
		if startAt.IsZero() {
			startAt = defaultEnd.Add(-historyWindow)
		}
		if endAt.IsZero() {
			endAt = defaultEnd
		}
	}
	startAt = startAt.UTC().Truncate(time.Millisecond)
	endAt = endAt.UTC().Truncate(time.Millisecond)
	if startAt.After(endAt) {
		startAt, endAt = endAt, startAt
	}
	if endAt.Sub(startAt) > historyWindow {
		startAt = endAt.Add(-historyWindow)
	}
	return startAt.Format(isoMilliseconds), endAt.Format(isoMilliseconds)
}

func normalizedLimit(value int, fallback int, maximum int) int {
	if value <= 0 {
		return fallback
	}
	return min(value, maximum)
}

func databaseSnapshot(row port.ManagementDatabaseStorageSnapshot) DatabaseStorageSnapshot {
	return DatabaseStorageSnapshot{
		DatabaseRole: row.DatabaseRole, DatabasePath: row.DatabasePath, SampledAt: row.SampledAt,
		FileBytes: row.FileBytes, WALBytes: row.WALBytes, SHMBytes: row.SHMBytes,
		PageSize: row.PageSize, PageCount: row.PageCount, FreelistCount: row.FreelistCount,
		UsedBytes: row.UsedBytes, FreeBytes: row.FreeBytes, TableCount: row.TableCount, IndexCount: row.IndexCount,
	}
}

func tableOverviewSnapshot(row port.ManagementTableStorageSnapshot) TableStorageOverviewSnapshot {
	return TableStorageOverviewSnapshot{
		DatabaseRole: row.DatabaseRole, TableName: row.TableName, SampledAt: row.SampledAt,
		TableKind: optionalText(row.TableKind), ParentTableName: optionalText(row.ParentTableName),
		IsPartition: row.IsPartition, IsArchive: row.IsArchive, RowCount: row.RowCount,
		TableBytes: row.TableBytes, IndexBytes: row.IndexBytes, IndexToTableRatio: ratio(row.IndexBytes, row.TableBytes),
		TotalBytes: row.TotalBytes, GrowthBytes1H: row.GrowthBytes1H, GrowthRows1H: row.GrowthRows1H,
		GrowthBytes24H: row.GrowthBytes24H, GrowthRows24H: row.GrowthRows24H,
	}
}

func tableSnapshot(row port.ManagementTableStorageSnapshot) TableStorageSnapshot {
	return TableStorageSnapshot{
		DatabaseRole: row.DatabaseRole, TableName: row.TableName, SampledAt: row.SampledAt,
		TableKind: optionalText(row.TableKind), ParentTableName: optionalText(row.ParentTableName),
		IsPartition: row.IsPartition, IsArchive: row.IsArchive, RowCount: row.RowCount,
		TableBytes: row.TableBytes, IndexBytes: row.IndexBytes, IndexToTableRatio: ratio(row.IndexBytes, row.TableBytes),
		IndexToTotalRatio: ratio(row.IndexBytes, row.TotalBytes), TotalBytes: row.TotalBytes,
		PageCount: row.PageCount, IndexCount: row.IndexCount,
		GrowthBytes1H: row.GrowthBytes1H, GrowthRows1H: row.GrowthRows1H,
		GrowthBytes24H: row.GrowthBytes24H, GrowthRows24H: row.GrowthRows24H,
	}
}

func optionalText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func ratio(numerator *int64, denominator *int64) *float64 {
	if numerator == nil || denominator == nil || *denominator <= 0 {
		return nil
	}
	value := float64(*numerator) / float64(*denominator)
	return &value
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
