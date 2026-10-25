// The juhe-ai-maintenance --apply-success-cost-backfill command (成功成本
// 历史回填治理，2026-10-12；契约见
// docs/plans/计划-20261011T225418-成功成本历史回填治理.md §3.2)：六张
// usage_stats 表 success_cost_usd 历史回填的唯一执行者。
//
// 语义与边界：
//   - ensure-schema 只幂等建 backfill_progress marker 表、绝不回填（结构性
//     职责分离，BUG-0307 教训）；本命令前置检查 marker 表，缺失时显式报错
//     指引先执行 --ensure-schema，不静默建表。
//   - 逐表分批：主键 tuple 游标 `WHERE (pk...) > (cursor...) ORDER BY pk...
//     LIMIT 5000` 读批锚点，批内 `UPDATE <表> SET success_cost_usd =
//     total_cost_usd WHERE pk 范围 AND error_count = 0 AND
//     success_cost_usd <> total_cost_usd`（谓词在行锁后的当前值上重判，
//     不用批前读取值写回）；cursor 推进与批 UPDATE 同一事务提交，中断后从
//     持久化 cursor 续跑，不丢行、不重改。
//   - 批 CAS：UPDATE backfill_progress ... WHERE table_name = ? AND
//     status = 'running' AND cursor_json = 期望值，影响 0 行即并发 runner
//     冲突，报错回滚（运维约定维护窗口内单实例，CAS 仅为防御）。
//   - --dry-run 只对六表 SELECT COUNT(*) 统计待回填行数，不读不写 marker。
//
// 四形态承接（统一为“无 marker 就从头分批跑”）：新库 0 批秒 done；存量库
// 分批迁移；已回填无 marker（生产现状）批 UPDATE 命中 0 行自然收敛 done；
// 中断库从持久化 cursor 续跑。
//
// 退出码契约：0 完成（六表收敛 done，含 0 批秒完）、1 运行失败（marker 表
// 缺失、cursor CAS 冲突、SQL 错误等，保留原始错误）、2 用法错误（连接参数
// 缺失或矛盾）。SQLite --paths 形态随 SQLite 存储形态退役删除
// （PLAN-20261024T051925945Z B4）。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// successCostBackfillBatchSize 是单批扫描的行数上限（计划 §3.2 固定值）。
const successCostBackfillBatchSize = 5000

// missingBackfillProgressHint 是 marker 表缺失时的固定指引文案：结构缺失
// 收敛到显式运维动作，本命令绝不静默建表。
const missingBackfillProgressHint = "backfill_progress 表不存在；先执行 --ensure-schema 创建该表（成功成本历史回填治理：ensure 只建标记表，回填只由本命令执行）"

// successCostBackfillTarget 描述一张回填目标表与其主键 tuple（游标列序，
// 主键列序以 juhe_stats 建表 DDL 为准）。
type successCostBackfillTarget struct {
	name      string
	pkColumns []string
}

// successCostBackfillTargets 按固定执行序列列出六张表。
var successCostBackfillTargets = []successCostBackfillTarget{
	{name: "usage_stats_totals", pkColumns: []string{"system_account_id", "scope_type", "scope_id"}},
	{name: "usage_stats_minute", pkColumns: []string{"system_account_id", "scope_type", "scope_id", "stat_minute"}},
	{name: "usage_stats_hourly", pkColumns: []string{"system_account_id", "scope_type", "scope_id", "stat_hour"}},
	{name: "usage_stats_daily", pkColumns: []string{"system_account_id", "scope_type", "scope_id", "stat_date"}},
	{name: "usage_stats_weekly", pkColumns: []string{"system_account_id", "scope_type", "scope_id", "stat_week"}},
	{name: "usage_stats_monthly", pkColumns: []string{"system_account_id", "scope_type", "scope_id", "stat_month"}},
}

// backfillDialect 承载 juhe_stats schema 限定（表名前缀 + $n 占位符）。
// sqlite 方言随 SQLite 存储形态退役删除（PLAN-20261024T051925945Z B4）。
type backfillDialect struct{}

func (d backfillDialect) table(name string) string {
	return "juhe_stats." + name
}

func (d backfillDialect) placeholder(index int) string {
	return "$" + strconv.Itoa(index)
}

// placeholderTuple 渲染 (?, ?, ...) / ($1, $2, ...) 形态的占位符 tuple。
func (d backfillDialect) placeholderTuple(count, firstIndex int) string {
	parts := make([]string, count)
	for i := range parts {
		parts[i] = d.placeholder(firstIndex + i)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// columnTuple 渲染 (col1, col2, ...) 形态的列 tuple。
func columnTuple(columns []string) string {
	return "(" + strings.Join(columns, ", ") + ")"
}

// backfillMarkerReadSQL / InsertRunningSQL / AdvanceSQL / DoneSQL 是
// backfill_progress 标记面的四条语句（cursor CAS 谓词在 Advance/Done 的
// WHERE 子句中：table_name + status='running' + cursor_json=期望值）。
func backfillMarkerReadSQL(dialect backfillDialect) string {
	return "SELECT status, cursor_json FROM " + dialect.table("backfill_progress") +
		" WHERE table_name = " + dialect.placeholder(1)
}

func backfillMarkerInsertRunningSQL(dialect backfillDialect) string {
	return "INSERT INTO " + dialect.table("backfill_progress") +
		" (table_name, status, cursor_json, updated_at) VALUES (" +
		dialect.placeholder(1) + ", 'running', '{}', " + dialect.placeholder(2) + ")"
}

func backfillMarkerAdvanceSQL(dialect backfillDialect) string {
	return "UPDATE " + dialect.table("backfill_progress") +
		" SET cursor_json = " + dialect.placeholder(1) +
		", updated_at = " + dialect.placeholder(2) +
		" WHERE table_name = " + dialect.placeholder(3) +
		" AND status = 'running' AND cursor_json = " + dialect.placeholder(4)
}

func backfillMarkerDoneSQL(dialect backfillDialect) string {
	return "UPDATE " + dialect.table("backfill_progress") +
		" SET status = 'done', cursor_json = '{}', updated_at = " + dialect.placeholder(1) +
		" WHERE table_name = " + dialect.placeholder(2) +
		" AND status = 'running' AND cursor_json = " + dialect.placeholder(3)
}

// backfillBatchSelectSQL 渲染单批扫描语句：主键 tuple 游标锚点 + 排序 +
// LIMIT。hasCursor=false（首扫无游标）时省略 WHERE 段。
func backfillBatchSelectSQL(dialect backfillDialect, target successCostBackfillTarget, hasCursor bool) string {
	query := "SELECT " + strings.Join(target.pkColumns, ", ") + " FROM " + dialect.table(target.name)
	if hasCursor {
		query += " WHERE " + columnTuple(target.pkColumns) + " > " + dialect.placeholderTuple(len(target.pkColumns), 1)
	}
	return query + " ORDER BY " + strings.Join(target.pkColumns, ", ") +
		" LIMIT " + strconv.Itoa(successCostBackfillBatchSize)
}

// backfillBatchUpdateSQL 渲染单批回填语句：批内主键范围 + 在行锁后的当前
// 值上重判谓词。hasCursor=false 时范围只有上界（游标占位符从 $1 起）。
func backfillBatchUpdateSQL(dialect backfillDialect, target successCostBackfillTarget, hasCursor bool) string {
	width := len(target.pkColumns)
	upperFirst := 1
	rangeClause := ""
	if hasCursor {
		rangeClause = columnTuple(target.pkColumns) + " > " + dialect.placeholderTuple(width, 1) + " AND "
		upperFirst = width + 1
	}
	return "UPDATE " + dialect.table(target.name) + " SET success_cost_usd = total_cost_usd WHERE " +
		rangeClause +
		columnTuple(target.pkColumns) + " <= " + dialect.placeholderTuple(width, upperFirst) +
		" AND error_count = 0 AND success_cost_usd <> total_cost_usd"
}

// successCostBackfillReport 是命令的 JSON 报告面（stdout 单行）。
type successCostBackfillReport struct {
	Driver    string                          `json:"driver"`
	DryRun    bool                            `json:"dryRun"`
	Completed bool                            `json:"completed"`
	Tables    map[string]successCostTableStat `json:"tables"`
}

// successCostTableStat 汇总单表结果：status = done（apply 模式收敛完成或
// marker 已 done）| dry-run（预检）；matchedRows 是本命令实际回填的行数
// （dry-run 下为待回填行数），batches 是本命令处理的批数。
type successCostTableStat struct {
	Status      string `json:"status"`
	Batches     int    `json:"batches"`
	MatchedRows int    `json:"matchedRows"`
}

// applySuccessCostBackfillResult 返回 CLI 退出码；runMaintenance 派发它，
// 测试进程内直调。用法/开库错误 exit 2，运行失败 exit 1（保留原始错误）。
func applySuccessCostBackfillResult(dsn string, dryRun bool) int {
	parsedDSN := strings.TrimSpace(dsn)
	if parsedDSN == "" {
		fmt.Fprintln(os.Stderr, "--apply-success-cost-backfill 需要 --dsn 指向显式的 PostgreSQL URL")
		return 2
	}
	db, openErr := openPostgresBootstrap(parsedDSN)
	if openErr != nil {
		fmt.Fprintf(os.Stderr, "%v\n", openErr)
		return 2
	}
	defer db.Close()

	ctx := context.Background()
	dialect := backfillDialect{}
	report := successCostBackfillReport{Driver: "postgres", DryRun: dryRun, Tables: map[string]successCostTableStat{}}
	if dryRun {
		for _, target := range successCostBackfillTargets {
			pending, countErr := countPendingSuccessCostRows(ctx, db, dialect, target)
			if countErr != nil {
				fmt.Fprintf(os.Stderr, "成功成本回填预检失败：%v\n", countErr)
				return 1
			}
			report.Tables[target.name] = successCostTableStat{Status: "dry-run", MatchedRows: pending}
		}
		// 预检不落地任何收敛，completed 恒 false。
		if err := encodeSuccessCostBackfillReport(report); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 1
		}
		return 0
	}

	// 前置检查：marker 表缺失 → 显式报错（exit 1），不静默建表。
	if err := ensureBackfillProgressTablePresent(ctx, db, dialect); err != nil {
		fmt.Fprintf(os.Stderr, "成功成本回填前置检查失败：%s（原始错误：%v）\n", missingBackfillProgressHint, err)
		return 1
	}
	for _, target := range successCostBackfillTargets {
		stat, backfillErr := backfillSuccessCostTable(ctx, db, dialect, target)
		if backfillErr != nil {
			fmt.Fprintf(os.Stderr, "成功成本回填失败（表 %s）：%v\n", target.name, backfillErr)
			return 1
		}
		report.Tables[target.name] = stat
	}
	report.Completed = true
	if err := encodeSuccessCostBackfillReport(report); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	return 0
}

// encodeSuccessCostBackfillReport 以单行 JSON 输出报告。
func encodeSuccessCostBackfillReport(report successCostBackfillReport) error {
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		return fmt.Errorf("encode apply-success-cost-backfill report: %w", err)
	}
	return nil
}

// countPendingSuccessCostRows 只读统计单表待回填行数（--dry-run 用）。
func countPendingSuccessCostRows(ctx context.Context, db *sql.DB, dialect backfillDialect, target successCostBackfillTarget) (int, error) {
	query := "SELECT COUNT(*) FROM " + dialect.table(target.name) +
		" WHERE error_count = 0 AND success_cost_usd <> total_cost_usd"
	var pending int
	if err := db.QueryRowContext(ctx, query).Scan(&pending); err != nil {
		return 0, fmt.Errorf("count pending rows of %s: %w", target.name, err)
	}
	return pending, nil
}

// backfillProgressPresent 三态探测 marker 表存在性（to_regclass，juhe_stats
// 前缀限定）；探测失败返回包装后的原始错误（显式失败不静默）。
func backfillProgressPresent(ctx context.Context, db *sql.DB, dialect backfillDialect) (bool, error) {
	var qualified *string
	if err := db.QueryRowContext(ctx, "SELECT to_regclass('juhe_stats.backfill_progress')").Scan(&qualified); err != nil {
		return false, fmt.Errorf("probe backfill_progress: %w", err)
	}
	return qualified != nil, nil
}

// ensureBackfillProgressTablePresent 要求 marker 表存在（回填子命令前置
// 检查），缺失时返回显式错误。
func ensureBackfillProgressTablePresent(ctx context.Context, db *sql.DB, dialect backfillDialect) error {
	present, err := backfillProgressPresent(ctx, db, dialect)
	if err != nil {
		return err
	}
	if !present {
		return errors.New("juhe_stats.backfill_progress is missing")
	}
	return nil
}

// pendingBackfillTables 探测固定六张回填目标的待办。marker 表不存在时六表
// 均视为待办且 present=false；表存在时，缺少 marker 或 status 非 done 的
// 已知目标列为待办。未知 marker 名称忽略；不扫描事实表。
func pendingBackfillTables(ctx context.Context, db *sql.DB, dialect backfillDialect) ([]string, bool, error) {
	present, err := backfillProgressPresent(ctx, db, dialect)
	if err != nil {
		return nil, false, err
	}
	if !present {
		return sortedBackfillTargetNames(nil), false, nil
	}
	rows, err := db.QueryContext(ctx,
		"SELECT table_name, status FROM "+dialect.table("backfill_progress"))
	if err != nil {
		return nil, true, fmt.Errorf("list pending backfill tables: %w", err)
	}
	defer rows.Close()
	statuses := make(map[string]string, len(successCostBackfillTargets))
	knownTargets := make(map[string]struct{}, len(successCostBackfillTargets))
	for _, target := range successCostBackfillTargets {
		knownTargets[target.name] = struct{}{}
	}
	for rows.Next() {
		var name, status string
		if err := rows.Scan(&name, &status); err != nil {
			return nil, true, fmt.Errorf("scan pending backfill table: %w", err)
		}
		if _, known := knownTargets[name]; known {
			statuses[name] = status
		}
	}
	if err := rows.Err(); err != nil {
		return nil, true, fmt.Errorf("iterate pending backfill tables: %w", err)
	}
	return sortedBackfillTargetNames(statuses), true, nil
}

func sortedBackfillTargetNames(statuses map[string]string) []string {
	pending := make([]string, 0, len(successCostBackfillTargets))
	for _, target := range successCostBackfillTargets {
		if statuses[target.name] != "done" {
			pending = append(pending, target.name)
		}
	}
	sort.Strings(pending)
	return pending
}

// successCostBackfillTimestamp 产出与库内 stats 时间戳同风格的 UTC 文本。
func successCostBackfillTimestamp(now time.Time) string {
	return now.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// decodeBackfillProgressCursor 解析 backfill_progress.cursor_json：主键
// tuple 的 JSON 字符串数组；空 cursor 为 DDL 默认值 '{}'。
func decodeBackfillProgressCursor(raw string, width int) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
		return nil, fmt.Errorf("decode cursor %q: %w", trimmed, err)
	}
	if len(values) != width {
		return nil, fmt.Errorf("decode cursor %q: want %d tuple values, got %d", trimmed, width, len(values))
	}
	return values, nil
}

// encodeBackfillProgressCursor 序列化游标；空游标写 DDL 默认值 '{}'。
func encodeBackfillProgressCursor(values []string) (string, error) {
	if len(values) == 0 {
		return "{}", nil
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return string(encoded), nil
}

// backfillSuccessCostTable 回填单表并收敛 marker：done 直接跳过（只查一
// 行）；无 marker 先 INSERT running；每批的锚点扫描、批内 UPDATE（谓词在
// 当前值重判）与 cursor CAS 推进同一事务提交；扫完收敛 done 并清 cursor。
func backfillSuccessCostTable(ctx context.Context, db *sql.DB, dialect backfillDialect, target successCostBackfillTarget) (successCostTableStat, error) {
	stat := successCostTableStat{Status: "done"}
	now := successCostBackfillTimestamp(time.Now())

	var status, cursorRaw string
	scanErr := db.QueryRowContext(ctx, backfillMarkerReadSQL(dialect), target.name).Scan(&status, &cursorRaw)
	switch {
	case errors.Is(scanErr, sql.ErrNoRows):
		// 无 marker（新表/已回填无标记的生产现状）：从头分批跑。
		if _, execErr := db.ExecContext(ctx, backfillMarkerInsertRunningSQL(dialect), target.name, now); execErr != nil {
			return stat, fmt.Errorf("insert running marker for %s: %w", target.name, execErr)
		}
		status = "running"
		cursorRaw = "{}"
	case scanErr != nil:
		return stat, fmt.Errorf("read marker for %s: %w", target.name, scanErr)
	}
	switch status {
	case "done":
		// 只查一行即跳过，零事实表触碰。
		return stat, nil
	case "running":
	default:
		return stat, fmt.Errorf("marker for %s has unknown status %q", target.name, status)
	}
	cursor, cursorErr := decodeBackfillProgressCursor(cursorRaw, len(target.pkColumns))
	if cursorErr != nil {
		return stat, fmt.Errorf("marker for %s: %w", target.name, cursorErr)
	}

	expectedCursor := cursorRaw
	for {
		last, matchedRows, done, batchErr := backfillSuccessCostBatch(ctx, db, dialect, target, cursor, expectedCursor, now)
		if batchErr != nil {
			return stat, batchErr
		}
		if done {
			return stat, nil
		}
		stat.Batches++
		stat.MatchedRows += int(matchedRows)
		cursor = last
		nextCursor, encodeErr := encodeBackfillProgressCursor(last)
		if encodeErr != nil {
			return stat, encodeErr
		}
		expectedCursor = nextCursor
	}
}

// backfillSuccessCostBatch 把单批事务限制在本次调用范围内。批 CAS 冲突或
// 任一 SQL/RowsAffected 错误都在提交前返回并回滚整批。
func backfillSuccessCostBatch(ctx context.Context, db *sql.DB, dialect backfillDialect, target successCostBackfillTarget, cursor []string, expectedCursor, now string) (last []string, matchedRows int64, done bool, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, false, fmt.Errorf("begin batch transaction for %s: %w", target.name, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	hasCursor := len(cursor) > 0
	selectSQL := backfillBatchSelectSQL(dialect, target, hasCursor)
	var scanArgs []any
	if hasCursor {
		for _, value := range cursor {
			scanArgs = append(scanArgs, value)
		}
	}
	rows, queryErr := tx.QueryContext(ctx, selectSQL, scanArgs...)
	if queryErr != nil {
		return nil, 0, false, fmt.Errorf("scan batch of %s: %w", target.name, queryErr)
	}
	var batch [][]string
	for rows.Next() {
		values := make([]string, len(target.pkColumns))
		scans := make([]any, len(values))
		for i := range scans {
			scans[i] = &values[i]
		}
		if scanErr := rows.Scan(scans...); scanErr != nil {
			_ = rows.Close()
			return nil, 0, false, fmt.Errorf("scan %s batch row: %w", target.name, scanErr)
		}
		batch = append(batch, values)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		_ = rows.Close()
		return nil, 0, false, fmt.Errorf("iterate %s batch: %w", target.name, rowsErr)
	}
	if closeErr := rows.Close(); closeErr != nil {
		return nil, 0, false, fmt.Errorf("close %s batch rows: %w", target.name, closeErr)
	}

	if len(batch) == 0 {
		result, updateErr := tx.ExecContext(ctx, backfillMarkerDoneSQL(dialect), now, target.name, expectedCursor)
		if updateErr != nil {
			return nil, 0, false, fmt.Errorf("mark %s done: %w", target.name, updateErr)
		}
		affected, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return nil, 0, false, fmt.Errorf("mark %s done: read affected rows: %w", target.name, rowsErr)
		}
		if affected == 0 {
			return nil, 0, false, fmt.Errorf("cursor CAS conflict while marking %s done (another runner advanced the marker); rerun after the window", target.name)
		}
		if err := tx.Commit(); err != nil {
			return nil, 0, false, fmt.Errorf("commit done marker for %s: %w", target.name, err)
		}
		committed = true
		return nil, 0, true, nil
	}

	last = batch[len(batch)-1]
	updateSQL := backfillBatchUpdateSQL(dialect, target, hasCursor)
	updateArgs := make([]any, 0, 2*len(target.pkColumns))
	if hasCursor {
		for _, value := range cursor {
			updateArgs = append(updateArgs, value)
		}
	}
	for _, value := range last {
		updateArgs = append(updateArgs, value)
	}
	updateResult, updateErr := tx.ExecContext(ctx, updateSQL, updateArgs...)
	if updateErr != nil {
		return nil, 0, false, fmt.Errorf("backfill batch of %s: %w", target.name, updateErr)
	}
	matchedRows, err = updateResult.RowsAffected()
	if err != nil {
		return nil, 0, false, fmt.Errorf("backfill batch of %s: read affected rows: %w", target.name, err)
	}
	newCursorJSON, encodeErr := encodeBackfillProgressCursor(last)
	if encodeErr != nil {
		return nil, 0, false, encodeErr
	}
	cursorUpdate, markerErr := tx.ExecContext(ctx, backfillMarkerAdvanceSQL(dialect),
		newCursorJSON, now, target.name, expectedCursor)
	if markerErr != nil {
		return nil, 0, false, fmt.Errorf("advance cursor for %s: %w", target.name, markerErr)
	}
	affected, rowsErr := cursorUpdate.RowsAffected()
	if rowsErr != nil {
		return nil, 0, false, fmt.Errorf("advance cursor for %s: read affected rows: %w", target.name, rowsErr)
	}
	if affected == 0 {
		return nil, 0, false, fmt.Errorf("cursor CAS conflict while advancing %s (another runner advanced the marker); rerun after the window", target.name)
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, false, fmt.Errorf("commit batch of %s: %w", target.name, err)
	}
	committed = true
	return last, matchedRows, false, nil
}
