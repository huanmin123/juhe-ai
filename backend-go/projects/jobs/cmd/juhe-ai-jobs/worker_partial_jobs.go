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

// workerPartialJobGaps 是 11 项 go-partial 中尚未接线的 scheduled job 缺口
// 清单。文本必须与 jobregistry GoBinding 一致：命名缺失的适配器与 Node 侧
// 权威来源，禁止使用“暂不支持”一类无信息量措辞。
var workerPartialJobGaps = []disabledJob{
	{
		JobName: "account-quality-refresh",
		Reason:  "缺 AccountReader/Prober/PrecheckMutation 探针链适配器：find_account_for_test 的 effectiveAvailability 派生与 testOpenAIAccountDiagnosticAttempt 协议诊断栈（backend/src/modules/accounts/account-test.service.ts）尚无 jobs 侧等价实现；gateway-jobs 两 module 不可互 import",
	},
	{
		JobName: "account-api-key-cooldown-retest",
		Reason:  "缺 Prober/CooldownCandidateSource/CooldownMutation 探针链适配器：探针传输（协议诊断）与 record/defer 运行态 CAS 仓储（account-api-key-runtime-state.repository.ts）尚无 jobs 侧等价实现",
	},
	{
		JobName: "normal-route-speed-first-recovery-probe",
		Reason:  "缺 SpeedFirstClaimStore（Redis 降级运行态 normal-route-latency-degradation.service.ts 为 gateway runtime 单实现，jobs 复制 Lua/键契约会引入第二 writer）与首字探针传输",
	},
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
	{
		JobName: "data-retention-cleanup",
		Reason:  "缺 StatsWriter/PublicApiLogsCleaner/UsageRecordsCleaner/DbService 适配器：data-retention.repository.ts（1074 行）与 codex-context-state.repository.ts（1753 行）尚无 jobs 侧等价迁移",
	},
	{
		JobName: "chat-retention-cleanup",
		Reason:  "缺 DbService.CleanupChatRetention 适配器：chat.repository.ts cleanupChatRetention（chat 库分区/资产/检查点链）尚无 jobs 侧等价迁移",
	},
	{
		JobName: "expired-deleted-account-cleanup",
		Reason:  "缺 DbService.CleanupExpiredDeletedAccounts/RecordMaintenanceEnqueuer 适配器：逻辑删除物理清理仓储尚无 jobs 侧等价迁移",
	},
	{
		JobName: "api-key-record-cleanup-retry",
		Reason:  "缺 APIKeyRecordCleanupRetryer 适配器：api-key-record-cleanup.ts（1188 行 dataset targets/关联行清理/统计扣减）尚无 jobs 侧等价迁移",
	},
	{
		JobName: "account-record-cleanup-retry",
		Reason:  "缺 AccountRecordCleanupRetryer 适配器：account-record-cleanup.ts（1383 行 dataset targets/关联行清理/统计扣减）尚无 jobs 侧等价迁移",
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
