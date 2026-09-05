package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/businesshandoff"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/goruntimemetrics"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3aproxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3bmodelcheck"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/ownermanifest"
)

func main() {
	version := flag.Bool("version", false, "print the maintenance project contract version")
	check := flag.Bool("check-boundary", false, "verify the scaffold boundary")
	j3Check := flag.Bool("check-j3a-proxy-latency-postgres", false, "read-only verify pre-provisioned J3a PostgreSQL jobs schema")
	j3Apply := flag.Bool("apply-j3a-proxy-latency-postgres", false, "add missing J3a PostgreSQL jobs tables/indexes after explicit authorization")
	j3bCheck := flag.Bool("check-j3b-model-check-postgres", false, "read-only verify pre-provisioned J3b PostgreSQL juhe_j3b schema")
	j3bApply := flag.Bool("apply-j3b-model-check-postgres", false, "add missing J3b PostgreSQL juhe_j3b tables/indexes after explicit authorization")
	j3bPostgresReadback := flag.Bool("verify-j3b-model-check-postgres-backfill", false, "read-only compare legacy PostgreSQL J3b facts with juhe_j3b; never writes")
	j3bPostgresReadbackURL := flag.String("j3b-postgres-readback-url", "", "explicit maintenance-scoped PostgreSQL URL for --verify-j3b-model-check-postgres-backfill")
	j3bPostgresReadbackMaxRows := flag.Int64("j3b-postgres-readback-max-rows", j3bmodelcheck.DefaultPostgresReadbackMaxRows, "maximum rows per J3b fact table accepted as complete readback evidence")
	j3bPostgresBackfill := flag.Bool("backfill-j3b-model-check-postgres", false, "copy whitelisted legacy PostgreSQL J3b facts into juhe_j3b after explicit stop and backup confirmations")
	j3bPostgresBackfillURL := flag.String("j3b-postgres-backfill-url", "", "explicit maintenance-scoped PostgreSQL URL for --backfill-j3b-model-check-postgres")
	j3bPostgresBackfillMaxRows := flag.Int64("j3b-postgres-backfill-max-rows", j3bmodelcheck.DefaultPostgresBackfillMaxRows, "maximum rows per J3b fact table accepted by PostgreSQL backfill")
	j3bPostgresBackfillMaxBytes := flag.Int64("j3b-postgres-backfill-max-bytes", j3bmodelcheck.DefaultPostgresBackfillMaxBytes, "maximum source bytes per J3b fact table accepted by PostgreSQL backfill")
	j3bBackfillEvidence := flag.String("j3b-backfill-evidence", "", "explicit JSON pre-backfill handoff evidence required before J3b PostgreSQL/SQLite backfill")
	j3bInventoryCheck := flag.Bool("verify-j3b-model-check-inventory", false, "read-only verify legacy J3b fact inventory against explicit evidence; never writes")
	j3bInventoryEvidence := flag.String("j3b-inventory-evidence", "", "explicit JSON evidence file for --verify-j3b-model-check-inventory")
	j3bCutoverEvidence := flag.String("verify-j3b-cutover-evidence", "", "read-only verify J3b cutover evidence JSON; never writes")
	j3bSQLiteCheck := flag.Bool("check-j3b-model-check-sqlite", false, "read-only verify dedicated J3b SQLite schema")
	j3bSQLiteApply := flag.Bool("apply-j3b-model-check-sqlite", false, "bootstrap dedicated J3b SQLite schema after stop and backup confirmations")
	goRuntimeMetricsCheck := flag.Bool("check-go-runtime-metrics", false, "read-only verify independent Go runtime metrics PostgreSQL schema")
	goRuntimeMetricsApply := flag.Bool("apply-go-runtime-metrics", false, "add missing Go runtime metrics PostgreSQL tables after explicit stop and backup confirmations")
	goRuntimeMetricsURL := flag.String("go-runtime-metrics-postgres-url", "", "explicit maintenance-scoped PostgreSQL URL for Go runtime metrics (or JUHE_AI_MAINTENANCE_GO_RUNTIME_METRICS_POSTGRES_URL)")
	nodeStopped := flag.Bool("node-stopped", false, "confirm Node writers are stopped for an offline migration")
	goStopped := flag.Bool("go-stopped", false, "confirm Go owners are stopped for an offline migration")
	backupConfirmed := flag.Bool("backup-confirmed", false, "confirm a recoverable backup was verified")
	j3bBackfill := flag.Bool("backfill-j3b-model-check-sqlite", false, "copy legacy J3b SQLite facts into the dedicated file")
	j3bReadback := flag.Bool("verify-j3b-model-check-sqlite-backfill", false, "read-only verify legacy-to-dedicated J3b SQLite row and digest parity")
	ownerManifestCheck := flag.Bool("verify-business-owner-manifest", false, "read-only verify the Business SQLite operation handoff manifest")
	capabilityManifestCheck := flag.Bool("verify-business-capability-manifest", false, "read-only verify the Go Business capability handoff manifest")
	routeOwnerManifestCheck := flag.Bool("verify-gateway-route-owner-manifest", false, "read-only verify Node system-api mutation routes and Gateway owner mapping")
	businessHandoffCheck := flag.Bool("verify-business-sqlite-handoff", false, "read-only verify Business/J3b SQLite path isolation and query_only write fencing")
	businessSchemaCheck := flag.Bool("verify-business-sqlite-schema", false, "read-only verify required Gateway Business SQLite tables, columns and indexes")
	businessSQLitePath := flag.String("business-sqlite-path", "", "Business SQLite path for handoff preflight (or JUHE_AI_MAINTENANCE_BUSINESS_SQLITE_PATH)")
	j3bSQLitePath := flag.String("j3b-sqlite-path", "", "dedicated J3b SQLite path for handoff preflight (or JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH)")
	nodeActivePathCheck := flag.Bool("scan-node-j3b-active-path", false, "read-only scan Node J3b routes, workers and writers")
	j3cReadOnlyCheck := flag.Bool("verify-j3c-readonly-boundary", false, "read-only audit the J3b-to-J3c health reader boundary")
	ensureSchema := flag.Bool("ensure-schema", false, "idempotently apply the six-database SQLite schema or the full PostgreSQL schema (with --driver plus --paths/--dsn)")
	seedDefaults := flag.Bool("seed", false, "idempotently run the default seed (business SQLite or PostgreSQL; runs after --ensure-schema when both are set)")
	bootstrapDriver := flag.String("driver", "", "storage driver for --ensure-schema/--seed: sqlite or postgres")
	bootstrapPaths := flag.String("paths", "", "sqlite storage paths: business=...,chat=...,dataset=...,usage-catalog=...,stats=...,codex-context-shard-root=...,codex-context-shard-count=...")
	bootstrapDSN := flag.String("dsn", "", "postgres URL for --ensure-schema/--seed")
	seedSecret := flag.String("secret", "", "seed encryption secret (or JUHE_AI_SECRET); empty selects the Node dev default")
	flag.Parse()
	if *ensureSchema || *seedDefaults {
		if *version || *check || *goRuntimeMetricsCheck || *goRuntimeMetricsApply || strings.TrimSpace(*j3bCutoverEvidence) != "" || *ownerManifestCheck || *capabilityManifestCheck || *routeOwnerManifestCheck || *businessHandoffCheck || *businessSchemaCheck || *nodeActivePathCheck || *j3cReadOnlyCheck || *j3bInventoryCheck || *j3Check || *j3Apply || *j3bCheck || *j3bApply || *j3bPostgresReadback || *j3bPostgresBackfill || *j3bSQLiteCheck || *j3bSQLiteApply || *j3bBackfill || *j3bReadback {
			fmt.Fprintln(os.Stderr, "storage bootstrap flags are mutually exclusive with other maintenance commands")
			os.Exit(2)
		}
		os.Exit(runStorageBootstrap(*ensureSchema, *seedDefaults, *bootstrapDriver, *bootstrapPaths, *bootstrapDSN, *seedSecret))
	}
	if *goRuntimeMetricsCheck || *goRuntimeMetricsApply {
		if *goRuntimeMetricsCheck && *goRuntimeMetricsApply {
			fmt.Fprintln(os.Stderr, "Go runtime metrics check and apply flags are mutually exclusive")
			os.Exit(2)
		}
		if *version || *check || *ownerManifestCheck || *capabilityManifestCheck || *routeOwnerManifestCheck || *businessHandoffCheck || *businessSchemaCheck || *nodeActivePathCheck || *j3cReadOnlyCheck || *j3bInventoryCheck || strings.TrimSpace(*j3bCutoverEvidence) != "" || *j3Check || *j3Apply || *j3bCheck || *j3bApply || *j3bPostgresReadback || *j3bPostgresBackfill || *j3bSQLiteCheck || *j3bSQLiteApply || *j3bBackfill || *j3bReadback {
			fmt.Fprintln(os.Stderr, "Go runtime metrics flags are mutually exclusive with other maintenance commands")
			os.Exit(2)
		}
		runGoRuntimeMetricsBootstrap(*goRuntimeMetricsApply, *goRuntimeMetricsURL, *nodeStopped, *goStopped, *backupConfirmed)
		return
	}
	if strings.TrimSpace(*j3bCutoverEvidence) != "" {
		if strings.TrimSpace(*j3bBackfillEvidence) != "" || *version || *check || *ownerManifestCheck || *capabilityManifestCheck || *routeOwnerManifestCheck || *businessHandoffCheck || *businessSchemaCheck || *nodeActivePathCheck || *j3cReadOnlyCheck || *j3bInventoryCheck || *j3Check || *j3Apply || *j3bCheck || *j3bApply || *j3bPostgresReadback || *j3bPostgresBackfill || *j3bSQLiteCheck || *j3bSQLiteApply || *j3bBackfill || *j3bReadback {
			fmt.Fprintln(os.Stderr, "J3b cutover evidence verification is mutually exclusive with other maintenance commands")
			os.Exit(2)
		}
		runJ3bCutoverEvidenceCheck(*j3bCutoverEvidence)
		return
	}
	if *version {
		fmt.Printf("juhe-ai-maintenance project=%s contract=%s\n", contracts.ProjectMaintenance, contracts.ArchitectureVersion)
		return
	}
	if *check {
		fmt.Println("juhe-ai-maintenance boundary=ready runtime=one-shot-scaffold")
		return
	}
	if *ownerManifestCheck {
		runBusinessOwnerManifestCheck()
		return
	}
	if *capabilityManifestCheck {
		runBusinessCapabilityManifestCheck()
		return
	}
	if *routeOwnerManifestCheck {
		runGatewayRouteOwnerManifestCheck()
		return
	}
	if *businessHandoffCheck {
		runBusinessSQLiteHandoffCheck(*businessSQLitePath, *j3bSQLitePath)
		return
	}
	if *businessSchemaCheck {
		runBusinessSQLiteSchemaCheck(*businessSQLitePath)
		return
	}
	if *nodeActivePathCheck {
		runNodeJ3bActivePathCheck()
		return
	}
	if *j3cReadOnlyCheck {
		runJ3cReadOnlyBoundaryCheck()
		return
	}
	if *j3bInventoryCheck {
		if *j3Check || *j3Apply || *j3bCheck || *j3bApply || *j3bPostgresReadback || *j3bPostgresBackfill || *j3bSQLiteCheck || *j3bSQLiteApply || *j3bBackfill || *j3bReadback {
			fmt.Fprintln(os.Stderr, "J3b inventory verification flag is mutually exclusive with bootstrap, backfill and readback flags")
			os.Exit(2)
		}
		runJ3bModelCheckInventory(*j3bInventoryEvidence)
		return
	}
	if *j3Check || *j3Apply || *j3bCheck || *j3bApply || *j3bPostgresReadback || *j3bPostgresBackfill || *j3bSQLiteCheck || *j3bSQLiteApply || *j3bBackfill || *j3bReadback {
		if (*j3bPostgresReadback || *j3bPostgresBackfill) && (*j3Check || *j3Apply || *j3bCheck || *j3bApply || (*j3bPostgresReadback && *j3bPostgresBackfill) || *j3bSQLiteCheck || *j3bSQLiteApply || *j3bBackfill || *j3bReadback) {
			fmt.Fprintln(os.Stderr, "J3b PostgreSQL backfill/readback flags are mutually exclusive with bootstrap, SQLite and other backfill flags")
			os.Exit(2)
		}
		if *j3Check && *j3Apply {
			fmt.Fprintln(os.Stderr, "J3a PostgreSQL bootstrap flags are mutually exclusive")
			os.Exit(2)
		}
		if *j3bCheck && *j3bApply {
			fmt.Fprintln(os.Stderr, "J3b PostgreSQL bootstrap flags are mutually exclusive")
			os.Exit(2)
		}
		if *j3bSQLiteCheck && *j3bSQLiteApply {
			fmt.Fprintln(os.Stderr, "J3b SQLite bootstrap flags are mutually exclusive")
			os.Exit(2)
		}
		if *j3bBackfill && *j3bReadback {
			fmt.Fprintln(os.Stderr, "J3b SQLite backfill and readback flags are mutually exclusive")
			os.Exit(2)
		}
		if *j3bSQLiteCheck || *j3bSQLiteApply {
			runJ3bModelCheckSQLiteBootstrap(*j3bSQLiteApply, *nodeStopped, *goStopped, *backupConfirmed)
			return
		}
		if *j3bBackfill {
			runJ3bModelCheckSQLiteBackfill(*nodeStopped, *goStopped, *backupConfirmed, *j3bBackfillEvidence)
			return
		}
		if *j3bReadback {
			runJ3bModelCheckSQLiteReadback()
			return
		}
		if *j3bPostgresReadback {
			runJ3bModelCheckPostgresBackfillReadback(*j3bPostgresReadbackURL, *j3bPostgresReadbackMaxRows)
			return
		}
		if *j3bPostgresBackfill {
			runJ3bModelCheckPostgresBackfill(*j3bPostgresBackfillURL, *j3bPostgresBackfillMaxRows, *j3bPostgresBackfillMaxBytes, *nodeStopped, *goStopped, *backupConfirmed, *j3bBackfillEvidence)
			return
		}
		if *j3bCheck || *j3bApply {
			runJ3bModelCheckBootstrap(*j3bApply)
			return
		}
		runJ3aProxyLatencyBootstrap(*j3Apply)
		return
	}
	fmt.Fprintln(os.Stderr, "maintenance project runtime is not switched yet; select an explicit one-shot command")
	os.Exit(2)
}

func runGoRuntimeMetricsBootstrap(apply bool, rawURL string, nodeStopped, goStopped, backupConfirmed bool) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		rawURL = strings.TrimSpace(os.Getenv(goruntimemetrics.BootstrapEnv))
	}
	if goRuntimeMetricsURLRequiredExitCode(rawURL) != 0 {
		fmt.Fprintf(os.Stderr, "Go runtime metrics bootstrap requires --go-runtime-metrics-postgres-url or %s\n", goruntimemetrics.BootstrapEnv)
		os.Exit(2)
	}
	if apply && goRuntimeMetricsApplyPreflightExitCode(rawURL, nodeStopped, goStopped, backupConfirmed) != 0 {
		fmt.Fprintln(os.Stderr, "Go runtime metrics apply requires --node-stopped --go-stopped --backup-confirmed")
		os.Exit(2)
	}
	db, err := goruntimemetrics.Open(rawURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open Go runtime metrics PostgreSQL connection: %v\n", err)
		os.Exit(2)
	}
	defer db.Close()
	report, err := goruntimemetrics.Run(context.Background(), db, apply)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Go runtime metrics bootstrap failed: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode Go runtime metrics bootstrap report: %v\n", err)
		os.Exit(1)
	}
	if !report.Ready() {
		os.Exit(3)
	}
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

func runJ3bModelCheckPostgresBackfill(rawURL string, maxRowsPerTable, maxBytesPerTable int64, nodeStopped, goStopped, backupConfirmed bool, evidencePath string) {
	if strings.TrimSpace(rawURL) == "" {
		fmt.Fprintln(os.Stderr, "J3b PostgreSQL backfill requires --j3b-postgres-backfill-url with an explicit maintenance-scoped PostgreSQL URL")
		os.Exit(2)
	}
	if j3bPostgresBackfillPreflightExitCode(rawURL, nodeStopped, goStopped, backupConfirmed) != 0 {
		fmt.Fprintln(os.Stderr, "J3b PostgreSQL backfill requires --node-stopped --go-stopped --backup-confirmed")
		os.Exit(2)
	}
	if report, exitCode, err := j3bBackfillEvidencePreflight(evidencePath); err != nil {
		fmt.Fprintf(os.Stderr, "J3b backfill evidence input failed: %v\n", err)
		os.Exit(exitCode)
	} else if exitCode != 0 {
		fmt.Fprintf(os.Stderr, "J3b backfill evidence verification failed: %s\n", strings.Join(report.Errors, "; "))
		os.Exit(exitCode)
	}
	db, err := j3bmodelcheck.Open(rawURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open J3b PostgreSQL backfill connection: %v\n", err)
		os.Exit(2)
	}
	defer db.Close()
	report, err := j3bmodelcheck.BackfillPostgres(context.Background(), db, j3bmodelcheck.PostgresBackfillOptions{MaxRowsPerTable: maxRowsPerTable, MaxBytesPerTable: maxBytesPerTable})
	if err != nil {
		fmt.Fprintf(os.Stderr, "J3b PostgreSQL backfill failed and was rolled back: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3b PostgreSQL backfill report: %v\n", err)
		os.Exit(1)
	}
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

func runJ3bModelCheckPostgresBackfillReadback(rawURL string, maxRowsPerTable int64) {
	rawURL = strings.TrimSpace(rawURL)
	if j3bPostgresReadbackURLRequiredExitCode(rawURL) != 0 {
		fmt.Fprintln(os.Stderr, "J3b PostgreSQL backfill readback requires --j3b-postgres-readback-url with an explicit maintenance-scoped PostgreSQL URL")
		os.Exit(2)
	}
	db, err := j3bmodelcheck.Open(rawURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open J3b PostgreSQL backfill readback connection: %v\n", err)
		os.Exit(2)
	}
	defer db.Close()
	report, err := j3bmodelcheck.VerifyPostgresBackfill(context.Background(), db, j3bmodelcheck.PostgresReadbackOptions{MaxRowsPerTable: maxRowsPerTable})
	if err != nil {
		fmt.Fprintf(os.Stderr, "J3b PostgreSQL backfill readback failed: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3b PostgreSQL backfill readback report: %v\n", err)
		os.Exit(1)
	}
	if j3bPostgresReadbackExitCode(report) != 0 {
		os.Exit(3)
	}
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

func runJ3bModelCheckInventory(evidencePath string) {
	if j3bInventoryEvidenceRequiredExitCode(evidencePath) != 0 {
		fmt.Fprintln(os.Stderr, "J3b inventory verification requires --j3b-inventory-evidence with an explicit JSON evidence file")
		os.Exit(2)
	}
	evidence, err := j3bmodelcheck.LoadLegacyJ3bFactEvidence(evidencePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "J3b inventory evidence input failed: %v\n", err)
		os.Exit(2)
	}
	report := j3bmodelcheck.ValidateLegacyJ3bFactCoverage(j3bmodelcheck.LegacyJ3bFactInventory, evidence)
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3b inventory coverage report: %v\n", err)
		os.Exit(1)
	}
	if j3bInventoryExitCode(report) != 0 {
		os.Exit(3)
	}
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

func runJ3cReadOnlyBoundaryCheck() {
	root := resolveRepositoryRoot()
	report, err := ownermanifest.VerifyJ3cReadOnlyBoundary(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "J3c read-only boundary verification failed: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3c read-only boundary report: %v\n", err)
		os.Exit(1)
	}
	if !report.ReadOnlyAuditReady || !report.J3cOwnerReady {
		os.Exit(3)
	}
}

func runJ3bCutoverEvidenceCheck(path string) {
	report, err := businesshandoff.VerifyJ3bCutoverEvidence(path, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "J3b cutover evidence input failed: %v\n", err)
		os.Exit(2)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3b cutover evidence report: %v\n", err)
		os.Exit(1)
	}
	if exitCode := j3bCutoverEvidenceExitCode(report); exitCode != 0 {
		os.Exit(exitCode)
	}
}

func j3bCutoverEvidenceExitCode(report businesshandoff.J3bCutoverEvidenceReport) int {
	if !report.Ready {
		return 3
	}
	return 0
}

func runGatewayRouteOwnerManifestCheck() {
	manifestPath := resolveRepoPath(envOrDefault("JUHE_AI_MAINTENANCE_GATEWAY_ROUTE_MANIFEST", "docs/migration/GatewayManagementRouteOwnerManifest.json"))
	root := resolveRepositoryRoot()
	report, err := ownermanifest.VerifyGatewayRouteOwnerManifest(manifestPath, root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Gateway route owner manifest verification failed: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode Gateway route owner manifest report: %v\n", err)
		os.Exit(1)
	}
	if len(report.PendingFamilies) > 0 {
		os.Exit(3)
	}
}

func runBusinessSQLiteSchemaCheck(path string) {
	if strings.TrimSpace(path) == "" {
		path = strings.TrimSpace(os.Getenv("JUHE_AI_MAINTENANCE_BUSINESS_SQLITE_PATH"))
	}
	if strings.TrimSpace(path) == "" {
		fmt.Fprintln(os.Stderr, "Business SQLite schema preflight requires --business-sqlite-path or JUHE_AI_MAINTENANCE_BUSINESS_SQLITE_PATH")
		os.Exit(2)
	}
	report, err := businesshandoff.VerifySQLiteSchema(context.Background(), path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Business SQLite schema preflight failed: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode Business SQLite schema preflight report: %v\n", err)
		os.Exit(1)
	}
	if !report.Ready {
		os.Exit(3)
	}
}

func runBusinessCapabilityManifestCheck() {
	capabilityPath := resolveRepoPath(envOrDefault("JUHE_AI_MAINTENANCE_CAPABILITY_MANIFEST", "docs/migration/GoBusinessCapabilityManifest.json"))
	operationPath := resolveRepoPath(envOrDefault("JUHE_AI_MAINTENANCE_OWNER_MANIFEST", "docs/migration/BusinessSQLite-owner-manifest.json"))
	report, err := ownermanifest.VerifyCapabilityManifest(capabilityPath, operationPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Business capability manifest verification failed: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode Business capability manifest report: %v\n", err)
		os.Exit(1)
	}
	// A valid manifest is still only a completeness proof. Any capability
	// marked missing/partial keeps the handoff gate closed.
	if report.StatusCoverage["missing"] > 0 || report.StatusCoverage["partial"] > 0 {
		os.Exit(3)
	}
}

func runJ3bModelCheckSQLiteReadback() {
	targetPath := strings.TrimSpace(os.Getenv(j3bmodelcheck.SQLiteBootstrapEnv))
	datasetPath := strings.TrimSpace(os.Getenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH"))
	statsPath := strings.TrimSpace(os.Getenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH"))
	if targetPath == "" || datasetPath == "" || statsPath == "" {
		fmt.Fprintln(os.Stderr, "J3b SQLite readback requires JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH, JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH and JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH")
		os.Exit(2)
	}
	report, err := j3bmodelcheck.VerifySQLiteBackfill(context.Background(), targetPath, datasetPath, statsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "J3b SQLite readback failed: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3b SQLite readback report: %v\n", err)
		os.Exit(1)
	}
	if j3bSQLiteReadbackExitCode(report) != 0 {
		os.Exit(3)
	}
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

func runBusinessSQLiteHandoffCheck(businessPath, j3bPath string) {
	if strings.TrimSpace(businessPath) == "" {
		businessPath = strings.TrimSpace(os.Getenv("JUHE_AI_MAINTENANCE_BUSINESS_SQLITE_PATH"))
	}
	if strings.TrimSpace(j3bPath) == "" {
		j3bPath = strings.TrimSpace(os.Getenv(j3bmodelcheck.SQLiteBootstrapEnv))
	}
	if strings.TrimSpace(businessPath) == "" || strings.TrimSpace(j3bPath) == "" {
		fmt.Fprintln(os.Stderr, "Business SQLite handoff preflight requires --business-sqlite-path/--j3b-sqlite-path or JUHE_AI_MAINTENANCE_BUSINESS_SQLITE_PATH/JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH")
		os.Exit(2)
	}
	report, err := businesshandoff.Verify(context.Background(), businessPath, j3bPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Business SQLite handoff preflight failed: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode Business SQLite handoff preflight report: %v\n", err)
		os.Exit(1)
	}
	if !report.Ready {
		os.Exit(3)
	}
}

func runNodeJ3bActivePathCheck() {
	root := resolveRepositoryRoot()
	report, err := ownermanifest.ScanNodeJ3bActivePaths(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Node J3b active-path scan failed: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode Node J3b active-path report: %v\n", err)
		os.Exit(1)
	}
	if len(report.Findings) > 0 {
		os.Exit(3)
	}
}

func resolveRepositoryRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for depth := 0; depth <= 8; depth++ {
		if info, statErr := os.Stat(filepath.Join(dir, "backend", "src")); statErr == nil && info.IsDir() {
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

func runBusinessOwnerManifestCheck() {
	manifestPath := resolveRepoPath(envOrDefault("JUHE_AI_MAINTENANCE_OWNER_MANIFEST", "docs/migration/BusinessSQLite-owner-manifest.json"))
	typesPath := resolveRepoPath(envOrDefault("JUHE_AI_MAINTENANCE_DB_SERVICE_TYPES", "backend/src/modules/db-service/db-service-types.ts"))
	accessPath := resolveRepoPath(envOrDefault("JUHE_AI_MAINTENANCE_DB_SERVICE_ACCESS", "backend/src/modules/db-service/db-service-operation-access-mode.ts"))
	handlerPath := resolveRepoPath(envOrDefault("JUHE_AI_MAINTENANCE_DB_SERVICE_HANDLERS", "backend/src/modules/db-service/db-service-handlers.ts"))
	report, err := ownermanifest.Verify(manifestPath, typesPath, accessPath, handlerPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Business SQLite owner manifest verification failed: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode Business SQLite owner manifest report: %v\n", err)
		os.Exit(1)
	}
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

func runJ3bModelCheckSQLiteBackfill(nodeStopped, goStopped, backupConfirmed bool, evidencePath string) {
	if !nodeStopped || !goStopped || !backupConfirmed {
		fmt.Fprintln(os.Stderr, "J3b SQLite backfill requires --node-stopped --go-stopped --backup-confirmed")
		os.Exit(2)
	}
	if report, exitCode, err := j3bBackfillEvidencePreflight(evidencePath); err != nil {
		fmt.Fprintf(os.Stderr, "J3b backfill evidence input failed: %v\n", err)
		os.Exit(exitCode)
	} else if exitCode != 0 {
		fmt.Fprintf(os.Stderr, "J3b backfill evidence verification failed: %s\n", strings.Join(report.Errors, "; "))
		os.Exit(exitCode)
	}
	targetPath := strings.TrimSpace(os.Getenv(j3bmodelcheck.SQLiteBootstrapEnv))
	datasetPath := strings.TrimSpace(os.Getenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH"))
	statsPath := strings.TrimSpace(os.Getenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH"))
	if targetPath == "" || datasetPath == "" || statsPath == "" {
		fmt.Fprintln(os.Stderr, "J3b SQLite backfill requires JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH, JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH and JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH")
		os.Exit(2)
	}
	if err := j3bmodelcheck.ValidateSQLiteBackfillPaths(targetPath, datasetPath, statsPath); err != nil {
		fmt.Fprintf(os.Stderr, "J3b SQLite backfill path isolation failed: %v\n", err)
		os.Exit(2)
	}
	target, err := j3bmodelcheck.OpenSQLite(targetPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open J3b SQLite backfill target: %v\n", err)
		os.Exit(2)
	}
	defer target.Close()
	report, err := j3bmodelcheck.BackfillSQLite(context.Background(), target, datasetPath, statsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "J3b SQLite backfill failed: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3b SQLite backfill report: %v\n", err)
		os.Exit(1)
	}
}

func runJ3bModelCheckSQLiteBootstrap(apply, nodeStopped, goStopped, backupConfirmed bool) {
	path := strings.TrimSpace(os.Getenv(j3bmodelcheck.SQLiteBootstrapEnv))
	if path == "" {
		fmt.Fprintf(os.Stderr, "J3b SQLite bootstrap requires %s\n", j3bmodelcheck.SQLiteBootstrapEnv)
		os.Exit(2)
	}
	if apply && (!nodeStopped || !goStopped || !backupConfirmed) {
		fmt.Fprintln(os.Stderr, "J3b SQLite apply requires --node-stopped --go-stopped --backup-confirmed")
		os.Exit(2)
	}
	db, err := j3bmodelcheck.OpenSQLite(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open J3b SQLite bootstrap connection: %v\n", err)
		os.Exit(2)
	}
	defer db.Close()
	report, err := j3bmodelcheck.RunSQLite(context.Background(), db, apply)
	if err != nil {
		fmt.Fprintf(os.Stderr, "J3b SQLite bootstrap failed: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3b SQLite bootstrap report: %v\n", err)
		os.Exit(1)
	}
	if !report.Ready() {
		os.Exit(3)
	}
}

func runJ3bModelCheckBootstrap(apply bool) {
	rawURL := strings.TrimSpace(os.Getenv(j3bmodelcheck.BootstrapEnv))
	if rawURL == "" {
		fmt.Fprintf(os.Stderr, "J3b PostgreSQL bootstrap requires %s\n", j3bmodelcheck.BootstrapEnv)
		os.Exit(2)
	}
	db, err := j3bmodelcheck.Open(rawURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open J3b PostgreSQL bootstrap connection: %v\n", err)
		os.Exit(2)
	}
	defer db.Close()
	report, err := j3bmodelcheck.Run(context.Background(), db, apply)
	if err != nil {
		fmt.Fprintf(os.Stderr, "J3b PostgreSQL bootstrap failed: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3b PostgreSQL bootstrap report: %v\n", err)
		os.Exit(1)
	}
	if !report.Ready() {
		os.Exit(3)
	}
}

func runJ3aProxyLatencyBootstrap(apply bool) {
	rawURL := strings.TrimSpace(os.Getenv(j3aproxylatency.BootstrapEnv))
	if rawURL == "" {
		fmt.Fprintf(os.Stderr, "J3a PostgreSQL bootstrap requires %s\n", j3aproxylatency.BootstrapEnv)
		os.Exit(2)
	}
	db, err := j3aproxylatency.Open(rawURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open J3a PostgreSQL bootstrap connection: %v\n", err)
		os.Exit(2)
	}
	defer db.Close()
	report, err := j3aproxylatency.Run(context.Background(), db, apply)
	if err != nil {
		fmt.Fprintf(os.Stderr, "J3a PostgreSQL bootstrap failed: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode J3a PostgreSQL bootstrap report: %v\n", err)
		os.Exit(1)
	}
	if !report.Ready() {
		os.Exit(3)
	}
}
