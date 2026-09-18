// 波次 w13g8：modelcheckapp host.go 剩余臂的登记与可触达分支补测。
//
// 不可达语句归因（host.go，均为生产契约行为，不在测试侧强行驱动）：
//   - 74.17-77.4（business SQLite sql.Open 失败）：sqlite 驱动经空白导入静态
//     注册，sql.Open 惰性连接不再出错（w14i 同款登记；未知查询参数也会被
//     modernc sqlite 宽松忽略，本文件 TestW13G8OpenHostSQLiteUnknownDSNParam
//     按当前行为固定该事实），属防御分支。
//   - 157.2-164.161（Service/Handler 组装成功链）：modelcheckexecutor.TargetResolver
//     是函数类型而非接口，OpenHost 在 152 行对 *modelchecksource.SQLiteReader
//     做类型断言必然失败并 fail-closed（wo_host_test.go 已按当前行为断言）；
//     成功组装分支在当前实现下不可达。
//   - 87.17-90.4（policy.NewSQLiteReader 失败）：业务库已通过 sql.Open 成功，
//     NewSQLiteReader 仅在连接/参数非法时失败，构造参数均来自已校验的
//     business 连接，属防御分支。
//   - 139.16-142.3（modelcheckauth.New 失败）：mode 为编译期常量、连接非 nil，
//     New 不可能失败（w14i 已登记），属防御分支。
//   - 98.17-101.4 / 103.17-106.4 / 117.17-120.4（PostgreSQL 模式的 dataset/
//     business 打开与 policy 读取失败分支）：需要"durable 打开成功而 dataset
//     打开失败"等不可注入的 PG 故障组合；PG 装配成功链本身归 w14q。
package modelcheckapp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckruntime"
	_ "modernc.org/sqlite"
)

// TestW13G8OpenHostSQLiteUnknownDSNParam 固定防御分支 74.17-77.4 的现状：
// modernc sqlite 对未知查询参数宽松忽略，sql.Open 惰性连接不校验 DSN 参数，
// 业务库路径附带未知参数时 OpenHost 继续走到业务读取器契约校验并报缺表。
func TestW13G8OpenHostSQLiteUnknownDSNParam(t *testing.T) {
	dir := t.TempDir()
	cfg := modelcheckruntime.RuntimeConfig{
		Enabled:              true,
		StoreMode:            "sqlite",
		JobsDatabasePath:     filepath.Join(dir, "jobs.sqlite3"),
		DatasetDatabasePath:  filepath.Join(dir, "dataset.sqlite3"),
		BusinessDatabasePath: filepath.Join(dir, "business.sqlite3") + "?bogus_w13g8_param=1",
		CredentialSecret:     "w13g8-credential",
		IdentitySecret:       "w13g8-identity",
		ProbeSetVersion:      "w13g8-probe-v1",
		Deadline:             time.Minute,
	}
	_, err := OpenHost(context.Background(), cfg)
	if err == nil {
		t.Fatal("空业务库必须触发契约校验失败")
	}
	if !strings.Contains(err.Error(), "verify J3b business reader contract") {
		t.Fatalf("错误应指向业务读取器契约（证明 sql.Open 未因未知参数失败）: %v", err)
	}
}
