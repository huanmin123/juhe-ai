export type IpcPendingRequestLike<Value> = {
  resolve: (value: Value) => void
  timeout: NodeJS.Timeout
}

export function finishIpcPendingRequest<Value>(
  requests: Map<string, IpcPendingRequestLike<Value>>,
  requestId: string,
  value: Value
): boolean {
  const pending = requests.get(requestId)
  if (!pending) {
    return false
  }

  clearTimeout(pending.timeout)
  requests.delete(requestId)
  pending.resolve(value)
  return true
}

export function timeoutIpcPendingRequest<Value>(
  requests: Map<string, IpcPendingRequestLike<Value | undefined>>,
  requestId: string
): boolean {
  const pending = requests.get(requestId)
  if (!pending) {
    return false
  }

  requests.delete(requestId)
  pending.resolve(undefined)
  return true
}

export function failIpcPendingRequests<Value>(
  requests: Map<string, IpcPendingRequestLike<Value | undefined>>
): void {
  for (const [requestId, pending] of requests) {
    clearTimeout(pending.timeout)
    pending.resolve(undefined)
    requests.delete(requestId)
  }
}
