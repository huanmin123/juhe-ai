export type GatewayUpstreamFailureClass =
  | 'opaque_upstream_response'
  | 'transport'
  | 'unknown'

export interface GatewayUpstreamFailureClassificationInput {
  phase: 'upstream_request' | 'upstream_response'
}

export interface GatewayUpstreamFailureClassification {
  failureClass: GatewayUpstreamFailureClass
  classificationReason: string
}

export function classifyGatewayUpstreamFailure(
  input: GatewayUpstreamFailureClassificationInput
): GatewayUpstreamFailureClassification {
  if (input.phase === 'upstream_request') {
    return observation('transport', 'upstream_transport_failure')
  }
  if (input.phase === 'upstream_response') {
    return observation('opaque_upstream_response', 'opaque_upstream_response_failure')
  }
  return observation('unknown', 'unknown_failure_phase')
}

function observation(
  failureClass: GatewayUpstreamFailureClass,
  classificationReason: string
): GatewayUpstreamFailureClassification {
  return {
    failureClass,
    classificationReason
  }
}
