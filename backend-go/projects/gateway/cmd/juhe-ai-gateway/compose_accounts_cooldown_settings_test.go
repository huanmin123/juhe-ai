package main

// compose_accounts_cooldown_settings_test.go —— compose.go
// runtimeCooldownSettingsAdapter（accounts RuntimeCooldownSettings 端口，
// patch_runtime_state.go rate_limited re-arm 冷却）的表驱动接线测试。
//
// 覆盖臂（真实 settings 仓 seam，w1_compose_arms2_test.go B3 同款装配）：
//   1. 种子默认值 2（schema-defaults.ts:599）；
//   2. 显式有值（store.Update float64(30) → 30，管理面 gateway-core 分区
//      同键）；
//   3. 缺行回退：该键不在 compatibleSystemSettingDefaults
//      （settings/store.go），SQL 删行使 assertAllSettingsPresent 让 Load
//      整体报错 → 回退 accounts 包 schema fallback 2
//      （cmd 侧镜像 runtimeCooldownFallbackMinutes）；
//   4. 非法值回退：SQL 直写越界值 5000（绕过 Update 校验）→
//      normalizeSystemSetting 报错 → Load 失败 → 回退 2；
//   5. Load 失败回退：DROP TABLE → 查询错误 → 回退 2。
//
// 步进时钟保证每次读取都越过 settings.Load 的 60s 快照缓存
// （settingsCacheTTL），三臂都打在真实 SQL 状态上。

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/settings"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"

	_ "modernc.org/sqlite"
)

// newCooldownSettingsAdapterStack 提供真实 settings 仓 + 步进时钟。
func newCooldownSettingsAdapterStack(t *testing.T) (*settings.Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "cooldown-settings.sqlite3"))
	if err != nil {
		t.Fatalf("打开临时库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := bootstrap.EnsureSQLiteSchema(context.Background(), bootstrap.SQLiteSchemaBusiness, db); err != nil {
		t.Fatalf("ensure 业务 schema: %v", err)
	}
	if _, err := bootstrap.SeedSQLiteBusiness(context.Background(), db, bootstrap.SeedOptions{Secret: "cooldown-settings-secret"}); err != nil {
		t.Fatalf("seed 业务库: %v", err)
	}
	base := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)
	step := 0
	now := func() time.Time {
		step++
		return base.Add(time.Duration(step) * 2 * time.Minute)
	}
	store, err := settings.NewStore(db, false, now, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store, db
}

func TestRuntimeCooldownSettingsAdapterArms(t *testing.T) {
	store, db := newCooldownSettingsAdapterStack(t)
	adapter := runtimeCooldownSettingsAdapter{settings: store}
	const fallback = runtimeCooldownFallbackMinutes
	if fallback != 2 {
		t.Fatalf("fallback 常量 = %d，必须与 accounts 包 schema fallback 2 一致", fallback)
	}

	t.Run("seed default resolves 2", func(t *testing.T) {
		if got := adapter.DefaultTemporaryUnschedulableMinutes(); got != 2 {
			t.Fatalf("种子默认值 = %d，want 2", got)
		}
	})

	t.Run("explicit value resolves through", func(t *testing.T) {
		if _, err := store.Update(context.Background(), map[string]any{"defaultTemporaryUnschedulableMinutes": float64(30)}); err != nil {
			t.Fatalf("Update defaultTemporaryUnschedulableMinutes: %v", err)
		}
		if got := adapter.DefaultTemporaryUnschedulableMinutes(); got != 30 {
			t.Fatalf("显式设置值 = %d，want 30", got)
		}
	})

	t.Run("missing row falls back", func(t *testing.T) {
		if _, err := db.Exec(`DELETE FROM system_settings WHERE system_account_id = ? AND key = ?`,
			settings.SystemSettingsAccountID, "defaultTemporaryUnschedulableMinutes"); err != nil {
			t.Fatalf("删除设置行: %v", err)
		}
		// 缺行使 assertAllSettingsPresent 让 Load 整体报错（该键不在
		// compatibleSystemSettingDefaults），适配器必须回退而不是 0。
		if _, err := store.Load(context.Background()); err == nil {
			t.Fatal("预置条件失效：缺行必须让 settings.Load 报错")
		}
		if got := adapter.DefaultTemporaryUnschedulableMinutes(); got != fallback {
			t.Fatalf("缺行回退 = %d，want %d", got, fallback)
		}
	})

	t.Run("out-of-range value falls back", func(t *testing.T) {
		if _, err := db.Exec(`INSERT INTO system_settings (system_account_id, key, value_json, updated_at)
			VALUES (?, ?, ?, ?) ON CONFLICT(system_account_id, key) DO UPDATE SET value_json = excluded.value_json`,
			settings.SystemSettingsAccountID, "defaultTemporaryUnschedulableMinutes", "5000", "2026-09-20T08:00:00.000Z"); err != nil {
			t.Fatalf("直写越界值: %v", err)
		}
		if _, err := store.Load(context.Background()); err == nil {
			t.Fatal("预置条件失效：越界值必须让 settings.Load 报错")
		}
		if got := adapter.DefaultTemporaryUnschedulableMinutes(); got != fallback {
			t.Fatalf("越界值回退 = %d，want %d", got, fallback)
		}
	})

	t.Run("load failure falls back", func(t *testing.T) {
		if _, err := db.Exec(`DROP TABLE system_settings`); err != nil {
			t.Fatalf("drop system_settings: %v", err)
		}
		if got := adapter.DefaultTemporaryUnschedulableMinutes(); got != fallback {
			t.Fatalf("Load 失败回退 = %d，want %d", got, fallback)
		}
	})
}
