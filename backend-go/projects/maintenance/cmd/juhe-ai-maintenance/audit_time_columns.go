// audit-time-columns 是时间类型迁移的只读清单命令：
//
//	juhe-ai-maintenance audit-time-columns --dsn "$JUHE_AI_POSTGRES_URL" [--out /tmp/time-inventory.json]
//
// 它只读扫描 information_schema / pg_catalog，输出每列的目标类型、精度建议与
// 主键/分区键/索引/约束/触发器依赖；不执行任何 DDL。真正的 ALTER TYPE 由后续
// schema revision 步骤承载。
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

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/timeinventory"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const auditTimeColumnsCommandName = "audit-time-columns"

func runAuditTimeColumnsCommand(argv []string) int {
	fs := flag.NewFlagSet(auditTimeColumnsCommandName, flag.ContinueOnError)
	dsn := fs.String("dsn", "", "postgres URL (read-only)")
	out := fs.String("out", "", "optional JSON output path; stdout when empty")
	schemasFlag := fs.String("schemas", strings.Join(timeinventory.DefaultSchemas, ","),
		"comma-separated schemas to inspect")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	rawDSN := strings.TrimSpace(*dsn)
	if rawDSN == "" {
		fmt.Fprintf(os.Stderr, "%s 需要 --dsn 指向显式的 PostgreSQL URL\n", auditTimeColumnsCommandName)
		return 2
	}
	parsed, err := url.Parse(rawDSN)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		fmt.Fprintf(os.Stderr, "%s 的 --dsn 必须是 postgres:// 或 postgresql:// URL（只读工具不支持其它方言）\n", auditTimeColumnsCommandName)
		return 2
	}
	schemas := splitSchemaList(*schemasFlag)
	if len(schemas) == 0 {
		fmt.Fprintf(os.Stderr, "%s 的 --schemas 不能为空\n", auditTimeColumnsCommandName)
		return 2
	}

	db, err := sql.Open("pgx", rawDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s 打开连接失败: %v\n", auditTimeColumnsCommandName, err)
		return 1
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s 开启只读事务失败: %v\n", auditTimeColumnsCommandName, err)
		return 1
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`SELECT set_config('statement_timeout', '60s', true), set_config('lock_timeout', '10s', true)`); err != nil {
		fmt.Fprintf(os.Stderr, "%s 设置只读超时失败: %v\n", auditTimeColumnsCommandName, err)
		return 1
	}
	report, err := timeinventory.Collect(ctx, tx, schemas)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s 收集时间列清单失败: %v\n", auditTimeColumnsCommandName, err)
		return 1
	}
	if err := tx.Commit(); err != nil {
		fmt.Fprintf(os.Stderr, "%s 提交只读事务失败: %v\n", auditTimeColumnsCommandName, err)
		return 1
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s 编码 JSON 失败: %v\n", auditTimeColumnsCommandName, err)
		return 1
	}
	data = append(data, '\n')
	if strings.TrimSpace(*out) == "" {
		if _, err := os.Stdout.Write(data); err != nil {
			fmt.Fprintf(os.Stderr, "%s 写 stdout 失败: %v\n", auditTimeColumnsCommandName, err)
			return 1
		}
		return 0
	}
	if err := os.WriteFile(*out, data, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "%s 写清单文件失败: %v\n", auditTimeColumnsCommandName, err)
		return 1
	}
	return 0
}

func splitSchemaList(value string) []string {
	seen := map[string]bool{}
	var result []string
	for _, part := range strings.Split(value, ",") {
		schema := strings.TrimSpace(part)
		if schema == "" || seen[schema] {
			continue
		}
		seen[schema] = true
		result = append(result, schema)
	}
	return result
}
