import type { Request } from 'express'

import { accountSupportsClientCompatibility } from '../../../../domain/account-client-compatibility.js'
import {
  accountSupportsOpenAIEndpointMode,
  openAIEndpointModeForRequestShape
} from '../../../../domain/openai-endpoint-modes.js'
import {
  ANTHROPIC_PROTOCOL_CODE,
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  isGlmProviderCode,
  isOpenAIProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import { isGatewayProtocolNativeRequest } from '../../../gateway/protocols/registry.js'
import { applyOpenAIClientCompatibilityHeaders, buildOpenAIClientCompatibilityBody } from '../../../gateway/protocols/openai-v1/api-key-client-compatibility.js'
import {
  buildOpenAIModelMappedJsonBody,
  isAnthropicMessagesToChatCompletionsModelMapping,
  isOpenAIResponsesToChatCompletionsModelMapping,
  openAIModelMappedUpstreamPathAndQuery,
  resolveOpenAIAccountModelMapping,
  resolveOpenAIRequestModelMapping
} from '../../../gateway/protocols/openai-v1/model-mapping.js'
import { splitPathAndQuery } from '../../../gateway/protocols/openai-v1/route-helpers.js'
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
  buildCodexResponsesChatBridgeBody,
  codexResponsesChatBridgeRequiredEndpointMode,
  prepareCodexResponsesChatBridgeHeaders,
  transformCodexResponsesChatBridgeUpstreamResponse
} from '../_shared/codex-responses-chat-bridge.js'
import type { ProviderDriver, ProviderDriverAccount } from '../_shared/types.js'

const glmProfileIds = [GLM_GENERAL_OPENAI_V1_PROFILE_ID, GLM_CODING_OPENAI_V1_PROFILE_ID] as const
const GLM_CODEX_BRIDGE_DEFAULT_MODEL = 'glm-5.2'

function openAIEndpointModeForGatewayRequest(req: Request, account: ProviderDriverAccount) {
  return openAIEndpointModeForRequestShape({
    endpoint: (req.originalUrl || req.path || '').split('?', 1)[0],
    stream: isEffectiveOpenAIStreamRequest(req, account)
  })
}

function isGlmSupportedEndpointMode(mode: string | undefined): boolean {
  return mode === 'chat_json' || mode === 'chat_sse'
}

export const glmProviderDriver: ProviderDriver = {
  id: 'glm',
  providerCode: GLM_PROVIDER_CODE,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  usageSemantic: 'openai',
  profileIds: glmProfileIds,
  supportsProfile(profile) {
    if (!profile || !isGlmProviderCode(profile.providerCode) || !isOpenAIProtocolProfile(profile)) {
      return false
    }
    const profileId = profile.providerProtocolProfileId ?? profile.id
    return Boolean(profileId && glmProfileIds.includes(profileId as never))
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
    return buildGlmOpenAIChatUpstreamUrls(account, req)
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
          guidanceProviderName: 'GLM',
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
          defaultModel: GLM_CODEX_BRIDGE_DEFAULT_MODEL,
          guidanceProviderName: 'GLM',
          includeReasoningContent: true,
          modelOverride: modelMapping?.upstreamModel
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
      model: modelMapping?.upstreamModel ?? requestModel(req) ?? GLM_CODEX_BRIDGE_DEFAULT_MODEL
    })
    return transformCodexResponsesChatBridgeUpstreamResponse(req, anthropicMessagesResponse, {
      defaultModel: GLM_CODEX_BRIDGE_DEFAULT_MODEL,
      enabled: isOpenAIResponsesToChatCompletionsModelMapping(modelMapping),
      explicitMappingBridge: true,
      finishReasonFailures: {
        sensitive: {
          code: 'content_filter',
          message: '上游模型触发内容安全拦截'
        },
        network_error: {
          code: 'upstream_retryable_error',
          message: '上游模型返回网络错误，请重试'
        },
        model_context_window_exceeded: {
          code: 'context_length_exceeded',
          message: '上游模型上下文窗口超限'
        }
      },
      idPrefix: 'glm_bridge',
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
    if (!isGlmSupportedEndpointMode(mode)) return false
    return accountSupportsOpenAIEndpointMode({
      mode: mode as 'chat_json' | 'chat_sse',
      supportedEndpointModes: account.supportedEndpointModes,
      credentials: account.credentials,
      providerCode: account.providerCode,
      providerProtocolProfileId: account.providerProtocolProfileId,
      accountType: account.type,
      clientCompatibility: account.clientCompatibility
    })
  }
}

function buildGlmOpenAIChatUpstreamUrls(account: DispatchAccountSecret, req: Request): string[] {
  if (req.method.toUpperCase() !== 'POST') {
    return []
  }
  const baseUrl = normalizeGlmOpenAIChatBaseUrl(account.baseUrl)
  const modelMapping = resolveOpenAIRequestModelMapping(req, account)
  if (modelMapping && (isOpenAIResponsesToChatCompletionsModelMapping(modelMapping) || isAnthropicMessagesToChatCompletionsModelMapping(modelMapping))) {
    return [`${baseUrl}${openAIModelMappedUpstreamPathAndQuery(req, modelMapping)}`]
  }
  const { path, query } = splitPathAndQuery(req.originalUrl)
  const requestPath = path.startsWith('/') ? path : `/${path}`
  const normalizedPath = requestPath.replace(/^\/v1(?=\/|$)/, '') || '/'
  if (normalizedPath !== '/chat/completions') {
    return []
  }
  return [`${baseUrl}${normalizedPath}${query}`]
}

function normalizeGlmOpenAIChatBaseUrl(baseUrl: string): string {
  const normalizedBase = baseUrl.trim().replace(/\/+$/, '')
  try {
    const url = new URL(normalizedBase)
    const normalizedPath = url.pathname.replace(/\/+$/, '')
    if (!normalizedPath || normalizedPath === '/') {
      return url.origin
    }
  } catch {
    return normalizedBase
  }
  return normalizedBase
}
