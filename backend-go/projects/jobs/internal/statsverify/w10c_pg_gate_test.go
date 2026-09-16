package statsverify

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// w10c_pg_gate_test.go 门禁化开发 PostgreSQL（w1cover 临时覆盖库）覆盖
// statsverify 的 PG 专有分支：checkPostgresSchema、groupstats Postgres
// 包装与 MarkAllGroupAccountStatsDirty Postgres 臂、client-ip 聚合 PG 空批次
// 臂。数据全部 w10cpgsv- 前缀 ID，测试结束清理；数据库不可达时 t.Skip。

func w10cSvPgSkip(reason string) string { return "w10c statsverify PG gated: " + reason }

func w10cSvPgDSN(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("JUHE_AI_W10CSV_PG_URL"); url != "" {
		return url
	}
	raw, err := os.ReadFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
	if err != nil {
		t.Skip(w10cSvPgSkip("shared.env 不可读"))
	}
	base := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "JUHE_AI_POSTGRES_URL=") {
			base = strings.TrimPrefix(line, "JUHE_AI_POSTGRES_URL=")
		}
	}
	if base == "" {
		t.Skip(w10cSvPgSkip("shared.env 缺少 JUHE_AI_POSTGRES_URL"))
	}
	base = strings.Replace(base, ":6432/", ":5432/", 1)
	base = strings.Replace(base, "/juhe_ai_sub2api_dev?", "/juhe_ai_sub2api_dev_w1cover?", 1)
	return base
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
		t.Skipf(w10cSvPgSkip("OpenStore PG 打开失败: %v"), err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if store.mode != StorePostgres {
		t.Fatalf("模式应为 postgres")
	}

	// checkPostgresSchema：w1cover 预置表齐备则通过（0% → 全臂）。
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
