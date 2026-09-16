package accountquality

// w12h 补充 arms（三）：损坏行的读取归一化矩阵、runner 失败日志与取消、
// 队列出队顺序与停机、precheck/logger 小臂。

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestW12HLoadQualityRowCorruptionMatrix(t *testing.T) {
	store, _, _ := newQualityStore(t)
	ctx := context.Background()
	if _, err := store.db.Exec(`INSERT INTO account_quality_scores (
		account_id, system_account_id, provider_code, quality_score, quality_state,
		recent_request_count, recent_success_count, recent_error_count, recent_first_token_sample_count,
		recent_avg_first_token_ms, ewma_first_token_ms, success_rate,
		window_started_at, window_ended_at, updated_at
	) VALUES ('acc-1', 'sys-1', 'openai', 'not-a-number', 'weird-state',
	           -5, -1, 8, 2, NULL, 300, 1.5, 'w', 'w', '2026-09-04T08:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	row, err := store.LoadQualityRow(ctx, "acc-1")
	if err != nil || row == nil {
		t.Fatalf("损坏行必须按兜底归一化读取: %v %v", row, err)
	}
	if row.QualityScore != UnknownQualityScore {
		t.Fatalf("不可解析分数应回落默认: %d", row.QualityScore)
	}
	if row.QualityState != QualityUnknown {
		t.Fatalf("未知状态应归一化 unknown: %s", row.QualityState)
	}
	if row.RecentRequestCount != 0 || row.RecentSuccessCount != 0 {
		t.Fatalf("负计数应钳制 0: %+v", row)
	}
	if row.RecentAvgFirstTokenMs != nil {
		t.Fatalf("非数值均值应为 nil: %v", row.RecentAvgFirstTokenMs)
	}
	if row.SuccessRate == nil || *row.SuccessRate != 1 {
		t.Fatalf("超额成功率应钳制 1: %v", row.SuccessRate)
	}
	if row.EwmaFirstTokenMs == nil || *row.EwmaFirstTokenMs != 300 {
		t.Fatalf("EWMA 原样读取: %v", row.EwmaFirstTokenMs)
	}
}

func TestW12HRefreshRunnerFailureLogsAndCancel(t *testing.T) {
	store, _, _ := newQualityStore(t)
	logger := &fakeLogger{}
	precheck := NewPrecheckRunner(PrecheckDeps{Logger: logger, Reader: &mockReader{}, Prober: &mockProber{}, Mutation: &mockPrecheckMutation{}, Concurrency: 1})
	// 分钟表缺失 → 刷新失败 → runner 打失败日志并返回错误。
	if _, err := store.db.Exec(`DROP TABLE account_quality_minute_stats`); err != nil {
		t.Fatal(err)
	}
	runner := NewRefreshRunner(RefreshDeps{
		Store: store, Logger: logger, Caches: &mockCacheInvalidator{}, Precheck: precheck,
		IngestGate: allowIngestGate{}, Settings: func(string, int, int) int { return 10 },
	})
	if err := runner.Run(context.Background()); err == nil {
		t.Fatal("刷新失败必须上抛")
	}
	if logger.findByEvent("background_account_quality_refresh_failed") == nil {
		t.Fatal("应有刷新失败日志")
	}

	// 分数表缺失 → 刷新通过但候选读取失败 → 失败日志（第二分支）。
	if _, err := store.db.Exec(`CREATE TABLE account_quality_minute_stats (
		account_id text, system_account_id text, provider_code text, stat_minute text,
		request_count integer, success_count integer, error_count integer,
		first_token_ms_sum real, first_token_ms_count integer,
		last_sample_at text, last_success_at text, last_error_at text,
		last_error_message text, updated_at text
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE account_quality_scores`); err != nil {
		t.Fatal(err)
	}
	logger2 := &fakeLogger{}
	runner2 := NewRefreshRunner(RefreshDeps{
		Store: store, Logger: logger2, Caches: &mockCacheInvalidator{}, Precheck: precheck,
		IngestGate: allowIngestGate{}, Settings: func(string, int, int) int { return 10 },
	})
	if err := runner2.Run(context.Background()); err == nil {
		t.Fatal("候选读取失败必须上抛")
	}
	if logger2.findByEvent("background_account_quality_refresh_failed") == nil {
		t.Fatal("候选读取失败也应有失败日志")
	}
}

func TestW12HRefreshRunnerCancelDuringEnqueue(t *testing.T) {
	store, _, _ := newQualityStore(t)
	precheck := NewPrecheckRunner(PrecheckDeps{Reader: &mockReader{}, Prober: &mockProber{}, Mutation: &mockPrecheckMutation{}, Concurrency: 1})
	// 门控取消 ctx 但返回成功：刷新与候选读取在 sqlite 上不受取消影响，
	// 入队循环的 ctx 检查必须收口返回取消错误。
	ctx, cancel := context.WithCancel(context.Background())
	runner := NewRefreshRunner(RefreshDeps{
		Store: store, Logger: NopLogger{}, Caches: &mockCacheInvalidator{}, Precheck: precheck,
		IngestGate: cancelingGate{cancel: cancel}, Settings: func(string, int, int) int { return 10 },
	})
	err := runner.Run(ctx)
	if err == nil {
		t.Fatal("入队循环必须响应取消")
	}
}

type cancelingGate struct{ cancel context.CancelFunc }

func (g cancelingGate) EnsureUsageRecordsIngested(ctx context.Context) error {
	g.cancel()
	return nil
}

func TestW12HRefreshRunnerPendingSnapshotFields(t *testing.T) {
	store, _, lookup := newQualityStore(t)
	ctx := context.Background()
	lookup.accounts["acc-1"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
	store.seedMinute(t, "acc-1", MinuteKey(time.Now().Add(-time.Minute), time.UTC), 10, 2, 8, 0, 0, "upstream 500")
	if err := store.MarkQualityDirty(ctx, "acc-1"); err != nil {
		t.Fatal(err)
	}
	logger := &fakeLogger{}
	precheck := NewPrecheckRunner(PrecheckDeps{Logger: logger, Reader: &mockReader{}, Prober: &mockProber{}, Mutation: &mockPrecheckMutation{}, Concurrency: 1})
	// 占满并发：候选入队后保持 pending → 快照带 NextRunAt。
	precheck.queue.mu.Lock()
	precheck.queue.concurrency = 1
	precheck.queue.running = 1
	precheck.queue.items["occupied"] = &retryQueueItem[FailurePrecheckCandidate]{key: "occupied", nextRunAtMs: time.Now().UnixMilli(), running: true}
	precheck.queue.mu.Unlock()
	runner := NewRefreshRunner(RefreshDeps{
		Store: store, Logger: logger, Caches: &mockCacheInvalidator{}, Precheck: precheck,
		IngestGate: allowIngestGate{}, Settings: func(string, int, int) int { return 10 },
	})
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	event := logger.findByEvent("background_account_quality_refresh_completed")
	if event == nil || event.fields["failurePrecheckQueuePendingCount"] != 1 || event.fields["failurePrecheckQueueNextRunAt"] == nil {
		t.Fatalf("pending 快照字段不符: %v", event.fields)
	}
}

func TestW12HPrecheckRunnerArms(t *testing.T) {
	// nil logger → NopLogger。
	runner := NewPrecheckRunner(PrecheckDeps{Reader: &mockReader{}, Prober: &mockProber{}, Mutation: &mockPrecheckMutation{}, Concurrency: 1})
	if _, ok := runner.logger.(NopLogger); !ok {
		t.Fatal("nil logger 应回落 NopLogger")
	}
	// cleanupRecent 清理过期记忆。
	clock := &fakeClock{current: time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)}
	runner2 := NewPrecheckRunner(PrecheckDeps{Clock: clock, Logger: &fakeLogger{}, Reader: &mockReader{}, Prober: &mockProber{}, Mutation: &mockPrecheckMutation{}, Concurrency: 1})
	runner2.rememberPrechecked("acc-old")
	clock.Advance(time.Hour)
	runner2.cleanupRecent()
	if runner2.wasRecentlyPrechecked("acc-old") {
		t.Fatal("过期记忆应被清理")
	}
	// 有效记忆在保留期内拒绝重复入队。
	runner2.rememberPrechecked("acc-new")
	if !runner2.wasRecentlyPrechecked("acc-new") {
		t.Fatal("保留期内应命中")
	}

	// 有调度代次但状态非 active → 丢弃告警（带 dispatchRevision 字段）。
	logger := &fakeLogger{}
	reader := &mockReader{
		accounts: map[string]*AccountForTest{"acc-1": baseAccount("acc-1")},
		group:    map[string]*OpenAIAccountCandidate{"acc-1": {ID: "acc-1", Type: "api_key", Status: AccountStatusError, DispatchRevision: 7, HasDispatchRevision: true}},
	}
	runner3 := NewPrecheckRunner(PrecheckDeps{Logger: logger, Reader: reader, Prober: &mockProber{}, Mutation: &mockPrecheckMutation{}, Concurrency: 1})
	runner3.Enqueue(precheckCandidate("acc-1"))
	waitFor(t, func() bool {
		event := logger.findByEvent("background_account_quality_failure_precheck_discarded")
		return event != nil && event.fields["dispatchRevision"] != nil
	})

	// 超长原因按 rune 截断（>1000 码元触发截断分支）。
	item := precheckCandidate("acc-1")
	item.LastErrorMessage = strings.Repeat("长", 1200)
	reason := precheckReason(item, ProbeResult{})
	if len([]rune(reason)) != 1000 {
		t.Fatalf("原因应截断为 1000 码元: %d", len([]rune(reason)))
	}
}

func TestW12HQueuePumpStoppedAndOrdering(t *testing.T) {
	queue := NewRetryQueue[string]("w12h-order", 1, nil, nil,
		func(ctx context.Context, run QueueRunContext, item string) (bool, error) { return true, nil }, nil)
	// 停止后 pump 直接返回。
	queue.mu.Lock()
	queue.stopped = true
	queue.items["late"] = &retryQueueItem[string]{key: "late", nextRunAtMs: time.Now().Add(-time.Hour).UnixMilli()}
	queue.mu.Unlock()
	queue.pumpWithCtx(context.Background())
	if queue.Snapshot().PendingCount != 1 {
		t.Fatal("停止后 pump 不得出队")
	}

	// 到期时间升序出队（直接注入到期项，避免 Enqueue 立即调度干扰）。
	queue2 := NewRetryQueue[string]("w12h-order2", 1, nil, nil,
		func(ctx context.Context, run QueueRunContext, item string) (bool, error) { return true, nil }, nil)
	now := time.Now().UnixMilli()
	queue2.mu.Lock()
	queue2.items["later"] = &retryQueueItem[string]{key: "later", item: "2", nextRunAtMs: now - 5}
	queue2.items["earlier"] = &retryQueueItem[string]{key: "earlier", item: "1", nextRunAtMs: now - 10}
	queue2.mu.Unlock()
	queue2.pump()
	// 并发 1：先跑 earlier；被动调度语义下 later 等待下一次 pump（Scan/Enqueue
	// 等外部节拍），先确认 earlier 已消费、later 仍保留。
	waitFor(t, func() bool { return !queue2.HasKey("earlier") })
	if !queue2.HasKey("later") {
		t.Fatal("later 应仍在队等待下一节拍")
	}
	queue2.pump()
	waitFor(t, func() bool { return !queue2.HasKey("later") })
}

func TestW12HResolveDecisionValidPolicyQuotaArms(t *testing.T) {
	// 候选带合法策略 → generic 冷却按策略边界计算。
	valid := &OpenAIAccountCandidate{QuotaRecoveryPolicy: map[string]any{
		"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": float64(45)},
	}}
	status := 402
	decision := ResolveQuotaRetestDecision(
		ProbeResult{StatusCode: &status, Message: "insufficient quota"},
		ProbeEvidence{}, valid, "", "", "", time.Now())
	if decision.CooldownUntil == "" {
		t.Fatal("合法策略决策应带冷却时间")
	}
	parsed, err := time.Parse(time.RFC3339, decision.CooldownUntil)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := parsed.Sub(time.Now())
	if elapsed < 20*time.Minute || elapsed > 46*time.Minute {
		t.Fatalf("策略化冷却应贴近 45 分钟边界: %s", elapsed)
	}
}

func TestW12HQuotaBoundaryHelpers(t *testing.T) {
	// 极近边界：intervalMs<1 钳制为 1（schedule 边界等于 now）。
	durationOne := &QuotaRecoveryPolicy{APIKey: &QuotaRecoverySchedule{ResetStrategy: StrategyDuration, DurationMinutes: intPtr(0)}}
	schedule := QuotaRecoveryScheduleForAccount(durationOne, "api_key")
	if schedule.DurationMinutes == nil || *schedule.DurationMinutes != 0 {
		t.Fatalf("零 duration 应合并: %+v", schedule)
	}
	// duration 无 minutes 配置 → 默认 60（scheduleBoundary 默认分支）。
	plain := &QuotaRecoveryPolicy{APIKey: &QuotaRecoverySchedule{ResetStrategy: StrategyDuration}}
	boundary := scheduleBoundary(*plain.APIKey, time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC))
	if !boundary.Equal(time.Date(2026, 9, 17, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("duration 默认 60 分钟边界不符: %v", boundary)
	}
	// daily 无 hour 配置 → 0 点；timezone 空 → UTC。
	emptyTz := QuotaRecoverySchedule{ResetStrategy: StrategyDaily}
	boundary = scheduleBoundary(emptyTz, time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC))
	if !boundary.Equal(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("daily 默认 0 点边界不符: %v", boundary)
	}
	// weekly 无 day/hour 配置 → 周日 0 点。
	weekly := QuotaRecoverySchedule{ResetStrategy: StrategyWeekly}
	boundary = scheduleBoundary(weekly, time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)) // 周四
	if !boundary.Equal(time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("weekly 默认周日 0 点边界不符: %v", boundary)
	}
	// google_oauth 走专用槽位。
	policy := &QuotaRecoveryPolicy{GoogleOAuth: &QuotaRecoverySchedule{ResetStrategy: StrategyDaily, DailyResetHour: intPtr(5)}}
	merged := QuotaRecoveryScheduleForAccount(policy, "google_oauth")
	if merged.DailyResetHour == nil || *merged.DailyResetHour != 5 {
		t.Fatalf("google_oauth 槽位不符: %+v", merged)
	}
}

func TestW12HStaleFilterMatrixAndOrphanMinuteCleanup(t *testing.T) {
	store, clock, lookup := newQualityStore(t)
	ctx := context.Background()
	// acc-1：本轮唯一被认领的脏账户（DirtyLimit=1）。
	lookup.accounts["acc-1"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
	store.seedMinute(t, "acc-1", MinuteKey(clock.Now().Add(-time.Minute), time.UTC), 8, 6, 2, 500, 5, "")
	if err := store.MarkQualityDirty(ctx, "acc-1"); err != nil {
		t.Fatal(err)
	}
	// acc-2：脏但本轮不认领 → 不参与 stale 降级。
	lookup.accounts["acc-2"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
	if err := store.MarkQualityDirty(ctx, "acc-2"); err != nil {
		t.Fatal(err)
	}
	lookup.accounts["acc-b"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
	lookup.accounts["acc-c"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
	seedScore := func(id, state string, updatedAt string) {
		if _, err := store.db.Exec(`INSERT INTO account_quality_scores (
			account_id, system_account_id, provider_code, quality_score, quality_state,
			window_started_at, window_ended_at, updated_at
		) VALUES (?, 'sys-1', 'openai', 100, ?, 'w', 'w', ?)`, id, state, updatedAt); err != nil {
			t.Fatal(err)
		}
	}
	seedScore("acc-b", "fresh", "2026-09-04T07:00:00.000Z") // 非 dirty → stale 降级
	seedScore("acc-c", "failed", "2026-09-04T06:00:00.000Z") // 非 dirty → failed→unknown 降级
	seedScore("acc-d", "stale", "2026-09-04T05:00:00.000Z")  // 非 fresh/failed → 不参与
	// acc-orphan：只有分钟行、无质量行、无元数据 → 孤儿分钟行被清理。
	store.seedMinute(t, "acc-orphan", MinuteKey(clock.Now().Add(-48*time.Hour), time.UTC), 4, 0, 4, 0, 0, "gone")

	result, err := store.RefreshFromUsage(ctx, RefreshInput{WindowMinutes: 10, Timezone: "UTC", DirtyLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Refreshed != 1 {
		t.Fatalf("仅认领 1 个脏账户: %d", result.Refreshed)
	}
	// acc-b 非 dirty → fresh 降级 stale。
	if row, _ := store.LoadQualityRow(ctx, "acc-b"); row == nil || row.QualityState != QualityStale {
		t.Fatalf("acc-b 应降级 stale: %+v", row)
	}
	// acc-c 非 dirty → failed 转 unknown。
	if row, _ := store.LoadQualityRow(ctx, "acc-c"); row == nil || row.QualityState != QualityUnknown {
		t.Fatalf("acc-c 应转 unknown: %+v", row)
	}
	// acc-2 脏但未认领 → 保持 fresh 原样（未参与降级）。
	if row, _ := store.LoadQualityRow(ctx, "acc-2"); row != nil {
		t.Fatalf("acc-2 未认领不应有质量行: %+v", row)
	}
	// acc-orphan 的孤儿分钟行被清理。
	var remaining int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM account_quality_minute_stats WHERE account_id = 'acc-orphan'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("孤儿分钟行应被清理: %d", remaining)
	}
}

func TestW12HCooldownScanCountsSkippedDuplicates(t *testing.T) {
	logger := &fakeLogger{}
	runner := NewCooldownRetestRunner(CooldownDeps{
		Logger: logger, Reader: cooldownReader(), Prober: &mockProber{observation: successObservation()},
		Mutation: &w12hFailingCooldownMutation{},
		Candidates: &mockCooldownCandidates{candidates: []CooldownProbeCandidate{
			cooldownCandidate("acc-1", "fp-1", "sk-key"),
			cooldownCandidate("acc-1", "fp-1", "sk-key"),
		}},
		Settings: func(string, int, int) int { return 24 }, Concurrency: func() int { return 4 }, QueueWorkers: 1,
	})
	if err := runner.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return logger.findByEvent("background_account_api_key_cooldown_retest_completed") != nil })
	event := logger.findByEvent("background_account_api_key_cooldown_retest_completed")
	if event.fields["enqueuedCount"] != 1 || event.fields["skippedQueuedCount"] != 1 {
		t.Fatalf("去重计数不符: %v", event.fields)
	}
	// 结束排空，避免泄漏 goroutine。
	runner.StopAndDrain(2 * time.Second)
}

func TestW12HTriggerAbortMatrix(t *testing.T) {
	seedDirtyWithMinute := func(t *testing.T, store *StatsStore, clock *fakeClock, lookup *mapBusinessLookup, accountID string) {
		t.Helper()
		lookup.accounts[accountID] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
		store.seedMinute(t, accountID, MinuteKey(clock.Now().Add(-time.Minute), time.UTC), 8, 6, 2, 500, 5, "")
		if err := store.MarkQualityDirty(context.Background(), accountID); err != nil {
			t.Fatal(err)
		}
	}
	refresh := func(store *StatsStore) error {
		_, err := store.RefreshFromUsage(context.Background(), RefreshInput{WindowMinutes: 10, Timezone: "UTC", DirtyLimit: DirtyAccountBatchLimit})
		return err
	}

	t.Run("scores 更新触发器中止", func(t *testing.T) {
		store, clock, lookup := newQualityStore(t)
		seedDirtyWithMinute(t, store, clock, lookup, "acc-1")
		// 预置既有质量行：刷新的 upsert 走 UPDATE 路径才会触发中止器。
		if _, err := store.db.Exec(`INSERT INTO account_quality_scores (
			account_id, system_account_id, provider_code, quality_score, quality_state,
			window_started_at, window_ended_at, updated_at
		) VALUES ('acc-1', 'sys-1', 'openai', 100, 'fresh', 'w', 'w', '2026-09-04T07:00:00.000Z')`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`CREATE TRIGGER w12h_abort_score_update BEFORE UPDATE ON account_quality_scores BEGIN SELECT RAISE(ABORT, 'w12h score update aborted'); END`); err != nil {
			t.Fatal(err)
		}
		if err := refresh(store); err == nil {
			t.Fatal("更新中止必须使刷新失败")
		}
	})

	t.Run("scores 删除触发器中止", func(t *testing.T) {
		store, _, _ := newQualityStore(t)
		// 孤儿质量行 → 清理路径的 DELETE 被中止。
		if _, err := store.db.Exec(`INSERT INTO account_quality_scores (
			account_id, system_account_id, provider_code, quality_score, quality_state,
			window_started_at, window_ended_at, updated_at
		) VALUES ('acc-gone', 'sys-1', 'openai', 100, 'fresh', 'w', 'w', '2026-09-04T07:00:00.000Z')`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`CREATE TRIGGER w12h_abort_score_delete BEFORE DELETE ON account_quality_scores BEGIN SELECT RAISE(ABORT, 'w12h score delete aborted'); END`); err != nil {
			t.Fatal(err)
		}
		if err := refresh(store); err == nil {
			t.Fatal("删除中止必须使刷新失败")
		}
	})

	t.Run("分钟表删除触发器中止", func(t *testing.T) {
		store, clock, lookup := newQualityStore(t)
		seedDirtyWithMinute(t, store, clock, lookup, "acc-1")
		store.seedMinute(t, "acc-orphan", MinuteKey(clock.Now().Add(-48*time.Hour), time.UTC), 4, 0, 4, 0, 0, "gone")
		if _, err := store.db.Exec(`CREATE TRIGGER w12h_abort_minute_delete BEFORE DELETE ON account_quality_minute_stats BEGIN SELECT RAISE(ABORT, 'w12h minute delete aborted'); END`); err != nil {
			t.Fatal(err)
		}
		if err := refresh(store); err == nil {
			t.Fatal("分钟清理中止必须使刷新失败")
		}
	})

	t.Run("脏表删除触发器中止", func(t *testing.T) {
		store, clock, lookup := newQualityStore(t)
		seedDirtyWithMinute(t, store, clock, lookup, "acc-1")
		if _, err := store.db.Exec(`CREATE TRIGGER w12h_abort_dirty_delete BEFORE DELETE ON account_quality_dirty_accounts BEGIN SELECT RAISE(ABORT, 'w12h dirty delete aborted'); END`); err != nil {
			t.Fatal(err)
		}
		if err := refresh(store); err == nil {
			t.Fatal("脏行清理中止必须使刷新失败")
		}
	})
}

func TestW12HQuotaRemainingDirectArms(t *testing.T) {
	// errorType 稳定码。
	if !SystemInsufficientQuotaRuleMatches(403, "", "insufficient_balance", "") {
		t.Fatal("errorType 稳定码必须命中")
	}
	// findFirstField 非对象输入。
	if got := findFirstField("str", []string{"reset_at"}); got != nil {
		t.Fatalf("字符串输入必须为 nil: %v", got)
	}
	// parsePositiveSeconds 数值臂。
	if got := parsePositiveSeconds(2.5); got == nil || *got != 3 {
		t.Fatalf("浮点秒向上取整不符: %v", got)
	}
	if parsePositiveSeconds("0") != nil {
		t.Fatal("零秒必须为 nil")
	}
	// 槽位配置带时区覆盖（ForAccount 的 timezone 合并分支）。
	policy := &QuotaRecoveryPolicy{APIKey: &QuotaRecoverySchedule{ResetStrategy: StrategyDuration, DurationMinutes: intPtr(45), Timezone: "Europe/Berlin"}}
	merged := QuotaRecoveryScheduleForAccount(policy, "api_key")
	if merged.Timezone != "Europe/Berlin" {
		t.Fatalf("时区合并不符: %+v", merged)
	}
	// 零时长策略：边界=now → intervalMs<1 钳制。
	zeroDuration := &QuotaRecoveryPolicy{APIKey: &QuotaRecoverySchedule{ResetStrategy: StrategyDuration, DurationMinutes: intPtr(0)}}
	until := QuotaRecoveryCooldownUntil(zeroDuration, "api_key", "seed-9", time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC))
	if _, err := time.Parse(time.RFC3339, until); err != nil {
		t.Fatalf("零时长冷却时间不可解析: %v", err)
	}
	// utf16 代理对（显式 rune 避免编码歧义）。
	if got := utf16CodeUnits(string(rune(0x1F600))); len(got) != 2 || got[0] != 0xD83D {
		t.Fatalf("代理对拆分不符: %v", got)
	}
	// JitterWindowMs 微小间隔触发上限钳制。
	if got := JitterWindowMs(1); got != 0 {
		t.Fatalf("JitterWindowMs(1) 应被 interval/2 钳制为 0: %d", got)
	}
}

func TestW12HCooldownMessageDepthAndDefaultBatch(t *testing.T) {
	// error.error 逐层嵌套 message，超过 8 层后放弃。
	deep := `{"error":{"message":"deep-message"}}`
	for i := 0; i < 10; i++ {
		deep = `{"error":` + deep + `}`
	}
	// 逐层 error 嵌套使深度超过 8：findErrorMessage 直接放弃。
	if got := findErrorMessage(parseJSONValue(deep), 0); got != "" {
		t.Fatalf("超深 error.message 必须放弃: %q", got)
	}
	// parseUpstreamMessage 回落原文前 240 字符。
	fallback := parseUpstreamMessage(deep)
	if !strings.HasPrefix(fallback, `{"error":`) {
		t.Fatalf("回落原文不符: %q", fallback)
	}
	if got := findErrorCode(map[string]any{"code": 42}); got != "" {
		t.Fatalf("非字符串 code 必须为空: %q", got)
	}
	// BatchSize 缺省 → 默认 10。
	runner := NewCooldownRetestRunner(CooldownDeps{
		Logger: &fakeLogger{}, Reader: cooldownReader(), Prober: &mockProber{}, Mutation: &w12hFailingCooldownMutation{},
		Settings: func(string, int, int) int { return 24 }, Concurrency: func() int { return 1 }, QueueWorkers: 1,
	})
	if runner.batchSize != DefaultCooldownRetestBatchSize {
		t.Fatalf("默认批大小不符: %d", runner.batchSize)
	}
	runner.StopAndDrain(time.Second)
}

func TestW12HSmallHelperArms(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	// firstHeader 多候选与缺失。
	headers := map[string]string{"x-ratelimit-reset": "1789700400000"}
	if got := firstHeader(headers, "x-quota-reset-at", "x-ratelimit-reset"); got != "1789700400000" {
		t.Fatalf("firstHeader 候选序不符: %q", got)
	}
	if got := firstHeader(map[string]string{}, "x-quota-reset-at"); got != "" {
		t.Fatalf("缺失头应为空: %q", got)
	}
	// parseProviderResetHeader 三个分支。
	if parseProviderResetHeader("", now) != nil {
		t.Fatal("空 reset 头必须为 nil")
	}
	if got := parseProviderResetHeader("1789700400000", now); got == nil {
		t.Fatal("毫秒 reset 头必须解析")
	}
	if parseProviderResetHeader("1000", now) != nil {
		t.Fatal("过去 reset 头必须为 nil")
	}
	// bounded 三分支。
	if bounded(5, 10, 20) != 10 || bounded(30, 10, 20) != 20 || bounded(15, 10, 20) != 15 {
		t.Fatal("bounded 钳制不符")
	}
	// clamp/normalize 小助手。
	if clampNonNegative(-1) != 0 || clampNonNegative(3) != 3 {
		t.Fatal("clampNonNegative 不符")
	}
	if clampRate(-0.5) != 0 || clampRate(1.5) != 1 || clampRate(0.5) != 0.5 {
		t.Fatal("clampRate 不符")
	}
	if got := integerOrNull(nil); got != nil {
		t.Fatalf("integerOrNull(nil) 必须为 nil: %v", got)
	}
	value := 12.0
	if got := integerOrNull(&value); got == nil || *got != 12 {
		t.Fatalf("integerOrNull 不符: %v", got)
	}
	if nullableRate(nil) != nil {
		t.Fatal("nullableRate(nil) 必须为 nil")
	}
	if got := nullableInteger(nil); got != nil {
		t.Fatalf("nullableInteger(nil) 必须为 nil: %v", got)
	}
	// integerOrDefault 家族。
	if integerOrDefault(sql.NullString{Valid: false}, 7) != 7 {
		t.Fatal("NULL 分数回落默认")
	}
	if integerOrDefault(sql.NullString{String: "abc", Valid: true}, 7) != 7 {
		t.Fatal("不可解析分数回落默认")
	}
	if integerOrDefault(sql.NullString{String: "  ", Valid: true}, 7) != 7 {
		t.Fatal("空白分数回落默认")
	}
	if integerOrDefault(sql.NullString{String: "2.7", Valid: true}, 7) != 3 {
		t.Fatal("小数分数四舍五入")
	}
	if integerOrDefault(sql.NullString{String: "-3", Valid: true}, 7) != 0 {
		t.Fatal("负分数钳制 0")
	}
	if integerOrDefault(sql.NullString{String: "NaN", Valid: true}, 7) != 7 {
		t.Fatal("NaN 回落默认")
	}
	if integerOrDefaultInt(sql.NullInt64{Int64: -9, Valid: true}, 7) != 0 {
		t.Fatal("负整数钳制 0")
	}
	if integerOrDefaultText("", 4) != 4 {
		t.Fatal("空白回落默认")
	}
	// normalizeQualityState 未知值归一化。
	if normalizeQualityState("weird") != QualityUnknown {
		t.Fatal("未知状态必须归一化 unknown")
	}
	// clampLimit / uniqueIds / toSet。
	if clampLimit(-3) != 1 || clampLimit(5000) != 5000 {
		t.Fatal("clampLimit 不符")
	}
	if got := uniqueIds([]string{"a", "b", "a", ""}); len(got) != 2 {
		t.Fatalf("uniqueIds 去重不符: %v", got)
	}
	if toSet([]string{"x"})["x"] != struct{}{} {
		t.Fatal("toSet 不符")
	}
	// NopLogger 直接调用（零副作用契约）。
	NopLogger{}.Debug("e", nil, "m")
	NopLogger{}.Info("e", nil, "m")
	NopLogger{}.Warn("e", nil, "m")
	NopLogger{}.Error("e", nil, "m")
	// findErrorMessage 非对象输入。
	if findErrorMessage("str", 0) != "" {
		t.Fatal("非对象必须为空")
	}
	// DeterministicOffsetMs 零窗口。
	if DeterministicOffsetMs(0, "s") != 0 {
		t.Fatal("零窗口必须为 0")
	}
	// JitterWindowMs 微小间隔钳制。
	if JitterWindowMs(1) != 0 {
		t.Fatal("JitterWindowMs(1) 必须钳制 0")
	}
	// quotaRecoveryDelaySeconds 零时长策略触发下限钳制。
	zero := &OpenAIAccountCandidate{ID: "acc-1", QuotaRecoveryPolicy: map[string]any{
		"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": float64(0)},
	}}
	// 零时长被策略归一化拒绝 → 回落默认 60 分钟边界（56 分钟偏移窗口）。
	if got := quotaRecoveryDelaySeconds(zero, "fp", "", now); got <= CooldownQuotaMinDeferSeconds {
		t.Fatalf("回落默认边界顺延应远大于下限: %d", got)
	}
	// errorType 稳定码 quota 子串双臂。
	if !SystemInsufficientQuotaRuleMatches(403, "plain", "xquota", "") {
		t.Fatal("errorType quota 子串必须命中")
	}
	// 时区覆盖合并分支。
	policy := &QuotaRecoveryPolicy{APIKey: &QuotaRecoverySchedule{ResetStrategy: StrategyDuration, DurationMinutes: intPtr(45), Timezone: "Europe/Berlin"}}
	if merged := QuotaRecoveryScheduleForAccount(policy, "api_key"); merged.Timezone != "Europe/Berlin" {
		t.Fatalf("时区合并不符: %+v", merged)
	}
	// 零时长冷却（intervalMs<1 钳制分支）。
	zeroPolicy := &QuotaRecoveryPolicy{APIKey: &QuotaRecoverySchedule{ResetStrategy: StrategyDuration, DurationMinutes: intPtr(0)}}
	if until := QuotaRecoveryCooldownUntil(zeroPolicy, "api_key", "seed", now); until == "" {
		t.Fatal("零时长冷却应产出时间")
	}
}

func TestW12HPGModeStoreHelpersAndSchemaError(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "pgmode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := OpenStatsStore(StatsStoreConfig{Mode: StatsPostgres, PostgresDB: db})
	if err != nil {
		t.Fatal(err)
	}
	// PG 表名限定分支。
	if store.scoresTable() != "juhe_stats.account_quality_scores" || store.minuteTable() != "juhe_stats.account_quality_minute_stats" || store.dirtyTable() != "juhe_stats.account_quality_dirty_accounts" {
		t.Fatal("PG 表名限定不符")
	}
	// PG 冻结 DDL 在 sqlite 句柄上执行 → schema 初始化错误分支。
	if err := store.EnsureSchema(context.Background()); err == nil {
		t.Fatal("PG 脚本在 sqlite 句柄上必须失败")
	}
}
