export type FirstByteDeadlineAction = 'continue' | 'abort'

export interface FirstByteDeadlineDecisionInput {
  elapsedMs: number
  timeoutMs: number
  transport: 'stream' | 'non_stream'
}

export type FirstByteDeadlineHandler = (
  input: FirstByteDeadlineDecisionInput
) => FirstByteDeadlineAction | Promise<FirstByteDeadlineAction>

export interface ObservedFirstBytePendingRead<T> {
  promise: Promise<T>
  isSettled: () => boolean
  settledAtMs: () => number | undefined
}

export type FirstByteDeadlineDecisionResult<T> =
  | {
      type: 'read'
      result: T
      action?: FirstByteDeadlineAction
      decisionError?: unknown
      settledAtMs?: number
    }
  | { type: 'action'; action: FirstByteDeadlineAction }
  | { type: 'response_precommit_deadline'; error: GatewayResponsePrecommitDeadlineError }

export interface FirstByteDeadlineDecisionWaitOptions {
  responsePrecommitDeadlineAtMs?: number
  onResponsePrecommitDeadline?: () => void
}

export class GatewayResponsePrecommitDeadlineError extends Error {
  readonly code = 'gateway_request_wall_budget_exhausted'

  constructor(readonly deadlineAtMs: number) {
    super('网关请求墙钟已到，响应尚未产生可提交的语义结果')
    this.name = 'GatewayResponsePrecommitDeadlineError'
  }
}

export function isGatewayResponsePrecommitDeadlineError(
  error: unknown
): error is GatewayResponsePrecommitDeadlineError {
  return error instanceof GatewayResponsePrecommitDeadlineError
}

export function observeFirstBytePendingRead<T>(pendingRead: Promise<T>): ObservedFirstBytePendingRead<T> {
  let settled = false
  let settledAtMs: number | undefined
  const promise = pendingRead.then(
    (result) => {
      settled = true
      settledAtMs = Date.now()
      return result
    },
    (error: unknown) => {
      settled = true
      settledAtMs = Date.now()
      throw error
    }
  )
  // The routing decision can remain pending after the read rejects. Keep the
  // observed promise handled until the caller awaits it and rethrows normally.
  void promise.catch(() => undefined)
  return {
    promise,
    isSettled: () => settled,
    settledAtMs: () => settledAtMs
  }
}

/**
 * Wait for the routing decision because it may own shared reservations or
 * observations. The caller decides whether a read that settled in parallel is
 * semantic enough to supersede that decision; raw activity alone is not.
 */
export async function decideFirstByteDeadlineAfterPendingRead<T>(
  pendingRead: ObservedFirstBytePendingRead<T>,
  handler: FirstByteDeadlineHandler | undefined,
  input: FirstByteDeadlineDecisionInput,
  options: FirstByteDeadlineDecisionWaitOptions = {}
): Promise<FirstByteDeadlineDecisionResult<T>> {
  let handlerResult: FirstByteDeadlineAction | Promise<FirstByteDeadlineAction>
  try {
    handlerResult = handler?.(input) ?? 'abort'
  } catch (error) {
    if (!pendingRead.isSettled()) {
      throw error
    }
    return {
      type: 'read',
      result: await pendingRead.promise,
      decisionError: error,
      settledAtMs: pendingRead.settledAtMs()
    }
  }

  const decision = Promise.resolve(handlerResult).then(
    (action) => ({ type: 'action' as const, action, settledAtMs: Date.now() }),
    (error: unknown) => ({ type: 'error' as const, error, settledAtMs: Date.now() })
  )
  const responsePrecommitDeadlineAtMs = options.responsePrecommitDeadlineAtMs
  let responsePrecommitTimer: NodeJS.Timeout | undefined
  const outcome = responsePrecommitDeadlineAtMs === undefined
    ? await decision
    : await Promise.race([
        decision,
        new Promise<{ type: 'response_precommit_deadline' }>((resolve) => {
          responsePrecommitTimer = setTimeout(
            () => resolve({ type: 'response_precommit_deadline' }),
            Math.max(1, responsePrecommitDeadlineAtMs - Date.now())
          )
        })
      ]).finally(() => {
        if (responsePrecommitTimer) clearTimeout(responsePrecommitTimer)
      })

  if (
    responsePrecommitDeadlineAtMs !== undefined
    && (
      outcome.type === 'response_precommit_deadline'
      || outcome.settledAtMs > responsePrecommitDeadlineAtMs
    )
  ) {
    const deadlineError = new GatewayResponsePrecommitDeadlineError(responsePrecommitDeadlineAtMs)
    notifyResponsePrecommitDeadline(options.onResponsePrecommitDeadline)
    // The local routing decision may acquire a reservation after the wall
    // deadline wins. Release again when that late decision settles.
    void decision.then(() => notifyResponsePrecommitDeadline(options.onResponsePrecommitDeadline))
    const readSettledAtMs = pendingRead.settledAtMs()
    if (
      pendingRead.isSettled()
      && readSettledAtMs !== undefined
      && readSettledAtMs <= responsePrecommitDeadlineAtMs
    ) {
      return {
        type: 'read',
        result: await pendingRead.promise,
        decisionError: deadlineError,
        settledAtMs: readSettledAtMs
      }
    }
    return { type: 'response_precommit_deadline', error: deadlineError }
  }
  if (outcome.type === 'response_precommit_deadline') {
    return {
      type: 'response_precommit_deadline',
      error: new GatewayResponsePrecommitDeadlineError(responsePrecommitDeadlineAtMs ?? Date.now())
    }
  }

  if (outcome.type === 'error') {
    if (!pendingRead.isSettled()) throw outcome.error
    return {
      type: 'read',
      result: await pendingRead.promise,
      decisionError: outcome.error,
      settledAtMs: pendingRead.settledAtMs()
    }
  }
  if (!pendingRead.isSettled()) {
    return { type: 'action', action: outcome.action }
  }
  return {
    type: 'read',
    result: await pendingRead.promise,
    action: outcome.action,
    settledAtMs: pendingRead.settledAtMs()
  }
}

function notifyResponsePrecommitDeadline(callback: (() => void) | undefined): void {
  try {
    callback?.()
  } catch {
  }
}
