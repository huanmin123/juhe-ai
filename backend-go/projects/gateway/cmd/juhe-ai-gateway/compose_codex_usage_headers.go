package main

// Codex / Anthropic 用量响应头持久化的组合根装配：把链条的 fire-and-forget
// 派发口（gatewaycodex.PersistOpenAICodexHeadersIfNeeded 的
// CodexUsageHeadersDispatcher 窄口 + gatewaycodex.PersistAnthropicUsageHeaders
// IfNeeded 的 AnthropicUsageHeadersDispatcher 窄口，AI账户Grok用量快照设计
// §8.2：失败面 chain_ports.go / 成功面 chain_v1.go 调用）接到
// record_maintenance_jobs 持久交接表的 account_usage_snapshot_upsert 行
// （gateway internal/tablemonitor 写入 → jobs internal/recordmaintenance
// drain → 执行器 account_usage_snapshots upsert）。
//
// Node 契约逐段对照：
//   - runtime/account-effects.ts:111-128 persistOpenAICodexHeadersIfNeeded：
//     fire-and-forget requestGatewayDbService（priority: low），失败 catch-warn
//     （event gateway_codex_usage_snapshot_side_effect_failed），不阻塞请求面；
//   - adapters/gpt-codex/usage.service.ts:67-87
//     persistOpenAICodexUsageHeadersAsync / buildOpenAICodexUsageRecordMaintenanceJob：
//     job 形状 { type: 'account_usage_snapshot_upsert', accountId,
//     kind: 'openai_codex', source, snapshot, updatedAt }；
//   - payload 投影（usage.service.ts:96-127，含 5h/7d 归一与 reset_at）在
//     gatewaydispatch/usageheaders.go 已逐字段移植，本适配器只做通道转换。

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/tablemonitor"
	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

// codexUsageHeadersChannelDispatcher 实现 gatewaycodex.CodexUsageHeadersDispatcher
// 与 gatewaycodex.AnthropicUsageHeadersDispatcher：复用既有
// recordMaintenanceDispatch 通道把 codex / anthropic 快照 job 落到交接表。
type codexUsageHeadersChannelDispatcher struct {
	dispatch *tablemonitor.DurableDispatch
}

// newCodexUsageHeadersChannelDispatcher 构造派发适配器（dispatch 为 nil 时
// 返回 nil，链条侧对 nil 派发器保持静默契约）。
func newCodexUsageHeadersChannelDispatcher(dispatch *tablemonitor.DurableDispatch) gatewaycodex.CodexUsageHeadersDispatcher {
	if dispatch == nil {
		return nil
	}
	return codexUsageHeadersChannelDispatcher{dispatch: dispatch}
}

// newAnthropicUsageHeadersChannelDispatcher 构造 anthropic 侧的同一通道适配
// 器（nil 契约同上；两个窄口共享同一 recordMaintenanceDispatch 实例）。
func newAnthropicUsageHeadersChannelDispatcher(dispatch *tablemonitor.DurableDispatch) gatewaycodex.AnthropicUsageHeadersDispatcher {
	if dispatch == nil {
		return nil
	}
	return codexUsageHeadersChannelDispatcher{dispatch: dispatch}
}

// PersistOpenAICodexUsageHeaders 镜像 persistOpenAICodexUsageHeadersAsync +
// account-effects.ts 的 fire-and-forget：job 构造与入队都不阻塞请求面
// （goroutine 承接 Node priority:low 队列语义），入队失败按 Node catch 分支
// 记 warn（不静默、不 panic、不改请求结果）。
func (d codexUsageHeadersChannelDispatcher) PersistOpenAICodexUsageHeaders(ctx context.Context, accountID string, headers http.Header, source string) {
	job := gatewaydispatch.BuildOpenAICodexUsageRecordMaintenanceJob(accountID, headers, source)
	if job == nil {
		return
	}
	d.enqueueUsageSnapshotJob(ctx, accountID, source, job, "gateway_codex_usage_snapshot_side_effect_failed", "OpenAI Codex 用量快照副作用写入失败")
}

// PersistAnthropicUsageHeaders 是 anthropic unified rate limit 头的对称派发
// 面（AI账户Grok用量快照设计 §8.2）：同一 record_maintenance_jobs 通道，
// job kind anthropic_claude；fire-and-forget 与失败 warn 契约同 codex。
func (d codexUsageHeadersChannelDispatcher) PersistAnthropicUsageHeaders(ctx context.Context, accountID string, headers http.Header, source string) {
	job := gatewaydispatch.BuildAnthropicUsageRecordMaintenanceJob(accountID, headers, source)
	if job == nil {
		return
	}
	d.enqueueUsageSnapshotJob(ctx, accountID, source, job, "gateway_anthropic_usage_snapshot_side_effect_failed", "Anthropic 用量快照副作用写入失败")
}

// enqueueUsageSnapshotJob 承接 fire-and-forget 入队：请求 ctx 随响应结束
// 取消；派发生命周期独立于单个请求（Node 低优先级队列同样跨请求存活）。
func (d codexUsageHeadersChannelDispatcher) enqueueUsageSnapshotJob(ctx context.Context, accountID, source string, job *gatewaydispatch.RecordMaintenanceJob, failureEvent, failureMessage string) {
	// 请求 ctx 随响应结束取消；派发生命周期独立于单个请求（Node 低优先级
	// 队列同样跨请求存活）。
	dispatchCtx := context.WithoutCancel(ctx)
	go func() {
		defer safego.Recover("juheai.compose_codex_usage_headers.persist")
		result := d.dispatch.EnqueueAccountUsageSnapshotUpsert(dispatchCtx, tablemonitor.RecordMaintenanceSnapshotJob{
			AccountID: job.AccountID,
			Kind:      job.Kind,
			Source:    job.Source,
			Snapshot:  job.Snapshot,
			UpdatedAt: job.UpdatedAt,
		})
		if !result.Queued {
			slog.Warn(failureMessage,
				"event", failureEvent,
				"accountId", accountID,
				"source", source,
				"droppedReason", result.DroppedReason)
		}
	}()
}
