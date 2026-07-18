import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { defaultAccountForm } from '../../views/accounts/accountFormDefaults'
import { buildAccountSavePayload, buildAccountUpdatePayload } from '../../views/accounts/accountSavePayload'

const form = defaultAccountForm('gpt', 'api_key')
assert.equal(form.temporaryUnavailableContinuousProbeEnabled, true, '新账户持续恢复探活必须默认开启')
form.name = '有界探活表单回归'
form.groupId = 'grp-test'
form.apiKey = 'sk-test'
form.apiKeys = ['sk-test']
form.baseUrl = 'https://api.openai.com/v1'
form.supportedModels = ['gpt-5.4-mini']
form.healthCheckModel = 'gpt-5.4-mini'
form.temporaryUnavailableContinuousProbeEnabled = false
const savePayload = buildAccountSavePayload({
  accounts: [],
  form,
  errorPolicyRules: [],
  responseInspectionRules: []
})
assert.equal(savePayload.temporaryUnavailableContinuousProbeEnabled, false, '创建 payload 必须保留关闭值')
assert.equal(buildAccountUpdatePayload(savePayload).temporaryUnavailableContinuousProbeEnabled, false, '编辑 payload 必须保留关闭值')

const modalSource = readFileSync(fileURLToPath(new URL('../../views/accounts/AccountEditModal.vue', import.meta.url)), 'utf8')
assert.match(modalSource, /持续恢复探活/, '高级配置必须展示持续恢复探活开关')
assert.match(modalSource, /前 10 分钟/, '开关说明必须明确十分钟有界窗口')
assert.match(modalSource, /:disabled="authorizedEditing"/, '授权实例只能只读查看来源账户策略')
assert.match(modalSource, /temporaryUnavailableContinuousProbeEnabled === false/, '关闭值必须计入高级配置项数量')

console.log('持续恢复探活前端表单回归通过')
