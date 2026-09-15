package main

// w1 波次收尾单元测试（w1y 前缀，TestW1Y 入口）：按覆盖率画像补齐
// chain_chat_keys.go / chain_chat_observation.go / chain_chat.go /
// chain_chat_mount.go / chain_chat_images.go 的剩余可确定性构造分支。
//
// 已知发现（证据见对应测试）：
//   - chain_chat_keys.go nextDefaultApiKeyName / nextDefaultRouteStrategyName
//     的 `LIKE ? ESCAPE '\\'`（Go 原始字符串中是两个反斜杠字符）在
//     modernc.org/sqlite 上，凡是候选行真正进入 LIKE 求值时就报
//     `SQL logic error: ESCAPE expression must be a single character (1)`。
//     仅当没有同 owner 候选行（或 `name = ?` 短路命中）时才幸免。
//   - chain_chat_mount.go openChatDatabase 的 pgDialect 分支以
//     Acquire(url, "gateway-chat", 0, 0) 取池，而 sqlpool.ValidatePoolLimits
//     要求 1 <= idle <= min(open, 10)，0/0 恒被拒绝，PostgreSQL 模式下
//     chat 数据库永远打不开（成功返回行 218 不可达）。
//
// 按波次约定跳过：Schedule/track/Wait/runObservation（goroutine + time.After
// 类）、chatAttachStreamHandler 心跳循环、Dispatch ctx.Done 与 status==0 分支
// 中依赖真实并发的部分、依赖 image.Decode 注册表之外格式的分支（当前二进制
// 只注册 jpeg/png/gif/webp 四种受支持格式）。

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sqlite "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
)

// ---------------------------------------------------------------------------
// 通用小工具
// ---------------------------------------------------------------------------

func w1yExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("执行 seed 语句失败: %v\nSQL: %s", err, query)
	}
}

func w1yOpenSQLite(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), name+".sqlite3"))
	if err != nil {
		t.Fatalf("打开 sqlite %s: %v", name, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func w1yOpenClosedSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "closed.sqlite3"))
	if err != nil {
		t.Fatalf("打开待关闭 sqlite: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭 sqlite: %v", err)
	}
	return db
}

// w1yKeysDB 建业务面 chat key 相关四表（列集与 w1 波次既有 fixture 一致，
// provider_code 允许 NULL 以构造 Scan 错误分支）。
func w1yKeysDB(t *testing.T) *sql.DB {
	t.Helper()
	db := w1yOpenSQLite(t, "w1y-keys")
	for _, ddl := range []string{
		`CREATE TABLE groups (
			id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT,
			provider_code TEXT, enabled INTEGER DEFAULT 1, is_default INTEGER DEFAULT 0,
			created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE route_strategies (
			id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT, description TEXT,
			mode TEXT, status TEXT DEFAULT 'active', is_default INTEGER DEFAULT 0,
			config_json TEXT, created_at TEXT, updated_at TEXT,
			UNIQUE(system_account_id, name))`,
		`CREATE TABLE route_strategy_groups (
			id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL,
			group_id TEXT NOT NULL, priority INTEGER, weight INTEGER, status TEXT DEFAULT 'active',
			created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE api_keys (
			id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, route_strategy_id TEXT,
			name TEXT NOT NULL, description TEXT, key_hash TEXT, key_prefix TEXT, key_suffix TEXT,
			key_secret_encrypted TEXT, status TEXT DEFAULT 'active', is_default INTEGER DEFAULT 0,
			purpose TEXT, expires_at TEXT, quota_limits_json TEXT, availability_schedule_json TEXT,
			availability_schedule_next_check_at TEXT, created_at TEXT, updated_at TEXT,
			UNIQUE(system_account_id, name))`,
		`CREATE UNIQUE INDEX idx_api_keys_chat_purpose_unique
			ON api_keys(system_account_id, purpose) WHERE purpose = 'chat'`,
	} {
		w1yExec(t, db, ddl)
	}
	return db
}

// w1ySeedGPTDefault 预置一个 provider_code='gpt' 的默认分组 + 默认策略路由 +
// 绑定行，使 ensureDefaultRouteStrategies 命中"已存在"跳过分支、
// defaultGptRouteStrategyForSystemAccount 能 join 出结果。
func w1ySeedGPTDefault(t *testing.T, db *sql.DB, owner string) {
	t.Helper()
	now := "2026-09-14T12:00:00.000Z"
	w1yExec(t, db, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, created_at, updated_at)
		VALUES ('grp-w1y-gpt', ?, 'GPT 分组', 'gpt', 1, 1, ?, ?)`, owner, now, now)
	w1yExec(t, db, `INSERT INTO route_strategies (id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at)
		VALUES ('rs-w1y-gpt', ?, 'GPT 路由', 'w1y 预置', 'normal', 'active', 1, NULL, ?, ?)`, owner, now, now)
	w1yExec(t, db, `INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
		VALUES ('rsg-w1y-gpt', 'rs-w1y-gpt', ?, 'grp-w1y-gpt', 1, 1, 'active', ?, ?)`, owner, now, now)
}

// ---------------------------------------------------------------------------
// 可注入行为的 sqlite 驱动包装：在指定次数的 chat key 探针查询上注入失败 /
// 额外结果行，在 api_keys INSERT 上注入唯一约束错误，从而在单线程测试里
// 确定性地复现 Node 并发 ensure 竞态恢复路径。
// ---------------------------------------------------------------------------

type w1yProbeBehavior struct {
	failProbeCall     int    // 第几次 purpose='chat' 探针查询返回失败（0 = 不失败）
	probeError        error  // 探针失败时返回的错误
	injectProbeCall   int    // 第几次探针查询追加一行伪造结果（0 = 不注入）
	injectedKeyID     string // 伪造结果行的 id 列值
	failInsertKeys    bool   // 是否让 INSERT INTO api_keys 直接失败
	insertError       error  // INSERT 失败时返回的错误
	injectGPTStrategy bool   // 是否为 GPT 默认策略查询注入一行结果（绕开其 JOIN SQL 缺陷）
	gptStrategyID     string // 注入的 GPT 策略行 id
}

type w1yProbeDriver struct {
	mu    sync.Mutex
	hooks map[string]*w1yProbeBehavior
}

var w1ySharedProbe = &w1yProbeDriver{hooks: map[string]*w1yProbeBehavior{}}

func init() {
	sql.Register("w1y-sqlite-probe", w1ySharedProbe)
}

func (d *w1yProbeDriver) Open(name string) (driver.Conn, error) {
	d.mu.Lock()
	behavior := d.hooks[name]
	d.mu.Unlock()
	inner, err := (&sqlite.Driver{}).Open(name)
	if err != nil {
		return nil, err
	}
	return &w1yProbeConn{inner: inner, behavior: behavior}, nil
}

type w1yProbeConn struct {
	inner      driver.Conn
	behavior   *w1yProbeBehavior
	probeCalls int
}

func (c *w1yProbeConn) Prepare(query string) (driver.Stmt, error) { return c.inner.Prepare(query) }
func (c *w1yProbeConn) Close() error                              { return c.inner.Close() }
func (c *w1yProbeConn) Begin() (driver.Tx, error)                 { return c.inner.Begin() }

func (c *w1yProbeConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.behavior != nil && c.behavior.failInsertKeys && c.behavior.insertError != nil &&
		strings.Contains(query, "INSERT INTO api_keys") {
		return nil, c.behavior.insertError
	}
	execer, ok := c.inner.(driver.ExecerContext)
	if !ok {
		return nil, errors.New("w1y probe 驱动包装要求底层连接实现 ExecerContext")
	}
	return execer.ExecContext(ctx, query, args)
}

func (c *w1yProbeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	isProbe := strings.Contains(query, "purpose = 'chat'")
	injectGPT := c.behavior != nil && c.behavior.injectGPTStrategy && strings.Contains(query, "provider_code = ?")
	if isProbe && c.behavior != nil {
		c.probeCalls++
		if c.behavior.failProbeCall == c.probeCalls && c.behavior.probeError != nil {
			return nil, c.behavior.probeError
		}
	}
	// GPT 默认策略查询在真实 SQLite 上 prepare 即报 JOIN 缺陷错误（见
	// TestW1YDefaultGptRouteStrategyJoinDefect），注入必须在委托执行前短路。
	if injectGPT {
		return &w1ySliceRows{
			cols: []string{"id", "name"},
			rows: [][]driver.Value{{c.behavior.gptStrategyID, "GPT 路由"}},
		}, nil
	}
	queryer, ok := c.inner.(driver.QueryerContext)
	if !ok {
		return nil, errors.New("w1y probe 驱动包装要求底层连接实现 QueryerContext")
	}
	rows, err := queryer.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	if !(isProbe && c.behavior != nil && c.behavior.injectProbeCall == c.probeCalls) {
		return rows, nil
	}
	cols := rows.Columns()
	values, err := w1yMaterializeRows(rows)
	if err != nil {
		return nil, err
	}
	values = append(values, []driver.Value{c.behavior.injectedKeyID})
	return &w1ySliceRows{cols: cols, rows: values}, nil
}

// w1yMaterializeRows 把委托驱动 Rows 读成内存行。
func w1yMaterializeRows(rows driver.Rows) ([][]driver.Value, error) {
	cols := rows.Columns()
	values := [][]driver.Value{}
	for {
		row := make([]driver.Value, len(cols))
		if err := rows.Next(row); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			_ = rows.Close()
			return nil, err
		}
		values = append(values, row)
	}
	_ = rows.Close()
	return values, nil
}

// w1ySliceRows 是物化结果行 + 可选伪造行的最小 driver.Rows。
type w1ySliceRows struct {
	cols []string
	rows [][]driver.Value
	pos  int
}

func (r *w1ySliceRows) Columns() []string { return r.cols }
func (r *w1ySliceRows) Close() error      { return nil }
func (r *w1ySliceRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.pos])
	r.pos++
	return nil
}

func w1yOpenProbeDB(t *testing.T, name string, behavior *w1yProbeBehavior) *sql.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), name+".sqlite3")
	w1ySharedProbe.mu.Lock()
	w1ySharedProbe.hooks[dsn] = behavior
	w1ySharedProbe.mu.Unlock()
	t.Cleanup(func() {
		w1ySharedProbe.mu.Lock()
		delete(w1ySharedProbe.hooks, dsn)
		w1ySharedProbe.mu.Unlock()
	})
	db, err := sql.Open("w1y-sqlite-probe", dsn)
	if err != nil {
		t.Fatalf("打开探针 sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ---------------------------------------------------------------------------
// chain_chat_keys.go：EnsureChatAPIKey 主路径
// ---------------------------------------------------------------------------

// TestW1YDefaultGptRouteStrategyJoinDefect 锁定修复后契约（历史缺陷：GPT
// 默认策略查询的 `INNER JOIN %[1]s route_strategy_groups` 把 route_strategies
// 表自联成 route_strategy_groups 别名，SQLite 与 PostgreSQL 下都必然报
// `no such column: route_strategy_groups.route_strategy_id`，导致全新 owner
// 永远无法创建 chat API Key；修复改为独立的第三个表参数 %[3]s）。
func TestW1YDefaultGptRouteStrategyJoinDefect(t *testing.T) {
	db := w1yKeysDB(t)
	w1ySeedGPTDefault(t, db, "own-gpt")
	provider := newChatAPIKeyProvider(db, false, "w1y-secret")
	// 修复后契约（历史缺陷：JOIN 误用 route_strategies 自联导致
	// no such column: route_strategy_groups.route_strategy_id）：
	// route_strategy_groups 以独立表名参与 JOIN，默认策略可被查询命中。
	strategy, err := provider.defaultGptRouteStrategyForSystemAccount("own-gpt")
	if err != nil {
		t.Fatalf("defaultGptRouteStrategyForSystemAccount = %v, want nil", err)
	}
	if strategy == nil {
		t.Fatal("GPT 默认策略应被查询命中")
	}
	if strategy.id != "rs-w1y-gpt" || strategy.name != "GPT 路由" {
		t.Fatalf("strategy = %+v, want rs-w1y-gpt/GPT 路由", strategy)
	}
}

func TestW1YEnsureChatAPIKeyFreshOwnerCreatesStrategyThenHitsDefect(t *testing.T) {
	db := w1yKeysDB(t)
	provider := newChatAPIKeyProvider(db, false, "w1y-secret")
	w1yExec(t, db, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, created_at, updated_at)
		VALUES ('grp-w1y-fresh', 'own-fresh', 'GPT 分组', 'gpt', 1, 1, '2026-09-14T00:00:00.000Z', '2026-09-14T00:00:00.000Z')`)

	// 修复后契约（历史缺陷：GPT 默认策略 JOIN 错表曾使建键主链路全断）：
	// 全新 owner 走通 默认分组 → 默认策略路由 → 绑定 → chat key 创建全链。
	keyID, err := provider.EnsureChatAPIKey("own-fresh")
	if err != nil {
		t.Fatalf("全新 owner EnsureChatAPIKey = %v, want nil", err)
	}

	// 默认策略路由与绑定行存在，api_keys 落行。
	var strategyName string
	if err := db.QueryRow(`SELECT name FROM route_strategies WHERE system_account_id = 'own-fresh'`).Scan(&strategyName); err != nil {
		t.Fatalf("回读默认策略路由: %v", err)
	}
	if strategyName != "GPT 路由" {
		t.Errorf("默认策略路由名 = %q, want GPT 路由（分组名去 分组 后缀）", strategyName)
	}
	var bindingCount, keyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM route_strategy_groups WHERE system_account_id = 'own-fresh'`).Scan(&bindingCount); err != nil {
		t.Fatalf("回读策略分组绑定: %v", err)
	}
	if bindingCount != 1 {
		t.Errorf("route_strategy_groups 行数 = %d, want 1", bindingCount)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE system_account_id = 'own-fresh'`).Scan(&keyCount); err != nil {
		t.Fatalf("回读 api_keys: %v", err)
	}
	if keyCount != 1 {
		t.Errorf("api_keys 行数 = %d, want 1（建键主链路修复后应落行）", keyCount)
	}
	if keyID == "" {
		t.Fatal("EnsureChatAPIKey 应返回新键 id")
	}
}

func TestW1YEnsureChatAPIKeyGuardErrors(t *testing.T) {
	db := w1yKeysDB(t)
	provider := newChatAPIKeyProvider(db, false, "w1y-secret")

	if _, err := provider.EnsureChatAPIKey("own-nogroups"); err == nil ||
		!strings.Contains(err.Error(), "创建默认策略路由前必须先创建默认分组") {
		t.Fatalf("无默认分组 EnsureChatAPIKey 错误 = %v, want 默认分组提示", err)
	}

	w1yExec(t, db, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, created_at, updated_at)
		VALUES ('grp-w1y-openai', 'own-openai', 'OpenAI 分组', 'openai', 1, 1, '2026-09-14T00:00:00.000Z', '2026-09-14T00:00:00.000Z')`)
	// 非 gpt 分组场景：GPT 默认策略查询正常返回 no-row（JOIN 修复后不再
	// 抛 no such column），守卫提示"先创建 GPT 默认策略路由"可达。
	if _, err := provider.EnsureChatAPIKey("own-openai"); err == nil ||
		!strings.Contains(err.Error(), "创建 AI 对话 API Key 前必须先创建 GPT 默认策略路由") {
		t.Fatalf("无 GPT 默认策略 EnsureChatAPIKey = %v, want GPT 默认策略守卫提示", err)
	}
	var openaiStrategyName string
	if err := db.QueryRow(`SELECT name FROM route_strategies WHERE system_account_id = 'own-openai'`).Scan(&openaiStrategyName); err != nil {
		t.Fatalf("回读 OpenAI 默认策略路由: %v", err)
	}
	if openaiStrategyName != "OpenAI 路由" {
		t.Errorf("OpenAI 默认策略路由名 = %q, want OpenAI 路由", openaiStrategyName)
	}

	w1ySeedGPTDefault(t, db, "own-existing")
	key := apikeys.NewAPIKey()
	sealed, err := apikeys.EncryptJSON("w1y-secret", map[string]string{"key": key})
	if err != nil {
		t.Fatalf("EncryptJSON: %v", err)
	}
	w1yExec(t, db, `INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
		key_secret_encrypted, status, is_default, purpose, created_at, updated_at)
		VALUES ('key-w1y-existing', 'own-existing', 'rs-w1y-gpt', 'AI 对话 API Key', 'w1y', ?, ?, ?, ?, 'active', 0, 'chat', 'now', 'now')`,
		apikeys.HashSecret(key), key[:8], key[len(key)-8:], sealed)
	got, err := provider.EnsureChatAPIKey("own-existing")
	if err != nil {
		t.Fatalf("已有 chat key EnsureChatAPIKey = %v, want nil", err)
	}
	if got != "key-w1y-existing" {
		t.Errorf("已有 chat key 复用 = %q, want key-w1y-existing", got)
	}
}

func TestW1YEnsureChatAPIKeyProbeFailure(t *testing.T) {
	db := w1yOpenProbeDB(t, "probe-fail", &w1yProbeBehavior{
		failProbeCall: 1,
		probeError:    errors.New("w1y 强制探针失败"),
	})
	w1yKeysSchemaOnProbe(t, db)
	w1ySeedGPTDefault(t, db, "own-probe")
	provider := newChatAPIKeyProvider(db, false, "w1y-secret")
	if _, err := provider.EnsureChatAPIKey("own-probe"); err == nil ||
		!strings.Contains(err.Error(), "w1y 强制探针失败") {
		t.Fatalf("chat key 探针失败 EnsureChatAPIKey = %v, want 探针错误透传", err)
	}
}

func TestW1YEnsureChatAPIKeyInsertConflictReusesRacedKey(t *testing.T) {
	db := w1yOpenProbeDB(t, "probe-race", &w1yProbeBehavior{
		failInsertKeys:    true,
		insertError:       errors.New("UNIQUE constraint failed: idx_api_keys_chat_purpose_unique"),
		injectProbeCall:   2,
		injectedKeyID:     "key-w1y-raced",
		injectGPTStrategy: true, // 绕开 GPT 默认策略查询的 JOIN 缺陷（见 TestW1YDefaultGptRouteStrategyJoinDefect）
		gptStrategyID:     "rs-w1y-gpt",
	})
	w1yKeysSchemaOnProbe(t, db)
	w1ySeedGPTDefault(t, db, "own-race")
	provider := newChatAPIKeyProvider(db, false, "w1y-secret")
	got, err := provider.EnsureChatAPIKey("own-race")
	if err != nil {
		t.Fatalf("唯一冲突竞态恢复 EnsureChatAPIKey = %v, want nil", err)
	}
	if got != "key-w1y-raced" {
		t.Errorf("竞态恢复复用 = %q, want key-w1y-raced", got)
	}
}

func TestW1YEnsureChatAPIKeyInsertConflictWithoutWinner(t *testing.T) {
	db := w1yOpenProbeDB(t, "probe-conflict", &w1yProbeBehavior{
		failInsertKeys:    true,
		insertError:       errors.New("UNIQUE constraint failed: idx_api_keys_chat_purpose_unique"),
		injectGPTStrategy: true,
		gptStrategyID:     "rs-w1y-gpt",
	})
	w1yKeysSchemaOnProbe(t, db)
	w1ySeedGPTDefault(t, db, "own-conflict")
	provider := newChatAPIKeyProvider(db, false, "w1y-secret")
	if _, err := provider.EnsureChatAPIKey("own-conflict"); err == nil ||
		!strings.Contains(err.Error(), "idx_api_keys_chat_purpose_unique") {
		t.Fatalf("无竞态胜者 EnsureChatAPIKey 错误 = %v, want 唯一冲突原始错误", err)
	}
}

// TestW1YEnsureChatAPIKeyFullSuccessWithInjectedGPTStrategy 在注入 GPT 默认
// 策略行绕开 JOIN 缺陷后，验证新建链路剩余步骤全部走通：命名、加密信封、
// api_keys 落行（JOIN 缺陷修复后这就是生产行为）。
func TestW1YEnsureChatAPIKeyFullSuccessWithInjectedGPTStrategy(t *testing.T) {
	db := w1yOpenProbeDB(t, "probe-success", &w1yProbeBehavior{
		injectGPTStrategy: true,
		gptStrategyID:     "rs-w1y-gpt",
	})
	w1yKeysSchemaOnProbe(t, db)
	w1ySeedGPTDefault(t, db, "own-success")
	provider := newChatAPIKeyProvider(db, false, "w1y-secret")
	keyID, err := provider.EnsureChatAPIKey("own-success")
	if err != nil {
		t.Fatalf("注入 GPT 策略后 EnsureChatAPIKey = %v, want nil", err)
	}
	if !strings.HasPrefix(keyID, "key_") {
		t.Fatalf("keyID = %q, want key_ 前缀", keyID)
	}
	var name, purpose, strategyID string
	if err := db.QueryRow(`SELECT name, purpose, route_strategy_id FROM api_keys WHERE id = ?`, keyID).
		Scan(&name, &purpose, &strategyID); err != nil {
		t.Fatalf("回读 chat key 行: %v", err)
	}
	if name != "AI 对话 API Key" || purpose != "chat" || strategyID != "rs-w1y-gpt" {
		t.Errorf("chat key 行 = (%q, %q, %q), want (AI 对话 API Key, chat, rs-w1y-gpt)", name, purpose, strategyID)
	}
}

// TestW1YEnsureDefaultRouteStrategiesBindingInsertError 用 CHECK 约束让绑定行
// 插入失败，覆盖 ensureDefaultRouteStrategies 的错误传播分支。
func TestW1YEnsureDefaultRouteStrategiesBindingInsertError(t *testing.T) {
	db := w1yKeysDB(t)
	w1yExec(t, db, `DROP TABLE route_strategy_groups`)
	w1yExec(t, db, `CREATE TABLE route_strategy_groups (
		id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL,
		group_id TEXT NOT NULL, priority INTEGER, weight INTEGER, status TEXT DEFAULT 'active',
		created_at TEXT, updated_at TEXT, CHECK (weight > 100))`)
	w1yExec(t, db, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, created_at, updated_at)
		VALUES ('grp-w1y-check', 'own-check', '检查分组', 'gpt', 1, 1, 'now', 'now')`)
	provider := newChatAPIKeyProvider(db, false, "w1y-secret")
	if err := provider.ensureDefaultRouteStrategies("own-check", "2026-09-14T00:00:00.000Z"); err == nil {
		t.Fatalf("绑定行 CHECK 失败时 ensureDefaultRouteStrategies 未报错")
	}
	var strategyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM route_strategies WHERE system_account_id = 'own-check'`).Scan(&strategyCount); err != nil {
		t.Fatalf("回读策略行数: %v", err)
	}
	if strategyCount != 1 {
		t.Errorf("策略行数 = %d, want 1（策略插入成功、绑定插入失败）", strategyCount)
	}
}

// w1yKeysSchemaOnProbe 在探针包装驱动上建与 w1yKeysDB 相同的表。
func w1yKeysSchemaOnProbe(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, ddl := range []string{
		`CREATE TABLE groups (
			id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT,
			provider_code TEXT, enabled INTEGER DEFAULT 1, is_default INTEGER DEFAULT 0,
			created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE route_strategies (
			id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT, description TEXT,
			mode TEXT, status TEXT DEFAULT 'active', is_default INTEGER DEFAULT 0,
			config_json TEXT, created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE route_strategy_groups (
			id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL,
			group_id TEXT NOT NULL, priority INTEGER, weight INTEGER, status TEXT DEFAULT 'active',
			created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE api_keys (
			id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, route_strategy_id TEXT,
			name TEXT NOT NULL, description TEXT, key_hash TEXT, key_prefix TEXT, key_suffix TEXT,
			key_secret_encrypted TEXT, status TEXT DEFAULT 'active', is_default INTEGER DEFAULT 0,
			purpose TEXT, expires_at TEXT, quota_limits_json TEXT, availability_schedule_json TEXT,
			availability_schedule_next_check_at TEXT, created_at TEXT, updated_at TEXT,
			UNIQUE(system_account_id, name))`,
	} {
		w1yExec(t, db, ddl)
	}
}

// ---------------------------------------------------------------------------
// chain_chat_keys.go：FindChatAPIKey 剩余分支 + 关闭句柄错误臂 + 命名生成
// ---------------------------------------------------------------------------

func TestW1YFindChatAPIKeyInvalidSecretArms(t *testing.T) {
	db := w1yKeysDB(t)
	provider := newChatAPIKeyProvider(db, false, "w1y-secret")

	// 信封解密失败。
	w1yExec(t, db, `INSERT INTO api_keys (id, system_account_id, name, key_secret_encrypted, status, purpose, created_at, updated_at)
		VALUES ('key-w1y-bad', 'own-bad', '坏信封', 'w1y-not-an-envelope', 'active', 'chat', 'now', 'now')`)
	if _, err := provider.FindChatAPIKey("key-w1y-bad", "own-bad"); err == nil ||
		!strings.Contains(err.Error(), "密钥数据无效") {
		t.Fatalf("坏信封 FindChatAPIKey = %v, want 密钥数据无效", err)
	}

	// 解密成功但 key 字段为空。
	sealed, err := apikeys.EncryptJSON("w1y-secret", map[string]string{"other": "value"})
	if err != nil {
		t.Fatalf("EncryptJSON: %v", err)
	}
	w1yExec(t, db, `INSERT INTO api_keys (id, system_account_id, name, key_secret_encrypted, status, purpose, created_at, updated_at)
		VALUES ('key-w1y-empty', 'own-empty', '空明文', ?, 'active', 'chat', 'now', 'now')`, sealed)
	if _, err := provider.FindChatAPIKey("key-w1y-empty", "own-empty"); err == nil ||
		!strings.Contains(err.Error(), "密钥数据无效") {
		t.Fatalf("空明文 FindChatAPIKey = %v, want 密钥数据无效", err)
	}
}

func TestW1YChatKeysClosedDBArms(t *testing.T) {
	db := w1yOpenClosedSQLite(t)
	provider := newChatAPIKeyProvider(db, false, "w1y-secret")

	if _, err := provider.EnsureChatAPIKey("own-closed"); err == nil {
		t.Fatalf("关闭句柄 EnsureChatAPIKey 未报错")
	} else if !strings.Contains(err.Error(), "sql: database is closed") {
		t.Fatalf("关闭句柄 EnsureChatAPIKey = %v, want database is closed", err)
	}
	if _, err := provider.FindChatAPIKey("k", "own"); err == nil ||
		!strings.Contains(err.Error(), "sql: database is closed") {
		t.Fatalf("关闭句柄 FindChatAPIKey = %v, want database is closed", err)
	}
	if _, err := provider.chatApiKeyIdForSystemAccount("own"); err == nil ||
		!strings.Contains(err.Error(), "sql: database is closed") {
		t.Fatalf("关闭句柄 chatApiKeyIdForSystemAccount = %v, want database is closed", err)
	}
	if _, err := provider.defaultGptRouteStrategyForSystemAccount("own"); err == nil ||
		!strings.Contains(err.Error(), "sql: database is closed") {
		t.Fatalf("关闭句柄 defaultGptRouteStrategyForSystemAccount = %v, want database is closed", err)
	}
	if err := provider.ensureDefaultRouteStrategies("own", "2026-09-14T00:00:00.000Z"); err == nil ||
		!strings.Contains(err.Error(), "sql: database is closed") {
		t.Fatalf("关闭句柄 ensureDefaultRouteStrategies = %v, want database is closed", err)
	}
	if _, err := provider.defaultRouteStrategyIDForGroup("own", "grp"); err == nil ||
		!strings.Contains(err.Error(), "sql: database is closed") {
		t.Fatalf("关闭句柄 defaultRouteStrategyIDForGroup = %v, want database is closed", err)
	}
	if _, err := provider.defaultRouteStrategyGroups("own"); err == nil ||
		!strings.Contains(err.Error(), "sql: database is closed") {
		t.Fatalf("关闭句柄 defaultRouteStrategyGroups = %v, want database is closed", err)
	}
	if _, err := provider.nextDefaultApiKeyName("own", "AI 对话 API Key"); err == nil ||
		!strings.Contains(err.Error(), "sql: database is closed") {
		t.Fatalf("关闭句柄 nextDefaultApiKeyName = %v, want database is closed", err)
	}
	if _, err := provider.nextDefaultRouteStrategyName("own", "默认路由"); err == nil ||
		!strings.Contains(err.Error(), "sql: database is closed") {
		t.Fatalf("关闭句柄 nextDefaultRouteStrategyName = %v, want database is closed", err)
	}
}

// TestW1YNextDefaultNameLikeEscapeDefect 锁定修复后契约（历史缺陷：原始
// 字符串 `ESCAPE '\\'` 是两个反斜杠字符，SQLite 报 ESCAPE expression must
// be a single character）：LIKE 求值与精确命中一样正常返回下一序号名。
func TestW1YNextDefaultNameLikeEscapeDefect(t *testing.T) {
	db := w1yKeysDB(t)
	provider := newChatAPIKeyProvider(db, false, "w1y-secret")

	// 精确命中：OR 短路，返回下一个序号名。
	w1yExec(t, db, `INSERT INTO api_keys (id, system_account_id, name, status, created_at, updated_at)
		VALUES ('k-w1y-a', 'own-exact', 'AI 对话 API Key', 'active', 'now', 'now')`)
	got, err := provider.nextDefaultApiKeyName("own-exact", "AI 对话 API Key")
	if err != nil {
		t.Fatalf("精确命中 nextDefaultApiKeyName = %v, want nil", err)
	}
	if got != "AI 对话 API Key 2" {
		t.Errorf("精确命中下一个名字 = %q, want AI 对话 API Key 2", got)
	}

	// 同 owner 但名字不同：LIKE 求值路径（历史缺陷在此报错）应正常返回。
	w1yExec(t, db, `INSERT INTO api_keys (id, system_account_id, name, status, created_at, updated_at)
		VALUES ('k-w1y-b', 'own-other', '另一个名字', 'active', 'now', 'now')`)
	got, err = provider.nextDefaultApiKeyName("own-other", "AI 对话 API Key")
	if err != nil {
		t.Fatalf("LIKE 求值 nextDefaultApiKeyName = %v, want nil", err)
	}
	if got != "AI 对话 API Key" {
		t.Errorf("LIKE 求值下一个名字 = %q, want AI 对话 API Key（无同前缀候选）", got)
	}

	// route_strategies 同构。
	w1yExec(t, db, `INSERT INTO route_strategies (id, system_account_id, name, mode, status, is_default, created_at, updated_at)
		VALUES ('rs-w1y-exact', 'own-rs-exact', '默认路由', 'normal', 'active', 1, 'now', 'now')`)
	got, err = provider.nextDefaultRouteStrategyName("own-rs-exact", "默认路由")
	if err != nil {
		t.Fatalf("精确命中 nextDefaultRouteStrategyName = %v, want nil", err)
	}
	if got != "默认路由 2" {
		t.Errorf("精确命中下一个策略名 = %q, want 默认路由 2", got)
	}
	w1yExec(t, db, `INSERT INTO route_strategies (id, system_account_id, name, mode, status, is_default, created_at, updated_at)
		VALUES ('rs-w1y-other', 'own-rs-other', '别的策略', 'normal', 'active', 0, 'now', 'now')`)
	got, err = provider.nextDefaultRouteStrategyName("own-rs-other", "默认路由")
	if err != nil {
		t.Fatalf("LIKE 求值 nextDefaultRouteStrategyName = %v, want nil", err)
	}
	if got != "默认路由" {
		t.Errorf("LIKE 求值下一个策略名 = %q, want 默认路由（无同前缀候选）", got)
	}
}

func TestW1YDefaultRouteStrategyGroupsScanError(t *testing.T) {
	db := w1yKeysDB(t)
	provider := newChatAPIKeyProvider(db, false, "w1y-secret")
	w1yExec(t, db, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, created_at, updated_at)
		VALUES ('grp-w1y-null', 'own-nullname', '空供应商', NULL, 1, 1, 'now', 'now')`)
	if _, err := provider.defaultRouteStrategyGroups("own-nullname"); err == nil ||
		!strings.Contains(err.Error(), "converting NULL to string is unsupported") {
		t.Fatalf("NULL provider_code defaultRouteStrategyGroups = %v, want Scan 转换错误", err)
	}
}

func TestW1YChainNextDefaultNameFromExistingOverflow(t *testing.T) {
	base := "AI 对话 API Key"
	names := []string{base}
	for index := 2; index <= 1000; index++ {
		names = append(names, fmt.Sprintf("%s %d", base, index))
	}
	got := chainNextDefaultNameFromExisting(names, base)
	if !strings.HasPrefix(got, base+" ") {
		t.Fatalf("2..1000 全占用时 = %q, want base + 时间戳后缀", got)
	}
	for _, name := range names {
		if name == got {
			t.Fatalf("溢出结果 %q 与既有名字冲突", got)
		}
	}
}

func TestW1YChainDefaultRouteStrategyNameForGroupVerbatim(t *testing.T) {
	if got := chainDefaultRouteStrategyNameForGroup("生产环境"); got != "生产环境" {
		t.Fatalf("无 分组 后缀 = %q, want 原文", got)
	}
	if got := chainDefaultRouteStrategyNameForGroup("  生产分组  "); got != "生产路由" {
		t.Fatalf("带空白分组名 = %q, want 去空白后缀替换", got)
	}
}

// ---------------------------------------------------------------------------
// chain_chat_observation.go：claim / setObservation / buildAssetDataURL 错误臂
// ---------------------------------------------------------------------------

func w1yChatAssetsDB(t *testing.T) *sql.DB {
	t.Helper()
	db := w1yOpenSQLite(t, "w1y-chat-assets")
	for _, ddl := range []string{
		`CREATE TABLE chat_assets (
			id TEXT PRIMARY KEY,
			system_account_id TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			processing_status TEXT NOT NULL DEFAULT 'ready',
			cleanup_status TEXT NOT NULL DEFAULT 'active',
			expires_at TEXT NOT NULL,
			observation_status TEXT NOT NULL DEFAULT 'not_requested',
			observation_json TEXT,
			observation_revision INTEGER NOT NULL DEFAULT 0,
			observation_claim_id TEXT,
			observation_claimed_at TEXT,
			updated_at TEXT NOT NULL,
			storage_key TEXT NOT NULL DEFAULT '',
			processed_mime_type TEXT NOT NULL DEFAULT 'image/png',
			processed_bytes INTEGER NOT NULL DEFAULT 0,
			processed_sha256 TEXT NOT NULL DEFAULT '',
			source_kind TEXT NOT NULL DEFAULT 'user_uploaded')`,
		`CREATE TABLE chat_asset_references (
			asset_id TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			message_id TEXT NOT NULL,
			reference_kind TEXT NOT NULL,
			expires_at TEXT NOT NULL)`,
	} {
		w1yExec(t, db, ddl)
	}
	return db
}

func TestW1YChatObservationClosedDBArms(t *testing.T) {
	db := w1yOpenClosedSQLite(t)
	observations := &chatImageObservations{db: db, postgres: false}
	ctx := context.Background()

	if _, err := observations.claim(ctx, "ast", "conv", "sys", "turn", "msg", w1yNow()); err == nil ||
		!strings.Contains(err.Error(), "sql: database is closed") {
		t.Fatalf("关闭句柄 claim = %v, want database is closed", err)
	}
	if _, err := observations.setObservation(ctx, "ast", "conv", "sys", "failed", nil, 1, "claim", w1yNow()); err == nil ||
		!strings.Contains(err.Error(), "sql: database is closed") {
		t.Fatalf("关闭句柄 setObservation = %v, want database is closed", err)
	}
	if _, err := observations.buildAssetDataURL(ctx, "ast", "conv", "sys", w1yNow()); err == nil ||
		!strings.Contains(err.Error(), "sql: database is closed") {
		t.Fatalf("关闭句柄 buildAssetDataURL = %v, want database is closed", err)
	}
}

func w1yNow() string { return "2026-09-14T12:00:00.000Z" }

func TestW1YChatObservationArms(t *testing.T) {
	db := w1yChatAssetsDB(t)
	ctx := context.Background()

	// observation 无法 JSON 编码 → 直接报 marshal 错误。
	observations := &chatImageObservations{db: db, postgres: false}
	if _, err := observations.setObservation(ctx, "ast", "conv", "sys", "failed",
		map[string]any{"summary": make(chan int)}, 1, "claim", w1yNow()); err == nil {
		t.Fatalf("不可编码 observation setObservation 未报错")
	}

	// assistant_generated 走 generated 上限分支后，对象存储未接线 → 读失败。
	w1yExec(t, db, `INSERT INTO chat_assets (id, system_account_id, conversation_id, expires_at, updated_at, source_kind)
		VALUES ('ast-w1y-generated', 'sys', 'conv', '2027-01-01T00:00:00.000Z', 'now', 'assistant_generated')`)
	unwired := &chatImageObservations{db: db, postgres: false, objects: nil}
	if _, err := unwired.buildAssetDataURL(ctx, "ast-w1y-generated", "conv", "sys", w1yNow()); err == nil ||
		!strings.Contains(err.Error(), "chat 资产存储未接线") {
		t.Fatalf("对象存储未接线 buildAssetDataURL = %v, want 未接线错误", err)
	}

	// 超出 64KiB 上限的 observation。
	w1yExec(t, db, `INSERT INTO chat_assets (id, system_account_id, conversation_id, expires_at, updated_at)
		VALUES ('ast-w1y-huge', 'sys', 'conv', '2027-01-01T00:00:00.000Z', 'now')`)
	huge := map[string]any{"summary": strings.Repeat("字", chatAssetObservationMaxBytes)}
	if _, err := observations.setObservation(ctx, "ast-w1y-huge", "conv", "sys", "ready", huge,
		0, "claim", w1yNow()); err == nil || !strings.Contains(err.Error(), "超出字节上限") {
		t.Fatalf("超限 observation setObservation = %v, want 超出字节上限", err)
	}

	// revision/claim 不匹配 → RowsAffected = 0 → false。
	w1yExec(t, db, `INSERT INTO chat_assets (id, system_account_id, conversation_id, expires_at, updated_at,
		observation_status, observation_revision, observation_claim_id)
		VALUES ('ast-w1y-claim', 'sys', 'conv', '2027-01-01T00:00:00.000Z', 'now', 'pending', 3, 'claim-3')`)
	ok, err := observations.setObservation(ctx, "ast-w1y-claim", "conv", "sys", "ready",
		map[string]any{"summary": "w1y"}, 4, "claim-3", w1yNow())
	if err != nil || ok {
		t.Fatalf("revision 不匹配 setObservation = (%t, %v), want (false, nil)", ok, err)
	}
	ok, err = observations.setObservation(ctx, "ast-w1y-claim", "conv", "sys", "ready",
		map[string]any{"summary": "w1y"}, 3, "claim-3", w1yNow())
	if err != nil || !ok {
		t.Fatalf("revision 匹配 setObservation = (%t, %v), want (true, nil)", ok, err)
	}
}

// ---------------------------------------------------------------------------
// chain_chat.go：对象存储错误臂
// ---------------------------------------------------------------------------

func TestW1YChatAssetObjectStoreArms(t *testing.T) {
	// 根目录被同名文件占用 → MkdirAll 失败。
	root := t.TempDir()
	occupied := filepath.Join(root, "occupied")
	if err := os.WriteFile(occupied, []byte("x"), 0o644); err != nil {
		t.Fatalf("写占用文件: %v", err)
	}
	if _, err := newChatAssetObjectStore(occupied); err == nil ||
		!strings.Contains(err.Error(), "创建 chat 资产根目录失败") {
		t.Fatalf("根目录被文件占用 newChatAssetObjectStore = %v, want 创建失败", err)
	}

	store, err := newChatAssetObjectStore(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatalf("newChatAssetObjectStore: %v", err)
	}

	// 非法存储键：Write / Open 均透传 path 错误。
	payload := []byte("w1y-payload")
	if err := store.Write("..hidden", payload, 1<<20, ""); err == nil ||
		!strings.Contains(err.Error(), "不合法") {
		t.Fatalf("非法键 Write = %v, want 不合法", err)
	}
	if _, _, err := store.Open("..hidden", 1<<20); err == nil ||
		!strings.Contains(err.Error(), "不合法") {
		t.Fatalf("非法键 Open = %v, want 不合法", err)
	}

	// 目录路径被文件占用 → Write 的 MkdirAll 失败。
	w1yExecFile(t, filepath.Join(store.root, "occupied"))
	if err := store.Write("occupied/inner.bin", payload, 1<<20, ""); err == nil {
		t.Fatalf("目录被文件占用 Write 未报错")
	}

	// Delete 对非空目录 os.Remove 失败并保留首个错误。
	if err := store.Write("w1ydir/f.bin", payload, 1<<20, ""); err != nil {
		t.Fatalf("写入待删除目录: %v", err)
	}
	if err := store.Delete([]string{"w1ydir"}); err == nil {
		t.Fatalf("Delete 非空目录未报错")
	}
}

func w1yExecFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("occupied"), 0o644); err != nil {
		t.Fatalf("写占位文件 %s: %v", path, err)
	}
}

// ---------------------------------------------------------------------------
// chain_chat.go：网关执行器 / 模型目录 / 网关 key 校验
// ---------------------------------------------------------------------------

type w1yRecorderChain struct {
	mu     sync.Mutex
	method string
	path   string
	body   string
}

func (r *w1yRecorderChain) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.method = req.Method
	r.path = req.URL.Path
	payload, _ := io.ReadAll(req.Body)
	r.body = string(payload)
	r.mu.Unlock()
	_, _ = w.Write([]byte("w1y-chain-ok"))
}

func (r *w1yRecorderChain) snapshot() (string, string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.method, r.path, r.body
}

func TestW1YChatGatewayExecutorDispatchArms(t *testing.T) {
	if _, err := (&chatGatewayExecutor{}).Dispatch(context.Background(), chat.GenerationDispatchRequest{}); err == nil ||
		!strings.Contains(err.Error(), "chat gateway executor 未接线") {
		t.Fatalf("nil chain Dispatch = %v, want 未接线", err)
	}
	var nilExecutor *chatGatewayExecutor
	if _, err := nilExecutor.Dispatch(context.Background(), chat.GenerationDispatchRequest{}); err == nil ||
		!strings.Contains(err.Error(), "未接线") {
		t.Fatalf("nil receiver Dispatch = %v, want 未接线", err)
	}

	recorder := &w1yRecorderChain{}
	executor := newChatGatewayExecutor(recorder)
	response, err := executor.Dispatch(context.Background(), chat.GenerationDispatchRequest{
		Body: []byte(`{"w1y":true}`),
	})
	if err != nil {
		t.Fatalf("默认路径 Dispatch = %v, want nil", err)
	}
	body, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		t.Fatalf("读响应体: %v", readErr)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("关闭响应体: %v", readErr)
	}
	if response.Status != http.StatusOK || string(body) != "w1y-chain-ok" {
		t.Fatalf("响应 = (%d, %q), want (200, w1y-chain-ok)", response.Status, body)
	}
	method, path, bodyText := recorder.snapshot()
	if method != http.MethodPost || path != "/v1/chat/completions" || bodyText != `{"w1y":true}` {
		t.Fatalf("链上请求 = (%q, %q, %q), want (POST, /v1/chat/completions, 原文透传)", method, path, bodyText)
	}

	if _, err := executor.Dispatch(context.Background(), chat.GenerationDispatchRequest{
		Method: "BAD METHOD",
	}); err == nil || !strings.Contains(err.Error(), "构建 chat 网关请求失败") {
		t.Fatalf("非法 method Dispatch = %v, want 构建失败", err)
	}

	// 处理器不写响应头：done 分支收敛，status 缺省 200。
	silent := newChatGatewayExecutor(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	silentResponse, err := silent.Dispatch(context.Background(), chat.GenerationDispatchRequest{})
	if err != nil {
		t.Fatalf("无响应头 Dispatch = %v, want nil", err)
	}
	if silentResponse.Status != http.StatusOK {
		t.Errorf("无响应头 status = %d, want 缺省 200", silentResponse.Status)
	}
	_, _ = io.ReadAll(silentResponse.Body)
	_ = silentResponse.Body.Close()

	// ctx.Done 分支与真实调度时序相关（goroutine + select 竞争），按波次约定跳过。
}

// w1yRuntimeModels 在 w1hReadModels 之上覆盖三个读取口，按字段注入结果或错误。
type w1yRuntimeModels struct {
	*w1hReadModels
	accounts    []gatewayruntimecache.OpenAIAccountSecret
	accountsErr error
	catalog     []gatewayruntimecache.ProviderModelCatalogItem
	catalogErr  error
	runtime     *gatewayruntimecache.GatewayRuntime
	runtimeErr  error
}

func (m *w1yRuntimeModels) ListOpenAIAccountsForGroupResult(context.Context, string, string, gatewayruntimecache.OpenAIAccountsForGroupOptions) (gatewayruntimecache.OpenAIAccountsForGroupResult, error) {
	if m.accountsErr != nil {
		return gatewayruntimecache.OpenAIAccountsForGroupResult{}, m.accountsErr
	}
	return gatewayruntimecache.OpenAIAccountsForGroupResult{Accounts: m.accounts}, nil
}

func (m *w1yRuntimeModels) ListProviderModelCatalog(_ context.Context, _ gatewayruntimecache.ModelCatalogListOptions) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	return m.catalog, m.catalogErr
}

func (m *w1yRuntimeModels) ReadGatewayRuntime(context.Context, string) (gatewayruntimecache.GatewayRuntime, error) {
	if m.runtimeErr != nil {
		return gatewayruntimecache.GatewayRuntime{}, m.runtimeErr
	}
	if m.runtime == nil {
		return gatewayruntimecache.GatewayRuntime{}, nil
	}
	return *m.runtime, nil
}

func w1yNewRuntimeCache(t *testing.T, models gatewayruntimecache.ReadModels) *gatewayruntimecache.Service {
	t.Helper()
	service, err := gatewayruntimecache.New(models, gatewayruntimecache.Options{})
	if err != nil {
		t.Fatalf("组装 w1y runtime cache: %v", err)
	}
	return service
}

func TestW1YChatModelCatalogArms(t *testing.T) {
	if got := (chatModelCatalog{}).ListAccountsForGroup("grp", "sys", "m", "chat"); got != nil {
		t.Fatalf("nil cache ListAccountsForGroup = %+v, want nil", got)
	}
	if got := (chatModelCatalog{}).ListProviderCatalog("openai", "sys"); got != nil {
		t.Fatalf("nil cache ListProviderCatalog = %+v, want nil", got)
	}

	enabled := true
	models := &w1yRuntimeModels{
		accounts: []gatewayruntimecache.OpenAIAccountSecret{{
			ID:                     "acc-w1y",
			Type:                   "api_key",
			Status:                 "active",
			ProviderCode:           "openai",
			SupportedEndpointModes: []string{"chat_completions"},
			SupportedModels:        []string{"gpt-w1y"},
			ModelMappings: []gatewayruntimecache.AccountModelMapping{{
				Enabled:                true,
				SourceModel:            "src-w1y",
				UpstreamModel:          "up-w1y",
				SourceEndpointFamily:   "chat",
				UpstreamEndpointFamily: "chat",
			}},
		}},
		catalog: []gatewayruntimecache.ProviderModelCatalogItem{{ProviderCode: "openai", Model: "m-w1y"}},
		runtime: &gatewayruntimecache.GatewayRuntime{APIKey: &gatewayruntimecache.GatewayAPIKeyRow{
			Status:                              "active",
			SystemAccountImageGenerationEnabled: 1,
			GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{{
				GroupID: "grp-w1y", Status: "active", GroupEnabled: 1,
			}},
		}},
	}
	catalog := chatModelCatalog{cache: w1yNewRuntimeCache(t, models)}
	accounts := catalog.ListAccountsForGroup("grp-w1y", "sys-w1y", "gpt-w1y", "chat")
	if len(accounts) != 1 {
		t.Fatalf("accounts = %d 项, want 1", len(accounts))
	}
	account := accounts[0]
	if account.ID != "acc-w1y" || account.Type != "api_key" || account.ProviderCode != "openai" {
		t.Errorf("account 头字段 = %+v", account)
	}
	if len(account.SupportedEndpointModes) != 1 || account.SupportedEndpointModes[0] != "chat_completions" {
		t.Errorf("SupportedEndpointModes = %v", account.SupportedEndpointModes)
	}
	if len(account.ModelMappings) != 1 {
		t.Fatalf("ModelMappings = %d 项, want 1", len(account.ModelMappings))
	}
	mapping := account.ModelMappings[0]
	if mapping.Enabled == nil || *mapping.Enabled != enabled {
		t.Errorf("mapping.Enabled = %v, want true 指针", mapping.Enabled)
	}
	if mapping.SourceModel != "src-w1y" || mapping.UpstreamModel != "up-w1y" {
		t.Errorf("mapping 模型字段 = %+v", mapping)
	}

	items := catalog.ListProviderCatalog("openai", "sys-w1y")
	if len(items) != 1 || items[0].Model != "m-w1y" || items[0].ProviderCode != "openai" {
		t.Fatalf("ListProviderCatalog = %+v, want m-w1y 透传", items)
	}

	validator := chatGatewayKeyValidator{cache: catalog.cache}
	view, err := validator.ValidateGatewayKey("sk-w1y")
	if err != nil {
		t.Fatalf("ValidateGatewayKey = %v, want nil", err)
	}
	if view == nil || !view.ImageGenerationEnabled {
		t.Fatalf("view = %+v, want 图片生成开启", view)
	}
	if len(view.GroupBindings) != 1 || view.GroupBindings[0].GroupID != "grp-w1y" ||
		view.GroupBindings[0].Status != "active" || !view.GroupBindings[0].GroupEnabled {
		t.Fatalf("GroupBindings = %+v", view.GroupBindings)
	}

	// runtime 无 API Key：返回 (nil, nil)。
	emptyValidator := chatGatewayKeyValidator{cache: w1yNewRuntimeCache(t, &w1yRuntimeModels{})}
	if view, err := emptyValidator.ValidateGatewayKey("sk-none"); err != nil || view != nil {
		t.Fatalf("无 API Key 校验 = (%v, %v), want (nil, nil)", view, err)
	}
}

func TestW1YChatModelCatalogAndValidatorErrors(t *testing.T) {
	models := &w1yRuntimeModels{
		accountsErr: errors.New("w1y accounts 读取失败"),
		catalogErr:  errors.New("w1y catalog 读取失败"),
		runtimeErr:  errors.New("w1y runtime 读取失败"),
	}
	cache := w1yNewRuntimeCache(t, models)
	if got := (chatModelCatalog{cache: cache}).ListAccountsForGroup("grp", "sys", "m", "chat"); got != nil {
		t.Fatalf("账户读取失败 ListAccountsForGroup = %+v, want nil", got)
	}
	if got := (chatModelCatalog{cache: cache}).ListProviderCatalog("openai", "sys"); got != nil {
		t.Fatalf("目录读取失败 ListProviderCatalog = %+v, want nil", got)
	}
	if _, err := (chatGatewayKeyValidator{cache: cache}).ValidateGatewayKey("sk-w1y"); err == nil ||
		!strings.Contains(err.Error(), "w1y runtime 读取失败") {
		t.Fatalf("runtime 读取失败 ValidateGatewayKey = %v, want 读取错误透传", err)
	}
	if _, err := (chatGatewayKeyValidator{}).ValidateGatewayKey("sk-w1y"); err == nil ||
		!strings.Contains(err.Error(), "chat gateway key validator 未接线") {
		t.Fatalf("nil cache ValidateGatewayKey = %v, want 未接线", err)
	}
}

// ---------------------------------------------------------------------------
// chain_chat_mount.go：composeChatFamily 资产根目录要求 + openChatDatabase pg 臂
// ---------------------------------------------------------------------------

func TestW1YComposeChatFamilyRequiresChatAssetsRoot(t *testing.T) {
	db := w1hNewChatFamilyDB(t)
	composed := &composition{db: db, kernel: kernel.New(kernel.Options{}), authDeps: &authsys.Deps{}}
	services := &chainRuntimeServices{Cache: w1hNewRuntimeCache(t, &w1hReadModels{})}
	if _, err := composeChatFamily(composed, runtimeConfig{ChatAssetsRoot: "   "}, db, services, &gatewayChain{}); err == nil ||
		!strings.Contains(err.Error(), "JUHE_AI_CHAT_ASSETS_ROOT") {
		t.Fatalf("空 ChatAssetsRoot composeChatFamily = %v, want JUHE_AI_CHAT_ASSETS_ROOT 提示", err)
	}
}

func TestW1YOpenChatDatabasePostgresArms(t *testing.T) {
	// 修复后契约（历史缺陷：Acquire 传 0/0 池上限恒被 ValidatePoolLimits
	// 拒绝，PG 模式 chat 库永远打不开）：合法池规格 + pgx 惰性打开 → 成功
	// 返回共享池句柄（不做真实连接）。
	handle, isSQLite, err := openChatDatabase(runtimeConfig{BusinessPostgresURL: "postgres://w1y-chat"}, pgpool.NewRegistry(), nil, true)
	if err != nil {
		t.Fatalf("pgDialect openChatDatabase = %v, want nil", err)
	}
	if isSQLite {
		t.Fatal("pgDialect isSQLite = true, want false")
	}
	if handle == nil {
		t.Fatal("pgDialect handle = nil")
	}
	if closeErr := handle.Close(); closeErr != nil {
		t.Fatalf("关闭池句柄: %v", closeErr)
	}
	_, _, err = openChatDatabase(runtimeConfig{BusinessPostgresURL: "postgres://w1y-chat"}, nil, nil, true)
	if err == nil || !strings.Contains(err.Error(), "未初始化") {
		t.Fatalf("nil registry openChatDatabase = %v, want 未初始化错误", err)
	}
}

// ---------------------------------------------------------------------------
// chain_chat_images.go：JPEG/EXIF 方向解析
// ---------------------------------------------------------------------------

func TestW1YChatJPEGOrientationByteGuards(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want int
	}{
		{"段前非 FF 字节", []byte("\xff\xd8\x00\x01"), 0},
		{"段前非法字节", []byte("\xff\xd8\x00\x01\xff\xe1\x00\x00"), 0},
		{"独立 marker 跳过", []byte("\xff\xd8\xff\xd8\xff\x01"), 0},
		{"段长被截断", []byte("\xff\xd8\xff\xe1"), 0},
		{"段长不足 2", []byte("\xff\xd8\xff\xe1\x00\x00"), 0},
		{"APP1 无 Exif 签名", []byte("\xff\xd8\xff\xe1\x00\x04Abcd"), 0},
	}
	for _, testCase := range cases {
		if got := chatJPEGOrientation(testCase.data, "jpeg"); got != testCase.want {
			t.Errorf("%s: chatJPEGOrientation = %d, want %d", testCase.name, got, testCase.want)
		}
	}
	if got := chatJPEGOrientation([]byte("\xff\xd8\xff\xe0"), "png"); got != 0 {
		t.Errorf("非 jpeg 格式入参 = %d, want 0", got)
	}
}

// w1yEXIFEntry 是 IFD0 里的一个 12 字节表项。
type w1yEXIFEntry struct {
	tag   uint16
	value uint16
}

// w1yBuildEXIFJPEG 组装带 APP1 EXIF 段的 JPEG：header + TIFF 字节序 + 魔数 +
// IFD 偏移 + entryCount（可与实际条目数不一致以构造截断）+ 条目。
func w1yBuildEXIFJPEG(byteOrder string, magic uint16, ifdOffset uint32, declaredCount int, entries []w1yEXIFEntry) []byte {
	tiff := &bytes.Buffer{}
	tiff.WriteString(byteOrder)
	writeU16 := func(v uint16) {
		tmp := make([]byte, 2)
		if byteOrder == "II" {
			binary.LittleEndian.PutUint16(tmp, v)
		} else {
			binary.BigEndian.PutUint16(tmp, v)
		}
		tiff.Write(tmp)
	}
	writeU32 := func(v uint32) {
		tmp := make([]byte, 4)
		if byteOrder == "II" {
			binary.LittleEndian.PutUint32(tmp, v)
		} else {
			binary.BigEndian.PutUint32(tmp, v)
		}
		tiff.Write(tmp)
	}
	writeU16(magic)
	writeU32(ifdOffset)
	writeU16(uint16(declaredCount))
	for _, entry := range entries {
		writeU16(entry.tag)
		writeU16(3) // SHORT 类型
		writeU32(1) // count 是 uint32 字段
		writeU16(entry.value)
		writeU16(0)
	}
	segment := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	// JPEG 段长含自身 2 字节长度字段：内容 28 字节时长度字段应写 30。
	segmentLength := len(segment) + 2
	out := []byte{0xFF, 0xD8, 0xFF, 0xE1, byte(segmentLength >> 8), byte(segmentLength)}
	return append(out, segment...)
}

func TestW1YChatEXIFOrientationTable(t *testing.T) {
	orientationEntry := w1yEXIFEntry{tag: 0x0112, value: 6}
	cases := []struct {
		name string
		data []byte
		want int
	}{
		{"小端序方向 6", w1yBuildEXIFJPEG("II", 0x002A, 8, 1, []w1yEXIFEntry{orientationEntry}), 6},
		{"大端序方向 8", w1yBuildEXIFJPEG("MM", 0x002A, 8, 1, []w1yEXIFEntry{{tag: 0x0112, value: 8}}), 8},
		{"未知字节序", w1yBuildEXIFJPEG("XX", 0x002A, 8, 1, []w1yEXIFEntry{orientationEntry}), 0},
		{"魔数错误", w1yBuildEXIFJPEG("II", 0x0042, 8, 1, []w1yEXIFEntry{orientationEntry}), 0},
		{"IFD 偏移越界", w1yBuildEXIFJPEG("II", 0x002A, 1000, 1, []w1yEXIFEntry{orientationEntry}), 0},
		{"条目被截断", w1yBuildEXIFJPEG("II", 0x002A, 8, 3, []w1yEXIFEntry{{tag: 0x010E, value: 1}}), 0},
		{"非方向 tag 先行", w1yBuildEXIFJPEG("II", 0x002A, 8, 2, []w1yEXIFEntry{{tag: 0x010E, value: 1}, orientationEntry}), 6},
		{"方向值越界", w1yBuildEXIFJPEG("II", 0x002A, 8, 1, []w1yEXIFEntry{{tag: 0x0112, value: 9}}), 0},
		{"无方向 tag 遍历到底", w1yBuildEXIFJPEG("II", 0x002A, 8, 2, []w1yEXIFEntry{{tag: 0x010E, value: 1}, {tag: 0x010F, value: 2}}), 0},
	}
	for _, testCase := range cases {
		if got := chatJPEGOrientation(testCase.data, "jpeg"); got != testCase.want {
			t.Errorf("%s: chatJPEGOrientation = %d, want %d", testCase.name, got, testCase.want)
		}
	}
	if got := chatEXIFOrientation([]byte("short")); got != 0 {
		t.Errorf("过短 EXIF = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// chain_chat_images.go：方向变换与尺寸界
// ---------------------------------------------------------------------------

// w1yGradientImage 生成 R=x, G=y, B=7, A=255 的确定性测试图。
func w1yGradientImage(width, height int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, w1yPixel(x, y))
		}
	}
	return img
}

func w1yPixel(x, y int) color.NRGBA {
	return color.NRGBA{R: uint8(x + 1), G: uint8(y + 1), B: 7, A: 255}
}

func TestW1YApplyChatOrientationOrientations(t *testing.T) {
	const width, height = 3, 2
	src := w1yGradientImage(width, height)
	transforms := map[int]func(x, y int) (int, int){
		2: func(x, y int) (int, int) { return width - 1 - x, y },
		3: func(x, y int) (int, int) { return width - 1 - x, height - 1 - y },
		4: func(x, y int) (int, int) { return x, height - 1 - y },
		5: func(x, y int) (int, int) { return y, x },
		6: func(x, y int) (int, int) { return height - 1 - y, x },
		7: func(x, y int) (int, int) { return height - 1 - y, width - 1 - x },
		8: func(x, y int) (int, int) { return y, width - 1 - x },
	}
	for orientation, transform := range transforms {
		got := applyChatOrientation(src, orientation)
		wantWidth, wantHeight := width, height
		if orientation >= 5 {
			wantWidth, wantHeight = height, width
		}
		if got.Bounds().Dx() != wantWidth || got.Bounds().Dy() != wantHeight {
			t.Fatalf("orientation %d 尺寸 = %dx%d, want %dx%d", orientation, got.Bounds().Dx(), got.Bounds().Dy(), wantWidth, wantHeight)
		}
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				dx, dy := transform(x, y)
				r, g, b, a := got.At(dx, dy).RGBA()
				want := w1yPixel(x, y)
				if uint8(r>>8) != want.R || uint8(g>>8) != want.G || uint8(b>>8) != want.B || uint8(a>>8) != want.A {
					t.Errorf("orientation %d 像素 (%d,%d)->(%d,%d) = (%d,%d,%d,%d), want %v",
						orientation, x, y, dx, dy, r>>8, g>>8, b>>8, a>>8, want)
				}
			}
		}
	}

	// 方向 1 / 越界方向：原样 NRGBA。
	same := applyChatOrientation(src, 1)
	if same.Bounds() != src.Bounds() {
		t.Fatalf("方向 1 尺寸改变: %v", same.Bounds())
	}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, g, b, a := same.At(x, y).RGBA()
			want := w1yPixel(x, y)
			if uint8(r>>8) != want.R || uint8(g>>8) != want.G || uint8(b>>8) != want.B || uint8(a>>8) != want.A {
				t.Fatalf("方向 1 像素 (%d,%d) 被改写", x, y)
			}
		}
	}
}

func TestW1YChatImageDimensionsAndMath(t *testing.T) {
	if _, ok := orientedChatDimensions(image.Rect(0, 0, 0, 4), 1); ok {
		t.Fatalf("零宽图 orientedChatDimensions = true, want false")
	}
	dimensions, ok := orientedChatDimensions(image.Rect(0, 0, 10, 4), 6)
	if !ok || dimensions.width != 4 || dimensions.height != 10 {
		t.Fatalf("方向 6 交换尺寸 = (%+v, %t), want ({4,10}, true)", dimensions, ok)
	}

	// 边长超限：1024 边长缩放生效。
	target := boundedChatImageDimensions(2000, 500)
	if target.width != 1024 || target.height != 256 {
		t.Fatalf("bounded(2000,500) = %+v, want {1024,256}", target)
	}
	// 边长未超限：原尺寸。
	target = boundedChatImageDimensions(800, 600)
	if target.width != 800 || target.height != 600 {
		t.Fatalf("bounded(800,600) = %+v, want 原尺寸", target)
	}

	if got := sqrtOf(4); math.Abs(got-2) > 1e-9 {
		t.Fatalf("sqrtOf(4) = %v, want 收敛到 2（牛顿迭代允许浮点误差）", got)
	}
	if got := sqrtOf(0); got != 0 {
		t.Fatalf("sqrtOf(0) = %v, want 0", got)
	}
	if got := sqrtOf(-1); got != 0 {
		t.Fatalf("sqrtOf(-1) = %v, want 0", got)
	}
	if got := maxInt(5, 3); got != 5 {
		t.Fatalf("maxInt(5,3) = %d, want 5", got)
	}
	if got := maxInt(3, 5); got != 5 {
		t.Fatalf("maxInt(3,5) = %d, want 5", got)
	}
	if got := resizeChatImage(w1yGradientImage(3, 2), 2, 1); got.Bounds().Dx() != 2 || got.Bounds().Dy() != 1 {
		t.Fatalf("resizeChatImage(3x2 -> 2x1) = %v, want 2x1", got.Bounds())
	}
}

// ---------------------------------------------------------------------------
// chain_chat_images.go：解码像素门 / 上传 WebP 尺寸门 / 预览阶梯
// ---------------------------------------------------------------------------

func w1yPNGBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	buffer := &bytes.Buffer{}
	if err := png.Encode(buffer, w1yGradientImage(width, height)); err != nil {
		t.Fatalf("PNG 编码 %dx%d: %v", width, height, err)
	}
	return buffer.Bytes()
}

func TestW1YDecodeChatImagePixelGate(t *testing.T) {
	// 6401x6251 = 40,012,651 像素 > chatMaxDecodedPixels(40M)。
	data := w1yPNGBytes(t, 6401, 6251)
	if _, _, err := decodeChatImage(data); !errors.Is(err, errChatImageDecode) {
		t.Fatalf("超大像素 decodeChatImage = %v, want errChatImageDecode", err)
	}
	if _, err := newChatImageProcessor().ProcessUpload(data, "image/png"); err == nil ||
		!strings.Contains(err.Error(), "图片无法解码、像素过大或文件已损坏") {
		t.Fatalf("超大像素 ProcessUpload = %v, want 像素过大错误", err)
	}
}

func TestW1YProcessUploadWebPByteCeiling(t *testing.T) {
	// 1600x1600：边界缩放到 1024x1024 后，全字面量 VP8L 恒为
	// 宽*高*4 ≈ 4.19MiB > 3MiB → 触发尺寸门错误（同时覆盖缩放重编码路径）。
	processor := newChatImageProcessor()
	if _, err := processor.ProcessUpload(w1yPNGBytes(t, 1600, 1600), "image/png"); err == nil ||
		!strings.Contains(err.Error(), "仍超过 3 MiB") {
		t.Fatalf("大图 ProcessUpload = %v, want 仍超过 3 MiB", err)
	}
}

func TestW1YProcessUploadHappyResizePath(t *testing.T) {
	// 1600x1190 → 边界缩放 0.64 → 1024x761 = 779,264 像素，全字面量 VP8L
	// 输出 ≈ 3,117,056 字节 < 3MiB 上限，走缩放重编码并成功。
	// （目标高边必须低于 768：1024x768 的像素数据本身已等于 3MiB，加
	// WebP 容器头部必超限。）
	processor := newChatImageProcessor()
	processed, err := processor.ProcessUpload(w1yPNGBytes(t, 1600, 1190), "image/png")
	if err != nil {
		t.Fatalf("缩放路径 ProcessUpload = %v, want nil", err)
	}
	if processed.OriginalMimeType != "image/png" || processed.MimeType != "image/webp" {
		t.Errorf("MIME = %s -> %s, want image/png -> image/webp", processed.OriginalMimeType, processed.MimeType)
	}
	if processed.OriginalWidth != 1600 || processed.OriginalHeight != 1190 {
		t.Errorf("原始尺寸 = %dx%d, want 1600x1190", processed.OriginalWidth, processed.OriginalHeight)
	}
	if processed.Width != 1024 || processed.Height != 761 {
		t.Errorf("输出尺寸 = %dx%d, want 1024x761", processed.Width, processed.Height)
	}
	if processed.ByteSize != int64(len(processed.Buffer)) || len(processed.SHA256) != 64 {
		t.Errorf("ByteSize/SHA256 = %d/%d, want 一致且 64 位十六进制", processed.ByteSize, len(processed.SHA256))
	}
}

func TestW1YCreatePreviewDecodeError(t *testing.T) {
	if _, err := newChatImageProcessor().CreatePreview([]byte("not-an-image")); !errors.Is(err, errChatImageDecode) {
		t.Fatalf("垃圾字节 CreatePreview = %v, want errChatImageDecode", err)
	}
}

func TestW1YCreatePreviewLadder(t *testing.T) {
	// 全字面量 VP8L 输出恒为 宽*高*4+常量，阶梯逐级缩小直到 384 档落入
	// 512KiB 上限；640/560/480 档触发 continue 分支。
	processor := newChatImageProcessor()

	preview, err := processor.CreatePreview(w1yPNGBytes(t, 1200, 800))
	if err != nil {
		t.Fatalf("横图 CreatePreview = %v, want nil", err)
	}
	if preview.Width != 384 || preview.Height != 256 {
		t.Errorf("横图预览尺寸 = %dx%d, want 384x256（384 档）", preview.Width, preview.Height)
	}
	if preview.MimeType != "image/webp" || preview.ByteSize > chatImagePreviewBytes {
		t.Errorf("预览 MIME/大小 = %s/%d, want webp 且不超过 %d", preview.MimeType, preview.ByteSize, chatImagePreviewBytes)
	}

	preview, err = processor.CreatePreview(w1yPNGBytes(t, 800, 1200))
	if err != nil {
		t.Fatalf("竖图 CreatePreview = %v, want nil", err)
	}
	if preview.Width != 256 || preview.Height != 384 {
		t.Errorf("竖图预览尺寸 = %dx%d, want 256x384（竖图换向）", preview.Width, preview.Height)
	}
}
