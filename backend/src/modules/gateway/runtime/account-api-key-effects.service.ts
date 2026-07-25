import { errorLogFields, logger } from '../../../shared/logger.js'
import { runtimeConfig } from '../../../config/runtime.js'
import type { AccountApiKeyRuntimeStatus } from '../../../storage/account-api-key-rotation.js'
import type { OpenAIAccountSecret } from '../../../storage/repositories.js'
import type { OpenAIGatewayTrafficSource } from '../usage/traffic-source.js'
import {
  captureGatewayAccountApiKeyFailureObservation as captureGatewayAccountApiKeyFailureObservationGuard,
  clearGatewayAccountApiKeyTransientFailure,
  recordGatewayAccountApiKeySuccessGuard,
  recordGatewayAccountApiKeyFailureGuard,
  recordGatewayAccountApiKeyTransientFailure
} from './account-api-key-failure-guard.service.js'
import { clearGatewayRuntimeCache } from './runtime-cache.service.js'
import { requestGatewayDbService } from './gateway-db-service-request.js'
import {
  authorizeAccountApiKeyPersistentMutationForTrafficSource,
  type AccountApiKeyPersistentMutationContext
} from './account-api-key-mutation-authority.js'

interface PendingAccountApiKeySuccessWrite {
  account: OpenAIAccountSecret
  source: string
  trafficSource: OpenAIGatewayTrafficSource
  mutationContext: AccountApiKeyPersistentMutationContext
  observedAt: string
  timer?: ReturnType<typeof setTimeout>
  writing: boolean
}

const accountApiKeySuccessWriteCoalesceMs = 250
const pendingAccountApiKeySuccessWrites = new Map<string, PendingAccountApiKeySuccessWrite>()

export async function recordGatewayAccountApiKeyFailure(
  account: OpenAIAccountSecret,
  input: {
    status?: Exclude<AccountApiKeyRuntimeStatus, 'active' | 'disabled'>
    statusCode?: number
    errorCode?: string
    errorMessage?: string
    traceId?: string
    cooldownUntil?: string
    trafficSource?: OpenAIGatewayTrafficSource
    mutationContext?: AccountApiKeyPersistentMutationContext
    clientIp?: string
    apiKeyId?: string
    observationEpoch?: number
    source: string
  }
): Promise<void> {
  if (!account.selectedApiKeyFingerprint || account.apiKeyRuntimeStateDisabled) {
    return
  }
  const observedAt = new Date().toISOString()
  const guardDecision = recordGatewayAccountApiKeyFailureGuard(account, {
    status: input.status,
    statusCode: input.statusCode,
    errorCode: input.errorCode,
    errorMessage: input.errorMessage,
    trafficSource: input.trafficSource,
    mutationContext: input.mutationContext,
    clientIp: input.clientIp,
    apiKeyId: input.apiKeyId,
    observationEpoch: input.observationEpoch,
    source: input.source
  })
  if (guardDecision.reason === 'redis_transient_only') {
    try {
      await recordGatewayAccountApiKeyTransientFailure(account, {
        status: input.status
      })
    } catch (error) {
      logger.warn(errorLogFields(error, {
        event: 'gateway_account_api_key_transient_avoidance_write_failed',
        accountId: account.id,
        selectedApiKeyFingerprint: account.selectedApiKeyFingerprint,
        source: input.source
      }), '账户内 API Key Redis 短暂避让写入失败')
    }
    return
  }
  if (!guardDecision.persist) {
    return
  }
  const mutationContext = input.mutationContext
  const trafficSource = input.trafficSource
  if (!mutationContext || !trafficSource) {
    return
  }
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    void requestGatewayDbService({
      type: 'record_account_api_key_failure',
      account,
      trafficSource,
      mutationContext,
      input: {
        status: input.status,
        statusCode: input.statusCode,
        errorCode: input.errorCode,
        errorMessage: input.errorMessage,
        traceId: input.traceId,
        cooldownUntil: input.cooldownUntil,
        observedAt
      }
    }, {
      priority: 'low'
    }).then((result) => {
      if (result.changed) {
        clearGatewayRuntimeCache()
      }
    }).catch((error) => {
      logger.warn(errorLogFields(error, {
        event: 'gateway_account_api_key_failure_side_effect_failed',
        accountId: account.id,
        selectedApiKeyFingerprint: account.selectedApiKeyFingerprint,
        source: input.source
      }), '账户内 API Key 失败运行态异步写入失败')
    })
    return
  }
  try {
    const result = await requestGatewayDbService({
      type: 'record_account_api_key_failure',
      account,
      trafficSource,
      mutationContext,
      input: {
        status: input.status,
        statusCode: input.statusCode,
        errorCode: input.errorCode,
        errorMessage: input.errorMessage,
        traceId: input.traceId,
        cooldownUntil: input.cooldownUntil,
        observedAt
      }
    }, {
      priority: 'low'
    })
    if (result.changed) {
      clearGatewayRuntimeCache()
    }
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'gateway_account_api_key_failure_side_effect_failed',
      accountId: account.id,
      selectedApiKeyFingerprint: account.selectedApiKeyFingerprint,
      source: input.source
    }), '账户内 API Key 失败运行态写入失败')
  }
}

export function captureGatewayAccountApiKeyFailureObservation(account: OpenAIAccountSecret): number | undefined {
  return captureGatewayAccountApiKeyFailureObservationGuard(account)
}

export function recordGatewayAccountApiKeySuccess(
  account: OpenAIAccountSecret,
  input: {
    source: string
    trafficSource?: OpenAIGatewayTrafficSource
    mutationContext?: AccountApiKeyPersistentMutationContext
  }
): void {
  if (account.apiKeyRuntimeStateDisabled) {
    return
  }
  const observedAt = new Date().toISOString()
  const authorization = authorizeAccountApiKeyPersistentMutationForTrafficSource(
    'success',
    input.trafficSource,
    input.mutationContext
  )
  if (input.mutationContext && !authorization.allowed) {
    return
  }
  if (input.trafficSource === 'gateway') {
    recordGatewayAccountApiKeySuccessGuard(account)
  }
  if (input.trafficSource === 'gateway' && runtimeConfig.runtimeStateDriver === 'redis' && account.selectedApiKeyFingerprint) {
    void clearGatewayAccountApiKeyTransientFailure(account).catch((error) => {
      logger.warn(errorLogFields(error, {
        event: 'gateway_account_api_key_transient_avoidance_clear_failed',
        accountId: account.id,
        selectedApiKeyFingerprint: account.selectedApiKeyFingerprint,
        source: input.source
      }), '账户内 API Key Redis 短暂避让清理失败')
    })
  }
  if (!account.selectedApiKeyFingerprint || !authorization.allowed || !input.mutationContext || !input.trafficSource) {
    return
  }
  coalesceGatewayAccountApiKeySuccessWrite(
    account,
    input.source,
    input.trafficSource,
    input.mutationContext,
    observedAt
  )
}

function coalesceGatewayAccountApiKeySuccessWrite(
  account: OpenAIAccountSecret,
  source: string,
  trafficSource: OpenAIGatewayTrafficSource,
  mutationContext: AccountApiKeyPersistentMutationContext,
  observedAt: string
): void {
  const key = accountApiKeySuccessWriteKey(account)
  const current = pendingAccountApiKeySuccessWrites.get(key)
  if (current) {
    if (observedAt >= current.observedAt) {
      current.account = account
      current.source = source
      current.trafficSource = trafficSource
      current.mutationContext = mutationContext
      current.observedAt = observedAt
    }
    return
  }
  const entry: PendingAccountApiKeySuccessWrite = {
    account,
    source,
    trafficSource,
    mutationContext,
    observedAt,
    writing: false
  }
  pendingAccountApiKeySuccessWrites.set(key, entry)
  scheduleAccountApiKeySuccessWrite(key, entry)
}

function scheduleAccountApiKeySuccessWrite(key: string, entry: PendingAccountApiKeySuccessWrite): void {
  if (entry.timer || entry.writing) return
  entry.timer = setTimeout(() => {
    entry.timer = undefined
    void flushAccountApiKeySuccessWrite(key, entry)
  }, accountApiKeySuccessWriteCoalesceMs)
  entry.timer.unref?.()
}

async function flushAccountApiKeySuccessWrite(key: string, entry: PendingAccountApiKeySuccessWrite): Promise<void> {
  if (entry.writing || pendingAccountApiKeySuccessWrites.get(key) !== entry) return
  entry.writing = true
  const account = entry.account
  const source = entry.source
  const trafficSource = entry.trafficSource
  const mutationContext = entry.mutationContext
  const observedAt = entry.observedAt
  try {
    const result = await requestGatewayDbService({
      type: 'record_account_api_key_success',
      account,
      trafficSource,
      mutationContext,
      observedAt
    }, {
      priority: 'low'
    })
    if (result.changed) clearGatewayRuntimeCache()
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'gateway_account_api_key_success_side_effect_failed',
      accountId: account.id,
      selectedApiKeyFingerprint: account.selectedApiKeyFingerprint,
      source
    }), '账户内 API Key 成功运行态写入失败')
  } finally {
    entry.writing = false
    if (pendingAccountApiKeySuccessWrites.get(key) !== entry) return
    if (entry.observedAt === observedAt) {
      pendingAccountApiKeySuccessWrites.delete(key)
    } else {
      scheduleAccountApiKeySuccessWrite(key, entry)
    }
  }
}

function accountApiKeySuccessWriteKey(account: OpenAIAccountSecret): string {
  return `${account.credentialSourceAccountId ?? account.id}\u0000${account.selectedApiKeyFingerprint ?? ''}`
}

export async function flushGatewayAccountApiKeySuccessWritesForTest(): Promise<void> {
  while (pendingAccountApiKeySuccessWrites.size) {
    const entries = [...pendingAccountApiKeySuccessWrites.entries()]
    for (const [, entry] of entries) {
      if (entry.timer) {
        clearTimeout(entry.timer)
        entry.timer = undefined
      }
    }
    await Promise.all(entries.map(([key, entry]) => flushAccountApiKeySuccessWrite(key, entry)))
  }
}
