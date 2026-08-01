import type { UpstreamAttempt } from './attempt.js'
import { gatewayErrorPayload } from '../response/responses.js'
import { sanitizeDiagnosticPayload } from '../diagnostics/diagnostic-sanitizer.js'
import { parseGatewayProtocolErrorPayloadFromJsonValue } from '../protocols/registry.js'
import { parseGatewayNonStreamJsonBody } from '../response/non-stream-json-body.js'

type GatewayDiagnosticErrorPayload = ReturnType<typeof gatewayErrorPayload>

export function buildDiagnosticUpstreamError(
  lastAttempt: UpstreamAttempt | undefined,
  fallbackMessage: string
): { statusCode: number; payload: GatewayDiagnosticErrorPayload; errorMessage: string } | undefined {
  if (!lastAttempt) return undefined

  const transportFailure = lastAttempt.transportFailureKind
  const statusCode = isHttpStatusCode(lastAttempt.status)
    ? lastAttempt.status
    : transportFailure === 'timeout' ? 504 : transportFailure ? 502 : 503
  const bodyText = lastAttempt.responseBodyText?.trim()
  const responseHeaders = headersFromObject(lastAttempt.responseHeaders)
  const parsedJsonBody = lastAttempt.parsedResponseBody
    ?? (bodyText ? parseGatewayNonStreamJsonBody(bodyText, responseHeaders) : undefined)
  const parsedPayload = parsedJsonBody?.status === 'valid' && isJsonObject(parsedJsonBody.value)
    ? parsedJsonBody.value
    : undefined
  const parsedError = parsedPayload
    ? parseGatewayProtocolErrorPayloadFromJsonValue(lastAttempt, parsedPayload)
    : {}
  const errorMessage = sanitizeDiagnosticPayload(stringValue(parsedError.message) || lastAttempt.message || fallbackMessage)
  const errorType = stringValue(parsedError.type)
    || (transportFailure === 'timeout' ? 'upstream_timeout_error' : transportFailure ? 'upstream_transport_error' : '')
    || stringValue(parsedError.code)
    || 'upstream_error'
  const errorCode = stringValue(parsedError.code)
    || lastAttempt.errorCode
    || (transportFailure === 'timeout' ? 'upstream_timeout' : transportFailure ? `upstream_${transportFailure}` : undefined)
  const payload = hasErrorObject(parsedPayload)
    ? sanitizeDiagnosticPayload(parsedPayload) as GatewayDiagnosticErrorPayload
    : gatewayErrorPayload(errorMessage, errorType, errorCode) as GatewayDiagnosticErrorPayload

  return { statusCode, payload, errorMessage }
}

function headersFromObject(headers?: Record<string, string>): Headers {
  const output = new Headers()
  if (!headers) return output
  for (const [name, value] of Object.entries(headers)) {
    output.set(name, value)
  }
  return output
}

function isJsonObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function hasErrorObject(value: Record<string, unknown> | undefined): boolean {
  return typeof value?.error === 'object' && value.error !== null && !Array.isArray(value.error)
}

function isHttpStatusCode(value: unknown): value is number {
  return typeof value === 'number' && value >= 400 && value <= 599
}

function stringValue(value: unknown): string {
  return typeof value === 'string' && value.trim() ? value.trim() : ''
}
