import { createHash } from 'node:crypto'
import type { Request } from 'express'

import { createAppCache } from '../../shared/cache.js'
import type { OpenAIAccountSecret } from '../../storage/repositories.js'

interface SessionBinding {
  accountId: string
}

const sessionAffinityTtlMs = 60 * 60 * 1000

const sessionAffinityCache = createAppCache<string, SessionBinding>({
  name: 'gateway:openai-session-affinity',
  max: 5000,
  ttlMs: sessionAffinityTtlMs,
  updateAgeOnGet: true
})

export function resolveOpenAIGatewaySessionAffinityKey(req: Request, input: {
  systemAccountId: string
  apiKeyId: string
  groupId: string
}): string | undefined {
  const session = extractSessionIdentity(req)
  if (!session) {
    return undefined
  }
  return createHash('sha256')
    .update(JSON.stringify({
      systemAccountId: input.systemAccountId,
      apiKeyId: input.apiKeyId,
      groupId: input.groupId,
      session
    }))
    .digest('hex')
}

export function orderOpenAIAccountsBySessionAffinity(
  accounts: OpenAIAccountSecret[],
  sessionAffinityKey?: string
): OpenAIAccountSecret[] {
  if (!sessionAffinityKey || accounts.length < 2) {
    return accounts
  }
  const binding = sessionAffinityCache.get(sessionAffinityKey)
  if (!binding) {
    return accounts
  }
  const boundIndex = accounts.findIndex((account) => account.id === binding.accountId)
  if (boundIndex <= 0) {
    return accounts
  }
  return [
    accounts[boundIndex],
    ...accounts.slice(0, boundIndex),
    ...accounts.slice(boundIndex + 1)
  ]
}

export function rememberOpenAIAccountForSession(sessionAffinityKey: string | undefined, accountId: string): void {
  if (!sessionAffinityKey) {
    return
  }
  sessionAffinityCache.set(sessionAffinityKey, { accountId })
}

export function forgetOpenAIAccountForSession(sessionAffinityKey: string | undefined, accountId?: string): void {
  if (!sessionAffinityKey) {
    return
  }
  const binding = sessionAffinityCache.get(sessionAffinityKey)
  if (!binding) {
    return
  }
  if (accountId && binding.accountId !== accountId) {
    return
  }
  sessionAffinityCache.delete(sessionAffinityKey)
}

function extractSessionIdentity(req: Request): { source: string; value: string } | undefined {
  for (const name of sessionHeaderNames) {
    const value = stringValue(req.header(name))
    if (value) {
      return { source: `header:${name.toLowerCase()}`, value }
    }
  }

  for (const path of sessionBodyPaths) {
    const value = stringValue(valueAtPath(req.body, path))
    if (value) {
      return { source: `body:${path.join('.')}`, value }
    }
  }

  return undefined
}

function valueAtPath(value: unknown, path: string[]): unknown {
  let current = value
  for (const key of path) {
    if (typeof current !== 'object' || current === null) {
      return undefined
    }
    current = (current as Record<string, unknown>)[key]
  }
  return current
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

const sessionHeaderNames = [
  'session_id',
  'session-id',
  'x-session-id',
  'conversation_id',
  'conversation-id',
  'x-conversation-id',
  'prompt_cache_key',
  'x-prompt-cache-key'
]

const sessionBodyPaths = [
  ['session_id'],
  ['conversation_id'],
  ['prompt_cache_key'],
  ['metadata', 'session_id'],
  ['metadata', 'conversation_id'],
  ['metadata', 'user_id']
]
