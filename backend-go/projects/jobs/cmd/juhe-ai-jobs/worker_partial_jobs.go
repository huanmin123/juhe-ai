package main

import (
	"log/slog"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobregistry"
)

// disabledJob 登记一个注册表已收录但组合根依赖未齐、本轮不调度的 scheduled
// job（对照 Node registry 显式登记缺失、不静默跳过的语义）。依赖补齐后由
// 对应 wire*Family 接管，本清单随 registry 的 GoStatus 更新收缩。
type disabledJob struct {
	JobName string `json:"jobName"`
	Reason  string `json:"reason"`
}

// workerPartialJobGaps 是 go-partial 中尚未接线的 scheduled job 缺口清单。
// 文本必须与 jobregistry GoBinding 一致：命名缺失的适配器与 Node 侧
// 权威来源，禁止使用“暂不支持”一类无信息量措辞。
// account-quality-refresh / account-api-key-cooldown-retest /
// normal-route-speed-first-recovery-probe 已由 wireProbeFamily 接管
// （worker_probe_jobs.go），account-circuit-control-plane-maintenance /
// account-circuit-recovery 已由 wireCircuitFamily（worker_circuit_jobs.go）
// 接管，从本清单移除。
var workerPartialJobGaps = []disabledJob{
	{
		JobName: "account-list-availability-projection-maintenance",
		Reason:  "ListAvailabilityRepo（17 方法 PG 双模读模型）与 overlay Redis 对账已由 circuitstore 提供；仍缺 LoadItems 物化载荷来源（ProjectionItem payload/AccountListItem 由网关域组装，jobs 无等价实现），不降级伪装",
	},
}

// registerDisabledJobsStartup 在 worker 装配时逐项登记未接线 job：与
// scheduleWiredJob 拒绝非 GoWired 的硬门禁互为两半——登记产生可观测启动
// 日志与 /health 载荷，硬门禁保证它们绝不进入调度循环。已翻转为 GoWired
// 的条目自动从缺口清单剔除（以 jobregistry 为准）。
func registerDisabledJobsStartup(assembly *workerAssembly, logger *slog.Logger) {
	for _, gap := range workerPartialJobGaps {
		entry, ok := jobregistry.Find(gap.JobName)
		if !ok || entry.GoStatus == jobregistry.GoWired {
			continue
		}
		assembly.registerDisabledJob(gap.JobName, gap.Reason)
	}
	// GoWired 但所属家族未启用（JUHE_AI_JOBS_*_ENABLED=false）的任务同样必须
	// 显式登记，不允许从未接线任务静默消失。
	registered := map[string]bool{}
	for _, job := range assembly.wiredJobs {
		registered[job] = true
	}
	for _, disabled := range assembly.disabledJobs {
		registered[disabled.JobName] = true
	}
	for _, entry := range jobregistry.ScheduledEntries() {
		if entry.GoStatus != jobregistry.GoWired || registered[entry.JobName] {
			continue
		}
		assembly.registerDisabledJob(entry.JobName, "所属家族未启用（对应 JUHE_AI_JOBS_*_ENABLED=false，组合根未装配依赖）")
	}
	_ = logger
}
