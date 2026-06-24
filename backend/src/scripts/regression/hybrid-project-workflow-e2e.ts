import { strict as assert } from 'node:assert'
import { spawnSync } from 'node:child_process'
import { channel } from 'node:diagnostics_channel'
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import { dirname, join, normalize, resolve } from 'node:path'
import { tmpdir } from 'node:os'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import type { ApiKeyHybridRoutingConfig, ProviderCode } from '../../domain/types.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { logger } from '../../shared/logger.js'

type WorkflowStrategyName = 'full_hybrid' | 'hybrid_main_agent' | 'fixed_gpt55' | 'fixed_glm52' | 'fixed_opus'
type WorkflowRole = 'planner' | 'executor' | 'reviewer'

interface WorkflowTask {
  id: string
  title: string
  instructions: string
  expectedFiles: string[]
}

interface AgentCallResult {
  attempts?: number
  content: string
  durationMs: number
  error?: string
  model?: string
  ok: boolean
  retryErrors?: string[]
  status: number
}

interface ValidationResult {
  exitCode: number
  output: string
  ok: boolean
}

interface TaskRunResult {
  appliedFiles: string[]
  execution: AgentCallResult
  id: string
  repair?: AgentCallResult
  repairAppliedFiles?: string[]
  test: ValidationResult
  title: string
}

interface WorkflowRunResult {
  directCallCounts: Record<string, number>
  durationMs: number
  finalReview: AgentCallResult
  finalTest: ValidationResult
  ok: boolean
  plan: AgentCallResult
  strategy: WorkflowStrategyName
  tasks: TaskRunResult[]
}

interface UsageCountRow {
  traffic_source: string
  model: string | null
  success: number
  count: number
  cost_usd: number | null
}

interface HybridRouteEvent {
  affinityApplied?: boolean
  affinityReason?: string
  confidence?: number
  level?: number
  levelRange?: [number, number]
  outcome?: string
  scoringCacheHit?: boolean
  scoringDefaulted?: boolean
  scoringErrorCode?: string
  scoringErrorMessage?: string
  scoringReason?: string
  sessionId?: string
  targetModel?: string
}

const realApiKey = requiredEnv('JUHE_REAL_HYBRID_PROJECT_API_KEY', [
  'JUHE_REAL_HYBRID_API_KEY',
  'JUHE_REAL_HYBRID_QUALITY_API_KEY',
  'HYBRID_REAL_API_KEY'
])
const realBaseUrl = envText('JUHE_REAL_HYBRID_PROJECT_BASE_URL', [
  'JUHE_REAL_HYBRID_BASE_URL',
  'JUHE_REAL_HYBRID_QUALITY_BASE_URL',
  'HYBRID_REAL_BASE_URL'
]) || 'https://vsllm.com'
const repoUrl = envText('JUHE_REAL_HYBRID_PROJECT_REPO_URL') || 'https://github.com/codescandy/dash-ui-react-vitejs-typescript.git'
const taskLimit = Math.min(workflowTasks().length, Math.max(1, positiveIntegerEnv('JUHE_REAL_HYBRID_PROJECT_TASKS') ?? 4))
const requestTimeoutMs = positiveIntegerEnv('JUHE_REAL_HYBRID_PROJECT_REQUEST_TIMEOUT_MS') ?? 240_000
const requestIntervalMs = positiveIntegerEnv('JUHE_REAL_HYBRID_PROJECT_REQUEST_INTERVAL_MS') ?? 6_500
const upstreamRetryCount = positiveIntegerEnv('JUHE_REAL_HYBRID_PROJECT_UPSTREAM_RETRIES') ?? 20
const upstreamRetryDelayMs = positiveIntegerEnv('JUHE_REAL_HYBRID_PROJECT_UPSTREAM_RETRY_DELAY_MS') ?? 5_000
const repositoryCloneRetries = positiveIntegerEnv('JUHE_REAL_HYBRID_PROJECT_CLONE_RETRIES') ?? 3
const outputMaxTokens = positiveIntegerEnv('JUHE_REAL_HYBRID_PROJECT_OUTPUT_MAX_TOKENS') ?? 4_000
const scoringCacheTtlSeconds = Math.min(
  3600,
  Math.max(300, Math.ceil(((requestTimeoutMs + upstreamRetryDelayMs) * (upstreamRetryCount + 1)) / 1000))
)
const outputPath = envText('JUHE_REAL_HYBRID_PROJECT_OUTPUT_PATH')
const runBuildValidation = booleanEnv('JUHE_REAL_HYBRID_PROJECT_RUN_BUILD') ?? false
const allowGeneratedFixture = booleanEnv('JUHE_REAL_HYBRID_PROJECT_ALLOW_GENERATED_FIXTURE') ?? false
const projectProfile = allowGeneratedFixture || repoUrl === 'local:generated'
  ? 'Generated React + Vite + TypeScript admin dashboard fixture, src > 10k lines'
  : 'React + Vite + TypeScript admin dashboard, src ~= 22k lines'
const selectedStrategies = workflowStrategies()
const hybridRouteEvents: HybridRouteEvent[] = []
const hybridRouteDiagnosticsChannel = channel('juhe-ai:hybrid-route-decision')
const hybridRouteDiagnosticsSubscriber = (message: unknown): void => {
  if (typeof message === 'object' && message !== null) {
    hybridRouteEvents.push(message as HybridRouteEvent)
  }
}
hybridRouteDiagnosticsChannel.subscribe(hybridRouteDiagnosticsSubscriber)

const scoringModel = envText('JUHE_REAL_HYBRID_PROJECT_SCORING_MODEL') || 'gpt-5.4-mini'
const flashModel = envText('JUHE_REAL_HYBRID_PROJECT_MODEL_1_2') || 'deepseek-ai-v4-flash'
const lowModel = envText('JUHE_REAL_HYBRID_PROJECT_MODEL_1_3') || 'gpt-5.4-mini'
const glm51Model = envText('JUHE_REAL_HYBRID_PROJECT_MODEL_5_6') || 'glm-5.1'
const glmModel = envText('JUHE_REAL_HYBRID_PROJECT_MODEL_7_8') || envText('JUHE_REAL_HYBRID_PROJECT_MODEL_4_6') || 'glm-5.2'
const gpt54Model = envText('JUHE_REAL_HYBRID_PROJECT_MODEL_9') || 'gpt-5.4'
const gptModel = envText('JUHE_REAL_HYBRID_PROJECT_GPT_MODEL') || 'gpt-5.5'
const mainModel = envText('JUHE_REAL_HYBRID_PROJECT_MAIN_MODEL') || 'claude-opus-4-7'
const opusModel = envText('JUHE_REAL_HYBRID_PROJECT_OPUS_MODEL') || mainModel
const scoringUnitCost = numberEnv('JUHE_REAL_HYBRID_PROJECT_SCORING_UNIT_COST') ?? 0.002
const qualityInspectionEnabled = booleanEnv('JUHE_REAL_HYBRID_PROJECT_QUALITY_ENABLED') ?? false
const qualityScoringModel = envText('JUHE_REAL_HYBRID_PROJECT_QUALITY_MODEL') || scoringModel
const qualityInspectionMaxLevel = Math.min(10, Math.max(1, positiveIntegerEnv('JUHE_REAL_HYBRID_PROJECT_QUALITY_MAX_LEVEL') ?? 6))
const qualityInspectionMaxRetries = Math.min(2, Math.max(0, positiveIntegerEnv('JUHE_REAL_HYBRID_PROJECT_QUALITY_MAX_RETRIES') ?? 2))

const modelUnitCosts = new Map<string, number>([
  [flashModel, numberEnv('JUHE_REAL_HYBRID_PROJECT_COST_1_2') ?? modelCostDefault(flashModel)],
  [lowModel, numberEnv('JUHE_REAL_HYBRID_PROJECT_COST_1_3') ?? modelCostDefault(lowModel)],
  [glm51Model, numberEnv('JUHE_REAL_HYBRID_PROJECT_COST_5_6') ?? modelCostDefault(glm51Model)],
  [glmModel, numberEnv('JUHE_REAL_HYBRID_PROJECT_COST_4_6') ?? modelCostDefault(glmModel)],
  [gpt54Model, numberEnv('JUHE_REAL_HYBRID_PROJECT_COST_9') ?? modelCostDefault(gpt54Model)],
  [gptModel, numberEnv('JUHE_REAL_HYBRID_PROJECT_GPT_COST') ?? modelCostDefault(gptModel)],
  [mainModel, numberEnv('JUHE_REAL_HYBRID_PROJECT_MAIN_COST') ?? modelCostDefault(mainModel)],
  [opusModel, numberEnv('JUHE_REAL_HYBRID_PROJECT_OPUS_COST') ?? modelCostDefault(opusModel)],
  [qualityScoringModel, numberEnv('JUHE_REAL_HYBRID_PROJECT_QUALITY_COST') ?? modelCostDefault(qualityScoringModel)],
  [scoringModel, scoringUnitCost]
])

const levelRoutes: ApiKeyHybridRoutingConfig['levelRoutes'] = [
  { minLevel: 1, maxLevel: 2, targetModel: flashModel, enabled: true },
  { minLevel: 3, maxLevel: 4, targetModel: lowModel, enabled: true },
  { minLevel: 5, maxLevel: 6, targetModel: glm51Model, enabled: true },
  { minLevel: 7, maxLevel: 8, targetModel: glmModel, enabled: true },
  { minLevel: 9, maxLevel: 9, targetModel: gpt54Model, enabled: true },
  { minLevel: 10, maxLevel: 10, targetModel: gptModel, enabled: true }
]

const allowedOutputFiles = new Set([
  'src/data/operations/OperationsCenterData.ts',
  'src/pages/dashboard/pages/OperationsCenter.tsx',
  'src/App.tsx',
  'src/routes/DashboardRoutes.ts'
])

const tempRoot = resolve(tmpdir(), `juhe-ai-hybrid-project-workflow-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'hybrid-project-workflow.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'hybrid-project-workflow-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { handleOpenAIGatewayRequest },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue,
  hybridAffinity
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/gateway/hybrid/affinity.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '10mb' }), captureGatewayRawBody, async (req, res, next) => {
  try {
    await handleOpenAIGatewayRequest(req, res, { exposeUpstreamDiagnostics: true })
  } catch (error) {
    next(error)
  }
})

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()
  hybridAffinity.clearHybridRouteAffinityForTest()
  let appServer: http.Server | undefined
  try {
    registerWorkflowCustomModels()
    const scoring = createRealGroupAccount('Hybrid Project 评分分组', 'Hybrid Project 评分账户', scoringModel)
    const qualityScoring = qualityScoringModel === scoringModel
      ? scoring
      : createRealGroupAccount('Hybrid Project 质量评分分组', 'Hybrid Project 质量评分账户', qualityScoringModel)
    const flash = createRealGroupAccount('Hybrid Project Flash 分组', 'Hybrid Project Flash 账户', flashModel)
    const low = createRealGroupAccount('Hybrid Project Mini 分组', 'Hybrid Project Mini 账户', lowModel)
    const glm51 = createRealGroupAccount('Hybrid Project GLM 5.1 分组', 'Hybrid Project GLM 5.1 账户', glm51Model)
    const glm = createRealGroupAccount('Hybrid Project GLM 5.2 分组', 'Hybrid Project GLM 5.2 账户', glmModel)
    const gpt54 = createRealGroupAccount('Hybrid Project GPT 5.4 分组', 'Hybrid Project GPT 5.4 账户', gpt54Model)
    const gpt = createRealGroupAccount('Hybrid Project GPT 5.5 分组', 'Hybrid Project GPT 5.5 账户', gptModel)
    const hybridApiKey = repositories.createApiKeyRecord({
      name: 'Hybrid Project Workflow Key',
      routeMode: 'hybrid',
      groupRouteStrategy: 'priority_failover',
      groupBindings: uniqueGroupBindings([scoring, qualityScoring, flash, low, glm51, glm, gpt54, gpt]).map((item, index) => ({
        groupId: item.groupId,
        priority: index + 1,
        weight: 1,
        status: 'active'
      })),
      hybridRoutingConfig: {
        scoringModel,
        scoringContextMode: 'full_request',
        qualityPreference: 'balanced',
        scoringTimeoutMs: 45_000,
        scoringFallbackMaxLevel: 5,
        scoringCacheEnabled: true,
        scoringCacheTtlSeconds,
        cacheAffinityEnabled: true,
        affinityTtlSeconds: 900,
        switchMinLevelDelta: 2,
        downgradeConsecutiveLowCount: 2,
        qualityInspection: qualityInspectionEnabled ? {
          enabled: true,
          scoringModel: qualityScoringModel,
          triggerMode: 'risk_based',
          maxTriggerLevel: qualityInspectionMaxLevel,
          maxRetries: qualityInspectionMaxRetries,
          failureAction: 'repair_then_upgrade',
          unavailableAction: 'pass_through'
        } : undefined,
        levelRoutes
      } satisfies ApiKeyHybridRoutingConfig,
      status: 'active'
    }, access)
    assert(hybridApiKey.key, '项目工作流混合 API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`
    const tasks = workflowTasks().slice(0, taskLimit)
    const runs: WorkflowRunResult[] = []

    for (const strategy of selectedStrategies) {
      if (runs.length && requestIntervalMs > 0) {
        await wait(requestIntervalMs)
      }
      runs.push(await runWorkflow({
        baseUrl,
        hybridApiKey: hybridApiKey.key,
        strategy,
        tasks
      }))
      writeSummary({
        hybridApiKeyId: hybridApiKey.id,
        runs,
        tasks
      })
      accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
      gatewayCache.clearGatewayRuntimeCache()
    }

    usageRecordQueue.flushAllUsageRecordQueue()
    const summary = buildSummary({
      hybridApiKeyId: hybridApiKey.id,
      runs,
      tasks
    })
    console.log(JSON.stringify(summary, null, 2))
  } finally {
    await closeServer(appServer)
  }
} finally {
  hybridRouteDiagnosticsChannel.unsubscribe(hybridRouteDiagnosticsSubscriber)
  hybridAffinity.clearHybridRouteAffinityForTest()
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function registerWorkflowCustomModels(): void {
  for (const model of new Set([scoringModel, qualityScoringModel, flashModel, lowModel, glm51Model, glmModel, gpt54Model, gptModel, mainModel, opusModel])) {
    saveWorkflowCustomModel(OPENAI_COMPATIBLE_PROVIDER_CODE, model, modelUnitCosts.get(model) ?? modelCostDefault(model))
  }
}

function saveWorkflowCustomModel(providerCode: ProviderCode, model: string, unitCost: number): void {
  saveCustomProviderModel({
    providerCode,
    model,
    scope: 'personal',
    systemAccountId: access.systemAccountId,
    status: 'active',
    supportedApiProtocols: ['chat_completions'],
    inputUsdPer1M: unitCost,
    outputUsdPer1M: unitCost,
    cachedInputUsdPer1M: unitCost / 10,
    actorSystemAccountId: access.systemAccountId
  })
}

function createRealGroupAccount(groupName: string, accountName: string, supportedModel: string): { accountId: string; groupId: string } {
  const group = repositories.createGroup({
    name: groupName,
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    name: accountName,
    type: 'api_key',
    clientCompatibility: 'openai_standard',
    credentials: {
      api_key: realApiKey,
      base_url: realBaseUrl,
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    concurrencyLimit: 1,
    supportedModels: [supportedModel]
  }, access)
  assert.deepEqual(account.supportedModels, [supportedModel])
  return { accountId: account.id, groupId: group.id }
}

function uniqueGroupBindings(items: Array<{ accountId: string; groupId: string }>): Array<{ accountId: string; groupId: string }> {
  const seen = new Set<string>()
  return items.filter((item) => {
    if (seen.has(item.groupId)) return false
    seen.add(item.groupId)
    return true
  })
}

async function runWorkflow(input: {
  baseUrl: string
  hybridApiKey: string
  strategy: WorkflowStrategyName
  tasks: WorkflowTask[]
}): Promise<WorkflowRunResult> {
  const startedAt = Date.now()
  const directCallCounts: Record<string, number> = {}
  const worktree = join(tempRoot, `repo-${input.strategy}`)
  cloneRepository(worktree)
  const projectContext = readProjectContext(worktree)
  logProgress(input.strategy, 'planning', 'start')
  const plan = await callForStrategy({
    baseUrl: input.baseUrl,
    directCallCounts,
    hybridApiKey: input.hybridApiKey,
    messages: planningMessages(projectContext, input.tasks),
    role: 'planner',
    sessionId: `${input.strategy}-plan`,
    strategy: input.strategy
  })
  logProgress(input.strategy, 'planning', plan.ok ? 'ok' : `failed:${plan.status}`)
  await wait(requestIntervalMs)

  const taskResults: TaskRunResult[] = []
  for (const [index, task] of input.tasks.entries()) {
    logProgress(input.strategy, task.id, 'start')
    const execution = await callForStrategy({
      baseUrl: input.baseUrl,
      directCallCounts,
      hybridApiKey: input.hybridApiKey,
      messages: executionMessages({
        context: readProjectContext(worktree, task.id),
        plan: plan.content,
        task,
        tasksDone: input.tasks.slice(0, index),
        tasksRequired: input.tasks.slice(0, index + 1)
      }),
      role: 'executor',
      sessionId: `${input.strategy}-task-${task.id}`,
      strategy: input.strategy
    })
    let appliedFiles = applyFilesFromAgent(worktree, execution.content)
    let test = runWorkflowValidation(worktree, input.tasks.slice(0, index + 1))
    logProgress(input.strategy, task.id, test.ok ? `ok files=${appliedFiles.join(',')}` : 'validation_failed')
    let repair: AgentCallResult | undefined
    let repairAppliedFiles: string[] | undefined
    if (!test.ok) {
      await wait(requestIntervalMs)
      logProgress(input.strategy, `${task.id}:repair`, 'start')
      repair = await callForStrategy({
        baseUrl: input.baseUrl,
        directCallCounts,
        hybridApiKey: input.hybridApiKey,
        messages: repairMessages({
          context: readProjectContext(worktree, task.id),
          failedOutput: test.output,
          plan: plan.content,
          task,
          tasksRequired: input.tasks.slice(0, index + 1)
        }),
        role: 'executor',
        sessionId: `${input.strategy}-repair-${task.id}`,
        strategy: input.strategy
      })
      repairAppliedFiles = applyFilesFromAgent(worktree, repair.content)
      appliedFiles = [...new Set([...appliedFiles, ...repairAppliedFiles])]
      test = runWorkflowValidation(worktree, input.tasks.slice(0, index + 1))
      logProgress(input.strategy, `${task.id}:repair`, test.ok ? `ok files=${repairAppliedFiles.join(',')}` : 'validation_failed')
    }
    taskResults.push({
      appliedFiles,
      execution,
      id: task.id,
      repair,
      repairAppliedFiles,
      test,
      title: task.title
    })
    await wait(requestIntervalMs)
  }

  const finalTest = runWorkflowValidation(worktree, input.tasks)
  logProgress(input.strategy, 'final-review', 'start')
  const finalReview = await callForStrategy({
    baseUrl: input.baseUrl,
    directCallCounts,
    hybridApiKey: input.hybridApiKey,
    messages: reviewMessages({
      context: readProjectContext(worktree),
      tasks: input.tasks,
      testOutput: finalTest.output
    }),
    role: 'reviewer',
    sessionId: `${input.strategy}-review`,
    strategy: input.strategy
  })
  logProgress(input.strategy, 'final-review', finalReview.ok ? 'ok' : `failed:${finalReview.status}`)

  return {
    directCallCounts,
    durationMs: Date.now() - startedAt,
    finalReview,
    finalTest,
    ok: finalTest.ok,
    plan,
    strategy: input.strategy,
    tasks: taskResults
  }
}

function cloneRepository(targetPath: string): void {
  if (repoUrl === 'local:generated') {
    createGeneratedWorkflowProject(targetPath)
    return
  }
  let lastOutput = ''
  for (let attempt = 1; attempt <= repositoryCloneRetries + 1; attempt += 1) {
    const result = spawnSync('git', ['clone', '--depth', '1', repoUrl, targetPath], {
      encoding: 'utf8',
      timeout: 180_000
    })
    if (result.status === 0) return
    lastOutput = `${result.stdout}\n${result.stderr}`
    if (attempt <= repositoryCloneRetries) {
      rmSync(targetPath, { recursive: true, force: true })
      continue
    }
  }
  if (allowGeneratedFixture) {
    createGeneratedWorkflowProject(targetPath)
    return
  }
  throw new Error(`克隆 GitHub 项目失败：${sanitizeErrorSnippet(lastOutput)}`)
}

function createGeneratedWorkflowProject(targetPath: string): void {
  rmSync(targetPath, { recursive: true, force: true })
  mkdirSync(targetPath, { recursive: true })
  writeFixtureFile(targetPath, 'package.json', JSON.stringify({
    scripts: { build: 'vite build' },
    dependencies: {
      '@vitejs/plugin-react': '^latest',
      vite: '^latest',
      typescript: '^latest',
      react: '^latest',
      'react-dom': '^latest',
      'react-bootstrap': '^latest'
    },
    devDependencies: {}
  }, null, 2))
  writeFixtureFile(targetPath, 'src/types.ts', [
    'export type DashboardStatus = "active" | "paused" | "warning";',
    'export interface DashboardMetric {',
    '  id: string;',
    '  label: string;',
    '  value: number;',
    '  status: DashboardStatus;',
    '}',
    '',
    'export interface DashboardRouteItem {',
    '  title: string;',
    '  link: string;',
    '  children?: DashboardRouteItem[];',
    '}'
  ].join('\n'))
  writeFixtureFile(targetPath, 'src/data/dashboard/ProjectsStatsData.tsx', [
    'import type { DashboardMetric } from "../../types";',
    '',
    'export const projectsStats: DashboardMetric[] = [',
    '  { id: "revenue", label: "Revenue", value: 128, status: "active" },',
    '  { id: "tickets", label: "Tickets", value: 42, status: "warning" },',
    '  { id: "deployments", label: "Deployments", value: 17, status: "active" }',
    '];'
  ].join('\n'))
  writeFixtureFile(targetPath, 'src/pages/dashboard/Index.tsx', [
    'import { Card, Col, Container, Row } from "react-bootstrap";',
    'import { projectsStats } from "../../data/dashboard/ProjectsStatsData";',
    '',
    'export default function DashboardIndex() {',
    '  return (',
    '    <Container fluid>',
    '      <Row>',
    '        {projectsStats.map((item) => (',
    '          <Col md={4} key={item.id}>',
    '            <Card><Card.Body><Card.Title>{item.label}</Card.Title><strong>{item.value}</strong></Card.Body></Card>',
    '          </Col>',
    '        ))}',
    '      </Row>',
    '    </Container>',
    '  );',
    '}'
  ].join('\n'))
  writeFixtureFile(targetPath, 'src/pages/dashboard/pages/Settings.tsx', [
    'import { Card, Container } from "react-bootstrap";',
    '',
    'export default function Settings() {',
    '  return <Container fluid><Card><Card.Body>Settings</Card.Body></Card></Container>;',
    '}'
  ].join('\n'))
  writeFixtureFile(targetPath, 'src/App.tsx', [
    'import DashboardIndex from "./pages/dashboard/Index";',
    'import Settings from "./pages/dashboard/pages/Settings";',
    '',
    'const routes = [',
    '  { path: "/", element: <DashboardIndex /> },',
    '  { path: "/pages/settings", element: <Settings /> },',
    '  { path: "/pages/profile", element: <Settings /> },',
    '  { path: "/pages/billing", element: <Settings /> },',
    '  { path: "/pages/pricing", element: <Settings /> },',
    '  { path: "/pages/api-demo", element: <Settings /> }',
    '];',
    '',
    'export default routes;'
  ].join('\n'))
  writeFixtureFile(targetPath, 'src/routes/DashboardRoutes.ts', [
    'import type { DashboardRouteItem } from "../types";',
    '',
    'const DashboardRoutes: DashboardRouteItem[] = [',
    '  { title: "Dashboard", link: "/" },',
    '  {',
    '    title: "Pages",',
    '    link: "/pages",',
    '    children: [',
    '      { title: "Profile", link: "/pages/profile" },',
    '      { title: "Settings", link: "/pages/settings" },',
    '      { title: "Billing", link: "/pages/billing" },',
    '      { title: "Pricing", link: "/pages/pricing" },',
    '      { title: "Api Demo", link: "/pages/api-demo" },',
    '      { title: "404 Error", link: "/pages/404" }',
    '    ]',
    '  }',
    '];',
    '',
    'export default DashboardRoutes;'
  ].join('\n'))
  for (let index = 0; index < 180; index += 1) {
    writeFixtureFile(targetPath, `src/generated/GeneratedPanel${String(index).padStart(3, '0')}.tsx`, generatedPanelSource(index))
  }
}

function writeFixtureFile(worktree: string, relativePath: string, content: string): void {
  const target = join(worktree, relativePath)
  mkdirSync(dirname(target), { recursive: true })
  writeFileSync(target, `${content.trim()}\n`, 'utf8')
}

function generatedPanelSource(index: number): string {
  const rows = Array.from({ length: 64 }, (_, row) =>
    `  { id: "panel-${index}-${row}", label: "Metric ${index}-${row}", value: ${index * 100 + row}, trend: ${row % 2 === 0 ? '"up"' : '"down"'} }`
  )
  return [
    `export const generatedPanel${index}Rows = [`,
    rows.join(',\n'),
    '];',
    '',
    `export function generatedPanel${index}Summary() {`,
    `  return generatedPanel${index}Rows.reduce((sum, item) => sum + item.value, 0);`,
    '}'
  ].join('\n')
}

async function callForStrategy(input: {
  baseUrl: string
  directCallCounts: Record<string, number>
  hybridApiKey: string
  messages: Array<{ role: string; content: string }>
  role: WorkflowRole
  sessionId: string
  strategy: WorkflowStrategyName
}): Promise<AgentCallResult> {
  const model = modelForStrategyRole(input.strategy, input.role)
  if (model === 'hybrid-client-router') {
    return callGatewayCompletion(input.baseUrl, input.hybridApiKey, model, input.messages, input.sessionId)
  }
  input.directCallCounts[model] = (input.directCallCounts[model] ?? 0) + 1
  return callUpstreamChatCompletion(model, input.messages)
}

function modelForStrategyRole(strategy: WorkflowStrategyName, role: WorkflowRole): string {
  if (strategy === 'full_hybrid') {
    return 'hybrid-client-router'
  }
  if (strategy === 'hybrid_main_agent') {
    return role === 'executor' ? 'hybrid-client-router' : mainModel
  }
  if (strategy === 'fixed_glm52') return glmModel
  if (strategy === 'fixed_opus') return opusModel
  return gptModel
}

async function callGatewayCompletion(
  baseUrl: string,
  localApiKey: string,
  model: string,
  messages: Array<{ role: string; content: string }>,
  sessionId: string
): Promise<AgentCallResult> {
  const clientRequestId = `${sessionId}-${Date.now()}-${Math.random().toString(16).slice(2)}`
  return callWithRetries(() => callGatewayCompletionOnce(baseUrl, localApiKey, model, messages, sessionId, clientRequestId))
}

async function callGatewayCompletionOnce(
  baseUrl: string,
  localApiKey: string,
  model: string,
  messages: Array<{ role: string; content: string }>,
  sessionId: string,
  clientRequestId: string
): Promise<AgentCallResult> {
  const startedAt = Date.now()
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), requestTimeoutMs)
  timer.unref()
  try {
    const response = await fetch(`${baseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${localApiKey}`,
        'content-type': 'application/json',
        'x-session-id': sessionId,
        'x-client-request-id': clientRequestId
      },
      body: JSON.stringify({
        model,
        messages,
        stream: false,
        max_tokens: outputMaxTokens,
        temperature: 0.1
      }),
      signal: controller.signal
    })
    const text = await response.text()
    const body = safeJsonObject(text)
    const content = firstAssistantContent(body)
    return {
      content: content ?? '',
      durationMs: Date.now() - startedAt,
      error: response.ok && content ? undefined : sanitizeErrorSnippet(text),
      model: typeof body.model === 'string' ? body.model : undefined,
      ok: response.ok && Boolean(content),
      status: response.status
    }
  } catch (error) {
    return {
      content: '',
      durationMs: Date.now() - startedAt,
      error: sanitizeErrorSnippet(error instanceof Error ? error.message : String(error)),
      ok: false,
      status: 0
    }
  } finally {
    clearTimeout(timer)
  }
}

async function callUpstreamChatCompletion(
  model: string,
  messages: Array<{ role: string; content: string }>
): Promise<AgentCallResult> {
  return callWithRetries(() => callUpstreamChatCompletionOnce(model, messages))
}

async function callUpstreamChatCompletionOnce(
  model: string,
  messages: Array<{ role: string; content: string }>
): Promise<AgentCallResult> {
  const startedAt = Date.now()
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), requestTimeoutMs)
  timer.unref()
  try {
    const response = await fetch(chatCompletionsUrl(realBaseUrl), {
      method: 'POST',
      headers: {
        authorization: `Bearer ${realApiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model,
        messages,
        stream: false,
        max_tokens: outputMaxTokens,
        temperature: 0.1
      }),
      signal: controller.signal
    })
    const text = await response.text()
    const body = safeJsonObject(text)
    const content = firstAssistantContent(body)
    return {
      content: content ?? '',
      durationMs: Date.now() - startedAt,
      error: response.ok && content ? undefined : sanitizeErrorSnippet(text),
      model: typeof body.model === 'string' ? body.model : undefined,
      ok: response.ok && Boolean(content),
      status: response.status
    }
  } catch (error) {
    return {
      content: '',
      durationMs: Date.now() - startedAt,
      error: sanitizeErrorSnippet(error instanceof Error ? error.message : String(error)),
      ok: false,
      status: 0
    }
  } finally {
    clearTimeout(timer)
  }
}

async function callWithRetries(callOnce: () => Promise<AgentCallResult>): Promise<AgentCallResult> {
  const startedAt = Date.now()
  const retryErrors: string[] = []
  let lastResult: AgentCallResult | undefined
  for (let attempt = 1; attempt <= upstreamRetryCount + 1; attempt += 1) {
    const result = await callOnce()
    lastResult = result
    if (result.ok || !isRetryableCallResult(result) || attempt > upstreamRetryCount) {
      return {
        ...result,
        attempts: attempt,
        durationMs: Date.now() - startedAt,
        retryErrors: retryErrors.length ? retryErrors : undefined
      }
    }
    retryErrors.push(callRetrySummary(result))
    await wait(upstreamRetryDelayMs)
  }
  return {
    ...(lastResult ?? {
      content: '',
      error: 'retry loop exited without result',
      ok: false,
      status: 0
    }),
    attempts: upstreamRetryCount + 1,
    durationMs: Date.now() - startedAt,
    retryErrors: retryErrors.length ? retryErrors : undefined
  }
}

function isRetryableCallResult(result: AgentCallResult): boolean {
  if (result.ok) return false
  if ([0, 401, 408, 409, 425, 429, 500, 502, 503, 504, 520, 522, 524].includes(result.status)) {
    return true
  }
  const error = result.error?.toLowerCase() ?? ''
  return error.includes('aborted') ||
    error.includes('timeout') ||
    error.includes('timed out') ||
    error.includes('auth_unavailable') ||
    error.includes('invalid authentication credentials')
}

function callRetrySummary(result: AgentCallResult): string {
  const error = result.error ? result.error.replace(/\s+/g, ' ').slice(0, 180) : 'empty error'
  return `status=${result.status} ${error}`
}

function planningMessages(context: string, tasks: WorkflowTask[]): Array<{ role: string; content: string }> {
  return [
    {
      role: 'system',
      content: '你是主 Agent，负责在真实前端后台项目里规划功能拆解和质量把关。输出简洁中文计划，不要写代码。'
    },
    {
      role: 'user',
      content: [
        `项目：${repoUrl === 'local:generated' ? '本地生成 React + Vite + TypeScript 后台项目夹具' : 'codescandy/dash-ui-react-vitejs-typescript，React + Vite + TypeScript 后台模板'}，${projectProfile}。`,
        '目标：连续完成“运营中心”功能，保持现有 Dashboard、Pages 和 Bootstrap 风格。',
        '',
        '项目关键上下文：',
        context,
        '',
        '需求列表：',
        ...tasks.map((task, index) => `${index + 1}. ${task.title}：${task.instructions}`),
        '',
        '请给执行 Agent 一个分步计划，重点说明要改哪些文件、如何避免破坏现有路由和菜单。'
      ].join('\n')
    }
  ]
}

function executionMessages(input: {
  context: string
  plan: string
  task: WorkflowTask
  tasksDone: WorkflowTask[]
  tasksRequired: WorkflowTask[]
}): Array<{ role: string; content: string }> {
  return [
    {
      role: 'system',
      content: [
        '你是执行 Agent，只负责输出需要写入项目的完整文件。',
        '允许新增或修改这些文件：',
        'src/data/operations/OperationsCenterData.ts',
        'src/pages/dashboard/pages/OperationsCenter.tsx',
        'src/App.tsx',
        'src/routes/DashboardRoutes.ts',
        '不要输出 Markdown 或解释。',
        '每个文件必须使用如下格式：',
        'BEGIN_FILE:src/path/File.tsx',
        '<完整文件内容>',
        'END_FILE'
      ].join('\n')
    },
    {
      role: 'user',
      content: [
        '主 Agent 计划：',
        input.plan.slice(0, 3_000),
        '',
        `当前要完成：${input.task.title}`,
        input.task.instructions,
        '',
        input.tasksDone.length ? `已经完成的需求：${input.tasksDone.map((item) => item.title).join('、')}` : '已经完成的需求：无',
        '',
        '项目当前关键上下文：',
        input.context,
        '',
        '本轮保存后必须通过这些累计验收：',
        validationChecklist(input.tasksRequired)
      ].join('\n')
    }
  ]
}

function repairMessages(input: {
  context: string
  failedOutput: string
  plan: string
  task: WorkflowTask
  tasksRequired: WorkflowTask[]
}): Array<{ role: string; content: string }> {
  return [
    {
      role: 'system',
      content: [
        '你是修复 Agent。验收失败了，请只输出需要修复的完整文件。',
        '格式仍必须是 BEGIN_FILE:src/path 到 END_FILE；不要输出 Markdown 或解释。'
      ].join('\n')
    },
    {
      role: 'user',
      content: [
        '主 Agent 计划：',
        input.plan.slice(0, 2_000),
        '',
        `失败任务：${input.task.title}`,
        input.task.instructions,
        '',
        '失败输出：',
        input.failedOutput.slice(0, 4_000),
        '',
        '当前项目上下文：',
        input.context,
        '',
        '必须通过的累计验收：',
        validationChecklist(input.tasksRequired)
      ].join('\n')
    }
  ]
}

function reviewMessages(input: {
  context: string
  tasks: WorkflowTask[]
  testOutput: string
}): Array<{ role: string; content: string }> {
  return [
    {
      role: 'system',
      content: '你是质量把关 Agent。请审查最终代码和验收结果，输出 JSON：{"approved":true,"risks":[],"summary":""}。'
    },
    {
      role: 'user',
      content: [
        '最终需求：',
        ...input.tasks.map((task, index) => `${index + 1}. ${task.title}：${task.instructions}`),
        '',
        '最终验收输出：',
        input.testOutput.slice(0, 3_000),
        '',
        '最终项目上下文：',
        input.context
      ].join('\n')
    }
  ]
}

function applyFilesFromAgent(worktree: string, content: string): string[] {
  const applied: string[] = []
  const pattern = /BEGIN_FILE:([^\n\r]+)\s*([\s\S]*?)\s*END_FILE/g
  let match: RegExpExecArray | null
  while ((match = pattern.exec(content)) !== null) {
    const relativePath = normalizeOutputPath(match[1] ?? '')
    if (!allowedOutputFiles.has(relativePath)) continue
    const target = resolve(worktree, relativePath)
    if (!target.startsWith(resolve(worktree))) continue
    mkdirSync(dirname(target), { recursive: true })
    writeFileSync(target, normalizeNewline(match[2] ?? ''), 'utf8')
    applied.push(relativePath)
  }
  return [...new Set(applied)]
}

function normalizeOutputPath(value: string): string {
  return normalize(value.trim().replace(/^["'`]+|["'`]+$/g, '')).replace(/\\/g, '/')
}

function readProjectContext(worktree: string, focus?: string): string {
  const files = contextFilesForFocus(focus)
  return files.map((file) => [
    `--- ${file}`,
    readFileIfExists(worktree, file).slice(0, file.endsWith('App.tsx') ? 5_000 : 2_200)
  ].join('\n')).join('\n\n')
}

function contextFilesForFocus(focus?: string): string[] {
  if (focus === 'operations_data') {
    return [
      'src/data/dashboard/ProjectsStatsData.tsx',
      'src/types.ts'
    ]
  }
  if (focus === 'operations_page') {
    return [
      'src/data/operations/OperationsCenterData.ts',
      'src/pages/dashboard/Index.tsx',
      'src/pages/dashboard/pages/Settings.tsx',
      'src/types.ts'
    ]
  }
  if (focus === 'operations_route') {
    return [
      'src/App.tsx',
      'src/pages/dashboard/pages/OperationsCenter.tsx'
    ]
  }
  if (focus === 'operations_menu') {
    return [
      'src/routes/DashboardRoutes.ts'
    ]
  }
  return [
    'src/App.tsx',
    'src/routes/DashboardRoutes.ts',
    'src/pages/dashboard/Index.tsx',
    'src/pages/dashboard/pages/Settings.tsx',
    'src/types.ts'
  ]
}

function readFileIfExists(worktree: string, relativePath: string): string {
  const target = join(worktree, relativePath)
  return existsSync(target) ? readFileSync(target, 'utf8') : '[file_missing]'
}

function runWorkflowValidation(worktree: string, tasks: WorkflowTask[]): ValidationResult {
  const errors: string[] = []
  const enabled = new Set(tasks.map((task) => task.id))
  if (enabled.has('operations_data')) {
    validateOperationsData(worktree, errors)
  }
  if (enabled.has('operations_page')) {
    validateOperationsPage(worktree, errors)
  }
  if (enabled.has('operations_route')) {
    validateOperationsRoute(worktree, errors)
  }
  if (enabled.has('operations_menu')) {
    validateOperationsMenu(worktree, errors)
  }
  if (runBuildValidation && enabled.has('operations_menu')) {
    const build = runBuild(worktree)
    if (!build.ok) {
      errors.push(`build failed:\n${build.output}`)
    }
  }
  return {
    exitCode: errors.length ? 1 : 0,
    output: errors.length ? errors.join('\n') : 'workflow validation passed',
    ok: errors.length === 0
  }
}

function validateOperationsData(worktree: string, errors: string[]): void {
  const file = readFileIfExists(worktree, 'src/data/operations/OperationsCenterData.ts')
  if (file === '[file_missing]') {
    errors.push('缺少 src/data/operations/OperationsCenterData.ts')
    return
  }
  assertSource(file, /operationsIncidents/, '数据模块必须导出 operationsIncidents', errors)
  assertSource(file, /getOperationsSummary/, '数据模块必须提供 getOperationsSummary', errors)
  assertSource(file, /critical/, '数据模块必须包含 critical 严重级别', errors)
  assertSource(file, /investigating/, '数据模块必须包含 investigating 状态', errors)
  assertSource(file, /owner|service/, '数据模块必须包含 owner 或 service 字段', errors)
  const itemCount = (file.match(/id:/g) ?? []).length
  if (itemCount < 6) {
    errors.push(`运营事件样本至少 6 条，实际疑似 ${itemCount} 条`)
  }
}

function validateOperationsPage(worktree: string, errors: string[]): void {
  const file = readFileIfExists(worktree, 'src/pages/dashboard/pages/OperationsCenter.tsx')
  if (file === '[file_missing]') {
    errors.push('缺少 src/pages/dashboard/pages/OperationsCenter.tsx')
    return
  }
  assertSource(file, /operationsIncidents|getOperationsSummary/, '页面必须使用运营数据模块', errors)
  assertSource(file, /useMemo/, '页面必须使用 useMemo 派生筛选或汇总', errors)
  assertSource(file, /useState/, '页面必须有筛选状态', errors)
  assertSource(file, /Form\.(Control|Select)|<Form/, '页面必须提供筛选控件', errors)
  assertSource(file, /Table/, '页面必须展示运营事件表格', errors)
  assertSource(file, /Badge/, '页面必须用 Badge 展示状态或等级', errors)
  assertSource(file, /statusFilter|severityFilter|search/i, '页面必须包含状态、严重级别或搜索筛选变量', errors)
  assertSource(file, /SLA|MTTR|Open|Resolved|Critical/i, '页面必须包含运营指标摘要', errors)
}

function validateOperationsRoute(worktree: string, errors: string[]): void {
  const app = readFileIfExists(worktree, 'src/App.tsx')
  assertSource(app, /OperationsCenter/, 'App.tsx 必须导入并使用 OperationsCenter', errors)
  assertSource(app, /path:\s*["']operations["']/, 'App.tsx 必须注册 /pages/operations 子路由', errors)
}

function validateOperationsMenu(worktree: string, errors: string[]): void {
  const routes = readFileIfExists(worktree, 'src/routes/DashboardRoutes.ts')
  assertSource(routes, /Operations Center|Operations/, 'DashboardRoutes.ts 必须加入 Operations 菜单项', errors)
  assertSource(routes, /\/pages\/operations/, 'DashboardRoutes.ts 菜单必须指向 /pages/operations', errors)
}

function runBuild(worktree: string): ValidationResult {
  const install = spawnSync('npm', ['ci', '--ignore-scripts'], {
    cwd: worktree,
    encoding: 'utf8',
    timeout: 480_000
  })
  if (install.status !== 0) {
    return {
      exitCode: typeof install.status === 'number' ? install.status : 1,
      output: sanitizeErrorSnippet(`${install.stdout}\n${install.stderr}`),
      ok: false
    }
  }
  const build = spawnSync('npm', ['run', 'build'], {
    cwd: worktree,
    encoding: 'utf8',
    timeout: 480_000
  })
  return {
    exitCode: typeof build.status === 'number' ? build.status : 1,
    output: sanitizeErrorSnippet(`${build.stdout}\n${build.stderr}`),
    ok: build.status === 0
  }
}

function assertSource(source: string, pattern: RegExp, message: string, errors: string[]): void {
  if (!pattern.test(source)) {
    errors.push(message)
  }
}

function validationChecklist(tasks: WorkflowTask[]): string {
  return tasks.map((task, index) => [
    `${index + 1}. ${task.title}`,
    task.instructions,
    `期望文件：${task.expectedFiles.join('、')}`
  ].join('\n')).join('\n\n')
}

function buildSummary(input: {
  hybridApiKeyId: string
  runs: WorkflowRunResult[]
  tasks: WorkflowTask[]
}): Record<string, unknown> {
  const hybridUsageCounts = usageCountsForApiKey(input.hybridApiKeyId)
  return {
    ok: input.runs.every((run) => run.ok),
    baseUrl: sanitizeBaseUrl(realBaseUrl),
    repoUrl,
    repoProfile: projectProfile,
    runBuildValidation,
    retryPolicy: {
      retryableFailuresAreStabilityNoise: true,
      maxRetries: upstreamRetryCount,
      maxAttempts: upstreamRetryCount + 1,
      retryDelayMs: upstreamRetryDelayMs,
      requestIntervalMs
    },
    tasks: input.tasks.map((task) => ({ id: task.id, title: task.title, expectedFiles: task.expectedFiles })),
    strategies: input.runs.map((run) => ({
      strategy: run.strategy,
      ok: run.ok,
      durationMs: run.durationMs,
      directCallCounts: run.directCallCounts,
      finalTest: {
        ok: run.finalTest.ok,
        exitCode: run.finalTest.exitCode,
        output: run.finalTest.output.slice(0, 1_500)
      },
      plan: callSummary(run.plan),
      finalReview: callSummary(run.finalReview),
      tasks: run.tasks.map((task) => ({
        id: task.id,
        title: task.title,
        appliedFiles: task.appliedFiles,
        execution: callSummary(task.execution),
        repair: task.repair ? callSummary(task.repair) : undefined,
        repairAppliedFiles: task.repairAppliedFiles,
        routeEvents: routeEventsForTask(run.strategy, task.id),
        test: {
          ok: task.test.ok,
          exitCode: task.test.exitCode,
          output: task.test.output.slice(0, 1_200)
        }
      })),
      estimatedCost: estimateWorkflowCost(run, hybridUsageCounts)
    })),
    hybridUsageCounts,
    hybridRouteEvents: hybridRouteEvents.map(summarizeHybridRouteEvent),
    hybridScoringStats: summarizeHybridScoringStats(hybridRouteEvents),
    routeModels: levelRoutes.map((route) => `${route.minLevel}-${route.maxLevel}:${route.targetModel}`)
  }
}

function writeSummary(input: {
  hybridApiKeyId: string
  runs: WorkflowRunResult[]
  tasks: WorkflowTask[]
}): void {
  if (!outputPath) return
  writeFileSync(outputPath, `${JSON.stringify(buildSummary(input), null, 2)}\n`, 'utf8')
}

function routeEventsForTask(strategy: WorkflowStrategyName, taskId: string): Record<string, unknown>[] {
  return [
    ...hybridRouteEvents.filter((event) => event.sessionId === `${strategy}-task-${taskId}`),
    ...hybridRouteEvents.filter((event) => event.sessionId === `${strategy}-repair-${taskId}`)
  ].map(summarizeHybridRouteEvent)
}

function hybridRouteEventsForStrategy(strategy: WorkflowStrategyName): HybridRouteEvent[] {
  return hybridRouteEvents.filter((event) => event.sessionId?.startsWith(`${strategy}-`))
}

function summarizeHybridRouteEvent(event: HybridRouteEvent): Record<string, unknown> {
  return {
    affinityApplied: event.affinityApplied,
    affinityReason: event.affinityReason,
    confidence: event.confidence,
    level: event.level,
    levelRange: event.levelRange,
    outcome: event.outcome,
    scoringCacheHit: event.scoringCacheHit,
    scoringDefaulted: event.scoringDefaulted,
    scoringErrorCode: event.scoringErrorCode,
    scoringErrorMessage: event.scoringErrorMessage,
    scoringReason: event.scoringReason,
    sessionId: event.sessionId,
    targetModel: event.targetModel
  }
}

function summarizeHybridScoringStats(events: HybridRouteEvent[]): Record<string, unknown> {
  const selectedEvents = events.filter((event) => event.outcome === 'selected')
  return {
    count: selectedEvents.length,
    averageLevel: selectedEvents.length
      ? roundMoney(selectedEvents.reduce((sum, event) => sum + (event.level ?? 0), 0) / selectedEvents.length)
      : 0,
    levelCounts: countBy(selectedEvents.map((event) => String(event.level ?? 'unknown'))),
    targetModelCounts: countBy(selectedEvents.map((event) => event.targetModel ?? 'unknown')),
    defaultedCount: selectedEvents.filter((event) => event.scoringDefaulted).length,
    cacheHitCount: selectedEvents.filter((event) => event.scoringCacheHit).length,
    affinityAppliedCount: selectedEvents.filter((event) => event.affinityApplied).length
  }
}

function countBy(values: string[]): Record<string, number> {
  const counts: Record<string, number> = {}
  for (const value of values) {
    counts[value] = (counts[value] ?? 0) + 1
  }
  return counts
}

function callSummary(call: AgentCallResult): Record<string, unknown> {
  return {
    attempts: call.attempts,
    durationMs: call.durationMs,
    error: call.error,
    model: call.model,
    ok: call.ok,
    retryErrors: call.retryErrors,
    status: call.status
  }
}

function usageCountsForApiKey(apiKeyId: string): UsageCountRow[] {
  return databaseModule.getUsageCatalogDatabase()
    .prepare(`
      SELECT traffic_source, model, success, COUNT(*) AS count, SUM(cost_usd) AS cost_usd
      FROM usage_record_shard_entries
      WHERE api_key_id = ?
      GROUP BY traffic_source, model, success
      ORDER BY traffic_source ASC, model ASC, success ASC
    `)
    .all(apiKeyId) as unknown as UsageCountRow[]
}

function estimateWorkflowCost(run: WorkflowRunResult, _hybridUsageCounts: UsageCountRow[]): Record<string, number> {
  const directCost = Object.entries(run.directCallCounts)
    .reduce((sum, [model, count]) => sum + count * (modelUnitCosts.get(model) ?? modelCostDefault(model)), 0)
  if (run.strategy !== 'hybrid_main_agent' && run.strategy !== 'full_hybrid') {
    return {
      directCost: roundMoney(directCost),
      estimatedTotal: roundMoney(directCost)
    }
  }
  const selectedEvents = hybridRouteEventsForStrategy(run.strategy).filter((event) => event.outcome === 'selected')
  const scoringCount = selectedEvents.filter((event) => !event.scoringCacheHit).length
  const targetCost = selectedEvents
    .reduce((sum, event) => sum + (modelUnitCosts.get(event.targetModel ?? '') ?? modelCostDefault(event.targetModel ?? '')), 0)
  const scoringCost = scoringCount * scoringUnitCost
  return {
    directCost: roundMoney(directCost),
    scoringCount,
    scoringCost: roundMoney(scoringCost),
    targetCost: roundMoney(targetCost),
    estimatedTotal: roundMoney(directCost + scoringCost + targetCost)
  }
}

function workflowStrategies(): WorkflowStrategyName[] {
  const configured = envText('JUHE_REAL_HYBRID_PROJECT_STRATEGIES')
  const values = configured
    ? configured.split(',').map((item) => item.trim()).filter(Boolean)
    : ['full_hybrid', 'hybrid_main_agent', 'fixed_gpt55']
  const allowed = new Set<WorkflowStrategyName>(['full_hybrid', 'hybrid_main_agent', 'fixed_gpt55', 'fixed_glm52', 'fixed_opus'])
  return values.filter((item): item is WorkflowStrategyName => allowed.has(item as WorkflowStrategyName))
}

function workflowTasks(): WorkflowTask[] {
  return [
    {
      id: 'operations_data',
      title: '新增运营中心数据模块',
      instructions: '新增 src/data/operations/OperationsCenterData.ts。定义 OperationsIncident 类型、operationsIncidents 样本数组和 getOperationsSummary(events) 汇总函数。样本至少 6 条，包含 service、owner、status、severity、slaMinutes、durationMinutes、createdAt 等字段，状态至少覆盖 open、investigating、resolved，严重级别至少覆盖 critical、high、medium。',
      expectedFiles: ['src/data/operations/OperationsCenterData.ts']
    },
    {
      id: 'operations_page',
      title: '新增运营中心页面',
      instructions: '新增 src/pages/dashboard/pages/OperationsCenter.tsx。页面使用 react-bootstrap 的 Container、Row、Col、Card、Form、Table、Badge 等组件，展示运营事件摘要卡片、搜索框、状态筛选、严重级别筛选和事件表格。筛选逻辑用 useMemo 派生，页面风格要贴近现有 Dashboard。',
      expectedFiles: ['src/pages/dashboard/pages/OperationsCenter.tsx']
    },
    {
      id: 'operations_route',
      title: '接入页面路由',
      instructions: '修改 src/App.tsx 导入 OperationsCenter，并在 /pages 子路由下新增 path: "operations"。不要破坏已有 Profile、Settings、Billing、Pricing、ApiDemo 等页面路由。',
      expectedFiles: ['src/App.tsx']
    },
    {
      id: 'operations_menu',
      title: '接入侧边栏菜单',
      instructions: '修改 src/routes/DashboardRoutes.ts，在 Pages 菜单里加入 Operations Center，link 为 /pages/operations。不要破坏已有 Profile、Settings、Billing、Pricing、404 Error 等菜单项。',
      expectedFiles: ['src/routes/DashboardRoutes.ts']
    }
  ]
}

function chatCompletionsUrl(baseUrl: string): string {
  const url = new URL(baseUrl)
  const normalizedPath = url.pathname.replace(/\/+$/, '')
  url.pathname = `${normalizedPath.endsWith('/v1') ? normalizedPath : `${normalizedPath}/v1`}/chat/completions`
  return url.toString()
}

function safeJsonObject(text: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(text) as unknown
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
      ? parsed as Record<string, unknown>
      : {}
  } catch {
    return {}
  }
}

function firstAssistantContent(body: Record<string, unknown>): string | undefined {
  const choices = Array.isArray(body.choices) ? body.choices : []
  const first = choices[0] as { message?: { content?: unknown, reasoning_content?: unknown } } | undefined
  if (typeof first?.message?.content === 'string' && first.message.content.trim()) {
    return first.message.content
  }
  if (typeof first?.message?.reasoning_content === 'string' && first.message.reasoning_content.trim()) {
    return first.message.reasoning_content
  }
  return choices.length > 0 ? '[non-empty-choice]' : undefined
}

function requiredEnv(name: string, aliases: string[] = []): string {
  const value = envText(name, aliases)
  if (!value) {
    throw new Error(`缺少环境变量 ${name}`)
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

function numberEnv(name: string): number | undefined {
  const value = envText(name)
  if (!value) return undefined
  const parsed = Number(value)
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : undefined
}

function booleanEnv(name: string): boolean | undefined {
  const value = envText(name)
  if (!value) return undefined
  if (['1', 'true', 'yes', 'on'].includes(value.toLowerCase())) return true
  if (['0', 'false', 'no', 'off'].includes(value.toLowerCase())) return false
  return undefined
}

function modelCostDefault(model: string): number {
  if (model === flashModel || model === lowModel || model.includes('mini') || model.includes('flash')) return 0.002
  if (model.includes('glm-5.1')) return 0.006
  if (model.includes('glm')) return 0.01
  if (model.includes('gpt-5.4') && !model.includes('mini')) return 0.015
  if (model.includes('gpt-5.5')) return 0.02
  if (model.includes('opus')) return 0.05
  return 0.02
}

function roundMoney(value: number): number {
  return Number(value.toFixed(6))
}

function sanitizeErrorSnippet(value: string): string {
  return value.replaceAll(realApiKey, '[redacted-real-api-key]').slice(0, 2_000) || 'empty response'
}

function sanitizeBaseUrl(value: string): string {
  try {
    const url = new URL(value)
    url.username = ''
    url.password = ''
    return url.toString().replace(/\/$/, '')
  } catch {
    return value.replaceAll(realApiKey, '[redacted-real-api-key]')
  }
}

function normalizeNewline(value: string): string {
  return `${value.replace(/\r\n/g, '\n').replace(/\r/g, '\n').trim()}\n`
}

function wait(ms: number): Promise<void> {
  if (ms <= 0) return Promise.resolve()
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}

function logProgress(strategy: WorkflowStrategyName, step: string, status: string): void {
  console.error(`[hybrid-project-workflow] ${strategy} ${step}: ${status}`)
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
