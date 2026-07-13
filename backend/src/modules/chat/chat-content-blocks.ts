const maxPersistedChatContentBlocksBytes = 192 * 1024

export function sanitizeChatContentBlocksForPersistence<T>(blocks: T[]): T[] {
  try {
    return Buffer.byteLength(JSON.stringify(blocks), 'utf8') <= maxPersistedChatContentBlocksBytes ? blocks : []
  } catch {
    return []
  }
}
