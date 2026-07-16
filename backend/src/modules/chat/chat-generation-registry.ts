import {
  ChatGenerationRunner,
  type ChatGenerationIdentity,
  type ChatGenerationSubscriber
} from './chat-generation-runner.js'

export class ChatGenerationRegistry {
  private readonly runners = new Map<string, ChatGenerationRunner>()
  private shuttingDown = false

  start(runner: ChatGenerationRunner): boolean {
    if (this.shuttingDown || this.runners.has(runner.identity.conversationId)) return false
    this.runners.set(runner.identity.conversationId, runner)
    if (!runner.start(() => { this.deleteIfMatches(runner) })) {
      this.deleteIfMatches(runner)
      return false
    }
    return true
  }

  get(identity: Pick<ChatGenerationIdentity, 'ownerId' | 'conversationId' | 'turnId'>): ChatGenerationRunner | undefined {
    const runner = this.runners.get(identity.conversationId)
    return runner && matchesIdentity(runner, identity) ? runner : undefined
  }

  subscribe(identity: Pick<ChatGenerationIdentity, 'ownerId' | 'conversationId' | 'turnId'>, subscriber: ChatGenerationSubscriber): boolean {
    if (this.shuttingDown) return false
    return this.get(identity)?.subscribe(subscriber) ?? false
  }

  unsubscribe(identity: Pick<ChatGenerationIdentity, 'ownerId' | 'conversationId' | 'turnId'>, subscriber: ChatGenerationSubscriber): boolean {
    return this.get(identity)?.unsubscribe(subscriber) ?? false
  }

  stop(identity: Pick<ChatGenerationIdentity, 'ownerId' | 'conversationId' | 'turnId'>): boolean {
    return this.get(identity)?.abort() ?? false
  }

  delete(identity: Pick<ChatGenerationIdentity, 'ownerId' | 'conversationId' | 'turnId'>): boolean {
    const runner = this.get(identity)
    return runner ? this.deleteIfMatches(runner) : false
  }

  async shutdown(options: { timeoutMs: number }): Promise<void> {
    this.shuttingDown = true
    const runners = [...this.runners.values()]
    for (const runner of runners) runner.abort()
    if (!runners.length) return
    let timeout: ReturnType<typeof setTimeout> | undefined
    try {
      await Promise.race([
        Promise.allSettled(runners.map((runner) => runner.completion)),
        new Promise<void>((resolve) => { timeout = setTimeout(resolve, Math.max(0, options.timeoutMs)) })
      ])
    } finally {
      if (timeout) clearTimeout(timeout)
    }
  }

  private deleteIfMatches(expected: ChatGenerationRunner): boolean {
    if (this.runners.get(expected.identity.conversationId) !== expected) return false
    return this.runners.delete(expected.identity.conversationId)
  }
}

function matchesIdentity(
  runner: ChatGenerationRunner,
  identity: Pick<ChatGenerationIdentity, 'ownerId' | 'conversationId' | 'turnId'>
): boolean {
  return runner.identity.ownerId === identity.ownerId &&
    runner.identity.conversationId === identity.conversationId &&
    runner.identity.turnId === identity.turnId
}
