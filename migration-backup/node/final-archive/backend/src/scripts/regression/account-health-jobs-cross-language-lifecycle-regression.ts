import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { createCipheriv, createHash, randomBytes } from 'node:crypto'
import { mkdtempSync, rmSync } from 'node:fs'
import { createServer } from 'node:http'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { DatabaseSync } from 'node:sqlite'

import { publishAccountHealthJobsInput } from '../../modules/background/account-health-jobs-input.protocol.js'
import { projectAccountHealthJobsOutcome } from '../../storage/account-health-projection.repository.js'
import type { AccountHealthJobsOutcome } from '../../storage/account-health-jobs-outcome.repository.js'
import { encryptJson } from '../../storage/crypto.js'

const require = createRequire(import.meta.url)
const Constructor = require('node:sqlite').DatabaseSync as new (path: string) => DatabaseSync
const jobsRoot = resolve(import.meta.dirname, '../../../../backend-go/projects/jobs')
const root = mkdtempSync(join(tmpdir(), 'juhe-ai-j1-cross-language-'))
const inputDirectory = join(root, 'input')
const storePath = join(root, 'j1-jobs.sqlite3')
const signingKey = randomBytes(32).toString('base64url')
const credentialSecret = 'j1-cross-language-credential-secret'
const accountId = 'j1-cross-language-account'
let upstreamServer: ReturnType<typeof createServer> | undefined

try {
  upstreamServer = createServer((request, response) => {
    assert.equal(request.method, 'POST')
    assert.equal(request.url, '/v1/chat/completions')
    assert.equal(request.headers.authorization, 'Bearer sk-j1-cross-language')
    response.setHeader('content-type', 'application/json')
    response.end(JSON.stringify({ choices: [{ message: { content: 'juhe' } }] }))
  })
  const upstreamAddress = await listen(upstreamServer)
  const now = new Date()
  publishAccountHealthJobsInput({
    root: inputDirectory,
    accountId,
    signingKey,
    payload: {
      account_id: accountId,
      input_version: 1,
      config_revision: 2,
      dispatch_revision: 3,
      provider: 'openai',
      type: 'api_key',
      endpoint_mode: 'chat_json',
      health_model: 'gpt-j1-test',
      base_url: upstreamAddress,
      key_set_fingerprint: 'j1-cross-language-keyset',
      api_keys: [{
        index: 0,
        fingerprint: 'j1-cross-language-key',
        credential: {
          kind: 'api_key',
          ciphertext: encryptV1(credentialSecret, JSON.stringify({ api_key: 'sk-j1-cross-language' }))
        }
      }],
      issued_at: now.toISOString(),
      expires_at: new Date(now.getTime() + 60 * 60 * 1000).toISOString(),
      tls_policy_version: 'j1-direct-upstream-v1',
      allow_insecure_base_url: true,
      eligibility: {
        account_status: 'active',
        schedulable: true,
        bound_group: true,
        authorization_eligible: true
      },
      schedule: {
        health_interval_ms: 60 * 60 * 1000,
        health_jitter_ms: 0,
        failure_threshold: 1,
        failure_retry_ms: 60 * 1000,
        cooldown_neutral_base_ms: 30 * 1000,
        cooldown_neutral_max_ms: 15 * 60 * 1000,
        cooldown_failure_backoff_ms: 60 * 1000
      }
    }
  })

  const result = await runGoCrossLanguageFixture({
    JUHE_AI_J1_CROSS_LANGUAGE_INPUT_DIRECTORY: inputDirectory,
    JUHE_AI_J1_CROSS_LANGUAGE_STORE_PATH: storePath,
    JUHE_AI_J1_CROSS_LANGUAGE_SIGNING_KEY: signingKey,
    JUHE_AI_J1_CROSS_LANGUAGE_CREDENTIAL_SECRET: credentialSecret,
    GOCACHE: join(root, 'go-cache')
  })
  assert.equal(result.code, 0, `${result.stdout}\n${result.stderr}`)

  const outcomeDatabase = new Constructor(storePath)
  let outcome: AccountHealthJobsOutcome
  try {
    const row = outcomeDatabase.prepare('SELECT payload FROM account_health_outcomes').get() as { payload: string } | undefined
    assert(row, 'Go jobs 必须向 jobs-owned SQLite 写入 outcome')
    outcome = JSON.parse(row.payload) as AccountHealthJobsOutcome
  } finally {
    outcomeDatabase.close()
  }
  assert.equal(outcome.account_id, accountId)
  assert.equal(outcome.outcome, 'complete_success')
  assert.equal(outcome.projection?.transition_kind, 'health_success')
  assert.equal(outcome.projection?.target_account_id, accountId)

  const businessDatabase = new Constructor(':memory:')
  try {
    createBusinessProjectionFixture(businessDatabase)
    const projected = projectAccountHealthJobsOutcome(outcome, businessDatabase)
    assert.deepEqual(projected, {
      outcomeId: outcome.outcome_id,
      accountId,
      inputVersion: 1,
      disposition: 'applied',
      changed: true
    })
    const account = businessDatabase.prepare(`
      SELECT last_health_check_at, last_health_success_at, health_check_failure_count
      FROM accounts WHERE id = ?
    `).get(accountId) as Record<string, unknown>
    assert.equal(account.last_health_check_at, outcome.observed_at)
    assert.equal(account.last_health_success_at, outcome.observed_at)
    assert.equal(account.health_check_failure_count, 0)
  } finally {
    businessDatabase.close()
  }
} finally {
  await closeServer(upstreamServer)
  rmSync(root, { recursive: true, force: true })
}

console.log('account-health-jobs-cross-language-lifecycle-regression passed')

function encryptV1(secret: string, value: string): string {
  const key = createHash('sha256').update(secret, 'utf8').digest()
  const iv = randomBytes(12)
  const cipher = createCipheriv('aes-256-gcm', key, iv)
  const ciphertext = Buffer.concat([cipher.update(value, 'utf8'), cipher.final()])
  return `v1:${iv.toString('base64url')}:${cipher.getAuthTag().toString('base64url')}:${ciphertext.toString('base64url')}`
}

async function listen(server: ReturnType<typeof createServer>): Promise<string> {
  await new Promise<void>((resolveListen, rejectListen) => {
    server.once('error', rejectListen)
    server.listen(0, '127.0.0.1', () => {
      server.off('error', rejectListen)
      resolveListen()
    })
  })
  const address = server.address()
  assert(address && typeof address !== 'string')
  return `http://127.0.0.1:${address.port}`
}

async function closeServer(server: ReturnType<typeof createServer> | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolveClose, rejectClose) => server.close((error) => error ? rejectClose(error) : resolveClose()))
}

async function runGoCrossLanguageFixture(overrides: Record<string, string>): Promise<{ code: number | null, stdout: string, stderr: string }> {
  return await new Promise((resolveRun, rejectRun) => {
    const child = spawn(process.env.JUHE_AI_GO_BINARY?.trim() || 'go', [
      'test', './internal/accounthealth', '-run', '^TestNodePublishedInputCrossLanguageFixture$', '-count=1'
    ], {
      cwd: jobsRoot,
      env: { ...process.env, ...overrides },
      stdio: ['ignore', 'pipe', 'pipe']
    })
    let stdout = ''
    let stderr = ''
    child.stdout.setEncoding('utf8').on('data', (chunk: string) => { stdout += chunk })
    child.stderr.setEncoding('utf8').on('data', (chunk: string) => { stderr += chunk })
    child.once('error', rejectRun)
    child.once('close', (code) => resolveRun({ code, stdout, stderr }))
  })
}

function createBusinessProjectionFixture(database: DatabaseSync): void {
  database.exec(`
    CREATE TABLE accounts (
      id TEXT PRIMARY KEY,
      status TEXT NOT NULL,
      schedulable INTEGER NOT NULL,
      config_revision INTEGER NOT NULL,
      dispatch_revision INTEGER NOT NULL,
      authorization_instance_source_account_id TEXT,
      cooldown_until TEXT,
      cooldown_retest_failure_count INTEGER NOT NULL DEFAULT 0,
      cooldown_retest_observation_started_at TEXT,
      cooldown_retest_generation TEXT,
      type TEXT NOT NULL DEFAULT 'api_key',
      credentials_encrypted TEXT NOT NULL DEFAULT '',
      balance_query_enabled INTEGER NOT NULL DEFAULT 0,
      balance_query_config_json TEXT NOT NULL DEFAULT '{}',
      balance_query_next_refresh_at TEXT,
      availability_schedule_json TEXT,
      availability_schedule_next_check_at TEXT,
      cooldown_retest_last_at TEXT,
      cooldown_retest_last_status_code INTEGER,
      last_error_code TEXT,
      last_error_message TEXT,
      last_error_trace_id TEXT,
      last_health_check_at TEXT,
      next_health_check_at TEXT,
      last_health_success_at TEXT,
      health_check_failure_count INTEGER NOT NULL DEFAULT 0,
      health_check_failure_started_at TEXT,
      last_health_check_status_code INTEGER,
      last_health_check_error_code TEXT,
      last_health_check_error_message TEXT,
      last_health_check_trace_id TEXT,
      deleted_at TEXT,
      updated_at TEXT NOT NULL
    );
    CREATE TABLE account_health_jobs_input_versions (
      account_id TEXT PRIMARY KEY,
      current_version INTEGER NOT NULL,
      reserved_at TEXT NOT NULL
    );
    CREATE TABLE account_health_projection_receipts (
      outcome_id TEXT PRIMARY KEY,
      account_id TEXT NOT NULL,
      input_version INTEGER NOT NULL,
      disposition TEXT NOT NULL,
      reason TEXT,
      applied_at TEXT NOT NULL
    );
  `)
  database.prepare(`
    INSERT INTO accounts(id, status, schedulable, config_revision, dispatch_revision, credentials_encrypted, updated_at)
    VALUES (?, 'active', 1, 2, 3, ?, ?)
  `).run(accountId, encryptJson({ api_key: 'sk-j1-cross-language' }), '2026-08-17T00:00:00.000Z')
  database.prepare(`
    INSERT INTO account_health_jobs_input_versions(account_id, current_version, reserved_at)
    VALUES (?, 1, ?)
  `).run(accountId, '2026-08-17T00:00:00.000Z')
}
