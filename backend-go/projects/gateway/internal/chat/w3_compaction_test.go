package chat

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// 上下文压缩服务：状态机分支、快照解析、响应文本提取与重试延迟计算。
// 上游总结模型全部走 Mock 执行器，压缩结果用 SQLite fixture 断言。

// TestCompactionServiceSkipsW3 覆盖 runCompaction 的前置跳过分支。
func TestCompactionServiceSkipsW3(t *testing.T) {
	executor := &mockExecutor{}
	service := NewCompactionService(newChatFixture(t).store, executor, func(text string) int { return len(text) / 4 }, func() string { return "2026-03-10T08:00:00.000Z" })
	input := CompactionInput{ConversationID: "conv-missing", SystemAccountID: "owner", Model: "gpt-5"}
	t.Run("会话缺失", func(t *testing.T) {
		result := service.CompactOnce(context.Background(), input)
		if result.Status != "skipped" || result.Reason != "conversation_missing" {
			t.Fatalf("结果 = %+v", result)
		}
	})
	t.Run("无可用轮次", func(t *testing.T) {
		fixture := newChatFixture(t)
		service2 := NewCompactionService(fixture.store, executor, func(text string) int { return len(text) / 4 }, func() string { return fixture.nowISO })
		fixture.createConversation("conv_small", "owner")
		// 单轮时 sourceThroughSequence = 3-3 = 0，无可压缩区间。
		fixture.seedTurns("owner", "conv_small", 1)
		result := service2.CompactOnce(context.Background(), CompactionInput{ConversationID: "conv_small", SystemAccountID: "owner", Model: "gpt-5"})
		if result.Status != "skipped" || result.Reason != "no_compactable_turn" {
			t.Fatalf("结果 = %+v", result)
		}
	})
	t.Run("Start 与 already_running", func(t *testing.T) {
		fixture := newChatFixture(t)
		service3 := NewCompactionService(fixture.store, executor, func(text string) int { return len(text) / 4 }, func() string { return fixture.nowISO })
		fixture.createConversation("conv_start", "owner")
		fixture.seedTurns("owner", "conv_start", 4)
		accepted := service3.Start(context.Background(), CompactionInput{ConversationID: "conv_start", SystemAccountID: "owner", Model: "gpt-5"})
		if accepted.Status != "accepted" && accepted.Status != "failed" && accepted.Status != "skipped" {
			t.Fatalf("接受状态不正确: %+v", accepted)
		}
		// 等待首轮结束后再次 Start 走全新执行。
		second := service3.Start(context.Background(), CompactionInput{ConversationID: "conv_start", SystemAccountID: "owner", Model: "gpt-5"})
		if second.Status == "" {
			t.Fatalf("第二次 Start 应返回明确状态: %+v", second)
		}
	})
	t.Run("claim 失败进入 failClaim", func(t *testing.T) {
		fixture := newChatFixture(t)
		service4 := NewCompactionService(fixture.store, executor, func(text string) int { return len(text) / 4 }, func() string { return fixture.nowISO })
		fixture.createConversation("conv_fail", "owner")
		fixture.seedTurns("owner", "conv_fail", 4)
		result := service4.CompactOnce(context.Background(), CompactionInput{ConversationID: "conv_fail", SystemAccountID: "owner", Model: "gpt-5"})
		if result.Status == "" {
			t.Fatalf("结果不应为空")
		}
	})
}

// TestPassiveDelayISOW3 覆盖被动重试延迟的窗口与钳制契约。
func TestPassiveDelayISOW3(t *testing.T) {
	_, clock := fixedChatClock()
	service := NewCompactionService(nil, nil, nil, nil)
	service.WallClock = clock
	service.Random = func() float64 { return 0.5 }
	delay := service.passiveDelayISO(60000)
	parsed, err := parseRFC3339Instant(delay)
	if err != nil {
		t.Fatalf("延迟时间非法: %v", err)
	}
	offsetMs := parsed.UnixMilli() - clock().UnixMilli()
	if offsetMs < 1 || offsetMs > 120000 {
		t.Fatalf("延迟偏移超窗: %d", offsetMs)
	}
	service.Random = func() float64 { return 2 }
	smallDelay := service.passiveDelayISO(1)
	parsedSmall, _ := parseRFC3339Instant(smallDelay)
	if diff := parsedSmall.UnixMilli() - clock().UnixMilli(); diff < 1 || diff > 2000 {
		t.Fatalf("小间隔延迟超窗: %d", diff)
	}
}

// TestExtractCompactionResponseTextW3 覆盖两种协议的响应文本提取。
func TestExtractCompactionResponseTextW3(t *testing.T) {
	chatPayload := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "  {\"a\":1}  "}}}}
	if got := extractCompactionResponseText(chatPayload, ProtocolChatCompletions); got != `{"a":1}` {
		t.Fatalf("chat 提取失败: %q", got)
	}
	if got := extractCompactionResponseText(map[string]any{}, ProtocolChatCompletions); got != "" {
		t.Fatalf("chat 空载荷应为空: %q", got)
	}
	responsesPayload := map[string]any{"output_text": "直接文本"}
	if got := extractCompactionResponseText(responsesPayload, ProtocolResponses); got != "直接文本" {
		t.Fatalf("responses output_text 提取失败: %q", got)
	}
	structured := map[string]any{"output": []any{map[string]any{"content": []any{map[string]any{"text": "第一段"}, map[string]any{}, map[string]any{"text": "第二段"}}}}}
	if got := extractCompactionResponseText(structured, ProtocolResponses); got != "第一段\n第二段" {
		t.Fatalf("responses 结构化提取失败: %q", got)
	}
}

// TestParseJSONObjectLooseW3 覆盖围栏剥离与非法 JSON。
func TestParseJSONObjectLooseW3(t *testing.T) {
	value, err := parseJSONObjectLoose("```json\n{\"a\":1}\n```")
	if err != nil || value["a"] != float64(1) {
		t.Fatalf("围栏剥离失败: %v %v", value, err)
	}
	if _, err := parseJSONObjectLoose("```JSON {\"a\":1}```"); err != nil {
		t.Fatalf("大写围栏剥离失败: %v", err)
	}
	if _, err := parseJSONObjectLoose("   "); err == nil {
		t.Fatalf("空串应报错")
	}
	if _, err := parseJSONObjectLoose("not json"); err == nil {
		t.Fatalf("非法 JSON 应报错")
	}
}

// TestParseSnapshotW3 覆盖快照解析与必填字段回填。
func TestParseSnapshotW3(t *testing.T) {
	if _, err := parseSnapshot("not-a-map"); err == nil {
		t.Fatalf("非对象应报错")
	}
	raw := map[string]any{
		"durableMemory":       []any{"偏好简体中文", "", 42},
		"currentGoal":         "  完成导出  ",
		"importantToolResults": []any{map[string]any{"name": "搜索", "result": "结果"}, map[string]any{"name": "", "result": "无"}, "junk"},
		"imageMemories":       []any{map[string]any{"assetId": "asset-1", "summary": "一只猫", "ocr": []any{"文字"}, "uncertainties": []any{"背景模糊"}}, map[string]any{"assetId": ""}},
		"uncertainties":       []any{"时间未知"},
	}
	snapshot, err := parseSnapshot(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !equalStringsW3(snapshot.DurableMemory, []string{"偏好简体中文"}) {
		t.Fatalf("durableMemory = %v", snapshot.DurableMemory)
	}
	if snapshot.CurrentGoal != "完成导出" {
		t.Fatalf("currentGoal 应 trim: %q", snapshot.CurrentGoal)
	}
	if len(snapshot.ImportantToolResults) != 1 || snapshot.ImportantToolResults[0]["name"] != "搜索" {
		t.Fatalf("importantToolResults = %v", snapshot.ImportantToolResults)
	}
	if len(snapshot.ImageMemories) != 1 || snapshot.ImageMemories[0]["assetId"] != "asset-1" {
		t.Fatalf("imageMemories = %v", snapshot.ImageMemories)
	}
	if !equalStringsW3(snapshot.Uncertainties, []string{"时间未知"}) {
		t.Fatalf("uncertainties = %v", snapshot.Uncertainties)
	}
	// fillRequiredSnapshotFields：模型漏填时从最新用户消息回填。
	filled := fillRequiredSnapshotFields(memorySnapshot{}, emptySnapshot(), []any{
		map[string]any{"role": "assistant", "content": "回答"},
		map[string]any{"role": "user", "content": strings.Repeat("问", 9000)},
	})
	if filled.CurrentGoal == "" || len([]rune(filled.CurrentGoal)) > 8000 {
		t.Fatalf("currentGoal 回填失败: %d", len([]rune(filled.CurrentGoal)))
	}
	// 双空时回填 currentGoal ← recentUserIntent。
	fallback := fillRequiredSnapshotFields(memorySnapshot{RecentUserIntent: "已有意图"}, emptySnapshot(), nil)
	if fallback.CurrentGoal != "已有意图" {
		t.Fatalf("currentGoal 应回退到 intent: %q", fallback.CurrentGoal)
	}
	if got := boundedString("  hello  ", 3); got != "hel" {
		t.Fatalf("boundedString = %q", got)
	}
	if got := stringValue(42); got != "" {
		t.Fatalf("stringValue 非字符串应为空: %q", got)
	}
	if got := stringArray("not-array", 10); len(got) != 0 {
		t.Fatalf("stringArray 非数组应为空: %v", got)
	}
	if got := objectArray([]any{"junk", map[string]any{"a": 1}}, 10); len(got) != 1 {
		t.Fatalf("objectArray 过滤失败: %v", got)
	}
}

// TestInitialSnapshotW3 覆盖从既有 checkpoint 条目恢复快照。
func TestInitialSnapshotW3(t *testing.T) {
	if got := initialSnapshot(nil); len(got.Completed) != 0 {
		t.Fatalf("空条目应为空快照: %+v", got)
	}
	broken := initialSnapshot([]contextEntry{{kind: "durable_memory", contentJSON: "{bad"}})
	if broken.CurrentGoal != "" {
		t.Fatalf("坏 JSON 应回退空快照: %+v", broken)
	}
	restored := initialSnapshot([]contextEntry{
		{kind: "durable_memory", contentJSON: `{"durableMemory":["记忆"],"constraints":["约束"],"decisions":["决定"]}`},
		{kind: "task_state", contentJSON: `{"currentGoal":"目标","completed":["已完成"],"pending":["待办"],"recentUserIntent":"意图","uncertainties":["不确定"]}`},
		{kind: "tool_result", contentJSON: `{"name":"工具"}`},
		{kind: "image_observation", contentJSON: `{"assetId":"a"}`},
		{kind: "unknown", contentJSON: `{}`},
	})
	if !equalStringsW3(restored.DurableMemory, []string{"记忆"}) || restored.CurrentGoal != "目标" || !equalStringsW3(restored.Completed, []string{"已完成"}) {
		t.Fatalf("恢复快照不正确: %+v", restored)
	}
}

// TestTimeHelpersW3 覆盖压缩时间 helper 与时间解析契约。
func TestTimeHelpersW3(t *testing.T) {
	earlier, err := earlierTime("2026-03-10T08:00:00Z", "2026-03-09T08:00:00Z")
	if err != nil || earlier != "2026-03-09T08:00:00.000Z" {
		t.Fatalf("earlierTime = %q err=%v", earlier, err)
	}
	empty, err := earlierTime("", "")
	if err != nil || empty != "" {
		t.Fatalf("双空应为空: %q %v", empty, err)
	}
	if _, err := earlierTime("bad", ""); err == nil {
		t.Fatalf("非法时间应报错")
	}
	shifted, err := shiftInstantISO("2026-03-10T08:00:00Z", -1000)
	if err != nil || shifted != "2026-03-10T07:59:59.000Z" {
		t.Fatalf("shiftInstantISO = %q err=%v", shifted, err)
	}
	if _, err := shiftInstantISO("bad", 1); err == nil {
		t.Fatalf("非法输入应报错")
	}
	if got := safeErrorCode(nil); got != "chat_context_compaction_failed" {
		t.Fatalf("safeErrorCode(nil) = %q", got)
	}
	// 非法字符（含中文与空格）统一替换为下划线。
	if got := safeErrorCode(errors.New("出 错!")); got != "____" {
		t.Fatalf("safeErrorCode 清洗失败: %q", got)
	}
	long := safeErrorCode(errors.New(strings.Repeat("a", 200)))
	if len(long) != 128 {
		t.Fatalf("错误码应截断到 128: %d", len(long))
	}
	if got := errorReason(nil); got != "chat_context_compaction_failed" {
		t.Fatalf("errorReason(nil) = %q", got)
	}

	validCases := []struct {
		name   string
		value  string
		canon  string
	}{
		{"Z 无毫秒", "2026-03-10T08:00:00Z", "2026-03-10T08:00:00.000Z"},
		{"正 offset", "2026-03-10T09:30:00+01:30", "2026-03-10T08:00:00.000Z"},
		{"负 offset", "2026-03-10T06:30:00-01:30", "2026-03-10T08:00:00.000Z"},
		{"带毫秒", "2026-03-10T08:00:00.5Z", "2026-03-10T08:00:00.500Z"},
	}
	for _, testCase := range validCases {
		t.Run("解析/"+testCase.name, func(t *testing.T) {
			canon, ok := canonicalRFC3339(testCase.value)
			if !ok || canon != testCase.canon {
				t.Fatalf("canonicalRFC3339(%s) = %q %v", testCase.value, canon, ok)
			}
		})
	}
	invalid := []string{"2026-03-10T08:00:00", "2026-13-01T00:00:00Z", "2026-02-30T00:00:00Z", "2026-03-10T24:00:00Z", "2026-03-10T08:61:00Z", "2026-03-10T08:00:00+99:00", "not-a-time"}
	for _, value := range invalid {
		if _, ok := canonicalRFC3339(value); ok {
			t.Fatalf("应拒绝 %q", value)
		}
	}
	if millis, ok := rfc3339Millis("2026-03-10T08:00:00Z"); !ok || millis != 1773129600000 {
		t.Fatalf("rfc3339Millis = %d %v", millis, ok)
	}
	if _, ok := rfc3339Millis("bad"); ok {
		t.Fatalf("非法时间不应解析")
	}
	canonical, err := requireRFC3339Instant("2026-03-10T08:00:00Z", "测试")
	if err != nil || canonical != "2026-03-10T08:00:00.000Z" {
		t.Fatalf("requireRFC3339Instant 失败: %q %v", canonical, err)
	}
	if _, err := requireRFC3339Instant("bad", "测试"); err == nil || !strings.Contains(err.Error(), "测试") {
		t.Fatalf("错误应带标签: %v", err)
	}
	if _, err := addDays("bad", 1, "过期时间"); err == nil {
		t.Fatalf("addDays 非法时间应报错")
	}
	if days, err := addDays("2026-03-10T08:00:00Z", 10, "过期时间"); err != nil || days != "2026-03-20T08:00:00.000Z" {
		t.Fatalf("addDays = %q %v", days, err)
	}
	if daysInMonth(2026, 2) != 28 || daysInMonth(2024, 2) != 29 || daysInMonth(2000, 2) != 29 || daysInMonth(1900, 2) != 28 || daysInMonth(2026, 4) != 30 || daysInMonth(2026, 12) != 31 {
		t.Fatalf("daysInMonth 闰年契约不正确")
	}
}

// TestStorePureHelpersW3 覆盖 store 层零散纯函数契约。
func TestStorePureHelpersW3(t *testing.T) {
	if clampInt(1, 2, 10) != 2 || clampInt(20, 2, 10) != 10 || clampInt(5, 2, 10) != 5 {
		t.Fatalf("clampInt 契约不正确")
	}
	if got := uniqueStrings([]string{"a", "a", "", "b"}); !equalStringsW3(got, []string{"a", "b"}) {
		t.Fatalf("uniqueStrings = %v", got)
	}
	if got := placeholders(3); got != "?,?,?" {
		t.Fatalf("placeholders(3) = %q", got)
	}
	if boolToInt(true) != 1 || boolToInt(false) != 0 {
		t.Fatalf("boolToInt 契约不正确")
	}
	if got := nullText(sql.NullString{}); got != nil {
		t.Fatalf("无效 NULL 应返回 nil: %v", got)
	}
	valid := sql.NullString{String: "x", Valid: true}
	if got := nullText(valid); got == nil || *got != "x" {
		t.Fatalf("有效值应返回指针: %v", got)
	}
	emptyString := sql.NullString{String: "", Valid: true}
	if nullText(emptyString) != nil {
		t.Fatalf("空字符串应返回 nil")
	}
	if sqlText(nil) != nil || sqlText(strPtrT("v")) != "v" {
		t.Fatalf("sqlText 契约不正确")
	}
	if isoMillis(fixedChatClockBase()) != "2026-03-10T08:00:00.000Z" {
		t.Fatalf("isoMillis 格式不正确")
	}
	id := chatID("conv")
	if !strings.HasPrefix(id, "chat_conv_") || len(id) != len("chat_conv_")+32 {
		t.Fatalf("chatID 格式不正确: %s", id)
	}
}

// TestReadBoundedAllW3 覆盖有界读取契约。
func TestReadBoundedAllW3(t *testing.T) {
	data, err := readBoundedAll(strings.NewReader("abc"), 5)
	if err != nil || string(data) != "abc" {
		t.Fatalf("正常读取失败: %v %v", string(data), err)
	}
	if _, err := readBoundedAll(strings.NewReader("abcdef"), 5); err == nil {
		t.Fatalf("超限应报错")
	}
	if _, err := readBoundedAll(failingReaderW3{}, 5); err == nil {
		t.Fatalf("读取错误应透传")
	}
}

type failingReaderW3 struct{}

func (failingReaderW3) Read([]byte) (int, error) { return 0, errors.New("读取失败") }

var _ io.Reader = failingReaderW3{}

func fixedChatClockBase() time.Time {
	base, _ := fixedChatClock()
	return base
}
