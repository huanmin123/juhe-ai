package statreads

// health-snapshot 聚合读面契约测试：gateway 段进程内透传 readiness 载荷、
// jobs 段按注入 URL 拉取 /health 原样透传，地址缺失/不可达/非 200/非 JSON
// 一律 available:false + reason 降级。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func invokeHealthSnapshot(t *testing.T, deps *Deps) map[string]any {
	t.Helper()
	recorder := invoke(t, deps.healthSnapshotHandler, http.MethodGet, "/", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("health-snapshot 必须 200: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("health-snapshot 必须带 no-store")
	}
	return dataMap(t, decodeBody(t, recorder))
}

func TestHealthSnapshotPassesGatewayReadinessThrough(t *testing.T) {
	fixture := newFixture(t)
	fixture.deps.GatewayReadiness = func() (int, map[string]any) {
		return http.StatusServiceUnavailable, map[string]any{
			"ready":                      false,
			"ownerReady":                 false,
			"ownerMode":                  "secondary",
			"auditLogReady":              false,
			"operationLogReady":          true,
			"j3bReady":                   true,
			"sessionRetentionReady":      true,
			"accountCircuitRuntimeReady": true,
		}
	}
	fixture.deps.JobsHealthURL = ""
	payload := invokeHealthSnapshot(t, fixture.deps)
	if payload["checkedAt"] != "2026-09-04T12:00:00Z" {
		t.Fatalf("checkedAt 契约错误: %#v", payload["checkedAt"])
	}
	gateway, ok := payload["gateway"].(map[string]any)
	if !ok {
		t.Fatalf("gateway 段必须为对象: %#v", payload["gateway"])
	}
	if gateway["ready"] != false || gateway["ownerMode"] != "secondary" || gateway["auditLogReady"] != false || gateway["operationLogReady"] != true {
		t.Fatalf("gateway readiness 载荷必须原样透传: %#v", gateway)
	}
	jobs, ok := payload["jobs"].(map[string]any)
	if !ok || jobs["available"] != false || jobs["reason"] == nil || jobs["payload"] != nil {
		t.Fatalf("未配置 jobs 地址必须降级 available:false + reason: %#v", jobs)
	}
}

func TestHealthSnapshotFetchesJobsHealthPayload(t *testing.T) {
	fixture := newFixture(t)
	jobsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ready":        true,
			"ownerMode":    "primary",
			"workerWired":  []string{"usage-stats-aggregation"},
			"workerJobs":   []any{},
			"extraIgnored": nil,
		})
	}))
	defer jobsServer.Close()
	fixture.deps.GatewayReadiness = func() (int, map[string]any) {
		return http.StatusOK, map[string]any{"ready": true, "ownerMode": "primary"}
	}
	fixture.deps.JobsHealthURL = jobsServer.URL + "/health"

	payload := invokeHealthSnapshot(t, fixture.deps)
	gateway := payload["gateway"].(map[string]any)
	if gateway["ready"] != true {
		t.Fatalf("gateway 载荷透传错误: %#v", gateway)
	}
	jobs, ok := payload["jobs"].(map[string]any)
	if !ok || jobs["available"] != true {
		t.Fatalf("可达的 jobs /health 必须 available:true: %#v", jobs)
	}
	if _, hasReason := jobs["reason"]; hasReason {
		t.Fatalf("available:true 时不得携带 reason: %#v", jobs)
	}
	jobsPayload, ok := jobs["payload"].(map[string]any)
	if !ok || jobsPayload["ready"] != true || jobsPayload["ownerMode"] != "primary" {
		t.Fatalf("jobs /health 载荷必须原样透传: %#v", jobs["payload"])
	}
	if _, hasWired := jobsPayload["workerWired"]; !hasWired {
		t.Fatalf("jobs 载荷字段必须保留: %#v", jobsPayload)
	}
}

func TestHealthSnapshotDegradesOnJobsFetchFailures(t *testing.T) {
	fixture := newFixture(t)
	fixture.deps.GatewayReadiness = func() (int, map[string]any) {
		return http.StatusOK, map[string]any{"ready": true}
	}
	// 不可达地址：连接被拒绝。
	fixture.deps.JobsHealthURL = "http://127.0.0.1:1/health"
	jobs := invokeHealthSnapshot(t, fixture.deps)["jobs"].(map[string]any)
	if jobs["available"] != false || jobs["reason"] == nil || jobs["payload"] != nil {
		t.Fatalf("不可达 jobs 必须降级: %#v", jobs)
	}

	// 非 200。
	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "jobs degraded", http.StatusServiceUnavailable)
	}))
	defer errorServer.Close()
	fixture.deps.JobsHealthURL = errorServer.URL + "/health"
	jobs = invokeHealthSnapshot(t, fixture.deps)["jobs"].(map[string]any)
	if jobs["available"] != false {
		t.Fatalf("非 200 必须降级: %#v", jobs)
	}
	reason, _ := jobs["reason"].(string)
	if reason == "" {
		t.Fatalf("非 200 必须携带 reason: %#v", jobs)
	}

	// 非 JSON 响应体。
	plainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer plainServer.Close()
	fixture.deps.JobsHealthURL = plainServer.URL + "/health"
	jobs = invokeHealthSnapshot(t, fixture.deps)["jobs"].(map[string]any)
	if jobs["available"] != false {
		t.Fatalf("非 JSON 必须降级: %#v", jobs)
	}
}

func TestJobsHealthURLJoinsListenAddress(t *testing.T) {
	if got := JobsHealthURL(" 127.0.0.1:3305 "); got != "http://127.0.0.1:3305/health" {
		t.Fatalf("地址拼接错误: %q", got)
	}
	if got := JobsHealthURL("   "); got != "" {
		t.Fatalf("空地址必须返回空串: %q", got)
	}
	if got := JobsHealthURL(""); got != "" {
		t.Fatalf("未配置必须返回空串: %q", got)
	}
}
