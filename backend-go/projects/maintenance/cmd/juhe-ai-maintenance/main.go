package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/accountdefaultresources"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/chatbindingmigration"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3aproxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3bmodelcheck"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/mockdata"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/version"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	// 出口 0 走正常返回（等价于以状态 0 退出），非零出口才 os.Exit；进程内
	// 直调 runMaintenance 的任意出口分支因此都不终止宿主进程（测试单进程纪律，
	// 见 docs/develop/后端测试分层规则.md「测试进程纪律（硬性）」）。
	if code := runMaintenance(os.Args[1:]); code != 0 {
		os.Exit(code)
	}
}

// runMaintenance carries the original main() body with every exit converted
// into a returned code; main() is the only remaining os.Exit site, so the
// whole dispatch stays callable in-process from tests (wm_main_exit_branches_test.go).
// The CLI contract (flags, exit codes, stdout/stderr text) is unchanged branch
// by branch. Flags are registered on a fresh flag.ContinueOnError FlagSet per
// call: ExitOnError would os.Exit straight out of the host process on parse
// errors, while ContinueOnError turns them into a returned code — flag itself
// already prints the parse error and usage to stderr before returning, and
// ErrHelp maps back to exit 0 to keep the old ExitOnError contract.
//
// 2026-10-24 SQLite 退役（PLAN-20261024T051925945Z B4）：--driver/--paths
// 旗标族与 --apply-j3b-model-check-sqlite、--chat-sqlite-path 一并删除；
// 全部存储型命令收敛为 --dsn 单形态。
func runMaintenance(argv []string) int {
	// upgrade / check-config 显式子命令分发（升级兼容与迁移账本设计 §9.1）：
	// Go flag 在首个非 flag 参数处停止解析，单层 FlagSet 无法承载子命令；
	// 这里在旗标解析前剥离子命令并交独立 FlagSet。
	if len(argv) > 0 && argv[0] == "upgrade" {
		return runUpgradeCommand(argv[1:])
	}
	if len(argv) > 0 && argv[0] == "check-config" {
		return runCheckConfigCommand(argv[1:])
	}
	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	printVersion := fs.Bool("version", false, "print the maintenance build version, commit and schema revision")
	check := fs.Bool("check-boundary", false, "verify the scaffold boundary")
	j3Apply := fs.Bool("apply-j3a-proxy-latency-postgres", false, "add missing J3a PostgreSQL jobs tables/indexes after explicit authorization (reads JUHE_AI_MAINTENANCE_J3A_POSTGRES_URL)")
	j3bApply := fs.Bool("apply-j3b-model-check-postgres", false, "add missing J3b PostgreSQL juhe_j3b tables/indexes after explicit authorization (reads JUHE_AI_MAINTENANCE_J3B_POSTGRES_URL)")
	accountBalanceApply := fs.Bool("apply-account-balance-postgres", false, "add missing J2 account-balance PostgreSQL juhe_jobs tables/indexes after explicit authorization (reads JUHE_AI_MAINTENANCE_ACCOUNT_BALANCE_POSTGRES_URL)")
	applyStudioRetirement := fs.Bool("apply-studio-retirement", false, "explicit cleanup of legacy media-studio artifacts after stop and backup confirmations; refuses before writes while studio API Keys still own OpenAI-compatible files/vector resources; storage via --dsn")
	dropRetiredMonitoringTables := fs.Bool("drop-retired-monitoring-tables", false, "one-shot idempotent DROP of the 12 retired monitoring tables (9 stats tables + 3 table-monitor tables) after stop and backup confirmations; storage via --dsn")
	ensureAccountDefaults := fs.Bool("ensure-account-default-resources", false, "idempotent additive backfill of the default group/route-strategy/API-key family for existing system accounts created before the 2026-10-07 default-resources cutover (only INSERTs what is missing; rerun writes nothing; requires --dsn, --secret and --backup-confirmed)")
	nodeStopped := fs.Bool("node-stopped", false, "confirm Node writers are stopped for an offline migration")
	goStopped := fs.Bool("go-stopped", false, "confirm Go owners are stopped for an offline migration")
	backupConfirmed := fs.Bool("backup-confirmed", false, "confirm a recoverable backup was verified")
	ensureSchema := fs.Bool("ensure-schema", false, "idempotently apply the full PostgreSQL schema (requires --dsn)")
	seedDefaults := fs.Bool("seed", false, "idempotently run the default seed (PostgreSQL; runs after --ensure-schema when both are set)")
	bootstrapDSN := fs.String("dsn", "", "postgres URL for storage-bound commands")
	seedSecret := fs.String("secret", "", "seed encryption secret (or JUHE_AI_SECRET); empty selects the Node dev default")
	migrateChatBinding := fs.Bool("migrate-chat-account-only-binding", false, `one-shot idempotent migration of chat_conversations to the account-only binding model (docs/functions/AI问答会话账户唯一绑定设计.md): rows with bind_mode IS NULL OR bind_mode <> 'account' are stamped archived=1, the bind_mode/bind_group_id/bind_group_name_snapshot data is backed up to chat_conversations_bind_legacy_backup and the three columns are dropped (PostgreSQL uses DROP COLUMN IF EXISTS). Requires --dsn URL. The report JSON carries the rollback SQL (recreate the three columns from the backup table after rolling the code back): ALTER TABLE juhe_chat.chat_conversations ADD COLUMN IF NOT EXISTS bind_mode text NOT NULL DEFAULT 'api_key'; ADD COLUMN IF NOT EXISTS bind_group_id text; ADD COLUMN IF NOT EXISTS bind_group_name_snapshot text; UPDATE juhe_chat.chat_conversations c SET bind_mode = COALESCE(b.bind_mode, 'api_key'), bind_group_id = b.bind_group_id, bind_group_name_snapshot = b.bind_group_name_snapshot FROM juhe_chat.chat_conversations_bind_legacy_backup b WHERE b.id = c.id`)
	postgresSchemaSnapshot := fs.Bool("postgres-schema-snapshot", false, "read-only PostgreSQL schema snapshot JSON to stdout (requires JUHE_AI_SCHEMA_SNAPSHOT_TARGET=production|test, JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL and JUHE_AI_SCHEMA_SNAPSHOT_READ_ONLY_CONFIRM=READ_ONLY)")
	mockdataRun := fs.Bool("mockdata", false, "idempotently rebuild the development mockdata set in the target PostgreSQL database (schema ensure + seed + cleanup + per-domain seeding + summary; requires --dsn)")
	mockdataVerifyCoverage := fs.Bool("verify-mockdata-coverage", false, "read-only verify that every wired mockdata domain store table is non-empty (allowlisted runtime-state tables excepted) and that the key coverage assertions hold (requires --dsn; exit 3 when not ready)")
	mockdataDays := fs.Int("mockdata-days", mockdata.DefaultDays, "mockdata history span in days for detail and monitoring samples (1..90)")
	mockdataDailyRequests := fs.Int("mockdata-daily-requests", mockdata.DefaultDailyRequests, "mockdata usage records generated per day (1..500)")
	mockdataSummaryFile := fs.String("mockdata-summary-file", "", "optional mockdata summary JSON path: --mockdata writes it, --verify-mockdata-coverage reads the domain-wiring snapshot from it (no implicit fallback)")
	rebuildUsageStats := fs.Bool("rebuild-usage-stats", false, "offline rebuild of the usage statistics cache from usage_records (requires --confirm-offline or JUHE_AI_CONFIRM_USAGE_STATS_REBUILD=1; gateway/jobs must be stopped; business database and usage_records stay read-only; BUG-0182)")
	confirmOffline := fs.Bool("confirm-offline", false, "confirm an offline window for --rebuild-usage-stats (gateway/jobs stopped); alternatively set JUHE_AI_CONFIRM_USAGE_STATS_REBUILD=1")
	rebuildBatchSize := fs.Int("batch-size", 2000, "rebuild-usage-stats aggregation batch size (clamped to 1..50000; default 2000)")
	rebuildMaxBatches := fs.Int("max-batches", 1000, "rebuild-usage-stats aggregation batch cap per run (clamped to 1..10000; default 1000; rerun to continue when the cap is hit)")
	applySuccessCostBackfill := fs.Bool("apply-success-cost-backfill", false, "one-shot idempotent backfill of usage_stats success_cost_usd from total_cost_usd for the six stats tables (maintenance window with jobs stopped; run --ensure-schema first to create the backfill_progress marker table; requires --dsn; resumable from the persisted cursor)")
	backfillDryRun := fs.Bool("dry-run", false, "read-only dry-run: with --apply-success-cost-backfill prints the pending-row count per table (never touches the backfill_progress marker); also selects dry-run for other one-shot migration commands")
	importLegacyBusiness := fs.Bool("import-legacy-business", false, `business database rebuild backfill (2026-10-22 cutover): reads a legacy provider-era database via --legacy-dsn and writes business configuration rows (accounts/groups/routing/keys/authorizations/settings/teams/tags/integrations) into the freshly ensured target --dsn; historical usage/audit/statistics/model-check domains are intentionally discarded; --dry-run runs the whole transaction and rolls back`)
	legacyDSN := fs.String("legacy-dsn", "", "legacy provider-era PostgreSQL URL read by --import-legacy-business")
	manifestOut := fs.String("manifest-out", "", "manifest JSON output path for one-shot migration commands (required with --migrate-merge-super-groups/--migrate-upstream-base-url-version-root; the only dry-run write)")
	migrateMergeSuperGroups := fs.Bool("migrate-merge-super-groups", false, "merge route-strategy migration to super groups (超级分组设计 §6): with --dry-run writes the reviewable manifest to --manifest-out; without --dry-run requires --node-stopped --go-stopped --backup-confirmed and applies mapped strategies in one business transaction (--dsn)")
	migrationBatchID := fs.String("migration-batch-id", "", "stable migration batch id for --migrate-merge-super-groups (manifest reuse key)")
	migrateAccountCapabilityDomain := fs.Bool("migrate-account-capability-domain", false, "AI account capability-domain migration (PLAN-20261011T141004000Z): requires --account-capability-domain-stage=m1|m2-cutover plus --node-stopped --go-stopped --backup-confirmed; storage via --dsn")
	verifyAccountCapabilityDomain := fs.Bool("verify-account-capability-domain", false, "read-only AI account capability-domain verification: requires --account-capability-domain-stage=m1|m2-cutover and --account-capability-domain-check=preflight|postflight (preflight tolerates a missing column, postflight requires it)")
	accountCapabilityDomainStage := fs.String("account-capability-domain-stage", "", "migration target for --migrate-account-capability-domain/--verify-account-capability-domain: m1 or m2-cutover")
	accountCapabilityDomainCheck := fs.String("account-capability-domain-check", "", "verification mode for --verify-account-capability-domain: preflight or postflight")
	migrateSchedulingPreferences := fs.Bool("migrate-scheduling-preferences", false, "scheduling-preferences one-shot migration (PLAN-20261012T033000000Z P2): requires --node-stopped --go-stopped --backup-confirmed; storage via --dsn; resumable (rerun continues, NULL judgment is idempotent)")
	verifySchedulingPreferences := fs.Bool("verify-scheduling-preferences", false, "read-only scheduling-preferences verification: requires --scheduling-preferences-check=preflight (pre-migration report, exit 0) or postflight (completed-state check)")
	schedulingPreferencesCheck := fs.String("scheduling-preferences-check", "", "verification mode for --verify-scheduling-preferences: preflight or postflight")
	migrateUpstreamBaseURLVersionRoot := fs.Bool("migrate-upstream-base-url-version-root", false, "upstream base URL version-root backfill (上游 Base URL 版本根语义 v2): with --dry-run scans juhe_business account_connections.base_url and accounts.credentials_encrypted read-only and writes the reviewable manifest to --manifest-out (the only dry-run write; non-conforming values land in the review bucket untouched); without --dry-run requires --node-stopped --go-stopped --backup-confirmed and applies the planned rewrites in one business transaction (--dsn; conditional on the old value, idempotent rerun)")
	verifyUpstreamBaseURLVersionRoot := fs.Bool("verify-upstream-base-url-version-root", false, "read-only upstream base URL verification: requires --upstream-base-url-check=preflight (pre-migration classification report, exit 0) or postflight (migrated-state check; residual rewrite/fill rows fail readiness with exit 3)")
	upstreamBaseURLCheck := fs.String("upstream-base-url-check", "", "verification mode for --verify-upstream-base-url-version-root: preflight or postflight")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *applyStudioRetirement && (*printVersion || *check || *backfillDryRun || *j3Apply || *j3bApply || *accountBalanceApply || *dropRetiredMonitoringTables || *migrateUpstreamBaseURLVersionRoot || *verifyUpstreamBaseURLVersionRoot || *ensureAccountDefaults) {
		fmt.Fprintln(os.Stderr, "studio retirement is mutually exclusive with other maintenance commands")
		return 2
	}
	if *dropRetiredMonitoringTables && (*printVersion || *check || *backfillDryRun || *j3Apply || *j3bApply || *accountBalanceApply || *applyStudioRetirement || *migrateUpstreamBaseURLVersionRoot || *verifyUpstreamBaseURLVersionRoot || *ensureAccountDefaults) {
		fmt.Fprintln(os.Stderr, "drop-retired-monitoring-tables is mutually exclusive with other maintenance commands")
		return 2
	}
	if *migrateAccountCapabilityDomain || *verifyAccountCapabilityDomain {
		if *migrateAccountCapabilityDomain && *verifyAccountCapabilityDomain {
			fmt.Fprintln(os.Stderr, "capability-domain migrate and verify flags are mutually exclusive")
			return 2
		}
		if *postgresSchemaSnapshot || *ensureSchema || *seedDefaults || *printVersion || *check || *applyStudioRetirement || *j3Apply || *j3bApply || *accountBalanceApply || *mockdataRun || *mockdataVerifyCoverage || *rebuildUsageStats || *migrateChatBinding || *applySuccessCostBackfill || *migrateSchedulingPreferences || *verifySchedulingPreferences || *dropRetiredMonitoringTables || *migrateUpstreamBaseURLVersionRoot || *verifyUpstreamBaseURLVersionRoot {
			fmt.Fprintln(os.Stderr, "capability domain migration flags are mutually exclusive with other maintenance commands")
			return 2
		}
		if *migrateAccountCapabilityDomain {
			return accountCapabilityDomainMigrateResult(*bootstrapDSN, *seedSecret, *accountCapabilityDomainStage, *nodeStopped, *goStopped, *backupConfirmed)
		}
		return accountCapabilityDomainVerifyResult(*bootstrapDSN, *seedSecret, *accountCapabilityDomainStage, *accountCapabilityDomainCheck)
	}
	if *migrateSchedulingPreferences || *verifySchedulingPreferences {
		if *migrateSchedulingPreferences && *verifySchedulingPreferences {
			fmt.Fprintln(os.Stderr, "scheduling-preferences migrate and verify flags are mutually exclusive")
			return 2
		}
		if *postgresSchemaSnapshot || *ensureSchema || *seedDefaults || *printVersion || *check || *applyStudioRetirement || *j3Apply || *j3bApply || *accountBalanceApply || *mockdataRun || *mockdataVerifyCoverage || *rebuildUsageStats || *migrateChatBinding || *applySuccessCostBackfill || *dropRetiredMonitoringTables || *migrateUpstreamBaseURLVersionRoot || *verifyUpstreamBaseURLVersionRoot {
			fmt.Fprintln(os.Stderr, "scheduling preferences migration flags are mutually exclusive with other maintenance commands")
			return 2
		}
		if *migrateSchedulingPreferences {
			return schedulingPreferencesMigrateResult(*bootstrapDSN, *nodeStopped, *goStopped, *backupConfirmed)
		}
		return schedulingPreferencesVerifyResult(*bootstrapDSN, *schedulingPreferencesCheck)
	}
	if *migrateUpstreamBaseURLVersionRoot || *verifyUpstreamBaseURLVersionRoot {
		if *migrateUpstreamBaseURLVersionRoot && *verifyUpstreamBaseURLVersionRoot {
			fmt.Fprintln(os.Stderr, "upstream base URL migrate and verify flags are mutually exclusive")
			return 2
		}
		if *postgresSchemaSnapshot || *ensureSchema || *seedDefaults || *printVersion || *check || *applyStudioRetirement || *j3Apply || *j3bApply || *accountBalanceApply || *mockdataRun || *mockdataVerifyCoverage || *rebuildUsageStats || *migrateChatBinding || *applySuccessCostBackfill || *dropRetiredMonitoringTables || *migrateAccountCapabilityDomain || *verifyAccountCapabilityDomain || *migrateSchedulingPreferences || *verifySchedulingPreferences || *migrateMergeSuperGroups || *importLegacyBusiness {
			fmt.Fprintln(os.Stderr, "upstream base URL migration flags are mutually exclusive with other maintenance commands")
			return 2
		}
		if *migrateUpstreamBaseURLVersionRoot {
			return upstreamBaseURLMigrationResult(*backfillDryRun, *bootstrapDSN, *manifestOut, *seedSecret, *nodeStopped, *goStopped, *backupConfirmed)
		}
		return upstreamBaseURLVerifyResult(*bootstrapDSN, *upstreamBaseURLCheck, *seedSecret)
	}
	if *migrateMergeSuperGroups {
		if *postgresSchemaSnapshot || *ensureSchema || *seedDefaults || *printVersion || *check || *applyStudioRetirement || *j3Apply || *j3bApply || *accountBalanceApply || *mockdataRun || *mockdataVerifyCoverage || *rebuildUsageStats || *applySuccessCostBackfill || *migrateChatBinding || *migrateAccountCapabilityDomain || *verifyAccountCapabilityDomain || *migrateSchedulingPreferences || *verifySchedulingPreferences || *dropRetiredMonitoringTables || *migrateUpstreamBaseURLVersionRoot || *verifyUpstreamBaseURLVersionRoot {
			fmt.Fprintln(os.Stderr, "merge→super migration flags are mutually exclusive with other maintenance commands")
			return 2
		}
		return mergeSuperGroupsMigrationResult(*backfillDryRun, *bootstrapDSN, *manifestOut, *migrationBatchID, *nodeStopped, *goStopped, *backupConfirmed)
	}
	if *importLegacyBusiness {
		if *postgresSchemaSnapshot || *ensureSchema || *seedDefaults || *printVersion || *check || *applyStudioRetirement || *j3Apply || *j3bApply || *accountBalanceApply || *mockdataRun || *mockdataVerifyCoverage || *rebuildUsageStats || *applySuccessCostBackfill || *migrateChatBinding || *migrateAccountCapabilityDomain || *verifyAccountCapabilityDomain || *migrateSchedulingPreferences || *verifySchedulingPreferences || *dropRetiredMonitoringTables || *migrateMergeSuperGroups || *migrateUpstreamBaseURLVersionRoot || *verifyUpstreamBaseURLVersionRoot {
			fmt.Fprintln(os.Stderr, "legacy business import flags are mutually exclusive with other maintenance commands")
			return 2
		}
		return legacyBusinessImportResult(*backfillDryRun, *legacyDSN, *bootstrapDSN, *seedSecret)
	}
	if *migrateChatBinding {
		if *postgresSchemaSnapshot || *ensureSchema || *seedDefaults || *printVersion || *check || *applyStudioRetirement || *j3Apply || *j3bApply || *accountBalanceApply || *mockdataRun || *mockdataVerifyCoverage || *rebuildUsageStats || *applySuccessCostBackfill || *dropRetiredMonitoringTables || *migrateUpstreamBaseURLVersionRoot || *verifyUpstreamBaseURLVersionRoot {
			fmt.Fprintln(os.Stderr, "chat account-only binding migration flag is mutually exclusive with other maintenance commands")
			return 2
		}
		return chatAccountOnlyBindingMigrationResult(*bootstrapDSN)
	}
	if *postgresSchemaSnapshot {
		if *ensureSchema || *seedDefaults || *printVersion || *check || *applyStudioRetirement || *j3Apply || *j3bApply || *accountBalanceApply || *mockdataRun || *mockdataVerifyCoverage || *rebuildUsageStats || *applySuccessCostBackfill || *dropRetiredMonitoringTables || *migrateUpstreamBaseURLVersionRoot || *verifyUpstreamBaseURLVersionRoot {
			fmt.Fprintln(os.Stderr, "PostgreSQL schema snapshot flag is mutually exclusive with other maintenance commands")
			return 2
		}
		return postgresSchemaSnapshotResult()
	}
	if *ensureSchema || *seedDefaults {
		if *postgresSchemaSnapshot || *printVersion || *check || *applyStudioRetirement || *j3Apply || *j3bApply || *accountBalanceApply || *mockdataRun || *mockdataVerifyCoverage || *rebuildUsageStats || *applySuccessCostBackfill || *dropRetiredMonitoringTables || *migrateUpstreamBaseURLVersionRoot || *verifyUpstreamBaseURLVersionRoot {
			fmt.Fprintln(os.Stderr, "storage bootstrap flags are mutually exclusive with other maintenance commands")
			return 2
		}
		return runStorageBootstrap(*ensureSchema, *seedDefaults, *bootstrapDSN, *seedSecret)
	}
	if *mockdataRun || *mockdataVerifyCoverage {
		if *mockdataRun && *mockdataVerifyCoverage {
			fmt.Fprintln(os.Stderr, "mockdata seeding and mockdata coverage verification flags are mutually exclusive")
			return 2
		}
		if *printVersion || *check || *postgresSchemaSnapshot || *ensureSchema || *seedDefaults || *applyStudioRetirement || *j3Apply || *j3bApply || *accountBalanceApply || *rebuildUsageStats || *applySuccessCostBackfill || *dropRetiredMonitoringTables || *migrateUpstreamBaseURLVersionRoot || *verifyUpstreamBaseURLVersionRoot {
			fmt.Fprintln(os.Stderr, "mockdata flags are mutually exclusive with other maintenance commands")
			return 2
		}
		return runMockdataCommand(mockdataCommandFlags{
			VerifyCoverage: *mockdataVerifyCoverage,
			Days:           *mockdataDays,
			DailyRequests:  *mockdataDailyRequests,
			DSN:            *bootstrapDSN,
			Secret:         *seedSecret,
			SummaryFile:    *mockdataSummaryFile,
		})
	}
	if *rebuildUsageStats {
		if *printVersion || *check || *postgresSchemaSnapshot || *ensureSchema || *seedDefaults || *applyStudioRetirement || *j3Apply || *j3bApply || *accountBalanceApply || *mockdataRun || *mockdataVerifyCoverage || *migrateChatBinding || *applySuccessCostBackfill || *dropRetiredMonitoringTables || *migrateUpstreamBaseURLVersionRoot || *verifyUpstreamBaseURLVersionRoot {
			fmt.Fprintln(os.Stderr, "rebuild-usage-stats flag is mutually exclusive with other maintenance commands")
			return 2
		}
		return rebuildUsageStatsResult(*bootstrapDSN, *rebuildBatchSize, *rebuildMaxBatches, *confirmOffline)
	}
	if *applySuccessCostBackfill {
		if *printVersion || *check || *postgresSchemaSnapshot || *ensureSchema || *seedDefaults || *applyStudioRetirement || *j3Apply || *j3bApply || *accountBalanceApply || *mockdataRun || *mockdataVerifyCoverage || *rebuildUsageStats || *migrateChatBinding || *migrateAccountCapabilityDomain || *verifyAccountCapabilityDomain || *dropRetiredMonitoringTables || *migrateUpstreamBaseURLVersionRoot || *verifyUpstreamBaseURLVersionRoot {
			fmt.Fprintln(os.Stderr, "apply-success-cost-backfill flag is mutually exclusive with other maintenance commands")
			return 2
		}
		return applySuccessCostBackfillResult(*bootstrapDSN, *backfillDryRun)
	}
	if *printVersion {
		fmt.Printf("juhe-ai-maintenance version=%s commit=%s buildTime=%s schemaRevision=%d\n", version.Version, version.Commit, version.BuildTime, contracts.SchemaRevision)
		return 0
	}
	if *check {
		fmt.Println("juhe-ai-maintenance boundary=ready runtime=one-shot-scaffold")
		return 0
	}
	// J3a/J3b/J2 account-balance 一次性 bootstrap：dispatch 顺序与基线一致
	// （PG 优先于 J3a，J2 account-balance 随 J3 族之后），apply flag 互斥由
	// 上游各命令分支的互斥链承载。studio 退场显式清理（2026-10-14 契约）
	// 排在 J2 之后；退役监控表清退（--drop-retired-monitoring-tables）与
	// studio 清理同模式，互斥同链。
	if *j3bApply {
		return j3bModelCheckBootstrapResult(*j3bApply)
	}
	if *j3Apply {
		return j3aProxyLatencyBootstrapResult(*j3Apply)
	}
	if *accountBalanceApply {
		return accountBalanceBootstrapResult()
	}
	if *applyStudioRetirement {
		return studioRetirementResult(*bootstrapDSN, *nodeStopped, *goStopped, *backupConfirmed)
	}
	if *dropRetiredMonitoringTables {
		return dropRetiredMonitoringTablesResult(*bootstrapDSN, *nodeStopped, *goStopped, *backupConfirmed)
	}
	if *ensureAccountDefaults {
		if *printVersion || *check || *postgresSchemaSnapshot || *ensureSchema || *seedDefaults || *applyStudioRetirement || *j3Apply || *j3bApply || *accountBalanceApply || *mockdataRun || *mockdataVerifyCoverage || *rebuildUsageStats || *applySuccessCostBackfill || *dropRetiredMonitoringTables || *migrateUpstreamBaseURLVersionRoot || *verifyUpstreamBaseURLVersionRoot || *migrateChatBinding || *migrateAccountCapabilityDomain || *verifyAccountCapabilityDomain || *migrateSchedulingPreferences || *verifySchedulingPreferences || *migrateMergeSuperGroups || *importLegacyBusiness {
			fmt.Fprintln(os.Stderr, "account default resources flags are mutually exclusive with other maintenance commands")
			return 2
		}
		return accountDefaultResourcesResult(*bootstrapDSN, *seedSecret, *backupConfirmed)
	}
	fmt.Fprintln(os.Stderr, "maintenance project runtime is not switched yet; select an explicit one-shot command")
	return 2
}

// chatAccountOnlyBindingMigrationResult returns the CLI exit code;
// runMaintenance dispatches it and tests call it in-process. Usage errors
// (missing --dsn) exit 2, runtime failures exit 1, a not-ready post-state
// (legacy columns still present) exits 3. The rollback SQL is printed next to
// the JSON report (design §10 回滚窗口).
func chatAccountOnlyBindingMigrationResult(dsn string) int {
	parsedDSN := strings.TrimSpace(dsn)
	if parsedDSN == "" {
		fmt.Fprintf(os.Stderr, "--migrate-chat-account-only-binding 需要 --dsn 指向显式的 PostgreSQL URL\n")
		return 2
	}
	db, err := chatbindingmigration.OpenPostgres(parsedDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open chat account-only binding migration connection: %v\n", err)
		return 2
	}
	defer db.Close()
	report, runErr := chatbindingmigration.Run(context.Background(), db, time.Now().UTC())
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "chat account-only binding migration failed: %v\n", runErr)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode chat account-only binding migration report: %v\n", err)
		return 1
	}
	fmt.Println(report.RollbackSQL)
	if !report.Ready() {
		return 3
	}
	return 0
}

// j3bModelCheckBootstrapResult returns the CLI exit code; runMaintenance dispatches it and tests call it in-process.
func j3bModelCheckBootstrapResult(apply bool) int {
	rawURL := strings.TrimSpace(os.Getenv(j3bmodelcheck.BootstrapEnv))
	if rawURL == "" {
		fmt.Fprintf(os.Stderr, "J3b PostgreSQL bootstrap requires %s\n", j3bmodelcheck.BootstrapEnv)
		return 2
	}
	db, err := j3bmodelcheck.Open(rawURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open J3b PostgreSQL bootstrap connection: %v\n", err)
		return 2
	}
	defer db.Close()
	report, runErr := j3bmodelcheck.Run(context.Background(), db, apply)
	return j3bBootstrapOutcomeExitCode(report, runErr)
}

// j3bBootstrapOutcomeExitCode renders the J3b PostgreSQL bootstrap report and
// maps the outcome to the maintenance exit-code contract.
func j3bBootstrapOutcomeExitCode(report j3bmodelcheck.Report, runErr error) int {
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "J3b PostgreSQL bootstrap failed: %v\n", runErr)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3b PostgreSQL bootstrap report: %v\n", err)
		return 1
	}
	if !report.Ready() {
		return 3
	}
	return 0
}

// j3aProxyLatencyBootstrapResult returns the CLI exit code; runMaintenance dispatches it and tests call it in-process.
func j3aProxyLatencyBootstrapResult(apply bool) int {
	rawURL := strings.TrimSpace(os.Getenv(j3aproxylatency.BootstrapEnv))
	if rawURL == "" {
		fmt.Fprintf(os.Stderr, "J3a PostgreSQL bootstrap requires %s\n", j3aproxylatency.BootstrapEnv)
		return 2
	}
	db, err := j3aproxylatency.Open(rawURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open J3a PostgreSQL bootstrap connection: %v\n", err)
		return 2
	}
	defer db.Close()
	report, runErr := j3aproxylatency.Run(context.Background(), db, apply)
	return j3aBootstrapOutcomeExitCode(report, runErr)
}

// j3aBootstrapOutcomeExitCode renders the J3a bootstrap report and maps the
// outcome to the maintenance exit-code contract.
func j3aBootstrapOutcomeExitCode(report j3aproxylatency.Report, runErr error) int {
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "J3a PostgreSQL bootstrap failed: %v\n", runErr)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3a bootstrap report: %v\n", err)
		return 1
	}
	if !report.Ready() {
		return 3
	}
	return 0
}

// accountBalanceBootstrapEnv 对齐 j3a/j3b 的 JUHE_AI_MAINTENANCE_*_POSTGRES_URL
// 家族；accountbalance 是纯库包（连接打开与 CLI 契约留在 maintenance）。
const accountBalanceBootstrapEnv = "JUHE_AI_MAINTENANCE_ACCOUNT_BALANCE_POSTGRES_URL"

// accountBalanceBootstrapResult returns the CLI exit code; runMaintenance dispatches it and tests call it in-process.
func accountBalanceBootstrapResult() int {
	rawURL := strings.TrimSpace(os.Getenv(accountBalanceBootstrapEnv))
	if rawURL == "" {
		fmt.Fprintf(os.Stderr, "J2 account-balance PostgreSQL bootstrap requires %s\n", accountBalanceBootstrapEnv)
		return 2
	}
	db, err := openAccountBalancePostgres(rawURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open J2 account-balance PostgreSQL bootstrap connection: %v\n", err)
		return 2
	}
	defer db.Close()
	report, runErr := accountbalance.BootstrapPostgres(context.Background(), db)
	return accountBalanceBootstrapOutcomeExitCode(report, runErr)
}

// openAccountBalancePostgres mirrors j3aproxylatency.Open: it validates the
// maintenance-only URL shape (postgres scheme, host, database, explicit role)
// before opening a one-shot single-connection pool. It never falls back to
// application URLs.
func openAccountBalancePostgres(rawURL string) (*sql.DB, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return nil, errors.New("J2 account-balance bootstrap 必须提供 postgres/postgresql URL")
	}
	if parsed.Hostname() == "" || strings.Trim(strings.TrimSpace(parsed.Path), "/") == "" || parsed.User == nil || strings.TrimSpace(parsed.User.Username()) == "" {
		return nil, errors.New("J2 account-balance bootstrap URL 必须包含主机、数据库和显式角色")
	}
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		return nil, fmt.Errorf("打开 J2 account-balance bootstrap PostgreSQL 连接失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// accountBalanceBootstrapOutcomeExitCode renders the J2 account-balance
// bootstrap report and maps the outcome to the maintenance exit-code contract.
func accountBalanceBootstrapOutcomeExitCode(report accountbalance.BootstrapReport, runErr error) int {
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "J2 account-balance PostgreSQL bootstrap failed: %v\n", runErr)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J2 account-balance bootstrap report: %v\n", err)
		return 1
	}
	if !report.Ready() {
		return 3
	}
	return 0
}

// accountDefaultResourcesResult returns the CLI exit code; runMaintenance
// dispatches it and tests call it in-process. Usage errors (missing --dsn,
// secret, or --backup-confirmed) exit 2, runtime failures exit 1. The secret
// never falls back to the Node dev default: an envelope sealed with the wrong
// secret would be unreadable by the production gateway, so an explicit
// --secret or JUHE_AI_SECRET is mandatory.
func accountDefaultResourcesResult(dsn, secret string, backupConfirmed bool) int {
	if strings.TrimSpace(dsn) == "" {
		fmt.Fprintf(os.Stderr, "--ensure-account-default-resources 需要 --dsn 指向显式的 PostgreSQL URL\n")
		return 2
	}
	if !backupConfirmed {
		fmt.Fprintf(os.Stderr, "--ensure-account-default-resources 需要 --backup-confirmed（执行前先完成可恢复备份确认）\n")
		return 2
	}
	resolvedSecret := strings.TrimSpace(secret)
	if resolvedSecret == "" {
		resolvedSecret = strings.TrimSpace(os.Getenv("JUHE_AI_SECRET"))
	}
	if resolvedSecret == "" {
		fmt.Fprintf(os.Stderr, "--ensure-account-default-resources 需要 --secret（或 JUHE_AI_SECRET 环境变量）以密封默认 API Key；不回落开发默认密钥\n")
		return 2
	}
	db, err := accountdefaultresources.Open(dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open account default resources connection: %v\n", err)
		return 2
	}
	defer db.Close()
	report, runErr := accountdefaultresources.Run(context.Background(), db, accountdefaultresources.Options{Secret: resolvedSecret})
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "account default resources backfill failed: %v\n", runErr)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode account default resources report: %v\n", err)
		return 1
	}
	return 0
}
