import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const frontendRoot = resolve(import.meta.dirname, '../..')
const source = readFileSync(
  resolve(frontendRoot, 'src/views/audit-logs/AuditLogDetailDrawer.vue'),
  'utf8'
)
const listSource = readFileSync(resolve(frontendRoot, 'src/views/audit-logs/AuditLogList.vue'), 'utf8')
const columnsSource = readFileSync(resolve(frontendRoot, 'src/views/audit-logs/auditLogTableColumns.ts'), 'utf8')
const backgroundJobsSource = readFileSync(resolve(frontendRoot, 'src/views/stats/StatsBackgroundJobsCard.vue'), 'utf8')

assert.match(listSource, /column\.key === 'model'[\s\S]*record\.model[\s\S]*modelMappingApplied[\s\S]*上游/, '审计日志模型列必须与使用记录一致显示请求模型和映射后的上游模型标签')
assert.match(columnsSource, /title: '模型', key: 'model', width: 240/, '审计日志模型列宽必须与使用记录对齐')
assert.ok(!backgroundJobsSource.includes("record.failureCount > 0 ? 'warning'"), '后台任务累计失败后若已成功恢复，不应继续显示当前异常')
assert.match(backgroundJobsSource, /record\.lastError \? 'warning' : 'success'/, '后台任务当前异常状态必须以尚未恢复的 lastError 为准')

assert.ok(!source.includes('<a-tab-pane key="attempts"'), '审计详情不应保留请求链路页签')
assert.ok(!source.includes('<a-tab-pane key="payloads"'), '审计详情不应保留原始请求页签')
assert.ok(source.includes('class="request-chain-section"'), '请求链路应作为详情抽屉的直接内容展示')
assert.ok(source.includes(':data-source="requestChainRows"'), '合并后的列表应继续使用完整请求链路数据')
assert.ok(source.includes('class="payload-viewer"'), '链路详情入口应在列表下方复用原文查看器')

const accountCellStart = source.indexOf("column.key === 'account'")
const accountCellEnd = source.indexOf("column.key === 'status'", accountCellStart)
assert.ok(accountCellStart >= 0 && accountCellEnd > accountCellStart, '应存在 AI 账户单元格')
const accountCellSource = source.slice(accountCellStart, accountCellEnd)
assert.ok(!accountCellSource.includes('modelMappingText'), 'AI 账户列只能显示账户名称')

assert.ok(!source.includes("{ title: '捕获', key: 'captureStatus'"), '捕获状态应合并到数据列')
assert.ok(!source.includes("{ title: '耗时', key: 'duration'"), '耗时应与时间合并展示')
assert.ok(!source.includes("{ title: '大小', key: 'size'"), '大小应合并到数据列')
assert.ok(
  source.includes("record.payload ? captureStatusText(record.captureStatus) : '未捕获'"),
  '存在无正文 payload 时移动端仍应显示 overflow 捕获状态，不能显示未捕获'
)
assert.ok(
  source.includes("record.sizeBytes === undefined ? '-' : formatBytes(record.sizeBytes)"),
  '无正文 payload 仍应显示已记录的原始字节数'
)
assert.ok(
  source.includes("label: record.payload ? payloadActionLabel(record.payload) : '未捕获'"),
  '存在无正文 payload 时详情操作不应标记为未捕获'
)

console.log('audit log detail layout regression passed')
