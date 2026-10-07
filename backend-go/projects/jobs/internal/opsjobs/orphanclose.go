package opsjobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// 孤儿 incident 结清扫描：业务库 ledger 中 state<>'CLOSED' 的行，若运行态
// （Redis 同键空间）已无对应键且超过宽限期，按 CLOSED 结清投影回 ledger。
// 对齐设计 §566"缺失 key 只有在 due 索引也没有成员、没有用户策略阻断且没有
// 在途 fencing 记录时才能按 CLOSED 处理"：
//   - PERSISTING / SHADOWED_BY_PERSISTENT → 持久冷却交接态不是孤儿，跳过；
//   - 行租约活跃（lease_until_ms > now）→ 在途确认/canary，跳过；
//   - 运行态仍有键 → sweep/主链会推进，跳过；
//   - 缺键但未超宽限期 → 等待（覆盖重启/重建窗口）。
//
// 结清走与投影器同一 CAS 管道（TransitionID 加 orphan-close: 前缀保证 dedupe
// 幂等键唯一），CAS 围栏外的行（dispatch revision 失配/账户已删）由 CAS 返回
// 终态丢弃——这些行已不进任何读面，等 Cleanup 或人工处理。冲突回填重试前
// 复查运行态存在性，避免把"Get 缺键 → CAS 提交"窗口内被等代复开的新行结清。

// DefaultOrphanGracePeriodMS 是缺键行结清前的宽限期（覆盖进程重启/运行态
// 重建窗口；期间行保持 pending）。
const DefaultOrphanGracePeriodMS int64 = 10 * 60_000

// DefaultOrphanScanPageSize 是单页读取行数（keyset 游标推进）。
const DefaultOrphanScanPageSize = 200

// orphanScanBudgetPerSweep 限制单轮扫描总行数，防止大表长事务；超出部分等
// 下一轮维护继续推进。
const orphanScanBudgetPerSweep = 2000

// orphanCloseTransitionPrefix 使结清 CAS 的 dedupe key 与正常 mutation 以及
// 重复结清互不冲突且幂等。
const orphanCloseTransitionPrefix = "orphan-close:"

// IncidentActiveLister 是不带 dispatch_revision 围栏的活动行分页读 port
// （circuitstore.ControlPlaneRepo.ListActiveIncidentsPage）。
type IncidentActiveLister interface {
	ListActiveIncidentsPage(ctx context.Context, afterUpdatedAtMS int64, afterScopeKey string, limit int) ([]IncidentCASRow, error)
}

// OrphanCloseOptions 数值均为可测 options，不新增 env。
type OrphanCloseOptions struct {
	// Lister 提供活动行分页读；必填。
	Lister IncidentActiveLister
	// GracePeriodMS 缺键行结清宽限期；<=0 用默认 10 分钟。
	GracePeriodMS int64
	// ClosedRetentionMS 结清 CLOSED 行的 tombstone 保留窗；<=0 用默认 5 分钟。
	ClosedRetentionMS int64
	NowMS             func() int64
	Logger            *slog.Logger
}

// OrphanCloseResult 计数供任务日志（每行恰好计入一个非 Scanned 分类）。
type OrphanCloseResult struct {
	Scanned         int `json:"scanned"`
	RunningLeased   int `json:"runningLeased"`
	RuntimePresent  int `json:"runtimePresent"`
	SkippedHandover int `json:"skippedHandover"`
	SkippedReopened int `json:"skippedReopened"`
	ClosedRetired   int `json:"closedRetired"`
	ConflictRetired int `json:"conflictRetired"`
	StaleSkipped    int `json:"staleSkipped"`
	GracePending    int `json:"gracePending"`
	InvalidSkipped  int `json:"invalidSkipped"`
	Errors          int `json:"errors"`
}

// OrphanIncidentCloser 依赖运行态 store（存在性判定）与 ledger CAS 写。
type OrphanIncidentCloser struct {
	store             CircuitStore
	cas               IncidentCASPort
	lister            IncidentActiveLister
	gracePeriodMS     int64
	closedRetentionMS int64
	nowMS             func() int64
	logger            *slog.Logger
}

// NewOrphanIncidentCloser 构建结清扫描器；依赖缺失返回错误。
func NewOrphanIncidentCloser(store CircuitStore, cas IncidentCASPort, options OrphanCloseOptions) (*OrphanIncidentCloser, error) {
	if store == nil {
		return nil, errors.New("孤儿 incident 结清缺少运行态 store")
	}
	if cas == nil {
		return nil, errors.New("孤儿 incident 结清缺少 CAS port")
	}
	if options.Lister == nil {
		return nil, errors.New("孤儿 incident 结清缺少活动行读面")
	}
	if options.NowMS == nil {
		return nil, errors.New("孤儿 incident 结清必须注入 NowMS 时钟")
	}
	gracePeriodMS := options.GracePeriodMS
	if gracePeriodMS <= 0 {
		gracePeriodMS = DefaultOrphanGracePeriodMS
	}
	closedRetentionMS := options.ClosedRetentionMS
	if closedRetentionMS <= 0 {
		closedRetentionMS = DefaultIncidentClosedRetentionMS
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &OrphanIncidentCloser{
		store:             store,
		cas:               cas,
		lister:            options.Lister,
		gracePeriodMS:     gracePeriodMS,
		closedRetentionMS: closedRetentionMS,
		nowMS:             options.NowMS,
		logger:            logger,
	}, nil
}

// Sweep 执行一轮结清：keyset 分页扫完全部活动行（或用尽总预算）。
func (c *OrphanIncidentCloser) Sweep(ctx context.Context, limit int) (OrphanCloseResult, error) {
	result := OrphanCloseResult{}
	pageLimit := limit
	if pageLimit < 1 {
		pageLimit = DefaultOrphanScanPageSize
	}
	now := c.nowMS()
	afterUpdatedAtMS := int64(-1)
	afterScopeKey := ""
	for result.Scanned < orphanScanBudgetPerSweep {
		rows, err := c.lister.ListActiveIncidentsPage(ctx, afterUpdatedAtMS, afterScopeKey, pageLimit)
		if err != nil {
			return result, fmt.Errorf("读取账户电路活跃 incident 失败: %w", err)
		}
		if len(rows) == 0 {
			return result, nil
		}
		for i := range rows {
			row := rows[i]
			result.Scanned++
			afterUpdatedAtMS = row.UpdatedAtMS
			afterScopeKey = row.CircuitScopeKey
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			c.closeRow(ctx, row, now, &result)
		}
		if len(rows) < pageLimit {
			return result, nil
		}
	}
	return result, nil
}

// closeRow 按交接态→租约→作用域→运行态→宽限期顺序判定，只对"缺键且超宽
// 限"的行发起 CAS 结清。
func (c *OrphanIncidentCloser) closeRow(ctx context.Context, row IncidentCASRow, now int64, result *OrphanCloseResult) {
	// PERSISTING / SHADOWED_BY_PERSISTENT 是设计 §647-648 的持久冷却交接
	// 态：当前无写方但属持久事实源，不是孤儿；结清会抹掉交接事实，交由其
	// 状态机闭环。
	if row.State == string(CircuitIncidentPersisting) || row.State == string(CircuitIncidentShadowedByPersistent) {
		result.SkippedHandover++
		return
	}
	if row.LeaseUntilMS != nil && *row.LeaseUntilMS > now {
		result.RunningLeased++
		return
	}
	record := circuitIncidentRecordFromCASRow(row)
	state, err := IncidentToRuntimeState(record, incidentScopeKeyMap([]CircuitIncidentRecord{record}))
	if err != nil {
		// scopeKey 与字段不一致 / lane 词表越界等历史脏行：跳过不阻断，
		// 结构化日志定位（不含凭据与上游响应）。
		result.InvalidSkipped++
		c.logger.Warn("孤儿 incident 结清跳过：作用域构造失败",
			"scopeKey", row.CircuitScopeKey, "accountRuntimeKey", row.AccountRuntimeKey, "error", err)
		return
	}
	runtimeState, err := c.store.Get(ctx, state.Scope, now)
	if err != nil {
		result.Errors++
		c.logger.Warn("孤儿 incident 结清读取运行态失败",
			"scopeKey", row.CircuitScopeKey, "accountRuntimeKey", row.AccountRuntimeKey, "error", err)
		return
	}
	// 运行态缺键的形状是 closed 零值（dispatchRevision 空串，与 Redis Lua
	// get / MemoryCircuitStore 的缺失响应一致）。
	if strings.TrimSpace(runtimeState.DispatchRevision) != "" {
		result.RuntimePresent++
		return
	}
	if row.UpdatedAtMS+c.gracePeriodMS >= now {
		result.GracePending++
		return
	}
	c.closeRowCAS(ctx, row, state.Scope, result)
}

// closeRowCAS 构造 CLOSED 输入结清：事实字段照抄行值（含
// last_failure_class，不再跑 classify），仅覆盖 state / retainedUntil /
// transitionID；expected 取行 ledger_revision（读面即当前行，围栏失配由
// CAS 冲突路径兜底重试）。
func (c *OrphanIncidentCloser) closeRowCAS(ctx context.Context, row IncidentCASRow, scope CircuitScope, result *OrphanCloseResult) {
	closed := row
	closed.State = string(CircuitIncidentClosed)
	retainedUntil := c.nowMS() + c.closedRetentionMS
	closed.RetainedUntilMS = &retainedUntil
	closed.TransitionID = orphanCloseTransitionPrefix + row.TransitionID
	expected := row.LedgerRevision
	for attempt := 0; ; attempt++ {
		out, err := c.cas.CompareAndSetIncident(ctx, IncidentCASInput{
			Incident:               closed,
			ExpectedLedgerRevision: &expected,
		})
		if err != nil {
			result.Errors++
			c.logger.Warn("孤儿 incident 结清 CAS 失败", "scopeKey", row.CircuitScopeKey, "error", err)
			return
		}
		switch out.Status {
		case IncidentCASApplied, IncidentCASIdempotent:
			result.ClosedRetired++
			return
		case IncidentCASConflict:
			if attempt < projectorMaxConflictRetries && out.Incident != nil {
				// 冲突重试前复查运行态存在性："Get 缺键 → CAS 提交"窗口
				// 内该 scope 可能已被 gateway 以等 generation 复开（更高
				// generation 会被 CAS 拦，等代不拦），盲目重试会把新 OPEN
				// 行结清为 CLOSED。运行态存在即放弃本次结清，不重试；
				// 复查失败按未知运行态处理，同样不重试。
				recheck, err := c.store.Get(ctx, scope, c.nowMS())
				if err != nil {
					result.Errors++
					c.logger.Warn("孤儿 incident 结清复查运行态失败", "scopeKey", row.CircuitScopeKey, "error", err)
					return
				}
				// 缺键形状是 closed 零值（dispatchRevision 空串，与 closeRow
				// 首查一致）。
				if strings.TrimSpace(recheck.DispatchRevision) != "" {
					result.SkippedReopened++
					return
				}
				expected = out.Incident.LedgerRevision
				continue
			}
			result.ConflictRetired++
			return
		case IncidentCASAccountNotFound, IncidentCASStaleDispatchRevision:
			// revision 已失配或账户已删：行已不进任何读面（围栏读面全部带
			// dispatch 围栏/deleted_at 过滤），等 Cleanup 或人工处理。
			result.StaleSkipped++
			return
		default:
			result.Errors++
			c.logger.Warn("孤儿 incident 结清返回未知状态",
				"scopeKey", row.CircuitScopeKey, "status", string(out.Status))
			return
		}
	}
}

// circuitIncidentRecordFromCASRow 把 CAS 全字段行降为窄投影记录（仅供
// IncidentToRuntimeState 构造作用域使用）。
func circuitIncidentRecordFromCASRow(row IncidentCASRow) CircuitIncidentRecord {
	record := CircuitIncidentRecord{
		AccountID:                       row.AccountID,
		AccountRuntimeKey:               row.AccountRuntimeKey,
		IncidentID:                      row.IncidentID,
		CircuitScopeKey:                 row.CircuitScopeKey,
		ScopeKind:                       row.ScopeKind,
		ChildIncidentIDs:                row.ChildIncidentIDs,
		State:                           CircuitIncidentState(row.State),
		Generation:                      row.Generation,
		DispatchRevision:                row.DispatchRevision,
		LedgerRevision:                  row.LedgerRevision,
		TransitionID:                    row.TransitionID,
		BackoffLevel:                    int(row.BackoffLevel),
		ConsecutiveFailures:             int(row.ConsecutiveFailures),
		ConfirmationFailuresRequired:    int(row.ConfirmationFailuresRequired),
		ConfirmationFailureEvidenceKeys: row.ConfirmationFailureEvidenceKeys,
		RecoveringSuccesses:             int(row.RecoveringSuccesses),
		UpdatedAtMS:                     row.UpdatedAtMS,
	}
	record.ParentIncidentID = derefOrEmpty(row.ParentIncidentID)
	record.KeyFingerprint = derefOrEmpty(row.KeyFingerprint)
	record.ProtocolCode = derefOrEmpty(row.ProtocolCode)
	record.RequestLane = derefOrEmpty(row.RequestLane)
	record.ModelFamily = derefOrEmpty(row.ModelFamily)
	record.LeaseID = derefOrEmpty(row.LeaseID)
	record.LeasePurpose = derefOrEmpty(row.LeasePurpose)
	if row.LeaseUntilMS != nil {
		value := *row.LeaseUntilMS
		record.LeaseUntilMS = &value
	}
	if row.NextTransitionAtMS != nil {
		value := *row.NextTransitionAtMS
		record.NextTransitionAtMS = &value
	}
	if row.OpenUntilMS != nil {
		value := *row.OpenUntilMS
		record.OpenUntilMS = &value
	}
	record.LastFailureClass = derefOrEmpty(row.LastFailureClass)
	return record
}

func derefOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
