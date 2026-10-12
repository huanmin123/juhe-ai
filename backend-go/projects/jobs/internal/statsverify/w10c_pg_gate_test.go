package statsverify

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"
)

const w10cSvScratchDatabase = "juhe_ai_sub2api_dev_w1cover"

// w10c_pg_gate_test.go 只在显式 opt-in 的 w1cover scratch 库上覆盖
// statsverify 的 PostgreSQL 专有分支。测试不会从开发配置推导连接地址。

func w10cSvPgSkip(reason string) string { return "w10c statsverify PG gated: " + reason }

func w10cSvValidatePGURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("JUHE_AI_W10CSV_PG_URL 不是有效 PostgreSQL URL")
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return fmt.Errorf("JUHE_AI_W10CSV_PG_URL 必须使用 postgres 或 postgresql scheme")
	}
	if parsed.Path != "/"+w10cSvScratchDatabase {
		return fmt.Errorf("JUHE_AI_W10CSV_PG_URL 必须指向 scratch database %q", w10cSvScratchDatabase)
	}
	if database := parsed.Query().Get("dbname"); database != "" && database != w10cSvScratchDatabase {
		return fmt.Errorf("JUHE_AI_W10CSV_PG_URL 的 dbname 参数必须指向 scratch database %q", w10cSvScratchDatabase)
	}
	return nil
}

func w10cSvPgDSN(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("JUHE_AI_W10CSV_PG_URL")
	if raw == "" {
		t.Skip(w10cSvPgSkip("未设置 JUHE_AI_W10CSV_PG_URL"))
	}
	if err := w10cSvValidatePGURL(raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestW10CPGStoreGroupStatsAndClientIP(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(StoreConfig{
		Mode:                 StorePostgres,
		PostgresURL:          w10cSvPgDSN(t),
		PostgresMaxOpenConns: 4,
		PostgresMaxIdleConns: 2,
	})
	if err != nil {
		t.Fatalf("显式 opt-in 的 PG OpenStore 失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if store.mode != StorePostgres {
		t.Fatalf("模式应为 postgres")
	}
	var currentDatabase string
	if err := store.db.QueryRowContext(ctx, "SELECT current_database()").Scan(&currentDatabase); err != nil {
		t.Fatalf("显式 opt-in 的 PG 读取 current_database 失败: %v", err)
	}
	if currentDatabase != w10cSvScratchDatabase {
		t.Fatalf("PG current_database=%q，要求为 scratch database %q", currentDatabase, w10cSvScratchDatabase)
	}

	// checkPostgresSchema：确认 scratch 库具备外部预置表。
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("PG EnsureSchema 失败: %v", err)
	}

	// 时区行（client-ip 聚合空批次需要 LoadUsageStatsLocation）。
	mustExec(t, ctx, store.db, `INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
		VALUES ('sys_admin', 'usageStatsTimezone', $1, $2) ON CONFLICT (system_account_id, key) DO UPDATE SET value_json = EXCLUDED.value_json`,
		mustJSONString("UTC"), NowIso(time.Now()))
	t.Cleanup(func() {
		if _, err := store.db.ExecContext(ctx, `DELETE FROM juhe_business.system_settings WHERE system_account_id='sys_admin' AND key='usageStatsTimezone'`); err != nil {
			t.Logf("w10c cleanup system_settings: %v", err)
		}
	})

	// MarkAllGroupAccountStatsDirty Postgres 臂。
	if err := store.MarkAllGroupAccountStatsDirty(ctx, "w10cpgsv-initial", time.Now()); err != nil {
		t.Fatalf("PG MarkAllGroupAccountStatsDirty 失败: %v", err)
	}

	// RefreshDirtyGroupAccountStats Postgres 包装（refreshDirtyGroupAccountStatsPostgres
	// 0% → 全臂；空 __all__ 分批消费路径，清理脏行）。
	if _, err := store.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Now: time.Now()}); err != nil {
		t.Fatalf("PG RefreshDirtyGroupAccountStats 失败: %v", err)
	}
	// 消费后 __all__ 脏行应已被删除。
	var dirtyCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_business.group_account_stats_dirty WHERE reason LIKE 'w10cpgsv%'`).Scan(&dirtyCount); err != nil {
		t.Fatal(err)
	}
	if dirtyCount != 0 {
		t.Fatalf("PG 脏行应被消费删除, count=%d", dirtyCount)
	}

	// AggregateClientIPStatsBatch Postgres 路径（beginWriteTx PG + client-ip
	// 聚合 PG 批处理）。w1cover 可能残留其它 wave 的 usage_records，因此不
	// 断言具体批次大小，仅验证 PG 批处理无错。
	if _, err := store.AggregateClientIPStatsBatch(ctx, 10, time.Now()); err != nil {
		t.Fatalf("PG AggregateClientIPStatsBatch 失败: %v", err)
	}

	// 清理 stats_job_state 的 client-ip / 刷新 job state 行。
	t.Cleanup(func() {
		for _, jobName := range []string{clientIpStatsJobName} {
			if _, err := store.db.ExecContext(ctx, `DELETE FROM juhe_stats.stats_job_state WHERE job_name = $1`, jobName); err != nil {
				t.Logf("w10c cleanup job state: %v", err)
			}
		}
	})
}
