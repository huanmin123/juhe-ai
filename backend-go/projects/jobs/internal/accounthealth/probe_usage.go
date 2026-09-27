package accounthealth

import (
	"context"
	"strings"
	"time"
)

// BUG-0194 方案 B：探针观测补记使用记录。
//
// Node 单体时代，健康探针经 /v1 account-health-check 派发链执行，使用记录
// （traffic_source=account_health_check / cooldown_retest /
// runtime_recovery_probe）是请求管道的自然产物；使用记录页的来源筛选、
// account_health_hourly 聚合与全部用量统计都以它为源。Go 三项目形态下 J1
// 由 jobs 直连上游执行（probe.go，去跨进程战役第二刀），这段副作用丢失。
//
// 本端口把真实执行的探针观测交回使用记录写入面（组合根装配为
// usagewriter.Writer + 业务库作用域解析）；`stale`（输入失效未执行）与
// `probe_task_failure`（输入级确定性失败，无上游请求）不是真实探针观测，
// 不补记。作用域五元组由适配器按业务库解析，归一化
// （usagewriter.NormalizeUsageRecordInput）作为完整性安全网。

// Traffic source values for probe observations（与 usagewriter 常量同源，
// 漂移由 probe_usage_test.go 对照锚定）。
const (
	ProbeTrafficSourceAccountHealthCheck = "account_health_check"
	ProbeTrafficSourceCooldownRetest     = "cooldown_retest"
	ProbeTrafficSourceRuntimeRecovery    = "runtime_recovery_probe"
)

// ProbeUsageObservation 是一次真实探针执行的使用记录投影输入。
type ProbeUsageObservation struct {
	AccountID     string
	TrafficSource string
	Model         string
	Endpoint      string
	Success       bool
	StatusCode    int
	ErrorCode     string
	ErrorMessage  string
	ObservedAt    time.Time
}

// ProbeUsageRecorder 是使用记录写入窄口（组合根装配 usagewriter.Writer +
// 作用域解析；缺席时跳过）。
type ProbeUsageRecorder interface {
	RecordProbeUsage(ctx context.Context, observation ProbeUsageObservation) error
}

// SetProbeUsageRecorder 绑定使用记录写入面（runner 构造后、supervisor 启动
// 前调用；与 SetProbeRequestDrain 同款回绑模式）。
func (r *Runner) SetProbeUsageRecorder(recorder ProbeUsageRecorder) {
	r.usageRecorder = recorder
}

// probeTrafficSourceForKind 计划扫描分类 → 流量来源。
func probeTrafficSourceForKind(kind string) string {
	if kind == "cooldown_retest" {
		return ProbeTrafficSourceCooldownRetest
	}
	return ProbeTrafficSourceAccountHealthCheck
}

// probeTrafficSourceForReason 显式派发 reason → 流量来源：cooldown 显式复测
// → cooldown_retest；runtime 恢复/重置族 → runtime_recovery_probe；其余
// （request_failure 健康确认、激活等）→ account_health_check。
func probeTrafficSourceForReason(reason string) string {
	lower := strings.ToLower(reason)
	switch {
	case strings.Contains(lower, "cooldown"):
		return ProbeTrafficSourceCooldownRetest
	case strings.Contains(lower, "recovery"), strings.Contains(lower, "reset"):
		return ProbeTrafficSourceRuntimeRecovery
	default:
		return ProbeTrafficSourceAccountHealthCheck
	}
}

// endpointFamilyForMode mirrors direct_input_reader.go 的 endpoint mode →
// 端点族映射（使用记录 endpoint 列与统计族同口径）。
func endpointFamilyForMode(mode string) string {
	switch mode {
	case "chat_json", "chat_sse":
		return "chat_completions"
	case "responses_json", "responses_sse":
		return "responses"
	case "messages_json", "messages_sse":
		return "messages"
	case "generate_content_json":
		return "generate_content"
	case "generate_content_sse":
		return "stream_generate_content"
	case "interactions_json", "interactions_sse":
		return "interactions"
	case "images_json":
		return "images"
	default:
		return ""
	}
}

// recordProbeUsage 在 outcome 持久化成功后补记使用记录；失败 warn 继续
// （使用记录为旁路观测面，不阻塞健康管道主职责）。
func (r *Runner) recordProbeUsage(ctx context.Context, outcome Outcome, input Input, trafficSource string) {
	if r.usageRecorder == nil {
		return
	}
	// 非真实探针观测：stale（输入失效未执行）、task_failure（输入级确定性
	// 失败，未发起上游请求）不补记。
	if outcome.Outcome == OutcomeStale || outcome.Outcome == OutcomeTaskFailed {
		return
	}
	observation := ProbeUsageObservation{
		AccountID:     outcome.AccountID,
		TrafficSource: trafficSource,
		Model:         input.HealthModel,
		Endpoint:      endpointFamilyForMode(input.EndpointMode),
		Success:       outcome.Outcome == OutcomeSuccess,
		StatusCode:    outcome.StatusCode,
		ErrorCode:     outcome.ErrorCode,
		ErrorMessage:  outcome.ErrorMessage,
		ObservedAt:    outcome.ObservedAt,
	}
	if err := r.usageRecorder.RecordProbeUsage(ctx, observation); err != nil {
		r.logger.Warn("探针使用记录写入失败，不影响健康管道",
			"event", "account_health_probe_usage_record_failed",
			"accountId", outcome.AccountID, "outcomeId", outcome.OutcomeID, "error", err.Error())
	}
}
