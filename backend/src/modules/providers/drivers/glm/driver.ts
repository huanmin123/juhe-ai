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
import type { ClientCompatibilityCapability } from '../../../../domain/types.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import { isGatewayProtocolNativeRequest } from '../../../gateway/protocols/registry.js'
import { applyOpenAIClientCompatibilityHeaders, buildOpenAIClientCompatibilityBody } from '../../../gateway/protocols/openai-v1/api-key-client-compatibility.js'
import {
  buildOpenAIModelMappedJsonBody,
  resolveOpenAIAccountModelMapping,
  resolveOpenAIRequestModelMapping
} from '../../../gateway/protocols/openai-v1/model-mapping.js'
import { splitPathAndQuery } from '../../../gateway/protocols/openai-v1/route-helpers.js'
import {
  buildUpstreamHeaders,
  buildUpstreamRequestBody,
  isEffectiveOpenAIStreamRequest
} from '../../../gateway/upstream/request.js'
import {
  buildCodexResponsesChatBridgeBody,
  codexResponsesChatBridgeLocalValidationUpstreamUrl,
  codexResponsesChatBridgeRequiredEndpointMode,
  codexResponsesChatBridgeUpstreamPath,
  isCodexResponsesChatBridgeCandidateRequest,
  isCodexResponsesChatBridgeRequest,
  isCodexResponsesChatBridgeUnsupportedCompactCandidateRequest,
  isCodexResponsesChatBridgeUnsupportedCompactRequest,
  prepareCodexResponsesChatBridgeHeaders,
  rejectUnsupportedCodexResponsesChatBridgeCompactRequest,
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
    return buildGlmOpenAIChatUpstreamUrls(account, req)
  },
  async buildUpstreamRequestParts(req, account, _identity, signal, context) {
    const modelMapping = resolveOpenAIRequestModelMapping(req, account)
    if (isGlmCodexResponsesBridgeUnsupportedCompactRequest(req, account, context?.requestClientCompatibility)) {
      rejectUnsupportedCodexResponsesChatBridgeCompactRequest()
    }
    if (isGlmCodexResponsesBridgeRequest(req, account, context?.requestClientCompatibility)) {
      const headers = buildUpstreamHeaders(req.headers, account)
      prepareCodexResponsesChatBridgeHeaders(headers)
      return {
        headers,
        body: await buildCodexResponsesChatBridgeBody(req, {
          defaultModel: GLM_CODEX_BRIDGE_DEFAULT_MODEL,
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
      defaultModel: GLM_CODEX_BRIDGE_DEFAULT_MODEL,
      enabled: isGlmCodexResponsesBridgeEnabled(account),
      idPrefix: 'glm_bridge',
      model: modelMapping?.upstreamModel,
      previousResponseId: context?.codexResponsesChatBridgePreviousResponseId,
      onCompleted: context?.codexResponsesChatBridgeCompletionHandler,
      requestClientCompatibility: context?.requestClientCompatibility
    })
  },
  endpointModeForRequest: openAIEndpointModeForGatewayRequest,
  accountSupportsRequest(req, account, context) {
    if (isGlmCodexResponsesBridgeUnsupportedCompactRequest(req, account, context?.requestClientCompatibility)) {
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
    if (isGlmCodexResponsesBridgeRequest(req, account, context?.requestClientCompatibility)) {
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
  if (isGlmCodexResponsesBridgeCandidateRequest(req, account)) {
    const bridgePath = codexResponsesChatBridgeUpstreamPath(req)
    return bridgePath ? [`${normalizeGlmBaseUrl(account.baseUrl)}${bridgePath}`] : []
  }
  if (isGlmCodexResponsesBridgeUnsupportedCompactCandidateRequest(req, account)) {
    return [codexResponsesChatBridgeLocalValidationUpstreamUrl()]
  }
  const { path, query } = splitPathAndQuery(req.originalUrl)
  const requestPath = path.startsWith('/') ? path : `/${path}`
  const normalizedPath = requestPath.replace(/^\/v1(?=\/|$)/, '') || '/'
  if (normalizedPath !== '/chat/completions') {
    return []
  }
  return [`${normalizeGlmBaseUrl(account.baseUrl)}${normalizedPath}${query}`]
}

function normalizeGlmBaseUrl(baseUrl: string): string {
  return baseUrl.trim().replace(/\/+$/, '')
}

function isGlmCodingProfile(account: { providerProtocolProfileId?: string }): boolean {
  return account.providerProtocolProfileId === GLM_CODING_OPENAI_V1_PROFILE_ID
}

function isGlmCodexResponsesBridgeRequest(
  req: Request,
  account: { clientCompatibility?: string; providerProtocolProfileId?: string },
  requestClientCompatibility?: ClientCompatibilityCapability
): boolean {
  return isCodexResponsesChatBridgeRequest(req, {
    enabled: isGlmCodexResponsesBridgeEnabled(account),
    requestClientCompatibility
  })
}

function isGlmCodexResponsesBridgeCandidateRequest(
  req: Request,
  account: { clientCompatibility?: string; providerProtocolProfileId?: string }
): boolean {
  return isCodexResponsesChatBridgeCandidateRequest(req, isGlmCodexResponsesBridgeEnabled(account))
}

function isGlmCodexResponsesBridgeUnsupportedCompactRequest(
  req: Request,
  account: { clientCompatibility?: string; providerProtocolProfileId?: string },
  requestClientCompatibility?: ClientCompatibilityCapability
): boolean {
  return isCodexResponsesChatBridgeUnsupportedCompactRequest(req, {
    enabled: isGlmCodexResponsesBridgeEnabled(account),
    requestClientCompatibility
  })
}

function isGlmCodexResponsesBridgeUnsupportedCompactCandidateRequest(
  req: Request,
  account: { clientCompatibility?: string; providerProtocolProfileId?: string }
): boolean {
  return isCodexResponsesChatBridgeUnsupportedCompactCandidateRequest(req, isGlmCodexResponsesBridgeEnabled(account))
}

function isGlmCodexResponsesBridgeEnabled(account: { clientCompatibility?: string; providerProtocolProfileId?: string }): boolean {
  return isGlmCodingProfile(account) && account.clientCompatibility === 'codex_responses'
}
