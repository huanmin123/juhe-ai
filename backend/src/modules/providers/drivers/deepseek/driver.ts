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
  codexResponsesChatBridgeUpstreamPath,
  isCodexResponsesChatBridgeCandidateRequest,
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
  resolveUsageModel(account, requestedModel) {
    const modelMapping = resolveOpenAIAccountModelMapping(account, requestedModel)
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
    if (isDeepSeekCodexResponsesBridgeRequest(req, account, context?.requestClientCompatibility)) {
      const headers = buildUpstreamHeaders(req.headers, account)
      prepareCodexResponsesChatBridgeHeaders(headers)
      return {
        headers,
        body: await buildCodexResponsesChatBridgeBody(req, {
          defaultModel: DEEPSEEK_CODEX_BRIDGE_DEFAULT_MODEL,
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
      enabled: isDeepSeekCodexResponsesBridgeEnabled(account),
      idPrefix: 'deepseek_bridge',
      model: modelMapping?.upstreamModel,
      requestClientCompatibility: context?.requestClientCompatibility
    })
  },
  endpointModeForRequest: openAIEndpointModeForGatewayRequest,
  accountSupportsRequest(req, account, context) {
    if (isDeepSeekCodexResponsesBridgeRequest(req, account, context?.requestClientCompatibility)) {
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
  if (isDeepSeekCodexResponsesBridgeCandidateRequest(req, account)) {
    const bridgePath = codexResponsesChatBridgeUpstreamPath(req)
    return bridgePath ? [buildUpstreamUrl(account.baseUrl, bridgePath)] : []
  }
  const { path, query } = splitPathAndQuery(req.originalUrl)
  const requestPath = path.startsWith('/') ? path : `/${path}`
  const normalizedPath = requestPath.replace(/^\/v1(?=\/|$)/, '') || '/'
  if (normalizedPath !== '/chat/completions') {
    return []
  }
  return [buildUpstreamUrl(account.baseUrl, `${normalizedPath}${query}`)]
}

function isDeepSeekCodexResponsesBridgeRequest(
  req: Request,
  account: { clientCompatibility?: string; providerProtocolProfileId?: string },
  requestClientCompatibility?: 'openai_standard' | 'codex_responses' | 'anthropic_native' | 'claude_code'
): boolean {
  return isCodexResponsesChatBridgeRequest(req, {
    enabled: isDeepSeekCodexResponsesBridgeEnabled(account),
    requestClientCompatibility
  })
}

function isDeepSeekCodexResponsesBridgeCandidateRequest(
  req: Request,
  account: { clientCompatibility?: string; providerProtocolProfileId?: string }
): boolean {
  return isCodexResponsesChatBridgeCandidateRequest(req, isDeepSeekCodexResponsesBridgeEnabled(account))
}

function isDeepSeekCodexResponsesBridgeEnabled(account: { clientCompatibility?: string; providerProtocolProfileId?: string }): boolean {
  return account.providerProtocolProfileId === DEEPSEEK_OPENAI_V1_PROFILE_ID && account.clientCompatibility === 'codex_responses'
}
