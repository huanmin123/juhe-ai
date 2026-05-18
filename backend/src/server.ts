import { existsSync } from 'node:fs'
import { basename, resolve } from 'node:path'

import cors from 'cors'
import express, { type NextFunction, type Request, type Response } from 'express'

import { startBackgroundWorkerSupervisor } from './modules/background/background-worker-supervisor.js'
import { createDbServiceHttpProxy } from './modules/db-service/db-service-http-proxy.js'
import { startDbServiceSupervisor } from './modules/db-service/db-service-supervisor.js'
import { openAIGatewayRouter } from './modules/gateway/openai-gateway.routes.js'
import { captureGatewayRawBody } from './modules/gateway/openai-gateway-request-body-middleware.js'
import { recordDroppedAuditCapture } from './modules/audit-logs/audit-log-queue.service.js'
import { backendRoot, runtimeConfig } from './config/runtime.js'
import { installProcessLogHandlers, logger } from './shared/logger.js'
import { startProcessEventLoopMonitor } from './shared/process-event-loop-monitor.js'
import { getRequestLogger, getTraceId, requestContextMiddleware, sanitizeUrlForLog } from './shared/request-context.js'
import { setRuntimeLogLineSink } from './modules/runtime-logs/runtime-log-stream.js'
import { sendRuntimeLogLineToWorker } from './modules/background/background-ipc.js'

const app = express()
const host = runtimeConfig.host
const port = runtimeConfig.port
const frontendDistPath = resolve(backendRoot, '..', 'frontend', 'dist')
const frontendIndexPath = resolve(frontendDistPath, 'index.html')
const frontendAssetsPath = resolve(frontendDistPath, 'assets')
const systemPrefix = '/__aisys__'
const systemApiPrefix = `${systemPrefix}/api`
const gatewayRawBodyLimit = '64mb'
const dbServiceHttpProxy = createDbServiceHttpProxy()

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

installProcessLogHandlers()
startProcessEventLoopMonitor()
setRuntimeLogLineSink((line, options) => sendRuntimeLogLineToWorker(line, options))
startDbServiceSupervisor()
startBackgroundWorkerSupervisor()

app.use(requestContextMiddleware)
app.use(cors({ credentials: true, origin: true }))

app.get(`${systemPrefix}/health`, (_req, res) => {
  res.json({ status: 'ok', service: 'juhe-ai' })
})

app.use(systemApiPrefix, dbServiceHttpProxy)

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

app.use(express.raw({ type: () => true, limit: gatewayRawBodyLimit }), handleGatewayRawBodyError, captureGatewayRawBody, openAIGatewayRouter)

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
