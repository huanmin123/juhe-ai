package main

// 统计链路排查修复的组合根回归：共享 pgpool.Registry（B④）、溢出 spool
// 回放装配与 ingestgate 多源水位接线（B①）、writer 观测面（B②/B③）。

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagewriter"
)

func TestWorkerUsageOverflowSpoolDirectory(t *testing.T) {
	// D1：溢出 spool 派生改挂 DATA_DIR（原 stats 库路径派生随 sqlite 形态退役）。
	config := workerConfig{DataDir: filepath.Join("data")}
	got := workerUsageOverflowSpoolDirectory(config)
	if want := filepath.Join("data", "usage-record-overflow-spool"); got != want {
		t.Fatalf("溢出 spool 目录 = %q, want %q", got, want)
	}
}

type wbFailingEnqueuer struct{}

func (wbFailingEnqueuer) EnqueueDurable(ctx usagewriter.Ctx, input usagewriter.UsageRecordInput) error {
	return context.Canceled
}

