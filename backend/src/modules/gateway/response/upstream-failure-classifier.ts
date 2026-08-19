import type { GatewayUpstreamFailureMetricReasonClass } from '../../../shared/prometheus-metrics.js'

export type GatewayUpstreamFailureClass =
  | 'opaque_upstream_response'
  | 'transport'
  | 'unknown'

export interface GatewayUpstreamFailureClassificationInput {
  phase: 'upstream_request' | 'upstream_response'
  statusCode?: number
  errorCode?: string
}

export interface GatewayUpstreamFailureClassification {
  failureClass: GatewayUpstreamFailureClass
  metricReasonClass: GatewayUpstreamFailureMetricReasonClass
  classificationReason: string
}

export function classifyGatewayUpstreamFailure(
  input: GatewayUpstreamFailureClassificationInput
): GatewayUpstreamFailureClassification {
  if (input.phase === 'upstream_request') {
    return observation('transport', classifyMetricReason(input), 'upstream_transport_failure')
  }
  if (input.phase === 'upstream_response') {
    return observation('opaque_upstream_response', classifyMetricReason(input), 'opaque_upstream_response_failure')
  }
  return observation('unknown', 'unknown', 'unknown_failure_phase')
}

function observation(
  failureClass: GatewayUpstreamFailureClass,
  metricReasonClass: GatewayUpstreamFailureMetricReasonClass,
  classificationReason: string
): GatewayUpstreamFailureClassification {
  return {
    failureClass,
    metricReasonClass,
    classificationReason
  }
}

function classifyMetricReason(
  input: GatewayUpstreamFailureClassificationInput
): GatewayUpstreamFailureMetricReasonClass {
  const errorCode = input.errorCode?.trim().toLowerCase()
  if (errorCode === 'insufficient_user_quota' || errorCode === 'insufficient_quota' || errorCode === 'quota_exceeded' || errorCode === 'quota_exhausted' || errorCode === 'billing_hard_limit_reached') return 'quota'
  if (errorCode === 'rate_limit_exceeded' || errorCode === 'rate_limited' || errorCode === 'too_many_requests') return 'rate_limit'
  if (errorCode === 'invalid_api_key' || errorCode === 'invalid_authentication' || errorCode === 'authentication_error' || errorCode === 'access_denied' || errorCode === 'permission_denied') return 'authorization'
  if (errorCode === 'upstream_protocol_failure' || errorCode === 'upstream_protocol_error') return 'protocol'
  if (errorCode === 'first_byte_timeout' || errorCode === 'normal_route_first_byte_timeout' || errorCode === 'etimedout' || errorCode === 'timeout') return 'timeout'
  if (input.phase === 'upstream_request') return 'transport'
  if (input.statusCode !== undefined && input.statusCode >= 500) return 'upstream_5xx'
  if (input.statusCode !== undefined && input.statusCode >= 400) return 'upstream_4xx'
  return 'unknown'
}
