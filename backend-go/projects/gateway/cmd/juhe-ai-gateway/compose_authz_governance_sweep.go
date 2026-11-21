package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
	"github.com/huanminabc/juhe-ai/backend-go-platform/supervisor"
)

// authz-governance-sweep 是接收方确认模型的两个周期收敛面：
//
//  1. offer/审批/成员邀请 TTL 到期（设计 §5.5/§8.5）。过期状态机与 runtime
//     投影都在 gateway/internal/authz，jobs 不能跨 module import；由本进程
//     周期调用 ExpireDueOffers，避免依赖某个请求恰好触发到期。
//  2. 来源修订对账（设计 §4.3）。账户 PATCH 与批量转移已在事务内打标；代理
//     资料、直连 SQL、旧导入等旁路写入只能靠周期扫描补偿，发现差异即把
//     source 转入 needs_review，等待接收方重新确认。
//
// 两段彼此独立：一段失败只告警并等待下一轮，不影响另一段；组件只在 ctx
// 取消时退出。intervalOverride 仅供测试注入短节拍。
func newAuthzGovernanceSweepComponent(store *authz.Store, intervalOverride ...time.Duration) supervisor.Component {
	interval := authzGovernanceSweepInterval
	if len(intervalOverride) > 0 && intervalOverride[0] > 0 {
		interval = intervalOverride[0]
	}
	return supervisor.Component{
		Name: "authz-governance-sweep",
		Run: func(runCtx context.Context) error {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			runPass := func() {
				now := time.Now().UTC().Format(time.RFC3339Nano)
				expireCtx, cancelExpire := context.WithTimeout(runCtx, authzGovernanceSweepPassTimeout)
				expired, err := store.ExpireDueOffers(expireCtx, now, authzGovernanceSweepBatch)
				cancelExpire()
				if err != nil {
					slog.Warn("授权 TTL 到期扫描失败，等待下一轮",
						"event", "authz_offer_expiry_sweep_failed", "error", err)
				} else if expired.GrantsExpired > 0 || expired.SourcesExpired > 0 {
					slog.Info("授权 TTL 到期扫描完成",
						"event", "authz_offer_expiry_sweep_done",
						"grants", expired.GrantsExpired, "sources", expired.SourcesExpired)
				}

				// 对账与到期共用同一个 now；扫描按资源去重，幂等，重复轮次只会
				// 重算相同修订。
				reconcileCtx, cancelReconcile := context.WithTimeout(runCtx, authzGovernanceSweepPassTimeout)
				processed, err := store.ReconcileSourceRevisions(reconcileCtx, now, authzGovernanceSweepBatch)
				cancelReconcile()
				if err != nil {
					slog.Warn("授权来源修订对账失败，等待下一轮",
						"event", "authz_source_revision_reconcile_failed", "error", err)
				} else if processed > 0 {
					slog.Info("授权来源修订对账完成",
						"event", "authz_source_revision_reconcile_done", "resources", processed)
				}

				// 存量授权复核截止（设计 §7.6 #10）：迁移后 30 天仍未重新确认的
				// legacy 来源转 needs_review；网关门禁随后硬阻断。
				legacyCtx, cancelLegacy := context.WithTimeout(runCtx, authzGovernanceSweepPassTimeout)
				legacyProcessed, err := store.ExpireLegacyReviews(legacyCtx, now, authzGovernanceSweepBatch)
				cancelLegacy()
				if err != nil {
					slog.Warn("存量授权复核截止扫描失败，等待下一轮",
						"event", "authz_legacy_review_sweep_failed", "error", err)
				} else if legacyProcessed > 0 {
					slog.Info("存量授权复核截止扫描完成",
						"event", "authz_legacy_review_sweep_done", "sources", legacyProcessed)
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

const (
	authzGovernanceSweepInterval    = time.Minute
	authzGovernanceSweepPassTimeout = 30 * time.Second
	authzGovernanceSweepBatch       = 200
)
