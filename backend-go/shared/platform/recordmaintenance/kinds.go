// Package recordmaintenance 是 record_maintenance_jobs 快照行 kind 白名单的
// 单一事实源：gateway 投递端（internal/recordmaintenance dispatch 投递前校验）
// 与 jobs 消费端（internal/retention 执行器校验）共用同一集合，防止两端各自
// 持有字面量集合后漂移——生产端放行的 kind 若不被消费端接受，行会毒化队头
// （T207 C1 投递端校验的去重实现）。gateway 与 jobs 是不同 Go module，共用
// 常量只能落在 shared/platform。
package recordmaintenance

// 账号用量快照行的合法 kind 集合（Node RecordMaintenanceJob
// account_usage_snapshot_upsert 变体的采集族判别）。
const (
	// SnapshotKindOpenAICodex 是 codex usage headers 快照行 kind。
	SnapshotKindOpenAICodex = "openai_codex"
	// SnapshotKindAnthropicClaude 是 anthropic（Claude OAuth）unified rate
	// limit 头快照行 kind（AI账户Grok用量快照设计 §8.2）。
	SnapshotKindAnthropicClaude = "anthropic_claude"
)

// IsValidAccountUsageSnapshotKind 报告 kind 是否在白名单内。
func IsValidAccountUsageSnapshotKind(kind string) bool {
	switch kind {
	case SnapshotKindOpenAICodex, SnapshotKindAnthropicClaude:
		return true
	}
	return false
}
