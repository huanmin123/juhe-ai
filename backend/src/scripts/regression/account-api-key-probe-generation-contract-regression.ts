import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const repositorySource = readFileSync(fileURLToPath(new URL('../../storage/account-api-key-runtime-state.repository.ts', import.meta.url)), 'utf8')
const cooldownServiceSource = readFileSync(fileURLToPath(new URL('../../modules/background/account-api-key-cooldown-retest.service.ts', import.meta.url)), 'utf8')
const accountProbeJobsSource = readFileSync(fileURLToPath(new URL('../../modules/background/account-probe-jobs.ts', import.meta.url)), 'utf8')
const dbServiceTypesSource = readFileSync(fileURLToPath(new URL('../../modules/db-service/db-service-types.ts', import.meta.url)), 'utf8')
const dbServiceHandlersSource = readFileSync(fileURLToPath(new URL('../../modules/db-service/db-service-handlers.ts', import.meta.url)), 'utf8')

assert.match(repositorySource, /stateUpdatedAt: row\.updated_at/, '探针候选必须携带 runtime state updated_at')
assert.match(repositorySource, /accountConfigRevision: row\.config_revision/, '探针候选必须携带账户配置代次')
assert.match(repositorySource, /states\.updated_at,[\s\S]{0,300}accounts\.config_revision/, 'SQLite/PG 候选查询必须在同一快照读取运行态和账户配置代次')
assert.match(repositorySource, /probe_account\.config_revision = \?[\s\S]{0,100}FOR UPDATE/, 'PG 探针回写必须锁住并核对账户配置代次')

assert.match(cooldownServiceSource, /entry\.fingerprint === item\.keyFingerprint && entry\.key === item\.apiKey/, '执行探针前必须同时复核 fingerprint 和原始 secret')
for (const generationField of [
  'expectedStatus',
  'expectedNextProbeAt',
  'expectedStateUpdatedAt',
  'expectedAccountConfigRevision',
  'expectedProbeClaimToken'
] as const) {
  const serviceMatches = cooldownServiceSource.match(new RegExp(`${generationField}: item\\.`, 'g')) ?? []
  assert.equal(serviceMatches.length, 3, `success/failure/defer 必须都透传 ${generationField}`)
  assert.match(dbServiceTypesSource, new RegExp(`${generationField}\\?:`), `DB-service IPC 必须声明 ${generationField}`)
}

assert.match(dbServiceHandlersSource, /recordAccountApiKeyRuntimeSuccessAsync\(operation\.account, \{[\s\S]{0,400}expectedAccountConfigRevision/, 'PG success handler 必须透传完整探针代次')
assert.match(dbServiceHandlersSource, /recordAccountApiKeyRuntimeSuccess\(operation\.account, \{[\s\S]{0,400}expectedAccountConfigRevision/, 'SQLite success handler 必须透传完整探针代次')
assert.match(repositorySource, /probeCandidateScanLimit = 10_000/, '候选扫描不得被少量旧 fingerprint 占满 batch')
assert.match(repositorySource, /function accountApiKeyRuntimeProbeCandidatesFromRows[\s\S]{0,300}Math\.min\(probeCandidateScanLimit, Math\.trunc\(limit\)\)/, '候选转换必须保留完整 10,000 行扫描窗口，不得重新截断为 100')
assert.match(repositorySource, /probe_claimed_until IS NULL OR[\s\S]{0,100}probe_claimed_until <= \?/, '候选查询必须排除仍在租约内的 claim')
assert.match(repositorySource, /probe_claim_token = \?/, '探针回写必须核对当前 claim token')
assert.match(
  accountProbeJobsSource,
  /export async function runAccountApiKeyCooldownRetest[\s\S]*?const queueBeforeScan = getAccountApiKeyCooldownRetestQueueSnapshot\(\)[\s\S]*?const availableQueueSlots = Math\.max\(\s*0,\s*queueConcurrency - queueBeforeScan\.runningCount - queueBeforeScan\.pendingCount\s*\)[\s\S]*?if \(availableQueueSlots === 0\) return[\s\S]*?type: 'list_account_api_key_runtime_states_due_for_probe'[\s\S]*?limit: Math\.min\(batchSize, availableQueueSlots\)/,
  'Key 探针 scheduler 必须同时扣除 running 与 pending，并通过 DB service 只 claim 实际空槽'
)
assert.match(dbServiceHandlersSource, /case 'list_account_api_key_runtime_states_due_for_probe':[\s\S]{0,250}listAccountApiKeyRuntimeStatesDueForProbe/, 'Key 探针候选 claim 必须在 DB service 中执行')
assert.doesNotMatch(cooldownServiceSource, /statusCode\s*===|\[\s*401|\[\s*429|statusCode\s*>?=/, 'Key 冷却探针不得按具体上游状态码解释账户语义')

console.log('ACCOUNT_API_KEY_PROBE_GENERATION_CONTRACT_OK')
