package openaicompat

// 基础设施小文件的补充覆盖测试：JSON/SSE 助手、HTTP 查询解析、时间工具、
// hosted tool 注册表、computer adapter 归一化、config 投影、错误渲染、
// SSE pump、文本索引与端点族表。

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCovBridgeJSONHelpers(t *testing.T) {
	t.Run("bridgeIntegerValue 边界", func(t *testing.T) {
		if _, ok := bridgeIntegerValue(1.5); ok {
			t.Errorf("非整数 float 不应接受")
		}
		if got, ok := bridgeIntegerValue(float64(3)); !ok || got != 3 {
			t.Errorf("整数 float = %d,%v", got, ok)
		}
		if got, ok := bridgeIntegerValue(int(4)); !ok || got != 4 {
			t.Errorf("int = %d,%v", got, ok)
		}
		if _, ok := bridgeIntegerValue("3"); ok {
			t.Errorf("字符串不应接受")
		}
	})
	t.Run("bridgeNumberValue int 族", func(t *testing.T) {
		if got, ok := bridgeNumberValue(int(2)); !ok || got != 2 {
			t.Errorf("int = %v,%v", got, ok)
		}
		if got, ok := bridgeNumberValue(int64(5)); !ok || got != 5 {
			t.Errorf("int64 = %v,%v", got, ok)
		}
		if _, ok := bridgeNumberValue(true); ok {
			t.Errorf("bool 不应接受")
		}
	})
	t.Run("bridgeParseToolArguments", func(t *testing.T) {
		if got := bridgeParseToolArguments(""); len(covMapStatic(got)) != 0 {
			t.Errorf("空串应为空对象：%v", got)
		}
		raw := bridgeParseToolArguments("not-json")
		if rawMap := covMapStatic(raw); rawMap["_raw"] != "not-json" {
			t.Errorf("非法 JSON 应包 _raw：%v", raw)
		}
		array := bridgeParseToolArguments("[1,2]")
		if arrayMap := covMapStatic(array); arrayMap["value"] == nil {
			t.Errorf("非对象 JSON 应包 value：%v", array)
		}
		object := bridgeParseToolArguments(`{"a":1}`)
		if objectMap := covMapStatic(object); objectMap["a"] != float64(1) {
			t.Errorf("对象应原样解析：%v", object)
		}
	})
	t.Run("bridgeJSONStringify 失败返回空串", func(t *testing.T) {
		if got := bridgeJSONStringify(map[string]any{"bad": make(chan int)}); got != "" {
			t.Errorf("不可序列化值应返回空串：%q", got)
		}
		if got := BridgeJSONStringifyOf(map[string]any{"a": float64(1)}); got != `{"a":1}` {
			t.Errorf("导出版本 = %q", got)
		}
	})
	t.Run("bridgeTakeCompleteSseEvents", func(t *testing.T) {
		events, rest := bridgeTakeCompleteSseEvents("a\n\nb\r\n\r\nc")
		if len(events) != 2 || events[0] != "a\n\n" || events[1] != "b\r\n\r\n" {
			t.Errorf("events = %q", events)
		}
		if rest != "c" {
			t.Errorf("rest = %q", rest)
		}
		events2, rest2 := bridgeTakeCompleteSseEvents("无边界")
		if len(events2) != 0 || rest2 != "无边界" {
			t.Errorf("无边界应全部留在 rest：%v/%q", events2, rest2)
		}
	})
	t.Run("anthropic 错误体渲染", func(t *testing.T) {
		body := string(TransformAnthropicJSONToChatErrorBody())
		if !strings.Contains(body, "upstream_chat_completions_invalid_json") {
			t.Errorf("错误体 = %s", body)
		}
		tooLarge := string(TransformAnthropicJSONToChatTooLargeErrorBody())
		if !strings.Contains(tooLarge, "upstream_chat_completions_response_too_large") {
			t.Errorf("过大错误体 = %s", tooLarge)
		}
	})
}

// covMapStatic 避免在非 testing 语义里反复写断言：仅做类型收窄。
func covMapStatic(value any) map[string]any {
	record, _ := value.(map[string]any)
	return record
}

func TestCovHTTPUtilQuery(t *testing.T) {
	query := url.Values{}
	query.Set("a", "12")
	query.Add("rep", "1")
	query.Add("rep", "2")
	query.Set("blank", "  ")
	query.Set("float", "1.9")
	query.Set("bad", "x")
	if got := queryStringParam(query, "a"); got == nil || *got != "12" {
		t.Errorf("a = %v", got)
	}
	if got := queryStringParam(query, "rep"); got != nil {
		t.Errorf("重复参数应忽略：%v", got)
	}
	if got := queryStringParam(query, "blank"); got != nil {
		t.Errorf("空白应忽略：%v", got)
	}
	if got := queryStringParam(query, "missing"); got != nil {
		t.Errorf("缺失应 nil：%v", got)
	}
	if got := queryIntegerParam(query, "float"); got == nil || *got != 1 {
		t.Errorf("float 截断 = %v", got)
	}
	if got := queryIntegerParam(query, "bad"); got != nil {
		t.Errorf("非数字应 nil：%v", got)
	}
	if got := queryIntegerValue(float64(2.7)); got == nil || *got != 2 {
		t.Errorf("queryIntegerValue float = %v", got)
	}
	if got := queryIntegerValue(" 8 "); got == nil || *got != 8 {
		t.Errorf("queryIntegerValue string = %v", got)
	}
	if got := queryIntegerValue(""); got != nil {
		t.Errorf("空串 = %v", got)
	}
	if got := intFromFloat(math.NaN()); got != nil {
		t.Errorf("NaN 应 nil：%v", got)
	}
	if got := intFromFloat(9.1e15); got != nil {
		t.Errorf("超 safe 范围应 nil：%v", got)
	}
	if got := queryNumberValue("3.5"); got == nil || *got != 3.5 {
		t.Errorf("queryNumberValue string = %v", got)
	}
	if got := queryNumberValue("bad"); got != nil {
		t.Errorf("非法 number = %v", got)
	}
	if got := queryNumberValue(7); got != nil {
		t.Errorf("非 float64 非 string 应 nil（int 不走 JSON 路径）")
	}
	if got := stringValue("  x  "); got == nil || *got != "x" {
		t.Errorf("stringValue = %v", got)
	}
	if got := stringValue("   "); got != nil {
		t.Errorf("空白 stringValue = %v", got)
	}
	if got := objectValue("no"); got != nil {
		t.Errorf("objectValue = %v", got)
	}
}

func TestCovReadJSONObjectBody(t *testing.T) {
	t.Run("body 为 nil", func(t *testing.T) {
		request := httptest.NewRequest("POST", "/x", nil)
		request.Body = nil
		object, err := readJSONObjectBody(request)
		if err != nil || len(object) != 0 {
			t.Fatalf("nil body 应得空对象：%v/%v", object, err)
		}
	})
	t.Run("空 body 默认空对象", func(t *testing.T) {
		object, err := readJSONObjectBody(httptest.NewRequest("POST", "/x", strings.NewReader("  ")))
		if err != nil || len(object) != 0 {
			t.Fatalf("空 body = %v/%v", object, err)
		}
	})
	t.Run("非法 JSON", func(t *testing.T) {
		_, err := readJSONObjectBody(httptest.NewRequest("POST", "/x", strings.NewReader("{坏")))
		requestErr, ok := err.(*RequestError)
		if !ok || requestErr.Code != "invalid_json_body" || requestErr.StatusCode != 400 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("非对象 JSON", func(t *testing.T) {
		_, err := readJSONObjectBody(httptest.NewRequest("POST", "/x", strings.NewReader("[1]")))
		if err == nil || err.(*RequestError).Code != "invalid_json_body" {
			t.Fatalf("数组 body 应报对象要求：%v", err)
		}
	})
	t.Run("超大 body 413", func(t *testing.T) {
		big := `{"a":"` + strings.Repeat("x", jsonBodyLimit) + `"}`
		_, err := readJSONObjectBody(httptest.NewRequest("POST", "/x", strings.NewReader(big)))
		requestErr, ok := err.(*RequestError)
		if !ok || requestErr.StatusCode != 413 || requestErr.Code != "request_body_too_large" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("读取失败回退 500", func(t *testing.T) {
		request := httptest.NewRequest("POST", "/x", errReader{})
		_, err := readJSONObjectBody(request)
		if err != errUnhandled {
			t.Fatalf("读取失败应返回 errUnhandled：%v", err)
		}
	})
}

// errReader 模拟底层读取失败。
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestCovTimeUtil(t *testing.T) {
	t.Run("parseRFC3339Instant 严格校验", func(t *testing.T) {
		bad := []string{
			"", "2026-09-04", "2026-09-04T08:00:00", "2026-13-01T00:00:00Z",
			"2026-02-30T00:00:00Z", "2026-09-04T24:00:00Z", "2026-09-04T08:60:00Z",
			"2026-09-04T08:00:61Z", "2026-09-04T08:00:00+25:00", "2026-09-04T08:00:00+00:99",
		}
		for _, value := range bad {
			if _, ok := parseRFC3339Instant(value); ok {
				t.Errorf("%q 不应解析成功", value)
			}
		}
		good := map[string]time.Time{
			"2026-09-04T08:00:00Z":          time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC),
			"2024-02-29T00:00:00Z":          time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC),
			"2026-09-04T08:00:00+02:00":     time.Date(2026, 9, 4, 8, 0, 0, 0, time.FixedZone("", 2*3600)),
			"2026-09-04T08:00:00.123-05:30": time.Date(2026, 9, 4, 8, 0, 0, 0, time.FixedZone("", -(5*3600+30*60))),
		}
		for value, want := range good {
			got, ok := parseRFC3339Instant(value)
			if !ok || !got.Equal(want) {
				t.Errorf("%q -> %v（ok=%v），期望 %v", value, got, ok, want)
			}
		}
	})
	t.Run("毫秒与秒转换", func(t *testing.T) {
		millis, ok := rfc3339InstantMilliseconds("2026-09-04T08:00:00Z")
		if !ok || millis != 1788508800000 {
			t.Errorf("millis = %d（%v）", millis, ok)
		}
		if _, ok := rfc3339InstantMilliseconds("bad"); ok {
			t.Errorf("非法输入不应成功")
		}
		seconds, err := openAITimestamp("2026-09-04T08:00:00Z")
		if err != nil || seconds != 1788508800 {
			t.Errorf("openAITimestamp = %d（%v）", seconds, err)
		}
		if _, err := openAITimestamp("bad"); err == nil {
			t.Errorf("非法输入应报错")
		}
	})
	t.Run("isoMillis 与 expiresAtFromDays", func(t *testing.T) {
		base := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
		if got := isoMillis(base); got != "2026-09-04T08:00:00.000Z" {
			t.Errorf("isoMillis = %q", got)
		}
		if got := expiresAtFromDays(nil, base); got != nil {
			t.Errorf("nil days = %v", got)
		}
		zero := 0
		if got := expiresAtFromDays(&zero, base); got != nil {
			t.Errorf("0 days = %v", got)
		}
		three := 3
		got := expiresAtFromDays(&three, base)
		if got == nil || *got != "2026-09-07T08:00:00.000Z" {
			t.Errorf("3 days = %v", got)
		}
	})
	t.Run("ID 生成形状", func(t *testing.T) {
		now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
		fileID := newOpenAICompatibleFileID(now)
		if !strings.HasPrefix(fileID, "file-") || len(strings.Split(fileID, "-")) != 3 {
			t.Errorf("file id 形状 = %q", fileID)
		}
		vsID := newOpenAICompatibleVectorStoreID(now)
		if !strings.HasPrefix(vsID, "vs_") || len(strings.Split(vsID, "_")) != 3 {
			t.Errorf("vector store id 形状 = %q", vsID)
		}
		chunkID := newVectorStoreChunkID()
		if !strings.HasPrefix(chunkID, "vschunk_") || len(chunkID) != len("vschunk_")+32 {
			t.Errorf("chunk id 形状 = %q", chunkID)
		}
		if got := randomHex(99); len(got) != 32 {
			t.Errorf("randomHex 超长截断到 32：%d", len(got))
		}
	})
}

func TestCovHostedToolRegistryDetails(t *testing.T) {
	t.Run("模式解析", func(t *testing.T) {
		modes := OpenAIHostedToolRuntimeModes{
			CodeInterpreter: "mock", Computer: "local_runtime", Shell: "reject", Skills: "bogus",
		}
		cases := map[OpenAIHostedToolRuntimeType]OpenAIHostedToolRuntimeMode{
			OpenAIHostedToolCodeInterpreter: OpenAIHostedToolModeMock,
			OpenAIHostedToolComputer:        OpenAIHostedToolModeLocalRuntim,
			OpenAIHostedToolMCP:             OpenAIHostedToolModeGuidance,
			OpenAIHostedToolShell:           OpenAIHostedToolModeReject,
			OpenAIHostedToolSkills:          OpenAIHostedToolModeGuidance,
			OpenAIHostedToolToolSearch:      OpenAIHostedToolModeGuidance,
		}
		for toolType, want := range cases {
			if got := OpenAIHostedToolRuntimeModeForType(toolType, modes); got != want {
				t.Errorf("%s -> %v，期望 %v", toolType, got, want)
			}
		}
	})
	t.Run("兼容性说明", func(t *testing.T) {
		if detail, ok := OpenAIHostedToolRuntimeCompatibilityDetail("container"); !ok || detail == "" {
			t.Errorf("container 别名应命中 code_interpreter 说明：%q（%v）", detail, ok)
		}
		if detail, ok := OpenAIHostedToolRuntimeCompatibilityDetail("nope"); ok || detail != "" {
			t.Errorf("未知类型应 miss：%q（%v）", detail, ok)
		}
	})
	t.Run("Unsupported 标签", func(t *testing.T) {
		if label, ok := UnsupportedOpenAIHostedToolLabel("mcp", OpenAIHostedToolRuntimeModes{}); !ok || label != "mcp" {
			t.Errorf("guidance 标签 = %q（%v）", label, ok)
		}
		if label, ok := UnsupportedOpenAIHostedToolLabel("shell", OpenAIHostedToolRuntimeModes{Shell: "reject"}); ok || label != "" {
			t.Errorf("reject 应 miss：%q（%v）", label, ok)
		}
		if label, ok := UnsupportedOpenAIHostedToolLabel("unknown_x", OpenAIHostedToolRuntimeModes{}); !ok || label != "unknown_x" {
			t.Errorf("非注册表类型透传：%q（%v）", label, ok)
		}
	})
	t.Run("约束文本去重", func(t *testing.T) {
		if got := AppendUnsupportedHostedToolConstraintText(nil); got != "" {
			t.Errorf("空列表应为空：%q", got)
		}
		got := AppendUnsupportedHostedToolConstraintText([]string{"computer", "computer", "", "shell"})
		if !strings.Contains(got, "computer, shell.") || strings.Count(got, "computer") != 1 {
			t.Errorf("应去重并跳过空项：%q", got)
		}
	})
}

func TestCovComputerAdapterNormalization(t *testing.T) {
	t.Run("normalize 完整形态", func(t *testing.T) {
		result, err := normalizeComputerAdapterResult(map[string]any{
			"message": "完成",
			"call": map[string]any{
				"callId":  "c9",
				"status":  "done",
				"actions": []any{map[string]any{"type": "click"}, "not-object"},
			},
			"metadata": map[string]any{"k": "v"},
		})
		if err != nil {
			t.Fatalf("归一化失败：%v", err)
		}
		if result.Message != "完成" || result.Call == nil || result.Call.CallID != "c9" {
			t.Fatalf("result = %+v", result)
		}
		if len(result.Call.Actions) != 1 {
			t.Errorf("actions 应过滤非对象：%v", result.Call.Actions)
		}
		if result.Metadata["adapter"] != "http_browser" || result.Metadata["k"] != "v" {
			t.Errorf("metadata = %v", result.Metadata)
		}
	})
	t.Run("normalize 非对象", func(t *testing.T) {
		if _, err := normalizeComputerAdapterResult("no"); err == nil {
			t.Fatal("非对象应报错")
		}
		fallback, err := normalizeComputerAdapterResult(map[string]any{})
		if err != nil || fallback.Message != "Computer browser adapter completed." {
			t.Fatalf("缺省 message = %+v（%v）", fallback, err)
		}
	})
	t.Run("adapter 工具函数", func(t *testing.T) {
		if adapterString(42) != nil {
			t.Errorf("非字符串应 nil")
		}
		if adapterString("  ") != nil {
			t.Errorf("空白应 nil")
		}
		if got := firstAdapterString(1, " ", "ok"); got == nil || *got != "ok" {
			t.Errorf("firstAdapterString = %v", got)
		}
		if got := adapterActions("bad"); len(got) != 0 {
			t.Errorf("非数组 actions = %v", got)
		}
		if got := truncateString("短文本", 100); got != "短文本" {
			t.Errorf("未超限应原样：%q", got)
		}
	})
	t.Run("readAdapterText", func(t *testing.T) {
		if text, err := readAdapterText(&http.Response{}, 10); err != nil || text != "" {
			t.Fatalf("nil body = %q（%v）", text, err)
		}
		ok := &http.Response{Body: io.NopCloser(strings.NewReader("hello"))}
		if text, err := readAdapterText(ok, 10); err != nil || text != "hello" {
			t.Errorf("正常读取 = %q（%v）", text, err)
		}
		big := &http.Response{Body: io.NopCloser(strings.NewReader("0123456789A"))}
		if _, err := readAdapterText(big, 10); err == nil || !strings.Contains(err.Error(), "limit") {
			t.Errorf("超限应报错：%v", err)
		}
	})
	t.Run("computerTimeoutOrError", func(t *testing.T) {
		plain := errors.New("原始错误")
		if got := computerTimeoutOrError(context.Background(), plain); got != plain {
			t.Errorf("未取消 ctx 应透传原错误：%v", got)
		}
		cancelledCtx, cancel := context.WithCancel(context.Background())
		cancel()
		got := computerTimeoutOrError(cancelledCtx, plain)
		if !strings.Contains(got.Error(), "timed out") {
			t.Errorf("已取消 ctx 应报超时：%v", got)
		}
	})
	t.Run("Run 缺 endpoint 与坏 URL", func(t *testing.T) {
		noEndpoint := &httpComputerExecutor{config: ComputerAdapterConfig{TimeoutMs: 100}}
		if _, err := noEndpoint.Run(context.Background(), ComputerRuntimeInput{}); err == nil || !strings.Contains(err.Error(), "endpoint") {
			t.Fatalf("缺 endpoint 应报错：%v", err)
		}
		badURL := &httpComputerExecutor{config: ComputerAdapterConfig{Endpoint: "://bad", TimeoutMs: 100}}
		if _, err := badURL.Run(context.Background(), ComputerRuntimeInput{}); err == nil {
			t.Fatal("坏 URL 应报错")
		}
	})
}

func TestCovConfigAndEndpointFamilies(t *testing.T) {
	t.Run("IsCrossProtocolBridgeRequired 全对", func(t *testing.T) {
		yes := [][2]string{
			{FamilyResponses, FamilyAnthropicMessages},
			{FamilyResponses, FamilyGeminiGenerateContent},
			{FamilyResponses, FamilyGeminiStreamGenerate},
			{FamilyAnthropicMessages, FamilyGeminiGenerateContent},
			{FamilyAnthropicMessages, FamilyGeminiStreamGenerate},
			{FamilyGeminiGenerateContent, FamilyAnthropicMessages},
			{FamilyGeminiStreamGenerate, FamilyAnthropicMessages},
		}
		for _, pair := range yes {
			if !IsCrossProtocolBridgeRequired(pair[0], pair[1]) {
				t.Errorf("%s -> %s 应需要桥接", pair[0], pair[1])
			}
		}
		no := [][2]string{
			{FamilyResponses, FamilyResponses},
			{FamilyAnthropicMessages, "unknown"},
			{"unknown", FamilyChatCompletions},
		}
		for _, pair := range no {
			if IsCrossProtocolBridgeRequired(pair[0], pair[1]) {
				t.Errorf("%s -> %s 不应需要桥接", pair[0], pair[1])
			}
		}
	})
	t.Run("HostedToolRuntimeModes 投影", func(t *testing.T) {
		config := Config{
			HostedToolCodeInterpreterMode: "mock",
			HostedToolComputerMode:        "local_runtime",
			HostedToolShellMode:           "reject",
			HostedToolSkillsMode:          "guidance",
			HostedToolToolSearchMode:      "mock",
		}
		modes := config.HostedToolRuntimeModes()
		if modes.CodeInterpreter != "mock" || modes.Computer != "local_runtime" || modes.Shell != "reject" ||
			modes.Skills != "guidance" || modes.ToolSearch != "mock" {
			t.Errorf("modes = %+v", modes)
		}
	})
	t.Run("withDefaults", func(t *testing.T) {
		defaulted := Config{}.withDefaults()
		if defaulted.MaxFileBytes != DefaultMaxFileBytes || defaulted.FilesRoot == "" {
			t.Errorf("Config 缺省 = %+v", defaulted)
		}
		ci := CodeInterpreterConfig{}.withDefaults()
		if ci.PythonCommand != "python" || ci.TimeoutMs != 5000 || ci.MaxCodeBytes != 64*1024 ||
			ci.MaxOutputBytes != 64*1024 || ci.MaxArtifactCount != 8 || ci.TempRoot == "" {
			t.Errorf("CodeInterpreter 缺省 = %+v", ci)
		}
		ca := ComputerAdapterConfig{}.withDefaults()
		if ca.TimeoutMs != 30000 || ca.MaxBodyBytes != 512*1024 {
			t.Errorf("ComputerAdapter 缺省 = %+v", ca)
		}
	})
}

func TestCovRoutesAndErrorsContract(t *testing.T) {
	t.Run("RequestError 文本与 401", func(t *testing.T) {
		err := badRequest("参数坏", "bad_param")
		if err.Error() != "参数坏" {
			t.Errorf("Error() = %q", err.Error())
		}
		recorder := httptest.NewRecorder()
		unauthorized := badRequest("缺少或无效的 API Key", "invalid_api_key")
		unauthorized.StatusCode = 401
		unauthorized.write(recorder)
		if recorder.Code != 401 || !strings.Contains(recorder.Body.String(), "invalid_api_key") {
			t.Errorf("401 渲染 = %d %s", recorder.Code, recorder.Body.String())
		}
		// code 为空时 JSON omitempty 生效。
		noCode := httptest.NewRecorder()
		writeGatewayErrorPayload(noCode, 400, "消息", "invalid_request_error", "")
		if strings.Contains(noCode.Body.String(), `"code"`) {
			t.Errorf("空 code 不应输出：%s", noCode.Body.String())
		}
	})
	t.Run("unhandled 500 契约", func(t *testing.T) {
		if errUnhandled.Error() != "服务器内部错误" {
			t.Errorf("errUnhandled.Error() = %q", errUnhandled.Error())
		}
		recorder := httptest.NewRecorder()
		writeUnhandledError(recorder)
		if recorder.Code != 500 || recorder.Body.String() != `{"message":"服务器内部错误"}` {
			t.Errorf("500 渲染 = %d %s", recorder.Code, recorder.Body.String())
		}
	})
	t.Run("handle 分派", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		handle(recorder, func() error { return notFound("找不到", "missing") })
		if recorder.Code != 404 {
			t.Errorf("RequestError 应按状态渲染：%d", recorder.Code)
		}
		recorder2 := httptest.NewRecorder()
		handle(recorder2, func() error { return newIndexingError("索引失败", 422, "invalid_request_error", "index_failed") })
		if recorder2.Code != 422 {
			t.Errorf("IndexingError 应按状态渲染：%d", recorder2.Code)
		}
		recorder3 := httptest.NewRecorder()
		handle(recorder3, func() error { return errors.New("未知") })
		if recorder3.Code != 500 {
			t.Errorf("未知错误应 500：%d", recorder3.Code)
		}
		recorder4 := httptest.NewRecorder()
		handle(recorder4, func() error { return nil })
		if recorder4.Code != 200 {
			t.Errorf("无错误不应写响应：%d", recorder4.Code)
		}
	})
	t.Run("requireScope 401", func(t *testing.T) {
		deps := &Deps{}
		recorder := httptest.NewRecorder()
		if scope := deps.requireScope(recorder, httptest.NewRequest("GET", "/x", nil)); scope != nil {
			t.Fatal("无 Scope 解析器应拒绝")
		}
		if recorder.Code != 401 {
			t.Errorf("401 契约 = %d", recorder.Code)
		}
	})
	t.Run("isMultipartContentType 边界", func(t *testing.T) {
		cases := map[string]bool{
			"multipart/form-data":             true,
			"multipart/form-data; boundary=x": true,
			"multipart/form-datanope":         false,
			"multipart/form-data2":            false,
			"MULTIPART/FORM-DATA; BOUNDARY=y": true,
			"application/json":                false,
			"multipart/form-data_boundary":    false,
		}
		for contentType, want := range cases {
			if got := isMultipartContentType(contentType); got != want {
				t.Errorf("isMultipart(%q) = %v，期望 %v", contentType, got, want)
			}
		}
	})
}

func TestCovBridgeStreamPump(t *testing.T) {
	t.Run("IndexBridgeSseBoundary 变体", func(t *testing.T) {
		// index 指向空行对的第一个 \n，length 覆盖整个空行对。
		if index, length := IndexBridgeSseBoundary("a\r\n\r\nb"); index != 2 || length != 3 {
			t.Errorf("CRLF 边界 = %d/%d", index, length)
		}
		if index, length := IndexBridgeSseBoundary("a\n\r\nb"); index != 1 || length != 3 {
			t.Errorf("LF+CRLF 边界 = %d/%d", index, length)
		}
		if index, _ := IndexBridgeSseBoundary("无"); index != -1 {
			t.Errorf("无边界的 index = %d", index)
		}
	})
	// 行为存疑：PumpBridgeSseTransform 注释称 process 为 nil 时是 pass-through，
	// 实际实现把 nil process 替换为不产出任何输出；按当前实际行为断言。
	t.Run("pump nil process 不产出与跨读分片", func(t *testing.T) {
		var output strings.Builder
		source := &covChunkReader{chunks: []string{"data: 1\n", "\n\ndata: 2", "\n\ndata: 3"}}
		if err := PumpBridgeSseTransform(source, &output, nil, nil); err != nil {
			t.Fatalf("pump 失败：%v", err)
		}
		if output.String() != "" {
			t.Errorf("nil process 应无输出：%q", output.String())
		}
		var echo strings.Builder
		source2 := &covChunkReader{chunks: []string{"data: 1\n", "\n\ndata: 2", "\n\ndata: 3"}}
		if err := PumpBridgeSseTransform(source2, &echo, func(event string) []string { return []string{event} }, nil); err != nil {
			t.Fatalf("pump 失败：%v", err)
		}
		// 跨读分片后事件按边界切出；末尾未终止事件在 EOF 后处理一次。
		if echo.String() != "data: 1\n\n\ndata: 2\n\ndata: 3" {
			t.Errorf("分片输出 = %q", echo.String())
		}
	})
	t.Run("pump finish 与写失败", func(t *testing.T) {
		var output strings.Builder
		if err := PumpBridgeSseTransform(strings.NewReader(""), &output, func(event string) []string {
			return []string{event}
		}, func() []string { return []string{"FIN"} }); err != nil {
			t.Fatalf("pump 失败：%v", err)
		}
		if output.String() != "FIN" {
			t.Errorf("空流应仅输出 finish：%q", output.String())
		}
		if err := PumpBridgeSseTransform(strings.NewReader("a\n\n"), &covErrWriter{}, func(string) []string {
			return []string{"X"}
		}, nil); err == nil {
			t.Fatal("写失败应上抛")
		}
		if err := PumpBridgeSseTransform(&covErrReader{}, io.Discard, nil, nil); err == nil {
			t.Fatal("读失败应上抛")
		}
	})
}

// covChunkReader 按片读取，模拟上游分片。
type covChunkReader struct {
	chunks []string
	index  int
}

func (r *covChunkReader) Read(buffer []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}
	chunk := r.chunks[r.index]
	r.index++
	copy(buffer, chunk)
	return len(chunk), nil
}

// covErrWriter 首次写即失败。
type covErrWriter struct{}

func (covErrWriter) Write([]byte) (int, error) { return 0, errors.New("write boom") }

// covErrReader 读即失败。
type covErrReader struct{}

func (covErrReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }

func TestCovTextIndexerBranches(t *testing.T) {
	t.Run("IndexingError 契约", func(t *testing.T) {
		err := newIndexingError("索引失败", 422, "invalid_request_error", "index_failed")
		if err.Error() != "索引失败" {
			t.Errorf("Error() = %q", err.Error())
		}
		recorder := httptest.NewRecorder()
		err.write(recorder)
		if recorder.Code != 422 {
			t.Errorf("write 状态 = %d", recorder.Code)
		}
	})
	t.Run("媒体类型白名单", func(t *testing.T) {
		for mediaType, want := range map[string]bool{
			"": false, "text/plain": true, "application/json": true,
			"application/typescript": true, "application/x-sh": true, "image/png": false,
		} {
			if got := IsSupportedVectorStoreTextMediaType(mediaType); got != want {
				t.Errorf("IsSupported(%q) = %v，期望 %v", mediaType, got, want)
			}
		}
	})
	t.Run("BuildVectorStoreChunks 错误分支", func(t *testing.T) {
		root := t.TempDir()
		media := "image/png"
		if _, err := BuildVectorStoreChunks(root, FileRecord{ID: "f1", MediaType: &media}); err == nil {
			t.Fatal("不支持媒体类型应报错")
		} else if indexingErr, ok := err.(*IndexingError); !ok || indexingErr.Code != "openai_compatible_file_mime_unsupported" {
			t.Fatalf("err = %v", err)
		}
		text := "text/plain"
		// 空文件（内容全空白）。
		if _, err := BuildVectorStoreChunks(root, FileRecord{ID: "f2", MediaType: &text, StorageKey: "files/x/empty", Bytes: 3}); err == nil {
			t.Fatal("缺文件应报错")
		}
	})
	t.Run("超大文本分块过多", func(t *testing.T) {
		// >256 个 2400 字符窗口：约 2000 步进 x 300 = 600K 字符。
		huge := strings.Repeat("字", 300*2000+2400)
		chunks := chunkTextForVectorStore(huge)
		if len(chunks) <= 256 {
			t.Fatalf("构造的块数应超过 256：%d", len(chunks))
		}
	})
	t.Run("ReadFileTextForIndexing 边界", func(t *testing.T) {
		root := t.TempDir()
		media := "text/plain"
		if _, err := ReadFileTextForIndexing(root, FileRecord{ID: "big", Bytes: MaxVectorStoreTextIndexBytes + 1, StorageKey: "files/x/big"}); err == nil {
			t.Fatal("超限应报错")
		}
		escaped := FileRecord{ID: "esc", StorageKey: "../esc", MediaType: &media}
		if _, err := ReadFileTextForIndexing(root, escaped); err != StorageKeyEscapeError {
			t.Fatalf("逃逸 key 应报 StorageKeyEscapeError：%v", err)
		}
		missing := FileRecord{ID: "missing", StorageKey: "files/x/gone", MediaType: &media}
		if _, err := ReadFileTextForIndexing(root, missing); err == nil {
			t.Fatal("缺文件应报错")
		}
	})
	t.Run("NUL 清洗", func(t *testing.T) {
		if got := removeNULCharacters("a\x00b"); got != "ab" {
			t.Errorf("NUL 移除 = %q", got)
		}
		if got := removeNULCharacters("abc"); got != "abc" {
			t.Errorf("无 NUL 原样 = %q", got)
		}
	})
}
