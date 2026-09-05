export interface DownstreamResponseWithErrorBoundary {
  on?(event: 'error', listener: (error: Error) => void): unknown
  once?(event: 'error', listener: (error: Error) => void): unknown
  off?(event: 'error', listener: (error: Error) => void): unknown
}

export function attachDownstreamResponseErrorBoundary(input: {
  response: DownstreamResponseWithErrorBoundary
  onError(error: Error): void
  onUnwritable(): void
}): () => void {
  let detached = false
  let reported = false
  const hasPersistentListener = typeof input.response.on === 'function'
  const onError = (error: Error): void => {
    if (!detached && !reported) {
      reported = true
      try { input.onError(error) } catch {}
      try { input.onUnwritable() } catch {}
    }
    if (!detached && !hasPersistentListener) {
      try { input.response.once?.('error', onError) } catch {}
    }
  }
  try {
    if (hasPersistentListener) input.response.on?.('error', onError)
    else input.response.once?.('error', onError)
  } catch {
    onError(new Error('downstream_response_error_listener_failed'))
  }
  return () => {
    if (detached) return
    detached = true
    try { input.response.off?.('error', onError) } catch {}
  }
}
