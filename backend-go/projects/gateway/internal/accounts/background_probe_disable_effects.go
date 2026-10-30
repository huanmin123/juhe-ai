package accounts

import "context"

// 后台自动探测总开关禁用臂的运行态清理端口（总开关设计 §4，2026-10-30 独立
// 复审 M1）：禁用事务只覆盖数据库侧（状态归一化、锁死/enforcement/J1 outbox
// 清理、dispatch fence 推进入队 circuit outbox 关闭事件），Redis 运行态与
// 速度优先降级、探针派生 Key 运行态池等网关面在提交后经本端口清理。生产装配
// 见 cmd/juhe-ai-gateway/compose_accounts_reset.go（复用既有运行时重置桥）；
// 端口未装配（nil）时禁用降级为仅数据库侧清理，并在审计 details 注明。

// BackgroundProbeRuntimeCleanupInput 是禁用臂运行态清理的输入：ExpectedConfigRevision
// 是禁用事务提交后的新版本，供 Key 运行态池重验证对齐新配置。
type BackgroundProbeRuntimeCleanupInput struct {
	AccountID              string
	SystemAccountID        string
	ExpectedConfigRevision int64
}

// BackgroundProbeRuntimeCleanup 清理禁用账户的网关侧运行态（circuit 运行态
// 经事务内 dispatch fence 推进由 jobs 投影关闭，端口负责其余面）：
//   - latency_degraded（速度优先首字降级）运行态；
//   - 探针派生 Key 运行态池（account_api_key_runtime_states）按新版本重验证；
//   - 网关运行时缓存投影失效（K5 总线）。
//
// 返回清理计数（审计明细用）；错误上抛由调用方按 best-effort 留痕（禁用已
// 提交，不再回滚业务结果）。
type BackgroundProbeRuntimeCleanup interface {
	ClearDisabledBackgroundProbeRuntime(ctx context.Context, input BackgroundProbeRuntimeCleanupInput) (int64, error)
}

// SetBackgroundProbeRuntimeCleanup wires the disable-arm runtime port
// (composition-root handover; nil keeps the disable arm DB-only).
func (s *Store) SetBackgroundProbeRuntimeCleanup(cleanup BackgroundProbeRuntimeCleanup) {
	s.backgroundProbeRuntimeCleanup = cleanup
}
