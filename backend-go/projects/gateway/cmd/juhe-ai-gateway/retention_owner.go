package main

import (
	"context"
	"log/slog"
	"time"
)

// retentionOwnerRetryInterval 是全局 retention lease 被其他实例持有时，
// 本实例重试接管前的等待间隔。retention 本身按分钟级节拍运行，5s 只影响
// 故障接管延迟，不产生业务压力。
const retentionOwnerRetryInterval = 5 * time.Second

// runRetentionOwnerWithLease 是多副本 gateway 的 retention 外壳：
//   - 写入 producer 使用每实例独立 lease（main 中注入），互不阻塞；
//   - retention 使用独立的全局 lease 键，任意时刻只有一个实例持有；
//   - 未抢到全局 lease 的实例只等待重试，不阻止 gateway 启动；持锁实例
//     退出/失租后，其他实例在下一轮接管。
//
// start 返回 ok=false 表示租约被其他实例持有；error 表示数据库/传输失败，
// 由 supervisor 按既有策略重试。run 返回 nil 表示正常退出（进程关闭），
// 返回 err 表示 retention 世代结束，由 supervisor 重启组件。
func runRetentionOwnerWithLease[T any](
	ctx context.Context,
	label string,
	logger *slog.Logger,
	start func(context.Context) (T, bool, error),
	run func(context.Context, T) error,
	closeFn func(T),
) error {
	waiting := false
	for {
		keeper, ok, err := start(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if ok {
			if waiting && logger != nil {
				logger.Info("retention owner lease acquired", "retention", label)
			}
			runErr := func() error {
				defer closeFn(keeper)
				return run(ctx, keeper)
			}()
			if ctx.Err() != nil {
				return nil
			}
			return runErr
		}
		if !waiting && logger != nil {
			logger.Info("retention owner lease held by another gateway instance; this instance will retry",
				"retention", label, "retryInterval", retentionOwnerRetryInterval.String())
			waiting = true
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(retentionOwnerRetryInterval):
		}
	}
}
