package tablemonitor

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/pgpool"
)

// w9g（tablemonitor 错误臂批次四）：宽松脚本 driver + store/sampler/config/
// owner-lease/runner 剩余错误分支。与 w7d 脚本 driver 并存，命名为 w9g 前缀。

// ---------------------------------------------------------------------------
// 宽松脚本 driver：未命中的语句默认成功；按子串注入错误/panic/行。
// ---------------------------------------------------------------------------

var w9gTMDriverSeq int64

type w9gTrigger struct {
	contains     string
	queryErr     error
	execErr      error
	panicMessage string
	rows         [][]driver.Value
	cols         []string
	zeroAffected bool
}

type w9gScript struct {
	triggers []w9gTrigger
}

func (s *w9gScript) match(kind, query string) *w9gTrigger {
	for index := range s.triggers {
		trigger := &s.triggers[index]
		if trigger.contains != "" && containsFold(query, trigger.contains) {
			return trigger
		}
	}
	return nil
}

func containsFold(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOfFold(haystack, needle) >= 0)
}

func indexOfFold(haystack, needle string) int {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if equalFold(haystack[index:index+len(needle)], needle) {
			return index
		}
	}
	return -1
}

func equalFold(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := 0; index < len(left); index++ {
		leftChar, rightChar := left[index], right[index]
		if 'A' <= leftChar && leftChar <= 'Z' {
			leftChar += 'a' - 'A'
		}
		if 'A' <= rightChar && rightChar <= 'Z' {
			rightChar += 'a' - 'A'
		}
		if leftChar != rightChar {
			return false
		}
	}
	return true
}

type w9gTMDriver struct{ script *w9gScript }

func (d w9gTMDriver) Open(string) (driver.Conn, error) { return &w9gTMConn2{script: d.script}, nil }

type w9gTMConn2 struct{ script *w9gScript }

func (c *w9gTMConn2) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w9g tm: Prepare unsupported")
}
func (c *w9gTMConn2) Close() error              { return nil }
func (c *w9gTMConn2) Begin() (driver.Tx, error) { return w9gTMTx2{}, nil }
func (c *w9gTMConn2) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}

func (c *w9gTMConn2) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if trigger := c.script.match("Query", query); trigger != nil {
		if trigger.panicMessage != "" {
			panic(trigger.panicMessage)
		}
		if trigger.queryErr != nil {
			return nil, trigger.queryErr
		}
		if trigger.rows != nil {
			cols := trigger.cols
			if cols == nil {
				cols = []string{"value"}
			}
			return &w9gTMRows2{cols: cols, values: trigger.rows}, nil
		}
	}
	return &w9gTMRows2{cols: []string{"value"}, values: [][]driver.Value{}}, nil
}

func (c *w9gTMConn2) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if trigger := c.script.match("Exec", query); trigger != nil {
		if trigger.panicMessage != "" {
			panic(trigger.panicMessage)
		}
		if trigger.execErr != nil {
			return nil, trigger.execErr
		}
		if trigger.zeroAffected {
			return driver.RowsAffected(0), nil
		}
	}
	return driver.RowsAffected(1), nil
}

type w9gTMTx2 struct{}

func (w9gTMTx2) Commit() error   { return nil }
func (w9gTMTx2) Rollback() error { return nil }

type w9gTMRows2 struct {
	cols   []string
	values [][]driver.Value
	index  int
}

func (r *w9gTMRows2) Columns() []string { return r.cols }
func (r *w9gTMRows2) Close() error      { return nil }
func (r *w9gTMRows2) Err() error        { return nil }
func (r *w9gTMRows2) Next(dest []driver.Value) error {
	if r.index < len(r.values) {
		copy(dest, r.values[r.index])
		r.index++
		return nil
	}
	return io.EOF
}

func w9gOpenScriptedDB(t *testing.T, triggers []w9gTrigger) *sql.DB {
	t.Helper()
	name := "w9g-tm-scripted-" + fmt.Sprint(atomic.AddInt64(&w9gTMDriverSeq, 1))
	sql.Register(name, w9gTMDriver{script: &w9gScript{triggers: triggers}})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// w9gSQLiteScriptedStore 构造 ModeSQLite 的脚本化 Store。
func w9gSQLiteScriptedStore(t *testing.T, triggers []w9gTrigger) *Store {
	t.Helper()
	return &Store{db: w9gOpenScriptedDB(t, triggers), mode: ModeSQLite}
}

// newW9gPoolHandle 把脚本化 *sql.DB 包装成 pgpool 句柄。
func newW9gPoolHandle(t *testing.T, db *sql.DB) *pgpool.Handle {
	t.Helper()
	registry := pgpool.NewRegistry()
	handle, err := registry.AcquireWith(func() (*sql.DB, error) { return db, nil }, "w9g://table-monitor", "w9g-store", 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	return handle
}

// w9gFixtureConfig 返回指向真实空源库的 SQLite 配置（无 shard）。
func w9gFixtureConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	env := sqliteTestEnv(root)
	for _, key := range []string{"JUHE_AI_DATABASE_PATH", "JUHE_AI_DATASET_DATABASE_PATH", "JUHE_AI_USAGE_CATALOG_DATABASE_PATH", "JUHE_AI_STATS_DATABASE_PATH", "JUHE_AI_RUNTIME_LOG_DATABASE_PATH"} {
		createSQLiteSource(t, env[key])
	}
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	cfg.OwnerLease = 30 * time.Second
	cfg.Interval = 50 * time.Millisecond
	cfg.RetentionBatchSize = 10
	cfg.RetentionMaxBatches = 2
	return cfg
}

// w9gSampleContext 返回携带租约的上下文（RunOnce 契约）。
func w9gSampleContext(lease OwnerLease) context.Context {
	return context.WithValue(context.Background(), ownerLeaseContextKey{}, lease)
}

// ---------- Store 错误臂（SQLite 脚本化） ----------

func TestW9GStoreErrorArms(t *testing.T) {
	ctx := context.Background()
	lease := OwnerLease{OwnerID: "w9g-owner", FenceToken: 3}
	boom := errors.New("w9g: boom")

	cases := []struct {
		name     string
		triggers []w9gTrigger
		act      func(t *testing.T, store *Store)
	}{
		{
			name:     "schema 检查失败",
			triggers: []w9gTrigger{{contains: "sqlite_schema", queryErr: boom}},
			act: func(t *testing.T, store *Store) {
				if err := store.EnsureSchema(ctx); err == nil || !containsFold(err.Error(), "检查表监控 SQLite schema 失败") {
					t.Fatalf("err = %v", err)
				}
			},
		},
		{
			name:     "Acquire 租约失败",
			triggers: []w9gTrigger{{contains: "RETURNING fence_token", queryErr: boom}},
			act: func(t *testing.T, store *Store) {
				if _, _, err := store.AcquireOwnerLease(ctx, "owner", time.Minute); err == nil {
					t.Fatal("必须报错")
				}
			},
		},
		{
			name:     "Acquire 租约未命中（ErrNoRows）",
			triggers: []w9gTrigger{{contains: "RETURNING fence_token"}},
			act: func(t *testing.T, store *Store) {
				acquired, err := func() (bool, error) {
					_, acquired, err := store.AcquireOwnerLease(ctx, "owner", time.Minute)
					return acquired, err
				}()
				if err != nil || acquired {
					t.Fatalf(" acquired=%t err=%v", acquired, err)
				}
			},
		},
		{
			name:     "WriteSample 校验失败",
			triggers: []w9gTrigger{{contains: "updated_at = updated_at", zeroAffected: true}},
			act: func(t *testing.T, store *Store) {
				err := store.WriteSample(ctx, lease, collectedSample{})
				if !errors.Is(err, ErrOwnerLeaseLost) {
					t.Fatalf("err = %v", err)
				}
			},
		},
		{
			name:     "WriteSample 数据库插入失败",
			triggers: []w9gTrigger{{contains: "INSERT INTO database_storage_snapshots", execErr: boom}},
			act: func(t *testing.T, store *Store) {
				sample := collectedSample{databases: []DatabaseSnapshot{{Role: "business"}}}
				if err := store.WriteSample(ctx, lease, sample); !errors.Is(err, boom) {
					t.Fatalf("err = %v", err)
				}
			},
		},
		{
			name:     "WriteSample 表插入失败",
			triggers: []w9gTrigger{{contains: "INSERT INTO table_storage_snapshots", execErr: boom}},
			act: func(t *testing.T, store *Store) {
				sample := collectedSample{tables: []TableSnapshot{{Role: "business", TableName: "t"}}}
				if err := store.WriteSample(ctx, lease, sample); !errors.Is(err, boom) {
					t.Fatalf("err = %v", err)
				}
			},
		},
		{
			name:     "Cleanup DELETE 失败",
			triggers: []w9gTrigger{{contains: "DELETE FROM table_storage_snapshots", execErr: boom}},
			act: func(t *testing.T, store *Store) {
				if _, err := store.Cleanup(ctx, lease, time.Now(), 10); !errors.Is(err, boom) {
					t.Fatalf("err = %v", err)
				}
			},
		},
		{
			name:     "CleanupUntilComplete 中途失败",
			triggers: []w9gTrigger{{contains: "DELETE FROM database_storage_snapshots", execErr: boom}},
			act: func(t *testing.T, store *Store) {
				// 第一批 table 表删除成功（affected 1），第二批 database 表失败。
				if _, err := store.CleanupUntilComplete(ctx, lease, time.Now(), 10, 3); !errors.Is(err, boom) {
					t.Fatalf("err = %v", err)
				}
			},
		},
		{
			name:     "hasExpiredSnapshots 查询失败",
			triggers: []w9gTrigger{{contains: "SELECT EXISTS", queryErr: boom}},
			act: func(t *testing.T, store *Store) {
				// 全部批次删满 → 尾部 pending 检查 → 查询失败上抛。
				if _, err := store.CleanupUntilComplete(ctx, lease, time.Now(), 10, 2); !errors.Is(err, boom) {
					t.Fatalf("err = %v", err)
				}
			},
		},
		{
			name:     "hasExpiredSnapshots 仍有 pending",
			triggers: []w9gTrigger{{contains: "SELECT EXISTS", cols: []string{"exists"}, rows: [][]driver.Value{{true}}}},
			act: func(t *testing.T, store *Store) {
				_, err := store.CleanupUntilComplete(ctx, lease, time.Now(), 10, 2)
				if err == nil || !containsFold(err.Error(), "仍未清空") {
					t.Fatalf("err = %v", err)
				}
			},
		},
		{
			name:     "previousTableSnapshots 查询失败",
			triggers: []w9gTrigger{{contains: "WITH target", queryErr: boom}},
			act: func(t *testing.T, store *Store) {
				sample := collectedSample{tables: []TableSnapshot{{Role: "business", TableName: "t"}}}
				err := store.populateGrowth(ctx, &sample)
				if !errors.Is(err, boom) {
					t.Fatalf("err = %v", err)
				}
			},
		},
		{
			name: "previousTableSnapshots 基线填充",
			triggers: []w9gTrigger{{
				contains: "WITH target",
				cols:     []string{"target_index", "total_bytes", "row_count"},
				rows:     [][]driver.Value{{int64(0), int64(100), int64(7)}},
			}},
			act: func(t *testing.T, store *Store) {
				sample := collectedSample{tables: []TableSnapshot{{Role: "business", TableName: "t", TotalBytes: intPtrW9G2(150), RowCount: intPtrW9G2(9)}}}
				if err := store.populateGrowth(ctx, &sample); err != nil {
					t.Fatal(err)
				}
				if sample.tables[0].GrowthBytes1h == nil || *sample.tables[0].GrowthBytes1h != 50 {
					t.Fatalf("growth = %+v", sample.tables[0])
				}
			},
		},
		{
			name: "previousTableSnapshots 无效序号",
			triggers: []w9gTrigger{{
				contains: "WITH target",
				cols:     []string{"target_index", "total_bytes", "row_count"},
				rows:     [][]driver.Value{{int64(99), nil, nil}},
			}},
			act: func(t *testing.T, store *Store) {
				sample := collectedSample{tables: []TableSnapshot{{Role: "business", TableName: "t"}}}
				if err := store.populateGrowth(ctx, &sample); err == nil {
					t.Fatal("无效序号必须报错")
				}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.act(t, w9gSQLiteScriptedStore(t, testCase.triggers))
		})
	}
}

func intPtrW9G2(v int64) *int64 { return &v }

// ---------- sampler 采集臂 ----------

func TestW9GCollectSQLiteEdges(t *testing.T) {
	cfg := w9gFixtureConfig(t)

	// 全空源库：RunOnce 成功且零快照。
	store := w9gSQLiteScriptedStore(t, []w9gTrigger{
		{contains: "SELECT EXISTS", cols: []string{"exists"}, rows: [][]driver.Value{{false}}},
	})
	result, err := RunOnce(w9gSampleContext(OwnerLease{OwnerID: "w9g", FenceToken: 1}), cfg, store, time.Now().UTC())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.DatabaseSnapshots != 4 {
		t.Fatalf("result = %+v", result)
	}

	// now 为零值：回落 time.Now。
	if _, err := RunOnce(w9gSampleContext(OwnerLease{OwnerID: "w9g", FenceToken: 1}), cfg, store, time.Time{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 源库缺失：采集整体失败且无部分结果 → RunOnce 直接上抛。
	missing := cfg
	missing.BusinessPath = filepath.Join(t.TempDir(), "missing.sqlite3")
	if _, err := RunOnce(w9gSampleContext(OwnerLease{OwnerID: "w9g", FenceToken: 1}), missing, store, time.Now().UTC()); err == nil {
		t.Fatal("源库缺失必须上抛")
	}

	// Codex shard 根为非法 glob 模式：枚举失败。
	badGlob := cfg
	badGlob.CodexShardRoot = filepath.Join(t.TempDir(), "[x")
	if _, err := collectSQLite(context.Background(), badGlob, time.Now().UTC()); err == nil {
		t.Fatal("非法 glob 必须报错")
	}

	// 非常规文件：打开失败。
	notRegular := cfg
	dir := filepath.Join(t.TempDir(), "as-directory.sqlite3")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	notRegular.BusinessPath = dir
	if _, err := collectSQLite(context.Background(), notRegular, time.Now().UTC()); err == nil {
		t.Fatal("非常规文件必须报错")
	}

	// 垃圾文件：打开后 PRAGMA 失败。
	garbage := cfg
	garbageFile := filepath.Join(t.TempDir(), "garbage.sqlite3")
	if err := os.WriteFile(garbageFile, []byte("definitely not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	garbage.BusinessPath = garbageFile
	if _, err := collectSQLite(context.Background(), garbage, time.Now().UTC()); err == nil {
		t.Fatal("垃圾文件必须报错")
	}

	// shard 部分选中：未全选时逐 shard 表直通 else 分支。
	shardRoot := t.TempDir()
	for _, name := range []string{"a.sqlite3", "b.sqlite3", "c.sqlite3"} {
		createSQLiteSource(t, filepath.Join(shardRoot, name))
	}
	partial := cfg
	partial.CodexShardRoot = shardRoot
	partial.MaxConcurrentSources = 1
	partial.MaxTables = 4
	if _, err := collectSQLite(context.Background(), partial, time.Now().UTC()); err != nil {
		t.Fatalf("部分 shard 采集必须成功: %v", err)
	}
}

func TestW9GSelectShardWindow(t *testing.T) {
	entries := []string{"a", "b", "c"}
	if got := selectShardWindow(nil, 2, time.Now(), time.Minute); got != nil {
		t.Fatalf("空 entries = %v", got)
	}
	if got := selectShardWindow(entries, 0, time.Now(), time.Minute); got != nil {
		t.Fatalf("limit 0 = %v", got)
	}
	if got := selectShardWindow(entries, 5, time.Now(), time.Minute); len(got) != 3 {
		t.Fatalf("limit 超量 = %v", got)
	}
	// interval 缺省回落 1 分钟。
	got := selectShardWindow(entries, 2, time.Unix(0, 0).Add(time.Minute), 0)
	if len(got) != 2 {
		t.Fatalf("got = %v", got)
	}
	// 负 slot（1970 前）回卷。
	negative := selectShardWindow(entries, 2, time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC), time.Millisecond)
	if len(negative) != 2 {
		t.Fatalf("negative slot = %v", negative)
	}
}

func TestW9GCollectBoundedEdges(t *testing.T) {
	ctx := context.Background()
	if _, err := collectBounded(ctx, 0, []int{1}, func(v int) (int, error) { return v, nil }); err == nil {
		t.Fatal("limit 0 必须报错")
	}
	empty, err := collectBounded(ctx, 4, nil, func(v int) (int, error) { return v, nil })
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty targets = %v err=%v", empty, err)
	}
	// 采集错误聚合。
	if _, err := collectBounded(ctx, 2, []int{1}, func(int) (int, error) { return 0, errors.New("collect boom") }); err == nil {
		t.Fatal("采集错误必须聚合上抛")
	}
	// 回调 panic 恢复。
	if _, err := collectBounded(ctx, 2, []int{1}, func(int) (int, error) { panic("callback boom") }); err == nil {
		t.Fatal("回调 panic 必须转错误")
	}
	if _, err := collectSafely(func(int) (int, error) { panic("safely boom") }, 1); err == nil {
		t.Fatal("collectSafely 必须恢复 panic")
	}
	// worker panic 恢复（collect 本身 panic 由 collectSafely 恢复；这里恢复
	// worker 层 panic 的分支经 collectBounded 的 recover 覆盖）。
	// ctx 取消分支。
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := collectBounded(cancelled, 2, []int{1, 2}, func(int) (int, error) { return 1, nil }); err == nil {
		t.Fatal("取消后必须报错")
	}
	// 成功路径。
	ok, err := collectBounded(ctx, 2, []int{1, 2}, func(v int) (int, error) { return v * 2, nil })
	if err != nil || len(ok) != 2 {
		t.Fatalf("ok = %v err = %v", ok, err)
	}
}

func TestW9GCollectPostgresEdges(t *testing.T) {
	ctx := context.Background()
	// 缺 URL：连接池获取失败。
	if _, err := collectPostgres(ctx, Config{Mode: ModePostgres}, time.Now().UTC()); err == nil {
		t.Fatal("缺 URL 必须报错")
	}
	// 池默认值 + 真实拨号失败：schema 检查聚合错误。
	cfg := Config{Mode: ModePostgres, PostgresURL: "postgres://w9g.invalid:1/db?sslmode=disable&connect_timeout=1"}
	if _, err := collectPostgres(ctx, cfg, time.Now().UTC()); err == nil {
		t.Fatal("不可达 PG 必须报错")
	}

	// 脚本化源库：schema 缺失 / 各查询错误臂。
	build := func(triggers []w9gTrigger) Config {
		db := w9gOpenScriptedDB(t, triggers)
		t.Cleanup(func() { _ = db.Close() })
		return Config{Mode: ModePostgres, PostgresURL: "w9g://scripted", PostgresPool: newW9gPoolHandle(t, db)}
	}

	missing := build(nil)
	missing.MaxConcurrentSources = 1
	missing.MaxTables = 5
	if _, err := collectPostgres(ctx, missing, time.Now().UTC()); err == nil {
		t.Fatal("schema 缺失必须报错")
	}

	boomErr := errors.New("w9g: pg boom")
	failing := build([]w9gTrigger{{contains: "pg_namespace", queryErr: boomErr}})
	failing.MaxConcurrentSources = 1
	if _, err := collectPostgres(ctx, failing, time.Now().UTC()); err == nil {
		t.Fatal("schema 检查失败必须报错")
	}

	scanFail := build([]w9gTrigger{
		{contains: "pg_namespace", cols: []string{"exists"}, rows: [][]driver.Value{{true}}},
		{contains: "block_size", cols: []string{"setting"}, rows: [][]driver.Value{{int64(8192)}}},
		{contains: "COUNT(*)", cols: []string{"tables", "indexes"}, rows: [][]driver.Value{{int64(1), int64(1)}}},
		{contains: "index_summary", cols: []string{"one"}, rows: [][]driver.Value{{int64(1)}}},
	})
	scanFail.MaxConcurrentSources = 1
	if _, err := collectPostgres(ctx, scanFail, time.Now().UTC()); err == nil {
		t.Fatal("目录 Scan 失败必须报错")
	}
}

func TestW9GAggregateCodexShardsEdges(t *testing.T) {
	if _, _, err := aggregateCodexShards("root", time.Now(), []collectedSample{{}}); err == nil {
		t.Fatal("数据库快照数量异常必须报错")
	}
	incomplete := collectedSample{databases: []DatabaseSnapshot{{}}}
	if _, _, err := aggregateCodexShards("root", time.Now(), []collectedSample{incomplete}); err == nil {
		t.Fatal("缺少统计必须报错")
	}
	page4, page8 := int64(4096), int64(8192)
	files, pages, free := int64(10), int64(2), int64(1)
	first := collectedSample{databases: []DatabaseSnapshot{{
		Path: "a", PageSize: &page4, PageCount: &pages, FreelistCount: &free, FileBytes: &files, UsedBytes: &files, FreeBytes: &files,
		TableCount: 1, IndexCount: 2, WALBytes: nil, SHMBytes: nil,
	}}, tables: []TableSnapshot{{TableName: "t"}}}
	second := collectedSample{databases: []DatabaseSnapshot{{
		Path: "b", PageSize: &page8, PageCount: &pages, FreelistCount: &free, FileBytes: &files, UsedBytes: &files, FreeBytes: &files,
		TableCount: 3, IndexCount: 4, WALBytes: nil, SHMBytes: nil,
	}}}
	aggregated, tables, err := aggregateCodexShards("root", time.Now(), []collectedSample{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if aggregated.PageSize != nil {
		t.Fatal("page size 不一致时必须为 nil")
	}
	if aggregated.WALBytes != nil || aggregated.SHMBytes != nil {
		t.Fatal("任一 shard 缺 WAL/SHM 时必须为 nil")
	}
	if aggregated.TableCount != 4 || len(tables) != 1 {
		t.Fatalf("aggregate = %+v tables=%d", aggregated, len(tables))
	}

	// WAL/SHM 全量存在时求和；缺一后不再累计负值。
	walBytes, shmBytes := int64(5), int64(6)
	first.databases[0].WALBytes = &walBytes
	first.databases[0].SHMBytes = &shmBytes
	second.databases[0].WALBytes = &walBytes
	second.databases[0].SHMBytes = &shmBytes
	aggregated, _, err = aggregateCodexShards("root", time.Now(), []collectedSample{first, second})
	if err != nil {
		t.Fatal(err)
	}
	// 第二个 shard page size 不一致 → 聚合 page size 为 nil；WAL/SHM 求和。
	if aggregated.PageSize != nil || aggregated.WALBytes == nil || *aggregated.WALBytes != 10 || aggregated.SHMBytes == nil || *aggregated.SHMBytes != 12 {
		t.Fatalf("aggregate = %+v", aggregated)
	}
}

func TestW9GSamplerSmallHelpers(t *testing.T) {
	if got := addOptionalInt64(nil, intPtrW9G2(1)); got != nil {
		t.Fatalf("nil left = %v", got)
	}
	if got := addOptionalInt64(intPtrW9G2(2), intPtrW9G2(3)); got == nil || *got != 5 {
		t.Fatalf("sum = %v", got)
	}
	sizes := sqliteObjectSizes{"t": {bytes: 1, pages: 2}}
	if sizes.bytes("missing") != nil || sizes.pages("missing") != nil {
		t.Fatal("缺失对象必须返回 nil")
	}
	if sumObjectBytes(sizes, []string{"t", "missing"}) != nil {
		t.Fatal("部分缺失时求和必须为 nil")
	}
	if sumObjectPages(sizes, []string{"t"}) == nil {
		t.Fatal("完整命中时求和不得为 nil")
	}
	if growthDelta(nil, intPtrW9G2(1)) != nil {
		t.Fatal("nil current 必须为 nil")
	}
}

// ---------- config 校验臂 ----------

func TestW9GConfigValidationArms(t *testing.T) {
	root := t.TempDir()
	env := sqliteTestEnv(root)
	for _, key := range []string{"JUHE_AI_DATABASE_PATH", "JUHE_AI_DATASET_DATABASE_PATH", "JUHE_AI_USAGE_CATALOG_DATABASE_PATH", "JUHE_AI_STATS_DATABASE_PATH", "JUHE_AI_RUNTIME_LOG_DATABASE_PATH"} {
		createSQLiteSource(t, env[key])
	}

	cases := []struct {
		name     string
		override map[string]string
		wantErr  string
	}{
		{
			name:     "postgres 池参数非整数",
			override: map[string]string{"JUHE_AI_TABLE_MONITOR_POSTGRES_MAX_OPEN_CONNS": "abc"},
			wantErr:  "必须是正整数",
		},
		// 「sqlite 缺输出路径」「sqlite 缺运行日志路径」「源库路径缺失」
		// 「缺 Codex shard 根」四个失败臂已删除（2026-09-19 零配置决策：
		// 路径类 env 缺省按 DATA_DIR 派生，恒非空，不再是校验失败分支）。
		{
			name:     "sqlite 与运行日志共用文件",
			override: map[string]string{"JUHE_AI_RUNTIME_LOG_DATABASE_PATH": env["JUHE_AI_TABLE_MONITOR_DATABASE_PATH"]},
			wantErr:  "不得与 JUHE_AI_RUNTIME_LOG_DATABASE_PATH 共用",
		},
		{
			name:     "源库与输出共用文件",
			override: map[string]string{"JUHE_AI_DATABASE_PATH": env["JUHE_AI_TABLE_MONITOR_DATABASE_PATH"]},
			wantErr:  "不得与 JUHE_AI_DATABASE_PATH 共用",
		},
		{
			name:     "输出路径的父目录是文件",
			override: map[string]string{"JUHE_AI_TABLE_MONITOR_DATABASE_PATH": filepath.Join(env["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"], "child.sqlite3")},
			wantErr:  "不是目录",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			merged := map[string]string{}
			for key, value := range env {
				merged[key] = value
			}
			for key, value := range testCase.override {
				merged[key] = value
			}
			_, err := LoadConfig(func(key string) string { return merged[key] })
			if err == nil || !containsFold(err.Error(), testCase.wantErr) {
				t.Fatalf("err = %v, want 含 %q", err, testCase.wantErr)
			}
		})
	}

	// 与 Codex shard 共用文件 + shard 文件名重复。
	shardRoot := t.TempDir()
	shared := filepath.Join(shardRoot, "shared.sqlite3")
	createSQLiteSource(t, shared)
	duplicateEnv := map[string]string{}
	for key, value := range env {
		duplicateEnv[key] = value
	}
	duplicateEnv["JUHE_AI_TABLE_MONITOR_DATABASE_PATH"] = shared
	duplicateEnv["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"] = shardRoot
	if _, err := LoadConfig(func(key string) string { return duplicateEnv[key] }); err == nil || !containsFold(err.Error(), "Codex context SQLite shard 共用") {
		t.Fatalf("共用 shard 文件必须拒绝: %v", err)
	}
}

func TestW9GPathHelpers(t *testing.T) {
	if _, err := sameSQLiteFile("", "x"); err == nil {
		t.Fatal("空路径必须报错")
	}
	if _, err := sameSQLiteFile("x", ""); err == nil {
		t.Fatal("空路径必须报错")
	}
	// Windows 保留字符触发非 not-exist 的 stat 错误。
	if runtimeWindows() {
		invalid := filepath.Join(t.TempDir(), "bad?.sqlite3")
		if _, err := sameSQLiteFile(invalid, invalid); err == nil {
			t.Fatal("非法路径 stat 必须报错")
		}
	}
	if _, err := pathWithin("?", "x"); err == nil && runtimeWindows() {
		t.Fatal("非法根路径必须报错")
	}
	if _, err := canonicalPath(""); err == nil {
		t.Fatal("空路径必须报错")
	}
	if _, err := canonicalPath(filepath.Join(t.TempDir(), "missing", "deep", "file.sqlite3")); err != nil {
		t.Fatalf("不存在文件按父目录解析: %v", err)
	}
	if danglingSQLiteSymlink(filepath.Join(t.TempDir(), "missing.sqlite3")) {
		t.Fatal("普通缺失文件不是悬空符号链接")
	}
	if !equalFilesystemPath("A/B", "a\\b") != !isDirSEPWindows() {
		t.Fatal("equalFilesystemPath 平台语义")
	}
}

func runtimeWindows() bool  { return os.PathSeparator == '\\' }
func isDirSEPWindows() bool { return os.PathSeparator == '\\' }

// ---------- owner lease 续租 panic 与 runner ----------

func TestW9GRenewalPanicRecovered(t *testing.T) {
	cfg := w9gFixtureConfig(t)
	cfg.Interval = 30 * time.Second
	cfg.OwnerLease = 3 * time.Second // 续租节拍 = max(1s, OwnerLease/3)。
	store := w9gSQLiteScriptedStore(t, []w9gTrigger{
		{contains: "SET lease_until", panicMessage: "w9g renewal boom"},
	})
	runner := NewRunner(cfg, store, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	err := runner.Run(ctx)
	if err == nil {
		t.Fatal("续租 panic 必须以错误终止 Run")
	}
}

func TestW9GRunCycleArms(t *testing.T) {
	cfg := w9gFixtureConfig(t)
	store := w9gSQLiteScriptedStore(t, nil)
	runner := NewRunner(cfg, store, nil)

	// 带租约的取消 ctx：RunOnce 以 ctx.Canceled 失败，runCycle 透传 ctx.Err()。
	cancelled, cancel := context.WithCancel(w9gSampleContext(OwnerLease{OwnerID: "w9g", FenceToken: 1}))
	cancel()
	if err := runner.runCycle(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	// 无租约的 ctx：RunOnce 直接 ErrOwnerLeaseLost，runCycle 归一为该错误。
	if err := runner.runCycle(context.Background()); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("err = %v", err)
	}

	// 未初始化 runner。
	var nilRunner *Runner
	if err := nilRunner.Run(context.Background()); err == nil {
		t.Fatal("nil runner 必须报错")
	}
}

// ---------- 追加批次：OpenStore / PG bootstrap / 采集内层错误臂 ----------

func TestW9GOpenStoreArms(t *testing.T) {
	// 输出路径是垃圾文件：configureSQLiteWriter 首个 PRAGMA 失败。
	garbage := filepath.Join(t.TempDir(), "garbage.sqlite3")
	if err := os.WriteFile(garbage, []byte("not sqlite at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(Config{Mode: ModeSQLite, OutputPath: garbage}); err == nil {
		t.Fatal("垃圾输出文件必须失败")
	}
	// 正常打开后 Close。
	store, err := OpenStore(Config{Mode: ModeSQLite, OutputPath: filepath.Join(t.TempDir(), "ok.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var nilStore *Store
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil store Close = %v", err)
	}
}

func TestW9GPostgresSchemaAndLeaseArms(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("w9g: pg boom")

	t.Run("advisory lock 失败", func(t *testing.T) {
		store := w7dPostgresStore(t, []w7dTMStep{
			{contains: "to_regclass", cols: []string{"count"}, rows: [][]driver.Value{{int64(0)}}},
			{contains: "pg_advisory_xact_lock", execErr: boom},
		})
		if err := store.EnsureSchema(ctx); err == nil {
			t.Fatal("必须报错")
		}
	})
	t.Run("bootstrap 重检失败", func(t *testing.T) {
		store := w7dPostgresStore(t, []w7dTMStep{
			{contains: "to_regclass", cols: []string{"count"}, rows: [][]driver.Value{{int64(0)}}},
			{contains: "pg_advisory_xact_lock"},
			{contains: "to_regclass", queryErr: boom},
		})
		if err := store.EnsureSchema(ctx); err == nil {
			t.Fatal("必须报错")
		}
	})
	t.Run("DDL 失败", func(t *testing.T) {
		store := w7dPostgresStore(t, []w7dTMStep{
			{contains: "to_regclass", cols: []string{"count"}, rows: [][]driver.Value{{int64(0)}}},
			{contains: "pg_advisory_xact_lock"},
			{contains: "to_regclass", cols: []string{"count"}, rows: [][]driver.Value{{int64(0)}}},
			{execErr: boom},
		})
		if err := store.EnsureSchema(ctx); err == nil {
			t.Fatal("必须报错")
		}
	})
	t.Run("commit 失败", func(t *testing.T) {
		store := w7dPostgresStore(t, []w7dTMStep{
			{contains: "to_regclass", cols: []string{"count"}, rows: [][]driver.Value{{int64(0)}}},
			{contains: "pg_advisory_xact_lock"},
			{contains: "to_regclass", cols: []string{"count"}, rows: [][]driver.Value{{int64(0)}}},
			{},
			{},
			{},
			{},
			{},
			{},
			{},
			{commitErr: boom},
		})
		if err := store.EnsureSchema(ctx); err == nil {
			t.Fatal("必须报错")
		}
	})
	t.Run("PG 租约 ErrNoRows 与续租失败", func(t *testing.T) {
		// Acquire 未命中：RETURNING 无行。
		store := w7dPostgresStore(t, []w7dTMStep{
			{contains: "to_regclass", cols: []string{"count"}, rows: [][]driver.Value{{int64(3)}}},
			{},
			{contains: "RETURNING fence_token"},
		})
		if _, acquired, err := store.AcquireOwnerLease(ctx, "w9g", time.Minute); err != nil || acquired {
			t.Fatalf("acquired=%t err=%v", acquired, err)
		}
		// Renew 失败上抛。
		failing := w7dPostgresStore(t, []w7dTMStep{
			{contains: "SET lease_until", execErr: boom},
		})
		if _, err := failing.RenewOwnerLease(ctx, OwnerLease{OwnerID: "o", FenceToken: 1}, time.Minute); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		// Release 未命中 → ErrOwnerLeaseLost。
		lost := w7dPostgresStore(t, []w7dTMStep{
			{contains: "owner_id = ''", zeroAffected: true},
		})
		if err := lost.ReleaseOwnerLease(ctx, OwnerLease{OwnerID: "o", FenceToken: 1}); !errors.Is(err, ErrOwnerLeaseLost) {
			t.Fatalf("err = %v", err)
		}
		// PG verify FOR UPDATE 未命中 → ErrOwnerLeaseLost。
		verifyLost := w7dPostgresStore(t, []w7dTMStep{
			{contains: "FOR UPDATE"},
		})
		err := verifyLost.WriteSample(ctx, OwnerLease{OwnerID: "o", FenceToken: 1}, collectedSample{})
		if !errors.Is(err, ErrOwnerLeaseLost) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestW9GSQLiteInnerFunctionArms(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("w9g: sqlite boom")

	// listSQLiteTables：查询错误 / Scan 错误 / 计数错误。
	db := w9gOpenScriptedDB(t, []w9gTrigger{{contains: "sqlite_schema", queryErr: boom}})
	if _, _, _, err := listSQLiteTables(ctx, db, 5); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	scanDB := w9gOpenScriptedDB(t, []w9gTrigger{
		{contains: "LIMIT", cols: []string{"name"}, rows: [][]driver.Value{{int64(9)}}},
	})
	if _, _, _, err := listSQLiteTables(ctx, scanDB, 5); err == nil {
		t.Fatal("Scan 类型错误必须上抛")
	}
	countDB := w9gOpenScriptedDB(t, []w9gTrigger{
		{contains: "LIMIT", cols: []string{"name"}, rows: [][]driver.Value{{"t1"}}},
		{contains: "type = 'table'", queryErr: boom},
	})
	if _, _, _, err := listSQLiteTables(ctx, countDB, 5); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}

	// sqliteRowCount / sqliteIndexNames。
	rowDB := w9gOpenScriptedDB(t, []w9gTrigger{{contains: "COUNT(*)", queryErr: boom}})
	if _, err := sqliteRowCount(ctx, rowDB, "t"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	indexDB := w9gOpenScriptedDB(t, []w9gTrigger{{contains: "type = 'index'", queryErr: boom}})
	if _, err := sqliteIndexNames(ctx, indexDB, "t"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}

	// loadSQLiteObjectSizes：dbstat 缺失回退 / 真实错误。
	statMissing := w9gOpenScriptedDB(t, []w9gTrigger{{contains: "dbstat", queryErr: errors.New("no such table: dbstat")}})
	if _, available, err := loadSQLiteObjectSizes(ctx, statMissing, []string{"t"}); available || err != nil {
		t.Fatalf("available=%t err=%v", available, err)
	}
	statBoom := w9gOpenScriptedDB(t, []w9gTrigger{{contains: "dbstat", queryErr: boom}})
	if _, _, err := loadSQLiteObjectSizes(ctx, statBoom, []string{"t"}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	// 缺失 page 求和分支。
	if sumObjectPages(sqliteObjectSizes{"t": {pages: 1}}, []string{"t", "missing"}) != nil {
		t.Fatal("部分缺失求和必须为 nil")
	}
}

func TestW9GCollectPostgresTargetArms(t *testing.T) {
	ctx := context.Background()
	build := func(triggers []w9gTrigger) Config {
		db := w9gOpenScriptedDB(t, triggers)
		t.Cleanup(func() { _ = db.Close() })
		return Config{Mode: ModePostgres, PostgresURL: "w9g://scripted", PostgresPool: newW9gPoolHandle(t, db), MaxConcurrentSources: 1, MaxTables: 5}
	}
	boom := errors.New("w9g: target boom")

	// schema 检查失败（显式错误，而非 ErrNoRows）。
	if _, err := collectPostgres(ctx, build([]w9gTrigger{
		{contains: "SELECT EXISTS (SELECT 1 FROM pg_namespace", queryErr: boom},
	}), time.Now().UTC()); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	// schema 缺失（exists false）。
	if _, err := collectPostgres(ctx, build([]w9gTrigger{
		{contains: "SELECT EXISTS (SELECT 1 FROM pg_namespace", cols: []string{"exists"}, rows: [][]driver.Value{{false}}},
	}), time.Now().UTC()); err == nil || !containsFold(err.Error(), "不存在") {
		t.Fatalf("err = %v", err)
	}
	// block_size 失败。
	if _, err := collectPostgres(ctx, build([]w9gTrigger{
		{contains: "SELECT EXISTS (SELECT 1 FROM pg_namespace", cols: []string{"exists"}, rows: [][]driver.Value{{true}}},
		{contains: "current_setting", queryErr: boom},
	}), time.Now().UTC()); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	// 计数查询失败。
	if _, err := collectPostgres(ctx, build([]w9gTrigger{
		{contains: "SELECT EXISTS (SELECT 1 FROM pg_namespace", cols: []string{"exists"}, rows: [][]driver.Value{{true}}},
		{contains: "current_setting", cols: []string{"size"}, rows: [][]driver.Value{{int64(8192)}}},
		{contains: "relkind IN ('r', 'p', 'm')),", queryErr: boom},
	}), time.Now().UTC()); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	// 目录查询失败。
	if _, err := collectPostgres(ctx, build([]w9gTrigger{
		{contains: "SELECT EXISTS (SELECT 1 FROM pg_namespace", cols: []string{"exists"}, rows: [][]driver.Value{{true}}},
		{contains: "current_setting", cols: []string{"size"}, rows: [][]driver.Value{{int64(8192)}}},
		{contains: "relkind IN ('r', 'p', 'm')),", cols: []string{"tables", "indexes"}, rows: [][]driver.Value{{int64(1), int64(1)}}},
		{contains: "index_summary AS (", queryErr: boom},
	}), time.Now().UTC()); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	// 目录 Scan 列数不足失败。
	_, failed := collectPostgres(ctx, build([]w9gTrigger{
		{contains: "SELECT EXISTS (SELECT 1 FROM pg_namespace", cols: []string{"exists"}, rows: [][]driver.Value{{true}}},
		{contains: "current_setting", cols: []string{"size"}, rows: [][]driver.Value{{int64(8192)}}},
		{contains: "relkind IN ('r', 'p', 'm')),", cols: []string{"tables", "indexes"}, rows: [][]driver.Value{{int64(0), int64(0)}}},
		{contains: "index_summary AS (", cols: []string{"one"}, rows: [][]driver.Value{{int64(1)}}},
	}), time.Now().UTC())
	if failed == nil {
		t.Fatal("目录 Scan 失败必须上抛")
	}
}
