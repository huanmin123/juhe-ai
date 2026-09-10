package gatewayopenai

import (
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// OpenAI 错误负载归一化：error 子对象优先、message 别名链、嵌套 error 细化。
func TestWAParseErrorPayloadOpenAI(t *testing.T) {
	headerJSON := map[string][]string{"Content-Type": {"application/json"}}
	t.Run("标准 error 子对象", func(t *testing.T) {
		payload := ParseErrorPayload(`{"error":{"code":"insufficient_quota","type":"insufficient_quota_error","message":"额度不足"}}`, headerJSON)
		if payload.Code != "insufficient_quota" || payload.Type != "insufficient_quota_error" || payload.Message != "额度不足" {
			t.Fatalf("错误负载 = %+v", payload)
		}
	})
	t.Run("无 error 子对象", func(t *testing.T) {
		payload := ParseErrorPayload(`{"code":401,"type":"auth_error","message":"未授权"}`, headerJSON)
		if payload.Code != "401" || payload.Type != "auth_error" || payload.Message != "未授权" {
			t.Fatalf("根对象负载 = %+v", payload)
		}
	})
	t.Run("嵌套 error 细化", func(t *testing.T) {
		payload := ParseErrorPayload(`{"error":{"code":"outer","message":"外层","data":{"message":"内层明细"}}}`, headerJSON)
		if payload.Code != "outer" || payload.Message != "外层" {
			t.Fatalf("嵌套负载 = %+v", payload)
		}
		// 外层 message 缺失时读取嵌套对象。
		payload = ParseErrorPayload(`{"error":{"code":"outer","err":{"detail":"嵌套明细"}}}`, headerJSON)
		if payload.Message != "嵌套明细" {
			t.Fatalf("嵌套明细 = %+v", payload)
		}
	})
	t.Run("message 别名链", func(t *testing.T) {
		aliases := []string{"msg", "error_message", "error_description", "detail", "reason"}
		for _, alias := range aliases {
			body := `{"error":{"` + alias + `":"别名-` + alias + `"}}`
			payload := ParseErrorPayload(body, headerJSON)
			want := "别名-" + alias
			if payload.Message != want {
				t.Fatalf("别名 %s = %+v，期望 %q", alias, payload, want)
			}
		}
	})
	t.Run("数字与布尔字段", func(t *testing.T) {
		payload := ParseErrorPayload(`{"error":{"code":403,"message":true}}`, headerJSON)
		if payload.Code != "403" || payload.Message != "true" {
			t.Fatalf("标量字段 = %+v", payload)
		}
		payload = ParseErrorPayload(`{"error":{"code":1.5}}`, headerJSON)
		if payload.Code != "1.5" {
			t.Fatalf("小数 code = %+v", payload)
		}
	})
	t.Run("message 对象取 message 字段", func(t *testing.T) {
		payload := ParseErrorPayload(`{"error":{"message":{"msg":"对象消息"}}}`, headerJSON)
		if payload.Message != "对象消息" {
			t.Fatalf("对象消息 = %+v", payload)
		}
	})
	t.Run("非 JSON 与空 header", func(t *testing.T) {
		if payload := ParseErrorPayload("plain text", nil); payload.HasEvidence() {
			t.Fatalf("纯文本应为空: %+v", payload)
		}
		if payload := ParseErrorPayload(`{"error":{}}`, nil); payload.HasEvidence() {
			// 文本以 { 开头：无 Content-Type 也可解析；空 error 对象归一化为零值。
			t.Fatalf("空 error 对象应零值: %+v", payload)
		}
		if payload := ParseErrorPayload("[1]", headerJSON); payload.HasEvidence() {
			t.Fatalf("数组负载应为空: %+v", payload)
		}
		if payload := ParseErrorPayload("{bad", headerJSON); payload.HasEvidence() {
			t.Fatalf("解析失败应为空: %+v", payload)
		}
		// Content-Type 为空但非 JSON 前缀。
		if payload := ParseErrorPayload("upstream error", map[string][]string{"Content-Type": {""}}); payload.HasEvidence() {
			t.Fatalf("空 Content-Type 纯文本应为空: %+v", payload)
		}
	})
}

func TestWAParseErrorPayloadFromJSONValueOpenAI(t *testing.T) {
	if payload := ParseErrorPayloadFromJSONValue(7); payload.HasEvidence() {
		t.Fatalf("标量输入应为空: %+v", payload)
	}
	value := mustParseJSON(t, `{"error":{"code":"c","type":"t","message":"m"}}`)
	payload := ParseErrorPayloadFromJSONValue(value)
	if payload.Code != "c" || payload.Type != "t" || payload.Message != "m" {
		t.Fatalf("JSONValue 负载 = %+v", payload)
	}
	if payload := ParseErrorPayloadFromJSONValue(map[string]any{}); payload.HasEvidence() {
		t.Fatalf("空对象负载应零值: %+v", payload)
	}
}

// usage 补充：ParseUsageFromJSONValue、字节估算与输出值估算。
func TestWAUsageGaps(t *testing.T) {
	t.Run("ParseUsageFromJSONValue", func(t *testing.T) {
		value := mustParseJSON(t, `{"service_tier":"default","usage":{"prompt_tokens":3,"completion_tokens":4}}`)
		usage := ParseUsageFromJSONValue(value)
		waOAssertToken(t, usage.InputTokens, 3, "input")
		waOAssertToken(t, usage.OutputTokens, 4, "output")
		if usage.ServiceTier != "default" {
			t.Fatalf("ServiceTier = %q", usage.ServiceTier)
		}
		if usage := ParseUsageFromJSONValue([]any{}); gatewayproto.HasAnyUsageValue(usage) {
			t.Fatalf("数组输入应为空: %+v", usage)
		}
	})
	t.Run("EstimateTokenCountFromByteLength", func(t *testing.T) {
		if _, ok := EstimateTokenCountFromByteLength(0); ok {
			t.Fatal("0 字节不可估算")
		}
		tokens, ok := EstimateTokenCountFromByteLength(8)
		if !ok || tokens != 2 {
			t.Fatalf("8 字节 = %d/%v，期望 2/true", tokens, ok)
		}
		tokens, ok = EstimateTokenCountFromByteLength(3)
		if !ok || tokens != 1 {
			t.Fatalf("3 字节下限 = %d/%v，期望 1/true", tokens, ok)
		}
	})
	t.Run("EstimateTokensFromOutputValue", func(t *testing.T) {
		value := mustParseJSON(t, `{"output":[{"content":[{"text":"你好 world"}]}],"status":"x"}`)
		tokens := EstimateTokensFromOutputValue(value)
		if tokens <= 0 {
			t.Fatalf("输出估算应为正: %d", tokens)
		}
		// 空白文本与空结构不估算。
		if tokens := EstimateTokensFromOutputValue("   "); tokens != 0 {
			t.Fatalf("空白文本 = %d", tokens)
		}
		// output 估算跳过元数据键。
		value = mustParseJSON(t, `{"output":[{"type":"message","id":"m1","role":"assistant","content":[{"text":"abcd"}]}]}`)
		if tokens := EstimateTokensFromOutputValue(value); tokens != 1 {
			t.Fatalf("跳过元数据后 = %d，期望 1", tokens)
		}
	})
	t.Run("numberValue 边界", func(t *testing.T) {
		if numberValue(-1) != nil {
			t.Fatal("负 int 应为 nil")
		}
		waOAssertToken(t, numberValue(7), 7, "int")
		waOAssertToken(t, numberValue(" 9 "), 9, "带空白字符串数字")
		if numberValue("abc") != nil {
			t.Fatal("非法数字字符串应为 nil")
		}
	})
	t.Run("sumDefined", func(t *testing.T) {
		if sumDefined(nil, nil) != nil {
			t.Fatal("全 nil 应为 nil")
		}
		waOAssertToken(t, sumDefined(waOIntPtr(2), nil, waOIntPtr(3)), 5, "求和")
	})
	t.Run("片段扫描字符串属性", func(t *testing.T) {
		got := extractJSONStringPropertyFromTextFragment(`{"a":"x","service_tier":"flex"}`, "service_tier")
		if got != "flex" {
			t.Fatalf("service_tier = %q", got)
		}
		if got := extractJSONStringPropertyFromTextFragment(`{"a":"x"}`, "missing"); got != "" {
			t.Fatalf("缺失属性 = %q", got)
		}
	})
	t.Run("对象片段扫描", func(t *testing.T) {
		if _, ok := extractJSONObjectPropertyFromTextFragment(`{"usage": "no"}`, "usage"); ok {
			t.Fatal("usage 非对象应失败")
		}
		if _, ok := extractJSONObjectPropertyFromTextFragment(`no tokens here`, "usage"); ok {
			t.Fatal("无 token 应失败")
		}
		if _, ok := extractJSONObjectPropertyFromTextFragment(`{"usage":{"a":1`, "usage"); ok {
			t.Fatal("未配平应失败")
		}
	})
}

func TestWAErrorPayloadTextHelpers(t *testing.T) {
	if got := errorFieldText(map[string]any{"code": "inner-code"}); got != "inner-code" {
		t.Fatalf("对象内 code = %q", got)
	}
	if got := errorFieldText(3.5); got != "3.5" {
		t.Fatalf("数字 = %q", got)
	}
	if got := errorFieldText(nil); got != "" {
		t.Fatalf("nil = %q", got)
	}
	if got := stringErrorField(float64(2)); got != "2" {
		t.Fatalf("整数数字 = %q", got)
	}
	if nestedErrorObject(nil) != nil {
		t.Fatal("nil 嵌套应为 nil")
	}
	nested := nestedErrorObject(map[string]any{"err": map[string]any{"message": "m"}})
	if nested == nil || nested["message"] != "m" {
		t.Fatalf("err 键嵌套 = %+v", nested)
	}
	if nestedErrorField(nil, "message") != nil {
		t.Fatal("nil 嵌套字段应为 nil")
	}
	if firstErrorFieldText(nil, "", "x") != "x" {
		t.Fatal("firstErrorFieldText 跳过空值")
	}
	// trimNumber 的 JSON 序列化路径。
	if got := trimNumber(2.5); got != "2.5" {
		t.Fatalf("trimNumber = %q", got)
	}
	// 布尔字段文本。
	if got := stringErrorField(false); got != "false" {
		t.Fatalf("布尔 = %q", got)
	}
	// http.Header 为 nil 的解析路径。
	if payload := ParseErrorPayload("text", http.Header(nil)); payload.HasEvidence() {
		t.Fatalf("nil header 纯文本应为空: %+v", payload)
	}
}

func waOAssertToken(t *testing.T, value *int, want int, label string) {
	t.Helper()
	if value == nil {
		t.Fatalf("%s：得到 nil，期望 %d", label, want)
	}
	if *value != want {
		t.Fatalf("%s = %d，期望 %d", label, *value, want)
	}
}

func waOIntPtr(value int) *int { return &value }
