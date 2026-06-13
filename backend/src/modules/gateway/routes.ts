import {
  Router,
  type NextFunction,
  type Request,
  type Response,
} from 'express'

import { createTraceId, getRequestLogger, getTraceId } from '../../shared/request-context.js'
import { errorLogFields } from '../../shared/logger.js'
import {
  extractClientIp,
  requestEndpoint
} from './request/metadata.js'
import { buildUsageRequestSnapshot } from './usage/snapshots.js'
import {
  isEffectiveOpenAIStreamRequest
} from './upstream/request.js'
import {
  gatewayStreamClientRetryErrorCode,
  gatewayErrorPayload,
  sendGatewayErrorResponse,
  shouldHandleOpenAIUpstreamResponseAsStream,
  writeGatewayStreamFailureEvent
} from './response/responses.js'
import { createAuditCapture, responseHeadersToObject } from './audit/capture.service.js'
import {
  type UpstreamAccount
} from './protocols/openai-v1/route-helpers.js'
import {
  buildDiagnosticUpstreamError
} from './upstream/error-helpers.js'
import {
  persistOpenAICodexHeadersIfNeeded
} from './runtime/account-effects.js'
import {
  probeCodexSwitchCandidateAccount,
  type CodexSwitchProbeResult
} from './client-profiles/codex-switch-probe.js'
import {
  finalizeHandledUpstreamResponse,
  handleNonStreamUpstreamResponse,
  handleStreamUpstreamResponse,
  type StreamServerRetryReason
} from './response/finalization.js'
import { rememberCodexTurnStreamFailure } from './client-profiles/codex-turn-retry.service.js'
import { sendGatewayFailureResponse } from './response/failure-response.js'
import { handleUpstreamRequestError } from './response/failure-dispatch.js'
import { handleGatewayRequestKnownErrorResponse } from './request/error-response.js'
import {
  prepareOpenAIGatewayDispatchContext,
  prepareApiKeyGroupFallbackDispatchContext,
  type OpenAIGatewayDispatchContext,
  type OpenAIGatewayRequestIdentity
} from './request/preflight.js'
import {
  fetchFirstAvailableUpstream,
  UpstreamAttemptError
} from './dispatch/upstream-dispatch.js'
import type { ResponseInspectionDecision } from './response/inspection.js'
import type { GatewaySettings } from './policy/account-error-policy.service.js'
import { OpenAIOAuthCodexAdapterError } from './adapters/gpt-codex/oauth-adapter.js'
import { recordClientIpErrorCircuitSample } from './runtime/client-ip-error-circuit.service.js'
import {
  confirmClientIpAccountAvoidanceAfterFinalFailure,
  transferClientIpAccountPendingFailures
} from './runtime/client-ip-account-avoidance.service.js'
import type { GatewayFailureUsageContext } from './usage/records.js'
import { isGatewayForcedDownstreamClose } from './upstream/body.js'
import {
  normalizeOpenAIGatewayTrafficSource,
  type OpenAIGatewayTrafficSource
} from './usage/traffic-source.js'
import { resolveOpenAIGatewayRequestLane } from './protocols/openai-v1/request-lane.js'

export const openAIGatewayRouter = Router()

export type { OpenAIGatewayRequestIdentity } from './request/preflight.js'

export interface OpenAIGatewayHandleOptions {
  identity?: OpenAIGatewayRequestIdentity
  candidateAccounts?: UpstreamAccount[]
  disableSessionAffinity?: boolean
  exposeUpstreamDiagnostics?: boolean
  trafficSource?: OpenAIGatewayTrafficSource
  settingsOverride?: Partial<GatewaySettings>
  disableAccountStateMutation?: boolean
}

export function handleGatewayDbServiceUnavailable(error: unknown, req: Request, res: Response, next: NextFunction): void {
  const message = dbServiceUnavailableMessage(error)
  if (!message || res.headersSent) {
    next(error)
    return
  }

  getRequestLogger().error(errorLogFields(error, {
    event: 'gateway_db_service_unavailable',
    endpoint: `${req.method.toUpperCase()} ${requestEndpoint(req)}`
  }), '网关 DB service 不可用')

  sendGatewayErrorResponse(res, 503, gatewayErrorPayload(message, 'service_unavailable'))
}

function dbServiceUnavailableMessage(error: unknown): string | undefined {
  if (!(error instanceof Error)) {
    return undefined
  }
  return /^本地数据库服务(暂时不可用|未就绪|请求超时|已退出)/.test(error.message)
    ? error.message
    : undefined
}

openAIGatewayRouter.all('*', async (req, res, next) => {
  try {
    await handleOpenAIGatewayRequest(req, res)
  } catch (error) {
    handleGatewayDbServiceUnavailable(error, req, res, next)
  }
})

export async function handleOpenAIGatewayRequest(
  req: Request,
  res: Response,
  options: OpenAIGatewayHandleOptions = {}
): Promise<void> {
  const startedAt = Date.now()
  const abortController = new AbortController()
  const traceId = getTraceId() ?? createTraceId()
  const clientIp = extractClientIp(req)
  const endpoint = requestEndpoint(req)
  const requestLane = resolveOpenAIGatewayRequestLane(req)
  const trafficSource = normalizeOpenAIGatewayTrafficSource(options.trafficSource)
  const requestSnapshot = buildUsageRequestSnapshot(req, traceId, clientIp)
  const auditCapture = createAuditCapture({
    req,
    traceId,
    clientIp,
    startedAtMs: startedAt,
    trafficSource,
    captureMode: trafficSource === 'cooldown_retest' ? 'metadata_only' : 'default'
  })
  req.once('aborted', () => {
    auditCapture.markClientAborted()
    abortController.abort()
  })
  res.once('close', () => {
    if (!res.writableEnded && !isGatewayForcedDownstreamClose(res)) {
      auditCapture.markClientAborted()
      abortController.abort()
    }
  })

  const preflight = await prepareOpenAIGatewayDispatchContext({
    req,
    res,
    auditCapture,
    options: { ...options, trafficSource, requestLane },
    startedAt,
    traceId,
    clientIp,
    endpoint,
    requestSnapshot,
    signal: abortController.signal
  })
  if (!preflight) {
    return
  }
  let currentPreflight = preflight
  let releaseClientIpSlot = attachClientIpSlotRelease(res, currentPreflight)
  let streamServerRetryExcludedAccountIds = new Set<string>()
  let streamServerRetryCount = 0
  let maxStreamServerRetryCount = streamServerRetryLimit(currentPreflight.accounts)
  const exhaustedAccountIds = new Set<string>()
  const nonStreamResponseStartedFailedAccountIds = new Set<string>()
  const switchToFallbackGroup = async (
    reason: string,
    input: { allowCandidateWrap?: boolean } = {}
  ): Promise<'none' | 'switched' | 'completed'> => {
    const gatewayUsageContext = currentPreflight.usageContext
    const fallback = await prepareApiKeyGroupFallbackDispatchContext({
      req,
      res,
      auditCapture,
      options: {
        ...options,
        trafficSource,
        requestLane: currentPreflight.requestLane
      },
      startedAt,
      traceId,
      clientIp,
      endpoint,
      requestSnapshot,
      signal: abortController.signal,
      reason,
      apiKeyRecord: currentPreflight.apiKeyRecord,
      systemAccountId: gatewayUsageContext.systemAccountId,
      apiKeyId: gatewayUsageContext.apiKeyId,
      groupId: gatewayUsageContext.groupId,
      trafficSource: gatewayUsageContext.trafficSource,
      requestLane: currentPreflight.requestLane,
      excludedAccountIds: exhaustedAccountIds,
      allowCandidateWrap: input.allowCandidateWrap
    })
    if (!fallback.attempted) {
      return 'none'
    }
    if (!fallback.context) {
      return 'completed'
    }
    transferClientIpAccountPendingFailures(
      currentPreflight.clientIpAccountAvoidanceTracker,
      fallback.context.clientIpAccountAvoidanceTracker
    )
    releaseClientIpSlot()
    currentPreflight = fallback.context
    releaseClientIpSlot = attachClientIpSlotRelease(res, currentPreflight)
    streamServerRetryExcludedAccountIds = new Set<string>()
    streamServerRetryCount = 0
    maxStreamServerRetryCount = streamServerRetryLimit(currentPreflight.accounts)
    return 'switched'
  }

  try {
    while (true) {
      const {
        activeGatewaySettings,
        usageContext: gatewayUsageContext,
        accounts,
        sessionAffinityKey,
        clientStrategy,
        clientIpAccountAvoidanceTracker,
        responseInspectionPolicies,
        codexTurnAccountAvoidanceApplied,
        codexTurnAvoidedAccountIds
      } = currentPreflight
      let dispatchAccounts = streamRetryDispatchAccounts(accounts, streamServerRetryExcludedAccountIds)
      if (dispatchAccounts.length === 0) {
        for (const accountId of streamServerRetryExcludedAccountIds) {
          exhaustedAccountIds.add(accountId)
        }
        const fallbackSwitch = await switchToFallbackGroup('upstream_accounts_exhausted', { allowCandidateWrap: true })
        if (fallbackSwitch === 'completed') {
          return
        }
        if (fallbackSwitch === 'switched') {
          continue
        }
        throw new UpstreamAttemptError('没有可用的上游账户')
      }
      if (codexTurnAccountAvoidanceApplied) {
        const probeSelection = await selectCodexProbeVerifiedDispatchAccount({
          accounts: dispatchAccounts,
          avoidedAccountIds: new Set(codexTurnAvoidedAccountIds ?? []),
          req,
          settings: activeGatewaySettings,
          systemAccountId: gatewayUsageContext.systemAccountId,
          groupId: gatewayUsageContext.groupId,
          auditCapture,
          signal: abortController.signal
        })
        for (const probe of probeSelection.probes) {
          if (!probe.success) {
            streamServerRetryExcludedAccountIds.add(probe.accountId)
          }
        }
        auditCapture.addGatewayMetadata({
          label: 'codex_switch_probe',
          metadata: {
            selectedAccountId: probeSelection.account?.id,
            selectedAccountName: probeSelection.account?.name,
            probeCount: probeSelection.probes.length,
            probes: probeSelection.probes.map(codexSwitchProbeAuditMetadata)
          }
        })
        if (!probeSelection.account) {
          for (const accountId of streamServerRetryExcludedAccountIds) {
            exhaustedAccountIds.add(accountId)
          }
          const fallbackSwitch = await switchToFallbackGroup('codex_switch_probe_failed', { allowCandidateWrap: true })
          if (fallbackSwitch === 'completed') {
            return
          }
          if (fallbackSwitch === 'switched') {
            continue
          }
          sendCodexSwitchProbeFailedResponse({
            req,
            res,
            auditCapture,
            usageContext: gatewayUsageContext,
            startedAt,
            probes: probeSelection.probes
          })
          return
        }
        dispatchAccounts = [probeSelection.account]
      }
      let upstreamResult: Awaited<ReturnType<typeof fetchFirstAvailableUpstream>>
      try {
        upstreamResult = await fetchFirstAvailableUpstream(
          req,
          dispatchAccounts,
          activeGatewaySettings,
          gatewayUsageContext,
          auditCapture,
          sessionAffinityKey,
          abortController.signal,
          clientIpAccountAvoidanceTracker,
          currentPreflight.requestLane,
          currentPreflight.groupSchedulingPolicy,
          options.disableAccountStateMutation !== true
        )
      } catch (error) {
        if (error instanceof UpstreamAttemptError) {
          for (const accountId of nonStreamResponseStartedFailedAccountIds) {
            exhaustedAccountIds.add(accountId)
          }
          for (const accountId of error.failedAccountIds) {
            exhaustedAccountIds.add(accountId)
            if (codexTurnAccountAvoidanceApplied) {
              streamServerRetryExcludedAccountIds.add(accountId)
            }
          }
          if (codexTurnAccountAvoidanceApplied) {
            continue
          }
          const fallbackSwitch = await switchToFallbackGroup('upstream_accounts_exhausted', { allowCandidateWrap: true })
          if (fallbackSwitch === 'completed') {
            return
          }
          if (fallbackSwitch === 'switched') {
            continue
          }
        }
        throw error
      }
      const { account, response: upstreamResponse, upstreamUrl, auditAttemptId, releaseConcurrency, markFirstOutput } = upstreamResult

      try {
        const responseHandlingStartedAt = Date.now()
        const contentType = upstreamResponse.headers.get('content-type') ?? ''
        const shouldHandleAsStream = shouldHandleOpenAIUpstreamResponseAsStream({
          contentType,
          streamRequest: isEffectiveOpenAIStreamRequest(req, account)
        })
        persistOpenAICodexHeadersIfNeeded(account, upstreamResponse.headers, gatewayUsageContext.trafficSource)

        let handledResponse: Awaited<ReturnType<typeof handleStreamUpstreamResponse>>
        if (shouldHandleAsStream) {
          handledResponse = await handleStreamUpstreamResponse({
            req,
            res,
            account,
            upstreamResponse,
            upstreamUrl,
            auditAttemptId,
            auditCapture,
            settings: activeGatewaySettings,
            usageContext: gatewayUsageContext,
            startedAt,
            signal: abortController.signal,
            sessionAffinityKey,
            clientStrategy,
            responseInspectionPolicies,
            markFirstOutput,
            clientIpAccountAvoidanceTracker,
            accountStateMutationEnabled: options.disableAccountStateMutation !== true,
            codexTurnAccountAvoidanceApplied
          })
        } else {
          try {
            handledResponse = await handleNonStreamUpstreamResponse({
              req,
              res,
              account,
              upstreamResponse,
              upstreamUrl,
              auditAttemptId,
              auditCapture,
              settings: activeGatewaySettings,
              usageContext: gatewayUsageContext,
              startedAt,
              signal: abortController.signal,
              sessionAffinityKey,
              responseInspectionPolicies,
              markFirstOutput,
              clientIpAccountAvoidanceTracker,
              accountStateMutationEnabled: options.disableAccountStateMutation !== true
            })
          } catch (error) {
            if (res.headersSent || res.writableEnded || res.destroyed) {
              throw error
            }
            const requestErrorResult = await handleUpstreamRequestError({
              req,
              usageContext: gatewayUsageContext,
              auditCapture,
              auditAttemptId,
              account,
              upstreamUrl,
              attemptStartedAt: responseHandlingStartedAt,
              attemptIndex: 0,
              auditAttemptIndex: 0,
              settings: activeGatewaySettings,
              sessionAffinityKey,
              signal: abortController.signal,
              lastAttempt: { accountId: account.id, accountName: account.name, upstreamUrl, status: upstreamResponse.status },
              failedProxyDispatchKeys: new Map(),
              error,
              clientIpAccountAvoidanceTracker,
              accountStateMutationEnabled: options.disableAccountStateMutation !== true
            })
            nonStreamResponseStartedFailedAccountIds.add(account.id)
            if (requestErrorResult.action === 'skip_account') {
              streamServerRetryExcludedAccountIds.add(account.id)
              continue
            }
            throw error
          }
        }
        if (handledResponse.alreadyFinalized) {
          return
        }
        if (handledResponse.retryUpstream) {
          streamServerRetryCount += 1
          if (
            handledResponse.excludeCurrentAccount
            || (handledResponse.responseInspection && shouldExcludeCurrentAccountForStreamRetry(handledResponse.responseInspection))
          ) {
            streamServerRetryExcludedAccountIds.add(account.id)
          }
          const effectiveMaxStreamServerRetryCount = handledResponse.excludeCurrentAccount
            ? Math.max(1, accounts.length)
            : maxStreamServerRetryCount
          auditCapture.addGatewayMetadata({
            label: 'stream_server_retry_dispatch',
            metadata: {
              retryReason: handledResponse.retryReason,
              retryCount: streamServerRetryCount,
              maxRetryCount: effectiveMaxStreamServerRetryCount,
              accountId: account.id,
              excludedAccountIds: [...streamServerRetryExcludedAccountIds],
              excludeCurrentAccount: handledResponse.excludeCurrentAccount,
              policyId: handledResponse.responseInspection?.policyId,
              policyName: handledResponse.responseInspection?.policyName,
              accountSwitch: handledResponse.responseInspection?.accountSwitch,
              errorCode: handledResponse.errorCode
            }
          })
          if (
            streamServerRetryCount > effectiveMaxStreamServerRetryCount
            || streamRetryDispatchAccounts(accounts, streamServerRetryExcludedAccountIds).length === 0
          ) {
            for (const accountId of streamServerRetryExcludedAccountIds) {
              exhaustedAccountIds.add(accountId)
            }
            const fallbackReason = streamServerRetryFallbackReason(handledResponse.retryReason)
            const fallbackSwitch = await switchToFallbackGroup(fallbackReason, { allowCandidateWrap: true })
            if (fallbackSwitch !== 'none') {
              if (fallbackSwitch === 'completed') {
                return
              }
              continue
            }
            confirmCurrentClientIpAccountAvoidanceAfterFinalFailure(currentPreflight, auditCapture, 'stream_server_retry_exhausted')
            sendStreamServerRetryExhaustedResponse({
              req,
              res,
              auditCapture,
              usageContext: gatewayUsageContext,
              startedAt,
              retryReason: handledResponse.retryReason,
              decision: handledResponse.responseInspection,
              message: handledResponse.message,
              errorCode: handledResponse.errorCode,
              uncommittedResponseBody: handledResponse.uncommittedResponseBody,
              accountId: account.id,
              clientStrategy
            })
            return
          }
          continue
        }
        finalizeHandledUpstreamResponse({
          req,
          res,
          account,
          upstreamResponse,
          upstreamUrl,
          auditAttemptId,
          auditCapture,
          settings: activeGatewaySettings,
          usageContext: gatewayUsageContext,
          startedAt,
          signal: abortController.signal,
          result: handledResponse,
          clientIpAccountAvoidanceTracker,
          accountStateMutationEnabled: options.disableAccountStateMutation !== true
        })
        return
      } finally {
        releaseConcurrency()
      }
    }
  } catch (error) {
    if (error instanceof UpstreamAttemptError) {
      for (const accountId of nonStreamResponseStartedFailedAccountIds) {
        error.failedAccountIds.push(accountId)
      }
    }
    const gatewayUsageContext = currentPreflight.usageContext
    recordKnownClientIpRequestError(error, gatewayUsageContext, auditCapture)
    if (handleGatewayRequestKnownErrorResponse({
      res,
      auditCapture,
      error,
      signal: abortController.signal
    })) {
      return
    }
    const lastAttempt = error instanceof UpstreamAttemptError ? error.lastAttempt : undefined
    const message = error instanceof Error ? error.message : '没有可用的上游账户'
    const diagnosticError = options.exposeUpstreamDiagnostics
      ? buildDiagnosticUpstreamError(lastAttempt, message)
      : undefined
    const statusCode = diagnosticError?.statusCode ?? 503
    const responsePayload = diagnosticError?.payload ?? gatewayErrorPayload('没有可用的上游账户', 'service_unavailable')
    confirmCurrentClientIpAccountAvoidanceAfterFinalFailure(currentPreflight, auditCapture, 'gateway_failure_response')
    sendGatewayFailureResponse({
      req,
      res,
      auditCapture,
      usageContext: gatewayUsageContext,
      startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'upstream_failed',
        errorPhase: 'dispatch',
        errorCode: 'service_unavailable',
        errorMessage: diagnosticError?.errorMessage ?? message
      },
      recordUsage: !lastAttempt,
      usageErrorMessage: message
    })
  } finally {
    releaseClientIpSlot()
  }
}

function confirmCurrentClientIpAccountAvoidanceAfterFinalFailure(
  preflight: OpenAIGatewayDispatchContext,
  auditCapture: ReturnType<typeof createAuditCapture>,
  reason: string
): void {
  const result = confirmClientIpAccountAvoidanceAfterFinalFailure(
    preflight.clientIpAccountAvoidanceTracker,
    preflight.activeGatewaySettings
  )
  if (result.confirmedAccountIds.length === 0) {
    return
  }
  getRequestLogger().warn({
    event: 'gateway_client_ip_account_failure_confirmed_after_final_failure',
    reason,
    confirmedAccountIds: result.confirmedAccountIds,
    systemAccountId: preflight.usageContext.systemAccountId,
    apiKeyId: preflight.usageContext.apiKeyId,
    groupId: preflight.usageContext.groupId,
    clientIp: preflight.usageContext.clientIp
  }, '请求失败已返回客户端，客户端 IP 级账号回避状态已立即确认')
  auditCapture.addGatewayMetadata({
    label: 'client_ip_account_avoidance_update',
    metadata: {
      reason,
      confirmedAccountIds: result.confirmedAccountIds
    }
  })
}

function once(callback: () => void): () => void {
  let called = false
  return () => {
    if (called) return
    called = true
    callback()
  }
}

function attachClientIpSlotRelease(res: Response, preflight: OpenAIGatewayDispatchContext): () => void {
  const releaseClientIpSlot = once(preflight.releaseClientIpConcurrency)
  res.once('finish', releaseClientIpSlot)
  res.once('close', releaseClientIpSlot)
  return releaseClientIpSlot
}

function streamServerRetryLimit(accounts: UpstreamAccount[]): number {
  return Math.max(1, Math.min(3, accounts.length))
}

function streamServerRetryFallbackReason(retryReason: StreamServerRetryReason): string {
  return retryReason === 'response_inspection'
    ? 'response_inspection_server_retry_exhausted'
    : 'stream_server_retry_exhausted'
}

function streamRetryDispatchAccounts(accounts: UpstreamAccount[], excludedAccountIds: Set<string>): UpstreamAccount[] {
  if (excludedAccountIds.size === 0) {
    return accounts
  }
  return accounts.filter((account) => !excludedAccountIds.has(account.id))
}

async function selectCodexProbeVerifiedDispatchAccount(input: {
  accounts: UpstreamAccount[]
  avoidedAccountIds: Set<string>
  req: Request
  settings: GatewaySettings
  systemAccountId: string
  groupId: string
  auditCapture: ReturnType<typeof createAuditCapture>
  signal?: AbortSignal
}): Promise<{ account?: UpstreamAccount; probes: CodexSwitchProbeResult[] }> {
  const probes: CodexSwitchProbeResult[] = []
  const candidates = input.accounts.filter((account) => !input.avoidedAccountIds.has(account.id))
  for (const account of candidates) {
    const probe = await probeCodexSwitchCandidateAccount(account, {
      req: input.req,
      systemAccountId: input.systemAccountId,
      groupId: input.groupId,
      settings: input.settings,
      signal: input.signal
    })
    probes.push(probe)
    if (probe.success) {
      return { account, probes }
    }
  }
  return { probes }
}

function codexSwitchProbeAuditMetadata(probe: CodexSwitchProbeResult): Record<string, unknown> {
  return {
    accountId: probe.accountId,
    accountName: probe.accountName,
    success: probe.success,
    statusCode: probe.statusCode,
    durationMs: probe.durationMs,
    errorCode: probe.errorCode,
    traceId: probe.traceId,
    model: probe.model,
    message: truncateProbeMessage(probe.message)
  }
}

function shouldExcludeCurrentAccountForStreamRetry(decision: ResponseInspectionDecision): boolean {
  return decision.accountSwitch === 'request_next_account'
    || decision.accountSwitch === 'avoid_account_ttl'
    || decision.accountSwitch === 'avoid_upstream_bucket_ttl'
    || decision.accountState === 'runtime_avoidance'
}

function sendStreamServerRetryExhaustedResponse(input: {
  req: Request
  res: Response
  auditCapture: ReturnType<typeof createAuditCapture>
  usageContext: GatewayFailureUsageContext
  startedAt: number
  retryReason: StreamServerRetryReason
  decision?: ResponseInspectionDecision
  message: string
  errorCode?: string
  uncommittedResponseBody?: Buffer
  accountId?: string
  clientStrategy?: OpenAIGatewayDispatchContext['clientStrategy']
}): void {
  const message = input.message || '服务端流式重试未找到可用账号'
  if (input.retryReason === 'pre_commit_stream_failure' || input.retryReason === 'codex_pre_commit_stream_failure') {
    sendPreCommitStreamRetryExhaustedResponse({
      req: input.req,
      res: input.res,
      auditCapture: input.auditCapture,
      usageContext: input.usageContext,
      startedAt: input.startedAt,
      retryReason: input.retryReason,
      message,
      errorCode: input.errorCode,
      uncommittedResponseBody: input.uncommittedResponseBody,
      accountId: input.accountId,
      clientStrategy: input.clientStrategy
    })
    return
  }
  const responsePayload = gatewayErrorPayload(message, 'service_unavailable', 'stream_server_retry_exhausted')
  input.auditCapture.addGatewayMetadata({
    label: 'stream_server_retry_exhausted',
    metadata: {
      retryReason: input.retryReason,
      policyId: input.decision?.policyId,
      policyName: input.decision?.policyName,
      accountSwitch: input.decision?.accountSwitch,
      retryEnabled: input.decision?.retryEnabled,
      matchedField: input.decision?.matchedField,
      matchedValue: input.decision?.matchedValue
    }
  })
  sendGatewayFailureResponse({
    req: input.req,
    res: input.res,
    auditCapture: input.auditCapture,
    usageContext: input.usageContext,
    startedAt: input.startedAt,
    statusCode: 503,
    responsePayload,
    audit: {
      outcome: 'upstream_failed',
      errorPhase: 'dispatch',
      errorCode: 'stream_server_retry_exhausted',
      errorMessage: message
    },
    recordUsage: false,
    usageErrorMessage: message
  })
}

function sendPreCommitStreamRetryExhaustedResponse(input: {
  req: Request
  res: Response
  auditCapture: ReturnType<typeof createAuditCapture>
  usageContext: GatewayFailureUsageContext
  startedAt: number
  retryReason: 'pre_commit_stream_failure' | 'codex_pre_commit_stream_failure'
  message: string
  errorCode?: string
  uncommittedResponseBody?: Buffer
  accountId?: string
  clientStrategy?: OpenAIGatewayDispatchContext['clientStrategy']
}): void {
  const failureEvent = writeGatewayStreamFailureEvent(input.res, input.message, input.errorCode)
  const responseBody = input.uncommittedResponseBody
    ? Buffer.concat([input.uncommittedResponseBody, failureEvent ?? Buffer.alloc(0)])
    : failureEvent
  input.auditCapture.addGatewayMetadata({
    label: 'stream_server_retry_exhausted',
    metadata: {
      retryReason: input.retryReason,
      errorCode: input.errorCode,
      responseMode: input.errorCode === gatewayStreamClientRetryErrorCode ? 'codex_retryable_sse' : 'openai_stream_failure_sse'
    }
  })
  rememberCodexTurnFailureWhenClientRetryIsVisible(input)
  if (!input.res.headersSent) {
    input.res.status(200)
    input.res.setHeader('content-type', 'text/event-stream; charset=utf-8')
    input.res.setHeader('cache-control', 'no-cache, no-transform')
    input.res.setHeader('x-accel-buffering', 'no')
  }
  if (!input.res.writableEnded && !input.res.destroyed && input.uncommittedResponseBody?.length) {
    input.res.write(input.uncommittedResponseBody)
  }
  if (!input.res.writableEnded && !input.res.destroyed && failureEvent) {
    input.res.write(failureEvent)
  }
  if (!input.res.writableEnded && !input.res.destroyed) {
    input.res.end()
  }
  input.auditCapture.finalize({
    outcome: 'stream_failed',
    success: false,
    statusCode: 200,
    responseHeaders: responseHeadersToObject(input.res),
    responseBody,
    responsePartType: 'gateway_response',
    errorPhase: 'stream',
    errorCode: input.errorCode,
    errorMessage: input.message,
    accountId: input.accountId
  })
}

function rememberCodexTurnFailureWhenClientRetryIsVisible(input: {
  auditCapture: ReturnType<typeof createAuditCapture>
  clientStrategy?: OpenAIGatewayDispatchContext['clientStrategy']
  accountId?: string
  errorCode?: string
  message: string
}): void {
  if (
    input.errorCode !== gatewayStreamClientRetryErrorCode
    || !input.accountId
    || input.clientStrategy?.allowCodexTurnAccountAvoidance !== true
  ) {
    return
  }
  const codexTurnFailure = rememberCodexTurnStreamFailure(input.clientStrategy, input.accountId, {
    errorCode: input.errorCode,
    message: input.message
  })
  if (!codexTurnFailure) {
    return
  }
  input.auditCapture.addGatewayMetadata({
    label: 'codex_turn_stream_failure',
    metadata: {
      stateKey: codexTurnFailure.stateKey,
      failureCount: codexTurnFailure.failureCount,
      failedAccountIds: codexTurnFailure.failedAccountIds,
      accountId: input.accountId
    }
  })
}

function sendCodexSwitchProbeFailedResponse(input: {
  req: Request
  res: Response
  auditCapture: ReturnType<typeof createAuditCapture>
  usageContext: GatewayFailureUsageContext
  startedAt: number
  probes: CodexSwitchProbeResult[]
}): void {
  const message = codexSwitchProbeFailedMessage(input.probes)
  const failureEvent = writeGatewayStreamFailureEvent(input.res, message, 'codex_switch_probe_failed')
  input.auditCapture.addGatewayMetadata({
    label: 'codex_switch_probe_failed',
    metadata: {
      probeCount: input.probes.length,
      probes: input.probes.map(codexSwitchProbeAuditMetadata)
    }
  })
  if (!input.res.headersSent) {
    input.res.status(200)
    input.res.setHeader('content-type', 'text/event-stream; charset=utf-8')
    input.res.setHeader('cache-control', 'no-cache, no-transform')
    input.res.setHeader('x-accel-buffering', 'no')
  }
  if (!input.res.writableEnded && !input.res.destroyed && failureEvent) {
    input.res.write(failureEvent)
  }
  if (!input.res.writableEnded && !input.res.destroyed) {
    input.res.end()
  }
  input.auditCapture.finalize({
    outcome: 'stream_failed',
    success: false,
    statusCode: 200,
    responseHeaders: responseHeadersToObject(input.res),
    responseBody: failureEvent,
    responsePartType: 'gateway_response',
    errorPhase: 'stream',
    errorCode: 'codex_switch_probe_failed',
    errorMessage: message
  })
}

function codexSwitchProbeFailedMessage(probes: CodexSwitchProbeResult[]): string {
  if (probes.length === 0) {
    return 'Codex 切号失败：没有可探测的备用账号'
  }
  const summaries = probes
    .slice(0, 5)
    .map((probe) => `${probe.accountName || probe.accountId}: ${probe.errorCode ?? probe.statusCode ?? 'probe_failed'} ${probe.message}`)
  const suffix = probes.length > 5 ? `；另有 ${probes.length - 5} 个账号探针失败` : ''
  return `Codex 切号失败：所有备用账号探针均未通过。${summaries.join('；')}${suffix}`
}

function truncateProbeMessage(message: string): string {
  return message.length > 300 ? `${message.slice(0, 300)}...` : message
}

function recordKnownClientIpRequestError(
  error: unknown,
  usageContext: GatewayFailureUsageContext,
  auditCapture: ReturnType<typeof createAuditCapture>
): void {
  const sample = clientIpRequestErrorSample(error)
  if (!sample) {
    return
  }
  const result = recordClientIpErrorCircuitSample({
    systemAccountId: usageContext.systemAccountId,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    clientIp: usageContext.clientIp,
    endpoint: usageContext.endpoint,
    reason: sample.reason,
    signature: sample.signature
  })
  if (!result.blocked) {
    return
  }
  getRequestLogger().warn({
    event: 'gateway_client_ip_error_circuit_opened',
    reason: sample.reason,
    retryAfterSeconds: result.retryAfterSeconds,
    failureCount: result.failureCount,
    systemAccountId: usageContext.systemAccountId,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    clientIp: usageContext.clientIp
  }, '客户端 IP 级错误熔断已打开')
  auditCapture.addGatewayMetadata({
    label: 'client_ip_error_circuit',
    metadata: {
      opened: true,
      reason: sample.reason,
      retryAfterSeconds: result.retryAfterSeconds,
      failureCount: result.failureCount
    }
  })
}

function clientIpRequestErrorSample(error: unknown): { reason: 'adapter_request_validation'; signature: string } | undefined {
  if (error instanceof OpenAIOAuthCodexAdapterError) {
    return {
      reason: 'adapter_request_validation',
      signature: [error.type, error.code].filter(Boolean).join('|') || error.message
    }
  }
  return undefined
}
