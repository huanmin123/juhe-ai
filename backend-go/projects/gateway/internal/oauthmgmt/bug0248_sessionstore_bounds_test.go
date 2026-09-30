// BUG-0248 契约 2：OAuth 授权会话 store 只靠同键惰性删除时，未完成授权会
// 单调积累内存。本文件钉住「写入时顺带清扫过期项 + maxEntries 容量裁剪」
// 契约（对齐 kernel.DeduplicationStore 的 maxEntries+trim 先例），并确认
// get 语义不变。
package oauthmgmt

import (
	"fmt"
	"testing"
	"time"
)

func TestBug0248SessionStoreSweepsExpiredOnWrite(t *testing.T) {
	clock := time.UnixMilli(1_000_000)
	store := newSessionStore(func() time.Time { return clock })

	// 写入一批将过期的会话。
	for i := 0; i < 128; i++ {
		store.set("grok-oauth:sessions", fmt.Sprintf("old-%d", i), map[string]string{"state": fmt.Sprint(i)}, oauthSessionTTL)
	}
	if len(store.entries) != 128 {
		t.Fatalf("预热条目数=%d want 128", len(store.entries))
	}

	// 时钟越过 TTL 后写入新会话：旧条目应被顺带清扫，只剩新写入。
	clock = clock.Add(oauthSessionTTL + time.Minute)
	store.set("grok-oauth:sessions", "fresh", map[string]string{"state": "fresh"}, oauthSessionTTL)
	if len(store.entries) != 1 {
		t.Fatalf("清扫后条目数=%d want 1", len(store.entries))
	}
	if raw := store.get("grok-oauth:sessions", "fresh"); raw == nil {
		t.Fatal("清扫必须保留刚写入的存活会话")
	}
}

func TestBug0248SessionStoreTrimsToMaxEntries(t *testing.T) {
	clock := time.UnixMilli(2_000_000)
	store := newSessionStore(func() time.Time { return clock })

	total := oauthSessionMaxEntries + 37
	for i := 0; i < total; i++ {
		store.set("openai-oauth:sessions", fmt.Sprintf("s-%d", i), map[string]string{"state": fmt.Sprint(i)}, oauthSessionTTL)
		if len(store.entries) > oauthSessionMaxEntries {
			t.Fatalf("写入 %d 条后条目数=%d 超过上限 %d", i+1, len(store.entries), oauthSessionMaxEntries)
		}
	}
	if len(store.entries) != oauthSessionMaxEntries {
		t.Fatalf("条目数=%d want %d", len(store.entries), oauthSessionMaxEntries)
	}
	// 裁剪保护刚写入的 key：最后一个会话必须存活且可读。
	last := fmt.Sprintf("s-%d", total-1)
	raw := store.get("openai-oauth:sessions", last)
	if raw == nil {
		t.Fatal("容量裁剪必须保护刚写入的会话")
	}
	// get 语义不变：不存在的键返回 nil。
	if store.get("openai-oauth:sessions", "never-written") != nil {
		t.Fatal("缺失会话必须返回 nil")
	}
	// compareDelete 语义不变：匹配即单次消费删除。
	if !store.compareDelete("openai-oauth:sessions", last, map[string]string{"state": fmt.Sprint(total - 1)}) {
		t.Fatal("存活会话的 compareDelete 必须成功")
	}
	if store.compareDelete("openai-oauth:sessions", last, map[string]string{"state": fmt.Sprint(total - 1)}) {
		t.Fatal("已消费会话的 compareDelete 必须失败")
	}
}
