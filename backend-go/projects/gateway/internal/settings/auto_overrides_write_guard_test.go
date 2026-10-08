// 设计 §7 写面收口：upstreamClientVersionAutoOverrides 加入白名单与 spec 后
// GET 可见，但管理端 v1 不可写（jobs 跟版任务独占写入）。本文件钉桩：
//   - Update（legacy 全量 PATCH 写路径）与 UpdateSection（分区写路径）携带
//     自动键 → ValidationError，且不落库；
//   - Load 与直读 UpstreamClientVersionAutoOverrides 仍正常返回该键值；
//   - 手动键 upstreamClientVersionOverrides 的写入行为不变。
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const autoOverridesWriteRejection = "客户端版本自动覆盖由系统任务维护，不支持通过管理接口写入"

func newAutoOverridesGuardDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:settings-auto-guard-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (system_account_id, key))`); err != nil {
		t.Fatal(err)
	}
	// 成功的写路径以全量快照回读收尾（refreshSystemCache →
	// assertAllSettingsPresent），库内必须有白名单全键。整型键取 spec 上界
	// （必在 [min,max] 内），时区、采样率与对象键按校验器认可的形状补齐。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, key := range SystemSettingKeys {
		valueJSON := "1"
		switch {
		case key == "usageStatsTimezone":
			valueJSON = `"UTC"`
		case key == "auditLogSuccessSampleRate":
			valueJSON = "1"
		case key == upstreamClientVersionOverridesKey || key == upstreamClientVersionAutoOverridesKey:
			valueJSON = "{}"
		default:
			if spec, ok := systemSettingSpecs[key]; ok && spec.integer {
				valueJSON = strconv.Itoa(spec.max)
			}
		}
		if _, execErr := db.Exec(`INSERT INTO system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', ?, ?, ?)`, key, valueJSON, now); execErr != nil {
			t.Fatal(execErr)
		}
	}
	return db
}

func replaceSettingRow(t *testing.T, db *sql.DB, key, valueJSON string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE system_settings SET value_json=? WHERE system_account_id='sys_admin' AND key=?`, valueJSON, key); err != nil {
		t.Fatal(err)
	}
}

func storedSettingJSON(t *testing.T, db *sql.DB, key string) string {
	t.Helper()
	var value string
	err := db.QueryRow(`SELECT value_json FROM system_settings WHERE system_account_id='sys_admin' AND key=?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// TestAutoOverridesKeyRejectedOnUpdate：全量写路径携带自动键被拒绝且不落库。
func TestAutoOverridesKeyRejectedOnUpdate(t *testing.T) {
	db := newAutoOverridesGuardDB(t)
	store, err := NewStore(db, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(context.Background(), map[string]any{
		upstreamClientVersionAutoOverridesKey: map[string]any{"codex": "9.9.9"},
	})
	validation, ok := err.(*ValidationError)
	if !ok || validation.Message != autoOverridesWriteRejection {
		t.Fatalf("Update 携带自动键必须返回写拒绝 ValidationError，实际 %v", err)
	}
	if got := storedSettingJSON(t, db, upstreamClientVersionAutoOverridesKey); got != "{}" {
		t.Fatalf("被拒绝的写入不得改动既有行，实际 %q", got)
	}
}

// TestAutoOverridesKeyRejectedOnUpdateSection：分区写路径同一收口。自动键不在
// 任何分区键表内（目录不收录即「不允许的字段」），这里把含自动键的分区临时注入
// 目录，证明即使目录扩张把该键纳入分区，写守卫仍先于分区校验拒绝它。
func TestAutoOverridesKeyRejectedOnUpdateSection(t *testing.T) {
	db := newAutoOverridesGuardDB(t)
	store, err := NewStore(db, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	const sectionKey = "auto-overrides-guard"
	ManagementSettingsSectionCatalog[sectionKey] = ManagementSettingsSection{
		Domain: settingsSectionDomainSystem,
		Keys:   []string{upstreamClientVersionAutoOverridesKey},
	}
	t.Cleanup(func() { delete(ManagementSettingsSectionCatalog, sectionKey) })

	_, err = store.UpdateSection(context.Background(), sectionKey, map[string]any{
		upstreamClientVersionAutoOverridesKey: map[string]any{"codex": "9.9.9"},
	})
	validation, ok := err.(*ValidationError)
	if !ok || validation.Message != autoOverridesWriteRejection {
		t.Fatalf("UpdateSection 携带自动键必须返回写拒绝 ValidationError，实际 %v", err)
	}
	if got := storedSettingJSON(t, db, upstreamClientVersionAutoOverridesKey); got != "{}" {
		t.Fatalf("被拒绝的分区写入不得改动既有行，实际 %q", got)
	}
}

// TestAutoOverridesKeyReadableViaLoadAndDirectRead：拒绝只发生在写路径，
// Load 与直读函数仍正常返回自动键值。
func TestAutoOverridesKeyReadableViaLoadAndDirectRead(t *testing.T) {
	db := newAutoOverridesGuardDB(t)
	replaceSettingRow(t, db, upstreamClientVersionAutoOverridesKey, `{"codex":"1.2.3","claudeCode":"2.0.1"}`)
	store, err := NewStore(db, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load 不得因自动键失败: %v", err)
	}
	loaded, ok := snapshot[upstreamClientVersionAutoOverridesKey].(map[string]any)
	if !ok || loaded["codex"] != "1.2.3" || loaded["claudeCode"] != "2.0.1" {
		t.Fatalf("Load 必须返回自动键存储值，实际 %#v", snapshot[upstreamClientVersionAutoOverridesKey])
	}

	direct, err := store.UpstreamClientVersionAutoOverrides(context.Background())
	if err != nil {
		t.Fatalf("直读不得失败: %v", err)
	}
	if direct["codex"] != "1.2.3" || direct["claudeCode"] != "2.0.1" || len(direct) != 2 {
		t.Fatalf("直读必须返回过滤后的自动键值，实际 %v", direct)
	}
}

// TestManualOverridesWriteUnaffected：手动键写入行为不变（对照守卫不误伤）。
func TestManualOverridesWriteUnaffected(t *testing.T) {
	db := newAutoOverridesGuardDB(t)
	store, err := NewStore(db, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(context.Background(), map[string]any{
		upstreamClientVersionOverridesKey: map[string]any{"codex": "0.160.0"},
	}); err != nil {
		t.Fatalf("手动键写入不得被拒绝: %v", err)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(storedSettingJSON(t, db, upstreamClientVersionOverridesKey)), &stored); err != nil {
		t.Fatal(err)
	}
	if stored["codex"] != "0.160.0" {
		t.Fatalf("手动键必须落库，实际 %v", stored)
	}
}
