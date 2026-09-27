import type { SystemMetricsRuntimeJob } from '@/types/domain'

export type BackgroundJobRow = SystemMetricsRuntimeJob

const backgroundJobStatusMeta: Record<string, { text: string; color: string }> = {
  queued: { text: '排队中', color: 'default' },
  running: { text: '运行中', color: 'processing' },
  completed: { text: '成功', color: 'success' },
  failed: { text: '失败', color: 'error' },
  skipped: { text: '已跳过', color: 'default' }
}

export function backgroundJobStatusText(status: string): string {
  return backgroundJobStatusMeta[status]?.text ?? (status || '未知')
}

export function backgroundJobStatusColor(status: string): string {
  return backgroundJobStatusMeta[status]?.color ?? 'default'
}

// job_name 全集来自 backend-go/projects/jobs/internal/jobregistry/registry.go
// 的 ScheduledEntries（RunWithTaskRun 只为 scheduled 条目写 background_task_runs）。
const backgroundJobNameLabels: Record<string, string> = {
  'system-metrics-sample': '系统指标采样',
  'system-metrics-trend-windows-refresh': '系统指标趋势窗口刷新',
  'usage-stats-aggregation': '用量统计聚合',
  'usage-hot-window-refresh': '用量热窗口刷新',
  'client-ip-stats-aggregation': '客户端 IP 统计聚合',
  'group-account-stats-refresh': '分组账户统计刷新',
  'usage-rank-snapshots-refresh': '用量排行快照刷新',
  'ai-performance-summary-windows-refresh': 'AI 性能汇总窗口刷新',
  'usage-overview-windows-refresh': '用量概览窗口刷新',
  'usage-scope-range-windows-refresh': '用量范围窗口刷新',
  'authorization-usage-range-windows-refresh': '授权用量范围窗口刷新',
  'usage-quota-hourly-windows-refresh': '用量配额小时窗口刷新',
  'usage-stats-consistency-check': '用量统计一致性检查',
  'background-task-run-reconcile': '后台任务运行记录对账',
  'api-key-record-cleanup-retry': 'API Key 记录清理重试',
  'account-record-cleanup-retry': '账户记录清理重试',
  'api-key-availability-schedule-status-sync': 'API Key 可用性计划状态同步',
  'account-availability-schedule-status-sync': '账户可用性计划状态同步',
  'resource-authorization-expiry-sweep': '资源授权到期清扫',
  'account-quality-refresh': '账户质量刷新',
  'account-balance-refresh': '账户余额刷新',
  'account-balance-auto-detect-recovery': '账户余额自动检测恢复',
  'account-balance-stats-projection': '账户余额统计投影',
  'xai-grok-usage-refresh': 'xAI Grok 用量刷新',
  'openai-oauth-access-token-refresh': 'OpenAI OAuth 访问令牌刷新',
  'oauth-keepalive-token-refresh': 'OAuth 保活令牌刷新',
  'account-api-key-cooldown-retest': '账户 API Key 冷却重测',
  'normal-route-speed-first-recovery-probe': '普通路由速度优先恢复探测',
  'account-circuit-control-plane-maintenance': '账户熔断控制面维护',
  'account-list-availability-projection-maintenance': '账户列表可用性投影维护',
  'account-circuit-recovery': '账户熔断恢复',
  'key-model-memory-recovery': '键模型运行态恢复',
  'data-retention-cleanup': '数据保留清理',
  'chat-retention-cleanup': '会话保留清理',
  'expired-deleted-account-cleanup': '过期已删除账户清理'
}

export function backgroundJobNameText(jobName: string): string {
  return backgroundJobNameLabels[jobName] ?? (jobName || '未知')
}

// 失败行整行高亮（设计文档：后台任务表失败行高亮）；其余状态不附加类。
export function backgroundJobRowClassName(record: BackgroundJobRow): string {
  return record.status === 'failed' ? 'stats-background-job-row-failed' : ''
}

export function workerRoleText(role: string): string {
  if (role === 'gateway') return '网关'
  if (role === 'jobs') return '后台任务'
  return role || '-'
}
