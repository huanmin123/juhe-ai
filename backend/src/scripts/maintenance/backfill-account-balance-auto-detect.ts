import { autoDetectAccountBalanceCandidate } from '../../modules/background/account-balance-auto-detect.service.js'
import { runtimeConfig } from '../../config/runtime.js'
import { closeStorageDatabases, getBusinessDatabase } from '../../storage/database.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { listAccountBalanceDetectionCandidatePageAsync } from '../../storage/account-balance.repository.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'

const pageSize = 50
const concurrency = 2

let afterId: string | undefined
let scanned = 0
let enabled = 0
let unsupported = 0
let stale = 0

try {
  const migrated = await migrateLegacyAccountBalanceConfigurations()
  process.stdout.write(`${JSON.stringify({ event: 'account_balance_backfill_config_migrated', migrated })}\n`)
  while (true) {
    const page = await listAccountBalanceDetectionCandidatePageAsync({ afterId, limit: pageSize })
    if (!page.nextAfterId) break
    afterId = page.nextAfterId
    let cursor = 0
    await Promise.all(Array.from({ length: Math.min(concurrency, page.candidates.length) }, async () => {
      while (cursor < page.candidates.length) {
        const candidate = page.candidates[cursor]
        cursor += 1
        const result = await autoDetectAccountBalanceCandidate(candidate)
        scanned += 1
        if (result === 'enabled') enabled += 1
        else if (result === 'unsupported') unsupported += 1
        else stale += 1
      }
    }))
    process.stdout.write(`${JSON.stringify({ event: 'account_balance_backfill_progress', afterId, scanned, enabled, unsupported, stale })}\n`)
  }
  process.stdout.write(`${JSON.stringify({ event: 'account_balance_backfill_completed', scanned, enabled, unsupported, stale })}\n`)
} finally {
  closeStorageDatabases()
  await closePostgresPool()
}

async function migrateLegacyAccountBalanceConfigurations(): Promise<number> {
  const legacyAdapters = new Set(['sub2api', 'newapi', 'litellm', 'user_balance'])
  const limit = 100
  const postgresClient = runtimeConfig.databaseDriver === 'postgres'
    ? createPostgresDatabaseClient(await getPostgresPool())
    : undefined
  let afterId = ''
  let migrated = 0
  while (true) {
    const rows = postgresClient
      ? await postgresClient.query<{ id: string; balance_query_config_json: string }>(`
          SELECT id, balance_query_config_json
          FROM juhe_business.accounts
          WHERE id > ? AND deleted_at IS NULL
          ORDER BY id ASC
          LIMIT ?
        `, [afterId, limit])
      : getBusinessDatabase().prepare(`
          SELECT id, balance_query_config_json
          FROM accounts
          WHERE id > ? AND deleted_at IS NULL
          ORDER BY id ASC
          LIMIT ?
        `).all(afterId, limit) as unknown as Array<{ id: string; balance_query_config_json: string }>
    if (rows.length === 0) break
    afterId = rows.at(-1)?.id ?? afterId
    for (const row of rows) {
      let current: Record<string, unknown>
      try {
        current = JSON.parse(row.balance_query_config_json) as Record<string, unknown>
      } catch {
        continue
      }
      const adapter = typeof current.adapter === 'string' ? current.adapter : ''
      if (!legacyAdapters.has(adapter)) continue
      const intervalMinutes = Number.isInteger(current.intervalMinutes) ? Number(current.intervalMinutes) : 5
      const nextJson = JSON.stringify({ adapter: 'builtin', intervalMinutes, preferredBuiltinAdapter: adapter })
      const changes = postgresClient
        ? await postgresClient.execute(`
            UPDATE juhe_business.accounts
            SET balance_query_config_json = ?, updated_at = now()
            WHERE id = ? AND balance_query_config_json = ?
          `, [nextJson, row.id, row.balance_query_config_json])
        : getBusinessDatabase().prepare(`
            UPDATE accounts
            SET balance_query_config_json = ?, updated_at = ?
            WHERE id = ? AND balance_query_config_json = ?
          `).run(nextJson, new Date().toISOString(), row.id, row.balance_query_config_json)
      migrated += Number(changes.changes ?? 0)
    }
  }
  return migrated
}
