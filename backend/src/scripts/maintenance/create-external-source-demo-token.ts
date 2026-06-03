import {
  createExternalIntegrationSourceToken,
  externalIntegrationSourceAuthDemoScope,
  upsertExternalIntegrationSource
} from '../../storage/external-integration-source.repository.js'

const sourceName = readOption('--name', process.env.JUHE_AI_EXTERNAL_SOURCE_NAME) ?? 'juhe-ai公益站'
const tokenName = readOption('--token-name', process.env.JUHE_AI_EXTERNAL_TOKEN_NAME) ?? '来源系统鉴权 demo token'
const expiresAt = readOption('--expires-at', process.env.JUHE_AI_EXTERNAL_TOKEN_EXPIRES_AT)
const scopes = readScopes(readOption('--scopes', process.env.JUHE_AI_EXTERNAL_SOURCE_SCOPES))

const source = upsertExternalIntegrationSource({
  name: sourceName,
  status: 'active',
  scopes,
  notes: '用于验证外部来源系统是否允许调用的 demo 来源。'
})

const created = createExternalIntegrationSourceToken({
  sourceRefId: source.id,
  name: tokenName,
  scopes,
  expiresAt
})

console.log('外部来源系统 demo token 已创建。明文 token 只会在本次命令输出中展示，请保存到调用方后端配置。')
console.log(`来源系统：${source.name}`)
console.log(`Token 名称：${created.name}`)
console.log(`Token 标识：${created.tokenPrefix}...${created.tokenSuffix}`)
console.log(`Scopes：${created.scopes.join(', ')}`)
if (created.expiresAt) {
  console.log(`过期时间：${created.expiresAt}`)
}
console.log(`Token：${created.token}`)
console.log('')
console.log('PowerShell 测试示例：')
console.log(`$headers = @{ Authorization = "Bearer ${created.token}" }`)
console.log('Invoke-RestMethod -Headers $headers -Uri "http://127.0.0.1:3000/__aipublic__/demo/source-auth"')

function readOption(name: string, fallback?: string): string | undefined {
  const index = process.argv.indexOf(name)
  if (index >= 0) {
    const value = process.argv[index + 1]?.trim()
    return value ? value : fallback
  }
  return fallback?.trim() || undefined
}

function readScopes(value: string | undefined): string[] {
  const scopes = new Set<string>([externalIntegrationSourceAuthDemoScope])
  for (const item of readCsv(value)) {
    const scope = item.trim()
    if (scope) {
      scopes.add(scope)
    }
  }
  return [...scopes].sort()
}

function readCsv(value: string | undefined): string[] {
  return value?.split(',').map((item) => item.trim()).filter(Boolean) ?? []
}
