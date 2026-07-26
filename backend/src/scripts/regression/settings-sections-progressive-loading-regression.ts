import assert from 'node:assert/strict'
import type http from 'node:http'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-settings-sections-'))
const repositorySource = readFileSync(new URL('../../storage/settings.repository.ts', import.meta.url), 'utf8')
assert(repositorySource.includes('await refreshSystemSettingsCacheAfterSectionWrite()'), '系统 section PATCH 必须等待重建完整 settings cache')
assert(repositorySource.includes('await refreshGlobalSettingsCacheAfterSectionWrite()'), '品牌 section PATCH 必须等待重建完整 global cache')
process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
process.env.JUHE_AI_DATABASE_DRIVER = 'sqlite'
process.env.JUHE_AI_CACHE_DRIVER = 'memory'
process.env.JUHE_AI_DATABASE_PATH = join(tempRoot, 'business.sqlite3')
process.env.JUHE_AI_DATASET_DATABASE_PATH = join(tempRoot, 'dataset.sqlite3')
process.env.JUHE_AI_STATS_DATABASE_PATH = join(tempRoot, 'stats.sqlite3')
runtimeConfig.processRole = 'db-service'
runtimeConfig.workerRole = 'worker'
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.sqliteReadWorkerPoolSize = 1

const database = await import('../../storage/database.js')
database.getBusinessDatabase()
const repository = await import('../../storage/settings.repository.js')
const readWorkerPool = await import('../../storage/sqlite-read-worker-pool.js')
let server: http.Server | undefined

try {
  for (const sectionKey of Object.keys(repository.managementSettingsSectionCatalog) as Array<keyof typeof repository.managementSettingsSectionCatalog>) {
    const result = await repository.getManagementSettingsSectionAsync(sectionKey)
    assert.deepEqual(Object.keys(result).sort(), [...repository.managementSettingsSectionCatalog[sectionKey].keys].sort(), `${sectionKey} 只能返回本分区字段`)
  }
  const gatewayCore = await repository.getManagementSettingsSectionAsync('gateway-core')
  assert.equal(gatewayCore.imageRequestWallTimeoutSeconds, 3600, '通用网关图片整请求总时限默认值必须为 60 分钟')
  assert.equal(gatewayCore.chatImageGenerationTotalTimeoutSeconds, 900, 'AI 对话生图总超时默认值必须为 15 分钟')
  assert(readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs > 0, 'SQLite section GET 必须实际经过 read-worker')

  const [{ createSystemApiApp }, repositories] = await Promise.all([
    import('../../modules/system-api/system-api-app.js'),
    import('../../storage/repositories.js')
  ])
  const cookie = `juhe_ai_session=${repositories.createSession('sys_admin', 1).token}`
  server = createSystemApiApp({ systemApiPrefix: '/__aisys__/api', bypassSystemApiRateLimitForTest: true }).listen(0, '127.0.0.1')
  await new Promise<void>((resolve, reject) => {
    server!.once('listening', resolve)
    server!.once('error', reject)
  })
  const address = server.address()
  assert(address && typeof address === 'object')
  const baseUrl = `http://127.0.0.1:${address.port}/__aisys__/api`
  const handledBeforeHttp = readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs
  const httpGet = await fetch(`${baseUrl}/settings/sections/account-test`, { headers: { cookie } })
  assert.equal(httpGet.status, 200)
  const httpGetBody = await httpGet.json() as { data: { sectionKey: string; values: Record<string, unknown> } }
  assert.equal(httpGetBody.data.sectionKey, 'account-test')
  assert.deepEqual(Object.keys(httpGetBody.data.values), ['accountTestTaskConcurrency'])
  assert(readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs > handledBeforeHttp, 'HTTP section GET 必须实际投递 read-worker')
  const unauthorized = await fetch(`${baseUrl}/settings/sections/account-test`)
  assert.equal(unauthorized.status, 401, 'section GET 必须保持认证边界')
  const unknown = await fetch(`${baseUrl}/settings/sections/not-a-section`, { headers: { cookie } })
  assert.equal(unknown.status, 400, '未知 section 必须返回 400')

  const before = await repository.getSettingsAsync()
  const previousLimit = Number(before.systemApiRateLimitIpReadPerMinute)
  const updated = await repository.updateManagementSettingsSectionAsync('api-rate-limit', {
    systemApiRateLimitIpReadPerMinute: previousLimit + 1
  })
  assert.equal(updated.systemApiRateLimitIpReadPerMinute, previousLimit + 1)
  assert.deepEqual(Object.keys(updated).sort(), [...repository.managementSettingsSectionCatalog['api-rate-limit'].keys].sort(), 'PATCH 不得返回完整 settings 快照')
  const after = await repository.getSettingsAsync()
  assert.equal(after.systemApiRateLimitIpReadPerMinute, previousLimit + 1)
  assert.equal(after.accountHealthCheckIntervalHours, before.accountHealthCheckIntervalHours, '部分 PATCH 不得覆盖未提交 section')

  await assert.rejects(
    () => repository.updateManagementSettingsSectionAsync('api-rate-limit', { accountHealthCheckIntervalHours: 1 }),
    /不允许的字段/,
    '跨 section 字段必须拒绝'
  )
  await assert.rejects(
    () => repository.updateManagementSettingsSectionAsync('gateway-core', { imageRequestWallTimeoutSeconds: 59 }),
    /必须在 60 到 86400 之间/,
    '通用网关图片整请求总时限必须执行系统设置范围校验'
  )
  await assert.rejects(
    () => repository.updateManagementSettingsSectionAsync('gateway-core', { chatImageGenerationTotalTimeoutSeconds: 59 }),
    /必须在 60 到 86400 之间/,
    'AI 对话生图总超时必须执行系统设置范围校验'
  )
  await assert.rejects(
    () => repository.updateManagementSettingsSectionAsync('api-rate-limit', {}),
    /不能为空/,
    '空 PATCH 必须拒绝'
  )

  await repository.updateManagementSettingsSectionAsync('api-rate-limit', {
    systemApiRateLimitIpReadPerMinute: previousLimit
  })
  console.log('settings sections progressive loading regression passed')
} finally {
  try {
    if (server) await new Promise<void>((resolve) => server!.close(() => resolve()))
    await readWorkerPool.closeSqliteReadWorkerPool()
    database.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
