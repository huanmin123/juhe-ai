package accounts

// w13g oauth 用量快照、凭据合并与锁状态补测。
//
// 不可达登记（w13g）：
// - oauth_usage_snapshot.go:127-130 rows.Scan err、133-136
//   oauthUsageSnapshotFromRow err（DB 路径）、142-145 rows.Err：单连接下
//   Next/Scan/Err 之间无故障注入口（纯函数侧已覆盖同一解析矩阵）。
// - oauth_usage_snapshot.go:226-228 reset_after_seconds 基准时间解析错误臂：
//   updatedAt 在 oauthUsageSnapshotFromRow 入口已通过 requiredRFC3339Instant
//   校验，此处解析恒成功。

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestW13GOAuthUsageSnapshotFromRowArms(t *testing.T) {
	// updated_at 非法。
	if _, err := oauthUsageSnapshotFromRow("", "", "", "", "", "", "", "bad"); err == nil ||
		!strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("坏 updated_at：%v", err)
	}
	// snapshot_json 不可解析 → 跳过该行。
	out, err := oauthUsageSnapshotFromRow("src", "{not-json", "", "", "", "", "", "2026-01-01T00:00:00Z")
	if err != nil || out != nil {
		t.Fatalf("坏 JSON 应跳过：%v %v", out, err)
	}
	// 全空。
	out, err = oauthUsageSnapshotFromRow("", "", "", "", "", "", "", "2026-01-01T00:00:00Z")
	if err != nil || out == nil {
		t.Fatalf("全空行：%v %v", out, err)
	}
	if out.Kind != "openai_codex" || out.FiveHour != nil || out.SevenDay != nil {
		t.Fatalf("全空快照：%+v", out)
	}
	// last_attempt_at 非法。
	if _, err := oauthUsageSnapshotFromRow("", "", "", "bad", "", "", "", "2026-01-01T00:00:00Z"); err == nil {
		t.Fatal("坏 last_attempt_at 应报错")
	}
	// last_success_at 非法。
	if _, err := oauthUsageSnapshotFromRow("", "", "", "", "bad", "", "", "2026-01-01T00:00:00Z"); err == nil {
		t.Fatal("坏 last_success_at 应报错")
	}
	// next_refresh_after 非法。
	if _, err := oauthUsageSnapshotFromRow("", "", "", "", "", "bad", "", "2026-01-01T00:00:00Z"); err == nil {
		t.Fatal("坏 next_refresh_after 应报错")
	}
	// 全字段 + source 落 snapshot。
	out, err = oauthUsageSnapshotFromRow("", `{"source":"snapshot-src","codex_5h_used_percent":80,
		"codex_5h_reset_at":"2099-01-01T00:00:00Z","codex_5h_window_minutes":300,
		"codex_7d_used_percent":10}`, "ok", "2026-01-02T00:00:00Z", "2026-01-03T00:00:00Z",
		"2026-01-04T00:00:00Z", "boom", "2026-01-01T00:00:00Z")
	if err != nil || out == nil {
		t.Fatalf("完整快照：%v %v", out, err)
	}
	if out.Source == nil || *out.Source != "snapshot-src" || out.RefreshStatus == nil ||
		out.LastErrorMessage == nil || *out.LastErrorMessage != "boom" {
		t.Fatalf("快照头字段：%+v", out)
	}
	if out.FiveHour == nil || out.FiveHour.Utilization != 80 || out.FiveHour.WindowMinutes == nil {
		t.Fatalf("5h 窗口：%+v", out.FiveHour)
	}
	if out.SevenDay == nil || out.SevenDay.ResetsAt != nil {
		t.Fatalf("7d 窗口：%+v", out.SevenDay)
	}
	// source 列优先于 snapshot。
	out, err = oauthUsageSnapshotFromRow("column-src", `{"source":"snapshot-src"}`, "", "", "", "", "", "2026-01-01T00:00:00Z")
	if err != nil || out.Source == nil || *out.Source != "column-src" {
		t.Fatalf("source 列优先：%+v %v", out, err)
	}
}

func TestW13GOAuthUsageWindowArms(t *testing.T) {
	// 无 utilization → nil。
	if out, err := oauthUsageWindowFromSnapshot(map[string]any{}, "5h", "2026-01-01T00:00:00Z"); err != nil || out != nil {
		t.Fatalf("无 utilization：%v %v", out, err)
	}
	// reset_at 非字符串。
	bad, err := oauthUsageWindowFromSnapshot(map[string]any{
		"codex_5h_used_percent": float64(1), "codex_5h_reset_at": 5,
	}, "5h", "2026-01-01T00:00:00Z")
	if err == nil || bad != nil {
		t.Fatalf("reset_at 非字符串：%v %v", bad, err)
	}
	// reset_at 非法时间。
	bad, err = oauthUsageWindowFromSnapshot(map[string]any{
		"codex_5h_used_percent": float64(1), "codex_5h_reset_at": "bad",
	}, "5h", "2026-01-01T00:00:00Z")
	if err == nil || bad != nil {
		t.Fatalf("reset_at 非法时间：%v %v", bad, err)
	}
	// reset_after_seconds 正数 → 计算重置点；elapsed 重置清零。
	elapsed, err := oauthUsageWindowFromSnapshot(map[string]any{
		"codex_5h_used_percent": float64(50), "codex_5h_reset_after_seconds": float64(1),
	}, "5h", "2020-01-01T00:00:00Z")
	if err != nil || elapsed == nil || elapsed.ResetsAt == nil {
		t.Fatalf("elapsed 重置：%v %v", elapsed, err)
	}
	if elapsed.Utilization != 0 || elapsed.RemainingSeconds != 0 {
		t.Fatalf("elapsed 应清零：%+v", elapsed)
	}
	// 未来重置点 → remainingSeconds > 0。
	future, err := oauthUsageWindowFromSnapshot(map[string]any{
		"codex_7d_used_percent": float64(25), "codex_7d_reset_after_seconds": float64(3600),
	}, "7d", time.Now().UTC().Format(time.RFC3339))
	if err != nil || future == nil || future.RemainingSeconds <= 0 {
		t.Fatalf("未来重置：%+v %v", future, err)
	}
	// numberFromSnapshot / optionalSnapshotString。
	if number, ok := numberFromSnapshot(float64(3)); !ok || *number != 3 {
		t.Fatal("合法数字应返回")
	}
	for _, bad := range []any{"x", nil} {
		if _, ok := numberFromSnapshot(bad); ok {
			t.Fatalf("非数字应拒绝：%v", bad)
		}
	}
	if optionalSnapshotString("x") != "x" || optionalSnapshotString(1) != "" {
		t.Fatal("optionalSnapshotString")
	}
	// requiredRFC3339Instant 拒绝坏值。
	if _, err := requiredRFC3339Instant("nope", "label"); err == nil {
		t.Fatal("坏时间应报错")
	}
}

func TestW13GOAuthUsageHydrateArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedM11Account(t, "acc-w13g-oauth", adminID, "w13g-oauth", "oauth", "active", Credentials{
		"access_token": "at", "base_url": "https://api.example.com/v1",
	})
	now := "2026-01-01T00:00:00Z"
	env.exec(t, `INSERT INTO account_usage_snapshots (system_account_id, account_id, kind, source, snapshot_json,
		refresh_status, created_at, updated_at) VALUES (?, 'acc-w13g-oauth', 'openai_codex', 'codex', '{}', 'ok', ?, ?)`,
		adminID, now, now)

	// 命中：gpt + oauth 账户读取快照。
	items := []ListItem{{ID: "acc-w13g-oauth", ProviderCode: "gpt", Type: "oauth"}}
	if err := env.store.hydrateOAuthUsageSnapshots(context.Background(), items); err != nil {
		t.Fatalf("hydrate 失败：%v", err)
	}
	if items[0].OAuthUsage == nil || items[0].OAuthUsage.Source == nil || *items[0].OAuthUsage.Source != "codex" {
		t.Fatalf("快照未回填：%+v", items[0].OAuthUsage)
	}

	// 实例账户：fact id 用源账户。
	sourceID := "acc-w13g-oauth"
	instance := ListItem{ID: "acc-w13g-oauth-inst", ProviderCode: "gpt", Type: "oauth",
		AuthorizationInstanceSourceAccountID: &sourceID}
	roster := []ListItem{instance, {ID: "acc-w13g-other", ProviderCode: "gpt", Type: "api_key"}}
	if err := env.store.hydrateOAuthUsageSnapshots(context.Background(), roster); err != nil {
		t.Fatalf("实例 hydrate 失败：%v", err)
	}
	if roster[0].OAuthUsage == nil {
		t.Fatal("实例应回填源账户快照")
	}
	if roster[1].OAuthUsage != nil {
		t.Fatal("api_key 不应回填")
	}

	// 空列表与空 fact 集。
	if err := env.store.hydrateOAuthUsageSnapshots(context.Background(), nil); err != nil {
		t.Fatalf("空列表：%v", err)
	}
	if err := env.store.hydrateOAuthUsageSnapshots(context.Background(),
		[]ListItem{{ID: "x", ProviderCode: "anthropic", Type: "api_key"}}); err != nil {
		t.Fatalf("无命中：%v", err)
	}

	// 快照表缺失 → 传播。
	env.exec(t, `DROP TABLE account_usage_snapshots`)
	if err := env.store.hydrateOAuthUsageSnapshots(context.Background(), items); err == nil {
		t.Fatal("快照表缺失应报错")
	}
}

func TestW13GMergeCredentialsForUpdate(t *testing.T) {
	// patch：null 删除键。
	merged := applyAccountCredentialsPatch(Credentials{"api_key": "sk", "base_url": "u"},
		Credentials{"base_url": nil})
	if _, exists := merged["base_url"]; exists {
		t.Fatal("null patch 应删除键")
	}
	if merged["api_key"] != "sk" {
		t.Fatalf("其余键保留：%v", merged)
	}
	// api_key：单 key 替换池。
	merged = mergeAccountCredentialsForUpdate("api_key",
		Credentials{"api_keys": []any{"a", "b"}, "api_key_strategy": "round_robin", "api_key": "old"},
		Credentials{"api_key": "new"})
	if _, exists := merged["api_keys"]; exists || merged["api_key"] != "new" {
		t.Fatalf("单 key 替换池：%v", merged)
	}
	// api_key：池替换单 key，策略与权重保留 current。
	merged = mergeAccountCredentialsForUpdate("api_key",
		Credentials{"api_key": "old", "api_key_strategy": "round_robin",
			"api_key_weights": []any{float64(2)}},
		Credentials{"api_keys": []any{"a", "b"}})
	if merged["api_key"] != nil || merged["api_key_strategy"] != "round_robin" {
		t.Fatalf("池替换单 key：%v", merged)
	}
	// api_key：均未提供 → 保留 current 键。
	merged = mergeAccountCredentialsForUpdate("api_key",
		Credentials{"api_key": "old", "api_keys": []any{"a"}},
		Credentials{"base_url": "u"})
	if merged["api_key"] != "old" || merged["api_key"] == nil {
		t.Fatalf("保留 current：%v", merged)
	}
	// oauth：可选字段保留。
	merged = mergeAccountCredentialsForUpdate("oauth",
		Credentials{"access_token": "old-at", "scope": "read"},
		Credentials{})
	if merged["access_token"] != "old-at" || merged["scope"] != "read" {
		t.Fatalf("oauth 保留：%v", merged)
	}
	// google_oauth：可选字段保留。
	merged = mergeAccountCredentialsForUpdate("google_oauth",
		Credentials{"refresh_token": "rt", "drive_storage_limit": float64(3)},
		Credentials{})
	if merged["refresh_token"] != "rt" || merged["drive_storage_limit"] != float64(3) {
		t.Fatalf("google 保留：%v", merged)
	}
	// 空数组/空白文本不覆盖 current。
	merged = mergeAccountCredentialsForUpdate("api_key",
		Credentials{"api_key": "old"},
		Credentials{"api_key": "  ", "supported_endpoint_modes": []any{}})
	if merged["api_key"] != "old" {
		t.Fatalf("空白不覆盖：%v", merged)
	}
}

func TestW13GSetLockArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13g-lock", adminID, "w13g-lock", "active")
	admin := AccessScope{ViewerID: adminID, IsAdmin: true}

	// 空 scope（非管理员无 viewer）→ (nil,nil)。
	if out, err := env.store.SetLock(context.Background(), SetLockInput{AccountID: "acc-w13g-lock", Enabled: true}, AccessScope{}); err != nil || out != nil {
		t.Fatalf("空 scope：%v %v", out, err)
	}
	// revision 冲突。
	if _, err := env.store.SetLock(context.Background(), SetLockInput{
		AccountID: "acc-w13g-lock", Enabled: true, ExpectedConfigRevision: 99,
	}, admin); err == nil || !strings.Contains(err.Error(), "并发变更") {
		t.Fatalf("revision 冲突：%v", err)
	}
	// lock 世代冲突。
	generation := int64(7)
	if _, err := env.store.SetLock(context.Background(), SetLockInput{
		AccountID: "acc-w13g-lock", Enabled: true, ExpectedLockGeneration: &generation,
	}, admin); err == nil || !strings.Contains(err.Error(), "锁定状态") && !strings.Contains(err.Error(), "并发变更") {
		t.Fatalf("世代冲突：%v", err)
	}
	// 非法 death timeout / retry interval。
	badTimeout := 1
	if _, err := env.store.SetLock(context.Background(), SetLockInput{
		AccountID: "acc-w13g-lock", Enabled: true, LockDeathTimeoutSeconds: &badTimeout,
	}, admin); err == nil {
		t.Fatal("非法 death timeout 应报错")
	}
	badInterval := 1
	if _, err := env.store.SetLock(context.Background(), SetLockInput{
		AccountID: "acc-w13g-lock", Enabled: true, LockRetryIntervalSeconds: &badInterval,
	}, admin); err == nil {
		t.Fatal("非法 retry interval 应报错")
	}
	// accounts 表缺失 → 查询错误。
	env.exec(t, `DROP TABLE accounts`)
	if _, err := env.store.SetLock(context.Background(), SetLockInput{AccountID: "acc-w13g-lock", Enabled: true}, admin); err == nil {
		t.Fatal("accounts 缺失应报错")
	}
	// db 关闭 → BeginTx 失败。
	t.Run("closed-db", func(t *testing.T) {
		env2 := newTestEnv(t)
		admin2 := env2.login(t, "root", "root-pass", "super_admin")
		env2.seedProviderAndDefaultGroup(t, admin2)
		env2.seedAccount(t, "acc-w13g-lock2", admin2, "w13g-lock2", "active")
		env2.db.Close()
		if _, err := env2.store.SetLock(context.Background(), SetLockInput{AccountID: "acc-w13g-lock2", Enabled: true},
			AccessScope{ViewerID: admin2, IsAdmin: true}); err == nil {
			t.Fatal("db 关闭应报错")
		}
	})
	// lock 表缺失 → findAccountLockState 错误。
	t.Run("lock-table-drop", func(t *testing.T) {
		env3 := newTestEnv(t)
		admin3 := env3.login(t, "root", "root-pass", "super_admin")
		env3.seedProviderAndDefaultGroup(t, admin3)
		env3.seedAccount(t, "acc-w13g-lock3", admin3, "w13g-lock3", "active")
		env3.exec(t, `DROP TABLE account_lock_states`)
		if _, err := env3.store.SetLock(context.Background(), SetLockInput{AccountID: "acc-w13g-lock3", Enabled: true},
			AccessScope{ViewerID: admin3, IsAdmin: true}); err == nil {
			t.Fatal("lock 表缺失应报错")
		}
	})
}

func TestW13GForceActivatePendingArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13g-pend", adminID, "w13g-pend", "pending_test")
	admin := AccessScope{ViewerID: adminID, IsAdmin: true}

	// 空 ID → (nil,nil)。
	if out, err := env.store.ForceActivatePending(context.Background(), "  ", admin); err != nil || out != nil {
		t.Fatalf("空 ID：%v %v", out, err)
	}
	// pending_test 恢复：无可用性计划 → active。
	out, err := env.store.ForceActivatePending(context.Background(), "acc-w13g-pend", admin)
	if err != nil || out == nil || !out.Changed || out.Account == nil {
		t.Fatalf("恢复 pending：%+v %v", out, err)
	}
	if out.Account.Status != "active" {
		t.Fatalf("恢复状态：%+v", out.Account)
	}
	// 空 scope → (nil,nil)。
	if out, err := env.store.ForceActivatePending(context.Background(), "acc-w13g-pend", AccessScope{}); err != nil || out != nil {
		t.Fatalf("空 scope：%v %v", out, err)
	}
	// accounts 表缺失 → 错误。
	env.exec(t, `DROP TABLE accounts`)
	if _, err := env.store.ForceActivatePending(context.Background(), "acc-w13g-pend", admin); err == nil {
		t.Fatal("accounts 缺失应报错")
	}
}
