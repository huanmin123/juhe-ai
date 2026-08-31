import { Router, type NextFunction, type Request, type Response } from 'express'
import { z } from 'zod'

import { badRequest, ok, sendNotFound } from '../../shared/http.js'
import { runtimeConfig } from '../../config/runtime.js'
import { attachDownstreamResponseErrorBoundary } from '../../shared/downstream-response-error-boundary.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { mainDatabaseRuntimeInfo } from '../../storage/database.js'
import { manageableSystemAccountId } from '../../storage/access-scope.js'
import {
  getModelQualityPolicyAsync,
  listModelQualitySchedulesAsync
} from '../../storage/model-quality.repository.js'
import { activateModelTokenInterceptBaselineAsync } from '../../storage/model-trust.repository.js'
import { requestStatsWriter } from '../background/background-stats-writer.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { requestDbService } from '../db-service/db-service-ipc.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { diagnosticTaskBusyMessage, diagnosticTaskRetryAfterSeconds, tryAcquireDiagnosticTaskSlot } from '../diagnostics/diagnostic-task-limiter.js'
import {
  getModelCheckOptions,
  listModelCheckAccountOptions,
  getModelCheckRun,
  listModelCheckRunPage,
  ModelCheckRequestError,
  runModelCheck,
  type ModelCheckProgressEvent
} from './model-checks.service.js'
import {
  activeModelCheckConflictMessage,
  activeModelCheckRetryAfterSeconds,
  getActiveModelCheckRun,
  finishActiveModelCheckRun,
  stopActiveModelCheckRun,
  tryStartActiveModelCheckRun,
  updateActiveModelCheckRun
} from './model-checks-active-runs.js'

export const modelChecksRouter = Router()
export const modelCheckStreamHeartbeatMs = 10_000

const modelCheckRunSchema = z.object({
  targetType: z.literal('account', {
    errorMap: () => ({ message: '模型检测目标只能选择 AI 账户' })
  }),
  targetId: z.string().trim().min(1, '检测目标不能为空'),
  model: z.string({ invalid_type_error: '模型必须使用完整模型 ID' }).trim().min(1, '模型必须使用完整模型 ID'),
  profile: z.enum(['quick', 'full']).optional(),
  trustedComparison: z.boolean().optional(),
  trustedComparisonAccountId: z.string().trim().optional()
}).strict()

const modelQualityPolicySchema = z.object({
  expectedRevision: z.number().int().min(0),
  profile: z.enum(['quick', 'full']).optional(),
  manualEnforcementEnabled: z.boolean().optional(),
  penaltyThreshold: z.number().int().min(40).max(100).optional(),
  penaltyAction: z.enum(['disable', 'fallback', 'quality_isolate']).optional(),
  recoveryIntervalMinutes: z.number().int().min(10).max(10080).optional()
}).strict().refine((value) => Object.keys(value).some((key) => key !== 'expectedRevision'), {
  message: '模型质量检测配置没有变化'
})

const modelQualityScheduleSchema = z.object({
  accountId: z.string().trim().min(1),
  model: z.string().trim().min(1).max(200),
  intervalMinutes: z.number().int().min(10).max(10080),
  profile: z.enum(['quick', 'full']),
  penaltyThreshold: z.number().int().min(40).max(100),
  penaltyAction: z.enum(['disable', 'fallback', 'quality_isolate']),
  recoveryIntervalMinutes: z.number().int().min(10).max(10080),
  enabled: z.boolean().optional()
}).strict()

const modelQualitySchedulePatchSchema = z.object({
  expectedRevision: z.number().int().min(1),
  model: z.string().trim().min(1).max(200).optional(),
  intervalMinutes: z.number().int().min(10).max(10080).optional(),
  profile: z.enum(['quick', 'full']).optional(),
  penaltyThreshold: z.number().int().min(40).max(100).optional(),
  penaltyAction: z.enum(['disable', 'fallback', 'quality_isolate']).optional(),
  recoveryIntervalMinutes: z.number().int().min(10).max(10080).optional(),
  enabled: z.boolean().optional()
}).strict().refine((value) => Object.keys(value).some((key) => key !== 'expectedRevision'), {
  message: '定时检查配置没有变化'
})

const tokenInterceptBaselineActivationSchema = z.object({
  cohortKeyHmac: z.string().trim().regex(/^hmac-sha256-v1:[a-f0-9]{64}$/i, 'cohort key 格式无效'),
  requestedModel: z.string().trim().min(1).max(200),
  tokenizerVersion: z.string().trim().min(1).max(200),
  probeSetVersion: z.string().trim().min(1).max(200),
  baselineVersion: z.number().int().positive(),
  strongThresholdIntercept: z.number().finite().nonnegative(),
  calibrationNote: z.string().trim().min(1).max(500)
}).strict()

modelChecksRouter.post('/token-intercept-baselines/activate', requireAdmin, async (req, res, next) => {
  const parsed = tokenInterceptBaselineActivationSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '固定截距基线激活参数无效'))
    return
  }
  try {
    if (runtimeConfig.databaseDriver === 'postgres' || !mainDatabaseRuntimeInfo('stats').queryOnly) {
      await activateModelTokenInterceptBaselineAsync(parsed.data)
    } else {
      await requestStatsWriter({ type: 'activate_model_token_intercept_baseline', input: parsed.data }, 120_000)
    }
    res.json(ok({ activated: true, baselineVersion: parsed.data.baselineVersion }))
  } catch (error) {
    const message = error instanceof Error ? error.message : '固定截距基线激活失败'
    if (message.startsWith('固定截距')) {
      res.status(409).json({ message })
      return
    }
    next(error)
  }
})

modelChecksRouter.get('/options', (req, res, next) => {
  try {
    res.json(ok(getModelCheckOptions(getRequestAccessScope(req.query.systemAccountId))))
  } catch (error) {
    next(error)
  }
})

modelChecksRouter.get('/quality-policy', async (req, res, next) => {
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const systemAccountId = requireModelQualitySystemAccountId(access)
    res.json(ok(await getModelQualityPolicyAsync(systemAccountId)))
  } catch (error) {
    handleModelQualityRouteError(error, res, next)
  }
})

modelChecksRouter.patch('/quality-policy', async (req, res, next) => {
  const parsed = modelQualityPolicySchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '模型质量检测配置无效'))
    return
  }
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const systemAccountId = requireModelQualitySystemAccountId(access)
    const result = await requestDbService({ type: 'model_quality_command', command: { kind: 'save_policy', systemAccountId, input: parsed.data } })
    if (result.kind !== 'policy') throw new Error('模型质量检测配置保存返回类型无效')
    res.json(ok(result.policy))
  } catch (error) {
    handleModelQualityRouteError(error, res, next)
  }
})

modelChecksRouter.get('/quality-schedules', async (req, res, next) => {
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const systemAccountId = requireModelQualitySystemAccountId(access)
    res.json(ok(await listModelQualitySchedulesAsync(systemAccountId, {
      page: optionalPositiveInteger(req.query.page),
      pageSize: optionalPositiveInteger(req.query.pageSize)
    })))
  } catch (error) {
    handleModelQualityRouteError(error, res, next)
  }
})

modelChecksRouter.post('/quality-schedules', async (req, res, next) => {
  const parsed = modelQualityScheduleSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '定时检查配置无效'))
    return
  }
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const systemAccountId = requireModelQualitySystemAccountId(access)
    const result = await requestDbService({ type: 'model_quality_command', command: { kind: 'create_schedule', systemAccountId, input: parsed.data } })
    if (result.kind !== 'schedule') throw new Error('定时检查配置保存返回类型无效')
    res.json(ok(result.schedule))
  } catch (error) {
    handleModelQualityRouteError(error, res, next)
  }
})

modelChecksRouter.patch('/quality-schedules/:id', async (req, res, next) => {
  const parsed = modelQualitySchedulePatchSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '定时检查配置无效'))
    return
  }
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const systemAccountId = requireModelQualitySystemAccountId(access)
    const result = await requestDbService({
      type: 'model_quality_command',
      command: { kind: 'patch_schedule', systemAccountId, scheduleId: req.params.id, input: parsed.data }
    })
    if (result.kind !== 'schedule') throw new Error('定时检查配置保存返回类型无效')
    res.json(ok(result.schedule))
  } catch (error) {
    handleModelQualityRouteError(error, res, next)
  }
})

modelChecksRouter.delete('/quality-schedules/:id', async (req, res, next) => {
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const systemAccountId = requireModelQualitySystemAccountId(access)
    const result = await requestDbService({ type: 'model_quality_command', command: { kind: 'delete_schedule', systemAccountId, scheduleId: req.params.id } })
    if (result.kind !== 'deleted') throw new Error('定时检查配置删除返回类型无效')
    if (!result.deleted) {
      sendNotFound(res, '定时检查配置不存在')
      return
    }
    res.json(ok(result))
  } catch (error) {
    handleModelQualityRouteError(error, res, next)
  }
})

modelChecksRouter.get('/account-options', handleModelCheckAccountOptions)
modelChecksRouter.get('/options/accounts', handleModelCheckAccountOptions)

async function handleModelCheckAccountOptions(req: Request, res: Response, next: NextFunction) {
  const purpose = req.query.purpose
  if (purpose !== 'run' && purpose !== 'history' && purpose !== 'schedule') {
    res.status(400).json(badRequest('模型检测账户选项 purpose 仅支持 run、history 或 schedule'))
    return
  }
  if (req.query.keyword !== undefined && (typeof req.query.keyword !== 'string' || req.query.keyword.trim().length > 100)) {
    res.status(400).json(badRequest('模型检测账户选项 keyword 无效'))
    return
  }
  const keyword = typeof req.query.keyword === 'string' ? req.query.keyword.trim() || undefined : undefined
  const accountId = typeof req.query.accountId === 'string' ? req.query.accountId.trim() : undefined
  if (req.query.accountId !== undefined && (!accountId || accountId.length > 120 || /[,[\]]/.test(accountId))) {
    res.status(400).json(badRequest('模型检测账户选项 accountId 无效'))
    return
  }
  const limitRaw = req.query.limit
  const limit = limitRaw === undefined ? 50 : Number(limitRaw)
  if (!Number.isInteger(limit) || limit < 1 || limit > 50) {
    res.status(400).json(badRequest('模型检测账户选项 limit 必须是 1 到 50 的整数'))
    return
  }
  const bareSelected = req.query.selectedIds
  const bracketSelected = req.query['selectedIds[]']
  if (bareSelected !== undefined && bracketSelected !== undefined) {
    res.status(400).json(badRequest('模型检测账户选项 selectedIds 无效'))
    return
  }
  const rawSelected = bareSelected ?? bracketSelected
  const selectedValues = Array.isArray(rawSelected) ? rawSelected : rawSelected === undefined ? [] : [rawSelected]
  if (selectedValues.some((value) => typeof value !== 'string' || !value.trim() || value.trim().length > 120 || /[,[\]]/.test(value)) || selectedValues.length > 20) {
    res.status(400).json(badRequest('模型检测账户选项 selectedIds 无效'))
    return
  }
  if (accountId && (keyword || selectedValues.length > 0 || limit !== 1)) {
    res.status(400).json(badRequest('模型检测账户定点模型选项只接受 accountId、purpose 和 limit=1'))
    return
  }
  try {
    const values = await listModelCheckAccountOptions(getRequestAccessScope(req.query.systemAccountId), { purpose, accountId, keyword, selectedIds: selectedValues as string[], limit })
    res.json(ok(values))
  } catch (error) {
    next(error)
  }
}

modelChecksRouter.get('/run/active', (req, res, next) => {
  try {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    res.json(ok(getActiveModelCheckRun(getRequestAccessScope(scopeQuery.data.systemAccountId)) ?? null))
  } catch (error) {
    next(error)
  }
})

modelChecksRouter.post('/run/stop', (req, res, next) => {
  try {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const active = stopActiveModelCheckRun(getRequestAccessScope(scopeQuery.data.systemAccountId))
    res.json(ok({ stopped: Boolean(active), active: active ?? null }))
  } catch (error) {
    next(error)
  }
})

modelChecksRouter.post('/run', async (req, res, next) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const parsed = modelCheckRunSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '模型检测参数无效'))
    return
  }
  const access = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const activeRun = tryStartActiveModelCheckRun(access)
  if (!activeRun.acquired) {
    res.setHeader('Retry-After', String(activeModelCheckRetryAfterSeconds))
    res.status(409).json({ message: activeModelCheckConflictMessage, active: activeRun.active })
    return
  }
  const releaseDiagnosticSlot = tryAcquireDiagnosticTaskSlot()
  if (!releaseDiagnosticSlot) {
    finishActiveModelCheckRun(activeRun.key, activeRun.controller)
    res.setHeader('Retry-After', String(diagnosticTaskRetryAfterSeconds))
    res.status(503).json({ message: diagnosticTaskBusyMessage })
    return
  }
  const clientAbortController = new AbortController()
  // A model check may legitimately exceed a short HTTP request budget while it
  // retries upstream probes. Cancellation is owned by the active-run stop
  // controller or by the client disconnecting.
  const signal = AbortSignal.any([activeRun.controller.signal, clientAbortController.signal])
  req.once('aborted', () => {
    clientAbortController.abort()
    activeRun.controller.abort()
  })
  res.once('close', () => {
    if (!res.writableEnded) {
      clientAbortController.abort()
      activeRun.controller.abort()
    }
  })
  try {
    const result = await runModelCheck(
      parsed.data,
      access,
      signal,
      activeModelCheckProgressUpdater(activeRun.key)
    )
    if (clientAbortController.signal.aborted || res.writableEnded) {
      return
    }
    res.json(ok(result))
  } catch (error) {
    if (clientAbortController.signal.aborted || res.writableEnded) {
      return
    }
    if (error instanceof ModelCheckRequestError) {
      res.status(error.statusCode).json({ message: error.message })
      return
    }
    next(error)
  } finally {
    releaseDiagnosticSlot()
    finishActiveModelCheckRun(activeRun.key, activeRun.controller)
  }
})

modelChecksRouter.post('/run/stream', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const parsed = modelCheckRunSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '模型检测参数无效'))
    return
  }

  const access = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const activeRun = tryStartActiveModelCheckRun(access)
  if (!activeRun.acquired) {
    res.setHeader('Retry-After', String(activeModelCheckRetryAfterSeconds))
    res.status(409).json({ message: activeModelCheckConflictMessage, active: activeRun.active })
    return
  }

  const releaseDiagnosticSlot = tryAcquireDiagnosticTaskSlot()
  if (!releaseDiagnosticSlot) {
    finishActiveModelCheckRun(activeRun.key, activeRun.controller)
    res.setHeader('Retry-After', String(diagnosticTaskRetryAfterSeconds))
    res.status(503).json({ message: diagnosticTaskBusyMessage })
    return
  }

  const clientAbortController = new AbortController()
  const signal = activeRun.controller.signal
  let downstreamResponseErrorReported = false
  let latestWriteStage: 'connected' | 'heartbeat' | 'progress' | 'complete' | 'error' = 'connected'
  const reportDownstreamResponseError = (error: unknown): void => {
    if (downstreamResponseErrorReported) return
    downstreamResponseErrorReported = true
    const errorCode = nodeErrorCode(error)
    logger.warn(errorLogFields(error, {
      event: 'model_check_stream_downstream_response_error',
      epipeSource: errorCode === 'EPIPE' ? 'model_check_sse' : undefined,
      writeStage: latestWriteStage,
      errorCode
    }), '模型检测 SSE 下游响应发生错误')
    clientAbortController.abort()
  }
  const detachDownstreamResponseErrorBoundary = attachDownstreamResponseErrorBoundary({
    response: res,
    onError: reportDownstreamResponseError,
    onUnwritable: () => clientAbortController.abort()
  })
  res.once('finish', detachDownstreamResponseErrorBoundary)
  res.once('close', detachDownstreamResponseErrorBoundary)
  req.once('aborted', () => {
    clientAbortController.abort()
  })
  res.once('close', () => {
    if (!res.writableEnded) {
      clientAbortController.abort()
    }
  })
  const writeEvent = (stage: typeof latestWriteStage, event: string, data: unknown): boolean => {
    if (clientAbortController.signal.aborted || res.writableEnded || res.destroyed) return false
    latestWriteStage = stage
    try {
      return res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`)
    } catch (error) {
      reportDownstreamResponseError(error)
      return false
    }
  }
  try {
    res.writeHead(200, {
      'content-type': 'text/event-stream; charset=utf-8',
      'cache-control': 'no-cache, no-transform',
      connection: 'keep-alive',
      'x-accel-buffering': 'no'
    })
    latestWriteStage = 'connected'
    res.write(': connected\n\n')
  } catch (error) {
    reportDownstreamResponseError(error)
  }
  const heartbeat = setInterval(() => {
    if (clientAbortController.signal.aborted || res.writableEnded) return
    latestWriteStage = 'heartbeat'
    try {
      res.write(': heartbeat\n\n')
    } catch (error) {
      reportDownstreamResponseError(error)
    }
  }, modelCheckStreamHeartbeatMs)
  heartbeat.unref()
  const progressReporter = (event: ModelCheckProgressEvent): void => {
    activeModelCheckProgressUpdater(activeRun.key)(event)
    writeEvent('progress', 'progress', event)
  }

  try {
    const result = await runModelCheck(
      parsed.data,
      access,
      signal,
      progressReporter
    )
    if (clientAbortController.signal.aborted || res.writableEnded) {
      return
    }
    writeEvent('complete', 'complete', result)
    res.end()
  } catch (error) {
    if (clientAbortController.signal.aborted || res.writableEnded) {
      return
    }
    if (error instanceof ModelCheckRequestError) {
      writeEvent('error', 'error', { message: error.message, statusCode: error.statusCode })
      res.end()
      return
    }
    writeEvent('error', 'error', { message: error instanceof Error ? error.message : '模型检测失败' })
    res.end()
  } finally {
    clearInterval(heartbeat)
    releaseDiagnosticSlot()
    finishActiveModelCheckRun(activeRun.key, activeRun.controller)
  }
})

function nodeErrorCode(error: unknown): string | undefined {
  if (!error || typeof error !== 'object') return undefined
  const code = (error as { code?: unknown }).code
  return typeof code === 'string' ? code : undefined
}

modelChecksRouter.get('/runs', async (req, res, next) => {
  try {
    res.json(ok(await listModelCheckRunPage(getRequestAccessScope(req.query.systemAccountId), req.query)))
  } catch (error) {
    next(error)
  }
})

modelChecksRouter.get('/runs/:id', async (req, res, next) => {
  try {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const result = await getModelCheckRun(req.params.id, getRequestAccessScope(scopeQuery.data.systemAccountId))
    if (!result) {
      sendNotFound(res, '模型检测记录不存在')
      return
    }
    res.json(ok(result))
  } catch (error) {
    next(error)
  }
})

function activeModelCheckProgressUpdater(key: string): (event: ModelCheckProgressEvent) => void {
  return (event) => {
    if (event.type === 'run_started') {
      updateActiveModelCheckRun(key, {
        targetId: event.targetId,
        targetName: event.targetName,
        model: event.model,
        profile: event.profile
      })
      return
    }
    if (event.type === 'run_created') {
      updateActiveModelCheckRun(key, {
        runId: event.runId,
        traceId: event.traceId,
        startedAt: event.startedAt
      })
    }
  }
}

function requireModelQualitySystemAccountId(access: ReturnType<typeof getRequestAccessScope>): string {
  const systemAccountId = manageableSystemAccountId(access)
  if (!systemAccountId) {
    throw new ModelCheckRequestError(400, '请先选择具体系统账户')
  }
  return systemAccountId
}

function optionalPositiveInteger(value: unknown): number | undefined {
  if (value === undefined) return undefined
  const raw = Array.isArray(value) ? value[0] : value
  const parsed = typeof raw === 'string' && /^\d+$/.test(raw) ? Number(raw) : NaN
  if (!Number.isSafeInteger(parsed) || parsed <= 0) throw new ModelCheckRequestError(400, '分页参数无效')
  return parsed
}

function handleModelQualityRouteError(error: unknown, res: Response, next: NextFunction): void {
  const message = error instanceof Error ? error.message : '模型质量配置操作失败'
  if (error instanceof ModelCheckRequestError) {
    res.status(error.statusCode).json({ message })
    return
  }
  if (/已变化|已被其他操作修改/.test(message)) {
    res.status(409).json({ message })
    return
  }
  if (/无效|必须|不能为空|不存在|无权|不是当前/.test(message)) {
    res.status(400).json({ message })
    return
  }
  next(error)
}
