package groups

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// ---------------------------------------------------------------------------
// w9d：补齐 owner 校验、description 回读与事务中段失败臂。
// 不可达登记：DeleteGroup/DeleteRouteStrategy/RotateAPIKeySecret/DeleteAPIKey
// 的 owner err 体（301/505/713/779）在 requireWrite 保证 actor.SystemAccountID
// 非空且 owner(actor, "") 恒等于 actor 自身后不可达，属防御臂。
// ---------------------------------------------------------------------------

var w9dErrBoom = errors.New("w9d scripted failure")

func TestW9DOwnerFallbackEmptyID(t *testing.T) {
	svc := &Service{}
	// requested 与 actor.SystemAccountID 均为空 → owner 兜底拒绝。
	if _, err := svc.owner(Actor{Role: "admin"}, "  "); !errors.Is(err, ErrForbidden) {
		t.Fatalf("empty owner err=%v", err)
	}
}

func TestW9DGroupValidationAndDescriptionReadback(t *testing.T) {
	db := testDB(t)
	svc := wkReadyService(t, db, testCipher{})
	ctx := context.Background()
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	user := Actor{SystemAccountID: "sys-2", Role: "user"}
	if _, err := svc.CreateGroup(ctx, admin, GroupInput{Name: " ", ProviderCode: "openai"}); err == nil {
		t.Fatal("blank name must fail")
	}
	if _, err := svc.CreateGroup(ctx, admin, GroupInput{Name: "g", ProviderCode: " "}); err == nil {
		t.Fatal("blank provider must fail")
	}
	desc := "group desc"
	created, err := svc.CreateGroup(ctx, admin, GroupInput{Name: "g", ProviderCode: "openai", Description: &desc, Enabled: boolPtr(false)})
	if err != nil || created.Enabled {
		t.Fatalf("create=%+v err=%v", created, err)
	}
	found, err := svc.FindGroup(ctx, admin, "", created.ID)
	if err != nil || found.Description == nil || *found.Description != desc {
		t.Fatalf("find=%+v err=%v", found, err)
	}
	// 非 admin 代查/代改/代列他人 owner。
	if _, err := svc.ListGroups(ctx, user, "sys-1", 10); !errors.Is(err, ErrForbidden) {
		t.Fatalf("list cross owner err=%v", err)
	}
	if _, err := svc.UpdateGroup(ctx, user, created.ID, created.Revision, GroupInput{SystemAccountID: "sys-1", Name: "x", ProviderCode: "openai"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("update cross owner err=%v", err)
	}
	// 带 description 的列表回读。
	listed, err := svc.ListGroups(ctx, admin, "", 10)
	if err != nil || len(listed) != 1 || listed[0].Description == nil {
		t.Fatalf("list=%+v err=%v", listed, err)
	}
}

func TestW9DRouteStrategyOwnerAndDescriptionArms(t *testing.T) {
	db := testDB(t)
	svc := wkReadyService(t, db, testCipher{})
	ctx := context.Background()
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	user := Actor{SystemAccountID: "sys-2", Role: "user"}
	desc := "route desc"
	if _, err := svc.CreateRouteStrategy(ctx, user, RouteStrategyInput{SystemAccountID: "sys-1", Name: "cross", Bindings: []RouteBinding{{GroupID: "g"}}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("create route cross owner err=%v", err)
	}
	created, err := svc.CreateRouteStrategy(ctx, admin, RouteStrategyInput{Name: "r", Description: &desc, Bindings: []RouteBinding{{GroupID: ""}}})
	if err == nil {
		t.Fatal("empty binding must fail")
	}
	group, err := svc.CreateGroup(ctx, admin, GroupInput{Name: "g", ProviderCode: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	created, err = svc.CreateRouteStrategy(ctx, admin, RouteStrategyInput{Name: "r", Description: &desc, Bindings: []RouteBinding{{GroupID: group.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	found, err := svc.FindRouteStrategy(ctx, admin, "", created.ID)
	if err != nil || found.Description == nil || *found.Description != desc {
		t.Fatalf("find route=%+v err=%v", found, err)
	}
	if _, err := svc.FindRouteStrategy(ctx, user, "sys-1", created.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("find cross owner err=%v", err)
	}
	if _, err := svc.ListRouteStrategies(ctx, user, "sys-1", 10); !errors.Is(err, ErrForbidden) {
		t.Fatalf("list cross owner err=%v", err)
	}
	if _, err := svc.UpdateRouteStrategy(ctx, user, created.ID, created.Revision, RouteStrategyInput{SystemAccountID: "sys-1", Name: "x", Bindings: []RouteBinding{{GroupID: group.ID}}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("update cross owner err=%v", err)
	}
	// Update 的绑定校验臂（Create 侧已由 wk 覆盖）。
	if _, err := svc.UpdateRouteStrategy(ctx, admin, created.ID, created.Revision, RouteStrategyInput{Name: "x"}); err == nil || !strings.Contains(err.Error(), "at least one group binding") {
		t.Fatalf("update empty bindings err=%v", err)
	}
	groupB, err := svc.CreateGroup(ctx, admin, GroupInput{Name: "g-b", ProviderCode: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateRouteStrategy(ctx, admin, created.ID, created.Revision, RouteStrategyInput{Name: "x", Mode: "normal", Bindings: []RouteBinding{{GroupID: group.ID}, {GroupID: groupB.ID}}}); err == nil || !strings.Contains(err.Error(), "exactly one active group") {
		t.Fatalf("update normal mode err=%v", err)
	}
}

func TestW9DDeleteRouteStrategyReferencedByAPIKey(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO route_strategies (id,system_account_id,name,mode,status,is_default,created_at,updated_at) VALUES ('r-ref','sys-1','r','normal','active',0,'2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO api_keys (id,system_account_id,route_strategy_id,name,key_hash,key_prefix,key_suffix,key_secret_encrypted,status,is_default,purpose,created_at,updated_at) VALUES ('k-ref','sys-1','r-ref','k','h','p','s','e','active',0,'general','2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	svc := wkReadyService(t, db, testCipher{})
	// 非 default 且被 API key 引用 → 引用防护臂。
	if err := svc.DeleteRouteStrategy(context.Background(), Actor{SystemAccountID: "sys-1", Role: "admin"}, "r-ref", "2026-01-01"); err == nil || !strings.Contains(err.Error(), "referenced") {
		t.Fatalf("reference guard err=%v", err)
	}
}

func TestW9DAPIKeyOwnerAndDescriptionArms(t *testing.T) {
	db := testDB(t)
	svc := wkReadyService(t, db, testCipher{})
	ctx := context.Background()
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	user := Actor{SystemAccountID: "sys-2", Role: "user"}
	group, strategy := wkGroupFixture(t, svc, admin)
	_ = group
	desc := "key desc"
	created, secret, err := svc.CreateAPIKey(ctx, user, APIKeyInput{SystemAccountID: "sys-1", RouteStrategyID: strategy.ID, Name: "cross", Secret: "sk", Description: &desc})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("create cross owner err=%v secret=%q", err, secret)
	}
	created, secret, err = svc.CreateAPIKey(ctx, admin, APIKeyInput{RouteStrategyID: strategy.ID, Name: "k", Secret: "sk-1", Description: &desc})
	if err != nil || secret != "sk-1" {
		t.Fatalf("create=%+v err=%v", created, err)
	}
	found, err := svc.FindAPIKey(ctx, admin, "", created.ID)
	if err != nil || found.Description == nil || *found.Description != desc {
		t.Fatalf("find=%+v err=%v", found, err)
	}
	if _, err := svc.FindAPIKey(ctx, user, "sys-1", created.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("find cross owner err=%v", err)
	}
	if _, err := svc.ListAPIKeys(ctx, user, "sys-1", 10); !errors.Is(err, ErrForbidden) {
		t.Fatalf("list cross owner err=%v", err)
	}
	if _, err := svc.UpdateAPIKey(ctx, user, created.ID, created.Revision, APIKeyInput{SystemAccountID: "sys-1", RouteStrategyID: strategy.ID, Name: "x"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("update cross owner err=%v", err)
	}
}

func TestW9DRotateArmsOnRealDB(t *testing.T) {
	db := testDB(t)
	failSvc := wkReadyService(t, db, wkFailCipher{})
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	if _, _, err := failSvc.RotateAPIKeySecret(context.Background(), admin, "k", "rev", "sk"); err == nil || !strings.Contains(err.Error(), "encrypt api key secret") {
		t.Fatalf("rotate cipher err=%v", err)
	}
	// 默认 key 不可轮换（cipher 成功后才会触达默认检查）。
	if _, err := db.Exec(`INSERT INTO api_keys (id,system_account_id,route_strategy_id,name,key_hash,key_prefix,key_suffix,key_secret_encrypted,status,is_default,purpose,created_at,updated_at) VALUES ('k-def','sys-1','r','k','h','p','s','e','active',1,'general','2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	okSvc := wkReadyService(t, db, testCipher{})
	if _, _, err := okSvc.RotateAPIKeySecret(context.Background(), admin, "k-def", "2026-01-01", "sk"); err == nil || !strings.Contains(err.Error(), "default") {
		t.Fatalf("rotate default err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// 脚本化 driver：事务中段失败臂。
// ---------------------------------------------------------------------------

type w9dStep struct {
	contains    string
	cols        []string
	rows        [][]driver.Value
	rowsErr     error
	execErr     error
	affected    int64
	affectedErr error
	beginErr    error
	commitErr   error
}

type w9dScript struct{ steps []w9dStep }

func (s *w9dScript) next(kind, query string) (*w9dStep, error) {
	if len(s.steps) == 0 {
		return nil, fmt.Errorf("w9d script exhausted at %s: %s", kind, query)
	}
	step := &s.steps[0]
	s.steps = s.steps[1:]
	if step.contains != "" && !strings.Contains(query, step.contains) {
		return nil, fmt.Errorf("w9d step mismatch: want %q got %s: %s", step.contains, kind, query)
	}
	return step, nil
}

type w9dConn struct{ script *w9dScript }

func (c *w9dConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w9d: prepare unsupported")
}
func (c *w9dConn) Close() error { return nil }
func (c *w9dConn) Begin() (driver.Tx, error) {
	step, err := c.script.next("Begin", "BEGIN")
	if err != nil {
		return nil, err
	}
	if step.beginErr != nil {
		return nil, step.beginErr
	}
	return &w9dTx{script: c.script}, nil
}
func (c *w9dConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	step, err := c.script.next("Query", query)
	if err != nil {
		return nil, err
	}
	if step.rowsErr != nil {
		return nil, step.rowsErr
	}
	cols := step.cols
	if cols == nil {
		cols = []string{"id"}
	}
	return &w9dRows{cols: cols, values: step.rows}, nil
}
func (c *w9dConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	step, err := c.script.next("Exec", query)
	if err != nil {
		return nil, err
	}
	if step.execErr != nil {
		return nil, step.execErr
	}
	return w9dResult{affected: step.affected, err: step.affectedErr}, nil
}

type w9dTx struct{ script *w9dScript }

func (t *w9dTx) Commit() error {
	step, err := t.script.next("Commit", "COMMIT")
	if err != nil {
		return err
	}
	return step.commitErr
}
func (t *w9dTx) Rollback() error { return nil }

type w9dResult struct {
	affected int64
	err      error
}

func (r w9dResult) LastInsertId() (int64, error) { return 0, nil }
func (r w9dResult) RowsAffected() (int64, error) { return r.affected, r.err }

type w9dRows struct {
	cols   []string
	values [][]driver.Value
	index  int
}

func (r *w9dRows) Columns() []string { return r.cols }
func (r *w9dRows) Close() error      { return nil }
func (r *w9dRows) Next(dest []driver.Value) error {
	if r.index < len(r.values) {
		copy(dest, r.values[r.index])
		r.index++
		return nil
	}
	return io.EOF
}
func (r *w9dRows) Err() error { return nil }

type w9dDriver struct{ script *w9dScript }

func (d w9dDriver) Open(string) (driver.Conn, error) { return &w9dConn{script: d.script}, nil }

var w9dDriverSeq int64

func w9dOpen(t *testing.T, steps []w9dStep) *Service {
	t.Helper()
	name := fmt.Sprintf("w9d-groups-scripted-%d", atomic.AddInt64(&w9dDriverSeq, 1))
	sql.Register(name, w9dDriver{script: &w9dScript{steps: steps}})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = db.Close() })
	svc, err := New(db, modelcheckauth.SQLite, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, nil, testCipher{})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestW9DGroupAndRouteScriptedFailureArms(t *testing.T) {
	ctx := context.Background()
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	// UpdateGroup commit 失败。
	updateGroup := w9dOpen(t, []w9dStep{
		{beginErr: nil},
		{contains: "UPDATE groups SET", affected: 1},
		{commitErr: w9dErrBoom},
	})
	if _, err := updateGroup.UpdateGroup(ctx, admin, "g1", "rev", GroupInput{Name: "g", ProviderCode: "openai"}); err == nil {
		t.Fatal("update group commit failure must fail")
	}
	// DeleteGroup：DELETE 失败 / CAS / commit 失败。
	selectGroup := func() w9dStep {
		return w9dStep{contains: "SELECT is_default,updated_at FROM groups", cols: []string{"def", "rev"}, rows: [][]driver.Value{{0, "rev"}}}
	}
	countNone := func() w9dStep {
		return w9dStep{contains: "SELECT COUNT(1) FROM route_strategy_groups", cols: []string{"n"}, rows: [][]driver.Value{{0}}}
	}
	delExec := w9dOpen(t, []w9dStep{{beginErr: nil}, selectGroup(), countNone(), {contains: "DELETE FROM groups", execErr: w9dErrBoom}})
	if err := delExec.DeleteGroup(ctx, admin, "g1", "rev"); err == nil {
		t.Fatal("delete group exec failure must fail")
	}
	delCAS := w9dOpen(t, []w9dStep{{beginErr: nil}, selectGroup(), countNone(), {contains: "DELETE FROM groups", affected: 0}})
	if err := delCAS.DeleteGroup(ctx, admin, "g1", "rev"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("delete group cas err=%v", err)
	}
	delCommit := w9dOpen(t, []w9dStep{{beginErr: nil}, selectGroup(), countNone(), {contains: "DELETE FROM groups", affected: 1}, {commitErr: w9dErrBoom}})
	if err := delCommit.DeleteGroup(ctx, admin, "g1", "rev"); err == nil {
		t.Fatal("delete group commit failure must fail")
	}
	// CreateRouteStrategy commit 失败。
	createRoute := w9dOpen(t, []w9dStep{
		{beginErr: nil},
		{contains: "SELECT 1 FROM groups", cols: []string{"one"}, rows: [][]driver.Value{{1}}},
		{contains: "INSERT INTO route_strategies", affected: 1},
		{contains: "DELETE FROM route_strategy_groups", affected: 1},
		{contains: "INSERT INTO route_strategy_groups", affected: 1},
		{commitErr: w9dErrBoom},
	})
	if _, err := createRoute.CreateRouteStrategy(ctx, admin, RouteStrategyInput{Name: "r", Bindings: []RouteBinding{{GroupID: "g1"}}}); err == nil {
		t.Fatal("create route commit failure must fail")
	}
}

func TestW9DCreateGroupCommitFailure(t *testing.T) {
	svc := w9dOpen(t, []w9dStep{
		{beginErr: nil},
		{contains: "INSERT INTO groups", affected: 1},
		{commitErr: w9dErrBoom},
	})
	if _, err := svc.CreateGroup(context.Background(), Actor{SystemAccountID: "sys-1", Role: "admin"}, GroupInput{Name: "g", ProviderCode: "openai"}); err == nil {
		t.Fatal("commit failure must fail")
	}
}

func TestW9DListGroupsScriptedArms(t *testing.T) {
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	// Scan 失败：time.Time 无法赋给 *string。
	badScan := w9dOpen(t, []w9dStep{{cols: []string{"a", "b", "c", "d", "e", "f", "g", "h"}, rows: [][]driver.Value{{nil, "s", "n", "p", nil, 1, 0, "u"}}}})
	if _, err := badScan.ListGroups(context.Background(), admin, "", 10); err == nil {
		t.Fatal("scan failure must fail")
	}
	// description 回读臂。
	withDesc := w9dOpen(t, []w9dStep{{cols: []string{"a", "b", "c", "d", "e", "f", "g", "h"}, rows: [][]driver.Value{{"g1", "sys-1", "n", "p", []byte("d"), 1, 0, "u"}}}})
	listed, err := withDesc.ListGroups(context.Background(), admin, "", 10)
	if err != nil || len(listed) != 1 || listed[0].Description == nil {
		t.Fatalf("list=%+v err=%v", listed, err)
	}
}

func TestW9DRouteStrategyScriptedArms(t *testing.T) {
	ctx := context.Background()
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	detailCols := []string{"id", "system_account_id", "name", "description", "mode", "status", "is_default", "updated_at"}
	detailRow := []driver.Value{"r1", "sys-1", "n", nil, "normal", "active", 0, "u"}
	// 列表 scan 失败。
	badScan := w9dOpen(t, []w9dStep{{contains: "SELECT id FROM route_strategies", cols: []string{"id"}, rows: [][]driver.Value{{nil}}}})
	if _, err := badScan.ListRouteStrategies(ctx, admin, "", 10); err == nil {
		t.Fatal("list scan failure must fail")
	}
	// 列表项详情查询失败。
	badFind := w9dOpen(t, []w9dStep{
		{contains: "SELECT id FROM route_strategies", cols: []string{"id"}, rows: [][]driver.Value{{"r1"}}},
		{contains: "SELECT id,system_account_id,name,description", rowsErr: w9dErrBoom},
	})
	if _, err := badFind.ListRouteStrategies(ctx, admin, "", 10); err == nil {
		t.Fatal("find failure must fail")
	}
	// 绑定查询失败。
	badBindings := w9dOpen(t, []w9dStep{
		{cols: detailCols, rows: [][]driver.Value{detailRow}},
		{contains: "SELECT group_id,priority,weight,status", rowsErr: w9dErrBoom},
	})
	if _, err := badBindings.FindRouteStrategy(ctx, admin, "", "r1"); err == nil {
		t.Fatal("bindings query failure must fail")
	}
	// 绑定 scan 失败。
	badBindingScan := w9dOpen(t, []w9dStep{
		{cols: detailCols, rows: [][]driver.Value{detailRow}},
		{contains: "SELECT group_id,priority,weight,status", cols: []string{"a", "b", "c", "d"}, rows: [][]driver.Value{{nil, 1, 1, "active"}}},
	})
	if _, err := badBindingScan.FindRouteStrategy(ctx, admin, "", "r1"); err == nil {
		t.Fatal("bindings scan failure must fail")
	}
	// Update：UPDATE 失败 / replaceBindings 失败 / commit 失败。
	bindOK := func() w9dStep {
		return w9dStep{contains: "SELECT 1 FROM groups", cols: []string{"one"}, rows: [][]driver.Value{{1}}}
	}
	updateOK := func() w9dStep { return w9dStep{contains: "UPDATE route_strategies SET", affected: 1} }
	deleteOK := func() w9dStep { return w9dStep{contains: "DELETE FROM route_strategy_groups", affected: 1} }
	insertBad := w9dStep{contains: "INSERT INTO route_strategy_groups", execErr: w9dErrBoom}
	cases := []struct {
		name  string
		steps []w9dStep
	}{
		{"update exec", []w9dStep{{beginErr: nil}, bindOK(), {contains: "UPDATE route_strategies SET", execErr: w9dErrBoom}}},
		{"replace bindings", []w9dStep{{beginErr: nil}, bindOK(), updateOK(), deleteOK(), insertBad}},
		{"commit", []w9dStep{{beginErr: nil}, bindOK(), updateOK(), deleteOK(), {contains: "INSERT INTO route_strategy_groups", affected: 1}, {commitErr: w9dErrBoom}}},
	}
	for _, tc := range cases {
		svc := w9dOpen(t, tc.steps)
		in := RouteStrategyInput{Name: "r", Mode: "weight", Bindings: []RouteBinding{{GroupID: "g1"}}}
		if _, err := svc.UpdateRouteStrategy(ctx, admin, "r1", "rev", in); err == nil {
			t.Fatalf("update %s must fail", tc.name)
		}
	}
	// Delete：DELETE 失败 / CAS 冲突 / commit 失败。
	delExec := w9dOpen(t, []w9dStep{
		{beginErr: nil},
		{contains: "SELECT is_default,updated_at FROM route_strategies", cols: []string{"def", "rev"}, rows: [][]driver.Value{{0, "rev"}}},
		{contains: "SELECT COUNT(1) FROM api_keys", cols: []string{"n"}, rows: [][]driver.Value{{0}}},
		{contains: "DELETE FROM route_strategies", execErr: w9dErrBoom},
	})
	if err := delExec.DeleteRouteStrategy(ctx, admin, "r1", "rev"); err == nil {
		t.Fatal("delete route exec failure must fail")
	}
	cas := w9dOpen(t, []w9dStep{
		{beginErr: nil},
		{contains: "SELECT is_default,updated_at FROM route_strategies", cols: []string{"def", "rev"}, rows: [][]driver.Value{{0, "rev"}}},
		{contains: "SELECT COUNT(1) FROM api_keys", cols: []string{"n"}, rows: [][]driver.Value{{0}}},
		{contains: "DELETE FROM route_strategies", affected: 0},
	})
	if err := cas.DeleteRouteStrategy(ctx, admin, "r1", "rev"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("delete cas err=%v", err)
	}
	commit := w9dOpen(t, []w9dStep{
		{beginErr: nil},
		{contains: "SELECT is_default,updated_at FROM route_strategies", cols: []string{"def", "rev"}, rows: [][]driver.Value{{0, "rev"}}},
		{contains: "SELECT COUNT(1) FROM api_keys", cols: []string{"n"}, rows: [][]driver.Value{{0}}},
		{contains: "DELETE FROM route_strategies", affected: 1},
		{commitErr: w9dErrBoom},
	})
	if err := commit.DeleteRouteStrategy(ctx, admin, "r1", "rev"); err == nil {
		t.Fatal("delete commit failure must fail")
	}
}

func TestW9DAPIKeyScriptedArms(t *testing.T) {
	ctx := context.Background()
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	routeOK := func() w9dStep {
		return w9dStep{contains: "SELECT id FROM route_strategies", cols: []string{"id"}, rows: [][]driver.Value{{"r1"}}}
	}
	prefixOK := func() w9dStep {
		return w9dStep{contains: "SELECT key_prefix,key_suffix FROM api_keys", cols: []string{"p", "s"}, rows: [][]driver.Value{{"p", "s"}}}
	}
	// CreateAPIKey commit 失败。
	create := w9dOpen(t, []w9dStep{{beginErr: nil}, routeOK(), {contains: "INSERT INTO api_keys", affected: 1}, {commitErr: w9dErrBoom}})
	if _, _, err := create.CreateAPIKey(ctx, admin, APIKeyInput{RouteStrategyID: "r1", Name: "k", Secret: "sk"}); err == nil {
		t.Fatal("create commit failure must fail")
	}
	// ListAPIKeys：scan 失败 / FindAPIKey 失败。
	badScan := w9dOpen(t, []w9dStep{{contains: "SELECT id FROM api_keys", cols: []string{"id"}, rows: [][]driver.Value{{nil}}}})
	if _, err := badScan.ListAPIKeys(ctx, admin, "", 10); err == nil {
		t.Fatal("list scan failure must fail")
	}
	badFind := w9dOpen(t, []w9dStep{
		{contains: "SELECT id FROM api_keys", cols: []string{"id"}, rows: [][]driver.Value{{"k1"}}},
		{contains: "SELECT id,system_account_id,route_strategy_id", rowsErr: w9dErrBoom},
	})
	if _, err := badFind.ListAPIKeys(ctx, admin, "", 10); err == nil {
		t.Fatal("list find failure must fail")
	}
	// UpdateAPIKey：UPDATE 失败 / CAS / commit 失败。
	cases := []struct {
		name  string
		steps []w9dStep
	}{
		{"update exec", []w9dStep{{beginErr: nil}, routeOK(), prefixOK(), {contains: "UPDATE api_keys SET", execErr: w9dErrBoom}}},
		{"cas", []w9dStep{{beginErr: nil}, routeOK(), prefixOK(), {contains: "UPDATE api_keys SET", affected: 0}}},
		{"commit", []w9dStep{{beginErr: nil}, routeOK(), prefixOK(), {contains: "UPDATE api_keys SET", affected: 1}, {commitErr: w9dErrBoom}}},
	}
	for _, tc := range cases {
		svc := w9dOpen(t, tc.steps)
		if _, err := svc.UpdateAPIKey(ctx, admin, "k1", "rev", APIKeyInput{RouteStrategyID: "r1", Name: "k"}); err == nil {
			t.Fatalf("update %s must fail", tc.name)
		}
	}
}

func TestW9DRotateAndDeleteAPIKeyScriptedArms(t *testing.T) {
	ctx := context.Background()
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	keyCols := []string{"id", "system_account_id", "route_strategy_id", "name", "description", "status", "is_default", "purpose"}
	keyRow := []driver.Value{"k1", "sys-1", "r1", "k", nil, "active", 0, "general"}
	keyRowDesc := []driver.Value{"k1", "sys-1", "r1", "k", []byte("d"), "active", 0, "general"}
	updateOK := func() w9dStep { return w9dStep{contains: "UPDATE api_keys SET key_hash", affected: 1} }
	cases := []struct {
		name  string
		steps []w9dStep
	}{
		{"update exec", []w9dStep{{beginErr: nil}, {cols: keyCols, rows: [][]driver.Value{keyRow}}, {contains: "UPDATE api_keys SET key_hash", execErr: w9dErrBoom}}},
		{"cas", []w9dStep{{beginErr: nil}, {cols: keyCols, rows: [][]driver.Value{keyRow}}, {contains: "UPDATE api_keys SET key_hash", affected: 0}}},
		{"commit", []w9dStep{{beginErr: nil}, {cols: keyCols, rows: [][]driver.Value{keyRow}}, updateOK(), {commitErr: w9dErrBoom}}},
	}
	for _, tc := range cases {
		svc := w9dOpen(t, tc.steps)
		if _, _, err := svc.RotateAPIKeySecret(ctx, admin, "k1", "rev", "sk-new"); err == nil {
			t.Fatalf("rotate %s must fail", tc.name)
		}
	}
	// 轮换成功 + description 回读臂。
	ok := w9dOpen(t, []w9dStep{{beginErr: nil}, {cols: keyCols, rows: [][]driver.Value{keyRowDesc}}, updateOK(), {commitErr: nil}})
	rotated, _, err := ok.RotateAPIKeySecret(ctx, admin, "k1", "rev", "sk-new")
	if err != nil || rotated.Description == nil || *rotated.Description != "d" {
		t.Fatalf("rotate=%+v err=%v", rotated, err)
	}
	// DeleteAPIKey：DELETE 失败 / CAS。
	delExec := w9dOpen(t, []w9dStep{
		{beginErr: nil},
		{contains: "SELECT is_default,updated_at FROM api_keys", cols: []string{"def", "rev"}, rows: [][]driver.Value{{0, "rev"}}},
		{contains: "DELETE FROM api_keys", execErr: w9dErrBoom},
	})
	if err := delExec.DeleteAPIKey(ctx, admin, "k1", "rev"); err == nil {
		t.Fatal("delete exec failure must fail")
	}
	delCAS := w9dOpen(t, []w9dStep{
		{beginErr: nil},
		{contains: "SELECT is_default,updated_at FROM api_keys", cols: []string{"def", "rev"}, rows: [][]driver.Value{{0, "rev"}}},
		{contains: "DELETE FROM api_keys", affected: 0},
	})
	if err := delCAS.DeleteAPIKey(ctx, admin, "k1", "rev"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("delete cas err=%v", err)
	}
}
