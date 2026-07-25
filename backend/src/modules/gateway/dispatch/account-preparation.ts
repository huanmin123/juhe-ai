import type { Request } from 'express'

import { isOpenAIProtocolProfile } from '../../../domain/provider-protocol.js'
import { getRequestLogger } from '../../../shared/request-context.js'
import type { GatewaySettings } from '../policy/account-error-policy.service.js'
import {
  type GatewayAccountFailurePrecheckInput,
  recordGatewayAccountFailureForPrecheck,
  suppressGatewayAccountLocally
} from '../runtime/account-side-effects.service.js'
import { applyAccountErrorHandlingWithCacheInvalidation } from '../runtime/account-effects.js'
import {
  failedProxyDispatchReason,
  rememberFailedProxyForDispatch,
} from './helpers.js'
import { OpenAIOAuthCodexAdapterError } from '../adapters/gpt-codex/oauth-adapter.js'
import { buildGatewayUpstreamRequestParts, prepareGatewayUpstreamAccount } from '../../providers/drivers/registry.js'
import type { ProviderGatewayRequestContext } from '../../providers/drivers/_shared/types.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import type { UpstreamAttempt } from '../upstream/attempt.js'
import type { AuditCaptureContext } from '../audit/capture.service.js'
import {
  accountApiKeyEntries,
  isAccountApiKeyPoolIsolationEnabled,
  selectAccountRuntimeApiKeyEntryAsync
} from '../../../storage/account-api-key-rotation.js'
import {
  recordFailedUpstreamAttempt,
  type GatewayUsageContext
} from '../usage/records.js'
import { recordGatewayProxyFailureAsync } from '../runtime/proxy-health.service.js'
import { requestEndpoint } from '../request/metadata.js'
import { loadGatewayAccountApiKeyTransientStatesForDispatch } from '../runtime/account-api-key-failure-guard.service.js'
import { extractGatewayJsonBodyMetadata } from '../request/json-metadata-scanner.js'
import { replaceGatewayJsonBody, type GatewayRawBodyRequest } from '../request/body.js'
import type { UsageServiceTier } from '../usage/service-tier.js'
import type { UsageReasoningEffort } from '../usage/reasoning-effort.js'
import { prepareCodexResponsesContextForAccount } from '../codex-responses/chat-bridge-state.js'
import { sanitizeCodexResponseHistoryItems } from '../codex-responses/request-history-sanitizer.js'
import { codexResponsesContractRevision } from '../codex-responses/contract-registry.js'
import { gatewayRequestEndpointFamily } from '../protocols/openai-v1/model-mapping.js'

export interface PreparedUpstreamRequestParts {
  headers: Headers
  body?: Buffer | string
  effectiveServiceTier: UsageServiceTier
  effectiveReasoningEffort?: UsageReasoningEffort
}

type GatewayAccountFailurePrecheckRecorder = (
  account: UpstreamAccount,
  settings: GatewaySettings | undefined,
  input: GatewayAccountFailurePrecheckInput
) => void

export function skipAccountForFailedProxyDispatch(
  failedProxyDispatchKeys: Map<string, string>,
  account: UpstreamAccount
): UpstreamAttempt | undefined {
  const skippedProxyReason = failedProxyDispatchReason(failedProxyDispatchKeys, account)
  if (!skippedProxyReason) {
    return undefined
  }

  const message = `账户绑定的代理已在本次调度中失败，跳过重复尝试：${skippedProxyReason}`
  getRequestLogger().warn({
    event: 'gateway_proxy_duplicate_skipped',
    accountId: account.id,
    accountType: account.type,
    proxyProfileId: account.proxyProfileId,
    proxyConfigured: Boolean(account.proxyProfileId || account.proxyUrl)
  }, '跳过已失败代理绑定账号')
  return {
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    upstreamUrl: 'proxy:skipped',
    message
  }
}

export async function handleUnavailableProxyProfile(
  req: Request,
  usageContext: GatewayUsageContext,
  account: UpstreamAccount,
  settings: GatewaySettings,
  failedProxyDispatchKeys: Map<string, string>,
  accountStateMutationEnabled = true,
  recordPrecheckFailure: GatewayAccountFailurePrecheckRecorder = recordGatewayAccountFailureForPrecheck,
  auditCapture?: AuditCaptureContext,
  auditAttemptIndex?: number
): Promise<UpstreamAttempt | undefined> {
  if (!account.proxyProfileUnavailable) {
    return undefined
  }

  const attemptStartedAt = Date.now()
  const message = account.proxyProfileErrorMessage ?? '账户绑定的代理不可用'
  const lastAttempt = {
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    upstreamUrl: 'proxy:configured',
    message
  }
  await recordFailedUpstreamAttempt(req, usageContext, account, {
    upstreamUrl: 'proxy:configured',
    startedAt: attemptStartedAt,
    errorMessage: message
  })
  if (auditCapture && typeof auditAttemptIndex === 'number') {
    auditCapture.recordFailedDispatchAttempt({
      account,
      attemptIndex: auditAttemptIndex,
      upstreamUrl: 'proxy:configured',
      method: req.method,
      startedAtMs: attemptStartedAt,
      errorPhase: 'dispatch',
      errorCode: 'proxy_unavailable',
      errorMessage: message,
      requestForModelAccounting: req
    })
  }
  if (accountStateMutationEnabled && usageContext.trafficSource !== 'gateway') {
    await applyAccountErrorHandlingWithCacheInvalidation(account, {
      success: false,
      errorMessage: message,
      settings,
      trafficSource: usageContext.trafficSource
    })
  }
  if (accountStateMutationEnabled) {
    const localSuppression = suppressGatewayAccountLocally(account, settings, message)
    if (usageContext.trafficSource === 'gateway') {
      recordPrecheckFailure(account, settings, {
        systemAccountId: usageContext.systemAccountId,
        groupId: usageContext.groupId,
        apiKeyId: usageContext.apiKeyId,
        clientIp: usageContext.clientIp,
        endpoint: requestEndpoint(req),
        reason: message,
        forcePrecheck: localSuppression.action === 'precheck_required',
        localSuppressionDelayMs: localSuppression.delayMs
      })
    }
    await recordGatewayProxyFailureAsync(account, message)
  }
  rememberFailedProxyForDispatch(failedProxyDispatchKeys, account, message)
  return lastAttempt
}

export async function prepareUpstreamAccount(account: UpstreamAccount, signal?: AbortSignal): Promise<UpstreamAccount> {
  return await prepareGatewayUpstreamAccount(account, signal)
}

export async function selectAccountApiKeyForDispatch(
  account: UpstreamAccount,
  options: {
    excludeFingerprints?: Iterable<string>
    continueAfterFingerprint?: string
  } = {}
): Promise<UpstreamAccount | undefined> {
  if (account.type !== 'api_key') {
    return account
  }

  const accountId = account.credentialSourceAccountId ?? account.id
  const credentials = accountApiKeySelectionCredentials(account)
  const apiKeyEntries = accountApiKeyEntries(credentials)
  const apiKeyPoolIsolationEnabled = isAccountApiKeyPoolIsolationEnabled({
    providerCode: account.providerCode,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    type: account.type,
    credentials
  })
  const fixedFingerprint = account.selectedApiKeyFingerprint?.trim()
  if (fixedFingerprint && apiKeyPoolIsolationEnabled) {
    const excludedFingerprints = new Set(options.excludeFingerprints ?? [])
    if (excludedFingerprints.has(fixedFingerprint)) {
      return undefined
    }
    const fixed = apiKeyEntries.find((entry) => entry.fingerprint === fixedFingerprint)
    if (!fixed) {
      return undefined
    }
    return accountWithSelectedApiKey(
      account,
      fixed.key,
      fixed.fingerprint,
      fixed.index,
      account.selectedApiKeyTransientGeneration
    )
  }

  const transientStates = await loadGatewayAccountApiKeyTransientStatesForDispatch(
    accountId,
    apiKeyEntries.map((entry) => entry.fingerprint)
  )

  const runtimeStates = [
    ...(account.apiKeyRuntimeStates ?? []),
    ...transientStates
  ]
  const selected = await selectAccountRuntimeApiKeyEntryAsync({
    accountId,
    credentials,
    excludeFingerprints: options.excludeFingerprints,
    continueAfterFingerprint: options.continueAfterFingerprint,
    runtimeStates
  })
  if (!selected && apiKeyPoolIsolationEnabled) {
    return undefined
  }
  if (!selected) {
    return account
  }

  return accountWithSelectedApiKey(
    account,
    selected.key,
    apiKeyPoolIsolationEnabled ? selected.fingerprint : undefined,
    apiKeyPoolIsolationEnabled ? selected.index : undefined,
    apiKeyPoolIsolationEnabled
      ? runtimeStates.find((state) => state.keyFingerprint === selected.fingerprint && state.transientGeneration !== undefined)?.transientGeneration
      : undefined
  )
}

function accountWithSelectedApiKey(
  account: UpstreamAccount,
  apiKey: string,
  selectedApiKeyFingerprint?: string,
  selectedApiKeyIndex?: number,
  selectedApiKeyTransientGeneration?: string
): UpstreamAccount {
  return {
    ...account,
    apiKey,
    selectedApiKeyFingerprint,
    selectedApiKeyIndex,
    selectedApiKeyTransientGeneration,
    credentials: {
      ...account.credentials,
      api_key: apiKey
    }
  }
}

export async function buildPreparedUpstreamRequestParts(
  req: Request,
  account: UpstreamAccount,
  usageContext: GatewayUsageContext,
  signal?: AbortSignal,
  context?: ProviderGatewayRequestContext
): Promise<PreparedUpstreamRequestParts> {
  try {
    prepareCodexResponsesContextForAccount(req, account)
    const parts = await buildGatewayUpstreamRequestParts(req, account, {
      systemAccountId: usageContext.systemAccountId,
      apiKeyId: usageContext.apiKeyId,
      groupId: usageContext.groupId
    }, signal, context)
    const bodyBuffer = typeof parts.body === 'string' ? Buffer.from(parts.body, 'utf8') : parts.body
    const metadata = bodyBuffer ? extractGatewayJsonBodyMetadata(bodyBuffer) : undefined
    return {
      ...parts,
      effectiveServiceTier: metadata?.serviceTier ?? 'default',
      effectiveReasoningEffort: metadata?.reasoningEffort
    }
  } catch (error) {
    if (error instanceof OpenAIOAuthCodexAdapterError) {
      const responseBodyText = JSON.stringify({
        error: {
          message: error.message,
          type: error.type,
          code: error.code
        }
      })
      await recordFailedUpstreamAttempt(req, usageContext, account, {
        upstreamUrl: account.type === 'oauth' && isOpenAIProtocolProfile(account)
          ? 'openai-oauth-codex:local-validation'
          : 'gateway:local-validation',
        startedAt: Date.now(),
        statusCode: error.statusCode,
        bodyText: responseBodyText,
        errorMessage: error.message
      })
    }
    throw error
  }
}

function sanitizeCodexResponsesHistoryForAccount(
  req: Request,
  account: UpstreamAccount,
  context: ProviderGatewayRequestContext | undefined
): void {
  if (context?.requestClientCompatibility !== 'codex_responses') return
  if (gatewayRequestEndpointFamily(req) !== 'responses') return
  const body = gatewayJsonObjectBody(req)
  if (!body || !Array.isArray(body.input)) return
  const result = sanitizeCodexResponseHistoryItems(body.input, {
    store: false,
    targetScopeKey: `account:${account.id}`,
    targetPersistenceScope: 'none',
    contractRevision: codexResponsesContractRevision
  })
  if (!result.changed) return
  replaceGatewayJsonBody(req, {
    ...body,
    input: result.items
  })
}

function gatewayJsonObjectBody(req: Request): Record<string, unknown> | undefined {
  const request = req as GatewayRawBodyRequest
  const body = request.body !== undefined
    ? request.body
    : request.gatewayParsedJsonBodyAvailable
      ? request.gatewayParsedJsonBody
      : undefined
  return isPlainObject(body) ? body : undefined
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
function accountApiKeySelectionCredentials(account: UpstreamAccount): Record<string, unknown> {
  return {
    ...account.credentials,
    api_key: account.apiKey,
    ...(account.apiKeys?.length ? { api_keys: account.apiKeys } : {})
  }
}
