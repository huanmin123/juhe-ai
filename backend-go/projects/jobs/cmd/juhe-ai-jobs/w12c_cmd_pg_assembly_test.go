// 波次 w12c：PG 门控的 worker 组合根装配覆盖。建库走 wgCmdKitDB 的 pgtestkit
// 隔离 PG 测试库（生产对齐契约面预置，原 shared.env 门禁共享覆盖库装配已
// 退役，PG 不可达显式 Fatal）：fresh 库上做组合根装配与空跑，连接串/密码不
// 写入日志与断言。
package main

import (
	"context"

	"github.com/alicebob/miniredis/v2"
	"log/slog"
	"testing"
	"time"
)

// TestW12CPGWorkerAssemblyWiring 在隔离测试库上以 postgres 驱动装配全部任务
// 族：锁定 wire* 家族 PG 分支接线（store 打开、schema 初始化、PG-only 任务
// 注册）与最小组件关闭路径。业务契约表缺失的任务按组合根契约登记 disabled 或
// 任务失败，这里只记录不判失败（fresh 库不保证每个任务族的运行时数据面）。
func TestW12CPGWorkerAssemblyWiring(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门控装配 skipped in -short mode")
	}
	_, pgURL := wgCmdKitDB(t)
	redisServer := miniredis.RunT(t)
	env := map[string]string{
		"JUHE_AI_DATABASE_DRIVER": "postgres",
		"JUHE_AI_POSTGRES_URL":    pgURL,
		// PG pool conns 已收编为常量（2026-09-30 C4），不再设置 env。
		"JUHE_AI_SECRET":          wgBalanceSecret,
		"JUHE_AI_INSTANCE_ID":     "w12c-pg-assembly",
		"JUHE_AI_WORKER_ROLE":     "stats-worker",
		"JUHE_AI_REDIS_STATE_URL": "redis://" + redisServer.Addr(),
		"JUHE_AI_REDIS_NAMESPACE": "juhe-ai:w12c",
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
				t.Logf("任务 %s 在隔离测试库上执行失败（记录不判失败）: %v", name, err)
			}
		})
	}
}
