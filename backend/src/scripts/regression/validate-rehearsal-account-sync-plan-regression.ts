import assert from 'node:assert/strict'

import {
  ACCOUNT_SYNC_TABLE_POLICIES,
  ACCOUNT_SYNC_RUNTIME_RESET_TABLES,
  AUXILIARY_RUNTIME_RESET_TABLES,
  type AccountSyncPreflightReport,
  type AccountSyncTableReport,
  type AccountSyncColumnMetadata,
  type AccountSyncForeignKey
} from '../operations/rehearsal-account-sync-preflight.js'
import {
  type RehearsalAccountSyncPlan,
  type RehearsalAccountSyncTablePlan,
  sha256,
  validateRehearsalAccountSyncPlanBinding,
  validateRehearsalAccountSyncPlan
} from '../operations/validate-rehearsal-account-sync-plan.js'

const commonColumns = [
  'id', 'created_at', 'updated_at'
]
const systemAccountColumns = [
  'id', 'username', 'display_name', 'role', 'status', 'password_hash',
  'must_change_password', 'image_generation_enabled', 'created_at', 'updated_at'
]
const accountColumns = [
  'id', 'config_revision', 'dispatch_revision', 'circuit_projection_revision', 'system_account_id',
  'provider_code', 'provider_protocol_profile_id', 'protocol_code', 'protocol_version', 'name', 'type',
  'status', 'credentials_encrypted', 'credential_fingerprint', 'credential_mask', 'oauth_access_token_expires_at', 'oauth_refresh_token_present', 'concurrency_limit',
  'priority', 'super_priority_enabled', 'fallback_enabled', 'client_compatibility', 'schedulable',
  'availability_schedule_json', 'availability_schedule_next_check_at', 'account_expires_at',
  'cooldown_retest_failure_count', 'temporary_unavailable_continuous_probe_enabled', 'health_check_model',
  'health_check_endpoint_mode', 'health_check_failure_count', 'stream_failure_count', 'balance_query_enabled',
  'balance_query_config_json', 'created_at', 'updated_at', 'proxy_profile_id', 'authorization_instance_source_account_id',
  'authorization_instance_authorization_id', 'authorization_instance_owner_system_account_id', 'last_used_at',
  'cooldown_until', 'last_error_code', 'last_error_message', 'last_error_trace_id',
  'cooldown_retest_observation_started_at', 'cooldown_retest_generation', 'cooldown_retest_last_at',
  'cooldown_retest_last_status_code', 'last_health_check_at', 'next_health_check_at', 'last_health_success_at',
  'health_check_failure_started_at', 'last_health_check_status_code', 'last_health_check_error_code',
  'last_health_check_error_message', 'last_health_check_trace_id', 'stream_failure_window_started_at',
  'balance_query_next_refresh_at', 'deleted_at', 'deleted_by'
]
const scheduleColumns = [
  'id', 'system_account_id', 'account_id', 'model', 'interval_minutes', 'profile', 'penalty_threshold',
  'penalty_action', 'recovery_interval_minutes', 'enabled', 'revision', 'next_run_at', 'last_run_id',
  'last_run_at', 'last_run_status', 'lease_owner', 'lease_until', 'created_at', 'updated_at'
]

function metadata(name: string, nullable = true): AccountSyncColumnMetadata {
  return { name, dataType: 'text', udtName: 'text', isNullable: nullable, defaultExpression: null }
}

function foreignKey(constraintName: string, parentTable: string): AccountSyncForeignKey {
  return {
    constraintName,
    parentSchema: 'juhe_business',
    parentTable,
    definition: `FOREIGN KEY (parent_id) REFERENCES juhe_business.${parentTable}(id)`
  }
}

function providerSelfForeignKey(): AccountSyncForeignKey {
  return {
    constraintName: 'providers_parent_code_fkey',
    parentSchema: 'juhe_business',
    parentTable: 'providers',
    definition: 'FOREIGN KEY (parent_code) REFERENCES juhe_business.providers(code)'
  }
}

function makeReport(name: string, columns: string[], foreignKeys: AccountSyncForeignKey[] = []): AccountSyncTableReport {
  const sourceColumns = columns.map((column) => metadata(column))
  return {
    name,
    purpose: ACCOUNT_SYNC_TABLE_POLICIES.find((policy) => policy.name === name)?.purpose ?? 'configuration',
    sourceExists: true,
    targetExists: true,
    sourceRows: 1,
    targetRows: 0,
    sourceColumns,
    targetColumns: sourceColumns,
    sourceNotNullColumns: [],
    targetNotNullColumns: [],
    sourceForeignKeys: foreignKeys,
    targetForeignKeys: foreignKeys,
    foreignKeyDifferences: [],
    foreignKeysOutsidePolicy: [],
    missingTargetColumns: [],
    unexpectedTargetColumns: [],
    targetRequiredColumnsWithoutDefault: [],
    columnDifferences: [],
    sensitiveColumns: ACCOUNT_SYNC_TABLE_POLICIES.find((policy) => policy.name === name)?.sensitiveColumns ?? [],
    specialHandling: 'regression fixture'
  }
}

function buildFixture(): { preflight: AccountSyncPreflightReport; plan: RehearsalAccountSyncPlan } {
  const tableReports = ACCOUNT_SYNC_TABLE_POLICIES.map((policy, index) => {
    let columns = commonColumns
    if (policy.name === 'providers') columns = [...commonColumns, 'code', 'parent_code']
    if (policy.name === 'system_accounts') columns = systemAccountColumns
    if (policy.name === 'accounts') columns = accountColumns
    if (policy.name === 'api_keys') columns = [...commonColumns, 'key_secret_encrypted']
    if (policy.name === 'model_quality_schedules') columns = scheduleColumns
    const foreignKeys = policy.name === 'accounts'
      ? [foreignKey('accounts_provider_fkey', 'providers')]
      : policy.name === 'providers'
        ? [providerSelfForeignKey()]
        : []
    return makeReport(policy.name, columns, foreignKeys)
  })
  const preflight: AccountSyncPreflightReport = {
    schemaVersion: 1,
    mode: 'plan',
    source: { databaseName: 'juhe_ai_prod', databaseOid: '1' },
    target: { databaseName: 'juhe_ai_test_rehearsal_regression', databaseOid: '2' },
    targetNameAccepted: true,
    targetNamePattern: '^juhe_ai_test_rehearsal_[a-z0-9_]{3,48}$',
    tableReports,
    runtimeResetReports: ACCOUNT_SYNC_RUNTIME_RESET_TABLES.map((name) => ({
      name,
      sourceExists: true,
      targetExists: true,
      sourceRows: 0,
      targetRows: 0
    })),
    auxiliaryRuntimeResetReports: AUXILIARY_RUNTIME_RESET_TABLES.map(({ schema, name }) => ({
      schema,
      name,
      sourceExists: true,
      targetExists: true,
      sourceRows: 0,
      targetRows: 0
    })),
    blockers: [],
    status: 'passed'
  }
  const tables: RehearsalAccountSyncTablePlan[] = ACCOUNT_SYNC_TABLE_POLICIES.map((policy, index) => {
    const report = tableReports[index]
    const sourceColumns = report.sourceColumns.map((column) => column.name)
    if (policy.purpose === 'runtime-reset') {
      return {
        name: policy.name,
        importOrder: index + 1,
        copiedColumns: [],
        generatedColumns: [],
        clearedColumns: sourceColumns,
        targetExtraColumns: [],
        conflictStrategy: 'truncate-test-runtime'
      }
    }
    if (policy.name === 'system_accounts') {
      return {
        name: policy.name,
        importOrder: index + 1,
        copiedColumns: sourceColumns.filter((column) => column !== 'password_hash'),
        generatedColumns: ['password_hash'],
        clearedColumns: [],
        targetExtraColumns: [],
        conflictStrategy: 'upsert-by-id'
      }
    }
    if (policy.name === 'accounts') {
      const runtimeColumns = new Set([
        'availability_schedule_next_check_at',
        'last_used_at', 'cooldown_until', 'last_error_code', 'last_error_message', 'last_error_trace_id',
        'cooldown_retest_observation_started_at', 'cooldown_retest_generation', 'cooldown_retest_last_at',
        'cooldown_retest_last_status_code', 'last_health_check_at', 'next_health_check_at', 'last_health_success_at',
        'health_check_failure_started_at', 'last_health_check_status_code', 'last_health_check_error_code',
        'last_health_check_error_message', 'last_health_check_trace_id', 'stream_failure_window_started_at',
        'balance_query_next_refresh_at', 'deleted_at', 'deleted_by'
      ])
      return {
        name: policy.name,
        importOrder: index + 1,
        copiedColumns: sourceColumns.filter((column) => ![
          'credentials_encrypted', 'credential_fingerprint', 'credential_mask',
          'oauth_access_token_expires_at', 'oauth_refresh_token_present',
          'availability_schedule_next_check_at', 'cooldown_retest_failure_count', 'stream_failure_count'
        ].includes(column) && !runtimeColumns.has(column)),
        generatedColumns: [
          'credentials_encrypted', 'credential_fingerprint', 'credential_mask',
          'oauth_access_token_expires_at', 'oauth_refresh_token_present',
          'availability_schedule_next_check_at', 'cooldown_retest_failure_count', 'stream_failure_count'
        ],
        clearedColumns: sourceColumns.filter((column) => runtimeColumns.has(column) && column !== 'availability_schedule_next_check_at'),
        targetExtraColumns: [],
        conflictStrategy: 'source-topological-upsert',
        selfForeignKeyPolicy: 'source-before-authorization-instance',
        availabilityScheduleNextCheckAtControlled: true
      }
    }
    if (policy.name === 'providers') {
      return {
        name: policy.name,
        importOrder: index + 1,
        copiedColumns: sourceColumns,
        generatedColumns: [],
        clearedColumns: [],
        targetExtraColumns: [],
        conflictStrategy: 'parent-topological-upsert',
        selfForeignKeyPolicy: 'parent-before-child'
      }
    }
    if (policy.name === 'api_keys') {
      return {
        name: policy.name,
        importOrder: index + 1,
        copiedColumns: sourceColumns.filter((column) => column !== 'key_secret_encrypted'),
        generatedColumns: ['key_secret_encrypted'],
        clearedColumns: [],
        targetExtraColumns: [],
        conflictStrategy: 'regenerate-test-key'
      }
    }
    if (policy.name === 'model_quality_schedules') {
      return {
        name: policy.name,
        importOrder: index + 1,
        copiedColumns: sourceColumns.filter((column) => !['enabled', 'next_run_at', 'last_run_id', 'last_run_at', 'last_run_status', 'lease_owner', 'lease_until'].includes(column)),
        generatedColumns: ['enabled', 'next_run_at'],
        clearedColumns: ['last_run_id', 'last_run_at', 'last_run_status', 'lease_owner', 'lease_until'],
        targetExtraColumns: [],
        conflictStrategy: 'skip-unapproved',
        canaryOnly: true,
        disabledUntilSmoke: true,
        nextRunAtControlled: true
      }
    }
    return {
      name: policy.name,
      importOrder: index + 1,
      copiedColumns: sourceColumns,
      generatedColumns: [],
      clearedColumns: [],
      targetExtraColumns: [],
      conflictStrategy: 'upsert-by-id'
    }
  })
  return {
    preflight,
    plan: {
      schemaVersion: 1,
      mode: 'field-level-plan',
      preflightSha256: '0'.repeat(64),
      credentialsPolicy: 'test-only-equivalent',
      credentialsEvidenceRef: 'accounts/credential-policy.json',
      approvedCanaryAccountIdsHash: '1'.repeat(64),
      approvedCanaryCount: 1,
      tables,
      runtimeResetTables: ACCOUNT_SYNC_RUNTIME_RESET_TABLES.map((name) => ({
        schema: 'juhe_business',
        name,
        action: 'structure-only-clear' as const,
        readbackRequired: true as const,
        checksumRequired: true as const
      })),
      auxiliaryRuntimeResetTables: AUXILIARY_RUNTIME_RESET_TABLES.map(({ schema, name }) => ({
        schema,
        name,
        action: 'structure-only-clear' as const,
        readbackRequired: true as const,
        checksumRequired: true as const
      }))
    }
  }
}

const fixture = buildFixture()
assert.deepEqual(validateRehearsalAccountSyncPlan(fixture.preflight, fixture.plan), { status: 'passed', blockers: [] })
const preflightText = JSON.stringify(fixture.preflight)
fixture.plan.preflightSha256 = sha256(preflightText)
assert.deepEqual(validateRehearsalAccountSyncPlanBinding(preflightText, fixture.preflight, fixture.plan), { status: 'passed', blockers: [] })

const stalePreflightBinding = structuredClone(fixture)
stalePreflightBinding.plan.preflightSha256 = '0'.repeat(64)
assert.equal(validateRehearsalAccountSyncPlanBinding(preflightText, stalePreflightBinding.preflight, stalePreflightBinding.plan).status, 'blocked')
assert.match(validateRehearsalAccountSyncPlanBinding(preflightText, stalePreflightBinding.preflight, stalePreflightBinding.plan).blockers.join('\n'), /preflightSha256/)

const omittedColumn = structuredClone(fixture)
const genericPlan = omittedColumn.plan.tables.find((table) => table.name === 'providers')!
genericPlan.copiedColumns = []
assert.equal(validateRehearsalAccountSyncPlan(omittedColumn.preflight, omittedColumn.plan).status, 'blocked')
assert.match(validateRehearsalAccountSyncPlan(omittedColumn.preflight, omittedColumn.plan).blockers.join('\n'), /providers: 源列 id 未分类/)

const copiedSensitive = structuredClone(fixture)
const copiedAccounts = copiedSensitive.plan.tables.find((table) => table.name === 'accounts')!
copiedAccounts.copiedColumns.push('credentials_encrypted')
assert.equal(validateRehearsalAccountSyncPlan(copiedSensitive.preflight, copiedSensitive.plan).status, 'blocked')
assert.match(validateRehearsalAccountSyncPlan(copiedSensitive.preflight, copiedSensitive.plan).blockers.join('\n'), /敏感列 credentials_encrypted/)

const copiedDerivedCredential = structuredClone(fixture)
const copiedDerivedAccounts = copiedDerivedCredential.plan.tables.find((table) => table.name === 'accounts')!
copiedDerivedAccounts.copiedColumns.push('credential_fingerprint')
assert.equal(validateRehearsalAccountSyncPlan(copiedDerivedCredential.preflight, copiedDerivedCredential.plan).status, 'blocked')
assert.match(validateRehearsalAccountSyncPlan(copiedDerivedCredential.preflight, copiedDerivedCredential.plan).blockers.join('\n'), /敏感列 credential_fingerprint/)

const staleSensitiveManifest = structuredClone(fixture)
const staleSensitiveAccounts = staleSensitiveManifest.preflight.tableReports.find((report) => report.name === 'accounts')!
staleSensitiveAccounts.sensitiveColumns = ['credentials_encrypted', 'credential_mask', 'oauth_access_token_expires_at']
assert.equal(validateRehearsalAccountSyncPlan(staleSensitiveManifest.preflight, staleSensitiveManifest.plan).status, 'blocked')
assert.match(validateRehearsalAccountSyncPlan(staleSensitiveManifest.preflight, staleSensitiveManifest.plan).blockers.join('\n'), /preflight\.sensitiveColumns/)

const copiedScheduleCheckpoint = structuredClone(fixture)
const copiedScheduleAccounts = copiedScheduleCheckpoint.plan.tables.find((table) => table.name === 'accounts')!
copiedScheduleAccounts.copiedColumns.push('availability_schedule_next_check_at')
assert.equal(validateRehearsalAccountSyncPlan(copiedScheduleCheckpoint.preflight, copiedScheduleCheckpoint.plan).status, 'blocked')
assert.match(validateRehearsalAccountSyncPlan(copiedScheduleCheckpoint.preflight, copiedScheduleCheckpoint.plan).blockers.join('\n'), /availability_schedule_next_check_at/)

const missingProviderSelfForeignKeyPolicy = structuredClone(fixture)
delete missingProviderSelfForeignKeyPolicy.plan.tables.find((table) => table.name === 'providers')!.selfForeignKeyPolicy
assert.equal(validateRehearsalAccountSyncPlan(missingProviderSelfForeignKeyPolicy.preflight, missingProviderSelfForeignKeyPolicy.plan).status, 'blocked')
assert.match(validateRehearsalAccountSyncPlan(missingProviderSelfForeignKeyPolicy.preflight, missingProviderSelfForeignKeyPolicy.plan).blockers.join('\n'), /providers\.selfForeignKeyPolicy/)

const outsideForeignKey = structuredClone(fixture)
outsideForeignKey.preflight.tableReports.find((table) => table.name === 'accounts')!.foreignKeysOutsidePolicy = ['source:accounts_external_fkey|juhe_business.external_sources']
assert.equal(validateRehearsalAccountSyncPlan(outsideForeignKey.preflight, outsideForeignKey.plan).status, 'blocked')

const reversedImportOrder = structuredClone(fixture)
const reversedProvider = reversedImportOrder.plan.tables.find((table) => table.name === 'providers')!
const reversedAccounts = reversedImportOrder.plan.tables.find((table) => table.name === 'accounts')!
reversedProvider.importOrder = reversedAccounts.importOrder + 1
assert.equal(validateRehearsalAccountSyncPlan(reversedImportOrder.preflight, reversedImportOrder.plan).status, 'blocked')
assert.match(validateRehearsalAccountSyncPlan(reversedImportOrder.preflight, reversedImportOrder.plan).blockers.join('\n'), /父表 providers 必须先于子表导入/)

const extraRequiredTargetColumn = structuredClone(fixture)
const extraReport = extraRequiredTargetColumn.preflight.tableReports.find((table) => table.name === 'providers')!
extraReport.targetRequiredColumnsWithoutDefault = ['future_required_column']
assert.equal(validateRehearsalAccountSyncPlan(extraRequiredTargetColumn.preflight, extraRequiredTargetColumn.plan).status, 'blocked')
assert.match(validateRehearsalAccountSyncPlan(extraRequiredTargetColumn.preflight, extraRequiredTargetColumn.plan).blockers.join('\n'), /目标存在 source 未覆盖的必填列/)

const missingRuntimeReset = structuredClone(fixture)
missingRuntimeReset.plan.runtimeResetTables.pop()
assert.equal(validateRehearsalAccountSyncPlan(missingRuntimeReset.preflight, missingRuntimeReset.plan).status, 'blocked')
assert.match(validateRehearsalAccountSyncPlan(missingRuntimeReset.preflight, missingRuntimeReset.plan).blockers.join('\n'), /runtimeResetTables: 缺少/)

console.log(`validate rehearsal account sync plan regression passed: ${ACCOUNT_SYNC_TABLE_POLICIES.length} policy tables`)
