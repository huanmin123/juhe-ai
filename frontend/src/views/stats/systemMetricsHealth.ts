import type { SystemMetricsHealthSnapshot } from '@/types/domain'

// 权威 key 清单来源（不得凭空新增）：
// - gateway 就绪载荷：backend-go/projects/gateway/cmd/juhe-ai-gateway/health_route.go readiness()
// - jobs /health 载荷：backend-go/projects/jobs/cmd/juhe-ai-jobs/main.go 健康监听 payload
const healthKeyLabels: Record<string, string> = {
  ready: '整体就绪',
  ownerReady: 'Owner 就绪',
  ownerMode: 'Owner 模式',
  auditLogReady: '审计日志',
  operationLogReady: '操作日志',
  j3bReady: '模型检查',
  jobsReady: '后台任务',
  sessionRetentionReady: '会话保留',
  accountCircuitRuntimeReady: '账户熔断运行时',
  runtimeLogOwnerHeld: '运行日志 Owner',
  tableMonitorReady: '表监控',
  accountHealthEnabled: '账户健康启用',
  accountHealthReady: '账户健康',
  accountBalanceEnabled: '账户余额启用',
  accountBalanceReady: '账户余额',
  proxyLatencyEnabled: '代理延迟探测启用',
  proxyLatencyReady: '代理延迟探测',
  proxyLatencyOwnerHeld: '代理延迟 Owner',
  workerEnabled: 'Worker 启用',
  workerReady: 'Worker 就绪'
}

export type HealthConclusion = 'ok' | 'partial' | 'unreachable'

export interface HealthProcessSection {
  conclusion: HealthConclusion
  reason?: string
  entries: Array<[string, unknown]>
}

/** 就绪项 key → 中文标签；未知 key 回退显示原 key。 */
export function healthStatusLabel(key: string): string {
  return healthKeyLabels[key] ?? key
}

/** Owner 模式文本值：active→主用、standby→备用、其他原样。 */
export function ownerModeText(value: unknown): string {
  if (value === 'active') return '主用'
  if (value === 'standby') return '备用'
  if (value === null || value === undefined || value === '') return '-'
  return String(value)
}

/** 非布尔就绪项的文本渲染（ownerMode 走语义映射）。 */
export function healthValueText(key: string, value: unknown): string {
  if (key === 'ownerMode') return ownerModeText(value)
  if (value === null || value === undefined || value === '') return '-'
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

/** 结论 tag 文案。 */
export function healthConclusionText(conclusion: HealthConclusion): string {
  if (conclusion === 'ok') return '正常'
  if (conclusion === 'partial') return '部分未就绪'
  return '不可达'
}

/** 结论 tag 语义色（a-tag color）。 */
export function healthConclusionColor(conclusion: HealthConclusion): string {
  if (conclusion === 'ok') return 'success'
  if (conclusion === 'partial') return 'warning'
  return 'error'
}

function conclusionFromEntries(entries: Array<[string, unknown]>): HealthConclusion {
  const booleans = entries.filter(([, value]) => typeof value === 'boolean')
  if (booleans.length === 0) return 'ok'
  return booleans.every(([, value]) => value === true) ? 'ok' : 'partial'
}

/** Gateway 进程卡：就绪载荷逐项透出，结论由布尔项判定。 */
export function gatewayHealthSection(payload: Record<string, unknown> | undefined): HealthProcessSection {
  const entries = Object.entries(payload ?? {})
  return { conclusion: conclusionFromEntries(entries), entries }
}

/** Jobs 进程卡：不可达时 conclusion=unreachable 并保留 reason。 */
export function jobsHealthSection(jobs: SystemMetricsHealthSnapshot['jobs'] | undefined): HealthProcessSection {
  if (!jobs || !jobs.available) {
    return { conclusion: 'unreachable', reason: jobs?.reason, entries: [] }
  }
  const entries = Object.entries(jobs.payload ?? {})
  return { conclusion: conclusionFromEntries(entries), entries }
}
