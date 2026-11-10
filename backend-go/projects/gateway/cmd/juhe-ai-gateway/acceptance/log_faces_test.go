// X04 404 项补齐：日志读面的进程级 200 探测。go 模式下审计与公开接口日志
// 家族在 compose 挂载 logreads.ReadsDeps（F3/F5 数据集读），未登录保持 401
// 请先登录契约。运行日志读面（runtime-logs / grep）已于 2026-11-09 随 F1
// 下架，路由不再注册，不在本探测范围。
package acceptance

import (
	"net/http"
	"testing"
)

func TestAcceptanceLogReadFaces(t *testing.T) {
	fixture := startGateway(t, gatewayEnvOptions{})
	client := &acceptanceClient{t: t, http: fixture.admin, baseURL: fixture.baseURL}

	for _, path := range []string{
		"/__aisys__/api/audit-logs",
		"/__aisys__/api/audit-logs/runtime",
		"/__aisys__/api/audit-logs/search-hot",
		"/__aisys__/api/audit-logs/error-groups",
		"/__aisys__/api/public-api-logs",
	} {
		client.do(http.MethodGet, path, nil, wantStatus(http.StatusOK))
	}

	// search-hot 无关键字：available:true + 中文提示。
	_, hot := client.do(http.MethodGet, "/__aisys__/api/audit-logs/search-hot", nil, wantStatus(http.StatusOK))
	if data(hot)["available"] != true {
		t.Fatalf("search-hot available=%v", data(hot)["available"])
	}

	// 未登录 → 401 请先登录（requireAdmin 契约，不是 404 回落）。
	anonymous := &acceptanceClient{t: t, http: &http.Client{}, baseURL: fixture.baseURL}
	for _, path := range []string{
		"/__aisys__/api/audit-logs",
		"/__aisys__/api/audit-logs/search-hot",
		"/__aisys__/api/audit-logs/some-id",
		"/__aisys__/api/audit-logs/some-id/payloads/some-payload",
		"/__aisys__/api/public-api-logs",
	} {
		anonymous.do(http.MethodGet, path, nil, wantStatus(http.StatusUnauthorized))
	}
}
