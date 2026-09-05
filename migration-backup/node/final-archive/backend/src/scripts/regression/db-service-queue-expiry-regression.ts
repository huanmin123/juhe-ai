import { strict as assert } from 'node:assert'
import { fork, type ChildProcess } from 'node:child_process'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import type {
  DbServiceChildMessage,
  DbServiceOperation,
  DbServiceParentMessage,
  DbServiceRuntimeSnapshot
} from '../../modules/db-service/db-service-types.js'

const currentDir = dirname(fileURLToPath(import.meta.url))
const backendRoot = resolve(currentDir, '../../..')
const dbServiceEntry = resolve(backendRoot, 'src/db-service.ts')
const tempRoot = resolve(tmpdir(), `juhe-ai-db-service-queue-expiry-${Date.now()}-${Math.random().toString(16).slice(2)}`)

mkdirSync(tempRoot, { recursive: true })

let requestSequence = 0
let child: ChildProcess | undefined

try {
  child = fork(dbServiceEntry, [], {
    cwd: backendRoot,
    env: {
      ...process.env,
      JUHE_AI_PROCESS_ROLE: 'db-service',
      JUHE_AI_DATABASE_PATH: resolve(tempRoot, 'business.sqlite3'),
      JUHE_AI_DATASET_DATABASE_PATH: resolve(tempRoot, 'dataset.sqlite3'),
      JUHE_AI_STATS_DATABASE_PATH: resolve(tempRoot, 'stats.sqlite3'),
      JUHE_AI_USAGE_CATALOG_DATABASE_PATH: resolve(tempRoot, 'usage-catalog.sqlite3'),
      JUHE_AI_USAGE_SHARD_ROOT: resolve(tempRoot, 'usage-shards'),
      JUHE_AI_SECRET: 'db-service-queue-expiry-secret',
      JUHE_AI_DB_SERVICE_HTTP_HOST: '127.0.0.1',
      JUHE_AI_DB_SERVICE_HTTP_PORT: '0',
      JUHE_AI_LOG_LEVEL: 'warn',
      JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
      JUHE_AI_LOG_FILE_ENABLED: 'false'
    },
    execArgv: process.execArgv.filter((arg) => !arg.startsWith('--inspect')),
    serialization: 'advanced',
    stdio: ['ignore', 'pipe', 'pipe', 'ipc']
  })
  child.stdout?.on('data', (chunk) => process.stdout.write(`[db-service-queue-expiry] ${String(chunk)}`))
  child.stderr?.on('data', (chunk) => process.stderr.write(`[db-service-queue-expiry] ${String(chunk)}`))

  await waitForDbServiceReady(child)

  const dbServiceSource = readFileSync(dbServiceEntry, 'utf8')
  assert.match(
    dbServiceSource,
    /function enqueueDbServiceRequest[\s\S]+purgeExpiredDbServiceRequests\(\)[\s\S]+if \(!canQueueDbServiceRequest\(estimatedBytes\)\)/,
    'DB service 入队容量判断前必须先清理过期请求，避免队列满但全过期时拒绝新请求'
  )

  const expired = await requestDbServiceRaw(child, {
    type: 'cleanup_expired_system_sessions',
    expiredBefore: '2000-01-01T00:00:00.000Z',
    limit: 1
  }, Date.now() - 1)
  assert.equal(expired.ok, false, '已过期 DB service 请求应被拒绝')
  assert.match(expired.errorMessage ?? '', /本地数据库服务请求已过期/, '过期请求应返回明确的本地队列过期错误')

  const normal = await requestDbServiceRaw(child, { type: 'status' })
  assert.equal(normal.ok, true, '过期请求后正常 status 请求仍应成功')
  const snapshot = normal.result as DbServiceRuntimeSnapshot
  assert(snapshot.queueExpiredCount !== undefined && snapshot.queueExpiredCount >= 1, 'status snapshot 应记录 queueExpiredCount')
  assert.equal(snapshot.queuedRequestCount, 0, '过期清理后不应残留排队请求')
  assert.equal(snapshot.queuedRequestBytes, 0, '过期清理后不应残留排队字节')

  console.log('DB service 队列过期回归通过：过期请求被服务端清理，指标递增且后续请求正常')
} finally {
  await stopChild(child)
  rmSync(tempRoot, { recursive: true, force: true })
}

function waitForDbServiceReady(target: ChildProcess): Promise<void> {
  return new Promise((resolvePromise, rejectPromise) => {
    const timeout = setTimeout(() => cleanup(rejectPromise, new Error('DB service ready 等待超时')), 20_000)
    const onMessage = (message: DbServiceChildMessage) => {
      if (message?.type === 'db_service_ready') {
        cleanup(resolvePromise)
      }
    }
    const onExit = (code: number | null, signal: NodeJS.Signals | null) => {
      cleanup(rejectPromise, new Error(`DB service 提前退出：code=${code ?? ''} signal=${signal ?? ''}`))
    }
    const cleanup = (done: (value?: never) => void, error?: Error) => {
      clearTimeout(timeout)
      target.off('message', onMessage)
      target.off('exit', onExit)
      if (error) {
        done(error as never)
        return
      }
      done()
    }
    target.on('message', onMessage)
    target.once('exit', onExit)
  })
}

function requestDbServiceRaw(
  target: ChildProcess,
  operation: DbServiceOperation,
  deadlineAtMs?: number
): Promise<{ ok: true; result: unknown } | { ok: false; errorMessage?: string }> {
  const requestId = `queue-expiry-${++requestSequence}`
  const message: DbServiceParentMessage = {
    type: 'db_service_request',
    requestId,
    operation,
    ...(deadlineAtMs === undefined ? {} : { deadlineAtMs })
  }
  return new Promise((resolvePromise, rejectPromise) => {
    const timeout = setTimeout(() => cleanup(rejectPromise, new Error(`DB service 请求 ${requestId} 等待超时`)), 10_000)
    const onMessage = (response: DbServiceChildMessage) => {
      if (response?.type !== 'db_service_response' || response.requestId !== requestId) {
        return
      }
      cleanup(resolvePromise, undefined, response.ok
        ? { ok: true, result: response.result }
        : { ok: false, errorMessage: response.errorMessage })
    }
    const cleanup = (
      done: (value: { ok: true; result: unknown } | { ok: false; errorMessage?: string }) => void,
      error?: Error,
      value?: { ok: true; result: unknown } | { ok: false; errorMessage?: string }
    ) => {
      clearTimeout(timeout)
      target.off('message', onMessage)
      if (error) {
        rejectPromise(error)
        return
      }
      done(value ?? { ok: false, errorMessage: 'missing response' })
    }
    target.on('message', onMessage)
    target.send?.(message, (error) => {
      if (error) {
        cleanup(resolvePromise, error)
      }
    })
  })
}

async function stopChild(target?: ChildProcess): Promise<void> {
  if (!target || target.exitCode !== null) {
    return
  }
  target.kill('SIGTERM')
  await new Promise<void>((resolvePromise) => {
    const timeout = setTimeout(() => {
      target.kill('SIGKILL')
      resolvePromise()
    }, 3000)
    target.once('exit', () => {
      clearTimeout(timeout)
      resolvePromise()
    })
  })
}
