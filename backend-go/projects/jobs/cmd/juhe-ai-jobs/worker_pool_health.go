package main

import (
	"context"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/poolhealth"
)

// wirePoolHealthFamily assembles the independent proxy-pool availability probe
// (docs/functions/代理池设计.md §7.2). It deliberately does not share the J3a
// vendor-matrix budget or state: the probe only answers "is the proxy usable"
// against one fixed high-quota 204 endpoint.
func (a *workerAssembly) wirePoolHealthFamily(_ context.Context) error {
	handle, err := a.acquirePool(a.config.PostgresURL, "proxy-pool-health")
	if err != nil {
		return err
	}
	store, err := poolhealth.NewStore(handle.DB())
	if err != nil {
		return err
	}
	prober := poolhealth.NewHTTPProber(a.config.Secret, poolhealth.DefaultEndpoint, poolhealth.DefaultProbeTimeout)
	var healthWriter poolhealth.HealthWriter
	if a.config.RedisURL != "" {
		if _, client, redisErr := a.oauthCrossProcessState(); redisErr == nil && client != nil {
			writer, writerErr := poolhealth.NewRedisHealthWriter(client, a.config.RedisNamespace)
			if writerErr != nil {
				a.logger.Warn("代理池健康 Redis 投影未装配，gateway 将回退 PG", "error", writerErr)
			} else {
				healthWriter = writer
			}
		} else if redisErr != nil {
			a.logger.Warn("代理池健康 Redis 客户端不可用，gateway 将回退 PG", "error", redisErr)
		}
	}
	runner, err := poolhealth.NewRunner(store, prober, healthWriter, poolhealth.RunnerOptions{
		Logger: a.logger,
	})
	if err != nil {
		return err
	}
	a.scheduleWiredJob("proxy-pool-health", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		started := time.Now()
		summary, runErr := runner.RunOnce(taskCtx)
		if runErr != nil {
			return jobsched.TaskResult{}, runErr
		}
		fields := []any{
			"scanned", summary.Scanned,
			"succeeded", summary.Succeeded,
			"failed", summary.Failed,
			"skippedConfig", summary.SkippedConfig,
			"skippedCanceled", summary.SkippedCanceled,
			"projectionFailed", summary.ProjectionFailed,
			"redisFailed", summary.RedisFailed,
			"durationMs", time.Since(started).Milliseconds(),
		}
		if summary.Scanned == 0 {
			a.logger.Debug("代理池可用性探测无到期代理", fields...)
		} else {
			a.logger.Info("代理池可用性探测完成", fields...)
		}
		return jobsched.TaskResult{}, nil
	})
	return nil
}
