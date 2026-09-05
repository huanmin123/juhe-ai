import { createHmac } from 'node:crypto'

import type {
  GatewaySessionAffinityKeyScope,
  GatewaySessionIdentityInvalidReason,
  GatewaySessionIdentityRawCandidate,
  ResolvedGatewaySessionIdentityScope,
  ValidatedGatewaySessionIdentityCandidate
} from './types.js'

export const gatewaySessionIdentityMaxBytes = 512

export function validateGatewaySessionIdentityCandidate(
  candidate: GatewaySessionIdentityRawCandidate,
  scope: ResolvedGatewaySessionIdentityScope
): { candidate?: ValidatedGatewaySessionIdentityCandidate; invalidReason?: GatewaySessionIdentityInvalidReason } {
  if (candidate.invalidShape || typeof candidate.rawValue !== 'string') {
    return { invalidReason: 'invalid_shape' }
  }
  if (/[\u0000-\u001f\u007f-\u009f]/u.test(candidate.rawValue)) {
    return { invalidReason: 'control_character' }
  }
  const rawValue = candidate.rawValue.trim()
  if (!rawValue) {
    return { invalidReason: 'empty' }
  }
  if (Buffer.byteLength(rawValue, 'utf8') > gatewaySessionIdentityMaxBytes) {
    return { invalidReason: 'too_long' }
  }
  return {
    candidate: {
      ...candidate,
      rawValue,
      evidenceKey: canonicalizeGatewayIdentityValue(scope, 'evidence', candidate.semanticNamespace, rawValue, 'ev_v1_')
    }
  }
}

export function createGatewayConversationKey(
  scope: ResolvedGatewaySessionIdentityScope,
  semanticNamespace: string,
  rawSessionId: string
): string {
  return canonicalizeGatewayIdentityValue(scope, 'conversation', semanticNamespace, rawSessionId, 'conv_v1_')
}

export function deriveGatewaySessionAffinityKeyFromConversationKey(
  conversationKey: string,
  scope: GatewaySessionAffinityKeyScope & { hmacSecret: string }
): string {
  return versionedHmac(scope.hmacSecret, 'affinity:v1', [
    scope.systemAccountId,
    scope.apiKeyId ?? 'internal',
    conversationKey,
    scope.routeStrategyId ?? 'default',
    scope.groupId,
    scope.providerProtocolProfileId ?? 'default'
  ], 'aff_v1_')
}

function canonicalizeGatewayIdentityValue(
  scope: ResolvedGatewaySessionIdentityScope,
  domain: string,
  semanticNamespace: string,
  rawValue: string,
  prefix: string
): string {
  return versionedHmac(scope.hmacSecret, `${domain}:v1`, [
    scope.systemAccountId,
    scope.apiKeyId ?? 'internal',
    semanticNamespace,
    rawValue
  ], prefix)
}

function versionedHmac(secret: string, domain: string, parts: string[], prefix: string): string {
  if (!secret.trim()) {
    throw new Error('Gateway session identity HMAC secret must not be empty')
  }
  const payload = JSON.stringify([domain, ...parts])
  return `${prefix}${createHmac('sha256', secret).update(payload).digest('base64url')}`
}
