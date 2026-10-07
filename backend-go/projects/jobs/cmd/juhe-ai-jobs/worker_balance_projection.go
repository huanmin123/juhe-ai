package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
)

// account-balance-stats-projection 组合根适配器，对齐归档 Node
// account-balance-jobs-projector：J2 快照 → stats relay_balance 投影。
//
// 生产事实（2026-09-26）：juhe_stats.account_usage_snapshots 的 relay_balance
// 行数为 0（唯一写入方是 account-balance-auto-detect-recovery 的首探补偿路径，
// 周期快照只落 juhe_jobs），而 gateway 列表/明细读端全部读 stats relay_balance
// 行——Node 归档有 jobs-projector 专门做 jobs→stats 投影，Go 侧缺失，本任务补上：
//   - 数据流：全量读 juhe_jobs.account_balance_snapshots（J2 余额服务每周期
//     写入、秒级新鲜）→ 逐行 UPSERT 到 juhe_stats.account_usage_snapshots
//     kind='relay_balance'。无游标（当前 98 行级别），幂等：重复执行结果一致
//     （同一冲突键覆盖同一载荷）；
//   - system_account_id 从业务库 accounts join 取得（stats 主键是
//     (system_account_id, account_id, kind)，J2 行不含 owner 命名空间；
//     账户缺失/已删除的行跳过并计数，不阻塞其余行）；
//   - snapshot_json 映射为 gateway 读端期望的 camelCase 形状
//     （balanceSnapshotPersist 字段集 + 多 Key 字段透传，见
//     projectBalanceSnapshotJSON）；configRevision 取 juhe_jobs 行的
//     config_revision 列（gateway 列表/明细匹配围栏的关键：快照必须携带
//     与账户当前 config_revision 相等的 configRevision）；
//   - next_refresh_after 取 J2 行的 next_refresh_at（gateway 围栏要求它与
//     账户 balance_query_next_refresh_at 毫秒相等或双空，J2 周期推进与
//     AdvancePeriodicDue 同源同值）；
//   - 列集合、主键/冲突目标与 opsjobs 路径 ReplaceSnapshotIfCurrent 完全一致
//     （维护冻结 DDL 归 maintenance 项目，本任务不建表只校验）；
//   - J2 Go owner 仅支持 PostgreSQL（JUHE_AI_ACCOUNT_BALANCE_STORE=postgres），
//     SQLite 部署无 juhe_jobs.account_balance_snapshots → 登记 disabled。
//
// 独立 lane=balance-projection，与 external-account-maintenance 的余额族错峰，
// 避免相互争抢 lane 并发槽。

// balanceProjectionRuntime 承载投影读写的单一双模句柄：PG 模式下 juhe_jobs /
// juhe_business / juhe_stats 三个 schema 同库，业务池一个连接即可；SQLite 模式
// 仅测试构造使用（生产 SQLite 部署在装配层登记 disabled）。
type balanceProjectionRuntime struct {
	db       *sql.DB
	postgres bool
	nowFunc  func() time.Time
}

// balanceProjectionResult 是一轮投影的计数汇总。
type balanceProjectionResult struct {
	// ScannedCount 是 juhe_jobs.account_balance_snapshots 读到的行数。
	ScannedCount int
	// ProjectedCount 是成功 UPSERT 的行数。
	ProjectedCount int
	// SkippedCount 是业务库账户缺失/已删除导致无法落主键的行数。
	SkippedCount int
}

// sourceTable 限定 J2 快照表名（PG 走 juhe_jobs schema；SQLite 测试直名）。
func (r *balanceProjectionRuntime) sourceTable() string {
	if r.postgres {
		return "juhe_jobs.account_balance_snapshots"
	}
	return "account_balance_snapshots"
}

// businessTable 限定业务表名（PG 走 juhe_business schema；SQLite 测试直名）。
func (r *balanceProjectionRuntime) businessTable(name string) string {
	if r.postgres {
		return "juhe_business." + name
	}
	return name
}

// projectSourceRow 是 juhe_jobs.account_balance_snapshots 的读取投影。
type projectSourceRow struct {
	AccountID      string
	ConfigRevision int64
	SnapshotJSON   string
	NextRefreshAt  *time.Time
}

// ProjectAll 执行一轮全量投影：读 J2 快照 → join 业务账户 → 逐行 UPSERT。
// 任一行失败即返回错误（契约破坏必须显式失败，不静默降级）。
func (r *balanceProjectionRuntime) ProjectAll(ctx context.Context) (balanceProjectionResult, error) {
	result := balanceProjectionResult{}
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
    SELECT account_id, config_revision, snapshot_json, next_refresh_at
    FROM %s
    ORDER BY account_id
  `, r.sourceTable()))
	if err != nil {
		return result, fmt.Errorf("读取 J2 余额快照失败: %w", err)
	}
	source := make([]projectSourceRow, 0, 128)
	for rows.Next() {
		var (
			row          projectSourceRow
			snapshotJSON sql.NullString
			nextRefresh  any
		)
		if err := rows.Scan(&row.AccountID, &row.ConfigRevision, &snapshotJSON, &nextRefresh); err != nil {
			rows.Close()
			return result, err
		}
		row.SnapshotJSON = snapshotJSON.String
		if row.NextRefreshAt, err = scanNullTime(r.postgres, nextRefresh); err != nil {
			rows.Close()
			return result, fmt.Errorf("解析 J2 快照 next_refresh_at 失败: %w", err)
		}
		source = append(source, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	rows.Close()
	result.ScannedCount = len(source)

	owners, err := r.loadSystemAccountIDs(ctx, source)
	if err != nil {
		return result, err
	}
	for _, row := range source {
		systemAccountID, ok := owners[row.AccountID]
		if !ok {
			result.SkippedCount++
			continue
		}
		if err := r.projectRow(ctx, row, systemAccountID); err != nil {
			return result, err
		}
		result.ProjectedCount++
	}
	return result, nil
}

// loadSystemAccountIDs 按 900 分块 IN 读取快照账户的 owner 命名空间
// （模式对齐 gateway loadAccountBalanceSnapshotRecordsByAccountIdsAsync 的
// 900 分块；只取未删除账户）。
func (r *balanceProjectionRuntime) loadSystemAccountIDs(ctx context.Context, rows []projectSourceRow) (map[string]string, error) {
	ids := make([]string, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		if row.AccountID == "" || seen[row.AccountID] {
			continue
		}
		seen[row.AccountID] = true
		ids = append(ids, row.AccountID)
	}
	owners := make(map[string]string, len(ids))
	const chunkSize = 900
	for start := 0; start < len(ids); start += chunkSize {
		end := start + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		placeholders := ""
		args := make([]any, 0, len(chunk))
		for index, id := range chunk {
			if index > 0 {
				placeholders += ", "
			}
			placeholders += "?"
			args = append(args, textParam(id))
		}
		query := fmt.Sprintf(`
      SELECT id, system_account_id
      FROM %s
      WHERE deleted_at IS NULL AND id IN (%s)
    `, r.businessTable("accounts"), placeholders)
		dbRows, err := r.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("读取投影账户 owner 失败: %w", err)
		}
		for dbRows.Next() {
			var id, systemAccountID string
			if err := dbRows.Scan(&id, &systemAccountID); err != nil {
				dbRows.Close()
				return nil, err
			}
			owners[id] = systemAccountID
		}
		if err := dbRows.Err(); err != nil {
			dbRows.Close()
			return nil, err
		}
		dbRows.Close()
	}
	return owners, nil
}

// projectBalanceSnapshot 把 J2 快照 JSON 映射为 stats relay_balance 行的
// 持久化形状：同时返回映射视图（供列值提取）与其 JSON 序列化（供
// snapshot_json 落列），一次解析两者共用。字段集对齐 balanceSnapshotPersist
// （status/configRevision/remainingUsd/rawRemaining/rawUnit/basis/errorMessage/
// lastAttemptAt/lastSuccessAt/瞬时失败三字段）+ 多 Key 字段透传（keyCount/
// queriedKeyCount/scope/aggregation/keyBalances，元素键名即 shared KeyBalance
// 的 camelCase tag）。J2 JSON 本就是 shared Snapshot 的 camelCase 序列化
// （gateway 读端按同名键消费），映射为直通 + configRevision 覆写；未知键在
// 反序列化时丢弃（只放行契约字段）。
func projectBalanceSnapshot(snapshotJSON string, configRevision int64) (*balanceSnapshotPersist, string, error) {
	var source accountbalance.Snapshot
	if err := json.Unmarshal([]byte(snapshotJSON), &source); err != nil {
		return nil, "", fmt.Errorf("解析 J2 余额快照 JSON 失败: %w", err)
	}
	if source.Status == "" {
		return nil, "", errors.New("J2 余额快照缺少 status")
	}
	view := balanceSnapshotFromJ2(&source, configRevision)
	serialized, err := json.Marshal(view)
	if err != nil {
		return nil, "", err
	}
	return view, string(serialized), nil
}

// balanceSnapshotFromJ2 是 J2 Snapshot → balanceSnapshotPersist 的逐字段映射
// （纯函数，供投影与测试共用）：remainingUsd 为规范小数文本直通（gateway 读端
// 按字符串消费，无货币换算），lastAttemptAt/lastSuccessAt 为 RFC3339 文本直通，
// 瞬时失败三字段直通（pending/failed 瞬态快照的三振计数与最近失败消息/时间，
// 成功快照零值省略），多 Key 五字段原样透传，configRevision 一律取 juhe_jobs
// 行的 config_revision 列（J2 JSON 内无该字段，这也是 gateway 匹配围栏的权威
// 来源）。
func balanceSnapshotFromJ2(source *accountbalance.Snapshot, configRevision int64) *balanceSnapshotPersist {
	view := &balanceSnapshotPersist{
		Status:                    string(source.Status),
		ConfigRevision:            configRevision,
		LastAttemptAt:             source.LastAttemptAt,
		LastSuccessAt:             source.LastSuccessAt,
		ConsecutiveTransientFails: source.ConsecutiveTransientFails,
		LastTransientErrorMessage: source.LastTransientErrorMessage,
		LastTransientFailureAt:    source.LastTransientFailureAt,
		KeyCount:                  source.KeyCount,
		QueriedKeyCount:           source.QueriedKeyCount,
		Scope:                     source.Scope,
		Aggregation:               source.Aggregation,
	}
	// BUG-0286：输入身份摘要透传（J2 执行核统一打点，gateway 读端匹配）。
	view.InputDigest = source.InputDigest
	view.RemainingUSD = optionalString(source.RemainingUSD)
	view.RawRemaining = optionalString(source.RawRemaining)
	view.RawUnit = optionalString(string(source.RawUnit))
	view.Basis = optionalString(string(source.Basis))
	view.ErrorMessage = optionalString(source.ErrorMessage)
	if len(source.KeyBalances) > 0 {
		view.KeyBalances = source.KeyBalances
	}
	return view
}

// projectRow 把一行 J2 快照 UPSERT 到 stats（列集合/冲突目标/
// next_refresh_after 语义与 ReplaceSnapshotIfCurrent 一致）。
func (r *balanceProjectionRuntime) projectRow(ctx context.Context, row projectSourceRow, systemAccountID string) error {
	persist, serialized, err := projectBalanceSnapshot(row.SnapshotJSON, row.ConfigRevision)
	if err != nil {
		return fmt.Errorf("投影账户 %s 快照失败: %w", row.AccountID, err)
	}
	now := r.nowFunc().UTC()
	lastAttempt := now.Format(time.RFC3339Nano)
	if persist.LastAttemptAt != "" {
		lastAttempt = persist.LastAttemptAt
	}
	var lastSuccess any
	if persist.LastSuccessAt != "" {
		lastSuccess = persist.LastSuccessAt
	}
	var nextRefreshAfter any
	if row.NextRefreshAt != nil {
		nextRefreshAfter = timeParam(r.postgres, *row.NextRefreshAt)
	}
	upsert := fmt.Sprintf(`
    INSERT INTO %s (
      system_account_id, account_id, kind, source, snapshot_json, refresh_status,
      last_attempt_at, last_success_at, next_refresh_after, last_error_message, updated_at, created_at
    ) VALUES (?, ?, 'relay_balance', 'upstream_api', ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, account_id, kind) DO UPDATE SET
      source = excluded.source,
      snapshot_json = excluded.snapshot_json,
      refresh_status = excluded.refresh_status,
      last_attempt_at = excluded.last_attempt_at,
      last_success_at = excluded.last_success_at,
      next_refresh_after = excluded.next_refresh_after,
      last_error_message = excluded.last_error_message,
      updated_at = excluded.updated_at
  `, statsTable(r.postgres, "account_usage_snapshots"))
	if _, err := r.db.ExecContext(ctx, upsert,
		textParam(systemAccountID), textParam(row.AccountID), textParam(serialized), textParam(persist.Status),
		textParam(lastAttempt), lastSuccess, nextRefreshAfter, nullableTextPtr(persist.ErrorMessage),
		timeParam(r.postgres, now), timeParam(r.postgres, now)); err != nil {
		return fmt.Errorf("写入余额快照投影失败: %w", err)
	}
	return nil
}

// ensureBalanceProjectionContract 校验投影两端表存在（冻结 schema；缺失时
// fail closed 登记 disabled，不静默建表——生产 DDL 归 maintenance 项目，
// juhe_jobs.account_balance_snapshots 由 shared accountbalance store 契约管理）。
func ensureBalanceProjectionContract(ctx context.Context, db *sql.DB, postgres bool) error {
	if postgres {
		var count int
		check := `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'juhe_jobs' AND table_name = 'account_balance_snapshots'`
		if err := db.QueryRowContext(ctx, check).Scan(&count); err != nil {
			return err
		}
		if count < 1 {
			return errors.New("缺少 juhe_jobs.account_balance_snapshots 表")
		}
	}
	return ensureAccountUsageSnapshotsTable(ctx, db, postgres)
}

// wireBalanceStatsProjectionFamily 装配 account-balance-stats-projection。
// 任一依赖缺失→登记 disabled 并说明（不阻塞其他 job）。
func (a *workerAssembly) wireBalanceStatsProjectionFamily(ctx context.Context) error {
	name := "account-balance-stats-projection"
	if a.config.Driver != "postgres" {
		a.registerDisabledJob(name, "J2 投影链 PG-only（juhe_jobs.account_balance_snapshots 仅 PG 形态存在）；SQLite 部署无投影源：周期快照由 balance-detect 直写 stats、手动刷新由 gateway 进程内直写 stats")
		return nil
	}
	business, err := openBusinessDB(a, "balance-projection-business")
	if err != nil {
		return err
	}
	if err := ensureBalanceProjectionContract(ctx, business.db, true); err != nil {
		a.registerDisabledJob(name, "统计库投影契约校验失败："+err.Error())
		_ = business.close()
		return nil
	}
	runtime := &balanceProjectionRuntime{
		db:       business.db,
		postgres: true,
		nowFunc:  func() time.Time { return time.Now().UTC() },
	}
	a.addCloser(business.close)
	a.scheduleWiredJob(name, func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		result, err := runtime.ProjectAll(taskCtx)
		if err != nil {
			return jobsched.TaskResult{}, err
		}
		a.logger.Info("AI 账户余额快照投影完成",
			"event", "account_balance_stats_projection_completed",
			"scannedCount", result.ScannedCount,
			"projectedCount", result.ProjectedCount,
			"skippedCount", result.SkippedCount)
		return jobsched.TaskResult{}, nil
	})
	return nil
}
