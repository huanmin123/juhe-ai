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
import { prepareXaiAccountBeforeDispatch } from './oauth-dispatch-preparation.js'

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
  async prepareAccountBeforeDispatch(account, context) {
    return await prepareXaiAccountBeforeDispatch(account, context.signal)
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
    if (!isSupportedXaiAccountType(account.type) || !isSupportedXaiPath(req)) return []
    if (account.type === 'oauth' && !isXaiOAuthPath(req)) return []
    return buildUpstreamUrls(account.baseUrl, req.originalUrl)
  },
  async buildUpstreamRequestParts(req, account, _identity, signal, context) {
    if (!isSupportedXaiAccountType(account.type)) {
      throw new Error('xAI 官方 API 档案只支持 API Key 或 OAuth 账户')
    }
    if (account.type === 'oauth' && !isXaiOAuthPath(req)) {
      throw new Error('Grok OAuth 账户只支持 Responses 接口')
    }
    const modelMapping = resolveOpenAIRequestModelMapping(req, account)
    const compatibilityBody = await buildOpenAIClientCompatibilityBody(req, signal, {
      modelOverride: modelMapping?.upstreamModel,
      requestClientCompatibility: context?.requestClientCompatibility
    })
    const headers = buildUpstreamHeaders(req.headers, account)
    if (account.type === 'oauth') {
      headers.set('user-agent', 'sub2api-grok/1.0')
      headers.set('x-grok-client-version', '0.2.93')
      headers.set('accept', 'application/json, text/event-stream')
      headers.set('content-type', 'application/json')
    }
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
    if (!isSupportedXaiAccountType(account.type)) return false
    if (account.type === 'oauth' && !isXaiOAuthPath(req)) return false
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

function isSupportedXaiAccountType(type: string | undefined): boolean {
  return type === 'api_key' || type === 'oauth'
}

function isXaiOAuthPath(req: Request): boolean {
  const { path } = splitPathAndQuery(req.originalUrl || req.path || '')
  const requestPath = path.startsWith('/') ? path : `/${path}`
  const normalizedPath = requestPath.replace(/^\/v1(?=\/|$)/, '') || '/'
  return req.method.toUpperCase() === 'POST' && normalizedPath === '/responses'
}

function isSupportedXaiPath(req: Request): boolean {
  if (req.method.toUpperCase() !== 'POST') return false
  const { path } = splitPathAndQuery(req.originalUrl || req.path || '')
  const requestPath = path.startsWith('/') ? path : `/${path}`
  const normalizedPath = requestPath.replace(/^\/v1(?=\/|$)/, '') || '/'
  return normalizedPath === '/chat/completions' || normalizedPath === '/responses'
}
