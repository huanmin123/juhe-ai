import type { DatabaseSync } from 'node:sqlite'

import { getStatsDatabase, newId, nowIso } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../shared/rfc3339.js'

export type UsageRangeWindowRequestDomain = 'usage_scope'

export interface UsageRangeWindowRequestInput {
  domain: UsageRangeWindowRequestDomain
  systemAccountId: string
  scopeType: string
  scopeId?: string
  startDate: string
  endDate: string
}

export interface UsageRangeWindowRequestRow {
  id: string
  domain: UsageRangeWindowRequestDomain
  system_account_id: string
  scope_type: string
  scope_id: string
  start_date: string
  end_date: string
  status: 'pending' | 'processing' | 'completed' | 'failed'
}

const usageRangeWindowRequestTtlDays = 7
const usageRangeWindowRequestBatchLimit = 5

export function registerUsageRangeWindowRequest(input: UsageRangeWindowRequestInput, requestedAt = nowIso()): void {
  const normalized = normalizeUsageRangeWindowRequestInput(input)
  const canonicalRequestedAt = requiredRfc3339Instant(requestedAt, '用量范围窗口请求 requestedAt')
  const expiresAt = expiresAtFrom(canonicalRequestedAt)
  getStatsDatabase().prepare(`
    INSERT INTO usage_range_window_requests (
      id, domain, system_account_id, scope_type, scope_id, start_date, end_date,
      status, requested_count, last_requested_at, expires_at, created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', 1, ?, ?, ?, ?)
    ON CONFLICT(domain, system_account_id, scope_type, scope_id, start_date, end_date) DO UPDATE SET
      requested_count = usage_range_window_requests.requested_count + 1,
      last_requested_at = excluded.last_requested_at,
      expires_at = excluded.expires_at,
      status = CASE
        WHEN usage_range_window_requests.status = 'completed' THEN usage_range_window_requests.status
        ELSE 'pending'
      END,
      error_message = NULL,
      updated_at = excluded.updated_at
  `).run(
    newId('rngwin'),
    normalized.domain,
    normalized.systemAccountId,
    normalized.scopeType,
    normalized.scopeId,
    normalized.startDate,
    normalized.endDate,
    canonicalRequestedAt,
    expiresAt,
    canonicalRequestedAt,
    canonicalRequestedAt
  )
}

export async function registerUsageRangeWindowRequestAsync(client: DatabaseClient, input: UsageRangeWindowRequestInput, requestedAt = nowIso()): Promise<void> {
  const normalized = normalizeUsageRangeWindowRequestInput(input)
  const canonicalRequestedAt = requiredRfc3339Instant(requestedAt, '用量范围窗口请求 requestedAt')
  const expiresAt = expiresAtFrom(canonicalRequestedAt)
  await client.execute(`
    INSERT INTO juhe_stats.usage_range_window_requests (
      id, domain, system_account_id, scope_type, scope_id, start_date, end_date,
      status, requested_count, last_requested_at, expires_at, created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', 1, ?, ?, ?, ?)
    ON CONFLICT(domain, system_account_id, scope_type, scope_id, start_date, end_date) DO UPDATE SET
      requested_count = usage_range_window_requests.requested_count + 1,
      last_requested_at = excluded.last_requested_at,
      expires_at = excluded.expires_at,
      status = CASE
        WHEN usage_range_window_requests.status = 'completed' THEN usage_range_window_requests.status
        ELSE 'pending'
      END,
      error_message = NULL,
      updated_at = excluded.updated_at
  `, [
    newId('rngwin'),
    normalized.domain,
    normalized.systemAccountId,
    normalized.scopeType,
    normalized.scopeId,
    normalized.startDate,
    normalized.endDate,
    canonicalRequestedAt,
    expiresAt,
    canonicalRequestedAt,
    canonicalRequestedAt
  ])
}

export function listPendingUsageRangeWindowRequests(database: DatabaseSync, domain: UsageRangeWindowRequestDomain, limit = usageRangeWindowRequestBatchLimit): UsageRangeWindowRequestRow[] {
  return database.prepare(`
    SELECT id, domain, system_account_id, scope_type, scope_id, start_date, end_date, status
    FROM usage_range_window_requests
    WHERE domain = ?
      AND status IN ('pending', 'failed')
      AND expires_at > ?
    ORDER BY updated_at ASC, id ASC
    LIMIT ?
  `).all(domain, nowIso(), positiveLimit(limit)) as unknown as UsageRangeWindowRequestRow[]
}

export async function listPendingUsageRangeWindowRequestsAsync(client: DatabaseClient, domain: UsageRangeWindowRequestDomain, limit = usageRangeWindowRequestBatchLimit): Promise<UsageRangeWindowRequestRow[]> {
  return await client.query<UsageRangeWindowRequestRow>(`
    SELECT id, domain, system_account_id, scope_type, scope_id, start_date, end_date, status
    FROM juhe_stats.usage_range_window_requests
    WHERE domain = ?
      AND status IN ('pending', 'failed')
      AND expires_at > ?
    ORDER BY updated_at ASC, id ASC
    LIMIT ?
  `, [domain, nowIso(), positiveLimit(limit)])
}

export function markUsageRangeWindowRequestProcessing(database: DatabaseSync, id: string, updatedAt = nowIso()): void {
  database.prepare(`
    UPDATE usage_range_window_requests
    SET status = 'processing', updated_at = ?
    WHERE id = ?
  `).run(updatedAt, id)
}

export async function markUsageRangeWindowRequestProcessingAsync(client: DatabaseClient, id: string, updatedAt = nowIso()): Promise<void> {
  await client.execute(`
    UPDATE juhe_stats.usage_range_window_requests
    SET status = 'processing', updated_at = ?
    WHERE id = ?
  `, [updatedAt, id])
}

export function markUsageRangeWindowRequestCompleted(database: DatabaseSync, id: string, updatedAt = nowIso()): void {
  database.prepare(`
    UPDATE usage_range_window_requests
    SET status = 'completed',
        last_processed_at = ?,
        error_message = NULL,
        updated_at = ?
    WHERE id = ?
  `).run(updatedAt, updatedAt, id)
}

export async function markUsageRangeWindowRequestCompletedAsync(client: DatabaseClient, id: string, updatedAt = nowIso()): Promise<void> {
  await client.execute(`
    UPDATE juhe_stats.usage_range_window_requests
    SET status = 'completed',
        last_processed_at = ?,
        error_message = NULL,
        updated_at = ?
    WHERE id = ?
  `, [updatedAt, updatedAt, id])
}

export function markUsageRangeWindowRequestFailed(database: DatabaseSync, id: string, message: string, updatedAt = nowIso()): void {
  database.prepare(`
    UPDATE usage_range_window_requests
    SET status = 'failed',
        error_message = ?,
        updated_at = ?
    WHERE id = ?
  `).run(message.slice(0, 500), updatedAt, id)
}

export async function markUsageRangeWindowRequestFailedAsync(client: DatabaseClient, id: string, message: string, updatedAt = nowIso()): Promise<void> {
  await client.execute(`
    UPDATE juhe_stats.usage_range_window_requests
    SET status = 'failed',
        error_message = ?,
        updated_at = ?
    WHERE id = ?
  `, [message.slice(0, 500), updatedAt, id])
}

function normalizeUsageRangeWindowRequestInput(input: UsageRangeWindowRequestInput): Required<UsageRangeWindowRequestInput> {
  return {
    domain: input.domain,
    systemAccountId: requiredText(input.systemAccountId, 'systemAccountId'),
    scopeType: requiredText(input.scopeType, 'scopeType'),
    scopeId: input.scopeId?.trim() || '*',
    startDate: requiredDate(input.startDate, 'startDate'),
    endDate: requiredDate(input.endDate, 'endDate')
  }
}

function expiresAtFrom(requestedAt: string): string {
  const baseTime = rfc3339InstantMilliseconds(requestedAt)
  if (baseTime === undefined) {
    throw new Error(`用量范围窗口请求 requestedAt 必须是带 Z 或数值 offset 的 RFC3339 时间：${requestedAt}`)
  }
  return new Date(baseTime + usageRangeWindowRequestTtlDays * 24 * 60 * 60 * 1000).toISOString()
}

function requiredText(value: string, label: string): string {
  const normalized = value.trim()
  if (!normalized) throw new Error(`用量范围窗口请求 ${label} 不能为空`)
  return normalized
}

function requiredDate(value: string, label: string): string {
  const normalized = requiredText(value, label)
  if (!/^\d{4}-\d{2}-\d{2}$/.test(normalized)) {
    throw new Error(`用量范围窗口请求 ${label} 必须是 YYYY-MM-DD`)
  }
  return normalized
}

function positiveLimit(value: number): number {
  return Number.isFinite(value) ? Math.max(1, Math.min(100, Math.trunc(value))) : usageRangeWindowRequestBatchLimit
}
