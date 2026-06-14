import { getBusinessDatabase } from '../../storage/database.js'
import type { UsageRecordSeed } from './mockdata-shared.js'

export function updateApiKeyLastUsedAt(records: UsageRecordSeed[]): void {
  const lastUsedByKey = new Map<string, string>()
  for (const record of records) {
    if (!record.apiKeyId) continue
    const previous = lastUsedByKey.get(record.apiKeyId)
    if (!previous || record.createdAt > previous) {
      lastUsedByKey.set(record.apiKeyId, record.createdAt)
    }
  }
  const statement = getBusinessDatabase().prepare('UPDATE api_keys SET last_used_at = ?, updated_at = ? WHERE id = ?')
  for (const [apiKeyId, lastUsedAt] of lastUsedByKey) {
    statement.run(lastUsedAt, lastUsedAt, apiKeyId)
  }
}
