package accounts

// 时间计划闸门与账户状态解耦（docs/functions/时间计划闸门与账户状态解耦设计.md）
// 的 gateway 写面/读面回归：
//   - 创建/PATCH/批量编辑只重算派生闸门 availability_schedule_gate，不改写
//     status（§4.2）；
//   - 人工恢复（clearFailureState / ForceActivatePending）时段外恢复为
//     active + gate=0（§4.2）；
//   - 列表派生子状态 schedule_out_of_window、presentation 词汇与筛选归组
//     （§5.4；2026-11-07 修正：可调度筛选包含时段外，out_of_window 筛选值
//     退役）。

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// scheduleGateClosedSchedule 是任何执行时刻都判定为时段外的计划（dateRange
// 整体在过去），对测试运行时钟鲁棒。
const scheduleGateClosedSchedule = `{"enabled":true,"timezone":"UTC","mode":"allow_windows",` +
	`"windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"00:00","end":"23:59"}],` +
	`"dateRange":{"startDate":"2000-01-01","endDate":"2000-01-02"}}`

// withSchedule 在既有创建载荷末尾追加 availabilitySchedule 键。
func withSchedule(payload, schedule string) string {
	return strings.TrimSuffix(payload, "}") + `,"availabilitySchedule":` + schedule + `}`
}

func TestScheduleGateValueDerivation(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if gate := ScheduleGateValue(nil, now); gate.Valid {
		t.Fatalf("无计划必须为 NULL：%+v", gate)
	}
	if gate := ScheduleGateValue(&AvailabilitySchedule{Enabled: false}, now); gate.Valid {
		t.Fatalf("未启用计划必须为 NULL：%+v", gate)
	}
	open, err := NormalizeSchedule(map[string]any{
		"enabled": true, "timezone": "UTC", "mode": "allow_windows",
		"windows": []any{map[string]any{
			"daysOfWeek": []any{float64(1), float64(2), float64(3), float64(4), float64(5), float64(6), float64(7)},
			"start":      "00:00", "end": "23:59",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gate := ScheduleGateValue(open, now); !gate.Valid || gate.Int64 != 1 {
		t.Fatalf("时段内必须为 1：%+v", gate)
	}
	closed, err := NormalizeSchedule(map[string]any{
		"enabled": true, "timezone": "UTC", "mode": "allow_windows",
		"windows":   []any{map[string]any{"daysOfWeek": []any{float64(1)}, "start": "00:00", "end": "23:59"}},
		"dateRange": map[string]any{"startDate": "2000-01-01", "endDate": "2000-01-02"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gate := ScheduleGateValue(closed, now); !gate.Valid || gate.Int64 != 0 {
		t.Fatalf("时段外必须为 0：%+v", gate)
	}
}

// TestScheduleGateCreateAndPatchDoNotRewriteStatus：创建与 PATCH 的计划变化
// 只落闸门，status 保持用户选择（设计 §4.2）。
func TestScheduleGateCreateAndPatchDoNotRewriteStatus(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	// 创建：时段外计划 + 显式 active → active + gate=0。
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		withSchedule(createPayload("闸门创建-时段外"), scheduleGateClosedSchedule))
	if code != http.StatusCreated {
		t.Fatalf("create closed schedule: %d %v", code, created)
	}
	closedID := dataMap(t, created)["id"].(string)
	if row := env.queryCell(t, `SELECT status||'|'||availability_schedule_gate FROM accounts WHERE id = ?`, closedID); row != "active|0" {
		t.Fatalf("创建时段外必须 active|gate=0：%s", row)
	}

	// 创建：时段内计划 → active + gate=1。
	code, created = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		withSchedule(createPayload("闸门创建-时段内"), alwaysAllowSchedule))
	if code != http.StatusCreated {
		t.Fatalf("create open schedule: %d %v", code, created)
	}
	openID := dataMap(t, created)["id"].(string)
	if row := env.queryCell(t, `SELECT status||'|'||availability_schedule_gate FROM accounts WHERE id = ?`, openID); row != "active|1" {
		t.Fatalf("创建时段内必须 active|gate=1：%s", row)
	}

	// PATCH：时段内 → 时段外，只写 availabilitySchedule 与闸门，status 不变。
	code, patched := w2PatchBody(t, env, closedID, `"availabilitySchedule":`+alwaysAllowSchedule)
	if code != http.StatusOK {
		t.Fatalf("patch open schedule: %d %v", code, patched)
	}
	if row := env.queryCell(t, `SELECT status||'|'||availability_schedule_gate FROM accounts WHERE id = ?`, closedID); row != "active|1" {
		t.Fatalf("PATCH 时段内必须 active|gate=1：%s", row)
	}
	code, patched = w2PatchBody(t, env, openID, `"availabilitySchedule":`+scheduleGateClosedSchedule)
	if code != http.StatusOK {
		t.Fatalf("patch closed schedule: %d %v", code, patched)
	}
	changed := changedFieldSet(t, patched)
	if !changed["availabilitySchedule"] || changed["status"] {
		t.Fatalf("计划变化不得改写 status：%v", changed)
	}
	if row := env.queryCell(t, `SELECT status||'|'||availability_schedule_gate FROM accounts WHERE id = ?`, openID); row != "active|0" {
		t.Fatalf("PATCH 时段外必须 active|gate=0：%s", row)
	}

	// PATCH：计划清除 → status 保持 active，闸门回 NULL（恒开）。
	code, patched = w2PatchBody(t, env, openID, `"availabilitySchedule":null`)
	if code != http.StatusOK {
		t.Fatalf("patch clear schedule: %d %v", code, patched)
	}
	if row := env.queryCell(t, `SELECT status||'|'||COALESCE(availability_schedule_gate::text,'NULL') FROM accounts WHERE id = ?`, openID); row != "active|NULL" {
		t.Fatalf("清除计划必须 active|NULL：%s", row)
	}

	// 手动停用持久：时段外的 active 行被人工停用后保持 disabled（下一个窗口
	// 开始也不会被计划复活——计划不再写 status）。
	code, patched = w2PatchBody(t, env, closedID, `"status":"disabled"`)
	if code != http.StatusOK {
		t.Fatalf("patch status disabled: %d %v", code, patched)
	}
	if row := env.queryCell(t, `SELECT status||'|'||availability_schedule_gate FROM accounts WHERE id = ?`, closedID); row != "disabled|1" {
		t.Fatalf("人工停用必须保持 disabled（闸门只表达计划事实）：%s", row)
	}
}

// TestScheduleGateBatchAndForceActivate：批量编辑走同口径（计划只写闸门），
// 人工确认恢复（ForceActivatePending）在时段外落 active + gate=0。
func TestScheduleGateBatchAndForceActivate(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	// 批量编辑接受至少两个账户（单账户 = 批量编辑账户不能重复）。
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("闸门批量甲"))
	if code != http.StatusCreated {
		t.Fatalf("create 1: %d %v", code, created)
	}
	batchID := dataMap(t, created)["id"].(string)
	code, created = env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("闸门批量乙"))
	if code != http.StatusCreated {
		t.Fatalf("create 2: %d %v", code, created)
	}
	batchID2 := dataMap(t, created)["id"].(string)
	body := `{"targets":[{"accountId":"` + batchID + `","configRevision":1},` +
		`{"accountId":"` + batchID2 + `","configRevision":1}],"updates":{` +
		`"availabilitySchedule":{"enabled":true,"value":` + scheduleGateClosedSchedule + `}}}`
	code, updated := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update", body)
	if code != http.StatusOK {
		t.Fatalf("batch-update: %d %v", code, updated)
	}
	for _, id := range []string{batchID, batchID2} {
		if row := env.queryCell(t, `SELECT status||'|'||availability_schedule_gate FROM accounts WHERE id = ?`, id); row != "active|0" {
			t.Fatalf("批量计划变化必须 active|gate=0（%s）：%s", id, row)
		}
	}

	// ForceActivatePending：时段外计划 + pending_test → 人工确认恢复为
	// active + gate=0（设计 §4.2「正常 · 非时间段」）。
	env.seedAccount(t, "acc-gate-force", adminID, "闸门恢复", "pending_test")
	env.exec(t, `UPDATE accounts SET availability_schedule_json = ? WHERE id = ?`,
		scheduleGateClosedSchedule, "acc-gate-force")
	out, err := env.store.ForceActivatePending(context.Background(), "acc-gate-force",
		AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || out == nil || !out.Changed || out.Account == nil {
		t.Fatalf("force activate: %+v %v", out, err)
	}
	if out.Account.Status != "active" {
		t.Fatalf("人工恢复必须 active：%+v", out.Account)
	}
	if row := env.queryCell(t, `SELECT status||'|'||availability_schedule_gate FROM accounts WHERE id = ?`, "acc-gate-force"); row != "active|0" {
		t.Fatalf("时段外人工恢复必须 active|gate=0：%s", row)
	}
}

// TestScheduleGateListAvailabilityAndFilters：列表派生子状态、presentation
// 词汇与筛选归组（设计 §5.4；2026-11-07 修正：可调度包含时段外，
// out_of_window 筛选值退役）。
func TestScheduleGateListAvailabilityAndFilters(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-gate-own", adminID, "闸门时段外", "active")
	env.exec(t, `UPDATE accounts SET availability_schedule_json = ?, availability_schedule_next_check_at = ?,
		availability_schedule_gate = 0 WHERE id = ?`,
		scheduleGateClosedSchedule, "2026-11-04T00:00:00.000Z", "acc-gate-own")
	// 对照行：无计划的正常 active（gate NULL、schedulable 默认 1），用于区分
	// 「可调度筛选包含时段外」与「只剩时段外子集」。
	env.seedAccount(t, "acc-gate-normal", adminID, "闸门正常", "active")

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts?page=1&pageSize=20", "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, payload)
	}
	item := findListItemByName(t, payload, "闸门时段外")
	if gate, ok := item["availabilityScheduleGate"].(float64); !ok || gate != 0 {
		t.Fatalf("列表必须回显 availabilityScheduleGate=0：%v", item["availabilityScheduleGate"])
	}
	availability := item["effectiveAvailability"].(map[string]any)
	if availability["status"] != "schedule_out_of_window" || availability["label"] != "非时间段" ||
		availability["available"] != false || availability["blockerScope"] != "schedule" {
		t.Fatalf("effectiveAvailability 非时间段分支不一致：%v", availability)
	}
	if retryAt, _ := availability["retryAt"].(string); retryAt != "2026-11-04T00:00:00.000Z" {
		t.Fatalf("retryAt 必须取下一个计划边界：%v", availability["retryAt"])
	}
	presentation := item["availabilityPresentation"].(map[string]any)
	if presentation["status"] != "out_of_window" || presentation["action"] != "none" {
		t.Fatalf("presentation 映射不一致：%v", presentation)
	}
	boundary, _ := presentation["statusBoundary"].(map[string]any)
	if boundary == nil || boundary["kind"] != "schedule_window_open" || boundary["at"] != "2026-11-04T00:00:00.000Z" {
		t.Fatalf("statusBoundary 必须为 schedule_window_open：%v", presentation["statusBoundary"])
	}

	// 筛选归组（2026-11-07 裁决 §5.4 修正）：可调度（enabled）包含时段外行；
	// out_of_window 筛选值退役，请求归一为 all 不过滤；disabled 仍不命中
	// gate=0 actives；status=active 按「正常状态」语义展开命中全部正常行
	// （词表展开为 active + schedule_out_of_window）。
	if n := listItemCountForQuery(t, env, "schedulable=enabled"); n != 2 {
		t.Fatalf("schedulable=enabled 应命中 2 条（gate NULL + gate=0），得到 %d", n)
	}
	if n := listItemCountForQuery(t, env, "schedulable=out_of_window"); n != 2 {
		t.Fatalf("schedulable=out_of_window 已退役，应按不过滤处理命中 2 条，得到 %d", n)
	}
	if n := listItemCountForQuery(t, env, "schedulable=disabled"); n != 0 {
		t.Fatalf("schedulable=disabled 不得命中 gate=0 actives，得到 %d", n)
	}
	if n := listItemCountForQuery(t, env, "status=active"); n != 2 {
		t.Fatalf("status=active 应按正常状态语义命中 2 条（含时段外），得到 %d", n)
	}
	if n := listItemCountForQuery(t, env, "status=pending_test"); n != 0 {
		t.Fatalf("status=pending_test 不得命中正常/时段外行，得到 %d", n)
	}
	if n := listItemCountForQuery(t, env, "status=disabled"); n != 0 {
		t.Fatalf("status=disabled 应命中 0 条，得到 %d", n)
	}
	// owner options 完整条件同口径：enabled 命中两条（schedulable=1 的
	// gate=0 与 gate NULL）。
	code, optionsPayload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/options?schedulable=enabled", "")
	if code != http.StatusOK {
		t.Fatalf("options enabled: %d %v", code, optionsPayload)
	}
	if n := len(dataArray(t, optionsPayload)); n != 2 {
		t.Fatalf("options schedulable=enabled 应命中 2 条（含时段外），得到 %d", n)
	}
	// owner 臂 status 词表同样展开：status=active 命中正常 + 时段外两条
	// （null 掉本断言即失去对 list.go owner 臂展开的覆盖）。
	code, optionsStatusPayload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/options?status=active", "")
	if code != http.StatusOK {
		t.Fatalf("options status=active: %d %v", code, optionsStatusPayload)
	}
	if n := len(dataArray(t, optionsStatusPayload)); n != 2 {
		t.Fatalf("options status=active 应命中 2 条（含时段外），得到 %d", n)
	}
}

func listItemCountForQuery(t *testing.T, env *testEnv, query string) int {
	t.Helper()
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts?page=1&pageSize=50&"+query, "")
	if code != http.StatusOK {
		t.Fatalf("list %s: %d %v", query, code, payload)
	}
	return len(listItems(t, payload))
}

func findListItemByName(t *testing.T, payload map[string]any, name string) map[string]any {
	t.Helper()
	for _, item := range listItems(t, payload) {
		if item["name"] == name {
			return item
		}
	}
	t.Fatalf("列表缺少账户 %q：%v", name, payload)
	return nil
}
