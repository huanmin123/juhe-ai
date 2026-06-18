import type { Request } from 'express'

import {
  accountSupportsOpenAIEndpointMode,
  openAIEndpointModeForRequestShape
} from '../../../domain/openai-endpoint-modes.js'
import {
  accountSupportsAnthropicEndpointMode,
  anthropicEndpointModeForRequestShape
} from '../../../domain/anthropic-endpoint-modes.js'
import {
  isAnthropicProtocolProfile
} from '../../../domain/provider-protocol.js'
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
    const mode = gatewayEndpointModeForRequestShape(req, account)
    if (!mode) {
      return true
    }
    if (isAnthropicProtocolProfile(account)) {
      return accountSupportsAnthropicEndpointMode({
        mode,
        supportedEndpointModes: account.supportedEndpointModes,
        credentials: account.credentials,
        providerCode: account.providerCode,
        accountType: account.type,
        protocolCode: account.protocolCode,
        protocolVersion: account.protocolVersion
      })
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

function gatewayEndpointModeForRequestShape(req: Request, account: UpstreamAccount) {
  if (isAnthropicProtocolProfile(account)) {
    return anthropicEndpointModeForRequestShape({
      endpoint: req.path || req.originalUrl.split('?', 1)[0],
      stream: isEffectiveOpenAIStreamRequest(req, account)
    })
  }
  return openAIEndpointModeForRequestShape({
    endpoint: req.path || req.originalUrl.split('?', 1)[0],
    stream: isEffectiveOpenAIStreamRequest(req, account)
  })
}
