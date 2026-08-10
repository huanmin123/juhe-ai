import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
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
const sourceServiceImports = [
  {
    label: 'Sub2API',
    mode: 'sub2api' as const,
    expectedName: '来源服务 Sub2API',
    expectedProviderCode: 'openai',
    data: {
      type: 'sub2api-data',
      version: 1,
      accounts: [
        {
          name: '来源服务 Sub2API',
          platform: 'openai',
          type: 'apikey',
          credentials: {
            api_key: 'sk-source-service-sub2api',
            base_url: 'https://api.openai.com/v1',
            runtime_only: 'ignored'
          },
          runtime_only: 'ignored'
        }
      ]
    }
  },
  {
    label: 'NewAPI',
    mode: 'newapi' as const,
    expectedName: '来源服务 NewAPI',
    expectedProviderCode: 'openai',
    data: [
      {
        type: 1,
        name: '来源服务 NewAPI',
        key: 'sk-source-service-newapi',
        base_url: 'https://api.openai.com/v1',
        group: '来源服务 NewAPI 分组',
        runtime_only: 'ignored'
      }
    ]
  },
  {
    label: 'One-API',
    mode: 'oneapi' as const,
    expectedName: '来源服务 OneAPI',
    expectedProviderCode: 'openai',
    data: [
      {
        type: 'openai',
        name: '来源服务 OneAPI',
        key: 'sk-source-service-oneapi',
        base_url: 'https://api.openai.com/v1',
        group: '来源服务 OneAPI 分组',
        runtime_only: 'ignored'
      }
    ]
  },
  {
    label: 'CPA API Key YAML',
    mode: 'cpa' as const,
    expectedName: '来源服务 CPA 1',
    expectedProviderCode: 'openai',
    data: `openai-compatibility:\n  - name: 来源服务 CPA\n    base-url: https://api.openai.com/v1\n    api-key-entries:\n      - api-key: sk-source-service-cpa\n        runtime_only: ignored\n`
  },
  {
    label: 'CPA Codex OAuth',
    mode: 'cpa' as const,
    expectedName: '来源服务 CPA OAuth',
    expectedProviderCode: 'gpt',
    data: {
      type: 'codex',
      name: '来源服务 CPA OAuth',
      refresh_token: 'rt-source-service-cpa',
      account_id: 'acct-source-service-cpa',
      base_url: 'https://api.openai.com/v1',
      runtime_only: 'ignored'
    }
  }
]
const importGroupName = '导入回归分组'

function assertAccountImportRouteBoundary(): void {
  const mainRouteSource = readFileSync(resolve('src', 'modules', 'accounts', 'accounts.routes.ts'), 'utf8')
  const importRouteSource = readFileSync(resolve('src', 'modules', 'accounts', 'account-import.routes.ts'), 'utf8')
  const importServiceSource = readFileSync(resolve('src', 'modules', 'accounts', 'account-import.service.ts'), 'utf8')
  const resourceResolverSource = readFileSync(resolve('src', 'modules', 'accounts', 'account-import-resource-resolver.ts'), 'utf8')
  const providerResolverSource = readFileSync(resolve('src', 'modules', 'accounts', 'account-import-provider-resolver.ts'), 'utf8')
  const modelCatalogSource = readFileSync(resolve('src', 'modules', 'accounts', 'account-import-model-catalog.ts'), 'utf8')
  const createPayloadSource = readFileSync(resolve('src', 'modules', 'accounts', 'account-import-account-payload.ts'), 'utf8')
  const resourceCreatorSource = readFileSync(resolve('src', 'modules', 'accounts', 'account-import-resource-creator.ts'), 'utf8')
  const accountCreatorSource = readFileSync(resolve('src', 'modules', 'accounts', 'account-import-account-creator.ts'), 'utf8')
  const proxyPlanSource = readFileSync(resolve('src', 'modules', 'accounts', 'account-import-proxy-plan.ts'), 'utf8')
  const accountPlanSource = readFileSync(resolve('src', 'modules', 'accounts', 'account-import-account-plan.ts'), 'utf8')
  const rootValidationSource = readFileSync(resolve('src', 'modules', 'accounts', 'account-import-root-validation.ts'), 'utf8')
  const executorSource = readFileSync(resolve('src', 'modules', 'accounts', 'account-import-executor.ts'), 'utf8')

  assert(
    mainRouteSource.includes('registerAccountImportRoutes(accountsRouter)'),
    '账户主路由必须只通过 registerAccountImportRoutes 注册导入路由'
  )
  assert(
    !mainRouteSource.includes("accountsRouter.post('/import/preview'") &&
      !mainRouteSource.includes("accountsRouter.post('/import/confirm'"),
    '账户导入 preview/confirm 路由不应回退到 accounts.routes.ts'
  )
  assert(importRouteSource.includes("router.post('/import/preview'"), '账户导入子路由必须保留 preview 入口')
  assert(importRouteSource.includes("router.post('/import/confirm'"), '账户导入子路由必须保留 confirm 入口')
  assert(importRouteSource.includes('accountImportRequestSchema.safeParse'), '账户导入子路由必须负责请求体 schema 校验')
  assert(importRouteSource.includes('previewAccountImportAsync'), '账户导入子路由必须调用 async 导入预览服务')
  assert(importRouteSource.includes('executeAccountImportAsync'), '账户导入子路由必须调用 async 导入执行服务')
  assert(importRouteSource.includes("operationKey: 'accounts.import'"), '账户导入确认必须保留幂等与操作日志 key')
  assert(importRouteSource.includes('runLoggedOperationAsync'), '账户导入确认必须保留 async 操作日志记录边界')
  assert(!/\brunLoggedOperation\(/.test(importRouteSource), '账户导入 HTTP 入口不应回退到同步操作日志边界')
  assert(!importRouteSource.includes('createAccount('), '账户导入子路由不应直接创建账户')
  assert(!importRouteSource.includes('updateAccount('), '账户导入子路由不应包含普通账户编辑逻辑')
  assert(!importRouteSource.includes('deleteAccountWithRelatedCleanup('), '账户导入子路由不应包含普通账户删除逻辑')
  assert(importServiceSource.includes('export async function previewAccountImportAsync'), '账户导入 service 必须提供 async 预览入口')
  assert(importServiceSource.includes('export async function executeAccountImportAsync'), '账户导入 service 必须提供 async 执行入口')
  assert(importServiceSource.includes('listProvidersAsync'), '账户导入 async 计划构建必须使用 async 供应商读取')
  assert(importServiceSource.includes("from './account-import-root-validation.js'"), '账户导入 service 必须使用根对象校验 helper')
  assert(importServiceSource.includes('validateAccountImportRoot'), '账户导入 service 必须调用根对象校验 helper')
  assert(!importServiceSource.includes('appendUnknownFieldMessages'), '账户导入根未知字段校验不应回退到 service')
  assert(!importServiceSource.includes('importRootKeys'), '账户导入根字段白名单不应回退到 service')
  assert(!importServiceSource.includes('hasOwnField(data'), '账户导入根数组字段判断不应回退到 service')
  assert(!importServiceSource.includes('导入内容必须是 JSON 对象'), '账户导入根类型错误文案不应回退到 service')
  assert(!importServiceSource.includes('accounts 至少需要 1 条账户'), '账户导入 accounts 空数组校验不应回退到 service')
  assert(!importServiceSource.includes('proxies 必须是数组'), '账户导入 proxies 数组校验不应回退到 service')
  assert(rootValidationSource.includes('export function validateAccountImportRoot'), '账户导入根校验 helper 必须承接根对象校验')
  assert(rootValidationSource.includes('appendUnknownFieldMessages'), '账户导入根校验 helper 必须保留未知字段校验')
  assert(rootValidationSource.includes('importRootKeys'), '账户导入根校验 helper 必须保留根字段白名单')
  assert(rootValidationSource.includes('accounts 至少需要 1 条账户'), '账户导入根校验 helper 必须保留 accounts 空数组校验')
  assert(rootValidationSource.includes('proxies 必须是数组'), '账户导入根校验 helper 必须保留 proxies 数组校验')
  assert(importServiceSource.includes("from './account-import-resource-resolver.js'"), '账户导入 service 必须使用资源解析 helper')
  assert(!importServiceSource.includes('function resolveAccountGroup('), '账户导入分组解析不应回退到 service')
  assert(!importServiceSource.includes('function resolveAccountProxy('), '账户导入代理解析不应回退到 service')
  assert(!importServiceSource.includes('listGroupOptions('), '账户导入 service 不应直接扫描分组选项')
  assert(!importServiceSource.includes('listProxyOptions('), '账户导入 service 不应直接扫描代理选项')
  assert(!importServiceSource.includes('findGroupSummary('), '账户导入 service 不应直接读取分组摘要')
  assert(!importServiceSource.includes('findProxy('), '账户导入 service 不应直接读取代理详情')
  assert(resourceResolverSource.includes('export function resolveAccountGroup'), '账户导入资源解析 helper 必须承接分组解析')
  assert(resourceResolverSource.includes('export async function resolveAccountGroupAsync'), '账户导入资源解析 helper 必须承接 async 分组解析')
  assert(resourceResolverSource.includes('export function resolveAccountProxy'), '账户导入资源解析 helper 必须承接代理解析')
  assert(resourceResolverSource.includes('export async function resolveAccountProxyAsync'), '账户导入资源解析 helper 必须承接 async 代理解析')
  assert(resourceResolverSource.includes('listGroupOptions('), '账户导入资源解析 helper 必须保留分组选项查找')
  assert(resourceResolverSource.includes('listGroupOptionsAsync('), '账户导入资源解析 helper 必须保留 async 分组选项查找')
  assert(resourceResolverSource.includes('listProxyOptions('), '账户导入资源解析 helper 必须保留代理选项查找')
  assert(resourceResolverSource.includes('listProxyOptionsAsync('), '账户导入资源解析 helper 必须保留 async 代理选项查找')
  assert(importServiceSource.includes("from './account-import-proxy-plan.js'"), '账户导入 service 必须使用代理 plan helper')
  assert(!importServiceSource.includes('function planProxy('), '账户导入代理字段解析不应回退到 service')
  assert(!importServiceSource.includes('normalizeProxyType'), '账户导入 service 不应直接规范化代理类型')
  assert(!importServiceSource.includes('findProxyOptionByName'), '账户导入 service 不应直接判断代理同名复用')
  assert(!importServiceSource.includes('代理 ref 重复'), '账户导入代理 ref 重复处理不应回退到 service')
  assert(proxyPlanSource.includes('export function planImportProxies'), '账户导入代理 plan helper 必须承接代理计划构建')
  assert(proxyPlanSource.includes('export async function planImportProxiesAsync'), '账户导入代理 plan helper 必须承接 async 代理计划构建')
  assert(proxyPlanSource.includes('normalizeProxyType'), '账户导入代理 plan helper 必须保留代理类型规范化')
  assert(proxyPlanSource.includes('findProxyOptionByName'), '账户导入代理 plan helper 必须保留代理同名复用判断')
  assert(proxyPlanSource.includes('代理 ref 重复'), '账户导入代理 plan helper 必须保留代理 ref 重复处理')
  assert(importServiceSource.includes("from './account-import-account-plan.js'"), '账户导入 service 必须使用账户 plan helper')
  assert(!importServiceSource.includes('function planAccount('), '账户导入账户字段解析不应回退到 service')
  assert(!importServiceSource.includes('normalizeAccountCredentialsForWrite'), '账户导入 service 不应直接归一化账户凭据')
  assert(!importServiceSource.includes('normalizeStatus'), '账户导入 service 不应直接规范化账户状态')
  assert(!importServiceSource.includes('importAvailabilityScheduleInput'), '账户导入 service 不应直接解析账户时间计划')
  assert(accountPlanSource.includes('export function planImportAccount'), '账户导入账户 plan helper 必须承接账户计划构建')
  assert(accountPlanSource.includes('export async function planImportAccountAsync'), '账户导入账户 plan helper 必须承接 async 账户计划构建')
  assert(accountPlanSource.includes('normalizeAccountCredentialsForWrite'), '账户导入账户 plan helper 必须保留账户凭据归一化')
  assert(accountPlanSource.includes('validateImportAccountProviderAndBasics'), '账户导入账户 plan helper 必须保留 provider / profile 基础校验')
  assert(accountPlanSource.includes('validateAccountModelCatalogFields'), '账户导入账户 plan helper 必须保留模型目录校验')
  assert(accountPlanSource.includes('validateAccountModelCatalogFieldsAsync'), '账户导入账户 plan helper 必须保留 async 模型目录校验')
  assert(accountPlanSource.includes('resolveAccountGroup'), '账户导入账户 plan helper 必须保留分组解析')
  assert(accountPlanSource.includes('resolveAccountGroupAsync'), '账户导入账户 plan helper 必须保留 async 分组解析')
  assert(accountPlanSource.includes('resolveAccountProxy'), '账户导入账户 plan helper 必须保留代理引用解析')
  assert(accountPlanSource.includes('resolveAccountProxyAsync'), '账户导入账户 plan helper 必须保留 async 代理引用解析')
  assert(accountPlanSource.includes("from './account-import-provider-resolver.js'"), '账户导入账户 plan helper 必须使用 provider 解析 helper')
  assert(!importServiceSource.includes('function validateAccountBasics('), '账户导入基础校验不应回退到 service')
  assert(!importServiceSource.includes('function resolveImportAccountProtocolProfile('), '账户导入协议档案解析不应回退到 service')
  assert(!importServiceSource.includes('optionalServerDateTimeIso'), '账户导入 service 不应直接做基础时间格式校验')
  assert(providerResolverSource.includes('export function validateImportAccountProviderAndBasics'), '账户导入 provider helper 必须承接 provider / profile 基础校验')
  assert(providerResolverSource.includes('optionalServerDateTimeIso'), '账户导入 provider helper 必须保留账户到期时间格式校验')
  assert(accountPlanSource.includes("from './account-import-model-catalog.js'"), '账户导入账户 plan helper 必须使用模型目录 helper')
  assert(!importServiceSource.includes('function validateAccountModelCatalogFields('), '账户导入模型目录校验不应回退到 service')
  assert(!importServiceSource.includes('normalizeAccountSupportedModelsForProvider'), '账户导入 service 不应直接规范化 supportedModels')
  assert(!importServiceSource.includes('normalizeAccountModelMappingsForProvider'), '账户导入 service 不应直接规范化 modelMappings')
  assert(modelCatalogSource.includes('export function validateAccountModelCatalogFields'), '账户导入模型目录 helper 必须承接模型目录校验')
  assert(modelCatalogSource.includes('export async function validateAccountModelCatalogFieldsAsync'), '账户导入模型目录 helper 必须承接 async 模型目录校验')
  assert(modelCatalogSource.includes('normalizeAccountSupportedModelsForProvider'), '账户导入模型目录 helper 必须保留 supportedModels 规范化')
  assert(modelCatalogSource.includes('normalizeAccountModelMappingsForProvider'), '账户导入模型目录 helper 必须保留 modelMappings 规范化')
  assert(accountCreatorSource.includes("from './account-import-account-payload.js'"), '账户导入账户创建 helper 必须使用创建 payload helper')
  assert(!importServiceSource.includes('const accountInput: Record<string, unknown> = {'), '账户导入创建 payload 拼装不应回退到 service')
  assert(!importServiceSource.includes('function accountImportCreateStatus('), '账户导入创建状态转换不应回退到 service')
  assert(createPayloadSource.includes('export function buildAccountImportCreatePayload'), '账户导入创建 payload helper 必须承接创建 payload 拼装')
  assert(createPayloadSource.includes("return status === 'active' ? 'pending_test' : status"), '账户导入创建 payload helper 必须保留 active 导入转 pending_test 语义')
  assert(importServiceSource.includes("from './account-import-executor.js'"), '账户导入 service 必须使用导入执行 helper')
  assert(importServiceSource.includes('executeAccountImportPlan'), '账户导入 service 必须调用导入执行 helper')
  assert(importServiceSource.includes('executeAccountImportPlanAsync'), '账户导入 service 必须调用 async 导入执行 helper')
  assert(!importServiceSource.includes("from './account-import-resource-creator.js'"), '账户导入 service 不应直接使用资源创建 helper')
  assert(!importServiceSource.includes("from './account-import-account-creator.js'"), '账户导入 service 不应直接使用账户创建 helper')
  assert(!importServiceSource.includes('createPlannedImportProxies'), '账户导入代理创建执行不应回退到 service')
  assert(!importServiceSource.includes('createPlannedImportGroups'), '账户导入分组创建执行不应回退到 service')
  assert(!importServiceSource.includes('failAccountsWithUnresolvedImportProxy'), '账户导入代理创建失败联动账户不应回退到 service')
  assert(!importServiceSource.includes('createPlannedImportAccounts'), '账户导入账户创建执行不应回退到 service')
  assert(executorSource.includes('export function executeAccountImportPlan'), '账户导入执行 helper 必须承接导入执行编排')
  assert(executorSource.includes('export async function executeAccountImportPlanAsync'), '账户导入执行 helper 必须承接 async 导入执行编排')
  assert(executorSource.includes('createPlannedImportProxies'), '账户导入执行 helper 必须调用代理创建执行')
  assert(executorSource.includes('failAccountsWithUnresolvedImportProxy'), '账户导入执行 helper 必须调用代理失败联动账户')
  assert(executorSource.includes('createPlannedImportGroups'), '账户导入执行 helper 必须调用分组创建执行')
  assert(executorSource.includes('createPlannedImportAccounts'), '账户导入执行 helper 必须调用账户创建执行')
  assert(executorSource.includes('plan.result.imported = true'), '账户导入执行 helper 必须保留导入完成标记')
  assert(executorSource.includes('plan.result.canImport = false'), '账户导入执行 helper 必须保留导入后不可重复导入标记')
  assert(!importServiceSource.includes('function createPlannedProxies('), '账户导入代理创建执行不应回退到 service')
  assert(!importServiceSource.includes('function createPlannedGroups('), '账户导入分组创建执行不应回退到 service')
  assert(!importServiceSource.includes('function failAccountsWithUnresolvedProxy('), '账户导入代理创建失败联动账户不应回退到 service')
  assert(resourceCreatorSource.includes('export function createPlannedImportProxies'), '账户导入资源创建 helper 必须承接代理创建执行')
  assert(resourceCreatorSource.includes('export async function createPlannedImportProxiesAsync'), '账户导入资源创建 helper 必须承接 async 代理创建执行')
  assert(resourceCreatorSource.includes('export function createPlannedImportGroups'), '账户导入资源创建 helper 必须承接分组创建执行')
  assert(resourceCreatorSource.includes('export async function createPlannedImportGroupsAsync'), '账户导入资源创建 helper 必须承接 async 分组创建执行')
  assert(resourceCreatorSource.includes('export function failAccountsWithUnresolvedImportProxy'), '账户导入资源创建 helper 必须承接代理失败联动账户')
  assert(resourceCreatorSource.includes('createProxy('), '账户导入资源创建 helper 必须保留代理创建调用')
  assert(resourceCreatorSource.includes('createGroup('), '账户导入资源创建 helper 必须保留分组创建调用')
  assert(!importServiceSource.includes('createAccount('), '账户导入账户创建调用不应回退到 service')
  assert(!importServiceSource.includes('function isDuplicateAccountError('), '账户导入重复账户错误处理不应回退到 service')
  assert(!importServiceSource.includes('function groupIdForAccount('), '账户导入创建阶段分组 ID 兜底不应回退到 service')
  assert(accountCreatorSource.includes('export function createPlannedImportAccounts'), '账户导入账户创建 helper 必须承接账户创建执行')
  assert(accountCreatorSource.includes('export async function createPlannedImportAccountsAsync'), '账户导入账户创建 helper 必须承接 async 账户创建执行')
  assert(accountCreatorSource.includes('buildAccountImportCreatePayload'), '账户导入账户创建 helper 必须调用创建 payload helper')
  assert(accountCreatorSource.includes('createAccount('), '账户导入账户创建 helper 必须保留账户创建调用')
  assert(accountCreatorSource.includes('createAccountAsync('), '账户导入账户创建 helper 必须保留 async 账户创建调用')
  assert(accountCreatorSource.includes('isDuplicateAccountError'), '账户导入账户创建 helper 必须保留重复账户处理')
}

assertAccountImportRouteBoundary()

try {
	  repositories.createGroup({
	    name: importGroupName,
	    providerCode: 'gpt',
	  }, access)

  const importData = {
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    accounts: [
      {
	        name: '导入计划账户',
	        providerCode: 'gpt',
	        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
	        type: 'api_key',
        status: 'active',
        groupName: importGroupName,
        supportedModels: ['gpt-5.5'],
        healthCheckModel: 'gpt-5.5',
        availabilitySchedule: schedule,
        credentials: {
          api_key: 'sk-import-schedule-explicit',
          base_url: 'https://api.openai.com/v1'
        }
      },
      {
	        name: '导入无计划账户',
	        providerCode: 'gpt',
	        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
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
  assert.equal(preview.canImport, true, '显式时间计划的账户导入预览应可导入')
  const result = accountImport.executeAccountImport(importData, {}, access)
  assert.equal(result.imported, true, '显式时间计划的账户导入应成功')

  const asyncImportData = {
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    accounts: [
      {
	        name: '导入 async 计划账户',
	        providerCode: 'gpt',
	        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
	        type: 'api_key',
        status: 'active',
        groupName: importGroupName,
        availabilitySchedule: schedule,
        credentials: {
          api_key: 'sk-import-schedule-async',
          base_url: 'https://api.openai.com/v1'
        }
      }
    ]
  }
  const asyncPreview = await accountImport.previewAccountImportAsync(asyncImportData, {}, access)
  assert.equal(asyncPreview.canImport, true, 'async 导入预览应可导入')
  const asyncResult = await accountImport.executeAccountImportAsync(asyncImportData, {}, access)
  assert.equal(asyncResult.imported, true, 'async 导入执行应成功')

  const scheduled = repositories.listAccounts(access, { keyword: '导入计划账户', providerCode: 'gpt' })
    .find((item) => item.name === '导入计划账户')
  assert(scheduled, '显式计划的导入账户应创建成功')
  assert.equal(scheduled.availabilitySchedule?.enabled, true, '账户导入应保存账户级 availabilitySchedule')
  assert.equal(scheduled.availabilitySchedule?.windows?.[0]?.start, '22:00', '账户导入应保存可用时段时段')
  assert.equal(scheduled.healthCheckModel, 'gpt-5.5', '账户导入应保存账户级检查模型')

  const withoutSchedule = repositories.listAccounts(access, { keyword: '导入无计划账户', providerCode: 'gpt' })
    .find((item) => item.name === '导入无计划账户')
  assert(withoutSchedule, '未配置计划的导入账户应创建成功')
  assert.equal(withoutSchedule.availabilitySchedule, undefined, '未填写 availabilitySchedule 时账户不应生成计划')
  assert(withoutSchedule.supportedModels?.includes('gpt-5.5'), '未填写 supportedModels 时账户导入应按供应商默认支持模型回填')

  const asyncScheduled = repositories.listAccounts(access, { keyword: '导入 async 计划账户', providerCode: 'gpt' })
    .find((item) => item.name === '导入 async 计划账户')
  assert(asyncScheduled, 'async 导入账户应创建成功')
  assert.equal(asyncScheduled.availabilitySchedule?.enabled, true, 'async 账户导入应保存账户级 availabilitySchedule')

  for (const sourceImport of sourceServiceImports) {
    const sourcePreview = await accountImport.previewAccountImportAsync(sourceImport.data, {}, access, sourceImport.mode)
    assert.equal(sourcePreview.source.mode, sourceImport.mode, `${sourceImport.label} 的来源摘要应保留导入模式`)
    assert.equal(sourcePreview.source.accepted, 1, `${sourceImport.label} 应接受一条可导入账户`)
    assert.equal(sourcePreview.canImport, true, `${sourceImport.label} 经过实际导入服务预览后应可导入：${JSON.stringify(sourcePreview)}`)
    assert.equal(sourcePreview.summary.accounts.create, 1, `${sourceImport.label} 应计划创建一条账户`)

    const sourceResult = await accountImport.executeAccountImportAsync(sourceImport.data, {}, access, sourceImport.mode)
    assert.equal(sourceResult.imported, true, `${sourceImport.label} 经过实际导入服务确认后应成功`)
    const importedSourceAccount = repositories.listAccounts(access, {
      keyword: sourceImport.expectedName,
      providerCode: sourceImport.expectedProviderCode
    }).find((item) => item.name === sourceImport.expectedName)
    assert(importedSourceAccount, `${sourceImport.label} 账户应通过来源适配器落库`)
  }

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
  assert.equal(invalidPreview.canImport, false, '非法时间计划应阻止账户导入')
  assert.match(invalidPreview.accounts[0]?.messages.join('\n') ?? '', /账户时间计划重复日期无效/, '非法计划应返回账户计划语义错误')

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
  assert.match(unknownCredentialPreview.accounts[0]?.messages.join('\n') ?? '', /账户凭据包含不支持的字段：apiKey/, '凭据旧字段应在预览阶段返回明确错误')

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
      },
      {
	        name: '导入账户空支持模型',
	        providerCode: 'gpt',
	        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
	        type: 'api_key',
        status: 'pending_test',
        groupName: importGroupName,
        supportedModels: [],
        credentials: {
          api_key: 'sk-import-account-empty-supported-models',
          base_url: 'https://api.openai.com/v1'
        }
      },
      {
        name: '导入账户非法检查模型',
        providerCode: 'gpt',
        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
        type: 'api_key',
        status: 'pending_test',
        groupName: importGroupName,
        supportedModels: ['gpt-5.5'],
        healthCheckModel: 'gpt-4o-mini',
        credentials: {
          api_key: 'sk-import-account-invalid-default-test-model',
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
  assert.match(strictAccountPreview.accounts[2]?.messages.join('\n') ?? '', /账户 supportedModels必须是非空字符串数组/, '账户 supportedModels 显式空数组不应按省略处理')
  assert.match(strictAccountPreview.accounts[3]?.messages.join('\n') ?? '', /账户 healthCheckModel 必须属于 supportedModels/, '账户检查模型不在支持列表时应阻止导入')

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

  console.log('账户导入回归通过：原生计划与字段契约、Sub2API/NewAPI/One-API/CPA 实际服务导入、非法输入和小批量边界校验符合预期')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
