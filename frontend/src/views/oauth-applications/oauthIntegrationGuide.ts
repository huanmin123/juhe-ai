import type { OAuthClientSummary, OAuthIntegrationInfo } from '@/types/domain'

export interface OAuthIntegrationGuideInput {
  client: OAuthClientSummary
  integration: OAuthIntegrationInfo
  clientSecret?: string
}

export function oauthIntegrationGuideFilename(clientId: string): string {
  return `juhe-ai-${clientId}-integration-guide.md`
}

export function buildOAuthIntegrationGuide(input: OAuthIntegrationGuideInput): string {
  const { client, clientSecret, integration } = input
  const defaultRedirectUri = client.redirectUris[0] ?? '<REGISTERED_REDIRECT_URI>'
  const requestedScope = client.allowedScopes.join(' ')
  const usesOpenId = client.allowedScopes.includes('openid')
  const allowedScopeRows = client.allowedScopes.map((scope) => `| \`${scope}\` | ${scopeDescription(scope)} |`)
  const clientAuthenticationCurlLine = client.clientType === 'confidential'
    ? `  --user '${client.clientId}:<CLIENT_SECRET>'`
    : `  --data-urlencode 'client_id=${client.clientId}'`
  const curlContinuation = '\\'
  const encodedRedirectUri = encodeURIComponent(defaultRedirectUri)
  const encodedScope = encodeURIComponent(requestedScope)
  const authorizationUrl = `${integration.authorizationEndpoint}?response_type=code&client_id=${encodeURIComponent(client.clientId)}&redirect_uri=${encodedRedirectUri}&scope=${encodedScope}&state=<STATE>&code_challenge=<S256_CODE_CHALLENGE>&code_challenge_method=S256${usesOpenId ? '&nonce=<NONCE>' : ''}`
  const delegatedApiContract = buildDelegatedApiContract(integration.issuer, client.allowedScopes)
  const clientSecretSection = client.clientType === 'confidential'
    ? clientSecret
      ? [
          '## 机密配置',
          '',
          '这份应用专属文档含有当前 `client_secret`。管理员可以随时从本应用的操作菜单再次下载完整文档；重新签发 Client Secret 后，旧值会立即失效，后续下载只会提供新值。',
          '将本文档交给负责接入的 AI 或工程师即可。机密 Client 的运行代码应从服务端或 BFF 的 `CLIENT_SECRET` 配置读取该值，不要把它放进浏览器、桌面包或移动端。',
          '',
          '```text',
          `CLIENT_ID=${client.clientId}`,
          `CLIENT_SECRET=${clientSecret}`,
          '```',
          ''
        ]
      : [
          '## 机密配置缺失',
          '',
          '当前 Client Secret 不可用，不能生成完整对接文档。请在管理后台重新签发 Client Secret 后再次下载。',
          ''
        ]
    : []
  const nonceGuidance = usesOpenId
    ? '本应用允许 `openid`，浏览器与 Device Flow 请求必须携带随机 `nonce`，并在验证 ID Token 时比对它。'
    : '本应用未申请 `openid`，不返回 ID Token；不要在请求中伪造 `nonce` 或依赖 UserInfo。'

  const guide = [
    `# ${client.displayName}：juhe-ai OAuth/OIDC 对接文档`,
    '',
    '> 这是一份按第三方应用实时生成的交付文件。将整份 Markdown 交给负责集成的 AI 或工程师即可；其中的 Client ID、回调地址、Scope 和服务地址已经绑定到本应用。',
    '',
    '## 应用配置',
    '',
    '```json',
    JSON.stringify({
      applicationName: client.displayName,
      clientId: client.clientId,
      clientType: client.clientType,
      status: client.status,
      redirectUris: client.redirectUris,
      allowedScopes: client.allowedScopes
    }, null, 2),
    '```',
    '',
    client.status === 'active'
      ? '该 Client 当前已启用。'
      : '该 Client 当前已停用。完成代码接入前，请先在 juhe-ai 管理后台启用它；停用时现有 token 会立即失效。',
    '',
    '只能使用上面列出的精确 `redirect_uri`，不能自行更换域名、路径、查询参数或添加通配符。示例默认使用第一条：',
    '',
    `\`${defaultRedirectUri}\``,
    '',
    ...clientSecretSection,
    '## 服务地址',
    '',
    `- Issuer：\`${integration.issuer}\``,
    `- OIDC Discovery：\`${integration.discoveryUrl}\``,
    `- JWKS：\`${integration.jwksUrl}\``,
    `- Authorization Endpoint：\`${integration.authorizationEndpoint}\``,
    `- Token Endpoint：\`${integration.tokenEndpoint}\``,
    `- Device Authorization Endpoint：\`${integration.deviceAuthorizationEndpoint}\``,
    `- UserInfo Endpoint：\`${integration.userinfoEndpoint}\``,
    `- Revocation Endpoint：\`${integration.revocationEndpoint}\``,
    `- Token Renewal Endpoint（juhe-ai 扩展）：\`${integration.tokenRenewalEndpoint}\``,
    `- ID Token 签名算法：\`${integration.idTokenSigningAlgorithm}\``,
    '',
    '不需要也不能索取 juhe-ai 的签名私钥。平台会每 7 天自动轮换 OIDC 签名密钥，并在 JWKS 中保留旧公钥覆盖已签发 ID Token 的验证窗口。标准 OIDC 库应从 Discovery 自动取得 JWKS 公钥，再按 `kid` 验证 ID Token。',
    '',
    '## 标准 OIDC SDK 配置',
    '',
    'juhe-ai 不提供需要单独安装的私有 SDK；任何支持 Authorization Code + PKCE、Discovery、JWKS 和 RS256 的标准 OAuth/OIDC SDK 都可以使用。把以下已绑定的配置交给 SDK：`issuer` 使用上面的 Issuer，`client_id` 使用本应用 Client ID，`redirect_uri` 必须精确使用已登记地址，`scope` 只能使用本文件列出的范围。每个 `juhe:*.write` 必须在同一个 `scope` 参数中同时携带对应的 `juhe:*.read`，否则服务器返回 `invalid_scope`。公开 Client 不配置 `client_secret`；机密 Client 只能在第三方服务端或 BFF 的密钥管理中配置它。',
    '',
    'SDK 必须开启 PKCE `S256`、校验 `state`；申请 `openid` 时还必须校验 `nonce`、`iss`、`aud`、`exp` 和 JWKS 中按 `kid` 选择的 `RS256` 公钥。遇到未知 `kid` 时刷新 JWKS，再重试验签，而不是把单一公钥写死；标准 OIDC SDK 通常会自动处理这次 JWKS 刷新，接入方只需确认没有把单个 JWK 固定在本地。',
    '',
    '## 浏览器授权码 + PKCE',
    '',
    '```mermaid',
    'sequenceDiagram',
    '    participant B as 用户浏览器',
    '    participant C as 第三方应用',
    '    participant J as juhe-ai 授权服务',
    '    B->>C: 开始登录或授权',
    '    C->>C: 生成 state 和 PKCE verifier/challenge',
    usesOpenId ? '    C->>C: 额外生成 nonce' : '    Note over C: 本应用未申请 openid',
    '    C-->>B: 重定向到 authorize',
    '    B->>J: 登录并确认应用请求的权限',
    '    J-->>B: redirect_uri?code&state',
    '    B-->>C: 回调 code 与 state',
    '    C->>C: 先校验 state',
    '    C->>J: token + code + PKCE verifier',
    '    J-->>C: access_token，openid 时另有 id_token',
    '```',
    '',
    `本应用的默认授权 URL 结构：`,
    '',
    '```text',
    authorizationUrl,
    '```',
    '',
    '真实请求必须为每次登录生成高熵 `state` 与 PKCE verifier，并使用 SHA-256 生成 `code_challenge`。回调后先比较 `state`，再把授权码换成 token。',
    nonceGuidance,
    '',
    client.clientType === 'confidential' ? '机密 Client 换 token（仍必须使用 PKCE）：' : '公开 Client 换 token：',
    '',
    '```bash',
    `curl -X POST '${integration.tokenEndpoint}' \\\n  -H 'Content-Type: application/x-www-form-urlencoded' \\\n  --data-urlencode 'grant_type=authorization_code' \\\n  --data-urlencode 'client_id=${client.clientId}' \\\n  --data-urlencode 'code=<AUTHORIZATION_CODE>' \\\n  --data-urlencode 'redirect_uri=${defaultRedirectUri}' \\\n  --data-urlencode 'code_verifier=<PKCE_VERIFIER>'`,
    '```',
    '',
    ...(client.clientType === 'confidential' ? [
      '机密 Client 仍必须使用 PKCE；在第三方服务端额外使用 HTTP Basic Client 认证：',
      '',
      '```bash',
      `curl -X POST '${integration.tokenEndpoint}' \\\n  --user '${client.clientId}:<CLIENT_SECRET>' \\\n  -H 'Content-Type: application/x-www-form-urlencoded' \\\n  --data-urlencode 'grant_type=authorization_code' \\\n  --data-urlencode 'code=<AUTHORIZATION_CODE>' \\\n  --data-urlencode 'redirect_uri=${defaultRedirectUri}' \\\n  --data-urlencode 'code_verifier=<PKCE_VERIFIER>'`,
      '```',
      ''
    ] : []),
    '## Device Flow（桌面 App / CLI）',
    '',
    client.clientType === 'confidential'
      ? '机密 Client 的 Device Flow 只能由其受控服务端或 BFF 调用，桌面 App / CLI 本体不得持有 `client_secret`；桌面或 CLI 直连时应登记为公开 Client。受控服务端调用 `device_authorization` 后打开响应中的 `verification_uri_complete`，用户在浏览器完成登录与同意，再按服务端 `interval` 轮询 token。'
      : '桌面客户端、CLI 和无可用回调服务的场景使用 Device Flow。客户端调用 `device_authorization` 后打开响应中的 `verification_uri_complete`；用户在浏览器完成登录与同意，客户端按服务端 `interval` 轮询 token。',
    '',
    '```mermaid',
    'sequenceDiagram',
    '    participant D as 桌面 App 或 CLI',
    '    participant J as juhe-ai 授权服务',
    '    participant B as 用户浏览器',
    '    D->>J: POST device_authorization',
    '    J-->>D: device_code、user_code、verification_uri_complete、interval',
    '    D-->>B: 打开 verification_uri_complete',
    '    B->>J: 登录并允许或拒绝',
    '    loop 按 interval 轮询',
    '        D->>J: POST token（device_code grant）',
    '        J-->>D: authorization_pending / slow_down / token',
    '    end',
    '```',
    '',
    '```bash',
    `curl -X POST '${integration.deviceAuthorizationEndpoint}' \\\n  -H 'Content-Type: application/x-www-form-urlencoded' \\\n  --data-urlencode 'client_id=${client.clientId}' \\\n  --data-urlencode 'scope=${requestedScope}'${usesOpenId ? " \\\n  --data-urlencode 'nonce=<NONCE>'" : ''}`,
    '```',
    '',
    '收到 `authorization_pending` 时等待 `interval` 秒；收到 `slow_down` 时继续增加等待时间，禁止高频轮询。',
    '',
    '轮询一次 token：',
    '',
    '```bash',
    `curl -X POST '${integration.tokenEndpoint}' ${curlContinuation}`,
    `  -H 'Content-Type: application/x-www-form-urlencoded' ${curlContinuation}`,
    `${clientAuthenticationCurlLine} ${curlContinuation}`,
    `  --data-urlencode 'grant_type=urn:ietf:params:oauth:grant-type:device_code' ${curlContinuation}`,
    "  --data-urlencode 'device_code=<DEVICE_CODE>'",
    '```',
    '',
    '## Token 生命周期、续换与撤销',
    '',
    '- 授权自用户确认时开始固定 7 天（168 小时）硬到期，换 token 不会延长它。',
    '- 当前 token 只有签发满 72 小时且原授权仍有效时，才可调用 renewal endpoint 获取 successor token；旧 token 立即失效。',
    '- V1 不支持 `refresh_token` 或 `refresh_token` grant；超过 7 天只能重新执行用户授权。',
    '',
    '```bash',
    `curl -X POST '${integration.tokenRenewalEndpoint}' \\\n  -H 'Content-Type: application/x-www-form-urlencoded' \\\n  --data-urlencode 'client_id=${client.clientId}' \\\n  --data-urlencode 'current_access_token=<CURRENT_ACCESS_TOKEN>'`,
    '```',
    '',
    '不再使用时撤销当前 token：',
    '',
    '```bash',
    `curl -X POST '${integration.revocationEndpoint}' ${curlContinuation}`,
    `  -H 'Content-Type: application/x-www-form-urlencoded' ${curlContinuation}`,
    `${clientAuthenticationCurlLine} ${curlContinuation}`,
    `  --data-urlencode 'token=<CURRENT_ACCESS_TOKEN>' ${curlContinuation}`,
    "  --data-urlencode 'token_type_hint=access_token'",
    '```',
    '',
    '## ID Token 与个人委托 API',
    '',
    usesOpenId
      ? `请求 \`openid\` 时，验证 ID Token 的签名、\`iss=${integration.issuer}\`、\`aud=${client.clientId}\`、\`exp\` 与本地 nonce。ID Token 只用于第三方登录会话；调用资源 API 必须使用 access token。`
      : '本应用未登记 `openid`，不要使用 ID Token 建立登录会话；调用已授权资源时仅使用 access token。',
    '',
    `个人委托 API 基础路径：\`${integration.issuer}/__aidelegated__/v1\`，请求头为 \`Authorization: Bearer <access_token>\`。所有数据和写操作都严格属于授权用户本人。`,
    '',
    '| Scope | 已允许的资源与接口 |',
    '| --- | --- |',
    ...allowedScopeRows,
    '',
    `本应用允许申请的 scope：\`${requestedScope}\`。实际请求只能使用其中的子集；未登记的 scope 必须视为错误，不能尝试调用。请求任何 \`juhe:*.write\` 时，同一个 scope 参数必须同时含有对应的 \`juhe:*.read\`，否则会收到 \`invalid_scope\`。`,
    '',
    ...delegatedApiContract,
    '## 交付安全规则',
    '',
    '- 不记录授权码、access token、ID Token、device code、PKCE verifier、state、nonce 或 client secret。',
    '- 不把机密 Client 的 secret 放进 SPA、桌面 App、移动 App、浏览器本地存储、代码仓库或公开文档。',
    '- OAuth token 失效、Client 被停用、用户账号被停用或授权到期后，清理本地 token 并按需重新授权。',
    '- 本文档以下载时的 Client 配置为准；管理员修改回调地址、Scope 或 Client 状态后，请重新下载并交付新版本。',
    ''
  ].join('\n')
  return client.clientType === 'confidential'
    ? formatConfidentialGuide(guide, client.clientId, clientAuthenticationCurlLine)
    : guide
}

function formatConfidentialGuide(guide: string, clientId: string, clientAuthenticationCurlLine: string): string {
  return guide
    .replaceAll(`  --data-urlencode 'client_id=${clientId}'`, clientAuthenticationCurlLine)
    .replace(
      /\n机密 Client 仍必须使用 PKCE；在第三方服务端额外使用 HTTP Basic Client 认证：\n\n```bash\n[\s\S]*?\n```\n\n(?=## Device Flow)/,
      '\n'
    )
}

function scopeDescription(scope: string): string {
  const descriptions: Record<string, string> = {
    openid: '启用 OIDC 登录，可取得并验证 ID Token，也可调用 `/userinfo`。',
    profile: '允许 `/userinfo` 返回基础身份资料；必须与 `openid` 一起请求。',
    'juhe:profile.read': '`GET` `/profile`',
    'juhe:profile.write': '`PATCH` `/profile`',
    'juhe:groups.read': '`GET` `/groups`、`GET` `/groups/:id`',
    'juhe:groups.write': '`POST` `/groups`、`PATCH` / `DELETE` `/groups/:id`',
    'juhe:route_strategies.read': '`GET` `/route-strategies`、`GET` `/route-strategies/:id`',
    'juhe:route_strategies.write': '`POST` `/route-strategies`、`PATCH` / `DELETE` `/route-strategies/:id`',
    'juhe:api_keys.read': '`GET` `/api-keys`；不会返回明文 Key。',
    'juhe:api_keys.write': '`PATCH` `/api-keys/:id`；不支持创建、删除或重置。',
    'juhe:ai_accounts.read': '`GET` `/ai-accounts`；不会返回上游凭据。',
    'juhe:ai_accounts.write': '`PATCH` `/ai-accounts/:id`；不支持创建或删除。',
    'juhe:request_limits.read': '`GET` `/request-limits`，包含每日、每周、每月、每分钟限制及近似已用/剩余量。'
  }
  return descriptions[scope] ?? '该 scope 已在此 Client 登记；仅能按服务端实际响应使用。'
}

function buildDelegatedApiContract(issuer: string, scopes: string[]): string[] {
  const has = (scope: string) => scopes.includes(`juhe:${scope}`)
  const lines = [
    '## 个人委托 API 请求结构',
    '',
    `以下地址都以 \`${issuer}/__aidelegated__/v1\` 为前缀，并携带 \`Authorization: Bearer <access_token>\`。成功响应统一为 \`{ "data": ... }\`；列表查询可传 \`page\`、\`pageSize\`。读取当前对象后再修改，遇到 \`409\` 必须重新读取并由用户或业务规则合并。`,
    ''
  ]

  if (has('profile.write')) {
    lines.push('### 修改个人资料', '', '`PATCH /profile`：`{ "displayName": "新的显示名称" }`。不能修改登录账号、密码、角色或状态。', '')
  }
  if (has('groups.write')) {
    lines.push(
      '### 分组写入',
      '',
      '`POST /groups` 最小请求：`{ "name": "我的分组", "providerCode": "<已启用供应商编码>" }`。可选字段为 `description`、`enabled`、`groupType`（`personal` 或 `high_concurrency`）和 `schedulingPolicy`。',
      '`PATCH /groups/:id` 必须先 `GET /groups/:id`，再提交要修改的字段及该响应的 `updatedAt`：`{ "expectedUpdatedAt": "<ISO-8601>", "enabled": false }`。删除为 `DELETE /groups/:id`；仍被路由策略绑定时还需要 `juhe:route_strategies.write`。',
      ''
    )
  }
  if (has('route_strategies.write')) {
    lines.push(
      '### 路由策略写入',
      '',
      '`POST /route-strategies` 至少包含名称与一个本人分组绑定：`{ "name": "默认路由", "groupBindings": [{ "groupId": "<GROUP_ID>", "priority": 1, "status": "active" }] }`。可选 `description`、`mode`、`status`、`normalRoutingConfig` 和 `hybridRoutingConfig`。',
      '`PATCH /route-strategies/:id` 先读取详情，再提交部分更新加 `expectedUpdatedAt`，例如 `{"expectedUpdatedAt":"<ISO-8601>","status":"disabled"}`。所有 `groupBindings` 都必须属于当前授权用户；删除为 `DELETE /route-strategies/:id`。',
      ''
    )
  }
  if (has('api_keys.write')) {
    lines.push(
      '### API Key 元数据写入',
      '',
      '`PATCH /api-keys/:id` 必须先从列表读取 `revision`，再提交 `{"expectedRevision":"<REVISION>","name":"新的名称","status":"active","routeStrategyId":"<OWN_ROUTE_STRATEGY_ID>"}` 的任意修改子集。该 API 不会也不能创建、重置、删除或返回 Key 明文。',
      ''
    )
  }
  if (has('ai_accounts.write')) {
    lines.push(
      '### AI 账户元数据写入',
      '',
      '`PATCH /ai-accounts/:id` 必须先读取 `configRevision`，再提交 `{"expectedConfigRevision":1,"name":"新的名称","status":"active"}` 的任意修改子集。仅限授权用户自己的物理账户，不含上游凭据、创建或删除。',
      ''
    )
  }
  if (has('request_limits.read')) {
    lines.push(
      '### 请求限制读取',
      '',
      '`GET /request-limits` 返回 `usageStatus`、`asOf`、`timezone` 和 `windows`。`windows` 含 `minute`、`day`、`week`、`month`，每项为 `{ limit, limitMode, usageTracked, used, remaining, source, resetsAt }`。数据为性能优先的近似快照，不应把 `remaining` 当作强一致扣减依据。',
      ''
    )
  }
  return lines
}
