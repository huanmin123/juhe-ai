import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  createSavedAccountApiKeyRuntimeSnapshot,
  visibleSavedAccountApiKeyRuntimeDetails
} from '@/views/accounts/accountApiKeyRuntimeDisplay'
import type { AccountApiKeyRuntimeDetail } from '@/types/domain'

const statuses = [
  ['active', '可调度'],
  ['temporary_unavailable', '临时避让'],
  ['rate_limited', '限流冷却'],
  ['error', '异常'],
  ['disabled', '已停用']
] as const

const items = statuses.map(([status], keyIndex) => ({
  keyIndex,
  status,
  lastErrorMessage: status === 'active' ? undefined : `${status} 原因`
})) as AccountApiKeyRuntimeDetail[]

const snapshot = createSavedAccountApiKeyRuntimeSnapshot({
  accountId: 'account-multi-key',
  configRevision: 7,
  apiKeys: ['sk-one', ' sk-two ', 'sk-three', 'sk-four', 'sk-five'],
  response: {
    accountId: 'account-multi-key',
    configRevision: 7,
    items
  }
})
assert(snapshot, '编辑已保存的多 Key 账户必须保存当前配置版本对应的运行态明细')
assert.deepEqual(
  visibleSavedAccountApiKeyRuntimeDetails(snapshot, ['sk-one', 'sk-two', 'sk-three', 'sk-four', 'sk-five'])?.map((item) => item.status),
  statuses.map(([status]) => status),
  'Key 文本仅规范化空白时，编辑页必须保留每个 Key 的真实运行态'
)
assert.equal(
  visibleSavedAccountApiKeyRuntimeDetails(snapshot, ['sk-one', 'sk-two', 'sk-three', 'sk-four', 'changed']),
  undefined,
  '任一 Key 真正变更后不得展示旧运行态标签'
)

const sectionSource = readFileSync('../frontend/src/views/accounts/AccountApiKeySection.vue', 'utf8')
assert.match(sectionSource, /v-for="\(_, index\) in form\.apiKeys"/, '多 Key 表单必须为每个输入行渲染状态单元')
assert.match(sectionSource, /runtimeDetailForIndex\(index\)/, '状态标签必须按 Key index 与上游运行态明细对应')
assert.match(sectionSource, /filledApiKeyCount\.value > 1/, '已保存多 Key 编辑页必须启用运行态标签显示')
for (const [status, label] of statuses) {
  assert.match(sectionSource, new RegExp(`case '${status}':\\s*return \\{ label: '${label}'`), `运行态 ${status} 必须渲染为中文真实状态标签`)
}

console.log('多 Key 编辑页运行态标签回归通过：配置未变化时逐 Key 保留真实状态，变更后不复用旧快照')
