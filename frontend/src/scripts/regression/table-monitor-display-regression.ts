import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  databaseRoleDetailLabel,
  databaseRoleLabel,
  tableMonitorDatabaseRoles
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

const typeSource = readFileSync('../frontend/src/types/domain/table-monitor.ts', 'utf8')
const displaySource = readFileSync('../frontend/src/views/table-monitor/tableMonitorDisplay.ts', 'utf8')
assert.doesNotMatch(typeSource, /'archive'/, '前端监控角色类型不应保留归档库')
assert.doesNotMatch(displaySource, /归档库|juhe_archive|archive:\s*'magenta'/, '前端不应继续显示无实际 schema 的归档库')

console.log('table monitor display regression passed')
