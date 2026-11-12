package accounts

// DD2-D4 钉住测试：写面 replace 覆盖的 rule_index 以同次提交的账户
// error_handling_rules 数量为上界——越界报 ValidationError「第 N 条错误处理
// 策略覆盖规则索引超出账户规则范围」（此前越界 replace 使系统额度规则被无声
// 抑制且无替换体，退化为 delete）。前端 materializeInheritedRule 形态（系统
// 规则克隆追加为末条后取其下标）与 delete 覆盖不受影响；读面保持宽容（不触
// DB 的纯函数断言）。

import (
	"errors"
	"strings"
	"testing"
)

func dd2D4Rule(name string) map[string]any {
	return map[string]any{
		"enabled": true, "name": name, "priority": float64(1),
		"action": "retry_next", "status_codes": []any{float64(500)},
	}
}

func dd2D4ReplaceOverride(ruleIndex float64) map[string]any {
	return map[string]any{
		"system_rule_id": systemInsufficientQuotaErrorPolicyRuleID,
		"action":         "replace", "rule_index": ruleIndex,
	}
}

func TestDD2D4WriteFaceRejectsOutOfRangeReplaceIndex(t *testing.T) {
	input := map[string]any{
		"error_handling_rules":          []any{dd2D4Rule("r0"), dd2D4Rule("r1")},
		"error_handling_rule_overrides": []any{dd2D4ReplaceOverride(float64(5))},
	}
	credentials := Credentials{}
	err := normalizeAccountCredentialPolicies(input, credentials)
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) ||
		!strings.Contains(validationErr.Message, "第 1 条错误处理策略覆盖规则索引超出账户规则范围") {
		t.Fatalf("越界 replace 必须 400 ValidationError，got %v", err)
	}
	// 越界载荷不得落任何覆盖。
	if _, present := credentials["error_handling_rule_overrides"]; present {
		t.Fatalf("越界载荷不得写入覆盖：%v", credentials["error_handling_rule_overrides"])
	}
}

func TestDD2D4WriteFaceRejectsReplaceWithoutRules(t *testing.T) {
	// rules 缺席（accountRuleCount=0）时 rule_index=0 也越界：replace 没有可
	// 替换的账户规则即退化为 delete，写面拒绝。
	input := map[string]any{
		"error_handling_rule_overrides": []any{dd2D4ReplaceOverride(float64(0))},
	}
	credentials := Credentials{}
	err := normalizeAccountCredentialPolicies(input, credentials)
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) ||
		!strings.Contains(validationErr.Message, "第 1 条错误处理策略覆盖规则索引超出账户规则范围") {
		t.Fatalf("无规则的 replace 必须 400 ValidationError，got %v", err)
	}
}

func TestDD2D4WriteFaceAcceptsMaterializeShapeAndDelete(t *testing.T) {
	// 前端 materializeInheritedRule 形态：账户已有 3 条规则，系统规则克隆追加
	// 为末条（下标 2）后 replace 指向下标 2 —— 合法载荷照常通过并归一化。
	rules := []any{dd2D4Rule("r0"), dd2D4Rule("r1"), dd2D4Rule("系统规则克隆")}
	input := map[string]any{
		"error_handling_rules":          rules,
		"error_handling_rule_overrides": []any{dd2D4ReplaceOverride(float64(2))},
	}
	credentials := Credentials{}
	if err := normalizeAccountCredentialPolicies(input, credentials); err != nil {
		t.Fatalf("末条下标 replace 必须通过：%v", err)
	}
	overrides, ok := credentials["error_handling_rule_overrides"].([]any)
	if !ok || len(overrides) != 1 {
		t.Fatalf("归一化覆盖形状：%v", credentials["error_handling_rule_overrides"])
	}

	// delete 覆盖无 rule_index，不受上界约束（含 rules 为空的 clearRules 形态）。
	input = map[string]any{
		"error_handling_rules": []any{},
		"error_handling_rule_overrides": []any{
			map[string]any{"system_rule_id": systemInsufficientQuotaErrorPolicyRuleID, "action": "delete"},
		},
	}
	credentials = Credentials{}
	if err := normalizeAccountCredentialPolicies(input, credentials); err != nil {
		t.Fatalf("空规则下的 delete 覆盖必须通过：%v", err)
	}
}
