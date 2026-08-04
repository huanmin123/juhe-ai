import type { Request } from 'express'

import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import { gatewayProtocolResponseEndpointFamilyForRequest } from '../protocols/registry.js'

export function isSuccessfulEmptyUpstreamResponseAllowed(input: {
  req: Request
  account: UpstreamAccount
  statusCode: number
}): boolean {
  if (input.statusCode < 200 || input.statusCode >= 300) return false
  if (input.req.method.toUpperCase() !== 'DELETE') return false
  const requestPath = (input.req.originalUrl || input.req.path || '').split('?', 1)[0].toLowerCase()
  const normalizedPath = requestPath.replace(/^\/v1beta(?=\/|$)/, '') || '/'
  if (!/^\/interactions\/[^/]+$/.test(normalizedPath)) return false
  return gatewayProtocolResponseEndpointFamilyForRequest(input.req, input.account) === 'interactions'
}
