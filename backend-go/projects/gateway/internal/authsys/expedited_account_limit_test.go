package authsys

// 《AI账户特供快速恢复通道设计》§8 系统账户侧 / §11.6（authsys 范围）：
// system_accounts.expedited_account_limit 的三态补丁语义、读投影
// （int|null，默认 3 归一由消费方 COALESCE）、越界拒绝、审计与 updated_at。
// 既有 aiAccountLimit 链路（TestSystemAccountNullablePatchSemantics /
// TestW11APatchMutationArms）是本文件断言形态的模板。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

func expeditedNewStore(t *testing.T) (*AccountStore, *sql.DB) {
	t.Helper()
	db := newContractTestDB(t)
	store, err := NewAccountStore(db, modelcheckauth.SQLite, time.Now, readyOwnerGate)
	if err != nil {
		t.Fatal(err)
	}
	return store, db
}

func expeditedCreate(t *testing.T, store *AccountStore, username string, limit *int) AccountListItem {
	t.Helper()
	item, err := store.Create(context.Background(), CreateInput{
		Username: username, DisplayName: username + "_name", Password: "expedited-pass",
		ExpeditedAccountLimit: limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func expeditedVersion(t *testing.T, store *AccountStore, id string) string {
	t.Helper()
	found, err := store.FindByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return found.UpdatedAt
}

func expeditedColumn(t *testing.T, db *sql.DB, id string) sql.NullInt64 {
	t.Helper()
	var value sql.NullInt64
	if err := db.QueryRow(`SELECT expedited_account_limit FROM system_accounts WHERE id = ?`, id).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func expeditedLimitPtr(value int) *int { return &value }

// 默认（无覆盖）读取为 NULL：摘要投影为 nil，读侧不回填默认 3。
func TestExpeditedAccountLimitDefaultIsNull(t *testing.T) {
	store, db := expeditedNewStore(t)
	item := expeditedCreate(t, store, "exp-default", nil)

	found, err := store.FindByID(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.ExpeditedAccountLimit != nil {
		t.Fatalf("default limit = %v, want nil (DB NULL)", *found.ExpeditedAccountLimit)
	}
	if column := expeditedColumn(t, db, item.ID); column.Valid {
		t.Fatalf("default column = %d, want NULL", column.Int64)
	}
	items, _, _, err := store.ListPage(context.Background(), "exp-default", 1, 20)
	if err != nil || len(items) != 1 {
		t.Fatalf("list = %v items, err %v", len(items), err)
	}
	if items[0].ExpeditedAccountLimit != nil {
		t.Fatalf("list projection = %v, want nil", *items[0].ExpeditedAccountLimit)
	}
	// JSON 键恒存在且为显式 null（契约 §8.1：响应为 int|null）。
	encoded, err := json.Marshal(items[0])
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if value, ok := payload["expeditedAccountLimit"]; !ok || value != nil {
		t.Fatalf("expeditedAccountLimit must render explicit null: %s", encoded)
	}
}

// 设置 0–100 边界生效；mutation 回显携带整数。
func TestExpeditedAccountLimitSetBoundaries(t *testing.T) {
	store, db := expeditedNewStore(t)
	item := expeditedCreate(t, store, "exp-bounds", nil)

	for _, boundary := range []int{0, 3, 100} {
		version := expeditedVersion(t, store, item.ID)
		result, err := store.Patch(context.Background(), item.ID, PatchInput{
			ExpectedUpdatedAt: version, ExpeditedAccountLimitPresent: true, ExpeditedAccountLimit: expeditedLimitPtr(boundary),
		})
		if err != nil {
			t.Fatalf("set %d: %v", boundary, err)
		}
		if string(result.ExpeditedAccountLimit) != strconv.Itoa(boundary) {
			t.Fatalf("set %d mutation echo = %s", boundary, result.ExpeditedAccountLimit)
		}
		if column := expeditedColumn(t, db, item.ID); !column.Valid || column.Int64 != int64(boundary) {
			t.Fatalf("set %d column = %+v", boundary, column)
		}
	}
}

// 显式 null 清除覆盖值恢复 DB NULL；已为 NULL 时再清为 no-op（不动版本）。
func TestExpeditedAccountLimitClearRestoresNull(t *testing.T) {
	store, db := expeditedNewStore(t)
	item := expeditedCreate(t, store, "exp-clear", expeditedLimitPtr(5))

	version := expeditedVersion(t, store, item.ID)
	result, err := store.Patch(context.Background(), item.ID, PatchInput{
		ExpectedUpdatedAt: version, ExpeditedAccountLimitPresent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExpeditedAccountLimit == nil || string(result.ExpeditedAccountLimit) != "null" {
		t.Fatalf("clear mutation echo = %s, want explicit null", result.ExpeditedAccountLimit)
	}
	if column := expeditedColumn(t, db, item.ID); column.Valid {
		t.Fatalf("cleared column = %d, want NULL", column.Int64)
	}
	if result.UpdatedAt == version {
		t.Fatal("clearing an override must rotate updated_at")
	}

	// 已为 NULL 的重复清除是 no-op：无回显、版本不变。
	noOpVersion := expeditedVersion(t, store, item.ID)
	noOp, err := store.Patch(context.Background(), item.ID, PatchInput{
		ExpectedUpdatedAt: noOpVersion, ExpeditedAccountLimitPresent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(noOp.ExpeditedAccountLimit) != 0 {
		t.Fatalf("redundant clear must not echo the field: %s", noOp.ExpeditedAccountLimit)
	}
	if noOp.UpdatedAt != noOpVersion {
		t.Fatalf("redundant clear rotated the version: %s -> %s", noOpVersion, noOp.UpdatedAt)
	}
}

// 越界（-1、101）拒绝且不改原值、不动版本；创建通道同样拒绝。
func TestExpeditedAccountLimitRejectsOutOfRange(t *testing.T) {
	store, db := expeditedNewStore(t)
	item := expeditedCreate(t, store, "exp-range", expeditedLimitPtr(7))
	version := expeditedVersion(t, store, item.ID)

	for _, invalid := range []int{-1, 101} {
		_, err := store.Patch(context.Background(), item.ID, PatchInput{
			ExpectedUpdatedAt: version, ExpeditedAccountLimitPresent: true, ExpeditedAccountLimit: expeditedLimitPtr(invalid),
		})
		var validation *ValidationError
		if !errors.As(err, &validation) || validation.Message != "特供账户上限必须是 0 到 100 之间的整数" {
			t.Fatalf("patch %d err = %v", invalid, err)
		}
	}
	if column := expeditedColumn(t, db, item.ID); !column.Valid || column.Int64 != 7 {
		t.Fatalf("rejected patches must not touch the stored value: %+v", column)
	}
	if expeditedVersion(t, store, item.ID) != version {
		t.Fatal("rejected patches must not rotate updated_at")
	}

	for _, invalid := range []int{-1, 101} {
		if _, err := store.Create(context.Background(), CreateInput{
			Username: "exp-range-create", DisplayName: "exp_range_create", Password: "expedited-pass",
			ExpeditedAccountLimit: expeditedLimitPtr(invalid),
		}); err == nil || !strings.Contains(err.Error(), "特供账户上限") {
			t.Fatalf("create %d err = %v", invalid, err)
		}
	}
}

// 缺省字段不动原值：补丁只携带其他字段时列值保持；同值重复提交为 no-op。
func TestExpeditedAccountLimitAbsentLeavesValueUntouched(t *testing.T) {
	store, db := expeditedNewStore(t)
	item := expeditedCreate(t, store, "exp-absent", expeditedLimitPtr(7))
	version := expeditedVersion(t, store, item.ID)

	renamed := "exp_absent_renamed"
	if _, err := store.Patch(context.Background(), item.ID, PatchInput{
		ExpectedUpdatedAt: version, DisplayName: &renamed,
	}); err != nil {
		t.Fatal(err)
	}
	if column := expeditedColumn(t, db, item.ID); !column.Valid || column.Int64 != 7 {
		t.Fatalf("absent-field patch must not touch the column: %+v", column)
	}

	// 同值重复提交：无新版本（照 aiAccountLimit 的 no-op 语义）。
	sameVersion := expeditedVersion(t, store, item.ID)
	same, err := store.Patch(context.Background(), item.ID, PatchInput{
		ExpectedUpdatedAt: sameVersion, ExpeditedAccountLimitPresent: true, ExpeditedAccountLimit: expeditedLimitPtr(7),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(same.ExpeditedAccountLimit) != 0 || same.UpdatedAt != sameVersion {
		t.Fatalf("identical limit patch must be a no-op: echo=%s version %s -> %s", same.ExpeditedAccountLimit, sameVersion, same.UpdatedAt)
	}
}

// parse 层三态形态：缺省不置位、null 置位且清值、整数取整、非整数与小数拒绝。
func TestExpeditedAccountLimitParseTriState(t *testing.T) {
	stamp := "2026-01-01T00:00:00Z"
	// 缺省探测需携带另一个修改字段（仅有 expectedUpdatedAt 的补丁在解析层被拒）。
	input, err := parsePatchInput(map[string]any{"expectedUpdatedAt": stamp, "displayName": "renamed"})
	if err != nil {
		t.Fatal(err)
	}
	if input.ExpeditedAccountLimitPresent {
		t.Fatal("absent field must not set the present flag")
	}
	input, err = parsePatchInput(map[string]any{"expectedUpdatedAt": stamp, "expeditedAccountLimit": nil})
	if err != nil || !input.ExpeditedAccountLimitPresent || input.ExpeditedAccountLimit != nil {
		t.Fatalf("null tri-state = %+v err=%v", input, err)
	}
	input, err = parsePatchInput(map[string]any{"expectedUpdatedAt": stamp, "expeditedAccountLimit": float64(42)})
	if err != nil || !input.ExpeditedAccountLimitPresent || input.ExpeditedAccountLimit == nil || *input.ExpeditedAccountLimit != 42 {
		t.Fatalf("integer tri-state = %+v err=%v", input, err)
	}
	for _, invalid := range []any{"abc", 1.5, true} {
		if _, err := parsePatchInput(map[string]any{"expectedUpdatedAt": stamp, "expeditedAccountLimit": invalid}); err == nil || !strings.Contains(err.Error(), "系统账户参数无效") {
			t.Fatalf("invalid %v err = %v", invalid, err)
		}
	}
}

// HTTP 合同：默认 null 投影、设置、null 清除、越界 409、非法形态 400、审计变更记录。
func TestExpeditedAccountLimitHTTPContract(t *testing.T) {
	deps, _, server := newTestEnv(t)
	sink := &captureSink{}
	deps.Sink = sink
	seedAccount(t, deps, "exprthost", "exprt-host-pass", "super_admin")
	seedAccount(t, deps, "exprtuser", "exprt-user-pass", "user")
	adminCookie := login(t, server, "exprthost", "exprt-host-pass")
	plainID := accountIDByUsername(t, deps, "exprtuser")

	_, payload := getJSON(t, server, "/__aisys__/api/system-accounts?page=1&pageSize=20", adminCookie)
	item := findItemByUsername(t, payload, "exprtuser")
	if value, ok := item["expeditedAccountLimit"]; !ok || value != nil {
		t.Fatalf("default list projection must be explicit null: %v", item)
	}

	// 设置 42：回显整数、列表投影整数、updated_at 前进。
	patch, patchPayload := patchJSON(t, server, "/__aisys__/api/system-accounts/"+plainID,
		`{"expectedUpdatedAt":"`+item["editVersion"].(string)+`","expeditedAccountLimit":42}`, adminCookie)
	if patch.StatusCode != http.StatusOK {
		t.Fatalf("set via http: %d %v", patch.StatusCode, patchPayload)
	}
	data := patchPayload["data"].(map[string]any)
	if data["expeditedAccountLimit"] != float64(42) {
		t.Fatalf("set echo = %v", data)
	}
	if data["updatedAt"] == item["editVersion"] {
		t.Fatal("set must rotate updated_at")
	}
	_, payload = getJSON(t, server, "/__aisys__/api/system-accounts?page=1&pageSize=20", adminCookie)
	item = findItemByUsername(t, payload, "exprtuser")
	if item["expeditedAccountLimit"] != float64(42) {
		t.Fatalf("list projection after set = %v", item["expeditedAccountLimit"])
	}

	// 越界 101：409 校验错误且原值不变；非整数 "abc"：400 解析错误。
	rangePatch, rangePayload := patchJSON(t, server, "/__aisys__/api/system-accounts/"+plainID,
		`{"expectedUpdatedAt":"`+item["editVersion"].(string)+`","expeditedAccountLimit":101}`, adminCookie)
	if rangePatch.StatusCode != http.StatusConflict || rangePayload["message"] != "特供账户上限必须是 0 到 100 之间的整数" {
		t.Fatalf("out-of-range via http = %d %v", rangePatch.StatusCode, rangePayload)
	}
	typePatch, typePayload := patchJSON(t, server, "/__aisys__/api/system-accounts/"+plainID,
		`{"expectedUpdatedAt":"`+item["editVersion"].(string)+`","expeditedAccountLimit":"abc"}`, adminCookie)
	if typePatch.StatusCode != http.StatusBadRequest || typePayload["message"] != "系统账户参数无效" {
		t.Fatalf("non-integer via http = %d %v", typePatch.StatusCode, typePayload)
	}
	_, payload = getJSON(t, server, "/__aisys__/api/system-accounts?page=1&pageSize=20", adminCookie)
	if item = findItemByUsername(t, payload, "exprtuser"); item["expeditedAccountLimit"] != float64(42) {
		t.Fatalf("rejected patches must not touch the projection: %v", item["expeditedAccountLimit"])
	}

	// 显式 null 清除：回显显式 null、存储 NULL。
	cleared, clearedPayload := patchJSON(t, server, "/__aisys__/api/system-accounts/"+plainID,
		`{"expectedUpdatedAt":"`+item["editVersion"].(string)+`","expeditedAccountLimit":null}`, adminCookie)
	if cleared.StatusCode != http.StatusOK {
		t.Fatalf("clear via http: %d %v", cleared.StatusCode, clearedPayload)
	}
	if value, ok := clearedPayload["data"].(map[string]any)["expeditedAccountLimit"]; !ok || value != nil {
		t.Fatalf("clear echo must be explicit null: %v", clearedPayload)
	}
	stored, err := deps.Accounts.FindByID(nil, plainID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ExpeditedAccountLimit != nil {
		t.Fatalf("cleared storage = %v, want nil", *stored.ExpeditedAccountLimit)
	}

	// 审计：设置与清除各产生一条 expeditedAccountLimit 变更（空串表达 NULL）。
	var limitChanges []OperationLogChange
	for _, entry := range sink.snapshot() {
		for _, change := range entry.Changes {
			if change.Field == "expeditedAccountLimit" {
				limitChanges = append(limitChanges, change)
			}
		}
	}
	if len(limitChanges) != 2 {
		t.Fatalf("audit changes = %+v, want set+clear pair", limitChanges)
	}
	if limitChanges[0].Label != "特供账户上限" || limitChanges[0].Before != "" || limitChanges[0].After != "42" {
		t.Fatalf("set audit change = %+v", limitChanges[0])
	}
	if limitChanges[1].Before != "42" || limitChanges[1].After != "" {
		t.Fatalf("clear audit change = %+v", limitChanges[1])
	}
}
