import type { Request } from 'express'

import {
  buildUpstreamUrlsForAccount,
  type UpstreamAccount
} from './openai-gateway-route-helpers.js'
import {
  isOpenAIResponsesChatBridgeAccount,
  isOpenAIResponsesCompactRequest
} from './openai-responses-chat-bridge.js'

export interface GatewayAccountCapabilityFilterResult {
  accounts: UpstreamAccount[]
  skippedCount: number
  reason?: GatewayAccountCapabilityFilterReason
}

export type GatewayAccountCapabilityFilterReason =
  | 'request_capability_mismatch'
  | 'responses_compact_not_supported_by_chat_bridge'

export function filterGatewayAccountsByRequestCapability(
  req: Request,
  accounts: UpstreamAccount[]
): GatewayAccountCapabilityFilterResult {
  const filtered = accounts.filter((account) => buildUpstreamUrlsForAccount(account, req).length > 0)
  const skippedCount = accounts.length - filtered.length
  return {
    accounts: filtered,
    skippedCount,
    reason: accounts.length > 0 && filtered.length === 0
      ? requestCapabilityMismatchReason(req, accounts)
      : undefined
  }
}

function requestCapabilityMismatchReason(req: Request, accounts: UpstreamAccount[]): GatewayAccountCapabilityFilterReason {
  if (isOpenAIResponsesCompactRequest(req) && accounts.every(isOpenAIResponsesChatBridgeAccount)) {
    return 'responses_compact_not_supported_by_chat_bridge'
  }
  return 'request_capability_mismatch'
}
