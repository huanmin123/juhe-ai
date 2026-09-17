package cleanuprepo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// w13e_sqlite_arms_test.go 覆盖 recordcleanup.go / usageshards.go /
// dataretention.go（SQLite 半区）的剩余错误臂与数据驱动分支：
//   - 列表/定位查询失败臂（openKitFailingSQLiteDB 子串注入）；
//   - 分片库 Open / BeginTx / Exec / changes / Commit 臂（w13e 装饰器）；
//   - rows.Scan / rows.Err 臂（w13e 脚本化行集）；
//   - pending 汇总 Deferred / Completed 分支与 writer 失败钩子。

func w13eShardLocation(root, fileKey string) ShardLocation {
	return ShardLocation{
		ShardKey:      "sk-" + fileKey,
		BucketDate:    "2026-01-05",
		BucketDateKey: "20260105",
		ShardID:       1,
		FilePath:      shardFilePathForTest(root, fileKey, 1),
	}
}

// w13eTrigger 在 SQLite 库上创建 RAISE(ABORT) 触发器。
func w13eTrigger(t *testing.T, db *sql.DB, triggerSQL string) {
	t.Helper()
	if _, err := db.Exec(triggerSQL); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
}

func w13eOpenShardFile(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open shard file: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	return db
}

// w13eFixture 包装 newKitSQLiteStageFixture：failOn 为空表示不注入，
// 此时 store 句柄直接使用 seed 句柄（空子串会匹配一切语句）。
func w13eFixture(t *testing.T, failOn string) *kitSQLiteStageFixture {
	t.Helper()
	f := newKitSQLiteStageFixture(t, failOn)
	if failOn == "" {
		f.dataset = f.seedDataset
		f.stats = f.seedStats
		f.catalog = f.seedCatalog
	}
	return f
}

// w13eKitPlainStore 用 fixture 的 seed 句柄（无注入）构建 store。
func w13eKitPlainStore(t *testing.T, f *kitSQLiteStageFixture) *RecordCleanupStore {
	t.Helper()
	store := &RecordCleanupStore{
		Dataset: f.seedDataset, Stats: f.seedStats, UsageCatalog: f.seedCatalog,
		Shards:   NewShardStore(f.root),
		Now:      kitNow,
		Timezone: func(context.Context) (*time.Location, error) { return kitZone(), nil },
	}
	t.Cleanup(func() { _ = store.Shards.Close() })
	return store
}

// TestW13eListTargetsArms：targets 列表查询失败臂。
func TestW13eListTargetsArms(t *testing.T) {
	ctx := context.Background()
	{
		f := newKitSQLiteStageFixture(t, "FROM api_key_record_cleanup_targets")
		store := kitStageStore(t, f)
		if _, err := store.CleanupPendingAPIKeyTargets(ctx, 5, &kitStatsWriter{}); err == nil ||
			!strings.Contains(err.Error(), "api_key_record_cleanup_targets") {
			t.Fatalf("api key targets 列表失败应透传: %v", err)
		}
	}
	{
		f := newKitSQLiteStageFixture(t, "FROM account_record_cleanup_targets")
		store := kitStageStore(t, f)
		if _, err := store.CleanupPendingAccountTargets(ctx, 5, &kitStatsWriter{}); err == nil ||
			!strings.Contains(err.Error(), "account_record_cleanup_targets") {
			t.Fatalf("account targets 列表失败应透传: %v", err)
		}
	}
	{
		// 直连 listAPIKeyTargets / listAccountTargets。
		f := newKitSQLiteStageFixture(t, "api_key_record_cleanup_targets")
		store := kitStageStore(t, f)
		if _, err := store.listAPIKeyTargets(ctx, 5); err == nil {
			t.Fatalf("listAPIKeyTargets 应报错")
		}
	}
	{
		f := newKitSQLiteStageFixture(t, "account_record_cleanup_targets")
		store := kitStageStore(t, f)
		if _, err := store.listAccountTargets(ctx, 5); err == nil {
			t.Fatalf("listAccountTargets 应报错")
		}
	}
}

// TestW13eLocationAndHasArms：分片定位查询失败与 hasUsageRecords 臂。
func TestW13eLocationAndHasArms(t *testing.T) {
	ctx := context.Background()
	{
		f := newKitSQLiteStageFixture(t, "usage_record_account_shards")
		store := kitStageStore(t, f)
		if _, _, _, err := store.selectAccountUsageRows(ctx, "acc-1", 5); err == nil {
			t.Fatalf("selectAccountUsageRows 定位失败应透传")
		}
		if _, err := store.hasAccountUsageRecords(ctx, []string{"acc-1"}); err == nil {
			t.Fatalf("hasAccountUsageRecords 定位失败应透传")
		}
	}
	{
		f := newKitSQLiteStageFixture(t, "usage_record_api_key_shards")
		store := kitStageStore(t, f)
		if _, err := store.hasAPIKeyUsageRecords(ctx, "key-1", "sys-1"); err == nil {
			t.Fatalf("hasAPIKeyUsageRecords 定位失败应透传")
		}
	}
}

// TestW13eSelectUsageRowsArms：分片行选择的 Open 失败、未覆盖行与截断分支。
func TestW13eSelectUsageRowsArms(t *testing.T) {
	ctx := context.Background()
	// Open 失败（注入 opener）。
	{
		f := w13eFixture(t, "")
		store := kitStageStore(t, f)
		store.Shards.SetOpener(func(string) (*sql.DB, error) {
			return nil, errors.New("w13e opener 注入失败")
		})
		window := ShardLocationWindow{Locations: []ShardLocation{w13eShardLocation(f.root, "20260105")}}
		if _, _, _, err := store.selectUsageRows(ctx, window, 5, "account_id = ?", "acc-1"); err == nil {
			t.Fatalf("分片打开失败应透传")
		}
	}
	// 游标之后仍有记录 → hasUncoveredRows。
	{
		f := w13eFixture(t, "")
		store := kitStageStore(t, f)
		f.addKitStageShard(t, "sk-1", "20260105", true)
		shardDB := w13eOpenShardFile(t, shardFilePathForTest(f.root, "20260105", 1))
		seedKitUsageRecord(t, shardDB, "rec-late", "sys-1", "key-1", "acc-1", "2026-01-06T00:00:00.000Z")
		rows, hasMore, hasUncovered, err := store.selectAPIKeyUsageRows(ctx, "key-1", "sys-1", 5)
		if err != nil || hasMore || !hasUncovered || len(rows) != 1 {
			t.Fatalf("应识别未覆盖行: rows=%d hasMore=%v hasUncovered=%v err=%v", len(rows), hasMore, hasUncovered, err)
		}
	}
	// 双分片各 2 行、limit=1 → rows 超过 queryLimit 触发截断。
	{
		f := w13eFixture(t, "")
		store := kitStageStore(t, f)
		f.addKitStageShardWithID(t, "sk-20260105", "20260105", true, "rec-20260105-a")
		f.addKitStageShardWithID(t, "sk-20260106", "20260106", true, "rec-20260106-a")
		for _, fileKey := range []string{"20260105", "20260106"} {
			shardDB := w13eOpenShardFile(t, shardFilePathForTest(f.root, fileKey, 1))
			mustExecKit(t, shardDB, `INSERT INTO usage_records (
        id, system_account_id, api_key_id, account_id, provider_code, model, success,
        input_tokens, output_tokens, cost_usd, created_at)
        VALUES ('rec-20260105-b', 'sys-1', 'key-1', 'acc-1', 'openai', 'gpt-kit', 1, 10, 5, 0.01, '2026-01-05T02:00:00.000Z')`)
		}
		window := ShardLocationWindow{Locations: []ShardLocation{
			w13eShardLocation(f.root, "20260105"),
			w13eShardLocation(f.root, "20260106"),
		}}
		rows, hasMore, _, err := store.selectUsageRows(ctx, window, 1, "api_key_id = ? AND system_account_id = ?", "key-1", "sys-1")
		if err != nil {
			t.Fatalf("selectUsageRows: %v", err)
		}
		if len(rows) != 2 || !hasMore {
			t.Fatalf("limit=1 应截断到 queryLimit=2: rows=%d hasMore=%v", len(rows), hasMore)
		}
	}
}

// TestW13eDeleteUsageRowsArms：deleteUsageRows 的 Open / Begin / Exec /
// changes / Commit 错误臂。
func TestW13eDeleteUsageRowsArms(t *testing.T) {
	ctx := context.Background()
	buildRows := func(t *testing.T, f *kitSQLiteStageFixture) []shardUsageRow {
		f.addKitStageShard(t, "sk-20260105", "20260105", true)
		store := kitStageStore(t, f)
		rows, _, _, err := store.selectAPIKeyUsageRows(ctx, "key-1", "sys-1", 5)
		if err != nil || len(rows) != 1 {
			t.Fatalf("seeding rows: %d %v", len(rows), err)
		}
		return rows
	}
	// Open 失败。
	{
		f := w13eFixture(t, "")
		rows := buildRows(t, f)
		store := kitStageStore(t, f)
		store.Shards.SetOpener(func(string) (*sql.DB, error) {
			return nil, errors.New("w13e opener 注入失败")
		})
		if _, err := store.deleteUsageRows(ctx, rows, "api_key_id = ? AND system_account_id = ?", "key-1", "sys-1"); err == nil {
			t.Fatalf("deleteUsageRows Open 失败应透传")
		}
	}
	// Begin 失败。
	{
		f := w13eFixture(t, "")
		rows := buildRows(t, f)
		store := kitStageStore(t, f)
		store.Shards.SetOpener(func(filePath string) (*sql.DB, error) {
			return w13eOpenDecoratedRawSQLite(t, filePath, w13eSQLiteOptions{failBegin: true}), nil
		})
		if _, err := store.deleteUsageRows(ctx, rows, "api_key_id = ? AND system_account_id = ?", "key-1", "sys-1"); err == nil {
			t.Fatalf("deleteUsageRows Begin 失败应透传")
		}
	}
	// Exec 失败（触发器）。
	{
		f := w13eFixture(t, "")
		rows := buildRows(t, f)
		store := kitStageStore(t, f)
		shardPath := shardFilePathForTest(f.root, "20260105", 1)
		w13eTrigger(t, w13eOpenShardFile(t, shardPath), `CREATE TRIGGER w13e_del_block BEFORE DELETE ON usage_records
      BEGIN SELECT RAISE(ABORT,'w13e del boom'); END`)
		if _, err := store.deleteUsageRows(ctx, rows, "api_key_id = ? AND system_account_id = ?", "key-1", "sys-1"); err == nil ||
			!strings.Contains(err.Error(), "w13e del boom") {
			t.Fatalf("deleteUsageRows Exec 失败应透传: %v", err)
		}
	}
	// changes 失败。
	{
		f := w13eFixture(t, "")
		rows := buildRows(t, f)
		store := kitStageStore(t, f)
		store.Shards.SetOpener(func(filePath string) (*sql.DB, error) {
			return w13eOpenDecoratedRawSQLite(t, filePath, w13eSQLiteOptions{rowsAffectedFailOn: "DELETE FROM usage_records"}), nil
		})
		if _, err := store.deleteUsageRows(ctx, rows, "api_key_id = ? AND system_account_id = ?", "key-1", "sys-1"); err == nil {
			t.Fatalf("deleteUsageRows changes 失败应透传")
		}
	}
	// Commit 失败。
	{
		f := w13eFixture(t, "")
		rows := buildRows(t, f)
		store := kitStageStore(t, f)
		store.Shards.SetOpener(func(filePath string) (*sql.DB, error) {
			return w13eOpenDecoratedRawSQLite(t, filePath, w13eSQLiteOptions{failCommit: true}), nil
		})
		if _, err := store.deleteUsageRows(ctx, rows, "api_key_id = ? AND system_account_id = ?", "key-1", "sys-1"); err == nil {
			t.Fatalf("deleteUsageRows Commit 失败应透传")
		}
	}
}

// w13eHookWriter 带钩子的 statsWriter fake：可统计调用次数并在调用中改变
// 外部状态（如关闭 catalog 句柄）。
type w13eHookWriter struct {
	kitStatsWriter
	apiKeyCalls  int
	accountCalls int
	onAPIKey     func(call int)
	onAccount    func(call int)
}

func (f *w13eHookWriter) CleanupDeletedApiKeyRecordStats(ctx context.Context, input retention.DeletedApiKeyRecordStatsCleanupInput) error {
	f.apiKeyCalls++
	if f.onAPIKey != nil {
		f.onAPIKey(f.apiKeyCalls)
	}
	return f.kitStatsWriter.CleanupDeletedApiKeyRecordStats(ctx, input)
}

func (f *w13eHookWriter) CleanupDeletedAccountRecordStats(ctx context.Context, input retention.DeletedAccountRecordStatsCleanupInput) error {
	f.accountCalls++
	if f.onAccount != nil {
		f.onAccount(f.accountCalls)
	}
	return f.kitStatsWriter.CleanupDeletedAccountRecordStats(ctx, input)
}

// TestW13eCleanupAPIKeyChainArms：api key 关联清理链的 writer / 存在性臂。
func TestW13eCleanupAPIKeyChainArms(t *testing.T) {
	ctx := context.Background()
	// 首次 writer 成功、第二次（ShardDeleted=true）失败 → L567-576 臂。
	{
		f := w13eFixture(t, "")
		f.addKitStageShard(t, "sk-1", "20260105", true)
		store := kitStageStore(t, f)
		writer := &w13eHookWriter{}
		writer.onAPIKey = func(call int) {
			if call >= 2 {
				writer.apiKeyErr = errors.New("w13e writer 第二次调用失败")
			}
		}
		if _, err := store.CleanupAPIKeyRelatedSQLite(ctx, "key-1", "sys-1", writer); err == nil ||
			!strings.Contains(err.Error(), "第二次调用失败") {
			t.Fatalf("第二次 writer 失败应透传: %v", err)
		}
	}
	// writer 首次即失败 → L549-558 臂。
	{
		f := w13eFixture(t, "")
		f.addKitStageShard(t, "sk-1", "20260105", true)
		store := kitStageStore(t, f)
		writer := &kitStatsWriter{apiKeyErr: errors.New("w13e writer 失败")}
		if _, err := store.CleanupAPIKeyRelatedSQLite(ctx, "key-1", "sys-1", writer); err == nil {
			t.Fatalf("writer 失败应透传")
		}
	}
	// writer 在第二次调用时关闭 catalog → hasAPIKeyUsageRecords 失败。
	{
		f := w13eFixture(t, "")
		f.addKitStageShard(t, "sk-1", "20260105", true)
		store := kitStageStore(t, f)
		catalog := store.UsageCatalog
		writer := &w13eHookWriter{}
		writer.onAPIKey = func(call int) {
			if call >= 2 {
				_ = catalog.Close()
			}
		}
		if _, err := store.CleanupAPIKeyRelatedSQLite(ctx, "key-1", "sys-1", writer); err == nil {
			t.Fatalf("catalog 关闭后 hasUsageRecords 应报错")
		}
	}
	// 无分片 + nil writer → stats writer 未初始化臂。
	{
		f := w13eFixture(t, "")
		store := kitStageStore(t, f)
		if _, err := store.CleanupAPIKeyRelatedSQLite(ctx, "key-1", "sys-1", nil); err == nil ||
			!strings.Contains(err.Error(), "未初始化") {
			t.Fatalf("nil writer 应报未初始化: %v", err)
		}
	}
	// 分片无游标 → blocked 结果（Deferred 路径）。
	{
		f := w13eFixture(t, "")
		f.addKitStageShard(t, "sk-1", "20260105", false)
		store := kitStageStore(t, f)
		result, err := store.CleanupAPIKeyRelatedSQLite(ctx, "key-1", "sys-1", &kitStatsWriter{})
		if err != nil || !result.HasMore || result.BlockedReason == "" {
			t.Fatalf("无游标应阻塞: %+v %v", result, err)
		}
	}
}

// TestW13eCleanupAccountChainArms：account 关联清理链的对应臂。
func TestW13eCleanupAccountChainArms(t *testing.T) {
	ctx := context.Background()
	target := retention.ExpiredDeletedAccountTarget{AccountID: "acc-1", SystemAccountID: "sys-1"}
	// 定位失败（usage_record_account_shards）。
	{
		f := newKitSQLiteStageFixture(t, "usage_record_account_shards")
		f.addKitStageShard(t, "sk-1", "20260105", true)
		store := kitStageStore(t, f)
		if _, err := store.CleanupAccountRelatedSQLite(ctx, target, &kitStatsWriter{}); err == nil {
			t.Fatalf("account 定位失败应透传")
		}
	}
	// 分片删除触发器失败。
	{
		f := w13eFixture(t, "")
		f.addKitStageShard(t, "sk-1", "20260105", true)
		store := kitStageStore(t, f)
		w13eTrigger(t, w13eOpenShardFile(t, shardFilePathForTest(f.root, "20260105", 1)), `CREATE TRIGGER w13e_acc_del BEFORE DELETE ON usage_records
      BEGIN SELECT RAISE(ABORT,'w13e acc del boom'); END`)
		if _, err := store.CleanupAccountRelatedSQLite(ctx, target, &kitStatsWriter{}); err == nil ||
			!strings.Contains(err.Error(), "w13e acc del boom") {
			t.Fatalf("account 分片删除失败应透传: %v", err)
		}
	}
	// writer 失败（有行）。
	{
		f := w13eFixture(t, "")
		f.addKitStageShard(t, "sk-1", "20260105", true)
		store := kitStageStore(t, f)
		writer := &kitStatsWriter{accountErr: errors.New("w13e account writer 失败")}
		if _, err := store.CleanupAccountRelatedSQLite(ctx, target, writer); err == nil {
			t.Fatalf("account writer 失败应透传")
		}
	}
	// writer 第二次调用关闭 catalog → hasAccountUsageRecords 失败。
	{
		f := w13eFixture(t, "")
		f.addKitStageShard(t, "sk-1", "20260105", true)
		store := kitStageStore(t, f)
		catalog := store.UsageCatalog
		writer := &w13eHookWriter{}
		writer.onAccount = func(call int) {
			if call >= 2 {
				_ = catalog.Close()
			}
		}
		if _, err := store.CleanupAccountRelatedSQLite(ctx, target, writer); err == nil {
			t.Fatalf("catalog 关闭后 hasAccountUsageRecords 应报错")
		}
	}
	// 无分片 + nil writer → 未初始化臂。
	{
		f := w13eFixture(t, "")
		store := kitStageStore(t, f)
		if _, err := store.CleanupAccountRelatedSQLite(ctx, target, nil); err == nil ||
			!strings.Contains(err.Error(), "未初始化") {
			t.Fatalf("nil writer 应报未初始化: %v", err)
		}
	}
	// 无分片 + writer 返回错误（Rows=nil 收尾调用）。
	{
		f := w13eFixture(t, "")
		store := kitStageStore(t, f)
		writer := &kitStatsWriter{accountErr: errors.New("w13e final account writer 失败")}
		if _, err := store.CleanupAccountRelatedSQLite(ctx, target, writer); err == nil ||
			!strings.Contains(err.Error(), "final account writer") {
			t.Fatalf("收尾 writer 失败应透传: %v", err)
		}
	}
	// 无游标分片 → blocked（Deferred）。
	{
		f := w13eFixture(t, "")
		f.addKitStageShard(t, "sk-1", "20260105", false)
		store := kitStageStore(t, f)
		result, err := store.CleanupAccountRelatedSQLite(ctx, target, &kitStatsWriter{})
		if err != nil || !result.HasMore || result.BlockedReason == "" {
			t.Fatalf("无游标应阻塞: %+v %v", result, err)
		}
	}
}

// TestW13ePendingSummaryBranches：pending 汇总的 Failed/Deferred/Completed 分支。
func TestW13ePendingSummaryBranches(t *testing.T) {
	ctx := context.Background()
	// mark 目标失败（清理亦失败）→ markErr 透传。
	{
		f := newKitSQLiteStageFixture(t, "UPDATE api_key_record_cleanup_targets")
		store := kitStageStore(t, f)
		f.addKitStageShard(t, "sk-1", "20260105", false) // 无游标 → 清理被阻塞 → mark
		mustExecKit(t, f.seedDataset, `INSERT INTO api_key_record_cleanup_targets
      (api_key_id, system_account_id, created_at, updated_at) VALUES ('key-1','sys-1','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`)
		if _, err := store.CleanupPendingAPIKeyTargets(ctx, 5, &kitStatsWriter{}); err == nil {
			t.Fatalf("mark 失败应透传")
		}
	}
	// Completed 分支。
	{
		f := w13eFixture(t, "")
		store := kitStageStore(t, f)
		f.addKitStageShard(t, "sk-1", "20260105", true)
		mustExecKit(t, f.seedDataset, `INSERT INTO api_key_record_cleanup_targets
      (api_key_id, system_account_id, created_at, updated_at) VALUES ('key-1','sys-1','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`)
		summary, err := store.CleanupPendingAPIKeyTargets(ctx, 5, &kitStatsWriter{})
		if err != nil || summary.Completed != 1 || summary.Deferred != 0 || summary.Failed != 0 {
			t.Fatalf("应完成一个目标: %+v %v", summary, err)
		}
	}
	// mark 目标失败（account）。
	{
		f := newKitSQLiteStageFixture(t, "UPDATE account_record_cleanup_targets")
		store := kitStageStore(t, f)
		f.addKitStageShard(t, "sk-1", "20260105", false)
		mustExecKit(t, f.seedDataset, `INSERT INTO account_record_cleanup_targets
      (account_id, system_account_id, created_at, updated_at) VALUES ('acc-1','sys-1','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`)
		if _, err := store.CleanupPendingAccountTargets(ctx, 5, &kitStatsWriter{}); err == nil {
			t.Fatalf("account mark 失败应透传")
		}
	}
	// account Deferred 分支。
	{
		f := w13eFixture(t, "")
		store := kitStageStore(t, f)
		f.addKitStageShard(t, "sk-1", "20260105", false)
		mustExecKit(t, f.seedDataset, `INSERT INTO account_record_cleanup_targets
      (account_id, system_account_id, created_at, updated_at) VALUES ('acc-1','sys-1','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`)
		summary, err := store.CleanupPendingAccountTargets(ctx, 5, &kitStatsWriter{})
		if err != nil || summary.Deferred != 1 {
			t.Fatalf("account 应 Deferred: %+v %v", summary, err)
		}
	}
}

// TestW13eScopeCatalogArms：listScopeEntries / cleanupScopeShardCatalog 错误与
// 跳过分支（脚本化行集）。
func TestW13eScopeCatalogArms(t *testing.T) {
	ctx := context.Background()
	baseEntry := func(usageID, shardKey, systemAccountID, apiKeyID, accountID string) scopeEntry {
		return scopeEntry{UsageID: usageID, ShardKey: shardKey, SystemAccountID: systemAccountID, APIKeyID: apiKeyID, AccountID: accountID}
	}
	// listScopeEntries 的 scan 失败与迭代错误（脚本化行集）。
	for _, tc := range []struct {
		name   string
		script w13eRowsScript
	}{
		{"scan 失败", w13eRowsScript{match: "FROM usage_record_shard_entries",
			columns: []string{"usage_id", "shard_key", "system_account_id", "api_key_id", "account_id"},
			values:  [][]driver.Value{{struct{}{}, "sk", "sys", "key", "acc"}}, errAfterRows: -1}},
		{"迭代错误", w13eRowsScript{match: "FROM usage_record_shard_entries",
			columns: []string{"usage_id", "shard_key", "system_account_id", "api_key_id", "account_id"},
			values:  [][]driver.Value{{"u-1", "sk", "sys", "key", "acc"}}, errAfterRows: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := w13eFixture(t, "")
			catalog := w13eOpenDecoratedSQLite(t, filepath.Join(f.dir, "catalog.sqlite3"), w13eSQLiteOptions{rowsScripts: []w13eRowsScript{tc.script}})
			_, err := listScopeEntries(ctx, catalog, []string{"u-1"})
			if err == nil {
				t.Fatalf("脚本注入应产生错误")
			}
		})
	}
	// 空 parts 跳过分支已随 w13e 生产守卫删除；此处验证正常收缩路径 +
	// api key 删除失败臂。
	{
		f := w13eFixture(t, "")
		f.addKitStageShard(t, "sk-del", "20260105", true)
		failing := w13eOpenDecoratedSQLite(t, f.dir+"/catalog.sqlite3", w13eSQLiteOptions{failOn: "DELETE FROM usage_record_api_key_shards"})
		scopes := []scopeEntry{baseEntry("u-1", "sk-del", "sys-1", "key-1", "acc-1")}
		if err := cleanupScopeShardCatalog(ctx, failing, scopes); err == nil {
			t.Fatalf("api key scope 删除失败应透传")
		}
	}
	// account scope 删除失败臂。
	{
		f := w13eFixture(t, "")
		f.addKitStageShard(t, "sk-del", "20260105", true)
		failing := w13eOpenDecoratedSQLite(t, f.dir+"/catalog.sqlite3", w13eSQLiteOptions{failOn: "DELETE FROM usage_record_account_shards"})
		scopes := []scopeEntry{baseEntry("u-1", "sk-del", "sys-1", "", "acc-1")}
		if err := cleanupScopeShardCatalog(ctx, failing, scopes); err == nil {
			t.Fatalf("account scope 删除失败应透传")
		}
	}
}

// TestW13eEmptyShardCleanupArms：CleanupEmptyShardFilesBefore 的剩余臂。
func TestW13eEmptyShardCleanupArms(t *testing.T) {
	ctx := context.Background()
	cutoff := "2026-09-10T00:00:00.000Z"
	// Scan 失败与迭代错误（脚本化行集）。
	for _, tc := range []struct {
		name   string
		script w13eRowsScript
	}{
		{"scan 失败", w13eRowsScript{match: "s.bucket_date <= ?", columns: []string{"shard_key", "bucket_date", "shard_id", "file_path"},
			values: [][]driver.Value{{"sk", "2026-01-05", struct{}{}, "p"}}, errAfterRows: -1}},
		{"迭代错误", w13eRowsScript{match: "s.bucket_date <= ?", columns: []string{"shard_key", "bucket_date", "shard_id", "file_path"},
			values: [][]driver.Value{{"sk", "2026-01-05", int64(1), "p"}}, errAfterRows: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := w13eFixture(t, "")
			catalog := w13eOpenDecoratedSQLite(t, f.dir+"/catalog.sqlite3", w13eSQLiteOptions{rowsScripts: []w13eRowsScript{tc.script}})
			shards := NewShardStore(f.root)
			t.Cleanup(func() { _ = shards.Close() })
			if _, err := shards.CleanupEmptyShardFilesBefore(ctx, catalog, cutoff, 5); err == nil {
				t.Fatalf("脚本注入应产生错误")
			}
		})
	}
	// hasMore：两个空分片、limit=1。
	{
		f := w13eFixture(t, "")
		for _, key := range []string{"sk-a", "sk-b"} {
			seedKitShard(t, f.seedCatalog, key, "2026-01-05", 1, f.root+"/empty-"+key+".sqlite3")
		}
		shards := NewShardStore(f.root)
		t.Cleanup(func() { _ = shards.Close() })
		result, err := shards.CleanupEmptyShardFilesBefore(ctx, f.catalog, cutoff, 1)
		if err != nil || !result.HasMore || result.UsageRecordShards != 1 {
			t.Fatalf("应 hasMore 并清理一个: %+v %v", result, err)
		}
	}
	// 无候选空分片 → 提前返回分支。
	{
		f := w13eFixture(t, "")
		shards := NewShardStore(f.root)
		t.Cleanup(func() { _ = shards.Close() })
		result, err := shards.CleanupEmptyShardFilesBefore(ctx, f.catalog, cutoff, 5)
		if err != nil || result.HasMore || result.UsageRecordShards != 0 {
			t.Fatalf("无候选应零值返回: %+v %v", result, err)
		}
	}
	// Close 失败（注入 failClose 的已缓存分片句柄；内存库无文件锁问题）。
	// 需要至少一个候选分片，让流程走到 s.Close()（候选为空时提前返回）。
	{
		f := w13eFixture(t, "")
		seedKitShard(t, f.seedCatalog, "sk-close", "2026-01-05", 1, f.root+"/close.sqlite3")
		shards := NewShardStore(f.root)
		t.Cleanup(func() { _ = shards.Close() })
		broken := w13eOpenDecoratedRawSQLite(t, ":memory:", w13eSQLiteOptions{failClose: true})
		if err := broken.Ping(); err != nil {
			t.Fatalf("ping broken shard: %v", err)
		}
		shards.databases["w13e-broken"] = broken
		if _, err := shards.CleanupEmptyShardFilesBefore(ctx, f.catalog, cutoff, 5); err == nil {
			t.Fatalf("Close 注入失败应透传")
		}
	}
	// 删除分片文件失败：file_path 指向非空目录（os.Remove 拒绝）。
	{
		f := w13eFixture(t, "")
		dirPath := filepath.Join(f.root, "w13e-dir")
		if err := os.MkdirAll(dirPath, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// 目录非空时 os.Remove 才会失败。
		if err := os.WriteFile(filepath.Join(dirPath, "keep.txt"), []byte("w13e"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		seedKitShard(t, f.seedCatalog, "sk-dir", "2026-01-05", 1, dirPath)
		shards := NewShardStore(f.root)
		t.Cleanup(func() { _ = shards.Close() })
		if _, err := shards.CleanupEmptyShardFilesBefore(ctx, f.catalog, cutoff, 5); err == nil {
			t.Fatalf("目录删除失败应透传")
		}
	}
	// 目录行删除失败臂。
	{
		f := w13eFixture(t, "")
		seedKitShard(t, f.seedCatalog, "sk-x", "2026-01-05", 1, f.root+"/x.sqlite3")
		catalog := w13eOpenDecoratedSQLite(t, f.dir+"/catalog.sqlite3", w13eSQLiteOptions{failOn: "DELETE FROM usage_record_shards"})
		shards := NewShardStore(f.root)
		t.Cleanup(func() { _ = shards.Close() })
		if _, err := shards.CleanupEmptyShardFilesBefore(ctx, catalog, cutoff, 5); err == nil {
			t.Fatalf("目录行删除失败应透传")
		}
	}
	// ShardStore.Close 汇聚错误 + 零值 map 初始化。
	{
		s := &ShardStore{Root: "w13e"}
		if err := s.Close(); err != nil {
			t.Fatalf("零值 Close 不应报错: %v", err)
		}
		if _, err := s.Open("w13e-nonexistent.sqlite3"); err != nil {
			t.Fatalf("lazy open 不应报错: %v", err)
		}
	}
}

// osMkdirAllForTest 已内联为 os.MkdirAll，占位删除。

// TestW13eUsageRecordsSQLiteArms：UsageRecordsStore SQLite 主链剩余臂。
func TestW13eUsageRecordsSQLiteArms(t *testing.T) {
	ctx := context.Background()
	// 无游标 + 存在更早记录 → BlockedReason。
	{
		f := w13eFixture(t, "")
		f.addKitStageShard(t, "sk-1", "20260105", false)
		store := &UsageRecordsStore{Catalog: f.catalog, Stats: f.stats, Shards: NewShardStore(f.root)}
		t.Cleanup(func() { _ = store.Shards.Close() })
		batch, err := store.CleanupProcessedBefore(ctx, "2026-09-10T00:00:00.000Z", 5)
		if err != nil || batch.BlockedReason == "" {
			t.Fatalf("无游标应阻塞: %+v %v", batch, err)
		}
	}
	// 无游标 + sqliteHasRecordsBefore 失败。
	{
		f := newKitSQLiteStageFixture(t, "SELECT ue.usage_id\n      FROM usage_record_shard_entries")
		f.addKitStageShard(t, "sk-1", "20260105", false)
		store := &UsageRecordsStore{Catalog: f.catalog, Stats: f.stats, Shards: NewShardStore(f.root)}
		t.Cleanup(func() { _ = store.Shards.Close() })
		if _, err := store.CleanupProcessedBefore(ctx, "2026-09-10T00:00:00.000Z", 5); err == nil {
			t.Fatalf("存在性查询失败应透传")
		}
	}
	// 无游标 + 无记录 → 空批次直通。
	{
		f := w13eFixture(t, "")
		store := &UsageRecordsStore{Catalog: f.catalog, Stats: f.stats, Shards: NewShardStore(f.root)}
		t.Cleanup(func() { _ = store.Shards.Close() })
		batch, err := store.CleanupProcessedBefore(ctx, "2026-09-10T00:00:00.000Z", 5)
		if err != nil || batch.BlockedReason != "" || batch.DeletedRows != 0 {
			t.Fatalf("无记录应直通: %+v %v", batch, err)
		}
	}
	// 有游标 + sqliteBlockedReasonForRows 覆盖缺失 → 阻塞 + hasMore 批次。
	{
		f := w13eFixture(t, "")
		f.addKitStageShard(t, "sk-a", "20260105", true)
		f.addKitStageShard(t, "sk-b", "20260106", false)
		store := &UsageRecordsStore{Catalog: f.catalog, Stats: f.stats, Shards: NewShardStore(f.root)}
		t.Cleanup(func() { _ = store.Shards.Close() })
		batch, err := store.CleanupProcessedBefore(ctx, "2026-09-10T00:00:00.000Z", 5)
		if err != nil || batch.BlockedReason == "" || batch.SafetyCursorCreatedAt == "" {
			t.Fatalf("部分游标覆盖应阻塞并携带游标: %+v %v", batch, err)
		}
	}
	// floor cursor：scan 失败、迭代错误与空游标行（脚本化行集）。
	for _, tc := range []struct {
		name    string
		script  w13eRowsScript
		wantErr bool
	}{
		{"scan 失败", w13eRowsScript{match: "cursor_created_at, cursor_id", columns: []string{"cursor_created_at", "cursor_id"},
			values: [][]driver.Value{{struct{}{}, "id"}}}, true},
		{"迭代错误", w13eRowsScript{match: "cursor_created_at, cursor_id", columns: []string{"cursor_created_at", "cursor_id"},
			values: [][]driver.Value{{"2026-01-05T03:00:00.000Z", "rec-0"}}, errAfterRows: 0}, true},
		{"空游标行", w13eRowsScript{match: "cursor_created_at, cursor_id", columns: []string{"cursor_created_at", "cursor_id"},
			values: [][]driver.Value{{"", ""}}, errAfterRows: -1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := w13eFixture(t, "")
			stats := w13eOpenDecoratedSQLite(t, f.dir+"/stats.sqlite3", w13eSQLiteOptions{rowsScripts: []w13eRowsScript{tc.script}})
			store := &UsageRecordsStore{Catalog: f.catalog, Stats: stats, Shards: NewShardStore(f.root)}
			t.Cleanup(func() { _ = store.Shards.Close() })
			cursor, err := store.sqliteFloorCursor(ctx)
			if tc.wantErr && err == nil {
				t.Fatalf("脚本注入应产生错误")
			}
			if !tc.wantErr && (err != nil || cursor != nil) {
				t.Fatalf("空游标行应返回 nil 游标: %+v %v", cursor, err)
			}
		})
	}
	// selectSQLiteCleanupRows：scan 失败 / 空 usage_id 跳过。
	{
		f := w13eFixture(t, "")
		f.addKitStageShard(t, "sk-1", "20260105", true)
		scripts := []w13eRowsScript{{
			match:   "ue.created_at < ?",
			columns: []string{"usage_id", "created_at", "shard_key", "bucket_date", "shard_id", "file_path"},
			values: [][]driver.Value{
				{"", "2026-01-05T01:00:00.000Z", "sk-1", "2026-01-05", int64(1), shardFilePathForTest(f.root, "20260105", 1)},
				{"rec-1", "2026-01-05T01:00:00.000Z", "sk-1", "2026-01-05", struct{}{}, shardFilePathForTest(f.root, "20260105", 1)},
			},
		}}
		catalog := w13eOpenDecoratedSQLite(t, f.dir+"/catalog.sqlite3", w13eSQLiteOptions{rowsScripts: scripts})
		store := &UsageRecordsStore{Catalog: catalog, Stats: f.stats, Shards: NewShardStore(f.root)}
		t.Cleanup(func() { _ = store.Shards.Close() })
		if _, err := store.CleanupProcessedBefore(ctx, "2026-09-10T00:00:00.000Z", 5); err == nil {
			t.Fatalf("scan 注入应产生错误")
		}
	}
	// deleteSQLiteShardRows：空 ID 跳过、Open/Begin/changes/Commit 失败。
	// 每个臂独立 fixture：正常路径会真实删除记录，避免相互影响。
	t.Run("deleteSQLiteShardRows 臂", func(t *testing.T) {
		seedStore := func(t *testing.T) (*UsageRecordsStore, []ShardCleanupRow) {
			t.Helper()
			f := w13eFixture(t, "")
			f.addKitStageShard(t, "sk-1", "20260105", true)
			store := &UsageRecordsStore{Catalog: f.catalog, Stats: f.stats, Shards: NewShardStore(f.root)}
			t.Cleanup(func() { _ = store.Shards.Close() })
			rows, err := store.selectSQLiteCleanupRows(ctx, cutoffKit,
				cleanupCursor{CreatedAt: cutoffKit, ID: "zz"}, 5)
			if err != nil || len(rows) != 1 {
				t.Fatalf("select rows: %d %v", len(rows), err)
			}
			return store, rows
		}
		// 正常删除。
		{
			store, rows := seedStore(t)
			if _, err := store.deleteSQLiteShardRows(ctx, rows); err != nil {
				t.Fatalf("正常删除不应报错: %v", err)
			}
		}
		// Open 失败。
		{
			store, rows := seedStore(t)
			store.Shards.SetOpener(func(string) (*sql.DB, error) { return nil, errors.New("w13e opener 失败") })
			if _, err := store.deleteSQLiteShardRows(ctx, rows); err == nil {
				t.Fatalf("Open 失败应透传")
			}
		}
		// Begin 失败。
		{
			store, rows := seedStore(t)
			store.Shards.SetOpener(func(filePath string) (*sql.DB, error) {
				return w13eOpenDecoratedRawSQLite(t, filePath, w13eSQLiteOptions{failBegin: true}), nil
			})
			if _, err := store.deleteSQLiteShardRows(ctx, rows); err == nil {
				t.Fatalf("Begin 失败应透传")
			}
		}
		// changes 失败。
		{
			store, rows := seedStore(t)
			store.Shards.SetOpener(func(filePath string) (*sql.DB, error) {
				return w13eOpenDecoratedRawSQLite(t, filePath, w13eSQLiteOptions{rowsAffectedFailOn: "DELETE FROM usage_records WHERE id IN"}), nil
			})
			if _, err := store.deleteSQLiteShardRows(ctx, rows); err == nil {
				t.Fatalf("changes 失败应透传")
			}
		}
		// Commit 失败。
		{
			store, rows := seedStore(t)
			store.Shards.SetOpener(func(filePath string) (*sql.DB, error) {
				return w13eOpenDecoratedRawSQLite(t, filePath, w13eSQLiteOptions{failCommit: true}), nil
			})
			if _, err := store.deleteSQLiteShardRows(ctx, rows); err == nil {
				t.Fatalf("Commit 失败应透传")
			}
		}
	})
	// 既有行查询 scan/rows.Err/空 ID（脚本化行集）。
	for _, tc := range []struct {
		name   string
		script w13eRowsScript
	}{
		{"scan 失败", w13eRowsScript{match: "SELECT id FROM usage_records WHERE id IN", columns: []string{"id"},
			values: [][]driver.Value{{struct{}{}}}, errAfterRows: -1}},
		{"迭代错误", w13eRowsScript{match: "SELECT id FROM usage_records WHERE id IN", columns: []string{"id"},
			values: [][]driver.Value{{"rec-1"}}, errAfterRows: 1}},
		{"空 ID", w13eRowsScript{match: "SELECT id FROM usage_records WHERE id IN", columns: []string{"id"},
			values: [][]driver.Value{{""}}, errAfterRows: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := w13eFixture(t, "")
			f.addKitStageShard(t, "sk-1", "20260105", true)
			shardOpts := w13eSQLiteOptions{rowsScripts: []w13eRowsScript{tc.script}}
			store := &UsageRecordsStore{Catalog: f.catalog, Stats: f.stats, Shards: NewShardStore(f.root)}
			store.Shards.SetOpener(func(filePath string) (*sql.DB, error) {
				return w13eOpenDecoratedRawSQLite(t, filePath, shardOpts), nil
			})
			t.Cleanup(func() { _ = store.Shards.Close() })
			rows := []ShardCleanupRow{{
				ID: "rec-1", CreatedAt: "2026-01-05T01:00:00.000Z", Location: w13eShardLocation(f.root, "20260105"),
			}}
			_, err := store.deleteSQLiteShardRows(ctx, rows)
			if tc.name == "空 ID" {
				if err != nil {
					t.Fatalf("空 ID 行应跳过不报错: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("脚本注入应产生错误")
			}
		})
	}
	// cursor 覆盖查询 scan/rows.Err（脚本化行集）。
	for _, tc := range []struct {
		name   string
		script w13eRowsScript
	}{
		{"scan 失败", w13eRowsScript{match: "GROUP BY scope_id", columns: []string{"scope_id"},
			values: [][]driver.Value{{struct{}{}}}, errAfterRows: -1}},
		{"迭代错误", w13eRowsScript{match: "GROUP BY scope_id", columns: []string{"scope_id"},
			values: [][]driver.Value{{"sk-1"}}, errAfterRows: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := w13eFixture(t, "")
			f.addKitStageShard(t, "sk-1", "20260105", true)
			stats := w13eOpenDecoratedSQLite(t, f.dir+"/stats.sqlite3", w13eSQLiteOptions{rowsScripts: []w13eRowsScript{tc.script}})
			store := &UsageRecordsStore{Catalog: f.catalog, Stats: stats, Shards: NewShardStore(f.root)}
			t.Cleanup(func() { _ = store.Shards.Close() })
			rows := []ShardCleanupRow{{
				ID: "rec-1", CreatedAt: "2026-01-05T01:00:00.000Z", Location: w13eShardLocation(f.root, "20260105"),
			}}
			if _, err := store.deleteSQLiteShardRows(ctx, rows); err == nil {
				t.Fatalf("脚本注入应产生错误")
			}
		})
	}
	// blocked reason 行过滤的空键 / 覆盖缺失（直调）。
	{
		f := w13eFixture(t, "")
		store := &UsageRecordsStore{Catalog: f.catalog, Stats: f.stats, Shards: NewShardStore(f.root)}
		t.Cleanup(func() { _ = store.Shards.Close() })
		reason, err := store.sqliteBlockedReasonForRows(ctx, []ShardCleanupRow{
			{ID: "a", Location: ShardLocation{ShardKey: "  "}},
			{ID: "b", Location: ShardLocation{ShardKey: ""}},
		})
		if err != nil || reason != "" {
			t.Fatalf("空键行不应阻塞: %q %v", reason, err)
		}
		reason, err = store.sqliteBlockedReasonForRows(ctx, nil)
		if err != nil || reason != "" {
			t.Fatalf("空行集不应阻塞: %q %v", reason, err)
		}
	}
}

// TestW13eNonBusinessDatasetArms：NonBusinessDatasetStore.CleanupBefore 剩余臂。
func TestW13eNonBusinessDatasetArms(t *testing.T) {
	ctx := context.Background()
	cutoff := "2026-09-10T00:00:00.000Z"
	newStore := func(f *kitSQLiteStageFixture) *NonBusinessDatasetStore {
		// dataset 半区规则涉及 public_api_logs；targets 表由 fixture 建。
		execKitSchema(t, f.seedDataset.DB, `CREATE TABLE IF NOT EXISTS public_api_logs (
      id TEXT PRIMARY KEY, created_at TEXT NOT NULL)`)
		shards := NewShardStore(f.root)
		t.Cleanup(func() { _ = shards.Close() })
		return &NonBusinessDatasetStore{
			Dataset: f.dataset, UsageCatalog: f.catalog, Stats: f.stats,
			Shards:       shards,
			UsageRecords: &UsageRecordsStore{Catalog: f.catalog, Stats: f.stats, Shards: shards},
			Timezone:     func(context.Context) (*time.Location, error) { return kitZone(), nil },
		}
	}
	// 非法 cutoff。
	{
		f := w13eFixture(t, "")
		if _, err := newStore(f).CleanupBefore(ctx, "not-a-time", 5); err == nil ||
			!strings.Contains(err.Error(), "RFC3339") {
			t.Fatalf("非法 cutoff 应报错: %v", err)
		}
	}
	// Timezone 失败。
	{
		f := w13eFixture(t, "")
		shards := NewShardStore(f.root)
		t.Cleanup(func() { _ = shards.Close() })
		store := &NonBusinessDatasetStore{
			Dataset: f.dataset, UsageCatalog: f.catalog, Stats: f.stats, Shards: shards,
			UsageRecords: &UsageRecordsStore{Catalog: f.catalog, Stats: f.stats, Shards: shards},
			Timezone:     func(context.Context) (*time.Location, error) { return nil, errors.New("w13e timezone 失败") },
		}
		if _, err := store.CleanupBefore(ctx, cutoff, 5); err == nil {
			t.Fatalf("Timezone 失败应透传")
		}
	}
	// usage records 清理失败。
	{
		f := newKitSQLiteStageFixture(t, "SELECT ue.usage_id")
		f.addKitStageShard(t, "sk-1", "20260105", false)
		if _, err := newStore(f).CleanupBefore(ctx, cutoff, 5); err == nil {
			t.Fatalf("usage records 清理失败应透传")
		}
	}
	// dataset 表删除失败（public_api_logs 触发器）。
	{
		f := w13eFixture(t, "")
		execKitSchema(t, f.seedDataset.DB, `CREATE TABLE IF NOT EXISTS public_api_logs (
      id TEXT PRIMARY KEY, created_at TEXT NOT NULL)`)
		// 触发器只在真实删行时触发：种一行早于 cutoff 的日志。
		mustExecKit(t, f.seedDataset, `INSERT INTO public_api_logs (id, created_at) VALUES ('log-1', '2026-01-01T00:00:00.000Z')`)
		w13eTrigger(t, w13eOpenShardFile(t, filepath.Join(f.dir, "dataset.sqlite3")), `CREATE TRIGGER w13e_pal_del BEFORE DELETE ON public_api_logs
      BEGIN SELECT RAISE(ABORT,'w13e pal boom'); END`)
		if _, err := newStore(f).CleanupBefore(ctx, cutoff, 5); err == nil {
			t.Fatalf("dataset 表删除失败应透传")
		}
	}
	// usage-catalog 表删除失败。
	{
		f := w13eFixture(t, "")
		failingCatalog := w13eOpenDecoratedSQLite(t, f.dir+"/catalog.sqlite3", w13eSQLiteOptions{failOn: "DELETE FROM usage_record_account_shards"})
		shards := NewShardStore(f.root)
		t.Cleanup(func() { _ = shards.Close() })
		store := &NonBusinessDatasetStore{
			Dataset: f.dataset, UsageCatalog: failingCatalog, Stats: f.stats, Shards: shards,
			UsageRecords: &UsageRecordsStore{Catalog: f.catalog, Stats: f.stats, Shards: shards},
			Timezone:     func(context.Context) (*time.Location, error) { return kitZone(), nil },
		}
		// usage records 已被 cursor 覆盖并清空 → 走 catalog 半区。
		if _, err := store.CleanupBefore(ctx, cutoff, 5); err == nil {
			t.Fatalf("usage-catalog 表删除失败应透传")
		}
	}
	// 空分片文件清理失败（bucket_date 查询失败）与文件删除计数。
	{
		f := w13eFixture(t, "")
		// 手工种一个分片并立即关闭 seed 句柄（否则 Windows 下句柄锁住文件，
		// 空分片清理无法删除文件）。
		path := shardFilePathForTest(f.root, "20260105", 1)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		seedDB := createKitUsageShardDB(t, path)
		seedKitUsageRecord(t, seedDB, "rec-1", "sys-1", "key-1", "acc-1", "2026-01-05T01:00:00.000Z")
		if err := seedDB.Close(); err != nil {
			t.Fatalf("close seed shard: %v", err)
		}
		seedKitShard(t, f.seedCatalog, "sk-empty", "2026-01-05", 1, path)
		// 清掉目录条目使分片变空。
		mustExecKit(t, f.seedCatalog, `DELETE FROM usage_record_shard_entries`)
		mustExecKit(t, f.seedCatalog, `DELETE FROM usage_record_api_key_shards`)
		mustExecKit(t, f.seedCatalog, `DELETE FROM usage_record_account_shards`)
		counts, err := newStore(f).CleanupBefore(ctx, cutoff, 5)
		if err != nil {
			t.Fatalf("空分片清理不应报错: %v", err)
		}
		if counts.FileDeletes["usage_shard_files"] < 1 {
			t.Fatalf("应记录文件删除: %+v", counts)
		}
	}
	// 空分片清理失败臂（bucket_date 查询注入）。
	{
		f := w13eFixture(t, "")
		failingCatalog := w13eOpenDecoratedSQLite(t, f.dir+"/catalog.sqlite3", w13eSQLiteOptions{failOn: "s.bucket_date <= ?"})
		shards := NewShardStore(f.root)
		t.Cleanup(func() { _ = shards.Close() })
		store := &NonBusinessDatasetStore{
			Dataset: f.dataset, UsageCatalog: f.catalog, Stats: f.stats, Shards: shards,
			UsageRecords: &UsageRecordsStore{Catalog: failingCatalog, Stats: f.stats, Shards: shards},
			Timezone:     func(context.Context) (*time.Location, error) { return kitZone(), nil },
		}
		if _, err := store.CleanupBefore(ctx, cutoff, 5); err == nil {
			t.Fatalf("空分片查询失败应透传")
		}
	}
	// Shards == nil → 跳过分片清理分支。
	{
		f := w13eFixture(t, "")
		execKitSchema(t, f.seedDataset.DB, `CREATE TABLE IF NOT EXISTS public_api_logs (
      id TEXT PRIMARY KEY, created_at TEXT NOT NULL)`)
		store := &NonBusinessDatasetStore{
			Dataset: f.dataset, UsageCatalog: f.catalog, Stats: f.stats, Shards: nil,
			UsageRecords: &UsageRecordsStore{Catalog: f.catalog, Stats: f.stats, Shards: NewShardStore(f.root)},
			Timezone:     func(context.Context) (*time.Location, error) { return kitZone(), nil },
		}
		if _, err := store.CleanupBefore(ctx, cutoff, 5); err != nil {
			t.Fatalf("nil Shards 应跳过分片清理: %v", err)
		}
	}
}

// TestW13eStatsRetentionArms：StatsRetentionStore 的错误臂与 HasMore 分支。
func TestW13eStatsRetentionArms(t *testing.T) {
	ctx := context.Background()
	// CleanupUsageStatsRetention 中途失败（触发器）。
	{
		f := w13eFixture(t, "")
		w13eTrigger(t, w13eOpenShardFile(t, f.dir+"/stats.sqlite3"), `CREATE TRIGGER w13e_stats_del BEFORE DELETE ON usage_stats_minute
      BEGIN SELECT RAISE(ABORT,'w13e stats boom'); END`)
		// 触发器只在真实删行时触发：种一行早于 cutoff 的分钟统计。
		mustExecKit(t, f.seedStats, `INSERT INTO usage_stats_minute (system_account_id, scope_type, scope_id, stat_minute)
      VALUES ('sys-1','system_account','acc-1','2026010100')`)
		store := &StatsRetentionStore{DB: f.stats}
		if _, err := store.CleanupUsageStatsRetention(ctx, retention.UsageStatsRetentionInput{
			AccountQualityMinuteCutoffMinute: "2026091000",
			MinuteCutoffMinute:               "2026091000", HourlyCutoffHour: "2026091000", DailyCutoffDate: "2026-09-10",
			WeeklyCutoffWeek: "2026-W37", MonthlyCutoffMonth: "2026-09",
			RankSnapshotCutoffIso: cutoffKit, WindowCutoffDate: "2026-09-10", WindowCutoffIso: cutoffKit,
		}); err == nil || !strings.Contains(err.Error(), "w13e stats boom") {
			t.Fatalf("usage stats 清理失败应透传: %v", err)
		}
	}
	// CleanupSystemMetricsRetention 失败。
	{
		f := newKitSQLiteStageFixture(t, "system_metrics_samples")
		store := &StatsRetentionStore{DB: f.stats}
		if _, err := store.CleanupSystemMetricsRetention(ctx, retention.SystemMetricsRetentionInput{
			SamplesCutoffIso: cutoffKit, HourlyCutoffHour: "2026091000", TrendWindowCutoffDate: "2026-09-10",
		}); err == nil {
			t.Fatalf("system metrics 清理失败应透传")
		}
	}
	// CleanupNonBusinessStatsData 失败与 HasMore。
	{
		f := w13eFixture(t, "")
		// 补齐 stats 半区全部规则表（缺表会让流程在 HasMore 之前中断）。
		for _, rule := range nonBusinessStatsCleanupTables {
			createKitMinimalTable(t, f.seedStats.DB, rule.TableName, rule.TimeColumnName)
		}
		mustExecKit(t, f.seedStats, `INSERT INTO client_ip_stats_daily (stat_date) VALUES ('2026-01-01'),('2026-01-02')`)
		store := &StatsRetentionStore{DB: f.stats}
		counts, err := store.CleanupNonBusinessStatsData(ctx, cutoffKit, 1, kitZone())
		if err != nil || !counts.HasMore {
			t.Fatalf("达到 batch 上限应 HasMore: %+v %v", counts, err)
		}
	}
	{
		f := newKitSQLiteStageFixture(t, "account_quality_minute_stats")
		store := &StatsRetentionStore{DB: f.stats}
		if _, err := store.CleanupNonBusinessStatsData(ctx, cutoffKit, 5, kitZone()); err == nil {
			t.Fatalf("非业务 stats 清理失败应透传")
		}
	}
	{
		f := w13eFixture(t, "")
		store := &StatsRetentionStore{DB: f.stats}
		if _, err := store.CleanupNonBusinessStatsData(ctx, "bad-time", 5, kitZone()); err == nil {
			t.Fatalf("非法截止时间应报错")
		}
	}
	// PG 模式 schema 前缀 + deleteRowsBefore 错误。
	{
		rec := newPGRecorder()
		failing := w13eOpenDecoratedPG(rec, w13ePGOptions{failOn: []string{"DELETE FROM juhe_stats"}})
		store := &StatsRetentionStore{DB: failing}
		if _, err := store.CleanupUsageStatsRetention(ctx, retention.UsageStatsRetentionInput{MinuteCutoffMinute: "x"}); err == nil {
			t.Fatalf("PG 清理失败应透传")
		}
	}
	// AfterDelete checkpoint 挂钩（成功与失败均不外泄）。
	{
		store := &StatsRetentionStore{}
		store.AfterDelete(ctx) // nil checkpoint 安全
		called := false
		store = &StatsRetentionStore{Checkpoint: func(context.Context) error { called = true; return errors.New("w13e checkpoint 失败") }}
		store.AfterDelete(ctx)
		if !called {
			t.Fatalf("checkpoint 应被调用")
		}
	}
}

const cutoffKit = "2026-09-10T00:00:00.000Z"
