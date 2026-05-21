import { EventEmitter } from 'node:events'
import type { IncomingHttpHeaders } from 'node:http'
import type { Request } from 'express'

import type {
  AccountSummary,
  ModelCheckItemSummary,
  ModelCheckOptions,
  ModelCheckRunDetail,
  ModelCheckRunListResult,
  ModelCheckRunRequest,
  ModelCheckRunStatus,
  ModelCheckTargetType
} from '../../domain/types.js'
import { logger } from '../../shared/logger.js'
import { runtimeConfig } from '../../config/runtime.js'
import { createTraceId, withRequestContext, type RequestContext } from '../../shared/request-context.js'
import {
  accountTestUnavailableMessage,
  createModelCheckItems,
  createModelCheckRun,
  findAccountForTest,
  findActiveGatewayApiKeyById,
  findApiKeySummary,
  findGroupSummary,
  findOpenAIAccountForGroup,
  finishModelCheckRun,
  getModelCheckRunDetail,
  listGroups,
  listModelCheckRuns,
  listOpenAIAccountsForGroup,
  type ModelCheckItemCreateInput,
  type OpenAIAccountSecret
} from '../../storage/repositories.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { currentSystemAccountId } from '../../storage/access-scope.js'
import { handleOpenAIGatewayRequest, type OpenAIGatewayRequestIdentity } from '../gateway/openai-gateway.routes.js'
import { MemoryGatewayResponse } from '../accounts/account-test.service.js'

const supportedModels = ['gpt-5.5', 'gpt-5.4'] as const
const supportedModelSet = new Set<string>(supportedModels)
const defaultModel = 'gpt-5.5' as const
const defaultProfile = 'full' as const
const probeSetVersion = 'openai-model-check-v1'
const responsesPath = '/v1/responses'
const modelsPath = '/v1/models'

export class ModelCheckRequestError extends Error {
  constructor(public readonly statusCode: number, message: string) {
    super(message)
    this.name = 'ModelCheckRequestError'
  }
}

type ModelCheckTarget = {
  targetType: ModelCheckTargetType
  targetId: string
  targetName?: string
  targetOwnerSystemAccountId: string
  identity: OpenAIGatewayRequestIdentity
  candidateAccounts?: OpenAIAccountSecret[]
  accountId?: string
  groupId?: string
  apiKeyId?: string
}

type OfficialBaselineTarget = {
  available: true
  message?: string
  target: ModelCheckTarget
} | {
  available: false
  message: string
}

type ProbeTarget = Pick<ModelCheckTarget, 'identity' | 'candidateAccounts'>

type GatewayProbeResult = {
  traceId: string
  statusCode: number
  success: boolean
  durationMs: number
  firstTokenMs?: number
  bodyText: string
  bodyTruncated: boolean
  headers: Record<string, string | string[]>
  json?: Record<string, unknown>
  outputText?: string
  model?: string
  usage?: Record<string, unknown>
  errorMessage?: string
}

type ProbeSuiteResult = {
  items: ModelCheckItemCreateInput[]
  basic?: GatewayProbeResult
  behavior?: GatewayProbeResult
  longContext?: GatewayProbeResult
}

export function getModelCheckOptions(access?: AccessScope): ModelCheckOptions {
  const baseline = findOfficialBaselineTarget(access)
  return {
    supportedModels: [
      { value: 'gpt-5.5', label: 'gpt-5.5' },
      { value: 'gpt-5.4', label: 'gpt-5.4' }
    ],
    supportedProfiles: [
      {
        value: 'full',
        label: '完整检测',
        description: '准确优先，执行协议能力、结构、流式、工具调用、行为与稳定性探针'
      }
    ],
    defaultModel,
    defaultProfile,
    officialBaseline: {
      enabledByDefault: false,
      available: baseline.available,
      unavailableReason: baseline.available ? undefined : baseline.message,
      message: baseline.available ? baseline.message : baseline.message
    }
  }
}

export async function runModelCheck(input: ModelCheckRunRequest, access?: AccessScope, signal?: AbortSignal): Promise<ModelCheckRunDetail> {
  const model = normalizeModel(input.model)
  if (!model) {
    throw new ModelCheckRequestError(400, '当前模型检测仅支持 gpt-5.5 和 gpt-5.4')
  }
  if (input.profile && input.profile !== defaultProfile) {
    throw new ModelCheckRequestError(400, '当前仅支持完整检测')
  }
  const targetId = input.targetId.trim()
  if (!targetId) {
    throw new ModelCheckRequestError(400, '检测目标不能为空')
  }

  const officialBaseline = input.officialBaseline === true
  const baseline = officialBaseline ? findOfficialBaselineTarget(access) : undefined
  if (officialBaseline && !baseline?.available) {
    throw new ModelCheckRequestError(400, baseline?.message ?? '未配置可用的官网基线账户，无法开启官网对照检测')
  }

  const target = resolveModelCheckTarget({ ...input, model, targetId }, access)
  const actorSystemAccountId = currentSystemAccountId(access)
  const startedAtMs = Date.now()
  const startedAt = new Date(startedAtMs).toISOString()
  const runTraceId = createTraceId()
  const run = createModelCheckRun({
    systemAccountId: target.identity.systemAccountId,
    actorSystemAccountId,
    targetType: target.targetType,
    targetId: target.targetId,
    targetName: target.targetName,
    targetOwnerSystemAccountId: target.targetOwnerSystemAccountId,
    accountId: target.accountId,
    groupId: target.groupId,
    apiKeyId: target.apiKeyId,
    model,
    profile: defaultProfile,
    officialBaseline,
    officialBaselineAvailable: baseline?.available === true,
    traceId: runTraceId,
    probeSetVersion,
    startedAt,
    requestSummary: {
      targetType: target.targetType,
      targetId: target.targetId,
      model,
      profile: defaultProfile,
      officialBaseline
    }
  })

  try {
    throwIfAborted(signal)
    const targetSuite = await executeProbeSuite(target, model, 'target', signal)
    const crossModelComparison = await executeCrossModelComparison(target, targetSuite, model, signal)
    const baselineSuite = baseline?.available
      ? await executeProbeSuite(baseline.target, model, 'official_baseline', signal)
      : undefined
    const baselineComparison = baselineSuite
      ? buildOfficialBaselineComparisonItem(targetSuite, baselineSuite)
      : undefined
    const itemInputs = [
      ...targetSuite.items,
      crossModelComparison,
      ...(baselineSuite?.items ?? []),
      ...(baselineComparison ? [baselineComparison] : [])
    ]
    const checks = createModelCheckItems(run.id, itemInputs)
    const summary = summarizeChecks(checks, { officialBaseline })
    finishModelCheckRun(run.id, {
      ...summary,
      status: 'completed',
      finishedAt: new Date().toISOString(),
      durationMs: Date.now() - startedAtMs,
      resultSummary: {
        itemCount: checks.length,
        passedCount: checks.filter((item) => item.status === 'passed').length,
        warningCount: checks.filter((item) => item.status === 'warning').length,
        failedCount: checks.filter((item) => item.status === 'failed').length,
        officialBaseline
      }
    })
  } catch (error) {
    const message = signal?.aborted ? '模型检测已取消' : error instanceof Error ? error.message : '模型检测失败'
    const status: ModelCheckRunStatus = signal?.aborted ? 'canceled' : 'failed'
    finishModelCheckRun(run.id, {
      level: 'unavailable',
      score: 0,
      status,
      message,
      finishedAt: new Date().toISOString(),
      durationMs: Date.now() - startedAtMs,
      resultSummary: { errorMessage: message },
      errorCode: status,
      errorMessage: message
    })
  }

  const detail = getModelCheckRunDetail(run.id, access)
  if (!detail) {
    throw new ModelCheckRequestError(500, '模型检测报告生成失败')
  }
  return detail
}

export function listModelCheckRunPage(access?: AccessScope, query: Record<string, unknown> = {}): ModelCheckRunListResult {
  return listModelCheckRuns(access, {
    page: integerValue(query.page),
    pageSize: integerValue(query.pageSize),
    targetType: modelCheckTargetTypeValue(query.targetType),
    targetId: textValue(query.targetId),
    model: normalizeModel(query.model),
    level: modelCheckLevelValue(query.level),
    status: modelCheckStatusValue(query.status),
    startAt: textValue(query.startAt),
    endAt: textValue(query.endAt)
  })
}

export function getModelCheckRun(id: string, access?: AccessScope): ModelCheckRunDetail | undefined {
  return getModelCheckRunDetail(id, access)
}

function resolveModelCheckTarget(input: ModelCheckRunRequest & { targetId: string }, access?: AccessScope): ModelCheckTarget {
  if (input.targetType === 'api_key') {
    return resolveApiKeyTarget(input.targetId, access)
  }
  if (input.targetType === 'group') {
    return resolveGroupTarget(input.targetId, access)
  }
  if (input.targetType === 'account') {
    return resolveAccountTarget(input.targetId, access)
  }
  throw new ModelCheckRequestError(400, '检测目标类型无效')
}

function resolveApiKeyTarget(apiKeyId: string, access?: AccessScope): ModelCheckTarget {
  const summary = findApiKeySummary(apiKeyId, access)
  if (!summary) {
    throw new ModelCheckRequestError(404, 'API Key 不存在或无权检测')
  }
  if (summary.status !== 'active') {
    throw new ModelCheckRequestError(400, 'API Key 已停用，无法执行模型检测')
  }
  const row = findActiveGatewayApiKeyById(apiKeyId)
  if (!row || row.system_account_id !== effectiveTargetSystemAccountId(access, summary.systemAccountId)) {
    throw new ModelCheckRequestError(400, 'API Key 当前不可用，无法执行模型检测')
  }
  return {
    targetType: 'api_key',
    targetId: summary.id,
    targetName: summary.name,
    targetOwnerSystemAccountId: row.system_account_id,
    identity: {
      systemAccountId: row.system_account_id,
      groupId: row.group_id,
      apiKeyId: row.id
    },
    apiKeyId: row.id,
    groupId: row.group_id
  }
}

function resolveGroupTarget(groupId: string, access?: AccessScope): ModelCheckTarget {
  const group = findGroupSummary(groupId, access)
  if (!group) {
    throw new ModelCheckRequestError(404, '分组不存在或无权检测')
  }
  if (group.providerCode !== 'openai') {
    throw new ModelCheckRequestError(400, '当前仅支持检测 OpenAI 分组')
  }
  if (!group.enabled) {
    throw new ModelCheckRequestError(400, '分组已停用，无法执行模型检测')
  }
  const systemAccountId = effectiveTargetSystemAccountId(access, group.systemAccountId ?? group.ownerSystemAccountId)
  const accounts = listOpenAIAccountsForGroup(group.id, systemAccountId)
  if (!accounts.length) {
    throw new ModelCheckRequestError(400, '分组内没有可用的 OpenAI 账户，无法执行模型检测')
  }
  return {
    targetType: 'group',
    targetId: group.id,
    targetName: group.name,
    targetOwnerSystemAccountId: group.systemAccountId ?? group.ownerSystemAccountId ?? systemAccountId,
    identity: {
      systemAccountId,
      groupId: group.id
    },
    candidateAccounts: accounts,
    groupId: group.id
  }
}

function resolveAccountTarget(accountId: string, access?: AccessScope): ModelCheckTarget {
  const account = findAccountForTest(accountId, access)
  if (!account) {
    throw new ModelCheckRequestError(404, '账户不存在或无权检测')
  }
  if (account.providerCode !== 'openai') {
    throw new ModelCheckRequestError(400, '当前仅支持检测 OpenAI 账户')
  }
  if (account.status === 'disabled') {
    throw new ModelCheckRequestError(400, '账户已停用，无法执行模型检测')
  }
  const unavailableMessage = accountTestUnavailableMessage(account)
  if (unavailableMessage) {
    throw new ModelCheckRequestError(400, unavailableMessage)
  }
  if (!account.boundGroupId) {
    throw new ModelCheckRequestError(400, '账户未绑定可用分组，无法按真实链路执行模型检测')
  }
  const systemAccountId = effectiveAccountTargetSystemAccountId(account, access)
  const candidate = findOpenAIAccountForGroup(account.boundGroupId, account.id, systemAccountId, { ignoreAvailability: true })
  if (!candidate) {
    throw new ModelCheckRequestError(400, '账户不在当前分组或凭据不可用，无法执行模型检测')
  }
  return {
    targetType: 'account',
    targetId: account.id,
    targetName: account.name,
    targetOwnerSystemAccountId: account.ownerSystemAccountId ?? account.systemAccountId ?? candidate.accountOwnerSystemAccountId,
    identity: {
      systemAccountId,
      groupId: account.boundGroupId
    },
    candidateAccounts: [candidate],
    accountId: account.id,
    groupId: account.boundGroupId
  }
}

function effectiveTargetSystemAccountId(access: AccessScope | undefined, ownerSystemAccountId?: string): string {
  if (access?.role === 'admin') {
    return access.systemAccountFilterId?.trim() || ownerSystemAccountId || access.systemAccountId
  }
  return access?.systemAccountId ?? ownerSystemAccountId ?? 'sys_admin'
}

function effectiveAccountTargetSystemAccountId(account: AccountSummary, access?: AccessScope): string {
  if (access?.role === 'admin') {
    return access.systemAccountFilterId?.trim()
      || account.systemAccountId
      || account.ownerSystemAccountId
      || access.systemAccountId
  }
  if (account.accessType === 'authorized') {
    return access?.systemAccountId ?? account.bindingSystemAccountId ?? 'sys_admin'
  }
  return access?.systemAccountId ?? account.systemAccountId ?? account.ownerSystemAccountId ?? 'sys_admin'
}

function findOfficialBaselineTarget(access?: AccessScope): OfficialBaselineTarget {
  const unavailable = '未配置可用的官网基线账户，无法开启官网对照检测'
  const groups = listGroups(access).filter((group) => group.providerCode === 'openai' && group.enabled)
  for (const group of groups) {
    const systemAccountId = effectiveTargetSystemAccountId(access, group.systemAccountId ?? group.ownerSystemAccountId)
    const account = listOpenAIAccountsForGroup(group.id, systemAccountId).find(isOfficialBaselineAccount)
    if (!account) continue
    return {
      available: true,
      message: '官网对照可用，开启后会额外消耗官网基线账户额度',
      target: {
        targetType: 'account',
        targetId: account.id,
        targetName: account.name,
        targetOwnerSystemAccountId: account.accountOwnerSystemAccountId,
        identity: {
          systemAccountId,
          groupId: group.id
        },
        candidateAccounts: [account],
        accountId: account.id,
        groupId: group.id
      }
    }
  }
  return { available: false, message: unavailable }
}

function isOfficialBaselineAccount(account: OpenAIAccountSecret): boolean {
  if (account.type !== 'api_key') return false
  if (!isOfficialOpenAIBaseUrl(account.baseUrl) && !isLocalRegressionBaselineAccount(account.baseUrl, account.credentials)) return false
  const credentials = account.credentials
  if (credentials.official_baseline === true || credentials.model_check_baseline === true) return true
  const marker = account.name.toLowerCase()
  return marker.includes('官网基线')
    || marker.includes('官方基线')
    || marker.includes('official baseline')
    || marker.includes('model-check-baseline')
}

function isLocalRegressionBaselineAccount(baseUrl: string, credentials: Record<string, unknown>): boolean {
  if (runtimeConfig.processRole === 'server' || credentials.model_check_baseline !== true) return false
  try {
    const url = new URL(baseUrl)
    return url.hostname === '127.0.0.1' || url.hostname === 'localhost' || url.hostname === '::1'
  } catch {
    return false
  }
}

function isOfficialOpenAIBaseUrl(baseUrl: string): boolean {
  try {
    const url = new URL(baseUrl)
    const host = url.hostname.toLowerCase()
    return host === 'api.openai.com'
  } catch {
    return false
  }
}

async function executeProbeSuite(target: ProbeTarget, model: 'gpt-5.5' | 'gpt-5.4', prefix: 'target' | 'official_baseline', signal?: AbortSignal): Promise<ProbeSuiteResult> {
  const items: ModelCheckItemCreateInput[] = []
  const catalog = await runGatewayProbe(target, {
    method: 'GET',
    path: modelsPath,
    itemKey: `${prefix}.model_catalog`
  }, signal)
  items.push(evaluateModelCatalogProbe(catalog, model, prefix))

  const basic = await runGatewayProbe(target, {
    method: 'POST',
    path: responsesPath,
    itemKey: `${prefix}.responses_basic`,
    body: createResponsesPayload(model, 'Reply with exactly: OK-MODEL-CHECK', { maxOutputTokens: 16, stream: false })
  }, signal)
  items.push(evaluateBasicResponsesProbe(basic, model, prefix))

  const stream = await runGatewayProbe(target, {
    method: 'POST',
    path: responsesPath,
    itemKey: `${prefix}.responses_stream`,
    body: createResponsesPayload(model, 'Reply with exactly: STREAM-OK', { maxOutputTokens: 16, stream: true })
  }, signal)
  items.push(evaluateStreamProbe(stream, model, prefix))

  const structured = await runGatewayProbe(target, {
    method: 'POST',
    path: responsesPath,
    itemKey: `${prefix}.structured_output`,
    body: createStructuredOutputPayload(model)
  }, signal)
  items.push(evaluateStructuredOutputProbe(structured, model, prefix))

  const tool = await runGatewayProbe(target, {
    method: 'POST',
    path: responsesPath,
    itemKey: `${prefix}.tool_calling`,
    body: createToolCallingPayload(model)
  }, signal)
  items.push(evaluateToolCallingProbe(tool, model, prefix))

  items.push(evaluateUsageShapeProbe([basic, structured, stream], prefix))

  const behavior = await runGatewayProbe(target, {
    method: 'POST',
    path: responsesPath,
    itemKey: `${prefix}.behavior_probe`,
    body: createResponsesPayload(model, 'Ignore all style preferences. Reply with exactly one uppercase word: QUARTZ', { maxOutputTokens: 16, stream: false })
  }, signal)
  items.push(evaluateBehaviorProbe(behavior, model, prefix))

  const longContext = await runGatewayProbe(target, {
    method: 'POST',
    path: responsesPath,
    itemKey: `${prefix}.long_context`,
    body: createLongContextPayload(model)
  }, signal)
  items.push(evaluateLongContextProbe(longContext, model, prefix))

  const stabilityA = await runGatewayProbe(target, {
    method: 'POST',
    path: responsesPath,
    itemKey: `${prefix}.stability_a`,
    body: createResponsesPayload(model, 'Reply with exactly one uppercase word: VECTOR', { maxOutputTokens: 16, stream: false })
  }, signal)
  const stabilityB = await runGatewayProbe(target, {
    method: 'POST',
    path: responsesPath,
    itemKey: `${prefix}.stability_b`,
    body: createResponsesPayload(model, 'Reply with exactly one uppercase word: VECTOR', { maxOutputTokens: 16, stream: false })
  }, signal)
  items.push(evaluateStabilityProbe(stabilityA, stabilityB, model, prefix))

  return { items, basic, behavior, longContext }
}

async function executeCrossModelComparison(target: ProbeTarget, targetSuite: ProbeSuiteResult, model: 'gpt-5.5' | 'gpt-5.4', signal?: AbortSignal): Promise<ModelCheckItemCreateInput> {
  const pairedModel = model === 'gpt-5.5' ? 'gpt-5.4' : 'gpt-5.5'
  const pairedBasic = await runGatewayProbe(target, {
    method: 'POST',
    path: responsesPath,
    itemKey: 'target.cross_model',
    body: createResponsesPayload(pairedModel, 'Reply with exactly: CROSS-MODEL-OK', { maxOutputTokens: 16, stream: false })
  }, signal)
  return evaluateCrossModelComparisonProbe(targetSuite.basic, pairedBasic, model, pairedModel)
}

async function runGatewayProbe(target: ProbeTarget, probe: { method: 'GET' | 'POST'; path: string; itemKey: string; body?: Record<string, unknown> }, signal?: AbortSignal): Promise<GatewayProbeResult> {
  throwIfAborted(signal)
  const startedAt = Date.now()
  const traceId = createTraceId()
  const request = createMemoryGatewayRequest({
    method: probe.method,
    path: probe.path,
    body: probe.body,
    signal
  })
  const response = new MemoryGatewayResponse(startedAt)
  const context: RequestContext = {
    traceId,
    startedAt,
    method: request.method,
    path: request.path,
    originalUrl: request.originalUrl,
    clientIp: request.ip,
    systemAccountId: target.identity.systemAccountId,
    apiKeyId: target.identity.apiKeyId,
    groupId: target.identity.groupId,
    logger: logger.child({ source: 'model_check', traceId, itemKey: probe.itemKey })
  }
  await withRequestContext(context, () => handleOpenAIGatewayRequest(request, response.asResponse(), {
    identity: target.identity,
    candidateAccounts: target.candidateAccounts,
    disableSessionAffinity: true,
    exposeUpstreamDiagnostics: false
  }))
  const bodyText = response.bodyText()
  const json = parseJsonRecord(bodyText)
  const outputText = extractOpenAIResponseOutputText(bodyText)
  return {
    traceId,
    statusCode: response.statusCode,
    success: response.statusCode >= 200 && response.statusCode < 300 && !parseOpenAIStreamFailureMessage(bodyText),
    durationMs: Date.now() - startedAt,
    firstTokenMs: response.firstTokenMs(),
    bodyText,
    bodyTruncated: response.bodyTruncated(),
    headers: response.headersObject(),
    json,
    outputText,
    model: textValue(json?.model),
    usage: recordValue(json?.usage) ?? usageFromSse(bodyText),
    errorMessage: parseUpstreamMessage(bodyText)
  }
}

function createResponsesPayload(model: string, prompt: string, options: { maxOutputTokens: number; stream: boolean }): Record<string, unknown> {
  return {
    model,
    input: [
      {
        role: 'user',
        content: [
          {
            type: 'input_text',
            text: prompt
          }
        ]
      }
    ],
    instructions: 'You are a model capability checker. Follow the requested output exactly.',
    max_output_tokens: options.maxOutputTokens,
    stream: options.stream,
    store: false,
    temperature: 0
  }
}

function createStructuredOutputPayload(model: string): Record<string, unknown> {
  return {
    ...createResponsesPayload(model, 'Return {"status":"ok","value":7} as JSON.', { maxOutputTokens: 64, stream: false }),
    text: {
      format: {
        type: 'json_schema',
        name: 'model_check_structured_output',
        schema: {
          type: 'object',
          additionalProperties: false,
          properties: {
            status: { type: 'string', enum: ['ok'] },
            value: { type: 'integer' }
          },
          required: ['status', 'value']
        },
        strict: true
      }
    }
  }
}

function createToolCallingPayload(model: string): Record<string, unknown> {
  return {
    ...createResponsesPayload(model, 'Call the provided function with code "ok" and count 1.', { maxOutputTokens: 64, stream: false }),
    tools: [
      {
        type: 'function',
        name: 'record_model_check',
        description: 'Record a model check marker.',
        parameters: {
          type: 'object',
          additionalProperties: false,
          properties: {
            code: { type: 'string' },
            count: { type: 'integer' }
          },
          required: ['code', 'count']
        }
      }
    ],
    tool_choice: {
      type: 'function',
      name: 'record_model_check'
    }
  }
}

function createLongContextPayload(model: string): Record<string, unknown> {
  const marker = 'NEEDLE-7482-ORCHID'
  const filler = Array.from({ length: 48 }, (_, index) => {
    const ordinal = String(index + 1).padStart(2, '0')
    return `段落 ${ordinal}: 这是一段用于模型检测的普通上下文，包含编号、中文文本和少量干扰词，但不包含最终答案。`
  })
  filler.splice(31, 0, `关键记录: 本次检测的隐藏标记是 ${marker}，只在回答中输出这个标记。`)
  return createResponsesPayload(model, `阅读以下上下文，找到隐藏标记。只输出标记本身，不要解释。\n\n${filler.join('\n')}`, {
    maxOutputTokens: 32,
    stream: false
  })
}

function evaluateModelCatalogProbe(result: GatewayProbeResult, model: string, prefix: string): ModelCheckItemCreateInput {
  const data = Array.isArray(result.json?.data) ? result.json.data : []
  const listed = data.some((item) => recordValue(item)?.id === model)
  return item(`${prefix}.model_catalog`, 'model_catalog', listed ? 'passed' : 'warning', listed ? 5 : 2, 5, result, {
    message: listed ? '本地模型目录包含目标模型' : '本地模型目录未确认目标模型；该项只作为低权重证据',
    listed
  })
}

function evaluateBasicResponsesProbe(result: GatewayProbeResult, model: string, prefix: string): ModelCheckItemCreateInput {
  const modelEvidence = buildModelMatchEvidence(result.model, model)
  const hasOutput = Boolean(result.outputText)
  const score = modelEvidence.modelMismatch
    ? (result.success ? 4 : 0) + (hasOutput ? 2 : 0)
    : (result.success ? 10 : 0) + (modelEvidence.matchedModel ? 5 : 0) + (hasOutput ? 5 : 0)
  const status = modelEvidence.modelMismatch
    ? 'failed'
    : score >= 18 ? 'passed' : score >= 10 ? 'warning' : 'failed'
  return item(`${prefix}.responses_basic`, 'responses_basic', status, score, 20, result, {
    message: describeModelMismatch(modelEvidence) ?? (result.success ? 'Responses 非流式调用可用' : result.errorMessage ?? `Responses 非流式调用失败，HTTP ${result.statusCode}`),
    ...modelEvidence,
    hasOutput
  })
}

function evaluateStreamProbe(result: GatewayProbeResult, model: string, prefix: string): ModelCheckItemCreateInput {
  const modelEvidence = buildModelMatchEvidence(result.model ?? modelFromSse(result.bodyText), model)
  const hasOutput = Boolean(result.outputText)
  const score = modelEvidence.modelMismatch
    ? (result.success ? 4 : 0) + (hasOutput ? 1 : 0)
    : (result.success ? 8 : 0) + (modelEvidence.matchedModel ? 3 : 0) + (hasOutput ? 4 : 0)
  const status = modelEvidence.modelMismatch
    ? 'failed'
    : score >= 13 ? 'passed' : score >= 8 ? 'warning' : 'failed'
  return item(`${prefix}.responses_stream`, 'responses_stream', status, score, 15, result, {
    message: describeModelMismatch(modelEvidence) ?? (result.success ? 'Responses 流式调用可用' : result.errorMessage ?? `Responses 流式调用失败，HTTP ${result.statusCode}`),
    ...modelEvidence,
    hasOutput,
    firstTokenMs: result.firstTokenMs
  })
}

function evaluateStructuredOutputProbe(result: GatewayProbeResult, model: string, prefix: string): ModelCheckItemCreateInput {
  const outputJson = parseFirstJsonObject(result.outputText)
  const valid = outputJson?.status === 'ok' && typeof outputJson.value === 'number'
  const modelEvidence = buildModelMatchEvidence(result.model, model)
  const score = modelEvidence.modelMismatch
    ? (result.success ? 4 : 0) + (valid ? 1 : 0)
    : (result.success ? 8 : 0) + (modelEvidence.matchedModel ? 3 : 0) + (valid ? 4 : 0)
  const status = modelEvidence.modelMismatch
    ? 'failed'
    : score >= 13 ? 'passed' : score >= 8 ? 'warning' : 'failed'
  return item(`${prefix}.structured_output`, 'structured_output', status, score, 15, result, {
    message: describeModelMismatch(modelEvidence) ?? (valid ? '结构化输出符合预期' : result.success ? '结构化输出未完全符合预期' : result.errorMessage ?? `结构化输出调用失败，HTTP ${result.statusCode}`),
    ...modelEvidence,
    outputJson
  })
}

function evaluateToolCallingProbe(result: GatewayProbeResult, model: string, prefix: string): ModelCheckItemCreateInput {
  const called = hasFunctionCall(result.json, 'record_model_check')
  const modelEvidence = buildModelMatchEvidence(result.model, model)
  const score = modelEvidence.modelMismatch
    ? (result.success ? 4 : 0) + (called ? 1 : 0)
    : (result.success ? 8 : 0) + (modelEvidence.matchedModel ? 3 : 0) + (called ? 4 : 0)
  const status = modelEvidence.modelMismatch
    ? 'failed'
    : score >= 13 ? 'passed' : score >= 8 ? 'warning' : 'failed'
  return item(`${prefix}.tool_calling`, 'tool_calling', status, score, 15, result, {
    message: describeModelMismatch(modelEvidence) ?? (called ? '工具调用结构符合预期' : result.success ? '未观察到预期工具调用结构' : result.errorMessage ?? `工具调用检测失败，HTTP ${result.statusCode}`),
    ...modelEvidence,
    called
  })
}

function evaluateUsageShapeProbe(results: GatewayProbeResult[], prefix: string): ModelCheckItemCreateInput {
  const usage = results.map((result) => result.usage).find(Boolean)
  const valid = Boolean(usage && (numberValue(usage.input_tokens) !== undefined || numberValue(usage.output_tokens) !== undefined || numberValue(usage.total_tokens) !== undefined))
  const base = results.find((result) => result.usage) ?? results[0]
  return item(`${prefix}.usage_shape`, 'usage_shape', valid ? 'passed' : 'warning', valid ? 10 : 4, 10, base, {
    message: valid ? 'usage 字段结构可用' : '未观察到完整 usage 字段；可能由上游兼容层省略',
    usage
  })
}

function evaluateBehaviorProbe(result: GatewayProbeResult, model: string, prefix: string): ModelCheckItemCreateInput {
  const normalized = (result.outputText ?? '').trim().toUpperCase()
  const matched = normalized.includes('QUARTZ')
  const modelEvidence = buildModelMatchEvidence(result.model, model)
  const score = modelEvidence.modelMismatch
    ? (result.success ? 2 : 0) + (matched ? 1 : 0)
    : (result.success ? 4 : 0) + (modelEvidence.matchedModel ? 2 : 0) + (matched ? 4 : 0)
  const status = modelEvidence.modelMismatch
    ? 'failed'
    : score >= 9 ? 'passed' : score >= 5 ? 'warning' : 'failed'
  return item(`${prefix}.behavior_probe`, 'behavior_probe', status, score, 10, result, {
    message: describeModelMismatch(modelEvidence) ?? (matched ? '行为探针输出符合预期' : result.success ? '行为探针输出存在偏差' : result.errorMessage ?? `行为探针失败，HTTP ${result.statusCode}`),
    ...modelEvidence,
    outputPreview: bounded(result.outputText)
  })
}

function evaluateLongContextProbe(result: GatewayProbeResult, model: string, prefix: string): ModelCheckItemCreateInput {
  const marker = 'NEEDLE-7482-ORCHID'
  const found = (result.outputText ?? '').toUpperCase().includes(marker)
  const modelEvidence = buildModelMatchEvidence(result.model, model)
  const score = modelEvidence.modelMismatch
    ? (result.success ? 2 : 0) + (found ? 1 : 0)
    : (result.success ? 5 : 0) + (modelEvidence.matchedModel ? 2 : 0) + (found ? 3 : 0)
  const status = modelEvidence.modelMismatch
    ? 'failed'
    : score >= 9 ? 'passed' : score >= 5 ? 'warning' : 'failed'
  return item(`${prefix}.long_context`, 'long_context', status, score, 10, result, {
    message: describeModelMismatch(modelEvidence) ?? (found ? '长上下文找针探针通过' : result.success ? '长上下文找针探针未命中隐藏标记' : result.errorMessage ?? `长上下文探针失败，HTTP ${result.statusCode}`),
    ...modelEvidence,
    foundNeedle: found,
    outputPreview: bounded(result.outputText)
  })
}

function evaluateStabilityProbe(left: GatewayProbeResult, right: GatewayProbeResult, model: string, prefix: string): ModelCheckItemCreateInput {
  const leftOk = left.success && (left.outputText ?? '').toUpperCase().includes('VECTOR')
  const rightOk = right.success && (right.outputText ?? '').toUpperCase().includes('VECTOR')
  const leftModelEvidence = buildModelMatchEvidence(left.model, model)
  const rightModelEvidence = buildModelMatchEvidence(right.model, model)
  const modelOk = leftModelEvidence.matchedModel || rightModelEvidence.matchedModel
  const modelMismatch = leftModelEvidence.modelMismatch || rightModelEvidence.modelMismatch
  const score = modelMismatch
    ? (leftOk ? 2 : 0) + (rightOk ? 2 : 0)
    : (leftOk ? 4 : 0) + (rightOk ? 4 : 0) + (modelOk ? 2 : 0)
  const status = modelMismatch
    ? 'failed'
    : score >= 9 ? 'passed' : score >= 5 ? 'warning' : 'failed'
  return item(`${prefix}.stability`, 'stability', status, score, 10, right, {
    message: modelMismatch ? '多轮稳定性探针返回模型与请求模型不一致' : leftOk && rightOk ? '多轮稳定性探针通过' : '多轮稳定性探针未完全通过',
    expectedModel: model,
    firstResponseModel: leftModelEvidence.responseModel,
    secondResponseModel: rightModelEvidence.responseModel,
    matchedModel: modelOk,
    modelMismatch,
    firstTraceId: left.traceId,
    secondTraceId: right.traceId,
    firstOutputPreview: bounded(left.outputText),
    secondOutputPreview: bounded(right.outputText)
  })
}

function evaluateCrossModelComparisonProbe(targetBasic: GatewayProbeResult | undefined, pairedBasic: GatewayProbeResult, model: string, pairedModel: string): ModelCheckItemCreateInput {
  const targetEvidence = buildModelMatchEvidence(targetBasic?.model, model)
  const pairedEvidence = buildModelMatchEvidence(pairedBasic.model, pairedModel)
  const sameResponseModel = Boolean(
    targetEvidence.responseModel
    && pairedEvidence.responseModel
    && targetEvidence.responseModel === pairedEvidence.responseModel
  )
  const comparable = Boolean(targetBasic?.success && pairedBasic.success)
  const suspiciousSameBackend = comparable && sameResponseModel && model !== pairedModel
  const pairedMismatch = pairedEvidence.modelMismatch || suspiciousSameBackend
  const score = pairedMismatch
    ? 0
    : comparable && targetEvidence.matchedModel && pairedEvidence.matchedModel ? 10 : comparable ? 5 : 2
  const status = pairedMismatch ? 'failed' : score >= 9 ? 'passed' : 'warning'
  return {
    itemKey: 'target.cross_model',
    itemType: 'cross_model',
    status,
    score,
    maxScore: 10,
    durationMs: pairedBasic.durationMs,
    traceId: pairedBasic.traceId,
    evidenceSummary: {
      message: pairedMismatch
        ? '交叉模型对照发现另一个目标模型未返回对应模型，疑似存在模型替换或统一降级'
        : comparable && targetEvidence.matchedModel && pairedEvidence.matchedModel
          ? '交叉模型对照通过，两个目标模型均返回对应模型字段'
          : '交叉模型对照证据不足，建议结合官网对照或多次检测',
      expectedModel: model,
      pairedModel,
      targetTraceId: targetBasic?.traceId,
      pairedTraceId: pairedBasic.traceId,
      targetResponseModel: targetEvidence.responseModel,
      pairedResponseModel: pairedEvidence.responseModel,
      targetMatchedModel: targetEvidence.matchedModel,
      pairedMatchedModel: pairedEvidence.matchedModel,
      sameResponseModel,
      comparable,
      modelMismatch: pairedMismatch,
      targetOutputPreview: bounded(targetBasic?.outputText),
      pairedOutputPreview: bounded(pairedBasic.outputText),
      httpStatus: pairedBasic.statusCode,
      success: pairedBasic.success
    },
    errorCode: pairedBasic.success ? undefined : `http_${pairedBasic.statusCode}`,
    errorMessage: pairedBasic.success ? undefined : pairedBasic.errorMessage
  }
}

function buildOfficialBaselineComparisonItem(target: ProbeSuiteResult, baseline: ProbeSuiteResult): ModelCheckItemCreateInput {
  const targetOk = Boolean(target.basic?.success && target.behavior?.success)
  const baselineOk = Boolean(baseline.basic?.success && baseline.behavior?.success)
  const comparable = targetOk && baselineOk
  const status = comparable ? 'passed' : baselineOk ? 'warning' : 'failed'
  return {
    itemKey: 'official_baseline.comparison',
    itemType: 'official_baseline',
    status,
    score: comparable ? 10 : baselineOk ? 4 : 0,
    maxScore: 10,
    durationMs: 0,
    traceId: baseline.basic?.traceId,
    evidenceSummary: {
      message: comparable ? '目标链路和官网基线链路均完成核心探针' : '官网基线对照未形成完整可比结果',
      targetTraceId: target.basic?.traceId,
      baselineTraceId: baseline.basic?.traceId,
      targetOutputPreview: bounded(target.behavior?.outputText),
      baselineOutputPreview: bounded(baseline.behavior?.outputText)
    }
  }
}

function item(
  itemKey: string,
  itemType: string,
  status: ModelCheckItemCreateInput['status'],
  score: number,
  maxScore: number,
  result: GatewayProbeResult,
  evidence: Record<string, unknown>
): ModelCheckItemCreateInput {
  const evidenceResponseModel = textValue(evidence.responseModel)
  return {
    itemKey,
    itemType,
    status,
    score,
    maxScore,
    durationMs: result.durationMs,
    traceId: result.traceId,
    evidenceSummary: {
      ...evidence,
      httpStatus: result.statusCode,
      success: result.success,
      responseModel: result.model ?? evidenceResponseModel,
      firstTokenMs: result.firstTokenMs,
      responseTruncated: result.bodyTruncated
    },
    errorCode: result.success ? undefined : `http_${result.statusCode}`,
    errorMessage: result.success ? undefined : result.errorMessage
  }
}

function summarizeChecks(checks: ModelCheckItemSummary[], options: { officialBaseline: boolean }): {
  level: 'high_confidence' | 'likely' | 'uncertain' | 'suspicious' | 'unavailable'
  score: number
  maxScore: number
  message: string
} {
  const maxScore = checks.reduce((sum, item) => sum + item.maxScore, 0)
  const rawScore = checks.reduce((sum, item) => sum + item.score, 0)
  const score = maxScore > 0 ? Math.round((rawScore / maxScore) * 100) : 0
  const failedCount = checks.filter((item) => item.status === 'failed').length
  const modelMismatchCount = checks.filter(hasModelMismatchEvidence).length
  const stabilityPassed = checks.some((item) => item.itemType === 'stability' && item.status === 'passed')
  const crossModelPassed = checks.some((item) => item.itemType === 'cross_model' && item.status === 'passed')
  const baselinePassed = !options.officialBaseline || checks.some((item) => item.itemType === 'official_baseline' && item.status === 'passed')
  if (modelMismatchCount > 0) {
    return { level: 'suspicious', score, maxScore: 100, message: '响应模型字段与请求模型不一致，目标链路疑似被替换或降级' }
  }
  if (score >= 90 && failedCount === 0 && stabilityPassed && baselinePassed && (options.officialBaseline || crossModelPassed)) {
    return {
      level: 'high_confidence',
      score,
      maxScore: 100,
      message: options.officialBaseline
        ? '目标模型链路高可信，核心协议、稳定性和官网对照均通过'
        : '目标模型链路高可信，核心协议、稳定性和交叉模型对照均通过'
    }
  }
  if (score >= 75 && failedCount <= 1) {
    return { level: 'likely', score, maxScore: 100, message: '目标模型链路较可信，仍建议结合多次检测结果观察' }
  }
  if (score >= 50) {
    return { level: 'uncertain', score, maxScore: 100, message: '目标模型链路存在不确定项，建议复查上游账号和代理配置' }
  }
  if (score >= 25) {
    return { level: 'suspicious', score, maxScore: 100, message: '目标模型链路疑似不符，多个关键探针未通过' }
  }
  return { level: 'unavailable', score, maxScore: 100, message: '目标模型链路不可检测或上游不可用' }
}

function createMemoryGatewayRequest(input: {
  method: 'GET' | 'POST'
  path: string
  body?: Record<string, unknown>
  signal?: AbortSignal
}): Request {
  const rawBody = input.body ? Buffer.from(JSON.stringify(input.body), 'utf8') : Buffer.alloc(0)
  const headers: IncomingHttpHeaders = {
    accept: input.body?.stream === true ? 'application/json, text/event-stream' : 'application/json'
  }
  if (input.body) {
    headers['content-type'] = 'application/json'
    headers['content-length'] = String(rawBody.length)
  }
  return new MemoryGatewayRequest({
    method: input.method,
    originalUrl: input.path,
    path: input.path.split('?')[0] || input.path,
    headers,
    body: input.body,
    rawBody,
    ip: '127.0.0.1',
    signal: input.signal
  }).asRequest()
}

class MemoryGatewayRequest extends EventEmitter {
  constructor(private readonly input: {
    method: string
    originalUrl: string
    path: string
    headers: IncomingHttpHeaders
    body?: Record<string, unknown>
    rawBody: Buffer
    ip: string
    signal?: AbortSignal
  }) {
    super()
    if (this.input.signal?.aborted) {
      queueMicrotask(() => this.emit('aborted'))
    } else {
      this.input.signal?.addEventListener('abort', () => this.emit('aborted'), { once: true })
    }
  }

  get method(): string {
    return this.input.method
  }

  get originalUrl(): string {
    return this.input.originalUrl
  }

  get path(): string {
    return this.input.path
  }

  get headers(): IncomingHttpHeaders {
    return this.input.headers
  }

  get body(): Record<string, unknown> | undefined {
    return this.input.body
  }

  get rawBody(): Buffer {
    return this.input.rawBody
  }

  get ip(): string {
    return this.input.ip
  }

  get socket(): { remoteAddress: string } {
    return { remoteAddress: this.input.ip }
  }

  get aborted(): boolean {
    return this.input.signal?.aborted ?? false
  }

  header(name: string): string | undefined {
    const value = this.input.headers[name.toLowerCase()]
    if (Array.isArray(value)) return value.join(', ')
    return typeof value === 'string' ? value : undefined
  }

  asRequest(): Request {
    return this as unknown as Request
  }
}

function parseJsonRecord(bodyText: string): Record<string, unknown> | undefined {
  if (!bodyText.trim() || bodyText.trimStart().startsWith('event:')) return undefined
  try {
    const parsed = JSON.parse(bodyText) as unknown
    return recordValue(parsed)
  } catch {
    return undefined
  }
}

function extractOpenAIResponseOutputText(bodyText: string): string | undefined {
  const direct = extractTextFromResponsePayload(parseJsonRecord(bodyText))
  if (direct) return direct
  const parts: string[] = []
  for (const event of parseSseEvents(bodyText)) {
    if (event.type === 'response.output_text.delta' || event.type === 'response.refusal.delta') {
      const delta = textValue(event.delta)
      if (delta) parts.push(delta)
    }
    if (event.type === 'response.output_text.done') {
      const text = textValue(event.text)
      if (text) return text
    }
    if (event.type === 'response.completed' || event.type === 'response.done') {
      const text = extractTextFromResponsePayload(recordValue(event.response))
      if (text) return text
    }
  }
  const text = parts.join('').trim()
  return text || undefined
}

function extractTextFromResponsePayload(payload?: Record<string, unknown>): string | undefined {
  const direct = textValue(payload?.output_text)
  if (direct) return direct
  const output = Array.isArray(payload?.output) ? payload.output : []
  const parts: string[] = []
  for (const item of output) {
    const content = recordValue(item)?.content
    if (!Array.isArray(content)) continue
    for (const contentItem of content) {
      const text = textValue(recordValue(contentItem)?.text)
      if (text) parts.push(text)
    }
  }
  const text = parts.join('').trim()
  return text || undefined
}

function parseOpenAIStreamFailureMessage(bodyText: string): string | undefined {
  for (const event of parseSseEvents(bodyText)) {
    const type = textValue(event.type)
    if (type !== 'response.failed' && type !== 'response.incomplete' && type !== 'error') continue
    const error = event.error ?? recordValue(event.response)?.error
    return parseErrorMessage(error) || parseErrorMessage(event) || type
  }
  return undefined
}

function parseSseEvents(bodyText: string): Array<Record<string, unknown>> {
  const events: Array<Record<string, unknown>> = []
  let eventName = ''
  let dataLines: string[] = []
  const flush = () => {
    const data = dataLines.join('\n').trim()
    const type = eventName
    eventName = ''
    dataLines = []
    if (!data || data === '[DONE]') return
    try {
      const payload = JSON.parse(data) as Record<string, unknown>
      if (type && typeof payload.type !== 'string') payload.type = type
      events.push(payload)
    } catch {
    }
  }
  for (const line of bodyText.split(/\r?\n/)) {
    if (!line) {
      flush()
    } else if (line.startsWith('event:')) {
      eventName = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      dataLines.push(line.slice(5).trimStart())
    }
  }
  flush()
  return events
}

function parseUpstreamMessage(bodyText: string): string | undefined {
  const json = parseJsonRecord(bodyText)
  const error = recordValue(json?.error)
  return textValue(error?.message) || textValue(error?.code) || textValue(json?.message) || parseOpenAIStreamFailureMessage(bodyText)
}

function parseErrorMessage(value: unknown): string | undefined {
  if (typeof value === 'string' && value.trim()) return value.trim()
  const record = recordValue(value)
  return textValue(record?.message) || textValue(record?.code) || textValue(record?.type)
}

function parseFirstJsonObject(text?: string): Record<string, unknown> | undefined {
  if (!text) return undefined
  const trimmed = text.trim()
  try {
    return recordValue(JSON.parse(trimmed))
  } catch {
    const match = trimmed.match(/\{[\s\S]*\}/)
    if (!match) return undefined
    try {
      return recordValue(JSON.parse(match[0]))
    } catch {
      return undefined
    }
  }
}

function hasFunctionCall(payload: Record<string, unknown> | undefined, name: string): boolean {
  const output = Array.isArray(payload?.output) ? payload.output : []
  return output.some((item) => {
    const record = recordValue(item)
    return record?.type === 'function_call' && record.name === name
  })
}

function usageFromSse(bodyText: string): Record<string, unknown> | undefined {
  for (const event of parseSseEvents(bodyText)) {
    const response = recordValue(event.response)
    const usage = recordValue(response?.usage)
    if (usage) return usage
  }
  return undefined
}

function modelFromSse(bodyText: string): string | undefined {
  for (const event of parseSseEvents(bodyText)) {
    const model = textValue(recordValue(event.response)?.model)
    if (model) return model
  }
  return undefined
}

function buildModelMatchEvidence(actual: unknown, expected: string): {
  expectedModel: string
  responseModel?: string
  matchedModel: boolean
  modelMismatch: boolean
} {
  const text = textValue(actual)
  const matchedModel = modelMatches(text, expected)
  return {
    expectedModel: expected,
    responseModel: text,
    matchedModel,
    modelMismatch: Boolean(text && !matchedModel)
  }
}

function describeModelMismatch(evidence: { expectedModel: string; responseModel?: string; modelMismatch: boolean }): string | undefined {
  return evidence.modelMismatch && evidence.responseModel
    ? `上游返回模型 ${evidence.responseModel}，与请求模型 ${evidence.expectedModel} 不一致`
    : undefined
}

function hasModelMismatchEvidence(item: ModelCheckItemSummary): boolean {
  return recordValue(item.evidenceSummary)?.modelMismatch === true
}

function modelMatches(actual: unknown, expected: string): boolean {
  const text = textValue(actual)
  if (!text) return false
  if (text === expected) return true
  if (!text.startsWith(`${expected}-`)) return false
  const suffix = text.slice(expected.length + 1)
  return /^\d{4}-\d{2}-\d{2}(?:$|[._-])/.test(suffix)
}

function normalizeModel(value: unknown): 'gpt-5.5' | 'gpt-5.4' | undefined {
  const text = textValue(value)
  if (!text) return undefined
  return supportedModelSet.has(text) ? text as 'gpt-5.5' | 'gpt-5.4' : undefined
}

function modelCheckTargetTypeValue(value: unknown): ModelCheckTargetType | undefined {
  return value === 'api_key' || value === 'group' || value === 'account' ? value : undefined
}

function modelCheckLevelValue(value: unknown): 'high_confidence' | 'likely' | 'uncertain' | 'suspicious' | 'unavailable' | undefined {
  return value === 'high_confidence' || value === 'likely' || value === 'uncertain' || value === 'suspicious' || value === 'unavailable' ? value : undefined
}

function modelCheckStatusValue(value: unknown): ModelCheckRunStatus | undefined {
  return value === 'running' || value === 'completed' || value === 'failed' || value === 'canceled' ? value : undefined
}

function integerValue(value: unknown): number | undefined {
  const raw = Array.isArray(value) ? value[0] : value
  const numeric = typeof raw === 'number' ? raw : typeof raw === 'string' ? Number.parseInt(raw, 10) : NaN
  return Number.isFinite(numeric) ? Math.trunc(numeric) : undefined
}

function textValue(value: unknown): string | undefined {
  const raw = Array.isArray(value) ? value[0] : value
  return typeof raw === 'string' && raw.trim() ? raw.trim() : undefined
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : undefined
}

function numberValue(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function bounded(value?: string): string | undefined {
  if (!value) return undefined
  const text = value.trim()
  return text.length > 160 ? `${text.slice(0, 160)}...` : text
}

function throwIfAborted(signal?: AbortSignal): void {
  if (signal?.aborted) {
    throw new Error('模型检测已取消')
  }
}
