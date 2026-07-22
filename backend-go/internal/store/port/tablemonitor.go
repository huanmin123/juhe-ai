package port

import "context"

type MonitoredDatabaseRole string

const (
	MonitoredDatabaseRoleBusiness          MonitoredDatabaseRole = "business"
	MonitoredDatabaseRoleDataset           MonitoredDatabaseRole = "dataset"
	MonitoredDatabaseRoleUsageCatalog      MonitoredDatabaseRole = "usage-catalog"
	MonitoredDatabaseRoleStats             MonitoredDatabaseRole = "stats"
	MonitoredDatabaseRoleCodexContextState MonitoredDatabaseRole = "codex-context-state"
)

type ManagementDatabaseStorageSnapshot struct {
	DatabaseRole  MonitoredDatabaseRole
	DatabasePath  string
	SampledAt     string
	FileBytes     *int64
	WALBytes      *int64
	SHMBytes      *int64
	PageSize      *int64
	PageCount     *int64
	FreelistCount *int64
	UsedBytes     *int64
	FreeBytes     *int64
	TableCount    *int64
	IndexCount    *int64
}

type ManagementTableStorageSnapshot struct {
	DatabaseRole    MonitoredDatabaseRole
	TableName       string
	SampledAt       string
	TableKind       *string
	ParentTableName *string
	IsPartition     bool
	IsArchive       bool
	RowCount        *int64
	TableBytes      *int64
	IndexBytes      *int64
	TotalBytes      *int64
	PageCount       *int64
	IndexCount      int64
	GrowthBytes1H   *int64
	GrowthRows1H    *int64
	GrowthBytes24H  *int64
	GrowthRows24H   *int64
}

type ManagementTableStorageOverview struct {
	SampledAt string
	Databases []ManagementDatabaseStorageSnapshot
	Tables    []ManagementTableStorageSnapshot
}

type ManagementTableStorageHistoryInput struct {
	DatabaseRole MonitoredDatabaseRole
	TableName    string
	StartAt      string
	EndAt        string
	Limit        int
}

type ManagementDatabaseStorageHistoryInput struct {
	StartAt string
	EndAt   string
	Limit   int
}

type ManagementTableMonitorReader interface {
	GetManagementTableStorageOverview(ctx context.Context, limit int) (ManagementTableStorageOverview, error)
	ListManagementTableStorageHistory(ctx context.Context, input ManagementTableStorageHistoryInput) ([]ManagementTableStorageSnapshot, error)
	ListManagementDatabaseStorageHistory(ctx context.Context, input ManagementDatabaseStorageHistoryInput) ([]ManagementDatabaseStorageSnapshot, error)
}
