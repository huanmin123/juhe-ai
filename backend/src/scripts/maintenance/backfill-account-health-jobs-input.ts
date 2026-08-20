import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'
import type { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import {
  findAccountForAccountHealthJobsInput,
  findAccountForAccountHealthJobsInputAsync,
  findAccountHealthJobsInputRevisions,
  findAccountHealthJobsInputRevisionsAsync
} from '../../storage/account-health-jobs-input.repository.js'
import {
  currentAccountHealthJobsInputVersion,
  currentAccountHealthJobsInputVersionAsync
} from '../../storage/account-health-jobs-input-version.repository.js'
import {
  reserveAndEnqueueAccountHealthJobsInputInTransaction,
  reserveAndEnqueueAccountHealthJobsInputInTransactionAsync,
  type ReservedAccountHealthJobsInputIntent
} from '../../storage/account-health-jobs-input-outbox.repository.js'
import { getBusinessDatabase, runInDatabaseTransaction } from '../../storage/database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'

const DEFAULT_LIMIT = 1_000
const MAX_LIMIT = 100_000
const BACKFILL_REASON = 'j1_missing_input_bootstrap'

export interface AccountHealthJobsInputBackfillStats {
  dryRun: boolean
  limit: number
  scanned: number
  eligible: number
  queued: number
  skippedIneligible: number
  skippedExisting: number
  failed: number
  accountIds: string[]
}

export interface AccountHealthJobsInputBackfillDependencies {
  listMissingAccountIds: (limit: number, afterId?: string) => Promise<string[]>
  findEligibleAccount: (accountId: string) => Promise<{ id: string; configRevision: number; dispatchRevision: number } | undefined>
  enqueueMissing: (intent: {
    accountId: string
    configRevision: number
    dispatchRevision: number
    kind: 'snapshot'
    reason: string
  }) => Promise<ReservedAccountHealthJobsInputIntent | undefined>
}

export async function runAccountHealthJobsInputBackfill(
  dependencies: AccountHealthJobsInputBackfillDependencies,
  options: { execute: boolean; limit: number }
): Promise<AccountHealthJobsInputBackfillStats> {
  const limit = normalizeLimit(options.limit)
  const stats: AccountHealthJobsInputBackfillStats = {
    dryRun: !options.execute,
    limit,
    scanned: 0,
    eligible: 0,
    queued: 0,
    skippedIneligible: 0,
    skippedExisting: 0,
    failed: 0,
    accountIds: []
  }

  let afterId: string | undefined
  while (stats.eligible < limit) {
    const accountIds = await dependencies.listMissingAccountIds(limit, afterId)
    if (accountIds.length === 0) break
    for (const accountId of accountIds) {
      if (stats.eligible >= limit) break
      stats.scanned += 1
      const account = await dependencies.findEligibleAccount(accountId)
      if (!account) {
        stats.skippedIneligible += 1
        continue
      }
      stats.eligible += 1
      stats.accountIds.push(account.id)
      if (!options.execute) continue
      try {
        const reserved = await dependencies.enqueueMissing({
          accountId: account.id,
          configRevision: account.configRevision,
          dispatchRevision: account.dispatchRevision,
          kind: 'snapshot',
          reason: BACKFILL_REASON
        })
        if (reserved) stats.queued += 1
        else stats.skippedExisting += 1
      } catch (error) {
        stats.failed += 1
        await writeStructuredLine(process.stderr, { event: 'j1_input_backfill_failed', accountId: account.id, error: safeError(error) })
      }
    }
    afterId = accountIds[accountIds.length - 1]
    if (accountIds.length < limit) break
  }
  return stats
}

function normalizeLimit(value: number): number {
  if (!Number.isSafeInteger(value) || value < 1 || value > MAX_LIMIT) {
    throw new Error(`--limit must be an integer in the range 1..${MAX_LIMIT}`)
  }
  return value
}

function safeError(error: unknown): string {
  if (!(error instanceof Error)) return 'unknown_error'
  const name = error.name.trim()
  return name ? name.slice(0, 100) : 'unknown_error'
}

function parseArguments(argv: string[]): { execute: boolean; limit: number } {
  let execute = false
  let limit = DEFAULT_LIMIT
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index]
    if (argument === '--execute') {
      execute = true
      continue
    }
    if (argument === '--limit') {
      const value = argv[index + 1]
      if (value === undefined) throw new Error('--limit requires a value')
      limit = Number(value)
      index += 1
      continue
    }
    if (argument === '--help' || argument === '-h') {
      throw new Error('usage: pnpm maintenance:backfill-account-health-jobs-input [--execute] [--limit N]')
    }
    throw new Error(`unknown argument: ${argument}`)
  }
  return { execute, limit: normalizeLimit(limit) }
}

async function listMissingAccountIdsSqlite(database: DatabaseSync, limit: number, afterId?: string): Promise<string[]> {
  const cursorClause = afterId === undefined ? '' : ' AND a.id > ?'
  const parameters = afterId === undefined ? [limit] : [afterId, limit]
  return (database.prepare(`
    SELECT a.id
    FROM accounts a
    LEFT JOIN account_health_jobs_input_versions v ON v.account_id = a.id
    WHERE a.deleted_at IS NULL AND v.account_id IS NULL${cursorClause}
    ORDER BY a.id ASC
    LIMIT ?
  `).all(...parameters) as Array<{ id: string }>).map((row) => row.id)
}

async function listMissingAccountIdsPostgres(client: DatabaseClient, limit: number, afterId?: string): Promise<string[]> {
  const cursorClause = afterId === undefined ? '' : ' AND a.id > ?'
  const parameters = afterId === undefined ? [limit] : [afterId, limit]
  const rows = await client.query<{ id: string }>(`
    SELECT a.id
    FROM juhe_business.accounts a
    LEFT JOIN juhe_business.account_health_jobs_input_versions v ON v.account_id = a.id
    WHERE a.deleted_at IS NULL AND v.account_id IS NULL${cursorClause}
    ORDER BY a.id ASC
    LIMIT ?
  `, parameters)
  return rows.map((row) => row.id)
}

// The SQLite production wrapper deliberately uses only synchronous adapters.
// Keep the public backfill contract async while ensuring this branch never
// reaches the PostgreSQL-only revision reader.
export function createSqliteAccountHealthJobsInputBackfillDependencies(): AccountHealthJobsInputBackfillDependencies {
  const database = getBusinessDatabase()
  return {
    listMissingAccountIds: async (limit, afterId) => await listMissingAccountIdsSqlite(database, limit, afterId),
    findEligibleAccount: async (accountId) => {
      const account = findAccountForAccountHealthJobsInput(accountId)
      if (!account) return undefined
      const revisions = findAccountHealthJobsInputRevisions(account.id)
      if (!revisions || revisions.configRevision !== account.configRevision) return undefined
      return { id: account.id, configRevision: revisions.configRevision, dispatchRevision: revisions.dispatchRevision }
    },
    enqueueMissing: async (intent) => await runInDatabaseTransaction(() => {
      const current = database.prepare(`
        SELECT config_revision, dispatch_revision
        FROM accounts
        WHERE id = ? AND deleted_at IS NULL
      `).get(intent.accountId) as { config_revision?: number; dispatch_revision?: number } | undefined
      if (current?.config_revision !== intent.configRevision || current?.dispatch_revision !== intent.dispatchRevision) return undefined
      if (currentAccountHealthJobsInputVersion(intent.accountId) !== undefined) return undefined
      return reserveAndEnqueueAccountHealthJobsInputInTransaction(intent)
    }, database)
  }
}

function sqliteDependencies(): AccountHealthJobsInputBackfillDependencies {
  return createSqliteAccountHealthJobsInputBackfillDependencies()
}

function postgresDependencies(client: DatabaseClient): AccountHealthJobsInputBackfillDependencies {
  return {
    listMissingAccountIds: async (limit, afterId) => await listMissingAccountIdsPostgres(client, limit, afterId),
    findEligibleAccount: async (accountId) => {
      const account = await findAccountForAccountHealthJobsInputAsync(accountId)
      if (!account) return undefined
      const revisions = await findAccountHealthJobsInputRevisionsAsync(account.id)
      if (!revisions || revisions.configRevision !== account.configRevision) return undefined
      return { id: account.id, configRevision: revisions.configRevision, dispatchRevision: revisions.dispatchRevision }
    },
    enqueueMissing: async (intent) => await client.transaction(async (tx) => {
      await tx.query('SELECT id FROM juhe_business.accounts WHERE id = ? AND deleted_at IS NULL FOR UPDATE', [intent.accountId])
      await tx.query('SELECT pg_advisory_xact_lock(hashtextextended(?, 0))', [intent.accountId])
      const current = await tx.one<{ config_revision?: number; dispatch_revision?: number }>(`
        SELECT config_revision, dispatch_revision
        FROM juhe_business.accounts
        WHERE id = ? AND deleted_at IS NULL
      `, [intent.accountId])
      if (current?.config_revision !== intent.configRevision || current?.dispatch_revision !== intent.dispatchRevision) return undefined
      if (await currentAccountHealthJobsInputVersionAsync(tx, intent.accountId) !== undefined) return undefined
      return await reserveAndEnqueueAccountHealthJobsInputInTransactionAsync(tx, intent)
    })
  }
}

export async function main(argv = process.argv.slice(2)): Promise<AccountHealthJobsInputBackfillStats> {
  const options = parseArguments(argv)
  const dependencies = runtimeConfig.databaseDriver === 'postgres'
    ? postgresDependencies(createPostgresDatabaseClient(await getPostgresPool()))
    : sqliteDependencies()
  const stats = await runAccountHealthJobsInputBackfill(dependencies, options)
  await writeStructuredLine(process.stdout, { event: 'j1_input_backfill_completed', ...stats })
  return stats
}

export async function runCli(
  argv = process.argv.slice(2),
  closePool: () => Promise<void> = closePostgresPool
): Promise<AccountHealthJobsInputBackfillStats> {
  try {
    return await main(argv)
  } finally {
    await closePool()
  }
}

export async function runProcessCli(
  argv = process.argv.slice(2),
  execute: (arguments_: string[]) => Promise<AccountHealthJobsInputBackfillStats> = main,
  exit: (code: number) => void = (code) => process.exit(code)
): Promise<void> {
  let exitCode = 0
  try {
    const stats = await execute(argv)
    if (stats.failed > 0) {
      await writeStructuredLine(process.stderr, { event: 'j1_input_backfill_failed', error: 'account_enqueue_failed', failed: stats.failed })
      exitCode = 1
    }
  } catch (error) {
    await writeStructuredLine(process.stderr, { event: 'j1_input_backfill_failed', error: safeError(error) })
    exitCode = 1
  }
  // One-shot maintenance commands must not remain alive because a repository
  // client retained an idle handle after its final durable write. The command
  // has already flushed its structured completion/failure record above.
  exit(exitCode)
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  await runProcessCli()
}

function writeStructuredLine(stream: NodeJS.WriteStream, record: Record<string, unknown>): Promise<void> {
  return new Promise((resolveWrite, rejectWrite) => {
    stream.write(`${JSON.stringify(record)}\n`, (error) => {
      if (error) rejectWrite(error)
      else resolveWrite()
    })
  })
}
