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
  isGatewayJsonWorkerQueueFullError,
  parseGatewayRequestJsonBody
} from '../../../gateway/request/json-parser.js'
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
import { createGeminiGoogleOAuthTokenProvider, type GeminiGoogleOAuthTokenProvider } from './google-oauth-token.service.js'
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

const googleOAuthProviders = new Map<string, { fingerprint: string; provider: GeminiGoogleOAuthTokenProvider }>()
const maxGoogleOAuthProviders = 1024

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
    const credentials = account.credentials ?? {}
    const accessToken = textCredential(credentials.access_token)
    const refreshToken = textCredential(credentials.refresh_token)
    if (!accessToken && !refreshToken) throw new Error('Gemini Google OAuth 凭据不完整')
    const fingerprint = geminiGoogleOAuthProviderFingerprint(account)
    const key = account.credentialSourceAccountId ?? account.id ?? fingerprint
    let entry = googleOAuthProviders.get(key)
    if (!entry || entry.fingerprint !== fingerprint) {
      entry = {
        fingerprint,
        provider: createGeminiGoogleOAuthTokenProvider({
          access_token: accessToken,
          refresh_token: refreshToken,
          client_id: textCredential(credentials.client_id),
          client_secret: textCredential(credentials.client_secret),
          expires_at: textCredential(credentials.expires_at)
        }, {
          proxyUrl: account.proxyUrl
        })
      }
      setBoundedProviderCache(googleOAuthProviders, key, entry)
    }
    const token = await entry.provider.getAccessToken({ signal: context.signal })
    return { ...account, apiKey: token }
  },
  buildUpstreamUrls(account: DispatchAccountSecret, req: Request): string[] {
    const mapping = resolveOpenAIRequestModelMapping(req, account)
    if (isOpenAIOrAnthropicToGeminiGenerateContentModelMapping(mapping) || isGeminiNativeGenerateContentModelMapping(mapping)) {
      return [buildGeminiUpstreamUrl(account.baseUrl, geminiGenerateContentModelMappedUpstreamPathAndQuery(req, mapping))]
    }
    return buildGeminiUpstreamUrlsForAccount(account, req)
  },
  async buildUpstreamRequestParts(req, account, _identity, signal) {
    if (account.type !== 'api_key' && account.type !== 'google_oauth') {
      throw new Error('Gemini 原生协议当前仅支持 API Key 或 Google OAuth 账户')
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
    const mapping = resolveOpenAIRequestModelMapping(req, account)
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
    if (isOpenAIOrAnthropicToGeminiGenerateContentModelMapping(mapping)) {
      return transformGeminiNativeTargetBridgeUpstreamResponse(req, response, { mapping })
    }
    return response
  },
  endpointModeForRequest: geminiEndpointModeForGatewayRequest,
  accountSupportsRequest(req, account) {
    const mapping = resolveOpenAIRequestModelMapping(req, account)
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

function secretFingerprint(parts: Array<string | undefined>): string {
  return createHash('sha256').update(parts.map((part) => part ?? '').join('\0')).digest('hex')
}

export function geminiGoogleOAuthProviderFingerprint(input: {
  credentials?: {
    access_token?: unknown
    refresh_token?: unknown
    client_id?: unknown
    client_secret?: unknown
    expires_at?: unknown
  }
  proxyUrl?: string
}): string {
  const credentials = input.credentials ?? {}
  return secretFingerprint([
    textCredential(credentials.access_token),
    textCredential(credentials.refresh_token),
    textCredential(credentials.client_id),
    textCredential(credentials.client_secret),
    textCredential(credentials.expires_at),
    textCredential(input.proxyUrl)
  ])
}

function setBoundedProviderCache<T>(
  cache: Map<string, T>,
  key: string,
  value: T
): void {
  cache.delete(key)
  cache.set(key, value)
  while (cache.size > maxGoogleOAuthProviders) {
    const oldest = cache.keys().next().value as string | undefined
    if (oldest === undefined) break
    cache.delete(oldest)
  }
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
  let parsedBody: unknown = req.body
  if (parsedBody === undefined && requestWithBody.gatewayParsedJsonBodyAvailable) {
    parsedBody = requestWithBody.gatewayParsedJsonBody
  }
  if (parsedBody === undefined) {
    const bodyState = getGatewayRequestBodyState(req)
    if (bodyState?.jsonParseStatus === 'invalid_json') {
      throw geminiInteractionsJsonBodyError('Interactions 请求体必须是有效的 JSON 对象')
    }
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
      throw geminiInteractionsJsonBodyError('Interactions 请求体必须是有效的 JSON 对象')
    }
  }
  if (typeof parsedBody !== 'object' || parsedBody === null || Array.isArray(parsedBody)) {
    throw geminiInteractionsJsonBodyError('Interactions 请求体必须是 JSON 对象')
  }
  if ((parsedBody as Record<string, unknown>).stream === true) {
    return body
  }
  return Buffer.from(JSON.stringify({ ...parsedBody, stream: true }))
}

function geminiInteractionsJsonBodyError(message: string): GatewayRequestValidationError {
  return new GatewayRequestValidationError(message, 'invalid_gemini_interactions_json_body')
}
