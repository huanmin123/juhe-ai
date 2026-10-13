/**
 * 工作室跨工作台展示纯函数（日期分组、字节/耗时/成本格式化）。各工作台
 * display 模块按需包装并配套 vitest 用例（views 纯函数不计入覆盖率门槛，
 * 先例 chatMediaTaskPolling.test.ts 同口径）。
 */

import type { ResponsivePagedListResult } from '@/composables/useResponsivePagedList'
import type { StudioAsset, StudioListResult } from '@/types/domain'

export interface StudioAssetDateGroup {
  dateLabel: string
  assets: StudioAsset[]
}

function startOfLocalDay(value: Date): number {
  return new Date(value.getFullYear(), value.getMonth(), value.getDate()).getTime()
}

/** 画廊按日期分组（今天/昨天/YYYY-MM-DD），保持输入时间倒序分组先后。 */
export function groupStudioAssetsByDate(assets: StudioAsset[], nowMs: number = Date.now()): StudioAssetDateGroup[] {
  const groups: StudioAssetDateGroup[] = []
  const indexByLabel = new Map<string, number>()
  const today = startOfLocalDay(new Date(nowMs))
  const yesterday = today - 86_400_000
  for (const asset of assets) {
    const created = new Date(asset.createdAt)
    const dayStart = startOfLocalDay(created)
    let label: string
    if (dayStart === today) label = '今天'
    else if (dayStart === yesterday) label = '昨天'
    else label = `${created.getFullYear()}-${String(created.getMonth() + 1).padStart(2, '0')}-${String(created.getDate()).padStart(2, '0')}`
    const existing = indexByLabel.get(label)
    if (existing !== undefined) {
      groups[existing].assets.push(asset)
      continue
    }
    indexByLabel.set(label, groups.length)
    groups.push({ dateLabel: label, assets: [asset] })
  }
  return groups
}

/** 本地时间展示（HH:mm 或去年跨年带日期），Invalid Date 回落 '-'。 */
export function formatStudioDateTime(iso: string, nowMs: number = Date.now()): string {
  const value = new Date(iso)
  if (Number.isNaN(value.getTime())) return '-'
  const sameYear = value.getFullYear() === new Date(nowMs).getFullYear()
  const hhmm = `${String(value.getHours()).padStart(2, '0')}:${String(value.getMinutes()).padStart(2, '0')}`
  if (sameYear) {
    return `${value.getMonth() + 1}-${value.getDate()} ${hhmm}`
  }
  return `${value.getFullYear()}-${value.getMonth() + 1}-${value.getDate()} ${hhmm}`
}

/** 已耗时（创建时间 → now）：60s 内 x 秒，之后 x 分 y 秒，失败/完成后定格。 */
export function formatStudioTaskElapsed(createdAt: string, nowMs: number): string {
  const created = new Date(createdAt).getTime()
  if (!Number.isFinite(created)) return '-'
  const totalSeconds = Math.max(0, Math.floor((nowMs - created) / 1000))
  if (totalSeconds < 60) return `${totalSeconds}s`
  const minutes = Math.floor(totalSeconds / 60)
  const rest = totalSeconds % 60
  return `${minutes}m${String(rest).padStart(2, '0')}s`
}

/** 字节数人类可读（B/KB/MB/GB，一位小数）。 */
export function formatStudioBytes(bytes?: number | null): string {
  if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes < 0) return '-'
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB']
  let value = bytes / 1024
  let unitIndex = 0
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024
    unitIndex += 1
  }
  return `${value.toFixed(1)} ${units[unitIndex]}`
}

/** 时长（毫秒 → mm:ss / hh:mm:ss），来源缺失 null 回落 '-'。 */
export function formatStudioDuration(durationMs?: number | null): string {
  if (typeof durationMs !== 'number' || !Number.isFinite(durationMs) || durationMs < 0) return '-'
  const totalSeconds = Math.round(durationMs / 1000)
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  if (hours > 0) {
    return `${hours}:${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`
  }
  return `${minutes}:${String(seconds).padStart(2, '0')}`
}

/**
 * 成本预估格式化（§4.1：目录价缺失时显式"无法估算"，不编造数字）。
 * unitPrice 由调用方从模型目录能力载荷读取；quantity 为数量基准
 * （图像 1 张、TTS 千字符、视频秒数）。无价/非法输入一律"无法估算"。
 */
export function formatStudioCostEstimate(unitPrice: number | undefined, quantity: number): string {
  if (typeof unitPrice !== 'number' || !Number.isFinite(unitPrice) || unitPrice < 0) return '无法估算'
  if (!Number.isFinite(quantity) || quantity <= 0) return '无法估算'
  return `$${(unitPrice * quantity).toFixed(6)}`
}

/** 后端 `{rows,total}` + limit/offset 翻页 → useResponsivePagedList 页结果（mediaJobs adapt 先例）。 */
export function adaptStudioListPage<T>(
  result: StudioListResult<T>,
  page: { current: number; pageSize: number }
): ResponsivePagedListResult<T> {
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
