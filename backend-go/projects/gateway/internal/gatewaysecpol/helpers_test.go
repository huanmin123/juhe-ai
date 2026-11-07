package gatewaysecpol

import (
	"encoding/json"
	"testing"
)

// 测试公共 helper：白盒同包测试，直接构造策略与解析产物。

func mustCompile(t *testing.T, rulesJSON string) *CompiledPolicy {
	t.Helper()
	policy, err := Compile(rulesJSON)
	if err != nil {
		t.Fatalf("Compile(%s) 失败: %v", rulesJSON, err)
	}
	return policy
}

// singleRule 构造只含一条规则的策略。
func singleRule(t *testing.T, ruleJSON string) *CompiledPolicy {
	t.Helper()
	return mustCompile(t, `{"rules":[`+ruleJSON+`]}`)
}

func phonePlaceholderRule() string {
	return `{"id":"r1","type":"phone","action":"placeholder"}`
}

func phoneMaskRule() string {
	return `{"id":"r1","type":"phone","action":"mask"}`
}

func mustJSONString(text string) string {
	encoded, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// mediaScan 用 media 协议族的顶层 text 字段跑单段文本（最短端到端路径），
// 返回替换后的文本与完整结果。
func mediaScan(t *testing.T, policy *CompiledPolicy, text string) (string, SanitizeResult) {
	t.Helper()
	body := []byte(`{"text":` + mustJSONString(text) + `}`)
	result := SanitizeBody(policy, body, FamilyMedia)
	parsed, err := parseOrderedJSON(result.Body)
	if err != nil {
		t.Fatalf("解析 sanitized body 失败: %v（body=%s）", err, result.Body)
	}
	object, ok := parsed.(*orderedObject)
	if !ok {
		t.Fatalf("sanitized body 顶层不是对象: %s", result.Body)
	}
	out, _ := object.get("text").(string)
	return out, result
}

// parseBody 解析 sanitized body 为有序对象（断言注入形态用）。
func parseBody(t *testing.T, raw []byte) *orderedObject {
	t.Helper()
	value, err := parseOrderedJSON(raw)
	if err != nil {
		t.Fatalf("解析 body 失败: %v（body=%s）", err, raw)
	}
	object, ok := value.(*orderedObject)
	if !ok {
		t.Fatalf("body 顶层不是 JSON 对象: %s", raw)
	}
	return object
}
