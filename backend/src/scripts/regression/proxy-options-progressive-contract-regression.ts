import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = '0'

const tempRoot = resolve(tmpdir(), `juhe-ai-proxy-options-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'proxy-options-progressive-contract-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.sqliteReadWorkerPoolSize = 1
runtimeConfig.sqliteReadWorkerQueueMaxItems = 8
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [expressModule, databaseModule, proxyRepository, readWorkerPool, proxyRoutes] = await Promise.all([
  import('express'),
  import('../../storage/database.js'),
  import('../../storage/proxy.repository.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../modules/proxies/proxies.routes.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const server = createServer(expressModule.default().use('/proxies', proxyRoutes.proxiesRouter))

try {
  const first = proxyRepository.createProxy({
    name: 'Alpha Shared A', type: 'http', host: '127.0.0.1', port: 18_081, password: 'secret-1', enabled: true
  }, access)
  const second = proxyRepository.createProxy({
    name: 'Alpha Shared B', type: 'https', host: '127.0.0.1', port: 18_082, password: 'secret-2', enabled: true
  }, access)
  const third = proxyRepository.createProxy({
    name: 'Alpha Shared C', type: 'socks5', host: '127.0.0.1', port: 18_083, password: 'secret-3', enabled: true
  }, access)
  const selected = proxyRepository.createProxy({
    name: 'Zeta Selected', type: 'socks5h', host: '127.0.0.1', port: 18_084, password: 'secret-selected', enabled: true
  }, access)
  const disabled = proxyRepository.createProxy({
    name: 'Y Disabled', type: 'http', host: '127.0.0.1', port: 18_085, password: 'secret-disabled', enabled: false
  }, access)

  const database = databaseModule.getBusinessDatabase()
  database.prepare('UPDATE proxy_profiles SET updated_at = ?, password_encrypted = ? WHERE id = ?')
    .run('2026-07-21T03:00:00.000Z', 'not-valid-ciphertext', first.id)
  database.prepare('UPDATE proxy_profiles SET updated_at = ?, password_encrypted = ? WHERE id = ?')
    .run('2026-07-21T02:00:00.000Z', 'not-valid-ciphertext', second.id)
  database.prepare('UPDATE proxy_profiles SET updated_at = ?, password_encrypted = ? WHERE id = ?')
    .run('2026-07-21T01:00:00.000Z', 'not-valid-ciphertext', third.id)
  database.prepare('UPDATE proxy_profiles SET password_encrypted = ? WHERE id IN (?, ?)')
    .run('not-valid-ciphertext', selected.id, disabled.id)

  const requestedOptions = {
    keyword: 'Alpha',
    limit: 1,
    selectedIds: [` ${third.id} `, selected.id, second.id, second.id, disabled.id, 'proxy_missing']
  }
  // name ASC 窗口只有 A，selectedIds 再补齐 B/C 与已选启用项
  const expected = [first.id, second.id, third.id, selected.id]

  const direct = proxyRepository.listProxyOptionsReadOnly(requestedOptions)
  assert.deepEqual(direct.map((item) => item.id), expected, 'SQLite 主线程应合并启用已选项并统一稳定排序')
  assert.deepEqual(direct.map((item) => Object.keys(item).sort()), direct.map(() => ['enabled', 'id', 'name', 'type']), 'options 只能返回字段白名单')
  assert(direct.every((item) => item.enabled === true), '停用和不存在的 selectedIds 必须静默省略')

  const worker = await proxyRepository.listProxyOptionsAsync(requestedOptions)
  assert.deepEqual(worker, direct, 'SQLite read worker 必须与主线程逐字段一致')

  await new Promise<void>((resolveListen) => server.listen(0, '127.0.0.1', resolveListen))
  const address = server.address()
  assert(address && typeof address === 'object', 'HTTP 测试服务器应成功监听')
  const baseUrl = `http://127.0.0.1:${address.port}/proxies/options`

  const bare = await requestOptions(`${baseUrl}?keyword=Alpha&limit=1&selectedIds=${encodeURIComponent(third.id)}&selectedIds=${encodeURIComponent(selected.id)}&selectedIds=${encodeURIComponent(second.id)}`)
  assert.equal(bare.status, 200, '重复裸键 selectedIds 应成功')
  assert.deepEqual(optionIds(bare.body), expected, '重复裸键 selectedIds 应补齐并排序')

  const brackets = await requestOptions(`${baseUrl}?keyword=Alpha&limit=1&selectedIds[]=${encodeURIComponent(third.id)}&selectedIds[]=${encodeURIComponent(selected.id)}&selectedIds[]=${encodeURIComponent(second.id)}`)
  assert.equal(brackets.status, 200, 'bracket 数组 selectedIds[] 应成功')
  assert.deepEqual(optionIds(brackets.body), expected, 'bracket 数组 selectedIds[] 应补齐并排序')

  const invalidLimit = await requestOptions(`${baseUrl}?keyword=Alpha&limit=1.5`)
  assert.equal(invalidLimit.status, 200, '非整数 limit 应按未传处理')
  assert.equal(optionIds(invalidLimit.body).length, 3, '非整数 limit 应使用默认窗口而不是截断')

  for (const query of [
    `selectedIds=${encodeURIComponent(`${first.id},${second.id}`)}`,
    `selectedIds=${encodeURIComponent(JSON.stringify([first.id]))}`,
    `selectedIds[id]=${encodeURIComponent(first.id)}`,
    `selectedIds=${encodeURIComponent('x'.repeat(121))}`,
    Array.from({ length: 21 }, (_item, index) => `selectedIds=proxy_${index}`).join('&')
  ]) {
    const response = await requestOptions(`${baseUrl}?${query}`)
    assert.equal(response.status, 400, `非法 selectedIds 必须返回 400：${query.slice(0, 80)}`)
  }

  console.log('代理 options 渐进加载契约回归通过：Node HTTP、SQLite 主线程与 read worker 语义一致')
} finally {
  await new Promise<void>((resolveClose) => server.close(() => resolveClose()))
  await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  for (let attempt = 0; attempt < 5; attempt += 1) {
    try {
      rmSync(tempRoot, { recursive: true, force: true })
      break
    } catch (error) {
      if (attempt === 4) throw error
      await delay(100 * (attempt + 1))
    }
  }
}

async function requestOptions(url: string): Promise<{ status: number; body: unknown }> {
  const response = await fetch(url)
  return { status: response.status, body: await response.json() }
}

function optionIds(body: unknown): string[] {
  assert(body && typeof body === 'object' && 'data' in body, '响应必须包含 data')
  const data = (body as { data?: unknown }).data
  assert(Array.isArray(data), '响应 data 必须为数组')
  return data.map((item) => {
    assert(item && typeof item === 'object' && typeof (item as { id?: unknown }).id === 'string', 'option 必须包含字符串 id')
    assert.deepEqual(Object.keys(item).sort(), ['enabled', 'id', 'name', 'type'], 'HTTP option 只能返回字段白名单')
    return (item as { id: string }).id
  })
}
