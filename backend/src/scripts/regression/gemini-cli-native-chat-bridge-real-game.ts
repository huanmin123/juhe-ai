import { strict as assert } from 'node:assert'
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, rmSync, statSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { delimiter as pathDelimiter, dirname, join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express, { type NextFunction, type Request, type Response as ExpressResponse } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import type { AccountModelMapping } from '../../domain/types.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'

interface GatewayIncomingHit {
  path: string
  method: string
  authorizationPresent: boolean
  xGoogApiKeyPresent: boolean
  bodySummary: Record<string, unknown>
}

interface CliRunResult {
  commandLine: string
  exitCode: number | null
  stderr: string
  stdout: string
}

const realApiKey = requiredEnv('JUHE_REAL_GEMINI_CLI_CHAT_BRIDGE_API_KEY', [
  'JUHE_REAL_GEMINI_NATIVE_CHAT_BRIDGE_API_KEY',
  'JUHE_REAL_OPENAI_COMPATIBLE_API_KEY',
  'JUHE_REAL_HYBRID_API_KEY'
])
const realBaseUrl = envText('JUHE_REAL_GEMINI_CLI_CHAT_BRIDGE_BASE_URL', [
  'JUHE_REAL_GEMINI_NATIVE_CHAT_BRIDGE_BASE_URL',
  'JUHE_REAL_OPENAI_COMPATIBLE_BASE_URL',
  'JUHE_REAL_HYBRID_BASE_URL'
]) || 'https://vsllm.com'
const sourceModel = envText('JUHE_REAL_GEMINI_CLI_CHAT_BRIDGE_SOURCE_MODEL') || 'gemini-3.5-flash'
const upstreamModel = envText('JUHE_REAL_GEMINI_CLI_CHAT_BRIDGE_UPSTREAM_MODEL') || 'glm-5.2'
const outputDir = resolve(envText('JUHE_REAL_GEMINI_CLI_CHAT_BRIDGE_OUTPUT_DIR') || 'D:\\Downloads\\temp\\gemini-cli-snake')
const upstreamProvider = bridgeProvider()
const requestTimeoutMs = positiveIntegerEnv('JUHE_REAL_GEMINI_CLI_CHAT_BRIDGE_TIMEOUT_MS') ?? 300_000
const requiredFiles = ['index.html', 'styles.css', 'game.js']

const tempRoot = resolve(tmpdir(), `juhe-ai-gemini-cli-chat-bridge-game-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'gemini-cli-chat-bridge-game.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gemini-cli-chat-bridge-game-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
mkdirSync(outputDir, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const gatewayIncomingHits: GatewayIncomingHit[] = []
const app = express()
app.use(requestContextMiddleware)
app.use('/v1beta', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, captureIncomingGatewayRequest, openAIGatewayRouter)

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()

  let appServer: http.Server | undefined
  try {
    const upstreamModels = await listRealModels()
    assert(
      upstreamModels.availableModels.length === 0 || upstreamModels.availableModels.includes(upstreamModel),
      `真实上游模型列表未包含 ${upstreamModel}，可见模型样本：${upstreamModels.availableModels.slice(0, 40).join('、')}`
    )

    registerModels()
    const group = repositories.createGroup({
      name: 'Gemini CLI Native 转 Chat 真实游戏分组',
      providerCode: upstreamProvider.providerCode,
      enabled: true
    }, access)
    repositories.createAccount({
      providerCode: upstreamProvider.providerCode,
      providerProtocolProfileId: upstreamProvider.providerProtocolProfileId,
      name: 'Gemini CLI Native 转 Chat 真实游戏账户',
      type: 'api_key',
      credentials: {
        api_key: realApiKey,
        base_url: realBaseUrl,
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      supportedModels: [upstreamModel],
      modelMappings: bridgeMappings()
    }, access)
    const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'Gemini CLI Native 转 Chat 真实游戏 Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(apiKey.key, 'Gemini CLI Native 转 Chat 真实游戏本地 API Key 未返回明文密钥')
    gatewayCache.clearGatewayRuntimeCache()

    appServer = http.createServer(app)
    await listen(appServer)
    const gatewayBaseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    const result = await runGeminiCli({
      gatewayBaseUrl,
      localApiKey: apiKey.key
    })
    assert.equal(result.exitCode, 0, `gemini-cli 应成功退出：${summarizeCliFailure(result)}`)
    assert(
      gatewayIncomingHits.some((hit) => hit.path.includes(':generateContent') || hit.path.includes(':streamGenerateContent')),
      `gemini-cli 应命中 Gemini native 生成路径，实际：${JSON.stringify(gatewayIncomingHits.map(summarizeIncomingHit), null, 2)}`
    )
    const fileSummary = assertGeneratedGameFiles(result)

    console.log(JSON.stringify({
      ok: true,
      provider: upstreamProvider.providerCode,
      profile: upstreamProvider.providerProtocolProfileId,
      baseUrl: sanitizeBaseUrl(realBaseUrl),
      sourceModel,
      upstreamModel,
      outputDir,
      upstreamModels: {
        status: upstreamModels.status,
        modelCount: upstreamModels.availableModels.length,
        sample: upstreamModels.availableModels.slice(0, 20)
      },
      gatewayRequests: gatewayIncomingHits.map(summarizeIncomingHit),
      generatedFiles: fileSummary,
      cli: {
        commandLine: result.commandLine,
        exitCode: result.exitCode,
        stdout: sanitizeSecretText(result.stdout).slice(0, 1600),
        stderr: sanitizeSecretText(result.stderr).slice(0, 1600)
      }
    }, null, 2))
  } finally {
    await closeServer(appServer)
  }
} catch (error) {
  throw new Error(sanitizeSecretText(error instanceof Error ? error.stack ?? error.message : String(error)))
} finally {
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function listRealModels(): Promise<{ status: number; availableModels: string[] }> {
  const response = await fetchWithTimeout(openAICompatibleModelsUrl(realBaseUrl), {
    headers: {
      authorization: `Bearer ${realApiKey}`
    }
  })
  const text = await response.text()
  assert.equal(response.status, 200, `真实上游 /models 应成功，实际 HTTP ${response.status}: ${responseSnippet(text)}`)
  const body = parseJsonObject(text)
  const availableModels = Array.isArray(body.data)
    ? body.data.map((item) => modelNameFromUnknown(item)).filter((item): item is string => Boolean(item))
    : Array.isArray(body.models)
      ? body.models.map((item) => modelNameFromUnknown(item)).filter((item): item is string => Boolean(item))
      : []
  return { status: response.status, availableModels }
}

function captureIncomingGatewayRequest(req: Request, _res: ExpressResponse, next: NextFunction): void {
  gatewayIncomingHits.push({
    path: req.originalUrl || req.url,
    method: req.method,
    authorizationPresent: Boolean(req.headers.authorization),
    xGoogApiKeyPresent: Boolean(req.headers['x-goog-api-key']),
    bodySummary: summarizeGatewayBody(requestJsonBody(req))
  })
  next()
}

function summarizeGatewayBody(body: Record<string, unknown>): Record<string, unknown> {
  const summary: Record<string, unknown> = {}
  for (const key of ['model', 'stream', 'temperature', 'max_tokens', 'contents', 'config', 'generationConfig', 'tools', 'toolConfig']) {
    if (key in body) summary[key] = summarizeBodyValue(body[key])
  }
  return summary
}

function summarizeBodyValue(value: unknown): unknown {
  if (Array.isArray(value)) return { arrayLength: value.length }
  if (value && typeof value === 'object') return { objectKeys: Object.keys(value as Record<string, unknown>).slice(0, 12) }
  return value
}

function requestBodyText(req: Request): string {
  const body = req.body
  if (Buffer.isBuffer(body)) return body.toString('utf8')
  if (typeof body === 'string') return body
  if (body && typeof body === 'object' && 'toString' in body) return String(body)
  return ''
}

function requestJsonBody(req: Request): Record<string, unknown> {
  const body = req.body
  if (body && typeof body === 'object' && !Buffer.isBuffer(body) && !Array.isArray(body)) {
    return body as Record<string, unknown>
  }
  const bodyText = requestBodyText(req)
  return bodyText.trim() ? parseJsonObject(bodyText) : {}
}

function parseJsonObject(text: string): Record<string, unknown> {
  const parsed = JSON.parse(text) as unknown
  assert(parsed && typeof parsed === 'object' && !Array.isArray(parsed), `响应不是 JSON 对象：${responseSnippet(text)}`)
  return parsed as Record<string, unknown>
}

function summarizeIncomingHit(hit: GatewayIncomingHit): Record<string, unknown> {
  return {
    path: hit.path,
    method: hit.method,
    authorizationPresent: hit.authorizationPresent,
    xGoogApiKeyPresent: hit.xGoogApiKeyPresent,
    bodySummary: hit.bodySummary
  }
}

async function runGeminiCli(input: { gatewayBaseUrl: string; localApiKey: string }): Promise<CliRunResult> {
  const cliRoot = createCliRoot('gemini')
  writeGeminiCliSettings(cliRoot)
  return runCli({
    command: 'gemini',
    args: [
      '--prompt',
      gamePrompt(),
      '--output-format',
      'text',
      '--model',
      sourceModel,
      '--skip-trust',
      '--approval-mode',
      'yolo'
    ],
    cwd: outputDir,
    env: isolatedCliEnv(cliRoot, {
      GEMINI_API_KEY: input.localApiKey,
      GOOGLE_GEMINI_BASE_URL: input.gatewayBaseUrl,
      GEMINI_CLI_DISABLE_UPDATE_CHECK: '1',
      DISABLE_TELEMETRY: '1'
    }),
    timeoutMs: requestTimeoutMs,
    stdinText: ''
  })
}

function gamePrompt(): string {
  return [
    '请在当前工作目录创建一个可直接打开运行的 HTML5 贪吃蛇小游戏。',
    '必须真实写入文件，不要只输出代码块或说明。',
    '只创建或覆盖这三个文件：index.html、styles.css、game.js。',
    '功能要求：Canvas 游戏区、键盘方向键和 WASD 控制、开始/暂停/重新开始按钮、分数与最高分、本地存储最高分、撞墙或撞到自己结束、移动端触摸滑动控制。',
    '视觉要求：深色背景、清晰网格、蛇头和食物有明显颜色，页面中文文案。',
    '完成后只用中文简短说明已写入哪些文件。'
  ].join('\n')
}

function writeGeminiCliSettings(cliRoot: string): void {
  const geminiDir = join(cliRoot, '.gemini')
  mkdirSync(geminiDir, { recursive: true })
  writeFileSync(join(geminiDir, 'settings.json'), JSON.stringify({
    security: {
      auth: {
        selectedType: 'gemini-api-key'
      }
    },
    usageStatisticsEnabled: false
  }, null, 2))
}

function runCli(input: {
  args: string[]
  command: string
  cwd: string
  env: Record<string, string>
  stdinText?: string
  timeoutMs: number
}): Promise<CliRunResult> {
  const directLauncher = process.platform === 'win32' ? resolveWindowsNodeCliLauncher(input.command) : undefined
  const command = directLauncher?.command ?? (process.platform === 'win32' ? process.env.ComSpec || 'cmd.exe' : input.command)
  const args = directLauncher
    ? [...directLauncher.args, ...input.args]
    : process.platform === 'win32'
      ? ['/d', '/s', '/c', windowsCommandLine([input.command, ...input.args])]
      : input.args
  const commandLine = windowsCommandLine([input.command, ...input.args])

  return new Promise((resolvePromise, rejectPromise) => {
    let settled = false
    const child = spawn(command, args, {
      cwd: input.cwd,
      env: input.env,
      stdio: ['pipe', 'pipe', 'pipe'],
      windowsHide: true
    })
    const stdout: Buffer[] = []
    const stderr: Buffer[] = []
    const settle = (fn: () => void) => {
      if (settled) return
      settled = true
      fn()
    }
    const timeout = setTimeout(() => {
      child.kill()
      settle(() => rejectPromise(new Error(`${input.command} 超时；stdout=${sanitizeSecretText(Buffer.concat(stdout).toString('utf8')).slice(0, 1200)}；stderr=${sanitizeSecretText(Buffer.concat(stderr).toString('utf8')).slice(0, 1200)}`)))
    }, input.timeoutMs)

    child.stdout.on('data', (chunk: Buffer) => stdout.push(chunk))
    child.stderr.on('data', (chunk: Buffer) => stderr.push(chunk))
    if (input.stdinText) child.stdin.end(input.stdinText)
    else child.stdin.end()
    child.on('error', (error) => {
      clearTimeout(timeout)
      settle(() => rejectPromise(error))
    })
    child.on('close', (code) => {
      clearTimeout(timeout)
      settle(() => resolvePromise({
        commandLine: sanitizeSecretText(commandLine),
        exitCode: code,
        stdout: Buffer.concat(stdout).toString('utf8'),
        stderr: Buffer.concat(stderr).toString('utf8')
      }))
    })
  })
}

function registerModels(): void {
  saveCustomProviderModel({
    providerCode: upstreamProvider.providerCode,
    model: upstreamModel,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: 0.002,
    outputUsdPer1M: 0.002,
    cachedInputUsdPer1M: 0.0002,
    actorSystemAccountId: access.systemAccountId
  })
}

function bridgeMappings(): AccountModelMapping[] {
  return [
    {
      sourceModel,
      sourceEndpointFamily: 'generate_content',
      upstreamModel,
      upstreamEndpointFamily: 'chat_completions',
      enabled: true
    },
    {
      sourceModel,
      sourceEndpointFamily: 'stream_generate_content',
      upstreamModel,
      upstreamEndpointFamily: 'chat_completions',
      enabled: true
    }
  ]
}

function bridgeProvider(): { providerCode: string; providerProtocolProfileId: string } {
  const value = (envText('JUHE_REAL_GEMINI_CLI_CHAT_BRIDGE_PROVIDER') || 'openai').toLowerCase()
  if (value === 'openai' || value === 'openai-compatible' || value === 'openai_compatible') {
    return {
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID
    }
  }
  if (value === 'glm') {
    return {
      providerCode: GLM_PROVIDER_CODE,
      providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID
    }
  }
  throw new Error(`JUHE_REAL_GEMINI_CLI_CHAT_BRIDGE_PROVIDER 只支持 openai 或 glm，实际：${value}`)
}

function assertGeneratedGameFiles(result: CliRunResult): Array<{ file: string; bytes: number }> {
  const summary: Array<{ file: string; bytes: number }> = []
  for (const file of requiredFiles) {
    const fullPath = join(outputDir, file)
    assert(existsSync(fullPath), `gemini-cli 未生成 ${fullPath}；${gameGenerationDiagnostics(result)}`)
    const size = statSync(fullPath).size
    assert(size > 100, `gemini-cli 生成的 ${fullPath} 内容过短：${size} bytes；${gameGenerationDiagnostics(result)}`)
    summary.push({ file, bytes: size })
  }
  return summary
}

function gameGenerationDiagnostics(result: CliRunResult): string {
  return [
    `stdout=${sanitizeSecretText(result.stdout).slice(0, 1600)}`,
    `stderr=${sanitizeSecretText(result.stderr).slice(0, 1600)}`,
    `gatewayRequests=${JSON.stringify(gatewayIncomingHits.map(summarizeIncomingHit)).slice(0, 2000)}`
  ].join('；')
}

function openAICompatibleModelsUrl(baseUrl: string): string {
  const url = new URL(baseUrl)
  const normalizedPath = url.pathname.replace(/\/+$/, '')
  url.pathname = normalizedPath.endsWith('/v1') || normalizedPath.endsWith('/v1beta/openai')
    ? `${normalizedPath}/models`
    : `${normalizedPath}/v1/models`
  url.search = ''
  return url.toString()
}

function modelNameFromUnknown(value: unknown): string | undefined {
  if (!value || typeof value !== 'object') return undefined
  const record = value as { id?: unknown; name?: unknown }
  return typeof record.id === 'string'
    ? record.id
    : typeof record.name === 'string'
      ? record.name
      : undefined
}

async function fetchWithTimeout(url: string, init: RequestInit = {}): Promise<Response> {
  const timeoutSignal = AbortSignal.timeout(requestTimeoutMs)
  return await fetch(url, {
    ...init,
    signal: init.signal ?? timeoutSignal
  })
}

function resolveWindowsNodeCliLauncher(command: string): { command: string; args: string[] } | undefined {
  const entryByCommand: Record<string, string> = {
    gemini: 'node_modules\\@google\\gemini-cli\\bundle\\gemini.js'
  }
  const entry = entryByCommand[command]
  if (!entry) return undefined
  const commandPath = findOnPath(`${command}.cmd`) ?? findOnPath(`${command}.ps1`) ?? findOnPath(command)
  if (!commandPath) return undefined
  const baseDir = dirname(commandPath)
  const entryPath = join(baseDir, entry)
  if (!existsSync(entryPath)) return undefined
  const bundledNode = join(baseDir, 'node.exe')
  return {
    command: existsSync(bundledNode) ? bundledNode : 'node.exe',
    args: [entryPath]
  }
}

function findOnPath(filename: string): string | undefined {
  const pathValue = process.env.PATH ?? process.env.Path ?? ''
  for (const rawDir of pathValue.split(pathDelimiter)) {
    const dir = rawDir.trim().replace(/^"|"$/g, '')
    if (!dir) continue
    const candidate = join(dir, filename)
    if (existsSync(candidate)) return candidate
  }
  return undefined
}

function createCliRoot(name: string): string {
  const root = join(tempRoot, 'cli', name)
  mkdirSync(root, { recursive: true })
  return root
}

function isolatedCliEnv(cliRoot: string, extra: Record<string, string>): Record<string, string> {
  const env = sanitizedProcessEnv()
  for (const key of Object.keys(env)) {
    if (isAiCredentialEnvName(key)) delete env[key]
  }
  const appData = join(cliRoot, 'AppData', 'Roaming')
  const localAppData = join(cliRoot, 'AppData', 'Local')
  const xdgConfig = join(cliRoot, '.config')
  const xdgData = join(cliRoot, '.local', 'share')
  const xdgCache = join(cliRoot, '.cache')
  mkdirSync(appData, { recursive: true })
  mkdirSync(localAppData, { recursive: true })
  mkdirSync(xdgConfig, { recursive: true })
  mkdirSync(xdgData, { recursive: true })
  mkdirSync(xdgCache, { recursive: true })
  return {
    ...env,
    HOME: cliRoot,
    USERPROFILE: cliRoot,
    APPDATA: appData,
    LOCALAPPDATA: localAppData,
    XDG_CONFIG_HOME: xdgConfig,
    XDG_DATA_HOME: xdgData,
    XDG_CACHE_HOME: xdgCache,
    ...extra
  }
}

function sanitizedProcessEnv(): Record<string, string> {
  const env: Record<string, string> = {}
  for (const [key, value] of Object.entries(process.env)) {
    if (typeof value === 'string') env[key] = value
  }
  return env
}

function isAiCredentialEnvName(key: string): boolean {
  const normalized = key.toUpperCase()
  return normalized.includes('OPENAI')
    || normalized.includes('ANTHROPIC')
    || normalized.includes('CLAUDE')
    || normalized.includes('CODEX')
    || normalized.includes('OPENCODE')
    || normalized.includes('DEEPSEEK')
    || normalized.includes('GLM')
    || normalized.includes('GEMINI')
    || normalized.endsWith('API_KEY')
    || normalized.endsWith('AUTH_TOKEN')
    || normalized.endsWith('ACCESS_TOKEN')
}

function windowsCommandLine(args: string[]): string {
  return args.map((arg) => windowsQuote(arg)).join(' ')
}

function windowsQuote(value: string): string {
  if (value === '') return '""'
  if (!/[\s"]/u.test(value)) return value
  return `"${value.replaceAll('"', '\\"')}"`
}

function requiredEnv(name: string, aliases: string[] = []): string {
  const value = envText(name, aliases)
  if (!value) {
    throw new Error(`${name} 未设置；运行真实 Gemini CLI Native -> Chat 测试前请通过环境变量传入 API Key`)
  }
  return value
}

function envText(name: string, aliases: string[] = []): string | undefined {
  for (const key of [name, ...aliases]) {
    const value = process.env[key]?.trim()
    if (value) return value
  }
  return undefined
}

function positiveIntegerEnv(name: string): number | undefined {
  const value = envText(name)
  if (!value) return undefined
  const parsed = Number(value)
  return Number.isInteger(parsed) && parsed > 0 ? parsed : undefined
}

function sanitizeBaseUrl(value: string): string {
  try {
    const url = new URL(value)
    url.username = ''
    url.password = ''
    return url.toString().replace(/\/$/, '')
  } catch {
    return sanitizeSecretText(value)
  }
}

function sanitizeSecretText(text: string): string {
  return text
    .replaceAll(realApiKey, '[redacted-real-api-key]')
    .replaceAll(encodeURIComponent(realApiKey), '[redacted-real-api-key]')
}

function responseSnippet(text: string): string {
  return sanitizeSecretText(text).slice(0, 1000)
}

function summarizeCliFailure(result: CliRunResult): string {
  return `exit=${result.exitCode}; stdout=${sanitizeSecretText(result.stdout).slice(0, 1000)}; stderr=${sanitizeSecretText(result.stderr).slice(0, 1000)}`
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  server.listen(0, '127.0.0.1')
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  assert(typeof address === 'object' && address !== null, 'server 未监听端口')
  return { port: address.port }
}

function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server || !server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.close((error) => {
      if (error) rejectPromise(error)
      else resolvePromise()
    })
  })
}
