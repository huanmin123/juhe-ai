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
assert.deepEqual(
  visibleSavedAccountApiKeyRuntimeDetails(snapshot, ['sk-three', 'sk-one', 'sk-two', 'sk-four', 'sk-five'])?.map((item) => item.status),
  ['rate_limited', 'active', 'temporary_unavailable', 'error', 'disabled'],
  '仅调整 Key 顺序时，运行态必须跟随具体 Key 而不是原行位置'
)

const sectionSource = readFileSync('../frontend/src/views/accounts/AccountApiKeySection.vue', 'utf8')
assert.match(sectionSource, /v-for="\(_, index\) in form\.apiKeys"/, '多 Key 表单必须为每个输入行渲染状态单元')
assert.match(sectionSource, /API Key 使用说明/, 'API Key 区域必须提供机制说明入口')
assert.match(sectionSource, /<strong>主备<\/strong>/, 'API Key 说明必须解释主备机制')
assert.match(sectionSource, /<strong>轮询<\/strong>/, 'API Key 说明必须解释轮询机制')
assert.match(sectionSource, /<strong>权重<\/strong>/, 'API Key 说明必须解释权重机制')
const defaultsSource = readFileSync('../frontend/src/views/accounts/accountFormDefaults.ts', 'utf8')
assert.match(defaultsSource, /apiKeyStrategy: 'failover'/, '新建多 Key 表单默认策略必须是主备')
assert.match(sectionSource, /Base URL 填写说明/, 'Base URL 必须提供独立填写说明入口')
assert.match(sectionSource, /<strong>不要填写<\/strong>/, 'Base URL 说明必须强调不能填写具体接口路径')
assert.match(sectionSource, /<strong>本地联调<\/strong>/, 'Base URL 说明必须包含本地联调写法')
assert.match(sectionSource, /<template[^>]*#prefix>/, 'Key 运行态必须放入密码输入框前缀')
assert.match(sectionSource, /runtimeDetailForIndex\(index\)/, '状态标签必须按当前 Key 身份与上游运行态明细对应')
assert.match(sectionSource, /value="failover"/, '多 Key 表单必须提供主备模式')
assert.match(sectionSource, /isFailoverMode/, '主备模式必须有独立的拖拽开关')
assert.match(sectionSource, /handleApiKeyDragStart/, '主备模式必须支持拖拽调整 Key 顺序')
assert.match(sectionSource, /moveApiKeyForMode\(index, index - 1\)/, '主备模式必须支持键盘上移调整顺序')
assert.match(sectionSource, /@click="removeApiKeyInput\(index\)"[\s\S]*@click="addApiKeyInput\(index\)"/, '删除按钮必须位于行内添加按钮左侧，保持加号位置稳定')
assert.doesNotMatch(sectionSource, /api-key-add-action/, '添加 API Key 不得脱离当前输入行')
assert.match(sectionSource, /filledApiKeyCount\.value > 1/, '已保存多 Key 编辑页必须启用运行态标签显示')
for (const [status, label] of statuses) {
  assert.match(sectionSource, new RegExp(`case '${status}':\\s*return \\{ label: '${label}'`), `运行态 ${status} 必须渲染为中文真实状态标签`)
}

console.log('多 Key 编辑页运行态标签回归通过：按 Key 身份保留运行态，变更文本后不复用旧快照')
