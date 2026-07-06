import type { Request } from 'express'

import { accountSupportsClientCompatibility } from '../../../../domain/account-client-compatibility.js'
import {
  accountSupportsAnthropicEndpointMode,
  anthropicEndpointModeForRequestShape
} from '../../../../domain/anthropic-endpoint-modes.js'
import {
  accountSupportsGeminiEndpointMode,
  geminiEndpointModeForRequestShape
} from '../../../../domain/gemini-endpoint-modes.js'
import {
  accountSupportsOpenAIEndpointMode,
  openAIEndpointModeForRequestShape
} from '../../../../domain/openai-endpoint-modes.js'
import {
  ANTHROPIC_MESSAGES_FAMILY,
  ANTHROPIC_PROTOCOL_CODE,
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_PROTOCOL_CODE,
  HYBRID_ANTHROPIC_MESSAGES_V1_PROFILE_ID,
  HYBRID_GEMINI_NATIVE_V1BETA_PROFILE_ID,
  HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
  HYBRID_PROVIDER_CODE,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  isHybridProviderCode
} from '../../../../domain/provider-protocol.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import {
  anthropicClaudeCodePathAndQueryForRequest,
  applyAnthropicClientCompatibilityHeaders
} from '../../../gateway/protocols/anthropic-v1/client-compatibility.js'
import { buildAnthropicUpstreamUrl, buildAnthropicUpstreamUrlsForAccount } from '../../../gateway/protocols/anthropic-v1/route-helpers.js'
import { buildGeminiUpstreamUrl, buildGeminiUpstreamUrlsForAccount, isGeminiModelsRequest, isGeminiNativeRequest } from '../../../gateway/protocols/gemini-v1beta/route-helpers.js'
import { isGatewayProtocolNativeRequest } from '../../../gateway/protocols/registry.js'
import { applyOpenAIClientCompatibilityHeaders, buildOpenAIClientCompatibilityBody } from '../../../gateway/protocols/openai-v1/api-key-client-compatibility.js'
import {
  buildOpenAIModelMappedJsonBody,
  geminiGenerateContentModelMappedUpstreamPathAndQuery,
  geminiGenerateContentToAnthropicMessagesUpstreamPathAndQuery,
  isAnthropicMessagesToChatCompletionsModelMapping,
  isGeminiGenerateContentToAnthropicMessagesModelMapping,
  isGeminiGenerateContentToChatCompletionsModelMapping,
  isOpenAIOrAnthropicToGeminiGenerateContentModelMapping,
  isOpenAIResponsesToChatCompletionsModelMapping,
  openAIModelMappedUpstreamPathAndQuery,
  resolveOpenAIAccountModelMapping,
  resolveOpenAIRequestModelMapping
} from '../../../gateway/protocols/openai-v1/model-mapping.js'
import { buildUpstreamUrl, buildUpstreamUrls } from '../../../gateway/protocols/openai-v1/route-helpers.js'
import { requestModel, requestStream } from '../../../gateway/request/metadata.js'
import {
  buildUpstreamHeaders,
  buildUpstreamRequestBody,
  copySafeUpstreamRequestHeaders,
  isEffectiveOpenAIStreamRequest
} from '../../../gateway/upstream/request.js'
import {
  anthropicMessagesChatBridgeRequiredEndpointMode,
  buildAnthropicMessagesChatBridgeBody,
  prepareAnthropicMessagesChatBridgeHeaders,
  transformAnthropicMessagesChatBridgeUpstreamResponse
} from '../_shared/anthropic-openai-chat-bridge.js'
import {
  buildCodexResponsesChatBridgeBody,
  codexResponsesChatBridgeRequiredEndpointMode,
  prepareCodexResponsesChatBridgeHeaders,
  transformCodexResponsesChatBridgeUpstreamResponse
} from '../_shared/codex-responses-chat-bridge.js'
import {
  buildGeminiGenerateContentAnthropicMessagesBridgeBody,
  geminiGenerateContentAnthropicMessagesBridgeRequiredEndpointMode,
  prepareGeminiGenerateContentAnthropicMessagesBridgeHeaders,
  transformGeminiGenerateContentAnthropicMessagesBridgeUpstreamResponse
} from '../_shared/gemini-anthropic-messages-bridge.js'
import {
  buildGeminiGenerateContentChatBridgeBody,
  geminiGenerateContentChatBridgeRequiredEndpointMode,
  prepareGeminiGenerateContentChatBridgeHeaders,
  transformGeminiGenerateContentChatBridgeUpstreamResponse
} from '../_shared/gemini-openai-chat-bridge.js'
import {
  buildOpenAIOrAnthropicToGeminiNativeBody,
  openAIOrAnthropicToGeminiNativeRequiredEndpointMode,
  prepareOpenAIOrAnthropicToGeminiNativeHeaders,
  transformGeminiNativeTargetBridgeUpstreamResponse
} from '../_shared/openai-anthropic-gemini-native-bridge.js'
import {
  buildOpenAIToAnthropicBridgeBody,
  isOpenAIToAnthropicBridgeCandidateRequest,
  isOpenAIToAnthropicMessagesModelMapping,
  openAIToAnthropicBridgeRequiredEndpointMode,
  openAIToAnthropicBridgeUpstreamModel,
  openAIToAnthropicBridgeUpstreamPath,
  prepareOpenAIToAnthropicBridgeHeaders,
  transformOpenAIToAnthropicBridgeUpstreamResponse
} from '../_shared/openai-anthropic-bridge.js'
import type { ProviderDriver, ProviderDriverAccount, ProviderGatewayRequestContext } from '../_shared/types.js'

const hybridProfileIds = [
  HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
  HYBRID_ANTHROPIC_MESSAGES_V1_PROFILE_ID,
  HYBRID_GEMINI_NATIVE_V1BETA_PROFILE_ID
] as const

export const hybridProviderDriver: ProviderDriver = {
  id: 'hybrid',
  providerCode: HYBRID_PROVIDER_CODE,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  usageSemantic: 'openai',
  usageSemanticForProfile(profile) {
    const profileId = profile?.providerProtocolProfileId ?? profile?.id
    if (profileId === HYBRID_ANTHROPIC_MESSAGES_V1_PROFILE_ID) return 'anthropic'
    if (profileId === HYBRID_GEMINI_NATIVE_V1BETA_PROFILE_ID) return 'gemini'
    if (profileId === HYBRID_OPENAI_CHAT_V1_PROFILE_ID) return 'openai'
    return undefined
  },
  profileIds: hybridProfileIds,
  supportsProfile(profile) {
    if (!profile || !isHybridProviderCode(profile.providerCode)) return false
    const profileId = profile.providerProtocolProfileId ?? profile.id
    return Boolean(profileId && hybridProfileIds.includes(profileId as never))
  },
  resolveUsageModel(account, requestedModel, sourceEndpointFamily) {
    const mapping = resolveOpenAIAccountModelMapping(account, requestedModel, sourceEndpointFamily)
    return {
      upstreamModel: mapping?.upstreamModel ?? requestedModel,
      modelMappingApplied: Boolean(mapping),
      modelMappingSource: mapping ? mapping.runtimeSource ?? 'account' : undefined
    }
  },
  buildUpstreamUrls(account, req) {
    const target = hybridUpstreamTargetForRequest(req, account)
    if (target === 'openai') return buildHybridOpenAIUpstreamUrls(account, req)
    if (target === 'anthropic') return buildHybridAnthropicUpstreamUrls(account, req)
    if (target === 'gemini') return buildHybridGeminiUpstreamUrls(account, req)
    return []
  },
  async buildUpstreamRequestParts(req, account, _identity, signal, context) {
    if (account.type !== 'api_key') {
      throw new Error('混合供应商账户当前仅支持 API Key')
    }
    const target = hybridUpstreamTargetForRequest(req, account)
    if (target === 'openai') {
      return await buildHybridOpenAIRequestParts(req, account, signal, context)
    }
    if (target === 'anthropic') {
      return await buildHybridAnthropicRequestParts(req, account, signal, context)
    }
    if (target === 'gemini') {
      return await buildHybridGeminiRequestParts(req, account, signal)
    }
    throw new Error('混合供应商无法根据请求和模型映射确定真实上游协议')
  },
  transformUpstreamResponse(req, account, response, context) {
    const target = hybridUpstreamTargetForRequest(req, account)
    if (target === 'openai') {
      return transformHybridOpenAIResponse(req, account, response, context)
    }
    if (target === 'anthropic') {
      return transformHybridAnthropicResponse(req, account, response, context)
    }
    if (target === 'gemini') {
      const mapping = resolveOpenAIRequestModelMapping(req, account)
      return isOpenAIOrAnthropicToGeminiGenerateContentModelMapping(mapping)
        ? transformGeminiNativeTargetBridgeUpstreamResponse(req, response, { mapping })
        : response
    }
    return response
  },
  endpointModeForRequest(req, account) {
    const target = hybridUpstreamTargetForRequest(req, account)
    if (target === 'openai') return hybridOpenAIEndpointModeForGatewayRequest(req, account)
    if (target === 'anthropic') return hybridAnthropicEndpointModeForGatewayRequest(req, account)
    if (target === 'gemini') return hybridGeminiEndpointModeForGatewayRequest(req, account)
    return undefined
  },
  accountSupportsRequest(req, account, context) {
    const target = hybridUpstreamTargetForRequest(req, account)
    if (target === 'openai') return hybridOpenAIAccountSupportsRequest(req, account, context)
    if (target === 'anthropic') return hybridAnthropicAccountSupportsRequest(req, account, context)
    if (target === 'gemini') return hybridGeminiAccountSupportsRequest(req, account)
    return false
  }
}

type HybridUpstreamTarget = 'openai' | 'anthropic' | 'gemini'

function hybridUpstreamTargetForRequest(req: Request, account: ProviderDriverAccount): HybridUpstreamTarget | undefined {
  if (!isHybridProviderCode(account.providerCode)) return undefined
  const mapping = resolveOpenAIRequestModelMapping(req, account)
  const mappedTarget = mapping ? hybridUpstreamTargetForEndpointFamily(mapping.upstreamEndpointFamily) : undefined
  if (mappedTarget) return mappedTarget
  if (isGatewayProtocolNativeRequest(req, ANTHROPIC_PROTOCOL_CODE)) return 'anthropic'
  if (isGatewayProtocolNativeRequest(req, GEMINI_PROTOCOL_CODE) || isGeminiNativeRequest(req)) return 'gemini'
  return 'openai'
}

function hybridUpstreamTargetForEndpointFamily(endpointFamily: string | undefined): HybridUpstreamTarget | undefined {
  if (endpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY) return 'openai'
  if (endpointFamily === ANTHROPIC_MESSAGES_FAMILY) return 'anthropic'
  if (endpointFamily === GEMINI_GENERATE_CONTENT_FAMILY) return 'gemini'
  return undefined
}

function buildHybridOpenAIUpstreamUrls(account: DispatchAccountSecret, req: Request): string[] {
  const mapping = resolveOpenAIRequestModelMapping(req, account)
  if (isGatewayProtocolNativeRequest(req, ANTHROPIC_PROTOCOL_CODE) && !isAnthropicMessagesToChatCompletionsModelMapping(mapping)) return []
  if (isGatewayProtocolNativeRequest(req, GEMINI_PROTOCOL_CODE) && !isGeminiGenerateContentToChatCompletionsModelMapping(mapping)) return []
  if (mapping && mapping.upstreamEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY) {
    return [buildUpstreamUrl(account.baseUrl, openAIModelMappedUpstreamPathAndQuery(req, mapping))]
  }
  return buildUpstreamUrls(account.baseUrl, req.originalUrl)
}

async function buildHybridOpenAIRequestParts(req: Request, account: DispatchAccountSecret, signal?: AbortSignal, context?: ProviderGatewayRequestContext) {
  const mapping = resolveOpenAIRequestModelMapping(req, account)
  if (mapping && isAnthropicMessagesToChatCompletionsModelMapping(mapping)) {
    const headers = buildUpstreamHeaders(req.headers, account)
    prepareAnthropicMessagesChatBridgeHeaders(headers, req)
    return {
      headers,
      body: await buildAnthropicMessagesChatBridgeBody(req, {
        defaultModel: mapping.upstreamModel,
        guidanceProviderName: '混合供应商',
        modelOverride: mapping.upstreamModel
      }, signal)
    }
  }
  if (mapping && isGeminiGenerateContentToChatCompletionsModelMapping(mapping)) {
    const headers = buildUpstreamHeaders(req.headers, account)
    prepareGeminiGenerateContentChatBridgeHeaders(headers, req)
    return {
      headers,
      body: await buildGeminiGenerateContentChatBridgeBody(req, {
        defaultModel: mapping.upstreamModel,
        guidanceProviderName: '混合供应商',
        modelOverride: mapping.upstreamModel
      }, signal)
    }
  }
  if (mapping && isOpenAIResponsesToChatCompletionsModelMapping(mapping)) {
    const headers = buildUpstreamHeaders(req.headers, account)
    prepareCodexResponsesChatBridgeHeaders(headers)
    return {
      headers,
      body: await buildCodexResponsesChatBridgeBody(req, {
        defaultModel: mapping.upstreamModel,
        guidanceProviderName: '混合供应商',
        modelOverride: mapping.upstreamModel
      }, signal)
    }
  }
  const requestClientCompatibility = context?.requestClientCompatibility
  const compatibilityBody = await buildOpenAIClientCompatibilityBody(req, account, signal, {
    modelOverride: mapping?.upstreamModel,
    requestClientCompatibility
  })
  const headers = buildUpstreamHeaders(req.headers, account)
  applyOpenAIClientCompatibilityHeaders(req, account, headers, {
    requestClientCompatibility
  })
  return {
    headers,
    body: compatibilityBody ?? (mapping ? await buildOpenAIModelMappedJsonBody(req, mapping.upstreamModel, signal) : buildUpstreamRequestBody(req))
  }
}

function transformHybridOpenAIResponse(req: Request, account: DispatchAccountSecret, response: Parameters<NonNullable<ProviderDriver['transformUpstreamResponse']>>[2], context?: Parameters<NonNullable<ProviderDriver['transformUpstreamResponse']>>[3]) {
  const mapping = resolveOpenAIRequestModelMapping(req, account)
  const anthropicMessagesResponse = transformAnthropicMessagesChatBridgeUpstreamResponse(req, response, {
    enabled: isAnthropicMessagesToChatCompletionsModelMapping(mapping),
    model: mapping?.upstreamModel ?? requestModel(req) ?? 'hybrid'
  })
  const geminiGenerateContentResponse = transformGeminiGenerateContentChatBridgeUpstreamResponse(req, anthropicMessagesResponse, {
    enabled: isGeminiGenerateContentToChatCompletionsModelMapping(mapping),
    model: mapping?.upstreamModel ?? requestModel(req) ?? 'hybrid'
  })
  return transformCodexResponsesChatBridgeUpstreamResponse(req, geminiGenerateContentResponse, {
    defaultModel: mapping?.upstreamModel ?? requestModel(req) ?? 'hybrid',
    enabled: isOpenAIResponsesToChatCompletionsModelMapping(mapping),
    explicitMappingBridge: true,
    idPrefix: 'hybrid_chat_bridge',
    model: mapping?.upstreamModel,
    previousResponseId: context?.codexResponsesChatBridgePreviousResponseId,
    onCompleted: context?.codexResponsesChatBridgeCompletionHandler,
    continueChatRequest: context?.codexResponsesChatBridgeContinueChatRequest,
    requestClientCompatibility: context?.requestClientCompatibility
  })
}

function hybridOpenAIEndpointModeForGatewayRequest(req: Request, account: ProviderDriverAccount) {
  const mapping = resolveOpenAIRequestModelMapping(req, account)
  if (isAnthropicMessagesToChatCompletionsModelMapping(mapping)) {
    return anthropicMessagesChatBridgeRequiredEndpointMode(isEffectiveOpenAIStreamRequest(req, account))
  }
  if (isGeminiGenerateContentToChatCompletionsModelMapping(mapping)) {
    return geminiGenerateContentChatBridgeRequiredEndpointMode(req)
  }
  if (isOpenAIResponsesToChatCompletionsModelMapping(mapping)) {
    return codexResponsesChatBridgeRequiredEndpointMode()
  }
  return openAIEndpointModeForRequestShape({
    endpoint: req.path || req.originalUrl.split('?', 1)[0],
    stream: isEffectiveOpenAIStreamRequest(req, account)
  })
}

function hybridOpenAIAccountSupportsRequest(req: Request, account: ProviderDriverAccount, context?: { requestClientCompatibility?: Parameters<typeof accountSupportsClientCompatibility>[1] }): boolean {
  const mapping = resolveOpenAIRequestModelMapping(req, account)
  if (mapping && mapping.upstreamEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY) {
    const mode = hybridOpenAIEndpointModeForGatewayRequest(req, account)
    if (!mode) return false
    return accountSupportsOpenAIEndpointMode(openAIEndpointModeSupportInput(account, mode))
  }
  if (isGatewayProtocolNativeRequest(req, ANTHROPIC_PROTOCOL_CODE) || isGatewayProtocolNativeRequest(req, GEMINI_PROTOCOL_CODE)) return false
  if (!accountSupportsClientCompatibility(account, context?.requestClientCompatibility)) return false
  const mode = hybridOpenAIEndpointModeForGatewayRequest(req, account)
  if (!mode) return false
  return accountSupportsOpenAIEndpointMode(openAIEndpointModeSupportInput(account, mode))
}

function buildHybridAnthropicUpstreamUrls(account: DispatchAccountSecret, req: Request): string[] {
  const mapping = resolveOpenAIRequestModelMapping(req, account)
  if (isGeminiGenerateContentToAnthropicMessagesModelMapping(mapping)) {
    return [buildAnthropicUpstreamUrl(account.baseUrl, geminiGenerateContentToAnthropicMessagesUpstreamPathAndQuery(req))]
  }
  const bridgePath = openAIToAnthropicBridgeUpstreamPath(req)
  if (bridgePath && isOpenAIToAnthropicMessagesModelMapping(req, account)) {
    return [buildAnthropicUpstreamUrl(account.baseUrl, anthropicClaudeCodePathAndQueryForRequest(req, bridgePath, {
      targetPathAndQuery: bridgePath
    }))]
  }
  if (isGatewayProtocolNativeRequest(req, GEMINI_PROTOCOL_CODE)) return []
  if (isGatewayProtocolNativeRequest(req, OPENAI_PROTOCOL_CODE) && !isOpenAIToAnthropicMessagesModelMapping(req, account)) return []
  return buildAnthropicUpstreamUrlsForAccount(account, req)
}

async function buildHybridAnthropicRequestParts(req: Request, account: DispatchAccountSecret, signal?: AbortSignal, context?: ProviderGatewayRequestContext) {
  const headers = copySafeUpstreamRequestHeaders(req.headers)
  headers.set('x-api-key', account.apiKey)
  headers.set('anthropic-version', headerText(req, 'anthropic-version') ?? '2023-06-01')
  const betaHeader = headerText(req, 'anthropic-beta')
  if (betaHeader) headers.set('anthropic-beta', betaHeader)
  applyAnthropicClientCompatibilityHeaders(req, headers, {
    requestClientCompatibility: context?.requestClientCompatibility
  })
  if (!headers.get('content-type') && req.method !== 'GET' && req.method !== 'HEAD') headers.set('content-type', 'application/json')
  if (!headers.get('accept')) headers.set('accept', requestStream(req) ? 'text/event-stream' : 'application/json')

  const mapping = resolveOpenAIRequestModelMapping(req, account)
  if (mapping && isGeminiGenerateContentToAnthropicMessagesModelMapping(mapping)) {
    prepareGeminiGenerateContentAnthropicMessagesBridgeHeaders(headers, req)
    return {
      headers,
      body: await buildGeminiGenerateContentAnthropicMessagesBridgeBody(req, {
        defaultModel: mapping.upstreamModel,
        guidanceProviderName: '混合供应商',
        modelOverride: mapping.upstreamModel
      }, signal)
    }
  }
  if (isOpenAIToAnthropicBridgeCandidateRequest(req) && isOpenAIToAnthropicMessagesModelMapping(req, account)) {
    prepareOpenAIToAnthropicBridgeHeaders(headers, req)
    const bridgePath = openAIToAnthropicBridgeUpstreamPath(req)
    applyAnthropicClientCompatibilityHeaders(req, headers, {
      requestClientCompatibility: context?.requestClientCompatibility,
      targetPathAndQuery: bridgePath
    })
    return {
      headers,
      body: await buildOpenAIToAnthropicBridgeBody(req, {
        guidanceProviderName: '混合供应商',
        modelOverride: openAIToAnthropicBridgeUpstreamModel(req, account),
        requestClientCompatibility: context?.requestClientCompatibility,
        targetPathAndQuery: bridgePath
      }, signal)
    }
  }
  return {
    headers,
    body: mapping ? await buildOpenAIModelMappedJsonBody(req, mapping.upstreamModel, signal) : buildUpstreamRequestBody(req)
  }
}

function transformHybridAnthropicResponse(req: Request, account: DispatchAccountSecret, response: Parameters<NonNullable<ProviderDriver['transformUpstreamResponse']>>[2], context?: Parameters<NonNullable<ProviderDriver['transformUpstreamResponse']>>[3]) {
  const mapping = resolveOpenAIRequestModelMapping(req, account)
  const geminiGenerateContentResponse = transformGeminiGenerateContentAnthropicMessagesBridgeUpstreamResponse(req, response, {
    enabled: isGeminiGenerateContentToAnthropicMessagesModelMapping(mapping),
    model: mapping?.upstreamModel ?? requestModel(req) ?? 'hybrid'
  })
  if (!isOpenAIToAnthropicMessagesModelMapping(req, account)) {
    return geminiGenerateContentResponse
  }
  return transformOpenAIToAnthropicBridgeUpstreamResponse(req, geminiGenerateContentResponse, {
    model: openAIToAnthropicBridgeUpstreamModel(req, account),
    previousResponseId: context?.codexResponsesChatBridgePreviousResponseId,
    onResponsesCompleted: context?.codexResponsesChatBridgeCompletionHandler,
    continueAnthropicMessagesRequest: context?.continueUpstreamJsonRequest
  })
}

function hybridAnthropicEndpointModeForGatewayRequest(req: Request, account: ProviderDriverAccount) {
  const mapping = resolveOpenAIRequestModelMapping(req, account)
  if (isGeminiGenerateContentToAnthropicMessagesModelMapping(mapping)) {
    return geminiGenerateContentAnthropicMessagesBridgeRequiredEndpointMode(req)
  }
  if (isOpenAIToAnthropicMessagesModelMapping(req, account)) {
    return openAIToAnthropicBridgeRequiredEndpointMode(req)
  }
  return anthropicEndpointModeForRequestShape({
    endpoint: req.path || req.originalUrl.split('?', 1)[0],
    stream: isEffectiveOpenAIStreamRequest(req, account)
  })
}

function hybridAnthropicAccountSupportsRequest(req: Request, account: ProviderDriverAccount, context?: { requestClientCompatibility?: Parameters<typeof accountSupportsClientCompatibility>[1] }): boolean {
  const mapping = resolveOpenAIRequestModelMapping(req, account)
  if (isGeminiGenerateContentToAnthropicMessagesModelMapping(mapping) || isOpenAIToAnthropicMessagesModelMapping(req, account)) {
    const mode = hybridAnthropicEndpointModeForGatewayRequest(req, account)
    if (!mode) return false
    return accountSupportsAnthropicEndpointMode(anthropicEndpointModeSupportInput(account, mode))
  }
  if (isGatewayProtocolNativeRequest(req, OPENAI_PROTOCOL_CODE) || isGatewayProtocolNativeRequest(req, GEMINI_PROTOCOL_CODE)) return false
  if (!accountSupportsClientCompatibility(account, context?.requestClientCompatibility)) return false
  const mode = hybridAnthropicEndpointModeForGatewayRequest(req, account)
  if (!mode) return false
  return accountSupportsAnthropicEndpointMode(anthropicEndpointModeSupportInput(account, mode))
}

function buildHybridGeminiUpstreamUrls(account: DispatchAccountSecret, req: Request): string[] {
  const mapping = resolveOpenAIRequestModelMapping(req, account)
  if (isOpenAIOrAnthropicToGeminiGenerateContentModelMapping(mapping)) {
    return [buildGeminiUpstreamUrl(account.baseUrl, geminiGenerateContentModelMappedUpstreamPathAndQuery(req, mapping))]
  }
  return buildGeminiUpstreamUrlsForAccount(account, req)
}

async function buildHybridGeminiRequestParts(req: Request, account: DispatchAccountSecret, signal?: AbortSignal) {
  const headers = copySafeUpstreamRequestHeaders(req.headers)
  headers.set('x-goog-api-key', account.apiKey)
  const mapping = resolveOpenAIRequestModelMapping(req, account)
  if (isOpenAIOrAnthropicToGeminiGenerateContentModelMapping(mapping)) {
    prepareOpenAIOrAnthropicToGeminiNativeHeaders(headers, req)
    return {
      headers,
      body: await buildOpenAIOrAnthropicToGeminiNativeBody(req, {
        mapping,
        providerName: '混合供应商'
      }, signal)
    }
  }
  if (!headers.get('content-type') && req.method !== 'GET' && req.method !== 'HEAD') headers.set('content-type', 'application/json')
  if (!headers.get('accept')) headers.set('accept', requestStream(req) || req.originalUrl.includes(':streamGenerateContent') ? 'text/event-stream' : 'application/json')
  return {
    headers,
    body: buildUpstreamRequestBody(req)
  }
}

function hybridGeminiEndpointModeForGatewayRequest(req: Request, account: ProviderDriverAccount) {
  const mapping = resolveOpenAIRequestModelMapping(req, account)
  if (isOpenAIOrAnthropicToGeminiGenerateContentModelMapping(mapping)) {
    return openAIOrAnthropicToGeminiNativeRequiredEndpointMode(req)
  }
  return geminiEndpointModeForRequestShape({
    endpoint: req.path || req.originalUrl.split('?', 1)[0],
    stream: isEffectiveOpenAIStreamRequest(req, account)
  })
}

function hybridGeminiAccountSupportsRequest(req: Request, account: ProviderDriverAccount): boolean {
  const mapping = resolveOpenAIRequestModelMapping(req, account)
  if (isOpenAIOrAnthropicToGeminiGenerateContentModelMapping(mapping)) {
    return accountSupportsGeminiEndpointMode(geminiEndpointModeSupportInput(account, openAIOrAnthropicToGeminiNativeRequiredEndpointMode(req)))
  }
  if (!isGeminiNativeRequest(req)) return false
  if (isGeminiModelsRequest(req)) return true
  const mode = hybridGeminiEndpointModeForGatewayRequest(req, account)
  if (!mode) return false
  return accountSupportsGeminiEndpointMode(geminiEndpointModeSupportInput(account, mode))
}

function openAIEndpointModeSupportInput(account: ProviderDriverAccount, mode: NonNullable<ReturnType<typeof hybridOpenAIEndpointModeForGatewayRequest>>) {
  return {
    mode,
    supportedEndpointModes: account.supportedEndpointModes,
    credentials: account.credentials,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    accountType: account.type,
    clientCompatibility: account.clientCompatibility
  }
}

function anthropicEndpointModeSupportInput(account: ProviderDriverAccount, mode: NonNullable<ReturnType<typeof hybridAnthropicEndpointModeForGatewayRequest>>) {
  return {
    mode,
    supportedEndpointModes: account.supportedEndpointModes,
    credentials: account.credentials,
    providerCode: account.providerCode,
    accountType: account.type,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    providerProtocolProfileId: account.providerProtocolProfileId
  }
}

function geminiEndpointModeSupportInput(account: ProviderDriverAccount, mode: NonNullable<ReturnType<typeof hybridGeminiEndpointModeForGatewayRequest>>) {
  return {
    mode,
    supportedEndpointModes: account.supportedEndpointModes,
    credentials: account.credentials,
    providerCode: account.providerCode,
    accountType: account.type,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    providerProtocolProfileId: account.providerProtocolProfileId
  }
}

function headerText(req: Request, name: string): string | undefined {
  const value = typeof req.header === 'function' ? req.header(name) : undefined
  const text = typeof value === 'string' ? value.trim() : ''
  return text || undefined
}
