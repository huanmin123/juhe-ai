package runtimelog

import (
	"context"
	"time"
)

type Mode string

const (
	ModeSQLite   Mode = "sqlite"
	ModePostgres Mode = "postgres"
)

type Config struct {
	OwnerID                string
	OwnerLease             time.Duration
	Mode                   Mode
	DatasetPath            string
	RuntimeLogDatabasePath string
	BusinessPath           string
	UsageCatalogPath       string
	StatsPath              string
	CodexShardRoot         string
	PostgresURL            string
	PostgresMaxConns       int
	PostgresMinConns       int
	LogDirectory           string
	FileEnabled            bool
	Once                   bool
	PollInterval           time.Duration
	RetentionInterval      time.Duration
	RetentionDays          int
	LogRetentionDays       int
	LogMaxFiles            int
	BatchSize              int
}

// OwnerLease is the current writer authority for one runtime-log data domain.
// FenceToken only increases when an expired lease is acquired by a new owner;
// it prevents a delayed former owner from committing after that handoff.
type OwnerLease struct {
	OwnerID    string
	FenceToken int64
}

type LogFileKind string

const (
	LogFileCurrent LogFileKind = "current"
	LogFileRotated LogFileKind = "rotated"
)

type LogFile struct {
	Path string
	Role string
	Kind LogFileKind
}

type Cursor struct {
	LogFile              string
	FileIdentity         string
	CursorOffset         int64
	LineNumber           int64
	FileSize             int64
	TruncationGeneration int64
	FileMtimeMs          int64
	LastReadAt           string
	LastErrorMessage     string
	CreatedAt            string
	UpdatedAt            string
}

type Record struct {
	ID           string
	LogFile      string
	LogOffset    int64
	LineNumber   int64
	Time         string
	Level        string
	TraceID      string
	Event        string
	Message      string
	ErrorMessage string
	RawJSON      string
	CreatedAt    string
}

type CleanupResult struct {
	RuntimeLogs       int64
	RuntimeLogCursors int64
	RotatedLogFiles   int64
}

type Store interface {
	FindCursor(ctx context.Context, logFile string) (*Cursor, error)
	FindCursorByIdentity(ctx context.Context, identity string) (*Cursor, error)
	ReplaceCursor(ctx context.Context, lease OwnerLease, displaced *Cursor, replacement Cursor) error
	CopyCursor(ctx context.Context, lease OwnerLease, cursor Cursor) error
	Commit(ctx context.Context, lease OwnerLease, records []Record, cursor Cursor, retentionCutoff time.Time) error
	Cleanup(ctx context.Context, lease OwnerLease, cutoff time.Time, batchSize int, maxBatches int) (CleanupResult, error)
	VerifyOwnerLease(ctx context.Context, lease OwnerLease) error
	// WithOwnerLeaseFence validates the lease and executes callback while the
	// Store's exclusive owner-row fencing scope remains held. Implementations
	// must not release the transaction/row lock until callback returns.
	WithOwnerLeaseFence(ctx context.Context, lease OwnerLease, callback func() error) error
	RuntimeRetentionDays(ctx context.Context, fallback int) (int, error)
	AcquireOwnerLease(ctx context.Context, ownerID string, duration time.Duration) (OwnerLease, bool, error)
	RenewOwnerLease(ctx context.Context, lease OwnerLease, duration time.Duration) (bool, error)
	ReleaseOwnerLease(ctx context.Context, lease OwnerLease) error
	CheckSchema(ctx context.Context) error
	Close() error
}
