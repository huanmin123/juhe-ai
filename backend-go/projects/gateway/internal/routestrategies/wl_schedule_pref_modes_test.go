// 调度偏好通用化回归：全部五种模式共享 normalRoutingConfig（speed_first 落盘、
// cost_first 保持 config_json NULL）。
package routestrategies

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
)

func TestWlSchedulePrefModeSupportsPredicate(t *testing.T) {
	for _, mode := range []string{ModeNormal, ModeWeighted, ModeFailover, ModeRoundRobin, ModeMerge} {
		if !ModeSupportsSchedulingPreference(mode) {
			t.Fatalf("mode %q 必须支持调度偏好", mode)
		}
	}
	for _, mode := range []string{"", "bogus"} {
		if ModeSupportsSchedulingPreference(mode) {
			t.Fatalf("mode %q 不得支持调度偏好", mode)
		}
	}
}

// TestWlSchedulePrefNormalizeForWriteModes：normalizeConfigForWrite 的模式矩阵。
func TestWlSchedulePrefNormalizeForWriteModes(t *testing.T) {
	speedRaw := func() map[string]any {
		return map[string]any{
			"schedulingPreference": "speed_first",
			"firstByteDeadlineMs":  20000,
			"speedFirstConfig":     map[string]any{"slowTriggerCount": 5},
		}
	}
	costRaw := map[string]any{"schedulingPreference": "cost_first"}
	badPrefRaw := map[string]any{"schedulingPreference": "bogus"}

	for _, mode := range []string{ModeNormal, ModeWeighted, ModeFailover, ModeRoundRobin, ModeMerge} {
		t.Run(mode, func(t *testing.T) {
			// nil 输入回落 cost_first 默认对象（routeStrategyConfigJSON 仍判 NULL）。
			normal, err := normalizeConfigForWrite(nil, mode)
			if err != nil || normal == nil || normal.SchedulingPreference != defaultNormalSchedulingPreference {
				t.Fatalf("nil 输入: normal=%+v err=%v", normal, err)
			}
			// cost_first 归一为仅 preference。
			normal, err = normalizeConfigForWrite(costRaw, mode)
			if err != nil || normal == nil || normal.FirstByteDeadlineMs != nil || normal.SpeedFirstConfig != nil {
				t.Fatalf("cost_first: normal=%+v err=%v", normal, err)
			}
			if routeStrategyConfigJSON(normal).Valid {
				t.Fatal("cost_first 必须存 NULL")
			}
			// speed_first 附带 deadline + 完整 speedFirstConfig。
			normal, err = normalizeConfigForWrite(speedRaw(), mode)
			if err != nil || normal == nil {
				t.Fatalf("speed_first: normal=%+v err=%v", normal, err)
			}
			if normal.SchedulingPreference != "speed_first" || normal.FirstByteDeadlineMs == nil || *normal.FirstByteDeadlineMs != 20000 {
				t.Fatalf("speed_first 归一: %+v", normal)
			}
			if normal.SpeedFirstConfig == nil || normal.SpeedFirstConfig.SlowTriggerCount != 5 ||
				normal.SpeedFirstConfig.SlowWindowSeconds != 120 || normal.SpeedFirstConfig.MaxFirstByteRetriesPerRequest != 2 {
				t.Fatalf("speedFirstConfig 默认回填: %+v", normal.SpeedFirstConfig)
			}
			// 非法 preference → 调度偏好无效。
			if _, err = normalizeConfigForWrite(badPrefRaw, mode); err == nil || err.Error() != "调度偏好无效" {
				t.Fatalf("非法 preference: err=%v", err)
			}
		})
	}
}

// TestWlSchedulePrefConfigJSONRoundTrip：三新模式 speed_first 落盘后可回读。
func TestWlSchedulePrefConfigJSONRoundTrip(t *testing.T) {
	speedRaw := map[string]any{
		"schedulingPreference": "speed_first",
		"firstByteDeadlineMs":  45000,
		"speedFirstConfig":     map[string]any{"probeIntervalSeconds": 45},
	}
	for _, mode := range []string{ModeWeighted, ModeFailover, ModeRoundRobin} {
		normal, err := normalizeConfigForWrite(speedRaw, mode)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		stored := routeStrategyConfigJSON(normal)
		if !stored.Valid {
			t.Fatalf("%s speed_first 必须落盘", mode)
		}
		var document map[string]json.RawMessage
		if err := json.Unmarshal([]byte(stored.String), &document); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if _, has := document["normalRoutingConfig"]; !has {
			t.Fatalf("%s 落盘缺 normalRoutingConfig 键: %s", mode, stored.String)
		}
		parsedNormal, err := parseStoredConfig(stored)
		if err != nil || parsedNormal == nil {
			t.Fatalf("%s 回读: normal=%+v err=%v", mode, parsedNormal, err)
		}
		if parsedNormal.SchedulingPreference != "speed_first" || parsedNormal.FirstByteDeadlineMs == nil || *parsedNormal.FirstByteDeadlineMs != 45000 {
			t.Fatalf("%s 回读值: %+v", mode, parsedNormal)
		}
		// cost_first（含 nil 归一结果）保持 NULL。
		costNormal, err := normalizeConfigForWrite(nil, mode)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if stored := routeStrategyConfigJSON(costNormal); stored.Valid {
			t.Fatalf("%s cost_first 必须存 NULL: %v", mode, stored)
		}
		// 坏值经 parseStoredConfig 报错。
		if _, err := parseStoredConfig(sql.NullString{String: `{"normalRoutingConfig":{"schedulingPreference":"bogus"}}`, Valid: true}); err == nil {
			t.Fatalf("%s 坏值必须报错", mode)
		}
	}
}

// TestWlSchedulePrefNormalInput：各模式 feed-forward 当前配置。
func TestWlSchedulePrefNormalInput(t *testing.T) {
	current := &NormalRoutingConfig{SchedulingPreference: "cost_first"}
	empty := MutationInput{}
	for _, mode := range []string{ModeNormal, ModeWeighted, ModeFailover, ModeRoundRobin, ModeMerge} {
		if got := empty.normalInput(current); got == nil {
			t.Fatalf("mode %q 无新输入必须 feed-forward 当前配置", mode)
		}
	}
	withRaw := MutationInput{HasNormalConfig: true, NormalConfigRaw: map[string]any{"schedulingPreference": "speed_first"}}
	if got := withRaw.normalInput(current); got == nil {
		t.Fatal("带 HasNormalConfig 必须透传 raw")
	}
}

// TestWlSchedulePrefPatchFeedForward：normal(speed_first) → weighted patch 保留
// 调度配置（config_json 不变）。
func TestWlSchedulePrefPatchFeedForward(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	path := "/__aisys__/api/route-strategies"

	code, created := env.createStrategy(t, path,
		`{"name":"ff","normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":25000},"groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	strategyID := dataMap(t, created)["id"].(string)

	var configJSON sql.NullString
	if err := env.db.QueryRow(`SELECT config_json FROM route_strategies WHERE id = ?`, strategyID).Scan(&configJSON); err != nil {
		t.Fatal(err)
	}
	if !configJSON.Valid || configJSON.String == "" {
		t.Fatal("speed_first 创建后 config_json 必须落盘")
	}
	beforeJSON := configJSON.String

	// mode 切到 weighted：无 normalRoutingConfig 输入，feed-forward 保留。
	updatedAt := env.strategyUpdatedAt(t, strategyID)
	code, patched := env.do(t, http.MethodPatch, path+"/"+strategyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","mode":"weighted"}`)
	if code != http.StatusOK {
		t.Fatalf("patch to weighted: %d %v", code, patched)
	}
	if !changedSet(t, patched)["mode"] {
		t.Fatalf("mode 必须出现在 changedFields: %v", patched)
	}
	rowPatch := dataMap(t, patched)["rowPatch"].(map[string]any)
	// feed-forward 后配置值未变化：rowPatch 不携带 normalRoutingConfig 键。
	if value, has := rowPatch["normalRoutingConfig"]; has && value != nil {
		t.Fatalf("配置未变时 rowPatch 不得携带 normalRoutingConfig: %v", rowPatch)
	}
	if err := env.db.QueryRow(`SELECT config_json FROM route_strategies WHERE id = ?`, strategyID).Scan(&configJSON); err != nil {
		t.Fatal(err)
	}
	if !configJSON.Valid || configJSON.String != beforeJSON {
		t.Fatalf("weighted 切换不得改写 config_json: before=%s after=%v", beforeJSON, configJSON)
	}
	// GET detail：weighted 模式渲染保留的 speed_first 配置。
	code, detail := env.do(t, http.MethodGet, path+"/"+strategyID, "")
	if code != http.StatusOK {
		t.Fatalf("detail: %d %v", code, detail)
	}
	detailConfig, ok := dataMap(t, detail)["normalRoutingConfig"].(map[string]any)
	if !ok || detailConfig["schedulingPreference"] != "speed_first" {
		t.Fatalf("weighted detail 必须保留 speed_first: %v", dataMap(t, detail)["normalRoutingConfig"])
	}
}

// TestWlSchedulePrefHTTPModeMatrix：三种新模式 speed_first/cost_first 的
// 创建、落盘与渲染。
func TestWlSchedulePrefHTTPModeMatrix(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	groupB := env.createGroup(t, adminID, "bravo", true)
	path := "/__aisys__/api/route-strategies"

	// failover 需要主备两个启用分组，其余模式单绑定即可。
	bindingsFor := func(mode string) string {
		if mode == ModeFailover {
			return bindingJSON(groupA, 0) + "," + bindingJSON(groupB, 0)
		}
		return bindingJSON(groupA, 0)
	}
	speedBody := func(mode, name string) string {
		return `{"name":"` + name + `","mode":"` + mode + `","normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":30000,"speedFirstConfig":{"slowTriggerCount":4}},"groupBindings":[` + bindingsFor(mode) + `]}`
	}
	for _, mode := range []string{ModeWeighted, ModeFailover, ModeRoundRobin} {
		code, payload := env.createStrategy(t, path, speedBody(mode, "sf-"+mode))
		if code != http.StatusCreated {
			t.Fatalf("%s create: %d %v", mode, code, payload)
		}
		created := dataMap(t, payload)
		config, ok := created["normalRoutingConfig"].(map[string]any)
		if !ok || config["schedulingPreference"] != "speed_first" || config["firstByteDeadlineMs"] != float64(30000) {
			t.Fatalf("%s 响应渲染: %v", mode, created["normalRoutingConfig"])
		}
		speedFirst := config["speedFirstConfig"].(map[string]any)
		if speedFirst["slowTriggerCount"] != float64(4) || speedFirst["probeIntervalSeconds"] != float64(30) {
			t.Fatalf("%s speedFirstConfig: %v", mode, speedFirst)
		}
		strategyID := created["id"].(string)
		var configJSON sql.NullString
		if err := env.db.QueryRow(`SELECT config_json FROM route_strategies WHERE id = ?`, strategyID).Scan(&configJSON); err != nil {
			t.Fatal(err)
		}
		if !configJSON.Valid || !contains(configJSON.String, "speed_first") {
			t.Fatalf("%s config_json 必须落盘: %v", mode, configJSON)
		}

		// 同模式 cost_first：响应渲染默认对象，库中 config_json 为 NULL。
		code, payload = env.createStrategy(t, path,
			`{"name":"cf-`+mode+`","mode":"`+mode+`","groupBindings":[`+bindingsFor(mode)+`]}`)
		if code != http.StatusCreated {
			t.Fatalf("%s cost_first create: %d %v", mode, code, payload)
		}
		created = dataMap(t, payload)
		config, ok = created["normalRoutingConfig"].(map[string]any)
		if !ok || config["schedulingPreference"] != "cost_first" {
			t.Fatalf("%s cost_first 响应: %v", mode, created["normalRoutingConfig"])
		}
		if err := env.db.QueryRow(`SELECT config_json FROM route_strategies WHERE id = ?`, created["id"].(string)).Scan(&configJSON); err != nil {
			t.Fatal(err)
		}
		if configJSON.Valid {
			t.Fatalf("%s cost_first config_json 必须为 NULL: %v", mode, configJSON)
		}
	}
}

// TestWlSchedulePrefRuntimeCleanupAndEnrich：failover 的 cost_first →
// speed_first patch 触发 runtime cleanup；weighted speed_first 行获得列表
// runtime 富化与 speed-first-runtime 端点 enabled:true。
func TestWlSchedulePrefRuntimeCleanupAndEnrich(t *testing.T) {
	facade := &fakeSpeedFirstFacade{available: true}
	env := newTestEnvFull(t, facade)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	groupB := env.createGroup(t, adminID, "bravo", true)
	path := "/__aisys__/api/route-strategies"

	// failover 需要主备两个启用分组。
	code, created := env.createStrategy(t, path,
		`{"name":"fo","mode":"failover","groupBindings":[`+bindingJSON(groupA, 0)+`,`+bindingJSON(groupB, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("failover create: %d %v", code, created)
	}
	failoverID := dataMap(t, created)["id"].(string)

	// cost_first → speed_first 的 patch 触发 cleanup（参照 bug0164 断言方式）。
	updatedAt := env.strategyUpdatedAt(t, failoverID)
	code, patched := env.do(t, http.MethodPatch, path+"/"+failoverID,
		`{"expectedUpdatedAt":"`+updatedAt+`","normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":30000}}`)
	if code != http.StatusOK {
		t.Fatalf("speed_first patch: %d %v", code, patched)
	}
	facade.mu.Lock()
	cleared := append([]string{}, facade.cleared...)
	facade.mu.Unlock()
	if len(cleared) != 1 || cleared[0] != failoverID {
		t.Fatalf("normalRoutingConfig patch 必须清理 runtime: %v", cleared)
	}

	// weighted speed_first：列表富化 + speed-first-runtime 端点 enabled。
	code, created = env.createStrategy(t, path,
		`{"name":"wsf","mode":"weighted","normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":20000},"groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("weighted speed_first create: %d %v", code, created)
	}
	weightedID := dataMap(t, created)["id"].(string)
	facade.mu.Lock()
	facade.items = []SpeedFirstRuntimeItem{{
		AccountID:     "acc-1",
		Scope:         runtimeScope{RouteStrategyID: weightedID, GroupID: groupA},
		SlowCount:     3,
		DegradedUntil: "2026-01-01T00:05:00.000Z",
		Reason:        "slow_first_byte",
	}}
	facade.mu.Unlock()

	code, listPayload := env.do(t, http.MethodGet, path+"?pageSize=50", "")
	if code != 200 {
		t.Fatalf("list: %d %v", code, listPayload)
	}
	for _, raw := range items(t, listPayload) {
		entry := raw.(map[string]any)
		if entry["id"] != weightedID {
			continue
		}
		summary, ok := entry["speedFirstLatencyRuntime"].(map[string]any)
		if !ok || summary["runtimeAvailable"] != true || summary["degradedCount"] != float64(1) {
			t.Fatalf("weighted speed_first 列表富化: %v", entry["speedFirstLatencyRuntime"])
		}
	}

	code, runtimePayload := env.do(t, http.MethodGet, path+"/"+weightedID+"/speed-first-runtime", "")
	if code != 200 {
		t.Fatalf("weighted runtime: %d %v", code, runtimePayload)
	}
	runtime := dataMap(t, runtimePayload)
	if runtime["enabled"] != true || runtime["runtimeAvailable"] != true || runtime["degradedCount"] != float64(1) {
		t.Fatalf("weighted speed-first-runtime: %v", runtime)
	}

	// weighted speed_first → cost_first 的 patch 同样触发 cleanup。
	updatedAt = env.strategyUpdatedAt(t, weightedID)
	code, patched = env.do(t, http.MethodPatch, path+"/"+weightedID,
		`{"expectedUpdatedAt":"`+updatedAt+`","normalRoutingConfig":{"schedulingPreference":"cost_first"}}`)
	if code != http.StatusOK {
		t.Fatalf("cost_first patch: %d %v", code, patched)
	}
	facade.mu.Lock()
	cleared = append([]string{}, facade.cleared...)
	facade.mu.Unlock()
	if len(cleared) != 2 || cleared[1] != weightedID {
		t.Fatalf("改回 cost_first 必须再次清理 runtime: %v", cleared)
	}
}

// TestWlSchedulePrefPatchIdenticalConfigNoOp：三种新模式行 patch 提交与存量
// 相同的 speed_first 配置 → changedFields 为空、config_json 逐字节不变、
// updated_at 不变（与 normal 行为对称；修复前 currentJSON 被渲染成 NULL，
// 会追加逐字节相同的 config_json 赋值并轮换 updated_at）。
func TestWlSchedulePrefPatchIdenticalConfigNoOp(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	groupB := env.createGroup(t, adminID, "bravo", true)
	path := "/__aisys__/api/route-strategies"

	// failover 需要主备两个启用分组，其余模式单绑定即可。
	bindingsFor := func(mode string) string {
		if mode == ModeFailover {
			return bindingJSON(groupA, 0) + "," + bindingJSON(groupB, 0)
		}
		return bindingJSON(groupA, 0)
	}
	speedConfig := `{"schedulingPreference":"speed_first","firstByteDeadlineMs":35000,"speedFirstConfig":{"slowTriggerCount":4}}`
	for _, mode := range []string{ModeWeighted, ModeFailover, ModeRoundRobin} {
		t.Run(mode, func(t *testing.T) {
			code, created := env.createStrategy(t, path,
				`{"name":"noop-`+mode+`","mode":"`+mode+`","normalRoutingConfig":`+speedConfig+`,"groupBindings":[`+bindingsFor(mode)+`]}`)
			if code != http.StatusCreated {
				t.Fatalf("%s create: %d %v", mode, code, created)
			}
			strategyID := dataMap(t, created)["id"].(string)

			var beforeJSON sql.NullString
			if err := env.db.QueryRow(`SELECT config_json FROM route_strategies WHERE id = ?`, strategyID).Scan(&beforeJSON); err != nil {
				t.Fatal(err)
			}
			if !beforeJSON.Valid || beforeJSON.String == "" {
				t.Fatalf("%s speed_first 创建后 config_json 必须落盘", mode)
			}
			beforeUpdatedAt := env.strategyUpdatedAt(t, strategyID)

			// PATCH 只带 expectedUpdatedAt 与完全相同的 normalRoutingConfig。
			code, patched := env.do(t, http.MethodPatch, path+"/"+strategyID,
				`{"expectedUpdatedAt":"`+beforeUpdatedAt+`","normalRoutingConfig":`+speedConfig+`}`)
			if code != http.StatusOK {
				t.Fatalf("%s identical patch: %d %v", mode, code, patched)
			}
			changed := changedSet(t, patched)
			if changed["normalRoutingConfig"] || len(changed) != 0 {
				t.Fatalf("%s 零变更 patch 不得产生 changedFields: %v", mode, changed)
			}

			var afterJSON sql.NullString
			if err := env.db.QueryRow(`SELECT config_json FROM route_strategies WHERE id = ?`, strategyID).Scan(&afterJSON); err != nil {
				t.Fatal(err)
			}
			if afterJSON.Valid != beforeJSON.Valid || afterJSON.String != beforeJSON.String {
				t.Fatalf("%s config_json 必须逐字节不变: before=%s after=%v", mode, beforeJSON.String, afterJSON)
			}
			if after := env.strategyUpdatedAt(t, strategyID); after != beforeUpdatedAt {
				t.Fatalf("%s updated_at 必须不变: before=%s after=%s", mode, beforeUpdatedAt, after)
			}
		})
	}
}
