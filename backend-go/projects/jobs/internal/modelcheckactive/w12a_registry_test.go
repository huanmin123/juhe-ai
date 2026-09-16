package modelcheckactive

import (
	"context"
	"testing"
	"time"
)

// w12a_registry_test.go 覆盖空键/缺失条目/补丁字段与句柄更新的分支。

func TestW12aRegistryEmptyKeyAndMissingEntries(t *testing.T) {
	registry := NewRegistry()
	if _, ok, _ := registry.TryStart(context.Background(), "   ", Summary{}); ok {
		t.Fatalf("空键不应启动")
	}
	if _, ok := registry.Stop("w12a-missing"); ok {
		t.Fatalf("缺失条目 Stop 不应命中")
	}
	if _, ok := registry.Get("w12a-missing"); ok {
		t.Fatalf("缺失条目 Get 不应命中")
	}
	if registry.Update("w12a-missing", Summary{RunID: "r"}) {
		t.Fatalf("缺失条目 Update 不应成功")
	}
	if registry.Update("", Summary{RunID: "r"}) {
		t.Fatalf("空键 Update 不应成功")
	}
}

func TestW12aHandleUpdateOwnershipAndPatchFields(t *testing.T) {
	registry := NewRegistry()
	handle, started, _ := registry.TryStart(context.Background(), "w12a-key", Summary{RunID: "run-1", TargetID: "acct"})
	if !started {
		t.Fatalf("应启动成功")
	}
	// 句柄更新：逐字段补丁。
	if !handle.Update(Summary{RunID: "run-2", TargetName: "target", Model: "m", Profile: "quick", StartedAt: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}) {
		t.Fatalf("持有句柄的更新应成功")
	}
	summary, ok := registry.Get("w12a-key")
	if !ok || summary.RunID != "run-2" || summary.TargetName != "target" || summary.Model != "m" || summary.Profile != "quick" || summary.StartedAt.IsZero() {
		t.Fatalf("补丁字段不符: %#v", summary)
	}
	// StopRequest 补丁。
	if !handle.Update(Summary{StopRequest: true}) {
		t.Fatalf("StopRequest 补丁应成功")
	}
	if summary, _ = registry.Get("w12a-key"); !summary.StopRequest {
		t.Fatalf("StopRequest 应被写入")
	}
	// Finish 后句柄更新失败（所有权转移给新条目或移除）。
	handle.Finish()
	if handle.Update(Summary{RunID: "late"}) {
		t.Fatalf("Finish 后的句柄更新应失败")
	}
	// 空 Handle 分支。
	var empty Handle
	if empty.Update(Summary{RunID: "x"}) {
		t.Fatalf("空句柄更新应失败")
	}
	empty.Finish()
	if empty.Context() == nil {
		t.Fatalf("空句柄 Context 应回退 Background")
	}
}

func TestW12aStopCancelsHandleContext(t *testing.T) {
	registry := NewRegistry()
	handle, started, _ := registry.TryStart(context.Background(), "w12a-key", Summary{})
	if !started {
		t.Fatalf("应启动成功")
	}
	stopped, ok := registry.Stop("w12a-key")
	if !ok || !stopped.StopRequest {
		t.Fatalf("Stop 应返回带 StopRequest 的摘要: %#v %v", stopped, ok)
	}
	select {
	case <-handle.Context().Done():
	default:
		t.Fatalf("Stop 应取消句柄上下文")
	}
	// Stop 后条目仍在，Get 仍命中。
	if _, ok := registry.Get("w12a-key"); !ok {
		t.Fatalf("Stop 不应移除条目")
	}
	handle.Finish()
	if _, ok := registry.Get("w12a-key"); ok {
		t.Fatalf("Finish 应移除条目")
	}
}
