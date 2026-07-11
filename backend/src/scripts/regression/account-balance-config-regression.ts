import assert from 'node:assert/strict'

import { buildPostgresSchemaSql } from '../../storage/postgres-schema.js'
import {
  normalizeAccountBalanceConfig,
  validateAccountBalanceCapability
} from '../../modules/accounts/account-balance-config.js'
import { accountCreateSchema, accountUpdateSchema } from '../../modules/accounts/account-request.schemas.js'

const schemaSql = buildPostgresSchemaSql()

assert.match(schemaSql, /balance_query_enabled integer NOT NULL DEFAULT 0/, '账户应保存余额查询启用状态')
assert.match(schemaSql, /balance_query_config_json text NOT NULL DEFAULT '\{\}'/, '账户应保存余额查询配置')
assert.match(schemaSql, /balance_query_next_refresh_at text/, '账户应保存下次余额刷新时间')
assert.match(schemaSql, /CREATE INDEX IF NOT EXISTS idx_accounts_balance_query_due/, '账户应具备余额到期扫描索引')
assert.match(
  schemaSql,
  /account_usage_snapshots[\s\S]+kind text NOT NULL CHECK \(kind IN \([^)]+relay_balance[^)]+\)\)/,
  '账户使用快照应接受 relay_balance 类型'
)

assert.deepEqual(normalizeAccountBalanceConfig({ adapter: 'sub2api' }), {
  adapter: 'sub2api',
  intervalMinutes: 5
})
assert.equal(normalizeAccountBalanceConfig({ adapter: 'newapi', intervalMinutes: 1 }).intervalMinutes, 1)
assert.equal(normalizeAccountBalanceConfig({ adapter: 'litellm', intervalMinutes: 10 }).intervalMinutes, 10)
for (const intervalMinutes of [0, 11, 1.5]) {
  assert.throws(
    () => normalizeAccountBalanceConfig({ adapter: 'sub2api', intervalMinutes }),
    /刷新周期/,
    `刷新周期 ${intervalMinutes} 应被拒绝`
  )
}
assert.throws(() => normalizeAccountBalanceConfig({ adapter: 'oneapi_compatible' }), /查询类型/)
assert.deepEqual(normalizeAccountBalanceConfig({
  adapter: 'custom',
  custom: { path: '/api/balance', remainingPointer: '/data/remaining', divisor: '100' }
}), {
  adapter: 'custom',
  intervalMinutes: 5,
  custom: { path: '/api/balance', remainingPointer: '/data/remaining', divisor: '100' }
})
assert.deepEqual(normalizeAccountBalanceConfig({
  adapter: 'custom',
  custom: { path: '/api/usage', totalPointer: '/total', usedPointer: '/used' }
}).custom, { path: '/api/usage', totalPointer: '/total', usedPointer: '/used' })
assert.throws(() => normalizeAccountBalanceConfig({
  adapter: 'custom',
  custom: { path: 'https://evil.example/balance', remainingPointer: '/balance' }
}), /相对路径/)
assert.throws(() => normalizeAccountBalanceConfig({
  adapter: 'custom',
  custom: { path: '/balance', remainingPointer: 'balance' }
}), /JSON Pointer/)
assert.throws(() => normalizeAccountBalanceConfig({
  adapter: 'custom',
  custom: { path: '/balance', remainingPointer: '/balance', divisor: '0' }
}), /除数/)

const physicalApiKeyAccount = {
  type: 'api_key',
  credentials: { api_key: 'sk-test' }
}
assert.doesNotThrow(() => validateAccountBalanceCapability(physicalApiKeyAccount, true))
assert.throws(() => validateAccountBalanceCapability({
  type: 'oauth',
  credentials: { access_token: 'oauth-test' }
}, true), /仅支持 API Key/)
assert.throws(() => validateAccountBalanceCapability({
  type: 'api_key',
  credentials: { api_keys: ['sk-one', 'sk-two'] }
}, true), /单 API Key/)
assert.throws(() => validateAccountBalanceCapability({
  ...physicalApiKeyAccount,
  authorizationInstanceAuthorizationId: 'authorization-test'
}, true), /授权实例/)
assert.doesNotThrow(() => validateAccountBalanceCapability({ type: 'oauth', credentials: {} }, false))

const balanceFields = {
  balanceQueryEnabled: true,
  balanceQueryConfig: { adapter: 'sub2api', intervalMinutes: 5 }
}
assert.equal(accountCreateSchema.safeParse({
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile-test',
  name: 'balance-test',
  type: 'api_key',
  ...balanceFields
}).success, true, '创建账户契约应接受余额查询配置')
assert.equal(accountUpdateSchema.safeParse(balanceFields).success, true, '编辑账户契约应接受余额查询配置')
assert.equal(accountUpdateSchema.safeParse({
  balanceQueryEnabled: true,
  balanceQueryConfig: { adapter: 'oneapi_compatible' }
}).success, false, '编辑账户契约必须拒绝 oneapi_compatible')

console.log('account balance config regression passed')
