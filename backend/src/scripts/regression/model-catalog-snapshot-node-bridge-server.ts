import { createServer, type Server } from 'node:http'
import type { AddressInfo } from 'node:net'

import express, {
  type NextFunction,
  type Request,
  type Response
} from 'express'

import {
  createModelCatalogSnapshotRebuildRouter,
  modelCatalogSnapshotRebuildInternalPrefix
} from '../../modules/internal-api/model-catalog-snapshot-rebuild.routes.js'
import { reconcileModelCatalogSnapshotScopeAsync } from '../../modules/model-pricing/model-catalog-snapshot-reconcile.service.js'
import { closePostgresPool } from '../../storage/postgres-client.js'
import { closeRedisClients } from '../../shared/redis-client.js'

export const modelCatalogSnapshotNodeBridgeReadyPrefix = 'JUHE_AI_MODEL_CATALOG_BRIDGE_READY '

const secretEnvironmentName = 'JUHE_AI_SECRET'
const forceCloseTimeoutMilliseconds = 5_000

let server: Server | undefined
let shutdownPromise: Promise<void> | undefined
let requestedExitCode = 0

process.once('uncaughtException', handleFatalError)
process.once('unhandledRejection', handleFatalError)

await main().catch(async (error: unknown) => {
  writeError(error)
  try {
    await shutdown(1)
  } catch (shutdownError) {
    writeError(shutdownError)
    process.exitCode = 1
  }
})

async function main(): Promise<void> {
  const secret = process.env[secretEnvironmentName]?.trim()
  if (!secret) throw new Error(`缺少必需环境变量 ${secretEnvironmentName}`)

  const app = express()
  app.disable('x-powered-by')
  app.use(modelCatalogSnapshotRebuildInternalPrefix, createModelCatalogSnapshotRebuildRouter({
    secret,
    schemaVersion: 63,
    checkReady: async () => {},
    rebuildAll: async () => {
      const result = await reconcileModelCatalogSnapshotScopeAsync({ scope: 'all' })
      if (result && !result.acknowledged) {
        throw new Error('模型目录快照 dirty generation 已变化，当前重建未确认')
      }
    },
    rebuildPersonal: async (systemAccountId) => {
      const result = await reconcileModelCatalogSnapshotScopeAsync({ scope: 'personal', systemAccountId })
      if (result && !result.acknowledged) {
        throw new Error('个人模型目录快照 dirty generation 已变化，当前重建未确认')
      }
    }
  }))
  app.use((_req, res) => {
    res.status(404).json({ message: '资源不存在' })
  })
  app.use((error: unknown, _req: Request, res: Response, next: NextFunction) => {
    writeError(error)
    if (res.headersSent) {
      next(error)
      return
    }
    res.status(500).json({ message: '服务器内部错误' })
  })

  server = createServer(app)
  await listen(server)
  installShutdownTriggers()
  process.stdout.write(`${modelCatalogSnapshotNodeBridgeReadyPrefix}${JSON.stringify({ port: serverPort(server) })}\n`)
}

function installShutdownTriggers(): void {
  process.once('SIGINT', handleSignal)
  process.once('SIGTERM', handleSignal)
  process.stdin.once('end', handleStdinEnd)
  process.stdin.resume()
}

function handleSignal(): void {
  requestShutdown(0)
}

function handleStdinEnd(): void {
  requestShutdown(0)
}

function handleFatalError(error: unknown): void {
  writeError(error)
  requestShutdown(1)
}

function requestShutdown(exitCode: number): void {
  void shutdown(exitCode).catch((error: unknown) => {
    writeError(error)
    process.exitCode = 1
  })
}

function shutdown(exitCode: number): Promise<void> {
  requestedExitCode = Math.max(requestedExitCode, exitCode)
  if (shutdownPromise) {
    process.exitCode = requestedExitCode
    return shutdownPromise
  }

  shutdownPromise = (async () => {
    process.off('SIGINT', handleSignal)
    process.off('SIGTERM', handleSignal)
    process.stdin.off('end', handleStdinEnd)
    process.stdin.pause()

    let shutdownError: unknown
    try {
      if (server) await closeServer(server)
    } catch (error) {
      shutdownError = error
    }
    try {
    await Promise.all([closePostgresPool(), closeRedisClients()])
    } catch (error) {
      shutdownError = shutdownError
        ? new AggregateError([shutdownError, error], '关闭模型目录快照 Node bridge 失败')
        : error
    }
    process.exitCode = requestedExitCode
    if (shutdownError) throw shutdownError
  })()
  return shutdownPromise
}

function listen(httpServer: Server): Promise<void> {
  return new Promise((resolve, reject) => {
    const handleError = (error: Error): void => {
      httpServer.off('listening', handleListening)
      reject(error)
    }
    const handleListening = (): void => {
      httpServer.off('error', handleError)
      resolve()
    }

    httpServer.once('error', handleError)
    httpServer.once('listening', handleListening)
    httpServer.listen(0, '127.0.0.1')
  })
}

function closeServer(httpServer: Server): Promise<void> {
  if (!httpServer.listening) return Promise.resolve()

  return new Promise((resolve, reject) => {
    const forceCloseTimer = setTimeout(() => {
      httpServer.closeAllConnections()
    }, forceCloseTimeoutMilliseconds)
    forceCloseTimer.unref()

    httpServer.close((error) => {
      clearTimeout(forceCloseTimer)
      if (error) {
        reject(error)
        return
      }
      resolve()
    })
    httpServer.closeIdleConnections()
  })
}

function serverPort(httpServer: Server): number {
  const address = httpServer.address()
  if (!address || typeof address === 'string') {
    throw new Error('模型目录快照 Node bridge 未返回 TCP 监听地址')
  }
  return (address as AddressInfo).port
}

function writeError(error: unknown): void {
  const message = error instanceof Error ? (error.stack ?? error.message) : String(error)
  process.stderr.write(`${message}\n`)
}
