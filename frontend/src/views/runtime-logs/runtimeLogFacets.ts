import type { RuntimeLogFacets, RuntimeLogGrepRuntime } from '@/types/domain'

import { eventText } from './runtimeLogFormatters'

export type RuntimeLogEventOption = {
  label: string
  rawEvent: string
  value: string
}

export function buildRuntimeLogEventOptions(events: string[] = []): RuntimeLogEventOption[] {
  return events.map((event) => ({ label: eventText(event), value: event, rawEvent: event }))
}

export function filterRuntimeLogEventOption(input: string, option?: Partial<RuntimeLogEventOption>): boolean {
  const keyword = input.trim().toLowerCase()
  if (!keyword) return true
  return [option?.label, option?.rawEvent, option?.value].some((item) => String(item ?? '').toLowerCase().includes(keyword))
}

export function runtimeLogGrepRangeLimitText(runtime?: RuntimeLogGrepRuntime): string {
  if (!runtime) return '按文件时间筛选，默认最近 3 天，单次最多 7 天'
  return `按文件时间筛选，默认最近 ${runtime.defaultRangeDays} 天，单次最多 ${runtime.maxRangeDays} 天`
}

export function isRuntimeLogsAlertVisible(facets?: RuntimeLogFacets): boolean {
  return Boolean(facets && (
    !facets.runtimeAvailable
    || !facets.workerSnapshotAvailable
    || !facets.runtimeLogIndexQueueAvailable
    || !facets.dbService.statusAvailable
    || !facets.dbService.stateAvailable
    || !facets.gatewayAccountSideEffectsAvailable
  ))
}

export function runtimeLogsAlertDescription(facets?: RuntimeLogFacets): string {
  if (!facets) return ''
  const reasons: string[] = []
  if (!facets.runtimeAvailable) {
    reasons.push('服务运行态不可用')
  } else {
    if (!facets.workerSnapshotAvailable) reasons.push('后台进程快照不可用')
    if (!facets.runtimeLogIndexQueueAvailable) reasons.push('运行日志索引队列不可用')
    if (!facets.gatewayAccountSideEffectsAvailable) reasons.push('网关账户副作用状态不可用')
  }
  if (!facets.dbService.statusAvailable) {
    reasons.push('本地数据库服务状态不可用')
  } else if (!facets.dbService.stateAvailable) {
    reasons.push('本地数据库服务父进程状态不可用')
  }
  return `${reasons.join('；') || '运行态状态未知'}。`
}

export function isRuntimeLogQueueHealthAlertVisible(facets?: RuntimeLogFacets): boolean {
  const status = facets?.queueHealth?.status
  return status === 'degraded' || status === 'backlogged'
}

export function runtimeLogQueueHealthAlertDescription(facets?: RuntimeLogFacets): string {
  const health = facets?.queueHealth
  if (!health) return ''
  const parts: string[] = []
  if (health.summary.degradedCount > 0) parts.push(`${formatRuntimeCount(health.summary.degradedCount)} 个队列出现丢弃、拒绝或写入失败`)
  if (health.summary.backloggedCount > 0) parts.push(`${formatRuntimeCount(health.summary.backloggedCount)} 个队列明显积压`)
  if (health.summary.droppedCount > 0) parts.push(`累计丢弃 ${formatRuntimeCount(health.summary.droppedCount)} 条`)
  if (health.summary.rejectedCount > 0) parts.push(`IPC 拒绝 ${formatRuntimeCount(health.summary.rejectedCount)} 次`)
  if (health.summary.flushFailureCount > 0) parts.push(`落库失败 ${formatRuntimeCount(health.summary.flushFailureCount)} 次`)
  if (health.summary.queuedCount > 0) parts.push(`当前排队 ${formatRuntimeCount(health.summary.queuedCount)} 条`)
  const affectedQueues = queueHealthAffectedQueuesText(health)
  if (affectedQueues) parts.push(`受影响队列：${affectedQueues}`)
  return `${parts.join('；') || '队列状态异常'}。`
}

function queueHealthAffectedQueuesText(health: RuntimeLogFacets['queueHealth']): string {
  const queues = [...health.workerQueues, ...health.serverIpcQueues]
    .filter((queue) => queue.status === 'degraded' || queue.status === 'backlogged')
    .map((queue) => queue.label)
  if (queues.length <= 5) return queues.join('、')
  return `${queues.slice(0, 5).join('、')} 等 ${formatRuntimeCount(queues.length)} 个`
}

function formatRuntimeCount(value: number): string {
  return Number.isFinite(value) ? value.toLocaleString('zh-CN') : '0'
}
