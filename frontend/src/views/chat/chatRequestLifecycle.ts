export class ChatRequestLifecycleEpochs {
  private readonly epochs = new Map<string, number>()

  begin(conversationId: string): number {
    return this.advance(conversationId)
  }

  invalidate(conversationId: string): void {
    this.advance(conversationId)
  }

  isCurrent(conversationId: string, epoch: number): boolean {
    return this.epochs.get(conversationId) === epoch
  }

  clear(): void {
    this.epochs.clear()
  }

  private advance(conversationId: string): number {
    const next = (this.epochs.get(conversationId) ?? 0) + 1
    this.epochs.set(conversationId, next)
    return next
  }
}
