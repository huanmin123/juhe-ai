import type { Request } from 'express'

export interface LoginGuardBlockResult {
  blocked: boolean
  message?: string
  retryAfterSeconds?: number
}

interface AttemptRecord {
  timestamps: number[]
  lockedUntil?: number
}

const windowMs = 10 * 60 * 1000
const lockMs = 15 * 60 * 1000
const ipFailureThreshold = 10
const usernameFailureThreshold = 5
const maxTrackedKeys = 2000

const ipAttempts = new Map<string, AttemptRecord>()
const usernameAttempts = new Map<string, AttemptRecord>()

export function getLoginClientIp(req: Request): string {
  const forwardedFor = req.headers['x-forwarded-for']
  const forwardedValue = Array.isArray(forwardedFor) ? forwardedFor[0] : forwardedFor
  const forwardedIp = forwardedValue?.split(',')[0]?.trim()
  return forwardedIp || req.ip || req.socket.remoteAddress || 'unknown'
}

export function checkLoginAllowed(clientIp: string, username: string): LoginGuardBlockResult {
  cleanupLoginGuard()

  const ipBlock = getActiveBlock(ipAttempts.get(clientIp), '尝试过于频繁，请稍后再试')
  if (ipBlock.blocked) return ipBlock

  const usernameBlock = getActiveBlock(usernameAttempts.get(normalizeUsername(username)), '账号暂时锁定，请稍后再试')
  if (usernameBlock.blocked) return usernameBlock

  return { blocked: false }
}

export function recordFailedLogin(clientIp: string, username: string): LoginGuardBlockResult {
  cleanupLoginGuard()
  pruneLoginGuard()

  const now = Date.now()
  const ipResult = recordAttempt(ipAttempts, clientIp, now, ipFailureThreshold)
  const usernameResult = recordAttempt(usernameAttempts, normalizeUsername(username), now, usernameFailureThreshold)

  if (ipResult.blocked) return { ...ipResult, message: '尝试过于频繁，请稍后再试' }
  if (usernameResult.blocked) return { ...usernameResult, message: '账号暂时锁定，请稍后再试' }
  return { blocked: false }
}

export function recordSuccessfulLogin(clientIp: string, username: string): void {
  ipAttempts.delete(clientIp)
  usernameAttempts.delete(normalizeUsername(username))
}

function recordAttempt(records: Map<string, AttemptRecord>, key: string, now: number, threshold: number): LoginGuardBlockResult {
  const record = records.get(key) ?? { timestamps: [] }
  record.timestamps = record.timestamps.filter((timestamp) => now - timestamp <= windowMs)
  record.timestamps.push(now)
  if (record.timestamps.length >= threshold) {
    record.lockedUntil = now + lockMs
  }
  records.set(key, record)
  return getActiveBlock(record)
}

function getActiveBlock(record: AttemptRecord | undefined, message?: string): LoginGuardBlockResult {
  if (!record?.lockedUntil) return { blocked: false }
  const remainingMs = record.lockedUntil - Date.now()
  if (remainingMs <= 0) return { blocked: false }
  return {
    blocked: true,
    message,
    retryAfterSeconds: Math.ceil(remainingMs / 1000)
  }
}

function cleanupLoginGuard(): void {
  cleanupRecords(ipAttempts)
  cleanupRecords(usernameAttempts)
}

function cleanupRecords(records: Map<string, AttemptRecord>): void {
  const now = Date.now()
  for (const [key, record] of records.entries()) {
    record.timestamps = record.timestamps.filter((timestamp) => now - timestamp <= windowMs)
    const lockActive = typeof record.lockedUntil === 'number' && record.lockedUntil > now
    if (record.timestamps.length === 0 && !lockActive) {
      records.delete(key)
      continue
    }
    if (!lockActive) {
      record.lockedUntil = undefined
    }
  }
}

function pruneLoginGuard(): void {
  pruneRecords(ipAttempts)
  pruneRecords(usernameAttempts)
}

function pruneRecords(records: Map<string, AttemptRecord>): void {
  if (records.size < maxTrackedKeys) return
  const overflow = records.size - maxTrackedKeys + 1
  const oldest = [...records.entries()]
    .sort((first, second) => oldestTimestamp(first[1]) - oldestTimestamp(second[1]))
    .slice(0, overflow)
  for (const [key] of oldest) {
    records.delete(key)
  }
}

function oldestTimestamp(record: AttemptRecord): number {
  return Math.min(record.timestamps[0] ?? Date.now(), record.lockedUntil ?? Date.now())
}

function normalizeUsername(username: string): string {
  return username.trim().toLowerCase()
}
