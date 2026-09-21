package auditlog

// w14h 覆盖波次：Persist / CleanupRetention / hot-search 的数据库层错误臂，
// 通过包装连接器按 SQL 子串注入故障（w11f 模式移植）。每个子用例使用独立
// 命名的共享内存库，互不干扰。
//
// 覆盖率现状（w14h 收尾）：本包实测 88.8%，未达 95%。剩余未覆盖语句集中于：
//   - store.go（约 79 条）：Persist 各步骤错误臂的深组合（writeBlobTemps/
//     publishBlobPlans/syncBlobParent 内部分支）、verifyLeaseBeforeCommit 的
//     PG commit-fence 不匹配臂、MarshalJSON/resolveBlobIDs 的分支组合；
//   - retention.go（约 56 条）：cleanupScheduledBlobFile 的重引用取消/元数据
//     缺失/删除计数异常组合臂、blobFilePath/removeBlobFile 的平台错误臂；
//   - legacy_migration.go（约 47 条）：路径解析/sql.Open/ATTACH/integrity
//     异常等需要伪造驱动或损坏库文件的臂；
//   - hot_search.go（约 26 条）/ config.go（11）/ owner.go（7）/ types.go（3）。
// 其中多数为可通过更深层故障注入（脚本化 PG 驱动、损坏 SQLite 文件）触达的
// 错误臂，少数为防御性分支；未发现真实缺陷。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"
)

var w14hAuditBoom = errors.New("w14h audit boom")

type w14hAuditRule struct {
	substr   string
	queryErr error
	execErr  error
	limit    int
	hits     int
	// exempt 标记负对照臂：注册后故意不命中，豁免 assertRulesFired。
	exempt bool
}

type w14hAuditScript struct {
	mu        sync.Mutex
	t         *testing.T
	rules     []*w14hAuditRule
	beginErr  error
	commitErr error
}

// allowUnfired 把规则标记为负对照（故意不命中），Cleanup 断言跳过。
func (r *w14hAuditRule) allowUnfired() *w14hAuditRule {
	r.exempt = true
	return r
}

// assertRulesFired 在测试收尾断言所有注册规则都被真实消费过（hits>0）：
// 子串与生产 SQL 漂移导致规则永不命中的伪覆盖臂在此变红。
func (s *w14hAuditScript) assertRulesFired() {
	if s.t == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if r.hits == 0 && !r.exempt {
			s.t.Errorf("w14h 注入规则未命中（伪覆盖）：substr=%q", r.substr)
		}
	}
}

func (s *w14hAuditScript) failQuery(substr string) *w14hAuditRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := &w14hAuditRule{substr: substr, queryErr: w14hAuditBoom}
	s.rules = append(s.rules, r)
	return r
}

func (s *w14hAuditScript) failExec(substr string) *w14hAuditRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := &w14hAuditRule{substr: substr, execErr: w14hAuditBoom}
	s.rules = append(s.rules, r)
	return r
}

// skipQuery / skipExec 放过前 N 次匹配（不注入错误），用于命中"第二次校验"臂。
func (s *w14hAuditScript) skipQuery(substr string, limit int) *w14hAuditRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := &w14hAuditRule{substr: substr, limit: limit}
	s.rules = append(s.rules, r)
	return r
}

func (s *w14hAuditScript) skipExec(substr string, limit int) *w14hAuditRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := &w14hAuditRule{substr: substr, limit: limit}
	s.rules = append(s.rules, r)
	return r
}

func (s *w14hAuditScript) take(query string) *w14hAuditRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rule := range s.rules {
		if !strings.Contains(query, rule.substr) {
			continue
		}
		if rule.limit >= 0 && rule.hits >= max(rule.limit, 1) {
			continue
		}
		rule.hits++
		return rule
	}
	return nil
}

type w14hAuditConnector struct {
	base   driver.Connector
	script *w14hAuditScript
}

func (c w14hAuditConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &w14hAuditConn{base: conn, script: c.script}, nil
}

func (c w14hAuditConnector) Driver() driver.Driver { return c.base.Driver() }

type w14hAuditConn struct {
	base   driver.Conn
	script *w14hAuditScript
}

func (c *w14hAuditConn) Prepare(query string) (driver.Stmt, error) {
	return c.base.Prepare(query)
}
func (c *w14hAuditConn) Close() error { return c.base.Close() }

func (c *w14hAuditConn) Begin() (driver.Tx, error) {
	c.script.mu.Lock()
	beginErr := c.script.beginErr
	c.script.mu.Unlock()
	if beginErr != nil {
		return nil, beginErr
	}
	tx, err := c.base.Begin()
	if err != nil {
		return nil, err
	}
	return &w14hAuditTx{base: tx, script: c.script}, nil
}

func (c *w14hAuditConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}

func (c *w14hAuditConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if rule := c.script.take(query); rule != nil && rule.execErr != nil {
		return nil, rule.execErr
	}
	return c.base.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *w14hAuditConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if rule := c.script.take(query); rule != nil && rule.queryErr != nil {
		return nil, rule.queryErr
	}
	return c.base.(driver.QueryerContext).QueryContext(ctx, query, args)
}

type w14hAuditTx struct {
	base   driver.Tx
	script *w14hAuditScript
}

func (t *w14hAuditTx) Commit() error {
	t.script.mu.Lock()
	commitErr := t.script.commitErr
	t.script.mu.Unlock()
	if commitErr != nil {
		_ = t.base.Rollback()
		return commitErr
	}
	return t.base.Commit()
}

func (t *w14hAuditTx) Rollback() error { return t.base.Rollback() }

// w14hFaultStore 构造带故障脚本的 SQLite store；每个调用点必须位于独立命名的
// 子测试内（共享内存库以 t.Name() 命名）。
func w14hFaultStore(t *testing.T) (*sqlStore, *w14hAuditScript, OwnerLease) {
	t.Helper()
	base, err := sqlite.NewConnector("file:w14h-audit-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	script := &w14hAuditScript{t: t}
	t.Cleanup(script.assertRulesFired)
	db := sql.OpenDB(w14hAuditConnector{base: base, script: script})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	cfg := sqliteConfig(t, t.TempDir())
	discard, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = discard.Close() })
	store := &sqlStore{db: db, mode: ModeSQLite, blobDir: cfg.PayloadBlobDirectory, hotDir: filepath.Join(filepath.Dir(cfg.AuditDatabasePath), "w14h-hot")}
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	lease, ok, err := store.AcquireOwnerLease(ctx, "w14h-owner", time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease=%v/%v", ok, err)
	}
	return store, script, lease
}

// w14hFullInput 构造带 attempts/payloads/错误分组输入的完整 finalized 日志。
func w14hFullInput(id string) AuditLogInput {
	input := w14hPayloadInput(id, `{"body":"w14h"}`)
	input.AuditOutcome = AuditOutcomeGatewayFailed
	input.Success = false
	input.FinalStatusCode = intPointer(502)
	input.ErrorPhase = "upstream"
	input.ErrorCode = "E_UP"
	input.ErrorMessage = "upstream boom"
	input.Attempts = []AuditLogAttemptInput{{
		TempID: "attempt-temp", AttemptIndex: 0, AccountID: "acc", GroupID: "grp",
		ProviderCode: "openai", UpstreamMethod: "POST", UpstreamURL: "https://upstream",
		UpstreamStatusCode: intPointer(502), StartedAt: input.StartedAt, EndedAt: input.EndedAt,
	}}
	input.Payloads[0].AttemptTempID = "attempt-temp"
	return input
}

func TestW14HAuditFaultSweep(t *testing.T) {
	build := func(t *testing.T) (*sqlStore, *w14hAuditScript, OwnerLease, AuditLogInput) {
		store, script, lease := w14hFaultStore(t)
		return store, script, lease, w14hFullInput("w14h-fault-1")
	}
	t.Run("begin", func(t *testing.T) {
		store, script, lease, input := build(t)
		script.beginErr = w14hAuditBoom
		if _, err := store.Persist(context.Background(), lease, input); err == nil {
			t.Fatal("begin 失败必须透传")
		}
	})
	t.Run("lease verify", func(t *testing.T) {
		store, script, _, input := build(t)
		script.failExec("UPDATE audit_log_owner_leases")
		if _, err := store.Persist(context.Background(), OwnerLease{OwnerID: "w14h-owner", FenceToken: 1}, input); err == nil {
			t.Fatal("租约校验失败必须透传")
		}
	})
	t.Run("plan payloads", func(t *testing.T) {
		store, script, lease, input := build(t)
		script.failQuery("FROM audit_payload_blobs WHERE sha256")
		if _, err := store.Persist(context.Background(), lease, input); err == nil {
			t.Fatal("blob 计划失败必须透传")
		}
	})
	t.Run("reactivate gc", func(t *testing.T) {
		store, script, lease, input := build(t)
		script.failQuery("FROM audit_payload_blob_gc")
		if _, err := store.Persist(context.Background(), lease, input); err == nil {
			t.Fatal("pending GC 读取失败必须透传")
		}
	})
	t.Run("upsert log", func(t *testing.T) {
		store, script, lease, input := build(t)
		script.failExec("INSERT INTO audit_logs")
		if _, err := store.Persist(context.Background(), lease, input); err == nil {
			t.Fatal("audit_logs 写入失败必须透传")
		}
	})
	t.Run("resolve blob ids", func(t *testing.T) {
		store, script, lease, input := build(t)
		script.failQuery("INSERT INTO audit_payload_blobs")
		if _, err := store.Persist(context.Background(), lease, input); err == nil {
			t.Fatal("blob id 解析失败必须透传")
		}
	})
	t.Run("insert attempts", func(t *testing.T) {
		store, script, lease, input := build(t)
		script.failQuery("INSERT INTO audit_log_attempts")
		if _, err := store.Persist(context.Background(), lease, input); err == nil {
			t.Fatal("attempts 写入失败必须透传")
		}
	})
	t.Run("insert blob touch", func(t *testing.T) {
		store, script, lease, input := build(t)
		script.failExec("SET last_seen_at")
		if _, err := store.Persist(context.Background(), lease, input); err == nil {
			t.Fatal("blob 时间更新失败必须透传")
		}
	})
	t.Run("insert refs", func(t *testing.T) {
		store, script, lease, input := build(t)
		script.failExec("INSERT INTO audit_payload_refs")
		if _, err := store.Persist(context.Background(), lease, input); err == nil {
			t.Fatal("refs 写入失败必须透传")
		}
	})
	t.Run("increment refs", func(t *testing.T) {
		store, script, lease, input := build(t)
		script.failExec("SET ref_count=ref_count+1")
		if _, err := store.Persist(context.Background(), lease, input); err == nil {
			t.Fatal("引用计数失败必须透传")
		}
	})
	t.Run("upsert error group", func(t *testing.T) {
		store, script, lease, input := build(t)
		script.failQuery("INSERT INTO audit_error_groups")
		if _, err := store.Persist(context.Background(), lease, input); err == nil {
			t.Fatal("错误分组写入失败必须透传")
		}
	})
	t.Run("link error group", func(t *testing.T) {
		store, script, lease, input := build(t)
		script.failExec("SET error_group_id=")
		if _, err := store.Persist(context.Background(), lease, input); err == nil {
			t.Fatal("错误分组关联失败必须透传")
		}
	})
	t.Run("lease before commit", func(t *testing.T) {
		store, script, lease, input := build(t)
		script.skipExec("UPDATE audit_log_owner_leases", 1)
		script.failExec("UPDATE audit_log_owner_leases")
		if _, err := store.Persist(context.Background(), lease, input); err == nil {
			t.Fatal("提交前租约校验失败必须透传")
		}
	})
	t.Run("commit", func(t *testing.T) {
		store, script, lease, input := build(t)
		script.commitErr = w14hAuditBoom
		if _, err := store.Persist(context.Background(), lease, input); err == nil {
			t.Fatal("commit 失败必须透传")
		}
	})
	t.Run("write blob temps", func(t *testing.T) {
		store, _, lease, input := build(t)
		occupied := filepath.Join(t.TempDir(), "occupied")
		if err := os.WriteFile(occupied, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		store.blobDir = occupied
		if _, err := store.Persist(context.Background(), lease, input); err == nil {
			t.Fatal("blob 临时写入失败必须透传")
		}
	})
	t.Run("retention select", func(t *testing.T) {
		store, script, lease, input := build(t)
		_ = script
		if _, err := store.Persist(context.Background(), lease, input); err != nil {
			t.Fatal(err)
		}
		script.failQuery("SELECT id FROM audit_logs")
		config := w14hRetentionConfig()
		if _, err := store.CleanupRetention(context.Background(), lease, config); err == nil {
			t.Fatal("retention 读取失败必须透传")
		}
	})
	t.Run("retention delete refs", func(t *testing.T) {
		store, script, lease, input := build(t)
		if _, err := store.Persist(context.Background(), lease, input); err != nil {
			t.Fatal(err)
		}
		script.failExec("DELETE FROM audit_payload_refs")
		if _, err := store.CleanupRetention(context.Background(), lease, w14hRetentionConfig()); err == nil {
			t.Fatal("refs 删除失败必须透传")
		}
	})
	t.Run("retention delete attempts", func(t *testing.T) {
		store, script, lease, input := build(t)
		if _, err := store.Persist(context.Background(), lease, input); err != nil {
			t.Fatal(err)
		}
		script.failExec("DELETE FROM audit_log_attempts")
		if _, err := store.CleanupRetention(context.Background(), lease, w14hRetentionConfig()); err == nil {
			t.Fatal("attempts 删除失败必须透传")
		}
	})
	t.Run("retention delete logs", func(t *testing.T) {
		store, script, lease, input := build(t)
		if _, err := store.Persist(context.Background(), lease, input); err != nil {
			t.Fatal(err)
		}
		script.failExec("DELETE FROM audit_logs")
		if _, err := store.CleanupRetention(context.Background(), lease, w14hRetentionConfig()); err == nil {
			t.Fatal("logs 删除失败必须透传")
		}
	})
	t.Run("retention schedule gc", func(t *testing.T) {
		store, script, lease, input := build(t)
		if _, err := store.Persist(context.Background(), lease, input); err != nil {
			t.Fatal(err)
		}
		script.failExec("INSERT INTO audit_payload_blob_gc")
		if _, err := store.CleanupRetention(context.Background(), lease, w14hRetentionConfig()); err == nil {
			t.Fatal("GC 调度失败必须透传")
		}
	})
	t.Run("retention commit", func(t *testing.T) {
		store, script, lease, input := build(t)
		if _, err := store.Persist(context.Background(), lease, input); err != nil {
			t.Fatal(err)
		}
		script.commitErr = w14hAuditBoom
		if _, err := store.CleanupRetention(context.Background(), lease, w14hRetentionConfig()); err == nil {
			t.Fatal("retention commit 失败必须透传")
		}
	})
	t.Run("hot append begin", func(t *testing.T) {
		store, script, lease, _ := build(t)
		script.beginErr = w14hAuditBoom
		if _, err := store.AppendHotSearch(context.Background(), lease, []AuditLogInput{w14hPayloadInput("w14h-hot-x", "")}); err == nil {
			t.Fatal("hot begin 失败必须透传")
		}
	})
	t.Run("hot append lease", func(t *testing.T) {
		store, script, _, input := build(t)
		script.failExec("UPDATE audit_log_owner_leases")
		if _, err := store.AppendHotSearch(context.Background(), OwnerLease{OwnerID: "w14h-owner", FenceToken: 1}, []AuditLogInput{input}); err == nil {
			t.Fatal("hot 租约失败必须透传")
		}
	})
	t.Run("hot append commit", func(t *testing.T) {
		store, script, lease, _ := build(t)
		script.commitErr = w14hAuditBoom
		if _, err := store.AppendHotSearch(context.Background(), lease, []AuditLogInput{w14hPayloadInput("w14h-hot-y", "")}); err == nil {
			t.Fatal("hot commit 失败必须透传")
		}
	})
	t.Run("hot cleanup commit", func(t *testing.T) {
		store, script, lease, _ := build(t)
		script.commitErr = w14hAuditBoom
		if _, err := store.CleanupHotSearch(context.Background(), lease, time.Now().Add(time.Hour), 4); err == nil {
			t.Fatal("hot 清理 commit 失败必须透传")
		}
	})
	t.Run("retention hot dir error", func(t *testing.T) {
		store, script, lease, input := build(t)
		if _, err := store.Persist(context.Background(), lease, input); err != nil {
			t.Fatal(err)
		}
		occupied := filepath.Join(t.TempDir(), "occupied")
		if err := os.WriteFile(occupied, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		store.hotDir = occupied
		script.skipExec("UPDATE audit_log_owner_leases", 2)
		script.failExec("UPDATE audit_log_owner_leases")
		if _, err := store.CleanupRetention(context.Background(), lease, w14hRetentionConfig()); err == nil {
			t.Fatal("hot 目录读取失败必须透传")
		}
	})
}

func w14hRetentionConfig() RetentionConfig {
	return RetentionConfig{
		SuccessHotCutoff: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		SuccessCutoff:    time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		FailureCutoff:    time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		ErrorGroupCutoff: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		BatchSize:        50,
	}
}
