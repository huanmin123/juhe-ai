import type { Request } from 'express'

import { accountSupportsClientCompatibility } from '../../../../domain/account-client-compatibility.js'
import {
  accountSupportsOpenAIEndpointMode,
  openAIEndpointModeForRequestShape
} from '../../../../domain/openai-endpoint-modes.js'
import {
  ANTHROPIC_PROTOCOL_CODE,
  GEMINI_PROTOCOL_CODE,
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  GEMINI_PROVIDER_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  isOpenAIProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import { isGatewayProtocolNativeRequest } from '../../../gateway/protocols/registry.js'
import { applyOpenAIClientCompatibilityHeaders, buildOpenAIClientCompatibilityBody } from '../../../gateway/protocols/openai-v1/api-key-client-compatibility.js'
import {
  buildOpenAIModelMappedJsonBody,
  isGeminiGenerateContentToChatCompletionsModelMapping,
  isAnthropicMessagesToChatCompletionsModelMapping,
  isOpenAIResponsesToChatCompletionsModelMapping,
  openAIModelMappedUpstreamPathAndQuery,
  resolveOpenAIAccountModelMapping,
  resolveOpenAIRequestModelMapping
} from '../../../gateway/protocols/openai-v1/model-mapping.js'
import { buildUpstreamUrl, buildUpstreamUrls, splitPathAndQuery } from '../../../gateway/protocols/openai-v1/route-helpers.js'
import {
  buildUpstreamHeaders,
  buildUpstreamRequestBody,
  isEffectiveOpenAIStreamRequest
} from '../../../gateway/upstream/request.js'
import { requestModel } from '../../../gateway/request/metadata.js'
import {
  anthropicMessagesChatBridgeRequiredEndpointMode,
  buildAnthropicMessagesChatBridgeBody,
  prepareAnthropicMessagesChatBridgeHeaders,
  transformAnthropicMessagesChatBridgeUpstreamResponse
} from '../_shared/anthropic-openai-chat-bridge.js'
import {
  buildGeminiGenerateContentChatBridgeBody,
  geminiGenerateContentChatBridgeRequiredEndpointMode,
  prepareGeminiGenerateContentChatBridgeHeaders,
  transformGeminiGenerateContentChatBridgeUpstreamResponse
} from '../_shared/gemini-openai-chat-bridge.js'
import {
  buildCodexResponsesChatBridgeBody,
  codexResponsesChatBridgeRequiredEndpointMode,
  prepareCodexResponsesChatBridgeHeaders,
  transformCodexResponsesChatBridgeUpstreamResponse
} from '../_shared/codex-responses-chat-bridge.js'
import type { ProviderDriver, ProviderDriverAccount } from '../_shared/types.js'

function openAIEndpointModeForGatewayRequest(req: Request, account: ProviderDriverAccount) {
  return openAIEndpointModeForRequestShape({
    endpoint: req.path || req.originalUrl.split('?', 1)[0],
    stream: isEffectiveOpenAIStreamRequest(req, account)
  })
}

function isOpenAICompatibleDriverProfile(profile: ProviderDriverAccount | undefined): boolean {
  const profileId = profile?.providerProtocolProfileId ?? profile?.id
  return profile?.providerCode === OPENAI_COMPATIBLE_PROVIDER_CODE
    || (profile?.providerCode === GEMINI_PROVIDER_CODE && profileId === GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID)
}

function buildOpenAICompatibleDriverUpstreamUrl(account: DispatchAccountSecret, pathAndQuery: string): string {
  if (account.providerCode === GEMINI_PROVIDER_CODE && account.providerProtocolProfileId === GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID) {
    const normalizedBase = account.baseUrl.trim().replace(/\/+$/, '')
    if (!geminiOpenAIChatBaseUrlOwnsOpenAIPath(normalizedBase)) {
      return buildUpstreamUrl(account.baseUrl, pathAndQuery)
    }
    const { path, query } = splitPathAndQuery(pathAndQuery)
    const requestPath = path.startsWith('/') ? path : `/${path}`
    const pathWithoutVersion = requestPath.replace(/^\/v1(?=\/|$)/, '') || '/'
    return `${normalizedBase}${pathWithoutVersion === '/' ? '' : pathWithoutVersion}${query}`
  }
  return buildUpstreamUrl(account.baseUrl, pathAndQuery)
}

function geminiOpenAIChatBaseUrlOwnsOpenAIPath(baseUrl: string): boolean {
  try {
    const url = new URL(baseUrl)
    return url.pathname.replace(/\/+$/, '').toLowerCase().endsWith('/v1beta/openai')
  } catch {
    return baseUrl.toLowerCase().endsWith('/v1beta/openai')
  }
}

function buildOpenAICompatibleDriverUpstreamUrls(account: DispatchAccountSecret, pathAndQuery: string): string[] {
  if (account.providerCode === GEMINI_PROVIDER_CODE && account.providerProtocolProfileId === GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID) {
    return [buildOpenAICompatibleDriverUpstreamUrl(account, pathAndQuery)]
  }
  return buildUpstreamUrls(account.baseUrl, pathAndQuery)
}

export const openAICompatibleProviderDriver: ProviderDriver = {
  id: 'openai-compatible',
  providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  usageSemantic: 'openai',
  profileIds: [OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID, GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID],
  supportsProfile(profile) {
    const profileId = profile?.providerProtocolProfileId ?? profile?.id
    return isOpenAICompatibleDriverProfile(profile)
      && isOpenAIProtocolProfile(profile)
      && (profileId === OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID || profileId === GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID)
  },
  resolveUsageModel(account, requestedModel, sourceEndpointFamily) {
    const modelMapping = resolveOpenAIAccountModelMapping(account, requestedModel, sourceEndpointFamily)
    return {
      upstreamModel: modelMapping?.upstreamModel ?? requestedModel,
      modelMappingApplied: Boolean(modelMapping),
      modelMappingSource: modelMapping ? 'account' : undefined
    }
  },
  buildUpstreamUrls(account: DispatchAccountSecret, req: Request): string[] {
    const modelMapping = resolveOpenAIRequestModelMapping(req, account)
    if (isGatewayProtocolNativeRequest(req, ANTHROPIC_PROTOCOL_CODE) && !isAnthropicMessagesToChatCompletionsModelMapping(modelMapping)) {
      return []
    }
    if (isGatewayProtocolNativeRequest(req, GEMINI_PROTOCOL_CODE) && !isGeminiGenerateContentToChatCompletionsModelMapping(modelMapping)) {
      return []
    }
    if (modelMapping && (isOpenAIResponsesToChatCompletionsModelMapping(modelMapping) || isAnthropicMessagesToChatCompletionsModelMapping(modelMapping) || isGeminiGenerateContentToChatCompletionsModelMapping(modelMapping))) {
      return [buildOpenAICompatibleDriverUpstreamUrl(account, openAIModelMappedUpstreamPathAndQuery(req, modelMapping))]
    }
    return buildOpenAICompatibleDriverUpstreamUrls(account, req.originalUrl)
  },
  async buildUpstreamRequestParts(req, account, _identity, signal, context) {
    const modelMapping = resolveOpenAIRequestModelMapping(req, account)
    if (modelMapping && isAnthropicMessagesToChatCompletionsModelMapping(modelMapping)) {
      const headers = buildUpstreamHeaders(req.headers, account)
      prepareAnthropicMessagesChatBridgeHeaders(headers, req)
      return {
        headers,
        body: await buildAnthropicMessagesChatBridgeBody(req, {
          defaultModel: modelMapping.upstreamModel,
          modelOverride: modelMapping.upstreamModel
        }, signal)
      }
    }
    if (modelMapping && isGeminiGenerateContentToChatCompletionsModelMapping(modelMapping)) {
      const headers = buildUpstreamHeaders(req.headers, account)
      prepareGeminiGenerateContentChatBridgeHeaders(headers, req)
      return {
        headers,
        body: await buildGeminiGenerateContentChatBridgeBody(req, {
          defaultModel: modelMapping.upstreamModel,
          modelOverride: modelMapping.upstreamModel
        }, signal)
      }
    }
    if (modelMapping && isOpenAIResponsesToChatCompletionsModelMapping(modelMapping)) {
      const headers = buildUpstreamHeaders(req.headers, account)
      prepareCodexResponsesChatBridgeHeaders(headers)
      return {
        headers,
        body: await buildCodexResponsesChatBridgeBody(req, {
          defaultModel: modelMapping.upstreamModel,
          modelOverride: modelMapping.upstreamModel
        }, signal)
      }
    }
    const compatibilityBody = await buildOpenAIClientCompatibilityBody(req, account, signal, {
      modelOverride: modelMapping?.upstreamModel,
      requestClientCompatibility: context?.requestClientCompatibility
    })
    const headers = buildUpstreamHeaders(req.headers, account)
    applyOpenAIClientCompatibilityHeaders(req, account, headers, {
      requestClientCompatibility: context?.requestClientCompatibility
    })
    return {
      headers,
      body: compatibilityBody ?? (modelMapping ? await buildOpenAIModelMappedJsonBody(req, modelMapping.upstreamModel, signal) : buildUpstreamRequestBody(req))
    }
  },
  transformUpstreamResponse(req, account, response, context) {
    const modelMapping = resolveOpenAIRequestModelMapping(req, account)
    const anthropicMessagesResponse = transformAnthropicMessagesChatBridgeUpstreamResponse(req, response, {
      enabled: isAnthropicMessagesToChatCompletionsModelMapping(modelMapping),
      model: modelMapping?.upstreamModel ?? requestModel(req) ?? 'openai-compatible'
    })
    const geminiGenerateContentResponse = transformGeminiGenerateContentChatBridgeUpstreamResponse(req, anthropicMessagesResponse, {
      enabled: isGeminiGenerateContentToChatCompletionsModelMapping(modelMapping),
      model: modelMapping?.upstreamModel ?? requestModel(req) ?? 'openai-compatible'
    })
    return transformCodexResponsesChatBridgeUpstreamResponse(req, geminiGenerateContentResponse, {
      defaultModel: modelMapping?.upstreamModel ?? requestModel(req) ?? 'openai-compatible',
      enabled: isOpenAIResponsesToChatCompletionsModelMapping(modelMapping),
      explicitMappingBridge: true,
      idPrefix: 'openai_compatible_bridge',
      model: modelMapping?.upstreamModel,
      previousResponseId: context?.codexResponsesChatBridgePreviousResponseId,
      onCompleted: context?.codexResponsesChatBridgeCompletionHandler,
      continueChatRequest: context?.codexResponsesChatBridgeContinueChatRequest,
      requestClientCompatibility: context?.requestClientCompatibility
    })
  },
  endpointModeForRequest: openAIEndpointModeForGatewayRequest,
  accountSupportsRequest(req, account, context) {
    const modelMapping = resolveOpenAIRequestModelMapping(req, account)
    if (modelMapping && isAnthropicMessagesToChatCompletionsModelMapping(modelMapping)) {
      return accountSupportsOpenAIEndpointMode({
        mode: anthropicMessagesChatBridgeRequiredEndpointMode(isEffectiveOpenAIStreamRequest(req, account)),
        supportedEndpointModes: account.supportedEndpointModes,
        credentials: account.credentials,
        providerCode: account.providerCode,
        providerProtocolProfileId: account.providerProtocolProfileId,
        accountType: account.type,
        clientCompatibility: account.clientCompatibility
      })
    }
    if (modelMapping && isGeminiGenerateContentToChatCompletionsModelMapping(modelMapping)) {
      return accountSupportsOpenAIEndpointMode({
        mode: geminiGenerateContentChatBridgeRequiredEndpointMode(req),
        supportedEndpointModes: account.supportedEndpointModes,
        credentials: account.credentials,
        providerCode: account.providerCode,
        providerProtocolProfileId: account.providerProtocolProfileId,
        accountType: account.type,
        clientCompatibility: account.clientCompatibility
      })
    }
    if (modelMapping && isOpenAIResponsesToChatCompletionsModelMapping(modelMapping)) {
      return accountSupportsOpenAIEndpointMode({
        mode: codexResponsesChatBridgeRequiredEndpointMode(),
        supportedEndpointModes: account.supportedEndpointModes,
        credentials: account.credentials,
        providerCode: account.providerCode,
        providerProtocolProfileId: account.providerProtocolProfileId,
        accountType: account.type,
        clientCompatibility: account.clientCompatibility
      })
    }
    if (!accountSupportsClientCompatibility(account, context?.requestClientCompatibility)) {
      return false
    }
    const mode = openAIEndpointModeForGatewayRequest(req, account)
    if (!mode) return true
    return accountSupportsOpenAIEndpointMode({
      mode,
      supportedEndpointModes: account.supportedEndpointModes,
      credentials: account.credentials,
      providerCode: account.providerCode,
      providerProtocolProfileId: account.providerProtocolProfileId,
      accountType: account.type,
      clientCompatibility: account.clientCompatibility
    })
  }
}
