// 调度偏好独立实体 P1——公开面 API Key add/update 绑定解析矩阵（与内面
// 创建/PATCH 同规则的五写入口矩阵两行）。运行级链路由 apikeys 包与 aipublic
// 生命周期测试覆盖。
package aipublic

import (
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
)

func TestSPC1PublicApiKeyParsersMirrorBindingContract(t *testing.T) {
	// 公开 add：缺省 → nil（store 层落内置）；null → nil；字符串 → 值；非
	// 法类型/空字符串 → 统一文案。
	parsed, issue := parseApiKeyAddBody(map[string]any{
		"targetUsername": "user-a", "name": "k", "routeStrategyId": "rs-1",
	})
	if issue != "" || parsed.SchedulingPreferenceID != nil {
		t.Fatalf("add absent: %v %v", parsed, issue)
	}
	parsed, issue = parseApiKeyAddBody(map[string]any{
		"targetUsername": "user-a", "name": "k", "routeStrategyId": "rs-1", "schedulingPreferenceId": nil,
	})
	if issue != "" || parsed.SchedulingPreferenceID != nil {
		t.Fatalf("add null: %v %v", parsed, issue)
	}
	parsed, issue = parseApiKeyAddBody(map[string]any{
		"targetUsername": "user-a", "name": "k", "routeStrategyId": "rs-1", "schedulingPreferenceId": " sp-x ",
	})
	if issue != "" || parsed.SchedulingPreferenceID == nil || *parsed.SchedulingPreferenceID != "sp-x" {
		t.Fatalf("add value: %v %v", parsed, issue)
	}
	for name, value := range map[string]any{"number": 3.0, "empty": "   "} {
		_, issue = parseApiKeyAddBody(map[string]any{
			"targetUsername": "user-a", "name": "k", "routeStrategyId": "rs-1", "schedulingPreferenceId": value,
		})
		if issue != apikeys.SchedulingPreferenceUnavailableMessage {
			t.Fatalf("add %s: issue %q", name, issue)
		}
	}

	// 公开 update：缺失 = Has false（其余可变字段满足至少一个变更约束）；
	// null = Has true + nil（回默认）；值与非法同 add；仅提交本字段即满足约
	// 束。
	updated, issue := parseApiKeyUpdateBody(map[string]any{"apiKeyId": "key-1", "name": "renamed"})
	if issue != "" || updated.HasSchedulingPreference {
		t.Fatalf("update absent: %v %v", updated, issue)
	}
	updated, issue = parseApiKeyUpdateBody(map[string]any{"apiKeyId": "key-1", "schedulingPreferenceId": nil})
	if issue != "" || !updated.HasSchedulingPreference || updated.SchedulingPreferenceID != nil {
		t.Fatalf("update null: %v %v", updated, issue)
	}
	updated, issue = parseApiKeyUpdateBody(map[string]any{"apiKeyId": "key-1", "schedulingPreferenceId": "sp-y"})
	if issue != "" || !updated.HasSchedulingPreference || updated.SchedulingPreferenceID == nil || *updated.SchedulingPreferenceID != "sp-y" {
		t.Fatalf("update value: %v %v", updated, issue)
	}
	_, issue = parseApiKeyUpdateBody(map[string]any{"apiKeyId": "key-1", "schedulingPreferenceId": true})
	if issue != apikeys.SchedulingPreferenceUnavailableMessage {
		t.Fatalf("update nonstring: issue %q", issue)
	}

	// 未知键仍被 strict 拒绝（两入口键集已扩展但仍封闭）。
	if _, issue := parseApiKeyAddBody(map[string]any{"targetUsername": "u", "name": "k", "routeStrategyId": "r", "schedulingPreferenceIDX": "x"}); !strings.Contains(issue, "Unrecognized") {
		t.Fatalf("add strict: %q", issue)
	}
	if _, issue := parseApiKeyUpdateBody(map[string]any{"apiKeyId": "key-1", "schedulingPreferenceIDX": "x"}); !strings.Contains(issue, "Unrecognized") {
		t.Fatalf("update strict: %q", issue)
	}
}
