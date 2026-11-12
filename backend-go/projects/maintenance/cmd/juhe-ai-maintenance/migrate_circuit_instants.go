// migrate-circuit-instants 是 Phase 5 的只读预检命令。
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

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/epochmsmigration"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const migrateCircuitInstantsCommandName = "migrate-circuit-instants"

func runMigrateCircuitInstantsCommand(argv []string) int {
	fs := flag.NewFlagSet(migrateCircuitInstantsCommandName, flag.ContinueOnError)
	dsn := fs.String("dsn", "", "postgres URL (read-only preflight)")
	out := fs.String("out", "", "optional JSON output path; stdout when empty")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	rawDSN := strings.TrimSpace(*dsn)
	if rawDSN == "" {
		fmt.Fprintf(os.Stderr, "%s 需要 --dsn\n", migrateCircuitInstantsCommandName)
		return 2
	}
	parsed, err := url.Parse(rawDSN)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		fmt.Fprintf(os.Stderr, "%s 的 --dsn 必须是 postgres:// 或 postgresql:// URL\n", migrateCircuitInstantsCommandName)
		return 2
	}
	db, err := sql.Open("pgx", rawDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s 打开数据库失败: %v\n", migrateCircuitInstantsCommandName, err)
		return 1
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	plan, err := epochmsmigration.Build(ctx, db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s 预检失败: %v\n", migrateCircuitInstantsCommandName, err)
		return 1
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return 1
	}
	data = append(data, '\n')
	if strings.TrimSpace(*out) == "" {
		_, _ = os.Stdout.Write(data)
	} else if err := os.WriteFile(*out, data, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "%s 写报告失败: %v\n", migrateCircuitInstantsCommandName, err)
		return 1
	}
	if !plan.Ready() {
		return 3
	}
	return 0
}
