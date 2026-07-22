import { createTraceId } from '../../shared/request-context.js'
import type { ModelCheckItemCreateInput } from '../../storage/model-checks.repository.js'
import type { ModelCheckObservationInput } from '../../storage/model-trust.repository.js'
import { recordValue } from './model-checks-parsing.js'
import { createModelCheckProbeRequest, type ModelCheckProbeRequest } from './model-checks.payloads.js'
import type { GatewayProbeResult } from './model-checks-evaluation.js'
import {
  analyzeTokenIntegritySamples,
  modelCheckTokenizerVersion,
  modelCheckTokenProbeVersion,
  type TokenIntegritySample
} from './model-checks-token-integrity.js'
import { prepareModelCheckTokenProbePromptInWorker } from './model-checks-token-worker.service.js'
import {
  modelCheckCohortKey,
  modelCheckObservationHmac,
  normalizedUpstreamOrigin
} from './model-checks-observation-security.js'

export type TokenIntegrityObservationSeed = Omit<ModelCheckObservationInput,
  'runId' | 'systemAccountId' | 'accountId' | 'providerCode' | 'providerProtocolProfileId'
  | 'identityStatus' | 'mappingStatus' | 'protocolStatus' | 'evidenceCoverage'>

export async function executeModelCheckTokenIntegrityProbes(input: {
  model: string
  providerCode: string
  providerProtocolProfileId: string
  baseUrl: string
  credentialMode: string
  probeSetVersion: string
  signal?: AbortSignal
  runProbe: (request: ModelCheckProbeRequest, itemKey: string) => Promise<GatewayProbeResult>
}): Promise<{ item: ModelCheckItemCreateInput; observations: TokenIntegrityObservationSeed[] }> {
  const upstreamBucketHmac = modelCheckObservationHmac(normalizedUpstreamOrigin(input.baseUrl), 'upstream')
  const samples: TokenIntegritySample[] = []
  const observations: TokenIntegrityObservationSeed[] = []
  let representative: GatewayProbeResult | undefined
  for (let roundIndex = 0; roundIndex < 3; roundIndex += 1) {
    const nonce = createTraceId().replace(/[^a-zA-Z0-9]/g, '').slice(-16)
    const basePrompt = `Controlled token integrity probe ${modelCheckTokenProbeVersion}. Nonce ${nonce}. Reply with exactly OK.\n`
    for (const paddingTokens of paddingVariants(roundIndex)) {
      const preparedPrompt = await prepareModelCheckTokenProbePromptInWorker(basePrompt, paddingTokens, input.signal)
      const prompt = preparedPrompt.prompt
      const request = createModelCheckProbeRequest('openai_responses', input.model, prompt, {
        maxOutputTokens: 8,
        stream: false,
        temperature: 0
      })
      const result = await input.runProbe(request, `target.token_integrity.r${roundIndex}.p${paddingTokens}`)
      representative ??= result
      const localInputTokens = preparedPrompt.localInputTokens
      const reportedInputTokens = usageValue(result.usage, ['input_tokens', 'prompt_tokens'])
      const cachedInputTokens = cachedUsageValue(result.usage)
      samples.push({ roundIndex, paddingTokens, localInputTokens, reportedInputTokens, cachedInputTokens })
      const mappedUpstreamModel = result.upstreamModel ?? result.expectedModel ?? input.model
      const cohortKey = modelCheckCohortKey({
        providerCode: input.providerCode,
        providerProtocolProfileId: input.providerProtocolProfileId,
        endpointFamily: result.upstreamEndpointFamily ?? result.sourceEndpointFamily ?? 'responses',
        credentialMode: input.credentialMode,
        mappedUpstreamModel,
        probeSetVersion: input.probeSetVersion,
        tokenizerVersion: modelCheckTokenizerVersion
      })
      observations.push({
        endpointFamily: result.upstreamEndpointFamily ?? result.sourceEndpointFamily ?? 'responses',
        requestedModel: result.requestModel ?? input.model,
        mappedUpstreamModel,
        observedModel: result.model,
        mappingApplied: result.modelMappingApplied === true,
        upstreamBucketHmac,
        cohortKeyHmac: modelCheckObservationHmac(cohortKey, 'cohort'),
        populationKeyHmac: modelCheckObservationHmac(cohortKey, 'cohort'),
        probeKeyHmac: modelCheckObservationHmac(`${modelCheckTokenProbeVersion}:${roundIndex}:${paddingTokens}`, 'probe'),
        systemFingerprintHmac: result.systemFingerprint
          ? modelCheckObservationHmac(result.systemFingerprint, 'fingerprint')
          : undefined,
        probeFamily: 'token_input_differential',
        probeSetVersion: input.probeSetVersion,
        tokenizerVersion: modelCheckTokenizerVersion,
        featureVersion: 'none',
        roundIndex,
        paddingTokens,
        localInputTokens,
        reportedInputTokens,
        cachedInputTokens,
        observationStatus: tokenObservationStatus(result, reportedInputTokens),
        traceId: result.traceId
      })
    }
  }
  const analysis = analyzeTokenIntegritySamples(samples)
  return {
    item: {
      itemKey: 'target.token_integrity',
      itemType: 'token_integrity',
      status: analysis.status === 'suspected_padding' ? 'failed' : analysis.status === 'consistent' ? 'passed' : analysis.status === 'unsupported' ? 'skipped' : 'warning',
      score: 0,
      maxScore: 0,
      traceId: representative?.traceId,
      evidenceSummary: {
        message: tokenIntegrityMessage(analysis.status),
        diagnosticOnly: true,
        tokenizerVersion: modelCheckTokenizerVersion,
        probeVersion: modelCheckTokenProbeVersion,
        slope: analysis.slope,
        intercept: analysis.intercept,
        slopeConfidenceLow: analysis.slopeConfidenceLow,
        slopeConfidenceHigh: analysis.slopeConfidenceHigh,
        sampleCount: analysis.sampleCount,
        roundCount: analysis.roundCount,
        reasonCodes: analysis.reasonCodes
      }
    },
    observations
  }
}

function tokenObservationStatus(result: GatewayProbeResult, reportedInputTokens: number | undefined): string {
  if (!result.success) return 'request_failed'
  if (!result.model?.trim()) return 'model_missing'
  return reportedInputTokens === undefined ? 'usage_missing' : 'observed'
}

function paddingVariants(roundIndex: number): number[] {
  const orders = [[0, 512, 2048], [2048, 0, 512], [512, 2048, 0]] as const
  return [...(orders[roundIndex % orders.length] ?? orders[0])]
}

function usageValue(usage: Record<string, unknown> | undefined, keys: string[]): number | undefined {
  if (!usage) return undefined
  for (const key of keys) {
    const value = Number(usage[key])
    if (Number.isFinite(value) && value >= 0) return Math.trunc(value)
  }
  return undefined
}

function cachedUsageValue(usage: Record<string, unknown> | undefined): number | undefined {
  const details = recordValue(usage?.input_tokens_details) ?? recordValue(usage?.prompt_tokens_details)
  return usageValue(details, ['cached_tokens'])
}

function tokenIntegrityMessage(status: string): string {
  if (status === 'consistent') return '受控差分 Token 探针未发现比例或分桶异常'
  if (status === 'suspected_padding') return '受控差分 Token 探针发现疑似比例灌水'
  if (status === 'warning') return '受控差分 Token 探针存在需要继续校准的异常'
  return '上游 usage 不完整或不兼容，暂不支持形成 Token 诚信结论'
}
