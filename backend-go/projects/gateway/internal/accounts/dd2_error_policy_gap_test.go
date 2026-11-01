package accounts

// DD2 深挖复审钉住测试（错误策略族）：
// - DD2-D3：overrides 读面逐条应用——写面接受任意长度（每条强制指向唯一系统
//   规则），读面按写面排序语义后条覆盖前条，不再只取第一条。
//
// DD2-D8（克隆投影补 error_handling_rule_overrides）经取证停手：上游额度不足
// 继承错误策略设计 §实现语义/§前端混排 明文裁决「克隆账户不复制来源账户的覆
// 盖，仍从全局默认开始」，前端克隆装载链亦硬编码空覆盖——缺键是文档契约而非
// 移植缺口，改变克隆语义须先经用户裁决并同步文档。
//
// 纯函数断言，不触 DB。

import (
	"testing"
)

func dd2OverrideEntry(action string, ruleIndex float64) map[string]any {
	entry := map[string]any{"system_rule_id": systemInsufficientQuotaRuleID, "action": action}
	if action == "replace" {
		entry["rule_index"] = ruleIndex
	}
	return entry
}

func TestDD2D3EffectiveRulesProcessAllOverrideEntries(t *testing.T) {
	rules := []any{
		map[string]any{"enabled": true, "name": "r-low", "priority": float64(1), "action": "retry_next", "status_codes": []any{float64(500)}},
		map[string]any{"enabled": true, "name": "r-high", "priority": float64(2), "action": "retry_next", "status_codes": []any{float64(500)}},
	}
	// 后条覆盖前条：delete → replace(1)，末条 replace 生效——系统注册条目不出
	// 现，原下标 1 的账户规则携带系统规则 ID。
	effective, err := effectiveAccountErrorHandlingRules(rules, []any{
		dd2OverrideEntry("delete", 0), dd2OverrideEntry("replace", 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range effective {
		if rule.ID == systemInsufficientQuotaRuleID && rule.Source == "system" {
			t.Fatalf("末条 replace 生效后不得出现系统注册条目：%+v", effective)
		}
	}
	carried := false
	for _, rule := range effective {
		if rule.ID == systemInsufficientQuotaRuleID && rule.Source == "account" {
			if body, ok := rule.Rule.(map[string]any); !ok || body["name"] != "r-high" {
				t.Fatalf("携带系统 ID 的应是原下标 1 的 r-high：%+v", effective)
			}
			carried = true
		}
	}
	if !carried {
		t.Fatalf("原下标 1 的账户规则应携带系统规则 ID：%+v", effective)
	}

	// 前条 replace、后条 delete：末条 delete 生效——系统条目被抑制且无账户规
	// 则改挂系统 ID（此前「只取第一条」会把这条展示成 replace）。
	effective, err = effectiveAccountErrorHandlingRules(rules, []any{
		dd2OverrideEntry("replace", 1), dd2OverrideEntry("delete", 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range effective {
		if rule.ID == systemInsufficientQuotaRuleID {
			t.Fatalf("末条 delete 生效后任何条目都不得携带系统规则 ID：%+v", effective)
		}
	}
	if len(effective) != 2 {
		t.Fatalf("账户规则应全部保留：%+v", effective)
	}
}
