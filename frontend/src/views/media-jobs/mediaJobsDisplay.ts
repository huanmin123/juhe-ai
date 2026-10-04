import type { ResponsivePagedListResult } from '@/composables/useResponsivePagedList'
import type { MediaJobKind, MediaJobListItem, MediaJobListResult, MediaJobStatus } from '@/types/domain'
import { mediaJobKindLabels, mediaJobStatusColors, mediaJobStatusLabels } from '@/types/domain'

export function formatMediaJobKind(kind?: MediaJobKind): string {
  if (!kind) return '-'
  return mediaJobKindLabels[kind] ?? kind
}

export function formatMediaJobStatus(status?: MediaJobStatus): string {
  if (!status) return '-'
  return mediaJobStatusLabels[status] ?? status
}

export function mediaJobStatusColor(status?: MediaJobStatus): string {
  if (!status) return 'default'
  return mediaJobStatusColors[status] ?? 'default'
}

export function formatMediaJobSeconds(seconds?: number): string {
  if (typeof seconds !== 'number' || !Number.isFinite(seconds) || seconds < 0) return '-'
  const total = Math.round(seconds)
  if (total < 60) return `${total}s`
  const minutes = Math.floor(total / 60)
  const rest = total % 60
  return `${minutes}m${String(rest).padStart(2, '0')}s`
}

/** 尺寸直通显示：size 是 WxH 像素串（如 1280x720），原样展示，缺省显示 -。 */
export function formatMediaJobSize(size?: string): string {
  if (typeof size !== 'string' || !size.trim()) return '-'
  return size
}

export function formatMediaJobCost(costUsd?: number): string {
  if (typeof costUsd !== 'number' || !Number.isFinite(costUsd)) return '-'
  return `$${costUsd.toFixed(6)}`
}

export function mediaJobErrorSummary(record: MediaJobListItem): string {
  return record.error?.code ?? '-'
}

export function mediaJobErrorDetail(record: MediaJobListItem): string {
  if (!record.error) return ''
  return record.error.message ? `${record.error.code}：${record.error.message}` : record.error.code
}

/** 后端契约 `{rows,total}` + limit/offset 翻页 → useResponsivePagedList 页结果。 */
export function adaptMediaJobListResult(
  result: MediaJobListResult,
  page: { current: number; pageSize: number }
): ResponsivePagedListResult<MediaJobListItem> {
  const offset = (page.current - 1) * page.pageSize
  return {
    items: result.rows,
    page: page.current,
    pageSize: page.pageSize,
    total: result.total,
    hasMore: offset + result.rows.length < result.total,
    currentPageCount: result.rows.length
  }
}
