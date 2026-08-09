// Command juhe-ai-audit-log-migrate is an explicit, offline-only migration
// utility. It is intentionally not imported by the server or any worker.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/huanminabc/juhe-ai/backend-go/internal/auditlog"
)

func main() {
	var options auditlog.LegacyMigrationOptions
	flag.StringVar(&options.SourceDatabasePath, "source-db", "", "旧 Node SQLite 审计数据库文件")
	flag.StringVar(&options.TargetDatabasePath, "target-db", "", "Go F3 专用 SQLite 数据库文件")
	flag.StringVar(&options.SourceBlobDirectory, "source-blob-dir", "", "旧审计 blob 根目录")
	flag.StringVar(&options.TargetBlobDirectory, "target-blob-dir", "", "Go F3 blob 根目录")
	flag.BoolVar(&options.NodeStopped, "node-stopped", false, "确认 Node 服务已停止")
	flag.BoolVar(&options.GoStopped, "go-stopped", false, "确认 Go 服务/worker 已停止")
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "离线迁移旧 Node 审计 SQLite 到 Go F3 专库。")
		fmt.Fprintln(flag.CommandLine.Output(), "停机前提：执行前必须停止 Node 和 Go 的全部写入进程；本命令不会替你停止进程，也不会自动运行。")
		fmt.Fprintln(flag.CommandLine.Output(), "示例：juhe-ai-audit-log-migrate --source-db old.sqlite --target-db audit.sqlite --source-blob-dir old-blobs --target-blob-dir audit-blobs --node-stopped --go-stopped")
		flag.PrintDefaults()
	}
	flag.Parse()

	result, err := auditlog.MigrateLegacySQLite(context.Background(), options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "F3 审计 SQLite 迁移失败：%v\n", err)
		os.Exit(1)
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "序列化迁移结果失败：%v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(encoded))
}
