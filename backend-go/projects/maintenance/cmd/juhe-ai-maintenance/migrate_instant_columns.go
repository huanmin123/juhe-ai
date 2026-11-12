// migrate-instant-columns 是 Phase 2 的只读迁移计划命令：
//
//	juhe-ai-maintenance migrate-instant-columns --dsn "$JUHE_AI_POSTGRES_URL" \
//	  [--manifest /tmp/time-inventory.json] [--include-review] [--out /tmp/instant-plan.json]
//
// 它只生成 ALTER TYPE 计划与 SQL，不执行 DDL；真正执行由后续 schema
// revision 步骤在停写窗口内调用 instantmigration.Apply。
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

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/instantmigration"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/timeinventory"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const migrateInstantColumnsCommandName = "migrate-instant-columns"

func runMigrateInstantColumnsCommand(argv []string) int {
	fs := flag.NewFlagSet(migrateInstantColumnsCommandName, flag.ContinueOnError)
	dsn := fs.String("dsn", "", "postgres URL used when --manifest is absent (read-only)")
	manifestPath := fs.String("manifest", "", "optional time-inventory JSON from audit-time-columns")
	includeReview := fs.Bool("include-review", false, "include review=true columns in the plan")
	scopeFlag := fs.String("scope", "phase2", "migration scope: phase2 (non-stats/non-usage) or all")
	out := fs.String("out", "", "optional plan JSON output path; stdout when empty")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	var report timeinventory.Report
	rawDSN := strings.TrimSpace(*dsn)
	if strings.TrimSpace(*manifestPath) != "" {
		data, err := os.ReadFile(*manifestPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s 读取 --manifest 失败: %v\n", migrateInstantColumnsCommandName, err)
			return 1
		}
		if err := json.Unmarshal(data, &report); err != nil {
			fmt.Fprintf(os.Stderr, "%s 解析 --manifest 失败: %v\n", migrateInstantColumnsCommandName, err)
			return 1
		}
	} else {
		if rawDSN == "" {
			fmt.Fprintf(os.Stderr, "%s 需要 --dsn 或 --manifest\n", migrateInstantColumnsCommandName)
			return 2
		}
		parsed, err := url.Parse(rawDSN)
		if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
			fmt.Fprintf(os.Stderr, "%s 的 --dsn 必须是 postgres:// 或 postgresql:// URL\n", migrateInstantColumnsCommandName)
			return 2
		}
		collected, err := collectTimeInventory(rawDSN)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s 收集清单失败: %v\n", migrateInstantColumnsCommandName, err)
			return 1
		}
		report = collected
	}
	var scope instantmigration.Scope
	switch strings.TrimSpace(*scopeFlag) {
	case "phase2":
		scope = instantmigration.Phase2Scope
	case "all":
		scope = instantmigration.AllScope
	default:
		fmt.Fprintf(os.Stderr, "%s 的 --scope 只能是 phase2 或 all\n", migrateInstantColumnsCommandName)
		return 2
	}
	plan := instantmigration.BuildPlan(report, *includeReview, scope)
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s 编码计划失败: %v\n", migrateInstantColumnsCommandName, err)
		return 1
	}
	data = append(data, '\n')
	if strings.TrimSpace(*out) == "" {
		if _, err := os.Stdout.Write(data); err != nil {
			fmt.Fprintf(os.Stderr, "%s 写 stdout 失败: %v\n", migrateInstantColumnsCommandName, err)
			return 1
		}
		return 0
	}
	if err := os.WriteFile(*out, data, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "%s 写计划文件失败: %v\n", migrateInstantColumnsCommandName, err)
		return 1
	}
	return 0
}

func collectTimeInventory(rawDSN string) (timeinventory.Report, error) {
	db, err := sql.Open("pgx", rawDSN)
	if err != nil {
		return timeinventory.Report{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return timeinventory.Report{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`SELECT set_config('statement_timeout', '60s', true), set_config('lock_timeout', '10s', true)`); err != nil {
		return timeinventory.Report{}, err
	}
	report, err := timeinventory.Collect(ctx, tx, timeinventory.DefaultSchemas)
	if err != nil {
		return timeinventory.Report{}, err
	}
	if err := tx.Commit(); err != nil {
		return timeinventory.Report{}, err
	}
	return report, nil
}
