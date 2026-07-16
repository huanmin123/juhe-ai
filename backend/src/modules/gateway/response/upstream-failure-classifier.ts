export type GatewayUpstreamFailureClass =
  | 'client_lifecycle'
  | 'request_semantic'
  | 'credential'
  | 'rate_limit'
  | 'upstream_service'
  | 'transport'
  | 'unknown'

export interface GatewayUpstreamFailureClassificationInput {
  phase: 'client_lifecycle' | 'upstream_request' | 'upstream_response'
  statusCode?: number
  errorCode?: string
  errorType?: string
  hasAlternativeApiKeys?: boolean
}

export interface GatewayUpstreamFailureClassification {
  failureClass: GatewayUpstreamFailureClass
  classificationReason: string
  wouldAvoidApiKey: boolean
  wouldAvoidAccount: boolean
  wouldAvoidUpstreamBucket: boolean
}

const requestSemanticErrorCodes = new Set([
  'content_policy_violation',
  'context_length_exceeded',
  'invalid_prompt',
  'invalid_request_error',
  'model_not_found',
  'unsupported_value'
])

const credentialErrorCodes = new Set([
  'authentication_error',
  'invalid_api_key',
  'invalid_authentication',
  'permission_denied',
  'unauthorized'
])

const rateLimitErrorCodes = new Set([
  'insufficient_quota',
  'rate_limit_error',
  'rate_limit_exceeded'
])

export function classifyGatewayUpstreamFailure(
  input: GatewayUpstreamFailureClassificationInput
): GatewayUpstreamFailureClassification {
  if (input.phase === 'client_lifecycle') {
    return observation('client_lifecycle', 'client_lifecycle_failure')
  }

  if (input.phase === 'upstream_request') {
    return observation('transport', 'upstream_transport_failure', {
      wouldAvoidAccount: true,
      wouldAvoidUpstreamBucket: true
    })
  }

  const errorCode = normalizeFailureIdentifier(input.errorCode)
  const errorType = normalizeFailureIdentifier(input.errorType)
  if (requestSemanticErrorCodes.has(errorCode) || requestSemanticErrorCodes.has(errorType)) {
    return observation('request_semantic', 'explicit_request_error')
  }

  if (credentialErrorCodes.has(errorCode) || credentialErrorCodes.has(errorType) || input.statusCode === 401 || input.statusCode === 403) {
    if (input.hasAlternativeApiKeys) {
      return observation('credential', 'credential_error_with_alternative_key', {
        wouldAvoidApiKey: true
      })
    }
    return observation('credential', 'credential_error_without_alternative_key', {
      wouldAvoidAccount: true
    })
  }

  if (rateLimitErrorCodes.has(errorCode) || rateLimitErrorCodes.has(errorType) || input.statusCode === 429) {
    return observation('rate_limit', 'upstream_rate_limit', {
      wouldAvoidAccount: true
    })
  }

  if (typeof input.statusCode === 'number' && input.statusCode >= 500) {
    return observation('upstream_service', 'upstream_server_error', {
      wouldAvoidAccount: true,
      wouldAvoidUpstreamBucket: true
    })
  }

  return observation('unknown', 'unclassified_upstream_response')
}

function observation(
  failureClass: GatewayUpstreamFailureClass,
  classificationReason: string,
  overrides: Partial<Omit<GatewayUpstreamFailureClassification, 'failureClass' | 'classificationReason'>> = {}
): GatewayUpstreamFailureClassification {
  return {
    failureClass,
    classificationReason,
    wouldAvoidApiKey: false,
    wouldAvoidAccount: false,
    wouldAvoidUpstreamBucket: false,
    ...overrides
  }
}

function normalizeFailureIdentifier(value: string | undefined): string {
  return value?.trim().toLowerCase() ?? ''
}
