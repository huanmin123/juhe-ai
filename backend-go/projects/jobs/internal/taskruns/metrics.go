package taskruns

import (
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/prommetrics"
)

// 任务运行指标（jobs 进程唯一 prommetrics Registry 上注册，经健康监听
// /__aisys__/metrics 暴露）。埋点只在 RunWithTaskRun 单点收口：一次运行
// 记录状态机（queued→running→终态）产出一个 counter/histogram 样本，
// status 与 job_name/job_type 来自既有 TaskRunRunnerOptions 与终态运行
// 记录，不新增任务侧配置。
var taskMetrics taskRunMetrics

type taskRunMetrics struct {
	enabled atomic.Bool
	// runsTotal 是 juhe_ai_jobs_task_runs_total{job_name,job_type,status}。
	runsTotal prommetrics.CounterVec
	// runDuration 是 juhe_ai_jobs_task_run_duration_seconds{job_name,job_type}。
	runDuration prommetrics.HistogramVec
	// lastSuccess 是 juhe_ai_jobs_task_last_success_timestamp_seconds{job_name,job_type}，
	// completed 收口时 Set 当前 unix 秒。
	lastSuccess prommetrics.GaugeVec
	now         func() time.Time
}

// BindMetrics 在组合根的 registry 上注册任务指标族。进程生命周期内装配期
// 调用一次；未绑定（未经过组合根装配的进程内单测）时埋点为 no-op，任务
// 语义不变。
func BindMetrics(registry *prommetrics.Registry) {
	taskMetrics.runsTotal = prommetrics.NewCounterVec(registry,
		"juhe_ai_jobs_task_runs_total",
		"Background task runs by job name, type and terminal status.",
		nil, []string{"job_name", "job_type", "status"})
	taskMetrics.runDuration = prommetrics.NewHistogramVec(registry,
		"juhe_ai_jobs_task_run_duration_seconds",
		"Background task run wall duration from run record creation to terminal status.",
		nil, []string{"job_name", "job_type"}, nil)
	taskMetrics.lastSuccess = prommetrics.NewGaugeVec(registry,
		"juhe_ai_jobs_task_last_success_timestamp_seconds",
		"Unix timestamp of the last completed run per job name and type.",
		nil, []string{"job_name", "job_type"})
	taskMetrics.now = time.Now
	taskMetrics.enabled.Store(true)
}

// recordTaskRun 记录一次终态收口。status 必须是 completed/failed/skipped
// 之一（与 TaskRunStatus 同域）；终态运行记录缺失 status（收口自身失败）
// 时由调用方按 err 推导补齐，不丢样本。
func recordTaskRun(jobName, jobType, status string, startedAt time.Time) {
	if !taskMetrics.enabled.Load() {
		return
	}
	now := taskMetrics.now()
	taskMetrics.runsTotal.WithLabelValues(jobName, jobType, status).Inc()
	taskMetrics.runDuration.WithLabelValues(jobName, jobType).Observe(now.Sub(startedAt).Seconds())
	if status == string(StatusCompleted) {
		taskMetrics.lastSuccess.WithLabelValues(jobName, jobType).Set(float64(now.Unix()))
	}
}
