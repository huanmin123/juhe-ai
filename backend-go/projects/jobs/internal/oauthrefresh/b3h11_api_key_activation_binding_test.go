package oauthrefresh

// B3-H11 回归：API Key 可用性排期同步把 Key 从非 active 翻为 active 时，必须在
// 同一事务重建 request_quota_hourly_window_scope_bindings 的 api_key 绑定，并在
// 提交后给 statsagg 打脏；否则小时额度窗口永远缺失（静默 fail-open）。
//
// 覆盖：
//   - 停用态创建（无绑定）→ 计划激活 → 绑定行逐字段正确 + 脏范围出现；
//   - 重复激活幂等（DELETE + ON CONFLICT，仅一行，generation 自增）；
//   - 计划停用 / 无小时额度不写绑定、不打脏；
//   - 额度文档无法判定小时窗口时按行隔离：该 Key 不激活、不写绑定，同批
//     其它 Key 照常完成（避免一行坏数据卡死整批状态收敛）。

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
// 停用态创建（创建时 active=false，不写绑定）→ 计划窗口起点激活 → 绑定行
// 出现且 owner/scope/source/window_hours/created_at/updated_at 与 gateway 写侧
// 逐字段一致，同时打脏 statsagg。
func TestSyncApiKeyScheduleActivationRebuildsHourlyWindowBinding(t *testing.T) {
	store, db, _ := newSweepStore(t)
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	seedApiKeyWithScheduleAndQuota(t, db, "key-hourly", availabilityScheduleJSON, "", "disabled", apiKeyHourlyQuotaJSON)
	if countAPIKeyQuotaBindings(t, db, "key-hourly") != 0 {
		t.Fatal("停用态创建不应有小时额度绑定")
	}

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), atStart, 0, store.APIKeyScheduleActivationHook(nil))
	if err != nil {
		t.Fatal(err)
	}
	if result.Activated != 1 {
		t.Fatalf("result=%+v，期望激活 1 个 Key", result)
	}
	status, _, _ := readApiKey(t, db, "key-hourly")
	if status != "active" {
		t.Fatalf("status=%q，期望 active", status)
	}

	row, ok := readAPIKeyQuotaBinding(t, db, "key-hourly")
	if !ok {
		t.Fatal("计划激活后必须重建小时额度窗口绑定")
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

// TestSyncApiKeyScheduleActivationBindingIdempotent 覆盖重复激活：第二个计划
// 窗口起点再次激活同一 Key 时，绑定仍只有一行（DELETE + ON CONFLICT），
// created_at 保留首次值，updated_at 推进，脏范围 generation 自增。
func TestSyncApiKeyScheduleActivationBindingIdempotent(t *testing.T) {
	store, db, _ := newSweepStore(t)
	firstStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	seedApiKeyWithScheduleAndQuota(t, db, "key-hourly", availabilityScheduleJSON, "", "disabled", apiKeyHourlyQuotaJSON)
	hook := store.APIKeyScheduleActivationHook(nil)
	if _, err := store.SyncApiKeyScheduleStatuses(context.Background(), firstStart, 0, hook); err != nil {
		t.Fatal(err)
	}
	firstRow, ok := readAPIKeyQuotaBinding(t, db, "key-hourly")
	if !ok {
		t.Fatal("首次激活未写绑定")
	}

	// 第二个窗口起点：把行恢复为待激活态（等价于上一日窗口结束后的落库状态），
	// 使用不同 date key 的激活事件，确保 hook 再次执行。
	if _, err := db.Exec(`UPDATE juhe_business.api_keys SET status = 'disabled', availability_schedule_next_check_at = NULL WHERE id = 'key-hourly'`); err != nil {
		t.Fatal(err)
	}
	secondStart := firstStart.AddDate(0, 0, 1)
	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), secondStart, 0, hook)
	if err != nil {
		t.Fatal(err)
	}
	if result.Activated != 1 {
		t.Fatalf("result=%+v，期望再次激活 1 个 Key", result)
	}
	if count := countAPIKeyQuotaBindings(t, db, "key-hourly"); count != 1 {
		t.Fatalf("重复激活后绑定行=%d，期望 1（不得产生重复行）", count)
	}
	secondRow, ok := readAPIKeyQuotaBinding(t, db, "key-hourly")
	if !ok {
		t.Fatal("重复激活后绑定行丢失")
	}
	if secondRow.windowHours != firstRow.windowHours {
		t.Fatalf("重复激活不得改变 window_hours：first=%+v second=%+v", firstRow, secondRow)
	}
	// gateway 写侧是 DELETE 后 INSERT，created_at 与 updated_at 都刷新为本次
	// 重建时间；jobs 重建必须逐字段同语义。
	if secondRow.createdAt != isoMillis(secondStart) || secondRow.updatedAt != isoMillis(secondStart) {
		t.Fatalf("重复激活的 created_at/updated_at=%q/%q，期望 %q", secondRow.createdAt, secondRow.updatedAt, isoMillis(secondStart))
	}
	dirtyCount, generation := countAPIKeyQuotaDirty(t, db, "key-hourly")
	if dirtyCount != 1 || generation != 2 {
		t.Fatalf("脏范围 count=%d generation=%d，期望 1/2", dirtyCount, generation)
	}
}

// TestSyncApiKeyScheduleDeactivationWritesNoBinding 覆盖计划停用：active → disabled
// 不触发激活 hook，不写绑定、不打脏（保持既有转换语义）。
func TestSyncApiKeyScheduleDeactivationWritesNoBinding(t *testing.T) {
	store, db, _ := newSweepStore(t)
	atEnd := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	seedApiKeyWithScheduleAndQuota(t, db, "key-off", availabilityScheduleJSON, "", "active", apiKeyHourlyQuotaJSON)

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), atEnd, 0, store.APIKeyScheduleActivationHook(nil))
	if err != nil {
		t.Fatal(err)
	}
	if result.Disabled != 1 {
		t.Fatalf("result=%+v，期望停用 1 个 Key", result)
	}
	if count := countAPIKeyQuotaBindings(t, db, "key-off"); count != 0 {
		t.Fatalf("计划停用不得写绑定，实际 %d 行", count)
	}
	if dirtyCount, _ := countAPIKeyQuotaDirty(t, db, "key-off"); dirtyCount != 0 {
		t.Fatalf("计划停用不得打脏，实际 %d 行", dirtyCount)
	}
}

// TestSyncApiKeyScheduleActivationWithoutHourlyQuotaWritesNoBinding 覆盖激活但
// 无小时额度：只翻状态，DELETE 后无 INSERT，也不打脏。
func TestSyncApiKeyScheduleActivationWithoutHourlyQuotaWritesNoBinding(t *testing.T) {
	store, db, _ := newSweepStore(t)
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	seedApiKeyWithScheduleAndQuota(t, db, "key-daily", availabilityScheduleJSON, "", "disabled", `{"daily":{"enabled":true,"limit":40}}`)

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), atStart, 0, store.APIKeyScheduleActivationHook(nil))
	if err != nil {
		t.Fatal(err)
	}
	if result.Activated != 1 {
		t.Fatalf("result=%+v，期望激活 1 个 Key", result)
	}
	status, _, _ := readApiKey(t, db, "key-daily")
	if status != "active" {
		t.Fatalf("status=%q，期望 active", status)
	}
	if count := countAPIKeyQuotaBindings(t, db, "key-daily"); count != 0 {
		t.Fatalf("无小时额度不得写绑定，实际 %d 行", count)
	}
	if dirtyCount, _ := countAPIKeyQuotaDirty(t, db, "key-daily"); dirtyCount != 0 {
		t.Fatalf("无小时额度不得打脏，实际 %d 行", dirtyCount)
	}
}

// TestSyncApiKeyScheduleActivationInvalidQuotaJSONSkipsRow 覆盖额度文档无法判定
// 小时窗口（JSON 非法）：该行按无效逐项隔离——不翻为 active（否则小时额度
// 不可判定）、保持 disabled、不写绑定，且同批其它 Key 的转换照常完成。
func TestSyncApiKeyScheduleActivationInvalidQuotaJSONSkipsRow(t *testing.T) {
	store, db, _ := newSweepStore(t)
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	seedApiKeyWithScheduleAndQuota(t, db, "key-bad", availabilityScheduleJSON, "", "disabled", `{"hourly":`)
	seedApiKeyWithScheduleAndQuota(t, db, "key-ok", availabilityScheduleJSON, "", "disabled", apiKeyHourlyQuotaJSON)

	result, err := store.SyncApiKeyScheduleStatuses(context.Background(), atStart, 0, store.APIKeyScheduleActivationHook(nil))
	if err != nil {
		t.Fatalf("单行额度文档损坏不得让整批同步失败: %v", err)
	}
	if result.Invalid != 1 || len(result.InvalidIDs) != 1 || result.InvalidIDs[0] != "key-bad" {
		t.Fatalf("result=%+v，期望 key-bad 计为 invalid", result)
	}
	if result.Activated != 1 || len(result.ChangedIDs) != 1 || result.ChangedIDs[0] != "key-ok" {
		t.Fatalf("result=%+v，期望同批 key-ok 正常激活", result)
	}
	badStatus, _, _ := readApiKey(t, db, "key-bad")
	if badStatus != "disabled" {
		t.Fatalf("额度文档损坏的 Key 必须保持 disabled，status=%q", badStatus)
	}
	if count := countAPIKeyQuotaBindings(t, db, "key-bad"); count != 0 {
		t.Fatalf("额度文档损坏的 Key 不得写绑定，实际 %d 行", count)
	}
	if count := countAPIKeyQuotaBindings(t, db, "key-ok"); count != 1 {
		t.Fatalf("同批正常 Key 必须写入绑定，实际 %d 行", count)
	}
}
