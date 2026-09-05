// worker_settings.go wires the background-jobs system_settings read model
// (background-jobs.ts settingsNumber port) into the worker assembly.
// staticSettings keeps serving the DEFAULT_SYSTEM_SETTINGS values for
// families without a settings database; the stats family upgrades the source
// to dbSettingsSource, which reads system_settings through
// internal/jobssettings with the Node failure semantics (missing table /
// failed snapshot refresh degrade to the defaults with a warn; integer or
// bounds violations fail the job task).
package main

import (
	"context"
	"log/slog"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountquality"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobssettings"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsverify"
)

// workerSettingsSource 是任务闭包读取调度参数的边界（Node
// settingsNumber(key, min, max) 调用点）。
type workerSettingsSource interface {
	statsAggregationBatchSize(ctx context.Context) (int, error)
	statsAggregationMaxBatches(ctx context.Context) (int, error)
}

// dbSettingsSource 经 jobssettings.Source 读取 system_settings；键边界沿用
// statsverify 的调度常量（与 Node 调用点一致）。
type dbSettingsSource struct{ source *jobssettings.Source }

func (s dbSettingsSource) statsAggregationBatchSize(ctx context.Context) (int, error) {
	return s.source.Number(ctx, "statsAggregationBatchSize",
		statsverify.StatsAggregationBatchSizeMin, statsverify.StatsAggregationBatchSizeMax)
}

func (s dbSettingsSource) statsAggregationMaxBatches(ctx context.Context) (int, error) {
	return s.source.Number(ctx, "statsAggregationMaxBatchesPerRun",
		statsverify.StatsAggregationMaxBatchesMin, statsverify.StatsAggregationMaxBatchesMax)
}

// jobssettingsWarn 适配 slog（Node logger.warn(errorLogFields(error,
// {event}), message)）。
func jobssettingsWarn(logger *slog.Logger) jobssettings.WarnFunc {
	if logger == nil {
		return nil
	}
	return func(event string, fields map[string]any, message string) {
		args := make([]any, 0, 2*len(fields)+2)
		args = append(args, "event", event)
		for key, value := range fields {
			args = append(args, key, value)
		}
		logger.Warn(message, args...)
	}
}

// settingsMode 映射 driver 到读模型模式。
func settingsMode(postgres bool) jobssettings.Mode {
	if postgres {
		return jobssettings.Postgres
	}
	return jobssettings.SQLite
}

// probeSettingsSource 经 system_settings 读模型解析 probe 族设置（Node
// account-probe-jobs.ts 的 SettingsNumberReader 注入的就是 background-jobs
// settingsNumber）。accountquality.SettingsNumber 没有错误通道：读取失败
// warn 后回落 DEFAULT_SYSTEM_SETTINGS 边界值，保持该端口的既有消费语义。
func (a *workerAssembly) probeSettingsSource(business *businessDB) accountquality.SettingsNumber {
	source := jobssettings.NewSource(jobssettings.Options{
		DB:   business.db,
		Mode: settingsMode(business.postgres),
		Warn: jobssettingsWarn(a.logger),
	})
	logger := a.logger
	return func(key string, min, max int) int {
		value, err := source.Number(context.Background(), key, min, max)
		if err == nil {
			return value
		}
		fallback, ok := jobssettings.DefaultNumber(key, min, max)
		if !ok {
			fallback = min
		}
		if logger != nil {
			logger.Warn("后台探针任务设置读取失败，回落默认值",
				"event", "background_probe_job_settings_read_failed", "key", key, "error", err.Error())
		}
		return fallback
	}
}
