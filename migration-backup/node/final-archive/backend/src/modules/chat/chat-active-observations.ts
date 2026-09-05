export function trackActiveChatObservation(
  observations: Map<string, Set<Promise<void>>>,
  assetId: string,
  task: Promise<void>
): void {
  let active = observations.get(assetId)
  if (!active) {
    active = new Set()
    observations.set(assetId, active)
  }
  active.add(task)
  const cleanup = (): void => {
    const current = observations.get(assetId)
    current?.delete(task)
    if (current?.size === 0) observations.delete(assetId)
  }
  void task.then(cleanup, cleanup)
}

export function listActiveChatObservationTasks(
  observations: ReadonlyMap<string, ReadonlySet<Promise<void>>>,
  assetIds: readonly string[]
): Promise<void>[] {
  return [...new Set(assetIds)].flatMap((assetId) => [...(observations.get(assetId) ?? [])])
}
