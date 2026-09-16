package operationlog

// w9b 剩余可达臂补充：OpenStore 打开失败、inode/别名守卫、SQLite 写路径错误、
// 配置校验与 normalize 矩阵的分支。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestW9BOpenStoreMissingBusinessSettings(t *testing.T) {
	root := t.TempDir()
	operation := filepath.Join(root, "operation.sqlite3")
	// BusinessSettingsPath 不存在 → openSQLiteReadOnly 失败。
	if _, err := OpenStore(Config{Mode: ModeSQLite, DatabasePath: operation, BusinessSettingsPath: filepath.Join(root, "missing.sqlite3")}); err == nil || !strings.Contains(err.Error(), "read F4 business settings SQLite") {
		t.Fatalf("缺失业务镜像=%v", err)
	}
	// BusinessDatabasePath 指向不存在文件 → 兜底句柄打开失败并关闭主句柄。
	business := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, business, "365")
	if _, err := OpenStore(Config{Mode: ModeSQLite, DatabasePath: operation, BusinessSettingsPath: business, BusinessDatabasePath: filepath.Join(root, "missing-fallback.sqlite3")}); err == nil || !strings.Contains(err.Error(), "read F4 business settings SQLite") {
		t.Fatalf("缺失兜底库=%v", err)
	}
}

func TestW9BOpenStoreRejectsSharedInode(t *testing.T) {
	root := t.TempDir()
	business := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, business, "365")
	alias := filepath.Join(root, "hardlink.sqlite3")
	if err := os.Link(business, alias); err != nil {
		t.Skipf("当前文件系统不支持硬链接：%v", err)
	}
	// operation 数据库文件与隔离目标（业务镜像原件）是同一 inode。
	if _, err := OpenStore(Config{Mode: ModeSQLite, DatabasePath: alias, BusinessSettingsPath: business, SQLiteIsolationPaths: []string{business}}); err == nil || !strings.Contains(err.Error(), "must not share an inode") {
		t.Fatalf("inode 冲突=%v", err)
	}
}

func TestW9BSameSQLiteFileHelpers(t *testing.T) {
	// 空路径直接 false。
	if sameSQLiteFile("", "x") || sameSQLiteFile("x", "") {
		t.Fatal("空路径必须返回 false")
	}
	// 不存在路径的 EvalSymlinks 失败分支（退回 Clean(Abs)）。
	root := t.TempDir()
	if !sameSQLiteFile(filepath.Join(root, "a.sqlite3"), filepath.Join(root, "a.sqlite3")) {
		t.Fatal("相同路径必须相等")
	}
	if sameSQLiteFile(filepath.Join(root, "a.sqlite3"), filepath.Join(root, "b.sqlite3")) {
		t.Fatal("不同路径必须不等")
	}
}

func TestW9BRetentionDaysSQLiteErrorArms(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		value   any
		noRows  bool
		rowsErr bool
		wantErr bool
		want    int
	}{
		{name: "query error", rowsErr: true, wantErr: true},
		{name: "bad json", value: "{invalid", wantErr: true},
		{name: "string json", value: `"12"`, wantErr: true},
		{name: "fraction", value: "12.5", wantErr: true},
		{name: "zero", value: "0", wantErr: true},
		{name: "too large", value: "3651", wantErr: true},
		{name: "valid", value: "90", want: 90},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := &w9bScript{}
			if tc.noRows {
				script.steps = append(script.steps, w9bStep{matcher: []string{"system_settings"}, noRows: true})
			} else if tc.rowsErr {
				script.steps = append(script.steps, w9bStep{matcher: []string{"system_settings"}, rowsErr: w9bErrBoom})
			} else {
				script.steps = append(script.steps, w9bStep{matcher: []string{"system_settings"}, rows: [][]driver.Value{{tc.value}}})
			}
			store := &sqlStore{db: w9bOpen(t, &w9bScript{}), businessDB: w9bOpen(t, script), mode: ModeSQLite}
			days, err := store.RetentionDays(ctx, 365)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("%s 必须报错，days=%d", tc.name, days)
				}
				return
			}
			if err != nil || days != tc.want {
				t.Fatalf("%s：days=%d err=%v", tc.name, days, err)
			}
		})
	}
}

func TestW9BEnsureSchemaSQLiteFailure(t *testing.T) {
	script := &w9bScript{steps: []w9bStep{{matcher: []string{"CREATE TABLE IF NOT EXISTS operation_log_owner_leases"}, execErr: w9bErrBoom}}}
	store := &sqlStore{db: w9bOpen(t, script), mode: ModeSQLite}
	if err := store.EnsureSchema(context.Background()); err == nil || !strings.Contains(err.Error(), "initialize F4 sqlite schema") {
		t.Fatalf("SQLite schema 失败=%v", err)
	}
}

func TestW9BPersistSQLiteChildrenErrorArms(t *testing.T) {
	ctx := context.Background()
	base := Input{ID: "w9b-sqlite", ActorSystemAccountID: "actor", ActorRole: "admin", Mode: "admin", Module: "m", Action: "a", OperationKey: "m.a", ResourceType: "r", Summary: "hello w9b", DetailLevel: "full", VisibilityScope: "targeted", CreatedAt: "2026-09-01T00:00:00.000000000Z", Targets: []Target{{TargetType: "account", Relation: "primary"}}, Viewers: []Viewer{{SystemAccountID: "v", VisibilityReason: "resource_owner", DetailLevel: "full"}}}
	lease := OwnerLease{OwnerID: "w9b", FenceToken: 1}
	childFail := func(matcher string) *w9bScript {
		return &w9bScript{steps: []w9bStep{
			{matcher: []string{"SELECT 1 FROM operation_log_owner_leases"}, rows: [][]driver.Value{{int64(1)}}, repeat: 2},
			{matcher: []string{"INSERT INTO operation_logs ("}, affected: 1},
			{matcher: []string{matcher}, execErr: w9bErrBoom},
		}}
	}
	for _, tc := range []struct {
		name   string
		script *w9bScript
	}{
		{"targets", childFail("INSERT INTO operation_log_targets (")},
		{"viewers", childFail("INSERT INTO operation_log_viewers (")},
		{"terms", childFail("INSERT INTO operation_log_summary_search_terms (")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &sqlStore{db: w9bOpen(t, tc.script), mode: ModeSQLite}
			if _, err := store.Persist(ctx, lease, base); err == nil {
				t.Fatalf("%s 写入失败必须透传", tc.name)
			}
		})
	}
}

func TestW9BMigrateLegacySQLiteRowLevelGuards(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	target := filepath.Join(root, "target.sqlite3")
	business := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, business, "365")
	cfg := Config{Mode: ModeSQLite, DatabasePath: target, BusinessSettingsPath: business, InstanceID: "w9b-rowguard"}

	// 源行 changes_json 非法 → 迁移拒绝。
	badJSON := filepath.Join(root, "bad-json.sqlite3")
	createLegacyNodeOperationLogSQLite(t, badJSON)
	badDB, err := sql.Open("sqlite", badJSON)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badDB.Exec(`UPDATE operation_logs SET changes_json='{invalid'`); err != nil {
		t.Fatal(err)
	}
	_ = badDB.Close()
	if _, err := MigrateLegacySQLite(ctx, cfg, LegacyMigrationOptions{SourceDatabasePath: badJSON, NodeStopped: true, GoStopped: true, BackupConfirmed: true}); err == nil || !strings.Contains(err.Error(), "changes_json 无效") {
		t.Fatalf("坏 changes 源=%v", err)
	}

	// target 行 relation 非法 → 迁移拒绝。
	badRelation := filepath.Join(root, "bad-relation.sqlite3")
	createLegacyNodeOperationLogSQLite(t, badRelation)
	relDB, err := sql.Open("sqlite", badRelation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relDB.Exec(`UPDATE operation_log_targets SET relation='weird'`); err != nil {
		t.Fatal(err)
	}
	_ = relDB.Close()
	if _, err := MigrateLegacySQLite(ctx, cfg, LegacyMigrationOptions{SourceDatabasePath: badRelation, NodeStopped: true, GoStopped: true, BackupConfirmed: true}); err == nil || !strings.Contains(err.Error(), "target 不兼容") {
		t.Fatalf("坏 relation=%v", err)
	}

	// viewer 行 visibility_reason 非法 → 迁移拒绝。
	badViewer := filepath.Join(root, "bad-viewer.sqlite3")
	createLegacyNodeOperationLogSQLite(t, badViewer)
	viewerDB, err := sql.Open("sqlite", badViewer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := viewerDB.Exec(`UPDATE operation_log_viewers SET visibility_reason='weird'`); err != nil {
		t.Fatal(err)
	}
	_ = viewerDB.Close()
	if _, err := MigrateLegacySQLite(ctx, cfg, LegacyMigrationOptions{SourceDatabasePath: badViewer, NodeStopped: true, GoStopped: true, BackupConfirmed: true}); err == nil || !strings.Contains(err.Error(), "viewer 不兼容") {
		t.Fatalf("坏 viewer=%v", err)
	}
}

func TestW9BNormalizedViewersArms(t *testing.T) {
	// 非法 reason。
	if _, err := normalizedViewers([]Viewer{{SystemAccountID: "v", VisibilityReason: "weird"}}, "full"); err == nil || !strings.Contains(err.Error(), "viewer is invalid") {
		t.Fatalf("非法 reason=%v", err)
	}
	// 非法 detail level。
	if _, err := normalizedViewers([]Viewer{{SystemAccountID: "v", VisibilityReason: "actor_self", DetailLevel: "weird"}}, "full"); err == nil || !strings.Contains(err.Error(), "detail level is invalid") {
		t.Fatalf("非法 detail=%v", err)
	}
	// 空 ID 跳过、默认 level、去重。
	out, err := normalizedViewers([]Viewer{
		{SystemAccountID: " ", VisibilityReason: "actor_self", DetailLevel: "full"},
		{SystemAccountID: "v", VisibilityReason: "actor_self"},
		{SystemAccountID: "v", VisibilityReason: "actor_self", DetailLevel: "summary"},
	}, "full")
	if err != nil || len(out) != 2 || out[0].DetailLevel != "full" {
		t.Fatalf("跳过空 ID/默认 level/去重=%+v err=%v", out, err)
	}
}

func TestW9BLoadConfigPostgresPoolLimitsArm(t *testing.T) {
	env := func(key string) string {
		values := map[string]string{
			"JUHE_AI_OPERATION_LOG_STORE":                   "postgres",
			"JUHE_AI_OPERATION_LOG_POSTGRES_URL":            "postgres://example/db",
			"JUHE_AI_OPERATION_LOG_INSTANCE_ID":             "w9b",
			"JUHE_AI_OPERATION_LOG_POSTGRES_MAX_OPEN_CONNS": "2",
			"JUHE_AI_OPERATION_LOG_POSTGRES_MAX_IDLE_CONNS": "5",
		}
		return values[key]
	}
	if _, err := LoadConfig(env); err == nil || !strings.Contains(err.Error(), "连接池配置无效") {
		t.Fatalf("池上限校验=%v", err)
	}
}

func TestW9BProducerNilSafetyAndUnmarshalFallback(t *testing.T) {
	// nil 接收者与 nil store 都是 no-op。
	var nilProducer *Producer
	nilProducer.Record(Input{})
	(&Producer{}).Record(Input{})
	// 无法 JSON 序列化的值回退空串（normalizeSafeValue default 分支）。
	if got := normalizeSafeValue(make(chan int)); got != "" {
		t.Fatalf("不可序列化值=%v", got)
	}
}

type w9bAcquireOKStore struct{ w9bFakeStore }

func (s *w9bAcquireOKStore) AcquireOwnerLease(_ context.Context, owner string, _ time.Duration) (OwnerLease, bool, error) {
	return OwnerLease{OwnerID: owner, FenceToken: 1}, true, nil
}

func TestW9BStartLeaseKeeperDefaultTTLAndTransientRenew(t *testing.T) {
	// ttl<=0 走默认值。
	store := &w9bAcquireOKStore{}
	keeper, ok, err := StartLeaseKeeper(context.Background(), store, "w9b-ttl", 0, nil)
	if err != nil || !ok {
		t.Fatalf("默认 TTL 启动=%v err=%v", ok, err)
	}
	if keeper.TTL() != defaultOwnerLease {
		t.Fatalf("TTL=%v", keeper.TTL())
	}
	keeper.Close()
	// Acquire 失败 → 不启动。
	failing := &w9bAcquireFailingStore{}
	if keeper, ok, err := StartLeaseKeeper(context.Background(), failing, "w9b-fail", time.Minute, nil); err == nil || ok || keeper != nil {
		t.Fatalf("Acquire 失败应拒绝：ok=%v err=%v", ok, err)
	}
}

type w9bAcquireFailingStore struct{ w9bFakeStore }

func (s *w9bAcquireFailingStore) AcquireOwnerLease(context.Context, string, time.Duration) (OwnerLease, bool, error) {
	return OwnerLease{}, false, w9bErrBoom
}

func TestW9BRunOwnerDefaultLogger(t *testing.T) {
	// logger 为 nil 时使用 slog.Default()，仍能优雅退出。
	keeper := &LeaseKeeper{lostCh: make(chan struct{})}
	store := &w9bFakeStore{retentionDays: 1}
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunOwner(runCtx, store, keeper, Config{RetentionInterval: time.Millisecond}, nil) }()
	time.Sleep(40 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("默认 logger=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("未退出")
	}
}

func TestW9BValidateUsageShardIsolationArms(t *testing.T) {
	// shard root 不存在 → RequirePhysicalRoot 失败。
	if err := validateUsageShardIsolation("unused.sqlite3", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("缺失 shard root 必须报错")
	}
	// shard root 是文件 → ListUsageShardFiles 失败。
	root := t.TempDir()
	file := filepath.Join(root, "file.sqlite3")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateUsageShardIsolation(filepath.Join(root, "op.sqlite3"), file); err == nil {
		t.Fatal("文件型 shard root 必须报错")
	}
	// shard 外的数据库与 shard 内文件同 inode（硬链接）→ SameFile 拒绝。
	shard := filepath.Join(root, "shard")
	if err := os.Mkdir(shard, 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(shard, "2026", "09", "01")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	inside = filepath.Join(inside, "usage-20260901-s1.sqlite3")
	if err := os.WriteFile(inside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.sqlite3")
	if err := os.Link(inside, outside); err != nil {
		t.Skipf("当前文件系统不支持硬链接：%v", err)
	}
	if err := validateUsageShardIsolation(outside, shard); err == nil || !strings.Contains(err.Error(), "must not share a SQLite file") {
		t.Fatalf("同 inode=%v", err)
	}
}
