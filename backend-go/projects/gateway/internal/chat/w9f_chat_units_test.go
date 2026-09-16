package chat

// w9f 覆盖收尾（第一批）：直驱包内小函数与解析边界，生产逻辑零改动。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// routes.go：进行中动作/准备的互斥原语
// ---------------------------------------------------------------------------

func TestW9FActivePreparationAbortOnce(t *testing.T) {
	prep := &activePreparation{token: 1, ownerID: "o", clientMessageID: "c", phase: "preparing"}
	if !prep.abort() {
		t.Fatal("first abort must win")
	}
	if prep.abort() {
		t.Fatal("second abort must be a no-op")
	}
	if !prep.isCanceled() {
		t.Fatal("isCanceled after abort")
	}
}

func TestW9FChatRoutesActionAndPreparationPrimitives(t *testing.T) {
	deps := &Deps{}
	rt := newChatRoutesForTest(deps)
	if got := rt.now(); got == "" {
		t.Fatal("now with nil Now must fall back to time.Now")
	}
	// 动作：claim/互斥/按 token 删除。
	if rt.claimAction("conv", "o1", "clearing") == nil {
		t.Fatal("first claim must succeed")
	}
	if rt.claimAction("conv", "o2", "compacting") != nil {
		t.Fatal("second claim must be rejected")
	}
	if rt.claimAction("conv2", "o1", "clearing") == nil {
		t.Fatal("other conversation must claim")
	}
	// 有 prep 时动作不可 claim。
	rt.claimPreparation("conv3", "o1", "cmid")
	if rt.claimAction("conv3", "o1", "clearing") != nil {
		t.Fatal("action claim with live prep must fail")
	}
	if rt.getAction("conv", "other") != nil {
		t.Fatal("foreign owner must not see action")
	}
	if rt.getAction("missing", "o1") != nil {
		t.Fatal("missing action")
	}
	action := rt.getAction("conv", "o1")
	rt.deleteActionIfMatches("conv", action.token+123)
	if rt.getAction("conv", "o1") == nil {
		t.Fatal("wrong token must not delete")
	}
	rt.deleteActionIfMatches("conv", action.token)
	if rt.getAction("conv", "o1") != nil {
		t.Fatal("delete by token")
	}
	// 准备：claim/互斥/取消。
	prep := rt.claimPreparation("conv4", "o1", "cmid-1")
	if prep == nil {
		t.Fatal("prep claim")
	}
	if rt.claimPreparation("conv4", "o1", "cmid-2") != nil {
		t.Fatal("second prep claim must fail")
	}
	if rt.claimAction("conv4", "o1", "clearing") != nil {
		t.Fatal("action claim with prep must fail")
	}
	if rt.getPreparationForConversation("conv4", "other") != nil {
		t.Fatal("foreign owner prep")
	}
	if rt.getPreparation("conv4", "o1", "other-cmid") != nil {
		t.Fatal("cmid mismatch prep")
	}
	if rt.getPreparation("conv4", "o1", "cmid-1") == nil {
		t.Fatal("prep lookup")
	}
	// cancelPreparation：先占用一次 abort（第二次调用返回 false）。
	_ = prep.abort()
	if _, ok := rt.cancelPreparation("conv4", "o1", "cmid-1"); ok {
		t.Fatal("cancel on canceled prep must fail")
	}
	prep2 := rt.claimPreparation("conv5", "o1", "cmid-9")
	phase, ok := rt.cancelPreparation("conv5", "o1", "cmid-9")
	if !ok || phase != "preparing" {
		t.Fatalf("cancel = %q/%v", phase, ok)
	}
	_ = prep2
	if _, ok := rt.cancelPreparation("missing", "o1", "x"); ok {
		t.Fatal("cancel missing prep")
	}
	rt.deletePreparationIfMatches("conv5", prep2.token+5)
	if rt.getPreparationForConversation("conv5", "o1") == nil {
		t.Fatal("wrong token delete must no-op")
	}
	rt.deletePreparationIfMatches("conv5", prep2.token)
	if rt.getPreparationForConversation("conv5", "o1") != nil {
		t.Fatal("prep delete by token")
	}
	// beginAcceptance：phase 前置与取消检查。
	prep3 := rt.claimPreparation("conv6", "o1", "c6")
	if !rt.beginAcceptance("conv6", prep3) {
		t.Fatal("begin acceptance")
	}
	if rt.beginAcceptance("conv6", prep3) {
		t.Fatal("double begin must fail")
	}
	other := rt.claimPreparation("conv7", "o1", "c7")
	_ = other.abort()
	if rt.beginAcceptance("conv7", other) {
		t.Fatal("begin on canceled prep must fail")
	}
}

func TestW9FRequireChatAuthFallsBackToError(t *testing.T) {
	rt := newChatRoutesForTest(&Deps{})
	request := httptest.NewRequest("GET", "/x", nil)
	if _, err := rt.requireChatAuth(request); err == nil {
		t.Fatal("missing auth must error")
	}
}

func TestW9FReadJSONBodyEdges(t *testing.T) {
	// 读失败。
	broken := &brokenBodyReader{}
	if _, err := readJSONBody(httptest.NewRequest("POST", "/x", broken)); err == nil {
		t.Fatal("read error must surface")
	}
	// 超 24MiB。
	huge := strings.Repeat("a", chatSystemAPIJSONBodyLimit+16)
	if _, err := readJSONBody(httptest.NewRequest("POST", "/x", strings.NewReader(huge))); err == nil {
		t.Fatal("oversize body must fail")
	}
	// 空 body → {}。
	raw, err := readJSONBody(httptest.NewRequest("POST", "/x", strings.NewReader("   ")))
	if err != nil || string(raw) != "{}" {
		t.Fatalf("empty body = %s/%v", raw, err)
	}
	// 非法 JSON。
	if _, err := readJSONBody(httptest.NewRequest("POST", "/x", strings.NewReader("{bad"))); err == nil {
		t.Fatal("invalid JSON must fail")
	}
}

type brokenBodyReader struct{}

func (brokenBodyReader) Read([]byte) (int, error) { return 0, errors.New("boom") }
func (brokenBodyReader) Close() error             { return nil }

var _ io.Reader = brokenBodyReader{}

func TestW9FDecodeObjectBodyAndValueTypes(t *testing.T) {
	if _, err := decodeObjectBody(json.RawMessage(`[1,2]`)); err == nil {
		t.Fatal("array body must fail")
	}
	if _, err := decodeObjectBody(json.RawMessage(`null`)); err == nil {
		t.Fatal("null body must fail")
	}
	if got := jsonValueTypeName(json.RawMessage(``)); got != "undefined" {
		t.Fatalf("empty = %s", got)
	}
	for raw, want := range map[string]string{
		`{"a":1}`: "object", `[1]`: "array", `"x"`: "string",
		"true": "boolean", "false": "boolean", "null": "null", "12.5": "number",
	} {
		if got := jsonValueTypeName(json.RawMessage(raw)); got != want {
			t.Fatalf("jsonValueTypeName(%s) = %s, want %s", raw, got, want)
		}
	}
}

func TestW9FBoundedTrimmedString(t *testing.T) {
	if _, err := boundedTrimmedString(json.RawMessage(`5`), 10); err == nil {
		t.Fatal("non-string must fail")
	}
	if _, err := boundedTrimmedString(json.RawMessage(`"   "`), 10); err == nil {
		t.Fatal("blank must fail")
	}
	if _, err := boundedTrimmedString(json.RawMessage(`"`+strings.Repeat("字", 11)+`"`), 10); err == nil {
		t.Fatal("too long must fail")
	}
	value, err := boundedTrimmedString(json.RawMessage(`"  ok  "`), 10)
	if err != nil || value == nil || *value != "ok" {
		t.Fatalf("ok = %v/%v", value, err)
	}
}

func TestW9FQueryHelpers(t *testing.T) {
	if got := integerQuery("notnum", 30, 1, 50); got != 30 {
		t.Fatalf("invalid fallback = %d", got)
	}
	if got := integerQuery("0", 30, 1, 50); got != 30 {
		t.Fatalf("zero fallback = %d", got)
	}
	if got := integerQuery("500", 30, 1, 50); got != 50 {
		t.Fatalf("max clamp = %d", got)
	}
	if got := integerQuery(" 7 ", 30, 10, 50); got != 10 {
		t.Fatalf("min clamp = %d", got)
	}
	if got := integerQuery("20", 30, 1, 50); got != 20 {
		t.Fatalf("passthrough = %d", got)
	}
	if textQuery("  ") != nil {
		t.Fatal("blank text query")
	}
	if optionalBooleanQuery("yes") != nil {
		t.Fatal("invalid boolean")
	}
	if value := optionalBooleanQuery("TRUE"); value == nil || !*value {
		t.Fatal("true boolean")
	}
	if value := optionalBooleanQuery("0"); value == nil || *value {
		t.Fatal("false boolean")
	}
	if err := ensureStrictQueryKeys(map[string][]string{"a": {"1"}}, "b"); err == nil {
		t.Fatal("unknown key must fail")
	}
	if err := ensureStrictQueryKeys(map[string][]string{"b": {"1"}}, "b"); err != nil {
		t.Fatalf("allowed key: %v", err)
	}
	if _, ok, err := queryScalarInteger(map[string][]string{"page": {}}, "page"); ok || err != nil {
		t.Fatalf("empty values = %v/%v", ok, err)
	}
	if _, ok, _ := queryScalarInteger(map[string][]string{"page": {""}}, "page"); ok {
		t.Fatal("blank value is absent")
	}
	if _, _, err := queryScalarInteger(map[string][]string{"page": {"x"}}, "page"); err == nil {
		t.Fatal("non-number must fail")
	}
	if value, ok, _ := queryScalarInteger(map[string][]string{"page": {" 3 "}}, "page"); !ok || value != 3 {
		t.Fatalf("page = %d/%v", value, ok)
	}
}

func TestW9FTrimmedPointer(t *testing.T) {
	if trimmedPointer(nil) != nil {
		t.Fatal("nil pointer")
	}
	blank := "   "
	if trimmedPointer(&blank) != nil {
		t.Fatal("blank pointer")
	}
	value := " x "
	if got := trimmedPointer(&value); got == nil || *got != "x" {
		t.Fatalf("trimmed = %v", got)
	}
}

// ---------------------------------------------------------------------------
// stream_route.go：请求体解析与模型选项解析
// ---------------------------------------------------------------------------

func mustParseBody(t *testing.T, raw string) map[string]json.RawMessage {
	t.Helper()
	parsed, err := decodeObjectBody(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return parsed
}

func TestW9FParseStreamMessageBodyErrors(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"字符串字段类型错误", `{"content":5}`, "Expected string"},
		{"clientMessageId 缺失", `{"content":"hi","model":"m"}`, "at least 1 character"},
		{"content 缺失", `{"clientMessageId":"c","model":"m"}`, "请输入消息"},
		{"model 缺失", `{"clientMessageId":"c","content":"hi"}`, "请选择模型"},
		{"replaceTurnId 空白跳过", `{"clientMessageId":"c","content":"hi","model":"m","replaceTurnId":"  "}`, ""},
		{"content 过长", `{"clientMessageId":"c","content":"` + strings.Repeat("字", 196609) + `","model":"m"}`, "消息内容过长"},
		{"clientMessageId 过长", `{"clientMessageId":"` + strings.Repeat("c", 101) + `","content":"hi","model":"m"}`, "at most 100"},
		{"model 过长", `{"clientMessageId":"c","content":"hi","model":"` + strings.Repeat("m", 201) + `"}`, "at most 200"},
		{"思考级别非法", `{"clientMessageId":"c","content":"hi","model":"m","reasoningEffort":"ultra"}`, "Invalid enum value"},
		{"服务等级非法", `{"clientMessageId":"c","content":"hi","model":"m","serviceTier":"turbo"}`, "Invalid enum value"},
		{"未知键", `{"clientMessageId":"c","content":"hi","model":"m","bogus":1}`, "Unrecognized key"},
		{"contentBlocks 非数组", `{"clientMessageId":"c","content":"hi","model":"m","contentBlocks":{}}`, "Expected array"},
		{"contentBlocks 超长", `{"clientMessageId":"c","content":"hi","model":"m","contentBlocks":[` + strings.Repeat(`{"type":"input_text","text":"x"},`, 12) + `{"type":"input_text","text":"x"}]}`, "at most 11"},
		{"块类型未知", `{"clientMessageId":"c","content":"hi","model":"m","contentBlocks":[{"type":"audio"}]}`, "Invalid input"},
		{"文本块缺 text", `{"clientMessageId":"c","content":"hi","model":"m","contentBlocks":[{"type":"input_text"}]}`, "Required"},
		{"文本块 text 非字符串", `{"clientMessageId":"c","content":"hi","model":"m","contentBlocks":[{"type":"input_text","text":5}]}`, "Expected string"},
		{"图片块缺 assetId", `{"clientMessageId":"c","content":"hi","model":"m","contentBlocks":[{"type":"input_image"}]}`, "Required"},
		{"图片块 assetId 非字符串", `{"clientMessageId":"c","content":"hi","model":"m","contentBlocks":[{"type":"input_image","assetId":5}]}`, "Expected string"},
		{"图片块 assetId 空白", `{"clientMessageId":"c","content":"hi","model":"m","contentBlocks":[{"type":"input_image","assetId":"  "}]}`, "不能为空"},
		{"图片块 assetId 过长", `{"clientMessageId":"c","content":"hi","model":"m","contentBlocks":[{"type":"input_image","assetId":"` + strings.Repeat("a", 121) + `"}]}`, "at most 120"},
		{"块未知键", `{"clientMessageId":"c","content":"hi","model":"m","contentBlocks":[{"type":"input_text","text":"x","extra":1}]}`, "Unrecognized key"},
		{"图片重复引用", `{"clientMessageId":"c","content":"hi","model":"m","contentBlocks":[{"type":"input_image","assetId":"a1"},{"type":"input_image","assetId":"a1"}]}`, "重复引用"},
		{"generationParameters 非对象", `{"clientMessageId":"c","content":"hi","model":"m","generationParameters":[]}`, "Expected object"},
		{"generationParameters 值非数字", `{"clientMessageId":"c","content":"hi","model":"m","generationParameters":{"temperature":"x"}}`, "Expected number"},
		{"maxOutputTokens 非整数", `{"clientMessageId":"c","content":"hi","model":"m","generationParameters":{"maxOutputTokens":1.5}}`, "Expected int"},
		{"seed 非整数", `{"clientMessageId":"c","content":"hi","model":"m","generationParameters":{"seed":2.5}}`, "Expected int"},
		{"generationParameters 未知键", `{"clientMessageId":"c","content":"hi","model":"m","generationParameters":{"bogus":1}}`, "Unrecognized key"},
	}
	for _, item := range cases {
		_, err := parseStreamMessageBody(mustParseBody(t, item.raw))
		if item.want == "" {
			if err != nil {
				t.Fatalf("%s: unexpected err %v", item.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), item.want) {
			t.Fatalf("%s: err = %v, want contains %q", item.name, err, item.want)
		}
	}
	// >5 张图片。
	blocks := []string{}
	for i := 0; i < 6; i++ {
		blocks = append(blocks, `{"type":"input_image","assetId":"a`+string(rune('a'+i))+`"}`)
	}
	raw := `{"clientMessageId":"c","content":"hi","model":"m","contentBlocks":[` + strings.Join(blocks, ",") + `]}`
	if _, err := parseStreamMessageBody(mustParseBody(t, raw)); err == nil || !strings.Contains(err.Error(), "5 张") {
		t.Fatalf("image count err = %v", err)
	}
	// 文本块超长。
	long := `{"clientMessageId":"c","content":"hi","model":"m","contentBlocks":[{"type":"input_text","text":"` + strings.Repeat("字", 196609) + `"}]}`
	if _, err := parseStreamMessageBody(mustParseBody(t, long)); err == nil || !strings.Contains(err.Error(), "过长") {
		t.Fatalf("long text block err = %v", err)
	}
	// 合法 body 完整字段。
	body, err := parseStreamMessageBody(mustParseBody(t, `{"clientMessageId":"c","replaceTurnId":"r","content":"hi","model":"m","reasoningEffort":"low","serviceTier":"priority","generationParameters":{"temperature":0.5,"maxOutputTokens":10,"seed":3}}`))
	if err != nil {
		t.Fatalf("valid body: %v", err)
	}
	if body.ReplaceTurnID != "r" || body.ReasoningEffort != "low" || body.ServiceTier != "priority" ||
		body.GenerationParameters == nil || body.GenerationParameters.Temperature == nil {
		t.Fatalf("valid body = %+v", body)
	}
}

func w9fModelOptionPtr() *ChatModelOption {
	return &ChatModelOption{
		SupportedReasoningEfforts: []string{"low", "high"},
		SupportedServiceTiers:     []string{"priority"},
		GenerationParameters: []ChatGenerationParameterCapability{
			{Parameter: "temperature", Min: 0, Max: 2},
			{Parameter: "seed", Min: 0, Max: 100},
		},
		MaxInputTokens: int64PtrT(1234),
	}
}

func TestW9FResolveChatModelRequestOptions(t *testing.T) {
	rt := newChatRoutesForTest(&Deps{})
	base := func() *streamMessageBody { return &streamMessageBody{ReasoningEffort: "low"} }
	if _, _, _, _, err := resolveChatModelRequestOptions(rt, &ChatModelOption{}, base()); err == nil {
		t.Fatal("unsupported effort must fail")
	}
	tierBody := &streamMessageBody{ServiceTier: "flex"}
	if _, _, _, _, err := resolveChatModelRequestOptions(rt, &ChatModelOption{}, tierBody); err == nil {
		t.Fatal("unsupported tier must fail")
	}
	bothBody := &streamMessageBody{GenerationParameters: &ChatGenerationParameters{Temperature: floatPtr(0.5), TopP: floatPtr(0.9)}}
	if _, _, _, _, err := resolveChatModelRequestOptions(rt, w9fModelOptionPtr(), bothBody); err == nil {
		t.Fatal("temperature+topP must fail")
	}
	unknownBody := &streamMessageBody{GenerationParameters: &ChatGenerationParameters{FrequencyPenalty: floatPtr(1)}}
	if _, _, _, _, err := resolveChatModelRequestOptions(rt, w9fModelOptionPtr(), unknownBody); err == nil {
		t.Fatal("unknown parameter must fail")
	}
	rangeBody := &streamMessageBody{GenerationParameters: &ChatGenerationParameters{Temperature: floatPtr(5)}}
	if _, _, _, _, err := resolveChatModelRequestOptions(rt, w9fModelOptionPtr(), rangeBody); err == nil {
		t.Fatal("out-of-range parameter must fail")
	}
	nonIntBody := &streamMessageBody{GenerationParameters: &ChatGenerationParameters{Seed: floatPtr(1.5)}}
	if _, _, _, _, err := resolveChatModelRequestOptions(rt, w9fModelOptionPtr(), nonIntBody); err == nil {
		t.Fatal("non-integer seed must fail")
	}
	effort, tier, params, maxInput, err := resolveChatModelRequestOptions(rt, w9fModelOptionPtr(), &streamMessageBody{
		ReasoningEffort: "low", ServiceTier: "priority",
		GenerationParameters: &ChatGenerationParameters{Temperature: floatPtr(1)},
	})
	if err != nil || effort != "low" || tier != "priority" || params == nil || maxInput == nil || *maxInput != 1234 {
		t.Fatalf("ok = %s/%s/%+v/%v/%v", effort, tier, params, maxInput, err)
	}
	// 默认参数对象兜底。
	_, _, params2, maxInput2, err := resolveChatModelRequestOptions(rt, &ChatModelOption{MaxInputTokens: int64PtrT(7)}, &streamMessageBody{})
	if err != nil || params2 == nil || maxInput2 == nil || *maxInput2 != 7 {
		t.Fatalf("default = %+v/%v/%v", params2, maxInput2, err)
	}
}

func floatPtr(value float64) *float64 { return &value }

// ---------------------------------------------------------------------------
// turns.go：消息行映射的时间戳校验分支
// ---------------------------------------------------------------------------

func TestW9FMapMessageTimestampValidation(t *testing.T) {
	if _, err := mapMessage(messageRow{createdAt: "not-a-time", expiresAt: "2026-01-01T00:00:00Z"}); err == nil {
		t.Fatal("invalid created_at must fail")
	}
	if _, err := mapMessage(messageRow{createdAt: "2026-01-01T00:00:00Z", expiresAt: "bogus"}); err == nil {
		t.Fatal("invalid expires_at must fail")
	}
	if _, err := mapMessage(messageRow{
		createdAt: "2026-01-01T00:00:00Z", expiresAt: "2026-01-02T00:00:00Z",
		completedAt: sql.NullString{String: "nope", Valid: true},
	}); err == nil {
		t.Fatal("invalid completed_at must fail")
	}
	message, err := mapMessage(messageRow{
		createdAt: "2026-01-01T00:00:00Z", expiresAt: "2026-01-02T00:00:00Z",
		completedAt: sql.NullString{String: "2026-01-01T01:00:00Z", Valid: true},
	})
	if err != nil || message == nil || message.CompletedAt == nil {
		t.Fatalf("ok message = %+v/%v", message, err)
	}
}

func nullStringOf(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }

// ---------------------------------------------------------------------------
// store.go：构造器与小工具
// ---------------------------------------------------------------------------

func TestW9FChatStoreConstructorsAndHelpers(t *testing.T) {
	if _, err := NewStore(nil, false, nil, nil); err == nil {
		t.Fatal("nil db must fail")
	}
	fixture := newChatFixture(t)
	if fixture.store.DB() == nil {
		t.Fatal("DB() handle")
	}
	if ensureCtx(nil) != context.Background() {
		t.Fatal("nil ctx fallback")
	}
	if ensureCtx(context.Background()) == nil {
		t.Fatal("ctx passthrough")
	}
	if fixture.store.nowTime().IsZero() {
		t.Fatal("nowTime")
	}
	// NewStore 的默认时钟与 id 生成。
	bare, err := NewStore(fixture.store.DB(), false, nil, nil)
	if err != nil || bare.nowTime().IsZero() || bare.nowISO() == "" {
		t.Fatalf("bare store = %v", err)
	}
}

// ---------------------------------------------------------------------------
// 错误写入器：writeStreamRouteError 的分类分支
// ---------------------------------------------------------------------------

func TestW9FWriteStreamRouteErrorClassifications(t *testing.T) {
	check := func(err error, wantStatus int, wantCode string) {
		t.Helper()
		recorder := httptest.NewRecorder()
		writeStreamRouteError(recorder, err)
		if recorder.Code != wantStatus {
			t.Fatalf("%v: status = %d, want %d", err, recorder.Code, wantStatus)
		}
		var payload messageCodePayload
		_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
		if payload.Code != wantCode {
			t.Fatalf("%v: code = %s, want %s", err, payload.Code, wantCode)
		}
	}
	check(&PreparationCanceledError{}, 499, "chat_preparation_canceled")
	check(&ConflictError{Code: ConflictMessageInProgress}, 409, "chat_message_in_progress")
	check(&ContextBudgetError{}, 422, "chat_input_exceeds_context")
	check(&RequestError{Code: RequestImageNotSupported}, 422, "chat_image_not_supported")
	check(&ChatAssetInputError{Message: "x"}, 422, "chat_asset_unavailable")
	check(&ModelCapabilityError{Message: "x"}, 422, "chat_model_capability_unavailable")
	check(errors.New("boom"), 500, "internal_generation_failed")
}

// ---------------------------------------------------------------------------
// 时间工具
// ---------------------------------------------------------------------------

func TestW9FIsoMillisShape(t *testing.T) {
	stamp := isoMillis(time.Date(2026, 9, 4, 12, 30, 5, int(250*time.Millisecond), time.UTC))
	if stamp != "2026-09-04T12:30:05.250Z" {
		t.Fatalf("isoMillis = %q", stamp)
	}
}
