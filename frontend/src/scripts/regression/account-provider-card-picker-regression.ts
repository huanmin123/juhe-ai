import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const selectorSource = readFileSync(
  fileURLToPath(new URL('../../views/accounts/AccountFormSelector.vue', import.meta.url)),
  'utf8'
)
const modalSource = readFileSync(
  fileURLToPath(new URL('../../views/accounts/AccountEditModal.vue', import.meta.url)),
  'utf8'
)
const accountsViewSource = readFileSync(
  fileURLToPath(new URL('../../views/accounts/AccountsView.vue', import.meta.url)),
  'utf8'
)
const mediaFormSource = readFileSync(
  fileURLToPath(new URL('../../views/accounts/AccountMediaForm.vue', import.meta.url)),
  'utf8'
)

const selectorTemplate = componentTemplate(selectorSource)
const selectorScript = componentScript(selectorSource)

// ---- ① 新建态卡片墙：搜索、grid、ProviderIcon、卡片点击只 emit 既有事件 ----

const cardGridSource = sourceBetween(selectorTemplate, '<template v-else>', '</section>')
assert.match(selectorTemplate, /v-model:value="searchKeyword"/, '卡片墙必须提供搜索框绑定')
assert.match(selectorTemplate, /placeholder="搜索供应商名称或代码"/, '搜索框必须可辨识')
assert.match(cardGridSource, /class="provider-card-grid"/, '卡片墙必须是 grid 布局')
assert.match(cardGridSource, /v-for="provider in visibleProviders"/, '卡片必须按过滤+排序后的清单渲染')
assert.match(cardGridSource, /@click="handleProviderCardClick\(provider\.code\)"/, '卡片必须可点击')
assert.match(cardGridSource, /accountCountText\(provider\.code\)/, '卡片必须渲染账户计数文案')
assert.match(cardGridSource, /没有匹配的供应商/, '搜索无结果必须给空态文案')

// 横向卡片解剖：左 logo 块（ProviderIcon），右侧 名称→支持的账户类型→账户数 纵向排列
const cardItemSource = sourceBetween(cardGridSource, '<button', '</button>')
assert.match(cardItemSource, /class="provider-card-logo"[\s\S]*?<ProviderIcon[\s\S]*?:size="26"/, '左侧 logo 块必须承载 ProviderIcon（size 26）')
assert.match(cardItemSource, /class="provider-card-body"[\s\S]*?provider-card-name[\s\S]*?provider-card-types[\s\S]*?provider-card-count/, '卡片主体必须按 名称→账户类型→账户数 排列')
assert.doesNotMatch(cardItemSource, /provider\.description/, '卡片按新设计不再渲染供应商描述')

const countTextSource = sourceBetween(selectorSource, 'function accountCountText(', '</script>')
assert.match(countTextSource, /个账户/, '有账户的卡片必须显示「N 个账户」')
assert.match(countTextSource, /还没有账户/, '零账户卡片必须显示「还没有账户」')

const visibleProvidersSource = sourceBetween(selectorScript, 'const visibleProviders = computed', '// 选项只展示接入方式短码')
assert.match(selectorScript, /const enabledProviders = computed\(\(\) => props\.providers\.filter\(\(provider\) => provider\.enabled\)\)/, '卡片墙只展示 enabled 供应商')
assert.match(visibleProvidersSource, /provider\.name\.toLowerCase\(\)\.includes\(keyword\)[\s\S]*?provider\.code\.toLowerCase\(\)\.includes\(keyword\)/, '搜索必须按名称与 code 不区分大小写过滤')
assert.match(visibleProvidersSource, /\.sort\(\(a, b\) => b\.count - a\.count \|\| a\.index - b\.index\)/, '有账户必须在前按数量降序')
assert.match(visibleProvidersSource, /const withoutAccounts = ranked[\s\S]*?a\.index - b\.index/, '无账户供应商必须保持传入顺序')
assert.match(visibleProvidersSource, /props\.accountCounts\[provider\.code\] \?\? 0/, '账户计数必须来自 accountCounts prop')

const cardClickSource = sourceBetween(selectorScript, 'function handleProviderCardClick(', 'function providerAccountTypeTags(')
assert.match(cardClickSource, /emit\('select-provider', code\)/, '卡片点击必须 emit 既有 select-provider 事件')
assert.match(cardClickSource, /pickerExpanded\.value = false/, '卡片点击后必须收起为摘要条')
assert.doesNotMatch(cardClickSource, /loadCurrentProviderModelOptions|providers\.definitions|api\./, '卡片点击不得预取模型目录或全量 definitions')

const typeTagsSource = sourceBetween(selectorScript, 'function providerAccountTypeTags(', 'function accountCountText(')
assert.match(typeTagsSource, /provider\.protocolProfiles\.flatMap\(\(profile\) => profile\.accountTypes\)/, '接入方式标签必须来自 protocolProfiles.accountTypes')
assert.match(typeTagsSource, /: provider\.accountTypes/, '接入方式标签必须回退 provider.accountTypes')
assert.match(typeTagsSource, /accountTypeText\(type\)/, '接入方式标签文案必须复用既有 accountTypeText')

// ---- ② 编辑/克隆态分支不含卡片墙，结构与重构前一致 ----

const editingBranchSource = sourceBetween(selectorTemplate, '<template v-if="editing">', '<template v-else>')
assert.match(editingBranchSource, /:disabled="editing"/, '编辑态接入方式 segmented 必须保持 disabled')
assert.match(editingBranchSource, /<a-tag color="blue">/, '编辑态必须保留供应商徽标 tag')
assert.doesNotMatch(editingBranchSource, /provider-card|ProviderIcon|provider-summary|searchKeyword/, '编辑/克隆态不得出现卡片墙、摘要条或搜索框')

// ---- 展开状态：弹窗每次打开复位为展开，「更换」重新展开 ----

const expandResetSource = sourceBetween(selectorScript, 'watch(() => props.open', 'const enabledProviders')
assert.match(expandResetSource, /if \(!next\) return[\s\S]*?pickerExpanded\.value = !props\.cloning/, '弹窗打开复位必须区分克隆：新建展开卡片墙，克隆保持收起直接进入阶段二')
assert.match(expandResetSource, /emit\('picker-expanded-change', !props\.cloning\)/, '弹窗打开复位必须按「新建展开/克隆收起」语义通知上层')
assert.match(cardGridSource, /v-if="pickerExpanded"/, '卡片墙必须受展开状态控制')
assert.match(cardGridSource, /@click="handlePickerExpand">更换/, '「更换」必须重新展开卡片墙')

// ---- 两阶段交互（选择器侧）：阶段一=独立供应商选择，阶段二=已选摘要 + 配置 ----

assert.match(selectorScript, /\(event: 'picker-expanded-change', expanded: boolean\): void/, '选择器必须声明展开状态外报事件')
const pickerExpandSource = sourceBetween(selectorScript, 'function handlePickerExpand(', 'function providerAccountTypeTags(')
assert.match(pickerExpandSource, /pickerExpanded\.value = true[\s\S]*?emit\('picker-expanded-change', true\)/, '「更换」展开时必须通知上层隐藏配置区')
const cardClickPhaseSource = sourceBetween(selectorScript, 'function handleProviderCardClick(', 'function providerAccountTypeTags(')
assert.match(cardClickPhaseSource, /emit\('picker-expanded-change', false\)/, '点选卡片进入配置阶段必须通知上层展示配置区')

// ---- 对外契约：props 与事件名不变，切换下拉被卡片墙替代 ----

for (const prop of [
  'accountType: AccountType',
  'accountTypeChoices: AccountTypeChoice[]',
  'editing: boolean',
  'providerCode: string',
  'providers: ProviderDefinition[]',
  'selectedProtocolProfile?: ProviderProtocolProfileDefinition',
  'selectedProvider?: ProviderDefinition'
]) {
  assert.ok(selectorScript.includes(prop), `AccountFormSelector 既有 prop 不得缺失或改名：${prop}`)
}
assert.match(selectorScript, /\(event: 'select-provider', providerCode: string\): void/, 'select-provider 事件名不得变化')
assert.match(selectorScript, /\(event: 'select-type-choice', value: string\): void/, 'select-type-choice 事件名不得变化')
assert.doesNotMatch(selectorSource, /a-dropdown|a-menu|handleProviderMenuClick|providerAccountTypeCount/, '切换下拉必须被卡片墙替代且不留死代码')

// ---- ③ AccountEditModal：计数改读时聚合 prop，卡片相关处理无预取调用 ----

assert.match(modalSource, /accountCounts\?: Record<string, number>/, 'AccountEditModal 必须以 accountCounts prop 接收卡片墙全量计数')
assert.match(modalSource, /accountCounts: \(\) => \(\{\}\)/, 'accountCounts prop 必须缺省空对象（计数未加载时不显示旧值）')
assert.doesNotMatch(modalSource, /const accountCounts = computed/, '卡片计数不得再由弹窗内部从已加载账户行归并')
assert.doesNotMatch(modalSource, /accounts\?: AccountListItem\[\]/, 'accounts prop 必须移除（计数改为 provider-counts 读时聚合）')

const modalSelectorSlice = sourceBetween(modalSource, '<AccountFormSelector', '/>')
assert.match(modalSelectorSlice, /:account-counts="accountCounts"/, '对话域 AccountFormSelector 必须接收 accountCounts')
assert.match(modalSelectorSlice, /:open="open"/, '对话域 AccountFormSelector 必须接收 open 以复位展开状态')
assert.match(modalSelectorSlice, /@select-provider="\$emit\('select-provider', \$event\)"/, 'select-provider 事件必须原样上抛')
assert.match(modalSelectorSlice, /@select-type-choice="\$emit\('select-type-choice', \$event\)"/, 'select-type-choice 事件必须原样上抛')

const modalMediaFormSlice = sourceBetween(modalSource, '<AccountMediaForm', '/>')
assert.match(modalMediaFormSlice, /:account-counts="accountCounts"/, '媒体域 AccountMediaForm 必须接收 accountCounts')

// 既有 open-watch 默认名逻辑与第 3 步探活文案不受影响
assert.match(modalSource, /if \(!props\.editing && !props\.form\.name\.trim\(\)\) \{[\s\S]*?defaultAccountNameFromBaseUrl\(props\.form\.baseUrl\)/, 'open-watch 默认名逻辑不得移动')
assert.equal((modalSource.match(/持续恢复探活/g) ?? []).length, 2, '第 3 步「持续恢复探活」文案位置不得变化')

// ---- ④ provider-counts 读时聚合全链：AccountsView 打开创建弹窗拉一次，prop 直达卡片 ----

const accountsViewModalSlice = sourceBetween(accountsViewSource, '<AccountEditModal', '/>')
assert.match(accountsViewModalSlice, /:account-counts="providerAccountCounts"/, 'AccountsView 必须向 AccountEditModal 传入读时聚合的全量计数')
assert.doesNotMatch(accountsViewModalSlice, /:accounts="/, 'AccountsView 不得再把已加载列表行传给弹窗当计数源')

const countsLoaderSource = sourceBetween(accountsViewSource, 'async function loadProviderAccountCounts(', 'watch(modalOpen')
assert.match(countsLoaderSource, /api\.accounts\.providerCounts\(params\)/, '管理面必须调用 accounts.providerCounts 读时聚合接口')
assert.match(countsLoaderSource, /api\.myAccounts\.providerCounts\(params\)/, '用户面必须调用 myAccounts.providerCounts 读时聚合接口')
assert.match(countsLoaderSource, /domain: capabilityDomain\.value/, '计数请求必须携带当前能力域')
assert.match(countsLoaderSource, /systemAccountId: scope\.systemAccountId/, '管理面计数必须随 systemAccountId 范围参数')
assert.match(countsLoaderSource, /message\.error\(extractApiErrorMessage\(error, '加载供应商账户计数失败'\)\)/, '计数拉取失败必须显式报错，不得静默显示 0')

const countsWatchSource = sourceBetween(accountsViewSource, 'watch(modalOpen', 'watch(')
assert.match(countsWatchSource, /if \(!editingId\.value\) void loadProviderAccountCounts\(\)/, '只有创建态打开弹窗才拉取计数（编辑态无卡片墙）')

assert.match(mediaFormSource, /accountCounts\?: Record<string, number>/, 'AccountMediaForm 必须声明 accountCounts 透传 prop')
const mediaSelectorSlice = sourceBetween(mediaFormSource, '<AccountFormSelector', '/>')
assert.match(mediaSelectorSlice, /:account-counts="accountCounts"/, '媒体域内嵌 AccountFormSelector 必须接收 accountCounts')
assert.match(mediaSelectorSlice, /:open="open"/, '媒体域内嵌 AccountFormSelector 必须接收 open 以复位展开状态')
assert.doesNotMatch(mediaSelectorSlice, /loadCurrentProviderModelOptions|providers\.definitions|api\./, '媒体域供应商选择不得引入预取调用')

// ---- ⑤ 两阶段交互（弹窗/媒体侧）：阶段一弹窗只呈现供应商卡片 ----

assert.match(modalSource, /const providerPickerExpanded = ref\(true\)/, '弹窗必须持有卡片墙展开状态')
assert.match(modalSource, /const providerSelectionStage = computed\(\(\) => !props\.editing && !props\.cloning && providerPickerExpanded\.value\)/, '弹窗必须以「卡片墙展开且非编辑/克隆」判定阶段一（克隆豁免阶段一）')
assert.match(modalSelectorSlice, /@picker-expanded-change="providerPickerExpanded = \$event"/, '弹窗必须监听选择器展开状态事件')
assert.match(modalMediaFormSlice, /@picker-expanded-change="providerPickerExpanded = \$event"/, '媒体域 AccountMediaForm 必须把展开状态事件上抛弹窗')
assert.match(modalSource, /v-show="editing \|\| \(wizardStep === 1 && !providerPickerExpanded\)"/, '对话域凭据配置区必须在卡片墙展开期间隐藏')
assert.match(modalSource, /<span v-if="!editing \|\| loading">\{\{ displayTitle \}\}<\/span>/, '标题槽必须经 displayTitle 呈现')
assert.match(modalSource, /const displayTitle = computed\(\(\) => \(providerSelectionStage\.value \? '添加账户' : props\.title\)\)/, '阶段一标题必须是中性的「添加账户」')
assert.match(modalSource, /:footer="modalFooter"/, '阶段一必须隐藏底部按钮区')
assert.match(modalSource, /const modalFooter = computed\(\(\) => \(providerSelectionStage\.value \? null : undefined\)\)/, '底部按钮区必须由 modalFooter 控制而非写死')
assert.match(modalSource, /v-if="!editing && !providerSelectionStage" class="wizard-steps"/, '阶段一必须隐藏向导步骤指示')

assert.match(mediaFormSource, /const providerPickerExpanded = ref\(true\)/, '媒体表单必须持有卡片墙展开状态')
assert.match(mediaSelectorSlice, /@picker-expanded-change="handlePickerExpandedChange"/, '媒体表单必须监听内嵌选择器展开状态事件')
assert.match(mediaFormSource, /function handlePickerExpandedChange\(expanded: boolean\): void \{[\s\S]*?providerPickerExpanded\.value = expanded[\s\S]*?emit\('picker-expanded-change', expanded\)/, '媒体表单必须同步本地展开状态并上抛弹窗')
assert.match(mediaFormSource, /<div v-show="!providerPickerExpanded" class="media-section-label">/, '阶段一必须隐藏「凭据」小节头')
assert.equal((mediaFormSource.match(/v-show="!providerPickerExpanded"/g) ?? []).length, 4, '媒体域段1凭据标签与字段、段2/段3配置区必须在卡片墙展开期间隐藏')

// ---- ⑥ 克隆态豁免阶段一：克隆打开即阶段二（来源供应商已预选），「更换」只在表单内展开卡片墙 ----

assert.match(selectorScript, /cloning\?: boolean/, 'AccountFormSelector 必须声明 cloning prop')
assert.match(modalSource, /cloning\?: boolean/, 'AccountEditModal 必须声明 cloning prop')
assert.match(mediaFormSource, /cloning\?: boolean/, 'AccountMediaForm 必须声明 cloning 透传 prop')
assert.match(modalSelectorSlice, /:cloning="cloning"/, '对话域 AccountFormSelector 必须接收 cloning')
assert.match(modalMediaFormSlice, /:cloning="cloning"/, '媒体域 AccountMediaForm 必须接收 cloning')
assert.match(accountsViewModalSlice, /:cloning="Boolean\(cloningSourceId\)"/, 'AccountsView 必须以 cloningSourceId 驱动弹窗克隆态')
assert.match(mediaSelectorSlice, /:cloning="cloning"/, '媒体域内嵌 AccountFormSelector 必须接收克隆透传')

console.log('账户供应商卡片墙回归通过：阶段一弹窗只呈现供应商卡片（中性标题/无向导/无凭据头/无底部按钮）+ 横向卡片（logo/名称/账户类型/账户数）+ 全量计数读时聚合（provider-counts 双面接口，不受分页筛选影响）+ 搜索/排序 + 编辑态零卡片墙 + 克隆态豁免阶段一（打开即阶段二，更换仅在表单内展开）+ 卡片点击零预取')

function componentTemplate(source: string): string {
  return source.slice(source.indexOf('<template>'), source.lastIndexOf('</template>'))
}

function componentScript(source: string): string {
  return source.slice(source.indexOf('<script setup'), source.indexOf('</script>'))
}

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert.notEqual(startIndex, -1, `未找到源码起点：${start}`)
  assert.notEqual(endIndex, -1, `未找到源码终点：${end}`)
  return source.slice(startIndex, endIndex)
}
