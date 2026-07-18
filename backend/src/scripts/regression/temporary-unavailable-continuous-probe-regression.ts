import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { buildPostgresSchemaSql } from '../../storage/postgres-schema.js'
import { accountCreateSchema, accountUpdateSchema } from '../../modules/accounts/account-request.schemas.js'
import { buildAccountImportCreatePayload } from '../../modules/accounts/account-import-account-payload.js'

const schemaSql = buildPostgresSchemaSql()
const cooldownSource = readFileSync(resolve('src/storage/account-cooldown-retest.repository.ts'), 'utf8')
const workerSource = readFileSync(resolve('src/modules/background/cooldown-account-retest.service.ts'), 'utf8')
const exportSource = readFileSync(resolve('src/modules/accounts/account-export.service.ts'), 'utf8')
const accountReadSource = readFileSync(resolve('src/storage/account-read.repository.ts'), 'utf8')
const accountSummarySource = readFileSync(resolve('src/storage/account-summary.repository.ts'), 'utf8')
const repositoriesSource = readFileSync(resolve('src/storage/repositories.ts'), 'utf8')

assert.match(schemaSql, /temporary_unavailable_continuous_probe_enabled integer NOT NULL DEFAULT 1/, '物理账户必须持久化持续恢复探活开关，默认开启')
assert.match(schemaSql, /temporary_unavailable_continuous_probe_enabled integer NOT NULL DEFAULT 1 CHECK \(temporary_unavailable_continuous_probe_enabled IN \(0, 1\)\)/, 'SQLite 持续恢复探活开关必须与 PostgreSQL 保持相同的 0/1 约束')
assert.equal(accountCreateSchema.safeParse({
  providerCode: 'gpt', providerProtocolProfileId: 'profile-test', name: 'probe-test', type: 'api_key',
  temporaryUnavailableContinuousProbeEnabled: false
}).success, true, '创建账户契约必须接受持续恢复探活开关')
assert.equal(accountUpdateSchema.safeParse({ temporaryUnavailableContinuousProbeEnabled: false }).success, true, '编辑账户契约必须接受持续恢复探活开关')
assert.equal(accountUpdateSchema.safeParse({ temporaryUnavailableContinuousProbeEnabled: 'false' }).success, false, '持续恢复探活必须严格拒绝字符串布尔值')
assert.equal(buildAccountImportCreatePayload({
  providerCode: 'gpt', name: 'import-probe', type: 'api_key', status: 'active', credentials: {},
  temporaryUnavailableContinuousProbeEnabled: false
}).temporaryUnavailableContinuousProbeEnabled, false, '账户导入 payload 必须完整保留关闭值')
assert.match(exportSource, /temporaryUnavailableContinuousProbeEnabled === false/, '账户导出必须显式保留偏离默认值的关闭配置')
assert.match(cooldownSource, /temporaryUnavailableContinuousProbeEnabled/, '冷却复测必须读取账户级持续恢复探活开关')
assert.match(cooldownSource, /10 \* 60/, '关闭持续恢复探活后必须使用十分钟有界观察窗口')
assert.match(cooldownSource, /config_revision/, '复测结果必须受配置版本保护，旧探针不能覆盖新配置')
assert.match(workerSource, /configRevision/, '后台复测队列必须携带账户配置版本')
assert.match(workerSource, /expectedConfigRevision/, '后台复测写回必须携带期望配置版本')
assert.match(workerSource, /expectedObservationStartedAt/, '后台复测失败写回必须携带观察代次')
assert.match(accountReadSource, /accounts\.temporary_unavailable_continuous_probe_enabled/, 'SQLite 账户基础读取投影必须包含持续恢复探活开关')
assert.match(accountReadSource, /source_accounts\.temporary_unavailable_continuous_probe_enabled AS source_temporary_unavailable_continuous_probe_enabled/, 'SQLite 授权来源读取投影必须包含持续恢复探活开关')
assert.match(accountReadSource, /source_temporary_unavailable_continuous_probe_enabled: source\.temporary_unavailable_continuous_probe_enabled/, 'SQLite 授权来源补充读取必须回填持续恢复探活开关')
assert.match(accountSummarySource, /source_accounts\.temporary_unavailable_continuous_probe_enabled AS source_temporary_unavailable_continuous_probe_enabled/, 'PostgreSQL 授权账户列表必须投影来源持续恢复探活开关')
assert.match(
  cooldownSource,
  /clauses\.push\('AND cooldown_retest_observation_started_at = \?'\)/,
  '冷却复测期望状态构造器必须生成观察代次保护条件'
)
assert.equal(
  [...cooldownSource.matchAll(/const expectedState = cooldownRetestExpectedStateGuard\(input\)/g)].length,
  4,
  'SQLite 与 PostgreSQL 的成功、失败写回都必须复用观察代次保护构造器'
)
assert.match(
  repositoriesSource,
  /const boundedRecoveryPolicyActivated = current\.temporaryUnavailableContinuousProbeEnabled !== false\s*&& !nextTemporaryUnavailableContinuousProbeEnabled/,
  '来源由开启切到关闭时必须独立记录策略切换，不能只看来源账户当前状态'
)
assert.ok(
  [...repositoriesSource.matchAll(/boundedRecoveryPolicyActivated \? 1 : 0/g)].length >= 10,
  'SQLite 与 PostgreSQL 授权实例同步必须按每个实例自身 temporary_unavailable 状态重启十分钟观察窗口'
)
assert.ok(
  [...repositoriesSource.matchAll(/boundedRecoveryObservationStartedAt/g)].length >= 6,
  '来源账户和授权实例必须共用本次保存生成的新观察代次，而不是沿用来源账户旧值'
)

console.log('temporary unavailable continuous probe regression passed')
