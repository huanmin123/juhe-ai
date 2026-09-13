package gatewaycodex

import (
	"context"
	"testing"
)

func TestWIShardStoreCRUDRoundTrip(t *testing.T) {
	store, _ := newSQLiteStore(t)
	ctx := context.Background()

	// 响应行往返：save 后按 ID 读回，字段一致。
	row := CodexContextResponseStateIndex{
		CodexContextStateBoundary: CodexContextStateBoundary{
			SystemAccountID: "sys", GroupID: "group", ProviderCode: "openai",
		},
		ResponseID:         "resp-1",
		SessionID:          "session-1",
		PreviousResponseID: "",
		Model:              "gpt-5",
		UpstreamModel:      "gpt-5",
		CreatedAt:          "2026-01-01T00:00:00.000Z",
		UpdatedAt:          "2026-01-01T00:00:00.000Z",
		ExpiresAt:          "2026-01-02T00:00:00.000Z",
	}
	if err := store.SaveResponseStateRow(ctx, row); err != nil {
		t.Fatalf("保存响应行失败: %v", err)
	}
	loaded, err := store.ReadResponseStateRow(ctx, "resp-1")
	if err != nil {
		t.Fatalf("读取响应行失败: %v", err)
	}
	if loaded.ResponseID != "resp-1" || loaded.SessionID != "session-1" || loaded.Model != "gpt-5" {
		t.Fatalf("读回=%+v", loaded)
	}
	// compact 行往返。
	compact := CodexContextCompactStateIndex{
		CodexContextStateBoundary: row.CodexContextStateBoundary,
		CompactID:                 "compact-1",
		SessionID:                 "session-1",
		SummaryDigest:             stringsRepeat("a", 64),
		UpdatedAt:                 "2026-01-01T00:00:00.000Z",
		ExpiresAt:                 "2026-01-02T00:00:00.000Z",
	}
	if err := store.SaveCompactStateRow(ctx, compact); err != nil {
		t.Fatalf("保存 compact 行失败: %v", err)
	}
	loadedCompact, err := store.ReadCompactStateRow(ctx, "compact-1")
	if err != nil || loadedCompact.CompactID != "compact-1" || loadedCompact.SummaryDigest != stringsRepeat("a", 64) {
		t.Fatalf("读回 compact=%+v err=%v", loadedCompact, err)
	}
	// touch：响应链与 compact 的 last_used/expires 刷新。
	if err := store.TouchResponseChain(ctx, []CodexContextResponseStateIndex{row}, "2026-01-03T00:00:00.000Z", "2026-01-04T00:00:00.000Z"); err != nil {
		t.Fatalf("touch 链失败: %v", err)
	}
	refreshed, err := store.ReadResponseStateRow(ctx, "resp-1")
	if err != nil || refreshed.LastUsedAt != "2026-01-03T00:00:00.000Z" || refreshed.ExpiresAt != "2026-01-04T00:00:00.000Z" {
		t.Fatalf("touch 后=%+v err=%v", refreshed, err)
	}
	if err := store.TouchCompact(ctx, compact, "2026-01-03T00:00:00.000Z", "2026-01-05T00:00:00.000Z"); err != nil {
		t.Fatalf("touch compact 失败: %v", err)
	}
	refreshedCompact, err := store.ReadCompactStateRow(ctx, "compact-1")
	if err != nil || refreshedCompact.ExpiresAt != "2026-01-05T00:00:00.000Z" {
		t.Fatalf("compact touch 后=%+v err=%v", refreshedCompact, err)
	}
	// 空 rows 的 touch 是 no-op。
	if err := store.TouchResponseChain(ctx, nil, "now", "later"); err != nil {
		t.Fatalf("空链 touch=%v", err)
	}
	// 契约：缺失行读作 (nil, nil)（Node undefined 语义）。
	if loadedMissing, err := store.ReadResponseStateRow(ctx, "missing"); err != nil || loadedMissing != nil {
		t.Fatalf("缺失行=%+v err=%v", loadedMissing, err)
	}
	if loadedMissingCompact, err := store.ReadCompactStateRow(ctx, "missing"); err != nil || loadedMissingCompact != nil {
		t.Fatalf("缺失 compact=%+v err=%v", loadedMissingCompact, err)
	}
	// 行为存疑：Close 释放全部已打开的 shard 连接，但下一次读取会经
	// databaseForKey 惰性重开文件并恢复数据（文件仍在），并不会使 store
	// 失效。按当前实际行为断言（重开后同一行仍可读回）。
	if err := store.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	if reloaded, err := store.ReadResponseStateRow(ctx, "resp-1"); err != nil || reloaded == nil || reloaded.ResponseID != "resp-1" {
		t.Fatalf("关闭后重开读回=%+v err=%v", reloaded, err)
	}
}
