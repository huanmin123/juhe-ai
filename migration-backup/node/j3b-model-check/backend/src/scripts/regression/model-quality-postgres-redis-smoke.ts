import { strict as assert } from 'node:assert'
import { createClient } from 'redis'
import { Pool } from 'pg'

import { collectPostgresSchemaStatements, type PostgresSchemaName } from '../../storage/postgres-schema.js'

const postgresUrl = process.env.JUHE_AI_MODEL_QUALITY_SMOKE_POSTGRES_URL?.trim()
if (!postgresUrl) throw new Error('缺少 JUHE_AI_MODEL_QUALITY_SMOKE_POSTGRES_URL')

const suffix = `mqtest_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`
const schemaMap: Record<PostgresSchemaName, string> = {
  juhe_business: `${suffix}_business`,
  juhe_chat: `${suffix}_chat`,
  juhe_dataset: `${suffix}_dataset`,
  juhe_usage: `${suffix}_usage`,
  juhe_stats: `${suffix}_stats`,
  juhe_codex_context: `${suffix}_codex`
}
const pool = new Pool({ connectionString: postgresUrl, max: 1, connectionTimeoutMillis: 30_000, idleTimeoutMillis: 2_000 })
const client = await pool.connect()

try {
  await client.query('SELECT 1')
  for (const schemaName of Object.values(schemaMap)) {
    assert(/^mqtest_[a-z0-9_]+$/.test(schemaName), '测试 schema 名称不安全')
    await client.query(`CREATE SCHEMA "${schemaName}"`)
  }
  for (const statement of collectPostgresSchemaStatements()) {
    const mappedSchema = schemaMap[statement.schemaName]
    const sql = replaceSchemaNames(statement.sql)
    await client.query(`SET search_path TO "${mappedSchema}", public`)
    await client.query(sql)
  }

  const columnResult = await client.query<{ column_name: string }>(`
    SELECT column_name FROM information_schema.columns
    WHERE table_schema = $1 AND table_name = 'model_check_runs'
      AND column_name = ANY($2::text[])
  `, [schemaMap.juhe_dataset, ['trigger_kind', 'schedule_id', 'policy_snapshot_json', 'quality_decision_json', 'quality_health_sync_status']])
  assert.equal(columnResult.rowCount, 5)
  const retryIndexResult = await client.query<{ indexname: string }>(`
    SELECT indexname FROM pg_indexes
    WHERE schemaname = $1 AND indexname = 'idx_model_check_runs_quality_health_sync_retry'
  `, [schemaMap.juhe_dataset])
  assert.equal(retryIndexResult.rowCount, 1)
  const tableResult = await client.query<{ table_name: string }>(`
    SELECT table_name FROM information_schema.tables
    WHERE table_schema = $1 AND table_name = ANY($2::text[])
  `, [schemaMap.juhe_business, ['model_quality_policies', 'model_quality_schedules', 'account_quality_enforcements']])
  assert.equal(tableResult.rowCount, 3)
  const statsResult = await client.query<{ table_name: string }>(`
    SELECT table_name FROM information_schema.tables
    WHERE table_schema = $1 AND table_name = 'account_quality_health_hourly'
  `, [schemaMap.juhe_stats])
  assert.equal(statsResult.rowCount, 1)
  console.log('postgres_model_quality_schema=passed')

  const redisUrls = [
    process.env.JUHE_AI_MODEL_QUALITY_SMOKE_REDIS_CACHE_URL,
    process.env.JUHE_AI_MODEL_QUALITY_SMOKE_REDIS_STATE_URL,
    process.env.JUHE_AI_MODEL_QUALITY_SMOKE_REDIS_QUEUE_URL
  ].filter((value): value is string => Boolean(value?.trim()))
  for (let index = 0; index < redisUrls.length; index += 1) {
    const redis = createClient({ url: redisUrls[index], socket: { connectTimeout: 8_000, reconnectStrategy: false } })
    try {
      await redis.connect()
      assert.equal(await redis.ping(), 'PONG')
      console.log(`redis_${index + 1}=passed`)
    } finally {
      if (redis.isOpen) await redis.quit()
    }
  }
} finally {
  for (const schemaName of Object.values(schemaMap).reverse()) {
    if (/^mqtest_[a-z0-9_]+$/.test(schemaName)) {
      await client.query(`DROP SCHEMA IF EXISTS "${schemaName}" CASCADE`)
    }
  }
  client.release()
  await pool.end()
}

function replaceSchemaNames(sql: string): string {
  let output = sql
  for (const [source, target] of Object.entries(schemaMap)) {
    output = output.replaceAll(source, target)
  }
  return output
}
