package chat

// w16c 覆盖收尾：包内纯函数与无副作用入口的错误臂/罕见分支。

import (
	"strings"
	"testing"
	"time"
)

// TestW16CImageResultChunkErrorArms 覆盖 extractImageResultChunks* 错误臂。
func TestW16CImageResultChunksErrorArms(t *testing.T) {
	// 字符串值未闭合 → 扫描器报截断错误。
	if got := extractImageResultChunksWithFields(`{"result": "abc`, "result"); got != nil {
		t.Fatalf("截断 JSON 应返回 nil: %v", got)
	}
	if got := extractImageResultChunks(`{"b64_json": "xyz`); got != nil {
		t.Fatalf("截断 JSON 应返回 nil: %v", got)
	}
	if got := extractImageResultChunksWithFields(`{"result":"aa"}`, "result"); len(got) != 1 {
		t.Fatalf("合法 result = %v", got)
	}
}

// TestW16CMustJSONErrorArm 覆盖 mustJSON 的 Marshal 失败臂。
func TestW16CMustJSONErrorArm(t *testing.T) {
	if got := mustJSON(make(chan int)); got != "" {
		t.Fatalf("不可序列化值应返回空串: %q", got)
	}
	if got := mustJSON(map[string]any{"a": 1.0}); got != `{"a":1}` {
		t.Fatalf("正常序列化 = %q", got)
	}
}

// TestW16CNewGenerationHubDefaultClock 覆盖 now==nil 默认时钟臂。
func TestW16CNewGenerationHubDefaultClock(t *testing.T) {
	hub := NewGenerationHub(nil)
	if hub == nil || hub.terminalSnapshotLimit != 512 || hub.now == nil {
		t.Fatalf("默认 hub 构造不正确: %+v", hub)
	}
	if value := hub.now(); !strings.Contains(value, "Z") {
		t.Fatalf("默认时钟应输出 ISO 毫秒: %q", value)
	}
}

// TestW16CContextHeadFromRowBadState 覆盖 contextHeadFromRow 未知状态错误臂。
func TestW16CContextHeadFromRowBadState(t *testing.T) {
	if head, err := contextHeadFromRow(contextHeadRow{contextState: "bogus"}); err == nil || head != nil {
		t.Fatalf("未知状态应报错: %+v/%v", head, err)
	}
	head, err := contextHeadFromRow(contextHeadRow{contextState: string(StateReady), id: "c1"})
	if err != nil || head == nil || head.ContextState != StateReady {
		t.Fatalf("就绪状态 = %+v/%v", head, err)
	}
}

// TestW16CActiveCheckpointHelpers 覆盖 checkpoint 非空臂。
func TestW16CActiveCheckpointHelpers(t *testing.T) {
	if activeCheckpointID(nil) != nil || activeCheckpointExpires(nil) != nil {
		t.Fatal("空 checkpoint 应返回 nil")
	}
	marker := &checkpoint{id: "ckpt_1", expiresAt: "2026-03-10T09:00:00.000Z"}
	if got := activeCheckpointID(marker); got == nil || *got != "ckpt_1" {
		t.Fatalf("activeCheckpointID = %v", got)
	}
	if got := activeCheckpointExpires(marker); got != marker.expiresAt {
		t.Fatalf("activeCheckpointExpires = %v", got)
	}
}

// TestW16CNormalizeChatImageFormatQuality 覆盖 png 输出格式与空 quality 分支。
func TestW16CNormalizeChatImageFormatQuality(t *testing.T) {
	if got, err := normalizeChatImageOutputFormat("PNG"); err != nil || got != "png" {
		t.Fatalf("png 格式 = %q/%v", got, err)
	}
	if got, err := normalizeChatImageOutputFormat(" Jpg "); err != nil || got != "jpeg" {
		t.Fatalf("jpg 格式 = %q/%v", got, err)
	}
	if _, err := normalizeChatImageOutputFormat("bmp"); err == nil {
		t.Fatal("bmp 应失败")
	}
	if got, err := normalizeChatImageQuality("   "); err != nil || got != "auto" {
		t.Fatalf("空白 quality = %q/%v", got, err)
	}
	if got, err := normalizeChatImageQuality("High"); err != nil || got != "high" {
		t.Fatalf("high quality = %q/%v", got, err)
	}
}

// TestW16CRemainingTextBytesOverflow 覆盖剩余字节为负的截断臂。
func TestW16CRemainingTextBytesOverflow(t *testing.T) {
	blocks := []*assistantBlock{{Type: "output_text", Text: strings.Repeat("x", 50)}}
	if got := remainingTextBytes(blocks, "output_text", 10); got != 0 {
		t.Fatalf("超限应返回 0: %d", got)
	}
	if got := remainingTextBytes(blocks, "reasoning", 10); got != 10 {
		t.Fatalf("类型不匹配应返回全量: %d", got)
	}
}

// TestW16CSanitizeToolEventArms 覆盖 Item 克隆失败与超预算臂。
func TestW16CSanitizeToolEventArms(t *testing.T) {
	// Item 序列化失败 → cloneJSONMap 为 nil → 返回 base。
	base := sanitizeToolEvent(&ChatGenerationToolEvent{ID: "c1", ToolType: "web_search", Status: asstStarted, Item: map[string]any{"bad": make(chan int)}})
	if base.Item != nil {
		t.Fatalf("不可序列化 Item 应被丢弃: %+v", base)
	}
	// Item 超预算 → 返回 base。
	huge := sanitizeToolEvent(&ChatGenerationToolEvent{ID: "c2", ToolType: "web_search", Status: asstStarted, Item: map[string]any{"k": strings.Repeat("a", chatGenerationToolJSONMaxBytes+64)}})
	if huge.Item != nil {
		t.Fatalf("超预算 Item 应被丢弃: %+v", huge)
	}
	// 正常路径保留 Item。
	ok := sanitizeToolEvent(&ChatGenerationToolEvent{ID: "c3", ToolType: "web_search", Status: asstStarted, Item: map[string]any{"k": "v"}})
	if ok.Item == nil || ok.Item["k"] != "v" {
		t.Fatalf("正常 Item 应保留: %+v", ok)
	}
}

// TestW16CCheckpointEntryMarshalFail 覆盖 checkpointEntry 序列化失败臂。
func TestW16CCheckpointEntryMarshalFail(t *testing.T) {
	service := &CompactionService{TokenCount: func(text string) int { return (len(text) + 3) / 4 }}
	entry := checkpointEntry(service, "image_observation", make(chan int), "asset")
	if string(entry.Content) != "{}" {
		t.Fatalf("序列化失败应回退 {}：%q", string(entry.Content))
	}
	entryOK := checkpointEntry(service, "image_observation", map[string]any{"assetId": "a1"}, "asset")
	if entryOK.TokenCount == nil || *entryOK.TokenCount <= 0 || entryOK.TrustLevel != "assistant_derived" {
		t.Fatalf("正常条目 = %+v", entryOK)
	}
}

// TestW16CPassiveDelayISOWideWindow 覆盖 half<windowMs 收敛臂。
func TestW16CPassiveDelayISOWideWindow(t *testing.T) {
	base := time.Date(2026, 3, 10, 8, 0, 0, 0, time.UTC)
	service := &CompactionService{WallClock: func() time.Time { return base }, Random: func() float64 { return 0.5 }}
	// interval=1_000_000 → windowMs=30 分钟，half=500_000 小于 windowMs。
	value := service.passiveDelayISO(1_000_000)
	if !strings.HasSuffix(value, "Z") {
		t.Fatalf("passiveDelayISO 输出 = %q", value)
	}
	// 采样值越界回退 0。
	service.Random = func() float64 { return 1.5 }
	if value2 := service.passiveDelayISO(1_000_000); value2 == "" {
		t.Fatal("越界采样不应产生空值")
	}
}

// TestW16CReleaseRetryAtWallClock 覆盖 deps.Now==nil 的墙钟臂。
func TestW16CReleaseRetryAtWallClock(t *testing.T) {
	rt := &chatRoutes{deps: &Deps{}}
	if got := rt.releaseRetryAt(); !strings.HasSuffix(got, "Z") {
		t.Fatalf("releaseRetryAt = %q", got)
	}
	fixed := time.Date(2026, 3, 10, 8, 0, 0, 0, time.UTC)
	rt2 := &chatRoutes{deps: &Deps{Now: func() time.Time { return fixed }}}
	// Node 语义 now + 60_000ms（60 秒）；修复后按秒域断言。
	if got := rt2.releaseRetryAt(); got != "2026-03-10T08:01:00.000Z" {
		t.Fatalf("注入时钟 releaseRetryAt = %q", got)
	}
}
