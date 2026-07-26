import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

function read(relativePath: string): string {
  return readFileSync(fileURLToPath(new URL(relativePath, import.meta.url)), 'utf8')
}

const editorSource = read('../../views/accounts/AccountPriorityEditor.vue')
const accountListSource = read('../../views/accounts/AccountList.vue')
const accountsViewSource = read('../../views/accounts/AccountsView.vue')

assert.match(editorSource, /editing: boolean/, '优先级编辑器必须由列表级状态控制，不能由每行各自保存编辑态')
assert.doesNotMatch(editorSource, /const editing = ref\(/, '优先级编辑器不得恢复为每行独立编辑状态')
assert.match(editorSource, /document\.addEventListener\('pointerdown', handleDocumentPointerDown, true\)/, '优先级编辑器必须在捕获阶段监听外部点击')
assert.match(editorSource, /document\.removeEventListener\('pointerdown', handleDocumentPointerDown, true\)[\s\S]*if \(editing\)[\s\S]*document\.addEventListener/, '只有当前编辑行可以注册外部点击监听器')
assert.match(editorSource, /!editorRef\.value\?\.contains\(target\).*cancel\(\)/, '点击优先级编辑器外部必须取消当前草稿')
assert.doesNotMatch(editorSource, /CloseOutlined|取消修改优先级/, '优先级编辑器不得保留独立 X 取消按钮')
assert.match(editorSource, /class="account-priority-control"[\s\S]*class="account-priority-input"[\s\S]*class="account-priority-confirm"/, '确认按钮必须与输入框组成一体化控件')
assert.match(editorSource, /@press-enter="save"/, '回车必须继续确认优先级')

assert.match(accountsViewSource, /const editingPriorityAccountId = ref<string>\(\)/, '账户列表必须只维护一个当前编辑账户 ID')
assert.match(accountsViewSource, /function startPriorityEditor\(accountId: string\)[\s\S]*editingPriorityAccountId\.value = accountId/, '开始编辑另一行时必须直接切换唯一编辑账户')
assert.match(accountsViewSource, /function refreshData\(\)[\s\S]*closePriorityEditor\(\)[\s\S]*refreshAccountList\(\)/, '刷新桌面列表前必须重置优先级编辑态')
assert.match(accountsViewSource, /function refreshMobileAccounts\(\)[\s\S]*closePriorityEditor\(\)[\s\S]*refreshMobileAccountList\(\)/, '刷新移动列表前必须重置优先级编辑态')
assert.match(accountListSource, /:priority-editing="editingPriorityAccountId === record\.id"/, '桌面与移动列表必须由唯一账户 ID 决定编辑行')

console.log('账户优先级内联编辑回归通过：单行受控、外部点击和列表刷新取消、一体化确认及回车保存均符合预期')
