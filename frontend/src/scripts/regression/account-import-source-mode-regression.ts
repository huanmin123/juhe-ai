import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const modal = await readFile(new URL('../../views/accounts/AccountImportModal.vue', import.meta.url), 'utf8')
const api = await readFile(new URL('../../api/domains/accounts.ts', import.meta.url), 'utf8')
const actions = await readFile(new URL('../../views/api-keys/useApiKeyRowActions.ts', import.meta.url), 'utf8')
const ccsModal = await readFile(new URL('../../views/api-keys/ApiKeyCcsExportModal.vue', import.meta.url), 'utf8')

for (const mode of ['native', 'sub2api', 'newapi', 'cpa', 'oneapi']) {
  assert.match(modal, new RegExp(`value: '${mode}'`), `导入弹窗必须提供 ${mode} 模式`)
}
assert.match(modal, /sourceMode: sourceMode\.value/, '导入请求必须携带来源模式')
assert.match(modal, /sourceMode\.value === 'cpa'\) return importText\.value/, 'CPA 必须保留 YAML 原文交给后端解析')
assert.match(modal, /label: 'CLIProxyAPI', value: 'cpa'/, 'CLIProxyAPI 必须作为 CPA 导入模式的用户可见名称')
assert.match(modal, /const sourceExample = computed/, '外部导入模式必须提供来源格式示例')
assert.match(modal, /sourceMode\.value === 'sub2api'[\s\S]*sourceMode\.value === 'newapi'[\s\S]*sourceMode\.value === 'oneapi'/, 'Sub2API、NewAPI 与 One-API 必须各自提供示例')
assert.match(modal, /openai-compatibility:/, 'CLIProxyAPI 必须提供 YAML 配置示例')
assert.match(modal, /previewResult\.source\.ignoredFields/, '来源预览必须展示忽略字段计数')
assert.match(api, /sourceMode\?: AccountImportSourceMode/, '账户导入 API payload 必须声明来源模式')
assert.match(actions, /apiKeysApi\.secret\(apiKey\.id/, 'CCS 导出必须通过既有 secret reveal 读取完整密钥')
assert.match(actions, /buildCcSwitchExportUrl/, 'CCS 导出必须使用统一 deeplink 构造器')
assert.doesNotMatch(actions, /confirmTitle: '导出 CCS/, 'CCS 导出不应增加额外确认弹窗')
assert.match(ccsModal, /v-model:value="model"/, 'CCS 模型必须是受控下拉选择')
assert.match(ccsModal, /:options="modelOptions"/, 'CCS 模型候选必须来自所选分组的供应商目录')
assert.match(ccsModal, /@search="handleModelOptionsSearch"/, 'CCS 模型下拉必须支持搜索目录')
assert.match(ccsModal, /:disabled="!modelsReady"/, 'CCS 模型目录首次加载完成前必须禁用选择')
assert.doesNotMatch(ccsModal, /:disabled="!modelsReady \|\| modelsLoading"/, '已有目录时打开下拉不得因刷新而立即锁住模型选择')
assert.match(ccsModal, /modelsLoading: props\.modelsLoading/, 'CCS 导出提交必须继续在模型刷新期间保持禁用')
assert.match(actions, /loadAccountProviderModelOptionsResource/, 'CCS 模型候选必须复用供应商模型目录加载器')
assert.match(actions, /ccsExportModelOptionsGroupId\.value !== selection\.groupId/, 'CCS 导出必须校验模型目录属于当前分组')
assert.match(actions, /clearTimeout\(ccsExportModelSearchTimer\)/, '切换分组时必须取消旧模型搜索')
assert.match(actions, /catch \(error\)[\s\S]{0,320}ccsExportModelsReady\.value = false/, '当前模型目录请求失败时必须撤销可导出状态')

console.log('account-import-source-mode regression passed')
