package main

// P0 补修回归：account-list-availability-projection-maintenance 的排期同步
// （SyncSchedules）必须与 account-availability-schedule-status-sync 一样注入
// 激活 hook——投影认领时被定时窗口启用的账号同样要推进 circuit dispatch
// revision 家族，否则认领后仍被网关 dispatch revision 门控拒绝。
//
// 断言两层（对齐组合根接线守卫风格）：
//  1. 行为层：全部依赖门禁就绪时 wireListProjectionFamily 装配成功（激活
//     hook 构造在此路径上执行，业务库句柄复用本族入参，装配失败即上抛）；
//  2. 源码层：worker_projection_jobs.go 内禁止任何以 nil hook 调用
//     SyncAccountScheduleStatuses 的行（池级接线守卫表达不了的调用点级契约）。

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
)

func TestP0ProjectionFamilyWiresActivationHook(t *testing.T) {
	ctx := context.Background()
	unitDir := t.TempDir()
	assembly := w13g8NewUnitAssembly(t, func(config *workerConfig) {
		config.Driver = "postgres"
		config.ListProjectionEnabled = true
		config.RedisStateURL = "redis://127.0.0.1:6379/9"
		config.RedisNamespace = "p0"
	})
	defer assembly.closeStores()
	business, err := assembly.openSQLite(filepath.Join(unitDir, "projection-business.sqlite3"), "p0-projection")
	if err != nil {
		t.Fatal(err)
	}
	oauthDB, err := sql.Open("sqlite", filepath.Join(unitDir, "oauth.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = oauthDB.Close() })
	oauthStore, err := oauthrefresh.OpenStore(oauthDB, oauthrefresh.StoreSQLite, "p0-projection-secret")
	if err != nil {
		t.Fatal(err)
	}
	assembly.oauthStore = oauthStore
	// 全部门禁就绪：本任务族（PG-only 物化器）走完整装配路径，激活 hook 与
	// 同步闭包在该路径上构造。Redis 客户端惰性建连，无需真实实例。
	if err := assembly.wireListProjectionFamily(ctx, &businessDB{db: business}, &proberepo.Store{}); err != nil {
		t.Fatalf("投影族装配必须成功（含激活 hook 构造）: %v", err)
	}
	wired := false
	for _, name := range assembly.wiredJobs {
		if name == "account-list-availability-projection-maintenance" {
			wired = true
			break
		}
	}
	if !wired {
		t.Fatalf("任务必须注册，wiredJobs=%v", assembly.wiredJobs)
	}
}

func TestP0ProjectionSyncSchedulesNeverNilHook(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("worker_projection_jobs.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "newAccountScheduleActivationHook(") {
		t.Fatal("worker_projection_jobs.go 必须接线 newAccountScheduleActivationHook（排期激活 dispatch 推进）")
	}
	for index, line := range strings.Split(string(source), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.Contains(line, "SyncAccountScheduleStatuses(") && strings.Contains(line, "nil)") {
			t.Fatalf("worker_projection_jobs.go:%d 禁止以 nil hook 调用 SyncAccountScheduleStatuses（激活翻转将不解除 dispatch revision 门控）: %s", index+1, trimmed)
		}
	}
}
