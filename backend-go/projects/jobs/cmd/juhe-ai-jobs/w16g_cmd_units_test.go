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
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/cleanuprepo"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsverify"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/taskruns"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
)

const w16gNamespace = "juhe-ai:w16g"

// w16gGarbageSQLite 写一个非 SQLite 文件（openSQLite 的 PRAGMA WAL 立即失败）。
func w16gGarbageSQLite(t *testing.T, path string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte("w16g not a sqlite database"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// w16gMissingDirPath 返回不存在目录下的路径（openSQLite 建库必然失败）。
func w16gMissingDirPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "missing-dir", name)
}

// w16gValidSQLitePath 返回空库路径（openSQLite 的 PRAGMA WAL 会物理建库）。
func w16gValidSQLitePath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}

// w16gCreateView 在 SQLite 文件上建视图占位表（CREATE TABLE IF NOT EXISTS
// 撞同名视图必然报错，用于驱动 EnsureSchema 错误臂）。
func w16gCreateView(t *testing.T, path, view string) {
	t.Helper()
	db, err := sqlOpenSQLiteFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE VIEW " + view + " AS SELECT 1"); err != nil {
		t.Fatal(err)
	}
}

func TestW16GRetentionOpenDualAndCodexArms(t *testing.T) {
	root := t.TempDir()
	retentionConfig := func(mutate func(config *workerConfig)) workerConfig {
		config := workerConfig{
			Driver:             "sqlite",
			InstanceID:         "w16g-retention",
			BusinessSQLitePath: filepath.Join(root, "business.sqlite3"),
			StatsSQLitePath:    filepath.Join(root, "stats.sqlite3"),
			// 3dd1bb310 起 wireRetentionFamily 对 task-runs 库走 openDual
			// 硬依赖（background_task_runs/background_job_leases 保留清理），
			// fixture 必须与共享 retentionTestConfig 一样提供该路径。
			TaskRunsSQLitePath:          filepath.Join(root, "task-runs.sqlite3"),
			DatasetSQLitePath:           filepath.Join(root, "dataset.sqlite3"),
			ChatSQLitePath:              filepath.Join(root, "chat.sqlite3"),
			UsageCatalogSQLitePath:      filepath.Join(root, "usage-catalog.sqlite3"),
			CodexContextStateShardRoot:  filepath.Join(root, "codex-state"),
			CodexContextStateShardCount: 1,
		}
		if mutate != nil {
			mutate(&config)
		}
		return config
	}
	// 「家族未启用直接返回」臂已删除（2026-09-19 家族开关移除：retention 恒装配）。
	t.Run("postgres 空 URL acquirePool 失败", func(t *testing.T) {
		assembly := newWorkerAssembly(retentionConfig(func(config *workerConfig) {
			config.Driver = "postgres"
			config.PostgresURL = ""
		}), nil)
		t.Cleanup(assembly.closeStores)
		if err := assembly.wireRetentionFamily(context.Background()); err == nil {
			t.Fatal("空 PostgresURL 必须使 openDual 失败")
		}
	})
	t.Run("业务库路径目录非法", func(t *testing.T) {
		assembly := newWorkerAssembly(retentionConfig(func(config *workerConfig) {
			config.BusinessSQLitePath = w16gMissingDirPath(t, "business.sqlite3")
		}), nil)
		t.Cleanup(assembly.closeStores)
		if err := assembly.wireRetentionFamily(context.Background()); err == nil {
			t.Fatal("业务库目录非法必须失败")
		}
	})
	for _, item := range []struct {
		name     string
		brokenDB string
		label    string
	}{
		{"stats 库非法", "StatsSQLitePath", "retention-stats"},
		{"dataset 库非法", "DatasetSQLitePath", "retention-dataset"},
		{"chat 库非法", "ChatSQLitePath", "retention-chat"},
		{"usage-catalog 库非法", "UsageCatalogSQLitePath", "retention-usage-catalog"},
	} {
		t.Run(item.name, func(t *testing.T) {
			badPath := w16gMissingDirPath(t, "broken.sqlite3")
			assembly := newWorkerAssembly(retentionConfig(func(config *workerConfig) {
				switch item.brokenDB {
				case "StatsSQLitePath":
					config.StatsSQLitePath = badPath
				case "DatasetSQLitePath":
					config.DatasetSQLitePath = badPath
				case "ChatSQLitePath":
					config.ChatSQLitePath = badPath
				case "UsageCatalogSQLitePath":
					config.UsageCatalogSQLitePath = badPath
				}
			}), nil)
			t.Cleanup(assembly.closeStores)
			err := assembly.wireRetentionFamily(context.Background())
			if err == nil || !strings.Contains(err.Error(), item.label) {
				t.Fatalf("%s 必须在 %s 打开处失败: %v", item.name, item.label, err)
			}
		})
	}
	t.Run("SQLite 模式缺 codex shard root", func(t *testing.T) {
		assembly := newWorkerAssembly(retentionConfig(func(config *workerConfig) {
			config.CodexContextStateShardRoot = ""
		}), nil)
		t.Cleanup(assembly.closeStores)
		err := assembly.wireRetentionFamily(context.Background())
		if err == nil || !strings.Contains(err.Error(), "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT") {
			t.Fatalf("缺 codex shard root 必须报错: %v", err)
		}
	})
	t.Run("codex shard count 小于 1", func(t *testing.T) {
		assembly := newWorkerAssembly(retentionConfig(func(config *workerConfig) {
			config.CodexContextStateShardCount = 0
		}), nil)
		t.Cleanup(assembly.closeStores)
		err := assembly.wireRetentionFamily(context.Background())
		if err == nil || !strings.Contains(err.Error(), ">= 1") {
			t.Fatalf("codex shard count=0 必须报错: %v", err)
		}
	})
}

func TestW16GRetentionRecordMaintenanceViewConflictArm(t *testing.T) {
	// recordmaintenance.OpenStore 的 nil 句柄错误臂（直接构造家族）。
	if err := (&workerAssembly{}).wireRecordMaintenanceTableDrain(&retentionFamily{}, &cleanuprepo.DB{}, nil); err == nil {
		t.Fatal("nil 业务库句柄必须使 record_maintenance store 打开失败")
	}
	// 业务库上以视图占位 record_maintenance_jobs → EnsureSchema 冲突 →
	// drain 装配失败并从 wireRetentionFamily 传播（302-304 + 62-64）。
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	w16gCreateView(t, businessPath, "record_maintenance_jobs")
	assembly := newWorkerAssembly(workerConfig{
		Driver:                      "sqlite",
		InstanceID:                  "w16g-retention-view",
		BusinessSQLitePath:          businessPath,
		StatsSQLitePath:             filepath.Join(root, "stats.sqlite3"),
		TaskRunsSQLitePath:          filepath.Join(root, "task-runs.sqlite3"),
		DatasetSQLitePath:           filepath.Join(root, "dataset.sqlite3"),
		ChatSQLitePath:              filepath.Join(root, "chat.sqlite3"),
		UsageCatalogSQLitePath:      filepath.Join(root, "usage-catalog.sqlite3"),
		CodexContextStateShardRoot:  filepath.Join(root, "codex-state"),
		CodexContextStateShardCount: 1,
	}, nil)
	t.Cleanup(assembly.closeStores)
	err := assembly.wireRetentionFamily(context.Background())
	if err == nil || !strings.Contains(err.Error(), "record_maintenance_jobs") {
		t.Fatalf("视图占位必须使交接表 schema 初始化失败: %v", err)
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
				Driver:                 "postgres",
				InstanceID:             "w16g-pg",
				PostgresURL:            "",
				PostgresMaxOpenConns:   2,
				PostgresMaxIdleConns:   1,
				StatsSQLitePath:        w16gValidSQLitePath(t, "unused.sqlite3"),
				BusinessSQLitePath:     w16gValidSQLitePath(t, "unused-business.sqlite3"),
				UsageCatalogSQLitePath: w16gValidSQLitePath(t, "unused-catalog.sqlite3"),
				Secret:                 wgBalanceSecret,
			}
			assembly := newWorkerAssembly(config, nil)
			if err := family.wire(assembly); err == nil {
				t.Fatalf("空 PostgresURL 的 %s 家族必须失败", family.name)
			}
		})
	}
}

func TestW16GSQLiteBadPathArms(t *testing.T) {
	t.Run("oauth 业务库打开失败", func(t *testing.T) {
		assembly := newWorkerAssembly(workerConfig{
			Driver:             "sqlite",
			InstanceID:         "w16g-oauth",
			Secret:             wgBalanceSecret,
			BusinessSQLitePath: w16gGarbageSQLite(t, filepath.Join(t.TempDir(), "oauth-garbage.sqlite3")),
		}, nil)
		t.Cleanup(assembly.closeStores)
		if err := assembly.wireOAuthFamily(context.Background()); err == nil {
			t.Fatal("垃圾业务库必须使 oauth 家族失败")
		}
	})
	t.Run("usage-writer 业务库打开失败", func(t *testing.T) {
		assembly := newWorkerAssembly(workerConfig{
			Driver:                 "sqlite",
			InstanceID:             "w16g-usage",
			UsageCatalogSQLitePath: w16gValidSQLitePath(t, "catalog.sqlite3"),
			UsageShardRoot:         filepath.Join(t.TempDir(), "shards"),
			BusinessSQLitePath:     w16gGarbageSQLite(t, filepath.Join(t.TempDir(), "usage-garbage.sqlite3")),
		}, nil)
		t.Cleanup(assembly.closeStores)
		if err := assembly.wireUsageWriterFamily(context.Background()); err == nil {
			t.Fatal("垃圾业务库必须使 usage-writer 家族失败")
		}
	})
	t.Run("probe 业务库打开失败", func(t *testing.T) {
		assembly := newWorkerAssembly(workerConfig{
			Driver:             "sqlite",
			InstanceID:         "w16g-probe",
			Secret:             wgBalanceSecret,
			BusinessSQLitePath: w16gGarbageSQLite(t, filepath.Join(t.TempDir(), "probe-garbage.sqlite3")),
		}, nil)
		t.Cleanup(assembly.closeStores)
		if err := assembly.wireProbeFamily(context.Background()); err == nil {
			t.Fatal("垃圾业务库必须使探针族失败")
		}
	})
}

func TestW16GViewConflictAndCancelledCtxArms(t *testing.T) {
	t.Run("probe EnsureSchema 视图冲突登记 disabled", func(t *testing.T) {
		businessPath := filepath.Join(t.TempDir(), "business.sqlite3")
		w16gCreateView(t, businessPath, "account_api_key_runtime_states")
		assembly := newWorkerAssembly(workerConfig{
			Driver:             "sqlite",
			InstanceID:         "w16g-probe-view",
			Secret:             wgBalanceSecret,
			BusinessSQLitePath: businessPath,
		}, nil)
		t.Cleanup(assembly.closeStores)
		if err := assembly.wireProbeFamily(context.Background()); err != nil {
			t.Fatalf("视图冲突必须 fail closed 登记而非报错: %v", err)
		}
		found := false
		for _, disabled := range assembly.disabledJobs {
			if disabled.JobName == "account-quality-refresh" {
				found = true
			}
		}
		if !found {
			t.Fatalf("探针族必须登记 disabled: %+v", assembly.disabledJobs)
		}
	})
	t.Run("usage catalog EnsureSchema 视图冲突", func(t *testing.T) {
		catalogPath := filepath.Join(t.TempDir(), "catalog.sqlite3")
		w16gCreateView(t, catalogPath, "usage_record_shards")
		assembly := newWorkerAssembly(workerConfig{
			Driver:                 "sqlite",
			InstanceID:             "w16g-usage-view",
			UsageCatalogSQLitePath: catalogPath,
			UsageShardRoot:         filepath.Join(t.TempDir(), "shards"),
			BusinessSQLitePath:     w16gValidSQLitePath(t, "business.sqlite3"),
		}, nil)
		t.Cleanup(assembly.closeStores)
		err := assembly.wireUsageWriterFamily(context.Background())
		if err == nil || !strings.Contains(err.Error(), "usage-writer catalog schema") {
			t.Fatalf("视图占位必须使 catalog schema 初始化失败: %v", err)
		}
	})
	// 说明：probe 族 stats EnsureSchema（worker_probe_jobs.go 94-96）错误臂
	// 不可在 t.TempDir 卫生约束下驱动——错误返回前 statsStore 句柄未登记进
	// closers，测试侧无法关闭（句柄不可达），TempDir 清理必然失败。该臂连同
	// 装配错误路径的句柄滞留登记为已知边界，不在本波次覆盖。
	t.Run("stats EnsureSchema 取消 ctx", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		assembly := newWorkerAssembly(workerConfig{
			Driver:             "sqlite",
			InstanceID:         "w16g-stats-ctx",
			StatsSQLitePath:    w16gValidSQLitePath(t, "stats.sqlite3"),
			BusinessSQLitePath: w16gValidSQLitePath(t, "business.sqlite3"),
		}, nil)
		t.Cleanup(assembly.closeStores)
		if err := assembly.wireStatsFamily(ctx); err == nil {
			t.Fatal("取消 ctx 必须使 stats-verify schema 初始化失败")
		}
	})
}

func TestW16GBalanceDetectSnapshotTableMissingArm(t *testing.T) {
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	seedProbeCoreTables(t, businessPath)
	business := w16dOpenTestSQLite(t, businessPath)
	for _, statement := range []string{
		`ALTER TABLE accounts ADD COLUMN balance_query_enabled INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE accounts ADD COLUMN balance_query_config_json TEXT`,
		`ALTER TABLE accounts ADD COLUMN balance_query_next_refresh_at TEXT`,
		`ALTER TABLE accounts ADD COLUMN proxy_profile_id TEXT`,
	} {
		if _, err := business.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	leaseStore, err := taskruns.OpenStore(taskruns.StoreConfig{Mode: taskruns.ModeSQLite, DatabasePath: filepath.Join(root, "leases.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = leaseStore.Close() })
	assembly := newWorkerAssembly(workerConfig{
		Driver:             "sqlite",
		InstanceID:         "w16g-balance",
		Secret:             wgBalanceSecret,
		BusinessSQLitePath: businessPath,
		StatsSQLitePath:    filepath.Join(root, "stats-empty.sqlite3"),
	}, nil)
	t.Cleanup(assembly.closeStores)
	assembly.taskRunsStore = leaseStore
	if err := assembly.wireBalanceDetectFamily(context.Background()); err != nil {
		t.Fatalf("快照表缺失必须 fail closed 登记而非报错: %v", err)
	}
	found := false
	for _, disabled := range assembly.disabledJobs {
		if strings.Contains(disabled.Reason, "统计库快照表校验失败") {
			found = true
		}
	}
	if !found {
		t.Fatalf("快照表缺失必须登记校验失败原因: %+v", assembly.disabledJobs)
	}
}

func TestW16GProjectionGateArms(t *testing.T) {
	db, err := sqlOpenSQLiteFile(filepath.Join(t.TempDir(), "probe-store.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	probeStore, err := proberepo.NewStore(proberepo.Config{DB: db, Secret: wgBalanceSecret})
	if err != nil {
		t.Fatal(err)
	}
	base := workerConfig{
		Driver:                "postgres",
		InstanceID:            "w16g-projection",
		ListProjectionEnabled: true,
		Secret:                wgBalanceSecret,
		RedisNamespace:        w16gNamespace,
	}
	t.Run("OAuth 族缺席登记 disabled", func(t *testing.T) {
		config := base
		config.RedisStateURL = "redis://127.0.0.1:1"
		assembly := newWorkerAssembly(config, nil)
		if err := assembly.wireListProjectionFamily(context.Background(), &businessDB{}, probeStore); err != nil {
			t.Fatalf("登记分支不得报错: %v", err)
		}
		found := false
		for _, disabled := range assembly.disabledJobs {
			if strings.Contains(disabled.Reason, "OAuth 族未装配") {
				found = true
			}
		}
		if !found {
			t.Fatalf("OAuth 缺席必须登记: %+v", assembly.disabledJobs)
		}
	})
	t.Run("overlay Redis URL 解析失败", func(t *testing.T) {
		config := base
		config.RedisStateURL = "http://127.0.0.1:1"
		oauthDB, err := sqlOpenSQLiteFile(filepath.Join(t.TempDir(), "oauth.sqlite3"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = oauthDB.Close() })
		oauthStore, err := oauthrefresh.OpenStore(oauthDB, oauthrefresh.StoreSQLite, wgBalanceSecret)
		if err != nil {
			t.Fatal(err)
		}
		assembly := newWorkerAssembly(config, nil)
		assembly.oauthStore = oauthStore
		if err := assembly.wireListProjectionFamily(context.Background(), &businessDB{}, probeStore); err == nil {
			t.Fatal("非法 Redis URL 必须使 overlay store 打开失败")
		}
	})
	t.Run("投影仓储 nil 业务库句柄", func(t *testing.T) {
		config := base
		config.RedisStateURL = "redis://127.0.0.1:1"
		oauthDB, err := sqlOpenSQLiteFile(filepath.Join(t.TempDir(), "oauth2.sqlite3"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = oauthDB.Close() })
		oauthStore, err := oauthrefresh.OpenStore(oauthDB, oauthrefresh.StoreSQLite, wgBalanceSecret)
		if err != nil {
			t.Fatal(err)
		}
		assembly := newWorkerAssembly(config, nil)
		assembly.oauthStore = oauthStore
		// overlay/runtime reader 构造成功后，nil 业务库句柄使 NewListAvailabilityRepo 失败。
		if err := assembly.wireListProjectionFamily(context.Background(), &businessDB{}, probeStore); err == nil {
			t.Fatal("nil 业务库句柄必须使投影仓储构造失败")
		}
	})
}

func TestW16GAssemblyComponentsLifecycle(t *testing.T) {
	env := probeWorkerTestEnv(t)
	env["JUHE_AI_USAGE_SPOOL_DIRECTORY"] = filepath.Join(t.TempDir(), "usage-spool")
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

func TestW16GWireHealthOutcomeProjectorStatsMarkerArm(t *testing.T) {
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	seedProbeCoreTables(t, businessPath)
	statsStore, err := statsverify.OpenStore(statsverify.StoreConfig{
		Mode:               statsverify.StoreSQLite,
		SQLiteStatsPath:    filepath.Join(root, "stats.sqlite3"),
		SQLiteBusinessPath: filepath.Join(root, "stats-business.sqlite3"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = statsStore.Close() })
	assembly := newWorkerAssembly(workerConfig{
		Driver:             "sqlite",
		InstanceID:         "w16g-projection-health",
		BusinessSQLitePath: businessPath,
	}, nil)
	t.Cleanup(assembly.closeStores)
	assembly.statsStore = statsStore
	projector, err := assembly.wireHealthOutcomeProjector(func(name string) string {
		return w16dJ1Env(t, root)[name]
	}, &accounthealth.Store{})
	if err != nil {
		t.Fatalf("statsStore 就位时投影装配必须成功: %v", err)
	}
	if projector == nil {
		t.Fatal("J1 启用时必须返回投影器")
	}
}

func TestW16GHealthOutboxArms(t *testing.T) {
	root := t.TempDir()
	env := func() func(string) string {
		base := w16dJ1Env(t, root)
		return func(name string) string { return base[name] }
	}
	t.Run("outbox schema 视图冲突", func(t *testing.T) {
		businessPath := filepath.Join(root, "view-business.sqlite3")
		w16gCreateView(t, businessPath, "account_health_probe_request_outbox")
		assembly := newWorkerAssembly(workerConfig{
			Driver:             "sqlite",
			InstanceID:         "w16g-face-view",
			BusinessSQLitePath: businessPath,
		}, nil)
		if _, err := assembly.wireHealthProbeOutboxFace(env()); err == nil {
			t.Fatal("视图占位必须使 outbox schema 初始化失败")
		}
	})
	t.Run("J1 + miniredis 配置 settler closer", func(t *testing.T) {
		redisServer := miniredis.RunT(t)
		businessPath := filepath.Join(root, "face-business.sqlite3")
		fixture := w16dOpenTestSQLite(t, businessPath)
		seedProbeCoreTables(t, businessPath)
		w16dCreateJ1FixtureTables(t, fixture)
		config := workerConfig{
			Driver:             "sqlite",
			InstanceID:         "w16g-face",
			BusinessSQLitePath: businessPath,
			RedisStateURL:      "redis://" + redisServer.Addr(),
			RedisNamespace:     w16gNamespace,
		}
		assembly := newWorkerAssembly(config, nil)
		face, err := assembly.wireHealthProbeOutboxFace(env())
		if err != nil {
			t.Fatalf("J1 + miniredis 的 face 装配: %v", err)
		}
		if face.drain == nil || face.pruner == nil {
			t.Fatalf("完整消费面必须就绪: %+v", face)
		}
		assembly.closeStores()
	})
	t.Run("boundary 输入版本表缺失报错", func(t *testing.T) {
		businessPath := filepath.Join(root, "boundary-business.sqlite3")
		fixture := w16dOpenTestSQLite(t, businessPath)
		seedProbeCoreTables(t, businessPath)
		if _, err := fixture.Exec(`INSERT INTO accounts (id, system_account_id, name, type, status, schedulable,
			credentials_encrypted, config_revision, dispatch_revision)
			VALUES ('w16g-boundary', 'sys-w16g', 'boundary', 'api_key', 'active', 1, '{}', 2, 3)`); err != nil {
			t.Fatal(err)
		}
		boundary := healthProbeBoundary{business: &businessDB{db: fixture}}
		if _, _, _, ok, err := boundary.CurrentProbeInput(context.Background(), "w16g-boundary"); err == nil || ok {
			t.Fatalf("输入版本表缺失必须报错: ok=%v err=%v", ok, err)
		}
	})
}

func TestW16GCircuitResolveArms(t *testing.T) {
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	seedProbeCoreTables(t, businessPath)
	db, err := sqlOpenSQLiteFile(businessPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`ALTER TABLE accounts ADD COLUMN proxy_profile_id TEXT`); err != nil {
		t.Fatal(err)
	}
	credentials, err := json.Marshal(map[string]any{"api_key": "sk-w16g", "base_url": "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := accountbalance.EncryptV1Envelope(wgBalanceSecret, credentials)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO accounts (id, system_account_id, name, type, status, schedulable, provider_code,
			credentials_encrypted, config_revision, dispatch_revision)
		 VALUES ('w16g-orphan', 'sys-w16g', 'orphan', 'api_key', 'active', 1, 'openai', ?, 1, 2)`,
		`INSERT INTO accounts (id, system_account_id, name, type, status, schedulable, provider_code,
			credentials_encrypted, config_revision, dispatch_revision)
		 VALUES ('w16g-missing-group', 'sys-w16g2', 'missing-group', 'api_key', 'active', 1, 'openai', ?, 1, 2)`,
		`INSERT INTO groups (id, system_account_id, provider_code) VALUES ('g-w16g', 'sys-w16g3', 'openai')`,
		`INSERT INTO group_accounts (group_id, system_account_id, account_id) VALUES ('g-w16g', 'sys-w16g3', 'w16g-zero-rev')`,
		`INSERT INTO accounts (id, system_account_id, name, type, status, schedulable, provider_code,
			credentials_encrypted, config_revision, dispatch_revision)
		 VALUES ('w16g-zero-rev', 'sys-w16g3', 'zero-rev', 'api_key', 'active', 1, 'openai', ?, 1, 0)`,
	}
	for index, statement := range statements {
		if index == 0 || index == 1 || index == 4 {
			if _, err := db.Exec(statement, envelope); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	probeStore, err := proberepo.NewStore(proberepo.Config{DB: db, Secret: wgBalanceSecret})
	if err != nil {
		t.Fatal(err)
	}
	// 预置探针族辅助表（account_supported_models / account_api_key_runtime_states），
	// 否则 LoadAccountForTest 的辅助读取会先于目标臂报错。
	if err := probeStore.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	resolver := circuitRecoveryTargetResolver{store: probeStore}
	resolve := func(runtimeKey string) (bool, error) {
		_, ok, err := resolver.Resolve(context.Background(), opsjobs.CircuitState{
			Scope: opsjobs.CircuitScope{AccountRuntimeKey: runtimeKey},
		})
		return ok, err
	}
	t.Run("owner 空分组臂", func(t *testing.T) {
		ok, err := resolve("w16g-orphan")
		if err != nil || ok {
			t.Fatalf("无绑定分组的 owner 身份必须 !ok: ok=%v err=%v", ok, err)
		}
	})
	t.Run("authorized 候选缺失臂", func(t *testing.T) {
		ok, err := resolve("w16g-missing-group:authorized:sys-w16g2:g-nope:auth-w16g")
		if err != nil || ok {
			t.Fatalf("分组缺失必须 !ok: ok=%v err=%v", ok, err)
		}
	})
	t.Run("dispatch revision 无效臂", func(t *testing.T) {
		ok, err := resolve("w16g-zero-rev:authorized:sys-w16g3:g-w16g:auth-w16g")
		if err != nil || ok {
			t.Fatalf("dispatch_revision=0 必须 !ok: ok=%v err=%v", ok, err)
		}
	})
}

func TestW16GCircuitControlPlaneNilDBArm(t *testing.T) {
	// 控制面仓储构造缺少业务库句柄（Redis 合法、业务库 nil → NewControlPlaneRepo 失败）。
	assembly := newWorkerAssembly(workerConfig{
		Driver:         "sqlite",
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

func TestW16GProbeFamilyCircuitErrorArm(t *testing.T) {
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	seedProbeCoreTables(t, businessPath)
	assembly := newWorkerAssembly(workerConfig{
		Driver:             "sqlite",
		InstanceID:         "w16g-probe-circuit",
		Secret:             wgBalanceSecret,
		BusinessSQLitePath: businessPath,
		StatsSQLitePath:    w16gValidSQLitePath(t, "stats.sqlite3"),
		RedisStateURL:      "http://127.0.0.1:1",
		RedisNamespace:     w16gNamespace,
	}, nil)
	t.Cleanup(assembly.closeStores)
	if err := assembly.wireProbeFamily(context.Background()); err == nil {
		t.Fatal("电路族 Redis URL 非法必须使探针族装配失败")
	}
}

func TestW16GProbeRecoveryRedisClosedArm(t *testing.T) {
	redisServer := miniredis.RunT(t)
	dir := t.TempDir()
	wgSeedAllJobsFixture(t, dir)
	env := wgAllJobsAssemblyEnv(t, dir, "redis://"+redisServer.Addr())
	assembly, err := buildWorkerAssembly(loadWorkerConfigOrFatal(t, env), nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()
	wired := false
	for _, name := range assembly.wiredJobs {
		if name == "normal-route-speed-first-recovery-probe" {
			wired = true
		}
	}
	if !wired {
		t.Fatalf("速度优先恢复探针必须接线: wired=%v disabled=%v", assembly.wiredJobs, assembly.disabledJobs)
	}
	redisServer.Close()
	if _, err := assembly.runWiredJobOnce(context.Background(), "normal-route-speed-first-recovery-probe"); err == nil {
		t.Fatal("Redis 关闭后候选读取必须报错")
	}
}

// TestW16GScanNullTimeArm 覆盖 scanNullTime 的 PG []byte 解析失败分支。
// 原「探针族缺业务库路径必须 fail closed」臂已删除（2026-09-19 家族开关
// 移除 + DATA_DIR 派生：业务库路径派生后恒非空）。
func TestW16GScanNullTimeArm(t *testing.T) {
	if _, err := scanNullTime(true, []byte("w16g-not-a-time")); err == nil {
		t.Fatal("PG []byte 非 RFC3339 必须解析失败")
	}
}
