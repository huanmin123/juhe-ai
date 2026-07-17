export interface ChatImagePreparationToken {
  readonly id: number
  readonly generation: number
}

export interface ChatImagePreparationSnapshot {
  generation: number
  pendingCount: number
  activeTokenCount: number
}

export interface ChatImagePreparationState {
  begin: () => ChatImagePreparationToken
  release: (token: ChatImagePreparationToken) => void
  advanceGeneration: () => ChatImagePreparationSnapshot
  currentGeneration: () => number
  pendingCount: () => number
  isCurrent: (token: ChatImagePreparationToken) => boolean
  snapshot: () => ChatImagePreparationSnapshot
}

export function createChatImagePreparationState(): ChatImagePreparationState {
  let generation = 0
  let nextTokenId = 1
  const activeTokens = new Map<number, number>()
  const pendingCount = (): number => {
    let count = 0
    for (const tokenGeneration of activeTokens.values()) {
      if (tokenGeneration === generation) count += 1
    }
    return count
  }
  const snapshot = (): ChatImagePreparationSnapshot => ({
    generation,
    pendingCount: pendingCount(),
    activeTokenCount: activeTokens.size
  })

  return {
    begin() {
      const token = { id: nextTokenId, generation }
      nextTokenId += 1
      activeTokens.set(token.id, token.generation)
      return token
    },
    release(token) {
      if (activeTokens.get(token.id) === token.generation) activeTokens.delete(token.id)
    },
    advanceGeneration() {
      generation += 1
      activeTokens.clear()
      return snapshot()
    },
    currentGeneration: () => generation,
    pendingCount,
    isCurrent: (token) => token.generation === generation,
    snapshot
  }
}
