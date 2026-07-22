import type { ChatMessage, ChatMessageContentBlock } from '@/types/domain/chat'
import type { JSONContent } from '@tiptap/core'
import { maxChatImageCount } from './composer/chatImageSelection'

export interface ChatTurnEditCandidate {
  conversationId: string
  turnId: string
  userMessageId: string
  assistantMessageId: string
  content: string
  contentBlocks: Array<{ type: 'input_text'; text: string } | { type: 'input_image'; assetId: string }>
}

export interface ChatTurnRetryCandidate extends ChatTurnEditCandidate {
  assistantStatus: 'failed' | 'canceled'
  model: string
  replaceTurnId?: string
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

export function beginLatestTurnRetry(messages: readonly ChatMessage[]): ChatTurnRetryCandidate | undefined {
  if (messages.length < 2) return undefined
  const userMessage = messages[messages.length - 2]
  const assistantMessage = messages[messages.length - 1]
  if (!userMessage || !assistantMessage || userMessage.role !== 'user' || assistantMessage.role !== 'assistant') return undefined
  if (assistantMessage.status !== 'failed' && assistantMessage.status !== 'canceled') return undefined
  if (isUnacceptedOptimisticPair(userMessage, assistantMessage)) {
    const contentBlocks = strictInputBlocks(userMessage.contentBlocks)
    if (!contentBlocks?.length) return undefined
    return {
      conversationId: userMessage.conversationId,
      turnId: userMessage.turnId,
      userMessageId: userMessage.id,
      assistantMessageId: assistantMessage.id,
      content: userMessage.contentText,
      contentBlocks,
      assistantStatus: assistantMessage.status,
      model: userMessage.model || assistantMessage.model
    }
  }
  const editable = beginLatestTurnEdit(messages, userMessage.id)
  if (!editable) return undefined
  return { ...editable, assistantStatus: assistantMessage.status, model: userMessage.model || assistantMessage.model, replaceTurnId: editable.turnId }
}

function isUnacceptedOptimisticPair(userMessage: ChatMessage, assistantMessage: ChatMessage): boolean {
  const clientMessageId = userMessage.clientMessageId
  return Boolean(clientMessageId
    && assistantMessage.clientMessageId === clientMessageId
    && userMessage.conversationId === assistantMessage.conversationId
    && userMessage.turnId === `optimistic-turn:${clientMessageId}`
    && assistantMessage.turnId === userMessage.turnId
    && userMessage.id === `optimistic-user:${clientMessageId}`
    && assistantMessage.id === `optimistic-assistant:${clientMessageId}`
    && userMessage.sequenceNo === 0
    && assistantMessage.sequenceNo === 0
    && userMessage.status === 'completed')
}

export function restoreChatMessagesAfterRejectedReplacement(input: {
  messages: readonly ChatMessage[]
  clientMessageId: string
  originalMessages: readonly ChatMessage[]
}): ChatMessage[] {
  const optimisticTurnId = `optimistic-turn:${input.clientMessageId}`
  const originalIds = new Set(input.originalMessages.map((item) => item.id))
  return [
    ...input.messages.filter((item) => item.turnId !== optimisticTurnId && item.clientMessageId !== input.clientMessageId && !originalIds.has(item.id)),
    ...input.originalMessages
  ].sort((left, right) => left.sequenceNo - right.sequenceNo || left.id.localeCompare(right.id))
}

export function removeInvalidatedGeneratedAssetsFromDraft(input: {
  snapshot: JSONContent
  replacedAssistantBlocks: readonly ChatMessageContentBlock[] | undefined
  submittedBlocks: readonly ChatTurnEditCandidate['contentBlocks'][number][]
}): { snapshot: JSONContent; removedCount: number } {
  const retainedAssetIds = new Set(input.submittedBlocks
    .filter((block) => block.type === 'input_image')
    .map((block) => block.assetId))
  const invalidatedAssetIds = new Set<string>()
  for (const block of input.replacedAssistantBlocks ?? []) {
    if (block.type === 'output_image' && !retainedAssetIds.has(block.assetId)) invalidatedAssetIds.add(block.assetId)
  }
  if (invalidatedAssetIds.size === 0) return { snapshot: input.snapshot, removedCount: 0 }

  let removedCount = 0
  const prune = (node: JSONContent): JSONContent | undefined => {
    if (node.type === 'chatImageAttachment' && invalidatedAssetIds.has(String(node.attrs?.assetId ?? ''))) {
      removedCount += 1
      return undefined
    }
    const next: JSONContent = { ...node }
    if (node.content) {
      const content = node.content.map(prune).filter((child): child is JSONContent => Boolean(child))
      if (content.length > 0) next.content = content
      else delete next.content
    }
    return next
  }
  const snapshot = prune(input.snapshot) ?? { type: 'doc' }
  if (snapshot.type === 'doc' && !snapshot.content?.length) snapshot.content = [{ type: 'paragraph' }]
  return { snapshot, removedCount }
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
