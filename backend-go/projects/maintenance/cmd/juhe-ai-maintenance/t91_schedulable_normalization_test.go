package main

// T91-3（R7-B B1-10 裁决）存量归一钉测：升级链第 13 步
// account-schedulable-normalization 只把 status='active' AND schedulable=0 的
// 行改为 1，其余组合不动；Satisfied/Verify 与 Apply 同源，重复执行零写入。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func t91SeedSchedulableAccount(t *testing.T, db *sql.DB, id, status string, schedulable int) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// accounts 写入触发器会按 system_account_id 写 account_list_availability_dirty
	// （外键指向 system_accounts），先播系统账户。
	if _, err := db.ExecContext(context.Background(), `INSERT INTO juhe_business.system_accounts
		(id, username, display_name, role, status, password_hash, created_at, updated_at)
		VALUES ('sys-t91', 'sys-t91', 'T91 系统账户', 'user', 'active', 'hash', $1, $1)
		ON CONFLICT (id) DO NOTHING`, now); err != nil {
		t.Fatalf("seed system account: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO juhe_business.accounts
		(id, system_account_id, connection_driver_code, protocol_code, protocol_version, name, type,
		 status, schedulable, credentials_encrypted, created_at, updated_at)
		VALUES ($1, 'sys-t91', 'gpt', 'openai', 'v1', $1, 'api_key', $2, $3, 'sealed', $4, $4)`,
		id, status, schedulable, now); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func TestT91AccountSchedulableNormalizationStep(t *testing.T) {
	td := pgtestkit.NewTestDatabase(t)
	ctx := context.Background()
	if _, err := schema.EnsurePostgres(ctx, td.DB); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	t91SeedSchedulableAccount(t, td.DB, "acc-t91-active-off-a", "active", 0)
	t91SeedSchedulableAccount(t, td.DB, "acc-t91-active-off-b", "active", 0)
	t91SeedSchedulableAccount(t, td.DB, "acc-t91-active-on", "active", 1)
	t91SeedSchedulableAccount(t, td.DB, "acc-t91-disabled-off", "disabled", 0)
	t91SeedSchedulableAccount(t, td.DB, "acc-t91-temp-on", "temporary_unavailable", 1)

	// upgradeSteps 的 upgradeTargets 会把缺失的 J3a/J3b/balance 连接串写回进程
	// 环境（生产语义：缺省回落 primary）。这里用 t.Setenv 显式占位，测试结束时
	// 自动还原，避免把「已删除的临时库 DSN」泄漏给同包后续用例。
	t.Setenv(envJ3AURL, td.DSN)
	t.Setenv(envJ3BURL, td.DSN)
	t.Setenv(envBalanceURL, td.DSN)
	steps := upgradeSteps(td.DSN, "t91-normalization-secret")
	var step *upgradepipeline.Step
	for index := range steps {
		if steps[index].ID == "account-schedulable-normalization" {
			step = &steps[index]
			break
		}
	}
	if step == nil {
		t.Fatal("注册表缺少 account-schedulable-normalization 步骤")
	}
	if step.Revision != 13 {
		t.Fatalf("步骤 revision = %d, want 13", step.Revision)
	}
	env := upgradepipeline.Env{PrimaryDSN: td.DSN, Secret: "t91-normalization-secret"}

	// 归一前：不满足（存在 2 行 active+schedulable=0）。
	if satisfied, err := step.Satisfied(ctx, env); err != nil || satisfied {
		t.Fatalf("归一前 Satisfied = %v / %v，want false", satisfied, err)
	}
	detail, err := step.Apply(ctx, env)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, _ := detail["updatedRows"].(int); got != 2 {
		t.Fatalf("首跑受影响行数 = %v, want 2", detail["updatedRows"])
	}
	if err := step.Verify(ctx, env); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if satisfied, err := step.Satisfied(ctx, env); err != nil || !satisfied {
		t.Fatalf("归一后 Satisfied = %v / %v，want true", satisfied, err)
	}

	// 幂等：重跑零写入。
	again, err := step.Apply(ctx, env)
	if err != nil {
		t.Fatalf("重跑 Apply: %v", err)
	}
	if got, _ := again["updatedRows"].(int); got != 0 {
		t.Fatalf("重跑受影响行数 = %v, want 0", again["updatedRows"])
	}

	// 目标组合收敛为 1，其余组合原样。
	rows := map[string]int{}
	for _, id := range []string{"acc-t91-active-off-a", "acc-t91-active-off-b", "acc-t91-active-on",
		"acc-t91-disabled-off", "acc-t91-temp-on"} {
		var schedulable int
		if err := td.DB.QueryRowContext(ctx,
			`SELECT schedulable FROM juhe_business.accounts WHERE id = $1`, id).Scan(&schedulable); err != nil {
			t.Fatalf("读 %s: %v", id, err)
		}
		rows[id] = schedulable
	}
	want := map[string]int{
		"acc-t91-active-off-a": 1,
		"acc-t91-active-off-b": 1,
		"acc-t91-active-on":    1,
		"acc-t91-disabled-off": 0,
		"acc-t91-temp-on":      1,
	}
	for id, value := range want {
		if rows[id] != value {
			t.Fatalf("%s schedulable = %d, want %d（仅 active 组合归一）", id, rows[id], value)
		}
	}
}
