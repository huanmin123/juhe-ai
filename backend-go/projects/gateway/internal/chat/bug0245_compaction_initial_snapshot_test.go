package chat

// BUG-0245 回归：已安装 checkpoint 的会话再次压缩时，prior snapshot 的
// importantToolResults/imageMemories 必须从 checkpoint 条目恢复。写入侧
// snapshotEntries 对这两类 kind 落数组 JSON，读回侧 initialSnapshot 必须
// 按 kind 分派反序列化目标（数组保真），不得统一 unmarshal 到 map 后跳过。

import (
	"reflect"
	"testing"
)

// TestBug0245InitialSnapshotRestoresCheckpointMemories 锁定"写入 → 读回"
// 链路：snapshotEntries 产出的 checkpoint 条目经 initialSnapshot 恢复后，
// 两类数组记忆与单对象 kind（durable_memory/task_state）字段全部保留。
func TestBug0245InitialSnapshotRestoresCheckpointMemories(t *testing.T) {
	service := NewCompactionService(nil, nil, nil, nil)
	prior := emptySnapshot()
	prior.DurableMemory = []string{"偏好简体中文"}
	prior.Constraints = []string{"不改线上数据"}
	prior.Decisions = []string{"采用最小方案"}
	prior.CurrentGoal = "完成压缩续跑修复"
	prior.Completed = []string{"定位根因"}
	prior.Pending = []string{"补回归测试"}
	prior.RecentUserIntent = "保留压缩记忆"
	prior.Uncertainties = []string{"无"}
	prior.ImportantToolResults = []map[string]any{
		{"name": "web_search", "result": "检索到三条关键结论"},
		{"name": "run_command", "result": "命令输出摘要"},
	}
	prior.ImageMemories = []map[string]any{
		{
			"assetId":       "asset-1",
			"summary":       "一张部署拓扑截图",
			"ocr":           []string{"拓扑标题"},
			"relevantFacts": []string{"单机形态"},
			"uncertainties": []string{"截图时间未知"},
		},
	}

	// 写入侧：snapshotEntries 生成 checkpoint 条目（tool_result/image_observation
	// 仅在非空时落条目，contentJSON 为数组 JSON）。
	entries := snapshotEntries(service, prior)
	if len(entries) != 4 {
		t.Fatalf("应生成 4 条 entry（对象 kind ×2 + 数组 kind ×2）: %d", len(entries))
	}
	contextEntries := make([]contextEntry, 0, len(entries))
	for _, entry := range entries {
		contextEntries = append(contextEntries, contextEntry{kind: entry.Kind, contentJSON: string(entry.Content)})
	}

	restored := initialSnapshot(contextEntries)

	// 数组记忆恢复（修复前 unmarshal 到 map 必败被跳过，恒为空）。
	if !reflect.DeepEqual(restored.ImportantToolResults, prior.ImportantToolResults) {
		t.Fatalf("importantToolResults 丢失: %+v", restored.ImportantToolResults)
	}
	if !reflect.DeepEqual(restored.ImageMemories, prior.ImageMemories) {
		t.Fatalf("imageMemories 丢失: %+v", restored.ImageMemories)
	}
	// 单对象 kind 回归不变。
	if !reflect.DeepEqual(restored.DurableMemory, prior.DurableMemory) ||
		!reflect.DeepEqual(restored.Constraints, prior.Constraints) ||
		!reflect.DeepEqual(restored.Decisions, prior.Decisions) {
		t.Fatalf("durable_memory 字段恢复不正确: %+v", restored)
	}
	if restored.CurrentGoal != prior.CurrentGoal ||
		!reflect.DeepEqual(restored.Completed, prior.Completed) ||
		!reflect.DeepEqual(restored.Pending, prior.Pending) ||
		restored.RecentUserIntent != prior.RecentUserIntent ||
		!reflect.DeepEqual(restored.Uncertainties, prior.Uncertainties) {
		t.Fatalf("task_state 字段恢复不正确: %+v", restored)
	}
}

// TestBug0245InitialSnapshotSkipsMalformedArrayKinds 锁定畸形条目容错：
// 数组 kind 携带对象形态或坏 JSON 时跳过该条目，不破坏其余条目恢复。
func TestBug0245InitialSnapshotSkipsMalformedArrayKinds(t *testing.T) {
	restored := initialSnapshot([]contextEntry{
		{kind: "task_state", contentJSON: `{"currentGoal":"目标","completed":["已完成"],"pending":[],"recentUserIntent":"意图","uncertainties":[]}`},
		// 对象形态的 tool_result（与写入契约不符）与坏 JSON 均应跳过。
		{kind: "tool_result", contentJSON: `{"name":"工具"}`},
		{kind: "image_observation", contentJSON: `{bad`},
	})
	if restored.CurrentGoal != "目标" || !reflect.DeepEqual(restored.Completed, []string{"已完成"}) {
		t.Fatalf("畸形数组条目不应影响对象 kind 恢复: %+v", restored)
	}
	if len(restored.ImportantToolResults) != 0 || len(restored.ImageMemories) != 0 {
		t.Fatalf("畸形数组条目应被跳过: %+v", restored)
	}
}
