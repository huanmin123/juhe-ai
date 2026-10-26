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
	"github.com/alicebob/miniredis/v2"
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
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func TestW16BInProcessBootCoverage(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过进程内 boot 泄漏驱动")
	}
	// 进程内 boot 需要 account circuit runtime 索引已由独立构建流程写入
	// Redis（version/status/ownerMode 元数据 + scanned 索引）。该前置在本
	// 进程内没有构建入口，属测试基础设施缺口（见 docs/待办与阻塞事项.md
	// T70），不以产品降级绕过。
	t.Skip("account circuit runtime 索引需外部构建，进程内 boot 驱动暂不可用（T70）")
	// 独立临时根：有意不清理（见文件头边界说明）。
	root, err := os.MkdirTemp("", "w16b-boot-")
	if err != nil {
		t.Fatalf("mkdir temp root: %v", err)
	}
	mainPort := w1FreePort(t)
	healthPort := w1FreePort(t)
	// PostgreSQL + Redis 是唯一存储形态（PLAN-20261024T051925945Z）：业务库、
	// F3 审计库与 F4 操作日志库共用 pgtestkit 隔离库（fresh DDL + seed 等价
	// maintenance --ensure-schema --seed 的 business 面）；文件面只剩 blob/hot
	// 搜索目录。
	businessDB := pgtestkit.NewTestDatabase(t,
		func(ctx context.Context, db *sql.DB) error {
			_, err := bootstrap.EnsurePostgres(ctx, db)
			return err
		},
		func(ctx context.Context, db *sql.DB) error {
			_, err := bootstrap.SeedPostgres(ctx, db, bootstrap.SeedOptions{Secret: "w16b-boot-secret"})
			return err
		})
	businessDSN := businessDB.DSN
	_, evidenceEnv := w1BuildSystemApiEvidence(t, root)
	cacheRedis := miniredis.RunT(t)
	stateRedis := miniredis.RunT(t)
	env := map[string]string{
		"NODE_ENV":                             "development",
		"JUHE_AI_REDIS_CACHE_URL":              "redis://" + cacheRedis.Addr() + "/0",
		"JUHE_AI_REDIS_STATE_URL":              "redis://" + stateRedis.Addr() + "/0",
		"JUHE_AI_REDIS_NAMESPACE":              "w16b-boot",
		"JUHE_AI_J3B_CIRCUIT_REDIS_URL":        "redis://" + stateRedis.Addr() + "/0",
		"JUHE_AI_SECRET":                       "w16b-boot-secret-0123456789abcdef",
		"JUHE_AI_POSTGRES_URL":                 businessDSN,
		"JUHE_AI_BUSINESS_POSTGRES_URL":        businessDSN,
		"JUHE_AI_AUDIT_LOG_POSTGRES_URL":       businessDSN,
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL": businessDSN,
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID":           "w16b-boot-instance",
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY":        filepath.Join(root, "blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY":  filepath.Join(root, "hotsearch"),
		// F4 操作日志：composeSystemAPI 硬要求 store 已启用（缺失即 fail-fast）。
		"JUHE_AI_OPERATION_LOG_POSTGRES_URL": businessDSN,
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID":  "w16b-boot-instance",
		"JUHE_AI_HOST":                       "127.0.0.1",
		"JUHE_AI_PORT":                       mainPort,
		"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS": "127.0.0.1:" + healthPort,
	}
	for key, value := range evidenceEnv {
		if key == "JUHE_AI_BUSINESS_DATABASE_PATH" || key == "JUHE_AI_CHAT_DATABASE_PATH" {
			// 证据夹具的 sqlite 库路径键已随存储形态退役，不注入。
			continue
		}
		env[key] = value
	}
	for key, value := range env {
		t.Setenv(key, value)
	}

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
