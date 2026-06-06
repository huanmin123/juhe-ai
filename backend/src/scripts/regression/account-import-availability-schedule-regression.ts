import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
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
  mode: 'allow_windows',
  windows: [
    { daysOfWeek: [1, 2, 3, 4, 5], start: '22:00', end: '23:55' }
  ]
}
const importGroupName = '导入回归分组'

try {
  repositories.createGroup({
    name: importGroupName,
    providerCode: 'openai'
  }, access)

  const importData = {
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    accounts: [
      {
        name: '导入计划账户',
        providerCode: 'openai',
        type: 'api_key',
        status: 'active',
        groupName: importGroupName,
        availabilitySchedule: schedule,
        credentials: {
          api_key: 'sk-import-schedule-explicit',
          base_url: 'https://api.openai.com/v1'
        }
      },
      {
        name: '导入无计划账户',
        providerCode: 'openai',
        type: 'api_key',
        status: 'active',
        groupName: importGroupName,
        credentials: {
          api_key: 'sk-import-schedule-empty',
          base_url: 'https://api.openai.com/v1'
        }
      }
    ]
  }

  const preview = accountImport.previewAccountImport(importData, {}, access)
  assert.equal(preview.canImport, true, '显式可用时段计划的账户导入预览应可导入')
  const result = accountImport.executeAccountImport(importData, {}, access)
  assert.equal(result.imported, true, '显式可用时段计划的账户导入应成功')

  const scheduled = repositories.listAccounts(access, { keyword: '导入计划账户', providerCode: 'openai' })
    .find((item) => item.name === '导入计划账户')
  assert(scheduled, '显式计划的导入账户应创建成功')
  assert.equal(scheduled.availabilitySchedule?.enabled, true, '账户导入应保存账户级 availabilitySchedule')
  assert.equal(scheduled.availabilitySchedule?.windows?.[0]?.start, '22:00', '账户导入应保存可用时段时段')

  const withoutSchedule = repositories.listAccounts(access, { keyword: '导入无计划账户', providerCode: 'openai' })
    .find((item) => item.name === '导入无计划账户')
  assert(withoutSchedule, '未配置计划的导入账户应创建成功')
  assert.equal(withoutSchedule.availabilitySchedule, undefined, '未填写 availabilitySchedule 时账户不应生成计划')

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
          mode: 'allow_windows',
          windows: [
            { daysOfWeek: [9], start: '22:00', end: '23:55' }
          ]
        }
      }
    ]
  }, {}, access)
  assert.equal(invalidPreview.canImport, false, '非法可用时段计划应阻止账户导入')
  assert.match(invalidPreview.accounts[0]?.messages.join('\n') ?? '', /账户可用时段计划重复日期无效/, '非法计划应返回账户计划语义错误')

  const unknownCredentialPreview = accountImport.previewAccountImport({
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    accounts: [
      {
        name: '导入凭据旧字段账户',
        credentials: {
          api_key: 'sk-import-unknown-credential',
          base_url: 'https://api.openai.com/v1',
          apiKey: 'legacy-key'
        }
      }
    ]
  }, {}, access)
  assert.equal(unknownCredentialPreview.canImport, false, '凭据旧字段应阻止账户导入')
  assert.match(unknownCredentialPreview.accounts[0]?.messages.join('\n') ?? '', /账户凭据包含未知字段：apiKey/, '凭据旧字段应在预览阶段返回明确错误')

  const unknownRootPreview = accountImport.previewAccountImport({
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    legacyDefaults: {},
    accounts: [
      {
        name: '导入根未知字段账户',
        credentials: {
          api_key: 'sk-import-root-unknown',
          base_url: 'https://api.openai.com/v1'
        }
      }
    ]
  }, {}, access)
  assert.equal(unknownRootPreview.canImport, false, '导入根对象未知字段不应被静默忽略')
  assert.match(unknownRootPreview.messages.join('\n'), /导入内容包含未知字段：legacyDefaults/, '导入根对象未知字段应返回明确错误')

  const defaultsPreview = accountImport.previewAccountImport({
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    defaults: {
      status: 'archived',
      concurrencyLimit: '20'
    },
    accounts: [
      {
        name: '导入非法 defaults 账户',
        credentials: {
          api_key: 'sk-import-invalid-defaults',
          base_url: 'https://api.openai.com/v1'
        }
      }
    ]
  }, {}, access)
  assert.equal(defaultsPreview.canImport, false, '导入 defaults 不应被继续支持')
  assert.match(defaultsPreview.messages.join('\n'), /导入内容包含未知字段：defaults/, '顶层 defaults 应在预览阶段被拒绝')

  const strictAccountPreview = accountImport.previewAccountImport({
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    accounts: [
      {
        name: '导入账户未知字段',
        legacyStatus: 'active',
        credentials: {
          api_key: 'sk-import-account-unknown',
          base_url: 'https://api.openai.com/v1'
        }
      },
      {
        name: '导入账户非法值',
        status: 1,
        concurrencyLimit: '20',
        priority: -1,
        superPriorityEnabled: 'true',
        supportedModels: ['gpt-5.4', 123],
        accountExpiresAt: '2026-02-31T00:00:00',
        credentials: {
          api_key: 'sk-import-account-invalid-values',
          base_url: 'https://api.openai.com/v1'
        }
      }
    ]
  }, {}, access)
  assert.equal(strictAccountPreview.canImport, false, '导入账户非法字段不应被默认值或空值兜底')
  assert.match(strictAccountPreview.accounts[0]?.messages.join('\n') ?? '', /账户配置包含未知字段：legacyStatus/, '账户未知字段应在预览阶段失败')
  assert.match(strictAccountPreview.accounts[1]?.messages.join('\n') ?? '', /账户 status必须是字符串/, '账户 status 非字符串不应回退默认状态')
  assert.match(strictAccountPreview.accounts[1]?.messages.join('\n') ?? '', /账户 concurrencyLimit必须是整数/, '账户 concurrencyLimit 字符串不应被兼容为数字')
  assert.match(strictAccountPreview.accounts[1]?.messages.join('\n') ?? '', /账户 priority必须是大于等于 0 的整数/, '账户 priority 负数不应延后到创建阶段才失败')
  assert.match(strictAccountPreview.accounts[1]?.messages.join('\n') ?? '', /账户 superPriorityEnabled必须是布尔值/, '账户布尔字段不应接收字符串')
  assert.match(strictAccountPreview.accounts[1]?.messages.join('\n') ?? '', /账户 supportedModels必须是非空字符串数组/, '账户 supportedModels 不应过滤非法成员后继续导入')
  assert.match(strictAccountPreview.accounts[1]?.messages.join('\n') ?? '', /账户 accountExpiresAt必须是有效时间字符串/, '账户不存在的日历日期不应被 Date 自动修正')

  const strictProxyPreview = accountImport.previewAccountImport({
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    proxies: [
      {
        ref: 'strict-proxy',
        name: '导入代理非法值',
        type: 'socks5h',
        host: '127.0.0.1',
        port: '1080',
        enabled: 'true',
        legacyProxyField: true
      }
    ],
    accounts: [
      {
        name: '导入代理非法值账户',
        credentials: {
          api_key: 'sk-import-proxy-invalid-values',
          base_url: 'https://api.openai.com/v1'
        }
      }
    ]
  }, {}, access)
  assert.equal(strictProxyPreview.canImport, false, '导入代理非法字段不应被默认值或空值兜底')
  assert.match(strictProxyPreview.proxies[0]?.messages.join('\n') ?? '', /代理配置包含未知字段：legacyProxyField/, '代理未知字段应在预览阶段失败')
  assert.match(strictProxyPreview.proxies[0]?.messages.join('\n') ?? '', /代理 port必须是整数/, '代理 port 字符串不应被兼容为数字')
  assert.match(strictProxyPreview.proxies[0]?.messages.join('\n') ?? '', /代理 enabled必须是布尔值/, '代理 enabled 字符串不应被兼容为布尔值')

  const missingBaseUrlPreview = accountImport.previewAccountImport({
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    defaults: {
      baseUrl: 'https://api.openai.com/v1'
    },
    accounts: [
      {
        name: '导入缺失 Base URL 账户',
        credentials: {
          api_key: 'sk-import-missing-base-url'
        }
      }
    ]
  }, {}, access)
  assert.equal(missingBaseUrlPreview.canImport, false, 'defaults.baseUrl 不应再为账户凭据补默认 Base URL')
  assert.match(missingBaseUrlPreview.messages.join('\n'), /导入内容包含未知字段：defaults/, '旧 defaults 字段应在预览阶段失败')

  const overAccountLimitPreview = accountImport.previewAccountImport({
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    accounts: Array.from({ length: accountImport.accountImportMaxAccounts + 1 }, (_, index) => ({
      name: `导入超限账户 ${index + 1}`,
      credentials: {
        api_key: `sk-import-over-account-limit-${index + 1}`,
        base_url: 'https://api.openai.com/v1'
      }
    }))
  }, {}, access)
  assert.equal(overAccountLimitPreview.canImport, false, '超过账户导入批量上限应阻止导入')
  assert.match(overAccountLimitPreview.messages.join('\n'), /accounts 单次最多导入 50 条/, '账户导入批量上限应保持小批次边界')

  const overProxyLimitPreview = accountImport.previewAccountImport({
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    proxies: Array.from({ length: accountImport.accountImportMaxProxies + 1 }, (_, index) => ({
      ref: `proxy-over-limit-${index + 1}`,
      name: `导入超限代理 ${index + 1}`,
      type: 'http',
      host: '127.0.0.1',
      port: 7000 + index,
      enabled: true
    })),
    accounts: [
      {
        name: '导入代理超限账户',
        credentials: {
          api_key: 'sk-import-over-proxy-limit',
          base_url: 'https://api.openai.com/v1'
        }
      }
    ]
  }, {}, access)
  assert.equal(overProxyLimitPreview.canImport, false, '超过代理导入批量上限应阻止导入')
  assert.match(overProxyLimitPreview.messages.join('\n'), /proxies 单次最多导入 20 条/, '代理导入批量上限应保持小批次边界')

  const frontendImportModalSource = readFileSync(resolve('..', 'frontend', 'src', 'views', 'accounts', 'AccountImportModal.vue'), 'utf8')
  const frontendImportTemplateSource = frontendImportModalSource.slice(
    frontendImportModalSource.indexOf('const importTemplate = JSON.stringify({'),
    frontendImportModalSource.indexOf('}, null, 2)', frontendImportModalSource.indexOf('const importTemplate = JSON.stringify({'))
  )
  assert(!frontendImportTemplateSource.includes('metadata:'), '前端账户导入模板不应包含后端协议拒绝的 metadata 根字段')

  console.log('账户导入可用时段计划回归通过：显式账户计划、非法计划、字段白名单、凭据契约和小批量边界校验符合预期')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}


