package gometrics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type SQLDialect string

const (
	DialectSQLite   SQLDialect = "sqlite"
	DialectPostgres SQLDialect = "postgres"
)

var ErrTrendRangeTooLarge = errors.New("gometrics: trend range too large")

const (
	maxHourlyTrendRange = 90 * 24 * time.Hour
	maxDailyTrendRange  = 366 * 24 * time.Hour
	maxHourlyTrendRows  = 24*90 + 1
	maxDailyTrendRows   = 367
)

type runtimeSchemaContract struct {
	columns    []string
	postgres   map[string]struct{ dataType, udtName string }
	primaryKey []string
}

var runtimeSchemaContracts = map[string]runtimeSchemaContract{
	"go_runtime_metrics_samples":       {columns: []string{"service", "role", "runtime_kind", "process_pid", "sampled_at", "goroutines", "goroutines_runnable", "goroutines_waiting", "threads", "gomaxprocs", "heap_alloc_bytes", "heap_live_bytes", "heap_objects", "cpu_percent", "rss_bytes", "fd_count", "uptime_seconds"}, postgres: map[string]struct{ dataType, udtName string }{"service": {"text", "text"}, "role": {"text", "text"}, "runtime_kind": {"text", "text"}, "process_pid": {"bigint", "int8"}, "sampled_at": {"timestamp with time zone", "timestamptz"}, "goroutines": {"bigint", "int8"}, "goroutines_runnable": {"bigint", "int8"}, "goroutines_waiting": {"bigint", "int8"}, "threads": {"bigint", "int8"}, "gomaxprocs": {"bigint", "int8"}, "heap_alloc_bytes": {"bigint", "int8"}, "heap_live_bytes": {"bigint", "int8"}, "heap_objects": {"bigint", "int8"}, "cpu_percent": {"double precision", "float8"}, "rss_bytes": {"bigint", "int8"}, "fd_count": {"bigint", "int8"}, "uptime_seconds": {"double precision", "float8"}}, primaryKey: []string{"service", "role", "runtime_kind", "process_pid", "sampled_at"}},
	"go_runtime_metrics_hourly":        {columns: aggregateColumns(false), postgres: aggregateTypes(false), primaryKey: []string{"service", "role", "runtime_kind", "window_start"}},
	"go_runtime_metrics_trend_windows": {columns: aggregateColumns(true), postgres: aggregateTypes(true), primaryKey: []string{"service", "role", "runtime_kind", "window_start"}},
}

func aggregateColumns(daily bool) []string {
	r := []string{"service", "role", "runtime_kind", "window_start"}
	if daily {
		r = append(r, "window_end")
	}
	r = append(r, "sample_count", "avg_goroutines", "max_goroutines", "avg_goroutines_runnable", "max_goroutines_runnable", "avg_goroutines_waiting", "max_goroutines_waiting", "avg_threads", "max_threads", "avg_gomaxprocs", "max_gomaxprocs", "avg_heap_alloc_bytes", "max_heap_alloc_bytes", "avg_heap_live_bytes", "max_heap_live_bytes", "avg_heap_objects", "max_heap_objects", "avg_cpu_percent", "max_cpu_percent", "cpu_sample_count", "avg_rss_bytes", "max_rss_bytes", "rss_sample_count", "avg_fd_count", "max_fd_count", "fd_sample_count", "avg_uptime_seconds", "max_uptime_seconds")
	return r
}
func aggregateTypes(daily bool) map[string]struct{ dataType, udtName string } {
	m := map[string]struct{ dataType, udtName string }{"service": {"text", "text"}, "role": {"text", "text"}, "runtime_kind": {"text", "text"}, "window_start": {"timestamp with time zone", "timestamptz"}, "sample_count": {"bigint", "int8"}, "avg_goroutines": {"double precision", "float8"}, "max_goroutines": {"double precision", "float8"}, "avg_goroutines_runnable": {"double precision", "float8"}, "max_goroutines_runnable": {"double precision", "float8"}, "avg_goroutines_waiting": {"double precision", "float8"}, "max_goroutines_waiting": {"double precision", "float8"}, "avg_threads": {"double precision", "float8"}, "max_threads": {"double precision", "float8"}, "avg_gomaxprocs": {"double precision", "float8"}, "max_gomaxprocs": {"double precision", "float8"}, "avg_heap_alloc_bytes": {"double precision", "float8"}, "max_heap_alloc_bytes": {"double precision", "float8"}, "avg_heap_live_bytes": {"double precision", "float8"}, "max_heap_live_bytes": {"double precision", "float8"}, "avg_heap_objects": {"double precision", "float8"}, "max_heap_objects": {"double precision", "float8"}, "avg_cpu_percent": {"double precision", "float8"}, "max_cpu_percent": {"double precision", "float8"}, "cpu_sample_count": {"bigint", "int8"}, "avg_rss_bytes": {"double precision", "float8"}, "max_rss_bytes": {"double precision", "float8"}, "rss_sample_count": {"bigint", "int8"}, "avg_fd_count": {"double precision", "float8"}, "max_fd_count": {"double precision", "float8"}, "fd_sample_count": {"bigint", "int8"}, "avg_uptime_seconds": {"double precision", "float8"}, "max_uptime_seconds": {"double precision", "float8"}}
	if daily {
		m["window_end"] = struct{ dataType, udtName string }{"timestamp with time zone", "timestamptz"}
	}
	return m
}

type Store struct {
	db      *sql.DB
	dialect SQLDialect
}

func NewStore(db *sql.DB, d SQLDialect) (*Store, error) {
	if db == nil {
		return nil, errors.New("gometrics: nil database")
	}
	if d != DialectSQLite && d != DialectPostgres {
		return nil, fmt.Errorf("gometrics: unsupported SQL dialect %q", d)
	}
	return &Store{db: db, dialect: d}, nil
}
func (s *Store) tableName(t string) string {
	if s.dialect == DialectPostgres {
		return "juhe_stats." + t
	}
	return t
}
func (s *Store) placeholder(i int) string {
	if s.dialect == DialectPostgres {
		return fmt.Sprintf("$%d", i)
	}
	return "?"
}
func (s *Store) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("gometrics: nil store")
	}
	p, i, ts, r := "", "INTEGER", "TIMESTAMP", "REAL"
	if s.dialect == DialectPostgres {
		if _, e := s.db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS juhe_stats"); e != nil {
			return e
		}
		p = "juhe_stats."
		i = "BIGINT"
		ts = "TIMESTAMPTZ"
		r = "DOUBLE PRECISION"
	}
	stmts := []string{"CREATE TABLE IF NOT EXISTS " + p + "go_runtime_metrics_samples (service TEXT NOT NULL, role TEXT NOT NULL, runtime_kind TEXT NOT NULL DEFAULT 'go', process_pid " + i + " NOT NULL, sampled_at " + ts + " NOT NULL, goroutines " + i + " NOT NULL, goroutines_runnable " + i + " NOT NULL, goroutines_waiting " + i + " NOT NULL, threads " + i + " NOT NULL, gomaxprocs " + i + " NOT NULL, heap_alloc_bytes " + i + " NOT NULL, heap_live_bytes " + i + " NOT NULL, heap_objects " + i + " NOT NULL, cpu_percent " + r + ", rss_bytes " + i + ", fd_count " + i + ", uptime_seconds " + r + " NOT NULL, PRIMARY KEY(service,role,runtime_kind,process_pid,sampled_at))", "CREATE TABLE IF NOT EXISTS " + p + "go_runtime_metrics_hourly (service TEXT NOT NULL, role TEXT NOT NULL, runtime_kind TEXT NOT NULL DEFAULT 'go', window_start " + ts + " NOT NULL, sample_count " + i + " NOT NULL, avg_goroutines " + r + " NOT NULL, max_goroutines " + r + " NOT NULL, avg_heap_alloc_bytes " + r + " NOT NULL, max_heap_alloc_bytes " + r + " NOT NULL, avg_heap_live_bytes " + r + " NOT NULL, max_heap_live_bytes " + r + " NOT NULL, avg_heap_objects " + r + " NOT NULL, max_heap_objects " + r + " NOT NULL, avg_threads " + r + " NOT NULL, max_threads " + r + " NOT NULL, avg_cpu_percent " + r + ", max_cpu_percent " + r + ", avg_rss_bytes " + r + ", max_rss_bytes " + r + ", avg_fd_count " + r + ", max_fd_count " + r + ", avg_uptime_seconds " + r + " NOT NULL, max_uptime_seconds " + r + " NOT NULL, PRIMARY KEY(service,role,runtime_kind,window_start))", "CREATE TABLE IF NOT EXISTS " + p + "go_runtime_metrics_trend_windows (service TEXT NOT NULL, role TEXT NOT NULL, runtime_kind TEXT NOT NULL DEFAULT 'go', window_start " + ts + " NOT NULL, window_end " + ts + " NOT NULL, sample_count " + i + " NOT NULL, avg_goroutines " + r + " NOT NULL, max_goroutines " + r + " NOT NULL, avg_heap_alloc_bytes " + r + " NOT NULL, max_heap_alloc_bytes " + r + " NOT NULL, avg_heap_live_bytes " + r + " NOT NULL, max_heap_live_bytes " + r + " NOT NULL, avg_heap_objects " + r + " NOT NULL, max_heap_objects " + r + " NOT NULL, avg_threads " + r + " NOT NULL, max_threads " + r + " NOT NULL, avg_cpu_percent " + r + ", max_cpu_percent " + r + ", avg_rss_bytes " + r + ", max_rss_bytes " + r + ", avg_fd_count " + r + ", max_fd_count " + r + ", avg_uptime_seconds " + r + " NOT NULL, max_uptime_seconds " + r + " NOT NULL, PRIMARY KEY(service,role,runtime_kind,window_start))"}
	for _, q := range stmts {
		if _, e := s.db.ExecContext(ctx, q); e != nil {
			return fmt.Errorf("gometrics ensure schema: %w", e)
		}
	}
	// Samples are the only unbounded-by-window table. Retention runs by
	// sampled_at, so keep that maintenance path indexed without adding any
	// high-cardinality dimensions to the runtime contract.
	if _, e := s.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS "+p+"go_runtime_metrics_samples_sampled_at_idx ON "+p+"go_runtime_metrics_samples (sampled_at)"); e != nil {
		return fmt.Errorf("gometrics ensure schema samples index: %w", e)
	}
	if e := s.ensureMetricColumns(ctx, p, i, r); e != nil {
		return e
	}
	return nil
}

func (s *Store) ensureMetricColumns(ctx context.Context, prefix, integerType, realType string) error {
	additions := []struct{ table, name, definition string }{}
	for _, table := range []string{"go_runtime_metrics_hourly", "go_runtime_metrics_trend_windows"} {
		for _, name := range []string{"avg_goroutines_runnable", "max_goroutines_runnable", "avg_goroutines_waiting", "max_goroutines_waiting", "avg_gomaxprocs", "max_gomaxprocs"} {
			additions = append(additions, struct{ table, name, definition string }{table, name, realType + " NOT NULL DEFAULT 0"})
		}
		for _, name := range []string{"cpu_sample_count", "rss_sample_count", "fd_sample_count"} {
			additions = append(additions, struct{ table, name, definition string }{table, name, integerType + " NOT NULL DEFAULT 0"})
		}
	}
	for _, a := range additions {
		if s.dialect == DialectPostgres {
			if _, e := s.db.ExecContext(ctx, "ALTER TABLE "+prefix+a.table+" ADD COLUMN IF NOT EXISTS "+a.name+" "+a.definition); e != nil {
				return fmt.Errorf("gometrics add column %s.%s: %w", a.table, a.name, e)
			}
			continue
		}
		rows, e := s.db.QueryContext(ctx, "PRAGMA table_info("+a.table+")")
		if e != nil {
			return e
		}
		found := false
		for rows.Next() {
			var cid, nn, pk int
			var n, t string
			var d any
			if e = rows.Scan(&cid, &n, &t, &nn, &d, &pk); e != nil {
				rows.Close()
				return e
			}
			if n == a.name {
				found = true
			}
		}
		rows.Close()
		if !found {
			if _, e = s.db.ExecContext(ctx, "ALTER TABLE "+a.table+" ADD COLUMN "+a.name+" "+a.definition); e != nil {
				return fmt.Errorf("gometrics add column %s.%s: %w", a.table, a.name, e)
			}
		}
	}
	return nil
}
func (s *Store) CheckSchema(ctx context.Context) error {
	for _, t := range []string{"go_runtime_metrics_samples", "go_runtime_metrics_hourly", "go_runtime_metrics_trend_windows"} {
		c := runtimeSchemaContracts[t]
		if s.dialect == DialectSQLite {
			if err := s.checkSQLiteTable(ctx, t, c); err != nil {
				return err
			}
			continue
		}
		if err := s.checkPostgresTable(ctx, t, c); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) checkSQLiteTable(ctx context.Context, table string, contract runtimeSchemaContract) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return fmt.Errorf("gometrics check schema %s: %w", table, err)
	}
	defer rows.Close()
	columns := make(map[string]sqliteColumn, len(contract.columns))
	primary := make([]string, 0, len(contract.primaryKey))
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return fmt.Errorf("gometrics check schema %s: %w", table, err)
		}
		columns[name] = sqliteColumn{typeName: typ, notNull: notNull != 0}
		if pk > 0 {
			for len(primary) <= pk-1 {
				primary = append(primary, "")
			}
			primary[pk-1] = name
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("gometrics check schema %s: %w", table, err)
	}
	if len(columns) == 0 {
		return fmt.Errorf("gometrics check schema %s: table is missing", table)
	}
	for _, column := range contract.columns {
		actual, ok := columns[column]
		if !ok {
			return fmt.Errorf("gometrics check schema %s: column %s is missing", table, column)
		}
		if expected, ok := contract.postgres[column]; ok && !sqliteTypeMatches(actual.typeName, expected) {
			return fmt.Errorf("gometrics check schema %s: column %s type mismatch", table, column)
		}
		if sqliteColumnMustBeNotNull(column) && !actual.notNull {
			return fmt.Errorf("gometrics check schema %s: column %s must be NOT NULL", table, column)
		}
	}
	if len(primary) != len(contract.primaryKey) {
		return fmt.Errorf("gometrics check schema %s: primary key mismatch", table)
	}
	for i, column := range contract.primaryKey {
		if primary[i] != column {
			return fmt.Errorf("gometrics check schema %s: primary key mismatch", table)
		}
	}
	return nil
}

func (s *Store) checkPostgresTable(ctx context.Context, table string, contract runtimeSchemaContract) error {
	rows, err := s.db.QueryContext(ctx, "SELECT column_name,data_type,udt_name,is_nullable FROM information_schema.columns WHERE table_schema='juhe_stats' AND table_name=$1", table)
	if err != nil {
		return fmt.Errorf("gometrics check schema %s: %w", table, err)
	}
	columns := make(map[string]postgresColumn, len(contract.columns))
	for rows.Next() {
		var name, dataType, udtName, nullable string
		if err := rows.Scan(&name, &dataType, &udtName, &nullable); err != nil {
			return fmt.Errorf("gometrics check schema %s: %w", table, err)
		}
		columns[name] = postgresColumn{dataType: dataType, udtName: udtName, notNull: nullable == "NO"}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("gometrics check schema %s: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("gometrics check schema %s: %w", table, err)
	}
	if len(columns) == 0 {
		return fmt.Errorf("gometrics check schema %s: table is missing", table)
	}
	for _, column := range contract.columns {
		actual, ok := columns[column]
		if !ok {
			return fmt.Errorf("gometrics check schema %s: column %s is missing", table, column)
		}
		expected, ok := contract.postgres[column]
		if !ok || actual.dataType != expected.dataType || actual.udtName != expected.udtName {
			return fmt.Errorf("gometrics check schema %s: column %s type mismatch", table, column)
		}
		if sqliteColumnMustBeNotNull(column) && !actual.notNull {
			return fmt.Errorf("gometrics check schema %s: column %s must be NOT NULL", table, column)
		}
	}
	pkRows, err := s.db.QueryContext(ctx, `SELECT kcu.column_name
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu ON kcu.constraint_name=tc.constraint_name
 AND kcu.table_schema=tc.table_schema AND kcu.table_name=tc.table_name
WHERE tc.table_schema='juhe_stats' AND tc.table_name=$1 AND tc.constraint_type='PRIMARY KEY'
ORDER BY kcu.ordinal_position`, table)
	if err != nil {
		return fmt.Errorf("gometrics check schema %s: %w", table, err)
	}
	primary := make([]string, 0, len(contract.primaryKey))
	for pkRows.Next() {
		var column string
		if err := pkRows.Scan(&column); err != nil {
			return fmt.Errorf("gometrics check schema %s: %w", table, err)
		}
		primary = append(primary, column)
	}
	if err := pkRows.Err(); err != nil {
		return fmt.Errorf("gometrics check schema %s: %w", table, err)
	}
	if err := pkRows.Close(); err != nil {
		return fmt.Errorf("gometrics check schema %s: %w", table, err)
	}
	if len(primary) != len(contract.primaryKey) {
		return fmt.Errorf("gometrics check schema %s: primary key mismatch", table)
	}
	for i, column := range contract.primaryKey {
		if primary[i] != column {
			return fmt.Errorf("gometrics check schema %s: primary key mismatch", table)
		}
	}
	return nil
}

type sqliteColumn struct {
	typeName string
	notNull  bool
}

type postgresColumn struct {
	dataType string
	udtName  string
	notNull  bool
}

func sqliteColumnMustBeNotNull(column string) bool {
	switch column {
	case "cpu_percent", "rss_bytes", "fd_count", "avg_cpu_percent", "max_cpu_percent", "avg_rss_bytes", "max_rss_bytes", "avg_fd_count", "max_fd_count":
		return false
	default:
		return true
	}
}

func sqliteTypeMatches(actual string, expected struct{ dataType, udtName string }) bool {
	actual = strings.ToUpper(strings.TrimSpace(actual))
	switch expected.udtName {
	case "text":
		return strings.Contains(actual, "CHAR") || strings.Contains(actual, "CLOB") || strings.Contains(actual, "TEXT")
	case "int8":
		return strings.Contains(actual, "INT")
	case "float8":
		return strings.Contains(actual, "REAL") || strings.Contains(actual, "FLOA") || strings.Contains(actual, "DOUB") || strings.Contains(actual, "NUM")
	case "timestamptz":
		return strings.Contains(actual, "TIME") || strings.Contains(actual, "DATE") || strings.Contains(actual, "TEXT")
	default:
		return true
	}
}
func (s *Store) InsertSnapshot(ctx context.Context, x RuntimeSnapshot) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("gometrics: nil store")
	}
	if x.SampledAt.IsZero() || x.Service == "" || x.Role == "" {
		return false, errors.New("gometrics: sample timestamp, service and role are required")
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	p := s.placeholder
	insertPrefix := "INSERT INTO "
	conflictSuffix := " ON CONFLICT DO NOTHING"
	if s.dialect == DialectSQLite {
		insertPrefix = "INSERT OR IGNORE INTO "
		conflictSuffix = ""
	}
	q := fmt.Sprintf(insertPrefix+"%s (service,role,runtime_kind,process_pid,sampled_at,goroutines,goroutines_runnable,goroutines_waiting,threads,gomaxprocs,heap_alloc_bytes,heap_live_bytes,heap_objects,cpu_percent,rss_bytes,fd_count,uptime_seconds) VALUES (%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s)"+conflictSuffix, s.tableName("go_runtime_metrics_samples"), p(1), p(2), p(3), p(4), p(5), p(6), p(7), p(8), p(9), p(10), p(11), p(12), p(13), p(14), p(15), p(16), p(17))
	r, e := tx.ExecContext(ctx, q, x.Service, x.Role, "go", x.ProcessPID, x.SampledAt.UTC(), x.Goroutines, x.GoroutinesRunnable, x.GoroutinesWaiting, x.Threads, x.GOMAXPROCS, x.HeapAllocBytes, x.HeapLiveBytes, x.HeapObjects, nullableFloat(x.CPUPercent), nullableUint(x.RSSBytes), nullableUint(x.FDCount), x.UptimeSeconds)
	if e != nil {
		return false, e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		_ = tx.Commit()
		return false, nil
	}
	if e = s.upsert(ctx, tx, "go_runtime_metrics_hourly", x.SampledAt.UTC().Truncate(time.Hour), x.SampledAt.UTC().Truncate(time.Hour).Add(time.Hour), x); e != nil {
		return false, e
	}
	d := utcDay(x.SampledAt)
	if e = s.upsert(ctx, tx, "go_runtime_metrics_trend_windows", d, d.Add(24*time.Hour), x); e != nil {
		return false, e
	}
	if e = tx.Commit(); e != nil {
		return false, e
	}
	return true, nil
}

func (s *Store) updateAggregate(ctx context.Context, tx *sql.Tx, table string, start time.Time, x RuntimeSnapshot) error {
	p := s.placeholder
	names := []string{"goroutines", "goroutines_runnable", "goroutines_waiting", "threads", "gomaxprocs", "heap_alloc_bytes", "heap_live_bytes", "heap_objects", "uptime_seconds"}
	vals := []float64{float64(x.Goroutines), float64(x.GoroutinesRunnable), float64(x.GoroutinesWaiting), float64(x.Threads), float64(x.GOMAXPROCS), float64(x.HeapAllocBytes), float64(x.HeapLiveBytes), float64(x.HeapObjects), x.UptimeSeconds}
	sets := []string{"sample_count=sample_count+1"}
	args := make([]any, 0, 40)
	idx := 1
	for i, n := range names {
		sets = append(sets,
			"avg_"+n+"=(avg_"+n+"*sample_count+"+p(idx)+")/(sample_count+1)",
			"max_"+n+"="+s.maxAggregateExpr("max_"+n, p(idx+1)))
		args = append(args, vals[i], vals[i])
		idx += 2
	}
	optional := []struct {
		name, countColumn string
		value             *float64
	}{{"cpu_percent", "cpu_sample_count", x.CPUPercent}, {"rss_bytes", "rss_sample_count", floatPointerFromUint(x.RSSBytes)}, {"fd_count", "fd_sample_count", floatPointerFromUint(x.FDCount)}}
	for _, metric := range optional {
		if metric.value == nil {
			continue
		}
		sets = append(sets,
			"avg_"+metric.name+"=(COALESCE(avg_"+metric.name+",0)*"+metric.countColumn+"+"+p(idx)+")/("+metric.countColumn+"+1)",
			"max_"+metric.name+"="+s.maxAggregateExpr("max_"+metric.name, p(idx+1)),
			metric.countColumn+"="+metric.countColumn+"+1")
		args = append(args, *metric.value, *metric.value)
		idx += 2
	}
	args = append(args, x.Service, x.Role, start)
	q := "UPDATE " + s.tableName(table) + " SET " + strings.Join(sets, ",") + " WHERE service=" + p(idx) + " AND role=" + p(idx+1) + " AND runtime_kind='go' AND window_start=" + p(idx+2)
	_, err := tx.ExecContext(ctx, q, args...)
	return err
}

func (s *Store) maxAggregateExpr(column, valuePlaceholder string) string {
	// All optional process gauges and runtime counters are non-negative. Using
	// a two-argument scalar max keeps each value bound exactly once, which is
	// important for SQLite's positional '?' parameters; COALESCE handles a
	// previously unavailable optional metric without turning it into zero data.
	if s.dialect == DialectPostgres {
		return "GREATEST(COALESCE(" + column + ",0)," + valuePlaceholder + ")"
	}
	return "MAX(COALESCE(" + column + ",0)," + valuePlaceholder + ")"
}

func floatPointerFromUint(value *uint64) *float64 {
	if value == nil {
		return nil
	}
	result := float64(*value)
	return &result
}
func nullableFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}
func nullableUint(v *uint64) any {
	if v == nil {
		return nil
	}
	return *v
}
func (s *Store) upsert(ctx context.Context, tx *sql.Tx, t string, start, end time.Time, x RuntimeSnapshot) error {
	p := s.placeholder
	vals := []any{x.Service, x.Role, "go", start, 1}
	cols := "service,role,runtime_kind,window_start,sample_count"
	if t == "go_runtime_metrics_trend_windows" {
		cols += ",window_end"
		vals = append(vals, end)
	}
	names := []string{"goroutines", "goroutines_runnable", "goroutines_waiting", "threads", "gomaxprocs", "heap_alloc_bytes", "heap_live_bytes", "heap_objects", "cpu_percent", "rss_bytes", "fd_count", "uptime_seconds"}
	v := []any{x.Goroutines, x.GoroutinesRunnable, x.GoroutinesWaiting, x.Threads, x.GOMAXPROCS, x.HeapAllocBytes, x.HeapLiveBytes, x.HeapObjects, nullableFloat(x.CPUPercent), nullableUint(x.RSSBytes), nullableUint(x.FDCount), x.UptimeSeconds}
	for i, n := range names {
		cols += ",avg_" + n + ",max_" + n
		vals = append(vals, v[i], v[i])
	}
	cols += ",cpu_sample_count,rss_sample_count,fd_sample_count"
	cpuCount, rssCount, fdCount := 0, 0, 0
	if x.CPUPercent != nil {
		cpuCount = 1
	}
	if x.RSSBytes != nil {
		rssCount = 1
	}
	if x.FDCount != nil {
		fdCount = 1
	}
	vals = append(vals, cpuCount, rssCount, fdCount)
	ph := make([]string, len(vals))
	for i := range ph {
		ph[i] = p(i + 1)
	}
	insertPrefix := "INSERT INTO "
	conflictSuffix := " ON CONFLICT DO NOTHING"
	if s.dialect == DialectSQLite {
		insertPrefix = "INSERT OR IGNORE INTO "
		conflictSuffix = ""
	}
	q := fmt.Sprintf(insertPrefix+"%s (%s) VALUES (%s)"+conflictSuffix, s.tableName(t), cols, strings.Join(ph, ","))
	r, e := tx.ExecContext(ctx, q, vals...)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n > 0 {
		return nil
	}
	return s.updateAggregate(ctx, tx, t, start, x)
}
func (s *Store) Record(ctx context.Context, x RuntimeSnapshot) (bool, error) {
	return s.InsertSnapshot(ctx, x)
}
func (s *Store) WriteSnapshot(ctx context.Context, x RuntimeSnapshot) (bool, error) {
	return s.InsertSnapshot(ctx, x)
}
func (s *Store) PruneBefore(ctx context.Context, c time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("gometrics: nil store")
	}
	for _, v := range [][2]string{{"go_runtime_metrics_samples", "sampled_at"}, {"go_runtime_metrics_hourly", "window_start"}, {"go_runtime_metrics_trend_windows", "window_end"}} {
		if _, e := s.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE %s < %s", s.tableName(v[0]), v[1], s.placeholder(1)), c.UTC()); e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) QueryTrend(ctx context.Context, service, role string, from, to time.Time) ([]WindowAggregate, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("gometrics: nil store")
	}
	if to.IsZero() {
		to = time.Now().UTC()
	}
	if from.IsZero() {
		from = to.Add(-24 * time.Hour)
	}
	if !from.Before(to) {
		return []WindowAggregate{}, nil
	}
	if to.Sub(from) > maxHourlyTrendRange {
		return nil, ErrTrendRangeTooLarge
	}
	return s.query(ctx, "go_runtime_metrics_hourly", service, role, from, to)
}
func (s *Store) QueryTrendWindows(ctx context.Context, service, role string, from, to time.Time) ([]WindowAggregate, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("gometrics: nil store")
	}
	if to.IsZero() {
		to = time.Now().UTC()
	}
	if from.IsZero() {
		from = to.Add(-7 * 24 * time.Hour)
	}
	if !from.Before(to) {
		return []WindowAggregate{}, nil
	}
	if to.Sub(from) > maxDailyTrendRange {
		return nil, ErrTrendRangeTooLarge
	}
	return s.query(ctx, "go_runtime_metrics_trend_windows", service, role, from, to)
}
func (s *Store) query(ctx context.Context, t, service, role string, from, to time.Time) ([]WindowAggregate, error) {
	p := s.placeholder
	daily := t == "go_runtime_metrics_trend_windows"
	cols := "service,role,runtime_kind,window_start,sample_count,avg_goroutines,max_goroutines,avg_goroutines_runnable,max_goroutines_runnable,avg_goroutines_waiting,max_goroutines_waiting,avg_threads,max_threads,avg_gomaxprocs,max_gomaxprocs,avg_heap_alloc_bytes,max_heap_alloc_bytes,avg_heap_live_bytes,max_heap_live_bytes,avg_heap_objects,max_heap_objects,avg_cpu_percent,max_cpu_percent,cpu_sample_count,avg_rss_bytes,max_rss_bytes,rss_sample_count,avg_fd_count,max_fd_count,fd_sample_count,avg_uptime_seconds,max_uptime_seconds"
	if daily {
		cols = "service,role,runtime_kind,window_start,window_end," + strings.TrimPrefix(cols, "service,role,runtime_kind,window_start,")
	}
	q := fmt.Sprintf("SELECT %s FROM %s WHERE service=%s AND role=%s AND runtime_kind='go' AND window_start >= %s AND window_start < %s ORDER BY window_start LIMIT %d", cols, s.tableName(t), p(1), p(2), p(3), p(4), func() int {
		if daily {
			return maxDailyTrendRows
		}
		return maxHourlyTrendRows
	}())
	rows, e := s.db.QueryContext(ctx, q, service, role, from.UTC(), to.UTC())
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []WindowAggregate{}
	for rows.Next() {
		var a WindowAggregate
		var ws, we any
		var o [6]sql.NullFloat64
		var cpuCount, rssCount, fdCount uint64
		args := []any{&a.Service, &a.Role, &a.RuntimeKind, &ws}
		if daily {
			args = append(args, &we)
		}
		args = append(args, &a.SampleCount, &a.GoroutinesAvg, &a.GoroutinesMax, &a.GoroutinesRunnableAvg, &a.GoroutinesRunnableMax, &a.GoroutinesWaitingAvg, &a.GoroutinesWaitingMax, &a.ThreadsAvg, &a.ThreadsMax, &a.GOMAXPROCSAvg, &a.GOMAXPROCSMax, &a.HeapAllocBytesAvg, &a.HeapAllocBytesMax, &a.HeapLiveBytesAvg, &a.HeapLiveBytesMax, &a.HeapObjectsAvg, &a.HeapObjectsMax, &o[0], &o[1], &cpuCount, &o[2], &o[3], &rssCount, &o[4], &o[5], &fdCount, &a.UptimeSecondsAvg, &a.UptimeSecondsMax)
		if e = rows.Scan(args...); e != nil {
			return nil, e
		}
		a.CPUPercentAvg = nullable(o[0])
		a.CPUPercentMax = nullable(o[1])
		a.RSSBytesAvg = nullable(o[2])
		a.RSSBytesMax = nullable(o[3])
		a.FDCountAvg = nullable(o[4])
		a.FDCountMax = nullable(o[5])
		a.WindowStart = parseSQLTime(ws)
		if daily {
			a.WindowEnd = parseSQLTime(we)
		} else {
			a.WindowEnd = a.WindowStart.Add(time.Hour)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WindowStart.Before(out[j].WindowStart) })
	return out, nil
}
func nullable(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	x := v.Float64
	return &x
}
func parseSQLTime(v any) time.Time {
	switch x := v.(type) {
	case time.Time:
		return x.UTC()
	case string:
		t, _ := time.Parse(time.RFC3339Nano, x)
		return t.UTC()
	case []byte:
		return parseSQLTime(string(x))
	}
	return time.Time{}
}
func utcDay(v time.Time) time.Time {
	v = v.UTC()
	return time.Date(v.Year(), v.Month(), v.Day(), 0, 0, 0, 0, time.UTC)
}
