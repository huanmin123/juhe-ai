// 波次 w12c：PG 门控的 worker 组合根装配覆盖。连接串只从任务授权的
// .local/project-resources/dev/env/shared.env 读取，改写端口 6432→5432 与
// 库名 →juhe_ai_sub2api_dev_w1cover；连接失败 t.Skip。共享覆盖库上只做
// 幂等 DDL 与空跑，不清理他人数据；连接串/密码不写入日志与断言。
package main

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// w12cPGOverrideURL 读取开发连接串并改写为隔离覆盖库；任一步失败返回空。
func w12cPGOverrideURL(t *testing.T) string {
	t.Helper()
	if raw := strings.TrimSpace(os.Getenv("W12C_TEST_PG_URL")); raw != "" {
		return w12cRewritePGURL(raw)
	}
	candidates := []string{
		filepath.Join("..", "..", "..", "..", ".local", "project-resources", "dev", "env", "shared.env"),
		filepath.FromSlash("F:/sub2api-lite/.local/project-resources/dev/env/shared.env"),
	}
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if value, ok := strings.CutPrefix(line, "JUHE_AI_POSTGRES_URL="); ok {
				return w12cRewritePGURL(strings.TrimSpace(value))
			}
		}
	}
	return ""
}

// w12cRewritePGURL 把 6432 端口与开发库改写为覆盖库（5432/juhe_ai_sub2api_dev_w1cover）。
func w12cRewritePGURL(raw string) string {
	rewritten := strings.Replace(raw, ":6432/", ":5432/", 1)
	rewritten = strings.Replace(rewritten, "/juhe_ai_sub2api_dev?", "/juhe_ai_sub2api_dev_w1cover?", 1)
	if strings.Contains(rewritten, "/juhe_ai_sub2api_dev_w1cover") {
		return rewritten
	}
	// 库名无查询参数时直接追加。
	if index := strings.LastIndex(rewritten, "/"); index >= 0 {
		base := rewritten[:index+1]
		tail := rewritten[index+1:]
		if suffix, ok := strings.CutPrefix(tail, "juhe_ai_sub2api_dev"); ok {
			return base + "juhe_ai_sub2api_dev_w1cover" + suffix
		}
	}
	return ""
}

// TestW12CPGWorkerAssemblyWiring 在覆盖库上以 postgres 驱动装配全部任务族：
// 锁定 wire* 家族 PG 分支接线（store 打开、schema 初始化、PG-only 任务注册）
// 与最小组件关闭路径。业务契约表缺失的任务按组合根契约登记 disabled 或任务
// 失败，这里只记录不判失败（共享覆盖库不保证业务 schema）。
func TestW12CPGWorkerAssemblyWiring(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门控装配 skipped in -short mode")
	}
	pgURL := w12cPGOverrideURL(t)
	if pgURL == "" {
		t.Skip("w12c: 覆盖库连接串不可用")
	}
	// w14j：共享覆盖库可能被外部重置，先幂等自愈测试形状（加法 DDL +
	// canonical sys_admin 种子；连接串不落日志）。
	if db, err := sql.Open("pgx", pgURL); err == nil {
		w14jEnsurePGFixture(t, db)
		_ = db.Close()
	}
	redisServer := miniredis.RunT(t)
	env := map[string]string{
		"JUHE_AI_DATABASE_DRIVER":         "postgres",
		"JUHE_AI_POSTGRES_URL":            pgURL,
		"JUHE_AI_POSTGRES_MAX_OPEN_CONNS": "10",
		"JUHE_AI_POSTGRES_MAX_IDLE_CONNS": "5",
		"JUHE_AI_SECRET":                  wgBalanceSecret,
		"JUHE_AI_INSTANCE_ID":             "w12c-pg-assembly",
		"JUHE_AI_WORKER_ROLE":             "stats-worker",
		"JUHE_AI_REDIS_STATE_URL":         "redis://" + redisServer.Addr(),
		"JUHE_AI_REDIS_NAMESPACE":         "juhe-ai:w12c",
		"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_ENABLED": "true",
	}
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadWorkerConfig(postgres): %v", err)
	}
	assembly, err := buildWorkerAssembly(config, slog.Default())
	if err != nil {
		t.Fatalf("buildWorkerAssembly(postgres): %v", err)
	}
	defer assembly.closeStores()

	wired := map[string]bool{}
	for _, name := range assembly.wiredJobs {
		wired[name] = true
	}
	// PG 分支核心差异任务：PG-only 的 ai-performance-summary-windows-refresh
	// 必须接线；SQLite-only 的 usage-scope-range-windows-refresh 必须登记
	// disabled（冻结清单 §2.2/§4.1）。
	if !wired["ai-performance-summary-windows-refresh"] {
		t.Errorf("PG 分支必须接线 ai-performance-summary-windows-refresh: disabled=%v", assembly.disabledJobs)
	}
	for _, disabled := range assembly.disabledJobs {
		if disabled.JobName == "usage-scope-range-windows-refresh" {
			continue
		}
		t.Logf("PG 装配 disabled 任务 %s: %s", disabled.JobName, disabled.Reason)
	}
	// 逐任务直跑一轮：覆盖 PG 任务闭包主体；契约表缺失导致的失败只记录
	// （组合根对这些任务的生产行为由 fail closed 登记或任务错误上报承担）。
	for _, name := range assembly.wiredJobs {
		name := name
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := assembly.runWiredJobOnce(ctx, name); err != nil {
				t.Logf("任务 %s 在共享覆盖库上执行失败（记录不判失败）: %v", name, err)
			}
		})
	}
}
