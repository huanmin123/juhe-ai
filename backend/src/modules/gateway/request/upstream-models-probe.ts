import type { Request } from 'express'

const upstreamModelsProbeRequests = new WeakSet<Request>()

export function markGatewayUpstreamModelsProbe(req: Request): Request {
  upstreamModelsProbeRequests.add(req)
  return req
}

export function isGatewayUpstreamModelsProbe(req: Request): boolean {
  return upstreamModelsProbeRequests.has(req)
}
