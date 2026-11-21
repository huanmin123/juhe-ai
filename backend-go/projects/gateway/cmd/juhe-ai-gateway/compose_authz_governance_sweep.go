package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
	"github.com/huanminabc/juhe-ai/backend-go-platform/supervisor"
)

// authz-governance-sweep 是发送-领取模型的周期收敛面：授权有效期
// （expires_at）到期翻转。过期状态机与 runtime 投影都在 gateway/internal/
// authz，jobs 不能跨 module import；由本进程周期调用 ExpireDueOffers，避免
// 依赖某个请求恰好触发到期。待领取不过期：只有设置了 expires_at 的授权才
// 参与本扫描。
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
					slog.Warn("授权有效期到期扫描失败，等待下一轮",
						"event", "authz_offer_expiry_sweep_failed", "error", err)
				} else if expired.GrantsExpired > 0 || expired.SourcesExpired > 0 {
					slog.Info("授权有效期到期扫描完成",
						"event", "authz_offer_expiry_sweep_done",
						"grants", expired.GrantsExpired, "sources", expired.SourcesExpired)
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
