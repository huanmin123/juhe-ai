import { existsSync } from 'node:fs'
import { resolve } from 'node:path'

import cors from 'cors'
import express, { type NextFunction, type Request, type Response } from 'express'

import { accountsRouter } from './modules/accounts/accounts.routes.js'
import { announcementsRouter } from './modules/announcements/announcements.routes.js'
import { forceSelfAccessScope, requireAdmin, requireAuth } from './modules/auth/auth.middleware.js'
import { authRouter } from './modules/auth/auth.routes.js'
import { startBackgroundWorkerSupervisor } from './modules/background/background-worker-supervisor.js'
import { startDbServiceSupervisor } from './modules/db-service/db-service-supervisor.js'
import { apiKeysRouter } from './modules/api-keys/api-keys.routes.js'
import { auditLogsRouter } from './modules/audit-logs/audit-logs.routes.js'
import { authorizationOptionsRouter } from './modules/authorization-options/authorization-options.routes.js'
import { authorizationsRouter } from './modules/authorizations/authorizations.routes.js'
import { errorPoliciesRouter } from './modules/error-policies/error-policies.routes.js'
import { groupsRouter } from './modules/groups/groups.routes.js'
import { providersRouter } from './modules/providers/providers.routes.js'
import { proxiesRouter } from './modules/proxies/proxies.routes.js'
import { runtimeLogsRouter } from './modules/runtime-logs/runtime-logs.routes.js'
import { settingsRouter } from './modules/settings/settings.routes.js'
import { statsRouter } from './modules/stats/stats.routes.js'
import { systemAccountsRouter } from './modules/system-accounts/system-accounts.routes.js'
import { myTeamsRouter, systemTeamsRouter } from './modules/system-teams/system-teams.routes.js'
import { usageRecordsRouter } from './modules/usage-records/usage-records.routes.js'
import { openAIGatewayRouter } from './modules/gateway/openai-gateway.routes.js'
import { myOperationLogsRouter, operationLogsRouter } from './modules/operation-logs/operation-logs.routes.js'
import { recordDroppedAuditCapture } from './modules/audit-logs/audit-log-queue.service.js'
import { openAIOAuthRouter } from './modules/openai-oauth/openai-oauth.routes.js'
import { backendRoot, runtimeConfig } from './config/runtime.js'
import { getDatabase } from './storage/database.js'
import { listPublicGlobalSettings } from './storage/repositories.js'
import { ok } from './shared/http.js'
import { installProcessLogHandlers, logger } from './shared/logger.js'
import { getRequestLogger, getTraceId, requestContextMiddleware, sanitizeUrlForLog } from './shared/request-context.js'
import { setRuntimeLogLineSink } from './modules/runtime-logs/runtime-log-stream.js'
import { sendRuntimeLogLineToWorker } from './modules/background/background-ipc.js'

const app = express()
const host = runtimeConfig.host
const port = runtimeConfig.port
const frontendDistPath = resolve(backendRoot, '..', 'frontend', 'dist')
const frontendIndexPath = resolve(frontendDistPath, 'index.html')
const gatewayRawBodyLimit = '64mb'

type RawBodyRequest = Request & { rawBody?: Buffer }
type BodyParserError = Error & { status?: number; statusCode?: number; type?: string; received?: number; length?: number; limit?: number }

function captureGatewayRawBody(req: RawBodyRequest, _res: Response, next: NextFunction): void {
  const rawBody = Buffer.isBuffer(req.body) ? req.body : Buffer.alloc(0)
  req.rawBody = rawBody

  const contentType = req.headers['content-type'] ?? ''
  if (rawBody.length > 0 && String(contentType).toLowerCase().includes('json')) {
    try {
      req.body = JSON.parse(rawBody.toString('utf8')) as unknown
    } catch {
      req.body = undefined
    }
  } else {
    req.body = undefined
  }

  next()
}

function handleGatewayRawBodyError(error: BodyParserError, req: Request, res: Response, next: NextFunction): void {
  if (res.headersSent) {
    next(error)
    return
  }

  const statusCode = Number.isInteger(error.statusCode)
    ? Number(error.statusCode)
    : Number.isInteger(error.status)
      ? Number(error.status)
      : 400
  const message = statusCode === 413 ? '请求体过大' : '网关请求体无效'
  const traceId = getTraceId() ?? 'unknown'
  recordDroppedAuditCapture({
    traceId,
    auditOutcome: 'gateway_failed',
    success: false,
    bytes: Number(error.received ?? error.length ?? error.limit ?? 0),
    reason: 'gateway_body_rejected',
    method: req.method,
    path: req.path,
    queryString: req.originalUrl.includes('?') ? req.originalUrl.split('?').slice(1).join('?') : undefined,
    statusCode,
    errorPhase: 'gateway',
    errorCode: error.type,
    errorMessage: message
  })
  getRequestLogger().warn({
    event: 'gateway_raw_body_rejected',
    traceId,
    method: req.method,
    path: req.path,
    originalUrl: sanitizeUrlForLog(req.originalUrl),
    statusCode,
    errorType: error.type,
    receivedBytes: error.received,
    bodyLength: error.length,
    bodyLimit: error.limit
  }, '网关原始请求体被拒绝')
  res.status(statusCode >= 400 && statusCode < 600 ? statusCode : 400).json({
    error: {
      message,
      type: statusCode === 413 ? 'request_too_large' : 'invalid_request_error'
    }
  })
}

getDatabase()
installProcessLogHandlers()
setRuntimeLogLineSink((line) => sendRuntimeLogLineToWorker(line))

app.use(requestContextMiddleware)
app.use(cors({ credentials: true, origin: true }))
app.use('/v1', express.raw({ type: () => true, limit: gatewayRawBodyLimit }), handleGatewayRawBodyError, captureGatewayRawBody, openAIGatewayRouter)
app.use(express.json({
  limit: '2mb',
  verify: (req, _res, buffer) => {
    ;(req as RawBodyRequest).rawBody = Buffer.from(buffer)
  }
}))

app.get('/health', (_req, res) => {
  res.json({ status: 'ok', service: 'juhe-ai' })
})

app.get('/api/health', (_req, res) => {
	res.json({ status: 'ok', service: 'juhe-ai' })
})

app.use('/api/auth', authRouter)
app.get('/api/settings/public', (_req, res) => {
  res.json(ok(listPublicGlobalSettings()))
})

app.use('/api', requireAuth)
app.use('/api/announcements', announcementsRouter)
app.use('/api/my-accounts', forceSelfAccessScope, accountsRouter)
app.use('/api/my-groups', forceSelfAccessScope, groupsRouter)
app.use('/api/my-api-keys', forceSelfAccessScope, apiKeysRouter)
app.use('/api/my-authorization-options', forceSelfAccessScope, authorizationOptionsRouter)
app.use('/api/my-authorizations', forceSelfAccessScope, authorizationsRouter)
app.use('/api/my-openai-oauth', forceSelfAccessScope, openAIOAuthRouter)
app.use('/api/my-usage-records', forceSelfAccessScope, usageRecordsRouter)
app.use('/api/my-stats', forceSelfAccessScope, statsRouter)
app.use('/api/my-operation-logs', forceSelfAccessScope, myOperationLogsRouter)
app.use('/api/providers', requireAdmin, providersRouter)
app.use('/api/error-policies', errorPoliciesRouter)
app.use('/api/accounts', requireAdmin, accountsRouter)
app.use('/api/groups', requireAdmin, groupsRouter)
app.use('/api/api-keys', requireAdmin, apiKeysRouter)
app.use('/api/authorization-options', requireAdmin, authorizationOptionsRouter)
app.use('/api/authorizations', requireAdmin, authorizationsRouter)
app.use('/api/openai-oauth', requireAdmin, openAIOAuthRouter)
app.use('/api/proxies', proxiesRouter)
app.use('/api/usage-records', requireAdmin, usageRecordsRouter)
app.use('/api/operation-logs', requireAdmin, operationLogsRouter)
app.use('/api/audit-logs', requireAdmin, auditLogsRouter)
app.use('/api/runtime-logs', requireAdmin, runtimeLogsRouter)
app.use('/api/stats', requireAdmin, statsRouter)
app.use('/api/settings', settingsRouter)
app.use('/api/system-accounts', systemAccountsRouter)
app.use('/api/my-teams', forceSelfAccessScope, myTeamsRouter)
app.use('/api/system-teams', systemTeamsRouter)

if (existsSync(frontendIndexPath)) {
  app.use(express.static(frontendDistPath))
  app.get('*', (req, res, next) => {
    if (req.path === '/health' || req.path.startsWith('/api') || req.path.startsWith('/v1')) {
      next()
      return
    }

    res.sendFile(frontendIndexPath)
  })
}

app.use((_req, res) => {
  res.status(404).json({ message: '资源不存在' })
})

app.use((error: unknown, req: Request, res: Response, _next: NextFunction) => {
  getRequestLogger().error({
    event: 'http_request_unhandled_error',
    err: error instanceof Error ? error : undefined,
    errorMessage: error instanceof Error ? undefined : String(error),
    method: req.method,
    path: req.path,
    originalUrl: sanitizeUrlForLog(req.originalUrl)
  }, '未处理的 HTTP 请求错误')

  if (res.headersSent) {
    res.end()
    return
  }

  res.status(500).json({ message: '服务器内部错误' })
})

const server = app.listen(port, host, () => {
  logger.info({
    event: 'server_started',
    host,
    port,
    logDirectory: runtimeConfig.log.fileEnabled ? runtimeConfig.log.directory : undefined
  }, `juhe-ai 后端已监听 http://${host}:${port}`)
  startDbServiceSupervisor()
  startBackgroundWorkerSupervisor()
})

server.on('error', (error: NodeJS.ErrnoException) => {
  logger.fatal({
    event: 'server_listen_failed',
    err: error,
    host,
    port,
    code: error.code
  }, '后端服务监听失败')
  process.exit(1)
})
