package circuitstore

// W24② 回归（真实 PG）：余额快照 timestamptz 读出归一为 RFC3339 毫秒文本。
//
// 缺陷定性（真实 PG 渲染实证修正）：loadBalanceSnapshotRecords 原把
// juhe_stats.account_usage_snapshots 的 next_refresh_after/updated_at
// （timestamptz）扫入 string——pgx 驱动渲染为会话时区偏移形态（实测
// "2026-10-24T12:00:00.123456+08:00"），恰好可被 rfc3339Millis 解析，故
// balanceSnapshotMatchesConfiguration 的毫秒比较语义未破坏；但输出格式
// 随 session TZ 漂移（生产 UTC 库为 "+00:00" 形态）、与配置侧 text 列
// 的 Z 形态文本不同构，属真实格式分歧。本批改为 sql.NullTime 扫描 + 边界
// timestamptzMillisText 归一为 RFC3339 毫秒 UTC（Z）文本，格式不再依赖
// 驱动渲染与会话时区。

import (
	"context"
	"database/sql"
	"testing"
)

const w24bSnapshotsPrepare = `CREATE SCHEMA juhe_stats;
CREATE TABLE juhe_stats.account_usage_snapshots (
  account_id text NOT NULL,
  kind text NOT NULL,
  snapshot_json text NOT NULL,
  next_refresh_after timestamptz,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (account_id, kind)
)`

func TestW24BBalanceSnapshotTimestamptzLoadsRFC3339MillisOnPostgres(t *testing.T) {
	db := openKitPostgresDB(t, func(ctx context.Context, handle *sql.DB) error {
		_, err := handle.ExecContext(ctx, w24bSnapshotsPrepare)
		return err
	})
	ctx := context.Background()
	// next_refresh_after 带微秒精度（与生产 time.Time 写入经 PG 微秒截断的
	// 存储形态一致），验证毫秒归一输出。
	seed := `INSERT INTO juhe_stats.account_usage_snapshots (account_id, kind, snapshot_json, next_refresh_after, updated_at) VALUES
	  ('acc-w24-match', 'relay_balance', '{"configRevision":3,"totalBalance":5.5}', '2026-10-24T04:00:00.123456+00'::timestamptz, '2026-10-24T04:00:00.654321+00'::timestamptz),
	  ('acc-w24-null', 'relay_balance', '{}', NULL, '2026-10-24T05:00:00.000000+00'::timestamptz)`
	if _, err := db.ExecContext(ctx, seed); err != nil {
		t.Fatalf("种子 account_usage_snapshots 失败: %v", err)
	}

	loader := &ProjectionItemLoader{statsDB: db}
	records, err := loader.loadBalanceSnapshotRecords(ctx, []string{"acc-w24-match", "acc-w24-null"})
	if err != nil {
		t.Fatalf("loadBalanceSnapshotRecords 失败: %v", err)
	}

	record := records["acc-w24-match"]
	if record == nil {
		t.Fatal("必须读到 acc-w24-match 的余额快照")
	}
	if record.nextRefreshAfter != "2026-10-24T04:00:00.123Z" {
		t.Fatalf("nextRefreshAfter 必须归一为 RFC3339 毫秒文本（W24② 修复前为 pgx 渲染形态）: %q", record.nextRefreshAfter)
	}
	if record.updatedAt != "2026-10-24T04:00:00.654Z" {
		t.Fatalf("updatedAt 必须归一为 RFC3339 毫秒文本: %q", record.updatedAt)
	}

	// 端到端契约：与 accounts.balance_query_next_refresh_at（text 列 RFC3339
	// 毫秒文本）同毫秒即匹配——修复前 rfc3339Millis 对 pgx 渲染形态解析
	// 失败，匹配恒 false。
	if !balanceSnapshotMatchesConfiguration("2026-10-24T04:00:00.123Z", 3, record) {
		t.Fatal("同毫秒 nextRefreshAt 必须匹配（W24② 核心契约）")
	}
	if balanceSnapshotMatchesConfiguration("2026-10-24T04:00:00.124Z", 3, record) {
		t.Fatal("不同毫秒的 nextRefreshAt 不得匹配")
	}
	if record.snapshotRevision != 3 {
		t.Fatalf("snapshotRevision 不符: %v", record.snapshotRevision)
	}

	nullRecord := records["acc-w24-null"]
	if nullRecord == nil {
		t.Fatal("必须读到 acc-w24-null 的余额快照")
	}
	if nullRecord.nextRefreshAfter != "" {
		t.Fatalf("NULL next_refresh_after 必须归一为空串: %q", nullRecord.nextRefreshAfter)
	}
	if !balanceSnapshotMatchesConfiguration("", 0, nullRecord) {
		t.Fatal("两侧均为空时必须匹配")
	}
	if balanceSnapshotMatchesConfiguration("2026-10-24T04:00:00.000Z", 0, nullRecord) {
		t.Fatal("配置侧非空而持久化侧为空时不得匹配")
	}
}

// TestW24BServerSideTimestamptzTextIsNotRFC3339 固化两个渲染面的事实：
//  1. 服务器端 ::text 是空格形态（"2026-10-24 04:00:00.123456+00"），不可被
//     rfc3339Millis 解析——任何走 ::text 的读出都不能再扫 string 参与该比较；
//  2. pgx 驱动渲染（loader 原路径）是会话时区偏移形态（实测 dev 库
//     "2026-10-24T12:00:00.123456+08:00"），恰好是合法 RFC3339，毫秒比较
//     语义未破坏，但格式随 session TZ 漂移——本批归一消除该依赖。
func TestW24BServerSideTimestamptzTextIsNotRFC3339(t *testing.T) {
	db := openKitPostgresDB(t, func(ctx context.Context, handle *sql.DB) error {
		_, err := handle.ExecContext(ctx, w24bSnapshotsPrepare)
		return err
	})
	var rendered string
	if err := db.QueryRowContext(context.Background(), "SELECT '2026-10-24T04:00:00.123456+00'::timestamptz::text").Scan(&rendered); err != nil {
		t.Fatal(err)
	}
	if _, ok := rfc3339Millis(rendered); ok {
		t.Fatalf("PG 服务器端 timestamptz::text 渲染 %q 不应被 rfc3339Millis 解析（根因事实变化，请复核 W24② 修复前提）", rendered)
	}
	if _, ok := rfc3339Millis("2026-10-24T04:00:00.123Z"); !ok {
		t.Fatal("RFC3339 毫秒文本必须可被 rfc3339Millis 解析")
	}
}
