import { runtimeConfig } from '../../config/runtime.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { runGooseSchemaUp } from '../../storage/postgres-goose-schema-migration.js'
import { EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION, checkPostgresGooseSchemaVersion } from '../../storage/postgres-goose-schema-gate.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { applyPostgresSchema, buildPostgresSchemaSql } from '../../storage/postgres-schema.js'
import { initializePostgresSchemaWithGoose } from '../../storage/postgres-schema-bootstrap.js'
import { seedPostgresDefaults } from '../../storage/postgres-seed-defaults.js'

async function main(): Promise<void> {
  const args = new Set(process.argv.slice(2))
  if (args.has('--print')) {
    process.stdout.write(`${buildPostgresSchemaSql()}\n`)
    return
  }

  if (runtimeConfig.databaseDriver !== 'postgres') {
    throw new Error('初始化 PostgreSQL schema 前必须设置 JUHE_AI_DATABASE_DRIVER=postgres 或 JUHE_AI_RUNTIME_MODE=performance')
  }

  const postgresURL = runtimeConfig.postgres.url?.trim()
  if (!postgresURL) {
    throw new Error('初始化 PostgreSQL schema 前必须设置 JUHE_AI_POSTGRES_URL')
  }

  const pool = await getPostgresPool()
  const connection = await pool.connect()
  try {
    const databaseClient = createPostgresDatabaseClient(connection)
    const result = await initializePostgresSchemaWithGoose({
      client: databaseClient,
      expectedVersion: EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION,
      schemaOnly: args.has('--schema-only'),
      migrate: () => runGooseSchemaUp({
        postgresURL,
        maintenanceBinary: process.env.JUHE_AI_MAINTENANCE_BINARY,
        goBackendRoot: process.env.JUHE_AI_GO_BACKEND_ROOT,
        migrationDir: process.env.JUHE_AI_MIGRATION_DIR
      }),
      verify: () => checkPostgresGooseSchemaVersion(pool),
      applySchema: () => applyPostgresSchema(databaseClient),
      seed: () => seedPostgresDefaults(databaseClient)
    })
    console.log(`PostgreSQL 初始化完成：schemas=${result.schemaCount}, schemaStatements=${result.schemaStatementCount}, seedStatements=${result.seedStatementCount}, gooseSchemaVersion=${EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION}`)
  } finally {
    connection.release(true)
  }
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
