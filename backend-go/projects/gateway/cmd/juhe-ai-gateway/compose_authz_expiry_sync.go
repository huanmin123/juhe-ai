package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
	"github.com/huanminabc/juhe-ai/backend-go-platform/supervisor"
)

// authz-expiry-runtime-sync 组件（T6d 登记缺口的 gateway 侧最小消费面装配）：
// jobs 的 resource-authorization-expiry-sweep 把到期 grant 翻转为 expired 并
// 写健康任务输入 fanout/统计脏标记；runtime 投影（resource_authorizations +
// effective source）与 quota 窗口 scope bindings 属 gateway authz sync 域
// （jobregistry GoBinding 冻结登记）。本组件按固定节拍调用
// authz.Store.ReconcileExpiredGrants，对窗口内已翻转的 grant 幂等重放这两段
// 下游同步——无论 jobs 与 gateway 谁先观察到到期，投影都会收敛。
//
// 错误语义：单轮失败仅告警并等下一轮（投影收敛是尽力而为的补偿面，不得
// 因短暂 DB 抖动拖垮 system-api owner）；组件本身只在 ctx 取消时退出。
func newAuthzExpiryRuntimeSyncComponent(store *authz.Store) supervisor.Component {
	return supervisor.Component{
		Name: "authz-expiry-runtime-sync",
		Run: func(runCtx context.Context) error {
			ticker := time.NewTicker(authzExpirySyncInterval)
			defer ticker.Stop()
			runPass := func() {
				ctx, cancel := context.WithTimeout(runCtx, authzExpirySyncPassTimeout)
				defer cancel()
				reconciled, err := store.ReconcileExpiredGrants(ctx, 0, 0)
				if err != nil {
					slog.Warn("授权过期 runtime 投影重同步失败，等待下一轮",
						"event", "authz_expiry_runtime_sync_failed", "error", err)
					return
				}
				if reconciled > 0 {
					slog.Info("授权过期 runtime 投影重同步完成",
						"event", "authz_expiry_runtime_sync_reconciled", "grants", reconciled)
				}
			}
			runPass()
			for {
				select {
				case <-runCtx.Done():
					return runCtx.Err()
				case <-ticker.C:
					runPass()
				}
			}
		},
	}
}

// 节拍与 jobs sweep（1 分钟 + 抖动）同量级；单轮超时远小于节拍，避免堆积。
const (
	authzExpirySyncInterval    = time.Minute
	authzExpirySyncPassTimeout = 30 * time.Second
)
