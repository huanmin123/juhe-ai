package accounts

// w13a m11_traffic_migration.go 未覆盖臂补齐：parseTrafficMigrationBody 解析
// 臂与 migrateOwnerTraffic 的守卫矩阵（同账户、缺目标、跨归属、跨供应商、
// 跨分组、目标不可调度、unchanged 摘要、临时不可用与停用分支）。

import (
	"context"
	"testing"
)

func TestW13AParseTrafficMigrationBody(t *testing.T) {
	if _, message := parseTrafficMigrationBody(map[string]any{"bogus": 1}); message == "" {
		t.Fatal("未知键应拒绝")
	}
	if _, message := parseTrafficMigrationBody(map[string]any{}); message == "" {
		t.Fatal("缺目标应拒绝")
	}
	if _, message := parseTrafficMigrationBody(map[string]any{"targetAccountId": "   "}); message == "" {
		t.Fatal("空白目标应拒绝")
	}
	if _, message := parseTrafficMigrationBody(map[string]any{"targetAccountId": "t", "sourceStatus": 3}); message == "" {
		t.Fatal("状态非字符串应拒绝")
	}
	if _, message := parseTrafficMigrationBody(map[string]any{"targetAccountId": "t", "sourceStatus": "bogus"}); message == "" {
		t.Fatal("非法状态枚举应拒绝")
	}
	input, message := parseTrafficMigrationBody(map[string]any{
		"targetAccountId": " t ", "sourceStatus": "disabled",
	})
	if message != "" || input.TargetAccountID != "t" || input.SourceStatus != trafficSourceDisabled {
		t.Fatalf("合法体解析不一致：%+v %q", input, message)
	}
}

func TestW13AMigrateOwnerTrafficGuards(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	seedOpenAICompatibleProvider(t, env)
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}

	env.seedAccount(t, "acc-w13a-tm-s", adminID, "w13a-tm-s", "active")
	env.seedAccount(t, "acc-w13a-tm-t", adminID, "w13a-tm-t", "active")
	// 同分组绑定。
	now := "2026-09-17T00:00:00.000Z"
	bindGroup := func(accountID, groupID string) {
		env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at)
			VALUES (?, ?, ?, 1, ?, ?) ON CONFLICT(group_id, account_id) DO UPDATE SET enabled = 1`, adminID, groupID, accountID, now, now)
	}
	bindGroup("acc-w13a-tm-s", "grp-default-"+adminID)
	bindGroup("acc-w13a-tm-t", "grp-default-"+adminID)

	// 同账户。
	if _, err := env.store.MigrateTraffic(context.Background(), "acc-w13a-tm-s",
		TrafficMigrationInput{TargetAccountID: "acc-w13a-tm-s"}, scope); err != errTrafficSameAccount {
		t.Fatalf("同账户应报错：%v", err)
	}
	// 目标缺失 → nil, nil。
	result, err := env.store.MigrateTraffic(context.Background(), "acc-w13a-tm-s",
		TrafficMigrationInput{TargetAccountID: "acc-w13a-none"}, scope)
	if err != nil || result != nil {
		t.Fatalf("缺失目标应返回 nil：%v %v", result, err)
	}

	// 跨归属：另一 owner 的目标。
	other := env.login(t, "w13a-tm-other", "other-pass", "user")
	env.seedAccount(t, "acc-w13a-tm-o", other, "w13a-tm-o", "active")
	if _, err := env.store.MigrateTraffic(context.Background(), "acc-w13a-tm-s",
		TrafficMigrationInput{TargetAccountID: "acc-w13a-tm-o"}, scope); err == nil ||
		err.Error() != "目标账户必须和当前账户归属同一个系统账户" {
		t.Fatalf("跨归属应拒绝：%v", err)
	}

	// 跨供应商。
	env.seedAccount(t, "acc-w13a-tm-p", adminID, "w13a-tm-p", "active")
	env.exec(t, `UPDATE accounts SET provider_code = 'openai' WHERE id = 'acc-w13a-tm-p'`)
	bindGroup("acc-w13a-tm-p", "grp-default-"+adminID)
	if _, err := env.store.MigrateTraffic(context.Background(), "acc-w13a-tm-s",
		TrafficMigrationInput{TargetAccountID: "acc-w13a-tm-p"}, scope); err == nil ||
		err.Error() != "目标账户必须和当前账户属于同一个供应商" {
		t.Fatalf("跨供应商应拒绝：%v", err)
	}

	// 同供应商但不在同分组。
	env.seedAccount(t, "acc-w13a-tm-g", adminID, "w13a-tm-g", "active")
	if _, err := env.store.MigrateTraffic(context.Background(), "acc-w13a-tm-s",
		TrafficMigrationInput{TargetAccountID: "acc-w13a-tm-g"}, scope); err == nil ||
		err.Error() != "目标账户必须和当前账户在同一个分组内" {
		t.Fatalf("跨分组应拒绝：%v", err)
	}

	// 目标不可调度（冷却中）。
	env.seedAccount(t, "acc-w13a-tm-c", adminID, "w13a-tm-c", "active")
	bindGroup("acc-w13a-tm-c", "grp-default-"+adminID)
	env.exec(t, `UPDATE accounts SET cooldown_until = '2030-01-01T00:00:00Z' WHERE id = 'acc-w13a-tm-c'`)
	if _, err := env.store.MigrateTraffic(context.Background(), "acc-w13a-tm-s",
		TrafficMigrationInput{TargetAccountID: "acc-w13a-tm-c"}, scope); err == nil ||
		err.Error() != "目标账户当前不可调度，请选择正常可用的账户" {
		t.Fatalf("冷却目标应拒绝：%v", err)
	}

	// unchanged 分支：只读摘要。
	result, err = env.store.MigrateTraffic(context.Background(), "acc-w13a-tm-s",
		TrafficMigrationInput{TargetAccountID: "acc-w13a-tm-t", SourceStatus: trafficSourceUnchanged}, scope)
	if err != nil || result == nil || result.SourceStatus != string(trafficSourceUnchanged) {
		t.Fatalf("unchanged 迁移应返回摘要：%+v %v", result, err)
	}

	// 临时不可用分支：源账户进入冷却并携带代际。
	result, err = env.store.MigrateTraffic(context.Background(), "acc-w13a-tm-s",
		TrafficMigrationInput{TargetAccountID: "acc-w13a-tm-t"}, scope)
	if err != nil || result == nil || result.SourceStatus != string(trafficSourceTemporaryUnavailable) {
		t.Fatalf("临时不可用迁移应成功：%+v %v", result, err)
	}
	if result.SourceCooldownUntil == nil || result.SourceAccount == nil || result.SourceAccount.Status != "temporary_unavailable" {
		t.Fatalf("源账户应进入临时不可用：%+v", result.SourceAccount)
	}

	// 停用分支：源账户直接停用（目标换新的可用账户）。
	env.seedAccount(t, "acc-w13a-tm-s2", adminID, "w13a-tm-s2", "active")
	bindGroup("acc-w13a-tm-s2", "grp-default-"+adminID)
	if _, err := env.store.MigrateTraffic(context.Background(), "acc-w13a-tm-t",
		TrafficMigrationInput{TargetAccountID: "acc-w13a-tm-s2", SourceStatus: trafficSourceDisabled}, scope); err != nil {
		t.Fatalf("停用迁移应成功：%v", err)
	}
	if got := env.queryCell(t, `SELECT status FROM accounts WHERE id = 'acc-w13a-tm-t'`); got != "disabled" {
		t.Fatalf("源账户应停用：%q", got)
	}
}
