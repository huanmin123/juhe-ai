export interface ChatStopTarget {
  conversationId: string
  clientMessageId: string
  turnId?: string
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
