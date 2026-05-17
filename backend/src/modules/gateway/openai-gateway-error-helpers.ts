import type { Response } from 'express'

import type { UpstreamAttempt } from './openai-gateway-usage.js'
import { parseErrorPayload } from './account-error-policy.service.js'
import { copyResponseHeaders } from './openai-gateway-upstream.js'
import { gatewayErrorPayload } from './openai-gateway-responses.js'

export interface ClientVisibleUpstreamErrorResponse {
  statusCode: number
  headers: Headers
  body: Buffer
  bodyText: string
}

export interface UpstreamFailureSignature {
  key: string
  label: string
}

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
  const parsedError = bodyText ? parseErrorPayload(bodyText, responseHeaders) : {}
  const errorMessage = stringValue(parsedError.message) || lastAttempt.message || fallbackMessage
  const errorType = stringValue(parsedError.type) || stringValue(parsedError.code) || 'upstream_error'
  const parsedPayload = bodyText ? parseJsonObject(bodyText) : undefined
  const payload = hasErrorObject(parsedPayload)
    ? parsedPayload as GatewayDiagnosticErrorPayload
    : gatewayErrorPayload(errorMessage, errorType) as GatewayDiagnosticErrorPayload

  payload.upstream = {
    statusCode: lastAttempt.status,
    accountId: lastAttempt.accountId,
    accountName: lastAttempt.accountName,
    upstreamUrl: lastAttempt.upstreamUrl
  }

  return { statusCode, payload, errorMessage }
}

export function parseClientVisibleUpstreamErrorForAudit(
  response: ClientVisibleUpstreamErrorResponse,
  fallbackMessage: string
): { errorMessage: string; errorCode?: string } {
  const parsedError = response.bodyText ? parseErrorPayload(response.bodyText, response.headers) : {}
  return {
    errorMessage: stringValue(parsedError.message) || fallbackMessage,
    errorCode: stringValue(parsedError.code) || stringValue(parsedError.type) || undefined
  }
}

export function sendRawUpstreamErrorResponse(res: Response, response: ClientVisibleUpstreamErrorResponse): void {
  if (res.writableEnded || res.destroyed) {
    return
  }
  if (!res.headersSent) {
    res.status(response.statusCode)
    copyResponseHeaders({
      status: response.statusCode,
      ok: false,
      headers: response.headers,
      body: null
    }, res)
  }
  res.end(response.body)
}

export function buildUpstreamFailureSignature(headers: Headers, bodyText: string): UpstreamFailureSignature | undefined {
  const parsedError = parseErrorPayload(bodyText, headers)
  const parts = [
    signaturePart('type', parsedError.type),
    signaturePart('code', parsedError.code),
    signaturePart('message', parsedError.message)
  ].filter((part): part is string => Boolean(part))

  if (parts.length > 0) {
    return {
      key: parts.join('|'),
      label: signatureLabel(parts.join(' '))
    }
  }

  const normalizedBody = normalizeFailureSignatureText(bodyText)
  return normalizedBody
    ? {
      key: 'body:' + normalizedBody,
      label: signatureLabel(normalizedBody)
    }
    : undefined
}

export function headersFromObjectForPolicy(headers: Record<string, string | string[]>): Headers {
  const output = new Headers()
  for (const [name, value] of Object.entries(headers)) {
    output.set(name, Array.isArray(value) ? value.join(', ') : value)
  }
  return output
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

function signaturePart(name: string, value: unknown): string | undefined {
  const normalized = normalizeFailureSignatureText(typeof value === 'string' ? value : '')
  return normalized ? `${name}:${normalized}` : undefined
}

function normalizeFailureSignatureText(value: string): string {
  return value.trim().replace(/\s+/g, ' ').toLowerCase().slice(0, 2000)
}

function signatureLabel(value: string): string {
  return value.length > 240 ? value.slice(0, 240) + '...' : value
}
