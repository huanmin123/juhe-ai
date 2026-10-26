// w13g2 失败注入驱动（仅测试编译）：包装 pgx stdlib 驱动，在
// QueryContext/ExecContext 层按 SQL 片段匹配注入错误，从而覆盖 store 各
// 函数事务中途的错误返回臂（canceled context 只能命中事务内第一条语句，
// 覆盖不到深层分支）。failpoint 用完立即 disarm，不影响正常路径。
// 匹配前按 w13g2FailpointMatchQuery 归一（去 schema 前缀、$n 还原 ?），注入
// 模式保持裸表名 + `?` 的方言惯用形态（2026-10-25 存储形态收尾，自
// modernc.org/sqlite 内核迁 pgx stdlib 内核 + pgtestkit 隔离真库）。
package authz

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
	"github.com/jackc/pgx/v5/stdlib"
)

var errW13g2Failpoint = errors.New("w13g2 failpoint: injected failure")

// w13g2FailpointSchemaPrefixes 是归一化剥除的生产 schema 前缀（authz 生产
// SQL 的 s.table 全限定 juhe_business；兜底列出其余前缀族）。
var w13g2FailpointSchemaPrefixes = []string{"juhe_business.", "juhe_chat.", "juhe_stats.", "juhe_j3b.", "juhe_dataset."}

var w13g2FailpointPlaceholderPattern = regexp.MustCompile(`\$\d+`)

// w13g2FailpointMatchQuery 把生产方言 SQL 归一成注入模式惯用的裸表名 + `?`
// 形态：失败注入关心「命中了哪条语句」，与占位符编号和 schema 限定无关。
func w13g2FailpointMatchQuery(query string) string {
	for _, prefix := range w13g2FailpointSchemaPrefixes {
		query = strings.ReplaceAll(query, prefix, "")
	}
	return w13g2FailpointPlaceholderPattern.ReplaceAllString(query, "?")
}

type w13g2Failpoint struct {
	mu      sync.Mutex
	pattern string
	scan    bool // true = 注入单列假行，驱动 rows.Scan 列数不匹配错误
}

func (fp *w13g2Failpoint) arm(pattern string) {
	fp.mu.Lock()
	fp.pattern = pattern
	fp.mu.Unlock()
}

func (fp *w13g2Failpoint) armScan(pattern string) {
	fp.mu.Lock()
	fp.pattern = pattern
	fp.scan = true
	fp.mu.Unlock()
}

func (fp *w13g2Failpoint) disarm() {
	fp.mu.Lock()
	fp.pattern = ""
	fp.scan = false
	fp.mu.Unlock()
}

func (fp *w13g2Failpoint) armed(query string) bool {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return fp.pattern != "" && !fp.scan && strings.Contains(w13g2FailpointMatchQuery(query), w13g2FailpointMatchQuery(fp.pattern))
}

func (fp *w13g2Failpoint) scanArmed(query string) bool {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return fp.pattern != "" && fp.scan && strings.Contains(w13g2FailpointMatchQuery(query), w13g2FailpointMatchQuery(fp.pattern))
}

// w13g2FakeRows 单列假行：与代码多列 Scan 目标不匹配，触发 rows.Scan 错误
// 分支。
type w13g2FakeRows struct {
	sent bool
}

func (r *w13g2FakeRows) Columns() []string { return []string{"c0"} }

func (r *w13g2FakeRows) Close() error { return nil }

func (r *w13g2FakeRows) Next(dest []driver.Value) error {
	if r.sent {
		return context.Canceled
	}
	r.sent = true
	if len(dest) > 0 {
		dest[0] = int64(7)
	}
	return nil
}

type w13g2FailDriver struct {
	inner driver.Driver
	fp    *w13g2Failpoint
}

func (d *w13g2FailDriver) Open(name string) (driver.Conn, error) {
	raw, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	conn := &w13g2FailConn{Conn: raw, fp: d.fp}
	if beginTx, ok := raw.(driver.ConnBeginTx); ok {
		conn.beginTx = beginTx
	}
	if queryer, ok := raw.(driver.QueryerContext); ok {
		conn.queryer = queryer
	}
	if execer, ok := raw.(driver.ExecerContext); ok {
		conn.execer = execer
	}
	return conn, nil
}

type w13g2FailConn struct {
	driver.Conn
	beginTx driver.ConnBeginTx
	queryer driver.QueryerContext
	execer  driver.ExecerContext
	fp      *w13g2Failpoint
}

func (c *w13g2FailConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.beginTx == nil {
		return nil, driver.ErrSkip
	}
	return c.beginTx.BeginTx(ctx, opts)
}

func (c *w13g2FailConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.fp.scanArmed(query) {
		return &w13g2FakeRows{}, nil
	}
	if c.fp.armed(query) {
		return nil, errW13g2Failpoint
	}
	if c.queryer == nil {
		return nil, driver.ErrSkip
	}
	return c.queryer.QueryContext(ctx, query, args)
}

func (c *w13g2FailConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.fp.armed(query) {
		return nil, errW13g2Failpoint
	}
	if c.execer == nil {
		return nil, driver.ErrSkip
	}
	return c.execer.ExecContext(ctx, query, args)
}

var w13g2FailDrivers sync.Map

// w13g2RegisterFailDriver 按唯一名字注册（或复用）失败注入驱动；重复运行
// （-count=N）复用同一实例避免 sql.Register 重名 panic。内核为 pgx stdlib，
// 外层 sqldialect 包装使夹具/测试手写 SQL 的裸 `?` 直发可用。
func w13g2RegisterFailDriver(key string) *w13g2FailDriver {
	if loaded, ok := w13g2FailDrivers.Load(key); ok {
		return loaded.(*w13g2FailDriver)
	}
	inner := sqldialect.WrapDriver(stdlib.GetDefaultDriver())
	d := &w13g2FailDriver{inner: inner, fp: &w13g2Failpoint{}}
	if actual, loaded := w13g2FailDrivers.LoadOrStore(key, d); loaded {
		return actual.(*w13g2FailDriver)
	}
	sql.Register("w13g2fail-"+key, d)
	return d
}

// w13g2FailStore 打开与 fixture 同构的失败注入库：pgtestkit 隔离 PG 测试库
// + authzPGFixtureSchema（juhe_business 限定，与 pgkit_test.go 组A 夹具同一
// 生产形状）+ pgx 驱动内核（2026-10-25 存储形态收尾）。
func w13g2FailStore(t *testing.T) (*Store, *w13g2Failpoint) {
	t.Helper()
	key := strings.ReplaceAll(t.Name(), "/", "-")
	d := w13g2RegisterFailDriver(key)
	tdb := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, authzPGFixtureSchema)
		return err
	})
	db, err := sql.Open("w13g2fail-"+key, tdb.DSN+"&search_path=juhe_business")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStore(db, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return store, d.fp
}

func w13g2SeedFail(t *testing.T, s *Store) (*sql.DB, *CreateResult) {
	t.Helper()
	db := s.db
	for _, id := range []string{"owner", "grantee"} {
		if _, err := db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
			VALUES (?, ?, ?, 'user', 'active', 'x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_f13', 'G', 'owner', 'active')`); err != nil {
		t.Fatal(err)
	}
	created, err := s.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_f13",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	return db, created
}

// TestW13g2FailpointArms 批量覆盖事务深层错误臂：每个子测试自建独立授权
// （独立资源与 grantee），武装一个 SQL 片段 → 调用写路径 → 断言失败。
func TestW13g2FailpointArms(t *testing.T) {
	s, fp := w13g2FailStore(t)
	db := s.db
	ctx := context.Background()
	for _, id := range []string{"ga", "gb", "gc", "gd", "ge", "gf", "gg", "gh", "gi", "gj", "gk", "gl", "gm"} {
		if _, err := db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
			VALUES (?, ?, ?, 'user', 'active', 'x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, id, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, groupID := range []string{"grpA", "grpB", "grpC", "grpD", "grpE", "grpF", "grpG", "grpH", "grpI", "grpJ", "grpK", "grpL", "grpM"} {
		if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES (?, 'G', 'owner', 'active')`, groupID); err != nil {
			t.Fatal(err)
		}
	}

	// w13g2FpGrant 创建一条全新授权，返回 grant id 与当前版本。
	w13g2FpGrant := func(t *testing.T, resourceID, granteeID string) (string, string) {
		t.Helper()
		created, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: resourceID,
			GranteeType: "system_account", GranteeID: granteeID,
		}, "owner")
		if err != nil {
			t.Fatal(err)
		}
		return created.Item.ID, created.Item.UpdatedAt
	}

	t.Run("syncUserPausedUpdate", func(t *testing.T) {
		gid, ver := w13g2FpGrant(t, "grpA", "ga")
		fp.arm("ELSE 'authorization_paused' END")
		defer fp.disarm()
		if _, err := s.PatchForOwner(ctx, gid, PatchInput{Status: func() *string { v := StatusPaused; return &v }()}, ver, "owner", ""); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("syncTeamRowUpdate", func(t *testing.T) {
		if _, err := db.Exec(`INSERT INTO system_teams (id, name, status, created_by, created_at, updated_at)
			VALUES ('team_fp13', 'T', 'active', 'owner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_by, created_at, updated_at)
			VALUES ('teammem_fp13', 'team_fp13', 'gm', 'active', '2026-01-01T00:00:00Z', 'owner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		created, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grpB",
			GranteeType: "team", GranteeID: "team_fp13",
		}, "owner")
		if err != nil {
			t.Fatal(err)
		}
		fp.arm("THEN NULL ELSE ? END")
		defer fp.disarm()
		if _, err := s.PatchForOwner(ctx, created.Item.ID, PatchInput{Status: func() *string { v := StatusPaused; return &v }()}, created.Item.UpdatedAt, "owner", ""); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("refreshActiveTeamQuery", func(t *testing.T) {
		gid, ver := w13g2FpGrant(t, "grpC", "gb")
		fp.arm("trg.expires_at > ?")
		defer fp.disarm()
		if _, err := s.PatchForOwner(ctx, gid, PatchInput{Status: func() *string { v := StatusPaused; return &v }()}, ver, "owner", ""); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("upsertSourceSelect", func(t *testing.T) {
		gid, ver := w13g2FpGrant(t, "grpD", "gc")
		fp.arm("SELECT id FROM resource_authorization_sources")
		defer fp.disarm()
		if _, err := s.PatchForOwner(ctx, gid, PatchInput{LimitsSet: true, LimitsJSON: func() *string { v := `{"daily":{"enabled":true,"limit":7}}`; return &v }()}, ver, "owner", ""); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("hasActiveTeamSource", func(t *testing.T) {
		gid, ver := w13g2FpGrant(t, "grpE", "gd")
		fp.arm("SELECT ras.id")
		defer fp.disarm()
		if _, err := s.PatchForOwner(ctx, gid, PatchInput{LimitsSet: true, LimitsJSON: func() *string { v := `{"daily":{"enabled":true,"limit":8}}`; return &v }()}, ver, "owner", ""); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("teamSourcesUpdate", func(t *testing.T) {
		if _, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grpF",
			GranteeType: "team", GranteeID: "team_fp13",
		}, "owner"); err != nil {
			t.Fatal(err)
		}
		fp.arm("COALESCE(ended_reason, ?)")
		defer fp.disarm()
		if err := s.RevokeAllTeamSources(ctx, "team_fp13", "owner", "team_disabled"); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("runtimeUpsertUpdate", func(t *testing.T) {
		gid, ver := w13g2FpGrant(t, "grpG", "ge")
		fp.arm("SET resource_owner_system_account_id = ?")
		defer fp.disarm()
		if _, err := s.PatchForOwner(ctx, gid, PatchInput{LimitsSet: true, LimitsJSON: func() *string { v := `{"daily":{"enabled":true,"limit":3}}`; return &v }()}, ver, "owner", ""); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("supersedeManualSource", func(t *testing.T) {
		// 先建 direct 授权（manual source active），再建同资源团队授权：
		// team 展开写入 runtime 时把 manual source 置为 superseded
		//（covered_by_team UPDATE）。
		if _, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grpH",
			GranteeType: "system_account", GranteeID: "gm",
		}, "owner"); err != nil {
			t.Fatal(err)
		}
		fp.arm("covered_by_team")
		defer fp.disarm()
		if _, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grpH",
			GranteeType: "team", GranteeID: "team_fp13",
		}, "owner"); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("revokeSyncUpdate", func(t *testing.T) {
		gid, ver := w13g2FpGrant(t, "grpI", "gf")
		fp.arm("SET status = 'revoked', revoked_by = ?, revoked_at = ?, updated_at = ?")
		defer fp.disarm()
		if _, err := s.Revoke(ctx, gid, ver, "owner"); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("returnPaths", func(t *testing.T) {
		gid, ver := w13g2FpGrant(t, "grpJ", "gg")
		// findRuntimeID 查询失败（Return 714-717）。
		fp.arm("grantee_system_account_id = ?")
		if _, err := s.Return(ctx, gid, ver, "gg"); err == nil {
			fp.disarm()
			t.Fatalf("应失败")
		}
		fp.disarm()
		// grant UPDATE 失败。
		fp.arm("SET status = 'returned'")
		if _, err := s.Return(ctx, gid, ver, "gg"); err == nil {
			fp.disarm()
			t.Fatalf("应失败")
		}
		fp.disarm()
		// sources UPDATE 失败。
		fp.arm("status IN ('active', 'superseded')")
		if _, err := s.Return(ctx, gid, ver, "gg"); err == nil {
			fp.disarm()
			t.Fatalf("应失败")
		}
		fp.disarm()
		// quota bindings 前置查询失败。
		fp.arm("FROM request_quota_hourly_window_scope_bindings")
		defer fp.disarm()
		if _, err := s.Return(ctx, gid, ver, "gg"); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("sweepArms", func(t *testing.T) {
		past := time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
		if _, err := db.Exec(`INSERT INTO resource_authorization_grants
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_system_account_id, scope, status, expires_at, created_by, created_at, updated_at)
			VALUES ('grant_fp13', 'group', 'grpK', 'owner', 'system_account', 'gi', 'use', 'active', ?, 'c', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, past); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO resource_authorizations
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status, activated_at, created_by, created_at, updated_at)
			VALUES ('rt_fp13', 'group', 'grpK', 'owner', 'gi', 'use', 'active', '2026-01-01T00:00:00Z', 'c', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO resource_authorization_sources
			(id, authorization_id, source_type, status, activated_at, created_by, created_at, updated_at)
			VALUES ('src_fp13', 'rt_fp13', 'manual', 'active', '2026-01-01T00:00:00Z', 'c', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		// GetGrantForMutation 失败。
		fp.arm("WHERE g.id = ?")
		if _, err := s.ExpireSweep(ctx, 0); err == nil {
			fp.disarm()
			t.Fatalf("应失败")
		}
		fp.disarm()
		// 到期 UPDATE 失败。
		fp.arm("SET status = 'expired', revoked_at = COALESCE(revoked_at, ?)")
		if _, err := s.ExpireSweep(ctx, 0); err == nil {
			fp.disarm()
			t.Fatalf("应失败")
		}
		fp.disarm()
		// expired 同步的 refreshEffectiveSource 查询失败。
		fp.arm("trg.expires_at > ?")
		defer fp.disarm()
		if _, err := s.ExpireSweep(ctx, 0); err == nil {
			t.Fatalf("应失败")
		}
		fp.disarm()
		if _, err := db.Exec(`DELETE FROM resource_authorization_sources WHERE id = 'src_fp13'`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DELETE FROM resource_authorizations WHERE id = 'rt_fp13'`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DELETE FROM resource_authorization_grants WHERE id = 'grant_fp13'`); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("reconcileTeamSync", func(t *testing.T) {
		past := time.Now().Add(-time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
		if _, err := db.Exec(`INSERT INTO resource_authorization_grants
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_team_id, scope, status, expires_at, revoked_at, created_by, created_at, updated_at)
			VALUES ('grant_fp13t', 'group', 'grpL', 'owner', 'team', 'team_fp13', 'use', 'expired', NULL, ?, 'c', '2026-01-01T00:00:00Z', ?)`, past, past); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO resource_authorizations
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status, effective_source_type, effective_source_team_id, activated_at, created_by, created_at, updated_at)
			VALUES ('rt_fp13t', 'group', 'grpL', 'owner', 'gm', 'use', 'active', 'team', 'team_fp13', '2026-01-01T00:00:00Z', 'c', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO resource_authorization_sources
			(id, authorization_id, source_type, source_team_id, status, activated_at, created_by, created_at, updated_at)
			VALUES ('src_fp13t', 'rt_fp13t', 'team', 'team_fp13', 'active', '2026-01-01T00:00:00Z', 'c', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		fp.arm("THEN NULL ELSE ? END")
		defer fp.disarm()
		if _, err := s.ReconcileExpiredGrants(ctx, 0, 0); err == nil {
			t.Fatalf("should fail")
		}
		fp.disarm()
		for _, target := range []string{"DELETE FROM resource_authorization_sources WHERE id = 'src_fp13t'",
			"DELETE FROM resource_authorizations WHERE id = 'rt_fp13t'",
			"DELETE FROM resource_authorization_grants WHERE id = 'grant_fp13t'"} {
			if _, err := db.Exec(target); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("reconcileQuotaBindings", func(t *testing.T) {
		past := time.Now().Add(-time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
		if _, err := db.Exec(`INSERT INTO resource_authorization_grants
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_system_account_id, scope, status, expires_at, revoked_at, created_by, created_at, updated_at)
			VALUES ('grant_fp13q', 'group', 'grpM', 'owner', 'system_account', 'gj', 'use', 'expired', NULL, ?, 'c', '2026-01-01T00:00:00Z', ?)`, past, past); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO resource_authorizations
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status, activated_at, created_by, created_at, updated_at)
			VALUES ('rt_fp13q', 'group', 'grpM', 'owner', 'gj', 'use', 'active', '2026-01-01T00:00:00Z', 'c', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO resource_authorization_sources
			(id, authorization_id, source_type, status, activated_at, created_by, created_at, updated_at)
			VALUES ('src_fp13q', 'rt_fp13q', 'manual', 'active', '2026-01-01T00:00:00Z', 'c', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		fp.arm("FROM request_quota_hourly_window_scope_bindings")
		defer fp.disarm()
		if _, err := s.ReconcileExpiredGrants(ctx, 0, 0); err == nil {
			t.Fatalf("should fail")
		}
		fp.disarm()
		for _, target := range []string{"DELETE FROM resource_authorization_sources WHERE id = 'src_fp13q'",
			"DELETE FROM resource_authorizations WHERE id = 'rt_fp13q'",
			"DELETE FROM resource_authorization_grants WHERE id = 'grant_fp13q'"} {
			if _, err := db.Exec(target); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("instanceNameAvailable", func(t *testing.T) {
		fp.arm("SELECT id FROM accounts")
		defer fp.disarm()
		if _, err := s.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: "acc_none",
			GranteeType: "system_account", GranteeID: "gk",
			TargetGroupID: func() *string { v := "grp_f13b"; return &v }(),
		}, "owner"); err == nil {
			t.Fatalf("should fail")
		}
	})

	t.Run("loadStatsAgg", func(t *testing.T) {
		fp.arm("COUNT(DISTINCT ra.id) AS authorization_count")
		defer fp.disarm()
		if _, err := s.ResourceAuthorizationStatsByResourceIds(ctx, "group", []string{"grpM"}); err == nil {
			t.Fatalf("should fail")
		}
	})
}
