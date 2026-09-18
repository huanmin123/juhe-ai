package accounts

// w14b rows 级故障注入：QueryContext 返回一个 Next 即失败的 rows，命中
// 各读取循环之后的 rows.Err() 错误臂与 QueryRow 的 Scan 错误臂。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
)

type w14bRowsBoomConnector struct{}

func (w14bRowsBoomConnector) Connect(context.Context) (driver.Conn, error) {
	return w14bRowsBoomConn{}, nil
}
func (w14bRowsBoomConnector) Driver() driver.Driver { return w14bBoomDriver{} }

type w14bRowsBoomConn struct{}

func (c w14bRowsBoomConn) Prepare(string) (driver.Stmt, error) { return nil, w14bBoom }
func (c w14bRowsBoomConn) Close() error                        { return nil }
func (c w14bRowsBoomConn) Begin() (driver.Tx, error)           { return nil, w14bBoom }
func (c w14bRowsBoomConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return w14bErrRows{}, nil
}
func (c w14bRowsBoomConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, w14bBoom
}

type w14bErrRows struct{}

func (w14bErrRows) Columns() []string { return []string{"x"} }
func (w14bErrRows) Close() error      { return nil }
func (w14bErrRows) Next([]driver.Value) error {
	return w14bBoom
}

func w14bRowsBoomStore(env *testEnv) *Store {
	clone := *env.store
	clone.db = sql.OpenDB(w14bRowsBoomConnector{})
	return &clone
}

func TestW14BFaultInjectedRowsArms(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedAccount(t, "acc-w14b-rb", adminID, "w14b-rb", "active")
	ctx := context.Background()
	store := w14bRowsBoomStore(env)
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}

	// rows.Err 臂（循环后统一错误检查）。
	if _, err := store.ListPage(ctx, scope, ListOptions{Page: 1, PageSize: 5}); err == nil {
		t.Fatal("ListPage rows.Err 应传播")
	}
	if _, err := store.loadBatchSupportedModels(ctx, store.db, []string{"a"}); err == nil {
		t.Fatal("loadBatchSupportedModels rows.Err 应传播")
	}
	if _, err := store.loadBatchModelMappings(ctx, store.db, []string{"a"}); err == nil {
		t.Fatal("loadBatchModelMappings rows.Err 应传播")
	}
	if _, err := store.loadBatchTags(ctx, store.db, []string{"a"}); err == nil {
		t.Fatal("loadBatchTags rows.Err 应传播")
	}
	if _, err := store.ListTags(ctx, scope); err == nil {
		t.Fatal("ListTags rows.Err 应传播")
	}
	if _, err := store.protocolProviderCodes(ctx, "openai", "v1"); err == nil {
		t.Fatal("protocolProviderCodes rows.Err 应传播")
	}
	if _, err := store.listBuiltInTestCatalogOptions(ctx, []string{"gpt"}, ManualTestOptionsQuery{}); err == nil {
		t.Fatal("listBuiltInTestCatalogOptions rows.Err 应传播")
	}
	if _, err := store.listCustomTestCatalogOptions(ctx, []string{"gpt"}, adminID, ManualTestOptionsQuery{}); err == nil {
		t.Fatal("listCustomTestCatalogOptions rows.Err 应传播")
	}
	if _, err := store.collectTestCatalogCandidates(ctx, []string{"gpt"}, []string{"gpt"}, "m", ""); err == nil {
		t.Fatal("collectTestCatalogCandidates rows.Err 应传播")
	}
	if err := store.hydrateTags(ctx, []ListItem{{ID: "acc-w14b-rb"}}, []string{"acc-w14b-rb"}); err == nil {
		t.Fatal("hydrateTags rows.Err 应传播")
	}
	if err := store.hydrateLockStates(ctx, []ListItem{{ID: "acc-w14b-rb"}}, []string{"acc-w14b-rb"}); err == nil {
		t.Fatal("hydrateLockStates rows.Err 应传播")
	}
	if err := store.hydrateOAuthUsageSnapshots(ctx, []ListItem{{ID: "acc-w14b-rb", ProviderCode: "gpt", Type: "oauth"}}); err == nil {
		t.Fatal("hydrateOAuthUsageSnapshots rows.Err 应传播")
	}
	if err := store.markBatchGroupStatsDirty(ctx, []string{"a"}, "w14b"); err == nil {
		t.Fatal("markBatchGroupStatsDirty rows.Err 应传播")
	}
	if _, err := store.listSessionTasks(ctx, store.db, "sess-x", &scope); err == nil {
		t.Fatal("listSessionTasks rows.Err 应传播")
	}
	// QueryRow → Scan 错误臂。
	if _, err := store.loadBalanceSnapshotRecord(ctx, "acc-w14b-rb"); err == nil {
		t.Fatal("loadBalanceSnapshotRecord scan 应传播")
	}
	if _, err := store.loadAuthorizedDispatchRow(ctx, store.db, "acc-w14b-rb", adminID); err == nil {
		t.Fatal("loadAuthorizedDispatchRow scan 应传播")
	}
	if _, err := store.loadAuthorizedDispatchBinding(ctx, store.db, &authorizedDispatchRow{id: "acc-w14b-rb"}); err == nil {
		t.Fatal("loadAuthorizedDispatchBinding scan 应传播")
	}
	if _, err := store.migrateOwnerTraffic(ctx, "acc-w14b-rb", TrafficMigrationInput{}, scope); err == nil {
		t.Fatal("migrateOwnerTraffic scan 应传播")
	}
}
