import { runtimeConfig } from '../../config/runtime.js'
import { applyPostgresSchema, buildPostgresSchemaSql } from '../../storage/postgres-schema.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { seedPostgresDefaults } from '../../storage/postgres-seed-defaults.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'

async function main(): Promise<void> {
  const args = new Set(process.argv.slice(2))
  if (args.has('--print')) {
    process.stdout.write(`${buildPostgresSchemaSql()}\n`)
    return
  }

  if (runtimeConfig.databaseDriver !== 'postgres') {
    throw new Error('初始化 PostgreSQL schema 前必须设置 JUHE_AI_DATABASE_DRIVER=postgres 或 JUHE_AI_RUNTIME_MODE=performance')
  }

  const pool = await getPostgresPool()
  const databaseClient = createPostgresDatabaseClient(pool)
  const schemaResult = await applyPostgresSchema(databaseClient)
  const seedResult = args.has('--schema-only')
    ? { statementCount: 0 }
    : await seedPostgresDefaults(databaseClient)
  console.log(`PostgreSQL 初始化完成：schemas=${schemaResult.schemaCount}, schemaStatements=${schemaResult.statementCount}, seedStatements=${seedResult.statementCount}`)
}

main()
  .catch((error: unknown) => {
    const message = error instanceof Error ? error.message : String(error)
    console.error(`PostgreSQL schema 初始化失败：${message}`)
    process.exitCode = 1
  })
  .finally(async () => {
    await closePostgresPool()
  })
