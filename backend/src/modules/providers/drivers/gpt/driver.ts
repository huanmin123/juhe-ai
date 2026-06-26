import type { Request } from 'express'

import { accountSupportsClientCompatibility } from '../../../../domain/account-client-compatibility.js'
import {
  accountSupportsOpenAIEndpointMode,
  openAIEndpointModeForRequestShape
} from '../../../../domain/openai-endpoint-modes.js'
import {
  ANTHROPIC_PROTOCOL_CODE,
  GEMINI_PROTOCOL_CODE,
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  isGptVendorCode,
  isOpenAIProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import { buildOpenAIOAuthCodexRequestParts } from '../../../gateway/adapters/gpt-codex/oauth-adapter.js'
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
import {
  buildOpenAICodexUpstreamUrls,
  buildUpstreamUrl,
  buildUpstreamUrls
} from '../../../gateway/protocols/openai-v1/route-helpers.js'
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
import { prepareGptAccountBeforeDispatch } from './oauth-dispatch-preparation.js'

function openAIEndpointModeForGatewayRequest(req: Request, account: ProviderDriverAccount) {
  return openAIEndpointModeForRequestShape({
    endpoint: req.path || req.originalUrl.split('?', 1)[0],
    stream: isEffectiveOpenAIStreamRequest(req, account)
  })
}

export const gptProviderDriver: ProviderDriver = {
  id: 'gpt',
  providerCode: GPT_VENDOR_CODE,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  usageSemantic: 'openai',
  profileIds: [GPT_OPENAI_V1_PROFILE_ID],
  supportsProfile(profile) {
    const profileId = profile?.providerProtocolProfileId ?? profile?.id
    return isGptVendorCode(profile?.providerCode)
      && isOpenAIProtocolProfile(profile)
      && profileId === GPT_OPENAI_V1_PROFILE_ID
  },
  resolveUsageModel(account, requestedModel, sourceEndpointFamily) {
    const modelMapping = resolveOpenAIAccountModelMapping(account, requestedModel, sourceEndpointFamily)
    return {
      upstreamModel: modelMapping?.upstreamModel ?? requestedModel,
      modelMappingApplied: Boolean(modelMapping),
      modelMappingSource: modelMapping ? modelMapping.runtimeSource ?? 'account' : undefined
    }
  },
  async prepareAccountBeforeDispatch(account, context) {
    return await prepareGptAccountBeforeDispatch(account, context.signal)
  },
  buildUpstreamUrls(account: DispatchAccountSecret, req: Request): string[] {
    const modelMapping = resolveOpenAIRequestModelMapping(req, account)
    if (isGatewayProtocolNativeRequest(req, ANTHROPIC_PROTOCOL_CODE) && !(account.type !== 'oauth' && isAnthropicMessagesToChatCompletionsModelMapping(modelMapping))) {
      return []
    }
    if (isGatewayProtocolNativeRequest(req, GEMINI_PROTOCOL_CODE) && !(account.type !== 'oauth' && isGeminiGenerateContentToChatCompletionsModelMapping(modelMapping))) {
      return []
    }
    if (account.type !== 'oauth' && modelMapping && (isOpenAIResponsesToChatCompletionsModelMapping(modelMapping) || isAnthropicMessagesToChatCompletionsModelMapping(modelMapping) || isGeminiGenerateContentToChatCompletionsModelMapping(modelMapping))) {
      return [buildUpstreamUrl(account.baseUrl, openAIModelMappedUpstreamPathAndQuery(req, modelMapping))]
    }
    if (account.type === 'oauth') {
      return buildOpenAICodexUpstreamUrls(req)
    }
    return buildUpstreamUrls(account.baseUrl, req.originalUrl)
  },
  async buildUpstreamRequestParts(req, account, identity, signal, context) {
    const modelMapping = resolveOpenAIRequestModelMapping(req, account)
    if (account.type !== 'oauth' && modelMapping && isAnthropicMessagesToChatCompletionsModelMapping(modelMapping)) {
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
    if (account.type !== 'oauth' && modelMapping && isGeminiGenerateContentToChatCompletionsModelMapping(modelMapping)) {
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
    if (account.type !== 'oauth' && modelMapping && isOpenAIResponsesToChatCompletionsModelMapping(modelMapping)) {
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
    if (account.type === 'oauth') {
      return await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity, signal, {
        modelOverride: modelMapping?.upstreamModel
      })
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
      enabled: account.type !== 'oauth' && isAnthropicMessagesToChatCompletionsModelMapping(modelMapping),
      model: modelMapping?.upstreamModel ?? requestModel(req) ?? 'gpt-5.3-codex'
    })
    const geminiGenerateContentResponse = transformGeminiGenerateContentChatBridgeUpstreamResponse(req, anthropicMessagesResponse, {
      enabled: account.type !== 'oauth' && isGeminiGenerateContentToChatCompletionsModelMapping(modelMapping),
      model: modelMapping?.upstreamModel ?? requestModel(req) ?? 'gpt-5.3-codex'
    })
    return transformCodexResponsesChatBridgeUpstreamResponse(req, geminiGenerateContentResponse, {
      defaultModel: modelMapping?.upstreamModel ?? requestModel(req) ?? 'gpt-5.3-codex',
      enabled: account.type !== 'oauth' && isOpenAIResponsesToChatCompletionsModelMapping(modelMapping),
      explicitMappingBridge: true,
      idPrefix: 'openai_bridge',
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
    if (account.type !== 'oauth' && modelMapping && isAnthropicMessagesToChatCompletionsModelMapping(modelMapping)) {
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
    if (account.type !== 'oauth' && modelMapping && isGeminiGenerateContentToChatCompletionsModelMapping(modelMapping)) {
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
    if (account.type !== 'oauth' && modelMapping && isOpenAIResponsesToChatCompletionsModelMapping(modelMapping)) {
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
