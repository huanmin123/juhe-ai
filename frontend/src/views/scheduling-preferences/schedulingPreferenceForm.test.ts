import { describe, expect, it } from 'vitest'

import type { RouteStrategySpeedFirstConfig, SchedulingPreferenceMutationResult, SchedulingPreferenceSummary } from '@/types/domain'
import {
  buildSchedulingPreferenceFormPayload,
  buildSchedulingPreferenceMutationPatch,
  boundedSpeedFirstParam,
  defaultSpeedFirstParamsForm,
  extractSchedulingPreferenceReferenceCount,
  hasSchedulingPreferenceMutationChanges,
  mergeSchedulingPreferenceMutationResult,
  schedulingPreferenceSpeedFirstBounds,
  speedFirstParamsFormFromConfig,
  type SchedulingPreferenceFormPayload
} from './schedulingPreferenceForm'

const speedFirstForm = defaultSpeedFirstParamsForm()

function costFirstPayload(overrides: Partial<SchedulingPreferenceFormPayload> = {}): SchedulingPreferenceFormPayload {
  return { name: '偏好 A', chatMode: 'cost_first', enabled: true, ...overrides }
}

function speedFirstPayload(overrides: Partial<SchedulingPreferenceFormPayload> = {}): SchedulingPreferenceFormPayload {
  return {
    name: '偏好 A',
    chatMode: 'speed_first',
    enabled: true,
    chatSpeedFirstConfig: buildSchedulingPreferenceFormPayload({
      name: '偏好 A',
      chatMode: 'speed_first',
      enabled: true,
      speedFirst: speedFirstForm
    }).chatSpeedFirstConfig,
    ...overrides
  }
}

describe('九字段范围与默认值', () => {
  it('默认值与契约表一致', () => {
    expect(defaultSpeedFirstParamsForm()).toEqual({
      firstByteDeadlineMs: 30000,
      slowTriggerCount: 3,
      slowWindowSeconds: 120,
      recoverySuccessCount: 3,
      probeIntervalSeconds: 30,
      degradedTtlSeconds: 300,
      maxFirstByteRetriesPerRequest: 2,
      totalTimeDeadlineSeconds: 120,
      compactionTotalTimeDeadlineSeconds: 300
    })
  })

  it('boundedSpeedFirstParam 收敛越界值并回落非法输入', () => {
    expect(boundedSpeedFirstParam('firstByteDeadlineMs', 5000)).toBe(schedulingPreferenceSpeedFirstBounds.firstByteDeadlineMs.min)
    expect(boundedSpeedFirstParam('firstByteDeadlineMs', 99999)).toBe(schedulingPreferenceSpeedFirstBounds.firstByteDeadlineMs.max)
    expect(boundedSpeedFirstParam('slowTriggerCount', 1)).toBe(schedulingPreferenceSpeedFirstBounds.slowTriggerCount.min)
    expect(boundedSpeedFirstParam('compactionTotalTimeDeadlineSeconds', 1200)).toBe(schedulingPreferenceSpeedFirstBounds.compactionTotalTimeDeadlineSeconds.max)
    expect(boundedSpeedFirstParam('probeIntervalSeconds', 'abc')).toBe(schedulingPreferenceSpeedFirstBounds.probeIntervalSeconds.fallback)
    expect(boundedSpeedFirstParam('slowWindowSeconds', 90.6)).toBe(91)
  })

  it('speedFirstParamsFormFromConfig 对缺省与非法字段回落默认或收敛范围', () => {
    expect(speedFirstParamsFormFromConfig(undefined)).toEqual(defaultSpeedFirstParamsForm())
    const form = speedFirstParamsFormFromConfig({
      firstByteDeadlineMs: 45000,
      speedFirstConfig: {
        ...fullSpeedFirstConfig(),
        slowTriggerCount: 5,
        totalTimeDeadlineSeconds: undefined,
        compactionTotalTimeDeadlineSeconds: undefined
      }
    })
    expect(form.firstByteDeadlineMs).toBe(45000)
    expect(form.slowTriggerCount).toBe(5)
    expect(form.totalTimeDeadlineSeconds).toBe(schedulingPreferenceSpeedFirstBounds.totalTimeDeadlineSeconds.fallback)
    expect(form.compactionTotalTimeDeadlineSeconds).toBe(schedulingPreferenceSpeedFirstBounds.compactionTotalTimeDeadlineSeconds.fallback)
  })
})

describe('buildSchedulingPreferenceFormPayload', () => {
  it('cost_first 不携带 chatSpeedFirstConfig', () => {
    const payload = buildSchedulingPreferenceFormPayload({ name: ' A ', chatMode: 'cost_first', enabled: true, speedFirst: speedFirstForm })
    expect(payload).toEqual({ name: 'A', chatMode: 'cost_first', enabled: true })
  })

  it('speed_first 携带九字段全量配置并收敛范围', () => {
    const payload = buildSchedulingPreferenceFormPayload({
      name: 'B',
      chatMode: 'speed_first',
      enabled: false,
      speedFirst: { ...speedFirstForm, firstByteDeadlineMs: 99999, slowTriggerCount: 1 }
    })
    expect(payload.chatSpeedFirstConfig?.firstByteDeadlineMs).toBe(schedulingPreferenceSpeedFirstBounds.firstByteDeadlineMs.max)
    expect(payload.chatSpeedFirstConfig?.speedFirstConfig.slowTriggerCount).toBe(schedulingPreferenceSpeedFirstBounds.slowTriggerCount.min)
    expect(Object.keys(payload.chatSpeedFirstConfig?.speedFirstConfig ?? {})).toHaveLength(8)
  })
})

describe('buildSchedulingPreferenceMutationPatch', () => {
  it('未修改时生成空 delta', () => {
    const current = speedFirstPayload()
    expect(buildSchedulingPreferenceMutationPatch(current, { ...current })).toEqual({})
    expect(hasSchedulingPreferenceMutationChanges(buildSchedulingPreferenceMutationPatch(current, { ...current }))).toBe(false)
  })

  it('只改名称时只发送 name', () => {
    expect(buildSchedulingPreferenceMutationPatch(speedFirstPayload(), speedFirstPayload({ name: '新名' })))
      .toEqual({ name: '新名' })
  })

  it('cost_first 切 speed_first 时发送 chatMode 与完整配置', () => {
    const patch = buildSchedulingPreferenceMutationPatch(costFirstPayload(), speedFirstPayload())
    expect(Object.prototype.hasOwnProperty.call(patch, 'chatMode')).toBe(true)
    expect(patch.chatSpeedFirstConfig?.firstByteDeadlineMs).toBe(30000)
    expect(patch.chatSpeedFirstConfig?.speedFirstConfig).toBeDefined()
  })

  it('speed_first 切 cost_first 时不携带 chatSpeedFirstConfig', () => {
    const patch = buildSchedulingPreferenceMutationPatch(speedFirstPayload(), costFirstPayload())
    expect(patch).toEqual({ chatMode: 'cost_first' })
  })

  it('停用切换只发送 enabled', () => {
    expect(buildSchedulingPreferenceMutationPatch(costFirstPayload(), costFirstPayload({ enabled: false })))
      .toEqual({ enabled: false })
  })

  it('配置值变化发送整体 chatSpeedFirstConfig', () => {
    const current = speedFirstPayload()
    const changed = speedFirstPayload({
      chatSpeedFirstConfig: {
        firstByteDeadlineMs: 20000,
        speedFirstConfig: { ...speedFirstFormConfigOf(current), slowTriggerCount: 4 }
      }
    })
    const patch = buildSchedulingPreferenceMutationPatch(current, changed)
    expect(patch.chatSpeedFirstConfig?.firstByteDeadlineMs).toBe(20000)
    expect(patch.chatSpeedFirstConfig?.speedFirstConfig.slowTriggerCount).toBe(4)
  })
})

describe('mergeSchedulingPreferenceMutationResult', () => {
  const row: SchedulingPreferenceSummary = {
    id: 'sp-1',
    name: '旧名',
    chatMode: 'cost_first',
    builtIn: false,
    enabled: true,
    boundApiKeyCount: 2,
    updatedAt: '2026-01-01T00:00:00Z'
  }

  it('应用 rowPatch 并推进 updatedAt', () => {
    const result: SchedulingPreferenceMutationResult = {
      id: 'sp-1',
      changedFields: ['name', 'enabled'],
      rowPatch: { name: '新名', enabled: false },
      updatedAt: '2026-02-02T00:00:00Z'
    }
    const merged = mergeSchedulingPreferenceMutationResult(row, result)
    expect(merged.name).toBe('新名')
    expect(merged.enabled).toBe(false)
    expect(merged.updatedAt).toBe('2026-02-02T00:00:00Z')
    expect(merged.chatMode).toBe('cost_first')
  })

  it('切换 cost_first 时清空速度配置', () => {
    const speedRow: SchedulingPreferenceSummary = {
      ...row,
      chatMode: 'speed_first',
      chatSpeedFirstConfig: { firstByteDeadlineMs: 30000, speedFirstConfig: fullSpeedFirstConfig() }
    }
    const merged = mergeSchedulingPreferenceMutationResult(speedRow, {
      id: 'sp-1',
      changedFields: ['chatMode'],
      rowPatch: { chatMode: 'cost_first' },
      updatedAt: '2026-03-03T00:00:00Z'
    })
    expect(merged.chatMode).toBe('cost_first')
    expect(merged.chatSpeedFirstConfig).toBeUndefined()
  })

  it('rowPatch null chatSpeedFirstConfig 归一为缺失', () => {
    const speedRow: SchedulingPreferenceSummary = {
      ...row,
      chatMode: 'speed_first',
      chatSpeedFirstConfig: { firstByteDeadlineMs: 30000, speedFirstConfig: fullSpeedFirstConfig() }
    }
    const merged = mergeSchedulingPreferenceMutationResult(speedRow, {
      id: 'sp-1',
      changedFields: [],
      rowPatch: { chatSpeedFirstConfig: null },
      updatedAt: '2026-04-04T00:00:00Z'
    })
    expect(merged.chatSpeedFirstConfig).toBeUndefined()
  })

  it('id 不同时不合并', () => {
    const merged = mergeSchedulingPreferenceMutationResult(row, {
      id: 'sp-other',
      changedFields: ['name'],
      rowPatch: { name: '别的行' },
      updatedAt: '2026-05-05T00:00:00Z'
    })
    expect(merged.name).toBe('旧名')
  })
})

describe('extractSchedulingPreferenceReferenceCount', () => {
  it('从 axios 错误响应体提取 referenceCount', () => {
    const error = { response: { data: { message: '调度偏好仍被 API Key 绑定，不能删除', referenceCount: 3 } } }
    expect(extractSchedulingPreferenceReferenceCount(error)).toBe(3)
  })

  it('非 409 场景或非法值返回 undefined', () => {
    expect(extractSchedulingPreferenceReferenceCount(new Error('boom'))).toBeUndefined()
    expect(extractSchedulingPreferenceReferenceCount({ response: { data: { referenceCount: 'x' } } })).toBeUndefined()
    expect(extractSchedulingPreferenceReferenceCount(undefined)).toBeUndefined()
  })
})

function speedFirstFormConfigOf(payload: SchedulingPreferenceFormPayload) {
  return payload.chatSpeedFirstConfig?.speedFirstConfig ?? defaultSpeedFirstParamsForm()
}

function fullSpeedFirstConfig(): RouteStrategySpeedFirstConfig {
  return {
    slowTriggerCount: 3,
    slowWindowSeconds: 120,
    recoverySuccessCount: 3,
    probeIntervalSeconds: 30,
    degradedTtlSeconds: 300,
    maxFirstByteRetriesPerRequest: 2,
    totalTimeDeadlineSeconds: 120,
    compactionTotalTimeDeadlineSeconds: 300
  }
}
