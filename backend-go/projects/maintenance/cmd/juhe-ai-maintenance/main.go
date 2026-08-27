package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3aproxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3bmodelcheck"
)

func main() {
	version := flag.Bool("version", false, "print the maintenance project contract version")
	check := flag.Bool("check-boundary", false, "verify the scaffold boundary")
	j3Check := flag.Bool("check-j3a-proxy-latency-postgres", false, "read-only verify pre-provisioned J3a PostgreSQL jobs schema")
	j3Apply := flag.Bool("apply-j3a-proxy-latency-postgres", false, "add missing J3a PostgreSQL jobs tables/indexes after explicit authorization")
	j3bCheck := flag.Bool("check-j3b-model-check-postgres", false, "read-only verify pre-provisioned J3b PostgreSQL juhe_j3b schema")
	j3bApply := flag.Bool("apply-j3b-model-check-postgres", false, "add missing J3b PostgreSQL juhe_j3b tables/indexes after explicit authorization")
	j3bSQLiteCheck := flag.Bool("check-j3b-model-check-sqlite", false, "read-only verify dedicated J3b SQLite schema")
	j3bSQLiteApply := flag.Bool("apply-j3b-model-check-sqlite", false, "bootstrap dedicated J3b SQLite schema after stop and backup confirmations")
	nodeStopped := flag.Bool("node-stopped", false, "confirm Node writers are stopped for an offline migration")
	goStopped := flag.Bool("go-stopped", false, "confirm Go owners are stopped for an offline migration")
	backupConfirmed := flag.Bool("backup-confirmed", false, "confirm a recoverable backup was verified")
	j3bBackfill := flag.Bool("backfill-j3b-model-check-sqlite", false, "copy legacy J3b SQLite facts into the dedicated file")
	flag.Parse()
	if *version {
		fmt.Printf("juhe-ai-maintenance project=%s contract=%s\n", contracts.ProjectMaintenance, contracts.ArchitectureVersion)
		return
	}
	if *check {
		fmt.Println("juhe-ai-maintenance boundary=ready runtime=one-shot-scaffold")
		return
	}
	if *j3Check || *j3Apply || *j3bCheck || *j3bApply || *j3bSQLiteCheck || *j3bSQLiteApply || *j3bBackfill {
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
		if *j3bSQLiteCheck || *j3bSQLiteApply {
			runJ3bModelCheckSQLiteBootstrap(*j3bSQLiteApply, *nodeStopped, *goStopped, *backupConfirmed)
			return
		}
		if *j3bBackfill {
			runJ3bModelCheckSQLiteBackfill(*nodeStopped, *goStopped, *backupConfirmed)
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

func runJ3bModelCheckSQLiteBackfill(nodeStopped, goStopped, backupConfirmed bool) {
	if !nodeStopped || !goStopped || !backupConfirmed {
		fmt.Fprintln(os.Stderr, "J3b SQLite backfill requires --node-stopped --go-stopped --backup-confirmed")
		os.Exit(2)
	}
	targetPath := strings.TrimSpace(os.Getenv(j3bmodelcheck.SQLiteBootstrapEnv))
	datasetPath := strings.TrimSpace(os.Getenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH"))
	statsPath := strings.TrimSpace(os.Getenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH"))
	if targetPath == "" || datasetPath == "" || statsPath == "" {
		fmt.Fprintln(os.Stderr, "J3b SQLite backfill requires JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH, JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH and JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH")
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
