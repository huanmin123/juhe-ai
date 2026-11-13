package oauthrefresh

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/quotahourly"
	"github.com/huanminabc/juhe-ai/backend-go-platform/dbtime"
)

// Availability schedule gate sync ×2（《时间计划闸门与账户状态解耦设计》§4.1）：
// due rows are evaluated for the current availability window, and the derived
// gate column (availability_schedule_gate: NULL = 无计划 / 1 = 时段内 / 0 = 时段外)
// plus the next-check column are advanced for every scanned row.
//
// 2026-11-03 契约变更：时间计划不再写 status。status 只表达账户 / Key 自身的
// 管理与健康事实（人工启停、待检查、异常、冷却、限流），计划只维护 gate 这一
// 个派生事实。由此：
//   - 人工停用持久生效，不会被下一个窗口开始边界自动复活；
//   - 时段外表现为「正常状态 + 非时间段」（列表侧派生），不再表现为停用；
//   - gate 每轮按「当前时刻是否在允许窗口内」重算，错过边界由下一轮天然纠正，
//     不再需要 T91-7 的事件补偿分支；
//   - *_schedule_status_events 保留为翻转审计（status 列读作闸门开/关），
//     不再承担「决定是否写」的去重职责。
//
// 旧文案留档（供 git 历史对照）：原实现 mirrors
// syncApiKeyAvailabilityScheduleStatuses(Async) /
// syncAccountAvailabilityScheduleStatuses(Async)，在窗口边界写 status。

// DefaultScheduleSyncBatchLimit mirrors
// runtimeConfig.background.{apiKeyScheduleSyncBatchLimit,
// accountAvailabilityScheduleSyncBatchLimit} (default 500).
const DefaultScheduleSyncBatchLimit = 500

// ScheduleStatusSyncResult 汇总一轮闸门同步。Activated / Disabled 现在表示
// gate 的 0/NULL→1 与 1→0 翻转（不再表示 status 变化）；Invalid 统计本轮无法
// 应用的行（计划文档不可解析，以及 Key 在闸门打开时额度文档无法判定小时窗口）。
type ScheduleStatusSyncResult struct {
	Scanned    int
	Activated  int
	Disabled   int
	Unchanged  int
	Skipped    int
	Invalid    int
	ChangedIDs []string
	InvalidIDs []string
}

// ActivationHook runs the activation side effects of an account schedule sync
// (Node advanceAccountCircuitDispatchRevisionFamily*): the gateway circuit
// control plane lives in the gateway module, so the jobs wiring supplies the
// implementation; nil keeps the status flip only. The hook receives the sync's
// own transaction exactly like the Node archive (the side effect runs inside
// the same tx as the status flip); opening a second transaction inside the
// hook would self-deadlock on the uncommitted flip (SQLite single writer / PG
// row lock), so implementations must use the given tx.
type ActivationHook interface {
	OnAccountActivated(ctx context.Context, tx *sql.Tx, accountID string, nowIso string) error
}

// ActivationHookFunc adapts a function to ActivationHook.
type ActivationHookFunc func(ctx context.Context, tx *sql.Tx, accountID string, nowIso string) error

// OnAccountActivated implements ActivationHook.
func (f ActivationHookFunc) OnAccountActivated(ctx context.Context, tx *sql.Tx, accountID, nowIso string) error {
	return f(ctx, tx, accountID, nowIso)
}

// APIKeyActivationHook runs the activation side effects of an api_key schedule
// sync. The schedule flip itself writes only api_keys.status, but an hourly
// quota API Key additionally needs its request_quota_hourly_window_scope_bindings
// row: the binding table is the only source statsagg uses to build
// usage_quota_hourly_windows, so a key activated without it keeps an hourly
// quota that silently reads zero cost (B3-H11). gateway internal/apikeys owns
// the same write for manual create/edit; jobs wires the schedule-side rebuild
// because it owns the flip transaction.
//
// The hook runs inside the sync transaction (same tx as the status flip), so a
// hook error rolls the flip back instead of leaving an active key without its
// binding. The returned afterCommit callback runs only after the transaction
// commits (dirty-scope marking); its failures must be handled by the callback
// and never fail the committed sync, mirroring gateway's after-commit dirty
// mark.
type APIKeyActivationHook interface {
	OnAPIKeyActivated(ctx context.Context, tx *sql.Tx, apiKeyID string, nowIso string) (afterCommit func(), err error)
}

// APIKeyActivationHookFunc adapts a function to APIKeyActivationHook.
type APIKeyActivationHookFunc func(ctx context.Context, tx *sql.Tx, apiKeyID string, nowIso string) (func(), error)

// OnAPIKeyActivated implements APIKeyActivationHook.
func (f APIKeyActivationHookFunc) OnAPIKeyActivated(ctx context.Context, tx *sql.Tx, apiKeyID, nowIso string) (func(), error) {
	return f(ctx, tx, apiKeyID, nowIso)
}

// scheduleSyncUpdate 承载一行的闸门求值结果。gate 为 nil 表示本轮只推进
// next_check_at（无计划文档，或目标与现值为 NULL 对齐）；非 nil 即目标闸门值
// （1 = 时段内，0 = 时段外）。
type scheduleSyncUpdate struct {
	id          string
	eventKey    string // "" = 不写审计事件
	gate        *int   // nil 且 clearGate=false 表示只推进下次检查
	clearGate   bool   // true = 把闸门显式写回 NULL（计划已清除）
	nextCheckAt string
	// scheduleJSON 是求值时读到的计划原文，作为写回时的 CAS 栅栏：求值事务外
	// 读行、写回在事务内，期间 PATCH/批量可能改写计划；栅栏确保「用旧计划算出
	// 的 next_check / gate」不会覆盖新计划的结果（覆盖会把 next_check 推到旧
	// 计划的远端边界，导致新窗口整个不会被扫描）。
	scheduleJSON string
}

// gateWord 是 gate 值的事件表词形（沿用 active/disabled 词表，审计兼容）。
func gateWord(gate int) string {
	if gate == 1 {
		return "active"
	}
	return "disabled"
}

// SyncApiKeyScheduleStatuses 执行 API Key 闸门同步。hook 在生产路径必填：闸门
// 由关到开时要重建该 Key 的小时额度窗口绑定（B3-H11）；nil 仅保留闸门写入，
// 供只断言裸闸门语义的测试使用。
func (s *Store) SyncApiKeyScheduleStatuses(ctx context.Context, now time.Time, batchLimit int, hook APIKeyActivationHook) (ScheduleStatusSyncResult, error) {
	rows, err := s.listScheduleRows(ctx, s.table("api_keys"), false, batchLimit, isoMillis(now))
	if err != nil {
		return ScheduleStatusSyncResult{}, err
	}
	updates, result, err := s.evaluateScheduleRows(rows, now, false)
	if err != nil {
		return ScheduleStatusSyncResult{}, err
	}
	activation := func(tx *sql.Tx, update scheduleSyncUpdate) (func(), error) {
		if hook == nil || update.gate == nil || *update.gate != 1 {
			return nil, nil
		}
		return hook.OnAPIKeyActivated(ctx, tx, update.id, isoMillis(now))
	}
	if err := s.applyScheduleUpdates(ctx, s.table("api_keys"), s.table("api_key_schedule_status_events"), "api_key_id", false, updates, isoMillis(now), &result, activation); err != nil {
		return ScheduleStatusSyncResult{}, err
	}
	bumpScheduleVersions(ctx, result.ChangedIDs, s.apiKeyStateBumpFunc(), "api-key")
	return result, nil
}

// SyncAccountScheduleStatuses 执行账户闸门同步。闸门与 status 正交：任何 status
// 的行都可以维护闸门，闸门本身不改变账户可用性事实；激活副作用（dispatch
// revision 家族推进）只在闸门由关到开时触发。
func (s *Store) SyncAccountScheduleStatuses(ctx context.Context, now time.Time, batchLimit int, hook ActivationHook) (ScheduleStatusSyncResult, error) {
	rows, err := s.listScheduleRows(ctx, s.table("accounts"), true, batchLimit, isoMillis(now))
	if err != nil {
		return ScheduleStatusSyncResult{}, err
	}
	updates, result, err := s.evaluateScheduleRows(rows, now, true)
	if err != nil {
		return ScheduleStatusSyncResult{}, err
	}
	activation := func(tx *sql.Tx, update scheduleSyncUpdate) (func(), error) {
		if hook == nil || update.gate == nil || *update.gate != 1 {
			return nil, nil
		}
		return nil, hook.OnAccountActivated(ctx, tx, update.id, isoMillis(now))
	}
	if err := s.applyScheduleUpdates(ctx, s.table("accounts"), s.table("account_schedule_status_events"), "account_id", true, updates, isoMillis(now), &result, activation); err != nil {
		return ScheduleStatusSyncResult{}, err
	}
	bumpScheduleVersions(ctx, result.ChangedIDs, s.accountStateBumpFunc(), "account")
	return result, nil
}

// evaluateScheduleRows 求值每行的目标闸门：解析 → 目标 gate → 与现值比较。
// status 不参与求值也不再被改写。
func (s *Store) evaluateScheduleRows(rows []scheduleRow, now time.Time, accountMode bool) ([]scheduleSyncUpdate, ScheduleStatusSyncResult, error) {
	result := ScheduleStatusSyncResult{Scanned: len(rows)}
	updates := make([]scheduleSyncUpdate, 0, len(rows))
	for _, row := range rows {
		schedule, parseErr := ParseScheduleJSON(row.scheduleJSON)
		if parseErr != nil {
			// 计划文档不可解析：fail-closed 关闸，避免坏数据放行；但不能把
			// next_check 写成 NULL/空串——候选条件是 `IS NULL OR <= now`，
			// 坏行会因此永久 due 并占满批次。改写到 1 小时后重试；PATCH 修正
			// 计划会同时重算 gate/next_check，不必等重试。
			result.Invalid++
			result.InvalidIDs = append(result.InvalidIDs, row.id)
			closed := 0
			updates = append(updates, scheduleSyncUpdate{id: row.id, gate: &closed, nextCheckAt: isoMillis(now.Add(time.Hour)), scheduleJSON: row.scheduleJSON})
			continue
		}
		nextCheckAt, _ := NextScheduleCheckAt(schedule, now)
		target, ok := ScheduleStatus(schedule, now)
		if !ok {
			// 无计划文档（已清除 / 未启用）：闸门回到 NULL（恒开）。gate.Valid 说明
			// 库里有陈旧值，需要显式清空；已为 NULL 时只推进下次检查。
			// 同样不能写 NULL：availability_schedule_json 仍非 NULL 时该行会永久
			// 命中候选；写到 1 小时后重试，PATCH 启用计划会立即重算。
			retryAt := isoMillis(now.Add(time.Hour))
			if row.gate.Valid {
				updates = append(updates, scheduleSyncUpdate{id: row.id, clearGate: true, nextCheckAt: retryAt, scheduleJSON: row.scheduleJSON})
			} else {
				updates = append(updates, scheduleSyncUpdate{id: row.id, nextCheckAt: retryAt, scheduleJSON: row.scheduleJSON})
			}
			result.Unchanged++
			continue
		}
		gate := 0
		if target == "active" {
			gate = 1
		}
		if row.gate.Valid && int(row.gate.Int64) == gate {
			// 目标与现值一致：只推进下次检查。
			updates = append(updates, scheduleSyncUpdate{id: row.id, nextCheckAt: nextCheckAt, scheduleJSON: row.scheduleJSON})
			result.Unchanged++
			continue
		}
		if !accountMode && gate == 1 {
			// B3-H11：闸门打开必须能重建小时额度窗口绑定。额度文档无法判定小时
			// 窗口时不得开闸（否则小时额度不可判定），按「该行本轮无效」逐项隔离：
			// 只推进下次检查、保持现闸门，不影响同批其它 Key。
			if _, _, quotaErr := quotahourly.HourlyWindowHours(row.quotaLimitsJSON.String); quotaErr != nil {
				result.Invalid++
				result.InvalidIDs = append(result.InvalidIDs, row.id)
				updates = append(updates, scheduleSyncUpdate{id: row.id, nextCheckAt: nextCheckAt, scheduleJSON: row.scheduleJSON})
				continue
			}
		}
		targetGate := gate
		updates = append(updates, scheduleSyncUpdate{
			id:           row.id,
			eventKey:     row.id + ":" + gateWord(gate) + ":" + now.UTC().Format("2006-01-02T15:04"),
			gate:         &targetGate,
			nextCheckAt:  nextCheckAt,
			scheduleJSON: row.scheduleJSON,
		})
	}
	return updates, result, nil
}

// catchUpScheduleEvent 已随 2026-11-03 闸门契约删除：闸门每轮按「当前时刻是否
// 在允许窗口内」重算并与现值比较，错过边界天然由下一轮纠正，不再需要「最近
// 一个本应执行但尚未执行」的事件键补偿。保留此说明供 git 历史对照。

// scheduleRow 是同步候选行的投影。gate 为闸门现值（NULL = 尚未求值过 / 无
// 计划），用于判定目标是否已达成，也是升级后存量行的自愈入口。
type scheduleRow struct {
	id           string
	status       string
	scheduleJSON string
	nextCheckAt  sql.NullString
	gate         sql.NullInt64
	// quotaLimitsJSON 只在 api_keys 同步读取（闸门打开时要重建小时额度窗口
	// 绑定，B3-H11）；accounts 同步留空。
	quotaLimitsJSON sql.NullString
}

func (s *Store) listScheduleRows(ctx context.Context, table string, accountMode bool, batchLimit int, dueAt string) ([]scheduleRow, error) {
	if batchLimit <= 0 {
		batchLimit = DefaultScheduleSyncBatchLimit
	}
	columns := "id, status, availability_schedule_json, availability_schedule_next_check_at, availability_schedule_gate"
	if !accountMode {
		columns += ", quota_limits_json"
	}
	// gate IS NULL 也纳入候选：升级后的存量行闸门未求值，需要第一轮补齐；
	// 这也是唯一让新列对历史数据生效的入口（设计 §7.2 同步任务内建自愈）。
	query := `SELECT ` + columns + `
		FROM ` + table + `
		WHERE availability_schedule_json IS NOT NULL
			AND (availability_schedule_next_check_at IS NULL OR availability_schedule_next_check_at <= ? OR availability_schedule_gate IS NULL)`
	if accountMode {
		query += `
			AND deleted_at IS NULL`
	}
	query += `
		ORDER BY availability_schedule_next_check_at IS NOT NULL ASC, availability_schedule_next_check_at ASC, id ASC
		LIMIT ?`
	rows, err := s.db.QueryContext(ctx, s.bind(query), scheduleInstant(table, dueAt), batchLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	output := []scheduleRow{}
	for rows.Next() {
		var row scheduleRow
		if accountMode {
			if err := rows.Scan(&row.id, &row.status, &row.scheduleJSON, &row.nextCheckAt, &row.gate); err != nil {
				return nil, err
			}
		} else if err := rows.Scan(&row.id, &row.status, &row.scheduleJSON, &row.nextCheckAt, &row.gate, &row.quotaLimitsJSON); err != nil {
			return nil, err
		}
		output = append(output, row)
	}
	return output, rows.Err()
}

// applyScheduleUpdates 是两侧同步的事务半部。闸门是否写入由「目标与现值是否
// 一致」决定（已在求值阶段判定）；事件表只做审计，插入冲突不阻断闸门写入。
// activation 在闸门由关到开时于同一事务内执行，可返回 afterCommit 回调；
// 回调只在 Commit 成功后运行，自身失败由回调负责（已提交的同步不得被尾部
// 副作用改写为失败）。
func (s *Store) applyScheduleUpdates(
	ctx context.Context,
	table string,
	eventsTable string,
	entityColumn string,
	accountMode bool,
	updates []scheduleSyncUpdate,
	updatedAt string,
	result *ScheduleStatusSyncResult,
	activation func(tx *sql.Tx, update scheduleSyncUpdate) (afterCommit func(), err error),
) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	afterCommit := []func(){}
	for _, update := range updates {
		if update.gate == nil {
			if update.clearGate {
				if _, err := s.clearScheduleGate(ctx, tx, table, update.id, updatedAt, update.scheduleJSON); err != nil {
					return err
				}
			}
			if err := s.updateScheduleNextCheckAt(ctx, tx, table, update.id, update.nextCheckAt, update.scheduleJSON); err != nil {
				return err
			}
			continue
		}
		changed, err := s.applyScheduleGate(ctx, tx, table, accountMode, update, updatedAt)
		if err != nil {
			return err
		}
		if !changed {
			if err := s.updateScheduleNextCheckAt(ctx, tx, table, update.id, update.nextCheckAt, update.scheduleJSON); err != nil {
				return err
			}
			result.Unchanged++
			continue
		}
		// 审计事件：翻转已确定发生，事件键按目标值 + 分钟唯一，冲突视为已记账。
		if update.eventKey != "" {
			if _, err := s.insertScheduleStatusEvent(ctx, tx, eventsTable, entityColumn, update, updatedAt); err != nil {
				return err
			}
		}
		result.ChangedIDs = append(result.ChangedIDs, update.id)
		if *update.gate == 1 {
			if activation != nil {
				after, err := activation(tx, update)
				if err != nil {
					return err
				}
				if after != nil {
					afterCommit = append(afterCommit, after)
				}
			}
			result.Activated++
		} else {
			result.Disabled++
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, callback := range afterCommit {
		callback()
	}
	return nil
}

// clearScheduleGate 把闸门显式写回 NULL（计划已清除或未启用）。只在现值非空时
// 写入，避免无意义推进 updated_at。
func (s *Store) clearScheduleGate(ctx context.Context, tx *sql.Tx, table, id, updatedAt, scheduleJSON string) (bool, error) {
	// next_check 同时写 NULL：无计划语义下不该保留边界时间，否则该行会因
	// next_check_at <= now 每轮都进候选扫描。
	query := `UPDATE ` + table + `
		SET availability_schedule_gate = NULL, availability_schedule_next_check_at = NULL, updated_at = ?
		WHERE id = ?
			AND availability_schedule_gate IS NOT NULL
			AND availability_schedule_json = ?`
	executed, err := tx.ExecContext(ctx, s.bind(query), scheduleInstant(table, updatedAt), id, scheduleJSON)
	if err != nil {
		return false, err
	}
	affected, err := executed.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// insertScheduleStatusEvent 写一条闸门翻转审计（INSERT ... ON CONFLICT DO
// NOTHING）。返回值不再用于决定是否写闸门——去重职责已移到求值阶段的
// 「目标与现值比较」。
func (s *Store) insertScheduleStatusEvent(ctx context.Context, tx *sql.Tx, eventsTable, entityColumn string, update scheduleSyncUpdate, executedAt string) (bool, error) {
	query := `INSERT INTO ` + eventsTable + ` (event_key, ` + entityColumn + `, status, executed_at)
		VALUES (?, ?, ?, ?)`
	query += ` ON CONFLICT(event_key) DO NOTHING`
	result, err := tx.ExecContext(ctx, s.bind(query), update.eventKey, update.id, gateWord(*update.gate), scheduleInstant(eventsTable, executedAt))
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// applyScheduleGate 写目标闸门。守卫：
//   - 计划文档必须仍是求值时的同一份（`availability_schedule_json = ?` CAS）——
//     求值在事务外读行，写回在事务内；期间 PATCH / 批量可能改写计划，用旧计划
//     算出的 gate 与 next_check 一旦覆盖新结果，会把手闸与下次扫描推到旧计划的
//     远端边界，新窗口就整个不会被扫描；
//   - 闸门现值与目标不同。
//
// 不再守卫 status / schedulable / 质量处罚：闸门与可用性事实正交，任何 status
// 的行都维护闸门。
//
// updated_at 的刷新按方言语义区分：账户用 config_revision 做乐观锁，刷新它无
// 副作用；api_keys 用 updated_at 作为对外 revision 与 PATCH CAS 基准，闸门翻转
// 不得推进它，否则窗口边界会在用户编辑期间制造 409。
func (s *Store) applyScheduleGate(ctx context.Context, tx *sql.Tx, table string, accountMode bool, update scheduleSyncUpdate, updatedAt string) (bool, error) {
	setClause := `SET availability_schedule_gate = ?, availability_schedule_next_check_at = ?`
	args := []any{*update.gate, scheduleInstant(table, update.nextCheckAt)}
	if accountMode {
		setClause += `, updated_at = ?`
		args = append(args, scheduleInstant(table, updatedAt))
	}
	args = append(args, update.id, *update.gate, update.scheduleJSON)
	query := `UPDATE ` + table + `
		` + setClause + `
		WHERE id = ?
			AND availability_schedule_json IS NOT NULL
			AND (availability_schedule_gate IS NULL OR availability_schedule_gate <> ?)
			AND availability_schedule_json = ?`
	if accountMode {
		query += `
			AND deleted_at IS NULL`
	}
	executed, err := tx.ExecContext(ctx, s.bind(query), args...)
	if err != nil {
		return false, err
	}
	affected, err := executed.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// outerEntityName keeps the Node NOT EXISTS correlation name ("accounts")
// while allowing the api_keys variant to reference its own table.
func outerEntityName(table string) string {
	if strings.HasSuffix(table, "accounts") {
		return "accounts"
	}
	return "api_keys"
}

func (s *Store) updateScheduleNextCheckAt(ctx context.Context, tx *sql.Tx, table, id, nextCheckAt, scheduleJSON string) error {
	// 计划 CAS 同 applyScheduleGate：求值到写回之间计划被改写时放弃本行推进，
	// 由下一轮按新计划重算。
	query := `UPDATE ` + table + `
		SET availability_schedule_next_check_at = ?
		WHERE id = ?
			AND availability_schedule_json IS NOT NULL
			AND (availability_schedule_next_check_at IS NULL OR availability_schedule_next_check_at <> ?)
			AND availability_schedule_json = ?`
	_, err := tx.ExecContext(ctx, s.bind(query), scheduleInstant(table, nextCheckAt), id, scheduleInstant(table, nextCheckAt), scheduleJSON)
	return err
}

// scheduleInstant 决定时间参数的绑定类型：Revision 25/26 起 api_keys、accounts
// 及其事件表的时间列均已是 timestamptz，统一绑定 time.Time。
func scheduleInstant(table, value string) any {
	return dbtime.DBArg(value)
}

// accountStateBumpFunc / apiKeyStateBumpFunc 把可选端口转成 bump 函数（nil 安全）。
func (s *Store) accountStateBumpFunc() func(context.Context, string) error {
	if s.accountState == nil {
		return nil
	}
	return s.accountState.BumpAccountState
}

func (s *Store) apiKeyStateBumpFunc() func(context.Context, string) error {
	if s.apiKeyState == nil {
		return nil
	}
	return s.apiKeyState.BumpAPIKeyState
}

// bumpScheduleVersions 在闸门事务提交后按变更 ID 递增共享序号；失败只告警。
func bumpScheduleVersions(ctx context.Context, ids []string, bump func(context.Context, string) error, kind string) {
	if bump == nil || len(ids) == 0 {
		return
	}
	bumpCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	for _, id := range ids {
		if err := bump(bumpCtx, id); err != nil {
			slog.Warn("运行态版本栅栏写入失败，gateway 按既有缓存窗口回退",
				"event", "runtime_state_version_bump_failed",
				"kind", kind, "id", id, "error", err)
		}
	}
}
