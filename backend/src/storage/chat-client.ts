import { runtimeConfig } from '../config/runtime.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getChatDatabase } from './database.js'
import { getPostgresPool } from './postgres-client.js'

export async function getChatDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') return createPostgresDatabaseClient(await getPostgresPool())
  return createSqliteDatabaseClient(getChatDatabase())
}
