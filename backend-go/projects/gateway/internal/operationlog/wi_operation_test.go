package operationlog

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// wiOpenSQLiteStore 构造已打开 schema 的 SQLite operation log store（组B：
// SQLite-only 生产臂测试与方言 sqlite 分支继续使用，随生产臂删除批次退役）。
func wiOpenSQLiteStore(t *testing.T, root string) Store {
	t.Helper()
	business := root + "/business.sqlite3"
	createBusinessSettings(t, business, "365")
	store, err := OpenStore(Config{Enabled: true, InstanceID: "wi-owner", Mode: ModeSQLite, DatabasePath: root + "/operation.sqlite3", BusinessSettingsPath: business})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// wiOpenPostgresStore 构造已装配的 PG operation log store（组A 测试入口：
// pgtestkit 独立库 + 生产 OpenStore ModePostgres，业务夹具与 SQLite 版同构）。
func wiOpenPostgresStore(t *testing.T) Store {
	t.Helper()
	store, _ := openPostgresStore(t)
	return store
}

func TestWILeaseKeeperLifecycle(t *testing.T) {
	store := wiOpenPostgresStore(t)
	ctx := context.Background()

	keeper, ok, err := StartLeaseKeeper(ctx, store, "wi-keeper", 3*time.Second, slog.Default())
	if err != nil || !ok {
		t.Fatalf("启动 keeper=%v err=%v", ok, err)
	}
	if got := keeper.TTL(); got != 3*time.Second {
		t.Fatalf("TTL=%v", got)
	}
	if keeper.Lease().OwnerID != "wi-keeper" {
		t.Fatalf("lease=%+v", keeper.Lease())
	}
	time.Sleep(300 * time.Millisecond)
	if err := keeper.LostError(); err != nil {
		t.Fatalf("正常续租不得丢失: %v", err)
	}
	// 优雅关闭释放租约，继任者立即接管。
	keeper.Close()
	if _, ok, err := store.AcquireOwnerLease(ctx, "successor", time.Minute); err != nil || !ok {
		t.Fatalf("继任接管=%v err=%v", ok, err)
	}
	keeper.Close() // 幂等。
	if err := keeper.LostError(); err != nil {
		t.Fatalf("正常关闭不得有 LostError: %v", err)
	}
	// 被持有时拒绝第二个 keeper。
	if keeper, ok, err := StartLeaseKeeper(ctx, store, "second", time.Minute, nil); err != nil || ok || keeper != nil {
		t.Fatalf("被持有时应拒绝: ok=%v keeper=%v err=%v", ok, keeper, err)
	}
}

func TestWILeaseKeeperTerminalWhenUsurped(t *testing.T) {
	store := wiOpenPostgresStore(t)
	ctx := context.Background()
	keeper, ok, err := StartLeaseKeeper(ctx, store, "wi-doomed", 3*time.Second, slog.Default())
	if err != nil || !ok {
		t.Fatalf("启动 keeper=%v err=%v", ok, err)
	}
	// 手工过期租约行，随后由 raider 抢占（fence 递增）。
	implementation := store.(*sqlStore)
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	if _, err := implementation.db.Exec(`UPDATE juhe_dataset.operation_log_owner_leases SET lease_until=$1 WHERE lease_key='f4-operation-log-persistence' AND owner_id='wi-doomed'`, past); err != nil {
		t.Fatalf("过期租约失败: %v", err)
	}
	if _, acquired, err := store.AcquireOwnerLease(ctx, "raider", time.Minute); err != nil || !acquired {
		t.Fatalf("抢占=%v err=%v", acquired, err)
	}
	// 被拒续租是终态：Lost 关闭并记录 ErrOwnerLeaseLost。
	select {
	case <-keeper.Lost():
		if !errors.Is(keeper.LostError(), ErrOwnerLeaseLost) {
			t.Fatalf("LostError=%v", keeper.LostError())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("被抢占后 Lost 未关闭")
	}
	keeper.Close() // 已丢失的关闭只停循环。
}

func TestWILeaseKeeperFatalOnce(t *testing.T) {
	// fatal 只记录首个原因且只关闭一次。
	keeper := &LeaseKeeper{lostCh: make(chan struct{})}
	keeper.fatal(errors.New("first"))
	keeper.fatal(errors.New("second"))
	if got := keeper.LostError(); got == nil || got.Error() != "first" {
		t.Fatalf("LostError=%v", got)
	}
	select {
	case <-keeper.Lost():
	default:
		t.Fatal("Lost 未关闭")
	}
}

func TestWIRunOwnerContract(t *testing.T) {
	store := wiOpenPostgresStore(t)
	ctx := context.Background()

	// nil keeper 必须报错。
	if err := RunOwner(ctx, store, nil, Config{}, slog.Default()); err == nil {
		t.Fatal("缺 keeper 必须报错")
	}
	// ctx 取消 → 优雅 nil。
	keeper, ok, err := StartLeaseKeeper(ctx, store, "wi-owner", time.Minute, slog.Default())
	if err != nil || !ok {
		t.Fatalf("启动 keeper=%v err=%v", ok, err)
	}
	defer keeper.Close()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	done := make(chan error, 1)
	go func() { done <- RunOwner(cancelled, store, keeper, Config{}, slog.Default()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("取消后应 nil: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消后 RunOwner 未退出")
	}
	// lease 丢失后 RunOwner 再进入：入口 reacquire 必须先触库重取，不再回放
	// 旧 keeper 的存储错误（2026-09-28 修订）。2026-10-15 起获取冲突分支对同
	// owner_id 的未过期行直接接管并自增 fence（store.go AcquireOwnerLease），
	// 所以行仍被 wi-owner 自己持有时重取成功、终态清空；跨 owner 持有才被拒。
	lostFence := keeper.Lease().FenceToken
	keeper.fatal(ErrOwnerLeaseLost)
	// 重取成功后 RunOwner 进入保留循环，零值间隔下不会自行返回，所以带取消
	// 跑并断言它以 nil 退出（重取失败会在进入循环前直接返回错误）。
	sameOwnerCtx, sameOwnerCancel := context.WithCancel(ctx)
	sameOwnerDone := make(chan error, 1)
	go func() { sameOwnerDone <- RunOwner(sameOwnerCtx, store, keeper, Config{}, slog.Default()) }()
	deadline := time.Now().Add(5 * time.Second)
	for keeper.LostError() != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	sameOwnerCancel()
	select {
	case err := <-sameOwnerDone:
		if err != nil {
			t.Fatalf("同 owner 未过期行的入口重取必须接管成功: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("同 owner 重取后 RunOwner 未随取消退出")
	}
	if keeper.LostError() != nil {
		t.Fatal("同 owner 接管后终态必须清空")
	}
	if got := keeper.Lease().FenceToken; got <= lostFence {
		t.Fatalf("同 owner 接管必须递增 fence: before=%d after=%d", lostFence, got)
	}
	keeper.fatal(ErrOwnerLeaseLost)
	// 同 owner 重取刚把 lease_until 续到未来，先把行置为过期，rival 才能按
	// 跨 owner 契约拿到行（未过期的跨 owner 获取本来就应被拒）。
	if _, err := store.(*sqlStore).db.Exec(
		`UPDATE juhe_dataset.operation_log_owner_leases SET lease_until=$1 WHERE lease_key='f4-operation-log-persistence'`,
		storageTime(time.Now().Add(-time.Minute)),
	); err != nil {
		t.Fatalf("置过期 rival 前置行: %v", err)
	}
	rival, ok, err := StartLeaseKeeper(ctx, store, "wi-owner-rival", time.Minute, slog.Default())
	if err != nil || !ok {
		t.Fatalf("rival 获取=%v err=%v", ok, err)
	}
	defer rival.Close()
	rivalDone := make(chan error, 1)
	go func() { rivalDone <- RunOwner(ctx, store, keeper, Config{}, slog.Default()) }()
	select {
	case err := <-rivalDone:
		if err == nil {
			t.Fatal("跨 owner 持有行时入口重取必须失败")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunOwner 未报告跨 owner 入口重取失败")
	}
}

func TestWIRunOwnerRetentionPasses(t *testing.T) {
	store := wiOpenPostgresStore(t)
	ctx := context.Background()
	keeper, ok, err := StartLeaseKeeper(ctx, store, "wi-cadence", time.Minute, slog.Default())
	if err != nil || !ok {
		t.Fatalf("启动 keeper=%v err=%v", ok, err)
	}
	defer keeper.Close()
	cfg := Config{Mode: ModeSQLite, RetentionInterval: 15 * time.Millisecond, RetentionDays: 30, RetentionBatchSize: 100}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond) // 至少经历一次正常 pass。
		cancel()
		done <- RunOwner(runCtx, store, keeper, cfg, slog.Default())
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("正常节奏应优雅退出: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunOwner 未退出")
	}

	// store 故障 → transient 失败跳过本轮（不终止组件）。
	broken := wiOpenPostgresStore(t)
	brokenKeeper, ok, err := StartLeaseKeeper(ctx, broken, "wi-broken", time.Minute, slog.Default())
	if err != nil || !ok {
		t.Fatalf("启动 keeper=%v err=%v", ok, err)
	}
	defer brokenKeeper.Close()
	_ = broken.Close()
	brokenDone := make(chan error, 1)
	brokenCtx, brokenCancel := context.WithCancel(ctx)
	go func() {
		time.Sleep(50 * time.Millisecond)
		brokenCancel()
		brokenDone <- RunOwner(brokenCtx, broken, brokenKeeper, cfg, slog.Default())
	}()
	select {
	case err := <-brokenDone:
		if err != nil {
			t.Fatalf("transient 失败不得终止组件: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("故障场景 RunOwner 未退出")
	}
}

func TestWIStorageTimestampScanMatrix(t *testing.T) {
	var timestamp storageTimestamp
	// time.Time。
	if err := timestamp.Scan(time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("time.Time=%v", err)
	}
	if !strings.HasPrefix(string(timestamp), "2026-09-10T08:00:00") {
		t.Fatalf("timestamp=%q", timestamp)
	}
	// string。
	if err := timestamp.Scan("2026-09-10T08:00:00Z"); err != nil {
		t.Fatalf("string=%v", err)
	}
	// []byte。
	if err := timestamp.Scan([]byte("2026-09-10T08:00:00Z")); err != nil {
		t.Fatalf("[]byte=%v", err)
	}
	// 非法字符串。
	if err := timestamp.Scan("nope"); err == nil {
		t.Fatal("非法字符串必须报错")
	}
	// 不支持类型。
	if err := timestamp.Scan(42); err == nil {
		t.Fatal("int 必须报错")
	}
	// parseStorageTime 拒绝空值。
	if _, err := parseStorageTime("  "); err == nil {
		t.Fatal("空时间必须报错")
	}
}

func TestWIKnownAndQuoteHelpers(t *testing.T) {
	if !known("self", "self", "admin") || known("weird", "self", "admin") {
		t.Fatal("known 错误")
	}
	if got := quotePostgresIdentifier(`ta"ble`); got != `"ta""ble"` {
		t.Fatalf("quote=%q", got)
	}
	// normalizeLegacyOperationLogInput 矩阵。
	valid := Input{ID: "id-1", ActorSystemAccountID: "a", ActorRole: "admin", Mode: "admin", Module: "m", Action: "act", OperationKey: "m.act", ResourceType: "r", Summary: "s", DetailLevel: "full", VisibilityScope: "admin_only", CreatedAt: "2026-09-10T08:00:00Z", Metadata: []byte(`{"k":1}`)}
	if err := normalizeLegacyOperationLogInput(valid); err != nil {
		t.Fatalf("合法输入报错: %v", err)
	}
	missing := valid
	missing.ID = " "
	if err := normalizeLegacyOperationLogInput(missing); err == nil || !strings.Contains(err.Error(), "id") {
		t.Fatalf("缺 id=%v", err)
	}
	badEnum := valid
	badEnum.Mode = "weird"
	if err := normalizeLegacyOperationLogInput(badEnum); err == nil || !strings.Contains(err.Error(), "枚举") {
		t.Fatalf("非法枚举=%v", err)
	}
	badMeta := valid
	badMeta.Metadata = []byte(`{invalid`)
	if err := normalizeLegacyOperationLogInput(badMeta); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("坏 metadata=%v", err)
	}
	badTime := valid
	badTime.CreatedAt = "nope"
	if err := normalizeLegacyOperationLogInput(badTime); err == nil || !strings.Contains(err.Error(), "created_at") {
		t.Fatalf("坏 createdAt=%v", err)
	}
}

func TestWIPostgresCatalogOverSQLite(t *testing.T) {
	// postgresSQLCatalog 是通用 database/sql 包装：用 SQLite 验证行为。
	db, err := sql.Open("sqlite", "file:wi-oplog-catalog?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	catalog := postgresSQLCatalog{queryer: db}
	if _, err := db.Exec(`CREATE TABLE probe (name TEXT, ok INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO probe VALUES ('x', 1)`); err != nil {
		t.Fatal(err)
	}
	name, err := catalog.String(context.Background(), `SELECT name FROM probe WHERE ok=?`, 1)
	if err != nil || name != "x" {
		t.Fatalf("String=%q err=%v", name, err)
	}
	ok, err := catalog.Bool(context.Background(), `SELECT ok FROM probe WHERE name='x'`)
	if err != nil || !ok {
		t.Fatalf("Bool=%v err=%v", ok, err)
	}
	// 查询失败透传。
	if _, err := catalog.String(context.Background(), `SELECT missing FROM probe`); err == nil {
		t.Fatal("坏查询必须报错")
	}
}

func TestWIDetailAndListFilterContract(t *testing.T) {
	store := wiOpenPostgresStore(t)
	ctx := context.Background()
	lease, ok, err := store.AcquireOwnerLease(ctx, "wi-filter", time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease=%v err=%v", ok, err)
	}
	base := Input{ActorSystemAccountID: "actor-1", ActorRole: "admin", Module: "accounts", Action: "update", OperationKey: "accounts.update", ResourceType: "account", ResourceID: "acc-1", Summary: "first", CreatedAt: "2026-08-13T00:00:00Z", Mode: "admin", DetailLevel: "full", VisibilityScope: "all_users", Metadata: []byte(`{}`)}
	first := base
	first.ID = "op-1"
	second := base
	second.ID = "op-2"
	second.Summary = "second"
	second.Module = "keys"
	second.Action = "create"
	second.OperationKey = "keys.create"
	second.CreatedAt = "2026-08-14T00:00:00Z"
	for _, entry := range []Input{first, second} {
		if ignored, err := store.Persist(ctx, lease, entry); err != nil || ignored {
			t.Fatalf("persist %s: ignored=%v err=%v", entry.ID, ignored, err)
		}
	}
	// module 过滤。
	filtered, err := store.List(ctx, ListOptions{Module: "keys"})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].ID != "op-2" {
		t.Fatalf("module 过滤=%+v err=%v", filtered, err)
	}
	// summary 关键词过滤走搜索词表（second 的 second 的摘要归一化词）。
	keyword, err := store.List(ctx, ListOptions{SummaryKeyword: "second"})
	if err != nil || len(keyword.Items) != 1 || keyword.Items[0].ID != "op-2" {
		t.Fatalf("关键词过滤=%+v err=%v", keyword, err)
	}
	// 超过 128 rune 的关键词读作空结果（1=0 短路）。
	long := strings.Repeat("长", 129)
	empty, err := store.List(ctx, ListOptions{SummaryKeyword: long})
	if err != nil || len(empty.Items) != 0 {
		t.Fatalf("超长关键词=%+v err=%v", empty, err)
	}
	// all_users 记录对所有 viewer 可见；空 viewer 视为完整视图。
	if _, found, err := store.Detail(ctx, "op-1", ""); err != nil || !found {
		t.Fatalf("空 viewer=%v err=%v", found, err)
	}
	detail, found, err := store.Detail(ctx, "op-1", "actor-1")
	if err != nil || !found || detail.OperationKey != "accounts.update" {
		t.Fatalf("detail=%+v found=%v err=%v", detail, found, err)
	}
	// 不存在的 ID。
	if _, found, err := store.Detail(ctx, "nope", "actor-1"); err != nil || found {
		t.Fatalf("缺失 detail=%v err=%v", found, err)
	}
	// 分页：pageSize=1 只返回最新一条。
	page, err := store.List(ctx, ListOptions{PageSize: 1})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("分页=%+v err=%v", page, err)
	}
}
