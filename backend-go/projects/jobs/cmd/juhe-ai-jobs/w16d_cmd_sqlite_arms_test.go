// 波次 w16d：SQLite 臂批次二。覆盖 worker_health_probe_outbox.go（store/
// boundary/pruner/face 装配臂）、worker_health_projection.go（投影装配臂）、
// worker_circuit_jobs.go（电路族装配与 Resolve 臂）与 worker_business_db.go
// 的契约校验臂。全部进程内，不依赖 PG；Redis 依赖用 miniredis 或非法 URL
// 触发解析错误。数据一律 w16d- 前缀并用 t.Cleanup 清理。
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
	_ "modernc.org/sqlite"
)

// w16dOpenTestSQLite 打开独立 SQLite 文件并触发物理建库。
func w16dOpenTestSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sqlOpenSQLiteFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// w16dCreateJ1FixtureTables 建 outbox 消费面依赖的最小业务表（加法幂等）。
func w16dCreateJ1FixtureTables(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS account_health_probe_request_outbox (
			request_id TEXT PRIMARY KEY, account_id TEXT NOT NULL, reason TEXT NOT NULL,
			trace_id TEXT NOT NULL DEFAULT '', source_fence TEXT NOT NULL DEFAULT '',
			deadline_at TEXT NOT NULL, status TEXT NOT NULL, consumed_at TEXT,
			available_at TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS account_health_jobs_input_versions (
			account_id TEXT PRIMARY KEY, current_version INTEGER NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestW16DBusinessDBContractArms(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db := w16dOpenTestSQLite(t, root+"/business.sqlite3")
	if _, err := db.Exec(`CREATE TABLE accounts (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	business := &businessDB{db: db}
	// 契约列缺失 → count<7 fail closed。
	if err := ensureAccountsBalanceColumns(ctx, business); err == nil {
		t.Fatal("缺契约列必须报错")
	}
	// 关闭句柄 → 查询错误分支。
	closed := w16dOpenTestSQLite(t, root+"/closed.sqlite3")
	_ = closed.Close()
	if err := ensureAccountsBalanceColumns(ctx, &businessDB{db: closed}); err == nil {
		t.Fatal("关闭句柄必须报错")
	}
	// close 的 nil 防御分支。
	var nilBusiness *businessDB
	if err := nilBusiness.close(); err != nil {
		t.Fatalf("nil receiver close 必须 nil: %v", err)
	}
	if err := (&businessDB{}).close(); err != nil {
		t.Fatalf("空句柄 close 必须 nil: %v", err)
	}
}

func TestW16DProbeOutboxStoreArms(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db := w16dOpenTestSQLite(t, root+"/business.sqlite3")
	if _, err := db.Exec(`CREATE TABLE accounts (id TEXT PRIMARY KEY, config_revision INTEGER, dispatch_revision INTEGER, deleted_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	w16dCreateJ1FixtureTables(t, db)
	business := &businessDB{db: db}
	if err := EnsureHealthProbeOutboxSchema(ctx, business); err != nil {
		t.Fatalf("幂等建表: %v", err)
	}
	if err := EnsureHealthProbeOutboxSchema(ctx, nil); err == nil {
		t.Fatal("nil business 必须报错")
	}
	closed := w16dOpenTestSQLite(t, root+"/closed.sqlite3")
	_ = closed.Close()
	if err := EnsureHealthProbeOutboxSchema(ctx, &businessDB{db: closed}); err == nil {
		t.Fatal("关闭句柄建表必须报错")
	}

	store := healthProbeOutboxStore{business: business}
	insert := func(requestID, deadline string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO account_health_probe_request_outbox
			(request_id, account_id, reason, deadline_at, status, available_at, created_at, updated_at)
			VALUES (?, 'w16d-acc', 'w16d-test', ?, 'pending', '2020-01-01T00:00:00Z', '2020-01-01T00:00:00Z', '2020-01-01T00:00:00Z')`,
			requestID, deadline); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	insert("w16d-ok-1", now.Add(time.Minute).UTC().Format(time.RFC3339Nano))
	insert("w16d-bad-deadline", "not-a-time")
	// 整数型 deadline_at 触发 rows.Scan 类型错误分支。
	if _, err := db.Exec(`INSERT INTO account_health_probe_request_outbox
		(request_id, account_id, reason, deadline_at, status, available_at, created_at, updated_at)
		VALUES ('w16d-scan-bad', 'w16d-acc', 'w16d-test', 12345, 'pending', '2020-01-01T00:00:00Z', '2020-01-01T00:00:00Z', '2020-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimPendingProbeRequests(ctx, 10, now); err == nil {
		t.Fatal("坏 deadline 行必须使 claim 报错")
	}
	if _, err := db.Exec(`DELETE FROM account_health_probe_request_outbox WHERE request_id IN ('w16d-bad-deadline','w16d-scan-bad')`); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ClaimPendingProbeRequests(ctx, 10, now)
	if err != nil {
		t.Fatalf("正常 claim: %v", err)
	}
	if len(rows) != 1 || rows[0].RequestID != "w16d-ok-1" {
		t.Fatalf("claim 结果错误: %v", rows)
	}
	if _, err := (healthProbeOutboxStore{business: &businessDB{db: closed}}).ClaimPendingProbeRequests(ctx, 10, now); err == nil {
		t.Fatal("关闭句柄 claim 必须报错")
	}
	// Complete：pending 行删除返回 true；重复删除 false；关闭句柄报错。
	committed, err := store.CompleteProbeRequest(ctx, "w16d-ok-1", now)
	if err != nil || !committed {
		t.Fatalf("complete pending 行: %v %v", committed, err)
	}
	if committed, err = store.CompleteProbeRequest(ctx, "w16d-ok-1", now); err != nil || committed {
		t.Fatalf("重复 complete 必须 false,nil: %v %v", committed, err)
	}
	if _, err := (healthProbeOutboxStore{business: &businessDB{db: closed}}).CompleteProbeRequest(ctx, "x", now); err == nil {
		t.Fatal("关闭句柄 complete 必须报错")
	}

	// boundary 读取链。
	boundary := healthProbeBoundary{business: business}
	if _, _, _, ok, err := boundary.CurrentProbeInput(ctx, "w16d-missing"); err != nil || ok {
		t.Fatalf("缺失账户必须 !ok: %v %v", ok, err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, config_revision, dispatch_revision, deleted_at) VALUES ('w16d-acc-zero', 0, 1, NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok, err := boundary.CurrentProbeInput(ctx, "w16d-acc-zero"); err != nil || ok {
		t.Fatalf("revision<1 必须 !ok: %v %v", ok, err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, config_revision, dispatch_revision, deleted_at) VALUES ('w16d-acc-ok', 3, 2, NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok, err := boundary.CurrentProbeInput(ctx, "w16d-acc-ok"); err != nil || ok {
		t.Fatalf("缺 input_versions 行必须 !ok: %v %v", ok, err)
	}
	if _, err := db.Exec(`INSERT INTO account_health_jobs_input_versions (account_id, current_version) VALUES ('w16d-acc-ok', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok, err := boundary.CurrentProbeInput(ctx, "w16d-acc-ok"); err != nil || ok {
		t.Fatalf("version<1 必须 !ok: %v %v", ok, err)
	}
	if _, err := db.Exec(`UPDATE account_health_jobs_input_versions SET current_version = 5 WHERE account_id = 'w16d-acc-ok'`); err != nil {
		t.Fatal(err)
	}
	configRev, dispatchRev, version, ok, err := boundary.CurrentProbeInput(ctx, "w16d-acc-ok")
	if err != nil || !ok || configRev != 3 || dispatchRev != 2 || version != 5 {
		t.Fatalf("合法读取链: ok=%v %d/%d/%d %v", ok, configRev, dispatchRev, version, err)
	}
	if _, _, _, ok, err := (healthProbeBoundary{business: &businessDB{db: closed}}).CurrentProbeInput(ctx, "w16d-acc-ok"); err == nil || ok {
		t.Fatal("关闭句柄 boundary 必须报错")
	}

	// pruner：pruneOnce 有界删除 + 关闭句柄错误 + Run 定时节拍分支。
	pruner := &healthProbeOutboxPruner{business: business, retention: time.Hour, interval: time.Hour, logger: slog.Default()}
	if deleted, err := pruner.pruneOnce(ctx, now); err != nil || deleted != 0 {
		t.Fatalf("prune 无过期行: %d %v", deleted, err)
	}
	if _, err := db.Exec(`INSERT INTO account_health_probe_request_outbox
		(request_id, account_id, reason, deadline_at, status, available_at, created_at, updated_at)
		VALUES ('w16d-expired', 'w16d-acc', 'w16d-test', '', 'consumed', '2020-01-01T00:00:00Z', '2000-01-01T00:00:00Z', '2000-01-01T00:00:00Z'),
		       ('w16d-fresh', 'w16d-acc', 'w16d-test', '', 'pending', '2020-01-01T00:00:00Z', ?, ?)`,
		now.Add(-time.Minute).UTC().Format(time.RFC3339Nano), now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if deleted, err := pruner.pruneOnce(ctx, now); err != nil || deleted != 1 {
		t.Fatalf("prune 过期行: %d %v", deleted, err)
	}
	if _, err := (&healthProbeOutboxPruner{business: &businessDB{db: closed}}).pruneOnce(ctx, now); err == nil {
		t.Fatal("关闭句柄 prune 必须报错")
	}
	ticking := &healthProbeOutboxPruner{business: business, retention: time.Hour, interval: 20 * time.Millisecond, logger: slog.Default()}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- ticking.Run(runCtx) }()
	time.Sleep(80 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(fmt.Sprint(err), context.Canceled.Error()) {
			t.Fatalf("Run 必须以 ctx.Err 退出: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未随 ctx 取消退出")
	}
}

// w16dJ1Env 是让 accounthealth.LoadConfig 通过的最小 J1 SQLite env。
func w16dJ1Env(t *testing.T, root string) map[string]string {
	t.Helper()
	inputDirectory := root + "/account-health-inputs"
	if err := os.MkdirAll(inputDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":        "go",
		"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":       "w16d-j1",
		"JUHE_AI_ACCOUNT_HEALTH_STORE":             "sqlite",
		"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH":     root + "/account-health.sqlite3",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   inputDirectory,
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA0",
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "0123456789abcdef0123456789abcdef",
	}
}

func TestW16DWireHealthProbeOutboxFaceArms(t *testing.T) {
	root := t.TempDir()
	businessPath := root + "/business.sqlite3"
	fixture := w16dOpenTestSQLite(t, businessPath)
	seedProbeCoreTables(t, businessPath)
	w16dCreateJ1FixtureTables(t, fixture)

	envLookup := func(extra map[string]string) func(string) string {
		env := w16dJ1Env(t, root)
		for key, value := range extra {
			env[key] = value
		}
		return func(name string) string { return env[name] }
	}
	newAssembly := func(driver, businessPath string) *workerAssembly {
		config := workerConfig{Driver: driver, InstanceID: "w16d-face"}
		if driver == "postgres" {
			config.PostgresURL = "pgx://w16d-invalid-url"
		} else {
			config.BusinessSQLitePath = businessPath
		}
		assembly := newWorkerAssembly(config, slog.Default())
		t.Cleanup(assembly.closeStores)
		return assembly
	}
	// openBusinessDB 失败臂（postgres 坏 URL；恒开语义下 face 恒开业务库）。
	if _, err := newAssembly("postgres", "").wireHealthProbeOutboxFace(func(string) string { return "" }); err == nil {
		t.Fatal("postgres 坏 URL 必须使 face 装配失败")
	}
	// 恒开终态：无 J1 门控，drain 与 pruner 恒装配；保留天数非法走告警回调
	// （228-230）。getenv 全空（仅保留天数非法）即可触发。
	face, err := newAssembly("sqlite", businessPath).wireHealthProbeOutboxFace(func(name string) string {
		if name == probeOutboxRetentionEnvVar {
			return "bogus"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("恒开 face 装配: %v", err)
	}
	if face.drain == nil || face.pruner == nil {
		t.Fatalf("恒开语义下 drain 与 pruner 必须就绪: %+v", face)
	}
	// J1 开启 + assembly config 携带非法 Redis URL（scheme 非 redis）：
	// fence settler 解析失败臂（238-240 + 355-357；settler 读 a.config 而非 env）。
	{
		config := workerConfig{Driver: "sqlite", InstanceID: "w16d-face", BusinessSQLitePath: businessPath}
		config.RedisStateURL = "http://127.0.0.1:6379"
		config.RedisNamespace = "juhe-ai:w16d"
		badRedisAssembly := newWorkerAssembly(config, slog.Default())
		t.Cleanup(badRedisAssembly.closeStores)
		if _, err := badRedisAssembly.wireHealthProbeOutboxFace(envLookup(nil)); err == nil {
			t.Fatal("非法 Redis URL 必须使 J1 face 装配失败")
		}
	}
	// J1 开启 + miniredis：完整消费面 + settler closer（241-243）。
	redisServer := miniredis.RunT(t)
	assembly := newAssembly("sqlite", businessPath)
	face, err = assembly.wireHealthProbeOutboxFace(envLookup(map[string]string{
		"JUHE_AI_REDIS_STATE_URL": "redis://" + redisServer.Addr(),
		"JUHE_AI_REDIS_NAMESPACE": "juhe-ai:w16d",
	}))
	if err != nil {
		t.Fatalf("J1 开启 face 装配: %v", err)
	}
	if face.drain == nil || face.pruner == nil {
		t.Fatalf("J1 开启时完整消费面必须就绪: drain=%v", face.drain)
	}
	assembly.closeStores()
}

func TestW16DWireHealthOutcomeProjectorArms(t *testing.T) {
	root := t.TempDir()
	businessPath := root + "/business.sqlite3"
	fixture := w16dOpenTestSQLite(t, businessPath)
	seedProbeCoreTables(t, businessPath)
	w16dCreateJ1FixtureTables(t, fixture)

	// store 为 nil → 合法缺席（防御性分支；恒开终态下 store 恒非 nil）。
	emptyAssembly := newWorkerAssembly(workerConfig{Driver: "sqlite"}, slog.Default())
	if projector, err := emptyAssembly.wireHealthOutcomeProjector(nil, nil); err != nil || projector != nil {
		t.Fatalf("store nil 必须合法缺席: %v %v", projector, err)
	}
	// env 显式关闭 → 合法缺席（47-50）。
	if projector, err := emptyAssembly.wireHealthOutcomeProjector(func(name string) string {
		if name == healthProjectionDisabledEnvVar {
			return "true"
		}
		return ""
	}, &accounthealth.Store{}); err != nil || projector != nil {
		t.Fatalf("env 关闭必须合法缺席: %v %v", projector, err)
	}
	// openBusinessDB 失败（postgres 坏 URL；68-70）。
	pgAssembly := newWorkerAssembly(workerConfig{Driver: "postgres", PostgresURL: "pgx://w16d-invalid"}, slog.Default())
	if _, err := pgAssembly.wireHealthOutcomeProjector(func(name string) string {
		return w16dJ1Env(t, root)[name]
	}, &accounthealth.Store{}); err == nil {
		t.Fatal("postgres 坏 URL 必须使投影装配失败")
	}
	pgAssembly.closeStores()
	// poll/batch env 非法 → fail loud（76-80 / 82-85）。
	envWithBounds := func(poll, batch string) func(string) string {
		base := w16dJ1Env(t, root)
		return func(name string) string {
			switch name {
			case healthProjectionPollEnvVar:
				return poll
			case healthProjectionBatchEnvVar:
				return batch
			}
			return base[name]
		}
	}
	sqliteAssembly := newWorkerAssembly(workerConfig{Driver: "sqlite", BusinessSQLitePath: businessPath}, slog.Default())
	t.Cleanup(sqliteAssembly.closeStores)
	if _, err := sqliteAssembly.wireHealthOutcomeProjector(envWithBounds("abc", ""), &accounthealth.Store{}); err == nil {
		t.Fatal("非法 poll 必须使投影装配失败")
	}
	if _, err := sqliteAssembly.wireHealthOutcomeProjector(envWithBounds("", "abc"), &accounthealth.Store{}); err == nil {
		t.Fatal("非法 batch 必须使投影装配失败")
	}
	// 空 credential secret：投影器 fail closed（95-98；LoadConfig 接受空 secret 时命中）。
	noSecretEnv := w16dJ1Env(t, root)
	noSecretEnv["JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET"] = ""
	if _, err := sqliteAssembly.wireHealthOutcomeProjector(func(name string) string { return noSecretEnv[name] }, &accounthealth.Store{}); err == nil {
		t.Fatal("空凭据 secret 必须使投影装配失败或被 LoadConfig 拒绝")
	}
}

func TestW16DWireCircuitFamilyArms(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	businessPath := root + "/business.sqlite3"
	seedProbeCoreTables(t, businessPath)
	w16dOpenTestSQLite(t, businessPath) // Ping 触发物理建库

	disabled := map[string]string{}
	registerDisabled := func(name, reason string) { disabled[name] = reason }
	assembly := newWorkerAssembly(workerConfig{
		Driver: "sqlite", InstanceID: "w16d-circuit",
		BusinessSQLitePath: businessPath, Secret: wgBalanceSecret,
	}, slog.Default())
	t.Cleanup(assembly.closeStores)
	business, err := openBusinessDB(assembly, "w16d-circuit-business")
	if err != nil {
		t.Fatal(err)
	}
	// 非法 Redis URL（scheme 非 redis → 解析失败）→ 错误传播（51-53）。
	assembly.config.RedisStateURL = "http://127.0.0.1:6379"
	assembly.config.RedisNamespace = "juhe-ai:w16d"
	if err := assembly.wireCircuitFamily(ctx, business, registerDisabled); err == nil {
		t.Fatal("非法 Redis URL 必须使电路族装配失败")
	}
	// miniredis：完整装配 + 单轮任务执行（任务闭包主体）。
	redisServer := miniredis.RunT(t)
	assembly.config.RedisStateURL = "redis://" + redisServer.Addr()
	if err := assembly.wireCircuitFamily(ctx, business, registerDisabled); err != nil {
		t.Fatalf("miniredis 电路族装配: %v", err)
	}
	for _, name := range []string{"account-circuit-control-plane-maintenance", "account-circuit-recovery"} {
		taskCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if _, err := assembly.runWiredJobOnce(taskCtx, name); err != nil {
			t.Logf("任务 %s 在 fixture 上执行失败（覆盖错误臂，登记不判失败）: %v", name, err)
		}
		cancel()
	}
	// EnsureCursorSchema 错误臂：失败登记 disabled 并返回 nil（不传播）。
	closed := w16dOpenTestSQLite(t, root+"/closed.sqlite3")
	_ = closed.Close()
	assembly2 := newWorkerAssembly(workerConfig{Driver: "sqlite"}, slog.Default())
	t.Cleanup(assembly2.closeStores)
	assembly2.config.RedisStateURL = "redis://" + redisServer.Addr()
	assembly2.config.RedisNamespace = "juhe-ai:w16d"
	if err := assembly2.wireCircuitFamily(ctx, &businessDB{db: closed}, registerDisabled); err != nil {
		t.Fatalf("schema 初始化失败臂必须登记 disabled 并返回 nil: %v", err)
	}
	if _, ok := disabled["account-circuit-control-plane-maintenance"]; !ok {
		t.Fatalf("控制面 schema 失败必须登记 disabled: %v", disabled)
	}
	// Resolve 臂：关闭句柄 → LoadAccountForTest 错误（142-144）。
	closedStore, err := proberepo.NewStore(proberepo.Config{DB: closed, Secret: wgBalanceSecret})
	if err != nil {
		t.Fatalf("closed probe store: %v", err)
	}
	resolver := circuitRecoveryTargetResolver{store: closedStore}
	if _, _, err := resolver.Resolve(ctx, opsjobs.CircuitState{
		Scope: opsjobs.CircuitScope{AccountRuntimeKey: "w16d-acc"},
	}); err == nil {
		t.Fatal("关闭句柄 Resolve 必须报错")
	}
}
