import { errorLogFields, logger } from '../../../shared/logger.js'
import type { AccountApiKeyRuntimeStatus } from '../../../storage/account-api-key-rotation.js'
import type { OpenAIAccountSecret } from '../../../storage/repositories.js'
import { requestDbService } from '../../db-service/db-service-ipc.js'
import type { OpenAIGatewayTrafficSource } from '../usage/traffic-source.js'
import {
  recordGatewayAccountApiKeySuccessGuard,
  recordGatewayAccountApiKeyFailureGuard
} from './account-api-key-failure-guard.service.js'
import { clearGatewayRuntimeCache } from './runtime-cache.service.js'

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

export function recordGatewayAccountApiKeySuccess(account: OpenAIAccountSecret, source: string): void {
  recordGatewayAccountApiKeySuccessGuard(account)
  if (!account.selectedApiKeyFingerprint) {
    return
  }
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
