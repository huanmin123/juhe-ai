import assert from 'node:assert/strict'

import {
  ACCOUNT_SYNC_EVIDENCE_TABLE_NAMES,
  AUXILIARY_RUNTIME_RESET_EVIDENCE_TABLE_NAMES,
  RUNTIME_RESET_EVIDENCE_TABLE_NAMES,
  TestRehearsalEvidenceError,
  validateTestRehearsalEvidence
} from './validate-test-rehearsal.mjs'

const validEvidence = {
  schemaVersion: 1,
  target: { environment: 'juhe-ai-test', namespace: 'juhe-ai-test' },
  release: {
    sourceCommit: 'd45c0c8ef',
    releaseMode: 'single-active-stop',
    nodeDigest: `sha256:${'1'.repeat(64)}`,
    jobsDigest: `sha256:${'2'.repeat(64)}`,
    gatewayDigest: `sha256:${'3'.repeat(64)}`,
    sameDigestInTestAndProd: true,
    evidenceRefs: ['release/test-prod-digests.json'],
    test: {
      sourceCommit: 'd45c0c8ef',
      nodeDigest: `sha256:${'1'.repeat(64)}`,
      jobsDigest: `sha256:${'2'.repeat(64)}`,
      gatewayDigest: `sha256:${'3'.repeat(64)}`,
      evidenceRefs: ['runtime/test-pod-images.json']
    },
    prod: {
      sourceCommit: 'd45c0c8ef',
      nodeDigest: `sha256:${'1'.repeat(64)}`,
      jobsDigest: `sha256:${'2'.repeat(64)}`,
      gatewayDigest: `sha256:${'3'.repeat(64)}`,
      evidenceRefs: ['runtime/prod-pod-images.json']
    }
  },
  runtime: {
    evidenceRefs: ['runtime/argo-pod-health.json'],
    argo: { syncStatus: 'Synced', healthStatus: 'Healthy' },
    active: { slot: 'a', replicas: 1, readyContainers: 3, containerCount: 3 },
    standby: { replicas: 0 },
    stableServiceSlots: ['a'],
    owner: { activeOwnerCount: 1, jobsConsumerCount: 1, leaseVerified: true },
    health: { nodeDbReady: true, nodeApi: true, jobs: true, gateway: true, f3: true, f4: true }
  },
  schema: {
    evidenceRefs: ['schema/three-way-diff.json'],
    threeWayStatus: 'passed',
    candidateContractVersion: 96,
    productionSnapshotReadOnly: true,
    testCloneApplied: true,
    testReadbackVerified: true,
    productionPreflightVerified: true,
    tablesColumnsConstraintsIndexesIncluded: true,
    aclRolesExtensionsIncluded: true,
    functionsTriggersViewsPartitionsSequencesIncluded: true,
    productionSnapshotDigest: 'a'.repeat(64),
    testBaselineDigest: 'b'.repeat(64),
    candidateContractDigest: 'c'.repeat(64),
    approvedForwardDeltas: [{
      id: 'delta-accounts-index',
      approvedBy: 'schema-owner@example',
      changeType: 'additive',
      objects: ['juhe_business.accounts.idx_accounts_name_c_trgm_lookup'],
      approvedAt: '2026-09-02T01:00:00Z',
      evidenceRef: 'schema/approved-delta-accounts-index.json'
    }]
  },
  accounts: {
    evidenceRefs: ['accounts/sync-summary.json'],
    status: 'passed',
    credentialsPolicy: 'test-only-equivalent',
    credentialsEvidenceRef: 'accounts/credentials-policy.json',
    approvedCanaryAccountIdsHash: 'd'.repeat(64),
    closureEvidenceRef: 'accounts/closure.json',
    sourceAccountIdsHash: 'e'.repeat(64),
    systemAccountIdsHash: 'f'.repeat(64),
    groupIdsHash: 'a'.repeat(64),
    routeStrategyIdsHash: 'b'.repeat(64),
    resourceAuthorizationIdsHash: 'c'.repeat(64),
    apiKeyRemapHash: 'd'.repeat(64),
    approvedCanaryCount: 1,
    productionSecretReused: false,
    rawProductionCredentialsCopied: false,
    productionApiKeysCopied: false,
    runtimeStateCopied: false,
    fieldLevelTransformVerified: true,
    apiKeysRegenerated: true,
    runtimeStateReset: true,
    schedulesDisabledUntilSmoke: true,
    systemAccountRequiredFieldsVerified: true,
    sourceAccountDependencyOrderVerified: true,
    foreignKeysVerified: true,
    allowlistClosed: true,
    accountScheduleStatusEventsRows: 0,
    apiKeyScheduleStatusEventsRows: 0,
    tables: [
      {
        name: 'system_accounts',
        rows: 1,
        importOrder: 1,
        transformation: 'required-fields-generated; password-replaced',
        requiredNotNullColumns: [
          'id', 'username', 'display_name', 'role', 'status', 'password_hash',
          'must_change_password', 'image_generation_enabled', 'created_at', 'updated_at'
        ],
        copiedColumns: ['id', 'username', 'display_name', 'description', 'role', 'status', 'image_generation_enabled', 'ai_account_limit', 'request_limits_json'],
        generatedColumns: ['password_hash', 'must_change_password', 'created_at', 'updated_at'],
        clearedColumns: ['last_login_at'],
        conflictStrategy: 'upsert-by-id'
      },
      {
        name: 'resource_authorizations',
        rows: 1,
        importOrder: 2,
        transformation: 'allowlist-copy',
        copiedColumns: ['id', 'resource_type', 'resource_id', 'resource_owner_system_account_id', 'grantee_system_account_id', 'scope', 'status', 'created_by', 'created_at', 'updated_at'],
        generatedColumns: [],
        clearedColumns: [],
        conflictStrategy: 'upsert-by-id'
      },
      {
        name: 'accounts',
        rows: 1,
        importOrder: 3,
        transformation: 'source-then-authorization; credential-reencrypt',
        requiredNotNullColumns: [
          'id', 'config_revision', 'dispatch_revision', 'circuit_projection_revision', 'system_account_id',
          'provider_code', 'provider_protocol_profile_id', 'protocol_code', 'protocol_version', 'name', 'type',
          'status', 'credentials_encrypted', 'credential_mask', 'oauth_refresh_token_present', 'concurrency_limit',
          'priority', 'super_priority_enabled', 'fallback_enabled', 'client_compatibility', 'schedulable',
          'cooldown_retest_failure_count',
          'temporary_unavailable_continuous_probe_enabled', 'health_check_model', 'health_check_endpoint_mode',
          'health_check_failure_count', 'stream_failure_count', 'balance_query_enabled', 'balance_query_config_json', 'created_at', 'updated_at'
        ],
        selfForeignKeyPolicy: 'source-before-authorization-instance',
        sourceAccountRows: 1,
        authorizationInstanceRows: 0,
        copiedColumns: [
          'id', 'config_revision', 'dispatch_revision', 'circuit_projection_revision', 'system_account_id',
          'provider_code', 'provider_protocol_profile_id', 'protocol_code', 'protocol_version', 'name', 'type',
          'status', 'credential_mask', 'oauth_refresh_token_present', 'concurrency_limit', 'priority',
          'super_priority_enabled', 'fallback_enabled', 'client_compatibility', 'schedulable',
          'temporary_unavailable_continuous_probe_enabled', 'health_check_model', 'health_check_endpoint_mode',
          'health_check_failure_count', 'balance_query_enabled', 'balance_query_config_json',
          'authorization_instance_source_account_id', 'authorization_instance_authorization_id',
          'authorization_instance_owner_system_account_id'
        ],
        generatedColumns: ['credentials_encrypted', 'cooldown_retest_failure_count', 'stream_failure_count', 'created_at', 'updated_at'],
        clearedColumns: [
          'last_used_at', 'cooldown_until', 'last_error_code', 'last_error_message', 'last_error_trace_id',
          'cooldown_retest_observation_started_at', 'cooldown_retest_generation',
          'cooldown_retest_last_at', 'cooldown_retest_last_status_code', 'last_health_check_at',
          'next_health_check_at', 'last_health_success_at', 'health_check_failure_started_at',
          'last_health_check_status_code', 'last_health_check_error_code', 'last_health_check_error_message',
          'last_health_check_trace_id', 'stream_failure_window_started_at',
          'balance_query_next_refresh_at', 'deleted_at', 'deleted_by'
        ],
        conflictStrategy: 'source-topological-upsert'
      },
      {
        name: 'model_quality_schedules',
        rows: 0,
        importOrder: 4,
        transformation: 'canary-only; disabled-until-smoke; next-run-controlled',
        canaryOnly: true,
        disabledUntilSmoke: true,
        nextRunAtControlled: true,
        copiedColumns: [],
        generatedColumns: [],
        clearedColumns: ['*'],
        conflictStrategy: 'skip-unapproved'
      },
      {
        name: 'account_schedule_status_events',
        rows: 0,
        importOrder: 90,
        transformation: 'structure-only; runtime-reset',
        copiedColumns: [],
        generatedColumns: [],
        clearedColumns: ['*'],
        conflictStrategy: 'truncate-test-runtime'
      },
      {
        name: 'api_key_schedule_status_events',
        rows: 0,
        importOrder: 91,
        transformation: 'structure-only; runtime-reset',
        copiedColumns: [],
        generatedColumns: [],
        clearedColumns: ['*'],
        conflictStrategy: 'truncate-test-runtime'
      }
    ]
  },
  environment: {
    evidenceRefs: ['environment/pod-env-diff.json'],
    status: 'passed',
    sameProductionSemantics: true,
    secretValuesNotRecorded: true,
    cookieSecureResolvedProduction: true,
    unexpectedDiffs: [],
    permittedDiffs: ['database endpoint', 'instance id']
  },
  redis: {
    evidenceRefs: ['redis/acl-isolation.json'],
    status: 'passed',
    physicalEndpointDistinct: true,
    logicalDbDistinct: true,
    namespaceDistinct: true,
    aclUserDistinct: true,
    forbiddenCommandsDenied: true,
    crossEnvironmentKeysZero: true,
    persistenceAndCapacityVerified: true,
    sharedDefaultUser: false
  },
  smoke: {
    evidenceRefs: ['smoke/canary-run.json'],
    status: 'passed',
    testLogin: true,
    approvedUpstreamCanary: true,
    ordinaryGatewayRequest: true,
    sse: true,
    upload: true,
    businessWriteRead: true,
    j1J2OutcomeReadback: true,
    errorPathAndRecovery: true,
    structuredLogs: true,
    modelCheckMode: 'disabled',
    modelCheckCompatibilityAcknowledged: true
  },
  controls: {
    evidenceRefs: ['controls/maintenance-owner-verifier.json'],
    verifierIdentity: 'release-verifier@example',
    verifiedAt: '2026-09-02T02:00:00Z',
    evidenceManifestDigest: 'e'.repeat(64),
    singleActiveGitOpsVerified: true,
    maintenanceGateVerified: true,
    independentVerifierVerified: true
  }
}

for (const environment of ['test', 'prod']) {
  validEvidence.release[environment].imageResolution = {
    'node-runtime': {
      requestedDigest: validEvidence.release.nodeDigest,
      registryManifestDigest: validEvidence.release.nodeDigest,
      resolvedPlatformManifestDigest: `sha256:${'4'.repeat(64)}`,
      runtimeImageID: `sha256:${'4'.repeat(64)}`,
      runtimeImageIDKind: 'index',
      runtimeImageIDPlatformManifestDigest: `sha256:${'4'.repeat(64)}`,
      platform: 'linux/amd64',
      evidenceRef: `runtime/${environment}-node-image-resolution.json`
    },
    'go-jobs': {
      requestedDigest: validEvidence.release.jobsDigest,
      registryManifestDigest: validEvidence.release.jobsDigest,
      resolvedPlatformManifestDigest: `sha256:${'5'.repeat(64)}`,
      runtimeImageID: `sha256:${'5'.repeat(64)}`,
      runtimeImageIDKind: 'index',
      runtimeImageIDPlatformManifestDigest: `sha256:${'5'.repeat(64)}`,
      platform: 'linux/amd64',
      evidenceRef: `runtime/${environment}-jobs-image-resolution.json`
    },
    'go-gateway': {
      requestedDigest: validEvidence.release.gatewayDigest,
      registryManifestDigest: validEvidence.release.gatewayDigest,
      resolvedPlatformManifestDigest: `sha256:${'6'.repeat(64)}`,
      runtimeImageID: `sha256:${'6'.repeat(64)}`,
      runtimeImageIDKind: 'index',
      runtimeImageIDPlatformManifestDigest: `sha256:${'6'.repeat(64)}`,
      platform: 'linux/amd64',
      evidenceRef: `runtime/${environment}-gateway-image-resolution.json`
    }
  }
}

// 其余白名单表使用最小的结构化占位，确保验证器测试覆盖完整闭包而非仅六张关键表。
for (const [index, name] of ACCOUNT_SYNC_EVIDENCE_TABLE_NAMES.entries()) {
  if (!validEvidence.accounts.tables.some(table => table.name === name)) {
    validEvidence.accounts.tables.push({
      name,
      rows: 0,
      importOrder: index + 10,
      transformation: 'allowlist-copy-or-runtime-policy',
      copiedColumns: [],
      generatedColumns: [],
      clearedColumns: [],
      conflictStrategy: 'upsert-by-id'
    })
  }
}
for (const [index, table] of validEvidence.accounts.tables.entries()) {
  table.sourceRows = table.rows
  table.targetRows = table.rows
  table.sourceChecksum = `${String.fromCharCode(97 + (index % 6))}`.repeat(64)
  table.targetChecksum = `${String.fromCharCode(102 - (index % 6))}`.repeat(64)
  table.evidenceRefs = [`accounts/tables/${table.name}.json`]
}
validEvidence.accounts.runtimeResetTables = RUNTIME_RESET_EVIDENCE_TABLE_NAMES.map((name, index) => ({
  name,
  beforeRows: 0,
  afterRows: 0,
  checksum: `${String.fromCharCode(97 + (index % 6))}`.repeat(64),
  evidenceRefs: [`accounts/runtime-reset/${name}.json`]
}))
validEvidence.accounts.auxiliaryRuntimeResetTables = AUXILIARY_RUNTIME_RESET_EVIDENCE_TABLE_NAMES.map((name, index) => ({
  name,
  beforeRows: 0,
  afterRows: 0,
  checksum: `${String.fromCharCode(97 + (index % 6))}`.repeat(64),
  evidenceRefs: [`accounts/runtime-reset/${name.replace('.', '_')}.json`]
}))

assert.deepEqual(validateTestRehearsalEvidence(validEvidence), { status: 'passed', blockers: [] })

const redisUnsafe = structuredClone(validEvidence)
redisUnsafe.redis.aclUserDistinct = false
redisUnsafe.redis.forbiddenCommandsDenied = false
redisUnsafe.redis.sharedDefaultUser = true
assert.equal(validateTestRehearsalEvidence(redisUnsafe).status, 'blocked')
assert.match(validateTestRehearsalEvidence(redisUnsafe).blockers.join('\n'), /redis\.aclUserDistinct|FLUSHALL|FLUSHDB|default 用户/)

const missingEvidenceReference = structuredClone(validEvidence)
delete missingEvidenceReference.release.evidenceRefs
assert.equal(validateTestRehearsalEvidence(missingEvidenceReference).status, 'blocked')

const unsafeEvidenceReference = structuredClone(validEvidence)
unsafeEvidenceReference.release.evidenceRefs = ['../outside-controlled-evidence.json']
assert.equal(validateTestRehearsalEvidence(unsafeEvidenceReference).status, 'blocked')

const unsafeSchemaDeltaReference = structuredClone(validEvidence)
unsafeSchemaDeltaReference.schema.approvedForwardDeltas[0].evidenceRef = '/tmp/approval.json'
assert.equal(validateTestRehearsalEvidence(unsafeSchemaDeltaReference).status, 'blocked')

const missingVerifierBinding = structuredClone(validEvidence)
delete missingVerifierBinding.controls.evidenceManifestDigest
assert.equal(validateTestRehearsalEvidence(missingVerifierBinding).status, 'blocked')
assert.match(validateTestRehearsalEvidence(missingVerifierBinding).blockers.join('\n'), /evidenceManifestDigest/)

const missingRuntimeEvidenceReference = structuredClone(validEvidence)
delete missingRuntimeEvidenceReference.accounts.runtimeResetTables[0].evidenceRefs
assert.equal(validateTestRehearsalEvidence(missingRuntimeEvidenceReference).status, 'blocked')
assert.match(validateTestRehearsalEvidence(missingRuntimeEvidenceReference).blockers.join('\n'), /runtimeResetTables\[0\].evidenceRefs/)

const mismatchedObservedDigest = structuredClone(validEvidence)
mismatchedObservedDigest.release.prod.gatewayDigest = `sha256:${'4'.repeat(64)}`
assert.equal(validateTestRehearsalEvidence(mismatchedObservedDigest).status, 'blocked')

const missingImageResolution = structuredClone(validEvidence)
delete missingImageResolution.release.test.imageResolution
assert.equal(validateTestRehearsalEvidence(missingImageResolution).status, 'blocked')
assert.match(validateTestRehearsalEvidence(missingImageResolution).blockers.join('\n'), /imageResolution/)

const unresolvedImageIndex = structuredClone(validEvidence)
unresolvedImageIndex.release.test.imageResolution['go-jobs'].registryManifestDigest = `sha256:${'7'.repeat(64)}`
assert.equal(validateTestRehearsalEvidence(unresolvedImageIndex).status, 'blocked')
assert.match(validateTestRehearsalEvidence(unresolvedImageIndex).blockers.join('\n'), /registryManifestDigest/)

const mismatchedRuntimeResolution = structuredClone(validEvidence)
mismatchedRuntimeResolution.release.test.imageResolution['go-gateway'].runtimeImageIDPlatformManifestDigest = `sha256:${'8'.repeat(64)}`
assert.equal(validateTestRehearsalEvidence(mismatchedRuntimeResolution).status, 'blocked')
assert.match(validateTestRehearsalEvidence(mismatchedRuntimeResolution).blockers.join('\n'), /runtimeImageIDPlatformManifestDigest/)

const unapprovedEnvironmentDiff = structuredClone(validEvidence)
unapprovedEnvironmentDiff.environment.permittedDiffs.push('temporary allowlist')
assert.equal(validateTestRehearsalEvidence(unapprovedEnvironmentDiff).status, 'blocked')

const unapprovedSchemaDelta = structuredClone(validEvidence)
unapprovedSchemaDelta.schema.approvedForwardDeltas = [{ id: 'x', approvedBy: 'reviewer' }]
assert.equal(validateTestRehearsalEvidence(unapprovedSchemaDelta).status, 'blocked')

const missingSchemaDeltaApproval = structuredClone(validEvidence)
missingSchemaDeltaApproval.schema.approvedForwardDeltas = []
assert.equal(validateTestRehearsalEvidence(missingSchemaDeltaApproval).status, 'blocked')
assert.match(validateTestRehearsalEvidence(missingSchemaDeltaApproval).blockers.join('\n'), /三方 schema digest.*至少列出一条/)

const copiedSecrets = structuredClone(validEvidence)
copiedSecrets.accounts.productionSecretReused = true
copiedSecrets.accounts.productionApiKeysCopied = true
assert.equal(validateTestRehearsalEvidence(copiedSecrets).status, 'blocked')

const invalidCanaryApproval = structuredClone(validEvidence)
invalidCanaryApproval.accounts.approvedCanaryAccountIdsHash = 'not-a-sha256'
invalidCanaryApproval.accounts.approvedCanaryCount = 0
assert.equal(validateTestRehearsalEvidence(invalidCanaryApproval).status, 'blocked')
assert.match(validateTestRehearsalEvidence(invalidCanaryApproval).blockers.join('\n'), /approvedCanaryAccountIdsHash|approvedCanaryCount/)

const missingCredentialsEvidence = structuredClone(validEvidence)
delete missingCredentialsEvidence.accounts.credentialsEvidenceRef
assert.equal(validateTestRehearsalEvidence(missingCredentialsEvidence).status, 'blocked')
assert.match(validateTestRehearsalEvidence(missingCredentialsEvidence).blockers.join('\n'), /credentialsEvidenceRef/)

const missingClosureEvidence = structuredClone(validEvidence)
delete missingClosureEvidence.accounts.closureEvidenceRef
delete missingClosureEvidence.accounts.apiKeyRemapHash
assert.equal(validateTestRehearsalEvidence(missingClosureEvidence).status, 'blocked')
assert.match(validateTestRehearsalEvidence(missingClosureEvidence).blockers.join('\n'), /closureEvidenceRef|apiKeyRemapHash/)

const missingRequiredAccountColumn = structuredClone(validEvidence)
missingRequiredAccountColumn.accounts.tables.find(table => table.name === 'system_accounts').generatedColumns = ['created_at', 'updated_at']
assert.equal(validateTestRehearsalEvidence(missingRequiredAccountColumn).status, 'blocked')
assert.match(validateTestRehearsalEvidence(missingRequiredAccountColumn).blockers.join('\n'), /system_accounts.*password_hash/)

const missingAccountTableChecksum = structuredClone(validEvidence)
delete missingAccountTableChecksum.accounts.tables.find(table => table.name === 'accounts').targetChecksum
assert.equal(validateTestRehearsalEvidence(missingAccountTableChecksum).status, 'blocked')
assert.match(validateTestRehearsalEvidence(missingAccountTableChecksum).blockers.join('\n'), /accounts\.tables.*targetChecksum/)

const mismatchedAccountTableRows = structuredClone(validEvidence)
mismatchedAccountTableRows.accounts.tables.find(table => table.name === 'accounts').targetRows += 1
assert.equal(validateTestRehearsalEvidence(mismatchedAccountTableRows).status, 'blocked')
assert.match(validateTestRehearsalEvidence(mismatchedAccountTableRows).blockers.join('\n'), /rows.*targetRows/)

const missingDeclaredNotNullColumn = structuredClone(validEvidence)
missingDeclaredNotNullColumn.accounts.tables.find(table => table.name === 'system_accounts').requiredNotNullColumns = ['id']
assert.equal(validateTestRehearsalEvidence(missingDeclaredNotNullColumn).status, 'blocked')
assert.match(validateTestRehearsalEvidence(missingDeclaredNotNullColumn).blockers.join('\n'), /requiredNotNullColumns.*password_hash/)

const clearedRequiredNotNullColumn = structuredClone(validEvidence)
const clearedAccountsTable = clearedRequiredNotNullColumn.accounts.tables.find(table => table.name === 'accounts')
clearedAccountsTable.generatedColumns = clearedAccountsTable.generatedColumns.filter(column => column !== 'cooldown_retest_failure_count')
clearedAccountsTable.clearedColumns.push('cooldown_retest_failure_count')
assert.equal(validateTestRehearsalEvidence(clearedRequiredNotNullColumn).status, 'blocked')
assert.match(validateTestRehearsalEvidence(clearedRequiredNotNullColumn).blockers.join('\n'), /cooldown_retest_failure_count.*不得只清空/)

const clearedSourceForeignKey = structuredClone(validEvidence)
clearedSourceForeignKey.accounts.tables.find(table => table.name === 'accounts').clearedColumns.push('authorization_instance_source_account_id')
assert.equal(validateTestRehearsalEvidence(clearedSourceForeignKey).status, 'blocked')
assert.match(validateTestRehearsalEvidence(clearedSourceForeignKey).blockers.join('\n'), /authorization_instance_source_account_id/)

const unsafeSourceOrder = structuredClone(validEvidence)
const unsafeAccountsTable = unsafeSourceOrder.accounts.tables.find(table => table.name === 'accounts')
unsafeAccountsTable.selfForeignKeyPolicy = 'single-transaction'
unsafeAccountsTable.sourceAccountRows = 0
unsafeAccountsTable.authorizationInstanceRows = 1
assert.equal(validateTestRehearsalEvidence(unsafeSourceOrder).status, 'blocked')
assert.match(validateTestRehearsalEvidence(unsafeSourceOrder).blockers.join('\n'), /selfForeignKeyPolicy|source account/)

const duplicateImportOrder = structuredClone(validEvidence)
duplicateImportOrder.accounts.tables.find(table => table.name === 'model_quality_schedules').importOrder = 3
assert.equal(validateTestRehearsalEvidence(duplicateImportOrder).status, 'blocked')
assert.match(validateTestRehearsalEvidence(duplicateImportOrder).blockers.join('\n'), /importOrder.*唯一/)

const wrongTarget = structuredClone(validEvidence)
wrongTarget.target.environment = 'juhe-ai-prod'
assert.equal(validateTestRehearsalEvidence(wrongTarget).status, 'blocked')

const wrongReleaseMode = structuredClone(validEvidence)
wrongReleaseMode.release.releaseMode = 'long-running-uat-standby'
assert.equal(validateTestRehearsalEvidence(wrongReleaseMode).status, 'blocked')

assert.throws(() => validateTestRehearsalEvidence(null), TestRehearsalEvidenceError)

console.log('test rehearsal evidence validator passed')
