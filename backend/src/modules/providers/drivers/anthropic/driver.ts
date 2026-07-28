import type { Request } from 'express'

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
import {
  anthropicClaudeCodePathAndQueryForRequest,
  applyAnthropicClientCompatibilityHeaders
} from '../../../gateway/protocols/anthropic-v1/client-compatibility.js'
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
import { applyProviderAccountRequestOverridesToBody } from '../_shared/provider-request-overrides.js'
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
import { prepareAnthropicAccountBeforeDispatch } from './oauth-dispatch-preparation.js'

const defaultAnthropicVersion = '2023-06-01'
const anthropicOAuthBetaHeaders = [
  'claude-code-20250219',
  'oauth-2025-04-20',
  'interleaved-thinking-2025-05-14',
  'fine-grained-tool-streaming-2025-05-14'
] as const

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
  async prepareAccountBeforeDispatch(account, context) {
    return await prepareAnthropicAccountBeforeDispatch(account, context.signal)
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
    const modelMapping = resolveOpenAIRequestModelMapping(req, account)
    if (isGatewayProtocolNativeRequest(req, GEMINI_PROTOCOL_CODE) && !isGeminiGenerateContentToAnthropicMessagesModelMapping(modelMapping)) {
      return []
    }
    if (supportsAnthropicCredentialDispatch(account) && isGeminiGenerateContentToAnthropicMessagesModelMapping(modelMapping)) {
      return [buildAnthropicUpstreamUrl(account.baseUrl, geminiGenerateContentToAnthropicMessagesUpstreamPathAndQuery(req))]
    }
    const bridgePath = openAIToAnthropicBridgeUpstreamPath(req)
    if (supportsAnthropicCredentialDispatch(account) && bridgePath && shouldUseOpenAIToAnthropicBridge(req, account)) {
      return [buildAnthropicUpstreamUrl(account.baseUrl, anthropicClaudeCodePathAndQueryForRequest(req, bridgePath, {
        targetPathAndQuery: bridgePath
      }))]
    }
    return buildAnthropicUpstreamUrlsForAccount(account, req)
  },
  async buildUpstreamRequestParts(req, account, _identity, signal, context) {
    if (!supportsAnthropicCredentialDispatch(account)) {
      throw new Error('Anthropic 当前仅支持 API Key 或 OAuth Access Token 账户')
    }
    const headers = copySafeUpstreamRequestHeaders(req.headers)
    applyAnthropicUpstreamAuthHeaders(headers, account)
    headers.set('anthropic-version', anthropicVersionHeader(req))
    const betaHeader = anthropicBetaHeader(req, account)
    if (betaHeader) {
      headers.set('anthropic-beta', betaHeader)
    }
    if (account.providerCode === ANTHROPIC_PROVIDER_CODE && account.type === 'oauth') {
      applyAnthropicOAuthCliIdentityHeaders(headers)
    }
    applyAnthropicClientCompatibilityHeaders(req, headers, {
      requestClientCompatibility: context?.requestClientCompatibility
    })
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
        body: await applyAnthropicOverrides(account, await buildGeminiGenerateContentAnthropicMessagesBridgeBody(req, {
          defaultModel: modelMapping.upstreamModel,
          guidanceProviderName: guidanceProviderNameForAccount(account),
          modelOverride: modelMapping.upstreamModel
        }, signal), modelMapping.upstreamModel, signal)
      }
    }
    if (shouldUseOpenAIToAnthropicBridge(req, account)) {
      prepareOpenAIToAnthropicBridgeHeaders(headers, req)
      const bridgePath = openAIToAnthropicBridgeUpstreamPath(req)
      applyAnthropicClientCompatibilityHeaders(req, headers, {
        requestClientCompatibility: context?.requestClientCompatibility,
        targetPathAndQuery: bridgePath
      })
      return {
        headers,
        body: await applyAnthropicOverrides(account, await buildOpenAIToAnthropicBridgeBody(req, {
          guidanceProviderName: guidanceProviderNameForAccount(account),
          modelOverride: openAIToAnthropicBridgeUpstreamModel(req, account),
          requestClientCompatibility: context?.requestClientCompatibility,
          targetPathAndQuery: bridgePath,
          fileResolver: openAICompatibleFilesResolverForGatewayRequest(req),
          fileSearchExecutor: openAICompatibleFileSearchExecutorForGatewayRequest(req),
          codeInterpreterExecutor: openAICompatibleCodeInterpreterExecutorForGatewayRequest(req),
          computerExecutor: openAICompatibleComputerExecutorForGatewayRequest(req),
          imageGenerationExecutor: openAICompatibleImageGenerationExecutorForGatewayRequest(req)
        }, signal), openAIToAnthropicBridgeUpstreamModel(req, account), signal)
      }
    }
    const nativeBody = modelMapping
      ? await buildOpenAIModelMappedJsonBody(req, modelMapping.upstreamModel, signal)
      : buildUpstreamRequestBody(req)
    return {
      headers,
      body: isAnthropicMessagesPath(req)
        ? await applyAnthropicOverrides(account, nativeBody, modelMapping?.upstreamModel ?? requestModel(req), signal)
        : nativeBody
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
      continueAnthropicMessagesRequest: context?.continueUpstreamJsonRequest,
      signal: context?.signal
    })
  },
  endpointModeForRequest: anthropicEndpointModeForGatewayRequest,
  accountSupportsRequest(req, account) {
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

function applyAnthropicOAuthCliIdentityHeaders(headers: Headers): void {
  const identityHeaders: Record<string, string> = {
    'user-agent': 'claude-cli/2.1.161 (external, cli)',
    'x-stainless-lang': 'js',
    'x-stainless-package-version': '0.94.0',
    'x-stainless-os': 'Linux',
    'x-stainless-arch': 'arm64',
    'x-stainless-runtime': 'node',
    'x-stainless-runtime-version': 'v24.3.0',
    'x-stainless-retry-count': '0',
    'x-stainless-timeout': '600',
    'x-app': 'cli',
    'anthropic-dangerous-direct-browser-access': 'true'
  }
  for (const [name, value] of Object.entries(identityHeaders)) headers.set(name, value)
}

async function applyAnthropicOverrides(
  account: DispatchAccountSecret,
  body: Buffer | string | undefined,
  upstreamModel: string | undefined,
  signal?: AbortSignal
): Promise<Buffer | string | undefined> {
  if (account.providerCode !== ANTHROPIC_PROVIDER_CODE) return body
  return await applyProviderAccountRequestOverridesToBody(body, {
    account,
    upstreamModel,
    wireFormat: 'anthropic_messages',
    signal
  })
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

function supportsAnthropicCredentialDispatch(account: ProviderDriverAccount): boolean {
  return account.type === 'api_key' || account.type === 'oauth'
}

function applyAnthropicUpstreamAuthHeaders(headers: Headers, account: DispatchAccountSecret): void {
  if (account.type === 'oauth') {
    const credential = typeof account.credentials.access_token === 'string'
      ? account.credentials.access_token.trim()
      : ''
    if (!credential) {
      throw new Error('Anthropic OAuth 账户缺少 Access Token')
    }
    headers.set('authorization', `Bearer ${credential}`)
    return
  }
  if (account.providerCode === GLM_PROVIDER_CODE && account.providerProtocolProfileId === GLM_CODING_ANTHROPIC_V1_PROFILE_ID) {
    const credential = account.apiKey?.trim()
    if (!credential) {
      throw new Error('GLM Coding Anthropic 账户缺少 Bearer Token')
    }
    headers.set('authorization', `Bearer ${credential}`)
    return
  }
  const credential = account.apiKey?.trim()
  if (!credential) {
    throw new Error('Anthropic API Key 账户缺少 API Key')
  }
  headers.set('x-api-key', credential)
}

function anthropicVersionHeader(req: Request): string {
  return headerText(req, 'anthropic-version')
    ?? defaultAnthropicVersion
}

function anthropicBetaHeader(req: Request, account: DispatchAccountSecret): string | undefined {
  const values = [
    headerText(req, 'anthropic-beta'),
    account.providerCode === ANTHROPIC_PROVIDER_CODE && account.type === 'oauth'
      ? anthropicOAuthBetaHeaders.join(',')
      : undefined
  ]
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

function isAnthropicMessagesPath(req: Request): boolean {
  const path = (req.originalUrl || req.path || '').split('?', 1)[0] ?? ''
  return (path.startsWith('/') ? path : `/${path}`).replace(/^\/v1(?=\/|$)/, '') === '/messages'
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
