import { ChatGenerationRegistry } from './chat-generation-registry.js'

export const chatGenerationRegistry = new ChatGenerationRegistry()

export const shutdownChatGenerationRegistry = (options: { timeoutMs: number }): Promise<void> => chatGenerationRegistry.shutdown(options)

export const isActiveChatGeneration = (ownerId: string, conversationId: string, turnId: string): boolean => Boolean(
  chatGenerationRegistry.get({ ownerId, conversationId, turnId })
)
