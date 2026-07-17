import type { ChatConversation } from '@/types/domain/chat'

export function mergeChatConversationSummary(current: ChatConversation | undefined, incoming: ChatConversation): ChatConversation {
  return current ? { ...incoming, isPinned: current.isPinned } : incoming
}

export function createChatConversationSummaryRefresher(input: {
  load: (conversationId: string) => Promise<ChatConversation>
  apply: (conversation: ChatConversation) => void
}): (conversationId: string) => Promise<ChatConversation | undefined> {
  const versions = new Map<string, number>()
  return async (conversationId) => {
    const version = (versions.get(conversationId) ?? 0) + 1
    versions.set(conversationId, version)
    const conversation = await input.load(conversationId)
    if (versions.get(conversationId) !== version) return undefined
    input.apply(conversation)
    return conversation
  }
}
