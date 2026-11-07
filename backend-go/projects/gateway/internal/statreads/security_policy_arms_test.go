package statreads

// securityPolicyApplied / piiHits 列表映射断言（API Key 安全策略与出站内容
// 脱敏设计 §8，批D）：镜像 TestUsageRecordListCompactionExpectedArms 的升级
// 期兼容臂——security_policy_applied 走 boolLike（PG bool / 旧行缺列 / NULL
// 均按 false）；pii_hits_json NULL / 空串 / 坏 JSON / 空 object 均 piiHits
// 按 nil（omitempty 不透出），不报错。

import (
	"net/http"
	"testing"
)

func TestUsageRecordListSecurityPolicyArms(t *testing.T) {
	deps := &Deps{}
	row := func(applied any, appliedPresent bool, hits any, hitsPresent bool) Row {
		base := Row{
			"id": "ur-security", "trace_id": "trace-security",
			"traffic_source": "gateway", "success": true,
		}
		if appliedPresent {
			base["security_policy_applied"] = applied
		}
		if hitsPresent {
			base["pii_hits_json"] = hits
		}
		return base
	}
	assert := func(name string, item usageRecordListItem, wantApplied bool, wantHits map[string]int) {
		t.Helper()
		if item.SecurityPolicyApplied != wantApplied {
			t.Fatalf("%s securityPolicyApplied = %v, want %v", name, item.SecurityPolicyApplied, wantApplied)
		}
		if len(item.PIIHits) != len(wantHits) {
			t.Fatalf("%s piiHits = %#v, want %#v", name, item.PIIHits, wantHits)
		}
		for label, count := range wantHits {
			if item.PIIHits[label] != count {
				t.Fatalf("%s piiHits[%s] = %d, want %d", name, label, item.PIIHits[label], count)
			}
		}
	}
	// PG 臂：boolean 列解码为 Go bool；pii_hits_json 解码为 text。
	assert("PG bool true + hits", deps.mapUsageRecordListItem(row(true, true, `{"PHONE":2,"SECRET":1}`, true), AccessScope{}, nil, nil, nil, nil), true, map[string]int{"PHONE": 2, "SECRET": 1})
	assert("PG bool false + NULL hits", deps.mapUsageRecordListItem(row(false, true, nil, true), AccessScope{}, nil, nil, nil, nil), false, nil)
	// 旧行缺列与 NULL：boolLike 缺键/nil 都按 false；piiHits 缺列/NULL 都按 nil。
	assert("missing columns", deps.mapUsageRecordListItem(row(nil, false, nil, false), AccessScope{}, nil, nil, nil, nil), false, nil)
	assert("NULL applied", deps.mapUsageRecordListItem(row(nil, true, nil, false), AccessScope{}, nil, nil, nil, nil), false, nil)
	// 坏 JSON / 空 object / 空串 / JSON null：piiHits 静默 nil，不影响 applied。
	assert("bad JSON", deps.mapUsageRecordListItem(row(true, true, "not-json", true), AccessScope{}, nil, nil, nil, nil), true, nil)
	assert("empty object", deps.mapUsageRecordListItem(row(true, true, "{}", true), AccessScope{}, nil, nil, nil, nil), true, nil)
	assert("blank text", deps.mapUsageRecordListItem(row(true, true, "  ", true), AccessScope{}, nil, nil, nil, nil), true, nil)
	assert("JSON null", deps.mapUsageRecordListItem(row(true, true, "null", true), AccessScope{}, nil, nil, nil, nil), true, nil)
}

// TestUsageRecordListSecurityPolicyPGArm 是批D 端到端读臂（API Key 安全策略
// 与出站内容脱敏设计 §8）：pgtestkit 真实 PG 上种子全值 / 无值两行，
// usageRecordsListHandler 全链路断言列表 JSON——全值记录透出
// securityPolicyApplied=true 与 piiHits={"PHONE":2}，无值记录 applied=false
// 且 piiHits 因 omitempty 不出现。
func TestUsageRecordListSecurityPolicyPGArm(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.exec(t,
		// 读面夹具母表不建分区，读臂种子按需补当日分区（边界同 jobs 分区链）。
		`CREATE TABLE juhe_usage.usage_records_20260904 PARTITION OF juhe_usage.usage_records FOR VALUES FROM ('2026-09-04') TO ('2026-09-05')`,
		`INSERT INTO juhe_usage.usage_records (id, system_account_id, trace_id, traffic_source, security_policy_applied, pii_hits_json, success, created_at) VALUES
			('ur-sp-hit', 'sys-admin-1', 'trace-sp-hit', 'gateway', 1, '{"PHONE":2}', 1, '2026-09-04T01:02:03.000Z'),
			('ur-sp-plain', 'sys-admin-1', 'trace-sp-plain', 'gateway', 0, NULL, 1, '2026-09-04T01:02:04.000Z')`)
	recorder := invoke(t, fixture.deps.usageRecordsListHandler(false), http.MethodGet, "/usage-records", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("pg security policy list = %d body=%s", recorder.Code, recorder.Body.String())
	}
	items, ok := dataMap(t, decodeBody(t, recorder))["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("usage-records items wrong: %#v", items)
	}
	byTrace := map[string]map[string]any{}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("item shape wrong: %#v", raw)
		}
		byTrace[item["traceId"].(string)] = item
	}
	hit := byTrace["trace-sp-hit"]
	if hit == nil || hit["securityPolicyApplied"] != true {
		t.Fatalf("hit securityPolicyApplied wrong: %#v", hit)
	}
	hits, ok := hit["piiHits"].(map[string]any)
	if !ok || hits["PHONE"] != float64(2) {
		t.Fatalf("hit piiHits wrong: %#v", hit["piiHits"])
	}
	plain := byTrace["trace-sp-plain"]
	if plain == nil {
		t.Fatalf("plain item missing: %#v", byTrace)
	}
	// omitempty 契约（对齐前端 securityPolicyApplied?: boolean）：false /
	// nil 两字段都不出现在 JSON。
	if _, present := plain["securityPolicyApplied"]; present {
		t.Fatalf("plain securityPolicyApplied must be omitted: %#v", plain["securityPolicyApplied"])
	}
	if _, present := plain["piiHits"]; present {
		t.Fatalf("plain piiHits must be omitted: %#v", plain["piiHits"])
	}
}
