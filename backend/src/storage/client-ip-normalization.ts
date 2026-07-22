import { createHash } from 'node:crypto'
import { isIP } from 'node:net'

export const clientIpRegistryBucketCount = 4096

export interface NormalizedClientIp {
  clientIp: string
  aggregateIpKey: string
  ipVersion: 4
  ipHash: string
  bucketNo: number
}

export function normalizeClientIpForStats(value?: string | null): NormalizedClientIp | undefined {
  const normalizedIp = normalizePlainClientIp(value)
  if (!normalizedIp) return undefined
  const version = isIP(normalizedIp)
  if (version === 4) {
    const clientIp = normalizeIpv4(normalizedIp)
    if (!clientIp) return undefined
    return clientIpIdentity(clientIp, clientIp, 4)
  }
  return undefined
}

export function normalizeIpHash(value: string): string | undefined {
  const text = value.trim().toLowerCase()
  return /^[0-9a-f]{64}$/.test(text) ? text : undefined
}

function normalizePlainClientIp(value?: string | null): string | undefined {
  if (!value) return undefined
  let ip = value.trim()
  if (!ip) return undefined
  if (ip.includes(',')) {
    ip = ip.split(',')[0].trim()
  }
  const zoneIndex = ip.indexOf('%')
  if (zoneIndex > 0) {
    ip = ip.slice(0, zoneIndex)
  }
  if (ip.startsWith('[')) {
    const end = ip.indexOf(']')
    if (end > 0) ip = ip.slice(1, end)
  }
  if (/^\d{1,3}(?:\.\d{1,3}){3}:\d+$/.test(ip)) {
    ip = ip.replace(/:\d+$/, '')
  }
  if (ip.toLowerCase().startsWith('::ffff:')) {
    ip = ip.slice('::ffff:'.length)
  }
  return ip.toLowerCase()
}

function normalizeIpv4(value: string): string | undefined {
  if (isIP(value) !== 4) return undefined
  const parts = value.split('.').map((part) => Number(part))
  if (parts.length !== 4 || parts.some((part) => !Number.isInteger(part) || part < 0 || part > 255)) return undefined
  return parts.join('.')
}

function clientIpIdentity(clientIp: string, aggregateIpKey: string, ipVersion: 4): NormalizedClientIp {
  const ipHash = createHash('sha256').update(`client-ip:${aggregateIpKey}`).digest('hex')
  return {
    clientIp,
    aggregateIpKey,
    ipVersion,
    ipHash,
    bucketNo: Number.parseInt(ipHash.slice(0, 8), 16) % clientIpRegistryBucketCount
  }
}
