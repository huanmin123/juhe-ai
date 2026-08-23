import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-proxy-single-read-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'proxy-single-read-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])
const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  let targetId = ''
  for (let index = 0; index < 250; index += 1) {
    const proxy = repositories.createProxy({
      name: `代理单条读取回归-${String(index).padStart(3, '0')}`,
      type: 'http',
      host: '127.0.0.1',
      port: 10_000 + index,
      enabled: true
    }, access)
    if (index === 0) {
      targetId = proxy.id
    }
  }

  databaseModule.getBusinessDatabase()
    .prepare("UPDATE proxy_profiles SET updated_at = '2000-01-01T00:00:00.000Z' WHERE id = ?")
    .run(targetId)

  const firstPageLikeList = repositories.listProxies().slice(0, 200)
  assert.equal(firstPageLikeList.some((proxy) => proxy.id === targetId), false, '最早创建的第 250 条外代理不应出现在前 200 条列表窗口里')

  const target = repositories.findProxy(targetId)
  assert.equal(target?.id, targetId, '按 ID 单条读取应能找到前 200 条之外的代理')
  assert.equal(target?.name, '代理单条读取回归-000', '按 ID 单条读取应返回完整代理摘要')

  repositories.updateProxy(targetId, { username: 'proxy-user', password: ' p@ss ' })
  assert.equal(
    repositories.getProxyTestConfig(targetId)?.proxyUrl,
    'http://proxy-user:%20p%40ss%20@127.0.0.1:10000',
    '代理密码作为凭据写入时应保留前后空格'
  )
  assert.throws(
    () => repositories.updateProxy(targetId, { password: '   ' }),
    /代理密码不能为空/,
    '纯空白代理密码不应被当成清空或空对象写入'
  )
  assert.throws(
    () => repositories.updateProxy(targetId, { password: undefined }),
    /代理密码不能为空/,
    '显式 undefined 代理密码不应写成空凭据对象'
  )
  assert.equal(
    repositories.getProxyTestConfig(targetId)?.proxyUrl,
    'http://proxy-user:%20p%40ss%20@127.0.0.1:10000',
    '非法代理密码更新不应覆盖旧密码'
  )

  const asyncListed = await repositories.listProxiesPageAsync({ page: 1, pageSize: 20, keyword: '代理单条读取回归-000' })
  assert.equal(asyncListed.items.some((proxy) => proxy.id === targetId), true, 'async 列表 fallback 应能读取代理')
  assert.equal((await repositories.findProxyAsync(targetId))?.id, targetId, 'async 单条读取 fallback 应能按 ID 找到代理')
  assert.equal((await repositories.listProxyOptionsAsync({ keyword: '代理单条读取回归-000' })).some((proxy) => proxy.id === targetId), true, 'async options fallback 应能读取启用代理')
  const asyncUpdated = await repositories.updateProxyAsync(targetId, { description: 'async fallback 更新' })
  assert.equal(asyncUpdated?.description, 'async fallback 更新', 'async 更新 fallback 应返回更新后的代理')
  assert.equal((await repositories.getProxyTestConfigAsync(targetId))?.proxyUrl, 'http://proxy-user:%20p%40ss%20@127.0.0.1:10000', 'async 检测配置 fallback 应保留代理 URL')
  assert.equal((await repositories.resolveProxyUrlForProfileAsync(targetId)), 'http://proxy-user:%20p%40ss%20@127.0.0.1:10000', 'async 代理 URL fallback 应解析凭据')

  const proxyRepositorySource = readFileSync(new URL('../../storage/proxy.repository.ts', import.meta.url), 'utf8')
  const proxyRoutesSource = readFileSync(new URL('../../modules/proxies/proxies.routes.ts', import.meta.url), 'utf8')
  const dbServiceHandlersSource = readFileSync(new URL('../../modules/db-service/db-service-handlers.ts', import.meta.url), 'utf8')
  assert(proxyRepositorySource.includes('listProxiesPageAsync'), '代理仓储必须提供 async 分页读取')
  assert(proxyRepositorySource.includes('createProxyAsync'), '代理仓储必须提供 async 创建')
  assert(proxyRepositorySource.includes('patchProxyForManagementAsync'), '代理仓储必须提供字段级 async 管理更新')
  assert(proxyRepositorySource.includes('deleteProxyForManagementAsync'), '代理仓储必须提供全局 async 管理删除')
  assert(proxyRoutesSource.includes('await listProxiesPageAsync'), '代理管理列表路由必须走 async 仓储')
  assert(proxyRoutesSource.includes('await createProxyAsync'), '代理创建路由必须走 async 仓储')
  assert(proxyRoutesSource.includes('await patchProxyForManagementAsync'), '代理更新路由必须走字段级 async 仓储')
  assert(proxyRoutesSource.includes('await deleteProxyForManagementAsync'), '代理删除路由必须走全局 async 仓储')
  assert.doesNotMatch(proxyRepositorySource, /updateProxyTestState/, 'Node 仓储不得保留 J3a business writer')
  assert.doesNotMatch(dbServiceHandlersSource, /update_proxy_test_state|updateProxyTestState/, 'Node DB service 不得保留 J3a writer operation')

  assert.equal(await repositories.deleteProxyAsync(targetId), true, 'async 删除代理 fallback 应成功')
  assert.equal(await repositories.findProxyAsync(targetId), undefined, 'async 删除后按 ID 单条读取应找不到代理')

  console.log('代理单条读取回归通过：CRUD、凭据配置、异步单条读取和删除不依赖全量代理列表；Node J3a writer 已删除')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
