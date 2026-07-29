import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  databaseRoleDetailLabel,
  databaseRoleLabel,
  buildTableStorageHistoryChartOption,
  tableMonitorDatabaseRoles,
  totalDatabaseHistoryBytes
} from '@/views/table-monitor/tableMonitorDisplay'

assert.deepEqual(tableMonitorDatabaseRoles, [
  'business',
  'dataset',
  'usage-catalog',
  'stats',
  'codex-context-state'
])
assert.equal(databaseRoleLabel('codex-context-state'), 'Responses 状态库')
assert.equal(databaseRoleDetailLabel('usage-catalog'), '使用记录目录库')
assert.equal(totalDatabaseHistoryBytes({
  databaseRole: 'business',
  sampledAt: '2026-01-01T00:00:00.000Z',
  fileBytes: 10,
  walBytes: 5,
  freeBytes: 3,
  tableCount: 2
}), 15, '数据库历史趋势应只统计主库文件与 WAL')

const typeSource = readFileSync('../frontend/src/types/domain/table-monitor.ts', 'utf8')
const displaySource = readFileSync('../frontend/src/views/table-monitor/tableMonitorDisplay.ts', 'utf8')
assert.doesNotMatch(typeSource, /'archive'/, '前端监控角色类型不应保留归档库')
assert.doesNotMatch(displaySource, /归档库|juhe_archive|archive:\s*'magenta'/, '前端不应继续显示无实际 schema 的归档库')
assert.match(typeSource, /export interface DatabaseStorageHistoryPoint[\s\S]*databaseRole[\s\S]*sampledAt[\s\S]*fileBytes[\s\S]*walBytes[\s\S]*freeBytes[\s\S]*tableCount/, '前端应使用独立六字段数据库历史 DTO')
assert.match(typeSource, /export interface TableStorageHistoryPoint[\s\S]*sampledAt[\s\S]*rowCount[\s\S]*totalBytes/, '单表趋势应使用独立三字段 DTO')
const tableHistoryOption = buildTableStorageHistoryChartOption([
  { sampledAt: '2026-01-01T00:00:00.000Z', totalBytes: 1024, rowCount: 12 }
])
assert.equal(Array.isArray(tableHistoryOption.series) ? tableHistoryOption.series.length : 0, 2, '单表趋势应分别展示总大小与行数')

console.log('table monitor display regression passed')
