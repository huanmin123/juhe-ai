import { createServer, type Server } from 'node:http'
import type { AddressInfo } from 'node:net'
import { createInterface, type Interface } from 'node:readline'

import express, {
  type NextFunction,
  type Request,
  type RequestHandler,
  type Response
} from 'express'

import {
  mountAccountHealthCheckDispatchBridge,
  type AccountHealthCheckDispatchReason
} from '../../modules/internal-api/account-health-check-dispatch.routes.js'
import { createHttpCompressionMiddleware } from '../../shared/http-compression.js'

const secretEnvironmentName = 'JUHE_AI_ACCOUNT_HEALTH_CHECK_DISPATCH_E2E_SECRET'
const protectionTimeoutMilliseconds = 30_000
const forceCloseTimeoutMilliseconds = 1_000
const expectedCalls: ReadonlyArray<DispatchCall> = [
  { accountId: 'e2e-activation', reason: 'activation' },
  { accountId: 'e2e-configuration', reason: 'configuration' }
]

interface DispatchCall {
  accountId: string
  reason: AccountHealthCheckDispatchReason
}

type E2eEvent =
  | { event: 'ready'; baseUrl: string }
  | { event: 'confirmed'; calls: DispatchCall[] }
  | { event: 'rejected'; expected: DispatchCall | null; actual: DispatchCall }
  | { event: 'stopped' }

await main().catch((error: unknown) => {
  console.error(error instanceof Error ? error.message : String(error))
  process.exitCode = 1
})

async function main(): Promise<void> {
  const secret = process.env[secretEnvironmentName]
  if (!secret) {
    throw new Error(`缺少必需环境变量 ${secretEnvironmentName}`)
  }

  const app = express()
  const matchedCalls: DispatchCall[] = []
  let confirmed = false
  const corsMiddleware: RequestHandler = (_req, _res, next) => next()

  mountAccountHealthCheckDispatchBridge(app, {
    secret,
    corsMiddleware,
    compressionMiddleware: createHttpCompressionMiddleware(),
    dispatch: (accountId, reason) => {
      const expected = expectedCalls[matchedCalls.length]
      if (
        !expected
        || expected.accountId !== accountId
        || expected.reason !== reason
      ) {
        emitEvent({
          event: 'rejected',
          expected: expected ?? null,
          actual: { accountId, reason }
        })
        return false
      }

      matchedCalls.push({ accountId, reason })
      if (!confirmed && matchedCalls.length === expectedCalls.length) {
        confirmed = true
        emitEvent({ event: 'confirmed', calls: [...matchedCalls] })
      }
      return true
    }
  })

  app.use((_req, res) => {
    res.status(404).json({ message: '资源不存在' })
  })
  app.use((error: unknown, _req: Request, res: Response, next: NextFunction) => {
    if (res.headersSent) {
      next(error)
      return
    }
    res.status(500).json({ message: '服务器内部错误' })
  })

  const server = createServer(app)
  await listen(server)

  const input = createInterface({
    input: process.stdin,
    crlfDelay: Infinity,
    terminal: false
  })
  const shutdown = createShutdown(server, input)
  const handleSignal = (): void => {
    void shutdown()
  }

  process.once('SIGINT', handleSignal)
  process.once('SIGTERM', handleSignal)
  input.on('line', (line) => {
    if (line.trim() === 'shutdown') {
      void shutdown()
    }
  })
  input.once('close', () => {
    void shutdown()
  })

  const protectionTimer = setTimeout(() => {
    void shutdown()
  }, protectionTimeoutMilliseconds)
  protectionTimer.unref()

  shutdown.setProtectionTimer(protectionTimer)
  shutdown.setSignalHandler(handleSignal)
  emitEvent({
    event: 'ready',
    baseUrl: `http://127.0.0.1:${serverPort(server)}`
  })
}

interface Shutdown {
  (): Promise<void>
  setProtectionTimer: (timer: NodeJS.Timeout) => void
  setSignalHandler: (handler: () => void) => void
}

function createShutdown(server: Server, input: Interface): Shutdown {
  let protectionTimer: NodeJS.Timeout | undefined
  let signalHandler: (() => void) | undefined
  let shutdownPromise: Promise<void> | undefined

  const shutdown = (): Promise<void> => {
    if (shutdownPromise) {
      return shutdownPromise
    }

    shutdownPromise = Promise.resolve().then(async () => {
      if (protectionTimer) {
        clearTimeout(protectionTimer)
      }
      if (signalHandler) {
        process.off('SIGINT', signalHandler)
        process.off('SIGTERM', signalHandler)
      }
      input.close()
      process.stdin.pause()
      process.stdin.destroy()
      await closeServer(server)
      emitEvent({ event: 'stopped' })
    })
    return shutdownPromise
  }

  shutdown.setProtectionTimer = (timer: NodeJS.Timeout) => {
    protectionTimer = timer
  }
  shutdown.setSignalHandler = (handler: () => void) => {
    signalHandler = handler
  }
  return shutdown
}

function listen(server: Server): Promise<void> {
  return new Promise((resolve, reject) => {
    const handleError = (error: Error): void => {
      server.off('listening', handleListening)
      reject(error)
    }
    const handleListening = (): void => {
      server.off('error', handleError)
      resolve()
    }

    server.once('error', handleError)
    server.once('listening', handleListening)
    server.listen(0, '127.0.0.1')
  })
}

function closeServer(server: Server): Promise<void> {
  return new Promise((resolve, reject) => {
    const forceCloseTimer = setTimeout(() => {
      server.closeAllConnections()
    }, forceCloseTimeoutMilliseconds)
    forceCloseTimer.unref()

    server.close((error) => {
      clearTimeout(forceCloseTimer)
      if (error) {
        reject(error)
        return
      }
      resolve()
    })
    server.closeIdleConnections()
  })
}

function serverPort(server: Server): number {
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('E2E server 未返回 TCP 监听地址')
  }
  return (address as AddressInfo).port
}

function emitEvent(event: E2eEvent): void {
  process.stdout.write(`JUHE_AI_E2E ${JSON.stringify(event)}\n`)
}
