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
