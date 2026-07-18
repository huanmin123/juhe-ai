import { runtimeConfig } from '../config/runtime.js'
import type { PageDataDomain } from '../modules/page-data/page-data-change.service.js'
import { getBusinessDatabase, nowIso } from './database.js'
import { getPostgresPool } from './postgres-client.js'

export interface PageDataDirtyDomainRow {
  domain: string
  generation: number
}

export async function listPageDataDirtyDomains(): Promise<PageDataDirtyDomainRow[]> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    const result = await (await getPostgresPool()).query(`
      SELECT domain, generation
      FROM juhe_business.page_data_dirty_domains
      WHERE is_dirty = TRUE
      ORDER BY domain ASC
    `)
    return result.rows.flatMap(toDirtyRow)
  }
  const rows = getBusinessDatabase().prepare(`
    SELECT domain, generation
    FROM page_data_dirty_domains
    WHERE is_dirty = 1
    ORDER BY domain ASC
  `).all() as unknown as Array<{ domain?: unknown; generation?: unknown }>
  return rows.flatMap(toDirtyRow)
}

export async function markPageDataDomainDirty(domain: PageDataDomain): Promise<number> {
  const updatedAt = nowIso()
  if (runtimeConfig.databaseDriver === 'postgres') {
    const result = await (await getPostgresPool()).query(`
      INSERT INTO juhe_business.page_data_dirty_domains(domain, generation, is_dirty, updated_at)
      VALUES ($1, 1, TRUE, $2)
      ON CONFLICT(domain) DO UPDATE SET
        generation = page_data_dirty_domains.generation + 1,
        is_dirty = TRUE,
        updated_at = EXCLUDED.updated_at
      RETURNING generation
    `, [domain, updatedAt])
    return requiredGeneration(result.rows[0]?.generation)
  }
  const row = getBusinessDatabase().prepare(`
    INSERT INTO page_data_dirty_domains(domain, generation, is_dirty, updated_at)
    VALUES (?, 1, 1, ?)
    ON CONFLICT(domain) DO UPDATE SET
      generation = page_data_dirty_domains.generation + 1,
      is_dirty = 1,
      updated_at = excluded.updated_at
    RETURNING generation
  `).get(domain, updatedAt) as unknown as { generation?: unknown } | undefined
  return requiredGeneration(row?.generation)
}

export async function clearPageDataDomainDirty(domain: PageDataDomain, generation: number): Promise<boolean> {
  const updatedAt = nowIso()
  if (runtimeConfig.databaseDriver === 'postgres') {
    const result = await (await getPostgresPool()).query(`
      UPDATE juhe_business.page_data_dirty_domains
      SET is_dirty = FALSE, updated_at = $3
      WHERE domain = $1 AND generation = $2 AND is_dirty = TRUE
    `, [domain, generation, updatedAt])
    return result.rowCount === 1
  }
  return getBusinessDatabase().prepare(`
    UPDATE page_data_dirty_domains
    SET is_dirty = 0, updated_at = ?
    WHERE domain = ? AND generation = ? AND is_dirty = 1
  `).run(updatedAt, domain, generation).changes === 1
}

function toDirtyRow(row: Record<string, unknown>): PageDataDirtyDomainRow[] {
  if (typeof row.domain !== 'string' || !row.domain.trim()) return []
  const generation = Number(row.generation)
  return Number.isSafeInteger(generation) && generation > 0
    ? [{ domain: row.domain.trim(), generation }]
    : []
}

function requiredGeneration(value: unknown): number {
  const generation = Number(value)
  if (!Number.isSafeInteger(generation) || generation < 1) throw new Error('页面数据 dirty domain 代际写入失败')
  return generation
}
