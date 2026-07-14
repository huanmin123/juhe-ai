import type { Request } from 'express'

import {
  accountSupportsGeminiEndpointMode,
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
  buildUpstreamUrls(account: DispatchAccountSecret, req: Request): string[] {
    const mapping = resolveOpenAIRequestModelMapping(req, account)
    if (isOpenAIOrAnthropicToGeminiGenerateContentModelMapping(mapping) || isGeminiNativeGenerateContentModelMapping(mapping)) {
      return [buildGeminiUpstreamUrl(account.baseUrl, geminiGenerateContentModelMappedUpstreamPathAndQuery(req, mapping))]
    }
    return buildGeminiUpstreamUrlsForAccount(account, req)
  },
  async buildUpstreamRequestParts(req, account, _identity, signal) {
    if (account.type !== 'api_key') {
      throw new Error('Gemini 原生协议当前仅支持 API Key 账户')
    }
    const headers = copySafeUpstreamRequestHeaders(req.headers)
    headers.set('x-goog-api-key', account.apiKey)
    const mapping = resolveOpenAIRequestModelMapping(req, account)
    if (isOpenAIOrAnthropicToGeminiGenerateContentModelMapping(mapping)) {
      prepareOpenAIOrAnthropicToGeminiNativeHeaders(headers, req)
      return {
        headers,
        body: await applyProviderAccountRequestOverridesToBody(await buildOpenAIOrAnthropicToGeminiNativeBody(req, {
          mapping,
          providerName: account.name
        }, signal), { account, upstreamModel: mapping?.upstreamModel, wireFormat: 'gemini_generate_content' })
      }
    }
    if (!headers.get('content-type') && req.method !== 'GET' && req.method !== 'HEAD') {
      headers.set('content-type', 'application/json')
    }
    if (!headers.get('accept')) {
      headers.set('accept', requestStream(req) || req.originalUrl.includes(':streamGenerateContent') ? 'text/event-stream' : 'application/json')
    }
    return {
      headers,
      body: await applyProviderAccountRequestOverridesToBody(buildUpstreamRequestBody(req), {
        account,
        upstreamModel: requestModel(req),
        wireFormat: 'gemini_generate_content'
      })
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
