import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const projectRoot = resolve(import.meta.dirname, '../../../..')

const datasetSchemaSource = readProjectFile('backend/src/storage/schema/dataset-schema.ts')
const usageRecordTypesSource = readProjectFile('backend/src/storage/usage-records.repository.ts')
const usageRecordMappersSource = readProjectFile('backend/src/storage/usage-record-mappers.ts')
const usageRecordShardsSource = readProjectFile('backend/src/storage/usage-record-shards.ts')
const gatewayUsageRecordsSource = readProjectFile('backend/src/modules/gateway/usage/records.ts')
const auditLogTypesSource = readProjectFile('backend/src/storage/audit-log-types.ts')
const auditLogMappersSource = readProjectFile('backend/src/storage/audit-log-mappers.ts')
const auditLogsRepositorySource = readProjectFile('backend/src/storage/audit-logs.repository.ts')
const auditCaptureSource = readProjectFile('backend/src/modules/gateway/audit/capture.service.ts')
const modelChecksServiceSource = readProjectFile('backend/src/modules/model-checks/model-checks.service.ts')
const postgresSchemaSource = readProjectFile('backend/src/storage/postgres-schema.ts')
const usageRecordDomainTypesSource = readProjectFile('frontend/src/types/domain/usage-records.ts')
const auditLogDomainTypesSource = readProjectFile('frontend/src/types/domain/audit-logs.ts')
const usageCostDetailsSource = readProjectFile('frontend/src/views/usage-records/usageRecordCostDetails.ts')
const auditDetailDrawerSource = readProjectFile('frontend/src/views/audit-logs/AuditLogDetailDrawer.vue')
const accountTestDisplayFormattersSource = readProjectFile('frontend/src/views/accounts/accountTestDisplayFormatters.ts')
const modelChecksViewSource = readProjectFile('frontend/src/views/model-checks/ModelChecksView.vue')

assertIncludes(datasetSchemaSource, 'source_endpoint_family TEXT', '数据集 schema 必须持久化模型映射来源协议族')
assertIncludes(datasetSchemaSource, 'upstream_endpoint_family TEXT', '数据集 schema 必须持久化模型映射上游协议族')
assertIncludes(datasetSchemaSource, 'attempt_source_endpoint_family TEXT', '审计尝试表必须持久化每次尝试的来源协议族')
assertIncludes(datasetSchemaSource, 'attempt_upstream_endpoint_family TEXT', '审计尝试表必须持久化每次尝试的上游协议族')
assertIncludes(postgresSchemaSource, 'applyDatasetSchema', 'PostgreSQL DDL 必须从数据集 schema 生成，确保审计模型映射字段同步到生产')
assertIncludes(postgresSchemaSource, 'applyUsageRecordShardBaseSchema', 'PostgreSQL DDL 必须从 usage shard schema 生成，确保使用记录模型映射字段同步到生产')

assertIncludes(usageRecordTypesSource, 'sourceEndpointFamily?:', 'UsageRecordSummary/Input 必须包含 sourceEndpointFamily')
assertIncludes(usageRecordTypesSource, 'upstreamEndpointFamily?:', 'UsageRecordSummary/Input 必须包含 upstreamEndpointFamily')
assertIncludes(usageRecordMappersSource, 'sourceEndpointFamily: optionalString(row.source_endpoint_family)', '使用记录 mapper 必须返回 sourceEndpointFamily')
assertIncludes(usageRecordMappersSource, 'upstreamEndpointFamily: optionalString(row.upstream_endpoint_family)', '使用记录 mapper 必须返回 upstreamEndpointFamily')
assertIncludes(usageRecordShardsSource, 'source_endpoint_family', '使用记录 shard 表、插入 SQL 和写入行必须包含 source_endpoint_family')
assertIncludes(usageRecordShardsSource, 'upstream_endpoint_family', '使用记录 shard 表、插入 SQL 和写入行必须包含 upstream_endpoint_family')
assertIncludes(gatewayUsageRecordsSource, 'sourceEndpointFamily: modelAccounting.sourceEndpointFamily', '网关使用记录必须写入来源协议族')
assertIncludes(gatewayUsageRecordsSource, 'upstreamEndpointFamily: modelAccounting.upstreamEndpointFamily', '网关使用记录必须写入上游协议族')

assertIncludes(auditLogTypesSource, 'sourceEndpointFamily?:', '审计日志顶层和尝试摘要必须包含 sourceEndpointFamily')
assertIncludes(auditLogTypesSource, 'upstreamEndpointFamily?:', '审计日志顶层和尝试摘要必须包含 upstreamEndpointFamily')
assertIncludes(auditLogMappersSource, 'sourceEndpointFamily: optionalString(row.source_endpoint_family)', '审计日志 mapper 必须返回顶层 sourceEndpointFamily')
assertIncludes(auditLogMappersSource, 'upstreamEndpointFamily: optionalString(row.upstream_endpoint_family)', '审计日志 mapper 必须返回顶层 upstreamEndpointFamily')
assertIncludes(auditLogMappersSource, 'sourceEndpointFamily: optionalString(row.attempt_source_endpoint_family)', '审计尝试 mapper 必须返回 attempt sourceEndpointFamily')
assertIncludes(auditLogMappersSource, 'upstreamEndpointFamily: optionalString(row.attempt_upstream_endpoint_family)', '审计尝试 mapper 必须返回 attempt upstreamEndpointFamily')
assertIncludes(auditLogsRepositorySource, 'attempt_source_endpoint_family', '审计尝试写入 SQL 必须包含 attempt_source_endpoint_family')
assertIncludes(auditLogsRepositorySource, 'attempt_upstream_endpoint_family', '审计尝试写入 SQL 必须包含 attempt_upstream_endpoint_family')
assertIncludes(auditCaptureSource, 'sourceEndpointFamily: accounting.sourceEndpointFamily', '审计捕获尝试必须记录来源协议族')
assertIncludes(auditCaptureSource, 'upstreamEndpointFamily: accounting.upstreamEndpointFamily', '审计捕获尝试必须记录上游协议族')

assertIncludes(modelChecksServiceSource, 'accountAllowsModel(account, model, modelCheckProfile)', '模型检测必须用协议 profile 感知模型映射入口')
assertIncludes(modelChecksServiceSource, 'mappedModelCheckSourceAllowed', '模型检测必须允许映射入口模型')

assertIncludes(usageRecordDomainTypesSource, 'sourceEndpointFamily?:', '前端使用记录类型必须包含 sourceEndpointFamily')
assertIncludes(usageRecordDomainTypesSource, 'upstreamEndpointFamily?:', '前端使用记录类型必须包含 upstreamEndpointFamily')
assertIncludes(auditLogDomainTypesSource, 'sourceEndpointFamily?:', '前端审计类型必须包含 sourceEndpointFamily')
assertIncludes(auditLogDomainTypesSource, 'upstreamEndpointFamily?:', '前端审计类型必须包含 upstreamEndpointFamily')
assertIncludes(usageCostDetailsSource, '计价模型', '使用记录成本明细必须展示计价模型')
assertIncludes(usageCostDetailsSource, '映射来源', '使用记录成本明细必须展示映射来源')
assertIncludes(auditDetailDrawerSource, 'attempt.sourceEndpointFamily', '审计详情尝试列表必须展示 attempt sourceEndpointFamily')
assertIncludes(auditDetailDrawerSource, 'attempt.upstreamEndpointFamily', '审计详情尝试列表必须展示 attempt upstreamEndpointFamily')
assertIncludes(accountTestDisplayFormattersSource, 'if (result.modelMappingApplied)', '账户测试终端必须在模型名不变但协议族映射时仍展示命中映射')
assertIncludes(modelChecksViewSource, 'modelCheckMappingProgressText(event)', '模型检测终端必须集中展示映射细节')
assertIncludes(modelChecksViewSource, 'event.modelMappingSource', '模型检测终端必须展示模型映射来源')
assertIncludes(modelChecksViewSource, 'modelCheckEndpointFamilyText(event.sourceEndpointFamily)', '模型检测终端必须展示来源协议族')
assertIncludes(modelChecksViewSource, 'modelCheckEndpointFamilyText(event.upstreamEndpointFamily)', '模型检测终端必须展示上游协议族')

console.log('模型映射可观测性回归通过：usage/audit 持久化、attempt 明细、模型检测映射入口和前端展示均有边界保护')

function readProjectFile(path: string): string {
  return readFileSync(resolve(projectRoot, path), 'utf8')
}

function assertIncludes(source: string, expected: string, message: string): void {
  assert(
    source.includes(expected),
    `${message}，缺少源码片段：${expected}`
  )
}
