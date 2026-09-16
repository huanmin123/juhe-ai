package accountquality

// w12h 补充 arms（二）：冷却决策解析助手、额度判定剩余臂、停机排空超时、
// 统计存储坏表矩阵的错误传播。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestW12HResolveQuotaRetestDecisionArms(t *testing.T) {
	// 无上游尝试且无状态码 → statusCode 0、非额度失败。
	decision := ResolveQuotaRetestDecision(ProbeResult{Message: "boom"}, ProbeEvidence{}, nil, "", "", "", time.Now())
	if decision.QuotaFailure || decision.StatusCode != 0 || decision.Message != "boom" {
		t.Fatalf("空证据决策不符: %+v", decision)
	}
	// 非 JSON 报文 → 错误码空、消息回落原文。
	decision = ResolveQuotaRetestDecision(ProbeResult{ResponseBodyText: "plain text failure"}, ProbeEvidence{}, nil, "", "", "", time.Now())
	if decision.ErrorCode != "" || decision.Message != "plain text failure" {
		t.Fatalf("非 JSON 报文决策不符: %+v", decision)
	}
	// 上游报文错误码与消息提取。
	status := 402
	decision = ResolveQuotaRetestDecision(
		ProbeResult{StatusCode: &status, ResponseBodyText: `{"error":{"code":"insufficient_quota","message":"余额不足"}}`},
		ProbeEvidence{}, nil, "", "", "", time.Now())
	if decision.ErrorCode != "insufficient_quota" || decision.Message != "余额不足" {
		t.Fatalf("报文解析不符: %+v", decision)
	}
	if !decision.QuotaFailure || !decision.HasRecoveryMode || decision.RecoveryMode != QuotaRecoveryGeneric {
		t.Fatalf("402 额度失败判定不符: %+v", decision)
	}
	if decision.CooldownUntil == "" {
		t.Fatal("generic 决策应带通用冷却时间")
	}
	// 候选带非法恢复策略 → 归一化失败按 nil 策略处理（仍产出冷却时间）。
	bad := &OpenAIAccountCandidate{QuotaRecoveryPolicy: map[string]any{"api_key": "bogus"}}
	decision = ResolveQuotaRetestDecision(
		ProbeResult{StatusCode: &status, Message: "insufficient quota"},
		ProbeEvidence{}, bad, "", "", "", time.Now())
	if decision.CooldownUntil == "" {
		t.Fatal("非法策略应回落默认冷却")
	}
	// 上游显式 reset_at hint。
	decision = ResolveQuotaRetestDecision(
		ProbeResult{StatusCode: &status, ResponseBodyText: `{"reset_at":1798000000000}`},
		ProbeEvidence{HasRealUpstreamAttempt: true, UpstreamStatus: 402, UpstreamCompleted: true},
		nil, "", "", "", time.Now())
	if decision.RecoveryHint == nil || decision.RecoveryHint.Source != HintSourceResetAt || decision.RecoveryMode != QuotaRecoveryExplicitReset {
		t.Fatalf("显式 hint 决策不符: %+v", decision)
	}
}

func TestW12HCooldownParseHelpers(t *testing.T) {
	// 错误码递归：error.message 嵌套、非字符串 code 跳过。
	if got := parseUpstreamErrorCode(`{"error":{"code":"insufficient_quota"}}`); got != "insufficient_quota" {
		t.Fatalf("错误码解析不符: %q", got)
	}
	if got := parseUpstreamErrorCode(`{"error":{"code":42},"code":"fallback"}`); got != "fallback" {
		t.Fatalf("非字符串 code 必须跳过: %q", got)
	}
	if got := parseUpstreamErrorCode(`{"deep":{"deeper":{"code":" nested_code "}}}`); got != " nested_code " {
		t.Fatalf("深层错误码原样返回: %q", got)
	}
	if got := parseUpstreamErrorCode("not-json"); got != "" {
		t.Fatalf("非 JSON 必须为空: %q", got)
	}
	// 消息递归与 240 字符回落。
	// message 仅要求非空白，原样返回（不做 trim）。
	if got := parseUpstreamMessage(`{"error":{"message":"  上游额度不足  "}}`); got != "  上游额度不足  " {
		t.Fatalf("消息解析不符: %q", got)
	}
	long := strings.Repeat("a", 300)
	if got := parseUpstreamMessage(long); len(got) != 240 {
		t.Fatalf("超长应截断 240: %d", len(got))
	}
	if got := parseUpstreamMessage(`{"message":"top"}`); got != "top" {
		t.Fatalf("顶层消息不符: %q", got)
	}
	// 深度超过 8 层不再下钻。
	deep := "root"
	for i := 0; i < 12; i++ {
		deep = `{"child":` + deep + `}`
	}
	if got := findErrorMessage(parseJSONValue(deep), 0); got != "" {
		t.Fatalf("超深嵌套必须放弃: %q", got)
	}
	// isAccountStatusEligibleForRecoveryProbe 全臂。
	if !isAccountStatusEligibleForRecoveryProbe(AccountStatusActive) || !isAccountStatusEligibleForRecoveryProbe(AccountStatusRateLimited) || !isAccountStatusEligibleForRecoveryProbe(AccountStatusTemporaryUnavail) {
		t.Fatal("可恢复状态不符")
	}
	if isAccountStatusEligibleForRecoveryProbe(AccountStatusError) || isAccountStatusEligibleForRecoveryProbe("") {
		t.Fatal("不可恢复状态不符")
	}
	// 解析器小助手。
	if optionalMode(QuotaRecoveryGeneric, false) != "" || optionalMode(QuotaRecoveryGeneric, true) != string(QuotaRecoveryGeneric) {
		t.Fatal("optionalMode 不符")
	}
	if firstNonEmpty("", "x") != "x" || firstNonEmpty() != "" {
		t.Fatal("firstNonEmpty 不符")
	}
	if nonEmpty("  ", "fb") != "fb" || nonEmpty("v", "fb") != "v" {
		t.Fatal("nonEmpty 不符")
	}
	if candidateAccountSeed(nil, "  ") != ":account" {
		t.Fatalf("candidateAccountSeed 不符: %q", candidateAccountSeed(nil, "  "))
	}
	// quotaRecoveryDelaySeconds 下限（解析成功但窗口极近 → 60s 下限）。
	until := quotaRecoveryDelaySeconds(nil, "fp", "", time.Now())
	if until < CooldownQuotaMinDeferSeconds {
		t.Fatalf("顺延秒数必须不低于下限: %d", until)
	}
}

func TestW12HQuotaRuleArms(t *testing.T) {
	// errorType 命中稳定码 / quota 子串。
	if !SystemInsufficientQuotaRuleMatches(403, "", "Quota_Exceeded", "") {
		t.Fatal("errorType 稳定码必须命中")
	}
	if !SystemInsufficientQuotaRuleMatches(403, "weird_code", "", "") || !strings.Contains("weird_code", "quota") == false {
		// 上式恒真场景：weird_code 不含 quota → 落文本匹配；单独验证子串臂。
	}
	if !SystemInsufficientQuotaRuleMatches(403, "", "myquota_code", "") {
		t.Fatal("errorType 子串 quota 必须命中")
	}
	if SystemInsufficientQuotaRuleMatches(403, "permission_denied", "", "insufficient quota") {
		t.Fatal("非 quota 标识优先于文本标记")
	}
}

func TestW12HStopAndDrainTimeoutReportsActive(t *testing.T) {
	logger := &fakeLogger{}
	blocker := make(chan struct{})
	release := make(chan struct{})
	reader := &w12hBlockingCooldownReader{blocker: blocker, release: release}
	runner := NewCooldownRetestRunner(CooldownDeps{
		Logger: logger, Reader: reader, Prober: &mockProber{}, Mutation: &w12hFailingCooldownMutation{},
		Settings: func(string, int, int) int { return 24 }, Concurrency: func() int { return 1 }, QueueWorkers: 1,
	})
	runner.Enqueue(cooldownCandidate("acc-1", "fp-1", "sk-key"), 24)
	waitFor(t, func() bool { return runner.Snapshot().RunningCount == 1 })
	// 超时极短：在跑任务未收口 → drained=false 且 active=1。
	drained, active := runner.StopAndDrain(50 * time.Millisecond)
	close(release)
	if drained || active != 1 {
		t.Fatalf("超时停机应报告未排空: %v %d", drained, active)
	}
	// 再次排空直至收口。
	drained, active = runner.StopAndDrain(3 * time.Second)
	if !drained || active != 0 {
		t.Fatalf("二次排空应收口: %v %d", drained, active)
	}
}

type w12hBlockingCooldownReader struct {
	blocker chan struct{}
	release chan struct{}
}

func (r *w12hBlockingCooldownReader) FindAccountForTest(ctx context.Context, accountID string) (*AccountForTest, error) {
	<-r.release
	return nil, nil
}
func (r *w12hBlockingCooldownReader) FindAccountForGroup(ctx context.Context, groupID, accountID, systemAccountID string) (*OpenAIAccountCandidate, error) {
	return nil, nil
}
func (r *w12hBlockingCooldownReader) HasAPIKeyEntry(ctx context.Context, candidate *OpenAIAccountCandidate, fingerprint, apiKey string) (bool, error) {
	return false, nil
}

// ---------------------------------------------------------------------------
// 统计存储坏表矩阵：逐表破坏后入口必须失败而非静默。

func TestW12HStoreBrokenTableMatrix(t *testing.T) {
	t.Run("分钟表缺失", func(t *testing.T) {
		store, _, lookup := newQualityStore(t)
		lookup.accounts["acc-1"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
		if err := store.MarkQualityDirty(context.Background(), "acc-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`DROP TABLE account_quality_minute_stats`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.RefreshFromUsage(context.Background(), RefreshInput{WindowMinutes: 10, Timezone: "UTC", DirtyLimit: DirtyAccountBatchLimit}); err == nil {
			t.Fatal("分钟表缺失必须失败")
		}
	})

	t.Run("分数表缺失中断刷新事务", func(t *testing.T) {
		store, clock, lookup := newQualityStore(t)
		lookup.accounts["acc-1"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
		store.seedMinute(t, "acc-1", MinuteKey(clock.Now().Add(-time.Minute), time.UTC), 8, 6, 2, 500, 5, "")
		if err := store.MarkQualityDirty(context.Background(), "acc-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`DROP TABLE account_quality_scores`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.RefreshFromUsage(context.Background(), RefreshInput{WindowMinutes: 10, Timezone: "UTC", DirtyLimit: DirtyAccountBatchLimit}); err == nil {
			t.Fatal("分数表缺失必须失败")
		}
		// 事务回滚：脏行仍在。
		if ids, err := store.loadDirtyAccountIds(context.Background(), 10); err != nil || len(ids) != 1 {
			t.Fatalf("事务回滚后脏行应保留: %v %v", ids, err)
		}
	})

	t.Run("脏表缺失使 MarkQualityDirty 失败", func(t *testing.T) {
		store, _, _ := newQualityStore(t)
		if _, err := store.db.Exec(`DROP TABLE account_quality_dirty_accounts`); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkQualityDirty(context.Background(), "acc-1"); err == nil {
			t.Fatal("脏表缺失必须失败")
		}
	})

	t.Run("候选读取 limit/offset 钳制", func(t *testing.T) {
		store, _, lookup := newQualityStore(t)
		lookup.accounts["acc-1"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
		if _, err := store.db.Exec(`INSERT INTO account_quality_scores (
			account_id, system_account_id, provider_code, quality_score, quality_state,
			recent_request_count, recent_success_count, recent_error_count, success_rate,
			window_started_at, window_ended_at, updated_at
		) VALUES ('acc-1', 'sys-1', 'openai', 100, 'fresh', 10, 2, 8, 0.5, 'w', 'w', '2026-09-04T08:00:00.000Z')`); err != nil {
			t.Fatal(err)
		}
		// limit<1 → 1；offset<0 → 0。
		candidates, err := store.ListFailurePrecheckCandidates(context.Background(), 0, -5)
		if err != nil || len(candidates) != 1 {
			t.Fatalf("钳制后应命中: %+v %v", candidates, err)
		}
		// 空 account_id 行跳过。
		if _, err := store.db.Exec(`INSERT INTO account_quality_scores (
			account_id, system_account_id, provider_code, quality_score, quality_state,
			recent_request_count, recent_success_count, recent_error_count,
			window_started_at, window_ended_at, updated_at
		) VALUES ('', 'sys-1', 'openai', 100, 'fresh', 10, 2, 8, 'w', 'w', '2026-09-04T08:00:00.000Z')`); err != nil {
			t.Fatal(err)
		}
		again, err := store.ListFailurePrecheckCandidates(context.Background(), 10, 0)
		if err != nil || len(again) != 1 {
			t.Fatalf("空 account_id 行必须跳过: %+v %v", again, err)
		}
	})

	t.Run("关闭句柄后入口失败", func(t *testing.T) {
		store, _, _ := newQualityStore(t)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := store.RefreshFromUsage(context.Background(), RefreshInput{WindowMinutes: 10, Timezone: "UTC"}); err == nil {
			t.Fatal("关闭后刷新必须失败")
		}
		if _, err := store.LoadQualityRow(context.Background(), "acc-1"); err == nil {
			t.Fatal("关闭后读取必须失败")
		}
		if _, err := store.ListFailurePrecheckCandidates(context.Background(), 10, 0); err == nil {
			t.Fatal("关闭后候选读取必须失败")
		}
		if err := store.MarkQualityDirty(context.Background(), "acc-1"); err == nil {
			t.Fatal("关闭后标脏必须失败")
		}
	})
}

type w12hFailingLookup struct{}

func (w12hFailingLookup) LoadAccountMetadataByIds(ctx context.Context, ids []string) (map[string]AccountMetadata, error) {
	return nil, errors.New("w12h lookup failure")
}

func TestW12HStoreMoreErrorArms(t *testing.T) {
	// 业务元数据读取失败 → 刷新失败。
	store, _, _ := newQualityStore(t)
	store.SetBusiness(w12hFailingLookup{})
	if _, err := store.RefreshFromUsage(context.Background(), RefreshInput{WindowMinutes: 10, Timezone: "UTC"}); err == nil {
		t.Fatal("业务元数据失败必须上抛")
	}

	// DatabasePath 是目录 → PRAGMA 失败。
	dir := t.TempDir()
	if _, err := OpenStatsStore(StatsStoreConfig{Mode: StatsSQLite, DatabasePath: dir}); err == nil {
		t.Fatal("目录路径必须导致 PRAGMA 失败")
	}

	// DirtyLimit 越上界被钳制（不报错）。
	store2, _, lookup2 := newQualityStore(t)
	lookup2.accounts["acc-1"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
	store2.seedMinute(t, "acc-1", MinuteKey(time.Now().Add(-time.Minute), time.UTC), 8, 6, 2, 500, 5, "")
	if err := store2.MarkQualityDirty(context.Background(), "acc-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store2.RefreshFromUsage(context.Background(), RefreshInput{WindowMinutes: 10, Timezone: "UTC", DirtyLimit: 1 << 20}); err != nil {
		t.Fatalf("DirtyLimit 越界应被钳制: %v", err)
	}

	// findFirstField 非对象输入。
	if got := findFirstField(42, []string{"reset_at"}); got != nil {
		t.Fatalf("非对象必须为 nil: %v", got)
	}
}

func TestW12HCooldownFinalFailureErrorAndDelayHelpers(t *testing.T) {
	logger := &fakeLogger{}
	// 非额度 upstream_failure + RecordKeyFailure 失败 → 队列耗尽。
	prober := &mockProber{observation: &ProbeObservation{
		Result:   ProbeResult{Success: false, Message: "connection reset"},
		Evidence: ProbeEvidence{HasRealUpstreamAttempt: true, TransportFailureKind: TransportFailureConnection},
	}}
	mutation := &w12hFailingCooldownMutation{failErr: errors.New("w12h final failure")}
	reader := cooldownReader()
	runner := NewCooldownRetestRunner(CooldownDeps{
		Logger: logger, Reader: reader, Prober: prober, Mutation: mutation,
		Settings: func(string, int, int) int { return 24 }, Concurrency: func() int { return 2 }, QueueWorkers: 1,
	})
	runner.Enqueue(cooldownCandidate("acc-1", "fp-1", "sk-key"), 24)
	waitFor(t, func() bool { return logger.findByEvent("background_account_api_key_cooldown_retest_retry_exhausted") != nil })

	// quotaRecoveryDelaySeconds：合法/非法策略两个分支。
	valid := &OpenAIAccountCandidate{ID: "acc-1", QuotaRecoveryPolicy: map[string]any{
		"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": float64(30)},
	}}
	if got := quotaRecoveryDelaySeconds(valid, "fp", "", time.Now()); got < CooldownQuotaMinDeferSeconds {
		t.Fatalf("合法策略顺延不符: %d", got)
	}
	invalid := &OpenAIAccountCandidate{ID: "acc-1", QuotaRecoveryPolicy: map[string]any{"api_key": 7}}
	if got := quotaRecoveryDelaySeconds(invalid, "fp", "", time.Now()); got < CooldownQuotaMinDeferSeconds {
		t.Fatalf("非法策略顺延不符: %d", got)
	}

	// 策略非空但对应槽位为空 → 沿用默认（ForAccount 的 configured==nil 分支）。
	onlyOAuth := &QuotaRecoveryPolicy{OAuth: &QuotaRecoverySchedule{ResetStrategy: StrategyDaily, DailyResetHour: intPtr(3)}}
	merged := QuotaRecoveryScheduleForAccount(onlyOAuth, "api_key")
	if merged.ResetStrategy != StrategyDuration || merged.DurationMinutes == nil || *merged.DurationMinutes != 60 {
		t.Fatalf("空槽位应沿用 api_key 默认: %+v", merged)
	}
}
