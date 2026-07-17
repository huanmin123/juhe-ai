import type { ChatMessage, ChatMessageContentBlock } from '@/types/domain/chat'
import { maxChatImageCount } from './composer/chatImageSelection'

export interface ChatTurnEditCandidate {
  conversationId: string
  turnId: string
  userMessageId: string
  assistantMessageId: string
  content: string
  contentBlocks: Array<{ type: 'input_text'; text: string } | { type: 'input_image'; assetId: string }>
}

export interface ChatSubmitFailureResolution {
  restoreSubmittedDraft: boolean
  clearEditing: boolean
  pendingConfirmation?: boolean
}

export type ChatReconciliationNotice = 'none' | 'stopped' | 'failed' | 'pending' | 'transport_error'

export function isDefinitiveChatHttpRejection(input: { status: number; code?: string }): boolean {
  return input.status >= 400 && input.status < 500 && input.code !== 'chat_message_already_exists'
}

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
  if (userMessage.status !== 'completed' || !['completed', 'failed', 'canceled'].includes(assistantMessage.status)) return undefined
  const contentBlocks = strictInputBlocks(userMessage.contentBlocks)
  if (!contentBlocks?.length) return undefined
  return {
    conversationId: userMessage.conversationId,
    turnId: userMessage.turnId,
    userMessageId: userMessage.id,
    assistantMessageId: assistantMessage.id,
    content: userMessage.contentText,
    contentBlocks
  }
}

export function resolveChatSubmitFailure(input: {
  streamStarted: boolean
  accepted: boolean
  confirmed?: boolean
  replaceConflict: boolean
}): ChatSubmitFailureResolution {
  if (input.replaceConflict) return { restoreSubmittedDraft: true, clearEditing: true }
  if (input.confirmed === false) return { restoreSubmittedDraft: false, clearEditing: false, pendingConfirmation: true }
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

function strictInputBlocks(blocks: readonly ChatMessageContentBlock[] | undefined): ChatTurnEditCandidate['contentBlocks'] | undefined {
  if (!blocks?.length) return undefined
  const result: ChatTurnEditCandidate['contentBlocks'] = []
  let imageCount = 0
  for (let order = 0; order < blocks.length; order += 1) {
    const block = blocks[order]
    if (!block || (block.type !== 'input_text' && block.type !== 'input_image') || block.order !== order) return undefined
    if (block.type === 'input_text' && typeof block.text === 'string' && Object.keys(block).sort().join(',') === 'order,text,type') {
      result.push({ type: 'input_text', text: block.text })
      continue
    }
    if (block.type === 'input_image' && typeof block.assetId === 'string' && block.assetId && Object.keys(block).sort().join(',') === 'assetId,order,type') {
      imageCount += 1
      if (imageCount > maxChatImageCount) return undefined
      result.push({ type: 'input_image', assetId: block.assetId })
      continue
    }
    return undefined
  }
  return result
}
