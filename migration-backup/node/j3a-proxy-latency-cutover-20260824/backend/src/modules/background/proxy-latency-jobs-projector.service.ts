import { nowIso } from '../../storage/database.js'
import type { DatabaseClient } from '../../storage/database-client.js'
import type { ProxyLatencyJobsOutcome, ProxyLatencyJobsOutcomeItem } from '../../storage/proxy-latency-jobs-outcome.repository.js'

export type ProxyLatencyProjectionDisposition = 'applied' | 'stale' | 'ignored' | 'rejected'

export interface ProxyLatencyProjectionResult {
  outcomeId: string
  proxyId: string
  inputVersion: number
  disposition: ProxyLatencyProjectionDisposition
  changed: boolean
  reason?: string
}

interface ReceiptRow {
  outcome_id: string
  proxy_id: string
  input_version: number | string | bigint
  disposition: ProxyLatencyProjectionDisposition
  reason: string | null
}

interface ProxyFenceRow {
  id: string
  updated_at: string
  last_tested_at: string | null
}

export async function projectProxyLatencyJobsOutcomeAsync(client: DatabaseClient, outcome: ProxyLatencyJobsOutcome): Promise<ProxyLatencyProjectionResult> {
  const base = { outcomeId: outcome.outcomeId, proxyId: outcome.proxyId, inputVersion: outcome.inputVersion }
  return client.transaction(async (tx) => {
    const existing = await tx.one<ReceiptRow>(`SELECT outcome_id,proxy_id,input_version,disposition,reason FROM ${table(tx, 'proxy_latency_projection_receipts')} WHERE outcome_id=?`, [outcome.outcomeId])
    if (existing) return receiptResult(existing)

    const validation = validateOutcome(outcome)
    if (validation) {
      await insertReceipt(tx, base, 'rejected', validation)
      return { ...base, disposition: 'rejected', changed: false, reason: validation }
    }

    const proxy = await tx.one<ProxyFenceRow>(`SELECT id,${updatedAtSql(tx)} AS updated_at,${observedAtSql(tx)} AS last_tested_at FROM ${table(tx, 'proxy_profiles')} WHERE id=?${tx.driver === 'postgres' ? ' FOR UPDATE' : ''}`, [outcome.proxyId])
    if (!proxy) {
      await insertReceipt(tx, base, 'ignored', 'proxy_missing_or_deleted')
      return { ...base, disposition: 'ignored', changed: false, reason: 'proxy_missing_or_deleted' }
    }
    if (!sameInstant(proxy.updated_at, outcome.configRevision)) {
      await insertReceipt(tx, base, 'stale', 'config_revision_stale')
      return { ...base, disposition: 'stale', changed: false, reason: 'config_revision_stale' }
    }
    if (proxy.last_tested_at && preciseInstant(proxy.last_tested_at) > preciseInstant(outcome.observedAt)) {
      await insertReceipt(tx, base, 'stale', 'observed_at_stale')
      return { ...base, disposition: 'stale', changed: false, reason: 'observed_at_stale' }
    }

    const summary = summarize(outcome.items)
    const result = await tx.execute(`
      UPDATE ${table(tx, 'proxy_profiles')}
      SET test_status=?,latency_ms=?,last_test_message=?,last_tested_at=?
      WHERE id=? AND updated_at=? AND (last_tested_at IS NULL OR last_tested_at <= ?)
    `, [summary.status, summary.latencyMs, summary.message, outcome.observedAt, outcome.proxyId, outcome.configRevision, outcome.observedAt])
    if (result.changes !== 1) {
      await insertReceipt(tx, base, 'stale', 'projection_compare_and_set_missed')
      return { ...base, disposition: 'stale', changed: false, reason: 'projection_compare_and_set_missed' }
    }
    await insertReceipt(tx, base, 'applied', null)
    return { ...base, disposition: 'applied', changed: true }
  })
}

function validateOutcome(outcome: ProxyLatencyJobsOutcome): string | undefined {
  if (outcome.trigger !== 'periodic' && outcome.trigger !== 'manual') return 'trigger_not_allowed'
  if (!outcome.outcomeId || !outcome.proxyId || !outcome.requestId) return 'outcome_identity_missing'
  if (!Number.isSafeInteger(outcome.inputVersion) || outcome.inputVersion < 1) return 'input_version_invalid'
  if (!outcome.items.length) return 'outcome_items_missing'
  if (summarize(outcome.items).status !== outcome.overallStatus) return 'overall_status_mismatch'
  return undefined
}

function summarize(items: ProxyLatencyJobsOutcomeItem[]): { status: 'passed' | 'warning' | 'failed' | 'unknown'; latencyMs: number | null; message: string } {
  const base = baseItem(items)
  const all = [base, ...items]
  const passedCount = all.filter((item) => item.status === 'passed').length
  const warningCount = all.filter((item) => item.status === 'warning').length
  const failedCount = all.filter((item) => item.status === 'failed').length
  const unknownCount = all.filter((item) => item.status === 'unknown').length
  const status = failedCount > 0
    ? 'failed'
    : warningCount > 0 || (passedCount > 0 && unknownCount > 0)
      ? 'warning'
      : unknownCount > 0 || all.length === 0
        ? 'unknown'
        : 'passed'
  const message = status === 'passed'
    ? '代理质量检测通过'
    : status === 'warning'
      ? `代理可用，存在 ${warningCount} 项告警`
      : status === 'failed'
        ? `代理检测存在 ${failedCount} 项失败`
        : '代理检测未形成有效传输尝试'
  return { status, latencyMs: base.latencyMs ?? null, message }
}

function baseItem(items: ProxyLatencyJobsOutcomeItem[]): { status: 'passed' | 'warning' | 'failed' | 'unknown'; latencyMs?: number } {
  const failed = items.filter((item) => item.status === 'failed').length
  const unknown = items.filter((item) => item.status === 'unknown').length
  const reachable = items.filter((item) => item.status === 'passed').length
  const latencies = items.map((item) => item.latencyMs).filter((value): value is number => typeof value === 'number' && Number.isFinite(value))
  const average = latencies.length ? Math.round(latencies.reduce((sum, value) => sum + value, 0) / latencies.length) : undefined
  return {
    status: failed === 0 && unknown === 0 ? 'passed' : reachable > 0 ? 'warning' : failed > 0 ? 'failed' : 'unknown',
    ...(average === undefined ? {} : { latencyMs: average })
  }
}

function receiptResult(row: ReceiptRow): ProxyLatencyProjectionResult {
  return {
    outcomeId: row.outcome_id,
    proxyId: row.proxy_id,
    inputVersion: Number(row.input_version),
    disposition: row.disposition,
    changed: false,
    ...(row.reason ? { reason: row.reason } : {})
  }
}

async function insertReceipt(client: DatabaseClient, base: Pick<ProxyLatencyProjectionResult, 'outcomeId' | 'proxyId' | 'inputVersion'>, disposition: ProxyLatencyProjectionDisposition, reason: string | null): Promise<void> {
  await client.execute(`INSERT INTO ${table(client, 'proxy_latency_projection_receipts')}(outcome_id,proxy_id,input_version,disposition,reason,applied_at) VALUES(?,?,?,?,?,?)`, [base.outcomeId, base.proxyId, base.inputVersion, disposition, reason, nowIso()])
}

function table(client: DatabaseClient, name: string): string { return client.dialect.qualifyTable('juhe_business', name) }
function updatedAtSql(client: DatabaseClient): string { return client.driver === 'postgres' ? `to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')` : 'updated_at' }
function observedAtSql(client: DatabaseClient): string { return client.driver === 'postgres' ? `CASE WHEN last_tested_at IS NULL THEN NULL ELSE to_char(last_tested_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END` : 'last_tested_at' }
function sameInstant(left: string, right: string): boolean { return preciseInstant(left) === preciseInstant(right) }
function preciseInstant(value: string): bigint {
  const match = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/u.exec(value)
  const parsed = Date.parse(value)
  if (!match || !Number.isFinite(parsed)) throw new Error('J3a projection timestamp 无效')
  const seconds = BigInt(Math.trunc(parsed / 1000))
  const fraction = BigInt((match[2] ?? '').padEnd(9, '0'))
  return seconds * 1_000_000_000n + fraction
}
