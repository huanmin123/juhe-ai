import assert from 'node:assert/strict'
import { existsSync, mkdtempSync, readdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { publishNextAccountHealthJobsInputFromBusinessOutbox } from '../../modules/background/account-health-jobs-input-publisher.service.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'

const accountId = process.env.JUHE_AI_J1_INPUT_PUBLISHER_POSTGRES_SMOKE_ACCOUNT_ID?.trim()
if (runtimeConfig.databaseDriver !== 'postgres' || !accountId) {
  throw new Error('J1 PG input publisher smoke 需要 PostgreSQL 和 fixture account ID')
}
const inputDirectory = mkdtempSync(join(tmpdir(), 'juhe-ai-j1-input-publisher-pg-'))
runtimeConfig.accountHealthJobs.inputDirectory = inputDirectory
runtimeConfig.accountHealthJobs.inputSigningKey = Buffer.alloc(32, 23).toString('base64url')

try {
  let published = false
  for (let index = 0; index < 16; index += 1) {
    const disposition = await publishNextAccountHealthJobsInputFromBusinessOutbox()
    const pool = await getPostgresPool()
    const row = await pool.query(`
      SELECT status
      FROM juhe_business.account_health_jobs_input_outbox
      WHERE account_id = $1
      ORDER BY input_version DESC
      LIMIT 1
    `, [accountId])
    if (row.rows[0]?.status === 'published') {
      published = true
      break
    }
    assert.notEqual(disposition, 'retry_scheduled', 'PG publisher 不得把有效当前 input 变为可重试失败')
  }
  assert.equal(published, true, 'PG publisher 必须发布 fixture account 的当前 snapshot')
  assert.equal(existsSync(inputDirectory), true, 'PG publisher 必须创建签名 input 目录')
  assert.ok(readdirSync(inputDirectory).some((name) => name.endsWith('.account-health-input.json')), 'PG publisher 必须原子发布签名 input 文件')
  console.log('account-health-jobs-input-publisher-postgres-smoke passed')
} finally {
  await closePostgresPool()
  rmSync(inputDirectory, { recursive: true, force: true })
}
