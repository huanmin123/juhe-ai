export function scheduleProcessFatalError(error: unknown): void {
  process.nextTick(() => {
    throw error instanceof Error ? error : new Error(String(error))
  })
}
