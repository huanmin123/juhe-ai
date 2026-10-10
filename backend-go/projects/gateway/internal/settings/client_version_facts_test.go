package settings

// 客户端版本自动跟版设计 §7（v1.1 管理页只读展示）：gateway-core 分区 GET
// 响应伴随只读字段 clientVersionFacts（自动层当前值 + 五族内置基线），其余
// 分区不带。该字段不是设置键：不进分区 values、不在白名单、不参与 PATCH。

import (
	"net/http"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

func TestSettingsGatewayCoreSectionCarriesClientVersionFacts(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	// 预置自动层：一个合法族值 + 一个非法族值（宽容过滤语义，与既有读取路径
	// 一致）+ 其余缺族。
	env.exec(t, `INSERT OR REPLACE INTO system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', ?, ?, ?)`,
		upstreamClientVersionAutoOverridesKey, `{"codex":"0.160.0","claudeCode":"not-a-version"}`, time.Now().UTC().Format(time.RFC3339Nano))

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/settings/sections/gateway-core", "")
	if code != http.StatusOK {
		t.Fatalf("get gateway-core: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	facts, ok := data["clientVersionFacts"].(map[string]any)
	if !ok {
		t.Fatalf("clientVersionFacts shape: %v", data)
	}
	auto, ok := facts["autoOverrides"].(map[string]any)
	if !ok {
		t.Fatalf("autoOverrides shape: %v", facts)
	}
	if len(auto) != 1 || auto["codex"] != "0.160.0" {
		t.Fatalf("autoOverrides 过滤语义: %v（非法族值应被忽略，合法值保留）", auto)
	}
	builtIns, ok := facts["builtIns"].(map[string]any)
	if !ok {
		t.Fatalf("builtIns shape: %v", facts)
	}
	if len(builtIns) != len(upstreamClientVersionFamilies) {
		t.Fatalf("builtIns 族集: %d != %d (%v)", len(builtIns), len(upstreamClientVersionFamilies), builtIns)
	}
	for family := range upstreamClientVersionFamilies {
		want := upstreamidentity.BuiltInClientVersion(family)
		if want == "" {
			t.Fatalf("BuiltInClientVersion(%q) 为空，家族键失配", family)
		}
		if builtIns[family] != want {
			t.Fatalf("builtIns[%s]: %v != %v", family, builtIns[family], want)
		}
	}
	if _, polluted := data["values"].(map[string]any)["clientVersionFacts"]; polluted {
		t.Fatalf("clientVersionFacts 不得进入分区 values: %v", data["values"])
	}
}

func TestSettingsNonGatewayCoreSectionsOmitClientVersionFacts(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	for _, sectionKey := range ManagementSettingsSectionKeys {
		if sectionKey == "gateway-core" {
			continue
		}
		code, payload := env.do(t, http.MethodGet, "/__aisys__/api/settings/sections/"+sectionKey, "")
		if code != http.StatusOK {
			t.Fatalf("get section %s: %d %v", sectionKey, code, payload)
		}
		if _, ok := dataMap(t, payload)["clientVersionFacts"]; ok {
			t.Fatalf("section %s 不应携带 clientVersionFacts", sectionKey)
		}
	}
}
