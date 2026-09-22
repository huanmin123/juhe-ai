package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/businesshandoff"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/cutoverevidence"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/goruntimemetrics"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3aproxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3bmodelcheck"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/mockdata"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/ownermanifest"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/routestrategymigration"
)

// db-service 契约源在二次伪迁移第一轮（63793f367）移入 migration-backup-1
// 墓地；快照保留瘦身后的其余文件。
const archivedDBServiceSourceRoot = "migration-backup-1/node/final-archive/backend/src/modules/db-service"

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
	j3Check := fs.Bool("check-j3a-proxy-latency-postgres", false, "read-only verify pre-provisioned J3a PostgreSQL jobs schema")
	j3Apply := fs.Bool("apply-j3a-proxy-latency-postgres", false, "add missing J3a PostgreSQL jobs tables/indexes after explicit authorization")
	j3bCheck := fs.Bool("check-j3b-model-check-postgres", false, "read-only verify pre-provisioned J3b PostgreSQL juhe_j3b schema")
	j3bApply := fs.Bool("apply-j3b-model-check-postgres", false, "add missing J3b PostgreSQL juhe_j3b tables/indexes after explicit authorization")
	j3bPostgresReadback := fs.Bool("verify-j3b-model-check-postgres-backfill", false, "read-only compare legacy PostgreSQL J3b facts with juhe_j3b; never writes")
	j3bPostgresReadbackURL := fs.String("j3b-postgres-readback-url", "", "explicit maintenance-scoped PostgreSQL URL for --verify-j3b-model-check-postgres-backfill")
	j3bPostgresReadbackMaxRows := fs.Int64("j3b-postgres-readback-max-rows", j3bmodelcheck.DefaultPostgresReadbackMaxRows, "maximum rows per J3b fact table accepted as complete readback evidence")
	j3bReadbackManifestOut := fs.String("j3b-readback-manifest-out", "", "optional output path: when --verify-j3b-model-check-postgres-backfill succeeds, write the built v2 readback manifest JSON here")
	j3bReadbackSourceSnapshot := fs.String("j3b-readback-source-snapshot", "", "legacy source snapshot identity recorded in --j3b-readback-manifest-out (required with it)")
	j3bPostgresBackfill := fs.Bool("backfill-j3b-model-check-postgres", false, "copy whitelisted legacy PostgreSQL J3b facts into juhe_j3b after explicit stop and backup confirmations")
	j3bPostgresBackfillURL := fs.String("j3b-postgres-backfill-url", "", "explicit maintenance-scoped PostgreSQL URL for --backfill-j3b-model-check-postgres")
	j3bPostgresBackfillMaxRows := fs.Int64("j3b-postgres-backfill-max-rows", j3bmodelcheck.DefaultPostgresBackfillMaxRows, "maximum rows per J3b fact table accepted by PostgreSQL backfill")
	j3bPostgresBackfillMaxBytes := fs.Int64("j3b-postgres-backfill-max-bytes", j3bmodelcheck.DefaultPostgresBackfillMaxBytes, "maximum source bytes per J3b fact table accepted by PostgreSQL backfill")
	j3bBackfillEvidence := fs.String("j3b-backfill-evidence", "", "explicit JSON pre-backfill handoff evidence required before J3b PostgreSQL/SQLite backfill")
	j3bInventoryCheck := fs.Bool("verify-j3b-model-check-inventory", false, "read-only verify legacy J3b fact inventory against explicit evidence; never writes")
	j3bInventoryEvidence := fs.String("j3b-inventory-evidence", "", "explicit JSON evidence file for --verify-j3b-model-check-inventory")
	j3bCutoverEvidence := fs.String("verify-j3b-cutover-evidence", "", "read-only verify J3b cutover evidence JSON; never writes")
	assembleCutoverEvidence := fs.Bool("assemble-cutover-evidence", false, "assemble the post-cutover J3b evidence JSON for go-gateway from a real backup artifact and a v2 readback manifest, then self-verify it with the gateway contract chain before keeping the file")
	cutoverBackupArtifact := fs.String("cutover-backup-artifact", "", "real backup artifact file hashed into the assembled evidence (required with --assemble-cutover-evidence)")
	cutoverReadbackManifest := fs.String("cutover-readback-manifest", "", "v2 readback manifest file bound into the assembled evidence (required with --assemble-cutover-evidence)")
	cutoverOldOwner := fs.String("cutover-old-owner", cutoverevidence.DefaultOldOwner, "oldOwner recorded in the assembled evidence (default node-runtime; must differ from go-gateway)")
	cutoverOwnerEpoch := fs.String("cutover-owner-epoch", "", "ownerEpoch recorded in the assembled evidence; go-gateway compares it against its configured epoch (required with --assemble-cutover-evidence)")
	cutoverReplayCursor := fs.String("cutover-replay-cursor", "", "rollbackReplayCursor recorded in the assembled evidence, e.g. a timestamped batch id (required with --assemble-cutover-evidence)")
	cutoverEvidenceOut := fs.String("cutover-evidence-out", "", "output path of the assembled evidence JSON (required with --assemble-cutover-evidence)")
	cutoverMaxAgeSeconds := fs.Int64("cutover-max-age-seconds", cutoverevidence.DefaultMaxAgeSeconds, "freshness.maxAgeSeconds recorded in the assembled evidence (default 7776000 = 90 days)")
	j3bSQLiteCheck := fs.Bool("check-j3b-model-check-sqlite", false, "read-only verify dedicated J3b SQLite schema")
	j3bSQLiteApply := fs.Bool("apply-j3b-model-check-sqlite", false, "bootstrap dedicated J3b SQLite schema after stop and backup confirmations")
	goRuntimeMetricsCheck := fs.Bool("check-go-runtime-metrics", false, "read-only verify independent Go runtime metrics PostgreSQL schema")
	goRuntimeMetricsApply := fs.Bool("apply-go-runtime-metrics", false, "add missing Go runtime metrics PostgreSQL tables after explicit stop and backup confirmations")
	goRuntimeMetricsURL := fs.String("go-runtime-metrics-postgres-url", "", "explicit maintenance-scoped PostgreSQL URL for Go runtime metrics (or JUHE_AI_MAINTENANCE_GO_RUNTIME_METRICS_POSTGRES_URL)")
	nodeStopped := fs.Bool("node-stopped", false, "confirm Node writers are stopped for an offline migration")
	goStopped := fs.Bool("go-stopped", false, "confirm Go owners are stopped for an offline migration")
	backupConfirmed := fs.Bool("backup-confirmed", false, "confirm a recoverable backup was verified")
	j3bBackfill := fs.Bool("backfill-j3b-model-check-sqlite", false, "copy legacy J3b SQLite facts into the dedicated file")
	j3bReadback := fs.Bool("verify-j3b-model-check-sqlite-backfill", false, "read-only verify legacy-to-dedicated J3b SQLite row and digest parity")
	ownerManifestCheck := fs.Bool("verify-business-owner-manifest", false, "read-only verify the Business SQLite operation handoff manifest")
	capabilityManifestCheck := fs.Bool("verify-business-capability-manifest", false, "read-only verify the Go Business capability handoff manifest")
	routeOwnerManifestCheck := fs.Bool("verify-gateway-route-owner-manifest", false, "read-only verify Node system-api mutation routes and Gateway owner mapping")
	businessHandoffCheck := fs.Bool("verify-business-sqlite-handoff", false, "read-only verify Business/J3b SQLite path isolation and query_only write fencing")
	businessSchemaCheck := fs.Bool("verify-business-sqlite-schema", false, "read-only verify required Gateway Business SQLite tables, columns and indexes")
	businessSQLitePath := fs.String("business-sqlite-path", "", "Business SQLite path for handoff preflight (or JUHE_AI_MAINTENANCE_BUSINESS_SQLITE_PATH)")
	j3bSQLitePath := fs.String("j3b-sqlite-path", "", "dedicated J3b SQLite path for handoff preflight (or JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH)")
	nodeActivePathCheck := fs.Bool("scan-node-j3b-active-path", false, "read-only scan Node J3b routes, workers and writers")
	j3cReadOnlyCheck := fs.Bool("verify-j3c-readonly-boundary", false, "read-only audit the J3b-to-J3c health reader boundary")
	ensureSchema := fs.Bool("ensure-schema", false, "idempotently apply the six-database SQLite schema or the full PostgreSQL schema (with --driver plus --paths/--dsn)")
	seedDefaults := fs.Bool("seed", false, "idempotently run the default seed (business SQLite or PostgreSQL; runs after --ensure-schema when both are set)")
	bootstrapDriver := fs.String("driver", "", "storage driver for --ensure-schema/--seed: sqlite or postgres")
	bootstrapPaths := fs.String("paths", "", "sqlite storage paths: business=...,chat=...,dataset=...,usage-catalog=...,stats=...,codex-context-shard-root=...,codex-context-shard-count=...")
	bootstrapDSN := fs.String("dsn", "", "postgres URL for --ensure-schema/--seed/--migrate-hybrid-smart-strategies")
	seedSecret := fs.String("secret", "", "seed encryption secret (or JUHE_AI_SECRET); empty selects the Node dev default")
	migrateHybridSmart := fs.Bool("migrate-hybrid-smart-strategies", false, "one-shot migration of hybrid_smart route strategies to failover+disabled with config_json=NULL (PostgreSQL only via --dsn; default read-only dry-run listing the affected rows, add --confirm to write)")
	migrateConfirm := fs.Bool("confirm", false, "execute the --migrate-hybrid-smart-strategies migration; without it the migration stays a dry-run")
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
	if *migrateHybridSmart {
		if *postgresSchemaSnapshot || *ensureSchema || *seedDefaults || *version || *check || *goRuntimeMetricsCheck || *goRuntimeMetricsApply || strings.TrimSpace(*j3bCutoverEvidence) != "" || *ownerManifestCheck || *capabilityManifestCheck || *routeOwnerManifestCheck || *businessHandoffCheck || *businessSchemaCheck || *nodeActivePathCheck || *j3cReadOnlyCheck || *j3bInventoryCheck || strings.TrimSpace(*j3bInventoryEvidence) != "" || strings.TrimSpace(*j3bBackfillEvidence) != "" || *j3Check || *j3Apply || *j3bCheck || *j3bApply || *j3bPostgresReadback || *j3bPostgresBackfill || *j3bSQLiteCheck || *j3bSQLiteApply || *j3bBackfill || *j3bReadback || *businessDatasetExport || *businessDatasetImport || *businessDatasetReplaceExisting || *assembleCutoverEvidence || *mockdataRun || *mockdataVerifyCoverage {
			fmt.Fprintln(os.Stderr, "hybrid_smart strategy migration flag is mutually exclusive with other maintenance commands")
			return 2
		}
		return hybridSmartStrategyMigrationResult(*bootstrapDSN, *migrateConfirm)
	}
	if *postgresSchemaSnapshot {
		if *ensureSchema || *seedDefaults || *version || *check || *goRuntimeMetricsCheck || *goRuntimeMetricsApply || strings.TrimSpace(*j3bCutoverEvidence) != "" || *ownerManifestCheck || *capabilityManifestCheck || *routeOwnerManifestCheck || *businessHandoffCheck || *businessSchemaCheck || *nodeActivePathCheck || *j3cReadOnlyCheck || *j3bInventoryCheck || strings.TrimSpace(*j3bInventoryEvidence) != "" || strings.TrimSpace(*j3bBackfillEvidence) != "" || *j3Check || *j3Apply || *j3bCheck || *j3bApply || *j3bPostgresReadback || *j3bPostgresBackfill || *j3bSQLiteCheck || *j3bSQLiteApply || *j3bBackfill || *j3bReadback || *businessDatasetExport || *businessDatasetImport || *businessDatasetReplaceExisting || *assembleCutoverEvidence || *mockdataRun || *mockdataVerifyCoverage {
			fmt.Fprintln(os.Stderr, "PostgreSQL schema snapshot flag is mutually exclusive with other maintenance commands")
			return 2
		}
		return postgresSchemaSnapshotResult()
	}
	if *businessDatasetExport || *businessDatasetImport {
		if *businessDatasetExport && *businessDatasetImport || *ensureSchema || *seedDefaults || *version || *check || *goRuntimeMetricsCheck || *goRuntimeMetricsApply || strings.TrimSpace(*j3bCutoverEvidence) != "" || *ownerManifestCheck || *capabilityManifestCheck || *routeOwnerManifestCheck || *businessHandoffCheck || *businessSchemaCheck || *nodeActivePathCheck || *j3cReadOnlyCheck || *j3bInventoryCheck || strings.TrimSpace(*j3bInventoryEvidence) != "" || strings.TrimSpace(*j3bBackfillEvidence) != "" || *j3Check || *j3Apply || *j3bCheck || *j3bApply || *j3bPostgresReadback || *j3bPostgresBackfill || *j3bSQLiteCheck || *j3bSQLiteApply || *j3bBackfill || *j3bReadback || *migrateHybridSmart || *assembleCutoverEvidence || *mockdataRun || *mockdataVerifyCoverage {
			fmt.Fprintln(os.Stderr, "business dataset export/import flags are mutually exclusive with other maintenance commands")
			return 2
		}
		if *businessDatasetExport {
			return businessDatasetExportResult(*businessDatasetURL, *businessDatasetDir, *businessDatasetConfirmReadOnly)
		}
		return businessDatasetImportResultWithReplace(*businessDatasetURL, *businessDatasetDir, businessDatasetAllowMissing, *businessDatasetExpectedTargetDB, *businessDatasetExpectedSourceDB, *businessDatasetReplaceExisting)
	}
	if *ensureSchema || *seedDefaults {
		if *postgresSchemaSnapshot || *version || *check || *goRuntimeMetricsCheck || *goRuntimeMetricsApply || strings.TrimSpace(*j3bCutoverEvidence) != "" || *ownerManifestCheck || *capabilityManifestCheck || *routeOwnerManifestCheck || *businessHandoffCheck || *businessSchemaCheck || *nodeActivePathCheck || *j3cReadOnlyCheck || *j3bInventoryCheck || *j3Check || *j3Apply || *j3bCheck || *j3bApply || *j3bPostgresReadback || *j3bPostgresBackfill || *j3bSQLiteCheck || *j3bSQLiteApply || *j3bBackfill || *j3bReadback || *businessDatasetExport || *businessDatasetImport || *businessDatasetReplaceExisting || *assembleCutoverEvidence || *mockdataRun || *mockdataVerifyCoverage {
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
		if *version || *check || *ownerManifestCheck || *capabilityManifestCheck || *routeOwnerManifestCheck || *businessHandoffCheck || *businessSchemaCheck || *nodeActivePathCheck || *j3cReadOnlyCheck || *j3bInventoryCheck || strings.TrimSpace(*j3bCutoverEvidence) != "" || *j3Check || *j3Apply || *j3bCheck || *j3bApply || *j3bPostgresReadback || *j3bPostgresBackfill || *j3bSQLiteCheck || *j3bSQLiteApply || *j3bBackfill || *j3bReadback || *businessDatasetExport || *businessDatasetImport || *businessDatasetReplaceExisting || *assembleCutoverEvidence || *mockdataRun || *mockdataVerifyCoverage {
			fmt.Fprintln(os.Stderr, "Go runtime metrics flags are mutually exclusive with other maintenance commands")
			return 2
		}
		return goRuntimeMetricsBootstrapResult(*goRuntimeMetricsApply, *goRuntimeMetricsURL, *nodeStopped, *goStopped, *backupConfirmed)
	}
	if strings.TrimSpace(*j3bCutoverEvidence) != "" {
		if strings.TrimSpace(*j3bBackfillEvidence) != "" || *version || *check || *ownerManifestCheck || *capabilityManifestCheck || *routeOwnerManifestCheck || *businessHandoffCheck || *businessSchemaCheck || *nodeActivePathCheck || *j3cReadOnlyCheck || *j3bInventoryCheck || *j3Check || *j3Apply || *j3bCheck || *j3bApply || *j3bPostgresReadback || *j3bPostgresBackfill || *j3bSQLiteCheck || *j3bSQLiteApply || *j3bBackfill || *j3bReadback || *businessDatasetExport || *businessDatasetImport || *businessDatasetReplaceExisting || *assembleCutoverEvidence || *mockdataRun || *mockdataVerifyCoverage {
			fmt.Fprintln(os.Stderr, "J3b cutover evidence verification is mutually exclusive with other maintenance commands")
			return 2
		}
		return j3bCutoverEvidenceCheckResult(*j3bCutoverEvidence)
	}
	if *mockdataRun || *mockdataVerifyCoverage {
		if *mockdataRun && *mockdataVerifyCoverage {
			fmt.Fprintln(os.Stderr, "mockdata seeding and mockdata coverage verification flags are mutually exclusive")
			return 2
		}
		if *version || *check || *migrateHybridSmart || *postgresSchemaSnapshot || *ensureSchema || *seedDefaults || *goRuntimeMetricsCheck || *goRuntimeMetricsApply || strings.TrimSpace(*j3bCutoverEvidence) != "" || strings.TrimSpace(*j3bBackfillEvidence) != "" || strings.TrimSpace(*j3bInventoryEvidence) != "" || *ownerManifestCheck || *capabilityManifestCheck || *routeOwnerManifestCheck || *businessHandoffCheck || *businessSchemaCheck || *nodeActivePathCheck || *j3cReadOnlyCheck || *j3bInventoryCheck || *j3Check || *j3Apply || *j3bCheck || *j3bApply || *j3bPostgresReadback || *j3bPostgresBackfill || *j3bSQLiteCheck || *j3bSQLiteApply || *j3bBackfill || *j3bReadback || *businessDatasetExport || *businessDatasetImport || *businessDatasetReplaceExisting || *assembleCutoverEvidence {
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
	if *assembleCutoverEvidence {
		if *version || *check || *migrateHybridSmart || *postgresSchemaSnapshot || *ensureSchema || *seedDefaults || *goRuntimeMetricsCheck || *goRuntimeMetricsApply || strings.TrimSpace(*j3bCutoverEvidence) != "" || strings.TrimSpace(*j3bBackfillEvidence) != "" || strings.TrimSpace(*j3bInventoryEvidence) != "" || *ownerManifestCheck || *capabilityManifestCheck || *routeOwnerManifestCheck || *businessHandoffCheck || *businessSchemaCheck || *nodeActivePathCheck || *j3cReadOnlyCheck || *j3bInventoryCheck || *j3Check || *j3Apply || *j3bCheck || *j3bApply || *j3bPostgresReadback || *j3bPostgresBackfill || *j3bSQLiteCheck || *j3bSQLiteApply || *j3bBackfill || *j3bReadback || *businessDatasetExport || *businessDatasetImport || *businessDatasetReplaceExisting {
			fmt.Fprintln(os.Stderr, "cutover evidence assembly flag is mutually exclusive with other maintenance commands")
			return 2
		}
		return cutoverEvidenceAssembleResult(cutoverevidence.Options{
			OldOwner:             *cutoverOldOwner,
			OwnerEpoch:           *cutoverOwnerEpoch,
			RollbackReplayCursor: *cutoverReplayCursor,
			MaxAgeSeconds:        *cutoverMaxAgeSeconds,
			BackupArtifactPath:   *cutoverBackupArtifact,
			ReadbackManifestPath: *cutoverReadbackManifest,
		}, *cutoverEvidenceOut)
	}
	if *version {
		fmt.Printf("juhe-ai-maintenance project=%s contract=%s\n", contracts.ProjectMaintenance, contracts.ArchitectureVersion)
		return 0
	}
	if *check {
		fmt.Println("juhe-ai-maintenance boundary=ready runtime=one-shot-scaffold")
		return 0
	}
	if *ownerManifestCheck {
		return businessOwnerManifestResult(
			resolveRepoPath(envOrDefault("JUHE_AI_MAINTENANCE_OWNER_MANIFEST", "docs/migration/BusinessSQLite-owner-manifest.json")),
			resolveRepoPath(envOrDefault("JUHE_AI_MAINTENANCE_DB_SERVICE_TYPES", filepath.Join(archivedDBServiceSourceRoot, "db-service-types.ts"))),
			resolveRepoPath(envOrDefault("JUHE_AI_MAINTENANCE_DB_SERVICE_ACCESS", filepath.Join(archivedDBServiceSourceRoot, "db-service-operation-access-mode.ts"))),
			resolveRepoPath(envOrDefault("JUHE_AI_MAINTENANCE_DB_SERVICE_HANDLERS", filepath.Join(archivedDBServiceSourceRoot, "db-service-handlers.ts"))))
	}
	if *capabilityManifestCheck {
		return businessCapabilityManifestResult(
			resolveRepoPath(envOrDefault("JUHE_AI_MAINTENANCE_CAPABILITY_MANIFEST", "docs/migration/GoBusinessCapabilityManifest.json")),
			resolveRepoPath(envOrDefault("JUHE_AI_MAINTENANCE_OWNER_MANIFEST", "docs/migration/BusinessSQLite-owner-manifest.json")))
	}
	if *routeOwnerManifestCheck {
		return gatewayRouteOwnerManifestResult(
			resolveRepoPath(envOrDefault("JUHE_AI_MAINTENANCE_GATEWAY_ROUTE_MANIFEST", "docs/migration/GatewayManagementRouteOwnerManifest.json")),
			resolveRepositoryRoot())
	}
	if *businessHandoffCheck {
		return businessSQLiteHandoffCheckResult(*businessSQLitePath, *j3bSQLitePath)
	}
	if *businessSchemaCheck {
		return businessSQLiteSchemaCheckResult(*businessSQLitePath)
	}
	if *nodeActivePathCheck {
		return nodeJ3bActivePathResult(resolveRepositoryRoot())
	}
	if *j3cReadOnlyCheck {
		return j3cReadOnlyBoundaryResult(resolveRepositoryRoot())
	}
	if *j3bInventoryCheck {
		if *j3Check || *j3Apply || *j3bCheck || *j3bApply || *j3bPostgresReadback || *j3bPostgresBackfill || *j3bSQLiteCheck || *j3bSQLiteApply || *j3bBackfill || *j3bReadback || *businessDatasetExport || *businessDatasetImport || *businessDatasetReplaceExisting || *assembleCutoverEvidence || *mockdataRun || *mockdataVerifyCoverage {
			fmt.Fprintln(os.Stderr, "J3b inventory verification flag is mutually exclusive with bootstrap, backfill and readback flags")
			return 2
		}
		return j3bModelCheckInventoryResult(*j3bInventoryEvidence)
	}
	if *j3Check || *j3Apply || *j3bCheck || *j3bApply || *j3bPostgresReadback || *j3bPostgresBackfill || *j3bSQLiteCheck || *j3bSQLiteApply || *j3bBackfill || *j3bReadback {
		if (*j3bPostgresReadback || *j3bPostgresBackfill) && (*j3Check || *j3Apply || *j3bCheck || *j3bApply || (*j3bPostgresReadback && *j3bPostgresBackfill) || *j3bSQLiteCheck || *j3bSQLiteApply || *j3bBackfill || *j3bReadback || *businessDatasetExport || *businessDatasetImport || *businessDatasetReplaceExisting || *assembleCutoverEvidence || *mockdataRun || *mockdataVerifyCoverage) {
			fmt.Fprintln(os.Stderr, "J3b PostgreSQL backfill/readback flags are mutually exclusive with bootstrap, SQLite and other backfill flags")
			return 2
		}
		if *j3Check && *j3Apply {
			fmt.Fprintln(os.Stderr, "J3a PostgreSQL bootstrap flags are mutually exclusive")
			return 2
		}
		if *j3bCheck && *j3bApply {
			fmt.Fprintln(os.Stderr, "J3b PostgreSQL bootstrap flags are mutually exclusive")
			return 2
		}
		if *j3bSQLiteCheck && *j3bSQLiteApply {
			fmt.Fprintln(os.Stderr, "J3b SQLite bootstrap flags are mutually exclusive")
			return 2
		}
		if *j3bBackfill && *j3bReadback {
			fmt.Fprintln(os.Stderr, "J3b SQLite backfill and readback flags are mutually exclusive")
			return 2
		}
		if *j3bSQLiteCheck || *j3bSQLiteApply {
			return j3bModelCheckSQLiteBootstrapResult(*j3bSQLiteApply, *nodeStopped, *goStopped, *backupConfirmed)
		}
		if *j3bBackfill {
			return j3bModelCheckSQLiteBackfillResult(*nodeStopped, *goStopped, *backupConfirmed, *j3bBackfillEvidence)
		}
		if *j3bReadback {
			return j3bModelCheckSQLiteReadbackResult()
		}
		if *j3bPostgresReadback {
			return j3bModelCheckPostgresReadbackResultWithManifestOut(*j3bPostgresReadbackURL, *j3bPostgresReadbackMaxRows, *j3bReadbackManifestOut, *j3bReadbackSourceSnapshot)
		}
		if *j3bPostgresBackfill {
			return j3bModelCheckPostgresBackfillResult(*j3bPostgresBackfillURL, *j3bPostgresBackfillMaxRows, *j3bPostgresBackfillMaxBytes, *nodeStopped, *goStopped, *backupConfirmed, *j3bBackfillEvidence)
		}
		if *j3bCheck || *j3bApply {
			return j3bModelCheckBootstrapResult(*j3bApply)
		}
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

// hybridSmartStrategyMigrationResult returns the CLI exit code; runMaintenance dispatches it and tests call it in-process.
func hybridSmartStrategyMigrationResult(rawDSN string, confirm bool) int {
	rawDSN = strings.TrimSpace(rawDSN)
	if rawDSN == "" {
		fmt.Fprintln(os.Stderr, "hybrid_smart strategy migration requires --dsn with an explicit maintenance-scoped PostgreSQL URL (PostgreSQL only; SQLite is not supported by this command)")
		return 2
	}
	db, err := routestrategymigration.Open(rawDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open hybrid_smart strategy migration connection: %v\n", err)
		return 2
	}
	defer db.Close()
	report, runErr := routestrategymigration.Run(context.Background(), db, routestrategymigration.DialectPostgres, confirm, time.Now())
	return hybridSmartMigrationOutcomeExitCode(report, runErr)
}

// hybridSmartMigrationOutcomeExitCode renders the migration report and maps
// the outcome to the maintenance exit-code contract.
func hybridSmartMigrationOutcomeExitCode(report routestrategymigration.Report, runErr error) int {
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "hybrid_smart strategy migration failed: %v\n", runErr)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode hybrid_smart strategy migration report: %v\n", err)
		return 1
	}
	if !report.Ready() {
		return 3
	}
	return 0
}

// j3bModelCheckPostgresBackfillResult returns the CLI exit code; runMaintenance dispatches it and tests call it in-process.
func j3bModelCheckPostgresBackfillResult(rawURL string, maxRowsPerTable, maxBytesPerTable int64, nodeStopped, goStopped, backupConfirmed bool, evidencePath string) int {
	if strings.TrimSpace(rawURL) == "" {
		fmt.Fprintln(os.Stderr, "J3b PostgreSQL backfill requires --j3b-postgres-backfill-url with an explicit maintenance-scoped PostgreSQL URL")
		return 2
	}
	if j3bPostgresBackfillPreflightExitCode(rawURL, nodeStopped, goStopped, backupConfirmed) != 0 {
		fmt.Fprintln(os.Stderr, "J3b PostgreSQL backfill requires --node-stopped --go-stopped --backup-confirmed")
		return 2
	}
	if report, exitCode, err := j3bBackfillEvidencePreflight(evidencePath); err != nil {
		fmt.Fprintf(os.Stderr, "J3b backfill evidence input failed: %v\n", err)
		return exitCode
	} else if exitCode != 0 {
		fmt.Fprintf(os.Stderr, "J3b backfill evidence verification failed: %s\n", strings.Join(report.Errors, "; "))
		return exitCode
	}
	db, err := j3bmodelcheck.Open(rawURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open J3b PostgreSQL backfill connection: %v\n", err)
		return 2
	}
	defer db.Close()
	report, runErr := j3bmodelcheck.BackfillPostgres(context.Background(), db, j3bmodelcheck.PostgresBackfillOptions{MaxRowsPerTable: maxRowsPerTable, MaxBytesPerTable: maxBytesPerTable})
	return j3bPostgresBackfillOutcomeExitCode(report, runErr)
}

// j3bPostgresBackfillOutcomeExitCode renders the backfill report and maps the
// outcome to the maintenance exit-code contract.
func j3bPostgresBackfillOutcomeExitCode(report j3bmodelcheck.PostgresBackfillReport, runErr error) int {
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "J3b PostgreSQL backfill failed and was rolled back: %v\n", runErr)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3b PostgreSQL backfill report: %v\n", err)
		return 1
	}
	return 0
}

func j3bPostgresBackfillPreflightExitCode(rawURL string, nodeStopped, goStopped, backupConfirmed bool) int {
	if strings.TrimSpace(rawURL) == "" || !nodeStopped || !goStopped || !backupConfirmed {
		return 2
	}
	return 0
}

func j3bBackfillEvidencePreflight(path string) (businesshandoff.J3bCutoverEvidenceReport, int, error) {
	if strings.TrimSpace(path) == "" {
		return businesshandoff.J3bCutoverEvidenceReport{}, 2, fmt.Errorf("requires --j3b-backfill-evidence with an explicit JSON evidence file")
	}
	report, err := businesshandoff.VerifyJ3bBackfillEvidence(path, time.Now().UTC())
	if err != nil {
		return report, 2, err
	}
	if !report.Ready {
		return report, 3, nil
	}
	return report, 0, nil
}

// j3bModelCheckPostgresReadbackResult keeps the original two-argument runner
// signature for existing callers and tests; runMaintenance dispatches the
// manifest-out aware variant below. Behavior without --j3b-readback-manifest-out
// is unchanged.
func j3bModelCheckPostgresReadbackResult(rawURL string, maxRowsPerTable int64) int {
	return j3bModelCheckPostgresReadbackResultWithManifestOut(rawURL, maxRowsPerTable, "", "")
}

// j3bPostgresReadbackOutcomeExitCode renders the readback report and maps the
// outcome to the maintenance exit-code contract.
func j3bPostgresReadbackOutcomeExitCode(report j3bmodelcheck.PostgresBackfillVerificationReport, runErr error) int {
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "J3b PostgreSQL backfill readback failed: %v\n", runErr)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3b PostgreSQL backfill readback report: %v\n", err)
		return 1
	}
	if j3bPostgresReadbackExitCode(report) != 0 {
		return 3
	}
	return 0
}

func j3bPostgresReadbackURLRequiredExitCode(rawURL string) int {
	if strings.TrimSpace(rawURL) == "" {
		return 2
	}
	return 0
}

func j3bPostgresReadbackExitCode(report j3bmodelcheck.PostgresBackfillVerificationReport) int {
	if !report.Ready {
		return 3
	}
	return 0
}

// j3bModelCheckPostgresReadbackResultWithManifestOut returns the CLI exit code;
// runMaintenance dispatches it and tests call it in-process. When
// manifestOutPath is set and the verification is ready (exit would otherwise
// be 0), the built contracts.J3bReadbackManifest is written there in its JSON
// form after a contracts.ValidateJ3bReadbackManifest self-check; a write or
// self-check failure is a runtime error (exit 1). The verification semantics
// are unchanged.
func j3bModelCheckPostgresReadbackResultWithManifestOut(rawURL string, maxRowsPerTable int64, manifestOutPath, sourceSnapshot string) int {
	rawURL = strings.TrimSpace(rawURL)
	if j3bPostgresReadbackURLRequiredExitCode(rawURL) != 0 {
		fmt.Fprintln(os.Stderr, "J3b PostgreSQL backfill readback requires --j3b-postgres-readback-url with an explicit maintenance-scoped PostgreSQL URL")
		return 2
	}
	manifestOutPath = strings.TrimSpace(manifestOutPath)
	sourceSnapshot = strings.TrimSpace(sourceSnapshot)
	if manifestOutPath != "" && sourceSnapshot == "" {
		fmt.Fprintln(os.Stderr, "--j3b-readback-manifest-out requires --j3b-readback-source-snapshot with the legacy source snapshot identity recorded in the manifest")
		return 2
	}
	db, err := j3bmodelcheck.Open(rawURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open J3b PostgreSQL backfill readback connection: %v\n", err)
		return 2
	}
	defer db.Close()
	report, runErr := j3bmodelcheck.VerifyPostgresBackfill(context.Background(), db, j3bmodelcheck.PostgresReadbackOptions{MaxRowsPerTable: maxRowsPerTable})
	exitCode := j3bPostgresReadbackOutcomeExitCode(report, runErr)
	if exitCode != 0 || manifestOutPath == "" {
		return exitCode
	}
	if _, err := j3bmodelcheck.WriteJ3bReadbackManifestFile(manifestOutPath, report, j3bmodelcheck.J3bReadbackManifestOptions{SourceSnapshotIdentity: sourceSnapshot, VerifiedAt: time.Now().UTC()}); err != nil {
		fmt.Fprintf(os.Stderr, "write J3b readback manifest failed: %v\n", err)
		return 1
	}
	return 0
}

// cutoverEvidenceAssembleResult returns the CLI exit code; runMaintenance
// dispatches it and tests call it in-process. Input errors (missing flag
// values, unreadable or undecodable input files) exit 2, runtime failures
// exit 1, and a not-ready self-verification exits 3 with the written evidence
// already removed by the assembler.
func cutoverEvidenceAssembleResult(opts cutoverevidence.Options, evidenceOutPath string) int {
	report, err := cutoverevidence.AssembleCutoverEvidence(opts, evidenceOutPath, time.Now().UTC())
	var inputErr *cutoverevidence.InputError
	if err != nil {
		if errors.As(err, &inputErr) {
			fmt.Fprintf(os.Stderr, "cutover evidence assembly input failed: %v\n", err)
			return 2
		}
		fmt.Fprintf(os.Stderr, "cutover evidence assembly failed: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode cutover evidence assembly report: %v\n", err)
		return 1
	}
	if !report.Ready {
		fmt.Fprintf(os.Stderr, "assembled cutover evidence failed self-verification and was removed: %s\n", strings.Join(report.Errors, "; "))
		return 3
	}
	return 0
}

// j3bModelCheckInventoryResult returns the CLI exit code; runMaintenance dispatches it and tests call it in-process.
func j3bModelCheckInventoryResult(evidencePath string) int {
	if j3bInventoryEvidenceRequiredExitCode(evidencePath) != 0 {
		fmt.Fprintln(os.Stderr, "J3b inventory verification requires --j3b-inventory-evidence with an explicit JSON evidence file")
		return 2
	}
	evidence, err := j3bmodelcheck.LoadLegacyJ3bFactEvidence(evidencePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "J3b inventory evidence input failed: %v\n", err)
		return 2
	}
	report := j3bmodelcheck.ValidateLegacyJ3bFactCoverage(j3bmodelcheck.LegacyJ3bFactInventory, evidence)
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3b inventory coverage report: %v\n", err)
		return 1
	}
	if j3bInventoryExitCode(report) != 0 {
		return 3
	}
	return 0
}

func j3bInventoryEvidenceRequiredExitCode(path string) int {
	if strings.TrimSpace(path) == "" {
		return 2
	}
	return 0
}

func j3bInventoryExitCode(report j3bmodelcheck.LegacyJ3bFactCoverageReport) int {
	if !report.Ready {
		return 3
	}
	return 0
}

// j3cReadOnlyBoundaryResult returns the CLI exit code; runMaintenance dispatches it and tests call it in-process.
func j3cReadOnlyBoundaryResult(root string) int {
	report, err := ownermanifest.VerifyJ3cReadOnlyBoundary(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "J3c read-only boundary verification failed: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3c read-only boundary report: %v\n", err)
		return 1
	}
	if !report.ReadOnlyAuditReady || !report.J3cOwnerReady {
		return 3
	}
	return 0
}

// j3bCutoverEvidenceCheckResult returns the CLI exit code; runMaintenance dispatches it and tests call it in-process.
func j3bCutoverEvidenceCheckResult(path string) int {
	report, err := businesshandoff.VerifyJ3bCutoverEvidence(path, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "J3b cutover evidence input failed: %v\n", err)
		return 2
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3b cutover evidence report: %v\n", err)
		return 1
	}
	if exitCode := j3bCutoverEvidenceExitCode(report); exitCode != 0 {
		return exitCode
	}
	return 0
}

func j3bCutoverEvidenceExitCode(report businesshandoff.J3bCutoverEvidenceReport) int {
	if !report.Ready {
		return 3
	}
	return 0
}

// gatewayRouteOwnerManifestResult returns the CLI exit code; runMaintenance dispatches it and tests call it in-process.
func gatewayRouteOwnerManifestResult(manifestPath, root string) int {
	report, err := ownermanifest.VerifyGatewayRouteOwnerManifest(manifestPath, root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Gateway route owner manifest verification failed: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode Gateway route owner manifest report: %v\n", err)
		return 1
	}
	if len(report.PendingFamilies) > 0 {
		return 3
	}
	return 0
}

// businessSQLiteSchemaCheckResult returns the CLI exit code; runMaintenance dispatches it and tests call it in-process.
func businessSQLiteSchemaCheckResult(path string) int {
	if strings.TrimSpace(path) == "" {
		path = strings.TrimSpace(os.Getenv("JUHE_AI_MAINTENANCE_BUSINESS_SQLITE_PATH"))
	}
	if strings.TrimSpace(path) == "" {
		fmt.Fprintln(os.Stderr, "Business SQLite schema preflight requires --business-sqlite-path or JUHE_AI_MAINTENANCE_BUSINESS_SQLITE_PATH")
		return 2
	}
	report, err := businesshandoff.VerifySQLiteSchema(context.Background(), path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Business SQLite schema preflight failed: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode Business SQLite schema preflight report: %v\n", err)
		return 1
	}
	if !report.Ready {
		return 3
	}
	return 0
}

// businessCapabilityManifestResult returns the CLI exit code; runMaintenance dispatches it and tests call it in-process.
func businessCapabilityManifestResult(capabilityPath, operationPath string) int {
	report, err := ownermanifest.VerifyCapabilityManifest(capabilityPath, operationPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Business capability manifest verification failed: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode Business capability manifest report: %v\n", err)
		return 1
	}
	// A valid manifest is still only a completeness proof. Any capability
	// marked missing/partial keeps the handoff gate closed.
	if report.StatusCoverage["missing"] > 0 || report.StatusCoverage["partial"] > 0 {
		return 3
	}
	return 0
}

// j3bModelCheckSQLiteReadbackResult returns the CLI exit code; runMaintenance dispatches it and tests call it in-process.
func j3bModelCheckSQLiteReadbackResult() int {
	targetPath := strings.TrimSpace(os.Getenv(j3bmodelcheck.SQLiteBootstrapEnv))
	datasetPath := strings.TrimSpace(os.Getenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH"))
	statsPath := strings.TrimSpace(os.Getenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH"))
	if targetPath == "" || datasetPath == "" || statsPath == "" {
		fmt.Fprintln(os.Stderr, "J3b SQLite readback requires JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH, JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH and JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH")
		return 2
	}
	report, err := j3bmodelcheck.VerifySQLiteBackfill(context.Background(), targetPath, datasetPath, statsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "J3b SQLite readback failed: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3b SQLite readback report: %v\n", err)
		return 1
	}
	if j3bSQLiteReadbackExitCode(report) != 0 {
		return 3
	}
	return 0
}

// j3bSQLiteReadbackExitCode gates cutover evidence on the lossless projection
// result. Ready intentionally retains the legacy common-column compatibility
// signal and is therefore insufficient for this command's success status.
func j3bSQLiteReadbackExitCode(report j3bmodelcheck.BackfillVerificationReport) int {
	if !report.Complete {
		return 3
	}
	return 0
}

// businessSQLiteHandoffCheckResult returns the CLI exit code; runMaintenance dispatches it and tests call it in-process.
func businessSQLiteHandoffCheckResult(businessPath, j3bPath string) int {
	if strings.TrimSpace(businessPath) == "" {
		businessPath = strings.TrimSpace(os.Getenv("JUHE_AI_MAINTENANCE_BUSINESS_SQLITE_PATH"))
	}
	if strings.TrimSpace(j3bPath) == "" {
		j3bPath = strings.TrimSpace(os.Getenv(j3bmodelcheck.SQLiteBootstrapEnv))
	}
	if strings.TrimSpace(businessPath) == "" || strings.TrimSpace(j3bPath) == "" {
		fmt.Fprintln(os.Stderr, "Business SQLite handoff preflight requires --business-sqlite-path/--j3b-sqlite-path or JUHE_AI_MAINTENANCE_BUSINESS_SQLITE_PATH/JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH")
		return 2
	}
	report, err := businesshandoff.Verify(context.Background(), businessPath, j3bPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Business SQLite handoff preflight failed: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode Business SQLite handoff preflight report: %v\n", err)
		return 1
	}
	if !report.Ready {
		return 3
	}
	return 0
}

// nodeJ3bActivePathResult returns the CLI exit code; runMaintenance dispatches it and tests call it in-process.
func nodeJ3bActivePathResult(root string) int {
	report, err := ownermanifest.ScanNodeJ3bActivePaths(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Node J3b active-path scan failed: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode Node J3b active-path report: %v\n", err)
		return 1
	}
	if len(report.Findings) > 0 {
		return 3
	}
	return 0
}

func resolveRepositoryRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for depth := 0; depth <= 8; depth++ {
		if isRepositoryRoot(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "."
}

// isRepositoryRoot uses the Go project, migration documents and the archived
// Node source tree as stable repository markers. The live backend/src tree is
// intentionally absent after the final Node archive, so it cannot be used as
// a repository-root marker any longer.
func isRepositoryRoot(dir string) bool {
	for _, marker := range []string{
		"backend-go",
		"docs/migration",
		"migration-backup/node/final-archive/backend/src",
	} {
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(marker)))
		if err != nil || !info.IsDir() {
			return false
		}
	}
	return true
}

// businessOwnerManifestResult returns the CLI exit code; runMaintenance dispatches it and tests call it in-process.
func businessOwnerManifestResult(manifestPath, typesPath, accessPath, handlerPath string) int {
	report, err := ownermanifest.Verify(manifestPath, typesPath, accessPath, handlerPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Business SQLite owner manifest verification failed: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode Business SQLite owner manifest report: %v\n", err)
		return 1
	}
	return 0
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func resolveRepoPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	dir, err := os.Getwd()
	if err != nil {
		return path
	}
	for depth := 0; depth <= 8; depth++ {
		candidate := filepath.Join(dir, path)
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return path
}

// j3bModelCheckSQLiteBackfillResult returns the CLI exit code; runMaintenance dispatches it and tests call it in-process.
func j3bModelCheckSQLiteBackfillResult(nodeStopped, goStopped, backupConfirmed bool, evidencePath string) int {
	if !nodeStopped || !goStopped || !backupConfirmed {
		fmt.Fprintln(os.Stderr, "J3b SQLite backfill requires --node-stopped --go-stopped --backup-confirmed")
		return 2
	}
	if report, exitCode, err := j3bBackfillEvidencePreflight(evidencePath); err != nil {
		fmt.Fprintf(os.Stderr, "J3b backfill evidence input failed: %v\n", err)
		return exitCode
	} else if exitCode != 0 {
		fmt.Fprintf(os.Stderr, "J3b backfill evidence verification failed: %s\n", strings.Join(report.Errors, "; "))
		return exitCode
	}
	targetPath := strings.TrimSpace(os.Getenv(j3bmodelcheck.SQLiteBootstrapEnv))
	datasetPath := strings.TrimSpace(os.Getenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH"))
	statsPath := strings.TrimSpace(os.Getenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH"))
	if targetPath == "" || datasetPath == "" || statsPath == "" {
		fmt.Fprintln(os.Stderr, "J3b SQLite backfill requires JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH, JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH and JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH")
		return 2
	}
	if err := j3bmodelcheck.ValidateSQLiteBackfillPaths(targetPath, datasetPath, statsPath); err != nil {
		fmt.Fprintf(os.Stderr, "J3b SQLite backfill path isolation failed: %v\n", err)
		return 2
	}
	target, err := j3bmodelcheck.OpenSQLite(targetPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open J3b SQLite backfill target: %v\n", err)
		return 2
	}
	defer target.Close()
	report, runErr := j3bmodelcheck.BackfillSQLite(context.Background(), target, datasetPath, statsPath)
	return j3bSQLiteBackfillOutcomeExitCode(report, runErr)
}

// j3bSQLiteBackfillOutcomeExitCode renders the SQLite backfill report and maps
// the outcome to the maintenance exit-code contract.
func j3bSQLiteBackfillOutcomeExitCode(report j3bmodelcheck.BackfillReport, runErr error) int {
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "J3b SQLite backfill failed: %v\n", runErr)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3b SQLite backfill report: %v\n", err)
		return 1
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
