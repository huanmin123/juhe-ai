// Package monitoringretirement contains the one-shot maintenance command that
// drops the 12 retired monitoring tables (退役监控表清退 2026-10-14):
//
//   - 9 stats tables — the Node-legacy system/process sampling family
//     (system_metrics_{samples,hourly,trend_windows}、
//     process_event_loop_{samples,hourly,trend_windows}, created by this
//     module's --ensure-schema) and the Go runtime metrics family
//     (go_runtime_metrics_{samples,hourly,trend_windows}, created by the
//     shared platform gometrics store);
//   - 3 table-monitor tables (database_storage_snapshots、
//     table_storage_snapshots、table_monitor_owner_leases), created at
//     runtime by the gateway/jobs tablemonitor stores — maintenance never
//     held their DDL, so --ensure-schema is untouched and they are cleaned
//     only here.
//
// Storage locations (取证 2026-10-14):
//
//   - PostgreSQL: all 12 tables live in the juhe_stats schema of the single
//     --dsn database — the gometrics store falls back to the shared
//     JUHE_AI_POSTGRES_URL and the tablemonitor stores bootstrap into the
//     same juhe_stats schema. A deployment that pointed the former
//     JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL at a separate database can
//     simply re-run the command with --dsn targeting that database; the
//     already-absent tables are a benign no-op.
//   - SQLite: three files — the stats file (6 system/process tables), the
//     go-runtime-metrics file (go-runtime-metrics.sqlite3 fixed name, 3
//     tables) and the table-monitor file (table-monitor.sqlite3 fixed name,
//     3 tables).
//
// Every statement is idempotent (existence probe + DROP TABLE IF EXISTS);
// a second run finds nothing left and reports alreadyRetired without
// writing. Per-table failures never abort the run — every target is
// attempted so the report always carries the complete succeeded/failed
// split, and the CLI exits non-zero when anything failed.
package monitoringretirement

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
)

// SchemaName is the PostgreSQL schema owning all 12 retired tables.
const SchemaName = "juhe_stats"

// SQLite 固定文件名与 store 名：与 gateway/jobs 的 datadir 固定名逐字一致
// （go-runtime-metrics.sqlite3 / table-monitor.sqlite3）。
const (
	StoreStats            = "stats"
	StoreGoRuntimeMetrics = "go-runtime-metrics"
	StoreTableMonitor     = "table-monitor"
)

// statsTables 是 stats 库（SQLite stats 文件 / PG juhe_stats schema）里由
// maintenance --ensure-schema 建表的 6 张 Node 遗留采样表。
var statsTables = []string{
	"system_metrics_samples", "system_metrics_hourly", "system_metrics_trend_windows",
	"process_event_loop_samples", "process_event_loop_hourly", "process_event_loop_trend_windows",
}

// goRuntimeMetricsTables 是共享 platform gometrics store 自举的 3 张表
// （SQLite 独立 go-runtime-metrics 文件 / PG juhe_stats schema）。
var goRuntimeMetricsTables = []string{
	"go_runtime_metrics_samples", "go_runtime_metrics_hourly", "go_runtime_metrics_trend_windows",
}

// tableMonitorTables 是 gateway/jobs tablemonitor store 运行时自举的 3 张表
// （SQLite 独立 table-monitor 文件 / PG juhe_stats schema）。
var tableMonitorTables = []string{
	"database_storage_snapshots", "table_storage_snapshots", "table_monitor_owner_leases",
}

// RetiredTables 返回全部 12 张退役表名（去重断言与报告用）。
func RetiredTables() []string {
	return append(append(append([]string{}, statsTables...), goRuntimeMetricsTables...), tableMonitorTables...)
}

// rollbackNote 是报告与 stderr 共用的回滚指引：DROP TABLE 无备份表可回填，
// 唯一回滚路径是外部备份整体恢复。
const rollbackNote = "回滚 = 用 --backup-confirmed 验证过的外部备份整体恢复对应数据库。" +
	"本命令的 DROP TABLE 不可原地撤销，报告因此不携带回滚 SQL；表上索引随表连带删除，无需单独清理。"

const isoMillisLayout = "2006-01-02T15:04:05.000Z"

// SQLiteTargets 是 sqlite 模式下三个目标库文件路径（--paths 解析结果）。
type SQLiteTargets struct {
	Stats            string
	GoRuntimeMetrics string
	TableMonitor     string
}

// TableResult 是单张退役表的清退结果。
type TableResult struct {
	Table   string `json:"table"`
	Existed bool   `json:"existed"`
	Dropped bool   `json:"dropped"`
	Error   string `json:"error,omitempty"`
}

// StoreReport 是单个目标库（PG 一个连接 / SQLite 一个文件）的清退结果。
type StoreReport struct {
	Store string `json:"store"`
	// Path 是 sqlite 文件路径；postgres 目标省略。
	Path string `json:"path,omitempty"`
	// FileExisted 表示 sqlite 文件是否存在（不存在时全部表记为已清退，
	// 不创建文件）；postgres 目标恒为 true。
	FileExisted bool          `json:"fileExisted"`
	Tables      []TableResult `json:"tables"`
}

// Report 是 --drop-retired-monitoring-tables 的 stdout JSON。
type Report struct {
	Driver  string        `json:"driver"`
	Targets []StoreReport `json:"targets"`
	// FailedTables 是执行失败的 <store>.<table> 清单；非空时 CLI 退出码 1。
	FailedTables   []string `json:"failedTables,omitempty"`
	DroppedCount   int      `json:"droppedTables"`
	AlreadyAbsent  int      `json:"alreadyAbsentTables"`
	AlreadyRetired bool     `json:"alreadyRetired"`
	FinishedAt     string   `json:"finishedAt"`
	RollbackNote   string   `json:"rollbackNote"`
}

// Ready 是成功条件：12 张表全部清退（或本就缺席）且无执行失败。
func (r Report) Ready() bool {
	return len(r.FailedTables) == 0
}

// RunPostgres drops the 12 retired tables from the juhe_stats schema of db
// (single --dsn connection, PG 形态全部 12 张同库同 schema)。逐表探测 +
// 幂等 DROP；单表失败不中断后续表。
func RunPostgres(ctx context.Context, db *sql.DB, now time.Time) (Report, error) {
	if db == nil {
		return Report{}, errors.New("退役监控表清退数据库未初始化")
	}
	if err := db.PingContext(ctx); err != nil {
		return Report{}, fmt.Errorf("目标 PostgreSQL 不可达: %w", err)
	}
	report := Report{
		Driver:       capabilitydomain.DialectPostgres.String(),
		FinishedAt:   now.UTC().Format(isoMillisLayout),
		RollbackNote: rollbackNote,
	}
	store := StoreReport{Store: SchemaName, FileExisted: true, Tables: []TableResult{}}
	for _, table := range RetiredTables() {
		result := dropPostgresTable(ctx, db, table)
		store.Tables = append(store.Tables, result)
		recordTableResult(&report, SchemaName, table, result)
	}
	report.Targets = append(report.Targets, store)
	finalize(&report)
	return report, nil
}

// RunSQLite drops the retired tables from the three SQLite files.文件不存在
// 的目标按「从未建表」处理（全部表记为已清退），绝不创建文件；文件存在但
// 打不开或单表失败计入 FailedTables。
func RunSQLite(ctx context.Context, targets SQLiteTargets, now time.Time) (Report, error) {
	report := Report{
		Driver:       capabilitydomain.DialectSQLite.String(),
		FinishedAt:   now.UTC().Format(isoMillisLayout),
		RollbackNote: rollbackNote,
	}
	for _, target := range []struct {
		store  string
		path   string
		tables []string
	}{
		{StoreStats, targets.Stats, statsTables},
		{StoreGoRuntimeMetrics, targets.GoRuntimeMetrics, goRuntimeMetricsTables},
		{StoreTableMonitor, targets.TableMonitor, tableMonitorTables},
	} {
		store := StoreReport{Store: target.store, Path: target.path, Tables: []TableResult{}}
		exists, err := sqliteFileExists(target.path)
		if err != nil {
			return report, fmt.Errorf("探测 %s 库文件失败: %w", target.store, err)
		}
		store.FileExisted = exists
		if !exists {
			for _, table := range target.tables {
				result := TableResult{Table: table, Existed: false, Dropped: false}
				store.Tables = append(store.Tables, result)
				recordTableResult(&report, target.store, table, result)
			}
			report.Targets = append(report.Targets, store)
			continue
		}
		db, err := openSQLiteReadOnlyCreate(target.path)
		if err != nil {
			return report, fmt.Errorf("打开 %s 库文件失败: %w", target.store, err)
		}
		for _, table := range target.tables {
			result := dropSQLiteTable(ctx, db, table)
			store.Tables = append(store.Tables, result)
			recordTableResult(&report, target.store, table, result)
		}
		if closeErr := db.Close(); closeErr != nil {
			return report, fmt.Errorf("关闭 %s 库连接失败: %w", target.store, closeErr)
		}
		report.Targets = append(report.Targets, store)
	}
	finalize(&report)
	return report, nil
}

// recordTableResult 汇总单表结果到报告级计数与失败清单。
func recordTableResult(report *Report, store, table string, result TableResult) {
	if result.Error != "" {
		report.FailedTables = append(report.FailedTables, store+"."+table)
		return
	}
	if result.Dropped {
		report.DroppedCount++
	} else {
		report.AlreadyAbsent++
	}
}

// finalize 排序失败清单并推导 AlreadyRetired。
func finalize(report *Report) {
	sort.Strings(report.FailedTables)
	report.AlreadyRetired = report.DroppedCount == 0 && len(report.FailedTables) == 0
}

// dropPostgresTable 探测并幂等删除 juhe_stats schema 的一张表。
func dropPostgresTable(ctx context.Context, db *sql.DB, table string) TableResult {
	result := TableResult{Table: table}
	var found sql.NullString
	// 表名为编译期常量，直接内联拼接与 ensure/schema 包同口径。
	if err := db.QueryRowContext(ctx, "SELECT to_regclass($1)", SchemaName+"."+table).Scan(&found); err != nil {
		result.Error = fmt.Sprintf("探测 %s.%s 失败: %v", SchemaName, table, err)
		return result
	}
	result.Existed = found.Valid && strings.TrimSpace(found.String) != ""
	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+SchemaName+"."+table); err != nil {
		result.Error = fmt.Sprintf("DROP TABLE %s.%s 失败: %v", SchemaName, table, err)
		return result
	}
	result.Dropped = result.Existed
	return result
}

// dropSQLiteTable 探测并幂等删除一个 SQLite 文件里的一张表。
func dropSQLiteTable(ctx context.Context, db *sql.DB, table string) TableResult {
	result := TableResult{Table: table}
	var count int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count); err != nil {
		result.Error = fmt.Sprintf("探测表 %s 失败: %v", table, err)
		return result
	}
	result.Existed = count > 0
	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
		result.Error = fmt.Sprintf("DROP TABLE %s 失败: %v", table, err)
		return result
	}
	result.Dropped = result.Existed
	return result
}

// sqliteFileExists 只探测不创建：清退命令不得在缺文件的目标上凭空建库。
func sqliteFileExists(path string) (bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return false, errors.New("sqlite 目标路径为空")
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.IsDir() {
		return false, fmt.Errorf("sqlite 目标路径是目录: %s", path)
	}
	return true, nil
}

// openSQLiteReadOnlyCreate 打开已存在的 SQLite 文件（mode=rw：文件缺失即报
// 错，绝不创建；单连接 + busy timeout，与 maintenance 连接契约一致）。
func openSQLiteReadOnlyCreate(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rw&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("打开 sqlite 文件 %s 失败: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("初始化 sqlite PRAGMA 失败: %w", err)
	}
	return db, nil
}
