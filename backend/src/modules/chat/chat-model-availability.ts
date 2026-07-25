import { chatTransportAccountSupportsProtocol, type ChatTransportAccount, type ChatTransportProtocol } from './chat-transport.js'
import { buildChatModelOptions, type ChatModelOption } from './chat-model-options.js'
import { GPT_VENDOR_CODE, normalizeProviderToken } from '../../domain/provider-protocol.js'

type ChatModelAccountSnapshot = ChatTransportAccount

interface ChatModelOptionLike {
  id: string
  supportedApiProtocols?: readonly string[]
}

interface ChatProviderBinding {
  status: 'active' | 'disabled'
  providerCode: string
}

interface ChatProviderCatalogItem {
  model: string
  supportsPromptCaching?: boolean
  supportedReasoningEfforts: readonly string[]
  defaultReasoningEffort?: string | null
  supportedServiceTiers: readonly string[]
  contextWindowTokens?: number
  maxInputTokens?: number
  maxOutputTokens?: number
  supportedApiProtocols?: readonly string[]
  inputModalities?: readonly string[]
  outputModalities?: readonly string[]
  supportedTools?: readonly string[]
}

interface CacheEntry<TValue> {
  expiresAtMs: number
  value: TValue
}

export function resolveChatModelOptionsFromAccountSnapshot<TOption extends ChatModelOptionLike>(input: {
  accounts: readonly ChatModelAccountSnapshot[]
  modelOptions: readonly TOption[]
}): Array<TOption & { supportedApiProtocols: ChatTransportProtocol[] }> {
  return input.modelOptions.flatMap((modelOption) => {
    const supportedApiProtocols = supportedProtocolsForModel(input.accounts, modelOption.id)
    return supportedApiProtocols.length ? [{ ...modelOption, supportedApiProtocols }] : []
  })
}

export async function loadChatModelOptionsFromProviderCatalogs(input: {
  bindings: readonly ChatProviderBinding[]
  loadCatalog: (providerCode: string) => Promise<ChatProviderCatalogItem[]>
}): Promise<ChatModelOption[]> {
  const providerCodes = [...new Set(input.bindings
    .filter((binding) => binding.status === 'active')
    .map((binding) => binding.providerCode.trim())
    .filter(Boolean))]
  const catalog = (await Promise.all(providerCodes.map(async (providerCode) => (
    (await input.loadCatalog(providerCode)).map((item) => stableProviderCatalogItem(providerCode, item))
  )))).flat()
  return buildChatModelOptions(catalog.map((item) => item.model), catalog)
}

function stableProviderCatalogItem(providerCode: string, item: ChatProviderCatalogItem): ChatProviderCatalogItem {
  if (normalizeProviderToken(providerCode) !== GPT_VENDOR_CODE) return item
  return {
    ...item,
    supportedServiceTiers: item.supportedServiceTiers.filter((tier) => tier !== 'flex')
  }
}

export function createChatModelOptionsSnapshotCache<TValue>(input: {
  ttlMs: number
  now?: () => number
}): {
  getOrLoad: (identity: string, load: () => Promise<TValue>) => Promise<TValue>
  clear: () => void
} {
  const now = input.now ?? Date.now
  const values = new Map<string, CacheEntry<TValue>>()
  const pending = new Map<string, Promise<TValue>>()

  return {
    async getOrLoad(identity, load) {
      const cached = values.get(identity)
      if (cached && cached.expiresAtMs > now()) return cached.value
      const existing = pending.get(identity)
      if (existing) return existing
      const request = load().then((value) => {
        values.set(identity, { value, expiresAtMs: now() + input.ttlMs })
        return value
      }).finally(() => {
        pending.delete(identity)
      })
      pending.set(identity, request)
      return request
    },
    clear() {
      values.clear()
      pending.clear()
    }
  }
}

function supportedProtocolsForModel(accounts: readonly ChatModelAccountSnapshot[], model: string): ChatTransportProtocol[] {
  const protocolOrder: ChatTransportProtocol[] = ['chat_completions', 'responses']
  return protocolOrder.filter((protocol) => accounts.some((account) => chatTransportAccountSupportsProtocol(account, model, protocol)))
}
