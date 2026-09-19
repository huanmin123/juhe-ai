package openaicompatcore

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRequestErrorContract(t *testing.T) {
	err := BadRequest("参数坏", "bad_param")
	if err.Error() != "参数坏" {
		t.Errorf("Error() = %q", err.Error())
	}
	if err.StatusCode != 400 || err.Type != "invalid_request_error" || err.Code != "bad_param" {
		t.Errorf("BadRequest 默认值 = %+v", err)
	}
	notFoundErr := NotFound("缺失", "missing")
	if notFoundErr.StatusCode != 404 {
		t.Errorf("NotFound 状态码 = %d", notFoundErr.StatusCode)
	}
	if NewRequestError("自定义", 418, "custom_error", "code").StatusCode != 418 {
		t.Errorf("NewRequestError 未保留状态码")
	}

	recorder := httptest.NewRecorder()
	unauthorized := BadRequest("缺少或无效的 API Key", "invalid_api_key")
	unauthorized.StatusCode = 401
	unauthorized.Write(recorder)
	body := recorder.Body.String()
	if recorder.Code != 401 || !strings.Contains(body, "invalid_api_key") {
		t.Errorf("401 渲染 = %d %s", recorder.Code, body)
	}
	if recorder.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", recorder.Header().Get("Content-Type"))
	}

	// code 为空时 JSON omitempty 生效。
	noCode := httptest.NewRecorder()
	WriteGatewayErrorPayload(noCode, 400, "消息", "invalid_request_error", "")
	if strings.Contains(noCode.Body.String(), `"code"`) {
		t.Errorf("空 code 不应序列化: %s", noCode.Body.String())
	}
}

func TestUnhandledErrorContract(t *testing.T) {
	if ErrUnhandled.Error() != "服务器内部错误" {
		t.Errorf("ErrUnhandled.Error() = %q", ErrUnhandled.Error())
	}
	recorder := httptest.NewRecorder()
	WriteUnhandledError(recorder)
	if recorder.Code != 500 || recorder.Body.String() != `{"message":"服务器内部错误"}` {
		t.Errorf("writeUnhandledError 渲染 = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestBridgeRequestErrorContract(t *testing.T) {
	err := BridgeError("校验失败", "invalid_tool", 400, "invalid_request_error")
	if err.Error() != "校验失败" || err.Code != "invalid_tool" || err.StatusCode != 400 || err.Type != "invalid_request_error" {
		t.Errorf("BridgeError = %+v", err)
	}
}

func TestConfigWithDefaults(t *testing.T) {
	defaulted := Config{}.WithDefaults()
	if defaulted.MaxFileBytes != DefaultMaxFileBytes || defaulted.FilesRoot != "data/openai-compatible-files" {
		t.Errorf("Config 缺省 = %+v", defaulted)
	}
	ci := defaulted.CodeInterpreter
	if ci.PythonCommand != "python" || ci.TimeoutMs != 5000 || ci.MaxCodeBytes != 64*1024 ||
		ci.MaxOutputBytes != 64*1024 || ci.MaxArtifactCount != 8 || ci.MaxArtifactBytes != 256*1024 ||
		ci.TempRoot != "data/code-interpreter-tmp" {
		t.Errorf("CodeInterpreter 缺省 = %+v", ci)
	}
	ca := defaulted.ComputerAdapter
	if ca.TimeoutMs != 30000 || ca.MaxBodyBytes != 512*1024 {
		t.Errorf("ComputerAdapter 缺省 = %+v", ca)
	}
	// 非零值不回填。
	kept := Config{MaxFileBytes: 1, FilesRoot: "custom"}.WithDefaults()
	if kept.MaxFileBytes != 1 || kept.FilesRoot != "custom" {
		t.Errorf("非零值被覆盖 = %+v", kept)
	}
}

func TestHostedToolRuntimeModesProjection(t *testing.T) {
	modes := Config{
		HostedToolCodeInterpreterMode: "local_runtime",
		HostedToolComputerMode:        "mock",
	}.HostedToolRuntimeModes()
	if modes.CodeInterpreter != "local_runtime" || modes.Computer != "mock" || modes.Shell != "" {
		t.Errorf("投影 = %+v", modes)
	}
}

func TestNormalizeEndpointFamily(t *testing.T) {
	cases := map[string]string{
		"messages":               FamilyAnthropicMessages,
		"generate_content":       FamilyGeminiGenerateContent,
		"stream_generate_content": FamilyGeminiStreamGenerate,
		"chat_completions":       FamilyChatCompletions,
		"unknown":                "unknown",
	}
	for input, want := range cases {
		if got := NormalizeEndpointFamily(input); got != want {
			t.Errorf("NormalizeEndpointFamily(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIsCrossProtocolBridgeRequired(t *testing.T) {
	if !IsCrossProtocolBridgeRequired("messages", FamilyChatCompletions) {
		t.Errorf("存储行 token 应可判定跨协议")
	}
	if IsCrossProtocolBridgeRequired(FamilyChatCompletions, FamilyChatCompletions) {
		t.Errorf("同协议不应桥接")
	}
	if IsCrossProtocolBridgeRequired(FamilyResponses, "unknown") {
		t.Errorf("未知上游不应桥接")
	}
}

func TestQueryPrimitives(t *testing.T) {
	query := url.Values{"n": []string{"42"}}
	if got := QueryIntegerParam(query, "n"); got == nil || *got != 42 {
		t.Errorf("QueryIntegerParam = %v", got)
	}
	if QueryIntegerParam(query, "absent") != nil {
		t.Errorf("缺失参数应返回 nil")
	}
	if QueryStringParam(url.Values{"x": []string{"a", "b"}}, "x") != nil {
		t.Errorf("重复参数应返回 nil")
	}
	if _, ok := ParseJSNumber("   "); ok {
		t.Errorf("空输入应视为 NaN")
	}
	if value, ok := ParseJSNumber(" 2.5e1 "); !ok || value != 25 {
		t.Errorf("ParseJSNumber = %v %v", value, ok)
	}
	if got := QueryIntegerValue(float64(3.9)); got == nil || *got != 3 {
		t.Errorf("QueryIntegerValue(3.9) = %v", got)
	}
	if got := QueryIntegerValue(""); got != nil {
		t.Errorf("QueryIntegerValue(\"\") = %v", got)
	}
	if got := QueryNumberValue("1.5"); got == nil || *got != 1.5 {
		t.Errorf("QueryNumberValue = %v", got)
	}
	if got := IntFromFloat(1e300); got != nil {
		t.Errorf("超安全范围应返回 nil, got %v", got)
	}
	if got := StringValue("  x  "); got == nil || *got != "x" {
		t.Errorf("StringValue = %v", got)
	}
	if ObjectValue("nope") != nil || ObjectValue(map[string]any{"a": 1}) == nil {
		t.Errorf("ObjectValue 类型过滤失败")
	}
}

func TestReadJSONObjectBody(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"a":1}`))
	record, err := ReadJSONObjectBody(req)
	if err != nil || record["a"].(float64) != 1 {
		t.Errorf("合法对象读取 = %v %v", record, err)
	}
	req = httptest.NewRequest("POST", "/", strings.NewReader(`[1]`))
	if _, err := ReadJSONObjectBody(req); !isRequestError(err, "JSON 请求体必须是对象", "invalid_json_body") {
		t.Errorf("非对象 JSON 应返回固定错误, got %v", err)
	}
	req = httptest.NewRequest("POST", "/", strings.NewReader(`{`))
	if _, err := ReadJSONObjectBody(req); !isRequestError(err, "JSON 请求体无效", "invalid_json_body") {
		t.Errorf("非法 JSON 应返回固定错误, got %v", err)
	}
	big := strings.NewReader(`{"pad":"` + strings.Repeat("x", JSONBodyLimit) + `"}`)
	req = httptest.NewRequest("POST", "/", big)
	requestErr, ok := readBodyError(t, req).(*RequestError)
	if !ok || requestErr.StatusCode != 413 || requestErr.Code != "request_body_too_large" {
		t.Errorf("超限应返回 413 RequestError, got %v", readBodyError(t, req))
	}
}

func isRequestError(err error, message, code string) bool {
	requestErr, ok := err.(*RequestError)
	return ok && requestErr.Message == message && requestErr.Code == code
}

func readBodyError(t *testing.T, req *http.Request) error {
	t.Helper()
	_, err := ReadJSONObjectBody(req)
	return err
}

func TestParseRFC3339Instant(t *testing.T) {
	parsed, ok := ParseRFC3339Instant("2026-01-02T03:04:05+08:00")
	if !ok || parsed.UnixMilli() != time.Date(2026, 1, 1, 19, 4, 5, 0, time.UTC).UnixMilli() {
		t.Errorf("offset 解析 = %v %v", parsed, ok)
	}
	if _, ok := ParseRFC3339Instant("2026-02-30T00:00:00Z"); ok {
		t.Errorf("2 月 30 日应拒绝")
	}
	if _, ok := ParseRFC3339Instant("2026-01-02T03:04:05"); ok {
		t.Errorf("缺 offset 应拒绝")
	}
	millis, ok := RFC3339InstantMilliseconds("2026-01-02T03:04:05Z")
	if want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).UnixMilli(); !ok || millis != want {
		t.Errorf("RFC3339InstantMilliseconds = %d %v, want %d", millis, ok, want)
	}
	if _, err := OpenAITimestamp("bad"); err == nil {
		t.Errorf("非法时间应报错")
	}
}

func TestTimePrimitives(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 678_000_000, time.UTC)
	if got := IsoMillis(now); got != "2026-01-02T03:04:05.678Z" {
		t.Errorf("IsoMillis = %q", got)
	}
	if got := ExpiresAtFromDays(nil, now); got != nil {
		t.Errorf("nil days 应无过期 = %v", got)
	}
	thirty := 30
	if got := ExpiresAtFromDays(&thirty, now); got == nil || !strings.HasSuffix(*got, "Z") {
		t.Errorf("ExpiresAtFromDays = %v", got)
	}
	if got := NewOpenAICompatibleFileID(now); !strings.HasPrefix(got, "file-") || !strings.Contains(got, "-") {
		t.Errorf("NewOpenAICompatibleFileID = %q", got)
	} else if hexLen := len(got) - strings.LastIndex(got, "-") - 1; hexLen != 20 {
		t.Errorf("NewOpenAICompatibleFileID hex 段 = %d (%q)", hexLen, got)
	}
	if got := NewOpenAICompatibleVectorStoreID(now); !strings.HasPrefix(got, "vs_") {
		t.Errorf("NewOpenAICompatibleVectorStoreID = %q", got)
	}
	if got := NewVectorStoreChunkID(); len(got) != 8+32 || !strings.HasPrefix(got, "vschunk_") {
		t.Errorf("NewVectorStoreChunkID = %q", got)
	}
	if got := RandomHex(8); len(got) != 8 {
		t.Errorf("RandomHex = %q", got)
	}
}
