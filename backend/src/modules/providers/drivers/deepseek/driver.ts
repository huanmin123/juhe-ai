import type { Request } from 'express'

import {
  accountSupportsOpenAIEndpointMode,
  openAIEndpointModeForRequestShape
} from '../../../../domain/openai-endpoint-modes.js'
import {
  ANTHROPIC_PROTOCOL_CODE,
  GEMINI_PROTOCOL_CODE,
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  isDeepSeekProviderCode,
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
import { buildUpstreamUrl, isOpenAIModelsRequest, splitPathAndQuery } from '../../../gateway/protocols/openai-v1/route-helpers.js'
import { isGatewayUpstreamModelsProbe } from '../../../gateway/request/upstream-models-probe.js'
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

const DEEPSEEK_CODEX_BRIDGE_DEFAULT_MODEL = 'deepseek-v4-flash'
const DEEPSEEK_NATIVE_RESPONSES_MODELS = new Set(['deepseek-v4-flash', 'deepseek-v4-pro'])

function openAIEndpointModeForGatewayRequest(req: Request, account: ProviderDriverAccount) {
  if (req.method.toUpperCase() !== 'POST') return undefined
  const { path } = splitPathAndQuery(req.originalUrl || req.path || '')
  const requestPath = path.startsWith('/') ? path : `/${path}`
  const normalizedPath = requestPath.replace(/^\/v1(?=\/|$)/, '') || '/'
  if (normalizedPath !== '/chat/completions' && normalizedPath !== '/responses') return undefined
  return openAIEndpointModeForRequestShape({
    endpoint: normalizedPath,
    stream: isEffectiveOpenAIStreamRequest(req, account)
  })
}

export const deepSeekProviderDriver: ProviderDriver = {
  id: 'deepseek',
  providerCode: DEEPSEEK_PROVIDER_CODE,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  usageSemantic: 'openai',
  profileIds: [DEEPSEEK_OPENAI_V1_PROFILE_ID],
  supportsProfile(profile) {
    const profileId = profile?.providerProtocolProfileId ?? profile?.id
    return isDeepSeekProviderCode(profile?.providerCode)
      && isOpenAIProtocolProfile(profile)
      && profileId === DEEPSEEK_OPENAI_V1_PROFILE_ID
  },
  resolveUsageModel(account, requestedModel, sourceEndpointFamily) {
    const modelMapping = resolveOpenAIAccountModelMapping(account, requestedModel, sourceEndpointFamily)
    return {
      upstreamModel: modelMapping?.upstreamModel ?? requestedModel,
      modelMappingApplied: Boolean(modelMapping),
      modelMappingSource: modelMapping ? modelMapping.runtimeSource ?? 'account' : undefined,
      sourceEndpointFamily: modelMapping?.sourceEndpointFamily,
      upstreamEndpointFamily: modelMapping?.upstreamEndpointFamily
    }
  },
  buildUpstreamUrls(account: DispatchAccountSecret, req: Request): string[] {
    if (isDeepSeekApiKeyUpstreamModelsProbe(req, account)) {
      return [buildUpstreamUrl(account.baseUrl, req.originalUrl)]
    }
    const modelMapping = resolveOpenAIRequestModelMapping(req, account)
    if (isGatewayProtocolNativeRequest(req, ANTHROPIC_PROTOCOL_CODE) && !isAnthropicMessagesToChatCompletionsModelMapping(modelMapping)) {
      return []
    }
    if (isGatewayProtocolNativeRequest(req, GEMINI_PROTOCOL_CODE) && !isGeminiGenerateContentToChatCompletionsModelMapping(modelMapping)) {
      return []
    }
    return buildDeepSeekOpenAIChatUpstreamUrls(account, req)
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
          guidanceProviderName: 'DeepSeek',
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
          guidanceProviderName: 'DeepSeek',
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
          defaultModel: DEEPSEEK_CODEX_BRIDGE_DEFAULT_MODEL,
          guidanceProviderName: 'DeepSeek',
          includeReasoningContent: true,
          streamOptionsIncludeUsage: true,
          modelOverride: modelMapping?.upstreamModel
        }, signal)
      }
    }
    const compatibilityBody = await buildOpenAIClientCompatibilityBody(req, signal, {
      modelOverride: modelMapping?.upstreamModel,
      requestClientCompatibility: context?.requestClientCompatibility
    })
    const headers = buildUpstreamHeaders(req.headers, account)
    applyOpenAIClientCompatibilityHeaders(req, headers, {
      modelOverride: modelMapping?.upstreamModel,
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
      model: modelMapping?.upstreamModel ?? requestModel(req) ?? DEEPSEEK_CODEX_BRIDGE_DEFAULT_MODEL
    })
    const geminiGenerateContentResponse = transformGeminiGenerateContentChatBridgeUpstreamResponse(req, anthropicMessagesResponse, {
      enabled: isGeminiGenerateContentToChatCompletionsModelMapping(modelMapping),
      model: modelMapping?.upstreamModel ?? requestModel(req) ?? DEEPSEEK_CODEX_BRIDGE_DEFAULT_MODEL
    })
    return transformCodexResponsesChatBridgeUpstreamResponse(req, geminiGenerateContentResponse, {
      defaultModel: DEEPSEEK_CODEX_BRIDGE_DEFAULT_MODEL,
      enabled: isOpenAIResponsesToChatCompletionsModelMapping(modelMapping),
      explicitMappingBridge: isOpenAIResponsesToChatCompletionsModelMapping(modelMapping),
      idPrefix: 'deepseek_bridge',
      model: modelMapping?.upstreamModel,
      previousResponseId: context?.codexResponsesChatBridgePreviousResponseId,
      onCompleted: context?.codexResponsesChatBridgeCompletionHandler,
      continueChatRequest: context?.codexResponsesChatBridgeContinueChatRequest,
      requestClientCompatibility: context?.requestClientCompatibility
    })
  },
  endpointModeForRequest: openAIEndpointModeForGatewayRequest,
  accountSupportsRequest(req, account) {
    if (isDeepSeekApiKeyUpstreamModelsProbe(req, account)) return true
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
    const mode = openAIEndpointModeForGatewayRequest(req, account)
    if (!mode) return false
    if (isDeepSeekNativeResponsesRequest(req) && !isDeepSeekNativeResponsesModel(req, modelMapping)) {
      return false
    }
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

function isDeepSeekApiKeyUpstreamModelsProbe(req: Request, account: ProviderDriverAccount): boolean {
  return account.type === 'api_key'
    && isGatewayUpstreamModelsProbe(req)
    && isOpenAIModelsRequest(req)
}

function buildDeepSeekOpenAIChatUpstreamUrls(account: DispatchAccountSecret, req: Request): string[] {
  if (req.method.toUpperCase() !== 'POST') {
    return []
  }
  const modelMapping = resolveOpenAIRequestModelMapping(req, account)
  if (modelMapping && (isOpenAIResponsesToChatCompletionsModelMapping(modelMapping) || isAnthropicMessagesToChatCompletionsModelMapping(modelMapping) || isGeminiGenerateContentToChatCompletionsModelMapping(modelMapping))) {
    return [buildUpstreamUrl(account.baseUrl, openAIModelMappedUpstreamPathAndQuery(req, modelMapping))]
  }
  const { path, query } = splitPathAndQuery(req.originalUrl)
  const requestPath = path.startsWith('/') ? path : `/${path}`
  const normalizedPath = requestPath.replace(/^\/v1(?=\/|$)/, '') || '/'
  if (normalizedPath !== '/chat/completions' && normalizedPath !== '/responses') {
    return []
  }
  if (normalizedPath === '/responses' && !isDeepSeekNativeResponsesModel(req, modelMapping)) {
    return []
  }
  return [buildUpstreamUrl(account.baseUrl, `${normalizedPath}${query}`)]
}

function isDeepSeekNativeResponsesRequest(req: Request): boolean {
  const { path } = splitPathAndQuery(req.originalUrl || req.path || '')
  const requestPath = path.startsWith('/') ? path : `/${path}`
  return (requestPath.replace(/^\/v1(?=\/|$)/, '') || '/') === '/responses'
}

function isDeepSeekNativeResponsesModel(
  req: Request,
  modelMapping: ReturnType<typeof resolveOpenAIRequestModelMapping>
): boolean {
  return DEEPSEEK_NATIVE_RESPONSES_MODELS.has(modelMapping?.upstreamModel ?? requestModel(req) ?? '')
}
