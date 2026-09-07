import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-single-read-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-single-read-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const accountGroupBindingRoutesSource = readFileSync(resolve('src/modules/accounts/account-group-binding.routes.ts'), 'utf8')
assert.match(accountGroupBindingRoutesSource, /patchAccountManagementAsync/, '账户单独绑定分组路由必须复用窄字段级 PATCH')
assert.match(accountGroupBindingRoutesSource, /runLoggedOperationAsync/, '账户单独绑定分组路由必须使用 async 操作日志包裹')
assert.doesNotMatch(accountGroupBindingRoutesSource, /findAccountForTestAsync|findAccountSummaryAsync/, '账户单独绑定分组路由不应为日志或校验额外物化完整账户摘要')
assert.doesNotMatch(accountGroupBindingRoutesSource, /import \{[^}]*\bsetAccountGroup\b[^}]*\} from '..\/..\/storage\/repositories\.js'/, '账户单独绑定分组路由不能重新导入同步 setAccountGroup')

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const group = repositories.createGroup({
    name: '账户单条读取回归分组',
    providerCode: 'gpt',
    enabled: true
  }, access)

  let targetId = ''
  for (let index = 0; index < 250; index += 1) {
    const account = repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `账户单条读取回归-${String(index).padStart(3, '0')}`,
      type: 'api_key',
      credentials: {
        api_key: `sk-account-single-read-${index}`,
        base_url: 'https://api.openai.com/v1'
      },
      supportedModels: ['gpt-5.4-mini'],
      groupId: group.id,
      status: 'disabled'
    }, access)
    if (index === 249) {
      targetId = account.id
    }
  }

  const firstPage = repositories.listAccountsPage(access, { page: 1, pageSize: 200 })
  assert.equal(firstPage.items.some((account) => account.id === targetId), false, '第 250 个创建的停用账户不应出现在默认前 200 条列表里')

  const target = repositories.findAccountSummary(targetId, access)
  assert.equal(target?.id, targetId, '按 ID 单条读取应能找到前 200 条之外的停用账户')
  assert.equal(target?.name, '账户单条读取回归-249', '按 ID 单条读取应返回完整账户摘要')
  assert.equal(target?.status, 'disabled', '按 ID 单条读取应可用于删除日志 before 的停用账户快照')

  const accountCountBeforeInvalidCreate = repositories.listAccounts(access).length
  assert.throws(() => repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '账户单条读取回归-非法到期时间',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-single-read-invalid-date',
      base_url: 'https://api.openai.com/v1'
    },
    supportedModels: ['gpt-5.4-mini'],
    groupId: group.id,
    accountExpiresAt: 'not-a-date'
  }, access), /账户套餐到期时间必须是有效时间字符串/, '创建账户时非法到期时间不应被静默当作未设置')
  assert.throws(() => repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '账户单条读取回归-非法日历日期',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-single-read-invalid-calendar-date',
      base_url: 'https://api.openai.com/v1'
    },
    supportedModels: ['gpt-5.4-mini'],
    groupId: group.id,
    accountExpiresAt: '2026-02-31T00:00:00'
  }, access), /账户套餐到期时间必须是有效时间字符串/, '创建账户时不存在的日历日期不应被 Date 自动修正')
  assert.throws(() => repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '账户单条读取回归-非法调度布尔',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-single-read-invalid-schedulable',
      base_url: 'https://api.openai.com/v1'
    },
    supportedModels: ['gpt-5.4-mini'],
    groupId: group.id,
    schedulable: 'false'
  }, access), /账户是否参与调度必须是布尔值/, '创建账户时字符串布尔不应被兼容为调度开关')
  assert.equal(repositories.listAccounts(access).length, accountCountBeforeInvalidCreate, '非法创建账户不应落库')

  assert.throws(() => repositories.updateAccount(targetId, { name: '' }, access), /账户名称不能为空/, '更新账户时空名称不应静默沿用旧名称')
  assert.throws(() => repositories.updateAccount(targetId, { schedulable: 'false' }, access), /账户是否参与调度必须是布尔值/, '更新账户时字符串布尔不应静默沿用旧调度开关')
  assert.throws(() => repositories.updateAccount(targetId, { accountExpiresAt: '2026-02-31T00:00:00' }, access), /账户套餐到期时间必须是有效时间字符串/, '更新账户时不存在的日历日期不应被 Date 自动修正')
  const afterInvalidUpdate = repositories.findAccountSummary(targetId, access)
  assert.equal(afterInvalidUpdate?.name, target?.name, '非法更新账户名称不应改变')
  assert.equal(afterInvalidUpdate?.schedulable, target?.schedulable, '非法更新账户调度开关不应改变')
  assert.equal(afterInvalidUpdate?.accountExpiresAt, target?.accountExpiresAt, '非法更新账户到期时间不应改变')

  assert.equal(repositories.deleteAccount(targetId, access), true, '删除第 200 条之外的停用账户应成功')
  assert.equal(repositories.findAccountSummary(targetId, access), undefined, '删除后按 ID 单条读取应找不到账户')

  console.log('账户单条读取回归通过：删除日志 before 不再依赖前 200 条列表或测试用读取')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
