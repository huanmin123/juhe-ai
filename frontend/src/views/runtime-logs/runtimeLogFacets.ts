import type { RuntimeLogGrepRuntime } from '@/types/domain'

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
