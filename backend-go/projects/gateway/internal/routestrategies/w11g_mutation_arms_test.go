package routestrategies

// w11g 覆盖补充：Create/Patch/Delete 的校验与冲突分支、配置重算输入选择、
// 版本时间戳与 Delete 守卫的 store 级直测。

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestW11GCreateValidationArms(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w11g-cv", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w11g-cv-group", true)
	store := w9eStore(t, env)
	ctx := context.Background()
	viewer := AccessScope{ViewerID: admin}

	mk := func(mods ...func(*MutationInput)) MutationInput {
		input := MutationInput{
			Name:        ptrString("w11g 策略"),
			HasBindings: true,
			Bindings:    []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
		}
		for _, mod := range mods {
			mod(&input)
		}
		return input
	}
	// 无 viewer 上下文。
	if _, err := store.Create(ctx, mk(), AccessScope{}); err == nil {
		t.Fatal("missing viewer must fail")
	}
	// 非法 mode。
	bogusMode := "bogus"
	if _, err := store.Create(ctx, mk(func(i *MutationInput) { i.Mode = &bogusMode }), viewer); err == nil {
		t.Fatal("bogus mode must fail")
	}
	// 非法 normal 配置（非对象输入）。
	if _, err := store.Create(ctx, mk(func(i *MutationInput) {
		i.Mode = ptrString(ModeNormal)
		i.HasNormalConfig = true
		i.NormalConfigRaw = "not-a-map"
	}), viewer); err == nil {
		t.Fatal("bogus normal config must fail")
	}
	// 非法 hybrid 配置。
	if _, err := store.Create(ctx, mk(func(i *MutationInput) {
		i.Mode = ptrString(ModeHybridSmart)
		i.HasHybridConfig = true
		i.HybridConfigRaw = map[string]any{"tiers": "not-array"}
	}), viewer); err == nil {
		t.Fatal("bogus hybrid config must fail")
	}
	// 描述超长（>200 UTF-16 单位）。
	longDesc := strings.Repeat("说", 201)
	if _, err := store.Create(ctx, mk(func(i *MutationInput) {
		i.HasDescription = true
		i.Description = &longDesc
	}), viewer); err == nil {
		t.Fatal("long description must fail")
	}
	// 空白描述归一化为 NULL。
	blank := "   "
	if _, err := store.Create(ctx, mk(func(i *MutationInput) {
		i.HasDescription = true
		i.Description = &blank
	}), viewer); err != nil {
		t.Fatalf("blank description err=%v", err)
	}
}

func TestW11GPatchValidationArms(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w11g-pv", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w11g-pv-group", true)
	group2 := env.createGroup(t, admin, "w11g-pv-group2", true)
	store := w9eStore(t, env)
	ctx := context.Background()
	viewer := AccessScope{ViewerID: admin}

	created, err := store.Create(ctx, MutationInput{
		Name: ptrString("w11g 待改"), HasBindings: true,
		Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
	}, viewer)
	if err != nil {
		t.Fatal(err)
	}
	at := created.UpdatedAt
	patch := func(mods ...func(*MutationInput)) (*PatchResult, error) {
		input := MutationInput{}
		for _, mod := range mods {
			mod(&input)
		}
		return store.Patch(ctx, created.ID, input, at, viewer)
	}
	// 空名。
	if _, err := patch(func(i *MutationInput) { i.Name = ptrString("  ") }); err == nil {
		t.Fatal("blank name must fail")
	}
	// 非法 status。
	if _, err := patch(func(i *MutationInput) { i.Status = ptrString("bogus") }); err == nil {
		t.Fatal("bogus status must fail")
	}
	// 非法 mode。
	if _, err := patch(func(i *MutationInput) { i.Mode = ptrString("bogus") }); err == nil {
		t.Fatal("bogus mode must fail")
	}
	// 非法描述。
	longDesc := strings.Repeat("说", 201)
	if _, err := patch(func(i *MutationInput) { i.HasDescription = true; i.Description = &longDesc }); err == nil {
		t.Fatal("long description must fail")
	}
	// 非法 normal 配置（recompute，非对象输入）。
	if _, err := patch(func(i *MutationInput) {
		i.HasNormalConfig = true
		i.NormalConfigRaw = "not-a-map"
	}); err == nil {
		t.Fatal("bogus normal config must fail")
	}
	// 非法绑定（未知分组）。
	if _, err := patch(func(i *MutationInput) {
		i.HasBindings = true
		i.Bindings = []BindingInput{{GroupID: "grp-w11g-ghost", Priority: intPtr(1), Status: "active"}}
	}); err == nil {
		t.Fatal("unknown group binding must fail")
	}
	// 混合模式绑定校验（bare mode switch 下重复优先级）。
	if _, err := patch(func(i *MutationInput) { i.Mode = ptrString(ModeHybridSmart) }); err == nil {
		t.Fatal("bare mode switch with invalid bindings must fail")
	}
	// 版本时间戳非法。
	if _, err := store.Patch(ctx, created.ID, MutationInput{Name: ptrString("x")}, "not-a-time", viewer); err == nil {
		t.Fatal("bad expectedUpdatedAt must fail")
	}
	// 版本冲突：过期 expected。
	_, err = patch(func(i *MutationInput) { i.Name = ptrString("w11g 改名") })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := patch(func(i *MutationInput) { i.Name = ptrString("再改") }); err == nil {
		var conflict *VersionConflictError
		if !asVersionConflict(err, &conflict) {
			t.Fatalf("stale patch err=%v", err)
		}
	}
	// 不存在：404 语义 (nil, nil)。
	missing, err := store.Patch(ctx, "route_strategy_w11g_ghost", MutationInput{Name: ptrString("x")}, at, viewer)
	if err != nil || missing != nil {
		t.Fatalf("missing patch=%v err=%v", missing, err)
	}
	_ = group2
}

func asVersionConflict(err error, target **VersionConflictError) bool {
	if c, ok := err.(*VersionConflictError); ok {
		*target = c
		return true
	}
	return false
}

func TestW11GPatchConfigRecomputeAndModeSwitch(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w11g-pr", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w11g-pr-group", true)
	store := w9eStore(t, env)
	ctx := context.Background()
	viewer := AccessScope{ViewerID: admin}
	created, err := store.Create(ctx, MutationInput{
		Name: ptrString("w11g 配置"), Mode: ptrString(ModeNormal), HasBindings: true,
		Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
	}, viewer)
	if err != nil {
		t.Fatal(err)
	}
	// 配置重算：mode 切到 hybrid 后 normal 输入仍取 raw。
	result, err := store.Patch(ctx, created.ID, MutationInput{
		Mode:             ptrString(ModeHybridSmart),
		HasHybridConfig:  true,
		HybridConfigRaw:  map[string]any{"tiers": []any{map[string]any{"name": "w11g-t", "weight": 1, "groupId": group}}},
		HasNormalConfig:  true,
		NormalConfigRaw:  map[string]any{"unknownField": 1},
	}, created.UpdatedAt, viewer)
	if err == nil {
		t.Fatalf("recompute with unknown normal field must fail: %+v", result)
	}
	// 纯函数输入选择。
	input := MutationInput{HasNormalConfig: true, NormalConfigRaw: map[string]any{"k": 1}}
	if got := input.normalInput(ModeHybridSmart, nil); got == nil {
		t.Fatal("hybrid mode keeps normal raw")
	}
	input = MutationInput{}
	if got := input.normalInput(ModeHybridSmart, nil); got != nil {
		t.Fatalf("hybrid mode nil raw = %v", got)
	}
	if got := input.hybridInput(ModeNormal, nil); got != nil {
		t.Fatalf("normal mode nil raw = %v", got)
	}
	config := &NormalRoutingConfig{}
	encoded := input.normalInput(ModeNormal, config)
	if encoded == nil {
		t.Fatal("normal mode feeds typed current")
	}
	if got := typedToRaw(nil); got != nil {
		t.Fatalf("typedToRaw nil = %v", got)
	}
	if got := rawForMode(ModeHybridSmart, ModeNormal, map[string]any{"k": 1}); got != nil {
		t.Fatalf("rawForMode mismatch = %v", got)
	}
	// 存量 JSON 规范化错误。
	if _, err := routeStrategyConfigJSONFromRaw("not-a-map", nil); err == nil {
		t.Fatal("bad stored normal json must fail")
	}
	if _, err := routeStrategyConfigJSONFromRaw(nil, "not-a-map"); err == nil {
		t.Fatal("bad stored hybrid json must fail")
	}
	// nextStrategyUpdatedAt：非法格式。
	if _, err := nextStrategyUpdatedAt("bad", time.Now()); err == nil {
		t.Fatal("bad current timestamp must fail")
	}
}

func TestW11GDeleteGuardArms(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w11g-dl", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w11g-dl-group", true)
	store := w9eStore(t, env)
	ctx := context.Background()
	viewer := AccessScope{ViewerID: admin}
	created, err := store.Create(ctx, MutationInput{
		Name: ptrString("w11g 删除"), HasBindings: true,
		Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
	}, viewer)
	if err != nil {
		t.Fatal(err)
	}
	// 他人不可删：返回空回执。
	other := AccessScope{ViewerID: "sys-w11g-other"}
	receipt, err := store.Delete(ctx, created.ID, other)
	if err != nil || receipt.Deleted {
		t.Fatalf("foreign delete receipt=%+v err=%v", receipt, err)
	}
	// 不存在：空回执。
	receipt, err = store.Delete(ctx, "route_strategy_w11g_ghost", viewer)
	if err != nil || receipt.Deleted {
		t.Fatalf("missing delete receipt=%+v err=%v", receipt, err)
	}
	// 默认策略不可删。
	env.exec(t, `UPDATE route_strategies SET is_default = 1 WHERE id = '`+created.ID+`'`)
	if _, err := store.Delete(ctx, created.ID, viewer); err == nil {
		t.Fatal("default strategy delete must fail")
	}
	// 被 API Key 引用不可删。
	env.exec(t, `UPDATE route_strategies SET is_default = 0 WHERE id = '`+created.ID+`'`)
	env.exec(t, `INSERT INTO api_keys (id, name, system_account_id, route_strategy_id, created_at, updated_at) VALUES ('w11g-key', 'w11g key', '`+admin+`', '`+created.ID+`', '2026-09-01T00:00:00.000Z', '2026-09-01T00:00:00.000Z')`)
	if _, err := store.Delete(ctx, created.ID, viewer); err == nil {
		t.Fatal("referenced strategy delete must fail")
	}
	// 解绑后成功。
	env.exec(t, `DELETE FROM api_keys WHERE id = 'w11g-key'`)
	receipt, err = store.Delete(ctx, created.ID, viewer)
	if err != nil || !receipt.Deleted {
		t.Fatalf("delete receipt=%+v err=%v", receipt, err)
	}
	// 查询错误臂。
	if _, err := env.db.Exec(`DROP TABLE route_strategies`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Delete(ctx, "route_strategy_w11g_x", viewer); err == nil {
		t.Fatal("lookup error must propagate")
	}
}
