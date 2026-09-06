package main

// Codex 用量响应头持久化的组合根装配：把链条的 fire-and-forget 派发口
// （gatewaycodex.PersistOpenAICodexHeadersIfNeeded 的 CodexUsageHeadersDispatcher
// 窄口，成功面 chain_usage.go / 失败面 chain_ports.go 调用）接到
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
	"net/http"
	"log/slog"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/tablemonitor"
)

// codexUsageHeadersChannelDispatcher 实现 gatewaycodex.CodexUsageHeadersDispatcher：
// 复用既有 recordMaintenanceDispatch 通道把快照 job 落到交接表。
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

// PersistOpenAICodexUsageHeaders 镜像 persistOpenAICodexUsageHeadersAsync +
// account-effects.ts 的 fire-and-forget：job 构造与入队都不阻塞请求面
// （goroutine 承接 Node priority:low 队列语义），入队失败按 Node catch 分支
// 记 warn（不静默、不 panic、不改请求结果）。
func (d codexUsageHeadersChannelDispatcher) PersistOpenAICodexUsageHeaders(ctx context.Context, accountID string, headers http.Header, source string) {
	job := gatewaydispatch.BuildOpenAICodexUsageRecordMaintenanceJob(accountID, headers, source)
	if job == nil {
		return
	}
	// 请求 ctx 随响应结束取消；派发生命周期独立于单个请求（Node 低优先级
	// 队列同样跨请求存活）。
	dispatchCtx := context.WithoutCancel(ctx)
	go func() {
		result := d.dispatch.EnqueueAccountUsageSnapshotUpsert(dispatchCtx, tablemonitor.RecordMaintenanceSnapshotJob{
			AccountID: job.AccountID,
			Kind:      job.Kind,
			Source:    job.Source,
			Snapshot:  job.Snapshot,
			UpdatedAt: job.UpdatedAt,
		})
		if !result.Queued {
			slog.Warn("OpenAI Codex 用量快照副作用写入失败",
				"event", "gateway_codex_usage_snapshot_side_effect_failed",
				"accountId", accountID,
				"source", source,
				"droppedReason", result.DroppedReason)
		}
	}()
}
