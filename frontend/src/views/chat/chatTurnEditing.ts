import type { ChatMessage, ChatMessageContentBlock } from '@/types/domain/chat'

export interface ChatTurnEditCandidate {
  conversationId: string
  turnId: string
  userMessageId: string
  assistantMessageId: string
  content: string
}

export interface ChatSubmitFailureResolution {
  restoreSubmittedDraft: boolean
  clearEditing: boolean
}

export type ChatReconciliationNotice = 'none' | 'stopped' | 'failed' | 'pending' | 'transport_error'

export function isLatestEditableUserMessage(messages: readonly ChatMessage[], userMessageId: string): boolean {
  return beginLatestTurnEdit(messages, userMessageId) !== undefined
}

export function beginLatestTurnEdit(messages: readonly ChatMessage[], userMessageId: string): ChatTurnEditCandidate | undefined {
  if (messages.length < 2) return undefined
  const userMessage = messages[messages.length - 2]
  const assistantMessage = messages[messages.length - 1]
  if (!userMessage || !assistantMessage || userMessage.id !== userMessageId) return undefined
  if (userMessage.role !== 'user' || assistantMessage.role !== 'assistant') return undefined
  if (userMessage.turnId !== assistantMessage.turnId || userMessage.conversationId !== assistantMessage.conversationId) return undefined
  if (assistantMessage.sequenceNo !== userMessage.sequenceNo + 1) return undefined
  if (userMessage.status !== 'completed' || assistantMessage.status !== 'completed') return undefined
  if (!userMessage.contentText.trim() || !isStrictTextInputMarkers(userMessage.contentBlocks)) return undefined
  return {
    conversationId: userMessage.conversationId,
    turnId: userMessage.turnId,
    userMessageId: userMessage.id,
    assistantMessageId: assistantMessage.id,
    content: userMessage.contentText
  }
}

export function resolveChatSubmitFailure(input: {
  streamStarted: boolean
  accepted: boolean
  replaceConflict: boolean
}): ChatSubmitFailureResolution {
  if (input.replaceConflict) return { restoreSubmittedDraft: true, clearEditing: true }
  if (input.streamStarted || input.accepted) return { restoreSubmittedDraft: false, clearEditing: true }
  return { restoreSubmittedDraft: true, clearEditing: false }
}

export function resolveChatReconciliationNotice(input: {
  accepted: boolean
  assistantStatus?: ChatMessage['status']
  silent: boolean
}): ChatReconciliationNotice {
  if (!input.accepted) return input.silent ? 'none' : 'transport_error'
  if (input.assistantStatus === 'completed') return 'none'
  if (!input.assistantStatus || input.assistantStatus === 'streaming') return 'pending'
  if (input.silent) return 'none'
  return input.assistantStatus === 'failed' ? 'failed' : 'stopped'
}

function isStrictTextInputMarkers(blocks: readonly ChatMessageContentBlock[] | undefined): boolean {
  if (!blocks?.length) return false
  return blocks.every((block, order) => {
    if (block.type !== 'input_marker' || block.inputType !== 'input_text' || block.order !== order) return false
    return Object.keys(block).sort().join(',') === 'inputType,order,type'
  })
}
