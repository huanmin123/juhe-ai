export class ChatConversationMutationQueue {
  private readonly tails = new Map<string, Promise<void>>()

  get size(): number { return this.tails.size }

  enqueue<T>(conversationId: string, task: () => Promise<T>): Promise<T> {
    const previous = this.tails.get(conversationId) ?? Promise.resolve()
    const result = previous.catch(() => undefined).then(task)
    const settled = result.then(() => undefined, () => undefined)
    this.tails.set(conversationId, settled)
    void settled.then(() => {
      if (this.tails.get(conversationId) === settled) this.tails.delete(conversationId)
    })
    return result
  }
}
