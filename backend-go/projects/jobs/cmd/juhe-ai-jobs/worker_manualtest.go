package main

import (
	"context"
	"net/http"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/internalapi"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/manualtest"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/manualtestrepo"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proberepo"
)

// wireManualTestFamily 把手动账号测试族收口为全链路可用：
//   - manualtestrepo（account_test_tasks/_sessions/_session_tasks 双模仓储，
//     opsjobs.ManualTestTaskRepo port）；
//   - manualtest.Executor（draft v1 信封解密 → accountprobe.ManualDiagnostics
//     分级诊断 → result_json 信封写回 → 取消响应）；
//   - opsjobs.ManualTestQueue（start/sweep 维护 + 本地并发队列）；
//   - internalapi loopback 派发/取消回调（gateway 反向调用入口）。
//
// 核心业务表缺失时 fail closed 登记本族 disabled（对齐探针族契约校验模式），
// 派发回调保持 503 不可用语义。诊断并发/超时沿用 accountprobe 约定
// （JUHE_AI_JOBS_PROBE_CONCURRENCY + [10s,20s,30s] / images 120s）。
func (a *workerAssembly) wireManualTestFamily(ctx context.Context) error {
	if !a.config.ManualTestEnabled {
		return nil
	}
	business, err := openBusinessDB(a, "manual-test-family-business")
	if err != nil {
		return err
	}
	repo, err := manualtestrepo.New(manualtestrepo.Config{
		DB:       business.db,
		Postgres: business.postgres,
	})
	if err != nil {
		_ = business.close()
		return err
	}
	a.addCloser(business.close)
	if err := repo.ValidateCoreTables(ctx); err != nil {
		a.registerManualTestFamilyDisabled("业务库契约校验失败：" + err.Error())
		_ = business.close()
		return nil
	}

	savedStore, err := proberepo.NewStore(proberepo.Config{
		DB:       business.db,
		Postgres: business.postgres,
		Secret:   a.config.Secret,
	})
	if err != nil {
		return err
	}
	probeService, err := accountprobe.NewService(accountprobe.Options{
		Source:      savedStore,
		Client:      &http.Client{},
		Secret:      a.config.Secret,
		Concurrency: a.config.ProbeConcurrency,
	})
	if err != nil {
		return err
	}
	executor, err := manualtest.NewExecutor(manualtest.ExecutorOptions{
		Probe:         probeService,
		SavedAccounts: savedStore,
		Secret:        a.config.Secret,
	})
	if err != nil {
		return err
	}
	queue, err := opsjobs.NewManualTestQueue(repo, executor.Execute, opsjobs.ManualTestQueueConfig{
		RefillMaxBatchSize:   a.config.ManualTestRefillMaxBatchSize,
		QueuedMaxWaitMS:      a.config.ManualTestQueuedMaxWaitMS,
		RunningStaleMS:       a.config.ManualTestRunningStaleMS,
		QueuedSweepBatchSize: a.config.ManualTestQueuedSweepBatchSize,
		Concurrency:          a.config.ProbeConcurrency,
		NowMS:                func() int64 { return time.Now().UnixMilli() },
	})
	if err != nil {
		return err
	}
	a.manualTestQueue = queue
	a.addCloser(func() error {
		// Stop 依赖 sweep 循环启动后的 sweepDone；启动维护失败或组件未运行时
		// 该通道不会关闭，必须带超时上限（对齐 drain 排空约定）。
		stopCtx, cancel := context.WithTimeout(context.Background(), a.config.DrainTimeout)
		defer cancel()
		queue.Stop(stopCtx)
		return nil
	})
	return nil
}

// registerManualTestFamilyDisabled 登记手动测试族 disabled（派发回调保持 503）。
func (a *workerAssembly) registerManualTestFamilyDisabled(reason string) {
	a.registerDisabledJob("background_worker_account_test_tasks", reason)
	a.registerDisabledJob("background_worker_account_test_cancel", "同手动测试族失败："+reason)
	a.registerDisabledJob("manual-account-test-queue", reason)
}

// manualTestQueueComponent 返回手动测试队列的长驻组件：Start 启动维护 +
// sweep 循环，Run 消费本地队列直到 ctx 取消或 Stop。停机取消不算组件错误
// （对齐 worker scheduler 组件的退出语义）。
func manualTestQueueComponent(queue *opsjobs.ManualTestQueue, stopTimeout time.Duration, logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}) (name string, run func(runCtx context.Context) error) {
	return "manual account test queue", func(runCtx context.Context) error {
		// Node startAccountTestTaskQueue：启动维护失败仅告警，sweep/refill 会
		// 重试；不阻塞队列运行。
		if resumed, err := queue.Start(runCtx); err != nil {
			logger.Warn("账号测试队列启动维护失败", "error", err.Error())
		} else if len(resumed) > 0 {
			logger.Info("账号测试队列恢复中断任务", "count", len(resumed))
		}
		runErr := queue.Run(runCtx)
		stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		defer cancel()
		queue.Stop(stopCtx)
		if runCtx.Err() != nil {
			return nil
		}
		return runErr
	}
}

// manualTestDispatchOptions 组装 internalapi 派发/取消回调；队列未接线时
// Dispatch 恒返 false（503 服务暂不可用，任务留在 queued 由 queued-max-wait
// sweep 收口），Cancel 恒返 false（同语义）。
func (a *workerAssembly) manualTestDispatchOptions(secret string) internalapi.AccountTestDispatchRouterOptions {
	options := internalapi.AccountTestDispatchRouterOptions{Secret: secret}
	if a.manualTestQueue == nil {
		options.Dispatch = func(context.Context, string) (bool, error) { return false, nil }
		return options
	}
	queue := a.manualTestQueue
	options.Dispatch = func(ctx context.Context, taskID string) (bool, error) {
		return queue.DispatchAccountTestTask(ctx, taskID)
	}
	options.Cancel = func(ctx context.Context, taskID string) bool {
		normalized, ok := opsjobs.NormalizeTaskID(taskID)
		if !ok {
			return false
		}
		queue.CancelLocal(ctx, normalized, "已停止测试")
		return true
	}
	return options
}
