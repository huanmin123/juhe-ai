import type { Request } from 'express'

import {
  accountSupportsOpenAIEndpointMode,
  openAIEndpointModeForRequestShape
} from '../../../../domain/openai-endpoint-modes.js'
import {
  XAI_OPENAI_V1_PROFILE_ID,
  XAI_PROVIDER_CODE,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  isOpenAIProtocolProfile,
  isXaiProviderCode
} from '../../../../domain/provider-protocol.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import {
  applyOpenAIClientCompatibilityHeaders,
  buildOpenAIClientCompatibilityBody
} from '../../../gateway/protocols/openai-v1/api-key-client-compatibility.js'
import {
  buildOpenAIModelMappedJsonBody,
  resolveOpenAIAccountModelMapping,
  resolveOpenAIRequestModelMapping
} from '../../../gateway/protocols/openai-v1/model-mapping.js'
import { buildUpstreamUrls, splitPathAndQuery } from '../../../gateway/protocols/openai-v1/route-helpers.js'
import {
  buildUpstreamHeaders,
  buildUpstreamRequestBody,
  isEffectiveOpenAIStreamRequest
} from '../../../gateway/upstream/request.js'
import type { ProviderDriver, ProviderDriverAccount, ProviderGatewayRequestContext } from '../_shared/types.js'

function xaiEndpointModeForGatewayRequest(req: Request, account: ProviderDriverAccount, _context?: ProviderGatewayRequestContext) {
  if (!isSupportedXaiPath(req)) return undefined
  return openAIEndpointModeForRequestShape({
    endpoint: req.path || req.originalUrl.split('?', 1)[0],
    stream: isEffectiveOpenAIStreamRequest(req, account)
  })
}

export const xaiProviderDriver: ProviderDriver = {
  id: 'xai',
  providerCode: XAI_PROVIDER_CODE,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  usageSemantic: 'openai',
  profileIds: [XAI_OPENAI_V1_PROFILE_ID],
  supportsProfile(profile) {
    const profileId = profile?.providerProtocolProfileId ?? profile?.id
    return isXaiProviderCode(profile?.providerCode)
      && isOpenAIProtocolProfile(profile)
      && profileId === XAI_OPENAI_V1_PROFILE_ID
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
    if (account.type !== 'api_key' || !isSupportedXaiPath(req)) return []
    return buildUpstreamUrls(account.baseUrl, req.originalUrl)
  },
  async buildUpstreamRequestParts(req, account, _identity, signal, context) {
    if (account.type !== 'api_key') {
      throw new Error('xAI 官方 API 档案只支持 API Key 账户')
    }
    const modelMapping = resolveOpenAIRequestModelMapping(req, account)
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
      body: compatibilityBody
        ?? (modelMapping ? await buildOpenAIModelMappedJsonBody(req, modelMapping.upstreamModel, signal) : buildUpstreamRequestBody(req))
    }
  },
  endpointModeForRequest: xaiEndpointModeForGatewayRequest,
  accountSupportsRequest(req, account, context) {
    if (account.type !== 'api_key') return false
    const mode = xaiEndpointModeForGatewayRequest(req, account, context)
    if (!mode) return false
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

function isSupportedXaiPath(req: Request): boolean {
  if (req.method.toUpperCase() !== 'POST') return false
  const { path } = splitPathAndQuery(req.originalUrl || req.path || '')
  const requestPath = path.startsWith('/') ? path : `/${path}`
  const normalizedPath = requestPath.replace(/^\/v1(?=\/|$)/, '') || '/'
  return normalizedPath === '/chat/completions' || normalizedPath === '/responses'
}
