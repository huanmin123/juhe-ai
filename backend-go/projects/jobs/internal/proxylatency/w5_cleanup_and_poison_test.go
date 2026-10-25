package proxylatency

import (
	"context"
	"errors"
	"testing"
	"time"
)

// W5 杂项修复的两组回归：
// 1) 恢复游标毒丸——确定性 rejected 行越过游标并留痕，不确定性错误保持重试；
// 2) outcomes/inputs 运行态表的独立时间窗清理与 owner 循环挂靠。
//
// 迁移裁决（PLAN-20261024T051925945Z proxylatency 切片）：TestW5PruneExpiredRecords
// 与两个 runner prune 调度测试迁真实 PG；前两个 Drain 测试保持 SQLite 原状
// （组B：被测对象 ResultProjector 的 SQLite 业务库真库臂，随生产 sqlite 臂
// 删除批次退役，证据见 wf_result_projector_test.go 文件头）。

func TestW5PruneExpiredRecords(t *testing.T) {
	store := openKitPostgresStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	committer := newWFCommitter(t, store)
	old := committer.commitFunc("p-old", wfProjBase.Add(-time.Hour).UTC().Format(time.RFC3339Nano), nil)
	fresh := committer.commitFunc("p-fresh", wfProjBase.Add(-time.Hour).UTC().Format(time.RFC3339Nano), nil)

	// 把旧行的 stored_at/issued_at 直接改写为超过保留窗口；payload digest
	// 校验不参与清理路径。PG timestamptz 列以 time.Time 参数改写（SQLite
	// 形态为 RFC3339Nano 文本）。
	staleCutoff := now.Add(-48 * time.Hour).UTC()
	if _, err := store.db.Exec(`UPDATE juhe_jobs.proxy_latency_outcomes SET stored_at=$1 WHERE outcome_id=$2`, staleCutoff, old.OutcomeID); err != nil {
		t.Fatalf("改写旧 outcome 时间失败: %v", err)
	}
	for _, requestID := range []string{old.RequestID, fresh.RequestID} {
		if _, err := store.db.Exec(`UPDATE juhe_jobs.proxy_latency_inputs SET issued_at=$1 WHERE request_id=$2`, staleCutoff, requestID); err != nil {
			t.Fatalf("改写 input 时间失败: %v", err)
		}
	}

	stats, err := store.PruneExpiredRecords(ctx, now, 24*time.Hour)
	if err != nil {
		t.Fatalf("PruneExpiredRecords err=%v", err)
	}
	if stats.Outcomes != 1 || stats.Inputs != 2 {
		t.Fatalf("清理统计=%+v want outcomes=1 inputs=2", stats)
	}
	var outcomes, inputs, versions int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM juhe_jobs.proxy_latency_outcomes`).Scan(&outcomes); err != nil || outcomes != 1 {
		t.Fatalf("剩余 outcomes=%d err=%v", outcomes, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM juhe_jobs.proxy_latency_inputs`).Scan(&inputs); err != nil || inputs != 0 {
		t.Fatalf("剩余 inputs=%d err=%v", inputs, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM juhe_jobs.proxy_latency_input_versions`).Scan(&versions); err != nil || versions == 0 {
		t.Fatalf("input_versions 计数器行必须保留 count=%d err=%v", versions, err)
	}

	if _, err := store.PruneExpiredRecords(ctx, now, 0); err == nil {
		t.Fatal("retain<=0 必须拒绝")
	}
}

// TestW5RunnerPruneDueScheduling：owner 循环内清理到期触发、失败推进周期且
// 不污染周期状态。
func TestW5RunnerPruneDueScheduling(t *testing.T) {
	cfg := testRuntimeConfig(t)
	store := openKitRuntimeStore(t, cfg)
	defer store.Close()

	ctx := context.Background()
	runner := NewRunner(cfg, store, fakeInputReader{}, nil)
	calls := 0
	runner.pruneExpiredFn = func(_ context.Context, _ time.Time, retain time.Duration) (PruneStats, error) {
		calls++
		if retain != DefaultProxyLatencyRetention {
			t.Fatalf("retain=%v want 默认 %v", retain, DefaultProxyLatencyRetention)
		}
		if calls == 1 {
			return PruneStats{Outcomes: 3, Inputs: 2}, nil
		}
		return PruneStats{}, errors.New("prune boom")
	}

	// 首次零值 nextPruneAt 必须立即到期。
	runner.pruneExpiredIfDue(ctx)
	if calls != 1 {
		t.Fatalf("首次必须到期执行 calls=%d", calls)
	}
	// 未到期不重复执行。
	runner.pruneExpiredIfDue(ctx)
	if calls != 1 {
		t.Fatalf("未到期不得重复执行 calls=%d", calls)
	}
	// 时间推进越过周期后再次执行；失败也推进周期。
	runner.nextPruneAt = runner.now().Add(-time.Millisecond)
	runner.pruneExpiredIfDue(ctx)
	if calls != 2 {
		t.Fatalf("到期必须再次执行 calls=%d", calls)
	}
	if runner.status.LastError != "" {
		t.Fatalf("清理失败不得污染周期状态 LastError=%q", runner.status.LastError)
	}
	// 失败后同样推进：立即再调不触发。
	runner.pruneExpiredIfDue(ctx)
	if calls != 2 {
		t.Fatalf("失败后必须推进周期 calls=%d", calls)
	}
}

// TestW5RunnerPruneUsesConfiguredRetention：配置的保留窗口透传清理函数。
func TestW5RunnerPruneUsesConfiguredRetention(t *testing.T) {
	cfg := testRuntimeConfig(t)
	store := openKitRuntimeStore(t, cfg)
	defer store.Close()

	cfg.Retention = 7 * 24 * time.Hour
	cfg.CleanupInterval = time.Hour
	runner := NewRunner(cfg, store, fakeInputReader{}, nil)
	calls := 0
	runner.pruneExpiredFn = func(_ context.Context, _ time.Time, retain time.Duration) (PruneStats, error) {
		calls++
		if retain != cfg.Retention {
			t.Fatalf("retain=%v want %v", retain, cfg.Retention)
		}
		return PruneStats{}, nil
	}
	runner.pruneExpiredIfDue(context.Background())
	if calls != 1 {
		t.Fatalf("配置保留窗口必须生效 calls=%d", calls)
	}
}

