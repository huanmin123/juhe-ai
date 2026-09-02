// Package goruntimemetrics contains the maintenance-only schema preflight for
// the independent Go runtime metrics tables. It deliberately does not touch
// the legacy Node statistics tables or any application-owned schema.
package goruntimemetrics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	gometrics "github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	SchemaName   = "juhe_stats"
	BootstrapEnv = "JUHE_AI_MAINTENANCE_GO_RUNTIME_METRICS_POSTGRES_URL"
)

var requiredTables = []string{
	"go_runtime_metrics_samples",
	"go_runtime_metrics_hourly",
	"go_runtime_metrics_trend_windows",
}

// Report contains only database identity and schema object names. It never
// echoes the connection URL or any credential.
type Report struct {
	Database      string   `json:"database"`
	CurrentRole   string   `json:"currentRole"`
	Schema        string   `json:"schema"`
	MissingTables []string `json:"missingTables"`
	InvalidTables []string `json:"invalidTables"`
	Applied       bool     `json:"applied"`
}

func (r Report) Ready() bool {
	return len(r.MissingTables) == 0 && len(r.InvalidTables) == 0
}

// Open validates the explicit maintenance-scoped PostgreSQL URL before
// opening a one-shot connection. No application URL fallback is permitted.
func Open(rawURL string) (*sql.DB, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return nil, errors.New("Go runtime metrics bootstrap 必须提供 postgres/postgresql URL")
	}
	if parsed.Hostname() == "" || strings.Trim(strings.TrimSpace(parsed.Path), "/") == "" || parsed.User == nil || strings.TrimSpace(parsed.User.Username()) == "" {
		return nil, errors.New("Go runtime metrics bootstrap URL 必须包含主机、数据库和显式角色")
	}
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		return nil, fmt.Errorf("打开 Go runtime metrics PostgreSQL 连接失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// Run performs a read-only schema check, or applies the additive schema via
// the shared gometrics Store and then verifies it. Stop/backup confirmations
// are enforced by the maintenance command before this function is called.
func Run(ctx context.Context, db *sql.DB, apply bool) (Report, error) {
	if db == nil {
		return Report{}, errors.New("Go runtime metrics bootstrap 数据库未初始化")
	}
	if !apply {
		return inspect(ctx, db)
	}
	store, err := gometrics.NewStore(db, gometrics.DialectPostgres)
	if err != nil {
		return Report{}, err
	}
	if err := store.EnsureSchema(ctx); err != nil {
		return Report{}, fmt.Errorf("执行 Go runtime metrics PostgreSQL schema bootstrap 失败: %w", err)
	}
	report, err := inspect(ctx, db)
	if err != nil {
		return Report{}, err
	}
	if !report.Ready() {
		return Report{}, fmt.Errorf("Go runtime metrics schema bootstrap 后契约仍不完整: missing_tables=%s invalid_tables=%s", strings.Join(report.MissingTables, ","), strings.Join(report.InvalidTables, ","))
	}
	report.Applied = true
	return report, nil
}

func inspect(ctx context.Context, db *sql.DB) (Report, error) {
	report := Report{Schema: SchemaName}
	if err := db.QueryRowContext(ctx, "SELECT current_database(), current_user").Scan(&report.Database, &report.CurrentRole); err != nil {
		return Report{}, fmt.Errorf("读取 Go runtime metrics PostgreSQL 身份失败: %w", err)
	}
	rows, err := db.QueryContext(ctx, "SELECT table_name FROM information_schema.tables WHERE table_schema=$1 AND table_name IN ($2,$3,$4)", SchemaName, requiredTables[0], requiredTables[1], requiredTables[2])
	if err != nil {
		return Report{}, fmt.Errorf("读取 Go runtime metrics table 契约失败: %w", err)
	}
	seen := make(map[string]struct{}, len(requiredTables))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return Report{}, fmt.Errorf("读取 Go runtime metrics table 名称失败: %w", err)
		}
		seen[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return Report{}, fmt.Errorf("遍历 Go runtime metrics table 契约失败: %w", err)
	}
	_ = rows.Close()
	for _, name := range requiredTables {
		if _, ok := seen[name]; !ok {
			report.MissingTables = append(report.MissingTables, name)
		}
	}
	if len(report.MissingTables) == 0 {
		if err := inspectColumns(ctx, db, &report); err != nil {
			return Report{}, err
		}
		if err := inspectPrimaryKeys(ctx, db, &report); err != nil {
			return Report{}, err
		}
	}
	sort.Strings(report.MissingTables)
	sort.Strings(report.InvalidTables)
	return report, nil
}

type columnSpec struct {
	dataType string
	udtName  string
}

var requiredColumns = map[string]map[string]columnSpec{
	"go_runtime_metrics_samples": {
		"service": {"text", "text"}, "role": {"text", "text"}, "runtime_kind": {"text", "text"},
		"process_pid": {"bigint", "int8"}, "sampled_at": {"timestamp with time zone", "timestamptz"},
		"goroutines": {"bigint", "int8"}, "goroutines_runnable": {"bigint", "int8"}, "goroutines_waiting": {"bigint", "int8"},
		"threads": {"bigint", "int8"}, "gomaxprocs": {"bigint", "int8"}, "heap_alloc_bytes": {"bigint", "int8"},
		"heap_live_bytes": {"bigint", "int8"}, "heap_objects": {"bigint", "int8"},
		"cpu_percent": {"double precision", "float8"}, "rss_bytes": {"bigint", "int8"}, "fd_count": {"bigint", "int8"}, "uptime_seconds": {"double precision", "float8"},
	},
	"go_runtime_metrics_hourly": {
		"service": {"text", "text"}, "role": {"text", "text"}, "runtime_kind": {"text", "text"},
		"window_start": {"timestamp with time zone", "timestamptz"}, "sample_count": {"bigint", "int8"},
		"avg_goroutines": {"double precision", "float8"}, "max_goroutines": {"double precision", "float8"},
		"avg_goroutines_runnable": {"double precision", "float8"}, "max_goroutines_runnable": {"double precision", "float8"},
		"avg_goroutines_waiting": {"double precision", "float8"}, "max_goroutines_waiting": {"double precision", "float8"},
		"avg_gomaxprocs": {"double precision", "float8"}, "max_gomaxprocs": {"double precision", "float8"},
		"avg_heap_alloc_bytes": {"double precision", "float8"}, "max_heap_alloc_bytes": {"double precision", "float8"},
		"avg_heap_live_bytes": {"double precision", "float8"}, "max_heap_live_bytes": {"double precision", "float8"},
		"avg_heap_objects": {"double precision", "float8"}, "max_heap_objects": {"double precision", "float8"},
		"avg_threads": {"double precision", "float8"}, "max_threads": {"double precision", "float8"},
		"avg_cpu_percent": {"double precision", "float8"}, "max_cpu_percent": {"double precision", "float8"}, "cpu_sample_count": {"bigint", "int8"},
		"avg_rss_bytes": {"double precision", "float8"}, "max_rss_bytes": {"double precision", "float8"}, "rss_sample_count": {"bigint", "int8"},
		"avg_fd_count": {"double precision", "float8"}, "max_fd_count": {"double precision", "float8"}, "fd_sample_count": {"bigint", "int8"},
		"avg_uptime_seconds": {"double precision", "float8"}, "max_uptime_seconds": {"double precision", "float8"},
	},
	"go_runtime_metrics_trend_windows": {
		"service": {"text", "text"}, "role": {"text", "text"}, "runtime_kind": {"text", "text"},
		"window_start": {"timestamp with time zone", "timestamptz"}, "window_end": {"timestamp with time zone", "timestamptz"}, "sample_count": {"bigint", "int8"},
		"avg_goroutines": {"double precision", "float8"}, "max_goroutines": {"double precision", "float8"},
		"avg_goroutines_runnable": {"double precision", "float8"}, "max_goroutines_runnable": {"double precision", "float8"},
		"avg_goroutines_waiting": {"double precision", "float8"}, "max_goroutines_waiting": {"double precision", "float8"},
		"avg_gomaxprocs": {"double precision", "float8"}, "max_gomaxprocs": {"double precision", "float8"},
		"avg_heap_alloc_bytes": {"double precision", "float8"}, "max_heap_alloc_bytes": {"double precision", "float8"},
		"avg_heap_live_bytes": {"double precision", "float8"}, "max_heap_live_bytes": {"double precision", "float8"},
		"avg_heap_objects": {"double precision", "float8"}, "max_heap_objects": {"double precision", "float8"},
		"avg_threads": {"double precision", "float8"}, "max_threads": {"double precision", "float8"},
		"avg_cpu_percent": {"double precision", "float8"}, "max_cpu_percent": {"double precision", "float8"}, "cpu_sample_count": {"bigint", "int8"},
		"avg_rss_bytes": {"double precision", "float8"}, "max_rss_bytes": {"double precision", "float8"}, "rss_sample_count": {"bigint", "int8"},
		"avg_fd_count": {"double precision", "float8"}, "max_fd_count": {"double precision", "float8"}, "fd_sample_count": {"bigint", "int8"},
		"avg_uptime_seconds": {"double precision", "float8"}, "max_uptime_seconds": {"double precision", "float8"},
	},
}

func inspectColumns(ctx context.Context, db *sql.DB, report *Report) error {
	rows, err := db.QueryContext(ctx, `SELECT table_name,column_name,data_type,udt_name,is_nullable FROM information_schema.columns WHERE table_schema=$1 AND table_name IN ($2,$3,$4)`, SchemaName, requiredTables[0], requiredTables[1], requiredTables[2])
	if err != nil {
		return fmt.Errorf("读取 Go runtime metrics column 契约失败: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]map[string]columnSpec, len(requiredColumns))
	for rows.Next() {
		var table, column, dataType, udtName, nullable string
		if err := rows.Scan(&table, &column, &dataType, &udtName, &nullable); err != nil {
			return fmt.Errorf("读取 Go runtime metrics column 定义失败: %w", err)
		}
		if seen[table] == nil {
			seen[table] = make(map[string]columnSpec)
		}
		seen[table][column] = columnSpec{dataType, udtName}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历 Go runtime metrics column 契约失败: %w", err)
	}
	// Release the single maintenance connection before issuing the second
	// information_schema query below. database/sql normally closes at EOF, but
	// making it explicit keeps the max-open-conns=1 contract deterministic.
	if err := rows.Close(); err != nil {
		return fmt.Errorf("关闭 Go runtime metrics column 契约结果失败: %w", err)
	}
	for table, columns := range requiredColumns {
		for column, expected := range columns {
			actual, ok := seen[table][column]
			if !ok {
				report.InvalidTables = append(report.InvalidTables, table+"."+column+":missing")
				continue
			}
			if actual != expected {
				report.InvalidTables = append(report.InvalidTables, fmt.Sprintf("%s.%s:type=%s/%s", table, column, actual.dataType, actual.udtName))
			}
		}
	}
	// A nullable metric column would make aggregation semantics ambiguous and
	// is not part of the bootstrap contract, even when its SQL type matches.
	rows, err = db.QueryContext(ctx, `SELECT table_name,column_name FROM information_schema.columns WHERE table_schema=$1 AND table_name IN ($2,$3,$4) AND is_nullable <> 'NO'`, SchemaName, requiredTables[0], requiredTables[1], requiredTables[2])
	if err != nil {
		return fmt.Errorf("读取 Go runtime metrics nullability 契约失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return fmt.Errorf("读取 Go runtime metrics nullability 定义失败: %w", err)
		}
		if !optionalColumn(table, column) {
			report.InvalidTables = append(report.InvalidTables, table+"."+column+":nullable")
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历 Go runtime metrics nullability 契约失败: %w", err)
	}
	return nil
}

func optionalColumn(table, column string) bool {
	if table == "go_runtime_metrics_samples" {
		return column == "cpu_percent" || column == "rss_bytes" || column == "fd_count"
	}
	return column == "avg_cpu_percent" || column == "max_cpu_percent" || column == "avg_rss_bytes" || column == "max_rss_bytes" || column == "avg_fd_count" || column == "max_fd_count"
}

var requiredPrimaryKeys = map[string][]string{
	"go_runtime_metrics_samples":       {"service", "role", "runtime_kind", "process_pid", "sampled_at"},
	"go_runtime_metrics_hourly":        {"service", "role", "runtime_kind", "window_start"},
	"go_runtime_metrics_trend_windows": {"service", "role", "runtime_kind", "window_start"},
}

func inspectPrimaryKeys(ctx context.Context, db *sql.DB, report *Report) error {
	for table, expected := range requiredPrimaryKeys {
		rows, err := db.QueryContext(ctx, `SELECT kcu.column_name
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON kcu.constraint_schema=tc.constraint_schema
 AND kcu.constraint_name=tc.constraint_name
 AND kcu.table_name=tc.table_name
WHERE tc.constraint_schema=$1 AND tc.table_name=$2
  AND tc.constraint_type='PRIMARY KEY'
ORDER BY kcu.ordinal_position`, SchemaName, table)
		if err != nil {
			return fmt.Errorf("读取 Go runtime metrics primary key 契约失败: %w", err)
		}
		actual := make([]string, 0, len(expected))
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				_ = rows.Close()
				return fmt.Errorf("读取 Go runtime metrics primary key 定义失败: %w", err)
			}
			actual = append(actual, column)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("遍历 Go runtime metrics primary key 契约失败: %w", err)
		}
		_ = rows.Close()
		if !sameStrings(actual, expected) {
			report.InvalidTables = append(report.InvalidTables, fmt.Sprintf("%s:primary_key=%s", table, strings.Join(actual, ",")))
		}
	}
	return nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
