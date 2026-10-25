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
// Storage location (取证 2026-10-14): PostgreSQL — all 12 tables live in the
// juhe_stats schema of the single --dsn database — the gometrics store falls
// back to the shared JUHE_AI_POSTGRES_URL and the tablemonitor stores
// bootstrap into the same juhe_stats schema. A deployment that pointed the
// former JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL at a separate database can
// simply re-run the command with --dsn targeting that database; the
// already-absent tables are a benign no-op. SQLite 目标随 SQLite 存储形态
// 退役删除（PLAN-20261024T051925945Z B4）。
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
	"sort"
	"strings"
	"time"
)

// SchemaName is the PostgreSQL schema owning all 12 retired tables.
const SchemaName = "juhe_stats"

// statsTables 是 juhe_stats schema 里由 maintenance --ensure-schema 建表的
// 6 张 Node 遗留采样表。
var statsTables = []string{
	"system_metrics_samples", "system_metrics_hourly", "system_metrics_trend_windows",
	"process_event_loop_samples", "process_event_loop_hourly", "process_event_loop_trend_windows",
}

// goRuntimeMetricsTables 是共享 platform gometrics store 自举的 3 张表
// （juhe_stats schema）。
var goRuntimeMetricsTables = []string{
	"go_runtime_metrics_samples", "go_runtime_metrics_hourly", "go_runtime_metrics_trend_windows",
}

// tableMonitorTables 是 gateway/jobs tablemonitor store 运行时自举的 3 张表
// （juhe_stats schema）。
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

// TableResult 是单张退役表的清退结果。
type TableResult struct {
	Table   string `json:"table"`
	Existed bool   `json:"existed"`
	Dropped bool   `json:"dropped"`
	Error   string `json:"error,omitempty"`
}

// StoreReport 是单个目标 schema（juhe_stats）的清退结果。
type StoreReport struct {
	Store string `json:"store"`
	// Path 是历史 sqlite 文件路径字段；PG 目标恒省略（omitempty 保留，
	// PG 报告 JSON 形状与 sqlite 退役前逐字节一致）。
	Path string `json:"path,omitempty"`
	// FileExisted 表示目标库可达；postgres 单库目标恒为 true。
	FileExisted bool           `json:"fileExisted"`
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
// (single --dsn connection, 全部 12 张同库同 schema)。逐表探测 + 幂等 DROP；
// 单表失败不中断后续表。
func RunPostgres(ctx context.Context, db *sql.DB, now time.Time) (Report, error) {
	if db == nil {
		return Report{}, errors.New("退役监控表清退数据库未初始化")
	}
	if err := db.PingContext(ctx); err != nil {
		return Report{}, fmt.Errorf("目标 PostgreSQL 不可达: %w", err)
	}
	report := Report{
		Driver:       "postgres",
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
