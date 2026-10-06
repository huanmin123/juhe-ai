type DynamicImport = (specifier: string) => Promise<unknown>

const dynamicImport = new Function('specifier', 'return import(specifier)') as DynamicImport
const nodeFs = await dynamicImport('node:fs') as {
  readFileSync: (path: string, encoding: 'utf8') => string
  existsSync: (path: string) => boolean
}
const nodePath = await dynamicImport('node:path') as {
  dirname: (path: string) => string
  resolve: (...segments: string[]) => string
  join: (...segments: string[]) => string
}
const nodeUrl = await dynamicImport('node:url') as { fileURLToPath: (url: string) => string }
const nodeChildProcess = await dynamicImport('node:child_process') as {
  spawnSync: (command: string, args: string[], options: { stdio: 'pipe' }) => { status: number | null; stdout: string; stderr: string }
}
const repoRoot = nodePath.resolve(nodePath.dirname(nodeUrl.fileURLToPath(import.meta.url)), '../../../..')
const readRepoFile = (...segments: string[]) => nodeFs.readFileSync(nodePath.resolve(repoRoot, ...segments), 'utf8')
const helpRoot = nodePath.resolve(repoRoot, 'frontend', 'public', 'help')

const routerSource = readRepoFile('frontend', 'src', 'router', 'index.ts')
const viteConfigSource = readRepoFile('frontend', 'vite.config.ts')
const manifest = JSON.parse(readRepoFile('frontend', 'public', 'help', 'manifest.json')) as {
  updatedAt: string
  title: string
  sections: Array<{ id: string; title: string; docs: Array<{ id: string; title: string; file: string }> }>
}
const helpIndex = readRepoFile('frontend', 'public', 'help', 'index.html')
const helpCss = readRepoFile('frontend', 'public', 'help', 'help.css')
const helpJs = readRepoFile('frontend', 'public', 'help', 'help.js')
const userShell = readRepoFile('frontend', 'public', 'help', 'user', 'index.html')
const adminShell = readRepoFile('frontend', 'public', 'help', 'admin', 'index.html')

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
  '/response-inspection-policies', '/route-strategies', '/external-integration-sources',
  '/announcements', '/system-accounts', '/settings'
] as const

assertEqual(userRoutes.length, 17, '用户帮助路由清单必须维护 17 项')
assertEqual(adminRoutes.length, 28, '管理员帮助路由清单必须维护 28 项')
for (const route of [...userRoutes, ...adminRoutes]) {
  assertMatch(routerSource, new RegExp(`path:\\s*['"]${escapeRegExp(route)}['"]`), `路由源必须保留 ${route}`)
}

// 渲染管线一致性：源 md 与生成物 rendered/ 必须同步入库（--check 由渲染脚本自身钉住）
const rendered = nodeChildProcess.spawnSync(
  process.execPath,
  [nodePath.join(repoRoot, 'frontend', 'scripts', 'render-help-docs.mjs'), '--check'],
  { stdio: 'pipe' }
)
assertEqual(rendered.status ?? -1, 0, `渲染物一致性校验必须通过：${rendered.stderr.toString().trim()}`)

// manifest 结构：单一手册双分区（接入与调用 / 管理运维，无角色门槛）、每篇 id/file 唯一、md 与 rendered 生成物都在
const usageSection = manifest.sections.find((section) => section.id === 'usage')
const adminSection = manifest.sections.find((section) => section.id === 'admin')
assertEqual(manifest.sections.length, 2, '手册必须恰好两个分区（接入与调用/管理运维）')
const userDocs = usageSection?.docs ?? []
const adminDocs = adminSection?.docs ?? []
assertEqual(userDocs.length, 18, '接入与调用分区必须维护 18 篇')
assertEqual(adminDocs.length, 8, '管理运维分区必须维护 8 篇')
const seenIds = new Set<string>()
const userMdBundle: string[] = []
const adminMdBundle: string[] = []
for (const section of manifest.sections) {
  for (const doc of section.docs) {
    assertFalse(seenIds.has(doc.id), `篇目 id 不得重复：${doc.id}`)
    seenIds.add(doc.id)
    const mdPath = nodePath.join(helpRoot, 'docs', doc.file)
    const htmlPath = nodePath.join(helpRoot, 'rendered', doc.file.replace(/\.md$/, '.html'))
    assertTrue(nodeFs.existsSync(mdPath), `篇目源文件必须存在：${doc.file}`)
    assertTrue(nodeFs.existsSync(htmlPath), `篇目渲染物必须存在：${doc.file}`)
    const md = readRepoFile('frontend', 'public', 'help', 'docs', ...doc.file.split('/'))
    assertMatch(md.replace(/\r\n/g, '\n'), /^# .+/, `篇目首行必须是 h1 标题：${doc.file}`)
    const h1 = /^# (.+)$/m.exec(md.replace(/\r\n/g, '\n'))?.[1]?.trim() ?? ''
    assertEqual(h1, doc.title, `篇目 h1 必须与 manifest 标题一致：${doc.file}`)
    // 行内代码与围栏代码块里的尖括号（如 <API Key>）是合法占位符；只拦正文中真正的内联 HTML 标签
    const prose = md.replace(/```[\s\S]*?```/g, '').replace(/`[^`\n]*`/g, '')
    assertNotMatch(prose, /<\w+[^>]*>/, `篇目正文不得写内联 HTML（渲染器会剥离）：${doc.file}`)
    if (doc.file.startsWith('user/')) userMdBundle.push(md)
    else adminMdBundle.push(md)
  }
}

// 深链资产：旧手册 17 条用户页与 28 条管理页深链必须全部出现在新篇目中
const userBundle = userMdBundle.join('\n')
const adminBundle = adminMdBundle.join('\n')
for (const route of userRoutes) {
  // 管理台挂在 /__aisys__/ base 下，帮助文档中的深链必须带前缀，裸 /my-* 会 404
  assertContains(userBundle, `](/__aisys__${route})`, `用户篇目必须保留 ${route} 管理页深链`)
}
for (const route of adminRoutes) {
  // 管理员篇目沿用旧手册的完整管理面路径形态（/__aisys__/ 前缀）
  assertContains(adminBundle, `](/__aisys__${route})`, `管理员篇目必须保留 ${route} 管理页深链`)
}
// 语义资产：跨协议口径与高危语义必须随篇目保留
assertContains(userBundle, '专属对话 Key', '用户篇目必须说明 AI 问答使用专属对话 Key')
assertContains(userBundle, '不能替代客户端 Key 的接入验证', '用户篇目不得把 AI 问答当作客户端 Key 验证入口')
assertContains(userBundle, '`active`（检查通过且启用）是进入候选的必要条件，不保证当前一定可调度', '用户篇目不得把 active 写成充分条件')
assertContains(adminBundle, 'juhe-ai-account-import v1', '管理员篇目必须说明代用户导入协议')
assertContains(adminBundle, 'pending_test', '管理员篇目必须保留 pending_test 语义')
assertContains(adminBundle, '最多 50 个账户', '管理员篇目必须保留导入限额')
assertContains(adminBundle, '分组保存 `providerCode`，它既是账户集合，也是供应商过滤边界', '管理员篇目必须说明分组保存供应商边界')
assertContains(adminBundle, '新建一个同配置的策略', '管理员篇目必须说明复制策略的正确做法')

// 页面壳契约：单一手册（user/ 与 admin/ 路径同壳）、导航挂载点、正文挂载点、下载链接
assertContains(userShell, 'data-manual-title', '用户路径壳必须提供手册标题挂载点')
assertContains(adminShell, 'data-manual-title', '管理员路径壳必须提供手册标题挂载点')
assertEqual(userShell, adminShell, '两个路径必须放同一份手册壳（单一手册，无角色区分）')
for (const [shell, label] of [[userShell, '用户'], [adminShell, '管理员']] as const) {
  assertContains(shell, 'data-doc-nav', `${label}壳必须提供篇目导航挂载点`)
  assertContains(shell, 'data-doc-body', `${label}壳必须提供正文挂载点`)
  assertContains(shell, 'data-download-link', `${label}壳必须提供 Markdown 下载链接`)
  assertContains(shell, ' download>', `${label}壳的下载链接必须带 download 属性`)
  assertContains(shell, 'data-doc-pager', `${label}壳必须提供上下篇导航`)
  assertContains(shell, 'aria-live="polite"', `${label}壳的搜索状态必须向辅助技术播报`)
  assertContains(shell, '跳到正文', `${label}壳必须有跳到正文链接`)
  assertContains(shell, '/__aisys__/brand-icon.svg', `${label}壳必须复用品牌图标`)
}

// 入口门控页
assertContains(helpIndex, 'help-gate', '入口页必须保留角色分流门控')
assertContains(helpIndex, '/__aisys__/brand-icon.svg', '入口页必须复用品牌图标')

// 脚本契约：manifest 驱动导航、hash 路由、门控分流、Esc 清空搜索
assertContains(helpJs, "contains('help-gate')", '入口页角色分流必须由外部脚本执行')
assertContains(helpJs, "fetch('../manifest.json')", '文档站必须由 manifest 驱动篇目')
assertContains(helpJs, 'hashchange', '文档站必须支持 hash 路由深链')
assertContains(helpJs, "'#/doc/'", '文档站路由必须使用 #/doc/<id> 形态')
assertContains(helpJs, '../rendered/', '正文必须加载预渲染生成物')
assertContains(helpJs, "event.key === 'Escape'", '搜索必须支持 Esc 清空')
assertContains(helpJs, 'aria-busy', '正文加载必须播报忙闲状态')

// 渲染产物安全：生成物是受信自家内容，但不得携带脚本或内联事件
for (const audience of ['user', 'admin'] as const) {
  const dir = nodePath.join(helpRoot, 'rendered', audience)
  for (const name of nodeFs.readdirSync(dir)) {
    const html = nodeFs.readFileSync(nodePath.join(dir, name), 'utf8')
    assertMatch(html, /^<!-- Code generated by scripts\/render-help-docs\.mjs/, `渲染物必须带生成头：${audience}/${name}`)
    assertNotMatch(html, /<script/i, `渲染物不得包含脚本：${audience}/${name}`)
    assertNotMatch(html, /\son\w+=/i, `渲染物不得包含内联事件：${audience}/${name}`)
  }
}

// 样式契约：对齐主应用设计语言 + 文档站关键布局
assertContains(helpCss, '--page: #f5f7fb', '帮助页必须以 #f5f7fb 为页面底色并对齐主应用')
assertContains(helpCss, '--bg: #ffffff', '帮助页卡片表面必须保持纯白 --bg')
assertContains(helpCss, '.brand-badge', '帮助页必须为品牌图标加载失败提供视觉回退')
assertContains(helpCss, '.brand-badge[hidden] { display: none; }', '品牌图加载成功后必须隐藏文字徽标')
assertContains(helpCss, '@media (max-width: 900px)', '帮助页必须在平板宽度前切换为单列布局')
assertContains(helpCss, '.docs-nav', '帮助页必须提供篇目导航样式')
assertContains(helpCss, '.markdown-body', '帮助页必须提供 Markdown 正文排版')
assertContains(helpCss, '.download-link', '帮助页必须提供下载按钮样式')
assertContains(helpCss, '.doc-pager', '帮助页必须提供上下篇样式')
assertNotMatch(`${userShell}\n${adminShell}\n${helpIndex}`, /<script(?![^>]*\bsrc=)[^>]*>[\s\S]*?<\/script>|\bonload\s*=/i, '帮助页不得引入会被 CSP 阻止的内联脚本或事件处理器')

// 构建契约：dev 侧帮助面仍由 vite 静态服务，不代理给 gateway
assertNotContains(viteConfigSource, "devProxy['^/__aisys__/help", 'dev 代理不得把帮助页转发给 gateway：未配置 JUHE_AI_FRONTEND_DIST_PATH 时 help 面不挂载会 404')
assertContains(viteConfigSource, 'helpPageDirectoryIndexPlugin', 'dev 下必须保留帮助页目录索引插件，否则目录形 URL 会落入 SPA fallback 返回主应用页面')

console.log(`帮助文档站回归通过：${userDocs.length + adminDocs.length} 篇 md/渲染物/manifest 三方一致，17+28 条管理页深链全部保留，协议/概念/导入语义与下载、hash 路由、门控契约完好（更新于 ${manifest.updatedAt}）`)

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}
function assertEqual(actual: number, expected: number, message: string): void {
  if (actual !== expected) throw new Error(`${message}，实际 ${actual}，期望 ${expected}`)
}
function assertTrue(value: boolean, message: string): void {
  if (!value) throw new Error(message)
}
function assertFalse(value: boolean, message: string): void {
  if (value) throw new Error(message)
}
function assertContains(value: string, expected: string, message: string): void {
  if (!value.includes(expected)) throw new Error(`${message}：缺少 ${expected}`)
}
function assertNotContains(value: string, forbidden: string, message: string): void {
  if (value.includes(forbidden)) throw new Error(`${message}：不应出现 ${forbidden}`)
}
function assertMatch(value: string, pattern: RegExp, message: string): void {
  if (!pattern.test(value)) throw new Error(message)
}
function assertNotMatch(value: string, pattern: RegExp, message: string): void {
  if (pattern.test(value)) throw new Error(message)
}
