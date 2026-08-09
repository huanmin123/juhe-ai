import { runtimeConfig } from '../config/runtime.js'
import { buildSystemAccountScopeClause, type AccessScope } from './access-scope.js'
import { getBusinessDatabase } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { canManageResourceOwner } from './resource-authorization-helpers.js'

const businessSchemaName = 'juhe_business'

interface AccountApiKeyRuntimeRow {
  id: string
  config_revision: number
  system_account_id: string
  authorized_instance: number
}

export interface AccountApiKeyRuntimeAccountProjection {
  id: string
  configRevision: number
  accessType: 'owner' | 'authorized'
  ownerSystemAccountId: string
}

export async function findAccountApiKeyRuntimeAccountAsync(
  accountId: string,
  access?: AccessScope
): Promise<AccountApiKeyRuntimeAccountProjection | undefined> {
  const id = accountId.trim()
  if (!id) return undefined

  const client = await accountApiKeyRuntimeDatabaseClient()
  const ownerScope = buildSystemAccountScopeClause(access, 'accounts.system_account_id')
  const row = await client.one<AccountApiKeyRuntimeRow>(`
    SELECT
      accounts.id,
      accounts.config_revision,
      accounts.system_account_id,
      CASE
        WHEN accounts.authorization_instance_authorization_id IS NOT NULL
          OR accounts.authorization_instance_source_account_id IS NOT NULL
        THEN 1
        ELSE 0
      END AS authorized_instance
    FROM ${accountApiKeyRuntimeTable(client, 'accounts')} accounts
    WHERE accounts.id = ?
      AND accounts.deleted_at IS NULL
      ${ownerScope.clause}
    LIMIT 1
  `, [id, ...ownerScope.params])
  if (!row || !canManageResourceOwner(row.system_account_id, access)) return undefined

  return {
    id: row.id,
    configRevision: Number(row.config_revision ?? 1),
    accessType: Number(row.authorized_instance) === 1 ? 'authorized' : 'owner',
    ownerSystemAccountId: row.system_account_id
  }
}

async function accountApiKeyRuntimeDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function accountApiKeyRuntimeTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}
