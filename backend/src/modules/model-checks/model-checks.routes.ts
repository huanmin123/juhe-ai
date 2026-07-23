import { Router, type NextFunction, type Request, type Response } from 'express'
import { z } from 'zod'

import { badRequest, ok, sendNotFound } from '../../shared/http.js'
import { runtimeConfig } from '../../config/runtime.js'
import { mainDatabaseRuntimeInfo } from '../../storage/database.js'
import { activateModelTokenInterceptBaselineAsync } from '../../storage/model-trust.repository.js'
import { requestStatsWriter } from '../background/background-stats-writer.js'
import { requireAdmin } from '../auth/auth.middleware.js'
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
export const modelCheckHttpRunDeadlineMs = 25_000
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

modelChecksRouter.get('/account-options', handleModelCheckAccountOptions)
modelChecksRouter.get('/options/accounts', handleModelCheckAccountOptions)

async function handleModelCheckAccountOptions(req: Request, res: Response, next: NextFunction) {
  const purpose = req.query.purpose
  if (purpose !== 'run' && purpose !== 'history') {
    res.status(400).json(badRequest('模型检测账户选项 purpose 仅支持 run 或 history'))
    return
  }
  if (req.query.keyword !== undefined && (typeof req.query.keyword !== 'string' || req.query.keyword.trim().length > 100)) {
    res.status(400).json(badRequest('模型检测账户选项 keyword 无效'))
    return
  }
  const keyword = typeof req.query.keyword === 'string' ? req.query.keyword.trim() || undefined : undefined
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
  try {
    const values = await listModelCheckAccountOptions(getRequestAccessScope(req.query.systemAccountId), { purpose, keyword, selectedIds: selectedValues as string[], limit })
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
  const deadlineSignal = AbortSignal.timeout(modelCheckHttpRunDeadlineMs)
  const signal = AbortSignal.any([activeRun.controller.signal, clientAbortController.signal, deadlineSignal])
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
  req.once('aborted', () => {
    clientAbortController.abort()
  })
  res.once('close', () => {
    if (!res.writableEnded) {
      clientAbortController.abort()
    }
  })
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'cache-control': 'no-cache, no-transform',
    connection: 'keep-alive',
    'x-accel-buffering': 'no'
  })
  res.write(': connected\n\n')
  const heartbeat = setInterval(() => {
    if (clientAbortController.signal.aborted || res.writableEnded) return
    res.write(': heartbeat\n\n')
  }, modelCheckStreamHeartbeatMs)
  heartbeat.unref()

  const writeEvent = (event: string, data: unknown): void => {
    if (clientAbortController.signal.aborted || res.writableEnded) return
    res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`)
  }
  const progressReporter = (event: ModelCheckProgressEvent): void => {
    activeModelCheckProgressUpdater(activeRun.key)(event)
    writeEvent('progress', event)
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
    writeEvent('complete', result)
    res.end()
  } catch (error) {
    if (clientAbortController.signal.aborted || res.writableEnded) {
      return
    }
    if (error instanceof ModelCheckRequestError) {
      writeEvent('error', { message: error.message, statusCode: error.statusCode })
      res.end()
      return
    }
    writeEvent('error', { message: error instanceof Error ? error.message : '模型检测失败' })
    res.end()
  } finally {
    clearInterval(heartbeat)
    releaseDiagnosticSlot()
    finishActiveModelCheckRun(activeRun.key, activeRun.controller)
  }
})

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
