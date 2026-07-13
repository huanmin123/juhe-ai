export function deleteActiveChatStreamIfMatches<T extends { turnId: string }>(
  streams: Map<string, T>,
  conversationId: string,
  turnId: string
): boolean {
  if (streams.get(conversationId)?.turnId !== turnId) return false
  return streams.delete(conversationId)
}
