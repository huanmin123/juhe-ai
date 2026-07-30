type DynamicImport = (specifier: string) => Promise<unknown>

const dynamicImport = new Function('specifier', 'return import(specifier)') as DynamicImport
const nodeFs = await dynamicImport('node:fs') as { readFileSync: (path: string, encoding: 'utf8') => string }
const nodePath = await dynamicImport('node:path') as {
  dirname: (path: string) => string
  resolve: (...segments: string[]) => string
}
const nodeUrl = await dynamicImport('node:url') as { fileURLToPath: (url: string) => string }
const repoRoot = nodePath.resolve(nodePath.dirname(nodeUrl.fileURLToPath(import.meta.url)), '../../../..')
const readRepoFile = (...segments: string[]) => nodeFs.readFileSync(nodePath.resolve(repoRoot, ...segments), 'utf8')

const routerSource = readRepoFile('frontend', 'src', 'router', 'index.ts')
const userHelp = readRepoFile('frontend', 'public', 'help', 'user', 'index.html')
const adminHelp = readRepoFile('frontend', 'public', 'help', 'admin', 'index.html')
const helpIndex = readRepoFile('frontend', 'public', 'help', 'index.html')
const helpCss = readRepoFile('frontend', 'public', 'help', 'help.css')
const helpJs = readRepoFile('frontend', 'public', 'help', 'help.js')

const userRoutes = [
  '/my-chat', '/my-stats', '/my-accounts', '/my-groups', '/my-api-keys', '/my-route-strategies',
  '/my-model-checks', '/my-models', '/my-authorizations', '/my-authorization-team-usage',
  '/my-authorization-user-usage', '/my-teams', '/my-usage-stats', '/my-ai-performance',
  '/my-ai-health', '/my-usage-records', '/my-operation-logs'
] as const

const adminRoutes = [
  '/stats', '/providers', '/proxies', '/accounts', '/groups', '/authorizations',
  '/authorization-team-usage', '/authorization-user-usage', '/authorization-teams', '/api-keys',
  '/model-checks', '/usage-stats', '/ai-performance', '/ai-health', '/usage-records', '/operation-logs',
  '/public-api-logs', '/audit-logs', '/runtime-logs', '/table-monitor', '/system-metrics-stats', '/ip-stats',
  '/response-inspection-policies', '/route-strategies', '/external-integration-sources', '/announcements',
  '/system-accounts', '/settings'
] as const

assertEqual(userRoutes.length, 17, '用户手册路由清单必须维护 17 项')
assertEqual(adminRoutes.length, 28, '管理员手册路由清单必须维护 28 项')

for (const route of [...userRoutes, ...adminRoutes]) {
  assertMatch(routerSource, new RegExp(`path:\\s*['\"]${escapeRegExp(route)}['\"]`), `路由源必须保留 ${route}`)
}

for (const route of userRoutes) {
  assertContains(userHelp, `href="/__aisys__${route}"`, `用户手册必须提供 ${route} 深链`)
}

for (const route of adminRoutes) {
  assertContains(adminHelp, `href="/__aisys__${route}"`, `管理员手册必须提供 ${route} 深链`)
}

for (const term of [
  '普通用户只导入到自己名下', '管理员可在管理账户页选择目标系统账户导入',
  'juhe-ai-account-import v1', 'JSON', '解析预览', '确认导入', '自动创建', '跳过',
  '普通用户不能创建新代理', 'pending_test', '256KB', '最多 50 个账户', '最多 20 个代理',
  '请根据我附上的《juhe-ai AI 账户导入协议 v1》Markdown', '只输出合法 JSON'
]) {
  assertContains(userHelp, term, `用户手册必须说明导入语义：${term}`)
}

for (const term of ['juhe-ai-account-import v1', '256KB', '最多 50 个账户', '最多 20 个代理', '解析预览', 'pending_test']) {
  assertContains(adminHelp, term, `管理员手册必须说明代用户导入语义：${term}`)
}

assertContains(adminHelp, '分组会保存 <code>providerCode</code>，它既是账户集合，也是供应商过滤边界', '管理员手册必须说明分组保存供应商配置')
assertContains(adminHelp, '新建一个同配置的策略，再将 API Key 绑定到新策略', '管理员手册必须说明复制策略的正确做法')
assertNotMatch(`${userHelp}\n${adminHelp}\n${helpIndex}`, /brand-icon">\?/i, '帮助页不得继续使用问号品牌图标')
assertNotMatch(helpCss, /linear-gradient|repeating-linear-gradient|background-size:\s*\d+px\s+\d+px/i, '帮助页不得保留大渐变或网格背景')
assertNotMatch(`${userHelp}\n${adminHelp}`, /接口能力限制|先复制策略再绑定|不保存供应商配置/i, '帮助页不得保留已废弃或错误文案')

assertContains(helpIndex, '/__aisys__/brand-icon.svg', '入口页必须复用品牌图标')
assertContains(userHelp, '/__aisys__/brand-icon.svg', '用户页必须复用品牌图标')
assertContains(adminHelp, '/__aisys__/brand-icon.svg', '管理员页必须复用品牌图标')
assertContains(helpIndex, '跳到正文', '入口页必须有跳到正文链接')
assertContains(userHelp, '跳到正文', '用户页必须有跳到正文链接')
assertContains(adminHelp, '跳到正文', '管理员页必须有跳到正文链接')
assertContains(userHelp, '更新于 2026-07-31', '用户页必须标明更新时间')
assertContains(adminHelp, '更新于 2026-07-31', '管理员页必须标明更新时间')
assertContains(userHelp, 'data-nav-mobile', '用户页必须提供移动目录')
assertContains(adminHelp, 'data-nav-mobile', '管理员页必须提供移动目录')
assertContains(helpCss, '.mobile-nav { display: none;', '样式必须在桌面隐藏移动目录')
assertContains(helpCss, '.mobile-nav { display: block;', '样式必须在手机显示移动目录')
assertContains(helpJs, 'data-help-search', '脚本必须实现无依赖搜索')
assertContains(helpJs, "event.key === 'Escape'", '脚本必须支持 Esc 清空搜索')
assertContains(helpJs, "aria-current", '脚本必须同步激活目录的 aria-current')
assertContains(helpJs, '/__aisys__/api/settings/public', '脚本必须尝试读取公开品牌设置')
assertContains(helpJs, 'catch(function () { setBrand(fallbackBrand); })', '公开品牌读取失败必须回退')
assertContains(userHelp, 'aria-live="polite"', '用户搜索状态必须向辅助技术播报')
assertContains(adminHelp, 'aria-live="polite"', '管理员搜索状态必须向辅助技术播报')

console.log('帮助页内容回归通过：17 个用户路由、28 个管理路由、导入语义与可访问性契约保持一致')

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

function assertEqual(actual: number, expected: number, message: string): void {
  if (actual !== expected) throw new Error(`${message}，实际 ${actual}，期望 ${expected}`)
}

function assertContains(value: string, expected: string, message: string): void {
  if (!value.includes(expected)) throw new Error(`${message}：缺少 ${expected}`)
}

function assertMatch(value: string, pattern: RegExp, message: string): void {
  if (!pattern.test(value)) throw new Error(message)
}

function assertNotMatch(value: string, pattern: RegExp, message: string): void {
  if (pattern.test(value)) throw new Error(message)
}
