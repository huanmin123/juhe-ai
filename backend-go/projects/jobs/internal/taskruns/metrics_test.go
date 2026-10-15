package taskruns

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/prommetrics"
)

// metrics_test.go 锁定任务指标埋点契约：RunWithTaskRun 单点收口在独立
// registry 上按终态产出 counter/histogram 样本，completed 另 Set
// last_success（固定时钟断言精确值）；CreateTaskRun 失败（无运行记录）
// 不产生样本。
func TestTaskRunMetricsEmittedPerTerminalStatus(t *testing.T) {
	ctx := context.Background()
	registry := prommetrics.NewRegistry()
	BindMetrics(registry)
	fixedNow := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	taskMetrics.now = func() time.Time { return fixedNow }
	defer func() { taskMetrics.now = time.Now }()

	// completed：三族指标全部产出样本。
	completedStore, _ := w12bOpenFailStore(t)
	run, _, err := RunWithTaskRun(ctx, completedStore, TaskRunRunnerOptions{
		JobName: "metrics-job", JobType: "metrics-type",
		WorkerRole: TemporaryMaintenanceWorkerRole, HeartbeatInterval: time.Hour,
	}, func(context.Context, LeaseFence, TaskRun) (TaskRunResult, error) {
		return TaskRunResult{Status: StatusCompleted, Result: map[string]any{"ok": true}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusCompleted {
		t.Fatalf("运行记录应 completed: %s", run.Status)
	}
	assertMetricBody(t, registry, func(what string, body string) {
		for _, want := range []string{
			`juhe_ai_jobs_task_runs_total{job_name="metrics-job",job_type="metrics-type",status="completed"} 1`,
			`juhe_ai_jobs_task_run_duration_seconds_count{job_name="metrics-job",job_type="metrics-type"} 1`,
			// expfmt 以 'g' 格式渲染浮点值（如 1.7919792e+09）。
			`juhe_ai_jobs_task_last_success_timestamp_seconds{job_name="metrics-job",job_type="metrics-type"} ` +
				strconv.FormatFloat(float64(fixedNow.Unix()), 'g', -1, 64),
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s 缺少样本 %q:\n%s", what, want, body)
			}
		}
	})

	// failed：任务函数错误 → status="failed" 样本，且不触碰 last_success。
	failedStore, _ := w12bOpenFailStore(t)
	_, _, err = RunWithTaskRun(ctx, failedStore, TaskRunRunnerOptions{
		JobName: "metrics-fail-job", JobType: "metrics-type",
		WorkerRole: TemporaryMaintenanceWorkerRole, HeartbeatInterval: time.Hour,
	}, func(context.Context, LeaseFence, TaskRun) (TaskRunResult, error) {
		return TaskRunResult{}, errors.New("metrics 业务失败")
	})
	if err == nil {
		t.Fatal("任务函数失败应返回错误")
	}
	assertMetricBody(t, registry, func(what string, body string) {
		if !strings.Contains(body, `juhe_ai_jobs_task_runs_total{job_name="metrics-fail-job",job_type="metrics-type",status="failed"} 1`) {
			t.Fatalf("%s 缺少 failed 样本:\n%s", what, body)
		}
		if strings.Contains(body, `juhe_ai_jobs_task_last_success_timestamp_seconds{job_name="metrics-fail-job"`) {
			t.Fatalf("%s failed 不得产出 last_success:\n%s", what, body)
		}
	})

	// skipped：启动 CAS 未命中 → status="skipped" 样本。
	skipStore, skipSpec := w12bOpenFailStore(t)
	skipSpec.armZero("SET status = 'running'")
	skipRun, _, err := RunWithTaskRun(ctx, skipStore, TaskRunRunnerOptions{
		JobName: "metrics-skip-job", JobType: "metrics-type",
		WorkerRole: TemporaryMaintenanceWorkerRole, HeartbeatInterval: time.Hour,
	}, func(context.Context, LeaseFence, TaskRun) (TaskRunResult, error) {
		t.Fatal("CAS 未命中时任务函数不应执行")
		return TaskRunResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if skipRun.Status != StatusSkipped {
		t.Fatalf("运行记录应 skipped: %s", skipRun.Status)
	}
	assertMetricBody(t, registry, func(what string, body string) {
		if !strings.Contains(body, `juhe_ai_jobs_task_runs_total{job_name="metrics-skip-job",job_type="metrics-type",status="skipped"} 1`) {
			t.Fatalf("%s 缺少 skipped 样本:\n%s", what, body)
		}
	})

	// CreateTaskRun 失败：无运行记录 → 不产出任何样本。
	createFailStore, createFailSpec := w12bOpenFailStore(t)
	createFailSpec.arm("INSERT INTO background_task_runs")
	_, _, err = RunWithTaskRun(ctx, createFailStore, TaskRunRunnerOptions{
		JobName: "metrics-absent-job", WorkerRole: TemporaryMaintenanceWorkerRole,
	}, nil)
	if err == nil {
		t.Fatal("创建注入失败应返回错误")
	}
	assertMetricBody(t, registry, func(what string, body string) {
		if strings.Contains(body, "metrics-absent-job") {
			t.Fatalf("%s 无运行记录不得产出样本:\n%s", what, body)
		}
	})
}

// assertMetricBody 拉取当前 exposition 文本并交给断言闭包。
func assertMetricBody(t *testing.T, registry *prommetrics.Registry, assert func(what string, body string)) {
	t.Helper()
	body, err := registry.GatherText()
	if err != nil {
		t.Fatal(err)
	}
	assert("metrics exposition", body)
}
