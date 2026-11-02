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
assert.doesNotMatch(editorSource, /v-if="editable && !mobile"/, '并发展示徽章必须排除 editing，编辑态下不得与输入控件并排（2026-10-24 用户报障）')
assert.match(editorSource, /v-if="!editing && editable && !mobile"/, '并发 trigger 分支必须以 !editing 开头')
assert.doesNotMatch(priorityEditorSource, /v-if="editable"/, '优先级 trigger 分支必须排除 editing（同源报障）')
assert.match(editorSource, /:min="1"/, '并发上限输入必须限制最小值为 1')
assert.match(editorSource, /并发上限必须是大于等于 1 的整数/, '并发上限必须校验为正整数并给出提示')

// ---- 限频单行（RPM 设计 §6.2）：并发 current/limit, 每分钟 current/limit ----

assert.match(editorSource, /rpmCurrent\?: number[\s\S]*rpmLimit\?: number[\s\S]*rpmResetAt\?: string/, '限频编辑器必须声明 RPM 三个可选 props')
assert.match(editorSource, /const rpmText = computed\(\(\) =>[\s\S]*return ''/, 'RPM 数对必须在未配置（rpmLimit=0 或缺省）时为空——0 = 不限制')
assert.match(editorSource, /\$\{Math\.max\(0, props\.rpmCurrent \?\? 0\)\}\/\$\{props\.rpmLimit\}/, 'RPM 数对必须展示「已用/上限」')
assert.match(editorSource, /const text = computed\(\(\) => \(rpmText\.value \? `\$\{concurrencyText\.value\}, \$\{rpmText\.value\}` : concurrencyText\.value\)\)/, '展示态必须为单行「并发 current/limit, 每分钟 current/limit」')
assert.match(editorSource, /本分钟已派发 \$\{Math\.max\(0, props\.rpmCurrent \?\? 0\)\} 次，上限 \$\{props\.rpmLimit\} 次\/分钟\$\{resetSuffix\}/, 'RPM tooltip 必须使用「本分钟已派发 X 次，上限 Y 次/分钟」文案')
assert.match(editorSource, /resetSeconds > 0 \? `，约 \$\{resetSeconds\} 秒后重置` : ''/, 'RPM tooltip 无重置时刻时不得拼「约 0 秒后重置」')
assert.match(editorSource, /Math\.max\(0, Math\.ceil\(\(resetAtMs - Date\.now\(\)\) \/ 1000\)\)/, 'RPM 重置秒数必须由 rpmResetAt 计算且最小值 0')
// 编辑态：两个输入 + 分隔线 + 确认按钮必须是一个整体控件。
assert.match(editorSource, /aria-label="并发上限"[\s\S]*account-concurrency-divider[\s\S]*aria-label="每分钟请求上限"[\s\S]*aria-label="保存限频"/, '编辑态必须是「并发输入 | 每分钟输入 ✓」一体化控件')
assert.match(editorSource, /:min="0"[\s\S]*每分钟请求上限必须是大于等于 0 的整数/, '每分钟请求上限必须允许 0 并校验非负整数')
assert.match(editorSource, /:max="9999"[\s\S]*:max="9999"/, '两个输入都必须限制 4 位上限 9999')
assert.match(editorSource, /并发上限最大为 9999/, '并发上限超过 9999 必须提示')
assert.match(editorSource, /每分钟请求上限最大为 9999/, '每分钟请求上限超过 9999 必须提示')
assert.match(editorSource, /props\.saveLimits\(\{ concurrencyLimit, rpmLimit \}\)/, '两个输入必须通过同一个回调一起保存')
assert.match(tableCellSource, /:save-limits="\(limits\) => saveConcurrency\(account, limits\)"/, '桌面单元格必须把两个上限作为整体提交')
assert.match(mobileCardSource, /<span>限频<\/span>[\s\S]*:save-limits="\(limits\) => saveConcurrency\(account, limits\)"/, '移动卡片「限频」行必须以整体控件提交两个上限')
const listSource = accountListSource
assert.doesNotMatch(listSource, /saveRpmLimit/, '不得新增独立 RPM 编辑回调，两个上限走同一 saveConcurrency')

assert.match(tableCellSource, /columnKey === 'concurrency'[\s\S]*AccountConcurrencyEditor/, '桌面表格并发列必须改用并发编辑器渲染')
assert.match(tableCellSource, /!isAuthorizedAccount\(account\)/, '授权账户并发上限只读，不得提供行内编辑')
assert.doesNotMatch(tableCellSource, /const concurrencyText = computed/, '并发文案与 tooltip 必须收敛进编辑器，不得在单元格重复维护')
assert.match(mobileCardSource, /<AccountConcurrencyEditor[\s\S]*mobile/, '移动卡片限频必须复用同一编辑器（mobile 展示形态）')

assert.match(accountsViewSource, /const editingConcurrencyAccountId = ref<string>\(\)/, '账户列表必须只维护一个当前并发编辑账户 ID')
assert.match(accountsViewSource, /function startConcurrencyEditor\(accountId: string\)[\s\S]*editingConcurrencyAccountId\.value = accountId/, '开始编辑另一行时必须直接切换唯一编辑账户')
assert.match(accountsViewSource, /function startConcurrencyEditor\(accountId: string\)[\s\S]*closePriorityEditor\(\)/, '并发与优先级行内编辑必须互斥，打开一个必须关闭另一个')
assert.match(accountsViewSource, /function refreshData\(\)[\s\S]*closeConcurrencyEditor\(\)[\s\S]*refreshAccountList\(\)/, '刷新桌面列表前必须重置并发编辑态')
assert.match(accountsViewSource, /function refreshMobileAccounts\(\)[\s\S]*closeConcurrencyEditor\(\)[\s\S]*refreshMobileAccountList\(\)/, '刷新移动列表前必须重置并发编辑态')
assert.match(accountsViewSource, /async function saveAccountConcurrency[\s\S]*isAuthorizedAccount\(account\)[\s\S]*expectedConfigRevision/, '限频保存必须校验授权账户只读并携带配置版本')
assert.match(accountsViewSource, /payload\.concurrencyLimit = limits\.concurrencyLimit/, '限频保存必须按实际变化提交并发上限')
assert.match(accountsViewSource, /payload\.rpmLimit = limits\.rpmLimit/, '限频保存必须按实际变化提交每分钟请求上限')
assert.match(accountListSource, /:concurrency-editing="editingConcurrencyAccountId === record\.id"/, '桌面与移动列表必须由唯一账户 ID 决定限频编辑行')

console.log('账户限频行内编辑回归通过：单行受控、双数对展示、正整数/非负整数校验、一体化双输入整体保存、授权账户只读及列表刷新取消均符合预期')
