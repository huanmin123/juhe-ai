import assert from 'node:assert/strict'

import { defaultAccountForm } from '../../src/views/accounts/accountFormDefaults'
import { FALLBACK_PROVIDERS } from '../../src/views/accounts/accountOptions'
import {
  buildAccountBasicEditSnapshot,
  buildAccountBasicUpdatePatch
} from '../../src/views/accounts/accountEditPatch'

const baselineForm = defaultAccountForm('gpt', 'api_key', FALLBACK_PROVIDERS)
baselineForm.name = '账户说明 PATCH 回归'
baselineForm.notes = '原说明'

const baseline = buildAccountBasicEditSnapshot(baselineForm)
const cleared = buildAccountBasicEditSnapshot({ ...baselineForm, notes: '' })
const clearedPatch = buildAccountBasicUpdatePatch(cleared, baseline, 7)
assert.deepEqual(
  clearedPatch,
  { notes: '', expectedConfigRevision: 7 },
  '清空已有说明必须提交空字符串，不能提交 null'
)
assert.notEqual(clearedPatch?.notes, null, '说明 PATCH 不得使用 null 表示清空')

const emptyBaseline = buildAccountBasicEditSnapshot({ ...baselineForm, notes: '' })
const emptyCurrent = buildAccountBasicEditSnapshot({ ...baselineForm, notes: '' })
assert.equal(
  buildAccountBasicUpdatePatch(emptyCurrent, emptyBaseline, 7),
  undefined,
  '说明原本为空且保持为空时不得生成 PATCH'
)

const changed = buildAccountBasicEditSnapshot({ ...baselineForm, notes: '新说明' })
assert.deepEqual(
  buildAccountBasicUpdatePatch(changed, baseline, 7),
  { notes: '新说明', expectedConfigRevision: 7 },
  '修改非空说明时必须继续提交字符串'
)

console.log('账户基础编辑说明 PATCH 回归通过：空说明按字符串提交且 no-op 不发请求')
