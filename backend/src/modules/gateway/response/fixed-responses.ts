import type { Request, Response } from 'express'

import type { GroupUsageAccessMetadata } from '../../../storage/repositories.js'
import { responseHeadersToObject, type AuditCaptureContext } from '../audit/capture.service.js'
import { gatewayErrorPayload } from './responses.js'
import { buildOpenAIModelsResponse, isCodexModelsRequest } from '../protocols/openai-v1/route-helpers.js'
import { buildAnthropicModelsResponse } from '../protocols/anthropic-v1/route-helpers.js'
import { buildGeminiModelsResponse } from '../protocols/gemini-v1beta/route-helpers.js'
import { listCachedProviderModelCatalogAsync } from '../runtime/runtime-cache.service.js'
import { extractBearerToken } from '../request/metadata.js'
import type { OpenAIGatewayTrafficSource } from '../usage/traffic-source.js'
import { enqueueUsageRecord } from '../usage/record-queue.service.js'
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
import {
  compareProviderModelCatalogItems,
  type ProviderModelCatalogItem
} from '../../model-pricing/model-catalog.service.js'
import {
  getAuthenticatedModelsResponseCache,
  setAuthenticatedModelsResponseCache
} from './models-response-cache.js'

interface OpenAIModelsResponseUsageContext {
  traceId: string
  trafficSource: OpenAIGatewayTrafficSource
  clientIp?: string
  systemAccountId: string
  apiKeyId?: string
  groupId: string
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

export function finalizeGatewayAuthFailureAudit(
  req: Request,
  res: Response,
  auditCapture: AuditCaptureContext
): void {
  const locals = res.locals as Record<string, unknown>
  const authErrorMessage = typeof locals.gatewayAuthFailureErrorMessage === 'string'
    ? locals.gatewayAuthFailureErrorMessage
    : extractBearerToken(req.header('authorization')) ? 'API Key 无效' : '缺少访问令牌'
  const authErrorCode = typeof locals.gatewayAuthFailureErrorCode === 'string'
    ? locals.gatewayAuthFailureErrorCode
    : 'invalid_request_error'
  const authErrorPayload = gatewayErrorPayload(authErrorMessage, 'invalid_request_error', authErrorCode)
  auditCapture.finalize({
    outcome: 'gateway_failed',
    success: false,
    statusCode: res.statusCode,
    responseHeaders: responseHeadersToObject(res),
    responseBody: JSON.stringify(authErrorPayload),
    responsePartType: 'gateway_error',
    errorPhase: 'auth',
    errorCode: authErrorCode,
    errorMessage: authErrorMessage
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
  await enqueueUsageRecord({
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
  const { req, res, auditCapture, protocol, providerCode, systemAccountId, providerCodes, startedAt } = input
  const cacheKey = systemAccountId
    ? {
        systemAccountId,
        providerCodes: providerCodes?.length ? providerCodes : [providerCode],
        protocol,
        variant: protocol === 'openai' ? (isCodexModelsRequest(req) ? 'codex' as const : 'openai' as const) : 'default' as const
      }
    : undefined
  let responsePayload = cacheKey
    ? await getAuthenticatedModelsResponseCache(cacheKey)
    : undefined
  if (!responsePayload) {
    const catalog = providerCodes?.length
      ? await listProviderScopedModelCatalog({
          providerCodes,
          systemAccountId
        })
      : await listCachedProviderModelCatalogAsync({
          providerCode,
          systemAccountId
        })
    responsePayload = protocol === 'anthropic'
      ? buildAnthropicModelsResponse(catalog)
      : protocol === 'gemini'
        ? buildGeminiModelsResponse(catalog)
        : buildOpenAIModelsResponse(catalog, req)
    if (cacheKey) {
      await setAuthenticatedModelsResponseCache(cacheKey, responsePayload)
    }
  }
  if (cacheKey) {
    res.setHeader('Cache-Control', 'private, max-age=30')
  }
  res.status(200).json(responsePayload)
  auditCapture.finalize({
    outcome: 'success',
    success: true,
    statusCode: 200,
    responseHeaders: responseHeadersToObject(res),
    responseBody: JSON.stringify(responsePayload),
    responsePartType: 'gateway_response',
    firstTokenMs: Date.now() - startedAt
  })
  return responsePayload
}

async function listProviderScopedModelCatalog(input: {
  providerCodes: string[]
  systemAccountId?: string
}): Promise<ProviderModelCatalogItem[]> {
  const providerCodes = normalizedProviderCodeList(input.providerCodes)
  if (!providerCodes.length) {
    return []
  }
  const catalogGroups = await Promise.all(providerCodes.map((providerCode) =>
    listCachedProviderModelCatalogAsync({
      providerCode,
      systemAccountId: input.systemAccountId
    })))
  return mergeModelCatalogItems(catalogGroups.flat())
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

function mergeModelCatalogItems(items: ProviderModelCatalogItem[]): ProviderModelCatalogItem[] {
  const merged = new Map<string, ProviderModelCatalogItem>()
  for (const item of items) {
    const model = item.model.trim()
    if (!model) continue
    const previous = merged.get(model)
    if (!previous || modelCatalogPriority(item) >= modelCatalogPriority(previous)) {
      merged.set(model, item)
    }
  }
  return [...merged.values()].sort(compareProviderModelCatalogItems)
}

function modelCatalogPriority(item: ProviderModelCatalogItem): number {
  if (item.scope === 'personal') return 3
  return 1
}

function modelsUsageProviderCode(providerCodes: readonly string[], fallback: string | undefined): string {
  return providerCodes[0] ?? fallback ?? defaultGatewayUsageProviderCode()
}

function publicModelsProviderCode(protocol: 'openai' | 'anthropic' | 'gemini'): ProviderCode {
  if (protocol === 'anthropic') return ANTHROPIC_PROVIDER_CODE
  if (protocol === 'gemini') return GEMINI_PROVIDER_CODE
  return OPENAI_COMPATIBLE_PROVIDER_CODE
}
