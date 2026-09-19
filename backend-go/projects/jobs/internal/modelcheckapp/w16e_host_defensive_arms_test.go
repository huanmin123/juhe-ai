// 波次 w16e：modelcheckapp host.go 全部未覆盖语句的终局触发性核查与事实锁定。
//
// 基线（全部既有测试含 PG 门控真实执行）：113 条语句，16 条未覆盖，
// 覆盖率 85.8%。未覆盖块与触发性结论（均已按上游依赖源码逐一核对）：
//
//	74.17-77.4   business SQLite sql.Open 失败臂：modernc.org/sqlite v1.58.0
//	             全模块无 OpenConnector（未实现 driver.DriverContext），
//	             database/sql 对非 DriverContext 驱动的 Open 恒不报错；
//	             w13g8 已锁定未知 DSN 参数宽松忽略，属防御分支。
//	87.17-90.4   modelcheckpolicy.NewSQLiteReader 失败臂：newReader 仅在
//	             db==nil 时报错（reader.go:31-33）；OpenHost 的 business
//	             连接经 sql.Open 成功后必非 nil，不可达。
//	98.17-101.4  dataset OpenPostgres 失败臂：与 durable 使用同一
//	             cfg.JobsPostgresURL；sqlpool.MaxIdleConns 为 const 10
//	             （registry.go:17），ValidatePoolLimits(1000, 10) 确定性
//	             通过，pgx 的 sql.Open 恒成功，故 dataset 只能在 durable
//	             先失败的组合中失败，经 OpenHost 不可达。
//	103.17-106.4 business PostgreSQL sql.Open 失败臂：pgx v5.10.0
//	             stdlib 的 Driver.OpenConnector 只构造惰性 connector、
//	             不解析 DSN（stdlib/sql.go:335-337），sql.Open("pgx", …)
//	             恒成功；DSN 解析失败延迟到 Connect（EnsureSchema 阶段）。
//	             TestW16eOpenHostPostgresBusinessDSNParseDefersToConnect
//	             在 OpenHost 层固定该事实。
//	117.17-120.4 modelcheckpolicy.NewPostgresReader 失败臂：同 87-90，
//	             仅 db==nil 时报错，不可达。
//	139.16-142.3 modelcheckauth.New 失败臂：New 仅在 db==nil 或 mode 非法
//	             时报错（auth.go:53-55）；OpenHost 传入非 nil 连接与编译期
//	             常量 mode，不可达。
//	158.9-161.3  "does not implement target resolution" 臂：sourceAny 动态
//	             类型恒为 *modelchecksource.SQLiteReader/*PostgresReader，
//	             两者均实现签名完全一致的 Resolve（sqlite_reader.go:134、
//	             postgres_reader.go:150）；双模式装配成功用例已证明断言
//	             恒真，失败臂不可达。
//	165.9-168.3  "does not implement management scope resolution" 臂：两个
//	             reader 均实现 ResolveManagementSystemAccount（即
//	             modelcheckhttp.ManagementTargetScopeReader），同上不可达。
//
// 结论：在只写本包 *_test.go、不修改 host.go、不注入生产依赖的边界内，
// 16 条未覆盖语句全部不可触达，语句覆盖率理论上限即 85.8%（97/113）。
// ≥95% 目标需改生产代码（如移除防御分支或开放注入点），超出本波次授权。
// 后续波次已按本证据删除 host.go 全部 8 个死守卫臂（留注释可追溯）；
// 下方两个守护测试保留，正向断言继续锁定上游驱动行为。
package modelcheckapp

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestW16eOpenHostPostgresBusinessDSNParseDefersToConnect 固定 103.17-106.4
// 证据的前提：pgx v5.10.0 的 sql.Open 惰性到 Connect 才解析 DSN，business
// 连接串携带必然解析失败的 sslmode 值时，OpenHost 不会在 business sql.Open
// 处失败，而是按装配顺序先在 durable EnsureSchema（127.0.0.1:1 连接拒绝）
// 处 fail-closed。原负向断言针对的 "open J3b business PostgreSQL" 臂已按
// 本文件头部证据删除，故仅保留正向断言继续锁定该驱动行为。
func TestW16eOpenHostPostgresBusinessDSNParseDefersToConnect(t *testing.T) {
	// 契约：business DSN 的语法校验属于连接期而非打开期；OpenHost 的
	// 失败信息必须来自首个真实连接点（durable schema 校验）。
	cfg := validSQLiteConfig(t)
	cfg.StoreMode = "postgres"
	cfg.JobsPostgresURL = "postgres://juhe:secret@127.0.0.1:1/juhe_jobs?connect_timeout=1"
	cfg.BusinessPostgresURL = "postgres://juhe:secret@127.0.0.1:1/juhe_business?sslmode=w16e-bogus-sslmode"
	host, err := OpenHost(context.Background(), cfg)
	if err == nil {
		_ = host.Close()
		t.Fatalf("durable 不可达时 OpenHost 应报错")
	}
	if host != nil {
		t.Fatalf("失败路径应返回 nil Host")
	}
	if !strings.Contains(err.Error(), "verify J3b durable schema") {
		t.Fatalf("错误应指向 durable schema 校验（证明 business sql.Open 未因非法 sslmode 失败）: %v", err)
	}
}

// TestW16eOpenHostSQLiteAssemblySurvivesRepeatedOpenClose 锁定 SQLite 装配
// 成功链（155-170 行鸭子断言与 Handler/Service 组装）的可重放性：同一
// 配置连续 Open/Close 三次，每次都应完整装配且 Source 契约可复查。
// 该用例同时守护 158-161 / 165-168 两个断言失败臂所依赖的前提
// （reader 实现两组接口）不被未来签名漂移破坏。
func TestW16eOpenHostSQLiteAssemblySurvivesRepeatedOpenClose(t *testing.T) {
	for i := 0; i < 3; i++ {
		cfg := validSQLiteConfig(t)
		prepareSQLiteBusinessDB(t, cfg.BusinessDatabasePath, sqliteBusinessFixtureSchema)
		host, err := OpenHost(context.Background(), cfg)
		if err != nil {
			t.Fatalf("第 %d 次 OpenHost 应完整装配: %v", i+1, err)
		}
		if !host.Ready() {
			t.Fatalf("第 %d 次 Host 应就绪", i+1)
		}
		if host.Handler == nil || host.Service == nil {
			t.Fatalf("第 %d 次 Handler/Service 应完成组装", i+1)
		}
		if err := host.Source.CheckContract(context.Background()); err != nil {
			t.Fatalf("第 %d 次 Source 契约复查失败: %v", i+1, err)
		}
		if cfg.Deadline != time.Minute {
			t.Fatalf("Config 应原样保留入参: %+v", cfg)
		}
		if err := host.Close(); err != nil {
			t.Fatalf("第 %d 次 Close 应返回 nil: %v", i+1, err)
		}
	}
}
