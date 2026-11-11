package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// writerLeaseKeeperRetryInterval 是写租约 keeper 丢失后的 Revive 重试周期，
// 与 retention_owner.go 的接管重试周期一致：写门禁停摆只影响本实例的审计/
// 操作日志落盘，5s 级重试不产生存储压力。
const writerLeaseKeeperRetryInterval = 5 * time.Second

// writerLeaseReviver 是写租约复活 runner 依赖的最小 keeper 面：T203 并行
// 契约下 auditlog.LeaseKeeper 与 operationlog.LeaseKeeper 均导出这三个方法
// ——Lost 在租约 terminal 时关闭且复活后换新通道，LostError 为丢失原因，
// Revive 重新获取租约并递增 fence（healthy 时 no-op；keeper 已 Close 后
// 返回错误）。嵌入 comparable 仅为泛型实例的 nil 判定。
type writerLeaseReviver interface {
	comparable
	Lost() <-chan struct{}
	LostError() error
	Revive(context.Context) error
}

// runWriterLeaseReviver 是 T203 每实例写租约 keeper 的复活外壳：写 keeper
// 丢失后 producer 门禁停摆至进程重启，本 runner 在 cmd 装配层按固定周期
// 重试 Revive(ctx)，复活成功（fence 递增、Lost() 换新通道）后 producer 因
// 逐条直读 Lease() 自动恢复写放行。契约：
//   - 每轮外层迭代重读 keeper.Lost()（复活后通道换新，与 RunOwner 每
//     select 重读同一约定）；
//   - 丢失后立即首次 Revive，失败按 retry 周期持续重试并记 Warn；
//   - Revive 成功记 Info；「keeper 已 Close」类错误属进程关闭态，直接
//     返回 nil 优雅退出；ctx 取消返回 nil；keeper 为 nil（F4 关闭态）
//     立即返回 nil；
//   - retry<=0 取默认 5s。
func runWriterLeaseReviver[T writerLeaseReviver](ctx context.Context, keeper T, retry time.Duration, logger *slog.Logger) error {
	var zero T
	if keeper == zero {
		return nil
	}
	if retry <= 0 {
		retry = writerLeaseKeeperRetryInterval
	}
	keeperLabel := fmt.Sprintf("%T", any(keeper))
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-keeper.Lost():
		}
		consecutiveFailures := 0
		for {
			reviveErr := keeper.Revive(ctx)
			if reviveErr == nil {
				if logger != nil {
					logger.Info("writer lease keeper revived; producer gate resumes",
						"component", "writer-lease-reviver", "keeper", keeperLabel,
						"attempts", consecutiveFailures+1)
				}
				break
			}
			if ctx.Err() != nil {
				return nil
			}
			if writerLeaseKeeperClosed(reviveErr) {
				if logger != nil {
					logger.Info("writer lease keeper closed; reviver stops",
						"component", "writer-lease-reviver", "keeper", keeperLabel,
						"error", reviveErr)
				}
				return nil
			}
			consecutiveFailures++
			if logger != nil {
				logger.Warn("writer lease keeper revive failed; retrying",
					"component", "writer-lease-reviver", "keeper", keeperLabel,
					"error", reviveErr, "consecutiveFailures", consecutiveFailures,
					"retryInterval", retry.String())
			}
			timer := time.NewTimer(retry)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return nil
			case <-timer.C:
			}
		}
	}
}

// writerLeaseKeeperClosed 判定 Revive 失败是否属于「keeper 已 Close」类：
// 进程关闭态下 keeper 不可复活，runner 视为不可复活并优雅退出（T203 裁决），
// 不让 supervisor 在停机窗口再做一轮错误重试。当前并行契约以「已关闭 /
// closed」措辞的错误承载该语义（两包 Close 既有 Warn 日志同款措辞），这里
// 保守匹配中英文措辞；并行实现若后续导出哨兵错误，应改为 errors.Is 精确
// 判定。ctx 取消/超时类错误不属于关闭态（由 runner 的 ctx.Err 分支处理）。
func writerLeaseKeeperClosed(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "已关闭") || strings.Contains(strings.ToLower(msg), "closed")
}
