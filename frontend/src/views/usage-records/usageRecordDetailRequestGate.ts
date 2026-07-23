export interface UsageRecordDetailRequestToken {
  generation: number
  id: string
  signature: string
}

export function createUsageRecordDetailRequestGate() {
  let generation = 0
  let active = true

  function begin(id: string, signature: string): UsageRecordDetailRequestToken {
    return { generation: ++generation, id, signature }
  }

  function isCurrent(token: UsageRecordDetailRequestToken, id: string, signature: string): boolean {
    return active && token.generation === generation && token.id === id && token.signature === signature
  }

  function invalidate(): void {
    generation += 1
  }

  function deactivate(): void {
    active = false
    invalidate()
  }

  function activate(): void {
    active = true
    invalidate()
  }

  return { activate, begin, deactivate, invalidate, isCurrent }
}
