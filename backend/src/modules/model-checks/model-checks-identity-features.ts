import { randomInt } from 'node:crypto'

import { createTraceId } from '../../shared/request-context.js'
import type { ModelCheckItemCreateInput } from '../../storage/model-checks.repository.js'
import type { ModelCheckObservationInput } from '../../storage/model-trust.repository.js'
import { numberValue, parseFirstJsonObject } from './model-checks-parsing.js'
import { createModelCheckProbeRequest, type ModelCheckProbeRequest } from './model-checks.payloads.js'
import type { BehaviorProbeObservation, GatewayProbeResult } from './model-checks-evaluation.js'
import { behaviorConstraintPassed } from './model-checks.probes.js'
import { countModelCheckInputTokens, modelCheckTokenizerVersion } from './model-checks-token-integrity.js'
import {
  modelCheckCohortKey,
  modelCheckObservationHmac,
  modelCheckPopulationKey,
  normalizedUpstreamOrigin
} from './model-checks-observation-security.js'

export const modelIdentityFeatureVersion = 'identity-features-v2-seven-categories'
export const modelIdentityProbeVersion = 'generated-canary-v2-seven-categories'
export const modelIdentityFeatureCount = 8

export const modelIdentityFeatureCategories = [
  'constraint',
  'code',
  'reasoning',
  'error_recovery',
  'multilingual',
  'tool_schema',
  'knowledge_window'
] as const

export type ModelIdentityFeatureCategory = typeof modelIdentityFeatureCategories[number]

const gpt56Models = ['gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna'] as const
const gpt55Models = ['gpt-5.5', 'gpt-5.4'] as const

type GeneratedCanary = {
  key: string
  category: ModelIdentityFeatureCategory
  prompt: string
  maxOutputTokens: number
  passed: (output: string) => boolean
}

export type IdentityObservationSeed = Omit<ModelCheckObservationInput,
  'runId' | 'systemAccountId' | 'accountId' | 'providerCode' | 'providerProtocolProfileId'
  | 'identityStatus' | 'mappingStatus' | 'protocolStatus' | 'evidenceCoverage'>

export function pairedIdentityModels(model: string): string[] {
  if ((gpt56Models as readonly string[]).includes(model)) return [...gpt56Models]
  if ((gpt55Models as readonly string[]).includes(model)) return [...gpt55Models]
  return [model]
}

export function createControlledBehaviorObservations(input: {
  model: string
  providerCode: string
  providerProtocolProfileId: string
  baseUrl: string
  credentialMode: string
  probeSetVersion: string
  observations: BehaviorProbeObservation[]
}): IdentityObservationSeed[] {
  const upstreamBucketHmac = modelCheckObservationHmac(normalizedUpstreamOrigin(input.baseUrl), 'upstream')
  return input.observations.map(({ definition, result }, index) => {
    const mappedUpstreamModel = result.upstreamModel ?? result.expectedModel ?? input.model
    const endpointFamily = result.upstreamEndpointFamily ?? result.sourceEndpointFamily ?? 'responses'
    const cohortKey = modelCheckCohortKey({
      providerCode: input.providerCode,
      providerProtocolProfileId: input.providerProtocolProfileId,
      endpointFamily,
      credentialMode: input.credentialMode,
      mappedUpstreamModel,
      probeSetVersion: input.probeSetVersion,
      tokenizerVersion: modelCheckTokenizerVersion
    })
    const populationKey = modelCheckPopulationKey({
      providerCode: input.providerCode,
      providerProtocolProfileId: input.providerProtocolProfileId,
      endpointFamily,
      credentialMode: input.credentialMode,
      probeSetVersion: input.probeSetVersion,
      featureVersion: modelIdentityFeatureVersion
    })
    const constraintPassed = result.success && behaviorConstraintPassed(definition, result.outputText ?? '')
    return {
      endpointFamily,
      requestedModel: result.requestModel ?? input.model,
      mappedUpstreamModel,
      observedModel: result.model,
      mappingApplied: result.modelMappingApplied === true,
      upstreamBucketHmac,
      cohortKeyHmac: modelCheckObservationHmac(cohortKey, 'cohort'),
      populationKeyHmac: modelCheckObservationHmac(populationKey, 'population'),
      probeKeyHmac: modelCheckObservationHmac(`controlled-behavior-v1:${definition.key}:${index}`, 'probe'),
      systemFingerprintHmac: result.systemFingerprint ? modelCheckObservationHmac(result.systemFingerprint, 'fingerprint') : undefined,
      probeFamily: 'identity_controlled_behavior',
      probeSetVersion: input.probeSetVersion,
      tokenizerVersion: modelCheckTokenizerVersion,
      featureVersion: modelIdentityFeatureVersion,
      roundIndex: 0,
      paddingTokens: 0,
      localInputTokens: countModelCheckInputTokens(definition.prompt),
      reportedInputTokens: usageValue(result.usage, ['input_tokens', 'prompt_tokens']),
      observationStatus: identityObservationStatus(result),
      constraintPassed,
      featureVector: extractStructuredIdentityFeatureVector(
        controlledBehaviorCategory(definition.key),
        result.outputText ?? '',
        result.usage,
        constraintPassed
      ),
      traceId: result.traceId
    }
  })
}

export async function executeModelIdentityObservationProbes(input: {
  model: string
  providerCode: string
  providerProtocolProfileId: string
  baseUrl: string
  credentialMode: string
  probeSetVersion: string
  runProbe: (request: ModelCheckProbeRequest, itemKey: string) => Promise<GatewayProbeResult>
}): Promise<{ item: ModelCheckItemCreateInput; observations: IdentityObservationSeed[] }> {
  const nonce = createTraceId().replace(/[^a-zA-Z0-9]/g, '').slice(-12)
  const definitions = generatedCanaries(nonce)
  const models = pairedIdentityModels(input.model)
  const jobs = shuffled(definitions.flatMap((definition) => models.map((model) => ({ definition, model }))))
  const upstreamBucketHmac = modelCheckObservationHmac(normalizedUpstreamOrigin(input.baseUrl), 'upstream')
  const observations: IdentityObservationSeed[] = []
  let successCount = 0
  let passedCount = 0
  for (let index = 0; index < jobs.length; index += 1) {
    const job = jobs[index] as { definition: GeneratedCanary; model: string }
    const request = createModelCheckProbeRequest('openai_responses', job.model, job.definition.prompt, {
      maxOutputTokens: job.definition.maxOutputTokens,
      stream: false,
      temperature: 0
    })
    const result = await input.runProbe(request, `target.identity.${job.definition.key}.${job.model}.${index}`)
    const output = result.outputText ?? ''
    const constraintPassed = result.success && job.definition.passed(output)
    if (result.success) successCount += 1
    if (constraintPassed) passedCount += 1
    const mappedUpstreamModel = result.upstreamModel ?? result.expectedModel ?? job.model
    const endpointFamily = result.upstreamEndpointFamily ?? result.sourceEndpointFamily ?? 'responses'
    const cohortKey = modelCheckCohortKey({
      providerCode: input.providerCode,
      providerProtocolProfileId: input.providerProtocolProfileId,
      endpointFamily,
      credentialMode: input.credentialMode,
      mappedUpstreamModel,
      probeSetVersion: input.probeSetVersion,
      tokenizerVersion: modelCheckTokenizerVersion
    })
    const populationKey = modelCheckPopulationKey({
      providerCode: input.providerCode,
      providerProtocolProfileId: input.providerProtocolProfileId,
      endpointFamily,
      credentialMode: input.credentialMode,
      probeSetVersion: input.probeSetVersion,
      featureVersion: modelIdentityFeatureVersion
    })
    observations.push({
      endpointFamily,
      requestedModel: result.requestModel ?? job.model,
      mappedUpstreamModel,
      observedModel: result.model,
      mappingApplied: result.modelMappingApplied === true,
      upstreamBucketHmac,
      cohortKeyHmac: modelCheckObservationHmac(cohortKey, 'cohort'),
      populationKeyHmac: modelCheckObservationHmac(populationKey, 'population'),
      probeKeyHmac: modelCheckObservationHmac(`${modelIdentityProbeVersion}:${job.definition.key}`, 'probe'),
      systemFingerprintHmac: result.systemFingerprint
        ? modelCheckObservationHmac(result.systemFingerprint, 'fingerprint')
        : undefined,
      probeFamily: 'identity_generated_canary',
      probeSetVersion: input.probeSetVersion,
      tokenizerVersion: modelCheckTokenizerVersion,
      featureVersion: modelIdentityFeatureVersion,
      roundIndex: 0,
      paddingTokens: 0,
      localInputTokens: countModelCheckInputTokens(job.definition.prompt),
      reportedInputTokens: usageValue(result.usage, ['input_tokens', 'prompt_tokens']),
      cachedInputTokens: undefined,
      observationStatus: identityObservationStatus(result),
      constraintPassed,
      featureVector: extractStructuredIdentityFeatureVector(job.definition.category, output, result.usage, constraintPassed),
      traceId: result.traceId
    })
  }
  return {
    item: {
      itemKey: 'target.identity_observation',
      itemType: 'identity_observation',
      status: successCount === jobs.length ? 'passed' : successCount > 0 ? 'warning' : 'skipped',
      score: 0,
      maxScore: 0,
      evidenceSummary: {
        message: successCount === jobs.length ? '受控生成式身份 observation 已采集' : '受控生成式身份 observation 仅部分采集',
        diagnosticOnly: true,
        featureVersion: modelIdentityFeatureVersion,
        probeVersion: modelIdentityProbeVersion,
        modelCount: models.length,
        probeCount: definitions.length,
        observationCount: jobs.length,
        successCount,
        constraintPassedCount: passedCount
      }
    },
    observations
  }
}

function identityObservationStatus(result: GatewayProbeResult): string {
  if (!result.success) return 'request_failed'
  return result.model?.trim() ? 'observed' : 'model_missing'
}

export function extractIdentityFeatureVector(output: string, usage: Record<string, unknown> | undefined, constraintPassed: boolean): number[] {
  return extractStructuredIdentityFeatureVector('constraint', output, usage, constraintPassed)
}

export function extractStructuredIdentityFeatureVector(
  category: ModelIdentityFeatureCategory,
  output: string,
  usage: Record<string, unknown> | undefined,
  constraintPassed: boolean
): number[] {
  const text = output.trim().slice(0, 4096)
  const outputTokens = usageValue(usage, ['output_tokens', 'completion_tokens']) ?? countModelCheckInputTokens(text)
  const vector = Array.from({ length: modelIdentityFeatureCount }, () => 0)
  const categoryIndex = modelIdentityFeatureCategories.indexOf(category)
  vector[categoryIndex] = constraintPassed ? 1 : 0
  vector[7] = boundedRatio(outputTokens, 256)
  return vector.map(roundFeature)
}

function generatedCanaries(nonce: string): GeneratedCanary[] {
  const left = 20 + randomInt(40)
  const right = 10 + randomInt(30)
  const values = [2 + randomInt(8), 11 + randomInt(9), 21 + randomInt(9)]
  const tag = `CANARY-${nonce.slice(0, 6).toUpperCase()}`
  return [
    {
      key: 'constraint_json',
      category: 'constraint',
      maxOutputTokens: 80,
      prompt: `只输出严格 JSON：{"result":数字,"tag":"${tag}"}。result 等于 ${left} + ${right}。`,
      passed: (output) => {
        const json = parseFirstJsonObject(output)
        return json?.tag === tag && numberValue(json.result) === left + right
      }
    },
    {
      key: 'code_patch',
      category: 'code',
      maxOutputTokens: 80,
      prompt: `只输出一行 TypeScript 表达式，把 [${values.join(',')}] 过滤为大于 ${values[0]} 的值并升序，不要解释；行尾注释必须是 ${tag}。`,
      passed: (output) => output.includes('.filter(') && output.includes('.sort(') && output.toUpperCase().includes(tag)
    },
    {
      key: 'reasoning_order',
      category: 'reasoning',
      maxOutputTokens: 64,
      prompt: `只输出严格 JSON：{"largest":数字,"tag":"${tag}"}。largest 是 ${values.join('、')} 中第二大值加 ${left - right}。`,
      passed: (output) => {
        const json = parseFirstJsonObject(output)
        const sorted = [...values].sort((a, b) => b - a)
        return json?.tag === tag && numberValue(json.largest) === (sorted[1] as number) + left - right
      }
    },
    {
      key: 'error_recovery',
      category: 'error_recovery',
      maxOutputTokens: 64,
      prompt: `中间结论错误地声称 ${left}+${right}=${left + right + 1}。请纠正，只输出严格 JSON：{"correct":数字,"tag":"${tag}"}。`,
      passed: (output) => {
        const json = parseFirstJsonObject(output)
        return json?.tag === tag && numberValue(json.correct) === left + right
      }
    },
    {
      key: 'multilingual_consistency',
      category: 'multilingual',
      maxOutputTokens: 80,
      prompt: `“队列超时”和 "queue timeout" 表达同一概念。只输出严格 JSON：{"zh":"队列超时","en":"queue timeout","tag":"${tag}"}。`,
      passed: (output) => {
        const json = parseFirstJsonObject(output)
        return json?.zh === '队列超时' && json?.en === 'queue timeout' && json?.tag === tag
      }
    },
    {
      key: 'tool_schema',
      category: 'tool_schema',
      maxOutputTokens: 96,
      prompt: `按工具参数 schema 生成且只输出 JSON：必填 action 枚举只能是 "inspect"，payload 必须含 ids 数组 [${values.join(',')}] 和布尔值 dryRun=true，tag="${tag}"。`,
      passed: (output) => {
        const json = parseFirstJsonObject(output)
        const payload = json?.payload as Record<string, unknown> | undefined
        return json?.action === 'inspect' && json?.tag === tag && payload?.dryRun === true
          && Array.isArray(payload.ids) && payload.ids.map(Number).join(',') === values.join(',')
      }
    },
    {
      key: 'knowledge_window',
      category: 'knowledge_window',
      maxOutputTokens: 64,
      prompt: `封闭时间线：2024-01 版本=A，2024-06 版本=B，2025-01 版本=C。知识截止 2024-10，不得使用截止后信息。只输出严格 JSON：{"version":"B","tag":"${tag}"}。`,
      passed: (output) => {
        const json = parseFirstJsonObject(output)
        return json?.version === 'B' && json?.tag === tag
      }
    }
  ]
}

function controlledBehaviorCategory(key: string): ModelIdentityFeatureCategory {
  if (key.includes('code')) return 'code'
  if (key.includes('logic') || key.includes('arithmetic') || key.includes('ordering')) return 'reasoning'
  if (key.includes('refusal') || key.includes('priority')) return 'error_recovery'
  if (key.includes('zh')) return 'multilingual'
  if (key.includes('tool') || key.includes('schema')) return 'tool_schema'
  if (key.includes('knowledge') || key.includes('window')) return 'knowledge_window'
  return 'constraint'
}

function shuffled<T>(values: T[]): T[] {
  const result = [...values]
  for (let index = result.length - 1; index > 0; index -= 1) {
    const target = randomInt(index + 1)
    ;[result[index], result[target]] = [result[target] as T, result[index] as T]
  }
  return result
}

function usageValue(usage: Record<string, unknown> | undefined, keys: string[]): number | undefined {
  if (!usage) return undefined
  for (const key of keys) {
    const value = Number(usage[key])
    if (Number.isFinite(value) && value >= 0) return Math.trunc(value)
  }
  return undefined
}

function boundedRatio(value: number, maximum: number): number {
  return Math.max(0, Math.min(1, value / maximum))
}

function roundFeature(value: number): number {
  return Math.round(Math.max(0, Math.min(1, Number.isFinite(value) ? value : 0)) * 1_000_000) / 1_000_000
}
