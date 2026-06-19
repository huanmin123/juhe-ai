import type { UpstreamAttempt } from './attempt.js'
import { gatewayErrorPayload } from '../response/responses.js'
import { sanitizeDiagnosticPayload } from '../audit/payload-sanitizer.js'
import { sanitizeUrlCredentialsForLog } from '../../../shared/request-context.js'
import { parseGatewayProtocolErrorPayload } from '../protocols/registry.js'

type GatewayDiagnosticErrorPayload = ReturnType<typeof gatewayErrorPayload> & {
  upstream?: {
    statusCode?: number
    accountId: string
    accountName: string
    upstreamUrl: string
  }
}

export function buildDiagnosticUpstreamError(
  lastAttempt: UpstreamAttempt | undefined,
  fallbackMessage: string
): { statusCode: number; payload: GatewayDiagnosticErrorPayload; errorMessage: string } | undefined {
  if (!lastAttempt) return undefined

  const statusCode = isHttpStatusCode(lastAttempt.status) ? lastAttempt.status : 503
  const bodyText = lastAttempt.responseBodyText?.trim()
  const responseHeaders = headersFromObject(lastAttempt.responseHeaders)
  const parsedError = bodyText ? parseGatewayProtocolErrorPayload(undefined, bodyText, responseHeaders) : {}
  const errorMessage = sanitizeDiagnosticPayload(stringValue(parsedError.message) || lastAttempt.message || fallbackMessage)
  const errorType = stringValue(parsedError.type) || stringValue(parsedError.code) || 'upstream_error'
  const parsedPayload = bodyText ? parseJsonObject(bodyText) : undefined
  const payload = hasErrorObject(parsedPayload)
    ? sanitizeDiagnosticPayload(parsedPayload) as GatewayDiagnosticErrorPayload
    : gatewayErrorPayload(errorMessage, errorType) as GatewayDiagnosticErrorPayload

  payload.upstream = {
    statusCode: lastAttempt.status,
    accountId: lastAttempt.accountId,
    accountName: lastAttempt.accountName,
    upstreamUrl: sanitizeUrlCredentialsForLog(lastAttempt.upstreamUrl) ?? lastAttempt.upstreamUrl
  }

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

function parseJsonObject(text: string): Record<string, unknown> | undefined {
  try {
    const value = JSON.parse(text) as unknown
    return typeof value === 'object' && value !== null && !Array.isArray(value)
      ? value as Record<string, unknown>
      : undefined
  } catch {
    return undefined
  }
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
