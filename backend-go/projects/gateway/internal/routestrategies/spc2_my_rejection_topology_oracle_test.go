// 调度偏好独立实体 P2 收口补钉（计划 D6 / D7 第 6 条）：
//  1. my-*（个人）POST/PATCH 写入口对退场调度字段的显式拒绝——与
//     管理 /route-strategies（TestWlSchedulePrefHTTPModeMatrix、
//     TestW9EStrategyUpdateArms）与公开 route-strategy/add|update
//     （aipublic TestSPC2PublicStrategy*）同契约，补齐个人镜像面的专测；
//  2. 拓扑（groupBindings）PATCH 的存储 oracle——不携带调度键的绑定替换
//     不得触发退场拒绝，存量 config_json（含调度键）逐字节保持
//     （与 TestWlSchedulePrefPatchFeedForward 的名称/mode 臂互补）。
package routestrategies

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
)

func TestSPC2MyRouteStrategyRejectsRetiredSchedulingFields(t *testing.T) {
	env := newTestEnv(t)
	carolID := env.login(t, "carol", "carol-pass", "user")
	groupID := env.createGroup(t, carolID, "spc2-my-组", true)
	myPath := "/__aisys__/api/my-route-strategies"

	cases := []struct {
		name string
		key  string
	}{
		{"schedulingPreference", `"schedulingPreference":"speed_first"`},
		{"speedFirstConfig", `"speedFirstConfig":{"slowTriggerCount":3}`},
		{"firstByteDeadlineMs", `"firstByteDeadlineMs":30000`},
		{"firstByteThresholdMs", `"firstByteThresholdMs":30000`},
	}
	for _, testCase := range cases {
		t.Run("post-"+testCase.name, func(t *testing.T) {
			body := `{"name":"spc2-my-` + testCase.name + `","groupBindings":[` + bindingJSON(groupID, 0) + `],"normalRoutingConfig":{` + testCase.key + `}}`
			code, payload := env.createStrategy(t, myPath, body)
			if code != http.StatusBadRequest || payload["message"] != LegacySchedulingConfigRejectedMessage {
				t.Fatalf("my POST 携带 %s 必须 400 专用文案: %d %v", testCase.name, code, payload)
			}
		})
	}

	// 不携带退场键的创建臂保持原行为。
	code, created := env.createStrategy(t, myPath, `{"name":"spc2-my-clean","groupBindings":[`+bindingJSON(groupID, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("my clean create: %d %v", code, created)
	}
	strategyID := dataMap(t, created)["id"].(string)

	for _, testCase := range cases {
		t.Run("patch-"+testCase.name, func(t *testing.T) {
			updatedAt := env.strategyUpdatedAt(t, strategyID)
			body := `{"expectedUpdatedAt":"` + updatedAt + `","normalRoutingConfig":{` + testCase.key + `}}`
			code, payload := env.do(t, http.MethodPatch, myPath+"/"+strategyID, body)
			if code != http.StatusBadRequest || payload["message"] != LegacySchedulingConfigRejectedMessage {
				t.Fatalf("my PATCH 携带 %s 必须 400 专用文案: %d %v", testCase.name, code, payload)
			}
		})
	}

	// 不携带退场键的 PATCH（停用）不受影响。
	updatedAt := env.strategyUpdatedAt(t, strategyID)
	code, patched := env.do(t, http.MethodPatch, myPath+"/"+strategyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","status":"disabled"}`)
	if code != http.StatusOK {
		t.Fatalf("my clean patch: %d %v", code, patched)
	}
}

// TestSPC2TopologyPatchPreservesStoredSchedulingConfigByteForByte：存量行
//（store 直建，代表迁移完成世界的旧数据/回滚 oracle）speed_first 配置在仅
// 替换分组绑定（拓扑）时逐字节保持；payload 不含调度键故不触发退场拒绝，
// 绑定变更照常生效——D7 第 6 条「其他合法数据不可整列误删」的拓扑臂。
func TestSPC2TopologyPatchPreservesStoredSchedulingConfigByteForByte(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "spc2-topo-a", true)
	groupB := env.createGroup(t, adminID, "spc2-topo-b", true)

	strategyID := env.createStrategyViaStore(t, adminID, "spc2-topo", ModeNormal, []string{groupA}, map[string]any{
		"schedulingPreference": "speed_first",
		"firstByteDeadlineMs":  25000,
	})

	var configJSON sql.NullString
	if err := env.db.QueryRow(`SELECT config_json FROM route_strategies WHERE id = ?`, strategyID).Scan(&configJSON); err != nil {
		t.Fatal(err)
	}
	if !configJSON.Valid || !strings.Contains(configJSON.String, "speed_first") {
		t.Fatalf("存量 speed_first 配置必须已落盘: %v", configJSON)
	}
	beforeJSON := configJSON.String

	// 仅替换绑定（groupA → groupB，normal 模式仍单绑定）：不携带任何调度键。
	updatedAt := env.strategyUpdatedAt(t, strategyID)
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/route-strategies/"+strategyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","groupBindings":[`+bindingJSON(groupB, 1)+`]}`)
	if code != http.StatusOK {
		t.Fatalf("拓扑 patch: %d %v", code, patched)
	}
	if !changedSet(t, patched)["groupBindings"] {
		t.Fatalf("groupBindings 必须出现在 changedFields: %v", patched)
	}
	// 存量 config_json 逐字节保持（调度键原样不变）。
	if err := env.db.QueryRow(`SELECT config_json FROM route_strategies WHERE id = ?`, strategyID).Scan(&configJSON); err != nil {
		t.Fatal(err)
	}
	if !configJSON.Valid || configJSON.String != beforeJSON {
		t.Fatalf("拓扑 PATCH 不得改写存量 config_json: before=%s after=%v", beforeJSON, configJSON)
	}
	// detail 照常渲染保留的 speed_first 配置（存储 oracle 只读语义）。
	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/route-strategies/"+strategyID, "")
	if code != http.StatusOK {
		t.Fatalf("detail: %d %v", code, detail)
	}
	detailConfig, ok := dataMap(t, detail)["normalRoutingConfig"].(map[string]any)
	if !ok || detailConfig["schedulingPreference"] != "speed_first" {
		t.Fatalf("detail 必须保留 speed_first: %v", dataMap(t, detail)["normalRoutingConfig"])
	}
}
