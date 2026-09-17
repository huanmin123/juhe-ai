package chat

// w13b 波次：纯函数单元测试批量覆盖（stream 请求解析、时间线、生成参数、
// 图片尺寸/存储、分区命名、SSE 收集、responses SSE 解析、写入器与注册中心）。
// 所有输入固定、断言确定性，不依赖数据库或网络。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestW13BParseStreamMessageBodyParameterKeys(t *testing.T) {
	parse := func(t *testing.T, raw string) (*streamMessageBody, error) {
		t.Helper()
		body, err := parseStreamMessageBody(decodeRawObjectW13B(t, raw))
		return body, err
	}
	body, err := parse(t, `{"clientMessageId":"c1","content":"hi","model":"m","generationParameters":{"frequencyPenalty":0.5,"presencePenalty":-1}}`)
	if err != nil || body.GenerationParameters == nil || body.GenerationParameters.FrequencyPenalty == nil {
		t.Fatalf("frequency/presence 解析失败: %+v %v", body, err)
	}
	if _, err := parse(t, `{"clientMessageId":"c1","content":"hi","model":"m","generationParameters":{"maxOutputTokens":1.5}}`); err == nil {
		t.Fatalf("maxOutputTokens 非整数应报错")
	}
	if _, err := parse(t, `{"clientMessageId":"c1","content":"hi","model":"m","generationParameters":{"seed":1.5}}`); err == nil {
		t.Fatalf("seed 非整数应报错")
	}
	if _, err := parse(t, `{"clientMessageId":"c1","content":"hi","model":"m","generationParameters":{"unknown":1}}`); err == nil {
		t.Fatalf("未知生成参数应报错")
	}
	if _, err := parse(t, `{"clientMessageId":"c1","content":"hi","model":"m","generationParameters":{"temperature":"x"}}`); err == nil {
		t.Fatalf("生成参数非数字应报错")
	}
	if _, err := parse(t, `{"clientMessageId":"c1","content":"hi","model":"m","generationParameters":[]}`); err == nil {
		t.Fatalf("generationParameters 非对象应报错")
	}
	if _, err := parse(t, `{"clientMessageId":"c1","content":"hi","model":"m","unknownField":1}`); err == nil {
		t.Fatalf("未知顶层键应报错")
	}
	if _, err := parse(t, `{"clientMessageId":"c1","content":"hi","model":"m","reasoningEffort":"bogus"}`); err == nil {
		t.Fatalf("非法思考级别应报错")
	}
	if _, err := parse(t, `{"clientMessageId":"c1","content":"hi","model":"m","serviceTier":"bogus"}`); err == nil {
		t.Fatalf("非法服务等级应报错")
	}
	if _, err := parse(t, `{"content":"hi","model":"m"}`); err == nil {
		t.Fatalf("缺少 clientMessageId 应报错")
	}
	if _, err := parse(t, `{"clientMessageId":"c1","model":"m"}`); err == nil || requiredMessage("content") != "请输入消息" {
		t.Fatalf("缺少 content 应报错")
	}
	if _, err := parse(t, `{"clientMessageId":"c1","content":"hi"}`); err == nil || requiredMessage("model") != "请选择模型" {
		t.Fatalf("缺少 model 应报错")
	}
	if got := maxMessage("nope"); got != "String too long" {
		t.Fatalf("maxMessage 默认值不正确: %q", got)
	}
	// replaceTurnId 空白被跳过（required=false 分支）。
	body, err = parse(t, `{"clientMessageId":"c1","replaceTurnId":"  ","content":"hi","model":"m"}`)
	if err != nil || body.ReplaceTurnID != "" {
		t.Fatalf("空白 replaceTurnId 应被跳过: %+v %v", body, err)
	}
	// 长度上限。
	if _, err := parse(t, `{"clientMessageId":"`+strings.Repeat("x", 101)+`","content":"hi","model":"m"}`); err == nil {
		t.Fatalf("clientMessageId 超长应报错")
	}
	if _, err := parse(t, `{"clientMessageId":"c1","content":"`+strings.Repeat("x", 196609)+`","model":"m"}`); err == nil {
		t.Fatalf("content 超长应报错")
	}
	if _, err := parse(t, `{"clientMessageId":"c1","content":"hi","model":"`+strings.Repeat("x", 201)+`"}`); err == nil {
		t.Fatalf("model 超长应报错")
	}
	if _, err := parse(t, `{"clientMessageId":123,"content":"hi","model":"m"}`); err == nil {
		t.Fatalf("clientMessageId 非字符串应报错")
	}
}

func decodeRawObjectW13B(t *testing.T, raw string) map[string]json.RawMessage {
	t.Helper()
	parsed, err := decodeObjectBody(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestW13BParseStreamMessageBodyContentBlocks(t *testing.T) {
	_, err := parseStreamMessageBody(map[string]json.RawMessage{
		"clientMessageId": json.RawMessage(`"c1"`), "content": json.RawMessage(`"hi"`), "model": json.RawMessage(`"m"`),
		"contentBlocks": json.RawMessage(`"nope"`),
	})
	if err == nil {
		t.Fatalf("contentBlocks 非数组应报错")
	}
	_, err = parseStreamMessageBody(map[string]json.RawMessage{
		"clientMessageId": json.RawMessage(`"c1"`), "content": json.RawMessage(`"hi"`), "model": json.RawMessage(`"m"`),
		"contentBlocks": json.RawMessage(`[{"type":"input_text"}]`),
	})
	if err == nil {
		t.Fatalf("input_text 缺 text 应报错")
	}
	_, err = parseStreamMessageBody(map[string]json.RawMessage{
		"clientMessageId": json.RawMessage(`"c1"`), "content": json.RawMessage(`"hi"`), "model": json.RawMessage(`"m"`),
		"contentBlocks": json.RawMessage(`[{"type":"input_text","text":42}]`),
	})
	if err == nil {
		t.Fatalf("input_text text 非字符串应报错")
	}
	_, err = parseStreamMessageBody(map[string]json.RawMessage{
		"clientMessageId": json.RawMessage(`"c1"`), "content": json.RawMessage(`"hi"`), "model": json.RawMessage(`"m"`),
		"contentBlocks": json.RawMessage(`[{"type":"input_image"}]`),
	})
	if err == nil {
		t.Fatalf("input_image 缺 assetId 应报错")
	}
	_, err = parseStreamMessageBody(map[string]json.RawMessage{
		"clientMessageId": json.RawMessage(`"c1"`), "content": json.RawMessage(`"hi"`), "model": json.RawMessage(`"m"`),
		"contentBlocks": json.RawMessage(`[{"type":"input_image","assetId":9}]`),
	})
	if err == nil {
		t.Fatalf("assetId 非字符串应报错")
	}
	_, err = parseStreamMessageBody(map[string]json.RawMessage{
		"clientMessageId": json.RawMessage(`"c1"`), "content": json.RawMessage(`"hi"`), "model": json.RawMessage(`"m"`),
		"contentBlocks": json.RawMessage(`[{"type":"input_image","assetId":"  "}]`),
	})
	if err == nil {
		t.Fatalf("assetId 空白应报错")
	}
	_, err = parseStreamMessageBody(map[string]json.RawMessage{
		"clientMessageId": json.RawMessage(`"c1"`), "content": json.RawMessage(`"hi"`), "model": json.RawMessage(`"m"`),
		"contentBlocks": json.RawMessage(`[{"type":"input_image","assetId":"` + strings.Repeat("x", 121) + `"}]`),
	})
	if err == nil {
		t.Fatalf("assetId 超长应报错")
	}
	_, err = parseStreamMessageBody(map[string]json.RawMessage{
		"clientMessageId": json.RawMessage(`"c1"`), "content": json.RawMessage(`"hi"`), "model": json.RawMessage(`"m"`),
		"contentBlocks": json.RawMessage(`[{"type":"bogus"}]`),
	})
	if err == nil {
		t.Fatalf("未知块类型应报错")
	}
	_, err = parseStreamMessageBody(map[string]json.RawMessage{
		"clientMessageId": json.RawMessage(`"c1"`), "content": json.RawMessage(`"hi"`), "model": json.RawMessage(`"m"`),
		"contentBlocks": json.RawMessage(`[{"type":"input_text","text":"a","extra":1}]`),
	})
	if err == nil {
		t.Fatalf("块内未知键应报错")
	}
	_, err = parseStreamMessageBody(map[string]json.RawMessage{
		"clientMessageId": json.RawMessage(`"c1"`), "content": json.RawMessage(`"hi"`), "model": json.RawMessage(`"m"`),
		"contentBlocks": json.RawMessage(`[{"type":"input_image","assetId":"a"},{"type":"input_image","assetId":"a"}]`),
	})
	if err == nil {
		t.Fatalf("重复图片应报错")
	}
	blocks := make([]string, 12)
	for i := range blocks {
		blocks[i] = `{"type":"input_text","text":"t"}`
	}
	_, err = parseStreamMessageBody(map[string]json.RawMessage{
		"clientMessageId": json.RawMessage(`"c1"`), "content": json.RawMessage(`"hi"`), "model": json.RawMessage(`"m"`),
		"contentBlocks": json.RawMessage("[" + strings.Join(blocks, ",") + "]"),
	})
	if err == nil {
		t.Fatalf("超过 11 个块应报错")
	}
	body, err := parseStreamMessageBody(map[string]json.RawMessage{
		"clientMessageId": json.RawMessage(`"c1"`), "content": json.RawMessage(`"hi"`), "model": json.RawMessage(`"m"`),
		"contentBlocks": json.RawMessage(`[{"type":"input_text","text":"a"},{"type":"input_image","assetId":"chat_asset_ok"}]`),
	})
	if err != nil || len(body.ContentBlocks) != 2 {
		t.Fatalf("合法块解析失败: %+v %v", body, err)
	}
}

func TestW13BStringHelpers(t *testing.T) {
	if truncateUTF8("abcdef", 0) != "abcdef" || truncateUTF8("", -1) != "" {
		t.Fatalf("truncateUTF8 边界失败")
	}
	if got := truncateUTF8("a\u4e2d\u6587b", 4); got != "a\u4e2d" {
		t.Fatalf("truncateUTF8 截断 rune 失败: %q", got)
	}
	if got := truncateUTF8("a\u4e2d", 3); got != "a" {
		t.Fatalf("truncateUTF8 剔除半个 rune 失败: %q", got)
	}
	if got := truncateUTF8("\xff\xfe\xfd", 2); got != "" {
		t.Fatalf("全无效字节应清空: %q", got)
	}
	if validBucketDate("2026-03-10") != true || validBucketDate("2026/03/10") || validBucketDate("20260310") || validBucketDate("2026-3a-10") {
		t.Fatalf("validBucketDate 分支失败")
	}
	if maxI64(3, 2) != 3 || maxI64(1, 5) != 5 {
		t.Fatalf("maxI64 失败")
	}
	if maxInt(3, 2) != 3 || maxInt(1, 5) != 5 {
		t.Fatalf("maxInt 失败")
	}
	if valueOrEmpty(nil) != "" || valueOrEmpty(stringPtr("x")) != "x" {
		t.Fatalf("valueOrEmpty 失败")
	}
	if !validProvenance("tool") || validProvenance("other") {
		t.Fatalf("validProvenance 失败")
	}
	if !validTrustLevel("provider_opaque") || validTrustLevel("other") {
		t.Fatalf("validTrustLevel 失败")
	}
	if stringsLower("AbC") != "abc" {
		t.Fatalf("stringsLower 失败")
	}
	if derefAssistantI64(nil) != 0 || derefAssistantI64(int64PtrT(7)) != 7 {
		t.Fatalf("derefAssistantI64 失败")
	}
	if mustJSON(map[string]any{"a": float64(1)}) != `{"a":1}` {
		t.Fatalf("mustJSON 失败")
	}
	if !containsAny([]string{"x", "responses"}, []string{"responses"}) || containsAny([]string{"x"}, []string{"y"}) {
		t.Fatalf("containsAny 失败")
	}
	if normalizeProviderToken("  OpenAI ") != "openai" || normalizeProviderToken("  ") != "" {
		t.Fatalf("normalizeProviderToken 失败")
	}
	payload := generationParametersPayload([]ChatGenerationParameterCapability{{Parameter: "temperature", Min: 0, Max: 2, DefaultValue: 1}})
	if len(payload) != 1 {
		t.Fatalf("generationParametersPayload 失败")
	}
	if sqlNullText(sql.NullString{}) != nil || sqlNullText(sql.NullString{String: "", Valid: true}) != nil || sqlNullText(sql.NullString{String: "x", Valid: true}) != "x" {
		t.Fatalf("sqlNullText 失败")
	}
	if _, err := normalizedContextState("bogus"); err == nil {
		t.Fatalf("normalizedContextState 非法值应报错")
	}
	if statusOrZero(nil) != 0 || statusOrZero(&GenerationDispatchResponse{Status: 502}) != 502 {
		t.Fatalf("statusOrZero 失败")
	}
	if finishReasonOr(nil, "stop") != "stop" || finishReasonOr([]ChatToolCall{{}}, "stop") != "tool_calls" {
		t.Fatalf("finishReasonOr 失败")
	}
	if !isApplicationFunctionToolEvent(map[string]any{"type": "function_call"}) ||
		isApplicationFunctionToolEvent(map[string]any{"type": "message"}) {
		t.Fatalf("isApplicationFunctionToolEvent 失败")
	}
	if nilIfZero(0) != nil || *nilIfZero(5) != 5 {
		t.Fatalf("nilIfZero 失败")
	}
	if normalizedErrorCode("") != "chat_asset_delete_failed" {
		t.Fatalf("normalizedErrorCode 默认值失败")
	}
	if got := normalizedErrorCode(strings.Repeat("x", 200)); len([]rune(got)) != 128 {
		t.Fatalf("normalizedErrorCode 截断失败: %d", len(got))
	}
}

func TestW13BNumericAndCloneHelpers(t *testing.T) {
	if got, ok := numericValue(float32(1.5)); !ok || got != 1.5 {
		t.Fatalf("numericValue float32 失败")
	}
	if got, ok := numericValue(int(3)); !ok || got != 3 {
		t.Fatalf("numericValue int 失败")
	}
	if got, ok := numericValue(int64(4)); !ok || got != 4 {
		t.Fatalf("numericValue int64 失败")
	}
	if got, ok := numericValue(json.Number("2.5")); !ok || got != 2.5 {
		t.Fatalf("numericValue json.Number 失败")
	}
	if _, ok := numericValue(json.Number("zz")); ok {
		t.Fatalf("numericValue 非法 json.Number 应失败")
	}
	if _, ok := numericValue("str"); ok {
		t.Fatalf("numericValue 字符串应失败")
	}
	if stringItem(nil, "k") != "" || stringItem(map[string]any{"k": "  "}, "k") != "" {
		t.Fatalf("stringItem 失败")
	}
	if positiveIntegerItem(map[string]any{"w": -1}, "w") != nil || positiveIntegerItem(map[string]any{"w": 1.5}, "w") != nil ||
		positiveIntegerItem(map[string]any{"w": 18014398509481984.0}, "w") != nil {
		t.Fatalf("positiveIntegerItem 边界失败")
	}
	if cloneJSONMap(nil) != nil {
		t.Fatalf("cloneJSONMap nil 应返回 nil")
	}
	if cloneJSONMap(map[string]any{"bad": make(chan int)}) != nil {
		t.Fatalf("cloneJSONMap marshal 失败应返回 nil")
	}
	if jsonBytesOf(make(chan int)) != math.MaxInt32 {
		t.Fatalf("jsonBytesOf marshal 失败应返回 MaxInt32")
	}
	if !jsonMapEqual(nil, nil) || jsonMapEqual(nil, map[string]any{"a": 1}) {
		t.Fatalf("jsonMapEqual 失败")
	}
	small := &ChatGenerationToolEvent{ID: "x", ToolType: "y", Status: asstStarted, Item: map[string]any{"k": "v"}}
	if sanitizeToolEvent(small).Item == nil {
		t.Fatalf("sanitizeToolEvent 小载荷应保留 item")
	}
	big := &ChatGenerationToolEvent{ID: "x", ToolType: "y", Status: asstStarted, Item: map[string]any{"k": strings.Repeat("v", chatGenerationToolJSONMaxBytes+1)}}
	sanitized := sanitizeToolEvent(big)
	if sanitized.Item != nil {
		t.Fatalf("sanitizeToolEvent 超大载荷应丢弃 item")
	}
	imageSanitized := sanitizeImageEvent(&ChatGenerationImageEvent{ID: "i", Status: "completed", Item: map[string]any{"assetId": "a", "b64_json": "zz", "result": "yy"}})
	if imageSanitized.Item["b64_json"] != nil || imageSanitized.Item["result"] != nil || imageSanitized.Item["assetId"] != "a" {
		t.Fatalf("sanitizeImageEvent 脱敏失败: %v", imageSanitized.Item)
	}
	if raw := jsonRawBlock(&assistantBlock{Type: "output_text", Text: "hi"}); raw["text"] != "hi" {
		t.Fatalf("jsonRawBlock 失败")
	}
	if jsonRawMap(nil) != nil {
		t.Fatalf("jsonRawMap nil 应返回 nil")
	}
}

func TestW13BAssistantTimeline(t *testing.T) {
	tl := newAssistantTimeline()
	if tl.AppendReasoning("") != nil {
		t.Fatalf("空 reasoning 应返回 nil")
	}
	first := tl.AppendReasoning("想")
	if first == nil || first.BlockID != "assistant_block_1" {
		t.Fatalf("首块 reasoning 失败: %+v", first)
	}
	merged := tl.AppendReasoning("法")
	if merged == nil || merged.Text != "想法" || merged.BlockID != "assistant_block_1" {
		t.Fatalf("合并 reasoning 失败: %+v", merged)
	}
	if _, err := tl.StartTool("", "t", nil); err == nil {
		t.Fatalf("空 callId 应报错")
	}
	if _, err := tl.StartTool("c1", " ", nil); err == nil {
		t.Fatalf("空 toolType 应报错")
	}
	block, err := tl.StartTool("c1", "web_search", map[string]any{"q": 1})
	if err != nil || block.Item["q"] != float64(1) {
		t.Fatalf("StartTool 失败: %v", err)
	}
	dup, err := tl.StartTool("c1", "web_search", map[string]any{"q": 2})
	if err != nil || dup.Item == nil {
		t.Fatalf("重复 StartTool 应返回既有块: %+v", dup)
	}
	if _, err := tl.StartTool("c1", "other", nil); err == nil {
		t.Fatalf("toolType 不一致应报错")
	}
	if _, err := tl.UpdateTool("missing", asstCompleted, nil); err == nil {
		t.Fatalf("未知工具更新应报错")
	}
	if _, err := tl.CompleteBlock("missing"); err == nil {
		t.Fatalf("未知块完成应报错")
	}
	completed, err := tl.CompleteBlock("assistant_block_1")
	if err != nil || completed.Status != asstCompleted {
		t.Fatalf("完成 reasoning 失败: %+v %v", completed, err)
	}
	toolDone, err := tl.CompleteBlock("assistant_block_2")
	if err != nil || toolDone.Status != asstCompleted {
		t.Fatalf("完成 tool 失败")
	}
	// 更新已终结块：幂等返回。
	if updated, err := tl.UpdateTool("c1", asstFailed, nil); err != nil || updated.Status != asstCompleted {
		t.Fatalf("终态工具更新应幂等: %+v %v", updated, err)
	}
	// 图片块。
	if tl.StartImage(StartImageInput{AssetID: " "}) != nil {
		t.Fatalf("空 assetId 应返回 nil")
	}
	img := tl.StartImage(StartImageInput{AssetID: "a1", MimeType: "image/webp", Width: int64PtrT(16), Height: int64PtrT(16), RevisedPrompt: "p"})
	if img == nil || img.MimeType != "image/webp" {
		t.Fatalf("StartImage 失败")
	}
	if again := tl.StartImage(StartImageInput{AssetID: "a1"}); again.BlockID != img.BlockID {
		t.Fatalf("重复 StartImage 应幂等")
	}
	if updated := tl.UpdateImage(StartImageInput{AssetID: "a1", RevisedPrompt: "p2"}, asstCompleted); updated.Status != asstCompleted {
		t.Fatalf("UpdateImage 完成失败")
	}
	if frozen := tl.UpdateImage(StartImageInput{AssetID: "a1"}, asstFailed); frozen.Status != asstCompleted {
		t.Fatalf("终态图片更新应幂等")
	}
	if tl.UpdateImage(StartImageInput{AssetID: "  "}, asstCompleted) != nil {
		t.Fatalf("空 assetId 应返回 nil")
	}
	created := tl.UpdateImage(StartImageInput{AssetID: "a2", MimeType: "image/png"}, asstStarted)
	if created == nil || created.MimeType != "image/png" {
		t.Fatalf("UpdateImage 创建 started 块失败")
	}
	// 终态后所有追加都应被拒绝。
	tl.Finalize(asstCompleted)
	if tl.AppendReasoning("x") != nil || tl.UpdateImage(StartImageInput{AssetID: "a3"}, asstStarted) != nil {
		t.Fatalf("终态时间线应拒绝更新")
	}
	if _, err := tl.StartTool("c9", "t", nil); err == nil {
		t.Fatalf("终态时间线 StartTool 应报错")
	}
	if _, err := tl.UpdateTool("c1", asstFailed, nil); err == nil {
		t.Fatalf("终态时间线 UpdateTool 应报错")
	}
	if _, err := tl.CompleteBlock("assistant_block_1"); err == nil {
		t.Fatalf("终态时间线 CompleteBlock 应报错")
	}
	snapshot := tl.Snapshot()
	if snapshot.Status != asstCompleted || snapshot.ContentText != "" || len(snapshot.ContentBlocks) != 4 {
		t.Fatalf("Snapshot 失败: %+v", snapshot)
	}
	if snapshot.ContentBlocks[0].Text != "想法" || snapshot.ContentBlocks[0].Status != asstCompleted {
		t.Fatalf("reasoning 终态快照失败: %+v", snapshot.ContentBlocks[0])
	}
}

func TestW13BRunnerProjectionAndEvents(t *testing.T) {
	nowCount := 0
	runner := NewChatGenerationRunner(ChatGenerationRunnerOptions{
		Identity: ChatGenerationIdentity{OwnerID: "o", ConversationID: "c", TurnID: "t", AssistantMessageID: "m"},
		Now:      func() string { nowCount++; return "2026-03-10T08:00:00.000Z" },
	}, context.Background(), func() {}, func() bool { return false })
	if runner.now == nil || runner.now() == "" {
		t.Fatalf("runner now 注入失败")
	}
	runner.currentState = "running"
	subscriber := &captureSubscriberW13B{}
	if !runner.Subscribe(subscriber) {
		t.Fatalf("首次订阅应成功")
	}
	if !runner.Subscribe(subscriber) {
		t.Fatalf("重复订阅应幂等成功")
	}
	if len(subscriber.events) != 1 || subscriber.events[0].Type != "message.snapshot" {
		t.Fatalf("订阅快照事件缺失: %+v", subscriber.events)
	}
	if !runner.Publish("message.delta", map[string]any{"delta": "hi"}, ChatGenerationProjectionUpdate{ContentTextDelta: stringPtr("hi")}) {
		t.Fatalf("发布文本增量失败")
	}
	if !runner.Publish("reasoning.delta", map[string]any{}, ChatGenerationProjectionUpdate{ReasoningTextDelta: stringPtr("想")}) {
		t.Fatalf("发布 reasoning 增量失败")
	}
	runner.Publish("noop", map[string]any{}, ChatGenerationProjectionUpdate{})
	runner.Publish("noop2", map[string]any{}, ChatGenerationProjectionUpdate{ReasoningCompleted: true})
	runner.Publish("tool.started", map[string]any{}, ChatGenerationProjectionUpdate{ToolEvent: &ChatGenerationToolEvent{ID: "c1", ToolType: "web_search", Status: asstStarted, Item: map[string]any{"q": 1}}})
	runner.Publish("tool.updated", map[string]any{}, ChatGenerationProjectionUpdate{ToolEvent: &ChatGenerationToolEvent{ID: "c1", ToolType: "web_search", Status: asstUpdated}})
	runner.Publish("tool.completed", map[string]any{}, ChatGenerationProjectionUpdate{ToolEvent: &ChatGenerationToolEvent{ID: "c1", ToolType: "web_search", Status: asstCompleted}})
	// 未知工具更新（no-op 分支）。
	runner.Publish("tool.completed", map[string]any{}, ChatGenerationProjectionUpdate{ToolEvent: &ChatGenerationToolEvent{ID: "ghost", ToolType: "web_search", Status: asstCompleted}})
	// toolType 不一致（no-op 分支）。
	runner.Publish("tool.started", map[string]any{}, ChatGenerationProjectionUpdate{ToolEvent: &ChatGenerationToolEvent{ID: "c1", ToolType: "other", Status: asstStarted}})
	// started 但已有 item（no-op 分支）。
	runner.Publish("tool.started", map[string]any{}, ChatGenerationProjectionUpdate{ToolEvent: &ChatGenerationToolEvent{ID: "c1", ToolType: "web_search", Status: asstStarted, Item: map[string]any{"q": 9}}})
	// 图片事件：started -> completed（含字段 patch）。
	runner.Publish("image.started", map[string]any{}, ChatGenerationProjectionUpdate{ImageEvent: &ChatGenerationImageEvent{ID: "i1", Status: asstStarted, Item: map[string]any{"assetId": "a1"}}})
	runner.Publish("image.completed", map[string]any{}, ChatGenerationProjectionUpdate{ImageEvent: &ChatGenerationImageEvent{ID: "i1", Status: asstCompleted, Item: map[string]any{"assetId": "a1", "mimeType": "image/webp", "width": 32, "height": 32, "revisedPrompt": "p"}}})
	// 已存在块重复 started（no-op 分支）。
	runner.Publish("image.started", map[string]any{}, ChatGenerationProjectionUpdate{ImageEvent: &ChatGenerationImageEvent{ID: "i1", Status: asstStarted, Item: map[string]any{"assetId": "a1"}}})
	// 未知图片 + failed（no-op 分支）。
	runner.Publish("image.failed", map[string]any{}, ChatGenerationProjectionUpdate{ImageEvent: &ChatGenerationImageEvent{ID: "i9", Status: asstFailed, Item: map[string]any{"assetId": "zz"}}})
	// 无 assetId（no-op 分支）。
	runner.Publish("image.failed", map[string]any{}, ChatGenerationProjectionUpdate{ImageEvent: &ChatGenerationImageEvent{ID: "i9", Status: asstFailed, Item: map[string]any{"other": 1}}})
	snapshot := runner.snapshotAssistantLocked()
	if snapshot["contentText"] != "hi" || snapshot["reasoningText"] != "想" {
		t.Fatalf("assistant 快照失败: %v", snapshot)
	}
	if len(runner.SnapshotContentBlocks()) == 0 {
		t.Fatalf("快照内容块缺失")
	}
	// 终态时间线会为非文本块补发 completed/updated 事件。
	runner.finalizeTimelineLocked(asstFailed)
	if runner.State() != "running" {
		t.Fatalf("finalizeTimeline 不应改变状态")
	}
	runner.Unsubscribe(subscriber)
	if runner.Unsubscribe(subscriber) {
		t.Fatalf("重复退订应返回 false")
	}
	status := runner.StatusSnapshot()
	if status.State != "running" {
		t.Fatalf("状态快照失败: %+v", status)
	}
}

type captureSubscriberW13B struct {
	events []ChatGenerationEvent
	panics bool
}

func (c *captureSubscriberW13B) TrySend(event ChatGenerationEvent) bool {
	if c.panics {
		panic("subscriber boom")
	}
	c.events = append(c.events, event)
	return true
}

func TestW13BRunnerRunLifecycle(t *testing.T) {
	t.Run("正常完成", func(t *testing.T) {
		runner := NewChatGenerationRunner(ChatGenerationRunnerOptions{
			Execute: func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
				ctx.Publish("message.delta", map[string]any{}, ChatGenerationProjectionUpdate{ContentTextDelta: stringPtr("ok")})
				return ChatGenerationTerminalResult{Status: "completed", Data: map[string]any{"messageId": "m"}}, nil
			},
		}, context.Background(), func() {}, func() bool { return false })
		if !runner.Start(nil) {
			t.Fatalf("启动失败")
		}
		<-runner.Completion()
		if runner.State() != "completed" || !runner.AuthoritativeTerminal() {
			t.Fatalf("完成状态不正确: %s", runner.State())
		}
		if runner.Start(nil) {
			t.Fatalf("重复启动应返回 false")
		}
	})
	t.Run("执行错误且终态已置位", func(t *testing.T) {
		var runner *ChatGenerationRunner
		runner = NewChatGenerationRunner(ChatGenerationRunnerOptions{
			Execute: func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
				// 在执行闭包里把状态置为终态，模拟并发终止。
				runner.currentState = asstCanceled
				return ChatGenerationTerminalResult{Status: "completed"}, errors.New("late")
			},
		}, context.Background(), func() {}, func() bool { return false })
		runner.Start(nil)
		<-runner.Completion()
		if runner.currentState != asstCanceled {
			t.Fatalf("终态应保持: %s", runner.currentState)
		}
	})
	t.Run("意外错误且终化成功", func(t *testing.T) {
		runner := NewChatGenerationRunner(ChatGenerationRunnerOptions{
			Execute: func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
				return ChatGenerationTerminalResult{}, errors.New("boom")
			},
			OnUnexpectedError:      func(publicError PublicChatGenerationError) error { return nil },
			UnexpectedErrorTraceID: "trace-1",
		}, context.Background(), func() {}, func() bool { return false })
		runner.Start(nil)
		<-runner.Completion()
		if runner.State() != asstFailed || !runner.AuthoritativeTerminal() {
			t.Fatalf("失败状态不正确: %s", runner.State())
		}
	})
	t.Run("意外错误且终化失败", func(t *testing.T) {
		runner := NewChatGenerationRunner(ChatGenerationRunnerOptions{
			Execute: func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
				return ChatGenerationTerminalResult{}, errors.New("boom")
			},
			OnUnexpectedError: func(publicError PublicChatGenerationError) error { return errors.New("finalize failed") },
		}, context.Background(), func() {}, func() bool { return false })
		runner.Start(nil)
		<-runner.Completion()
		if runner.State() != asstFailed || runner.AuthoritativeTerminal() {
			t.Fatalf("非权威失败状态不正确: %s authoritative=%v", runner.State(), runner.AuthoritativeTerminal())
		}
	})
	t.Run("订阅者 panic 被剔除", func(t *testing.T) {
		runner := NewChatGenerationRunner(ChatGenerationRunnerOptions{}, context.Background(), func() {}, func() bool { return false })
		runner.currentState = "running"
		bad := &captureSubscriberW13B{panics: true}
		if runner.Subscribe(bad) {
			t.Fatalf("panic 订阅者应被拒绝")
		}
		good := &captureSubscriberW13B{}
		if !runner.Subscribe(good) {
			t.Fatalf("第二订阅应成功")
		}
		if !runner.Publish("message.delta", nil, ChatGenerationProjectionUpdate{ContentTextDelta: stringPtr("x")}) {
			t.Fatalf("发布应成功")
		}
		if len(good.events) == 0 {
			t.Fatalf("健康订阅者应收到事件")
		}
	})
	t.Run("Abort 与默认时钟", func(t *testing.T) {
		runner := NewChatGenerationRunner(ChatGenerationRunnerOptions{}, context.Background(), func() {}, func() bool { return true })
		if runner.Abort() {
			t.Fatalf("已取消上下文 Abort 应返回 false")
		}
		defaults := NewChatGenerationRunner(ChatGenerationRunnerOptions{}, context.Background(), func() {}, func() bool { return false })
		if defaults.now == nil || defaults.now() == "" {
			t.Fatalf("默认时钟应生效")
		}
	})
}

func TestW13BGenerationHubRegistry(t *testing.T) {
	hub := NewGenerationHub(nil)
	if hub.now == nil {
		t.Fatalf("默认时钟应注入")
	}
	noopRunner := func() *ChatGenerationRunner {
		return NewChatGenerationRunner(ChatGenerationRunnerOptions{
			Identity:  ChatGenerationIdentity{OwnerID: "o", ConversationID: "c1", TurnID: "t"},
			Execute:   func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) { return ChatGenerationTerminalResult{Status: "completed"}, nil },
		}, context.Background(), func() {}, func() bool { return false })
	}
	first := noopRunner()
	if !hub.Register(first) {
		t.Fatalf("注册失败")
	}
	if hub.Register(noopRunner()) {
		t.Fatalf("会话槽位应互斥")
	}
	if !hub.Subscribe(GenerationIdentity{OwnerID: "o", ConversationID: "c1", TurnID: "t"}, &captureSubscriberW13B{}) {
		t.Fatalf("订阅活动 runner 应成功")
	}
	if hub.Subscribe(GenerationIdentity{OwnerID: "o", ConversationID: "missing", TurnID: "t"}, &captureSubscriberW13B{}) {
		t.Fatalf("订阅缺失 runner 应失败")
	}
	hub.Unsubscribe(GenerationIdentity{OwnerID: "o", ConversationID: "missing", TurnID: "t"}, &captureSubscriberW13B{})
	if hub.Stop(GenerationIdentity{OwnerID: "o", ConversationID: "missing", TurnID: "t"}) {
		t.Fatalf("停止缺失 runner 应失败")
	}
	if _, ok := hub.GetRunner(GenerationIdentity{OwnerID: "o", ConversationID: "c1", TurnID: "t"}); !ok {
		t.Fatalf("应能取回活动 runner")
	}
	hub.Launch(first)
	<-first.Completion()
	// 终端快照记忆 + 注册清除旧快照。
	snap := hub.Snapshot("o", "c1", "t")
	if snap.State != "terminal" {
		t.Fatalf("终端快照应被记忆: %+v", snap)
	}
	hub.Shutdown(10 * time.Millisecond)
	if !hub.shuttingDownState() {
		t.Fatalf("Shutdown 应置位")
	}
	if hub.Register(noopRunner()) {
		t.Fatalf("停机后注册应失败")
	}
	if hub.Subscribe(GenerationIdentity{OwnerID: "o", ConversationID: "c1", TurnID: "t"}, &captureSubscriberW13B{}) {
		t.Fatalf("停机后订阅应失败")
	}
	// 覆盖 removeTerminalKeyLocked 的删除分支：再注册同键 runner。
	hub2 := NewGenerationHub(func() string { return "2026-03-10T08:00:00.000Z" })
	done := noopRunner()
	done.Identity.ConversationID = "c2"
	hub2.Start(done)
	<-done.Completion()
	again := noopRunner()
	again.Identity.ConversationID = "c2"
	if !hub2.Register(again) {
		t.Fatalf("重注册应清除终端快照并成功")
	}
	if len(hub2.terminalSnapshots) != 0 || len(hub2.terminalSnapshotKeys) != 0 {
		t.Fatalf("重注册后终端快照应被清除: %+v", hub2.terminalSnapshots)
	}
	_ = hub2.Snapshot("o", "c2", "t")
	// 带 runner 的 Shutdown：等待完成或超时。
	hub3 := NewGenerationHub(func() string { return "2026-03-10T08:00:00.000Z" })
	blocking := NewChatGenerationRunner(ChatGenerationRunnerOptions{
		Identity: ChatGenerationIdentity{OwnerID: "o", ConversationID: "c3", TurnID: "t"},
		Execute: func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
			<-ctx.Context.Done()
			return ChatGenerationTerminalResult{}, ctx.Context.Err()
		},
	}, context.Background(), func() {}, func() bool { return false })
	hub3.Start(blocking)
	hub3.Shutdown(500 * time.Millisecond)
}

func TestW13BChatSSEWriterFailures(t *testing.T) {
	writer := newChatSSEWriter(&failingResponseWriterW13B{}, nil, nil)
	if writer.WriteEvent("event", map[string]any{"a": 1}) {
		t.Fatalf("失败 writer 应返回 false")
	}
	if writer.WriteEvent("event", map[string]any{"bad": make(chan int)}) {
		t.Fatalf("marshal 失败应返回 false")
	}
	if writer.WriteComment(": heartbeat\n\n") {
		t.Fatalf("失败 writer 心跳应返回 false")
	}
	if writer.Writable() {
		t.Fatalf("writeFailed 后不应可写")
	}
	writer.End()
	writer.End()

	closed := newChatSSEWriter(&failingResponseWriterW13B{}, nil, nil)
	closed.closed = true
	if closed.WriteEvent("event", nil) || closed.WriteComment("x") {
		t.Fatalf("已关闭 writer 应拒绝写入")
	}
	ended := newChatSSEWriter(&failingResponseWriterW13B{}, nil, nil)
	ended.ended = true
	if ended.WriteEvent("event", nil) || ended.WriteComment("x") {
		t.Fatalf("已结束 writer 应拒绝写入")
	}
	// 成功写入 + End。
	live := newChatSSEWriter(httptest.NewRecorder(), nil, nil)
	if !live.WriteEvent("event", map[string]any{"a": 1}) || !live.WriteComment(": hb\n\n") {
		t.Fatalf("正常写入失败")
	}
	live.End()
	// 请求上下文取消触发 onClose。
	ctx, cancel := context.WithCancel(context.Background())
	onClose := make(chan struct{})
	contextWriter := newChatSSEWriter(httptest.NewRecorder(), ctx, func() { close(onClose) })
	cancel()
	select {
	case <-onClose:
	case <-time.After(time.Second):
		t.Fatalf("取消后应触发 onClose")
	}
	if contextWriter.Writable() {
		t.Fatalf("取消后不应可写")
	}
}

type failingResponseWriterW13B struct{}

func (f *failingResponseWriterW13B) Header() http.Header { return http.Header{} }
func (f *failingResponseWriterW13B) Write([]byte) (int, error)   { return 0, errors.New("broken pipe") }
func (f *failingResponseWriterW13B) WriteHeader(int)             {}

func TestW13BSSEHeartbeatOnUnwritable(t *testing.T) {
	writer := newChatSSEWriter(&failingResponseWriterW13B{}, nil, nil)
	stopped := make(chan struct{})
	var once sync.Once
	stop := startChatSSEHeartbeat(writer, 10, func() {
		once.Do(func() { close(stopped) })
	})
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatalf("不可写心跳应触发回调")
	}
	stop()
	stop()
}

func TestW13BSSESubscriberDetach(t *testing.T) {
	writer := newChatSSEWriter(httptest.NewRecorder(), nil, nil)
	detachCount := 0
	subscriber := &sseSubscriber{writer: writer, detach: func() { detachCount++ }}
	if !subscriber.TrySend(ChatGenerationEvent{Type: "message.delta", EventVersion: 1, Data: map[string]any{"a": 1}}) {
		t.Fatalf("健康发送应成功")
	}
	if detachCount != 0 {
		t.Fatalf("非终态事件不应触发 detach")
	}
	if !subscriber.TrySend(ChatGenerationEvent{Type: "message.completed", EventVersion: 2, Data: map[string]any{}}) {
		t.Fatalf("终态事件应返回 true 并触发 detach")
	}
	if detachCount != 1 {
		t.Fatalf("终态事件应触发一次 detach: %d", detachCount)
	}
	if subscriber.TrySend(ChatGenerationEvent{Type: "message.delta", EventVersion: 3, Data: map[string]any{}}) {
		t.Fatalf("已 detach 后应返回 false")
	}
	if detachCount != 1 {
		t.Fatalf("detachOnce 应幂等: %d", detachCount)
	}
	// 写失败触发 detach。
	failWriter := newChatSSEWriter(&failingResponseWriterW13B{}, nil, nil)
	failSubscriber := &sseSubscriber{writer: failWriter}
	if failSubscriber.TrySend(ChatGenerationEvent{Type: "message.delta", EventVersion: 1}) {
		t.Fatalf("写失败应返回 false")
	}
}

func TestW13BPostgresPartitionNaming(t *testing.T) {
	if key, ok := chatMessagePartitionDateKeyFromISO("2026-03-10T08:00:00Z"); !ok || key != "20260310" {
		t.Fatalf("分区日期键失败: %q %v", key, ok)
	}
	for _, bad := range []string{"", "2026-3-10", "2026-13-10", "2026-02-30", "20260310xx", "abcd-ef-gh"} {
		if _, ok := chatMessagePartitionDateKeyFromISO(bad); ok {
			t.Fatalf("非法日期应失败: %q", bad)
		}
	}
	if name, err := postgresChatMessagePartitionName("20260310"); err != nil || name != "chat_messages_20260310" {
		t.Fatalf("分区名失败: %q %v", name, err)
	}
	if _, err := postgresChatMessagePartitionName("20261310"); err == nil {
		t.Fatalf("非法分区名应报错")
	}
	start, end, err := chatMessagePartitionBounds("20260310")
	if err != nil || start != "2026-03-10" || end != "2026-03-11" {
		t.Fatalf("分区边界失败: %q %q %v", start, end, err)
	}
	if _, _, err := chatMessagePartitionBounds("bogus1"); err == nil {
		t.Fatalf("非法边界应报错")
	}
	if _, ok := normalizeChatPartitionDateKey("2026031"); ok {
		t.Fatalf("长度不足应失败")
	}
	if _, ok := normalizeChatPartitionDateKey("20260310x"); ok {
		t.Fatalf("非数字应失败")
	}
}

func TestW13BGenerationParameterTables(t *testing.T) {
	tables := generationParameterCapabilitiesForModel("GPT", "gpt-4.1", nil)
	if len(tables["chat_completions"]) != 6 {
		t.Fatalf("gpt 非 gpt-5 参数表失败")
	}
	if tables := generationParameterCapabilitiesForModel("gpt", "GPT-5", int64PtrT(1000)); len(tables["responses"]) != 1 {
		t.Fatalf("gpt-5 responses 参数表失败")
	}
	if tables := generationParameterCapabilitiesForModel("xai", "grok-reasoning", nil); len(tables["chat_completions"]) != 4 {
		t.Fatalf("xai reasoning 参数表失败")
	}
	if tables := generationParameterCapabilitiesForModel("deepseek", "deepseek-chat", nil); len(tables["chat_completions"]) != 3 {
		t.Fatalf("deepseek-chat 参数表失败")
	}
	if tables := generationParameterCapabilitiesForModel("anthropic", "claude-sonnet-4.8", nil); len(tables["chat_completions"]) != 1 {
		t.Fatalf("anthropic 拒绝采样参数表失败")
	}
	if tables := generationParameterCapabilitiesForModel("gemini", "google.gemini-3.pro", nil); len(tables["responses"]) != 1 {
		t.Fatalf("gemini-3 参数表失败")
	}
	glm := generationParameterCapabilitiesForModel("glm", "glm-5", nil)["chat_completions"]
	if len(glm) != 3 {
		t.Fatalf("glm 参数表失败")
	}
	if len(generationParameterCapabilitiesForModel("unknown-provider", "x", nil)) != 0 {
		t.Fatalf("未知供应商应为空表")
	}
	limited := limitGenerationParameterMaxOutputTokens(map[string][]ChatGenerationParameterCapability{
		"chat_completions": {
			{Parameter: "maxOutputTokens", Min: 1, Max: 100, DefaultValue: 100},
			{Parameter: "temperature", Min: 0, Max: 2, DefaultValue: 1},
		},
	}, int64PtrT(10))
	entries := limited["chat_completions"]
	for _, entry := range entries {
		if entry.Parameter == "maxOutputTokens" && (entry.Max != 10 || entry.DefaultValue != 10) {
			t.Fatalf("maxOutputTokens 钳制失败: %+v", entry)
		}
	}
	dropped := limitGenerationParameterMaxOutputTokens(map[string][]ChatGenerationParameterCapability{
		"chat_completions": {{Parameter: "maxOutputTokens", Min: 5000, Max: 8000, DefaultValue: 6000}},
	}, int64PtrT(10))
	if len(dropped["chat_completions"]) != 0 {
		t.Fatalf("低于下限的 maxOutputTokens 应被剔除")
	}
	if clampCapabilityDefault(1, 2, 3) != 2 || clampCapabilityDefault(9, 2, 3) != 3 || clampCapabilityDefault(2.5, 2, 3) != 2.5 {
		t.Fatalf("clampCapabilityDefault 失败")
	}
	if got := intersectGenerationParameterCapabilityLists(nil); len(got) != 0 {
		t.Fatalf("空列表交集应为空")
	}
	emptyFirst := intersectGenerationParameterCapabilityLists([][]ChatGenerationParameterCapability{{}})
	if len(emptyFirst) != 0 {
		t.Fatalf("空首列表交集应为空")
	}
	missing := intersectGenerationParameterCapabilityLists([][]ChatGenerationParameterCapability{
		{{Parameter: "temperature", Min: 0, Max: 2, DefaultValue: 1}},
		{{Parameter: "topP", Min: 0, Max: 1, DefaultValue: 1}},
	})
	if len(missing) != 0 {
		t.Fatalf("参数缺失交集应为空")
	}
	narrowed := intersectGenerationParameterCapabilityLists([][]ChatGenerationParameterCapability{
		{{Parameter: "temperature", Min: 0, Max: 2, DefaultValue: 1}},
		{{Parameter: "temperature", Min: 0.5, Max: 1.5, DefaultValue: 1}},
		{{Parameter: "temperature", Min: 0.8, Max: 3, DefaultValue: 1}},
	})
	if len(narrowed) != 1 || narrowed[0].Min != 0.8 || narrowed[0].Max != 1.5 {
		t.Fatalf("交集宽窄合并失败: %+v", narrowed)
	}
	if items := constrainChatGenerationParametersForRoute(nil, "m", ProtocolChatCompletions, nil); len(items) != 0 {
		t.Fatalf("无账户应为空")
	}
	if got := flattenGenerationParameters(nil); len(got) != 0 {
		t.Fatalf("空目录展平应为空")
	}
	if got := flattenGenerationParameters([]ProviderModelCatalogItem{{Model: "m", ProviderCode: "gpt", SupportedAPIProtocols: []string{"chat_completions", "bogus"}}}); len(got) == 0 {
		t.Fatalf("单条目展平不应为空")
	}
	if got := flattenGenerationParameters([]ProviderModelCatalogItem{
		{Model: "m", ProviderCode: "gpt", SupportedAPIProtocols: []string{"chat_completions"}},
		{Model: "m", ProviderCode: "gpt", SupportedAPIProtocols: []string{"responses"}},
	}); len(got) != 0 {
		t.Fatalf("协议不相交应展平为空")
	}
}

func TestW13BRouteCapabilityForAccount(t *testing.T) {
	capability := ChatGenerationParameterCapability{Parameter: "temperature", Min: 0, Max: 2, DefaultValue: 1}
	oauth := ChatTransportAccount{ProviderCode: "gpt", Type: "oauth"}
	if _, ok := routeCapabilityForAccount(oauth, capability, "m", ProtocolChatCompletions); ok {
		t.Fatalf("gpt oauth 应拒绝采样参数")
	}
	plain := ChatTransportAccount{ProviderCode: "openai", Type: "api_key"}
	if got, ok := routeCapabilityForAccount(plain, capability, "m", ProtocolChatCompletions); !ok || got.Min != 0 {
		t.Fatalf("无映射应直通: %+v %v", got, ok)
	}
	bridged := ChatTransportAccount{ProviderCode: "openai", Type: "api_key", ModelMappings: []ChatTransportModelMapping{{
		SourceModel: "m", SourceEndpointFamily: "chat_completions", UpstreamEndpointFamily: "responses",
	}}}
	if _, ok := routeCapabilityForAccount(bridged, ChatGenerationParameterCapability{Parameter: "seed"}, "m", ProtocolChatCompletions); ok {
		t.Fatalf("桥接下非保留参数应被剔除")
	}
	if _, ok := routeCapabilityForAccount(bridged, capability, "m", ProtocolChatCompletions); !ok {
		t.Fatalf("桥接下 temperature 应保留")
	}
	if _, ok := routeCapabilityForAccount(bridged, ChatGenerationParameterCapability{Parameter: "maxOutputTokens"}, "m", ProtocolChatCompletions); !ok {
		t.Fatalf("桥接下 maxOutputTokens 应保留")
	}
	sameModel := ChatTransportAccount{ProviderCode: "openai", Type: "api_key", ModelMappings: []ChatTransportModelMapping{{
		SourceModel: "m", SourceEndpointFamily: "chat_completions", UpstreamModel: "m",
	}}}
	if _, ok := routeCapabilityForAccount(sameModel, capability, "m", ProtocolChatCompletions); !ok {
		t.Fatalf("上游同模型应直通")
	}
	different := ChatTransportAccount{ProviderCode: "gpt", Type: "api_key", ModelMappings: []ChatTransportModelMapping{{
		SourceModel: "m", SourceEndpointFamily: "chat_completions", UpstreamModel: "gpt-5",
	}}}
	if _, ok := routeCapabilityForAccount(different, capability, "m", ProtocolChatCompletions); ok {
		t.Fatalf("gpt-5 上游无 temperature 应剔除")
	}
	if _, ok := routeCapabilityForAccount(different, ChatGenerationParameterCapability{Parameter: "maxOutputTokens"}, "m", ProtocolChatCompletions); !ok {
		t.Fatalf("gpt-5 上游应支持 maxOutputTokens")
	}
	disabledMapping := ChatTransportAccount{ProviderCode: "openai", Type: "api_key", ModelMappings: []ChatTransportModelMapping{{
		Enabled: boolPtr(false), SourceModel: "m", SourceEndpointFamily: "chat_completions",
	}}}
	if _, ok := routeCapabilityForAccount(disabledMapping, capability, "m", ProtocolChatCompletions); !ok {
		t.Fatalf("禁用映射应被跳过并直通")
	}
}

func TestW13BSelectChatTransportAndReasoningDefault(t *testing.T) {
	both := []ChatTransportProtocol{ProtocolChatCompletions, ProtocolResponses}
	if got := selectChatTransport(both, false); got != ProtocolChatCompletions {
		t.Fatalf("默认应选 chat_completions: %s", got)
	}
	if got := selectChatTransport(both, true); got != ProtocolResponses {
		t.Fatalf("偏好 responses 应选 responses: %s", got)
	}
	responsesOnly := []ChatTransportProtocol{ProtocolResponses}
	if got := selectChatTransport(responsesOnly, false); got != ProtocolResponses {
		t.Fatalf("仅 responses 应回退: %s", got)
	}
	none := []ChatTransportProtocol{"messages"}
	if got := selectChatTransport(none, false); got != ProtocolChatCompletions {
		t.Fatalf("无可用协议应回退 chat_completions: %s", got)
	}
	items := []ProviderModelCatalogItem{{DefaultReasoningEffort: strPtrT("low")}}
	if got := commonReasoningDefault(items, []string{"low"}); got != "low" {
		t.Fatalf("默认思考级别失败: %q", got)
	}
	if got := commonReasoningDefault([]ProviderModelCatalogItem{{DefaultReasoningEffort: strPtrT("bogus")}}, []string{"low"}); got != "" {
		t.Fatalf("非法默认思考级别应为空")
	}
	if got := commonReasoningDefault([]ProviderModelCatalogItem{
		{DefaultReasoningEffort: strPtrT("low")}, {DefaultReasoningEffort: strPtrT("high")},
	}, []string{"low", "high"}); got != "" {
		t.Fatalf("默认值不一致应为空")
	}
	if got := commonReasoningDefault([]ProviderModelCatalogItem{{}}, []string{"low"}); got != "" {
		t.Fatalf("缺默认应为空")
	}
	// resolveChatSupportedProtocols 的账户聚合。
	enabled := true
	protocols := resolveChatSupportedProtocols([]string{"g1"}, "m", func(groupID, model, endpointFamily string) []ChatTransportAccount {
		return []ChatTransportAccount{{SupportedEndpointModes: []string{"chat_sse", "responses_sse"}, ModelMappings: []ChatTransportModelMapping{{Enabled: &enabled, SourceModel: model, SourceEndpointFamily: endpointFamily}}}}
	})
	if len(protocols) != 2 {
		t.Fatalf("双协议解析失败: %v", protocols)
	}
	empty := resolveChatSupportedProtocols([]string{"g1"}, "m", func(groupID, model, endpointFamily string) []ChatTransportAccount {
		return nil
	})
	if len(empty) != 0 {
		t.Fatalf("空账户应返回空协议: %v", empty)
	}
}

func TestW13BJpegWebpDimensions(t *testing.T) {
	if w, h := jpegDimensions([]byte{0xFF, 0xD8}); w != 0 || h != 0 {
		t.Fatalf("过短 jpeg 应为 0")
	}
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x02}, make([]byte, 20)...)
	if w, h := jpegDimensions(jpeg); w != 0 || h != 0 {
		t.Fatalf("无 SOF jpeg 应为 0")
	}
	sof := []byte{0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x0B, 0x00, 0x00, 0x08, 0x00, 0x10, 0, 0, 0, 0, 0}
	if w, h := jpegDimensions(sof); w != 16 || h != 8 {
		t.Fatalf("SOF 尺寸失败: %d %d", w, h)
	}
	if w, h := webpDimensions([]byte("RIFF")); w != 0 || h != 0 {
		t.Fatalf("过短 webp 应为 0")
	}
	vp8 := make([]byte, 30)
	copy(vp8, "RIFF\x00\x00\x00\x00WEBPVP8 ")
	vp8[27], vp8[29] = 0x10, 0x08
	if w, h := webpDimensions(vp8); w != 16 || h != 8 {
		t.Fatalf("VP8 尺寸失败: %d %d", w, h)
	}
	vp8l := make([]byte, 30)
	copy(vp8l, "RIFF\x00\x00\x00\x00WEBPVP8L")
	vp8l[24] = 0x0F
	if w, h := webpDimensions(vp8l); w != 16 || h != 1 {
		t.Fatalf("VP8L 尺寸失败: %d %d", w, h)
	}
	vp8x := make([]byte, 30)
	copy(vp8x, "RIFF\x00\x00\x00\x00WEBPVP8X")
	vp8x[24], vp8x[27] = 15, 7
	if w, h := webpDimensions(vp8x); w != 16 || h != 8 {
		t.Fatalf("VP8X 尺寸失败: %d %d", w, h)
	}
	unknown := make([]byte, 30)
	copy(unknown, "RIFF\x00\x00\x00\x00WEBPXXXX")
	if w, h := webpDimensions(unknown); w != 0 || h != 0 {
		t.Fatalf("未知 webp 块应为 0")
	}
}

func TestW13BLocalObjectStorePaths(t *testing.T) {
	if _, err := NewLocalObjectStore(string([]byte{0}) /* NUL 目录非法 */); err == nil {
		t.Fatalf("非法根目录应报错")
	}
	store, err := NewLocalObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.path("../escape"); err == nil {
		t.Fatalf("越出根目录应报错")
	}
	if _, err := store.path(filepath.Join(os.TempDir(), "w13b-outside")); err == nil {
		t.Fatalf("绝对路径应报错")
	}
	if err := store.Write("k", []byte("data"), 0, ""); err != nil {
		t.Fatalf("默认上限应生效: %v", err)
	}
	if err := store.Write("k2", []byte(strings.Repeat("x", 10)), 5, ""); err == nil {
		t.Fatalf("超限写入应报错")
	}
	if err := store.Write("k3", nil, 10, ""); err == nil {
		t.Fatalf("空写入应报错")
	}
	if err := store.Write("k4", []byte("data"), 10, strings.Repeat("0", 64)); err == nil {
		t.Fatalf("哈希不匹配应报错")
	}
	if err := store.Write("k5", []byte("data"), 10, strings.ToUpper(hexEncode(sha256SumW13B([]byte("data"))))); err != nil {
		t.Fatalf("大小写不敏感哈希应通过: %v", err)
	}
	if _, _, err := store.Open("missing", 10); err == nil {
		t.Fatalf("缺失对象应报错")
	}
	if err := store.Write("dir-obj", []byte("data"), 10, ""); err != nil {
		t.Fatal(err)
	}
	_ = store.Write("k", []byte("data2"), 10, "")
	if err := store.Delete([]string{"k", "", "   ", "../bad"}); err != nil {
		t.Fatalf("Delete 应容忍缺失: %v", err)
	}
	if data, n, err := store.Open("dir-obj", 10); err != nil || n != 4 || string(data) != "data" {
		t.Fatalf("Open 失败: %v %d", err, n)
	}
	if _, _, err := store.Open("k", 1); err == nil {
		t.Fatalf("超读取上限应报错")
	}
}

func TestW13BImageToolNormalizers(t *testing.T) {
	if got, err := normalizeChatImageOutputFormat(nil); err != nil || got != "webp" {
		t.Fatalf("默认输出格式失败")
	}
	if got, err := normalizeChatImageOutputFormat(" JPG "); err != nil || got != "jpeg" {
		t.Fatalf("jpg 归一失败: %q %v", got, err)
	}
	if _, err := normalizeChatImageOutputFormat("bmp"); err == nil {
		t.Fatalf("非法输出格式应报错")
	}
	if got, err := normalizeChatImageQuality("HIGH"); err != nil || got != "high" {
		t.Fatalf("quality 归一失败")
	}
	if _, err := normalizeChatImageQuality("ultra"); err == nil {
		t.Fatalf("非法 quality 应报错")
	}
	if got, err := normalizeChatImageSize("1024X1024"); err != nil || got != "1024x1024" {
		t.Fatalf("尺寸归一失败: %q %v", got, err)
	}
	if _, err := normalizeChatImageSize("1000x1000"); err == nil {
		t.Fatalf("非 16 倍数应报错")
	}
	if _, err := normalizeChatImageSize("4000x1024"); err == nil {
		t.Fatalf("超长边应报错")
	}
	if _, err := normalizeChatImageSize("2048x512"); err == nil {
		t.Fatalf("超比例应报错")
	}
	if _, err := normalizeChatImageSize("256x256"); err == nil {
		t.Fatalf("像素过低应报错")
	}
	if _, err := normalizeChatImageSize("bogus"); err == nil {
		t.Fatalf("非法尺寸应报错")
	}
	if parseInt("12x") != 0 {
		t.Fatalf("parseInt 非数字应为 0")
	}
	ids, err := normalizeReferenceAssetIDs([]any{" chat_asset_" + strings.Repeat("a", 32) + " "})
	if err != nil || len(ids) != 1 {
		t.Fatalf("引用归一失败: %v", err)
	}
	if _, err := normalizeReferenceAssetIDs("nope"); err == nil {
		t.Fatalf("非数组引用应报错")
	}
	if _, err := normalizeReferenceAssetIDs([]any{42}); err == nil {
		t.Fatalf("非法 assetId 应报错")
	}
	if _, err := normalizeReferenceAssetIDs(make([]any, 6)); err == nil {
		t.Fatalf("超过 5 张引用应报错")
	}
	if _, err := normalizeReferenceAssetIDs([]any{"chat_asset_" + strings.Repeat("a", 32), "chat_asset_" + strings.Repeat("a", 32)}); err == nil {
		t.Fatalf("重复引用应报错")
	}
	if got := chatToolErrorCode(&chatInternalToolError{Code: "tool_timeout"}); got != "tool_timeout" {
		t.Fatalf("工具错误码失败")
	}
	if got := chatToolErrorCode(&chatToolSchemaError{Code: "schema"}); got != "schema" {
		t.Fatalf("schema 错误码失败")
	}
	if got := chatToolErrorCode(errors.New("plain")); got != "tool_execution_failed" {
		t.Fatalf("普通错误码失败")
	}
	if got := publicChatDiagnostic(nil, "fallback"); got != "fallback" {
		t.Fatalf("nil 诊断应回退")
	}
	if got := publicChatDiagnostic(errors.New("  "), "fallback"); got != "fallback" {
		t.Fatalf("空白诊断应回退")
	}
	if got := publicChatDiagnostic(errors.New("boom"), "fallback"); got != "fallback；详情：boom" {
		t.Fatalf("诊断拼接失败: %q", got)
	}
}

func TestW13BProjectToolEvent(t *testing.T) {
	blocks := []*assistantBlock{}
	projectToolEvent(&blocks, "tool_started", map[string]any{"type": "web_search"})
	if len(blocks) != 1 || blocks[0].CallID != "tool_1" || blocks[0].ToolType != "web_search" {
		t.Fatalf("无 id 工具块创建失败: %+v", blocks)
	}
	projectToolEvent(&blocks, "tool_completed", map[string]any{"id": "tool_1"})
	if blocks[0].Status != "completed" {
		t.Fatalf("既有工具块更新失败")
	}
	projectToolEvent(&blocks, "tool_started", map[string]any{"call_id": "custom"})
	if len(blocks) != 2 || blocks[1].CallID != "custom" || blocks[1].ToolType != "tool" {
		t.Fatalf("call_id 工具块创建失败: %+v", blocks)
	}
	projection := chatGenerationToolEventProjection("tool_updated", map[string]any{"id": "x", "type": "web_search"})
	if projection.Status != "updated" || projection.ID != "x" {
		t.Fatalf("工具事件投影失败: %+v", projection)
	}
	if got := chatGenerationToolEventProjection("other", nil).ID; got != "tool" {
		t.Fatalf("默认工具 id 失败")
	}
	if msg := upstreamMessagePayload(`{"error":{"message":"上游错误"}}`, "fallback"); msg != "上游错误" {
		t.Fatalf("上游错误消息失败: %q", msg)
	}
	if msg := upstreamMessagePayload(`{"message":"顶层错误"}`, "fallback"); msg != "顶层错误" {
		t.Fatalf("顶层错误消息失败: %q", msg)
	}
	if msg := upstreamMessagePayload("not json", "fallback"); msg != "fallback" {
		t.Fatalf("非法载荷应回退")
	}
	if code := classifyGenerationError(&ChatImageGenerationRequestError{Code: GenErrImageFailed}, GenErrInternal).Code; code != GenErrImageFailed {
		t.Fatalf("图片错误分类失败")
	}
	if code := classifyGenerationError(errors.New("x"), GenErrUpstreamHTTP).Code; code != GenErrUpstreamHTTP {
		t.Fatalf("失败码透传失败: %s", code)
	}
	if code := classifyGenerationError(errors.New("x"), GenErrInternal).Code; code != GenErrInternal {
		t.Fatalf("未知错误分类失败")
	}
	if headRevision(&chatRoutes{deps: &Deps{Store: newChatFixture(t).store}}, "w13b-missing", "o") != 0 {
		t.Fatalf("无 head 应返回 0")
	}
	published := ""
	publishApplicationToolEvent(func(eventType string, data map[string]any, update ChatGenerationProjectionUpdate) bool {
		published = eventType
		return true
	}, "m", ChatToolExecutionEvent{
		Status: "completed", CallID: "c1", ToolName: "generate_image", Reused: true,
		ErrorCode: "x", ErrorMessage: "y",
		PublicResult: map[string]any{"assetId": "a1", "mimeType": "image/webp", "width": 8, "height": 8},
	})
	if published != "tool.completed" {
		t.Fatalf("应用工具事件发布失败: %q", published)
	}
}

func TestW13BRecoverChatTurnFinalization(t *testing.T) {
	f := newChatFixture(t)
	accepted := f.accept("owner", f.createConversation("conv_rec", "owner").ID, "cmid-r", "问题")
	env := newGenerationEnv(t)
	rt := newChatRoutesForTest(env.deps)
	// 终态权威：完成该轮后恢复应返回 completed。
	f.complete("owner", "conv_rec", accepted.TurnID, "答")
	if got := rt.recoverChatTurnFinalization("conv_rec", "owner", accepted.TurnID, "cmid-r", errors.New("x")); got != "completed" {
		t.Fatalf("恢复应返回 completed: %q", got)
	}
	// 不匹配轮次：返回 failed。
	if got := rt.recoverChatTurnFinalization("conv_rec", "owner", "turn-other", "cmid-r", errors.New("x")); got != "failed" {
		t.Fatalf("不匹配应返回 failed: %q", got)
	}
}

func TestW13BReadBoundedAllAndInstant(t *testing.T) {
	if _, err := readBoundedAll(strings.NewReader("abc"), 10); err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if _, err := readBoundedAll(strings.NewReader("abcdef"), 3); err == nil {
		t.Fatalf("超限应报错")
	}
	if _, err := readBoundedAll(iotestErrW13B{}, 10); err == nil {
		t.Fatalf("读取错误应上抛")
	}
	if _, err := requireRFC3339Instant("2026-02-30T08:00:00.000Z", "x"); err == nil {
		t.Fatalf("2 月 30 日应报错")
	}
	long := strings.Repeat(" secrets", 4000)
	sanitized := sanitizeChatDiagnosticMessage(long)
	if len([]rune(sanitized)) > maxPublicDiagnosticMessageLength {
		t.Fatalf("诊断长度应受限: %d", len([]rune(sanitized)))
	}
	if sanitizeChatDiagnosticMessage("   ") != "" {
		t.Fatalf("空白诊断应为空")
	}
}

type iotestErrW13B struct{}

func (iotestErrW13B) Read([]byte) (int, error) { return 0, errors.New("read boom") }

func TestW13BResolveChatModelRequestOptions(t *testing.T) {
	rt := newChatRoutesForTest(&Deps{})
	option := &ChatModelOption{
		SupportedReasoningEfforts: []string{"low"},
		SupportedServiceTiers:     []string{"priority"},
		GenerationParameters: []ChatGenerationParameterCapability{
			{Parameter: "temperature", Min: 0, Max: 2, DefaultValue: 1},
		},
		MaxInputTokens: int64PtrT(999),
	}
	body := &streamMessageBody{ReasoningEffort: "high"}
	if _, _, _, _, err := resolveChatModelRequestOptions(rt, option, body); err == nil {
		t.Fatalf("不支持的思考级别应报错")
	}
	body = &streamMessageBody{ServiceTier: "flex"}
	if _, _, _, _, err := resolveChatModelRequestOptions(rt, option, body); err == nil {
		t.Fatalf("不支持的服务等级应报错")
	}
	body = &streamMessageBody{GenerationParameters: &ChatGenerationParameters{Temperature: floatPtrW13B(1), TopP: floatPtrW13B(0.5)}}
	if _, _, _, _, err := resolveChatModelRequestOptions(rt, option, body); err == nil {
		t.Fatalf("temperature 与 topP 互斥应报错")
	}
	body = &streamMessageBody{GenerationParameters: &ChatGenerationParameters{FrequencyPenalty: floatPtrW13B(0.5)}}
	if _, _, _, _, err := resolveChatModelRequestOptions(rt, option, body); err == nil {
		t.Fatalf("未声明的参数应报错")
	}
	body = &streamMessageBody{GenerationParameters: &ChatGenerationParameters{Temperature: floatPtrW13B(5)}}
	if _, _, _, _, err := resolveChatModelRequestOptions(rt, option, body); err == nil {
		t.Fatalf("超范围参数应报错")
	}
	body = &streamMessageBody{GenerationParameters: &ChatGenerationParameters{Seed: floatPtrW13B(1.5)}}
	if _, _, _, _, err := resolveChatModelRequestOptions(rt, option, body); err == nil {
		t.Fatalf("非整数 seed 应报错")
	}
	body = &streamMessageBody{}
	effort, tier, params, maxInput, err := resolveChatModelRequestOptions(rt, option, body)
	if err != nil || maxInput == nil || *maxInput != 999 || params == nil {
		t.Fatalf("合法请求解析失败: %v", err)
	}
	if effort != "" || tier != "" {
		t.Fatalf("空请求 effort/tier 应为空")
	}
}

func floatPtrW13B(value float64) *float64 { return &value }

func TestW13BParseResponsesBlockMatrix(t *testing.T) {
	if parsed := parseResponsesBlock("data: [DONE]\n\n"); parsed.event != nil {
		t.Fatalf("DONE 块应为空")
	}
	if parsed := parseResponsesBlock("data: not-json\n\n"); parsed.event != nil {
		t.Fatalf("非法 JSON 应为空")
	}
	if parsed := parseResponsesBlock("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"); parsed.event == nil || parsed.event.Type != "text_delta" {
		t.Fatalf("text_delta 解析失败")
	}
	if parsed := parseResponsesBlock("data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"x\"}\n\n"); parsed.event.Type != "reasoning_delta" {
		t.Fatalf("reasoning_delta 解析失败")
	}
	if parsed := parseResponsesBlock("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"web_search_call\"}}\n\n"); parsed.event == nil || parsed.event.Type != "tool_started" {
		t.Fatalf("tool_started 解析失败")
	}
	if parsed := parseResponsesBlock("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\"}}\n\n"); parsed.event != nil {
		t.Fatalf("message added 应为空")
	}
	if parsed := parseResponsesBlock("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\"}}\n\n"); parsed.event.Type != "reasoning_completed" {
		t.Fatalf("reasoning_completed 解析失败")
	}
	if parsed := parseResponsesBlock("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"file_search_call\"}}\n\n"); parsed.event.Type != "tool_completed" {
		t.Fatalf("tool_completed 解析失败")
	}
	if parsed := parseResponsesBlock("data: {\"type\":\"response.failed\",\"error\":{\"message\":\"x\"}}\n\n"); parsed.event.Type != "failed" {
		t.Fatalf("failed 解析失败")
	}
	if parsed := parseResponsesBlock("data: {\"type\":\"response.failed\",\"response\":{}}\n\n"); parsed.event.Type != "failed" {
		t.Fatalf("failed response 解析失败")
	}
	if parsed := parseResponsesBlock("data: {\"type\":\"response.failed\"}\n\n"); parsed.event.Type != "failed" || parsed.event.Error == nil {
		t.Fatalf("failed 默认解析失败")
	}
	if parsed := parseResponsesBlock("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"image_generation_call\",\"id\":\"img1\"}}\n\n"); parsed.event == nil || parsed.event.Type != "image_started" {
		t.Fatalf("图片 added 解析失败")
	}
	merged := parseResponsesBlock("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"image_generation_call\",\"call_id\":\"img1\"}}\n\n")
	if merged.event.Item["callId"] != "img1" {
		t.Fatalf("图片 callId 合并失败")
	}
	if parsed := parseResponsesBlock("data: {\"type\":\"response.image_generation_call.completed\",\"status\":\"completed\",\"result\":\"QUJD\"}\n\n"); parsed.event.Type != "image_completed" || parsed.imageResultData == "" {
		t.Fatalf("图片完成解析失败")
	}
	if parsed := parseResponsesBlock("data: {\"type\":\"image_generation_call.partial_image\"}\n\n"); parsed.event.Type != "image_updated" {
		t.Fatalf("图片部分解析失败")
	}
	if parsed := parseResponsesBlock("data: {\"type\":\"image_generation.failed\"}\n\n"); parsed.event.Type != "image_failed" {
		t.Fatalf("图片失败解析失败")
	}
	if parsed := parseResponsesBlock("data: {\"type\":\"image_generation_call.done\"}\n\n"); parsed.event.Type != "image_failed" {
		t.Fatalf("无结果 done 应为 image_failed")
	}
	if parsed := parseResponsesBlock("data: {\"type\":\"image_generation_call.added\"}\n\n"); parsed.event.Type != "image_started" {
		t.Fatalf("无结果 added 应为 image_started")
	}
	if responsesImageCallID(map[string]any{"call_id": " x "}) != "x" {
		t.Fatalf("call_id 提取失败")
	}
	if responsesImageCallID(map[string]any{"id": 9}) != "" {
		t.Fatalf("非字符串 id 应为空")
	}
	if !isImageResultFieldName("b64_json") || isImageResultFieldName("other") {
		t.Fatalf("图片字段名判断失败")
	}
	if extractImageResultChunks("plain") != nil {
		t.Fatalf("无字段应返回 nil")
	}
	if extractImageResultChunksWithFields(`{"b64_json":"QUJD"}`, "b64_json") == nil {
		t.Fatalf("b64 提取失败")
	}
	images := completedResponseImages(map[string]any{"output": []any{
		map[string]any{"type": "image_generation_call", "result": "QUJD", "call_id": "c1", "revised_prompt": "p"},
		map[string]any{"type": "image_generation_call"},
		map[string]any{"type": "message"},
	}})
	if len(images) != 1 || images[0].callID != "c1" || images[0].revisedPrompt != "p" {
		t.Fatalf("完成图片提取失败: %+v", images)
	}
	item := normalizeResponsesContinuationItem(map[string]any{"type": "function_call", "call_id": "call-1"})
	if item["id"] == "" || item["status"] != "completed" {
		t.Fatalf("续答项归一失败: %v", item)
	}
	kept := normalizeResponsesContinuationItem(map[string]any{"type": "function_call"})
	if kept["id"] != nil {
		t.Fatalf("无 call_id 应原样返回")
	}
	long := normalizeResponsesContinuationItem(map[string]any{"type": "function_call", "call_id": strings.Repeat("a", 80)})
	if id, _ := long["id"].(string); len(id) != 63 {
		t.Fatalf("超长 id 应截断: %d", len(id))
	}
}

func TestW13BStripImageResultStrings(t *testing.T) {
	payload, values, err := stripImageResultStrings(`{"a":"x","result":"QUJD","b64_json":"REVG"}`, "result", "b64_json")
	if err != nil || len(values) != 2 || values[0] != "QUJD" || values[1] != "REVG" {
		t.Fatalf("提取失败: %q %v %v", payload, values, err)
	}
	if strings.Contains(payload, "QUJD") {
		t.Fatalf("载荷应剔除 base64: %q", payload)
	}
	if _, _, err := stripImageResultStrings(`{"result":"AB\\C"}`); err == nil {
		t.Fatalf("转义 base64 应报错")
	}
	if _, _, err := stripImageResultStrings(`{"result":""}`); err == nil {
		t.Fatalf("空 base64 应报错")
	}
	if _, _, err := stripImageResultStrings(`{"result":"unterminated`); err == nil {
		t.Fatalf("截断 JSON 应报错")
	}
	if _, _, err := stripImageResultStrings(`{"result":"QUJD"}`); err != nil {
		t.Fatalf("默认字段应生效: %v", err)
	}
}

func TestW13BCollectOpenAIChatSseEdgeCases(t *testing.T) {
	if _, err := CollectOpenAIChatSse(strings.NewReader("\xff\xfe"), 100, nil, 0); err == nil {
		t.Fatalf("无效 UTF-8 应报错")
	}
	stream := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":-1}]}}]}\n\n" + "data: [DONE]\n\n"
	if _, err := CollectOpenAIChatSse(strings.NewReader(stream), 100, nil, 0); err == nil {
		t.Fatalf("非法工具 index 应报错")
	}
	stream = "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"a\",\"function\":{\"name\":\"f\",\"arguments\":\"{}\"}}]}}]}\n\ndata: [DONE]\n\n"
	result, err := CollectOpenAIChatSse(strings.NewReader(stream), 100, nil, 0)
	if err != nil || len(result.ToolCalls) != 1 || result.ToolCalls[0].CallID != "a" {
		t.Fatalf("工具调用收集失败: %v %+v", err, result)
	}
	stream = "data: {\"error\":{\"message\":null}}\n\ndata: [DONE]\n\n"
	if _, err := CollectOpenAIChatSse(strings.NewReader(stream), 100, nil, 0); err == nil || err.Error() != "上游流式请求失败" {
		t.Fatalf("非字符串错误应回退: %v", err)
	}
	stream = "data: [DONE]\n\n"
	if _, err := CollectOpenAIChatSse(strings.NewReader(stream), 100, nil, 0); err != nil {
		t.Fatalf("DONE 应成功")
	}
	events := strings.Repeat("data: {}\n\n", 3)
	if _, err := CollectOpenAIChatSse(strings.NewReader(events+"data: [DONE]\n\n"), 100, nil, 2); err == nil {
		t.Fatalf("事件数超限应报错")
	}
	if _, err := CollectOpenAIChatSse(strings.NewReader("data: {}\n\ndata: {}\n\ndata: [DONE]\n\n"), 100, nil, 1); err == nil {
		t.Fatalf("事件数超限（尾部缓冲）应报错")
	}
	big := "data: " + strings.Repeat("x", 70*1024) + "\n\n"
	if _, err := CollectOpenAIChatSse(strings.NewReader(big+"data: [DONE]\n\n"), 100, nil, 0); err == nil {
		t.Fatalf("单事件超限应报错")
	}
	if _, err := CollectOpenAIChatSse(strings.NewReader(strings.Repeat("x", 70*1024)), 100, nil, 0); err == nil {
		t.Fatalf("尾部缓冲超限应报错")
	}
	stream = "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"a\",\"function\":{\"arguments\":\"" + strings.Repeat("x", 70*1024) + "\"\"}}]}}]}\n\n"
	if _, err := CollectOpenAIChatSse(strings.NewReader(stream), 100, nil, 0); err == nil {
		t.Fatalf("工具参数超限应报错")
	}
	stream = "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0}]}}]}\n\ndata: [DONE]\n\n"
	if _, err := CollectOpenAIChatSse(strings.NewReader(stream), 100, nil, 0); err == nil {
		t.Fatalf("缺失 id/name/arguments 应报错")
	}
	boundary := findEventBoundary("a\r\n\r\nb")
	if boundary == nil || boundary.length != 4 {
		t.Fatalf("CRLF 边界失败: %+v", boundary)
	}
	if findEventBoundary("abc") != nil {
		t.Fatalf("无边界的输入应返回 nil")
	}
	if mergeStableToolField("ab", "") != "ab" || mergeStableToolField("ab", "ab") != "ab" || mergeStableToolField("", "x") != "x" || mergeStableToolField("ab", "b") != "ab" {
		t.Fatalf("mergeStableToolField 分支失败")
	}
	if mergeStableToolField("ab", "c") != "abc" {
		t.Fatalf("mergeStableToolField 追加失败")
	}
}

func TestW13BCollectChatResponsesSseEdgeCases(t *testing.T) {
	if _, err := CollectChatResponsesSse(errReaderW13B{}, 100, 0, nil, nil); err == nil {
		t.Fatalf("读取错误应上抛")
	}
	if _, err := CollectChatResponsesSse(strings.NewReader("\xff"), 100, 0, nil, nil); err == nil {
		t.Fatalf("无效 UTF-8 应报错")
	}
	stream := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":2},\"output\":[]}}\n\n"
	result, err := CollectChatResponsesSse(strings.NewReader(stream), 100, 0, nil, nil)
	if err != nil || result.Content != "hi" || *result.InputTokens != 5 {
		t.Fatalf("responses 收集失败: %v %+v", err, result)
	}
	// completedItems 回退路径（无 output 数组）。
	stream = "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"c1\",\"name\":\"f\",\"arguments\":\"{}\",\"id\":\"i1\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{}}\n\n"
	result, err = CollectChatResponsesSse(strings.NewReader(stream), 100, 0, nil, nil)
	if err != nil || len(result.ToolCalls) != 1 || len(result.ContinuationItems) != 1 {
		t.Fatalf("completedItems 回退失败: %v %+v", err, result)
	}
	// 尾部缓冲消费。
	stream = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{}}"
	if _, err := CollectChatResponsesSse(strings.NewReader(stream), 100, 0, nil, nil); err != nil {
		t.Fatalf("尾部 completed 块应消费: %v", err)
	}
	// 事件数超限。
	events := strings.Repeat("data: {}\n\n", 4)
	if _, err := CollectChatResponsesSse(strings.NewReader(events+"data: {\"type\":\"response.completed\",\"response\":{}}\n\n"), 100, 2, nil, nil); err == nil {
		t.Fatalf("事件数超限应报错")
	}
	// 非图片大事件超限。
	big := "data: " + strings.Repeat("x", 70*1024) + "\n\n"
	if _, err := CollectChatResponsesSse(strings.NewReader(big), 100, 0, nil, nil); err == nil {
		t.Fatalf("大事件应报错")
	}
	// 截断的图片事件。
	bigImage := "event: image_generation_call\n" + strings.Repeat("x", 70*1024)
	if _, err := CollectChatResponsesSse(strings.NewReader(bigImage), 100, 0, nil, nil); err == nil || err.Error() != "图像 SSE 事件被截断" {
		t.Fatalf("截断图片事件应报错: %v", err)
	}
	// onEvent 回调错误传播。
	if _, err := CollectChatResponsesSse(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{}}\n\n"), 100, 0, func(event ChatResponsesEvent) error {
		return errors.New("callback boom")
	}, nil); err == nil {
		t.Fatalf("回调错误应上抛")
	}
	// 图片结果 sink。
	called := ""
	stream = "data: {\"type\":\"response.image_generation_call.completed\",\"id\":\"img1\",\"status\":\"completed\",\"result\":\"QUJD\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{}}\n\n"
	_, err = CollectChatResponsesSse(strings.NewReader(stream), 100, 0, nil, func(callID, revisedPrompt string, chunks []string) error {
		called = callID + ":" + strings.Join(chunks, ",")
		return nil
	})
	if err != nil || called != "img1:QUJD" {
		t.Fatalf("图片结果回调失败: %q %v", called, err)
	}
	// sink 错误传播。
	_, err = CollectChatResponsesSse(strings.NewReader(stream), 100, 0, nil, func(callID, revisedPrompt string, chunks []string) error {
		return errors.New("sink boom")
	})
	if err == nil {
		t.Fatalf("sink 错误应上抛")
	}
	// 辅助字节超限。
	aux := strings.Repeat("event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\""+strings.Repeat("x", 1000)+"\"}\n\n", 200)
	if _, err := CollectChatResponsesSse(strings.NewReader(aux+"data: {\"type\":\"response.completed\",\"response\":{}}\n\n"), 100, 0, nil, nil); err == nil {
		t.Fatalf("辅助字节超限应报错")
	}
	// 工具参数超限。
	args := "event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"i1\",\"delta\":\"" + strings.Repeat("x", 70*1024) + "\"}\n\n"
	if _, err := CollectChatResponsesSse(strings.NewReader(args), 100, 0, nil, nil); err == nil {
		t.Fatalf("工具参数超限应报错")
	}
}

type errReaderW13B struct{}

func (errReaderW13B) Read([]byte) (int, error) { return 0, errors.New("read boom") }

func TestW13BCompactionPureHelpers(t *testing.T) {
	service := NewCompactionService(nil, nil, nil, nil)
	service.Random = nil
	if got := service.passiveDelayISO(0); !strings.HasSuffix(got, "Z") {
		t.Fatalf("nil random 应回退: %q", got)
	}
	_, clock := fixedChatClock()
	service.WallClock = clock
	cases := []struct {
		interval int64
		sample   float64
	}{
		{interval: 100, sample: 0.5}, {interval: 59999, sample: 0}, {interval: 60000, sample: 1},
		{interval: 3599999, sample: 0.5}, {interval: 86399999, sample: 0.5},
		{interval: 7 * 86400000, sample: 0.5}, {interval: 10 * 86400000, sample: 0},
		{interval: 60000, sample: -1}, {interval: 60000, sample: math.NaN()},
	}
	for _, item := range cases {
		service.Random = func() float64 { return item.sample }
		delay := service.passiveDelayISO(item.interval)
		parsed, err := parseRFC3339Instant(delay)
		if err != nil {
			t.Fatalf("延迟解析失败: %v", err)
		}
		if diff := parsed.UnixMilli() - clock().UnixMilli(); diff < 1 {
			t.Fatalf("延迟必须至少 1ms: interval=%d sample=%v diff=%d", item.interval, item.sample, diff)
		}
	}
	if left, err := earlierTime("", ""); err != nil || left != "" {
		t.Fatalf("空时间合并失败")
	}
	if left, err := earlierTime("", "2026-03-10T08:00:00.000Z"); err != nil || left == "" {
		t.Fatalf("右侧时间合并失败")
	}
	if left, err := earlierTime("2026-03-10T07:00:00.000Z", ""); err != nil || left == "" {
		t.Fatalf("左侧时间合并失败")
	}
	if _, err := earlierTime("bad", "2026-03-10T07:00:00.000Z"); err == nil {
		t.Fatalf("左侧非法应报错")
	}
	if _, err := earlierTime("2026-03-10T07:00:00.000Z", "bad"); err == nil {
		t.Fatalf("右侧非法应报错")
	}
	merged, err := earlierTime("2026-03-10T07:00:00.000Z", "2026-03-10T06:00:00.000Z")
	if err != nil || merged != "2026-03-10T06:00:00.000Z" {
		t.Fatalf("较早时间选择失败: %q %v", merged, err)
	}
	if _, err := shiftInstantISO("bad", -1); err == nil {
		t.Fatalf("非法时间平移应报错")
	}
	if safeErrorCode(nil) != "chat_context_compaction_failed" {
		t.Fatalf("nil 错误码失败")
	}
	if got := safeErrorCode(errors.New("必须 处理!")); got != "______" {
		t.Fatalf("错误码归一失败: %q", got)
	}
	if errorReason(nil) != "chat_context_compaction_failed" {
		t.Fatalf("nil 原因失败")
	}
	empty := emptySnapshot()
	filled := fillRequiredSnapshotFields(memorySnapshot{}, empty, []any{})
	if filled.CurrentGoal != "" {
		t.Fatalf("无来源应保持空")
	}
	filled = fillRequiredSnapshotFields(memorySnapshot{}, memorySnapshot{CurrentGoal: "g", RecentUserIntent: "i"}, []any{})
	if filled.CurrentGoal != "g" || filled.RecentUserIntent != "i" {
		t.Fatalf("prior 回填失败")
	}
	if entries := snapshotEntries(service, emptySnapshot()); len(entries) != 2 {
		t.Fatalf("空快照应生成 2 条 entry: %d", len(entries))
	}
	toolSnapshot := emptySnapshot()
	toolSnapshot.ImportantToolResults = []map[string]any{{"name": "n", "result": "r"}}
	toolSnapshot.ImageMemories = []map[string]any{{"assetId": "a", "summary": "s"}}
	if entries := snapshotEntries(service, toolSnapshot); len(entries) != 4 {
		t.Fatalf("含工具与图片记忆应生成 4 条 entry: %d", len(entries))
	}
	if got := stringArray([]any{"a", "", "b", "c", "d", "e"}, 3); len(got) != 3 {
		t.Fatalf("stringArray 上限失败: %v", got)
	}
	if got := objectArray([]any{map[string]any{"a": 1}, "x", map[string]any{"b": 2}, map[string]any{"c": 3}}, 2); len(got) != 2 {
		t.Fatalf("objectArray 上限失败")
	}
	if got := boundedString("  hello  ", 3); got != "hel" {
		t.Fatalf("boundedString 失败: %q", got)
	}
	if stringValue(42) != "" || stringValue("s") != "s" {
		t.Fatalf("stringValue 失败")
	}
	bad := initialSnapshot([]contextEntry{{kind: "task_state", contentJSON: "not-json"}})
	if bad.CurrentGoal != "" {
		t.Fatalf("非法快照内容应回退空快照")
	}
}

func TestW13BNewCompactionServiceDefaults(t *testing.T) {
	service := NewCompactionService(nil, nil, nil, nil)
	if service.TokenCount("abcd") != 1 {
		t.Fatalf("默认 token 计数失败")
	}
	if service.Now() == "" {
		t.Fatalf("默认时钟失败")
	}
	if service.WallClock() == (time.Time{}) {
		t.Fatalf("默认墙钟失败")
	}
	if got := service.key(CompactionInput{SystemAccountID: "o", ConversationID: "c"}); got != "o:c" {
		t.Fatalf("键拼接失败: %q", got)
	}
}

func TestW13BCompactionConcurrencySameKey(t *testing.T) {
	fixture := newChatFixture(t)
	fixture.createConversation("w13b-conv-cc", "owner")
	fixture.seedTurns("owner", "w13b-conv-cc", 5)
	executor := &mockExecutor{}
	service := NewCompactionService(fixture.store, executor, func(text string) int { return len(text) / 4 }, func() string { return fixture.nowISO })
	input := CompactionInput{ConversationID: "w13b-conv-cc", SystemAccountID: "owner", Model: "gpt-5"}
	key := service.key(input)

	// join 分支：预置 active entry（完成值已在缓冲中），CompactOnce 直接返回该值。
	preset := &activeCompaction{
		acceptance: make(chan CompactionStartResult, 1),
		completion: make(chan CompactionResult, 1),
	}
	preset.completion <- CompactionResult{Status: "failed", Reason: "preset"}
	service.mu.Lock()
	service.active[key] = preset
	service.mu.Unlock()
	if result := service.CompactOnce(context.Background(), input); result.Reason != "preset" {
		t.Fatalf("join 分支应返回预置结果: %+v", result)
	}
	// Start already_running：真实 in-flight 压缩期间再次 Start。
	blocking := &mockExecutor{}
	blocking.steps = append(blocking.steps, scriptStep{match: func(call dispatchCall) bool { return true }, respond: func(call dispatchCall) *GenerationDispatchResponse { return nil }})
	blockingService := NewCompactionService(fixture.store, blocking, func(text string) int { return len(text) / 4 }, func() string { return fixture.nowISO })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan CompactionStartResult, 1)
	go func() { done <- blockingService.Start(ctx, input) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		blockingService.mu.Lock()
		_, running := blockingService.active[key]
		blockingService.mu.Unlock()
		if running || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if result := blockingService.Start(context.Background(), input); result.Status != "already_running" {
		t.Fatalf("in-flight Start 应返回 already_running: %+v", result)
	}
	cancel()
	<-done

	// 后台 Schedule 不阻塞主流程。
	service.Schedule(context.Background(), input)
	time.Sleep(50 * time.Millisecond)
}

func TestW13BTokenCountFallbacks(t *testing.T) {
	rt := &chatRoutes{deps: &Deps{}}
	if rt.tokenCount("abcd") != 1 || rt.estimateChatTokens("") != 1 || rt.estimateChatTokens("abcd") != 1 {
		t.Fatalf("token 回退失败")
	}
	if got := rt.estimateContentTokens("hello"); got != 2 {
		t.Fatalf("字符串内容估算失败: %d", got)
	}
	if got := rt.estimateContentTokens(make(chan int)); got != 1 {
		t.Fatalf("不可序列化内容应回退 1")
	}
	if got := rt.estimateContentTokens(map[string]any{"a": 1}); got < 1 {
		t.Fatalf("对象内容估算失败: %d", got)
	}
	if estimateChatImageTokens(nil, nil) != 2500 || estimateChatImageTokens(int64PtrT(0), int64PtrT(10)) != 2500 {
		t.Fatalf("图片 token 缺省失败")
	}
	if got := estimateChatImageTokens(int64PtrT(32), int64PtrT(32)); got != 86 {
		t.Fatalf("图片 token 计算失败: %d", got)
	}
	if err := rt.validateFixedChatInputBudget(fixedChatBudgetInput{}); err != nil {
		t.Fatalf("无上限预算应通过")
	}
	huge := strings.Repeat("x", 400)
	if err := rt.validateFixedChatInputBudget(fixedChatBudgetInput{
		CurrentUserContent: huge, MaxInputTokens: int64PtrT(1),
	}); err == nil {
		t.Fatalf("超预算应报错")
	}
	if err := rt.validateFixedChatInputBudget(fixedChatBudgetInput{MaxInputTokens: int64PtrT(0)}); err != nil {
		t.Fatalf("非正上限应跳过")
	}
}

func TestW13BImagePayloadDecoding(t *testing.T) {
	if _, err := decodeBase64Payload("!!!", 10); err == nil {
		t.Fatalf("非法 base64 应报错")
	}
	if _, err := decodeBase64Payload("", 10); err == nil {
		t.Fatalf("空解码应报错")
	}
	if _, err := decodeBase64Payload("QUJD", 1); err == nil {
		t.Fatalf("超限应报错")
	}
	if imageMimeTypeFromBytes([]byte("not-an-image")) != "" {
		t.Fatalf("未知格式应为空")
	}
	if got := chatAssetObjectExtension("image/gif"); got != ".bin" {
		t.Fatalf("未知扩展应回退 bin")
	}
	if got := safeStorageSegment("  ", 4); got != "__" {
		t.Fatalf("空段应替换为下划线: %q", got)
	}
	if got := safeStorageSegment(strings.Repeat("x", 300), 10); len(got) != 10 {
		t.Fatalf("段长应截断")
	}
	if got := normalizedSHA256("nothex"); got != strings.Repeat("0", 64) {
		t.Fatalf("非法摘要应回退全零")
	}
	key := StorageKeyForChatAsset("chat_asset_"+strings.Repeat("a", 32), strings.Repeat("b", 64), "image/png", "preview")
	if !strings.Contains(key, "preview") || !strings.HasSuffix(key, ".png") {
		t.Fatalf("存储键失败: %q", key)
	}
}

func TestW13BChatInternalToolRegistry(t *testing.T) {
	registry := newChatInternalToolRegistry("development", true, true)
	if _, err := registry.definition("missing"); err == nil {
		t.Fatalf("未知工具应报错")
	}
	if len(registry.resolveTools(true)) != 2 {
		t.Fatalf("双工具解析失败")
	}
	if len(registry.resolveTools(false)) != 0 {
		t.Fatalf("function calling 关闭应为空")
	}
	if len(newChatInternalToolRegistry("production", true, true).resolveTools(true)) != 1 {
		t.Fatalf("生产环境应只有图片工具")
	}
	if len(newChatInternalToolRegistry("development", false, false).resolveTools(true)) != 0 {
		t.Fatalf("未启用内部工具且未启用图片应为空")
	}
	if _, err := registry.normalizeArguments("any", "{}", 1); err == nil {
		t.Fatalf("参数超限应报错")
	}
	if _, err := registry.normalizeArguments("any", "not json", 100); err == nil {
		t.Fatalf("非法 JSON 应报错")
	}
	if _, err := registry.normalizeArguments("any", "[1]", 100); err == nil {
		t.Fatalf("根值非对象应报错")
	}
	value, err := registry.normalizeArguments("any", `{"a":1}`, 100)
	if err != nil || value["a"] != float64(1) {
		t.Fatalf("合法参数解析失败: %v", err)
	}
	if !availableInEnvironment([]string{"test"}, "test") || availableInEnvironment([]string{"test"}, "prod") {
		t.Fatalf("环境可用性判断失败")
	}
	echo := newDiagnosticEchoTool()
	result, err := echo.Execute(map[string]any{"text": "hi"}, nil)
	if err != nil || result.PublicResult["echoedText"] != "hi" {
		t.Fatalf("echo 工具失败: %v", err)
	}
	num, err := echo.Execute(map[string]any{"text": 7}, nil)
	if err != nil || num.PublicResult["echoedText"] != "7" {
		t.Fatalf("echo 数字回显失败: %+v", num)
	}
	empty, err := echo.Execute(nil, nil)
	if err != nil || empty.PublicResult["echoedText"] != "" {
		t.Fatalf("echo 空输入失败")
	}
}

func TestW13BImageGenerationTransportValidation(t *testing.T) {
	if _, err := GenerateChatImage(context.Background(), nil, ChatImageGenerationRequest{Model: " ", Prompt: "p"}, "key", ""); err == nil {
		t.Fatalf("空模型应报错")
	}
	if _, err := GenerateChatImage(context.Background(), nil, ChatImageGenerationRequest{Model: "m", Prompt: " "}, "key", ""); err == nil {
		t.Fatalf("空提示词应报错")
	}
	if _, err := GenerateChatImage(context.Background(), nil, ChatImageGenerationRequest{Model: "m", Prompt: "p", Size: "bad"}, "key", ""); err == nil {
		t.Fatalf("非法尺寸应报错")
	}
	if _, err := GenerateChatImage(context.Background(), nil, ChatImageGenerationRequest{Model: "m", Prompt: "p", Quality: "ultra"}, "key", ""); err == nil {
		t.Fatalf("非法质量应报错")
	}
	if _, err := GenerateChatImage(context.Background(), nil, ChatImageGenerationRequest{Model: "m", Prompt: "p", OutputFormat: "bmp"}, "key", ""); err == nil {
		t.Fatalf("非法输出格式应报错")
	}
	if _, err := GenerateChatImage(context.Background(), nil, ChatImageGenerationRequest{Model: "m", Prompt: "p", References: []ChatImageEditReference{{}}}, "key", ""); err == nil {
		t.Fatalf("引用限制应校验")
	}
	if err := validateChatImageEditReferenceLimits(nil); err == nil {
		t.Fatalf("空引用应报错")
	}
	if err := validateChatImageEditReferenceLimits(make([]ChatImageEditReference, 6)); err == nil {
		t.Fatalf("超量引用应报错")
	}
	if err := validateChatImageEditReferenceLimits([]ChatImageEditReference{{Bytes: 0}}); err == nil {
		t.Fatalf("零字节引用应报错")
	}
	if err := validateChatImageEditReferenceLimits([]ChatImageEditReference{{Bytes: 100}, {Bytes: chatImageEditMaxReferenceBytes}}); err == nil {
		t.Fatalf("总量超限应报错")
	}
	if code := imageGenerationPublicErrorCode(403, "Image generation is not enabled for this group", "permission_error"); code != GenErrImageNotEnabled {
		t.Fatalf("未开通错误码失败: %s", code)
	}
	if code := imageGenerationPublicErrorCode(401, "x", ""); code != GenErrImagePermissionDenied {
		t.Fatalf("权限错误码失败")
	}
	if code := imageGenerationPublicErrorCode(429, "x", ""); code != GenErrImageRateLimited {
		t.Fatalf("限流错误码失败")
	}
	if code := imageGenerationPublicErrorCode(400, "x", ""); code != GenErrImageRequestRejected {
		t.Fatalf("请求拒绝错误码失败")
	}
	if code := imageGenerationPublicErrorCode(500, "x", ""); code != GenErrImageFailed {
		t.Fatalf("默认错误码失败")
	}
	if msg, errType := readImageGenerationErrorPayload(`{"error":{"message":"boom","type":"t"}}`, "fallback"); msg != "boom" || errType != "t" {
		t.Fatalf("错误载荷解析失败: %q %q", msg, errType)
	}
	if msg, _ := readImageGenerationErrorPayload("nope", "fallback"); msg != "fallback" {
		t.Fatalf("回退消息失败")
	}
}

func TestW13BIsAbortError(t *testing.T) {
	if isAbortError(errors.New("plain")) {
		t.Fatalf("普通错误不应识别为中止")
	}
	if isAbortError(&abortLikeW13B{}) {
		t.Fatalf("实现但返回 false 应为 false")
	}
	if !isAbortError(abortTrueW13B{}) {
		t.Fatalf("AbortError 实现应识别")
	}
	if canceledTerm(true) != "canceled" || canceledTerm(false) != "failed" {
		t.Fatalf("canceledTerm 失败")
	}
}

type abortLikeW13B struct{}

func (abortLikeW13B) Error() string     { return "abort" }
func (abortLikeW13B) AbortError() bool  { return false }

type abortTrueW13B struct{}

func (abortTrueW13B) Error() string    { return "abort" }
func (abortTrueW13B) AbortError() bool { return true }

func sha256SumW13B(data []byte) []byte {
	digest := sha256.Sum256(data)
	return digest[:]
}

func TestW13BOrchestratorRunLimits(t *testing.T) {
	registry := newChatInternalToolRegistry("development", true, false)
	tools := registry.resolveTools(true)
	contextValue := &chatToolExecutionContext{OwnerID: "o"}
	published := []ChatToolExecutionEvent{}
	orchestrator := newChatInternalToolOrchestrator(registry, tools, contextValue, ChatOrchestratorLimits{MaxModelRounds: 1, MaxToolCalls: 1, MaxImageCalls: 0}, func(event ChatToolExecutionEvent) {
		published = append(published, event)
	})
	if _, err := orchestrator.Run(ProtocolChatCompletions, func(round int, continuation []any) (ChatToolModelTurn, error) {
		return ChatToolModelTurn{}, errors.New("模型失败")
	}); err == nil {
		t.Fatalf("模型失败应上抛")
	}
	if _, err := orchestrator.Run(ProtocolChatCompletions, func(round int, continuation []any) (ChatToolModelTurn, error) {
		return ChatToolModelTurn{ToolCalls: []ChatToolCall{{CallID: "c1", ToolName: "diagnostic_echo", ArgumentsJSON: `{"text":"hi"}`}}}, nil
	}); err == nil {
		t.Fatalf("超过模型轮次上限应报错")
	}
	abortContext := &chatToolExecutionContext{OwnerID: "o", Aborted: func() bool { return true }}
	abortOrchestrator := newChatInternalToolOrchestrator(registry, tools, abortContext, ChatOrchestratorLimits{MaxModelRounds: 2, MaxToolCalls: 2, MaxImageCalls: 0}, nil)
	if _, err := abortOrchestrator.Run(ProtocolChatCompletions, func(round int, continuation []any) (ChatToolModelTurn, error) {
		return ChatToolModelTurn{}, nil
	}); err == nil || !strings.Contains(err.Error(), "取消") {
		t.Fatalf("中止应报错: %v", err)
	}
	// 工具调用失败一次可纠偏后成功。
	allowContext := &chatToolExecutionContext{OwnerID: "o"}
	count := 0
	failureOrchestrator := newChatInternalToolOrchestrator(registry, tools, allowContext, ChatOrchestratorLimits{MaxModelRounds: 3, MaxToolCalls: 5, MaxImageCalls: 0}, nil)
	result, err := failureOrchestrator.Run(ProtocolChatCompletions, func(round int, continuation []any) (ChatToolModelTurn, error) {
		count++
		if count == 1 {
			return ChatToolModelTurn{ToolCalls: []ChatToolCall{{CallID: "bad", ToolName: "missing_tool", ArgumentsJSON: "{}"}}}, nil
		}
		return ChatToolModelTurn{Content: "done", FinishReason: "stop", InputTokens: int64PtrT(5), OutputTokens: int64PtrT(2)}, nil
	})
	if err != nil || result.Content != "done" || result.ModelRounds != 2 || result.ToolCalls != 1 {
		t.Fatalf("纠偏轮失败: %+v %v", result, err)
	}
	if len(published) != 0 {
		t.Fatalf("不应有事件")
	}
	// 复用缓存 + 次数上限。
	cacheOrchestrator := newChatInternalToolOrchestrator(registry, tools, allowContext, ChatOrchestratorLimits{MaxModelRounds: 4, MaxToolCalls: 8, MaxImageCalls: 0}, nil)
	cacheCount := 0
	cacheResult, err := cacheOrchestrator.Run(ProtocolChatCompletions, func(round int, continuation []any) (ChatToolModelTurn, error) {
		cacheCount++
		if cacheCount == 1 {
			return ChatToolModelTurn{ToolCalls: []ChatToolCall{
				{CallID: "a", ToolName: "diagnostic_echo", ArgumentsJSON: `{"text":"same"}`},
				{CallID: "b", ToolName: "diagnostic_echo", ArgumentsJSON: `{"text":"same"}`},
			}}, nil
		}
		return ChatToolModelTurn{Content: "ok", FinishReason: "stop"}, nil
	})
	if err != nil || cacheResult.ToolCalls != 2 {
		t.Fatalf("复用缓存失败: %+v %v", cacheResult, err)
	}
	// 超过工具调用上限。
	limitOrchestrator := newChatInternalToolOrchestrator(registry, tools, allowContext, ChatOrchestratorLimits{MaxModelRounds: 4, MaxToolCalls: 1, MaxImageCalls: 0}, nil)
	if _, err := limitOrchestrator.Run(ProtocolChatCompletions, func(round int, continuation []any) (ChatToolModelTurn, error) {
		return ChatToolModelTurn{ToolCalls: []ChatToolCall{
			{CallID: "a", ToolName: "diagnostic_echo", ArgumentsJSON: `{"text":"1"}`, SourceOrder: 1},
			{CallID: "b", ToolName: "diagnostic_echo", ArgumentsJSON: `{"text":"2"}`, SourceOrder: 0},
		}}, nil
	}); err == nil {
		t.Fatalf("超过调用上限应报错")
	}
}

func TestW13BStoreGeneratedImageSinkGuards(t *testing.T) {
	sink := &storeGeneratedImageSink{routes: &chatRoutes{deps: &Deps{}}}
	if _, err := sink.CommitGeneratedImage(GeneratedImageCommitInput{}); err == nil {
		t.Fatalf("未配置存储应报错")
	}
}

