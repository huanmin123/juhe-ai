export type ChatTransportProtocol = 'chat_completions' | 'responses'

export interface ChatTransportMessage {
  role: 'user' | 'assistant'
  content: string
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
  model: string
  history: ChatTransportMessage[]
  currentContent: string
  toolsEnabled: boolean
}): { path: '/v1/chat/completions' | '/v1/responses'; body: Record<string, unknown> } {
  const messages = [...input.history, { role: 'user' as const, content: input.currentContent }]
  if (input.protocol === 'responses') {
    return {
      path: '/v1/responses',
      body: {
        model: input.model,
        input: messages,
        stream: true,
        ...(input.toolsEnabled ? { tools: [{ type: 'web_search' }], tool_choice: 'auto' } : {})
      }
    }
  }
  return {
    path: '/v1/chat/completions',
    body: { model: input.model, messages, stream: true }
  }
}
