import type { Request } from 'express'

import { accountSupportsClientCompatibility } from '../../../../domain/account-client-compatibility.js'
import {
  OPENAI_CHAT_ENDPOINT_MODES,
  accountSupportsOpenAIEndpointMode,
  openAIEndpointModeForRequestShape
} from '../../../../domain/openai-endpoint-modes.js'
import {
  ANTHROPIC_PROTOCOL_CODE,
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
  isOpenAIResponsesToChatCompletionsModelMapping,
  openAIModelMappedUpstreamPathAndQuery,
  resolveOpenAIAccountModelMapping,
  resolveOpenAIRequestModelMapping
} from '../../../gateway/protocols/openai-v1/model-mapping.js'
import { buildUpstreamUrl, splitPathAndQuery } from '../../../gateway/protocols/openai-v1/route-helpers.js'
import {
  buildUpstreamHeaders,
  buildUpstreamRequestBody,
  isEffectiveOpenAIStreamRequest
} from '../../../gateway/upstream/request.js'
import {
  buildCodexResponsesChatBridgeBody,
  codexResponsesChatBridgeRequiredEndpointMode,
  isCodexResponsesChatBridgeRequest,
  prepareCodexResponsesChatBridgeHeaders,
  transformCodexResponsesChatBridgeUpstreamResponse
} from '../_shared/codex-responses-chat-bridge.js'
import type { ProviderDriver, ProviderDriverAccount } from '../_shared/types.js'

const DEEPSEEK_CODEX_BRIDGE_DEFAULT_MODEL = 'deepseek-v4-flash'

function openAIEndpointModeForGatewayRequest(req: Request, account: ProviderDriverAccount) {
  if (req.method.toUpperCase() !== 'POST') return undefined
  const { path } = splitPathAndQuery(req.originalUrl || req.path || '')
  const requestPath = path.startsWith('/') ? path : `/${path}`
  const normalizedPath = requestPath.replace(/^\/v1(?=\/|$)/, '') || '/'
  if (normalizedPath !== '/chat/completions') return undefined
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
      modelMappingSource: modelMapping ? 'account' : undefined
    }
  },
  buildUpstreamUrls(account: DispatchAccountSecret, req: Request): string[] {
    if (isGatewayProtocolNativeRequest(req, ANTHROPIC_PROTOCOL_CODE)) {
      return []
    }
    return buildDeepSeekOpenAIChatUpstreamUrls(account, req)
  },
  async buildUpstreamRequestParts(req, account, _identity, signal, context) {
    const modelMapping = resolveOpenAIRequestModelMapping(req, account)
    if (shouldUseDeepSeekCodexResponsesChatBridge(req, account, context?.requestClientCompatibility, modelMapping)) {
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
    return transformCodexResponsesChatBridgeUpstreamResponse(req, response, {
      defaultModel: DEEPSEEK_CODEX_BRIDGE_DEFAULT_MODEL,
      enabled: shouldUseDeepSeekCodexResponsesChatBridge(req, account, context?.requestClientCompatibility, modelMapping),
      explicitMappingBridge: isOpenAIResponsesToChatCompletionsModelMapping(modelMapping),
      finishReasonFailures: {
        insufficient_system_resource: {
          code: 'upstream_retryable_error',
          message: '上游模型资源不足，请重试'
        }
      },
      idPrefix: 'deepseek_bridge',
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
    if (shouldUseDeepSeekCodexResponsesChatBridge(req, account, context?.requestClientCompatibility, modelMapping)) {
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
    if (!mode || !OPENAI_CHAT_ENDPOINT_MODES.includes(mode)) return false
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

function buildDeepSeekOpenAIChatUpstreamUrls(account: DispatchAccountSecret, req: Request): string[] {
  if (req.method.toUpperCase() !== 'POST') {
    return []
  }
  const modelMapping = resolveOpenAIRequestModelMapping(req, account)
  if (modelMapping && isOpenAIResponsesToChatCompletionsModelMapping(modelMapping)) {
    return [buildUpstreamUrl(account.baseUrl, openAIModelMappedUpstreamPathAndQuery(req, modelMapping))]
  }
  if (shouldUseDeepSeekCodexResponsesChatBridge(req, account, 'codex_responses', modelMapping)) {
    const { query } = splitPathAndQuery(req.originalUrl || req.path || '')
    return [buildUpstreamUrl(account.baseUrl, `/chat/completions${query}`)]
  }
  const { path, query } = splitPathAndQuery(req.originalUrl)
  const requestPath = path.startsWith('/') ? path : `/${path}`
  const normalizedPath = requestPath.replace(/^\/v1(?=\/|$)/, '') || '/'
  if (normalizedPath !== '/chat/completions') {
    return []
  }
  return [buildUpstreamUrl(account.baseUrl, `${normalizedPath}${query}`)]
}

function shouldUseDeepSeekCodexResponsesChatBridge(
  req: Request,
  account: ProviderDriverAccount,
  requestClientCompatibility: import('../../../../domain/types.js').ClientCompatibilityCapability | undefined,
  modelMapping: ReturnType<typeof resolveOpenAIRequestModelMapping>
): boolean {
  if (modelMapping && isOpenAIResponsesToChatCompletionsModelMapping(modelMapping)) {
    return true
  }
  return account.clientCompatibility === 'codex_responses'
    && isCodexResponsesChatBridgeRequest(req, {
      enabled: true,
      requestClientCompatibility
    })
}
