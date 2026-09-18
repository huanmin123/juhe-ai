package main

// w2_account_funcs_test.go —— 账户功能组残余未覆盖行收官批次（w2b 前缀，
// TestW2B 入口；只新增本文件，不修改任何现有文件）。
//
// 已覆盖目标行（与 w4 基线覆盖档案逐行对照后确认语义，均经本文件测试触达）：
//   compose_account_test_local.go      139 152 159 172（另补 162/205 降级臂与 207 恢复臂）
//   compose_account_balance_health.go  115 123
//   compose_account_balance_refresh.go 118(PG 批) 298(PG 批) 301(PG 批) 361 423
//   chain_accounts.go                  353 529 635 649 725 1457（原 722 平局死体
//                                      已于 w3 清理，见下；本测试触达路径变为
//                                      717 quality 块与 725 ID 收尾，断言不变）
//   chain_accounts_secret.go           131 141
//   chain_usage.go                     55 84 169 184 314
//   chain_pricing.go                   80 139 174 206
//   chain_turn_retry_redis.go          137 167
//   compose_accounts_reset.go          476
//   compose_codex_usage_headers.go     66
//   compose_authz_expiry_sync.go       37 47（47 为慢速批：节拍常量 1 分钟）
//
// 恒不可达残余行（逐块证据，见各测试文件头与本清单，不硬凑）。
// w3 死代码清理结果：已删条目标注“已于 w3 清理”；保留条目注明理由：
//   compose_account_test_local.go:180 —— 保留。
//       accountprobe.NewService 仅在 Source==nil（167 行 proberepo.NewStore
//       成功后恒非 nil）或默认受控 HTTP client 构造失败（空代理恒成功，
//       accounttest/accountprobe/probe.go:110-135）时报错；经由
//       wireInProcessAccountTestDispatch 不可达。但 NewService 是共享平台
//       导出构造函数，删除错误臂后退化为 `probeService, _ :=` 反模式，
//       签名不在 w3 授权范围内 → 惯例守卫保留。
//   compose_account_test_local.go:188 —— 保留。同上：manualtest.NewExecutor
//       仅在 Probe==nil（175 行成功后恒非 nil）或 Secret 为空（167 行
//       proberepo.NewStore 已强制 Secret 非空）时报错
//       （accounttest/manualtest/executor.go:70-82）；不可达，但删后退化为
//       `executor, _ :=` 反模式 → 保留。
//   compose_account_test_local.go:197 —— 保留（w3 判定推翻原删除意向）。
//       NowMS 字面量体确实无调用点（ManualTestQueue.nowMS 字段在
//       shared/platform/accounttest/queue.go 仅参与构造赋值，全包无
//       q.nowMS() 读取点），但 NewManualTestQueue 对 cfg.NowMS==nil 是
//       fail-closed 校验（queue.go:117-119 “手动测试队列必须注入 NowMS
//       时钟”），删除赋值会让组合根装配直接失败；清除字面量需先改共享平台
//       queue.go（删除校验与字段），不在 w3 文件清单内 → 赋值保留并在此登记。
//   compose_account_test_local.go:199 —— 保留。NewManualTestQueue 错误臂，
//       惯例守卫。
//   compose_account_balance_refresh.go:99 —— 保留。OpenStore 错误臂，
//       PostgresPool 注入路径（store.go:118）直接返回 Store 无失败分支，
//       删后退化为 `store, _ :=` 反模式 → 惯例守卫保留。
//   compose_account_balance_refresh.go:305/553/557 —— 已于 w3 清理。
//       accountbalance.Snapshot 全字段为 string 底层类型/int（types.go:46-58），
//       json.Marshal 不可能失败；其产物 Unmarshal 进 map[string]any 亦不可能
//       失败 → snapshotToMap 两个错误臂及调用点 305 删除，签名简化为
//       无 error 返回（包内私有函数）。
//   compose_account_balance_refresh.go:443/449 —— 保留。
//       resolveProxyURL 的解密失败/非对象臂按当前不变式（envelope 由
//       resolveProxyURLEnvelope 用同一 r.secret 现场重加密）恒不可达，但
//       该函数整体仍有真实错误路径（DB 查询、不支持类型、无效地址），无法
//       去 error 签名；删除两臂后退化为 `plain, _ :=` 与忽略 Unmarshal
//       error 反模式 → 调用点守卫保留。
//   chain_accounts.go:723（原 722 的 delta!=0 体）—— 已于 w3 清理。
//       到达 722 需 717 条件为假；而 bucket key 相同 ⇒ 同 key 行数>=2 ⇒
//       quality 比较必然执行且对相异行恒非零（compareChainQuality 以名字或
//       ID 收尾）→ 717 命中即返回；717 为假要求 fallback/super/priority 至
//       少一个不同，均已在 708-715 提前返回 → 该体不可达。722-724 整块删除，
//       725 的 ID 收尾保留（“名称与 ID 双重复”行由
//       TestW2BChainAccountsOrderTieBreaks 继续触达，行为不变）。
//   compose_account_balance_health.go:123 —— 保留。NewRequest 仅在 URL 二次
//       Parse 失败时报错，经论证不可达（首次 Parse 成功后 Path 覆写为
//       "/health"、RawQuery/Fragment 清空、Host/Userinfo 非法转义已在首次
//       Parse 拒绝），但删除后退化为 `request, _ :=` 反模式 → 调用点守卫
//       保留。
//   chain_pricing.go:193 —— 已于 w3 清理。chainCatalogPricing 仅在 item==nil
//       时返回 nil，breakdownFor:189 已守卫 item 非 nil → 193-195 死检查删除
//       （chainCatalogPricing 自身的 nil 守卫保留：w1 测试以 nil 传参作为契约）。
//   chain_pricing.go:307 —— 已于 w3 清理。strconv.FormatFloat(v,'f',10,64) 的
//       输出（含 "NaN"/"+Inf" 文本）恒可被 strconv.ParseFloat 解析 → 错误臂
//       删除（同一表达式内自包含往返，非跨函数调用点守卫）。
//   compose_authz_grant_returner.go:38 —— 已于 w3 清理。authz.Store.Return
//       全部成功路径恒返回非 nil *TerminalMutation（internal/authz/
//       mutations.go:683 起：not_found/conflict/unchanged/updated 均显式构造，
//       mutation==nil 只与 err 同时出现）→ mutation==nil 死臂删除，err 守卫
//       保留。
//   chain_catalog.go:262 —— 保留（w3 评估结论）。rows.Scan(pointers...) 到
//       *any 目标的值转换恒成功，但列数不匹配是 database/sql 公共 API 的
//       真实错误路径（当前仅由调用方投影一致的不变式排除），删除后退化为
//       忽略 Scan error → 调用点守卫保留。

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	moderncsqlite "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/providers"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/tablemonitor"
	platformaccountbalance "github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/jackc/pgx/v5/stdlib"
)

// ---------------------------------------------------------------------------
// w2b 故障注入 driver：包装底层 driver.Conn，按查询匹配规则注入
// Scan 数量不匹配 / 迭代错误 / 空结果 / 不可序列化值 / 直接报错。
// ---------------------------------------------------------------------------

type w2bFaultAction int

const (
	w2bFaultQueryError   w2bFaultAction = iota // QueryContext 直接返回规则错误
	w2bFaultNoRows                             // 返回 0 列空结果集（QueryRow.Scan 得 ErrNoRows）
	w2bFaultShortColumns                       // Columns() 少报一列 → Scan 数量不匹配
	w2bFaultZeroColumns                        // Columns() 报 0 列 → Scan 数量不匹配
	w2bFaultNextError                          // 首次 Next 返回错误 → rows.Err() 非空
	w2bFaultBadValue                           // 首行首列替换为 func()（不可 JSON 序列化）
)

type w2bFaultRule struct {
	match  func(query string) bool
	action w2bFaultAction
	err    error
	// afterN > 0 时仅对第 afterN 次及以后命中的查询生效（按连接计数，
	// 须配合 SetMaxOpenConns(1)）。
	afterN int
	hits   int
}

type w2bFaultConnector struct {
	base  driver.Driver
	dsn   string
	rules []*w2bFaultRule
}

func (c *w2bFaultConnector) Connect(_ context.Context) (driver.Conn, error) {
	base, err := c.base.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &w2bFaultConn{base: base, rules: c.rules}, nil
}

func (c *w2bFaultConnector) Driver() driver.Driver { return c.base }

type w2bFaultConn struct {
	base  driver.Conn
	rules []*w2bFaultRule
}

func (c *w2bFaultConn) ruleFor(query string) *w2bFaultRule {
	for _, rule := range c.rules {
		if rule.match == nil || !rule.match(query) {
			continue
		}
		rule.hits++
		if rule.afterN > 0 && rule.hits < rule.afterN {
			continue
		}
		return rule
	}
	return nil
}

func (c *w2bFaultConn) Prepare(query string) (driver.Stmt, error) { return c.base.Prepare(query) }
func (c *w2bFaultConn) Close() error                              { return c.base.Close() }
func (c *w2bFaultConn) Begin() (driver.Tx, error)                 { return c.base.Begin() }

// BeginTx 委托底层（pgx 支持只读/隔离级别事务，checkPostgresSchema 依赖）。
func (c *w2bFaultConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if beginTx, ok := c.base.(driver.ConnBeginTx); ok {
		return beginTx.BeginTx(ctx, opts)
	}
	return c.base.Begin()
}

// CheckNamedValue 委托底层参数检查（pgx 的 ANY($1) 数组参数依赖它）。
func (c *w2bFaultConn) CheckNamedValue(nv *driver.NamedValue) error {
	if checker, ok := c.base.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(nv)
	}
	value, err := driver.DefaultParameterConverter.ConvertValue(nv.Value)
	if err != nil {
		return err
	}
	nv.Value = value
	return nil
}

func (c *w2bFaultConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if exec, ok := c.base.(driver.ExecerContext); ok {
		return exec.ExecContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *w2bFaultConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rule := c.ruleFor(query)
	if rule != nil {
		switch rule.action {
		case w2bFaultQueryError:
			if rule.err != nil {
				return nil, rule.err
			}
			return nil, errors.New("w2b 注入查询错误")
		case w2bFaultNoRows:
			return &w2bFaultRows{columns: []string{}}, nil
		case w2bFaultZeroColumns:
			return &w2bFaultRows{columns: []string{}, oneFakeRow: true}, nil
		}
	}
	queryer, ok := c.base.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := queryer.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	if rule != nil {
		switch rule.action {
		case w2bFaultShortColumns:
			return &w2bShortColumnsRows{base: rows, all: rows.Columns()}, nil
		case w2bFaultNextError:
			if rule.err != nil {
				return &w2bNextErrorRows{base: rows, err: rule.err}, nil
			}
			return &w2bNextErrorRows{base: rows, err: errors.New("w2b 注入迭代错误")}, nil
		case w2bFaultBadValue:
			return &w2bBadValueRows{base: rows}, nil
		}
	}
	return rows, nil
}

// w2bFaultRows 是合成结果集：NoRows（0 列无行）与 ZeroColumns（0 列一假行）。
type w2bFaultRows struct {
	columns     []string
	oneFakeRow  bool
	fakeRowRead bool
}

func (r *w2bFaultRows) Columns() []string { return r.columns }
func (r *w2bFaultRows) Close() error      { return nil }
func (r *w2bFaultRows) Next(dest []driver.Value) error {
	if r.oneFakeRow && !r.fakeRowRead {
		r.fakeRowRead = true
		return nil
	}
	return io.EOF
}

// w2bShortColumnsRows 少报一列，使调用方 Scan 出现数量不匹配错误。
type w2bShortColumnsRows struct {
	base driver.Rows
	all  []string
}

func (r *w2bShortColumnsRows) Columns() []string { return r.all[:len(r.all)-1] }
func (r *w2bShortColumnsRows) Close() error      { return r.base.Close() }
func (r *w2bShortColumnsRows) Next(dest []driver.Value) error {
	tmp := make([]driver.Value, len(r.all))
	if err := r.base.Next(tmp); err != nil {
		return err
	}
	copy(dest, tmp)
	return nil
}

// w2bNextErrorRows 首次 Next 即返回错误（rows.Err() 非空、循环体不执行）。
type w2bNextErrorRows struct {
	base driver.Rows
	err  error
}

func (r *w2bNextErrorRows) Columns() []string { return r.base.Columns() }
func (r *w2bNextErrorRows) Close() error      { return r.base.Close() }
func (r *w2bNextErrorRows) Next([]driver.Value) error {
	return r.err
}

// w2bBadValueRows 首行首列替换为 func(){}（json.Marshal 必然失败）。
type w2bBadValueRows struct {
	base  driver.Rows
	fired bool
}

func (r *w2bBadValueRows) Columns() []string { return r.base.Columns() }
func (r *w2bBadValueRows) Close() error      { return r.base.Close() }
func (r *w2bBadValueRows) Next(dest []driver.Value) error {
	tmp := make([]driver.Value, len(dest))
	if err := r.base.Next(tmp); err != nil {
		return err
	}
	if !r.fired {
		r.fired = true
		if len(tmp) > 0 {
			tmp[0] = func() {}
		}
	}
	copy(dest, tmp)
	return nil
}

// w2bOpenPlainSQLiteAt 打开指定路径的普通 SQLite 句柄（先建表种子用）。
func w2bOpenPlainSQLiteAt(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("打开 SQLite %s 失败: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// w2bOpenFaultySQLiteAt 在指定路径上打开带故障注入规则的 SQLite 句柄（与
// 普通句柄同文件：先用普通句柄建表种子，再用本句柄查询）。
func w2bOpenFaultySQLiteAt(t *testing.T, path string, rules ...*w2bFaultRule) *sql.DB {
	t.Helper()
	db := sql.OpenDB(&w2bFaultConnector{base: &moderncsqlite.Driver{}, dsn: "file:" + filepath.ToSlash(path), rules: rules})
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ---------------------------------------------------------------------------
// compose_account_test_local.go：139 / 152 / 159 / 172
// ---------------------------------------------------------------------------

// TestW2BAccountTestDispatchCancelInvalidID 覆盖 DispatchAccountTestCancel
// 的非法任务 ID 早退臂（139，queue 为 nil 也不会被触达）。
func TestW2BAccountTestDispatchCancelInvalidID(t *testing.T) {
	dispatch := &gatewayAccountTestDispatch{}
	dispatch.DispatchAccountTestCancel("   ") // NormalizeTaskID 失败 → 直接返回
	dispatch.DispatchAccountTestCancel("")
}

// TestW2BAccountTestDispatchWireArms 覆盖 wireInProcessAccountTestDispatch
// 的三个前置错误臂：152（队列 env 非法）/ 159（业务库句柄 nil）/ 172（
// Secret 为空且契约表可读）。
func TestW2BAccountTestDispatchWireArms(t *testing.T) {
	cfg := runtimeConfig{Secret: "w2b-secret"}

	// 152：JUHE_AI_JOBS_PROBE_CONCURRENCY 非整数 → fail closed。
	t.Setenv(envProbeConcurrency, "not-a-number")
	if err := wireInProcessAccountTestDispatch(&composition{}, cfg, nil); err == nil ||
		!strings.Contains(err.Error(), "必须是整数") {
		t.Fatalf("非法队列 env 必须报错: %v", err)
	}
	t.Setenv(envProbeConcurrency, "")

	// 159：合法 env + nil 业务库句柄 → manualtestrepo.New 报错。
	if err := wireInProcessAccountTestDispatch(&composition{}, cfg, nil); err == nil ||
		!strings.Contains(err.Error(), "缺少业务库句柄") {
		t.Fatalf("nil 业务库必须报错: %v", err)
	}

	// 172：契约表可读但 Secret 为空 → proberepo.NewStore 报错。
	db := w1uOpenPlainSQLite(t, "w2b-account-test-tables.sqlite3")
	for _, table := range []string{"account_test_tasks", "account_test_sessions", "account_test_session_tasks"} {
		if _, err := db.Exec("CREATE TABLE " + table + " (id TEXT PRIMARY KEY)"); err != nil {
			t.Fatalf("建契约表失败: %v", err)
		}
	}
	composed := &composition{db: db, pgDialect: false}
	if err := wireInProcessAccountTestDispatch(composed, runtimeConfig{Secret: ""}, nil); err == nil ||
		!strings.Contains(err.Error(), "JUHE_AI_SECRET") {
		t.Fatalf("空 Secret 必须在 proberepo.NewStore 处报错: %v", err)
	}
}

// TestW2BAccountTestDispatchWireDegradeAndResume 覆盖装配的三个降级/恢复臂：
// 162（契约表缺失 → 告警 + nil 端口降级）、205（启动维护失败 → 告警后继续
// 启动消费循环）与 207（启动维护恢复中断任务 → resumed>0 信息日志）。
func TestW2BAccountTestDispatchWireDegradeAndResume(t *testing.T) {
	cfg := runtimeConfig{Secret: "w2b-secret"}

	// 162：业务库没有 account_test_* 契约表 → 按路由 503 契约降级为 nil 端口。
	plain := w1uOpenPlainSQLite(t, "w2b-account-test-missing.sqlite3")
	if err := wireInProcessAccountTestDispatch(&composition{db: plain, pgDialect: false}, cfg, nil); err != nil {
		t.Fatalf("契约表缺失必须按 nil 端口降级，不得报错: %v", err)
	}

	// 205：契约表存在但列集残缺 → ValidateCoreTables 放行、启动维护查询失败
	// → 告警后继续装配（端口照常接线，路由契约不受影响）。
	minimal := w1uOpenPlainSQLite(t, "w2b-account-test-degraded.sqlite3")
	for _, table := range []string{"account_test_tasks", "account_test_sessions", "account_test_session_tasks"} {
		if _, err := minimal.Exec("CREATE TABLE " + table + " (id TEXT PRIMARY KEY)"); err != nil {
			t.Fatalf("建残缺契约表失败: %v", err)
		}
	}
	degradedStore, err := accounts.NewStore(minimal, false, cfg.Secret, time.Now, newCompositionID)
	if err != nil {
		t.Fatalf("accounts store: %v", err)
	}
	var degradedBuf bytes.Buffer
	previousLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&degradedBuf, nil)))
	degradedComposed := &composition{db: minimal, pgDialect: false}
	if err := wireInProcessAccountTestDispatch(degradedComposed, cfg, degradedStore); err != nil {
		t.Fatalf("启动维护失败必须按告警降级，不得报错: %v", err)
	}
	slog.SetDefault(previousLog)
	if !strings.Contains(degradedBuf.String(), "account_test_queue_start_maintenance_failed") {
		t.Fatalf("启动维护失败必须告警: %s", degradedBuf.String())
	}
	if degradedStore.TestDispatchEffects() == nil {
		t.Fatal("维护失败降级后端口仍必须接线")
	}
	defer degradedComposed.Shutdown()

	// 207：组合根同款装配 + 一条超时的 running 任务 → Start 恢复并入队续跑。
	if testing.Short() {
		t.Skip("composition 装配测试在 -short 下跳过")
	}
	composeCfg := composeTestConfig(t)
	store := openComposeOperationStore(t)
	createRuntimeLogDataset(t, composeCfg.RuntimeLogDatabasePath)
	auditConfig, auditProducer, closeAudit := openComposeAuditSources(t, filepath.Dir(composeCfg.DatasetDatabasePath))
	defer closeAudit()
	composedFull, err := composeSystemAPI(composeCfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), auditProducer, auditConfig)
	if err != nil {
		t.Fatalf("compose system api: %v", err)
	}
	defer composedFull.Shutdown()
	accountStore, err := accounts.NewStore(composedFull.DB, false, composeCfg.Secret, time.Now, newCompositionID)
	if err != nil {
		t.Fatalf("accounts store: %v", err)
	}

	stale := time.Now().UTC().Add(-20 * time.Minute).Format("2006-01-02T15:04:05.000Z07:00")
	if _, err := composedFull.DB.Exec(`INSERT INTO account_test_tasks (
			id, account_id, account_name, provider_code, provider_protocol_profile_id,
			protocol_code, protocol_version, account_type,
			request_system_account_id, request_role, diagnostics,
			model, test_endpoint_mode, draft_account_encrypted,
			status, status_message, cancel_requested, queued_at, created_at, updated_at, started_at
		) VALUES ('task-w2b-resume', 'acct-w2b', '恢复任务', 'openai', 'profile-w2b',
			'openai', 'v1', 'api_key',
			'sys_admin', 'user', 'full',
			'gpt-w2b', 'chat_json', 'junk-envelope',
			'running', '中断恢复', 0, ?, ?, ?, ?)`, stale, stale, stale, stale); err != nil {
		t.Fatalf("seed 中断任务失败: %v", err)
	}

	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	if err := wireInProcessAccountTestDispatch(composedFull, composeCfg, accountStore); err != nil {
		t.Fatalf("wire 进程内测试队列: %v", err)
	}
	slog.SetDefault(previous)

	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(buf.String(), "account_test_queue_resumed") {
		if time.Now().After(deadline) {
			t.Fatalf("启动维护必须恢复中断任务，实际日志: %s", buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// compose_account_balance_health.go：115 / 123
// ---------------------------------------------------------------------------

// TestW2BAccountBalanceHealthURLErrorArms 覆盖 jobs /health 探针的 URL 构造
// 降级臂：115（url.Parse 失败；123 见文件头的不可达证据——二次 Parse 在
// 首次成功的前提下恒可再解析）。
func TestW2BAccountBalanceHealthURLErrorArms(t *testing.T) {
	fetchGuard := func(*http.Request) (*http.Response, error) {
		t.Fatal("URL 构造失败时不得发起 HTTP 探针")
		return nil, nil
	}
	deps := accountBalanceHealthDeps{ProjectorReady: func() bool { return true }, Fetch: fetchGuard}
	getenv := func(endpoint string) func(string) string {
		return func(name string) string {
			switch name {
			case accountBalanceJobsOwnerEnv:
				return "go"
			case accountBalanceJobsHTTPURLEnv:
				return endpoint
			}
			return ""
		}
	}

	health := accountBalanceGoOwnerHealth(getenv("://bad"), deps)
	if !health.Enabled || health.Ready || health.OwnerMode != "" {
		t.Fatalf("非法 URL 必须按 enabled+notReady 降级: %+v", health)
	}
	if health.ProjectorReady == nil || !*health.ProjectorReady {
		t.Fatalf("notReady 仍须携带 projectorReady 事实: %+v", health)
	}

	// Host 的 %3A 在首次 Parse 即被 net/url 的 encodeHost 转义校验拒绝
	// （invalid URL escape）→ 仍走 115 降级臂；该输入合法性断言保持不回归。
	health = accountBalanceGoOwnerHealth(getenv("http://h%3Ax/"), deps)
	if !health.Enabled || health.Ready {
		t.Fatalf("Host 非法转义必须在 Parse 处降级: %+v", health)
	}
}

// ---------------------------------------------------------------------------
// compose_codex_usage_headers.go：66
// ---------------------------------------------------------------------------

// TestW2BCodexUsageHeadersDispatchFailure 覆盖入队失败臂（66）：通道指向已
// 关闭句柄 → Queued=false → catch-warn 契约（不静默）。
func TestW2BCodexUsageHeadersDispatchFailure(t *testing.T) {
	db := w1uOpenPlainSQLite(t, "w2b-codex-closed.sqlite3")
	dispatch, err := tablemonitor.NewDurableRecordMaintenanceDispatch(db, false, time.Now)
	if err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(previous)

	adapter := codexUsageHeadersChannelDispatcher{dispatch: dispatch}
	adapter.PersistOpenAICodexUsageHeaders(context.Background(), "acc-w2b", http.Header{
		"X-Codex-Primary-Used-Percent":        []string{"42"},
		"X-Codex-Primary-Reset-After-Seconds": []string{"3600"},
		"X-Codex-Primary-Window-Minutes":      []string{"300"},
	}, "gateway_error")

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), "gateway_codex_usage_snapshot_side_effect_failed") {
		if time.Now().After(deadline) {
			t.Fatalf("入队失败必须落告警，实际日志: %s", buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// chain_turn_retry_redis.go：137 / 167
// ---------------------------------------------------------------------------

// TestW2BTurnRetryRedisArms 覆盖：137（CompareSetJSON 的 next 不可序列化
// 错误臂，先于任何 Redis IO）与 167（构建失败的告警降级臂）。
func TestW2BTurnRetryRedisArms(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("启动 miniredis 失败: %v", err)
	}
	t.Cleanup(mr.Close)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store, err := newChainTurnRetryRedisStateStore(client, "juhe-ai:dev:w1cover")
	if err != nil || store == nil {
		t.Fatalf("构建 redis 状态存储失败: %v", err)
	}
	// 137：json.Marshal(make(chan int)) 必败，且不得触达 Redis。
	ok, err := store.CompareSetJSON(context.Background(), "w2b-key", nil, map[string]any{"bad": make(chan int)}, 1_000)
	if ok || err == nil {
		t.Fatalf("不可序列化 next 必须返回错误: ok=%t err=%v", ok, err)
	}

	// 167：非 nil client + 空 namespace → 构建失败并降级为 nil（告警面）。
	if store := newChainTurnRetryRedisStateStoreOrNil(&goredis.Client{}, ""); store != nil {
		t.Fatalf("空 namespace 必须降级为 nil store")
	}
}

// ---------------------------------------------------------------------------
// chain_usage.go：55 / 84 / 169 / 184 / 314
// ---------------------------------------------------------------------------

// TestW2BUsageBridgeArms 覆盖用量桥残余臂：55（slogLogger.Error）、84（
// BufferCapacity 缺省 4096）、169（spool 写入失败计数+采样告警）、184（
// Close 幂等）、314（nil recorder 的失败尝试静默契约）。
func TestW2BUsageBridgeArms(t *testing.T) {
	var buf bytes.Buffer
	logger := slogLogger{inner: slog.New(slog.NewTextHandler(&buf, nil))}
	logger.Error("w2b-error-probe", map[string]any{"k": "v"})
	if !strings.Contains(buf.String(), "w2b-error-probe") {
		t.Fatalf("slogLogger.Error 未落日志: %s", buf.String())
	}

	// 169：spool 未启用（Enabled=false）→ Persist 恒错 → 失败计数 + 逐条告警。
	disabledSpool := gatewayusage.NewUsageRecordSpool(gatewayusage.SpoolConfig{}, nil, nil)
	recorder := newSpooledUsageRecorder(usageBridgeConfig{BufferCapacity: 4, Logger: logger.inner}, disabledSpool)
	if err := recorder.EnqueueUsageRecord(context.Background(), gatewayusage.UsageRecordInput{ID: "w2b-u1"}); err != nil {
		t.Fatalf("本地路径入队失败不得上抛: %v", err)
	}
	recorder.Close() // 等待 drain 处理完缓冲后退出
	if recorder.failed != 1 {
		t.Fatalf("spool 写入失败必须计数, failed=%d", recorder.failed)
	}
	if !strings.Contains(buf.String(), "usage_record_spool_persist_failed") {
		t.Fatalf("spool 写入失败必须告警: %s", buf.String())
	}
	recorder.Close() // 184：重复 Close 幂等返回

	// 84：BufferCapacity<=0 → 缺省 4096。
	defaulted := newSpooledUsageRecorder(usageBridgeConfig{}, nil)
	if cap(defaulted.buffered) != 4096 {
		t.Fatalf("缺省缓冲容量 = %d, want 4096", cap(defaulted.buffered))
	}
	defaulted.Close()

	// 314：nil recorder 的失败尝试投递按静默契约返回。
	chainFinalizationUsage{}.RecordFailedUpstreamAttempt(gatewayresponse.FailedAttemptInput{})
}

// ---------------------------------------------------------------------------
// chain_pricing.go：80 / 139 / 174 / 206
// ---------------------------------------------------------------------------

// w2bCatalogReadModels 是 runtime cache 的最小目录读面：fail 置真模拟目录
// 读取失败（负载 goroutine 把错误带回 ListCachedProviderModelCatalogAsync）。
type w2bCatalogReadModels struct {
	gatewayruntimecache.ReadModels
	catalog []gatewayruntimecache.ProviderModelCatalogItem
	fail    bool
}

func (m *w2bCatalogReadModels) ListProviderModelCatalog(context.Context, gatewayruntimecache.ModelCatalogListOptions) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	if m.fail {
		return nil, errors.New("w2b catalog read failure")
	}
	return m.catalog, nil
}

func w2bFloatPtr(value float64) *float64 { return &value }
func w2bIntPtr(value int) *int           { return &value }

// TestW2BPricingCatalogArms 覆盖定价目录残余臂：80/206（目录读取失败 →
// ok=false / nil）、139（缓存读 0 token → 有费率无行 → 0 成本）、174（缓存
// 写 0 token 且无费率 → nil）。
func TestW2BPricingCatalogArms(t *testing.T) {
	failing, err := gatewayruntimecache.New(&w2bCatalogReadModels{fail: true}, gatewayruntimecache.Options{})
	if err != nil {
		t.Fatalf("组装失败目录缓存: %v", err)
	}
	t.Cleanup(failing.Close)

	// 80：in-flight 估算在目录读取失败时按无估算收口。
	estimator := newChainCostEstimator(failing)
	if cost, ok := estimator.EstimateCatalogCostUSD(context.Background(), gatewayquota.CatalogCostInput{
		ProviderCode: "openai", Model: "w2b-m", InputTokens: 10, OutputTokens: 5,
	}); ok || cost != 0 {
		t.Fatalf("目录读取失败必须返回 (0,false)，实际 (%v,%t)", cost, ok)
	}

	// 206：同步定价面在目录读取失败时返回 nil。
	catalog := newChainUsagePricingCatalog(failing)
	if cost := catalog.EstimateCost(gatewayusage.PricingCostInput{Model: "w2b-m", InputTokens: w2bIntPtr(10)}); cost != nil {
		t.Fatalf("目录读取失败时 EstimateCost 必须为 nil，实际 %v", *cost)
	}

	priced := &w2bCatalogReadModels{catalog: []gatewayruntimecache.ProviderModelCatalogItem{{
		ProviderCode:        "openai",
		Model:               "w2b-m",
		Status:              "active",
		InputUsdPer1M:       w2bFloatPtr(1.5),
		CachedInputUsdPer1M: w2bFloatPtr(0.75),
	}}}
	working, err := gatewayruntimecache.New(priced, gatewayruntimecache.Options{})
	if err != nil {
		t.Fatalf("组装目录缓存: %v", err)
	}
	t.Cleanup(working.Close)
	workingCatalog := newChainUsagePricingCatalog(working)

	// 139：缓存读 0 token → 无 cache_read 行但费率存在 → 0 成本。
	if cost := workingCatalog.EstimateCacheReadCost(gatewayusage.PricingCostInput{Model: "w2b-m", CacheReadTokens: w2bIntPtr(0)}); cost == nil || *cost != 0 {
		t.Fatalf("0 缓存读 token 必须得 0 成本，实际 %v", cost)
	}

	// 174：缓存写 0 token 且目录无缓存写费率 → nil（无零成本依据）。
	if cost := workingCatalog.EstimateCacheWriteCost(gatewayusage.PricingCostInput{Model: "w2b-m", CacheWriteTokens: w2bIntPtr(0)}); cost != nil {
		t.Fatalf("无费率时缓存写估算必须为 nil，实际 %v", *cost)
	}
}

// ---------------------------------------------------------------------------
// chain_accounts.go：722 / 725（排序平局规则）+ 353 / 649（选择窗口）
// ---------------------------------------------------------------------------

// TestW2BChainAccountsOrderTieBreaks 覆盖排序比较器的最后两级：原 722 平局
// 死体已于 w3 清理（bucket 全平时 quality 比较对相异行恒非零、以 collator/ID
// 收尾），本测试现触达 717 quality 块与 725 ID 收尾（“名称与 ID 双重复”的行
// 落到 725 返回 false，稳定序保持原位）。
func TestW2BChainAccountsOrderTieBreaks(t *testing.T) {
	duplicate := &chainCandidateRow{ID: "w2b-dup", Name: "重复行", Priority: 1}
	rows := []chainEligibleRow{
		{row: duplicate},
		{row: &chainCandidateRow{ID: "w2b-c", Name: "甲号", Priority: 1}},
		{row: &chainCandidateRow{ID: "w2b-d", Name: "乙号", Priority: 2}},
		{row: duplicate},
	}
	ordered := orderChainCandidateRowsForDispatch(rows, nil)
	if len(ordered) != len(rows) {
		t.Fatalf("排序不得丢行: %d", len(ordered))
	}
	// 甲号(p1) → 重复行对(p1，quality/collator/ID 全平局稳定序) → 乙号(p2)。
	if ordered[0].row.Name != "甲号" || ordered[3].row.Name != "乙号" {
		t.Fatalf("优先级排序错误: %s, %s", ordered[0].row.Name, ordered[3].row.Name)
	}
	if ordered[1].row.ID != "w2b-dup" || ordered[2].row.ID != "w2b-dup" {
		t.Fatalf("重复行必须保留: %s, %s", ordered[1].row.ID, ordered[2].row.ID)
	}
}

// TestW2BChainAccountsSelectionArms 复用 chainFixture 覆盖：
//   - 353：授权实例账户的分组绑定缺 account_authorization_id → 通过访问解析
//     与可调度检查后在绑定门（349-354）被丢弃；
//   - 649：模型窗口去重处 AccountID 为空的行回退 row.ID。
func TestW2BChainAccountsSelectionArms(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}
	credentials, err := accounts.EncryptJSON("chain-test-secret", map[string]any{"api_key": "sk-w2b"})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// 授权实例账户：授权 ID 存在、资源账户存在，但分组绑定不带授权 ID。
	seed(`INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
			grantee_system_account_id, status) VALUES ('auth-w2b', 'account', 'acc_inst', ?, ?, 'active')`,
		fixture.systemAccount, fixture.systemAccount)
	seed(`INSERT INTO accounts (id, system_account_id, provider_code, protocol_code, protocol_version, name, type,
			status, schedulable, credentials_encrypted, authorization_instance_authorization_id,
			authorization_instance_source_account_id)
			VALUES ('acc_inst', ?, 'openai', 'openai', 'v1', '授权实例', 'api_key', 'active', 1, ?, 'auth-w2b', ?)`,
		fixture.systemAccount, credentials, fixture.accountID)
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
			VALUES (?, ?, 'acc_inst', 1, ?)`, fixture.groupID, fixture.systemAccount, now)

	// 353 断言：候选行 2、合格行仅 1（授权实例在绑定门被丢弃）。
	result, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(), fixture.groupID, fixture.systemAccount,
		gatewayruntimecache.OpenAIAccountsForGroupOptions{
			PreResolvedGroupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{
				GroupOwnerSystemAccountID: fixture.systemAccount,
				ProviderCode:              "openai",
			},
		})
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if result.Diagnostics == nil || result.Diagnostics.CandidateRowCount != 2 || result.Diagnostics.EligibleRowCount != 1 {
		t.Fatalf("绑定门未按预期丢弃授权实例: %+v", result.Diagnostics)
	}
	for _, account := range result.Accounts {
		if account.ID == "acc_inst" {
			t.Fatal("缺少绑定授权 ID 的授权实例必须在绑定门被丢弃")
		}
	}

	// 649：空 account_id 的绑定（accounts.id 也为空，INNER JOIN 成立）在模型
	// 窗口去重处回退 row.ID。
	seed(`INSERT INTO accounts (id, system_account_id, provider_code, protocol_code, protocol_version, name, type,
			status, schedulable) VALUES ('', ?, 'openai', 'openai', 'v1', '空ID账户', 'api_key', 'active', 1)`,
		fixture.systemAccount)
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
			VALUES (?, ?, '', 1, ?)`, fixture.groupID, fixture.systemAccount, now)
	modelResult, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(), fixture.groupID, fixture.systemAccount,
		gatewayruntimecache.OpenAIAccountsForGroupOptions{
			RequestedModel:          "gpt-w2b",
			RequestedEndpointFamily: "chat_completions",
			PreResolvedGroupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{
				GroupOwnerSystemAccountID: fixture.systemAccount,
				ProviderCode:              "openai",
			},
		})
	if err != nil {
		t.Fatalf("model window: %v", err)
	}
	if modelResult.Diagnostics == nil || modelResult.Diagnostics.CandidateRowCount != 3 {
		t.Fatalf("模型窗口候选行数异常: %+v", modelResult.Diagnostics)
	}
}

// ---------------------------------------------------------------------------
// chain_accounts.go：529 / 635 / 1457（SQL 窗口与代理行的故障注入）
// ---------------------------------------------------------------------------

// TestW2BChainAccountsScanFaultArms 用少报一列的包装句柄驱动候选窗口 Scan
// 错误臂：529（基础窗口）与 635（模型窗口）。
func TestW2BChainAccountsScanFaultArms(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "w2b-selection-window.sqlite3")
	plain := w2bOpenPlainSQLiteAt(t, dbPath)
	seedChainBusinessSchema(t, plain)
	seedChainStatsSchema(t, plain)
	now := "2026-09-04T00:00:00.000Z"
	if _, err := plain.Exec(`INSERT INTO accounts (id, system_account_id, provider_code, protocol_code,
			protocol_version, name, type, status, schedulable) VALUES ('acc-w2b', 'sys-owner', 'openai', 'openai',
			'v1', 'W2B 账户', 'api_key', 'active', 1)`); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := plain.Exec(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
			VALUES ('grp-w2b', 'sys-owner', 'acc-w2b', 1, ?)`, now); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	wrapped := w2bOpenFaultySQLiteAt(t, dbPath,
		&w2bFaultRule{match: func(query string) bool { return strings.Contains(query, "group_accounts") }, action: w2bFaultShortColumns})
	selector := &chainAccountsSelector{db: wrapped, postgres: false, secret: "chain-test-secret", now: time.Now,
		candidateFinalLimit: 4, candidateScanLimit: 8}
	groupAccess := &gatewayruntimecache.GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys-owner", ProviderCode: "openai"}

	// 529：基础窗口 Scan 数量不匹配。
	if _, err := selector.ListOpenAIAccountsForGroupResult(context.Background(), "grp-w2b", "sys-owner",
		gatewayruntimecache.OpenAIAccountsForGroupOptions{PreResolvedGroupAccess: groupAccess}); err == nil ||
		!strings.Contains(err.Error(), "destination arguments") {
		t.Fatalf("基础窗口必须报 Scan 数量错误: %v", err)
	}

	// 635：模型窗口 Scan 数量不匹配。
	if _, err := selector.ListOpenAIAccountsForGroupResult(context.Background(), "grp-w2b", "sys-owner",
		gatewayruntimecache.OpenAIAccountsForGroupOptions{RequestedModel: "gpt-w2b", PreResolvedGroupAccess: groupAccess}); err == nil ||
		!strings.Contains(err.Error(), "destination arguments") {
		t.Fatalf("模型窗口必须报 Scan 数量错误: %v", err)
	}
}

// TestW2BProxyRowsIterationFault 覆盖 1457：代理行迭代在 rows.Err() 处失败
// （Next 首次即报错 → 循环体不执行 → 错误收口）。
func TestW2BProxyRowsIterationFault(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "w2b-proxy-rows.sqlite3")
	plain := w2bOpenPlainSQLiteAt(t, dbPath)
	if _, err := plain.Exec(`CREATE TABLE IF NOT EXISTS proxy_profiles (id TEXT PRIMARY KEY, name TEXT,
			type TEXT NOT NULL, host TEXT NOT NULL, port INTEGER NOT NULL, username TEXT,
			password_encrypted TEXT, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("建 proxy_profiles 失败: %v", err)
	}
	if _, err := plain.Exec(`INSERT INTO proxy_profiles (id, type, host, port, enabled, created_at, updated_at)
			VALUES ('proxy-w2b', 'http', '10.0.0.1', 8080, 1, '2026-09-04', '2026-09-04')`); err != nil {
		t.Fatalf("seed proxy: %v", err)
	}
	wrapped := w2bOpenFaultySQLiteAt(t, dbPath,
		&w2bFaultRule{match: func(query string) bool { return strings.Contains(query, "FROM proxy_profiles") }, action: w2bFaultNextError})
	selector := &chainAccountsSelector{db: wrapped, postgres: false, secret: "chain-test-secret", now: time.Now,
		candidateFinalLimit: 4, candidateScanLimit: 8}
	if _, err := selector.resolveProxyURLsForProfiles(context.Background(), []string{"proxy-w2b"}); err == nil {
		t.Fatal("代理行迭代失败必须上抛")
	}
}

// ---------------------------------------------------------------------------
// chain_accounts_secret.go：131 / 141（实例 owner 与 profile 覆盖投影）
// ---------------------------------------------------------------------------

// TestW2BAccountSecretInstanceOwnerArms 直驱 openAIAccountSecretFromRow：
// 131（access 无 owner 时回退 authorization_instance_owner_system_account_id）
// 与 141（resource 行的 provider_protocol_profile_id 覆盖）。
func TestW2BAccountSecretInstanceOwnerArms(t *testing.T) {
	const secret = "w2b-secret"
	credentials, err := accounts.EncryptJSON(secret, map[string]any{"api_key": "sk-w2b"})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	selector := &chainAccountsSelector{secret: secret}
	row := &chainCandidateRow{
		ID: "acc-w2b", SystemAccountID: "sys-owner", Name: "W2B 实例", Type: "api_key", Status: "active",
		ResourceCredentialsEncrypted:             sql.NullString{String: credentials, Valid: true},
		AuthorizationInstanceOwnerSystemAccountI: sql.NullString{String: "sys-instance-owner", Valid: true},
		ResourceProviderProtocolProfileID:        sql.NullString{String: "profile_w2b_override", Valid: true},
	}
	access := &chainAccountAccess{accountAccessType: chainAccountAccessAuthorized, accountAuthorizationID: strPtr("auth-w2b")}
	secretOut, err := selector.openAIAccountSecretFromRow(context.Background(), row, &gatewayruntimecache.GroupUsageAccessMetadata{}, access, chainSecretOptions{})
	if err != nil || secretOut == nil {
		t.Fatalf("secret 投影失败: %v", err)
	}
	if secretOut.AccountOwnerSystemAccountID != "sys-instance-owner" {
		t.Fatalf("131: accountOwner = %q, want 实例 owner 回退", secretOut.AccountOwnerSystemAccountID)
	}
	if secretOut.ProviderProtocolProfileID != "profile_w2b_override" {
		t.Fatalf("141: providerProtocolProfileId = %q, want resource 行覆盖", secretOut.ProviderProtocolProfileID)
	}
}

// ---------------------------------------------------------------------------
// compose_accounts_reset.go：476
// ---------------------------------------------------------------------------

// TestW2BResetBridgeInstanceScanFault 覆盖授权实例清单的 Scan 错误臂（476）：
// 包装句柄把结果集报成 0 列 → Scan 数量不匹配。
func TestW2BResetBridgeInstanceScanFault(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "w2b-reset-instances.sqlite3")
	plain := w2bOpenPlainSQLiteAt(t, dbPath)
	if _, err := plain.Exec(`CREATE TABLE IF NOT EXISTS accounts (id TEXT PRIMARY KEY,
			authorization_instance_authorization_id TEXT, deleted_at TEXT)`); err != nil {
		t.Fatalf("建 accounts 失败: %v", err)
	}
	if _, err := plain.Exec(`INSERT INTO accounts (id, authorization_instance_authorization_id) VALUES ('inst-1', 'auth-w2b')`); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	wrapped := w2bOpenFaultySQLiteAt(t, dbPath,
		&w2bFaultRule{match: func(query string) bool {
			return strings.Contains(query, "SELECT id FROM accounts") && strings.Contains(query, "authorization_instance_authorization_id")
		}, action: w2bFaultZeroColumns})
	bridge := &accountsRuntimeResetBridge{db: wrapped, pg: false}
	if _, err := bridge.authorizationInstanceIDs(context.Background(), "auth-w2b"); err == nil ||
		!strings.Contains(err.Error(), "destination arguments") {
		t.Fatalf("实例清单必须报 Scan 数量错误: %v", err)
	}
}

// ---------------------------------------------------------------------------
// chain_catalog.go：277
// ---------------------------------------------------------------------------

// TestW2BCatalogScanBadValueFault 覆盖目录行 JSON 编码错误臂（277）：首行
// 首列被替换为 func() → json.Marshal(map) 必败。
func TestW2BCatalogScanBadValueFault(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "w2b-catalog.sqlite3")
	plain := w2bOpenPlainSQLiteAt(t, dbPath)
	seedChainBusinessSchema(t, plain)
	if _, err := plain.Exec(`INSERT INTO provider_model_catalog (id, provider_code, model, status, source,
			created_at, updated_at) VALUES ('w2b-cat', 'openai', 'w2b-model', 'active', 'built_in', '2026-09-04', '2026-09-04')`); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	wrapped := w2bOpenFaultySQLiteAt(t, dbPath,
		&w2bFaultRule{match: func(query string) bool { return strings.Contains(query, "FROM provider_model_catalog") }, action: w2bFaultBadValue})
	source := &chainCatalogSource{db: wrapped, postgres: false}
	if _, err := source.ListProviderModelCatalog(context.Background(), gatewayruntimecache.ModelCatalogListOptions{ProviderCode: "openai"}); err == nil ||
		!strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("不可序列化目录值必须报 JSON 编码错误: %v", err)
	}
}

// ---------------------------------------------------------------------------
// compose_account_balance_refresh.go：361 / 423
// ---------------------------------------------------------------------------

// TestW2BBalanceDraftProbeExpiredInput 覆盖 TestDraft 的探测错误臂（361）：
// refresher 时钟每次调用前跳 30 秒 → 冻结输入在 ExecuteBalanceQuery 的
// Validate 处过期 → 错误上抛（而非 failed 快照）。
func TestW2BBalanceDraftProbeExpiredInput(t *testing.T) {
	base := time.Now().UTC().Add(-time.Minute)
	calls := 0
	refresher := &gatewayManualBalanceRefresher{
		secret: w1zRefreshSecret,
		now: func() time.Time {
			calls++
			return base.Add(time.Duration(calls) * 30 * time.Second)
		},
	}
	if _, err := refresher.TestDraft(context.Background(), accounts.BalanceDraftProbeInput{
		Credentials: accounts.Credentials{"api_key": "sk-w2b", "base_url": "http://w2b-upstream.invalid/v1"},
		Config:      map[string]any{"adapter": "builtin", "intervalMinutes": 5},
	}); err == nil || !strings.Contains(err.Error(), "过期") {
		t.Fatalf("过期冻结输入必须作为探测错误上抛: %v", err)
	}
}

// TestW2BModelCatalogRefreshSecondListFailure 覆盖 423：上游目录拉取成功后，
// 第二次本地目录投影读（testModels）失败 → 错误上抛。
func TestW2BModelCatalogRefreshSecondListFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"w2b-model"}]}`))
	}))
	t.Cleanup(upstream.Close)

	dbPath := filepath.Join(t.TempDir(), "w2b-catalog-refresh.sqlite3")
	plain := w2bOpenPlainSQLiteAt(t, dbPath)
	seedChainBusinessSchema(t, plain)
	if _, err := plain.Exec(`INSERT INTO provider_model_catalog (id, provider_code, model, status, mode,
			supported_api_protocols_json, input_usd_per_1m, source, created_at, updated_at)
			VALUES ('w2b-cpm', 'gpt', 'w2b-model', 'active', NULL, '["chat_completions"]', 2.0, 'built_in', '2026-09-04', '2026-09-04')`); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}

	rule := &w2bFaultRule{match: func(query string) bool { return strings.Contains(query, "FROM custom_provider_models") },
		action: w2bFaultQueryError, err: errors.New("w2b 第二次目录投影失败"), afterN: 2}
	wrapped := w2bOpenFaultySQLiteAt(t, dbPath, rule)
	wrapped.SetMaxOpenConns(1)
	catalogStore, err := providers.NewStore(wrapped, false, time.Now)
	if err != nil {
		t.Fatalf("providers store: %v", err)
	}
	refresher := &gatewayModelCatalogRefresher{db: wrapped, pg: false, secret: "w2b-refresh-secret", catalog: catalogStore, now: time.Now}
	if _, err := refresher.RefreshDraftModelCatalog(context.Background(), accounts.ModelCatalogDiscoveryInput{
		ProviderCode: "gpt",
		ProtocolCode: "openai",
		AccountType:  "api_key",
		Credentials:  accounts.Credentials{"api_key": "sk-w2b", "base_url": upstream.URL + "/v1"},
	}); err == nil || !strings.Contains(err.Error(), "w2b 第二次目录投影失败") {
		t.Fatalf("第二次目录投影失败必须上抛: %v", err)
	}
}

// ---------------------------------------------------------------------------
// compose_account_balance_refresh.go：118 / 298 / 301（真实 dev PG 门禁批，
// 只触碰临时库 juhe_ai_sub2api_dev_w1cover）
// ---------------------------------------------------------------------------

const w2bRefreshSecret = "w2b-refresh-secret"

// w2bSealedRefreshCandidate 构造凭据已按 w2bRefreshSecret 加封的有效候选行。
func w2bSealedRefreshCandidate(t *testing.T, baseURL string) accounts.BalanceRefreshCandidate {
	t.Helper()
	envelope, err := accounts.EncryptJSON(w2bRefreshSecret, accounts.Credentials{
		"api_key": "sk-w2b", "base_url": baseURL,
	})
	if err != nil {
		t.Fatalf("加密候选凭据失败: %v", err)
	}
	return accounts.BalanceRefreshCandidate{
		ID: "acct-w2b", SystemAccountID: "sys-w2b", ProviderCode: "openai", Type: "api_key",
		Status: "active", Schedulable: true, ConfigRevision: 1, DispatchRevision: 1,
		CredentialsEnvelope: envelope, ConfigJSON: `{"adapter":"builtin","intervalMinutes":5}`,
	}
}

// w2bOpenOutcomeFaultyPG 打开带 LoadOutcome 查询故障注入的临时子库句柄。
func w2bOpenOutcomeFaultyPG(t *testing.T, tempAppURL string, action w2bFaultAction) *sql.DB {
	t.Helper()
	db := sql.OpenDB(&w2bFaultConnector{base: stdlib.GetDefaultDriver(), dsn: tempAppURL, rules: []*w2bFaultRule{{
		match: func(query string) bool {
			return strings.HasPrefix(query, "SELECT payload FROM juhe_jobs.account_balance_outcomes")
		},
		action: action,
		err:    errors.New("w2b 注入 outcome 读错误"),
	}}})
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// w2bNewPGBalanceRefresher 基于（可选故障注入的）PG 句柄构造手动余额刷新
// 适配器；canned doer 承接上游余额 HTTP。
func w2bNewPGBalanceRefresher(t *testing.T, db *sql.DB, ownerID string) *gatewayManualBalanceRefresher {
	t.Helper()
	store, err := platformaccountbalance.OpenStore(platformaccountbalance.StoreConfig{
		Mode:         platformaccountbalance.StorePostgres,
		PostgresPool: sharedDBPool{db: db},
	})
	if err != nil {
		t.Fatalf("open pg balance store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	runner, err := platformaccountbalance.NewRunner(platformaccountbalance.RunnerConfig{
		Store:            store,
		OwnerID:          ownerID,
		CredentialSecret: w2bRefreshSecret,
		HTTPClient:       &cannedJSONDoer{body: `{"unit":"USD","remaining":"12.5"}`},
	})
	if err != nil {
		t.Fatalf("create balance runner: %v", err)
	}
	return &gatewayManualBalanceRefresher{db: db, pg: false, secret: w2bRefreshSecret, store: store, runner: runner, now: time.Now}
}

// TestW2BBalanceRefreshLoadOutcomeArms 覆盖 RefreshManual 的 outcome 回读
// 错误臂（298：LoadOutcome 报错）与未找到臂（301：outcome 查询恒空 →
// “未生成结果”）。依赖真实 dev PG（临时子库 juhe_ai_sub2api_dev_w1cover +
// juhe_jobs 租约四表），不可达即 t.Skip。
func TestW2BBalanceRefreshLoadOutcomeArms(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过真实 dev PG 测试")
	}
	tempAppURL := w1g2CoverPostgres(t)
	bootstrapDB, err := sql.Open("pgx", tempAppURL)
	if err != nil {
		t.Fatalf("打开临时子库失败: %v", err)
	}
	w1uFinalEnsureBalanceJobsSchema(t, bootstrapDB)
	if err := bootstrapDB.Close(); err != nil {
		t.Fatalf("关闭临时子库引导句柄失败: %v", err)
	}

	// 118：组合根装配在空 Secret 下必须于 NewRunner 处 fail closed（组合根
	// 先把目录刷新端口接到 accountStore，账户 store 只需可构造）。
	wireStore, err := accounts.NewStore(w2bOpenOutcomeFaultyPG(t, tempAppURL, w2bFaultNoRows), true, "w2b-wire-secret", time.Now, nil)
	if err != nil {
		t.Fatalf("构造账户 store 失败: %v", err)
	}
	if err := wireInProcessBalanceAndCatalogRefresh(&composition{db: w2bOpenOutcomeFaultyPG(t, tempAppURL, w2bFaultNoRows), pgDialect: true},
		runtimeConfig{Secret: ""}, wireStore, nil); err == nil || !strings.Contains(err.Error(), "create account-balance gateway runner") {
		t.Fatalf("空 Secret 必须在 runner 构造处报错: %v", err)
	}

	// 301：outcome 主键读恒空 → RunManual 执行成功但回读不到结果。
	notFound := w2bNewPGBalanceRefresher(t, w2bOpenOutcomeFaultyPG(t, tempAppURL, w2bFaultNoRows), "w2b-owner-not-found")
	if _, err := notFound.RefreshManual(context.Background(), w2bSealedRefreshCandidate(t, "http://w2b-canned.invalid/v1")); err == nil ||
		!strings.Contains(err.Error(), "outcome 未生成结果") {
		t.Fatalf("outcome 缺失必须报未生成结果: %v", err)
	}

	// 298：outcome 主键读恒错 → 错误原样上抛。
	broken := w2bNewPGBalanceRefresher(t, w2bOpenOutcomeFaultyPG(t, tempAppURL, w2bFaultQueryError), "w2b-owner-broken")
	if _, err := broken.RefreshManual(context.Background(), w2bSealedRefreshCandidate(t, "http://w2b-canned.invalid/v1")); err == nil ||
		!strings.Contains(err.Error(), "w2b 注入 outcome 读错误") {
		t.Fatalf("outcome 回读错误必须原样上抛: %v", err)
	}
}

// TestW2BAuthzExpirySyncReconciledLog 覆盖组件首轮重放的 reconciled>0 臂
// （37→40）：一条已翻转为 expired 的 grant（重扫窗口内）被幂等重放并计数。
func TestW2BAuthzExpirySyncReconciledLog(t *testing.T) {
	db, err := sql.Open("sqlite", "file:w2b-authz-expiry-reconciled?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE resource_authorization_grants (
			id TEXT PRIMARY KEY, resource_type TEXT, resource_id TEXT, resource_owner_system_account_id TEXT,
			grantee_type TEXT, grantee_system_account_id TEXT, grantee_team_id TEXT, status TEXT,
			remark TEXT, expires_at TEXT, limits_json TEXT, created_by TEXT, revoked_by TEXT, revoked_at TEXT,
			created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE request_quota_hourly_window_scope_bindings (
			scope_id TEXT, scope_type TEXT, source_type TEXT, source_id TEXT)`,
		`CREATE TABLE resource_authorizations (
			id TEXT PRIMARY KEY, resource_type TEXT, resource_id TEXT, resource_owner_system_account_id TEXT,
			grantee_system_account_id TEXT, status TEXT, limits_json TEXT,
			effective_source_type TEXT, effective_source_team_id TEXT)`,
		`CREATE TABLE accounts (
			id TEXT PRIMARY KEY, system_account_id TEXT, authorization_instance_authorization_id TEXT,
			authorization_instance_source_account_id TEXT, deleted_at TEXT)`,
		`CREATE TABLE resource_authorization_sources (
			authorization_id TEXT, source_type TEXT, source_team_id TEXT)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("建 authz 表失败: %v", err)
		}
	}
	nowText := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := db.Exec(`INSERT INTO resource_authorization_grants (id, resource_type, resource_id,
			resource_owner_system_account_id, grantee_type, status, created_at, updated_at)
			VALUES ('grant-w2b-expired', 'account', 'acct-w2b', 'sys-w2b', 'system_account', 'expired', ?, ?)`,
		nowText, nowText); err != nil {
		t.Fatalf("seed expired grant: %v", err)
	}
	store, err := authz.NewStore(db, false, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	// 组件首轮 runPass 即重放该 grant → reconciled>0 信息日志（37→40）。
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	component := newAuthzExpiryRuntimeSyncComponent(store)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- component.Run(runCtx) }()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(buf.String(), "authz_expiry_runtime_sync_reconciled") {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("reconciled>0 必须落信息日志，实际: %s", buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	slog.SetDefault(previous)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("component must report the cancel cause on exit")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("component must exit promptly after ctx cancel")
	}
}

// ---------------------------------------------------------------------------
// compose_authz_expiry_sync.go：47（慢速批：固定 1 分钟节拍）
// ---------------------------------------------------------------------------

// TestW2BAuthzExpirySyncTickerPass 覆盖组件运行循环的节拍臂（47→48）：
// 通过注入短节拍（生产装配不传参仍为固定 1 分钟），首跑后等待多次 tick
// 再取消退出，约 0.5 秒完成，失败信息不携带任何连接事实。
func TestW2BAuthzExpirySyncTickerPass(t *testing.T) {
	db, err := sql.Open("sqlite", "file:w2b-authz-expiry-tick?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := authz.NewStore(db, false, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	component := newAuthzExpiryRuntimeSyncComponent(store, 20*time.Millisecond)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- component.Run(runCtx) }()
	// 等待注入节拍多次触发后再取消。
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("component must report the cancel cause on exit")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("component must exit promptly after ctx cancel")
	}
}
