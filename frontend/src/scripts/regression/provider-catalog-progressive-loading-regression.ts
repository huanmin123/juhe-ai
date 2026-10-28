import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

// 模型广场页与供应商目录退役设计 §4.4/§5：原 ProvidersView 弹窗取数时序断言整体改写为
// 锁 ModelPlazaView 的广场页取数时序——进入页面加载一次、归属重载、created/deleted 重载、
// updated 就地替换、手动刷新门、KeepAlive 失活/身份切换不作废手动刷新门禁、无自动刷新。
const view = readFileSync(new URL('../../views/providers/ModelPlazaView.vue', import.meta.url), 'utf8')
const api = readFileSync(new URL('../../api/domains/providers.ts', import.meta.url), 'utf8')

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert.notEqual(startIndex, -1, `缺少源码片段起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码片段终点：${end}`)
  return source.slice(startIndex, endIndex)
}

// 聚合读接口：广场页只走一次聚合加载，不再逐供应商请求目录
assert.match(api, /modelPlaza:.*\/providers\/models\/plaza/, '广场必须使用聚合读接口一次加载')
assert.doesNotMatch(view, /providers\.detail|loadProviderModelCatalogResource|api\.providers\.models\(/, '广场页不得夹带逐供应商目录或完整 provider detail 请求')
assert.match(view, /if \(!isManagementView\.value\) return undefined/, '用户面请求不带参数，归属由后端 pin 本人')
assert.match(view, /return systemAccountId \? \{ viewScope: 'admin', systemAccountId \} : \{ viewScope: 'admin' \}/, '管理面请求必须携带归属窗口参数（空归属只传 viewScope）')

// 进入页面加载一次：先固化默认归属再发起加载，无前置详情请求
const onMountedSource = sourceBetween(view, 'onMounted(() => {', 'onDeactivated(')
assert.match(onMountedSource, /ensureSystemAccountFilter\(\)/, '进入页面必须先用登录态固化默认归属')
assert.match(onMountedSource, /void reloadPlaza\(\)/, '进入页面必须加载一次聚合数据')
assert.doesNotMatch(onMountedSource, /providers\.detail|ensureActiveProviderDetail/, '进入广场不得预取完整 provider detail')

// 默认归属本地回显：不得为回显预取 options
assert.match(view, /systemAccountFilter\.value = currentUser\?\.id \?\? ''/, '管理面默认归属必须本地回显为登录用户')
assert.match(view, /onMissingSelectedIds:[\s\S]*void reloadPlaza\(\)/, '无效归属回退后必须重载聚合数据')

// 迟到响应隔离：请求代次 + 身份签名双门禁
assert.match(view, /const requestSequence = \+\+plazaRequestSequence/, '广场加载必须使用独立请求代次隔离迟到响应')
assert.match(view, /authState\.revision\.value[\s\S]*viewer\?\.id[\s\S]*viewer\?\.role[\s\S]*query\?\.viewScope[\s\S]*query\?\.systemAccountId/, '聚合请求签名必须包含 auth revision、viewer、viewScope 与归属窗口')
assert.match(view, /if \(!isCurrentPlazaRequest\(requestSequence, requestSignature\)\) return/, '广场写回、错误和 loading 必须校验当前身份上下文')
assert.match(view, /finally \{\s*if \(requestSequence === plazaRequestSequence\) loading\.value = false\s*\}/, '旧请求的 finally 只能清除自身仍为当前代次的加载态，不能覆盖新请求')

// 手动刷新门：页头刷新入口保留；无激活自动刷新与轮询
assert.match(view, /@refresh="\(\) => void reloadPlaza\(\)"/, '身份切换后广场页面必须保留手动刷新入口')
assert.match(view, /onActivated\(\(\) => \{\s*pageActive = true\s*\}\)/, '重新激活只能恢复请求门禁，不得自动加载')
assert.doesNotMatch(view, /setInterval|setTimeout\([\s\S]*reloadPlaza/, '广场不得周期轮询或延时自动刷新聚合数据')

// 归属筛选变更、创建/删除成功后重载；编辑成功就地替换
assert.match(view, /function handleSystemAccountFilterChange\(\): void \{\s*void reloadPlaza\(\)\s*\}/, '归属筛选变更必须重载聚合')
const formSavedSource = sourceBetween(view, 'function handleFormSaved', 'function rowActions')
assert.match(formSavedSource, /record\.value = null/, '表单操作成功后必须关闭详情抽屉，避免展示过期快照')
assert.match(formSavedSource, /if \(payload\.kind === 'updated'\) \{\s*plazaItems\.value = plazaItems\.value\.map\(\(item\) => item\.id === payload\.result\.id \? payload\.result : item\)\s*return\s*\}/, '编辑成功必须就地替换同 id 行，不得整表重载')
assert.match(formSavedSource, /\}\s*void reloadPlaza\(\)/, '创建与删除成功后必须重载聚合数据')

// 「查看模型」跳转预选：路由 query 直接映射厂商标签
assert.match(view, /if \(routeProviderCode\) selectedVendor\.value = routeProviderCode/, '进入页面必须按路由 providerCode 预选厂商标签')
assert.match(view, /watch\(\(\) => route\.query\.providerCode[\s\S]*selectVendor\(code\)/, 'KeepAlive 存活期的新跳转也必须同步厂商标签预选')

// KeepAlive 失活/身份切换：作废请求但不清数据、不关手动刷新门禁
assert.match(view, /onDeactivated\(\(\) => \{\s*deactivatePlazaPage\(\)\s*\}\)/, 'KeepAlive 停用必须关闭请求门禁并作废聚合请求')
assert.match(view, /function deactivatePlazaPage\(\): void \{\s*pageActive = false[\s\S]*invalidatePlazaRequests\(\)/, 'KeepAlive 停用必须单独关闭请求门禁并作废请求')
const invalidatePlazaRequestsSource = sourceBetween(view, 'function invalidatePlazaRequests', 'function deactivatePlazaPage')
assert.doesNotMatch(invalidatePlazaRequestsSource, /plazaProviders\.value = \[\]|plazaItems\.value = \[\]/, 'KeepAlive 停用不得清空已加载聚合数据')
assert.doesNotMatch(invalidatePlazaRequestsSource, /pageActive\s*=/, '请求失效不得关闭当前页面的手动刷新门禁')
assert.match(invalidatePlazaRequestsSource, /plazaRequestSequence \+= 1[\s\S]*loading\.value = false/, '请求失效必须复位加载态，避免旧 finally 因代次失效而遗留禁用状态')
const resetPlazaPageStateSource = sourceBetween(view, 'function resetPlazaPageState', 'function ensureSystemAccountFilter')
assert.match(resetPlazaPageStateSource, /plazaProviders\.value = \[\][\s\S]*plazaItems\.value = \[\]/, '身份切换必须通过独立重置路径清空旧聚合数据')
assert.doesNotMatch(resetPlazaPageStateSource, /pageActive\s*=/, '身份切换清空广场状态后必须保留当前页面的手动刷新门禁')
const authRevisionWatcherSource = sourceBetween(view, 'watch(() => authState.revision.value', 'async function reloadPlaza')
assert.match(authRevisionWatcherSource, /resetPlazaPageState\(\)[\s\S]*invalidateSystemAccountOptions\(\)[\s\S]*systemAccountFilter\.value = ''[\s\S]*systemAccounts\.value = \[\][\s\S]*resetSystemAccountOptionsSearch\(\)/, '身份切换必须使归属选项失效并清空归属筛选与旧数据')
assert.doesNotMatch(authRevisionWatcherSource, /reloadPlaza/, '身份切换必须清空旧聚合数据，且不自动加载新身份数据')

// 加载态与操作区门禁：刷新期间拒绝旧行操作
assert.match(view, /function rowActions\(record: ProviderModelPricing\): RowActionItem\[\] \{\s*if \(loading\.value\) return \[\]/, '聚合刷新期间不得保留旧行操作')
assert.match(view, /function handleModelAction\(key: string, record: ProviderModelPricing\) \{\s*if \(loading\.value\) return/, '聚合刷新期间必须在事件入口拒绝旧行操作')

// 状态优先级与错误透出：loading > 错误/空 > 筛选空；错误原文透出
assert.match(view, /v-if="loading" class="model-card-grid"/, '加载期必须只渲染骨架，不得闪现筛选空态')
assert.match(view, /v-else-if="loadError \|\| !plazaItems\.length"/, '错误与空数据共用空态分支（优先级 loading > 错误/空 > 筛选空）')
assert.match(view, /v-else-if="!filteredModels\.length" class="page-empty-card" description="没有匹配的模型"/, '筛选无结果必须与加载/错误空态区分')
assert.match(view, /extractModelErrorMessage\(error, '加载模型广场失败'\)/, '加载失败必须透出错误信息')
console.log('provider-catalog-progressive-loading-regression: ok')
