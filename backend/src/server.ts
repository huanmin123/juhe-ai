import { existsSync } from 'node:fs'
import http from 'node:http'
import { basename, resolve } from 'node:path'

import cors from 'cors'
import express, { type NextFunction, type Request, type Response } from 'express'

import {
  startBackgroundWorkerSupervisor,
  stopBackgroundWorkerSupervisor
} from './modules/background/background-worker-supervisor.js'
import { requestIngestWorkerDrainStatus } from './modules/background/background-ipc.js'
import {
  getAuditLogServerDispatchPendingCount,
  waitForAuditLogServerDispatchIdle
} from './modules/audit-logs/audit-log-queue.service.js'
import {
  getAuditLogTransportRuntime,
  stopAuditLogTransportWorker
} from './modules/audit-logs/audit-log-transport.service.js'
import { createDbServiceHttpProxy } from './modules/db-service/db-service-http-proxy.js'
import { getDbServiceState } from './modules/db-service/db-service-ipc.js'
import {
  startDbServiceSupervisor,
  stopDbServiceSupervisor
} from './modules/db-service/db-service-supervisor.js'
import { handleGatewayDbServiceUnavailable, openAIGatewayRouter } from './modules/gateway/routes.js'
import {
  getActiveAuditCaptureCount,
  waitForActiveAuditCapturesIdle
} from './modules/gateway/audit/capture.service.js'
import {
  captureGatewayRawBody,
  classifyGatewayRawBodyParserError,
  recordGatewayBodyRejection,
  rejectGatewayRawBodyByContentLength,
  wrapGatewayRawBodyParser,
  type GatewayRawBodyParserError
} from './modules/gateway/request/body-middleware.js'
import { gatewayRawBodyHardLimit, gatewayRawBodyHardLimitBytes, type GatewayRawBodyRequest } from './modules/gateway/request/body.js'
import { preResolveGatewayRuntime } from './modules/gateway/request/pre-auth.js'
import { admitSpeedFirstRequestBody } from './modules/gateway/request/speed-first-body-admission.middleware.js'
import { backendRoot, runtimeConfig } from './config/runtime.js'
import { closeLogger, errorLogFields, installProcessLogHandlers, logger, startLogMaintenance } from './shared/logger.js'
import { startProcessEventLoopMonitor } from './shared/process-event-loop-monitor.js'
import { getRequestLogger, getTraceId, requestContextMiddleware, sanitizeUrlForLog } from './shared/request-context.js'
import { gatewayErrorPayload } from './modules/gateway/response/responses.js'
import { createCorsOriginDelegate, managementSecurityHeadersMiddleware } from './shared/http-security.js'
import { openAICompatibleFilesRouter } from './modules/openai-compatible-files/files.routes.js'
import { openAICompatibleVectorStoresRouter } from './modules/openai-compatible-vector-stores/vector-stores.routes.js'
import { createHttpCompressionMiddleware } from './shared/http-compression.js'
import { dispatchAccountHealthCheck } from './modules/internal-api/account-health-check-dispatch.service.js'
import {
  mountAccountHealthCheckDispatchBridge
} from './modules/internal-api/account-health-check-dispatch.routes.js'
import { stopModelCheckTokenWorker } from './modules/model-checks/model-checks-token-worker.service.js'
import {
  getPendingGatewayFailureUsageFinalizationCount,
  waitForGatewayFailureUsageFinalizationsIdle
} from './modules/gateway/usage/failure-finalization.service.js'
import { enforcePostgresGooseSchemaGate } from './storage/postgres-goose-schema-gate.js'
import { prewarmPublishedModelCatalogSnapshotsAsync } from './modules/model-pricing/published-model-catalog.service.js'
import { prewarmGatewayApiKeyValidationCacheAsync } from './storage/gateway-api-key.repository.js'

const app = express()
const host = runtimeConfig.host
const port = runtimeConfig.port
const frontendDistPath = resolve(backendRoot, '..', 'frontend', 'dist')
const frontendIndexPath = resolve(frontendDistPath, 'index.html')
const frontendAssetsPath = resolve(frontendDistPath, 'assets')
const systemPrefix = '/__aisys__'
const systemApiPrefix = `${systemPrefix}/api`
const publicApiPrefix = '/__aipublic__'
const helpPrefix = `${systemPrefix}/help`
const gatewayRawBodyLimit = gatewayRawBodyHardLimit
const httpListenBacklog = 8192
const dbServiceHttpProxy = createDbServiceHttpProxy()
const chatHttpProxy = createDbServiceHttpProxy({ maxInFlight: 128, timeoutMs: 15 * 60_000 })
const backgroundWorkerStartupFallbackMs = 15_000
let backgroundWorkerStartupFallbackTimer: NodeJS.Timeout | undefined
let backgroundWorkerSupervisorStarted = false
let serverShutdownInProgress = false
const serverShutdownGraceMs = 40_000
const httpShutdownGraceMs = 10_000

function handleGatewayRawBodyError(error: Error & GatewayRawBodyParserError, req: Request, res: Response, next: NextFunction): void {
  if (res.headersSent) {
    next(error)
    return
  }

  const parserFailure = classifyGatewayRawBodyParserError(error)
  const statusCode = parserFailure.statusCode
  const message = parserFailure.message
  const traceId = getTraceId() ?? 'unknown'
  const responsePayload = gatewayErrorPayload(message, parserFailure.errorType)
  recordGatewayBodyRejection(req as GatewayRawBodyRequest, {
    statusCode,
    responsePayload,
    rawBodyBytes: Number(error.received ?? error.length ?? error.limit ?? 0),
    reason: 'gateway_body_parser',
    errorCode: error.type,
    errorMessage: message,
    failureAttribution: parserFailure.failureAttribution,
    limitBytes: statusCode === 413 ? Number(error.limit ?? gatewayRawBodyHardLimitBytes) : undefined,
    limitScope: statusCode === 413 ? 'gateway' : undefined
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
    error: responsePayload.error
  })
}

const parseGatewayRawBody = wrapGatewayRawBodyParser(
  express.raw({ type: () => true, limit: gatewayRawBodyLimit }),
  handleGatewayRawBodyError
)

installProcessLogHandlers()
startLogMaintenance()
await enforcePostgresGooseSchemaGate()
if (runtimeConfig.auth.captchaDisabled) {
  logger.warn({
    event: 'auth_captcha_disabled',
    runtimeEnvironment: process.env.NODE_ENV?.trim() || 'development'
  }, '登录验证码已关闭：仅用于测试或临时排障，账号密码、登录限频、会话和权限校验仍然生效')
}
startProcessEventLoopMonitor()
startDbServiceSupervisor({ onReady: startBackgroundWorkerSupervisorAfterDbServiceReady })
backgroundWorkerStartupFallbackTimer = setTimeout(() => {
  if (runtimeConfig.runtimeMode === 'performance') {
    logger.warn({
      event: 'background_worker_waiting_for_db_service_ready',
      timeoutMs: backgroundWorkerStartupFallbackMs
    }, '高性能模式 DB service ready 等待超时，后台 worker 将继续等待 DB service 就绪')
    return
  }
  logger.warn({
    event: 'background_worker_start_before_db_service_ready',
    timeoutMs: backgroundWorkerStartupFallbackMs
  }, 'DB service ready 等待超时，后台 worker 将按错峰兜底启动')
  startBackgroundWorkerSupervisorAfterDbServiceReady()
}, backgroundWorkerStartupFallbackMs)
backgroundWorkerStartupFallbackTimer.unref()

function startBackgroundWorkerSupervisorAfterDbServiceReady(): void {
  if (!backgroundWorkerSupervisorStarted) {
    backgroundWorkerSupervisorStarted = true
    if (backgroundWorkerStartupFallbackTimer) {
      clearTimeout(backgroundWorkerStartupFallbackTimer)
      backgroundWorkerStartupFallbackTimer = undefined
    }
    startBackgroundWorkerSupervisor()
  }
  void Promise.all([
    prewarmPublishedModelCatalogSnapshotsAsync(),
    prewarmGatewayApiKeyValidationCacheAsync()
  ])
    .then(([snapshotCount, apiKeyCount]) => logger.info({
      event: 'published_model_catalog_prewarmed',
      snapshotCount,
      apiKeyCount
    }, '发布模型目录快照和 API Key 校验缓存已预热'))
    .catch((error) => logger.warn(errorLogFields(error, {
      event: 'published_model_catalog_prewarm_failed'
    }), '发布模型目录快照预热失败，模型接口将回退单行持久化快照'))
}

if (runtimeConfig.httpSecurity.trustProxy !== false) {
  app.set('trust proxy', runtimeConfig.httpSecurity.trustProxy)
}

app.disable('x-powered-by')
app.use(requestContextMiddleware)
app.use(systemPrefix, managementSecurityHeadersMiddleware)
const corsMiddleware = cors({ credentials: true, origin: createCorsOriginDelegate() })
mountAccountHealthCheckDispatchBridge(app, {
  corsMiddleware,
  compressionMiddleware: createHttpCompressionMiddleware(),
  secret: runtimeConfig.secret,
  dispatch: dispatchAccountHealthCheck
})

app.get(`${systemPrefix}/health`, (_req, res) => {
  res.json({ status: 'ok', service: 'juhe-ai' })
})

app.use(`${systemApiPrefix}/my-chat`, chatHttpProxy)
app.use(systemApiPrefix, dbServiceHttpProxy)
app.use(publicApiPrefix, dbServiceHttpProxy)

if (existsSync(frontendIndexPath)) {
  app.use(helpPrefix, requireHelpSession)
  app.get(systemPrefix, (req, res, next) => {
    if (req.path !== systemPrefix) {
      next()
      return
    }

    res.redirect(302, `${systemPrefix}/`)
  })
  app.use(systemPrefix, express.static(frontendDistPath, {
    setHeaders: (res, filePath) => {
      if (filePath.startsWith(frontendAssetsPath)) {
        res.setHeader('Cache-Control', 'public, max-age=31536000, immutable')
        return
      }
      if (
        basename(filePath) === 'index.html'
        || basename(filePath) === 'brand-icon.svg'
        || basename(filePath) === 'build-info.json'
      ) {
        res.setHeader('Cache-Control', 'no-cache')
      }
    }
  }))
  app.get(`${systemPrefix}/*`, (req, res, next) => {
    if (req.path === `${systemPrefix}/health` || req.path === systemApiPrefix || req.path.startsWith(`${systemApiPrefix}/`)) {
      next()
      return
    }

    res.setHeader('Cache-Control', 'no-cache')
    res.sendFile(frontendIndexPath)
  })
}

interface HelpCurrentUser {
  id: string
  username: string
  displayName: string
  role: string
  mustChangePassword?: boolean
}

async function requireHelpSession(req: Request, res: Response, next: NextFunction): Promise<void> {
  if (req.method !== 'GET' && req.method !== 'HEAD') {
    res.status(405).json({ message: '帮助文档只支持读取' })
    return
  }

  let user: HelpCurrentUser | undefined
  try {
    user = await readHelpCurrentUser(req)
  } catch (error) {
    getRequestLogger().warn({
      event: 'help_auth_check_failed',
      err: error instanceof Error ? error : undefined,
      errorMessage: error instanceof Error ? undefined : String(error),
      method: req.method,
      path: req.path,
      originalUrl: sanitizeUrlForLog(req.originalUrl)
    }, '帮助文档登录态校验失败')
    res.status(503).json({ message: '登录态校验暂不可用，请稍后重试' })
    return
  }

  if (!user) {
    redirectHelpRequestToLogin(req, res)
    return
  }

  const requestPath = pathnameFromOriginalUrl(req.originalUrl)
  if (requestPath === helpPrefix) {
    res.redirect(302, `${helpPrefix}/`)
    return
  }
  if (requestPath === `${helpPrefix}/`) {
    res.redirect(302, `${helpPrefix}/${isManagementRole(user.role) ? 'admin' : 'user'}/`)
    return
  }
  if (isAdminHelpPath(requestPath) && !isManagementRole(user.role)) {
    res.redirect(302, `${helpPrefix}/user/`)
    return
  }

  next()
}

function readHelpCurrentUser(req: Request): Promise<HelpCurrentUser | undefined> {
  const state = getDbServiceState()
  if (!state.ready || !state.httpHost || !state.httpPort) {
    return Promise.reject(new Error('DB service 未就绪'))
  }

  return new Promise((resolveUser, rejectUser) => {
    let settled = false
    const finish = (error: Error | undefined, user?: HelpCurrentUser): void => {
      if (settled) return
      settled = true
      if (error) {
        rejectUser(error)
        return
      }
      resolveUser(user)
    }

    const upstream = http.request({
      host: state.httpHost,
      port: state.httpPort,
      method: 'GET',
      path: `${systemApiPrefix}/auth/me`,
      headers: helpAuthRequestHeaders(req)
    }, (upstreamResponse) => {
      const statusCode = upstreamResponse.statusCode ?? 500
      const chunks: Buffer[] = []
      let bodyBytes = 0
      upstreamResponse.on('data', (chunk: Buffer) => {
        bodyBytes += chunk.length
        if (bodyBytes > 64 * 1024) {
          upstream.destroy(new Error('帮助文档登录态响应过大'))
          return
        }
        chunks.push(Buffer.from(chunk))
      })
      upstreamResponse.on('end', () => {
        if (statusCode === 401 || statusCode === 403) {
          finish(undefined)
          return
        }
        if (statusCode < 200 || statusCode >= 300) {
          finish(new Error(`DB service 登录态校验返回 HTTP ${statusCode}`))
          return
        }
        const payload = parseHelpAuthPayload(Buffer.concat(chunks).toString('utf8'))
        finish(undefined, payload)
      })
    })

    upstream.setTimeout(5000, () => {
      upstream.destroy(new Error('帮助文档登录态校验超时'))
    })
    upstream.on('error', (error) => finish(error))
    upstream.end()
  })
}

function helpAuthRequestHeaders(req: Request): http.OutgoingHttpHeaders {
  const headers: http.OutgoingHttpHeaders = {
    accept: 'application/json',
    cookie: req.headers.cookie ?? '',
    host: req.headers.host,
    'x-forwarded-for': appendHelpForwardedFor(req),
    'x-forwarded-host': req.headers.host,
    'x-forwarded-proto': req.protocol
  }
  const traceId = getTraceId()
  if (traceId) {
    headers['x-trace-id'] = traceId
  }
  return headers
}

function appendHelpForwardedFor(req: Request): string | undefined {
  const current = headerText(req.headers['x-forwarded-for'])
  const remoteAddress = req.ip || req.socket.remoteAddress
  if (!remoteAddress) {
    return current
  }
  return current ? `${current}, ${remoteAddress}` : remoteAddress
}

function headerText(value: string | string[] | undefined): string | undefined {
  return Array.isArray(value) ? value.join(', ') : value
}

function parseHelpAuthPayload(text: string): HelpCurrentUser | undefined {
  try {
    const payload = JSON.parse(text) as { data?: Partial<HelpCurrentUser> }
    const user = payload.data
    if (!user || typeof user.id !== 'string' || typeof user.username !== 'string' || typeof user.displayName !== 'string' || typeof user.role !== 'string') {
      return undefined
    }
    return {
      id: user.id,
      username: user.username,
      displayName: user.displayName,
      role: user.role,
      mustChangePassword: Boolean(user.mustChangePassword)
    }
  } catch {
    return undefined
  }
}

function redirectHelpRequestToLogin(req: Request, res: Response): void {
  res.redirect(302, `${systemPrefix}/login?redirect=${encodeURIComponent(req.originalUrl)}`)
}

function pathnameFromOriginalUrl(originalUrl: string): string {
  try {
    return new URL(originalUrl, 'http://127.0.0.1').pathname.replace(/\/index\.html$/, '/')
  } catch {
    return originalUrl.split('?')[0]?.replace(/\/index\.html$/, '/') ?? originalUrl
  }
}

function isAdminHelpPath(pathname: string): boolean {
  return pathname === `${helpPrefix}/admin` || pathname.startsWith(`${helpPrefix}/admin/`)
}

function isManagementRole(role: string | undefined): boolean {
  return role === 'super_admin' || role === 'admin'
}

app.use(systemPrefix, (_req, res) => {
  res.status(404).json({ message: '资源不存在' })
})

app.use(
  preResolveGatewayRuntime,
  handleGatewayDbServiceUnavailable,
  openAICompatibleFilesRouter,
  openAICompatibleVectorStoresRouter,
  rejectGatewayRawBodyByContentLength,
  admitSpeedFirstRequestBody,
  parseGatewayRawBody,
  captureGatewayRawBody,
  openAIGatewayRouter
)

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

const server = app.listen(port, host, httpListenBacklog, () => {
  logger.info({
    event: 'server_started',
    host,
    port,
    backlog: httpListenBacklog,
    logDirectory: runtimeConfig.log.fileEnabled ? runtimeConfig.log.directory : undefined
  }, `juhe-ai 后端已监听 http://${host}:${port}`)
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

installServerShutdownHooks(server)

function installServerShutdownHooks(httpServer: http.Server): void {
  process.once('SIGINT', () => void shutdownServer(httpServer, 0))
  process.once('SIGTERM', () => void shutdownServer(httpServer, 0))
}

async function shutdownServer(httpServer: http.Server, exitCode: number): Promise<void> {
  if (serverShutdownInProgress) return
  serverShutdownInProgress = true
  const forcedExit = setTimeout(() => {
    logger.error({ event: 'server_shutdown_forced', graceMs: serverShutdownGraceMs }, '服务优雅退出超时，进程将强制结束')
    process.exit(1)
  }, serverShutdownGraceMs)

  try {
    const httpClosed = await closeHttpServer(httpServer, httpShutdownGraceMs)
    const [failureUsageIdle, captureIdle] = await Promise.all([
      waitForGatewayFailureUsageFinalizationsIdle(8_000),
      waitForActiveAuditCapturesIdle(8_000)
    ])
    const dispatchIdle = await waitForAuditLogServerDispatchIdle(8_000)
    const ingestAuditIdle = await waitForIngestAuditDrain(5_000)
    if (!httpClosed || !failureUsageIdle || !captureIdle || !dispatchIdle || !ingestAuditIdle) {
      logger.warn({
        event: 'server_shutdown_drain_incomplete',
        httpClosed,
        failureUsageIdle,
        captureIdle,
        dispatchIdle,
        ingestAuditIdle,
        activeAuditCaptureCount: getActiveAuditCaptureCount(),
        pendingFailureUsageFinalizationCount: getPendingGatewayFailureUsageFinalizationCount(),
        pendingAuditDispatchCount: getAuditLogServerDispatchPendingCount(),
        auditLogTransport: getAuditLogTransportRuntime()
      }, '服务退出前部分请求或审计任务未在时限内排空')
    }
    await stopAuditLogTransportWorker()
    await stopModelCheckTokenWorker()
    await closeLogger()
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'server_shutdown_failed' }), '服务优雅退出失败')
  } finally {
    stopBackgroundWorkerSupervisor()
    stopDbServiceSupervisor()
    clearTimeout(forcedExit)
    process.exit(exitCode)
  }
}

async function closeHttpServer(httpServer: http.Server, timeoutMs: number): Promise<boolean> {
  return await new Promise<boolean>((resolvePromise) => {
    let settled = false
    const finish = (closed: boolean): void => {
      if (settled) return
      settled = true
      clearTimeout(timeout)
      resolvePromise(closed)
    }
    const timeout = setTimeout(() => {
      httpServer.closeAllConnections?.()
      finish(false)
    }, timeoutMs)
    httpServer.close(() => finish(true))
    httpServer.closeIdleConnections?.()
  })
}

async function waitForIngestAuditDrain(timeoutMs: number): Promise<boolean> {
  const deadline = Date.now() + Math.max(1, timeoutMs)
  while (Date.now() < deadline) {
    const status = await requestIngestWorkerDrainStatus(Math.min(1000, Math.max(1, deadline - Date.now())))
    const parentQueueLength = status?.pendingQueues.auditLogs.queueLength ?? 0
    const workerQueueLength = status?.snapshot?.auditLogQueue.queueLength ?? 0
    if (parentQueueLength === 0 && workerQueueLength === 0) return true
    await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 25))
  }
  return false
}
