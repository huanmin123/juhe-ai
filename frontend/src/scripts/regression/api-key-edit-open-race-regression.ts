import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const modalSource = readFileSync(
  fileURLToPath(new URL('../../views/api-keys/ApiKeyEditModal.vue', import.meta.url)),
  'utf8'
)

assert.doesNotMatch(
  modalSource,
  /loadUserReferenceData|prewarmCreateDefaultRouteStrategy/,
  'API Key 弹窗不得在打开时补发共享引用/bootstrap 请求'
)
assert.match(modalSource, /getCachedUserReferenceData/, '新建弹窗必须只同步读取登录后共享默认路由缓存')
assert.match(modalSource, /@cancel="handleModalCancel"/, '取消关闭必须立即失效当前弹窗会话')
assert.match(modalSource, /@after-close="handleModalAfterClose"/, '程序化关闭完成后也必须失效当前弹窗会话')

const openCreateSource = sourceBetween(modalSource, 'function openCreate', 'function openEdit')
assert.doesNotMatch(openCreateSource, /async|await|loadRouteStrategyOptions|loadUserReferenceData/, '新建必须同步开窗且零候选/引用网络')
assertOrdered(openCreateSource, [
  'const createScopeParams = normalizedScopeParams(props.scopeParams)',
  'const defaultStrategy = cachedDefaultRouteStrategy(createScopeParams)',
  'beginModalSession(createScopeParams)',
  'Object.assign(form',
  'editingBaseline = undefined',
  'modalOpen.value = true'
], '新建必须先固定 owner/scope 会话，再用缓存快照同步开窗')

const openEditSource = sourceBetween(modalSource, 'function openEdit', 'function apiKeyOperationScopeParams')
assert.doesNotMatch(openEditSource, /async|await|loadRouteStrategyOptions|loadUserReferenceData/, '编辑必须同步开窗且零候选/引用网络')
assertOrdered(openEditSource, [
  'const editScopeParams = apiKeyOperationScopeParams(apiKey)',
  'editingRevision = apiKey.revision',
  'beginModalSession(editScopeParams)',
  'routeStrategy: apiKeyRouteStrategySelection(apiKey)',
  'editingBaseline = currentApiKeyEditableSnapshot',
  'modalOpen.value = true'
], '编辑必须固定列表行 owner/scope，并直接使用列表回填的路由与 revision')

const saveSource = sourceBetween(modalSource, "const saveApiKey = submitAction", 'function quotaLimitsPayload')
assert.match(saveSource, /const modalSession = activeModalSession\.value[\s\S]*!isCurrentModalSession\(modalSession\)/, '保存前必须确认打开时会话仍属于当前 owner/scope')
assert.match(saveSource, /const operationScopeParams = modalSessionScopeParams\(modalSession\)/, '保存作用域必须来自打开时不可变会话')
assert.doesNotMatch(saveSource, /props\.scopeParams/, '保存不得读取可能已切换 owner 的实时 props scope')
assert.equal((saveSource.match(/}, operationScopeParams\)/g) ?? []).length, 2, '编辑和新建都必须提交到会话捕获的 owner')

const optionsLoaderSource = sourceBetween(modalSource, 'async function loadRouteStrategyOptions', 'function routeStrategyOptionsRequestKey')
assert.match(optionsLoaderSource, /const modalSession = activeModalSession\.value[\s\S]*!isCurrentModalSession\(modalSession\)/, '候选加载必须绑定一次弹窗会话')
assert.match(optionsLoaderSource, /const requestScopeKey = modalSession\.routeStrategyOptionsScopeKey/, '候选请求必须使用打开时捕获的 owner scope')
assert.match(optionsLoaderSource, /isCurrent: \(\) => requestToken === routeStrategyOptionsRequestToken[\s\S]*isCurrentModalSession\(modalSession\)/, '候选响应写入前必须同时校验请求代次和弹窗会话')
assert.match(optionsLoaderSource, /requestToken !== routeStrategyOptionsRequestToken \|\| !isCurrentModalSession\(modalSession\)/, '旧 owner/scope 的失败响应也不得污染新弹窗消息')
assert.match(optionsLoaderSource, /requestToken === routeStrategyOptionsRequestToken && routeStrategyOptionsLoadingKey === requestKey/, '旧请求 finally 不得清除新会话的 loading/promise')

const dropdownSource = sourceBetween(modalSource, 'function handleRouteStrategyDropdown', 'function markRouteStrategyTouched')
assert.match(dropdownSource, /open && !visibleRouteStrategyOptions\.value\.length[\s\S]*loadRouteStrategyOptions\(\)/, '只有用户展开路由下拉才允许首次请求候选')
assert.equal(
  (modalSource.match(/loadRouteStrategyOptions\(/g) ?? []).length,
  3,
  '候选加载只能出现在函数定义、下拉展开和用户搜索回调中'
)

const sessionSource = sourceBetween(modalSource, 'function beginModalSession', 'function apiKeyRouteStrategySelection')
assert.match(sessionSource, /generation: \+\+modalSessionGeneration/, '每次打开必须生成不可复用的会话代次')
assert.match(sessionSource, /parentScopeKey: currentParentScopeKey\(\)/, '会话必须记录打开时父页面 owner scope')
assert.match(sessionSource, /routeStrategyOptionsRequestToken \+= 1/, '关闭必须使进行中的候选请求失效')
assert.match(sessionSource, /clearRouteStrategyOptionsSearchTimer\(\)/, '关闭必须取消尚未触发的远程搜索')
assert.match(sessionSource, /activeModalSession\.value === modalSession[\s\S]*modalSession\.parentScopeKey === currentParentScopeKey\(\)/, '响应落地必须同时命中会话对象与当前 owner scope')

assert.match(
  modalSource,
  /watch\(currentParentScopeKey,[\s\S]*invalidateModalSession\(\{ clearOptions: true \}\)[\s\S]*modalOpen\.value = false/,
  '父页面切换 owner/scope 时必须关闭弹窗并清除旧候选'
)
assert.match(modalSource, /onBeforeUnmount\(\(\) => invalidateModalSession\(\{ clearOptions: true \}\)\)/, '组件卸载必须失效请求、timer 与旧候选')

console.log('API Key 弹窗零预加载与 owner/scope/close 晚到隔离回归通过')

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex)
  assert.notEqual(startIndex, -1, `缺少源码起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码终点：${end}`)
  return source.slice(startIndex, endIndex)
}

function assertOrdered(source: string, fragments: string[], message: string): void {
  let previousIndex = -1
  for (const fragment of fragments) {
    const currentIndex = source.indexOf(fragment)
    assert(currentIndex > previousIndex, `${message}；顺序片段缺失或错位：${fragment}`)
    previousIndex = currentIndex
  }
}
