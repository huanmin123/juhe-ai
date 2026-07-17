import type { ChatTransportProtocol } from './chat-transport.js'

interface ChatModelAccountSnapshot {
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

interface ChatModelOptionLike {
  id: string
  supportedApiProtocols?: readonly string[]
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
  return protocolOrder.filter((protocol) => accounts.some((account) => accountSupportsProtocol(account, model, protocol)))
}

function accountSupportsProtocol(account: ChatModelAccountSnapshot, model: string, protocol: ChatTransportProtocol): boolean {
  const mapping = account.modelMappings?.find((item) => (
    item.enabled !== false
    && item.sourceModel === model
    && item.sourceEndpointFamily === protocol
  ))
  const supportedModels = account.supportedModels ?? []
  if (supportedModels.length > 0 && !supportedModels.includes(mapping?.upstreamModel ?? model)) return false
  const upstreamProtocol = mapping?.upstreamEndpointFamily ?? protocol
  const requiredMode = upstreamProtocol === 'responses' ? 'responses_sse' : 'chat_sse'
  return account.supportedEndpointModes?.includes(requiredMode) === true
}
