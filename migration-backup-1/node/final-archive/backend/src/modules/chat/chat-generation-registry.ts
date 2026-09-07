import {
  ChatGenerationRunner,
  type ChatGenerationIdentity,
  type ChatGenerationStatusSnapshot,
  type ChatGenerationSubscriber
} from './chat-generation-runner.js'

export type ChatGenerationRegistryStatusSnapshot = ChatGenerationStatusSnapshot | { state: 'missing' }

export class ChatGenerationRegistry {
  private readonly runners = new Map<string, ChatGenerationRunner>()
  private readonly terminalSnapshots = new Map<string, ChatGenerationStatusSnapshot>()
  private readonly terminalSnapshotLimit: number
  private shuttingDown = false

  constructor(options: { terminalSnapshotLimit?: number } = {}) {
    this.terminalSnapshotLimit = normalizeTerminalSnapshotLimit(options.terminalSnapshotLimit)
  }

  start(runner: ChatGenerationRunner): boolean {
    if (this.shuttingDown || this.runners.has(runner.identity.conversationId)) return false
    this.terminalSnapshots.delete(identityKey(runner.identity))
    this.runners.set(runner.identity.conversationId, runner)
    if (!runner.start(() => {
      this.rememberTerminalSnapshot(runner)
      this.deleteIfMatches(runner)
    })) {
      this.deleteIfMatches(runner)
      return false
    }
    return true
  }

  get(identity: Pick<ChatGenerationIdentity, 'ownerId' | 'conversationId' | 'turnId'>): ChatGenerationRunner | undefined {
    const runner = this.runners.get(identity.conversationId)
    return runner && matchesIdentity(runner, identity) ? runner : undefined
  }

  snapshot(identity: Pick<ChatGenerationIdentity, 'ownerId' | 'conversationId' | 'turnId'>): ChatGenerationRegistryStatusSnapshot {
    const runner = this.get(identity)
    if (runner) return runner.statusSnapshot()
    return this.terminalSnapshots.get(identityKey(identity)) ?? { state: 'missing' }
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
    this.terminalSnapshots.clear()
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
      for (const runner of runners) this.deleteIfMatches(runner)
    }
  }

  private deleteIfMatches(expected: ChatGenerationRunner): boolean {
    if (this.runners.get(expected.identity.conversationId) !== expected) return false
    return this.runners.delete(expected.identity.conversationId)
  }

  private rememberTerminalSnapshot(runner: ChatGenerationRunner): void {
    if (this.shuttingDown || !runner.terminal || !runner.authoritativeTerminal || this.terminalSnapshotLimit === 0) return
    const key = identityKey(runner.identity)
    this.terminalSnapshots.delete(key)
    this.terminalSnapshots.set(key, Object.freeze({ ...runner.statusSnapshot() }))
    while (this.terminalSnapshots.size > this.terminalSnapshotLimit) {
      const oldest = this.terminalSnapshots.keys().next().value as string | undefined
      if (oldest === undefined) break
      this.terminalSnapshots.delete(oldest)
    }
  }
}

function identityKey(identity: Pick<ChatGenerationIdentity, 'ownerId' | 'conversationId' | 'turnId'>): string {
  return JSON.stringify([identity.ownerId, identity.conversationId, identity.turnId])
}

function normalizeTerminalSnapshotLimit(value: number | undefined): number {
  if (value === undefined) return 512
  return Number.isSafeInteger(value) && value >= 0 ? value : 512
}

function matchesIdentity(
  runner: ChatGenerationRunner,
  identity: Pick<ChatGenerationIdentity, 'ownerId' | 'conversationId' | 'turnId'>
): boolean {
  return runner.identity.ownerId === identity.ownerId &&
    runner.identity.conversationId === identity.conversationId &&
    runner.identity.turnId === identity.turnId
}
