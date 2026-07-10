import type { Request } from 'express'

import type { AccountSummary, AccountTestResult, AccountUsageSummary } from '../../../domain/types.js'
import {
  requestModel
} from '../request/metadata.js'
import {
  accountDiagnosticRetryTimeoutMs,
  diagnosticAccountTestGatewaySettingsOverride,
  diagnosticAttemptSignal,
  isDiagnosticTimeoutSignal
} from '../../accounts/account-diagnostic-retry-policy.js'
import { type UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import { recordGatewayUpstreamBucketFailureAsync } from '../runtime/proxy-health.service.js'
import { getRequestLogger } from '../../../shared/request-context.js'
import { requestGatewayDbService } from '../runtime/gateway-db-service-request.js'

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
    signal?: AbortSignal
  }
): Promise<CodexSwitchProbeResult> {
  const startedAt = Date.now()
  if (account.status !== 'active') {
    return failedProbe(account, startedAt, 'account_inactive', `账号状态不是 active：${account.status}`)
  }

  const summary = accountSummaryFromUpstreamAccount(account)

  try {
    const accountTestService = await import('../../accounts/account-test.service.js')
    const model = await accountTestService.resolveAccountTestModelAsync(summary, {
      explicitModel: requestModel(input.req),
      systemAccountId: input.systemAccountId
    })
    let lastResult: AccountTestResult | undefined
    for (let attemptIndex = 0; attemptIndex < accountDiagnosticRetryTimeoutMs.length; attemptIndex += 1) {
      const timeoutMs = accountDiagnosticRetryTimeoutMs[attemptIndex] ?? accountDiagnosticRetryTimeoutMs[accountDiagnosticRetryTimeoutMs.length - 1]
      const attemptSignal = diagnosticAttemptSignal(input.signal, timeoutMs)
      const result = await accountTestService.testOpenAIAccount(summary, {
        model,
        groupId: input.groupId,
        systemAccountId: input.systemAccountId,
        signal: attemptSignal,
        diagnostics: 'full',
        trafficSource: 'manual_account_test',
        gatewaySettingsOverride: diagnosticAccountTestGatewaySettingsOverride(
          undefined,
          codexSwitchProbeGatewayTimeoutMs(timeoutMs)
        ),
        disableAccountStateMutation: true,
        candidateAccount: account,
        findAccountForTest: (accountId, access) => requestGatewayDbService({
          type: 'find_account_for_test',
          accountId,
          access
        }, { timeoutMs: 10_000 }),
        findOpenAIAccountForGroup: (groupId, accountId, systemAccountId, options) => requestGatewayDbService({
          type: 'find_openai_account_for_group',
          groupId,
          accountId,
          systemAccountId,
          includeUnavailable: options?.includeUnavailable,
          ignoreAvailability: options?.ignoreAvailability
        }, { timeoutMs: 10_000 })
      })
      lastResult = result
      if (result.success || !shouldRetryCodexSwitchProbeSameAccount(attemptSignal, input.signal, attemptIndex)) {
        const probeResult = codexSwitchProbeResultFromAccountTest(account, result, startedAt)
        await recordCodexSwitchProbeBucketFailure(account, probeResult, input.signal)
        return probeResult
      }
    }
    const probeResult = lastResult
      ? codexSwitchProbeResultFromAccountTest(account, lastResult, startedAt)
      : failedProbe(account, startedAt, 'account_test_failed', 'Codex 切号真实账号测试失败：未执行探针')
    await recordCodexSwitchProbeBucketFailure(account, probeResult, input.signal)
    return probeResult
  } catch (error) {
    const probeResult = failedProbe(account, startedAt, 'account_test_failed', errorMessage(error))
    await recordCodexSwitchProbeBucketFailure(account, probeResult, input.signal)
    return probeResult
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

async function recordCodexSwitchProbeBucketFailure(
  account: UpstreamAccount,
  result: CodexSwitchProbeResult,
  signal?: AbortSignal
): Promise<void> {
  if (result.success || signal?.aborted) return
  try {
    await recordGatewayUpstreamBucketFailureAsync(account, `Codex 切号探针失败：${result.errorCode ?? 'account_test_failed'}`)
  } catch (error) {
    getRequestLogger().warn({
      event: 'gateway_codex_switch_probe_bucket_failure_record_failed',
      accountId: account.id,
      error: errorMessage(error)
    }, 'Codex 切号探针失败后记录上游桶运行态失败失败')
  }
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
    defaultTestModel: account.defaultTestModel,
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
    cacheWriteTokens: 0,
    cacheWrite1hTokens: 0,
    cacheWriteCost: 0,
    thinkingTokens: 0,
    inputImageTokens: 0,
    outputImageTokens: 0,
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
