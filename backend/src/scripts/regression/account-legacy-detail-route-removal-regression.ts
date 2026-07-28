import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import type { Server } from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-legacy-detail-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-legacy-detail-route-removal-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const detailRouteSource = readFileSync(new URL('../../modules/accounts/account-detail.routes.ts', import.meta.url), 'utf8')
const frontendAccountApiSource = readFileSync(new URL('../../../../frontend/src/api/domains/accounts.ts', import.meta.url), 'utf8')

assert.doesNotMatch(detailRouteSource, /router\.get\(['"]\/:id['"]/, '账户路由不得重新注册无场景的 legacy GET /:id')
assert.doesNotMatch(detailRouteSource, /findAccountSummaryAsync|applyServerAccountRuntimeToAccount|sanitizeAccountBasicDetailResponse/, '账户详情路由不得物化 full summary 后再裁剪')
assert.doesNotMatch(frontendAccountApiSource, /^\s*detail:\s*\(id: string/m, '前端账户 API 不得保留无人消费的 legacy detail 方法')

const [{ createSystemApiApp }, { closeSqliteReadWorkerPool }, databaseModule, repositories] = await Promise.all([
  import('../../modules/system-api/system-api-app.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

let server: Server | undefined

try {
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  repositories.updateSystemAccount(admin.id, { mustChangePassword: false })
  const access = { systemAccountId: admin.id, role: 'admin' as const }
  const group = repositories.createGroup({
    name: '旧详情路由移除回归分组',
    providerCode: 'gpt'
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '旧详情路由移除回归账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-legacy-detail-removal',
      base_url: 'https://api.openai.com/v1'
    },
    supportedModels: ['gpt-5.4-mini'],
    healthCheckModel: 'gpt-5.4-mini',
    groupId: group.id,
    status: 'disabled'
  }, access)
  const cookie = `juhe_ai_session=${repositories.createSession(admin.id, 1).token}`

  const app = createSystemApiApp({ systemApiPrefix: '/__aisys__/api' })
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('旧详情路由回归服务地址不可用')
  const baseUrl = `http://127.0.0.1:${address.port}/__aisys__/api`

  for (const prefix of ['accounts', 'my-accounts']) {
    await expectStatus(`${baseUrl}/${prefix}/${account.id}`, cookie, 404, `${prefix} legacy detail`)
    await expectStatus(`${baseUrl}/${prefix}/${account.id}/unknown-child`, cookie, 404, `${prefix} unknown child`)
    const editBasic = await requestJson(`${baseUrl}/${prefix}/${account.id}/edit-basic`, cookie)
    assert.equal(editBasic.status, 200, `${prefix} edit-basic 不应受 legacy 路由移除影响：${editBasic.text}`)
    assert.equal((JSON.parse(editBasic.text) as { data?: { id?: string } }).data?.id, account.id)
    await expectStatus(`${baseUrl}/${prefix}/${account.id}/advanced`, cookie, 200, `${prefix} advanced`)
  }
  await expectStatus(`${baseUrl}/accounts/tags`, cookie, 200, 'accounts static tags route')
  await expectStatus(`${baseUrl}/my-accounts/tags`, cookie, 200, 'my-accounts static tags route')

  console.log('账户 legacy 详情路由移除回归通过：管理/个人单账户 GET 固定 404，窄详情与静态子路由保持可用')
} finally {
  await closeServer(server)
  await closeSqliteReadWorkerPool()
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

async function requestJson(url: string, cookie: string): Promise<{ status: number; text: string }> {
  const response = await fetch(url, { headers: { cookie } })
  return { status: response.status, text: await response.text() }
}

async function expectStatus(url: string, cookie: string, expectedStatus: number, label: string): Promise<void> {
  const response = await requestJson(url, cookie)
  assert.equal(response.status, expectedStatus, `${label} HTTP ${response.status}: ${response.text}`)
}

async function onceListening(listeningServer: NonNullable<typeof server>): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.once('listening', resolvePromise)
    listeningServer.once('error', rejectPromise)
  })
}

async function closeServer(listeningServer?: typeof server): Promise<void> {
  if (!listeningServer?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}
