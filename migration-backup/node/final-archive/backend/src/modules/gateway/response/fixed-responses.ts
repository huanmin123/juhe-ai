import type { Request, Response } from 'express'

import type { GroupUsageAccessMetadata } from '../../../storage/repositories.js'
import { responseHeadersToObject, type AuditCaptureContext } from '../audit/capture.service.js'
import { gatewayErrorPayload } from './responses.js'
import { buildOpenAIModelsResponse } from '../protocols/openai-v1/route-helpers.js'
import { buildAnthropicModelsResponse } from '../protocols/anthropic-v1/route-helpers.js'
import { buildGeminiModelsResponse } from '../protocols/gemini-v1beta/route-helpers.js'
import { extractBearerToken } from '../request/metadata.js'
import type { OpenAIGatewayTrafficSource } from '../usage/traffic-source.js'
import { dispatchUsageRecord } from '../usage/records.js'
import {
  ANTHROPIC_PROVIDER_CODE,
  GEMINI_PROVIDER_CODE,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  normalizeProviderToken
} from '../../../domain/provider-protocol.js'
import type { ProviderCode } from '../../../domain/types.js'
import {
  defaultGatewayUsageProviderCode,
  usageSemanticForProfile
} from '../../providers/drivers/registry.js'
import { listClientModelCatalogAsync } from '../../model-pricing/client-model-catalog.service.js'

export interface OpenAIModelsResponseUsageContext {
  traceId: string
  trafficSource: OpenAIGatewayTrafficSource
  clientIp?: string
  systemAccountId: string
  apiKeyId?: string
  groupId?: string
  groupOwnerSystemAccountId?: string
  groupAccessType?: GroupUsageAccessMetadata['groupAccessType']
  groupAuthorizationId?: string
  groupAuthorizationSourceType?: GroupUsageAccessMetadata['groupAuthorizationSourceType']
  groupAuthorizationSourceTeamId?: string
  providerCode?: string
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
  endpoint: string
}

interface SendOpenAIModelsGatewayResponseInput {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: OpenAIModelsResponseUsageContext
  providerCodes?: string[]
  startedAt: number
}

interface SendPublicModelsGatewayResponseInput {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  protocol: 'openai' | 'anthropic' | 'gemini'
  startedAt: number
}

export interface SendAuthenticatedModelsGatewayResponseInput extends SendOpenAIModelsGatewayResponseInput {
  protocol: 'openai' | 'anthropic' | 'gemini'
}

export function finalizeGatewayAuthFailureAudit(
  req: Request,
  res: Response,
  auditCapture: AuditCaptureContext
): void {
  auditCapture.finalizeLazy(() => {
    const locals = res.locals as Record<string, unknown>
    const authErrorMessage = typeof locals.gatewayAuthFailureErrorMessage === 'string'
      ? locals.gatewayAuthFailureErrorMessage
      : extractBearerToken(req.header('authorization')) ? 'API Key 无效' : '缺少访问令牌'
    const authErrorCode = typeof locals.gatewayAuthFailureErrorCode === 'string'
      ? locals.gatewayAuthFailureErrorCode
      : 'invalid_request_error'
    return {
      outcome: 'gateway_failed',
      success: false,
      statusCode: res.statusCode,
      responseHeaders: responseHeadersToObject(res),
      responseBody: JSON.stringify(gatewayErrorPayload(authErrorMessage, 'invalid_request_error', authErrorCode)),
      responsePartType: 'gateway_error',
      errorPhase: 'auth',
      errorCode: authErrorCode,
      errorMessage: authErrorMessage
    }
  })
}

export async function sendOpenAIModelsGatewayResponse(input: SendOpenAIModelsGatewayResponseInput): Promise<void> {
  await sendModelsGatewayResponse(input, 'openai')
}

export async function sendAnthropicModelsGatewayResponse(input: SendOpenAIModelsGatewayResponseInput): Promise<void> {
  await sendModelsGatewayResponse(input, 'anthropic')
}

export async function sendGeminiModelsGatewayResponse(input: SendOpenAIModelsGatewayResponseInput): Promise<void> {
  await sendModelsGatewayResponse(input, 'gemini')
}

export async function sendPublicModelsGatewayResponse(input: SendPublicModelsGatewayResponseInput): Promise<void> {
  const providerCode = publicModelsProviderCode(input.protocol)
  await sendModelsGatewayResponsePayload({
    ...input,
    providerCode
  })
}

export async function sendAuthenticatedModelsGatewayResponse(
  input: SendAuthenticatedModelsGatewayResponseInput
): Promise<void> {
  await sendModelsGatewayResponse(input, input.protocol)
}

async function sendModelsGatewayResponse(input: SendOpenAIModelsGatewayResponseInput, protocol: 'openai' | 'anthropic' | 'gemini'): Promise<void> {
  const { req, res, auditCapture, usageContext, providerCodes, startedAt } = input
  const normalizedProviderCodes = normalizedProviderCodeList(providerCodes)
  const providerCode = modelsUsageProviderCode(normalizedProviderCodes, usageContext.providerCode)
  const responsePayload = await sendModelsGatewayResponsePayload({
    req,
    res,
    auditCapture,
    startedAt,
    protocol,
    providerCode,
    systemAccountId: usageContext.systemAccountId,
    providerCodes: normalizedProviderCodes
  })
  await dispatchUsageRecord({
    ...usageContext,
    providerCode,
    usageSemantic: usageSemanticForProfile({
      providerCode,
      providerProtocolProfileId: usageContext.providerProtocolProfileId,
      protocolCode: usageContext.protocolCode,
      protocolVersion: usageContext.protocolVersion
    }),
    stream: false,
    statusCode: 200,
    success: true,
    firstTokenMs: Date.now() - startedAt,
    durationMs: Date.now() - startedAt
  })
}

async function sendModelsGatewayResponsePayload(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  protocol: 'openai' | 'anthropic' | 'gemini'
  providerCode: string
  systemAccountId?: string
  providerCodes?: string[]
  startedAt: number
}): Promise<unknown> {
  const { req, res, auditCapture, protocol, systemAccountId, startedAt } = input
  const catalog = await listClientModelCatalogAsync({
    ...(systemAccountId ? { systemAccountId } : {}),
    providerCodes: input.providerCodes
  })
  const responsePayload = protocol === 'anthropic'
    ? buildAnthropicModelsResponse(catalog)
    : protocol === 'gemini'
      ? buildGeminiModelsResponse(catalog)
      : buildOpenAIModelsResponse(catalog, req)
  if (systemAccountId) {
    setAuthenticatedModelsClientCacheHeaders(res)
  }
  res.status(200).json(responsePayload)
  auditCapture.finalizeLazy(() => ({
    outcome: 'success',
    success: true,
    statusCode: 200,
    responseHeaders: responseHeadersToObject(res),
    responseBody: JSON.stringify(responsePayload),
    responsePartType: 'gateway_response',
    firstTokenMs: Date.now() - startedAt
  }))
  return responsePayload
}

export function setAuthenticatedModelsClientCacheHeaders(res: Response): void {
  res.setHeader('Cache-Control', 'private, no-cache')
  const varyHeaders = [
    'Authorization',
    'X-API-Key',
    'X-Goog-API-Key',
    'X-Juhe-Client-Profile',
    'Anthropic-Version',
    'Anthropic-Beta',
    'X-Claude-Code-Session-Id',
    'X-Claude-Code-Agent-Id',
    'Originator',
    'User-Agent',
    'X-Codex-Client'
  ]
  const existing = String(res.getHeader('Vary') ?? '')
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean)
  const merged = new Map(existing.map((item) => [item.toLowerCase(), item]))
  for (const header of varyHeaders) {
    merged.set(header.toLowerCase(), header)
  }
  res.setHeader('Vary', [...merged.values()].join(', '))
}

function normalizedProviderCodeList(providerCodes: readonly string[] | undefined): string[] {
  const codes = new Set<string>()
  for (const item of providerCodes ?? []) {
    const providerCode = normalizeProviderToken(item)
    if (providerCode) {
      codes.add(providerCode)
    }
  }
  return [...codes]
}

function modelsUsageProviderCode(providerCodes: readonly string[], fallback: string | undefined): string {
  return providerCodes[0] ?? fallback ?? defaultGatewayUsageProviderCode()
}

function publicModelsProviderCode(protocol: 'openai' | 'anthropic' | 'gemini'): ProviderCode {
  if (protocol === 'anthropic') return ANTHROPIC_PROVIDER_CODE
  if (protocol === 'gemini') return GEMINI_PROVIDER_CODE
  return OPENAI_COMPATIBLE_PROVIDER_CODE
}
