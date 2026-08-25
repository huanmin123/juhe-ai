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
)

func main() {
	version := flag.Bool("version", false, "print the maintenance project contract version")
	check := flag.Bool("check-boundary", false, "verify the scaffold boundary")
	j3Check := flag.Bool("check-j3a-proxy-latency-postgres", false, "read-only verify pre-provisioned J3a PostgreSQL jobs schema")
	j3Apply := flag.Bool("apply-j3a-proxy-latency-postgres", false, "add missing J3a PostgreSQL jobs tables/indexes after explicit authorization")
	flag.Parse()
	if *version {
		fmt.Printf("juhe-ai-maintenance project=%s contract=%s\n", contracts.ProjectMaintenance, contracts.ArchitectureVersion)
		return
	}
	if *check {
		fmt.Println("juhe-ai-maintenance boundary=ready runtime=one-shot-scaffold")
		return
	}
	if *j3Check || *j3Apply {
		if *j3Check && *j3Apply {
			fmt.Fprintln(os.Stderr, "J3a PostgreSQL bootstrap flags are mutually exclusive")
			os.Exit(2)
		}
		runJ3aProxyLatencyBootstrap(*j3Apply)
		return
	}
	fmt.Fprintln(os.Stderr, "maintenance project runtime is not switched yet; select an explicit one-shot command")
	os.Exit(2)
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
