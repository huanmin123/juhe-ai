import type { ChatReasoningEffort, ChatServiceTier } from './chat-model-options.js'
import { mapChatHostedToolsToResponses, type ChatHostedTool } from './chat-tools.js'
import type { ChatInternalToolDefinition } from './tools/contracts.js'
import { compileChatInternalTools } from './tools/protocol.js'

export type ChatTransportProtocol = 'chat_completions' | 'responses'

export interface ChatTransportMessage {
  role: 'user' | 'assistant'
  content: string | ChatTransportInputBlock[]
}
export interface ChatTransportInputBlock { type: 'input_text' | 'input_image'; text?: string; dataUrl?: string }
interface ChatTransportAccount {
  supportedEndpointModes?: readonly string[]
  supportedModels?: readonly string[]
  modelMappings?: ReadonlyArray<{
    enabled?: boolean
    sourceModel: string
    sourceEndpointFamily: string
    upstreamModel: string
    upstreamEndpointFamily: string
  }>
}

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
  preferResponses: boolean
}): ChatTransportProtocol {
  if (input.preferResponses && input.supportedProtocols.includes('responses')) return 'responses'
  if (input.supportedProtocols.includes('chat_completions')) return 'chat_completions'
  if (input.supportedProtocols.includes('responses')) return 'responses'
  return 'chat_completions'
}

export async function resolveChatSupportedProtocols(input: {
  groupIds: readonly string[]
  model: string
  loadAccounts: (groupId: string, model: string, endpointFamily: ChatTransportProtocol) => Promise<readonly ChatTransportAccount[]>
}): Promise<ChatTransportProtocol[]> {
  const protocolOrder: ChatTransportProtocol[] = ['chat_completions', 'responses']
  const supported = new Set<ChatTransportProtocol>()
  for (const groupId of [...new Set(input.groupIds.filter(Boolean))]) {
    for (const endpointFamily of protocolOrder) {
      if (supported.has(endpointFamily)) continue
      const accounts = await input.loadAccounts(groupId, input.model, endpointFamily)
      if (accounts.some((account) => chatTransportAccountSupportsProtocol(account, input.model, endpointFamily))) {
        supported.add(endpointFamily)
      }
    }
    if (supported.size === protocolOrder.length) break
  }
  return protocolOrder.filter((protocol) => supported.has(protocol))
}

function chatTransportAccountSupportsProtocol(
  account: ChatTransportAccount,
  model: string,
  protocol: ChatTransportProtocol
): boolean {
  const mapping = account.modelMappings?.find((item) => (
    item.enabled !== false
    && item.sourceModel === model
    && item.sourceEndpointFamily === protocol
  ))
  const supportedModels = account.supportedModels ?? []
  if (supportedModels.length > 0) {
    const routedModel = mapping?.upstreamModel ?? model
    if (!supportedModels.includes(routedModel)) return false
  }
  const upstreamProtocol = mapping?.upstreamEndpointFamily ?? protocol
  const requiredMode = {
    responses: 'responses_sse',
    chat_completions: 'chat_sse',
    messages: 'messages_sse',
    generate_content: 'generate_content_sse'
  }[upstreamProtocol]
  return Boolean(requiredMode && account.supportedEndpointModes?.includes(requiredMode))
}

export function buildChatTransportRequest(input: {
  protocol: ChatTransportProtocol
  instructions: string
  model: string
  history: ChatTransportMessage[]
  currentContent: string
  currentBlocks?: ChatTransportInputBlock[]
  effectiveTools: readonly ChatHostedTool[]
  internalTools?: readonly ChatInternalToolDefinition[]
  toolContinuation?: readonly unknown[]
  reasoningEffort?: ChatReasoningEffort
  serviceTier?: ChatServiceTier
  promptCacheKey?: string
}): { path: '/v1/chat/completions' | '/v1/responses'; body: Record<string, unknown> } {
  const messages = [
    { role: 'system' as const, content: input.instructions },
    ...input.history,
    { role: 'user' as const, content: input.currentContent },
    ...(input.toolContinuation ?? [])
  ]
  if (input.protocol === 'responses') {
    const internalTools = compileChatInternalTools('responses', input.internalTools ?? [])
    const tools = [...mapChatHostedToolsToResponses(input.effectiveTools), ...internalTools]
    const currentContent = input.currentBlocks?.length
      ? toResponsesBlocks(input.currentBlocks)
      : toResponsesBlocks([{ type: 'input_text', text: input.currentContent }])
    return {
      path: '/v1/responses',
      body: {
        model: input.model,
        instructions: input.instructions,
        input: [
          ...input.history.map((message) => ({ ...message, content: toResponsesMessageContent(message) })),
          { role: 'user' as const, content: currentContent },
          ...(input.toolContinuation ?? [])
        ],
        stream: true,
        ...(input.reasoningEffort ? { reasoning: { effort: input.reasoningEffort, summary: 'auto' } } : {}),
        ...(input.serviceTier ? { service_tier: input.serviceTier } : {}),
        ...(input.promptCacheKey ? { prompt_cache_key: input.promptCacheKey } : {}),
        ...(tools.length > 0 ? { tools, tool_choice: 'auto' } : {}),
        ...(internalTools.length > 0 ? { parallel_tool_calls: false } : {})
      }
    }
  }
  const internalTools = compileChatInternalTools('chat_completions', input.internalTools ?? [])
  return {
    path: '/v1/chat/completions',
    body: {
      model: input.model,
      messages,
      stream: true,
      stream_options: { include_usage: true },
      ...(input.reasoningEffort ? { reasoning_effort: input.reasoningEffort } : {}),
      ...(input.serviceTier ? { service_tier: input.serviceTier } : {}),
      ...(input.promptCacheKey ? { prompt_cache_key: input.promptCacheKey } : {}),
      ...(internalTools.length > 0 ? { tools: internalTools, tool_choice: 'auto', parallel_tool_calls: false } : {})
    }
  }
}

function toResponsesMessageContent(message: ChatTransportMessage): string | Array<Record<string, unknown>> {
  if (typeof message.content !== 'string') return toResponsesBlocks(message.content)
  return message.role === 'user'
    ? toResponsesBlocks([{ type: 'input_text', text: message.content }])
    : message.content
}

function toResponsesBlocks(blocks: ChatTransportInputBlock[]): Array<Record<string, unknown>> {
  return blocks.map((block) => block.type === 'input_image'
    ? { type: 'input_image', image_url: block.dataUrl, detail: 'high' }
    : { type: 'input_text', text: block.text ?? '' })
}
