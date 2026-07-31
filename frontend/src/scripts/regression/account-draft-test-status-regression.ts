import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { defaultAccountForm } from '../../views/accounts/accountFormDefaults'
import { statusAfterDraftTestSuccess } from '../../views/accounts/accountDraftTestStatus'
import { buildAccountSavePayload, buildAccountUpdatePayload } from '../../views/accounts/accountSavePayload'

const form = defaultAccountForm('gpt', 'api_key')
assert.equal(form.status, 'pending_test', '新账户未选择可调用时必须默认待检查')
assert.equal(statusAfterDraftTestSuccess('pending_test'), 'active', '草稿测试成功后必须将待检查预选为可调度')
assert.equal(statusAfterDraftTestSuccess('active'), 'active', '草稿测试成功不应改变已选可调度')
assert.equal(statusAfterDraftTestSuccess('disabled'), 'disabled', '草稿测试成功不得覆盖用户手动停用')

form.status = 'active'
form.statusSelectionExplicit = true
const updatePayload = buildAccountUpdatePayload(buildAccountSavePayload({
  accounts: [],
  form,
  errorPolicyRules: [],
  responseInspectionRules: []
}), true)
assert.equal(updatePayload.status, 'active', '编辑保存必须传递状态单选的可调度值')

const basicInfoSource = readFileSync(fileURLToPath(new URL('../../views/accounts/AccountBasicInfoSection.vue', import.meta.url)), 'utf8')
assert.match(basicInfoSource, /<a-form-item class="dispatch-status-field" label="状态">/, '新增和编辑必须共用状态单选')
assert.doesNotMatch(basicInfoSource, /v-if="!editing"/, '编辑表单不得隐藏状态单选')

const testModalSource = readFileSync(fileURLToPath(new URL('../../views/accounts/useAccountTestModal.ts', import.meta.url)), 'utf8')
assert.match(testModalSource, /if \(run\.draftPayload\) options\.onDraftTestSuccess\?\.\(run\.draftPayload\)/, '草稿测试成功必须携带被测草稿通知表单更新状态选择')

console.log('账户草稿测试状态回归通过：新增默认待检查、成功预选可调度、手动停用保持、编辑状态保存和双场景单选入口均正确')
