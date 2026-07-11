import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const frontendRoot = resolve(import.meta.dirname, '../..')
const source = readFileSync(
  resolve(frontendRoot, 'src/views/audit-logs/AuditLogDetailDrawer.vue'),
  'utf8'
)

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

console.log('audit log detail layout regression passed')
