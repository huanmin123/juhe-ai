package main

// chainGroupModelUnionLoader 手动性能 harness（并集设计 §5.4 实测交付）。
//
// 权威契约：docs/functions/网关模型列表账户并集设计.md §5.4——实施交付须记录
// 冷缓存 SQL/授权查询次数、冷热耗时、分组数/账户数/supportedModels/映射行数，
// 并用真实聚合 SQL 的 EXPLAIN (ANALYZE, BUFFERS) 验证索引路径。
//
// 默认跳过（CI/全量测试零影响）：设 JUHE_AI_UNION_PERF_DSN 指向已用
// maintenance --ensure-schema --seed 初始化的临时子库（如
// juhe_ai_sub2api_dev_unionperf）后运行：
//
//	JUHE_AI_UNION_PERF_DSN=<子库URL> go test -count=1 -run TestUnionPerfManual -v ./cmd/juhe-ai-gateway/
//
// 夹具（隔离前缀 unionperf，属主域=分组属主=调用方 system account，全部直连
// api_key 账户）：1 provider profile（复用种子 openai/openai/v1）、1 system
// account、3 enabled 分组 × 100 active 账户 = 300 账户、每账户 64 行
// account_supported_models（19200 行，相邻账户 50% 重叠测去重）、每账户 10 行
// 启用映射（8 行可被 gatewayopenai.IsAdmissibleModelMapping 接受：responses→
// chat_completions 跨族改写且 upstream ∈ SM + 同族跨模型改写；2 行恒等行被
// 排除）= 3000 行。加压场景把映射加到每账户 64 行（19200 行）复测。
//
// 构造方式对齐生产（chain_runtime.go:329 → newChainAccountsSelectorWithStats →
// newChainGroupModelUnionLoader 同一 selector 注入）：postgres 方言、secret 置空
// （本路径不解密凭据）、dispatchAccountCandidateLimit 取合法值（并集装载去掉
// LIMIT，不消费该上限）。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const (
	unionPerfGroupCount    = 3
	unionPerfGroupAccounts = 100
	unionPerfSMPerAccount  = 64
	// unionPerfPoolSize 是 supportedModels 模型名池：账户 i 取窗口
	// [(i*32)..(i*32+63)] mod pool，相邻账户共享 32/64 = 50% 行；300 账户
	// 覆盖全池 → SM 并集去重后 4096 个模型。
	unionPerfPoolSize    = 4096
	unionPerfMappingsA   = 10 // 8 可接受 + 2 恒等排除
	unionPerfAdmissibleA = 8
	unionPerfMappingsB   = 64 // 加压场景：全部为可接受行
	unionPerfRuns        = 20
)

func unionPerfModelName(index int) string { return fmt.Sprintf("unionperf-m-%04d", index) }

func unionPerfAccountID(group int, index int) string {
	return fmt.Sprintf("unionperf-acc-g%d-%03d", group, index)
}

func unionPerfSourceNameA(accountIndex, k int) string {
	return fmt.Sprintf("unionperf-src-a-%03d-%d", accountIndex, k)
}

func unionPerfSourceNameB(accountIndex, k int) string {
	return fmt.Sprintf("unionperf-src-b-%03d-%d", accountIndex, k)
}

// unionPerfMappingRowA 返回账户 i 的第 k 条映射（source, sourceFamily, upstream,
// upstreamFamily）。k < 8：responses→chat_completions 跨族改写（openai/v1 协议
// 档案经 isOpenAIModelMappingRuntimeConversionSupported 放行）且 upstream ∈ 本
// 账户 64 宽 SM 窗口；k = 8/9：恒等行（source == upstream 且同族），取窗口末尾
// 两个模型，IsAdmissibleModelMapping 恒拒。
func unionPerfMappingRowA(i, k int) (string, string, string, string) {
	if k < unionPerfAdmissibleA {
		return unionPerfSourceNameA(i, k), "responses",
			unionPerfModelName((i*32+k*8)%unionPerfPoolSize), "chat_completions"
	}
	model := unionPerfModelName((i*32+62+k-unionPerfAdmissibleA)%unionPerfPoolSize)
	return model, "chat_completions", model, "chat_completions"
}

// unionPerfMappingRowB 返回加压场景第 k 条（k = 0..53）全可接受映射：跨族改写
// 且 upstream = (i*32+k) ∈ 本账户 SM 窗口。
func unionPerfMappingRowB(i, k int) (string, string, string, string) {
	return unionPerfSourceNameB(i, k), "responses",
		unionPerfModelName((i*32+k)%unionPerfPoolSize), "chat_completions"
}

// ---------------------------------------------------------------------------
// SQL 计数 driver 中间件：包 driver.Connector/Conn 统计 QueryContext/ExecContext
// 调用次数（pgx stdlib 实现两接口，database/sql 优先走 QueryerContext）。
// ---------------------------------------------------------------------------

type unionPerfCountingConnector struct {
	driver.Connector
	counter *atomic.Int64
}

func (c unionPerfCountingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &unionPerfCountingConn{Conn: conn, counter: c.counter}, nil
}

type unionPerfCountingConn struct {
	driver.Conn
	counter *atomic.Int64
}

func (c *unionPerfCountingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.counter.Add(1)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *unionPerfCountingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.counter.Add(1)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

// ---------------------------------------------------------------------------
// 夹具装载（批量 INSERT + ON CONFLICT DO NOTHING，跨运行幂等）
// ---------------------------------------------------------------------------

func unionPerfBatchInsert(t *testing.T, db *sql.DB, table string, columns []string, rows [][]any, batch int) {
	t.Helper()
	for start := 0; start < len(rows); start += batch {
		end := min(start+batch, len(rows))
		var sb strings.Builder
		sb.WriteString("INSERT INTO " + table + " (" + strings.Join(columns, ", ") + ") VALUES ")
		args := make([]any, 0, len(columns)*(end-start))
		param := 1
		for i := start; i < end; i++ {
			if i > start {
				sb.WriteString(", ")
			}
			sb.WriteString("(")
			for j := range columns {
				if j > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString("$" + strconv.Itoa(param))
				param++
				args = append(args, rows[i][j])
			}
			sb.WriteString(")")
		}
		sb.WriteString(" ON CONFLICT DO NOTHING")
		if _, err := db.Exec(sb.String(), args...); err != nil {
			t.Fatalf("batch insert %s rows %d-%d: %v", table, start, end, err)
		}
	}
}

func unionPerfSeed(t *testing.T, db *sql.DB) (sysID, providerCode, profileID string) {
	t.Helper()
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	sysID = "unionperf-sys"

	// 跨运行幂等：清掉本夹具前缀的历史行（子库为 mock 专用；accounts 级联
	// 删除 SM/映射/绑定，groups 级联删除绑定，最后删属主 system account）。
	for _, statement := range []string{
		`DELETE FROM juhe_business.accounts WHERE id LIKE 'unionperf-%'`,
		`DELETE FROM juhe_business.groups WHERE id LIKE 'unionperf-%'`,
		`DELETE FROM juhe_business.system_accounts WHERE id = 'unionperf-sys'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("清理历史夹具行: %v", err)
		}
	}

	// provider/protocol profile 动态取库里已有行（accounts 两列有 FK）；限定
	// openai/openai/v1 使 responses→chat_completions 跨族改写被运行时矩阵放行。
	if err := db.QueryRow(`
		SELECT pp.provider_code, pp.id FROM juhe_business.provider_protocol_profiles pp
		WHERE pp.enabled = 1 AND pp.protocol_code = 'openai' AND pp.protocol_version = 'v1'
		ORDER BY (pp.provider_code = 'openai') DESC LIMIT 1`).
		Scan(&providerCode, &profileID); err != nil {
		t.Fatalf("读取 openai/v1 provider profile: %v", err)
	}

	// system account（属主域=调用方）。
	if _, err := db.Exec(`
		INSERT INTO juhe_business.system_accounts (id, username, display_name, password_hash, created_at, updated_at)
		VALUES ($1, $1, 'unionperf', 'unionperf-mock-hash', $2, $2) ON CONFLICT (id) DO NOTHING`,
		sysID, ts); err != nil {
		t.Fatalf("seed system account: %v", err)
	}

	// 3 个 enabled 分组。
	for group := 1; group <= unionPerfGroupCount; group++ {
		if _, err := db.Exec(`
			INSERT INTO juhe_business.groups (id, system_account_id, name, provider_code, enabled, group_type, created_at, updated_at)
			VALUES ($1, $2, $1, $3, 1, 'personal', $4, $4) ON CONFLICT (id) DO NOTHING`,
			fmt.Sprintf("unionperf-g%d", group), sysID, providerCode, ts); err != nil {
			t.Fatalf("seed group %d: %v", group, err)
		}
	}

	// 300 个 active/schedulable 直连账户 + group_accounts 绑定（属主域写分组属主）。
	totalAccounts := unionPerfGroupCount * unionPerfGroupAccounts
	accountRows := make([][]any, 0, totalAccounts)
	bindingRows := make([][]any, 0, totalAccounts)
	for group := 1; group <= unionPerfGroupCount; group++ {
		for index := 0; index < unionPerfGroupAccounts; index++ {
			id := unionPerfAccountID(group, index)
			accountRows = append(accountRows, []any{
				id, sysID, providerCode, profileID, "openai", "v1", id, "api_key", "active", 1,
				"unionperf-mock-creds", "unionperf-model", "chat_json", ts, ts,
			})
			bindingRows = append(bindingRows, []any{
				fmt.Sprintf("unionperf-g%d", group), sysID, id, 1, ts, ts,
			})
		}
	}
	unionPerfBatchInsert(t, db, "juhe_business.accounts", []string{
		"id", "system_account_id", "provider_code", "provider_protocol_profile_id", "protocol_code", "protocol_version",
		"name", "type", "status", "schedulable", "credentials_encrypted", "health_check_model",
		"health_check_endpoint_mode", "created_at", "updated_at",
	}, accountRows, 100)
	unionPerfBatchInsert(t, db, "juhe_business.group_accounts", []string{
		"group_id", "system_account_id", "account_id", "enabled", "created_at", "updated_at",
	}, bindingRows, 100)

	// supportedModels：19200 行（每账户 64，相邻账户 50% 重叠）。
	smRows := make([][]any, 0, totalAccounts*unionPerfSMPerAccount)
	for group := 1; group <= unionPerfGroupCount; group++ {
		for index := 0; index < unionPerfGroupAccounts; index++ {
			i := (group-1)*unionPerfGroupAccounts + index
			for j := 0; j < unionPerfSMPerAccount; j++ {
				smRows = append(smRows, []any{
					unionPerfAccountID(group, index), providerCode,
					unionPerfModelName((i*32+j)%unionPerfPoolSize), ts,
				})
			}
		}
	}
	unionPerfBatchInsert(t, db, "juhe_business.account_supported_models", []string{
		"account_id", "provider_code", "model", "created_at",
	}, smRows, 1000)
	return sysID, providerCode, profileID
}

func unionPerfSeedMappingsA(t *testing.T, db *sql.DB, providerCode string) {
	t.Helper()
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	totalAccounts := unionPerfGroupCount * unionPerfGroupAccounts
	rows := make([][]any, 0, totalAccounts*unionPerfMappingsA)
	for group := 1; group <= unionPerfGroupCount; group++ {
		for index := 0; index < unionPerfGroupAccounts; index++ {
			i := (group-1)*unionPerfGroupAccounts + index
			for k := 0; k < unionPerfMappingsA; k++ {
				source, sourceFamily, upstream, upstreamFamily := unionPerfMappingRowA(i, k)
				rows = append(rows, []any{
					unionPerfAccountID(group, index), providerCode, source, sourceFamily,
					upstream, upstreamFamily, 1, ts, ts,
				})
			}
		}
	}
	unionPerfBatchInsert(t, db, "juhe_business.account_model_mappings", []string{
		"account_id", "provider_code", "source_model", "source_endpoint_family",
		"upstream_model", "upstream_endpoint_family", "enabled", "created_at", "updated_at",
	}, rows, 500)
}

func unionPerfSeedMappingsB(t *testing.T, db *sql.DB, providerCode string) {
	t.Helper()
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	totalAccounts := unionPerfGroupCount * unionPerfGroupAccounts
	rows := make([][]any, 0, totalAccounts*(unionPerfMappingsB-unionPerfMappingsA))
	for group := 1; group <= unionPerfGroupCount; group++ {
		for index := 0; index < unionPerfGroupAccounts; index++ {
			i := (group-1)*unionPerfGroupAccounts + index
			for k := 0; k < unionPerfMappingsB-unionPerfMappingsA; k++ {
				source, sourceFamily, upstream, upstreamFamily := unionPerfMappingRowB(i, k)
				rows = append(rows, []any{
					unionPerfAccountID(group, index), providerCode, source, sourceFamily,
					upstream, upstreamFamily, 1, ts, ts,
				})
			}
		}
	}
	unionPerfBatchInsert(t, db, "juhe_business.account_model_mappings", []string{
		"account_id", "provider_code", "source_model", "source_endpoint_family",
		"upstream_model", "upstream_endpoint_family", "enabled", "created_at", "updated_at",
	}, rows, 500)
}

// ---------------------------------------------------------------------------
// 期望并集与统计
// ---------------------------------------------------------------------------

// unionPerfExpectedUnion 按夹具生成器推演期望去重模型数（SM 并集 ∪ 可接受映射
// 源；恒等行源 = SM 池内模型，不新增）。phaseB 为 true 时计入加压映射源。
func unionPerfExpectedUnion(phaseB bool) int {
	set := map[string]bool{}
	totalAccounts := unionPerfGroupCount * unionPerfGroupAccounts
	for i := 0; i < totalAccounts; i++ {
		for j := 0; j < unionPerfSMPerAccount; j++ {
			set[unionPerfModelName((i*32+j)%unionPerfPoolSize)] = true
		}
		for k := 0; k < unionPerfAdmissibleA; k++ {
			set[unionPerfSourceNameA(i, k)] = true
		}
		if phaseB {
			for k := 0; k < unionPerfMappingsB-unionPerfMappingsA; k++ {
				set[unionPerfSourceNameB(i, k)] = true
			}
		}
	}
	return len(set)
}

func unionPerfPercentile(sorted []time.Duration, percentile float64) time.Duration {
	rank := int(math.Ceil(percentile * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// unionPerfMeasure 预热 1 次（含计划编译）后测 unionPerfRuns 次，报 p50/p95/
// max/平均与每轮 SQL 查询次数，并打印结果数 sanity（不强断言）。
func unionPerfMeasure(t *testing.T, label string, loader chainGroupModelUnionLoader, opts gatewayruntimecache.GroupModelUnionListOptions, counter *atomic.Int64, expected int) {
	t.Helper()
	ctx := context.Background()
	counter.Store(0)
	warmStart := time.Now()
	entry, err := loader.ListGroupModelUnion(ctx, opts)
	if err != nil {
		t.Fatalf("[%s] 预热装载: %v", label, err)
	}
	warmup := time.Since(warmStart)
	t.Logf("[%s] 预热 1 次（含计划编译）=%v models=%d", label, warmup, len(entry.Models))

	durations := make([]time.Duration, 0, unionPerfRuns)
	counts := make([]int64, 0, unionPerfRuns)
	for run := 0; run < unionPerfRuns; run++ {
		counter.Store(0)
		started := time.Now()
		_, err := loader.ListGroupModelUnion(ctx, opts)
		durations = append(durations, time.Since(started))
		counts = append(counts, counter.Load())
		if err != nil {
			t.Fatalf("[%s] 第 %d 次装载: %v", label, run+1, err)
		}
	}
	sorted := append([]time.Duration(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var total time.Duration
	for _, d := range durations {
		total += d
	}
	t.Logf("[%s] N=%d p50=%v p95=%v max=%v avg=%v", label, unionPerfRuns,
		unionPerfPercentile(sorted, 0.50), unionPerfPercentile(sorted, 0.95),
		sorted[len(sorted)-1], total/time.Duration(unionPerfRuns))
	distinct := map[int64]bool{}
	for _, c := range counts {
		distinct[c] = true
	}
	keys := make([]int64, 0, len(distinct))
	for c := range distinct {
		keys = append(keys, c)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	t.Logf("[%s] 每轮 SQL 查询次数（20 轮去重集合）=%v", label, keys)
	t.Logf("[%s] sanity models=%d expected=%d match=%v", label, len(entry.Models), expected, len(entry.Models) == expected)
}

func start(t time.Time) time.Time { return t }

// ---------------------------------------------------------------------------
// EXPLAIN (ANALYZE, BUFFERS)
// ---------------------------------------------------------------------------

// unionPerfExplain 打印真实 SQL 的执行计划关键行（扫描方式/行数/planning/
// execution 时间）；query 必须使用 $n 占位符（pgx 方言）。
func unionPerfExplain(t *testing.T, db *sql.DB, label, query string, args ...any) {
	t.Helper()
	rows, err := db.Query("EXPLAIN (ANALYZE, BUFFERS) "+query, args...)
	if err != nil {
		t.Fatalf("[%s] EXPLAIN: %v", label, err)
	}
	defer rows.Close()
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("[%s] EXPLAIN scan: %v", label, err)
		}
		if strings.Contains(line, "Scan") || strings.Contains(line, "Time") {
			t.Logf("[%s] %s", label, strings.TrimSpace(line))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("[%s] EXPLAIN rows: %v", label, err)
	}
}

// ---------------------------------------------------------------------------
// harness 主入口
// ---------------------------------------------------------------------------

func TestUnionPerfManual(t *testing.T) {
	dsn := os.Getenv("JUHE_AI_UNION_PERF_DSN")
	if dsn == "" {
		t.Skip("手动性能 harness，设 JUHE_AI_UNION_PERF_DSN 后运行")
	}

	counter := &atomic.Int64{}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("解析 DSN: %v", err)
	}
	db := sql.OpenDB(unionPerfCountingConnector{Connector: stdlib.GetConnector(*config), counter: counter})
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("临时子库 ping: %v", err)
	}

	sysID, providerCode, profileID := unionPerfSeed(t, db)

	// 夹具映射有效性 sanity（openai/openai/v1 档案：跨族行必须被接受、恒等行
	// 必须被拒绝——防止夹具本身构造出无效场景）。
	runtimeAccount := &gatewayopenai.RuntimeAccount{
		ProviderCode:              providerCode,
		ProviderProtocolProfileID: profileID,
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
	}
	source, sourceFamily, upstream, upstreamFamily := unionPerfMappingRowA(0, 0)
	admissible := gatewayopenai.IsAdmissibleModelMapping(gatewayopenai.AccountModelMapping{
		SourceModel: source, SourceEndpointFamily: sourceFamily,
		UpstreamModel: upstream, UpstreamEndpointFamily: upstreamFamily,
	}, runtimeAccount)
	identitySource, identityFamily, identityUpstream, identityUpstreamFamily := unionPerfMappingRowA(0, 8)
	identity := gatewayopenai.IsAdmissibleModelMapping(gatewayopenai.AccountModelMapping{
		SourceModel: identitySource, SourceEndpointFamily: identityFamily,
		UpstreamModel: identityUpstream, UpstreamEndpointFamily: identityUpstreamFamily,
	}, runtimeAccount)
	if !admissible || identity {
		t.Fatalf("夹具映射有效性 sanity 失败: admissible=%v identity=%v", admissible, identity)
	}

	// 生产构造方式（chain_runtime.go:329 镜像；见文件头注释）。
	selector, err := newChainAccountsSelectorWithStats(db, db, true, "", time.Now, 100)
	if err != nil {
		t.Fatalf("create selector: %v", err)
	}
	loader, err := newChainGroupModelUnionLoader(selector)
	if err != nil {
		t.Fatalf("create union loader: %v", err)
	}
	opts := gatewayruntimecache.GroupModelUnionListOptions{
		CallerSystemAccountID: sysID,
		GroupIDs:              []string{"unionperf-g1", "unionperf-g2", "unionperf-g3"},
	}

	// ---- 场景 A：当前规模（每账户 10 行映射，共 3000 行）----
	unionPerfSeedMappingsA(t, db, providerCode)
	unionPerfMeasure(t, "A-映射3000行", *loader, opts, counter, unionPerfExpectedUnion(false))

	// ---- 场景 B：映射行较多（加压到 19200 行）----
	unionPerfSeedMappingsB(t, db, providerCode)
	unionPerfMeasure(t, "B-映射19200行", *loader, opts, counter, unionPerfExpectedUnion(true))

	// ---- EXPLAIN (ANALYZE, BUFFERS)：贴装载器实际生成的 SQL 与实参 ----
	// seed 后先 ANALYZE 刷新统计，避免规划器按陈旧 relpages 把小表误判为全表
	// 扫描（§5.4 要求执行计划反映代表性数据形态）。
	for _, table := range []string{"group_accounts", "accounts", "account_supported_models", "account_model_mappings"} {
		if _, err := db.Exec("ANALYZE juhe_business." + table); err != nil {
			t.Fatalf("ANALYZE %s: %v", table, err)
		}
	}
	now := chainNowISO(time.Now)
	where := strings.NewReplacer("{statusSet}", "'active', 'rate_limited', 'temporary_unavailable'").Replace(chainCandidateWhere)
	candidateQuery := strings.NewReplacer(
		"{columns}", chainCandidateColumns,
		"{from}", fmt.Sprintf(chainCandidateFrom, selector.table("group_accounts"), selector.table("accounts"), selector.table("accounts")),
		"{where}", where,
	).Replace(`SELECT {columns} {from} {where}`)
	unionPerfExplain(t, db, "候选SQL-g1", selector.bind(candidateQuery),
		"unionperf-g1", sysID, providerCode, 1, now, providerCode, 1, now, now, now)

	firstGroupIDs := make([]any, 0, unionPerfGroupAccounts)
	for index := 0; index < unionPerfGroupAccounts; index++ {
		firstGroupIDs = append(firstGroupIDs, unionPerfAccountID(1, index))
	}
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(firstGroupIDs)), ", ")
	smQuery := fmt.Sprintf(`SELECT account_id, provider_code, model FROM %s WHERE account_id IN (%s)`,
		selector.table("account_supported_models"), placeholders)
	unionPerfExplain(t, db, "SM批量SQL-g1", selector.bind(smQuery), firstGroupIDs...)
	mappingQuery := fmt.Sprintf(`SELECT account_id, provider_code, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family
		FROM %s WHERE account_id IN (%s) AND enabled = 1
		ORDER BY account_id ASC, source_model ASC, source_endpoint_family ASC`,
		selector.table("account_model_mappings"), placeholders)
	unionPerfExplain(t, db, "映射批量SQL-g1", selector.bind(mappingQuery), firstGroupIDs...)
}
