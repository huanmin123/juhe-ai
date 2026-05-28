import type { Request } from 'express'

import {
  buildUpstreamUrlsForAccount,
  type UpstreamAccount
} from './openai-gateway-route-helpers.js'

export interface GatewayAccountCapabilityFilterResult {
  accounts: UpstreamAccount[]
  skippedCount: number
  reason?: 'request_capability_mismatch'
}

export function filterGatewayAccountsByRequestCapability(
  req: Request,
  accounts: UpstreamAccount[]
): GatewayAccountCapabilityFilterResult {
  const filtered = accounts.filter((account) => buildUpstreamUrlsForAccount(account, req).length > 0)
  const skippedCount = accounts.length - filtered.length
  return {
    accounts: filtered,
    skippedCount,
    reason: accounts.length > 0 && filtered.length === 0 ? 'request_capability_mismatch' : undefined
  }
}
