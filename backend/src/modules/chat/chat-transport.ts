import type { ChatReasoningEffort, ChatServiceTier } from './chat-model-options.js'

export type ChatTransportProtocol = 'chat_completions' | 'responses'

export interface ChatTransportMessage {
  role: 'user' | 'assistant'
  content: string
}
export interface ChatTransportInputBlock { type: 'input_text' | 'input_image'; text?: string; dataUrl?: string }

export function resolveChatBudgetContent(input: {
  protocol: ChatTransportProtocol
  currentContent: string
  currentBlocks?: ChatTransportInputBlock[]
}): string {
  if (input.protocol !== 'responses' || !input.currentBlocks?.length) return input.currentContent
  return input.currentBlocks
    .filter((block) => block.type === 'input_text')
    .map((block) => block.text ?? '')
    .join('\n')
}

export function selectChatTransport(input: {
  supportedProtocols: readonly ChatTransportProtocol[]
  toolsEnabled: boolean
}): ChatTransportProtocol {
  return input.toolsEnabled && input.supportedProtocols.includes('responses')
    ? 'responses'
    : 'chat_completions'
}

export async function resolveChatSupportedProtocols(input: {
  groupIds: readonly string[]
  model: string
  loadAccounts: (groupId: string, model: string, endpointFamily: ChatTransportProtocol) => Promise<readonly unknown[]>
}): Promise<ChatTransportProtocol[]> {
  const protocols: ChatTransportProtocol[] = ['chat_completions']
  for (const groupId of [...new Set(input.groupIds.filter(Boolean))]) {
    const accounts = await input.loadAccounts(groupId, input.model, 'responses')
    if (accounts.length) {
      protocols.push('responses')
      break
    }
  }
  return protocols
}

export function buildChatTransportRequest(input: {
  protocol: ChatTransportProtocol
  instructions: string
  model: string
  history: ChatTransportMessage[]
  currentContent: string
  currentBlocks?: ChatTransportInputBlock[]
  toolsEnabled: boolean
  reasoningEffort?: ChatReasoningEffort
  serviceTier?: ChatServiceTier
}): { path: '/v1/chat/completions' | '/v1/responses'; body: Record<string, unknown> } {
  const messages = [{ role: 'system' as const, content: input.instructions }, ...input.history, { role: 'user' as const, content: input.currentContent }]
  if (input.protocol === 'responses') {
    const currentContent = input.currentBlocks?.length
      ? input.currentBlocks.map((block) => block.type === 'input_image' ? { type: 'input_image', image_url: block.dataUrl } : { type: 'input_text', text: block.text ?? '' })
      : input.currentContent
    return {
      path: '/v1/responses',
      body: {
        model: input.model,
        instructions: input.instructions,
        input: [...input.history, { role: 'user' as const, content: currentContent }],
        stream: true,
        ...(input.reasoningEffort ? { reasoning: { effort: input.reasoningEffort } } : {}),
        ...(input.serviceTier ? { service_tier: input.serviceTier } : {}),
        ...(input.toolsEnabled ? { tools: [{ type: 'web_search' }], tool_choice: 'auto' } : {})
      }
    }
  }
  return {
    path: '/v1/chat/completions',
    body: {
      model: input.model,
      messages,
      stream: true,
      ...(input.reasoningEffort ? { reasoning_effort: input.reasoningEffort } : {}),
      ...(input.serviceTier ? { service_tier: input.serviceTier } : {})
    }
  }
}
