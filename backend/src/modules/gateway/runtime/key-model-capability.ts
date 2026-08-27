import type { Request } from 'express'
import type { GatewayRequestEndpointFamily } from '../../../domain/types.js'

import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import {
  gatewayRequestEndpointFamily,
  resolveOpenAIAccountModelMapping
} from '../protocols/openai-v1/model-mapping.js'
import { requestModel, requestStream } from '../request/metadata.js'
import { type CapabilityKey } from './key-model-runtime.js'

export interface GatewayKeyModelCapability {
  capability: CapabilityKey
  isMainProbe: boolean
}

export function resolveGatewayKeyModelCapability(req: Request, account: UpstreamAccount): GatewayKeyModelCapability | undefined {
  const model = text(requestModel(req))
  const family = gatewayRequestEndpointFamily(req)
  const revision = account.dispatchRevision
  const keyFingerprint = text(account.selectedApiKeyFingerprint)
  if (!model || !family || !keyFingerprint || !Number.isSafeInteger(revision) || (revision ?? 0) < 1) return undefined
  const capability = capabilityForRoute(account, model, family, requestStream(req))
  if (!capability) return undefined
  return { capability, isMainProbe: routeMatchesMainProbe(account, model, family, requestStream(req), capability) }
}

function routeMatchesMainProbe(
  account: UpstreamAccount,
  requestedModel: string,
  family: GatewayRequestEndpointFamily,
  stream: boolean,
  capability: CapabilityKey
): boolean {
  if (requestedModel !== text(account.healthCheckModel)) return false
  const main = sourceMode(account.healthCheckEndpointMode)
  if (!main || main.family !== family || main.stream !== stream) return false
  const expected = capabilityForRoute(account, requestedModel, family, stream)
  return expected !== undefined
    && expected.finalUpstreamModel === capability.finalUpstreamModel
    && expected.upstreamEndpointMode === capability.upstreamEndpointMode
}

function capabilityForRoute(
  account: UpstreamAccount,
  clientModel: string,
  clientEndpointFamily: GatewayRequestEndpointFamily,
  stream: boolean
): CapabilityKey | undefined {
  const revision = account.dispatchRevision
  const keyFingerprint = text(account.selectedApiKeyFingerprint)
  if (!keyFingerprint || !Number.isSafeInteger(revision) || (revision ?? 0) < 1) return undefined
  const mapping = resolveOpenAIAccountModelMapping(account, clientModel, clientEndpointFamily)
  const upstreamFamily = mapping?.upstreamEndpointFamily ?? clientEndpointFamily
  const upstreamEndpointMode = endpointMode(upstreamFamily, stream)
  if (!upstreamEndpointMode) return undefined
  return {
    credentialSourceAccountId: text(account.credentialSourceAccountId) || account.id,
    keyFingerprint,
    clientModel,
    clientEndpointFamily,
    finalUpstreamModel: mapping?.upstreamModel ?? clientModel,
    upstreamEndpointMode,
    dispatchRevision: revision!
  }
}

function sourceMode(mode: string | undefined): { family: GatewayRequestEndpointFamily; stream: boolean } | undefined {
  switch (mode) {
    case 'chat_json': return { family: 'chat_completions', stream: false }
    case 'chat_sse': return { family: 'chat_completions', stream: true }
    case 'responses_json': return { family: 'responses', stream: false }
    case 'responses_sse': return { family: 'responses', stream: true }
    case 'messages_json': return { family: 'messages', stream: false }
    case 'messages_sse': return { family: 'messages', stream: true }
    case 'generate_content_json': return { family: 'generate_content', stream: false }
    case 'generate_content_sse': return { family: 'stream_generate_content', stream: true }
    case 'interactions_json': return { family: 'interactions', stream: false }
    case 'interactions_sse': return { family: 'interactions', stream: true }
    default: return undefined
  }
}

function endpointMode(family: string, stream: boolean): string | undefined {
  switch (family) {
    case 'chat_completions': return stream ? 'chat_sse' : 'chat_json'
    case 'responses': return stream ? 'responses_sse' : 'responses_json'
    case 'messages': return stream ? 'messages_sse' : 'messages_json'
    case 'generate_content': return stream ? 'generate_content_sse' : 'generate_content_json'
    case 'stream_generate_content': return 'generate_content_sse'
    case 'interactions': return stream ? 'interactions_sse' : 'interactions_json'
    default: return undefined
  }
}

function text(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}
