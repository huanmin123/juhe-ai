package sqlpool

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

var errW9ASQLPool = errors.New("w9a sqlpool 注入失败")

// w9aCloseErrConn 的 Close 返回错误，用于触发 Handle.Close 的错误传播。
type w9aCloseErrConn struct{}

func (w9aCloseErrConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w9a: Prepare 未实现")
}
func (w9aCloseErrConn) Close() error              { return errW9ASQLPool }
func (w9aCloseErrConn) Begin() (driver.Tx, error) { return nil, driver.ErrSkip }

type w9aCloseErrConnector struct{}

func (w9aCloseErrConnector) Connect(context.Context) (driver.Conn, error) {
	return w9aCloseErrConn{}, nil
}
func (w9aCloseErrConnector) Driver() driver.Driver { return w9aNopDriver{} }

type w9aNopDriver struct{}

func (w9aNopDriver) Open(string) (driver.Conn, error) { return nil, errors.New("w9a: 使用 OpenDB") }

// TestW9ANilRegistryAndHandleReceiver 覆盖 nil 接收者的防御分支。
func TestW9ANilRegistryAndHandleReceiver(t *testing.T) {
	var registry *Registry
	registry.SetObserver(nil)
	if registry.Stats() != nil {
		t.Fatal("nil registry Stats 必须返回 nil")
	}
	if _, err := registry.Acquire(func() (*sql.DB, error) { return nil, nil }, "url", "role", 2, 1); err == nil ||
		!strings.Contains(err.Error(), "未初始化") {
		t.Fatalf("nil registry Acquire 必须报错: %v", err)
	}
	if err := registry.Close(); err != nil {
		t.Fatalf("nil registry Close 必须返回 nil: %v", err)
	}

	var handle *Handle
	if handle.DB() != nil {
		t.Fatal("nil handle DB 必须返回 nil")
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("nil handle Close 必须返回 nil: %v", err)
	}
}

// TestW9AAcquireArgumentValidation 覆盖参数校验分支。
func TestW9AAcquireArgumentValidation(t *testing.T) {
	registry := NewRegistry()
	if _, err := registry.Acquire(nil, "", "", 0, 0); err == nil || !strings.Contains(err.Error(), "不能为空") {
		t.Fatalf("空参数必须报错: %v", err)
	}
	if _, err := registry.Acquire(func() (*sql.DB, error) { return nil, nil }, "url", "role", 0, 1); err == nil ||
		!strings.Contains(err.Error(), "max open/idle") {
		t.Fatalf("非法上限必须报错: %v", err)
	}
	if _, err := registry.Acquire(func() (*sql.DB, error) { return nil, nil }, "url", "role", 2, 5); err == nil {
		t.Fatal("idle > open 必须报错")
	}
}

// TestW9AAcquireOpenerFailures 覆盖 opener 失败与返回空连接分支。
func TestW9AAcquireOpenerFailures(t *testing.T) {
	registry := NewRegistry()
	if _, err := registry.Acquire(func() (*sql.DB, error) { return nil, errW9ASQLPool }, "url", "role", 2, 1); !errors.Is(err, errW9ASQLPool) {
		t.Fatalf("opener 错误必须传播: %v", err)
	}
	if _, err := registry.Acquire(func() (*sql.DB, error) { return nil, nil }, "url", "role", 2, 1); err == nil ||
		!strings.Contains(err.Error(), "返回空数据库连接") {
		t.Fatalf("opener 空连接必须报错: %v", err)
	}
}

// TestW9AAcquireReuseGrowsMaxOpen 覆盖复用时的扩容与 observer 事件。
func TestW9AAcquireReuseGrowsMaxOpen(t *testing.T) {
	registry := NewRegistry()
	events := make([]PoolEvent, 0, 4)
	registry.SetObserver(func(event PoolEvent) { events = append(events, event) })

	first, err := registry.Acquire(func() (*sql.DB, error) { return sql.OpenDB(w9aCloseErrConnector{}), nil }, "url", "role", 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Acquire(func() (*sql.DB, error) {
		t.Fatal("复用路径不得重新 open")
		return nil, nil
	}, "url", "role", 5, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Kind != PoolEventOpen || events[1].Kind != PoolEventReuse {
		t.Fatalf("事件序列 = %+v", events)
	}
	if second.DB().Stats().MaxOpenConnections != 5 {
		t.Fatalf("复用必须扩容 max open: %d", second.DB().Stats().MaxOpenConnections)
	}
	snapshots := registry.Stats()
	if len(snapshots) != 1 || snapshots[0].Refs != 2 {
		t.Fatalf("Stats = %+v", snapshots)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("release 引用必须成功: %v", err)
	}
	if got := registry.Stats()[0].Refs; got != 1 {
		t.Fatalf("release 后引用 = %d", got)
	}
	// database/sql 的 DB.Close 不传播 driver conn 关闭错误，因此最终关闭成功返回。
	if err := second.Close(); err != nil {
		t.Fatalf("最终关闭必须成功: %v", err)
	}
	if got := registry.Stats(); len(got) != 0 {
		t.Fatalf("最终关闭后必须移除条目: %+v", got)
	}
}

// TestW9AHandleCloseOnUnknownKey 覆盖条目已不存在时的幂等关闭。
func TestW9AHandleCloseOnUnknownKey(t *testing.T) {
	registry := NewRegistry()
	handle, err := registry.Acquire(func() (*sql.DB, error) { return sql.OpenDB(w9aCloseErrConnector{}), nil }, "url", "role", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Close(); err != nil {
		t.Fatalf("registry.Close 必须成功: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("条目已删除后的 Close 必须幂等: %v", err)
	}
	// registry 与 handle 指向同一批条目：registry nil entries 的防御分支。
	empty := &Registry{}
	emptyRegistryHandle := &Handle{registry: empty, key: Key{URL: "u", Role: "r"}, db: nil}
	if err := emptyRegistryHandle.Close(); err != nil {
		t.Fatalf("空 registry handle Close 必须幂等: %v", err)
	}
}
