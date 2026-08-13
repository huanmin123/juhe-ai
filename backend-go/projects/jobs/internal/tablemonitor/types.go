package tablemonitor

import "time"

type Mode string

const (
	ModeSQLite   Mode = "sqlite"
	ModePostgres Mode = "postgres"
)

type Config struct {
	InstanceID           string
	OwnerLease           time.Duration
	Mode                 Mode
	OutputPath           string
	BusinessPath         string
	DatasetPath          string
	UsageCatalogPath     string
	StatsPath            string
	CodexShardRoot       string
	PostgresURL          string
	Interval             time.Duration
	RunTimeout           time.Duration
	RetentionDays        int
	MaxTables            int
	MaxConcurrentSources int
	RetentionBatchSize   int
	RetentionMaxBatches  int
}

// OwnerLease identifies the Go process currently allowed to mutate table
// monitor snapshots. FenceToken changes on every ownership handoff.
type OwnerLease struct {
	OwnerID    string
	FenceToken int64
}

type DatabaseSnapshot struct {
	Role          string
	Path          string
	SampledAt     time.Time
	FileBytes     *int64
	WALBytes      *int64
	SHMBytes      *int64
	PageSize      *int64
	PageCount     *int64
	FreelistCount *int64
	UsedBytes     *int64
	FreeBytes     *int64
	TableCount    int
	IndexCount    int
}

type TableSnapshot struct {
	Role            string
	TableName       string
	SampledAt       time.Time
	TableKind       string
	ParentTableName *string
	IsPartition     bool
	IsArchive       bool
	RowCount        *int64
	TableBytes      *int64
	IndexBytes      *int64
	TotalBytes      *int64
	PageCount       *int64
	IndexCount      int
	GrowthBytes1h   *int64
	GrowthRows1h    *int64
	GrowthBytes24h  *int64
	GrowthRows24h   *int64
}

type SampleResult struct {
	SampledAt         time.Time
	DatabaseSnapshots int
	TableSnapshots    int
	DeletedSnapshots  int64
}
