type DynamicImport = (specifier: string) => Promise<unknown>

const dynamicImport = new Function('specifier', 'return import(specifier)') as DynamicImport
const nodeFs = await dynamicImport('node:fs') as {
  readFileSync: (path: string, encoding: 'utf8') => string
}
const nodePath = await dynamicImport('node:path') as {
  dirname: (path: string) => string
  resolve: (...segments: string[]) => string
}
const nodeUrl = await dynamicImport('node:url') as { fileURLToPath: (url: string) => string }
const { buildOAuthIntegrationGuide, oauthIntegrationGuideFilename } = await import('../../views/oauth-applications/oauthIntegrationGuide.ts')

const repoRoot = nodePath.resolve(nodePath.dirname(nodeUrl.fileURLToPath(import.meta.url)), '../../../..')
const readRepoFile = (...segments: string[]) => nodeFs.readFileSync(nodePath.resolve(repoRoot, ...segments), 'utf8')
const viewSource = readRepoFile('frontend', 'src', 'views', 'oauth-applications', 'OAuthApplicationsView.vue')
const apiSource = readRepoFile('frontend', 'src', 'api', 'domains', 'oauthApplications.ts')

const client = {
  id: 'client-row-1',
  clientId: 'client-portal-123',
  displayName: '客户门户',
  clientType: 'confidential' as const,
  redirectUris: ['https://client.example.test/oauth/callback', 'https://client.example.test/oauth/callback-alt'],
  allowedScopes: ['openid', 'profile', 'juhe:profile.read', 'juhe:request_limits.read'],
  status: 'active' as const,
  createdAt: '2026-08-11T00:00:00.000Z',
  updatedAt: '2026-08-11T00:00:00.000Z'
}
const integration = {
  issuer: 'https://oidc.example.test',
  discoveryUrl: 'https://oidc.example.test/.well-known/openid-configuration',
  jwksUrl: 'https://oidc.example.test/oauth/jwks',
  authorizationEndpoint: 'https://oidc.example.test/oauth/authorize',
  tokenEndpoint: 'https://oidc.example.test/oauth/token',
  userinfoEndpoint: 'https://oidc.example.test/oauth/userinfo',
  deviceAuthorizationEndpoint: 'https://oidc.example.test/oauth/device_authorization',
  revocationEndpoint: 'https://oidc.example.test/oauth/revoke',
  tokenRenewalEndpoint: 'https://oidc.example.test/oauth/token/renew',
  idTokenSigningAlgorithm: 'RS256' as const
}
const guide = buildOAuthIntegrationGuide({ client, integration, clientSecret: 'repeat-download-client-secret' })
const writableGuide = buildOAuthIntegrationGuide({
  client: {
    ...client,
    allowedScopes: [
      ...client.allowedScopes,
      'juhe:profile.write',
      'juhe:groups.write',
      'juhe:route_strategies.write',
      'juhe:api_keys.write',
      'juhe:ai_accounts.write'
    ]
  },
  integration
})

assertEqual(oauthIntegrationGuideFilename(client.clientId), 'juhe-ai-client-portal-123-integration-guide.md', '下载文件名必须按 Client 稳定生成')
for (const value of [
  client.displayName,
  client.clientId,
  ...client.redirectUris,
  ...client.allowedScopes,
  integration.issuer,
  integration.discoveryUrl,
  integration.jwksUrl,
  integration.authorizationEndpoint,
  integration.tokenEndpoint,
  integration.userinfoEndpoint,
  integration.deviceAuthorizationEndpoint,
  integration.revocationEndpoint,
  integration.tokenRenewalEndpoint,
  'RS256'
]) {
  assertContains(guide, value, `按 Client 下载的文档必须包含绑定信息：${value}`)
}
for (const term of [
  '浏览器授权码 + PKCE',
  'Device Flow（桌面 App / CLI）',
  'device_code=<DEVICE_CODE>',
  'sequenceDiagram',
  '7 天（168 小时）硬到期',
  '72 小时',
  'refresh_token',
  'token_type_hint=access_token',
  '个人委托 API',
  '/request-limits',
  '启用 OIDC 登录',
  '基础身份资料',
  '标准 OIDC SDK 配置',
  '当前 `client_secret`',
  '不需要也不能索取 juhe-ai 的签名私钥'
]) {
  assertContains(guide, term, `下载文档必须覆盖闭环协议与安全边界：${term}`)
}
assertContains(guide, 'repeat-download-client-secret', '机密 Client 的行内下载必须包含当前 Client secret')
assertContains(guide, '可以随时从本应用的操作菜单再次下载完整文档', '对接文档必须说明可重复下载当前 Client secret')
assertContains(guide, '每个 `juhe:*.write` 必须在同一个 `scope` 参数中同时携带对应的 `juhe:*.read`', '对接文档必须说明 write scope 的 read 依赖')
assertContains(guide, '每 7 天自动轮换', '对接文档必须说明 OIDC 签名密钥的周周期自动轮换')
assertContains(guide, '未知 `kid`', '对接文档必须说明未知 kid 时刷新 JWKS')
assertContains(guide, '标准 OIDC SDK 通常会自动', '对接文档必须说明标准 OIDC SDK 的 JWKS 刷新行为')
assertNotContains(guide, '一次性完整对接文档', '对接文档不得继续声称 Client secret 只能一次性取得')
assertNotContains(guide, 'juhe:groups.read', '按 Client 生成的文档不得列出未授予的资源 scope')
assertNotContains(guide, 'juhe:ai_accounts.write', '按 Client 生成的文档不得列出未授予的写入 scope')
assertContains(guide, "--user 'client-portal-123:<CLIENT_SECRET>'", '机密 Client 的设备授权、续换和撤销示例必须使用 HTTP Basic')
assertNotContains(guide, "--data-urlencode 'client_id=client-portal-123'", '机密 Client 的文档不得混入会被拒绝的 body Client 认证')
for (const term of ['expectedUpdatedAt', 'expectedRevision', 'expectedConfigRevision', 'groupBindings', '性能优先的近似快照']) {
  assertContains(writableGuide, term, `获授写权限时文档必须包含完整的委托 API 请求契约：${term}`)
}
assertContains(apiSource, "http.get('/oauth/integration-info')", '文档生成必须读取运行时对接信息')
assertContains(apiSource, 'integrationPackage', '文档生成必须按应用读取含当前 Client secret 的对接包')
assertContains(viewSource, 'downloadIntegrationGuide(record)', '列表操作必须按 Client 下载对接文档')
assertContains(viewSource, 'downloadCreatedClientGuide', '机密 Client 创建后必须可下载完整文档')
assertContains(viewSource, "{ label: 'OIDC 登录', value: 'openid' }", '创建 Client 时必须可配置 OIDC 登录 scope')
assertContains(viewSource, "allowedScopes: ['openid', 'profile']", '新建第三方登录应用应默认携带 OIDC 基础 scope')
assertContains(viewSource, 'const { clientSecret: _clientSecret, ...client } = await api.oauthApplications.createClient(payload)', '机密 secret 不得作为列表行状态长期保存')
assertContains(viewSource, 'api.oauthApplications.integrationPackage(client.clientId)', '下载文档必须从服务端读取应用当前密钥')
assertContains(viewSource, '可随时从应用操作中重新下载', '机密 Client 创建提示必须说明当前密钥可通过文档重复下载')
assertNotContains(viewSource, 'clients.value = [created, ...clients.value]', '创建响应不得连同 secret 写入列表行')
assertNotContains(viewSource, '创建后只显示一次 client_secret', '机密 Client 创建提示不得保留一次性密钥文案')
assertNotContains(viewSource, '对接信息', '页面不得保留单独的对接信息入口')
assertNotContains(viewSource, '轮换签名密钥', '页面不得提供手动轮换签名密钥入口')
assertNotContains(viewSource, 'rotateSigningKey', '页面不得保留手动轮换签名密钥调用')
assertNotContains(apiSource, 'keys/rotate', '前端 API 不得保留手动轮换签名密钥接口')
assertNotContains(viewSource, "key: 'actions', width: 170, fixed: 'right'", '操作列不得固定在右侧')
assertNotContains(viewSource, 'connected-applications', '第三方应用页不得恢复已连接应用接口')
assertNotContains(guide, '\n+  ', '下载的 curl 模板不得包含字面量 + 前缀')

console.log('OAuth/OIDC 应用对接文档回归通过：Client 绑定、可重复下载当前 secret 与安全边界保持一致')

function assertEqual(actual: string, expected: string, message: string): void {
  if (actual !== expected) throw new Error(`${message}，实际：${actual}`)
}

function assertContains(value: string, expected: string, message: string): void {
  if (!value.includes(expected)) throw new Error(`${message}，缺少：${expected}`)
}

function assertNotContains(value: string, expected: string, message: string): void {
  if (value.includes(expected)) throw new Error(`${message}，发现：${expected}`)
}
