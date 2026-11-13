package circuitruntime

// w7x：BUG-0364 回归——circuitstate Lua 以 cjson 序列化 states/escalation 条目，
// 空表被编码为 `{}`（空对象），运行时 Lua 读路径天然容忍，但 backfill 的严格
// 解码曾被它全量中止（2026-11-13 revision 24 升级窗实录，数据面修复 15 分钟
// 后即被运行进程复发）。本文件钉死解码容忍面：`{}`/null 视为空数组，其余
// 非法形态仍报错；finalize 收敛时清掉残留 error 字段。

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func w7xLenientScopeKey(t *testing.T) string {
	t.Helper()
	return mustGatewayAccountCircuitScopeKey(w7bAccountScope("acct-lenient"))
}

func w7xEntryWithReplayOrder(t *testing.T, replayOrder string) string {
	t.Helper()
	scopeKey := w7xLenientScopeKey(t)
	return `{"state":{"scopeKey":"` + scopeKey +
		`","scope":{"kind":"account","accountRuntimeKey":"acct-lenient"},"phase":"OPEN","generation":1,"dispatchRevision":"3","transitionId":"t1","updatedAtMs":100},"replayOrder":` +
		replayOrder + `}`
}

func TestW7XLenientEmptyTableReplayOrder(t *testing.T) {
	scopeKey := w7xLenientScopeKey(t)
	// cjson 空表形态（{}）与显式 null：视为空数组，解码成功。
	for _, order := range []string{"{}", "null"} {
		item, err := accountCircuitRuntimeIndexItemFromSource("states", scopeKey, w7xEntryWithReplayOrder(t, order))
		if err != nil {
			t.Fatalf("replayOrder=%s 必须容忍: %v", order, err)
		}
		if item.Runtime != "acct-lenient" || item.Revision != 3 {
			t.Fatalf("replayOrder=%s item = %+v", order, item)
		}
	}
	// 真数组仍正常。
	if _, err := accountCircuitRuntimeIndexItemFromSource("states", scopeKey,
		w7xEntryWithReplayOrder(t, `["t1","t2"]`)); err != nil {
		t.Fatalf("真数组必须解码成功: %v", err)
	}
	// 其余非法形态仍报错：非空对象、字符串、数字。
	for _, order := range []string{`{"a":1}`, `"abc"`, `3`} {
		if _, err := accountCircuitRuntimeIndexItemFromSource("states", scopeKey,
			w7xEntryWithReplayOrder(t, order)); err == nil {
			t.Fatalf("replayOrder=%s 必须报错", order)
		}
	}
}

func TestW7XLenientEmptyTableEvidenceScopes(t *testing.T) {
	// escalation：scopes 空表容忍为空数组；非空对象仍报错。
	if _, err := accountCircuitRuntimeIndexItemFromSource("escalation", "acct-lenient",
		`{"dispatchRevision":"5","scopes":{}}`); err != nil {
		t.Fatalf("scopes={} 必须容忍: %v", err)
	}
	if _, err := accountCircuitRuntimeIndexItemFromSource("escalation", "acct-lenient",
		`{"dispatchRevision":"5","scopes":{"x":1}}`); err == nil {
		t.Fatal("scopes 非空对象必须报错")
	}
}

// TestW7XFinalizeClearsStaleError 钉死 MINOR-1：finalize 收敛只覆写 status/phase
// 等字段，上一次失败经 invalidate 臂写入的 error 字段曾与 ready 并存误导诊断；
// 收敛时必须 HDEL error。
func TestW7XFinalizeClearsStaleError(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer func() { _ = client.Close() }()
	keys, err := accountCircuitRevisionRedisKeys("w7x", AccountCircuitRedisStoreName)
	if err != nil {
		t.Fatal(err)
	}
	meta := keys.indexMeta
	lock := keys.indexLock
	ctx := context.Background()
	if err := client.Set(ctx, lock, "w7x-token", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.HSet(ctx, meta, map[string]any{
		"version": "1", "status": "building", "buildEpoch": "ep-1", "phase": "auditing",
		"error": "decode account circuit runtime state source: stale failure",
	}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := finalizeAccountCircuitRuntimeIndexScript.Run(ctx, client,
		[]string{lock, meta}, "w7x-token", "ep-1", "1000").Err(); err != nil {
		t.Fatalf("finalize 失败: %v", err)
	}
	stale, err := client.HGet(ctx, meta, "error").Result()
	if err == nil && stale != "" {
		t.Fatalf("收敛后 error 字段必须被清除, got %q", stale)
	}
	status, err := client.HGet(ctx, meta, "status").Result()
	if err != nil || status != "ready" {
		t.Fatalf("收敛后 status 必须 ready, got %q err=%v", status, err)
	}
}
