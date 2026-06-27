import { strict as assert } from 'node:assert'
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { delimiter as pathDelimiter, dirname, join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express, { type NextFunction, type Request, type Response } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
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

const realApiKey = requiredEnv('JUHE_REAL_GEMINI_API_KEY', ['JUHE_REAL_GEMINI_NATIVE_API_KEY'])
const realBaseUrl = envText('JUHE_REAL_GEMINI_BASE_URL') || 'https://vsllm.com'
const cliModel = envText('JUHE_REAL_GEMINI_CLI_MODEL') || 'gemini-3.5-flash'
const promptText = envText('JUHE_REAL_GEMINI_CLI_PROMPT') || '只输出 GEMINI_CLI_REAL_GATEWAY_OK，不要解释，不要调用工具。'
const requestTimeoutMs = positiveIntegerEnv('JUHE_REAL_GEMINI_CLI_REQUEST_TIMEOUT_MS') ?? 180_000

const tempRoot = resolve(tmpdir(), `juhe-ai-gemini-cli-real-gateway-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'gemini-cli-real-gateway-e2e.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gemini-cli-real-gateway-e2e-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
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
    const group = repositories.createGroup({
      name: 'Gemini CLI 真实网关回归分组',
      providerCode: GEMINI_PROVIDER_CODE,
      providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
      enabled: true
    }, access)
    saveCustomProviderModel({
      providerCode: GEMINI_PROVIDER_CODE,
      model: cliModel,
      scope: 'personal',
      systemAccountId: access.systemAccountId,
      status: 'active',
      supportedApiProtocols: ['generate_content', 'stream_generate_content', 'count_tokens'],
      inputUsdPer1M: 0.002,
      outputUsdPer1M: 0.002,
      cachedInputUsdPer1M: 0.0002,
      actorSystemAccountId: access.systemAccountId
    })
    repositories.createAccount({
      providerCode: GEMINI_PROVIDER_CODE,
      providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
      name: 'Gemini CLI 真实网关回归账户',
      type: 'api_key',
      credentials: {
        api_key: realApiKey,
        base_url: realBaseUrl
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      supportedModels: [cliModel]
    }, access)
    const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'Gemini CLI 真实网关回归 Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(apiKey.key, 'Gemini CLI 真实网关回归 API Key 未返回明文密钥')

    gatewayCache.clearGatewayRuntimeCache()

    appServer = http.createServer(app)
    await listen(appServer)
    const gatewayBaseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    const result = await runGeminiCli({
      gatewayBaseUrl,
      localApiKey: apiKey.key
    })
    assert.equal(result.exitCode, 0, `gemini-cli 应成功退出：${summarizeCliFailure(result)}`)
    assert.match(result.stdout, /GEMINI_CLI_REAL_GATEWAY_OK/, `gemini-cli 输出应包含目标标记：${sanitizeSecretText(result.stdout).slice(0, 1200)}`)
    assert(gatewayIncomingHits.some((hit) => hit.path.includes(':generateContent') || hit.path.includes(':streamGenerateContent')), `gemini-cli 应命中 Gemini native 生成路径，实际：${JSON.stringify(gatewayIncomingHits.map(summarizeIncomingHit), null, 2)}`)

    console.log(JSON.stringify({
      ok: true,
      provider: GEMINI_PROVIDER_CODE,
      profile: GEMINI_NATIVE_V1BETA_PROFILE_ID,
      baseUrl: sanitizeBaseUrl(realBaseUrl),
      model: cliModel,
      gatewayRequests: gatewayIncomingHits.map(summarizeIncomingHit),
      cli: {
        commandLine: result.commandLine,
        exitCode: result.exitCode,
        stdout: sanitizeSecretText(result.stdout).slice(0, 1200)
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

function captureIncomingGatewayRequest(req: Request, _res: Response, next: NextFunction): void {
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
  for (const key of ['model', 'stream', 'temperature', 'max_tokens', 'contents', 'config', 'generationConfig']) {
    if (key in body) summary[key] = body[key]
  }
  return summary
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
  return parsed && typeof parsed === 'object' && !Array.isArray(parsed)
    ? parsed as Record<string, unknown>
    : {}
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
      promptText,
      '--output-format',
      'text',
      '--model',
      cliModel,
      '--skip-trust',
      '--approval-mode',
      'plan'
    ],
    cwd: cliRoot,
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
    if (input.stdinText) {
      child.stdin.end(input.stdinText)
    } else {
      child.stdin.end()
    }
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
    if (isAiCredentialEnvName(key)) {
      delete env[key]
    }
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
    throw new Error(`${name} 未设置；运行真实 Gemini CLI 测试前请通过环境变量传入 API Key`)
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
  return text.replaceAll(realApiKey, '[redacted-real-api-key]')
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
