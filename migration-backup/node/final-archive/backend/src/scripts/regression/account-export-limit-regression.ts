import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-export-limit-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-export-limit-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

function read(path: string): string {
  return readFileSync(resolve(path), 'utf8')
}

const exportRequest = read('src/modules/accounts/account-export-request.ts')
const exportService = read('src/modules/accounts/account-export.service.ts')
const exportRoutes = read('src/modules/accounts/account-export.routes.ts')
const toolbar = read('../frontend/src/views/accounts/AccountFilterToolbar.vue')
const accountsView = read('../frontend/src/views/accounts/AccountsView.vue')
const exportActions = read('../frontend/src/views/accounts/useAccountExportActions.ts')
const exportHelpers = read('../frontend/src/views/accounts/accountExportHelpers.ts')
const [
  databaseModule,
  repositories,
  { GPT_OPENAI_V1_PROFILE_ID },
  { assertAccountExportMatchCount, exportAccountsForRequest, exportAccountsForRequestAsync }
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../domain/provider-protocol.js'),
  import('../../modules/accounts/account-export-request.js')
])

assert.match(exportService, /export const accountExportMaxAccounts = 500/)
assert.match(exportService, /const accountExportAsyncReadConcurrency = 10/)
assert.match(exportService, /for \(const account of accounts\) \{\s*exportedAccounts\.push\(await exportAccountAsync/s)
assert.match(exportRequest, /const accountExportListPageSize = 200/)
assert.match(exportRequest, /collectAccountExportIds\(/)
assert.match(exportRequest, /collectAccountExportIdsAsync\(/)
assert.match(exportRequest, /assertAccountExportMatchCount\(accountIds\.length\)/)
assert.doesNotMatch(exportRequest, /accountImportMaxAccounts/)
assert.match(exportRoutes, /单次最多导出 \$\{accountExportMaxAccounts\} 个账户/)
assert.match(exportService, /单次最多导出 \$\{accountExportMaxAccounts\} 个 AI 账户/)
assert.doesNotThrow(() => assertAccountExportMatchCount(500))
assert.throws(() => assertAccountExportMatchCount(501), /500/)

const exportButton = toolbar.indexOf('<UploadOutlined />')
const importButton = toolbar.indexOf('<DownloadOutlined />')
assert.ok(exportButton >= 0 && importButton >= 0 && exportButton < importButton, '导出/导入图标必须交换')
assert.match(toolbar, />\s*导出\s*</)
assert.match(toolbar, />\s*导入\s*</)
assert.match(toolbar, /allLoadedSelected\?: boolean/)
assert.match(toolbar, /最多 500 个/)

assert.match(accountsView, /:all-loaded-selected="allLoadedAccountsSelected"/)
assert.match(accountsView, /const allLoadedAccountsSelected = computed\(/)
assert.match(accountsView, /allLoadedAccountsSelected,/)
assert.match(exportActions, /allLoadedAccountsSelected: MaybeRefOrGetter<boolean>/)
assert.match(exportActions, /if \(toValue\(config\.allLoadedAccountsSelected\)\)/)
assert.match(exportActions, /await exportFilteredAccounts\(\)/)
assert.match(exportActions, /单次最多导出 \$\{ACCOUNT_EXPORT_MAX_ACCOUNTS\} 个账户，请先筛选或分批次导出/)
assert.match(exportHelpers, /export const ACCOUNT_EXPORT_MAX_ACCOUNTS = 500/)

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const marker = `account-export-limit-${Date.now().toString(36)}`
const allowedKeyword = `${marker}-allowed`
const allowedAccountIds: string[] = []

try {
  const group = repositories.createGroup({ name: `账户导出上限分组 ${marker}`, providerCode: 'gpt' }, access)
  for (let index = 0; index < 500; index += 1) {
    const account = repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `${allowedKeyword}-${index}`,
      type: 'api_key',
      credentials: { api_key: `sk-export-limit-${index}`, base_url: 'https://api.openai.com/v1' },
      supportedModels: ['gpt-5.5'],
      healthCheckModel: 'gpt-5.5',
      groupId: group.id
    }, access)
    allowedAccountIds.push(account.id)
  }
  repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `${marker}-overflow`,
    type: 'api_key',
    credentials: { api_key: 'sk-export-limit-overflow', base_url: 'https://api.openai.com/v1' },
    supportedModels: ['gpt-5.5'],
    healthCheckModel: 'gpt-5.5',
    groupId: group.id
  }, access)

  const byFilter = exportAccountsForRequest({ filters: { keyword: allowedKeyword } }, access)
  assert.equal(byFilter.summary.matchedAccounts, 500, '500 条筛选匹配必须完整计数')
  assert.equal(byFilter.document.accounts.length, 500, '500 条筛选匹配必须跨 200 条分页完整导出')

  const byIds = exportAccountsForRequest({ accountIds: allowedAccountIds }, access)
  assert.equal(byIds.document.accounts.length, 500, '500 个明确选择的账户必须完整导出')
  assert.throws(
    () => exportAccountsForRequest({ filters: { keyword: marker } }, access),
    /超过单次导出上限 500 个，请先筛选或分批次导出/,
    '501 条筛选匹配必须拒绝导出，不能截断'
  )

  const sharedProxy = repositories.createProxy({
    name: `账户导出共享代理 ${marker}`,
    type: 'http',
    host: 'proxy.example',
    port: 8080,
    username: 'export-user',
    password: 'export-pass',
    enabled: true
  }, access)
  const sharedAccountIds = ['a', 'b'].map((suffix) => repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `${marker}-shared-proxy-${suffix}`,
    type: 'api_key',
    credentials: { api_key: `sk-export-shared-proxy-${suffix}`, base_url: 'https://api.openai.com/v1' },
    supportedModels: ['gpt-5.5'],
    healthCheckModel: 'gpt-5.5',
    groupId: group.id,
    proxyProfileId: sharedProxy.id
  }, access).id)
  const sharedProxyExport = await exportAccountsForRequestAsync({ accountIds: sharedAccountIds }, access)
  assert.equal(sharedProxyExport.document.proxies?.length, 1, '异步导出共享代理时只能输出一次代理定义')
  assert.deepEqual(
    sharedProxyExport.document.accounts.map((account) => account.proxyRef),
    [`proxy-${sharedProxy.id}`, `proxy-${sharedProxy.id}`],
    '异步导出的共享代理账户必须引用同一个 ref'
  )

  console.log('账户导出回归通过：按钮文案/图标、全选筛选导出分支、500/501 边界、200 分页累积和共享代理去重符合预期')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
