package gatewaycodex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestWIDecodeStatePayloadMatrix(t *testing.T) {
	// 合法载荷。
	valid := `{"schemaVersion":2,"responseId":"resp-1","sessionId":"s1","boundary":{"systemAccountId":"sys"},
		"request":{"instructions":"hi","input":[]},"outputItems":[]}`
	if _, err := decodeStatePayload(json.RawMessage(valid)); err != nil {
		t.Fatalf("合法载荷解码失败: %v", err)
	}
	invalid := []string{
		`not-json`,
		`[1,2]`,
		`{"schemaVersion":1,"responseId":"r","sessionId":"s","boundary":{},"request":{},"outputItems":[]}`,
		`{"schemaVersion":2,"sessionId":"s","boundary":{},"request":{},"outputItems":[]}`,
		`{"schemaVersion":2,"responseId":7,"sessionId":"s","boundary":{},"request":{},"outputItems":[]}`,
		`{"schemaVersion":2,"responseId":"r","boundary":{},"request":{},"outputItems":[]}`,
		`{"schemaVersion":2,"responseId":"r","sessionId":"s","request":{},"outputItems":[]}`,
		`{"schemaVersion":2,"responseId":"r","sessionId":"s","boundary":{},"outputItems":[]}`,
		`{"schemaVersion":2,"responseId":"r","sessionId":"s","boundary":{},"request":{}}`,
	}
	for i, raw := range invalid {
		if _, err := decodeStatePayload(json.RawMessage(raw)); err == nil {
			t.Fatalf("第 %d 个非法状态载荷必须报错: %s", i, raw)
		}
	}
}

func TestWIDecodeCompactSnapshotPayloadMatrix(t *testing.T) {
	valid := `{"schemaVersion":2,"compactId":"c1","sessionId":"s1","createdAt":"2026-01-01T00:00:00.000Z",
		"boundary":{"systemAccountId":"sys"},"summary":"sum"}`
	if _, err := decodeCompactSnapshotPayload(json.RawMessage(valid)); err != nil {
		t.Fatalf("合法 snapshot 解码失败: %v", err)
	}
	invalid := []string{
		`not-json`,
		`"text"`,
		`{"schemaVersion":3,"compactId":"c","sessionId":"s","createdAt":"","boundary":{},"summary":"x"}`,
		`{"schemaVersion":2,"sessionId":"s","createdAt":"","boundary":{},"summary":"x"}`,
		`{"schemaVersion":2,"compactId":"c","createdAt":"","boundary":{},"summary":"x"}`,
		`{"schemaVersion":2,"compactId":"c","sessionId":"s","summary":"x"}`,
		`{"schemaVersion":2,"compactId":"c","sessionId":"s","createdAt":"","summary":"x"}`,
		`{"schemaVersion":2,"compactId":"c","sessionId":"s","createdAt":"","boundary":{}}`,
	}
	for i, raw := range invalid {
		if _, err := decodeCompactSnapshotPayload(json.RawMessage(raw)); err == nil {
			t.Fatalf("第 %d 个非法 snapshot 必须报错: %s", i, raw)
		}
	}
}

func TestWICurrentInputFromMaterializedMutation(t *testing.T) {
	state := &CodexResponsesContextRequestState{}
	// 非 internal previous / 无 startIndex → 原样返回。
	materialized := []any{"a", "b", "c"}
	if got := currentInputFromMaterializedMutation(state, materialized); len(got.([]any)) != 3 {
		t.Fatalf("非 internal 必须原样: %v", got)
	}
	state.PreviousResponseKind = PreviousKindInternal
	if got := currentInputFromMaterializedMutation(state, "text"); got != "text" {
		t.Fatalf("非数组输入必须原样: %v", got)
	}
	// internal + startIndex → 从起点克隆。
	start := 1
	state.MaterializedCurrentInputStartIndex = &start
	got := currentInputFromMaterializedMutation(state, materialized)
	items, isArray := got.([]any)
	if !isArray || len(items) != 2 || items[0] != "b" {
		t.Fatalf("起点裁剪失败: %v", got)
	}
	// 克隆是副本：改原数组不影响结果（裁剪起点 1 → 首元素是原第 2 项）。
	materialized[1] = "changed"
	if got.([]any)[0] != "b" {
		t.Fatalf("裁剪结果必须是克隆: %v", got)
	}
	// 越界 clamp。
	big := 9
	state.MaterializedCurrentInputStartIndex = &big
	if got := currentInputFromMaterializedMutation(state, materialized); len(got.([]any)) != 0 {
		t.Fatalf("越界起点必须 clamp 到长度: %v", got)
	}
	negative := -3
	state.MaterializedCurrentInputStartIndex = &negative
	if got := currentInputFromMaterializedMutation(state, materialized); len(got.([]any)) != 3 {
		t.Fatalf("负起点必须 clamp 到 0: %v", got)
	}
}

func TestWIChatBridgeLoggerPlumbing(t *testing.T) {
	service, _, _, _, _ := newBridgeService(t)
	logger := service.Logger.(*recordingLogger)
	service.warn("wi-event", map[string]any{"err": "boom"}, errorsNewForTest("boom"), "boom")
	if len(logger.warnings) != 1 || logger.warnings[0] != "wi-event" {
		t.Fatalf("warn 未记录: %v", logger.warnings)
	}
	service.info("wi-info", nil, "")
	if len(logger.warnings) != 2 {
		t.Fatalf("info 必须经同一 Logger 可观察: %v", logger.warnings)
	}
	// 无 Logger 时安全 no-op。
	quiet := &ChatBridgeStateService{}
	quiet.warn("e", nil, errorsNewForTest("m"), "m")
	quiet.info("e", nil, "")
}

func TestWIAssertRestoredInputSize(t *testing.T) {
	if err := assertRestoredInputSize([]any{"ok"}); err != nil {
		t.Fatalf("正常输入报错: %v", err)
	}
	oversized := make([]any, 0, 100)
	for i := 0; i < 100; i++ {
		oversized = append(oversized, strings.Repeat("x", maxRestoredInputBytes/50))
	}
	if err := assertRestoredInputSize(oversized); err == nil {
		t.Fatal("超限输入必须报错")
	}
}

func TestWINativeResponsesMaterializedConversion(t *testing.T) {
	prefix := codexInlineCompactionSummaryPrefix
	summary := encodeInlineCodexCompactionSummary("压缩摘要")
	if !strings.HasPrefix(summary, prefix) {
		t.Fatalf("编码结果必须带前缀: %q", summary)
	}
	// 往返。
	if got := decodeInlineCodexCompactionSummary(summary); got != "压缩摘要" {
		t.Fatalf("往返摘要=%q", got)
	}
	// compaction 条目转换为 developer message。
	item := jsonRecord{"type": "compaction", "encrypted_content": summary}
	converted, err := nativeResponsesItemFromMaterialized(item)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	record, isObject := converted.(jsonRecord)
	if !isObject || record["role"] != "developer" {
		t.Fatalf("转换结果=%v", converted)
	}
	// 非 compaction 条目原样返回。
	plain := jsonRecord{"type": "message"}
	if got, err := nativeResponsesItemFromMaterialized(plain); err != nil || !jsonRefEqual(got, plain) {
		t.Fatalf("普通条目必须原样: %v err=%v", got, err)
	}
	// 无内部前缀的 compaction 原样返回。
	foreign := jsonRecord{"type": "compaction", "encrypted_content": "other-prefix"}
	if got, err := nativeResponsesItemFromMaterialized(foreign); err != nil || !jsonRefEqual(got, foreign) {
		t.Fatalf("外部 compaction 必须原样: %v err=%v", got, err)
	}
	// 坏摘要必须报错（禁止把不可解析内容发往上游）。
	broken := jsonRecord{"type": "compaction_summary", "encrypted_content": prefix + "%%%bad"}
	if _, err := nativeResponsesItemFromMaterialized(broken); err == nil {
		t.Fatal("坏摘要必须报错")
	}
	// 列表级转换：无变化时原样返回。
	unchanged := []any{plain, "scalar"}
	if got, err := nativeResponsesInputFromMaterialized(unchanged); err != nil || len(got.([]any)) != 2 {
		t.Fatalf("无变化列表=%v err=%v", got, err)
	}
	changed, err := nativeResponsesInputFromMaterialized([]any{item})
	if err != nil || !jsonRefNotEqual(changed, []any{item}) {
		t.Fatalf("有变化列表必须新建: %v err=%v", changed, err)
	}
	// 坏条目在列表级传播错误。
	if _, err := nativeResponsesInputFromMaterialized([]any{broken}); err == nil {
		t.Fatal("列表中的坏条目必须传播错误")
	}
	// 非数组原样返回。
	if got, err := nativeResponsesInputFromMaterialized("text"); err != nil || got != "text" {
		t.Fatalf("非数组=%v err=%v", got, err)
	}
}

func TestWIUpsertPostgresRowHelpersOverSQLite(t *testing.T) {
	handle := wiOpenSQLiteHandle(t, "wi-codex-pg-upsert?mode=memory&cache=shared")
	db := handle.DB()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("打开事务失败: %v", err)
	}
	defer tx.Rollback() //nolint
	responseRow := CodexContextResponseStateIndex{ResponseID: "resp-1", SessionID: "s1"}
	if err := upsertResponseStateRowPostgres(ctx, tx, &responseRow); err == nil {
		t.Fatal("postgres 响应行 upsert 必须报错（schema 缺失）")
	}
	compactRow := CodexContextCompactStateIndex{CompactID: "c1", SessionID: "s1"}
	if err := upsertCompactStateRowPostgres(ctx, tx, &compactRow); err == nil {
		t.Fatal("postgres compact 行 upsert 必须报错（schema 缺失）")
	}
}

func TestWICompactTerminalHelpers(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	// 取消与中断的 terminal 分类矩阵。
	if got := terminalOutcomeClass(cancelled, nil); got != "client_cancellation" {
		t.Fatalf("取消分类=%q", got)
	}
	if got := terminalOutcomeClass(context.Background(), nil); got != "read_interruption" {
		t.Fatalf("中断分类=%q", got)
	}
	if got := terminalFailureScope(cancelled, nil); got != "none" {
		t.Fatalf("取消 scope=%q", got)
	}
	if got := terminalFailureScope(context.Background(), nil); got != "protocol_model" {
		t.Fatalf("中断 scope=%q", got)
	}
	if got := terminalSource(cancelled, nil); got != "request_lifecycle" {
		t.Fatalf("取消 source=%q", got)
	}
	if got := terminalSource(context.Background(), nil); got != "gateway_transport" {
		t.Fatalf("中断 source=%q", got)
	}
	// recordExchangeTerminal：nil exchange / nil 回调安全。
	service := &CompactPreflightService{}
	service.recordExchangeTerminal(nil, CompactPreflightInput{}, CompactQualityTerminal{})
	called := false
	exchange := &CompactUpstreamExchange{RecordTerminal: func(terminal CompactQualityTerminal) { called = true }}
	service.recordExchangeTerminal(exchange, CompactPreflightInput{}, CompactQualityTerminal{})
	if !called {
		t.Fatal("terminal 回调必须触发")
	}
	service.releaseExchange(nil)
	released := false
	exchange2 := &CompactUpstreamExchange{ReleaseConcurrency: func() { released = true }}
	service.releaseExchange(exchange2)
	if !released {
		t.Fatal("并发释放必须触发")
	}
}

func TestWIRestoreFailureForCompactCopy(t *testing.T) {
	if failure := restoreFailureForCompact(RestoreOutcomeBoundaryMismatch); failure.statusCode != 403 {
		t.Fatalf("边界=%+v", failure)
	}
	if failure := restoreFailureForCompact(RestoreOutcomeChainTooDeep); failure.statusCode != 413 {
		t.Fatalf("链深=%+v", failure)
	}
	if failure := restoreFailureForCompact("unknown"); failure.statusCode != 404 || failure.code != "codex_bridge_compact_context_not_found" {
		t.Fatalf("默认=%+v", failure)
	}
}

func TestWIResolveGatewayUsageModel(t *testing.T) {
	// 空 model 直通空。
	if got := ResolveGatewayUsageModel(gatewayruntimecache.OpenAIAccountSecret{}, "", "chat_completions"); got != "" {
		t.Fatalf("空 model=%q", got)
	}
	// 无映射 → 请求模型直通。
	if got := ResolveGatewayUsageModel(gatewayruntimecache.OpenAIAccountSecret{}, "gpt-5", "chat_completions"); got != "gpt-5" {
		t.Fatalf("直通=%q", got)
	}
	// 有映射 → 上游模型。
	account := gatewayruntimecache.OpenAIAccountSecret{
		// responses → chat_completions 的跨协议转换要求 openai 协议 profile。
		ProviderCode:    "openai",
		ProtocolCode:    "openai",
		ProtocolVersion: "v1",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel: "gpt-5", UpstreamModel: "up-model",
			SourceEndpointFamily: "responses", UpstreamEndpointFamily: "chat_completions",
			Enabled: true,
		}},
	}
	if got := ResolveGatewayUsageModel(account, "gpt-5", "responses"); got != "up-model" {
		t.Fatalf("映射=%q", got)
	}
	// 非响应族的映射不生效 → 直通。
	if got := ResolveGatewayUsageModel(account, "gpt-5", "chat_completions"); got != "gpt-5" {
		t.Fatalf("无匹配映射应直通=%q", got)
	}
}

func TestWINowOrWallAndSliceCycleKey(t *testing.T) {
	if !nowOrWall(nil).After(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("nil clock 回落墙上时间")
	}
	base := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	if got := nowOrWall(newFakeClock(base)); !got.Equal(base) {
		t.Fatalf("nowOrWall=%v", got)
	}
	// 空切片 key 为 0；非空切片按指针。
	if sliceCycleKey(nil) != 0 || sliceCycleKey([]any{}) != 0 {
		t.Fatal("空 slice key 必须 0")
	}
	value := []any{1}
	if sliceCycleKey(value) == 0 {
		t.Fatal("非空 slice key 不得为 0")
	}
}

func TestWIParseGatewayJSONObjectFallbacks(t *testing.T) {
	service, _, _, _, _ := newBridgeService(t)
	// 已解析体优先（Body.State 已带解析结果）。
	parsed := newTestRequest(t, "POST", "/v1/responses", []byte(`{"input":"x"}`), nil)
	attachBody(parsed, map[string]any{"input": "x"}, []byte(`{"input":"x"}`))
	record, err := service.parseGatewayJSONObject(parsed)
	if err != nil || record["input"] != "x" {
		t.Fatalf("解析体=%v err=%v", record, err)
	}
	// 无 body → 空对象。
	empty := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	record, err = service.parseGatewayJSONObject(empty)
	if err != nil || len(record) != 0 {
		t.Fatalf("空体=%v err=%v", record, err)
	}
	// 原始体回落解码（Body.State 未解析）。
	rawOnly := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	rawOnly.Body = &gatewaybody.Request{RawBody: []byte(`{"input":"raw"}`)}
	record, err = service.parseGatewayJSONObject(rawOnly)
	if err != nil || record["input"] != "raw" {
		t.Fatalf("原始体回落=%v err=%v", record, err)
	}
	// 非对象 JSON → 空对象。
	array := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	array.Body = &gatewaybody.Request{RawBody: []byte(`[1,2]`)}
	record, err = service.parseGatewayJSONObject(array)
	if err != nil || len(record) != 0 {
		t.Fatalf("非对象=%v err=%v", record, err)
	}
	// 非法 JSON → 空对象（不报错）。
	invalid := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	invalid.Body = &gatewaybody.Request{RawBody: []byte(`nope`)}
	record, err = service.parseGatewayJSONObject(invalid)
	if err != nil || len(record) != 0 {
		t.Fatalf("非法 JSON=%v err=%v", record, err)
	}
	// 通过公开入口一致性校验。
	if public, err := service.ParseGatewayJSONObjectPublic(parsed); err != nil || public["input"] != "x" {
		t.Fatalf("公开入口=%v err=%v", public, err)
	}
}

func errorsNewForTest(message string) error { return &wiTestError{message} }

type wiTestError struct{ message string }

func (e *wiTestError) Error() string { return e.message }
