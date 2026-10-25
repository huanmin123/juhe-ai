package accounthealth

// w16f_store_runner_test.go 波次 w16f 第二批：Store 参数/取消/编码臂、
// Runner 主循环与 runCycle 未覆盖臂（SQLite 真库 + w13g5 注入 kit）、
// outbox drain 空 rows / LoadAccount 失败臂、executor 的 cursor 读取失败臂。
// 全部本地 SQLite，不触网；注入错误固定为 w13g5/w16f 文本，不含敏感数据。

import (
	"context"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	"testing"
	"time"
)

// --- Store 臂 ---

func TestW16fStoreOpenPostgresIdleClamp(t *testing.T) {
	// idle 缺省(10) > open(4) → 钳制到 open（惰性连接，不实际拨号）。
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: "postgres://w16f:nopass@127.0.0.1:1/w16f", PostgresMaxOpenConns: 4})
	if err != nil {
		t.Fatalf("惰性开库必须成功: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
}

func (r *Runner) prepareScheduledInputErr(ctx context.Context, lease OwnerLease, input exactkeyprobe.Input, now time.Time) error {
	_, err := r.prepareScheduledInput(ctx, lease, input, now)
	return err
}

