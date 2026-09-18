package accounts

// w14b Scan 错误注入：QueryContext 返回一行全 NULL 值，命中循环 Scan 的
// 错误分支（NULL 不能落入 string/int 非空目标）。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
)

type w14bNilRowConnector struct{}

func (w14bNilRowConnector) Connect(context.Context) (driver.Conn, error) {
	return w14bNilRowConn{}, nil
}
func (w14bNilRowConnector) Driver() driver.Driver { return w14bBoomDriver{} }

type w14bNilRowConn struct{}

func (c w14bNilRowConn) Prepare(string) (driver.Stmt, error) { return nil, w14bBoom }
func (c w14bNilRowConn) Close() error                        { return nil }
func (c w14bNilRowConn) Begin() (driver.Tx, error)           { return nil, w14bBoom }
func (c w14bNilRowConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &w14bNilRows{}, nil
}
func (c w14bNilRowConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, w14bBoom
}

type w14bNilRows struct{ consumed bool }

func (w14bNilRows) Columns() []string { return []string{"c0", "c1", "c2", "c3"} }
func (w14bNilRows) Close() error      { return nil }
func (r *w14bNilRows) Next(dest []driver.Value) error {
	if r.consumed {
		return w14bBoom
	}
	r.consumed = true
	for index := range dest {
		dest[index] = nil
	}
	return nil
}

func TestW14BFaultInjectedScanArms(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedAccount(t, "acc-w14b-scan", adminID, "w14b-scan", "active")
	ctx := context.Background()
	clone := *env.store
	clone.db = sql.OpenDB(w14bNilRowConnector{})
	store := &clone
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}

	// Scan 错误臂（NULL → string/int 目标失败）。
	if _, err := store.ListPage(ctx, scope, ListOptions{Page: 1, PageSize: 5}); err == nil {
		t.Fatal("ListPage scan 应失败")
	}
	if _, err := store.loadBatchSupportedModels(ctx, store.db, []string{"a"}); err == nil {
		t.Fatal("loadBatchSupportedModels scan 应失败")
	}
	if _, err := store.loadBatchTags(ctx, store.db, []string{"a"}); err == nil {
		t.Fatal("loadBatchTags scan 应失败")
	}
	if _, err := store.ListTags(ctx, scope); err == nil {
		t.Fatal("ListTags scan 应失败")
	}
	if _, err := store.protocolProviderCodes(ctx, "openai", "v1"); err == nil {
		t.Fatal("protocolProviderCodes scan 应失败")
	}
	if err := store.hydrateTags(ctx, []ListItem{{ID: "acc-w14b-scan"}}, []string{"acc-w14b-scan"}); err == nil {
		t.Fatal("hydrateTags scan 应失败")
	}
	if err := store.hydrateLockStates(ctx, []ListItem{{ID: "acc-w14b-scan"}}, []string{"acc-w14b-scan"}); err == nil {
		t.Fatal("hydrateLockStates scan 应失败")
	}
	if _, err := store.loadImportProviders(ctx); err == nil {
		t.Fatal("loadImportProviders scan 应失败")
	}
	if _, err := store.listSessionTasks(ctx, store.db, "sess-x", &scope); err == nil {
		t.Fatal("listSessionTasks scan 应失败")
	}
	if _, err := store.loadBalanceSnapshotRecord(ctx, "acc-w14b-scan"); err == nil {
		t.Fatal("loadBalanceSnapshotRecord scan 应失败")
	}
	if _, err := store.collectTestCatalogCandidates(ctx, []string{"gpt"}, []string{"gpt"}, "m", ""); err == nil {
		t.Fatal("collectTestCatalogCandidates scan 应失败")
	}
	if _, err := store.loadTestAccountModelMappings(ctx, store.db, "acc-w14b-scan", ""); err == nil {
		t.Fatal("loadTestAccountModelMappings scan 应失败")
	}
	if err := store.markBatchGroupStatsDirty(ctx, []string{"a"}, "w14b"); err == nil {
		t.Fatal("markBatchGroupStatsDirty scan 应失败")
	}
}
