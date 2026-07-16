export function deleteActiveChatStreamIfMatches<T extends { turnId: string }>(
  streams: Map<string, T>,
  conversationId: string,
  turnId: string
): boolean {
  if (streams.get(conversationId)?.turnId !== turnId) return false
  return streams.delete(conversationId)
}

export interface ActiveChatPreparation {
  token: symbol
  ownerId: string
  clientMessageId: string
  controller: AbortController
  phase: 'preparing' | 'accepting'
}

export function claimActiveChatPreparation(
  preparations: Map<string, ActiveChatPreparation>,
  input: { conversationId: string; ownerId: string; clientMessageId: string }
): ActiveChatPreparation | undefined {
  const { conversationId } = input
  if (preparations.has(conversationId)) return undefined
  const claim: ActiveChatPreparation = {
    token: Symbol(conversationId),
    ownerId: input.ownerId,
    clientMessageId: input.clientMessageId,
    controller: new AbortController(),
    phase: 'preparing'
  }
  preparations.set(conversationId, claim)
  return claim
}

export function beginActiveChatAcceptance(
  preparations: Map<string, ActiveChatPreparation>,
  conversationId: string,
  token: symbol
): boolean {
  const current = preparations.get(conversationId)
  if (current?.token !== token || current.phase !== 'preparing' || current.controller.signal.aborted) return false
  current.phase = 'accepting'
  return true
}

export function cancelActiveChatPreparation(
  preparations: Map<string, ActiveChatPreparation>,
  input: { conversationId: string; ownerId: string; clientMessageId: string }
): ActiveChatPreparation['phase'] | undefined {
  const current = preparations.get(input.conversationId)
  if (current?.ownerId !== input.ownerId || current.clientMessageId !== input.clientMessageId) return undefined
  const phase = current.phase
  current.controller.abort()
  return phase
}

export function hasActiveChatPreparation(
  preparations: Map<string, ActiveChatPreparation>,
  input: { conversationId: string; ownerId: string; clientMessageId: string }
): boolean {
  const current = preparations.get(input.conversationId)
  return current?.ownerId === input.ownerId && current.clientMessageId === input.clientMessageId
}

export function deleteActiveChatPreparationIfMatches(
  preparations: Map<string, ActiveChatPreparation>,
  conversationId: string,
  token: symbol
): boolean {
  if (preparations.get(conversationId)?.token !== token) return false
  return preparations.delete(conversationId)
}
