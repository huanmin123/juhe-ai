package main

// T93-6 / C2-H1 钉住测试：重算对账的差异必须进入 partial 任务结果（不静默）。

import (
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsverify"
)

func TestT936ReconciliationTaskResultSurfacesIssues(t *testing.T) {
	clean := usageStatsReconciliationTaskResult(statsverify.UsageStatsReconciliationResult{
		WindowStartHour: "2026-09-07T09",
		WindowEndHour:   "2026-09-07T10",
		IssueCount:      0,
	})
	if clean.Outcome != "" || clean.Warning != "" {
		t.Fatalf("无差异必须报 success 零值: %+v", clean)
	}

	mismatched := usageStatsReconciliationTaskResult(statsverify.UsageStatsReconciliationResult{
		WindowStartHour: "2026-09-07T09",
		WindowEndHour:   "2026-09-07T10",
		IssueCount:      7,
	})
	if mismatched.Outcome != jobsched.OutcomePartial {
		t.Fatalf("有差异必须报 partial（零值 Outcome 会被当成 success 并清空 Warning）: %+v", mismatched)
	}
	if !strings.Contains(mismatched.Warning, "7 条差异") ||
		!strings.Contains(mismatched.Warning, "2026-09-07T09~2026-09-07T10") {
		t.Fatalf("Warning 必须携带差异计数与窗口: %q", mismatched.Warning)
	}
}
