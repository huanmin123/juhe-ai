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
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_PROVIDER_CODE,
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'

interface GatewayIncomingHit {
  path: string
  method: string
  authorizationPresent: boolean
  xApiKeyPresent: boolean
  userAgent?: string
  codexTurnMetadata?: string
  bodySummary: Record<string, unknown>
}

interface AnthropicUpstreamHit {
  path: string
  method: string
  authorization: string
  xApiKey: string
  bodyText: string
}

interface OpenAIUpstreamHit {
  path: string
  method: string
  authorization: string
  userAgent?: string
  codexTurnMetadata?: string
  bodyText: string
}

interface SeededCliGateways {
  anthropicApiKey: { id: string; key: string }
  deepseekCodexApiKey: { id: string; key: string }
  glmOpencodeApiKey: { id: string; key: string }
}

interface CliRunResult {
  commandLine: string
  exitCode: number | null
  stderr: string
  stdout: string
}

const tempRoot = resolve(tmpdir(), `juhe-ai-cli-local-gateway-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'cli-local-gateway.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'cli-local-gateway-secret'
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
const anthropicUpstreamHits: AnthropicUpstreamHit[] = []
const openAIUpstreamHits: OpenAIUpstreamHit[] = []

async function main(): Promise<void> {
  let anthropicUpstreamServer: http.Server | undefined
  let openAIUpstreamServer: http.Server | undefined
  let gatewayServer: http.Server | undefined
  try {
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
    auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
    gatewayCache.clearGatewayRuntimeCache()

    anthropicUpstreamServer = createAnthropicMockUpstream()
    openAIUpstreamServer = createOpenAIMockUpstream()
    await listen(anthropicUpstreamServer)
    await listen(openAIUpstreamServer)

    const anthropicUpstreamBaseUrl = `http://127.0.0.1:${serverPort(anthropicUpstreamServer)}/v1`
    const openAIUpstreamBaseUrl = `http://127.0.0.1:${serverPort(openAIUpstreamServer)}/v1`
    const seeded = seedCliGateways({
      anthropicUpstreamBaseUrl,
      openAIUpstreamBaseUrl
    })

    gatewayServer = createGatewayServer()
    await listen(gatewayServer)
    const gatewayBaseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`

    const cliVersions = {
      claude: await readCliVersion('claude', ['--version']),
      codex: await readCliVersion('codex', ['--version']),
      opencode: await readCliVersion('opencode', ['--version'])
    }

    await assertClaudeCodeCliThroughLocalGateway(gatewayBaseUrl, seeded.anthropicApiKey.key)
    await assertCodexCliThroughDeepSeekBridge(gatewayBaseUrl, seeded.deepseekCodexApiKey.key)
    await assertOpencodeCliThroughGlmChat(gatewayBaseUrl, seeded.glmOpencodeApiKey.key)

    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    assertUsageRecords(seeded)

    console.log(JSON.stringify({
      cliLocalGateway: {
        versions: cliVersions,
        incomingRequests: gatewayIncomingHits.map(summarizeIncomingHit),
        anthropicUpstreamRequests: anthropicUpstreamHits.map(summarizeAnthropicUpstreamHit),
        openAIUpstreamRequests: openAIUpstreamHits.map(summarizeOpenAIUpstreamHit)
      }
    }, null, 2))
    console.log('cli local gateway regression passed')
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    auditLogQueue.flushAllAuditLogQueue()
    auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
    await closeServer(gatewayServer)
    await closeServer(anthropicUpstreamServer)
    await closeServer(openAIUpstreamServer)
    try {
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

function createGatewayServer(): http.Server {
  const app = express()
  app.use(requestContextMiddleware)
  app.use('/v1', express.raw({ type: () => true, limit: '12mb' }), captureGatewayRawBody, captureIncomingGatewayRequest, openAIGatewayRouter)
  return http.createServer(app)
}

function captureIncomingGatewayRequest(req: Request, _res: Response, next: NextFunction): void {
  const bodyText = requestBodyText(req)
  const body = parseJsonObject(bodyText)
  gatewayIncomingHits.push({
    path: req.originalUrl || req.url,
    method: req.method,
    authorizationPresent: Boolean(req.headers.authorization),
    xApiKeyPresent: Boolean(req.headers['x-api-key']),
    userAgent: headerText(req.headers['user-agent']),
    codexTurnMetadata: headerText(req.headers['x-codex-turn-metadata']),
    bodySummary: summarizeGatewayBody(body)
  })
  next()
}

function seedCliGateways(input: {
  anthropicUpstreamBaseUrl: string
  openAIUpstreamBaseUrl: string
}): SeededCliGateways {
  const anthropicGroup = repositories.createGroup({
    name: '真实 CLI 本地网关 Anthropic 分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    name: '真实 CLI 本地网关 Anthropic 账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-anthropic-cli-upstream',
      base_url: input.anthropicUpstreamBaseUrl,
      supported_endpoint_modes: ['messages_json', 'messages_sse', 'message_token_counting']
    },
    groupId: anthropicGroup.id,
    status: 'active',
    schedulable: true
  }, access)
  const anthropicApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '真实 CLI 本地网关 Anthropic Key',
    groupBindings: [{ groupId: anthropicGroup.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)

  const deepseekGroup = repositories.createGroup({
    name: '真实 CLI 本地网关 DeepSeek Codex 分组',
    providerCode: DEEPSEEK_PROVIDER_CODE,
    providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: DEEPSEEK_PROVIDER_CODE,
    providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
    name: '真实 CLI 本地网关 DeepSeek Codex 桥接账户',
    type: 'api_key',
    clientCompatibility: 'codex_responses',
    credentials: {
      api_key: 'sk-deepseek-cli-upstream',
      base_url: input.openAIUpstreamBaseUrl,
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    groupId: deepseekGroup.id,
    status: 'active',
    schedulable: true
  }, access)
  const deepseekCodexApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '真实 CLI 本地网关 DeepSeek Codex Key',
    groupBindings: [{ groupId: deepseekGroup.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)

  const glmGroup = repositories.createGroup({
    name: '真实 CLI 本地网关 GLM opencode 分组',
    providerCode: GLM_PROVIDER_CODE,
    providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: GLM_PROVIDER_CODE,
    providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
    name: '真实 CLI 本地网关 GLM Chat 账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-glm-cli-upstream',
      base_url: input.openAIUpstreamBaseUrl,
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    groupId: glmGroup.id,
    status: 'active',
    schedulable: true
  }, access)
  const glmOpencodeApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '真实 CLI 本地网关 GLM opencode Key',
    groupBindings: [{ groupId: glmGroup.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)

  assert(anthropicApiKey.key, 'Anthropic 本地 API Key 未返回明文')
  assert(deepseekCodexApiKey.key, 'DeepSeek Codex 本地 API Key 未返回明文')
  assert(glmOpencodeApiKey.key, 'GLM opencode 本地 API Key 未返回明文')
  gatewayCache.clearGatewayRuntimeCache()

  return {
    anthropicApiKey: { id: anthropicApiKey.id, key: anthropicApiKey.key },
    deepseekCodexApiKey: { id: deepseekCodexApiKey.id, key: deepseekCodexApiKey.key },
    glmOpencodeApiKey: { id: glmOpencodeApiKey.id, key: glmOpencodeApiKey.key }
  }
}

async function assertClaudeCodeCliThroughLocalGateway(gatewayBaseUrl: string, localApiKey: string): Promise<void> {
  const marker = 'CLAUDE_LOCAL_GATEWAY_OK'
  const beforeGatewayHits = gatewayIncomingHits.length
  const beforeUpstreamHits = anthropicUpstreamHits.length
  const result = await runClaudeCodeCli({
    gatewayBaseUrl,
    localApiKey,
    marker
  })
  assert.equal(result.exitCode, 0, `Claude Code CLI 应成功退出：${summarizeCliFailure(result)}`)
  assert.match(result.stdout, new RegExp(marker), `Claude Code CLI 输出应包含 mock marker：${sanitizeSecretText(result.stdout).slice(0, 1000)}`)

  const incoming = gatewayIncomingHits.slice(beforeGatewayHits)
  const incomingMessages = incoming.filter((hit) => hit.path.split('?', 1)[0].endsWith('/messages'))
  assert(incomingMessages.length > 0, `Claude Code CLI 应命中本地 /v1/messages，实际：${JSON.stringify(incoming.map(summarizeIncomingHit))}`)
  assert(incomingMessages.some((hit) => hit.authorizationPresent || hit.xApiKeyPresent), 'Claude Code CLI 请求应携带本地网关 API Key')

  const upstreamMessages = anthropicUpstreamHits.slice(beforeUpstreamHits).filter((hit) => hit.path === '/v1/messages')
  assert(upstreamMessages.length > 0, 'Claude Code CLI 应通过网关命中 Anthropic mock /v1/messages')
  assert(upstreamMessages.every((hit) => hit.xApiKey === 'sk-anthropic-cli-upstream'), 'Anthropic 上游应收到账号 API Key')
  assert(upstreamMessages.every((hit) => hit.authorization === ''), 'Anthropic 上游不应收到客户端 Bearer')
}

async function assertCodexCliThroughDeepSeekBridge(gatewayBaseUrl: string, localApiKey: string): Promise<void> {
  const marker = 'CODEX_LOCAL_GATEWAY_OK'
  const beforeGatewayHits = gatewayIncomingHits.length
  const beforeUpstreamHits = openAIUpstreamHits.length
  const result = await runCodexCli({
    gatewayBaseUrl,
    localApiKey,
    marker
  })
  assert.equal(result.exitCode, 0, `Codex CLI 应成功退出：${summarizeCliFailure(result)}`)
  assert.match(result.stdout, new RegExp(marker), `Codex CLI 输出应包含 mock marker：${sanitizeSecretText(result.stdout).slice(0, 1200)}`)

  const incoming = gatewayIncomingHits.slice(beforeGatewayHits)
  const incomingResponses = incoming.filter((hit) => hit.path.split('?', 1)[0].endsWith('/responses'))
  assert(incomingResponses.length > 0, `Codex CLI 应命中本地 /v1/responses，实际：${JSON.stringify(incoming.map(summarizeIncomingHit))}`)
  assert(incomingResponses.some((hit) => hit.authorizationPresent), 'Codex CLI 请求应通过 Bearer 携带本地网关 API Key')
  assert(incomingResponses.some((hit) => Boolean(hit.codexTurnMetadata)), 'Codex CLI 请求应携带 x-codex-turn-metadata 以启用 turn 级兼容策略')
  assert(incomingResponses.some((hit) => hasValidCodexTurnId(hit.codexTurnMetadata)), 'Codex CLI turn metadata 应包含 turn_id')

  const upstream = openAIUpstreamHits.slice(beforeUpstreamHits).filter((hit) => hit.authorization === 'Bearer sk-deepseek-cli-upstream')
  assert(upstream.some((hit) => hit.path === '/v1/chat/completions'), 'DeepSeek Codex bridge 应把 Codex /responses 转为上游 /v1/chat/completions')
  assert(upstream.every((hit) => !hit.authorization.includes(localApiKey)), 'DeepSeek 上游不应收到本地网关 API Key')
  assert(upstream.some((hit) => hit.bodyText.includes('"stream":true')), 'Codex bridge 上游请求应保持流式 Chat Completions')
}

async function assertOpencodeCliThroughGlmChat(gatewayBaseUrl: string, localApiKey: string): Promise<void> {
  const marker = 'OPENCODE_LOCAL_GATEWAY_OK'
  const beforeGatewayHits = gatewayIncomingHits.length
  const beforeUpstreamHits = openAIUpstreamHits.length
  const result = await runOpencodeCli({
    gatewayBaseUrl,
    localApiKey,
    marker
  })
  assert.equal(result.exitCode, 0, `opencode CLI 应成功退出：${summarizeCliFailure(result)}`)
  assert.match(result.stdout, new RegExp(marker), `opencode CLI 输出应包含 mock marker：${sanitizeSecretText(result.stdout).slice(0, 1200)}`)

  const incoming = gatewayIncomingHits.slice(beforeGatewayHits)
  const chatHits = incoming.filter((hit) => hit.path.split('?', 1)[0].endsWith('/chat/completions'))
  assert(chatHits.length > 0, `opencode CLI 应命中本地 /v1/chat/completions，实际：${JSON.stringify(incoming.map(summarizeIncomingHit))}`)
  assert(chatHits.some((hit) => hit.authorizationPresent), 'opencode CLI 请求应通过 Bearer 携带本地网关 API Key')

  const upstream = openAIUpstreamHits.slice(beforeUpstreamHits).filter((hit) => hit.authorization === 'Bearer sk-glm-cli-upstream')
  assert(upstream.some((hit) => hit.path === '/v1/chat/completions'), 'GLM opencode 链路应命中上游 /v1/chat/completions')
  assert(upstream.every((hit) => !hit.authorization.includes(localApiKey)), 'GLM 上游不应收到本地网关 API Key')
}

function runClaudeCodeCli(input: {
  gatewayBaseUrl: string
  localApiKey: string
  marker: string
}): Promise<CliRunResult> {
  const cliRoot = createCliRoot('claude')
  return runCli({
    command: 'claude',
    args: [
      '--print',
      '--setting-sources',
      'local',
      '--no-session-persistence',
      '--output-format',
      'text',
      '--model',
      'claude-haiku-4-5',
      '--max-budget-usd',
      '1',
      `Reply with exactly this marker and nothing else: ${input.marker}`
    ],
    cwd: cliRoot,
    env: isolatedCliEnv(cliRoot, {
      ANTHROPIC_BASE_URL: input.gatewayBaseUrl,
      ANTHROPIC_API_KEY: input.localApiKey,
      CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY: '1',
      CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS: '1',
      DISABLE_AUTOUPDATER: '1',
      DISABLE_BUG_COMMAND: '1',
      DISABLE_ERROR_REPORTING: '1',
      DISABLE_TELEMETRY: '1',
      DISABLE_FEEDBACK_COMMAND: '1'
    }),
    timeoutMs: 180_000
  })
}

function runCodexCli(input: {
  gatewayBaseUrl: string
  localApiKey: string
  marker: string
}): Promise<CliRunResult> {
  const cliRoot = createCliRoot('codex')
  const codexHome = join(cliRoot, '.codex')
  mkdirSync(codexHome, { recursive: true })
  return runCli({
    command: 'codex',
    args: [
      'exec',
      '--json',
      '--ephemeral',
      '--ignore-user-config',
      '--disable',
      'apps',
      '--disable',
      'plugins',
      '--disable',
      'tool_suggest',
      '--disable',
      'remote_plugin',
      '--disable',
      'multi_agent',
      '--disable',
      'multi_agent_v2',
      '--disable',
      'enable_mcp_apps',
      '--disable',
      'standalone_web_search',
      '--disable',
      'image_generation',
      '--disable',
      'computer_use',
      '--disable',
      'browser_use',
      '--disable',
      'browser_use_external',
      '--disable',
      'in_app_browser',
      '--skip-git-repo-check',
      '-C',
      cliRoot,
      '-s',
      'read-only',
      '-c',
      'approval_policy=never',
      '-c',
      'tools.experimental_request_user_input.enabled=false',
      '-c',
      'include_apps_instructions=false',
      '-c',
      'skills.include_instructions=false',
      '-c',
      'skills.bundled.enabled=false',
      '-c',
      'include_permissions_instructions=false',
      '-c',
      'include_collaboration_mode_instructions=false',
      '-c',
      'web_search="disabled"',
      '-c',
      'model_provider=local_gateway',
      '-c',
      'model=gpt-5.3-codex',
      '-c',
      'model_providers.local_gateway.name=LocalGateway',
      '-c',
      `model_providers.local_gateway.base_url=${input.gatewayBaseUrl}/v1`,
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
      JUHE_CODEX_API_KEY: input.localApiKey,
      OPENAI_API_KEY: '',
      DISABLE_TELEMETRY: '1'
    }),
    stdinText: `Reply with exactly this marker and nothing else. Do not run tools: ${input.marker}`,
    timeoutMs: 180_000
  })
}

function runOpencodeCli(input: {
  gatewayBaseUrl: string
  localApiKey: string
  marker: string
}): Promise<CliRunResult> {
  const cliRoot = createCliRoot('opencode')
  const configContent = JSON.stringify({
    provider: {
      'juhe-glm-local': {
        name: 'Juhe GLM Local Gateway',
        npm: '@ai-sdk/openai-compatible',
        api: `${input.gatewayBaseUrl}/v1`,
        env: ['JUHE_OPENCODE_API_KEY'],
        models: {
          'opencode-local': {
            id: 'opencode-local',
            name: 'opencode-local',
            attachment: false,
            reasoning: false,
            tool_call: true,
            temperature: true,
            limit: { context: 65_536, output: 8192 }
          }
        }
      }
    },
    model: 'juhe-glm-local/opencode-local',
    small_model: 'juhe-glm-local/opencode-local'
  })
  return runCli({
    command: 'opencode',
    args: [
      'run',
      '--format',
      'json',
      '-m',
      'juhe-glm-local/opencode-local',
      `Reply with exactly this marker and nothing else: ${input.marker}`
    ],
    cwd: cliRoot,
    env: isolatedCliEnv(cliRoot, {
      JUHE_OPENCODE_API_KEY: input.localApiKey,
      OPENCODE_CONFIG_CONTENT: configContent,
      DISABLE_TELEMETRY: '1',
      OPENCODE_DISABLE_UPDATE_CHECK: '1'
    }),
    timeoutMs: 180_000
  })
}

async function readCliVersion(command: string, args: string[]): Promise<string> {
  const cliRoot = createCliRoot(`version-${command}`)
  try {
    const result = await runCli({
      command,
      args,
      cwd: cliRoot,
      env: isolatedCliEnv(cliRoot, {}),
      timeoutMs: 30_000
    })
    const text = `${result.stdout}\n${result.stderr}`.trim()
    return text.split(/\r?\n/, 1)[0]?.trim() || `exit ${result.exitCode}`
  } catch (error) {
    return `unavailable: ${sanitizeSecretText(error instanceof Error ? error.message : String(error)).slice(0, 300)}`
  }
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
    claude: 'node_modules\\@anthropic-ai\\claude-code\\cli.js',
    codex: 'node_modules\\@openai\\codex\\bin\\codex.js',
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

function createAnthropicMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const path = req.url?.split('?', 1)[0] ?? ''
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(Buffer.from(chunk)))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      anthropicUpstreamHits.push({
        path,
        method: req.method ?? '',
        authorization: String(req.headers.authorization ?? ''),
        xApiKey: String(req.headers['x-api-key'] ?? ''),
        bodyText
      })
      if (req.method === 'GET' && path === '/v1/models') {
        sendJson(res, 200, {
          data: [
            {
              id: 'claude-haiku-4-5',
              type: 'model',
              display_name: 'Claude Haiku 4.5'
            }
          ]
        })
        return
      }
      if (req.method === 'POST' && path === '/v1/messages/count_tokens') {
        sendJson(res, 200, { input_tokens: 8 })
        return
      }
      if (req.method === 'POST' && path === '/v1/messages') {
        const body = parseJsonObject(bodyText)
        if (body.stream === true) {
          sendAnthropicMessageSse(res, 'CLAUDE_LOCAL_GATEWAY_OK', String(body.model ?? 'claude-haiku-4-5'))
        } else {
          sendJson(res, 200, {
            id: 'msg_cli_local_gateway',
            type: 'message',
            role: 'assistant',
            model: String(body.model ?? 'claude-haiku-4-5'),
            content: [{ type: 'text', text: 'CLAUDE_LOCAL_GATEWAY_OK' }],
            stop_reason: 'end_turn',
            usage: { input_tokens: 8, output_tokens: 3 }
          })
        }
        return
      }
      sendJson(res, 404, { error: { type: 'not_found_error', message: `unexpected path ${path}` } })
    })
  })
}

function createOpenAIMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const path = req.url?.split('?', 1)[0] ?? ''
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(Buffer.from(chunk)))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      const authorization = String(req.headers.authorization ?? '')
      openAIUpstreamHits.push({
        path,
        method: req.method ?? '',
        authorization,
        userAgent: headerText(req.headers['user-agent']),
        codexTurnMetadata: headerText(req.headers['x-codex-turn-metadata']),
        bodyText
      })
      if (req.method === 'GET' && path === '/v1/models') {
        sendJson(res, 200, {
          object: 'list',
          data: [
            { id: 'gpt-5.3-codex', object: 'model' },
            { id: 'deepseek-v4-flash', object: 'model' },
            { id: 'opencode-local', object: 'model' },
            { id: 'glm-5.2', object: 'model' }
          ]
        })
        return
      }
      if (req.method === 'POST' && path === '/v1/chat/completions') {
        const body = parseJsonObject(bodyText)
        const marker = authorization === 'Bearer sk-deepseek-cli-upstream'
          ? 'CODEX_LOCAL_GATEWAY_OK'
          : authorization === 'Bearer sk-glm-cli-upstream'
            ? 'OPENCODE_LOCAL_GATEWAY_OK'
            : 'OPENAI_LOCAL_GATEWAY_OK'
        if (body.stream === true) {
          sendOpenAIChatSse(res, marker, String(body.model ?? 'mock-model'))
        } else {
          sendJson(res, 200, openAIChatCompletionJson(marker, String(body.model ?? 'mock-model')))
        }
        return
      }
      if (req.method === 'POST' && path === '/v1/responses') {
        const body = parseJsonObject(bodyText)
        if (body.stream === true) {
          sendOpenAIResponsesSse(res, 'OPENAI_LOCAL_GATEWAY_OK', String(body.model ?? 'mock-model'))
        } else {
          sendJson(res, 200, openAIResponsesJson('OPENAI_LOCAL_GATEWAY_OK', String(body.model ?? 'mock-model')))
        }
        return
      }
      sendJson(res, 404, { error: { code: 'mock_unexpected_path', message: `unexpected path ${path}` } })
    })
  })
}

function sendAnthropicMessageSse(res: http.ServerResponse, text: string, model: string): void {
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'cache-control': 'no-cache',
    connection: 'keep-alive'
  })
  writeSse(res, 'message_start', {
    type: 'message_start',
    message: {
      id: 'msg_cli_local_gateway',
      type: 'message',
      role: 'assistant',
      model,
      content: [],
      stop_reason: null,
      stop_sequence: null,
      usage: { input_tokens: 8, output_tokens: 0 }
    }
  })
  writeSse(res, 'content_block_start', {
    type: 'content_block_start',
    index: 0,
    content_block: { type: 'text', text: '' }
  })
  writeSse(res, 'content_block_delta', {
    type: 'content_block_delta',
    index: 0,
    delta: { type: 'text_delta', text }
  })
  writeSse(res, 'content_block_stop', {
    type: 'content_block_stop',
    index: 0
  })
  writeSse(res, 'message_delta', {
    type: 'message_delta',
    delta: { stop_reason: 'end_turn', stop_sequence: null },
    usage: { output_tokens: 3 }
  })
  writeSse(res, 'message_stop', { type: 'message_stop' })
  res.end()
}

function sendOpenAIChatSse(res: http.ServerResponse, text: string, model: string): void {
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'cache-control': 'no-cache',
    connection: 'keep-alive'
  })
  const created = Math.floor(Date.now() / 1000)
  res.write(`data: ${JSON.stringify({
    id: 'chatcmpl_cli_local_gateway',
    object: 'chat.completion.chunk',
    created,
    model,
    choices: [{ index: 0, delta: { role: 'assistant', content: text }, finish_reason: null }]
  })}\n\n`)
  res.write(`data: ${JSON.stringify({
    id: 'chatcmpl_cli_local_gateway',
    object: 'chat.completion.chunk',
    created,
    model,
    choices: [{ index: 0, delta: {}, finish_reason: 'stop' }],
    usage: { prompt_tokens: 5, completion_tokens: 3, total_tokens: 8 }
  })}\n\n`)
  res.end('data: [DONE]\n\n')
}

function sendOpenAIResponsesSse(res: http.ServerResponse, text: string, model: string): void {
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'cache-control': 'no-cache',
    connection: 'keep-alive'
  })
  writeSse(res, 'response.created', {
    type: 'response.created',
    response: { id: 'resp_cli_local_gateway', model, status: 'in_progress' }
  })
  writeSse(res, 'response.output_text.delta', {
    type: 'response.output_text.delta',
    delta: text
  })
  writeSse(res, 'response.output_item.done', {
    type: 'response.output_item.done',
    item: {
      type: 'message',
      role: 'assistant',
      id: 'msg_cli_local_gateway',
      content: [{ type: 'output_text', text }]
    }
  })
  writeSse(res, 'response.completed', {
    type: 'response.completed',
    response: {
      id: 'resp_cli_local_gateway',
      status: 'completed',
      usage: { input_tokens: 5, output_tokens: 3, total_tokens: 8 }
    }
  })
  res.end()
}

function openAIChatCompletionJson(text: string, model: string): Record<string, unknown> {
  return {
    id: 'chatcmpl_cli_local_gateway',
    object: 'chat.completion',
    created: Math.floor(Date.now() / 1000),
    model,
    choices: [
      {
        index: 0,
        message: { role: 'assistant', content: text },
        finish_reason: 'stop'
      }
    ],
    usage: { prompt_tokens: 5, completion_tokens: 3, total_tokens: 8 }
  }
}

function openAIResponsesJson(text: string, model: string): Record<string, unknown> {
  return {
    id: 'resp_cli_local_gateway',
    object: 'response',
    model,
    status: 'completed',
    output: [
      {
        type: 'message',
        role: 'assistant',
        id: 'msg_cli_local_gateway',
        content: [{ type: 'output_text', text }]
      }
    ],
    usage: { input_tokens: 5, output_tokens: 3, total_tokens: 8 }
  }
}

function assertUsageRecords(seeded: SeededCliGateways): void {
  const records = repositories.listUsageRecords(undefined, { page: 1, pageSize: 100 }).items
  const apiKeyIds = new Set([
    seeded.anthropicApiKey.id,
    seeded.deepseekCodexApiKey.id,
    seeded.glmOpencodeApiKey.id
  ])
  const recordsByApiKey = records.filter((record) => typeof record.apiKeyId === 'string' && apiKeyIds.has(record.apiKeyId))
  assert(recordsByApiKey.some((record) => record.apiKeyId === seeded.anthropicApiKey.id && record.success === true), 'Claude Code 链路应写入成功使用记录')
  assert(recordsByApiKey.some((record) => record.apiKeyId === seeded.deepseekCodexApiKey.id && record.success === true), 'Codex/DeepSeek 链路应写入成功使用记录')
  assert(recordsByApiKey.some((record) => record.apiKeyId === seeded.glmOpencodeApiKey.id && record.success === true), 'opencode/GLM 链路应写入成功使用记录')
}

function summarizeGatewayBody(body: Record<string, unknown>): Record<string, unknown> {
  return {
    model: typeof body.model === 'string' ? body.model : undefined,
    stream: body.stream === true,
    inputType: Array.isArray(body.input) ? 'array' : typeof body.input,
    messages: Array.isArray(body.messages) ? body.messages.length : undefined,
    tools: Array.isArray(body.tools) ? body.tools.length : undefined,
    maxTokens: typeof body.max_tokens === 'number' ? body.max_tokens : undefined
  }
}

function summarizeIncomingHit(hit: GatewayIncomingHit): Record<string, unknown> {
  return {
    path: hit.path,
    method: hit.method,
    authorizationPresent: hit.authorizationPresent,
    xApiKeyPresent: hit.xApiKeyPresent,
    userAgent: hit.userAgent,
    codexTurnMetadataPresent: Boolean(hit.codexTurnMetadata),
    bodySummary: hit.bodySummary
  }
}

function summarizeAnthropicUpstreamHit(hit: AnthropicUpstreamHit): Record<string, unknown> {
  return {
    path: hit.path,
    method: hit.method,
    authorizationPresent: Boolean(hit.authorization),
    xApiKey: hit.xApiKey ? 'sk-***' : '',
    bodyBytes: Buffer.byteLength(hit.bodyText, 'utf8')
  }
}

function summarizeOpenAIUpstreamHit(hit: OpenAIUpstreamHit): Record<string, unknown> {
  return {
    path: hit.path,
    method: hit.method,
    authorization: hit.authorization ? 'Bearer sk-***' : '',
    userAgent: hit.userAgent,
    codexTurnMetadataPresent: Boolean(hit.codexTurnMetadata),
    bodyBytes: Buffer.byteLength(hit.bodyText, 'utf8')
  }
}

function summarizeCliFailure(result: CliRunResult): string {
  return JSON.stringify({
    commandLine: result.commandLine,
    exitCode: result.exitCode,
    stdout: sanitizeSecretText(result.stdout).slice(0, 1200),
    stderr: sanitizeSecretText(result.stderr).slice(0, 1200)
  })
}

function hasValidCodexTurnId(value: string | undefined): boolean {
  if (!value) return false
  const parsed = safeParseJson(value)
  return typeof parsed === 'object'
    && parsed !== null
    && !Array.isArray(parsed)
    && typeof (parsed as { turn_id?: unknown }).turn_id === 'string'
    && Boolean((parsed as { turn_id: string }).turn_id.trim())
}

function requestBodyText(req: Request): string {
  if (Buffer.isBuffer(req.body)) {
    return req.body.toString('utf8')
  }
  return ''
}

function parseJsonObject(text: string): Record<string, unknown> {
  const parsed = safeParseJson(text)
  return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
    ? parsed as Record<string, unknown>
    : {}
}

function safeParseJson(text: string): unknown {
  try {
    return JSON.parse(text)
  } catch {
    return undefined
  }
}

function headerText(value: unknown): string | undefined {
  if (Array.isArray(value)) return value.join(', ')
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function sendJson(res: http.ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify(body))
}

function writeSse(res: http.ServerResponse, event: string, data: unknown): void {
  res.write(`event: ${event}\n`)
  res.write(`data: ${JSON.stringify(data)}\n\n`)
}

function windowsCommandLine(args: string[]): string {
  return args.map(quoteWindowsCommandArg).join(' ')
}

function quoteWindowsCommandArg(value: string): string {
  if (/^[A-Za-z0-9_./:@=+-]+$/.test(value)) {
    return value
  }
  return `"${value.replace(/(["^&|<>%])/g, '^$1')}"`
}

function sanitizedProcessEnv(): Record<string, string> {
  const output: Record<string, string> = {}
  for (const [key, value] of Object.entries(process.env)) {
    if (!key || key.includes('=') || value === undefined) continue
    output[key] = value
  }
  return output
}

function sanitizeSecretText(value: string): string {
  return value.replace(/sk-[A-Za-z0-9_-]+/g, 'sk-***')
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  server.listen(0, '127.0.0.1')
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
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

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(address && typeof address === 'object', 'server address unavailable')
  return address.port
}

main().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
