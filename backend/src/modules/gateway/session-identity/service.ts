import { runtimeConfig } from '../../../config/runtime.js'
import {
  createGatewayConversationKey,
  deriveGatewaySessionAffinityKeyFromConversationKey,
  validateGatewaySessionIdentityCandidate
} from './canonicalizer.js'
import { collectGatewaySessionIdentityCandidates } from './registry.js'
import { normalizedGatewaySessionRequestPath } from './request-utils.js'
import type {
  GatewaySessionAffinityKeyScope,
  GatewaySessionIdentity,
  GatewaySessionIdentityCandidate,
  GatewaySessionIdentityPhysicalSource,
  GatewaySessionIdentityRequest,
  GatewaySessionIdentityResolver,
  GatewaySessionIdentityScope,
  ResolvedGatewaySessionIdentityScope,
  ValidatedGatewaySessionIdentityCandidate
} from './types.js'

const gatewaySessionIdentityByRequest = new WeakMap<GatewaySessionIdentityRequest, GatewaySessionIdentity>()

export function resolveGatewaySessionIdentity(
  request: GatewaySessionIdentityRequest,
  scope: GatewaySessionIdentityScope,
  resolvers?: readonly GatewaySessionIdentityResolver[]
): GatewaySessionIdentity {
  const resolvedScope = resolveGatewaySessionIdentityScope(scope)
  const rawCandidates = collectGatewaySessionIdentityCandidates({
    request,
    clientProfile: scope.clientProfile,
    normalizedPath: normalizedGatewaySessionRequestPath(request)
  }, resolvers)
  const validCandidates: ValidatedGatewaySessionIdentityCandidate[] = []
  const candidates: GatewaySessionIdentityCandidate[] = []
  let hasInvalidCandidate = false

  for (const rawCandidate of rawCandidates) {
    const validation = validateGatewaySessionIdentityCandidate(rawCandidate, resolvedScope)
    if (validation.candidate) {
      validCandidates.push(validation.candidate)
      candidates.push(toPublicCandidate(validation.candidate))
      continue
    }
    hasInvalidCandidate = true
    candidates.push({
      resolverId: rawCandidate.resolverId,
      semanticKind: 'session',
      semanticNamespace: rawCandidate.semanticNamespace,
      source: rawCandidate.source,
      confidence: rawCandidate.confidence,
      priority: rawCandidate.priority,
      valid: false,
      invalidReason: validation.invalidReason
    })
  }

  if (hasInvalidCandidate) {
    return rememberGatewaySessionIdentity(request, {
      status: 'invalid',
      resolution: 'invalid',
      sources: [],
      candidates,
      conflicts: []
    })
  }
  if (validCandidates.length === 0) {
    return rememberGatewaySessionIdentity(request, {
      status: 'missing',
      resolution: 'missing',
      sources: [],
      candidates,
      conflicts: []
    })
  }

  const highestPriority = Math.max(...validCandidates.map((candidate) => candidate.priority))
  const highest = validCandidates.filter((candidate) => candidate.priority === highestPriority)
  const identityValues = new Map<string, ValidatedGatewaySessionIdentityCandidate[]>()
  for (const candidate of highest) {
    const key = JSON.stringify([candidate.semanticNamespace, candidate.rawValue])
    const matching = identityValues.get(key) ?? []
    matching.push(candidate)
    identityValues.set(key, matching)
  }
  if (identityValues.size !== 1) {
    return rememberGatewaySessionIdentity(request, {
      status: 'conflict',
      resolution: 'conflict',
      sources: [],
      candidates,
      conflicts: [{
        kind: 'session',
        priority: highestPriority,
        sources: uniqueSources(highest.map((candidate) => candidate.source)),
        evidenceKeys: [...new Set(highest.map((candidate) => candidate.evidenceKey))]
      }]
    })
  }

  const selected = highest[0]
  const sameIdentity = validCandidates.filter((candidate) => (
    candidate.semanticNamespace === selected.semanticNamespace
    && candidate.rawValue === selected.rawValue
  ))
  const sources = uniqueSources(sameIdentity.map((candidate) => candidate.source))
  return rememberGatewaySessionIdentity(request, {
    status: 'resolved',
    resolution: 'official',
    sessionId: selected.rawValue,
    conversationKey: createGatewayConversationKey(resolvedScope, selected.semanticNamespace, selected.rawValue),
    semanticNamespace: selected.semanticNamespace,
    source: selected.source,
    sources,
    confidence: selected.confidence,
    candidates,
    conflicts: []
  })
}

export function getGatewaySessionIdentity(
  request: GatewaySessionIdentityRequest
): GatewaySessionIdentity | undefined {
  return gatewaySessionIdentityByRequest.get(request)
}

export function deriveGatewaySessionAffinityKey(
  identity: Pick<GatewaySessionIdentity, 'conversationKey'>,
  scope: GatewaySessionAffinityKeyScope
): string | undefined {
  return identity.conversationKey
    ? deriveGatewaySessionAffinityKeyFromConversationKey(identity.conversationKey, {
        ...scope,
        hmacSecret: scope.hmacSecret ?? runtimeConfig.secret
      })
    : undefined
}

function resolveGatewaySessionIdentityScope(
  scope: GatewaySessionIdentityScope
): ResolvedGatewaySessionIdentityScope {
  return { ...scope, hmacSecret: scope.hmacSecret ?? runtimeConfig.secret }
}

function rememberGatewaySessionIdentity(
  request: GatewaySessionIdentityRequest,
  identity: GatewaySessionIdentity
): GatewaySessionIdentity {
  gatewaySessionIdentityByRequest.set(request, identity)
  return identity
}

function uniqueSources(sources: GatewaySessionIdentityPhysicalSource[]): GatewaySessionIdentityPhysicalSource[] {
  const seen = new Set<string>()
  return sources.filter((source) => {
    const key = `${source.location}:${source.path}`
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

function toPublicCandidate(candidate: ValidatedGatewaySessionIdentityCandidate): GatewaySessionIdentityCandidate {
  return {
    resolverId: candidate.resolverId,
    semanticKind: 'session',
    semanticNamespace: candidate.semanticNamespace,
    source: candidate.source,
    confidence: candidate.confidence,
    priority: candidate.priority,
    valid: true,
    evidenceKey: candidate.evidenceKey
  }
}
