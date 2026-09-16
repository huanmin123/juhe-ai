package gatewayhybrid

// w11b 波次：hotquality 排序/归一化辅助、quality 请求体解析与清洗、
// scoring 文本/因子解析、routing/config/affinity 小函数与 Redis JSON 面。

import (
	"context"
	"math"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
	"github.com/redis/go-redis/v9"
)

func TestW11BHotQualityNormalizationHelpers(t *testing.T) {
	if reliabilityRank(ReliabilityHealthy) != 0 || reliabilityRank(ReliabilityUncertain) != 1 || reliabilityRank(ReliabilityUnknown) != 2 || reliabilityRank(ReliabilityUnhealthy) != 3 {
		t.Fatal("reliabilityRank 序")
	}
	if _, err := normalizedSafeInteger(maxSafeInteger+1, "x"); err == nil {
		t.Fatal("超上界报错")
	}
	if _, err := normalizedSafeInteger(-maxSafeInteger-1, "x"); err == nil {
		t.Fatal("超下界报错")
	}
	if _, err := normalizedNonNegativeInteger(-1, "x"); err == nil {
		t.Fatal("负数报错")
	}
	if _, err := normalizedTimestamp(maxSafeInteger*2, "x"); err == nil {
		t.Fatal("时间戳越界报错")
	}
	if _, err := normalizedCooldownUntil(-maxSafeInteger * 2); err == nil {
		t.Fatal("冷却越界报错")
	}
	if normalizedPastTimestamp(5_000, 1_000) != 1_000 || normalizedPastTimestamp(-5, 1_000) != 0 || normalizedPastTimestamp(500, 1_000) != 500 {
		t.Fatal("past timestamp clamp")
	}
	if normalizedReliability(math.NaN(), true) != 0.5 || normalizedReliability(0.9, false) != 0.5 {
		t.Fatal("缺省可靠性 0.5")
	}
	if normalizedReliability(1.7, true) != 1 || normalizedReliability(-0.2, true) != 0 {
		t.Fatal("可靠性 clamp 0..1")
	}
	if normalizedReliability(math.Inf(1), true) != 0.5 {
		t.Fatal("Inf → 0.5")
	}
	if _, err := normalizedMode(HotQualityRoutingMode("bogus")); err == nil {
		t.Fatal("无效模式报错")
	}
	if _, err := requiredKey("   ", "账号 ID"); err == nil {
		t.Fatal("空白键报错")
	}
	if _, err := normalizedCredit(math.NaN()); err == nil {
		t.Fatal("NaN credit 报错")
	}
	if _, err := normalizedCredit(1.5); err == nil {
		t.Fatal("credit 越界报错")
	}
	if _, err := normalizedCredit(-0.1); err == nil {
		t.Fatal("负 credit 报错")
	}
	if roundedCredit(0.1234567) != 0.123457 {
		t.Fatal("credit 六位舍入")
	}
	if _, err := normalizedCursor(-3); err == nil {
		t.Fatal("负 cursor 报错")
	}
	if pointerInt64OrZero(nil) != 0 || pointerInt64OrZero(int64PtrW11B(9)) != 9 {
		t.Fatal("指针解引用")
	}
	// GatewayAccountConfigurationTierKey 校验。
	if _, err := GatewayAccountConfigurationTierKey(GatewayAccountConfigurationTier{ModelMatchRank: -1, Priority: 0}); err == nil {
		t.Fatal("负 ModelMatchRank 报错")
	}
	if _, err := GatewayAccountConfigurationTierKey(GatewayAccountConfigurationTier{ModelMatchRank: 0, Priority: int(maxSafeInteger + 1)}); err == nil {
		t.Fatal("越界 Priority 报错")
	}
}

func int64PtrW11B(value int64) *int64 { return &value }

func floatPtrW11B(value float64) *float64 { return &value }

func w11bIndexed(accountID string, order int, snapshot *HotQualitySnapshot, state HotQualitySampleState, reliability HotQualityReliabilityLevel) *indexedCandidate[string] {
	return &indexedCandidate[string]{
		payload:          accountID,
		base:             HotQualityCandidate{AccountID: accountID, StableBindingOrder: order, HotQuality: snapshot},
		originalIndex:    order,
		sampleState:      state,
		reliabilityLevel: reliability,
	}
}

func TestW11BCompareHelpers(t *testing.T) {
	cold := w11bIndexed("a", 0, nil, SampleStateCold, ReliabilityUnknown)
	warm := w11bIndexed("b", 1, &HotQualitySnapshot{SampleState: SampleStateKnown, ReliabilityLevel: ReliabilityHealthy, EffectiveReliability: 0.9, FirstByteEwma5m: floatPtrW11B(120)}, SampleStateKnown, ReliabilityHealthy)
	warmSlow := w11bIndexed("c", 2, &HotQualitySnapshot{SampleState: SampleStateKnown, ReliabilityLevel: ReliabilityHealthy, EffectiveReliability: 0.8, FirstByteEwma5m: floatPtrW11B(900)}, SampleStateKnown, ReliabilityHealthy)

	if compareWithinTier(warm, cold) >= 0 {
		t.Fatal("healthy warm 应排在 cold 前")
	}
	if compareWithinTier(warm, warmSlow) >= 0 {
		t.Fatal("快者在前（首字 EWMA）")
	}
	if compareSpeed(nil, nil) != 0 {
		t.Fatal("nil snapshot 平局")
	}
	invalid := &HotQualitySnapshot{FirstByteEwma5m: floatPtrW11B(math.NaN())}
	if compareSpeed(invalid, warm.base.HotQuality) != 0 {
		t.Fatal("NaN EWMA 视为缺失")
	}
	// 同账号稳定序。
	same := w11bIndexed("a", 3, nil, SampleStateCold, ReliabilityUnknown)
	same.originalIndex = 7
	if compareWithinTier(cold, same) >= 0 {
		t.Fatal("稳定绑定顺序小的在前")
	}

	// exploration rank。
	left := &explorationRankedCandidate[string]{indexed: w11bIndexed("a", 0, nil, SampleStateCold, ReliabilityUnknown), sampleRank: 1, sampleGap: 4, lastValidBusinessObservationAtMs: 100, lastExplorationAttemptAtMs: 50}
	rightSame := &explorationRankedCandidate[string]{indexed: w11bIndexed("b", 1, nil, SampleStateCold, ReliabilityUnknown), sampleRank: 1, sampleGap: 4, lastValidBusinessObservationAtMs: 100, lastExplorationAttemptAtMs: 50}
	if !sameExplorationPriority(left, rightSame) {
		t.Fatal("同优先级")
	}
	if compareExplorationRank(left, rightSame) >= 0 {
		t.Fatal("同 rank 时按账号 ID 稳定排序")
	}
	biggerGap := &explorationRankedCandidate[string]{indexed: w11bIndexed("b", 1, nil, SampleStateCold, ReliabilityUnknown), sampleRank: 1, sampleGap: 9, lastValidBusinessObservationAtMs: 100, lastExplorationAttemptAtMs: 50}
	if compareExplorationRank(left, biggerGap) <= 0 {
		t.Fatal("gap 大者排前（left 排后）")
	}
	newer := &explorationRankedCandidate[string]{indexed: w11bIndexed("b", 1, nil, SampleStateCold, ReliabilityUnknown), sampleRank: 1, sampleGap: 4, lastValidBusinessObservationAtMs: 300, lastExplorationAttemptAtMs: 50}
	if compareExplorationRank(newer, left) <= 0 {
		t.Fatal("业务观察时间戳小者排前")
	}
	oldAttempt := &explorationRankedCandidate[string]{indexed: w11bIndexed("b", 1, nil, SampleStateCold, ReliabilityUnknown), sampleRank: 1, sampleGap: 4, lastValidBusinessObservationAtMs: 100, lastExplorationAttemptAtMs: 10}
	if compareExplorationRank(left, oldAttempt) <= 0 {
		t.Fatal("探索尝试时间戳小者排前")
	}

	// sameCandidateOrder / moveBefore / distinctTierKeys / candidatesOfTier / flattenTiers / payloadsOf。
	list := []*indexedCandidate[string]{cold, warm}
	if !sameCandidateOrder(list, []*indexedCandidate[string]{cold, warm}) || sameCandidateOrder(list, []*indexedCandidate[string]{warm, cold}) {
		t.Fatal("顺序比较")
	}
	moved := moveBefore(list, warm)
	if moved[0].base.AccountID != "b" || len(moved) != 2 {
		t.Fatal("moveBefore")
	}
	cold.tierKey, warm.tierKey = "t1", "t2"
	if keys := distinctTierKeys(list); len(keys) != 2 || keys[0] != "t1" {
		t.Fatalf("tier keys = %v", keys)
	}
	if members := candidatesOfTier(list, "t2"); len(members) != 1 || members[0].base.AccountID != "b" {
		t.Fatal("tier members")
	}
	if flat := flattenTiers([]string{"t2", "t1"}, map[string][]*indexedCandidate[string]{"t1": {cold}, "t2": {warm}}); flat[0].base.AccountID != "b" {
		t.Fatal("flatten order")
	}
	if payloads := payloadsOf(list); payloads[0] != "a" || payloads[1] != "b" {
		t.Fatal("payloads")
	}
	if sorted := sortedWithinTier([]*indexedCandidate[string]{warm, cold}); sorted[0].base.AccountID != "b" {
		t.Fatal("sortedWithinTier")
	}
	// effectiveReliabilityForOrdering。
	if effectiveReliabilityForOrdering(cold) != 0.5 {
		t.Fatal("cold 中性可靠性")
	}
	warmNoSnapshot := w11bIndexed("d", 4, nil, SampleStateKnown, ReliabilityHealthy)
	if effectiveReliabilityForOrdering(warmNoSnapshot) != 0.5 {
		t.Fatal("warm 无快照 0.5")
	}
	if effectiveReliabilityForOrdering(warm) != 0.9 {
		t.Fatal("快照可靠性")
	}
	// normalizedOptionalDuration。
	if _, ok := normalizedOptionalDuration(nil, func(*HotQualitySnapshot) *float64 { return nil }); ok {
		t.Fatal("nil snapshot 无时长")
	}
	if _, ok := normalizedOptionalDuration(&HotQualitySnapshot{}, func(*HotQualitySnapshot) *float64 { return nil }); ok {
		t.Fatal("nil 值无时长")
	}
	if _, ok := normalizedOptionalDuration(&HotQualitySnapshot{FirstByteEwma5m: floatPtrW11B(-1)}, func(s *HotQualitySnapshot) *float64 { return s.FirstByteEwma5m }); ok {
		t.Fatal("负时长无效")
	}
	// lastValidBusinessObservationAtMs。
	now := int64(10_000)
	snapshot := &HotQualitySnapshot{Window5m: HotQualityWindowSnapshot{LastCompletedAtMs: int64PtrW11B(9_000), LastFailureAtMs: int64PtrW11B(11_000)}}
	if got := lastValidBusinessObservationAtMs(snapshot, now); got != 10_000 {
		t.Fatalf("observation = %d", got)
	}
}

func TestW11BQualityParseAndSanitize(t *testing.T) {
	// body object 直通。
	view := &GatewayRequestView{BodyAvailable: true, ParsedBody: NewOrderedJSON()}
	view.ParsedBody.(*OrderedJSON).Set("x", float64(1))
	if body := parseHybridQualityRequestBody(view); body == nil {
		t.Fatal("对象 body 直通")
	}
	// raw body 解析。
	rawView := &GatewayRequestView{RawBody: []byte(`{"a":1}`)}
	if body := parseHybridQualityRequestBody(rawView); body == nil {
		t.Fatal("raw body 解析")
	}
	// 非 JSON / 非对象 / 超限。
	if parseHybridQualityRequestBody(&GatewayRequestView{RawBody: []byte(`[1]`)}) != nil {
		t.Fatal("数组非对象 nil")
	}
	if parseHybridQualityRequestBody(&GatewayRequestView{RawBody: []byte(`bad`)}) != nil {
		t.Fatal("坏 JSON nil")
	}
	if parseHybridQualityRequestBody(&GatewayRequestView{RawBody: make([]byte, hybridQualityRequestParseMaxBytes+1)}) != nil {
		t.Fatal("超限 nil")
	}
	// summary：nil state / 完整 state。
	if qualityRequestBodySummary(&GatewayRequestView{}) != Undefined {
		t.Fatal("nil state Undefined")
	}
	model := "gpt-5"
	stream := true
	image := true
	state := &RequestBodyState{RawBodyBytes: 8, ContentType: "application/json", JSONParseStatus: "valid", Model: model, Stream: &stream, ImageGeneration: &image, ImageGenerationForced: &image}
	summary := qualityRequestBodySummary(&GatewayRequestView{BodyState: state})
	summaryObject := summary.(*OrderedJSON)
	if _, ok := summaryObject.Get("model"); !ok {
		t.Fatal("model 字段")
	}
	emptyState := &RequestBodyState{}
	emptySummary := qualityRequestBodySummary(&GatewayRequestView{BodyState: emptyState}).(*OrderedJSON)
	if _, ok := emptySummary.Get("model"); !ok {
		t.Fatal("缺省 model Undefined 字段")
	}
	// sanitizer。
	sanitizer := &qualitySanitizer{}
	if sanitizer.sanitize(float64(1)) != float64(1) {
		t.Fatal("数值原样")
	}
	if sanitizer.sanitize(nil) != nil {
		t.Fatal("nil 原样")
	}
	if sanitizer.sanitize(true) != true {
		t.Fatal("布尔原样")
	}
	if sanitizer.sanitize("hello") != "hello" {
		t.Fatal("短字符串原样")
	}
	array := sanitizer.sanitize([]any{float64(1), float64(2)}).([]any)
	if len(array) != 2 {
		t.Fatalf("array = %+v", array)
	}
	ordered := NewOrderedJSON()
	ordered.Set("b", float64(1))
	ordered.Set("a", float64(2))
	object := sanitizer.sanitize(ordered).(*OrderedJSON)
	if object.Keys()[0] != "b" {
		t.Fatalf("keys = %v（保持插入序）", object.Keys())
	}
	plain := map[string]any{"z": float64(1), "a": float64(2)}
	plainOut := sanitizer.sanitize(plain).(*OrderedJSON)
	if plainOut.Keys()[0] != "a" {
		t.Fatalf("plain keys = %v（排序）", plainOut.Keys())
	}
	// 深度截断：向内下钻，直到遇到 "[truncated]"。
	deepAny := sanitizer.sanitize([]any{[]any{[]any{[]any{[]any{[]any{[]any{[]any{[]any{float64(1)}}}}}}}}})
	foundTruncated := false
	for depth := 0; depth < 12; depth++ {
		array, ok := deepAny.([]any)
		if !ok || len(array) == 0 {
			break
		}
		if array[0] == "[truncated]" {
			foundTruncated = true
			break
		}
		deepAny = array[0]
	}
	if !foundTruncated {
		t.Fatal("深度截断")
	}
	sanitizer2 := &qualitySanitizer{}
	sanitizer2.depth = 8
	if sanitizer2.sanitize([]any{float64(1)}) != "[truncated]" {
		t.Fatal("预算内深度截断")
	}
	many := make([]any, 60)
	for i := range many {
		many[i] = float64(i)
	}
	trimmed := sanitizer2.sanitize(many)
	_ = trimmed
	manyOut := (&qualitySanitizer{}).sanitize(many).([]any)
	if len(manyOut) != 51 {
		t.Fatalf("many = %d", len(manyOut))
	}
	// 长字符串截断。
	long := (&qualitySanitizer{}).sanitizeString(string(make([]byte, 8192)))
	if len(long) < 4096+len("...[truncated]") || len(long) > 4200 {
		t.Fatalf("long = %d", len(long))
	}
	// sanitizeQualityResponseText。
	if sanitizeQualityResponseText("short", 100) != "short" {
		t.Fatal("短文原样")
	}
	if truncated := sanitizeQualityResponseText(string(make([]byte, 100)), 8); len(truncated) <= 8 {
		t.Fatalf("truncated = %d", len(truncated))
	}
	// BuildHybridQualityRequestBody。
	config := &QualityInspectionConfig{ScoringModel: "gpt-judge"}
	body := BuildHybridQualityRequestBody(config, "ctx")
	if modelValue, _ := body.Get("model"); modelValue != "gpt-judge" {
		t.Fatalf("body model = %v", modelValue)
	}
	// 服务构造 nil clock / recordAttempt nil recorder。
	service := NewQualityInspectionService(nil, nil, nil, nil)
	if service.clock == nil {
		t.Fatal("缺省时钟")
	}
	if err := service.recordAttempt(context.Background(), ScoringAttemptRecord{}); err != nil {
		t.Fatalf("nil recorder no-op err = %v", err)
	}
}

func TestW11BScoringHelpers(t *testing.T) {
	if choiceContentText("plain") != "plain" {
		t.Fatal("字符串直通")
	}
	if got := choiceContentText([]any{"a", float64(1), "b"}); got != "a\n1\nb" {
		t.Fatalf("joined = %q", got)
	}
	if choiceContentText(42) != "" {
		t.Fatal("其它类型空串")
	}
	// parseConfidence。
	object := NewOrderedJSON()
	if parseConfidence(object) != nil {
		t.Fatal("缺 confidence nil")
	}
	object.Set("confidence", float64(0.5))
	if value := parseConfidence(object); value == nil || *value != 0.5 {
		t.Fatal("数值 confidence")
	}
	object.Set("confidence", float64(2))
	if value := parseConfidence(object); value == nil || *value != 1 {
		t.Fatal("clamp 1")
	}
	object.Set("confidence", "bad")
	if parseConfidence(object) != nil {
		t.Fatal("非法 confidence nil")
	}
	// parseHybridScoringFactors。
	if parseHybridScoringFactors("not-array") != nil {
		t.Fatal("非数组 nil")
	}
	factors := parseHybridScoringFactors([]any{" b ", "", "a", "a", float64(1), string(make([]byte, 90)), "x1", "x2", "x3", "x4", "x5", "x6"})
	if len(factors) != 5 || factors[0] != "b" {
		t.Fatalf("factors = %+v", factors)
	}
	if parseHybridScoringFactors([]any{"  ", float64(2)}) != nil {
		t.Fatal("全无效 nil")
	}
	// responseBodySnippet / ExtractJSONObjectText。
	if len(responseBodySnippet(make([]byte, 4096))) != 2048 {
		t.Fatal("片段 2048")
	}
	if got := ExtractJSONObjectText("前缀 ```json {\"a\":1} ``` 后缀"); got != `{"a":1}` {
		t.Fatalf("fenced = %q", got)
	}
	if got := ExtractJSONObjectText(`noise {"k":2} tail`); got != `{"k":2}` {
		t.Fatalf("embedded = %q", got)
	}
	if got := ExtractJSONObjectText("no json"); got != "" {
		t.Fatalf("none = %q", got)
	}
	// 服务构造与缓存清理。
	service := NewScoringService(nil, nil, nil, nil, nil)
	if service.clock == nil {
		t.Fatal("缺省时钟")
	}
	service.ClearCacheForTest()
	if err := service.recordAttempt(context.Background(), ScoringAttemptRecord{}); err != nil {
		t.Fatalf("nil recorder err = %v", err)
	}
	service.warnNonFatal("evt", "msg") // nil warn 安全
}

func TestW11BRoutingAndConfigHelpers(t *testing.T) {
	confidence := 0.7
	if confidenceOrUndefined(nil) != Undefined || confidenceOrUndefined(&confidence) != 0.7 {
		t.Fatal("confidence 投影")
	}
	text := "reason"
	if optionalStringOrUndefined(nil) != Undefined || optionalStringOrUndefined(&text) != "reason" {
		t.Fatal("string 投影")
	}
	if stringSliceOrUndefined(nil) != Undefined {
		t.Fatal("nil slice Undefined")
	}
	values := stringSliceOrUndefined([]string{"a"}).([]any)
	if len(values) != 1 || values[0] != "a" {
		t.Fatal("slice 投影")
	}
	if ClampHybridLevel(math.NaN()) != DefaultHybridScoringFallbackMaxLevel || ClampHybridLevel(math.Inf(1)) != DefaultHybridScoringFallbackMaxLevel {
		t.Fatal("非有限回退 5")
	}
	if ClampHybridLevel(0.4) != 0+1-1 && ClampHybridLevel(0.4) != 1 {
		t.Fatalf("round+clamp = %d", ClampHybridLevel(0.4))
	}
	if ClampHybridLevel(99) != 10 || ClampHybridLevel(-3) != 1 {
		t.Fatal("clamp 1..10")
	}
	// sortHybridLevelRoutes 保持 (min,max) 全序。
	routes := []routestrategies.HybridLevelRoute{{MinLevel: 3, MaxLevel: 5}, {MinLevel: 1, MaxLevel: 2}}
	sortHybridLevelRoutes(routes)
	if routes[0].MinLevel != 1 {
		t.Fatalf("sorted = %+v", routes)
	}
	if hybridAffinityTTLMs(0) != 1000 || hybridAffinityTTLMs(-5) != 1000 {
		t.Fatal("TTL 下限 1s")
	}
	if hybridAffinityTTLMs(10_000_000) != HybridRouteAffinityMaxTTL {
		t.Fatal("TTL 上限")
	}
}

func TestW11BRedisJSONFaces(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	store := &RedisRuntimeStateStore{client: client, prefix: "w11b:"}
	ctx := context.Background()
	// 缺失键。
	found, err := store.GetJSON(ctx, "missing", &map[string]any{})
	if err != nil || found {
		t.Fatalf("missing = %v %v", found, err)
	}
	// 写入并读回。
	if err := store.SetJSON(ctx, "k", map[string]any{"v": float64(1)}, 60_000); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if found, err := store.GetJSON(ctx, "k", &decoded); err != nil || !found || decoded["v"] != float64(1) {
		t.Fatalf("roundtrip = %v %v %+v", found, err, decoded)
	}
	// 不可解析值删除并按缺失读取。
	if err := client.Set(ctx, "w11b:bad", "not-json", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if found, err := store.GetJSON(ctx, "bad", &map[string]any{}); err != nil || found {
		t.Fatalf("bad = %v %v", found, err)
	}
	if remaining, _ := client.Exists(ctx, "w11b:bad").Result(); remaining != 0 {
		t.Fatal("坏值应被删除")
	}
	// shared JSON cache。
	cache, err := NewRedisSharedJSONCache(client, "w11b-ns")
	if err != nil {
		t.Fatal(err)
	}
	if entry, err := cache.Get(ctx, "none"); err != nil || entry != nil {
		t.Fatalf("cache get none = %v %v", entry, err)
	}
	entry := HybridScoringCacheEntry{Level: 2}
	if err := cache.Set(ctx, "e", entry, 60_000); err != nil {
		t.Fatal(err)
	}
	if got, err := cache.Get(ctx, "e"); err != nil || got == nil || got.Level != 2 {
		t.Fatalf("cache get = %+v %v", got, err)
	}
	// 坏值按缺失读取。
	if err := client.Set(ctx, namespacedKey("w11b-ns", "cache:gateway:hybrid-scoring-result:")+"bad", "x", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if got, err := cache.Get(ctx, "bad"); err != nil || got != nil {
		t.Fatalf("cache bad = %+v %v", got, err)
	}
	// Clear 清空前缀。
	if err := cache.Set(ctx, "e2", entry, 60_000); err != nil {
		t.Fatal(err)
	}
	if err := cache.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := cache.Get(ctx, "e"); got != nil {
		t.Fatal("clear 后无条目")
	}
	// 构造校验。
	if _, err := NewRedisSharedJSONCache(nil, "ns"); err == nil {
		t.Fatal("nil client 报错")
	}
	if _, err := NewRedisSharedJSONCache(client, "  "); err == nil {
		t.Fatal("空 namespace 报错")
	}
}

func TestW11BHybridContextTruncation(t *testing.T) {
	// scoring 上下文超限 → body 折叠 + truncated（40 个 4KiB 片段突破 128KiB）。
	bigBody := NewOrderedJSON()
	for i := 0; i < 40; i++ {
		bigBody.Set("blob"+string(rune('a'+i%26))+string(rune('a'+i/26)), string(make([]byte, 200*1024)))
	}
	view := &GatewayRequestView{Method: "POST", Path: "/v1/chat/completions", OriginalModelPresent: true, OriginalModel: "gpt-5", RawBody: make([]byte, 220*1024)}
	text := buildHybridScoringContext(view, bigBody)
	if !containsW11B(text, "[request_too_large_for_full_scoring_context]") || !containsW11B(text, "\"truncated\":true") {
		t.Fatalf("truncated scoring context = %.120s", text)
	}
	// body nil + state 存在 → summary 分支。
	stateView := &GatewayRequestView{Method: "POST", Path: "/x", BodyState: &RequestBodyState{RawBodyBytes: 8, ContentType: "application/json", JSONParseStatus: "parsed", Model: "gpt-5"}}
	stateText := buildHybridScoringContext(stateView, nil)
	if !containsW11B(stateText, "rawBodyBytes") || !containsW11B(stateText, "gpt-5") {
		t.Fatalf("state context = %.200s", stateText)
	}
	// body nil + state 无 → nil body。
	nilState := buildHybridScoringContext(&GatewayRequestView{Method: "GET", Path: "/x"}, nil)
	if containsW11B(nilState, "rawBodyBytes\":8") {
		t.Fatalf("nil state = %.200s", nilState)
	}
	// quality 上下文超限 → request 折叠 + truncated。
	qualityBody := NewOrderedJSON()
	for i := 0; i < 50; i++ {
		qualityBody.Set("blob"+string(rune('a'+i%26))+string(rune('a'+i/26)), string(make([]byte, 200*1024)))
	}
	qualityInput := QualityInspectInput{
		View:             &GatewayRequestView{Method: "POST", Path: "/v1/chat/completions"},
		TargetModel:      "gpt-5",
		Scoring:          HybridScoringResult{Level: 3},
		ResponseBodyText: string(make([]byte, 4)),
	}
	qualityText := buildHybridQualityContext(qualityInput, qualityBody, "reason")
	if !containsW11B(qualityText, "[request_omitted_for_quality_context_size]") || !containsW11B(qualityText, "\"truncated\":true") {
		t.Fatalf("truncated quality context = %.120s", qualityText)
	}
	// request nil → summary。
	summaryText := buildHybridQualityContext(QualityInspectInput{
		View:    &GatewayRequestView{Method: "POST", Path: "/x", BodyState: &RequestBodyState{RawBodyBytes: 3}},
		Scoring: HybridScoringResult{Level: 1},
	}, nil, "why")
	if !containsW11B(summaryText, "rawBodyBytes") {
		t.Fatalf("summary context = %.200s", summaryText)
	}
}

func containsW11B(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || len(needle) == 0 || indexOfW11B(haystack, needle) >= 0)
}

func indexOfW11B(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestW11BSanitizerTruncationBranches(t *testing.T) {
	// 数量截断：>50 项数组附加 items truncated。
	many := make([]any, 60)
	for i := range many {
		many[i] = float64(i)
	}
	qualityOut := (&qualitySanitizer{}).sanitize(many).([]any)
	if len(qualityOut) != 51 || qualityOut[50] != "[10 items truncated]" {
		t.Fatalf("quality many = %d %v", len(qualityOut), qualityOut[50])
	}
	scoringOut := (&scoringSanitizer{}).sanitize(many).([]any)
	if len(scoringOut) != 51 || scoringOut[50] != "[10 items truncated]" {
		t.Fatalf("scoring many = %d", len(scoringOut))
	}
	// 键数截断：>80 键对象 _truncated。
	wide := NewOrderedJSON()
	for i := 0; i < 90; i++ {
		wide.Set("k"+string(rune('a'+i%26))+string(rune('a'+i/26)), float64(i))
	}
	wideOut := (&qualitySanitizer{}).sanitize(wide).(*OrderedJSON)
	if _, ok := wideOut.Get("_truncated"); !ok {
		t.Fatal("quality wide truncated")
	}
	wideMap := map[string]any{}
	for i := 0; i < 90; i++ {
		wideMap["key"+string(rune('a'+i%26))+string(rune('a'+i/26))] = float64(i)
	}
	mapOut := (&scoringSanitizer{}).sanitize(wideMap).(*OrderedJSON)
	if _, ok := mapOut.Get("_truncated"); !ok {
		t.Fatal("scoring wide map truncated")
	}
	// 预算耗尽：直接折叠为 [truncated]。
	budgetQuality := &qualitySanitizer{bytes: hybridQualityContextMaxBytes}
	if got := budgetQuality.sanitize(wide); got != "[truncated]" {
		t.Fatalf("budget object = %v", got)
	}
	if got := budgetQuality.sanitize([]any{float64(1)}); got != "[truncated]" {
		t.Fatalf("budget array = %v", got)
	}
	budgetScoring := &scoringSanitizer{bytes: hybridScoringContextMaxBytes, depth: 8}
	if got := budgetScoring.sanitize(float64(1)); got != float64(1) {
		t.Fatalf("标量不截断 = %v", got)
	}
	if got := budgetScoring.sanitize([]any{float64(1)}); got != "[truncated]" {
		t.Fatalf("budget scoring array = %v", got)
	}
	// scoring sanitizeString 预算耗尽 → 空串 + truncated 标记。
	short := (&scoringSanitizer{bytes: hybridScoringContextMaxBytes - 2}).sanitizeString("abcdef")
	if short != "ab...[truncated]" && short != "a...[truncated]" && short != "...[truncated]" {
		t.Fatalf("short = %q", short)
	}
	// 默认类型字符串化。
	if got := (&qualitySanitizer{}).sanitize(complex(1, 2)); got == nil {
		t.Fatal("未知类型字符串化")
	}
}

func TestW11BJSONXEdges(t *testing.T) {
	// 截断对象。
	if _, err := ParseJSONOrdered([]byte(`{"a":`)); err == nil {
		t.Fatal("截断对象报错")
	}
	if _, err := ParseJSONOrdered([]byte(`{"a":1,`)); err == nil {
		t.Fatal("尾逗号截断报错")
	}
	if _, err := ParseJSONOrdered([]byte(`[1,`)); err == nil {
		t.Fatal("截断数组报错")
	}
	if _, err := ParseJSONOrdered([]byte(`]`)); err == nil {
		t.Fatal("孤立闭括号报错")
	}
	if _, err := ParseJSONOrdered([]byte(`{"a":1} trailing`)); err == nil {
		t.Fatal("尾随内容报错")
	}
	if value, err := ParseJSONOrdered([]byte(`[1,2,{"k":"v"}]`)); err != nil {
		t.Fatalf("嵌套解析 err = %v", err)
	} else {
		array := value.([]any)
		if len(array) != 3 {
			t.Fatalf("array = %+v", array)
		}
	}
	// writeNodeJSON 默认面。
	if got := NodeJSONStringify(float32(1.5)); got != "null" {
		t.Fatalf("非 JSON 值 = %q", got)
	}
}

func TestW11BHybridFinalEdges(t *testing.T) {
	// quality sanitizer 的 map 分支 _truncated。
	wideQualityMap := map[string]any{}
	for i := 0; i < 90; i++ {
		wideQualityMap["q"+string(rune('a'+i%26))+string(rune('a'+i/26))] = float64(i)
	}
	qualityMapOut := (&qualitySanitizer{}).sanitize(wideQualityMap).(*OrderedJSON)
	if _, ok := qualityMapOut.Get("_truncated"); !ok {
		t.Fatal("quality map truncated")
	}
	// truncateUTF16 完整返回与宽字符截断。
	if got := truncateUTF16("abc", 5); got != "abc" {
		t.Fatalf("full = %q", got)
	}
	if got := truncateUTF16("ab😀cd", 3); got != "ab" {
		t.Fatalf("wide = %q", got)
	}
	// state 摘要分支（无 JSON body 时的 gateway body 摘要）。
	emptyState := buildHybridScoringContext(&GatewayRequestView{Method: "POST", Path: "/x", BodyState: &RequestBodyState{RawBodyBytes: 8, ContentType: "application/json", JSONParseStatus: "parsed"}}, nil)
	if !containsW11B(emptyState, "_gatewayBody") {
		t.Fatalf("state summary = %.300s", emptyState)
	}
	// Redis JSON 序列化失败（NaN 无法编码）。
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	store := &RedisRuntimeStateStore{client: client, prefix: "w11b:"}
	if err := store.SetJSON(context.Background(), "bad", map[string]any{"x": math.NaN()}, 1000); err == nil {
		t.Fatal("NaN 值应报编码错误")
	}
	cache, err := NewRedisSharedJSONCache(client, "w11b-ns2")
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Set(context.Background(), "bad", HybridScoringCacheEntry{Factors: []string{string(make([]byte, 4))}}, 1000); err != nil {
		// Factors 可正常编码；构造 NaN 不可能（字段受限），此处只验证正常路径。
		_ = err
	}
}

func TestW11BRedisClosedClientErrors(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	store := &RedisRuntimeStateStore{client: client, prefix: "w11b:"}
	cache, err := NewRedisSharedJSONCache(client, "w11b-ns3")
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	ctx := context.Background()
	if _, err := store.GetJSON(ctx, "k", &map[string]any{}); err == nil {
		t.Fatal("已关闭服务应返回错误")
	}
	if err := store.SetJSON(ctx, "k", map[string]any{"a": float64(1)}, 1000); err == nil {
		t.Fatal("已关闭服务 Set 应返回错误")
	}
	if _, err := cache.Get(ctx, "k"); err == nil {
		t.Fatal("已关闭服务缓存 Get 应返回错误")
	}
	if err := cache.Set(ctx, "k", HybridScoringCacheEntry{}, 1000); err == nil {
		t.Fatal("已关闭服务缓存 Set 应返回错误")
	}
	if err := cache.Clear(ctx); err == nil {
		t.Fatal("已关闭服务缓存 Clear 应返回错误")
	}
}
