// 波次 w16h：run() 抽取后的进程内臂覆盖。main() 收敛为 os.Exit(run(...)) 后，
// 原 fail() 出口改为返回码，boot/停机序列在测试进程内可直接驱动：错误臂用
// 坏 env/垃圾 SQLite 文件/非法监听地址注入，优雅停机臂用 hooksSignalNotifyContext
// 注入可编程 ctx（等价 SIGTERM，无需真实信号），J1 sqlite 直读的「布局前置
// 成功 → 只读 ping 失败」臂用 hooksSQLiteDirectInputLayoutEnsured 在窗口内
// 破坏文件。stderr 错误文案断言锁定原 fail() 逐字节行为。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// w16hApplyEnv 把 env 逐键 t.Setenv（测试结束自动恢复）。
func w16hApplyEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for key, value := range env {
		t.Setenv(key, value)
	}
}

// w16hInjectCancelSignal 覆写 hooksSignalNotifyContext 为可编程取消 ctx：
// run/runPassiveJobs/runRuntimeLegacyMigration 的停机信号源在测试内可控。
func w16hInjectCancelSignal(t *testing.T) context.CancelFunc {
	t.Helper()
	original := hooksSignalNotifyContext
	ctx, cancel := context.WithCancel(context.Background())
	hooksSignalNotifyContext = func(parent context.Context, signals ...os.Signal) (context.Context, context.CancelFunc) {
		return ctx, cancel
	}
	t.Cleanup(func() {
		hooksSignalNotifyContext = original
		cancel()
	})
	return cancel
}

// w16hRunArms 统一断言：返回码 + stderr 关键片段（锁定原 fail 文案）。
// 注入可编程停机 ctx：若被测臂意外走到 supervisor 循环，立即收敛为 rc=0
// 断言失败可见，而不是挂住测试进程。
func w16hRunArms(t *testing.T, args []string, wantCode int, stderrContains string) (string, string) {
	t.Helper()
	w16hInjectCancelSignal(t)
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	if code != wantCode {
		t.Fatalf("run(%v) 退出码必须为 %d，得到 %d；stderr=%q", args, wantCode, code, stderr.String())
	}
	if stderrContains != "" && !bytes.Contains([]byte(stderr.String()), []byte(stderrContains)) {
		t.Fatalf("stderr 必须包含 %q，得到 %q", stderrContains, stderr.String())
	}
	return stdout.String(), stderr.String()
}

// TestW16HRunFlagArms 覆盖 run() 头部 flag 臂：未知 flag（rc=2）、-h（rc=0）、
// 位置参数（rc=2）、--run-jobs-once 与迁移互斥（rc=2）。FlagSet 的错误与 usage
// 输出目标是全局 os.Stderr（与原全局 flag.Parse 行为逐字节一致），用 pipe 捕获。
func TestW16HRunFlagArms(t *testing.T) {
	captureOSStderr := func(t *testing.T, invoke func()) string {
		t.Helper()
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		original := os.Stderr
		os.Stderr = writer
		invoke()
		os.Stderr = original
		_ = writer.Close()
		output := make([]byte, 4096)
		read, _ := reader.Read(output)
		_ = reader.Close()
		return string(output[:read])
	}
	t.Run("未知 flag 返回 2", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"-w16h-no-such-flag"}, &stdout, &stderr)
		if code != 2 {
			t.Fatalf("未知 flag 必须返回 2，得到 %d", code)
		}
	})
	t.Run("未知 flag 错误行进全局 stderr", func(t *testing.T) {
		dumped := captureOSStderr(t, func() {
			var stdout, stderr bytes.Buffer
			_ = run([]string{"-w16h-no-such-flag"}, &stdout, &stderr)
		})
		if !bytes.Contains([]byte(dumped), []byte("flag provided but not defined: -w16h-no-such-flag")) {
			t.Fatalf("全局 stderr 必须包含 flag 错误行，得到 %q", dumped)
		}
	})
	t.Run("-h 返回 0 并输出 usage", func(t *testing.T) {
		dumped := captureOSStderr(t, func() {
			var stdout, stderr bytes.Buffer
			if code := run([]string{"-h"}, &stdout, &stderr); code != 0 {
				t.Errorf("-h 必须返回 0，得到 %d", code)
			}
		})
		if !bytes.Contains([]byte(dumped), []byte("Usage of")) {
			t.Fatalf("-h 必须输出 usage 到全局 stderr，得到 %q", dumped)
		}
	})
	t.Run("位置参数返回 2", func(t *testing.T) {
		_, stderr := w16hRunArms(t, []string{"w16h-extra-arg"}, 2, "unsupported jobs arguments")
		if want := "unsupported jobs arguments: [w16h-extra-arg]"; stderr != want+"\n" {
			t.Fatalf("stderr 逐字节断言失败：want %q got %q", want+"\n", stderr)
		}
	})
}

// TestW16HRunStartupFailArms 覆盖 logger/owner/F1 配置链错误臂。
func TestW16HRunStartupFailArms(t *testing.T) {
	t.Run("非法日志级别返回 1", func(t *testing.T) {
		w16hApplyEnv(t, map[string]string{"JUHE_AI_LOG_LEVEL": "w16h-not-a-level"})
		w16hRunArms(t, nil, 1, "JUHE_AI_LOG_LEVEL")
	})
	t.Run("非法 owner 模式返回 1", func(t *testing.T) {
		w16hApplyEnv(t, map[string]string{"JUHE_AI_BLUE_GREEN_OWNER_MODE": "w16h-garbage"})
		w16hRunArms(t, nil, 1, "must be active, standby, or drain")
	})
	t.Run("standby 监听地址非法返回 1", func(t *testing.T) {
		w16hApplyEnv(t, map[string]string{"JUHE_AI_BLUE_GREEN_OWNER_MODE": "standby"})
		w16hRunArms(t, []string{"-health-listen-address=10.0.0.8:3305"}, 1,
			"listen passive jobs health endpoint")
	})
	t.Run("空 env 缺 PG 连接串配置装载失败返回 1", func(t *testing.T) {
		// PG 唯一形态：零配置不再可启动（sqlite 自动派生已退役），JUHE_AI_POSTGRES_URL
		// 必填 fail-fast。DATA_DIR 指向自管理目录，避免在包目录创建 ./data。
		root, mkdirErr := os.MkdirTemp("", "w16h-empty-env-")
		if mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		t.Cleanup(func() { _ = os.RemoveAll(root) })
		w16hApplyEnv(t, map[string]string{"JUHE_AI_DATA_DIR": root})
		w16hRunArms(t, nil, 1, "JUHE_AI_RUNTIME_LOG_POSTGRES_URL")
	})
}

// TestW16HRunPassiveGracefulShutdown 进程内覆盖 runPassiveJobs 全链：
// standby 模式健康端点服务 → 注入取消 → 优雅停机返回 0。
func TestW16HRunPassiveGracefulShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("passive 停机链路 skipped in -short mode")
	}
	w16hApplyEnv(t, map[string]string{"JUHE_AI_BLUE_GREEN_OWNER_MODE": "standby"})
	cancel := w16hInjectCancelSignal(t)
	port := wgFreePort(t)
	runDone := make(chan int, 1)
	go func() {
		runDone <- run([]string{"-health-listen-address=127.0.0.1:" + itoa(port)}, io.Discard, io.Discard)
	}()
	wgPollHealthPayload(t, port, 60*time.Second, func(payload map[string]any) bool {
		ownerReady, _ := payload["ownerReady"].(bool)
		return ownerReady == false
	})
	response, err := http.Get("http://127.0.0.1:" + itoa(port) + "/health")
	if err == nil {
		_ = response.Body.Close()
	}
	cancel()
	select {
	case code := <-runDone:
		if code != 0 {
			t.Fatalf("passive 优雅停机后 run 必须返回 0，得到 %d", code)
		}
	case <-time.After(wgChildGracefulTimeout):
		t.Fatal("passive run 未在限期内优雅返回")
	}
}

// TestW16HRunFlagOutputs 进程内断言 --version 与 --check-boundary 的 stdout
// 输出与退出码（run 抽取后 stdout 经参数注入）。
func TestW16HRunFlagOutputs(t *testing.T) {
	t.Run("--version", func(t *testing.T) {
		stdout, _ := w16hRunArms(t, []string{"--version"}, 0, "")
		want := "juhe-ai-jobs project=" + string(contracts.ProjectJobs) + " contract=" + contracts.ArchitectureVersion + "\n"
		if stdout != want {
			t.Fatalf("--version stdout 逐字节断言失败：want %q got %q", want, stdout)
		}
	})
	t.Run("--check-boundary", func(t *testing.T) {
		stdout, _ := w16hRunArms(t, []string{"--check-boundary"}, 0, "")
		want := "juhe-ai-jobs boundary=ready runtime=worker-owner\n"
		if stdout != want {
			t.Fatalf("--check-boundary stdout 逐字节断言失败：want %q got %q", want, stdout)
		}
	})
}

func w16hRunUntilReadyAndCancel(t *testing.T, env map[string]string, ready func(map[string]any) bool, label string, onReady ...func(port int)) {
	t.Helper()
	w16hApplyEnv(t, env)
	cancel := w16hInjectCancelSignal(t)
	port := wgFreePort(t)
	var stderr bytes.Buffer
	runDone := make(chan int, 1)
	go func() {
		runDone <- run([]string{"-health-listen-address=127.0.0.1:" + itoa(port)}, io.Discard, &stderr)
	}()
	ready2 := false
	var lastPayload map[string]any
	// 全包负载下 F1 lease 与 table-monitor 首轮可能显著变慢：supervisor 对
	// 首轮失败的组件按 1s→30s 抖动重试，重载机器上 F1 偶发需多轮重试才能
	// 持有 lease。就绪窗口取 240s（spawn 常量 60s 的四倍，120s 曾在负载下
	// 不足）。
	deadline := time.Now().Add(240 * time.Second)
	for time.Now().Before(deadline) && !ready2 {
		select {
		case code := <-runDone:
			t.Fatalf("%s：run 在就绪前退出（码=%d）stderr=%s", label, code, stderr.String())
			return
		default:
		}
		response, err := http.Get("http://127.0.0.1:" + itoa(port) + "/health")
		if err == nil {
			var payload map[string]any
			decodeErr := json.NewDecoder(response.Body).Decode(&payload)
			_ = response.Body.Close()
			if decodeErr == nil {
				lastPayload = payload
				if ready(payload) {
					ready2 = true
				}
			}
		}
		time.Sleep(wgHealthPollInterval)
	}
	if !ready2 {
		t.Fatalf("%s：健康端点未在限期内就绪，最后载荷=%v stderr=%s", label, lastPayload, stderr.String())
	}
	if len(onReady) > 0 && onReady[0] != nil {
		onReady[0](port)
	}
	cancel()
	select {
	case code := <-runDone:
		if code != 0 {
			t.Fatalf("%s：优雅停机后 run 必须返回 0，得到 %d", label, code)
		}
	case <-time.After(wgChildGracefulTimeout):
		t.Fatal(label + "：run 未在限期内优雅返回")
	}
}

// itoa 供测试内拼接端口号。
func itoa(value int) string {
	return strconv.Itoa(value)
}
