import assert from 'node:assert/strict'

import {
  DEFAULT_BUILT_IN_GROUPS,
  DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS,
  DEFAULT_PROVIDER_SEEDS
} from '../../storage/schema-defaults.js'

const providerCodes = new Set(DEFAULT_PROVIDER_SEEDS.map((provider) => provider.code))
const profileIds = new Set(DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS.map((profile) => profile.id))

assert.equal(providerCodes.size, DEFAULT_PROVIDER_SEEDS.length, '默认 provider seed code 不能重复')
assert.equal(profileIds.size, DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS.length, '默认 provider protocol profile seed id 不能重复')

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
  assert.ok(
    profileIds.has(group.providerProtocolProfileId),
    `默认分组 ${group.id} 引用的 provider protocol profile ${group.providerProtocolProfileId} 必须存在于默认 profile seed`
  )
}

console.log('postgres-seed-defaults-regression passed')
