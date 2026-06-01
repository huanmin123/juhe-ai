import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { createSystemApiApp, systemApiJsonBodyLimit } from '../../modules/system-api/system-api-app.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-system-api-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'system-api.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'system-api-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const databaseModule = await import('../../storage/database.js')

let server: http.Server | undefined
try {
  const app = createSystemApiApp({ systemApiPrefix: '/__aisys__/api' })
  server = app.listen(0, '127.0.0.1')
  await listen(server)
  const baseUrl = `http://127.0.0.1:${serverAddress(server).port}`

  const health = await getJson<{ status: string; service: string }>(`${baseUrl}/__aisys__/api/health`)
  assert.equal(health.status, 'ok', 'DB service system API health 应返回 ok')
  assert.equal(health.service, 'juhe-ai-db-service', 'DB service system API health 应标识内部服务')

  const publicSettings = await getJson<{ data: { appName?: string } }>(`${baseUrl}/__aisys__/api/settings/public`)
  assert.equal(publicSettings.data.appName, '聚合 AI', '公开设置应由 DB service system API 直接读取')

  assert.equal(systemApiJsonBodyLimit, '256kb', 'DB service system API JSON 请求体上限应保持 256KB')
  const largeBodyResponse = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ username: 'admin', password: 'secret', padding: 'x'.repeat(280 * 1024) })
  })
  assert.equal(largeBodyResponse.status, 413, 'DB service system API 应拒绝超过 256KB 的 JSON 请求体')
  const largeBodyError = await largeBodyResponse.json() as { message?: string }
  assert.equal(largeBodyError.message, '请求体过大', '超限 JSON 应返回中文请求体过大错误')

  console.log('DB service system API HTTP 回归通过：内部 health、公开设置与 256KB JSON 请求体上限可用')
} finally {
  await closeServer(server)
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

async function getJson<T>(url: string): Promise<T> {
  const response = await fetch(url)
  assert.equal(response.status, 200, `${url} 应返回 200`)
  return await response.json() as T
}

async function listen(server: http.Server): Promise<void> {
  if (server.listening) return
  await new Promise<void>((resolve, reject) => {
    server.once('listening', resolve)
    server.once('error', reject)
  })
}

async function closeServer(server?: http.Server): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolve, reject) => {
    server.close((error) => {
      if (error) {
        reject(error)
        return
      }
      resolve()
    })
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  assert(address && typeof address !== 'string', '测试服务器应监听 TCP 地址')
  return { port: address.port }
}
