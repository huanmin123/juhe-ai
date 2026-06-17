import type { Request } from 'express'

import {
  accountSupportsOpenAIEndpointMode,
  openAIEndpointModeForRequestShape
} from '../../../domain/openai-endpoint-modes.js'
import {
  buildUpstreamUrlsForAccount,
  type UpstreamAccount
} from '../protocols/openai-v1/route-helpers.js'
import { isEffectiveOpenAIStreamRequest } from '../upstream/request.js'

export interface GatewayAccountCapabilityFilterResult {
  accounts: UpstreamAccount[]
  skippedCount: number
  reason?: GatewayAccountCapabilityFilterReason
}

export type GatewayAccountCapabilityFilterReason =
  | 'request_capability_mismatch'

export function filterGatewayAccountsByRequestCapability(
  req: Request,
  accounts: UpstreamAccount[]
): GatewayAccountCapabilityFilterResult {
  const filtered = accounts.filter((account) => {
    if (buildUpstreamUrlsForAccount(account, req).length === 0) {
      return false
    }
    const mode = openAIEndpointModeForRequestShape({
      endpoint: req.path || req.originalUrl.split('?', 1)[0],
      stream: isEffectiveOpenAIStreamRequest(req, account)
    })
    if (!mode) {
      return true
    }
    return accountSupportsOpenAIEndpointMode({
      mode,
      supportedEndpointModes: account.supportedEndpointModes,
      credentials: account.credentials,
      providerCode: account.providerCode,
      accountType: account.type,
      clientCompatibility: account.clientCompatibility
    })
  })
  const skippedCount = accounts.length - filtered.length
  return {
    accounts: filtered,
    skippedCount,
    reason: accounts.length > 0 && filtered.length === 0
      ? 'request_capability_mismatch'
      : undefined
  }
}
