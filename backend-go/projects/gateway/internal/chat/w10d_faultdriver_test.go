package chat

// w10d 覆盖收尾：脚本化驱动层故障注入。
//
// 通过 sqlite.NewConnector 的连接 interposition 包装物理连接，按查询子串注入
// 一次性/持续失败，用于驱动 store 层真实可失败的 DB 错误臂（断连以外的
// 单条语句失败路径）。事务边界（BeginTx/Commit/Rollback）单独包一层，
// 以覆盖提交/回滚失败臂。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"

	sqlite "modernc.org/sqlite"
)

// errInjectedW10D 是故障脚本返回的统一错误。
var errInjectedW10D = errors.New("w10d 注入故障")

// commitFaultKeyW10D / rollbackFaultKeyW10D 是事务边界故障的特殊匹配键。
const (
	commitFaultKeyW10D   = "\x00w10d-commit"
	rollbackFaultKeyW10D = "\x00w10d-rollback"
)

// faultScriptW10D 记录按查询子串匹配的故障规则：rules 持续命中，once 命中一次即出队，
// nth 在该子串第 n 次命中时失败（用于区分同一子串在单次流程中的多次出现）。
type faultScriptW10D struct {
	mu    sync.Mutex
	rules map[string]error
	once  map[string][]error
	nth   map[string][]int
	hits  map[string]int
}

func newFaultScriptW10D() *faultScriptW10D {
	return &faultScriptW10D{rules: map[string]error{}, once: map[string][]error{}, nth: map[string][]int{}, hits: map[string]int{}}
}

// failOnce 注册一次性失败：首个包含 substr 的查询命中后弹出一条。
func (s *faultScriptW10D) failOnce(substr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.once[substr] = append(s.once[substr], errInjectedW10D)
}

// fail 注册持续失败：每次命中都失败，测试结束由 fixture 各自持有脚本回收。
func (s *faultScriptW10D) fail(substr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules[substr] = errInjectedW10D
}

// failNth 注册该子串第 n 次命中（n 从 1 开始）的失败；之前的命中放行。
func (s *faultScriptW10D) failNth(substr string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nth[substr] = append(s.nth[substr], n)
}

// take 返回该查询命中的故障；一次性命中即出队，第 n 次命中按计数判断。
func (s *faultScriptW10D) take(query string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for substr, queue := range s.once {
		if strings.Contains(query, substr) && len(queue) > 0 {
			err := queue[0]
			s.once[substr] = queue[1:]
			if len(s.once[substr]) == 0 {
				delete(s.once, substr)
			}
			return err
		}
	}
	for substr, err := range s.rules {
		if strings.Contains(query, substr) {
			return err
		}
	}
	for substr, targets := range s.nth {
		if strings.Contains(query, substr) {
			s.hits[substr]++
			for i, n := range targets {
				if n == s.hits[substr] {
					s.nth[substr] = append(targets[:i], targets[i+1:]...)
					return errInjectedW10D
				}
			}
		}
	}
	return nil
}

// faultConnectorW10D 包装 sqlite connector，在物理连接上叠加故障脚本。
type faultConnectorW10D struct {
	base   driver.Connector
	script *faultScriptW10D
}

func (c faultConnectorW10D) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &faultConnW10D{base: conn, script: c.script}, nil
}

func (c faultConnectorW10D) Driver() driver.Driver { return c.base.Driver() }

// faultConnW10D 只在 ExecContext/QueryContext 上注入；Prepare/Begin 直通，
// 事务边界交给 faultTxW10D。
type faultConnW10D struct {
	base   driver.Conn
	script *faultScriptW10D
}

func (c *faultConnW10D) Prepare(query string) (driver.Stmt, error) { return c.base.Prepare(query) }

func (c *faultConnW10D) Close() error { return c.base.Close() }

func (c *faultConnW10D) Begin() (driver.Tx, error) {
	tx, err := c.base.Begin()
	if err != nil {
		return nil, err
	}
	return &faultTxW10D{base: tx, script: c.script}, nil
}

func (c *faultConnW10D) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	var (
		tx  driver.Tx
		err error
	)
	if bt, ok := c.base.(driver.ConnBeginTx); ok {
		tx, err = bt.BeginTx(ctx, opts)
	} else {
		tx, err = c.base.Begin()
	}
	if err != nil {
		return nil, err
	}
	return &faultTxW10D{base: tx, script: c.script}, nil
}

func (c *faultConnW10D) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.script.take(query); err != nil {
		return nil, err
	}
	return c.base.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *faultConnW10D) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.script.take(query); err != nil {
		return nil, err
	}
	return c.base.(driver.QueryerContext).QueryContext(ctx, query, args)
}

// faultTxW10D 在 Commit/Rollback 上叠加故障脚本。
type faultTxW10D struct {
	base   driver.Tx
	script *faultScriptW10D
}

func (t *faultTxW10D) Commit() error {
	if err := t.script.take(commitFaultKeyW10D); err != nil {
		// 注入的提交失败不会到达底层：回滚底层事务，保持共享连接干净。
		_ = t.base.Rollback()
		return err
	}
	return t.base.Commit()
}

func (t *faultTxW10D) Rollback() error {
	if err := t.script.take(rollbackFaultKeyW10D); err != nil {
		return err
	}
	return t.base.Rollback()
}

// newFaultChatFixtureW10D 与 newChatFixture 等价，但物理连接经过故障脚本。
func newFaultChatFixtureW10D(t *testing.T) (*chatFixture, *faultScriptW10D) {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	base, err := sqlite.NewConnector("file:chat-w10d-" + name + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	script := newFaultScriptW10D()
	db := sql.OpenDB(faultConnectorW10D{base: base, script: script})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range strings.Split(chatTestDDL, ";") {
		trimmed := strings.TrimSpace(statement)
		if trimmed == "" {
			continue
		}
		if _, err := db.Exec(trimmed); err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	_, clock := fixedChatClock()
	store, err := NewStore(db, false, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &chatFixture{t: t, db: db, store: store, nowISO: "2026-03-10T08:00:00.000Z"}, script
}

// newFaultGenerationEnvW10D 与 newGenerationEnv 等价，但存储层经过故障脚本。
func newFaultGenerationEnvW10D(t *testing.T) (*generationEnv, *faultScriptW10D) {
	t.Helper()
	fixture, script := newFaultChatFixtureW10D(t)
	return buildGenerationEnvW10D(t, fixture), script
}
