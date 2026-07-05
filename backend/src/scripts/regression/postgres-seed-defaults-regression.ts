import assert from 'node:assert/strict'

import { HYBRID_PROVIDER_CODE } from '../../domain/provider-protocol.js'
import {
  DEFAULT_BUILT_IN_GROUPS,
  DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS,
  DEFAULT_PROVIDER_SEEDS
} from '../../storage/schema-defaults.js'

const providerCodes = new Set(DEFAULT_PROVIDER_SEEDS.map((provider) => provider.code))
const profileIds = new Set(DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS.map((profile) => profile.id))
const defaultGroupProviderCodes = new Set(DEFAULT_BUILT_IN_GROUPS.map((group) => group.providerCode))
const defaultRouteProviderCodes: string[] = DEFAULT_BUILT_IN_GROUPS
  .map((group) => group.providerCode)
  .filter((providerCode) => providerCode !== HYBRID_PROVIDER_CODE)

assert.equal(providerCodes.size, DEFAULT_PROVIDER_SEEDS.length, '默认 provider seed code 不能重复')
assert.equal(profileIds.size, DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS.length, '默认 provider protocol profile seed id 不能重复')
assert.equal(defaultGroupProviderCodes.size, DEFAULT_BUILT_IN_GROUPS.length, '默认内置分组必须按供应商唯一')
assert.equal(defaultRouteProviderCodes.includes(HYBRID_PROVIDER_CODE), false, '默认策略路由 / 默认 API Key seed 不应覆盖混合供应商默认分组')
assert.equal(defaultRouteProviderCodes.length, DEFAULT_BUILT_IN_GROUPS.length - 1, '默认策略路由 / 默认 API Key seed 应覆盖除混合供应商外的每个默认分组')

for (const profile of DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS) {
  assert.ok(
    providerCodes.has(profile.providerCode),
    `默认 provider protocol profile ${profile.id} 引用的 provider ${profile.providerCode} 必须存在于默认 provider seed`
  )
}

for (const group of DEFAULT_BUILT_IN_GROUPS) {
  assert.ok(
    providerCodes.has(group.providerCode),
    `默认分组 ${group.id} 引用的 provider ${group.providerCode} 必须存在于默认 provider seed`
  )
}

console.log('postgres-seed-defaults-regression passed')
