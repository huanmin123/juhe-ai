// 账户名称搜索索引退役命令（drop-retired-account-name-search-index，2026-11-03）：
//   - 一次性清退账户名称搜索的全部派生词项索引层：业务表
//     account_name_search_terms / account_name_search_documents、投影词项表
//     account_list_availability_projection_search_terms，投影索引列
//     account_list_availability_projection_index.search_index_complete，以及
//     schema 以前创建的触发器函数
//     juhe_business.account_list_availability_name_search_dirty_trigger()
//     （DROP TABLE 只会删除表上的触发器，函数本体不随表删除）；
//   - 全部动作幂等（to_regclass/to_regprocedure 探测 + DROP TABLE IF EXISTS /
//     DROP COLUMN IF EXISTS / DROP FUNCTION IF EXISTS），探测在 BeginTx 之前
//     只读完成，apply 在单事务内先 SET LOCAL lock_timeout = '2s' 再执行 5 条
//     DDL（3 DROP TABLE + 1 ALTER DROP COLUMN + 1 DROP FUNCTION）；重跑只报告
//     已缺席、不写入；
//   - 正式执行必须同时带 --stop-write-confirmed（gateway/jobs 已停止）与
//     --backup-confirmed（可恢复备份已验证），缺一 exit 2 且不开库；
//   - --dry-run 只读探测三张表/列/函数的当前存在状态与计划动作，不要求确认、
//     不写库；
//   - stdout 输出单行 JSON 报告（mode/database/tables/column/function/actions），
//     stderr 输出进度；退出码 0 成功、1 运行失败、2 用法/门禁错误。
//
// 搜索读取侧已改为直接查 accounts.name（ILIKE），本命令只负责存量库清理；
// 新库由 maintenance schema 直接建出无索引层形态，无需重放本命令。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

const accountNameSearchRetiredSchema = "juhe_business"

// accountNameSearchRetiredTables 是待清退的三张派生词项表（顺序即 DROP 顺序，
// 与 FK 依赖一致：先词项后文档）。
var accountNameSearchRetiredTables = []string{
	"account_name_search_terms",
	"account_name_search_documents",
	"account_list_availability_projection_search_terms",
}

const (
	accountNameSearchProjectionIndexTable = "account_list_availability_projection_index"
	accountNameSearchProjectionIndexCol   = "search_index_complete"
	// accountNameSearchDirtyTriggerFunction 是 schema 以前创建的名称搜索
	// dirty 触发器函数：DROP TABLE 只删除表上的触发器，函数本体不会随表
	// 删除，存量库必须显式清退。
	accountNameSearchDirtyTriggerFunction = "account_list_availability_name_search_dirty_trigger"
)

// accountNameSearchRetirementGate 是命令的用法与停写/备份门禁；validate 不
// 触碰数据库，dry-run 不要求确认。
type accountNameSearchRetirementGate struct {
	DSN                string
	DryRun             bool
	StopWriteConfirmed bool
	BackupConfirmed    bool
}

func (g accountNameSearchRetirementGate) validate() error {
	dsn := strings.TrimSpace(g.DSN)
	if dsn == "" {
		return errors.New("drop-retired-account-name-search-index 需要 --dsn 指向显式的 PostgreSQL URL")
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return errors.New("drop-retired-account-name-search-index 的 --dsn 必须是 postgres:// 或 postgresql:// URL")
	}
	if g.DryRun {
		return nil
	}
	if !g.StopWriteConfirmed {
		return errors.New("drop-retired-account-name-search-index 正式执行需要 --stop-write-confirmed（gateway/jobs 已停止）")
	}
	if !g.BackupConfirmed {
		return errors.New("drop-retired-account-name-search-index 正式执行需要 --backup-confirmed（可恢复备份已验证）")
	}
	return nil
}

// accountNameSearchRetirementTableReport 是单张退役表的探测/清退结果。
type accountNameSearchRetirementTableReport struct {
	Table   string `json:"table"`
	Existed bool   `json:"existed"`
	Dropped bool   `json:"dropped"`
}

// accountNameSearchRetirementColumnReport 是投影索引列的探测/清退结果。
type accountNameSearchRetirementColumnReport struct {
	Table   string `json:"table"`
	Column  string `json:"column"`
	Existed bool   `json:"existed"`
	Dropped bool   `json:"dropped"`
}

// accountNameSearchRetirementFunctionReport 是名称搜索 dirty 触发器函数的
// 探测/清退结果。
type accountNameSearchRetirementFunctionReport struct {
	Function string `json:"function"`
	Existed  bool   `json:"existed"`
	Dropped  bool   `json:"dropped"`
}

// accountNameSearchRetirementReport 是 stdout 的单行 JSON 报告。
type accountNameSearchRetirementReport struct {
	Mode     string                                    `json:"mode"`
	Database string                                    `json:"database"`
	Tables   []accountNameSearchRetirementTableReport  `json:"tables"`
	Column   accountNameSearchRetirementColumnReport   `json:"column"`
	Function accountNameSearchRetirementFunctionReport `json:"function"`
	// Actions 是本命令计划（dry-run）或已执行（apply）的 DDL 清单。
	Actions  []string `json:"actions"`
	Rollback string   `json:"rollbackNote"`
}

const accountNameSearchRetirementRollbackNote = "回滚 = 用 --backup-confirmed 验证过的外部备份整体恢复对应数据库；" +
	"DROP 不可原地撤销，本报告不携带回滚 SQL。"

// runDropRetiredAccountNameSearchIndexCommand 是子命令入口，返回进程退出码；
// 门禁先于一切开库动作。
func runDropRetiredAccountNameSearchIndexCommand(argv []string) int {
	fs := flag.NewFlagSet("juhe-ai-maintenance drop-retired-account-name-search-index", flag.ContinueOnError)
	dsn := fs.String("dsn", "", "postgres URL of the business database")
	dryRun := fs.Bool("dry-run", false, "read-only report of the retired tables/column existence and planned actions; no writes and no confirmations required")
	stopWriteConfirmed := fs.Bool("stop-write-confirmed", false, "confirm gateway/jobs are stopped for the retirement")
	backupConfirmed := fs.Bool("backup-confirmed", false, "confirm a recoverable backup was verified")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	gate := accountNameSearchRetirementGate{
		DSN:                *dsn,
		DryRun:             *dryRun,
		StopWriteConfirmed: *stopWriteConfirmed,
		BackupConfirmed:    *backupConfirmed,
	}
	if err := gate.validate(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	db, err := openPostgresBootstrap(strings.TrimSpace(gate.DSN))
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开 PostgreSQL 连接失败: %v\n", err)
		return 1
	}
	defer db.Close()

	report, runErr := runAccountNameSearchRetirement(context.Background(), db, gate.DryRun, func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "[drop-retired-account-name-search-index] "+format+"\n", args...)
	})
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "账户名称搜索索引退役失败: %v\n", runErr)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode drop-retired-account-name-search-index report: %v\n", err)
		return 1
	}
	if report.Mode == "dry-run" {
		fmt.Fprintf(os.Stderr, "[drop-retired-account-name-search-index] dry-run 只读探测完成，未执行任何写入（函数 %s.%s() 存在=%t）\n",
			accountNameSearchRetiredSchema, accountNameSearchDirtyTriggerFunction, report.Function.Existed)
		return 0
	}
	for _, table := range report.Tables {
		if table.Dropped {
			fmt.Fprintf(os.Stderr, "[drop-retired-account-name-search-index] 已删除 %s.%s\n", accountNameSearchRetiredSchema, table.Table)
		} else {
			fmt.Fprintf(os.Stderr, "[drop-retired-account-name-search-index] 已缺席 %s.%s（幂等跳过）\n", accountNameSearchRetiredSchema, table.Table)
		}
	}
	if report.Column.Dropped {
		fmt.Fprintf(os.Stderr, "[drop-retired-account-name-search-index] 已删除 %s.%s.%s\n",
			accountNameSearchRetiredSchema, report.Column.Table, report.Column.Column)
	} else {
		fmt.Fprintf(os.Stderr, "[drop-retired-account-name-search-index] 已缺席 %s.%s.%s（幂等跳过）\n",
			accountNameSearchRetiredSchema, report.Column.Table, report.Column.Column)
	}
	if report.Function.Dropped {
		fmt.Fprintf(os.Stderr, "[drop-retired-account-name-search-index] 已删除函数 %s.%s()\n",
			accountNameSearchRetiredSchema, report.Function.Function)
	} else {
		fmt.Fprintf(os.Stderr, "[drop-retired-account-name-search-index] 已缺席函数 %s.%s()（幂等跳过）\n",
			accountNameSearchRetiredSchema, report.Function.Function)
	}
	return 0
}

// runAccountNameSearchRetirement 执行探测与（非 dry-run 时）单事务清退。
// dry-run 全程只读；apply 的探测在 BeginTx 之前只读完成，随后在单事务内
// 执行 5 条 DDL（3 DROP TABLE + 1 ALTER DROP COLUMN + 1 DROP FUNCTION）提交。
func runAccountNameSearchRetirement(ctx context.Context, db *sql.DB, dryRun bool, logf func(string, ...any)) (accountNameSearchRetirementReport, error) {
	if db == nil {
		return accountNameSearchRetirementReport{}, errors.New("账户名称搜索索引退役数据库未初始化")
	}
	if err := db.PingContext(ctx); err != nil {
		return accountNameSearchRetirementReport{}, fmt.Errorf("目标 PostgreSQL 不可达: %w", err)
	}
	var database string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		return accountNameSearchRetirementReport{}, fmt.Errorf("读取目标数据库名: %w", err)
	}
	mode := "apply"
	if dryRun {
		mode = "dry-run"
	}
	report := accountNameSearchRetirementReport{
		Mode:     mode,
		Database: database,
		Tables:   make([]accountNameSearchRetirementTableReport, 0, len(accountNameSearchRetiredTables)),
		Column: accountNameSearchRetirementColumnReport{
			Table:  accountNameSearchProjectionIndexTable,
			Column: accountNameSearchProjectionIndexCol,
		},
		Function: accountNameSearchRetirementFunctionReport{Function: accountNameSearchDirtyTriggerFunction},
		Rollback: accountNameSearchRetirementRollbackNote,
	}
	for _, table := range accountNameSearchRetiredTables {
		existed, err := accountNameSearchTableExists(ctx, db, table)
		if err != nil {
			return accountNameSearchRetirementReport{}, err
		}
		report.Tables = append(report.Tables, accountNameSearchRetirementTableReport{Table: table, Existed: existed})
	}
	columnExisted, err := accountNameSearchColumnExists(ctx, db, accountNameSearchProjectionIndexTable, accountNameSearchProjectionIndexCol)
	if err != nil {
		return accountNameSearchRetirementReport{}, err
	}
	report.Column.Existed = columnExisted
	functionExisted, err := accountNameSearchFunctionExists(ctx, db)
	if err != nil {
		return accountNameSearchRetirementReport{}, err
	}
	report.Function.Existed = functionExisted
	for _, table := range accountNameSearchRetiredTables {
		report.Actions = append(report.Actions, "DROP TABLE IF EXISTS "+accountNameSearchRetiredSchema+"."+table)
	}
	report.Actions = append(report.Actions, "ALTER TABLE IF EXISTS "+accountNameSearchRetiredSchema+"."+accountNameSearchProjectionIndexTable+
		" DROP COLUMN IF EXISTS "+accountNameSearchProjectionIndexCol)
	report.Actions = append(report.Actions, "DROP FUNCTION IF EXISTS "+accountNameSearchRetiredSchema+"."+accountNameSearchDirtyTriggerFunction+"()")

	if dryRun {
		logf("dry-run：目标库 %s，三张表存在状态 %v，列 %s.%s 存在=%t，函数 %s() 存在=%t；不执行任何写入",
			database, tableExistenceSummary(report.Tables), accountNameSearchProjectionIndexTable,
			accountNameSearchProjectionIndexCol, columnExisted, accountNameSearchDirtyTriggerFunction, functionExisted)
		return report, nil
	}

	logf("apply：在目标库 %s 单事务清退 %d 张表、1 列与触发器函数 %s()",
		database, len(accountNameSearchRetiredTables), accountNameSearchDirtyTriggerFunction)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return accountNameSearchRetirementReport{}, fmt.Errorf("开启退役事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// 停写失效时快速失败：锁等待超过 2s 即中止，避免维护窗口长时间阻塞。
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '2s'`); err != nil {
		return accountNameSearchRetirementReport{}, fmt.Errorf("设置事务锁超时: %w", err)
	}
	logf("执行：SET LOCAL lock_timeout = '2s'")
	for index, table := range accountNameSearchRetiredTables {
		statement := report.Actions[index]
		logf("执行：%s", statement)
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return accountNameSearchRetirementReport{}, fmt.Errorf("删除 %s.%s: %w", accountNameSearchRetiredSchema, table, err)
		}
		report.Tables[index].Dropped = report.Tables[index].Existed
	}
	columnStatement := report.Actions[len(accountNameSearchRetiredTables)]
	logf("执行：%s", columnStatement)
	if _, err := tx.ExecContext(ctx, columnStatement); err != nil {
		return accountNameSearchRetirementReport{}, fmt.Errorf("删除 %s.%s.%s: %w",
			accountNameSearchRetiredSchema, accountNameSearchProjectionIndexTable, accountNameSearchProjectionIndexCol, err)
	}
	report.Column.Dropped = report.Column.Existed
	functionStatement := report.Actions[len(accountNameSearchRetiredTables)+1]
	logf("执行：%s", functionStatement)
	if _, err := tx.ExecContext(ctx, functionStatement); err != nil {
		return accountNameSearchRetirementReport{}, fmt.Errorf("删除函数 %s.%s(): %w",
			accountNameSearchRetiredSchema, accountNameSearchDirtyTriggerFunction, err)
	}
	report.Function.Dropped = report.Function.Existed
	if functionExisted {
		logf("已删除函数 %s.%s()", accountNameSearchRetiredSchema, accountNameSearchDirtyTriggerFunction)
	} else {
		logf("已缺席函数 %s.%s()（幂等跳过）", accountNameSearchRetiredSchema, accountNameSearchDirtyTriggerFunction)
	}
	if err := tx.Commit(); err != nil {
		return accountNameSearchRetirementReport{}, fmt.Errorf("提交退役事务: %w", err)
	}
	return report, nil
}

// accountNameSearchTableExists 用 to_regclass 探测 juhe_business 下一张表。
func accountNameSearchTableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var found sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT to_regclass($1)`, accountNameSearchRetiredSchema+"."+table).Scan(&found); err != nil {
		return false, fmt.Errorf("探测 %s.%s: %w", accountNameSearchRetiredSchema, table, err)
	}
	return found.Valid && strings.TrimSpace(found.String) != "", nil
}

// accountNameSearchColumnExists 用 information_schema.columns 探测目标列。
func accountNameSearchColumnExists(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2 AND column_name = $3`,
		accountNameSearchRetiredSchema, table, column).Scan(&count); err != nil {
		return false, fmt.Errorf("探测 %s.%s.%s: %w", accountNameSearchRetiredSchema, table, column, err)
	}
	return count > 0, nil
}

// accountNameSearchFunctionExists 用 to_regprocedure 探测名称搜索 dirty
// 触发器函数（函数不依赖表，DROP TABLE 不会连带删除）。
func accountNameSearchFunctionExists(ctx context.Context, db *sql.DB) (bool, error) {
	var missing bool
	if err := db.QueryRowContext(ctx, `SELECT to_regprocedure($1) IS NULL`,
		accountNameSearchRetiredSchema+"."+accountNameSearchDirtyTriggerFunction+"()").Scan(&missing); err != nil {
		return false, fmt.Errorf("探测函数 %s.%s(): %w",
			accountNameSearchRetiredSchema, accountNameSearchDirtyTriggerFunction, err)
	}
	return !missing, nil
}

// tableExistenceSummary 生成 stderr 进度用的短清单。
func tableExistenceSummary(tables []accountNameSearchRetirementTableReport) map[string]bool {
	summary := make(map[string]bool, len(tables))
	for _, table := range tables {
		summary[table.Table] = table.Existed
	}
	return summary
}
