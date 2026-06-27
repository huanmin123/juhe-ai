import type { Request } from 'express'

import { accountSupportsClientCompatibility } from '../../../../domain/account-client-compatibility.js'
import {
  accountSupportsAnthropicEndpointMode,
  anthropicEndpointModeForRequestShape
} from '../../../../domain/anthropic-endpoint-modes.js'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_MESSAGES_FAMILY,
  ANTHROPIC_PROVIDER_CODE,
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION,
  DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GEMINI_PROTOCOL_CODE,
  GLM_CODING_ANTHROPIC_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  isAnthropicProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import { isGatewayProtocolNativeRequest } from '../../../gateway/protocols/registry.js'
import { buildAnthropicUpstreamUrl, buildAnthropicUpstreamUrlsForAccount } from '../../../gateway/protocols/anthropic-v1/route-helpers.js'
import {
  buildOpenAIModelMappedJsonBody,
  geminiGenerateContentToAnthropicMessagesUpstreamPathAndQuery,
  isGeminiGenerateContentToAnthropicMessagesModelMapping,
  openAIRequestEndpointFamily,
  resolveOpenAIAccountModelMapping,
  resolveOpenAIRequestModelMapping
} from '../../../gateway/protocols/openai-v1/model-mapping.js'
import { openAICompatibleFilesResolverForGatewayRequest } from '../../../openai-compatible-files/file-resolver.js'
import { openAICompatibleCodeInterpreterExecutorForGatewayRequest } from '../../../openai-compatible-code-interpreter/code-interpreter-executor.js'
import { openAICompatibleComputerExecutorForGatewayRequest } from '../../../openai-compatible-computer/computer-adapter.js'
import { openAICompatibleImageGenerationExecutorForGatewayRequest } from '../../../openai-compatible-images/image-generation-executor.js'
import { openAICompatibleFileSearchExecutorForGatewayRequest } from '../../../openai-compatible-vector-stores/file-search-executor.js'
import { requestModel, requestStream } from '../../../gateway/request/metadata.js'
import {
  buildUpstreamRequestBody,
  copySafeUpstreamRequestHeaders,
  isEffectiveOpenAIStreamRequest
} from '../../../gateway/upstream/request.js'
import type { ProviderDriver, ProviderDriverAccount } from '../_shared/types.js'
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
import {
  buildGeminiGenerateContentAnthropicMessagesBridgeBody,
  geminiGenerateContentAnthropicMessagesBridgeRequiredEndpointMode,
  prepareGeminiGenerateContentAnthropicMessagesBridgeHeaders,
  transformGeminiGenerateContentAnthropicMessagesBridgeUpstreamResponse
} from '../_shared/gemini-anthropic-messages-bridge.js'

const defaultAnthropicVersion = '2023-06-01'

function anthropicEndpointModeForGatewayRequest(req: Request, account: ProviderDriverAccount) {
  const modelMapping = resolveOpenAIRequestModelMapping(req, account)
  if (isGeminiGenerateContentToAnthropicMessagesModelMapping(modelMapping)) {
    return geminiGenerateContentAnthropicMessagesBridgeRequiredEndpointMode(req)
  }
  const bridgeMode = openAIToAnthropicBridgeRequiredEndpointMode(req)
  if (bridgeMode) return bridgeMode
  return anthropicEndpointModeForRequestShape({
    endpoint: req.path || req.originalUrl.split('?', 1)[0],
    stream: isEffectiveOpenAIStreamRequest(req, account)
  })
}

export const anthropicProviderDriver: ProviderDriver = {
  id: 'anthropic',
  providerCode: ANTHROPIC_PROVIDER_CODE,
  protocolCode: ANTHROPIC_PROTOCOL_CODE,
  protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
  usageSemantic: 'anthropic',
  profileIds: [
    ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
    GLM_CODING_ANTHROPIC_V1_PROFILE_ID
  ],
  supportsProfile(profile) {
    const profileId = profile?.providerProtocolProfileId ?? profile?.id
    if (!isAnthropicProtocolProfile(profile)) return false
    return (
      profile?.providerCode === ANTHROPIC_PROVIDER_CODE
      && profileId === ANTHROPIC_ANTHROPIC_V1_PROFILE_ID
    ) || (
      profile?.providerCode === DEEPSEEK_PROVIDER_CODE
      && profileId === DEEPSEEK_ANTHROPIC_V1_PROFILE_ID
    ) || (
      profile?.providerCode === GLM_PROVIDER_CODE
      && profileId === GLM_CODING_ANTHROPIC_V1_PROFILE_ID
    )
  },
  resolveUsageModel(account, requestedModel, sourceEndpointFamily) {
    const modelMapping = resolveOpenAIAccountModelMapping(account, requestedModel, sourceEndpointFamily)
    return {
      upstreamModel: modelMapping?.upstreamModel ?? requestedModel,
      modelMappingApplied: Boolean(modelMapping),
      modelMappingSource: modelMapping ? modelMapping.runtimeSource ?? 'account' : undefined
    }
  },
  buildUpstreamUrls(account: DispatchAccountSecret, req: Request): string[] {
    const modelMapping = resolveOpenAIRequestModelMapping(req, account)
    if (isGatewayProtocolNativeRequest(req, GEMINI_PROTOCOL_CODE) && !isGeminiGenerateContentToAnthropicMessagesModelMapping(modelMapping)) {
      return []
    }
    if (account.type === 'api_key' && isGeminiGenerateContentToAnthropicMessagesModelMapping(modelMapping)) {
      return [buildAnthropicUpstreamUrl(account.baseUrl, geminiGenerateContentToAnthropicMessagesUpstreamPathAndQuery(req))]
    }
    const bridgePath = openAIToAnthropicBridgeUpstreamPath(req)
    if (account.type === 'api_key' && bridgePath) {
      return [buildAnthropicUpstreamUrl(account.baseUrl, bridgePath)]
    }
    return buildAnthropicUpstreamUrlsForAccount(account, req)
  },
  async buildUpstreamRequestParts(req, account, _identity, signal, context) {
    if (account.type !== 'api_key') {
      throw new Error('Anthropic 当前仅支持 API Key 账户')
    }
    const headers = copySafeUpstreamRequestHeaders(req.headers)
    applyAnthropicUpstreamAuthHeaders(headers, account)
    headers.set('anthropic-version', anthropicVersionHeader(req))
    const betaHeader = anthropicBetaHeader(req)
    if (betaHeader) {
      headers.set('anthropic-beta', betaHeader)
    }
    if (!headers.get('content-type') && req.method !== 'GET' && req.method !== 'HEAD') {
      headers.set('content-type', 'application/json')
    }
    if (!headers.get('accept')) {
      headers.set('accept', requestStream(req) ? 'text/event-stream' : 'application/json')
    }
    const modelMapping = resolveOpenAIRequestModelMapping(req, account)
    if (modelMapping && isGeminiGenerateContentToAnthropicMessagesModelMapping(modelMapping)) {
      prepareGeminiGenerateContentAnthropicMessagesBridgeHeaders(headers, req)
      return {
        headers,
        body: await buildGeminiGenerateContentAnthropicMessagesBridgeBody(req, {
          defaultModel: modelMapping.upstreamModel,
          guidanceProviderName: guidanceProviderNameForAccount(account),
          modelOverride: modelMapping.upstreamModel
        }, signal)
      }
    }
    if (shouldUseOpenAIToAnthropicBridge(req, account)) {
      prepareOpenAIToAnthropicBridgeHeaders(headers, req)
      return {
        headers,
        body: await buildOpenAIToAnthropicBridgeBody(req, {
          guidanceProviderName: guidanceProviderNameForAccount(account),
          modelOverride: openAIToAnthropicBridgeUpstreamModel(req, account),
          fileResolver: openAICompatibleFilesResolverForGatewayRequest(req),
          fileSearchExecutor: openAICompatibleFileSearchExecutorForGatewayRequest(req),
          codeInterpreterExecutor: openAICompatibleCodeInterpreterExecutorForGatewayRequest(req),
          computerExecutor: openAICompatibleComputerExecutorForGatewayRequest(req),
          imageGenerationExecutor: openAICompatibleImageGenerationExecutorForGatewayRequest()
        }, signal)
      }
    }
    return {
      headers,
      body: modelMapping
        ? await buildOpenAIModelMappedJsonBody(req, modelMapping.upstreamModel, signal)
        : buildAnthropicNativePassthroughBody(req)
    }
  },
  transformUpstreamResponse(req, account, response, context) {
    const modelMapping = resolveOpenAIRequestModelMapping(req, account)
    const geminiGenerateContentResponse = transformGeminiGenerateContentAnthropicMessagesBridgeUpstreamResponse(req, response, {
      enabled: isGeminiGenerateContentToAnthropicMessagesModelMapping(modelMapping),
      model: modelMapping?.upstreamModel ?? requestModel(req) ?? 'anthropic'
    })
    if (!shouldUseOpenAIToAnthropicBridge(req, account)) {
      return geminiGenerateContentResponse
    }
    return transformOpenAIToAnthropicBridgeUpstreamResponse(req, geminiGenerateContentResponse, {
      model: openAIToAnthropicBridgeUpstreamModel(req, account),
      previousResponseId: context?.codexResponsesChatBridgePreviousResponseId,
      onResponsesCompleted: context?.codexResponsesChatBridgeCompletionHandler,
      continueAnthropicMessagesRequest: context?.continueUpstreamJsonRequest
    })
  },
  endpointModeForRequest: anthropicEndpointModeForGatewayRequest,
  accountSupportsRequest(req, account, context) {
    const modelMapping = resolveOpenAIRequestModelMapping(req, account)
    if (isGatewayProtocolNativeRequest(req, GEMINI_PROTOCOL_CODE)) {
      if (!isGeminiGenerateContentToAnthropicMessagesModelMapping(modelMapping)) {
        return false
      }
      return accountSupportsAnthropicEndpointMode({
        mode: geminiGenerateContentAnthropicMessagesBridgeRequiredEndpointMode(req),
        supportedEndpointModes: account.supportedEndpointModes,
        credentials: account.credentials,
        providerCode: account.providerCode,
        accountType: account.type,
        protocolCode: account.protocolCode,
        protocolVersion: account.protocolVersion,
        providerProtocolProfileId: account.providerProtocolProfileId
      })
    }
    if (isOpenAIToAnthropicBridgeCandidateRequest(req)) {
      if (!shouldUseOpenAIToAnthropicBridge(req, account)) {
        return false
      }
      const mode = anthropicEndpointModeForGatewayRequest(req, account)
      if (!mode) return false
      return accountSupportsAnthropicEndpointMode({
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
    if (!accountSupportsClientCompatibility(account, context?.requestClientCompatibility)) {
      return false
    }
    const mode = anthropicEndpointModeForGatewayRequest(req, account)
    if (!mode) return true
    return accountSupportsAnthropicEndpointMode({
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

function guidanceProviderNameForAccount(account: DispatchAccountSecret): string {
  if (account.providerCode === GLM_PROVIDER_CODE) return 'GLM'
  if (account.providerCode === DEEPSEEK_PROVIDER_CODE) return 'DeepSeek'
  if (account.providerCode === ANTHROPIC_PROVIDER_CODE) return 'Anthropic'
  return account.providerCode
}

function shouldUseOpenAIToAnthropicBridge(
  req: Request,
  account: ProviderDriverAccount
): boolean {
  return isOpenAIToAnthropicBridgeCandidateRequest(req)
    && isOpenAIToAnthropicMessagesModelMapping(req, account)
}

function applyAnthropicUpstreamAuthHeaders(headers: Headers, account: DispatchAccountSecret): void {
  if (account.providerCode === GLM_PROVIDER_CODE && account.providerProtocolProfileId === GLM_CODING_ANTHROPIC_V1_PROFILE_ID) {
    headers.set('authorization', `Bearer ${account.apiKey}`)
    return
  }
  headers.set('x-api-key', account.apiKey)
}

function anthropicVersionHeader(req: Request): string {
  return headerText(req, 'anthropic-version')
    ?? defaultAnthropicVersion
}

function anthropicBetaHeader(req: Request): string | undefined {
  const values = [headerText(req, 'anthropic-beta')]
  const normalized = new Map<string, string>()
  for (const value of values) {
    for (const item of splitAnthropicBetaHeader(value)) {
      const key = item.toLowerCase()
      if (!normalized.has(key)) {
        normalized.set(key, item)
      }
    }
  }
  const merged = [...normalized.values()]
  return merged.length ? merged.join(',') : undefined
}

function buildAnthropicNativePassthroughBody(req: Request): Buffer | undefined {
  const body = buildUpstreamRequestBody(req)
  if (!body || !isAnthropicMessagesPath(req)) return body
  let parsed: unknown
  try {
    parsed = JSON.parse(body.toString('utf8')) as unknown
  } catch {
    return body
  }
  if (!isPlainObject(parsed)) return body
  const normalized = { ...parsed }
  if (normalized.stream === false) {
    delete normalized.stream
  }
  if (Array.isArray(normalized.messages)) {
    normalized.messages = normalized.messages.map(normalizeAnthropicNativeMessage)
  }
  return Buffer.from(JSON.stringify(normalized), 'utf8')
}

function normalizeAnthropicNativeMessage(value: unknown): unknown {
  if (!isPlainObject(value) || !Array.isArray(value.content)) return value
  const content = value.content
  if (!content.every(isPlainAnthropicNativeTextBlock)) return value
  return {
    ...value,
    content: content.map((block) => typeof block.text === 'string' ? block.text : '').join('')
  }
}

function isPlainAnthropicNativeTextBlock(value: unknown): value is { type: 'text'; text: string } {
  return isPlainObject(value)
    && value.type === 'text'
    && typeof value.text === 'string'
    && Object.keys(value).every((key) => key === 'type' || key === 'text')
}

function isAnthropicMessagesPath(req: Request): boolean {
  const path = (req.originalUrl || req.path || '').split('?', 1)[0] ?? ''
  return (path.startsWith('/') ? path : `/${path}`).replace(/^\/v1(?=\/|$)/, '') === '/messages'
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function splitAnthropicBetaHeader(value: string | undefined): string[] {
  if (!value) return []
  return value
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean)
}

function headerText(req: Request, name: string): string | undefined {
  const value = typeof req.header === 'function' ? req.header(name) : undefined
  const text = typeof value === 'string' ? value.trim() : ''
  return text || undefined
}
