import { existsSync } from 'node:fs'
import type { DatabaseSync } from 'node:sqlite'

import { getStatsDatabase, statsDatabasePath } from './database.js'
import type { DatabaseClient } from './database-client.js'
import type {
  EligibleOpenAIGroupAccountSelection,
  GroupUsageAccessMetadata,
  OpenAIAccountRow,
  OpenAIAccountsForGroupDiagnostics,
  OpenAIGroupAccountSelectionRow
} from './openai-account-selector.types.js'
import type { GatewayRequestEndpointFamily } from '../domain/types.js'
import {
  gatewayDispatchAccountCandidateLimit,
  gatewayDispatchAccountCandidateScanLimit
} from './openai-account-selector.types.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

const businessSchemaName = 'juhe_business'
const statsSchemaName = 'juhe_stats'

export interface GatewayDispatchCandidateOrderOptions {
  modelRankByAccountId?: ReadonlyMap<string, number>
}

export interface GatewayDispatchCandidateWindowOptions {
  includeUnavailable?: boolean
}

export interface GatewayDispatchModelCandidateRowsResult {
  rows: OpenAIGroupAccountSelectionRow[]
  modelRankByAccountId: Map<string, number>
}

export function emptyGatewayDispatchCandidateDiagnostics(): OpenAIAccountsForGroupDiagnostics {
  return {
    scanLimit: gatewayDispatchAccountCandidateScanLimit,
    finalLimit: gatewayDispatchAccountCandidateLimit,
    candidateRowCount: 0,
    scannedRowCount: 0,
    eligibleRowCount: 0,
    hydrationBatchCount: 0,
    hydratedAccountCount: 0,
    hydrationDroppedCount: 0,
    finalAccountCount: 0,
    scanLimitReached: false
  }
}

export function orderGatewayDispatchCandidateRowsForDispatch(
  items: EligibleOpenAIGroupAccountSelection[],
  options: GatewayDispatchCandidateOrderOptions = {}
): EligibleOpenAIGroupAccountSelection[] {
  const buckets = new Map<string, number>()
  for (const item of items) {
    const bucketKey = gatewayDispatchCandidateBucketKey(item.row)
    buckets.set(bucketKey, (buckets.get(bucketKey) ?? 0) + 1)
  }
  return [...items].sort((left, right) => {
    const leftModelRank = gatewayDispatchCandidateModelRank(left.row, options.modelRankByAccountId)
    const rightModelRank = gatewayDispatchCandidateModelRank(right.row, options.modelRankByAccountId)
    if (leftModelRank !== rightModelRank) return leftModelRank - rightModelRank
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

function gatewayDispatchCandidateModelRank(
  row: OpenAIGroupAccountSelectionRow,
  modelRankByAccountId?: ReadonlyMap<string, number>
): number {
  if (!modelRankByAccountId) return 0
  return modelRankByAccountId.get(row.id) ?? modelRankByAccountId.get(row.account_id) ?? 3
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
  options: GatewayDispatchCandidateWindowOptions = {}
): OpenAIGroupAccountSelectionRow[] {
  const includeUnavailable = options.includeUnavailable === true
  const statusSetSql = includeUnavailable
    ? "'active', 'rate_limited', 'temporary_unavailable'"
    : "'active'"
  const includeUnavailableFlag = includeUnavailable ? 1 : 0
  return database
    .prepare(`
      SELECT group_accounts.account_id, group_accounts.system_account_id AS binding_system_account_id, group_accounts.group_id, group_accounts.account_authorization_id,
        group_accounts.local_priority, group_accounts.local_super_priority_enabled, group_accounts.local_fallback_enabled,
        accounts.id, accounts.system_account_id, accounts.provider_code, accounts.provider_protocol_profile_id, accounts.protocol_code, accounts.protocol_version, accounts.name, accounts.type, accounts.status, accounts.schedulable, accounts.concurrency_limit, accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled, accounts.client_compatibility,
        accounts.config_revision, accounts.dispatch_revision, accounts.credentials_encrypted, accounts.proxy_profile_id, accounts.cooldown_until, accounts.last_error_message, accounts.stream_failure_count, accounts.stream_failure_window_started_at,
        accounts.account_expires_at, accounts.health_check_model, accounts.health_check_endpoint_mode, accounts.authorization_instance_source_account_id, accounts.authorization_instance_authorization_id, accounts.authorization_instance_owner_system_account_id,
        source_accounts.id AS resource_account_id,
        source_accounts.provider_code AS resource_provider_code,
        source_accounts.provider_protocol_profile_id AS resource_provider_protocol_profile_id,
        source_accounts.protocol_code AS resource_protocol_code,
        source_accounts.protocol_version AS resource_protocol_version,
        source_accounts.type AS resource_type,
        source_accounts.status AS resource_status,
        source_accounts.schedulable AS resource_schedulable,
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
        AND accounts.provider_code = ?
        AND accounts.deleted_at IS NULL
        AND accounts.status IN (${statusSetSql})
        AND accounts.schedulable = 1
        AND (? = 1 OR accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ?)
        AND (
          (accounts.authorization_instance_authorization_id IS NULL AND accounts.type IN ('api_key', 'oauth', 'google_oauth'))
          OR (
            accounts.authorization_instance_authorization_id IS NOT NULL
            AND source_accounts.deleted_at IS NULL
            AND source_accounts.provider_code = ?
            AND source_accounts.type IN ('api_key', 'oauth', 'google_oauth')
            AND source_accounts.status IN (${statusSetSql})
            AND source_accounts.schedulable = 1
            AND (? = 1 OR source_accounts.cooldown_until IS NULL OR source_accounts.cooldown_until <= ?)
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
      groupAccess.providerCode,
      includeUnavailableFlag,
      now,
      groupAccess.providerCode,
      includeUnavailableFlag,
      now,
      now,
      now,
      gatewayDispatchAccountCandidateScanLimit
    ) as unknown as OpenAIGroupAccountSelectionRow[]
}

export async function listGatewayDispatchCandidateRowsAsync(
  client: DatabaseClient,
  groupId: string,
  groupAccess: GroupUsageAccessMetadata,
  now: string,
  options: GatewayDispatchCandidateWindowOptions = {}
): Promise<OpenAIGroupAccountSelectionRow[]> {
  if (client.driver !== 'postgres') {
    throw new Error('异步候选账号窗口当前只用于 PostgreSQL 模式')
  }
  const includeUnavailable = options.includeUnavailable === true
  const statusSetSql = includeUnavailable
    ? "'active', 'rate_limited', 'temporary_unavailable'"
    : "'active'"
  const includeUnavailableFlag = includeUnavailable ? 1 : 0
  const tables = gatewayDispatchCandidateBusinessTables(client)
  return await client.query<OpenAIGroupAccountSelectionRow>(`
    SELECT group_accounts.account_id, group_accounts.system_account_id AS binding_system_account_id, group_accounts.group_id, group_accounts.account_authorization_id,
      group_accounts.local_priority, group_accounts.local_super_priority_enabled, group_accounts.local_fallback_enabled,
      accounts.id, accounts.system_account_id, accounts.provider_code, accounts.provider_protocol_profile_id, accounts.protocol_code, accounts.protocol_version, accounts.name, accounts.type, accounts.status, accounts.schedulable, accounts.concurrency_limit, accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled, accounts.client_compatibility,
      accounts.config_revision, accounts.dispatch_revision, accounts.credentials_encrypted, accounts.proxy_profile_id, accounts.cooldown_until, accounts.last_error_message, accounts.stream_failure_count, accounts.stream_failure_window_started_at,
      accounts.account_expires_at, accounts.health_check_model, accounts.health_check_endpoint_mode, accounts.authorization_instance_source_account_id, accounts.authorization_instance_authorization_id, accounts.authorization_instance_owner_system_account_id,
      source_accounts.id AS resource_account_id,
      source_accounts.provider_code AS resource_provider_code,
      source_accounts.provider_protocol_profile_id AS resource_provider_protocol_profile_id,
      source_accounts.protocol_code AS resource_protocol_code,
      source_accounts.protocol_version AS resource_protocol_version,
      source_accounts.type AS resource_type,
      source_accounts.status AS resource_status,
      source_accounts.schedulable AS resource_schedulable,
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
    FROM ${tables.groupAccounts} AS group_accounts
    INNER JOIN ${tables.accounts} AS accounts ON accounts.id = group_accounts.account_id
    LEFT JOIN ${tables.accounts} AS source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
    WHERE group_accounts.group_id = ?
      AND group_accounts.system_account_id = ?
      AND group_accounts.enabled = 1
      AND accounts.provider_code = ?
      AND accounts.deleted_at IS NULL
      AND accounts.status IN (${statusSetSql})
      AND accounts.schedulable = 1
      AND (? = 1 OR accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ?)
      AND (
        (accounts.authorization_instance_authorization_id IS NULL AND accounts.type IN ('api_key', 'oauth', 'google_oauth'))
        OR (
          accounts.authorization_instance_authorization_id IS NOT NULL
          AND source_accounts.deleted_at IS NULL
          AND source_accounts.provider_code = ?
          AND source_accounts.type IN ('api_key', 'oauth', 'google_oauth')
          AND source_accounts.status IN (${statusSetSql})
          AND source_accounts.schedulable = 1
          AND (? = 1 OR source_accounts.cooldown_until IS NULL OR source_accounts.cooldown_until <= ?)
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
  `, [
    groupId,
    groupAccess.groupOwnerSystemAccountId,
    groupAccess.providerCode,
    includeUnavailableFlag,
    now,
    groupAccess.providerCode,
    includeUnavailableFlag,
    now,
    now,
    now,
    gatewayDispatchAccountCandidateScanLimit
  ])
}

export function listGatewayDispatchModelCandidateRows(
  database: DatabaseSync,
  groupId: string,
  groupAccess: GroupUsageAccessMetadata,
  now: string,
  requestedModel: string,
  requestedEndpointFamily?: GatewayRequestEndpointFamily,
  options: GatewayDispatchCandidateWindowOptions = {}
): GatewayDispatchModelCandidateRowsResult {
  const model = requestedModel.trim()
  if (!model) {
    return { rows: [], modelRankByAccountId: new Map() }
  }
  const includeUnavailable = options.includeUnavailable === true
  const statusSetSql = includeUnavailable
    ? "'active', 'rate_limited', 'temporary_unavailable'"
    : "'active'"
  const includeUnavailableFlag = includeUnavailable ? 1 : 0
  const rows = database
    .prepare(`
      WITH eligible_rows AS (
        SELECT group_accounts.account_id, group_accounts.system_account_id AS binding_system_account_id, group_accounts.group_id, group_accounts.account_authorization_id,
          group_accounts.local_priority, group_accounts.local_super_priority_enabled, group_accounts.local_fallback_enabled,
          group_accounts.created_at AS binding_created_at,
          accounts.id, accounts.system_account_id, accounts.provider_code, accounts.provider_protocol_profile_id, accounts.protocol_code, accounts.protocol_version, accounts.name, accounts.type, accounts.status, accounts.schedulable, accounts.concurrency_limit, accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled, accounts.client_compatibility,
          accounts.config_revision, accounts.dispatch_revision, accounts.credentials_encrypted, accounts.proxy_profile_id, accounts.cooldown_until, accounts.last_error_message, accounts.stream_failure_count, accounts.stream_failure_window_started_at,
          accounts.account_expires_at, accounts.health_check_model, accounts.health_check_endpoint_mode, accounts.authorization_instance_source_account_id, accounts.authorization_instance_authorization_id, accounts.authorization_instance_owner_system_account_id,
          source_accounts.id AS resource_account_id,
          source_accounts.provider_code AS resource_provider_code,
          source_accounts.provider_protocol_profile_id AS resource_provider_protocol_profile_id,
          source_accounts.protocol_code AS resource_protocol_code,
          source_accounts.protocol_version AS resource_protocol_version,
          source_accounts.type AS resource_type,
          source_accounts.status AS resource_status,
          source_accounts.schedulable AS resource_schedulable,
          source_accounts.account_expires_at AS resource_account_expires_at,
          source_accounts.cooldown_until AS resource_cooldown_until,
          source_accounts.last_error_code AS resource_last_error_code,
          source_accounts.credentials_encrypted AS resource_credentials_encrypted,
          source_accounts.proxy_profile_id AS resource_proxy_profile_id,
          source_accounts.concurrency_limit AS resource_concurrency_limit,
          source_accounts.client_compatibility AS resource_client_compatibility,
          COALESCE(source_accounts.id, accounts.id) AS model_resource_account_id,
          COALESCE(source_accounts.provider_code, accounts.provider_code) AS model_resource_provider_code,
          NULL AS quality_score,
          NULL AS quality_state,
          NULL AS quality_ewma_first_token_ms
        FROM group_accounts INDEXED BY idx_group_accounts_dispatch_candidate_window
        INNER JOIN accounts ON accounts.id = group_accounts.account_id
        LEFT JOIN accounts source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
        WHERE group_accounts.group_id = ?
          AND group_accounts.system_account_id = ?
          AND group_accounts.enabled = 1
          AND accounts.provider_code = ?
          AND accounts.deleted_at IS NULL
          AND accounts.status IN (${statusSetSql})
          AND accounts.schedulable = 1
          AND (? = 1 OR accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ?)
          AND (
            (accounts.authorization_instance_authorization_id IS NULL AND accounts.type IN ('api_key', 'oauth', 'google_oauth'))
            OR (
              accounts.authorization_instance_authorization_id IS NOT NULL
              AND source_accounts.deleted_at IS NULL
              AND source_accounts.provider_code = ?
              AND source_accounts.type IN ('api_key', 'oauth', 'google_oauth')
              AND source_accounts.status IN (${statusSetSql})
              AND source_accounts.schedulable = 1
              AND (? = 1 OR source_accounts.cooldown_until IS NULL OR source_accounts.cooldown_until <= ?)
              AND (source_accounts.account_expires_at IS NULL OR source_accounts.account_expires_at > ?)
              AND (source_accounts.last_error_code IS NULL OR source_accounts.last_error_code <> 'account_expired')
            )
          )
          AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
      ),
      ranked_rows AS (
        SELECT
          CASE
            WHEN EXISTS (
              SELECT 1
              FROM account_supported_models direct_models
              WHERE direct_models.account_id = eligible_rows.model_resource_account_id
                AND direct_models.provider_code = eligible_rows.model_resource_provider_code
                AND direct_models.model = ?
            ) THEN 0
            WHEN EXISTS (
              SELECT 1
              FROM account_model_mappings model_mappings
              WHERE model_mappings.account_id = eligible_rows.model_resource_account_id
                AND model_mappings.provider_code = eligible_rows.model_resource_provider_code
                AND model_mappings.source_model = ?
                AND model_mappings.source_endpoint_family = ?
                AND model_mappings.enabled = 1
                AND (
                  model_mappings.upstream_model <> model_mappings.source_model
                  OR model_mappings.upstream_endpoint_family <> model_mappings.source_endpoint_family
                )
                AND (
                  NOT EXISTS (
                    SELECT 1
                    FROM account_supported_models limited_supported
                    WHERE limited_supported.account_id = eligible_rows.model_resource_account_id
                  )
                  OR EXISTS (
                    SELECT 1
                    FROM account_supported_models mapped_supported
                    WHERE mapped_supported.account_id = eligible_rows.model_resource_account_id
                      AND mapped_supported.provider_code = eligible_rows.model_resource_provider_code
                      AND mapped_supported.model = model_mappings.upstream_model
                  )
                )
            ) THEN 1
            WHEN NOT EXISTS (
              SELECT 1
              FROM account_supported_models limited_models
              WHERE limited_models.account_id = eligible_rows.model_resource_account_id
            ) THEN 2
            ELSE 3
          END AS model_rank,
          eligible_rows.*
        FROM eligible_rows
      )
      SELECT *
      FROM ranked_rows
      WHERE model_rank < 3
      ORDER BY
        model_rank ASC,
        local_fallback_enabled ASC,
        local_super_priority_enabled DESC,
        COALESCE(local_priority, priority, 0) ASC,
        binding_created_at ASC,
        account_id ASC
      LIMIT ?
    `)
    .all(
      groupId,
      groupAccess.groupOwnerSystemAccountId,
      groupAccess.providerCode,
      includeUnavailableFlag,
      now,
      groupAccess.providerCode,
      includeUnavailableFlag,
      now,
      now,
      now,
      model,
      model,
      requestedEndpointFamily ?? null,
      gatewayDispatchAccountCandidateScanLimit
    ) as unknown as Array<OpenAIGroupAccountSelectionRow & { model_rank?: number }>
  const modelRankByAccountId = new Map<string, number>()
  const seenAccountIds = new Set<string>()
  const uniqueRows: OpenAIGroupAccountSelectionRow[] = []
  for (const row of rows) {
    const accountId = row.account_id || row.id
    if (seenAccountIds.has(accountId)) {
      continue
    }
    seenAccountIds.add(accountId)
    const modelRank = Number(row.model_rank)
    if (Number.isFinite(modelRank)) {
      modelRankByAccountId.set(row.id, modelRank)
      modelRankByAccountId.set(accountId, modelRank)
    }
    uniqueRows.push(row)
  }
  return { rows: uniqueRows, modelRankByAccountId }
}

export async function listGatewayDispatchModelCandidateRowsAsync(
  client: DatabaseClient,
  groupId: string,
  groupAccess: GroupUsageAccessMetadata,
  now: string,
  requestedModel: string,
  requestedEndpointFamily?: GatewayRequestEndpointFamily,
  options: GatewayDispatchCandidateWindowOptions = {}
): Promise<GatewayDispatchModelCandidateRowsResult> {
  if (client.driver !== 'postgres') {
    throw new Error('异步模型候选账号窗口当前只用于 PostgreSQL 模式')
  }
  const model = requestedModel.trim()
  if (!model) {
    return { rows: [], modelRankByAccountId: new Map() }
  }
  const includeUnavailable = options.includeUnavailable === true
  const statusSetSql = includeUnavailable
    ? "'active', 'rate_limited', 'temporary_unavailable'"
    : "'active'"
  const includeUnavailableFlag = includeUnavailable ? 1 : 0
  const tables = gatewayDispatchCandidateBusinessTables(client)
  const rows = await client.query<OpenAIGroupAccountSelectionRow & { model_rank?: number }>(`
    WITH eligible_rows AS (
      SELECT group_accounts.account_id, group_accounts.system_account_id AS binding_system_account_id, group_accounts.group_id, group_accounts.account_authorization_id,
        group_accounts.local_priority, group_accounts.local_super_priority_enabled, group_accounts.local_fallback_enabled,
        group_accounts.created_at AS binding_created_at,
        accounts.id, accounts.system_account_id, accounts.provider_code, accounts.provider_protocol_profile_id, accounts.protocol_code, accounts.protocol_version, accounts.name, accounts.type, accounts.status, accounts.schedulable, accounts.concurrency_limit, accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled, accounts.client_compatibility,
        accounts.config_revision, accounts.dispatch_revision, accounts.credentials_encrypted, accounts.proxy_profile_id, accounts.cooldown_until, accounts.last_error_message, accounts.stream_failure_count, accounts.stream_failure_window_started_at,
        accounts.account_expires_at, accounts.health_check_model, accounts.health_check_endpoint_mode, accounts.authorization_instance_source_account_id, accounts.authorization_instance_authorization_id, accounts.authorization_instance_owner_system_account_id,
        source_accounts.id AS resource_account_id,
        source_accounts.provider_code AS resource_provider_code,
        source_accounts.provider_protocol_profile_id AS resource_provider_protocol_profile_id,
        source_accounts.protocol_code AS resource_protocol_code,
        source_accounts.protocol_version AS resource_protocol_version,
        source_accounts.type AS resource_type,
        source_accounts.status AS resource_status,
        source_accounts.schedulable AS resource_schedulable,
        source_accounts.account_expires_at AS resource_account_expires_at,
        source_accounts.cooldown_until AS resource_cooldown_until,
        source_accounts.last_error_code AS resource_last_error_code,
        source_accounts.credentials_encrypted AS resource_credentials_encrypted,
        source_accounts.proxy_profile_id AS resource_proxy_profile_id,
        source_accounts.concurrency_limit AS resource_concurrency_limit,
        source_accounts.client_compatibility AS resource_client_compatibility,
        COALESCE(source_accounts.id, accounts.id) AS model_resource_account_id,
        COALESCE(source_accounts.provider_code, accounts.provider_code) AS model_resource_provider_code,
        NULL AS quality_score,
        NULL AS quality_state,
        NULL AS quality_ewma_first_token_ms
      FROM ${tables.groupAccounts} AS group_accounts
      INNER JOIN ${tables.accounts} AS accounts ON accounts.id = group_accounts.account_id
      LEFT JOIN ${tables.accounts} AS source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
      WHERE group_accounts.group_id = ?
        AND group_accounts.system_account_id = ?
        AND group_accounts.enabled = 1
        AND accounts.provider_code = ?
        AND accounts.deleted_at IS NULL
        AND accounts.status IN (${statusSetSql})
        AND accounts.schedulable = 1
        AND (? = 1 OR accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ?)
        AND (
          (accounts.authorization_instance_authorization_id IS NULL AND accounts.type IN ('api_key', 'oauth', 'google_oauth'))
          OR (
            accounts.authorization_instance_authorization_id IS NOT NULL
            AND source_accounts.deleted_at IS NULL
            AND source_accounts.provider_code = ?
            AND source_accounts.type IN ('api_key', 'oauth', 'google_oauth')
            AND source_accounts.status IN (${statusSetSql})
            AND source_accounts.schedulable = 1
            AND (? = 1 OR source_accounts.cooldown_until IS NULL OR source_accounts.cooldown_until <= ?)
            AND (source_accounts.account_expires_at IS NULL OR source_accounts.account_expires_at > ?)
            AND (source_accounts.last_error_code IS NULL OR source_accounts.last_error_code <> 'account_expired')
          )
        )
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
    ),
    ranked_rows AS (
      SELECT
        CASE
          WHEN EXISTS (
            SELECT 1
            FROM ${tables.accountSupportedModels} AS direct_models
            WHERE direct_models.account_id = eligible_rows.model_resource_account_id
              AND direct_models.provider_code = eligible_rows.model_resource_provider_code
              AND direct_models.model = ?
          ) THEN 0
          WHEN EXISTS (
            SELECT 1
            FROM ${tables.accountModelMappings} AS model_mappings
            WHERE model_mappings.account_id = eligible_rows.model_resource_account_id
              AND model_mappings.provider_code = eligible_rows.model_resource_provider_code
              AND model_mappings.source_model = ?
              AND model_mappings.source_endpoint_family = ?
              AND model_mappings.enabled = 1
              AND (
                model_mappings.upstream_model <> model_mappings.source_model
                OR model_mappings.upstream_endpoint_family <> model_mappings.source_endpoint_family
              )
              AND (
                NOT EXISTS (
                  SELECT 1
                  FROM ${tables.accountSupportedModels} AS limited_supported
                  WHERE limited_supported.account_id = eligible_rows.model_resource_account_id
                )
                OR EXISTS (
                  SELECT 1
                  FROM ${tables.accountSupportedModels} AS mapped_supported
                  WHERE mapped_supported.account_id = eligible_rows.model_resource_account_id
                    AND mapped_supported.provider_code = eligible_rows.model_resource_provider_code
                    AND mapped_supported.model = model_mappings.upstream_model
                )
              )
          ) THEN 1
          WHEN NOT EXISTS (
            SELECT 1
            FROM ${tables.accountSupportedModels} AS limited_models
            WHERE limited_models.account_id = eligible_rows.model_resource_account_id
          ) THEN 2
          ELSE 3
        END AS model_rank,
        eligible_rows.*
      FROM eligible_rows
    )
    SELECT *
    FROM ranked_rows
    WHERE model_rank < 3
    ORDER BY
      model_rank ASC,
      local_fallback_enabled ASC,
      local_super_priority_enabled DESC,
      COALESCE(local_priority, priority, 0) ASC,
      binding_created_at ASC,
      account_id ASC
    LIMIT ?
  `, [
    groupId,
    groupAccess.groupOwnerSystemAccountId,
    groupAccess.providerCode,
    includeUnavailableFlag,
    now,
    groupAccess.providerCode,
    includeUnavailableFlag,
    now,
    now,
    now,
    model,
    model,
    requestedEndpointFamily ?? null,
    gatewayDispatchAccountCandidateScanLimit
  ])
  const modelRankByAccountId = new Map<string, number>()
  const seenAccountIds = new Set<string>()
  const uniqueRows: OpenAIGroupAccountSelectionRow[] = []
  for (const row of rows) {
    const accountId = row.account_id || row.id
    if (seenAccountIds.has(accountId)) {
      continue
    }
    seenAccountIds.add(accountId)
    const modelRank = Number(row.model_rank)
    if (Number.isFinite(modelRank)) {
      modelRankByAccountId.set(row.id, modelRank)
      modelRankByAccountId.set(accountId, modelRank)
    }
    uniqueRows.push(row)
  }
  return { rows: uniqueRows, modelRankByAccountId }
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
  if (isSqliteReadWorkerProcess() && !existsSync(statsDatabasePath())) {
    return new Map()
  }
  const database = getStatsDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    try {
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
    } catch (error) {
      if (isSqliteReadWorkerProcess() && isMissingAccountQualityTableError(error)) {
        return new Map()
      }
      throw error
    }
  }
  return new Map(rows.map((row) => [row.account_id, row]))
}

export async function loadFreshGatewayDispatchCandidateQualityRowsAsync(
  client: DatabaseClient,
  accountIds: string[],
  freshAfter: string
): Promise<Map<string, Pick<OpenAIAccountRow, 'quality_score' | 'quality_state' | 'quality_ewma_first_token_ms'>>> {
  if (client.driver !== 'postgres') {
    return loadFreshGatewayDispatchCandidateQualityRows(accountIds, freshAfter)
  }
  const ids = [...new Set(accountIds.filter(Boolean))]
  if (!ids.length) return new Map()
  const rows: Array<{
    account_id: string
    quality_score: number | null
    quality_state: string | null
    quality_ewma_first_token_ms: number | null
  }> = []
  const accountQualityScores = tableName(client, statsSchemaName, 'account_quality_scores')
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...await client.query<{
      account_id: string
      quality_score: number | null
      quality_state: string | null
      quality_ewma_first_token_ms: number | null
    }>(`
      SELECT account_id, quality_score, quality_state, ewma_first_token_ms AS quality_ewma_first_token_ms
      FROM ${accountQualityScores}
      WHERE account_id IN (${chunk.map(() => '?').join(', ')})
        AND last_sample_at >= ?
    `, [...chunk, freshAfter]))
  }
  return new Map(rows.map((row) => [row.account_id, row]))
}

function isSqliteReadWorkerProcess(): boolean {
  return process.env.JUHE_AI_SQLITE_READ_WORKER === 'true'
}

function isMissingAccountQualityTableError(error: unknown): boolean {
  return error instanceof Error && /no such table:\s*account_quality_scores/i.test(error.message)
}

function gatewayDispatchCandidateBusinessTables(client: DatabaseClient): {
  accounts: string
  groupAccounts: string
  accountSupportedModels: string
  accountModelMappings: string
} {
  return {
    accounts: tableName(client, businessSchemaName, 'accounts'),
    groupAccounts: tableName(client, businessSchemaName, 'group_accounts'),
    accountSupportedModels: tableName(client, businessSchemaName, 'account_supported_models'),
    accountModelMappings: tableName(client, businessSchemaName, 'account_model_mappings')
  }
}

function tableName(client: DatabaseClient, schemaName: string, name: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(schemaName, name)
    : client.dialect.quoteIdentifier(name)
}
