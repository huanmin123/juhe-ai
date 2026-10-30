package main

// 后台自动探测总开关（PLAN-20261030T062141000Z W2）outbox 消费面守卫的
// boundary 读臂：healthProbeBoundary.BackgroundProbeEnabled 的生效语义
// （实例行取来源账户值、来源缺失 COALESCE 归 1、软删行视为不存在）。
// pgtestkit 隔离 fresh 库 + accounts 最小列夹具（p0_oauth_wiring_test 先例）。

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// w2bCmdOpenAccountsKit 打开仅含 accounts 最小契约列的隔离库。
func w2bCmdOpenAccountsKit(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS juhe_business`); err != nil {
			return err
		}
		_, err := db.Exec(`CREATE TABLE IF NOT EXISTS juhe_business.accounts (
			id text PRIMARY KEY,
			deleted_at text,
			authorization_instance_source_account_id text,
			authorization_instance_authorization_id text,
			background_probe_enabled integer NOT NULL DEFAULT 1 CHECK (background_probe_enabled IN (0, 1)))`)
		return err
	})
}

func TestW2CmdBoundaryBackgroundProbeEnabledArms(t *testing.T) {
	db := w2bCmdOpenAccountsKit(t)
	ctx := context.Background()
	boundary := healthProbeBoundary{business: &businessDB{db: db}}

	seed := func(t *testing.T, id string, source any, authorization any, enabled int) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO juhe_business.accounts (id, authorization_instance_source_account_id, authorization_instance_authorization_id, background_probe_enabled)
			VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO UPDATE SET authorization_instance_source_account_id = EXCLUDED.authorization_instance_source_account_id, authorization_instance_authorization_id = EXCLUDED.authorization_instance_authorization_id, background_probe_enabled = EXCLUDED.background_probe_enabled`, id, source, authorization, enabled); err != nil {
			t.Fatal(err)
		}
	}
	seed(t, "w2b-cmd-off", nil, nil, 0)
	seed(t, "w2b-cmd-src", nil, nil, 0)
	// 实例行必须携带非空授权 ID，CASE 才会取来源账户分支（与生产实例行同形）。
	seed(t, "w2b-cmd-inst", "w2b-cmd-src", "w2b-cmd-auth", 1)

	// 普通账户行自身值。
	enabled, found, err := boundary.BackgroundProbeEnabled(ctx, "w2b-cmd-off")
	if err != nil || !found || enabled {
		t.Fatalf("flag=0 账户必须 (found, disabled): %t %t %v", enabled, found, err)
	}
	// 实例行显式 1 但来源 0 → 生效值跟随来源为关。
	enabled, found, err = boundary.BackgroundProbeEnabled(ctx, "w2b-cmd-inst")
	if err != nil || !found || enabled {
		t.Fatalf("实例生效值必须跟随来源关闭: %t %t %v", enabled, found, err)
	}
	// 来源翻回 1 → 实例生效值恢复。
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET background_probe_enabled = 1 WHERE id = 'w2b-cmd-src'`); err != nil {
		t.Fatal(err)
	}
	enabled, found, err = boundary.BackgroundProbeEnabled(ctx, "w2b-cmd-inst")
	if err != nil || !found || !enabled {
		t.Fatalf("来源恢复开启后实例生效值必须开启: %t %t %v", enabled, found, err)
	}
	// 缺失账户 → found=false（调用方按未关闭放行，由后续链收敛）。
	enabled, found, err = boundary.BackgroundProbeEnabled(ctx, "w2b-cmd-missing")
	if err != nil || found || enabled {
		t.Fatalf("缺失账户必须 (false, false, nil): %t %t %v", enabled, found, err)
	}
	// 软删账户行 → found=false。
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET deleted_at = '2026-10-30T00:00:00Z' WHERE id = 'w2b-cmd-off'`); err != nil {
		t.Fatal(err)
	}
	if _, found, err := boundary.BackgroundProbeEnabled(ctx, "w2b-cmd-off"); found || err != nil {
		t.Fatalf("软删账户必须 (false, nil): %t %v", found, err)
	}
}
