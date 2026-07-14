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

export const modelIdentityFeatureVersion = 'identity-features-v1'
export const modelIdentityProbeVersion = 'generated-canary-v1'
export const modelIdentityFeatureCount = 8

const gpt56Models = ['gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna'] as const
const gpt55Models = ['gpt-5.5', 'gpt-5.4'] as const

type GeneratedCanary = {
  key: string
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
      featureVector: extractIdentityFeatureVector(result.outputText ?? '', result.usage, constraintPassed),
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
      featureVector: extractIdentityFeatureVector(output, result.usage, constraintPassed),
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
  const text = output.trim().slice(0, 4096)
  const chars = [...text]
  const length = Math.max(1, chars.length)
  const lines = text ? text.split(/\r?\n/).filter(Boolean).length : 0
  const digits = chars.filter((char) => /[0-9]/.test(char)).length
  const uppercase = chars.filter((char) => /[A-Z]/.test(char)).length
  const punctuation = chars.filter((char) => /[.,!?;:，。！？；：{}[\]"'`|\-]/.test(char)).length
  const unique = new Set(chars.map((char) => char.toLowerCase())).size
  const outputTokens = usageValue(usage, ['output_tokens', 'completion_tokens']) ?? countModelCheckInputTokens(text)
  return [
    constraintPassed ? 1 : 0,
    boundedRatio(chars.length, 512),
    boundedRatio(lines, 8),
    digits / length,
    uppercase / length,
    punctuation / length,
    unique / length,
    boundedRatio(outputTokens, 256)
  ].map(roundFeature)
}

function generatedCanaries(nonce: string): GeneratedCanary[] {
  const left = 20 + randomInt(40)
  const right = 10 + randomInt(30)
  const values = [2 + randomInt(8), 11 + randomInt(9), 21 + randomInt(9)]
  const tag = `CANARY-${nonce.slice(0, 6).toUpperCase()}`
  return [
    {
      key: 'generated_json',
      maxOutputTokens: 80,
      prompt: `只输出严格 JSON：{"result":数字,"tag":"${tag}"}。result 等于 ${left} + ${right}。`,
      passed: (output) => {
        const json = parseFirstJsonObject(output)
        return json?.tag === tag && numberValue(json.result) === left + right
      }
    },
    {
      key: 'generated_sequence',
      maxOutputTokens: 48,
      prompt: `只输出 ${tag} 后跟数字从小到大排序并用竖线连接：${[...values].reverse().join('、')}。`,
      passed: (output) => output.toUpperCase().includes(tag) && output.includes([...values].sort((a, b) => a - b).join('|'))
    },
    {
      key: 'generated_zh_constraint',
      maxOutputTokens: 96,
      prompt: `用 24 到 48 个中文字符解释请求排队，必须包含“队列”和“超时”，末尾添加 ${tag}，不要分点。`,
      passed: (output) => output.includes('队列') && output.includes('超时') && output.toUpperCase().includes(tag)
    }
  ]
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
