import type { Request } from 'express'

import type { DispatchAccountSecret } from '../../../storage/repositories.js'
import {
  accountSupportsGatewayRequest,
  gatewayRequestCapabilityMismatchReason,
  type ProviderRequestCapabilityMismatchReason
} from '../../providers/drivers/registry.js'

export interface GatewayAccountCapabilityFilterResult {
  accounts: DispatchAccountSecret[]
  skippedCount: number
  reason?: GatewayAccountCapabilityFilterReason
}

export type GatewayAccountCapabilityFilterReason = ProviderRequestCapabilityMismatchReason

export function filterGatewayAccountsByRequestCapability(
  req: Request,
  accounts: DispatchAccountSecret[]
): GatewayAccountCapabilityFilterResult {
  const filtered = accounts.filter((account) => accountSupportsGatewayRequest(req, account))
  const skippedCount = accounts.length - filtered.length
  return {
    accounts: filtered,
    skippedCount,
    reason: accounts.length > 0 && filtered.length === 0
      ? gatewayRequestCapabilityMismatchReason(req, accounts)
      : undefined
  }
}
