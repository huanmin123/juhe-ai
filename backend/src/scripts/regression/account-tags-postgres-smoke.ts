import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  AccountTagInUseError,
  createAccountAsync,
  createGroupAsync,
  deleteAccountAsync,
  deleteAccountTagAsync,
  deleteGroupAsync,
  listAccountTagsAsync,
  updateAccountTagsAsync
} from '../../storage/repositories.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '账户标签 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `${Date.now().toString(36)}_${Math.random().toString(16).slice(2, 8)}`
const access: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const createdAccountIds: string[] = []
const createdGroupIds: string[] = []
const createdTagNames = [
  `pgA-${marker}`,
  `pgB-${marker}`,
  `pgC-${marker}`
]

try {
  const group = await createGroupAsync({
    name: `账户标签 PG smoke 分组 ${marker}`,
    providerCode: 'gpt'
  }, access)
  createdGroupIds.push(group.id)

  const account = await createAccountAsync({
    providerCode: 'gpt',
    providerProtocolProfileId: group.providerProtocolProfileId,
    name: `账户标签 PG smoke 账户 ${marker}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-account-tags-pg-smoke-${marker}`,
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    tags: [createdTagNames[0], createdTagNames[1], ` ${createdTagNames[0]} `],
    status: 'disabled'
  }, access)
  createdAccountIds.push(account.id)
  assert.deepEqual(sortedTagNames(account.tags), sortedText([createdTagNames[0], createdTagNames[1]]), 'PG 创建账户应去重并保存标签')

  const initialTags = await listAccountTagsAsync(access)
  const tagA = requiredTag(initialTags, createdTagNames[0])
  const tagB = requiredTag(initialTags, createdTagNames[1])
  assert.equal(tagA.accountCount, 1, 'PG 标签列表应统计绑定账户数')
  assert.equal(tagB.accountCount, 1, 'PG 第二个标签也应统计绑定账户数')

  await assert.rejects(
    () => deleteAccountTagAsync(tagA.id, access),
    AccountTagInUseError,
    'PG 已绑定账户的标签不能删除'
  )

  const updatedTags = await updateAccountTagsAsync(account.id, [createdTagNames[1], createdTagNames[2]], access)
  assert.deepEqual(sortedTagNames(updatedTags), sortedText([createdTagNames[1], createdTagNames[2]]), 'PG 独立标签更新应替换账户标签')

  const afterReplaceTags = await listAccountTagsAsync(access)
  const tagAAfterReplace = requiredTag(afterReplaceTags, createdTagNames[0])
  const tagCAfterReplace = requiredTag(afterReplaceTags, createdTagNames[2])
  assert.equal(tagAAfterReplace.accountCount, 0, 'PG 标签解除绑定后账户数应归零')
  assert.equal(tagCAfterReplace.accountCount, 1, 'PG 新标签应统计绑定账户数')
  assert.equal(await deleteAccountTagAsync(tagAAfterReplace.id, access), true, 'PG 未绑定标签应允许删除')

  const clearedTags = await updateAccountTagsAsync(account.id, [], access)
  assert.deepEqual(clearedTags, [], 'PG 清空账户标签应返回空数组')
  const afterClearTags = await listAccountTagsAsync(access)
  for (const name of [createdTagNames[1], createdTagNames[2]]) {
    const tag = requiredTag(afterClearTags, name)
    assert.equal(tag.accountCount, 0, `PG 清空后 ${name} 账户数应归零`)
    assert.equal(await deleteAccountTagAsync(tag.id, access), true, `PG 清空后 ${name} 应允许删除`)
  }

  console.log(JSON.stringify({
    message: '账户标签 PG smoke 通过',
    accountId: account.id,
    groupId: group.id,
    tagsChecked: true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

function requiredTag(tags: Awaited<ReturnType<typeof listAccountTagsAsync>>, name: string) {
  const tag = tags.find((item) => item.name === name)
  assert(tag, `PG smoke 应能找到标签 ${name}`)
  return tag
}

function sortedTagNames(tags: Array<{ name: string }> | undefined): string[] {
  return sortedText((tags ?? []).map((tag) => tag.name))
}

function sortedText(values: string[]): string[] {
  return [...values].sort((left, right) => left.localeCompare(right, 'zh-Hans-CN'))
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  if (createdAccountIds.length > 0) {
    for (const accountId of createdAccountIds) {
      await deleteAccountAsync(accountId, access).catch(() => false)
    }
  }
  await pool.query(`
    DELETE FROM juhe_business.account_tag_bindings
    WHERE account_id = ANY($1::text[])
       OR tag_id IN (
        SELECT id
        FROM juhe_business.account_tags
        WHERE system_account_id = $2
          AND name = ANY($3::text[])
      )
  `, [createdAccountIds, access.systemAccountId, createdTagNames])
  for (const tagName of createdTagNames) {
    await pool.query('DELETE FROM juhe_business.account_tags WHERE system_account_id = $1 AND name = $2', [access.systemAccountId, tagName])
  }
  if (createdGroupIds.length > 0) {
    for (const groupId of createdGroupIds) {
      await deleteGroupAsync(groupId, access).catch(() => undefined)
    }
  }
  await pool.query('DELETE FROM juhe_business.accounts WHERE id = ANY($1::text[])', [createdAccountIds])
  await pool.query('DELETE FROM juhe_business.groups WHERE id = ANY($1::text[])', [createdGroupIds])
}
