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

export function workerRoleText(role: string): string {
  if (role === 'gateway') return '网关'
  if (role === 'jobs') return '后台任务'
  return role || '-'
}
