import { createHmac } from 'node:crypto'

import type { Request } from 'express'

import { runtimeConfig } from '../../../config/runtime.js'
import { geminiInteractionResourceIdFromRequest } from '../protocols/gemini-v1beta/interaction-affinity.service.js'
import {
  resolveGatewaySessionIdentity,
  type GatewaySessionIdentity
} from '../session-identity/index.js'

export type GatewayClientSourceKind = 'official_session' | 'protocol_resource' | 'ip_api_key_fallback'
export type GatewayClientSourceStatus = 'resolved' | 'missing' | 'invalid' | 'conflict'

export interface GatewayClientSourceIdentity {
  status: GatewayClientSourceStatus
  sourceKey?: string
  // Only stable protocol evidence may participate in session affinity. The
  // IP/API-Key fallback protects short-lived scheduling state but must never
  // make unrelated requests sticky to an account.
  affinityKey?: string
  kind?: GatewayClientSourceKind
  semanticNamespace?: string
  // Kept request-local so preflight and source handling share one resolver
  // result. Only the HMAC source key may enter runtime state.
  sessionIdentity?: GatewaySessionIdentity
}

export interface GatewayClientSourceIdentityInput {
  clientProfile: string
  clientProfileSource?: string
  downstreamProtocol: string
  systemAccountId: string
  apiKeyId?: string
  clientIp?: string
}

/**
 * Resolves the one internal source identifier consumed by source avoidance
 * and availability-probe fencing. Its session result is also reused by the
 * separate session-affinity path. Individual profiles only contribute their
 * official, protocol-specific evidence.
 */
export function resolveGatewayClientSourceIdentity(
  req: Request,
  input: GatewayClientSourceIdentityInput
): GatewayClientSourceIdentity {
  const systemAccountId = requiredPart(input.systemAccountId)
  const apiKeyId = requiredPart(input.apiKeyId)
  if (!systemAccountId || !apiKeyId) return { status: 'missing' }

  let sessionIdentity: GatewaySessionIdentity | undefined
  if (profileMayUseOfficialSession(input)) {
    sessionIdentity = resolveGatewaySessionIdentity(req, {
      clientProfile: input.clientProfile,
      systemAccountId,
      apiKeyId
    })
    if (sessionIdentity.status === 'resolved' && sessionIdentity.conversationKey && sessionIdentity.semanticNamespace) {
      return {
        ...resolvedSource({
          kind: 'official_session',
          systemAccountId,
          apiKeyId,
          semanticNamespace: sessionIdentity.semanticNamespace,
          stableValue: sessionIdentity.conversationKey,
          affinityKey: sessionIdentity.conversationKey
        }),
        sessionIdentity
      }
    }
    if (sessionIdentity.status === 'invalid' || sessionIdentity.status === 'conflict') {
      return { status: sessionIdentity.status, sessionIdentity }
    }
  }

  // Gemini Interactions has no session header. A returned interaction resource
  // is nevertheless a stable protocol identity for follow-up/cancel requests.
  const interactionId = geminiInteractionResourceIdFromRequest(req)
  if (interactionId && input.downstreamProtocol.startsWith('gemini_interactions')) {
    return {
      ...resolvedSource({
        kind: 'protocol_resource',
        systemAccountId,
        apiKeyId,
        semanticNamespace: 'google.gemini.interaction',
        stableValue: interactionId,
        affinityKey: interactionId
      }),
      ...(sessionIdentity ? { sessionIdentity } : {})
    }
  }

  const clientIp = requiredPart(input.clientIp)
  if (!clientIp) {
    return {
      status: 'missing',
      ...(sessionIdentity ? { sessionIdentity } : {})
    }
  }
  return {
    ...resolvedSource({
      kind: 'ip_api_key_fallback',
      systemAccountId,
      apiKeyId,
      semanticNamespace: 'gateway.ip_api_key',
      stableValue: clientIp
    }),
    ...(sessionIdentity ? { sessionIdentity } : {})
  }
}

/**
 * Narrows a source identity to a dispatch surface without exposing raw IDs.
 * A turn, interaction or other profile-specific child identifier can be added
 * by its owner after this common scope has been derived.
 */
export function deriveGatewayClientSourceStateKey(
  source: Pick<GatewayClientSourceIdentity, 'sourceKey'>,
  input: { clientProfile: string; endpoint: string; downstreamProtocol: string }
): string | undefined {
  if (!source.sourceKey) return undefined
  const endpoint = requiredPart(input.endpoint)
  if (!endpoint) return undefined
  return hmac('state:v1', [source.sourceKey, input.clientProfile, endpoint, input.downstreamProtocol])
}

export function deriveGatewayClientSourceChildStateKey(
  sourceStateKey: string | undefined,
  childKind: string,
  childId: string
): string | undefined {
  const normalizedStateKey = requiredPart(sourceStateKey)
  const normalizedChildId = requiredPart(childId)
  if (!normalizedStateKey || !normalizedChildId) return undefined
  return hmac('child:v1', [normalizedStateKey, childKind, normalizedChildId])
}

function resolvedSource(input: {
  kind: GatewayClientSourceKind
  systemAccountId: string
  apiKeyId: string
  semanticNamespace: string
  stableValue: string
  affinityKey?: string
}): GatewayClientSourceIdentity {
  return {
    status: 'resolved',
    kind: input.kind,
    semanticNamespace: input.semanticNamespace,
    ...(input.affinityKey ? { affinityKey: input.affinityKey } : {}),
    sourceKey: hmac('source:v1', [
      input.systemAccountId,
      input.apiKeyId,
      input.kind,
      input.semanticNamespace,
      input.stableValue
    ])
  }
}

function profileMayUseOfficialSession(input: GatewayClientSourceIdentityInput): boolean {
  if (input.clientProfile === 'codex') {
    return input.clientProfileSource === 'codex_turn_metadata'
  }
  if (input.clientProfile === 'claude_code') {
    return input.clientProfileSource === 'claude_code_request_signature'
  }
  return false
}

function requiredPart(value: string | undefined): string | undefined {
  return value?.trim() || undefined
}

function hmac(domain: string, parts: string[]): string {
  const secret = runtimeConfig.secret.trim()
  if (!secret) throw new Error('Gateway client source identity requires a configured HMAC secret')
  return `src_v1_${createHmac('sha256', secret).update(JSON.stringify([domain, ...parts])).digest('base64url')}`
}
