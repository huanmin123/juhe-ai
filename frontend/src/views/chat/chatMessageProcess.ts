import type { ChatMessage, ChatToolEvent } from '@/types/domain/chat'

export function projectChatMessageProcess(message: ChatMessage): { reasoningText: string; toolEvents: ChatToolEvent[] } {
  const reasoningText = message.reasoningText ?? (message.contentBlocks ?? [])
    .filter((block): block is Extract<NonNullable<ChatMessage['contentBlocks']>[number], { type: 'reasoning' }> => block.type === 'reasoning')
    .map((block) => block.text).join('\n')
  const persistedTools = (message.contentBlocks ?? [])
    .filter((block): block is Extract<NonNullable<ChatMessage['contentBlocks']>[number], { type: 'tool_call' }> => block.type === 'tool_call')
    .map((block) => ({ id: block.id, type: block.toolType, status: block.status, item: block.item }))
  return { reasoningText, toolEvents: message.toolEvents?.length ? message.toolEvents : persistedTools }
}
