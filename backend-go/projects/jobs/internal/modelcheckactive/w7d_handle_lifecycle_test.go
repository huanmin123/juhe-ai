package modelcheckactive

// w7d（modelcheckactive 覆盖补齐）：Handle.Context/Finish/Update、Stop/Get/
// Update 键校验与孤儿句柄、并发竞争。进程内确定性，无外部依赖。

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestW7DTryStartRejectsBlankKeyAndNilContext(t *testing.T) {
	registry := NewRegistry()
	if handle, started, current := registry.TryStart(context.Background(), "   ", Summary{RunID: "r"}); started || handle.Context() == nil || current.RunID != "" {
		t.Fatalf("空白键必须拒绝: started=%t current=%#v", started, current)
	}
	// nil context 回落到 Background，Handle.Context 不 panic。
	handle, started, _ := registry.TryStart(nil, "nil-ctx", Summary{RunID: "r"})
	if !started {
		t.Fatal("nil context 必须回落 Background 并启动")
	}
	if handle.Context() == nil {
		t.Fatal("Handle.Context 不得为 nil")
	}
	handle.Finish()
}

func TestW7DZeroValueRegistryTryStartWorks(t *testing.T) {
	var registry Registry
	handle, started, _ := registry.TryStart(context.Background(), "lazy", Summary{RunID: "r1"})
	if !started {
		t.Fatal("零值 registry 必须懒初始化 active map")
	}
	handle.Finish()
}

func TestW7DHandleFinishCancelsChildAndIsIdempotent(t *testing.T) {
	registry := NewRegistry()
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	handle, started, _ := registry.TryStart(parent, "finish", Summary{RunID: "r1"})
	if !started {
		t.Fatal("首次启动必须成功")
	}
	handle.Finish()
	if handle.Context().Err() == nil {
		t.Fatal("Finish 必须取消子 context")
	}
	// 幂等 Finish 不得 panic，也不得影响同名新 run。
	again, started, _ := registry.TryStart(parent, "finish", Summary{RunID: "r2"})
	if !started {
		t.Fatal("Finish 后同键必须可重新启动")
	}
	handle.Finish()
	if again.Context().Err() != nil {
		t.Fatal("孤儿句柄的二次 Finish 不得取消新一轮 run")
	}
	again.Finish()
}

func TestW7DHandleUpdateOnlyWhileOwning(t *testing.T) {
	registry := NewRegistry()
	handle, started, _ := registry.TryStart(context.Background(), "update", Summary{RunID: "r1"})
	if !started {
		t.Fatal("启动失败")
	}
	// 空补丁不改动任何字段。
	if !handle.Update(Summary{}) {
		t.Fatal("持有期间 Update 必须成功")
	}
	current, ok := registry.Get("update")
	if !ok || current.RunID != "r1" {
		t.Fatalf("空补丁不得改动 summary: %#v ok=%t", current, ok)
	}
	// 非空补丁生效。
	if !handle.Update(Summary{Model: "gpt-x", StopRequest: true}) {
		t.Fatal("持有期间 Update 必须成功")
	}
	current, _ = registry.Get("update")
	if current.Model != "gpt-x" || !current.StopRequest {
		t.Fatalf("补丁未生效: %#v", current)
	}
	// Finish 后孤儿句柄 Update 必须失败，也不得影响新一轮。
	handle.Finish()
	orphan := handle
	next, started, _ := registry.TryStart(context.Background(), "update", Summary{RunID: "r2"})
	if !started {
		t.Fatal("重新启动失败")
	}
	if orphan.Update(Summary{RunID: "ghost"}) {
		t.Fatal("孤儿句柄 Update 必须失败")
	}
	current, _ = registry.Get("update")
	if current.RunID != "r2" {
		t.Fatalf("孤儿 Update 不得覆盖新一轮: %#v", current)
	}
	// Registry.Update 键级补丁只作用于仍在跑的条目。
	if !registry.Update("update", Summary{Profile: "full"}) {
		t.Fatal("在跑条目的键级 Update 必须成功")
	}
	next.Finish()
	if registry.Update("update", Summary{Profile: "quick"}) {
		t.Fatal("结束后键级 Update 必须失败")
	}
}

func TestW7DRegistryStopAndGetKeyValidation(t *testing.T) {
	registry := NewRegistry()
	if _, ok := registry.Stop("  "); ok {
		t.Fatal("空白键 Stop 必须失败")
	}
	if _, ok := registry.Get(""); ok {
		t.Fatal("空键 Get 必须失败")
	}
	if registry.Update("missing", Summary{RunID: "x"}) {
		t.Fatal("缺失键 Update 必须失败")
	}
	if _, ok := registry.Stop("missing"); ok {
		t.Fatal("缺失键 Stop 必须失败")
	}
	if _, ok := registry.Get("missing"); ok {
		t.Fatal("缺失键 Get 必须失败")
	}
	startedAt := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	handle, started, _ := registry.TryStart(context.Background(), "stop-me", Summary{RunID: "r1", StartedAt: startedAt})
	if !started {
		t.Fatal("启动失败")
	}
	summary, ok := registry.Stop(" stop-me ")
	if !ok || summary.RunID != "r1" || !summary.StopRequest || !summary.StartedAt.Equal(startedAt) {
		t.Fatalf("Stop 必须带 trim 返回 StopRequest summary: %#v ok=%t", summary, ok)
	}
	select {
	case <-handle.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("Stop 必须取消子 context")
	}
	handle.Finish()
}

func TestW7DConcurrentTryStartSingleWinner(t *testing.T) {
	registry := NewRegistry()
	const workers = 32
	results := make([]bool, workers)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := range results {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			handle, started, _ := registry.TryStart(context.Background(), "race", Summary{RunID: "r1"})
			results[index] = started
			if started {
				time.Sleep(time.Millisecond)
				handle.Finish()
			}
		}(index)
	}
	close(start)
	group.Wait()
	winners := 0
	for _, started := range results {
		if started {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("并发 TryStart 只能有一个赢家: %d", winners)
	}
}
