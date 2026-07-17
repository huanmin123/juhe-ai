export function isChatTurnLimitReached(userTurnCount: number, userTurnLimit: number): boolean {
  return Number.isSafeInteger(userTurnCount)
    && userTurnCount >= 0
    && Number.isSafeInteger(userTurnLimit)
    && userTurnLimit > 0
    && userTurnCount >= userTurnLimit
}

export function canSubmitChatTurn(input: {
  userTurnCount: number
  userTurnLimit: number
  replaceTurnId?: string
}): boolean {
  return input.replaceTurnId !== undefined || !isChatTurnLimitReached(input.userTurnCount, input.userTurnLimit)
}

export function chatTurnLimitMessage(userTurnLimit: number): string {
  return `本会话已达到 ${userTurnLimit} 轮，请新建对话`
}

export function markChatConversationTurnLimitReached<T extends { userTurnCount: number; userTurnLimit: number }>(conversation: T): T {
  const currentCount = Number.isSafeInteger(conversation.userTurnCount) && conversation.userTurnCount >= 0
    ? conversation.userTurnCount
    : 0
  const reachedCount = Number.isSafeInteger(conversation.userTurnLimit) && conversation.userTurnLimit > 0
    ? Math.max(currentCount, conversation.userTurnLimit)
    : currentCount
  return { ...conversation, userTurnCount: reachedCount }
}
