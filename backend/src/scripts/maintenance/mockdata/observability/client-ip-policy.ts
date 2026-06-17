import { getStatsDatabase, nowIso } from '../../../../storage/database.js'
import { recordClientIpPolicyHits } from '../../../../storage/client-ip-stats.repository.js'
import {
  dayMs,
  idPrefix,
  minuteMs,
  namePrefix,
  type ClientIpPolicyMockdataCounts,
  type CreatedMockdata
} from '../shared.js'

export function createClientIpPolicyMockdata(created: CreatedMockdata): ClientIpPolicyMockdataCounts {
  const database = getStatsDatabase()
  const rows = database.prepare(`
    SELECT ip_hash, client_ip, aggregate_ip_key, last_seen_at
    FROM client_ip_registry
    WHERE client_ip LIKE '10.10.%'
       OR client_ip LIKE '10.20.%'
    ORDER BY last_seen_at DESC, ip_hash ASC
    LIMIT 8
  `).all() as Array<{ ip_hash: string; client_ip: string; aggregate_ip_key: string; last_seen_at?: string | null }>
  if (!rows.length) {
    return { policies: 0, policyHits: 0 }
  }
  const now = nowIso()
  const insertPolicy = database.prepare(`
    INSERT INTO client_ip_policies (
      id, ip_hash, status, reason, expires_at, created_by_system_account_id,
      created_at, updated_at, disabled_at, disabled_by_system_account_id, disabled_reason
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  database.exec('BEGIN')
  try {
    rows.forEach((row, index) => {
      const status = index % 4 === 3 ? 'disabled' : 'active'
      const createdAt = new Date(Date.now() - (index + 1) * 6 * 60 * minuteMs).toISOString()
      insertPolicy.run(
        `${idPrefix}client_ip_policy_${String(index + 1).padStart(2, '0')}`,
        row.ip_hash,
        status,
        index % 3 === 0 ? `${namePrefix}高错误率自动封禁样例` : `${namePrefix}公益接口异常流量观察`,
        status === 'active' && index % 2 === 0 ? new Date(Date.now() + (index + 1) * dayMs).toISOString() : null,
        created.users.admin.id,
        createdAt,
        now,
        status === 'disabled' ? new Date(Date.now() - index * 60 * minuteMs).toISOString() : null,
        status === 'disabled' ? created.users.admin.id : null,
        status === 'disabled' ? `${namePrefix}人工解除封禁样例` : null
      )
    })
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }

  const hits = rows.flatMap((row, index) => {
    const policyId = `${idPrefix}client_ip_policy_${String(index + 1).padStart(2, '0')}`
    return Array.from({ length: Math.min(14, Math.max(4, Math.floor(index + 4))) }, (_, dayIndex) => ({
      ipHash: row.ip_hash,
      policyId,
      hitCount: 1 + ((index + dayIndex) % 9),
      hitAt: new Date(Date.now() - dayIndex * dayMs - index * 20 * minuteMs).toISOString()
    }))
  })
  const recorded = recordClientIpPolicyHits(hits).recorded
  return {
    policies: rows.length,
    policyHits: recorded
  }
}
