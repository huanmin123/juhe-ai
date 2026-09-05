// Package tablemonitor ports the read half of the table-storage-monitor route
// family (Node backend/src/modules/table-monitor/table-monitor.routes.ts +
// storage/table-monitor.repository.ts): overview, per-table history and
// database history over the table_storage_snapshots /
// database_storage_snapshots tables. PostgreSQL reads juhe_stats.* on the
// shared pool; SQLite opens the dedicated table-monitor database (the same
// file the jobs sampler writes).
//
// The cleanup write (POST /non-business-data/cleanup) stays Node-owned per
// the W6 record (docs/migration/W6-管理端表监控只读Schema共存记录.md): the
// Go gateway has no record-maintenance worker channel.
package tablemonitor

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
)

// ErrSchemaUnavailable is the W6 typed-unavailable outcome: the snapshot
// tables are missing (fresh boot before the sampler/jobs owner ran). The
// routes render it 503 and never fake empty results.
var ErrSchemaUnavailable = errors.New("表存储监控快照库暂不可用")

// monitoredDatabaseRoles mirror the MonitoredDatabaseRole list.
var monitoredDatabaseRoles = []string{"business", "dataset", "usage-catalog", "stats", "codex-context-state"}

const (
	tableMonitorHistoryWindowDays   = 30
	defaultTableStorageHistoryLimit = 720
	maxHistoryPointsPerSeries       = 2000
)

// Store is the snapshot read surface.
type Store struct {
	db  *sql.DB
	pg  bool
	now func() (string, int64) // nowIso() and its epoch millis
}

// NewStore builds the table-monitor read store. The clock pair mirrors the
// Node nowIso() helper; zero time keeps the default window anchored at the
// process clock.
func NewStore(db *sql.DB, postgres bool) (*Store, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	clock := func() (string, int64) {
		now := time.Now()
		return now.UTC().Format("2006-01-02T15:04:05.000Z"), now.UnixMilli()
	}
	return &Store{db: db, pg: postgres, now: clock}, nil
}

func (s *Store) table(name string) string {
	if s.pg {
		return "juhe_stats." + name
	}
	return name
}

// isSchemaMissing maps the driver shapes of missing snapshot tables/columns
// onto the typed unavailable outcome (SQLite "no such table", PostgreSQL
// SQLSTATE 42P01/42703).
func isSchemaMissing(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "no such table") ||
		strings.Contains(message, "no such column") ||
		strings.Contains(message, "42P01") ||
		strings.Contains(message, "42703") ||
		strings.Contains(message, "does not exist")
}

// DatabaseSnapshot mirrors DatabaseStorageSnapshotSummary.
type DatabaseSnapshot struct {
	DatabaseRole string   `json:"databaseRole"`
	DatabasePath string   `json:"databasePath"`
	SampledAt    string   `json:"sampledAt"`
	FileBytes    *float64 `json:"fileBytes,omitempty"`
	WalBytes     *float64 `json:"walBytes,omitempty"`
	ShmBytes     *float64 `json:"shmBytes,omitempty"`
	FreeBytes    *float64 `json:"freeBytes,omitempty"`
	TableCount   *float64 `json:"tableCount,omitempty"`
}

// TableSnapshot mirrors TableStorageOverviewSummary.
type TableSnapshot struct {
	DatabaseRole      string   `json:"databaseRole"`
	TableName         string   `json:"tableName"`
	SampledAt         string   `json:"sampledAt"`
	TableKind         *string  `json:"tableKind,omitempty"`
	ParentTableName   *string  `json:"parentTableName,omitempty"`
	IsPartition       bool     `json:"isPartition"`
	IsArchive         bool     `json:"isArchive"`
	RowCount          *float64 `json:"rowCount,omitempty"`
	TableBytes        *float64 `json:"tableBytes,omitempty"`
	IndexBytes        *float64 `json:"indexBytes,omitempty"`
	IndexToTableRatio *float64 `json:"indexToTableRatio,omitempty"`
	TotalBytes        *float64 `json:"totalBytes,omitempty"`
	GrowthBytes1h     *float64 `json:"growthBytes1h,omitempty"`
	GrowthRows1h      *float64 `json:"growthRows1h,omitempty"`
	GrowthBytes24h    *float64 `json:"growthBytes24h,omitempty"`
	GrowthRows24h     *float64 `json:"growthRows24h,omitempty"`
}

// Overview mirrors TableStorageOverview.
type Overview struct {
	SampledAt *string            `json:"sampledAt,omitempty"`
	Databases []DatabaseSnapshot `json:"databases"`
	Tables    []TableSnapshot    `json:"tables"`
	Page      int                `json:"page"`
	PageSize  int                `json:"pageSize"`
	Total     int                `json:"total"`
	HasMore   bool               `json:"hasMore"`
}

type row map[string]any

func (r row) text(key string) string {
	if value, ok := r[key]; ok && value != nil {
		switch typed := value.(type) {
		case string:
			return typed
		case []byte:
			return string(typed)
		}
	}
	return ""
}

func (r row) nullText(key string) *string {
	value, ok := r[key]
	if !ok || value == nil {
		return nil
	}
	text := r.text(key)
	return &text
}

func (r row) number(key string) *float64 {
	value, ok := r[key]
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case int64:
		out := float64(typed)
		return &out
	case float64:
		return &typed
	case string:
		parsed := parseFloat(typed)
		if parsed == nil {
			return nil
		}
		return parsed
	}
	return nil
}

func parseFloat(text string) *float64 {
	var out float64
	var trimmed strings.Builder
	trimmed.WriteString(strings.TrimSpace(text))
	if trimmed.Len() == 0 {
		return nil
	}
	for _, char := range trimmed.String() {
		if (char < '0' || char > '9') && char != '.' && char != '-' && char != '+' && char != 'e' && char != 'E' {
			return nil
		}
	}
	out = parseNumber(trimmed.String())
	return &out
}

func parseNumber(text string) float64 {
	neg := false
	if strings.HasPrefix(text, "-") {
		neg = true
		text = text[1:]
	}
	value := 0.0
	intPart, fracPart, _ := strings.Cut(text, ".")
	for _, char := range intPart {
		if char >= '0' && char <= '9' {
			value = value*10 + float64(char-'0')
		}
	}
	if fracPart != "" {
		scale := 0.1
		for _, char := range fracPart {
			if char >= '0' && char <= '9' {
				value += float64(char-'0') * scale
				scale /= 10
			}
		}
	}
	if neg {
		return -value
	}
	return value
}

func (r row) boolean(key string) bool {
	switch typed := r[key].(type) {
	case bool:
		return typed
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	case string:
		return typed == "1" || strings.EqualFold(typed, "true")
	default:
		return false
	}
}

// escapeLikePrefix mirrors escapeLikePrefix.
func escapeLikePrefix(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

// roleRank mirrors databaseRoleSortRank.
func roleRank(role string) int {
	for index, candidate := range monitoredDatabaseRoles {
		if candidate == role {
			return index
		}
	}
	return len(monitoredDatabaseRoles)
}

// LoadOverview mirrors getTableStorageOverview (the read part).
func (s *Store) LoadOverview(ctx context.Context, page, pageSize int, keyword string) (Overview, error) {
	offset := (page - 1) * pageSize
	keyword = strings.TrimSpace(keyword)
	keywordClause := ""
	keywordParams := []any{}
	if keyword != "" {
		keywordClause = " AND lower(table_name) LIKE lower(?) ESCAPE '\\'"
		keywordParams = append(keywordParams, escapeLikePrefix(keyword)+"%")
	}
	if s.pg {
		return s.loadOverviewPostgres(ctx, page, pageSize, offset, keywordClause, keywordParams)
	}
	return s.loadOverviewSQLite(ctx, page, pageSize, offset, keywordClause, keywordParams)
}

func (s *Store) loadOverviewPostgres(ctx context.Context, page, pageSize, offset int, keywordClause string, keywordParams []any) (Overview, error) {
	databaseRows, err := s.query(ctx, `
		SELECT DISTINCT ON (database_role)
			database_role, database_path, sampled_at, file_bytes, wal_bytes, shm_bytes, free_bytes, table_count
		FROM `+s.table("database_storage_snapshots")+`
		ORDER BY database_role, sampled_at DESC, id DESC
	`)
	if err != nil {
		return Overview{}, err
	}
	databases := make([]DatabaseSnapshot, 0, len(databaseRows))
	for _, row := range databaseRows {
		databases = append(databases, databaseSnapshotFromRow(row))
	}
	sortDatabaseSnapshots(databases)
	countRows, err := s.query(ctx, `
		SELECT COUNT(*) AS total
		FROM (
			SELECT database_role, table_name
			FROM `+s.table("table_storage_snapshots")+`
			WHERE 1 = 1`+keywordClause+`
			GROUP BY database_role, table_name
		) AS table_keys
	`, keywordParams...)
	if err != nil {
		return Overview{}, err
	}
	total := 0
	if len(countRows) > 0 {
		if value := countRows[0].number("total"); value != nil {
			total = int(*value)
		}
	}
	tableRows, err := s.query(ctx, `
		WITH latest_snapshots AS (
			SELECT DISTINCT ON (database_role, table_name)
				id, database_role, table_name, sampled_at, table_kind, parent_table_name, is_partition, is_archive,
				row_count, table_bytes, index_bytes, total_bytes, growth_bytes_1h, growth_rows_1h,
				growth_bytes_24h, growth_rows_24h
			FROM `+s.table("table_storage_snapshots")+`
			WHERE 1 = 1`+keywordClause+`
			ORDER BY database_role, table_name, sampled_at DESC, id DESC
		)
		SELECT database_role, table_name, sampled_at, table_kind, parent_table_name, is_partition, is_archive,
			row_count, table_bytes, index_bytes, total_bytes, growth_bytes_1h, growth_rows_1h,
			growth_bytes_24h, growth_rows_24h
		FROM latest_snapshots
		ORDER BY total_bytes DESC NULLS LAST, row_count DESC NULLS LAST, table_name ASC, database_role ASC
		LIMIT ? OFFSET ?
	`, append(append([]any{}, keywordParams...), pageSize, offset)...)
	if err != nil {
		return Overview{}, err
	}
	tables := make([]TableSnapshot, 0, len(tableRows))
	for _, row := range tableRows {
		tables = append(tables, tableSnapshotFromRow(row))
	}
	return Overview{
		SampledAt: latestSampledAt(databases),
		Databases: databases,
		Tables:    tables,
		Page:      page,
		PageSize:  pageSize,
		Total:     total,
		HasMore:   offset+len(tables) < total,
	}, nil
}

func (s *Store) loadOverviewSQLite(ctx context.Context, page, pageSize, offset int, keywordClause string, keywordParams []any) (Overview, error) {
	databaseRows, err := s.query(ctx, `
		SELECT database_role, database_path, sampled_at, file_bytes, wal_bytes, shm_bytes, free_bytes, table_count
		FROM database_storage_snapshots AS snapshots
		WHERE snapshots.id = (
			SELECT latest.id
			FROM database_storage_snapshots AS latest
			WHERE latest.database_role = snapshots.database_role
			ORDER BY latest.sampled_at DESC, latest.id DESC
			LIMIT 1
		)
	`)
	if err != nil {
		return Overview{}, err
	}
	databases := make([]DatabaseSnapshot, 0, len(databaseRows))
	for _, row := range databaseRows {
		databases = append(databases, databaseSnapshotFromRow(row))
	}
	sortDatabaseSnapshots(databases)
	countRows, err := s.query(ctx, `
		SELECT COUNT(*) AS total
		FROM (
			SELECT database_role, table_name
			FROM table_storage_snapshots
			WHERE 1 = 1`+keywordClause+`
			GROUP BY database_role, table_name
		)
	`, keywordParams...)
	if err != nil {
		return Overview{}, err
	}
	total := 0
	if len(countRows) > 0 {
		if value := countRows[0].number("total"); value != nil {
			total = int(*value)
		}
	}
	tableRows, err := s.query(ctx, `
		WITH table_keys AS (
			SELECT database_role, table_name
			FROM table_storage_snapshots
			WHERE 1 = 1`+keywordClause+`
			GROUP BY database_role, table_name
		), latest_ids AS (
			SELECT (
				SELECT latest.id
				FROM table_storage_snapshots AS latest
				WHERE latest.database_role = table_keys.database_role
					AND latest.table_name = table_keys.table_name
				ORDER BY latest.sampled_at DESC, latest.id DESC
				LIMIT 1
			) AS id
			FROM table_keys
		)
		SELECT snapshots.database_role, snapshots.table_name, snapshots.sampled_at, snapshots.table_kind,
			snapshots.parent_table_name, snapshots.is_partition, snapshots.is_archive, snapshots.row_count,
			snapshots.table_bytes, snapshots.index_bytes, snapshots.total_bytes, snapshots.growth_bytes_1h,
			snapshots.growth_rows_1h, snapshots.growth_bytes_24h, snapshots.growth_rows_24h
		FROM table_storage_snapshots AS snapshots
		WHERE snapshots.id IN (SELECT id FROM latest_ids WHERE id IS NOT NULL)
		ORDER BY snapshots.total_bytes DESC, snapshots.row_count DESC, snapshots.table_name ASC, snapshots.database_role ASC
		LIMIT ? OFFSET ?
	`, append(append([]any{}, keywordParams...), pageSize, offset)...)
	if err != nil {
		return Overview{}, err
	}
	tables := make([]TableSnapshot, 0, len(tableRows))
	for _, row := range tableRows {
		tables = append(tables, tableSnapshotFromRow(row))
	}
	return Overview{
		SampledAt: latestSampledAt(databases),
		Databases: databases,
		Tables:    tables,
		Page:      page,
		PageSize:  pageSize,
		Total:     total,
		HasMore:   offset+len(tables) < total,
	}, nil
}

func databaseSnapshotFromRow(r row) DatabaseSnapshot {
	return DatabaseSnapshot{
		DatabaseRole: r.text("database_role"),
		DatabasePath: basename(r.text("database_path")),
		SampledAt:    r.text("sampled_at"),
		FileBytes:    r.number("file_bytes"),
		WalBytes:     r.number("wal_bytes"),
		ShmBytes:     r.number("shm_bytes"),
		FreeBytes:    r.number("free_bytes"),
		TableCount:   r.number("table_count"),
	}
}

func tableSnapshotFromRow(r row) TableSnapshot {
	snapshot := TableSnapshot{
		DatabaseRole:    r.text("database_role"),
		TableName:       r.text("table_name"),
		SampledAt:       r.text("sampled_at"),
		TableKind:       r.nullText("table_kind"),
		ParentTableName: r.nullText("parent_table_name"),
		IsPartition:     r.boolean("is_partition"),
		IsArchive:       r.boolean("is_archive"),
		RowCount:        r.number("row_count"),
		TableBytes:      r.number("table_bytes"),
		IndexBytes:      r.number("index_bytes"),
		TotalBytes:      r.number("total_bytes"),
		GrowthBytes1h:   r.number("growth_bytes_1h"),
		GrowthRows1h:    r.number("growth_rows_1h"),
		GrowthBytes24h:  r.number("growth_bytes_24h"),
		GrowthRows24h:   r.number("growth_rows_24h"),
	}
	if tableBytes := snapshot.TableBytes; tableBytes != nil {
		if indexBytes := snapshot.IndexBytes; indexBytes != nil && *tableBytes > 0 {
			ratio := *indexBytes / *tableBytes
			snapshot.IndexToTableRatio = &ratio
		}
	}
	return snapshot
}

func sortDatabaseSnapshots(databases []DatabaseSnapshot) {
	sort.SliceStable(databases, func(left, right int) bool {
		return roleRank(databases[left].DatabaseRole) < roleRank(databases[right].DatabaseRole)
	})
}

func latestSampledAt(databases []DatabaseSnapshot) *string {
	var latest *string
	for index := range databases {
		if latest == nil || databases[index].SampledAt > *latest {
			value := databases[index].SampledAt
			latest = &value
		}
	}
	return latest
}

func basename(path string) string {
	if index := strings.LastIndexAny(path, `/\`); index >= 0 {
		return path[index+1:]
	}
	return path
}

// TableHistoryPoint mirrors TableStorageHistoryPoint.
type TableHistoryPoint struct {
	SampledAt  string   `json:"sampledAt"`
	RowCount   *float64 `json:"rowCount,omitempty"`
	TotalBytes *float64 `json:"totalBytes,omitempty"`
}

// DatabaseHistoryPoint mirrors DatabaseStorageHistoryPoint.
type DatabaseHistoryPoint struct {
	DatabaseRole string   `json:"databaseRole"`
	SampledAt    string   `json:"sampledAt"`
	FileBytes    *float64 `json:"fileBytes,omitempty"`
	WalBytes     *float64 `json:"walBytes,omitempty"`
	FreeBytes    *float64 `json:"freeBytes,omitempty"`
	TableCount   *float64 `json:"tableCount,omitempty"`
}

// LoadTableHistory mirrors listTableStorageHistory.
func (s *Store) LoadTableHistory(ctx context.Context, databaseRole, tableName, startAt, endAt string, limit int) ([]TableHistoryPoint, error) {
	rows, err := s.query(ctx, `
		SELECT sampled_at, row_count, total_bytes
		FROM `+s.table("table_storage_snapshots")+`
		WHERE database_role = ?
			AND table_name = ?
			AND sampled_at >= ?
			AND sampled_at <= ?
		ORDER BY sampled_at DESC
		LIMIT ?
	`, databaseRole, tableName, startAt, endAt, limit)
	if err != nil {
		return nil, err
	}
	points := make([]TableHistoryPoint, 0, len(rows))
	for _, row := range rows {
		points = append(points, TableHistoryPoint{
			SampledAt:  row.text("sampled_at"),
			RowCount:   row.number("row_count"),
			TotalBytes: row.number("total_bytes"),
		})
	}
	// Node re-sorts ascending after the DESC read.
	for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
		points[i], points[j] = points[j], points[i]
	}
	return points, nil
}

// LoadDatabaseHistory mirrors listDatabaseStorageHistory: per-role bounded
// reads merged ascending with the role tiebreak.
func (s *Store) LoadDatabaseHistory(ctx context.Context, startAt, endAt string, limit int) ([]DatabaseHistoryPoint, error) {
	points := []DatabaseHistoryPoint{}
	for _, role := range monitoredDatabaseRoles {
		rows, err := s.query(ctx, `
			SELECT database_role, sampled_at, file_bytes, wal_bytes, free_bytes, table_count
			FROM `+s.table("database_storage_snapshots")+`
			WHERE database_role = ?
				AND sampled_at >= ?
				AND sampled_at <= ?
			ORDER BY sampled_at DESC, id DESC
			LIMIT ?
		`, role, startAt, endAt, limit)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			points = append(points, DatabaseHistoryPoint{
				DatabaseRole: row.text("database_role"),
				SampledAt:    row.text("sampled_at"),
				FileBytes:    row.number("file_bytes"),
				WalBytes:     row.number("wal_bytes"),
				FreeBytes:    row.number("free_bytes"),
				TableCount:   row.number("table_count"),
			})
		}
	}
	sort.SliceStable(points, func(left, right int) bool {
		if points[left].SampledAt != points[right].SampledAt {
			return points[left].SampledAt < points[right].SampledAt
		}
		return roleRank(points[left].DatabaseRole) < roleRank(points[right].DatabaseRole)
	})
	return points, nil
}

func (s *Store) query(ctx context.Context, query string, args ...any) ([]row, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		if isSchemaMissing(err) {
			return nil, ErrSchemaUnavailable
		}
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	collected := []row{}
	for rows.Next() {
		values := make([]any, len(columns))
		scan := make([]any, len(columns))
		for index := range values {
			values[index] = new(any)
			scan[index] = values[index]
		}
		if err := rows.Scan(scan...); err != nil {
			return nil, err
		}
		entry := row{}
		for index, column := range columns {
			entry[column] = *(values[index].(*any))
		}
		collected = append(collected, entry)
	}
	return collected, rows.Err()
}
