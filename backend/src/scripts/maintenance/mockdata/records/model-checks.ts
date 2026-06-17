import type { AccountSummary, GroupSummary, SystemAccountSummary } from '../../../../domain/types.js'
import * as repositories from '../../../../storage/repositories.js'
import {
  dayMs,
  idPrefix,
  minuteMs,
  providerCode,
  tracePrefix,
  type ApiKeyWithSecret,
  type CreatedMockdata,
  type MockdataOptions
} from '../shared.js'
import { authorizationInstanceAccount } from '../core/account-helpers.js'

export interface ModelCheckMockdataCounts {
  runs: number
  items: number
}

type MockModelCheckLevel = 'high_confidence' | 'likely' | 'uncertain' | 'suspicious' | 'unavailable'
type MockModelCheckRunStatus = 'running' | 'completed' | 'failed' | 'canceled'
type MockModelCheckItemStatus = 'passed' | 'warning' | 'failed' | 'skipped'

interface ModelCheckTargetSeed {
  account: AccountSummary
  group: GroupSummary
  apiKey: ApiKeyWithSecret
  actor: SystemAccountSummary
  comparisonAccount?: AccountSummary
}

export function createModelCheckMockdata(created: CreatedMockdata, options: MockdataOptions): ModelCheckMockdataCounts {
  const devPrimaryInstance = authorizationInstanceAccount(created.accounts.primary, created.users.dev)
  const testerPrimaryInstance = authorizationInstanceAccount(created.accounts.primary, created.users.tester)
  const opsProxiedInstance = authorizationInstanceAccount(created.accounts.proxied, created.users.ops)
  const financeOauthInstance = authorizationInstanceAccount(created.accounts.oauth, created.users.finance)
  const viewerBurstImageInstance = authorizationInstanceAccount(created.accounts.burstImage, created.users.viewer)
  const targets: ModelCheckTargetSeed[] = [
    { account: created.accounts.primary, group: created.groups.main, apiKey: created.apiKeys.adminMain, actor: created.users.admin, comparisonAccount: created.accounts.oauth },
    { account: created.accounts.burstFast, group: created.groups.highConcurrency, apiKey: created.apiKeys.adminHighConcurrency, actor: created.users.admin, comparisonAccount: created.accounts.burstImage },
    { account: devPrimaryInstance, group: created.groups.devDefault, apiKey: created.apiKeys.devGroupAuthorized, actor: created.users.dev },
    { account: created.accounts.normal, group: created.groups.main, apiKey: created.apiKeys.adminHighFrequency, actor: created.users.admin, comparisonAccount: created.accounts.burstFast },
    { account: testerPrimaryInstance, group: created.groups.testerDefault, apiKey: created.apiKeys.testerTeamAuthorized, actor: created.users.tester },
    { account: opsProxiedInstance, group: created.groups.opsDefault, apiKey: created.apiKeys.opsAccountAuthorized, actor: created.users.ops },
    { account: created.accounts.oauth, group: created.groups.oauth, apiKey: created.apiKeys.adminOAuth, actor: created.users.admin, comparisonAccount: created.accounts.oauthBackup },
    { account: created.accounts.oauthBackup, group: created.groups.oauth, apiKey: created.apiKeys.adminOAuth, actor: created.users.admin, comparisonAccount: created.accounts.oauth },
    { account: financeOauthInstance, group: created.groups.financeDefault, apiKey: created.apiKeys.financeAuthorized, actor: created.users.finance },
    { account: viewerBurstImageInstance, group: created.groups.viewerDefault, apiKey: created.apiKeys.viewerAuthorized, actor: created.users.viewer },
    { account: created.accounts.rateLimited, group: created.groups.backup, apiKey: created.apiKeys.adminBackup, actor: created.users.admin, comparisonAccount: created.accounts.primary },
    { account: created.accounts.temporary, group: created.groups.experiment, apiKey: created.apiKeys.adminExpired, actor: created.users.admin, comparisonAccount: created.accounts.normal }
  ]
  const runCount = Math.min(120, Math.max(36, options.days))
  let itemCount = 0

  for (let index = 0; index < runCount; index += 1) {
    const target = targets[index % targets.length]
    const model = index % 3 === 0 ? 'gpt-5.5' : 'gpt-5.4'
    const runStatus = modelCheckRunStatusForIndex(index)
    const trustedComparison = Boolean(target.comparisonAccount) && index % 3 === 0
    const startedAtMs = Date.now() - 20 * minuteMs - Math.floor((index / Math.max(1, runCount - 1)) * options.days * dayMs)
    const startedAt = new Date(startedAtMs).toISOString()
    const runId = `${idPrefix}model_check_run_${String(index + 1).padStart(4, '0')}`
    const traceId = `${tracePrefix}model-check-${String(index + 1).padStart(4, '0')}`
    const checks = buildModelCheckItems({
      runIndex: index,
      runId,
      model,
      startedAtMs,
      trustedComparison,
      runStatus
    })
    const level = modelCheckLevelForRun(index, runStatus, checks.score, checks.maxScore)
    const message = modelCheckRunMessage(runStatus, level, checks.score, checks.maxScore)

    repositories.createModelCheckRun({
      id: runId,
      systemAccountId: target.actor.id,
      actorSystemAccountId: target.actor.id,
      providerCode,
      targetType: 'account',
      targetId: target.account.id,
      targetName: target.account.name,
      targetOwnerSystemAccountId: target.account.ownerSystemAccountId ?? target.account.systemAccountId ?? created.users.admin.id,
      accountId: target.account.id,
      groupId: target.group.id,
      apiKeyId: target.apiKey.id,
      model,
      profile: 'full',
      trustedComparison,
      trustedComparisonAvailable: trustedComparison && index % 4 !== 0,
      traceId,
      probeSetVersion: 'openai-model-check-v1',
      startedAt,
      requestSummary: {
        targetType: 'account',
        targetId: target.account.id,
        targetName: target.account.name,
        model,
        profile: 'full',
        trustedComparison,
        trustedComparisonAccountId: trustedComparison ? target.comparisonAccount?.id : undefined,
        trustedComparisonAccountName: trustedComparison ? target.comparisonAccount?.name : undefined,
        groupId: target.group.id,
        groupName: target.group.name,
        apiKeyId: target.apiKey.id,
        actorSystemAccountId: target.actor.id,
        generatedBy: 'mockdata'
      }
    })
    repositories.createModelCheckItems(runId, checks.items)
    itemCount += checks.items.length

    if (runStatus !== 'running') {
      const durationMs = checks.items.reduce((sum, item) => sum + (item.durationMs ?? 0), 0)
      repositories.finishModelCheckRun(runId, {
        level,
        score: checks.score,
        maxScore: checks.maxScore,
        status: runStatus,
        message,
        finishedAt: new Date(startedAtMs + durationMs + 800).toISOString(),
        durationMs: durationMs + 800,
        resultSummary: {
          verdict: message,
          passedItems: checks.items.filter((item) => item.status === 'passed').length,
          warningItems: checks.items.filter((item) => item.status === 'warning').length,
          failedItems: checks.items.filter((item) => item.status === 'failed').length,
          skippedItems: checks.items.filter((item) => item.status === 'skipped').length,
          trustedComparison,
          generatedBy: 'mockdata'
        },
        errorCode: runStatus === 'failed' ? 'mockdata_model_check_failed' : undefined,
        errorMessage: runStatus === 'failed' ? 'Mockdata 模拟上游探针失败' : undefined
      })
    }
  }

  return {
    runs: runCount,
    items: itemCount
  }
}

function buildModelCheckItems(input: {
  runIndex: number
  runId: string
  model: 'gpt-5.5' | 'gpt-5.4'
  startedAtMs: number
  trustedComparison: boolean
  runStatus: MockModelCheckRunStatus
}): {
  items: Array<{
    id: string
    itemKey: string
    itemType: string
    status: MockModelCheckItemStatus
    score: number
    maxScore: number
    durationMs: number
    traceId: string
    evidenceSummary: Record<string, unknown>
    errorCode?: string
    errorMessage?: string
    createdAt: string
  }>
  score: number
  maxScore: number
} {
  const definitions = [
    ['target.model_catalog', 'model_catalog', 10],
    ['target.responses_basic', 'responses_basic', 15],
    ['target.responses_stream', 'responses_stream', 15],
    ['target.structured_output', 'structured_output', 15],
    ['target.tool_calling', 'tool_calling', 10],
    ['target.behavior_probe', 'behavior_probe', 15],
    ['target.long_context', 'long_context', 10],
    ['target.stability_a', 'stability', 10],
    ...(input.trustedComparison ? [['trusted_comparison.comparison', 'trusted_comparison', 10] as const] : [])
  ] as const
  const items = definitions.map(([itemKey, itemType, maxScore], itemIndex) => {
    let status: MockModelCheckItemStatus = 'passed'
    let score: number = maxScore
    let errorCode: string | undefined
    let errorMessage: string | undefined
    if (input.runStatus === 'running' && itemIndex > 1) {
      status = 'skipped'
      score = 0
    } else if (input.runStatus === 'canceled' && itemIndex > 3) {
      status = 'skipped'
      score = 0
    } else if (input.runStatus === 'failed' && itemIndex === 2) {
      status = 'failed'
      score = 0
      errorCode = 'mockdata_probe_failed'
      errorMessage = 'Mockdata 模拟流式探针响应中断'
    } else if ((input.runIndex + itemIndex) % 17 === 0) {
      status = 'failed'
      score = Math.max(0, Math.floor(maxScore * 0.35))
      errorCode = 'mockdata_low_similarity'
      errorMessage = 'Mockdata 模拟输出特征偏离可信基线'
    } else if ((input.runIndex + itemIndex) % 7 === 0) {
      status = 'warning'
      score = Math.max(0, maxScore - 4)
    }
    const durationMs = 420 + ((input.runIndex + 1) * (itemIndex + 3) * 137) % 2600
    const createdAt = new Date(input.startedAtMs + (itemIndex + 1) * 1200).toISOString()
    const traceId = `${tracePrefix}model-check-${String(input.runIndex + 1).padStart(4, '0')}-${String(itemIndex + 1).padStart(2, '0')}`
    return {
      id: `${idPrefix}model_check_item_${String(input.runIndex + 1).padStart(4, '0')}_${String(itemIndex + 1).padStart(2, '0')}`,
      itemKey,
      itemType,
      status,
      score,
      maxScore,
      durationMs,
      traceId,
      evidenceSummary: {
        message: modelCheckItemMessage(status, itemType),
        responseModel: status === 'failed' ? 'unknown' : input.model,
        statusCode: status === 'failed' ? 502 : 200,
        latencyMs: durationMs,
        sample: `mockdata-${itemType}-${input.runIndex + 1}`
      },
      errorCode,
      errorMessage,
      createdAt
    }
  })
  return {
    items,
    score: items.reduce((sum, item) => sum + item.score, 0),
    maxScore: items.reduce((sum, item) => sum + item.maxScore, 0)
  }
}

function modelCheckRunStatusForIndex(index: number): MockModelCheckRunStatus {
  if (index % 41 === 0) return 'running'
  if (index % 29 === 0) return 'canceled'
  if (index % 13 === 0) return 'failed'
  return 'completed'
}

function modelCheckLevelForScore(score: number, maxScore: number): MockModelCheckLevel {
  const ratio = maxScore > 0 ? score / maxScore : 0
  if (ratio >= 0.92) return 'high_confidence'
  if (ratio >= 0.78) return 'likely'
  if (ratio >= 0.58) return 'uncertain'
  if (ratio > 0) return 'suspicious'
  return 'unavailable'
}

function modelCheckLevelForRun(index: number, status: MockModelCheckRunStatus, score: number, maxScore: number): MockModelCheckLevel {
  if (status !== 'completed') return 'unavailable'
  const base = modelCheckLevelForScore(score, maxScore)
  if (index % 12 === 0 && base !== 'high_confidence') return 'likely'
  if (index % 10 === 0 && base === 'high_confidence') return 'uncertain'
  return base
}

function modelCheckRunMessage(status: MockModelCheckRunStatus, level: MockModelCheckLevel, score: number, maxScore: number): string {
  if (status === 'running') return 'Mockdata 模拟检测仍在运行，等待后续探针完成'
  if (status === 'failed') return 'Mockdata 模拟检测失败：流式探针响应中断'
  if (status === 'canceled') return 'Mockdata 模拟检测已手动停止'
  const labels: Record<MockModelCheckLevel, string> = {
    high_confidence: '高可信',
    likely: '较可信',
    uncertain: '需复核',
    suspicious: '疑似异常',
    unavailable: '不可用'
  }
  return `Mockdata 检测完成：${labels[level]}，得分 ${score}/${maxScore}`
}

function modelCheckItemMessage(status: MockModelCheckItemStatus, itemType: string): string {
  if (status === 'passed') return `Mockdata ${itemType} 探针通过`
  if (status === 'warning') return `Mockdata ${itemType} 探针存在轻微偏差`
  if (status === 'failed') return `Mockdata ${itemType} 探针失败`
  return `Mockdata ${itemType} 探针已跳过`
}
