import { createHash } from 'node:crypto'
import type { Request } from 'express'

import {
  GEMINI_INTERACTIONS_FAMILY,
  accountSupportsGeminiEndpointMode,
  geminiEndpointFamilyFromPath,
  geminiEndpointModeForRequestShape
} from '../../../../domain/gemini-endpoint-modes.js'
import {
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_PROVIDER_CODE,
  GEMINI_PROTOCOL_CODE,
  GEMINI_PROTOCOL_VERSION,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
  isGeminiProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import {
  buildGeminiUpstreamUrl,
  buildGeminiUpstreamUrlsForAccount,
  isGeminiModelsRequest,
  isGeminiNativeRequest
} from '../../../gateway/protocols/gemini-v1beta/route-helpers.js'
import {
  geminiGenerateContentModelMappedUpstreamPathAndQuery,
  isOpenAIOrAnthropicToGeminiGenerateContentModelMapping,
  resolveOpenAIAccountModelMapping,
  resolveOpenAIRequestModelMapping
} from '../../../gateway/protocols/openai-v1/model-mapping.js'
import { requestModel, requestStream } from '../../../gateway/request/metadata.js'
import {
  getGatewayRequestBodyState,
  type GatewayRawBodyRequest
} from '../../../gateway/request/body.js'
import {
  isGatewayJsonWorkerInvalidJsonError,
  isGatewayJsonWorkerQueueFullError,
  parseGatewayRequestJsonBody
} from '../../../gateway/request/json-parser.js'
import { serializeGatewayJsonObject } from '../../../gateway/request/serialized-json-body.js'
import { GatewayRequestValidationError } from '../../../gateway/request/validation-error.js'
import {
  buildUpstreamRequestBody,
  copySafeUpstreamRequestHeaders,
  isEffectiveOpenAIStreamRequest
} from '../../../gateway/upstream/request.js'
import {
  buildOpenAIOrAnthropicToGeminiNativeBody,
  openAIOrAnthropicToGeminiNativeRequiredEndpointMode,
  prepareOpenAIOrAnthropicToGeminiNativeHeaders,
  transformGeminiNativeTargetBridgeUpstreamResponse
} from '../_shared/openai-anthropic-gemini-native-bridge.js'
import { prepareGeminiAccountBeforeDispatch } from './oauth-dispatch-preparation.js'
import {
  GEMINI_CODE_ASSIST_STREAM_URL,
  buildGeminiCodeAssistRequestParts,
  geminiCodeAssistProjectId,
  transformGeminiCodeAssistUpstreamResponse,
  usesGeminiCodeAssistRuntime
} from './code-assist-runtime.js'
import type { ProviderDriver, ProviderDriverAccount } from '../_shared/types.js'
import { applyProviderAccountRequestOverridesToBody } from '../_shared/provider-request-overrides.js'

function geminiEndpointModeForGatewayRequest(req: Request, account: ProviderDriverAccount) {
  const mapping = resolveOpenAIRequestModelMapping(req, account)
  if (isOpenAIOrAnthropicToGeminiGenerateContentModelMapping(mapping)) {
    return openAIOrAnthropicToGeminiNativeRequiredEndpointMode(req)
  }
  return geminiEndpointModeForRequestShape({
    endpoint: req.path || req.originalUrl.split('?', 1)[0],
    stream: isEffectiveOpenAIStreamRequest(req, account)
  })
}

function isGeminiNativeGenerateContentModelMapping(
  mapping: ReturnType<typeof resolveOpenAIRequestModelMapping> | ReturnType<typeof resolveOpenAIAccountModelMapping>
): mapping is NonNullable<typeof mapping> {
  return (
    mapping?.sourceEndpointFamily === GEMINI_GENERATE_CONTENT_FAMILY
    || mapping?.sourceEndpointFamily === GEMINI_STREAM_GENERATE_CONTENT_FAMILY
  ) && mapping.upstreamEndpointFamily === GEMINI_GENERATE_CONTENT_FAMILY
}

export const geminiProviderDriver: ProviderDriver = {
  id: 'gemini',
  providerCode: GEMINI_PROVIDER_CODE,
  protocolCode: GEMINI_PROTOCOL_CODE,
  protocolVersion: GEMINI_PROTOCOL_VERSION,
  usageSemantic: 'gemini',
  profileIds: [
    GEMINI_NATIVE_V1BETA_PROFILE_ID
  ],
  supportsProfile(profile) {
    const profileId = profile?.providerProtocolProfileId ?? profile?.id
    return profile?.providerCode === GEMINI_PROVIDER_CODE
      && profileId === GEMINI_NATIVE_V1BETA_PROFILE_ID
      && isGeminiProtocolProfile(profile)
  },
  resolveUsageModel(account, requestedModel, sourceEndpointFamily) {
    const mapping = resolveOpenAIAccountModelMapping(account, requestedModel, sourceEndpointFamily)
    if (isOpenAIOrAnthropicToGeminiGenerateContentModelMapping(mapping) || isGeminiNativeGenerateContentModelMapping(mapping)) {
      return {
        upstreamModel: mapping.upstreamModel,
        modelMappingApplied: true,
        modelMappingSource: mapping.runtimeSource ?? 'account',
        sourceEndpointFamily: mapping.sourceEndpointFamily,
        upstreamEndpointFamily: mapping.upstreamEndpointFamily
      }
    }
    return {
      upstreamModel: requestedModel,
      modelMappingApplied: false
    }
  },
  async prepareAccountBeforeDispatch(account, context) {
    if (account.type !== 'google_oauth') return account
    return await prepareGeminiAccountBeforeDispatch(account, context.signal)
  },
  buildUpstreamUrls(account: DispatchAccountSecret, req: Request): string[] {
    const mapping = resolveOpenAIRequestModelMapping(req, account)
    if (usesGeminiCodeAssistRuntime(account)) {
      return isGeminiCodeAssistGenerationRequest(req, mapping) ? [GEMINI_CODE_ASSIST_STREAM_URL] : []
    }
    if (isOpenAIOrAnthropicToGeminiGenerateContentModelMapping(mapping) || isGeminiNativeGenerateContentModelMapping(mapping)) {
      return [buildGeminiUpstreamUrl(account.baseUrl, geminiGenerateContentModelMappedUpstreamPathAndQuery(req, mapping))]
    }
    return buildGeminiUpstreamUrlsForAccount(account, req)
  },
  async buildUpstreamRequestParts(req, account, _identity, signal) {
    if (account.type !== 'api_key' && account.type !== 'google_oauth') {
      throw new Error('Gemini 原生协议当前仅支持 API Key 或 Google OAuth 账户')
    }
    const mapping = resolveOpenAIRequestModelMapping(req, account)
    if (usesGeminiCodeAssistRuntime(account)) {
      if (!isGeminiCodeAssistGenerationRequest(req, mapping)) {
        throw new Error('Gemini Code Assist / Google One OAuth 仅支持 generateContent 与 streamGenerateContent')
      }
      const preparedBody = isOpenAIOrAnthropicToGeminiGenerateContentModelMapping(mapping)
        ? await applyProviderAccountRequestOverridesToBody(await buildOpenAIOrAnthropicToGeminiNativeBody(req, {
            mapping,
            providerName: account.name
          }, signal), {
            account,
            upstreamModel: mapping.upstreamModel,
            wireFormat: 'gemini_generate_content',
            signal
          })
        : await applyProviderAccountRequestOverridesToBody(buildUpstreamRequestBody(req), {
            account,
            upstreamModel: requestModel(req),
            wireFormat: 'gemini_generate_content',
            signal
          })
      return buildGeminiCodeAssistRequestParts({
        accessToken: account.apiKey,
        projectId: geminiCodeAssistProjectId(account),
        model: mapping?.upstreamModel ?? requestModel(req) ?? '',
        body: preparedBody
      })
    }
    const headers = copySafeUpstreamRequestHeaders(req.headers)
    if (account.type === 'google_oauth') {
      headers.delete('x-goog-api-key')
      headers.set('authorization', `Bearer ${account.apiKey}`)
      const quotaProjectId = textCredential(account.credentials?.quota_project_id)
      if (quotaProjectId) headers.set('x-goog-user-project', quotaProjectId)
    } else {
      headers.set('x-goog-api-key', account.apiKey)
    }
    if (isOpenAIOrAnthropicToGeminiGenerateContentModelMapping(mapping)) {
      prepareOpenAIOrAnthropicToGeminiNativeHeaders(headers, req)
      return {
        headers,
        body: await applyProviderAccountRequestOverridesToBody(await buildOpenAIOrAnthropicToGeminiNativeBody(req, {
          mapping,
          providerName: account.name
        }, signal), {
          account,
          upstreamModel: mapping?.upstreamModel,
          wireFormat: 'gemini_generate_content',
          signal
        })
      }
    }
    if (!headers.get('content-type') && req.method !== 'GET' && req.method !== 'HEAD') {
      headers.set('content-type', 'application/json')
    }
    const endpointFamily = geminiEndpointFamilyFromPath(req.path || req.originalUrl.split('?', 1)[0])
    if (endpointFamily === GEMINI_INTERACTIONS_FAMILY && !headers.has('api-revision')) {
      headers.set('api-revision', '2026-05-20')
    }
    if (endpointFamily === GEMINI_INTERACTIONS_FAMILY && requestStream(req)) {
      headers.set('accept', 'text/event-stream')
    } else if (!headers.get('accept')) {
      headers.set('accept', requestStream(req) || req.originalUrl.includes(':streamGenerateContent') || req.originalUrl.includes('alt=sse')
        ? 'text/event-stream'
        : 'application/json')
    }
    const nativeBody = buildUpstreamRequestBody(req)
    const normalizedNativeBody = await normalizeGeminiInteractionsStreamBody(req, endpointFamily, headers, nativeBody, signal)
    return {
      headers,
      body: endpointFamily === GEMINI_GENERATE_CONTENT_FAMILY || endpointFamily === GEMINI_STREAM_GENERATE_CONTENT_FAMILY
        ? await applyProviderAccountRequestOverridesToBody(nativeBody, {
            account,
            upstreamModel: requestModel(req),
            wireFormat: 'gemini_generate_content',
            signal
          })
        : normalizedNativeBody
    }
  },
  transformUpstreamResponse(req, account, response) {
    const mapping = resolveOpenAIRequestModelMapping(req, account)
    const codeAssistResponse = usesGeminiCodeAssistRuntime(account)
      ? transformGeminiCodeAssistUpstreamResponse(response, {
          downstreamStream: geminiCodeAssistDownstreamStream(req, mapping)
        })
      : response
    if (isOpenAIOrAnthropicToGeminiGenerateContentModelMapping(mapping)) {
      return transformGeminiNativeTargetBridgeUpstreamResponse(req, codeAssistResponse, { mapping })
    }
    return codeAssistResponse
  },
  endpointModeForRequest: geminiEndpointModeForGatewayRequest,
  accountSupportsRequest(req, account) {
    const mapping = resolveOpenAIRequestModelMapping(req, account)
    if (usesGeminiCodeAssistRuntime(account) && !isGeminiCodeAssistGenerationRequest(req, mapping)) {
      return false
    }
    if (isOpenAIOrAnthropicToGeminiGenerateContentModelMapping(mapping)) {
      return accountSupportsGeminiEndpointMode({
        mode: openAIOrAnthropicToGeminiNativeRequiredEndpointMode(req),
        supportedEndpointModes: account.supportedEndpointModes,
        credentials: account.credentials,
        providerCode: account.providerCode,
        accountType: account.type,
        protocolCode: account.protocolCode,
        protocolVersion: account.protocolVersion,
        providerProtocolProfileId: account.providerProtocolProfileId
      })
    }
    if (!isGeminiNativeRequest(req)) return false
    if (isGeminiModelsRequest(req)) {
      return true
    }
    const mode = geminiEndpointModeForGatewayRequest(req, account)
    if (!mode) return false
    return accountSupportsGeminiEndpointMode({
      mode,
      supportedEndpointModes: account.supportedEndpointModes,
      credentials: account.credentials,
      providerCode: account.providerCode,
      accountType: account.type,
      protocolCode: account.protocolCode,
      protocolVersion: account.protocolVersion,
      providerProtocolProfileId: account.providerProtocolProfileId
    })
  }
}

/** Retained for regression compatibility; dispatch refresh now uses the shared persisted lock path. */
export function geminiGoogleOAuthProviderFingerprint(input: {
  credentials?: {
    access_token?: unknown
    refresh_token?: unknown
    client_id?: unknown
    client_secret?: unknown
    expires_at?: unknown
    oauth_type?: unknown
  }
  proxyUrl?: string
}): string {
  const credentials = input.credentials ?? {}
  return createHash('sha256').update([
    textCredential(credentials.access_token),
    textCredential(credentials.refresh_token),
    textCredential(credentials.client_id),
    textCredential(credentials.client_secret),
    textCredential(credentials.expires_at),
    textCredential(credentials.oauth_type),
    textCredential(input.proxyUrl)
  ].map((part) => part ?? '').join('\0')).digest('hex')
}

function isGeminiCodeAssistGenerationRequest(
  req: Request,
  mapping: ReturnType<typeof resolveOpenAIRequestModelMapping>
): boolean {
  if (isOpenAIOrAnthropicToGeminiGenerateContentModelMapping(mapping) || isGeminiNativeGenerateContentModelMapping(mapping)) {
    return true
  }
  if (!isGeminiNativeRequest(req)) return false
  const family = geminiEndpointFamilyFromPath(req.path || req.originalUrl.split('?', 1)[0])
  return family === GEMINI_GENERATE_CONTENT_FAMILY || family === GEMINI_STREAM_GENERATE_CONTENT_FAMILY
}

function geminiCodeAssistDownstreamStream(
  req: Request,
  mapping: ReturnType<typeof resolveOpenAIRequestModelMapping>
): boolean {
  if (isOpenAIOrAnthropicToGeminiGenerateContentModelMapping(mapping)) return requestStream(req)
  return geminiEndpointFamilyFromPath(req.path || req.originalUrl.split('?', 1)[0]) === GEMINI_STREAM_GENERATE_CONTENT_FAMILY
}

function textCredential(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

async function normalizeGeminiInteractionsStreamBody(
  req: Request,
  endpointFamily: string | undefined,
  headers: Headers,
  body: Buffer | undefined,
  signal?: AbortSignal
): Promise<Buffer | undefined> {
  if (
    endpointFamily !== GEMINI_INTERACTIONS_FAMILY
    || req.method !== 'POST'
    || !/\/interactions\/?$/.test(req.path || req.originalUrl.split('?', 1)[0])
    || !headers.get('accept')?.toLowerCase().includes('text/event-stream')
  ) {
    return body
  }
  const requestWithBody = req as GatewayRawBodyRequest
  const bodyState = getGatewayRequestBodyState(req)
  if (bodyState?.jsonParseStatus === 'invalid_json') {
    throw geminiInteractionsJsonBodyError('Interactions 请求体必须是有效的 JSON 对象')
  }
  if (bodyState?.stream === true) {
    return body
  }
  let parsedBody: unknown = req.body
  if (parsedBody === undefined && requestWithBody.gatewayParsedJsonBodyAvailable) {
    parsedBody = requestWithBody.gatewayParsedJsonBody
  }
  if (parsedBody === undefined) {
    if (!requestWithBody.rawBody?.length) return body
    try {
      parsedBody = await parseGatewayRequestJsonBody(req, undefined, signal)
    } catch (error) {
      if (isGatewayJsonWorkerQueueFullError(error)) {
        throw new GatewayRequestValidationError(
          '网关请求解析繁忙，请稍后重试',
          'gateway_json_parser_busy',
          { statusCode: 503, type: 'server_overloaded' }
        )
      }
      if (isGatewayJsonWorkerInvalidJsonError(error)) {
        throw geminiInteractionsJsonBodyError('Interactions 请求体必须是有效的 JSON 对象')
      }
      throw error
    }
  }
  if (typeof parsedBody !== 'object' || parsedBody === null || Array.isArray(parsedBody)) {
    throw geminiInteractionsJsonBodyError('Interactions 请求体必须是 JSON 对象')
  }
  if ((parsedBody as Record<string, unknown>).stream === true) {
    return body
  }
  return serializeGatewayJsonObject({ ...parsedBody, stream: true })
}

function geminiInteractionsJsonBodyError(message: string): GatewayRequestValidationError {
  return new GatewayRequestValidationError(message, 'invalid_gemini_interactions_json_body')
}
