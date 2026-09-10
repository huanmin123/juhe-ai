package gatewaydispatch

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 错误分类与纯函数单元测试：errors.go / attempt.go / util.go /
// providerprotocol.go / codexturnavoidance.go / apikeyrotation.go。
// 断言只依赖标准库；确定性：所有时间通过注入 NowMs 控制。

func TestErrorTaxonomyMessagesAndCodes(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		message string
		code    string
	}{
		{
			name:    "first byte timeout",
			err:     &GatewayFirstByteTimeoutError{Message: "超时", TimeoutMs: 5_000},
			message: "超时",
			code:    "first_byte_timeout",
		},
		{
			name:    "response precommit deadline",
			err:     &GatewayResponsePrecommitDeadlineError{DeadlineAtMs: 123},
			message: "网关请求墙钟已到，响应尚未产生可提交的语义结果",
			code:    "gateway_request_wall_budget_exhausted",
		},
		{
			name:    "wall budget wall kind",
			err:     &GatewayRequestWallBudgetExhaustedError{BudgetKind: WallBudgetKindWall},
			message: "网关请求墙钟预算已进入最终响应预留区",
			code:    "gateway_request_wall_budget_exhausted",
		},
		{
			name:    "wall budget coordination kind",
			err:     &GatewayRequestWallBudgetExhaustedError{BudgetKind: WallBudgetKindCoordination},
			message: "网关请求协调等待预算已耗尽，需要交接客户端重试",
			code:    "gateway_request_coordination_budget_exhausted",
		},
		{
			name:    "normal route first byte cutover",
			err:     &NormalRouteFirstByteCutoverError{Message: "切换候选"},
			message: "切换候选",
			code:    "normal_route_first_byte_timeout",
		},
		{
			name:    "body read incomplete timeout cause",
			err:     &UpstreamBodyReadIncompleteError{Cause: errors.New("context deadline 超时")},
			message: "上游响应正文读取超时",
			code:    "UPSTREAM_BODY_READ_INCOMPLETE",
		},
		{
			name:    "body read incomplete plain cause",
			err:     &UpstreamBodyReadIncompleteError{Cause: errors.New("unexpected EOF")},
			message: "上游响应正文读取未完成",
			code:    "UPSTREAM_BODY_READ_INCOMPLETE",
		},
		{
			name:    "body read max lifetime",
			err:     &UpstreamBodyReadMaxLifetimeError{TimeoutMs: 9_500},
			message: "上游非流式响应正文读取超时（绝对上限 10s）",
			code:    "UPSTREAM_BODY_READ_MAX_LIFETIME",
		},
		{
			name:    "unsupported encoding",
			err:     &UnsupportedUpstreamResponseEncodingError{Message: "不支持的上游响应压缩编码: zstd"},
			message: "不支持的上游响应压缩编码: zstd",
			code:    "",
		},
		{
			name:    "upstream request timeout",
			err:     &UpstreamRequestTimeoutError{Message: "上游请求超时"},
			message: "上游请求超时",
			code:    "",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.err.Error() != testCase.message {
				t.Fatalf("Error() = %q, 期望 %q", testCase.err.Error(), testCase.message)
			}
			coder, ok := testCase.err.(interface{ Code() string })
			if !ok {
				if testCase.code != "" {
					t.Fatalf("错误类型缺少 Code() 方法: %T", testCase.err)
				}
				return
			}
			if got := coder.Code(); got != testCase.code {
				t.Fatalf("Code() = %q, 期望 %q", got, testCase.code)
			}
		})
	}
}

func TestErrorPredicates(t *testing.T) {
	started := &StartedTransportError{Err: errors.New("连接被重置")}
	if !IsStartedUpstreamTransportError(started) {
		t.Fatal("StartedTransportError 必须被 IsStartedUpstreamTransportError 识别")
	}
	if IsStartedUpstreamTransportError(errors.New("普通错误")) {
		t.Fatal("普通错误不应被识别为 started 传输错误")
	}
	// Unwrap 链：包装错误保留底层信息。
	if !errors.Is(&PrimaryStartedGatewayTransportError{Err: started}, started) {
		t.Fatal("PrimaryStartedGatewayTransportError 必须透传 started 标记")
	}
	if !IsPrimaryStartedGatewayTransportError(&PrimaryStartedGatewayTransportError{Err: errors.New("x")}) {
		t.Fatal("PrimaryStartedGatewayTransportError 自身必须被识别")
	}
	bodyStarted := &StartedBodyTransportError{Err: errors.New("body 断流")}
	if !IsProvenUpstreamBodyTransportError(bodyStarted) {
		t.Fatal("StartedBodyTransportError 属于已证实的 body 传输失败")
	}
	// incomplete/pipe 包装的递归证明。
	proven := &UpstreamBodyReadIncompleteError{Cause: &StartedBodyTransportError{Err: errors.New("x")}}
	if !IsProvenUpstreamBodyTransportError(proven) {
		t.Fatal("Cause 为 started body 错误时必须递归证实")
	}
	piped := &NonStreamUpstreamBodyPipeError{OriginalError: proven, Message: "管道中断"}
	if !IsProvenUpstreamBodyTransportError(piped) {
		t.Fatal("OriginalError 为已证实错误时必须递归证实")
	}
	if IsProvenUpstreamBodyTransportError(&NonStreamUpstreamBodyPipeError{OriginalError: errors.New("未证实")}) {
		t.Fatal("未证实的原始错误不应通过管道错误递归证实")
	}
	if IsProvenUpstreamBodyTransportError(errors.New("普通错误")) {
		t.Fatal("普通错误不属于已证实的 body 传输失败")
	}
}

func TestGatewayResponsePrecommitDeadlinePredicate(t *testing.T) {
	if !IsGatewayResponsePrecommitDeadlineError(&GatewayResponsePrecommitDeadlineError{}) {
		t.Fatal("必须识别 precommit deadline 错误")
	}
	if IsGatewayResponsePrecommitDeadlineError(errors.New("x")) {
		t.Fatal("普通错误不应被识别")
	}
}

func TestGatewayFirstByteTimeoutPredicateAndSource(t *testing.T) {
	configured := &GatewayFirstByteTimeoutError{Source: FirstByteTimeoutSourceConfiguredDeadline}
	if !IsGatewayFirstByteTimeoutError(configured) {
		t.Fatal("必须识别首字超时错误")
	}
	if got := firstByteTimeoutSourceOf(configured); got != FirstByteTimeoutSourceConfiguredDeadline {
		t.Fatalf("source = %q", got)
	}
	hard := &GatewayFirstByteTimeoutError{Source: FirstByteTimeoutSourceHardTimeout}
	if got := firstByteTimeoutSourceOf(hard); got != FirstByteTimeoutSourceHardTimeout {
		t.Fatalf("source = %q", got)
	}
	if got := firstByteTimeoutSourceOf(errors.New("x")); got != "" {
		t.Fatalf("非首字超时错误的 source 应为空，got %q", got)
	}
}

func TestOpenAIOAuthCodexAdapterErrorConstruction(t *testing.T) {
	defaults := NewOpenAIOAuthCodexAdapterError("请求体无效")
	if defaults.Code != "invalid_openai_oauth_codex_request" || defaults.StatusCode != 400 || defaults.Type != "invalid_request_error" {
		t.Fatalf("默认值 = %#v", defaults)
	}
	custom := NewOpenAIOAuthCodexAdapterError("证明头无效",
		WithCodexAdapterCode("invalid_openai_oauth_codex_attestation"),
		WithCodexAdapterStatus(412),
		WithCodexAdapterType("attestation_error"),
		WithCodexAdapterAccountScoped(),
	)
	if custom.Code != "invalid_openai_oauth_codex_attestation" || custom.StatusCode != 412 || custom.Type != "attestation_error" || !custom.AccountScoped {
		t.Fatalf("自定义值 = %#v", custom)
	}
	if custom.Error() != "证明头无效" {
		t.Fatalf("Error() = %q", custom.Error())
	}
	if !IsOpenAIOAuthCodexAdapterError(errors.Join(errors.New("外层"), custom)) {
		t.Fatal("errors.Join 链上必须能识别 adapter 错误")
	}
	if IsOpenAIOAuthCodexAdapterError(errors.New("x")) {
		t.Fatal("普通错误不应被识别为 adapter 错误")
	}
}

func TestTimeoutLikeText(t *testing.T) {
	cases := map[string]bool{
		"":            false,
		"normal":      false,
		"TIMEOUT":     true,
		"ETIMEDOUT":   true,
		"req timedout": true,
		"timed out":   true,
		"请求超时":        true,
	}
	for text, want := range cases {
		if got := timeoutLikeText(text); got != want {
			t.Fatalf("timeoutLikeText(%q) = %v, 期望 %v", text, got, want)
		}
	}
}

func TestInt64CeilDiv(t *testing.T) {
	cases := []struct {
		value, divisor, want int64
	}{
		{9_500, 1_000, 10},
		{9_000, 1_000, 9},
		{1, 1_000, 1},
		{0, 1_000, 0},
		{5, 0, 5},
		{5, -1, 5},
	}
	for _, testCase := range cases {
		if got := int64CeilDiv(testCase.value, testCase.divisor); got != testCase.want {
			t.Fatalf("int64CeilDiv(%d, %d) = %d, 期望 %d", testCase.value, testCase.divisor, got, testCase.want)
		}
	}
}

func TestUpstreamDispatchConfigError(t *testing.T) {
	if errMissingCoordination.Error() != "fetchFirstAvailableUpstream requires shared request coordination context" {
		t.Fatalf("message = %q", errMissingCoordination.Error())
	}
}

func TestUpstreamAttemptClassifiers(t *testing.T) {
	if IsRealUpstreamAttempt(UpstreamAttempt{}) {
		t.Fatal("空 UpstreamURL 不算真实上游尝试")
	}
	if IsRealUpstreamAttempt(UpstreamAttempt{UpstreamURL: "account:locally_suppressed"}) {
		t.Fatal("非 http(s) URL 不算真实上游尝试")
	}
	if !IsRealUpstreamAttempt(UpstreamAttempt{UpstreamURL: "HTTP://upstream.example/v1"}) {
		t.Fatal("大写 scheme 的 http URL 属于真实上游尝试")
	}
	if !IsRealUpstreamAttempt(UpstreamAttempt{UpstreamURL: "https://upstream.example/v1"}) {
		t.Fatal("https URL 属于真实上游尝试")
	}
	if IsCompletedRealUpstreamAttempt(UpstreamAttempt{UpstreamURL: "https://u.example", Status: 0, HasStatus: false}) {
		t.Fatal("无状态不算已完成的真实尝试")
	}
	if !IsCompletedRealUpstreamAttempt(UpstreamAttempt{UpstreamURL: "https://u.example", Status: 500, HasStatus: true}) {
		t.Fatal("带状态的真实尝试应判定为完成")
	}
	if IsCompletedRealUpstreamAttempt(UpstreamAttempt{UpstreamURL: "https://u.example", Status: 200, HasStatus: true}) != true {
		t.Fatal("200 也算已完成（isCompletedRealUpstreamAttempt 只看 hasStatus && status>0）")
	}
}

func TestUtilPureHelpers(t *testing.T) {
	if _, ok := decodeJSONObject([]byte("not-json")); ok {
		t.Fatal("非法 JSON 不应解析成功")
	}
	if _, ok := decodeJSONObject([]byte("[1,2]")); ok {
		t.Fatal("JSON 数组不是对象")
	}
	object, ok := decodeJSONObject([]byte(`{"a":1}`))
	if !ok || object["a"] != float64(1) {
		t.Fatalf("decodeJSONObject 结果 = %#v ok=%v", object, ok)
	}
	if isPlainObjectValue("string") {
		t.Fatal("字符串不是 plain object")
	}
	if !isPlainObjectValue(map[string]any{}) {
		t.Fatal("map 应是 plain object")
	}
	uuid := uuid4String()
	if len(uuid) != 36 || uuid[14] != '4' {
		t.Fatalf("uuid4 格式错误: %q", uuid)
	}
	if trimString("  x  ") != "x" {
		t.Fatal("trimString 语义不符")
	}
	if sha256HexBytes([]byte("abc")) != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("sha256HexBytes 结果不符: %s", sha256HexBytes([]byte("abc")))
	}
	if jsonCloneValue(map[string]any{"a": 1}).(map[string]any)["a"] != float64(1) {
		t.Fatal("jsonCloneValue 应保持字段")
	}
}

func TestProviderProtocolTokens(t *testing.T) {
	if !IsGptVendorCode(" GPT ") {
		t.Fatal("GPT vendor code 应忽略大小写与空白")
	}
	if IsGptVendorCode("openai") {
		t.Fatal("openai 不是 gpt vendor")
	}
	if !isOpenAIProtocolProfileWith("openai", "v1") {
		t.Fatal("openai/v1 应是 openai 协议画像")
	}
	if isOpenAIProtocolProfileWith("anthropic", "v1") {
		t.Fatal("anthropic 不是 openai 协议画像")
	}
	if !isOpenAIProtocolProfileSecret("OpenAI", " V1 ") {
		t.Fatal("大小写与空白应归一化")
	}
	account := UpstreamHeaderAccount{ProtocolCode: "openai", ProtocolVersion: "v1"}
	if !isOpenAIProtocolProfile(account) {
		t.Fatal("账户投影判定失败")
	}
}

func TestCodexTurnAvoidanceHelpers(t *testing.T) {
	accounts := testAccounts("a-1", "a-2", "a-3")
	avoided := stringSet([]string{"a-1", "a-2"})
	if avoided == nil {
		t.Fatal("非空列表应生成集合")
	}
	if stringSet(nil) != nil {
		t.Fatal("空列表应返回 nil")
	}
	kept := filterCodexTurnAvoidedAccounts(accounts, avoided, false)
	if len(kept) != 1 || kept[0].ID != "a-3" {
		t.Fatalf("普通 pass 只保留非规避账户: %#v", accountIDs(kept))
	}
	reversed := filterCodexTurnAvoidedAccounts(accounts, avoided, true)
	if len(reversed) != 2 {
		t.Fatalf("反转 pass 只保留规避账户: %#v", accountIDs(reversed))
	}
	exhausted := nonRecoverableFailedAccountIDs(map[string]struct{}{"a-1": {}, "a-3": {}}, map[string]struct{}{"a-3": {}})
	if _, ok := exhausted["a-1"]; !ok {
		t.Fatal("a-1 非可恢复，应进入 exhausted")
	}
	if _, ok := exhausted["a-3"]; ok {
		t.Fatal("a-3 可恢复，不应进入 exhausted")
	}
	candidates := codexTurnReversalCandidates(accounts, avoided, exhausted)
	if len(candidates) != 1 || candidates[0].ID != "a-2" {
		t.Fatalf("reversal 候选 = %#v", accountIDs(candidates))
	}
}

func TestCooldownUntilActiveUsesInjectedClock(t *testing.T) {
	base := int64(1_000)
	previous := NowMs
	NowMs = func() int64 { return base }
	t.Cleanup(func() { NowMs = previous })

	// RFC3339 只有秒精度，未来/过去时间用整秒偏移构造。
	future := time.UnixMilli(base + 60_000).UTC().Format(time.RFC3339)
	past := time.UnixMilli(base - 60_000).UTC().Format(time.RFC3339)
	if !cooldownUntilActive(future) {
		t.Fatal("未来冷却时间应视为激活")
	}
	if cooldownUntilActive(past) {
		t.Fatal("过期冷却时间不应视为激活")
	}
	if cooldownUntilActive("not-a-date") {
		t.Fatal("无法解析的冷却时间不应视为激活")
	}
}

func TestShouldRecordAbortedUpstreamAttempt(t *testing.T) {
	if shouldRecordAbortedUpstreamAttempt(&UpstreamRequestAbortedError{Message: "请求已取消"}) {
		t.Fatal("未开始的取消不应记录 aborted 尝试")
	}
	if !shouldRecordAbortedUpstreamAttempt(&UpstreamRequestAbortedError{Message: "请求已取消", UpstreamRequestStarted: true}) {
		t.Fatal("已开始的取消应记录 aborted 尝试")
	}
	if shouldRecordAbortedUpstreamAttempt(errors.New("x")) {
		t.Fatal("非取消错误不应记录")
	}
}

func TestInProcessAPIKeyRotationCounterModuloLoop(t *testing.T) {
	counter := newInProcessAPIKeyRotationCounter()
	ctx := context.Background()
	for expected := 0; expected < 3; expected++ {
		index, err := counter.NextIndex(ctx, "acc", "round-robin", 3)
		if err != nil {
			t.Fatalf("NextIndex: %v", err)
		}
		if index != expected {
			t.Fatalf("第 %d 次取号 = %d", expected+1, index)
		}
	}
	if index, _ := counter.NextIndex(ctx, "acc", "round-robin", 0); index != 0 {
		t.Fatalf("modulo<=0 应返回 0, got %d", index)
	}
}

func TestNormalizeProviderTokenStrimsAndLowers(t *testing.T) {
	if normalizeProviderToken("  OpenAI ") != "openai" {
		t.Fatalf("normalizeProviderToken = %q", normalizeProviderToken("  OpenAI "))
	}
}

func TestHeaderMapOfAndHeaderToStringMap(t *testing.T) {
	if headerMapOf(nil) != nil {
		t.Fatal("nil 输入应返回 nil")
	}
	source := map[string]string{"A": "1"}
	if headerMapOf(source)["A"] != "1" {
		t.Fatal("非 nil 输入应原样返回")
	}
	joined := headerToStringMap(map[string][]string{"X-Multi": {"a", "b"}, "X-Empty": {}})
	if joined["X-Multi"] != "a, b" {
		t.Fatalf("X-Multi = %q", joined["X-Multi"])
	}
	if _, ok := joined["X-Empty"]; ok {
		t.Fatal("空值 header 不应输出")
	}
}
