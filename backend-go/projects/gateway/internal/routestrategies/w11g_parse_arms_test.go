package routestrategies

// w11g 覆盖补充（第三批）：请求体解析与配置规范化的纯函数分支、绑定行
// 完整性守卫与 normalizeBindings 的重复/越界臂。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestW11GParseMutationFieldArms(t *testing.T) {
	// parseMutationFields 各失败分支（create 必填名 / patch 严格字段）。
	cases := []struct {
		name       string
		body       map[string]any
		requireNew bool
	}{
		{"nameType", map[string]any{"name": 5}, true},
		{"nameBlank", map[string]any{"name": "  "}, true},
		{"nameMissing", map[string]any{}, true},
		{"patchNameBlank", map[string]any{"name": " "}, false},
		{"descType", map[string]any{"name": "n", "description": 5}, true},
		{"descLong", map[string]any{"name": "n", "description": strings.Repeat("说", 201)}, true},
		{"modeNull", map[string]any{"name": "n", "mode": nil}, true},
		{"modeType", map[string]any{"name": "n", "mode": 7}, true},
		{"modeValue", map[string]any{"name": "n", "mode": "bogus"}, true},
		{"statusNull", map[string]any{"name": "n", "status": nil}, true},
		{"statusValue", map[string]any{"name": "n", "status": "bogus"}, true},
		{"bindingsNull", map[string]any{"name": "n", "groupBindings": nil}, true},
		{"bindingsType", map[string]any{"name": "n", "groupBindings": "bad"}, true},
		{"bindingsEmpty", map[string]any{"name": "n", "groupBindings": []any{}}, true},
		{"bindingsOver", map[string]any{"name": "n", "groupBindings": make([]any, maxRouteStrategyGroupBindings+1)}, true},
		{"bindingBad", map[string]any{"name": "n", "groupBindings": []any{map[string]any{"groupId": "g", "extra": 1}}}, true},
		{"bindingNoGroup", map[string]any{"name": "n", "groupBindings": []any{map[string]any{"priority": 1}}}, true},
		{"bindingPriNull", map[string]any{"name": "n", "groupBindings": []any{map[string]any{"groupId": "g", "priority": nil}}}, true},
		{"bindingPriBad", map[string]any{"name": "n", "groupBindings": []any{map[string]any{"groupId": "g", "priority": 1.5}}}, true},
		{"bindingPriZero", map[string]any{"name": "n", "groupBindings": []any{map[string]any{"groupId": "g", "priority": 0}}}, true},
		{"bindingWeightNull", map[string]any{"name": "n", "groupBindings": []any{map[string]any{"groupId": "g", "weight": nil}}}, true},
		{"bindingWeightBad", map[string]any{"name": "n", "groupBindings": []any{map[string]any{"groupId": "g", "weight": "x"}}}, true},
		{"bindingStatusNull", map[string]any{"name": "n", "groupBindings": []any{map[string]any{"groupId": "g", "status": nil}}}, true},
		{"bindingStatusBad", map[string]any{"name": "n", "groupBindings": []any{map[string]any{"groupId": "g", "status": "bogus"}}}, true},
		{"normalBadShape", map[string]any{"name": "n", "normalRoutingConfig": "bad"}, true},
		{"normalSpeedFirstBad", map[string]any{"name": "n", "normalRoutingConfig": map[string]any{"speedFirstConfig": map[string]any{"unknownKey": 1}}}, true},
	}
	for _, testCase := range cases {
		if _, message := parseMutationFields(testCase.body, testCase.requireNew); message == "" {
			t.Fatalf("case %s must fail", testCase.name)
		}
	}
	// 未知顶层键。
	if _, message := parseCreateBody(map[string]any{"name": "n", "groupBindings": []any{map[string]any{"groupId": "g"}}, "extra": 1}); message == "" {
		t.Fatal("unknown create key must fail")
	}
	if _, message := parsePatchBody(map[string]any{"extra": 1}); message == "" {
		t.Fatal("unknown patch key must fail")
	}
	// 正常分支：显式 null 描述与 null 配置。
	input, message := parseMutationFields(map[string]any{
		"name": "n", "description": nil, "normalRoutingConfig": nil,
		"groupBindings": []any{map[string]any{"groupId": "g"}},
	}, true)
	if message != "" || !input.HasDescription || !input.HasNormalConfig {
		t.Fatalf("null fields input=%+v message=%s", input, message)
	}
	// priority 缺省回填序号。
	if input.Bindings[0].Priority == nil || *input.Bindings[0].Priority != 1 {
		t.Fatalf("fallback priority=%v", input.Bindings[0].Priority)
	}
	// parseBindingWeight 全分支。
	for _, raw := range []any{"x", 1.5, 0, 101} {
		if _, message := parseBindingWeight(raw); message == "" {
			t.Fatalf("weight %v must fail", raw)
		}
	}
	if weight, message := parseBindingWeight(50.0); message != "" || weight != 50 {
		t.Fatalf("weight 50 = %d %s", weight, message)
	}
}

func TestW11GBindingRowGuards(t *testing.T) {
	// scanBindingRow 的完整性守卫：优先级/状态/权重。
	build := func(values ...any) func(...any) error {
		return func(targets ...any) error {
			for i, target := range targets {
				if err := convertAssign(target, values[i]); err != nil {
					return err
				}
			}
			return nil
		}
	}
	// 合法行。
	row, err := scanBindingRow(build("b1", "rs1", "g1", int64(1), sql.NullString{String: "5", Valid: true}, "active", "grp", "openai", int64(1)))
	if err != nil || row.weight != 5 {
		t.Fatalf("valid row=%+v err=%v", row, err)
	}
	// 非法优先级。
	if _, err := scanBindingRow(build("b1", "rs1", "g1", int64(0), sql.NullString{}, "active", "grp", "openai", int64(1))); err == nil {
		t.Fatal("zero priority must fail")
	}
	// 非法状态。
	if _, err := scanBindingRow(build("b1", "rs1", "g1", int64(1), sql.NullString{}, "bogus", "grp", "openai", int64(1))); err == nil {
		t.Fatal("bad status must fail")
	}
	// 非法权重。
	if _, err := scanBindingRow(build("b1", "rs1", "g1", int64(1), sql.NullString{String: "101", Valid: true}, "active", "grp", "openai", int64(1))); err == nil {
		t.Fatal("bad weight must fail")
	}
	if _, err := scanBindingRow(build("b1", "rs1", "g1", int64(1), sql.NullString{String: "x", Valid: true}, "active", "grp", "openai", int64(1))); err == nil {
		t.Fatal("non-numeric weight must fail")
	}
	// REAL 形式的整数权重。
	row, err = scanBindingRow(build("b1", "rs1", "g1", int64(1), sql.NullString{String: "5.0", Valid: true}, "active", "grp", "openai", int64(1)))
	if err != nil || row.weight != 5 {
		t.Fatalf("real weight row=%+v err=%v", row, err)
	}
	// NULL/空白权重回填 1。
	row, err = scanBindingRow(build("b1", "rs1", "g1", int64(1), sql.NullString{}, "active", "grp", "openai", int64(1)))
	if err != nil || row.weight != 1 {
		t.Fatalf("null weight row=%+v err=%v", row, err)
	}
}

func TestW11GNormalizeBindingArmErrors(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w11g-nb", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w11g-nb-group", true)
	// MaxOpenConns(1)：分组必须先建完再开事务，否则插入等待唯一连接死锁。
	group2 := env.createGroup(t, admin, "w11g-nb-group2", true)
	store := w9eStore(t, env)
	ctx := context.Background()
	tx, err := env.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	// 重复分组。
	if _, err := store.normalizeBindings(ctx, tx, []BindingInput{
		{GroupID: group, Priority: intPtr(1), Status: "active"},
		{GroupID: group, Priority: intPtr(2), Status: "active"},
	}, admin, true); err == nil {
		t.Fatal("duplicate group must fail")
	}
	// 重复 active 优先级。
	if _, err := store.normalizeBindings(ctx, tx, []BindingInput{
		{GroupID: group, Priority: intPtr(1), Status: "active"},
		{GroupID: group2, Priority: intPtr(1), Status: "active"},
	}, admin, true); err == nil {
		t.Fatal("duplicate priority must fail")
	}
	// 空 groupID。
	if _, err := store.normalizeBindings(ctx, tx, []BindingInput{
		{GroupID: " ", Priority: intPtr(1), Status: "active"},
	}, admin, true); err == nil {
		t.Fatal("blank group must fail")
	}
	// 全 disabled。
	if _, err := store.normalizeBindings(ctx, tx, []BindingInput{
		{GroupID: group, Priority: intPtr(1), Status: "disabled"},
	}, admin, true); err == nil {
		t.Fatal("all disabled must fail")
	}
}

// convertAssign 把驱动值写进目标（scanBindingRow 目标类型的最小集合）。
func convertAssign(target any, value any) error {
	switch typed := target.(type) {
	case *string:
		text, ok := value.(string)
		if !ok {
			return errConvert
		}
		*typed = text
		return nil
	case **string:
		text, ok := value.(string)
		if !ok {
			return errConvert
		}
		*typed = &text
		return nil
	case *int:
		number, ok := value.(int64)
		if !ok {
			return errConvert
		}
		*typed = int(number)
		return nil
	case *sql.NullString:
		if value == nil {
			*typed = sql.NullString{}
			return nil
		}
		switch typedValue := value.(type) {
		case string:
			*typed = sql.NullString{String: typedValue, Valid: true}
		case sql.NullString:
			*typed = typedValue
		default:
			return errConvert
		}
		return nil
	default:
		return errConvert
	}
}

var errConvert = &ValidationError{Message: "w11g convert"}
