package taskruns

import (
	"context"
	"errors"
	"testing"
	"time"
)

// w12b_runner_test.go 覆盖 RunWithScheduledLease / RunWithTaskRun 的分支：
// 获取失败（错误与 busy）、默认续租/心跳间隔、续租成功与失败、fence 丢失、
// 释放未命中（partial）、启动 CAS 未命中（skipped）、心跳失联收口 failed、
// 非法终态归一 completed、以及 finish/get 读取错误路径。
// 依赖 w12b_failinject_test.go 的注入驱动设施。

func TestW12bScheduledLeaseRenewalExtendsLease(t *testing.T) {
	store := openKitPostgresStore(t)
	defer store.Close()
	ctx := context.Background()

	leaseUntil := func() (string, error) {
		var until string
		err := store.db.QueryRowContext(ctx,
			`SELECT lease_until FROM juhe_stats.background_job_leases WHERE lease_key = $1`,
			"w12b-renew-key").Scan(&until)
		return until, err
	}

	outcome, runErr := RunWithScheduledLease(ctx, store, ScheduledLeaseRunnerOptions{
		JobName: "w12b-renew-job", LeaseKey: "w12b-renew-key", OwnerID: "w12b-renew-owner",
		TTL: 1500 * time.Millisecond, // RenewInterval 缺省：TTL/3=500ms < 1s → 钳为 1s
	}, func(context.Context, LeaseIdentity) error {
		first, err := leaseUntil()
		if err != nil {
			return err
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			current, err := leaseUntil()
			if err == nil && current != first {
				return nil // 续租已推迟 lease_until
			}
			time.Sleep(10 * time.Millisecond)
		}
		return errors.New("未观察到续租推迟 lease_until")
	})
	if runErr != nil {
		t.Fatalf("RunWithScheduledLease: %v", runErr)
	}
	if outcome.Outcome != OutcomeSuccess || outcome.LeaseState != LeaseStateAcquired || outcome.Lease == nil {
		t.Fatalf("应 success/acquired: %+v", outcome)
	}
}

