export interface ChatModelLoadRequest {
  apiKeyId: string
  conversationId: string
}

export interface ChatModelLoadCoordinatorOptions<T> {
  load: (request: ChatModelLoadRequest, signal: AbortSignal) => Promise<T[]>
  retryDelayMilliseconds?: number
  cacheTtlMilliseconds?: number
  now?: () => number
}

export interface DeletedChatConversationState<T extends { id: string }> {
  conversations: T[]
  selectedConversationId?: string
  nextConversationId?: string
}

export class ChatModelLoadCoordinator<T> {
  private readonly cache = new Map<string, { value: readonly T[]; expiresAt: number }>()
  private readonly inFlight = new Map<string, { controller: AbortController; promise: Promise<readonly T[]> }>()
  private readonly retryDelayMilliseconds: number
  private readonly cacheTtlMilliseconds: number
  private readonly now: () => number

  constructor(private readonly options: ChatModelLoadCoordinatorOptions<T>) {
    this.retryDelayMilliseconds = options.retryDelayMilliseconds ?? 750
    this.cacheTtlMilliseconds = options.cacheTtlMilliseconds ?? 30_000
    this.now = options.now ?? Date.now
  }

  load(request: ChatModelLoadRequest): Promise<readonly T[]> {
    const cached = this.cache.get(request.apiKeyId)
    if (cached && cached.expiresAt > this.now()) return Promise.resolve(cached.value)
    if (cached) this.cache.delete(request.apiKeyId)
    const running = this.inFlight.get(request.apiKeyId)
    if (running) return running.promise

    const controller = new AbortController()
    const promise = this.loadWithSingleRetry(request, controller.signal)
      .then((models) => {
        this.cache.set(request.apiKeyId, { value: models, expiresAt: this.now() + this.cacheTtlMilliseconds })
        return models
      })
      .finally(() => {
        if (this.inFlight.get(request.apiKeyId)?.controller === controller) this.inFlight.delete(request.apiKeyId)
      })
    this.inFlight.set(request.apiKeyId, { controller, promise })
    return promise
  }

  peek(apiKeyId: string): readonly T[] | undefined {
    return this.cache.get(apiKeyId)?.value
  }

  refresh(request: ChatModelLoadRequest): Promise<readonly T[]> {
    this.cache.delete(request.apiKeyId)
    return this.load(request)
  }

  refreshIfExpired(request: ChatModelLoadRequest): Promise<readonly T[]> {
    const cached = this.cache.get(request.apiKeyId)
    if (cached && cached.expiresAt > this.now()) return Promise.resolve(cached.value)
    return this.refresh(request)
  }

  cancel(apiKeyId: string | undefined): void {
    if (apiKeyId) this.inFlight.get(apiKeyId)?.controller.abort()
  }

  private async loadWithSingleRetry(request: ChatModelLoadRequest, signal: AbortSignal): Promise<readonly T[]> {
    try {
      return await this.options.load(request, signal)
    } catch (error) {
      if (!isTimeoutError(error) || signal.aborted) throw error
      if (this.retryDelayMilliseconds > 0) await wait(this.retryDelayMilliseconds, signal)
      return this.options.load(request, signal)
    }
  }
}

export class ChatSingleFlightCoordinator<T> {
  private readonly inFlight = new Map<string, Promise<T>>()

  load(key: string, operation: () => Promise<T>): Promise<T> {
    const running = this.inFlight.get(key)
    if (running) return running
    const request = operation().finally(() => {
      if (this.inFlight.get(key) === request) this.inFlight.delete(key)
    })
    this.inFlight.set(key, request)
    return request
  }
}

export function applyDeletedChatConversation<T extends { id: string }>(input: {
  conversations: readonly T[]
  selectedConversationId?: string
  deletedConversationId: string
}): DeletedChatConversationState<T> {
  const conversations = input.conversations.filter((item) => item.id !== input.deletedConversationId)
  return {
    conversations,
    selectedConversationId: input.selectedConversationId === input.deletedConversationId ? undefined : input.selectedConversationId,
    nextConversationId: input.selectedConversationId === input.deletedConversationId ? conversations[0]?.id : undefined
  }
}

function isTimeoutError(error: unknown): boolean {
  const candidate = error as { code?: unknown; message?: unknown } | undefined
  return candidate?.code === 'ECONNABORTED'
    || candidate?.code === 'ETIMEDOUT'
    || (typeof candidate?.message === 'string' && /timeout|超时/i.test(candidate.message))
}

function wait(milliseconds: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = window.setTimeout(resolve, milliseconds)
    signal.addEventListener('abort', () => {
      window.clearTimeout(timer)
      reject(Object.assign(new Error('aborted'), { name: 'AbortError' }))
    }, { once: true })
  })
}
