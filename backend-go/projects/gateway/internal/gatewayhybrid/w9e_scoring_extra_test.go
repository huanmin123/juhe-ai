package gatewayhybrid

// w9e 覆盖率战役：驱动 ScoringService 全流程（缓存命中/派发失败/超大请求体/
// 深层上下文裁剪/解析健壮性）与 affinity 辅助。复用既有 mock 基建。

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestW9EScoreRawBodyTooLargeUsesPlaceholder(t *testing.T) {
	now := time.Now()
	// 超大 raw body 只是把上下文替换为占位文本，派发照常进行。
	dispatcher := &mockDispatcher{script: []dispatchOutcome{{
		success: successDispatch("acct", "grp", 200, `{"choices":[{"message":{"content":"{\"level\":2}"}}]}`, gatewayprotoEmptyUsage()),
	}}}
	service := newScoringServiceWith(testClock(&now), dispatcher, &mockRecorder{}, nil)
	view := scoringView()
	view.RawBody = []byte(`{"model":"m","pad":"` + strings.Repeat("x", hybridScoringRawBodyParseMaxBytes+10) + `"}`)
	view.BodyAvailable = true
	result := service.Score(context.Background(), ScoreInput{
		View: view, APIKeyRecord: APIKeyRecord{ID: "k", SystemAccountID: "s"}, Config: hybridConfig(), Endpoint: "/ep",
	})
	if result.Level != 2 {
		t.Fatalf("占位上下文仍应完成评分, result=%+v", result)
	}
}

func TestW9EScoreDeepContextTruncation(t *testing.T) {
	now := time.Now()
	// 构造超过 80 项的数组与超长键值，触发 context 深度/字节上限分支。
	items := make([]any, 0, 100)
	for i := 0; i < 100; i++ {
		items = append(items, map[string]any{"k" + strings.Repeat("x", 40): strings.Repeat("v", 200)})
	}
	object := NewOrderedJSON()
	object.Set("items", items)
	view := scoringView()
	view.RawBody = nil
	view.BodyAvailable = false
	view.ParsedBody = object
	dispatcher := &mockDispatcher{script: []dispatchOutcome{{
		success: successDispatch("acct", "grp", 200, `{"choices":[{"message":{"content":"{}"}}]}`, gatewayprotoEmptyUsage()),
	}}}
	service := newScoringServiceWith(testClock(&now), dispatcher, &mockRecorder{}, nil)
	result := service.Score(context.Background(), ScoreInput{
		View: view, APIKeyRecord: APIKeyRecord{ID: "k", SystemAccountID: "s"}, Config: hybridConfig(), Endpoint: "/ep",
	})
	// 裁剪只影响上下文体量；评分按默认/失败契约落地，不应 panic。
	if !result.Failed && result.Level == 0 {
		t.Fatalf("深层裁剪应有落地结果: %+v", result)
	}
}

func TestW9EScoreDispatchFailureDegrades(t *testing.T) {
	now := time.Now()
	failureCode := "no_account"
	dispatcher := &mockDispatcher{script: []dispatchOutcome{{
		failure: &AuxiliaryDispatchFailure{ErrorCode: failureCode, ErrorMessage: "无可用账户"},
	}}}
	recorder := &mockRecorder{}
	service := newScoringServiceWith(testClock(&now), dispatcher, recorder, nil)
	result := service.Score(context.Background(), ScoreInput{
		View: scoringView(), APIKeyRecord: APIKeyRecord{ID: "k", SystemAccountID: "s"}, Config: hybridConfig(), Endpoint: "/ep",
	})
	if !result.Failed || result.ErrorCode != "no_account" {
		t.Fatalf("派发失败应降级: %+v", result)
	}
}

func TestW9EScoreCacheHitSecondCall(t *testing.T) {
	now := time.Now()
	dispatcher := &mockDispatcher{script: []dispatchOutcome{{
		success: successDispatch("acct-1", "group-1", 200,
			`{"choices":[{"message":{"content":"{\"level\":3}"}}]}`, gatewayprotoEmptyUsage()),
	}}}
	shared := newMockSharedCache()
	service := newScoringServiceWith(testClock(&now), dispatcher, &mockRecorder{}, shared)
	input := ScoreInput{View: scoringView(), APIKeyRecord: APIKeyRecord{ID: "key", SystemAccountID: "sys"}, Config: hybridConfig(), Endpoint: "/ep"}
	first := service.Score(context.Background(), input)
	second := service.Score(context.Background(), input)
	if first.CacheHit {
		t.Fatal("首次不应命中缓存")
	}
	if !second.CacheHit {
		t.Fatal("二次应命中缓存")
	}
}

func TestW9EAffinityServiceClearAndTTLClamp(t *testing.T) {
	service := NewAffinityService(nil, nil, nil)
	service.ClearForTest()
	if got := hybridAffinityTTLMs(5); got != 5000 {
		t.Fatalf("ttl 5s = %d", got)
	}
	if got := hybridAffinityTTLMs(0); got != 1000 {
		t.Fatalf("ttl 0 应钳到 1s, got %d", got)
	}
	if got := hybridAffinityTTLMs(10_000_000); got != HybridRouteAffinityMaxTTL {
		t.Fatalf("超大 ttl 应钳到上限, got %d", got)
	}
	if hybridMaxInt64(2, 1) != 2 || hybridMaxInt64(1, 1) != 1 || hybridMinInt64(1, 2) != 1 {
		t.Fatal("min/max 辅助不符")
	}
}

func TestW9EMutableGatewayJSONBodyFallbacks(t *testing.T) {
	// bodyObject 缺失 + 无 raw body → nil。
	if got := MutableGatewayJSONBody(&GatewayRequestView{Method: "POST"}); got != nil {
		t.Fatalf("无 body 应为 nil, got %#v", got)
	}
	// raw body 为非对象 JSON → nil。
	nonObject := &GatewayRequestView{RawBody: []byte(`[1,2]`)}
	if got := MutableGatewayJSONBody(nonObject); got != nil {
		t.Fatalf("数组 body 应为 nil, got %#v", got)
	}
	// raw body 非法 JSON → nil。
	broken := &GatewayRequestView{RawBody: []byte(`{oops`)}
	if got := MutableGatewayJSONBody(broken); got != nil {
		t.Fatalf("坏 JSON 应为 nil, got %#v", got)
	}
}
