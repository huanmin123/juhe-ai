import type { Request } from 'express'

import { dispatchAccountHealthCheck } from '../../internal-api/account-health-check-dispatch.service.js'
import type { OpenAIGatewayTrafficSource } from '../usage/traffic-source.js'

const requestFailureHealthCheckDispatched = Symbol('requestFailureHealthCheckDispatched')

export function dispatchRequestFailureAccountHealthCheck(
  req: Request,
  trafficSource: OpenAIGatewayTrafficSource,
  accountId: string
): boolean {
  if (trafficSource !== 'gateway') return false
  const request = req as Request & { [requestFailureHealthCheckDispatched]?: boolean }
  if (request[requestFailureHealthCheckDispatched]) return false
  const dispatched = dispatchAccountHealthCheck(accountId, 'request_failure')
  if (dispatched) request[requestFailureHealthCheckDispatched] = true
  return dispatched
}
