package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/chatbindingmigration"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/goruntimemetrics"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3aproxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3bmodelcheck"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/mockdata"
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
func runMaintenance(argv []string) int {
	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	version := fs.Bool("version", false, "print the maintenance project contract version")
	check := fs.Bool("check-boundary", false, "verify the scaffold boundary")
	goRuntimeMetricsCheck := fs.Bool("check-go-runtime-metrics", false, "read-only verify independent Go runtime metrics PostgreSQL schema")
	goRuntimeMetricsApply := fs.Bool("apply-go-runtime-metrics", false, "add missing Go runtime metrics PostgreSQL tables after explicit stop and backup confirmations")
	goRuntimeMetricsURL := fs.String("go-runtime-metrics-postgres-url", "", "explicit maintenance-scoped PostgreSQL URL for Go runtime metrics (or JUHE_AI_MAINTENANCE_GO_RUNTIME_METRICS_POSTGRES_URL)")
	j3Apply := fs.Bool("apply-j3a-proxy-latency-postgres", false, "add missing J3a PostgreSQL jobs tables/indexes after explicit authorization (reads JUHE_AI_MAINTENANCE_J3A_POSTGRES_URL)")
	j3bApply := fs.Bool("apply-j3b-model-check-postgres", false, "add missing J3b PostgreSQL juhe_j3b tables/indexes after explicit authorization (reads JUHE_AI_MAINTENANCE_J3B_POSTGRES_URL)")
	j3bSQLiteApply := fs.Bool("apply-j3b-model-check-sqlite", false, "bootstrap dedicated J3b SQLite schema after stop and backup confirmations (reads JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH)")
	nodeStopped := fs.Bool("node-stopped", false, "confirm Node writers are stopped for an offline migration")
	goStopped := fs.Bool("go-stopped", false, "confirm Go owners are stopped for an offline migration")
	backupConfirmed := fs.Bool("backup-confirmed", false, "confirm a recoverable backup was verified")
	ensureSchema := fs.Bool("ensure-schema", false, "idempotently apply the six-database SQLite schema or the full PostgreSQL schema (with --driver plus --paths/--dsn)")
	seedDefaults := fs.Bool("seed", false, "idempotently run the default seed (business SQLite or PostgreSQL; runs after --ensure-schema when both are set)")
	bootstrapDriver := fs.String("driver", "", "storage driver for --ensure-schema/--seed: sqlite or postgres")
	bootstrapPaths := fs.String("paths", "", "sqlite storage paths: business=...,chat=...,dataset=...,usage-catalog=...,stats=...,codex-context-shard-root=...,codex-context-shard-count=...")
	bootstrapDSN := fs.String("dsn", "", "postgres URL for --ensure-schema/--seed")
	seedSecret := fs.String("secret", "", "seed encryption secret (or JUHE_AI_SECRET); empty selects the Node dev default")
	migrateChatBinding := fs.Bool("migrate-chat-account-only-binding", false, `one-shot idempotent migration of chat_conversations to the account-only binding model (docs/functions/AI问答会话账户唯一绑定设计.md): rows with bind_mode IS NULL OR bind_mode <> 'account' are stamped archived=1, the bind_mode/bind_group_id/bind_group_name_snapshot data is backed up to chat_conversations_bind_legacy_backup and the three columns are dropped (SQLite rebuilds the table because its table CHECK blocks DROP COLUMN; PostgreSQL uses DROP COLUMN IF EXISTS). Requires --driver sqlite --chat-sqlite-path FILE or --driver postgres --dsn URL. The report JSON carries the rollback SQL (recreate the three columns from the backup table after rolling the code back): SQLite = ALTER TABLE chat_conversations ADD COLUMN bind_mode TEXT NOT NULL DEFAULT 'api_key'; ADD COLUMN bind_group_id TEXT; ADD COLUMN bind_group_name_snapshot TEXT; UPDATE chat_conversations SET bind_mode = COALESCE((SELECT b.bind_mode FROM chat_conversations_bind_legacy_backup b WHERE b.id = chat_conversations.id), 'api_key'), bind_group_id = (SELECT b.bind_group_id FROM chat_conversations_bind_legacy_backup b WHERE b.id = chat_conversations.id), bind_group_name_snapshot = (SELECT b.bind_group_name_snapshot FROM chat_conversations_bind_legacy_backup b WHERE b.id = chat_conversations.id); PostgreSQL = ALTER TABLE juhe_chat.chat_conversations ADD COLUMN IF NOT EXISTS bind_mode text NOT NULL DEFAULT 'api_key'; ADD COLUMN IF NOT EXISTS bind_group_id text; ADD COLUMN IF NOT EXISTS bind_group_name_snapshot text; UPDATE juhe_chat.chat_conversations c SET bind_mode = COALESCE(b.bind_mode, 'api_key'), bind_group_id = b.bind_group_id, bind_group_name_snapshot = b.bind_group_name_snapshot FROM juhe_chat.chat_conversations_bind_legacy_backup b WHERE b.id = c.id`)
	chatSQLitePath := fs.String("chat-sqlite-path", "", "explicit chat SQLite file path for --migrate-chat-account-only-binding (requires --driver sqlite)")
	postgresSchemaSnapshot := fs.Bool("postgres-schema-snapshot", false, "read-only PostgreSQL schema snapshot JSON to stdout (requires JUHE_AI_SCHEMA_SNAPSHOT_TARGET=production|test, JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL and JUHE_AI_SCHEMA_SNAPSHOT_READ_ONLY_CONFIRM=READ_ONLY; PostgreSQL only, SQLite is rejected)")
	businessDatasetExport := fs.Bool("export-business-dataset", false, "read-only export of the fixed 40-table business dataset from --business-dataset-url into --business-dataset-dir (requires --business-dataset-confirm-readonly=READ_ONLY; PostgreSQL only)")
	businessDatasetImport := fs.Bool("import-business-dataset", false, "all-or-nothing import of the business dataset in --business-dataset-dir into the authoritative --business-dataset-url database; commits only when every manifest assertion verifies")
	businessDatasetURL := fs.String("business-dataset-url", "", "explicit PostgreSQL URL for --export-business-dataset/--import-business-dataset")
	businessDatasetDir := fs.String("business-dataset-dir", "", "business dataset directory: output dir for --export-business-dataset, input dir for --import-business-dataset")
	businessDatasetConfirmReadOnly := fs.String("business-dataset-confirm-readonly", "", "must be READ_ONLY to enable --export-business-dataset")
	var businessDatasetAllowMissing []string
	fs.Var(&businessDatasetAllowMissingFlag{values: &businessDatasetAllowMissing}, "business-dataset-allow-missing-column", "table.column source column allowed to be missing in the target schema for --import-business-dataset (repeatable)")
	businessDatasetExpectedTargetDB := fs.String("business-dataset-expected-target-db", "", "expected target database name (current_database()) for --import-business-dataset; strict fail-closed comparison, mismatch or unverifiable identity aborts as a usage error; PRODUCTION CUTOVER REQUIRES this flag together with --business-dataset-expected-source-db")
	businessDatasetExpectedSourceDB := fs.String("business-dataset-expected-source-db", "", "expected manifest sourceIdentity (source current_database() recorded at export) for --import-business-dataset; placeholder or mismatch aborts as a usage error; PRODUCTION CUTOVER REQUIRES this flag together with --business-dataset-expected-target-db")
	mockdataRun := fs.Bool("mockdata", false, "idempotently rebuild the local development mockdata set in the SQLite data root (cleanup by the documented mockdata markers, then seed every domain, then autofill uncovered tables)")
	mockdataVerifyCoverage := fs.Bool("verify-mockdata-coverage", false, "read-only verify that every mockdata store table is non-empty (allowlisted runtime-state tables excepted) and that the key coverage assertions hold")
	mockdataDataDir := fs.String("mockdata-data-dir", "", "mockdata data root (or JUHE_AI_DATA_DIR); every store path derives from it unless the matching explicit path env is set")
	mockdataLogDir := fs.String("mockdata-log-dir", "", "mockdata log-file directory (or JUHE_AI_LOG_DIR; default <data root>/logs)")
	mockdataDays := fs.Int("mockdata-days", mockdata.DefaultDays, "mockdata history span in days for detail and monitoring samples (1..90)")
	mockdataDailyRequests := fs.Int("mockdata-daily-requests", mockdata.DefaultDailyRequests, "mockdata usage records generated per day (1..500)")
	businessDatasetReplaceExisting := fs.Bool("business-dataset-replace-existing", false, "with --import-business-dataset, empty every whitelist data table in reverse foreign-key order (children before parents) inside the import transaction before INSERT so authoritative production rows replace built-in seed rows; REQUIRED when the target database is already seeded (production cutover section 6 step 3 imports after --seed); the three structural-only tables are never deleted; default false keeps insert-only behavior where any pre-existing row fails the import")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *migrateChatBinding {
		if *postgresSchemaSnapshot || *ensureSchema || *seedDefaults || *version || *check || *goRuntimeMetricsCheck || *goRuntimeMetricsApply || *businessDatasetExport || *businessDatasetImport || *businessDatasetReplaceExisting || *j3Apply || *j3bApply || *j3bSQLiteApply || *mockdataRun || *mockdataVerifyCoverage {
			fmt.Fprintln(os.Stderr, "chat account-only binding migration flag is mutually exclusive with other maintenance commands")
			return 2
		}
		return chatAccountOnlyBindingMigrationResult(*bootstrapDriver, *chatSQLitePath, *bootstrapDSN)
	}
	if *postgresSchemaSnapshot {
		if *ensureSchema || *seedDefaults || *version || *check || *goRuntimeMetricsCheck || *goRuntimeMetricsApply || *businessDatasetExport || *businessDatasetImport || *businessDatasetReplaceExisting || *j3Apply || *j3bApply || *j3bSQLiteApply || *mockdataRun || *mockdataVerifyCoverage {
			fmt.Fprintln(os.Stderr, "PostgreSQL schema snapshot flag is mutually exclusive with other maintenance commands")
			return 2
		}
		return postgresSchemaSnapshotResult()
	}
	if *businessDatasetExport || *businessDatasetImport {
		if *businessDatasetExport && *businessDatasetImport || *ensureSchema || *seedDefaults || *version || *check || *goRuntimeMetricsCheck || *goRuntimeMetricsApply || *j3Apply || *j3bApply || *j3bSQLiteApply || *mockdataRun || *mockdataVerifyCoverage {
			fmt.Fprintln(os.Stderr, "business dataset export/import flags are mutually exclusive with other maintenance commands")
			return 2
		}
		if *businessDatasetExport {
			return businessDatasetExportResult(*businessDatasetURL, *businessDatasetDir, *businessDatasetConfirmReadOnly)
		}
		return businessDatasetImportResultWithReplace(*businessDatasetURL, *businessDatasetDir, businessDatasetAllowMissing, *businessDatasetExpectedTargetDB, *businessDatasetExpectedSourceDB, *businessDatasetReplaceExisting)
	}
	if *ensureSchema || *seedDefaults {
		if *postgresSchemaSnapshot || *version || *check || *goRuntimeMetricsCheck || *goRuntimeMetricsApply || *businessDatasetExport || *businessDatasetImport || *businessDatasetReplaceExisting || *j3Apply || *j3bApply || *j3bSQLiteApply || *mockdataRun || *mockdataVerifyCoverage {
			fmt.Fprintln(os.Stderr, "storage bootstrap flags are mutually exclusive with other maintenance commands")
			return 2
		}
		return runStorageBootstrap(*ensureSchema, *seedDefaults, *bootstrapDriver, *bootstrapPaths, *bootstrapDSN, *seedSecret)
	}
	if *goRuntimeMetricsCheck || *goRuntimeMetricsApply {
		if *goRuntimeMetricsCheck && *goRuntimeMetricsApply {
			fmt.Fprintln(os.Stderr, "Go runtime metrics check and apply flags are mutually exclusive")
			return 2
		}
		if *version || *check || *businessDatasetExport || *businessDatasetImport || *businessDatasetReplaceExisting || *j3Apply || *j3bApply || *j3bSQLiteApply || *mockdataRun || *mockdataVerifyCoverage {
			fmt.Fprintln(os.Stderr, "Go runtime metrics flags are mutually exclusive with other maintenance commands")
			return 2
		}
		return goRuntimeMetricsBootstrapResult(*goRuntimeMetricsApply, *goRuntimeMetricsURL, *nodeStopped, *goStopped, *backupConfirmed)
	}
	if *mockdataRun || *mockdataVerifyCoverage {
		if *mockdataRun && *mockdataVerifyCoverage {
			fmt.Fprintln(os.Stderr, "mockdata seeding and mockdata coverage verification flags are mutually exclusive")
			return 2
		}
		if *version || *check || *postgresSchemaSnapshot || *ensureSchema || *seedDefaults || *goRuntimeMetricsCheck || *goRuntimeMetricsApply || *businessDatasetExport || *businessDatasetImport || *businessDatasetReplaceExisting || *j3Apply || *j3bApply || *j3bSQLiteApply {
			fmt.Fprintln(os.Stderr, "mockdata flags are mutually exclusive with other maintenance commands")
			return 2
		}
		return runMockdataCommand(mockdataCommandFlags{
			VerifyCoverage: *mockdataVerifyCoverage,
			DataDir:        *mockdataDataDir,
			LogDir:         *mockdataLogDir,
			Days:           *mockdataDays,
			DailyRequests:  *mockdataDailyRequests,
			Driver:         *bootstrapDriver,
			DSN:            *bootstrapDSN,
			Secret:         *seedSecret,
		})
	}
	if *version {
		fmt.Printf("juhe-ai-maintenance project=%s contract=%s\n", contracts.ProjectMaintenance, contracts.ArchitectureVersion)
		return 0
	}
	if *check {
		fmt.Println("juhe-ai-maintenance boundary=ready runtime=one-shot-scaffold")
		return 0
	}
	// J3a/J3b 一次性 bootstrap：dispatch 顺序与基线一致（SQLite 优先于 PG，
	// PG 优先于 J3a），apply flag 互斥由上游各命令分支的互斥链承载。
	if *j3bSQLiteApply {
		return j3bModelCheckSQLiteBootstrapResult(*j3bSQLiteApply, *nodeStopped, *goStopped, *backupConfirmed)
	}
	if *j3bApply {
		return j3bModelCheckBootstrapResult(*j3bApply)
	}
	if *j3Apply {
		return j3aProxyLatencyBootstrapResult(*j3Apply)
	}
	fmt.Fprintln(os.Stderr, "maintenance project runtime is not switched yet; select an explicit one-shot command")
	return 2
}

// goRuntimeMetricsBootstrapResult returns the CLI exit code; runMaintenance dispatches it and tests call it in-process.
func goRuntimeMetricsBootstrapResult(apply bool, rawURL string, nodeStopped, goStopped, backupConfirmed bool) int {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		rawURL = strings.TrimSpace(os.Getenv(goruntimemetrics.BootstrapEnv))
	}
	if goRuntimeMetricsURLRequiredExitCode(rawURL) != 0 {
		fmt.Fprintf(os.Stderr, "Go runtime metrics bootstrap requires --go-runtime-metrics-postgres-url or %s\n", goruntimemetrics.BootstrapEnv)
		return 2
	}
	if apply && goRuntimeMetricsApplyPreflightExitCode(rawURL, nodeStopped, goStopped, backupConfirmed) != 0 {
		fmt.Fprintln(os.Stderr, "Go runtime metrics apply requires --node-stopped --go-stopped --backup-confirmed")
		return 2
	}
	db, err := goruntimemetrics.Open(rawURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open Go runtime metrics PostgreSQL connection: %v\n", err)
		return 2
	}
	defer db.Close()
	report, runErr := goruntimemetrics.Run(context.Background(), db, apply)
	return goRuntimeMetricsOutcomeExitCode(report, runErr)
}

// goRuntimeMetricsOutcomeExitCode renders the bootstrap report and maps the
// outcome to the maintenance exit-code contract.
func goRuntimeMetricsOutcomeExitCode(report goruntimemetrics.Report, runErr error) int {
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "Go runtime metrics bootstrap failed: %v\n", runErr)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode Go runtime metrics bootstrap report: %v\n", err)
		return 1
	}
	if !report.Ready() {
		return 3
	}
	return 0
}

func goRuntimeMetricsURLRequiredExitCode(rawURL string) int {
	if strings.TrimSpace(rawURL) == "" {
		return 2
	}
	return 0
}

func goRuntimeMetricsApplyPreflightExitCode(rawURL string, nodeStopped, goStopped, backupConfirmed bool) int {
	if goRuntimeMetricsURLRequiredExitCode(rawURL) != 0 || !nodeStopped || !goStopped || !backupConfirmed {
		return 2
	}
	return 0
}

// chatAccountOnlyBindingMigrationResult returns the CLI exit code;
// runMaintenance dispatches it and tests call it in-process. Usage errors
// (missing/contradictory driver flags) exit 2, runtime failures exit 1, a
// not-ready post-state (legacy columns still present) exits 3. The rollback
// SQL is printed next to the JSON report (design §10 回滚窗口).
func chatAccountOnlyBindingMigrationResult(driver, chatPath, dsn string) int {
	driver = strings.ToLower(strings.TrimSpace(driver))
	if driver != "sqlite" && driver != "postgres" {
		fmt.Fprintf(os.Stderr, "--migrate-chat-account-only-binding 需要 --driver sqlite（配 --chat-sqlite-path）或 --driver postgres（配 --dsn）\n")
		return 2
	}
	var db *sql.DB
	var err error
	var dialect chatbindingmigration.Dialect
	if driver == "sqlite" {
		if strings.TrimSpace(dsn) != "" {
			fmt.Fprintln(os.Stderr, "--dsn 只适用于 --driver postgres；sqlite 模式使用 --chat-sqlite-path")
			return 2
		}
		if strings.TrimSpace(chatPath) == "" {
			fmt.Fprintln(os.Stderr, "--driver sqlite 需要 --chat-sqlite-path 指向 chat SQLite 文件")
			return 2
		}
		dialect = chatbindingmigration.DialectSQLite
		db, err = chatbindingmigration.OpenSQLite(chatPath)
	} else {
		if strings.TrimSpace(chatPath) != "" {
			fmt.Fprintln(os.Stderr, "--chat-sqlite-path 只适用于 --driver sqlite；postgres 模式使用 --dsn")
			return 2
		}
		if strings.TrimSpace(dsn) == "" {
			fmt.Fprintln(os.Stderr, "--driver postgres 需要 --dsn 指向显式的 PostgreSQL URL")
			return 2
		}
		dialect = chatbindingmigration.DialectPostgres
		db, err = chatbindingmigration.OpenPostgres(dsn)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "open chat account-only binding migration connection: %v\n", err)
		return 2
	}
	defer db.Close()
	report, runErr := chatbindingmigration.Run(context.Background(), db, dialect, time.Now().UTC())
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

// j3bModelCheckSQLiteBootstrapResult returns the CLI exit code; runMaintenance dispatches it and tests call it in-process.
func j3bModelCheckSQLiteBootstrapResult(apply, nodeStopped, goStopped, backupConfirmed bool) int {
	path := strings.TrimSpace(os.Getenv(j3bmodelcheck.SQLiteBootstrapEnv))
	if path == "" {
		fmt.Fprintf(os.Stderr, "J3b SQLite bootstrap requires %s\n", j3bmodelcheck.SQLiteBootstrapEnv)
		return 2
	}
	if apply && (!nodeStopped || !goStopped || !backupConfirmed) {
		fmt.Fprintln(os.Stderr, "J3b SQLite apply requires --node-stopped --go-stopped --backup-confirmed")
		return 2
	}
	db, err := j3bmodelcheck.OpenSQLite(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open J3b SQLite bootstrap connection: %v\n", err)
		return 2
	}
	defer db.Close()
	report, runErr := j3bmodelcheck.RunSQLite(context.Background(), db, apply)
	return j3bSQLiteBootstrapOutcomeExitCode(report, runErr)
}

// j3bSQLiteBootstrapOutcomeExitCode renders the SQLite bootstrap report and
// maps the outcome to the maintenance exit-code contract.
func j3bSQLiteBootstrapOutcomeExitCode(report j3bmodelcheck.SQLiteReport, runErr error) int {
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "J3b SQLite bootstrap failed: %v\n", runErr)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3b SQLite bootstrap report: %v\n", err)
		return 1
	}
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
