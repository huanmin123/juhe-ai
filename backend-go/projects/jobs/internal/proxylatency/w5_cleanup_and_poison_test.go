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

// TestW5DrainSkipsPoisonRowAndProjectsHealthyRow：坏行在前、好行在后，一次
// Drain 必须同时完成"投影好行 + 越过坏行"，游标落在最后一行。
func TestW5DrainSkipsPoisonRowAndProjectsHealthyRow(t *testing.T) {
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	projector := wfNewProjector(t, store, business)
	ctx := context.Background()

	wfSeedProxyRow(t, business, "p-ok", wfProjRevision, nil)
	committer := newWFCommitter(t, store)
	poison := committer.commitFunc("p-poison", wfProjRevision, func(value *Outcome) {
		value.Items = nil
		value.OverallStatus = OverallUnknown
	})
	healthy := committer.commit("p-ok", wfProjRevision)

	count, err := projector.Drain(ctx)
	if err != nil || count != 2 {
		t.Fatalf("毒丸行不得阻塞批次 count=%d err=%v", count, err)
	}
	// 好行正常投影 applied。
	var applied string
	if err := business.QueryRow(`SELECT disposition FROM proxy_latency_projection_receipts WHERE outcome_id=?`, healthy.OutcomeID).Scan(&applied); err != nil || applied != string(ProjectionApplied) {
		t.Fatalf("好行 receipt=%q err=%v", applied, err)
	}
	// 坏行 rejected 留痕。
	var rejected, reason string
	if err := business.QueryRow(`SELECT disposition, reason FROM proxy_latency_projection_receipts WHERE outcome_id=?`, poison.OutcomeID).Scan(&rejected, &reason); err != nil || rejected != string(ProjectionRejected) || reason != "outcome_items_missing" {
		t.Fatalf("坏行 receipt=%s/%s err=%v", rejected, reason, err)
	}
	// 游标必须推进到最后一行（好行），而不是停在毒丸行。
	var cursorOutcome string
	if err := business.QueryRow(`SELECT outcome_id FROM proxy_latency_projection_cursors WHERE consumer_key='wf-consumer'`).Scan(&cursorOutcome); err != nil || cursorOutcome != healthy.OutcomeID {
		t.Fatalf("cursor=%q want %q err=%v", cursorOutcome, healthy.OutcomeID, err)
	}
	if got := projector.RejectedSkippedCount(); got != 1 {
		t.Fatalf("RejectedSkippedCount=%d want 1", got)
	}
	// 重放不再读到任何行。
	if count, err := projector.Drain(ctx); err != nil || count != 0 {
		t.Fatalf("重放 count=%d err=%v", count, err)
	}
}

// TestW5DrainUncertainErrorDoesNotAdvanceCursor：业务库围栏表缺失（模拟
// 网络/DB 故障）时 Drain 返回错误、不落 receipt、不推进游标，下一轮重试。
func TestW5DrainUncertainErrorDoesNotAdvanceCursor(t *testing.T) {
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	projector := wfNewProjector(t, store, business)
	ctx := context.Background()

	committer := newWFCommitter(t, store)
	committer.commit("p-1", wfProjRevision)
	if _, err := business.Exec(`DROP TABLE proxy_profiles`); err != nil {
		t.Fatalf("准备故障库失败: %v", err)
	}
	if _, err := projector.Drain(ctx); err == nil {
		t.Fatal("DB 故障必须返回错误保持重试")
	}
	var cursorCount int
	if err := business.QueryRow(`SELECT COUNT(*) FROM proxy_latency_projection_cursors`).Scan(&cursorCount); err != nil || cursorCount != 0 {
		t.Fatalf("不确定错误不得推进 cursor count=%d err=%v", cursorCount, err)
	}
	if got := projector.RejectedSkippedCount(); got != 0 {
		t.Fatalf("不确定错误不得计入 rejected skipped: %d", got)
	}
}

// TestW5PruneExpiredRecords：清理按时间窗删除 outcomes/inputs，保留新行与
// input_versions 计数器行。
func TestW5PruneExpiredRecords(t *testing.T) {
	store := wfOpenJobsStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	committer := newWFCommitter(t, store)
	old := committer.commitFunc("p-old", wfProjBase.Add(-time.Hour).UTC().Format(time.RFC3339Nano), nil)
	fresh := committer.commitFunc("p-fresh", wfProjBase.Add(-time.Hour).UTC().Format(time.RFC3339Nano), nil)

	// 把旧行的 stored_at/issued_at 直接改写为超过保留窗口；payload digest
	// 校验不参与清理路径。
	staleCutoff := now.Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`UPDATE proxy_latency_outcomes SET stored_at=? WHERE outcome_id=?`, staleCutoff, old.OutcomeID); err != nil {
		t.Fatalf("改写旧 outcome 时间失败: %v", err)
	}
	for _, requestID := range []string{old.RequestID, fresh.RequestID} {
		if _, err := store.db.Exec(`UPDATE proxy_latency_inputs SET issued_at=? WHERE request_id=?`, staleCutoff, requestID); err != nil {
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
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM proxy_latency_outcomes`).Scan(&outcomes); err != nil || outcomes != 1 {
		t.Fatalf("剩余 outcomes=%d err=%v", outcomes, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM proxy_latency_inputs`).Scan(&inputs); err != nil || inputs != 0 {
		t.Fatalf("剩余 inputs=%d err=%v", inputs, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM proxy_latency_input_versions`).Scan(&versions); err != nil || versions == 0 {
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
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
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
	store, err := OpenStore(cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
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

// TestW5LoadRuntimeConfigRetentionDefaults：清理配置的默认值、env 覆盖与
// 非法值拒绝。
func TestW5LoadRuntimeConfigRetentionDefaults(t *testing.T) {
	base := map[string]string{
		"JUHE_AI_POSTGRES_URL": "postgres://user:pass@127.0.0.1:5432/juhe_jobs_sslmode_disabled",
		"JUHE_AI_SECRET":       "w5-secret",
	}
	getenv := func(name string) string { return base[name] }
	cfg, err := LoadRuntimeConfig(getenv)
	if err != nil {
		t.Fatalf("最小配置 err=%v", err)
	}
	if cfg.Retention != DefaultProxyLatencyRetention {
		t.Fatalf("默认 retention=%v want %v", cfg.Retention, DefaultProxyLatencyRetention)
	}
	if cfg.CleanupInterval != DefaultProxyLatencyCleanupInterval {
		t.Fatalf("默认 cleanup interval=%v want %v", cfg.CleanupInterval, DefaultProxyLatencyCleanupInterval)
	}

	withOverrides := map[string]string{
		"JUHE_AI_POSTGRES_URL":                   base["JUHE_AI_POSTGRES_URL"],
		"JUHE_AI_SECRET":                         base["JUHE_AI_SECRET"],
		"JUHE_AI_PROXY_LATENCY_RETENTION":        "72h",
		"JUHE_AI_PROXY_LATENCY_CLEANUP_INTERVAL": "30m",
	}
	cfg, err = LoadRuntimeConfig(func(name string) string { return withOverrides[name] })
	if err != nil {
		t.Fatalf("覆盖配置 err=%v", err)
	}
	if cfg.Retention != 72*time.Hour || cfg.CleanupInterval != 30*time.Minute {
		t.Fatalf("覆盖 retention/interval=%v/%v", cfg.Retention, cfg.CleanupInterval)
	}

	withOverrides["JUHE_AI_PROXY_LATENCY_RETENTION"] = "1h"
	if _, err := LoadRuntimeConfig(func(name string) string { return withOverrides[name] }); err == nil {
		t.Fatal("低于 24h 的 retention 必须拒绝")
	}
}
