import { runtimeConfig } from '../../config/runtime.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  checkPostgresNodeSchemaPreflight,
  NODE_POSTGRES_SCHEMA_CONTRACT_VERSION
} from '../../storage/postgres-schema-owner-gate.js'

async function main(): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    throw new Error('Node PostgreSQL schema preflight 必须设置 JUHE_AI_DATABASE_DRIVER=postgres 或 JUHE_AI_RUNTIME_MODE=performance')
  }
  const pool = await getPostgresPool()
  try {
    const state = await checkPostgresNodeSchemaPreflight(pool)
    process.stdout.write(`${JSON.stringify({
      owner: 'node',
      contractVersion: NODE_POSTGRES_SCHEMA_CONTRACT_VERSION,
      ledger: 'absent',
      ...state
    })}\n`)
  } finally {
    await closePostgresPool()
  }
}

main().catch((error: unknown) => {
  const message = error instanceof Error ? error.message : String(error)
  console.error(`Node PostgreSQL schema preflight 失败：${message}`)
  process.exitCode = 1
})

