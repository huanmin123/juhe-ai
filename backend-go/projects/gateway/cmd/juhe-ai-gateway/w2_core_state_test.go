package main

// w2（调度与状态核心组残余覆盖批次，w2a/TestW2A 前缀）：对照
// %TEMP%/w1pkg-FINAL2.out 的零计数块逐块补测。全部进程内确定性驱动，
// 不启动插桩二进制、不连接真实 dev PG/Redis（所有目标臂都可用
// 关闭的 sqlite / 拒连 redis 地址 / 脚本化 database/sql driver 触达）。
//
// 覆盖手段：
//  1. 直接调用包内函数/方法（error_policy 纯函数、routing 投影、
//     preflight 窄口守卫、apikey_rotation / turn_retry redis 适配器的
//     构造期错误臂）。
//  2. 关闭的 sqlite 句柄 / 视图表制造 SQL 错误（error_policy_effects
//     写侧、routing cache 读侧）。
//  3. 脚本化 database/sql driver（w2aScriptedSQL）：按调用次序返回行/
//     错误/RowsAffected，覆盖 chain_account_locks 的 CAS 竞争与事务
//     错误臂、newChainListAvailabilityDirtyMarker 闭包错误臂。
//  4. 组合根直驱 composeChainRuntimeServices（sqlite fixture schema +
//     memory/redis 驱动轴变体）覆盖 quota shared namespace、key-model
//     选择器错误臂与 keyStates/效果链运行态失效闭包。
//
// 恒不可达守卫登记（证据见各块注释，未硬凑）：
//   - chain_apikey_rotation_redis.go 74（INCR ≥ 1 + Lua 取模非负，
//     modulo<=0 已被 57 行守卫）。（已于 w3 清理）
//   - chain_runtime.go 252（NewRedisSharedCacheFactory 与 redis.ParseURL
//     对同一 URL 用同一解析器，前者成功则后者必成功）、301
//     （newChainCatalogSource 仅在 db==nil 报错，282 行 NewSQLReadModels
//     已先因 db==nil 失败）、320（gatewayruntimecache.New 仅在
//     models==nil 报错，models 由 282 行成功构造后非 nil）、348
//     （CacheDriverRedis 与 redisCache 判据同为 cfg.CacheDriver=="redis"，
//     Shared=factory 仅在 factory 失败时为 nil，而 factory 失败已在 247
//     返回）、359（NewAvoidance 与 NewErrorCircuit 以相同 URL/namespace
//     走同一 NewRedisRuntimeStateStore，circuit 先失败）、393
//     （NewStatsStore 仅在 statsDB==nil 报错，338 行 NewSQLPolicySource
//     已先因 statsDB==nil 失败）、400/466/521/566/628/656（namespace 为
//     空才能触发 SanitizeRedisNamespacePart 报错，而 redisState=true 时
//     327 行 NewErrorCircuit 已先因空 namespace 失败；client 非 nil 由
//     262 行构造保证）、405（runtimeState 非 nil 恰为
//     redisCache&&redisState 的结果，Modes 同源，条件恒 false）、426
//     （Timezone 为结构体值恒非 nil、Snapshot 由 404 行成功构造非 nil、
//     Shared nil 蕴含 Modes.RedisCache false）、431（与 414 行同一
//     newQuotaShared 调用，同参同果，先失败先返回）、443（Business/
//     Stats/Timezone 均非 nil）、458（NewInflightQuotaService 无错误
//     返回）、488（NewClientIPConcurrency 与 NewErrorCircuit 同 URL 判
//     空/解析，circuit 先失败）、497（newChainAccountCircuitService 的
//     URL 判空与 327 行 circuit 相同，capacity/closedRetention 为常量，
//     NewCircuitService 零值 options 不报错）、664（NewIdentityService
//     仅在空 secret 报错，空 secret 已在 606 行 accountkeystates.NewStore
//     先失败）、681（NewAffinityService 的 validateIdentitySecret 同空
//     secret 条件，同上先失败）、526（ProxyHealthService 的唯一 logWarn
//     触发点是上游桶避让 TTL 到期后的半开探测租约发放，需要桶先进入
//     failure 态且避让到期，其驱动入口在派发循环内部的桶健康评估，
//     进程内无同步公共入口；半开发放的另一半 929 行需 redis 桶状态 CAS
//     成功，同因不可达）。（w3 复核：以上 chain_runtime.go 各块均为组合根
//     构造器调用点的 err 守卫，同参上游先失败，按铁律保留不删）
//   - chain_routing.go 500（OrderedJSON 只承载 JSON 对象，
//     orderedValueToPlain(*OrderedJSON) 恒为 map[string]any，!ok 分支
//     死代码）（已于 w3 清理）、672（与 669-671 行逐字
//     重复的第二次 "?" 截断，第一次之后 path 不再含 "?"）（已于 w3 清理）。
//   - chain_dispatch.go 547（state.UntilMs > now 在 539 行已判，
//     同一 now 下 547 行 retryAtMs<=now 恒假）、565（nextRetryAtMs 取
//     自幸存面外的 UntilMs，恒 > now，retryAfter 恒 > 0）、705
//     （settings 校验强制 defaultTemporaryUnschedulableMinutes ∈ [1,1440]，
//     ttlSeconds = minutes*60 恒 >= 60，< 1 钳制不可达）。
//   - chain_preflight.go 321（Acquire 内部 135 行先检 ctx.Err()，
//     已取消即返回 aborted；Acquire 与 321 行之间无挂起点，同帧内
//     ctx 无法从 nil 变为 cancelled，除非引入并发取消的非确定性时序）。
//     （w3 保留：该分支是并发取消测试手段的限制，不是死代码，维持登记）
//   - chain_error_policy_effects.go 189（accountkeystates.NewStore 仅在
//     db==nil / 空 secret 报错，172/175 行已先守卫）、782
//     （Go 1.24+ crypto/rand.Read 恒成功）。
//   - chain_account_locks.go 530（NormalizeLockRetryIntervalSeconds
//     收敛到 5..30 秒，base>=5000ms，jitter 上限 2000/5000ms，
//     delay 下限 3000ms，恒 >= 2000）、717（同 crypto/rand）。
//   - chain_request_failure_health.go 181（chainHealthDispatchSourceFence
//     为 string/int64 平面结构，json.Marshal 恒成功）（w3 保留：属惯例
//     json.Marshal 调用点 err 处理臂，按铁律不删）、253（keys 与
//     order 严格同步维护，len(keys)>cap>=1 蕴含 order 非空）（已于 w3 清理）。
//   - chain_error_policy.go 237（systemQuotaDecision 的 policy 已经过
//     quotaRecoveryPolicyRead 严格校验（timezone 合法），boundary 重放
//     同一 LoadLocation 恒成功）、1335（入参恒为 string，
//     sanitizeDiagnosticValue 的 string 分支恒返回 string）。
//   - chain_accounts_traffic_migration.go 46（redis 驱动下 migrate 的
//     candidate keys 读恒先触 redis，migrate 失败先于 remember；memory
//     驱动下 remember 恒成功，ThrowOnRedisError 分支无失败源）。
//     （w3 保留：行为顺序依赖，不是死代码，维持登记）。
//
// 隔离说明：全部使用进程内 sqlite（t.TempDir）与 redis.ParseURL 可解析
// 但拒连的 127.0.0.1:1 地址，不触碰任何真实数据库与 Redis 实例。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accountkeystates"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/settings"
)

// ---------------------------------------------------------------------------
// 共享小工具
// ---------------------------------------------------------------------------

// w2aRefusedRedisClient 返回指向 127.0.0.1:1（拒连端口）的 redis 客户端：
// 构造恒成功，首次命令即报错。
func w2aRefusedRedisClient() *redis.Client {
	return redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond})
}

// w2aModelPtr 返回字符串指针。
func w2aModelPtr(value string) *string { return &value }

// w2aGatewayRequestWithModel 构造携带 body state 模型的 GatewayRequest。
func w2aGatewayRequestWithModel(rawBody string) *gatewaypreauth.GatewayRequest {
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	httpReq.Header.Set("Content-Type", "application/json")
	body := &gatewaybody.Request{State: &gatewaybody.BodyState{Model: w2aModelPtr("gpt-test")}}
	if rawBody != "" {
		body.RawBody = []byte(rawBody)
	}
	return &gatewaypreauth.GatewayRequest{HTTP: httpReq, Body: body}
}

// w2aClosedSQLiteDB 打开随后立即关闭的 sqlite 句柄（后续所有 SQL 报
// "sql: database is closed"）。
func w2aClosedSQLiteDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/w2a-closed.sqlite3")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}
	return db
}

// w2aBrokenRoutingCache 构造底库已关闭的运行态缓存服务。
func w2aBrokenRoutingCache(t *testing.T) *gatewayruntimecache.Service {
	t.Helper()
	models, err := gatewayruntimecache.NewSQLReadModels(w2aClosedSQLiteDB(t), false, time.Now, nil, nil, nil)
	if err != nil {
		t.Fatalf("create read models: %v", err)
	}
	cache, err := gatewayruntimecache.New(models, gatewayruntimecache.Options{})
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	t.Cleanup(cache.Close)
	return cache
}

// w2aFixtureDatabases 复用 chain_test.go 的种子 schema 建独立 sqlite
// 业务库/统计库（不与既有 fixture 共享文件）。
func w2aFixtureDatabases(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	root := t.TempDir()
	db, err := sql.Open("sqlite", root+"/w2a-business.sqlite3")
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	statsDB, err := sql.Open("sqlite", root+"/w2a-stats.sqlite3")
	if err != nil {
		t.Fatalf("open stats db: %v", err)
	}
	t.Cleanup(func() { _ = statsDB.Close() })
	seedChainBusinessSchema(t, db)
	seedChainStatsSchema(t, statsDB)
	// 系统设置：settings.Load 断言 SystemSettingKeys 全部存在（仅 5 个
	// legacy 键有兼容默认值），projectGatewaySettings 又对网关子集做区间
	// 校验。这里按双区间写全量种子，保证 ReadCachedGatewaySettings 可用。
	settingValues := map[string]string{
		"gatewayTextRawBodyLimitMegabytes":           "16",
		"accountCircuitConfirmationFailuresRequired": "2",
		"gatewayUserRequestLimitPerMinute":           "0",
		"gatewayUserRequestLimitPerDay":              "0",
		"gatewayUserRequestLimitPerWeek":             "0",
		"gatewayUserRequestLimitPerMonth":            "0",
		"userAiAccountLimit":                         "100",
		"systemApiRateLimitIpReadPerMinute":          "60",
		"systemApiRateLimitIpReadBurstPer10Seconds":  "60",
		"systemApiRateLimitIpWritePerMinute":         "60",
		"systemApiRateLimitIpWriteBurstPer10Seconds": "60",
		"systemApiRateLimitUserReadPerMinute":        "60",
		"systemApiRateLimitUserWritePerMinute":       "60",
		"defaultTemporaryUnschedulableMinutes":       "5",
		"temporaryUnschedulableRetryIntervalSeconds": "60",
		"temporaryUnschedulableRetryAttempts":        "2",
		"textFirstResponseTimeoutSeconds":            "60",
		"textNonStreamFirstResponseTimeoutSeconds":   "600",
		"textStreamIdleTimeoutSeconds":               "60",
		"textUncommittedAttemptMaxLifetimeSeconds":   "300",
		"imageFirstResponseTimeoutSeconds":           "60",
		"imageStreamIdleTimeoutSeconds":              "60",
		"imageUncommittedAttemptMaxLifetimeSeconds":  "300",
		"imageRequestWallTimeoutSeconds":             "600",
		"chatImageGenerationTotalTimeoutSeconds":     "300",
		"noAvailableAccountWaitTimeoutSeconds":       "30",
		"streamFailureThresholdCount":                "5",
		"streamFailureThresholdWindowMinutes":        "5",
		"operationLogRetentionDays":                  "30",
		"operationLogMaxChangesPerRecord":            "100",
		"statsAggregationIntervalSeconds":            "60",
		"statsAggregationBatchSize":                  "500",
		"statsAggregationMaxBatchesPerRun":           "10",
		"usageHotWindowRefreshIntervalSeconds":       "60",
		"groupAccountStatsRefreshIntervalSeconds":    "60",
		"systemMetricsSampleIntervalSeconds":         "60",
		"tableMonitorMaxTablesPerRun":                "10",
		"accountQualityRefreshIntervalSeconds":       "300",
		"accountQualityWindowMinutes":                "15",
		"accountHealthCheckIntervalHours":            "1",
		"accountHealthCheckJitterMinutes":            "0",
		"accountHealthCheckFailureThreshold":         "3",
		"cooldownAccountRetestIntervalSeconds":       "60",
		"cooldownAccountRetestMaxBackoffHours":       "24",
		"oauthAccessTokenRefreshIntervalSeconds":     "60",
		"oauthAccessTokenRefreshLeadSeconds":         "300",
		"oauthAccessTokenRefreshBatchSize":           "10",
		"oauthAccessTokenRefreshRetryBackoffSeconds": "60",
		"modelCheckRetentionDays":                    "30",
		"runtimeLogIndexRetentionDays":               "7",
		"publicApiLogRetentionDays":                  "30",
		"usageRecordRetentionDays":                   "30",
		"usageStatsTimezone":                         `"UTC"`,
		"usageStatsMinuteRetentionHours":             "24",
		"usageStatsHourlyRetentionDays":              "7",
		"usageStatsDailyRetentionDays":               "90",
		"usageStatsWeeklyRetentionWeeks":             "52",
		"usageStatsMonthlyRetentionMonths":           "12",
		"usageRankSnapshotRetentionDays":             "30",
		"systemMetricsRetentionDays":                 "3",
		"systemMetricsHourlyRetentionDays":           "7",
	}
	now := "2026-09-14T00:00:00.000Z"
	for _, key := range settings.SystemSettingKeys {
		value, ok := settingValues[key]
		if !ok {
			t.Fatalf("missing seed value for system setting %s", key)
		}
		if _, err := db.Exec(`INSERT INTO system_settings (system_account_id, key, value_json, updated_at)
			VALUES ('sys_admin', ?, ?, ?)`, key, value, now); err != nil {
			t.Fatalf("seed setting %s: %v", key, err)
		}
	}
	// chain_test.go 的精简版 account_api_key_runtime_states 缺少
	// accountkeystates 真实 DDL 的列；在自家测试库实例上替换为真实形状，
	// 并补齐 group_account_stats_dirty（Go-owned 交接表）。
	for _, statement := range []string{
		`DROP TABLE account_api_key_runtime_states`,
		`CREATE TABLE account_api_key_runtime_states (
			id TEXT PRIMARY KEY,
			system_account_id TEXT NOT NULL,
			account_id TEXT NOT NULL,
			key_fingerprint TEXT NOT NULL,
			key_index INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL,
			failure_count INTEGER NOT NULL DEFAULT 0,
			consecutive_failures INTEGER NOT NULL DEFAULT 0,
			success_count INTEGER NOT NULL DEFAULT 0,
			cooldown_until TEXT,
			next_probe_at TEXT,
			probe_backoff_seconds INTEGER NOT NULL DEFAULT 0,
			recovery_started_at TEXT,
			last_attempt_at TEXT,
			last_success_at TEXT,
			last_failure_at TEXT,
			last_error_code TEXT,
			last_error_message TEXT,
			last_trace_id TEXT,
			last_probe_at TEXT,
			probe_claim_token TEXT,
			probe_claimed_until TEXT,
			created_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT '',
			UNIQUE(account_id, key_fingerprint)
		)`,
		`CREATE TABLE IF NOT EXISTS group_account_stats_dirty (group_id TEXT PRIMARY KEY, reason TEXT, updated_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("upgrade business schema: %v: %v", statement, err)
		}
	}
	return db, statsDB
}

// ---------------------------------------------------------------------------
// 脚本化 database/sql driver（w2aScriptedSQL）
// ---------------------------------------------------------------------------

// w2aScriptedStep 脚本的一步：按次序匹配下一条 Query/Exec/Begin/Commit。
type w2aScriptedStep struct {
	contains    string         // 语句必须包含的子串（空 = 不校验）
	cols        []string       // Query 返回列名
	row         []driver.Value // Query 返回的单行（nil = 无行）
	rowsErr     error          // Query 直接返回的错误
	rowsEOFErr  error          // 行耗尽后 Err() 返回的错误
	scanAsNil   bool           // 单行首列返回 NULL（驱动 Scan 报错）
	noRows      bool           // 返回零行（Scan 得 sql.ErrNoRows）
	execErr     error          // Exec 返回的错误
	affected    int64          // Exec 的 RowsAffected
	affectedErr error          // RowsAffected 返回的错误
	beginErr    error          // Begin 返回的错误
	commitErr   error          // Tx.Commit 返回的错误
}

// w2aScriptedDriverSeq 保证一次性驱动的注册名唯一（Windows 时间戳粒度不足）。
var w2aScriptedDriverSeq int64

// w2aScriptedScript 共享的步骤队列（单连接串行消费）。
type w2aScriptedScript struct {
	steps []w2aScriptedStep
}

func (s *w2aScriptedScript) next(kind, query string) (*w2aScriptedStep, error) {
	if len(s.steps) == 0 {
		return nil, fmt.Errorf("w2a scripted script exhausted at %s: %s", kind, query)
	}
	step := &s.steps[0]
	s.steps = s.steps[1:]
	if step.contains != "" && !strings.Contains(query, step.contains) {
		return nil, fmt.Errorf("w2a scripted step %d mismatch: want %q got %s: %s", len(s.steps), step.contains, kind, query)
	}
	return step, nil
}

type w2aScriptedConn struct{ script *w2aScriptedScript }

func (c *w2aScriptedConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w2a scripted: Prepare unsupported")
}
func (c *w2aScriptedConn) Close() error { return nil }
func (c *w2aScriptedConn) Begin() (driver.Tx, error) {
	step, err := c.script.next("Begin", "BEGIN")
	if err != nil {
		return nil, err
	}
	if step.beginErr != nil {
		return nil, step.beginErr
	}
	return &w2aScriptedTx{script: c.script}, nil
}

func (c *w2aScriptedConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	step, err := c.script.next("Query", query)
	if err != nil {
		return nil, err
	}
	if step.rowsErr != nil {
		return nil, step.rowsErr
	}
	if step.noRows || (step.row == nil && step.scanAsNil == false && step.rowsEOFErr == nil && step.cols == nil) {
		return &w2aScriptedRows{cols: []string{"x"}}, nil
	}
	cols := step.cols
	if cols == nil {
		cols = []string{"id"}
	}
	row := step.row
	if step.scanAsNil {
		row = []driver.Value{nil}
	}
	if step.rowsEOFErr != nil {
		return &w2aScriptedRows{cols: cols, eofErr: step.rowsEOFErr}, nil
	}
	if row == nil {
		return &w2aScriptedRows{cols: cols}, nil
	}
	return &w2aScriptedRows{cols: cols, values: [][]driver.Value{row}}, nil
}

func (c *w2aScriptedConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	step, err := c.script.next("Exec", query)
	if err != nil {
		return nil, err
	}
	if step.execErr != nil {
		return nil, step.execErr
	}
	return w2aScriptedResult{affected: step.affected, err: step.affectedErr}, nil
}

type w2aScriptedTx struct{ script *w2aScriptedScript }

func (t *w2aScriptedTx) Commit() error {
	step, err := t.script.next("Commit", "COMMIT")
	if err != nil {
		return err
	}
	return step.commitErr
}
func (t *w2aScriptedTx) Rollback() error { return nil }

type w2aScriptedResult struct {
	affected int64
	err      error
}

func (r w2aScriptedResult) LastInsertId() (int64, error) { return 0, nil }
func (r w2aScriptedResult) RowsAffected() (int64, error) { return r.affected, r.err }

type w2aScriptedRows struct {
	cols   []string
	values [][]driver.Value
	eofErr error
	index  int
}

func (r *w2aScriptedRows) Columns() []string { return r.cols }
func (r *w2aScriptedRows) Close() error      { return nil }
func (r *w2aScriptedRows) Next(dest []driver.Value) error {
	if r.index < len(r.values) {
		copy(dest, r.values[r.index])
		r.index++
		return nil
	}
	if r.eofErr != nil {
		// database/sql 只在 Next 返回非 io.EOF 错误时把它暴露给
		// sql.Rows.Err()，因此行错误必须从 Next 本身返回。
		return r.eofErr
	}
	return io.EOF
}
func (r *w2aScriptedRows) Err() error { return nil }

type w2aScriptedDriver struct{ script *w2aScriptedScript }

func (d w2aScriptedDriver) Open(string) (driver.Conn, error) {
	return &w2aScriptedConn{script: d.script}, nil
}

// w2aOpenScriptedDB 注册一次性驱动并返回按脚本应答的 *sql.DB。
func w2aOpenScriptedDB(t *testing.T, steps []w2aScriptedStep) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("w2a-scripted-%d", atomic.AddInt64(&w2aScriptedDriverSeq, 1))
	sql.Register(name, w2aScriptedDriver{script: &w2aScriptedScript{steps: steps}})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("open scripted db: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ---------------------------------------------------------------------------
// 恒报错的运行态存储桩（gatewayproxyhealth.RuntimeStateStore 全方法）
// ---------------------------------------------------------------------------

type w2aFailingRuntimeStateStore struct{}

func (w2aFailingRuntimeStateStore) GetJSON(context.Context, string) (json.RawMessage, error) {
	return nil, errors.New("w2a: runtime state store unavailable")
}
func (w2aFailingRuntimeStateStore) GetJSONMany(ctx context.Context, keys []string) ([]json.RawMessage, error) {
	return make([]json.RawMessage, len(keys)), errors.New("w2a: runtime state store unavailable")
}
func (w2aFailingRuntimeStateStore) SetJSON(context.Context, string, any, int64) error {
	return errors.New("w2a: runtime state store unavailable")
}
func (w2aFailingRuntimeStateStore) CompareSetJSON(context.Context, string, json.RawMessage, any, int64) (bool, error) {
	return false, errors.New("w2a: runtime state store unavailable")
}
func (w2aFailingRuntimeStateStore) CompareDeleteJSON(context.Context, string, json.RawMessage) (bool, error) {
	return false, errors.New("w2a: runtime state store unavailable")
}
func (w2aFailingRuntimeStateStore) Delete(context.Context, string) error {
	return errors.New("w2a: runtime state store unavailable")
}
func (w2aFailingRuntimeStateStore) AcquireLock(context.Context, string, int64, string) (bool, error) {
	return false, errors.New("w2a: runtime state store unavailable")
}
func (w2aFailingRuntimeStateStore) RenewLock(context.Context, string, int64, string) (bool, error) {
	return false, errors.New("w2a: runtime state store unavailable")
}
func (w2aFailingRuntimeStateStore) ReleaseLock(context.Context, string, string) error {
	return errors.New("w2a: runtime state store unavailable")
}

// w2aErrResult 是 RowsAffected 报错的 sql.Result。
type w2aErrResult struct{}

func (w2aErrResult) LastInsertId() (int64, error) { return 0, nil }
func (w2aErrResult) RowsAffected() (int64, error) {
	return 0, errors.New("w2a: rows affected unavailable")
}

// ---------------------------------------------------------------------------
// chain_apikey_rotation_redis.go：63（ctx == nil 守卫）
// ---------------------------------------------------------------------------

func TestW2AAPIKeyRotationNilContextGuard(t *testing.T) {
	counter := &chainAPIKeyRotationRedisCounter{client: w2aRefusedRedisClient()}
	defer func() { _ = counter.client.Close() }()
	// ctx 为 nil 且 modulo > 0：63-65 行把 nil 换成 context.Background()，
	// 随后脚本执行在拒连客户端上失败（68-73 行错误出口）。
	_, err := counter.NextIndex(nil, "w2a-acc", "juhe-ai:dev:w1cover:rot", 5)
	if err == nil {
		t.Fatalf("NextIndex with nil ctx on refused client must fail")
	}
	// modulo <= 0 的直通臂（57-59 行）保持既有覆盖。
	if index, err := counter.NextIndex(context.Background(), "w2a-acc", "juhe-ai:dev:w1cover:rot", 0); err != nil || index != 0 {
		t.Fatalf("modulo<=0 must return 0,nil, got %d,%v", index, err)
	}
}

// ---------------------------------------------------------------------------
// chain_turn_retry_redis.go：137（CompareSetJSON marshal 错误）、167（OrNil
// 构建失败日志）
// ---------------------------------------------------------------------------

func TestW2ATurnRetryRedisMarshalFailureAndNilNamespace(t *testing.T) {
	client := w2aRefusedRedisClient()
	defer func() { _ = client.Close() }()
	store, err := newChainTurnRetryRedisStateStore(client, "juhe-ai:dev:w1cover")
	if err != nil || store == nil {
		t.Fatalf("build store: %v,%v", store, err)
	}
	// next 值不可 JSON 编码：137-139 行在触 redis 前直接报错。
	if _, err := store.CompareSetJSON(context.Background(), "w2a-key", nil, func() {}, 1000); err == nil {
		t.Fatalf("CompareSetJSON with unencodable next must fail")
	}
	// namespace 非法：newChainTurnRetryRedisStateStoreOrNil 记录错误并返回
	// nil（167-170 行），客户端不参与拨号。
	if store := newChainTurnRetryRedisStateStoreOrNil(client, "   "); store != nil {
		t.Fatalf("empty namespace must yield nil store, got %T", store)
	}
}

// ---------------------------------------------------------------------------
// chain_preflight.go：40 / 139 / 247 / 280
// ---------------------------------------------------------------------------

func TestW2APreflightNarrowGuards(t *testing.T) {
	// 40-42：空 API Key 直接未命中。
	validator := &chainAPIKeyValidator{}
	if row, err := validator.Validate(context.Background(), ""); err != nil || row != nil {
		t.Fatalf("empty key must be (nil,nil), got %v,%v", row, err)
	}

	// 40-42：运行态读取错误直接上抛（关闭句柄的缓存）。
	validator = &chainAPIKeyValidator{cache: w2aBrokenRoutingCache(t)}
	if _, err := validator.Validate(context.Background(), "w2a-key"); err == nil {
		t.Fatalf("validate over closed cache must fail")
	}

	// 139-141：req 缺失时超限降级检查直接放行。
	preflight := &chainImagePreflight{}
	if preflight.rejectOversizedAutoDowngrade(gatewaypreauth.ImagePermissionPreflightInput{}) {
		t.Fatalf("nil request must not be rejected")
	}

	// 247-249：nil 接收器的 body 准入门直通。
	var gate *chainSpeedFirstBodyAdmissionGate
	outcome, err := gate.AdmitBody(context.Background(), nil, nil, gatewayproto.LaneText)
	if err != nil || outcome.Handled || outcome.Release != nil {
		t.Fatalf("nil gate must pass through, got %+v,%v", outcome, err)
	}
}

func TestW2ASpeedFirstAdmissionCtxCancelledAborts(t *testing.T) {
	// 280-286：适用面成立但 ctx 已取消 → Acquire 返回 aborted → 记录
	// aborted 阶段并 Handled。
	gatewayhotquality.ClearSpeedFirstBodyAdmissionsForTest()
	defer gatewayhotquality.ClearSpeedFirstBodyAdmissionsForTest()
	obs := &chainCapturedObservability{}
	gate := newChainSpeedFirstGateForTest(obs)
	req, res, _ := speedFirstRequest(t, speedFirstRuntime(nil, nil, nil, nil, 2))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome, err := gate.AdmitBody(ctx, req, res, gatewayproto.LaneText)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if !outcome.Handled || outcome.Release != nil {
		t.Fatalf("cancelled ctx must be handled, got %+v", outcome)
	}
	found := false
	for _, stage := range obs.snapshotStages() {
		if stage.stage == "body.speed_first_admission" && stage.outcome == "aborted" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing aborted stage: %+v", obs.snapshotStages())
	}
}

// ---------------------------------------------------------------------------
// chain_request_failure_health.go：144（索引建失败）
// ---------------------------------------------------------------------------

func TestW2AProbeOutboxIndexCreateFailure(t *testing.T) {
	db, err := sql.Open("sqlite", t.TempDir()+"/w2a-outbox.sqlite3")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer func() { _ = db.Close() }()
	// 预置同名表占用索引名：CREATE TABLE IF NOT EXISTS 幂等成功（既有表
	// 即可），随后 CREATE INDEX IF NOT EXISTS 因索引名与表名冲突而失败
	// （SQLite 中索引与表共用命名空间）。
	for _, statement := range []string{
		`CREATE TABLE account_health_probe_request_outbox (request_id TEXT PRIMARY KEY, account_id TEXT NOT NULL, reason TEXT NOT NULL, trace_id TEXT NOT NULL DEFAULT '', source_fence TEXT NOT NULL DEFAULT '', deadline_at TEXT NOT NULL, status TEXT NOT NULL, consumed_at TEXT, available_at TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE idx_account_health_probe_request_outbox_pending (placeholder TEXT)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v: %v", statement, err)
		}
	}
	writer := newChainProbeRequestOutboxWriter(db, false, 1000)
	outcome := writer.EnqueueProbeRequest(context.Background(), "w2a-acc", chainRequestFailureReason, "", nil)
	if outcome.Outcome != gatewaycodex.HealthDispatchRejected || outcome.DecisionCode != "input_unavailable" {
		t.Fatalf("index failure must reject as input_unavailable, got %+v", outcome)
	}
	if writer.ensureErr == nil {
		t.Fatalf("ensureErr must be captured")
	}
}

// ---------------------------------------------------------------------------
// chain_error_policy.go：400 / 417 / 502 / 555 / 853 / 885 / 960
// ---------------------------------------------------------------------------

func TestW2ASystemQuotaMatcherExclusionArms(t *testing.T) {
	// 400-402：errorType 命中非额度 403 标识 → 明确不匹配。
	if systemInsufficientQuotaRuleMatches(http.StatusForbidden, "", "forbidden", "") {
		t.Fatalf("forbidden type on 403 must not match system quota rule")
	}
	// 417：402/403、无标识、无文本标记 → 走到末端 return false。
	if systemInsufficientQuotaRuleMatches(http.StatusForbidden, "", "", "upstream exploded") {
		t.Fatalf("403 without markers must not match")
	}
	// 对照组：402 + 空 code/type → 403 行 return true（既有覆盖）。
	if !systemInsufficientQuotaRuleMatches(http.StatusPaymentRequired, "", "", "") {
		t.Fatalf("bare 402 must match")
	}
}

func TestW2AAccountRuleReadValidationArms(t *testing.T) {
	base := map[string]any{
		"enabled":     true,
		"name":        "w2a-rule",
		"priority":    float64(1),
		"action":      "temp_unschedulable",
		"statusCodes": []any{float64(429)},
	}
	// 502-504：error_codes 元素非字符串。
	badCodes := map[string]any{
		"enabled": true, "name": "w2a-rule", "priority": float64(1), "action": "temp_unschedulable",
		"status_codes": []any{float64(429)}, "error_codes": []any{float64(42)},
	}
	if _, err := accountErrorHandlingRuleRead(badCodes, 1); err == nil || !strings.Contains(err.Error(), "字符串数组") {
		t.Fatalf("non-string error code must fail, got %v", err)
	}
	// 555：rate_limited + daily 策略合法读取。
	daily := map[string]any{
		"enabled": true, "name": "w2a-daily", "priority": float64(2), "action": "rate_limited",
		"status_codes": []any{float64(429)}, "reset_strategy": "daily", "daily_reset_hour": float64(6),
	}
	rule, err := accountErrorHandlingRuleRead(daily, 1)
	if err != nil {
		t.Fatalf("daily rule: %v", err)
	}
	if rule.ResetStrategy != "daily" || rule.DailyResetHour != 6 {
		t.Fatalf("daily rule projection = %+v", rule)
	}
	_ = base
}

func TestW2APassiveJitterArms(t *testing.T) {
	// 853-855：负 interval 的防御截断（intervalMs <= -2 时 half < 0 截为
	// 0，min(负窗口, 0) 取负窗口本身）。
	if window := passiveScheduleJitterWindowMs(-3); window != -1 {
		t.Fatalf("negative interval window = %d", window)
	}
	// 885-887：确定性抖动采到 offset==0 时改写为 1。window=1（intervalMs=3）
	// 时 span=3，offset ∈ {-1,0,1}：先用同款散列找出 offset==0 的种子，
	// 再调用服务函数断言改写。
	zeroSeed := ""
	for seed := 0; seed < 4096 && zeroSeed == ""; seed++ {
		candidate := fmt.Sprintf("w2a-zero-%d", seed)
		window := passiveScheduleJitterWindowMs(3)
		span := window*2 + 1
		hash := int32(-2128831035)
		for _, r := range candidate {
			hash ^= int32(r)
			hash = imul32(hash, 16777619)
		}
		if int64(uint64(uint32(hash))%uint64(span))-window == 0 {
			zeroSeed = candidate
		}
	}
	if zeroSeed == "" {
		t.Fatalf("no seed produced offset==0 for window=1")
	}
	if offset := passiveScheduleDeterministicOffsetMs(3, zeroSeed); offset != 1 {
		t.Fatalf("offset==0 must be rewritten to 1, got %d (seed %s)", offset, zeroSeed)
	}
}

func TestW2AQuotaRecoveryWeeklyScheduleRead(t *testing.T) {
	// 960-961：weekly 策略项合法读取（day+hour 两行赋值）。
	normalized, err := quotaRecoveryPolicyRead(map[string]any{
		"oauth": map[string]any{
			"reset_strategy":    "weekly",
			"weekly_reset_day":  float64(2),
			"weekly_reset_hour": float64(9),
		},
	})
	if err != nil {
		t.Fatalf("weekly policy: %v", err)
	}
	schedule := normalized["oauth"].(map[string]any)
	if schedule["weekly_reset_day"] != float64(2) || schedule["weekly_reset_hour"] != float64(9) {
		t.Fatalf("weekly projection = %+v", schedule)
	}
	if schedule["timezone"] != "UTC" || schedule["jitter_minutes"] != float64(15) {
		t.Fatalf("weekly defaults = %+v", schedule)
	}
}

// ---------------------------------------------------------------------------
// chain_error_policy_effects.go
// ---------------------------------------------------------------------------

// w2aErrorPolicyBridgeViews 建一个以同名视图承载 accounts 的 sqlite 库：
// SELECT 可读，UPDATE 触发 "cannot modify" 错误。
func w2aErrorPolicyBridgeViews(t *testing.T, expiresAt string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/w2a-view.sqlite3")
	if err != nil {
		t.Fatalf("open view db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// 先建同名表再替换为视图（SQLite 需要先 DROP TABLE 才能 CREATE VIEW）。
	if _, err := db.Exec(`CREATE TABLE accounts (id TEXT)`); err != nil {
		t.Fatalf("seed table: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE accounts`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	view := fmt.Sprintf(`CREATE VIEW accounts AS SELECT 'w2a-acc' AS id, 'active' AS status,
		1 AS config_revision, '%s' AS account_expires_at, NULL AS deleted_at,
		NULL AS dispatch_revision, NULL AS last_health_success_at, NULL AS updated_at`, expiresAt)
	if _, err := db.Exec(view); err != nil {
		t.Fatalf("create view: %v", err)
	}
	return db
}

func w2aErrorPolicyDecision(action string) accountErrorPolicyDecision {
	return accountErrorPolicyDecision{Action: action, RuleName: "w2a", RuleSource: "account", CooldownStatus: cooldownStatusTemporaryUnavailable}
}

func TestW2AErrorPolicyEffectsBridgeClosures(t *testing.T) {
	db, _ := w2aFixtureDatabases(t)
	composed := &composition{db: db, Bus: inval.New(time.Now)}
	bridge, _, err := newChainErrorPolicyEffectsBridge(composed, "w2a-bridge-secret")
	if err != nil {
		t.Fatalf("build bridge: %v", err)
	}
	// 构造池账户指纹（accountkeystates 与 effects 同 secret 同派生）。
	probe, err := accountkeystates.NewStore(accountkeystates.Config{DB: db, Secret: "w2a-bridge-secret", Now: time.Now})
	if err != nil {
		t.Fatalf("build probe store: %v", err)
	}
	entries := probe.AccountAPIKeyEntries(map[string]any{"api_keys": []any{"sk-w2a-a", "sk-w2a-b"}})
	if len(entries) == 0 {
		t.Fatalf("no api key entries derived")
	}
	fingerprint := entries[0].Fingerprint
	account := gatewaydispatch.AccountCandidate{
		ID:                        "w2a-pool-acc",
		SystemAccountID:           "sys_owner",
		ProviderCode:              "openai",
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
		Type:                      "api_key",
		APIKeys:                   []string{"sk-w2a-a", "sk-w2a-b"},
		SelectedAPIKeyFingerprint: &fingerprint,
	}
	// keyStates.RecordFailure 落库成功 → finishMutation → inval 闭包
	//（183-184，composed.Bus 非 nil，K5 Invalidate 也执行）。
	if err := bridge.RecordKeyScopedQuotaFailure(context.Background(), account, accountErrorPolicyDecision{QuotaRecoveryMode: "generic"}, chainErrorPolicyFailureInput{
		StatusCode:    429,
		HasStatusCode: true,
		TraceID:       "w2a-trace",
	}); err != nil {
		t.Fatalf("record key scoped quota failure: %v", err)
	}
	// 直接触发 invalidateRuntime 的 bus 分支（739-741）。
	concreteBridge, ok := bridge.(*chainErrorPolicyEffectsBridge)
	if !ok {
		t.Fatalf("bridge must be the concrete effects bridge, got %T", bridge)
	}
	concreteBridge.invalidateRuntime("w2a_invalidate_probe")
}

func TestW2AErrorPolicyApplyDecisionErrorArms(t *testing.T) {
	// 关闭句柄：loadAccountRuntimeRow 直接报错 → disable/cooldown 两个
	// Apply 出口（238-240 / 247-249）与非 ErrNoRows 的 409-411。
	bridge := &chainErrorPolicyEffectsBridge{db: w2aClosedSQLiteDB(t), now: time.Now}
	account := gatewaydispatch.AccountCandidate{ID: "w2a-acc", Status: "active"}
	if _, _, err := bridge.ApplyAccountErrorPolicyDecision(context.Background(), account, w2aErrorPolicyDecision(decisionActionDisable), chainErrorPolicyFailureInput{}); err == nil {
		t.Fatalf("disable on closed db must fail")
	}
	if _, _, err := bridge.ApplyAccountErrorPolicyDecision(context.Background(), account, w2aErrorPolicyDecision(decisionActionCooldown), chainErrorPolicyFailureInput{}); err == nil {
		t.Fatalf("cooldown on closed db must fail")
	}
}

func TestW2AErrorPolicyMarkCooldownHardUnavailable(t *testing.T) {
	db, _ := w2aFixtureDatabases(t)
	now := "2026-09-14T00:00:00.000Z"
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, provider_code, name, type, status, schedulable, concurrency_limit, deleted_at) VALUES ('w2a-hard', 'sys_owner', 'openai', 'W2A', 'api_key', 'error', 1, 1, NULL)`, now); err != nil {
		// 参数个数不匹配时退回无参插列。
		if _, err2 := db.Exec(`INSERT INTO accounts (id, system_account_id, provider_code, name, type, status, schedulable, concurrency_limit, deleted_at) VALUES ('w2a-hard', 'sys_owner', 'openai', 'W2A', 'api_key', 'error', 1, 1, NULL)`); err2 != nil {
			t.Fatalf("seed hard account: %v / %v", err, err2)
		}
	}
	bridge := &chainErrorPolicyEffectsBridge{db: db, now: time.Now}
	changed, status, err := bridge.ApplyAccountErrorPolicyDecision(context.Background(), gatewaydispatch.AccountCandidate{ID: "w2a-hard", Status: "error"}, w2aErrorPolicyDecision(decisionActionCooldown), chainErrorPolicyFailureInput{})
	if err != nil || changed || status != "error" {
		t.Fatalf("hard unavailable cooldown must be no-op, got %v,%v,%v", changed, status, err)
	}
}

func TestW2AErrorPolicyMarkDisabledMissingAccount(t *testing.T) {
	db, _ := w2aFixtureDatabases(t)
	bridge := &chainErrorPolicyEffectsBridge{db: db, now: time.Now}
	// 账户行缺失：loadAccountRuntimeRow 返回 (nil,nil) → 615-617。
	changed, _, err := bridge.ApplyAccountErrorPolicyDecision(context.Background(), gatewaydispatch.AccountCandidate{ID: "w2a-missing"}, w2aErrorPolicyDecision(decisionActionDisable), chainErrorPolicyFailureInput{})
	if err != nil || changed {
		t.Fatalf("missing account disable must be no-op, got %v,%v", changed, err)
	}
}

func TestW2AErrorPolicyWriteErrorsViaView(t *testing.T) {
	// 过期账户：expired 分支 UPDATE 报错（478-480）。
	expiredBridge := &chainErrorPolicyEffectsBridge{db: w2aErrorPolicyBridgeViews(t, "2020-01-01T00:00:00Z"), now: time.Now}
	if _, _, err := expiredBridge.ApplyAccountErrorPolicyDecision(context.Background(), gatewaydispatch.AccountCandidate{ID: "w2a-acc"}, w2aErrorPolicyDecision(decisionActionCooldown), chainErrorPolicyFailureInput{}); err == nil {
		t.Fatalf("expired branch UPDATE must fail on view")
	}
	// 未过期：主 cooldown UPDATE 报错（557-559）。
	liveBridge := &chainErrorPolicyEffectsBridge{db: w2aErrorPolicyBridgeViews(t, "2099-01-01T00:00:00Z"), now: time.Now}
	if _, _, err := liveBridge.ApplyAccountErrorPolicyDecision(context.Background(), gatewaydispatch.AccountCandidate{ID: "w2a-acc"}, w2aErrorPolicyDecision(decisionActionCooldown), chainErrorPolicyFailureInput{}); err == nil {
		t.Fatalf("cooldown UPDATE must fail on view")
	}
	// disable：非 error/disabled 账户的 UPDATE 报错（647-649）。
	if _, _, err := liveBridge.ApplyAccountErrorPolicyDecision(context.Background(), gatewaydispatch.AccountCandidate{ID: "w2a-acc"}, w2aErrorPolicyDecision(decisionActionDisable), chainErrorPolicyFailureInput{}); err == nil {
		t.Fatalf("disable UPDATE must fail on view")
	}
}

func TestW2ARowCountOfResultError(t *testing.T) {
	if count := rowCountOf(w2aErrResult{}); count != 0 {
		t.Fatalf("erroring RowsAffected must collapse to 0, got %d", count)
	}
	if count := rowCountOf(nil); count != 0 {
		t.Fatalf("nil result must collapse to 0, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// chain_account_locks.go（脚本化 driver）：213 / 431 / 442 / 445 / 487
// ---------------------------------------------------------------------------

func w2aLockRow(lockState, deadlineAt, incidentID string, generation int64) []driver.Value {
	return []driver.Value{"w2a-acc", int64(1), lockState, int64(30), int64(5),
		incidentID, "2026-01-01T00:00:00.000Z", deadlineAt, "disabled", "lock_policy",
		nil, nil, nil, generation, "2026-01-01T00:00:00.000Z"}
}

func w2aLocksPort(db *sql.DB) *chainAccountLocks {
	return &chainAccountLocks{
		db:       db,
		postgres: false,
		now:      func() time.Time { return time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC) },
		newToken: func() string { return "w2a-token" },
	}
}

func TestW2AAccountLocksFindStateLosesRecoveryRaces(t *testing.T) {
	// DEAD_CONFIRMED + 账户已恢复，但连续三次 CAS 均失败 → 落到 213 行
	// 的 readStateOnce 重读出口。
	row := w2aLockRow("DEAD_CONFIRMED", "2026-01-01T00:00:00.000Z", "inc", 7)
	steps := []w2aScriptedStep{}
	for i := 0; i < 3; i++ {
		steps = append(steps,
			w2aScriptedStep{contains: "FROM account_lock_states", cols: strings.Fields(chainAccountLockRowColumns), row: row},
			w2aScriptedStep{contains: "FROM accounts", cols: []string{"status", "schedulable"}, row: []driver.Value{"active", int64(1)}},
			w2aScriptedStep{contains: "UPDATE account_lock_states", affected: 0},
		)
	}
	steps = append(steps, w2aScriptedStep{contains: "FROM account_lock_states", cols: strings.Fields(chainAccountLockRowColumns), row: row})
	port := w2aLocksPort(w2aOpenScriptedDB(t, steps))
	result, err := port.FindStateAsync(context.Background(), "w2a-acc")
	if err != nil || result == nil {
		t.Fatalf("find state: %v,%v", result, err)
	}
	if result.Generation != 7 || result.IncidentID != "inc" || result.BlocksCrossAccount {
		t.Fatalf("state after lost races = %+v", result)
	}
}

func TestW2AAccountLocksSettleTransactionArms(t *testing.T) {
	ctx := context.Background()
	due := "2026-01-01T00:00:00.000Z"

	// 431-433：BeginTx 失败。
	steps := []w2aScriptedStep{
		{contains: "FROM account_lock_states", cols: strings.Fields(chainAccountLockRowColumns), row: w2aLockRow("ENGAGED", due, "inc-1", 7)},
		{contains: "BEGIN", beginErr: errors.New("w2a: begin refused")},
	}
	if err := w2aLocksPort(w2aOpenScriptedDB(t, steps)).SettleDeadlineAsync(ctx, "w2a-acc", time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC).UnixMilli(), nil); err == nil {
		t.Fatalf("begin failure must surface")
	}

	// 442-444：事务内读为空（行已被并发迁移）。
	steps = []w2aScriptedStep{
		{contains: "FROM account_lock_states", cols: strings.Fields(chainAccountLockRowColumns), row: w2aLockRow("ENGAGED", due, "inc-1", 7)},
		{contains: "BEGIN"},
		{contains: "SELECT lock_state", noRows: true},
	}
	if err := w2aLocksPort(w2aOpenScriptedDB(t, steps)).SettleDeadlineAsync(ctx, "w2a-acc", time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC).UnixMilli(), nil); err != nil {
		t.Fatalf("missing tx row must return nil, got %v", err)
	}

	// 445-447：事务内读直接报错。
	steps = []w2aScriptedStep{
		{contains: "FROM account_lock_states", cols: strings.Fields(chainAccountLockRowColumns), row: w2aLockRow("ENGAGED", due, "inc-1", 7)},
		{contains: "BEGIN"},
		{contains: "SELECT lock_state", rowsErr: errors.New("w2a: tx read refused")},
	}
	if err := w2aLocksPort(w2aOpenScriptedDB(t, steps)).SettleDeadlineAsync(ctx, "w2a-acc", time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC).UnixMilli(), nil); err == nil {
		t.Fatalf("tx read failure must surface")
	}

	// 487-489：结算 UPDATE 命中后 Commit 失败。
	steps = []w2aScriptedStep{
		{contains: "FROM account_lock_states", cols: strings.Fields(chainAccountLockRowColumns), row: w2aLockRow("ENGAGED", due, "inc-1", 7)},
		{contains: "BEGIN"},
		{contains: "SELECT lock_state", cols: []string{"lock_state", "generation", "deadline_at", "incident_id", "original_status", "lease_id"}, row: []driver.Value{"ENGAGED", int64(7), due, "inc-1", "disabled", nil}},
		{contains: "UPDATE account_lock_states", affected: 1},
		{contains: "COMMIT", commitErr: errors.New("w2a: commit refused")},
	}
	if err := w2aLocksPort(w2aOpenScriptedDB(t, steps)).SettleDeadlineAsync(ctx, "w2a-acc", time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC).UnixMilli(), nil); err == nil {
		t.Fatalf("commit failure must surface")
	}
}

// ---------------------------------------------------------------------------
// chain_dispatch.go：159 / 699 / 705 / 710 / 909 / 915 / 921 / 927
// ---------------------------------------------------------------------------

func TestW2AClientIPAvoidanceOrderError(t *testing.T) {
	// redis 驱动 avoidance 指向拒连地址：构造成功、读取报错（159-161）。
	avoidance, err := gatewayclientip.NewAvoidance(gatewayclientip.AvoidanceOptions{
		RuntimeStateDriver: "redis",
		StateRedisURL:      "redis://127.0.0.1:1",
		RedisNamespace:     "juhe-ai:dev:w1cover",
	})
	if err != nil {
		t.Fatalf("build avoidance: %v", err)
	}
	defer avoidance.Close()
	port := newChainClientIPAvoidance(avoidance)
	_, err = port.OrderAsync(context.Background(), []gatewaydispatch.AccountCandidate{{ID: "w2a-acc"}}, gatewaydispatch.ClientIPAvoidanceScope{ClientIP: "10.0.0.9"}, nil)
	if err == nil {
		t.Fatalf("avoidance order on refused redis must fail")
	}
}

func TestW2AResponseInspectionSideEffectWriteErrors(t *testing.T) {
	db, _ := w2aFixtureDatabases(t)
	models, err := gatewayruntimecache.NewSQLReadModels(db, false, time.Now, nil, nil, nil)
	if err != nil {
		t.Fatalf("read models: %v", err)
	}
	cache, err := gatewayruntimecache.New(models, gatewayruntimecache.Options{})
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	t.Cleanup(cache.Close)
	failingAvoidance := gatewayaccounteffects.NewConfiguredPolicyAvoidanceService(w2aFailingRuntimeStateStore{}, nil, nil, nil)
	failingProxyHealth := gatewayproxyhealth.NewProxyHealthService(nil, w2aFailingRuntimeStateStore{}, gatewayproxyhealth.ProxyHealthOptions{}, nil)
	effects := &chainResponseAccountEffects{
		avoidance:   failingAvoidance,
		proxyHealth: failingProxyHealth,
		cache:       cache,
	}
	view := gatewayresponse.OpenAIAccountView{Account: gatewayruntimecache.OpenAIAccountSecret{ID: "w2a-acc"}}

	// 设置读取自检：numberSetting 要求全部必需键齐备，缺键会在 682-684
	// 提前返回（这里必须读到合法 settings 才能走到两个写侧错误臂）。
	if _, err := cache.ReadCachedGatewaySettings(context.Background()); err != nil {
		t.Fatalf("settings read must succeed: %v", err)
	}

	// runtime_avoidance：配置策略避让写报错（699-701）。
	err = effects.ApplyInspectionPolicySideEffects(&gatewayresponse.ResponseInspectionDecision{
		Reason:       "configured_response_policy",
		Action:       "replace_with_failure",
		AccountState: "runtime_avoidance",
		PolicyName:   "w2a-policy",
	}, view, true)
	if err == nil {
		t.Fatalf("avoidance write must fail on failing store")
	}

	// avoid_upstream_bucket_ttl：设置读取成功后上游桶避让写报错（710-712）。
	// （705 的 ttlSeconds<1 钳制不可达：settings 校验强制
	// defaultTemporaryUnschedulableMinutes >= 1，seconds 恒 >= 60。）
	err = effects.ApplyInspectionPolicySideEffects(&gatewayresponse.ResponseInspectionDecision{
		Reason:        "configured_response_policy",
		Action:        "replace_with_failure",
		AccountSwitch: "avoid_upstream_bucket_ttl",
		PolicyName:    "w2a-policy",
	}, view, true)
	if err == nil {
		t.Fatalf("bucket write must fail on failing store")
	}
}

func TestW2ALatencyDegradationOrderError(t *testing.T) {
	// 754-756：时延降级排序的存储读取错误上抛（failing store）。
	service := gatewayproxyhealth.NewLatencyDegradationService(w2aFailingRuntimeStateStore{}, nil, gatewayproxyhealth.LatencyDegradationOptions{})
	port := chainLatencyDegradationPort{service: service}
	deadline := int64(1000)
	_, err := port.OrderAsync(context.Background(),
		[]gatewaydispatch.AccountCandidate{{ID: "w2a-acc"}},
		&gatewaydispatch.LatencyScopeInput{SystemAccountID: "sys", GroupID: "g_w2a"},
		&gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{FirstByteDeadlineMs: &deadline},
		nil,
	)
	if err == nil {
		t.Fatalf("latency order over failing store must fail")
	}
}

func TestW2AListAvailabilityDirtyMarkerArms(t *testing.T) {
	source := "acc_w2a_marker"

	// 909-911：家族 SELECT 直接报错。
	steps := []w2aScriptedStep{
		{contains: "BEGIN"},
		{contains: "SELECT id FROM juhe_business.accounts", rowsErr: errors.New("w2a: family query refused")},
	}
	marker := newChainListAvailabilityDirtyMarker(&composition{db: w2aOpenScriptedDB(t, steps), pgDialect: true})
	if err := marker(context.Background(), source, "w2a", 1000); err == nil {
		t.Fatalf("family query failure must surface")
	}

	// 915-918：单行首列为 NULL，Scan 报错。
	steps = []w2aScriptedStep{
		{contains: "BEGIN"},
		{contains: "SELECT id FROM juhe_business.accounts", cols: []string{"id"}, scanAsNil: true},
	}
	marker = newChainListAvailabilityDirtyMarker(&composition{db: w2aOpenScriptedDB(t, steps), pgDialect: true})
	if err := marker(context.Background(), source, "w2a", 1000); err == nil {
		t.Fatalf("null id scan failure must surface")
	}

	// 921-924：行耗尽后 Err() 报错。
	steps = []w2aScriptedStep{
		{contains: "BEGIN"},
		{contains: "SELECT id FROM juhe_business.accounts", cols: []string{"id"}, rowsEOFErr: errors.New("w2a: rows corrupted")},
	}
	marker = newChainListAvailabilityDirtyMarker(&composition{db: w2aOpenScriptedDB(t, steps), pgDialect: true})
	if err := marker(context.Background(), source, "w2a", 1000); err == nil {
		t.Fatalf("rows.Err failure must surface")
	}

	// 927-929：家族展开成功但 upsert 报错。
	steps = []w2aScriptedStep{
		{contains: "BEGIN"},
		{contains: "SELECT id FROM juhe_business.accounts", cols: []string{"id"}, row: []driver.Value{"acc_w2a_marker"}},
		{contains: "INSERT INTO juhe_business.account_list_availability_dirty", execErr: errors.New("w2a: upsert refused")},
	}
	marker = newChainListAvailabilityDirtyMarker(&composition{db: w2aOpenScriptedDB(t, steps), pgDialect: true})
	if err := marker(context.Background(), source, "w2a", 1000); err == nil {
		t.Fatalf("upsert failure must surface")
	}
}

// ---------------------------------------------------------------------------
// chain_routing.go：45 / 59 / 71 / 179 / 335 / 236
// ---------------------------------------------------------------------------

func TestW2ARoutingCacheReadErrorArms(t *testing.T) {
	cache := &chainRoutingCache{cache: w2aBrokenRoutingCache(t)}
	if _, _, err := cache.ResolveCachedGroupUsageAccessMetadataAsync(context.Background(), "g_w2a", "sys"); err == nil {
		t.Fatalf("group access read on closed db must fail")
	}
	if _, err := cache.ListCachedOpenAIAccountsForGroupAsync(context.Background(), "g_w2a", "sys", gatewayrouting.CachedAccountsForGroupOptions{}); err == nil {
		t.Fatalf("accounts listing on closed db must fail")
	}
	if _, err := cache.ResolveCachedProviderModelRouteAsync(context.Background(), gatewayrouting.ProviderModelRouteInput{
		Model:           "gpt-test",
		ProviderCodes:   []string{"openai"},
		SystemAccountID: "sys",
	}); err == nil {
		t.Fatalf("provider route read on closed db must fail")
	}
}

func TestW2ARoutingListFullAccountsError(t *testing.T) {
	resolver := &chainRouteResolver{cache: w2aBrokenRoutingCache(t)}
	if accounts := resolver.listFullAccounts(context.Background(), "g_w2a", "sys", "gpt-test", ""); accounts != nil {
		t.Fatalf("listFullAccounts must degrade to nil on error, got %v", accounts)
	}
}

func TestW2ARoutingNormalResolveError(t *testing.T) {
	cache := w2aBrokenRoutingCache(t)
	normal := gatewayrouting.NewNormalModelRouteService(&chainRoutingCache{cache: cache}, chainCapabilityFilter{})
	resolver := &chainRouteResolver{cache: cache, normal: normal}
	record := &gatewayruntimecache.GatewayAPIKeyRow{
		ID:                "key_w2a",
		SystemAccountID:   "sys",
		RouteStrategyID:   "rs_w2a",
		RouteStrategyMode: "normal",
		// 双供应商绑定：单供应商在 service 侧被 SingleProvider 直接跳过，
		// 两绑定才会走到目录路由读取（closed db → error）。
		GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{
				ID: "b1", APIKeyID: "key_w2a", SystemAccountID: "sys", GroupID: "g_w2a",
				Status: "active", ProviderCode: "openai", GroupEnabled: 1,
			},
			{
				ID: "b2", APIKeyID: "key_w2a", SystemAccountID: "sys", GroupID: "g_w2b",
				Status: "active", ProviderCode: "anthropic", GroupEnabled: 1,
			},
		},
	}
	_, err := resolver.ResolveNormalGatewayModelRoute(context.Background(), gatewaypreauth.NormalRouteInput{
		Req:          w2aGatewayRequestWithModel(""),
		APIKeyRecord: record,
	})
	if err == nil {
		t.Fatalf("normal route over closed cache must fail")
	}
}

// ---------------------------------------------------------------------------
// chain_runtime.go（组合根直驱）：415 / 526(见登记) / 549 / 600 / 601 / 613 / 614
// ---------------------------------------------------------------------------

func w2aComposeSettingValue(key string) (string, error) {
	if key == "usageStatsTimezone" {
		return "UTC", nil
	}
	return "", nil
}

func TestW2AComposeQuotaSharedNamespaceError(t *testing.T) {
	db, statsDB := w2aFixtureDatabases(t)
	cfg := runtimeConfig{
		RuntimeMode:                   "standalone",
		CacheDriver:                   "redis",
		RedisCacheURL:                 "redis://127.0.0.1:1",
		RedisNamespace:                "",
		RuntimeStateDriver:            "memory",
		Secret:                        "w2a-compose-secret",
		DispatchAccountCandidateLimit: 20,
	}
	services, err := composeChainRuntimeServices(&composition{db: db, statsDB: statsDB}, cfg, w2aComposeSettingValue)
	if err == nil {
		services.Close()
		t.Fatalf("empty redis namespace must fail quota shared cache construction")
	}
	if !strings.Contains(err.Error(), "namespace") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestW2AComposeKeyModelSelectError(t *testing.T) {
	// performance + redis 组合下 hot-quality 运行时会真实构建 redis 存储
	//（拒连地址在 539 行先失败），因此这里用真实 dev Redis（只构造、
	// 不写键），namespace 取受控前缀 + 超长后缀：clientip/quota 侧清洗
	// 通过，key-model 的 ^[A-Za-z0-9_.:-]{1,64}$ 拒绝 → Select 报错
	//（549-551）。
	env := w1pgEnvFile(t)
	stateURL := env["JUHE_AI_REDIS_STATE_URL"]
	if stateURL == "" {
		t.Skip("dev env 缺少 JUHE_AI_REDIS_STATE_URL（跳过）")
	}
	options, err := redis.ParseURL(stateURL)
	if err != nil {
		t.Skipf("dev redis url 不可解析（跳过）: %v", err)
	}
	probe := redis.NewClient(options)
	defer func() { _ = probe.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := probe.Ping(ctx).Err(); err != nil {
		t.Skipf("dev redis 不可达（跳过）: %v", err)
	}
	db, statsDB := w2aFixtureDatabases(t)
	cfg := runtimeConfig{
		RuntimeMode:                   "performance",
		CacheDriver:                   "memory",
		RuntimeStateDriver:            "redis",
		RedisStateURL:                 stateURL,
		RedisNamespace:                "juhe-ai:dev:w1cover:" + strings.Repeat("w2a", 30),
		Secret:                        "w2a-compose-secret",
		DispatchAccountCandidateLimit: 20,
	}
	services, composeErr := composeChainRuntimeServices(&composition{db: db, statsDB: statsDB}, cfg, w2aComposeSettingValue)
	if composeErr == nil {
		services.Close()
		t.Fatalf("oversized namespace must fail key-model select")
	}
	if !strings.Contains(composeErr.Error(), "key-model") {
		t.Fatalf("unexpected error: %v", composeErr)
	}
}

func TestW2AComposeRuntimeInvalidationClosures(t *testing.T) {
	db, statsDB := w2aFixtureDatabases(t)
	cfg := runtimeConfig{
		RuntimeMode:                   "standalone",
		CacheDriver:                   "memory",
		RuntimeStateDriver:            "memory",
		Secret:                        "w2a-compose-secret",
		DispatchAccountCandidateLimit: 20,
	}
	services, err := composeChainRuntimeServices(&composition{db: db, statsDB: statsDB, Bus: inval.New(time.Now)}, cfg, w2aComposeSettingValue)
	if err != nil {
		t.Fatalf("compose runtime services: %v", err)
	}
	defer services.Close()

	// 派生池账户 Key 指纹（与 effects 内部 keyStates 同 secret 同派生）。
	probe, err := accountkeystates.NewStore(accountkeystates.Config{DB: db, Secret: cfg.Secret, Now: time.Now})
	if err != nil {
		t.Fatalf("probe store: %v", err)
	}
	entries := probe.AccountAPIKeyEntries(map[string]any{"api_keys": []any{"sk-w2a-compose-a", "sk-w2a-compose-b"}})
	if len(entries) == 0 {
		t.Fatalf("no fingerprint derived")
	}
	fingerprint := entries[0].Fingerprint
	account := gatewayruntimecache.OpenAIAccountSecret{
		ID:                        "w2a-compose-pool",
		SystemAccountID:           "sys_owner",
		ProviderCode:              "openai",
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
		Type:                      "api_key",
		APIKeys:                   []string{"sk-w2a-compose-a", "sk-w2a-compose-b"},
		SelectedAPIKeyFingerprint: &fingerprint,
	}
	// effects.RecordFailure（memory 驱动 = 同步写）：显式用户策略授权
	// 上下文让守卫放行持久写 → keyStates.RecordFailure 落库 changed →
	// markRuntimeStateChanged → keyStates 失效闭包（600-603）→
	// effects.invalidateRuntimeCache（613-616）。
	services.AccountAPIKeyEffects.RecordFailure(context.Background(), account, gatewayaccounteffects.RecordFailureInput{
		Status:        "rate_limited",
		Source:        "w2a_test",
		TrafficSource: "gateway",
		MutationContext: &gatewayaccounteffects.AccountApiKeyPersistentMutationContext{
			Authority:     "explicit_user_policy",
			TrafficSource: "gateway",
		},
	})
}
