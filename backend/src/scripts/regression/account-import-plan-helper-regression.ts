import assert from 'node:assert/strict'

import {
  accountImportGroupKey,
  buildAccountImportSummary,
  markDuplicateAccountImportItems
} from '../../modules/accounts/account-import-plan.js'
import type { AccountImportDuplicateCandidate } from '../../modules/accounts/account-import-plan.js'
import type { AccountImportItem, AccountImportProxyItem } from '../../modules/accounts/account-import.service.js'

const duplicateCandidates: AccountImportDuplicateCandidate[] = [
  { source: { index: 1, name: ' 主账户 ' }, item: { action: 'create', messages: [] } },
  { source: { index: 2, name: '主账户' }, item: { action: 'create', messages: [] } },
  { source: { index: 3, name: '失败账户' }, item: { action: 'failed', messages: [] } },
  { source: { index: 4, name: '失败账户' }, item: { action: 'create', messages: [] } }
]

markDuplicateAccountImportItems(duplicateCandidates, true)

assert.equal(duplicateCandidates[1].item.action, 'skip')
assert.deepEqual(duplicateCandidates[1].item.messages, ['与第 1 条账户名称重复'])
assert.equal(duplicateCandidates[3].item.action, 'create')

const strictDuplicateCandidates: AccountImportDuplicateCandidate[] = [
  { source: { index: 1, name: 'Alpha' }, item: { action: 'create', messages: [] } },
  { source: { index: 2, name: ' alpha ' }, item: { action: 'create', messages: [] } }
]

markDuplicateAccountImportItems(strictDuplicateCandidates, false)

assert.equal(strictDuplicateCandidates[1].item.action, 'failed')
assert.deepEqual(strictDuplicateCandidates[1].item.messages, ['与第 1 条账户名称重复'])
assert.equal(accountImportGroupKey(' Profile-A ', ' 分组A '), 'profile-a:分组a')

const accounts: AccountImportItem[] = [
  {
    index: 1,
    providerProtocolProfileId: 'Profile-A',
    groupName: '现有分组',
    action: 'create',
    messages: [],
    warnings: []
  },
  {
    index: 2,
    groupId: 'group-owned',
    action: 'skip',
    messages: [],
    warnings: []
  },
  {
    index: 3,
    providerProtocolProfileId: 'Profile-A',
    groupName: '失败分组',
    action: 'failed',
    messages: ['分组不可用'],
    warnings: []
  },
  {
    index: 4,
    providerProtocolProfileId: 'Profile-A',
    groupName: '新分组',
    action: 'create',
    messages: [],
    warnings: []
  }
]

const proxies: AccountImportProxyItem[] = [
  { index: 1, action: 'create', messages: [], warnings: [] },
  { index: 2, action: 'reuse', messages: [], warnings: [] },
  { index: 3, action: 'skip', messages: [], warnings: [] },
  { index: 4, action: 'failed', messages: ['代理不可用'], warnings: [] }
]

const groupsToCreate = new Map([
  [accountImportGroupKey('Profile-A', '新分组'), { providerCode: 'gpt', providerProtocolProfileId: 'Profile-A', name: '新分组' }]
])

assert.deepEqual(buildAccountImportSummary(accounts, proxies, groupsToCreate), {
  accounts: {
    total: 4,
    create: 2,
    skip: 1,
    failed: 1
  },
  proxies: {
    total: 4,
    create: 1,
    reuse: 1,
    skip: 1,
    failed: 1
  },
  groups: {
    create: 1,
    reuse: 2,
    failed: 0
  }
})

console.log('account import plan helper regression passed')
