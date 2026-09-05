// X05 场景 1：fresh 启动。临时目录 + 随机端口 + SQLite 模式 → maintenance
// ensure+seed → gateway 启动 → 双健康面 200，进程级 /health 的 owner/worker
// 字段契约断言；随后以 seed 管理员完成一次真实登录，证明 fresh 库可用。
package acceptance

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAcceptanceFreshSQLiteBoot(t *testing.T) {
	fixture := startGateway(t, gatewayEnvOptions{})

	// 业务系统 API 健康（Node db-service 契约，kernel.HealthHandler）：
	// service=juhe-ai-db-service + accountBalance/proxyLatency ready 快照。
	response2, err := http.Get(fixture.baseURL + "/__aisys__/api/health")
	if err != nil {
		t.Fatalf("GET system health: %v", err)
	}
	defer response2.Body.Close()
	if response2.StatusCode != http.StatusOK {
		t.Fatalf("system health status=%d", response2.StatusCode)
	}
	var systemHealth map[string]any
	if err := json.NewDecoder(response2.Body).Decode(&systemHealth); err != nil {
		t.Fatalf("decode system health: %v", err)
	}
	if systemHealth["service"] != "juhe-ai-db-service" || systemHealth["status"] != "ok" {
		t.Fatalf("system health payload wrong: %#v", systemHealth)
	}
	if nested, _ := systemHealth["accountBalance"].(map[string]any); nested == nil || nested["ready"] != true {
		t.Fatalf("system health accountBalance wrong: %#v", systemHealth)
	}
	if nested, _ := systemHealth["proxyLatency"].(map[string]any); nested == nil || nested["ready"] != true {
		t.Fatalf("system health proxyLatency wrong: %#v", systemHealth)
	}

	// 进程级 /health（gateway main healthHandler）：owner/worker 字段契约。
	// 对齐 Go cmd/juhe-ai-gateway/main.go healthHandler。已知 F4 租约缺陷
	// 使 ready 滞后约 30-60s（见 harness waitForSystemAPIReady 注释），
	// 这里轮询至 ready 再断言字段。
	health := waitForProcessHealthReady(t, fixture)
	if health["ownerMode"] != "active" {
		t.Fatalf("gateway /health ownerMode=%v want active", health["ownerMode"])
	}
	if health["auditLogReady"] != true || health["operationLogReady"] != true {
		t.Fatalf("gateway /health owner components not ready: %#v", health)
	}
	for _, workerField := range []string{"j3bReady", "sessionRetentionReady", "accountCircuitRuntimeReady"} {
		if _, exists := health[workerField]; !exists {
			t.Fatalf("gateway /health missing worker field %s: %#v", workerField, health)
		}
	}

	// fresh 库 + seed 管理员可登录（密码为夹具重置后的 acceptanceAdminPassword，
	// 见 harness resetSeedAdminPassword 的缺陷说明）。
	client := newClient(t, fixture.baseURL)
	status, payload := client.do(http.MethodPost, "/__aisys__/api/auth/login",
		map[string]any{"username": "admin", "password": acceptanceAdminPassword}, wantStatus(http.StatusOK))
	if status != http.StatusOK {
		t.Fatalf("seed admin login status=%d", status)
	}
	user := data(payload)
	if user["username"] != "admin" || user["role"] != "super_admin" {
		t.Fatalf("seed admin login payload wrong: %#v", user)
	}
	if user["id"] != "sys_admin" {
		t.Fatalf("seed admin id wrong: %#v", user)
	}
}

// TestAcceptanceFreshPostgresBoot：PG 双模式门控变体（X05 双模式 fresh）。
// 无 JUHE_AI_ACCEPTANCE_PG_DSN 时 skip。PG 模式没有启动期 ensure+seed
// （Node runtime 拒绝 postgres 驱动；schema/seed 由外部 maintenance 命令
// 负责），因此这里先跑 maintenance --driver postgres --dsn 再启动。存储不
// 随用例销毁，断言降级为渲染级（健康 + 登录 + 只读列表）。
func TestAcceptanceFreshPostgresBoot(t *testing.T) {
	dsn := pgDSN()
	if dsn == "" {
		t.Skip("JUHE_AI_ACCEPTANCE_PG_DSN 未设置，跳过 PostgreSQL 验收模式")
	}
	fixture := startGateway(t, gatewayEnvOptions{PGDSN: dsn})

	// 进程级 /health 就绪（含 F4 租约缺陷的滞后预算）+ owner 字段。
	health := waitForProcessHealthReady(t, fixture)
	if health["ownerMode"] != "active" {
		t.Fatalf("gateway /health wrong in PG mode: %#v", health)
	}

	client := newClient(t, fixture.baseURL)
	client.do(http.MethodPost, "/__aisys__/api/auth/login",
		map[string]any{"username": "admin", "password": acceptanceAdminPassword}, wantStatus(http.StatusOK))
	// 渲染级断言：seed 分组列表可读（seed 分组名，见 pg_schema.go pgSeedGroups）。
	_, payload := client.do(http.MethodGet, "/__aisys__/api/groups", nil, wantStatus(http.StatusOK))
	items, _ := payload["data"].([]any)
	if len(items) == 0 {
		t.Fatalf("PG seed groups empty: %#v", payload)
	}
}
