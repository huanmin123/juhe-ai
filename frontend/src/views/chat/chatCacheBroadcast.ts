export interface ChatCacheBroadcastPayload {
  systemAccountId: string
  conversationId: string
  messageRevision: number
}

type ChannelLike = Pick<BroadcastChannel, 'postMessage' | 'close' | 'onmessage'>
type ChannelFactory = (name: string) => ChannelLike

const IDENTIFIER = /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/

export function isChatCacheBroadcastPayload(value: unknown): value is ChatCacheBroadcastPayload {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const record = value as Record<string, unknown>
  return Object.keys(record).length === 3
    && typeof record.systemAccountId === 'string'
    && IDENTIFIER.test(record.systemAccountId)
    && typeof record.conversationId === 'string'
    && IDENTIFIER.test(record.conversationId)
    && Number.isSafeInteger(record.messageRevision)
    && (record.messageRevision as number) >= 0
}

export class ChatCacheBroadcast {
  readonly enabled: boolean
  private readonly channel?: ChannelLike
  private readonly listeners = new Set<(payload: ChatCacheBroadcastPayload) => void>()
  private closed = false

  constructor(options: { channelName?: string; channelFactory?: ChannelFactory } = {}) {
    const factory = 'channelFactory' in options
      ? options.channelFactory
      : typeof BroadcastChannel === 'function' ? (name: string) => new BroadcastChannel(name) : undefined
    try {
      this.channel = factory?.(options.channelName ?? 'juhe-ai-chat-cache-v1')
      this.enabled = Boolean(this.channel)
      if (this.channel) this.channel.onmessage = (event) => this.receive(event.data)
    } catch {
      this.enabled = false
    }
  }

  publish(payload: ChatCacheBroadcastPayload): boolean {
    if (this.closed || !this.channel) return false
    const safePayload = {
      systemAccountId: payload.systemAccountId,
      conversationId: payload.conversationId,
      messageRevision: payload.messageRevision
    }
    if (!isChatCacheBroadcastPayload(safePayload)) return false
    try {
      this.channel.postMessage(safePayload)
      return true
    } catch {
      return false
    }
  }

  subscribe(listener: (payload: ChatCacheBroadcastPayload) => void): () => void {
    if (!this.closed) this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  close(): void {
    if (this.closed) return
    this.closed = true
    this.listeners.clear()
    if (this.channel) {
      this.channel.onmessage = null
      try { this.channel.close() } catch { /* no-op */ }
    }
  }

  private receive(value: unknown): void {
    if (this.closed || !isChatCacheBroadcastPayload(value)) return
    for (const listener of this.listeners) {
      try { listener(value) } catch { /* listeners are isolated */ }
    }
  }
}
