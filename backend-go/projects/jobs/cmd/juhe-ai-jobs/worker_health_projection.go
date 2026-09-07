package main

// J1 outcome → 业务账户投影面装配（BUG-0174 M-1）。归档行为基线是 Node
// db-service 的独立投影运行时
// （account-health-jobs-outcome-projection-runtime.service.ts：持续轮询
// projectionPollMs + passive jitter，非 J1 runCycle 的一环），Go 侧对应为
// juhe-ai-jobs owner 进程的独立 supervisor 组件（accounthealth.OutcomeProjector）。
//
// 装配边界：
//   - jobs store（juhe_jobs.account_health_outcomes）只读；main 注入既有
//     accountHealthStore，J1 未启用时投影面不存在（归档同样要求
//     accountHealthJobs.owner === 'go'）；
//   - 业务库经 worker 的 openBusinessDB 约定开库（双模）；receipts/cursors
//     表由 maintenance bootstrap 负责（schema 已有），进程不做 DDL；
//   - 默认开启，可用 JUHE_AI_ACCOUNT_HEALTH_JOBS_PROJECTION_DISABLED=true
//     关闭（归档 JUHE_AI_ACCOUNT_HEALTH_JOBS_PROJECTION_ENABLED 默认关闭的
//     语义在本装配反转，见 BUG-0174 M-1 的默认恢复闭环要求）；
//   - 轮询/批量沿用归档 env 名与默认值：POLL_MS 默认 1000（100..60000）、
//     BATCH_SIZE 默认 100（1..1000）。

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
)

const (
	healthProjectionDisabledEnvVar = "JUHE_AI_ACCOUNT_HEALTH_JOBS_PROJECTION_DISABLED"
	healthProjectionPollEnvVar     = "JUHE_AI_ACCOUNT_HEALTH_JOBS_PROJECTION_POLL_MS"
	healthProjectionBatchEnvVar    = "JUHE_AI_ACCOUNT_HEALTH_JOBS_PROJECTION_BATCH_SIZE"
)

// wireHealthOutcomeProjector 装配 J1 outcome 投影器。store 为 nil（J1 未启
// 用）或 env 显式关闭时返回 (nil, nil)（投影面合法缺席，非错误）；装配失败
// 返回错误，由 main 降级 warn（outcome 仅停留 juhe_jobs 审计面，不阻塞启动）。
func (a *workerAssembly) wireHealthOutcomeProjector(getenv func(string) string, store *accounthealth.Store) (*accounthealth.OutcomeProjector, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if store == nil {
		return nil, nil
	}
	if strings.EqualFold(strings.TrimSpace(getenv(healthProjectionDisabledEnvVar)), "true") {
		a.logger.Info("J1 outcome 投影面已通过 env 关闭", "event", "account_health_projection_disabled", "env", healthProjectionDisabledEnvVar)
		return nil, nil
	}
	config, err := accounthealth.LoadConfig(getenv)
	if err != nil {
		return nil, err
	}
	if !config.Enabled {
		return nil, nil
	}
	var stats accounthealth.GroupStatsDirtyMarker
	if a.statsStore != nil {
		stats = a.statsStore
	} else {
		// 归档在 availability 变化投影后标脏 group stats；stats 家族缺席时
		// 装配期显式 warn 一次（运行期投影继续，统计新鲜度窗口退化）。
		a.logger.Warn("J1 投影后的 group stats 脏标记无写入目标（stats 家族未装配）",
			"event", "account_health_projection_stats_marker_missing")
	}
	business, err := openBusinessDB(a, "health-projection")
	if err != nil {
		return nil, err
	}
	handle, handleErr := accounthealth.NewProjectionBusinessDB(business.db, business.postgres)
	if handleErr != nil {
		_ = business.close()
		return nil, handleErr
	}
	poll, pollErr := healthProjectionPollInterval(getenv)
	if pollErr != nil {
		_ = business.close()
		return nil, pollErr
	}
	batch, batchErr := healthProjectionBatchSize(getenv)
	if batchErr != nil {
		_ = business.close()
		return nil, batchErr
	}
	projector, projectorErr := accounthealth.NewOutcomeProjector(store, accounthealth.OutcomeProjectorConfig{
		Business:         handle,
		CredentialSecret: config.CredentialSecret,
		PollInterval:     poll,
		BatchSize:        batch,
		Stats:            stats,
		Logger:           a.logger,
		Now:              config.Now,
	})
	if projectorErr != nil {
		_ = business.close()
		return nil, projectorErr
	}
	// 投影器持有业务库句柄；关闭统一由 worker 组件 Close（closeStores）承担。
	a.addCloser(business.close)
	return projector, nil
}

// healthProjectionPollInterval 解析轮询间隔 env（毫秒；归档默认 1000，边界
// 100..60000）。非法值 fail loud（装配失败降级 warn），不静默回退。
func healthProjectionPollInterval(getenv func(string) string) (time.Duration, error) {
	raw := strings.TrimSpace(getenv(healthProjectionPollEnvVar))
	if raw == "" {
		return accounthealth.DefaultProjectionPollInterval, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 100 || value > 60000 {
		return 0, errors.New(healthProjectionPollEnvVar + " 必须在 100..60000（毫秒）")
	}
	return time.Duration(value) * time.Millisecond, nil
}

// healthProjectionBatchSize 解析单轮 drain 批量 env（归档默认 100，边界
// 1..1000）。
func healthProjectionBatchSize(getenv func(string) string) (int, error) {
	raw := strings.TrimSpace(getenv(healthProjectionBatchEnvVar))
	if raw == "" {
		return accounthealth.DefaultProjectionBatchSize, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 1000 {
		return 0, errors.New(healthProjectionBatchEnvVar + " 必须在 1..1000")
	}
	return value, nil
}
