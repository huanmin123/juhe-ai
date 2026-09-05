const maxPersistedChatContentBlocksBytes = 192 * 1024

export type ChatPersistedTerminalStatus = 'completed' | 'failed' | 'canceled'

export function sanitizeChatContentBlocksForPersistence<T>(blocks: T[]): T[] {
  try {
    return Buffer.byteLength(JSON.stringify(blocks), 'utf8') <= maxPersistedChatContentBlocksBytes ? blocks : []
  } catch {
    return []
  }
}

export function terminalizeChatContentBlocksForPersistence<T extends object>(
  blocks: T[],
  status: ChatPersistedTerminalStatus
): T[] {
  return sanitizeChatContentBlocksForPersistence(blocks.map((block) => {
    const record = block as { type?: unknown; status?: unknown }
    const type = record.type
    const blockStatus = record.status
    if (!['reasoning', 'tool_call', 'output_image'].includes(String(type))) return block
    if (blockStatus !== 'started' && blockStatus !== 'updated') return block
    return { ...block, status } as T
  }))
}
