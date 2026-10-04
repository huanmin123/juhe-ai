import { describe, expect, it } from 'vitest'
import type { MediaJobListItem } from '@/types/domain'
import {
  adaptMediaJobListResult,
  formatMediaJobCost,
  formatMediaJobKind,
  formatMediaJobSeconds,
  formatMediaJobSize,
  formatMediaJobStatus,
  mediaJobErrorDetail,
  mediaJobErrorSummary,
  mediaJobStatusColor
} from './mediaJobsDisplay'

describe('mediaJobsDisplay 枚举标签映射', () => {
  it('kind 与 status 六态均有中文标签', () => {
    expect(formatMediaJobKind('video')).toBe('视频')
    expect(formatMediaJobKind('audio_transcription')).toBe('长音频转写')
    expect(formatMediaJobKind('audio_speech')).toBe('长音频合成')
    for (const status of ['queued', 'in_progress', 'completed', 'failed', 'cancelled', 'expired'] as const) {
      expect(formatMediaJobStatus(status)).not.toBe(status)
      expect(mediaJobStatusColor(status)).toBeTruthy()
    }
  })

  it('缺省 kind/status 显示占位符', () => {
    expect(formatMediaJobKind(undefined)).toBe('-')
    expect(formatMediaJobStatus(undefined)).toBe('-')
  })
})

describe('mediaJobsDisplay 数值格式化', () => {
  it('时长秒：小于 60 秒显示秒，超过则分秒组合，缺省显示 -', () => {
    expect(formatMediaJobSeconds(12)).toBe('12s')
    expect(formatMediaJobSeconds(63)).toBe('1m03s')
    expect(formatMediaJobSeconds(undefined)).toBe('-')
  })

  it('尺寸：像素串直通显示，缺省显示 -', () => {
    expect(formatMediaJobSize('1280x720')).toBe('1280x720')
    expect(formatMediaJobSize('720x1280')).toBe('720x1280')
    expect(formatMediaJobSize('')).toBe('-')
    expect(formatMediaJobSize(undefined)).toBe('-')
  })

  it('成本 USD：六位小数，缺省显示 -', () => {
    expect(formatMediaJobCost(0.123456)).toBe('$0.123456')
    expect(formatMediaJobCost(0)).toBe('$0.000000')
    expect(formatMediaJobCost(undefined)).toBe('-')
  })
})

describe('mediaJobsDisplay 错误摘要', () => {
  it('摘要取 error.code，Tooltip 详情拼接 code 与 message', () => {
    const record = {
      id: 'job_1',
      kind: 'video',
      status: 'failed',
      error: { code: 'artifact_expired', message: '产物已过期' }
    } as MediaJobListItem
    expect(mediaJobErrorSummary(record)).toBe('artifact_expired')
    expect(mediaJobErrorDetail(record)).toBe('artifact_expired：产物已过期')
  })

  it('无错误时摘要为占位符、详情为空', () => {
    const record = { id: 'job_2', kind: 'video', status: 'completed' } as MediaJobListItem
    expect(mediaJobErrorSummary(record)).toBe('-')
    expect(mediaJobErrorDetail(record)).toBe('')
  })
})

describe('adaptMediaJobListResult limit/offset 契约适配', () => {
  it('按 offset + 行数计算 hasMore 并保留 total', () => {
    const adapted = adaptMediaJobListResult(
      { rows: [{ id: 'job_1', kind: 'video', status: 'queued', createdAt: '2026-10-01T00:00:00Z' }], total: 3 },
      { current: 1, pageSize: 1 }
    )
    expect(adapted.items).toHaveLength(1)
    expect(adapted.page).toBe(1)
    expect(adapted.pageSize).toBe(1)
    expect(adapted.total).toBe(3)
    expect(adapted.hasMore).toBe(true)
  })

  it('最后一页 hasMore 为 false', () => {
    const adapted = adaptMediaJobListResult({ rows: [], total: 0 }, { current: 1, pageSize: 50 })
    expect(adapted.hasMore).toBe(false)
    expect(adapted.total).toBe(0)
  })
})
