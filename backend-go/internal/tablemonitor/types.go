package tablemonitor

import "time"

type Mode string

const (
	ModeSQLite   Mode = "sqlite"
	ModePostgres Mode = "postgres"
)

type Config struct {
	InstanceID       string
	Mode             Mode
	OutputPath       string
	BusinessPath     string
	DatasetPath      string
	UsageCatalogPath string
	StatsPath        string
	CodexShardRoot   string
	PostgresURL      string
	Interval         time.Duration
	RetentionDays    int
	MaxTables        int
}

type DatabaseSnapshot struct {
	Role          string
	Path          string
	SampledAt     time.Time
	FileBytes     *int64
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
}

type SampleResult struct {
	SampledAt         time.Time
	DatabaseSnapshots int
	TableSnapshots    int
	DeletedSnapshots  int64
}
