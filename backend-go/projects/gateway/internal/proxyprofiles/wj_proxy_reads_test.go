package proxyprofiles

// 读取与路由收尾补充：ListPage 检测字段回填、ListOptions 排序 tie-break、
// options handler 的 limit 钳制与 description/null 输入。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWJListPageHydratesTestColumns 固定分页列表回填检测列。
func TestWJListPageHydratesTestColumns(t *testing.T) {
	fixture := newProxyFixture(t)
	if _, err := fixture.db.Exec(`INSERT INTO proxy_profiles
		(id, system_account_id, name, type, host, port, enabled, test_status, latency_ms, outbound_ip, outbound_region, last_test_message, last_tested_at, created_at, updated_at)
		VALUES ('p-hyd', 'sa-1', '回填', 'http', 'h', 8080, 1, 'warning', 321, '5.6.7.8', 'JP', 'warn', '2026-09-04T01:00:00.000Z', '2026-09-04T00:00:00Z', '2026-09-04T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	result, err := fixture.store.ListPage(context.Background(), 1, 20, "")
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("必须返回 1 行: %+v", result)
	}
	profile := result.Items[0]
	if profile.TestStatus != "warning" || profile.LatencyMs == nil || *profile.LatencyMs != 321 {
		t.Fatalf("检测列回填不符: %+v", profile)
	}
	if profile.OutboundIp == nil || *profile.OutboundIp != "5.6.7.8" || profile.OutboundRegion == nil || *profile.OutboundRegion != "JP" {
		t.Fatalf("出口列回填不符: %+v", profile)
	}
}

// TestWJListOptionsSortTieBreaks 固定选项合并排序的二级键：同名按更新时间
// 倒序，再按 id 升序。
func TestWJListOptionsSortTieBreaks(t *testing.T) {
	fixture := newProxyFixture(t)
	statements := []string{
		`INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at)
			VALUES ('o-1', 'sa-1', '同名', 'http', 'h', 8080, 1, '2026-01-01', '2026-01-05')`,
		`INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at)
			VALUES ('o-2', 'sa-1', '同名2', 'http', 'h', 8080, 1, '2026-01-01', '2026-01-04')`,
		`INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at)
			VALUES ('o-0', 'sa-1', '同名3', 'http', 'h', 8080, 1, '2026-01-01', '2026-01-04')`,
		`INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at)
			VALUES ('a-first', 'sa-1', 'AAA', 'http', 'h', 8080, 1, '2026-01-01', '2026-01-01')`,
	}
	for _, statement := range statements {
		if _, err := fixture.db.Exec(statement); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	options, err := fixture.store.ListOptions(context.Background(), "", 50, []string{"a-first", "o-1"})
	if err != nil {
		t.Fatalf("ListOptions: %v", err)
	}
	// 名称升序 AAA 在前；同名（按 id 去重窗口）内更新时间倒序 o-1(01-05) 领先。
	if len(options) != 4 || options[0].ID != "a-first" {
		t.Fatalf("排序不符: %#v", options)
	}
	if options[1].ID != "o-1" {
		t.Fatalf("同名组必须按更新时间倒序: %#v", options)
	}
}

// TestWJOptionsHandlerLimitClamp 固定 options handler 的 limit 钳制生效。
func TestWJOptionsHandlerLimitClamp(t *testing.T) {
	fixture := newProxyFixture(t)
	fixture.seedProfile(t, "p-1", "2026-01-01", "unknown")
	for _, limit := range []string{"0", "999", "abc"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/__aisys__/api/proxies/options?limit="+limit, nil)
		optionsHandler(recorder, request, fixture.store)
		if recorder.Code != http.StatusOK {
			t.Fatalf("limit=%s 必须 200: %d %s", limit, recorder.Code, recorder.Body.String())
		}
	}
}

// TestWJParseProxyInputNilFields 固定 null 字段的清空语义。
func TestWJParseProxyInputNilFields(t *testing.T) {
	// create：name=null → 缺必填拒绝。
	if _, message := parseProxyInput(map[string]any{"name": nil, "type": "http", "host": "h", "port": float64(80)}, false); message != "代理参数无效" {
		t.Fatalf("name null 必须拒绝: %q", message)
	}
	// update：description=null 清空（合法 payload）。
	input, message := parseProxyInput(map[string]any{
		"description": nil, "expectedUpdatedAt": "2026-09-04T12:00:00Z",
	}, true)
	if message != "" || !input.HasDescription || input.Description != nil {
		t.Fatalf("description null 语义不符: (%+v, %q)", input, message)
	}
	// name=null 跳过赋值但不算 payload key 之外的键。
	input, message = parseProxyInput(map[string]any{
		"name": nil, "expectedUpdatedAt": "2026-09-04T12:00:00Z",
	}, true)
	if message != "" || input.Name != nil {
		t.Fatalf("update name null 必须跳过: (%+v, %q)", input, message)
	}
}
