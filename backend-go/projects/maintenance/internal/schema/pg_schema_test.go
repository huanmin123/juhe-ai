// Tests alongside pg_schema.go. The golden counts are extracted from the Node
// PostgreSQL schema sources (backend/src/storage/postgres-schema.ts); regenerate
// them when the Node sources change. The live-database smoke test is opt-in via
// the JUHE_AI_PG_SCHEMA_SMOKE_URL environment variable.

package schema

import (
	"bytes"
	"context"
	"crypto/pbkdf2"
	"crypto/sha512"
	"database/sql"
	"encoding/base64"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// goldenPostgresSchemaStatementCount is the number of statements produced by
// collectPostgresSchemaStatements() in backend/src/storage/postgres-schema.ts,
// plus the Go-appended statements (model-check question bank table, its
// indexes and is_builtin ALTER column, the custom_question_ids ALTER columns,
// the stats success_cost_usd ALTER column statements and the Go-appended
// backfill_progress marker table — the six stats backfill UPDATE statements
// were retired to the --apply-success-cost-backfill subcommand, 2026-10-12 —
// the chat_conversations
// account-binding ALTER columns, the model_quality_schedules
// interval_minutes CHECK migration DO block, the accounts
// health_check_endpoint_mode audio/video CHECK migration DO block, the M2
// media_jobs table with its three indexes, the usage_records
// output_video_seconds ALTER column, the chat_assets media CHECK migration DO
// block, the system_accounts expedited_account_limit ALTER column, the
// accounts expedited_recovery_enabled ALTER column, and the accounts
// capability_domain ALTER column (AI 账户能力域拆分
// PLAN-20261011T141004000Z); the retired bind-mode ALTERs
// were removed with the account-only binding migration, and the model-check
// question-tests submission-gate table adds its two indexes and the question
// bank last_test ALTER columns). 2026-10-12 AI 问答简化（决策 F）批次 -15：
// chat_conversations 八个工具绑定列的 CREATE/ALTER、chat_user_tool_preferences
// 与 chat_image_generations 两表及其两枚索引从当前 schema 退役。
// Regenerate them when either source changes.
// 调度偏好独立实体（PLAN-20261012T033000000Z P1）批次 +7：scheduling_preferences
// 建表、api_keys.scheduling_preference_id 幂等 ALTER、scheduling_preferences
// 四枚索引与 api_keys 绑定列索引。v2 用户裁决修订（2026-10-13）第 4 条批次 +1：
// scheduling_preferences.enabled 存量库幂等 DROP COLUMN（列退役，创建即启用）。
// 模型检测超时处罚开关批次 +2：model_quality_policies /
// model_quality_schedules 各一条 timeout_penalty_enabled 幂等 ALTER。
// 媒体工作室 P0 批次（媒体工作室设计 §3）+12：studio_assets / studio_tasks /
// studio_operations / studio_user_asset_usage 四张建表、api_keys.studio_
// domains_json 幂等 ALTER、api_keys purpose CHECK 迁移 DO 块，以及
// idx_api_keys_studio_purpose_unique 与 studio 五枚索引（business indexes）。
// studio 退场契约修订（2026-10-14）批次 -2：破坏性 studio-retirement-tables
// 与 studio-retirement-api-keys-column 两条退出当前 schema（破坏性清理收敛到
// 显式维护命令 --apply-studio-retirement）；purpose CHECK 迁移 DO 块改写为
// 三值 legacy 词表的加法式修复（二值存量库修回三值），语句仍在。
// 718→700：2026-10-14 监控改造退役系统指标六表（system_metrics_*×3、
// process_event_loop_*×3，各 1 TABLE+2 INDEX=18 条 stats DDL）——实现随
// f1991b708 删除，本金数漏改（2026-10-17 专门批取证回正）。
// 700→678：供应商与上游协议面重构批（设计 §9 一次性裁决第 2 步切片，
// juhe_business 335→313）——-9 旧建表（providers、provider_protocol_
// profiles、profile families、provider_model_catalog、custom_provider_
// models、两张 provider default health-check、旧 account_supported_models、
// account_model_mappings）+7 新增（vendors、models、account_connections、
// account_endpoint_modes、account_supported_models 新形态、
// account_entry_mappings、account_connections 反向外键守卫 DO 块）、-23/+3
// 索引（删 provider/profile 维度 accounts 六枚与 oauth refresh 两枚、trigram
// 一枚，groups 四枚，旧表五枚目录/唯一索引与 supported_models/mappings 三枚；
// 加 idx_models_identity_unique、idx_groups_owner_name_unique、
// idx_groups_owner_default_unique）、-1/+0 触发器（profiles 脏标记触发器与
// mark_dirty_profile/profile_dirty_trigger 两函数随 provider_protocol_profiles
// 退役，CREATE/DROP 成对移除）。
// 678（±0）：2026-10-17 复审 blocker 修复批——accounts 健康检查三列
// （health_check_connection_id / health_check_endpoint_mode /
// health_check_model）转 nullable、health_check_endpoint_mode CHECK
// 22→25 值（建表内联与 accounts-health-check-endpoint-mode-pg-check DO 块
// 重建均原位改写、不新增语句），account-list-projection-pg-dirty-triggers
// 的 ROW() 比较纳入 vendor_code / connection_driver_code / oauth_type /
// health_check_connection_id；语句总数与 DO 块数不变。
// 678→679：2026-10-18 修复批——dbb2543e6 移除旧 account_model_mappings
// 建表但运行时仍读写（key-model 恢复 CheckContract、seed、accounts/usage
// 域），fresh 库 chain 网关启动 CheckContract fail-fast；恢复 ensure 建表
// （+1 TABLE juhe_business，providers 外键随收敛移除），存量迁移语义不变。
// 679→686：2026-10-18 修复批（同日第二笔）——dbb2543e6 同批移除的其余
// 7 张旧供应商表（providers、provider_protocol_profiles、profile families、
// provider_model_catalog、custom_provider_models、两张 provider default
// health-check）运行时仍读写，恢复 ensure 建表（+7 TABLE juhe_business，
// 列与约束逐字取回 dbb2543e6^ 版本，protocols 复合外键保留——该表仍在
// 当前建表清单；旧表索引不补建），存量数据迁移语义不变。
// 686→678：A 路线撤 shim 批（同日第三笔）——迁移窗口 shim 一次性收敛撤除
// （设计 §9 第 11 条：删除所有旧字段和旧 seed，不提供兼容分支）：撤回上述
// 679→686 恢复的 8 张旧表 ensure 建表（-8 TABLE juhe_business），配套旧
// seed（pgSeedLegacyProviders/Profiles、旧 provider_model_catalog 目录种子）
// 与 pgNullableText 同批删除；种子语句不计本 golden，总数变化仅 -8。
// 682→683：用量记录压缩标记批——usage_records compaction_expected 幂等
// ALTER 一条（使用记录延迟健康分档设计 §压缩请求识别；建表内联同位改写、
// 不另计语句）。683→689：provider 维度持久列退役批（B4①）——juhe_business
// +6 DROP COLUMN 幂等退役语句；codex 三表 -3 CREATE INDEX（boundary）+3
// DROP COLUMN，组内合计不变。689→691：media_jobs 归属改列批（AI 问答免 Key
// 直调计划 §3.3）——juhe_business +2（owner_system_account_id 幂等 ALTER 与
// api_keys 归属回填 UPDATE 各一条）。691→693：B4 两表 provider 维度收尾批
// ——juhe_business +2（account_test_tasks / media_jobs 各一条
// provider_protocol_profile_id 幂等 DROP COLUMN，写入面同批停写）。
// 693→681：2026-10-30 B08 撤除 12 条幂等收列语句（业务 9：scheduling_
// preferences.enabled 1 条 + business-provider-dimension-retirement 组 8 条；
// codex 3：codex-context-provider-code-retirement 组 3 条）——生产只读预检
// 确认 12 条目标旧列全部不存在，收列退役完成；当前 schema 0 条 DROP COLUMN。
// Regenerate them when either source changes.
const goldenPostgresSchemaStatementCount = 681

// goldenPostgresSchemaStatementCountsPerSchema pins the per-schema statement
// counts of collectPostgresSchemaStatements().
var goldenPostgresSchemaStatementCountsPerSchema = map[string]int{
	// 335→313：供应商与上游协议面重构批（见上方 golden 总数注释的 -9+7/-23+3
	// 明细，全部落在 juhe_business）。313→314：2026-10-18 修复批恢复旧
	// account_model_mappings ensure 建表（见上方总数注释）。314→321：同日
	// 第二笔恢复其余 7 张旧供应商表 ensure 建表（+7 TABLE）。321→313：A 路线
	// 撤 shim 批一次性撤回上述 8 张旧表 ensure 建表（-8 TABLE）。313→316：
	// 超级分组首批 +3（groups.group_kind 幂等 ALTER、super_group_members
	// TABLE、idx_super_group_members_member_group INDEX）。53→54：用量记录
	// 压缩标记批 +1（usage_records compaction_expected 幂等 ALTER）。
	// 316→322：provider 维度持久列退役批（B4①）+6（business 六列幂等
	// DROP COLUMN 退役语句）。322→324：media_jobs 归属改列批（AI 问答免 Key
	// 直调计划 §3.3）+2（owner 列幂等 ALTER 与归属回填 UPDATE）。324→326：
	// B4 两表 provider 维度收尾批 +2（account_test_tasks / media_jobs 各一条
	// provider_protocol_profile_id 幂等 DROP COLUMN）。326→317：2026-10-30
	// B08 撤除 business 9 条幂等 DROP COLUMN（enabled 1 + provider 维度 8）。
	"juhe_business": 317,
	"juhe_chat":     52,
	"juhe_dataset":  7,
	"juhe_usage":    54,
	// 256→238：同六监控表退役 -18（2026-10-17 专门批取证回正）。
	"juhe_stats": 238,
	// 16→16：provider 维度持久列退役批（B4①）-3 CREATE INDEX（codex boundary）
	// +3 DROP COLUMN。16→13：2026-10-30 B08 撤除 codex 三表 3 条 provider_code
	// 幂等 DROP COLUMN（boundary 索引已随 B4① 撤除，无对应加法语句）。
	"juhe_codex_context": 13,
}

// goldenPostgresSchemaNameOrder is the Node first-seen schema order, which is
// also the CREATE SCHEMA execution order.
var goldenPostgresSchemaNameOrder = []string{
	"juhe_business",
	"juhe_chat",
	"juhe_dataset",
	"juhe_usage",
	"juhe_stats",
	"juhe_codex_context",
}

func TestPostgresSchemaStatementsGoldenCount(t *testing.T) {
	statements := Statements()
	if len(statements) != goldenPostgresSchemaStatementCount {
		t.Fatalf("len(Statements()) = %d, want %d (Node collectPostgresSchemaStatements)", len(statements), goldenPostgresSchemaStatementCount)
	}
	if len(postgresSchemaStatements) != goldenPostgresSchemaStatementCount {
		t.Fatalf("len(postgresSchemaStatements) = %d, want %d", len(postgresSchemaStatements), goldenPostgresSchemaStatementCount)
	}
	for i, statement := range statements {
		if strings.TrimSpace(statement) == "" {
			t.Fatalf("statement %d is empty", i)
		}
	}
}

func TestPostgresSchemaStatementCountsPerSchema(t *testing.T) {
	counts := make(map[string]int, len(goldenPostgresSchemaStatementCountsPerSchema))
	firstSeenOrder := make([]string, 0, len(goldenPostgresSchemaNameOrder))
	for _, statement := range postgresSchemaStatements {
		if _, known := goldenPostgresSchemaStatementCountsPerSchema[statement.SchemaName]; !known {
			t.Fatalf("statement %d has unknown schema %q", statementIndex(postgresSchemaStatements, statement), statement.SchemaName)
		}
		if _, seen := counts[statement.SchemaName]; !seen {
			firstSeenOrder = append(firstSeenOrder, statement.SchemaName)
		}
		counts[statement.SchemaName]++
	}
	for schemaName, want := range goldenPostgresSchemaStatementCountsPerSchema {
		if got := counts[schemaName]; got != want {
			t.Fatalf("schema %s statement count = %d, want %d", schemaName, got, want)
		}
	}
	if len(counts) != len(goldenPostgresSchemaStatementCountsPerSchema) {
		t.Fatalf("schema count = %d, want %d", len(counts), len(goldenPostgresSchemaStatementCountsPerSchema))
	}
	if strings.Join(firstSeenOrder, ",") != strings.Join(goldenPostgresSchemaNameOrder, ",") {
		t.Fatalf("schema order = %v, want %v", firstSeenOrder, goldenPostgresSchemaNameOrder)
	}
}

func TestPostgresSchemaStatementsAreIdempotencyGuarded(t *testing.T) {
	var doBlocks, alterColumns, droppedColumns, extensions, triggerBlocks, triggers, droppedTriggers int
	for i, statement := range postgresSchemaStatements {
		trimmed := strings.TrimSpace(statement.SQL)
		upper := strings.ToUpper(trimmed)
		if strings.HasPrefix(upper, "CREATE TABLE") || strings.HasPrefix(upper, "CREATE INDEX") ||
			strings.HasPrefix(upper, "CREATE UNIQUE INDEX") || strings.HasPrefix(upper, "CREATE EXTENSION") {
			if !strings.Contains(upper, "IF NOT EXISTS") {
				t.Fatalf("statement %d (%s) lacks IF NOT EXISTS: %.80s", i, statement.Source, trimmed)
			}
		}
		if strings.HasPrefix(upper, "DROP INDEX") && !strings.Contains(upper, "IF EXISTS") {
			t.Fatalf("statement %d (%s) lacks IF EXISTS: %.80s", i, statement.Source, trimmed)
		}
		if strings.HasPrefix(upper, "ALTER TABLE") {
			// 幂等守卫双形态：加列走 ADD COLUMN IF NOT EXISTS；删列走
			// DROP COLUMN IF EXISTS。2026-10-30 B08 撤除全部 12 条幂等收列
			// 语句后当前无删列语句；保留本计数分支，使未来新增删列语句
			// 必须同步更新下方 0 条断言（有意新增破坏性语句须走 golden 评审）。
			switch {
			case strings.Contains(upper, "ADD COLUMN IF NOT EXISTS"):
				alterColumns++
			case strings.Contains(upper, "DROP COLUMN IF EXISTS"):
				droppedColumns++
			default:
				t.Fatalf("statement %d (%s) is an unguarded ALTER TABLE: %.120s", i, statement.Source, trimmed)
			}
		}
		if strings.HasPrefix(upper, "DO ") {
			doBlocks++
			if !strings.Contains(upper, "IS NOT NULL") {
				t.Fatalf("statement %d (%s) is an unguarded DO block", i, statement.Source)
			}
		}
		if strings.HasPrefix(upper, "CREATE EXTENSION") {
			extensions++
		}
		if strings.Contains(upper, "CREATE TRIGGER ") {
			triggerBlocks++
			triggers += strings.Count(statement.SQL, "CREATE TRIGGER ")
			droppedTriggers += strings.Count(statement.SQL, "DROP TRIGGER IF EXISTS ")
		}
	}
	// 87 = 98 - 11：2026-10-12 AI 问答简化批（决策 F）退役 11 条工具绑定
	// ALTER（chat_conversations 的 search/image 三列与 video/audio 四列，
	// 以及 chat_user_tool_preferences 的 video/audio 四列）；其余仍为原
	// 清单 + api_keys.scheduling_preference_id 守卫加列（调度偏好独立实体
	// PLAN-20261012T033000000Z）+ 模型检测超时处罚开关两表
	// timeout_penalty_enabled 幂等 ALTER 各一条。媒体工作室退场批的
	// api_keys.studio_domains_json 幂等 DROP COLUMN 已随 studio 退场契约修订
	// （2026-10-14）移出 ensure（破坏性清理收敛到 --apply-studio-retirement），
	// 仅剩调度偏好 enabled 列退役一条 DROP COLUMN。
	// DO 块 5→6：供应商与上游协议面重构批新增 account_connections 反向外键
	// 守卫 DO 块（account-connections-account-fk，具名约束幂等补挂；PG 外键
	// 目标必须已存在，双方互引只能单边内联，同 api-keys-purpose DO 块守卫
	// 模式）。2026-10-17 复审 blocker 修复批复用既有
	// accounts-health-check-endpoint-mode-pg-check DO 块原位改写（词表
	// 22→25 值），块数保持 6。100→101：用量记录压缩标记批
	// （usage_records compaction_expected 幂等 ALTER）。101→102：media_jobs
	// 归属改列批（owner_system_account_id 幂等 ALTER）。其余口径不变：
	// DROP COLUMN IF EXISTS 12→0 条（2026-10-30 B08 撤除 12 条幂等收列语句：
	// 业务 9——调度偏好 enabled 1 条 + B4① provider 维度 6 条 + B4 两表收尾
	// provider_protocol_profile_id 2 条；codex 3——三表 provider_code），
	// 当前 schema 无删列语句）、1 条 CREATE EXTENSION、2 个含
	// CREATE TRIGGER 的语句块。
	if alterColumns != 102 {
		t.Fatalf("ALTER TABLE ADD COLUMN count = %d, want 102", alterColumns)
	}
	// B08（2026-10-30）撤除 12 条幂等收列语句后为 0；保留计数断言以防新增
	// DROP COLUMN 绕过 golden 评审。
	if droppedColumns != 0 {
		t.Fatalf("ALTER TABLE DROP COLUMN count = %d, want 0 (B08 removed all 12 idempotent DROP COLUMN statements on 2026-10-30)", droppedColumns)
	}
	if doBlocks != 6 {
		t.Fatalf("DO block count = %d, want 6", doBlocks)
	}
	if extensions != 1 {
		t.Fatalf("CREATE EXTENSION count = %d, want 1", extensions)
	}
	if triggerBlocks != 2 {
		t.Fatalf("trigger block count = %d, want 2", triggerBlocks)
	}
	// proxy_profiles 触发器随 2026-10-17 触发器治理（PLAN-20261017T035500000Z）
	// 有意单向移除：CREATE 定义删除、块尾保留 legacy DROP IF EXISTS 清理存量库
	//（随 ensure-schema 重放生效）——允许 DROP 比 CREATE 多（legacy 清理尾巴），
	// 但 CREATE 不得多于 DROP（那是幂等性破坏）。
	if triggers > droppedTriggers {
		t.Fatalf("CREATE TRIGGER count %d exceeds DROP TRIGGER IF EXISTS count %d; trigger creation would not be idempotent", triggers, droppedTriggers)
	}
}

func statementIndex(statements []PGStatement, target PGStatement) int {
	for i, statement := range statements {
		if statement.SchemaName == target.SchemaName && statement.Source == target.Source && statement.SQL == target.SQL {
			return i
		}
	}
	return -1
}

// TestPostgresQualityScheduleIntervalCheckContract pins the widened
// model_quality_schedules.interval_minutes CHECK (lower bound 10 -> 1 minute)
// in both the CREATE TABLE statement and the guarded legacy migration, while
// the three recovery_interval_minutes CHECKs keep the 10-minute floor. The
// DO block cannot be exercised without a live PostgreSQL, so this asserts the
// SQL text (the migration targets pg_get_constraintdef's normalized
// '((interval_minutes >= 10) AND ...)' rendering, verified against a real
// catalog).
func TestPostgresQualityScheduleIntervalCheckContract(t *testing.T) {
	var createTable string
	var migration string
	for _, statement := range postgresSchemaStatements {
		switch {
		case strings.Contains(statement.SQL, "CREATE TABLE IF NOT EXISTS model_quality_schedules"):
			createTable = statement.SQL
		case strings.Contains(statement.SQL, "model_quality_schedules_interval_minutes_range_check"):
			migration = statement.SQL
		}
	}
	if createTable == "" || migration == "" {
		t.Fatalf("missing schedule CREATE TABLE or migration statement")
	}
	if !strings.Contains(createTable, "CHECK (interval_minutes BETWEEN 1 AND 10080)") {
		t.Fatalf("schedule CREATE TABLE must widen interval_minutes to BETWEEN 1 AND 10080")
	}
	recoveryChecks := strings.Count(createTable, "CHECK (recovery_interval_minutes BETWEEN 10 AND 10080)")
	if recoveryChecks != 1 {
		t.Fatalf("schedule CREATE TABLE must keep exactly one recovery_interval_minutes BETWEEN 10 AND 10080 check, got %d", recoveryChecks)
	}
	for _, legacyFragment := range []string{
		"to_regclass('juhe_business.model_quality_schedules') IS NOT NULL",
		"LIKE '%(interval_minutes >= 10)%'",
		"EXECUTE format('ALTER TABLE juhe_business.model_quality_schedules DROP CONSTRAINT %I', legacy_constraint)",
		"ADD CONSTRAINT model_quality_schedules_interval_minutes_range_check CHECK (interval_minutes BETWEEN 1 AND 10080)",
	} {
		if !strings.Contains(migration, legacyFragment) {
			t.Fatalf("interval migration missing fragment %q", legacyFragment)
		}
	}
	if strings.Contains(migration, "recovery_interval_minutes") {
		t.Fatalf("interval migration must not touch recovery_interval_minutes")
	}
	// policies 与 enforcements 的 recovery 下限不受本次放开影响。
	for _, statement := range postgresSchemaStatements {
		if strings.Contains(statement.SQL, "CREATE TABLE IF NOT EXISTS model_quality_policies") &&
			!strings.Contains(statement.SQL, "CHECK (recovery_interval_minutes BETWEEN 10 AND 10080)") {
			t.Fatalf("model_quality_policies must keep the 10-minute recovery floor")
		}
		if strings.Contains(statement.SQL, "CREATE TABLE IF NOT EXISTS account_quality_enforcements") &&
			!strings.Contains(statement.SQL, "CHECK (recovery_interval_minutes BETWEEN 10 AND 10080)") {
			t.Fatalf("account_quality_enforcements must keep the 10-minute recovery floor")
		}
	}
}

// TestPostgresAccountsHealthCheckContract pins 复审 blocker B-1/B-2（设计 §10
// 与 :114 硬规则）：accounts 的 health_check_connection_id /
// health_check_endpoint_mode / health_check_model 三列 nullable（NOT NULL +
// 复合外键会让 fresh 库任何插入顺序都无法创建账户；创建事务先插 accounts 暂
// NULL、事务内同一条 UPDATE 补齐）、health_check_endpoint_mode 列级 CHECK
// 精确覆盖 §2.2 的 25 值、复合外键 (id, health_check_connection_id) 保留，
// CHECK 迁移 DO 块按 message_token_counting 判别旧 22 值（及更早）约束。
// DO 块无法在无服务环境下执行，与 interval CHECK 契约测试同口径只断言 SQL
// 文本；三列 nullable 的实际插入顺序行为由 SQLite 同构 DDL 测试
// （TestSQLiteAccountsHealthCheckNullabilityAndForeignKey）覆盖。
func TestPostgresAccountsHealthCheckContract(t *testing.T) {
	var createAccounts, modeMigration, dirtyTriggers string
	for _, statement := range postgresSchemaStatements {
		switch {
		case statement.Source == "business" && strings.Contains(statement.SQL, "CREATE TABLE IF NOT EXISTS accounts ("):
			createAccounts = statement.SQL
		case statement.Source == "accounts-health-check-endpoint-mode-pg-check":
			modeMigration = statement.SQL
		case statement.Source == "account-list-projection-pg-dirty-triggers":
			dirtyTriggers = statement.SQL
		}
	}
	if createAccounts == "" || modeMigration == "" || dirtyTriggers == "" {
		t.Fatalf("missing accounts CREATE TABLE, endpoint-mode migration or dirty trigger statement")
	}
	// B-1：三列必须 nullable，不得保留 NOT NULL。
	for _, fragment := range []string{
		"health_check_connection_id text NOT NULL",
		"health_check_model text NOT NULL",
		"health_check_endpoint_mode text NOT NULL",
	} {
		if strings.Contains(createAccounts, fragment) {
			t.Fatalf("accounts CREATE TABLE must keep %q nullable (设计 §10 硬规则)", fragment)
		}
	}
	if !strings.Contains(createAccounts, "health_check_connection_id text,") ||
		!strings.Contains(createAccounts, "health_check_model text,") ||
		!strings.Contains(createAccounts, "health_check_endpoint_mode text CHECK (") {
		t.Fatalf("accounts CREATE TABLE missing the nullable health-check triple declarations")
	}
	// 复合外键保留（PG MATCH SIMPLE：任一列为 NULL 跳过检查，NULL 暂存语义正确）。
	if !strings.Contains(createAccounts, "FOREIGN KEY (id, health_check_connection_id) REFERENCES account_connections(account_id, id)") {
		t.Fatalf("accounts CREATE TABLE must keep the composite health-check FK")
	}
	// B-2：建表内联 CHECK 覆盖 §2.2 全 25 值（accounts 语句中 mode 值只出现在
	// health_check_endpoint_mode CHECK 词表内）。
	for _, mode := range sqliteHealthCheckEndpointModeVocabulary {
		if !strings.Contains(createAccounts, "'"+mode+"'") {
			t.Fatalf("accounts health_check_endpoint_mode CHECK must cover %q (25 值词表)", mode)
		}
	}
	// DO 块：判别谓词改为按 25 值新值定位旧约束，具名重建同样覆盖 25 值。
	if !strings.Contains(modeMigration, `NOT LIKE '%message_token_counting%'`) {
		t.Fatalf("endpoint-mode migration must locate legacy 22-value CHECKs via message_token_counting")
	}
	if strings.Contains(modeMigration, `NOT LIKE '%realtime_session%'`) {
		t.Fatalf("endpoint-mode migration must drop the retired realtime-only legacy predicate")
	}
	for _, mode := range sqliteHealthCheckEndpointModeVocabulary {
		if !strings.Contains(modeMigration, "''"+mode+"''") {
			t.Fatalf("endpoint-mode migration named CHECK must cover %q (25 值词表)", mode)
		}
	}
	// 顺手项：dirty 触发器 ROW() 比较纳入协议面新列与健康检查连接列。
	for _, fragment := range []string{
		"new_accounts.vendor_code", "old_accounts.vendor_code",
		"new_accounts.connection_driver_code", "old_accounts.connection_driver_code",
		"new_accounts.oauth_type", "old_accounts.oauth_type",
		"new_accounts.health_check_connection_id", "old_accounts.health_check_connection_id",
	} {
		if !strings.Contains(dirtyTriggers, fragment) {
			t.Fatalf("accounts update dirty trigger ROW() comparison must include %s", fragment)
		}
	}
}

// sqliteHealthCheckEndpointModeVocabulary 是设计 §2.2 的 25 值全词表
// （与 account_endpoint_modes CHECK、accounts.health_check_endpoint_mode
// CHECK、registry capabilityDomainLanes 并集同源）。
var sqliteHealthCheckEndpointModeVocabulary = []string{
	"images_json", "chat_json", "chat_sse",
	"responses_json", "responses_sse",
	"messages_json", "messages_sse", "message_token_counting",
	"generate_content_json", "generate_content_sse",
	"interactions_json", "interactions_sse", "count_tokens", "embed_content",
	"audio_speech", "audio_transcription_json",
	"video_create", "video_get", "video_content", "video_cancel",
	"audio_job_create", "audio_job_get", "audio_job_content", "audio_job_cancel",
	"realtime_session",
}

func TestPostgresSeedPasswordHashFormat(t *testing.T) {
	hash, err := hashSeedPassword("admin")
	if err != nil {
		t.Fatalf("hashSeedPassword: %v", err)
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 5 {
		t.Fatalf("hash parts = %d, want 5: %q", len(parts), hash)
	}
	if parts[0] != "pbkdf2" || parts[1] != "sha512" || parts[2] != strconv.Itoa(pgSeedPasswordIterations) {
		t.Fatalf("unexpected hash header %q", hash)
	}
	salt, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		t.Fatalf("decode salt: %v", err)
	}
	derived, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil {
		t.Fatalf("decode digest: %v", err)
	}
	if len(salt) != 16 {
		t.Fatalf("salt length = %d, want 16", len(salt))
	}
	if len(derived) != 32 {
		t.Fatalf("digest length = %d, want 32", len(derived))
	}
	// Node hashPassword / Go gateway verifyNodePBKDF2Password both derive from
	// the base64url salt TEXT bytes — the historical defect derived from the
	// raw decoded salt bytes, producing hashes neither runtime could verify.
	recomputed, err := pbkdf2.Key(sha512.New, "admin", []byte(parts[3]), pgSeedPasswordIterations, 32)
	if err != nil {
		t.Fatalf("recompute pbkdf2: %v", err)
	}
	if !bytes.Equal(recomputed, derived) {
		t.Fatal("recomputed digest does not match the stored digest")
	}
}

// TestSeedPasswordHashMatchesNodeVerifySemantics is the seed-interop
// regression: a Go seed hash must verify under the exact byte semantics of
// Node verifyPassword, mirrored byte-for-byte here from backend
// src/storage/crypto.ts (pbkdf2Sync(password, saltText, ...)) and the Go
// gateway modelcheckauth.verifyNodePBKDF2Password (pbkdf2 over []byte(salt
// text)). The seed self-test previously replayed the same raw-salt mistake as
// hashSeedPassword, so the incompatible hash went unnoticed.
func TestSeedPasswordHashMatchesNodeVerifySemantics(t *testing.T) {
	const password = "admin"
	hash, err := hashSeedPassword(password)
	if err != nil {
		t.Fatalf("hashSeedPassword: %v", err)
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 5 {
		t.Fatalf("hash parts = %d, want 5: %q", len(parts), hash)
	}
	iterations, err := strconv.Atoi(parts[2])
	if err != nil {
		t.Fatalf("parse iterations: %v", err)
	}
	expected, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil {
		t.Fatalf("decode digest: %v", err)
	}
	// Node verifyPassword: pbkdf2Sync(password, salt, iterations, len, sha512)
	// where salt is the base64url TEXT (parts[3]).
	derived, err := pbkdf2.Key(sha512.New, password, []byte(parts[3]), iterations, len(expected))
	if err != nil {
		t.Fatalf("derive with node semantics: %v", err)
	}
	if !bytes.Equal(derived, expected) {
		t.Fatal("seed hash must verify under the Node base64url-salt-text semantics")
	}
	// The raw-salt-bytes interpretation must NOT verify: if it does, the seed
	// has regressed to the incompatible derivation.
	rawSalt, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		t.Fatalf("decode salt: %v", err)
	}
	if legacy, err := pbkdf2.Key(sha512.New, password, rawSalt, iterations, len(expected)); err == nil && bytes.Equal(legacy, expected) {
		t.Fatal("seed hash unexpectedly verifies under raw-salt-byte derivation; base64url text semantics regressed")
	}
}

func TestPostgresSeedDataParity(t *testing.T) {
	// The default seed data must stay aligned with the schema objects it feeds.
	// 供应商与上游协议面重构（设计 §1.1/§8/§9）：vendors 恰为 9 个真实厂商
	//（openai 兼容行与 hybrid 行随旧供应商维度删除）；provider_protocol_
	// profiles 及其 family 绑定 seed 随旧表退役；默认分组无供应商维度，组名
	// 收敛为「默认分组」。
	if len(pgSeedProviders) != 9 {
		t.Fatalf("vendor seeds = %d, want 9", len(pgSeedProviders))
	}
	wantVendors := map[string]bool{
		"gpt": true, "xai": true, "deepseek": true, "anthropic": true, "gemini": true,
		"glm": true, "minimax": true, "volcengine": true, "qwen": true,
	}
	seenVendors := map[string]bool{}
	for _, vendor := range pgSeedProviders {
		if seenVendors[vendor.Code] {
			t.Fatalf("duplicate vendor seed %q", vendor.Code)
		}
		seenVendors[vendor.Code] = true
		if !wantVendors[vendor.Code] {
			t.Fatalf("unexpected vendor seed %q (must be one of the §1.1 nine)", vendor.Code)
		}
	}
	if len(seenVendors) != len(wantVendors) {
		t.Fatalf("vendor seeds %v miss §1.1 codes", seenVendors)
	}
	if len(pgSeedProtocols) != 3 {
		t.Fatalf("protocol seeds = %d, want 3", len(pgSeedProtocols))
	}
	if len(pgSeedEndpointFamilies) != 13 {
		t.Fatalf("endpoint family seeds = %d, want 13", len(pgSeedEndpointFamilies))
	}
	// 2026-10-07 默认资源收口：默认分组 seed 收敛为一项；供应商维度删除后
	// 组名收敛为「默认分组」。
	if len(pgSeedGroups) != 1 {
		t.Fatalf("group seeds = %d, want 1", len(pgSeedGroups))
	}
	if pgSeedGroups[0].SystemAccountID != "sys_admin" || pgSeedGroups[0].Name != "默认分组" {
		t.Fatalf("sole group seed = %+v, want the sys_admin 默认分组", pgSeedGroups[0])
	}
	if len(pgSeedGlobalSettings) != 2 {
		t.Fatalf("global settings = %d, want 2", len(pgSeedGlobalSettings))
	}
	// system settings 71→67：-4 来自 2026-10-14 监控改造批删除
	// systemMetricsSampleIntervalSeconds / tableMonitorMaxTablesPerRun /
	// systemMetricsRetentionDays / systemMetricsHourlyRetentionDays 四键
	//（f1991b708 实现侧已删、本计数漏改；2026-10-17 专门批取证回正）。
	// system settings 68→71：+3 来自 M5b realtime 会话生命周期键与
	// 2026-10-02 日志与审计 retention 批（与 sqlite 侧钉值同源）；
	// 71→72：+1 来自 2026-10-08 客户端版本自动跟版键（与 sqlite 侧钉值同源）；
	// 72→71：-1 来自 2026-10-12 AI 问答简化批——生图总超时键
	// chatImageGenerationTotalTimeoutSeconds 随生图工具退场。
	// 67→69：+2 来自调度慢失败直切与墙钟收尾修复设计 §5（快速失败耗时门槛
	// 与尝试启动最小剩余窗口两键，与 sqlite 侧钉值同源）。
	if len(pgSeedSystemSettings) != 69 {
		t.Fatalf("system settings = %d, want 69", len(pgSeedSystemSettings))
	}
	for _, key := range []string{"appName", "appIcon"} {
		found := false
		for _, setting := range pgSeedGlobalSettings {
			if setting.Key == key {
				found = true
			}
		}
		if !found {
			t.Fatalf("global settings missing key %q", key)
		}
	}
}

// TestEnsurePostgresIdempotentSmoke is the opt-in PostgreSQL integration test.
// It only runs when JUHE_AI_PG_SCHEMA_SMOKE_URL points at a disposable
// PostgreSQL database; otherwise it is skipped so plain `go test` stays green.
func TestEnsurePostgresIdempotentSmoke(t *testing.T) {
	databaseURL := os.Getenv("JUHE_AI_PG_SCHEMA_SMOKE_URL")
	if databaseURL == "" {
		t.Skip("JUHE_AI_PG_SCHEMA_SMOKE_URL not set; PostgreSQL schema smoke test skipped")
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open pgx: %v", err)
	}
	defer db.Close()
	if err := db.PingContext(ctxWithTimeout(t)); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}

	ctx := ctxWithTimeout(t)

	result1, err := EnsurePostgres(ctx, db)
	if err != nil {
		t.Fatalf("first EnsurePostgres: %v", err)
	}
	if result1.StatementCount != goldenPostgresSchemaStatementCount {
		t.Fatalf("first EnsurePostgres statement count = %d, want %d", result1.StatementCount, goldenPostgresSchemaStatementCount)
	}
	if result1.SchemaCount != len(goldenPostgresSchemaNameOrder) {
		t.Fatalf("first EnsurePostgres schema count = %d, want %d", result1.SchemaCount, len(goldenPostgresSchemaNameOrder))
	}
	tableCount1, err := countPGSmokeTables(ctx, db)
	if err != nil {
		t.Fatalf("count tables after first EnsurePostgres: %v", err)
	}
	if tableCount1 == 0 {
		t.Fatal("no tables were created in the juhe_* schemas")
	}

	result2, err := EnsurePostgres(ctx, db)
	if err != nil {
		t.Fatalf("second EnsurePostgres (idempotency): %v", err)
	}
	if result2 != result1 {
		t.Fatalf("second EnsurePostgres result = %+v, want %+v", result2, result1)
	}
	tableCount2, err := countPGSmokeTables(ctx, db)
	if err != nil {
		t.Fatalf("count tables after second EnsurePostgres: %v", err)
	}
	if tableCount2 != tableCount1 {
		t.Fatalf("table count changed between runs: %d -> %d", tableCount1, tableCount2)
	}

	seed1, err := EnsurePostgresSeeds(ctx, db)
	if err != nil {
		t.Fatalf("first EnsurePostgresSeeds: %v", err)
	}
	if seed1.StatementCount == 0 {
		t.Fatal("first EnsurePostgresSeeds executed no statements")
	}
	seed2, err := EnsurePostgresSeeds(ctx, db)
	if err != nil {
		t.Fatalf("second EnsurePostgresSeeds (idempotency): %v", err)
	}
	if seed2.StatementCount != seed1.StatementCount {
		t.Fatalf("seed statement count changed between runs: %d -> %d", seed1.StatementCount, seed2.StatementCount)
	}

	var adminRows int
	var mustChangePassword int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*), coalesce(max(must_change_password), 0) FROM "juhe_business"."system_accounts" WHERE id = 'sys_admin' AND username = 'admin'`,
	).Scan(&adminRows, &mustChangePassword); err != nil {
		t.Fatalf("query seeded admin account: %v", err)
	}
	if adminRows != 1 {
		t.Fatalf("seeded admin account rows = %d, want 1", adminRows)
	}
	// BUG-0224: the seeded super admin must carry must_change_password = 1 so
	// the first login hits the must_change_password 403 gate.
	if mustChangePassword != 1 {
		t.Fatalf("seeded admin must_change_password = %d, want 1", mustChangePassword)
	}
}

func ctxWithTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func countPGSmokeTables(ctx context.Context, db *sql.DB) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema IN ('juhe_business', 'juhe_chat', 'juhe_dataset', 'juhe_usage', 'juhe_stats', 'juhe_codex_context')`).Scan(&count)
	return count, err
}
