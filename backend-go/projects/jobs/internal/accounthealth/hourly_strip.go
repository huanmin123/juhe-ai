package accounthealth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// BUG-0194 第二层：AI 健康监控读模型断源修复。
//
// `account_health_hourly`（AI 健康监控唯一读源，statreads/aihealth.go）在
// statsagg 侧只从 `traffic_source='account_health_check'` 的使用记录聚合
// （statsagg/aggregate.go addPostgresAggregatedAccountHealthEntry）。而 Go
// 三项目形态下 J1 探针由 jobs 直连上游执行（probe.go），不产生使用记录——
// Node 单体时代探针与记录队列同进程，这段副作用在迁移中丢失，读模型恒空。
//
// 修复：投影面在消费 outcome 时把每次探针观测直写统计库小时条带（newest-
// wins，口径与 statsagg 写入方逐列一致）。statsagg 原聚合臂保留不动：未来若
// 探针流量经网关产生使用记录，两源自然并存收敛。条带为派生数据、可由后续
// 观测自愈，因此写失败按 warn 继续，不阻塞 current_state 投影主职责。

// AccountHealthHourlyObservation 是一次探针观测的小时条带投影输入。
type AccountHealthHourlyObservation struct {
	AccountID       string
	ProviderCode    string // 空 → "unknown"（对齐 statsagg 聚合口径）
	SystemAccountID string // 空 → 观测跳过（stats 表 NOT NULL，且 self 视图按其过滤）
	ObservedAt      time.Time
	OutcomeID       string
	Success         bool
	StatusCode      int
	ErrorCode       string
	ErrorMessage    string
}

// AccountHealthHourlySink 是统计库小时条带的写入窄口（组合根装配为 stats 库
// 双模句柄；缺席时投影面跳过并在装配期 warn）。
type AccountHealthHourlySink interface {
	RecordAccountHealthHourly(ctx context.Context, observation AccountHealthHourlyObservation) error
}

// recordAccountHealthHourlyObservation 把一个 outcome 转成条带观测并写入：
// `complete_success` → success，其余 outcome（neutral/upstream/task failure）
// → failure（与 J1 投影自身的失败归类一致）；`stale` 不是真实探针观测，跳过。
// 账户已删除（无 owner/provider 行）时跳过。返回错误供调用方决定游标策略。
func recordAccountHealthHourlyObservation(ctx context.Context, sink AccountHealthHourlySink, business *ProjectionBusinessDB, outcome Outcome) error {
	if sink == nil || outcome.Outcome == OutcomeStale || outcome.AccountID == "" {
		return nil
	}
	var providerCode, systemAccountID sql.NullString
	err := business.db.QueryRowContext(ctx, business.bind(`SELECT provider_code, system_account_id FROM `+business.table("accounts")+` WHERE id = ?`), outcome.AccountID).Scan(&providerCode, &systemAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取 J1 探针观测的账户归属失败: %w", err)
	}
	if systemAccountID.String == "" {
		return nil
	}
	provider := providerCode.String
	if provider == "" {
		provider = "unknown"
	}
	observation := AccountHealthHourlyObservation{
		AccountID:       outcome.AccountID,
		ProviderCode:    provider,
		SystemAccountID: systemAccountID.String,
		ObservedAt:      outcome.ObservedAt,
		OutcomeID:       outcome.OutcomeID,
		Success:         outcome.Outcome == OutcomeSuccess,
		StatusCode:      outcome.StatusCode,
		ErrorCode:       outcome.ErrorCode,
		ErrorMessage:    outcome.ErrorMessage,
	}
	if err := sink.RecordAccountHealthHourly(ctx, observation); err != nil {
		return fmt.Errorf("写入 J1 探针小时条带失败: %w", err)
	}
	return nil
}

// recordHourly 是投影循环的条带写入口：失败 warn 继续（条带为派生数据，可由
// 同账户下一轮观测自愈），不阻塞 current_state 投影主职责。
func (p *OutcomeProjector) recordHourly(ctx context.Context, outcome Outcome) {
	if p.hourly == nil {
		return
	}
	if err := recordAccountHealthHourlyObservation(ctx, p.hourly, p.business, outcome); err != nil {
		p.logger.Warn("J1 探针小时条带写入失败，等待同账户下轮观测覆盖",
			"event", "account_health_hourly_record_failed",
			"accountId", outcome.AccountID, "outcomeId", outcome.OutcomeID, "error", err.Error())
	}
}
