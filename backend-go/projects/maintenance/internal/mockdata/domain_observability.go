package mockdata

// 可观测域：审计日志与 payload blob（F3 / gateway）、操作日志（F4 / gateway）、
// 运行日志索引与文件（F1 / jobs）、后台任务运行（jobs task-runs 库），以及
// dataset 库的公开接口日志与后台记录清理目标。
//
// 依据：docs/functions/Mockdata造数设计.md「数据边界」「清理策略」「验证点」与
// migration-backup-1/node/final-archive/backend/src/scripts/maintenance/mockdata/
// 的 observability/{logs,storage,monitoring}.ts、records/record-cleanup.ts
// （Node 版曾把审计 / 运行日志 / 表监控造数让给 Go owner，这里补上审计、操作与
// 运行日志；表空间监控仍由 F2 owner 生成，本域不写）。
//
// 硬边界：
//   - 列名与取值一一对应各 owner 的 store DDL 与读面
//     （gateway/internal/auditlog、gateway/internal/operationlog、
//     gateway/internal/logreads、jobs/internal/runtimelog、
//     jobs/internal/taskruns、maintenance/internal/schema 的 dataset DDL）；
//   - audit-log / operation-log / runtime-log / task-runs 四个库不属 maintenance
//     schema：文件或表不存在即跳过并记日志，绝不自行建库建表；
//   - owner 租约与游标状态原则上不写。唯一例外是运行日志里「本域自己写的 mock
//     日志文件」的游标行：不写它，F1 首次看到文件会从文件末尾追新，而索引行又
//     必须由造数直接写（历史行不会被 F1 补），文件与索引就会互相矛盾。
//   - dataset 库属 maintenance schema，但本域同样只写不建（--ensure-schema 负责
//     建库）：造数在空数据根上不得自行创建存储文件，否则「空根=零写入」的既有
//     契约（TestRunOnEmptyDataRootWritesReportAndSummary）会失效。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// observabilityTimeLayout 与 jobs runtimelog 的 nodeISO 一致：毫秒精度的
	// UTC 文本。审计 / 操作 / 运行日志 / 公开接口日志的读面都按字符串比较时间，
	// 统一这个形态才能让筛选与排序与真实写入一致。
	observabilityTimeLayout = "2006-01-02T15:04:05.000Z"

	// observabilityBlobDigestBytes 与 F3 shortHash 一致：contentType 的
	// sha256 前 8 字节十六进制，用于 blob 物理对象名与 blob id 后缀。
	observabilityBlobDigestBytes = 8

	// observabilityRuntimeFacetBucketKey 与 jobs runtimelog 的 facetBucketKey 一致。
	observabilityRuntimeFacetBucketKey = "current"

	// 运行日志文件名：必须满足 jobs runtimelog filename.go 的实例日志模式
	// （juhe-ai.<instance>.log 与 juhe-ai.<role>.<instance>.log），否则 F1 与
	// grep 读面都看不见；同时带 mockdata 实例名，避免与真实实例日志同名互写。
	observabilityServerLogFile = "juhe-ai.mockdata-server.log"
	observabilityWorkerLogFile = "juhe-ai.stats-worker.mockdata-dev.log"

	// 内置外部来源系统与测试 Token（gateway/internal/aipublic 与
	// maintenance/internal/schema 的 seed 常量一致）：is_test_token 的判定就是
	// 「来源 id 或 Token id 命中这两个常量」。
	observabilityBuiltInTestSourceID  = "extsrc_builtin_test"
	observabilityBuiltInTestTokenID   = "exttok_builtin_test"
	observabilityBuiltInTestNameASCII = "builtin-test"

	// observabilitySearchTermMaxRunes / observabilitySearchTermLimit 是摘要检索词
	// 展开的边界：F4 的 searchTerms 会枚举到 1500 条，造数只需要页面按摘要里的
	// 词能命中，因此限制子串长度与总条数（行数与写入耗时都可控）。
	observabilitySearchTermMaxRunes = 8
	observabilitySearchTermLimit    = 400
)

// observabilityStep 是一次造数步骤：失败即整体失败（半批数据比没有更难排查）。
type observabilityStep struct {
	name string
	run  func() error
}

// seedObservability 是可观测域的入口：六步各自独立判存、独立跳过。
func seedObservability(ctx context.Context, e *env) (DomainResult, error) {
	counts := map[string]int{}
	now := e.options.clock().UTC()
	refs := observabilityCollectReferences(ctx, e)
	steps := []observabilityStep{
		{name: "审计日志", run: func() error { return seedAuditLogs(ctx, e, refs, now, counts) }},
		{name: "操作日志", run: func() error { return seedOperationLogs(ctx, e, refs, now, counts) }},
		{name: "运行日志", run: func() error { return seedRuntimeLogs(ctx, e, refs, now, counts) }},
		{name: "后台任务运行", run: func() error { return seedBackgroundTaskRuns(ctx, e, refs, now, counts) }},
		{name: "公开接口日志", run: func() error { return seedPublicAPILogs(ctx, e, refs, now, counts) }},
		{name: "后台清理目标", run: func() error { return seedRecordCleanupTargets(ctx, e, refs, now, counts) }},
	}
	for _, step := range steps {
		if err := step.run(); err != nil {
			return DomainResult{Name: DomainObservability, Counts: counts}, fmt.Errorf("%s: %w", step.name, err)
		}
	}
	// counts 只记真实写出的行数：全空数据根上 counts 必须为空，覆盖报告才会把
	// 本域判成「未接线」而不是「接线了但一行没写」。
	return DomainResult{Name: DomainObservability, Counts: counts}, nil
}

// observabilityAdd 记一次行数；0 行不入账，保持 counts 只表达真实写入。
func observabilityAdd(counts map[string]int, key string, rows int) {
	if rows > 0 {
		counts[key] += rows
	}
}

// observabilitySkip 记录一次「存储或表不存在」的跳过。跳过不是失败（首次运行
// 本来就没有这些库），但必须在日志里可查，否则「某张表为什么是空的」无从追溯。
func observabilitySkip(e *env, step, storeName, table string) {
	e.logger.Warn("mockdata 可观测域跳过：存储文件或表不存在",
		"step", step, "store", storeName, "table", table, "path", observabilityStorePath(e, storeName))
}

// observabilityStorePath 取存储路径用于日志；未登记的存储名返回空串。
func observabilityStorePath(e *env, storeName string) string {
	if item, ok := e.byName[storeName]; ok {
		return item.Path
	}
	return ""
}

// observabilityTableReady 判断存储文件与目标表是否都已存在。openExisting 不创建
// 文件，existsTable 不建表，因此「不存在」是稳定可判的跳过条件。
func observabilityTableReady(ctx context.Context, e *env, storeName, table string) (bool, error) {
	db, err := e.openExisting(storeName)
	if err != nil {
		return false, err
	}
	if db == nil {
		return false, nil
	}
	return queryExistsTable(ctx, db, table)
}

// ---------------------------------------------------------------------------
// business 域引用
// ---------------------------------------------------------------------------

// observabilityUser 是一条 mock 系统账户样本（操作日志 / 审计日志的操作者与归属）。
type observabilityUser struct {
	ID          string
	Username    string
	DisplayName string
	Role        string
}

// observabilityResource 是一条 mock 资源引用（账户 / 分组 / Key / 授权 / 公告）。
type observabilityResource struct {
	ID   string
	Name string
}

// observabilitySource 是一条外部来源系统样本及其可用 Token。
type observabilitySource struct {
	ID       string
	Name     string
	ReadOnly bool
	TokenID  string
	Token    string
	Prefix   string
	BuiltIn  bool
}

// observabilityReferences 是本域写入需要的 business 域引用。
//
// 为什么运行时查询而不是写死 ID：业务资源由 seedBusiness 生成，ID 方案属于它的
// 契约；本域只读它的库（openExisting，不创建文件），查不到时退回带清理标识的
// 约定 ID，保证造数仍可执行且报告里能看出「未关联到真实资源」。
type observabilityReferences struct {
	Users          []observabilityUser
	Accounts       []observabilityResource
	Groups         []observabilityResource
	APIKeys        []observabilityResource
	Authorizations []observabilityResource
	Announcements  []observabilityResource
	Sources        []observabilitySource
	// Resolved 记录是否从 business 库读到任何 mock 资源。
	Resolved bool
}

// observabilityFallbackUsers 是 business 域尚未产出时的兜底操作者样本。
//
// 用户名沿用设计文档的 `mockdata_*` 约定（docs/functions/Mockdata造数设计.md
// 「数据边界」：mockdata_admin 普通管理员 + 若干 mockdata_* 普通用户）。
var observabilityFallbackUsers = []observabilityUser{
	{ID: CleanupIDPrefix + "system_account_admin", Username: CleanupIDPrefix + "admin", DisplayName: CleanupNamePrefix + "管理员用户", Role: "admin"},
	{ID: CleanupIDPrefix + "system_account_ops", Username: CleanupIDPrefix + "ops", DisplayName: CleanupNamePrefix + "运维用户", Role: "user"},
	{ID: CleanupIDPrefix + "system_account_viewer", Username: CleanupIDPrefix + "viewer", DisplayName: CleanupNamePrefix + "观察用户", Role: "user"},
}

// observabilityFallbackResource 是兜底资源样本：ID 带清理标识，名称带业务前缀。
func observabilityFallbackResource(kind, suffix string) observabilityResource {
	return observabilityResource{ID: CleanupIDPrefix + kind + "_" + suffix, Name: CleanupNamePrefix + kind + " " + suffix}
}

// observabilityCollectReferences 只读地收集 business 库里的 mock 引用。
// 库或表缺失、查询失败都退化成兜底样本（记一条 warn），不让本域失败：business
// 域的数据不是本域的前置条件。
func observabilityCollectReferences(ctx context.Context, e *env) observabilityReferences {
	refs := observabilityReferences{}
	db, err := e.openExisting(StoreBusiness)
	if err != nil || db == nil {
		if err != nil {
			e.logger.Warn("mockdata 可观测域读取 business 库失败，改用兜底引用", "error", err.Error())
		}
		return observabilityCompleteReferencesFallback(refs)
	}
	refs.Users = observabilityQueryUsers(ctx, e, db)
	refs.Accounts = observabilityQueryResources(ctx, e, db, "accounts", "id", "name",
		"id LIKE '"+CleanupIDPrefix+"%' OR name LIKE '"+CleanupNamePrefix+"%'")
	refs.Groups = observabilityQueryResources(ctx, e, db, "groups", "id", "name",
		"id LIKE '"+CleanupIDPrefix+"%' OR name LIKE '"+CleanupNamePrefix+"%'")
	refs.APIKeys = observabilityQueryResources(ctx, e, db, "api_keys", "id", "name",
		"id LIKE '"+CleanupIDPrefix+"%' OR name LIKE '"+CleanupNamePrefix+"%'")
	refs.Authorizations = observabilityQueryResources(ctx, e, db, "resource_authorizations", "id", "remark",
		"id LIKE '"+CleanupIDPrefix+"%' OR remark LIKE '"+CleanupNamePrefix+"%'")
	refs.Announcements = observabilityQueryResources(ctx, e, db, "announcements", "id", "title",
		"id LIKE '"+CleanupIDPrefix+"%' OR title LIKE '"+CleanupNamePrefix+"%'")
	refs.Sources = observabilityQuerySources(ctx, e, db)
	refs.Resolved = len(refs.Users) > 0 || len(refs.Accounts) > 0 || len(refs.Groups) > 0
	return observabilityCompleteReferencesFallback(refs)
}

// observabilityCompleteReferencesFallback 给空集合补兜底，保证调用方永远有可用样本。
func observabilityCompleteReferencesFallback(refs observabilityReferences) observabilityReferences {
	if len(refs.Users) == 0 {
		refs.Users = append(refs.Users, observabilityFallbackUsers...)
	}
	if len(refs.Accounts) == 0 {
		refs.Accounts = append(refs.Accounts, observabilityFallbackResource("account", "primary"))
	}
	if len(refs.Groups) == 0 {
		refs.Groups = append(refs.Groups, observabilityFallbackResource("group", "main"))
	}
	if len(refs.APIKeys) == 0 {
		refs.APIKeys = append(refs.APIKeys, observabilityFallbackResource("api_key", "admin_main"))
	}
	if len(refs.Authorizations) == 0 {
		refs.Authorizations = append(refs.Authorizations, observabilityFallbackResource("authorization", "group"))
	}
	if len(refs.Announcements) == 0 {
		refs.Announcements = append(refs.Announcements, observabilityFallbackResource("announcement", "main"))
	}
	return refs
}

// observabilityQueryUsers 读取 mock 系统账户与内置 admin。
func observabilityQueryUsers(ctx context.Context, e *env, db *sql.DB) []observabilityUser {
	rows, err := db.QueryContext(ctx, `SELECT id, username, display_name, role FROM system_accounts
		WHERE id LIKE ? OR username LIKE ? OR display_name LIKE ?
		ORDER BY id LIMIT 8`,
		CleanupIDPrefix+"%", CleanupIDPrefix+"%", CleanupNamePrefix+"%")
	if err != nil {
		e.logger.Warn("mockdata 可观测域读取系统账户失败，改用兜底用户", "error", err.Error())
		return nil
	}
	defer rows.Close()
	users := make([]observabilityUser, 0, 8)
	for rows.Next() {
		var user observabilityUser
		if err := rows.Scan(&user.ID, &user.Username, &user.DisplayName, &user.Role); err != nil {
			e.logger.Warn("mockdata 可观测域读取系统账户行失败", "error", err.Error())
			return users
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		e.logger.Warn("mockdata 可观测域遍历系统账户失败", "error", err.Error())
	}
	return users
}

// observabilityQueryResources 读取一类 mock 资源（id + 名称列）。
// 表或列不存在（例如商品公告尚未实现）时返回 nil，由兜底补齐。
func observabilityQueryResources(ctx context.Context, e *env, db *sql.DB, table, idColumn, nameColumn, predicate string) []observabilityResource {
	query := "SELECT " + idColumn + ", COALESCE(" + nameColumn + ", '') FROM " + table +
		" WHERE " + predicate + " ORDER BY " + idColumn + " LIMIT 4"
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		e.logger.Warn("mockdata 可观测域读取 mock 资源失败，改用兜底资源", "table", table, "error", err.Error())
		return nil
	}
	defer rows.Close()
	items := make([]observabilityResource, 0, 4)
	for rows.Next() {
		var item observabilityResource
		if err := rows.Scan(&item.ID, &item.Name); err != nil {
			e.logger.Warn("mockdata 可观测域读取 mock 资源行失败", "table", table, "error", err.Error())
			return items
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		e.logger.Warn("mockdata 可观测域遍历 mock 资源失败", "table", table, "error", err.Error())
	}
	return items
}

// observabilityQuerySources 读取 mock 外部来源系统与各自可用 Token，并附带内置
// 测试来源。scopes_json 里没有任何 `:write` 来源即视为只读来源样本。
func observabilityQuerySources(ctx context.Context, e *env, db *sql.DB) []observabilitySource {
	rows, err := db.QueryContext(ctx, `SELECT s.id, s.name, COALESCE(s.scopes_json, '[]'),
			COALESCE(t.id, ''), COALESCE(t.name, ''), COALESCE(t.token_prefix, '')
		FROM external_integration_sources s
		LEFT JOIN external_integration_source_tokens t
			ON t.source_ref_id = s.id AND t.status = 'active'
		WHERE s.id LIKE ? OR s.name LIKE ? OR s.id = ?
		ORDER BY s.id LIMIT 8`,
		CleanupIDPrefix+"%", CleanupNamePrefix+"%", observabilityBuiltInTestSourceID)
	if err != nil {
		e.logger.Warn("mockdata 可观测域读取外部来源失败，公开接口日志将退化为内置测试来源", "error", err.Error())
		return nil
	}
	defer rows.Close()
	sources := make([]observabilitySource, 0, 4)
	seen := map[string]bool{}
	for rows.Next() {
		var (
			source  observabilitySource
			scopes  string
			tokenID string
			token   string
			prefix  string
		)
		if err := rows.Scan(&source.ID, &source.Name, &scopes, &tokenID, &token, &prefix); err != nil {
			e.logger.Warn("mockdata 可观测域读取外部来源行失败", "error", err.Error())
			return sources
		}
		if seen[source.ID] {
			continue
		}
		seen[source.ID] = true
		source.ReadOnly = !strings.Contains(scopes, ":write")
		source.TokenID, source.Token, source.Prefix = tokenID, token, prefix
		source.BuiltIn = source.ID == observabilityBuiltInTestSourceID
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		e.logger.Warn("mockdata 可观测域遍历外部来源失败", "error", err.Error())
	}
	return sources
}

// observabilityPickUser 按序号取样本（取模保证永远有值）。
func observabilityPickUser(users []observabilityUser, index int) observabilityUser {
	if len(users) == 0 {
		return observabilityFallbackUsers[0]
	}
	return users[index%len(users)]
}

// observabilityPickResource 按序号取资源样本。
func observabilityPickResource(items []observabilityResource, index int) observabilityResource {
	if len(items) == 0 {
		return observabilityFallbackResource("resource", "sample")
	}
	return items[index%len(items)]
}

// observabilityPrimarySource / observabilityReadOnlySource / observabilityTestSource
// 按「只读 / 内置测试 / 正式来源」三类从候选里挑样本；缺失返回零值。
func observabilitySourceByKind(sources []observabilitySource, kind string) observabilitySource {
	for _, source := range sources {
		switch kind {
		case "test":
			if source.BuiltIn {
				return source
			}
		case "readonly":
			if !source.BuiltIn && source.ReadOnly {
				return source
			}
		default:
			if !source.BuiltIn && !source.ReadOnly {
				return source
			}
		}
	}
	return observabilitySource{}
}

// ---------------------------------------------------------------------------
// 时间铺开
// ---------------------------------------------------------------------------

// observabilityFormatTime 归一化时间文本。
func observabilityFormatTime(value time.Time) string {
	return value.UTC().Format(observabilityTimeLayout)
}

// observabilityDayStart 取某天 UTC 零点。
func observabilityDayStart(value time.Time) time.Time {
	utc := value.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}

// observabilityDayIndexes 返回「近 days 天」的天序列表（0 为最早，days-1 为今天）。
func observabilityDayIndexes(days int) []int {
	indexes := make([]int, 0, days)
	for index := 0; index < days; index++ {
		indexes = append(indexes, index)
	}
	return indexes
}

// observabilitySpread 把第 index 个样本铺到某一天里：用固定步长 + 固定抖动让
// 时间既均匀又可在同一 Options 下复现，且不会越过 now（今天只用到已经过去的部分）。
func observabilitySpread(now, dayStart time.Time, index, perDay int) time.Time {
	if perDay < 1 {
		perDay = 1
	}
	budget := int(now.Sub(dayStart).Seconds())
	if budget > 23*3600 {
		budget = 23 * 3600
	}
	if budget < perDay*120 {
		budget = perDay * 120
	}
	step := budget / (perDay + 1)
	if step < 1 {
		step = 1
	}
	offset := 600 + (index+1)*step + (index*331)%step
	at := dayStart.Add(time.Duration(offset) * time.Second)
	if at.After(now) {
		at = now.Add(-time.Duration(perDay-index) * time.Minute)
	}
	return at.UTC()
}

// observabilityStamp 生成 <prefix><yyyymmdd>_<序号> 形态的稳定标识。
func observabilityStamp(prefix string, at time.Time, index int) string {
	return fmt.Sprintf("%s%s_%03d", prefix, at.UTC().Format("20060102"), index+1)
}

// observabilityBool 把 bool 写成 SQLite 的 0/1。
func observabilityBool(value bool) int {
	if value {
		return 1
	}
	return 0
}

// observabilitySetText 只写入非空文本列，保持空值在库里是 NULL（与 F3/F4 的
// 「可选字段缺省」语义一致，避免页面上出现空字符串与 NULL 两种空）。
func observabilitySetText(columns map[string]any, values map[string]string) {
	for name, value := range values {
		if strings.TrimSpace(value) != "" {
			columns[name] = value
		}
	}
}

// observabilityInsert 在事务里插入一行。列名排序后拼 SQL，与 env.insertMap 同构，
// 便于同一批数据在任何一次运行里生成逐字相同的语句。
func observabilityInsert(ctx context.Context, tx *sql.Tx, table string, columns map[string]any) error {
	if len(columns) == 0 {
		return fmt.Errorf("observabilityInsert(%s) 需要至少一列", table)
	}
	names := make([]string, 0, len(columns))
	for name := range columns {
		names = append(names, name)
	}
	sort.Strings(names)
	placeholders := make([]string, len(names))
	values := make([]any, len(names))
	for i, name := range names {
		placeholders[i] = "?"
		values[i] = columns[name]
	}
	query := "INSERT INTO " + table + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
	if _, err := tx.ExecContext(ctx, query, values...); err != nil {
		return fmt.Errorf("插入 %s: %w", table, err)
	}
	return nil
}

// observabilityExec 在事务里执行一条语句。
func observabilityExec(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("%s: %w", strings.Fields(query)[0], err)
	}
	return nil
}

// observabilityClearMockRows 删除本域上一轮写出的行（按清理标识前缀）。
//
// 为什么域内自己删而不是只靠 cleanupAll：清理步骤在 Run 里先跑，但测试与嵌入式
// 调用会直接调 seedObservability；「重复执行不报错且不叠加」是该域的契约。
func observabilityClearMockRows(ctx context.Context, tx *sql.Tx, table, column string) error {
	return observabilityExec(ctx, tx, "DELETE FROM "+table+" WHERE "+column+" LIKE ?", CleanupIDPrefix+"%")
}

// ---------------------------------------------------------------------------
// 审计日志（F3 / gateway audit-log.sqlite3）
// ---------------------------------------------------------------------------

// observabilityAuditSample 是审计日志样本矩阵的一行。
type observabilityAuditSample struct {
	Method         string
	Path           string
	Family         string
	Model          string
	UpstreamModel  string
	ResponseModel  string
	PricingModel   string
	MappingApplied bool
	MappingSource  string
	Stream         bool
	TrafficSource  string
	ClientType     string
	Outcome        string
	Success        bool
	StatusCode     int
	ErrorPhase     string
	ErrorCode      string
	ErrorMessage   string
	SampleReason   string
	Attempts       int
	Parts          []string
}

// observabilityAuditSamples 覆盖成功 / 重试成功 / 模型映射 / 上游失败 / 网关失败 /
// 请求体拒绝 / 流式中断 / 下游断开 / 健康检查流量来源，端点覆盖
// /v1/responses、/v1/chat/completions、/v1/images/generations、/v1/models。
var observabilityAuditSamples = []observabilityAuditSample{
	{
		Method: "POST", Path: "/v1/chat/completions", Family: "chat_completions",
		Model: "gpt-4.1-mini", UpstreamModel: "gpt-4.1-mini", PricingModel: "gpt-4.1-mini",
		Stream: true, TrafficSource: "gateway", ClientType: "generic_openai",
		Outcome: "success", Success: true, StatusCode: 200, SampleReason: "always",
		Attempts: 1, Parts: []string{"client_request", "upstream_response", "gateway_response"},
	},
	{
		Method: "POST", Path: "/v1/responses", Family: "responses",
		Model: "gpt-5.4", UpstreamModel: "gpt-5.4", PricingModel: "gpt-5.4",
		Stream: true, TrafficSource: "gateway", ClientType: "codex",
		Outcome: "success", Success: true, StatusCode: 200, SampleReason: "always",
		Attempts: 1, Parts: []string{"client_request", "upstream_response"},
	},
	{
		Method: "POST", Path: "/v1/images/generations", Family: "images",
		Model: "gpt-image-1", UpstreamModel: "gpt-image-1", PricingModel: "gpt-image-1",
		TrafficSource: "gateway", ClientType: "generic_openai",
		Outcome: "success", Success: true, StatusCode: 200, SampleReason: "always",
		Attempts: 1, Parts: []string{"client_request", "gateway_response"},
	},
	{
		Method: "GET", Path: "/v1/models", Family: "models",
		TrafficSource: "gateway", ClientType: "generic_openai",
		Outcome: "success", Success: true, StatusCode: 200, SampleReason: "always",
		Attempts: 1, Parts: []string{"client_request"},
	},
	{
		Method: "POST", Path: "/v1/chat/completions", Family: "chat_completions",
		Model: "gpt-4.1-mini", UpstreamModel: "gpt-4.1-mini", PricingModel: "gpt-4.1-mini",
		Stream: true, TrafficSource: "gateway", ClientType: "generic_openai",
		Outcome: "success_after_retry", Success: true, StatusCode: 200, SampleReason: "always",
		Attempts: 2, Parts: []string{"client_request", "upstream_request", "upstream_response"},
	},
	{
		Method: "POST", Path: "/v1/chat/completions", Family: "chat_completions",
		Model: "mockdata-global-long-context", UpstreamModel: "gpt-5.4-mini", PricingModel: "gpt-5.4-mini",
		MappingApplied: true, MappingSource: "account_model_mapping",
		Stream: true, TrafficSource: "gateway", ClientType: "generic_openai",
		Outcome: "success", Success: true, StatusCode: 200, SampleReason: "always",
		Attempts: 1, Parts: []string{"client_request", "upstream_request"},
	},
	{
		Method: "POST", Path: "/v1/chat/completions", Family: "chat_completions",
		Model: "gpt-4.1", UpstreamModel: "gpt-4.1", Stream: false,
		TrafficSource: "gateway", ClientType: "generic_openai",
		Outcome: "upstream_failed", Success: false, StatusCode: 502,
		ErrorPhase: "upstream_response", ErrorCode: "upstream_5xx",
		ErrorMessage: CleanupNamePrefix + "模拟上游返回 502", SampleReason: "always",
		Attempts: 2, Parts: []string{"client_request", "upstream_response", "gateway_error"},
	},
	{
		Method: "POST", Path: "/v1/chat/completions", Family: "chat_completions",
		Model: "gpt-4.1-mini", UpstreamModel: "gpt-4.1-mini", Stream: false,
		TrafficSource: "gateway", ClientType: "generic_openai",
		Outcome: "gateway_failed", Success: false, StatusCode: 401,
		ErrorPhase: "auth", ErrorCode: "invalid_api_key",
		ErrorMessage: CleanupNamePrefix + "模拟网关鉴权失败", SampleReason: "always",
		Attempts: 1, Parts: []string{"client_request", "gateway_error"},
	},
	{
		Method: "POST", Path: "/v1/responses", Family: "responses",
		Model: "gpt-5.4", UpstreamModel: "gpt-5.4", Stream: true,
		TrafficSource: "gateway", ClientType: "codex",
		Outcome: "stream_failed", Success: false, StatusCode: 200,
		ErrorPhase: "stream", ErrorCode: "upstream_protocol_error",
		ErrorMessage: CleanupNamePrefix + "模拟流式响应提前结束", SampleReason: "always",
		Attempts: 1, Parts: []string{"client_request", "upstream_response", "gateway_error"},
	},
	{
		Method: "POST", Path: "/v1/chat/completions", Family: "chat_completions",
		Model: "gpt-4.1-mini", UpstreamModel: "gpt-4.1-mini", Stream: true,
		TrafficSource: "gateway", ClientType: "generic_openai",
		Outcome: "downstream_closed", Success: false, StatusCode: 499,
		ErrorPhase: "downstream", ErrorCode: "downstream_connection_closed",
		ErrorMessage: CleanupNamePrefix + "模拟客户端提前断开", SampleReason: "always",
		Attempts: 1, Parts: []string{"client_request"},
	},
	{
		Method: "POST", Path: "/v1/responses", Family: "responses",
		Model: "gpt-5.4", UpstreamModel: "gpt-5.4", Stream: false,
		TrafficSource: "gateway", ClientType: "claude_code",
		Outcome: "gateway_failed", Success: false, StatusCode: 413,
		ErrorPhase: "request_validation", ErrorCode: "request_body_too_large",
		ErrorMessage: CleanupNamePrefix + "模拟请求体超出上限", SampleReason: "gateway_body_rejected",
		Attempts: 0, Parts: nil,
	},
	{
		Method: "POST", Path: "/v1/responses", Family: "responses",
		Model: "gpt-5.4", UpstreamModel: "gpt-5.4", Stream: false,
		TrafficSource: "account_health_check", ClientType: "generic_openai",
		Outcome: "success", Success: true, StatusCode: 200, SampleReason: "always",
		Attempts: 1, Parts: []string{"upstream_response"},
	},
	{
		Method: "POST", Path: "/v1/chat/completions", Family: "chat_completions",
		Model: "gpt-4.1-mini", UpstreamModel: "gpt-4.1-mini", Stream: false,
		TrafficSource: "manual_account_test", ClientType: "generic_openai",
		Outcome: "success", Success: true, StatusCode: 200, SampleReason: "always",
		Attempts: 1, Parts: []string{"client_request", "upstream_response"},
	},
}

// observabilityUpstreamResponseCoverageSample 是设计文档「验证点」要求的同 trace
// 上游响应模型对比样本（match / mismatch / unmapped_mismatch）。
//
// 为什么由本域写：usage 记录由用量域写，但同一 trace 的审计 `upstream_response`
// payload 只有审计造数能提供；docs/functions/Mockdata造数设计.md 明确要求三条
// trace 都命中带 payload 的审计记录，payload.model 为原始响应模型、upstream_model
// 为实际发送模型。trace 与模型按文档固定，用量域只要按同一约定写记录即可对齐。
type observabilityUpstreamResponseCoverageSample struct {
	TraceSuffix    string
	ResponseModel  string
	MappingApplied bool
}

var observabilityUpstreamResponseCoverageSamples = []observabilityUpstreamResponseCoverageSample{
	{TraceSuffix: "match", ResponseModel: "gpt-5.4-mini", MappingApplied: true},
	{TraceSuffix: "mismatch", ResponseModel: "gpt-5.4-mini-2026-03-17", MappingApplied: true},
	{TraceSuffix: "unmapped_mismatch", ResponseModel: "gpt-5.4-mini-2026-03-17", MappingApplied: false},
}

// observabilityCoverageTrace 拼出与用量域约定的 trace（保持逐字一致）。
func observabilityCoverageTrace(suffix string) string {
	return CleanupTracePrefix + "usage-coverage_upstream_response_model_" + suffix
}

// observabilityAuditRow 是一条待写入的审计日志及其子行。
type observabilityAuditRow struct {
	ID                     string
	TraceID                string
	TrafficSource          string
	SystemAccountID        string
	APIKeyID               string
	GroupID                string
	AccountID              string
	SessionID              string
	SessionClientType      string
	ProviderCode           string
	Method                 string
	Path                   string
	QueryString            string
	Model                  string
	UpstreamModel          string
	PricingModel           string
	ResponseModel          string
	MappingApplied         bool
	MappingSource          string
	Family                 string
	Stream                 bool
	ClientIP               string
	UserAgent              string
	Outcome                string
	Success                bool
	StatusCode             int
	ErrorPhase             string
	ErrorCode              string
	ErrorMessage           string
	SampleReason           string
	SampleBucket           int
	Attempts               []observabilityAuditAttempt
	Parts                  []string
	StartedAt              time.Time
	EndedAt                time.Time
	DurationMS             int
	FirstTokenMS           int
	RawPayloadBytes        int
	CompressedPayloadBytes int
}

// observabilityAuditAttempt 是一条审计尝试行。
type observabilityAuditAttempt struct {
	ID         string
	Index      int
	AccountID  string
	Success    bool
	StatusCode int
	ErrorPhase string
	ErrorCode  string
	Message    string
	StartedAt  time.Time
	EndedAt    time.Time
	DurationMS int
}

// observabilityAuditBlob 是一次 payload blob 的计划（元数据 + 存储字节）。
type observabilityAuditBlob struct {
	ID          string
	SHA256      string
	StorageKey  string
	ContentType string
	Bytes       []byte
	RefCount    int
}

// observabilityNewAuditBlob 复刻 F3 newBlobRecord 的 id / storage_key 算法：
// 小 JSON 落在 4KiB 压缩阈值之下，因此存储形态固定为未压缩的 .blob。
func observabilityNewAuditBlob(body any) (observabilityAuditBlob, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return observabilityAuditBlob{}, fmt.Errorf("编码审计 payload: %w", err)
	}
	contentType := "application/json"
	sum := sha256.Sum256(encoded)
	digest := hex.EncodeToString(sum[:])
	contentSum := sha256.Sum256([]byte(contentType))
	shortHash := hex.EncodeToString(contentSum[:observabilityBlobDigestBytes])
	storageKey := filepath.ToSlash(filepath.Join("sha256", digest+"-"+shortHash+".blob"))
	return observabilityAuditBlob{
		ID:          "blob:" + digest + ":" + shortHash,
		SHA256:      digest,
		StorageKey:  storageKey,
		ContentType: contentType,
		Bytes:       encoded,
		RefCount:    0,
	}, nil
}

// observabilityAuditPayloadBody 给出某一 payload 部位的内容。上游响应 payload 里
// 的 `model` 是「原始响应模型」——上游模型对比验收就是读这个字段。
func observabilityAuditPayloadBody(partType string, row observabilityAuditRow) any {
	switch partType {
	case "client_request":
		return map[string]any{
			"model":  row.Model,
			"stream": row.Stream,
			"messages": []any{
				map[string]any{"role": "user", "content": CleanupNamePrefix + "审计样本请求"},
			},
		}
	case "upstream_request":
		return map[string]any{
			"model":  row.UpstreamModel,
			"stream": row.Stream,
			"messages": []any{
				map[string]any{"role": "user", "content": CleanupNamePrefix + "审计样本上游请求"},
			},
		}
	case "upstream_response":
		responseModel := row.ResponseModel
		if responseModel == "" {
			responseModel = row.UpstreamModel
		}
		return map[string]any{
			"id":      "chatcmpl-" + row.ID,
			"object":  "chat.completion",
			"model":   responseModel,
			"choices": []any{map[string]any{"index": 0, "finish_reason": observabilityAuditFinishReason(row)}},
			"usage":   map[string]any{"prompt_tokens": 812, "completion_tokens": 143, "total_tokens": 955},
		}
	case "gateway_response":
		return map[string]any{
			"id":     row.ID,
			"object": "response",
			"model":  row.Model,
			"status": observabilityAuditGatewayStatus(row),
		}
	case "gateway_error":
		return map[string]any{
			"error": map[string]any{"message": row.ErrorMessage, "code": row.ErrorCode},
		}
	default:
		return map[string]any{"mockdata": true, "partType": partType, "traceId": row.TraceID}
	}
}

// observabilityAuditFinishReason 让成功样本与失败样本的 finish_reason 可区分。
func observabilityAuditFinishReason(row observabilityAuditRow) string {
	if row.Success {
		return "stop"
	}
	return "error"
}

// observabilityAuditGatewayStatus 同上，用于网关响应 payload 的 status。
func observabilityAuditGatewayStatus(row observabilityAuditRow) string {
	if row.Success {
		return "completed"
	}
	return "failed"
}

// seedAuditLogs 写入审计日志、尝试、payload blob / 引用与错误分组。
func seedAuditLogs(ctx context.Context, e *env, refs observabilityReferences, now time.Time, counts map[string]int) error {
	ready, err := observabilityTableReady(ctx, e, StoreAuditLog, "audit_logs")
	if err != nil {
		return err
	}
	if !ready {
		observabilitySkip(e, "审计日志", StoreAuditLog, "audit_logs")
		return nil
	}
	rows := observabilityBuildAuditRows(e, refs, now)
	// payload 的物理 blob 先落地：文件系统没有事务，若先写库再写文件，中途失败会
	// 留下「元数据存在但文件缺失」的 payload（读面会报 file_missing）。
	blobs, err := observabilityWriteAuditBlobs(e, rows)
	if err != nil {
		return err
	}
	return e.tx(ctx, StoreAuditLog, func(tx *sql.Tx) error {
		for _, table := range []string{"audit_logs", "audit_log_attempts", "audit_payload_refs", "audit_error_groups"} {
			if err := observabilityClearMockRows(ctx, tx, table, "id"); err != nil {
				return err
			}
		}
		auditRows, attemptRows, refRows := 0, 0, 0
		for _, row := range rows {
			if err := observabilityInsert(ctx, tx, "audit_logs", observabilityAuditLogColumns(row)); err != nil {
				return err
			}
			auditRows++
			for _, attempt := range row.Attempts {
				if err := observabilityInsert(ctx, tx, "audit_log_attempts", observabilityAuditAttemptColumns(row, attempt)); err != nil {
					return err
				}
				attemptRows++
			}
			sequence := 0
			for _, part := range row.Parts {
				sequence++
				if err := observabilityInsert(ctx, tx, "audit_payload_refs", observabilityAuditPayloadRefColumns(row, part, sequence)); err != nil {
					return err
				}
				refRows++
			}
		}
		for _, blob := range blobs {
			columns := map[string]any{
				"id": blob.ID, "sha256": blob.SHA256,
				"raw_size_bytes": len(blob.Bytes), "compressed_size_bytes": len(blob.Bytes),
				"content_type": blob.ContentType, "compression": "none", "storage_key": blob.StorageKey,
				"ref_count": blob.RefCount, "first_seen_at": observabilityFormatTime(now),
				"last_seen_at": observabilityFormatTime(now), "created_at": observabilityFormatTime(now),
			}
			// 同一份 payload 在重复执行时必须复用同一行：blob 的 id 与 storage_key
			// 由内容推导（不含清理标识），因此按 F3 的 UNIQUE 键做 upsert 而不是
			// 让第二次运行撞唯一约束。
			query := `INSERT INTO audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,` +
				`compression,storage_key,ref_count,first_seen_at,last_seen_at,created_at) ` +
				`VALUES (?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(sha256,raw_size_bytes,content_type) DO UPDATE SET ` +
				`ref_count = excluded.ref_count, last_seen_at = excluded.last_seen_at`
			if _, err := tx.ExecContext(ctx, query,
				columns["id"], columns["sha256"], columns["raw_size_bytes"], columns["compressed_size_bytes"],
				columns["content_type"], columns["compression"], columns["storage_key"], columns["ref_count"],
				columns["first_seen_at"], columns["last_seen_at"], columns["created_at"]); err != nil {
				return fmt.Errorf("写入 audit_payload_blobs: %w", err)
			}
		}
		groupRows, err := observabilityWriteAuditErrorGroups(ctx, tx, rows, now)
		if err != nil {
			return err
		}
		observabilityAdd(counts, "auditLogs", auditRows)
		observabilityAdd(counts, "auditLogAttempts", attemptRows)
		observabilityAdd(counts, "auditPayloadBlobs", len(blobs))
		observabilityAdd(counts, "auditPayloadRefs", refRows)
		observabilityAdd(counts, "auditErrorGroups", groupRows)
		return nil
	})
}

// observabilityBuildAuditRows 生成近 Days 天的审计样本。
func observabilityBuildAuditRows(e *env, refs observabilityReferences, now time.Time) []observabilityAuditRow {
	days := e.options.Days
	rows := make([]observabilityAuditRow, 0, days*8)
	index := 0
	for _, dayIndex := range observabilityDayIndexes(days) {
		dayStart := observabilityDayStart(now).AddDate(0, 0, -(days - 1 - dayIndex))
		perDay := 6 + (dayIndex*5)%15
		for rowIndex := 0; rowIndex < perDay; rowIndex++ {
			sample := observabilityAuditSamples[index%len(observabilityAuditSamples)]
			at := observabilitySpread(now, dayStart, rowIndex, perDay)
			duration := 120 + (index*137)%2400
			user := observabilityPickUser(refs.Users, index)
			resource := observabilityPickResource(refs.Accounts, index)
			key := observabilityPickResource(refs.APIKeys, index)
			group := observabilityPickResource(refs.Groups, index)
			row := observabilityAuditRow{
				ID:                observabilityStamp(CleanupIDPrefix+"audit_", at, index),
				TraceID:           CleanupTracePrefix + "audit-" + at.Format("20060102") + "-" + fmt.Sprintf("%03d", index+1),
				TrafficSource:     sample.TrafficSource,
				SystemAccountID:   user.ID,
				APIKeyID:          key.ID,
				GroupID:           group.ID,
				AccountID:         resource.ID,
				SessionID:         CleanupTracePrefix + "session-" + at.Format("20060102") + "-" + fmt.Sprintf("%03d", index+1),
				SessionClientType: sample.ClientType,
				ProviderCode:      "gpt",
				Method:            sample.Method,
				Path:              sample.Path,
				QueryString:       "",
				Model:             sample.Model,
				UpstreamModel:     sample.UpstreamModel,
				PricingModel:      sample.PricingModel,
				ResponseModel:     sample.ResponseModel,
				MappingApplied:    sample.MappingApplied,
				MappingSource:     sample.MappingSource,
				Family:            sample.Family,
				Stream:            sample.Stream,
				ClientIP:          fmt.Sprintf("172.20.%d.%d", index%16, 20+(index%180)),
				UserAgent:         observabilityAuditUserAgent(index),
				Outcome:           sample.Outcome,
				Success:           sample.Success,
				StatusCode:        sample.StatusCode,
				ErrorPhase:        sample.ErrorPhase,
				ErrorCode:         sample.ErrorCode,
				ErrorMessage:      sample.ErrorMessage,
				SampleReason:      sample.SampleReason,
				SampleBucket:      index % 10,
				Parts:             sample.Parts,
				StartedAt:         at,
				EndedAt:           at.Add(time.Duration(duration) * time.Millisecond),
				DurationMS:        duration,
				FirstTokenMS:      duration / 3,
			}
			row.Attempts = observabilityBuildAuditAttempts(row, sample)
			rows = append(rows, row)
			index++
		}
	}
	// 上游响应模型对比样本落在「今天」，与用量域的同 trace 记录同一天可见。
	coverageDay := observabilityDayStart(now)
	for i, coverage := range observabilityUpstreamResponseCoverageSamples {
		at := observabilitySpread(now, coverageDay, i, len(observabilityUpstreamResponseCoverageSamples)+1)
		user := observabilityPickUser(refs.Users, i)
		resource := observabilityPickResource(refs.Accounts, i)
		row := observabilityAuditRow{
			ID:                CleanupIDPrefix + "audit_coverage_upstream_response_model_" + coverage.TraceSuffix,
			TraceID:           observabilityCoverageTrace(coverage.TraceSuffix),
			TrafficSource:     "gateway",
			SystemAccountID:   user.ID,
			APIKeyID:          observabilityPickResource(refs.APIKeys, i).ID,
			GroupID:           observabilityPickResource(refs.Groups, i).ID,
			AccountID:         resource.ID,
			SessionID:         CleanupTracePrefix + "session-coverage-" + coverage.TraceSuffix,
			SessionClientType: "generic_openai",
			ProviderCode:      "gpt",
			Method:            "POST",
			Path:              "/v1/chat/completions",
			Model:             observabilityCoverageRequestModel(coverage),
			UpstreamModel:     "gpt-5.4-mini",
			PricingModel:      "gpt-5.4-mini",
			ResponseModel:     coverage.ResponseModel,
			MappingApplied:    coverage.MappingApplied,
			MappingSource:     observabilityCoverageMappingSource(coverage),
			Family:            "chat_completions",
			ClientIP:          fmt.Sprintf("172.20.9.%d", 20+i),
			UserAgent:         CleanupIDPrefix + "coverage-client/1.0",
			Outcome:           "success",
			Success:           true,
			StatusCode:        200,
			SampleReason:      "always",
			SampleBucket:      0,
			Parts:             []string{"client_request", "upstream_response"},
			StartedAt:         at,
			EndedAt:           at.Add(900 * time.Millisecond),
			DurationMS:        900,
			FirstTokenMS:      240,
		}
		row.Attempts = observabilityBuildAuditAttempts(row, observabilityAuditSample{Attempts: 1})
		rows = append(rows, row)
	}
	return rows
}

// observabilityCoverageRequestModel 给三条对比样本请求模型：前两条走账号模型映射，
// 第三条故意用未映射模型（设计文档「验证点」的口径）。
func observabilityCoverageRequestModel(coverage observabilityUpstreamResponseCoverageSample) string {
	if coverage.MappingApplied {
		return "mockdata-global-long-context"
	}
	return "gpt-5.4-mini"
}

// observabilityCoverageMappingSource 只在发生映射时给出来源。
func observabilityCoverageMappingSource(coverage observabilityUpstreamResponseCoverageSample) string {
	if coverage.MappingApplied {
		return "account_model_mapping"
	}
	return ""
}

// observabilityAuditUserAgent 给请求样本一个稳定的 User-Agent 分布。
func observabilityAuditUserAgent(index int) string {
	switch index % 4 {
	case 0:
		return CleanupIDPrefix + "gateway-client/1.0"
	case 1:
		return "OpenAI/Python 1.99.0"
	case 2:
		return "codex_cli_rs/0.20.0"
	default:
		return "node-fetch/1.0 (+https://example.invalid)"
	}
}

// observabilityBuildAuditAttempts 生成尝试行：失败样本的第一次尝试失败、第二次成功。
func observabilityBuildAuditAttempts(row observabilityAuditRow, sample observabilityAuditSample) []observabilityAuditAttempt {
	if sample.Attempts <= 0 {
		return nil
	}
	attempts := make([]observabilityAuditAttempt, 0, sample.Attempts)
	for index := 0; index < sample.Attempts; index++ {
		attempt := observabilityAuditAttempt{
			ID:         row.ID + "_attempt_" + fmt.Sprintf("%d", index+1),
			Index:      index + 1,
			AccountID:  row.AccountID,
			Success:    row.Success,
			StatusCode: row.StatusCode,
			ErrorPhase: row.ErrorPhase,
			ErrorCode:  row.ErrorCode,
			Message:    row.ErrorMessage,
			StartedAt:  row.StartedAt.Add(time.Duration(index) * 400 * time.Millisecond),
			EndedAt:    row.StartedAt.Add(time.Duration(index)*400*time.Millisecond + time.Duration(row.DurationMS/sample.Attempts)*time.Millisecond),
			DurationMS: row.DurationMS / sample.Attempts,
		}
		if index < sample.Attempts-1 {
			// 多尝试样本的中间尝试是失败的：这正是 success_after_retry 的含义。
			attempt.Success = false
			attempt.StatusCode = 502
			attempt.ErrorPhase = "upstream_response"
			attempt.ErrorCode = "upstream_5xx"
			attempt.Message = CleanupNamePrefix + "模拟首次尝试上游 502"
		}
		attempts = append(attempts, attempt)
	}
	return attempts
}

// observabilityAuditLogColumns 把样本映射成 audit_logs 的列集合。
func observabilityAuditLogColumns(row observabilityAuditRow) map[string]any {
	columns := map[string]any{
		"id":                       row.ID,
		"trace_id":                 row.TraceID,
		"traffic_source":           row.TrafficSource,
		"method":                   row.Method,
		"path":                     row.Path,
		"audit_outcome":            row.Outcome,
		"success":                  observabilityBool(row.Success),
		"stream":                   observabilityBool(row.Stream),
		"model_mapping_applied":    observabilityBool(row.MappingApplied),
		"sample_bucket":            row.SampleBucket,
		"sample_reason":            row.SampleReason,
		"attempt_count":            len(row.Attempts),
		"payload_count":            len(row.Parts),
		"raw_payload_bytes":        row.RawPayloadBytes,
		"compressed_payload_bytes": row.CompressedPayloadBytes,
		"compression_saved_bytes":  row.RawPayloadBytes - row.CompressedPayloadBytes,
		"capture_status":           observabilityAuditCaptureStatus(row),
		"lifecycle_status":         "finalized",
		"started_at":               observabilityFormatTime(row.StartedAt),
		"ended_at":                 observabilityFormatTime(row.EndedAt),
		"created_at":               observabilityFormatTime(row.EndedAt),
		"duration_ms":              row.DurationMS,
		"http_completed_at":        observabilityFormatTime(row.EndedAt),
		"http_duration_ms":         row.DurationMS,
		"first_token_ms":           row.FirstTokenMS,
		"source_endpoint_family":   row.Family,
		"upstream_endpoint_family": row.Family,
	}
	if row.StatusCode > 0 {
		columns["final_status_code"] = row.StatusCode
	}
	observabilitySetText(columns, map[string]string{
		"system_account_id":    row.SystemAccountID,
		"api_key_id":           row.APIKeyID,
		"group_id":             row.GroupID,
		"account_id":           row.AccountID,
		"session_id":           row.SessionID,
		"session_client_type":  row.SessionClientType,
		"provider_code":        row.ProviderCode,
		"query_string":         row.QueryString,
		"model":                row.Model,
		"upstream_model":       row.UpstreamModel,
		"pricing_model":        row.PricingModel,
		"model_mapping_source": row.MappingSource,
		"client_ip":            row.ClientIP,
		"user_agent":           row.UserAgent,
		"error_phase":          row.ErrorPhase,
		"error_code":           row.ErrorCode,
		"error_message":        row.ErrorMessage,
	})
	return columns
}

// observabilityAuditCaptureStatus 让 capture 状态与是否带 payload 一致。
func observabilityAuditCaptureStatus(row observabilityAuditRow) string {
	if len(row.Parts) == 0 {
		return "metadata_only"
	}
	return "complete"
}

// observabilityAuditAttemptColumns 映射 audit_log_attempts 列。
func observabilityAuditAttemptColumns(row observabilityAuditRow, attempt observabilityAuditAttempt) map[string]any {
	columns := map[string]any{
		"id":                            attempt.ID,
		"audit_log_id":                  row.ID,
		"attempt_index":                 attempt.Index,
		"success":                       observabilityBool(attempt.Success),
		"attempt_model":                 row.Model,
		"attempt_upstream_model":        row.UpstreamModel,
		"attempt_pricing_model":         row.PricingModel,
		"attempt_model_mapping_applied": observabilityBool(row.MappingApplied),
		"upstream_method":               row.Method,
		"upstream_url":                  observabilityAuditUpstreamURL(row),
		"started_at":                    observabilityFormatTime(attempt.StartedAt),
		"ended_at":                      observabilityFormatTime(attempt.EndedAt),
		"duration_ms":                   attempt.DurationMS,
	}
	if attempt.StatusCode > 0 {
		columns["upstream_status_code"] = attempt.StatusCode
	}
	observabilitySetText(columns, map[string]string{
		"account_id":                       attempt.AccountID,
		"account_owner_system_account_id":  row.SystemAccountID,
		"group_id":                         row.GroupID,
		"provider_code":                    row.ProviderCode,
		"attempt_model_mapping_source":     row.MappingSource,
		"attempt_source_endpoint_family":   row.Family,
		"attempt_upstream_endpoint_family": row.Family,
		"error_phase":                      attempt.ErrorPhase,
		"error_code":                       attempt.ErrorCode,
		"error_message":                    attempt.Message,
	})
	return columns
}

// observabilityAuditUpstreamURL 给尝试行一个真实形态的上游 URL（不回显任何密钥）。
func observabilityAuditUpstreamURL(row observabilityAuditRow) string {
	switch row.Family {
	case "images":
		return "https://api.openai.example.invalid/v1/images/generations"
	case "models":
		return "https://api.openai.example.invalid/v1/models"
	case "responses":
		return "https://api.openai.example.invalid/v1/responses"
	default:
		return "https://api.openai.example.invalid/v1/chat/completions"
	}
}

// observabilityAuditPayloadRefColumns 映射 audit_payload_refs 列。
// body_sha256 与 blob 元数据同源，读面按 sha256 + storage_key 找物理文件。
func observabilityAuditPayloadRefColumns(row observabilityAuditRow, partType string, sequence int) map[string]any {
	blob, err := observabilityNewAuditBlob(observabilityAuditPayloadBody(partType, row))
	if err != nil {
		// 内容全部由本域常量构造，编码失败不可达；给空 sha 让写入失败可见而不是静默丢列。
		blob = observabilityAuditBlob{}
	}
	columns := map[string]any{
		"id":                    row.ID + "_payload_" + fmt.Sprintf("%d", sequence),
		"audit_log_id":          row.ID,
		"part_type":             partType,
		"sequence_index":        sequence,
		"content_type":          blob.ContentType,
		"body_blob_id":          blob.ID,
		"body_sha256":           blob.SHA256,
		"raw_size_bytes":        len(blob.Bytes),
		"compressed_size_bytes": len(blob.Bytes),
		"capture_status":        "complete",
		"created_at":            observabilityFormatTime(row.EndedAt),
	}
	if len(row.Attempts) > 0 {
		columns["attempt_id"] = row.Attempts[len(row.Attempts)-1].ID
	}
	return columns
}

// observabilityWriteAuditBlobs 写出全部 payload 的物理 blob 并回填行内 payload 体积。
//
// 为什么在这里回填：audit_logs 的 payload_count / raw_payload_bytes 由 F3 从实际
// payload 计算；造数要保证同一行的列与 blob 元数据自洽（页面上的 payload 体积与
// 详情文件一致）。
func observabilityWriteAuditBlobs(e *env, rows []observabilityAuditRow) ([]observabilityAuditBlob, error) {
	byID := map[string]*observabilityAuditBlob{}
	order := make([]string, 0, len(rows))
	for index := range rows {
		row := &rows[index]
		raw, compressed := 0, 0
		for _, part := range row.Parts {
			blob, err := observabilityNewAuditBlob(observabilityAuditPayloadBody(part, *row))
			if err != nil {
				return nil, err
			}
			raw += len(blob.Bytes)
			compressed += len(blob.Bytes)
			if existing, ok := byID[blob.ID]; ok {
				existing.RefCount++
				continue
			}
			candidate := blob
			byID[blob.ID] = &candidate
			order = append(order, blob.ID)
		}
		row.RawPayloadBytes = raw
		row.CompressedPayloadBytes = compressed
	}
	blobs := make([]observabilityAuditBlob, 0, len(order))
	for _, id := range order {
		blob := *byID[id]
		if err := observabilityWriteAuditBlobFile(e, blob); err != nil {
			return nil, err
		}
		blobs = append(blobs, blob)
	}
	sort.Slice(blobs, func(left, right int) bool { return blobs[left].ID < blobs[right].ID })
	return blobs, nil
}

// observabilityWriteAuditBlobFile 把 blob 字节写到专用目录；内容相同即视为已存在。
//
// 尺寸不一致说明磁盘上的对象与元数据不是同一份内容（内容由本域常量决定，正常
// 情况下不会发生），这属于结构错误，直接失败而不是覆盖别人的文件。
func observabilityWriteAuditBlobFile(e *env, blob observabilityAuditBlob) error {
	path := filepath.Join(e.options.Paths.AuditBlobDirectory, filepath.FromSlash(blob.StorageKey))
	if info, err := os.Stat(path); err == nil {
		if info.Size() != int64(len(blob.Bytes)) {
			return fmt.Errorf("审计 payload blob 尺寸不一致（元数据 %d 字节，磁盘 %d 字节）：%s", len(blob.Bytes), info.Size(), path)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查审计 payload blob %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建审计 payload 目录 %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, blob.Bytes, 0o644); err != nil {
		return fmt.Errorf("写入审计 payload blob %s: %w", path, err)
	}
	return nil
}

// observabilityWriteAuditErrorGroups 按 (错误码, 阶段, 状态码) 聚合失败样本，
// 写 audit_error_groups 并把 group id 回填到对应的 audit_logs 行。
func observabilityWriteAuditErrorGroups(ctx context.Context, tx *sql.Tx, rows []observabilityAuditRow, now time.Time) (int, error) {
	type groupKey struct {
		code   string
		phase  string
		status int
	}
	groups := map[groupKey][]observabilityAuditRow{}
	order := make([]groupKey, 0, 4)
	for _, row := range rows {
		if row.Success {
			continue
		}
		key := groupKey{code: row.ErrorCode, phase: row.ErrorPhase, status: row.StatusCode}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], row)
	}
	for index, key := range order {
		members := groups[key]
		first, last := members[0], members[0]
		for _, member := range members {
			if member.StartedAt.Before(first.StartedAt) {
				first = member
			}
			if member.EndedAt.After(last.EndedAt) {
				last = member
			}
		}
		groupID := fmt.Sprintf("%saudit_error_group_%02d", CleanupIDPrefix, index+1)
		fingerprint := fmt.Sprintf("%serror-%s-%s-%d", CleanupTracePrefix, key.code, key.phase, key.status)
		columns := map[string]any{
			"id":                  groupID,
			"fingerprint":         fingerprint,
			"window_started_at":   observabilityFormatTime(first.StartedAt),
			"window_ended_at":     observabilityFormatTime(last.EndedAt),
			"count":               len(members),
			"first_event_id":      first.ID,
			"last_event_id":       last.ID,
			"sample_event_id":     first.ID,
			"created_at":          observabilityFormatTime(first.StartedAt),
			"updated_at":          observabilityFormatTime(now),
			"status_code":         key.status,
			"error_phase":         key.phase,
			"error_code":          key.code,
			"error_type":          observabilityAuditErrorType(key.phase),
			"request_fingerprint": fingerprint + "-request",
			"error_fingerprint":   fingerprint,
			"last_message":        first.ErrorMessage,
			"system_account_id":   first.SystemAccountID,
			"api_key_id":          first.APIKeyID,
			"group_id":            first.GroupID,
			"account_id":          first.AccountID,
			"provider_code":       first.ProviderCode,
			"path":                first.Path,
			"model":               first.Model,
		}
		if err := observabilityInsert(ctx, tx, "audit_error_groups", columns); err != nil {
			return 0, err
		}
		for _, member := range members {
			if err := observabilityExec(ctx, tx, "UPDATE audit_logs SET error_group_id = ? WHERE id = ?", groupID, member.ID); err != nil {
				return 0, err
			}
		}
	}
	return len(order), nil
}

// observabilityAuditErrorType 把失败阶段归到三类粗粒度类型（页面按类型筛选）。
func observabilityAuditErrorType(phase string) string {
	switch phase {
	case "upstream_request", "upstream_response", "stream":
		return "upstream"
	case "downstream":
		return "downstream"
	default:
		return "gateway"
	}
}

// ---------------------------------------------------------------------------
// 操作日志（F4 / gateway operation-log.sqlite3）
// ---------------------------------------------------------------------------

// observabilityOperationSample 是操作日志样本矩阵的一行：动作一律取自 gateway
// 各模块真实写出的 module / action / operationKey / resourceType 组合。
type observabilityOperationSample struct {
	Module          string
	Action          string
	OperationKey    string
	ResourceType    string
	ResourceKind    string
	Summary         string
	Mode            string
	VisibilityScope string
	DetailLevel     string
	Method          string
	Path            string
	StatusCode      int
	Changes         []observabilityOperationChange
	ExtraTarget     string
}

// observabilityOperationChange 对应 F4 的 changes_json 元素。
type observabilityOperationChange struct {
	Field  string
	Label  string
	Before string
	After  string
}

// observabilityOperationSamples 覆盖账户 / 分组 / Key / 授权 / 公告的
// create/update/delete，以及登录会话类（auth 模块）动作。
var observabilityOperationSamples = []observabilityOperationSample{
	{
		Module: "accounts", Action: "create", OperationKey: "accounts.create",
		ResourceType: "account", ResourceKind: "account",
		Summary: CleanupNamePrefix + "创建 AI 账户", Mode: "self", VisibilityScope: "targeted", DetailLevel: "full",
		Method: "POST", Path: "/__aisys__/api/accounts", StatusCode: 201,
		Changes: []observabilityOperationChange{{Field: "status", Label: "状态", Before: "", After: "pending_test"}},
	},
	{
		Module: "accounts", Action: "update", OperationKey: "accounts.update",
		ResourceType: "account", ResourceKind: "account",
		Summary: CleanupNamePrefix + "更新 AI 账户", Mode: "self", VisibilityScope: "targeted", DetailLevel: "full",
		Method: "PATCH", Path: "/__aisys__/api/accounts/mockdata", StatusCode: 200,
		Changes: []observabilityOperationChange{{Field: "priority", Label: "优先级", Before: "0", After: "10"}},
	},
	{
		Module: "accounts", Action: "delete", OperationKey: "accounts.delete",
		ResourceType: "account", ResourceKind: "account",
		Summary: CleanupNamePrefix + "删除 AI 账户", Mode: "self", VisibilityScope: "targeted", DetailLevel: "summary",
		Method: "DELETE", Path: "/__aisys__/api/accounts/mockdata", StatusCode: 200,
		Changes: []observabilityOperationChange{{Field: "status", Label: "状态", Before: "active", After: ""}},
	},
	{
		Module: "groups", Action: "create", OperationKey: "groups.create",
		ResourceType: "group", ResourceKind: "group",
		Summary: CleanupNamePrefix + "创建 AI 分组", Mode: "self", VisibilityScope: "targeted", DetailLevel: "full",
		Method: "POST", Path: "/__aisys__/api/groups", StatusCode: 201,
		Changes: []observabilityOperationChange{{Field: "groupType", Label: "分组类型", Before: "", After: "personal"}},
	},
	{
		Module: "groups", Action: "update", OperationKey: "groups.update",
		ResourceType: "group", ResourceKind: "group",
		Summary: CleanupNamePrefix + "更新 AI 分组", Mode: "self", VisibilityScope: "targeted", DetailLevel: "full",
		Method: "PATCH", Path: "/__aisys__/api/groups/mockdata", StatusCode: 200,
		Changes: []observabilityOperationChange{{Field: "enabled", Label: "启用", Before: "true", After: "false"}},
	},
	{
		Module: "groups", Action: "delete", OperationKey: "groups.delete",
		ResourceType: "group", ResourceKind: "group",
		Summary: CleanupNamePrefix + "删除 AI 分组", Mode: "self", VisibilityScope: "targeted", DetailLevel: "summary",
		Method: "DELETE", Path: "/__aisys__/api/groups/mockdata", StatusCode: 200,
	},
	{
		Module: "api_keys", Action: "create", OperationKey: "api_keys.create",
		ResourceType: "api_key", ResourceKind: "api_key",
		Summary: CleanupNamePrefix + "创建 API Key", Mode: "self", VisibilityScope: "targeted", DetailLevel: "full",
		Method: "POST", Path: "/__aisys__/api/api-keys", StatusCode: 201,
		Changes: []observabilityOperationChange{{Field: "status", Label: "状态", Before: "", After: "active"}},
	},
	{
		Module: "api_keys", Action: "update", OperationKey: "api_keys.update",
		ResourceType: "api_key", ResourceKind: "api_key",
		Summary: CleanupNamePrefix + "更新 API Key", Mode: "self", VisibilityScope: "targeted", DetailLevel: "full",
		Method: "PATCH", Path: "/__aisys__/api/api-keys/mockdata", StatusCode: 200,
		Changes: []observabilityOperationChange{{Field: "status", Label: "状态", Before: "active", After: "disabled"}},
	},
	{
		Module: "api_keys", Action: "delete", OperationKey: "api_keys.delete",
		ResourceType: "api_key", ResourceKind: "api_key",
		Summary: CleanupNamePrefix + "删除 API Key", Mode: "self", VisibilityScope: "targeted", DetailLevel: "summary",
		Method: "DELETE", Path: "/__aisys__/api/api-keys/mockdata", StatusCode: 200,
	},
	{
		Module: "authorizations", Action: "create", OperationKey: "authorizations.create",
		ResourceType: "authorization", ResourceKind: "authorization",
		Summary: CleanupNamePrefix + "创建资源授权", Mode: "self", VisibilityScope: "targeted", DetailLevel: "full",
		Method: "POST", Path: "/__aisys__/api/authorizations", StatusCode: 201,
		Changes:     []observabilityOperationChange{{Field: "scope", Label: "授权范围", Before: "", After: "use"}},
		ExtraTarget: "owner",
	},
	{
		Module: "authorizations", Action: "revoke", OperationKey: "authorizations.revoke",
		ResourceType: "authorization", ResourceKind: "authorization",
		Summary: CleanupNamePrefix + "回收资源授权", Mode: "self", VisibilityScope: "targeted", DetailLevel: "full",
		Method: "POST", Path: "/__aisys__/api/authorizations/revoke", StatusCode: 200,
		Changes: []observabilityOperationChange{{Field: "status", Label: "状态", Before: "active", After: "revoked"}},
	},
	{
		Module: "authorizations", Action: "return", OperationKey: "authorizations.return",
		ResourceType: "authorization", ResourceKind: "authorization",
		Summary: CleanupNamePrefix + "归还资源授权", Mode: "self", VisibilityScope: "targeted", DetailLevel: "full",
		Method: "POST", Path: "/__aisys__/api/authorizations/return", StatusCode: 200,
		Changes: []observabilityOperationChange{{Field: "status", Label: "状态", Before: "active", After: "returned"}},
	},
	{
		Module: "announcements", Action: "create", OperationKey: "announcements.create",
		ResourceType: "announcement", ResourceKind: "announcement",
		Summary: CleanupNamePrefix + "发布公告", Mode: "admin", VisibilityScope: "all_users", DetailLevel: "summary",
		Method: "POST", Path: "/__aisys__/api/announcements", StatusCode: 201,
		Changes: []observabilityOperationChange{{Field: "status", Label: "状态", Before: "draft", After: "published"}},
	},
	{
		Module: "announcements", Action: "update", OperationKey: "announcements.update",
		ResourceType: "announcement", ResourceKind: "announcement",
		Summary: CleanupNamePrefix + "更新公告", Mode: "admin", VisibilityScope: "all_users", DetailLevel: "summary",
		Method: "PATCH", Path: "/__aisys__/api/announcements/mockdata", StatusCode: 200,
		Changes: []observabilityOperationChange{{Field: "level", Label: "级别", Before: "info", After: "warning"}},
	},
	{
		Module: "announcements", Action: "delete", OperationKey: "announcements.delete",
		ResourceType: "announcement", ResourceKind: "announcement",
		Summary: CleanupNamePrefix + "删除公告", Mode: "admin", VisibilityScope: "admin_only", DetailLevel: "summary",
		Method: "DELETE", Path: "/__aisys__/api/announcements/mockdata", StatusCode: 200,
	},
	{
		// 登录会话类：Go 侧没有独立 login 操作键，auth 模块的临时访问令牌
		// create/revoke 是当前唯一可用的会话动作。
		Module: "auth", Action: "create", OperationKey: "auth.temporary_access_token.create",
		ResourceType: "system_session", ResourceKind: "session",
		Summary: CleanupNamePrefix + "申请临时访问令牌，900 秒后过期", Mode: "self", VisibilityScope: "targeted", DetailLevel: "summary",
		Method: "POST", Path: "/__aisys__/api/auth/temporary-access-token", StatusCode: 201,
	},
	{
		Module: "auth", Action: "delete", OperationKey: "auth.temporary_access_token.revoke",
		ResourceType: "system_session", ResourceKind: "session",
		Summary: CleanupNamePrefix + "撤销当前临时访问令牌", Mode: "self", VisibilityScope: "targeted", DetailLevel: "summary",
		Method: "DELETE", Path: "/__aisys__/api/auth/temporary-access-token", StatusCode: 200,
	},
	{
		Module: "system_accounts", Action: "update", OperationKey: "auth.update_profile",
		ResourceType: "system_account", ResourceKind: "user",
		Summary: CleanupNamePrefix + "修改显示名称", Mode: "self", VisibilityScope: "targeted", DetailLevel: "full",
		Method: "PATCH", Path: "/__aisys__/api/auth/profile", StatusCode: 200,
		Changes: []observabilityOperationChange{{Field: "displayName", Label: "显示名称", Before: CleanupNamePrefix + "管理员用户", After: CleanupNamePrefix + "管理员用户（已改）"}},
	},
}

// seedOperationLogs 写入操作日志及其受影响对象、可见者与摘要检索词。
func seedOperationLogs(ctx context.Context, e *env, refs observabilityReferences, now time.Time, counts map[string]int) error {
	ready, err := observabilityTableReady(ctx, e, StoreOperationLog, "operation_logs")
	if err != nil {
		return err
	}
	if !ready {
		observabilitySkip(e, "操作日志", StoreOperationLog, "operation_logs")
		return nil
	}
	rows := observabilityBuildOperationRows(e, refs, now)
	return e.tx(ctx, StoreOperationLog, func(tx *sql.Tx) error {
		// 先删有 id 列的目标表与父表；viewers / terms 没有 id 列（主键含父键），
		// 只能按 operation_log_id 前缀清。
		if err := observabilityClearMockRows(ctx, tx, "operation_log_targets", "id"); err != nil {
			return err
		}
		if err := observabilityClearMockRows(ctx, tx, "operation_logs", "id"); err != nil {
			return err
		}
		if err := observabilityExec(ctx, tx, "DELETE FROM operation_log_viewers WHERE operation_log_id LIKE ?", CleanupIDPrefix+"%"); err != nil {
			return err
		}
		if err := observabilityExec(ctx, tx, "DELETE FROM operation_log_summary_search_terms WHERE operation_log_id LIKE ?", CleanupIDPrefix+"%"); err != nil {
			return err
		}
		logRows, targetRows, viewerRows, termRows := 0, 0, 0, 0
		for _, row := range rows {
			if err := observabilityInsert(ctx, tx, "operation_logs", observabilityOperationLogColumns(row)); err != nil {
				return err
			}
			logRows++
			for index, target := range row.Targets {
				columns := map[string]any{
					"id":               row.ID + "_target_" + fmt.Sprintf("%d", index+1),
					"operation_log_id": row.ID,
					"target_type":      target.TargetType,
					"relation":         target.Relation,
					"created_at":       observabilityFormatTime(row.CreatedAt),
				}
				observabilitySetText(columns, map[string]string{
					"target_id":                      target.TargetID,
					"target_name":                    target.TargetName,
					"target_owner_system_account_id": target.TargetOwner,
				})
				if err := observabilityInsert(ctx, tx, "operation_log_targets", columns); err != nil {
					return err
				}
				targetRows++
			}
			for _, viewer := range row.Viewers {
				columns := map[string]any{
					"operation_log_id":  row.ID,
					"system_account_id": viewer.SystemAccountID,
					"visibility_reason": viewer.Reason,
					"detail_level":      viewer.DetailLevel,
					"created_at":        observabilityFormatTime(row.CreatedAt),
				}
				if err := observabilityInsert(ctx, tx, "operation_log_viewers", columns); err != nil {
					return err
				}
				viewerRows++
			}
			for _, term := range observabilitySearchTerms(row.Summary) {
				columns := map[string]any{
					"operation_log_id": row.ID,
					"term":             term,
					"created_at":       observabilityFormatTime(row.CreatedAt),
				}
				if err := observabilityInsert(ctx, tx, "operation_log_summary_search_terms", columns); err != nil {
					return err
				}
				termRows++
			}
		}
		observabilityAdd(counts, "operationLogs", logRows)
		observabilityAdd(counts, "operationLogTargets", targetRows)
		observabilityAdd(counts, "operationLogViewers", viewerRows)
		observabilityAdd(counts, "operationLogSummarySearchTerms", termRows)
		return nil
	})
}

// observabilityOperationTarget 是一条受影响对象引用。
type observabilityOperationTarget struct {
	TargetType  string
	TargetID    string
	TargetName  string
	TargetOwner string
	Relation    string
}

// observabilityOperationViewer 是一条操作日志可见者。
type observabilityOperationViewer struct {
	SystemAccountID string
	Reason          string
	DetailLevel     string
}

// observabilityOperationRow 是一条待写入的操作日志及其子行。
type observabilityOperationRow struct {
	ID                   string
	TraceID              string
	ActorSystemAccountID string
	ActorUsername        string
	ActorDisplayName     string
	ActorRole            string
	ScopeSystemAccountID string
	Mode                 string
	Module               string
	Action               string
	OperationKey         string
	ResourceType         string
	ResourceID           string
	ResourceName         string
	Summary              string
	DetailLevel          string
	VisibilityScope      string
	Changes              []observabilityOperationChange
	Metadata             map[string]any
	Method               string
	Path                 string
	StatusCode           int
	ClientIP             string
	UserAgent            string
	CreatedAt            time.Time
	Targets              []observabilityOperationTarget
	Viewers              []observabilityOperationViewer
}

// observabilityBuildOperationRows 生成近 Days 天的操作日志样本。
func observabilityBuildOperationRows(e *env, refs observabilityReferences, now time.Time) []observabilityOperationRow {
	days := e.options.Days
	rows := make([]observabilityOperationRow, 0, days*3)
	index := 0
	for _, dayIndex := range observabilityDayIndexes(days) {
		dayStart := observabilityDayStart(now).AddDate(0, 0, -(days - 1 - dayIndex))
		perDay := 2 + (dayIndex % 2)
		for rowIndex := 0; rowIndex < perDay; rowIndex++ {
			sample := observabilityOperationSamples[index%len(observabilityOperationSamples)]
			at := observabilitySpread(now, dayStart, rowIndex, perDay)
			actor := observabilityPickUser(refs.Users, index)
			resource := observabilityOperationResourceFor(refs, sample.ResourceKind, index)
			scope := actor.ID
			if sample.ResourceKind == "user" {
				scope = resource.ID
			}
			row := observabilityOperationRow{
				ID:                   observabilityStamp(CleanupIDPrefix+"oplog_", at, index),
				TraceID:              CleanupTracePrefix + "oplog-" + at.Format("20060102") + "-" + fmt.Sprintf("%03d", index+1),
				ActorSystemAccountID: actor.ID,
				ActorUsername:        actor.Username,
				ActorDisplayName:     actor.DisplayName,
				ActorRole:            actor.Role,
				ScopeSystemAccountID: scope,
				Mode:                 sample.Mode,
				Module:               sample.Module,
				Action:               sample.Action,
				OperationKey:         sample.OperationKey,
				ResourceType:         sample.ResourceType,
				ResourceID:           resource.ID,
				ResourceName:         resource.Name,
				Summary:              sample.Summary,
				DetailLevel:          sample.DetailLevel,
				VisibilityScope:      sample.VisibilityScope,
				Changes:              sample.Changes,
				Metadata:             map[string]any{"mockdata": true, "sampleIndex": index % len(observabilityOperationSamples)},
				Method:               sample.Method,
				Path:                 sample.Path,
				StatusCode:           sample.StatusCode,
				ClientIP:             fmt.Sprintf("172.20.%d.%d", index%16, 20+(index%180)),
				UserAgent:            CleanupIDPrefix + "console/1.0",
				CreatedAt:            at,
			}
			relation := "primary"
			if sample.Action == "create" {
				relation = "created"
			} else if sample.Action == "delete" {
				relation = "deleted"
			}
			row.Targets = []observabilityOperationTarget{{
				TargetType:  sample.ResourceType,
				TargetID:    resource.ID,
				TargetName:  resource.Name,
				TargetOwner: scope,
				Relation:    relation,
			}}
			if sample.ExtraTarget == "owner" {
				owner := observabilityPickUser(refs.Users, index+1)
				row.Targets = append(row.Targets, observabilityOperationTarget{
					TargetType:  "system_account",
					TargetID:    owner.ID,
					TargetName:  owner.DisplayName,
					TargetOwner: owner.ID,
					Relation:    "owner",
				})
			}
			row.Viewers = observabilityOperationViewers(row)
			rows = append(rows, row)
			index++
		}
	}
	return rows
}

// observabilityOperationResourceFor 按样本的资源族取引用（缺引用时用兜底样本）。
func observabilityOperationResourceFor(refs observabilityReferences, kind string, index int) observabilityResource {
	switch kind {
	case "group":
		return observabilityPickResource(refs.Groups, index)
	case "api_key":
		return observabilityPickResource(refs.APIKeys, index)
	case "authorization":
		return observabilityPickResource(refs.Authorizations, index)
	case "announcement":
		return observabilityPickResource(refs.Announcements, index)
	case "session":
		return observabilityResource{ID: CleanupTracePrefix + "session-" + fmt.Sprintf("%03d", index+1), Name: CleanupNamePrefix + "临时访问令牌"}
	case "user":
		return observabilityPickResource(observabilityUserResources(refs), index)
	default:
		return observabilityPickResource(refs.Accounts, index)
	}
}

// observabilityUserResources 把用户列表映射成资源形态（操作对象是系统账户时用）。
func observabilityUserResources(refs observabilityReferences) []observabilityResource {
	items := make([]observabilityResource, 0, len(refs.Users))
	for _, user := range refs.Users {
		items = append(items, observabilityResource{ID: user.ID, Name: user.DisplayName})
	}
	return items
}

// observabilityOperationViewers 复刻 F4 normalizeInput 的可见者推导：targeted 时
// 操作者自己可见，资源归属人另有一条可见记录（角色 admin 用管理员代管原因）。
func observabilityOperationViewers(row observabilityOperationRow) []observabilityOperationViewer {
	switch row.VisibilityScope {
	case "all_users", "admin_only":
		return nil
	default:
		viewers := []observabilityOperationViewer{{
			SystemAccountID: row.ActorSystemAccountID,
			Reason:          "actor_self",
			DetailLevel:     row.DetailLevel,
		}}
		if row.ScopeSystemAccountID != "" && row.ScopeSystemAccountID != row.ActorSystemAccountID {
			reason := "resource_owner"
			if row.ActorRole == "admin" {
				reason = "admin_managed_my_resource"
			}
			viewers = append(viewers, observabilityOperationViewer{
				SystemAccountID: row.ScopeSystemAccountID,
				Reason:          reason,
				DetailLevel:     row.DetailLevel,
			})
		}
		return viewers
	}
}

// observabilityOperationLogColumns 映射 operation_logs 列。
func observabilityOperationLogColumns(row observabilityOperationRow) map[string]any {
	changes, err := json.Marshal(observabilityOperationChanges(row.Changes))
	if err != nil {
		changes = []byte("[]")
	}
	metadata, err := json.Marshal(row.Metadata)
	if err != nil {
		metadata = []byte("{}")
	}
	columns := map[string]any{
		"id":                      row.ID,
		"actor_system_account_id": row.ActorSystemAccountID,
		"mode":                    row.Mode,
		"module":                  row.Module,
		"action":                  row.Action,
		"operation_key":           row.OperationKey,
		"resource_type":           row.ResourceType,
		"summary":                 row.Summary,
		"detail_level":            row.DetailLevel,
		"visibility_scope":        row.VisibilityScope,
		"changes_json":            string(changes),
		"metadata_json":           string(metadata),
		"created_at":              observabilityFormatTime(row.CreatedAt),
		"status_code":             row.StatusCode,
	}
	observabilitySetText(columns, map[string]string{
		"trace_id":                          row.TraceID,
		"actor_username":                    row.ActorUsername,
		"actor_display_name":                row.ActorDisplayName,
		"actor_role":                        row.ActorRole,
		"operation_scope_system_account_id": row.ScopeSystemAccountID,
		"resource_id":                       row.ResourceID,
		"resource_name":                     row.ResourceName,
		"method":                            row.Method,
		"path":                              row.Path,
		"client_ip":                         row.ClientIP,
		"user_agent":                        row.UserAgent,
	})
	return columns
}

// observabilityOperationChanges 把样本变更映射成 F4 的 Change 结构（含标签）。
func observabilityOperationChanges(changes []observabilityOperationChange) []map[string]any {
	items := make([]map[string]any, 0, len(changes))
	for _, change := range changes {
		items = append(items, map[string]any{
			"field":  change.Field,
			"label":  change.Label,
			"before": change.Before,
			"after":  change.After,
		})
	}
	return items
}

// observabilitySearchTerms 生成摘要检索词。
//
// 与 F4 searchTerms 同一语义（小写、非字母数字折叠成单空格、展开子串），但只展开
// 到 observabilitySearchTermMaxRunes 并限制总条数：造数只需要「页面按摘要里的词
// 能命中」，不需要复刻 F4 最多 1500 条的完整枚举（那会让检索词表膨胀到上万行）。
func observabilitySearchTerms(summary string) []string {
	normalized := observabilityNormalizeSearchText(summary)
	if normalized == "" {
		return nil
	}
	compact := strings.ReplaceAll(normalized, " ", "")
	set := map[string]bool{}
	add := func(term string) {
		runes := []rune(term)
		if len(runes) == 0 || len(runes) > 128 {
			return
		}
		set[term] = true
	}
	add(normalized)
	add(compact)
	for _, part := range strings.Fields(normalized) {
		add(part)
	}
	for _, candidate := range append([]string{normalized, compact}, strings.Fields(normalized)...) {
		runes := []rune(candidate)
		for length := 1; length <= observabilitySearchTermMaxRunes && length <= len(runes); length++ {
			for start := 0; start+length <= len(runes); start++ {
				add(string(runes[start : start+length]))
				if len(set) >= observabilitySearchTermLimit {
					break
				}
			}
			if len(set) >= observabilitySearchTermLimit {
				break
			}
		}
		if len(set) >= observabilitySearchTermLimit {
			break
		}
	}
	terms := make([]string, 0, len(set))
	for term := range set {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	return terms
}

// observabilityNormalizeSearchText 与 F4 normalizeSearchText 对齐（不含 NFKC：
// 造数样本全部是 ASCII 与常用汉字，NFKC 折叠不会改变结果）。
func observabilityNormalizeSearchText(value string) string {
	var builder strings.Builder
	needsSpace := false
	for _, char := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case isObservabilitySearchRune(char):
			builder.WriteRune(char)
			needsSpace = false
		case builder.Len() > 0 && !needsSpace:
			builder.WriteByte(' ')
			needsSpace = true
		}
	}
	return strings.TrimSpace(builder.String())
}

// isObservabilitySearchRune 判断字符是否保留在检索词里（字母或数字）。
func isObservabilitySearchRune(char rune) bool {
	switch {
	case char >= '0' && char <= '9':
		return true
	case char >= 'a' && char <= 'z':
		return true
	case char >= 0x4e00 && char <= 0x9fff: // CJK 统一表意文字
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// 运行日志（F1 / jobs runtime-log.sqlite3 + JUHE_AI_LOG_DIR 文件）
// ---------------------------------------------------------------------------

// observabilityRuntimeLine 是一条运行日志文件行（同时决定索引行内容）。
type observabilityRuntimeLine struct {
	// Role 是日志文件角色（server:实例 / stats-worker:实例）。
	Role string
	// FileName 是日志文件名。
	FileName string
	At       time.Time
	Level    string
	TraceID  string
	Event    string
	Message  string
	Fields   map[string]any
	// ErrorMessage 落进 F1 的 err.message（slog 用 error 值记录时就是这个形态）。
	ErrorMessage string
}

// observabilityRuntimeServerEvents 是 server 角色的请求 summary 事件序列：
// 事件名与字段取自 gateway/internal/kernel 的 http_request_completed / closed。
var observabilityRuntimeServerEvents = []struct {
	Level    string
	Event    string
	Message  string
	Method   string
	Path     string
	Status   int
	Failure  string
	Duration int
}{
	{Level: "info", Event: "http_request_completed", Message: "HTTP 请求已结束", Method: "POST", Path: "/v1/chat/completions", Status: 200, Duration: 812},
	{Level: "info", Event: "http_request_completed", Message: "HTTP 请求已结束", Method: "POST", Path: "/v1/responses", Status: 200, Duration: 1543},
	{Level: "warn", Event: "http_request_completed", Message: "HTTP 请求已结束", Method: "POST", Path: "/v1/chat/completions", Status: 429, Failure: "gateway", Duration: 96},
	{Level: "error", Event: "http_request_completed", Message: "HTTP 请求已结束", Method: "GET", Path: "/v1/models", Status: 500, Failure: "gateway", Duration: 204},
	{Level: "warn", Event: "gateway_upstream_response_failed", Message: "上游返回非成功状态", Method: "POST", Path: "/v1/chat/completions", Status: 502, Failure: "upstream", Duration: 1877},
	{Level: "error", Event: "http_request_closed", Message: "HTTP 请求已断开", Method: "POST", Path: "/v1/images/generations", Status: 499, Failure: "downstream", Duration: 4201},
}

// observabilityRuntimeWorkerEvents 是 stats-worker 角色的定时任务事件序列：
// 事件名与字段取自 jobs/internal/retention 与 jobs/internal/usagespooldrain。
var observabilityRuntimeWorkerEvents = []struct {
	Level   string
	Event   string
	Message string
	Fields  map[string]any
	Error   string
}{
	{
		Level: "info", Event: "data_retention_cleanup_completed", Message: "数据保留清理完成",
		Fields: map[string]any{"batchSize": 500, "maxBatches": 20, "workerRole": "stats-worker", "usageStats": 1200, "usageRecords": 8400},
	},
	{
		Level: "info", Event: "record_maintenance_usage_records_cleanup_completed", Message: "使用记录后台清理完成",
		Fields: map[string]any{"jobId": CleanupIDPrefix + "record_maintenance_job_usage", "deletedRows": 320, "batches": 2, "hasMore": false},
	},
	{
		Level: "warn", Event: "public_api_log_queue_overflow", Message: "公开接口日志队列已满，丢弃日志记录",
		Fields: map[string]any{"itemBytes": 1408, "droppedPublicApiLogCount": 100, "path": "/__aipublic__/api-key/list"},
	},
	{
		Level: "info", Event: "background_api_key_record_cleanup_retry_completed", Message: "API Key 关联数据清理重试完成",
		Fields: map[string]any{"jobId": CleanupIDPrefix + "record_maintenance_job_api_key", "attemptCount": 1, "deletedRows": 14},
	},
	{
		Level: "error", Event: "public_api_log_batch_write_failed", Message: "公开接口日志批量写入失败，已保留批次等待重试",
		Fields: map[string]any{"batchSize": 64, "batchBytes": 98304, "pendingCount": 64, "path": "/__aipublic__/account/list", "statusCode": 200},
		Error:  CleanupNamePrefix + "模拟批次写入遇到 SQLite 写锁",
	},
	{
		Level: "error", Event: "usage_record_spool_flush_failed", Message: "usage spool 投递失败，已保留文件等待重试",
		Fields: map[string]any{"usageRecordId": CleanupIDPrefix + "usage_spool_record", "attemptCount": 3},
		Error:  CleanupNamePrefix + "模拟 spool 投递失败",
	},
}

// observabilityBuildRuntimeLines 生成近 Days 天的运行日志行（两个文件各一份）。
func observabilityBuildRuntimeLines(e *env, now time.Time) []observabilityRuntimeLine {
	days := e.options.Days
	lines := make([]observabilityRuntimeLine, 0, days*8)
	index := 0
	for _, dayIndex := range observabilityDayIndexes(days) {
		dayStart := observabilityDayStart(now).AddDate(0, 0, -(days - 1 - dayIndex))
		for rowIndex := 0; rowIndex < 4; rowIndex++ {
			sample := observabilityRuntimeServerEvents[(dayIndex*4+rowIndex)%len(observabilityRuntimeServerEvents)]
			at := observabilitySpread(now, dayStart, rowIndex, 5)
			traceID := CleanupTracePrefix + "runtime-" + at.Format("20060102") + "-" + fmt.Sprintf("%03d", index+1)
			fields := map[string]any{
				"requestId":         CleanupIDPrefix + "request_" + fmt.Sprintf("%03d", index+1),
				"method":            sample.Method,
				"path":              sample.Path,
				"originalUrl":       sample.Path,
				"clientIp":          fmt.Sprintf("172.20.%d.%d", index%16, 20+(index%180)),
				"durationMs":        sample.Duration,
				"responseCommitted": true,
				"statusCode":        sample.Status,
				"failureScope":      sample.Failure,
			}
			lines = append(lines, observabilityRuntimeLine{
				Role: "server:mockdata-server", FileName: observabilityServerLogFile,
				At: at, Level: sample.Level, TraceID: traceID, Event: sample.Event,
				Message: sample.Message, Fields: fields,
			})
			index++
		}
		for rowIndex := 0; rowIndex < 4; rowIndex++ {
			sample := observabilityRuntimeWorkerEvents[(dayIndex*4+rowIndex)%len(observabilityRuntimeWorkerEvents)]
			at := observabilitySpread(now, dayStart, rowIndex+1, 6)
			fields := map[string]any{}
			for key, value := range sample.Fields {
				fields[key] = value
			}
			lines = append(lines, observabilityRuntimeLine{
				Role: "stats-worker:mockdata-dev", FileName: observabilityWorkerLogFile,
				At: at, Level: sample.Level, Event: sample.Event, Message: sample.Message,
				Fields: fields, ErrorMessage: sample.Error,
			})
		}
	}
	sort.SliceStable(lines, func(left, right int) bool {
		if lines[left].FileName != lines[right].FileName {
			return lines[left].FileName < lines[right].FileName
		}
		return lines[left].At.Before(lines[right].At)
	})
	return lines
}

// observabilityRuntimeLineJSON 把一行渲染成 slog JSON 形态（单行、稳定键序）。
//
// 为什么手工拼 JSON 而不直接 marshal map：slog 的行是「固定键 + 事件字段」，
// 固定键在前才能与真实日志在字符层面同形（grep 是纯文本扫描，键序会影响可读性
// 与人工比对）；字段值仍然走 json.Marshal 保证转义正确。
func observabilityRuntimeLineJSON(line observabilityRuntimeLine) (string, error) {
	var builder strings.Builder
	builder.WriteString(`{"time":`)
	writeJSONValue(&builder, observabilityFormatTime(line.At))
	builder.WriteString(`,"level":`)
	writeJSONValue(&builder, strings.ToUpper(line.Level))
	builder.WriteString(`,"msg":`)
	writeJSONValue(&builder, line.Message)
	if line.Event != "" {
		builder.WriteString(`,"event":`)
		writeJSONValue(&builder, line.Event)
	}
	if line.TraceID != "" {
		builder.WriteString(`,"traceId":`)
		writeJSONValue(&builder, line.TraceID)
	}
	keys := make([]string, 0, len(line.Fields))
	for key := range line.Fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		builder.WriteString(",")
		writeJSONValue(&builder, key)
		builder.WriteString(":")
		writeJSONValue(&builder, line.Fields[key])
	}
	if line.ErrorMessage != "" {
		builder.WriteString(`,"err":{"message":`)
		writeJSONValue(&builder, line.ErrorMessage)
		builder.WriteString("}")
	}
	builder.WriteString("}")
	return builder.String(), nil
}

// writeJSONValue 把值编码成 JSON 片段写入 builder（编码失败写成 null）。
func writeJSONValue(builder *strings.Builder, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		builder.WriteString("null")
		return
	}
	builder.Write(encoded)
}

// seedRuntimeLogs 写运行日志文件、索引行、分面与游标。
func seedRuntimeLogs(ctx context.Context, e *env, refs observabilityReferences, now time.Time, counts map[string]int) error {
	_ = refs
	lines := observabilityBuildRuntimeLines(e, now)
	files, err := observabilityWriteRuntimeLogFiles(e, lines)
	if err != nil {
		return err
	}
	// 文件数不进 Counts：Counts 的语义是「逻辑对象 → 行数」，日志文件不是库行；
	// 而且空数据根上 Counts 必须为空（域未接线），否则「空根=零写入」的既有契约
	// 会被这一项破坏。写出的文件数与行数记在日志里。
	e.logger.Info("mockdata 运行日志文件已写出",
		"files", len(files), "lines", len(lines), "logDir", e.options.Paths.LogDir)
	ready, err := observabilityTableReady(ctx, e, StoreRuntimeLog, "runtime_logs")
	if err != nil {
		return err
	}
	if !ready {
		// 库还没 bootstrap：文件仍然写出（grep 模式读文件），索引与分面留空并报告。
		observabilitySkip(e, "运行日志索引", StoreRuntimeLog, "runtime_logs")
		return nil
	}
	return e.tx(ctx, StoreRuntimeLog, func(tx *sql.Tx) error {
		if err := observabilityClearMockRows(ctx, tx, "runtime_logs", "id"); err != nil {
			return err
		}
		for _, line := range lines {
			raw, err := observabilityRuntimeLineJSON(line)
			if err != nil {
				return err
			}
			columns := map[string]any{
				"id":         observabilityRuntimeLogID(line, raw),
				"log_file":   filepath.Join(e.options.Paths.LogDir, line.FileName),
				"time":       observabilityFormatTime(line.At),
				"level":      strings.ToLower(line.Level),
				"raw_json":   raw,
				"created_at": observabilityFormatTime(line.At),
			}
			observabilitySetText(columns, map[string]string{
				"trace_id":      line.TraceID,
				"event":         line.Event,
				"message":       line.Message,
				"error_message": line.ErrorMessage,
			})
			if err := observabilityInsert(ctx, tx, "runtime_logs", columns); err != nil {
				return err
			}
		}
		if err := observabilityRebuildRuntimeFacets(ctx, tx, now); err != nil {
			return err
		}
		if err := observabilityUpsertRuntimeCursors(ctx, tx, e, files, lines, now); err != nil {
			return err
		}
		observabilityAdd(counts, "runtimeLogs", len(lines))
		return nil
	})
}

// observabilityRuntimeLogID 生成索引行 id：带清理标识 + 内容摘要。
//
// 为什么不复刻 F1 的 rtlog_<identity:offset> 算法：文件身份（inode / file index）
// 只有 F1 的进程能拿到，而维护项目禁止跨项目 import。带清理标识的 id 保证清理
// 扫描（按 id 前缀）能收敛，重复执行不会叠加。
func observabilityRuntimeLogID(line observabilityRuntimeLine, raw string) string {
	sum := sha256.Sum256([]byte(line.FileName + "\x00" + raw))
	return CleanupIDPrefix + "rtlog_" + hex.EncodeToString(sum[:])[:32]
}

// observabilityWriteRuntimeLogFiles 把日志行写成 JSONL 文件并返回每个文件的元数据。
//
// 文件名固定、内容由 Options 决定：重复执行是重写而不是追加，因此不会叠加旧样本，
// 也不会让 F1 的游标（文件末尾）指向半行数据。
func observabilityWriteRuntimeLogFiles(e *env, lines []observabilityRuntimeLine) ([]observabilityRuntimeLogFile, error) {
	grouped := map[string][]observabilityRuntimeLine{}
	order := make([]string, 0, 2)
	for _, line := range lines {
		if _, ok := grouped[line.FileName]; !ok {
			order = append(order, line.FileName)
		}
		grouped[line.FileName] = append(grouped[line.FileName], line)
	}
	sort.Strings(order)
	if err := os.MkdirAll(e.options.Paths.LogDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建运行日志目录 %s: %w", e.options.Paths.LogDir, err)
	}
	files := make([]observabilityRuntimeLogFile, 0, len(order))
	for _, name := range order {
		var builder strings.Builder
		for _, line := range grouped[name] {
			raw, err := observabilityRuntimeLineJSON(line)
			if err != nil {
				return nil, err
			}
			builder.WriteString(raw)
			builder.WriteString("\n")
		}
		path := filepath.Join(e.options.Paths.LogDir, name)
		if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
			return nil, fmt.Errorf("写入运行日志文件 %s: %w", path, err)
		}
		files = append(files, observabilityRuntimeLogFile{
			Name:  name,
			Path:  path,
			Lines: len(grouped[name]),
			Bytes: int64(builder.Len()),
		})
	}
	return files, nil
}

// observabilityRuntimeLogFile 是一个已写出的日志文件。
type observabilityRuntimeLogFile struct {
	Name  string
	Path  string
	Lines int
	Bytes int64
}

// observabilityRebuildRuntimeFacets 从 runtime_logs 重算分面表。
//
// 为什么重算而不是增量累加：F1 的分面是「索引行数的聚合视图」（total_count 与
// level / event 计数始终保持与表一致）。造数每次会先删掉自己的行再重写，增量累加
// 会在重复执行时重复计数；重算既幂等，也不会破坏 F1 已记录的其它行（计数仍与表
// 一致）。earliest / latest 同样从表里取，避免造数把分面时间窗写歪。
func observabilityRebuildRuntimeFacets(ctx context.Context, tx *sql.Tx, now time.Time) error {
	var total int
	var earliest, latest sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*), MIN(time), MAX(time) FROM runtime_logs").Scan(&total, &earliest, &latest); err != nil {
		return fmt.Errorf("统计 runtime_logs: %w", err)
	}
	if total == 0 {
		if err := observabilityExec(ctx, tx, "DELETE FROM runtime_log_facet_summary WHERE bucket_key = ?", observabilityRuntimeFacetBucketKey); err != nil {
			return err
		}
	} else {
		columns := map[string]any{
			"bucket_key":    observabilityRuntimeFacetBucketKey,
			"total_count":   total,
			"earliest_time": optionalTextValue(earliest),
			"latest_time":   optionalTextValue(latest),
			"updated_at":    observabilityFormatTime(now),
		}
		if err := observabilityInsertUpsert(ctx, tx, "runtime_log_facet_summary",
			columns, "bucket_key",
			[]string{"total_count", "earliest_time", "latest_time", "updated_at"}); err != nil {
			return err
		}
	}
	if err := observabilityRebuildRuntimeLevelFacets(ctx, tx, now); err != nil {
		return err
	}
	return observabilityRebuildRuntimeEventFacets(ctx, tx, now)
}

// observabilityRebuildRuntimeLevelFacets 重算 level 分面（并按当前级别集合清理）。
func observabilityRebuildRuntimeLevelFacets(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, "SELECT level, COUNT(*) FROM runtime_logs GROUP BY level")
	if err != nil {
		return fmt.Errorf("统计 runtime_logs level: %w", err)
	}
	defer rows.Close()
	levels := make([]string, 0, 6)
	for rows.Next() {
		var level string
		var count int
		if err := rows.Scan(&level, &count); err != nil {
			return fmt.Errorf("统计 runtime_logs level: %w", err)
		}
		levels = append(levels, level)
		columns := map[string]any{
			"bucket_key": observabilityRuntimeFacetBucketKey,
			"level":      level,
			"count":      count,
			"updated_at": observabilityFormatTime(now),
		}
		if err := observabilityInsertUpsert(ctx, tx, "runtime_log_level_facets", columns,
			"bucket_key, level", []string{"count", "updated_at"}); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("统计 runtime_logs level: %w", err)
	}
	return observabilityPruneFacetRows(ctx, tx, "runtime_log_level_facets", "level", levels)
}

// observabilityRebuildRuntimeEventFacets 重算 event 分面（含 latest_time）。
func observabilityRebuildRuntimeEventFacets(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(event, ''), COUNT(*), MAX(time) FROM runtime_logs
		WHERE event IS NOT NULL AND event <> '' GROUP BY event`)
	if err != nil {
		return fmt.Errorf("统计 runtime_logs event: %w", err)
	}
	defer rows.Close()
	events := make([]string, 0, 16)
	for rows.Next() {
		var (
			event  string
			count  int
			latest sql.NullString
		)
		if err := rows.Scan(&event, &count, &latest); err != nil {
			return fmt.Errorf("统计 runtime_logs event: %w", err)
		}
		events = append(events, event)
		columns := map[string]any{
			"bucket_key":  observabilityRuntimeFacetBucketKey,
			"event":       event,
			"count":       count,
			"latest_time": optionalTextValue(latest),
			"updated_at":  observabilityFormatTime(now),
		}
		if err := observabilityInsertUpsert(ctx, tx, "runtime_log_event_facets", columns,
			"bucket_key, event", []string{"count", "latest_time", "updated_at"}); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("统计 runtime_logs event: %w", err)
	}
	return observabilityPruneFacetRows(ctx, tx, "runtime_log_event_facets", "event", events)
}

// observabilityPruneFacetRows 删除当前集合之外的分面行（分面是表的聚合视图）。
func observabilityPruneFacetRows(ctx context.Context, tx *sql.Tx, table, column string, values []string) error {
	if len(values) == 0 {
		return observabilityExec(ctx, tx, "DELETE FROM "+table+" WHERE bucket_key = ?", observabilityRuntimeFacetBucketKey)
	}
	placeholders := make([]string, len(values))
	args := make([]any, 0, len(values)+1)
	args = append(args, observabilityRuntimeFacetBucketKey)
	for index, value := range values {
		placeholders[index] = "?"
		args = append(args, value)
	}
	query := "DELETE FROM " + table + " WHERE bucket_key = ? AND " + column + " NOT IN (" + strings.Join(placeholders, ", ") + ")"
	return observabilityExec(ctx, tx, query, args...)
}

// observabilityInsertUpsert 在事务里做 upsert（ON CONFLICT 目标 + 更新列）。
func observabilityInsertUpsert(ctx context.Context, tx *sql.Tx, table string, columns map[string]any, conflictTarget string, updateColumns []string) error {
	names := make([]string, 0, len(columns))
	for name := range columns {
		names = append(names, name)
	}
	sort.Strings(names)
	placeholders := make([]string, len(names))
	values := make([]any, len(names))
	for index, name := range names {
		placeholders[index] = "?"
		values[index] = columns[name]
	}
	assignments := make([]string, 0, len(updateColumns))
	for _, name := range updateColumns {
		assignments = append(assignments, name+" = excluded."+name)
	}
	query := "INSERT INTO " + table + " (" + strings.Join(names, ", ") + ") VALUES (" +
		strings.Join(placeholders, ", ") + ") ON CONFLICT(" + conflictTarget + ") DO UPDATE SET " +
		strings.Join(assignments, ", ")
	if _, err := tx.ExecContext(ctx, query, values...); err != nil {
		return fmt.Errorf("写入 %s: %w", table, err)
	}
	return nil
}

// optionalTextValue 把可空文本转成 any（NULL 保持 NULL）。
func optionalTextValue(value sql.NullString) any {
	if !value.Valid || value.String == "" {
		return nil
	}
	return value.String
}

// observabilityUpsertRuntimeCursors 给每个 mock 日志文件写一条「已读到文件末尾」
// 的游标行。
//
// 为什么必须写：F1 首次看到一个已存在的当前日志文件时会从文件末尾追新（cursor
// offset = 文件大小），历史行永远不会补索引；而历史行正是本域直接写进
// runtime_logs 的。游标把「文件已被读完」这个事实显式落库，F1 后续只追新行，索引
// 与文件才不会互相矛盾。file_identity 故意留空：F1 看到身份不匹配会按「当前文件」
// 规则把 offset 重新定到文件末尾，即本域想要的结果；同时删掉本域日志文件上遗留的
// 真实身份游标（那是上一次 F1 替换出来的），避免文件变小时 F1 回退到 offset 0
// 重复索引同一批历史行。
func observabilityUpsertRuntimeCursors(ctx context.Context, tx *sql.Tx, e *env, files []observabilityRuntimeLogFile, lines []observabilityRuntimeLine, now time.Time) error {
	perFile := map[string]int{}
	for _, line := range lines {
		perFile[line.FileName]++
	}
	for _, file := range files {
		if err := observabilityExec(ctx, tx,
			"DELETE FROM runtime_log_file_cursors WHERE log_file = ? AND COALESCE(file_identity, '') <> ''", file.Path); err != nil {
			return err
		}
		info, err := os.Stat(file.Path)
		if err != nil {
			return fmt.Errorf("读取运行日志文件信息 %s: %w", file.Path, err)
		}
		columns := map[string]any{
			"log_file":              file.Path,
			"file_identity":         "",
			"cursor_offset":         info.Size(),
			"line_number":           perFile[file.Name],
			"file_size":             info.Size(),
			"truncation_generation": 0,
			"file_mtime_ms":         info.ModTime().UnixMilli(),
			"last_read_at":          observabilityFormatTime(now),
			"created_at":            observabilityFormatTime(now),
			"updated_at":            observabilityFormatTime(now),
		}
		if err := observabilityInsertUpsert(ctx, tx, "runtime_log_file_cursors", columns,
			"log_file", []string{"file_identity", "cursor_offset", "line_number", "file_size",
				"truncation_generation", "file_mtime_ms", "last_read_at", "updated_at"}); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 后台任务运行（jobs task-runs 库）
// ---------------------------------------------------------------------------

// observabilityTaskRunSample 是后台任务运行样本：任务名与 job_type / worker_role
// 取自 jobs/internal/jobregistry 的真实注册项。
type observabilityTaskRunSample struct {
	JobName    string
	JobType    string
	WorkerRole string
	Status     string
	Result     map[string]any
	Error      string
	ExitCode   int
}

// observabilityTaskRunSamples 覆盖 completed / failed / running / queued / skipped
// 与 stats-worker / ops-worker / ingest-worker 三种角色。
var observabilityTaskRunSamples = []observabilityTaskRunSample{
	{
		JobName: "system-metrics-sample", JobType: "sample", WorkerRole: "stats-worker", Status: "completed",
		Result: map[string]any{"sampled": 1, "samples": 4}, ExitCode: 0,
	},
	{
		JobName: "usage-stats-aggregation", JobType: "stats", WorkerRole: "stats-worker", Status: "completed",
		Result: map[string]any{"aggregated": 12, "windows": 3}, ExitCode: 0,
	},
	{
		JobName: "client-ip-stats-aggregation", JobType: "stats", WorkerRole: "stats-worker", Status: "failed",
		Error: CleanupNamePrefix + "模拟统计聚合遇到 SQLite 写锁", ExitCode: 1,
	},
	{
		JobName: "account-balance-refresh", JobType: "probe", WorkerRole: "ops-worker", Status: "running",
	},
	{
		JobName: "data-retention-cleanup", JobType: "maintenance", WorkerRole: "ingest-worker", Status: "queued",
	},
	{
		JobName: "chat-retention-cleanup", JobType: "maintenance", WorkerRole: "ops-worker", Status: "skipped",
		Result: map[string]any{"skipped": true, "reason": "lease_busy"}, ExitCode: 0,
	},
	{
		JobName: "account-quality-refresh", JobType: "stats", WorkerRole: "stats-worker", Status: "completed",
		Result: map[string]any{"accounts": 26}, ExitCode: 0,
	},
	{
		JobName: "resource-authorization-expiry-sweep", JobType: "maintenance", WorkerRole: "ops-worker", Status: "failed",
		Error: CleanupNamePrefix + "模拟授权过期扫描失败", ExitCode: 1,
	},
}

// seedBackgroundTaskRuns 写 task-runs 库的 background_task_runs。
//
// 注意：系统指标页的「后台任务」区块读的是 stats 库里的同名表
// （gateway/internal/statreads/systemmetrics.go 用 statsTable("background_task_runs")），
// 按 autofill.go 的登记该表归 seedStatsRaw；本域只写 jobs 自己的 task-runs 库，
// 不重复占位另一个域的写入面（差异见交付报告）。
func seedBackgroundTaskRuns(ctx context.Context, e *env, refs observabilityReferences, now time.Time, counts map[string]int) error {
	_ = refs
	ready, err := observabilityTableReady(ctx, e, StoreTaskRuns, "background_task_runs")
	if err != nil {
		return err
	}
	if !ready {
		observabilitySkip(e, "后台任务运行", StoreTaskRuns, "background_task_runs")
		return nil
	}
	rows := observabilityBuildTaskRunRows(e, now)
	return e.tx(ctx, StoreTaskRuns, func(tx *sql.Tx) error {
		if err := observabilityClearMockRows(ctx, tx, "background_task_runs", "run_id"); err != nil {
			return err
		}
		for _, columns := range rows {
			if err := observabilityInsert(ctx, tx, "background_task_runs", columns); err != nil {
				return err
			}
		}
		observabilityAdd(counts, "backgroundTaskRuns", len(rows))
		return nil
	})
}

// observabilityBuildTaskRunRows 生成近 Days 天的后台任务运行行。
func observabilityBuildTaskRunRows(e *env, now time.Time) []map[string]any {
	days := e.options.Days
	rows := make([]map[string]any, 0, days*2)
	index := 0
	for _, dayIndex := range observabilityDayIndexes(days) {
		dayStart := observabilityDayStart(now).AddDate(0, 0, -(days - 1 - dayIndex))
		perDay := 1 + dayIndex%2
		for rowIndex := 0; rowIndex < perDay; rowIndex++ {
			sample := observabilityTaskRunSamples[index%len(observabilityTaskRunSamples)]
			at := observabilitySpread(now, dayStart, rowIndex, perDay)
			runID := observabilityStamp(CleanupIDPrefix+"taskrun_", at, index)
			duration := 240 + (index*197)%5400
			params, err := json.Marshal(map[string]any{
				"mockdata": true, "jobName": sample.JobName, "trigger": "scheduled",
			})
			if err != nil {
				params = []byte("{}")
			}
			result, err := json.Marshal(sample.Result)
			if err != nil {
				result = []byte("{}")
			}
			columns := map[string]any{
				"run_id":       runID,
				"job_name":     sample.JobName,
				"job_type":     sample.JobType,
				"worker_role":  sample.WorkerRole,
				"status":       sample.Status,
				"lease_key":    CleanupIDPrefix + "taskrun_lease_" + sample.JobName,
				"owner_id":     CleanupIDPrefix + "worker_" + sample.WorkerRole,
				"params_json":  string(params),
				"result_json":  string(result),
				"submitted_at": observabilityFormatTime(at),
				"created_at":   observabilityFormatTime(at),
				"updated_at":   observabilityFormatTime(at),
			}
			switch sample.Status {
			case "queued":
				// 排队中：只有提交时间，没有开始 / 结束 / 心跳。
			case "running":
				heartbeat := now
				if heartbeat.Before(at) {
					heartbeat = at
				}
				columns["started_at"] = observabilityFormatTime(at)
				columns["heartbeat_at"] = observabilityFormatTime(heartbeat)
			default:
				finished := at.Add(time.Duration(duration) * time.Millisecond)
				if finished.After(now) {
					finished = now
				}
				columns["started_at"] = observabilityFormatTime(at)
				columns["finished_at"] = observabilityFormatTime(finished)
				columns["duration_ms"] = duration
				columns["exit_code"] = sample.ExitCode
			}
			observabilitySetText(columns, map[string]string{"error_message": sample.Error})
			rows = append(rows, columns)
			index++
		}
	}
	return rows
}

// ---------------------------------------------------------------------------
// 公开接口日志（dataset 库 / gateway publicapilogs）
// ---------------------------------------------------------------------------

// observabilityPublicEndpoint 是一条 /__aipublic__ 端点样本。
type observabilityPublicEndpoint struct {
	Method string
	Path   string
	Query  string
	Write  bool
}

// observabilityPublicEndpoints 与 gateway/internal/aipublic 的 16 条路由同名。
var observabilityPublicEndpoints = []observabilityPublicEndpoint{
	{Method: "GET", Path: "/__aipublic__/api-key/list", Query: "targetUsername=" + CleanupIDPrefix + "admin"},
	{Method: "GET", Path: "/__aipublic__/route-strategy/list", Query: "targetUsername=" + CleanupIDPrefix + "admin&mode=all"},
	{Method: "GET", Path: "/__aipublic__/group/list", Query: "targetUsername=" + CleanupIDPrefix + "admin&providerCode=gpt"},
	{Method: "GET", Path: "/__aipublic__/account/list", Query: "targetUsername=" + CleanupIDPrefix + "admin&providerCode=gpt"},
	{Method: "POST", Path: "/__aipublic__/api-key/add", Write: true},
	{Method: "POST", Path: "/__aipublic__/api-key/update", Write: true},
	{Method: "POST", Path: "/__aipublic__/api-key/del", Write: true},
	{Method: "POST", Path: "/__aipublic__/route-strategy/add", Write: true},
	{Method: "POST", Path: "/__aipublic__/route-strategy/update", Write: true},
	{Method: "POST", Path: "/__aipublic__/route-strategy/del", Write: true},
	{Method: "POST", Path: "/__aipublic__/group/add", Write: true},
	{Method: "POST", Path: "/__aipublic__/group/update", Write: true},
	{Method: "POST", Path: "/__aipublic__/group/del", Write: true},
	{Method: "POST", Path: "/__aipublic__/account/add", Write: true},
	{Method: "POST", Path: "/__aipublic__/account/update", Write: true},
	{Method: "POST", Path: "/__aipublic__/account/del", Write: true},
}

// observabilityPublicStatus 复刻 Node mockdata 的状态分布：401 / 403 / 429 /
// 400 / 500 按固定取模命中，其余成功（POST 写入额外命中 201）。
func observabilityPublicStatus(index int, method string) int {
	switch {
	case index%29 == 0:
		return 401
	case index%23 == 0:
		return 403
	case index%19 == 0:
		return 429
	case index%17 == 0:
		return 400
	case index%13 == 0:
		return 500
	case method == "POST" && index%4 == 0:
		return 201
	default:
		return 200
	}
}

// observabilityPublicErrorCode / observabilityPublicErrorMessage 使用 gateway
// aipublic 真实错误码（401 缺 token、403 缺 scope、429 限流）。400 / 500 由 kernel
// 错误信封返回，只有 message 没有 code，因此错误码留空。
func observabilityPublicErrorCode(status int) string {
	switch status {
	case 401:
		return "external_source_token_missing"
	case 403:
		return "external_source_scope_forbidden"
	case 429:
		return "external_source_rate_limited"
	default:
		return ""
	}
}

// observabilityPublicErrorMessage 给出对应状态码的中文消息。
func observabilityPublicErrorMessage(status int) string {
	switch status {
	case 401:
		return CleanupNamePrefix + "模拟来源系统缺少 token"
	case 403:
		return CleanupNamePrefix + "模拟来源系统缺少接口权限"
	case 429:
		return CleanupNamePrefix + "模拟公开接口触发限流"
	case 400:
		return CleanupNamePrefix + "模拟公开接口参数无效"
	case 500:
		return CleanupNamePrefix + "模拟公开接口内部错误"
	default:
		return ""
	}
}

// seedPublicAPILogs 写 dataset 库的 public_api_logs。
//
// 来源与 Token 全部引用 business 域已造的 real 行；一条都查不到时跳过并报告
// （不伪造不存在的来源系统）。
func seedPublicAPILogs(ctx context.Context, e *env, refs observabilityReferences, now time.Time, counts map[string]int) error {
	ready, err := observabilityTableReady(ctx, e, StoreDataset, "public_api_logs")
	if err != nil {
		return err
	}
	if !ready {
		observabilitySkip(e, "公开接口日志", StoreDataset, "public_api_logs")
		return nil
	}
	sources := observabilityPublicSources(refs)
	if len(sources) == 0 {
		e.logger.Warn("mockdata 公开接口日志跳过：business 库没有可用的外部来源系统或 Token",
			"store", StoreDataset, "table", "public_api_logs")
		return nil
	}
	rows := observabilityBuildPublicAPILogRows(e, sources, now)
	return e.tx(ctx, StoreDataset, func(tx *sql.Tx) error {
		if err := observabilityClearMockRows(ctx, tx, "public_api_logs", "id"); err != nil {
			return err
		}
		for _, columns := range rows {
			if err := observabilityInsert(ctx, tx, "public_api_logs", columns); err != nil {
				return err
			}
		}
		observabilityAdd(counts, "publicApiLogs", len(rows))
		return nil
	})
}

// observabilityPublicSource 是一条可用于公开接口日志的来源 + Token 组合。
// Kind 取 primary / readonly / test，决定样本轮转时使用哪一类来源。
type observabilityPublicSource struct {
	SourceID   string
	SourceName string
	TokenID    string
	TokenName  string
	Prefix     string
	IsTest     bool
	ReadOnly   bool
	Kind       string
}

// observabilityPublicSources 挑出可用的来源 + Token 组合：一条正式来源、一条只读
// 来源、以及内置测试来源（各自都要有可用 Token）。
func observabilityPublicSources(refs observabilityReferences) []observabilityPublicSource {
	var out []observabilityPublicSource
	appendSource := func(kind string, source observabilitySource) {
		if source.ID == "" || source.TokenID == "" {
			return
		}
		out = append(out, observabilityPublicSource{
			SourceID:   source.ID,
			SourceName: source.Name,
			TokenID:    source.TokenID,
			TokenName:  source.Token,
			Prefix:     source.Prefix,
			IsTest:     source.BuiltIn,
			ReadOnly:   source.ReadOnly,
			Kind:       kind,
		})
	}
	appendSource("primary", observabilitySourceByKind(refs.Sources, "primary"))
	appendSource("readonly", observabilitySourceByKind(refs.Sources, "readonly"))
	appendSource("test", observabilitySourceByKind(refs.Sources, "test"))
	return out
}

// observabilityPickPublicSource 轮转来源类型：每 5 条一条内置测试 Token、每 3 条
// 一条只读来源，其余走正式来源。某一类不存在时退回第一条可用来源（不伪造来源）。
func observabilityPickPublicSource(sources []observabilityPublicSource, index int) observabilityPublicSource {
	if index%5 == 0 {
		if source, ok := observabilityPublicSourceOfKind(sources, "test"); ok {
			return source
		}
	}
	if index%3 == 0 {
		if source, ok := observabilityPublicSourceOfKind(sources, "readonly"); ok {
			return source
		}
	}
	return sources[0]
}

// observabilityPublicSourceOfKind 按来源类别取第一条（类别含义见 observabilitySourceByKind）。
func observabilityPublicSourceOfKind(sources []observabilityPublicSource, kind string) (observabilityPublicSource, bool) {
	for _, source := range sources {
		if source.Kind == kind {
			return source, true
		}
	}
	return observabilityPublicSource{}, false
}

// observabilityBuildPublicAPILogRows 生成近 Days 天的公开接口日志行。
func observabilityBuildPublicAPILogRows(e *env, sources []observabilityPublicSource, now time.Time) []map[string]any {
	days := e.options.Days
	perDay := 4 + (e.options.DailyRequests/20)%8
	if perDay < 2 {
		perDay = 2
	}
	rows := make([]map[string]any, 0, days*perDay)
	index := 0
	for _, dayIndex := range observabilityDayIndexes(days) {
		dayStart := observabilityDayStart(now).AddDate(0, 0, -(days - 1 - dayIndex))
		for rowIndex := 0; rowIndex < perDay; rowIndex++ {
			endpoint := observabilityPublicEndpoints[index%len(observabilityPublicEndpoints)]
			source := observabilityPickPublicSource(sources, index)
			at := observabilitySpread(now, dayStart, rowIndex, perDay)
			status := observabilityPublicStatus(index, endpoint.Method)
			success := status >= 200 && status < 300
			duration := 40 + (index*71)%1200
			ended := at.Add(time.Duration(duration) * time.Millisecond)
			requestJSON, responseJSON := observabilityPublicPayloads(endpoint, status, success, index)
			columns := map[string]any{
				"id":                      observabilityStamp(CleanupIDPrefix+"public_api_log_", at, index),
				"trace_id":                CleanupTracePrefix + "public-api-" + at.Format("20060102") + "-" + fmt.Sprintf("%03d", index+1),
				"source_ref_id":           source.SourceID,
				"source_name":             source.SourceName,
				"token_id":                source.TokenID,
				"token_name":              source.TokenName,
				"token_prefix":            source.Prefix,
				"is_test_token":           observabilityBool(source.IsTest),
				"method":                  endpoint.Method,
				"path":                    endpoint.Path,
				"query_string":            endpoint.Query,
				"client_ip":               fmt.Sprintf("172.20.%d.%d", index%16, 20+(index%180)),
				"user_agent":              observabilityPublicUserAgent(index),
				"status_code":             status,
				"success":                 observabilityBool(success),
				"duration_ms":             duration,
				"request_size_bytes":      len(requestJSON),
				"response_size_bytes":     len(responseJSON),
				"request_capture_status":  observabilityPublicRequestCaptureStatus(index, endpoint),
				"response_capture_status": observabilityPublicResponseCaptureStatus(index, success),
				"request_data_json":       requestJSON,
				"response_data_json":      responseJSON,
				"started_at":              observabilityFormatTime(at),
				"ended_at":                observabilityFormatTime(ended),
				"created_at":              observabilityFormatTime(ended),
			}
			observabilitySetText(columns, map[string]string{
				"error_code":    observabilityPublicErrorCode(status),
				"error_message": observabilityPublicErrorMessage(status),
			})
			rows = append(rows, columns)
			index++
		}
	}
	return rows
}

// observabilityPublicUserAgent 让只读来源与机器人样本可区分（页面按 UA 展示来源）。
func observabilityPublicUserAgent(index int) string {
	if index%7 == 0 {
		return CleanupIDPrefix + "public-bot/1.0"
	}
	return CleanupIDPrefix + "public-client/1.0"
}

// observabilityPublicRequestCaptureStatus 覆盖 empty / complete / truncated 三种
// capture 状态：GET 无请求体（empty）、写入请求完整（complete）、少数被截断。
func observabilityPublicRequestCaptureStatus(index int, endpoint observabilityPublicEndpoint) string {
	// 与 Node mockdata 的取模顺序一致：truncated 先在整体样本上命中，
	// 否则按「有请求体的写入请求 complete、无请求体的 GET empty」判定。
	if index%19 == 0 {
		return "truncated"
	}
	if endpoint.Write {
		return "complete"
	}
	return "empty"
}

// observabilityPublicResponseCaptureStatus 同上，作用于响应体。
//
// 截断样本用 11 取模而不是 Node 的 23：23 与 403 的判定取模同余，会让「响应体被
// 截断」的样本永远落在失败状态上（响应体只可能在成功响应里被截断），页面上就再也
// 看不到响应截断样本。11 与其它取模互质，能保证成功样本里真的有截断。
func observabilityPublicResponseCaptureStatus(index int, success bool) string {
	if success && index%11 == 0 {
		return "truncated"
	}
	return "complete"
}

// observabilityPublicPayloads 生成请求 / 响应 capture JSON。
func observabilityPublicPayloads(endpoint observabilityPublicEndpoint, status int, success bool, index int) (string, string) {
	request := map[string]any{
		"mockdata": true,
		"scope":    observabilityPublicScope(endpoint),
		"page":     1 + index%5,
		"pageSize": 20,
	}
	if endpoint.Write {
		request["targetUsername"] = CleanupIDPrefix + "admin"
		request["providerCode"] = "gpt"
	}
	if !endpoint.Write && endpoint.Query == "" {
		request["query"] = observabilityPublicScope(endpoint)
	}
	response := map[string]any{
		"source": "stats",
		"mock":   false,
	}
	if success {
		response["action"] = "read"
		if endpoint.Write {
			response["action"] = "created"
		}
		response["items"] = []any{map[string]any{
			"id":   CleanupIDPrefix + "public_item_" + fmt.Sprintf("%d", index),
			"name": CleanupNamePrefix + "公开接口返回项",
		}}
	} else {
		response["message"] = observabilityPublicErrorMessage(status)
		response["statusCode"] = status
		if code := observabilityPublicErrorCode(status); code != "" {
			response["code"] = code
		}
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		requestJSON = []byte("{}")
	}
	responseJSON, err := json.Marshal(response)
	if err != nil {
		responseJSON = []byte("{}")
	}
	return string(requestJSON), string(responseJSON)
}

// observabilityPublicScope 给端点一个稳定的 scope 标签（与 aipublic scope 同名）。
func observabilityPublicScope(endpoint observabilityPublicEndpoint) string {
	name := strings.TrimPrefix(endpoint.Path, "/__aipublic__/")
	name = strings.ReplaceAll(name, "-", "_")
	switch {
	case strings.HasSuffix(name, "/list"):
		return "juhe_ai_public:" + strings.TrimSuffix(name, "/list") + ":list:read"
	case strings.HasSuffix(name, "/add"):
		return "juhe_ai_public:" + strings.TrimSuffix(name, "/add") + ":add:write"
	case strings.HasSuffix(name, "/update"):
		return "juhe_ai_public:" + strings.TrimSuffix(name, "/update") + ":update:write"
	case strings.HasSuffix(name, "/del"):
		return "juhe_ai_public:" + strings.TrimSuffix(name, "/del") + ":delete:write"
	default:
		return "juhe_ai_public:" + name
	}
}

// ---------------------------------------------------------------------------
// 后台记录清理目标（dataset 库）
// ---------------------------------------------------------------------------

// observabilityCleanupTargetSample 是一条清理目标样本：attemptCount 覆盖
// 0（未尝试）/ 1（已尝试）/ 2（多次尝试后仍有阻塞原因）。
type observabilityCleanupTargetSample struct {
	Suffix            string
	AttemptCount      int
	BlockedReason     string
	ErrorMessage      string
	RelatedJSON       string
	AuthorizationJSON string
	TeamScopeJSON     string
}

var observabilityAccountCleanupTargets = []observabilityCleanupTargetSample{
	{
		Suffix: "display_deleted_account_01", AttemptCount: 2,
		BlockedReason:     CleanupNamePrefix + "等待 usage shard 短事务空闲",
		ErrorMessage:      CleanupNamePrefix + "Mockdata 模拟账号相关记录清理遇到 SQLite 写锁",
		RelatedJSON:       `["` + CleanupIDPrefix + `display_related_account_01"]`,
		AuthorizationJSON: `["` + CleanupIDPrefix + `display_authorization_01"]`,
		TeamScopeJSON:     `["` + CleanupIDPrefix + `display_team_scope_01"]`,
	},
	{
		Suffix: "display_deleted_account_02", AttemptCount: 1,
		BlockedReason:     CleanupNamePrefix + "等待授权窗口扣减完成",
		AuthorizationJSON: `["` + CleanupIDPrefix + `display_authorization_02","` + CleanupIDPrefix + `display_authorization_03"]`,
		TeamScopeJSON:     `["` + CleanupIDPrefix + `display_team_scope_02"]`,
	},
	{
		Suffix: "display_deleted_account_03", AttemptCount: 0,
		BlockedReason: CleanupNamePrefix + "待后台维护任务处理",
		RelatedJSON:   `["` + CleanupIDPrefix + `display_related_account_03"]`,
	},
}

var observabilityAPIKeyCleanupTargets = []observabilityCleanupTargetSample{
	{
		Suffix: "display_deleted_api_key_01", AttemptCount: 2,
		BlockedReason: CleanupNamePrefix + "等待统计扣减重试",
		ErrorMessage:  CleanupNamePrefix + "Mockdata 模拟 API Key 清理锁竞争",
	},
	{
		Suffix: "display_deleted_api_key_02", AttemptCount: 1,
		BlockedReason: CleanupNamePrefix + "等待数据集索引清理",
	},
	{
		Suffix: "display_deleted_api_key_03", AttemptCount: 0,
		BlockedReason: CleanupNamePrefix + "待后台维护任务处理",
	},
}

// seedRecordCleanupTargets 写 dataset 库的两张清理目标表。
func seedRecordCleanupTargets(ctx context.Context, e *env, refs observabilityReferences, now time.Time, counts map[string]int) error {
	accountReady, err := observabilityTableReady(ctx, e, StoreDataset, "account_record_cleanup_targets")
	if err != nil {
		return err
	}
	apiKeyReady, err := observabilityTableReady(ctx, e, StoreDataset, "api_key_record_cleanup_targets")
	if err != nil {
		return err
	}
	if !accountReady && !apiKeyReady {
		observabilitySkip(e, "后台清理目标", StoreDataset, "account_record_cleanup_targets")
		return nil
	}
	owner := observabilityPickUser(refs.Users, 0)
	return e.tx(ctx, StoreDataset, func(tx *sql.Tx) error {
		accountRows, apiKeyRows := 0, 0
		if accountReady {
			if err := observabilityExec(ctx, tx, "DELETE FROM account_record_cleanup_targets WHERE account_id LIKE ?", CleanupIDPrefix+"%"); err != nil {
				return err
			}
			for index, sample := range observabilityAccountCleanupTargets {
				createdAt := now.AddDate(0, 0, -(index + 1))
				columns := map[string]any{
					"account_id":               CleanupIDPrefix + sample.Suffix,
					"system_account_id":        owner.ID,
					"related_account_ids_json": observabilityJSONOrDefault(sample.RelatedJSON),
					"authorization_ids_json":   observabilityJSONOrDefault(sample.AuthorizationJSON),
					"team_scope_ids_json":      observabilityJSONOrDefault(sample.TeamScopeJSON),
					"created_at":               observabilityFormatTime(createdAt),
					"updated_at":               observabilityFormatTime(now),
					"attempt_count":            sample.AttemptCount,
					"last_blocked_reason":      sample.BlockedReason,
				}
				if sample.AttemptCount > 0 {
					columns["last_attempt_at"] = observabilityFormatTime(now.Add(-time.Duration(index+2) * time.Hour))
				}
				observabilitySetText(columns, map[string]string{"last_error_message": sample.ErrorMessage})
				if err := observabilityInsert(ctx, tx, "account_record_cleanup_targets", columns); err != nil {
					return err
				}
				accountRows++
			}
		}
		if apiKeyReady {
			if err := observabilityExec(ctx, tx, "DELETE FROM api_key_record_cleanup_targets WHERE api_key_id LIKE ?", CleanupIDPrefix+"%"); err != nil {
				return err
			}
			for index, sample := range observabilityAPIKeyCleanupTargets {
				createdAt := now.AddDate(0, 0, -(index + 1))
				columns := map[string]any{
					"api_key_id":          CleanupIDPrefix + sample.Suffix,
					"system_account_id":   owner.ID,
					"created_at":          observabilityFormatTime(createdAt),
					"updated_at":          observabilityFormatTime(now),
					"attempt_count":       sample.AttemptCount,
					"last_blocked_reason": sample.BlockedReason,
				}
				if sample.AttemptCount > 0 {
					columns["last_attempt_at"] = observabilityFormatTime(now.Add(-time.Duration(index+3) * time.Hour))
				}
				observabilitySetText(columns, map[string]string{"last_error_message": sample.ErrorMessage})
				if err := observabilityInsert(ctx, tx, "api_key_record_cleanup_targets", columns); err != nil {
					return err
				}
				apiKeyRows++
			}
		}
		observabilityAdd(counts, "accountCleanupTargets", accountRows)
		observabilityAdd(counts, "apiKeyCleanupTargets", apiKeyRows)
		return nil
	})
}

// observabilityJSONOrDefault 保持 JSON 列始终是合法 JSON 文本（空样本写 []）。
func observabilityJSONOrDefault(value string) string {
	if strings.TrimSpace(value) == "" {
		return "[]"
	}
	return value
}
