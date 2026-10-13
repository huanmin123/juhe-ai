import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { studioApi } from '@/api/domains/studio'
import type { StudioTask } from '@/types/domain'
import { isStudioTaskFinalSnapshot, isStudioTaskPending, studioTaskArtifactState } from './studioTaskState'
import { startStudioTaskPolling, type StudioTaskPollingDependencies } from './useStudioTaskPolling'

vi.mock('@/api/domains/studio', () => ({
  studioApi: { getTask: vi.fn() }
}))

const getTaskMock = vi.mocked(studioApi.getTask)

function studioTask(overrides: Partial<StudioTask> = {}): StudioTask {
  return {
    id: 'stask_1',
    kind: 'image',
    status: 'in_progress',
    settlement: 'pending_settled',
    model: 'gpt-image-1',
    promptSummary: '一只猫',
    clientOperationId: 'op-1',
    createdAt: '2026-10-12T00:00:00.000Z',
    updatedAt: '2026-10-12T00:00:00.000Z',
    ...overrides
  }
}

function buildDeps(tasks: StudioTask[], patches: StudioTask[], scope = 'images'): StudioTaskPollingDependencies {
  return {
    scopeId: () => scope,
    tasks: () => tasks,
    applySnapshot: (task) => patches.push(task)
  }
}

describe('studioTaskState 展示态判定', () => {
  it('queued/in_progress 与 completed 未结算都算待轮询', () => {
    expect(isStudioTaskPending(studioTask({ status: 'queued' }))).toBe(true)
    expect(isStudioTaskPending(studioTask({ status: 'in_progress' }))).toBe(true)
    expect(isStudioTaskPending(studioTask({ status: 'completed', settlement: 'pending_settled' }))).toBe(true)
    expect(isStudioTaskPending(studioTask({ status: 'completed', settlement: 'failed' }))).toBe(true)
    expect(isStudioTaskPending(studioTask({ status: 'completed', settlement: 'settled', assetId: 'a1' }))).toBe(false)
    expect(isStudioTaskPending(studioTask({ status: 'failed', error: { code: 'x' } }))).toBe(false)
    expect(isStudioTaskPending(studioTask({ status: 'cancelled' }))).toBe(false)
    expect(isStudioTaskPending(studioTask({ status: 'expired' }))).toBe(false)
  })

  it('残缺终态快照不落定：completed 未结算、failed 无 error', () => {
    expect(isStudioTaskFinalSnapshot(studioTask({ status: 'completed', settlement: 'pending_settled' }))).toBe(false)
    expect(isStudioTaskFinalSnapshot(studioTask({ status: 'failed' }))).toBe(false)
    expect(isStudioTaskFinalSnapshot(studioTask({ status: 'completed', settlement: 'settled', assetId: null }))).toBe(true)
    expect(isStudioTaskFinalSnapshot(studioTask({ status: 'failed', error: { code: 'studio_upstream_failed' } }))).toBe(true)
    expect(isStudioTaskFinalSnapshot(studioTask({ status: 'cancelled' }))).toBe(true)
    expect(isStudioTaskFinalSnapshot(studioTask({ status: 'expired' }))).toBe(true)
  })

  it('产物状态：settled 前 pending、settled 缺 assetId 为产物缺失', () => {
    expect(studioTaskArtifactState(studioTask({ status: 'in_progress' }))).toBe('pending')
    expect(studioTaskArtifactState(studioTask({ status: 'completed', settlement: 'settled', assetId: 'a1' }))).toBe('ready')
    expect(studioTaskArtifactState(studioTask({ status: 'completed', settlement: 'settled', assetId: null }))).toBe('missing')
    expect(studioTaskArtifactState(studioTask({ status: 'failed', error: { code: 'x' } }))).toBe('failed')
    expect(studioTaskArtifactState(studioTask({ status: 'cancelled' }))).toBe('stopped')
  })
})

describe('useStudioTaskPolling 轮询调度', () => {
  let tasks: StudioTask[]
  let patches: StudioTask[]

  beforeEach(() => {
    vi.useFakeTimers()
    getTaskMock.mockReset()
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' })
    patches = []
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('5s 首轮轮询并应用快照，终态后不再轮询', async () => {
    tasks = [studioTask()]
    getTaskMock.mockResolvedValueOnce(studioTask({ status: 'completed', settlement: 'settled', assetId: 'stasset_1' }))
    const control = startStudioTaskPolling(buildDeps(tasks, patches))
    await vi.advanceTimersByTimeAsync(5_000)
    expect(getTaskMock).toHaveBeenCalledWith('stask_1')
    expect(patches).toEqual([studioTask({ status: 'completed', settlement: 'settled', assetId: 'stasset_1' })])
    // 上层应用快照后任务已终态：下一轮无待轮询任务，不再发请求（原地替换，依赖闭包读同一数组）。
    tasks.splice(0, tasks.length, studioTask({ status: 'completed', settlement: 'settled', assetId: 'stasset_1' }))
    getTaskMock.mockClear()
    await vi.advanceTimersByTimeAsync(5_000)
    expect(getTaskMock).not.toHaveBeenCalled()
    control.stop()
  })

  it('页面不可见跳过且不计失败，回到可见立即补一轮', async () => {
    tasks = [studioTask()]
    getTaskMock.mockResolvedValue(studioTask({ status: 'in_progress' }))
    const control = startStudioTaskPolling(buildDeps(tasks, patches))
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' })
    await vi.advanceTimersByTimeAsync(12_000)
    expect(getTaskMock).not.toHaveBeenCalled()
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' })
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(0)
    expect(getTaskMock).toHaveBeenCalledTimes(1)
    control.stop()
  })

  it('连续失败按 5s→10s→20s→30s 封顶退避，成功后回到基础间隔', async () => {
    tasks = [studioTask()]
    getTaskMock.mockRejectedValueOnce(new Error('network'))
    getTaskMock.mockRejectedValueOnce(new Error('network'))
    getTaskMock.mockRejectedValueOnce(new Error('network'))
    getTaskMock.mockRejectedValueOnce(new Error('network'))
    getTaskMock.mockResolvedValue(studioTask({ status: 'in_progress' }))
    const control = startStudioTaskPolling(buildDeps(tasks, patches))
    await vi.advanceTimersByTimeAsync(5_000)
    expect(getTaskMock).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(5_000)
    expect(getTaskMock).toHaveBeenCalledTimes(1) // 退避 10s：+5s 未到点
    await vi.advanceTimersByTimeAsync(5_000)
    expect(getTaskMock).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(20_000)
    expect(getTaskMock).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(30_000) // 失败 3 次 → 40s 封顶到 30s，到点第 4 次（仍失败）
    expect(getTaskMock).toHaveBeenCalledTimes(4)
    await vi.advanceTimersByTimeAsync(30_000) // 第 5 次到点（mock 已切成功）→ 计数清零
    expect(getTaskMock).toHaveBeenCalledTimes(5)
    await vi.advanceTimersByTimeAsync(5_000) // 成功后回到基础 5s 间隔
    expect(getTaskMock).toHaveBeenCalledTimes(6)
    control.stop()
  })

  it('作用域切换后的迟到响应丢弃', async () => {
    tasks = [studioTask()]
    let scope = 'images'
    const deps = buildDeps(tasks, patches)
    deps.scopeId = () => scope
    let resolvePoll: ((task: StudioTask) => void) | undefined
    getTaskMock.mockImplementationOnce(() => new Promise((resolve) => { resolvePoll = resolve }))
    const control = startStudioTaskPolling(deps)
    await vi.advanceTimersByTimeAsync(5_000)
    scope = 'video'
    resolvePoll?.(studioTask({ status: 'completed', settlement: 'settled', assetId: 'stasset_9' }))
    await Promise.resolve()
    expect(patches).toEqual([])
    control.stop()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('残缺终态快照不落定并继续轮询重试', async () => {
    tasks = [studioTask()]
    const control = startStudioTaskPolling(buildDeps(tasks, patches))
    // 第一轮：上游 completed 但结算未落（无 assetId）→ 不落定。
    getTaskMock.mockResolvedValueOnce(studioTask({ status: 'completed', settlement: 'pending_settled' }))
    await vi.advanceTimersByTimeAsync(5_000)
    expect(patches).toEqual([])
    // 第二轮：结算落定 → 落定应用。
    getTaskMock.mockResolvedValueOnce(studioTask({ status: 'completed', settlement: 'settled', assetId: 'stasset_1' }))
    await vi.advanceTimersByTimeAsync(5_000)
    expect(patches).toEqual([studioTask({ status: 'completed', settlement: 'settled', assetId: 'stasset_1' })])
    control.stop()
  })
})
