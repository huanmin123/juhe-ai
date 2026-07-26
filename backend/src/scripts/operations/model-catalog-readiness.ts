import { readFileSync, statSync } from 'node:fs'
import { isAbsolute, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

interface ApiKeyFileStat {
  isFile(): boolean
  mode: number
  size: number
}

interface ApiKeyFileDependencies {
  platform?: NodeJS.Platform
  stat?: (path: string) => ApiKeyFileStat
  readFile?: (path: string) => string
}

interface ModelCatalogReadinessDependencies {
  fetch?: typeof fetch
  readApiKey?: (path: string) => string
  timeoutSignal?: (milliseconds: number) => AbortSignal
}

export interface ModelCatalogReadinessResult {
  modelCount: number
  latencyMs: number
  url: string
}

interface CliOptions {
  baseUrl: string
  apiKeyFile: string
}

const timeoutMs = 2_000

export function readApiKeyFromMode0600File(
  apiKeyFile: string,
  dependencies: ApiKeyFileDependencies = {}
): string {
  if (!isAbsolute(apiKeyFile)) throw new Error('API key 文件必须使用绝对路径')
  const platform = dependencies.platform ?? process.platform
  if (platform === 'win32') throw new Error('API key mode 0600 校验只支持 POSIX 部署主机')
  const fileStat = (dependencies.stat ?? statSync)(apiKeyFile)
  if (!fileStat.isFile()) throw new Error('API key 路径不是普通文件')
  const mode = fileStat.mode & 0o777
  if (mode !== 0o600) {
    throw new Error(`API key 文件权限必须为 0600，当前为 ${mode.toString(8).padStart(4, '0')}`)
  }
  if (fileStat.size > 16 * 1024) throw new Error('API key 文件超过 16KiB 上限')
  const apiKey = (dependencies.readFile ?? ((path) => readFileSync(path, 'utf8')))(apiKeyFile).trim()
  if (!apiKey || /[\r\n]/.test(apiKey)) throw new Error('API key 文件必须只包含一行非空密钥')
  return apiKey
}

export function modelCatalogReadinessUrl(baseUrl: string): URL {
  const parsed = new URL(baseUrl)
  if (parsed.protocol !== 'http:') throw new Error('模型目录 readiness 只允许回环 HTTP URL')
  if (!['127.0.0.1', '::1', '[::1]'].includes(parsed.hostname)) {
    throw new Error('模型目录 readiness 只允许 127.0.0.1 或 ::1')
  }
  if (parsed.username || parsed.password || parsed.search || parsed.hash) {
    throw new Error('模型目录 readiness URL 不得包含凭据、查询或片段')
  }
  if (parsed.pathname !== '/' && parsed.pathname !== '') {
    throw new Error('模型目录 readiness base URL 不得包含路径')
  }
  parsed.pathname = '/v1/models'
  return parsed
}

export async function verifyModelCatalogReadiness(
  input: CliOptions,
  dependencies: ModelCatalogReadinessDependencies = {}
): Promise<ModelCatalogReadinessResult> {
  const url = modelCatalogReadinessUrl(input.baseUrl)
  const apiKey = (dependencies.readApiKey ?? readApiKeyFromMode0600File)(input.apiKeyFile)
  const startedAt = performance.now()
  const response = await (dependencies.fetch ?? fetch)(url, {
    method: 'GET',
    headers: {
      accept: 'application/json',
      authorization: `Bearer ${apiKey}`
    },
    redirect: 'error',
    signal: (dependencies.timeoutSignal ?? AbortSignal.timeout)(timeoutMs)
  })
  if (response.status !== 200) throw new Error(`模型目录 readiness HTTP 状态异常：${response.status}`)

  let payload: unknown
  try {
    payload = await response.json()
  } catch {
    throw new Error('模型目录 readiness 响应不是有效 JSON')
  }
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
    throw new Error('模型目录 readiness 响应必须是 JSON 对象')
  }
  const catalog = payload as { object?: unknown; data?: unknown }
  if (catalog.object !== 'list') throw new Error('模型目录 readiness 响应 object 必须为 list')
  if (!Array.isArray(catalog.data) || catalog.data.length < 1) {
    throw new Error('模型目录 readiness 响应 data 不得为空')
  }
  return {
    modelCount: catalog.data.length,
    latencyMs: Math.round((performance.now() - startedAt) * 100) / 100,
    url: url.toString()
  }
}

export function parseModelCatalogReadinessCli(args: string[]): CliOptions {
  let baseUrl = ''
  let apiKeyFile = ''
  for (let index = 0; index < args.length; index += 1) {
    const value = args[index]
    if (value === '--base-url') baseUrl = args[++index] ?? ''
    else if (value === '--api-key-file') apiKeyFile = args[++index] ?? ''
    else if (value === '--help' || value === '-h') {
      console.log('Usage: model-catalog-readiness --base-url http://127.0.0.1:<port> --api-key-file /absolute/mode0600/file')
      process.exit(0)
    } else {
      throw new Error(`未知参数：${value ?? ''}`)
    }
  }
  if (!baseUrl) throw new Error('缺少 --base-url')
  if (!apiKeyFile) throw new Error('缺少 --api-key-file')
  return { baseUrl, apiKeyFile }
}

async function main(): Promise<void> {
  const result = await verifyModelCatalogReadiness(parseModelCatalogReadinessCli(process.argv.slice(2)))
  console.log(`MODEL_CATALOG_READY models=${result.modelCount} latencyMs=${result.latencyMs} url=${result.url}`)
}

const cliEntry = process.argv[1]
if (cliEntry && import.meta.url === pathToFileURL(resolve(cliEntry)).href) {
  void main().catch((error) => {
    console.error(error instanceof Error ? error.message : '模型目录 readiness 失败')
    process.exitCode = 1
  })
}
