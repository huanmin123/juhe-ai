export interface ModelCheckDemandRequestCoordinator {
  run<T>(key: string, request: (signal: AbortSignal) => Promise<T>): Promise<T | undefined>
  invalidate(): void
}

export function createModelCheckDemandRequestCoordinator(): ModelCheckDemandRequestCoordinator {
  let generation = 0
  const inFlight = new Map<string, { controller: AbortController; promise: Promise<unknown | undefined> }>()

  function run<T>(key: string, request: (signal: AbortSignal) => Promise<T>): Promise<T | undefined> {
    const existing = inFlight.get(key)
    if (existing) return existing.promise as Promise<T | undefined>

    const requestGeneration = generation
    const controller = new AbortController()
    const promise = request(controller.signal)
      .then((value) => requestGeneration === generation ? value : undefined)
      .finally(() => {
        if (inFlight.get(key)?.promise === promise) inFlight.delete(key)
      })
    inFlight.set(key, { controller, promise })
    return promise
  }

  function invalidate(): void {
    generation += 1
    for (const request of inFlight.values()) request.controller.abort()
    inFlight.clear()
  }

  return { run, invalidate }
}
