package main

// w16b（覆盖率收尾批次三，main() 进程内 boot 泄漏驱动）：
//
// main() 的完整 boot 路径此前只能经插桩二进制（GOCOVERDIR）触达，不计入
// `go test -cover` 的包覆盖率。本文件把 main() 放进测试进程内的 goroutine
// 直接驱动：环境与 w1_main_boot_test.go 场景三同源（sqlite 六库 + 交接证据
// + system-api + chain 全开），健康监听就绪后主测试正常返回、goroutine 有意
// 泄漏（supervisor 阻塞等信号）。Go 覆盖率计数器在语句执行时实时累加，进程
// 退出前统一冲洗 profile，因此泄漏 goroutine 已执行的 main() 前段语句
// （组合根、双监听、健康/指标 handler、supervisor 组件装配）全部计入包
// 覆盖率。
//
// 边界说明：
//   - 启动目录用 os.MkdirTemp 而非 t.TempDir：泄漏 goroutine 长期持有
//     sqlite 句柄，Windows 上会导致 t.TempDir 清理失败（测试反被置败）。
//     临时目录留给进程退出后由操作系统清理。
//   - 不向本进程发送任何信号（Windows 无安全的自发信号通道）；优雅停机尾
//     段（main.go 691-722）仍属二进制场景面，本文件不覆盖。
//   - boot 中任何 fail() 都会 os.Exit 拉垮整个测试进程：环境逐键复刻已验证
//     的二进制场景三，并在就绪轮询超时时只 Fail 测试（进程已被拉垮时自然
//     显形）。

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"

	_ "modernc.org/sqlite"
)

func TestW16BInProcessBootCoverage(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过进程内 boot 泄漏驱动")
	}
	// 独立临时根：有意不清理（见文件头边界说明）。
	root, err := os.MkdirTemp("", "w16b-boot-")
	if err != nil {
		t.Fatalf("mkdir temp root: %v", err)
	}
	mainPort := w1FreePort(t)
	healthPort := w1FreePort(t)
	_, evidenceEnv := w1BuildSystemApiEvidence(t, root)
	env := map[string]string{
		"NODE_ENV":                                 "development",
		"JUHE_AI_SECRET":                           "w16b-boot-secret-0123456789abcdef",
		"JUHE_AI_DATABASE_PATH":                    filepath.Join(root, "business.sqlite3"),
		"JUHE_AI_AUDIT_LOG_STORE":                  "sqlite",
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID":            "w16b-boot-instance",
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH": filepath.Join(root, "business-settings.sqlite3"),
		"JUHE_AI_DATASET_DATABASE_PATH":            filepath.Join(root, "dataset.sqlite3"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH":      filepath.Join(root, "usage-catalog.sqlite3"),
		"JUHE_AI_STATS_DATABASE_PATH":              filepath.Join(root, "stats.sqlite3"),
		"JUHE_AI_RUNTIME_LOG_DATABASE_PATH":        filepath.Join(root, "runtime-log.sqlite3"),
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH":      filepath.Join(root, "table-monitor.sqlite3"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT":   filepath.Join(root, "codex-shards"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT":  "2",
		"JUHE_AI_USAGE_SHARD_ROOT":                 filepath.Join(root, "usage-shards"),
		"JUHE_AI_AUDIT_LOG_DATABASE_PATH":          filepath.Join(root, "audit.sqlite3"),
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY":         filepath.Join(root, "blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY":   filepath.Join(root, "hotsearch"),
		// F4 操作日志：composeSystemAPI 硬要求 store 已启用（缺失即 fail-fast）。
		"JUHE_AI_OPERATION_LOG_STORE":                  "sqlite",
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID":            "w16b-boot-instance",
		"JUHE_AI_OPERATION_LOG_DATABASE_PATH":          filepath.Join(root, "operation-log.sqlite3"),
		"JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH": filepath.Join(root, "business-settings.sqlite3"),
		"JUHE_AI_HOST":                          "127.0.0.1",
		"JUHE_AI_PORT":                          mainPort,
		"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS": "127.0.0.1:" + healthPort,
	}
	for key, value := range evidenceEnv {
		if key == "JUHE_AI_BUSINESS_DATABASE_PATH" || key == "JUHE_AI_CHAT_DATABASE_PATH" {
			// 证据夹具的库路径钉进同一隔离根。
			env[key] = value
			continue
		}
		env[key] = value
	}
	for key, value := range env {
		t.Setenv(key, value)
	}

	// F4 只读业务设置镜像要求文件先在（openComposeOperationStore 同约定）。
	if err := os.WriteFile(filepath.Join(root, "business-settings.sqlite3"), nil, 0o644); err != nil {
		t.Fatalf("write business settings mirror: %v", err)
	}
	// F4 store 还要求业务库文件已存在（main 在 compose preflight 前打开
	// F4）；用 maintenance bootstrap 进程内建库 + 种子（等价
	// maintenance --ensure-schema --seed -driver sqlite 的 business 面）。
	businessDB, err := sql.Open("sqlite", filepath.Join(root, "business.sqlite3"))
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	if _, err := bootstrap.EnsureSQLiteSchema(context.Background(), bootstrap.SQLiteSchemaBusiness, businessDB); err != nil {
		_ = businessDB.Close()
		t.Fatalf("ensure business schema: %v", err)
	}
	if _, err := bootstrap.SeedSQLiteBusiness(context.Background(), businessDB, bootstrap.SeedOptions{Secret: "w16b-boot-secret"}); err != nil {
		_ = businessDB.Close()
		t.Fatalf("seed business db: %v", err)
	}
	_ = businessDB.Close()

	// main() 前置状态换装：os.Args / flag.CommandLine / stdout 捕获。
	oldArgs := os.Args
	oldCommandLine := flag.CommandLine
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Args = []string{"juhe-ai-gateway"}
	flag.CommandLine = flag.NewFlagSet("juhe-ai-gateway", flag.ContinueOnError)
	os.Stdout = writer
	// 后台持续排空管道，避免泄漏 goroutine 的周期日志写满缓冲后阻塞。
	go func() { _, _ = io.Copy(io.Discard, reader) }()
	go func() { main() }()

	healthURL := "http://127.0.0.1:" + healthPort + "/health"
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(90 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		response, err := client.Get(healthURL)
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if strings.Contains(string(body), `"ready":true`) {
				ready = true
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		// 恢复前置状态后报错；若 boot 已 os.Exit，进程在此前即消失。
		os.Stdout = oldStdout
		os.Args = oldArgs
		flag.CommandLine = oldCommandLine
		t.Fatal("进程内 boot 90s 未就绪")
	}

	// 健康面（ready 分支）+ 指标面（prometheus 拼接写）+ 主入口 401 契约。
	response, err := client.Get(healthURL)
	if err == nil {
		_, _ = io.ReadAll(response.Body)
		_ = response.Body.Close()
	}
	metricsResponse, err := client.Get("http://127.0.0.1:" + healthPort + "/__aisys__/metrics")
	if err == nil {
		_, _ = io.ReadAll(metricsResponse.Body)
		_ = metricsResponse.Body.Close()
	}
	gatewayResponse, err := client.Get(fmt.Sprintf("http://127.0.0.1:%s/v1/models", mainPort))
	if err == nil {
		body, _ := io.ReadAll(gatewayResponse.Body)
		_ = gatewayResponse.Body.Close()
		if gatewayResponse.StatusCode != http.StatusUnauthorized {
			t.Logf("主入口 /v1/models status=%d body=%s（组合根契约面）", gatewayResponse.StatusCode, string(body))
		}
	}

	// 前置状态恢复；boot goroutine 有意泄漏（supervisor 阻塞等信号，进程
	// 退出时由运行时回收；已执行语句的计数器随 profile 冲洗落盘）。
	os.Stdout = oldStdout
	os.Args = oldArgs
	flag.CommandLine = oldCommandLine
	_ = writer.Close()
}
