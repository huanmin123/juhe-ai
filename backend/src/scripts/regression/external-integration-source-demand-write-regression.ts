import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-external-source-demand-write-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'external-source-demand-write-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, externalSources] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/external-integration-source.repository.js')
])

try {
  const source = externalSources.createExternalIntegrationSource({
    name: '字段级来源',
    status: 'active',
    scopes: [externalSources.externalIntegrationGroupListReadScope],
    rateLimits: [{ windowSeconds: 60, maxRequests: 20 }],
    expiresAt: '2026-12-31T00:00:00.000Z',
    notes: '原备注'
  })
  const token = externalSources.createExternalIntegrationSourceToken({
    sourceRefId: source.id,
    name: '个体 Token 名称',
    token: 'juis_external_source_demand_write_token',
    status: 'active',
    scopes: [externalSources.externalIntegrationAccountListReadScope],
    expiresAt: '2026-11-30T00:00:00.000Z'
  })
  const database = databaseModule.getBusinessDatabase()

  const sourceBeforeNotes = sourceRow(database, source.id)
  const tokenBeforeNotes = tokenRow(database, source.id, token.id)
  const notesOutcome = externalSources.updateExternalIntegrationSource(source.id, {
    expectedUpdatedAt: sourceBeforeNotes.updated_at,
    notes: '新备注'
  })
  assert(notesOutcome, '备注 PATCH 应命中来源')
  assert.deepEqual(notesOutcome.changes.map((change) => change.field), ['notes'], '备注 PATCH 只能报告 notes 变化')
  assert.deepEqual(changedKeys(sourceBeforeNotes, sourceRow(database, source.id)), ['notes', 'updated_at'], '备注 PATCH 只能更新 notes/updated_at')
  assert.deepEqual(tokenRow(database, source.id, token.id), tokenBeforeNotes, '来源备注 PATCH 不得触碰 Token')

  const sourceBeforeNoop = sourceRow(database, source.id)
  const tokenBeforeNoop = tokenRow(database, source.id, token.id)
  const noopOutcome = externalSources.updateExternalIntegrationSource(source.id, {
    expectedUpdatedAt: sourceBeforeNoop.updated_at,
    notes: '新备注'
  })
  assert.equal(noopOutcome?.mutation.updatedAt, sourceBeforeNoop.updated_at, '来源同值 PATCH 不得推进版本')
  assert.deepEqual(sourceRow(database, source.id), sourceBeforeNoop, '来源同值 PATCH 必须零写入')
  assert.deepEqual(tokenRow(database, source.id, token.id), tokenBeforeNoop, '来源同值 PATCH 不得派生 Token 写入')

  assert.throws(() => externalSources.updateExternalIntegrationSource(source.id, {
    expectedUpdatedAt: source.updatedAt,
    notes: '过期写入'
  }), externalSources.ExternalIntegrationSourcePatchConflictError, '来源过期版本必须 CAS 冲突')
  assert.deepEqual(sourceRow(database, source.id), sourceBeforeNoop, '来源 CAS 冲突不得写入')

  const tokenVersionAheadOfSource = new Date(Date.parse(sourceBeforeNoop.updated_at) + 1).toISOString()
  database.prepare(`
    UPDATE external_integration_source_tokens
    SET updated_at = ?
    WHERE id = ? AND source_ref_id = ?
  `).run(tokenVersionAheadOfSource, token.id, source.id)
  const tokenBeforeSourceStatus = tokenRow(database, source.id, token.id)
  assert(tokenBeforeSourceStatus.updated_at > sourceBeforeNoop.updated_at, '反例夹具必须让 Token 版本严格领先来源版本')
  const sameTargetToken = externalSources.createExternalIntegrationSourceToken({
    sourceRefId: source.id,
    name: '已是目标状态 Token',
    token: 'juis_external_source_status_predicate_crossing_token',
    status: 'disabled'
  })
  const sameTargetTokenVersion = new Date(Date.parse(tokenVersionAheadOfSource) + 1).toISOString()
  database.prepare(`
    UPDATE external_integration_source_tokens
    SET updated_at = ?
    WHERE id = ? AND source_ref_id = ?
  `).run(sameTargetTokenVersion, sameTargetToken.id, source.id)
  const sameTargetTokenBeforeSourceStatus = tokenRow(database, source.id, sameTargetToken.id)

  const statusOutcome = externalSources.updateExternalIntegrationSource(source.id, {
    expectedUpdatedAt: sourceBeforeNoop.updated_at,
    status: 'disabled'
  })
  assert(statusOutcome, '来源状态 PATCH 应命中来源')
  const tokenAfterSourceStatus = tokenRow(database, source.id, token.id)
  assert(statusOutcome.mutation.updatedAt > sameTargetTokenBeforeSourceStatus.updated_at, '来源状态 PATCH 新版本必须严格超过该来源全部 Token 的最大旧版本')
  assert.equal(tokenAfterSourceStatus.updated_at, statusOutcome.mutation.updatedAt, '来源与联动 Token 必须共享同一个严格递增版本')
  assert.deepEqual(
    tokenRow(database, source.id, sameTargetToken.id),
    sameTargetTokenBeforeSourceStatus,
    '已是目标状态的 Token 不应被写入，但必须纳入版本基线和 PostgreSQL 锁集合'
  )
  assert.deepEqual(
    changedKeys(tokenBeforeNoop, tokenAfterSourceStatus),
    ['status', 'updated_at'],
    '来源状态派生只能更新 Token status/updated_at'
  )
  assert.equal(tokenAfterSourceStatus.name, tokenBeforeNoop.name, '来源状态派生不得覆盖 Token 个体名称')
  assert.equal(tokenAfterSourceStatus.scopes_json, tokenBeforeNoop.scopes_json, '来源状态派生不得覆盖 Token 个体 scopes')
  assert.equal(tokenAfterSourceStatus.expires_at, tokenBeforeNoop.expires_at, '来源状态派生不得覆盖 Token 个体 expiresAt')
  assert.throws(() => externalSources.updateExternalIntegrationSourceToken(source.id, token.id, {
    expectedUpdatedAt: tokenBeforeSourceStatus.updated_at,
    name: 'ABA 旧版本不得命中'
  }), externalSources.ExternalIntegrationSourcePatchConflictError, '来源状态联动后 Token 旧 CAS 版本必须冲突')

  const sourceBeforeIndependentFields = sourceRow(database, source.id)
  const sourceFieldsOutcome = externalSources.updateExternalIntegrationSource(source.id, {
    expectedUpdatedAt: sourceBeforeIndependentFields.updated_at,
    name: '字段级来源改名',
    scopes: [externalSources.externalIntegrationAccountListReadScope],
    expiresAt: '2027-01-31T00:00:00.000Z'
  })
  assert(sourceFieldsOutcome, '来源独立字段 PATCH 应命中来源')
  assert.deepEqual(
    changedKeys(sourceBeforeIndependentFields, sourceRow(database, source.id)),
    ['expires_at', 'name', 'scopes_json', 'updated_at'],
    '来源 name/scopes/expiresAt PATCH 只能更新来源自身提交列和版本'
  )
  assert.deepEqual(
    tokenRow(database, source.id, token.id),
    tokenAfterSourceStatus,
    '来源 name/scopes/expiresAt PATCH 不得覆盖 Token 个体字段'
  )

  const sourceBeforeTokenPatch = sourceRow(database, source.id)
  const tokenNameOutcome = externalSources.updateExternalIntegrationSourceToken(source.id, token.id, {
    expectedUpdatedAt: tokenAfterSourceStatus.updated_at,
    name: '独立改名 Token'
  })
  assert(tokenNameOutcome, 'Token 名称 PATCH 应命中归属来源')
  const tokenAfterName = tokenRow(database, source.id, token.id)
  assert.deepEqual(changedKeys(tokenAfterSourceStatus, tokenAfterName), ['name', 'updated_at'], 'Token 名称 PATCH 只能更新 name/updated_at')
  assert.deepEqual(sourceRow(database, source.id), sourceBeforeTokenPatch, 'Token PATCH 不得写来源行')

  const otherSource = externalSources.createExternalIntegrationSource({ name: '其他来源' })
  assert.equal(externalSources.updateExternalIntegrationSourceToken(otherSource.id, token.id, {
    expectedUpdatedAt: tokenAfterName.updated_at,
    name: '越权改名'
  }), undefined, 'Token 定位 SQL 必须同时约束 token id 与 source owner')
  assert.deepEqual(tokenRow(database, source.id, token.id), tokenAfterName, '错误来源归属不得修改 Token')

  const tokenNoop = externalSources.updateExternalIntegrationSourceToken(source.id, token.id, {
    expectedUpdatedAt: tokenAfterName.updated_at,
    name: '独立改名 Token'
  })
  assert.equal(tokenNoop?.mutation.updatedAt, tokenAfterName.updated_at, 'Token 同值 PATCH 不得推进版本')
  assert.deepEqual(tokenRow(database, source.id, token.id), tokenAfterName, 'Token 同值 PATCH 必须零写入')
  assert.throws(() => externalSources.updateExternalIntegrationSourceToken(source.id, token.id, {
    expectedUpdatedAt: tokenAfterSourceStatus.updated_at,
    name: '过期 Token 写入'
  }), externalSources.ExternalIntegrationSourcePatchConflictError, 'Token 过期版本必须 CAS 冲突')

  const revokedOutcome = externalSources.updateExternalIntegrationSourceToken(source.id, token.id, {
    expectedUpdatedAt: tokenAfterName.updated_at,
    status: 'revoked'
  })
  assert(revokedOutcome, 'Token revoke PATCH 应命中归属来源')
  const tokenAfterRevoke = tokenRow(database, source.id, token.id)
  assert.deepEqual(
    changedKeys(tokenAfterName, tokenAfterRevoke),
    ['revoked_at', 'status', 'updated_at'],
    'Token revoke 只能派生 revoked_at，并更新 status/updated_at'
  )
  assert.equal(tokenAfterRevoke.revoked_at, revokedOutcome.mutation.updatedAt, 'revoked_at 必须与 PATCH 新版本使用同一时间戳')

  assertStaticDemandWriteContract()
  console.log('外部来源字段级写入回归通过：CAS、归属 SQL、动态 SET、同值零写入及 Token 个体字段隔离均已覆盖')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function sourceRow(database: ReturnType<typeof databaseModule.getBusinessDatabase>, id: string): Record<string, unknown> & { updated_at: string } {
  const row = database.prepare('SELECT * FROM external_integration_sources WHERE id = ?').get(id) as Record<string, unknown> | undefined
  assert(row, `来源不存在：${id}`)
  return row as Record<string, unknown> & { updated_at: string }
}

function tokenRow(
  database: ReturnType<typeof databaseModule.getBusinessDatabase>,
  sourceRefId: string,
  tokenId: string
): Record<string, unknown> & { name: string; status: string; scopes_json: string; expires_at: string | null; updated_at: string; revoked_at: string | null } {
  const row = database.prepare('SELECT * FROM external_integration_source_tokens WHERE id = ? AND source_ref_id = ?')
    .get(tokenId, sourceRefId) as Record<string, unknown> | undefined
  assert(row, `Token 不存在：${tokenId}`)
  return row as Record<string, unknown> & { name: string; status: string; scopes_json: string; expires_at: string | null; updated_at: string; revoked_at: string | null }
}

function changedKeys(before: Record<string, unknown>, after: Record<string, unknown>): string[] {
  return Object.keys(after).filter((key) => !Object.is(before[key], after[key])).sort()
}

function assertStaticDemandWriteContract(): void {
  const sourceRepository = readFileSync(new URL('../../storage/external-integration-source.repository.ts', import.meta.url), 'utf8')
  const tokenRepository = readFileSync(new URL('../../storage/external-integration-source-token.repository.ts', import.meta.url), 'utf8')
  const routes = readFileSync(new URL('../../modules/external-integrations/external-integration-sources.routes.ts', import.meta.url), 'utf8')
  const sourcePatchSection = sourceRepository.slice(
    sourceRepository.indexOf('export function updateExternalIntegrationSource('),
    sourceRepository.indexOf('export function deleteExternalIntegrationSource(')
  )
  const tokenPatchSection = tokenRepository.slice(
    tokenRepository.indexOf('export function updateExternalIntegrationSourceToken('),
    tokenRepository.indexOf('export function findExternalIntegrationSourceTokenSecret(')
  )
  const tokenStatusVersionSection = tokenRepository.slice(
    tokenRepository.indexOf('export function latestExternalIntegrationSourceTokenUpdatedAt('),
    tokenRepository.indexOf('export function syncExternalIntegrationSourceTokenStatus(')
  )
  assert.doesNotMatch(tokenPatchSection, /SELECT\s+tokens\.\*/i, 'Token PATCH 不得宽读 tokens.*')
  assert.match(tokenPatchSection, /WHERE id = \? AND source_ref_id = \? AND updated_at = \?/, 'Token UPDATE 必须在 SQL 中约束来源归属和 CAS 版本')
  assert.match(tokenPatchSection, /FOR UPDATE OF tokens/, 'PostgreSQL Token PATCH 必须只锁定归属命中的 Token 行')
  assert.match(tokenPatchSection, /WHERE tokens\.id = \? AND tokens\.source_ref_id = \?/, 'PostgreSQL Token 锁行查询必须在 SQL 中约束来源归属')
  assert.match(tokenStatusVersionSection, /FOR UPDATE OF tokens[\s\S]*rows\.reduce<string \| undefined>/, 'PostgreSQL 来源状态联动必须锁定全部受影响 Token 后计算最大版本')
  assert.doesNotMatch(tokenStatusVersionSection, /status\s*<>|tokens\.status/, '来源状态前置锁不得按可变 Token status 过滤，避免谓词穿越')
  assert.match(sourcePatchSection, /latestExternalIntegrationSourceTokenUpdatedAt/, 'SQLite 来源状态 PATCH 必须把全部 Token 最大版本纳入新版本基线')
  assert.match(sourcePatchSection, /lockExternalIntegrationSourceTokenUpdatedAtAsync/, 'PostgreSQL 来源状态 PATCH 必须把锁定的全部 Token 最大版本纳入新版本基线')
  assert.match(sourcePatchSection, /externalIntegrationSourcePatchProjection/, '来源 PATCH 必须按提交字段构造读取投影')
  assert.doesNotMatch(sourcePatchSection, /SELECT\s+\*/i, '来源 PATCH 不得宽读来源整行')
  assert.match(sourcePatchSection, /FOR UPDATE/, 'PostgreSQL 来源 PATCH 必须锁定目标来源行')
  assert.match(sourcePatchSection, /WHERE id = \? AND updated_at = \?/, '来源 UPDATE 必须使用 updated_at CAS')
  assert.doesNotMatch(sourcePatchSection, /syncExternalIntegrationSourceTokenState/, '来源 PATCH 不得恢复全量 Token 状态同步')
  for (const field of ['name', 'status', 'scopes', 'rateLimits', 'expiresAt', 'notes', 'expectedUpdatedAt']) {
    assert.match(routes, new RegExp(`${field}: bodyField\\(req, '${field}'\\)`), `来源 mutation 指纹必须包含 ${field}`)
  }
  for (const field of ['name', 'status', 'scopes', 'expiresAt', 'expectedUpdatedAt']) {
    assert.match(routes, new RegExp(`${field}: bodyField\\(req, '${field}'\\)`), `Token mutation 指纹必须包含 ${field}`)
  }
}
