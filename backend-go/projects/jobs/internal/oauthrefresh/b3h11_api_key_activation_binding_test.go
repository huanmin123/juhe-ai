package oauthrefresh

// B3-H11 回归：API Key 可用性排期同步把闸门由关到开（gate 0/NULL → 1）时，
// 必须在同一事务重建 request_quota_hourly_window_scope_bindings 的 api_key 绑定，
// 并在提交后给 statsagg 打脏；否则小时额度窗口永远缺失（静默 fail-open）。
// 2026-11-03 闸门契约后绑定重建跟随闸门，不再跟随 status 翻转（status 同步
// 任务完全不写）。
//
// 覆盖：
//   - 活跃 Key（闸门未求值，无绑定）→ 计划开闸 → 绑定行逐字段正确 + 脏范围出现；
//   - 重复开闸幂等（DELETE + ON CONFLICT，仅一行，generation 自增）；
//   - 计划关闸 / 无小时额度不写绑定、不打脏；
//   - 额度文档无法判定小时窗口时按行隔离：该 Key 不开闸、不写绑定，同批
//     其它 Key 照常完成（避免一行坏数据卡死整批闸门收敛）。

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

const apiKeyHourlyQuotaJSON = `{"hourly":{"enabled":true,"hours":6,"limit":12.5}}`

// seedApiKeyWithScheduleAndQuota 种入带额度配置的排期 Key（列集与
// seedApiKeyWithSchedule 相同，额外写入 quota_limits_json）。
func seedApiKeyWithScheduleAndQuota(t *testing.T, db *sql.DB, id, scheduleJSON, nextCheckAt, status, quotaJSON string) {
	t.Helper()
	var scheduleArg, nextArg, quotaArg any
	if scheduleJSON != "" {
		scheduleArg = scheduleJSON
	}
	if nextCheckAt != "" {
		nextArg = nextCheckAt
	}
	if quotaJSON != "" {
		quotaArg = quotaJSON
	}
	if status == "" {
		status = "active"
	}
	_, err := db.Exec(`INSERT INTO juhe_business.api_keys (id, status, availability_schedule_json, availability_schedule_next_check_at, quota_limits_json, updated_at, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix, key_secret_encrypted, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'sys_admin', 'route-default', 'key', 'kh', 'kp', 'ks', 'enc', $6)`,
		id, status, scheduleArg, nextArg, quotaArg, isoMillis(defaultNow()))
	if err != nil {
		t.Fatal(err)
	}
}

type apiKeyQuotaBindingRow struct {
	ownerID     string
	scopeType   string
	scopeID     string
	sourceType  string
	sourceID    string
	windowHours int
	createdAt   string
	updatedAt   string
}

func readAPIKeyQuotaBinding(t *testing.T, db *sql.DB, apiKeyID string) (apiKeyQuotaBindingRow, bool) {
	t.Helper()
	var row apiKeyQuotaBindingRow
	err := db.QueryRow(`SELECT system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at
		FROM juhe_business.request_quota_hourly_window_scope_bindings
		WHERE source_type = 'api_key' AND source_id = $1`, apiKeyID).Scan(
		&row.ownerID, &row.scopeType, &row.scopeID, &row.sourceType, &row.sourceID, &row.windowHours, &row.createdAt, &row.updatedAt)
	if err == sql.ErrNoRows {
		return apiKeyQuotaBindingRow{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return row, true
}

func countAPIKeyQuotaBindings(t *testing.T, db *sql.DB, apiKeyID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_business.request_quota_hourly_window_scope_bindings
		WHERE source_type = 'api_key' AND source_id = $1`, apiKeyID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func countAPIKeyQuotaDirty(t *testing.T, db *sql.DB, apiKeyID string) (int, int64) {
	t.Helper()
	var count int
	var generation sql.NullInt64
	if err := db.QueryRow(`SELECT COUNT(*), MAX(generation)
		FROM juhe_stats.usage_quota_hourly_window_dirty_scopes
		WHERE scope_type = 'api_key' AND scope_id = $1`, apiKeyID).Scan(&count, &generation); err != nil {
		t.Fatal(err)
	}
	return count, generation.Int64
}

// TestSyncApiKeyScheduleActivationRebuildsHourlyWindowBinding 复现缺陷路径：
// 活跃 Key 的闸门尚未求值（gate NULL）→ 计划开闸 → 绑定行出现且 owner/scope/
// source/window_hours/created_at/updated_at 与 gateway 写侧逐字段一致，同时
// 打脏 statsagg；status 保持 active（同步不改写 status）。
func TestSyncApiKeyScheduleActivationRebuildsHourlyWindowBinding(t *testing.T) {
	store, db, _ := newSweepStore(t)
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	seedApiKeyWithScheduleAndQuota(t, db, "key-hourly", availabilityScheduleJSON, "", "active", apiKeyHourlyQuotaJSON)
	if countAPIKeyQuotaBindings(t, db, "key-hourly") != 0 {
		t.Fatal("闸门未求值时不应有小时额度绑定")
	}

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), atStart, 0, store.APIKeyScheduleActivationHook(nil))
	if err != nil {
		t.Fatal(err)
	}
	if result.Activated != 1 {
		t.Fatalf("result=%+v，期望开闸 1 个 Key", result)
	}
	status, _, _ := readApiKey(t, db, "key-hourly")
	if status != "active" {
		t.Fatalf("status=%q，期望保持 active（同步不改写 status）", status)
	}
	requireGate(t, "key-hourly", readApiKeyGate(t, db, "key-hourly"), "1")

	row, ok := readAPIKeyQuotaBinding(t, db, "key-hourly")
	if !ok {
		t.Fatal("计划开闸后必须重建小时额度窗口绑定")
	}
	want := apiKeyQuotaBindingRow{
		ownerID:     "sys_admin",
		scopeType:   "api_key",
		scopeID:     "key-hourly",
		sourceType:  "api_key",
		sourceID:    "key-hourly",
		windowHours: 6,
		createdAt:   isoMillis(atStart),
		updatedAt:   isoMillis(atStart),
	}
	if row != want {
		t.Fatalf("绑定行=%+v，期望 %+v", row, want)
	}

	dirtyCount, generation := countAPIKeyQuotaDirty(t, db, "key-hourly")
	if dirtyCount != 1 || generation != 1 {
		t.Fatalf("脏范围 count=%d generation=%d，期望 1/1", dirtyCount, generation)
	}
}

// TestSyncApiKeyScheduleActivationBindingIdempotent 覆盖重复开闸：窗口结束关闸
// 后，第二个计划窗口再次开闸时绑定仍只有一行（DELETE + ON CONFLICT），
// created_at / updated_at 刷新为本次重建时间，脏范围 generation 自增；status
// 与闸门解耦，全程保持种入值不变。
func TestSyncApiKeyScheduleActivationBindingIdempotent(t *testing.T) {
	store, db, _ := newSweepStore(t)
	firstStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	seedApiKeyWithScheduleAndQuota(t, db, "key-hourly", availabilityScheduleJSON, "", "active", apiKeyHourlyQuotaJSON)
	hook := store.APIKeyScheduleActivationHook(nil)
	if _, err := store.SyncApiKeyScheduleStatuses(context.Background(), firstStart, 0, hook); err != nil {
		t.Fatal(err)
	}
	firstRow, ok := readAPIKeyQuotaBinding(t, db, "key-hourly")
	if !ok {
		t.Fatal("首次开闸未写绑定")
	}
	// 窗口结束关闸：闸门 1→0，不触发激活 hook，绑定保持不变。
	closeResult, err := store.SyncApiKeyScheduleStatuses(context.Background(), firstStart.Add(9*time.Hour), 0, hook)
	if err != nil {
		t.Fatal(err)
	}
	if closeResult.Disabled != 1 || closeResult.Activated != 0 {
		t.Fatalf("close result=%+v，期望只关闸", closeResult)
	}
	requireGate(t, "key-hourly", readApiKeyGate(t, db, "key-hourly"), "0")
	if status, _, _ := readApiKey(t, db, "key-hourly"); status != "active" {
		t.Fatalf("关闸后 status=%q，期望保持 active", status)
	}

	// 第二个窗口起点：闸门 0→1，再次重建绑定。
	secondStart := firstStart.AddDate(0, 0, 1)
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), secondStart, 0, hook)
	if err != nil {
		t.Fatal(err)
	}
	if result.Activated != 1 {
		t.Fatalf("result=%+v，期望再次开闸 1 个 Key", result)
	}
	requireGate(t, "key-hourly", readApiKeyGate(t, db, "key-hourly"), "1")
	if status, _, _ := readApiKey(t, db, "key-hourly"); status != "active" {
		t.Fatalf("第二次开闸后 status=%q，期望保持 active", status)
	}
	if count := countAPIKeyQuotaBindings(t, db, "key-hourly"); count != 1 {
		t.Fatalf("重复开闸后绑定行=%d，期望 1（不得产生重复行）", count)
	}
	secondRow, ok := readAPIKeyQuotaBinding(t, db, "key-hourly")
	if !ok {
		t.Fatal("重复开闸后绑定行丢失")
	}
	if secondRow.windowHours != firstRow.windowHours {
		t.Fatalf("重复开闸不得改变 window_hours：first=%+v second=%+v", firstRow, secondRow)
	}
	// gateway 写侧是 DELETE 后 INSERT，created_at 与 updated_at 都刷新为本次
	// 重建时间；jobs 重建必须逐字段同语义。
	if secondRow.createdAt != isoMillis(secondStart) || secondRow.updatedAt != isoMillis(secondStart) {
		t.Fatalf("重复开闸的 created_at/updated_at=%q/%q，期望 %q", secondRow.createdAt, secondRow.updatedAt, isoMillis(secondStart))
	}
	dirtyCount, generation := countAPIKeyQuotaDirty(t, db, "key-hourly")
	if dirtyCount != 1 || generation != 2 {
		t.Fatalf("脏范围 count=%d generation=%d，期望 1/2", dirtyCount, generation)
	}
}

// TestSyncApiKeyScheduleDeactivationWritesNoBinding 覆盖计划关闸：active 行的
// 闸门 1（或 NULL）→0 不触发激活 hook，不写绑定、不打脏；status 保持 active。
func TestSyncApiKeyScheduleDeactivationWritesNoBinding(t *testing.T) {
	store, db, _ := newSweepStore(t)
	atEnd := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	seedApiKeyWithScheduleAndQuota(t, db, "key-off", availabilityScheduleJSON, "", "active", apiKeyHourlyQuotaJSON)

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), atEnd, 0, store.APIKeyScheduleActivationHook(nil))
	if err != nil {
		t.Fatal(err)
	}
	if result.Disabled != 1 {
		t.Fatalf("result=%+v，期望关闸 1 个 Key", result)
	}
	status, _, _ := readApiKey(t, db, "key-off")
	if status != "active" {
		t.Fatalf("status=%q，期望保持 active（同步不改写 status）", status)
	}
	requireGate(t, "key-off", readApiKeyGate(t, db, "key-off"), "0")
	if count := countAPIKeyQuotaBindings(t, db, "key-off"); count != 0 {
		t.Fatalf("计划关闸不得写绑定，实际 %d 行", count)
	}
	if dirtyCount, _ := countAPIKeyQuotaDirty(t, db, "key-off"); dirtyCount != 0 {
		t.Fatalf("计划关闸不得打脏，实际 %d 行", dirtyCount)
	}
}

// TestSyncApiKeyScheduleActivationWithoutHourlyQuotaWritesNoBinding 覆盖开闸但
// 无小时额度：只写闸门，DELETE 后无 INSERT，也不打脏；status 保持 active。
func TestSyncApiKeyScheduleActivationWithoutHourlyQuotaWritesNoBinding(t *testing.T) {
	store, db, _ := newSweepStore(t)
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	seedApiKeyWithScheduleAndQuota(t, db, "key-daily", availabilityScheduleJSON, "", "active", `{"daily":{"enabled":true,"limit":40}}`)

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), atStart, 0, store.APIKeyScheduleActivationHook(nil))
	if err != nil {
		t.Fatal(err)
	}
	if result.Activated != 1 {
		t.Fatalf("result=%+v，期望开闸 1 个 Key", result)
	}
	status, _, _ := readApiKey(t, db, "key-daily")
	if status != "active" {
		t.Fatalf("status=%q，期望保持 active（同步不改写 status）", status)
	}
	requireGate(t, "key-daily", readApiKeyGate(t, db, "key-daily"), "1")
	if count := countAPIKeyQuotaBindings(t, db, "key-daily"); count != 0 {
		t.Fatalf("无小时额度不得写绑定，实际 %d 行", count)
	}
	if dirtyCount, _ := countAPIKeyQuotaDirty(t, db, "key-daily"); dirtyCount != 0 {
		t.Fatalf("无小时额度不得打脏，实际 %d 行", dirtyCount)
	}
}

// TestSyncApiKeyScheduleActivationInvalidQuotaJSONSkipsRow 覆盖额度文档无法判定
// 小时窗口（JSON 非法）：该行按无效逐项隔离——不开闸（否则小时额度不可判定）、
// 闸门保持 NULL、不写绑定；同批其它 Key 照常开闸。
func TestSyncApiKeyScheduleActivationInvalidQuotaJSONSkipsRow(t *testing.T) {
	store, db, _ := newSweepStore(t)
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	seedApiKeyWithScheduleAndQuota(t, db, "key-bad", availabilityScheduleJSON, "", "active", `{"hourly":`)
	seedApiKeyWithScheduleAndQuota(t, db, "key-ok", availabilityScheduleJSON, "", "active", apiKeyHourlyQuotaJSON)

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), atStart, 0, store.APIKeyScheduleActivationHook(nil))
	if err != nil {
		t.Fatalf("单行额度文档损坏不得让整批同步失败: %v", err)
	}
	if result.Invalid != 1 || len(result.InvalidIDs) != 1 || result.InvalidIDs[0] != "key-bad" {
		t.Fatalf("result=%+v，期望 key-bad 计为 invalid", result)
	}
	if result.Activated != 1 || len(result.ChangedIDs) != 1 || result.ChangedIDs[0] != "key-ok" {
		t.Fatalf("result=%+v，期望同批 key-ok 正常开闸", result)
	}
	badStatus, badNext, _ := readApiKey(t, db, "key-bad")
	if badStatus != "active" {
		t.Fatalf("额度文档损坏只阻止开闸，不改写 status：status=%q want active", badStatus)
	}
	requireGate(t, "key-bad", readApiKeyGate(t, db, "key-bad"), "NULL")
	if badNext != "2026-09-07T18:00:00.000Z" {
		t.Fatalf("无效行仍应推进 next_check=%q want 窗口结束", badNext)
	}
	if okStatus, _, _ := readApiKey(t, db, "key-ok"); okStatus != "active" {
		t.Fatalf("key-ok status=%q，期望保持 active", okStatus)
	}
	requireGate(t, "key-ok", readApiKeyGate(t, db, "key-ok"), "1")
	if count := countAPIKeyQuotaBindings(t, db, "key-bad"); count != 0 {
		t.Fatalf("额度文档损坏的 Key 不得写绑定，实际 %d 行", count)
	}
	if count := countAPIKeyQuotaBindings(t, db, "key-ok"); count != 1 {
		t.Fatalf("同批正常 Key 必须写入绑定，实际 %d 行", count)
	}
}
