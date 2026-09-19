package routestrategies

// w11g 覆盖补充（第五批）：Create/Patch 事务与绑定写链错误臂、配置规范化
// 枚举分支与构造守卫。

import (
	"context"
	"database/sql"
	"testing"
)

func TestW11GCreateWriteErrorArms(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w11g-cw", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w11g-cw-group", true)
	store := w9eStore(t, env)
	ctx := context.Background()
	viewer := AccessScope{ViewerID: admin}
	input := MutationInput{
		Name: ptrString("w11g 写链"), HasBindings: true,
		Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
	}
	// 非法 status（store 层 normalizeStatus）。
	badStatus := MutationInput{
		Name: ptrString("w11g 写链"), HasBindings: true, Status: ptrString("bogus"),
		Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
	}
	if _, err := store.Create(ctx, badStatus, viewer); err == nil {
		t.Fatal("bogus status must fail")
	}
	// INSERT 失败：drop 主表（绑定规范化只查 groups，不受影响）。
	if _, err := env.db.Exec(`DROP TABLE route_strategies`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, input, viewer); err == nil {
		t.Fatal("insert error must propagate")
	}
	// BeginTx 失败：直接关闭当前库（此后本测试不再使用）。
	if err := env.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, input, viewer); err == nil {
		t.Fatal("begin error must propagate")
	}
	if _, err := store.Patch(ctx, "rs-w11g", MutationInput{Name: ptrString("x")}, "2026-09-01T00:00:00.000Z", viewer); err == nil {
		t.Fatal("patch begin error must propagate")
	}
}

func TestW11GPatchBindingWriteErrorArms(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w11g-pb", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w11g-pb-group", true)
	store := w9eStore(t, env)
	ctx := context.Background()
	viewer := AccessScope{ViewerID: admin}
	created, err := store.Create(ctx, MutationInput{
		Name: ptrString("w11g 绑定写"), HasBindings: true,
		Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
	}, viewer)
	if err != nil {
		t.Fatal(err)
	}
	// 他人视角：owner 过滤后不可见 → 404 语义 (nil, nil)。
	foreign := AccessScope{ViewerID: "sys-w11g-other"}
	foreignResult, err := store.Patch(ctx, created.ID, MutationInput{Name: ptrString("x")}, created.UpdatedAt, foreign)
	if err != nil || foreignResult != nil {
		t.Fatalf("foreign patch result=%v err=%v", foreignResult, err)
	}
	// 绑定读链失败：drop 绑定表后 patch 绑定集合。
	if _, err := env.db.Exec(`DROP TABLE route_strategy_groups`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Patch(ctx, created.ID, MutationInput{
		HasBindings: true,
		Bindings:    []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
	}, created.UpdatedAt, viewer); err == nil {
		t.Fatal("bindings load error must propagate")
	}
}

func TestW11GConfigNormalizeArms(t *testing.T) {
	// 调度偏好：合法枚举与非法值。
	if preference, err := normalizeSchedulingPreference(nil); err != nil || preference != "cost_first" {
		t.Fatalf("default preference=%s err=%v", preference, err)
	}
	if preference, err := normalizeSchedulingPreference("speed_first"); err != nil || preference != "speed_first" {
		t.Fatalf("speed preference=%s err=%v", preference, err)
	}
	if _, err := normalizeSchedulingPreference("bogus"); err == nil {
		t.Fatal("bogus preference must fail")
	}
	if _, err := normalizeSchedulingPreference(5); err == nil {
		t.Fatal("non-string preference must fail")
	}
	// speed_first 速度优先配置：deadline 双填冲突。
	if _, err := normalizeNormalRoutingConfig(map[string]any{
		"schedulingPreference": "speed_first",
		"firstByteDeadlineMs":  20000,
		"speedFirstConfig":     map[string]any{"firstByteThresholdMs": 20000},
	}); err == nil {
		t.Fatal("dual deadline must fail")
	}
	// speed_first 合法链路（deadline 走公共字段）。
	config, err := normalizeNormalRoutingConfig(map[string]any{
		"schedulingPreference": "speed_first",
		"firstByteDeadlineMs":  20000,
	})
	if err != nil || config.FirstByteDeadlineMs == nil || *config.FirstByteDeadlineMs != 20000 {
		t.Fatalf("speed config=%+v err=%v", config, err)
	}
	// parseStoredConfig：空/无效/合法 normal。
	if normal, err := parseStoredConfig(sql.NullString{}); err != nil || normal != nil {
		t.Fatalf("empty stored config=%v err=%v", normal, err)
	}
	if _, err := parseStoredConfig(sql.NullString{String: "not-json", Valid: true}); err == nil {
		t.Fatal("bad stored json must fail")
	}
	if _, err := parseStoredConfig(sql.NullString{String: `{"normalRoutingConfig":{"schedulingPreference":"bogus"}}`, Valid: true}); err == nil {
		t.Fatal("bad stored normal must fail")
	}
	normal, err := parseStoredConfig(sql.NullString{String: `{"normalRoutingConfig":{"schedulingPreference":"cost_first"}}`, Valid: true})
	if err != nil || normal == nil {
		t.Fatalf("stored normal=%v err=%v", normal, err)
	}
	// 构造守卫：NewStore nil db 与 itoa(0)。
	if _, err := NewStore(nil, false, nil, nil, nil); err == nil {
		t.Fatal("nil db must fail")
	}
	if got := itoa(0); got != "0" {
		t.Fatalf("itoa(0)=%s", got)
	}
}
