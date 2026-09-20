package main

// P0 修复回归：retention account_usage_snapshot_upsert 通道接线验证。
//
// 此前 runner.Executor.StatsWriter 在 PG 模式为 nil、SQLite 模式为报错
// stub，快照任务两条 driver 都以「retention stats writer 未初始化」失败，
// 单条失败即占死 record_maintenance_jobs drain 队头（1s 固定退避）。
// 现组合根把 StatsWriter 恒接到 cleanuprepo.RecordCleanupStore.
// UpsertAccountUsageSnapshots（jobregistry 登记 GoWired、owner=cleanuprepo），
// 本文件在联合 fixture 上锁定 job→输入映射与 DB 落行全链路。

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// TestWorkerAssemblySnapshotUpsertChannel 表驱动锁定
// account_usage_snapshot_upsert 任务的字段映射与落行：字段映射面
// （account_id/kind/source/snapshot_json/updated_at，runner 逐字段复制进
// AccountUsageSnapshotUpsertInput 后由 cleanuprepo 落 account_usage_snapshots）
// 与 updatedAt 非法的 fail-closed 臂。
func TestWorkerAssemblySnapshotUpsertChannel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped in -short mode")
	}
	redisServer := miniredis.RunT(t)
	dir := t.TempDir()
	wgSeedAllJobsFixture(t, dir)
	env := wgAllJobsAssemblyEnv(t, dir, "redis://"+redisServer.Addr())
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	assembly, err := buildWorkerAssembly(config, nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()
	family := assembly.retention
	if family == nil || family.runner == nil {
		t.Fatal("retention family 必须完整装配")
	}
	if family.runner.Executor.StatsWriter == nil {
		t.Fatal("runner.Executor.StatsWriter 必须双模式恒接线")
	}

	// owners 归属查询目标：business accounts 两行（同 owner 与不同 owner）。
	business := openTestSQLite(t, filepath.Join(dir, "business.sqlite3"))
	mustExec(t, business,
		"INSERT OR IGNORE INTO accounts (id, system_account_id, name, type, status, provider_code, config_revision, dispatch_revision, updated_at) VALUES ('acc-snap-a', 'sys_owner_a', '快照账户A', 'api_key', 'active', 'openai', 1, 1, '2026-01-01T00:00:00.000Z')",
		"INSERT OR IGNORE INTO accounts (id, system_account_id, name, type, status, provider_code, config_revision, dispatch_revision, updated_at) VALUES ('acc-snap-b', 'sys_owner_b', '快照账户B', 'api_key', 'active', 'openai', 1, 1, '2026-01-01T00:00:00.000Z')")

	t.Run("批量映射落行", func(t *testing.T) {
		stats := openTestSQLite(t, filepath.Join(dir, "stats.sqlite3"))
		jobs := []retention.RecordMaintenanceJob{
			{
				Type:      retention.JobTypeAccountUsageSnapshotUpsert,
				ID:        "recmaint-snap-a",
				AccountID: "acc-snap-a",
				Kind:      retention.AccountUsageSnapshotKindOpenAICodex,
				Source:    "balance_detect",
				Snapshot:  map[string]any{"costUsd": 0.5, "used": true},
				UpdatedAt: "2026-02-01T10:00:00.000Z",
				CreatedAt: "2026-02-01T10:00:00.000Z",
			},
			{
				Type:      retention.JobTypeAccountUsageSnapshotUpsert,
				ID:        "recmaint-snap-b",
				AccountID: "acc-snap-b",
				Kind:      retention.AccountUsageSnapshotKindOpenAICodex,
				Source:    "",
				Snapshot:  map[string]any{"costUsd": 1.25},
				UpdatedAt: "2026-02-01T10:00:01.000Z",
				CreatedAt: "2026-02-01T10:00:01.000Z",
			},
		}
		// 连续段合并入口（drain 的同一执行面）：一次 stats-writer 往返落两行。
		result, err := family.runMaintenanceSnapshotUpserts(context.Background(), jobs)
		if err != nil {
			t.Fatalf("runMaintenanceSnapshotUpserts: %v", err)
		}
		if upserted, ok := result["upsertedCount"].(int); !ok || upserted != 2 {
			t.Fatalf("upsertedCount 必须为 2: %#v", result)
		}

		wantRows := []struct {
			accountID      string
			systemOwner    string
			source         string
			snapshotSubstr string
		}{
			{"acc-snap-a", "sys_owner_a", "balance_detect", `"costUsd":0.5`},
			{"acc-snap-b", "sys_owner_b", "", `"costUsd":1.25`},
		}
		for _, want := range wantRows {
			var systemAccountID, kind, gotSource, snapshotJSON, refreshStatus, updatedAt string
			queryErr := stats.QueryRow(
				`SELECT system_account_id, kind, COALESCE(source, ''), snapshot_json, refresh_status, updated_at
				 FROM account_usage_snapshots WHERE account_id = ?`, want.accountID).
				Scan(&systemAccountID, &kind, &gotSource, &snapshotJSON, &refreshStatus, &updatedAt)
			if queryErr != nil {
				t.Fatalf("账户 %s 快照行缺失: %v", want.accountID, queryErr)
			}
			if systemAccountID != want.systemOwner {
				t.Fatalf("账户 %s owners 归属错误: got %s want %s", want.accountID, systemAccountID, want.systemOwner)
			}
			if kind != retention.AccountUsageSnapshotKindOpenAICodex {
				t.Fatalf("账户 %s kind 错误: %s", want.accountID, kind)
			}
			if gotSource != want.source {
				t.Fatalf("账户 %s source 映射错误: got %q want %q", want.accountID, gotSource, want.source)
			}
			if !strings.Contains(snapshotJSON, want.snapshotSubstr) {
				t.Fatalf("账户 %s snapshot_json 映射错误: got %s want 包含 %s", want.accountID, snapshotJSON, want.snapshotSubstr)
			}
			if refreshStatus != "fresh" {
				t.Fatalf("账户 %s refresh_status 必须为 fresh: %s", want.accountID, refreshStatus)
			}
			if want.accountID == "acc-snap-a" && updatedAt != "2026-02-01T10:00:00.000Z" {
				t.Fatalf("账户 %s updated_at 映射错误: %s", want.accountID, updatedAt)
			}
			if want.accountID == "acc-snap-b" && updatedAt != "2026-02-01T10:00:01.000Z" {
				t.Fatalf("账户 %s updated_at 映射错误: %s", want.accountID, updatedAt)
			}
		}
		// 重复 upsert 走 ON CONFLICT 更新（updated_at 抬升）。
		jobs[0].UpdatedAt = "2026-02-02T10:00:00.000Z"
		jobs[0].Snapshot = map[string]any{"costUsd": 2.5}
		if _, err := family.runMaintenanceSnapshotUpserts(context.Background(), jobs[:1]); err != nil {
			t.Fatalf("重复 upsert: %v", err)
		}
		var snapshotJSON string
		if err := stats.QueryRow(`SELECT snapshot_json FROM account_usage_snapshots WHERE account_id = 'acc-snap-a'`).Scan(&snapshotJSON); err != nil {
			t.Fatalf("重读快照行: %v", err)
		}
		if snapshotJSON != `{"costUsd":2.5}` {
			t.Fatalf("ON CONFLICT 更新未生效: %s", snapshotJSON)
		}
	})

	t.Run("updatedAt非法fail-closed", func(t *testing.T) {
		_, err := family.runMaintenanceOnce(context.Background(), retention.RecordMaintenanceJob{
			Type:      retention.JobTypeAccountUsageSnapshotUpsert,
			AccountID: "acc-snap-a",
			Kind:      retention.AccountUsageSnapshotKindOpenAICodex,
			Snapshot:  map[string]any{},
			UpdatedAt: "not-a-rfc3339-instant",
		})
		if err == nil {
			t.Fatal("updatedAt 非法的快照任务必须失败")
		}
	})

	t.Run("账户不存在显式报错", func(t *testing.T) {
		_, err := family.runMaintenanceOnce(context.Background(), retention.RecordMaintenanceJob{
			Type:      retention.JobTypeAccountUsageSnapshotUpsert,
			AccountID: "acc-missing",
			Kind:      retention.AccountUsageSnapshotKindOpenAICodex,
			Snapshot:  map[string]any{},
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		})
		if err == nil {
			t.Fatal("账户缺失的快照任务必须显式报错（Node 账户归属校验）")
		}
	})
}
