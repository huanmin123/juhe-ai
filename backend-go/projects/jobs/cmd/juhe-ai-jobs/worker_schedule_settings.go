// worker_schedule_settings.go wires the settings-driven schedule intervals
// into the worker composition root (F3-1). Node background-jobs.ts reads the
// same settings through settingsNumber at scheduler.schedule() time
// (:286-308 / :331 / :379 / :406 / :461), so the interval is fixed at startup
// in both runtimes — a settings change takes effect on the next process
// assembly, not mid-run. The Go composition root resolves the same keys at
// assembly time through internal/jobssettings (the PG/SQLite system_settings
// read model) and hands the resolver to ResolveScheduleForDriver.
package main

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobregistry"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobssettings"
)

// jobsScheduleIntervalSpecs 固定 Node scheduler.schedule 调用点上每个设置
// 驱动间隔的 settingsNumber 边界与附加约束；job→键映射的权威表是
// jobregistry.SettingsIntervalJobNames()（两表的一致性由
// TestJobsScheduleIntervalSpecsMatchRegistry 锁定）。
var jobsScheduleIntervalSpecs = map[string]struct {
	Min int
	Max int
	// OnlineFreshnessCap 是 usageStatsOnlineAggregationIntervalSeconds 的
	// Math.min(settings, usageStatsOnlineFreshnessMaxIntervalSeconds=60)
	// （background-jobs.ts:97 + :453-458）。
	OnlineFreshnessCap int
}{
	"system-metrics-sample":             {Min: 5, Max: 3600},
	"usage-stats-aggregation":           {Min: 5, Max: 3600, OnlineFreshnessCap: 60},
	"client-ip-stats-aggregation":       {Min: 5, Max: 3600},
	"group-account-stats-refresh":       {Min: 5, Max: 3600},
	"usage-hot-window-refresh":          {Min: 60, Max: 3600},
	"account-api-key-cooldown-retest":   {Min: 1, Max: 3600},
	"openai-oauth-access-token-refresh": {Min: 10, Max: 3600},
	"account-quality-refresh":           {Min: 60, Max: 3600},
}

// wireScheduleSettings 打开调度间隔设置读模型并注册到装配体。PG 复用共享
// URL 池；SQLite 直开业务库。业务库路径缺失时保持 nil（该模式下没有任何
// settings 驱动 job 会注册，注册表默认间隔兜底），并打启动日志说明。
func (a *workerAssembly) wireScheduleSettings() error {
	var db *sql.DB
	if a.config.Driver == "postgres" {
		handle, err := a.acquirePool(a.config.PostgresURL, "schedule-settings")
		if err != nil {
			return err
		}
		db = handle.DB()
	} else {
		if a.config.BusinessSQLitePath == "" {
			a.logger.Warn("后台任务调度间隔设置源未装配（缺少业务库路径），按注册表默认间隔调度",
				"event", "background_job_schedule_settings_source_missing")
			return nil
		}
		var err error
		if db, err = a.openSQLite(a.config.BusinessSQLitePath, "schedule-settings"); err != nil {
			return err
		}
	}
	source := jobssettings.NewSource(jobssettings.Options{
		DB:   db,
		Mode: settingsMode(a.config.Driver == "postgres"),
		Warn: jobssettingsWarn(a.logger),
	})
	a.scheduleIntervals = func(jobName string) (time.Duration, bool) {
		return resolveScheduleSettingsInterval(context.Background(), source, a.logger, jobName)
	}
	return nil
}

// resolveScheduleSettingsInterval 按注册表键表 + Node settingsNumber 边界解析
// 一个 job 的设置间隔；非设置驱动 job 返回 false（注册表默认）。读取失败
// warn 后回落注册表默认间隔——Node 的 settingsNumber 失败会中止 background
// jobs 启动，Go 装配保持可启动性（缺表 / PG 快照失败已在读模型内降级默认）。
func resolveScheduleSettingsInterval(ctx context.Context, source *jobssettings.Source, logger *slog.Logger, jobName string) (time.Duration, bool) {
	spec, ok := jobsScheduleIntervalSpecs[jobName]
	if !ok {
		return 0, false
	}
	key, ok := jobregistry.SettingsIntervalJobNames()[jobName]
	if !ok {
		return 0, false
	}
	seconds, err := source.Number(ctx, key, spec.Min, spec.Max)
	if err != nil {
		if logger != nil {
			logger.Warn("后台任务设置间隔解析失败，回落注册表默认间隔",
				"event", "background_job_schedule_settings_read_failed",
				"job", jobName, "key", key, "error", err.Error())
		}
		return 0, false
	}
	if spec.OnlineFreshnessCap > 0 && seconds > spec.OnlineFreshnessCap {
		seconds = spec.OnlineFreshnessCap
	}
	return time.Duration(seconds) * time.Second, true
}
