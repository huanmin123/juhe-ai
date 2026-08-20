import assert from 'node:assert/strict'
import { backup } from 'node:sqlite'
import { createRequire } from 'node:module'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import {
  createSqliteAccountHealthJobsInputBackfillDependencies,
  runAccountHealthJobsInputBackfill
} from '../maintenance/backfill-account-health-jobs-input.js'
import { currentAccountHealthJobsInputVersion } from '../../storage/account-health-jobs-input-version.repository.js'
import { reserveAndEnqueueAccountHealthJobsInputInTransaction } from '../../storage/account-health-jobs-input-outbox.repository.js'
import { closeStorageDatabases, getBusinessDatabase, runInDatabaseTransaction } from '../../storage/database.js'

const require = createRequire(import.meta.url)
const Constructor = require('node:sqlite').DatabaseSync as new (path: string) => import('node:sqlite').DatabaseSync

const productionRoot = mkdtempSync(join(resolve(tmpdir()), 'juhe-ai-j1-input-backfill-'))
runtimeConfig.databasePath = join(productionRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(productionRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(productionRoot, 'stats.sqlite3')

try {
  const repositories = await import('../../storage/repositories.js')
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({ name: 'J1 backfill wrapper 分组', providerCode: 'gpt' }, access)
  const eligible = repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'J1 backfill wrapper eligible', type: 'api_key', status: 'active', schedulable: true,
    supportedModels: ['gpt-5.5'], healthCheckModel: 'gpt-5.5', healthCheckEndpointMode: 'chat_json', groupId: group.id,
    credentials: { api_key: 'sk-j1-backfill-wrapper', base_url: 'https://api.openai.com/v1' }
  }, access)
  const ineligible = repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'J1 backfill wrapper ineligible', type: 'api_key', status: 'disabled', schedulable: false,
    supportedModels: ['gpt-5.5'], healthCheckModel: 'gpt-5.5', healthCheckEndpointMode: 'chat_json', groupId: group.id,
    credentials: { api_key: 'sk-j1-backfill-wrapper-disabled', base_url: 'https://api.openai.com/v1' }
  }, access)
  const productionDatabase = getBusinessDatabase()
  productionDatabase.prepare('UPDATE accounts SET status = ?, schedulable = ? WHERE id = ?').run('active', 1, eligible.id)
  productionDatabase.prepare('DELETE FROM account_health_jobs_input_outbox WHERE account_id IN (?, ?)').run(eligible.id, ineligible.id)
  productionDatabase.prepare('DELETE FROM account_health_jobs_input_versions WHERE account_id IN (?, ?)').run(eligible.id, ineligible.id)

  const isolatedPath = join(productionRoot, 'isolated-business.sqlite3')
  await backup(productionDatabase, isolatedPath)
  closeStorageDatabases()
  runtimeConfig.databasePath = isolatedPath
  const isolatedDatabase = getBusinessDatabase()
  try {
    isolatedDatabase.prepare('UPDATE accounts SET status = ?, schedulable = ? WHERE id = ?').run('active', 1, eligible.id)
    isolatedDatabase.prepare('DELETE FROM account_health_jobs_input_outbox WHERE account_id IN (?, ?)').run(eligible.id, ineligible.id)
    isolatedDatabase.prepare('DELETE FROM account_health_jobs_input_versions WHERE account_id IN (?, ?)').run(eligible.id, ineligible.id)

    const dependencies = createSqliteAccountHealthJobsInputBackfillDependencies()
    const dryRunBefore = isolatedDatabase.prepare('SELECT count(*) AS count FROM account_health_jobs_input_outbox').get() as { count: number }
    const dryRun = await runAccountHealthJobsInputBackfill(dependencies, { execute: false, limit: 10 })
    const dryRunAfter = isolatedDatabase.prepare('SELECT count(*) AS count FROM account_health_jobs_input_outbox').get() as { count: number }
    assert.equal(runtimeConfig.databaseDriver, 'sqlite')
    assert.equal(dryRun.dryRun, true)
    assert.equal(dryRun.queued, 0)
    assert.equal(dryRun.eligible, 1)
    assert.equal(dryRun.skippedIneligible, 1)
    assert.equal(dryRunBefore.count, dryRunAfter.count)
    assert.equal(currentAccountHealthJobsInputVersion(eligible.id), undefined)

    const execute = await runAccountHealthJobsInputBackfill(dependencies, { execute: true, limit: 10 })
    assert.equal(execute.dryRun, false)
    assert.equal(execute.queued, 1)
    assert.equal(execute.failed, 0)
    assert.equal(currentAccountHealthJobsInputVersion(eligible.id), 1)
    const produced = isolatedDatabase.prepare(`
      SELECT account_id, input_version, event_kind, reason, status
      FROM account_health_jobs_input_outbox WHERE account_id = ?
    `).get(eligible.id) as Record<string, unknown>
    assert.deepEqual({ ...produced }, {
      account_id: eligible.id, input_version: 1, event_kind: 'snapshot',
      reason: 'j1_missing_input_bootstrap', status: 'pending'
    })

    const repeat = await runAccountHealthJobsInputBackfill(dependencies, { execute: true, limit: 10 })
    assert.equal(repeat.queued, 0)
    assert.equal(repeat.skippedIneligible, 1)
    assert.equal((isolatedDatabase.prepare('SELECT count(*) AS count FROM account_health_jobs_input_outbox WHERE account_id = ?').get(eligible.id) as { count: number }).count, 1)
    assert.equal((isolatedDatabase.prepare('SELECT count(*) AS count FROM account_health_jobs_input_outbox WHERE account_id = ?').get(ineligible.id) as { count: number }).count, 0)

    isolatedDatabase.prepare('DELETE FROM account_health_jobs_input_versions WHERE account_id = ?').run(eligible.id)
    isolatedDatabase.prepare('DELETE FROM account_health_jobs_input_outbox WHERE account_id = ?').run(eligible.id)
    isolatedDatabase.prepare('UPDATE accounts SET config_revision = config_revision + 1, dispatch_revision = dispatch_revision + 1 WHERE id = ?').run(eligible.id)
    const staleRevision = await dependencies.enqueueMissing({
      accountId: eligible.id, configRevision: eligible.configRevision ?? 0, dispatchRevision: eligible.dispatchRevision ?? 0,
      kind: 'snapshot', reason: 'j1_missing_input_bootstrap'
    })
    assert.equal(staleRevision, undefined)
    assert.equal(currentAccountHealthJobsInputVersion(eligible.id), undefined)
    assert.equal((isolatedDatabase.prepare('SELECT count(*) AS count FROM account_health_jobs_input_outbox WHERE account_id = ?').get(eligible.id) as { count: number }).count, 0)
  } finally {
    closeStorageDatabases()
  }
} finally {
  closeStorageDatabases()
  rmSync(productionRoot, { recursive: true, force: true })
}

const database = new Constructor(':memory:')
try {
  database.exec(`
    CREATE TABLE accounts (id TEXT PRIMARY KEY, config_revision INTEGER NOT NULL, dispatch_revision INTEGER NOT NULL, deleted_at TEXT);
    CREATE TABLE account_health_jobs_input_versions (account_id TEXT PRIMARY KEY, current_version INTEGER NOT NULL, reserved_at TEXT NOT NULL);
    CREATE TABLE account_health_jobs_input_outbox (
      event_id TEXT PRIMARY KEY, account_id TEXT NOT NULL, input_version INTEGER NOT NULL, event_kind TEXT NOT NULL,
      reason TEXT NOT NULL, config_revision INTEGER NOT NULL, dispatch_revision INTEGER NOT NULL, status TEXT NOT NULL,
      claim_token TEXT, claimed_until TEXT, attempt_count INTEGER NOT NULL DEFAULT 0, available_at TEXT NOT NULL,
      last_error TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE (account_id, input_version)
    );
  `)
  database.prepare(`INSERT INTO accounts (id, config_revision, dispatch_revision, deleted_at) VALUES
    ('aaa-ineligible-1', 1, 1, NULL), ('aaa-ineligible-2', 1, 1, NULL), ('late-eligible', 9, 9, NULL), ('existing', 7, 7, NULL)`).run()
  database.prepare('INSERT INTO account_health_jobs_input_versions (account_id, current_version, reserved_at) VALUES (?, ?, ?)').run('existing', 7, '2030-01-01T00:00:00.000Z')
  const dependencies = {
    async listMissingAccountIds(limit: number, afterId?: string): Promise<string[]> {
      return (database.prepare(`SELECT a.id FROM accounts a LEFT JOIN account_health_jobs_input_versions v ON v.account_id = a.id WHERE a.deleted_at IS NULL AND v.account_id IS NULL AND (? IS NULL OR a.id > ?) ORDER BY a.id ASC LIMIT ?`).all(afterId ?? null, afterId ?? null, limit) as Array<{ id: string }>).map((row) => row.id)
    },
    async findEligibleAccount(accountId: string) { return accountId === 'late-eligible' ? { id: accountId, configRevision: 9, dispatchRevision: 9 } : undefined },
    async enqueueMissing(intent: { accountId: string; configRevision: number; dispatchRevision: number; kind: 'snapshot'; reason: string }) {
      return await runInDatabaseTransaction(() => {
        const current = database.prepare('SELECT config_revision, dispatch_revision FROM accounts WHERE id = ? AND deleted_at IS NULL').get(intent.accountId) as { config_revision?: number; dispatch_revision?: number } | undefined
        if (current?.config_revision !== intent.configRevision || current?.dispatch_revision !== intent.dispatchRevision) return undefined
        if (currentAccountHealthJobsInputVersion(intent.accountId, database) !== undefined) return undefined
        return reserveAndEnqueueAccountHealthJobsInputInTransaction(intent, database)
      }, database)
    }
  }
  const pagination = await runAccountHealthJobsInputBackfill(dependencies, { execute: true, limit: 1 })
  assert.equal(pagination.scanned, 3)
  assert.equal(pagination.skippedIneligible, 2)
  assert.equal(pagination.queued, 1)
  const repeat = await runAccountHealthJobsInputBackfill({ ...dependencies, async listMissingAccountIds() { return ['late-eligible'] } }, { execute: true, limit: 1 })
  assert.equal(repeat.skippedExisting, 1)
  database.prepare('DELETE FROM account_health_jobs_input_versions WHERE account_id = ?').run('late-eligible')
  database.prepare('DELETE FROM account_health_jobs_input_outbox WHERE account_id = ?').run('late-eligible')
  database.prepare('UPDATE accounts SET config_revision = ?, dispatch_revision = ? WHERE id = ?').run(10, 10, 'late-eligible')
  const stale = await runAccountHealthJobsInputBackfill(dependencies, { execute: true, limit: 1 })
  assert.equal(stale.queued, 0)
} finally {
  database.close()
}

console.log('account-health-jobs-input-backfill-regression passed')
