import type { OperationLogSummary } from '@/types/domain'

import { resourceTypeText } from './operationLogLabels'

export function actorText(record: OperationLogSummary): string {
  return displayName(record.actorDisplayName ?? record.actorSystemAccountName)
}

export function displayName(name?: string, _id?: string): string {
  return name || '-'
}

export function resourceText(record: Pick<OperationLogSummary, 'resourceType' | 'resourceName' | 'resourceId'>): string {
  return `${resourceTypeText(record.resourceType)}：${displayName(record.resourceName)}`
}

export function requestText(record: Pick<OperationLogSummary, 'method' | 'path'>): string {
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
