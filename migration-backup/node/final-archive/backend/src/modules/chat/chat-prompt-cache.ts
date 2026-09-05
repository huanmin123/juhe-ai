import { createHash } from 'node:crypto'

export function buildChatPromptCacheKey(input: {
  systemAccountId: string
  apiKeyId: string
  conversationId: string
}): string {
  return createHash('sha256')
    .update(JSON.stringify([
      'juhe-ai-chat-prompt-cache-v1',
      input.systemAccountId,
      input.apiKeyId,
      input.conversationId
    ]))
    .digest('base64url')
}
