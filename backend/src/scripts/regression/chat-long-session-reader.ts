export type ChatLongSessionCancellationTimer = {
  setTimeout: (callback: () => void, timeoutMs: number) => unknown
  clearTimeout: (handle: unknown) => void
}

const systemCancellationTimer: ChatLongSessionCancellationTimer = {
  setTimeout: (callback, timeoutMs) => setTimeout(callback, timeoutMs),
  clearTimeout: (handle) => clearTimeout(handle as NodeJS.Timeout)
}

export async function consumeReaderWithBoundedCancellation<T>(
  reader: Pick<ReadableStreamDefaultReader<Uint8Array>, 'cancel'>,
  consume: () => Promise<T>,
  cancelTimeoutMs = 2_000,
  timer: ChatLongSessionCancellationTimer = systemCancellationTimer
): Promise<T> {
  try {
    return await consume()
  } catch (error) {
    await cancelReaderBounded(reader, cancelTimeoutMs, timer)
    throw error
  }
}

async function cancelReaderBounded(
  reader: Pick<ReadableStreamDefaultReader<Uint8Array>, 'cancel'>,
  timeoutMs: number,
  timer: ChatLongSessionCancellationTimer
): Promise<void> {
  let timeout: unknown
  try {
    await Promise.race([
      Promise.resolve(reader.cancel()).catch(() => undefined),
      new Promise<void>((resolveTimeout) => { timeout = timer.setTimeout(resolveTimeout, Math.max(1, timeoutMs)) })
    ])
  } catch {
    // Cleanup must preserve the original reader error.
  } finally {
    if (timeout !== undefined) timer.clearTimeout(timeout)
  }
}
