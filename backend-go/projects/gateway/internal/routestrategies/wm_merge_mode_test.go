// 合并路由（merge 模式）注册与配置校验回归：
//   - 模式常量 / 谓词（IsRouteStrategyMode、ModeSupportsSchedulingPreference）；
//   - normalizeConfigForWrite 的 merge 分支 = 既有 passthrough 分支（接受
//     normalRaw），merge 不需要独立分支（设计 B4）；
//   - validateModeBindings merge ≥2 启用分组（设计 B1，允许并存 disabled）；
//   - 调度偏好矩阵：speed_first 落盘 / cost_first NULL / 五种调度模式间
//     feed-forward（设计 B4/B6）；
//   - HTTP 层创建 / patch / 单绑定与降绑校验 / list mode 过滤（设计 B1/B5）。
package routestrategies

import (
	"database/sql"
	"net/http"
	"testing"
)

// TestWMMergeModePredicates：merge 加入两个模式谓词白名单。
func TestWMMergeModePredicates(t *testing.T) {
	if !IsRouteStrategyMode(ModeMerge) {
		t.Fatal("IsRouteStrategyMode(merge) 必须为 true")
	}
	if !ModeSupportsSchedulingPreference(ModeMerge) {
		t.Fatal("ModeSupportsSchedulingPreference(merge) 必须为 true")
	}
	// 既有模式回归：仍是合法模式。
	for _, mode := range []string{ModeNormal, ModeWeighted, ModeFailover, ModeRoundRobin} {
		if !IsRouteStrategyMode(mode) {
			t.Fatalf("IsRouteStrategyMode(%q) 必须为 true", mode)
		}
	}
	for _, mode := range []string{"", "bogus", "merge2"} {
		if IsRouteStrategyMode(mode) {
			t.Fatalf("IsRouteStrategyMode(%q) 必须为 false", mode)
		}
		if ModeSupportsSchedulingPreference(mode) {
			t.Fatalf("ModeSupportsSchedulingPreference(%q) 必须为 false", mode)
		}
	}
}

// TestWMMergeNormalizeForWrite：normalizeConfigForWrite 的 merge 分支复用
// passthrough 行为（设计 B4，无需独立分支）。
func TestWMMergeNormalizeForWrite(t *testing.T) {
	// nil 输入回落 cost_first 默认对象。
	normal, err := normalizeConfigForWrite(nil, ModeMerge)
	if err != nil || normal == nil || normal.SchedulingPreference != defaultNormalSchedulingPreference {
		t.Fatalf("nil 输入: normal=%+v err=%v", normal, err)
	}
	// cost_first 归一为仅 preference 且保持 config_json NULL。
	normal, err = normalizeConfigForWrite(map[string]any{"schedulingPreference": "cost_first"}, ModeMerge)
	if err != nil || normal == nil || normal.FirstByteDeadlineMs != nil || normal.SpeedFirstConfig != nil {
		t.Fatalf("cost_first: normal=%+v err=%v", normal, err)
	}
	if routeStrategyConfigJSON(normal).Valid {
		t.Fatal("merge cost_first 必须存 NULL")
	}
	// speed_first 附带 deadline + 完整 speedFirstConfig 默认回填。
	normal, err = normalizeConfigForWrite(map[string]any{
		"schedulingPreference": "speed_first",
		"firstByteDeadlineMs":  20000,
		"speedFirstConfig":     map[string]any{"slowTriggerCount": 5},
	}, ModeMerge)
	if err != nil || normal == nil {
		t.Fatalf("speed_first: normal=%+v err=%v", normal, err)
	}
	if normal.SchedulingPreference != "speed_first" || normal.FirstByteDeadlineMs == nil || *normal.FirstByteDeadlineMs != 20000 {
		t.Fatalf("speed_first 归一: %+v", normal)
	}
	if normal.SpeedFirstConfig == nil || normal.SpeedFirstConfig.SlowTriggerCount != 5 || normal.SpeedFirstConfig.MaxFirstByteRetriesPerRequest != 2 {
		t.Fatalf("speedFirstConfig 默认回填: %+v", normal.SpeedFirstConfig)
	}
	// 非法 preference → 调度偏好无效。
	if _, err = normalizeConfigForWrite(map[string]any{"schedulingPreference": "bogus"}, ModeMerge); err == nil || err.Error() != "调度偏好无效" {
		t.Fatalf("非法 preference: err=%v", err)
	}
	// normalInput feed-forward：merge 无新输入时保留当前配置。
	current := &NormalRoutingConfig{SchedulingPreference: "cost_first"}
	if got := (MutationInput{}).normalInput(current); got == nil {
		t.Fatal("merge 无新输入必须 feed-forward 当前配置")
	}
}

// TestWMMergeHTTPBindingRules：merge 绑定数量校验与模式切换（设计 B1）。
func TestWMMergeHTTPBindingRules(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	groupB := env.createGroup(t, adminID, "bravo", true)
	groupC := env.createGroup(t, adminID, "charlie", true)
	path := "/__aisys__/api/route-strategies"

	// merge 单绑定 → 400 新文案。
	code, payload := env.createStrategy(t, path,
		`{"name":"m1","mode":"merge","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusBadRequest || payload["message"] != "合并路由至少需要两个启用分组" {
		t.Fatalf("merge 单绑定: %d %v", code, payload)
	}

	// merge 双启用绑定 → 201。
	code, created := env.createStrategy(t, path,
		`{"name":"m2","mode":"merge","groupBindings":[`+bindingJSON(groupA, 0)+`,`+bindingJSON(groupB, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("merge create: %d %v", code, created)
	}
	mergeID := dataMap(t, created)["id"].(string)
	createdData := dataMap(t, created)
	if createdData["mode"] != "merge" || createdData["bindingCount"] != float64(2) {
		t.Fatalf("merge create payload: %v", createdData)
	}
	config, ok := createdData["normalRoutingConfig"].(map[string]any)
	if !ok || config["schedulingPreference"] != "cost_first" {
		t.Fatalf("merge 默认渲染 cost_first: %v", createdData["normalRoutingConfig"])
	}

	// merge 允许并存 disabled 绑定：2 启用 + 1 停用 → 201。
	code, payload = env.createStrategy(t, path,
		`{"name":"m3","mode":"merge","groupBindings":[`+bindingJSON(groupA, 0)+`,`+bindingJSON(groupB, 0)+`,{"groupId":"`+groupC+`","status":"disabled"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("merge disabled 并存: %d %v", code, payload)
	}
	m3ID := dataMap(t, payload)["id"].(string)
	// 1 启用 + 1 停用 → 400。
	code, payload = env.createStrategy(t, path,
		`{"name":"m4","mode":"merge","groupBindings":[`+bindingJSON(groupA, 0)+`,{"groupId":"`+groupC+`","status":"disabled"}]}`)
	if code != http.StatusBadRequest || payload["message"] != "合并路由至少需要两个启用分组" {
		t.Fatalf("merge 单启用 + disabled: %d %v", code, payload)
	}

	// normal 单绑定裸切 merge → 400。
	code, normalCreated := env.createStrategy(t, path,
		`{"name":"n1","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("normal create: %d %v", code, normalCreated)
	}
	normalID := dataMap(t, normalCreated)["id"].(string)
	updatedAt := env.strategyUpdatedAt(t, normalID)
	code, payload = env.do(t, http.MethodPatch, path+"/"+normalID,
		`{"expectedUpdatedAt":"`+updatedAt+`","mode":"merge"}`)
	if code != http.StatusBadRequest || payload["message"] != "合并路由至少需要两个启用分组" {
		t.Fatalf("normal 裸切 merge: %d %v", code, payload)
	}

	// merge 双绑定切 normal（同时带 1 个绑定）→ 200。
	updatedAt = env.strategyUpdatedAt(t, mergeID)
	code, payload = env.do(t, http.MethodPatch, path+"/"+mergeID,
		`{"expectedUpdatedAt":"`+updatedAt+`","mode":"normal","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusOK {
		t.Fatalf("merge 切 normal（1 绑定）: %d %v", code, payload)
	}
	if changedSet(t, payload)["mode"] != true {
		t.Fatalf("mode 必须出现在 changedFields: %v", payload)
	}

	// merge patch 只改绑定、降到一个启用组 → 400（绑定整体替换同样过模式校验）。
	code, created = env.createStrategy(t, path,
		`{"name":"m5","mode":"merge","groupBindings":[`+bindingJSON(groupA, 0)+`,`+bindingJSON(groupB, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("merge create 2: %d %v", code, created)
	}
	mergeID2 := dataMap(t, created)["id"].(string)
	updatedAt = env.strategyUpdatedAt(t, mergeID2)
	code, payload = env.do(t, http.MethodPatch, path+"/"+mergeID2,
		`{"expectedUpdatedAt":"`+updatedAt+`","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusBadRequest || payload["message"] != "合并路由至少需要两个启用分组" {
		t.Fatalf("merge 降绑到一个启用组: %d %v", code, payload)
	}
	// merge 行必须原样保留（校验失败不得破坏既有绑定）。
	if env.count(t, `SELECT COUNT(*) FROM route_strategy_groups WHERE route_strategy_id = ?`, mergeID2) != 2 {
		t.Fatal("校验失败后绑定行不得变化")
	}

	// mode=merge 列表过滤（设计 B5）：命中 m3 与 m5（m2 已切 normal）。
	code, filtered := env.do(t, http.MethodGet, path+"?mode=merge", "")
	if code != 200 || len(items(t, filtered)) != 2 {
		t.Fatalf("mode=merge 过滤: %d %v", code, filtered)
	}
	mergeIDs := map[string]bool{}
	for _, raw := range items(t, filtered) {
		mergeIDs[raw.(map[string]any)["id"].(string)] = true
	}
	if !mergeIDs[mergeID2] || !mergeIDs[m3ID] {
		t.Fatalf("mode=merge 必须命中 m3/m5: %v", mergeIDs)
	}
}

// TestWMMergeSchedulingPreferenceMatrix：merge 的 speed_first/cost_first 落盘、
// 渲染与五种调度模式间的 feed-forward（设计 B4/B6，模式矩阵补齐）。
func TestWMMergeSchedulingPreferenceMatrix(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	groupB := env.createGroup(t, adminID, "bravo", true)
	path := "/__aisys__/api/route-strategies"

	// speed_first：201 + config_json 落盘 + 响应渲染 normalRoutingConfig。
	code, created := env.createStrategy(t, path,
		`{"name":"msf","mode":"merge","normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":30000,"speedFirstConfig":{"slowTriggerCount":4}},"groupBindings":[`+bindingJSON(groupA, 0)+`,`+bindingJSON(groupB, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("merge speed_first create: %d %v", code, created)
	}
	createdData := dataMap(t, created)
	config, ok := createdData["normalRoutingConfig"].(map[string]any)
	if !ok || config["schedulingPreference"] != "speed_first" || config["firstByteDeadlineMs"] != float64(30000) {
		t.Fatalf("merge speed_first 响应渲染: %v", createdData["normalRoutingConfig"])
	}
	speedFirst := config["speedFirstConfig"].(map[string]any)
	if speedFirst["slowTriggerCount"] != float64(4) || speedFirst["probeIntervalSeconds"] != float64(30) {
		t.Fatalf("merge speedFirstConfig: %v", speedFirst)
	}
	mergeID := createdData["id"].(string)
	var configJSON sql.NullString
	if err := env.db.QueryRow(`SELECT config_json FROM route_strategies WHERE id = ?`, mergeID).Scan(&configJSON); err != nil {
		t.Fatal(err)
	}
	if !configJSON.Valid || !contains(configJSON.String, "speed_first") {
		t.Fatalf("merge speed_first config_json 必须落盘: %v", configJSON)
	}
	beforeJSON := configJSON.String

	// cost_first：渲染默认对象、config_json 保持 NULL。
	code, payload := env.createStrategy(t, path,
		`{"name":"mcf","mode":"merge","groupBindings":[`+bindingJSON(groupA, 0)+`,`+bindingJSON(groupB, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("merge cost_first create: %d %v", code, payload)
	}
	costData := dataMap(t, payload)
	costConfig, ok := costData["normalRoutingConfig"].(map[string]any)
	if !ok || costConfig["schedulingPreference"] != "cost_first" {
		t.Fatalf("merge cost_first 响应: %v", costData["normalRoutingConfig"])
	}
	if err := env.db.QueryRow(`SELECT config_json FROM route_strategies WHERE id = ?`, costData["id"].(string)).Scan(&configJSON); err != nil {
		t.Fatal(err)
	}
	if configJSON.Valid {
		t.Fatalf("merge cost_first config_json 必须为 NULL: %v", configJSON)
	}

	// feed-forward：merge(speed_first) 裸切 weighted 保留配置（config_json
	// 逐字节不变），detail 渲染 speed_first。
	updatedAt := env.strategyUpdatedAt(t, mergeID)
	code, patched := env.do(t, http.MethodPatch, path+"/"+mergeID,
		`{"expectedUpdatedAt":"`+updatedAt+`","mode":"weighted"}`)
	if code != http.StatusOK {
		t.Fatalf("merge → weighted: %d %v", code, patched)
	}
	if err := env.db.QueryRow(`SELECT config_json FROM route_strategies WHERE id = ?`, mergeID).Scan(&configJSON); err != nil {
		t.Fatal(err)
	}
	if !configJSON.Valid || configJSON.String != beforeJSON {
		t.Fatalf("weighted 切换不得改写 config_json: before=%s after=%v", beforeJSON, configJSON)
	}
	code, detail := env.do(t, http.MethodGet, path+"/"+mergeID, "")
	if code != http.StatusOK {
		t.Fatalf("detail: %d %v", code, detail)
	}
	detailConfig, ok := dataMap(t, detail)["normalRoutingConfig"].(map[string]any)
	if !ok || detailConfig["schedulingPreference"] != "speed_first" {
		t.Fatalf("weighted detail 必须保留 speed_first: %v", dataMap(t, detail)["normalRoutingConfig"])
	}

	// 裸切回 merge（双绑定保留）：调度配置继续 feed-forward。
	updatedAt = env.strategyUpdatedAt(t, mergeID)
	code, patched = env.do(t, http.MethodPatch, path+"/"+mergeID,
		`{"expectedUpdatedAt":"`+updatedAt+`","mode":"merge"}`)
	if code != http.StatusOK {
		t.Fatalf("weighted → merge: %d %v", code, patched)
	}
	if err := env.db.QueryRow(`SELECT config_json FROM route_strategies WHERE id = ?`, mergeID).Scan(&configJSON); err != nil {
		t.Fatal(err)
	}
	if !configJSON.Valid || configJSON.String != beforeJSON {
		t.Fatalf("merge 切回不得改写 config_json: before=%s after=%v", beforeJSON, configJSON)
	}
	// merge + normalRoutingConfig 输入的 patch 照常接受（speed_first → cost_first）。
	updatedAt = env.strategyUpdatedAt(t, mergeID)
	code, patched = env.do(t, http.MethodPatch, path+"/"+mergeID,
		`{"expectedUpdatedAt":"`+updatedAt+`","normalRoutingConfig":{"schedulingPreference":"cost_first"}}`)
	if code != http.StatusOK {
		t.Fatalf("merge cost_first patch: %d %v", code, patched)
	}
	if err := env.db.QueryRow(`SELECT config_json FROM route_strategies WHERE id = ?`, mergeID).Scan(&configJSON); err != nil {
		t.Fatal(err)
	}
	if configJSON.Valid {
		t.Fatalf("cost_first patch 后 config_json 必须清为 NULL: %v", configJSON)
	}
}
