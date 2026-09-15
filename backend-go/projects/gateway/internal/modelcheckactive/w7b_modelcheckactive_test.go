package modelcheckactive

import (
	"context"
	"testing"
	"time"
)

// w7b：活跃运行注册表的边界与补丁分支覆盖。
func TestW7BRegistryEdgesAndPatches(t *testing.T) {
	r := NewRegistry()

	// 空键拒绝。
	if _, ok, _ := r.TryStart(context.Background(), "   ", Summary{}); ok {
		t.Fatal("空键必须拒绝")
	}
	// nil ctx 回退 Background。
	handle, ok, _ := r.TryStart(nil, "sys:nil", Summary{RunID: "run-0"})
	if !ok || handle.Context() == context.Background() {
		// ctx 为派生的可取消子上下文，不等于 Background。
		t.Fatal("nil ctx 必须派生子上下文")
	}
	handle.Finish()

	// 全字段补丁。
	handle, ok, _ = r.TryStart(context.Background(), "sys:patch", Summary{RunID: "r1"})
	if !ok {
		t.Fatal("启动失败")
	}
	started := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	if !handle.Update(Summary{RunID: "r2", TargetID: "t1", TargetName: "name", Model: "gpt", Profile: "chat", StartedAt: started, StopRequest: true}) {
		t.Fatal("更新必须成功")
	}
	got, ok := r.Get("  sys:patch  ")
	if !ok || got.RunID != "r2" || got.TargetID != "t1" || got.TargetName != "name" || got.Model != "gpt" || got.Profile != "chat" || !got.StartedAt.Equal(started) || !got.StopRequest {
		t.Fatalf("补丁结果 = %+v ok=%v", got, ok)
	}
	// 零值补丁不改写已有字段。
	if !handle.Update(Summary{}) {
		t.Fatal("零值补丁必须成功")
	}
	if got, _ := r.Get("sys:patch"); got.RunID != "r2" {
		t.Fatalf("零值补丁不得改写: %+v", got)
	}
	handle.Finish()
	// Finish 后的句柄：Update 失败、重复 Finish 安全、Stop/Get 未知键。
	if handle.Update(Summary{RunID: "r3"}) {
		t.Fatal("Finish 后更新必须失败")
	}
	handle.Finish()
	if _, ok := r.Stop("sys:patch"); ok {
		t.Fatal("Finish 后 Stop 必须失败")
	}
	if _, ok := r.Get("sys:unknown"); ok {
		t.Fatal("未知键 Get 必须失败")
	}
	if _, ok := r.Stop("   "); ok {
		t.Fatal("空键 Stop 必须失败")
	}

	// 零值句柄安全。
	var zero Handle
	if zero.Context() != context.Background() {
		t.Fatal("零值句柄必须回退 Background")
	}
	zero.Finish()
	if zero.Update(Summary{}) {
		t.Fatal("零值句柄更新必须失败")
	}

	// 停止后 Finish 由新属主接管：旧句柄 Finish 不得删除新条目。
	first, ok, _ := r.TryStart(context.Background(), "sys:handoff", Summary{RunID: "a"})
	if !ok {
		t.Fatal("first 启动失败")
	}
	r.Stop("sys:handoff")
	first.Finish()
	second, ok, _ := r.TryStart(context.Background(), "sys:handoff", Summary{RunID: "b"})
	if !ok {
		t.Fatal("second 启动失败")
	}
	first.Finish() // 旧句柄重复 Finish 不得误删 second
	if got, ok := r.Get("sys:handoff"); !ok || got.RunID != "b" {
		t.Fatalf("新属主必须保留: %+v ok=%v", got, ok)
	}
	second.Finish()
}
