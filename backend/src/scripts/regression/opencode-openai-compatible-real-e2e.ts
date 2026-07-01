import { strict as assert } from 'node:assert'
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { delimiter as pathDelimiter, dirname, join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express, { type NextFunction, type Request, type Response } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'

interface CliRunResult {
  exitCode: number | null
  stderr: string
  stdout: string
}

interface GatewayIncomingHit {
  authorizationPresent: boolean
  bodySummary: Record<string, unknown>
  method: string
  path: string
  userAgent?: string
}

const realApiKey = requiredEnv('JUHE_REAL_OPENCODE_OPENAI_COMPATIBLE_API_KEY', [
  'JUHE_REAL_OPENAI_COMPATIBLE_API_KEY',
  'JUHE_REAL_HYBRID_API_KEY',
  'HYBRID_REAL_API_KEY'
])
const realBaseUrl = envText('JUHE_REAL_OPENCODE_OPENAI_COMPATIBLE_BASE_URL', [
  'JUHE_REAL_OPENAI_COMPATIBLE_BASE_URL',
  'JUHE_REAL_HYBRID_BASE_URL',
  'HYBRID_REAL_BASE_URL'
]) || 'https://vsllm.com'
const opencodeModel = envText('JUHE_REAL_OPENCODE_MODEL') || 'gpt-5.4-mini'
const requestTimeoutMs = positiveIntegerEnv('JUHE_REAL_OPENCODE_TIMEOUT_MS') ?? 240_000
const marker = `OPENCODE_REAL_GATEWAY_OK_${Date.now()}`

const tempRoot = resolve(tmpdir(), `juhe-ai-opencode-openai-compatible-real-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'opencode-openai-compatible-real.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'opencode-openai-compatible-real-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
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
app.use('/v1', express.raw({ type: () => true, limit: '12mb' }), captureGatewayRawBody, captureIncomingGatewayRequest, openAIGatewayRouter)

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()
  let appServer: http.Server | undefined
  try {
    registerCustomModel()
    const group = repositories.createGroup({
      name: 'opencode OpenAI-compatible 真实网关分组',
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      enabled: true
    }, access)
    const account = repositories.createAccount({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      name: 'opencode OpenAI-compatible 真实上游账户',
      type: 'api_key',
      credentials: {
        api_key: realApiKey,
        base_url: realBaseUrl,
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      supportedModels: [opencodeModel]
    }, access)
    const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'opencode OpenAI-compatible 真实网关 Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(apiKey.key, 'opencode 真实联调本地 API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const gatewayBaseUrl = `http://127.0.0.1:${serverPort(appServer)}`
    const cliVersion = await readCliVersion()
    const result = await runOpencodeCli(gatewayBaseUrl, apiKey.key)
    assert.equal(result.exitCode, 0, `opencode CLI 应成功退出：${summarizeCliFailure(result)}`)
    assert.match(result.stdout, new RegExp(marker), `opencode CLI 输出应包含 marker：${sanitizeSecretText(result.stdout).slice(0, 2000)}`)

    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    assertUsageRecord(account.id, group.id)

    const chatHits = gatewayIncomingHits.filter((hit) => hit.path.split('?', 1)[0].endsWith('/chat/completions'))
    assert(chatHits.length > 0, `opencode 应命中本地 /v1/chat/completions，实际：${JSON.stringify(gatewayIncomingHits)}`)
    assert(chatHits.some((hit) => hit.authorizationPresent), 'opencode 应通过 Bearer 携带本地网关 API Key')

    console.log(JSON.stringify({
      ok: true,
      cli: 'opencode',
      cliVersion,
      provider: OPENAI_COMPATIBLE_PROVIDER_CODE,
      baseUrl: sanitizeBaseUrl(realBaseUrl),
      model: opencodeModel,
      marker,
      chatRequests: chatHits.map((hit) => ({
        model: hit.bodySummary.model,
        stream: hit.bodySummary.stream,
        messageCount: hit.bodySummary.messageCount,
        toolCount: hit.bodySummary.toolCount,
        userAgent: hit.userAgent
      }))
    }, null, 2))
  } finally {
    await closeServer(appServer)
  }
} catch (error) {
  throw new Error(sanitizeSecretText(error instanceof Error ? error.stack ?? error.message : String(error)))
} finally {
  usageRecordQueue.flushAllUsageRecordQueue()
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  auditLogQueue.flushAllAuditLogQueue()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function captureIncomingGatewayRequest(req: Request, _res: Response, next: NextFunction): void {
  const body = parseJsonObject(requestBodyText(req))
  gatewayIncomingHits.push({
    authorizationPresent: Boolean(req.headers.authorization),
    bodySummary: {
      messageCount: Array.isArray(body.messages) ? body.messages.length : undefined,
      model: typeof body.model === 'string' ? body.model : undefined,
      stream: body.stream === true,
      toolCount: Array.isArray(body.tools) ? body.tools.length : 0
    },
    method: req.method,
    path: req.originalUrl || req.url,
    userAgent: typeof req.headers['user-agent'] === 'string' ? req.headers['user-agent'] : undefined
  })
  next()
}

function registerCustomModel(): void {
  saveCustomProviderModel({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    model: opencodeModel,
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

function assertUsageRecord(accountId: string, groupId: string): void {
  const records = repositories.listUsageRecords(access, { pageSize: 100, result: 'all' }).items
  assert(records.some((record) =>
    record.accountId === accountId
    && record.groupId === groupId
    && record.model === opencodeModel
    && record.upstreamModel === opencodeModel
    && record.success === true
  ), `opencode 真实联调应写入成功使用记录，实际模型：${records.map((record) => `${record.model}:${record.success}`).join(', ')}`)
}

async function readCliVersion(): Promise<string> {
  try {
    const cliRoot = createCliRoot('version')
    const result = await runCli({
      args: ['--version'],
      command: 'opencode',
      cwd: cliRoot,
      env: isolatedCliEnv(cliRoot, {}),
      timeoutMs: 30_000
    })
    return `${result.stdout}\n${result.stderr}`.trim().split(/\r?\n/, 1)[0]?.trim() || `exit ${result.exitCode}`
  } catch (error) {
    return `unavailable: ${sanitizeSecretText(error instanceof Error ? error.message : String(error)).slice(0, 300)}`
  }
}

function runOpencodeCli(gatewayBaseUrl: string, localApiKey: string): Promise<CliRunResult> {
  const cliRoot = createCliRoot('opencode')
  const providerId = 'juhe-openai-real'
  const modelId = opencodeModel
  const configContent = JSON.stringify({
    model: `${providerId}/${modelId}`,
    provider: {
      [providerId]: {
        api: `${gatewayBaseUrl}/v1`,
        env: ['JUHE_OPENCODE_REAL_API_KEY'],
        models: {
          [modelId]: {
            attachment: false,
            id: modelId,
            limit: { context: 65_536, output: 8192 },
            name: modelId,
            reasoning: false,
            temperature: true,
            tool_call: true
          }
        },
        name: 'Juhe OpenAI Real Gateway',
        npm: '@ai-sdk/openai-compatible'
      }
    },
    small_model: `${providerId}/${modelId}`
  })
  return runCli({
    args: [
      'run',
      '--format',
      'json',
      '-m',
      `${providerId}/${modelId}`,
      `Reply with exactly this marker and nothing else: ${marker}`
    ],
    command: 'opencode',
    cwd: cliRoot,
    env: isolatedCliEnv(cliRoot, {
      DISABLE_TELEMETRY: '1',
      JUHE_OPENCODE_REAL_API_KEY: localApiKey,
      OPENCODE_CONFIG_CONTENT: configContent,
      OPENCODE_DISABLE_UPDATE_CHECK: '1'
    }),
    timeoutMs: requestTimeoutMs
  })
}

function runCli(input: {
  args: string[]
  command: string
  cwd: string
  env: Record<string, string>
  timeoutMs: number
}): Promise<CliRunResult> {
  const directLauncher = process.platform === 'win32' ? resolveWindowsNodeCliLauncher(input.command) : undefined
  const command = directLauncher?.command ?? (process.platform === 'win32' ? process.env.ComSpec || 'cmd.exe' : input.command)
  const args = directLauncher
    ? [...directLauncher.args, ...input.args]
    : process.platform === 'win32'
      ? ['/d', '/s', '/c', windowsCommandLine([input.command, ...input.args])]
      : input.args

  return new Promise((resolvePromise, rejectPromise) => {
    let settled = false
    const child = spawn(command, args, {
      cwd: input.cwd,
      env: input.env,
      stdio: ['ignore', 'pipe', 'pipe'],
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
    child.on('error', (error) => {
      clearTimeout(timeout)
      settle(() => rejectPromise(error))
    })
    child.on('close', (code) => {
      clearTimeout(timeout)
      settle(() => resolvePromise({
        exitCode: code,
        stderr: Buffer.concat(stderr).toString('utf8'),
        stdout: Buffer.concat(stdout).toString('utf8')
      }))
    })
  })
}

function resolveWindowsNodeCliLauncher(command: string): { command: string; args: string[] } | undefined {
  const entryByCommand: Record<string, string> = {
    opencode: 'node_modules\\opencode-ai\\bin\\opencode'
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
    args: [entryPath],
    command: existsSync(bundledNode) ? bundledNode : 'node.exe'
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
    ...extra,
    APPDATA: appData,
    HOME: cliRoot,
    LOCALAPPDATA: localAppData,
    USERPROFILE: cliRoot,
    XDG_CACHE_HOME: xdgCache,
    XDG_CONFIG_HOME: xdgConfig,
    XDG_DATA_HOME: xdgData
  }
}

function sanitizedProcessEnv(): Record<string, string> {
  const output: Record<string, string> = {}
  for (const [key, value] of Object.entries(process.env)) {
    if (typeof value === 'string') output[key] = value
  }
  return output
}

function isAiCredentialEnvName(name: string): boolean {
  const normalized = name.toUpperCase()
  return normalized.includes('API_KEY')
    || normalized.includes('ACCESS_TOKEN')
    || normalized.includes('AUTH_TOKEN')
    || normalized.includes('OPENAI')
    || normalized.includes('ANTHROPIC')
    || normalized.includes('CLAUDE')
    || normalized.includes('CODEX')
    || normalized.includes('OPENCODE')
}

function createCliRoot(name: string): string {
  const root = join(tempRoot, 'cli', name)
  mkdirSync(root, { recursive: true })
  return root
}

function parseJsonObject(text: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(text) as unknown
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function requestBodyText(req: Request): string {
  const rawBody = (req as Request & { rawBody?: Buffer }).rawBody
  if (Buffer.isBuffer(rawBody)) return rawBody.toString('utf8')
  const body = (req as Request & { body?: unknown }).body
  if (Buffer.isBuffer(body)) return body.toString('utf8')
  return ''
}

function requiredEnv(name: string, aliases: string[] = []): string {
  const value = envText(name, aliases)
  if (!value) {
    throw new Error(`${name} 未设置；运行真实 opencode 测试前请通过环境变量传入 API Key`)
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

function sanitizeSecretText(value: string): string {
  return value.replaceAll(realApiKey, '[redacted-real-api-key]')
}

function summarizeCliFailure(result: CliRunResult): string {
  return `exit=${result.exitCode}; stdout=${sanitizeSecretText(result.stdout).slice(0, 1600)}; stderr=${sanitizeSecretText(result.stderr).slice(0, 1600)}`
}

function windowsCommandLine(parts: string[]): string {
  return parts.map((part) => `"${part.replace(/"/g, '\\"')}"`).join(' ')
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  server.listen(0, '127.0.0.1')
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(typeof address === 'object' && address !== null, 'server 未监听端口')
  return address.port
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
