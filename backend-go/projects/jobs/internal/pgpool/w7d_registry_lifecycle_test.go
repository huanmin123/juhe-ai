package pgpool

// w7d（pgpool 覆盖补齐批次，w7d/TestW7D 前缀）：覆盖 Registry.Acquire/
// AcquireWith 生命周期、nil 守卫、并发去重、失败 open 不缓存、observer/stats
// 转发。全部进程内确定性驱动：
//   - 常规臂使用脚本化 database/sql driver（不真正拨号，pgx/sqlite 均不需要）。
//   - 拒连地址臂使用 127.0.0.1:1（构造成功、连接必失败），验证 registry 层
//     不做拨号也不缓存失败。
//   - 真实 PG 臂由 W7D_TEST_POSTGRES_DSN 门禁：设置时执行真实 Ping，
//     未设置时 t.Skip（本地联调用
//     .local/project-resources/dev/env/shared.env 的 JUHE_AI_POSTGRES_URL 导出即可）。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// ---------------------------------------------------------------------------
// 脚本化 database/sql driver：open 恒成功、语句按前缀应答
// ---------------------------------------------------------------------------

var w7dDriverSeq int64

type w7dScriptedDriver struct{ name string }

func (d w7dScriptedDriver) Open(string) (driver.Conn, error) {
	return w7dScriptedConn{}, nil
}

type w7dScriptedConn struct{}

func (w7dScriptedConn) Prepare(query string) (driver.Stmt, error) {
	return w7dScriptedStmt{query: query}, nil
}
func (w7dScriptedConn) Close() error              { return nil }
func (w7dScriptedConn) Begin() (driver.Tx, error) { return w7dScriptedTx{}, nil }

type w7dScriptedStmt struct{ query string }

func (w7dScriptedStmt) Close() error  { return nil }
func (w7dScriptedStmt) NumInput() int { return -1 }
func (w7dScriptedStmt) Exec([]driver.Value) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}
func (w7dScriptedStmt) Query([]driver.Value) (driver.Rows, error) {
	cols := []string{"ok"}
	return &w7dScriptedRows{cols: cols, values: [][]driver.Value{{"1"}}}, nil
}

type w7dScriptedTx struct{}

func (w7dScriptedTx) Commit() error   { return nil }
func (w7dScriptedTx) Rollback() error { return nil }

type w7dScriptedRows struct {
	cols   []string
	values [][]driver.Value
	index  int
}

func (r *w7dScriptedRows) Columns() []string { return r.cols }
func (r *w7dScriptedRows) Close() error      { return nil }
func (r *w7dScriptedRows) Err() error        { return nil }
func (r *w7dScriptedRows) Next(dest []driver.Value) error {
	if r.index < len(r.values) {
		copy(dest, r.values[r.index])
		r.index++
		return nil
	}
	return io.EOF
}

// w7dOpenScriptedDB 注册一次性脚本驱动并打开 *sql.DB。
func w7dOpenScriptedDB(t *testing.T) *sql.DB {
	t.Helper()
	name := "w7d-scripted-" + strconv.FormatInt(atomic.AddInt64(&w7dDriverSeq, 1), 10)
	sql.Register(name, w7dScriptedDriver{})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("打开脚本化 db 失败: %v", err)
	}
	db.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ---------------------------------------------------------------------------
// 生命周期与守卫
// ---------------------------------------------------------------------------

func TestW7DAcquireWithReusesPoolAndForwardsLifecycle(t *testing.T) {
	registry := NewRegistry()
	events := make([]PoolEvent, 0, 8)
	var eventMu sync.Mutex
	registry.SetObserver(func(event PoolEvent) {
		eventMu.Lock()
		defer eventMu.Unlock()
		events = append(events, event)
	})

	handle, err := registry.AcquireWith(func() (*sql.DB, error) { return w7dOpenScriptedDB(t), nil }, "scripted://primary", "jobs", 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	if handle == nil || handle.DB() == nil {
		t.Fatal("AcquireWith 必须返回可用 handle")
	}
	reused, err := registry.Acquire("pgx", "scripted://primary", "jobs", 8, 2)
	if err != nil {
		t.Fatal(err)
	}
	if reused.DB() != handle.DB() {
		t.Fatal("同 URL/role 必须复用同一连接池")
	}
	// 提升上限后 stats 反映新的 MaxOpen。
	reused.DB().SetMaxOpenConns(8)
	snapshots := registry.Stats()
	if len(snapshots) != 1 {
		t.Fatalf("stats 必须只含一个池: %#v", snapshots)
	}
	snapshot := snapshots[0]
	if snapshot.Role != "jobs" || snapshot.Refs != 2 || snapshot.MaxIdle != 2 {
		t.Fatalf("stats 快照不正确: %#v", snapshot)
	}
	if snapshot.MaxOpen != 8 {
		t.Fatalf("复用必须允许提升 MaxOpen: %d", snapshot.MaxOpen)
	}

	// 引用计数归零才真正关闭。
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reused.Close(); err != nil {
		t.Fatal(err)
	}
	if len(registry.Stats()) != 0 {
		t.Fatal("引用归零后池必须从 registry 移除")
	}
	eventMu.Lock()
	defer eventMu.Unlock()
	kinds := make([]string, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	// open → reuse → release → close 的完整事件链（事件常量由平台包定义）。
	want := []string{sqlpool.PoolEventOpen, sqlpool.PoolEventReuse, sqlpool.PoolEventRelease, sqlpool.PoolEventClose}
	if len(kinds) != len(want) {
		t.Fatalf("事件链不完整: %v", kinds)
	}
	for index, kind := range want {
		if kinds[index] != kind {
			t.Fatalf("事件链第 %d 个 = %s, want %s（全部: %v）", index, kinds[index], kind, kinds)
		}
	}
}

func TestW7DAcquireWithFailedOpenIsNotCached(t *testing.T) {
	registry := NewRegistry()
	attempts := 0
	boom := errors.New("w7d opener boom")
	for i := 0; i < 3; i++ {
		if _, err := registry.AcquireWith(func() (*sql.DB, error) {
			attempts++
			return nil, boom
		}, "scripted://failing", "jobs", 4, 2); !errors.Is(err, boom) {
			t.Fatalf("第 %d 次必须暴露原始 open 错误: %v", i+1, err)
		}
	}
	if attempts != 3 {
		t.Fatalf("失败的 open 不得缓存，应每次重试: attempts=%d", attempts)
	}
	if len(registry.Stats()) != 0 {
		t.Fatal("失败 open 不得留下注册条目")
	}
	// 同 key 随后成功 open 正常工作。
	handle, err := registry.AcquireWith(func() (*sql.DB, error) { return w7dOpenScriptedDB(t), nil }, "scripted://failing", "jobs", 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestW7DAcquireValidatesInputsAndLimits(t *testing.T) {
	registry := NewRegistry()
	if _, err := registry.AcquireWith(nil, "x", "jobs", 4, 2); err == nil {
		t.Fatal("空 opener 必须拒绝")
	}
	if _, err := registry.AcquireWith(func() (*sql.DB, error) { return w7dOpenScriptedDB(t), nil }, "", "jobs", 4, 2); err == nil {
		t.Fatal("空 URL 必须拒绝")
	}
	if _, err := registry.AcquireWith(func() (*sql.DB, error) { return w7dOpenScriptedDB(t), nil }, "x", "", 4, 2); err == nil {
		t.Fatal("空 role 必须拒绝")
	}
	if _, err := registry.Acquire("pgx", "postgres://x", "jobs", 0, 0); err == nil {
		t.Fatal("非法连接上限必须拒绝")
	}
	if _, err := registry.Acquire("pgx", "postgres://x", "jobs", 4, 5); err == nil {
		t.Fatal("idle>open 必须拒绝")
	}
	// 拒连地址：registry 层只做 sql.Open，不拨号也不报错。
	refused, err := registry.Acquire("pgx", "postgres://w7d@127.0.0.1:1/refused?sslmode=disable", "jobs", 2, 1)
	if err != nil {
		t.Fatalf("拒连地址构造必须成功（不拨号）: %v", err)
	}
	if err := refused.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestW7DNilRegistryAndZeroValueBehaviour(t *testing.T) {
	var nilRegistry *Registry
	if _, err := nilRegistry.Acquire("pgx", "x", "jobs", 4, 2); err == nil {
		t.Fatal("nil registry Acquire 必须拒绝")
	}
	if _, err := nilRegistry.AcquireWith(func() (*sql.DB, error) { return nil, nil }, "x", "jobs", 4, 2); err == nil {
		t.Fatal("nil registry AcquireWith 必须拒绝")
	}
	if err := nilRegistry.Close(); err != nil {
		t.Fatal("nil registry Close 必须为 nil")
	}
	nilRegistry.SetObserver(func(PoolEvent) {})
	if nilRegistry.Stats() != nil {
		t.Fatal("nil registry Stats 必须为 nil")
	}

	// 零值 registry 懒初始化且 nil opener 返回的 db 必须拒绝。
	zero := &Registry{}
	if _, err := zero.AcquireWith(func() (*sql.DB, error) { return nil, nil }, "x", "jobs", 4, 2); err == nil {
		t.Fatal("opener 返回空 db 必须拒绝")
	}
	if err := zero.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestW7DConcurrentAcquireDedupesToOnePool(t *testing.T) {
	registry := NewRegistry()
	const workers = 24
	var openCount atomic.Int64
	handles := make([]*Handle, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := range handles {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			handles[index], errs[index] = registry.AcquireWith(func() (*sql.DB, error) {
				openCount.Add(1)
				return w7dOpenScriptedDB(t), nil
			}, "scripted://dedupe", "jobs", 4, 2)
		}(index)
	}
	close(start)
	group.Wait()
	var first *sql.DB
	for index, handle := range handles {
		if errs[index] != nil {
			t.Fatal(errs[index])
		}
		if first == nil {
			first = handle.DB()
		} else if handle.DB() != first {
			t.Fatal("并发 Acquire 必须去重到同一连接池")
		}
	}
	if openCount.Load() != 1 {
		t.Fatalf("opener 必须只执行一次: %d", openCount.Load())
	}
	if snapshots := registry.Stats(); len(snapshots) != 1 || snapshots[0].Refs != workers {
		t.Fatalf("引用计数不正确: %#v", registry.Stats())
	}
	for _, handle := range handles {
		if err := handle.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if len(registry.Stats()) != 0 {
		t.Fatal("全部释放后必须没有残留池")
	}
}

func TestW7DRegistryCloseClosesAllPools(t *testing.T) {
	registry := NewRegistry()
	first, err := registry.AcquireWith(func() (*sql.DB, error) { return w7dOpenScriptedDB(t), nil }, "scripted://close-a", "jobs", 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.AcquireWith(func() (*sql.DB, error) { return w7dOpenScriptedDB(t), nil }, "scripted://close-b", "jobs", 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.DB().Ping(); err == nil {
		t.Fatal("registry.Close 后池必须关闭")
	}
	if err := second.DB().Ping(); err == nil {
		t.Fatal("registry.Close 后第二个池也必须关闭")
	}
}

// ---------------------------------------------------------------------------
// 门禁真实 PG 双臂：设置 W7D_TEST_POSTGRES_DSN 时验证真实拨号/Ping
// ---------------------------------------------------------------------------

func TestW7DRealPostgresDialWhenGated(t *testing.T) {
	dsn := envW7DPGDSN()
	if dsn == "" {
		t.Skip("W7D_TEST_POSTGRES_DSN 未设置，跳过真实 PG 臂")
	}
	registry := NewRegistry()
	handle, err := registry.Acquire("pgx", dsn, "jobs", 2, 1)
	if err != nil {
		t.Fatalf("真实 PG 构造失败: %v", err)
	}
	defer func() { _ = handle.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := handle.DB().PingContext(ctx); err != nil {
		t.Fatalf("真实 PG Ping 失败: %v", err)
	}
	if snapshots := registry.Stats(); len(snapshots) != 1 || snapshots[0].Role != "jobs" {
		t.Fatalf("真实 PG stats 不正确: %#v", registry.Stats())
	}
}

// envW7DPGDSN 只从环境读取门禁 DSN，连接串绝不写入源码、文档或日志。
func envW7DPGDSN() string {
	value := os.Getenv("W7D_TEST_POSTGRES_DSN")
	// 基本形状校验，避免误配置打到非 PG 服务。
	if len(value) < len("postgres://") || value[:11] != "postgres://" {
		return ""
	}
	return value
}
