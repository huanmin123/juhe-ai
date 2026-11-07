package main

// 安全策略防护次数刷写任务的组合根装配（写后缓存设计 §4.3.1）：gateway 进
// 程内定时节拍把 Redis 计数键增量 GETDEL 合并进 security_policies.
// protection_count 列。镜像 startUpstreamClientVersionAutoRefresh 的进程内
// ticker 先例（compose 无 ctx 入参，自持 cancel 绑定组合根生命周期，取消函
// 数登记进 composed.shutdowns）。不挂 owner lease 也安全：GETDEL 原子 +
// UPDATE 求和幂等，多实例只是各自取走各自 SCAN 到的键。

import (
	"context"
	"log/slog"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/securitypolicies"
)

// 节拍与单轮预算：5 分钟合并一次，展示口径的入库延迟上界即节拍值；单轮超时
// 远小于节拍，避免堆积。
const (
	securityPolicyProtectionFlushInterval    = 5 * time.Minute
	securityPolicyProtectionFlushPassTimeout = 30 * time.Second
)

// startSecurityPolicyProtectionFlush 启动防护次数刷写任务，返回取消函数。
// 单轮失败仅 WARN 等下轮（展示用途的运行态增量，绝不影响业务）；flusher 为
// nil（组合测试 / 未装配 Redis）时返回 no-op，不启动 goroutine。
func startSecurityPolicyProtectionFlush(flusher *securitypolicies.ProtectionFlusher) context.CancelFunc {
	if flusher == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(securityPolicyProtectionFlushInterval)
		defer ticker.Stop()
		runPass := func() {
			passCtx, passCancel := context.WithTimeout(ctx, securityPolicyProtectionFlushPassTimeout)
			defer passCancel()
			flushed, err := flusher.FlushOnce(passCtx)
			if err != nil {
				slog.Warn("安全策略防护次数刷写失败，增量留待下轮",
					"event", "gateway_security_policy_protection_flush_failed",
					"error", err)
				return
			}
			if flushed > 0 {
				slog.Debug("安全策略防护次数刷写完成",
					"event", "gateway_security_policy_protection_flush_done",
					"keys", flushed)
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runPass()
			}
		}
	}()
	return cancel
}
