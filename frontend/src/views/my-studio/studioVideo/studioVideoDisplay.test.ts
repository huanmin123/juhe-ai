import { describe, expect, it } from 'vitest'

import type { StudioModelOption, StudioTask } from '@/types/domain'
import {
  formatSecondsOption,
  sortVideoTasksForDisplay,
  studioVideoCostEstimate,
  videoTaskArtifactLabel
} from './studioVideoDisplay'

function videoModel(capabilities: Record<string, unknown>): StudioModelOption {
  return { model: 'veo-3', providerCode: 'gemini', domain: 'video', endpointModes: ['video_create'], capabilities }
}

describe('studioVideoDisplay 时长档位展示', () => {
  it('数值加"秒"，字符串原样', () => {
    expect(formatSecondsOption(4)).toBe('4 秒')
    expect(formatSecondsOption('6s')).toBe('6s')
    expect(formatSecondsOption('8')).toBe('8')
  })
})

describe('studioVideoDisplay 成本预估', () => {
  it('无价或未选时长显式"无法估算"，不编造数字', () => {
    expect(studioVideoCostEstimate(undefined, 4)).toBe('无法估算')
    expect(studioVideoCostEstimate(videoModel({}), 4)).toBe('无法估算')
    expect(studioVideoCostEstimate(videoModel({ unitPrice: 0.1 }))).toBe('无法估算')
  })

  it('按所选秒数计算', () => {
    expect(studioVideoCostEstimate(videoModel({ unitPrice: 0.1 }), 4)).toBe('$0.400000')
    expect(studioVideoCostEstimate(videoModel({ unitPrice: 0.1 }), '8')).toBe('$0.800000')
    expect(studioVideoCostEstimate(videoModel({ unitPrice: 0.1 }), '4s')).toBe('无法估算')
  })
})

describe('studioVideoDisplay 产物状态文案', () => {
  it('settled 前不显示成品；产物悬空按缺失渲染', () => {
    expect(videoTaskArtifactLabel('pending')).toBe('生成中')
    expect(videoTaskArtifactLabel('ready')).toBe('可播放')
    expect(videoTaskArtifactLabel('missing')).toBe('产物缺失')
    expect(videoTaskArtifactLabel('failed')).toBe('生成失败')
    expect(videoTaskArtifactLabel('stopped')).toBe('已停止')
  })
})

describe('studioVideoDisplay 任务排序', () => {
  it('草稿卡 → 进行中 → 未结算 → 终态 置顶顺序', () => {
    const task = (overrides: Partial<StudioTask>): StudioTask => ({
      id: 'stask_1',
      kind: 'video',
      status: 'completed',
      settlement: 'settled',
      model: 'veo-3',
      promptSummary: 'x',
      clientOperationId: 'op',
      createdAt: '2026-10-12T00:00:00.000Z',
      ...overrides
    })
    const sorted = sortVideoTasksForDisplay([
      task({ id: 'done', status: 'completed', settlement: 'settled' }),
      task({ id: 'pending-x', status: 'queued' }),
      task({ id: 'running', status: 'in_progress' }),
      task({ id: 'unsettled', status: 'completed', settlement: 'pending_settled' })
    ])
    expect(sorted.map((item) => item.id)).toEqual(['pending-x', 'running', 'unsettled', 'done'])
  })
})
