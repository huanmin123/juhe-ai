// 调度偏好通用化回归：全部四种模式共享 normalRoutingConfig（speed_first 落盘、
// cost_first 保持 config_json NULL）。
package routestrategies

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
)

func TestWlSchedulePrefModeSupportsPredicate(t *testing.T) {
	for _, mode := range []string{ModeNormal, ModeWeighted, ModeFailover, ModeRoundRobin} {
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

	for _, mode := range []string{ModeNormal, ModeWeighted, ModeFailover, ModeRoundRobin} {
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
	for _, mode := range []string{ModeNormal, ModeWeighted, ModeFailover, ModeRoundRobin} {
		if got := empty.normalInput(current); got == nil {
			t.Fatalf("mode %q 无新输入必须 feed-forward 当前配置", mode)
		}
	}
	withRaw := MutationInput{HasNormalConfig: true, NormalConfigRaw: map[string]any{"schedulingPreference": "speed_first"}}
	if got := withRaw.normalInput(current); got == nil {
		t.Fatal("带 HasNormalConfig 必须透传 raw")
	}
}

// TestWlSchedulePrefPatchFeedForward：存量行（store 直建，代表迁移完成世界
// 的旧数据/回滚 oracle）speed_first 配置在 mode 裸切时 feed-forward 保留
// （config_json 逐字节不变、detail 照常渲染）；HTTP payload 不携带调度字段，
// 不触发退场拒绝——存储 oracle 契约（D7 第 6 条：其他合法数据不可整列误删）。
func TestWlSchedulePrefPatchFeedForward(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	path := "/__aisys__/api/route-strategies"

	strategyID := env.createStrategyViaStore(t, adminID, "ff", ModeNormal, []string{groupA}, map[string]any{
		"schedulingPreference": "speed_first",
		"firstByteDeadlineMs":  25000,
	})

	var configJSON sql.NullString
	if err := env.db.QueryRow(fixtureBind(`SELECT config_json FROM route_strategies WHERE id = ?`), strategyID).Scan(&configJSON); err != nil {
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
	if err := env.db.QueryRow(fixtureBind(`SELECT config_json FROM route_strategies WHERE id = ?`), strategyID).Scan(&configJSON); err != nil {
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

	// 无关字段 PATCH（名称）同理逐字节保持存量调度键（oracle 钉住）。
	updatedAt = env.strategyUpdatedAt(t, strategyID)
	code, patched = env.do(t, http.MethodPatch, path+"/"+strategyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","name":"ff-renamed"}`)
	if code != http.StatusOK {
		t.Fatalf("name patch: %d %v", code, patched)
	}
	if err := env.db.QueryRow(fixtureBind(`SELECT config_json FROM route_strategies WHERE id = ?`), strategyID).Scan(&configJSON); err != nil {
		t.Fatal(err)
	}
	if !configJSON.Valid || configJSON.String != beforeJSON {
		t.Fatalf("无关字段 PATCH 不得改写 config_json: before=%s after=%v", beforeJSON, configJSON)
	}
}

// TestWlSchedulePrefHTTPModeMatrix：P2 四模式写入口一律拒绝旧调度字段；
// 不携带调度字段的 cost_first 创建臂保持原有行为（渲染默认对象、config_json
// NULL）。速度配置的落盘/回读语义由存储层测试覆盖（回滚 oracle）。
func TestWlSchedulePrefHTTPModeMatrix(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	groupB := env.createGroup(t, adminID, "bravo", true)
	path := "/__aisys__/api/route-strategies"

	// failover 需要两个启用分组，其余模式单绑定即可。
	bindingsFor := func(mode string) string {
		if mode == ModeFailover {
			return bindingJSON(groupA, 0) + "," + bindingJSON(groupB, 0)
		}
		return bindingJSON(groupA, 0)
	}
	speedBody := func(mode, name string) string {
		return `{"name":"` + name + `","mode":"` + mode + `","normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":30000,"speedFirstConfig":{"slowTriggerCount":4}},"groupBindings":[` + bindingsFor(mode) + `]}`
	}
	for _, mode := range []string{ModeNormal, ModeWeighted, ModeFailover, ModeRoundRobin} {
		code, payload := env.createStrategy(t, path, speedBody(mode, "sf-"+mode))
		if code != http.StatusBadRequest || payload["message"] != LegacySchedulingConfigRejectedMessage {
			t.Fatalf("%s speed_first create must be rejected: %d %v", mode, code, payload)
		}

		// cost_first（无 normalRoutingConfig）：照常创建，渲染默认对象，库中
		// config_json 为 NULL。
		code, payload = env.createStrategy(t, path,
			`{"name":"cf-`+mode+`","mode":"`+mode+`","groupBindings":[`+bindingsFor(mode)+`]}`)
		if code != http.StatusCreated {
			t.Fatalf("%s cost_first create: %d %v", mode, code, payload)
		}
		created := dataMap(t, payload)
		config, ok := created["normalRoutingConfig"].(map[string]any)
		if !ok || config["schedulingPreference"] != "cost_first" {
			t.Fatalf("%s cost_first 响应: %v", mode, created["normalRoutingConfig"])
		}
		var configJSON sql.NullString
		if err := env.db.QueryRow(fixtureBind(`SELECT config_json FROM route_strategies WHERE id = ?`), created["id"].(string)).Scan(&configJSON); err != nil {
			t.Fatal(err)
		}
		if configJSON.Valid {
			t.Fatalf("%s cost_first config_json 必须为 NULL: %v", mode, configJSON)
		}
	}

	// merge 已从路由模式退役：写入口统一返回 unsupported_route_mode（设计 §5）。
	code, payload := env.createStrategy(t, path,
		`{"name":"m-legacy","mode":"merge","groupBindings":[`+bindingJSON(groupA, 0)+`,`+bindingJSON(groupB, 0)+`]}`)
	if code != http.StatusBadRequest || payload["code"] != "unsupported_route_mode" {
		t.Fatalf("merge create must be unsupported: %d %v", code, payload)
	}
}

// TestWlSchedulePrefRuntimeCleanupAndEnrich：存量 speed_first 行（store 直建，
// 回滚 oracle 世界）获得列表 runtime 富化与 speed-first-runtime 端点
// enabled:true；normalRoutingConfig 输入（P2 唯一合法形态为 null/空对象）改写
// 配置时仍触发 runtime cleanup。
func TestWlSchedulePrefRuntimeCleanupAndEnrich(t *testing.T) {
	facade := &fakeSpeedFirstFacade{available: true}
	env := newTestEnvFull(t, facade)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	groupB := env.createGroup(t, adminID, "bravo", true)
	path := "/__aisys__/api/route-strategies"

	// 存量 speed_first 行：failover（主备两组）与 weighted 各一。
	failoverID := env.createStrategyViaStore(t, adminID, "fo", ModeFailover, []string{groupA, groupB}, map[string]any{
		"schedulingPreference": "speed_first",
		"firstByteDeadlineMs":  30000,
	})
	weightedID := env.createStrategyViaStore(t, adminID, "wsf", ModeWeighted, []string{groupA}, map[string]any{
		"schedulingPreference": "speed_first",
		"firstByteDeadlineMs":  20000,
	})
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

	// normalRoutingConfig 输入（null：归一为 cost_first）改写配置 → cleanup。
	updatedAt := env.strategyUpdatedAt(t, weightedID)
	code, patched := env.do(t, http.MethodPatch, path+"/"+weightedID,
		`{"expectedUpdatedAt":"`+updatedAt+`","normalRoutingConfig":null}`)
	if code != http.StatusOK {
		t.Fatalf("null config patch: %d %v", code, patched)
	}
	facade.mu.Lock()
	cleared := append([]string{}, facade.cleared...)
	facade.mu.Unlock()
	if len(cleared) != 1 || cleared[0] != weightedID {
		t.Fatalf("normalRoutingConfig patch 必须清理 runtime: %v", cleared)
	}

	// failover speed_first 行同样经 null 输入清配 → 再次 cleanup。
	updatedAt = env.strategyUpdatedAt(t, failoverID)
	code, patched = env.do(t, http.MethodPatch, path+"/"+failoverID,
		`{"expectedUpdatedAt":"`+updatedAt+`","normalRoutingConfig":{}}`)
	if code != http.StatusOK {
		t.Fatalf("empty object config patch: %d %v", code, patched)
	}
	facade.mu.Lock()
	cleared = append([]string{}, facade.cleared...)
	facade.mu.Unlock()
	if len(cleared) != 2 || cleared[1] != failoverID {
		t.Fatalf("清回 cost_first 必须再次清理 runtime: %v", cleared)
	}
}

// TestWlSchedulePrefPatchIdenticalConfigNoOp：三种新模式存量行（store 直建）
// 经 store patch 提交与存量相同的 speed_first 配置 → changedFields 为空、
// config_json 逐字节不变、updated_at 不变（与 normal 行为对称；修复前
// currentJSON 被渲染成 NULL，会追加逐字节相同的 config_json 赋值并轮换
// updated_at）。P2 起 HTTP 载荷不再接受调度字段，同值 no-op 语义留在存储层
// （回滚 oracle 契约）。
func TestWlSchedulePrefPatchIdenticalConfigNoOp(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	groupB := env.createGroup(t, adminID, "bravo", true)

	// failover 需要主备两个启用分组，其余模式单绑定即可。
	bindingsFor := func(mode string) []string {
		if mode == ModeFailover {
			return []string{groupA, groupB}
		}
		return []string{groupA}
	}
	speedRaw := func() map[string]any {
		return map[string]any{
			"schedulingPreference": "speed_first",
			"firstByteDeadlineMs":  35000,
			"speedFirstConfig":     map[string]any{"slowTriggerCount": 4},
		}
	}
	for _, mode := range []string{ModeWeighted, ModeFailover, ModeRoundRobin} {
		t.Run(mode, func(t *testing.T) {
			strategyID := env.createStrategyViaStore(t, adminID, "noop-"+mode, mode, bindingsFor(mode), speedRaw())

			var beforeJSON sql.NullString
			if err := env.db.QueryRow(fixtureBind(`SELECT config_json FROM route_strategies WHERE id = ?`), strategyID).Scan(&beforeJSON); err != nil {
				t.Fatal(err)
			}
			if !beforeJSON.Valid || beforeJSON.String == "" {
				t.Fatalf("%s speed_first 创建后 config_json 必须落盘", mode)
			}
			beforeUpdatedAt := env.strategyUpdatedAt(t, strategyID)

			// store patch 提交完全相同的 normalRoutingConfig。
			result, err := env.store.Patch(context.Background(), strategyID,
				MutationInput{HasNormalConfig: true, NormalConfigRaw: speedRaw()}, beforeUpdatedAt,
				AccessScope{ViewerID: adminID, IsAdmin: true})
			if err != nil {
				t.Fatalf("%s identical patch: %v", mode, err)
			}
			if len(result.ChangedFields) != 0 {
				t.Fatalf("%s 零变更 patch 不得产生 changedFields: %v", mode, result.ChangedFields)
			}

			var afterJSON sql.NullString
			if err := env.db.QueryRow(fixtureBind(`SELECT config_json FROM route_strategies WHERE id = ?`), strategyID).Scan(&afterJSON); err != nil {
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
