import type { Request } from 'express'

import {
  accountSupportsOpenAIEndpointMode,
  openAIEndpointModeForRequestShape
} from '../../../../domain/openai-endpoint-modes.js'
import {
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  isGptVendorCode,
  isOpenAIProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import { buildOpenAIOAuthCodexRequestParts } from '../../../gateway/adapters/gpt-codex/oauth-adapter.js'
import { isAnthropicNativeRequest } from '../../../gateway/protocols/anthropic-v1/route-helpers.js'
import { applyOpenAIClientCompatibilityHeaders, buildOpenAIClientCompatibilityBody } from '../../../gateway/protocols/openai-v1/api-key-client-compatibility.js'
import { buildOpenAIModelMappedJsonBody, resolveOpenAIRequestModelMapping } from '../../../gateway/protocols/openai-v1/model-mapping.js'
import {
  buildOpenAICodexUpstreamUrls,
  buildUpstreamUrls
} from '../../../gateway/protocols/openai-v1/route-helpers.js'
import {
  buildUpstreamHeaders,
  buildUpstreamRequestBody,
  isEffectiveOpenAIStreamRequest
} from '../../../gateway/upstream/request.js'
import type { ProviderDriver, ProviderDriverAccount } from '../_shared/types.js'

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
  profileIds: [GPT_OPENAI_V1_PROFILE_ID],
  supportsProfile(profile) {
    return isGptVendorCode(profile?.providerCode)
      && isOpenAIProtocolProfile(profile)
  },
  buildUpstreamUrls(account: DispatchAccountSecret, req: Request): string[] {
    if (isAnthropicNativeRequest(req)) {
      return []
    }
    if (account.type === 'oauth') {
      return buildOpenAICodexUpstreamUrls(req)
    }
    return buildUpstreamUrls(account.baseUrl, req.originalUrl)
  },
  async buildUpstreamRequestParts(req, account, identity, signal) {
    const modelMapping = resolveOpenAIRequestModelMapping(req, account)
    if (account.type === 'oauth') {
      return await buildOpenAIOAuthCodexRequestParts(req, req.headers, account, identity, signal, {
        modelOverride: modelMapping?.upstreamModel
      })
    }
    const compatibilityBody = await buildOpenAIClientCompatibilityBody(req, account, signal, {
      modelOverride: modelMapping?.upstreamModel
    })
    const headers = buildUpstreamHeaders(req.headers, account)
    applyOpenAIClientCompatibilityHeaders(req, account, headers)
    return {
      headers,
      body: compatibilityBody ?? (modelMapping ? await buildOpenAIModelMappedJsonBody(req, modelMapping.upstreamModel, signal) : buildUpstreamRequestBody(req))
    }
  },
  endpointModeForRequest: openAIEndpointModeForGatewayRequest,
  accountSupportsRequest(req, account) {
    const mode = openAIEndpointModeForGatewayRequest(req, account)
    if (!mode) return true
    return accountSupportsOpenAIEndpointMode({
      mode,
      supportedEndpointModes: account.supportedEndpointModes,
      credentials: account.credentials,
      providerCode: account.providerCode,
      accountType: account.type,
      clientCompatibility: account.clientCompatibility
    })
  }
}
