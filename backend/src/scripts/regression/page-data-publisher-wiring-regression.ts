import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

function source(path: string): string {
  return readFileSync(resolve(path), 'utf8')
}

const usageRepository = source('src/storage/usage-records.repository.ts')
assert.match(usageRepository, /publishUsageRecordBatchChange\((?:writePlan\.shardEntries|persistedRecords)\)/, '使用记录整批成功后必须发布页面变更')
assert.match(usageRepository, /publishUsageRecordBatchChange\(writePlan\.shardEntries\)/, '必须使用实际落库记录的 owner，不能使用可能缺少 owner 的原始输入')
assert.doesNotMatch(
  usageRepository,
  /for \(const .*inputs[\s\S]{0,200}publishUsageRecordBatchChange/,
  '使用记录不得逐条发布页面变更'
)

const accountRoutes = source('src/modules/accounts/accounts.routes.ts')
assert.match(accountRoutes, /publishAccountStaticChange/, '账户创建和更新必须发布 static 变更')
assert.match(accountRoutes, /publishAccountRuntimeChange/, '账户状态恢复必须发布 runtime 变更')
assert.match(source('src/modules/accounts/account-delete.routes.ts'), /publishAccountStaticChange/, '账户删除必须发布 static 变更')
assert.match(source('src/modules/accounts/account-force-activate.routes.ts'), /publishAccountRuntimeChange/, '人工恢复必须发布 runtime 变更')
for (const [path, publisher, label] of [
  ['src/modules/accounts/account-import.routes.ts', 'publishAccountStaticReset', '账户导入'],
  ['src/modules/accounts/account-batch-edit.routes.ts', 'publishAccountStaticReset', '账户批量编辑'],
  ['src/modules/accounts/account-group-binding.routes.ts', 'publishAccountStaticChange', '账户分组绑定'],
  ['src/modules/accounts/account-tags.routes.ts', 'publishAccountStaticChange', '账户标签更新'],
  ['src/modules/accounts/account-authorized-dispatch.routes.ts', 'publishAccountStaticChange', '授权账户调度'],
  ['src/modules/accounts/account-authorization-return.routes.ts', 'publishAccountStaticReset', '授权账户归还'],
  ['src/modules/accounts/account-traffic-migration.routes.ts', 'publishAccountRuntimeChange', '账户流量迁移'],
  ['src/modules/accounts/account-balance-query.service.ts', 'publishAccountStaticChange', '账户余额持久化'],
  ['src/modules/authorizations/authorizations.routes.ts', 'publishAccountStaticReset', '账户授权生命周期']
] as const) {
  assert.match(source(path), new RegExp(publisher), `${label}必须发布页面变更`)
}
assert.match(source('src/modules/accounts/account-batch-edit.routes.ts'), /resolveAccountsPageDataOwners/, '批量编辑 reset 必须 fanout 到每个源账户的授权 grantee')
assert.match(source('src/modules/background/account-health-check.service.ts'), /publishAccountRuntimeChange/, '后台健康探针状态变更必须发布 runtime 变更')
assert.match(source('src/modules/background/cooldown-account-retest.service.ts'), /publishAccountRuntimeChange/, '冷却复测状态变更必须发布 runtime 变更')
assert.match(source('src/modules/gateway/policy/account-error-policy.service.ts'), /publishAccountRuntimeChange/, '用户显式账户错误策略改变状态后必须发布 runtime 变更')

const maintenanceSource = source('src/modules/record-maintenance/record-maintenance-queue.service.ts')
const retentionSource = source('src/modules/background/data-retention-cleanup.service.ts')
assert.match(maintenanceSource, /publishUsageRecordsGlobalReset/, '手工使用记录保留期清理必须发布单个 global reset')
assert.match(retentionSource, /publishUsageRecordsGlobalReset/, '后台使用记录保留期清理必须发布单个 global reset')
assert.doesNotMatch(maintenanceSource + retentionSource, /listSystemAccountsPageAsync|publishUsageRecordsResetForAllOwners/, '保留期清理不得 OFFSET 扫描 owner 或产生 O(owner) 事件')
assert.match(source('src/modules/authorizations/authorizations.routes.ts'), /findSystemTeamSummaryAsync/, '团队账户授权必须把有效团队成员加入 static reset owner')
const authorizationWriteSource = source('src/storage/resource-authorization-write.repository.ts')
assert.match(authorizationWriteSource, /publishExpiredAccountAuthorizationPageData/, '后台授权到期扫描必须发布账户列表 reset')
assert.match(authorizationWriteSource, /activeTeamMemberRowsAsync/, '团队授权到期必须覆盖有效团队成员 owner')
assert.match(source('src/modules/system-teams/system-teams.routes.ts'), /publishAccountStaticReset/, '团队成员增删必须失效成员的授权账户列表')

const announcementRoutes = source('src/modules/announcements/announcements.routes.ts')
assert.match(announcementRoutes, /publishAnnouncementPublicChange/, '公开公告可见集变化必须发布')

const dbServiceIpc = source('src/modules/db-service/db-service-ipc.ts')
const backgroundIpc = source('src/modules/background/background-ipc.ts')
assert.match(dbServiceIpc, /page_data_change_publish/, 'server 必须能将变更转发给 DB service')
assert.match(backgroundIpc, /page_data_change_publish/, 'worker 变更必须能通过父进程汇聚')
assert.match(dbServiceIpc, /page_data_change_dirty/, 'server 发布失败必须用专用 IPC 把 dirty domains 送到 DB service')
assert.match(backgroundIpc, /page_data_change_dirty/, 'worker 发布失败退出前必须把 dirty domains 交给父进程')
assert.match(dbServiceIpc, /page_data_change_dirty_ack/, 'DB service 持久化 dirty domain 后必须返回专用 ACK')
assert.match(dbServiceIpc, /pendingPageDataDirty/, 'server 必须保留 dirty IPC pending，不能把 child.send 传输回调当成持久化成功')
assert.match(dbServiceIpc, /case 'page_data_change_dirty_ack':[\s\S]*record\.ok === true/, 'server 只能在 DB service 成功 ACK 后完成 dirty 请求')
assert.match(backgroundIpc, /page_data_change_dirty_ack/, 'server 必须把 DB service dirty ACK 反向传回原 worker')
assert.match(backgroundIpc, /record\.type === 'page_data_change_dirty_ack'[\s\S]*acceptPageDataDirtyDomainsParentAck/, 'worker 必须等待父进程持久化 ACK')
assert.match(runtimeSourceForAck(), /pendingPageDataDirtyParentAcks/, 'worker 必须保留等待父进程 ACK 的 dirty 请求')

const dirtyRepository = source('src/storage/page-data-dirty-domain.repository.ts')
assert.match(dirtyRepository, /page_data_dirty_domains/, 'dirty domain 必须持久化，DB service 重启后可恢复')
assert.match(dirtyRepository, /generation\s*=\s*page_data_dirty_domains\.generation\s*\+\s*1/, 'dirty 持久化必须按 domain 增长代际')
assert.match(dirtyRepository, /is_dirty\s*=\s*(?:FALSE|0)[\s\S]*generation\s*=\s*(?:\$2|\?)/, '恢复成功只能按 generation CAS 标记 clean')
assert.doesNotMatch(dirtyRepository, /DELETE FROM (?:juhe_business\.)?page_data_dirty_domains/, 'dirty generation 行不得删除后重用代际')
assert.match(dirtyRepository, /WHERE is_dirty\s*=\s*(?:TRUE|1)/, '启动恢复只能加载仍为 dirty 的 domain')

const routeSource = source('src/modules/page-data/page-data-change.routes.ts')
assert.doesNotMatch(routeSource, /listUsage|findAccount|listAccount|findAnnouncement/, 'confirm 只能读取变更流，禁止查询业务明细')
const runtimeSource = source('src/modules/page-data/page-data-change.runtime.ts')
assert.doesNotMatch(runtimeSource, /async confirm[\s\S]{0,500}(?:getBusinessDatabase|listPageDataDirtyDomains)/, 'confirm 热路径禁止查询 dirty 持久表')
assert.match(runtimeSource, /initializePageDataChangeRuntime[\s\S]*try\s*\{[\s\S]*recoverDirtyDomains\(\)[\s\S]*catch/, '启动恢复失败必须保留 dirty 并允许 DB service 继续启动')
assert.match(runtimeSource, /recoveryInFlight/, 'dirty domain 自动恢复必须 singleflight，避免 timer、IPC 与 confirm 并发重放')

console.log('页面数据写端接线回归通过')

function runtimeSourceForAck(): string {
  return source('src/modules/page-data/page-data-change.runtime.ts')
}
