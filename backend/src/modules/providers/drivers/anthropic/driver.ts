import type { Request } from 'express'

import { accountSupportsClientCompatibility } from '../../../../domain/account-client-compatibility.js'
import {
  accountSupportsAnthropicEndpointMode,
  anthropicEndpointModeForRequestShape
} from '../../../../domain/anthropic-endpoint-modes.js'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_PROVIDER_CODE,
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION,
  DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GLM_CODING_ANTHROPIC_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  isAnthropicProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import { buildAnthropicUpstreamUrlsForAccount } from '../../../gateway/protocols/anthropic-v1/route-helpers.js'
import { buildOpenAIModelMappedJsonBody, resolveOpenAIAccountModelMapping } from '../../../gateway/protocols/openai-v1/model-mapping.js'
import { requestModel, requestStream } from '../../../gateway/request/metadata.js'
import {
  buildUpstreamRequestBody,
  copySafeUpstreamRequestHeaders,
  isEffectiveOpenAIStreamRequest
} from '../../../gateway/upstream/request.js'
import type { ProviderDriver, ProviderDriverAccount } from '../_shared/types.js'

const defaultAnthropicVersion = '2023-06-01'

function anthropicEndpointModeForGatewayRequest(req: Request, account: ProviderDriverAccount) {
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
      modelMappingSource: modelMapping ? 'account' : undefined
    }
  },
  buildUpstreamUrls(account: DispatchAccountSecret, req: Request): string[] {
    return buildAnthropicUpstreamUrlsForAccount(account, req)
  },
  async buildUpstreamRequestParts(req, account, _identity, signal) {
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
    const modelMapping = resolveOpenAIAccountModelMapping(account, requestModel(req), undefined)
    return {
      headers,
      body: modelMapping
        ? await buildOpenAIModelMappedJsonBody(req, modelMapping.upstreamModel, signal)
        : buildUpstreamRequestBody(req)
    }
  },
  endpointModeForRequest: anthropicEndpointModeForGatewayRequest,
  accountSupportsRequest(req, account, context) {
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
