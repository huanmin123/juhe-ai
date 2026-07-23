export function createTableMonitorHistoryRequestGate() {
  let generation = 0

  return {
    begin(signature: string) {
      const requestGeneration = ++generation
      return {
        isCurrent(currentSignature: string) {
          return requestGeneration === generation && signature === currentSignature
        }
      }
    },
    invalidate() {
      generation += 1
    }
  }
}
