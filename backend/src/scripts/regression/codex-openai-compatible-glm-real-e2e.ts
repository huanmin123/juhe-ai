import { strict as assert } from 'node:assert'
import { spawn } from 'node:child_process'
import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express, { type NextFunction, type Request, type Response } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'

interface GatewayIncomingHit {
  path: string
  method: string
  authorizationPresent: boolean
  codexTurnMetadata?: string
  bodySummary: Record<string, unknown>
}

interface CliRunResult {
  exitCode: number | null
  stderr: string
  stdout: string
}

const realApiKey = requiredEnv('JUHE_REAL_CODEX_OPENAI_COMPATIBLE_API_KEY', ['JUHE_REAL_OPENAI_COMPATIBLE_API_KEY', 'JUHE_REAL_HYBRID_API_KEY'])
const realBaseUrl = envText('JUHE_REAL_CODEX_OPENAI_COMPATIBLE_BASE_URL', ['JUHE_REAL_OPENAI_COMPATIBLE_BASE_URL', 'JUHE_REAL_HYBRID_BASE_URL']) || 'https://vsllm.com'
const downstreamModel = envText('JUHE_REAL_CODEX_DOWNSTREAM_MODEL') || 'gpt-5.3-codex'
const upstreamModel = envText('JUHE_REAL_CODEX_UPSTREAM_MODEL') || 'glm-4.7-flash'
const requestTimeoutMs = positiveIntegerEnv('JUHE_REAL_CODEX_CLI_TIMEOUT_MS') ?? 240_000
const programmingTaskEnabled = booleanEnv('JUHE_REAL_CODEX_PROGRAMMING_TASK')
const marker = `CODEX_GPT_TO_GLM_OK_${Date.now()}`

const tempRoot = resolve(tmpdir(), `juhe-ai-codex-openai-compatible-glm-real-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'codex-openai-compatible-glm-real.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'codex-openai-compatible-glm-real-secret'
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

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()
  let gatewayServer: http.Server | undefined
  try {
    registerCustomModels()
    const group = repositories.createGroup({
      name: 'Codex GPT 转 GLM 真实网关分组',
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      enabled: true
    }, access)
    repositories.createAccount({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      name: 'Codex GPT 转 GLM 真实上游账户',
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
      modelMappings: [{
        sourceModel: downstreamModel,
        sourceEndpointFamily: 'responses',
        upstreamModel,
        upstreamEndpointFamily: 'chat_completions',
        enabled: true
      }]
    }, access)
    const apiKey = repositories.createApiKeyRecord({
      name: 'Codex GPT 转 GLM 真实网关 Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(apiKey.key, '真实联调本地 API Key 未返回明文密钥')

    gatewayServer = createGatewayServer()
    await listen(gatewayServer)
    const gatewayBaseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`
    const programmingProjectRoot = programmingTaskEnabled ? seedProgrammingProject() : undefined
    const result = programmingProjectRoot
      ? await runCodexProgrammingCli(gatewayBaseUrl, apiKey.key, programmingProjectRoot)
      : await runCodexCli(gatewayBaseUrl, apiKey.key)
    assert.equal(result.exitCode, 0, `Codex CLI 应成功退出：${summarizeCliFailure(result)}`)
    if (programmingProjectRoot) {
      const testResult = await runProjectTests(programmingProjectRoot)
      assert.equal(testResult.exitCode, 0, `Codex 编程任务完成后测试应通过：${summarizeCliFailure(testResult)}`)
    } else {
      assert.match(result.stdout, new RegExp(marker), `Codex CLI 输出应包含 marker：${sanitizeSecretText(result.stdout).slice(0, 2000)}`)
    }

    const responsesHits = gatewayIncomingHits.filter((hit) => hit.path.split('?', 1)[0].endsWith('/responses'))
    assert(responsesHits.length > 0, `Codex CLI 应命中本地 /v1/responses：${JSON.stringify(gatewayIncomingHits)}`)
    assert(responsesHits.some((hit) => hit.authorizationPresent), 'Codex CLI 应通过 Bearer 携带本地 API Key')
    assert(responsesHits.some((hit) => hasValidCodexTurnId(hit.codexTurnMetadata)), 'Codex CLI 应携带有效 x-codex-turn-metadata.turn_id')
    assert(responsesHits.some((hit) => hit.bodySummary.model === downstreamModel), 'Codex CLI 下游请求应保留 GPT/Codex 模型名')

    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    const records = repositories.listUsageRecords(undefined, { page: 1, pageSize: 50 }).items
    assert(records.some((record) => record.success === true && record.model === downstreamModel), '使用记录应保存下游 GPT/Codex 模型名并标记成功')

    console.log(JSON.stringify({
      ok: true,
      cli: 'codex',
      provider: OPENAI_COMPATIBLE_PROVIDER_CODE,
      baseUrl: sanitizeBaseUrl(realBaseUrl),
      downstreamModel,
      upstreamModel,
      mode: programmingProjectRoot ? 'programming_task' : 'marker',
      marker: programmingProjectRoot ? undefined : marker,
      programmingProjectRoot,
      responsesRequests: responsesHits.map((hit) => ({
        path: hit.path,
        model: hit.bodySummary.model,
        stream: hit.bodySummary.stream,
        codexTurnMetadataPresent: Boolean(hit.codexTurnMetadata)
      }))
    }, null, 2))
  } finally {
    await closeServer(gatewayServer)
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

function createGatewayServer(): http.Server {
  const app = express()
  app.use(requestContextMiddleware)
  app.use('/v1', express.raw({ type: () => true, limit: '12mb' }), captureGatewayRawBody, captureIncomingGatewayRequest, openAIGatewayRouter)
  return http.createServer(app)
}

function captureIncomingGatewayRequest(req: Request, _res: Response, next: NextFunction): void {
  const body = parseJsonObject(requestBodyText(req))
  gatewayIncomingHits.push({
    path: req.originalUrl || req.url,
    method: req.method,
    authorizationPresent: Boolean(req.headers.authorization),
    codexTurnMetadata: headerText(req.headers['x-codex-turn-metadata']),
    bodySummary: {
      model: typeof body.model === 'string' ? body.model : undefined,
      stream: body.stream === true,
      inputType: Array.isArray(body.input) ? 'array' : typeof body.input
    }
  })
  next()
}

function registerCustomModels(): void {
  for (const model of [downstreamModel, upstreamModel]) {
    saveCustomProviderModel({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      model,
      scope: 'personal',
      systemAccountId: access.systemAccountId,
      status: 'active',
      supportedApiProtocols: ['chat_completions', 'responses'],
      inputUsdPer1M: 0.002,
      outputUsdPer1M: 0.002,
      cachedInputUsdPer1M: 0.0002,
      actorSystemAccountId: access.systemAccountId
    })
  }
}

function runCodexCli(gatewayBaseUrl: string, localApiKey: string): Promise<CliRunResult> {
  const cliRoot = join(tempRoot, 'codex-cli')
  const codexHome = join(cliRoot, '.codex')
  mkdirSync(codexHome, { recursive: true })
  return runCli({
    args: [
      'exec',
      '--json',
      '--ephemeral',
      '--ignore-user-config',
      '--skip-git-repo-check',
      '-C',
      cliRoot,
      '-s',
      'read-only',
      '-c',
      'approval_policy=never',
      '-c',
      'model_provider=local_gateway',
      '-c',
      `model=${downstreamModel}`,
      '-c',
      'model_providers.local_gateway.name=LocalGateway',
      '-c',
      `model_providers.local_gateway.base_url=${gatewayBaseUrl}/v1`,
      '-c',
      'model_providers.local_gateway.env_key=JUHE_CODEX_API_KEY',
      '-c',
      'model_providers.local_gateway.wire_api=responses',
      '-c',
      'model_providers.local_gateway.requires_openai_auth=false',
      '-c',
      'model_providers.local_gateway.request_max_retries=0',
      '-c',
      'model_providers.local_gateway.stream_max_retries=0',
      '-'
    ],
    cwd: cliRoot,
    env: isolatedCliEnv(cliRoot, {
      CODEX_HOME: codexHome,
      JUHE_CODEX_API_KEY: localApiKey,
      OPENAI_API_KEY: '',
      DISABLE_TELEMETRY: '1'
    }),
    stdinText: `Reply with exactly this marker and nothing else. Do not run tools: ${marker}`,
    timeoutMs: requestTimeoutMs
  })
}

function runCodexProgrammingCli(gatewayBaseUrl: string, localApiKey: string, projectRoot: string): Promise<CliRunResult> {
  const codexHome = join(projectRoot, '.codex')
  mkdirSync(codexHome, { recursive: true })
  return runCli({
    args: [
      'exec',
      '--json',
      '--ephemeral',
      '--ignore-user-config',
      '--skip-git-repo-check',
      '-C',
      projectRoot,
      '-s',
      'workspace-write',
      '-c',
      'approval_policy=never',
      '-c',
      'model_provider=local_gateway',
      '-c',
      `model=${downstreamModel}`,
      '-c',
      'model_providers.local_gateway.name=LocalGateway',
      '-c',
      `model_providers.local_gateway.base_url=${gatewayBaseUrl}/v1`,
      '-c',
      'model_providers.local_gateway.env_key=JUHE_CODEX_API_KEY',
      '-c',
      'model_providers.local_gateway.wire_api=responses',
      '-c',
      'model_providers.local_gateway.requires_openai_auth=false',
      '-c',
      'model_providers.local_gateway.request_max_retries=0',
      '-c',
      'model_providers.local_gateway.stream_max_retries=0',
      '-'
    ],
    cwd: projectRoot,
    env: isolatedCliEnv(projectRoot, {
      CODEX_HOME: codexHome,
      JUHE_CODEX_API_KEY: localApiKey,
      OPENAI_API_KEY: '',
      DISABLE_TELEMETRY: '1'
    }),
    stdinText: [
      '这是一个很小的编程任务。',
      '请实现 src/isBalanced.js 中的 isBalanced(input) 函数，判断字符串里的 (), [], {} 是否括号平衡。',
      '要求忽略非括号字符，导出函数名保持 isBalanced。',
      '请运行 node --test test/isBalanced.test.js，并修复直到测试通过。',
      '不要安装依赖。'
    ].join('\n'),
    timeoutMs: requestTimeoutMs
  })
}

function seedProgrammingProject(): string {
  const projectRoot = join(tempRoot, 'programming-task')
  mkdirSync(join(projectRoot, 'src'), { recursive: true })
  mkdirSync(join(projectRoot, 'test'), { recursive: true })
  writeFileSync(join(projectRoot, 'package.json'), `${JSON.stringify({
    type: 'module',
    scripts: {
      test: 'node --test test/isBalanced.test.js'
    }
  }, null, 2)}\n`, 'utf8')
  writeFileSync(join(projectRoot, 'src', 'isBalanced.js'), [
    'export function isBalanced(input) {',
    '  throw new Error("TODO: implement");',
    '}',
    ''
  ].join('\n'), 'utf8')
  writeFileSync(join(projectRoot, 'test', 'isBalanced.test.js'), [
    'import test from "node:test";',
    'import assert from "node:assert/strict";',
    'import { isBalanced } from "../src/isBalanced.js";',
    '',
    'test("accepts balanced bracket strings", () => {',
    '  assert.equal(isBalanced(""), true);',
    '  assert.equal(isBalanced("([]{})"), true);',
    '  assert.equal(isBalanced("a(b[c]{d}e)f"), true);',
    '});',
    '',
    'test("rejects unbalanced or misordered brackets", () => {',
    '  assert.equal(isBalanced("("), false);',
    '  assert.equal(isBalanced("([)]"), false);',
    '  assert.equal(isBalanced("(()"), false);',
    '  assert.equal(isBalanced("())"), false);',
    '});',
    ''
  ].join('\n'), 'utf8')
  return projectRoot
}

function runProjectTests(projectRoot: string): Promise<CliRunResult> {
  return new Promise((resolvePromise, rejectPromise) => {
    const child = spawn(process.execPath, ['--test', 'test/isBalanced.test.js'], {
      cwd: projectRoot,
      env: process.env,
      stdio: ['ignore', 'pipe', 'pipe']
    })
    const stdout: Buffer[] = []
    const stderr: Buffer[] = []
    child.stdout.on('data', (chunk: Buffer) => stdout.push(chunk))
    child.stderr.on('data', (chunk: Buffer) => stderr.push(chunk))
    child.on('error', rejectPromise)
    child.on('close', (code) => resolvePromise({
      exitCode: code,
      stdout: Buffer.concat(stdout).toString('utf8'),
      stderr: Buffer.concat(stderr).toString('utf8')
    }))
  })
}

function runCli(input: {
  args: string[]
  cwd: string
  env: Record<string, string>
  stdinText: string
  timeoutMs: number
}): Promise<CliRunResult> {
  return new Promise((resolvePromise, rejectPromise) => {
    let settled = false
    const child = spawn('codex', input.args, {
      cwd: input.cwd,
      env: input.env,
      stdio: ['pipe', 'pipe', 'pipe'],
      windowsHide: true
    })
    const stdout: Buffer[] = []
    const stderr: Buffer[] = []
    const timeout = setTimeout(() => {
      child.kill()
      settle(() => rejectPromise(new Error(`codex 超时；stdout=${sanitizeSecretText(Buffer.concat(stdout).toString('utf8')).slice(0, 1200)}；stderr=${sanitizeSecretText(Buffer.concat(stderr).toString('utf8')).slice(0, 1200)}`)))
    }, input.timeoutMs)
    const settle = (fn: () => void) => {
      if (settled) return
      settled = true
      fn()
    }
    child.stdout.on('data', (chunk: Buffer) => stdout.push(chunk))
    child.stderr.on('data', (chunk: Buffer) => stderr.push(chunk))
    child.stdin.end(input.stdinText)
    child.on('error', (error) => {
      clearTimeout(timeout)
      settle(() => rejectPromise(error))
    })
    child.on('close', (code) => {
      clearTimeout(timeout)
      settle(() => resolvePromise({
        exitCode: code,
        stdout: Buffer.concat(stdout).toString('utf8'),
        stderr: Buffer.concat(stderr).toString('utf8')
      }))
    })
  })
}

function isolatedCliEnv(cliRoot: string, extra: Record<string, string>): Record<string, string> {
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
  const env = { ...process.env } as Record<string, string>
  for (const key of Object.keys(env)) {
    if (isAiCredentialEnvName(key)) delete env[key]
  }
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

function isAiCredentialEnvName(key: string): boolean {
  const normalized = key.toUpperCase()
  return normalized.includes('OPENAI')
    || normalized.includes('ANTHROPIC')
    || normalized.includes('CLAUDE')
    || normalized.includes('CODEX')
    || normalized.includes('OPENCODE')
    || normalized.includes('DEEPSEEK')
    || normalized.includes('GLM')
    || normalized.endsWith('API_KEY')
    || normalized.endsWith('AUTH_TOKEN')
    || normalized.endsWith('ACCESS_TOKEN')
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
  const rawBody = (req as { rawBody?: Buffer }).rawBody
  return rawBody ? rawBody.toString('utf8') : ''
}

function hasValidCodexTurnId(value: string | undefined): boolean {
  if (!value) return false
  const parsed = parseJsonObject(value)
  return typeof parsed.turn_id === 'string' && Boolean(parsed.turn_id.trim())
}

function headerText(value: string | string[] | undefined): string | undefined {
  return Array.isArray(value) ? value[0] : value
}

function requiredEnv(name: string, aliases: string[] = []): string {
  const value = envText(name, aliases)
  if (!value) throw new Error(`${name} 未设置`)
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

function booleanEnv(name: string): boolean {
  const value = envText(name)?.toLowerCase()
  return value === '1' || value === 'true' || value === 'yes'
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

function summarizeCliFailure(result: CliRunResult): string {
  return JSON.stringify({
    exitCode: result.exitCode,
    stdout: sanitizeSecretText(result.stdout).slice(0, 1200),
    stderr: sanitizeSecretText(result.stderr).slice(0, 1200)
  })
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
