export interface ApiKeyEditOpenOperation {
  generation: number
  scopeKey: string
}

export function createApiKeyEditOpenOperationGuard() {
  let generation = 0

  function beginOpenOperation(scopeKey: string): ApiKeyEditOpenOperation {
    generation += 1
    return { generation, scopeKey }
  }

  function isCurrentOpenOperation(operation: ApiKeyEditOpenOperation, currentScopeKey: string): boolean {
    return operation.generation === generation && operation.scopeKey === currentScopeKey
  }

  function invalidateOpenOperation(): void {
    generation += 1
  }

  return {
    beginOpenOperation,
    invalidateOpenOperation,
    isCurrentOpenOperation
  }
}
