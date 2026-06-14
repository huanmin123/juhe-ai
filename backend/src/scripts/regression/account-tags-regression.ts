import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import type { Server } from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-tags-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-tags-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  accountExport,
  accountImport,
  { accountsRouter },
  { forceSelfAccessScope, requireAuth },
  { requestContextMiddleware }
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/accounts/account-export.service.js'),
  import('../../modules/accounts/account-import.service.js'),
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const groupName = '标签回归分组'
const importGroupName = '标签导入回归分组'
let server: Server | undefined

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-accounts', forceSelfAccessScope, accountsRouter)

function assertAccountTagsRouteBoundary(): void {
  const mainRouteSource = readFileSync(resolve('src', 'modules', 'accounts', 'accounts.routes.ts'), 'utf8')
  const tagsRouteSource = readFileSync(resolve('src', 'modules', 'accounts', 'account-tags.routes.ts'), 'utf8')

  assert(
    mainRouteSource.includes('registerAccountTagsRoutes(accountsRouter)'),
    '账户主路由必须通过 registerAccountTagsRoutes 注册标签路由'
  )
  assert(!mainRouteSource.includes("accountsRouter.get('/tags'"), '账户标签列表路由不应回退到 accounts.routes.ts')
  assert(!mainRouteSource.includes("accountsRouter.delete('/tags/:tagId'"), '账户标签删除路由不应回退到 accounts.routes.ts')
  assert(!mainRouteSource.includes('listAccountTags'), '账户主路由不应直接读取账户标签')
  assert(!mainRouteSource.includes('deleteAccountTag'), '账户主路由不应直接删除账户标签')
  assert(!mainRouteSource.includes('AccountTagInUseError'), '账户主路由不应处理账户标签删除约束')
  assert(tagsRouteSource.includes("router.get('/tags'"), '账户标签子路由必须保留标签列表入口')
  assert(tagsRouteSource.includes("router.delete('/tags/:tagId'"), '账户标签子路由必须保留标签删除入口')
  assert(tagsRouteSource.includes('AccountTagInUseError'), '账户标签子路由必须保留绑定标签删除约束错误处理')
}

assertAccountTagsRouteBoundary()

try {
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('账户标签回归服务地址不可用')
  }
  const baseUrl = `http://127.0.0.1:${address.port}`

  const group = repositories.createGroup({
    name: groupName,
    providerCode: 'gpt'
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    name: '标签回归账户',
    type: 'api_key',
    credentials: { api_key: 'sk-account-tags-regression', base_url: 'https://api.openai.com/v1' },
    groupId: group.id,
    tags: ['主力', 'API', ' 主力 ']
  }, access)
  repositories.createAccount({
    providerCode: 'gpt',
    name: '标签未命中账户',
    type: 'api_key',
    credentials: { api_key: 'sk-account-tags-unmatched', base_url: 'https://api.openai.com/v1' },
    groupId: group.id
  }, access)

  assert.deepEqual(sortedTagNames(account.tags), sortedText(['API', '主力']), '创建账户应去重并保存多个标签')
  const tagOptions = repositories.listAccountTags(access)
  const mainTag = tagOptions.find((tag) => tag.name === '主力')
  const apiTag = tagOptions.find((tag) => tag.name === 'API')
  assert(mainTag, '标签下拉应返回已创建标签')
  assert(apiTag, '标签下拉应返回 API 标签')
  assert.equal(mainTag.accountCount, 1, '已绑定标签应返回绑定账户数')
  assert.deepEqual(
    repositories.listAccountsPage(access, { tagIds: [mainTag.id], page: 1, pageSize: 10 }).items.map((item) => item.name),
    ['标签回归账户'],
    '账户列表应支持按标签筛选'
  )
  assert.deepEqual(
    repositories.listAccountsPage(access, { tagIds: [mainTag.id, apiTag.id], page: 1, pageSize: 10 }).items.map((item) => item.name),
    ['标签回归账户'],
    '账户列表多标签筛选应命中任一标签'
  )
  assert.throws(
    () => repositories.deleteAccountTag(mainTag.id, access),
    /标签已绑定账户，不能删除/,
    '已绑定账户的标签不能删除'
  )

  const updatedTags = repositories.updateAccountTags(account.id, ['备用', 'API'], access)
  assert.deepEqual(sortedTagNames(updatedTags), sortedText(['API', '备用']), '独立标签更新应替换账户标签')
  const mainTagAfterUnbind = repositories.listAccountTags(access).find((tag) => tag.name === '主力')
  assert.equal(mainTagAfterUnbind?.accountCount, 0, '解除绑定后原标签账户数应归零')
  assert.equal(
    repositories.listAccountsPage(access, { tagIds: [mainTag.id], page: 1, pageSize: 10 }).items.length,
    0,
    '解除绑定后按原标签筛选不应继续返回账户'
  )
  assert.equal(repositories.deleteAccountTag(mainTag.id, access), true, '未绑定账户的标签应允许删除')

  const exported = accountExport.exportAccountsAsImportDocument({ accountIds: [account.id] }, access)
  assert.deepEqual(
    sortedText(exported.document.accounts[0]?.tags ?? []),
    sortedText(['API', '备用']),
    '账户导出应保留标签'
  )

  repositories.createGroup({
    name: importGroupName,
    providerCode: 'gpt'
  }, access)
  const importData = {
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    accounts: [
      {
        name: '标签导入账户',
        providerCode: 'gpt',
        type: 'api_key',
        status: 'pending_test',
        groupName: importGroupName,
        tags: ['导入', 'API'],
        credentials: {
          api_key: 'sk-account-tags-import',
          base_url: 'https://api.openai.com/v1'
        }
      }
    ]
  }
  const preview = accountImport.previewAccountImport(importData, {}, access)
  assert.equal(preview.canImport, true, '带标签的账户导入预览应可导入')
  const importResult = accountImport.executeAccountImport(importData, {}, access)
  assert.equal(importResult.imported, true, '带标签的账户导入应成功')
  const imported = repositories.listAccounts(access, { keyword: '标签导入账户', providerCode: 'gpt' })
    .find((item) => item.name === '标签导入账户')
  assert(imported, '导入账户应创建成功')
  assert.deepEqual(sortedTagNames(imported.tags), sortedText(['API', '导入']), '导入账户应保存标签绑定')

  const invalidPreview = accountImport.previewAccountImport({
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    accounts: [
      {
        name: '标签非法导入账户',
        providerCode: 'gpt',
        type: 'api_key',
        status: 'pending_test',
        groupName: importGroupName,
        tags: ['正常', 123],
        credentials: {
          api_key: 'sk-account-tags-invalid',
          base_url: 'https://api.openai.com/v1'
        }
      }
    ]
  }, {}, access)
  assert.equal(invalidPreview.canImport, false, '非法标签数组应阻止账户导入')
  assert.match(invalidPreview.accounts[0]?.messages.join('\n') ?? '', /账户 tags必须是字符串数组/, '非法标签应返回明确错误')

  await assertHiddenAuthorizationInstanceTagsNotMutated(baseUrl)

  console.log('账户标签回归通过：创建、更新、删除约束、导入导出均符合预期')
} finally {
  await closeServer(server)
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function sortedTagNames(tags: Array<{ name: string }> | undefined): string[] {
  return sortedText((tags ?? []).map((tag) => tag.name))
}

function sortedText(value: string[]): string[] {
  return [...value].sort((left, right) => left.localeCompare(right, 'zh-Hans-CN'))
}

async function assertHiddenAuthorizationInstanceTagsNotMutated(baseUrl: string): Promise<void> {
  const owner = repositories.createSystemAccount({
    username: 'account_tags_owner',
    displayName: '账户标签所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'account_tags_grantee',
    displayName: '账户标签被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const ownerGroup = repositories.createGroup({
    name: '标签可见性来源分组',
    providerCode: 'gpt'
  }, ownerAccess)
  const granteeGroup = repositories.createGroup({
    name: '标签可见性被授权人分组',
    providerCode: 'gpt'
  }, granteeAccess)
  const ownerAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '标签可见性来源账户',
    type: 'api_key',
    credentials: { api_key: 'sk-account-tags-visibility', base_url: 'https://api.openai.com/v1' },
    groupId: ownerGroup.id
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: ownerAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '账户标签隐藏授权实例回归'
  }, ownerAccess)
  const authorizedAccount = authorizedInstanceForSource(ownerAccount.id, granteeAccess)
  const localTags = repositories.updateAccountTags(authorizedAccount.id, ['授权本地标签'], granteeAccess)
  const localTag = localTags?.find((tag) => tag.name === '授权本地标签')
  assert(localTag, '被授权用户应可给授权实例配置自己的本地标签')
  assert.equal(
    repositories.listAccountTags(granteeAccess).find((tag) => tag.id === localTag.id)?.accountCount,
    1,
    '授权实例可见时本地标签应计入绑定账户数'
  )
  repositories.returnAccountAuthorizationInstanceForGrantee(authorizedAccount.id, granteeAccess)
  assert.equal(
    repositories.listAccounts(granteeAccess).some((account) => account.id === authorizedAccount.id),
    false,
    '归还后的授权实例不应继续可见'
  )
  assert.equal(
    repositories.listAccountTags(granteeAccess).find((tag) => tag.id === localTag.id)?.accountCount,
    0,
    '归还后的授权实例不应继续占用本地标签删除约束'
  )
  assert.equal(
    repositories.deleteAccountTag(localTag.id, granteeAccess),
    true,
    '只绑定了已归还授权实例的标签应允许删除'
  )

  const status = await patchStatus(baseUrl, `/__aisys__/api/my-accounts/${authorizedAccount.id}/tags`, sessionCookie(grantee.id), {
    tags: ['隐藏写入']
  })
  assert.equal(status, 404, '不可见授权实例标签更新应返回 404')
  assert.equal(
    repositories.listAccountTags(granteeAccess).some((tag) => tag.name === '隐藏写入'),
    false,
    '不可见授权实例标签更新失败时不应创建标签或绑定'
  )
}

function authorizedInstanceForSource(sourceAccountId: string, access: { systemAccountId: string; role: 'user' }): { id: string } {
  const account = repositories.listAccounts(access).find((item) => item.authorizationInstanceSourceAccountId === sourceAccountId)
  assert(account, `被授权用户视角应能读取来源账户 ${sourceAccountId} 的授权实例`)
  return account
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function patchStatus(baseUrl: string, path: string, cookie: string, payload: unknown): Promise<number> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'PATCH',
    headers: {
      'content-type': 'application/json',
      cookie
    },
    body: JSON.stringify(payload)
  })
  await response.arrayBuffer()
  return response.status
}

function onceListening(serverToListen: Server): Promise<void> {
  return new Promise((resolve, reject) => {
    serverToListen.once('listening', resolve)
    serverToListen.once('error', reject)
  })
}

function closeServer(serverToClose: Server | undefined): Promise<void> {
  return new Promise((resolve) => {
    if (!serverToClose) {
      resolve()
      return
    }
    serverToClose.close(() => resolve())
  })
}
