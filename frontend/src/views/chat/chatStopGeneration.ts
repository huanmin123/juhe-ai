export interface ChatStopTarget {
  conversationId: string
  clientMessageId: string
  turnId?: string
}

// 决定 runtime 回放轮次（applyRuntimeTurn）到达时是否允许重建 activeStopTarget。
// 每次发送都会重新订阅 chatGenerationRuntime，subscribe 会把 map 里的旧终端轮
// （上一条已 completed 的轮）立即投递给新订阅者；若据此重建 stopTarget，会把本轮
// 编辑请求（含 replaceTurnId/uiEpoch/lifecycleEpoch 的本地 requestContext）整体覆盖成
// 没有 replaceTurnId 的空壳，随后新轮被接受时 finishAcceptedTurnEdit 的
// replaceTurnId 守卫必然拒绝，编辑态永久卡死在 submitting。因此本地在途请求
// （有 lifecycleEpoch，见 ChatView.vue sendMessage 的 requestContext 构造）一律保留，
// 不被旧轮次回放覆盖；仅无 active（attach/恢复场景）、active 属于其他会话，或
// active 同会话但不是本地发起（lifecycleEpoch 为 undefined）时才允许重建。
export function shouldRebuildChatStopTarget(input: {
  active?: {
    conversationId: string
    clientMessageId: string
    lifecycleEpoch?: number
  }
  conversationId: string
  clientMessageId: string
}): boolean {
  const active = input.active
  if (!active || active.conversationId !== input.conversationId) return true
  if (active.clientMessageId === input.clientMessageId) return false
  return active.lifecycleEpoch === undefined
}

export function resolveChatStopTarget(input: {
  selectedConversationId?: string
  active?: ChatStopTarget
  pending?: ChatStopTarget
}): ChatStopTarget | undefined {
  const target = input.active ?? input.pending
  return target?.conversationId === input.selectedConversationId ? target : undefined
}

export async function stopActiveChatGeneration(input: {
  controller?: AbortController
  stop: () => Promise<unknown>
  sendSettled?: Promise<unknown>
}): Promise<void> {
  input.controller?.abort()
  const [stopResult] = await Promise.allSettled([input.stop(), input.sendSettled ?? Promise.resolve()])
  if (stopResult.status === 'rejected') throw stopResult.reason
}
