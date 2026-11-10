// `maintenance upgrade` 的 15 步骤注册表（升级兼容与迁移账本设计 §8/§10）。
// 每个步骤的 Apply 复用既有一次性命令的内部函数（单一实现），stdout/stderr
// 经 CaptureOutput 捕获后进 detail_json；Check 与 Verify 同源（本文件每个
// 步骤的 check 闭包同时作为 Satisfied 与 Verify 的实现）。
//
// revision 1..13 的分配即设计 §10 表序（revision 7 chat-account-only-binding
// 已随 AI 问答退役删除，序列 6 后直接 8；已执行库的账本行只记历史事实，
// 不受注册表删步影响）；golden 测试钉死
// (revision, step_id, implVersion) 有序对。低序步骤不得依赖高序产物（§8.4-3）。
// revision 13（account-schedulable-normalization，T91-3 / B1-10）是纯数据
// 归一：active 账户的派生 schedulable 收敛为 1。revision 20
// （retire-cross-protocol-entry-mappings，PLAN-20261106T162344524Z 批次C）是
// 跨协议入口映射退役：一个 Destructive 步骤按序执行归一→删除→删 schema 三个
// 阶段（账本 revision 主键 + 注册表唯一校验决定一个 revision 只能注册一个
// 步骤，阶段 ID 与逐阶段计数进 detail_json / stderr / dry-run 输出）。revision
// 22（retire-high-concurrency-groups，普通分组可选调度策略设计 §5）是
// high_concurrency 分组退役：一个 Destructive 步骤把 groups /
// group_authorization_settings 迁为 personal + 两键策略并按首成员重算超级分组
// 自身行，needs_review 行阻断（与 migrate-group-policy CLI 共用迁移内核）。
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/capabilitydomain"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/groupspolicymigration"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
)

// upgradeTargets 解析三个辅助 target 的连接串（env 优先，缺失回落 primary）。
// upgradeTargets 的所有域共用同一个 --dsn：J3a/J3b/J2 表与主库同实例，
// 不再支持维护专用的独立连接串（账号/密码差异由 PG 角色或 URL query 表达）。
func upgradeTargets(primaryDSN string) (j3a, j3b, balance string) {
	return primaryDSN, primaryDSN, primaryDSN
}

// openPG 打开 PG 连接（pgx stdlib 驱动在包内注册一次）。
func openPG(dsn string) (*sql.DB, error) {
	return sql.Open("pgx", dsn)
}

// callResult 捕获既有 *Result 函数的输出并返回退出码（0=成功）。
func callResult(fn func() int) (code int, output string) {
	stdoutText, stderrText := upgradepipeline.CaptureOutput(func() {
		code = fn()
	})
	return code, stdoutText + stderrText
}

// exitErr 把步骤内命令退出码转成错误（detail 携带捕获输出）。
func exitErr(stepID, output string, code int) error {
	tail := output
	if len(tail) > 4000 {
		tail = tail[len(tail)-4000:]
	}
	return fmt.Errorf("%s 命令退出码 %d；输出尾部: %s", stepID, code, tail)
}

// sqlScalar 执行单值查询。fresh 库语义（§9.4-6 先于 EnsurePostgres 求值）：
// 关系不存在（42P01）按 0 行处理——「表/列尚未建立」即「无可迁移数据」，
// 谓词据此返回未满足或空跑；结构由后续 EnsureAll 保证收敛。
func sqlScalar(ctx context.Context, dsn, query string, args ...any) (int, error) {
	db, err := openPG(dsn)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var value int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			return 0, nil
		}
		return 0, err
	}
	return value, nil
}

// sqlExecCount 执行幂等写语句并返回受影响行数（42P01 关系不存在按 0 行，
// 与 sqlScalar 的 fresh 库语义一致）。
func sqlExecCount(ctx context.Context, dsn, statement string) (int, error) {
	db, err := openPG(dsn)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	result, err := db.ExecContext(ctx, statement)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			return 0, nil
		}
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

// T91-3（R7-B B1-10 裁决）存量归一：schedulable 改为账户状态派生后，
// status='active' 的行必须可调度。只命中「active 且 schedulable=0」这一组合
// （历史手工关闭调度 / 旧版本 PATCH 传 false 的残留），不写 config_revision
// 与 updated_at——这不是用户配置变更，只是把存量收敛到派生值；语句幂等，
// 完成态零写入。
const (
	schedulableNormalizationPendingSQL = `SELECT count(*) FROM juhe_business.accounts
		WHERE status = 'active' AND schedulable = 0`
	schedulableNormalizationUpdateSQL = `UPDATE juhe_business.accounts SET schedulable = 1
		WHERE status = 'active' AND schedulable = 0`
)

// tableExists / columnExists 是 Check 谓词的结构探测原语。
func tableExists(ctx context.Context, dsn, schemaName, table string) (bool, error) {
	value, err := sqlScalar(ctx, dsn,
		`SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2`,
		schemaName, table)
	return value > 0, err
}

func columnExists(ctx context.Context, dsn, schemaName, table, column string) (bool, error) {
	value, err := sqlScalar(ctx, dsn,
		`SELECT count(*) FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2 AND column_name = $3`,
		schemaName, table, column)
	return value > 0, err
}

// ---------- revision 20：跨协议入口映射退役与 codex context schema 清退 ----------
//
// PLAN-20261106T162344524Z 批次C（2026-11-07 用户裁决：不支持跨协议模型映射，
// 强制客户端协议与上游协议一致）。一个 Destructive 步骤在 contract/all 相按序
// 执行三个阶段；每阶段独立幂等、中断重跑安全（阶段 0/1 按库内现值收敛，阶段
// 2 是 IF EXISTS 空操作安全）：
//  0. normalize-legacy-entry-mapping-families：两侧按反向别名表归一历史
//     token（anthropic_messages→messages、gemini_generate_content→
//     generate_content、gemini_stream_generate→stream_generate_content）。
//     归一前先处理 (account_id, source_model, source_endpoint_family) 唯一键
//     冲突：canonical 行（现行词表 token）已存在时删除 legacy 重复行
//     （canonical 优先），禁止 UPDATE 撞唯一键；Gemini legacy pair
//     （gemini_stream_generate → gemini_generate_content）归一后即现行写侧
//     合法 pair（协议等价，保留），报告中单列保留数；未知 token 不改写，计数
//     进 dry-run/detail 报告。
//  1. retire-cross-protocol-entry-mappings：删除规范化后仍非合规的行——
//     normalized(source) <> normalized(target) 且非 normalized(source) =
//     'stream_generate_content' AND normalized(target) = 'generate_content'
//     （normalized() 以显式 CASE 表达，不依赖运行时词表隐式转换）。
//  2. retire-codex-context-schema：DROP SCHEMA IF EXISTS juhe_codex_context
//     CASCADE（4 表 9 索引；磁盘历史 codex-context/ 目录由部署文档要求人工
//     备份后清理，不在本步骤范围）。

// entryMappingLegacyFamilyTokenList 是反向别名表的键（现行写侧词表之外的
// 历史 family token）；entryMappingCurrentFamilyTokenList 是现行 7 协议词表。
// 两者均为包内常量，仅供 SQL 文本拼接，无注入面。
const entryMappingLegacyFamilyTokenList = `'anthropic_messages','gemini_generate_content','gemini_stream_generate'`

const entryMappingCurrentFamilyTokenList = `'chat_completions','responses','messages','generate_content','stream_generate_content','video_generation','tts'`

// entryMappingFamilyNormalizeExpr 生成单列的显式 CASE 归一投影（计划裁决：
// normalized() 必须显式表达）。column 为包内拼装的固定列名/别名。
func entryMappingFamilyNormalizeExpr(column string) string {
	return `CASE ` + column + `
				 WHEN 'anthropic_messages' THEN 'messages'
				 WHEN 'gemini_generate_content' THEN 'generate_content'
				 WHEN 'gemini_stream_generate' THEN 'stream_generate_content'
				 ELSE ` + column + ` END`
}

// entryMappingConflictPredicate 是「legacy 源族行与既有 canonical 行在
// (account_id, source_model, 归一后源族) 上冲突」的 EXISTS 片段。调用方必须
// 已把外层行限定为 source_endpoint_family ∈ 历史 token（canonical 行的自匹配
// 不可能：CASE(legacy) ≠ legacy）。outerRef 为外层行的列限定前缀。
func entryMappingConflictPredicate(outerRef string) string {
	return `EXISTS (
					SELECT 1 FROM juhe_business.account_entry_mappings canonical
					 WHERE canonical.account_id = ` + outerRef + `account_id
					   AND canonical.source_model = ` + outerRef + `source_model
					   AND canonical.source_endpoint_family = ` + entryMappingFamilyNormalizeExpr(outerRef+`source_endpoint_family`) + `)`
}

// entryMappingRetirePredicate 是阶段 1 的删除谓词：归一后仍非合规（跨族且非
// Gemini stream 归一 pair）。Satisfied / Apply / Verify 共用本片段（同源）。
func entryMappingRetirePredicate() string {
	source := entryMappingFamilyNormalizeExpr(`source_endpoint_family`)
	target := entryMappingFamilyNormalizeExpr(`target_endpoint_family`)
	return source + ` <> ` + target + `
				 AND NOT (` + source + ` = 'stream_generate_content'
				          AND ` + target + ` = 'generate_content')`
}

// entryMigrationCounters 是三阶段的只读计数投影（dry-run / Apply 预检共用；
// 计数语义模拟「阶段 0 → 1 → 2」执行后的库态：冲突行计入删除、归一计数排除
// 冲突行、阶段 1 计数按归一投影求值）。
type entryMigrationCounters struct {
	LegacyTokenRows      int            // 两侧仍带历史 token 的行数（阶段 0 完成态谓词）
	ConflictsByAlias     map[string]int // 唯一键冲突待删（按历史别名）
	ConflictRows         int
	NormalizedSource     map[string]int // 待归一 source 侧（按历史别名，排除冲突行）
	NormalizedTarget     map[string]int // 待归一 target 侧（按历史别名）
	PendingUpdateRows    int            // 阶段 0 预计 UPDATE 行数
	GeminiLegacyPairKept int            // gemini_stream_generate → gemini_generate_content 归一后保留数
	UnknownTokens        map[string]int // 归一后仍不在现行词表的 token（不改写，只报告）
	NonCompliantRows     int            // 阶段 1 待删行数（完成态谓词）
	NonCompliantByPair   map[string]int // 阶段 1 待删行按「归一 source->target」分布
	CodexSchemaExists    bool           // 阶段 2 完成态谓词
	CodexSchemaTables    int
	CodexSchemaIndexes   int
}

func newEntryMigrationCounters() *entryMigrationCounters {
	return &entryMigrationCounters{
		ConflictsByAlias:   map[string]int{},
		NormalizedSource:   map[string]int{},
		NormalizedTarget:   map[string]int{},
		UnknownTokens:      map[string]int{},
		NonCompliantByPair: map[string]int{},
	}
}

// sqlCountByKey 执行 (key text, n integer) 两列查询并聚合为 map（42P01 按
// 空集处理，与 sqlScalar 的 fresh 库语义一致）。
func sqlCountByKey(ctx context.Context, dsn, query string, args ...any) (map[string]int, error) {
	db, err := openPG(dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			return map[string]int{}, nil
		}
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			return nil, err
		}
		out[key] = count
	}
	return out, rows.Err()
}

// countEntryMigrationState 只读求值三阶段计数。查询形状模拟执行顺序：
// 冲突行计入删除计数；归一/保留/未知 token/阶段 1 计数都排除冲突删除行
// （NOT (source ∈ legacy AND 冲突)），未知 token 再排除阶段 1 将删除的行，
// 使 dry-run 计数与 Apply 实际值同形状。
func countEntryMigrationState(ctx context.Context, dsn string) (*entryMigrationCounters, error) {
	counters := newEntryMigrationCounters()

	// 行级存活谓词：不被阶段 0a 冲突删除，且不会命中阶段 1 删除谓词。
	surviving := ` NOT (source_endpoint_family IN (` + entryMappingLegacyFamilyTokenList + `)
		   AND ` + entryMappingConflictPredicate(`account_entry_mappings.`) + `)
		   AND NOT (` + entryMappingRetirePredicate() + `)`

	legacySource, err := sqlCountByKey(ctx, dsn, `
		SELECT source_endpoint_family AS key, count(*) AS n
		  FROM juhe_business.account_entry_mappings
		 WHERE source_endpoint_family IN (`+entryMappingLegacyFamilyTokenList+`)
		   AND NOT `+entryMappingConflictPredicate(`account_entry_mappings.`)+`
		 GROUP BY 1`)
	if err != nil {
		return nil, fmt.Errorf("统计待归一 source 侧 family 失败: %w", err)
	}
	counters.NormalizedSource = legacySource

	legacyTarget, err := sqlCountByKey(ctx, dsn, `
		SELECT target_endpoint_family AS key, count(*) AS n
		  FROM juhe_business.account_entry_mappings
		 WHERE target_endpoint_family IN (`+entryMappingLegacyFamilyTokenList+`)
		   AND NOT (source_endpoint_family IN (`+entryMappingLegacyFamilyTokenList+`)
		   AND `+entryMappingConflictPredicate(`account_entry_mappings.`)+`)
		 GROUP BY 1`)
	if err != nil {
		return nil, fmt.Errorf("统计待归一 target 侧 family 失败: %w", err)
	}
	counters.NormalizedTarget = legacyTarget

	conflicts, err := sqlCountByKey(ctx, dsn, `
		SELECT legacy.source_endpoint_family AS key, count(*) AS n
		  FROM juhe_business.account_entry_mappings legacy
		 WHERE legacy.source_endpoint_family IN (`+entryMappingLegacyFamilyTokenList+`)
		   AND `+entryMappingConflictPredicate(`legacy.`)+`
		 GROUP BY 1`)
	if err != nil {
		return nil, fmt.Errorf("统计 family 归一唯一键冲突失败: %w", err)
	}
	counters.ConflictsByAlias = conflicts
	for _, count := range conflicts {
		counters.ConflictRows += count
	}

	// 阶段 0 预计 UPDATE 行数 = 带历史 token 且不被冲突删除的行。
	pendingUpdate, err := sqlScalar(ctx, dsn, `
		SELECT count(*) FROM juhe_business.account_entry_mappings m
		 WHERE (m.source_endpoint_family IN (`+entryMappingLegacyFamilyTokenList+`)
		    OR m.target_endpoint_family IN (`+entryMappingLegacyFamilyTokenList+`))
		   AND NOT (m.source_endpoint_family IN (`+entryMappingLegacyFamilyTokenList+`)
		   AND `+entryMappingConflictPredicate(`m.`)+`)`)
	if err != nil {
		return nil, fmt.Errorf("统计待归一行数失败: %w", err)
	}
	counters.PendingUpdateRows = pendingUpdate

	legacyTokenRows, err := sqlScalar(ctx, dsn, `
		SELECT count(*) FROM juhe_business.account_entry_mappings
		 WHERE source_endpoint_family IN (`+entryMappingLegacyFamilyTokenList+`)
		    OR target_endpoint_family IN (`+entryMappingLegacyFamilyTokenList+`)`)
	if err != nil {
		return nil, fmt.Errorf("统计历史 family token 残留失败: %w", err)
	}
	counters.LegacyTokenRows = legacyTokenRows

	geminiPairKept, err := sqlScalar(ctx, dsn, `
		SELECT count(*) FROM juhe_business.account_entry_mappings
		 WHERE source_endpoint_family = 'gemini_stream_generate'
		   AND target_endpoint_family = 'gemini_generate_content'
		   AND NOT `+entryMappingConflictPredicate(`account_entry_mappings.`))
	if err != nil {
		return nil, fmt.Errorf("统计 Gemini legacy pair 保留数失败: %w", err)
	}
	counters.GeminiLegacyPairKept = geminiPairKept

	unknownTokens, err := sqlCountByKey(ctx, dsn, `
		SELECT token, count(*) AS n FROM (
			SELECT `+entryMappingFamilyNormalizeExpr(`source_endpoint_family`)+` AS token
			  FROM juhe_business.account_entry_mappings
			 WHERE `+surviving+`
			UNION ALL
			SELECT `+entryMappingFamilyNormalizeExpr(`target_endpoint_family`)+` AS token
			  FROM juhe_business.account_entry_mappings
			 WHERE `+surviving+`
		) projected
		 WHERE token NOT IN (`+entryMappingCurrentFamilyTokenList+`)
		 GROUP BY 1`)
	if err != nil {
		return nil, fmt.Errorf("统计未知 family token 失败: %w", err)
	}
	counters.UnknownTokens = unknownTokens

	normalizedPairExpr := entryMappingFamilyNormalizeExpr(`source_endpoint_family`) + ` || '->' || ` + entryMappingFamilyNormalizeExpr(`target_endpoint_family`)
	nonCompliantByPair, err := sqlCountByKey(ctx, dsn, `
		SELECT `+normalizedPairExpr+` AS key, count(*) AS n
		  FROM juhe_business.account_entry_mappings
		 WHERE NOT (source_endpoint_family IN (`+entryMappingLegacyFamilyTokenList+`)
		   AND `+entryMappingConflictPredicate(`account_entry_mappings.`)+`)
		   AND `+entryMappingRetirePredicate()+`
		 GROUP BY 1`)
	if err != nil {
		return nil, fmt.Errorf("统计非合规映射分布失败: %w", err)
	}
	counters.NonCompliantByPair = nonCompliantByPair
	for _, count := range nonCompliantByPair {
		counters.NonCompliantRows += count
	}

	codexTables, err := sqlScalar(ctx, dsn,
		`SELECT count(*) FROM information_schema.tables WHERE table_schema = 'juhe_codex_context'`)
	if err != nil {
		return nil, fmt.Errorf("探测 juhe_codex_context 表失败: %w", err)
	}
	counters.CodexSchemaTables = codexTables
	// 索引按非主键口径计数（9 条显式 CREATE INDEX；主键约束索引不计，与计划
	// 「4 表 9 索引」口径一致）。
	codexIndexes, err := sqlScalar(ctx, dsn, `
		SELECT count(*) FROM pg_index x
		  JOIN pg_class tbl ON tbl.oid = x.indrelid
		  JOIN pg_namespace n ON n.oid = tbl.relnamespace
		 WHERE n.nspname = 'juhe_codex_context' AND NOT x.indisprimary`)
	if err != nil {
		return nil, fmt.Errorf("探测 juhe_codex_context 索引失败: %w", err)
	}
	counters.CodexSchemaIndexes = codexIndexes
	codexSchema, err := sqlScalar(ctx, dsn,
		`SELECT count(*) FROM information_schema.schemata WHERE schema_name = 'juhe_codex_context'`)
	if err != nil {
		return nil, fmt.Errorf("探测 juhe_codex_context schema 失败: %w", err)
	}
	counters.CodexSchemaExists = codexSchema > 0

	return counters, nil
}

// logPending 把待执行计数写到 w（Satisfied 求值于 dry-run 计划阶段，运维由此
// 核对各别名归一数/冲突数/删除数与 Gemini legacy pair 保留数；stdout 保持单行
// JSON 报告，计数走 stderr）。
func (c *entryMigrationCounters) logPending(w io.Writer) {
	fmt.Fprintf(w, "revision 20 normalize-legacy-entry-mapping-families：待归一 source 侧 %v / target 侧 %v；唯一键冲突待删 %d 行 %v；Gemini legacy pair（gemini_stream_generate -> gemini_generate_content）待保留 %d 行；未知 token（不改写，只报告）%v\n",
		c.NormalizedSource, c.NormalizedTarget, c.ConflictRows, c.ConflictsByAlias, c.GeminiLegacyPairKept, c.UnknownTokens)
	fmt.Fprintf(w, "revision 20 retire-cross-protocol-entry-mappings：待删除非合规映射 %d 行 %v\n",
		c.NonCompliantRows, c.NonCompliantByPair)
	if c.CodexSchemaExists {
		fmt.Fprintf(w, "revision 20 retire-codex-context-schema：juhe_codex_context 待删除（表 %d、索引 %d）\n",
			c.CodexSchemaTables, c.CodexSchemaIndexes)
	}
}

// entryMigrationRetirementSatisfied 是三阶段共享的完成态谓词（Satisfied 与
// Verify 同源）：无历史 token 残留、无非合规映射、juhe_codex_context schema
// 不存在。未知 token 不参与本谓词（按计划裁决只报告不改写）。
func entryMigrationRetirementSatisfied(ctx context.Context, dsn string) (bool, *entryMigrationCounters, error) {
	legacyTokenRows, err := sqlScalar(ctx, dsn, `
		SELECT count(*) FROM juhe_business.account_entry_mappings
		 WHERE source_endpoint_family IN (`+entryMappingLegacyFamilyTokenList+`)
		    OR target_endpoint_family IN (`+entryMappingLegacyFamilyTokenList+`)`)
	if err != nil {
		return false, nil, fmt.Errorf("统计历史 family token 残留失败: %w", err)
	}
	nonCompliantRows, err := sqlScalar(ctx, dsn, `
		SELECT count(*) FROM juhe_business.account_entry_mappings
		 WHERE `+entryMappingRetirePredicate())
	if err != nil {
		return false, nil, fmt.Errorf("统计非合规映射失败: %w", err)
	}
	codexSchema, err := sqlScalar(ctx, dsn,
		`SELECT count(*) FROM information_schema.schemata WHERE schema_name = 'juhe_codex_context'`)
	if err != nil {
		return false, nil, fmt.Errorf("探测 juhe_codex_context schema 失败: %w", err)
	}
	satisfied := legacyTokenRows == 0 && nonCompliantRows == 0 && codexSchema == 0
	if satisfied {
		return true, nil, nil
	}
	counters, err := countEntryMigrationState(ctx, dsn)
	if err != nil {
		return false, nil, err
	}
	return false, counters, nil
}

// applyEntryMigrationRetirement 按序执行三阶段并返回逐阶段 detail（进入账本
// detail_json）。写入用 RowsAffected 作权威计数，预检计数作分布明细；幂等
// 重跑在完成态上零写入。
func applyEntryMigrationRetirement(ctx context.Context, dsn string) (upgradepipeline.Detail, error) {
	counters, err := countEntryMigrationState(ctx, dsn)
	if err != nil {
		return nil, err
	}

	// 阶段 0a：唯一键冲突——canonical 优先，删除 legacy 重复行。
	deletedConflictRows, err := sqlExecCount(ctx, dsn, `
		DELETE FROM juhe_business.account_entry_mappings legacy
		 WHERE legacy.source_endpoint_family IN (`+entryMappingLegacyFamilyTokenList+`)
		   AND `+entryMappingConflictPredicate(`legacy.`))
	if err != nil {
		return nil, fmt.Errorf("删除 family 归一冲突行失败: %w", err)
	}

	// 阶段 0b：两侧全量归一（只改 family 两列，不动 updated_at——与 revision 13
	// 同款「存量收敛非用户配置变更」口径）。
	updatedRows, err := sqlExecCount(ctx, dsn, `
		UPDATE juhe_business.account_entry_mappings
		   SET source_endpoint_family = `+entryMappingFamilyNormalizeExpr(`source_endpoint_family`)+`,
		       target_endpoint_family = `+entryMappingFamilyNormalizeExpr(`target_endpoint_family`)+`
		 WHERE source_endpoint_family IN (`+entryMappingLegacyFamilyTokenList+`)
		    OR target_endpoint_family IN (`+entryMappingLegacyFamilyTokenList+`)`)
	if err != nil {
		return nil, fmt.Errorf("归一历史 family token 失败: %w", err)
	}

	// 阶段 1：删除规范化后仍非合规的映射行。
	deletedNonCompliantRows, err := sqlExecCount(ctx, dsn, `
		DELETE FROM juhe_business.account_entry_mappings
		 WHERE `+entryMappingRetirePredicate())
	if err != nil {
		return nil, fmt.Errorf("删除非合规跨协议映射失败: %w", err)
	}

	// 阶段 2：删除 juhe_codex_context schema。
	droppedSchema := false
	if counters.CodexSchemaExists {
		if _, err := sqlExecCount(ctx, dsn, `DROP SCHEMA IF EXISTS juhe_codex_context CASCADE`); err != nil {
			return nil, fmt.Errorf("删除 juhe_codex_context schema 失败: %w", err)
		}
		droppedSchema = true
	}

	fmt.Fprintf(os.Stderr, "revision 20 归一：唯一键冲突删除 %d 行（预检 %d），family 归一 %d 行（预检 %d）\n",
		deletedConflictRows, counters.ConflictRows, updatedRows, counters.PendingUpdateRows)
	fmt.Fprintf(os.Stderr, "revision 20 非合规映射删除：%d 行（预检 %d）\n", deletedNonCompliantRows, counters.NonCompliantRows)
	fmt.Fprintf(os.Stderr, "revision 20 juhe_codex_context schema 清退：%v（表 %d、索引 %d）\n",
		droppedSchema, counters.CodexSchemaTables, counters.CodexSchemaIndexes)

	return upgradepipeline.Detail{
		"normalize-legacy-entry-mapping-families": map[string]any{
			"conflictsByAlias":        counters.ConflictsByAlias,
			"deletedConflictRows":     deletedConflictRows,
			"normalizedSourceByAlias": counters.NormalizedSource,
			"normalizedTargetByAlias": counters.NormalizedTarget,
			"updatedRows":             updatedRows,
			"geminiLegacyPairKept":    counters.GeminiLegacyPairKept,
			"unknownTokens":           counters.UnknownTokens,
		},
		"retire-cross-protocol-entry-mappings": map[string]any{
			"deletedRows": deletedNonCompliantRows,
			"byPair":      counters.NonCompliantByPair,
		},
		"retire-codex-context-schema": map[string]any{
			"schemaExistsBefore": counters.CodexSchemaExists,
			"tables":             counters.CodexSchemaTables,
			"indexes":            counters.CodexSchemaIndexes,
			"dropped":            droppedSchema,
		},
	}, nil
}

// verifyEntryMigrationRetirement 是 Apply 后的完成态核验（与
// entryMigrationRetirementSatisfied 同一谓词，逐阶段给出可归因错误）。
func verifyEntryMigrationRetirement(ctx context.Context, dsn string) error {
	satisfied, counters, err := entryMigrationRetirementSatisfied(ctx, dsn)
	if err != nil {
		return err
	}
	if satisfied {
		return nil
	}
	if counters == nil {
		return errors.New("revision 20 完成态谓词未满足但无计数明细")
	}
	if counters.LegacyTokenRows > 0 {
		return fmt.Errorf("revision 20 归一未完成：仍有 %d 行历史 family token（source %v / target %v）",
			counters.LegacyTokenRows, counters.NormalizedSource, counters.NormalizedTarget)
	}
	if counters.NonCompliantRows > 0 {
		return fmt.Errorf("revision 20 仍存在 %d 条非合规跨协议映射 %v", counters.NonCompliantRows, counters.NonCompliantByPair)
	}
	if counters.CodexSchemaExists {
		return fmt.Errorf("revision 20 juhe_codex_context schema 仍存在（表 %d、索引 %d）",
			counters.CodexSchemaTables, counters.CodexSchemaIndexes)
	}
	return errors.New("revision 20 完成态谓词未满足")
}

// ---------- revision 22：high_concurrency 分组迁移为 personal + 两键策略 ----------
//
// 普通分组可选调度策略设计 §5（2026-11-09 裁决）：退役 high_concurrency 分组
// 类型，把 defaultSoftConcurrency / clientIpConcurrencyLimit 两个可选策略并入
// 普通分组。一个 Destructive 步骤在停写窗口内：
//  1. groups 全部行迁为 personal，16 键旧策略按执行时 globalMax 映射为两键
//     （globalMax 取 JUHE_AI_CONCURRENCY_GLOBAL_MAX 实际值，缺失/非法回落默认
//     5000；阈值 ∈ [1, min(1000000, globalMax)) 保留，缺失/<=0/>=globalMax →
//     -1；IP 限制保留；两项都默认 → NULL）；
//  2. group_authorization_settings 的 group_type/策略列清为 personal/NULL
//    （覆盖退役，契约 §2.1）；
//  3. 超级分组自身行按首成员重算（保持成员策略指纹一致，契约 §4）。
//
// 畸形 JSON/未知键/未知 group_type 行进入 needs_review 并阻断 apply（CLI 退出
// 码 3；upgrade 步骤 Apply 返回错误停账本于 running），不静默修复（契约 §8-14）。
// 不更新 updated_at（§8-13，由发布流程重启进程刷新运行时缓存）。与一次性命令
// migrate-group-policy 共用同一迁移内核（Satisfied/Verify 与内核完成态谓词
// 同源；dry-run 由 CLI 的 --dry-run/--check=preflight 承载，升级前先跑 CLI
// 盘点并复核 manifest）。

// groupPolicySatisfied 是 revision 22 的完成态谓词（Satisfied 与 Verify 同源）：
// 两表无 personal 之外的 group_type、策略列只含两键或 NULL。fresh 库语义
// （§9.4-6）：groups 表尚未建立即「无可迁移数据」，谓词返回满足。
func groupPolicySatisfied(ctx context.Context, dsn string) (bool, error) {
	exists, err := tableExists(ctx, dsn, "juhe_business", "groups")
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}
	db, err := openPG(dsn)
	if err != nil {
		return false, err
	}
	defer db.Close()
	return groupspolicymigration.Satisfied(ctx, db)
}

// groupPolicyGlobalMaxFromEnv 读取旧二进制运行时的实际 globalMax
// （JUHE_AI_CONCURRENCY_GLOBAL_MAX，契约 §5.2：阈值保留区间按执行时的实际值
// 判定）。upgrade 步骤没有人工 flag 面，以执行环境 env 为准；缺失回落
// DefaultGlobalConcurrencyMax；非法值同样回落但 stderr WARN（不静默吞掉配置
// 错误）。CLI 的 --global-max 保持人工显式覆盖入口。
func groupPolicyGlobalMaxFromEnv(logf func(format string, args ...any)) int {
	raw := strings.TrimSpace(os.Getenv("JUHE_AI_CONCURRENCY_GLOBAL_MAX"))
	if raw == "" {
		return groupspolicymigration.DefaultGlobalConcurrencyMax
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > groupspolicymigration.MaxGlobalConcurrencyMax {
		logf("JUHE_AI_CONCURRENCY_GLOBAL_MAX=%q 非法（需要 1-%d 的整数），回落默认 %d——请核对停写窗口执行环境的实际配置",
			raw, groupspolicymigration.MaxGlobalConcurrencyMax, groupspolicymigration.DefaultGlobalConcurrencyMax)
		return groupspolicymigration.DefaultGlobalConcurrencyMax
	}
	return value
}

// applyGroupPolicyMigration 是 revision 22 的 Apply：调用与
// migrate-group-policy CLI 同一迁移内核；needs_review 行返回错误阻断（引擎把
// detail_json 与错误写账本，账本停于 running，人工复核后重跑）。
func applyGroupPolicyMigration(ctx context.Context, dsn string) (upgradepipeline.Detail, error) {
	db, err := openPG(dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	globalMax := groupPolicyGlobalMaxFromEnv(func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "revision 22 "+format+"\n", args...)
	})
	plan, applyErr := groupspolicymigration.Apply(ctx, db, groupspolicymigration.Options{GlobalMax: globalMax})
	detail := upgradepipeline.Detail{
		"globalMax":       plan.GlobalMax,
		"mappedCount":     plan.Mapped,
		"unchangedCount":  plan.Unchanged,
		"blockedCount":    plan.Blocked,
		"appliedCount":    plan.Applied,
		"needsReviewRows": plan.NeedsReview,
	}
	if applyErr != nil {
		return detail, applyErr
	}
	fmt.Fprintf(os.Stderr, "revision 22 分组策略迁移（globalMax=%d）：update %d 行（unchanged %d；needs_review %d）\n",
		plan.GlobalMax, plan.Applied, plan.Unchanged, plan.Blocked)
	return detail, nil
}

// verifyGroupPolicyMigration 是 Apply 后的完成态核验（与 Satisfied 同一谓词，
// 逐项给出可归因计数）。
func verifyGroupPolicyMigration(ctx context.Context, dsn string) error {
	satisfied, err := groupPolicySatisfied(ctx, dsn)
	if err != nil {
		return err
	}
	if satisfied {
		return nil
	}
	db, err := openPG(dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	report, err := groupspolicymigration.VerifyPostflight(ctx, db)
	if err != nil {
		return err
	}
	if report.NonPersonalGroups > 0 {
		return fmt.Errorf("revision 22 迁移未完成：仍有 %d 行 groups.group_type 非 personal", report.NonPersonalGroups)
	}
	if report.NonPersonalAuthSettings > 0 {
		return fmt.Errorf("revision 22 迁移未完成：仍有 %d 行授权设置 group_type 非 personal", report.NonPersonalAuthSettings)
	}
	if report.InvalidPolicyGroups > 0 || report.InvalidPolicyAuthSettings > 0 {
		return fmt.Errorf("revision 22 策略列仍非两键/NULL 完成态形状（groups %d / 授权设置 %d）：%v",
			report.InvalidPolicyGroups, report.InvalidPolicyAuthSettings, report.Examples)
	}
	return errors.New("revision 22 完成态谓词未满足")
}

// ---------- revision 23：退役 usage shard 目录四表 ----------
//
// 2026-11-10 用户裁决：shard 目录四表没有现行需求，生产写入链路只写
// juhe_usage.usage_records，列表与清理均直接读该日分区表，四表无生产读取方。
// 一个 Destructive 步骤在停写窗口内按 FK 依赖顺序 DROP：
//  1. usage_record_shard_entries
//  2. usage_record_account_shards
//  3. usage_record_api_key_shards
//  4. usage_record_shards
//
// 四表是历史兼容/造数遗留，DROP 不可回滚；执行前必须完成可恢复备份（发布
// runbook 的停写+备份门禁）。缺失表是良性 no-op，重复执行只报告已缺席。

var usageShardCatalogTables = []string{
	"usage_record_shard_entries",
	"usage_record_account_shards",
	"usage_record_api_key_shards",
	"usage_record_shards",
}

func usageShardCatalogSatisfied(ctx context.Context, dsn string) (bool, error) {
	for _, table := range usageShardCatalogTables {
		exists, err := tableExists(ctx, dsn, "juhe_usage", table)
		if err != nil {
			return false, err
		}
		if exists {
			return false, nil
		}
	}
	return true, nil
}

func applyUsageShardCatalogRetirement(ctx context.Context, dsn string) (upgradepipeline.Detail, error) {
	satisfied, err := usageShardCatalogSatisfied(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if satisfied {
		return upgradepipeline.Detail{"dropped": 0, "alreadyRetired": true}, nil
	}
	toDrop := make([]string, 0, len(usageShardCatalogTables))
	for _, table := range usageShardCatalogTables {
		exists, err := tableExists(ctx, dsn, "juhe_usage", table)
		if err != nil {
			return nil, err
		}
		if exists {
			toDrop = append(toDrop, table)
		}
	}
	db, err := openPG(dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	dropped := 0
	for _, table := range toDrop {
		if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS juhe_usage.`+table); err != nil {
			return upgradepipeline.Detail{"dropped": dropped, "failedTable": table}, err
		}
		dropped++
	}
	if err := tx.Commit(); err != nil {
		return upgradepipeline.Detail{"dropped": dropped}, err
	}
	return upgradepipeline.Detail{"dropped": dropped, "alreadyRetired": false}, nil
}

// upgradeSteps 返回 15 步骤注册表（revision 升序；引擎再做一次排序校验）。
// secret 为空时，凭据类 Check 不允许调用（第 8 步门禁保证到 Apply 阶段必有；
// dry-run 阶段 Check 若需要 secret 会以错误呈现——预检语义可接受）。
func upgradeSteps(primaryDSN, secret string) []upgradepipeline.Step {
	j3aDSN, j3bDSN, balanceDSN := upgradeTargets(primaryDSN)

	studioCheck := func(ctx context.Context) (bool, error) {
		for _, table := range []string{"studio_assets", "studio_tasks", "studio_operations", "studio_user_asset_usage"} {
			exists, err := tableExists(ctx, primaryDSN, "juhe_business", table)
			if err != nil || exists {
				return false, err
			}
		}
		exists, err := columnExists(ctx, primaryDSN, "juhe_business", "api_keys", "studio_domains_json")
		if err != nil {
			return false, err
		}
		return !exists, nil
	}

	monitoringCheck := func(ctx context.Context) (bool, error) {
		for _, table := range []string{
			"system_metrics_samples", "system_metrics_hourly", "system_metrics_trend_windows",
			"process_event_loop_samples", "process_event_loop_hourly", "process_event_loop_trend_windows",
			"go_runtime_metrics_samples", "go_runtime_metrics_hourly", "go_runtime_metrics_trend_windows",
			"database_storage_snapshots", "table_storage_snapshots", "table_monitor_owner_leases",
		} {
			exists, err := tableExists(ctx, primaryDSN, "juhe_stats", table)
			if err != nil || exists {
				return false, err
			}
		}
		return true, nil
	}

	// capabilityRecordSealed 是能力域步骤达成判据的**记录臂**：gateway 启动
	// 门禁要求阶段记录 m2_finalized 存在（verify CLI 的 postflight 只校验域值
	// 不校验记录——P2 演练实锤的假阳性根源），故记录检查必须独立成臂且与
	// gateway 门禁同源。
	capabilityRecordSealed := func(ctx context.Context) (bool, error) {
		value, err := sqlScalar(ctx, primaryDSN,
			`SELECT count(*) FROM juhe_business.global_settings
			 WHERE key = 'account_capability_domain_migration'
			   AND value_json::jsonb->>'stage' = 'm2_finalized'`)
		if err != nil {
			return false, err
		}
		return value > 0, nil
	}
	capabilityCheck := func(ctx context.Context) (bool, error) {
		sealed, err := capabilityRecordSealed(ctx)
		if err != nil || !sealed {
			return sealed, err
		}
		code, _ := callResult(func() int {
			return accountCapabilityDomainVerifyResult(primaryDSN, secret, "m2-cutover", "postflight")
		})
		return code == 0, nil
	}

	schedprefCheck := func(ctx context.Context) (bool, error) {
		code, _ := callResult(func() int {
			return schedulingPreferencesVerifyResult(primaryDSN, "postflight")
		})
		return code == 0, nil
	}

	upstreamCheck := func(ctx context.Context) (bool, error) {
		code, _ := callResult(func() int {
			return upstreamBaseURLVerifyResult(primaryDSN, "postflight", secret)
		})
		return code == 0, nil
	}

	return []upgradepipeline.Step{
		{
			Revision: 1, ID: "j3a-proxy-latency-pg",
			Title: "J3a 代理延迟检测 PostgreSQL 表与索引预置", Target: upgradepipeline.TargetJ3A,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				code, _ := callResult(func() int { return j3aProxyLatencyBootstrapResult(ctx, j3aDSN, false) })
				return code == 0, nil
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				if err := upgradepipeline.EnsureTargetSchema(ctx, j3aDSN, upgradepipeline.TargetJ3A); err != nil {
					return nil, err
				}
				code, output := callResult(func() int { return j3aProxyLatencyBootstrapResult(ctx, j3aDSN, true) })
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("j3a 预置", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				code, _ := callResult(func() int { return j3aProxyLatencyBootstrapResult(ctx, j3aDSN, false) })
				if code != 0 {
					return fmt.Errorf("J3a 表/索引契约未就绪（退出码 %d）", code)
				}
				return nil
			},
		},
		{
			Revision: 2, ID: "j3b-model-check-pg",
			Title: "J3b 模型检测 PostgreSQL 表预置与 trigger_kind 词表", Target: upgradepipeline.TargetJ3B,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				code, _ := callResult(func() int { return j3bModelCheckBootstrapResult(ctx, j3bDSN, false) })
				return code == 0, nil
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				if err := upgradepipeline.EnsureTargetSchema(ctx, j3bDSN, upgradepipeline.TargetJ3B); err != nil {
					return nil, err
				}
				code, output := callResult(func() int { return j3bModelCheckBootstrapResult(ctx, j3bDSN, true) })
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("j3b 预置", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				code, _ := callResult(func() int { return j3bModelCheckBootstrapResult(ctx, j3bDSN, false) })
				if code != 0 {
					return fmt.Errorf("J3b 表/CHECK 契约未就绪（退出码 %d）", code)
				}
				return nil
			},
		},
		{
			Revision: 3, ID: "account-balance-pg",
			Title: "J2 账户余额 PostgreSQL 四表预置", Target: upgradepipeline.TargetAccountBalance,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				for _, table := range []string{
					"account_balance_owner_leases", "account_balance_account_leases",
					"account_balance_snapshots", "account_balance_outcomes",
				} {
					exists, err := tableExists(ctx, balanceDSN, "juhe_jobs", table)
					if err != nil || !exists {
						return false, err
					}
				}
				return true, nil
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				if err := upgradepipeline.EnsureTargetSchema(ctx, balanceDSN, upgradepipeline.TargetAccountBalance); err != nil {
					return nil, err
				}
				code, output := callResult(func() int { return accountBalanceBootstrapResult(ctx, balanceDSN) })
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("账户余额预置", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				for _, table := range []string{
					"account_balance_owner_leases", "account_balance_account_leases",
					"account_balance_snapshots", "account_balance_outcomes",
				} {
					exists, err := tableExists(ctx, balanceDSN, "juhe_jobs", table)
					if err != nil {
						return err
					}
					if !exists {
						return fmt.Errorf("juhe_jobs.%s 缺失", table)
					}
				}
				return nil
			},
		},
		{
			Revision: 4, ID: "capability-domain",
			Title: "AI 账户能力域赋域迁移（m1→m2-cutover）", Target: upgradepipeline.TargetPrimary,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				return capabilityCheck(ctx)
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				// 两阶段状态机固定 m1→m2-cutover 顺序（中间态持久于阶段记录，
				// 重复执行对已完成阶段为 no-op——设计 §10 注）。
				code, output := callResult(func() int {
					return accountCapabilityDomainMigrateResult(primaryDSN, secret, "m1", true, true, true)
				})
				if code != 0 {
					return upgradepipeline.Detail{"stage": "m1", "output": output}, exitErr("能力域 m1", output, code)
				}
				code, output = callResult(func() int {
					return accountCapabilityDomainMigrateResult(primaryDSN, secret, "m2-cutover", true, true, true)
				})
				if code != 0 {
					return upgradepipeline.Detail{"stage": "m2-cutover", "output": output}, exitErr("能力域 m2-cutover", output, code)
				}
				// 全新空库（零账户）补走 ensure 同源的自动封存：upgrade 绕过
				// runStorageBootstrap，SealFreshInstall 必须在此显式补位，否则
				// gateway 能力域门禁会拦死全新安装（P2 演练实锤）。
				sealed, sealErr := capabilityRecordSealed(ctx)
				if sealErr != nil {
					return upgradepipeline.Detail{"stage": "m2-cutover"}, sealErr
				}
				if !sealed {
					db, openErr := openPG(primaryDSN)
					if openErr != nil {
						return upgradepipeline.Detail{"stage": "m2-cutover"}, openErr
					}
					if _, sealErr := capabilitydomain.SealFreshInstall(ctx, db); sealErr != nil {
						db.Close()
						return upgradepipeline.Detail{"stage": "m2-cutover"}, sealErr
					}
					db.Close()
				}
				return upgradepipeline.Detail{"stage": "m2-cutover", "output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				satisfied, err := capabilityCheck(ctx)
				if err != nil {
					return err
				}
				if !satisfied {
					return errors.New("能力域阶段记录未达 m2_finalized")
				}
				return nil
			},
		},
		{
			Revision: 5, ID: "scheduling-preferences",
			Title: "调度偏好独立实体绑定回填", Target: upgradepipeline.TargetPrimary,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				return schedprefCheck(ctx)
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				code, output := callResult(func() int {
					return schedulingPreferencesMigrateResult(primaryDSN, true, true, true)
				})
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("调度偏好迁移", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				satisfied, err := schedprefCheck(ctx)
				if err != nil {
					return err
				}
				if !satisfied {
					return errors.New("调度偏好迁移完成记录未达 completed 或存在残留")
				}
				return nil
			},
		},
		{
			Revision: 6, ID: "merge-super-groups",
			Title: "merge 路由策略迁移至超级分组", Target: upgradepipeline.TargetPrimary,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				value, err := sqlScalar(ctx, primaryDSN,
					`SELECT count(*) FROM juhe_business.route_strategies WHERE mode = 'merge'`)
				if err != nil {
					return false, err
				}
				return value == 0, nil
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				code, output := callResult(func() int {
					return mergeSuperGroupsMigrationResult(false, primaryDSN, "", "upgrade-merge-super-groups", true, true, true)
				})
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("超级分组迁移", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				value, err := sqlScalar(ctx, primaryDSN,
					`SELECT count(*) FROM juhe_business.route_strategies WHERE mode = 'merge'`)
				if err != nil {
					return err
				}
				if value != 0 {
					return fmt.Errorf("仍存在 %d 条 mode='merge' 策略", value)
				}
				return nil
			},
		},
		{
			Revision: 8, ID: "upstream-base-url-version-root",
			Title: "上游 Base URL 版本根语义回填", Target: upgradepipeline.TargetPrimary,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				return upstreamCheck(ctx)
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				code, output := callResult(func() int {
					return upstreamBaseURLMigrationResult(false, primaryDSN, "", secret, true, true, true)
				})
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("Base URL 回填", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				satisfied, err := upstreamCheck(ctx)
				if err != nil {
					return err
				}
				if !satisfied {
					return errors.New("上游 Base URL postflight 核验未通过")
				}
				return nil
			},
		},
		{
			Revision: 9, ID: "success-cost-backfill",
			Title: "六表 success_cost_usd 历史回填", Target: upgradepipeline.TargetPrimary,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				exists, err := tableExists(ctx, primaryDSN, "juhe_stats", "backfill_progress")
				if err != nil || !exists {
					return false, err
				}
				total, err := sqlScalar(ctx, primaryDSN, `SELECT count(*) FROM juhe_stats.backfill_progress`)
				if err != nil {
					return false, err
				}
				done, err := sqlScalar(ctx, primaryDSN, `SELECT count(*) FROM juhe_stats.backfill_progress WHERE status = 'done'`)
				if err != nil {
					return false, err
				}
				return total >= 6 && done == total, nil
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				code, output := callResult(func() int { return applySuccessCostBackfillResult(primaryDSN, false) })
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("成功成本回填", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				satisfied, err := sqlScalar(ctx, primaryDSN,
					`SELECT count(*) FROM juhe_stats.backfill_progress WHERE status <> 'done'`)
				if err != nil {
					return err
				}
				if satisfied != 0 {
					return fmt.Errorf("backfill_progress 存在 %d 条非 done 行", satisfied)
				}
				return nil
			},
		},
		{
			Revision: 10, ID: "studio-retirement",
			Title: "媒体工作室遗留清退", Target: upgradepipeline.TargetPrimary,
			Destructive: true,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				return studioCheck(ctx)
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				code, output := callResult(func() int {
					return studioRetirementResult(primaryDSN, true, true, true)
				})
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("studio 清退", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				satisfied, err := studioCheck(ctx)
				if err != nil {
					return err
				}
				if !satisfied {
					return errors.New("studio 遗留对象未清退干净")
				}
				return nil
			},
		},
		{
			Revision: 11, ID: "drop-retired-monitoring-tables",
			Title: "12 张退役监控表清退", Target: upgradepipeline.TargetPrimary,
			Destructive: true,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				return monitoringCheck(ctx)
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				code, output := callResult(func() int {
					return dropRetiredMonitoringTablesResult(primaryDSN, true, true, true)
				})
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("监控表清退", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				satisfied, err := monitoringCheck(ctx)
				if err != nil {
					return err
				}
				if !satisfied {
					return errors.New("退役监控表未清退干净")
				}
				return nil
			},
		},
		{
			Revision: 12, ID: "account-default-resources",
			Title: "系统账户默认资源家族回填", Target: upgradepipeline.TargetPrimary,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				// P1 简化判据：无系统账户即满足（fresh adopt）；存量库一律执行
				// 幂等命令（完成态零写入，§10 命令语义）。弱判据在步骤注释声明。
				value, err := sqlScalar(ctx, primaryDSN, `SELECT count(*) FROM juhe_business.system_accounts`)
				if err != nil {
					return false, err
				}
				return value == 0, nil
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				code, output := callResult(func() int {
					return accountDefaultResourcesResult(primaryDSN, secret, true)
				})
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("默认资源回填", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				// P1 简化：Apply 退出码 0 即通过（命令内部含逐账户报告与
				// 幂等保证）；独立完成态谓词随 P2 补强。
				return nil
			},
		},
		{
			Revision: 13, ID: "account-schedulable-normalization",
			Title: "active 账户 schedulable 存量归一（状态派生收敛）", Target: upgradepipeline.TargetPrimary,
			ImplVersion: 1,
			// T91-3：schedulable 改为状态派生后，历史「active + 不可调度」行
			// 收敛为可调度。Satisfied 与 Verify 同源（同一完成态谓词）。
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				value, err := sqlScalar(ctx, primaryDSN, schedulableNormalizationPendingSQL)
				if err != nil {
					return false, err
				}
				return value == 0, nil
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				affected, err := sqlExecCount(ctx, primaryDSN, schedulableNormalizationUpdateSQL)
				if err != nil {
					return nil, err
				}
				// 受影响行数同时进 detail_json（账本记录）与 stderr（运维可见；
				// stdout 留给单行 JSON 报告）。
				fmt.Fprintf(os.Stderr, "active 账户 schedulable 归一：%d 行\n", affected)
				return upgradepipeline.Detail{"updatedRows": affected}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				pending, err := sqlScalar(ctx, primaryDSN, schedulableNormalizationPendingSQL)
				if err != nil {
					return err
				}
				if pending > 0 {
					return fmt.Errorf("仍有 %d 个 active 账户 schedulable=0 未归一", pending)
				}
				return nil
			},
		},
		{
			// PLAN-20261106T162344524Z 批次C（2026-11-07 裁决：不支持跨协议模型
			// 映射）。Destructive=true：expand 相被引擎过滤（只推进
			// schema_revision=20、旧二进制保持可读），contract/all 相执行归一→
			// 删除→删 schema 并把 min_readable 推到 20。Satisfied/Verify 与
			// Apply 同源（entryMigrationRetirementSatisfied /
			// verifyEntryMigrationRetirement 共用谓词）；dry-run 由 Satisfied
			// 求值时把逐阶段计数打到 stderr 供停写窗口前核对。批次 D 撤除了
			// ensure-schema 的 juhe_codex_context 预建 DDL：fresh 库 schema 恒
			// 不存在，Satisfied 即刻为真（无行可删、无 schema 可清退）；存量库
			// 升级时由本步骤实际执行 Apply 并清退，终态无 schema。
			// 账本契约一个 revision 只能注册一个步骤（schema_migrations.revision
			// 主键 + validateRegistry 唯一校验），三个阶段合入本条目，逐阶段 ID
			// 与计数进 detail_json。
			Revision: 20, ID: "retire-cross-protocol-entry-mappings",
			Title:       "退役跨协议入口映射：legacy family 归一、非合规行删除与 juhe_codex_context schema 清退",
			Target:      upgradepipeline.TargetPrimary,
			Destructive: true,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				satisfied, counters, err := entryMigrationRetirementSatisfied(ctx, primaryDSN)
				if err != nil {
					return false, err
				}
				if !satisfied && counters != nil {
					counters.logPending(os.Stderr)
				}
				return satisfied, nil
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				return applyEntryMigrationRetirement(ctx, primaryDSN)
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				return verifyEntryMigrationRetirement(ctx, primaryDSN)
			},
		},
		{
			// 普通分组可选调度策略设计 §5（2026-11-09 裁决）。Destructive=true：
			// expand 相被引擎过滤，contract/all 相执行分组策略迁移。Satisfied/
			// Verify 同源（groupPolicySatisfied / 内核完成态谓词）；Apply 与
			// migrate-group-policy CLI 共用迁移内核，needs_review 行阻断并停账
			// 本于 running（升级前先跑 CLI --dry-run 复核 manifest）。
			Revision: 22, ID: "retire-high-concurrency-groups",
			Title:       "high_concurrency 分组迁移为 personal + 两键调度策略（groups 与 group_authorization_settings）",
			Target:      upgradepipeline.TargetPrimary,
			Destructive: true,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				return groupPolicySatisfied(ctx, primaryDSN)
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				return applyGroupPolicyMigration(ctx, primaryDSN)
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				return verifyGroupPolicyMigration(ctx, primaryDSN)
			},
		},
		{
			// 2026-11-10 用户裁决：shard 目录四表没有现行需求，生产写入只写
			// usage_records。Destructive=true：停写窗口内按 FK 依赖顺序 DROP；
			// Satisfied/Verify 同源（四表全部缺席）。DROP 不可回滚，执行前必须
			// 完成可恢复备份。
			Revision: 23, ID: "retire-usage-shard-catalog",
			Title:       "退役 usage shard 目录四表（juhe_usage.usage_record_shard_*）",
			Target:      upgradepipeline.TargetPrimary,
			Destructive: true,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				return usageShardCatalogSatisfied(ctx, primaryDSN)
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				return applyUsageShardCatalogRetirement(ctx, primaryDSN)
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				satisfied, err := usageShardCatalogSatisfied(ctx, primaryDSN)
				if err != nil {
					return err
				}
				if !satisfied {
					return errors.New("usage shard 目录四表未清退干净")
				}
				return nil
			},
		},
	}
}
