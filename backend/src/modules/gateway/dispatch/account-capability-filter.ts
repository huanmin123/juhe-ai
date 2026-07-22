import type { Request } from 'express'

import type { ClientCompatibilityCapability } from '../../../domain/types.js'
import type { DispatchAccountSecret } from '../../../storage/repositories.js'
import {
  getGatewayRequestBodyState,
  type GatewayRawBodyRequest
} from '../request/body.js'
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
  accounts: DispatchAccountSecret[],
  options: {
    requestClientCompatibility?: ClientCompatibilityCapability
    requestModelOverride?: string
  } = {}
): GatewayAccountCapabilityFilterResult {
  const capabilityReq = options.requestModelOverride
    ? gatewayRequestWithModelOverride(req, options.requestModelOverride)
    : req
  const filtered = accounts.filter((account) => accountSupportsGatewayRequest(capabilityReq, account, {
    requestClientCompatibility: options.requestClientCompatibility
  }))
  const skippedCount = accounts.length - filtered.length
  return {
    accounts: filtered,
    skippedCount,
    reason: accounts.length > 0 && filtered.length === 0
      ? gatewayRequestCapabilityMismatchReason(capabilityReq, accounts)
      : undefined
  }
}

function gatewayRequestWithModelOverride(req: Request, model: string): Request {
  const targetModel = model.trim()
  if (!targetModel) return req
  const request = req as GatewayRawBodyRequest
  const sourceBody = isPlainObject(request.body)
    ? request.body
    : request.gatewayParsedJsonBodyAvailable && isPlainObject(request.gatewayParsedJsonBody)
      ? request.gatewayParsedJsonBody
      : undefined
  const body = {
    ...(sourceBody ?? {}),
    model: targetModel
  }
  const bodyState = getGatewayRequestBodyState(req)
  const output = Object.create(req) as GatewayRawBodyRequest
  output.body = body
  output.gatewayParsedJsonBodyAvailable = true
  output.gatewayParsedJsonBody = body
  output.gatewayUpstreamBodyCache = undefined
  if (bodyState) {
    output.gatewayRequestBody = {
      ...bodyState,
      model: targetModel
    }
  }
  return output as Request
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
