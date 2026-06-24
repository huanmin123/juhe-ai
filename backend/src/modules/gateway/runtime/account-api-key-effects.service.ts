import { errorLogFields, logger } from '../../../shared/logger.js'
import type { AccountApiKeyRuntimeStatus } from '../../../storage/account-api-key-rotation.js'
import type { OpenAIAccountSecret } from '../../../storage/repositories.js'
import { requestDbService } from '../../db-service/db-service-ipc.js'
import type { OpenAIGatewayTrafficSource } from '../usage/traffic-source.js'
import {
  recordGatewayAccountApiKeySuccessGuard,
  recordGatewayAccountApiKeyFailureGuard,
  recordGatewayAccountApiKeyLocalFailureGuard
} from './account-api-key-failure-guard.service.js'
import { clearGatewayRuntimeCache } from './runtime-cache.service.js'

const accountApiKeySuccessWriteThrottleMs = 30_000
const recentAccountApiKeySuccessWrites = new Map<string, number>()

export function recordGatewayAccountApiKeyFailure(
  account: OpenAIAccountSecret,
  input: {
    status?: Exclude<AccountApiKeyRuntimeStatus, 'active' | 'disabled'>
    statusCode?: number
    errorCode?: string
    errorMessage?: string
    cooldownUntil?: string
    trafficSource?: OpenAIGatewayTrafficSource
    clientIp?: string
    apiKeyId?: string
    source: string
  }
): void {
  if (!account.selectedApiKeyFingerprint) {
    return
  }
  recentAccountApiKeySuccessWrites.delete(accountApiKeyRuntimeCoalesceKey(account))
  const guardDecision = recordGatewayAccountApiKeyFailureGuard(account, {
    status: input.status,
    statusCode: input.statusCode,
    errorCode: input.errorCode,
    errorMessage: input.errorMessage,
    trafficSource: input.trafficSource,
    clientIp: input.clientIp,
    apiKeyId: input.apiKeyId,
    source: input.source
  })
  if (!guardDecision.persist) {
    return
  }
  void requestDbService({
    type: 'record_account_api_key_failure',
    account,
    input: {
      status: input.status,
      statusCode: input.statusCode,
      errorCode: input.errorCode,
      errorMessage: input.errorMessage,
      cooldownUntil: input.cooldownUntil
    }
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
    }), '账户内 API Key 失败运行态写入失败')
  })
}

export function recordGatewayAccountApiKeyLocalFailure(
  account: OpenAIAccountSecret,
  input: {
    status?: Exclude<AccountApiKeyRuntimeStatus, 'active' | 'disabled'>
    errorMessage?: string
  }
): void {
  recordGatewayAccountApiKeyLocalFailureGuard(account, {
    status: input.status,
    errorMessage: input.errorMessage
  })
}

export function recordGatewayAccountApiKeySuccess(account: OpenAIAccountSecret, source: string): void {
  const clearedLocalFailure = recordGatewayAccountApiKeySuccessGuard(account)
  if (!account.selectedApiKeyFingerprint) {
    return
  }
  if (!clearedLocalFailure && shouldSkipRecentAccountApiKeySuccessWrite(account)) {
    return
  }
  rememberAccountApiKeySuccessWrite(account)
  void requestDbService({
    type: 'record_account_api_key_success',
    account
  }).then((result) => {
    if (result.changed) {
      clearGatewayRuntimeCache()
    }
  }).catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'gateway_account_api_key_success_side_effect_failed',
      accountId: account.id,
      selectedApiKeyFingerprint: account.selectedApiKeyFingerprint,
      source
    }), '账户内 API Key 成功运行态写入失败')
  })
}

function shouldSkipRecentAccountApiKeySuccessWrite(account: OpenAIAccountSecret): boolean {
  const key = accountApiKeyRuntimeCoalesceKey(account)
  const previous = recentAccountApiKeySuccessWrites.get(key)
  return previous !== undefined && Date.now() - previous < accountApiKeySuccessWriteThrottleMs
}

function rememberAccountApiKeySuccessWrite(account: OpenAIAccountSecret): void {
  pruneRecentAccountApiKeySuccessWrites()
  recentAccountApiKeySuccessWrites.set(accountApiKeyRuntimeCoalesceKey(account), Date.now())
}

function accountApiKeyRuntimeCoalesceKey(account: OpenAIAccountSecret): string {
  return `${account.id}\u0000${account.selectedApiKeyFingerprint ?? ''}`
}

function pruneRecentAccountApiKeySuccessWrites(): void {
  if (recentAccountApiKeySuccessWrites.size < 5000) {
    return
  }
  const cutoff = Date.now() - accountApiKeySuccessWriteThrottleMs
  for (const [key, timestamp] of recentAccountApiKeySuccessWrites) {
    if (timestamp < cutoff) {
      recentAccountApiKeySuccessWrites.delete(key)
    }
  }
}
