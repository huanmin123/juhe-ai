import type { Request } from 'express'

import type { AccountSummary, AccountUsageSummary } from '../../domain/types.js'
import type { RecentOpenAIRequestShape } from '../../storage/repositories.js'
import type { GatewaySettings } from './account-error-policy.service.js'
import {
  requestEndpoint,
  requestModel,
  requestStream
} from './openai-gateway-usage.js'
import { type UpstreamAccount } from './openai-gateway-route-helpers.js'

export interface CodexSwitchProbeResult {
  accountId: string
  accountName: string
  success: boolean
  statusCode?: number
  upstreamUrl?: string
  durationMs: number
  errorCode?: string
  message: string
  traceId?: string
  model?: string
}

const codexSwitchProbeTimeoutMs = 8_000
const codexSwitchProbeTimeoutSeconds = Math.ceil(codexSwitchProbeTimeoutMs / 1000)

export async function probeCodexSwitchCandidateAccount(
  account: UpstreamAccount,
  input: {
    req: Request
    systemAccountId: string
    groupId: string
    settings: GatewaySettings
    signal?: AbortSignal
  }
): Promise<CodexSwitchProbeResult> {
  const startedAt = Date.now()
  if (account.status !== 'active') {
    return failedProbe(account, startedAt, 'account_inactive', `账号状态不是 active：${account.status}`)
  }

  const summary = accountSummaryFromUpstreamAccount(account)
  const signal = codexSwitchProbeSignal(input.signal)

  try {
    const accountTestService = await import('../accounts/account-test.service.js')
    const model = requestModel(input.req) || accountTestService.preferredSystemAccountTestModel(summary)
    const result = await accountTestService.testOpenAIAccount(summary, {
      model,
      requestShape: currentRequestShape(input.req, model),
      groupId: input.groupId,
      systemAccountId: input.systemAccountId,
      signal,
      diagnostics: 'full',
      trafficSource: 'manual_account_test',
      gatewaySettingsOverride: codexSwitchProbeGatewaySettings(input.settings),
      disableAccountStateMutation: true,
      clientCompatibility: account.clientCompatibility,
      candidateAccount: account
    })
    return {
      accountId: account.id,
      accountName: account.name,
      success: result.success,
      statusCode: result.statusCode,
      upstreamUrl: result.requestUrl,
      durationMs: Date.now() - startedAt,
      errorCode: result.success ? undefined : result.errorCode ?? probeErrorCode(result.statusCode),
      message: result.success ? 'Codex 切号真实账号测试通过' : probeFailureMessage(result),
      traceId: result.traceId,
      model: result.model
    }
  } catch (error) {
    return failedProbe(account, startedAt, 'account_test_failed', errorMessage(error))
  }
}

function codexSwitchProbeGatewaySettings(settings: GatewaySettings): Partial<GatewaySettings> {
  return {
    streamCircuitBreakerEnabled: settings.streamCircuitBreakerEnabled,
    streamRequestTimeoutSeconds: Math.min(settings.streamRequestTimeoutSeconds, codexSwitchProbeTimeoutSeconds),
    streamIdleTimeoutSeconds: Math.min(settings.streamIdleTimeoutSeconds, codexSwitchProbeTimeoutSeconds),
    temporaryUnschedulableRetryAttempts: 0
  }
}

function codexSwitchProbeSignal(signal?: AbortSignal): AbortSignal {
  const timeoutSignal = AbortSignal.timeout(codexSwitchProbeTimeoutMs)
  if (!signal) {
    return timeoutSignal
  }
  if (signal.aborted) {
    return signal
  }
  return AbortSignal.any([signal, timeoutSignal])
}

function currentRequestShape(req: Request, model?: string): RecentOpenAIRequestShape {
  return {
    endpoint: requestEndpoint(req),
    model,
    stream: requestStream(req) || requestAcceptsEventStream(req),
    createdAt: new Date().toISOString()
  }
}

function requestAcceptsEventStream(req: Request): boolean {
  const accept = req.header('accept')
  return typeof accept === 'string' && accept.toLowerCase().includes('text/event-stream')
}

function accountSummaryFromUpstreamAccount(account: UpstreamAccount): AccountSummary {
  return {
    id: account.id,
    systemAccountId: account.systemAccountId,
    ownerSystemAccountId: account.accountOwnerSystemAccountId,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    name: account.name,
    type: account.type,
    credentials: account.credentials,
    status: account.status,
    concurrencyLimit: account.concurrencyLimit,
    currentConcurrency: account.currentConcurrency ?? 0,
    effectiveAvailability: {
      available: account.status === 'active',
      status: account.status === 'active' ? 'available' : 'instance_disabled',
      label: account.status === 'active' ? '可用' : '不可用',
      color: account.status === 'active' ? 'green' : 'red'
    },
    priority: account.priority,
    superPriorityEnabled: account.superPriorityEnabled,
    fallbackEnabled: account.fallbackEnabled,
    clientCompatibility: account.clientCompatibility,
    supportedModels: account.supportedModels,
    modelMappings: account.modelMappings,
    lastSuccessfulTestModel: account.lastSuccessfulTestModel,
    proxyProfileId: account.proxyProfileId,
    proxyProfileUnavailable: account.proxyProfileUnavailable,
    proxyProfileErrorMessage: account.proxyProfileErrorMessage,
    schedulable: true,
    cooldownUntil: account.cooldownUntil,
    lastErrorMessage: account.lastErrorMessage,
    streamFailureCount: account.streamFailureCount,
    streamFailureWindowStartedAt: account.streamFailureWindowStartedAt,
    todayUsage: emptyAccountUsageSummary(),
    usage: emptyAccountUsageSummary(),
    accessType: account.accountAccessType === 'owner' ? 'owner' : 'authorized',
    accountAuthorizationId: account.accountAuthorizationId,
    authorizationExpiresAt: account.accountAuthorizationExpiresAt,
    authorizationQuotaExceeded: account.accountAuthorizationQuotaLimited,
    boundGroupId: account.boundGroupId,
    bindingSystemAccountId: account.bindingSystemAccountId
  }
}

function emptyAccountUsageSummary(): AccountUsageSummary {
  return {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheReadCost: 0,
    totalTokens: 0,
    totalCost: 0
  }
}

function failedProbe(
  account: UpstreamAccount,
  startedAt: number,
  errorCode: string,
  message: string,
  upstreamUrl?: string
): CodexSwitchProbeResult {
  return {
    accountId: account.id,
    accountName: account.name,
    success: false,
    upstreamUrl,
    durationMs: Date.now() - startedAt,
    errorCode,
    message
  }
}

function probeErrorCode(statusCode: number | undefined): string {
  return typeof statusCode === 'number'
    ? `account_test_http_${statusCode}`
    : 'account_test_failed'
}

function probeFailureMessage(result: {
  statusCode?: number
  errorCode?: string
  message?: string
  traceId?: string
}): string {
  const status = typeof result.statusCode === 'number' ? `HTTP ${result.statusCode}` : '无 HTTP 状态'
  const code = result.errorCode ? `，错误码 ${result.errorCode}` : ''
  const trace = result.traceId ? `，测试 traceId ${result.traceId}` : ''
  return `Codex 切号真实账号测试失败，${status}${code}${trace}：${result.message || '未知错误'}`
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}
