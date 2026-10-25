//go:build windows

package main

// w1_boot_owner_test.go —— owner 二进制级启动场景的共享 PG 夹具帮手。
//
// 历史沿革：本文件原承载场景 W1V2（sqlite 组合根 owner 完整启动），该场景
// 的被测形态（sqlite 六库组合根）已随 PLAN-20261024T051925945Z sqlite 存储
// 形态退役删除；其 PG 对应物（owner 全栈 PG 启动 → /health ready → 管理面
// → CTRL_BREAK exit 0）由 w1_pg_wave_test.go 的 TestW1G2OwnerFullBootPostgresBusinessMode
// 承载（D9：有 PG 对应物的 sqlite 臂删除，PG 臂保留）。保留的共享帮手：
//
//   - w1v2PrepareBusinessPostgres：pgtestkit 隔离库 + maintenance
//     bootstrap.EnsurePostgres/SeedPostgres fresh DDL+种子（等价原
//     w1v2PrepareBusinessSQLite 的 ensure+seed 面），供 owner 子进程场景
//     （w1_boot_arms M4、w1_owner_acquire_wait、w1_main_tail）注入
//     POSTGRES_URL/BUSINESS_POSTGRES_URL。
//   - w1v2OwnerFailMarkers：owner 启动路径 fail() 输出特征采样（w1_pg_wave
//     场景 C 与 w1_main_tail 优雅关闭断言共用）。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// w1v2RuntimeSecret 是 owner 场景夹具库种子的加密密钥（仅测试期使用）。
const w1v2RuntimeSecret = "w1v2-owner-full-boot-secret"

// w1v2PrepareBusinessPostgres 创建 pgtestkit 隔离测试库并应用 maintenance
// bootstrap 的 fresh DDL + 种子（原 w1v2PrepareBusinessSQLite 的 PG 形态）。
func w1v2PrepareBusinessPostgres(t *testing.T) *pgtestkit.TestDatabase {
	t.Helper()
	return pgtestkit.NewTestDatabase(t,
		func(ctx context.Context, db *sql.DB) error {
			_, err := bootstrap.EnsurePostgres(ctx, db)
			return err
		},
		func(ctx context.Context, db *sql.DB) error {
			_, err := bootstrap.SeedPostgres(ctx, db, bootstrap.SeedOptions{Now: func() time.Time { return time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC) }, Secret: w1v2RuntimeSecret})
			return err
		})
}

// w1v2OwnerFailMarkers 是 main.go owner 分支 fail() 输出的特征文案采样；
// 优雅路径的 combined 输出不得包含其中任何一项。（sqlite 存储专有文案标记
// 已随形态退役删除。）
var w1v2OwnerFailMarkers = []string{
	"load gateway runtime config",
	"verify business owner gates",
	"cutover evidence",
	"load J3b gateway owner config",
	"J3b Gateway authenticator",
	"J3b Gateway session retention",
	"J3b Gateway circuit control-plane",
	"J3b Gateway circuit runtime",
	"J3b Gateway key-model runtime",
	"J3b Gateway circuit projector",
	"J3b Gateway enforcement owner",
	"J3b Gateway recovery owner",
	"J3b Gateway quality manager",
	"J3b Gateway scheduler contract",
	"J3b Gateway tokenizer",
	"J3b Gateway model-limit source",
	"J3b Gateway usage stats timezone",
	"open J3b Gateway owner host",
	"mount J3b Gateway",
	"load F3 audit-log config",
	"open F3 audit-log store",
	"initialize F3 audit-log schema",
	"acquire F3 audit owner lease",
	"F3 audit owner lease held",
	"load F4 operation-log config",
	"open F4 operation-log store",
	"initialize F4 operation-log schema",
	"acquire F4 operation-log owner lease",
	"F4 operation log owner lease held",
	"compose gateway system api",
	"listen gateway system api endpoint",
	"listen gateway health endpoint",
	"create internal gateway registry",
	"shutdown gateway system api endpoint",
	"gateway system api endpoint stopped",
	"J3b management endpoint stopped",
	"gateway component supervisor stopped",
	"shutdown gateway health endpoint",
	"gateway health endpoint stopped",
}
