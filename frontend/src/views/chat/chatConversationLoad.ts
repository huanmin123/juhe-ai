export function isCurrentChatConversationLoad(input: {
  conversationId: string
  selectedConversationId?: string
  epoch: number
  currentEpoch: number
  disposed: boolean
}): boolean {
  return !input.disposed
    && input.epoch === input.currentEpoch
    && input.conversationId === input.selectedConversationId
}
