import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { createRuntimeStateStore } from '../../shared/runtime-state-store.js'

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
const usernameFailureThreshold = 10
const maxTrackedKeys = 2000
const loginGuardCleanupIntervalMs = 30 * 1000
const loginGuardCleanupBatchSize = 64

const ipAttempts = new Map<string, AttemptRecord>()
const usernameAttempts = new Map<string, AttemptRecord>()
const loginGuardStateStore = createRuntimeStateStore('auth_login_guard')
let nextLoginGuardCleanupAt = 0

export function getLoginClientIp(req: Request): string {
  return normalizeClientIp(req.ip) ?? normalizeClientIp(req.socket.remoteAddress) ?? 'unknown'
}

export function checkLoginAllowed(clientIp: string, username: string): LoginGuardBlockResult {
  const now = Date.now()
  runLoginGuardMaintenance(now)

  const ipBlock = getActiveBlock(ipAttempts.get(clientIp), '尝试过于频繁，请稍后再试', now)
  if (ipBlock.blocked) return ipBlock

  const usernameBlock = getActiveBlock(usernameAttempts.get(normalizeUsername(username)), '账号暂时锁定，请稍后再试', now)
  if (usernameBlock.blocked) return usernameBlock

  return { blocked: false }
}

export function recordFailedLogin(clientIp: string, username: string): LoginGuardBlockResult {
  const now = Date.now()
  runLoginGuardMaintenance(now)
  const normalizedUsername = normalizeUsername(username)
  pruneRecords(ipAttempts, clientIp)
  pruneRecords(usernameAttempts, normalizedUsername)

  const ipResult = recordAttempt(ipAttempts, clientIp, now, ipFailureThreshold)
  const usernameResult = recordAttempt(usernameAttempts, normalizedUsername, now, usernameFailureThreshold)

  if (ipResult.blocked) return { ...ipResult, message: '尝试过于频繁，请稍后再试' }
  if (usernameResult.blocked) return { ...usernameResult, message: '账号暂时锁定，请稍后再试' }
  return { blocked: false }
}

export function recordSuccessfulLogin(clientIp: string, username: string): void {
  ipAttempts.delete(clientIp)
  usernameAttempts.delete(normalizeUsername(username))
}

export async function checkLoginAllowedAsync(clientIp: string, username: string): Promise<LoginGuardBlockResult> {
  if (runtimeConfig.runtimeStateDriver === 'memory') {
    return checkLoginAllowed(clientIp, username)
  }
  const now = Date.now()
  const [ipLock, usernameLock] = await Promise.all([
    getRedisGuardBlock(redisGuardLockKey('ip', clientIp), '尝试过于频繁，请稍后再试', now),
    getRedisGuardBlock(redisGuardLockKey('username', normalizeUsername(username)), '账号暂时锁定，请稍后再试', now)
  ])
  return ipLock.blocked ? ipLock : usernameLock
}

export async function recordFailedLoginAsync(clientIp: string, username: string): Promise<LoginGuardBlockResult> {
  if (runtimeConfig.runtimeStateDriver === 'memory') {
    return recordFailedLogin(clientIp, username)
  }
  const normalizedUsername = normalizeUsername(username)
  const [ipResult, usernameResult] = await Promise.all([
    recordRedisAttempt('ip', clientIp, ipFailureThreshold, '尝试过于频繁，请稍后再试'),
    recordRedisAttempt('username', normalizedUsername, usernameFailureThreshold, '账号暂时锁定，请稍后再试')
  ])
  if (ipResult.blocked) return ipResult
  if (usernameResult.blocked) return usernameResult
  return { blocked: false }
}

export async function recordSuccessfulLoginAsync(clientIp: string, username: string): Promise<void> {
  if (runtimeConfig.runtimeStateDriver === 'memory') {
    recordSuccessfulLogin(clientIp, username)
    return
  }
  const normalizedUsername = normalizeUsername(username)
  await Promise.all([
    loginGuardStateStore.delete(redisGuardCounterKey('ip', clientIp)),
    loginGuardStateStore.delete(redisGuardLockKey('ip', clientIp)),
    loginGuardStateStore.delete(redisGuardCounterKey('username', normalizedUsername)),
    loginGuardStateStore.delete(redisGuardLockKey('username', normalizedUsername))
  ])
}

function recordAttempt(records: Map<string, AttemptRecord>, key: string, now: number, threshold: number): LoginGuardBlockResult {
  const record = records.get(key) ?? { timestamps: [] }
  record.timestamps = trimRecentTimestamps(record.timestamps, now, threshold - 1)
  record.timestamps.push(now)
  if (record.timestamps.length >= threshold) {
    record.lockedUntil = now + lockMs
  }
  records.delete(key)
  records.set(key, record)
  return getActiveBlock(record, undefined, now)
}

function getActiveBlock(record: AttemptRecord | undefined, message?: string, now = Date.now()): LoginGuardBlockResult {
  if (!record?.lockedUntil) return { blocked: false }
  const remainingMs = record.lockedUntil - now
  if (remainingMs <= 0) return { blocked: false }
  return {
    blocked: true,
    message,
    retryAfterSeconds: Math.ceil(remainingMs / 1000)
  }
}

function runLoginGuardMaintenance(now: number): void {
  if (now < nextLoginGuardCleanupAt) return
  nextLoginGuardCleanupAt = now + loginGuardCleanupIntervalMs
  cleanupRecords(ipAttempts, now, ipFailureThreshold)
  cleanupRecords(usernameAttempts, now, usernameFailureThreshold)
}

function cleanupRecords(records: Map<string, AttemptRecord>, now: number, maxTimestamps: number): void {
  let inspected = 0
  while (inspected < loginGuardCleanupBatchSize) {
    const nextEntry = records.entries().next()
    if (nextEntry.done) break

    const [key, record] = nextEntry.value
    records.delete(key)
    record.timestamps = trimRecentTimestamps(record.timestamps, now, maxTimestamps)
    const lockActive = typeof record.lockedUntil === 'number' && record.lockedUntil > now
    if (!lockActive) {
      record.lockedUntil = undefined
    }
    if (record.timestamps.length > 0 || lockActive) {
      records.set(key, record)
    }
    inspected += 1
  }
}

function pruneRecords(records: Map<string, AttemptRecord>, protectedKey: string): void {
  if (records.size < maxTrackedKeys) return
  const overflow = records.size - maxTrackedKeys + 1
  let removed = 0
  for (const key of records.keys()) {
    if (key === protectedKey) continue
    records.delete(key)
    removed += 1
    if (removed >= overflow) break
  }
}

function trimRecentTimestamps(timestamps: number[], now: number, maxTimestamps: number): number[] {
  const earliestAllowedAt = now - windowMs
  const recentTimestamps: number[] = []
  for (let index = timestamps.length - 1; index >= 0 && recentTimestamps.length < maxTimestamps; index -= 1) {
    const timestamp = timestamps[index]
    if (timestamp < earliestAllowedAt) break
    recentTimestamps.push(timestamp)
  }
  return recentTimestamps.reverse()
}

function normalizeUsername(username: string): string {
  return username.trim().toLowerCase()
}

function normalizeClientIp(value: string | undefined): string | undefined {
  const text = value?.trim()
  return text || undefined
}

async function recordRedisAttempt(scope: 'ip' | 'username', value: string, threshold: number, message: string): Promise<LoginGuardBlockResult> {
  const now = Date.now()
  const lockKey = redisGuardLockKey(scope, value)
  const activeBlock = await getRedisGuardBlock(lockKey, message, now)
  if (activeBlock.blocked) return activeBlock

  const count = await loginGuardStateStore.incr(redisGuardCounterKey(scope, value), { ttlMs: windowMs })
  if (count < threshold) {
    return { blocked: false }
  }

  const lockedUntil = now + lockMs
  await loginGuardStateStore.setJson(lockKey, lockedUntil, lockMs)
  return getActiveBlock({ timestamps: [], lockedUntil }, message, now)
}

async function getRedisGuardBlock(key: string, message: string, now: number): Promise<LoginGuardBlockResult> {
  const lockedUntil = await loginGuardStateStore.getJson<number>(key)
  return getActiveBlock(
    typeof lockedUntil === 'number' ? { timestamps: [], lockedUntil } : undefined,
    message,
    now
  )
}

function redisGuardCounterKey(scope: 'ip' | 'username', value: string): string {
  return `login:${scope}:${value}:count`
}

function redisGuardLockKey(scope: 'ip' | 'username', value: string): string {
  return `login:${scope}:${value}:lock`
}
