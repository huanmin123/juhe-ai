#!/usr/bin/env node

import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

export class TestRehearsalEvidenceError extends Error {
  constructor(message) {
    super(message)
    this.name = 'TestRehearsalEvidenceError'
  }
}

const DIGEST_PATTERN = /^sha256:[0-9a-f]{64}$/u
const COMMIT_PATTERN = /^[0-9a-f]{7,64}$/u
const SHA256_HEX_PATTERN = /^[0-9a-f]{64}$/u
export const EVIDENCE_REF_PATTERN = /^(?!\/)(?!.*(?:^|\/)\.\.(?:\/|$))[A-Za-z0-9._\/-]{1,256}$/u
const CREDENTIAL_POLICIES = new Set(['test-only-equivalent', 'isolated-reencrypt'])
const RELEASE_IMAGE_COMPONENTS = Object.freeze([
  ['nodeDigest', 'node-runtime'],
  ['jobsDigest', 'go-jobs'],
  ['gatewayDigest', 'go-gateway']
])
const RUNTIME_IMAGE_ID_KINDS = new Set(['index', 'manifest'])
const ACCOUNT_SELF_FK_POLICIES = new Set([
  'source-before-authorization-instance',
  'deferred-constraints-verified'
])
const REDIS_COMPONENTS = Object.freeze(['cache', 'state', 'queue'])
const REDIS_CREDENTIAL_SOURCES = new Set(['secret-env', 'external-secret', 'mounted-secret'])
const PERMITTED_ENV_DIFFS = new Set([
  'database endpoint',
  'instance id',
  'namespace',
  'public origin',
  'secret material',
  'k8s service discovery',
  'test administrator',
  'resource boundary',
  'test notification target',
  'owner id'
])
const ENV_KEY_PATTERN = /^[A-Z][A-Z0-9_]{1,127}$/u
const ENV_SOURCE_PATTERN = /^[A-Za-z][A-Za-z0-9._:/-]{1,127}$/u
const REQUIRED_ACCOUNT_COLUMNS = new Map([
  ['system_accounts', new Set([
    'id',
    'username',
    'display_name',
    'role',
    'status',
    'password_hash',
    'must_change_password',
    'image_generation_enabled',
    'created_at',
    'updated_at'
  ])],
  ['accounts', new Set([
    'id',
    'config_revision',
    'dispatch_revision',
    'circuit_projection_revision',
    'system_account_id',
    'provider_code',
    'provider_protocol_profile_id',
    'protocol_code',
    'protocol_version',
    'name',
    'type',
    'status',
    'credentials_encrypted',
    'credential_mask',
    'oauth_refresh_token_present',
    'concurrency_limit',
    'priority',
    'super_priority_enabled',
    'fallback_enabled',
    'client_compatibility',
    'schedulable',
    'cooldown_retest_failure_count',
    'temporary_unavailable_continuous_probe_enabled',
    'health_check_model',
    'health_check_endpoint_mode',
    'health_check_failure_count',
    'stream_failure_count',
    'balance_query_enabled',
    'balance_query_config_json',
    'created_at',
    'updated_at'
  ])]
])

// 与 rehearsal-account-sync-preflight.ts 保持同一份闭包；证据不得只挑选少数关键表。
export const ACCOUNT_SYNC_EVIDENCE_TABLE_NAMES = Object.freeze([
  'system_accounts', 'providers', 'protocols', 'protocol_endpoint_families',
  'provider_protocol_profiles', 'provider_protocol_profile_families', 'provider_model_catalog',
  'custom_provider_models', 'provider_default_health_check_models',
  'provider_system_default_health_check_models', 'proxy_profiles', 'model_quality_policies',
  'model_quality_schedules', 'account_quality_enforcements', 'account_name_search_terms',
  'account_name_search_documents', 'account_supported_models', 'account_model_mappings',
  'account_tags', 'account_tag_bindings', 'system_teams', 'system_team_members',
  'resource_authorizations', 'resource_authorization_sources', 'resource_authorization_grants',
  'accounts', 'groups', 'group_authorization_settings', 'group_accounts',
  'group_account_stats_dirty',
  'route_strategies', 'route_strategy_groups', 'response_inspection_policies', 'global_settings',
  'system_settings', 'request_quota_hourly_window_configs', 'request_quota_hourly_window_scope_bindings',
  'api_keys', 'account_schedule_status_events', 'api_key_schedule_status_events'
])

export const RUNTIME_RESET_EVIDENCE_TABLE_NAMES = Object.freeze([
  'account_lock_states', 'account_circuit_incidents', 'account_circuit_outbox',
  'account_api_key_runtime_states', 'account_api_key_pool_probe_cursors',
  'account_quality_enforcements', 'account_name_search_terms', 'account_name_search_documents',
  'account_health_jobs_input_versions', 'account_health_jobs_input_outbox',
  'account_health_projection_receipts', 'account_health_projection_cursors',
  'account_balance_projection_cursors', 'account_list_availability_projections',
  'account_list_availability_projection_index', 'account_list_availability_projection_tags',
  'account_list_availability_projection_search_terms', 'account_list_availability_projection_viewer_health',
  'account_list_availability_runtime_overlays', 'account_list_availability_projection_dependency_health',
  'account_list_availability_dirty', 'account_test_tasks', 'account_test_sessions',
  'account_test_session_tasks', 'proxy_latency_projection_receipts', 'proxy_latency_projection_cursors',
  'account_schedule_status_events', 'api_key_schedule_status_events'
])

export const AUXILIARY_RUNTIME_RESET_EVIDENCE_TABLE_NAMES = Object.freeze([
  'juhe_jobs.account_health_outcomes',
  'juhe_jobs.account_balance_outcomes',
  'juhe_stats.background_job_leases'
])

function isRecord(value) {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function addBlocker(blockers, pathName, message) {
  blockers.push(`${pathName}: ${message}`)
}

function requireRecord(value, pathName, blockers) {
  if (!isRecord(value)) {
    addBlocker(blockers, pathName, '必须是对象')
    return false
  }
  return true
}

function requireBoolean(record, key, pathName, blockers) {
  if (record[key] !== true) {
    addBlocker(blockers, `${pathName}.${key}`, '必须为 true')
  }
}

function requireString(record, key, pathName, blockers, predicate, expected) {
  const value = record[key]
  if (typeof value !== 'string' || value.trim() === '' || (predicate && !predicate.test(value))) {
    addBlocker(blockers, `${pathName}.${key}`, expected)
  }
}

function requireStringArray(record, key, pathName, blockers, expected) {
  if (!Array.isArray(record[key]) || record[key].some(value => typeof value !== 'string' || value.trim() === '')) {
    addBlocker(blockers, `${pathName}.${key}`, expected)
  }
}

function requireEvidenceRefs(record, pathName, blockers) {
  if (!Array.isArray(record.evidenceRefs) || record.evidenceRefs.length === 0 || record.evidenceRefs.some(value => typeof value !== 'string' || !EVIDENCE_REF_PATTERN.test(value))) {
    addBlocker(blockers, `${pathName}.evidenceRefs`, '必须引用受控现场证据文件/记录（不得填入 Secret 或业务正文）')
  }
}

function requireExactValue(record, key, expected, pathName, blockers) {
  if (record[key] !== expected) {
    addBlocker(blockers, `${pathName}.${key}`, `必须为 ${JSON.stringify(expected)}`)
  }
}

function validateTarget(evidence, blockers) {
  if (!requireRecord(evidence.target, 'target', blockers)) return
  requireExactValue(evidence.target, 'environment', 'juhe-ai-test', 'target', blockers)
  requireExactValue(evidence.target, 'namespace', 'juhe-ai-test', 'target', blockers)
}

function validateRelease(evidence, blockers) {
  if (!requireRecord(evidence.release, 'release', blockers)) return
  requireString(evidence.release, 'sourceCommit', 'release', blockers, COMMIT_PATTERN, '必须是 source commit')
  requireExactValue(evidence.release, 'releaseMode', 'single-active-stop', 'release', blockers)
  for (const key of ['nodeDigest', 'jobsDigest', 'gatewayDigest']) {
    requireString(evidence.release, key, 'release', blockers, DIGEST_PATTERN, '必须是不可变 sha256 digest')
  }
  requireBoolean(evidence.release, 'sameDigestInTestAndProd', 'release', blockers)
  requireEvidenceRefs(evidence.release, 'release', blockers)
  for (const environment of ['test', 'prod']) {
    const pathName = `release.${environment}`
    if (!requireRecord(evidence.release[environment], pathName, blockers)) continue
    requireString(evidence.release[environment], 'sourceCommit', pathName, blockers, COMMIT_PATTERN, '必须有现场 source commit')
    for (const key of ['nodeDigest', 'jobsDigest', 'gatewayDigest']) {
      requireString(evidence.release[environment], key, pathName, blockers, DIGEST_PATTERN, '必须有现场镜像 digest')
      if (evidence.release[environment][key] !== evidence.release[key]) addBlocker(blockers, pathName, `${key} 必须与候选 release digest 一致`)
    }
    if (evidence.release[environment].sourceCommit !== evidence.release.sourceCommit) addBlocker(blockers, pathName, 'sourceCommit 必须与候选 release 一致')
    requireEvidenceRefs(evidence.release[environment], pathName, blockers)
    validateImageResolution(evidence.release[environment], pathName, evidence.release, blockers)
  }
}

function validateImageResolution(environmentEvidence, pathName, release, blockers) {
  const resolutionPath = `${pathName}.imageResolution`
  if (!requireRecord(environmentEvidence.imageResolution, resolutionPath, blockers)) return
  for (const [digestKey, component] of RELEASE_IMAGE_COMPONENTS) {
    const componentPath = `${resolutionPath}.${component}`
    const resolution = environmentEvidence.imageResolution[component]
    if (!requireRecord(resolution, componentPath, blockers)) continue
    requireString(resolution, 'requestedDigest', componentPath, blockers, DIGEST_PATTERN, '必须记录 Pod spec 请求的 OCI index digest')
    requireString(resolution, 'registryManifestDigest', componentPath, blockers, DIGEST_PATTERN, '必须记录 registry 回读的 OCI index digest')
    requireString(resolution, 'resolvedPlatformManifestDigest', componentPath, blockers, DIGEST_PATTERN, '必须记录目标节点平台 manifest digest')
    requireString(resolution, 'runtimeImageID', componentPath, blockers, DIGEST_PATTERN, '必须记录 Pod status.imageID digest')
    if (!RUNTIME_IMAGE_ID_KINDS.has(resolution.runtimeImageIDKind)) {
      addBlocker(blockers, `${componentPath}.runtimeImageIDKind`, '必须为 index 或 manifest')
    }
    requireString(resolution, 'runtimeImageIDPlatformManifestDigest', componentPath, blockers, DIGEST_PATTERN, '必须记录 status.imageID 解析到的目标平台 manifest digest')
    requireExactValue(resolution, 'platform', 'linux/amd64', componentPath, blockers)
    requireString(resolution, 'evidenceRef', componentPath, blockers, EVIDENCE_REF_PATTERN, '必须引用 registry 与 Pod imageID 的受控回读证据')
    const expectedDigest = release[digestKey]
    if (resolution.requestedDigest !== expectedDigest) {
      addBlocker(blockers, `${componentPath}.requestedDigest`, `必须与候选 ${digestKey} 一致`)
    }
    if (resolution.registryManifestDigest !== resolution.requestedDigest) {
      addBlocker(blockers, `${componentPath}.registryManifestDigest`, '必须与 requestedDigest 一致，禁止 tag 或未核对的 registry 响应')
    }
    if (resolution.runtimeImageIDPlatformManifestDigest !== resolution.resolvedPlatformManifestDigest) {
      addBlocker(blockers, `${componentPath}.runtimeImageIDPlatformManifestDigest`, '必须与 resolvedPlatformManifestDigest 一致；index imageID 允许是同一平台 manifest 的已核验别名')
    }
    if (resolution.runtimeImageIDKind === 'manifest' && resolution.runtimeImageID !== resolution.resolvedPlatformManifestDigest) {
      addBlocker(blockers, `${componentPath}.runtimeImageID`, 'manifest 类型的 runtimeImageID 必须直接等于 resolvedPlatformManifestDigest')
    }
  }
}

function validateRuntime(evidence, blockers) {
  if (!requireRecord(evidence.runtime, 'runtime', blockers)) return
  const runtime = evidence.runtime
  requireEvidenceRefs(runtime, 'runtime', blockers)
  if (!requireRecord(runtime.argo, 'runtime.argo', blockers)) return
  requireExactValue(runtime.argo, 'syncStatus', 'Synced', 'runtime.argo', blockers)
  requireExactValue(runtime.argo, 'healthStatus', 'Healthy', 'runtime.argo', blockers)

  if (!requireRecord(runtime.active, 'runtime.active', blockers)) return
  requireExactValue(runtime.active, 'slot', 'a', 'runtime.active', blockers)
  requireExactValue(runtime.active, 'replicas', 1, 'runtime.active', blockers)
  requireExactValue(runtime.active, 'readyContainers', 3, 'runtime.active', blockers)
  requireExactValue(runtime.active, 'containerCount', 3, 'runtime.active', blockers)

  if (!requireRecord(runtime.standby, 'runtime.standby', blockers)) return
  requireExactValue(runtime.standby, 'replicas', 0, 'runtime.standby', blockers)

  if (!Array.isArray(runtime.stableServiceSlots) || runtime.stableServiceSlots.length !== 1 || runtime.stableServiceSlots[0] !== 'a') {
    addBlocker(blockers, 'runtime.stableServiceSlots', '必须只包含 active A 槽位')
  }

  if (!requireRecord(runtime.owner, 'runtime.owner', blockers)) return
  requireExactValue(runtime.owner, 'activeOwnerCount', 1, 'runtime.owner', blockers)
  requireExactValue(runtime.owner, 'jobsConsumerCount', 1, 'runtime.owner', blockers)
  requireBoolean(runtime.owner, 'leaseVerified', 'runtime.owner', blockers)

  if (!requireRecord(runtime.health, 'runtime.health', blockers)) return
  for (const key of ['nodeDbReady', 'nodeApi', 'jobs', 'gateway', 'f3', 'f4']) {
    requireBoolean(runtime.health, key, 'runtime.health', blockers)
  }
}

function validateSchema(evidence, blockers) {
  if (!requireRecord(evidence.schema, 'schema', blockers)) return
  const schema = evidence.schema
  requireExactValue(schema, 'threeWayStatus', 'passed', 'schema', blockers)
  if (!Number.isInteger(schema.candidateContractVersion) || schema.candidateContractVersion < 1) {
    addBlocker(blockers, 'schema.candidateContractVersion', '必须是正整数')
  }
  for (const key of [
    'productionSnapshotReadOnly',
    'testCloneApplied',
    'testReadbackVerified',
    'productionPreflightVerified',
    'tablesColumnsConstraintsIndexesIncluded',
    'aclRolesExtensionsIncluded',
    'functionsTriggersViewsPartitionsSequencesIncluded'
  ]) {
    requireBoolean(schema, key, 'schema', blockers)
  }
  requireString(schema, 'productionSnapshotDigest', 'schema', blockers, /^[0-9a-f]{64}$/u, '必须有生产 schema snapshot sha256')
  requireString(schema, 'testBaselineDigest', 'schema', blockers, /^[0-9a-f]{64}$/u, '必须有 test schema 基线 sha256')
  requireString(schema, 'candidateContractDigest', 'schema', blockers, /^[0-9a-f]{64}$/u, '必须有候选代码 contract sha256')
  if (!Array.isArray(schema.approvedForwardDeltas)) {
    addBlocker(blockers, 'schema.approvedForwardDeltas', '必须是显式审批的前向差异数组')
  } else {
    const hasDigestDifference = new Set([
      schema.productionSnapshotDigest,
      schema.testBaselineDigest,
      schema.candidateContractDigest
    ]).size > 1
    if (hasDigestDifference && schema.approvedForwardDeltas.length === 0) {
      addBlocker(blockers, 'schema.approvedForwardDeltas', '三方 schema digest 存在差异时必须至少列出一条已审批的加法变更')
    }
    for (const [index, delta] of schema.approvedForwardDeltas.entries()) {
      if (!isRecord(delta)) {
        addBlocker(blockers, `schema.approvedForwardDeltas[${index}]`, '必须是对象')
        continue
      }
      requireString(delta, 'id', `schema.approvedForwardDeltas[${index}]`, blockers, undefined, '必须有差异标识')
      requireString(delta, 'approvedBy', `schema.approvedForwardDeltas[${index}]`, blockers, undefined, '必须有审批人')
      requireExactValue(delta, 'changeType', 'additive', `schema.approvedForwardDeltas[${index}]`, blockers)
      requireStringArray(delta, 'objects', `schema.approvedForwardDeltas[${index}]`, blockers, '必须列出加法变更对象')
      requireString(delta, 'approvedAt', `schema.approvedForwardDeltas[${index}]`, blockers, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/u, '必须有 UTC 审批时间')
      requireString(delta, 'evidenceRef', `schema.approvedForwardDeltas[${index}]`, blockers, EVIDENCE_REF_PATTERN, '必须引用受控变更证据（禁止绝对路径或 .. 路径）')
    }
  }
  requireEvidenceRefs(schema, 'schema', blockers)
}

function validateAccounts(evidence, blockers) {
  if (!requireRecord(evidence.accounts, 'accounts', blockers)) return
  const accounts = evidence.accounts
  requireEvidenceRefs(accounts, 'accounts', blockers)
  requireExactValue(accounts, 'status', 'passed', 'accounts', blockers)
  if (!CREDENTIAL_POLICIES.has(accounts.credentialsPolicy)) {
    addBlocker(blockers, 'accounts.credentialsPolicy', '必须使用 test-only-equivalent 或 isolated-reencrypt')
  }
  requireString(
    accounts,
    'credentialsEvidenceRef',
    'accounts',
    blockers,
    EVIDENCE_REF_PATTERN,
    '必须引用凭据策略/重加密或 test canary 验证证据（不得写入凭据正文）'
  )
  requireString(
    accounts,
    'approvedCanaryAccountIdsHash',
    'accounts',
    blockers,
    SHA256_HEX_PATTERN,
    '必须记录批准 canary 账户 ID 排序后的 sha256 摘要（不得记录原始 ID）'
  )
  requireString(
    accounts,
    'closureEvidenceRef',
    'accounts',
    blockers,
    EVIDENCE_REF_PATTERN,
    '必须引用 canary→账户/系统账户/分组/路由/授权/API key 映射闭包证据（不得写入原始 ID）'
  )
  for (const key of [
    'sourceAccountIdsHash',
    'systemAccountIdsHash',
    'groupIdsHash',
    'routeStrategyIdsHash',
    'resourceAuthorizationIdsHash',
    'apiKeyRemapHash'
  ]) {
    requireString(accounts, key, 'accounts', blockers, SHA256_HEX_PATTERN, '必须记录同步引用闭包的排序 ID 或映射 sha256 摘要')
  }
  if (!Number.isInteger(accounts.approvedCanaryCount) || accounts.approvedCanaryCount < 1) {
    addBlocker(blockers, 'accounts.approvedCanaryCount', '必须是大于 0 的批准 canary 账户数量')
  }
  for (const key of [
    'productionSecretReused',
    'rawProductionCredentialsCopied',
    'productionApiKeysCopied',
    'runtimeStateCopied'
  ]) {
    if (accounts[key] !== false) addBlocker(blockers, `accounts.${key}`, '必须为 false')
  }
  for (const key of [
    'fieldLevelTransformVerified',
    'apiKeysRegenerated',
    'runtimeStateReset',
    'schedulesDisabledUntilSmoke',
    'systemAccountRequiredFieldsVerified',
    'sourceAccountDependencyOrderVerified',
    'foreignKeysVerified',
    'allowlistClosed'
  ]) {
    requireBoolean(accounts, key, 'accounts', blockers)
  }
  for (const key of ['accountScheduleStatusEventsRows', 'apiKeyScheduleStatusEventsRows']) {
    requireExactValue(accounts, key, 0, 'accounts', blockers)
  }
  if (!Array.isArray(accounts.tables) || accounts.tables.length === 0) {
    addBlocker(blockers, 'accounts.tables', '必须列出逐表字段处理与行数摘要')
  } else {
    const tableNames = new Set()
    for (const [index, table] of accounts.tables.entries()) {
      if (!isRecord(table)) {
        addBlocker(blockers, `accounts.tables[${index}]`, '必须是对象')
        continue
      }
      const tablePath = `accounts.tables[${index}]`
      requireEvidenceRefs(table, tablePath, blockers)
      requireString(table, 'name', tablePath, blockers, undefined, '必须有表名')
      if (typeof table.name === 'string' && table.name.trim() !== '') tableNames.add(table.name.trim())
      for (const key of ['sourceRows', 'targetRows', 'rows']) {
        if (!Number.isInteger(table[key]) || table[key] < 0) {
          addBlocker(blockers, `${tablePath}.${key}`, '必须是非负整数')
        }
      }
      if (Number.isInteger(table.targetRows) && Number.isInteger(table.rows) && table.rows !== table.targetRows) {
        addBlocker(blockers, `${tablePath}.rows`, '必须与 targetRows 一致（表示同步后的目标行数）')
      }
      requireString(table, 'sourceChecksum', tablePath, blockers, SHA256_HEX_PATTERN, '必须有源表行摘要 sha256')
      requireString(table, 'targetChecksum', tablePath, blockers, SHA256_HEX_PATTERN, '必须有目标表行摘要 sha256')
      if (typeof table.name !== 'string' || !RUNTIME_RESET_EVIDENCE_TABLE_NAMES.includes(table.name.trim())) {
        requireString(table, 'readbackDigest', tablePath, blockers, SHA256_HEX_PATTERN, '配置表必须有目标字段 readback 摘要 sha256')
      }
      if (!Number.isInteger(table.importOrder) || table.importOrder < 1) {
        addBlocker(blockers, `${tablePath}.importOrder`, '必须是正整数')
      }
      requireString(table, 'transformation', tablePath, blockers, undefined, '必须有字段转换说明')
      requireStringArray(table, 'copiedColumns', tablePath, blockers, '必须列出复制列（可为空数组）')
      requireStringArray(table, 'generatedColumns', tablePath, blockers, '必须列出生成/替换列（可为空数组）')
      requireStringArray(table, 'clearedColumns', tablePath, blockers, '必须列出清空列（可为空数组）')
      requireString(table, 'conflictStrategy', tablePath, blockers, undefined, '必须明确冲突处理策略')
      const requiredColumns = typeof table.name === 'string' ? REQUIRED_ACCOUNT_COLUMNS.get(table.name.trim()) : undefined
      if (requiredColumns) {
        requireStringArray(table, 'requiredNotNullColumns', `accounts.tables[${index}]`, blockers, '必须显式列出本表所有 NOT NULL 字段')
        const declaredRequiredColumns = new Set(Array.isArray(table.requiredNotNullColumns) ? table.requiredNotNullColumns : [])
        const handledColumns = new Set([
          ...(Array.isArray(table.copiedColumns) ? table.copiedColumns : []),
          ...(Array.isArray(table.generatedColumns) ? table.generatedColumns : [])
        ])
        const clearedColumns = new Set(Array.isArray(table.clearedColumns) ? table.clearedColumns : [])
        for (const column of requiredColumns) {
          if (!declaredRequiredColumns.has(column)) {
            addBlocker(blockers, `accounts.tables[${index}].requiredNotNullColumns`, `${table.name} 必须声明 NOT NULL 列 ${column}`)
          }
          if (!handledColumns.has(column)) {
            addBlocker(blockers, `accounts.tables[${index}]`, `${table.name} 必须在 copiedColumns 或 generatedColumns 中处理 NOT NULL 列 ${column}`)
          }
          if (clearedColumns.has(column) && !(Array.isArray(table.generatedColumns) && table.generatedColumns.includes(column))) {
            addBlocker(
              blockers,
              `accounts.tables[${index}].clearedColumns`,
              `${table.name} 的 NOT NULL 列 ${column} 不得只清空；必须在 generatedColumns 中声明重置值（例如计数器重置为 0）`
            )
          }
        }
      }
      if (table.name === 'accounts' && Array.isArray(table.clearedColumns) && table.clearedColumns.includes('authorization_instance_source_account_id')) {
        addBlocker(blockers, `accounts.tables[${index}].clearedColumns`, '不得统一清空 authorization_instance_source_account_id；必须按 source account 拓扑保留子账户自引用')
      }
      if (table.name === 'accounts') {
        const copiedColumns = new Set(Array.isArray(table.copiedColumns) ? table.copiedColumns : [])
        const generatedColumns = new Set(Array.isArray(table.generatedColumns) ? table.generatedColumns : [])
        const clearedColumns = new Set(Array.isArray(table.clearedColumns) ? table.clearedColumns : [])
        for (const derivedCredentialColumn of [
          'credentials_encrypted',
          'credential_fingerprint',
          'credential_mask',
          'oauth_access_token_expires_at',
          'oauth_refresh_token_present'
        ]) {
          if (!generatedColumns.has(derivedCredentialColumn)) {
            addBlocker(blockers, `${tablePath}.generatedColumns`, `accounts 的 ${derivedCredentialColumn} 必须使用 test 凭据逐行生成/替换，不能复制生产派生值`)
          }
          if (copiedColumns.has(derivedCredentialColumn) || clearedColumns.has(derivedCredentialColumn)) {
            addBlocker(blockers, `${tablePath}`, `accounts 的 ${derivedCredentialColumn} 不得复制或仅清空生产派生值`)
          }
        }
        if (copiedColumns.has('availability_schedule_next_check_at') || !generatedColumns.has('availability_schedule_next_check_at')) {
          addBlocker(blockers, `${tablePath}.availability_schedule_next_check_at`, '必须按 test 预演窗口逐行生成/替换，禁止复制生产旧时间戳')
        }
        requireBoolean(table, 'availabilityScheduleNextCheckAtControlled', tablePath, blockers)
        if (!ACCOUNT_SELF_FK_POLICIES.has(table.selfForeignKeyPolicy)) {
          addBlocker(blockers, `accounts.tables[${index}].selfForeignKeyPolicy`, '必须明确 source-before-authorization-instance 或已验证的 deferred-constraints-verified')
        }
        if (!Number.isInteger(table.sourceAccountRows) || table.sourceAccountRows < 0) {
          addBlocker(blockers, `accounts.tables[${index}].sourceAccountRows`, '必须记录 source account 导入行数')
        }
        if (!Number.isInteger(table.authorizationInstanceRows) || table.authorizationInstanceRows < 0) {
          addBlocker(blockers, `accounts.tables[${index}].authorizationInstanceRows`, '必须记录 authorization-instance account 导入行数')
        }
        if (table.selfForeignKeyPolicy === 'source-before-authorization-instance' && table.authorizationInstanceRows > 0 && table.sourceAccountRows < 1) {
          addBlocker(blockers, `accounts.tables[${index}]`, '存在 authorization-instance accounts 时必须先导入至少一个 source account')
        }
      }
      if (table.name === 'providers') {
        if (table.selfForeignKeyPolicy !== 'parent-before-child') {
          addBlocker(blockers, `${tablePath}.selfForeignKeyPolicy`, 'providers.parent_code 自引用必须按 parent-before-child 拓扑导入')
        }
        requireBoolean(table, 'parentBeforeChildVerified', tablePath, blockers)
        const copiedColumns = new Set(Array.isArray(table.copiedColumns) ? table.copiedColumns : [])
        for (const relationshipColumn of ['code', 'parent_code']) {
          if (!copiedColumns.has(relationshipColumn)) {
            addBlocker(blockers, `${tablePath}.copiedColumns`, `providers 自引用拓扑列 ${relationshipColumn} 必须原值复制`)
          }
        }
      }
      if (table.name === 'model_quality_schedules') {
        requireBoolean(table, 'canaryOnly', `accounts.tables[${index}]`, blockers)
        requireBoolean(table, 'disabledUntilSmoke', `accounts.tables[${index}]`, blockers)
        requireBoolean(table, 'nextRunAtControlled', `accounts.tables[${index}]`, blockers)
      }
    }
    if (tableNames.size !== accounts.tables.length) addBlocker(blockers, 'accounts.tables', '表名不得重复')
    const importOrders = accounts.tables
      .filter(table => isRecord(table))
      .map(table => table.importOrder)
      .filter(order => Number.isInteger(order))
    if (new Set(importOrders).size !== importOrders.length) {
      addBlocker(blockers, 'accounts.tables.importOrder', '导入顺序必须唯一，避免外键根与运行态表处理不确定')
    }
    const expectedTableNames = new Set(ACCOUNT_SYNC_EVIDENCE_TABLE_NAMES)
    for (const required of expectedTableNames) {
      if (!tableNames.has(required)) addBlocker(blockers, 'accounts.tables', `必须包含 ${required} 的行数与字段处理证据`)
    }
    for (const actual of tableNames) {
      if (!expectedTableNames.has(actual)) addBlocker(blockers, 'accounts.tables', `存在未批准的账户同步表 ${actual}`)
    }
    if (tableNames.size !== expectedTableNames.size) {
      addBlocker(blockers, 'accounts.tables', `必须完整覆盖 ${expectedTableNames.size} 张账户同步白名单表`)
    }
  }
  if (!Array.isArray(accounts.runtimeResetTables)) {
    addBlocker(blockers, 'accounts.runtimeResetTables', '必须逐表记录 28 张运行态清空表的清理前后行数与 checksum')
  } else {
    const runtimeNames = new Set()
    for (const [index, table] of accounts.runtimeResetTables.entries()) {
      if (!isRecord(table)) {
        addBlocker(blockers, `accounts.runtimeResetTables[${index}]`, '必须是对象')
        continue
      }
      const tablePath = `accounts.runtimeResetTables[${index}]`
      requireEvidenceRefs(table, tablePath, blockers)
      requireString(table, 'name', tablePath, blockers, undefined, '必须有表名')
      if (typeof table.name === 'string' && table.name.trim() !== '') runtimeNames.add(table.name.trim())
      for (const key of ['beforeRows', 'afterRows']) {
        if (!Number.isInteger(table[key]) || table[key] < 0) addBlocker(blockers, `${tablePath}.${key}`, '必须是非负整数')
      }
      requireExactValue(table, 'afterRows', 0, tablePath, blockers)
      requireString(table, 'checksum', tablePath, blockers, SHA256_HEX_PATTERN, '必须有清理后 checksum')
    }
    const expectedRuntimeNames = new Set(RUNTIME_RESET_EVIDENCE_TABLE_NAMES)
    for (const required of expectedRuntimeNames) {
      if (!runtimeNames.has(required)) addBlocker(blockers, 'accounts.runtimeResetTables', `必须包含 ${required} 的清理证据`)
    }
    for (const actual of runtimeNames) {
      if (!expectedRuntimeNames.has(actual)) addBlocker(blockers, 'accounts.runtimeResetTables', `存在未批准的运行态表 ${actual}`)
    }
    if (runtimeNames.size !== expectedRuntimeNames.size) addBlocker(blockers, 'accounts.runtimeResetTables', `必须完整覆盖 ${expectedRuntimeNames.size} 张运行态清空表`)
  }

  if (!Array.isArray(accounts.auxiliaryRuntimeResetTables)) {
    addBlocker(blockers, 'accounts.auxiliaryRuntimeResetTables', '必须逐表记录 juhe_jobs/juhe_stats 运行态表的清理前后行数与 checksum')
  } else {
    const auxiliaryNames = new Set()
    for (const [index, table] of accounts.auxiliaryRuntimeResetTables.entries()) {
      if (!isRecord(table)) {
        addBlocker(blockers, `accounts.auxiliaryRuntimeResetTables[${index}]`, '必须是对象')
        continue
      }
      const tablePath = `accounts.auxiliaryRuntimeResetTables[${index}]`
      requireEvidenceRefs(table, tablePath, blockers)
      requireString(table, 'name', tablePath, blockers, undefined, '必须有 schema-qualified 表名')
      if (typeof table.name === 'string' && table.name.trim() !== '') auxiliaryNames.add(table.name.trim())
      for (const key of ['beforeRows', 'afterRows']) {
        if (!Number.isInteger(table[key]) || table[key] < 0) addBlocker(blockers, `${tablePath}.${key}`, '必须是非负整数')
      }
      requireExactValue(table, 'afterRows', 0, tablePath, blockers)
      requireString(table, 'checksum', tablePath, blockers, SHA256_HEX_PATTERN, '必须有清理后 checksum')
    }
    const expectedAuxiliaryNames = new Set(AUXILIARY_RUNTIME_RESET_EVIDENCE_TABLE_NAMES)
    for (const required of expectedAuxiliaryNames) {
      if (!auxiliaryNames.has(required)) addBlocker(blockers, 'accounts.auxiliaryRuntimeResetTables', `必须包含 ${required} 的清理证据`)
    }
    for (const actual of auxiliaryNames) {
      if (!expectedAuxiliaryNames.has(actual)) addBlocker(blockers, 'accounts.auxiliaryRuntimeResetTables', `存在未批准的辅助运行态表 ${actual}`)
    }
    if (auxiliaryNames.size !== expectedAuxiliaryNames.size) addBlocker(blockers, 'accounts.auxiliaryRuntimeResetTables', `必须完整覆盖 ${expectedAuxiliaryNames.size} 张辅助运行态清空表`)
  }
}

function validateEnvironment(evidence, blockers) {
  if (!requireRecord(evidence.environment, 'environment', blockers)) return
  const environment = evidence.environment
  requireEvidenceRefs(environment, 'environment', blockers)
  requireExactValue(environment, 'status', 'passed', 'environment', blockers)
  requireBoolean(environment, 'sameProductionSemantics', 'environment', blockers)
  requireBoolean(environment, 'secretValuesNotRecorded', 'environment', blockers)
  requireBoolean(environment, 'cookieSecureResolvedProduction', 'environment', blockers)
  requireString(
    environment,
    'finalPodEnvEvidenceRef',
    'environment',
    blockers,
    EVIDENCE_REF_PATTERN,
    '必须引用最终 Pod env 键集合/值哈希对账证据'
  )
  requireBoolean(environment, 'finalPodEnvKeySetCompared', 'environment', blockers)
  requireBoolean(environment, 'finalPodEnvValuesHashedOnly', 'environment', blockers)
  if (!Array.isArray(environment.finalPodEnvUnexpectedDiffs) || environment.finalPodEnvUnexpectedDiffs.length !== 0) {
    addBlocker(blockers, 'environment.finalPodEnvUnexpectedDiffs', '必须为空；最终 Pod env 未批准差异必须阻断')
  }
  if (!Array.isArray(environment.finalPodEnvPermittedCategories) || environment.finalPodEnvPermittedCategories.length === 0) {
    addBlocker(blockers, 'environment.finalPodEnvPermittedCategories', '必须列出最终 Pod env 的允许差异类别')
  } else {
    const seenFinalCategories = new Set()
    for (const category of environment.finalPodEnvPermittedCategories) {
      if (typeof category !== 'string' || !PERMITTED_ENV_DIFFS.has(category)) {
        addBlocker(blockers, 'environment.finalPodEnvPermittedCategories', `存在未批准的最终 Pod env 差异类别 ${String(category)}`)
      }
      if (seenFinalCategories.has(category)) {
        addBlocker(blockers, 'environment.finalPodEnvPermittedCategories', `最终 Pod env 差异类别不得重复 ${String(category)}`)
      }
      seenFinalCategories.add(category)
    }
  }
  if (!Array.isArray(environment.unexpectedDiffs) || environment.unexpectedDiffs.length !== 0) {
    addBlocker(blockers, 'environment.unexpectedDiffs', '必须为空')
  }
  if (!Array.isArray(environment.permittedDiffs)) {
    addBlocker(blockers, 'environment.permittedDiffs', '必须明确列出允许的环境差异')
  } else {
    const seen = new Set()
    for (const diff of environment.permittedDiffs) {
      if (typeof diff !== 'string' || !PERMITTED_ENV_DIFFS.has(diff)) addBlocker(blockers, 'environment.permittedDiffs', `存在未批准的环境差异 ${String(diff)}`)
      if (seen.has(diff)) addBlocker(blockers, 'environment.permittedDiffs', `环境差异不得重复 ${String(diff)}`)
      seen.add(diff)
    }
  }
  if (!Array.isArray(environment.permittedDiffDetails)) {
    addBlocker(blockers, 'environment.permittedDiffDetails', '必须逐键列出每个允许差异的来源、值哈希、审批和证据；无差异时使用空数组')
    return
  }
  if (environment.permittedDiffDetails.length === 0
    && Array.isArray(environment.permittedDiffs)
    && environment.permittedDiffs.length > 0) {
    addBlocker(blockers, 'environment.permittedDiffDetails', '存在允许差异类别时必须提供逐键详情')
  }
  const detailKeys = new Set()
  const detailCategories = new Set()
  for (const [index, detail] of environment.permittedDiffDetails.entries()) {
    const detailPath = `environment.permittedDiffDetails[${index}]`
    if (!isRecord(detail)) {
      addBlocker(blockers, detailPath, '必须是对象')
      continue
    }
    requireString(detail, 'key', detailPath, blockers, ENV_KEY_PATTERN, '必须是环境变量键名')
    if (typeof detail.key === 'string' && detail.key.trim() !== '') {
      if (detailKeys.has(detail.key)) addBlocker(blockers, `${detailPath}.key`, `环境变量键不得重复 ${detail.key}`)
      detailKeys.add(detail.key)
    }
    requireString(detail, 'category', detailPath, blockers, undefined, '必须有差异类别')
    if (typeof detail.category === 'string') {
      if (!PERMITTED_ENV_DIFFS.has(detail.category)) {
        addBlocker(blockers, `${detailPath}.category`, `存在未批准的环境差异类别 ${detail.category}`)
      }
      detailCategories.add(detail.category)
      if (Array.isArray(environment.finalPodEnvPermittedCategories)
        && !environment.finalPodEnvPermittedCategories.includes(detail.category)) {
        addBlocker(blockers, `${detailPath}.category`, '差异类别必须包含在 finalPodEnvPermittedCategories 中')
      }
    }
    requireString(detail, 'testSource', detailPath, blockers, ENV_SOURCE_PATTERN, '必须记录 test 键的注入/解析来源')
    requireString(detail, 'prodSource', detailPath, blockers, ENV_SOURCE_PATTERN, '必须记录 prod 键的注入/解析来源')
    requireString(detail, 'testValueHash', detailPath, blockers, SHA256_HEX_PATTERN, '必须记录 test 解析值 sha256（不得记录原值）')
    requireString(detail, 'prodValueHash', detailPath, blockers, SHA256_HEX_PATTERN, '必须记录 prod 解析值 sha256（不得记录原值）')
    if (typeof detail.testValueHash === 'string' && typeof detail.prodValueHash === 'string'
      && SHA256_HEX_PATTERN.test(detail.testValueHash) && SHA256_HEX_PATTERN.test(detail.prodValueHash)
      && detail.testValueHash === detail.prodValueHash) {
      addBlocker(blockers, detailPath, '允许差异的 test/prod 值哈希不能相同；相同值不应列为差异')
    }
    requireStringArray(detail, 'consumerContainers', detailPath, blockers, '必须列出消费该键的容器')
    requireString(detail, 'reason', detailPath, blockers, undefined, '必须说明该差异的原因和边界')
    requireString(detail, 'approvedBy', detailPath, blockers, /^[A-Za-z0-9._:@/-]{1,128}$/u, '必须记录批准人')
    requireString(detail, 'approvedAt', detailPath, blockers, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/u, '必须记录 UTC 批准时间')
    requireString(detail, 'evidenceRef', detailPath, blockers, EVIDENCE_REF_PATTERN, '必须引用逐键差异现场证据')
  }
  if (Array.isArray(environment.permittedDiffs)) {
    const permittedCategories = new Set(environment.permittedDiffs)
    for (const category of permittedCategories) {
      if (!detailCategories.has(category)) {
        addBlocker(blockers, 'environment.permittedDiffDetails', `允许差异类别 ${category} 缺少逐键详情`)
      }
    }
    for (const category of detailCategories) {
      if (!permittedCategories.has(category)) {
        addBlocker(blockers, 'environment.permittedDiffs', `逐键详情声明了未列入 permittedDiffs 的类别 ${category}`)
      }
    }
  }
}

function validateRedis(evidence, blockers) {
  if (!requireRecord(evidence.redis, 'redis', blockers)) return
  const redis = evidence.redis
  requireEvidenceRefs(redis, 'redis', blockers)
  requireExactValue(redis, 'status', 'passed', 'redis', blockers)
  requireString(redis, 'credentialInjectionEvidenceRef', 'redis', blockers, EVIDENCE_REF_PATTERN, '必须引用 Redis 凭据注入来源/进程参数的受控回读证据')
  requireExactValue(redis, 'inlineCredentialDetected', false, 'redis', blockers)
  if (requireRecord(redis.credentialSourceByComponent, 'redis.credentialSourceByComponent', blockers)) {
    for (const component of REDIS_COMPONENTS) {
      const componentPath = `redis.credentialSourceByComponent.${component}`
      requireString(redis.credentialSourceByComponent, component, 'redis.credentialSourceByComponent', blockers, null, '必须明确每个 Redis 组件的凭据来源')
      if (typeof redis.credentialSourceByComponent[component] === 'string'
        && !REDIS_CREDENTIAL_SOURCES.has(redis.credentialSourceByComponent[component])) {
        addBlocker(blockers, componentPath, '凭据必须来自 Secret/env、external secret 或 mounted secret，不得来自命令行参数')
      }
    }
    for (const component of Object.keys(redis.credentialSourceByComponent)) {
      if (!REDIS_COMPONENTS.includes(component)) {
        addBlocker(blockers, 'redis.credentialSourceByComponent', `存在未批准的 Redis 组件 ${component}`)
      }
    }
  }
  for (const key of [
    'physicalEndpointDistinct',
    'logicalDbDistinct',
    'namespaceDistinct',
    'aclUserDistinct',
    'forbiddenCommandsDenied',
    'crossEnvironmentKeysZero',
    'persistenceAndCapacityVerified'
  ]) {
    requireBoolean(redis, key, 'redis', blockers)
  }
  if (redis.sharedDefaultUser === true) {
    addBlocker(blockers, 'redis.sharedDefaultUser', '禁止 test/prod 共用高权限 default 用户')
  }
}

function validateSmoke(evidence, blockers) {
  if (!requireRecord(evidence.smoke, 'smoke', blockers)) return
  const smoke = evidence.smoke
  requireEvidenceRefs(smoke, 'smoke', blockers)
  requireExactValue(smoke, 'status', 'passed', 'smoke', blockers)
  for (const key of [
    'testLogin',
    'approvedUpstreamCanary',
    'ordinaryGatewayRequest',
    'sse',
    'upload',
    'businessWriteRead',
    'j1J2OutcomeReadback',
    'errorPathAndRecovery',
    'structuredLogs'
  ]) {
    requireBoolean(smoke, key, 'smoke', blockers)
  }
  if (smoke.modelCheckMode === 'disabled') {
    requireBoolean(smoke, 'modelCheckCompatibilityAcknowledged', 'smoke', blockers)
  } else if (smoke.modelCheckMode !== 'separate-j3b-release') {
    addBlocker(blockers, 'smoke.modelCheckMode', '必须为 disabled 或 separate-j3b-release')
  }
}

function validateControls(evidence, blockers) {
  if (!requireRecord(evidence.controls, 'controls', blockers)) return
  const controls = evidence.controls
  requireEvidenceRefs(controls, 'controls', blockers)
  requireString(controls, 'verifierIdentity', 'controls', blockers, /^[A-Za-z0-9._:@/-]{1,128}$/u, '必须记录受控 verifier 身份')
  requireString(controls, 'verifiedAt', 'controls', blockers, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/u, '必须记录 UTC 验证时间')
  requireString(controls, 'evidenceManifestDigest', 'controls', blockers, SHA256_HEX_PATTERN, '必须记录证据清单 SHA-256 摘要')
  requireString(controls, 'evidenceBoundSourceCommit', 'controls', blockers, COMMIT_PATTERN, '必须记录证据生成时绑定的候选 source commit')
  if (isRecord(evidence.release)
    && typeof evidence.release.sourceCommit === 'string'
    && typeof controls.evidenceBoundSourceCommit === 'string'
    && controls.evidenceBoundSourceCommit !== evidence.release.sourceCommit) {
    addBlocker(blockers, 'controls.evidenceBoundSourceCommit', '必须与 release.sourceCommit 一致，禁止复用其他候选的演练证据')
  }
  for (const key of ['singleActiveGitOpsVerified', 'maintenanceGateVerified', 'independentVerifierVerified']) {
    requireBoolean(controls, key, 'controls', blockers)
  }
}

export function validateTestRehearsalEvidence(evidence) {
  const blockers = []
  if (!isRecord(evidence)) {
    throw new TestRehearsalEvidenceError('evidence 必须是 JSON 对象')
  }
  if (evidence.schemaVersion !== 1) addBlocker(blockers, 'schemaVersion', '必须为 1')
  validateTarget(evidence, blockers)
  validateRelease(evidence, blockers)
  validateRuntime(evidence, blockers)
  validateSchema(evidence, blockers)
  validateAccounts(evidence, blockers)
  validateEnvironment(evidence, blockers)
  validateRedis(evidence, blockers)
  validateSmoke(evidence, blockers)
  validateControls(evidence, blockers)
  return {
    status: blockers.length === 0 ? 'passed' : 'blocked',
    blockers
  }
}

async function main() {
  const args = process.argv.slice(2)
  if (args.some((arg) => arg.startsWith('--'))) {
    throw new TestRehearsalEvidenceError('不接受任何选项；该校验器只读取证据文件，不提供 apply/迁移/清理操作')
  }
  const positional = args
  if (positional.length !== 1) {
    throw new TestRehearsalEvidenceError('用法：node scripts/validate-test-rehearsal.mjs <test-rehearsal-evidence.json>')
  }
  let evidence
  try {
    evidence = JSON.parse(await readFile(path.resolve(positional[0]), 'utf8'))
  } catch (error) {
    throw new TestRehearsalEvidenceError(`无法读取有效 JSON：${error instanceof Error ? error.message : String(error)}`)
  }
  const report = validateTestRehearsalEvidence(evidence)
  process.stdout.write(`${JSON.stringify(report, null, 2)}\n`)
  if (report.status !== 'passed') process.exitCode = 3
}

const isDirectRun = process.argv[1]
  && path.resolve(process.argv[1]) === path.resolve(fileURLToPath(import.meta.url))

if (isDirectRun) {
  main().catch((error) => {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`)
    process.exitCode = 2
  })
}
