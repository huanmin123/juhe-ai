package main

// T93-6 / C2-H1：usage_records 重算对账的任务结果映射。
//
// 对账只检测不修复；差异必须进入 scheduler 可见的 partial 结果（lastWarning
// + jobsched_run_partial Warn），零值 Outcome 会被调度器按 success 处理并清空
// Warning（等于静默），因此映射单独成函数并配钉住测试。

import (
	"fmt"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsverify"
)

// usageStatsReconciliationTaskResult 把对账结果映射为任务结果：有差异时报
// OutcomePartial + 差异摘要，无差异报零值（success）。
func usageStatsReconciliationTaskResult(reconciliation statsverify.UsageStatsReconciliationResult) jobsched.TaskResult {
	if reconciliation.IssueCount == 0 {
		return jobsched.TaskResult{}
	}
	return jobsched.TaskResult{
		Outcome: jobsched.OutcomePartial,
		Warning: fmt.Sprintf("usage_records 与 usage_stats_hourly 对账发现 %d 条差异（窗口 %s~%s）",
			reconciliation.IssueCount, reconciliation.WindowStartHour, reconciliation.WindowEndHour),
	}
}
