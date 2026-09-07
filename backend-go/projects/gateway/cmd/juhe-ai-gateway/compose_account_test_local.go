package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/manualtest"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/manualtestrepo"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
)

// gatewayAccountTestExecutor 是手动账号测试执行链的进程内装配（去跨进程战役：
// 原 jobsAccountTestDispatchBridge HTTP 桥 + jobs internalapi 派发路由整体删除）。
// 组合根在 gateway 进程内装配共享 backend-go-platform/accounttest 执行链：
//
//	manualtestrepo.Repo（account_test_tasks/_sessions/_session_tasks 双模仓储）
//	→ proberepo.Store（保存账户视图解析）→ accountprobe.Service（分级诊断）
//	→ manualtest.Executor（draft v1 信封解密 → ManualDiagnostics → result_json）
//	→ accounttest.ManualTestQueue（start/sweep 维护 + 本地并发队列，单持有者）。
//
// jobs 不再运行该队列，sweep 从此只有 gateway 一个持有者；会话/任务表读写
// 分工不变（gateway 路由写创建/取消/失败，执行方写 running/结果）。
// 诊断并发/超时沿用原 jobs 约定（JUHE_AI_JOBS_PROBE_CONCURRENCY 默认 512 +
// [10s,20s,30s] / images 120s）。

// gatewayTestQueueEnv 是手动测试队列的 env 旋钮（同名 env、默认值与边界对齐
// 原 jobs loadWorkerConfig；队列执行权移交 gateway 后由 gateway 读取）。
type gatewayTestQueueEnv struct {
	ProbeConcurrency     int
	RefillMaxBatchSize   int
	QueuedSweepBatchSize int
	QueuedMaxWaitMS      int64
	RunningStaleMS       int64
}

// 队列旋钮 env 名（同名 env 对齐原 jobs 约定）。
const (
	envProbeConcurrency     = "JUHE_AI_JOBS_PROBE_CONCURRENCY"
	envRefillMaxBatchSize   = "JUHE_AI_BACKGROUND_ACCOUNT_TEST_REFILL_MAX_BATCH_SIZE"
	envQueuedSweepBatchSize = "JUHE_AI_BACKGROUND_ACCOUNT_TEST_QUEUED_SWEEP_BATCH_SIZE"
	envQueuedMaxWaitMS      = "JUHE_AI_BACKGROUND_ACCOUNT_TEST_QUEUED_MAX_WAIT_MS"
	envRunningStaleMS       = "JUHE_AI_BACKGROUND_ACCOUNT_TEST_RUNNING_STALE_MS"
)

// loadGatewayTestQueueEnv 读取队列旋钮；非法取值 fail closed（对齐原 jobs
// workerEnvInt 语义：配置错误不允许静默降级）。
func loadGatewayTestQueueEnv(getenv func(string) string) (gatewayTestQueueEnv, error) {
	env := gatewayTestQueueEnv{
		ProbeConcurrency:     512,
		RefillMaxBatchSize:   1_000,
		QueuedSweepBatchSize: 500,
		QueuedMaxWaitMS:      10 * 60_000,
		RunningStaleMS:       10 * 60_000,
	}
	envInt := func(name string, fallback int64, lower, upper int64) (int64, error) {
		value := strings.TrimSpace(getenv(name))
		if value == "" {
			return fallback, nil
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s 必须是整数", name)
		}
		if parsed < lower || parsed > upper {
			return 0, fmt.Errorf("%s 必须介于 %d 和 %d 之间", name, lower, upper)
		}
		return parsed, nil
	}
	probeConcurrency, err := envInt(envProbeConcurrency, int64(env.ProbeConcurrency), 1, 5096)
	if err != nil {
		return env, err
	}
	env.ProbeConcurrency = int(probeConcurrency)
	refill, err := envInt(envRefillMaxBatchSize, int64(env.RefillMaxBatchSize), 1, 100_000)
	if err != nil {
		return env, err
	}
	env.RefillMaxBatchSize = int(refill)
	sweep, err := envInt(envQueuedSweepBatchSize, int64(env.QueuedSweepBatchSize), 1, 100_000)
	if err != nil {
		return env, err
	}
	env.QueuedSweepBatchSize = int(sweep)
	queuedMaxWaitMS, err := envInt(envQueuedMaxWaitMS, env.QueuedMaxWaitMS, 1_000, 24*60*60_000)
	if err != nil {
		return env, err
	}
	env.QueuedMaxWaitMS = queuedMaxWaitMS
	runningStaleMS, err := envInt(envRunningStaleMS, env.RunningStaleMS, 60_000, 60*60_000)
	if err != nil {
		return env, err
	}
	env.RunningStaleMS = runningStaleMS
	return env, nil
}

// gatewayAccountTestDispatch 是 accounts.TestDispatchEffects 的进程内适配器。
type gatewayAccountTestDispatch struct {
	queue  *accounttest.ManualTestQueue
	logger *slog.Logger
}

// DispatchAccountTestTasks 逐任务本地入队。ctx 参数只满足端口签名；入队是
// 纯内存操作，绝不把请求 ctx 传给执行链（执行使用队列自身生命周期 ctx）。
// 原 quirk 修正：EnqueueLocal 返回 false（任务已在本进程 pending/running）按
// 幂等成功处理并记日志，不得把 running 任务置败；路由 503 分支仅在队列已
// 停止（停机中拒收）时保留。
func (d *gatewayAccountTestDispatch) DispatchAccountTestTasks(ctx context.Context, taskIDs []string) bool {
	_ = ctx
	if d.queue.Stopped() {
		return false
	}
	for _, taskID := range taskIDs {
		normalized, ok := accounttest.NormalizeTaskID(taskID)
		if !ok {
			continue
		}
		if !d.queue.EnqueueLocal(normalized) {
			slog.Warn("账号测试任务已在本进程队列中，重复派发按幂等成功处理",
				"event", "account_test_dispatch_idempotent", "taskId", normalized)
		}
	}
	return true
}

// DispatchAccountTestCancel 进程内取消：中止本地在跑任务或移除 pending；
// DB 取消写使用独立超时 ctx（fire-and-forget，与原 loopback 桥的尽力而为
// 语义一致，不阻塞取消响应）。
func (d *gatewayAccountTestDispatch) DispatchAccountTestCancel(taskID string) {
	normalized, ok := accounttest.NormalizeTaskID(taskID)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d.queue.CancelLocal(ctx, normalized, "已停止测试")
}

// wireInProcessAccountTestDispatch 装配进程内执行链并把适配器接线到
// accounts.Store。业务库契约表缺失时按 nil 端口降级（路由保持 Node
// worker-unavailable 契约：任务置败 + 503），不阻塞组合根。
func wireInProcessAccountTestDispatch(composed *composition, cfg runtimeConfig, accountStore *accounts.Store) error {
	env, err := loadGatewayTestQueueEnv(os.Getenv)
	if err != nil {
		return err
	}
	repo, err := manualtestrepo.New(manualtestrepo.Config{
		DB:       composed.db,
		Postgres: composed.pgDialect,
	})
	if err != nil {
		return err
	}
	if err := repo.ValidateCoreTables(context.Background()); err != nil {
		slog.Warn("手动账号测试契约表缺失，进程内测试队列不装配（路由保持 503 契约）",
			"event", "account_test_queue_tables_missing", "error", err.Error())
		return nil
	}
	savedStore, err := proberepo.NewStore(proberepo.Config{
		DB:       composed.db,
		Postgres: composed.pgDialect,
		Secret:   cfg.Secret,
	})
	if err != nil {
		return err
	}
	probeService, err := accountprobe.NewService(accountprobe.Options{
		Source:      savedStore,
		Secret:      cfg.Secret,
		Concurrency: env.ProbeConcurrency,
	})
	if err != nil {
		return err
	}
	executor, err := manualtest.NewExecutor(manualtest.ExecutorOptions{
		Probe:         probeService,
		SavedAccounts: savedStore,
		Secret:        cfg.Secret,
	})
	if err != nil {
		return err
	}
	queue, err := accounttest.NewManualTestQueue(repo, executor.Execute, accounttest.ManualTestQueueConfig{
		RefillMaxBatchSize:   env.RefillMaxBatchSize,
		QueuedMaxWaitMS:      env.QueuedMaxWaitMS,
		RunningStaleMS:       env.RunningStaleMS,
		QueuedSweepBatchSize: env.QueuedSweepBatchSize,
		Concurrency:          env.ProbeConcurrency,
		NowMS:                func() int64 { return time.Now().UnixMilli() },
	})
	if err != nil {
		return err
	}

	// 启动维护失败仅告警（sweep/refill 会重试；对齐原 jobs 队列组件语义），
	// 随后启动 sweep 循环与本地队列消费循环（Run 的 ctx 只随停机取消）。
	if resumed, err := queue.Start(context.Background()); err != nil {
		slog.Warn("账号测试队列启动维护失败", "event", "account_test_queue_start_maintenance_failed", "error", err.Error())
	} else if len(resumed) > 0 {
		slog.Info("账号测试队列恢复中断任务", "event", "account_test_queue_resumed", "count", len(resumed))
	}
	queueCtx, stopQueue := context.WithCancel(context.Background())
	go func() { _ = queue.Run(queueCtx) }()
	composed.shutdowns = append(composed.shutdowns, func() {
		stopQueue()
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		queue.Stop(stopCtx)
	})

	accountStore.SetTestDispatchEffects(&gatewayAccountTestDispatch{queue: queue})
	return nil
}
