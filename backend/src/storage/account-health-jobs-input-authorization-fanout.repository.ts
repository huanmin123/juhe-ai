import type { DatabaseSync } from 'node:sqlite'

import type { DatabaseClient } from './database-client.js'
import { getBusinessDatabase } from './database.js'
import {
  reserveAndEnqueueAccountHealthJobsInputInTransaction,
  reserveAndEnqueueAccountHealthJobsInputInTransactionAsync
} from './account-health-jobs-input-outbox.repository.js'

type AuthorizationSource = {
  resource_type: string
  resource_id: string
}

type AccountHealthInputEpoch = {
  id: string
  config_revision: number | string | bigint
  dispatch_revision: number | string | bigint
}

// Authorization changes are business-configuration changes for every
// authorization instance backed by that physical account.  This fanout is
// intentionally transaction-only: it reserves each instance's input epoch and
// its durable publish intent together with the authorization mutation.
export function enqueueAccountHealthJobsInputsForAuthorizationSourceInTransaction(
  authorization: AuthorizationSource,
  reason: string,
  database: DatabaseSync = getBusinessDatabase()
): void {
  if (authorization.resource_type !== 'account') return
  const accounts = database.prepare(`
    SELECT id, config_revision, dispatch_revision
    FROM accounts
    WHERE authorization_instance_source_account_id = ?
      AND deleted_at IS NULL
      AND provider_code IN ('gpt', 'openai')
      AND type IN ('api_key', 'oauth')
    ORDER BY id ASC
  `).all(authorization.resource_id) as AccountHealthInputEpoch[]
  for (const account of accounts) {
    reserveAndEnqueueAccountHealthJobsInputInTransaction({
      accountId: account.id,
      configRevision: Number(account.config_revision),
      dispatchRevision: Number(account.dispatch_revision),
      kind: 'snapshot',
      reason
    }, database)
  }
}

export async function enqueueAccountHealthJobsInputsForAuthorizationSourceInTransactionAsync(
  client: DatabaseClient,
  authorization: AuthorizationSource,
  reason: string
): Promise<void> {
  if (authorization.resource_type !== 'account') return
  const accounts = await client.query<AccountHealthInputEpoch>(`
    SELECT id, config_revision, dispatch_revision
    FROM ${client.dialect.qualifyTable('juhe_business', 'accounts')}
    WHERE authorization_instance_source_account_id = ?
      AND deleted_at IS NULL
      AND provider_code IN ('gpt', 'openai')
      AND type IN ('api_key', 'oauth')
    ORDER BY id ASC
    ${client.driver === 'postgres' ? 'FOR UPDATE' : ''}
  `, [authorization.resource_id])
  for (const account of accounts) {
    await reserveAndEnqueueAccountHealthJobsInputInTransactionAsync(client, {
      accountId: account.id,
      configRevision: Number(account.config_revision),
      dispatchRevision: Number(account.dispatch_revision),
      kind: 'snapshot',
      reason
    })
  }
}
