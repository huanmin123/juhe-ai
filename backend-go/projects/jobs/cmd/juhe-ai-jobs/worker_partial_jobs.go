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
// （worker_probe_jobs.go），从本清单移除。
var workerPartialJobGaps = []disabledJob{
	{
		JobName: "account-circuit-control-plane-maintenance",
		Reason:  "缺 Redis CircuitStore（gatewaycircuit/store_redis.go + lua.go，Lua 状态机单实现）与 ControlPlaneLedger/Outbox 适配器；跨模块不可 import，复制实现有状态分歧风险",
	},
	{
		JobName: "account-list-availability-projection-maintenance",
		Reason:  "缺 ListAvailabilityRepo（PostgreSQL 读模型仓储）与 LoadItems 物化载荷来源（ProjectionItem payload 由网关域组装，jobs 无等价实现）",
	},
	{
		JobName: "account-circuit-recovery",
		Reason:  "缺 Redis CircuitStore（同 control-plane：gatewaycircuit 单实现）与恢复探针目标解析（账户域读取链）",
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
	_ = logger
}
