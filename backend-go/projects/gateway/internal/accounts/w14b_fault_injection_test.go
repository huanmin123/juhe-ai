package accounts

// w14b DB 故障注入补齐：注册一个全部返回错误的 database/sql driver，克隆
// Store（in-package 可写 s.db）后批量命中错误传播臂——单连接 SQLite 无法
// 在 Scan/Rows/Commit 之间注入故障，这里在连接层统一注入。
//
// w14b 不可达/残余登记（solo 波次结束后仍未覆盖的主要类别）：
//   - sql.Tx.Commit/Rollback 失败臂（batch.go:541、write.go:388、delete.go:115、
//     import.go executeImportPlan 收尾）：BeginTx 成功后 Commit 需在连接层
//     之外注入失败，driver 接口无法在事务成功开启后再单独令 Commit 失败。
//   - rows.Scan/rows.Err 中依赖"扫描半途类型失败后仍继续迭代"的臂：nil 行
//     注入已覆盖首行失败路径，半途失败臂需要按查询定制列型，未逐一构造。
//   - endpointModeProtocolFamily 的 default 错误臂（test_options_service.go
//     :1219）：入参来自固定模式集合，无非法值来源。
//   - import.go createImportGroup 的 duplicateImportGroupNameError 复用臂：
//     测试 sqlite schema 的 groups 表无 (system_account_id, name) 唯一索引，
//     无法在包内产生该约束冲突。
//   - 其余为深层校验链中位于早期守卫之后的 1-2 条语句小臂，已在
//     w13a/w13g/w14b 各波测试中按可触达性逐步收敛。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"testing"
	"time"
)

var w14bBoom = errors.New("w14b 注入故障")

type w14bBoomDriver struct{}

func (w14bBoomDriver) Open(string) (driver.Conn, error) { return w14bBoomConn{}, nil }

type w14bBoomConn struct{}

func (c w14bBoomConn) Prepare(string) (driver.Stmt, error) { return nil, w14bBoom }
func (c w14bBoomConn) Close() error                        { return nil }
func (c w14bBoomConn) Begin() (driver.Tx, error)           { return nil, w14bBoom }
func (c w14bBoomConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, w14bBoom
}
func (c w14bBoomConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, w14bBoom
}

type w14bBoomRows struct{}

func (w14bBoomRows) Columns() []string { return nil }
func (w14bBoomRows) Close() error      { return nil }
func (w14bBoomRows) Next([]driver.Value) error {
	return io.EOF
}

// w14bBoomStore 克隆 env.store 并把 db 换成失败 driver。
func w14bBoomStore(env *testEnv) *Store {
	clone := *env.store
	clone.db = sql.OpenDB(w14bBoomConnector{})
	return &clone
}

type w14bBoomConnector struct{}

func (w14bBoomConnector) Connect(context.Context) (driver.Conn, error) { return w14bBoomConn{}, nil }
func (w14bBoomConnector) Driver() driver.Driver                        { return w14bBoomDriver{} }

func TestW14BFaultInjectedStoreArms(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedAccount(t, "acc-w14b-fi", adminID, "w14b-fi", "active")
	_ = w14bBoomRows{}
	ctx := context.Background()
	store := w14bBoomStore(env)
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}

	// 列表与选项。
	if _, err := store.ListPage(ctx, scope, ListOptions{Page: 1, PageSize: 5}); err == nil {
		t.Fatal("ListPage 应传播错误")
	}
	if _, err := store.ListOptionSummaries(ctx, scope, ListOptions{}); err == nil {
		t.Fatal("ListOptionSummaries 应传播错误")
	}
	if err := store.hydrateTags(ctx, []ListItem{{ID: "acc-w14b-fi"}}, []string{"acc-w14b-fi"}); err == nil {
		t.Fatal("hydrateTags 应传播错误")
	}
	if err := store.hydrateLockStates(ctx, []ListItem{{ID: "acc-w14b-fi"}}, []string{"acc-w14b-fi"}); err == nil {
		t.Fatal("hydrateLockStates 应传播错误")
	}
	if err := store.hydrateOAuthUsageSnapshots(ctx, []ListItem{{ID: "acc-w14b-fi", ProviderCode: "gpt", Type: "oauth"}}); err == nil {
		t.Fatal("hydrateOAuthUsageSnapshots 应传播错误")
	}
	// 批量上下文与批量更新。
	if _, err := store.LoadBatchEditContext(ctx, []string{"a", "b"}, nil, scope); err == nil {
		t.Fatal("LoadBatchEditContext 应传播错误")
	}
	if _, err := store.BatchUpdate(ctx, BatchUpdateInput{Targets: []BatchUpdateTarget{
		{AccountID: "a", ConfigRevision: 1}, {AccountID: "b", ConfigRevision: 1},
	}}, scope); err == nil {
		t.Fatal("BatchUpdate 应传播错误")
	}
	// 批量加载臂（q 参数即失败连接）。
	if _, err := store.loadBatchSupportedModels(ctx, store.db, []string{"a"}); err == nil {
		t.Fatal("loadBatchSupportedModels 应传播错误")
	}
	if _, err := store.loadBatchModelMappings(ctx, store.db, []string{"a"}); err == nil {
		t.Fatal("loadBatchModelMappings 应传播错误")
	}
	if _, err := store.loadBatchTags(ctx, store.db, []string{"a"}); err == nil {
		t.Fatal("loadBatchTags 应传播错误")
	}
	// 写替换链。
	if err := store.replaceAccountSupportedModels(ctx, store.db, "a", "gpt", []string{"m"}, "2026-09-17T00:00:00.000Z"); err == nil {
		t.Fatal("replaceAccountSupportedModels 应传播错误")
	}
	if err := store.replaceAccountModelMappings(ctx, store.db, "a", "gpt", []ModelMapping{{SourceModel: "a", UpstreamModel: "b"}}, "2026-09-17T00:00:00.000Z"); err == nil {
		t.Fatal("replaceAccountModelMappings 应传播错误")
	}
	if _, err := store.replaceAccountTags(ctx, store.db, "a", adminID, []string{"t"}, "2026-09-17T00:00:00.000Z"); err == nil {
		t.Fatal("replaceAccountTags 应传播错误")
	}
	if err := store.replaceAccountNameSearchTerms(ctx, store.db, "a", adminID, "n", "2026-09-17T00:00:00.000Z"); err == nil {
		t.Fatal("replaceAccountNameSearchTerms 应传播错误")
	}
	if err := store.markBatchGroupStatsDirty(ctx, []string{"a"}, "w14b"); err == nil {
		t.Fatal("markBatchGroupStatsDirty 应传播错误")
	}
	// 导入链。
	if _, err := store.PreviewImport(ctx, w13aRawDoc([]any{}, []any{}), "", ImportOptions{}, scope); err == nil {
		t.Fatal("PreviewImport 应传播错误")
	}
	if _, err := store.findImportGroupByID(ctx, "grp-x", scope); err == nil {
		t.Fatal("findImportGroupByID 应传播错误")
	}
	if _, err := store.findImportProxyByID(ctx, "proxy-x"); err == nil {
		t.Fatal("findImportProxyByID 应传播错误")
	}
	if _, _, err := store.createImportGroup(ctx, importGroupCreatePlan{name: "w14b", providerCode: "gpt"}, adminID, "2026-09-17T00:00:00.000Z"); err == nil {
		t.Fatal("createImportGroup 应传播错误")
	}
	// 导出链。
	if _, err := store.ExportAccounts(ctx, ExportOptions{AccountIDs: []string{"a"}}, scope); err == nil {
		t.Fatal("ExportAccounts 应传播错误")
	}
	// 标签。
	if _, err := store.ListTags(ctx, scope); err == nil {
		t.Fatal("ListTags 应传播错误")
	}
	if _, err := store.DeleteTag(ctx, "tag-x", scope); err == nil {
		t.Fatal("DeleteTag 应传播错误")
	}
	// 手动测试选项链。
	if _, err := store.protocolProviderCodes(ctx, "openai", "v1"); err == nil {
		t.Fatal("protocolProviderCodes 应传播错误")
	}
	if _, err := store.listBuiltInTestCatalogOptions(ctx, []string{"gpt"}, ManualTestOptionsQuery{}); err == nil {
		t.Fatal("listBuiltInTestCatalogOptions 应传播错误")
	}
	if _, err := store.listCustomTestCatalogOptions(ctx, []string{"gpt"}, adminID, ManualTestOptionsQuery{}); err == nil {
		t.Fatal("listCustomTestCatalogOptions 应传播错误")
	}
	if _, err := store.collectTestCatalogCandidates(ctx, []string{"gpt"}, []string{"gpt"}, "m", ""); err == nil {
		t.Fatal("collectTestCatalogCandidates 应传播错误")
	}
	if _, err := store.findTestCatalogItem(ctx, "gpt", adminID, "gpt-4o-mini"); err == nil {
		t.Fatal("findTestCatalogItem 应传播错误")
	}
	if _, err := store.findManualTestContextRow(ctx, "acc-w14b-fi", &scope, true); err == nil {
		t.Fatal("findManualTestContextRow 应传播错误")
	}
	if _, err := store.loadTestAccountModelMappings(ctx, store.db, "acc-w14b-fi", ""); err == nil {
		t.Fatal("loadTestAccountModelMappings 应传播错误")
	}
	// 会话任务链。
	if _, err := store.listSessionTasks(ctx, store.db, "sess-x", &scope); err == nil {
		t.Fatal("listSessionTasks 应传播错误")
	}
	if _, err := store.GetTestSession(ctx, "sess-x", &scope); err == nil {
		t.Fatal("GetTestSession 应传播错误")
	}
	if _, err := store.CreateTestSession(ctx, scope); err == nil {
		t.Fatal("CreateTestSession 应传播错误")
	}
	// 余额。
	if _, err := store.loadBalanceSnapshotRecord(ctx, "acc-w14b-fi"); err == nil {
		t.Fatal("loadBalanceSnapshotRecord 应传播错误")
	}
	if _, err := store.FindBalanceDetails(ctx, "acc-w14b-fi", scope); err == nil {
		t.Fatal("FindBalanceDetails 应传播错误")
	}
	cleaner := NewStoreBalanceSnapshotCleaner(store)
	if err := cleaner.deleteSupersededSnapshot(ctx, BalanceSnapshotCleanupRequest{AccountID: "acc-w14b-fi"}); err == nil {
		t.Fatal("deleteSupersededSnapshot 应传播错误")
	}
	cleaner.Close()
	// 创建与删除（BeginTx 失败臂）。
	if _, err := store.Create(ctx, CreateInput{ProviderCode: "gpt"}, scope); err == nil {
		t.Fatal("Create 应传播错误")
	}
	if _, err := store.Delete(ctx, "acc-w14b-fi", scope); err == nil {
		t.Fatal("Delete 应传播错误")
	}
	// 时间计划下次检查（NextScheduleCheckAt 使用 store 时钟；无 DB）。
	if _, ok := NextScheduleCheckAt(nil, time.Now()); ok {
		t.Fatal("nil 计划应返回 false")
	}
}
