package proxylatency

// W26 回归（真实 PG）：
//
// W26① 租约校验跨时钟源：verifyOwnerTx/verifyProxyTx 原以 Go 进程时钟判定
// leaseUntil.After(now)，而租约释放/续租/过期回收以 DB statement_timestamp()
// 驱动——跨机部署时「释放后立即失效」依赖时钟偏移方向。本批把判定谓词
// lease_until>statement_timestamp() 下推 SQL，与释放共用 DB 时钟。断言：
//  1. 释放后立即校验必须 ErrOwnerLeaseLost——修复后由 DB 时钟单调性保证
//     恒绿；修复前在 DB 时钟领先进程的环境（如远端 dev PG）会意外通过；
//  2. proxy lease 释放后可被同 owner 重新获取且 fence 单调 +1；
//  3. DB 视角过期（lease_until = statement_timestamp()-1µs）即失效，与
//     进程时钟无关。

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestW26LeaseVerifyUsesDatabaseClockOnPostgres(t *testing.T) {
	store := openKitPostgresStore(t)
	ctx := context.Background()

	owner, acquired, err := store.AcquireOwnerLease(ctx, "w26-owner", time.Hour)
	if err != nil || !acquired {
		t.Fatalf("获取 owner lease 失败: acquired=%v err=%v", acquired, err)
	}
	if err := store.VerifyOwnerLease(ctx, owner); err != nil {
		t.Fatalf("有效期内校验必须通过: %v", err)
	}

	// 核心回归：释放（DB statement_timestamp() 截断）后立即校验（DB 时钟
	// 判定）必须失效。
	if err := store.ReleaseOwnerLease(ctx, owner); err != nil {
		t.Fatalf("释放 owner lease 失败: %v", err)
	}
	if err := store.VerifyOwnerLease(ctx, owner); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("释放后校验必须 ErrOwnerLeaseLost（W26① DB 时钟基准），实际: %v", err)
	}

	// proxy lease：释放即过期（DB 时钟），同 owner 可重新获取且 fence 单调。
	owner2, acquired, err := store.AcquireOwnerLease(ctx, "w26-owner2", time.Hour)
	if err != nil || !acquired {
		t.Fatalf("重新获取 owner lease 失败: acquired=%v err=%v", acquired, err)
	}
	proxy1, acquired, err := store.AcquireProxyLease(ctx, owner2, "w26-proxy", time.Hour)
	if err != nil || !acquired {
		t.Fatalf("获取 proxy lease 失败: acquired=%v err=%v", acquired, err)
	}
	if proxy1.FenceToken != 1 {
		t.Fatalf("首取 fence 应为 1: %+v", proxy1)
	}
	if err := store.ReleaseProxyLease(ctx, proxy1); err != nil {
		t.Fatalf("释放 proxy lease 失败: %v", err)
	}
	proxy2, acquired, err := store.AcquireProxyLease(ctx, owner2, "w26-proxy", time.Hour)
	if err != nil || !acquired {
		t.Fatalf("释放后重新获取 proxy lease 失败: acquired=%v err=%v", acquired, err)
	}
	if proxy2.FenceToken != proxy1.FenceToken+1 {
		t.Fatalf("重取 fence 必须单调 +1: first=%d second=%d", proxy1.FenceToken, proxy2.FenceToken)
	}

	// DB 视角过期即失效：lease_until 落在 statement_timestamp() 之前 1µs，
	// 与进程时钟无关的稳定断言。
	if _, err := store.db.ExecContext(ctx, `UPDATE juhe_jobs.proxy_latency_owner_leases SET lease_until=statement_timestamp() - interval '1 microsecond' WHERE lease_key='proxy-latency-owner'`); err != nil {
		t.Fatalf("构造 DB 视角过期失败: %v", err)
	}
	if err := store.VerifyOwnerLease(ctx, owner2); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("DB 视角过期必须 ErrOwnerLeaseLost，实际: %v", err)
	}
}
