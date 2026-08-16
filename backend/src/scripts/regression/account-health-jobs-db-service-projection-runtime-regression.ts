import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { DatabaseSync } from 'node:sqlite'

import { OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { applyBusinessSchema, seedDefaults } from '../../storage/schema.js'

const require = createRequire(import.meta.url)
const Constructor = require('node:sqlite').DatabaseSync as new (path: string) => DatabaseSync
const backendRoot = resolve(import.meta.dirname, '../../..')
const root = mkdtempSync(join(tmpdir(), 'juhe-ai-j1-db-service-projector-'))
const businessPath = join(root, 'business.sqlite3')
const outcomesPath = join(root, 'jobs-outcomes.sqlite3')
const inputDirectory = join(root, 'input')
const accountId = 'j1-db-service-projector-account'

try {
  createBusinessFixture()
  createJobsOutcomeFixture()
  const child = spawnSync(process.execPath, [
    '--import',
    'tsx',
    '--input-type=module',
    '-e',
    `
      import { getBusinessDatabase } from './src/storage/database.js'
      import { startAccountHealthJobsInputPublisherRuntime, stopAccountHealthJobsInputPublisherRuntime } from './src/modules/background/account-health-jobs-input-publisher-runtime.service.js'
      import { startAccountHealthJobsOutcomeProjectionRuntime, stopAccountHealthJobsOutcomeProjectionRuntime } from './src/modules/background/account-health-jobs-outcome-projection-runtime.service.js'
      const database = getBusinessDatabase()
      startAccountHealthJobsInputPublisherRuntime()
      startAccountHealthJobsOutcomeProjectionRuntime()
      const deadline = Date.now() + 5000
      while (Date.now() < deadline) {
        const receipt = database.prepare('SELECT disposition FROM account_health_projection_receipts WHERE outcome_id = ?').get('j1-db-service-projector-outcome')
        if (receipt?.disposition === 'applied') {
          const account = database.prepare('SELECT last_health_check_at, last_health_success_at FROM accounts WHERE id = ?').get('${accountId}')
          await stopAccountHealthJobsOutcomeProjectionRuntime()
          await stopAccountHealthJobsInputPublisherRuntime()
          process.stdout.write(JSON.stringify({ receipt, account }) + '\\n')
          process.exit(0)
        }
        await new Promise((resolve) => setTimeout(resolve, 20))
      }
      await stopAccountHealthJobsOutcomeProjectionRuntime()
      await stopAccountHealthJobsInputPublisherRuntime()
      throw new Error('DB-service projector did not apply the J1 outcome before timeout')
    `
  ], {
    cwd: backendRoot,
    encoding: 'utf8',
    env: {
      ...process.env,
      JUHE_AI_DISABLE_BASE_ENV: 'true',
      NODE_ENV: 'test',
      JUHE_AI_RUNTIME_MODE: 'standalone',
      JUHE_AI_DATABASE_DRIVER: 'sqlite',
      JUHE_AI_CACHE_DRIVER: 'memory',
      JUHE_AI_RUNTIME_STATE_DRIVER: 'memory',
      JUHE_AI_QUEUE_DRIVER: 'memory',
      JUHE_AI_PROCESS_ROLE: 'db-service',
      JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER: 'go',
      JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY: inputDirectory,
      JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY: 'j1-db-service-projector-runtime-signing-key',
      JUHE_AI_ACCOUNT_HEALTH_INPUT_PUBLISHER_ENABLED: 'true',
      JUHE_AI_ACCOUNT_HEALTH_INPUT_PUBLISHER_POLL_MS: '100',
      JUHE_AI_ACCOUNT_HEALTH_JOBS_PROJECTION_ENABLED: 'true',
      JUHE_AI_ACCOUNT_HEALTH_JOBS_OUTCOME_SQLITE_PATH: outcomesPath,
      JUHE_AI_ACCOUNT_HEALTH_JOBS_PROJECTION_POLL_MS: '100',
      JUHE_AI_DATABASE_PATH: businessPath,
      JUHE_AI_DATASET_DATABASE_PATH: join(root, 'dataset.sqlite3'),
      JUHE_AI_USAGE_CATALOG_DATABASE_PATH: join(root, 'usage.sqlite3'),
      JUHE_AI_STATS_DATABASE_PATH: join(root, 'stats.sqlite3'),
      JUHE_AI_RUNTIME_LOG_DATABASE_PATH: join(root, 'runtime-log.sqlite3'),
      JUHE_AI_TABLE_MONITOR_DATABASE_PATH: join(root, 'table-monitor.sqlite3'),
      JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT: join(root, 'codex-shards')
    }
  })
  const output = `${child.stdout ?? ''}${child.stderr ?? ''}`
  assert.equal(child.status, 0, output)
  const resultLine = output.split(/\r?\n/u).find((line) => line.startsWith('{"receipt":'))
  assert(resultLine, output)
  const result = JSON.parse(resultLine) as {
    receipt: { disposition: string }
    account: { last_health_check_at: string, last_health_success_at: string }
  }
  assert.equal(result.receipt.disposition, 'applied')
  assert.equal(result.account.last_health_check_at, '2026-08-17T00:00:00.000Z')
  assert.equal(result.account.last_health_success_at, '2026-08-17T00:00:00.000Z')

  const business = new Constructor(businessPath)
  try {
    const cursor = business.prepare(`
      SELECT observed_at, outcome_id FROM account_health_projection_cursors
      WHERE consumer_key = 'juhe-ai-account-health-jobs-projector-v1'
    `).get() as { observed_at: string, outcome_id: string } | undefined
    assert(cursor)
    assert.equal(cursor.observed_at, '2026-08-17T00:00:00.000Z')
    assert.equal(cursor.outcome_id, 'j1-db-service-projector-outcome')
  } finally {
    business.close()
  }
} finally {
  rmSync(root, { recursive: true, force: true })
}

console.log('account-health-jobs-db-service-projection-runtime-regression passed')

function createBusinessFixture(): void {
  const database = new Constructor(businessPath)
  try {
    applyBusinessSchema(database)
    seedDefaults(database)
    database.prepare(`
      INSERT INTO accounts (
        id, config_revision, dispatch_revision, system_account_id,
        provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
        name, type, status, credentials_encrypted, schedulable,
        health_check_model, health_check_endpoint_mode, created_at, updated_at
      ) VALUES (?, 2, 3, 'sys_admin', 'openai', ?, 'openai', 'v1', ?, 'api_key', 'active', 'fixture', 1, 'gpt-j1-test', 'chat_json', ?, ?)
    `).run(accountId, OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID, 'J1 DB-service projector fixture', '2026-08-17T00:00:00.000Z', '2026-08-17T00:00:00.000Z')
    database.prepare(`
      INSERT INTO account_health_jobs_input_versions(account_id, current_version, reserved_at)
      VALUES (?, 1, '2026-08-17T00:00:00.000Z')
    `).run(accountId)
  } finally {
    database.close()
  }
}

function createJobsOutcomeFixture(): void {
  const database = new Constructor(outcomesPath)
  try {
    const outcome = {
      outcome_id: 'j1-db-service-projector-outcome',
      request_id: 'j1-db-service-projector-request',
      account_id: accountId,
      outcome: 'complete_success',
      observed_at: '2026-08-17T00:00:00.000Z',
      input_version: 1,
      config_revision: 2,
      dispatch_revision: 3,
      status_code: 200,
      next_due_at: '2026-08-17T01:00:00.000Z',
      projection: {
        target_account_id: accountId,
        transition_kind: 'health_success',
        input_version: 1,
        config_revision: 2,
        dispatch_revision: 3,
        expected_account_status: 'active'
      }
    }
    database.exec('CREATE TABLE account_health_outcomes(outcome_id TEXT PRIMARY KEY, observed_at TEXT NOT NULL, payload TEXT NOT NULL)')
    database.prepare('INSERT INTO account_health_outcomes(outcome_id, observed_at, payload) VALUES (?, ?, ?)').run(
      outcome.outcome_id,
      outcome.observed_at,
      JSON.stringify(outcome)
    )
  } finally {
    database.close()
  }
}
