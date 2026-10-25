// 波次 w16g：覆盖率续作（进程内可驱动残余臂）。覆盖目标为安全口径
// （-skip 全部 spawn 型测试）下仍为零覆盖、且可在进程内确定性驱动的错误臂：
//   - retention openDual/codex 门禁臂与 record_maintenance_jobs 视图占位冲突臂；
//   - postgres 空 URL 的各 wire* 家族 acquirePool 错误臂；
//   - SQLite 垃圾文件/视图占位（CREATE TABLE IF NOT EXISTS 撞视图）错误臂；
//   - components supervisor 闭包（writer/drain/Close）与 statusPayload；
//   - 电路恢复解析器 owner 空分组 / 候选缺失 / dispatch revision 无效臂；
//   - loadWorkerConfig 探针族缺业务库路径臂与 scanNullTime PG 文本解析臂。
//
// 全部进程内，无 spawn；Redis 依赖用 miniredis。数据一律 w16g- 前缀。
package main

import (
	"database/sql"
	"path/filepath"
	"context"
	"testing"

)

const w16gNamespace = "juhe-ai:w16g"






func TestW16GRetentionOpenDualAndCodexArms(t *testing.T) {
	// PG 唯一形态：保留空 PostgresURL 的 acquirePool 失败臂（原 sqlite 路径
	// 族失败臂与 codex shard 根校验臂随 sqlite 形态退役删除）。
	assembly := newWorkerAssembly(workerConfig{InstanceID: "w16g-retention"}, nil)
	t.Cleanup(assembly.closeStores)
	if err := assembly.wireRetentionFamily(context.Background()); err == nil {
		t.Fatal("空 PostgresURL 必须使 retention 家族池获取失败")
	}
}

func TestW16GPGBadURLWireFamilyArms(t *testing.T) {
	families := []struct {
		name string
		wire func(assembly *workerAssembly) error
	}{
		{"task-runs", func(assembly *workerAssembly) error {
			return assembly.wireTaskRunsFamily(context.Background())
		}},
		{"stats", func(assembly *workerAssembly) error {
			return assembly.wireStatsFamily(context.Background())
		}},
		{"oauth", func(assembly *workerAssembly) error {
			return assembly.wireOAuthFamily(context.Background())
		}},
		{"usage-writer", func(assembly *workerAssembly) error {
			return assembly.wireUsageWriterFamily(context.Background())
		}},
		{"schedule-settings", func(assembly *workerAssembly) error {
			return assembly.wireScheduleSettings()
		}},
	}
	for _, family := range families {
		t.Run(family.name, func(t *testing.T) {
			config := workerConfig{
				InstanceID:             "w16g-pg",
				PostgresURL:            "",
				PostgresMaxOpenConns:   2,
				PostgresMaxIdleConns:   1,
				Secret:                 wgBalanceSecret,
			}
			assembly := newWorkerAssembly(config, nil)
			if err := family.wire(assembly); err == nil {
				t.Fatalf("空 PostgresURL 的 %s 家族必须失败", family.name)
			}
		})
	}
}

func TestW16GAssemblyComponentsLifecycle(t *testing.T) {
	// PG 唯一形态：stats-verify schema 初始化是只读 18 表契约（外部
	// bootstrap 责任，独立空库必然缺失）→ 覆盖库门禁（w12c/w14q 同例，
	// 连接串不可用即 skip）。探针/余额族在覆盖库上按契约接线或登记
	// disabled，writer/drain/scheduler 组件面不受影响。
	pgURL := w12cPGOverrideURL(t)
	if pgURL == "" {
		t.Skip("w16g: 覆盖库连接串不可用")
	}
	if db, err := sql.Open("pgx", pgURL); err == nil {
		w14jEnsurePGFixture(t, db)
		_ = db.Close()
	}
	env := map[string]string{
		"JUHE_AI_POSTGRES_URL":             pgURL,
		"JUHE_AI_RUNTIME_LOG_POSTGRES_URL": pgURL,
		"JUHE_AI_DATA_DIR":                 filepath.Join(t.TempDir(), "data"),
		"JUHE_AI_SECRET":                   "0123456789abcdef0123456789abcdef",
		"JUHE_AI_LOG_DIR":                  filepath.Join(t.TempDir(), "logs"),
	}
	assembly, err := buildWorkerAssembly(loadWorkerConfigOrFatal(t, env), nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()

	components := assembly.components()
	names := map[string]bool{}
	for _, component := range components {
		names[component.Name] = true
	}
	if !names["usage-record writer"] || !names["usage-record spool drain"] {
		t.Fatalf("writer/drain 组件必须就位: %v", names)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, component := range components {
		switch component.Name {
		case "usage-record writer", "usage-record spool drain", "worker scheduler":
			if err := component.Run(ctx); err != nil {
				t.Fatalf("组件 %s 取消 ctx 直跑必须正常返回: %v", component.Name, err)
			}
		}
	}
	payload := assembly.statusPayload()
	if enabled, ok := payload["workerEnabled"].(bool); !ok || !enabled {
		t.Fatalf("statusPayload 必须报告 workerEnabled: %+v", payload["workerEnabled"])
	}
	components[0].Close()
}

func TestW16GCircuitControlPlaneNilDBArm(t *testing.T) {
	// 控制面仓储构造缺少业务库句柄（Redis 合法、业务库 nil → NewControlPlaneRepo 失败）。
	assembly := newWorkerAssembly(workerConfig{

		InstanceID:     "w16g-circuit-nil",
		RedisStateURL:  "redis://127.0.0.1:1",
		RedisNamespace: w16gNamespace,
	}, nil)
	if err := assembly.wireCircuitFamily(context.Background(), &businessDB{}, func(name, reason string) {
		t.Fatalf("nil 句柄必须传播错误而非登记 disabled: %s %s", name, reason)
	}); err == nil {
		t.Fatal("nil 业务库句柄必须使控制面仓储构造失败")
	}
}
