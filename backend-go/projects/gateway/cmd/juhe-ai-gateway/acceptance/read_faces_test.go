// X04 404 项补齐：七个轻量读/管理面的进程级 200 探测。共享同一 fresh
// gateway 实例（subtests 顺序执行）：stats 与 my-stats 概览/窗口、
// usage-records 与 my-usage-records、authorization-options 与 my-*、
// proxies options/创建/编辑/删除、table-monitor 三面、ui-bootstrap 与 my-*。
// help 静态面依赖前端 dist 目录，缺省构建不挂载，走 404 JSON 契约（本文件
// 同时验证该缺省行为）。
package acceptance

import (
	"net/http"
	"testing"
)

func TestAcceptanceReadFaces(t *testing.T) {
	fixture := startGateway(t, gatewayEnvOptions{})
	client := &acceptanceClient{t: t, http: fixture.admin, baseURL: fixture.baseURL}

	t.Run("stats usage-overview and usage-window", func(t *testing.T) {
		client.do(http.MethodGet, "/__aisys__/api/stats/usage-window", nil, wantStatus(http.StatusOK))
		client.do(http.MethodGet, "/__aisys__/api/stats/usage-overview/summary", nil, wantStatus(http.StatusOK))
		client.do(http.MethodGet, "/__aisys__/api/stats/usage-overview/daily-trend", nil, wantStatus(http.StatusOK))
		client.do(http.MethodGet, "/__aisys__/api/stats/usage-overview/hourly-trend", nil, wantStatus(http.StatusOK))
		client.do(http.MethodGet, "/__aisys__/api/stats/usage-overview/model-distribution", nil, wantStatus(http.StatusOK))
		client.do(http.MethodGet, "/__aisys__/api/stats/usage-overview/errors", nil, wantStatus(http.StatusOK))
		// 非法日期范围 → 400 中文契约。
		client.do(http.MethodGet, "/__aisys__/api/stats/usage-overview/summary?startDate=bad", nil, wantStatus(http.StatusBadRequest))
		// 未登录（错误 token）→ 401 请先登录。
		bad := &acceptanceClient{t: t, http: &http.Client{}, baseURL: fixture.baseURL}
		bad.do(http.MethodGet, "/__aisys__/api/stats/usage-window", nil, wantStatus(http.StatusUnauthorized))
	})

	t.Run("my-stats self surface", func(t *testing.T) {
		client.do(http.MethodGet, "/__aisys__/api/my-stats/usage-window", nil, wantStatus(http.StatusOK))
		client.do(http.MethodGet, "/__aisys__/api/my-stats/usage-overview/summary", nil, wantStatus(http.StatusOK))
		// my-stats 上的 router 内 requireAdmin 面（forceSelfAccessScope 角色降
		// 级后）保持 403。
		client.do(http.MethodGet, "/__aisys__/api/my-stats/system-metrics/trend", nil, wantStatus(http.StatusForbidden))
		// admin 面正常。
		client.do(http.MethodGet, "/__aisys__/api/stats/system-metrics/trend", nil, wantStatus(http.StatusOK))
	})

	t.Run("ai-performance and ai-health", func(t *testing.T) {
		client.do(http.MethodGet, "/__aisys__/api/stats/ai-performance", nil, wantStatus(http.StatusOK))
		client.do(http.MethodGet, "/__aisys__/api/stats/ai-performance/accounts", nil, wantStatus(http.StatusOK))
		client.do(http.MethodGet, "/__aisys__/api/stats/ai-health", nil, wantStatus(http.StatusOK))
	})

	t.Run("account-usage family", func(t *testing.T) {
		client.do(http.MethodGet, "/__aisys__/api/stats/account-usage", nil, wantStatus(http.StatusOK))
		client.do(http.MethodGet, "/__aisys__/api/stats/account-usage/options", nil, wantStatus(http.StatusOK))
		client.do(http.MethodGet, "/__aisys__/api/stats/account-usage/summary", nil, wantStatus(http.StatusOK))
		client.do(http.MethodGet, "/__aisys__/api/stats/account-usage/trend", nil, wantStatus(http.StatusOK))
		// includeSummary 明确拒绝。
		client.do(http.MethodGet, "/__aisys__/api/stats/account-usage?includeSummary=1", nil, wantStatus(http.StatusBadRequest))
	})

	t.Run("usage-records both surfaces", func(t *testing.T) {
		client.do(http.MethodGet, "/__aisys__/api/usage-records", nil, wantStatus(http.StatusOK))
		client.do(http.MethodGet, "/__aisys__/api/my-usage-records", nil, wantStatus(http.StatusOK))
		// 未选系统账户的管理端过滤 → 400。
		client.do(http.MethodGet, "/__aisys__/api/usage-records?model=gpt-5", nil, wantStatus(http.StatusBadRequest))
	})

	t.Run("authorization-options both surfaces", func(t *testing.T) {
		client.do(http.MethodGet, "/__aisys__/api/authorization-options/grantee-accounts", nil, wantStatus(http.StatusOK))
		client.do(http.MethodGet, "/__aisys__/api/authorization-options/grantee-teams", nil, wantStatus(http.StatusOK))
		client.do(http.MethodGet, "/__aisys__/api/authorization-options/grantee-accounts?keyword=ali&limit=5", nil, wantStatus(http.StatusOK))
		client.do(http.MethodGet, "/__aisys__/api/my-authorization-options/grantee-accounts", nil, wantStatus(http.StatusOK))
		client.do(http.MethodGet, "/__aisys__/api/my-authorization-options/grantee-teams", nil, wantStatus(http.StatusOK))
		// grantee-groups 需要被授权用户。
		client.do(http.MethodGet, "/__aisys__/api/authorization-options/grantee-groups", nil, wantStatus(http.StatusBadRequest))
	})

	t.Run("proxies management", func(t *testing.T) {
		client.do(http.MethodGet, "/__aisys__/api/proxies/options", nil, wantStatus(http.StatusOK))
		client.do(http.MethodGet, "/__aisys__/api/proxies", nil, wantStatus(http.StatusOK))
		_, created := client.do(http.MethodPost, "/__aisys__/api/proxies", map[string]any{
			"name": "验收代理", "type": "http", "host": "127.0.0.1", "port": 8080, "enabled": true,
		}, wantStatus(http.StatusCreated))
		proxyID := dataString(created, "id")
		if proxyID == "" {
			t.Fatalf("proxy create payload wrong: %#v", created)
		}
		// 正确版本编辑 → changed。
		_, patched := client.do(http.MethodPatch, "/__aisys__/api/proxies/"+proxyID, map[string]any{
			"expectedUpdatedAt": dataString(created, "updatedAt"), "host": "127.0.0.2",
		}, wantStatus(http.StatusOK))
		if data(patched)["changed"] != true {
			t.Fatalf("proxy patch payload wrong: %#v", patched)
		}
		// 旧版本编辑 → 409。
		client.do(http.MethodPatch, "/__aisys__/api/proxies/"+proxyID, map[string]any{
			"expectedUpdatedAt": dataString(created, "updatedAt"), "host": "127.0.0.3",
		}, wantStatus(http.StatusConflict))
		// 删除 → 空体 2xx（Go kernel 对空体写实测 200，与 accounts remove /
		// announcements delete 同一先例；再删 → 404）。
		client.do(http.MethodDelete, "/__aisys__/api/proxies/"+proxyID, nil, wantStatus(http.StatusOK))
		client.do(http.MethodDelete, "/__aisys__/api/proxies/"+proxyID, nil, wantStatus(http.StatusNotFound))
		// schema 失败 → 400 代理参数无效（经 localizer 保留中文）。
		client.do(http.MethodPost, "/__aisys__/api/proxies", map[string]any{
			"name": "坏代理", "type": "http", "host": "", "port": 8080,
		}, wantStatus(http.StatusBadRequest))
	})

	t.Run("table-monitor read faces", func(t *testing.T) {
		// fresh boot 快照表未建（采样 owner 是 jobs）：W6 typed unavailable 503，
		// 不伪造空结果。
		client.do(http.MethodGet, "/__aisys__/api/table-monitor/overview", nil, wantStatus(http.StatusServiceUnavailable))
		client.do(http.MethodGet, "/__aisys__/api/table-monitor/database-history", nil, wantStatus(http.StatusServiceUnavailable))
		client.do(http.MethodGet, "/__aisys__/api/table-monitor/history?databaseRole=business&tableName=accounts", nil, wantStatus(http.StatusServiceUnavailable))
		// 非法角色 → 400。
		client.do(http.MethodGet, "/__aisys__/api/table-monitor/history?databaseRole=ghost&tableName=accounts", nil, wantStatus(http.StatusBadRequest))
		// cleanup POST 仍由 Node 拥有（W6）：未配 bridge 时走 404 JSON 契约。
		client.do(http.MethodPost, "/__aisys__/api/table-monitor/non-business-data/cleanup", map[string]any{"cutoffAt": "2026-09-01T00:00:00.000Z"}, wantStatus(http.StatusNotFound))
	})

	t.Run("ui-bootstrap both surfaces", func(t *testing.T) {
		// admin 无目标 scope → 400 请选择目标系统账户。
		client.do(http.MethodGet, "/__aisys__/api/ui-bootstrap/options", nil, wantStatus(http.StatusBadRequest))
		// my-* 钳制自身 → 200。
		client.do(http.MethodGet, "/__aisys__/api/my-ui-bootstrap/options", nil, wantStatus(http.StatusOK))
	})

	t.Run("help surface default", func(t *testing.T) {
		// 缺省不配置 JUHE_AI_FRONTEND_DIST_PATH：help 面不挂载，走 404 JSON。
		client.do(http.MethodGet, "/__aisys__/help/", nil, wantStatus(http.StatusNotFound))
	})
}
