import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

function read(relativePath: string): string {
  return readFileSync(fileURLToPath(new URL(relativePath, import.meta.url)), 'utf8')
}

const editorSource = read('../../views/accounts/AccountConcurrencyEditor.vue')
const tableCellSource = read('../../views/accounts/AccountTableCell.vue')
const mobileCardSource = read('../../views/accounts/AccountMobileCard.vue')
const accountListSource = read('../../views/accounts/AccountList.vue')
const accountsViewSource = read('../../views/accounts/AccountsView.vue')

assert.match(editorSource, /editing: boolean/, '并发编辑器必须由列表级状态控制，不能由每行各自保存编辑态')
assert.doesNotMatch(editorSource, /const editing = ref\(/, '并发编辑器不得恢复为每行独立编辑状态')
assert.match(editorSource, /document\.addEventListener\('pointerdown', handleDocumentPointerDown, true\)/, '并发编辑器必须在捕获阶段监听外部点击')
assert.match(editorSource, /document\.removeEventListener\('pointerdown', handleDocumentPointerDown, true\)[\s\S]*if \(editing\)[\s\S]*document\.addEventListener/, '只有当前编辑行可以注册外部点击监听器')
assert.match(editorSource, /!editorRef\.value\?\.contains\(target\).*cancel\(\)/, '点击并发编辑器外部必须取消当前草稿')
assert.doesNotMatch(editorSource, /CloseOutlined|取消修改并发上限/, '并发编辑器不得保留独立 X 取消按钮')
assert.match(editorSource, /class="account-concurrency-control"[\s\S]*class="account-concurrency-input"[\s\S]*class="account-concurrency-confirm"/, '确认按钮必须与输入框组成一体化控件')
assert.match(editorSource, /@press-enter="save"/, '回车必须继续确认并发上限')
assert.match(editorSource, /<a-tag[\s\S]*color="blue"/, '非编辑态必须保留原蓝色并发标签展示，不得改为按钮或纯文本')
assert.doesNotMatch(editorSource, /<a-tooltip/, '并发编辑器展示态不得挂悬浮气泡（2026-10-23 用户裁决移除）')
const priorityEditorSource = read('../../views/accounts/AccountPriorityEditor.vue')
assert.doesNotMatch(priorityEditorSource, /<a-tooltip/, '优先级编辑器展示态同样不挂悬浮气泡')
assert.match(editorSource, /:min="1"/, '并发上限输入必须限制最小值为 1')
assert.match(editorSource, /并发上限必须是大于等于 1 的整数/, '并发上限必须校验为正整数并给出提示')

assert.match(tableCellSource, /columnKey === 'concurrency'[\s\S]*AccountConcurrencyEditor/, '桌面表格并发列必须改用并发编辑器渲染')
assert.match(tableCellSource, /!isAuthorizedAccount\(account\)/, '授权账户并发上限只读，不得提供行内编辑')
assert.doesNotMatch(tableCellSource, /const concurrencyText = computed/, '并发文案与 tooltip 必须收敛进编辑器，不得在单元格重复维护')
assert.match(mobileCardSource, /<AccountConcurrencyEditor[\s\S]*mobile/, '移动卡片实时并发必须复用同一编辑器（mobile 展示形态）')

assert.match(accountsViewSource, /const editingConcurrencyAccountId = ref<string>\(\)/, '账户列表必须只维护一个当前并发编辑账户 ID')
assert.match(accountsViewSource, /function startConcurrencyEditor\(accountId: string\)[\s\S]*editingConcurrencyAccountId\.value = accountId/, '开始编辑另一行时必须直接切换唯一编辑账户')
assert.match(accountsViewSource, /function startConcurrencyEditor\(accountId: string\)[\s\S]*closePriorityEditor\(\)/, '并发与优先级行内编辑必须互斥，打开一个必须关闭另一个')
assert.match(accountsViewSource, /function refreshData\(\)[\s\S]*closeConcurrencyEditor\(\)[\s\S]*refreshAccountList\(\)/, '刷新桌面列表前必须重置并发编辑态')
assert.match(accountsViewSource, /function refreshMobileAccounts\(\)[\s\S]*closeConcurrencyEditor\(\)[\s\S]*refreshMobileAccountList\(\)/, '刷新移动列表前必须重置并发编辑态')
assert.match(accountsViewSource, /async function saveAccountConcurrency[\s\S]*isAuthorizedAccount\(account\)[\s\S]*expectedConfigRevision/, '并发保存必须校验授权账户只读并携带配置版本')
assert.match(accountsViewSource, /async function saveAccountConcurrency[\s\S]*concurrencyLimit: limit/, '并发保存必须通过普通账户 PATCH 只提交 concurrencyLimit')
assert.match(accountListSource, /:concurrency-editing="editingConcurrencyAccountId === record\.id"/, '桌面与移动列表必须由唯一账户 ID 决定并发编辑行')

console.log('账户并发数行内编辑回归通过：单行受控、标签展示保留、正整数校验、授权账户只读及列表刷新取消均符合预期')
