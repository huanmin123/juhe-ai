package circuitstore

// w13g5_circuit_hydrate_arms_test.go 直调投影合成纯函数，覆盖运行态键
// 派生、分组绑定判定与配额载荷解析的分支。

import (
	"database/sql"
	"testing"
)

func w13g5NullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

func TestW13g5RuntimeKeyOfArms(t *testing.T) {
	unauthorized := managementRow{id: "w13g5-acc"}
	if got := runtimeKeyOf(unauthorized); got != "w13g5-acc" {
		t.Fatalf("未授权行运行态键必须为账户 ID: %s", got)
	}
	authorized := managementRow{
		id: "w13g5-acc", systemAccountID: "w13g5-sa",
		authorizationID:                  w13g5NullString("w13g5-authz"),
		boundGroupID:                     w13g5NullString("w13g5-grp"),
		bindingSystemAccountID:           sql.NullString{String: "w13g5-sa", Valid: true},
		boundGroupAccountAuthorizationID: sql.NullString{String: "w13g5-authz", Valid: true},
	}
	got := runtimeKeyOf(authorized)
	if got != "w13g5-acc:authorized:w13g5-sa:w13g5-grp:w13g5-authz" {
		t.Fatalf("授权行运行态键: %s", got)
	}
	// binding 归属不一致 → 回退账户 ID。
	mismatched := authorized
	mismatched.bindingSystemAccountID = sql.NullString{String: "w13g5-other", Valid: true}
	if got := runtimeKeyOf(mismatched); got != "w13g5-acc" {
		t.Fatalf("归属不一致必须回退: %s", got)
	}
}

func TestW13g5GroupBindingOfArms(t *testing.T) {
	if groupBindingOf(managementRow{id: "w13g5-acc"}) != nil {
		t.Fatal("无绑定组必须返回 nil")
	}
	row := managementRow{
		id:                               "w13g5-acc",
		systemAccountID:                  "w13g5-sa",
		boundGroupID:                     w13g5NullString("w13g5-grp"),
		bindingSystemAccountID:           sql.NullString{String: "w13g5-sa", Valid: true},
		boundGroupAccountAuthorizationID: sql.NullString{String: "w13g5-authz", Valid: true},
		authorizationID:                  sql.NullString{String: "w13g5-authz", Valid: true},
	}
	binding := groupBindingOf(row)
	if binding == nil || binding.groupID != "w13g5-grp" || binding.groupBindStatus != "bound" {
		t.Fatalf("绑定解析: %+v", binding)
	}
	mismatched := row
	mismatched.boundGroupAccountAuthorizationID = sql.NullString{String: "w13g5-other-authz", Valid: true}
	binding = groupBindingOf(mismatched)
	if binding == nil || binding.groupBindStatus != "authorization_unavailable" {
		t.Fatalf("授权不一致必须标记: %+v", binding)
	}
}
