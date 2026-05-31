import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-import-availability-schedule-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-import-availability-schedule-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  accountImport,
  repositories
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/accounts/account-import.service.js'),
  import('../../storage/repositories.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const schedule = {
  enabled: true,
  timezone: 'UTC',
  windows: [
    { daysOfWeek: [1, 2, 3, 4, 5], start: '22:00', end: '23:55' }
  ]
}

try {
  const importData = {
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    defaults: {
      availabilitySchedule: schedule
    },
    accounts: [
      {
        name: '导入计划继承账户',
        credentials: {
          api_key: 'sk-import-schedule-inherited',
          base_url: 'https://api.openai.com/v1'
        }
      },
      {
        name: '导入计划清空账户',
        availabilitySchedule: null,
        credentials: {
          api_key: 'sk-import-schedule-cleared',
          base_url: 'https://api.openai.com/v1'
        }
      }
    ]
  }

  const preview = accountImport.previewAccountImport(importData, {}, access)
  assert.equal(preview.canImport, true, '带默认自动启停计划的账户导入预览应可导入')
  const result = accountImport.executeAccountImport(importData, {}, access)
  assert.equal(result.imported, true, '带默认自动启停计划的账户导入应成功')

  const inherited = repositories.listAccounts(access, { keyword: '导入计划继承账户', providerCode: 'openai' })
    .find((item) => item.name === '导入计划继承账户')
  assert(inherited, '继承默认计划的导入账户应创建成功')
  assert.equal(inherited.availabilitySchedule?.enabled, true, '账户导入应继承 defaults.availabilitySchedule')
  assert.equal(inherited.availabilitySchedule?.windows?.[0]?.start, '22:00', '账户导入应保存自动启停时段')

  const cleared = repositories.listAccounts(access, { keyword: '导入计划清空账户', providerCode: 'openai' })
    .find((item) => item.name === '导入计划清空账户')
  assert(cleared, '覆盖清空计划的导入账户应创建成功')
  assert.equal(cleared.availabilitySchedule, undefined, '账户级 availabilitySchedule: null 应覆盖并清空默认计划')

  const invalidPreview = accountImport.previewAccountImport({
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    accounts: [
      {
        name: '导入非法计划账户',
        credentials: {
          api_key: 'sk-import-schedule-invalid',
          base_url: 'https://api.openai.com/v1'
        },
        availabilitySchedule: {
          enabled: true,
          timezone: 'UTC',
          windows: [
            { daysOfWeek: [9], start: '22:00', end: '23:55' }
          ]
        }
      }
    ]
  }, {}, access)
  assert.equal(invalidPreview.canImport, false, '非法自动启停计划应阻止账户导入')
  assert.match(invalidPreview.accounts[0]?.messages.join('\n') ?? '', /账户自动启停计划重复日期无效/, '非法计划应返回账户计划语义错误')

  console.log('账户导入自动启停计划回归通过：默认继承、账户级清空和非法计划校验符合预期')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
