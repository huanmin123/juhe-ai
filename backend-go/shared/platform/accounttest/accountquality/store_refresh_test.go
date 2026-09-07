package accountquality

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// mapBusinessLookup 以内存 map 模拟业务库 accounts 元数据。
type mapBusinessLookup struct {
	accounts map[string]AccountMetadata
}

func (m *mapBusinessLookup) LoadAccountMetadataByIds(ctx context.Context, ids []string) (map[string]AccountMetadata, error) {
	out := map[string]AccountMetadata{}
	for _, id := range ids {
		if meta, ok := m.accounts[id]; ok {
			out[id] = meta
		}
	}
	return out, nil
}

// fakeClock 测试用手动时钟（本包 Clock 注入实现）。
type fakeClock struct {
	current time.Time
}

func (c *fakeClock) Now() time.Time { return c.current }

func (c *fakeClock) Advance(d time.Duration) { c.current = c.current.Add(d) }

func newQualityStore(t *testing.T) (*StatsStore, *fakeClock, *mapBusinessLookup) {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStatsStore(StatsStoreConfig{
		Mode:         StatsSQLite,
		DatabasePath: filepath.Join(dir, "quality.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clock := &fakeClock{current: time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)}
	store.SetClock(clock)
	lookup := &mapBusinessLookup{accounts: map[string]AccountMetadata{}}
	store.SetBusiness(lookup)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store, clock, lookup
}

func (s *StatsStore) seedMinute(t *testing.T, accountID string, statMinute string, requests, successes, errors, ftSum, ftCount int, lastErrorMessage string) {
	t.Helper()
	_, err := s.db.Exec(`INSERT INTO account_quality_minute_stats (
		account_id, system_account_id, provider_code, stat_minute, request_count, success_count,
		error_count, first_token_ms_sum, first_token_ms_count, last_sample_at, last_success_at,
		last_error_at, last_error_message, updated_at
	) VALUES (?, 'sys-1', 'openai', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		accountID, statMinute, requests, successes, errors, ftSum, ftCount,
		"2026-09-04T07:59:00.000Z", "2026-09-04T07:59:00.000Z", "2026-09-04T07:59:30.000Z", lastErrorMessage,
		"2026-09-04T07:59:30.000Z")
	if err != nil {
		t.Fatal(err)
	}
}

func TestRefreshFromUsageComputesScores(t *testing.T) {
	store, clock, lookup := newQualityStore(t)
	ctx := context.Background()
	lookup.accounts["acc-1"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
	lookup.accounts["acc-2"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}

	windowStart := clock.Now().Add(-10 * time.Minute).In(time.UTC)
	minute := MinuteKey(clock.Now().Add(-time.Minute), time.UTC)
	store.seedMinute(t, "acc-1", minute, 8, 6, 2, 500, 5, "upstream 500")
	store.seedMinute(t, "acc-2", minute, 4, 4, 0, 200, 2, "")
	// Node 先认领 dirty 账户，再聚合分钟表；此处先标脏。
	if err := store.MarkQualityDirty(ctx, "acc-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkQualityDirty(ctx, "acc-2"); err != nil {
		t.Fatal(err)
	}

	result, err := store.RefreshFromUsage(ctx, RefreshInput{WindowMinutes: 10, DirtyLimit: DirtyAccountBatchLimit, Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Refreshed != 2 {
		t.Fatalf("应刷新 2 行: %d", result.Refreshed)
	}
	if _ = windowStart; false {
		t.Fatal("unreachable")
	}
	row1, err := store.LoadQualityRow(ctx, "acc-1")
	if err != nil || row1 == nil {
		t.Fatalf("读取 acc-1: %v", err)
	}
	if row1.RecentRequestCount != 8 || row1.RecentErrorCount != 2 || row1.RecentSuccessCount != 6 {
		t.Fatalf("聚合不符: %+v", row1)
	}
	if row1.QualityState != QualityFresh {
		t.Fatalf("有采样应为 fresh: %s", row1.QualityState)
	}
	if row1.EwmaFirstTokenMs == nil || *row1.EwmaFirstTokenMs != 100 {
		t.Fatalf("EWMA 应为 100: %v", row1.EwmaFirstTokenMs)
	}
	if row1.SuccessRate == nil || *row1.SuccessRate != 0.75 {
		t.Fatalf("成功率应为 0.75: %v", row1.SuccessRate)
	}
	if row1.LastErrorMessage == nil || *row1.LastErrorMessage != "upstream 500" {
		t.Fatalf("最后错误信息不符: %v", row1.LastErrorMessage)
	}
	// 质量分 = EWMA 100 + fresh 无罚 + 龄期 0。
	if row1.QualityScore != 100 {
		t.Fatalf("质量分应为 100: %d", row1.QualityScore)
	}

	// 第二轮：acc-1 无新样本 → stale 降级；acc-3 已删除账户 → 行被清理。
	lookup.accounts["acc-3"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
	if _, err := store.db.Exec(`INSERT INTO account_quality_scores (account_id, system_account_id, provider_code, quality_score, quality_state, window_started_at, window_ended_at, updated_at)
		VALUES ('acc-3', 'sys-1', 'openai', 100, 'fresh', '2026-09-04T07:00:00.000Z', '2026-09-04T07:10:00.000Z', '2026-09-04T07:10:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM accounts_meta`); err != nil && false {
		t.Fatal(err)
	}
	delete(lookup.accounts, "acc-3")
	clock.Advance(15 * time.Minute)
	refreshed, err := store.RefreshFromUsage(ctx, RefreshInput{WindowMinutes: 10, DirtyLimit: DirtyAccountBatchLimit, Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	// acc-1/acc-2 在新窗口无样本（不再刷 fresh），acc-3 行被清理。
	staleRow, _ := store.LoadQualityRow(ctx, "acc-1")
	if staleRow == nil || staleRow.QualityState != QualityStale {
		t.Fatalf("acc-1 应降级 stale: %+v", staleRow)
	}
	if staleRow.QualityScore != StalePenaltyMs+100 {
		t.Fatalf("stale 质量分 = EWMA + 5s 罚分: %d", staleRow.QualityScore)
	}
	gone, _ := store.LoadQualityRow(ctx, "acc-3")
	if gone != nil {
		t.Fatalf("已删除账户的质量行应被清理: %+v", gone)
	}
	if refreshed.Removed != 1 {
		t.Fatalf("应清理 1 行: %d", refreshed.Removed)
	}
}

func TestRefreshStaleFreshAndUnknownTransition(t *testing.T) {
	store, clock, lookup := newQualityStore(t)
	ctx := context.Background()
	lookup.accounts["acc-1"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
	if _, err := store.db.Exec(`INSERT INTO account_quality_scores (account_id, system_account_id, provider_code, quality_score, quality_state, window_started_at, window_ended_at, updated_at)
		VALUES ('acc-1', 'sys-1', 'openai', 50, 'failed', '2026-09-04T07:00:00.000Z', '2026-09-04T07:10:00.000Z', '2026-09-04T07:10:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RefreshFromUsage(ctx, RefreshInput{WindowMinutes: 10, Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	row, _ := store.LoadQualityRow(ctx, "acc-1")
	if row.QualityState != QualityUnknown {
		t.Fatalf("failed 无样本应转 unknown: %s", row.QualityState)
	}
	if row.QualityScore != UnknownQualityScore+UnknownStatePenaltyMs {
		t.Fatalf("failed→unknown 质量分 = 1e6 + 10s: %d", row.QualityScore)
	}
	_ = clock
}

func TestListFailurePrecheckCandidates(t *testing.T) {
	store, _, lookup := newQualityStore(t)
	ctx := context.Background()
	lookup.accounts["acc-1"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
	lookup.accounts["acc-2"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
	lookup.accounts["acc-3"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
	lookup.accounts["acc-4"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}

	seed := func(accountID string, requests, errors int, rate *float64, updatedAt string) {
		var rateArg any
		if rate != nil {
			rateArg = *rate
		}
		if _, err := store.db.Exec(`INSERT INTO account_quality_scores (
			account_id, system_account_id, provider_code, quality_score, quality_state,
			recent_request_count, recent_success_count, recent_error_count, success_rate,
			window_started_at, window_ended_at, updated_at
		) VALUES (?, 'sys-1', 'openai', 100, 'fresh', ?, ?, ?, ?, 'w', 'w', ?)`,
			accountID, requests, requests-errors, errors, rateArg, updatedAt); err != nil {
			t.Fatal(err)
		}
	}
	// acc-1：高频错误（5 次）→ 命中，排序第一。
	seed("acc-1", 10, 5, ptrF64(0.5), "2026-09-04T08:00:00.000Z")
	// acc-2：错误 2 + 成功率 0.5 → 命中。
	seed("acc-2", 6, 2, ptrF64(0.5), "2026-09-04T07:59:00.000Z")
	// acc-3：错误 2 但成功率 1 → 不命中。
	seed("acc-3", 10, 2, ptrF64(1), "2026-09-04T07:58:00.000Z")
	// acc-4：请求不足 5 → 不命中。
	seed("acc-4", 4, 4, ptrF64(0), "2026-09-04T07:57:00.000Z")

	candidates, err := store.ListFailurePrecheckCandidates(ctx, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("应命中 2 个候选: %+v", candidates)
	}
	if candidates[0].AccountID != "acc-1" || candidates[1].AccountID != "acc-2" {
		t.Fatalf("排序不符: %+v", candidates)
	}
	if candidates[0].RecentErrorCount != 5 || candidates[0].SuccessRate == nil || *candidates[0].SuccessRate != 0.5 {
		t.Fatalf("字段不符: %+v", candidates[0])
	}

	// offset 越过第一条。
	offsetCands, err := store.ListFailurePrecheckCandidates(ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(offsetCands) != 1 || offsetCands[0].AccountID != "acc-2" {
		t.Fatalf("offset 应跳过 acc-1: %+v", offsetCands)
	}
}

func TestRefreshHonorsFenceAssertion(t *testing.T) {
	store, _, lookup := newQualityStore(t)
	lookup.accounts["acc-1"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
	fenceErr := fmt.Errorf("后台任务租约已失效：scheduled:account-quality-refresh:global")
	_, err := store.RefreshFromUsage(context.Background(), RefreshInput{
		WindowMinutes: 10,
		Timezone:      "UTC",
		Fence:         &FenceToken{LeaseKey: "scheduled:account-quality-refresh:global", OwnerID: "o", FencingToken: 7, Assert: func(ctx context.Context) error { return fenceErr }},
	})
	if err == nil || err.Error() != fenceErr.Error() {
		t.Fatalf("fence 校验失败应中断刷新: %v", err)
	}
}
