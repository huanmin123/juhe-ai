package mockdata

// 可观测域（T66 PG 造数第二波）：juhe_dataset 维护面（public_api_logs、后台
// 记录清理目标）+ 属主表审计 / 操作 / 运行日志族（gateway internal/auditlog、
// gateway internal/operationlog、jobs internal/runtimelog 运行时建表）。
//
// 样本形状、数量、状态矩阵与关联语义沿用 sqlite 版 domain_observability.go
// （删除于 4645eb259，git 历史可溯）；存储形态按现行 PG DDL 逐列重写：
//   - 属主表的时间列是 timestamptz、布尔列是 boolean（与 maintenance schema
//     的 text 时间 + integer 布尔不同），写入值逐列对照
//     gateway/internal/auditlog/schema.go、gateway/internal/operationlog/
//     schema.go、jobs/internal/runtimelog/schema.go 与
//     maintenance/internal/schema/pg_schema_dataset.go；
//   - 属主表缺失时按子段跳过（framework probeOwnerStores 已把它们登记为
//     ownerStores，Run 报告 OwnerStoresPending），不复制 DDL、不阻塞 Ready；
//   - PG 形态没有数据根：审计 payload blob 只落库（内容寻址 id/storage_key，
//     媒体样本以 data URI 内嵌进 payload JSON），运行日志只写索引行与分面，
//     不写日志文件与文件游标（runtime_log_file_cursors 按属主游标族白名单
//     允许为空）。
//
// 覆盖点：docs/functions/Mockdata造数设计.md「数据边界」observability 行与
// 「验证点」（审计 / 操作 / 运行日志与公开接口日志页面可按 mockdata / 造数
// 关键字检索；上游响应模型三条对比 trace 由本域提供审计侧 payload）。

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// obsTable 返回 juhe_dataset 下的全限定表名（本文件所有 SQL 的唯一拼表口）。
func obsTable(table string) string { return qualified(SchemaDataset, table) }

// 可观测域常量：时间文本、blob 指纹、分面桶与内置测试来源。
const (
	// observabilityBlobDigestBytes 与 gateway auditlog shortHash 一致：
	// contentType 的 sha256 前 8 字节十六进制（16 个 hex 字符）。
	observabilityBlobDigestBytes = 8

	// blob 压缩阈值与 gateway auditlog store 的常量同语义（跨 module 不可
	// import，值对齐 4KiB 起压、1MiB 封顶；内容类型仅可文本压缩族）。
	observabilityBlobCompressionThresholdBytes = 4 * 1024
	observabilityBlobCompressionMaxBytes       = 1024 * 1024

	// observabilityRuntimeFacetBucketKey 与 jobs runtimelog / gateway logreads
	// 的 runtimeLogFacetBucketKey 一致（「当前索引」聚合桶）。
	observabilityRuntimeFacetBucketKey = "current"

	// 运行日志文件名：满足 jobs runtimelog 的实例日志文件模式
	// （juhe-ai.<instance>.log / juhe-ai.<role>.<instance>.log），并带 mockdata
	// 实例名避免与真实实例日志混淆。PG 形态只写索引行的 log_file 文本，不写
	// 物理文件。
	observabilityServerLogFile = "juhe-ai.mockdata-server.log"
	observabilityWorkerLogFile = "juhe-ai.stats-worker.mockdata-dev.log"

	// 内置外部来源系统与测试 Token（maintenance/internal/schema seed 与
	// gateway/internal/aipublic 同名常量）：is_test_token 的判定就是「来源 id
	// 命中该常量」。
	observabilityBuiltInTestSourceID = "extsrc_builtin_test"

	// observabilitySearchTermMaxRunes / observabilitySearchTermLimit 是摘要检索
	// 词展开的边界：页面只需要「按摘要里的词能命中」，不复刻 gateway F4 最多
	// 1500 条的完整枚举（行数与写入耗时可控）。
	observabilitySearchTermMaxRunes = 8
	observabilitySearchTermLimit    = 400
)

// seedObservability 是可观测域入口：五个子段各自独立判存、独立跳过。任一子段
// 写入失败即整体失败（半批数据比没有更难排查）；属主表缺失只跳过对应子段。
func seedObservability(ctx context.Context, e *env) (DomainResult, error) {
	now := e.options.clock().UTC()
	counts := map[string]int{}
	refs := observabilityCollectReferences(ctx, e)
	steps := []struct {
		name string
		run  func() error
	}{
		{"审计日志", func() error { return observabilitySeedAuditLogs(ctx, e, refs, now, counts) }},
		{"操作日志", func() error { return observabilitySeedOperationLogs(ctx, e, refs, now, counts) }},
		{"运行日志", func() error { return observabilitySeedRuntimeLogs(ctx, e, now, counts) }},
		{"公开接口日志", func() error { return observabilitySeedPublicAPILogs(ctx, e, refs, now, counts) }},
		{"后台清理目标", func() error { return observabilitySeedRecordCleanupTargets(ctx, e, refs, now, counts) }},
	}
	for _, step := range steps {
		if err := step.run(); err != nil {
			return DomainResult{}, fmt.Errorf("%s: %w", step.name, err)
		}
	}
	// Counts 只记真实写出的行数：全部子段都跳过时 Counts 为空（域按未接线
	// 呈现），部分跳过时只列写出的子段。
	return DomainResult{Name: DomainObservability, Counts: counts}, nil
}

// observabilityAdd 记一次行数；0 行不入账。
func observabilityAdd(counts map[string]int, key string, rows int) {
	if rows > 0 {
		counts[key] += rows
	}
}

// observabilitySkip 记录一次「属主表不存在」的子段跳过。跳过不是失败（属主表
// 由 gateway/jobs 运行时建），但必须可追溯：Run 报告的 OwnerStoresPending 与
// 覆盖报告 NotCovered 都以同一次探测为依据。
func observabilitySkip(e *env, step string, tables []string) {
	e.logger.Info("mockdata 可观测域跳过：属主表不存在", "step", step, "tables", strings.Join(tables, ","))
}

// observabilityTablesReady 探测一组属主表是否全部存在（to_regclass 只读探测）。
func observabilityTablesReady(ctx context.Context, e *env, step string, tables ...string) (bool, error) {
	missing := make([]string, 0, len(tables))
	for _, table := range tables {
		exists, err := tableExists(ctx, e.db, SchemaDataset, table)
		if err != nil {
			return false, fmt.Errorf("探测 %s: %w", obsTable(table), err)
		}
		if !exists {
			missing = append(missing, table)
		}
	}
	if len(missing) > 0 {
		observabilitySkip(e, step, missing)
		return false, nil
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// business 域引用
// ---------------------------------------------------------------------------

// observabilityUser 是一条 mock 系统账户样本（审计 / 操作日志的操作者与归属）。
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
// 为什么运行时查询而不是写死 ID：业务资源由 seedBusiness 生成，ID 方案属于它
// 的契约；本域只读 juhe_business（查询带清理标识前缀），查不到时退回带清理
// 标识的约定 ID（与 business 域现行 ID 方案一致），保证本域独立可执行且报告
// 能看出「未关联到真实资源」。
type observabilityReferences struct {
	Users          []observabilityUser
	Accounts       []observabilityResource
	Groups         []observabilityResource
	APIKeys        []observabilityResource
	Authorizations []observabilityResource
	Announcements  []observabilityResource
	Sources        []observabilitySource
}

// observabilityFallbackUsers 是 business 域尚未产出时的兜底操作者样本；ID 与
// business 域 seedUsers 的现行主键一致，使跨域引用在兜底场景下仍然对得上。
var observabilityFallbackUsers = []observabilityUser{
	{ID: CleanupIDPrefix + "user_admin", Username: CleanupIDPrefix + "admin", DisplayName: CleanupNamePrefix + "管理员用户", Role: "admin"},
	{ID: CleanupIDPrefix + "user_ops", Username: CleanupIDPrefix + "ops", DisplayName: CleanupNamePrefix + "运维用户", Role: "user"},
	{ID: CleanupIDPrefix + "user_viewer", Username: CleanupIDPrefix + "viewer", DisplayName: CleanupNamePrefix + "观察用户", Role: "user"},
}

// observabilityFallbackResource 是兜底资源样本：ID 带清理标识。
func observabilityFallbackResource(kind, suffix string) observabilityResource {
	return observabilityResource{ID: CleanupIDPrefix + kind + "_" + suffix, Name: CleanupNamePrefix + kind + " " + suffix}
}

// observabilityCollectReferences 只读收集 juhe_business 里的 mock 引用。表缺失
// 或查询失败时退回兜底样本（记 warn），不让本域失败：business 数据不是本域的
// 前置条件。
func observabilityCollectReferences(ctx context.Context, e *env) observabilityReferences {
	refs := observabilityReferences{}
	present, err := tableExists(ctx, e.db, SchemaBusiness, "system_accounts")
	if err != nil {
		e.logger.Warn("mockdata 可观测域探测 business 表失败，改用兜底引用", "error", err.Error())
	}
	if !present {
		return observabilityCompleteReferencesFallback(refs)
	}
	refs.Users = observabilityQueryUsers(ctx, e)
	refs.Accounts = observabilityQueryResources(ctx, e, "accounts", "name")
	refs.Groups = observabilityQueryResources(ctx, e, "groups", "name")
	refs.APIKeys = observabilityQueryResources(ctx, e, "api_keys", "name")
	refs.Authorizations = observabilityQueryResources(ctx, e, "resource_authorizations", "remark")
	refs.Announcements = observabilityQueryResources(ctx, e, "announcements", "title")
	refs.Sources = observabilityQuerySources(ctx, e)
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

// observabilityQueryUsers 读取 mock 系统账户样本。
func observabilityQueryUsers(ctx context.Context, e *env) []observabilityUser {
	rows, err := e.db.QueryContext(ctx, `SELECT id, username, display_name, role FROM `+bt("system_accounts")+`
		WHERE id LIKE $1 OR username LIKE $2 OR display_name LIKE $3
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

// observabilityQueryResources 读取一类 mock 资源（id + 名称列）。表缺失或查询
// 失败返回 nil，由兜底补齐。
func observabilityQueryResources(ctx context.Context, e *env, table, nameColumn string) []observabilityResource {
	query := "SELECT id, COALESCE(" + nameColumn + ", '') FROM " + bt(table) +
		" WHERE id LIKE $1 OR " + nameColumn + " LIKE $2 ORDER BY id LIMIT 4"
	rows, err := e.db.QueryContext(ctx, query, CleanupIDPrefix+"%", CleanupNamePrefix+"%")
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
func observabilityQuerySources(ctx context.Context, e *env) []observabilitySource {
	rows, err := e.db.QueryContext(ctx, `SELECT s.id, s.name, COALESCE(s.scopes_json, '[]'),
			COALESCE(t.id, ''), COALESCE(t.name, ''), COALESCE(t.token_prefix, '')
		FROM `+bt("external_integration_sources")+` s
		LEFT JOIN `+bt("external_integration_source_tokens")+` t
			ON t.source_ref_id = s.id AND t.status = 'active'
		WHERE s.id LIKE $1 OR s.name LIKE $2 OR s.id = $3
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

// observabilityPickUser / observabilityPickResource 按序号取样本（取模保证有值）。
func observabilityPickUser(users []observabilityUser, index int) observabilityUser {
	if len(users) == 0 {
		return observabilityFallbackUsers[0]
	}
	return users[index%len(users)]
}

func observabilityPickResource(items []observabilityResource, index int) observabilityResource {
	if len(items) == 0 {
		return observabilityFallbackResource("resource", "sample")
	}
	return items[index%len(items)]
}

// observabilitySourceByKind 按「primary（可写）/ readonly（只读）/ test（内置
// 测试）」三类从候选里挑样本；缺失返回零值。
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
// 时间铺开（与 business 域共用 options.clock()，全部 UTC）
// ---------------------------------------------------------------------------

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

// observabilitySpread 把第 index 个样本铺到某一天里：固定步长 + 固定抖动让时间
// 均匀且在同一 Options.Now 下可复现，且不会越过 now（今天只用到已过去的部分）。
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

// observabilitySetText 只写入非空文本列，保持空值在库里是 NULL（与属主 store
// 的「可选字段缺省」语义一致，避免页面上出现空字符串与 NULL 两种空）。
func observabilitySetText(columns map[string]any, values map[string]string) {
	for name, value := range values {
		if strings.TrimSpace(value) != "" {
			columns[name] = value
		}
	}
}

// ---------------------------------------------------------------------------
// 审计日志（audit_log 属主存储：audit_logs / audit_log_attempts /
// audit_payload_blobs / audit_payload_refs / audit_error_groups）
// ---------------------------------------------------------------------------

// observabilityAuditSample 是审计日志样本矩阵的一行：覆盖成功 / 重试成功 / 模型
// 映射 / 上游失败 / 网关失败 / 请求体拒绝 / 流式中断 / 下游断开 / 健康检查流量
// 来源；端点覆盖 /v1/responses、/v1/chat/completions、/v1/images/generations、
// /v1/models 与媒体端点 /v1/audio/speech、/v1/videos（媒体契约 §3.4）。
type observabilityAuditSample struct {
	Method         string
	Path           string
	Family         string
	Model          string
	UpstreamModel  string
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

// observabilityAuditSamples 的状态矩阵沿用 sqlite 版造数（4645eb259^），列型与
// 关联语义按现行 auditlog DDL 重写。
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
	// 媒体任务审计样本（媒体契约 §3.4）：M1 同步音频 speech 与 M2 视频创建。
	// 音频 speech 的 upstream_response payload 以 data URI 内嵌 WAV 样本字节。
	{
		Method: "POST", Path: "/v1/audio/speech", Family: "audio",
		Model: "gpt-4o-mini-tts", UpstreamModel: "gpt-4o-mini-tts", PricingModel: "gpt-4o-mini-tts",
		TrafficSource: "gateway", ClientType: "generic_openai",
		Outcome: "success", Success: true, StatusCode: 200, SampleReason: "always",
		Attempts: 1, Parts: []string{"client_request", "upstream_response", "gateway_response"},
	},
	{
		Method: "POST", Path: "/v1/videos", Family: "videos",
		Model: "sora-2", UpstreamModel: "sora-2", PricingModel: "sora-2",
		TrafficSource: "gateway", ClientType: "generic_openai",
		Outcome: "success", Success: true, StatusCode: 200, SampleReason: "always",
		Attempts: 1, Parts: []string{"client_request", "upstream_response", "gateway_response"},
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
		Model: mockModelMappingSourceModel, UpstreamModel: "gpt-5.4-mini", PricingModel: "gpt-5.4-mini",
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
// 上游响应模型对比样本（match / mismatch / unmapped_mismatch）。audit
// upstream_response payload 的 model 字段是「原始响应模型」，upstream_model 是
// 实际发送模型；usage 域按同一 trace 约定写记录即可对齐。
type observabilityUpstreamResponseCoverageSample struct {
	TraceSuffix    string
	ResponseModel  string
	MappingApplied bool
}

// observabilityUpstreamResponseCoverageSamples 与用量域的三条固定 trace 对齐
// （docs/functions/Mockdata造数设计.md「数据边界」补充约束）。
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

// observabilityBlob 是一次 payload blob 的计划：内容寻址元数据与存储字节。
type observabilityBlob struct {
	ID              string
	SHA256          string
	StorageKey      string
	ContentType     string
	Compression     string
	ContentEncoding any
	RawBytes        []byte
	RawSize         int
	CompressedSize  int
}

// observabilityBuildBlob 复刻 gateway auditlog newBlobRecord 的现行语义：
// id = blob:<sha256hex>:<shortHash8(contentType)>，storage_key =
// sha256/<digest>-<shortHash8><.blob|.gz>；4KiB 及以上的可文本压缩 JSON 走
// gzip（压缩无收益时回退 none），其余原样存储。内容全部由本域常量与 Options
// 决定，同一运行配置下 id 稳定（重复执行复用同一行）。
func observabilityBuildBlob(contentType string, raw []byte) observabilityBlob {
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	contentSum := sha256.Sum256([]byte(contentType))
	shortHash := hex.EncodeToString(contentSum[:observabilityBlobDigestBytes])
	stored, compression := raw, "none"
	if len(raw) >= observabilityBlobCompressionThresholdBytes &&
		len(raw) <= observabilityBlobCompressionMaxBytes &&
		observabilityCompressibleContentType(contentType) {
		var buffer bytes.Buffer
		writer := gzip.NewWriter(&buffer)
		_, _ = writer.Write(raw)
		_ = writer.Close()
		if buffer.Len() < len(raw) {
			stored, compression = buffer.Bytes(), "gzip"
		}
	}
	suffix := ".blob"
	if compression == "gzip" {
		suffix = ".gz"
	}
	return observabilityBlob{
		ID:              "blob:" + digest + ":" + shortHash,
		SHA256:          digest,
		StorageKey:      "sha256/" + digest + "-" + shortHash + suffix,
		ContentType:     contentType,
		Compression:     compression,
		ContentEncoding: nil,
		RawBytes:        raw,
		RawSize:         len(raw),
		CompressedSize:  len(stored),
	}
}

// observabilityCompressibleContentType 与 gateway auditlog
// isCompressibleAuditPayload 的内容类型词表一致（默认无 content-encoding）。
func observabilityCompressibleContentType(contentType string) bool {
	lower := strings.ToLower(contentType)
	return strings.Contains(lower, "json") || strings.Contains(lower, "text") ||
		strings.Contains(lower, "xml") || strings.Contains(lower, "event-stream") ||
		strings.Contains(lower, "javascript") || strings.Contains(lower, "x-www-form-urlencoded")
}

// observabilityAuditPayloadBody 给出某一 payload 部位的内容。媒体端点的
// upstream_response 以 data URI 内嵌共享媒体样本字节（与 business 域
// media_jobs artifact 的自包含指针形态一致）。上游响应 payload 的 model 是
// 「原始响应模型」——上游模型对比验收读这个字段。
func observabilityAuditPayloadBody(partType string, row observabilityAuditRow) []byte {
	var body any
	switch partType {
	case "client_request":
		body = map[string]any{
			"model":  row.Model,
			"stream": row.Stream,
			"messages": []any{
				map[string]any{"role": "user", "content": CleanupNamePrefix + "审计样本请求"},
			},
		}
	case "upstream_request":
		body = map[string]any{
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
		response := map[string]any{
			"id":      "chatcmpl-" + row.ID,
			"object":  "chat.completion",
			"model":   responseModel,
			"choices": []any{map[string]any{"index": 0, "finish_reason": observabilityAuditFinishReason(row)}},
			"usage":   map[string]any{"prompt_tokens": 812, "completion_tokens": 143, "total_tokens": 955},
		}
		// 音频 speech 样本：data URI 内嵌 1 秒 WAV 样本字节（>4KiB 且
		// application/json 可压缩——blob 元数据走 gzip / .gz 分支）。
		if row.Family == "audio" {
			response["audio"] = map[string]any{
				"format": "wav",
				"data": "data:audio/wav;base64," +
					base64.StdEncoding.EncodeToString(chatCodexWAV()),
			}
		}
		body = response
	case "gateway_response":
		body = map[string]any{
			"id":     row.ID,
			"object": "response",
			"model":  row.Model,
			"status": observabilityAuditGatewayStatus(row),
		}
	case "gateway_error":
		body = map[string]any{
			"error": map[string]any{"message": row.ErrorMessage, "code": row.ErrorCode},
		}
	default:
		body = map[string]any{"mockdata": true, "partType": partType, "traceId": row.TraceID}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		// 内容全部由本域常量构造，编码失败不可达；失败可见而不是静默丢列。
		panic(fmt.Sprintf("mockdata 审计 payload 编码失败: %v", err))
	}
	return encoded
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

// observabilitySeedAuditLogs 写入审计日志、尝试、payload blob / 引用与错误分组。
//
// 写入顺序沿属主表外键（立即约束）：audit_logs → audit_log_attempts →
// audit_payload_blobs → audit_payload_refs（attempt_id / body_blob_id 非空时
// 目标行必须已存在）→ audit_error_groups → 回填 audit_logs.error_group_id。
func observabilitySeedAuditLogs(ctx context.Context, e *env, refs observabilityReferences, now time.Time, counts map[string]int) error {
	ready, err := observabilityTablesReady(ctx, e, "审计日志", "audit_logs", "audit_log_attempts",
		"audit_payload_blobs", "audit_payload_refs", "audit_error_groups")
	if err != nil {
		return err
	}
	if !ready {
		return nil
	}
	rows := observabilityBuildAuditRows(e, refs, now)
	blobs := observabilityPlanAuditBlobs(rows)
	return e.tx(ctx, func(tx *sql.Tx) error {
		for _, row := range rows {
			if err := e.insertMapTx(ctx, tx, obsTable("audit_logs"), observabilityAuditLogColumns(row)); err != nil {
				return err
			}
			for _, attempt := range row.Attempts {
				if err := e.insertMapTx(ctx, tx, obsTable("audit_log_attempts"),
					observabilityAuditAttemptColumns(row, attempt)); err != nil {
					return err
				}
			}
		}
		for _, blob := range blobs {
			// ref_count 按属主 PG 语义固定写 0：gateway 在 PG 形态把
			// audit_payload_refs 当唯一事实源，ref_count 缓存列不再维护
			// （会与 retention / 并发写者竞态漂移），造数与真实写入同形。
			if err := e.insertMapTx(ctx, tx, obsTable("audit_payload_blobs"), map[string]any{
				"id": blob.ID, "sha256": blob.SHA256,
				"raw_size_bytes": blob.RawSize, "compressed_size_bytes": blob.CompressedSize,
				"content_type": blob.ContentType, "content_encoding": blob.ContentEncoding,
				"compression": blob.Compression, "storage_key": blob.StorageKey,
				"ref_count":     0,
				"first_seen_at": now, "last_seen_at": now, "created_at": now,
			}); err != nil {
				return err
			}
		}
		refRows := 0
		for _, row := range rows {
			for sequence, part := range row.Parts {
				if err := e.insertMapTx(ctx, tx, obsTable("audit_payload_refs"),
					observabilityAuditPayloadRefColumns(row, part, sequence+1)); err != nil {
					return err
				}
				refRows++
			}
		}
		groupRows, err := observabilityWriteAuditErrorGroups(ctx, e, tx, rows, now)
		if err != nil {
			return err
		}
		observabilityAdd(counts, "auditLogs", len(rows))
		observabilityAdd(counts, "auditLogAttempts", observabilityCountAttempts(rows))
		observabilityAdd(counts, "auditPayloadBlobs", len(blobs))
		observabilityAdd(counts, "auditPayloadRefs", refRows)
		observabilityAdd(counts, "auditErrorGroups", groupRows)
		return nil
	})
}

// observabilityCountAttempts 统计尝试行总数。
func observabilityCountAttempts(rows []observabilityAuditRow) int {
	total := 0
	for _, row := range rows {
		total += len(row.Attempts)
	}
	return total
}

// observabilityBuildAuditRows 生成近 Days 天的审计样本 + 三条上游响应模型对比
// 样本（对比样本落在「今天」，与用量域的同 trace 记录同一天可见）。
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
			row := observabilityAuditRow{
				ID:                observabilityStamp(CleanupIDPrefix+"audit_", at, index),
				TraceID:           CleanupTracePrefix + "audit-" + at.Format("20060102") + "-" + fmt.Sprintf("%03d", index+1),
				TrafficSource:     sample.TrafficSource,
				SystemAccountID:   observabilityPickUser(refs.Users, index).ID,
				APIKeyID:          observabilityPickResource(refs.APIKeys, index).ID,
				GroupID:           observabilityPickResource(refs.Groups, index).ID,
				AccountID:         observabilityPickResource(refs.Accounts, index).ID,
				SessionID:         CleanupTracePrefix + "session-" + at.Format("20060102") + "-" + fmt.Sprintf("%03d", index+1),
				SessionClientType: sample.ClientType,
				ProviderCode:      "gpt",
				Method:            sample.Method,
				Path:              sample.Path,
				Model:             sample.Model,
				UpstreamModel:     sample.UpstreamModel,
				PricingModel:      sample.PricingModel,
				ResponseModel:     "",
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
			row.Attempts = observabilityBuildAuditAttempts(row, sample.Attempts)
			rows = append(rows, row)
			index++
		}
	}
	for i, coverage := range observabilityUpstreamResponseCoverageSamples {
		at := observabilitySpread(now, observabilityDayStart(now), i, len(observabilityUpstreamResponseCoverageSamples)+1)
		mappingSource := ""
		if coverage.MappingApplied {
			mappingSource = "account_model_mapping"
		}
		requestModel := "gpt-5.4-mini"
		if coverage.MappingApplied {
			requestModel = mockModelMappingSourceModel
		}
		row := observabilityAuditRow{
			ID:                CleanupIDPrefix + "audit_coverage_upstream_response_model_" + coverage.TraceSuffix,
			TraceID:           observabilityCoverageTrace(coverage.TraceSuffix),
			TrafficSource:     "gateway",
			SystemAccountID:   observabilityPickUser(refs.Users, i).ID,
			APIKeyID:          observabilityPickResource(refs.APIKeys, i).ID,
			GroupID:           observabilityPickResource(refs.Groups, i).ID,
			AccountID:         observabilityPickResource(refs.Accounts, i).ID,
			SessionID:         CleanupTracePrefix + "session-coverage-" + coverage.TraceSuffix,
			SessionClientType: "generic_openai",
			ProviderCode:      "gpt",
			Method:            "POST",
			Path:              "/v1/chat/completions",
			Model:             requestModel,
			UpstreamModel:     "gpt-5.4-mini",
			PricingModel:      "gpt-5.4-mini",
			ResponseModel:     coverage.ResponseModel,
			MappingApplied:    coverage.MappingApplied,
			MappingSource:     mappingSource,
			Family:            "chat_completions",
			ClientIP:          fmt.Sprintf("172.20.9.%d", 20+i),
			UserAgent:         CleanupIDPrefix + "coverage-client/1.0",
			Outcome:           "success",
			Success:           true,
			StatusCode:        200,
			SampleReason:      "always",
			Parts:             []string{"client_request", "upstream_response"},
			StartedAt:         at,
			EndedAt:           at.Add(900 * time.Millisecond),
			DurationMS:        900,
			FirstTokenMS:      240,
		}
		row.Attempts = observabilityBuildAuditAttempts(row, 1)
		rows = append(rows, row)
	}
	return rows
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

// observabilityBuildAuditAttempts 生成尝试行：多尝试样本的中间尝试失败
// （success_after_retry 的含义），最后一次尝试与主行状态一致。
func observabilityBuildAuditAttempts(row observabilityAuditRow, count int) []observabilityAuditAttempt {
	if count <= 0 {
		return nil
	}
	attempts := make([]observabilityAuditAttempt, 0, count)
	for index := 0; index < count; index++ {
		attempt := observabilityAuditAttempt{
			ID:         fmt.Sprintf("%s_attempt_%d", row.ID, index+1),
			Index:      index + 1,
			AccountID:  row.AccountID,
			Success:    row.Success,
			StatusCode: row.StatusCode,
			ErrorPhase: row.ErrorPhase,
			ErrorCode:  row.ErrorCode,
			Message:    row.ErrorMessage,
			StartedAt:  row.StartedAt.Add(time.Duration(index) * 400 * time.Millisecond),
			EndedAt: row.StartedAt.Add(time.Duration(index)*400*time.Millisecond +
				time.Duration(row.DurationMS/count)*time.Millisecond),
			DurationMS: row.DurationMS / count,
		}
		if index < count-1 {
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

// observabilityAuditLogColumns 把样本映射成 audit_logs 的列集合。布尔列写 Go
// bool（DDL boolean），时间列写 time.Time（timestamptz），体积列写 int（bigint）。
func observabilityAuditLogColumns(row observabilityAuditRow) map[string]any {
	statusCode := any(nil)
	if row.StatusCode > 0 {
		statusCode = row.StatusCode
	}
	return map[string]any{
		"id": row.ID, "trace_id": row.TraceID, "traffic_source": row.TrafficSource,
		"system_account_id": nullString(row.SystemAccountID), "api_key_id": nullString(row.APIKeyID),
		"group_id": nullString(row.GroupID), "account_id": nullString(row.AccountID),
		"session_id": nullString(row.SessionID), "session_client_type": nullString(row.SessionClientType),
		"provider_code": nullString(row.ProviderCode),
		"method":        row.Method, "path": row.Path,
		"model": nullString(row.Model), "upstream_model": nullString(row.UpstreamModel),
		"pricing_model":            nullString(row.PricingModel),
		"model_mapping_applied":    row.MappingApplied,
		"model_mapping_source":     nullString(row.MappingSource),
		"source_endpoint_family":   nullString(row.Family),
		"upstream_endpoint_family": nullString(row.Family),
		"stream":                   row.Stream,
		"client_ip":                nullString(row.ClientIP),
		"user_agent":               nullString(row.UserAgent),
		"audit_outcome":            row.Outcome,
		"success":                  row.Success,
		"final_status_code":        statusCode,
		"error_phase":              nullString(row.ErrorPhase),
		"error_code":               nullString(row.ErrorCode),
		"error_message":            nullString(row.ErrorMessage),
		"sample_bucket":            row.SampleBucket,
		"sample_reason":            row.SampleReason,
		"attempt_count":            len(row.Attempts),
		"payload_count":            len(row.Parts),
		"raw_payload_bytes":        row.RawPayloadBytes,
		"compressed_payload_bytes": row.CompressedPayloadBytes,
		"compression_saved_bytes":  row.RawPayloadBytes - row.CompressedPayloadBytes,
		"error_group_id":           nil,
		"capture_status":           observabilityAuditCaptureStatus(row),
		"lifecycle_status":         "finalized",
		"started_at":               row.StartedAt,
		"ended_at":                 row.EndedAt,
		"duration_ms":              row.DurationMS,
		"http_completed_at":        row.EndedAt,
		"http_duration_ms":         row.DurationMS,
		"first_token_ms":           row.FirstTokenMS,
		"created_at":               row.EndedAt,
	}
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
	statusCode := any(nil)
	if attempt.StatusCode > 0 {
		statusCode = attempt.StatusCode
	}
	endedAt := any(nil)
	if !attempt.EndedAt.IsZero() {
		endedAt = attempt.EndedAt
	}
	return map[string]any{
		"id": attempt.ID, "audit_log_id": row.ID, "attempt_index": attempt.Index,
		"account_id":                       nullString(attempt.AccountID),
		"account_owner_system_account_id":  nullString(row.SystemAccountID),
		"group_id":                         nullString(row.GroupID),
		"provider_code":                    nullString(row.ProviderCode),
		"attempt_model":                    nullString(row.Model),
		"attempt_upstream_model":           nullString(row.UpstreamModel),
		"attempt_pricing_model":            nullString(row.PricingModel),
		"attempt_model_mapping_applied":    row.MappingApplied,
		"attempt_model_mapping_source":     nullString(row.MappingSource),
		"attempt_source_endpoint_family":   nullString(row.Family),
		"attempt_upstream_endpoint_family": nullString(row.Family),
		"upstream_method":                  row.Method,
		"upstream_url":                     observabilityAuditUpstreamURL(row),
		"upstream_status_code":             statusCode,
		"success":                          attempt.Success,
		"error_phase":                      nullString(attempt.ErrorPhase),
		"error_code":                       nullString(attempt.ErrorCode),
		"error_message":                    nullString(attempt.Message),
		"started_at":                       attempt.StartedAt,
		"ended_at":                         endedAt,
		"duration_ms":                      attempt.DurationMS,
	}
}

// observabilityAuditUpstreamURL 给尝试行一个真实形态的上游 URL（不回显任何密钥）。
func observabilityAuditUpstreamURL(row observabilityAuditRow) string {
	switch row.Family {
	case "images":
		return "https://api.openai.example.invalid/v1/images/generations"
	case "audio":
		return "https://api.openai.example.invalid/v1/audio/speech"
	case "videos":
		return "https://api.openai.example.invalid/v1/videos"
	case "models":
		return "https://api.openai.example.invalid/v1/models"
	case "responses":
		return "https://api.openai.example.invalid/v1/responses"
	default:
		return "https://api.openai.example.invalid/v1/chat/completions"
	}
}

// observabilityPlanAuditBlobs 预计算全部 payload blob（内容寻址去重）并回填行内
// payload 体积：audit_logs 的 payload_count / raw_payload_bytes 由实际 payload
// 计算，同一行的列与 blob 元数据必须自洽。
func observabilityPlanAuditBlobs(rows []observabilityAuditRow) []observabilityBlob {
	byID := map[string]*observabilityBlob{}
	order := make([]string, 0, len(rows)*3)
	for index := range rows {
		row := &rows[index]
		raw, compressed := 0, 0
		for _, part := range row.Parts {
			blob := observabilityBuildBlob("application/json", observabilityAuditPayloadBody(part, *row))
			raw += blob.RawSize
			compressed += blob.CompressedSize
			if _, ok := byID[blob.ID]; ok {
				continue
			}
			candidate := blob
			byID[blob.ID] = &candidate
			order = append(order, blob.ID)
		}
		row.RawPayloadBytes = raw
		row.CompressedPayloadBytes = compressed
	}
	blobs := make([]observabilityBlob, 0, len(order))
	for _, id := range order {
		blobs = append(blobs, *byID[id])
	}
	sort.Slice(blobs, func(left, right int) bool { return blobs[left].ID < blobs[right].ID })
	return blobs
}

// observabilityAuditPayloadRefColumns 映射 audit_payload_refs 列。body_sha256 与
// blob 元数据同源；attempt_id 指向该行最后一次尝试（可空 FK，非空时目标行必须
// 已写入）。
func observabilityAuditPayloadRefColumns(row observabilityAuditRow, partType string, sequence int) map[string]any {
	blob := observabilityBuildBlob("application/json", observabilityAuditPayloadBody(partType, row))
	attemptID := any(nil)
	if len(row.Attempts) > 0 {
		attemptID = row.Attempts[len(row.Attempts)-1].ID
	}
	return map[string]any{
		"id":                    fmt.Sprintf("%s_payload_%d", row.ID, sequence),
		"audit_log_id":          row.ID,
		"attempt_id":            attemptID,
		"part_type":             partType,
		"sequence_index":        sequence,
		"content_type":          blob.ContentType,
		"body_blob_id":          blob.ID,
		"body_sha256":           blob.SHA256,
		"raw_size_bytes":        blob.RawSize,
		"compressed_size_bytes": blob.CompressedSize,
		"capture_status":        "complete",
		"created_at":            row.EndedAt,
	}
}

// observabilityWriteAuditErrorGroups 按 (错误码, 阶段, 状态码) 聚合失败样本，写
// audit_error_groups 并把 group id 回填到对应的 audit_logs 行。
func observabilityWriteAuditErrorGroups(ctx context.Context, e *env, tx *sql.Tx, rows []observabilityAuditRow, now time.Time) (int, error) {
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
		if err := e.insertMapTx(ctx, tx, obsTable("audit_error_groups"), map[string]any{
			"id": groupID, "fingerprint": fingerprint,
			"window_started_at": first.StartedAt, "window_ended_at": last.EndedAt,
			"system_account_id": nullString(first.SystemAccountID), "api_key_id": nullString(first.APIKeyID),
			"group_id": nullString(first.GroupID), "account_id": nullString(first.AccountID),
			"provider_code": nullString(first.ProviderCode),
			"path":          nullString(first.Path), "model": nullString(first.Model),
			"status_code": key.status, "error_phase": nullString(key.phase), "error_code": key.code,
			"error_type":          observabilityAuditErrorType(key.phase),
			"request_fingerprint": fingerprint + "-request",
			"error_fingerprint":   fingerprint,
			"count":               len(members),
			"first_event_id":      first.ID, "last_event_id": last.ID, "sample_event_id": first.ID,
			"last_message": first.ErrorMessage,
			"created_at":   first.StartedAt, "updated_at": now,
		}); err != nil {
			return 0, err
		}
		for _, member := range members {
			if _, err := tx.ExecContext(ctx,
				"UPDATE "+obsTable("audit_logs")+" SET error_group_id = $1 WHERE id = $2", groupID, member.ID); err != nil {
				return 0, fmt.Errorf("回填 %s: %w", obsTable("audit_logs"), err)
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
// 操作日志（operation_log 属主存储：operation_logs / targets / viewers /
// summary_search_terms）
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

// observabilityOperationChange 对应 changes_json 的元素。
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

// observabilitySeedOperationLogs 写入操作日志及其受影响对象、可见者与摘要检索词。
func observabilitySeedOperationLogs(ctx context.Context, e *env, refs observabilityReferences, now time.Time, counts map[string]int) error {
	ready, err := observabilityTablesReady(ctx, e, "操作日志", "operation_logs",
		"operation_log_targets", "operation_log_viewers", "operation_log_summary_search_terms")
	if err != nil {
		return err
	}
	if !ready {
		return nil
	}
	rows := observabilityBuildOperationRows(e, refs, now)
	return e.tx(ctx, func(tx *sql.Tx) error {
		logRows, targetRows, viewerRows, termRows := 0, 0, 0, 0
		for _, row := range rows {
			if err := e.insertMapTx(ctx, tx, obsTable("operation_logs"),
				observabilityOperationLogColumns(row)); err != nil {
				return err
			}
			logRows++
			for index, target := range row.Targets {
				if err := e.insertMapTx(ctx, tx, obsTable("operation_log_targets"), map[string]any{
					"id":                             fmt.Sprintf("%s_target_%d", row.ID, index+1),
					"operation_log_id":               row.ID,
					"target_type":                    target.TargetType,
					"target_id":                      nullString(target.TargetID),
					"target_name":                    nullString(target.TargetName),
					"target_owner_system_account_id": nullString(target.TargetOwner),
					"relation":                       target.Relation,
					"created_at":                     row.CreatedAt,
				}); err != nil {
					return err
				}
				targetRows++
			}
			for _, viewer := range row.Viewers {
				if err := e.insertMapTx(ctx, tx, obsTable("operation_log_viewers"), map[string]any{
					"operation_log_id":  row.ID,
					"system_account_id": viewer.SystemAccountID,
					"visibility_reason": viewer.Reason,
					"detail_level":      viewer.DetailLevel,
					"created_at":        row.CreatedAt,
				}); err != nil {
					return err
				}
				viewerRows++
			}
			for _, term := range observabilitySearchTerms(row.Summary) {
				if err := e.insertMapTx(ctx, tx, obsTable("operation_log_summary_search_terms"), map[string]any{
					"operation_log_id": row.ID,
					"term":             term,
					"created_at":       row.CreatedAt,
				}); err != nil {
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
		items := make([]observabilityResource, 0, len(refs.Users))
		for _, user := range refs.Users {
			items = append(items, observabilityResource{ID: user.ID, Name: user.DisplayName})
		}
		return observabilityPickResource(items, index)
	default:
		return observabilityPickResource(refs.Accounts, index)
	}
}

// observabilityOperationViewers 复刻 gateway operationlog 的可见者推导：targeted
// 时操作者自己可见，资源归属人另有一条可见记录（角色 admin 用管理员代管原因）。
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

// observabilityOperationLogColumns 映射 operation_logs 列。changes_json /
// metadata_json 是 jsonb 列，写 JSON 文本（pgx 按语句描述的 jsonb 参数编码）。
func observabilityOperationLogColumns(row observabilityOperationRow) map[string]any {
	changes := make([]map[string]any, 0, len(row.Changes))
	for _, change := range row.Changes {
		changes = append(changes, map[string]any{
			"field": change.Field, "label": change.Label,
			"before": change.Before, "after": change.After,
		})
	}
	changesJSON, err := json.Marshal(changes)
	if err != nil {
		panic(fmt.Sprintf("mockdata 操作日志 changes 编码失败: %v", err))
	}
	metadataJSON, err := json.Marshal(row.Metadata)
	if err != nil {
		panic(fmt.Sprintf("mockdata 操作日志 metadata 编码失败: %v", err))
	}
	statusCode := any(nil)
	if row.StatusCode > 0 {
		statusCode = row.StatusCode
	}
	columns := map[string]any{
		"id": row.ID, "actor_system_account_id": row.ActorSystemAccountID,
		"actor_role": row.ActorRole,
		"mode":       row.Mode, "module": row.Module, "action": row.Action,
		"operation_key": row.OperationKey, "resource_type": row.ResourceType,
		"resource_id": nullString(row.ResourceID), "resource_name": nullString(row.ResourceName),
		"summary": row.Summary, "detail_level": row.DetailLevel,
		"visibility_scope": row.VisibilityScope,
		"changes_json":     string(changesJSON), "metadata_json": string(metadataJSON),
		"status_code": statusCode,
		"created_at":  row.CreatedAt,
	}
	observabilitySetText(columns, map[string]string{
		"trace_id":                          row.TraceID,
		"actor_username":                    row.ActorUsername,
		"actor_display_name":                row.ActorDisplayName,
		"operation_scope_system_account_id": row.ScopeSystemAccountID,
		"method":                            row.Method,
		"path":                              row.Path,
		"client_ip":                         row.ClientIP,
		"user_agent":                        row.UserAgent,
	})
	return columns
}

// observabilitySearchTerms 生成摘要检索词：与 gateway F4 searchTerms 同一语义
// （小写、非字母数字折叠成单空格、展开子串），但只展开到
// observabilitySearchTermMaxRunes 并限制总条数。
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

// observabilityNormalizeSearchText 与 gateway F4 normalizeSearchText 对齐（不含
// NFKC：造数样本全部是 ASCII 与常用汉字，NFKC 折叠不会改变结果）。
func observabilityNormalizeSearchText(value string) string {
	var builder strings.Builder
	needsSpace := false
	for _, char := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case observabilityIsSearchRune(char):
			builder.WriteRune(char)
			needsSpace = false
		case builder.Len() > 0 && !needsSpace:
			builder.WriteByte(' ')
			needsSpace = true
		}
	}
	return strings.TrimSpace(builder.String())
}

// observabilityIsSearchRune 判断字符是否保留在检索词里（字母或数字）。
func observabilityIsSearchRune(char rune) bool {
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
// 运行日志（runtime_log 属主存储：runtime_logs / runtime_log_facet_summary /
// runtime_log_level_facets / runtime_log_event_facets）
// ---------------------------------------------------------------------------

// observabilityRuntimeLine 是一条运行日志索引行（PG 形态没有日志目录契约，只
// 写索引行与 raw_json 文本，不写物理文件与文件游标）。
type observabilityRuntimeLine struct {
	FileName     string
	At           time.Time
	Level        string
	TraceID      string
	Event        string
	Message      string
	Fields       map[string]any
	ErrorMessage string
}

// observabilityRuntimeServerEvents 是 server 角色的请求 summary 事件序列：事件
// 名与字段取自 gateway/internal/kernel 的 http_request_completed / closed。
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
	// 媒体请求日志样本（媒体契约 §3.4）：同步音频与视频任务创建。
	{Level: "info", Event: "http_request_completed", Message: "HTTP 请求已结束", Method: "POST", Path: "/v1/audio/speech", Status: 200, Duration: 2210},
	{Level: "info", Event: "http_request_completed", Message: "HTTP 请求已结束", Method: "POST", Path: "/v1/videos", Status: 200, Duration: 310},
}

// observabilityRuntimeWorkerEvents 是 stats-worker 角色的定时任务事件序列：事件
// 名与字段取自 jobs/internal/retention 与 jobs/internal/usagespooldrain。
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
		Error:  CleanupNamePrefix + "模拟批次写入失败",
	},
	{
		Level: "error", Event: "usage_record_spool_flush_failed", Message: "usage spool 投递失败，已保留文件等待重试",
		Fields: map[string]any{"usageRecordId": CleanupIDPrefix + "usage_spool_record", "attemptCount": 3},
		Error:  CleanupNamePrefix + "模拟 spool 投递失败",
	},
}

// observabilityBuildRuntimeLines 生成近 Days 天的运行日志行（server / worker 两
// 个逻辑文件各一份），按文件名与时间排序保证写入顺序稳定。
func observabilityBuildRuntimeLines(e *env, now time.Time) []observabilityRuntimeLine {
	days := e.options.Days
	lines := make([]observabilityRuntimeLine, 0, days*8)
	index := 0
	for _, dayIndex := range observabilityDayIndexes(days) {
		dayStart := observabilityDayStart(now).AddDate(0, 0, -(days - 1 - dayIndex))
		for rowIndex := 0; rowIndex < 4; rowIndex++ {
			sample := observabilityRuntimeServerEvents[(dayIndex*4+rowIndex)%len(observabilityRuntimeServerEvents)]
			at := observabilitySpread(now, dayStart, rowIndex, 5)
			lines = append(lines, observabilityRuntimeLine{
				FileName: observabilityServerLogFile,
				At:       at, Level: sample.Level,
				TraceID: CleanupTracePrefix + "runtime-" + at.Format("20060102") + "-" + fmt.Sprintf("%03d", index+1),
				Event:   sample.Event, Message: sample.Message,
				Fields: map[string]any{
					"requestId":         CleanupIDPrefix + "request_" + fmt.Sprintf("%03d", index+1),
					"method":            sample.Method,
					"path":              sample.Path,
					"originalUrl":       sample.Path,
					"clientIp":          fmt.Sprintf("172.20.%d.%d", index%16, 20+(index%180)),
					"durationMs":        sample.Duration,
					"responseCommitted": true,
					"statusCode":        sample.Status,
					"failureScope":      sample.Failure,
				},
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
				FileName: observabilityWorkerLogFile,
				At:       at, Level: sample.Level, Event: sample.Event, Message: sample.Message,
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
// 固定键在前才能与真实日志在字符层面同形；字段值仍走 json.Marshal 保证转义正确。
func observabilityRuntimeLineJSON(line observabilityRuntimeLine) (string, error) {
	var builder strings.Builder
	builder.WriteString(`{"time":`)
	if err := observabilityWriteJSONValue(&builder, line.At.Format(isoMillisLayout)); err != nil {
		return "", err
	}
	builder.WriteString(`,"level":`)
	if err := observabilityWriteJSONValue(&builder, strings.ToUpper(line.Level)); err != nil {
		return "", err
	}
	builder.WriteString(`,"msg":`)
	if err := observabilityWriteJSONValue(&builder, line.Message); err != nil {
		return "", err
	}
	if line.Event != "" {
		builder.WriteString(`,"event":`)
		if err := observabilityWriteJSONValue(&builder, line.Event); err != nil {
			return "", err
		}
	}
	if line.TraceID != "" {
		builder.WriteString(`,"traceId":`)
		if err := observabilityWriteJSONValue(&builder, line.TraceID); err != nil {
			return "", err
		}
	}
	keys := make([]string, 0, len(line.Fields))
	for key := range line.Fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		builder.WriteString(",")
		if err := observabilityWriteJSONValue(&builder, key); err != nil {
			return "", err
		}
		builder.WriteString(":")
		if err := observabilityWriteJSONValue(&builder, line.Fields[key]); err != nil {
			return "", err
		}
	}
	if line.ErrorMessage != "" {
		builder.WriteString(`,"err":{"message":`)
		if err := observabilityWriteJSONValue(&builder, line.ErrorMessage); err != nil {
			return "", err
		}
		builder.WriteString("}")
	}
	builder.WriteString("}")
	return builder.String(), nil
}

// observabilityWriteJSONValue 把值编码成 JSON 片段写入 builder（编码失败即错）。
func observabilityWriteJSONValue(builder *strings.Builder, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("编码运行日志字段 %v: %w", value, err)
	}
	builder.Write(encoded)
	return nil
}

// observabilityRuntimeLogID 生成索引行 id：带清理标识 + 内容摘要。不复刻 jobs
// runtimelog 的 rtlog_<identity:offset> 算法：文件身份（inode / file index）只有
// 索引进程能拿到；带清理标识的 id 保证清理扫描收敛，重复执行不叠加。
func observabilityRuntimeLogID(line observabilityRuntimeLine, raw string) string {
	sum := sha256.Sum256([]byte(line.FileName + "\x00" + raw))
	return CleanupIDPrefix + "rtlog_" + hex.EncodeToString(sum[:])[:32]
}

// observabilitySeedRuntimeLogs 写运行日志索引行并重建分面（facet 汇总表）。
//
// 时间列逐列对照 DDL：runtime_logs.time / created_at 与分面表的
// earliest_time / latest_time / updated_at 都是 timestamptz，全部写 time.Time。
// runtime_log_file_cursors 是文件读取游标（属主游标族）：PG 形态不写物理日志
// 文件，造数不伪造读取进度，该表保持为空（覆盖白名单按属主游标族放行）。
func observabilitySeedRuntimeLogs(ctx context.Context, e *env, now time.Time, counts map[string]int) error {
	ready, err := observabilityTablesReady(ctx, e, "运行日志", "runtime_logs",
		"runtime_log_facet_summary", "runtime_log_level_facets", "runtime_log_event_facets")
	if err != nil {
		return err
	}
	if !ready {
		return nil
	}
	lines := observabilityBuildRuntimeLines(e, now)
	rendered := make([]string, 0, len(lines))
	for _, line := range lines {
		raw, err := observabilityRuntimeLineJSON(line)
		if err != nil {
			return err
		}
		rendered = append(rendered, raw)
	}
	return e.tx(ctx, func(tx *sql.Tx) error {
		for i, line := range lines {
			if err := e.insertMapTx(ctx, tx, obsTable("runtime_logs"), map[string]any{
				"id":            observabilityRuntimeLogID(line, rendered[i]),
				"log_file":      line.FileName,
				"time":          line.At,
				"level":         strings.ToLower(line.Level),
				"raw_json":      rendered[i],
				"trace_id":      nullString(line.TraceID),
				"event":         nullString(line.Event),
				"message":       nullString(line.Message),
				"error_message": nullString(line.ErrorMessage),
				"created_at":    line.At,
			}); err != nil {
				return err
			}
		}
		summaryRows, levelRows, eventRows, err := observabilityRebuildRuntimeFacets(ctx, e, tx, now)
		if err != nil {
			return err
		}
		observabilityAdd(counts, "runtimeLogs", len(lines))
		observabilityAdd(counts, "runtimeLogFacetSummary", summaryRows)
		observabilityAdd(counts, "runtimeLogLevelFacets", levelRows)
		observabilityAdd(counts, "runtimeLogEventFacets", eventRows)
		return nil
	})
}

// observabilityRebuildRuntimeFacets 从 runtime_logs 重算分面表并返回写入行数。
//
// 为什么重算而不是增量累加：分面是「索引行数的聚合视图」（total_count 与
// level / event 计数始终保持与表一致）；增量累加会在重复执行时重复计数，重算
// 既幂等，也不会破坏索引进程已记录的其它行。earliest / latest 同样从表里取，
// 避免造数把分面时间窗写歪。
func observabilityRebuildRuntimeFacets(ctx context.Context, e *env, tx *sql.Tx, now time.Time) (summaryRows, levelRows, eventRows int, err error) {
	var total int
	var earliest, latest sql.NullTime
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*), MIN(time), MAX(time) FROM "+obsTable("runtime_logs")).Scan(&total, &earliest, &latest); err != nil {
		return 0, 0, 0, fmt.Errorf("统计 %s: %w", obsTable("runtime_logs"), err)
	}
	if total == 0 {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM "+obsTable("runtime_log_facet_summary")+" WHERE bucket_key = $1",
			observabilityRuntimeFacetBucketKey); err != nil {
			return 0, 0, 0, fmt.Errorf("清理空分面汇总: %w", err)
		}
	} else {
		earliestValue, latestValue := observabilityNullTime(earliest), observabilityNullTime(latest)
		if err := e.insertMapTx(ctx, tx, obsTable("runtime_log_facet_summary"), map[string]any{
			"bucket_key":    observabilityRuntimeFacetBucketKey,
			"total_count":   total,
			"earliest_time": earliestValue,
			"latest_time":   latestValue,
			"updated_at":    now,
		}); err != nil {
			return 0, 0, 0, err
		}
		summaryRows = 1
	}
	if levelRows, err = observabilityRebuildRuntimeLevelFacets(ctx, e, tx, now); err != nil {
		return summaryRows, 0, 0, err
	}
	if eventRows, err = observabilityRebuildRuntimeEventFacets(ctx, e, tx, now); err != nil {
		return summaryRows, levelRows, 0, err
	}
	return summaryRows, levelRows, eventRows, nil
}

// observabilityRebuildRuntimeLevelFacets 重算 level 分面（并按当前级别集合清理）。
//
// 先收集后写入：insertMapTx 需要在同一事务连接上查询主键，不能在未读完的
// rows 迭代里再发查询（pgx 连接忙）。
func observabilityRebuildRuntimeLevelFacets(ctx context.Context, e *env, tx *sql.Tx, now time.Time) (int, error) {
	rows, err := tx.QueryContext(ctx,
		"SELECT level, COUNT(*) FROM "+obsTable("runtime_logs")+" GROUP BY level")
	if err != nil {
		return 0, fmt.Errorf("统计 %s level: %w", obsTable("runtime_logs"), err)
	}
	type levelCount struct {
		level string
		count int
	}
	levels := make([]levelCount, 0, 6)
	for rows.Next() {
		var item levelCount
		if err := rows.Scan(&item.level, &item.count); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("统计 %s level: %w", obsTable("runtime_logs"), err)
		}
		levels = append(levels, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("统计 %s level: %w", obsTable("runtime_logs"), err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("统计 %s level: %w", obsTable("runtime_logs"), err)
	}
	names := make([]string, 0, len(levels))
	for _, item := range levels {
		names = append(names, item.level)
	}
	for _, item := range levels {
		if err := e.insertMapTx(ctx, tx, obsTable("runtime_log_level_facets"), map[string]any{
			"bucket_key": observabilityRuntimeFacetBucketKey,
			"level":      item.level,
			"count":      item.count,
			"updated_at": now,
		}); err != nil {
			return 0, err
		}
	}
	return len(levels), observabilityPruneFacetRows(ctx, tx, "runtime_log_level_facets", "level", names)
}

// observabilityRebuildRuntimeEventFacets 重算 event 分面（含 latest_time）。
func observabilityRebuildRuntimeEventFacets(ctx context.Context, e *env, tx *sql.Tx, now time.Time) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT event, COUNT(*), MAX(time) FROM `+obsTable("runtime_logs")+`
		WHERE event IS NOT NULL AND event <> '' GROUP BY event`)
	if err != nil {
		return 0, fmt.Errorf("统计 %s event: %w", obsTable("runtime_logs"), err)
	}
	type eventCount struct {
		event  string
		count  int
		latest sql.NullTime
	}
	events := make([]eventCount, 0, 16)
	for rows.Next() {
		var item eventCount
		if err := rows.Scan(&item.event, &item.count, &item.latest); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("统计 %s event: %w", obsTable("runtime_logs"), err)
		}
		events = append(events, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("统计 %s event: %w", obsTable("runtime_logs"), err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("统计 %s event: %w", obsTable("runtime_logs"), err)
	}
	names := make([]string, 0, len(events))
	for _, item := range events {
		names = append(names, item.event)
	}
	for _, item := range events {
		if err := e.insertMapTx(ctx, tx, obsTable("runtime_log_event_facets"), map[string]any{
			"bucket_key":  observabilityRuntimeFacetBucketKey,
			"event":       item.event,
			"count":       item.count,
			"latest_time": observabilityNullTime(item.latest),
			"updated_at":  now,
		}); err != nil {
			return 0, err
		}
	}
	return len(events), observabilityPruneFacetRows(ctx, tx, "runtime_log_event_facets", "event", names)
}

// observabilityPruneFacetRows 删除当前集合之外的分面行（分面是表的聚合视图）。
func observabilityPruneFacetRows(ctx context.Context, tx *sql.Tx, table, column string, values []string) error {
	if len(values) == 0 {
		_, err := tx.ExecContext(ctx,
			"DELETE FROM "+obsTable(table)+" WHERE bucket_key = $1", observabilityRuntimeFacetBucketKey)
		if err != nil {
			return fmt.Errorf("清理分面 %s: %w", obsTable(table), err)
		}
		return nil
	}
	placeholders := make([]string, len(values))
	args := make([]any, 0, len(values)+1)
	args = append(args, observabilityRuntimeFacetBucketKey)
	for index, value := range values {
		placeholders[index] = fmt.Sprintf("$%d", index+2)
		args = append(args, value)
	}
	query := "DELETE FROM " + obsTable(table) + " WHERE bucket_key = $1 AND " +
		quotePGIdentifier(column) + " NOT IN (" + strings.Join(placeholders, ", ") + ")"
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("清理分面 %s: %w", obsTable(table), err)
	}
	return nil
}

// observabilityNullTime 把可空时间投影成 SQL NULL（PG 分面时间列是 timestamptz）。
func observabilityNullTime(value sql.NullTime) any {
	if !value.Valid {
		return nil
	}
	return value.Time
}

// ---------------------------------------------------------------------------
// 公开接口日志（juhe_dataset 维护面：public_api_logs，maintenance DDL 的
// text 时间 + integer 布尔列型）
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

// observabilityPublicSource 是一条可用于公开接口日志的来源 + Token 组合。
// Kind 取 primary / readonly / test，决定样本轮转时使用哪一类来源。
type observabilityPublicSource struct {
	SourceID   string
	SourceName string
	TokenID    string
	TokenName  string
	Prefix     string
	IsTest     bool
	Kind       string
}

// observabilityPublicSources 挑出可用的来源 + Token 组合：一条正式来源、一条
// 只读来源、以及内置测试来源（各自都要有可用 Token）。
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

// observabilityPublicSourceOfKind 按来源类别取第一条。
func observabilityPublicSourceOfKind(sources []observabilityPublicSource, kind string) (observabilityPublicSource, bool) {
	for _, source := range sources {
		if source.Kind == kind {
			return source, true
		}
	}
	return observabilityPublicSource{}, false
}

// observabilityPublicStatus 复刻状态分布：401 / 403 / 429 / 400 / 500 按固定取模
// 命中，其余成功（POST 写入额外命中 201）。
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
// aipublic 真实错误码（401 缺 token、403 缺 scope、429 限流）。400 / 500 由
// kernel 错误信封返回，只有 message 没有 code，因此错误码留空。
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

// observabilitySeedPublicAPILogs 写 public_api_logs（maintenance DDL：时间列是
// text、success / is_test_token 是 integer 0/1——与属主表逐列区分）。
//
// 来源与 Token 全部引用 business 域已造的真实行；一条都查不到时跳过并报告
// （不伪造不存在的来源系统）。
func observabilitySeedPublicAPILogs(ctx context.Context, e *env, refs observabilityReferences, now time.Time, counts map[string]int) error {
	ready, err := observabilityTablesReady(ctx, e, "公开接口日志", "public_api_logs")
	if err != nil {
		return err
	}
	if !ready {
		return nil
	}
	sources := observabilityPublicSources(refs)
	if len(sources) == 0 {
		e.logger.Warn("mockdata 公开接口日志跳过：business 库没有可用的外部来源系统或 Token",
			"table", obsTable("public_api_logs"))
		return nil
	}
	rows := observabilityBuildPublicAPILogRows(e, sources, now)
	return e.tx(ctx, func(tx *sql.Tx) error {
		for _, columns := range rows {
			if err := e.insertMapTx(ctx, tx, obsTable("public_api_logs"), columns); err != nil {
				return err
			}
		}
		observabilityAdd(counts, "publicApiLogs", len(rows))
		return nil
	})
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
			// maintenance DDL 列型：text 时间列沿用 UTC 毫秒 ISO 形态，布尔写
			// 0/1 integer（与属主表的 timestamptz / boolean 区分）。
			columns := map[string]any{
				"id":                      observabilityStamp(CleanupIDPrefix+"public_api_log_", at, index),
				"trace_id":                CleanupTracePrefix + "public-api-" + at.Format("20060102") + "-" + fmt.Sprintf("%03d", index+1),
				"source_ref_id":           source.SourceID,
				"source_name":             source.SourceName,
				"token_id":                nullString(source.TokenID),
				"token_name":              nullString(source.TokenName),
				"token_prefix":            nullString(source.Prefix),
				"is_test_token":           boolInt(source.IsTest),
				"method":                  endpoint.Method,
				"path":                    endpoint.Path,
				"query_string":            nullString(endpoint.Query),
				"client_ip":               fmt.Sprintf("172.20.%d.%d", index%16, 20+(index%180)),
				"user_agent":              observabilityPublicUserAgent(index),
				"status_code":             status,
				"success":                 boolInt(success),
				"duration_ms":             duration,
				"request_size_bytes":      len(requestJSON),
				"response_size_bytes":     len(responseJSON),
				"request_capture_status":  observabilityPublicRequestCaptureStatus(index, endpoint),
				"response_capture_status": observabilityPublicResponseCaptureStatus(index, success),
				"request_data_json":       requestJSON,
				"response_data_json":      responseJSON,
				"error_code":              nullString(observabilityPublicErrorCode(status)),
				"error_message":           nullString(observabilityPublicErrorMessage(status)),
				"started_at":              at.UTC().Format(isoMillisLayout),
				"ended_at":                ended.UTC().Format(isoMillisLayout),
				"created_at":              ended.UTC().Format(isoMillisLayout),
			}
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
	if index%19 == 0 {
		return "truncated"
	}
	if endpoint.Write {
		return "complete"
	}
	return "empty"
}

// observabilityPublicResponseCaptureStatus 同上，作用于响应体。截断样本用 11
// 取模而不是其它错误状态的取模：11 与它们互质，能保证成功样本里真的有截断
// （响应体只可能在成功响应里被截断）。
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
// 后台记录清理目标（juhe_dataset 维护面，maintenance DDL 的 text 时间列型）
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
		ErrorMessage:      CleanupNamePrefix + "模拟账号相关记录清理事务冲突",
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
		ErrorMessage:  CleanupNamePrefix + "模拟 API Key 清理事务冲突",
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

// observabilitySeedRecordCleanupTargets 写 dataset 面的两张清理目标表（重试队列
// 样本：被 jobs 的清理重试任务消费后即空，覆盖白名单允许为空，造数仍提供排队
// 态样本供页面验收）。
func observabilitySeedRecordCleanupTargets(ctx context.Context, e *env, refs observabilityReferences, now time.Time, counts map[string]int) error {
	ready, err := observabilityTablesReady(ctx, e, "后台清理目标",
		"account_record_cleanup_targets", "api_key_record_cleanup_targets")
	if err != nil {
		return err
	}
	if !ready {
		return nil
	}
	owner := observabilityPickUser(refs.Users, 0)
	return e.tx(ctx, func(tx *sql.Tx) error {
		accountRows, apiKeyRows := 0, 0
		for index, sample := range observabilityAccountCleanupTargets {
			createdAt := now.AddDate(0, 0, -(index + 1))
			columns := map[string]any{
				"account_id":               CleanupIDPrefix + sample.Suffix,
				"system_account_id":        owner.ID,
				"related_account_ids_json": observabilityJSONOrDefault(sample.RelatedJSON),
				"authorization_ids_json":   observabilityJSONOrDefault(sample.AuthorizationJSON),
				"team_scope_ids_json":      observabilityJSONOrDefault(sample.TeamScopeJSON),
				"created_at":               createdAt.UTC().Format(isoMillisLayout),
				"updated_at":               now.UTC().Format(isoMillisLayout),
				"attempt_count":            sample.AttemptCount,
				"last_blocked_reason":      sample.BlockedReason,
			}
			if sample.AttemptCount > 0 {
				columns["last_attempt_at"] = now.Add(-time.Duration(index+2) * time.Hour).UTC().Format(isoMillisLayout)
			}
			columns["last_error_message"] = nullString(sample.ErrorMessage)
			if err := e.insertMapTx(ctx, tx, obsTable("account_record_cleanup_targets"), columns); err != nil {
				return err
			}
			accountRows++
		}
		for index, sample := range observabilityAPIKeyCleanupTargets {
			createdAt := now.AddDate(0, 0, -(index + 1))
			columns := map[string]any{
				"api_key_id":          CleanupIDPrefix + sample.Suffix,
				"system_account_id":   owner.ID,
				"created_at":          createdAt.UTC().Format(isoMillisLayout),
				"updated_at":          now.UTC().Format(isoMillisLayout),
				"attempt_count":       sample.AttemptCount,
				"last_blocked_reason": sample.BlockedReason,
			}
			if sample.AttemptCount > 0 {
				columns["last_attempt_at"] = now.Add(-time.Duration(index+3) * time.Hour).UTC().Format(isoMillisLayout)
			}
			columns["last_error_message"] = nullString(sample.ErrorMessage)
			if err := e.insertMapTx(ctx, tx, obsTable("api_key_record_cleanup_targets"), columns); err != nil {
				return err
			}
			apiKeyRows++
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
