// 波次 w16d：批次四。main() 深层装配臂的子进程 fail-fast 批测（复用 w13g8
// 子进程协议：子进程以隔离 flag 驱动生产 main()，fail() os.Exit 时经
// GOCOVERDIR 合并覆盖计数）。只驱动早失败/守卫/PG 连接错误臂，不依赖共享库
// 形状（除个别标注 PG 门控的臂外，连接一律指向不可达端口，装配在 Ping/
// CheckSchema 处失败）。凭据不落日志。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/taskruns"
)

// w16dClosedPortURL 返回一个格式合法但端口不可达的 postgres 连接串。
func w16dClosedPortURL() string {
	return "postgres://w16d:w16d@127.0.0.1:1/w16d_unreachable?sslmode=disable"
}

func TestW16DMainFailFastArms(t *testing.T) {
	if testing.Short() {
		t.Skip("fail-fast 子进程场景在 -short 下跳过")
	}
	if w13g8FindGocoverdir() == "" {
		t.Skip("w16d fail-fast 子进程计数需要 -test.gocoverdir（go test -coverprofile 时生效）")
	}
	j2Base := map[string]string{
		"JUHE_AI_ACCOUNT_BALANCE_ENABLED":           "true",
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER":        "go",
		"JUHE_AI_ACCOUNT_BALANCE_OWNER_ID":          "w16d-j2",
		"JUHE_AI_ACCOUNT_BALANCE_STORE":             "postgres",
		"JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET": "0123456789abcdef0123456789abcdef",
	}
	j3aBase := map[string]string{
		"JUHE_AI_PROXY_LATENCY_ENABLED":             "true",
		"JUHE_AI_PROXY_LATENCY_JOBS_OWNER":          "go",
		"JUHE_AI_PROXY_LATENCY_INSTANCE_ID":         "w16d-j3a",
		"JUHE_AI_PROXY_LATENCY_STORE":               "postgres",
		"JUHE_AI_PROXY_LATENCY_CREDENTIAL_SECRET":   "0123456789abcdef0123456789abcdef",
		"JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_URL":  w16dClosedPortURL(),
		"JUHE_AI_PROXY_LATENCY_RESULT_POSTGRES_URL": w16dClosedPortURL(),
	}
	scenarios := []struct {
		name       string
		args       []string
		envBuilder func(*testing.T) map[string]string
		patch      map[string]string
	}{
		// J3a 管理接口守卫：management 配置合法但 J3a owner 未启用（255-257）。
		{"j3a-management-guard-valid", nil, w13g8FullOwnerEnv, map[string]string{
			"JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED":        "true",
			"JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_URL":   w16dClosedPortURL(),
			"JUHE_AI_PROXY_LATENCY_MANAGEMENT_LISTEN_ADDRESS": "127.0.0.1:0",
			"JUHE_AI_PROXY_LATENCY_ENABLED":                   "",
		}},
		// model-recovery：Redis URL 合法但不可达 → Ping 失败（218-224）。
		{"model-recovery-redis-closed-port", nil, w13g8FullOwnerEnv, map[string]string{
			"JUHE_AI_REDIS_STATE_URL": "redis://127.0.0.1:1/9",
			"JUHE_AI_REDIS_NAMESPACE": "juhe-ai:w16d",
		}},
		// J1 store=postgres 但连接不可达 → OpenStore/Ping 失败（157-159）。
		{"j1-pg-store-closed-port", nil, w13g8FullOwnerEnv, map[string]string{
			"JUHE_AI_ACCOUNT_HEALTH_ENABLED":      "true",
			"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":   "go",
			"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":  "w16d-j1",
			"JUHE_AI_ACCOUNT_HEALTH_STORE":        "postgres",
			"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL": w16dClosedPortURL(),
		}},
		// J1 input=postgres 不可达 → 直连 Ping 失败（169-183）。J1 store 保持
		// 基座 SQLite 形状（配 input 目录），只切换 input 源。
		{"j1-input-pg-closed-port", nil, w13g8FullOwnerEnv, map[string]string{
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE":                  "postgres",
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL":            w16dClosedPortURL(),
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_MAX_OPEN_CONNS": "4",
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_MAX_IDLE_CONNS": "2",
			"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET":             "0123456789abcdef0123456789abcdef",
		}},
		// J2 store=postgres 连接串非法 → Acquire 失败（232-236）。
		{"j2-pg-bad-url", nil, w13g8FullOwnerEnv, mergeEnv(j2Base, map[string]string{
			"JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL": "pgx://w16d-invalid-url",
		})},
		// J2 input 连接串非法 → InputPostgresPool Acquire 失败（237-241）。
		{"j2-input-bad-url", nil, w13g8FullOwnerEnv, mergeEnv(j2Base, map[string]string{
			"JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL":       w16dClosedPortURL(),
			"JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL": "pgx://w16d-invalid-url",
		})},
		// J3a store 不可达 → CheckSchema 失败（279-282）。
		{"j3a-store-closed-port", nil, w13g8FullOwnerEnv, mergeEnv(j3aBase, map[string]string{
			"JUHE_AI_PROXY_LATENCY_POSTGRES_URL": w16dClosedPortURL(),
		})},
		// J3a input 连接串非法 → Acquire 失败（283-287）。
		{"j3a-input-bad-url", nil, w13g8FullOwnerEnv, mergeEnv(j3aBase, map[string]string{
			"JUHE_AI_PROXY_LATENCY_POSTGRES_URL":       w16dClosedPortURL(),
			"JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_URL": "pgx://w16d-invalid-url",
		})},
		// gometrics：库文件存在但无 schema → EnsureReady 失败（469-472）。
		{"gometrics-ensure-ready-fail", nil, w13g8GarbageRuntimeEnv, nil},
		// migrate 分发 + 运行日志库不可写 → OpenStore 失败（1007-1009）。
		{"migrate-open-store-fail", []string{"-migrate-runtime-log-legacy-sqlite"}, w13g8FullOwnerEnv, map[string]string{
			"JUHE_AI_RUNTIME_LOG_DATABASE_PATH": ".",
		}},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			env := scenario.envBuilder(t)
			for key, value := range scenario.patch {
				env[key] = value
			}
			code := w16dSpawnFailFastChild(t, env, scenario.args)
			if code != 1 {
				t.Fatalf("场景 %s 退出码=%d want 1", scenario.name, code)
			}
		})
	}
}

// w16dSpawnFailFastChild 拉起 fail-fast 子进程并返回退出码。与 w13g8 助手
// 的差别：额外设置 GOCOVERDIR 环境变量——fail() 走 os.Exit，只有 runtime
// 覆盖钩子（依赖 GOCOVERDIR env）会在退出时写出累计计数；-test.gocoverdir
// 标志由 testing 框架在 m.Run() 之后消费，os.Exit 路径不会触发。
func w16dSpawnFailFastChild(t *testing.T, env map[string]string, args []string) int {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("定位测试二进制失败: %v", err)
	}
	gocoverdir := w13g8FindGocoverdir()
	childArgs := []string{"-test.run=^TestW16DMainFailFastArms$", "-test.timeout=120s"}
	if gocoverdir != "" {
		childArgs = append(childArgs, "-test.gocoverdir="+gocoverdir)
	}
	argsJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, childArgs...)
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Env = append(cmd.Env,
		w13g8ChildMode+"=1",
		w13g8ChildArgs+"="+string(argsJSON))
	if gocoverdir != "" {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+gocoverdir)
	}
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动子进程失败: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return 0
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		t.Fatalf("子进程异常退出: %v\n%s", err, output.String())
		return -1
	case <-time.After(90 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("子进程未在限期内退出（场景未命中 fail 分支）:\n%s", output.String())
		return -1
	}
}

func mergeEnv(base, extra map[string]string) map[string]string {
	merged := map[string]string{}
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

// TestW16DMainProjectionWarnArm 驱动「J1 开启 + worker 关闭 + 投影 env 非法」
// 的 minimal assembly 降级臂：装配 warn 不阻塞，进程正常服务后干净停机
// （434-437 与 minimal assembly 分支）。
func TestW16DMainProjectionWarnArm(t *testing.T) {
	if testing.Short() {
		t.Skip("main e2e skipped in -short mode")
	}
	root := t.TempDir()
	env := wgOwnerModeMainEnv(t, root)
	delete(env, "JUHE_AI_JOBS_WORKER_ENABLED")
	env["JUHE_AI_ACCOUNT_HEALTH_JOBS_PROJECTION_POLL_MS"] = "1"
	port := wgFreePort(t)
	cmd := wgSpawnMainChild(t, env, "w16d-projection-warn", port)
	wgPollHealthPayload(t, port, wgChildReadyTimeout, func(current map[string]any) bool {
		_, hasOwnerMode := current["ownerMode"]
		_, hasAccountHealth := current["accountHealthEnabled"]
		return hasOwnerMode && hasAccountHealth
	})
	wgInterruptAndWait(t, cmd)
}

// ---- PG 门控臂 ----

// TestW16DPGWithLeaseArms 覆盖 withLease 的 postgres 租约包裹分支（成功、
// 他人持锁 skip）。
func TestW16DPGWithLeaseArms(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门控臂 skipped in -short mode")
	}
	pgURL := w12cPGOverrideURL(t)
	if pgURL == "" {
		t.Skip("w16d: 覆盖库连接串不可用")
	}
	store, err := taskruns.OpenStore(taskruns.StoreConfig{
		Mode:                 taskruns.ModePostgres,
		PostgresURL:          pgURL,
		PostgresMaxOpenConns: 4,
		PostgresMaxIdleConns: 2,
	})
	if err != nil {
		t.Skipf("w16d: 打开 taskruns PG 存储失败（跳过）: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Skipf("w16d: taskruns PG schema 初始化失败（跳过）: %v", err)
	}
	assembly := newWorkerAssembly(workerConfig{
		Enabled: true, Driver: "postgres", InstanceID: "w16d-lease",
	}, slog.Default())
	assembly.taskRunsStore = store
	// 唯一 jobName：共享覆盖库上他人（历史 e2e 子进程）可能持有同名未过期
	// 调度租约；隔离键空间保证本测试的 busy/skip 判定确定。
	leaseJob := fmt.Sprintf("w16d-lease-job-%d", time.Now().UnixNano())
	invoked := false
	wrapped := assembly.withLease(leaseJob, time.Minute, func(context.Context, jobsched.TaskContext) (jobsched.TaskResult, error) {
		invoked = true
		return jobsched.TaskResult{}, nil
	})
	// 成功路径：租约获取 → 任务执行 → OutcomeSuccess（802-814、823-829）。
	result, err := wrapped(context.Background(), jobsched.TaskContext{})
	if err != nil {
		t.Fatalf("PG 租约包裹执行: %v", err)
	}
	if !invoked {
		t.Fatal("无竞争时内层任务必须执行")
	}
	if result.Outcome != "" && result.Outcome != jobsched.OutcomeSuccess {
		t.Fatalf("成功路径 outcome: %+v", result)
	}
	// 他人持锁 → OutcomeSkipped（819-822）。
	other := fmt.Sprintf("w16d-other-%d", time.Now().UnixNano())
	acquired, err := store.TryAcquireScheduledLease(context.Background(), taskruns.ScheduledLeaseAcquireInput{
		JobName: leaseJob,
		OwnerID: other,
		RunID:   "w16d-held-run",
		TTL:     time.Minute,
	})
	if err != nil || !acquired.Acquired {
		t.Fatalf("预占调度租约: acquired=%v err=%v", acquired.Acquired, err)
	}
	invoked = false
	skipped, err := wrapped(context.Background(), jobsched.TaskContext{})
	if err != nil {
		t.Fatalf("持锁包裹执行: %v", err)
	}
	if invoked {
		t.Fatal("他人持锁时内层任务不得执行")
	}
	if skipped.Outcome != jobsched.OutcomeSkipped {
		t.Fatalf("持锁必须 OutcomeSkipped: %+v", skipped)
	}
}
