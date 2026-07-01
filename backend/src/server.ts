import { existsSync } from 'node:fs'
import { basename, resolve } from 'node:path'

import cors from 'cors'
import express, { type NextFunction, type Request, type Response } from 'express'

import { startBackgroundWorkerSupervisor } from './modules/background/background-worker-supervisor.js'
import { createDbServiceHttpProxy } from './modules/db-service/db-service-http-proxy.js'
import { startDbServiceSupervisor } from './modules/db-service/db-service-supervisor.js'
import { handleGatewayDbServiceUnavailable, openAIGatewayRouter } from './modules/gateway/routes.js'
import {
  captureGatewayRawBody,
  recordGatewayBodyRejection,
  rejectGatewayRawBodyByContentLength
} from './modules/gateway/request/body-middleware.js'
import { gatewayRawBodyHardLimit, type GatewayRawBodyRequest } from './modules/gateway/request/body.js'
import { preResolveGatewayRuntime } from './modules/gateway/request/pre-auth.js'
import { backendRoot, runtimeConfig } from './config/runtime.js'
import { installProcessLogHandlers, logger } from './shared/logger.js'
import { startProcessEventLoopMonitor } from './shared/process-event-loop-monitor.js'
import { getRequestLogger, getTraceId, requestContextMiddleware, sanitizeUrlForLog } from './shared/request-context.js'
import { gatewayErrorPayload } from './modules/gateway/response/responses.js'
import { createCorsOriginDelegate } from './shared/http-security.js'
import { setRuntimeLogLineSink } from './modules/runtime-logs/runtime-log-stream.js'
import { enqueueRuntimeLogLine } from './modules/runtime-logs/runtime-log-index-queue.service.js'
import { openAICompatibleFilesRouter } from './modules/openai-compatible-files/files.routes.js'
import { openAICompatibleVectorStoresRouter } from './modules/openai-compatible-vector-stores/vector-stores.routes.js'
import { createHttpCompressionMiddleware } from './shared/http-compression.js'

const app = express()
const host = runtimeConfig.host
const port = runtimeConfig.port
const frontendDistPath = resolve(backendRoot, '..', 'frontend', 'dist')
const frontendIndexPath = resolve(frontendDistPath, 'index.html')
const frontendAssetsPath = resolve(frontendDistPath, 'assets')
const systemPrefix = '/__aisys__'
const systemApiPrefix = `${systemPrefix}/api`
const publicApiPrefix = '/__aipublic__'
const gatewayRawBodyLimit = gatewayRawBodyHardLimit
const httpListenBacklog = 8192
const dbServiceHttpProxy = createDbServiceHttpProxy()
const backgroundWorkerStartupFallbackMs = 15_000
let backgroundWorkerStartupFallbackTimer: NodeJS.Timeout | undefined
let backgroundWorkerSupervisorStarted = false

type BodyParserError = Error & { status?: number; statusCode?: number; type?: string; received?: number; length?: number; limit?: number }

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
  const responsePayload = gatewayErrorPayload(message, statusCode === 413 ? 'request_too_large' : 'invalid_request_error')
  recordGatewayBodyRejection(req as GatewayRawBodyRequest, {
    statusCode,
    responsePayload,
    rawBodyBytes: Number(error.received ?? error.length ?? error.limit ?? 0),
    reason: 'gateway_body_parser',
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
    error: responsePayload.error
  })
}

installProcessLogHandlers()
startProcessEventLoopMonitor()
setRuntimeLogLineSink((line, options) => enqueueRuntimeLogLine(line, options))
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
  if (backgroundWorkerSupervisorStarted) {
    return
  }
  backgroundWorkerSupervisorStarted = true
  if (backgroundWorkerStartupFallbackTimer) {
    clearTimeout(backgroundWorkerStartupFallbackTimer)
    backgroundWorkerStartupFallbackTimer = undefined
  }
  startBackgroundWorkerSupervisor()
}

if (runtimeConfig.httpSecurity.trustProxy !== false) {
  app.set('trust proxy', runtimeConfig.httpSecurity.trustProxy)
}

app.use(requestContextMiddleware)
app.use(cors({ credentials: true, origin: createCorsOriginDelegate() }))
app.use(createHttpCompressionMiddleware())

app.get(`${systemPrefix}/health`, (_req, res) => {
  res.json({ status: 'ok', service: 'juhe-ai' })
})

app.use(systemApiPrefix, dbServiceHttpProxy)
app.use(publicApiPrefix, dbServiceHttpProxy)

if (existsSync(frontendIndexPath)) {
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
      if (basename(filePath) === 'index.html' || basename(filePath) === 'brand-icon.svg') {
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

app.use(systemPrefix, (_req, res) => {
  res.status(404).json({ message: '资源不存在' })
})

app.use(
  preResolveGatewayRuntime,
  handleGatewayDbServiceUnavailable,
  openAICompatibleFilesRouter,
  openAICompatibleVectorStoresRouter,
  rejectGatewayRawBodyByContentLength,
  express.raw({ type: () => true, limit: gatewayRawBodyLimit }),
  handleGatewayRawBodyError,
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
