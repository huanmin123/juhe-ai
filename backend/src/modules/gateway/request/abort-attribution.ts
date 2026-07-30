import type { Request } from 'express'

export type GatewayRequestAbortSource =
  | 'server_diagnostic_timeout'
  | 'server_diagnostic_cancel'

const gatewayRequestAbortSourceSymbol = Symbol('juheAiGatewayRequestAbortSource')

type RequestWithAbortSource = Request & {
  [gatewayRequestAbortSourceSymbol]?: GatewayRequestAbortSource
}

export function markGatewayRequestAbortSource(
  req: Request,
  source: GatewayRequestAbortSource
): void {
  ;(req as RequestWithAbortSource)[gatewayRequestAbortSourceSymbol] = source
}

export function gatewayRequestAbortSource(req: Request): GatewayRequestAbortSource | undefined {
  return (req as RequestWithAbortSource)[gatewayRequestAbortSourceSymbol]
}

export function gatewayDiagnosticAbortSourceFromSignal(
  signal: AbortSignal
): GatewayRequestAbortSource {
  const reason = signal.reason
  if (isTimeoutLikeAbortReason(reason)) return 'server_diagnostic_timeout'
  return 'server_diagnostic_cancel'
}

function isTimeoutLikeAbortReason(reason: unknown): boolean {
  if (typeof reason === 'string') return /timeout|deadline/i.test(reason)
  if (!reason || typeof reason !== 'object') return false
  const candidate = reason as { name?: unknown; message?: unknown }
  return candidate.name === 'TimeoutError'
    || (typeof candidate.message === 'string' && /timeout|deadline/i.test(candidate.message))
}
