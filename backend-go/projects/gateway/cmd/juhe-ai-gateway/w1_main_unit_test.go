package main

// w1（单元层）：main.go 的纯参数可测元素——flag 早退分支（--version /
// --check-boundary，进程内直接调用 main）、回环监听校验、被动网关健康
// 响应、环境变量助手与会话保留配置解析。进程级 boot 覆盖（完整启动 /
// 信号退出）按测试分层规则属于业务层，见 w1_main_boot_test.go。

import (
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

// w1CallMain 在进程内以受控 os.Args 调用 main()，返回期间捕获的 stdout。
// 每次调用前重置 flag.CommandLine，规避 flag 重复注册 panic；仅限无副作用
// 且正常 return 的早退分支（version / check-boundary）。
func w1CallMain(t *testing.T, arguments ...string) string {
	t.Helper()
	oldArgs := os.Args
	oldCommandLine := flag.CommandLine
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe = %v", err)
	}
	os.Args = append([]string{"juhe-ai-gateway"}, arguments...)
	flag.CommandLine = flag.NewFlagSet("juhe-ai-gateway", flag.ContinueOnError)
	oldStdout := os.Stdout
	os.Stdout = writer
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(reader)
		done <- string(data)
	}()
	defer func() {
		os.Stdout = oldStdout
		os.Args = oldArgs
		flag.CommandLine = oldCommandLine
		_ = writer.Close()
		_ = reader.Close()
	}()
	main()
	_ = writer.Close()
	return <-done
}

func TestW1MainEarlyExitBranches(t *testing.T) {
	// --version：契约版本输出后正常 return。
	version := w1CallMain(t, "--version")
	if !strings.Contains(version, "juhe-ai-gateway project=") {
		t.Fatalf("version = %q", version)
	}
	// --check-boundary：边界自检输出。
	boundary := w1CallMain(t, "--check-boundary")
	if !strings.Contains(boundary, "boundary=ready") {
		t.Fatalf("boundary = %q", boundary)
	}
}

func TestW1MainLoopbackValidation(t *testing.T) {
	// 非回环 host / 非法端口 / 缺端口：拒绝。
	if _, err := listenLoopback("192.168.1.5:8080"); err == nil {
		t.Fatal("非回环地址必须拒绝")
	}
	if _, err := listenLoopback("127.0.0.1:0x1"); err == nil {
		t.Fatal("非数字端口必须拒绝")
	}
	if _, err := listenLoopback("127.0.0.1:70000"); err == nil {
		t.Fatal("越界端口必须拒绝")
	}
	if _, err := listenLoopback("127.0.0.1"); err == nil {
		t.Fatal("缺端口必须拒绝")
	}
	// 合法回环地址：先借 :0 探一个空闲端口，再绑定成功并立即关闭。
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe = %v", err)
	}
	freePort := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	listener, err := listenLoopback("127.0.0.1:" + strconv.Itoa(freePort))
	if err != nil || listener == nil {
		t.Fatalf("listen = %v, %v", listener, err)
	}
	_ = listener.Close()
}

func TestW1MainPassiveGatewayHealth(t *testing.T) {
	handler := passiveGatewayHealthHandler(ownermode.Standby)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{`"ready":false`, `"ownerMode":"standby"`, `"auditLogReady":false`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body 缺少 %s: %s", want, body)
		}
	}
	// 非 GET / 非健康路径：404。
	notFound := httptest.NewRecorder()
	handler.ServeHTTP(notFound, httptest.NewRequest(http.MethodPost, "/health", nil))
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("POST status = %d", notFound.Code)
	}
	otherPath := httptest.NewRecorder()
	handler.ServeHTTP(otherPath, httptest.NewRequest(http.MethodGet, "/other", nil))
	if otherPath.Code != http.StatusNotFound {
		t.Fatalf("other path status = %d", otherPath.Code)
	}
}

func TestW1EnvHelpers(t *testing.T) {
	t.Setenv("W1_ENV_PROBE", "值")
	if got := envOrDefault("W1_ENV_PROBE", "回退"); got != "值" {
		t.Fatalf("env = %q", got)
	}
	if got := envOrDefault("W1_ENV_MISSING", "回退"); got != "回退" {
		t.Fatalf("fallback = %q", got)
	}
	t.Setenv("W1_ENV_LIST", " a , b ,, c ")
	if got := commaList(" a , b ,, c "); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("commaList = %v", got)
	}
	_ = os.Getenv("W1_ENV_LIST")
	t.Setenv("W1_ENV_BOOL", "ON")
	if !envBool("W1_ENV_BOOL") {
		t.Fatal("on 必须为真")
	}
	t.Setenv("W1_ENV_BOOL", "0")
	if envBool("W1_ENV_BOOL") {
		t.Fatal("0 必须为假")
	}
	if envBool("W1_ENV_MISSING") {
		t.Fatal("缺失必须为假")
	}
}

func TestW1MainSessionRetentionConfig(t *testing.T) {
	// 缺省：15 分钟 / 10000。
	interval, limit, err := loadSessionRetentionConfig(func(string) string { return "" })
	if err != nil || interval != 15*time.Minute || limit != 10000 {
		t.Fatalf("default = %v, %d, %v", interval, limit, err)
	}
	// 自定义合法值。
	interval, limit, err = loadSessionRetentionConfig(func(key string) string {
		if key == "JUHE_AI_SESSION_RETENTION_INTERVAL" {
			return "1h"
		}
		return "25"
	})
	if err != nil || interval != time.Hour || limit != 25 {
		t.Fatalf("custom = %v, %d, %v", interval, limit, err)
	}
	// 非法值：报错。
	if _, _, err := loadSessionRetentionConfig(func(key string) string {
		if key == "JUHE_AI_SESSION_RETENTION_INTERVAL" {
			return "0"
		}
		return ""
	}); err == nil {
		t.Fatal("非正间隔必须报错")
	}
	if _, _, err := loadSessionRetentionConfig(func(key string) string {
		if key == "JUHE_AI_SESSION_RETENTION_BATCH_SIZE" {
			return "-3"
		}
		return ""
	}); err == nil {
		t.Fatal("非正批大小必须报错")
	}
	// runSessionRetention：配置无效直接报错。
	if err := runSessionRetention(t.Context(), nil, 0, 0, nil); err == nil {
		t.Fatal("无效配置必须报错")
	}
	_ = filepath.Join
	_ = os.TempDir
}
