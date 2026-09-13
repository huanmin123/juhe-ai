package gatewaycodex

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestWIReadCompactionSnapshotSummaryRoundTrip(t *testing.T) {
	service, _, _, _, clock := newBridgeService(t)
	ctx := context.Background()
	boundary := CodexContextStateBoundary{SystemAccountID: "sys", GroupID: "group", ProviderCode: "openai"}
	summary := "会话压缩摘要"
	created, err := service.CreateChatBridgeCompactSnapshot(ctx, CreateChatBridgeCompactSnapshotInput{
		SessionID:         "session-1",
		SourceResponseID:  "resp-1",
		Boundary:          boundary,
		Summary:           summary,
		UpstreamAccountID: "acc-1",
		Model:             "gpt-5",
		UpstreamModel:     "gpt-5",
		CreatedAt:         timePtr(clock.Now()),
	})
	if err != nil || created.CompactID == "" {
		t.Fatalf("创建快照失败: %+v err=%v", created, err)
	}
	digest := digestText(summary)
	result, err := service.readCodexCompactionSnapshotSummary(ctx, created.CompactID, digest, boundary)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if result.outcome != CodexContextOutcomeFound || result.summary != summary {
		t.Fatalf("读取=%+v", result)
	}
	// digest 不匹配 → payload 不可用。
	result, err = service.readCodexCompactionSnapshotSummary(ctx, created.CompactID, "bad-digest", boundary)
	if err != nil || result.outcome != RestoreOutcomePayloadUnavail {
		t.Fatalf("digest 不匹配=%+v err=%v", result, err)
	}
	// compactID 不存在 → not found。
	result, err = service.readCodexCompactionSnapshotSummary(ctx, "cmp_missing", digest, boundary)
	if err != nil || result.outcome == CodexContextOutcomeFound {
		t.Fatalf("缺失快照=%+v err=%v", result, err)
	}
	// SessionID 缺省时回落 SourceResponseID（创建入口的回退分支）。
	fallback, err := service.CreateChatBridgeCompactSnapshot(ctx, CreateChatBridgeCompactSnapshotInput{
		SourceResponseID: "resp-src", Boundary: boundary, Summary: "s2",
		CreatedAt: timePtr(clock.Now()),
	})
	if err != nil || fallback.CompactID == "" || fallback.EncryptedContent == "" {
		t.Fatalf("SessionID 回落创建=%+v err=%v", fallback, err)
	}
	// 两者都缺 → 会话段写入仍成功（compactID 充当会话键），快照可再次读取。
	fallback2, err := service.CreateChatBridgeCompactSnapshot(ctx, CreateChatBridgeCompactSnapshotInput{
		Boundary: boundary, Summary: "s3", CreatedAt: timePtr(clock.Now()),
	})
	if err != nil || fallback2.CompactID == "" || fallback2.EncryptedContent == "" {
		t.Fatalf("无会话创建=%+v err=%v", fallback2, err)
	}
}

func timePtr(value time.Time) *time.Time { return &value }

func TestWIValidateAndClampContextStoreHelpers(t *testing.T) {
	// validateResponseStateIndex 守卫。
	if err := validateResponseStateIndex(&CodexContextResponseStateIndex{ResponseID: " ", SessionID: "s"}); err == nil {
		t.Fatal("空 responseId 必须报错")
	}
	if err := validateResponseStateIndex(&CodexContextResponseStateIndex{ResponseID: "r", SessionID: " "}); err == nil {
		t.Fatal("空 sessionId 必须报错")
	}
	if err := validateResponseStateIndex(&CodexContextResponseStateIndex{ResponseID: "r", SessionID: "s"}); err != nil {
		t.Fatalf("合法行报错: %v", err)
	}
	// 保存入口的守卫分支。
	store, _ := newSQLiteStore(t)
	if err := SaveCodexContextCompactStateIndex(context.Background(), store, CodexContextCompactStateIndex{CompactID: " "}); err == nil {
		t.Fatal("空 compactId 必须报错")
	}
	if err := SaveCodexContextCompactStateIndex(context.Background(), store, CodexContextCompactStateIndex{CompactID: "c", SessionID: " "}); err == nil {
		t.Fatal("空 sessionId 必须报错")
	}
	if err := SaveCodexContextResponseStateIndex(context.Background(), store, CodexContextResponseStateIndex{}); err == nil {
		t.Fatal("空响应索引必须报错")
	}
	// clampChainDepth：0→64、负→1、超限→256、普通直通。
	if got := clampChainDepth(0); got != 64 {
		t.Fatalf("0→%d", got)
	}
	if got := clampChainDepth(-5); got != 1 {
		t.Fatalf("负→%d", got)
	}
	if got := clampChainDepth(9999); got != 256 {
		t.Fatalf("超限→%d", got)
	}
	if got := clampChainDepth(10); got != 10 {
		t.Fatalf("普通→%d", got)
	}
}

func TestWIShardCountAndRootGuard(t *testing.T) {
	if _, err := NewSQLiteShardContextStateStore(SQLiteShardStoreConfig{Root: " "}); err == nil {
		t.Fatal("空 root 必须报错")
	}
	store, err := NewSQLiteShardContextStateStore(SQLiteShardStoreConfig{Root: t.TempDir(), ShardCount: 0})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if got := store.shardCount(); got != 1 {
		t.Fatalf("0 分片回落 1: %d", got)
	}
	big, err := NewSQLiteShardContextStateStore(SQLiteShardStoreConfig{Root: t.TempDir(), ShardCount: 999})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	t.Cleanup(func() { _ = big.Close() })
	if got := big.shardCount(); got != 256 {
		t.Fatalf("分片上限: %d", got)
	}
	normal, err := NewSQLiteShardContextStateStore(SQLiteShardStoreConfig{Root: t.TempDir(), ShardCount: 4})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	t.Cleanup(func() { _ = normal.Close() })
	if got := normal.shardCount(); got != 4 {
		t.Fatalf("普通分片: %d", got)
	}
	// databaseForKey：非法 key（全空）也必须能定位分片或报错，不得 panic。
	if _, err := normal.databaseForKey(""); err != nil {
		t.Fatalf("空 key 分片路由失败: %v", err)
	}
}

func TestWICompactContextTextTruncation(t *testing.T) {
	short := compactContextText([]any{"x"})
	if !strings.HasPrefix(short, `["x"]`) {
		t.Fatalf("短文本=%q", short)
	}
	// 超长文本截断并带标记。
	long := compactContextText([]any{strings.Repeat("y", compactContextMaxChars)})
	if !strings.HasSuffix(long, "\n[truncated]") {
		t.Fatal("超长文本必须截断标记")
	}
}

func TestWICompactFailureAndHeaderHelpers(t *testing.T) {
	preflight := &CompactPreflightService{Sink: &recordedSink{}}
	sink := preflight.Sink.(*recordedSink)
	req := newTestRequest(t, "POST", "/v1/responses/compact", []byte(`{}`), nil)
	input := CompactPreflightInput{Req: req, StartedAt: 5}
	preflight.sendCompactFailure(input, gatewayFailure{statusCode: 404, _type: "invalid_request_error", code: "c1", message: "m1"})
	if failure, ok := sink.lastFailure(); !ok || failure.StatusCode != 404 {
		t.Fatalf("failure=%+v", failure)
	}
	// 无 Sink 安全返回。
	quiet := &CompactPreflightService{}
	quiet.sendCompactFailure(input, gatewayFailure{statusCode: 400})

	// state 失败发送。
	bridge, _, sink2, _, _ := newBridgeService(t)
	bridge.sendCodexBridgeStateFailure(ContextStatePreflightInput{Req: req, StartedAt: 7},
		gatewayFailure{statusCode: 403, _type: "invalid_request_error", code: "c2", message: "m2"})
	if failure, ok := sink2.lastFailure(); !ok || failure.StatusCode != 403 {
		t.Fatalf("state failure=%+v", failure)
	}
	quietBridge := &ChatBridgeStateService{}
	quietBridge.sendCodexBridgeStateFailure(ContextStatePreflightInput{}, gatewayFailure{})

	// mustMarshalJSON 对可序列化值输出 JSON；对不可序列化值回落 null。
	if string(mustMarshalJSON(map[string]any{"k": "v"})) != `{"k":"v"}` {
		t.Fatalf("marshal=%s", mustMarshalJSON(map[string]any{"k": "v"}))
	}
	if string(mustMarshalJSON(make(chan int))) != "null" {
		t.Fatal("不可序列化必须回落 null")
	}
	// responseHeadersToObject：单值字符串、多值数组、nil res。
	recorder, tracker := newTrackedWriter()
	recorder.Header().Set("Content-Type", "application/json")
	recorder.Header()["Set-Cookie"] = []string{"a=1", "b=2"}
	headers := responseHeadersToObject(tracker)
	if headers["Content-Type"] != "application/json" {
		t.Fatalf("headers=%v", headers)
	}
	if arr, ok := headers["Set-Cookie"].([]string); !ok || len(arr) != 2 {
		t.Fatalf("多值 header=%v", headers["Set-Cookie"])
	}
	if got := responseHeadersToObject(nil); len(got) != 0 {
		t.Fatalf("nil res=%v", got)
	}
}

func TestWISynchronizeDispatchBaseline(t *testing.T) {
	service, registry, _, _, _ := newBridgeService(t)
	req := newTestRequest(t, "POST", "/v1/responses", []byte(`{"input":["a"]}`), nil)
	attachBody(req, map[string]any{"input": []any{"a"}}, []byte(`{"input":["a"]}`))
	state := &CodexResponsesContextRequestState{}
	registry.Set(req, state)
	// 无 LastRenderedBody → 仅记录 canonical/current。
	service.synchronizeCodexResponsesDispatchBaseline(registry, req, state)
	if state.CanonicalBody == nil || state.CurrentBody == nil {
		t.Fatalf("baseline=%+v", state)
	}
	// body 未变化（引用相等）→ 直接返回。
	current := state.CurrentBody
	state.CurrentBody = current
	service.synchronizeCodexResponsesDispatchBaseline(registry, req, state)
	// body 变化 + LastRenderedBody 存在 → 重新物化 input。
	state.LastRenderedBody = cloneJSONMapShallow(state.CanonicalBody)
	newBody := map[string]any{"input": []any{"a", "b"}}
	attachBody(req, newBody, mustMarshalJSON(newBody))
	service.synchronizeCodexResponsesDispatchBaseline(registry, req, state)
	if state.MaterializedInput == nil {
		t.Fatal("body 变化后必须物化 input")
	}
	// currentGatewayJSONBody 对 nil req / nil body 安全。
	if currentGatewayJSONBody(nil) != nil {
		t.Fatal("nil req 必须 nil")
	}
	bodyless := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	if currentGatewayJSONBody(bodyless) != nil {
		t.Fatal("无 body 必须 nil")
	}
}

func TestWICompactAccountKindMatrix(t *testing.T) {
	service, registry, _, _, _ := newBridgeService(t)
	req := newTestRequest(t, "POST", "/v1/responses/compact", []byte(`{"input":[]}`), nil)
	attachBody(req, map[string]any{"input": []any{}}, []byte(`{"input":[]}`))
	// 无注册状态 → native 判定仍基于账号画像。
	bridgeAccount := gatewayruntimecache.OpenAIAccountSecret{
		ProviderCode: "openai", ProtocolCode: "openai", ProtocolVersion: "v1",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel: "m", UpstreamModel: "up", SourceEndpointFamily: "responses",
			UpstreamEndpointFamily: "chat_completions", Enabled: true,
		}},
	}
	nativeAccount := gatewayruntimecache.OpenAIAccountSecret{ProviderCode: "openai", ProtocolCode: "openai", ProtocolVersion: "v1"}
	// 无状态 Get miss → codexResponsesCompactAccountKind 的 state nil 分支（model 从请求取）。
	if got := service.codexResponsesCompactAccountKind(registry, req, nativeAccount); got != CompactAccountKindNative {
		t.Fatalf("native kind=%q", got)
	}
	// 请求体无 model → 从请求模型解析。
	// CanonicalBody 带 model 才能驱动映射判定。
	state := &CodexResponsesContextRequestState{RequestKind: RequestKindCompact, CanonicalBody: map[string]any{"model": "m"}}
	registry.Set(req, state)
	if got := service.codexResponsesCompactAccountKind(registry, req, bridgeAccount); got != CompactAccountKindBridge {
		t.Fatalf("bridge kind=%q", got)
	}
	// 非 openai 协议画像 → unsupported。
	weird := gatewayruntimecache.OpenAIAccountSecret{ProviderCode: "weird"}
	if got := service.codexResponsesCompactAccountKind(registry, req, weird); got != CompactAccountKindUnsupported {
		t.Fatalf("unsupported kind=%q", got)
	}
	// AllowsAccount：无注册状态 → true。
	if !service.CodexResponsesContextAllowsAccount(registry, req, nativeAccount) {
		t.Fatal("无注册状态必须放行")
	}
	// HasExplicit：包含 bridge 账号 → true。
	if !service.HasExplicitCodexResponsesChatBridgeRuntimeAccount(registry, req, []gatewayruntimecache.OpenAIAccountSecret{bridgeAccount}) {
		t.Fatal("bridge 账号必须被识别")
	}
	if service.HasExplicitCodexResponsesChatBridgeRuntimeAccount(registry, req, []gatewayruntimecache.OpenAIAccountSecret{nativeAccount}) {
		t.Fatal("纯 native 不应识别为 bridge")
	}
	// AllowsAccount：compact 请求 + native dispatch → bridge 账号不放行。
	state.CompactDispatchMode = CompactDispatchNative
	if service.CodexResponsesContextAllowsAccount(registry, req, bridgeAccount) {
		t.Fatal("native dispatch 下 bridge 账号不得放行")
	}
	if !service.CodexResponsesContextAllowsAccount(registry, req, nativeAccount) {
		t.Fatal("native dispatch 下 native 账号必须放行")
	}
	// internal previous → 仅 bridge。
	state.CompactDispatchMode = ""
	state.PreviousResponseKind = PreviousKindInternal
	if !service.CodexResponsesContextAllowsAccount(registry, req, bridgeAccount) {
		t.Fatal("internal previous 必须放行 bridge")
	}
	if service.CodexResponsesContextAllowsAccount(registry, req, nativeAccount) {
		t.Fatal("internal previous 不得放行 native")
	}
	// external previous → 仅 native。
	state.PreviousResponseKind = PreviousKindExternal
	if service.CodexResponsesContextAllowsAccount(registry, req, bridgeAccount) {
		t.Fatal("external previous 不得放行 bridge")
	}
	// 非 compact 请求 + 非 compaction expected → 默认分支。
	state.RequestKind = RequestKindResponses
	state.PreviousResponseKind = PreviousKindExternal
	if service.CodexResponsesContextAllowsAccount(registry, req, bridgeAccount) {
		t.Fatal("external previous 的 chat 请求不得放行 bridge")
	}
	if !service.CodexResponsesContextAllowsAccount(registry, req, nativeAccount) {
		t.Fatal("external previous 的 chat 请求必须放行 native")
	}
}

func TestWIEncryptedContentStripHelpers(t *testing.T) {
	encrypted := map[string]any{"type": "encrypted_content", "encrypted_content": "abc"}
	plain := map[string]any{"type": "message"}
	items := []any{encrypted, plain}
	result := stripEncryptedContentItems(items)
	if !result.changed || result.removedCount != 1 {
		t.Fatalf("strip=%+v", result)
	}
	if len(result.output.([]any)) != 1 {
		t.Fatalf("output=%v", result.output)
	}
	// 单个加密对象 → 移除为空数组。
	single := stripEncryptedContentItems(encrypted)
	if !single.changed || single.removedCount != 1 {
		t.Fatalf("single=%+v", single)
	}
	// 非 加密项原样保留。
	untouched := stripEncryptedContentItems("scalar")
	if untouched.changed || untouched.output != "scalar" {
		t.Fatalf("scalar=%+v", untouched)
	}
	// 空 reasoning 项判定：只有 type/id/status 与空字段。
	emptyReasoning := map[string]any{"type": "reasoning", "id": "r1", "status": "", "summary": []any{}}
	if !isEmptyReasoningItem(emptyReasoning) {
		t.Fatal("空 reasoning 必须判定为空")
	}
	nonEmpty := map[string]any{"type": "reasoning", "summary": []any{"内容"}}
	if isEmptyReasoningItem(nonEmpty) {
		t.Fatal("有内容 reasoning 不得判空")
	}
}

func TestWIContractRegistryItem(t *testing.T) {
	// 注册表 item 查找：已知类型命中，未知类型 miss，nil 注册表安全。
	if item, ok := defaultCodexResponsesContractRegistry.Item("message"); !ok || item.Type != "message" || item.Prefix == "" {
		t.Fatalf("message item=%+v ok=%v", item, ok)
	}
	if _, ok := defaultCodexResponsesContractRegistry.Item("unknown-type"); ok {
		t.Fatal("未知类型不得命中")
	}
	var nilRegistry *CodexResponsesContractRegistry
	if _, ok := nilRegistry.Item("message"); ok {
		t.Fatal("nil 注册表必须 miss")
	}
}

func TestWIReplaceGatewayJSONBodyBaseline(t *testing.T) {
	// PrepareCodexResponsesContextForAccount 对非 compact 请求的 body 重写分支。
	service, registry, _, _, _ := newBridgeService(t)
	req := newTestRequest(t, "POST", "/v1/responses", []byte(`{"input":["a"],"model":"m"}`), nil)
	attachBody(req, map[string]any{"input": []any{"a"}, "model": "m"}, []byte(`{"input":["a"],"model":"m"}`))
	state := &CodexResponsesContextRequestState{
		RequestKind:          RequestKindResponses,
		PreviousResponseKind: PreviousKindInternal,
		CanonicalBody:        map[string]any{"input": []any{"a"}, "model": "m"},
		MaterializedInput:    []any{"a"},
		CurrentInput:         []any{"a"},
	}
	registry.Set(req, state)
	account := gatewayruntimecache.OpenAIAccountSecret{ProviderCode: "openai", ProtocolCode: "openai", ProtocolVersion: "v1"}
	isBridge, err := service.PrepareCodexResponsesContextForAccount(registry, req, account)
	if err != nil {
		t.Fatalf("准备失败: %v", err)
	}
	if isBridge {
		t.Fatal("native 账号不得作为 bridge")
	}
	if state.LastRenderedBody == nil {
		t.Fatal("准备后必须记录渲染体")
	}
	// 无注册状态 → false 无错。
	notRegistered := newTestRequest(t, "POST", "/v1/responses", []byte(`{}`), nil)
	if done, err := service.PrepareCodexResponsesContextForAccount(registry, notRegistered, account); err != nil || done {
		t.Fatalf("无状态准备=%v err=%v", done, err)
	}
}
