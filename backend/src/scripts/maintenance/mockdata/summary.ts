import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'

import type { GroupSummary, ResourceAuthorizationSummary, SystemAccountSummary } from '../../../domain/types.js'
import { runtimeConfig } from '../../../config/runtime.js'
import { nowIso } from '../../../storage/database.js'
import type { ModelCheckMockdataCounts } from './records/model-checks.js'
import {
  apiKeyAuthorizedGroupBindingRule,
  idPrefix,
  mockPassword,
  type ApiKeyWithSecret,
  type CreatedMockdata,
  type ExtraMockdataCounts,
  type MockApiKeys,
  type MockdataOptions,
  type MockGroups,
  type MockSystemAccounts,
  type UsageRecordSeed
} from './shared.js'

function mockUserSummaries(users: MockSystemAccounts): Array<Record<string, unknown>> {
  return Object.entries(users)
    .filter(([name]) => name !== 'admin')
    .map(([name, user]) => ({
      name,
      id: user.id,
      username: user.username,
      displayName: user.displayName,
      role: user.role,
      status: user.status,
      password: mockPassword
    }))
}

function mockGroupById(groups: MockGroups): Map<string, GroupSummary> {
  return new Map<string, GroupSummary>(Object.values(groups).map((group) => [group.id, group]))
}

function mockGroupOwnerById(groups: MockGroups, users: MockSystemAccounts): Map<string, SystemAccountSummary> {
  return new Map<string, SystemAccountSummary>([
    [groups.main.id, users.admin],
    [groups.highConcurrency.id, users.admin],
    [groups.backup.id, users.admin],
    [groups.oauth.id, users.admin],
    [groups.openaiCompatible.id, users.admin],
    [groups.experiment.id, users.admin],
    [groups.empty.id, users.admin],
    [groups.managerMain.id, users.manager],
    [groups.managerHighConcurrency.id, users.manager],
    [groups.adminGrantedDev.id, users.dev],
    [groups.adminGrantedOps.id, users.ops],
    [groups.adminGrantedTester.id, users.tester],
    [groups.managerDefault.id, users.manager],
    [groups.devDefault.id, users.dev],
    [groups.opsDefault.id, users.ops],
    [groups.testerDefault.id, users.tester],
    [groups.financeDefault.id, users.finance],
    [groups.viewerDefault.id, users.viewer]
  ])
}

function mockApiKeyOwnerByName(name: string, users: MockSystemAccounts): SystemAccountSummary | undefined {
  if (name.startsWith('admin')) return users.admin
  if (name.startsWith('manager')) return users.manager
  if (name.startsWith('dev')) return users.dev
  if (name.startsWith('tester')) return users.tester
  if (name.startsWith('ops')) return users.ops
  if (name.startsWith('finance')) return users.finance
  if (name.startsWith('viewer')) return users.viewer
  return undefined
}

function apiKeySummariesForMockdata(
  apiKeys: MockApiKeys,
  groupById: Map<string, GroupSummary>,
  groupOwnerById: Map<string, SystemAccountSummary>,
  users: MockSystemAccounts
): Array<Record<string, unknown>> {
  return (Object.entries(apiKeys) as Array<[string, ApiKeyWithSecret]>).map(([name, key]) => {
    const keyOwner = mockApiKeyOwnerByName(name, users)
    return {
      name,
      id: key.id,
      label: key.name,
      description: key.description,
      ownerSystemAccountId: key.systemAccountId ?? keyOwner?.id,
      ownerSystemAccountName: key.systemAccountName ?? keyOwner?.displayName,
      bindingScope: 'visible_group',
      bindingRule: apiKeyAuthorizedGroupBindingRule,
      routeStrategyId: key.routeStrategyId,
      routeStrategyName: key.routeStrategyName,
      routeStrategyMode: key.routeStrategyMode,
      routeStrategyStatus: key.routeStrategyStatus,
      status: key.status,
      expiresAt: key.expiresAt,
      key: key.key
    }
  })
}

function groupAuthorizationSamples(authorizations: ResourceAuthorizationSummary[]): Array<Record<string, unknown>> {
  return authorizations
    .filter((authorization) => authorization.resourceType === 'group')
    .map((authorization) => ({
      id: authorization.id,
      resourceType: authorization.resourceType,
      resourceId: authorization.resourceId,
      resourceName: authorization.resourceName,
      resourceOwnerSystemAccountId: authorization.resourceOwnerSystemAccountId,
      resourceOwnerSystemAccountName: authorization.resourceOwnerSystemAccountName,
      granteeType: authorization.granteeType,
      granteeSystemAccountId: authorization.granteeSystemAccountId,
      granteeSystemAccountName: authorization.granteeSystemAccountName,
      granteeUsername: authorization.granteeUsername,
      granteeTeamId: authorization.granteeTeamId,
      granteeTeamName: authorization.granteeTeamName,
      status: authorization.status,
      remark: authorization.remark,
      expiresAt: authorization.expiresAt,
      bindableToApiKey: authorization.status === 'active',
      bindingRule: apiKeyAuthorizedGroupBindingRule
    }))
}

export function writeSummary(
  created: CreatedMockdata,
  records: UsageRecordSeed[],
  auditLogs: number,
  modelCheckCounts: ModelCheckMockdataCounts,
  extraCounts: ExtraMockdataCounts,
  options: MockdataOptions,
  durationMs: number
): void {
  const groupById = mockGroupById(created.groups)
  const groupOwnerById = mockGroupOwnerById(created.groups, created.users)
  const summary = {
    generatedAt: nowIso(),
    durationMs,
    options,
    owner: {
      id: created.users.admin.id,
      username: created.users.admin.username,
      displayName: created.users.admin.displayName
    },
    mockUserPassword: mockPassword,
    mockUsers: mockUserSummaries(created.users),
    apiKeyBindingRule: apiKeyAuthorizedGroupBindingRule,
    authorizedUsageRecordNote: 'usage_records 中的 group_authorized 样本用于授权分组直接作为 API Key 号池时的调度、审计和授权用量统计。',
    upstreamResponseModelSamples: records
      .filter((record) => record.id === `${idPrefix}usage_coverage_upstream_response_model_match`
        || record.id === `${idPrefix}usage_coverage_upstream_response_model_mismatch`
        || record.id === `${idPrefix}usage_coverage_upstream_response_model_unmapped_mismatch`)
      .map((record) => ({
        id: record.id,
        traceId: record.traceId,
        endpoint: record.endpoint,
        model: record.model,
        upstreamModel: record.upstreamModel,
        upstreamResponseModel: record.upstreamResponseModel,
        modelMappingApplied: record.modelMappingApplied
      })),
    apiKeys: apiKeySummariesForMockdata(created.apiKeys, groupById, groupOwnerById, created.users),
    authorizationSamples: groupAuthorizationSamples(created.authorizations),
    counts: {
      users: Object.keys(created.users).length - 1,
      groups: Object.keys(created.groups).length,
      accounts: Object.keys(created.accounts).length,
      apiKeys: Object.keys(created.apiKeys).length,
      teams: Object.keys(created.teams).length,
      authorizations: created.authorizations.length,
      externalSources: Object.keys(created.externalSources).length,
      responseInspectionPolicies: created.responseInspectionPolicies,
      customProviderModels: created.customProviderModels,
      usageRecords: records.length,
      publicApiLogs: extraCounts.publicApiLogs,
      auditLogs,
      operationLogs: 90,
      modelCheckRuns: modelCheckCounts.runs,
      modelCheckItems: modelCheckCounts.items,
      accountCleanupTargets: extraCounts.accountCleanupTargets,
      apiKeyCleanupTargets: extraCounts.apiKeyCleanupTargets,
      clientIpAggregatedRecords: extraCounts.clientIpAggregatedRecords,
      clientIpPolicies: extraCounts.clientIpPolicies,
      clientIpPolicyHits: extraCounts.clientIpPolicyHits
    }
  }
  const path = mockdataSummaryPath()
  mkdirSync(dirname(path), { recursive: true })
  writeFileSync(path, `${JSON.stringify(summary, null, 2)}\n`, 'utf8')
}

export function mockdataSummaryPath(): string {
  return join(dirname(runtimeConfig.databasePath), 'mockdata-summary.json')
}
