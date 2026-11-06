package oauthrefresh

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

const availabilityScheduleJSON = `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"09:00","end":"18:00"}]}`

func seedApiKeyWithSchedule(t *testing.T, db *sql.DB, id, scheduleJSON, nextCheckAt, status string) {
	t.Helper()
	var scheduleArg, nextArg any
	if scheduleJSON != "" {
		scheduleArg = scheduleJSON
	}
	if nextCheckAt != "" {
		nextArg = nextCheckAt
	}
	if status == "" {
		status = "active"
	}
	_, err := db.Exec(`INSERT INTO juhe_business.api_keys (id, status, availability_schedule_json, availability_schedule_next_check_at, updated_at, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix, key_secret_encrypted, created_at)
		VALUES ($1, $2, $3, $4, $5, 'sys_admin', 'route-default', 'key', 'kh', 'kp', 'ks', 'enc', $5)`, id, status, scheduleArg, nextArg, isoMillis(defaultNow()))
	if err != nil {
		t.Fatal(err)
	}
}

func seedAccountWithSchedule(t *testing.T, db *sql.DB, id, scheduleJSON, nextCheckAt, status string, deleted bool) {
	t.Helper()
	var scheduleArg, nextArg any
	if scheduleJSON != "" {
		scheduleArg = scheduleJSON
	}
	if nextCheckAt != "" {
		nextArg = nextCheckAt
	}
	if status == "" {
		status = "active"
	}
	deletedArg := any(nil)
	if deleted {
		deletedArg = isoMillis(defaultNow())
	}
	_, err := db.Exec(`INSERT INTO juhe_business.accounts (id, vendor_code, connection_driver_code, name, type, status, credentials_encrypted, availability_schedule_json, availability_schedule_next_check_at, deleted_at, updated_at, system_account_id, protocol_code, protocol_version, created_at)
		VALUES ($1, 'gpt', 'gpt', $2, 'api_key', $3, '{}', $4, $5, $6, $7, 'sys_admin', 'openai', 'v1', $7)`,
		id, "账户-"+id, status, scheduleArg, nextArg, deletedArg, isoMillis(defaultNow()))
	if err != nil {
		t.Fatal(err)
	}
}

// setScheduleGate 直接改写夹具行的闸门现值（NULL / 0 / 1），用于构造「闸门已
// 求值」「闸门已开」等前置状态。table 只取测试内的固定表名。
func setScheduleGate(t *testing.T, db *sql.DB, table, id string, gate any) {
	t.Helper()
	if _, err := db.Exec(`UPDATE juhe_business.`+table+` SET availability_schedule_gate = $1 WHERE id = $2`, gate, id); err != nil {
		t.Fatal(err)
	}
}

// requireGate 断言闸门列取值：want 为 "NULL" / "0" / "1"。
func requireGate(t *testing.T, label string, got sql.NullInt64, want string) {
	t.Helper()
	switch want {
	case "NULL":
		if got.Valid {
			t.Fatalf("%s gate=%d want NULL（无计划 / 未求值）", label, got.Int64)
		}
	case "0", "1":
		var expected int64
		if want == "1" {
			expected = 1
		}
		if !got.Valid || got.Int64 != expected {
			t.Fatalf("%s gate=%+v want %s", label, got, want)
		}
	default:
		t.Fatalf("%s 非法的期望闸门值 %q", label, want)
	}
}

func readApiKey(t *testing.T, db *sql.DB, id string) (status, nextCheckAt string, updatedAt string) {
	t.Helper()
	if err := db.QueryRow(`SELECT status, COALESCE(availability_schedule_next_check_at,''), updated_at FROM juhe_business.api_keys WHERE id = $1`, id).Scan(&status, &nextCheckAt, &updatedAt); err != nil {
		t.Fatal(err)
	}
	return
}

func readApiKeyGate(t *testing.T, db *sql.DB, id string) sql.NullInt64 {
	t.Helper()
	var gate sql.NullInt64
	if err := db.QueryRow(`SELECT availability_schedule_gate FROM juhe_business.api_keys WHERE id = $1`, id).Scan(&gate); err != nil {
		t.Fatal(err)
	}
	return gate
}

func readAccountStatus(t *testing.T, db *sql.DB, id string) (status, nextCheckAt, deletedAt string) {
	t.Helper()
	if err := db.QueryRow(`SELECT status, COALESCE(availability_schedule_next_check_at,''), COALESCE(deleted_at,'') FROM juhe_business.accounts WHERE id = $1`, id).Scan(&status, &nextCheckAt, &deletedAt); err != nil {
		t.Fatal(err)
	}
	return status, nextCheckAt, deletedAt
}

func readAccountGate(t *testing.T, db *sql.DB, id string) sql.NullInt64 {
	t.Helper()
	var gate sql.NullInt64
	if err := db.QueryRow(`SELECT availability_schedule_gate FROM juhe_business.accounts WHERE id = $1`, id).Scan(&gate); err != nil {
		t.Fatal(err)
	}
	return gate
}

// TestSyncApiKeyScheduleActivatesAtWindowStart 钉住闸门契约的核心不变量：窗口
// 开始时只把 gate 置 1，status 保持调用方（人工 / 健康状态机）的现值不变。
func TestSyncApiKeyScheduleActivatesAtWindowStart(t *testing.T) {
	store, db, _ := newSweepStore(t)
	// 09:00 UTC Monday: the window start boundary.
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	seedApiKeyWithSchedule(t, db, "key-1", availabilityScheduleJSON, "", "disabled")

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), atStart, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Activated != 1 || result.Disabled != 0 || len(result.ChangedIDs) != 1 || result.ChangedIDs[0] != "key-1" {
		t.Fatalf("result=%+v", result)
	}
	status, nextCheck, _ := readApiKey(t, db, "key-1")
	if status != "disabled" {
		t.Fatalf("同步不得改写 status：status=%q want disabled（人工停用持久生效）", status)
	}
	requireGate(t, "key-1", readApiKeyGate(t, db, "key-1"), "1")
	// The derived next check is the window end the same day.
	if nextCheck != "2026-09-07T18:00:00.000Z" {
		t.Fatalf("next check=%q", nextCheck)
	}
	// 事件表只是复核审计：键形如 id:active:<UTC 分钟>，不钉死旧边界键。
	var eventKey string
	if err := db.QueryRow(`SELECT event_key FROM juhe_business.api_key_schedule_status_events WHERE event_key LIKE 'key-1:%' AND status = 'active'`).Scan(&eventKey); err != nil {
		t.Fatal(err)
	}
	if len(eventKey) <= len("key-1:active:") || eventKey[:len("key-1:active:")] != "key-1:active:" {
		t.Fatalf("审计事件键=%q want 前缀 key-1:active:", eventKey)
	}

	// Replaying the same minute: the gate already matches the target, so only
	// next_check advances (reset it first, as a fresh import would).
	if _, err := db.Exec(`UPDATE juhe_business.api_keys SET availability_schedule_next_check_at = NULL WHERE id = 'key-1'`); err != nil {
		t.Fatal(err)
	}
	replay, err := store.SyncApiKeyScheduleStatuses(context.Background(), atStart, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Scanned != 1 || replay.Unchanged != 1 || replay.Activated != 0 || replay.Disabled != 0 || len(replay.ChangedIDs) != 0 {
		t.Fatalf("replay=%+v", replay)
	}
	status2, _, _ := readApiKey(t, db, "key-1")
	if status2 != "disabled" {
		t.Fatalf("status after replay=%q want disabled", status2)
	}
	requireGate(t, "key-1 replay", readApiKeyGate(t, db, "key-1"), "1")
	var events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_business.api_key_schedule_status_events WHERE event_key LIKE 'key-1:%'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("重放不得新增审计事件：rows=%d want 1", events)
	}
}

func TestSyncApiKeyScheduleDisablesAtWindowEnd(t *testing.T) {
	store, db, _ := newSweepStore(t)
	atEnd := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	seedApiKeyWithSchedule(t, db, "key-end", availabilityScheduleJSON, "", "active")
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), atEnd, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disabled != 1 || result.Activated != 0 {
		t.Fatalf("result=%+v", result)
	}
	status, nextCheck, _ := readApiKey(t, db, "key-end")
	if status != "active" {
		t.Fatalf("同步不得改写 status：status=%q want active（时段外只是非时间段）", status)
	}
	requireGate(t, "key-end", readApiKeyGate(t, db, "key-end"), "0")
	// Next boundary: tomorrow 09:00.
	if nextCheck != "2026-09-08T09:00:00.000Z" {
		t.Fatalf("next check=%q", nextCheck)
	}
}

func TestSyncApiKeyScheduleUnchangedAdvancesNextCheck(t *testing.T) {
	store, db, _ := newSweepStore(t)
	// Off-boundary minute inside the window with the gate already open: no
	// write except next_check.
	inside := time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC)
	seedApiKeyWithSchedule(t, db, "key-inside", availabilityScheduleJSON, "", "disabled")
	setScheduleGate(t, db, "api_keys", "key-inside", 1)
	_, _, updatedBefore := readApiKey(t, db, "key-inside")

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), inside, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Unchanged != 1 || result.Activated != 0 || result.Disabled != 0 {
		t.Fatalf("result=%+v", result)
	}
	status, nextCheck, updatedAfter := readApiKey(t, db, "key-inside")
	if status != "disabled" {
		t.Fatalf("status=%q want disabled（同值闸门不得改写 status）", status)
	}
	requireGate(t, "key-inside", readApiKeyGate(t, db, "key-inside"), "1")
	if nextCheck != "2026-09-07T18:00:00.000Z" {
		t.Fatalf("next check=%q", nextCheck)
	}
	if updatedAfter != updatedBefore {
		t.Fatalf("同值闸门不得改写 updated_at: %q -> %q", updatedBefore, updatedAfter)
	}
}

func TestSyncApiKeyScheduleInvalidClosesGate(t *testing.T) {
	store, db, _ := newSweepStore(t)
	seedApiKeyWithSchedule(t, db, "key-invalid", `{"enabled":true,"mode":"bogus"}`, "", "active")
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), defaultNow(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Invalid != 1 || len(result.InvalidIDs) != 1 || result.InvalidIDs[0] != "key-invalid" {
		t.Fatalf("result=%+v", result)
	}
	if result.Disabled != 1 || result.Activated != 0 {
		t.Fatalf("非法计划必须 fail-closed 关闸：%+v", result)
	}
	status, nextCheck, _ := readApiKey(t, db, "key-invalid")
	if status != "active" {
		t.Fatalf("非法计划只关闸不改写 status：status=%q want active", status)
	}
	requireGate(t, "key-invalid", readApiKeyGate(t, db, "key-invalid"), "0")
	// 设计 §4.1：非法计划 fail-closed 关闸，同时把 next_check_at 写到 1 小时后
	// 避免坏行永久 due；这里断言该值而不是“空”。
	if want := isoMillis(defaultNow().Add(time.Hour)); nextCheck != want {
		t.Fatalf("非法计划下次检查 next=%q want %q", nextCheck, want)
	}

	// 已落库为 disabled 的非法行同样只关闸，status 保持 disabled。第二行首次
	// 入候选；第一行已排到 1 小时后，推进到该时刻后两行同时到期。
	seedApiKeyWithSchedule(t, db, "key-invalid-disabled", `{"enabled":true,"mode":"bogus"}`, "", "disabled")
	result, err = store.SyncApiKeyScheduleStatuses(context.Background(), defaultNow().Add(2*time.Hour), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 2 || result.Invalid != 2 || result.Disabled != 1 || result.Unchanged != 1 {
		t.Fatalf("second invalid=%+v", result)
	}
	requireGate(t, "key-invalid", readApiKeyGate(t, db, "key-invalid"), "0")
	requireGate(t, "key-invalid-disabled", readApiKeyGate(t, db, "key-invalid-disabled"), "0")
	if status, _, _ := readApiKey(t, db, "key-invalid-disabled"); status != "disabled" {
		t.Fatalf("status=%q want disabled", status)
	}
}

func TestSyncApiKeyScheduleDueWindowCoversNullNextCheck(t *testing.T) {
	store, db, _ := newSweepStore(t)
	// NULL next_check_at is always due, even off-boundary; a NULL gate is also
	// due (upgrade self-healing), so the first scan opens the gate.
	inside := time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC)
	seedApiKeyWithSchedule(t, db, "key-null", availabilityScheduleJSON, "", "active")
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), inside, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Activated != 1 || result.Unchanged != 0 {
		t.Fatalf("result=%+v", result)
	}
	requireGate(t, "key-null", readApiKeyGate(t, db, "key-null"), "1")
	if status, _, _ := readApiKey(t, db, "key-null"); status != "active" {
		t.Fatalf("status=%q want active（同步不改写）", status)
	}

	// A future next_check_at beyond the due time is not scanned once the gate
	// has been evaluated.
	seedApiKeyWithSchedule(t, db, "key-future", availabilityScheduleJSON, "2026-09-07T18:00:00.000Z", "active")
	setScheduleGate(t, db, "api_keys", "key-future", 1)
	result, err = store.SyncApiKeyScheduleStatuses(context.Background(), inside, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 0 {
		t.Fatalf("future scanned=%d", result.Scanned)
	}
	requireGate(t, "key-future", readApiKeyGate(t, db, "key-future"), "1")
}

// TestSyncAccountScheduleSemantics 钉住账户闸门与 status 正交：所有非删除行都
// 按窗口维护闸门（含 pending_test），已一致的行只推进下次检查，激活 hook 只在
// 闸门由关到开时触发。
func TestSyncAccountScheduleSemantics(t *testing.T) {
	store, db, _ := newSweepStore(t)
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	const lateWindowJSON = `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"10:00","end":"18:00"}]}`

	// disabled 行：闸门开，status 保持 disabled（人工停用持久生效）。
	seedAccountWithSchedule(t, db, "acc-disabled", availabilityScheduleJSON, "", "disabled", false)
	// pending_test 行：闸门同样维护，status 保持 pending_test。
	seedAccountWithSchedule(t, db, "acc-pending", availabilityScheduleJSON, "", "pending_test", false)
	// 闸门已开的行：只推进下次检查。
	seedAccountWithSchedule(t, db, "acc-inside", availabilityScheduleJSON, "", "active", false)
	setScheduleGate(t, db, "accounts", "acc-inside", 1)
	// 时段外的行：闸门关，status 保持 active，不触发激活 hook。
	seedAccountWithSchedule(t, db, "acc-outside", lateWindowJSON, "", "active", false)
	// Deleted accounts are invisible.
	seedAccountWithSchedule(t, db, "acc-deleted", availabilityScheduleJSON, "", "disabled", true)

	activations := []string{}
	hook := ActivationHookFunc(func(_ context.Context, _ *sql.Tx, accountID, _ string) error {
		activations = append(activations, accountID)
		return nil
	})
	result, err := store.SyncAccountScheduleStatuses(context.Background(), atStart, 0, hook)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 4 {
		t.Fatalf("scanned=%d (deleted row must be excluded)", result.Scanned)
	}
	if result.Activated != 2 || result.Disabled != 1 || result.Unchanged != 1 || result.Invalid != 0 {
		t.Fatalf("result=%+v", result)
	}
	if len(activations) != 2 || activations[0] != "acc-disabled" || activations[1] != "acc-pending" {
		t.Fatalf("activations=%v want [acc-disabled acc-pending]（只在开闸时触发）", activations)
	}
	cases := []struct{ id, status, gate string }{
		{"acc-disabled", "disabled", "1"},
		{"acc-pending", "pending_test", "1"},
		{"acc-inside", "active", "1"},
		{"acc-outside", "active", "0"},
	}
	for _, testCase := range cases {
		status, _, _ := readAccountStatus(t, db, testCase.id)
		if status != testCase.status {
			t.Fatalf("%s status=%q want %q（同步不得改写 status）", testCase.id, status, testCase.status)
		}
		requireGate(t, testCase.id, readAccountGate(t, db, testCase.id), testCase.gate)
	}
	if status, _, deletedAt := readAccountStatus(t, db, "acc-deleted"); status != "disabled" || deletedAt == "" {
		t.Fatalf("deleted row mutated: status=%q deletedAt=%q", status, deletedAt)
	}
	requireGate(t, "acc-deleted", readAccountGate(t, db, "acc-deleted"), "NULL")
}

func TestSyncAccountScheduleInvalidActiveRowClosesGate(t *testing.T) {
	store, db, _ := newSweepStore(t)
	seedAccountWithSchedule(t, db, "acc-invalid", `{"enabled":true,"mode":"bogus"}`, "", "active", false)
	result, err := store.SyncAccountScheduleStatuses(context.Background(), defaultNow(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Invalid != 1 || result.Disabled != 1 {
		t.Fatalf("result=%+v", result)
	}
	status, _, _ := readAccountStatus(t, db, "acc-invalid")
	if status != "active" {
		t.Fatalf("非法计划只关闸不改写 status：status=%q want active", status)
	}
	requireGate(t, "acc-invalid", readAccountGate(t, db, "acc-invalid"), "0")
}

func TestSyncBatchLimit(t *testing.T) {
	store, db, _ := newSweepStore(t)
	for i := 0; i < 7; i++ {
		seedApiKeyWithSchedule(t, db, "batch-"+string(rune('a'+i)), availabilityScheduleJSON, "", "active")
	}
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC), 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 5 {
		t.Fatalf("scanned=%d want batch limit 5", result.Scanned)
	}
	// 先按 id 排序的前 5 行开闸，其余两行未被本轮扫描（闸门仍为 NULL）。
	for _, id := range []string{"batch-a", "batch-b", "batch-c", "batch-d", "batch-e"} {
		requireGate(t, id, readApiKeyGate(t, db, id), "1")
	}
	for _, id := range []string{"batch-f", "batch-g"} {
		requireGate(t, id, readApiKeyGate(t, db, id), "NULL")
	}
}

// TestSyncActivationHookErrorAborts 钉住 hook 与闸门写入同事务：hook 失败必须
// 回滚闸门与审计事件，status 保持不变（Key 与账户两侧同口径）。
func TestSyncActivationHookErrorAborts(t *testing.T) {
	store, db, _ := newSweepStore(t)
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	seedApiKeyWithSchedule(t, db, "key-hook", availabilityScheduleJSON, "", "disabled")
	seedAccountWithSchedule(t, db, "acc-hook", availabilityScheduleJSON, "", "disabled", false)
	failing := ActivationHookFunc(func(context.Context, *sql.Tx, string, string) error {
		return context.DeadlineExceeded
	})
	if _, err := store.SyncAccountScheduleStatuses(context.Background(), atStart, 0, failing); err == nil {
		t.Fatal("hook error must abort the sync transaction")
	}
	// The gate write and audit event rolled back with the hook failure.
	status, _, _ := readAccountStatus(t, db, "acc-hook")
	if status != "disabled" {
		t.Fatalf("status=%q after failed hook（status 不得被同步改写）", status)
	}
	requireGate(t, "acc-hook", readAccountGate(t, db, "acc-hook"), "NULL")
	var accountEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_business.account_schedule_status_events WHERE account_id = 'acc-hook'`).Scan(&accountEvents); err != nil {
		t.Fatal(err)
	}
	if accountEvents != 0 {
		t.Fatalf("hook 失败必须回滚审计事件：rows=%d", accountEvents)
	}

	failingKey := APIKeyActivationHookFunc(func(context.Context, *sql.Tx, string, string) (func(), error) {
		return nil, context.DeadlineExceeded
	})
	if _, err := store.SyncApiKeyScheduleStatuses(context.Background(), atStart, 0, failingKey); err == nil {
		t.Fatal("key hook error must abort the sync transaction")
	}
	keyStatus, _, _ := readApiKey(t, db, "key-hook")
	if keyStatus != "disabled" {
		t.Fatalf("key status=%q after failed hook", keyStatus)
	}
	requireGate(t, "key-hook", readApiKeyGate(t, db, "key-hook"), "NULL")
	var keyEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_business.api_key_schedule_status_events WHERE api_key_id = 'key-hook'`).Scan(&keyEvents); err != nil {
		t.Fatal(err)
	}
	if keyEvents != 0 {
		t.Fatalf("Key hook 失败必须回滚审计事件：rows=%d", keyEvents)
	}
}
