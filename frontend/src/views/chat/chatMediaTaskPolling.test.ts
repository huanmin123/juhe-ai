import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { collectPendingChatMediaTaskJobs, startChatMediaTaskPolling, type ChatMediaTaskBlock, type ChatMediaTaskBlockPatch } from './chatMediaTaskPolling'
import type { ChatMediaTaskSnapshot, ChatMessage } from '@/types/domain/chat'
import { chatApi } from '@/api/domains/chat'

vi.mock('@/api/domains/chat', () => ({
  chatApi: { mediaTask: vi.fn() }
}))

const mediaTaskMock = vi.mocked(chatApi.mediaTask)

function assistantMessage(id: string, blocks: ChatMessage['contentBlocks']): ChatMessage {
  return { id, conversationId: 'c1', turnId: `t-${id}`, sequenceNo: 2, role: 'assistant', status: 'completed', contentText: '', model: 'm', createdAt: '2026-10-04T00:00:00.000Z', expiresAt: '2026-10-08T00:00:00.000Z', contentBlocks: blocks }
}

function taskBlock(jobId: string, overrides: Partial<ChatMediaTaskBlock> = {}): ChatMediaTaskBlock {
  return { type: 'output_media_task', blockId: `block-${jobId}`, order: 1, jobId, kind: 'video', status: 'in_progress', ...overrides }
}

describe('chatMediaTaskPolling 未终态任务收集', () => {
  it('只收集 queued/in_progress 且未结算 assetId 的任务块，并按 jobId 去重', () => {
    const messages = [
      assistantMessage('m1', [taskBlock('job_a'), { type: 'output_media_task', blockId: 'b2', order: 2, jobId: 'job_done', kind: 'video', status: 'completed', assetId: 'asset_1' }]),
      assistantMessage('m2', [
        { type: 'output_media_task', blockId: 'b3', order: 1, jobId: 'job_failed', kind: 'video', status: 'failed', error: '视频生成失败' },
        taskBlock('job_a', { blockId: 'b4', status: 'queued', progress: 0 }),
        taskBlock('job_b')
      ])
    ]
    const jobs = collectPendingChatMediaTaskJobs(messages).map((block) => block.jobId)
    expect(jobs).toEqual(['job_a', 'job_b'])
  })
})

describe('chatMediaTaskPolling 轮询调度', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    mediaTaskMock.mockReset()
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' })
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('5s 首轮轮询并就地应用任务快照，终态后不再轮询', async () => {
    const messages = [assistantMessage('m1', [taskBlock('job_a')])]
    const patches: ChatMediaTaskBlockPatch[] = []
    // 镜像 ChatView 的 applyPatch：按 jobId 就地更新消息块（终态后扫描不再命中）。
    const applyPatch = (patch: ChatMediaTaskBlockPatch): void => {
      patches.push(patch)
      for (const message of messages) {
        message.contentBlocks = (message.contentBlocks ?? []).map((block) => {
          if (block.type !== 'output_media_task' || block.jobId !== patch.jobId) return block
          return { ...block, status: patch.status, ...(patch.assetId ? { assetId: patch.assetId } : {}), ...(patch.progress !== undefined ? { progress: patch.progress } : {}), ...(patch.error ? { error: patch.error } : {}) }
        })
      }
    }
    mediaTaskMock.mockResolvedValueOnce({ jobId: 'job_a', kind: 'video', status: 'completed', assetId: 'asset_v', progress: 100 } satisfies ChatMediaTaskSnapshot)
    const control = startChatMediaTaskPolling({ conversationId: () => 'c1', messages: () => messages, applyPatch })
    await vi.advanceTimersByTimeAsync(5_000)
    expect(mediaTaskMock).toHaveBeenCalledWith('c1', 'job_a')
    expect(patches).toEqual([{ jobId: 'job_a', status: 'completed', progress: 100, assetId: 'asset_v' }])
    // 快照已把内存块置终态：下一轮扫描无待轮询任务，不再发请求。
    mediaTaskMock.mockClear()
    await vi.advanceTimersByTimeAsync(5_000)
    expect(mediaTaskMock).not.toHaveBeenCalled()
    control.stop()
  })

  it('页面不可见暂停轮询，回到可见立即补一轮', async () => {
    const messages = [assistantMessage('m1', [taskBlock('job_a')])]
    mediaTaskMock.mockResolvedValue({ jobId: 'job_a', kind: 'video', status: 'in_progress', progress: 42 } satisfies ChatMediaTaskSnapshot)
    const control = startChatMediaTaskPolling({ conversationId: () => 'c1', messages: () => messages, applyPatch: () => undefined })
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' })
    await vi.advanceTimersByTimeAsync(12_000)
    expect(mediaTaskMock).not.toHaveBeenCalled()
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' })
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(0)
    expect(mediaTaskMock).toHaveBeenCalledTimes(1)
    control.stop()
  })

  it('连续失败按 5s→10s 退避，成功后回到基础间隔', async () => {
    const messages = [assistantMessage('m1', [taskBlock('job_a')])]
    mediaTaskMock.mockRejectedValueOnce(new Error('network'))
    const control = startChatMediaTaskPolling({ conversationId: () => 'c1', messages: () => messages, applyPatch: () => undefined })
    await vi.advanceTimersByTimeAsync(5_000)
    expect(mediaTaskMock).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(5_000)
    expect(mediaTaskMock).toHaveBeenCalledTimes(1) // 退避到 10s：+5s 未到点
    mediaTaskMock.mockResolvedValueOnce({ jobId: 'job_a', kind: 'video', status: 'in_progress', progress: 10 } satisfies ChatMediaTaskSnapshot)
    await vi.advanceTimersByTimeAsync(5_000)
    expect(mediaTaskMock).toHaveBeenCalledTimes(2) // 10s 点触发
    control.stop()
  })

  it('会话切换后的迟到响应丢弃，stop 清理定时器与监听', async () => {
    const messages = [assistantMessage('m1', [taskBlock('job_a')])]
    let conversationId = 'c1'
    const patches: ChatMediaTaskBlockPatch[] = []
    let resolvePoll: ((value: ChatMediaTaskSnapshot) => void) | undefined
    mediaTaskMock.mockImplementationOnce(() => new Promise((resolve) => { resolvePoll = resolve }))
    const control = startChatMediaTaskPolling({ conversationId: () => conversationId, messages: () => messages, applyPatch: (patch) => patches.push(patch) })
    await vi.advanceTimersByTimeAsync(5_000)
    conversationId = 'c2'
    resolvePoll?.({ jobId: 'job_a', kind: 'video', status: 'completed', assetId: 'asset_v' })
    await Promise.resolve()
    expect(patches).toEqual([])
    control.stop()
    expect(vi.getTimerCount()).toBe(0)
  })
})
