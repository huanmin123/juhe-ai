import type { Request } from 'express'

import type { AccountSummary, AccountTestResult, AccountUsageSummary } from '../../domain/types.js'
import type { RecentOpenAIRequestShape } from '../../storage/repositories.js'
import {
  accountDiagnosticRetryTimeoutMs,
  diagnosticAccountTestGatewaySettingsOverride,
  diagnosticAttemptSignal,
  isDiagnosticTimeoutSignal
} from '../accounts/account-diagnostic-retry-policy.js'
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

  try {
    const accountTestService = await import('../accounts/account-test.service.js')
    const model = requestModel(input.req) || accountTestService.preferredSystemAccountTestModel(summary)
    let lastResult: AccountTestResult | undefined
    for (let attemptIndex = 0; attemptIndex < accountDiagnosticRetryTimeoutMs.length; attemptIndex += 1) {
      const timeoutMs = accountDiagnosticRetryTimeoutMs[attemptIndex] ?? accountDiagnosticRetryTimeoutMs[accountDiagnosticRetryTimeoutMs.length - 1]
      const attemptSignal = diagnosticAttemptSignal(input.signal, timeoutMs)
      const result = await accountTestService.testOpenAIAccount(summary, {
        model,
        requestShape: currentRequestShape(input.req, model),
        groupId: input.groupId,
        systemAccountId: input.systemAccountId,
        signal: attemptSignal,
        diagnostics: 'full',
        trafficSource: 'manual_account_test',
        gatewaySettingsOverride: diagnosticAccountTestGatewaySettingsOverride(
          codexSwitchProbeGatewaySettings(input.settings),
          codexSwitchProbeGatewayTimeoutMs(timeoutMs)
        ),
        disableAccountStateMutation: true,
        clientCompatibility: account.clientCompatibility,
        candidateAccount: account
      })
      lastResult = result
      if (result.success || !shouldRetryCodexSwitchProbeSameAccount(attemptSignal, input.signal, attemptIndex)) {
        return codexSwitchProbeResultFromAccountTest(account, result, startedAt)
      }
    }
    return lastResult
      ? codexSwitchProbeResultFromAccountTest(account, lastResult, startedAt)
      : failedProbe(account, startedAt, 'account_test_failed', 'Codex 切号真实账号测试失败：未执行探针')
  } catch (error) {
    return failedProbe(account, startedAt, 'account_test_failed', errorMessage(error))
  }
}

function codexSwitchProbeGatewaySettings(settings: GatewaySettings): Partial<GatewaySettings> {
  return {
    streamCircuitBreakerEnabled: settings.streamCircuitBreakerEnabled
  }
}

function codexSwitchProbeGatewayTimeoutMs(timeoutMs: number): number {
  return Math.max(1, Math.trunc(timeoutMs)) + 1_000
}

function shouldRetryCodexSwitchProbeSameAccount(attemptSignal: AbortSignal, inputSignal: AbortSignal | undefined, attemptIndex: number): boolean {
  return attemptIndex + 1 < accountDiagnosticRetryTimeoutMs.length
    && inputSignal?.aborted !== true
    && attemptSignal.aborted
    && isDiagnosticTimeoutSignal(attemptSignal)
}

function codexSwitchProbeResultFromAccountTest(
  account: UpstreamAccount,
  result: AccountTestResult,
  startedAt: number
): CodexSwitchProbeResult {
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
