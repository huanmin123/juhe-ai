import type { Request } from 'express'

/**
 * Admission policy for the single gateway candidate-failover mechanism.
 *
 * This module never changes accounts itself. It only decides whether a failed
 * response may enter the common opaque failed-response handler, after which
 * the dispatch loop performs the same candidate exclusion, fallback, audit,
 * concurrency and circuit work for every endpoint family.
 */
export const earlyFailedResponseFailoverWindowMs = 5_000

export function shouldAdmitFailedResponseToUnifiedFailover(input: {
  req: Request
  attemptStartedAt: number
  nowMs?: number
}): boolean {
  if (!isNativeImageGenerationRequest(input.req)) return false
  const elapsedMs = Math.max(0, (input.nowMs ?? Date.now()) - input.attemptStartedAt)
  return elapsedMs <= earlyFailedResponseFailoverWindowMs
}

function isNativeImageGenerationRequest(req: Request): boolean {
  if (req.method.toUpperCase() !== 'POST') return false
  const path = (req.originalUrl || req.path || '').split('?', 1)[0]
  const normalizedPath = (path.startsWith('/') ? path : `/${path}`).replace(/^\/v1(?=\/|$)/, '')
  return normalizedPath === '/images/generations'
}
