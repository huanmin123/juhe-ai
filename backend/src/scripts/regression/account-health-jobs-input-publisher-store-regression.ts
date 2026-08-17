import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { readPublishedAccountHealthJobsInput } from '../../modules/background/account-health-jobs-input.protocol.js'
import { publishNextAccountHealthJobsInputFromBusinessOutbox } from '../../modules/background/account-health-jobs-input-publisher.service.js'
import { reserveAndEnqueueAccountHealthJobsInput } from '../../storage/account-health-jobs-input-outbox.repository.js'
import { getBusinessDatabase } from '../../storage/database.js'
import { logger } from '../../shared/logger.js'

const root = mkdtempSync(join(resolve(tmpdir()), 'juhe-ai-j1-input-publisher-store-'))
runtimeConfig.databasePath = join(root, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(root, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(root, 'stats.sqlite3')
runtimeConfig.secret = 'j1-input-publisher-store-secret'
runtimeConfig.processRole = 'db-service'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.accountHealthJobs.inputDirectory = join(root, 'inputs')
runtimeConfig.accountHealthJobs.inputSigningKey = Buffer.alloc(32, 19).toString('base64url')
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({ name: 'J1 input publisher store 分组', providerCode: 'gpt' }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'J1 input publisher store 账户',
    type: 'api_key',
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5'],
    healthCheckModel: 'gpt-5.5',
    healthCheckEndpointMode: 'chat_json',
    groupId: group.id,
    credentials: { api_key: 'sk-j1-input-publisher-store', base_url: 'https://api.openai.com/v1' }
  }, access)
  const database = getBusinessDatabase()
  database.prepare('UPDATE accounts SET status = ?, schedulable = ? WHERE id = ?').run('active', 1, account.id)
  assert.equal(await publishNextAccountHealthJobsInputFromBusinessOutbox(), 'published', '当前 snapshot intent 必须发布并 ACK')
  const inputPath = inputPathForAccount(account.id)
  const input = readPublishedAccountHealthJobsInput(inputPath)
  const payload = JSON.parse(Buffer.from(input.payload, 'base64url').toString('utf8')) as Record<string, unknown>
  assert.equal(payload.account_id, account.id)
  assert.equal(payload.input_version, 1)
  assert.equal(payload.provider, 'openai')
  assert.equal(outboxStatus(database, account.id, 1), 'published')

  const row = database.prepare('SELECT config_revision, dispatch_revision FROM accounts WHERE id = ?').get(account.id) as { config_revision: number, dispatch_revision: number }
  database.prepare('UPDATE accounts SET status = ?, schedulable = ? WHERE id = ?').run('disabled', 0, account.id)
  const tombstone = reserveAndEnqueueAccountHealthJobsInput({
    accountId: account.id,
    configRevision: row.config_revision,
    dispatchRevision: row.dispatch_revision,
    kind: 'snapshot',
    reason: 'account_disabled'
  }, database)
  assert.equal(tombstone.inputVersion, 2)
  assert.equal(await publishNextAccountHealthJobsInputFromBusinessOutbox(), 'published', '当前资格失效的 snapshot intent 必须转换成 tombstone 并 ACK')
  const tombstoneInput = readPublishedAccountHealthJobsInput(inputPath)
  const tombstonePayload = JSON.parse(Buffer.from(tombstoneInput.payload, 'base64url').toString('utf8')) as Record<string, unknown>
  assert.equal(tombstonePayload.input_version, 2)
  assert.equal((tombstonePayload.eligibility as Record<string, unknown>).schedulable, false)
  assert.equal(outboxStatus(database, account.id, 2), 'published')

  console.log('J1 input publisher store 回归通过：SQLite DB-service intent 已生成 snapshot/tombstone 并按 lease ACK')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(root, { recursive: true, force: true })
}

function inputPathForAccount(accountId: string): string {
  const path = join(root, 'inputs', `${createHash('sha256').update(accountId).digest('hex')}.account-health-input.json`)
  assert.ok(readFileSync(path).length > 0, 'input 文件必须原子发布到稳定 locator')
  return path
}

function outboxStatus(database: ReturnType<typeof getBusinessDatabase>, accountId: string, inputVersion: number): string | undefined {
  return (database.prepare(`
    SELECT status
    FROM account_health_jobs_input_outbox
    WHERE account_id = ? AND input_version = ?
  `).get(accountId, inputVersion) as { status?: string } | undefined)?.status
}
