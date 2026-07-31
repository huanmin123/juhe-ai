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
assertContains(userHelp, '用户使用手册', '用户页顶部应标明用户使用手册')
assertContains(adminHelp, '管理员使用手册', '管理员页顶部应标明管理员使用手册')
assertContains(userHelp, '专属对话 Key', '用户手册应说明 AI 问答使用专属对话 Key')
assertContains(userHelp, '不替代客户端 API Key', '用户手册不得将 AI 问答当作客户端 Key 验证入口')
assertNotMatch(userHelp, /AI 问答<\/a>做一次调用确认|用自己的 API Key 与模型进行交互验证/, '用户手册不得误导 AI 问答使用客户端 API Key')
for (const term of ['最大单账户排队阈值', '上游接口能力', '账号模型别名', 'n 小时美元额度', '时间计划', 'data-flow-explorer', 'data-flow-step', 'data-flow-detail', '<svg', '<title', '<desc']) {
  assertContains(userHelp, term, `用户手册必须保留字段级指南或 SVG 交互契约：${term}`)
}
for (const term of ['pending_test', '账户电路独立确认失败次数', '策略路由与流量变更', '系统设置与日常运维', 'data-flow-explorer', 'data-flow-step', 'data-flow-detail', '<svg', '<title', '<desc']) {
  assertContains(adminHelp, term, `管理员手册必须保留字段级指南或 SVG 交互契约：${term}`)
}
assertNotMatch(`${userHelp}\n${adminHelp}\n${helpIndex}`, /brand-icon">\?/i, '帮助页不得继续使用问号品牌图标')
assertNotMatch(helpCss, /linear-gradient|repeating-linear-gradient|background-size:\s*\d+px\s+\d+px/i, '帮助页不得保留大渐变或网格背景')
assertContains(helpCss, '--bg: #ffffff', '帮助页必须使用纯白主背景')
assertNotMatch(helpCss, /--bg:\s*#f5f7fa/i, '帮助页不得恢复灰色主背景')
assertContains(helpCss, '.document-title { padding: 0; background: transparent; border: 0; }', '帮助页不得恢复占用首屏的标题横幅')
assertContains(helpCss, '.brand-badge', '帮助页必须为品牌图标加载失败提供视觉回退')
assertContains(helpCss, '.brand-badge[hidden] { display: none; }', '品牌图加载成功后必须隐藏文字徽标')
assertContains(helpCss, '@media (max-width: 900px)', '帮助页必须在平板宽度前切换为单列布局')
assertContains(helpCss, '.flow-explorer', '帮助页必须提供流程图交互容器')
assertContains(helpCss, '.reference-entry', '帮助页必须提供可展开字段参考')
assertContains(helpCss, '.diagram-node', '帮助页必须为 SVG 节点提供交互样式')
assertNotMatch(`${userHelp}\n${adminHelp}`, /接口能力限制|先复制策略再绑定|不保存供应商配置/i, '帮助页不得保留已废弃或错误文案')
assertNotMatch(`${userHelp}\n${adminHelp}\n${helpIndex}`, /<script(?![^>]*\bsrc=)[^>]*>[\s\S]*?<\/script>|\bonload\s*=/i, '帮助页不得引入会被 CSP 阻止的内联脚本或事件处理器')

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
assertContains(helpJs, 'getSearchMatches', '搜索必须按关键词匹配章节')
assertContains(helpJs, "section.scrollIntoView({ behavior: 'smooth', block: 'start' })", '搜索结果必须支持滚动定位章节')
assertNotMatch(helpJs, /section\.hidden\s*=/, '搜索不得通过隐藏正文章节来呈现结果')
assertContains(helpJs, "aria-current", '脚本必须同步激活目录的 aria-current')
assertContains(helpJs, "mobileNav.removeAttribute('open')", '移动目录跳转后必须自动收起')
assertContains(helpJs, 'setFlowStep', '脚本必须支持 SVG 流程节点与步骤控件同步')
assertContains(helpJs, "event.key === 'ArrowRight'", '流程步骤必须支持键盘方向键')
assertContains(helpJs, "document.body.classList.contains('help-gate')", '入口页角色分流必须由外部脚本执行')
assertContains(userHelp, 'aria-live="polite"', '用户搜索状态必须向辅助技术播报')
assertContains(adminHelp, 'aria-live="polite"', '管理员搜索状态必须向辅助技术播报')

console.log('帮助页内容回归通过：17 个用户路由、28 个管理路由、字段级指南、SVG 流程、导入语义与可访问性契约保持一致')

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
