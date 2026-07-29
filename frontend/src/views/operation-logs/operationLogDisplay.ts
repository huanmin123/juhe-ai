import type { OperationLogListItem } from '@/types/domain'

import { resourceTypeText } from './operationLogLabels'

export function actorText(record: OperationLogListItem): string {
  return displayName(record.actorDisplayName ?? record.actorSystemAccountName)
}

export function displayName(name?: string, id?: string): string {
  return name || id || '-'
}

export function resourceText(record: { resourceType: string; resourceId?: string; resourceName?: string }): string {
  return `${resourceTypeText(record.resourceType)}：${displayName(record.resourceName, record.resourceId)}`
}

export function requestText(record: { method?: string; path?: string }): string {
  return [record.method, record.path].filter(Boolean).join(' ') || '-'
}

export function valueText(value: unknown): string {
  if (value === undefined || value === null || value === '') return '-'
  if (typeof value === 'string') return value
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  try {
    return JSON.stringify(value)
  } catch {
    return String(value)
  }
}
