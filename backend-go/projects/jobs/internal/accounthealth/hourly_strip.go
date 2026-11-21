package accounthealth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
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
// 观测自愈；T218 起写入前置到消费水位打标之前——写失败停轮保留消费水位，
// 下轮重放重试（见 projection.go DrainOnce），不再 warn 后永久丢失。

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

// nonQualityHealthOutcomes 是不计入 AI 健康小时条带的非质量终态，与
// gateway statreads j1NonQualityOutcomes、统计口径 §2.9 逐字成对
// （upstream_http_failure 的历史存量别名 framing_complete_neutral 仅读存量）。
// 跳过后该小时由后续质量终态 outcome 决定（或保持无条 = unknown），两臂与
// 读面 J1 排除口径收敛（T217）。修改名单必须三处同步。
var nonQualityHealthOutcomes = map[string]bool{
	"stale":                    true,
	"upstream_http_failure":    true,
	"framing_complete_neutral": true,
	"opaque_upstream":          true,
	"gateway_capacity":         true,
	"gateway_policy":           true,
	"downstream_closed":        true,
}

// recordAccountHealthHourlyObservation 把一个 outcome 转成条带观测并写入：
// `complete_success` → success，质量终态失败（upstream_failure /
// probe_task_failure 等）→ failure；`stale` 不是真实探针观测，非质量终态
// （upstream_http_failure 等）不计健康率，均跳过（T217 与读面口径统一）。
// 账户已删除（无 owner/provider 行）时跳过。返回错误供调用方决定游标策略。
func recordAccountHealthHourlyObservation(ctx context.Context, sink AccountHealthHourlySink, business *ProjectionBusinessDB, outcome Outcome) error {
	if sink == nil || nonQualityHealthOutcomes[outcome.Outcome] || outcome.AccountID == "" {
		return nil
	}
	// 供应商解耦：条带的 provider 维度取 accounts.vendor_code；异常缺失值按
	// "unknown" 归档（DB CHECK 已禁止空 vendor 账户）。
	var vendorCode, systemAccountID sql.NullString
	err := business.db.QueryRowContext(ctx, business.bind(`SELECT COALESCE(vendor_code, ''), system_account_id FROM `+business.table("accounts")+` WHERE id = ?`), outcome.AccountID).Scan(&vendorCode, &systemAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取 J1 探针观测的账户归属失败: %w", err)
	}
	if systemAccountID.String == "" {
		return nil
	}
	provider := vendorCode.String
	if provider == "" {
		provider = "unknown"
	}
	observation := AccountHealthHourlyObservation{
		AccountID:       outcome.AccountID,
		ProviderCode:    provider,
		SystemAccountID: systemAccountID.String,
		ObservedAt:      outcome.ObservedAt,
		OutcomeID:       outcome.OutcomeID,
		Success:         outcome.Outcome == exactkeyprobe.OutcomeSuccess,
		StatusCode:      outcome.StatusCode,
		ErrorCode:       outcome.ErrorCode,
		ErrorMessage:    outcome.ErrorMessage,
	}
	if err := sink.RecordAccountHealthHourly(ctx, observation); err != nil {
		return fmt.Errorf("写入 J1 探针小时条带失败: %w", err)
	}
	return nil
}

// recordHourly 是投影循环的条带写入口（T218 前置）：调用方把它排在
// projectOutcome（消费水位打标）之前，失败上抛停轮——该 outcome 未打标
// 消费，下轮 drain 重放重试（条带 upsert newest-wins 幂等）。取舍：stats
// 库故障会暂停 J1 投影消费（current_state 滞后），换取消费过的 outcome
// 小时条必不丢失；stats 恢复后从队头自动续跑。
func (p *OutcomeProjector) recordHourly(ctx context.Context, outcome Outcome) error {
	if p.hourly == nil {
		return nil
	}
	return recordAccountHealthHourlyObservation(ctx, p.hourly, p.business, outcome)
}
