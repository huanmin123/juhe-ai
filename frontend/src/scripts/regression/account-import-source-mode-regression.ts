import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const modal = await readFile(new URL('../../views/accounts/AccountImportModal.vue', import.meta.url), 'utf8')
const api = await readFile(new URL('../../api/domains/accounts.ts', import.meta.url), 'utf8')
const actions = await readFile(new URL('../../views/api-keys/useApiKeyRowActions.ts', import.meta.url), 'utf8')

for (const mode of ['native', 'sub2api', 'newapi', 'cpa', 'oneapi']) {
  assert.match(modal, new RegExp(`value: '${mode}'`), `导入弹窗必须提供 ${mode} 模式`)
}
assert.match(modal, /sourceMode: sourceMode\.value/, '导入请求必须携带来源模式')
assert.match(modal, /sourceMode\.value === 'cpa'\) return importText\.value/, 'CPA 必须保留 YAML 原文交给后端解析')
assert.match(modal, /previewResult\.source\.ignoredFields/, '来源预览必须展示忽略字段计数')
assert.match(api, /sourceMode\?: AccountImportSourceMode/, '账户导入 API payload 必须声明来源模式')
assert.match(actions, /apiKeysApi\.secret\(apiKey\.id/, 'CCS 导出必须通过既有 secret reveal 读取完整密钥')
assert.match(actions, /buildCcSwitchExportUrl/, 'CCS 导出必须使用统一 deeplink 构造器')
assert.match(actions, /confirmTitle: '导出 CCS/, 'CCS 导出必须在读取密钥前要求确认')

console.log('account-import-source-mode regression passed')
