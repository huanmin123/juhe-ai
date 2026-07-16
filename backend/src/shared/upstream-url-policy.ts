import { lookup } from 'node:dns/promises'
import type { RequestOptions } from 'node:http'
import { isIP } from 'node:net'

import { runtimeConfig, type RuntimeConfig } from '../config/runtime.js'
import {
  openAICompatibleBaseUrlPolicy,
  upstreamRequestUrlPolicy,
  openAICompatibleBaseUrlValidator,
  upstreamRequestUrlValidator,
  UpstreamBaseUrlValidator,
  type UpstreamBaseUrlValidationPolicy,
  UpstreamBaseUrlValidationError
} from './upstream-base-url-validator.js'

type UpstreamUrlSecurityConfig = RuntimeConfig['upstreamUrlSecurity']
type UpstreamLookup = NonNullable<RequestOptions['lookup']>

export class UnsafeUpstreamUrlError extends Error {
  constructor(message = '上游 Base URL 不能指向本机、内网、链路本地或保留地址') {
    super(message)
  }
}

interface ResolvedAddress {
  address: string
  family: 4 | 6
}

const blockedIpv4Ranges = [
  ['0.0.0.0', 8],
  ['10.0.0.0', 8],
  ['100.64.0.0', 10],
  ['127.0.0.0', 8],
  ['169.254.0.0', 16],
  ['172.16.0.0', 12],
  ['192.0.0.0', 24],
  ['192.0.2.0', 24],
  ['192.88.99.0', 24],
  ['192.168.0.0', 16],
  ['198.18.0.0', 15],
  ['198.51.100.0', 24],
  ['203.0.113.0', 24],
  ['224.0.0.0', 4],
  ['240.0.0.0', 4]
].map(([address, prefixLength]) => ({
  parts: parseIpv4Parts(String(address)) as number[],
  prefixLength: Number(prefixLength)
}))

const blockedIpv6Ranges = [
  ['::', 128],
  ['::1', 128],
  ['::', 96],
  ['::ffff:0:0', 96],
  ['64:ff9b::', 96],
  ['64:ff9b:1::', 48],
  ['100::', 64],
  ['2001::', 23],
  ['2001:db8::', 32],
  ['2002::', 16],
  ['fc00::', 7],
  ['fe80::', 10],
  ['ff00::', 8]
].map(([address, prefixLength]) => ({
  groups: parseIpv6Groups(String(address)) as number[],
  prefixLength: Number(prefixLength)
}))

export function assertSafeUpstreamBaseUrl(
  value: string,
  config: UpstreamUrlSecurityConfig = runtimeConfig.upstreamUrlSecurity,
  policy: UpstreamBaseUrlValidationPolicy = openAICompatibleBaseUrlPolicy
): void {
  const url = parseUpstreamUrl(value, config, policy)
  assertSafeUpstreamUrl(url, config)
}

export async function prepareSafeUpstreamRequestUrl(
  value: string,
  config: UpstreamUrlSecurityConfig = runtimeConfig.upstreamUrlSecurity
): Promise<{ url: URL; lookup?: UpstreamLookup }> {
  const url = parseUpstreamUrl(value, config, upstreamRequestUrlPolicy)
  assertSafeUpstreamUrl(url, config)
  const hostname = normalizeHostToken(url.hostname)
  const allowlistedOrigin = isAllowedPrivateOrigin(url, config)
  if (config.allowPrivateBaseUrls || isIP(hostname)) {
    return { url }
  }
  const addresses = await lookup(url.hostname, { all: true, verbatim: true }) as ResolvedAddress[]
  for (const address of addresses) {
    if (!allowlistedOrigin && isPrivateOrReservedIp(address.address)) {
      throw new UnsafeUpstreamUrlError()
    }
  }
  return { url, lookup: fixedLookup(addresses) }
}

function parseUpstreamUrl(
  value: string,
  config: UpstreamUrlSecurityConfig,
  policy: UpstreamBaseUrlValidationPolicy
): URL {
  try {
    return validatorForPolicy(policy).parse(value, {
      isPrivateHostAllowed: canUseHttpUpstreamUrlForConfiguredPrivateHost(value, config)
    })
  } catch (error) {
    throw new UnsafeUpstreamUrlError(error instanceof UpstreamBaseUrlValidationError ? error.message : '上游 Base URL 格式无效')
  }
}

function validatorForPolicy(policy: UpstreamBaseUrlValidationPolicy): UpstreamBaseUrlValidator {
  if (policy === openAICompatibleBaseUrlPolicy) return openAICompatibleBaseUrlValidator
  if (policy === upstreamRequestUrlPolicy) return upstreamRequestUrlValidator
  return new UpstreamBaseUrlValidator(policy)
}

function canUseHttpUpstreamUrlForConfiguredPrivateHost(value: string, config: UpstreamUrlSecurityConfig): boolean {
  let url: URL
  try {
    url = new URL(value)
  } catch {
    return false
  }
  if (url.protocol !== 'http:') return false
  const hostname = normalizeHostToken(url.hostname)
  return isAllowedPrivateOrigin(url, config)
    || (config.allowPrivateBaseUrls && (isLocalhostName(hostname) || isPrivateOrReservedIp(hostname)))
}

function assertSafeUpstreamUrl(url: URL, config: UpstreamUrlSecurityConfig): void {
  if (config.allowPrivateBaseUrls) return
  const hostname = normalizeHostToken(url.hostname)
  if (isAllowedPrivateOrigin(url, config)) return
  if (isLocalhostName(hostname) || isPrivateOrReservedIp(hostname)) {
    throw new UnsafeUpstreamUrlError()
  }
}

function isLocalhostName(hostname: string): boolean {
  return hostname === 'localhost' || hostname.endsWith('.localhost')
}

function isAllowedPrivateOrigin(url: URL, config: UpstreamUrlSecurityConfig): boolean {
  return config.privateBaseUrlAllowlist.includes(upstreamOriginKey(url))
}

function upstreamOriginKey(url: URL): string {
  return `${url.protocol}//${url.hostname.toLowerCase()}:${url.port || (url.protocol === 'https:' ? '443' : '80')}`
}

function normalizeHostToken(value: string): string {
  return value.trim().toLowerCase().replace(/^\[|\]$/g, '')
}

function isPrivateOrReservedIp(value: string): boolean {
  const normalized = normalizeHostToken(value)
  const version = isIP(normalized)
  if (version === 4) {
    return isBlockedIpv4(normalized)
  }
  if (version === 6) {
    return isBlockedIpv6(normalized)
  }
  return false
}

function isBlockedIpv4(address: string): boolean {
  const parts = parseIpv4Parts(address)
  if (!parts) return true
  return blockedIpv4Ranges.some((range) => ipv4MatchesPrefix(parts, range.parts, range.prefixLength))
}

function isBlockedIpv6(address: string): boolean {
  const groups = parseIpv6Groups(address)
  if (!groups) return true
  return blockedIpv6Ranges.some((range) => ipv6MatchesPrefix(groups, range.groups, range.prefixLength))
}

function parseIpv4Parts(address: string): number[] | undefined {
  const parts = address.split('.')
  if (parts.length !== 4) return undefined
  const numericParts = parts.map((part) => Number(part))
  if (numericParts.some((part) => !Number.isInteger(part) || part < 0 || part > 255)) {
    return undefined
  }
  return numericParts
}

function parseIpv6Groups(address: string): number[] | undefined {
  const normalized = address.toLowerCase()
  const compressedParts = normalized.split('::')
  if (compressedParts.length > 2) return undefined
  const leftGroups = parseIpv6Side(compressedParts[0])
  const rightGroups = parseIpv6Side(compressedParts[1] ?? '')
  if (!leftGroups || !rightGroups) return undefined
  const missingGroups = 8 - leftGroups.length - rightGroups.length
  if (compressedParts.length === 1 && missingGroups !== 0) return undefined
  if (compressedParts.length === 2 && missingGroups < 1) return undefined
  return [...leftGroups, ...Array.from({ length: missingGroups }, () => 0), ...rightGroups]
}

function parseIpv6Side(value: string): number[] | undefined {
  if (!value) return []
  const groups: number[] = []
  const parts = value.split(':')
  for (const [index, part] of parts.entries()) {
    if (!part) return undefined
    if (part.includes('.')) {
      if (index !== parts.length - 1) return undefined
      const ipv4Parts = parseIpv4Parts(part)
      if (!ipv4Parts) return undefined
      groups.push((ipv4Parts[0] << 8) | ipv4Parts[1], (ipv4Parts[2] << 8) | ipv4Parts[3])
      continue
    }
    if (!/^[0-9a-f]{1,4}$/.test(part)) return undefined
    groups.push(parseInt(part, 16))
  }
  return groups
}

function ipv4MatchesPrefix(parts: number[], rangeParts: number[], prefixLength: number): boolean {
  let remainingBits = prefixLength
  for (let index = 0; index < 4; index += 1) {
    if (remainingBits <= 0) return true
    const bits = Math.min(8, remainingBits)
    const mask = (0xff << (8 - bits)) & 0xff
    if ((parts[index] & mask) !== (rangeParts[index] & mask)) return false
    remainingBits -= bits
  }
  return true
}

function ipv6MatchesPrefix(groups: number[], rangeGroups: number[], prefixLength: number): boolean {
  let remainingBits = prefixLength
  for (let index = 0; index < 8; index += 1) {
    if (remainingBits <= 0) return true
    const bits = Math.min(16, remainingBits)
    const mask = (0xffff << (16 - bits)) & 0xffff
    if ((groups[index] & mask) !== (rangeGroups[index] & mask)) return false
    remainingBits -= bits
  }
  return true
}

function fixedLookup(addresses: ResolvedAddress[]): UpstreamLookup {
  const normalizedAddresses = addresses.length ? addresses : []
  return ((hostname: string, options: unknown, callback?: unknown) => {
    const cb = typeof options === 'function' ? options : callback
    if (typeof cb !== 'function') return
    const all = typeof options === 'object' && options !== null && 'all' in options && Boolean((options as { all?: unknown }).all)
    if (all) {
      ;(cb as (error: NodeJS.ErrnoException | null, addresses: ResolvedAddress[]) => void)(null, normalizedAddresses)
      return
    }
    const address = normalizedAddresses[0]
    ;(cb as (error: NodeJS.ErrnoException | null, address: string, family: number) => void)(null, address.address, address.family)
  }) as UpstreamLookup
}
