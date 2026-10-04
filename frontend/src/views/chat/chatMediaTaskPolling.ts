import { chatApi } from '@/api/domains/chat'
import type { ChatMediaTaskSnapshot, ChatMediaTaskStatus, ChatMessage, ChatMessageContentBlock } from '@/types/domain/chat'

/**
 * 问答音视频工具任务轮询（问答音视频工具设计 §2.2/§5）：无后台常驻任务，前端对
 * 消息列表中未终态 output_media_task 块每 5s 轮询 GET media-tasks；响应就地替换
 * 块状态（assetId 到达 → 上层渲染播放器）。页面不可见（visibilityState）暂停；
 * 连续失败指数退避（5s→10s→20s→30s 封顶）；会话切换由调用方 stop/丢弃校验兜底。
 */
export interface ChatMediaTaskBlockPatch {
  jobId: string
  status: ChatMediaTaskStatus
  progress?: number
  assetId?: string
  error?: string
}

export interface ChatMediaTaskPollingControl {
  stop(): void
}

export interface ChatMediaTaskPollingDependencies {
  conversationId(): string | undefined
  messages(): ChatMessage[]
  applyPatch(patch: ChatMediaTaskBlockPatch): void
}

export type ChatMediaTaskBlock = Extract<ChatMessageContentBlock, { type: 'output_media_task' }>

/** 未终态任务块（status queued/in_progress 且尚未结算出 assetId）视为待轮询。 */
export function isPendingChatMediaTaskBlock(block: ChatMediaTaskBlock): boolean {
  return (block.status === 'queued' || block.status === 'in_progress') && !block.assetId
}

/** 扫描消息列表收集待轮询任务块（按 jobId 去重，保留最新消息里的块）。 */
export function collectPendingChatMediaTaskJobs(messages: ChatMessage[]): ChatMediaTaskBlock[] {
  const byJobId = new Map<string, ChatMediaTaskBlock>()
  for (const message of messages) {
    for (const block of message.contentBlocks ?? []) {
      if (block.type !== 'output_media_task' || !isPendingChatMediaTaskBlock(block)) continue
      if (block.jobId) byJobId.set(block.jobId, block)
    }
  }
  return [...byJobId.values()]
}

const baseIntervalMs = 5_000
const maxIntervalMs = 30_000

export function startChatMediaTaskPolling(deps: ChatMediaTaskPollingDependencies): ChatMediaTaskPollingControl {
  let stopped = false
  let timer: ReturnType<typeof setTimeout> | undefined
  let consecutiveFailures = 0
  let pollConversationId: string | undefined
  const inflight = new Set<string>()

  function schedule(delay: number): void {
    if (stopped) return
    timer = setTimeout(tick, delay)
  }

  async function tick(): Promise<void> {
    if (stopped) return
    timer = undefined
    // 页面不可见暂停：不发请求、不计失败，回到可见时立即补一轮。
    if (typeof document !== 'undefined' && document.visibilityState !== 'visible') {
      schedule(baseIntervalMs)
      return
    }
    const conversationId = deps.conversationId()
    if (!conversationId) {
      schedule(baseIntervalMs)
      return
    }
    if (conversationId !== pollConversationId) {
      pollConversationId = conversationId
      consecutiveFailures = 0
      inflight.clear()
    }
    const jobs = collectPendingChatMediaTaskJobs(deps.messages()).filter((block) => !inflight.has(block.jobId))
    if (jobs.length === 0) {
      schedule(baseIntervalMs)
      return
    }
    let anyFailure = false
    await Promise.all(jobs.map(async (block) => {
      inflight.add(block.jobId)
      try {
        const snapshot = await chatApi.mediaTask(conversationId, block.jobId)
        // 会话已切换的迟到响应丢弃（块状态由新会话自己的轮询推进）。
        if (!stopped && deps.conversationId() === conversationId) deps.applyPatch(patchOf(snapshot))
      } catch {
        anyFailure = true
      } finally {
        inflight.delete(block.jobId)
      }
    }))
    if (stopped) return
    consecutiveFailures = anyFailure ? consecutiveFailures + 1 : 0
    schedule(nextDelay(consecutiveFailures))
  }

  function nextDelay(failures: number): number {
    return Math.min(maxIntervalMs, baseIntervalMs * 2 ** Math.min(failures, 3))
  }

  function onVisibilityChange(): void {
    if (stopped || document.visibilityState !== 'visible') return
    if (timer !== undefined) { clearTimeout(timer); timer = undefined }
    void tick()
  }
  document.addEventListener('visibilitychange', onVisibilityChange)

  schedule(baseIntervalMs)
  return {
    stop() {
      stopped = true
      if (timer !== undefined) { clearTimeout(timer); timer = undefined }
      document.removeEventListener('visibilitychange', onVisibilityChange)
    }
  }
}

function patchOf(snapshot: ChatMediaTaskSnapshot): ChatMediaTaskBlockPatch {
  return {
    jobId: snapshot.jobId,
    status: snapshot.status,
    ...(typeof snapshot.progress === 'number' ? { progress: snapshot.progress } : {}),
    ...(snapshot.assetId ? { assetId: snapshot.assetId } : {}),
    ...(snapshot.error ? { error: snapshot.error } : {})
  }
}
