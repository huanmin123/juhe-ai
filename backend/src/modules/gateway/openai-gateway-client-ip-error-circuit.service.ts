import { createHash } from 'node:crypto'

import { createAppCache } from '../../shared/cache.js'

export type GatewayPreAuthFailureReason = 'missing_bearer_token' | 'invalid_api_key'

export type GatewayClientIpErrorCircuitReason =
  | 'invalid_json'
  | 'unsupported_model'
  | 'adapter_request_validation'

export interface GatewayClientIpCircuitScope {
  systemAccountId: string
  groupId: string
  apiKeyId?: string
  clientIp?: string
}

export interface GatewayCircuitDecision {
  blocked: boolean
  reason?: string
  retryAfterSeconds?: number
  blockedUntilMs?: number
  failureCount?: number
}

export interface GatewayPreAuthCircuitInput {
  clientIp?: string
  authorization?: string
}

export interface GatewayPreAuthFailureInput extends GatewayPreAuthCircuitInput {
  reason: GatewayPreAuthFailureReason
}

export interface GatewayClientIpErrorCircuitInput extends GatewayClientIpCircuitScope {
  endpoint: string
}

export interface GatewayClientIpErrorCircuitSampleInput extends GatewayClientIpErrorCircuitInput {
  reason: GatewayClientIpErrorCircuitReason
  signature?: string
}

interface PreAuthEntry {
  key: string
  samples: number[]
  blockCount: number
  blockedUntilMs?: number
  lastReason?: GatewayPreAuthFailureReason | 'invalid_api_key_spray'
}

interface ClientIpErrorEntry {
  key: string
  samples: number[]
  signatures: Array<[string, number[]]>
  blockCount: number
  blockedUntilMs?: number
  lastReason?: GatewayClientIpErrorCircuitReason
}

const preAuthCache = createAppCache<string, PreAuthEntry>({
  name: 'gateway:client-ip-pre-auth-circuit',
  max: 20_000,
  ttlMs: 15 * 60_000,
  updateAgeOnGet: false
})

const clientIpErrorCircuitCache = createAppCache<string, ClientIpErrorEntry>({
  name: 'gateway:client-ip-error-circuit',
  max: 10_000,
  ttlMs: 15 * 60_000,
  updateAgeOnGet: false
})

const preAuthWindowMs = 60_000
const preAuthMissingThreshold = 40
const preAuthInvalidTokenThreshold = 8
const preAuthInvalidTokenSprayThreshold = 120
const preAuthInitialBlockMs = 30_000
const preAuthMaxBlockMs = 5 * 60_000

const clientIpSignatureWindowMs = 30_000
const clientIpTotalWindowMs = 60_000
const clientIpSignatureThreshold = 5
const clientIpTotalThreshold = 20
const clientIpInitialBlockMs = 30_000
const clientIpMaxBlockMs = 10 * 60_000
const maxSignaturesPerScope = 20

export function inspectGatewayPreAuthCircuit(input: GatewayPreAuthCircuitInput): GatewayCircuitDecision {
  const specificKey = preAuthSpecificKey(input)
  if (!specificKey) {
    return { blocked: false }
  }
  return entryDecision(preAuthCache.get(specificKey))
}

export function recordGatewayPreAuthFailure(input: GatewayPreAuthFailureInput): GatewayCircuitDecision {
  const specificKey = preAuthSpecificKey(input)
  if (!specificKey) {
    return { blocked: false }
  }
  const now = Date.now()
  const threshold = input.reason === 'missing_bearer_token'
    ? preAuthMissingThreshold
    : preAuthInvalidTokenThreshold
  const specificDecision = recordPreAuthEntry(specificKey, input.reason, threshold, now)
  if (specificDecision.blocked) {
    return specificDecision
  }

  if (input.reason !== 'invalid_api_key' || !input.clientIp?.trim()) {
    return specificDecision
  }
  const sprayDecision = recordPreAuthEntry(preAuthSprayKey(input.clientIp), 'invalid_api_key_spray', preAuthInvalidTokenSprayThreshold, now)
  return sprayDecision.blocked ? sprayDecision : specificDecision
}

export function inspectClientIpErrorCircuit(input: GatewayClientIpErrorCircuitInput): GatewayCircuitDecision {
  const key = clientIpErrorScopeKey(input)
  if (!key) {
    return { blocked: false }
  }
  return entryDecision(clientIpErrorCircuitCache.get(key))
}

export function recordClientIpErrorCircuitSample(input: GatewayClientIpErrorCircuitSampleInput): GatewayCircuitDecision {
  const key = clientIpErrorScopeKey(input)
  if (!key) {
    return { blocked: false }
  }
  const now = Date.now()
  const entry = clientIpErrorCircuitCache.get(key) ?? {
    key,
    samples: [],
    signatures: [],
    blockCount: 0
  }
  entry.samples = pruneSamples([...entry.samples, now], now, clientIpTotalWindowMs)
  const signature = sampleSignature(input)
  upsertSignatureSample(entry, signature, now)
  entry.lastReason = input.reason

  const signatureCount = signatureSampleCount(entry, signature, now)
  const shouldBlock = signatureCount >= clientIpSignatureThreshold || entry.samples.length >= clientIpTotalThreshold
  if (shouldBlock) {
    openBlock(entry, now, clientIpInitialBlockMs, clientIpMaxBlockMs)
  }
  clientIpErrorCircuitCache.set(key, entry, { ttlMs: clientIpMaxBlockMs + clientIpTotalWindowMs })
  return entryDecision(entry)
}

export function recordClientIpErrorCircuitSuccess(input: GatewayClientIpErrorCircuitInput): boolean {
  const key = clientIpErrorScopeKey(input)
  if (!key) {
    return false
  }
  const existed = Boolean(clientIpErrorCircuitCache.get(key))
  clientIpErrorCircuitCache.delete(key)
  return existed
}

export function clearGatewayClientIpErrorCircuitForTest(): void {
  preAuthCache.clear()
  clientIpErrorCircuitCache.clear()
}

export function getGatewayClientIpSecuritySnapshotForTest(): {
  preAuth: Array<{ key: string; failureCount: number; blocked: boolean; lastReason?: string }>
  clientIpErrors: Array<{ key: string; failureCount: number; blocked: boolean; lastReason?: string }>
} {
  return {
    preAuth: [...preAuthCache.values()].map((entry) => ({
      key: entry.key,
      failureCount: entry.samples.length,
      blocked: entryDecision(entry).blocked,
      lastReason: entry.lastReason
    })),
    clientIpErrors: [...clientIpErrorCircuitCache.values()].map((entry) => ({
      key: entry.key,
      failureCount: entry.samples.length,
      blocked: entryDecision(entry).blocked,
      lastReason: entry.lastReason
    }))
  }
}

function recordPreAuthEntry(
  key: string,
  reason: PreAuthEntry['lastReason'],
  threshold: number,
  now: number
): GatewayCircuitDecision {
  const entry = preAuthCache.get(key) ?? {
    key,
    samples: [],
    blockCount: 0
  }
  entry.samples = pruneSamples([...entry.samples, now], now, preAuthWindowMs)
  entry.lastReason = reason
  if (entry.samples.length >= threshold) {
    openBlock(entry, now, preAuthInitialBlockMs, preAuthMaxBlockMs)
  }
  preAuthCache.set(key, entry, { ttlMs: preAuthMaxBlockMs + preAuthWindowMs })
  return entryDecision(entry)
}

function openBlock(entry: { blockCount: number; blockedUntilMs?: number }, now: number, initialBlockMs: number, maxBlockMs: number): void {
  const active = typeof entry.blockedUntilMs === 'number' && entry.blockedUntilMs > now
  if (active) {
    return
  }
  const blockMs = Math.min(maxBlockMs, initialBlockMs * (2 ** Math.min(entry.blockCount, 4)))
  entry.blockCount += 1
  entry.blockedUntilMs = now + blockMs
}

function entryDecision(entry: PreAuthEntry | ClientIpErrorEntry | undefined): GatewayCircuitDecision {
  if (!entry?.blockedUntilMs) {
    return { blocked: false, failureCount: entry?.samples.length }
  }
  const now = Date.now()
  if (entry.blockedUntilMs <= now) {
    return { blocked: false, failureCount: entry.samples.length }
  }
  return {
    blocked: true,
    reason: entry.lastReason,
    retryAfterSeconds: Math.max(1, Math.ceil((entry.blockedUntilMs - now) / 1000)),
    blockedUntilMs: entry.blockedUntilMs,
    failureCount: entry.samples.length
  }
}

function preAuthSpecificKey(input: GatewayPreAuthCircuitInput): string | undefined {
  const clientIp = input.clientIp?.trim()
  if (!clientIp) {
    return undefined
  }
  const token = bearerToken(input.authorization)
  if (!token) {
    return `preauth:${clientIp}:missing`
  }
  return `preauth:${clientIp}:token:${tokenFingerprint(token)}`
}

function preAuthSprayKey(clientIp: string): string {
  return `preauth:${clientIp.trim()}:invalid-token-spray`
}

function clientIpErrorScopeKey(input: GatewayClientIpErrorCircuitInput): string | undefined {
  const clientIp = input.clientIp?.trim()
  if (!clientIp) {
    return undefined
  }
  return JSON.stringify({
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId?.trim() || 'internal',
    groupId: input.groupId,
    clientIp
  })
}

function sampleSignature(input: GatewayClientIpErrorCircuitSampleInput): string {
  return [
    normalizeSignaturePart(input.endpoint),
    input.reason,
    normalizeSignaturePart(input.signature ?? input.reason)
  ].join('|')
}

function upsertSignatureSample(entry: ClientIpErrorEntry, signature: string, now: number): void {
  const existing = entry.signatures.find((item) => item[0] === signature)
  if (existing) {
    existing[1] = pruneSamples([...existing[1], now], now, clientIpSignatureWindowMs)
  } else {
    entry.signatures.push([signature, [now]])
  }
  entry.signatures = entry.signatures
    .map(([key, samples]) => [key, pruneSamples(samples, now, clientIpSignatureWindowMs)] as [string, number[]])
    .filter(([, samples]) => samples.length > 0)
    .slice(-maxSignaturesPerScope)
}

function signatureSampleCount(entry: ClientIpErrorEntry, signature: string, now: number): number {
  const samples = entry.signatures.find((item) => item[0] === signature)?.[1] ?? []
  return samples.filter((sample) => now - sample <= clientIpSignatureWindowMs).length
}

function pruneSamples(samples: number[], now: number, windowMs: number): number[] {
  return samples.filter((sample) => now - sample <= windowMs)
}

function bearerToken(authorization?: string): string | undefined {
  if (!authorization) return undefined
  const match = authorization.match(/^Bearer\s+(.+)$/i)
  return match?.[1]?.trim()
}

function tokenFingerprint(token: string): string {
  return createHash('sha256').update(token).digest('hex').slice(0, 16)
}

function normalizeSignaturePart(value: string): string {
  return value.trim().replace(/\s+/g, ' ').toLowerCase().slice(0, 240)
}
