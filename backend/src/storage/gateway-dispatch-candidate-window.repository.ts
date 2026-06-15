import type { DatabaseSync } from 'node:sqlite'

import { getStatsDatabase } from './database.js'
import type {
  AccountAvailabilityScheduleCandidateFilter,
  EligibleOpenAIGroupAccountSelection,
  GroupUsageAccessMetadata,
  OpenAIAccountRow,
  OpenAIAccountsForGroupDiagnostics,
  OpenAIGroupAccountSelectionRow
} from './openai-account-selector.types.js'
import {
  gatewayDispatchAccountCandidateLimit,
  gatewayDispatchAccountCandidateScanLimit
} from './openai-account-selector.types.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

export function emptyGatewayDispatchCandidateDiagnostics(): OpenAIAccountsForGroupDiagnostics {
  return {
    scanLimit: gatewayDispatchAccountCandidateScanLimit,
    finalLimit: gatewayDispatchAccountCandidateLimit,
    withoutScheduleRowCount: 0,
    withScheduleRowCount: 0,
    scannedRowCount: 0,
    eligibleRowCount: 0,
    hydrationBatchCount: 0,
    hydratedAccountCount: 0,
    hydrationDroppedCount: 0,
    finalAccountCount: 0,
    scanLimitReached: false
  }
}

export function orderGatewayDispatchCandidateRowsForDispatch(items: EligibleOpenAIGroupAccountSelection[]): EligibleOpenAIGroupAccountSelection[] {
  const buckets = new Map<string, number>()
  for (const item of items) {
    const bucketKey = gatewayDispatchCandidateBucketKey(item.row)
    buckets.set(bucketKey, (buckets.get(bucketKey) ?? 0) + 1)
  }
  return [...items].sort((left, right) => {
    const leftFallback = gatewayDispatchCandidateFallbackRank(left.row)
    const rightFallback = gatewayDispatchCandidateFallbackRank(right.row)
    if (leftFallback !== rightFallback) return leftFallback - rightFallback
    const leftSuper = gatewayDispatchCandidateSuperRank(left.row)
    const rightSuper = gatewayDispatchCandidateSuperRank(right.row)
    if (leftSuper !== rightSuper) return rightSuper - leftSuper
    const leftPriority = gatewayDispatchCandidatePriority(left.row)
    const rightPriority = gatewayDispatchCandidatePriority(right.row)
    if (leftPriority !== rightPriority) return leftPriority - rightPriority
    const bucketKey = gatewayDispatchCandidateBucketKey(left.row)
    if (bucketKey === gatewayDispatchCandidateBucketKey(right.row) && (buckets.get(bucketKey) ?? 0) >= 2) {
      const qualityDelta = compareGatewayDispatchCandidateRowsByQuality(left.row, right.row)
      if (qualityDelta !== 0) return qualityDelta
    }
    const nameDelta = left.row.name.localeCompare(right.row.name, 'zh-CN')
    return nameDelta !== 0 ? nameDelta : left.row.id.localeCompare(right.row.id)
  })
}

function gatewayDispatchCandidateBucketKey(row: OpenAIGroupAccountSelectionRow): string {
  return `${gatewayDispatchCandidateFallbackRank(row)}:${gatewayDispatchCandidateSuperRank(row)}:${gatewayDispatchCandidatePriority(row)}`
}

function gatewayDispatchCandidateFallbackRank(row: OpenAIGroupAccountSelectionRow): number {
  return row.local_fallback_enabled === 1 ? 1 : 0
}

function gatewayDispatchCandidateSuperRank(row: OpenAIGroupAccountSelectionRow): number {
  return row.local_super_priority_enabled === 1 ? 1 : 0
}

function gatewayDispatchCandidatePriority(row: OpenAIGroupAccountSelectionRow): number {
  return Number(row.local_priority ?? row.priority ?? 0)
}

function compareGatewayDispatchCandidateRowsByQuality(left: OpenAIAccountRow, right: OpenAIAccountRow): number {
  const leftQuality = left.quality_score
  const rightQuality = right.quality_score
  const leftHasQuality = typeof leftQuality === 'number'
  const rightHasQuality = typeof rightQuality === 'number'
  if (leftHasQuality !== rightHasQuality) return leftHasQuality ? -1 : 1
  if (leftHasQuality && rightHasQuality && leftQuality !== rightQuality) {
    return leftQuality - rightQuality
  }
  const nameDelta = left.name.localeCompare(right.name, 'zh-CN')
  return nameDelta !== 0 ? nameDelta : left.id.localeCompare(right.id)
}

export function applyGatewayDispatchCandidateQualityRows(
  rows: EligibleOpenAIGroupAccountSelection[],
  qualityByAccountId: Map<string, Pick<OpenAIAccountRow, 'quality_score' | 'quality_state' | 'quality_ewma_first_token_ms'>>
): void {
  for (const { row } of rows) {
    const quality = qualityByAccountId.get(row.id)
    if (quality) {
      row.quality_score = quality.quality_score
      row.quality_state = quality.quality_state
      row.quality_ewma_first_token_ms = quality.quality_ewma_first_token_ms
    }
  }
}

export function listGatewayDispatchCandidateRows(
  database: DatabaseSync,
  groupId: string,
  groupAccess: GroupUsageAccessMetadata,
  now: string,
  scheduleFilter: AccountAvailabilityScheduleCandidateFilter
): OpenAIGroupAccountSelectionRow[] {
  const scheduleClause = scheduleFilter === 'with_schedule'
    ? 'AND (accounts.availability_schedule_json IS NOT NULL OR source_accounts.availability_schedule_json IS NOT NULL)'
    : 'AND accounts.availability_schedule_json IS NULL AND source_accounts.availability_schedule_json IS NULL'
  return database
    .prepare(`
      SELECT group_accounts.account_id, group_accounts.system_account_id AS binding_system_account_id, group_accounts.group_id, group_accounts.account_authorization_id,
        group_accounts.local_priority, group_accounts.local_super_priority_enabled, group_accounts.local_fallback_enabled,
        accounts.id, accounts.system_account_id, accounts.provider_code, accounts.provider_protocol_profile_id, accounts.protocol_code, accounts.protocol_version, accounts.name, accounts.type, accounts.status, accounts.schedulable, accounts.concurrency_limit, accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled, accounts.client_compatibility,
        accounts.credentials_encrypted, accounts.proxy_profile_id, accounts.cooldown_until, accounts.last_error_message, accounts.stream_failure_count, accounts.stream_failure_window_started_at,
        accounts.availability_schedule_json, accounts.account_expires_at, accounts.last_successful_test_model, accounts.authorization_instance_source_account_id, accounts.authorization_instance_authorization_id, accounts.authorization_instance_owner_system_account_id,
        source_accounts.id AS resource_account_id,
        source_accounts.provider_code AS resource_provider_code,
        source_accounts.provider_protocol_profile_id AS resource_provider_protocol_profile_id,
        source_accounts.protocol_code AS resource_protocol_code,
        source_accounts.protocol_version AS resource_protocol_version,
        source_accounts.type AS resource_type,
        source_accounts.status AS resource_status,
        source_accounts.schedulable AS resource_schedulable,
        source_accounts.availability_schedule_json AS resource_availability_schedule_json,
        source_accounts.account_expires_at AS resource_account_expires_at,
        source_accounts.cooldown_until AS resource_cooldown_until,
        source_accounts.last_error_code AS resource_last_error_code,
        source_accounts.credentials_encrypted AS resource_credentials_encrypted,
        source_accounts.proxy_profile_id AS resource_proxy_profile_id,
        source_accounts.concurrency_limit AS resource_concurrency_limit,
        source_accounts.client_compatibility AS resource_client_compatibility,
        NULL AS quality_score,
        NULL AS quality_state,
        NULL AS quality_ewma_first_token_ms
      FROM group_accounts INDEXED BY idx_group_accounts_dispatch_candidate_window
      INNER JOIN accounts ON accounts.id = group_accounts.account_id
      LEFT JOIN accounts source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
      WHERE group_accounts.group_id = ?
        AND group_accounts.system_account_id = ?
        AND group_accounts.enabled = 1
        AND accounts.provider_protocol_profile_id = ?
        AND accounts.deleted_at IS NULL
        AND accounts.status = 'active'
        AND accounts.schedulable = 1
        AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ?)
        ${scheduleClause}
        AND (
          (accounts.authorization_instance_authorization_id IS NULL AND accounts.type IN ('api_key', 'oauth'))
          OR (
            accounts.authorization_instance_authorization_id IS NOT NULL
            AND source_accounts.deleted_at IS NULL
            AND source_accounts.provider_protocol_profile_id = ?
            AND source_accounts.type IN ('api_key', 'oauth')
            AND source_accounts.status = 'active'
            AND source_accounts.schedulable = 1
            AND (source_accounts.cooldown_until IS NULL OR source_accounts.cooldown_until <= ?)
            AND (source_accounts.account_expires_at IS NULL OR source_accounts.account_expires_at > ?)
            AND (source_accounts.last_error_code IS NULL OR source_accounts.last_error_code <> 'account_expired')
          )
        )
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
      ORDER BY
        group_accounts.local_fallback_enabled ASC,
        group_accounts.local_super_priority_enabled DESC,
        group_accounts.local_priority ASC,
        group_accounts.created_at ASC,
        group_accounts.account_id ASC
      LIMIT ?
    `)
    .all(
      groupId,
      groupAccess.groupOwnerSystemAccountId,
      groupAccess.providerProtocolProfileId,
      now,
      groupAccess.providerProtocolProfileId,
      now,
      now,
      now,
      gatewayDispatchAccountCandidateScanLimit
    ) as unknown as OpenAIGroupAccountSelectionRow[]
}

export function gatewayDispatchCandidateQualityFreshAfterIso(): string {
  return new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString()
}

export function loadFreshGatewayDispatchCandidateQualityRows(
  accountIds: string[],
  freshAfter: string
): Map<string, Pick<OpenAIAccountRow, 'quality_score' | 'quality_state' | 'quality_ewma_first_token_ms'>> {
  const ids = [...new Set(accountIds.filter(Boolean))]
  if (!ids.length) return new Map()
  const rows: Array<{
    account_id: string
    quality_score: number | null
    quality_state: string | null
    quality_ewma_first_token_ms: number | null
  }> = []
  const database = getStatsDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...database
      .prepare(`
        SELECT account_id, quality_score, quality_state, ewma_first_token_ms AS quality_ewma_first_token_ms
        FROM account_quality_scores
        WHERE account_id IN (${sqlPlaceholders(chunk.length)})
          AND last_sample_at >= ?
      `)
      .all(...chunk, freshAfter) as unknown as Array<{
        account_id: string
        quality_score: number | null
        quality_state: string | null
        quality_ewma_first_token_ms: number | null
      }>)
  }
  return new Map(rows.map((row) => [row.account_id, row]))
}
