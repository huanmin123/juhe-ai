// migrate-usage-records 是 Phase 4 的只读 prepare 预检命令：
//
//	juhe-ai-maintenance migrate-usage-records --dsn "$JUHE_AI_POSTGRES_URL" \
//	  [--out /tmp/usage-records-prepare.json]
//
// 只输出父表/分区/行数/重复 id/时间文本规范化/水位列/影子表状态，不执行
// 任何 DDL。影子表构建与 swap 由后续 revision 步骤在发布窗执行。
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

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/usagerecordsmigration"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const migrateUsageRecordsCommandName = "migrate-usage-records"

func runMigrateUsageRecordsCommand(argv []string) int {
	fs := flag.NewFlagSet(migrateUsageRecordsCommandName, flag.ContinueOnError)
	dsn := fs.String("dsn", "", "postgres URL (read-only prepare preflight)")
	out := fs.String("out", "", "optional JSON output path; stdout when empty")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	rawDSN := strings.TrimSpace(*dsn)
	if rawDSN == "" {
		fmt.Fprintf(os.Stderr, "%s 需要 --dsn\n", migrateUsageRecordsCommandName)
		return 2
	}
	parsed, err := url.Parse(rawDSN)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		fmt.Fprintf(os.Stderr, "%s 的 --dsn 必须是 postgres:// 或 postgresql:// URL\n", migrateUsageRecordsCommandName)
		return 2
	}
	db, err := sql.Open("pgx", rawDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s 打开数据库失败: %v\n", migrateUsageRecordsCommandName, err)
		return 1
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	plan, err := usagerecordsmigration.PlanPrepare(ctx, db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s 预检失败: %v\n", migrateUsageRecordsCommandName, err)
		return 1
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s 编码报告失败: %v\n", migrateUsageRecordsCommandName, err)
		return 1
	}
	data = append(data, '\n')
	if strings.TrimSpace(*out) == "" {
		if _, err := os.Stdout.Write(data); err != nil {
			fmt.Fprintf(os.Stderr, "%s 写 stdout 失败: %v\n", migrateUsageRecordsCommandName, err)
			return 1
		}
	} else if err := os.WriteFile(*out, data, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "%s 写报告失败: %v\n", migrateUsageRecordsCommandName, err)
		return 1
	}
	if !plan.Ready() {
		return 3
	}
	return 0
}
